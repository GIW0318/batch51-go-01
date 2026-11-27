// Package task builds XML entity probes, submits them to a registered XML
// endpoint and correlates echo, error and out of band evidence by probe id.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Entity payload kinds.
const (
	KindInternal  = "internal"
	KindExternal  = "external"
	KindParameter = "parameter"
)

// Probe status values.
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusUncertain = "uncertain"
	StatusRefused   = "refused"
)

// Evidence channels.
const (
	ChannelEcho  = "echo"
	ChannelError = "error"
	ChannelOOB   = "oob"
)

// Reasons attached to a finding.
const (
	ReasonEntityExpanded     = "entity_expanded"
	ReasonEntityNotExpanded  = "entity_not_expanded"
	ReasonCallbackCorrelated = "callback_correlated"
	ReasonCallbackMissing    = "callback_missing"
	ReasonBodyTruncated      = "body_truncated"
	ReasonParseRejected      = "parse_rejected"
)

// EchoMarker prefixes the inline expansion written into a response body.
const EchoMarker = "GSB-"

const collectorURLPrefix = "http://collector.gsb/"

// Errors returned by the prober.
var (
	ErrBadEndpoint       = errors.New("endpoint name is required")
	ErrDuplicateEndpoint = errors.New("endpoint is already registered")
	ErrUnknownEndpoint   = errors.New("endpoint is not registered")
	ErrBadProbeID        = errors.New("probe id is required")
	ErrBadPayloadKind    = errors.New("payload kind is not supported")
	ErrBadPayload        = errors.New("payload does not carry a doctype declaration")
	ErrBadCallback       = errors.New("callback does not carry a probe id")
	ErrUnknownProbe      = errors.New("callback does not match any probe")
	ErrProbeBudget       = errors.New("probe budget exhausted for endpoint")
	ErrCallbackBudget    = errors.New("callback table is full")
	ErrBadLimit          = errors.New("prober limits must be positive")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// ProberConfig bounds one prober instance.
type ProberConfig struct {
	MaxConcurrent        int
	MaxProbesPerEndpoint int
	CallbackWindow       int64
	MaxCallbacks         int
}

// Endpoint is the parser profile of one candidate XML entry.
type Endpoint struct {
	Name             string
	ExpandsGeneral   bool
	ExpandsParameter bool
	ResolvesExternal bool
	RejectsDoctype   bool
	MaxBodyBytes     int
	SourceAddr       string
}

// Payload is one generated entity probe.
type Payload struct {
	ProbeID string
	Kind    string
	Body    string
	Marker  string
}

// Response is what an endpoint returns for one submitted payload.
type Response struct {
	Status    int
	Body      string
	Truncated bool
	Rejected  bool
}

// Callback is one out of band hit reported by the shared listener.
type Callback struct {
	ProbeID    string
	SourceAddr string
	At         int64
	Payload    string
}

// Correlation joins one probe with the evidence collected for it.
type Correlation struct {
	ProbeID   string
	Endpoint  string
	Kind      string
	Status    string
	Channel   string
	Confirmed bool
	Late      bool
	Callbacks int
}

// ProbeRecord is one submitted payload kept for replay.
type ProbeRecord struct {
	Seq       int64
	ProbeID   string
	Endpoint  string
	Kind      string
	Submitted int64
	Deadline  int64
	Status    string
	Channel   string
	Callbacks int
	Echoed    bool

	truncated bool
}

// Finding is the graded outcome of one full probe.
type Finding struct {
	Endpoint  string
	Kind      string
	ProbeID   string
	Status    string
	Channel   string
	Confirmed bool
	Reason    string
	Callbacks int
}

// ProberStats counts the traffic of one endpoint.
type ProberStats struct {
	Probes       int64
	Callbacks    int64
	Correlated   int64
	Orphans      int64
	PeakInFlight int
}

type endpointStats struct {
	probes       int64
	callbacks    int64
	correlated   int64
	peakInFlight int
}

type callbackEntry struct {
	Callback
	Endpoint string
	Late     bool
}

// XxeEntityProber owns endpoints, probe records, callbacks and counters.
type XxeEntityProber struct {
	cfg ProberConfig

	mu   sync.Mutex
	cond *sync.Cond

	endpoints     []Endpoint
	endpointIndex map[string]int
	records       []ProbeRecord
	probeIndex    map[string]int
	callbacks     []callbackEntry
	stats         map[string]*endpointStats

	clock      int64
	seq        int64
	autoID     int64
	generation uint64
	inFlight   int
	orphans    int64
}

// NewXxeEntityProber builds a prober with the given caps.
func NewXxeEntityProber(cfg ProberConfig) *XxeEntityProber {
	p := &XxeEntityProber{cfg: cfg}
	p.init()
	return p
}

func (p *XxeEntityProber) init() {
	p.cond = sync.NewCond(&p.mu)
	p.endpointIndex = make(map[string]int)
	p.probeIndex = make(map[string]int)
	p.stats = make(map[string]*endpointStats)
}

func (p *XxeEntityProber) ensureInitLocked() {
	if p.cond == nil {
		p.cond = sync.NewCond(&p.mu)
	}
	if p.endpointIndex == nil {
		p.endpointIndex = make(map[string]int)
	}
	if p.probeIndex == nil {
		p.probeIndex = make(map[string]int)
	}
	if p.stats == nil {
		p.stats = make(map[string]*endpointStats)
	}
}

func (p *XxeEntityProber) validConfig() bool {
	return p.cfg.MaxConcurrent > 0 &&
		p.cfg.MaxProbesPerEndpoint > 0 &&
		p.cfg.CallbackWindow > 0 &&
		p.cfg.MaxCallbacks > 0
}

func normalizeEndpoint(endpoint string) string {
	return strings.TrimSpace(endpoint)
}

func validKind(kind string) bool {
	switch kind {
	case KindInternal, KindExternal, KindParameter:
		return true
	default:
		return false
	}
}

func validStatus(status string) bool {
	switch status {
	case StatusPending, StatusConfirmed, StatusUncertain, StatusRefused:
		return true
	default:
		return false
	}
}

func validChannel(channel string) bool {
	switch channel {
	case "", ChannelEcho, ChannelError, ChannelOOB:
		return true
	default:
		return false
	}
}

// RegisterEndpoint adds one parser profile.
func (p *XxeEntityProber) RegisterEndpoint(ep Endpoint) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	if !p.validConfig() {
		return ErrBadLimit
	}
	name := normalizeEndpoint(ep.Name)
	if name == "" {
		return ErrBadEndpoint
	}
	if _, exists := p.endpointIndex[name]; exists {
		return ErrDuplicateEndpoint
	}
	ep.Name = name
	p.endpointIndex[name] = len(p.endpoints)
	p.endpoints = append(p.endpoints, ep)
	p.stats[name] = &endpointStats{}
	return nil
}

