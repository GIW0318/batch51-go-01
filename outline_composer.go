// Package task composes selection outlines from a depth buffer, a normal
// buffer and a target mask, and keeps the per target colour and width unique
// under a per frame pixel budget.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Errors reported by the outline composer.
var (
	ErrBadConfig    = errors.New("outline configuration is invalid")
	ErrBadStyle     = errors.New("outline style is invalid")
	ErrStyleLimit   = errors.New("outline style table is full")
	ErrUnknownStyle = errors.New("outline style is not registered")
	ErrBadTexture   = errors.New("texture payload is invalid")
	ErrBadWidthPx   = errors.New("outline width is invalid")
	ErrBadOcclusion = errors.New("occlusion mode is unknown")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// RGBA is one premultiplied colour sample.
type RGBA struct {
	R float32 `json:"r"`
	G float32 `json:"g"`
	B float32 `json:"b"`
	A float32 `json:"a"`
}

// Vec3 is one direction, used by the normal buffer.
type Vec3 struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// OutlineStyle is the authored look of one target.
type OutlineStyle struct {
	ID       string  `json:"id"`
	Color    RGBA    `json:"color"`
	WidthPx  float32 `json:"width_px"`
	Priority int     `json:"priority"`
}

// OcclusionMode selects how occluded outline pixels are treated.
type OcclusionMode int

// Occlusion modes.
const (
	OcclusionNone OcclusionMode = iota
	OcclusionMasked
)

// DepthTex is the linear depth buffer of the frame.
type DepthTex struct {
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Data   []float32 `json:"data"`
}

// NormalTex is the world space normal buffer of the frame.
type NormalTex struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Data   []Vec3 `json:"data"`
}

// MaskTex tags every pixel with a target id; zero is background.
type MaskTex struct {
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Data   []uint8 `json:"data"`
}

// OutlineImage is the composed overlay.
type OutlineImage struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Pixels []RGBA `json:"pixels"`
}

// OutlineStats is the accounting of the last Compose call.
type OutlineStats struct {
	Targets    int     `json:"targets"`
	Pixels     int     `json:"pixels"`
	Samples    int     `json:"samples"`
	WidthPx    float32 `json:"width_px"`
	Downgraded int     `json:"downgraded"`
	Skipped    int     `json:"skipped"`
}

// OutlineConfig bounds the composer.
type OutlineConfig struct {
	MaxStyles       int     `json:"max_styles"`
	MaxWidthPx      float32 `json:"max_width_px"`
	DepthThreshold  float32 `json:"depth_threshold"`
	NormalThreshold float32 `json:"normal_threshold"`
	MaxPixels       int     `json:"max_pixels"`
	MaxTexPixels    int     `json:"max_tex_pixels"`
}

// OutlineComposer owns the style table, the selective set and the pixel
// budget accounting.
type OutlineComposer struct {
}

// NewOutlineComposer validates the configuration and creates an empty style
// table.
func NewOutlineComposer(cfg OutlineConfig) (*OutlineComposer, error) {
	return nil, errNotImplemented
}

// SetStyle installs or replaces one style and swaps the table atomically.
func (c *OutlineComposer) SetStyle(id string, st OutlineStyle) error {
	return errNotImplemented
}

// RemoveStyle drops one style from the table.
func (c *OutlineComposer) RemoveStyle(id string) error {
	return errNotImplemented
}

// SetSelective turns outline composition on or off for one target.
func (c *OutlineComposer) SetSelective(id string, on bool) error {
	return errNotImplemented
}

// Selective reports whether one target takes part in composition.
func (c *OutlineComposer) Selective(id string) bool {
	return false
}

// Priority reports the priority of one style, or minus one when unknown.
func (c *OutlineComposer) Priority(id string) int {
	return -1
}

// SetWidthPx sets the pipeline wide outline width.
func (c *OutlineComposer) SetWidthPx(px float32) error {
	return errNotImplemented
}

// SetOcclusionMode selects how occluded outline pixels are treated.
func (c *OutlineComposer) SetOcclusionMode(mode OcclusionMode) error {
	return errNotImplemented
}

// Compose writes the outline overlay of one frame.
func (c *OutlineComposer) Compose(depth DepthTex, normal NormalTex, mask MaskTex) (OutlineImage, error) {
	return OutlineImage{}, errNotImplemented
}

// Stats returns the accounting of the last Compose call.
func (c *OutlineComposer) Stats() OutlineStats {
	return OutlineStats{}
}

// Snapshot serialises the style table, the selective set, the width, the
// occlusion mode and the budget counters.
func (c *OutlineComposer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole composer state with a snapshot, all or nothing.
func (c *OutlineComposer) Restore(data []byte) error {
	return errNotImplemented
}
