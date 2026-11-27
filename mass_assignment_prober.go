// Package task probes create and update endpoints for fields the server
// accepts even though they were never meant to be writable.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Endpoint kinds.
const (
	KindCreate = "create"
	KindUpdate = "update"
)

// Field value types.
const (
	TypeString = "string"
	TypeInt    = "int"
	TypeBool   = "bool"
	TypeEnum   = "enum"
)

// Verdict severity levels.
const (
	SeverityNone   = "none"
	SeverityLow    = "low"
	SeverityMedium = "medium"
	SeverityHigh   = "high"
)

// Verdict reasons.
const (
	ReasonPersistedFields = "reason_persisted_fields"
	ReasonNoPersistence   = "reason_no_persistence"
)

// Errors reported by the prober.
var (
	ErrNilStore        = errors.New("store is nil")
	ErrBadEndpoint     = errors.New("endpoint is incomplete")
	ErrUnknownEndpoint = errors.New("endpoint is not declared in the catalog")
	ErrEmptyFieldSet   = errors.New("field set is empty")
	ErrBusy            = errors.New("endpoint is already being probed")
	ErrProbeBudget     = errors.New("probe budget is exhausted")
	ErrNoObservations  = errors.New("no observations to diff")
	ErrBadFinding      = errors.New("finding is incomplete")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Field is one declared field of an endpoint.
type Field struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Values   []string `json:"values,omitempty"`
	ReadOnly bool     `json:"read_only"`
}

// FieldSet is the candidate field list of one endpoint.
type FieldSet struct {
	Endpoint string  `json:"endpoint"`
	Fields   []Field `json:"fields"`
}

// Endpoint identifies one create or update entry point.
type Endpoint struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
	Baseline string `json:"baseline"`
}

// EndpointSpec is the catalog declaration of one endpoint.
type EndpointSpec struct {
	Endpoint Endpoint `json:"endpoint"`
	Fields   []Field  `json:"fields"`
}

// Catalog is the declared surface of the service.
type Catalog struct {
	Endpoints []EndpointSpec `json:"endpoints"`
}

// Store is the caller supplied facade over the target service.
type Store interface {
	Create(endpoint Endpoint, values map[string]string) (string, map[string]string, error)
	Update(endpoint Endpoint, id string, values map[string]string) (echo map[string]string, err error)
	Read(endpoint Endpoint, id string) (map[string]string, error)
	Delete(endpoint Endpoint, id string) error
}

// FieldObservation is the attributed result of injecting one field.
type FieldObservation struct {
	Endpoint      string `json:"endpoint"`
	Resource      string `json:"resource"`
	Field         string `json:"field"`
	BatchID       string `json:"batch_id"`
	Sent          string `json:"sent"`
	Echoed        bool   `json:"echoed"`
	ReadBack      string `json:"read_back"`
	Persisted     bool   `json:"persisted"`
	Batched       bool   `json:"batched"`
	Bisected      bool   `json:"bisected"`
	Object        string `json:"object"`
	CleanupFailed bool   `json:"cleanup_failed"`
}

// FieldFinding is the per endpoint summary of a probe run.
type FieldFinding struct {
	Endpoint       string   `json:"endpoint"`
	Resource       string   `json:"resource"`
	Fields         []string `json:"fields"`
	Persisted      []string `json:"persisted"`
	RequiresBisect bool     `json:"requires_bisect"`
	CleanupNeeded  bool     `json:"cleanup_needed"`
}

// MassAssignVerdict is the explainable result of one finding.
type MassAssignVerdict struct {
	Endpoint   string `json:"endpoint"`
	Confirmed  bool   `json:"confirmed"`
	Reason     string `json:"reason"`
	Severity   string `json:"severity"`
	Conclusive bool   `json:"conclusive"`
}

// Limits bounds probing.
type Limits struct {
	MaxProbes int
	BatchSize int
}

// BudgetState reports how much probing room is left.
type BudgetState struct {
	Probes         int `json:"probes"`
	MaxProbes      int `json:"max_probes"`
	Remaining      int `json:"remaining"`
	Batches        int `json:"batches"`
	CleanupPending int `json:"cleanup_pending"`
}

