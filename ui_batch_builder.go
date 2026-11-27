// Package task merges UI draw commands into batches while keeping the
// hierarchy order, the clip stack and the per frame budget honest.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// MaxClipDepth is the hard cap on the nested clip stack.
const MaxClipDepth = 16

// Batch kinds written into UiBatch.Kind.
const (
	BatchKindQuad = "quad"
	BatchKindText = "text"
)

// Rect is an axis aligned clip rectangle in canvas pixels.
type Rect struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	W float32 `json:"w"`
	H float32 `json:"h"`
}

// UiQuad is one atlas quad submitted by a widget.
type UiQuad struct {
	Atlas    int `json:"atlas"`
	Material int `json:"material"`
	Sampler  int `json:"sampler"`
	Layer    int `json:"layer"`
	Verts    int `json:"verts"`
}

// Glyph is one shaped character of a text run.
type Glyph struct {
	Rune  rune `json:"rune"`
	Page  int  `json:"page"`
	Verts int  `json:"verts"`
}

// TextRun is one string shaped into glyphs, possibly across font pages.
type TextRun struct {
	Font   string  `json:"font"`
	Layer  int     `json:"layer"`
	Glyphs []Glyph `json:"glyphs"`
}

// UiBatch is one merged draw call.
type UiBatch struct {
	Index     int    `json:"index"`
	Kind      string `json:"kind"`
	Atlas     int    `json:"atlas"`
	Material  int    `json:"material"`
	Sampler   int    `json:"sampler"`
	Layer     int    `json:"layer"`
	ClipToken uint32 `json:"clip_token"`
	Page      int    `json:"page"`
	Items     int    `json:"items"`
	Verts     int    `json:"verts"`
}

// UiBudget bounds one frame.
type UiBudget struct {
	MaxBatches int `json:"max_batches"`
	MaxVerts   int `json:"max_verts"`
}

// UiStats is the per builder counter block.
type UiStats struct {
	Frame     int `json:"frame"`
	Quads     int `json:"quads"`
	TextRuns  int `json:"text_runs"`
	Batches   int `json:"batches"`
	Verts     int `json:"verts"`
	ClipDepth int `json:"clip_depth"`
}

// Errors returned by the builder.
var (
	ErrBadCanvas      = errors.New("canvas size must be positive")
	ErrBadClip        = errors.New("clip rectangle is not usable")
	ErrBadClipToken   = errors.New("clip token is zero, duplicated or unknown")
	ErrClipDepth      = errors.New("clip stack is too deep")
	ErrClipUnderflow  = errors.New("clip stack is empty")
	ErrClipMismatch   = errors.New("clip token does not match the stack top")
	ErrClipUnbalanced = errors.New("clip stack is not empty at build time")
	ErrBadQuad        = errors.New("quad is not usable")
	ErrBadText        = errors.New("text run is not usable")
	ErrBadBudget      = errors.New("batch budget is not usable")
	ErrBatchBudget    = errors.New("frame exceeds the batch or vertex budget")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// UiBatchBuilder owns the pending draw commands, the clip stack and the batch
// numbering of one UI frame.
type UiBatchBuilder struct {
	budget UiBudget
}

// NewUiBatchBuilder builds an empty builder with the default canvas.
func NewUiBatchBuilder() *UiBatchBuilder {
	return &UiBatchBuilder{}
}

// Reset drops every pending command and restarts batch numbering at zero.
func (b *UiBatchBuilder) Reset(frame int) {}

// SetCanvasSize sets the canvas the clip rectangles are checked against.
func (b *UiBatchBuilder) SetCanvasSize(w, h int) error {
	return errNotImplemented
}

// SetBudget replaces the per build batch and vertex caps.
func (b *UiBatchBuilder) SetBudget(budget UiBudget) error {
	return errNotImplemented
}

// PushClip opens one clip frame and makes its token current.
func (b *UiBatchBuilder) PushClip(rect Rect, token uint32) error {
	return errNotImplemented
}

// PopClip closes the clip frame carrying the given token.
func (b *UiBatchBuilder) PopClip(token uint32) error {
	return errNotImplemented
}

// AddQuad appends one atlas quad to the frame.
func (b *UiBatchBuilder) AddQuad(q UiQuad) error {
	return errNotImplemented
}

// AddTextRun appends one shaped string to the frame.
func (b *UiBatchBuilder) AddTextRun(r TextRun) error {
	return errNotImplemented
}

// Build merges the pending commands into draw batches.
func (b *UiBatchBuilder) Build() ([]UiBatch, error) {
	return nil, errNotImplemented
}

// BatchCount reports how many batches the last build produced.
func (b *UiBatchBuilder) BatchCount() int {
	return 0
}

// Stats reports the counters of the builder.
func (b *UiBatchBuilder) Stats() UiStats {
	return UiStats{}
}

// Snapshot serialises the clip stack, the canvas, the budget and the numbering.
func (b *UiBatchBuilder) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (b *UiBatchBuilder) Restore(data []byte) error {
	return errNotImplemented
}
