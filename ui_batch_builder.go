// Package task merges UI draw commands into batches while keeping the
// hierarchy order, the clip stack and the per frame budget honest.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sync"
)

// MaxClipDepth is the hard cap on the nested clip stack.
const MaxClipDepth = 16

// MaxPendingCommands and MaxPendingPrimitives bound how much work one frame
// may accumulate. A submission that would exceed either cap is rejected in
// full; existing pending work is never truncated.
const (
	MaxPendingCommands   = 1 << 16
	MaxPendingPrimitives = 1 << 20
)

const (
	defaultCanvasWidth  = 1920
	defaultCanvasHeight = 1080

	snapshotMagic   = "gsb-ui-batch-snapshot"
	snapshotVersion = 1
)

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

type pendingKind uint8

const (
	pendingQuad pendingKind = iota
	pendingText
)

type clipFrame struct {
	Rect  Rect   `json:"rect"`
	Token uint32 `json:"token"`
}

type pendingCommand struct {
	kind pendingKind
	quad UiQuad
	text TextRun
	clip []clipFrame
}

type batchKey struct {
	kind     string
	atlas    int
	material int
	sampler  int
	layer    int
	clip     []clipFrame
}

type batchWork struct {
	batch UiBatch
	key   batchKey
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotPending struct {
	Kind string      `json:"kind"`
	Quad UiQuad      `json:"quad,omitempty"`
	Text TextRun     `json:"text,omitempty"`
	Clip []clipFrame `json:"clip,omitempty"`
}

type snapshotState struct {
	CanvasWidth  int               `json:"canvas_width"`
	CanvasHeight int               `json:"canvas_height"`
	Budget       UiBudget          `json:"budget"`
	Frame        int               `json:"frame"`
	BatchNext    int               `json:"batch_next"`
	BatchCount   int               `json:"batch_count"`
	ClipStack    []clipFrame       `json:"clip_stack,omitempty"`
	Stats        UiStats           `json:"stats"`
	Pending      []snapshotPending `json:"pending,omitempty"`
}

// UiBatchBuilder owns the pending draw commands, the clip stack and the batch
// numbering of one UI frame.
type UiBatchBuilder struct {
	mu sync.Mutex

	canvasWidth  int
	canvasHeight int
	budget       UiBudget
	frame        int

	clipStack []clipFrame
	pending   []pendingCommand

	pendingPrimitives int
	batchNext         int
	batchCount        int
	stats             UiStats
}

// NewUiBatchBuilder builds an empty builder with the default canvas.
func NewUiBatchBuilder() *UiBatchBuilder {
	return &UiBatchBuilder{
		canvasWidth:  defaultCanvasWidth,
		canvasHeight: defaultCanvasHeight,
		budget: UiBudget{
			MaxBatches: int(^uint(0) >> 1),
			MaxVerts:   int(^uint(0) >> 1),
		},
	}
}

// Reset drops every pending command and restarts batch numbering at zero.
func (b *UiBatchBuilder) Reset(frame int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.frame = frame
	b.clipStack = nil
	b.pending = nil
	b.pendingPrimitives = 0
	b.batchNext = 0
	b.batchCount = 0
	b.stats = UiStats{Frame: frame}
}

// SetCanvasSize sets the canvas the clip rectangles are checked against.
func (b *UiBatchBuilder) SetCanvasSize(w, h int) error {
	if w <= 0 || h <= 0 {
		return ErrBadCanvas
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.canvasWidth = w
	b.canvasHeight = h
	return nil
}

// SetBudget replaces the per build batch and vertex caps.
func (b *UiBatchBuilder) SetBudget(budget UiBudget) error {
	if budget.MaxBatches <= 0 || budget.MaxVerts <= 0 {
		return ErrBadBudget
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.budget = budget
	return nil
}

// PushClip opens one clip frame and makes its token current.
func (b *UiBatchBuilder) PushClip(rect Rect, token uint32) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if token == 0 || b.hasClipToken(token) {
		return ErrBadClipToken
	}
	if !b.validRect(rect) {
		return ErrBadClip
	}
	if len(b.clipStack) >= MaxClipDepth {
		return ErrClipDepth
	}

	b.clipStack = append(b.clipStack, clipFrame{Rect: rect, Token: token})
	b.stats.ClipDepth = len(b.clipStack)
	return nil
}

// PopClip closes the clip frame carrying the given token.
func (b *UiBatchBuilder) PopClip(token uint32) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.clipStack) == 0 {
		return ErrClipUnderflow
	}
	if b.clipStack[len(b.clipStack)-1].Token != token {
		return ErrClipMismatch
	}

	b.clipStack[len(b.clipStack)-1] = clipFrame{}
	b.clipStack = b.clipStack[:len(b.clipStack)-1]
	b.stats.ClipDepth = len(b.clipStack)
	return nil
}

