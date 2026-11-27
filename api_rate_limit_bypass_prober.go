// Package task probes an API endpoint for rate limit key assumptions and
// for dimension switches that escape the bucket.
package task

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Variant dimensions produced by Variants, in fixed order.
const (
	DimSourceHeader   = "source_header"
	DimIdentity       = "identity"
	DimPathCase       = "path_case"
	DimTrailingSlash  = "trailing_slash"
	DimMethodOverride = "method_override"
	DimQueryOrder     = "query_order"
)

// Key structures inferred by Judge.
const (
	StructureShared      = "shared"
	StructureIndependent = "independent"
	StructureGlobal      = "global"
)

// Errors reported by the prober.
var (
	ErrBadEndpoint     = errors.New("endpoint name and path are required")
	ErrBadBurst        = errors.New("burst size must be positive")
	ErrNoTransport     = errors.New("transport is not installed")
	ErrNoLimitObserved = errors.New("no throttling observed during the burst")
	ErrUnknownVariant  = errors.New("variant was not produced for this profile")
	ErrVariantProbed   = errors.New("variant has already been probed")
	ErrBudgetExhausted = errors.New("request budget exhausted")
	ErrBackoffActive   = errors.New("retry-after backoff is still active")
	ErrNoObservations  = errors.New("no observation to judge")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

const (
	snapshotFormat  = "api-rate-limit-bypass-prober"
	snapshotVersion = 1
)

// Endpoint is one API entry point under test.
type Endpoint struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Request is one probe request handed to the transport.
type Request struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

// Response is what the transport reports back.
type Response struct {
	Status  int               `json:"status"`
	Length  int               `json:"length"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Transport performs one request against the target.
type Transport interface {
	Do(req Request) (Response, error)
}

// Clock supplies the probe timestamps.
type Clock interface {
	Now() int64
}

// LimitProfile is what Baseline learned about the endpoint.
type LimitProfile struct {
	Endpoint string `json:"endpoint"`
	Path     string `json:"path"`
	Limit    int    `json:"limit"`
	Probes   int    `json:"probes"`
	Blocked  bool   `json:"blocked"`
}

// BurstResult summarises one burst of requests.
type BurstResult struct {
	Requests   int `json:"requests"`
	Allowed    int `json:"allowed"`
	Blocked    int `json:"blocked"`
	LastStatus int `json:"last_status"`
}

// BypassVariant is one mutated request that may escape the bucket.
type BypassVariant struct {
	ID        string            `json:"id"`
	Dimension string            `json:"dimension"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	Headers   map[string]string `json:"headers"`
}

// Observation is the outcome of probing one variant once.
type Observation struct {
	VariantID string `json:"variant_id"`
	Dimension string `json:"dimension"`
	Status    int    `json:"status"`
	Allowed   bool   `json:"allowed"`
	At        int64  `json:"at"`
}

// BypassVerdict is the judgement over a set of observations.
type BypassVerdict struct {
	Structure string   `json:"structure"`
	Bypassed  []string `json:"bypassed"`
	Blocked   []string `json:"blocked"`
	Reason    string   `json:"reason"`
}

// BudgetState reports how many probe requests were spent.
type BudgetState struct {
	Used      int `json:"used"`
	Limit     int `json:"limit"`
	Remaining int `json:"remaining"`
}

// ProberConfig bounds the prober.
type ProberConfig struct {
	BaselineBurst int
	MaxRequests   int
	BackoffMs     int64
}

// ApiRateLimitBypassProber owns the transport, the budget and the probes.
type ApiRateLimitBypassProber struct {
	cfg       ProberConfig
	clock     Clock
	transport Transport

	// mu serialises requests as well as state changes. Holding it for the
	// duration of a request makes reservation of the budget and the
	// once-per-variant guarantee atomic under concurrency.
	mu sync.Mutex

	used         int
	profile      LimitProfile
	structure    string
	variants     map[string]BypassVariant
	probed       map[string]Observation
	observations []Observation
	backoffUntil int64
}

type snapshotPayload struct {
	Format         string          `json:"format"`
	Version        int             `json:"version"`
	Profile        LimitProfile    `json:"profile"`
	Structure      string          `json:"structure"`
	Variants       []BypassVariant `json:"variants"`
	ProbedVariants []BypassVariant `json:"probed_variants"`
	Observations   []Observation   `json:"observations"`
	Budget         BudgetState     `json:"budget"`
	BackoffUntil   int64           `json:"backoff_until"`
	Checksum       string          `json:"checksum"`
}

// NewApiRateLimitBypassProber builds a prober with the given limits.
func NewApiRateLimitBypassProber(cfg ProberConfig) *ApiRateLimitBypassProber {
	return &ApiRateLimitBypassProber{
		cfg:      cfg,
		variants: make(map[string]BypassVariant),
		probed:   make(map[string]Observation),
	}
}

// SetTransport installs the transport used for every request.
func (p *ApiRateLimitBypassProber) SetTransport(t Transport) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.transport = t
	return nil
}

