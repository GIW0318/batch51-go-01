// Package task packs sprite frames into atlas pages with shelf allocation,
// padding, optional rotation and a stable incremental layout.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Errors reported by the atlas packer.
var (
	ErrBadConfig       = errors.New("atlas configuration is invalid")
	ErrBadSprite       = errors.New("sprite request is invalid")
	ErrDuplicateSprite = errors.New("sprite id is already packed")
	ErrUnknownSprite   = errors.New("sprite id is not packed")
	ErrBatchRejected   = errors.New("sprite batch rejected as a whole")
	ErrPageBudget      = errors.New("atlas page budget exceeded")
	ErrAreaBudget      = errors.New("atlas pixel area budget exceeded")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// SpriteReq is one frame the UI wants packed.
type SpriteReq struct {
	ID          string `json:"id"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	AllowRotate bool   `json:"allow_rotate"`
}

// Placement is the resolved position of one sprite inside one page.
type Placement struct {
	ID         string  `json:"id"`
	Page       int     `json:"page"`
	X          int     `json:"x"`
	Y          int     `json:"y"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Rotated    bool    `json:"rotated"`
	U0         float32 `json:"u0"`
	V0         float32 `json:"v0"`
	U1         float32 `json:"u1"`
	V1         float32 `json:"v1"`
	PageWidth  int     `json:"page_width"`
	PageHeight int     `json:"page_height"`
}

// AtlasConfig bounds the atlas.
type AtlasConfig struct {
	PageWidth  int   `json:"page_width"`
	PageHeight int   `json:"page_height"`
	MaxPages   int   `json:"max_pages"`
	Padding    int   `json:"padding"`
	MaxArea    int64 `json:"max_area"`
	MaxSprite  int   `json:"max_sprite"`
}

// RepackOptions overrides the layout for one full repack.
type RepackOptions struct {
	PageWidth   int  `json:"page_width"`
	PageHeight  int  `json:"page_height"`
	Padding     int  `json:"padding"`
	AllowRotate bool `json:"allow_rotate"`
}

// RepackReport describes a finished repack.
type RepackReport struct {
	Pages  int      `json:"pages"`
	Placed int      `json:"placed"`
	Moved  []string `json:"moved"`
}

// AtlasStats is the live accounting of the packer.
type AtlasStats struct {
	Sprites  int   `json:"sprites"`
	Pages    int   `json:"pages"`
	UsedArea int64 `json:"used_area"`
	PageArea int64 `json:"page_area"`
	Repacks  int   `json:"repacks"`
	Rejected int   `json:"rejected"`
}

// SpriteAtlasPacker owns the page shelves, the free rects and the published
// placements.
type SpriteAtlasPacker struct {
}

// NewSpriteAtlasPacker validates the configuration and creates an empty atlas.
func NewSpriteAtlasPacker(cfg AtlasConfig) (*SpriteAtlasPacker, error) {
	return nil, errNotImplemented
}

// Add places one sprite and returns its published placement.
func (p *SpriteAtlasPacker) Add(s SpriteReq) (Placement, error) {
	return Placement{}, errNotImplemented
}

// AddBatch places a batch and rejects the whole batch on any failure.
func (p *SpriteAtlasPacker) AddBatch(ss []SpriteReq) ([]Placement, error) {
	return nil, errNotImplemented
}

// Remove drops one sprite and leaves its rect free for later requests.
func (p *SpriteAtlasPacker) Remove(id string) error {
	return errNotImplemented
}

// Rebuild repacks every live sprite and switches pages atomically.
func (p *SpriteAtlasPacker) Rebuild(opts RepackOptions) (RepackReport, error) {
	return RepackReport{}, errNotImplemented
}

// PageCount reports how many pages are currently allocated.
func (p *SpriteAtlasPacker) PageCount() int {
	return 0
}

// SetPageBudget lowers or raises the page cap.
func (p *SpriteAtlasPacker) SetPageBudget(n int) error {
	return errNotImplemented
}

// Placement reads the published placement of one sprite.
func (p *SpriteAtlasPacker) Placement(id string) (Placement, bool) {
	return Placement{}, false
}

// Sprite reads the request that produced one placement.
func (p *SpriteAtlasPacker) Sprite(id string) (SpriteReq, bool) {
	return SpriteReq{}, false
}

// Stats returns the live accounting.
func (p *SpriteAtlasPacker) Stats() AtlasStats {
	return AtlasStats{}
}

// Snapshot serialises configuration, sprite order, pages and placements.
func (p *SpriteAtlasPacker) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole atlas with a snapshot, all or nothing.
func (p *SpriteAtlasPacker) Restore(data []byte) error {
	return errNotImplemented
}