// Endpoints lists the registered endpoint names in registration order.
func (p *XxeEntityProber) Endpoints() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	out := make([]string, 0, len(p.endpoints))
	for _, ep := range p.endpoints {
		out = append(out, ep.Name)
	}
	return out
}

// BuildPayload builds one entity payload that embeds the probe id.
func (p *XxeEntityProber) BuildPayload(kind, probeID string) (Payload, error) {
	id := strings.TrimSpace(probeID)
	if id == "" {
		return Payload{}, ErrBadProbeID
	}
	marker := EchoMarker + id
	switch kind {
	case KindInternal:
		body := fmt.Sprintf(`<?xml version="1.0"?><!DOCTYPE root [<!ENTITY %s "GSB-%s">]><root>&%s;</root>`, id, id, id)
		return Payload{ProbeID: id, Kind: kind, Body: body, Marker: marker}, nil
	case KindExternal:
		body := fmt.Sprintf(`<?xml version="1.0"?><!DOCTYPE root [<!ENTITY %s SYSTEM "%s%s">]><root>&%s;</root>`,
			id, collectorURLPrefix, id, id)
		return Payload{ProbeID: id, Kind: kind, Body: body, Marker: marker}, nil
	case KindParameter:
		body := fmt.Sprintf(`<?xml version="1.0"?><!DOCTYPE root [<!ENTITY %% %s SYSTEM "%s%s"> %%%s;]><root/>`,
			id, collectorURLPrefix, id, id)
		return Payload{ProbeID: id, Kind: kind, Body: body, Marker: marker}, nil
	default:
		return Payload{}, ErrBadPayloadKind
	}
}

