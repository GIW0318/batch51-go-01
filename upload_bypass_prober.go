// Package task probes file upload entry points for parser takeover.
package task

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

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

	target Target

	pointOrder []string
	points     map[string]UploadPoint

	variants  map[string][]UploadVariant
	submitted map[string]UploadVariant

	uploads     []UploadResult
	uploadIndex map[string]int
	budgetUsed  int
}

const snapshotVersion = 1

type proberSnapshot struct {
	Version    int             `json:"version"`
	Points     []UploadPoint   `json:"points"`
	Variants   []UploadVariant `json:"variants"`
	Submitted  []UploadVariant `json:"submitted"`
	Registry   []UploadResult  `json:"registry"`
	BudgetUsed int             `json:"budget_used"`
}

// NewUploadBypassProber builds a prober with the given limits.
func NewUploadBypassProber(cfg ProberConfig) *UploadBypassProber {
	cfg.ScriptHandlers = append([]string(nil), cfg.ScriptHandlers...)
	p := &UploadBypassProber{cfg: cfg}
	p.initLocked()
	return p
}

func (p *UploadBypassProber) initLocked() {
	if p.points == nil {
		p.points = make(map[string]UploadPoint)
	}
	if p.variants == nil {
		p.variants = make(map[string][]UploadVariant)
	}
	if p.submitted == nil {
		p.submitted = make(map[string]UploadVariant)
	}
	if p.uploadIndex == nil {
		p.uploadIndex = make(map[string]int)
	}
}

