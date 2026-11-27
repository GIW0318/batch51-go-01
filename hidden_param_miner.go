// Package task mines undocumented query and form parameters on a known
// endpoint by differential probing.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Candidate sources, in priority order.
const (
	SourceSeed     = "seed"
	SourceExisting = "existing"
	SourcePage     = "page"
)

// Evidence kinds attached to a finding.
const (
	EvidenceLength      = "length"
	EvidenceFields      = "fields"
	EvidenceReflected   = "reflected"
	EvidenceStatus      = "status"
	EvidenceFingerprint = "fingerprint"
	EvidenceNone        = "none"
)

// Errors reported by the miner.
var (
	ErrBadEndpoint      = errors.New("endpoint name and path are required")
	ErrNoTransport      = errors.New("transport is not configured")
	ErrNoCandidates     = errors.New("no candidate parameter to probe")
	ErrEmptyBatch       = errors.New("probe batch is empty")
	ErrBudgetExhausted  = errors.New("request budget exhausted")
	ErrNoBaseline       = errors.New("baseline has not been captured")
	ErrNoObservations   = errors.New("no observation to judge")
	ErrUnstableEndpoint = errors.New("endpoint response is not stable")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Endpoint is one documented API entry point.
type Endpoint struct {
	Name        string            `json:"name"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	ContentType string            `json:"content_type"`
	Params      []string          `json:"params"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
}

// Param is one injected query or form parameter.
type Param struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Request is one probe request handed to the transport.
type Request struct {
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	ContentType string            `json:"content_type"`
	Params      []Param           `json:"params"`
	Headers     map[string]string `json:"headers"`
}

// Response is what the transport reports back.
type Response struct {
	Status int               `json:"status"`
	Body   string            `json:"body"`
	Header map[string]string `json:"header"`
}

// Transport performs one request against the endpoint.
type Transport interface {
	Send(req Request) (Response, error)
}

// Baseline is the stable shape of the bare response.
type Baseline struct {
	Endpoint    string   `json:"endpoint"`
	Status      int      `json:"status"`
	Length      int      `json:"length"`
	Fields      []string `json:"fields"`
	Fingerprint string   `json:"fingerprint"`
	Samples     int      `json:"samples"`
}

// ParamCandidate is one parameter name under test.
type ParamCandidate struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// ParamObservation is what one candidate did to the response.
type ParamObservation struct {
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Status    int      `json:"status"`
	Length    int      `json:"length"`
	Fields    []string `json:"fields"`
	Reflected bool     `json:"reflected"`
	Location  string   `json:"location"`
	Diff      bool     `json:"diff"`
	Evidence  string   `json:"evidence"`
	Sentinel  string   `json:"sentinel"`
}

// ParamFinding is one accepted hidden parameter.
type ParamFinding struct {
	Name     string `json:"name"`
	Source   string `json:"source"`
	Evidence string `json:"evidence"`
	Delta    int    `json:"delta"`
}

// BudgetState reports how many requests were spent.
type BudgetState struct {
	Used      int `json:"used"`
	Limit     int `json:"limit"`
	Remaining int `json:"remaining"`
}

// ProberConfig bounds the miner.
type ProberConfig struct {
	MaxRequests     int
	BaselineSamples int
	MaxSplits       int
}

// HiddenParamMiner owns the baseline, the observations and the budget.
type HiddenParamMiner struct {
	cfg ProberConfig
	mu  sync.Mutex

	transport Transport
	used      int

	baseline      *Baseline
	baselineScope string

	observations      []ParamObservation
	observationScopes []string

	confirmed             []ParamFinding
	confirmedObservations []ParamObservation
	confirmedScopes       []string

	sentinelSeq uint64
}

// NewHiddenParamMiner builds a miner with the given limits.
func NewHiddenParamMiner(cfg ProberConfig) *HiddenParamMiner {
	if cfg.BaselineSamples <= 0 {
		cfg.BaselineSamples = 1
	}
	if cfg.MaxRequests < 0 {
		cfg.MaxRequests = 0
	}
	if cfg.MaxSplits < 0 {
		cfg.MaxSplits = 0
	}
	return &HiddenParamMiner{cfg: cfg}
}

// SetTransport installs the transport used for every request.
func (m *HiddenParamMiner) SetTransport(t Transport) error {
	if t == nil {
		return ErrNoTransport
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transport = t
	return nil
}

// Baseline captures the shape of the bare response and checks it is stable.
func (m *HiddenParamMiner) Baseline(endpoint Endpoint) (Baseline, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateEndpoint(endpoint); err != nil {
		return Baseline{}, err
	}
	if m.transport == nil {
		return Baseline{}, ErrNoTransport
	}

	m.baseline = nil
	m.baselineScope = ""

	req := requestFor(endpoint, nil)
	raw := make([]Response, 0, m.cfg.BaselineSamples)
	for i := 0; i < m.cfg.BaselineSamples; i++ {
		resp, err := m.sendLocked(req)
		if err != nil {
			return Baseline{}, err
		}
		raw = append(raw, resp)
	}

	normalized := make([]Response, len(raw))
	for i := range raw {
		normalized[i] = normalizeResponse(raw[i])
	}

	first := normalized[0]
	firstFields := bodyFields(first.Body)
	firstFingerprint := fingerprint(first)
	for i := 1; i < len(normalized); i++ {
		fields := bodyFields(normalized[i].Body)
		if normalized[i].Status != first.Status ||
			len(normalized[i].Body) != len(first.Body) ||
			!equalStrings(fields, firstFields) ||
			fingerprint(normalized[i]) != firstFingerprint {
			return Baseline{}, ErrUnstableEndpoint
		}
	}

	base := Baseline{
		Endpoint:    endpoint.Name,
		Status:      first.Status,
		Length:      len(first.Body),
		Fields:      firstFields,
		Fingerprint: firstFingerprint,
		Samples:     m.cfg.BaselineSamples,
	}
	m.baseline = &base
	m.baselineScope = endpointScope(endpoint)
	return cloneBaseline(base), nil
}

// Candidates builds the ordered candidate list for one endpoint.
func (m *HiddenParamMiner) Candidates(endpoint Endpoint, seed []string) ([]ParamCandidate, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}

	type group struct {
		source string
		names  []string
	}
	groups := []group{
		{source: SourceSeed, names: seed},
		{source: SourceExisting, names: endpoint.Params},
		{source: SourcePage, names: pageParameterNames(endpoint.Body)},
	}

	seen := make(map[string]struct{})
	result := make([]ParamCandidate, 0)
	for _, g := range groups {
		localSeen := make(map[string]struct{})
		kept := make([]string, 0, len(g.names))
		for _, rawName := range g.names {
			name := strings.TrimSpace(rawName)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, ok := localSeen[key]; ok {
				continue
			}
			localSeen[key] = struct{}{}
			kept = append(kept, name)
		}
		sort.Strings(kept)
		for _, name := range kept {
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, ParamCandidate{Name: name, Source: g.source})
		}
	}
	if len(result) == 0 {
		return nil, ErrNoCandidates
	}
	return result, nil
}