// AddQuad appends one atlas quad to the frame.
func (b *UiBatchBuilder) AddQuad(q UiQuad) error {
	if !validQuad(q) {
		return ErrBadQuad
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.pending) >= MaxPendingCommands ||
		b.pendingPrimitives >= MaxPendingPrimitives {
		return ErrBatchBudget
	}
	if b.stats.Verts > int(^uint(0)>>1)-q.Verts {
		return ErrBadQuad
	}

	b.pending = append(b.pending, pendingCommand{
		kind: pendingQuad,
		quad: q,
		clip: b.copyClipStack(),
	})
	b.pendingPrimitives++
	b.stats.Quads++
	b.stats.Verts += q.Verts
	return nil
}

// AddTextRun appends one shaped string to the frame.
func (b *UiBatchBuilder) AddTextRun(r TextRun) error {
	if !validTextRun(r) {
		return ErrBadText
	}

	sum, ok := sumGlyphVerts(r.Glyphs)
	if !ok {
		return ErrBadText
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.pending) >= MaxPendingCommands ||
		len(r.Glyphs) > MaxPendingPrimitives-b.pendingPrimitives {
		return ErrBatchBudget
	}
	if b.stats.Verts > int(^uint(0)>>1)-sum {
		return ErrBadText
	}

	b.pending = append(b.pending, pendingCommand{
		kind: pendingText,
		text: TextRun{
			Font:   r.Font,
			Layer:  r.Layer,
			Glyphs: append([]Glyph(nil), r.Glyphs...),
		},
		clip: b.copyClipStack(),
	})
	b.pendingPrimitives += len(r.Glyphs)
	b.stats.TextRuns++
	b.stats.Verts += sum
	return nil
}

// Build merges the pending commands into draw batches.
func (b *UiBatchBuilder) Build() ([]UiBatch, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.clipStack) != 0 {
		return nil, ErrClipUnbalanced
	}

	work, _, err := b.assemble()
	if err != nil {
		return nil, err
	}

	batches := make([]UiBatch, len(work))
	for i := range work {
		batches[i] = work[i].batch
		batches[i].Index = b.batchNext + i
	}

	b.batchNext += len(batches)
	b.batchCount = len(batches)
	b.stats.Batches = len(batches)
	b.stats.ClipDepth = 0
	b.pending = nil
	b.pendingPrimitives = 0
	return batches, nil
}

// BatchCount reports how many batches the last successful build produced.
func (b *UiBatchBuilder) BatchCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.batchCount
}

// Stats reports the counters of the builder.
func (b *UiBatchBuilder) Stats() UiStats {
	b.mu.Lock()
	defer b.mu.Unlock()

	stats := b.stats
	stats.Frame = b.frame
	stats.ClipDepth = len(b.clipStack)
	return stats
}

// Snapshot serialises the clip stack, the canvas, the budget and the numbering.
func (b *UiBatchBuilder) Snapshot() ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	pending := make([]snapshotPending, len(b.pending))
	for i, command := range b.pending {
		pending[i] = snapshotPending{
			Kind: commandKindName(command.kind),
			Clip: copyClipFrames(command.clip),
		}
		if command.kind == pendingQuad {
			pending[i].Quad = command.quad
		} else {
			pending[i].Text = TextRun{
				Font:   command.text.Font,
				Layer:  command.text.Layer,
				Glyphs: append([]Glyph(nil), command.text.Glyphs...),
			}
		}
	}

	state := snapshotState{
		CanvasWidth:  b.canvasWidth,
		CanvasHeight: b.canvasHeight,
		Budget:       b.budget,
		Frame:        b.frame,
		BatchNext:    b.batchNext,
		BatchCount:   b.batchCount,
		ClipStack:    copyClipFrames(b.clipStack),
		Stats:        b.stats,
		Pending:      pending,
	}

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, ErrBadSnapshot
	}

	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (b *UiBatchBuilder) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic ||
		envelope.Version != snapshotVersion ||
		len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}

	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil {
		return ErrBadSnapshot
	}
	if !validSnapshotState(state) {
		return ErrBadSnapshot
	}

	pending := make([]pendingCommand, len(state.Pending))
	pendingPrimitives := 0
	for i, saved := range state.Pending {
		command := pendingCommand{clip: copyClipFrames(saved.Clip)}
		switch saved.Kind {
		case BatchKindQuad:
			command.kind = pendingQuad
			command.quad = saved.Quad
			pendingPrimitives++
		case BatchKindText:
			command.kind = pendingText
			command.text = TextRun{
				Font:   saved.Text.Font,
				Layer:  saved.Text.Layer,
				Glyphs: append([]Glyph(nil), saved.Text.Glyphs...),
			}
			pendingPrimitives += len(command.text.Glyphs)
		default:
			return ErrBadSnapshot
		}
		pending[i] = command
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.canvasWidth = state.CanvasWidth
	b.canvasHeight = state.CanvasHeight
	b.budget = state.Budget
	b.frame = state.Frame
	b.clipStack = copyClipFrames(state.ClipStack)
	b.pending = pending
	b.pendingPrimitives = pendingPrimitives
	b.batchNext = state.BatchNext
	b.batchCount = state.BatchCount
	b.stats = state.Stats
	b.stats.Frame = state.Frame
	b.stats.ClipDepth = len(b.clipStack)
	b.stats.Batches = state.BatchCount
	return nil
}