// Submit posts one payload and records the evidence observed inline.
func (p *XxeEntityProber) Submit(endpoint string, payload Payload) (Response, error) {
	p.mu.Lock()
	p.ensureInitLocked()
	if !p.validConfig() {
		p.mu.Unlock()
		return Response{}, ErrBadLimit
	}

	name := normalizeEndpoint(endpoint)
	epIndex, ok := p.endpointIndex[name]
	if !ok {
		p.mu.Unlock()
		return Response{}, ErrUnknownEndpoint
	}
	if strings.TrimSpace(payload.ProbeID) == "" {
		p.mu.Unlock()
		return Response{}, ErrBadProbeID
	}
	if !validKind(payload.Kind) {
		p.mu.Unlock()
		return Response{}, ErrBadPayloadKind
	}
	if !strings.Contains(strings.ToUpper(payload.Body), "<!DOCTYPE") {
		p.mu.Unlock()
		return Response{}, ErrBadPayload
	}

	st := p.stats[name]
	if st.probes >= int64(p.cfg.MaxProbesPerEndpoint) {
		p.mu.Unlock()
		return Response{}, ErrProbeBudget
	}

	for p.inFlight >= p.cfg.MaxConcurrent {
		p.cond.Wait()
	}
	if st.probes >= int64(p.cfg.MaxProbesPerEndpoint) {
		p.mu.Unlock()
		return Response{}, ErrProbeBudget
	}

	submitted := p.clock
	recIndex := len(p.records)
	p.seq++
	record := ProbeRecord{
		Seq:       p.seq,
		ProbeID:   payload.ProbeID,
		Endpoint:  name,
		Kind:      payload.Kind,
		Submitted: submitted,
		Deadline:  submitted + p.cfg.CallbackWindow,
		Status:    StatusPending,
	}
	p.records = append(p.records, record)
	p.probeIndex[payload.ProbeID] = recIndex
	st.probes++
	p.inFlight++
	if p.inFlight > st.peakInFlight {
		st.peakInFlight = p.inFlight
	}
	gen := p.generation
	ep := p.endpoints[epIndex]
	p.mu.Unlock()

	resp, oob := simulateEndpoint(ep, payload)

	p.mu.Lock()
	if gen == p.generation && recIndex < len(p.records) {
		rec := &p.records[recIndex]
		if rec.Seq == record.Seq && rec.ProbeID == record.ProbeID {
			if resp.Truncated {
				rec.truncated = true
			}
			if oob {
				_, _ = p.deliverCallbackLocked(Callback{
					ProbeID:    payload.ProbeID,
					SourceAddr: ep.SourceAddr,
					At:         submitted,
					Payload:    collectorURLPrefix + payload.ProbeID,
				})
			}
			marker := payload.Marker
			if marker == "" {
				marker = EchoMarker + payload.ProbeID
			}
			if strings.Contains(resp.Body, marker) {
				rec.Echoed = true
				rec.Status = StatusConfirmed
				rec.Channel = ChannelEcho
			}
			if resp.Rejected && !rec.Echoed && rec.Callbacks == 0 {
				rec.Status = StatusRefused
				rec.Channel = ChannelError
			}
		}
		p.inFlight--
		p.cond.Broadcast()
	}
	p.mu.Unlock()

	return resp, nil
}

func simulateEndpoint(ep Endpoint, payload Payload) (Response, bool) {
	if ep.RejectsDoctype {
		return Response{Status: 400, Body: "doctype rejected", Rejected: true}, false
	}

	echo := false
	switch payload.Kind {
	case KindInternal:
		echo = ep.ExpandsGeneral
	case KindExternal:
		echo = ep.ExpandsGeneral && ep.ResolvesExternal
	case KindParameter:
		echo = false
	}

	body := "ok"
	if echo {
		body = EchoMarker + payload.ProbeID
	}
	truncated := false
	if ep.MaxBodyBytes > 0 && len(body) > ep.MaxBodyBytes {
		body = body[:ep.MaxBodyBytes]
		truncated = true
	}

	oob := false
	switch payload.Kind {
	case KindExternal:
		oob = ep.ResolvesExternal
	case KindParameter:
		oob = ep.ExpandsParameter
	}

	return Response{Status: 200, Body: body, Truncated: truncated}, oob
}

func extractProbeID(payload string) string {
	index := strings.Index(payload, collectorURLPrefix)
	if index < 0 {
		return ""
	}
	rest := payload[index+len(collectorURLPrefix):]
	rest = strings.TrimSpace(rest)
	if cut := strings.IndexAny(rest, " \t\r\n\"'<>?#"); cut >= 0 {
		rest = rest[:cut]
	}
	rest = strings.Trim(rest, "/")
	return rest
}