// MassAssignmentProber owns the catalog, the object ledger and the budget.
type MassAssignmentProber struct {
	// gate makes a snapshot wait for in-flight probes.  Probes take a read
	// lock, so probes against different endpoints still run concurrently.
	gate sync.RWMutex

	mu      sync.Mutex
	store   Store
	catalog Catalog
	limits  Limits
	active  map[string]bool
	created []string
	cleanup []string
	failed  map[string]bool
	ledger  []FieldObservation
	probes  int
	batches int
}

// NewMassAssignmentProber builds a prober over the given store and catalog.
func NewMassAssignmentProber(store Store, catalog Catalog, limits Limits) *MassAssignmentProber {
	if limits.BatchSize <= 0 {
		limits.BatchSize = 1
	}
	if limits.MaxProbes < 0 {
		limits.MaxProbes = 0
	}
	return &MassAssignmentProber{
		store:   store,
		catalog: cloneCatalog(catalog),
		limits:  limits,
		active:  make(map[string]bool),
		failed:  make(map[string]bool),
	}
}

// Catalog returns the declared endpoint catalog.
func (p *MassAssignmentProber) Catalog() Catalog {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneCatalog(p.catalog)
}

// Schema merges the declared fields of one endpoint with its siblings.
func (p *MassAssignmentProber) Schema(endpoint Endpoint) (FieldSet, error) {
	spec, err := p.endpointSpec(endpoint)
	if err != nil {
		return FieldSet{}, err
	}
	if p.store == nil {
		return FieldSet{}, ErrNilStore
	}

	p.mu.Lock()
	catalog := cloneCatalog(p.catalog)
	p.mu.Unlock()

	own := make(map[string]Field, len(spec.Fields))
	declared := make([]Field, 0, len(spec.Fields))
	for _, f := range spec.Fields {
		if _, exists := own[f.Name]; exists {
			continue
		}
		f.Values = append([]string(nil), f.Values...)
		own[f.Name] = f
		declared = append(declared, f)
	}

	merged := make(map[string]Field, len(declared))
	for _, f := range declared {
		merged[f.Name] = f
	}

	// Catalog order is not semantically important to callers, but sorting
	// siblings by endpoint id makes the choice deterministic when malformed
	// catalogs declare the same sibling-only field more than once.
	siblings := make([]EndpointSpec, 0)
	for _, candidate := range catalog.Endpoints {
		if candidate.Endpoint.ID == spec.Endpoint.ID {
			continue
		}
		if spec.Endpoint.Resource == "" || candidate.Endpoint.Resource != spec.Endpoint.Resource {
			continue
		}
		siblings = append(siblings, candidate)
	}
	sort.Slice(siblings, func(i, j int) bool {
		return siblings[i].Endpoint.ID < siblings[j].Endpoint.ID
	})

	for _, sibling := range siblings {
		for _, f := range sibling.Fields {
			if _, exists := merged[f.Name]; exists {
				// A field declared by the endpoint itself keeps that
				// endpoint's read-only flag.  Flags are never borrowed from
				// a sibling.
				continue
			}
			f.Values = append([]string(nil), f.Values...)
			merged[f.Name] = f
		}
	}

	fields := make([]Field, 0, len(merged))
	for _, f := range merged {
		fields = append(fields, f)
	}
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].Name < fields[j].Name
	})

	return FieldSet{Endpoint: spec.Endpoint.ID, Fields: fields}, nil
}

// Marker returns the injected value used for one field.
func (p *MassAssignmentProber) Marker(field Field) string {
	switch field.Type {
	case TypeString:
		return "gsb-" + field.Name + "-probe"
	case TypeInt:
		return "424242"
	case TypeBool:
		return "true"
	case TypeEnum:
		if len(field.Values) == 0 {
			return ""
		}
		return field.Values[len(field.Values)-1]
	default:
		return "gsb-" + field.Name + "-probe"
	}
}