// SetClock installs the clock used for probe timestamps.
func (p *ApiRateLimitBypassProber) SetClock(c Clock) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clock = c
	return nil
}

// Baseline measures how many requests pass before the first 429.
func (p *ApiRateLimitBypassProber) Baseline(endpoint Endpoint) (LimitProfile, error) {
	if endpoint.Name == "" || endpoint.Path == "" {
		return LimitProfile{}, ErrBadEndpoint
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.transport == nil {
		return LimitProfile{}, ErrNoTransport
	}
	if p.backoffUntil > p.nowLocked() {
		return LimitProfile{}, ErrBackoffActive
	}
	if p.budgetLimitLocked() == 0 {
		return LimitProfile{}, ErrBudgetExhausted
	}

	burst := p.cfg.BaselineBurst
	if burst <= 0 {
		return LimitProfile{}, ErrNoLimitObserved
	}

	profile := LimitProfile{Endpoint: endpoint.Name, Path: endpoint.Path}
	allowed := 0
	for i := 0; i < burst; i++ {
		if p.used >= p.budgetLimitLocked() {
			return profile, ErrBudgetExhausted
		}
		p.used++
		profile.Probes++

		resp, err := p.transport.Do(requestFromEndpoint(endpoint))
		if resp.Status == 429 {
			p.observe429Locked(resp.Headers)
		}
		if err != nil {
			return profile, err
		}
		if resp.Status == 429 {
			profile.Limit = allowed
			profile.Blocked = true
			p.profile = profile
			return profile, nil
		}
		if statusAllowed(resp.Status) {
			allowed++
		}
	}

	// A burst without any rejection says only that no limit was observed. It
	// must never be interpreted as evidence of an unlimited bucket.
	return LimitProfile{}, ErrNoLimitObserved
}

// Burst sends up to n requests and stops at the first rejection.
func (p *ApiRateLimitBypassProber) Burst(endpoint Endpoint, n int) (BurstResult, error) {
	if n <= 0 {
		return BurstResult{}, ErrBadBurst
	}
	if endpoint.Name == "" || endpoint.Path == "" {
		return BurstResult{}, ErrBadEndpoint
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.transport == nil {
		return BurstResult{}, ErrNoTransport
	}
	if p.backoffUntil > p.nowLocked() {
		return BurstResult{}, ErrBackoffActive
	}

	var result BurstResult
	for i := 0; i < n; i++ {
		if p.used >= p.budgetLimitLocked() {
			return result, ErrBudgetExhausted
		}
		p.used++
		result.Requests++

		resp, err := p.transport.Do(requestFromEndpoint(endpoint))
		result.LastStatus = resp.Status
		if resp.Status == 429 {
			p.observe429Locked(resp.Headers)
		}
		if err != nil {
			result.Blocked++
			return result, err
		}
		if resp.Status == 429 {
			result.Blocked++
			return result, nil
		}
		if statusAllowed(resp.Status) {
			result.Allowed++
		} else {
			result.Blocked++
		}
	}
	return result, nil
}

// Variants derives the bypass variants for one profile.
func (p *ApiRateLimitBypassProber) Variants(profile LimitProfile) ([]BypassVariant, error) {
	// LimitProfile intentionally carries no method. Login probes are the
	// primary use case here, so POST is the stable default used by the
	// generated requests; the method-override dimension changes it explicitly.
	const baseMethod = "POST"

	vs := []BypassVariant{
		{
			ID:        variantID(profile, DimSourceHeader),
			Dimension: DimSourceHeader,
			Method:    baseMethod,
			Path:      profile.Path,
			Headers: map[string]string{
				"X-Forwarded-For": "198.51.100.10",
				"X-Real-IP":       "198.51.100.10",
				"Forwarded":       "for=198.51.100.10",
			},
		},
		{
			ID:        variantID(profile, DimIdentity),
			Dimension: DimIdentity,
			Method:    baseMethod,
			Path:      profile.Path,
			Headers: map[string]string{
				"Authorization": "Bearer bypass-probe",
				"Cookie":        "session=bypass-probe",
				"X-Account-ID":  "bypass-probe",
			},
		},
		{
			ID:        variantID(profile, DimPathCase),
			Dimension: DimPathCase,
			Method:    baseMethod,
			Path:      uppercasePath(profile.Path),
			Headers:   map[string]string{},
		},
		{
			ID:        variantID(profile, DimTrailingSlash),
			Dimension: DimTrailingSlash,
			Method:    baseMethod,
			Path:      alternateTrailingSlash(profile.Path),
			Headers:   map[string]string{},
		},
		{
			ID:        variantID(profile, DimMethodOverride),
			Dimension: DimMethodOverride,
			Method:    overrideMethod(baseMethod),
			Path:      profile.Path,
			Headers: map[string]string{
				"X-HTTP-Method-Override": baseMethod,
			},
		},
		{
			ID:        variantID(profile, DimQueryOrder),
			Dimension: DimQueryOrder,
			Method:    baseMethod,
			Path:      reorderQuery(profile.Path),
			Headers:   map[string]string{},
		},
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.variants == nil {
		p.variants = make(map[string]BypassVariant)
	}
	for _, variant := range vs {
		p.variants[probedKey(variant.ID, variant.Dimension)] = cloneVariant(variant)
	}
	p.profile = profile

	out := make([]BypassVariant, len(vs))
	for i := range vs {
		out[i] = cloneVariant(vs[i])
	}
	return out, nil
}

// Probe sends one variant exactly once and records the outcome.
func (p *ApiRateLimitBypassProber) Probe(variant BypassVariant) (Observation, error) {
	key := probedKey(variant.ID, variant.Dimension)

	p.mu.Lock()
	defer p.mu.Unlock()

	registered, ok := p.variants[key]
	if !ok {
		return Observation{}, ErrUnknownVariant
	}
	if _, ok := p.probed[key]; ok {
		return Observation{}, ErrVariantProbed
	}
	if p.transport == nil {
		return Observation{}, ErrNoTransport
	}
	if p.used >= p.budgetLimitLocked() {
		return Observation{}, ErrBudgetExhausted
	}
	if p.backoffUntil > p.nowLocked() {
		return Observation{}, ErrBackoffActive
	}

	at := p.nowLocked()
	p.used++
	resp, err := p.transport.Do(Request{
		Method:  registered.Method,
		Path:    registered.Path,
		Headers: cloneHeaders(registered.Headers),
	})
	if resp.Status == 429 {
		p.observe429Locked(resp.Headers)
	}

	obs := Observation{
		VariantID: registered.ID,
		Dimension: registered.Dimension,
		Status:    resp.Status,
		Allowed:   err == nil && statusAllowed(resp.Status),
		At:        at,
	}
	p.probed[key] = obs
	p.observations = append(p.observations, obs)
	return obs, err
}

// Judge folds the observations into one key structure verdict.
func (p *ApiRateLimitBypassProber) Judge(obs []Observation) (BypassVerdict, error) {
	if len(obs) == 0 {
		return BypassVerdict{}, ErrNoObservations
	}

	allowed := make(map[string]bool)
	blocked := make(map[string]bool)
	identityAllowed := false
	for _, observation := range obs {
		isAllowed := statusAllowed(observation.Status)
		if isAllowed {
			allowed[observation.VariantID] = true
			delete(blocked, observation.VariantID)
			if observation.Dimension == DimIdentity {
				identityAllowed = true
			}
		} else if !allowed[observation.VariantID] {
			blocked[observation.VariantID] = true
		}
	}

	verdict := BypassVerdict{
		Bypassed: make([]string, 0, len(allowed)),
		Blocked:  make([]string, 0, len(blocked)),
	}
	for id := range allowed {
		verdict.Bypassed = append(verdict.Bypassed, id)
	}
	for id := range blocked {
		verdict.Blocked = append(verdict.Blocked, id)
	}
	sort.Strings(verdict.Bypassed)
	sort.Strings(verdict.Blocked)

	switch {
	case len(verdict.Bypassed) == 0:
		verdict.Structure = StructureGlobal
		verdict.Reason = fmt.Sprintf("all %d observed variants were blocked", len(verdict.Blocked))
	case identityAllowed:
		verdict.Structure = StructureIndependent
		verdict.Reason = fmt.Sprintf("identity dimension was allowed while %d variants remained blocked", len(verdict.Blocked))
	default:
		verdict.Structure = StructureShared
		verdict.Reason = fmt.Sprintf("%d non-identity variants were allowed by the same limit", len(verdict.Bypassed))
	}

	p.mu.Lock()
	p.structure = verdict.Structure
	p.mu.Unlock()
	return verdict, nil
}

// Budget reports how much of the request budget is still available.
func (p *ApiRateLimitBypassProber) Budget() (BudgetState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	limit := p.budgetLimitLocked()
	remaining := limit - p.used
	if remaining < 0 {
		remaining = 0
	}
	return BudgetState{Used: p.used, Limit: limit, Remaining: remaining}, nil
}

// Observations returns a copy of every recorded probe outcome.
func (p *ApiRateLimitBypassProber) Observations() []Observation {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Observation(nil), p.observations...)
}

// Snapshot serialises the profile, the probed variants and the budget.
func (p *ApiRateLimitBypassProber) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	variants := make([]BypassVariant, 0, len(p.variants))
	for _, variant := range p.variants {
		variants = append(variants, cloneVariant(variant))
	}
	sort.Slice(variants, func(i, j int) bool {
		if variants[i].ID == variants[j].ID {
			return variants[i].Dimension < variants[j].Dimension
		}
		return variants[i].ID < variants[j].ID
	})

	probedVariants := make([]BypassVariant, 0, len(p.probed))
	for key := range p.probed {
		if variant, ok := p.variants[key]; ok {
			probedVariants = append(probedVariants, cloneVariant(variant))
		}
	}
	sort.Slice(probedVariants, func(i, j int) bool {
		if probedVariants[i].ID == probedVariants[j].ID {
			return probedVariants[i].Dimension < probedVariants[j].Dimension
		}
		return probedVariants[i].ID < probedVariants[j].ID
	})

	limit := p.budgetLimitLocked()
	remaining := limit - p.used
	if remaining < 0 {
		remaining = 0
	}
	payload := snapshotPayload{
		Format:         snapshotFormat,
		Version:        snapshotVersion,
		Profile:        p.profile,
		Structure:      p.structure,
		Variants:       variants,
		ProbedVariants: probedVariants,
		Observations:   append([]Observation(nil), p.observations...),
		Budget:         BudgetState{Used: p.used, Limit: limit, Remaining: remaining},
		BackoffUntil:   p.backoffUntil,
	}
	checksum, err := payload.digest()
	if err != nil {
		return nil, err
	}
	payload.Checksum = checksum
	return json.Marshal(payload)
}

// Restore replaces the whole prober state, all or nothing.
func (p *ApiRateLimitBypassProber) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Format != snapshotFormat || payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	checksum := payload.Checksum
	payload.Checksum = ""
	expected, err := payload.digest()
	if err != nil || checksum == "" || checksum != expected {
		return ErrBadSnapshot
	}
	if payload.Budget.Limit < 0 || payload.Budget.Used < 0 || payload.Budget.Used > payload.Budget.Limit {
		return ErrBadSnapshot
	}
	if payload.Budget.Remaining != payload.Budget.Limit-payload.Budget.Used {
		return ErrBadSnapshot
	}
	if payload.BackoffUntil < 0 {
		return ErrBadSnapshot
	}
	if payload.Structure != "" && payload.Structure != StructureShared &&
		payload.Structure != StructureIndependent && payload.Structure != StructureGlobal {
		return ErrBadSnapshot
	}

	variants := make(map[string]BypassVariant, len(payload.Variants))
	for _, variant := range payload.Variants {
		if variant.ID == "" || !knownDimension(variant.Dimension) {
			return ErrBadSnapshot
		}
		key := probedKey(variant.ID, variant.Dimension)
		if _, exists := variants[key]; exists {
			return ErrBadSnapshot
		}
		variants[key] = cloneVariant(variant)
	}

	probed := make(map[string]Observation, len(payload.ProbedVariants))
	for _, variant := range payload.ProbedVariants {
		if variant.ID == "" || !knownDimension(variant.Dimension) {
			return ErrBadSnapshot
		}
		key := probedKey(variant.ID, variant.Dimension)
		if _, exists := variants[key]; !exists {
			variants[key] = cloneVariant(variant)
		}
		probed[key] = Observation{VariantID: variant.ID, Dimension: variant.Dimension}
	}

	for _, observation := range payload.Observations {
		if observation.VariantID == "" || !knownDimension(observation.Dimension) {
			return ErrBadSnapshot
		}
		key := probedKey(observation.VariantID, observation.Dimension)
		if _, exists := variants[key]; !exists {
			return ErrBadSnapshot
		}
		probed[key] = observation
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.MaxRequests = payload.Budget.Limit
	p.used = payload.Budget.Used
	p.profile = payload.Profile
	p.structure = payload.Structure
	p.variants = variants
	p.probed = probed
	p.observations = append([]Observation(nil), payload.Observations...)
	p.backoffUntil = payload.BackoffUntil
	return nil
}

func (payload snapshotPayload) digest() (string, error) {
	payload.Checksum = ""
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum), nil
}