// DeliverCallback feeds one out of band hit into the shared listener table.
func (p *XxeEntityProber) DeliverCallback(cb Callback) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	if !p.validConfig() {
		return ErrBadLimit
	}
	_, err := p.deliverCallbackLocked(cb)
	return err
}

func (p *XxeEntityProber) deliverCallbackLocked(cb Callback) (Callback, error) {
	cb.ProbeID = strings.TrimSpace(cb.ProbeID)
	if cb.ProbeID == "" {
		cb.ProbeID = extractProbeID(cb.Payload)
	}
	if cb.ProbeID == "" {
		return cb, ErrBadCallback
	}

	index, ok := p.probeIndex[cb.ProbeID]
	if !ok || index < 0 || index >= len(p.records) {
		p.orphans++
		return cb, ErrUnknownProbe
	}
	if len(p.callbacks) >= p.cfg.MaxCallbacks {
		return cb, ErrCallbackBudget
	}

	rec := &p.records[index]
	late := cb.At > rec.Deadline
	p.callbacks = append(p.callbacks, callbackEntry{
		Callback: cb,
		Endpoint: rec.Endpoint,
		Late:     late,
	})
	rec.Callbacks++
	rec.Status = StatusConfirmed
	if !rec.Echoed {
		rec.Channel = ChannelOOB
	}

	st := p.stats[rec.Endpoint]
	if st == nil {
		st = &endpointStats{}
		p.stats[rec.Endpoint] = st
	}
	st.callbacks++
	st.correlated++
	return cb, nil
}

// Callbacks lists the callbacks received for one endpoint, oldest first.
func (p *XxeEntityProber) Callbacks(endpoint string) []Callback {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	name := normalizeEndpoint(endpoint)
	if _, ok := p.endpointIndex[name]; !ok {
		return nil
	}
	out := make([]Callback, 0)
	for _, entry := range p.callbacks {
		if entry.Endpoint == name {
			out = append(out, entry.Callback)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].At < out[j].At
	})
	return out
}

func (p *XxeEntityProber) correlationLocked(rec ProbeRecord) Correlation {
	channel := rec.Channel
	status := rec.Status
	confirmed := rec.Echoed || rec.Callbacks > 0 || status == StatusConfirmed
	if rec.Echoed {
		channel = ChannelEcho
	} else if rec.Callbacks > 0 {
		channel = ChannelOOB
	} else if status == StatusRefused {
		channel = ChannelError
	}
	if confirmed {
		status = StatusConfirmed
	}

	late := false
	for _, entry := range p.callbacks {
		if entry.ProbeID == rec.ProbeID && entry.Endpoint == rec.Endpoint && entry.Late {
			late = true
			break
		}
	}

	return Correlation{
		ProbeID:   rec.ProbeID,
		Endpoint:  rec.Endpoint,
		Kind:      rec.Kind,
		Status:    status,
		Channel:   channel,
		Confirmed: confirmed,
		Late:      late,
		Callbacks: rec.Callbacks,
	}
}

// Correlate joins probes and callbacks for one endpoint.
func (p *XxeEntityProber) Correlate(endpoint string) []Correlation {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	name := normalizeEndpoint(endpoint)
	out := make([]Correlation, 0)
	seen := make(map[string]int)
	for _, rec := range p.records {
		if rec.Endpoint != name {
			continue
		}
		correlation := p.correlationLocked(rec)
		if previous, exists := seen[rec.ProbeID]; exists {
			if (correlation.Confirmed && !out[previous].Confirmed) || correlation.Callbacks > out[previous].Callbacks {
				out[previous] = correlation
			}
			continue
		}
		seen[rec.ProbeID] = len(out)
		out = append(out, correlation)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return probeSeqLess(p, out[i].ProbeID, out[j].ProbeID)
	})
	return out
}

func probeSeqLess(p *XxeEntityProber, left, right string) bool {
	li, lok := p.probeIndex[left]
	ri, rok := p.probeIndex[right]
	if !lok || !rok {
		return left < right
	}
	return p.records[li].Seq < p.records[ri].Seq
}

// Probes lists the probe records of one endpoint, oldest first.
func (p *XxeEntityProber) Probes(endpoint string) []ProbeRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	name := normalizeEndpoint(endpoint)
	if _, ok := p.endpointIndex[name]; !ok {
		return nil
	}
	out := make([]ProbeRecord, 0)
	for _, rec := range p.records {
		if rec.Endpoint == name {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Seq < out[j].Seq
	})
	return out
}

