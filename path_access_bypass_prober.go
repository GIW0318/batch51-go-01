// Package task probes protected paths for normalisation mismatches between
// the front gateway and the backend router.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"
)

// Variant dimensions produced by Variants, in fixed order.
const (
	DimSingleEncode  = "single_encode"
	DimDoubleEncode  = "double_encode"
	DimDotSegment    = "dot_segment"
	DimSemicolon     = "semicolon_param"
	DimCase          = "case_flip"
	DimTrailingSlash = "trailing_slash"
)

// variantDimensions lists every dimension in its fixed probing order.
var variantDimensions = []string{
	DimSingleEncode,
	DimDoubleEncode,
	DimDotSegment,
	DimSemicolon,
	DimCase,
	DimTrailingSlash,
}

// Errors reported by the prober.
var (
	ErrBadPath         = errors.New("path is empty")
	ErrNoTransport     = errors.New("transport is not installed")
	ErrUnknownVariant  = errors.New("variant was not produced for this path")
	ErrVariantProbed   = errors.New("variant has already been probed")
	ErrBudgetExhausted = errors.New("request budget exhausted")
	ErrNoBaseline      = errors.New("baseline has not been captured")
	ErrNoObservations  = errors.New("no observation to judge")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Response is what the transport reports for one path.
type Response struct {
	Status  int               `json:"status"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}

// Transport fetches one path from the target.
type Transport interface {
	Fetch(path string) (Response, error)
}

// Clock supplies the probe timestamps.
type Clock interface {
	Now() int64
}

// systemClock is the default clock when none is installed.
type systemClock struct{}

func (systemClock) Now() int64 { return time.Now().Unix() }

// Baseline is the fingerprint of the protected path fetched as is.
type Baseline struct {
	Path        string   `json:"path"`
	Status      int      `json:"status"`
	Length      int      `json:"length"`
	Markers     []string `json:"markers"`
	Fingerprint string   `json:"fingerprint"`
}

// PathVariant is one normalisation variant of the protected path.
type PathVariant struct {
	ID        string `json:"id"`
	Dimension string `json:"dimension"`
	Path      string `json:"path"`
}

// Observation is the outcome of probing one variant once.
type Observation struct {
	VariantID   string   `json:"variant_id"`
	Dimension   string   `json:"dimension"`
	Path        string   `json:"path"`
	Status      int      `json:"status"`
	Length      int      `json:"length"`
	Markers     []string `json:"markers"`
	Fingerprint string   `json:"fingerprint"`
	At          int64    `json:"at"`
}

// BypassVerdict is the judgement over a set of observations.
type BypassVerdict struct {
	Bypassed []string `json:"bypassed"`
	Rejected []string `json:"rejected"`
	Reason   string   `json:"reason"`
}

// BudgetState reports how many requests were spent.
type BudgetState struct {
	Used      int `json:"used"`
	Limit     int `json:"limit"`
	Remaining int `json:"remaining"`
}

// ProberConfig bounds the prober and names the protected markers.
type ProberConfig struct {
	MaxRequests      int
	ProtectedMarkers []string
}

// PathAccessBypassProber owns the baseline, the probe history and the budget.
type PathAccessBypassProber struct {
	cfg       ProberConfig
	transport Transport
	clock     Clock

	mu          sync.Mutex
	baseline    Baseline
	hasBaseline bool
	known       map[string]PathVariant
	probed      map[string]bool
	obs         []Observation
	used        int
}

// NewPathAccessBypassProber builds a prober with the given limits.
func NewPathAccessBypassProber(cfg ProberConfig) *PathAccessBypassProber {
	return &PathAccessBypassProber{
		cfg:    cfg,
		clock:  systemClock{},
		known:  make(map[string]PathVariant),
		probed: make(map[string]bool),
	}
}

// SetTransport installs the transport used for every fetch.
func (p *PathAccessBypassProber) SetTransport(t Transport) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.transport = t
	return nil
}

// SetClock installs the clock used for probe timestamps.
func (p *PathAccessBypassProber) SetClock(c Clock) error {
	if c == nil {
		return errors.New("clock is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clock = c
	return nil
}

// fingerprint returns the FNV-1a 64-bit hex digest of the body.
func fingerprint(body string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(body))
	return fmt.Sprintf("%016x", h.Sum64())
}

// markersPresent lists the configured markers found in the body, in order.
func (p *PathAccessBypassProber) markersPresent(body string) []string {
	var found []string
	for _, m := range p.cfg.ProtectedMarkers {
		if m != "" && strings.Contains(body, m) {
			found = append(found, m)
		}
	}
	return found
}

// spendLocked consumes one request from the budget; caller holds the lock.
func (p *PathAccessBypassProber) spendLocked() error {
	if p.used >= p.cfg.MaxRequests {
		return ErrBudgetExhausted
	}
	p.used++
	return nil
}

// Baseline captures the fingerprint of the protected path itself.
func (p *PathAccessBypassProber) Baseline(path string) (Baseline, error) {
	if strings.TrimSpace(path) == "" {
		return Baseline{}, ErrBadPath
	}
	p.mu.Lock()
	if p.transport == nil {
		p.mu.Unlock()
		return Baseline{}, ErrNoTransport
	}
	if err := p.spendLocked(); err != nil {
		p.mu.Unlock()
		return Baseline{}, err
	}
	tr := p.transport
	p.mu.Unlock()

	resp, err := tr.Fetch(path)
	if err != nil {
		return Baseline{}, err
	}

	base := Baseline{
		Path:        path,
		Status:      resp.Status,
		Length:      len(resp.Body),
		Markers:     p.markersPresent(resp.Body),
		Fingerprint: fingerprint(resp.Body),
	}
	p.mu.Lock()
	p.baseline = base
	p.hasBaseline = true
	p.mu.Unlock()
	return base, nil
}

// mutate derives the variant path for one dimension.
func mutate(path, dimension string) string {
	switch dimension {
	case DimSingleEncode:
		return strings.ReplaceAll(path, "/", "%2f")
	case DimDoubleEncode:
		return strings.ReplaceAll(path, "/", "%252f")
	case DimDotSegment:
		trimmed := strings.TrimPrefix(path, "/")
		return "/%2e%2e/" + trimmed
	case DimSemicolon:
		return path + ";gsb=1"
	case DimCase:
		return strings.ToUpper(path)
	case DimTrailingSlash:
		return path + "/"
	}
	return path
}

// Variants derives the normalisation variants for one path.
func (p *PathAccessBypassProber) Variants(path string) ([]PathVariant, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrBadPath
	}
	variants := make([]PathVariant, 0, len(variantDimensions))
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, dim := range variantDimensions {
		mutated := mutate(path, dim)
		if mutated == path {
			continue
		}
		v := PathVariant{
			ID:        dim + ":" + mutated,
			Dimension: dim,
			Path:      mutated,
		}
		variants = append(variants, v)
		p.known[v.ID] = v
	}
	return variants, nil
}

// Probe fetches one variant exactly once and records the outcome.
func (p *PathAccessBypassProber) Probe(variant PathVariant) (Observation, error) {
	p.mu.Lock()
	known, ok := p.known[variant.ID]
	if !ok || known.Dimension != variant.Dimension || known.Path != variant.Path {
		p.mu.Unlock()
		return Observation{}, ErrUnknownVariant
	}
	if p.probed[variant.ID] {
		p.mu.Unlock()
		return Observation{}, ErrVariantProbed
	}
	if p.transport == nil {
		p.mu.Unlock()
		return Observation{}, ErrNoTransport
	}
	if err := p.spendLocked(); err != nil {
		p.mu.Unlock()
		return Observation{}, err
	}
	p.probed[variant.ID] = true
	tr := p.transport
	clk := p.clock
	p.mu.Unlock()

	resp, err := tr.Fetch(variant.Path)
	if err != nil {
		return Observation{}, err
	}

	obs := Observation{
		VariantID:   variant.ID,
		Dimension:   variant.Dimension,
		Path:        variant.Path,
		Status:      resp.Status,
		Length:      len(resp.Body),
		Markers:     p.markersPresent(resp.Body),
		Fingerprint: fingerprint(resp.Body),
		At:          clk.Now(),
	}
	p.mu.Lock()
	p.obs = append(p.obs, obs)
	p.mu.Unlock()
	return obs, nil
}

// Judge folds the observations into one bypass verdict.
func (p *PathAccessBypassProber) Judge(obs []Observation) (BypassVerdict, error) {
	if len(obs) == 0 {
		return BypassVerdict{}, ErrNoObservations
	}
	p.mu.Lock()
	if !p.hasBaseline {
		p.mu.Unlock()
		return BypassVerdict{}, ErrNoBaseline
	}
	base := p.baseline
	p.mu.Unlock()

	verdict := BypassVerdict{}
	for _, o := range obs {
		bypassed := o.Status >= 200 && o.Status < 300 &&
			len(o.Markers) > 0 &&
			o.Fingerprint != "" && o.Fingerprint != base.Fingerprint
		if bypassed {
			verdict.Bypassed = append(verdict.Bypassed, o.VariantID)
		} else {
			verdict.Rejected = append(verdict.Rejected, o.VariantID)
		}
	}
	sort.Strings(verdict.Bypassed)
	sort.Strings(verdict.Rejected)
	verdict.Reason = fmt.Sprintf(
		"judged %d observations against baseline %s (status %d, fingerprint %s): %d bypassed, %d rejected",
		len(obs), base.Path, base.Status, base.Fingerprint,
		len(verdict.Bypassed), len(verdict.Rejected))
	return verdict, nil
}

// Budget reports how much of the request budget is still available.
func (p *PathAccessBypassProber) Budget() (BudgetState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	remaining := p.cfg.MaxRequests - p.used
	if remaining < 0 {
		remaining = 0
	}
	return BudgetState{
		Used:      p.used,
		Limit:     p.cfg.MaxRequests,
		Remaining: remaining,
	}, nil
}

// Observations returns a copy of every recorded probe outcome.
func (p *PathAccessBypassProber) Observations() []Observation {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Observation, len(p.obs))
	copy(out, p.obs)
	return out
}

// snapshotState is the serialised prober state.
type snapshotState struct {
	Baseline    Baseline      `json:"baseline"`
	HasBaseline bool          `json:"has_baseline"`
	Obs         []Observation `json:"observations"`
	Used        int           `json:"used"`
	Known       []PathVariant `json:"known"`
	Probed      []string      `json:"probed"`
}

// Snapshot serialises the baseline, the probe history and the budget.
func (p *PathAccessBypassProber) Snapshot() ([]byte, error) {
	p.mu.Lock()
	state := snapshotState{
		Baseline:    p.baseline,
		HasBaseline: p.hasBaseline,
		Obs:         append([]Observation(nil), p.obs...),
		Used:        p.used,
	}
	for _, v := range p.known {
		state.Known = append(state.Known, v)
	}
	sort.Slice(state.Known, func(i, j int) bool { return state.Known[i].ID < state.Known[j].ID })
	for id := range p.probed {
		state.Probed = append(state.Probed, id)
	}
	sort.Strings(state.Probed)
	p.mu.Unlock()
	return json.Marshal(state)
}

// Restore replaces the whole prober state, all or nothing.
func (p *PathAccessBypassProber) Restore(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return ErrBadSnapshot
	}
	if state.Used < 0 {
		return ErrBadSnapshot
	}
	known := make(map[string]PathVariant, len(state.Known))
	for _, v := range state.Known {
		if v.ID == "" || v.Dimension == "" {
			return ErrBadSnapshot
		}
		known[v.ID] = v
	}
	probed := make(map[string]bool, len(state.Probed))
	for _, id := range state.Probed {
		probed[id] = true
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.baseline = state.Baseline
	p.hasBaseline = state.HasBaseline
	p.obs = append([]Observation(nil), state.Obs...)
	p.used = state.Used
	p.known = known
	p.probed = probed
	return nil
}