// Probe injects the field set in batches and reads every field back.
func (p *MassAssignmentProber) Probe(endpoint Endpoint, fields FieldSet) ([]FieldObservation, error) {
	if endpoint.ID == "" {
		return nil, ErrBadEndpoint
	}

	spec, err := p.endpointSpec(endpoint)
	if err != nil {
		return nil, err
	}
	if p.store == nil {
		return nil, ErrNilStore
	}
	if len(fields.Fields) == 0 {
		return nil, ErrEmptyFieldSet
	}
	if fields.Endpoint != endpoint.ID {
		return nil, ErrBadEndpoint
	}

	canonical := spec.Endpoint
	if canonical.Kind == KindUpdate && canonical.Baseline == "" {
		return nil, ErrBadEndpoint
	}

	candidates := normaliseFields(fields.Fields)
	if len(candidates) == 0 {
		return nil, ErrEmptyFieldSet
	}

	// A snapshot is a whole-state operation.  Let independent endpoint probes
	// proceed together, but keep snapshots outside all of them.
	p.gate.RLock()
	defer p.gate.RUnlock()

	p.mu.Lock()
	if p.active[canonical.ID] {
		p.mu.Unlock()
		return nil, ErrBusy
	}
	p.active[canonical.ID] = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.active, canonical.ID)
		p.mu.Unlock()
	}()

	observations, probeErr := p.probeEndpoint(canonical, candidates)
	if len(observations) > 0 {
		p.mu.Lock()
		p.ledger = append(p.ledger, observations...)
		p.mu.Unlock()
	}
	if probeErr != nil {
		return nil, probeErr
	}
	return observations, nil
}

// Diff groups observations into per endpoint findings.
func (p *MassAssignmentProber) Diff(obs []FieldObservation) ([]FieldFinding, error) {
	if len(obs) == 0 {
		return nil, ErrNoObservations
	}

	type group struct {
		resource      string
		fields        map[string]bool
		persisted     map[string]bool
		cleanupNeeded bool
	}
	groups := make(map[string]*group)
	type batchStat struct {
		fields       map[string]bool
		unattributed bool
	}
	batchHits := make(map[string]*batchStat)
	for _, o := range obs {
		g := groups[o.Endpoint]
		if g == nil {
			g = &group{fields: make(map[string]bool), persisted: make(map[string]bool)}
			groups[o.Endpoint] = g
		}
		if g.resource == "" {
			g.resource = o.Resource
		}
		if o.Field != "" {
			g.fields[o.Field] = true
		}
		if o.Persisted {
			g.persisted[o.Field] = true
			if o.Batched {
				key := o.Endpoint + "\x00"
				if o.BatchID != "" {
					key += o.BatchID
				}
				stat := batchHits[key]
				if stat == nil {
					stat = &batchStat{fields: make(map[string]bool)}
					batchHits[key] = stat
				}
				stat.fields[o.Field] = true
				if !o.Bisected {
					stat.unattributed = true
				}
			}
		}
		if o.CleanupFailed {
			g.cleanupNeeded = true
		}
	}

	endpoints := make([]string, 0, len(groups))
	for endpoint := range groups {
		endpoints = append(endpoints, endpoint)
	}
	sort.Strings(endpoints)

	findings := make([]FieldFinding, 0, len(endpoints))
	for _, endpoint := range endpoints {
		g := groups[endpoint]
		f := FieldFinding{
			Endpoint:      endpoint,
			Resource:      g.resource,
			Fields:        sortedKeys(g.fields),
			Persisted:     sortedKeys(g.persisted),
			CleanupNeeded: g.cleanupNeeded,
		}
		for key, stat := range batchHits {
			if len(stat.fields) < 2 || !stat.unattributed {
				continue
			}
			prefix := endpoint + "\x00"
			if key == prefix || (len(key) > len(prefix) && key[:len(prefix)] == prefix) {
				f.RequiresBisect = true
				break
			}
		}
		findings = append(findings, f)
	}
	return findings, nil
}