func (p *XxeEntityProber) findingLocked(rec ProbeRecord) Finding {
	correlation := p.correlationLocked(rec)
	finding := Finding{
		Endpoint:  rec.Endpoint,
		Kind:      rec.Kind,
		ProbeID:   rec.ProbeID,
		Status:    correlation.Status,
		Channel:   correlation.Channel,
		Confirmed: correlation.Confirmed,
		Callbacks: correlation.Callbacks,
	}
	switch {
	case correlation.Confirmed && correlation.Channel == ChannelEcho:
		finding.Reason = ReasonEntityExpanded
	case correlation.Confirmed:
		finding.Reason = ReasonCallbackCorrelated
	case correlation.Status == StatusUncertain:
		finding.Reason = ReasonCallbackMissing
	case correlation.Status == StatusRefused:
		finding.Reason = ReasonParseRejected
	case rec.truncated:
		finding.Reason = ReasonBodyTruncated
	default:
		finding.Reason = ReasonEntityNotExpanded
	}
	return finding
}

// Probe builds, submits and grades one entity probe end to end.
func (p *XxeEntityProber) Probe(endpoint, kind string) (Finding, error) {
	p.mu.Lock()
	if !p.validConfig() {
		p.mu.Unlock()
		return Finding{}, ErrBadLimit
	}
	p.ensureInitLocked()
	id := p.nextProbeIDLocked()
	p.mu.Unlock()

	payload, err := p.BuildPayload(kind, id)
	if err != nil {
		return Finding{}, err
	}
	if _, err := p.Submit(endpoint, payload); err != nil {
		return Finding{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	index, ok := p.probeIndex[payload.ProbeID]
	if !ok || index < 0 || index >= len(p.records) {
		return Finding{}, ErrUnknownProbe
	}
	return p.findingLocked(p.records[index]), nil
}

func (p *XxeEntityProber) nextProbeIDLocked() string {
	for {
		p.autoID++
		id := fmt.Sprintf("probe-%d", p.autoID)
		if _, exists := p.probeIndex[id]; !exists {
			return id
		}
	}
}

// Advance moves the logical clock forward and returns the new time.
func (p *XxeEntityProber) Advance(ticks int64) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	if ticks > 0 {
		p.clock += ticks
	}
	return p.clock
}

// Expire marks every overdue probe of one endpoint as uncertain.
func (p *XxeEntityProber) Expire(endpoint string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	name := normalizeEndpoint(endpoint)
	if _, ok := p.endpointIndex[name]; !ok {
		return 0
	}
	changed := 0
	for index := range p.records {
		rec := &p.records[index]
		if rec.Endpoint != name || rec.Status != StatusPending {
			continue
		}
		if p.clock > rec.Deadline {
			rec.Status = StatusUncertain
			rec.Channel = ""
			changed++
		}
	}
	return changed
}

// Stats reads the counters of one endpoint.
func (p *XxeEntityProber) Stats(endpoint string) ProberStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	name := normalizeEndpoint(endpoint)
	if _, ok := p.endpointIndex[name]; !ok {
		return ProberStats{}
	}
	st := p.stats[name]
	if st == nil {
		return ProberStats{Orphans: p.orphans}
	}
	return ProberStats{
		Probes:       st.probes,
		Callbacks:    st.callbacks,
		Correlated:   st.correlated,
		Orphans:      p.orphans,
		PeakInFlight: st.peakInFlight,
	}
}

const snapshotVersion = 1

type snapshotRecord struct {
	Seq       int64  `json:"seq"`
	ProbeID   string `json:"probe_id"`
	Endpoint  string `json:"endpoint"`
	Kind      string `json:"kind"`
	Submitted int64  `json:"submitted"`
	Deadline  int64  `json:"deadline"`
	Status    string `json:"status"`
	Channel   string `json:"channel"`
	Callbacks int    `json:"callbacks"`
	Echoed    bool   `json:"echoed"`
	Truncated bool   `json:"truncated,omitempty"`
}

type snapshotCallback struct {
	ProbeID    string `json:"probe_id"`
	SourceAddr string `json:"source_addr"`
	At         int64  `json:"at"`
	Payload    string `json:"payload"`
	Endpoint   string `json:"endpoint"`
}