func (p *ApiRateLimitBypassProber) budgetLimitLocked() int {
	if p.cfg.MaxRequests < 0 {
		return 0
	}
	return p.cfg.MaxRequests
}

func (p *ApiRateLimitBypassProber) nowLocked() int64 {
	if p.clock != nil {
		return p.clock.Now()
	}
	return time.Now().UnixMilli()
}

func (p *ApiRateLimitBypassProber) observe429Locked(headers map[string]string) {
	now := p.nowLocked()
	waitMs := p.cfg.BackoffMs
	if raw, ok := headerValue(headers, "Retry-After"); ok {
		if parsed, valid := retryAfterMillis(raw); valid {
			waitMs = parsed
		}
	}
	if waitMs < 0 {
		waitMs = 0
	}
	if waitMs == 0 {
		return
	}
	until := now + waitMs
	if until > p.backoffUntil {
		p.backoffUntil = until
	}
}

func requestFromEndpoint(endpoint Endpoint) Request {
	return Request{
		Method:  endpoint.Method,
		Path:    endpoint.Path,
		Headers: cloneHeaders(endpoint.Headers),
	}
}

func statusAllowed(status int) bool {
	return status >= 200 && status < 400
}

func variantID(profile LimitProfile, dimension string) string {
	return profile.Endpoint + "/" + dimension
}

