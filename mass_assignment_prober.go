// Package task probes create and update endpoints for fields the server
// accepts even though they were never meant to be writable.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

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
	mu sync.Mutex
}

// NewMassAssignmentProber builds a prober over the given store and catalog.
func NewMassAssignmentProber(store Store, catalog Catalog, limits Limits) *MassAssignmentProber {
	_ = store
	_ = catalog
	_ = limits
	return &MassAssignmentProber{}
}

// Catalog returns the declared endpoint catalog.
func (p *MassAssignmentProber) Catalog() Catalog {
	return Catalog{}
}

// Schema merges the declared fields of one endpoint with its siblings.
func (p *MassAssignmentProber) Schema(endpoint Endpoint) (FieldSet, error) {
	return FieldSet{}, errNotImplemented
}

// Marker returns the injected value used for one field.
func (p *MassAssignmentProber) Marker(field Field) string {
	return ""
}

// Probe injects the field set in batches and reads every field back.
func (p *MassAssignmentProber) Probe(endpoint Endpoint, fields FieldSet) ([]FieldObservation, error) {
	return nil, errNotImplemented
}

// Diff groups observations into per endpoint findings.
func (p *MassAssignmentProber) Diff(obs []FieldObservation) ([]FieldFinding, error) {
	return nil, errNotImplemented
}

// Judge grades one finding.
func (p *MassAssignmentProber) Judge(f FieldFinding) (MassAssignVerdict, error) {
	return MassAssignVerdict{}, errNotImplemented
}

// Budget reports the probe budget and the pending cleanups.
func (p *MassAssignmentProber) Budget() (BudgetState, error) {
	return BudgetState{}, errNotImplemented
}

// Created lists the objects this prober created, oldest first.
func (p *MassAssignmentProber) Created() []string {
	return nil
}

// CleanupState lists the objects whose cleanup failed.
func (p *MassAssignmentProber) CleanupState() []string {
	return nil
}

// Snapshot serialises the field ledger, the object ledger and the budget.
func (p *MassAssignmentProber) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *MassAssignmentProber) Restore(data []byte) error {
	return errNotImplemented
}