// Probe injects one batch and attributes every change to single candidates.
func (m *HiddenParamMiner) Probe(endpoint Endpoint, batch []ParamCandidate) ([]ParamObservation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(batch) == 0 {
		return nil, ErrEmptyBatch
	}
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	scope := endpointScope(endpoint)
	if m.baseline == nil || m.baselineScope != scope {
		return nil, ErrNoBaseline
	}

	unique := make([]uniqueCandidate, 0, len(batch))
	inputToUnique := make([]int, len(batch))
	uniqueIndex := make(map[string]int)
	for i, candidate := range batch {
		key := strings.ToLower(strings.TrimSpace(candidate.Name))
		if key == "" {
			return nil, ErrNoCandidates
		}
		if idx, ok := uniqueIndex[key]; ok {
			inputToUnique[i] = idx
			continue
		}
		idx := len(unique)
		uniqueIndex[key] = idx
		inputToUnique[i] = idx
		unique = append(unique, uniqueCandidate{candidate: candidate, key: key})
	}

	results := make([]outcome, len(unique))
	pending := make([]int, 0, len(unique))
	for i := range unique {
		if cached, ok := m.cachedObservationLocked(scope, unique[i].key); ok {
			results[i] = outcome{observation: cached, found: true}
			continue
		}
		unique[i].sentinel = m.nextSentinelLocked(unique[i].candidate.Name)
		pending = append(pending, i)
	}

	if len(pending) > 0 {
		if m.transport == nil {
			return nil, ErrNoTransport
		}
		params := make([]Param, 0, len(pending))
		for _, idx := range pending {
			params = append(params, Param{Name: unique[idx].candidate.Name, Value: unique[idx].sentinel})
		}
		resp, err := m.sendLocked(requestFor(endpoint, params))
		if err != nil {
			return nil, err
		}
		if err := m.attributeLocked(endpoint, unique, pending, resp, 0, results); err != nil {
			return nil, err
		}
	}

	observations := make([]ParamObservation, 0, len(unique))
	for i := range unique {
		if !results[i].found {
			results[i] = outcome{observation: inertObservation(unique[i].candidate, unique[i].sentinel, *m.baseline), found: true}
		}
		observations = append(observations, cloneObservation(results[i].observation))
	}

	for _, idx := range pending {
		m.observations = append(m.observations, cloneObservation(observations[idx]))
		m.observationScopes = append(m.observationScopes, scope)
	}

	expanded := make([]ParamObservation, len(batch))
	for i, candidate := range batch {
		obs := cloneObservation(observations[inputToUnique[i]])
		obs.Name = candidate.Name
		obs.Source = candidate.Source
		expanded[i] = obs
	}
	return expanded, nil
}

