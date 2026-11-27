// Package task discovers API version surfaces and grades whether an older
// version of a resource is weaker than the version the service advertises.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Version carriers.
const (
	CarrierPath   = "path"
	CarrierHeader = "header"
	CarrierQuery  = "query"
)

// Response buckets written to Observation.Class.
const (
	StatusOK       = "ok"
	StatusAuthGate = "auth_gate"
	StatusNotFound = "not_found"
	StatusServer   = "server_error"
)

// Verdict kinds returned by Judge.
const (
	VerdictDowngraded   = "downgraded"
	VerdictConsistent   = "consistent"
	VerdictUndetermined = "undetermined"
)

// Reason codes attached to a downgrade verdict.
const (
	ReasonAuthGap          = "reason_auth_gap"
	ReasonAuthParity       = "reason_auth_parity"
	ReasonFieldCut         = "reason_field_cut"
	ReasonRouteUnconfirmed = "reason_route_unconfirmed"
	ReasonServerError      = "reason_server_error"
	ReasonStable           = "reason_stable"
)

// Errors reported by the prober.
var (
	ErrEmptyBase      = errors.New("base address is empty")
	ErrNoResources    = errors.New("schema declares no resources")
	ErrNoVersions     = errors.New("schema declares no version tokens")
	ErrBadSchema      = errors.New("schema default version is not declared")
	ErrNilTransport   = errors.New("transport is nil")
	ErrBadSurface     = errors.New("version surface is incomplete")
	ErrUnknownCarrier = errors.New("version carrier is not supported")
	ErrProbeBudget    = errors.New("probe budget is exhausted")
	ErrCircuitOpen    = errors.New("circuit breaker is open")
	ErrRateLimited    = errors.New("prober is rate limited")
	ErrNoObservations = errors.New("no observations to align")
	ErrNoBaseline     = errors.New("baseline observation is missing")
	ErrBadDiff        = errors.New("version diff is incomplete")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Schema is the declared version layout of one service.
type Schema struct {
	Versions       []string `json:"versions"`
	Resources      []string `json:"resources"`
	DefaultVersion string   `json:"default_version"`
	HeaderName     string   `json:"header_name"`
	QueryName      string   `json:"query_name"`
}

// VersionSurface is one addressable entry point of one version of a resource.
type VersionSurface struct {
	ID          string `json:"id"`
	Resource    string `json:"resource"`
	Version     string `json:"version"`
	Carrier     string `json:"carrier"`
	Path        string `json:"path"`
	HeaderName  string `json:"header_name"`
	HeaderValue string `json:"header_value"`
	QueryName   string `json:"query_name"`
	QueryValue  string `json:"query_value"`
	Default     bool   `json:"default"`
}

// Request is the caller shaped request that is replayed against a surface.
type Request struct {
	Method string `json:"method"`
	Auth   bool   `json:"auth"`
	Body   string `json:"body"`
}

// Response is the raw answer of one exchange.
type Response struct {
	Status    int      `json:"status"`
	Fields    []string `json:"fields"`
	ErrorCode string   `json:"error_code"`
}

// Transport performs one exchange against one version surface.
type Transport interface {
	Do(surface VersionSurface, req Request) (Response, error)
}

// Observation is the bucketed record of one effective probe.
type Observation struct {
	SurfaceID string   `json:"surface_id"`
	Resource  string   `json:"resource"`
	Version   string   `json:"version"`
	Carrier   string   `json:"carrier"`
	Status    int      `json:"status"`
	Class     string   `json:"class"`
	Fields    []string `json:"fields"`
	ErrorCode string   `json:"error_code"`
	Cached    bool     `json:"cached"`
	Default   bool     `json:"default"`
}

// VersionDiff is the field level difference between a baseline and a candidate.
type VersionDiff struct {
	Resource        string   `json:"resource"`
	Baseline        string   `json:"baseline"`
	Candidate       string   `json:"candidate"`
	MissingFields   []string `json:"missing_fields"`
	AddedFields     []string `json:"added_fields"`
	PatternMismatch bool     `json:"pattern_mismatch"`
	BaselineClass   string   `json:"baseline_class"`
	CandidateClass  string   `json:"candidate_class"`
}

// DowngradeVerdict is the explainable result of one difference.
type DowngradeVerdict struct {
	Resource   string `json:"resource"`
	Candidate  string `json:"candidate"`
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason"`
	Conclusive bool   `json:"conclusive"`
	AuthGap    bool   `json:"auth_gap"`
}

// BudgetState reports how much probing room is left.
type BudgetState struct {
	Probes      int  `json:"probes"`
	MaxProbes   int  `json:"max_probes"`
	Remaining   int  `json:"remaining"`
	RateLimited bool `json:"rate_limited"`
	CircuitOpen bool `json:"circuit_open"`
}

// Limits bounds probing.
type Limits struct {
	MaxProbes                  int
	MaxConsecutiveServerErrors int
}

const snapshotMagic = "gsb/api-version-downgrade/v1"

type snapshotState struct {
	Magic                      string           `json:"magic"`
	Surfaces                   []VersionSurface `json:"surfaces"`
	Ledger                     []Observation    `json:"ledger"`
	Probes                     int              `json:"probes"`
	MaxProbes                  int              `json:"max_probes"`
	MaxConsecutiveServerErrors int              `json:"max_consecutive_server_errors"`
	ConsecutiveServerErrors    int              `json:"consecutive_server_errors"`
	CircuitOpen                bool             `json:"circuit_open"`
	RateLimited                bool             `json:"rate_limited"`
}

// ApiVersionDowngradeProber owns the surface table, the probe ledger and the
// breaker state.
type ApiVersionDowngradeProber struct {
	mu                      sync.Mutex
	tr                      Transport
	limits                  Limits
	surfaces                []VersionSurface
	ledger                  map[string]Observation
	probes                  int
	consecutiveServerErrors int
	circuitOpen             bool
	rateLimited             bool
}

// NewApiVersionDowngradeProber builds a prober on the given transport.
func NewApiVersionDowngradeProber(tr Transport, limits Limits) *ApiVersionDowngradeProber {
	return &ApiVersionDowngradeProber{
		tr:     tr,
		limits: limits,
		ledger: make(map[string]Observation),
	}
}

// Discover expands the schema into resource/version/carrier surfaces.
func (p *ApiVersionDowngradeProber) Discover(base string, spec Schema) ([]VersionSurface, error) {
	cleanBase := strings.TrimSpace(base)
	if cleanBase == "" {
		return nil, ErrEmptyBase
	}

	resources := uniqueNonBlank(spec.Resources)
	if len(resources) == 0 {
		return nil, ErrNoResources
	}
	versions := uniqueNonBlank(spec.Versions)
	if len(versions) == 0 {
		return nil, ErrNoVersions
	}

	defaultVersion := strings.TrimSpace(spec.DefaultVersion)
	if defaultVersion == "" {
		defaultVersion = versions[len(versions)-1]
	} else if !containsString(versions, defaultVersion) {
		return nil, ErrBadSchema
	}

	joinedBase := strings.TrimRight(cleanBase, "/")
	carriers := []string{CarrierPath, CarrierHeader, CarrierQuery}
	surfaces := make([]VersionSurface, 0, len(resources)*len(versions)*len(carriers))
	seen := make(map[string]struct{}, cap(surfaces))
	for _, resource := range resources {
		for _, version := range versions {
			for _, carrier := range carriers {
				surface := VersionSurface{
					ID:       fmt.Sprintf("%s|%s|%s", resource, version, carrier),
					Resource: resource,
					Version:  version,
					Carrier:  carrier,
					Default:  version == defaultVersion,
				}
				switch carrier {
				case CarrierPath:
					if joinedBase == "" {
						surface.Path = fmt.Sprintf("/%s/%s", version, resource)
					} else {
						surface.Path = fmt.Sprintf("%s/%s/%s", joinedBase, version, resource)
					}
				case CarrierHeader:
					surface.HeaderName = spec.HeaderName
					surface.HeaderValue = version
				case CarrierQuery:
					surface.QueryName = spec.QueryName
					surface.QueryValue = version
				}
				if _, exists := seen[surface.ID]; exists {
					continue
				}
				seen[surface.ID] = struct{}{}
				surfaces = append(surfaces, surface)
			}
		}
	}

	sort.Slice(surfaces, func(i, j int) bool { return surfaces[i].ID < surfaces[j].ID })
	registered := cloneSurfaces(surfaces)

	p.mu.Lock()
	p.surfaces = registered
	p.mu.Unlock()

	return cloneSurfaces(surfaces), nil
}

// Probe performs at most one effective exchange per version surface.
func (p *ApiVersionDowngradeProber) Probe(surface VersionSurface, req Request) (Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.tr == nil {
		return Observation{}, ErrNilTransport
	}
	if strings.TrimSpace(surface.ID) == "" ||
		strings.TrimSpace(surface.Resource) == "" ||
		strings.TrimSpace(surface.Version) == "" {
		return Observation{}, ErrBadSurface
	}
	switch surface.Carrier {
	case CarrierPath, CarrierHeader, CarrierQuery:
	default:
		return Observation{}, ErrUnknownCarrier
	}

	key := probeKey(surface.ID, surface.Resource)
	if cached, ok := p.ledger[key]; ok {
		cached.Cached = true
		return cloneObservation(cached), nil
	}

	if p.rateLimited {
		return Observation{}, ErrRateLimited
	}
	if p.circuitOpen {
		return Observation{}, ErrCircuitOpen
	}
	if p.limits.MaxProbes <= 0 || p.probes >= p.limits.MaxProbes {
		return Observation{}, ErrProbeBudget
	}

	p.probes++
	response, err := p.tr.Do(surface, req)
	if err != nil {
		return Observation{}, err
	}

	observation := Observation{
		SurfaceID: surface.ID,
		Resource:  surface.Resource,
		Version:   surface.Version,
		Carrier:   surface.Carrier,
		Status:    response.Status,
		Class:     statusClass(response.Status),
		Fields:    normaliseFields(response.Fields),
		ErrorCode: response.ErrorCode,
		Default:   surface.Default,
	}
	p.ledger[key] = cloneObservation(observation)

	if observation.Class == StatusServer {
		p.consecutiveServerErrors++
		if p.limits.MaxConsecutiveServerErrors > 0 &&
			p.consecutiveServerErrors >= p.limits.MaxConsecutiveServerErrors {
			p.circuitOpen = true
		}
	} else {
		p.consecutiveServerErrors = 0
	}

	return cloneObservation(observation), nil
}

// Align groups observations by resource and diffs each candidate with the baseline.
func (p *ApiVersionDowngradeProber) Align(obs []Observation) ([]VersionDiff, error) {
	if len(obs) == 0 {
		return nil, ErrNoObservations
	}

	groups := make(map[string][]Observation)
	for _, observation := range obs {
		groups[observation.Resource] = append(groups[observation.Resource], cloneObservation(observation))
	}

	resourceNames := make([]string, 0, len(groups))
	for resource := range groups {
		resourceNames = append(resourceNames, resource)
	}
	sort.Strings(resourceNames)

	diffs := make([]VersionDiff, 0)
	for _, resource := range resourceNames {
		group := groups[resource]
		baselineIndex := findBaseline(group)
		if baselineIndex < 0 {
			return nil, ErrNoBaseline
		}
		baseline := group[baselineIndex]

		candidates := make([]Observation, 0, len(group)-1)
		for i, observation := range group {
			if i != baselineIndex {
				candidates = append(candidates, observation)
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].SurfaceID != candidates[j].SurfaceID {
				return candidates[i].SurfaceID < candidates[j].SurfaceID
			}
			return candidates[i].Version < candidates[j].Version
		})

		for _, candidate := range candidates {
			diffs = append(diffs, buildDiff(resource, baseline, candidate))
		}
	}

	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].Resource != diffs[j].Resource {
			return diffs[i].Resource < diffs[j].Resource
		}
		return diffs[i].Candidate < diffs[j].Candidate
	})
	return diffs, nil
}

