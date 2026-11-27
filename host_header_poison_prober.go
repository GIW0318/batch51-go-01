// Package task builds host header probes for the three request sources,
// submits them to a registered entrypoint and grades which layer took the
// injected value.
package task

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Request sources that can carry the host value.
const (
	SourceHost        = "host"
	SourceForwarded   = "x_forwarded_host"
	SourceRequestLine = "request_line"
)

// Layers that can read the host value.
const (
	LayerFrontend = "frontend"
	LayerBackend  = "backend"
	LayerCache    = "cache"
	LayerTemplate = "template"
)

// Reasons attached to a verdict.
const (
	ReasonHostUsed     = "host_used"
	ReasonHostIgnored  = "host_ignored"
	ReasonLayeredSplit = "layered_split"
)

// DefaultHost is what a layer falls back to when it does not read the source.
const DefaultHost = "app.internal"

// MarkerPrefix prefixes every recyclable probe marker.
const MarkerPrefix = "h-"

const (
	snapshotVersion   = 1
	judgeHost         = "evil.test"
	judgeForwardedSet = "evil.test, layered.test"
)

var sourceOrder = []string{SourceHost, SourceForwarded, SourceRequestLine}

// Errors returned by the prober.
var (
	ErrBadEndpoint       = errors.New("entrypoint name is required")
	ErrDuplicateEndpoint = errors.New("entrypoint is already registered")
	ErrUnknownEndpoint   = errors.New("entrypoint is not registered")
	ErrBadHost           = errors.New("host value is required")
	ErrBadSource         = errors.New("request source is not supported")
	ErrBadPolicy         = errors.New("policy does not define any layer source")
	ErrProbeBudget       = errors.New("probe budget exhausted for entrypoint")
	ErrBadLimit          = errors.New("prober limits must be positive")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// ProberConfig bounds one prober instance.
type ProberConfig struct {
	MaxConcurrent        int
	MaxProbesPerEndpoint int
}

// HostPolicy describes which source each layer of one entrypoint reads.
type HostPolicy struct {
	Name           string
	FrontSource    string
	BackSource     string
	CacheSource    string
	TemplateSource string
	RedirectPath   string
	LinkPath       string
	EscapeLinks    bool
}

// Request is one probe request with the injected source populated.
type Request struct {
	Source      string
	Host        string
	Marker      string
	HostHeader  string
	Forwarded   string
	RequestLine string
}

// Observation is what one entrypoint produced for one request.
type Observation struct {
	Status     int
	Location   string
	Link       string
	CacheKey   string
	UsedSource string
	Layer      string
	Accepted   bool
	Marker     string
}

// Verdict is the graded value chain of one entrypoint.
type Verdict struct {
	Endpoint        string
	EffectiveSource string
	Layer           string
	Poisoned        bool
	Layered         bool
	Seq             int64
}

// SourceResult is the per source outcome of one judge round.
type SourceResult struct {
	Source   string
	Accepted bool
	Layer    string
	Value    string
}

// ProbeRecord is one submitted request kept for replay.
type ProbeRecord struct {
	Seq      int64
	Endpoint string
	Source   string
	Layer    string
	Value    string
	Marker   string
}

// ProberStats counts the traffic of one entrypoint.
type ProberStats struct {
	Probes       int64
	Accepted     int64
	Rejected     int64
	PeakInFlight int
}

type layerValue struct {
	layer    string
	source   string
	value    string
	accepted bool
}

type probeOutcome struct {
	observation Observation
	layers      []layerValue
	record      ProbeRecord
}

type acceptedLayer struct {
	source string
	layer  string
}

type endpointState struct {
	mu sync.Mutex

	policy        HostPolicy
	records       []ProbeRecord
	sources       []SourceResult
	verdict       Verdict
	hasVerdict    bool
	stats         ProberStats
	activeMarkers map[string]struct{}
	inFlight      int
}

// HostHeaderPoisonProber owns policies, records and counters.
type HostHeaderPoisonProber struct {
	cfg    ProberConfig
	cfgErr error
	global chan struct{}

	mu        sync.RWMutex
	order     []string
	endpoints map[string]*endpointState
	seq       sequenceCounter

	markerMu    sync.Mutex
	markerSeq   uint64
	usedMarkers map[string]struct{}
}

// sequenceCounter keeps sequence allocation independent from endpoint locks.
type sequenceCounter struct {
	mu sync.Mutex
	n  int64
}

func (s *sequenceCounter) Add(delta int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n += delta
	return s.n
}

func (s *sequenceCounter) Load() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func (s *sequenceCounter) Store(v int64) {
	s.mu.Lock()
	s.n = v
	s.mu.Unlock()
}

// NewHostHeaderPoisonProber builds a prober with the given caps.
func NewHostHeaderPoisonProber(cfg ProberConfig) *HostHeaderPoisonProber {
	p := &HostHeaderPoisonProber{
		cfg:         cfg,
		endpoints:   make(map[string]*endpointState),
		usedMarkers: make(map[string]struct{}),
	}
	if cfg.MaxConcurrent <= 0 || cfg.MaxProbesPerEndpoint <= 0 {
		p.cfgErr = ErrBadLimit
		return p
	}
	p.global = make(chan struct{}, cfg.MaxConcurrent)
	return p
}

// AddEntrypoint registers one policy profile.
func (p *HostHeaderPoisonProber) AddEntrypoint(policy HostPolicy) error {
	if p.cfgErr != nil {
		return p.cfgErr
	}
	policy, err := normalizePolicy(policy)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.endpoints[policy.Name]; exists {
		return ErrDuplicateEndpoint
	}
	p.endpoints[policy.Name] = &endpointState{
		policy:        policy,
		activeMarkers: make(map[string]struct{}),
	}
	p.order = append(p.order, policy.Name)
	return nil
}

// Entrypoints lists the registered entrypoint names in registration order.
func (p *HostHeaderPoisonProber) Entrypoints() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.order...)
}