func isNilTarget(t Target) bool {
	if t == nil {
		return true
	}
	v := reflect.ValueOf(t)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// SetTarget installs the target used for uploads and read backs.
func (p *UploadBypassProber) SetTarget(t Target) error {
	if isNilTarget(t) {
		return ErrNoTarget
	}
	p.mu.Lock()
	p.target = t
	p.mu.Unlock()
	return nil
}

// Enumerate lists the upload points of one form.
func (p *UploadBypassProber) Enumerate(form UploadForm) ([]UploadPoint, error) {
	if strings.TrimSpace(form.Action) == "" {
		return nil, ErrBadForm
	}

	found := make([]UploadPoint, 0)
	for _, field := range form.Fields {
		if !strings.EqualFold(strings.TrimSpace(field.Type), "file") {
			continue
		}
		found = append(found, UploadPoint{
			Field:    field.Name,
			Accept:   parseAccept(field.Accept),
			MaxBytes: field.MaxBytes,
		})
	}
	if len(found) == 0 {
		return nil, ErrNoUploadPoint
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	for _, point := range found {
		point = copyPoint(point)
		if _, ok := p.points[point.Field]; !ok {
			p.pointOrder = append(p.pointOrder, point.Field)
		}
		p.points[point.Field] = point
	}
	return copyPoints(found), nil
}

func parseAccept(accept string) []string {
	out := make([]string, 0)
	for _, part := range strings.Split(accept, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func copyPoint(point UploadPoint) UploadPoint {
	point.Accept = append([]string(nil), point.Accept...)
	return point
}

func copyPoints(points []UploadPoint) []UploadPoint {
	out := make([]UploadPoint, len(points))
	for i, point := range points {
		out[i] = copyPoint(point)
	}
	return out
}

func copyVariants(variants []UploadVariant) []UploadVariant {
	return append([]UploadVariant(nil), variants...)
}

// Variants derives the bypass payloads for one point.
func (p *UploadBypassProber) Variants(point UploadPoint) ([]UploadVariant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	stored, ok := p.points[point.Field]
	if !ok {
		return nil, ErrUnknownPoint
	}
	variants, ok := p.variants[point.Field]
	if !ok {
		variants = buildVariants(stored)
		p.variants[point.Field] = variants
	}
	return copyVariants(variants), nil
}

func buildVariants(point UploadPoint) []UploadVariant {
	declared := primaryMIME(point)
	ext := extensionForMIME(declared)
	base := sanitizeToken(point.Field)
	if base == "" || base == "." || base == ".." {
		base = "upload"
	}
	fieldToken := hex.EncodeToString([]byte(point.Field))

	makeVariant := func(dimension, filename, head string) UploadVariant {
		sentinel := "GSB_SENTINEL_" + fieldToken + "_" + dimension
		body := "<?php echo '" + sentinel + "'; ?>"
		return UploadVariant{
			ID:        point.Field + ":" + dimension,
			Point:     point.Field,
			Dimension: dimension,
			Filename:  filename,
			Declared:  declared,
			Head:      head,
			Body:      body,
			Sentinel:  sentinel,
		}
	}

	return []UploadVariant{
		makeVariant(DimDoubleExtension, base+".php."+ext, ""),
		makeVariant(DimCaseExtension, base+".PHP", ""),
		makeVariant(DimTrailingDot, base+".php.", ""),
		makeVariant(DimMimeMismatch, base+"."+ext, ""),
		makeVariant(DimMagicPrefix, base+"_magic."+ext, "\x89PNG\r\n\x1a\n"),
	}
}

func primaryMIME(point UploadPoint) string {
	for _, accept := range point.Accept {
		if value := strings.TrimSpace(accept); value != "" {
			return value
		}
	}
	return "application/octet-stream"
}

func extensionForMIME(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/svg+xml":
		return "svg"
	case "application/pdf":
		return "pdf"
	case "text/plain":
		return "txt"
	case "application/zip":
		return "zip"
	}
	if slash := strings.LastIndex(mime, "/"); slash >= 0 && slash+1 < len(mime) {
		if ext := sanitizeToken(mime[slash+1:]); ext != "" {
			return ext
		}
	}
	return "bin"
}

func sanitizeToken(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func findVariant(variants []UploadVariant, id string) (UploadVariant, bool) {
	for _, variant := range variants {
		if variant.ID == id {
			return variant, true
		}
	}
	return UploadVariant{}, false
}

// Submit sends one variant to the target and registers the upload.
func (p *UploadBypassProber) Submit(point UploadPoint, variant UploadVariant) (UploadResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	storedPoint, ok := p.points[point.Field]
	if !ok {
		return UploadResult{}, ErrUnknownPoint
	}
	if variant.Point != point.Field {
		return UploadResult{}, ErrVariantMismatch
	}

	variants, ok := p.variants[point.Field]
	if !ok {
		variants = buildVariants(storedPoint)
		p.variants[point.Field] = variants
	}
	canonical, ok := findVariant(variants, variant.ID)
	if !ok {
		return UploadResult{}, ErrVariantMismatch
	}
	if variant.Dimension != "" && variant.Dimension != canonical.Dimension {
		return UploadResult{}, ErrVariantMismatch
	}

	if _, ok := p.submitted[variant.ID]; ok {
		return UploadResult{}, ErrVariantSubmitted
	}
	if isNilTarget(p.target) {
		return UploadResult{}, ErrNoTarget
	}
	if p.liveUploadsLocked() >= p.cfg.MaxUploads {
		return UploadResult{}, ErrStorageQuota
	}
	if p.budgetUsed >= p.cfg.MaxRequests {
		return UploadResult{}, ErrRequestBudget
	}

	p.budgetUsed++
	stored, err := p.target.Upload(storedPoint, variant)
	if err != nil {
		return UploadResult{}, err
	}

	result := UploadResult{
		Point:     point.Field,
		VariantID: variant.ID,
		Path:      stored.Path,
		Status:    stored.Status,
		Accepted:  stored.Accepted,
		Cleaned:   false,
	}
	// Keep the canonical definition. The caller may customise the bytes sent
	// to the target, but verification must always look for the sentinel that
	// belongs to this generated variant.
	p.submitted[variant.ID] = canonical
	p.uploads = append(p.uploads, result)
	p.uploadIndex[variant.ID] = len(p.uploads) - 1
	return result, nil
}

func (p *UploadBypassProber) liveUploadsLocked() int {
	live := 0
	for i := range p.uploads {
		if !p.uploads[i].Cleaned {
			live++
		}
	}
	return live
}

// Verify reads the stored path back and decides whether a parser took over.
func (p *UploadBypassProber) Verify(result UploadResult) (ExecProof, error) {
	p.mu.Lock()
	p.initLocked()

	if result.VariantID == "" {
		p.mu.Unlock()
		return ExecProof{}, ErrUnknownUpload
	}
	index, ok := p.uploadIndex[result.VariantID]
	if !ok || index < 0 || index >= len(p.uploads) {
		p.mu.Unlock()
		return ExecProof{}, ErrUnknownUpload
	}
	registered := p.uploads[index]
	variant, ok := p.submitted[result.VariantID]
	if !ok {
		p.mu.Unlock()
		return ExecProof{}, ErrUnknownUpload
	}
	target := p.target
	if isNilTarget(target) {
		p.mu.Unlock()
		return ExecProof{}, ErrNoTarget
	}
	if p.budgetUsed >= p.cfg.MaxRequests {
		p.mu.Unlock()
		return ExecProof{}, ErrRequestBudget
	}
	p.budgetUsed++
	p.mu.Unlock()

	served, err := target.Fetch(registered.Path)
	if err != nil {
		return ExecProof{}, err
	}

	handlerOK := isScriptHandler(served.Handler, p.cfg.ScriptHandlers)
	bodyOK := variant.Sentinel != "" && strings.Contains(served.Body, variant.Sentinel)
	executable := handlerOK && bodyOK

	reason := ""
	switch {
	case executable:
		reason = fmt.Sprintf("script handler %q executed the stored path and served sentinel %q", served.Handler, variant.Sentinel)
	case !handlerOK && !bodyOK:
		reason = fmt.Sprintf("handler %q is not a configured script handler and the response did not contain the upload sentinel", served.Handler)
	case !handlerOK:
		reason = fmt.Sprintf("handler %q is not a configured script handler; the stored file was only served as static content", served.Handler)
	default:
		reason = fmt.Sprintf("handler %q served the stored path but the response did not contain the upload sentinel", served.Handler)
	}

	return ExecProof{
		VariantID:  variant.ID,
		Path:       registered.Path,
		Handler:    served.Handler,
		Executable: executable,
		Reason:     reason,
	}, nil
}

func isScriptHandler(handler string, allowed []string) bool {
	handler = strings.TrimSpace(handler)
	if handler == "" {
		return false
	}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), handler) {
			return true
		}
	}
	return false
}

// Judge turns one proof into a verdict.
func (p *UploadBypassProber) Judge(proof ExecProof) (UploadVerdict, error) {
	if strings.TrimSpace(proof.VariantID) == "" {
		return UploadVerdict{}, ErrUnknownUpload
	}
	reason := strings.TrimSpace(proof.Reason)
	if reason == "" {
		if proof.Executable {
			reason = "evidence shows the uploaded payload was executed by a script handler"
		} else {
			reason = "evidence does not show executable parser takeover"
		}
	}
	return UploadVerdict{
		VariantID: proof.VariantID,
		Hit:       proof.Executable,
		Reason:    reason,
	}, nil
}

// Cleanup releases the registered uploads.
func (p *UploadBypassProber) Cleanup() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	cleaned := 0
	for i := range p.uploads {
		if !p.uploads[i].Cleaned {
			p.uploads[i].Cleaned = true
			cleaned++
		}
	}
	return cleaned, nil
}