// Judge turns the observations into accepted findings.
func (m *HiddenParamMiner) Judge(obs []ParamObservation) ([]ParamFinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(obs) == 0 {
		return nil, ErrNoObservations
	}
	if m.baseline == nil {
		return nil, ErrNoBaseline
	}

	findings := make([]ParamFinding, 0)
	for _, observation := range obs {
		if !observation.Diff && !observation.Reflected && (observation.Evidence == "" || observation.Evidence == EvidenceNone) {
			continue
		}
		finding := ParamFinding{
			Name:     observation.Name,
			Source:   observation.Source,
			Evidence: observation.Evidence,
			Delta:    observation.Length - m.baseline.Length,
		}
		findings = append(findings, finding)
		m.confirmLocked(observation, m.observationScopeLocked(observation))
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Name == findings[j].Name {
			return findings[i].Source < findings[j].Source
		}
		return findings[i].Name < findings[j].Name
	})
	return append([]ParamFinding(nil), findings...), nil
}

// Confirmed returns a copy of every accepted finding.
func (m *HiddenParamMiner) Confirmed() []ParamFinding {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ParamFinding(nil), m.confirmed...)
}

// Observations returns a copy of every recorded observation.
func (m *HiddenParamMiner) Observations() []ParamObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]ParamObservation, len(m.observations))
	for i := range m.observations {
		result[i] = cloneObservation(m.observations[i])
	}
	return result
}

// Budget reports how much of the request budget is still available.
func (m *HiddenParamMiner) Budget() (BudgetState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.budgetLocked(), nil
}

type snapshotData struct {
	Version               int                `json:"version"`
	Baseline              *Baseline          `json:"baseline,omitempty"`
	BaselineScope         string             `json:"baseline_scope,omitempty"`
	Observations          []ParamObservation `json:"observations"`
	ObservationScopes     []string           `json:"observation_scopes,omitempty"`
	Confirmed             []ParamFinding     `json:"confirmed"`
	ConfirmedObservations []ParamObservation `json:"confirmed_observations,omitempty"`
	ConfirmedScopes       []string           `json:"confirmed_scopes,omitempty"`
	Budget                BudgetState        `json:"budget"`
	SentinelSeq           uint64             `json:"sentinel_seq"`
}