// Judge grades one finding.
func (p *MassAssignmentProber) Judge(f FieldFinding) (MassAssignVerdict, error) {
	if f.Endpoint == "" {
		return MassAssignVerdict{}, ErrBadFinding
	}
	verdict := MassAssignVerdict{Endpoint: f.Endpoint, Severity: SeverityNone}
	if f.RequiresBisect {
		verdict.Conclusive = false
		return verdict, nil
	}

	count := len(sortedKeys(fieldNameSet(f.Persisted)))
	verdict.Conclusive = true
	if count == 0 {
		verdict.Reason = ReasonNoPersistence
		return verdict, nil
	}

	verdict.Confirmed = true
	verdict.Reason = ReasonPersistedFields
	switch {
	case count == 1:
		verdict.Severity = SeverityLow
	case count == 2:
		verdict.Severity = SeverityMedium
	default:
		verdict.Severity = SeverityHigh
	}
	return verdict, nil
}

// Budget reports the probe budget and the pending cleanups.
func (p *MassAssignmentProber) Budget() (BudgetState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	remaining := p.limits.MaxProbes - p.probes
	if remaining < 0 {
		remaining = 0
	}
	return BudgetState{
		Probes:         p.probes,
		MaxProbes:      p.limits.MaxProbes,
		Remaining:      remaining,
		Batches:        p.batches,
		CleanupPending: len(p.cleanup),
	}, nil
}

// Created lists the objects this prober created, oldest first.
func (p *MassAssignmentProber) Created() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.created...)
}

// CleanupState lists the objects whose cleanup failed.
func (p *MassAssignmentProber) CleanupState() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.cleanup...)
}

// Snapshot serialises the field ledger, the object ledger and the budget.
func (p *MassAssignmentProber) Snapshot() ([]byte, error) {
	p.gate.Lock()
	defer p.gate.Unlock()

	p.mu.Lock()
	payload := snapshotPayload{
		Version:      snapshotVersion,
		Limits:       p.limits,
		Probes:       p.probes,
		Batches:      p.batches,
		Created:      append([]string(nil), p.created...),
		Cleanup:      append([]string(nil), p.cleanup...),
		Failed:       make([]string, 0, len(p.failed)),
		Observations: append([]FieldObservation(nil), p.ledger...),
	}
	for id := range p.failed {
		payload.Failed = append(payload.Failed, id)
	}
	p.mu.Unlock()
	sort.Strings(payload.Failed)

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *MassAssignmentProber) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != snapshotVersion || payload.Probes < 0 || payload.Batches < 0 {
		return ErrBadSnapshot
	}
	if payload.Limits.MaxProbes < 0 || payload.Limits.BatchSize <= 0 {
		return ErrBadSnapshot
	}
	for _, id := range payload.Created {
		if id == "" {
			return ErrBadSnapshot
		}
	}
	for _, id := range payload.Cleanup {
		if id == "" {
			return ErrBadSnapshot
		}
	}
	for _, o := range payload.Observations {
		if o.Endpoint == "" || o.Field == "" {
			return ErrBadSnapshot
		}
	}

	created := append([]string(nil), payload.Created...)
	cleanup := append([]string(nil), payload.Cleanup...)
	observations := append([]FieldObservation(nil), payload.Observations...)
	failed := make(map[string]bool, len(payload.Failed)+len(cleanup))
	for _, id := range payload.Failed {
		failed[id] = true
	}
	// Older/foreign snapshots are not accepted, but keeping cleanup ids in
	// the failed set makes the two ledgers agree even if one is omitted.
	for _, id := range cleanup {
		failed[id] = true
	}

	p.gate.Lock()
	defer p.gate.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.limits = payload.Limits
	p.probes = payload.Probes
	p.batches = payload.Batches
	p.created = created
	p.cleanup = cleanup
	p.failed = failed
	p.ledger = observations
	return nil
}

const snapshotVersion = 1

type snapshotPayload struct {
	Version      int                `json:"version"`
	Limits       Limits             `json:"limits"`
	Probes       int                `json:"probes"`
	Batches      int                `json:"batches"`
	Created      []string           `json:"created,omitempty"`
	Cleanup      []string           `json:"cleanup,omitempty"`
	Failed       []string           `json:"failed,omitempty"`
	Observations []FieldObservation `json:"observations,omitempty"`
}

