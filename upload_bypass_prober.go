// Package task probes file upload entry points for parser takeover.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Variant dimensions produced by Variants, in fixed order.
const (
	DimDoubleExtension = "double_extension"
	DimCaseExtension   = "case_extension"
	DimTrailingDot     = "trailing_dot"
	DimMimeMismatch    = "mime_mismatch"
	DimMagicPrefix     = "magic_prefix"
)

// Errors reported by the prober.
var (
	ErrBadForm          = errors.New("upload form action is required")
	ErrNoUploadPoint    = errors.New("form has no file field")
	ErrUnknownPoint     = errors.New("upload point was not enumerated")
	ErrVariantMismatch  = errors.New("variant belongs to another upload point")
	ErrVariantSubmitted = errors.New("variant has already been submitted")
	ErrUnknownUpload    = errors.New("upload is not registered")
	ErrStorageQuota     = errors.New("storage quota reached")
	ErrRequestBudget    = errors.New("request budget exhausted")
	ErrNoTarget         = errors.New("target is not installed")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Field is one input of the upload form.
type Field struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Accept   string `json:"accept"`
	MaxBytes int    `json:"max_bytes"`
}

// UploadForm is the parsed upload form.
type UploadForm struct {
	Action string  `json:"action"`
	Method string  `json:"method"`
	Fields []Field `json:"fields"`
}

// UploadPoint is one file input that accepts uploads.
type UploadPoint struct {
	Field    string   `json:"field"`
	Accept   []string `json:"accept"`
	MaxBytes int      `json:"max_bytes"`
}

// UploadVariant is one bypass payload built for a point.
type UploadVariant struct {
	ID        string `json:"id"`
	Point     string `json:"point"`
	Dimension string `json:"dimension"`
	Filename  string `json:"filename"`
	Declared  string `json:"declared"`
	Head      string `json:"head"`
	Body      string `json:"body"`
	Sentinel  string `json:"sentinel"`
}

// Stored is what the target reports after storing a payload.
type Stored struct {
	Path     string `json:"path"`
	Status   int    `json:"status"`
	Accepted bool   `json:"accepted"`
}

// Served is what the target serves back from a stored path.
type Served struct {
	Status  int    `json:"status"`
	Handler string `json:"handler"`
	Body    string `json:"body"`
}

// Target is the upload endpoint under test.
type Target interface {
	Upload(point UploadPoint, variant UploadVariant) (Stored, error)
	Fetch(path string) (Served, error)
}

// UploadResult is the outcome of one submission.
type UploadResult struct {
	Point     string `json:"point"`
	VariantID string `json:"variant_id"`
	Path      string `json:"path"`
	Status    int    `json:"status"`
	Accepted  bool   `json:"accepted"`
	Cleaned   bool   `json:"cleaned"`
}

// ExecProof is the evidence collected for one stored payload.
type ExecProof struct {
	VariantID  string `json:"variant_id"`
	Path       string `json:"path"`
	Handler    string `json:"handler"`
	Executable bool   `json:"executable"`
	Reason     string `json:"reason"`
}

// UploadVerdict is the judgement over one proof.
type UploadVerdict struct {
	VariantID string `json:"variant_id"`
	Hit       bool   `json:"hit"`
	Reason    string `json:"reason"`
}

// BudgetState reports how many requests were spent.
type BudgetState struct {
	Used      int `json:"used"`
	Limit     int `json:"limit"`
	Remaining int `json:"remaining"`
}

// ProberConfig bounds the prober and names the script handlers.
type ProberConfig struct {
	MaxUploads     int
	MaxRequests    int
	ScriptHandlers []string
}

// UploadBypassProber owns the points, the upload registry and the budget.
type UploadBypassProber struct {
	cfg ProberConfig
	mu  sync.Mutex
}

// NewUploadBypassProber builds a prober with the given limits.
func NewUploadBypassProber(cfg ProberConfig) *UploadBypassProber {
	return &UploadBypassProber{cfg: cfg}
}

// SetTarget installs the target used for uploads and read backs.
func (p *UploadBypassProber) SetTarget(t Target) error {
	return errNotImplemented
}

// Enumerate lists the upload points of one form.
func (p *UploadBypassProber) Enumerate(form UploadForm) ([]UploadPoint, error) {
	return nil, errNotImplemented
}

// Variants derives the bypass payloads for one point.
func (p *UploadBypassProber) Variants(point UploadPoint) ([]UploadVariant, error) {
	return nil, errNotImplemented
}

// Submit sends one variant to the target and registers the upload.
func (p *UploadBypassProber) Submit(point UploadPoint, variant UploadVariant) (UploadResult, error) {
	return UploadResult{}, errNotImplemented
}

// Verify reads the stored path back and decides whether a parser took over.
func (p *UploadBypassProber) Verify(result UploadResult) (ExecProof, error) {
	return ExecProof{}, errNotImplemented
}

// Judge turns one proof into a verdict.
func (p *UploadBypassProber) Judge(proof ExecProof) (UploadVerdict, error) {
	return UploadVerdict{}, errNotImplemented
}

// Cleanup releases the registered uploads.
func (p *UploadBypassProber) Cleanup() (int, error) {
	return 0, errNotImplemented
}

// Uploaded returns a copy of every registered upload.
func (p *UploadBypassProber) Uploaded() []UploadResult {
	return nil
}

// Budget reports how much of the request budget is still available.
func (p *UploadBypassProber) Budget() (BudgetState, error) {
	return BudgetState{}, errNotImplemented
}

// Snapshot serialises the registry, the submitted variants and the budget.
func (p *UploadBypassProber) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole prober state, all or nothing.
func (p *UploadBypassProber) Restore(data []byte) error {
	return errNotImplemented
}