// Build assembles one request for the requested source.
func (p *HostHeaderPoisonProber) Build(source, host string) (Request, error) {
	if p.cfgErr != nil {
		return Request{}, p.cfgErr
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.build(source, host)
}

func (p *HostHeaderPoisonProber) build(source, host string) (Request, error) {
	source, ok := canonicalSource(source)
	if !ok {
		return Request{}, ErrBadSource
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return Request{}, ErrBadHost
	}

	req := Request{
		Source: source,
		Host:   host,
		Marker: p.allocateMarker(),
	}
	switch source {
	case SourceHost:
		req.HostHeader = host
	case SourceForwarded:
		req.Forwarded = host
	case SourceRequestLine:
		req.RequestLine = "GET http://" + host + "/ HTTP/1.1"
	}
	return req, nil
}

// Probe submits one request and records the produced links and cache key.
func (p *HostHeaderPoisonProber) Probe(endpoint string, req Request) (Observation, error) {
	if p.cfgErr != nil {
		return Observation{}, p.cfgErr
	}
	endpoint = strings.TrimSpace(endpoint)

	p.mu.RLock()
	defer p.mu.RUnlock()
	ep := p.endpoints[endpoint]
	if ep == nil {
		return Observation{}, ErrUnknownEndpoint
	}

	source, ok := canonicalSource(req.Source)
	if !ok {
		return Observation{}, ErrBadSource
	}
	raw, err := requestSourceValue(source, req)
	if err != nil {
		return Observation{}, err
	}
	req.Source = source

	ep.mu.Lock()
	defer ep.mu.Unlock()
	outcome, err := p.probeLocked(ep, req, raw)
	if err != nil {
		return Observation{}, err
	}
	return outcome.observation, nil
}

func (p *HostHeaderPoisonProber) probeLocked(ep *endpointState, req Request, raw string) (probeOutcome, error) {
	if ep.stats.Probes >= int64(p.cfg.MaxProbesPerEndpoint) {
		return probeOutcome{}, ErrProbeBudget
	}

	ep.stats.Probes++
	marker := p.claimMarker(req.Marker)

	p.global <- struct{}{}
	defer func() { <-p.global }()

	ep.inFlight++
	if ep.inFlight > ep.stats.PeakInFlight {
		ep.stats.PeakInFlight = ep.inFlight
	}
	defer func() { ep.inFlight-- }()

	layers := resolveLayers(ep.policy, req, raw)
	effective, accepted := effectiveLayer(layers)

	obs := Observation{
		Status:   302,
		Location: renderURL(layers[0].value, ep.policy.RedirectPath, ep.policy.EscapeLinks),
		Link:     renderURL(layers[1].value, ep.policy.LinkPath, ep.policy.EscapeLinks),
		CacheKey: "v1:" + layers[2].value,
		Marker:   marker,
	}
	recordLayer := ""
	if accepted {
		obs.Accepted = true
		obs.UsedSource = req.Source
		obs.Layer = effective.layer
		recordLayer = effective.layer
		ep.stats.Accepted++
	} else {
		ep.stats.Rejected++
	}

	seq := p.seq.Add(1)
	record := ProbeRecord{
		Seq:      seq,
		Endpoint: ep.policy.Name,
		Source:   req.Source,
		Layer:    recordLayer,
		Value:    raw,
		Marker:   marker,
	}
	ep.records = append(ep.records, record)
	ep.activeMarkers[marker] = struct{}{}

	return probeOutcome{observation: obs, layers: layers, record: record}, nil
}

// Judge runs the three sources in a fixed order and grades the value chain.
func (p *HostHeaderPoisonProber) Judge(endpoint string) (Verdict, error) {
	if p.cfgErr != nil {
		return Verdict{}, p.cfgErr
	}
	endpoint = strings.TrimSpace(endpoint)

	p.mu.RLock()
	defer p.mu.RUnlock()
	ep := p.endpoints[endpoint]
	if ep == nil {
		return Verdict{}, ErrUnknownEndpoint
	}

	ep.mu.Lock()
	defer ep.mu.Unlock()

	remaining := int64(p.cfg.MaxProbesPerEndpoint) - ep.stats.Probes
	if remaining < int64(len(sourceOrder)) {
		return Verdict{}, ErrProbeBudget
	}

	results := make([]SourceResult, 0, len(sourceOrder))
	acceptedPairs := make(map[string]struct{})
	acceptedByLayer := make(map[string]acceptedLayer)
	var lastSeq int64
	poisoned := false

	for _, source := range sourceOrder {
		host := judgeHostFor(ep.policy, source)
		req, err := p.build(source, host)
		if err != nil {
			return Verdict{}, err
		}
		raw, err := requestSourceValue(source, req)
		if err != nil {
			return Verdict{}, err
		}

		outcome, err := p.probeLocked(ep, req, raw)
		if err != nil {
			return Verdict{}, err
		}
		lastSeq = outcome.record.Seq

		result := SourceResult{Source: source, Value: DefaultHost}
		if outcome.observation.Accepted {
			result.Accepted = true
			result.Layer = outcome.observation.Layer
			result.Value = outcome.observation.valueForLayer(outcome.layers)
			poisoned = true
			acceptedByLayer[result.Layer] = acceptedLayer{source: source, layer: result.Layer}
			for _, layer := range outcome.layers {
				if layer.accepted {
					acceptedPairs[source+"\x00"+layer.value] = struct{}{}
				}
			}
		}
		results = append(results, result)
	}

	verdict := Verdict{
		Endpoint: endpoint,
		Poisoned: poisoned,
		Layered:  len(acceptedPairs) > 1,
		Seq:      lastSeq,
	}
	for _, layer := range []string{LayerFrontend, LayerBackend, LayerCache, LayerTemplate} {
		if accepted, ok := acceptedByLayer[layer]; ok {
			verdict.EffectiveSource = accepted.source
			verdict.Layer = accepted.layer
			break
		}
	}

	ep.sources = append(ep.sources[:0], results...)
	ep.verdict = verdict
	ep.hasVerdict = true
	return verdict, nil
}

// Sources returns the per source results of the last judge round.
func (p *HostHeaderPoisonProber) Sources(endpoint string) []SourceResult {
	endpoint = strings.TrimSpace(endpoint)
	p.mu.RLock()
	defer p.mu.RUnlock()
	ep := p.endpoints[endpoint]
	if ep == nil {
		return nil
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	return append([]SourceResult(nil), ep.sources...)
}

// Records lists the submitted requests of one entrypoint, oldest first.
func (p *HostHeaderPoisonProber) Records(endpoint string) []ProbeRecord {
	endpoint = strings.TrimSpace(endpoint)
	p.mu.RLock()
	defer p.mu.RUnlock()
	ep := p.endpoints[endpoint]
	if ep == nil {
		return nil
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	return append([]ProbeRecord(nil), ep.records...)
}

// Stats reads the counters of one entrypoint.
func (p *HostHeaderPoisonProber) Stats(endpoint string) ProberStats {
	endpoint = strings.TrimSpace(endpoint)
	p.mu.RLock()
	defer p.mu.RUnlock()
	ep := p.endpoints[endpoint]
	if ep == nil {
		return ProberStats{}
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	return ep.stats
}

// RecycleMarkers releases the probe markers of one entrypoint and counts them.
func (p *HostHeaderPoisonProber) RecycleMarkers(endpoint string) int {
	endpoint = strings.TrimSpace(endpoint)
	p.mu.RLock()
	defer p.mu.RUnlock()
	ep := p.endpoints[endpoint]
	if ep == nil {
		return 0
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	n := len(ep.activeMarkers)
	ep.activeMarkers = make(map[string]struct{})
	return n
}

// Reset drops the records of one entrypoint but keeps the policy.
func (p *HostHeaderPoisonProber) Reset(endpoint string) error {
	if p.cfgErr != nil {
		return p.cfgErr
	}
	endpoint = strings.TrimSpace(endpoint)
	p.mu.RLock()
	defer p.mu.RUnlock()
	ep := p.endpoints[endpoint]
	if ep == nil {
		return ErrUnknownEndpoint
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	ep.records = nil
	ep.sources = nil
	ep.verdict = Verdict{}
	ep.hasVerdict = false
	ep.stats = ProberStats{}
	ep.activeMarkers = make(map[string]struct{})
	return nil
}

// MailPreview renders the notification template with the injected host.
func (p *HostHeaderPoisonProber) MailPreview(endpoint, host string) (Observation, error) {
	if p.cfgErr != nil {
		return Observation{}, p.cfgErr
	}
	endpoint = strings.TrimSpace(endpoint)

	p.mu.RLock()
	defer p.mu.RUnlock()
	ep := p.endpoints[endpoint]
	if ep == nil {
		return Observation{}, ErrUnknownEndpoint
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return Observation{}, ErrBadHost
	}

	obs := Observation{
		Status: 200,
		Link:   renderURL(DefaultHost, ep.policy.LinkPath, ep.policy.EscapeLinks),
	}
	value, ok := sourceValueForLayer(ep.policy.TemplateSource, host, LayerTemplate)
	if !ok {
		return obs, nil
	}
	obs.Accepted = true
	obs.UsedSource = ep.policy.TemplateSource
	obs.Layer = LayerTemplate
	obs.Link = renderURL(value, ep.policy.LinkPath, ep.policy.EscapeLinks)
	return obs, nil
}

type proberSnapshot struct {
	Policies []endpointSnapshot `json:"policies"`
	Seq      int64              `json:"seq"`
	Marker   uint64             `json:"marker_seq"`
}

type proberSnapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

type endpointSnapshot struct {
	HostPolicy
	Records       []ProbeRecord  `json:"records"`
	Sources       []SourceResult `json:"sources"`
	Verdict       Verdict        `json:"verdict"`
	HasVerdict    bool           `json:"has_verdict"`
	Stats         ProberStats    `json:"stats"`
	ActiveMarkers []string       `json:"active_markers"`
}

// Save serialises policies, records and counters.
func (p *HostHeaderPoisonProber) Save() ([]byte, error) {
	if p.cfgErr != nil {
		return nil, p.cfgErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	snap := proberSnapshot{
		Policies: make([]endpointSnapshot, 0, len(p.order)),
		Seq:      p.seq.Load(),
	}
	for _, name := range p.order {
		ep := p.endpoints[name]
		ep.mu.Lock()
		markers := make([]string, 0, len(ep.activeMarkers))
		for marker := range ep.activeMarkers {
			markers = append(markers, marker)
		}
		sort.Strings(markers)
		snap.Policies = append(snap.Policies, endpointSnapshot{
			HostPolicy:    ep.policy,
			Records:       append([]ProbeRecord(nil), ep.records...),
			Sources:       append([]SourceResult(nil), ep.sources...),
			Verdict:       ep.verdict,
			HasVerdict:    ep.hasVerdict,
			Stats:         ep.stats,
			ActiveMarkers: markers,
		})
		ep.mu.Unlock()
	}

	p.markerMu.Lock()
	snap.Marker = p.markerSeq
	p.markerMu.Unlock()

	state, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(state)
	return json.Marshal(proberSnapshotEnvelope{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		State:    state,
	})
}

// Reload replaces the whole state with a snapshot, all or nothing.
func (p *HostHeaderPoisonProber) Reload(data []byte) error {
	if p.cfgErr != nil {
		return p.cfgErr
	}

	var envelope proberSnapshotEnvelope
	if err := decodeStrictJSON(data, &envelope); err != nil || envelope.Version != snapshotVersion || len(envelope.State) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.State)
	want := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(envelope.Checksum), []byte(want)) != 1 {
		return ErrBadSnapshot
	}
	var snap proberSnapshot
	if err := decodeStrictJSON(envelope.State, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Seq < 0 {
		return ErrBadSnapshot
	}

	order := make([]string, 0, len(snap.Policies))
	endpoints := make(map[string]*endpointState, len(snap.Policies))
	usedMarkers := make(map[string]struct{})
	seenSequences := make(map[int64]struct{})
	maxSeq := snap.Seq
	maxMarker := snap.Marker

	for _, saved := range snap.Policies {
		policy, err := normalizePolicy(saved.HostPolicy)
		if err != nil {
			return ErrBadSnapshot
		}
		if _, exists := endpoints[policy.Name]; exists {
			return ErrBadSnapshot
		}

		ep := &endpointState{
			policy:        policy,
			records:       append([]ProbeRecord(nil), saved.Records...),
			sources:       append([]SourceResult(nil), saved.Sources...),
			verdict:       saved.Verdict,
			hasVerdict:    saved.HasVerdict,
			stats:         saved.Stats,
			activeMarkers: make(map[string]struct{}, len(saved.ActiveMarkers)),
		}
		if ep.stats.Probes < 0 || ep.stats.Accepted < 0 || ep.stats.Rejected < 0 || ep.stats.PeakInFlight < 0 {
			return ErrBadSnapshot
		}
		if ep.stats.Probes != int64(len(ep.records)) || ep.stats.Accepted+ep.stats.Rejected != ep.stats.Probes {
			return ErrBadSnapshot
		}
		if (ep.stats.Probes == 0 && ep.stats.PeakInFlight != 0) || ep.stats.PeakInFlight > int(ep.stats.Probes) {
			return ErrBadSnapshot
		}

		var previousSeq int64
		for _, record := range ep.records {
			if record.Seq <= previousSeq || strings.TrimSpace(record.Endpoint) != policy.Name {
				return ErrBadSnapshot
			}
			if _, duplicate := seenSequences[record.Seq]; duplicate {
				return ErrBadSnapshot
			}
			if _, ok := canonicalSource(record.Source); !ok {
				return ErrBadSnapshot
			}
			if record.Layer != "" && !validLayer(record.Layer) {
				return ErrBadSnapshot
			}
			if !strings.HasPrefix(record.Marker, MarkerPrefix) {
				return ErrBadSnapshot
			}
			if strings.TrimSpace(record.Value) == "" {
				return ErrBadSnapshot
			}
			if _, duplicate := usedMarkers[record.Marker]; duplicate {
				return ErrBadSnapshot
			}
			seenSequences[record.Seq] = struct{}{}
			if record.Seq > maxSeq {
				maxSeq = record.Seq
			}
			if n, ok := markerNumber(record.Marker); ok && n > maxMarker {
				maxMarker = n
			}
			usedMarkers[record.Marker] = struct{}{}
			previousSeq = record.Seq
		}

		for _, result := range ep.sources {
			if _, ok := canonicalSource(result.Source); !ok {
				return ErrBadSnapshot
			}
			if result.Layer != "" && !validLayer(result.Layer) {
				return ErrBadSnapshot
			}
			if result.Accepted && result.Layer == "" {
				return ErrBadSnapshot
			}
		}

		for _, marker := range saved.ActiveMarkers {
			if !strings.HasPrefix(marker, MarkerPrefix) {
				return ErrBadSnapshot
			}
			if _, duplicate := ep.activeMarkers[marker]; duplicate {
				return ErrBadSnapshot
			}
			ep.activeMarkers[marker] = struct{}{}
			if _, exists := usedMarkers[marker]; !exists {
				return ErrBadSnapshot
			}
			if n, ok := markerNumber(marker); ok && n > maxMarker {
				maxMarker = n
			}
		}

		if saved.HasVerdict {
			if strings.TrimSpace(saved.Verdict.Endpoint) != policy.Name {
				return ErrBadSnapshot
			}
			if saved.Verdict.Layer != "" && !validLayer(saved.Verdict.Layer) {
				return ErrBadSnapshot
			}
			if saved.Verdict.EffectiveSource != "" {
				if _, ok := canonicalSource(saved.Verdict.EffectiveSource); !ok {
					return ErrBadSnapshot
				}
			}
			if saved.Verdict.Seq > maxSeq {
				maxSeq = saved.Verdict.Seq
			}
		}

		order = append(order, policy.Name)
		endpoints[policy.Name] = ep
	}

	p.mu.Lock()
	p.order = order
	p.endpoints = endpoints
	p.seq.Store(maxSeq)
	p.markerMu.Lock()
	p.markerSeq = maxMarker
	p.usedMarkers = usedMarkers
	p.markerMu.Unlock()
	p.mu.Unlock()
	return nil
}

func normalizePolicy(policy HostPolicy) (HostPolicy, error) {
	policy.Name = strings.TrimSpace(policy.Name)
	if policy.Name == "" {
		return HostPolicy{}, ErrBadEndpoint
	}

	fields := []*string{&policy.FrontSource, &policy.BackSource, &policy.CacheSource, &policy.TemplateSource}
	defined := false
	for _, field := range fields {
		value := strings.TrimSpace(*field)
		if value == "" {
			*field = ""
			continue
		}
		source, ok := canonicalSource(value)
		if !ok {
			return HostPolicy{}, ErrBadSource
		}
		*field = source
		defined = true
	}
	if !defined {
		return HostPolicy{}, ErrBadPolicy
	}
	return policy, nil
}

func canonicalSource(source string) (string, bool) {
	switch strings.TrimSpace(source) {
	case SourceHost:
		return SourceHost, true
	case SourceForwarded:
		return SourceForwarded, true
	case SourceRequestLine:
		return SourceRequestLine, true
	default:
		return "", false
	}
}

func validLayer(layer string) bool {
	switch layer {
	case LayerFrontend, LayerBackend, LayerCache, LayerTemplate:
		return true
	default:
		return false
	}
}

func requestSourceValue(source string, req Request) (string, error) {
	var value string
	switch source {
	case SourceHost:
		value = req.HostHeader
	case SourceForwarded:
		value = req.Forwarded
	case SourceRequestLine:
		authority, ok := requestLineAuthority(req.RequestLine)
		if !ok {
			return "", ErrBadHost
		}
		value = authority
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrBadHost
	}
	return value, nil
}

func requestLineAuthority(line string) (string, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "GET" {
		return "", false
	}
	target := fields[1]
	index := strings.Index(target, "://")
	if index < 0 {
		return "", false
	}
	scheme := strings.ToLower(target[:index])
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	authority := target[index+3:]
	if cut := strings.IndexAny(authority, "/?#"); cut >= 0 {
		authority = authority[:cut]
	}
	authority = strings.TrimSpace(authority)
	return authority, authority != ""
}

func resolveLayers(policy HostPolicy, req Request, raw string) []layerValue {
	layers := []layerValue{
		{layer: LayerFrontend, source: policy.FrontSource},
		{layer: LayerBackend, source: policy.BackSource},
		{layer: LayerCache, source: policy.CacheSource},
		{layer: LayerTemplate, source: policy.TemplateSource},
	}
	for i := range layers {
		value, ok := sourceValueForLayer(layers[i].source, raw, layers[i].layer)
		if ok && layers[i].source == req.Source {
			layers[i].accepted = true
			layers[i].value = value
		} else {
			layers[i].value = DefaultHost
		}
	}
	return layers
}

func sourceValueForLayer(source, raw, layer string) (string, bool) {
	source, ok := canonicalSource(source)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", false
	}
	if source != SourceForwarded {
		value := strings.TrimSpace(raw)
		return value, value != ""
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value != "" {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return "", false
	}
	if layer == LayerFrontend {
		return values[0], true
	}
	return values[len(values)-1], true
}

func effectiveLayer(layers []layerValue) (layerValue, bool) {
	for _, layer := range layers {
		if layer.accepted {
			return layer, true
		}
	}
	return layerValue{}, false
}

func (obs Observation) valueForLayer(layers []layerValue) string {
	for _, layer := range layers {
		if layer.layer == obs.Layer {
			return layer.value
		}
	}
	return DefaultHost
}

func renderURL(host, path string, escape bool) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = DefaultHost
	}
	if escape {
		host = url.PathEscape(host)
	}
	return "https://" + host + path
}

func judgeHostFor(policy HostPolicy, source string) string {
	if source != SourceForwarded || policy.FrontSource != SourceForwarded {
		return judgeHost
	}
	if policy.BackSource == SourceForwarded || policy.CacheSource == SourceForwarded || policy.TemplateSource == SourceForwarded {
		return judgeForwardedSet
	}
	return judgeHost
}

func (p *HostHeaderPoisonProber) allocateMarker() string {
	p.markerMu.Lock()
	defer p.markerMu.Unlock()
	return p.nextMarkerLocked()
}

func (p *HostHeaderPoisonProber) nextMarkerLocked() string {
	p.markerSeq++
	return MarkerPrefix + strconv.FormatUint(p.markerSeq, 10)
}

func (p *HostHeaderPoisonProber) claimMarker(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	p.markerMu.Lock()
	defer p.markerMu.Unlock()
	if candidate == "" {
		candidate = p.nextMarkerLocked()
	} else if _, used := p.usedMarkers[candidate]; used {
		candidate = p.nextMarkerLocked()
	}
	if n, ok := markerNumber(candidate); ok && n > p.markerSeq {
		p.markerSeq = n
	}
	p.usedMarkers[candidate] = struct{}{}
	return candidate
}

func markerNumber(marker string) (uint64, bool) {
	if !strings.HasPrefix(marker, MarkerPrefix) {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(marker, MarkerPrefix), 10, 64)
	return n, err == nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