// Uploaded returns a copy of every registered upload.
func (p *UploadBypassProber) Uploaded() []UploadResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()
	return append([]UploadResult(nil), p.uploads...)
}

// Budget reports how much of the request budget is still available.
func (p *UploadBypassProber) Budget() (BudgetState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	remaining := p.cfg.MaxRequests - p.budgetUsed
	if remaining < 0 {
		remaining = 0
	}
	return BudgetState{
		Used:      p.budgetUsed,
		Limit:     p.cfg.MaxRequests,
		Remaining: remaining,
	}, nil
}

// Snapshot serialises the registry, the submitted variants and the budget.
func (p *UploadBypassProber) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initLocked()

	snapshot := proberSnapshot{
		Version:    snapshotVersion,
		Points:     make([]UploadPoint, 0, len(p.pointOrder)),
		Variants:   make([]UploadVariant, 0),
		Submitted:  make([]UploadVariant, 0, len(p.submitted)),
		Registry:   append([]UploadResult(nil), p.uploads...),
		BudgetUsed: p.budgetUsed,
	}
	for _, field := range p.pointOrder {
		point, ok := p.points[field]
		if ok {
			snapshot.Points = append(snapshot.Points, copyPoint(point))
		}
		if variants, ok := p.variants[field]; ok {
			snapshot.Variants = append(snapshot.Variants, variants...)
		}
	}
	submittedIDs := make([]string, 0, len(p.submitted))
	for id := range p.submitted {
		submittedIDs = append(submittedIDs, id)
	}
	sort.Strings(submittedIDs)
	for _, id := range submittedIDs {
		snapshot.Submitted = append(snapshot.Submitted, p.submitted[id])
	}
	if snapshot.Registry == nil {
		snapshot.Registry = make([]UploadResult, 0)
	}
	return json.Marshal(snapshot)
}