func probedKey(id, dimension string) string {
	return id + "\x00" + dimension
}

func knownDimension(dimension string) bool {
	switch dimension {
	case DimSourceHeader, DimIdentity, DimPathCase, DimTrailingSlash, DimMethodOverride, DimQueryOrder:
		return true
	default:
		return false
	}
}

func cloneHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		out[key] = value
	}
	return out
}

func cloneVariant(variant BypassVariant) BypassVariant {
	variant.Headers = cloneHeaders(variant.Headers)
	return variant
}

func uppercasePath(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return strings.ToUpper(path[:i]) + path[i:]
	}
	return strings.ToUpper(path)
}

func alternateTrailingSlash(path string) string {
	i := strings.IndexByte(path, '?')
	head, suffix := path, ""
	if i >= 0 {
		head, suffix = path[:i], path[i:]
	}
	if strings.HasSuffix(head, "/") {
		if len(head) > 1 {
			head = strings.TrimSuffix(head, "/")
		} else {
			head = "//"
		}
	} else {
		head += "/"
	}
	return head + suffix
}

func overrideMethod(method string) string {
	if strings.EqualFold(method, "GET") {
		return "POST"
	}
	return "GET"
}

func reorderQuery(path string) string {
	i := strings.IndexByte(path, '?')
	if i < 0 {
		return path + "?b=2&a=1"
	}
	head, raw := path[:i], path[i+1:]
	if raw == "" {
		return head + "?b=2&a=1"
	}
	parts := strings.Split(raw, "&")
	if len(parts) < 2 {
		return head + "?" + raw + "&probe=1"
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return head + "?" + strings.Join(parts, "&")
}

func headerValue(headers map[string]string, name string) (string, bool) {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

func retryAfterMillis(raw string) (int64, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0, true
		}
		return seconds * 1000, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delta := time.Until(when).Milliseconds()
		if delta <= 0 {
			return 0, true
		}
		return delta, true
	}
	return 0, false
}