type snapshotStats struct {
	Probes       int64 `json:"probes"`
	Callbacks    int64 `json:"callbacks"`
	Correlated   int64 `json:"correlated"`
	PeakInFlight int   `json:"peak_in_flight"`
}

type snapshotDocument struct {
	Version   int                      `json:"version"`
	Clock     int64                    `json:"clock"`
	Seq       int64                    `json:"seq"`
	AutoID    int64                    `json:"auto_id"`
	Endpoints []Endpoint               `json:"endpoints"`
	Records   []snapshotRecord         `json:"records"`
	Callbacks []snapshotCallback       `json:"callbacks"`
	Stats     map[string]snapshotStats `json:"stats"`
	Orphans   int64                    `json:"orphans"`
}

// Snapshot serialises endpoints, records, callbacks and the clock.
func (p *XxeEntityProber) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureInitLocked()
	if !p.validConfig() {
		return nil, ErrBadLimit
	}

	doc := snapshotDocument{
		Version:   snapshotVersion,
		Clock:     p.clock,
		Seq:       p.seq,
		AutoID:    p.autoID,
		Endpoints: append([]Endpoint(nil), p.endpoints...),
		Records:   make([]snapshotRecord, 0, len(p.records)),
		Callbacks: make([]snapshotCallback, 0, len(p.callbacks)),
		Stats:     make(map[string]snapshotStats, len(p.stats)),
		Orphans:   p.orphans,
	}
	for _, rec := range p.records {
		doc.Records = append(doc.Records, snapshotRecord{
			Seq:       rec.Seq,
			ProbeID:   rec.ProbeID,
			Endpoint:  rec.Endpoint,
			Kind:      rec.Kind,
			Submitted: rec.Submitted,
			Deadline:  rec.Deadline,
			Status:    rec.Status,
			Channel:   rec.Channel,
			Callbacks: rec.Callbacks,
			Echoed:    rec.Echoed,
			Truncated: rec.truncated,
		})
	}
	for _, entry := range p.callbacks {
		doc.Callbacks = append(doc.Callbacks, snapshotCallback{
			ProbeID:    entry.ProbeID,
			SourceAddr: entry.SourceAddr,
			At:         entry.At,
			Payload:    entry.Payload,
			Endpoint:   entry.Endpoint,
		})
	}
	for name, st := range p.stats {
		doc.Stats[name] = snapshotStats{
			Probes:       st.probes,
			Callbacks:    st.callbacks,
			Correlated:   st.correlated,
			PeakInFlight: st.peakInFlight,
		}
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func badSnapshot(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadSnapshot, fmt.Sprintf(format, args...))
}