// Judge grades one difference as a downgrade, consistent or undetermined.
func (p *ApiVersionDowngradeProber) Judge(diff VersionDiff) (DowngradeVerdict, error) {
	if strings.TrimSpace(diff.Candidate) == "" {
		return DowngradeVerdict{}, ErrBadDiff
	}

	verdict := DowngradeVerdict{
		Resource:  diff.Resource,
		Candidate: diff.Candidate,
	}
	switch {
	case diff.BaselineClass == StatusAuthGate && diff.CandidateClass == StatusOK:
		verdict.Verdict = VerdictDowngraded
		verdict.Reason = ReasonAuthGap
		verdict.Conclusive = true
		verdict.AuthGap = true
	case diff.CandidateClass == StatusServer:
		verdict.Verdict = VerdictUndetermined
		verdict.Reason = ReasonServerError
	case len(diff.MissingFields) > 0 || len(diff.AddedFields) > 0 || diff.PatternMismatch:
		verdict.Verdict = VerdictUndetermined
		verdict.Reason = ReasonFieldCut
	case diff.BaselineClass == StatusAuthGate && diff.CandidateClass == StatusAuthGate:
		verdict.Verdict = VerdictConsistent
		verdict.Reason = ReasonAuthParity
		verdict.Conclusive = true
	default:
		verdict.Verdict = VerdictConsistent
		verdict.Reason = ReasonStable
		verdict.Conclusive = true
	}
	return verdict, nil
}