// Restore replaces the whole prober state, all or nothing.
func (p *UploadBypassProber) Restore(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return ErrBadSnapshot
	}
	for _, required := range []string{"version", "points", "variants", "submitted", "registry", "budget_used"} {
		if _, ok := fields[required]; !ok {
			return ErrBadSnapshot
		}
	}
	var snapshot proberSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	if snapshot.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if snapshot.Points == nil || snapshot.Variants == nil || snapshot.Submitted == nil || snapshot.Registry == nil {
		return ErrBadSnapshot
	}
	if snapshot.BudgetUsed < 0 {
		return ErrBadSnapshot
	}

	points := make(map[string]UploadPoint, len(snapshot.Points))
	order := make([]string, 0, len(snapshot.Points))
	for _, point := range snapshot.Points {
		if strings.TrimSpace(point.Field) == "" {
			return ErrBadSnapshot
		}
		if _, ok := points[point.Field]; ok {
			return ErrBadSnapshot
		}
		point = copyPoint(point)
		points[point.Field] = point
		order = append(order, point.Field)
	}

	variants := make(map[string][]UploadVariant)
	seenVariants := make(map[string]bool)
	for _, variant := range snapshot.Variants {
		if variant.ID == "" || variant.Point == "" {
			return ErrBadSnapshot
		}
		if _, ok := points[variant.Point]; !ok {
			return ErrBadSnapshot
		}
		if seenVariants[variant.ID] {
			return ErrBadSnapshot
		}
		seenVariants[variant.ID] = true
		variants[variant.Point] = append(variants[variant.Point], variant)
	}

	submitted := make(map[string]UploadVariant, len(snapshot.Submitted))
	for _, variant := range snapshot.Submitted {
		if variant.ID == "" || variant.Point == "" || variant.Sentinel == "" {
			return ErrBadSnapshot
		}
		if _, ok := points[variant.Point]; !ok {
			return ErrBadSnapshot
		}
		if _, ok := submitted[variant.ID]; ok {
			return ErrBadSnapshot
		}
		if !seenVariants[variant.ID] {
			variants[variant.Point] = append(variants[variant.Point], variant)
			seenVariants[variant.ID] = true
		}
		submitted[variant.ID] = variant
	}

	// A snapshot may carry only the variants that were already submitted.
	// Rebuild the canonical five-dimension set for every point and overlay the
	// persisted definitions so Variants stays stable after a restart.
	for _, field := range order {
		point := points[field]
		if len(variants[field]) == 0 {
			continue
		}
		persisted := make(map[string]UploadVariant, len(variants[field]))
		for _, variant := range variants[field] {
			persisted[variant.ID] = variant
		}
		canonical := buildVariants(point)
		merged := make([]UploadVariant, 0, len(canonical)+len(persisted))
		seen := make(map[string]bool, len(canonical))
		for _, variant := range canonical {
			if saved, ok := persisted[variant.ID]; ok {
				variant = saved
			}
			merged = append(merged, variant)
			seen[variant.ID] = true
		}
		for id, variant := range persisted {
			if !seen[id] {
				merged = append(merged, variant)
			}
		}
		variants[field] = merged
	}

	registry := make([]UploadResult, 0, len(snapshot.Registry))
	uploadIndex := make(map[string]int, len(snapshot.Registry))
	for _, result := range snapshot.Registry {
		if result.VariantID == "" {
			return ErrBadSnapshot
		}
		submittedVariant, ok := submitted[result.VariantID]
		if !ok {
			return ErrBadSnapshot
		}
		if result.Point != "" {
			if _, ok := points[result.Point]; !ok {
				return ErrBadSnapshot
			}
			if result.Point != submittedVariant.Point {
				return ErrBadSnapshot
			}
		}
		if _, ok := uploadIndex[result.VariantID]; ok {
			return ErrBadSnapshot
		}
		uploadIndex[result.VariantID] = len(registry)
		registry = append(registry, result)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.pointOrder = order
	p.points = points
	p.variants = variants
	p.submitted = submitted
	p.uploads = registry
	p.uploadIndex = uploadIndex
	p.budgetUsed = snapshot.BudgetUsed
	return nil
}