func cloneCatalog(catalog Catalog) Catalog {
	out := Catalog{Endpoints: make([]EndpointSpec, len(catalog.Endpoints))}
	for i, spec := range catalog.Endpoints {
		out.Endpoints[i].Endpoint = spec.Endpoint
		out.Endpoints[i].Fields = make([]Field, len(spec.Fields))
		for j, field := range spec.Fields {
			out.Endpoints[i].Fields[j] = field
			out.Endpoints[i].Fields[j].Values = append([]string(nil), field.Values...)
		}
	}
	return out
}

func (p *MassAssignmentProber) endpointSpec(endpoint Endpoint) (EndpointSpec, error) {
	if endpoint.ID == "" {
		return EndpointSpec{}, ErrBadEndpoint
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, spec := range p.catalog.Endpoints {
		if spec.Endpoint.ID != endpoint.ID {
			continue
		}
		out := EndpointSpec{Endpoint: spec.Endpoint, Fields: make([]Field, len(spec.Fields))}
		for i, field := range spec.Fields {
			out.Fields[i] = field
			out.Fields[i].Values = append([]string(nil), field.Values...)
		}
		return out, nil
	}
	return EndpointSpec{}, ErrUnknownEndpoint
}

func normaliseFields(in []Field) []Field {
	byName := make(map[string]Field, len(in))
	names := make([]string, 0, len(in))
	for _, field := range in {
		if field.Name == "" {
			continue
		}
		if _, exists := byName[field.Name]; exists {
			continue
		}
		field.Values = append([]string(nil), field.Values...)
		byName[field.Name] = field
		names = append(names, field.Name)
	}
	sort.Strings(names)
	out := make([]Field, 0, len(names))
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out
}

func (p *MassAssignmentProber) probeEndpoint(endpoint Endpoint, fields []Field) ([]FieldObservation, error) {
	var baseline map[string]string
	if endpoint.Kind == KindUpdate {
		current, err := p.store.Read(endpoint, endpoint.Baseline)
		if err != nil {
			return nil, err
		}
		baseline = copyStringMap(current)
	}

	p.mu.Lock()
	batchSize := p.limits.BatchSize
	p.mu.Unlock()
	if batchSize <= 0 {
		batchSize = 1
	}

	results := make(map[string]FieldObservation, len(fields))
	for start, batchIndex := 0, 1; start < len(fields); start, batchIndex = start+batchSize, batchIndex+1 {
		end := start + batchSize
		if end > len(fields) {
			end = len(fields)
		}
		batch := fields[start:end]
		batchID := fmt.Sprintf("%s-batch-%d", endpoint.ID, batchIndex)

		if err := p.reserveProbe(); err != nil {
			return flattenObservations(results), err
		}
		batchObs, err := p.executeBatch(endpoint, baseline, batch, batchID)
		if err != nil {
			// Keep the results obtained from earlier batches.  The caller
			// receives the error, but the field ledger still records what was
			// actually observed.
			return flattenObservations(results), err
		}
		batchCleanupFailed := false
		for _, observation := range batchObs {
			if observation.CleanupFailed {
				batchCleanupFailed = true
				break
			}
		}

		hits := make([]Field, 0, len(batchObs))
		for i, observation := range batchObs {
			if observation.Persisted {
				hits = append(hits, batch[i])
				continue
			}
			results[observation.Field] = observation
		}

		if len(hits) < 2 {
			for _, observation := range batchObs {
				if observation.Persisted {
					results[observation.Field] = observation
				}
			}
			continue
		}

		for i, field := range hits {
			bisectID := fmt.Sprintf("%s-bisect-%d-%d-%s", endpoint.ID, batchIndex, i+1, field.Name)
			if err := p.reserveProbe(); err != nil {
				// The batch observation is stronger evidence than dropping
				// the field entirely, so retain it until attribution succeeds.
				for _, observation := range batchObs {
					if observation.Persisted {
						results[observation.Field] = observation
					}
				}
				return flattenObservations(results), err
			}
			attributed, err := p.executeBatch(endpoint, baseline, []Field{field}, bisectID)
			if err != nil {
				for _, observation := range batchObs {
					if observation.Persisted {
						results[observation.Field] = observation
					}
				}
				return flattenObservations(results), err
			}
			observation := attributed[0]
			observation.Bisected = true
			observation.CleanupFailed = observation.CleanupFailed || batchCleanupFailed
			results[field.Name] = observation
		}
	}

	return flattenObservations(results), nil
}

func (p *MassAssignmentProber) executeBatch(endpoint Endpoint, baseline map[string]string, fields []Field, batchID string) ([]FieldObservation, error) {
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		values[field.Name] = p.Marker(field)
	}

	if endpoint.Kind == KindUpdate {
		echo, updateErr := p.store.Update(endpoint, endpoint.Baseline, values)
		var readBack map[string]string
		var readErr error
		if updateErr == nil {
			readBack, readErr = p.store.Read(endpoint, endpoint.Baseline)
		}
		restoreErr := p.restoreBaseline(endpoint, baseline)
		if updateErr != nil {
			return nil, updateErr
		}
		observations := observationsFor(endpoint, fields, batchID, len(fields) > 1, false, endpoint.Baseline, values, echo, readBack)
		if restoreErr != nil {
			for i := range observations {
				observations[i].CleanupFailed = true
			}
		}
		if readErr != nil {
			return observations, readErr
		}
		return observations, nil
	}

	object, echo, createErr := p.store.Create(endpoint, values)
	if createErr != nil {
		return nil, createErr
	}
	p.recordCreated(object)
	readBack, readErr := p.store.Read(endpoint, object)
	observations := observationsFor(endpoint, fields, batchID, len(fields) > 1, false, object, values, echo, readBack)
	if p.cleanupObject(endpoint, object) {
		for i := range observations {
			observations[i].CleanupFailed = true
		}
	}
	if readErr != nil {
		return observations, readErr
	}
	return observations, nil
}