// Budget reports the probe budget and the breaker state.
func (p *ApiVersionDowngradeProber) Budget() (BudgetState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	remaining := p.limits.MaxProbes - p.probes
	if remaining < 0 {
		remaining = 0
	}
	return BudgetState{
		Probes:      p.probes,
		MaxProbes:   p.limits.MaxProbes,
		Remaining:   remaining,
		RateLimited: p.rateLimited,
		CircuitOpen: p.circuitOpen,
	}, nil
}

// SetRateLimited turns the caller driven rate limit on or off.
func (p *ApiVersionDowngradeProber) SetRateLimited(limited bool) {
	p.mu.Lock()
	p.rateLimited = limited
	p.mu.Unlock()
}

// Surfaces returns the discovered surfaces ordered by identifier.
func (p *ApiVersionDowngradeProber) Surfaces() []VersionSurface {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneSurfaces(p.surfaces)
}

// Observation reads one ledger entry.
func (p *ApiVersionDowngradeProber) Observation(surfaceID, resource string) (Observation, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	observation, ok := p.ledger[probeKey(surfaceID, resource)]
	if !ok {
		return Observation{}, false
	}
	return cloneObservation(observation), true
}

// Snapshot serialises surfaces, the probe ledger, budget and breaker state.
func (p *ApiVersionDowngradeProber) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ledger := make([]Observation, 0, len(p.ledger))
	for _, observation := range p.ledger {
		ledger = append(ledger, cloneObservation(observation))
	}
	sort.Slice(ledger, func(i, j int) bool {
		if ledger[i].SurfaceID != ledger[j].SurfaceID {
			return ledger[i].SurfaceID < ledger[j].SurfaceID
		}
		return ledger[i].Resource < ledger[j].Resource
	})

	state := snapshotState{
		Magic:                      snapshotMagic,
		Surfaces:                   cloneSurfaces(p.surfaces),
		Ledger:                     ledger,
		Probes:                     p.probes,
		MaxProbes:                  p.limits.MaxProbes,
		MaxConsecutiveServerErrors: p.limits.MaxConsecutiveServerErrors,
		ConsecutiveServerErrors:    p.consecutiveServerErrors,
		CircuitOpen:                p.circuitOpen,
		RateLimited:                p.rateLimited,
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *ApiVersionDowngradeProber) Restore(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil || state.Magic != snapshotMagic {
		return ErrBadSnapshot
	}
	if state.Probes < 0 || state.MaxProbes < 0 ||
		state.MaxConsecutiveServerErrors < 0 ||
		state.ConsecutiveServerErrors < 0 {
		return ErrBadSnapshot
	}

	surfaces := make([]VersionSurface, 0, len(state.Surfaces))
	seenSurfaces := make(map[string]struct{}, len(state.Surfaces))
	for _, surface := range state.Surfaces {
		if strings.TrimSpace(surface.ID) == "" ||
			strings.TrimSpace(surface.Resource) == "" ||
			strings.TrimSpace(surface.Version) == "" {
			return ErrBadSnapshot
		}
		switch surface.Carrier {
		case CarrierPath, CarrierHeader, CarrierQuery:
		default:
			return ErrBadSnapshot
		}
		if _, exists := seenSurfaces[surface.ID]; exists {
			return ErrBadSnapshot
		}
		seenSurfaces[surface.ID] = struct{}{}
		surfaces = append(surfaces, surface)
	}
	sort.Slice(surfaces, func(i, j int) bool { return surfaces[i].ID < surfaces[j].ID })

	ledger := make(map[string]Observation, len(state.Ledger))
	for _, observation := range state.Ledger {
		if strings.TrimSpace(observation.SurfaceID) == "" ||
			strings.TrimSpace(observation.Resource) == "" ||
			strings.TrimSpace(observation.Version) == "" {
			return ErrBadSnapshot
		}
		key := probeKey(observation.SurfaceID, observation.Resource)
		if _, exists := ledger[key]; exists {
			return ErrBadSnapshot
		}
		ledger[key] = cloneObservation(observation)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.surfaces = surfaces
	p.ledger = ledger
	p.probes = state.Probes
	p.limits.MaxProbes = state.MaxProbes
	p.limits.MaxConsecutiveServerErrors = state.MaxConsecutiveServerErrors
	p.consecutiveServerErrors = state.ConsecutiveServerErrors
	p.circuitOpen = state.CircuitOpen
	p.rateLimited = state.RateLimited
	return nil
}

func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return StatusOK
	case status == 401 || status == 403:
		return StatusAuthGate
	case status == 404:
		return StatusNotFound
	default:
		return StatusServer
	}
}