// Load replaces the whole state with a snapshot, all or nothing.
func (p *XxeEntityProber) Load(data []byte) error {
	p.mu.Lock()
	validConfig := p.validConfig()
	p.mu.Unlock()
	if !validConfig {
		return ErrBadLimit
	}
	if len(data) == 0 {
		return badSnapshot("empty payload")
	}

	var doc snapshotDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return badSnapshot("%v", err)
	}
	if doc.Version != snapshotVersion {
		return badSnapshot("unsupported version %d", doc.Version)
	}
	if doc.Clock < 0 || doc.Seq < 0 || doc.AutoID < 0 || doc.Orphans < 0 {
		return badSnapshot("negative clock or counter")
	}

	endpoints := make([]Endpoint, 0, len(doc.Endpoints))
	endpointIndex := make(map[string]int, len(doc.Endpoints))
	for _, ep := range doc.Endpoints {
		name := normalizeEndpoint(ep.Name)
		if name == "" {
			return badSnapshot("blank endpoint")
		}
		if _, exists := endpointIndex[name]; exists {
			return badSnapshot("duplicate endpoint %q", name)
		}
		ep.Name = name
		endpointIndex[name] = len(endpoints)
		endpoints = append(endpoints, ep)
	}

	records := make([]ProbeRecord, 0, len(doc.Records))
	probeIndex := make(map[string]int, len(doc.Records))
	recordCounts := make(map[string]int, len(endpoints))
	seenSeq := make(map[int64]bool, len(doc.Records))
	var maxSeq int64
	for _, item := range doc.Records {
		if strings.TrimSpace(item.ProbeID) == "" {
			return badSnapshot("blank probe id")
		}
		if _, ok := endpointIndex[item.Endpoint]; !ok {
			return badSnapshot("record refers to unknown endpoint %q", item.Endpoint)
		}
		if !validKind(item.Kind) {
			return badSnapshot("record has invalid kind %q", item.Kind)
		}
		if !validStatus(item.Status) {
			return badSnapshot("record has invalid status %q", item.Status)
		}
		if !validChannel(item.Channel) {
			return badSnapshot("record has invalid channel %q", item.Channel)
		}
		if item.Seq <= 0 || seenSeq[item.Seq] {
			return badSnapshot("invalid or duplicate sequence %d", item.Seq)
		}
		if item.Submitted < 0 || item.Deadline < item.Submitted {
			return badSnapshot("invalid timestamps for probe %q", item.ProbeID)
		}
		recordCounts[item.Endpoint]++
		if recordCounts[item.Endpoint] > p.cfg.MaxProbesPerEndpoint {
			return badSnapshot("probe budget exceeded for endpoint %q", item.Endpoint)
		}
		seenSeq[item.Seq] = true
		if item.Seq > maxSeq {
			maxSeq = item.Seq
		}
		records = append(records, ProbeRecord{
			Seq:       item.Seq,
			ProbeID:   item.ProbeID,
			Endpoint:  item.Endpoint,
			Kind:      item.Kind,
			Submitted: item.Submitted,
			Deadline:  item.Deadline,
			Status:    item.Status,
			Channel:   item.Channel,
			Echoed:    item.Echoed,
			truncated: item.Truncated,
		})
		probeIndex[item.ProbeID] = len(records) - 1
	}

	if len(doc.Callbacks) > p.cfg.MaxCallbacks {
		return badSnapshot("callback table exceeds configured cap")
	}
	callbacks := make([]callbackEntry, 0, len(doc.Callbacks))
	for _, item := range doc.Callbacks {
		probeID := strings.TrimSpace(item.ProbeID)
		if probeID == "" {
			probeID = extractProbeID(item.Payload)
		}
		if probeID == "" {
			return badSnapshot("callback has no probe id")
		}
		index, ok := probeIndex[probeID]
		if !ok {
			return badSnapshot("callback refers to unknown probe %q", probeID)
		}
		rec := &records[index]
		if item.Endpoint != "" && item.Endpoint != rec.Endpoint {
			return badSnapshot("callback endpoint does not match probe %q", probeID)
		}
		cb := Callback{
			ProbeID:    probeID,
			SourceAddr: item.SourceAddr,
			At:         item.At,
			Payload:    item.Payload,
		}
		rec.Callbacks++
		rec.Status = StatusConfirmed
		if !rec.Echoed {
			rec.Channel = ChannelOOB
		}
		callbacks = append(callbacks, callbackEntry{
			Callback: cb,
			Endpoint: rec.Endpoint,
			Late:     cb.At > rec.Deadline,
		})
	}
	for index := range records {
		rec := &records[index]
		if rec.Echoed {
			rec.Status = StatusConfirmed
			rec.Channel = ChannelEcho
		}
	}

	stats := make(map[string]*endpointStats, len(endpoints))
	for _, ep := range endpoints {
		stats[ep.Name] = &endpointStats{probes: int64(recordCounts[ep.Name])}
	}
	for _, entry := range callbacks {
		st := stats[entry.Endpoint]
		if st == nil {
			return badSnapshot("callback endpoint %q is not registered", entry.Endpoint)
		}
		st.callbacks++
		st.correlated++
	}
	for name, item := range doc.Stats {
		if _, ok := endpointIndex[name]; !ok {
			return badSnapshot("stats refer to unknown endpoint %q", name)
		}
		if item.PeakInFlight < 0 {
			return badSnapshot("negative peak in flight for endpoint %q", name)
		}
		stats[name].peakInFlight = item.PeakInFlight
	}

	seq := doc.Seq
	if seq < maxSeq {
		seq = maxSeq
	}

	p.mu.Lock()
	p.ensureInitLocked()
	p.endpoints = endpoints
	p.endpointIndex = endpointIndex
	p.records = records
	p.probeIndex = probeIndex
	p.callbacks = callbacks
	p.stats = stats
	p.clock = doc.Clock
	p.seq = seq
	p.autoID = doc.AutoID
	p.orphans = doc.Orphans
	p.inFlight = 0
	p.generation++
	p.cond.Broadcast()
	p.mu.Unlock()
	return nil
}