func (p *MassAssignmentProber) restoreBaseline(endpoint Endpoint, baseline map[string]string) error {
	_, err := p.store.Update(endpoint, endpoint.Baseline, copyStringMap(baseline))
	return err
}

func observationsFor(endpoint Endpoint, fields []Field, batchID string, batched, bisected bool, object string, values, echo, readBack map[string]string) []FieldObservation {
	out := make([]FieldObservation, 0, len(fields))
	for _, field := range fields {
		sent := values[field.Name]
		_, echoed := echo[field.Name]
		readValue, read := readBack[field.Name]
		out = append(out, FieldObservation{
			Endpoint:  endpoint.ID,
			Resource:  endpoint.Resource,
			Field:     field.Name,
			BatchID:   batchID,
			Sent:      sent,
			Echoed:    echoed,
			ReadBack:  readValue,
			Persisted: read && readValue == sent,
			Batched:   batched,
			Bisected:  bisected,
			Object:    object,
		})
	}
	return out
}

func (p *MassAssignmentProber) reserveProbe() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.probes >= p.limits.MaxProbes {
		return ErrProbeBudget
	}
	p.probes++
	p.batches++
	return nil
}

func (p *MassAssignmentProber) recordCreated(object string) {
	p.mu.Lock()
	p.created = append(p.created, object)
	p.mu.Unlock()
}

func (p *MassAssignmentProber) cleanupObject(endpoint Endpoint, object string) bool {
	if err := p.store.Delete(endpoint, object); err != nil {
		p.mu.Lock()
		if !p.failed[object] {
			p.failed[object] = true
			p.cleanup = append(p.cleanup, object)
		}
		p.mu.Unlock()
		return true
	}
	return false
}

func flattenObservations(byField map[string]FieldObservation) []FieldObservation {
	names := make([]string, 0, len(byField))
	for name := range byField {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]FieldObservation, 0, len(names))
	for _, name := range names {
		out = append(out, byField[name])
	}
	return out
}

func copyStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func fieldNameSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