const snapshotVersion = 1

// Snapshot serialises the baseline, the findings and the budget.
func (m *HiddenParamMiner) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var baseline *Baseline
	if m.baseline != nil {
		copy := cloneBaseline(*m.baseline)
		baseline = &copy
	}
	data := snapshotData{
		Version:               snapshotVersion,
		Baseline:              baseline,
		BaselineScope:         m.baselineScope,
		Observations:          cloneObservations(m.observations),
		ObservationScopes:     append([]string(nil), m.observationScopes...),
		Confirmed:             append([]ParamFinding(nil), m.confirmed...),
		ConfirmedObservations: cloneObservations(m.confirmedObservations),
		ConfirmedScopes:       append([]string(nil), m.confirmedScopes...),
		Budget:                m.budgetLocked(),
		SentinelSeq:           m.sentinelSeq,
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// Restore replaces the whole miner state, all or nothing.
func (m *HiddenParamMiner) Restore(data []byte) error {
	var restored snapshotData
	if err := json.Unmarshal(data, &restored); err != nil {
		return ErrBadSnapshot
	}
	if restored.Version != snapshotVersion || restored.Budget.Used < 0 {
		return ErrBadSnapshot
	}
	if restored.Baseline != nil && strings.TrimSpace(restored.Baseline.Endpoint) == "" {
		return ErrBadSnapshot
	}
	if len(restored.ObservationScopes) != 0 && len(restored.ObservationScopes) != len(restored.Observations) {
		return ErrBadSnapshot
	}
	if len(restored.ConfirmedScopes) != 0 && len(restored.ConfirmedScopes) != len(restored.ConfirmedObservations) {
		return ErrBadSnapshot
	}
	if len(restored.Confirmed) != 0 && len(restored.ConfirmedObservations) != 0 && len(restored.Confirmed) != len(restored.ConfirmedObservations) {
		return ErrBadSnapshot
	}

	observationScopes := append([]string(nil), restored.ObservationScopes...)
	if len(observationScopes) == 0 && len(restored.Observations) > 0 {
		observationScopes = make([]string, len(restored.Observations))
	}
	confirmedScopes := append([]string(nil), restored.ConfirmedScopes...)
	if len(confirmedScopes) == 0 && len(restored.ConfirmedObservations) > 0 {
		confirmedScopes = make([]string, len(restored.ConfirmedObservations))
	}
	var baseline *Baseline
	if restored.Baseline != nil {
		copy := cloneBaseline(*restored.Baseline)
		baseline = &copy
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.baseline = baseline
	m.baselineScope = restored.BaselineScope
	m.observations = cloneObservations(restored.Observations)
	m.observationScopes = observationScopes
	m.confirmed = append([]ParamFinding(nil), restored.Confirmed...)
	m.confirmedObservations = cloneObservations(restored.ConfirmedObservations)
	m.confirmedScopes = confirmedScopes
	m.used = restored.Budget.Used
	m.sentinelSeq = restored.SentinelSeq
	return nil
}

func (m *HiddenParamMiner) attributeLocked(endpoint Endpoint, unique []uniqueCandidate, indexes []int, resp Response, depth int, results []outcome) error {
	normalized := normalizeResponse(resp)
	diff, evidence := diffFromBaseline(*m.baseline, normalized)

	reflected := make([]bool, len(indexes))
	hasReflection := false
	for i, idx := range indexes {
		if strings.Contains(resp.Body, unique[idx].sentinel) {
			reflected[i] = true
			hasReflection = true
		}
	}

	if !diff && !hasReflection {
		for _, idx := range indexes {
			results[idx] = outcome{observation: inertObservation(unique[idx].candidate, unique[idx].sentinel, *m.baseline), found: true}
		}
		return nil
	}

	if hasReflection {
		for i, idx := range indexes {
			if reflected[i] {
				results[idx] = outcome{observation: responseObservation(unique[idx].candidate, unique[idx].sentinel, normalized, true, EvidenceReflected), found: true}
			} else {
				results[idx] = outcome{observation: inertObservation(unique[idx].candidate, unique[idx].sentinel, *m.baseline), found: true}
			}
		}
		return nil
	}

	if len(indexes) == 1 {
		idx := indexes[0]
		results[idx] = outcome{observation: responseObservation(unique[idx].candidate, unique[idx].sentinel, normalized, true, evidence), found: true}
		return nil
	}
	if depth >= m.cfg.MaxSplits {
		for _, idx := range indexes {
			results[idx] = outcome{observation: inertObservation(unique[idx].candidate, unique[idx].sentinel, *m.baseline), found: true}
		}
		return nil
	}

	mid := len(indexes) / 2
	left := indexes[:mid]
	right := indexes[mid:]
	leftResp, err := m.sendLocked(requestForParams(endpoint, unique, left))
	if err != nil {
		return err
	}
	if err := m.attributeLocked(endpoint, unique, left, leftResp, depth+1, results); err != nil {
		return err
	}
	rightResp, err := m.sendLocked(requestForParams(endpoint, unique, right))
	if err != nil {
		return err
	}
	return m.attributeLocked(endpoint, unique, right, rightResp, depth+1, results)
}

type uniqueCandidate struct {
	candidate ParamCandidate
	key       string
	sentinel  string
}

type outcome struct {
	observation ParamObservation
	found       bool
}

func validateEndpoint(endpoint Endpoint) error {
	if strings.TrimSpace(endpoint.Name) == "" || strings.TrimSpace(endpoint.Path) == "" {
		return ErrBadEndpoint
	}
	return nil
}

func requestFor(endpoint Endpoint, params []Param) Request {
	return Request{
		Method:      endpoint.Method,
		Path:        endpoint.Path,
		ContentType: endpoint.ContentType,
		Params:      append([]Param(nil), params...),
		Headers:     cloneHeaders(endpoint.Headers),
	}
}

func requestForParams(endpoint Endpoint, unique []uniqueCandidate, indexes []int) Request {
	params := make([]Param, 0, len(indexes))
	for _, idx := range indexes {
		params = append(params, Param{Name: unique[idx].candidate.Name, Value: unique[idx].sentinel})
	}
	return requestFor(endpoint, params)
}

func (m *HiddenParamMiner) sendLocked(req Request) (Response, error) {
	if m.transport == nil {
		return Response{}, ErrNoTransport
	}
	if m.used >= m.cfg.MaxRequests {
		return Response{}, ErrBudgetExhausted
	}
	m.used++
	return m.transport.Send(req)
}

func (m *HiddenParamMiner) budgetLocked() BudgetState {
	remaining := m.cfg.MaxRequests - m.used
	if remaining < 0 {
		remaining = 0
	}
	return BudgetState{Used: m.used, Limit: m.cfg.MaxRequests, Remaining: remaining}
}

func (m *HiddenParamMiner) nextSentinelLocked(name string) string {
	m.sentinelSeq++
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, name)
	if clean == "" {
		clean = "param"
	}
	return fmt.Sprintf("__hpm_%d_%s__", m.sentinelSeq, clean)
}

func (m *HiddenParamMiner) cachedObservationLocked(scope, key string) (ParamObservation, bool) {
	for i, finding := range m.confirmed {
		if !strings.EqualFold(finding.Name, key) || i >= len(m.confirmedScopes) || i >= len(m.confirmedObservations) {
			continue
		}
		if m.confirmedScopes[i] == scope {
			return cloneObservation(m.confirmedObservations[i]), true
		}
	}
	return ParamObservation{}, false
}

func (m *HiddenParamMiner) observationScopeLocked(observation ParamObservation) string {
	if observation.Sentinel != "" {
		for i, recorded := range m.observations {
			if i < len(m.observationScopes) && recorded.Sentinel == observation.Sentinel && strings.EqualFold(recorded.Name, observation.Name) {
				return m.observationScopes[i]
			}
		}
	}
	return m.baselineScope
}

func (m *HiddenParamMiner) confirmLocked(observation ParamObservation, scope string) {
	key := strings.ToLower(observation.Name)
	for i, finding := range m.confirmed {
		if !strings.EqualFold(finding.Name, key) {
			continue
		}
		if i < len(m.confirmedScopes) && m.confirmedScopes[i] == scope {
			return
		}
	}
	m.confirmed = append(m.confirmed, ParamFinding{
		Name:     observation.Name,
		Source:   observation.Source,
		Evidence: observation.Evidence,
		Delta:    observation.Length - m.baseline.Length,
	})
	m.confirmedObservations = append(m.confirmedObservations, cloneObservation(observation))
	m.confirmedScopes = append(m.confirmedScopes, scope)
}

func inertObservation(candidate ParamCandidate, sentinel string, baseline Baseline) ParamObservation {
	return ParamObservation{
		Name:     candidate.Name,
		Source:   candidate.Source,
		Status:   baseline.Status,
		Length:   baseline.Length,
		Fields:   cloneFields(baseline.Fields),
		Evidence: EvidenceNone,
		Sentinel: sentinel,
	}
}

func responseObservation(candidate ParamCandidate, sentinel string, resp Response, diff bool, evidence string) ParamObservation {
	return ParamObservation{
		Name:      candidate.Name,
		Source:    candidate.Source,
		Status:    resp.Status,
		Length:    len(resp.Body),
		Fields:    bodyFields(resp.Body),
		Reflected: evidence == EvidenceReflected,
		Location:  reflectionLocation(evidence),
		Diff:      diff,
		Evidence:  evidence,
		Sentinel:  sentinel,
	}
}

func reflectionLocation(evidence string) string {
	if evidence == EvidenceReflected {
		return "body"
	}
	return ""
}

func diffFromBaseline(baseline Baseline, resp Response) (bool, string) {
	if resp.Status != baseline.Status {
		return true, EvidenceStatus
	}
	fields := bodyFields(resp.Body)
	if !equalStrings(fields, baseline.Fields) {
		return true, EvidenceFields
	}
	if len(resp.Body) != baseline.Length {
		return true, EvidenceLength
	}
	if fingerprint(resp) != baseline.Fingerprint {
		return true, EvidenceFingerprint
	}
	return false, EvidenceNone
}

func bodyFields(body string) []string {
	seen := make(map[string]struct{})
	for _, part := range strings.Split(body, "&") {
		if part == "" {
			continue
		}
		key := part
		if eq := strings.IndexByte(part, '='); eq >= 0 {
			key = part[:eq]
		}
		key = strings.TrimSpace(key)
		if key != "" {
			seen[key] = struct{}{}
		}
	}
	fields := make([]string, 0, len(seen))
	for key := range seen {
		fields = append(fields, key)
	}
	sort.Strings(fields)
	return fields
}

func fingerprint(resp Response) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(resp.Status) + "\x00" + resp.Body))
	return hex.EncodeToString(sum[:])
}