func normaliseFields(fields []string) []string {
	if len(fields) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for field := range set {
		result = append(result, field)
	}
	sort.Strings(result)
	return result
}

func findBaseline(group []Observation) int {
	baseline := -1
	for i, observation := range group {
		if !observation.Default {
			continue
		}
		if baseline < 0 || observation.SurfaceID < group[baseline].SurfaceID {
			baseline = i
		}
	}
	return baseline
}

func buildDiff(resource string, baseline, candidate Observation) VersionDiff {
	baselineFields := make(map[string]struct{}, len(baseline.Fields))
	for _, field := range baseline.Fields {
		baselineFields[field] = struct{}{}
	}
	candidateFields := make(map[string]struct{}, len(candidate.Fields))
	for _, field := range candidate.Fields {
		candidateFields[field] = struct{}{}
	}

	missing := make([]string, 0)
	for field := range baselineFields {
		if _, ok := candidateFields[field]; !ok {
			missing = append(missing, field)
		}
	}
	sort.Strings(missing)

	added := make([]string, 0)
	for field := range candidateFields {
		if _, ok := baselineFields[field]; !ok {
			added = append(added, field)
		}
	}
	sort.Strings(added)

	return VersionDiff{
		Resource:        resource,
		Baseline:        baseline.SurfaceID,
		Candidate:       candidate.SurfaceID,
		MissingFields:   missing,
		AddedFields:     added,
		PatternMismatch: baseline.ErrorCode != candidate.ErrorCode,
		BaselineClass:   baseline.Class,
		CandidateClass:  candidate.Class,
	}
}

func probeKey(surfaceID, resource string) string {
	return surfaceID + "\x00" + resource
}

func cloneSurfaces(surfaces []VersionSurface) []VersionSurface {
	if len(surfaces) == 0 {
		return nil
	}
	return append([]VersionSurface(nil), surfaces...)
}

func cloneObservation(observation Observation) Observation {
	observation.Fields = append([]string(nil), observation.Fields...)
	return observation
}

func uniqueNonBlank(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