func (b *UiBatchBuilder) assemble() ([]batchWork, int, error) {
	work := make([]batchWork, 0)
	totalVerts := 0

	appendBatch := func(key batchKey, kind string, atlas, material, sampler, layer, page, verts int) error {
		if !vertsFit(totalVerts, verts, b.budget.MaxVerts) ||
			len(work) >= b.budget.MaxBatches {
			return ErrBatchBudget
		}

		token := uint32(0)
		if len(key.clip) != 0 {
			token = key.clip[len(key.clip)-1].Token
		}
		work = append(work, batchWork{
			batch: UiBatch{
				Kind:      kind,
				Atlas:     atlas,
				Material:  material,
				Sampler:   sampler,
				Layer:     layer,
				ClipToken: token,
				Page:      page,
				Items:     1,
				Verts:     verts,
			},
			key: key,
		})
		totalVerts += verts
		return nil
	}

	appendToLast := func(verts int) error {
		if !vertsFit(totalVerts, verts, b.budget.MaxVerts) {
			return ErrBatchBudget
		}
		last := &work[len(work)-1]
		last.batch.Items++
		last.batch.Verts += verts
		totalVerts += verts
		return nil
	}

	var haveLast bool
	for _, command := range b.pending {
		switch command.kind {
		case pendingQuad:
			key := batchKey{
				kind:     BatchKindQuad,
				atlas:    command.quad.Atlas,
				material: command.quad.Material,
				sampler:  command.quad.Sampler,
				layer:    command.quad.Layer,
				clip:     command.clip,
			}
			if haveLast && sameBatchKey(work[len(work)-1].key, key) {
				if err := appendToLast(command.quad.Verts); err != nil {
					return nil, 0, err
				}
				continue
			}
			if err := appendBatch(key, BatchKindQuad, command.quad.Atlas,
				command.quad.Material, command.quad.Sampler, command.quad.Layer,
				0, command.quad.Verts); err != nil {
				return nil, 0, err
			}
			haveLast = true

		case pendingText:
			for _, glyph := range command.text.Glyphs {
				key := batchKey{
					kind:  BatchKindText,
					atlas: glyph.Page,
					layer: command.text.Layer,
					clip:  command.clip,
				}
				if haveLast && sameBatchKey(work[len(work)-1].key, key) {
					if err := appendToLast(glyph.Verts); err != nil {
						return nil, 0, err
					}
					continue
				}
				if err := appendBatch(key, BatchKindText, glyph.Page, 0, 0,
					command.text.Layer, glyph.Page, glyph.Verts); err != nil {
					return nil, 0, err
				}
				haveLast = true
			}

		default:
			return nil, 0, ErrBadQuad
		}
	}

	return work, totalVerts, nil
}

func validQuad(q UiQuad) bool {
	return q.Atlas >= 0 &&
		q.Material >= 0 &&
		q.Sampler >= 0 &&
		q.Layer >= 0 &&
		q.Verts > 0
}

func validTextRun(r TextRun) bool {
	if r.Layer < 0 || len(r.Glyphs) == 0 {
		return false
	}
	for _, glyph := range r.Glyphs {
		if glyph.Page < 0 || glyph.Verts <= 0 {
			return false
		}
	}
	return true
}