func endpointScope(endpoint Endpoint) string {
	return strings.ToLower(strings.TrimSpace(endpoint.Name)) + "\x00" +
		endpoint.Method + "\x00" + endpoint.Path + "\x00" + strings.ToLower(endpoint.ContentType)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	copy := make(map[string]string, len(headers))
	for key, value := range headers {
		copy[key] = value
	}
	return copy
}

func cloneBaseline(baseline Baseline) Baseline {
	baseline.Fields = cloneFields(baseline.Fields)
	return baseline
}

func cloneObservation(observation ParamObservation) ParamObservation {
	observation.Fields = cloneFields(observation.Fields)
	return observation
}

func cloneFields(fields []string) []string {
	if fields == nil {
		return nil
	}
	result := make([]string, len(fields))
	copy(result, fields)
	return result
}

func cloneObservations(observations []ParamObservation) []ParamObservation {
	if len(observations) == 0 {
		return nil
	}
	result := make([]ParamObservation, len(observations))
	for i := range observations {
		result[i] = cloneObservation(observations[i])
	}
	return result
}

var (
	pageNameDouble = regexp.MustCompile(`(?i)\bname\s*=\s*"([^"]*)"`)
	pageNameSingle = regexp.MustCompile(`(?i)\bname\s*=\s*'([^']*)'`)
	reRFC3339      = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?\b`)
	reDate         = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
	reClock        = regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(?:\.\d+)?\b`)
	reUnix         = regexp.MustCompile(`\b\d{10}(?:\d{3})?\b`)
	reUUID         = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reHexToken     = regexp.MustCompile(`(?i)\b[0-9a-f]{16,}\b`)
	reLongToken    = regexp.MustCompile(`\b[A-Za-z0-9][A-Za-z0-9+/=_-]{19,}\b`)
	reJSONDynamic  = regexp.MustCompile(`(?i)("(?:ts|time|timestamp|nonce|token|request_?id|trace_?id|correlation_?id|session_?id|csrf|signature|rand|random|epoch)"\s*:\s*)"[^"]*"`)
)

const dynamicMarker = "__hpm_dynamic__"

func pageParameterNames(body string) []string {
	names := make([]string, 0)
	for _, match := range pageNameDouble.FindAllStringSubmatch(body, -1) {
		if len(match) > 1 {
			names = append(names, match[1])
		}
	}
	for _, match := range pageNameSingle.FindAllStringSubmatch(body, -1) {
		if len(match) > 1 {
			names = append(names, match[1])
		}
	}
	return names
}

func normalizeResponse(response Response) Response {
	response.Body = normalizeBody(response.Body)
	return response
}

func normalizeBody(body string) string {
	parts := strings.Split(body, "&")
	for i, part := range parts {
		eq := strings.IndexByte(part, '=')
		if eq > 0 {
			key := part[:eq]
			if isVolatileKey(key) {
				parts[i] = key + "=" + dynamicMarker
			} else {
				parts[i] = key + "=" + normalizeValue(part[eq+1:])
			}
			continue
		}
		parts[i] = normalizeValue(part)
	}
	body = strings.Join(parts, "&")
	body = reJSONDynamic.ReplaceAllString(body, `${1}"`+dynamicMarker+`"`)
	return body
}

func normalizeValue(value string) string {
	value = reRFC3339.ReplaceAllString(value, dynamicMarker)
	value = reDate.ReplaceAllString(value, dynamicMarker)
	value = reClock.ReplaceAllString(value, dynamicMarker)
	value = reUnix.ReplaceAllString(value, dynamicMarker)
	value = reUUID.ReplaceAllString(value, dynamicMarker)
	value = reHexToken.ReplaceAllString(value, dynamicMarker)
	value = reLongToken.ReplaceAllString(value, dynamicMarker)
	return value
}

func normalizeKey(key string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(key), `"'`))
}

func isVolatileKey(key string) bool {
	normalized := normalizeKey(key)
	switch strings.ReplaceAll(strings.ReplaceAll(normalized, "-", "_"), "[]", "") {
	case "ts", "_ts", "t", "_t", "time", "timestamp", "timestamp_ms", "date", "datetime", "now", "clock", "epoch", "expires", "expiry", "ttl", "age",
		"nonce", "_nonce", "token", "_token", "csrf", "csrf_token", "_csrf", "authenticity_token",
		"requestid", "request_id", "traceid", "trace_id", "correlationid", "correlation_id", "sessionid", "session_id", "session",
		"rand", "random", "rnd", "cb", "cachebuster", "cache_buster", "_", "sig", "signature", "seq", "sequence":
		return true
	default:
		return false
	}
}