func sumGlyphVerts(glyphs []Glyph) (int, bool) {
	maxInt := int(^uint(0) >> 1)
	sum := 0
	for _, glyph := range glyphs {
		if glyph.Verts > maxInt-sum {
			return 0, false
		}
		sum += glyph.Verts
	}
	return sum, true
}

func vertsFit(total, add, limit int) bool {
	return add >= 0 && total <= limit && add <= limit-total
}

func (b *UiBatchBuilder) hasClipToken(token uint32) bool {
	for _, frame := range b.clipStack {
		if frame.Token == token {
			return true
		}
	}
	return false
}

func (b *UiBatchBuilder) validRect(rect Rect) bool {
	return validRectForCanvas(rect, b.canvasWidth, b.canvasHeight)
}

func (b *UiBatchBuilder) copyClipStack() []clipFrame {
	return copyClipFrames(b.clipStack)
}

func copyClipFrames(frames []clipFrame) []clipFrame {
	if len(frames) == 0 {
		return nil
	}
	out := make([]clipFrame, len(frames))
	copy(out, frames)
	return out
}

func sameBatchKey(a, b batchKey) bool {
	if a.kind != b.kind ||
		a.atlas != b.atlas ||
		a.material != b.material ||
		a.sampler != b.sampler ||
		a.layer != b.layer ||
		len(a.clip) != len(b.clip) {
		return false
	}
	for i := range a.clip {
		if a.clip[i] != b.clip[i] {
			return false
		}
	}
	return true
}

func commandKindName(kind pendingKind) string {
	if kind == pendingText {
		return BatchKindText
	}
	return BatchKindQuad
}

func validSnapshotState(state snapshotState) bool {
	if state.CanvasWidth <= 0 ||
		state.CanvasHeight <= 0 ||
		state.Budget.MaxBatches <= 0 ||
		state.Budget.MaxVerts <= 0 ||
		state.BatchNext < 0 ||
		state.BatchCount < 0 ||
		state.BatchCount > state.BatchNext ||
		state.Stats.Frame != state.Frame ||
		state.Stats.Quads < 0 ||
		state.Stats.TextRuns < 0 ||
		state.Stats.Verts < 0 ||
		state.Stats.Batches != state.BatchCount ||
		state.Stats.ClipDepth != len(state.ClipStack) ||
		!validClipFrames(state.ClipStack) {
		return false
	}

	pendingPrimitives := 0
	for _, saved := range state.Pending {
		if !validClipFrames(saved.Clip) {
			return false
		}
		switch saved.Kind {
		case BatchKindQuad:
			if !validQuad(saved.Quad) {
				return false
			}
			if pendingPrimitives == MaxPendingPrimitives {
				return false
			}
			pendingPrimitives++
		case BatchKindText:
			if !validTextRun(saved.Text) {
				return false
			}
			if len(saved.Text.Glyphs) > MaxPendingPrimitives-pendingPrimitives {
				return false
			}
			pendingPrimitives += len(saved.Text.Glyphs)
		default:
			return false
		}
	}
	return len(state.Pending) <= MaxPendingCommands
}

func validClipFrames(frames []clipFrame) bool {
	if len(frames) > MaxClipDepth {
		return false
	}
	seen := make(map[uint32]struct{}, len(frames))
	for _, frame := range frames {
		if frame.Token == 0 {
			return false
		}
		if _, ok := seen[frame.Token]; ok {
			return false
		}
		seen[frame.Token] = struct{}{}
		if !validClipRect(frame.Rect) {
			return false
		}
	}
	return true
}

func validClipRect(rect Rect) bool {
	x := float64(rect.X)
	y := float64(rect.Y)
	w := float64(rect.W)
	h := float64(rect.H)
	if math.IsNaN(x) || math.IsNaN(y) || math.IsNaN(w) || math.IsNaN(h) ||
		math.IsInf(x, 0) || math.IsInf(y, 0) ||
		math.IsInf(w, 0) || math.IsInf(h, 0) {
		return false
	}
	return w > 0 && h > 0 && x >= 0 && y >= 0 && x <= w && y <= h
}

func validRectForCanvas(rect Rect, canvasWidth, canvasHeight int) bool {
	x := float64(rect.X)
	y := float64(rect.Y)
	w := float64(rect.W)
	h := float64(rect.H)
	if !validClipRect(rect) {
		return false
	}
	return x+w <= float64(canvasWidth) && y+h <= float64(canvasHeight)
}
