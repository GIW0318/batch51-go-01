// Package task packs sprite frames into atlas pages with shelf allocation,
// padding, optional rotation and a stable incremental layout.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
)

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

type atlasRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type atlasShelf struct {
	Y      int `json:"y"`
	Height int `json:"height"`
	NextX  int `json:"next_x"`
}

type atlasPage struct {
	Width   int          `json:"width"`
	Height  int          `json:"height"`
	Shelves []atlasShelf `json:"shelves"`
	Holes   []atlasRect  `json:"holes"`
}

type atlasState struct {
	cfg        AtlasConfig
	pages      []atlasPage
	order      []string
	sprites    map[string]SpriteReq
	placements map[string]Placement
	usedArea   int64
	pageArea   int64
	repacks    int
	rejected   int
}

type placementCandidate struct {
	page     int
	x        int
	y        int
	width    int
	height   int
	rotated  bool
	hole     int
	shelf    int
	newShelf atlasShelf
	newPage  bool
}

// SpriteAtlasPacker owns the page shelves, the free rects and the published
// placements.
type SpriteAtlasPacker struct {
	mu sync.RWMutex
	st atlasState
}

// NewSpriteAtlasPacker validates the configuration and creates an empty atlas.
func NewSpriteAtlasPacker(cfg AtlasConfig) (*SpriteAtlasPacker, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return &SpriteAtlasPacker{
		st: atlasState{
			cfg:        cfg,
			sprites:    make(map[string]SpriteReq),
			placements: make(map[string]Placement),
		},
	}, nil
}

// Add places one sprite and returns its published placement.
func (p *SpriteAtlasPacker) Add(s SpriteReq) (Placement, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := validateSprite(s, p.st.cfg.MaxSprite); err != nil {
		p.st.rejected++
		return Placement{}, err
	}
	if _, exists := p.st.sprites[s.ID]; exists {
		p.st.rejected++
		return Placement{}, ErrDuplicateSprite
	}
	pl, err := p.st.place(s, s.AllowRotate)
	if err != nil {
		p.st.rejected++
		return Placement{}, err
	}
	return pl, nil
}

// AddBatch places a batch and rejects the whole batch on any failure.
func (p *SpriteAtlasPacker) AddBatch(ss []SpriteReq) ([]Placement, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(ss) == 0 {
		return []Placement{}, nil
	}
	seen := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		if err := validateSprite(s, p.st.cfg.MaxSprite); err != nil {
			p.st.rejected += len(ss)
			return nil, ErrBatchRejected
		}
		if _, exists := p.st.sprites[s.ID]; exists {
			p.st.rejected += len(ss)
			return nil, ErrBatchRejected
		}
		if _, exists := seen[s.ID]; exists {
			p.st.rejected += len(ss)
			return nil, ErrBatchRejected
		}
		seen[s.ID] = struct{}{}
	}

	work := cloneState(p.st)
	out := make([]Placement, 0, len(ss))
	for _, s := range ss {
		pl, err := work.place(s, s.AllowRotate)
		if err != nil {
			p.st.rejected += len(ss)
			return nil, ErrBatchRejected
		}
		out = append(out, pl)
	}
	p.st = work
	return out, nil
}

// Remove drops one sprite and leaves its rect free for later requests.
func (p *SpriteAtlasPacker) Remove(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	pl, ok := p.st.placements[id]
	if !ok {
		return ErrUnknownSprite
	}
	delete(p.st.placements, id)
	delete(p.st.sprites, id)
	for i, ordered := range p.st.order {
		if ordered == id {
			p.st.order = append(p.st.order[:i], p.st.order[i+1:]...)
			break
		}
	}
	p.st.usedArea -= spriteArea(pl.Width, pl.Height)
	if pl.Page >= 0 && pl.Page < len(p.st.pages) {
		p.st.pages[pl.Page].Holes = append(p.st.pages[pl.Page].Holes, atlasRect{
			X:      pl.X,
			Y:      pl.Y,
			Width:  pl.Width,
			Height: pl.Height,
		})
	}
	return nil
}

// Rebuild repacks every live sprite and switches pages atomically.
func (p *SpriteAtlasPacker) Rebuild(opts RepackOptions) (RepackReport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if opts.PageWidth <= 0 || opts.PageHeight <= 0 || opts.Padding < 0 {
		return RepackReport{}, ErrBadConfig
	}
	cfg := p.st.cfg
	cfg.PageWidth = opts.PageWidth
	cfg.PageHeight = opts.PageHeight
	cfg.Padding = opts.Padding
	if cfg.MaxPages <= 0 || cfg.MaxSprite <= 0 || cfg.MaxArea <= 0 {
		return RepackReport{}, ErrBadConfig
	}

	work := atlasState{
		cfg:        cfg,
		sprites:    make(map[string]SpriteReq, len(p.st.order)),
		placements: make(map[string]Placement, len(p.st.order)),
		order:      make([]string, 0, len(p.st.order)),
	}
	for _, id := range p.st.order {
		req := p.st.sprites[id]
		pl, err := work.place(req, opts.AllowRotate && req.AllowRotate)
		if err != nil {
			if errors.Is(err, ErrBadSprite) {
				return RepackReport{}, ErrBadConfig
			}
			return RepackReport{}, err
		}
		work.placements[id] = pl
	}

	moved := make([]string, 0)
	for _, id := range work.order {
		old, ok := p.st.placements[id]
		next := work.placements[id]
		if !ok || old.Page != next.Page || old.X != next.X || old.Y != next.Y ||
			old.Width != next.Width || old.Height != next.Height || old.Rotated != next.Rotated {
			moved = append(moved, id)
		}
	}
	sort.Strings(moved)

	work.repacks = p.st.repacks + 1
	work.rejected = p.st.rejected
	p.st = work
	return RepackReport{
		Pages:  len(work.pages),
		Placed: len(work.placements),
		Moved:  moved,
	}, nil
}

// PageCount reports how many pages are currently allocated.
func (p *SpriteAtlasPacker) PageCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.st.pages)
}

// SetPageBudget lowers or raises the page cap.
func (p *SpriteAtlasPacker) SetPageBudget(n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if n <= 0 {
		return ErrBadConfig
	}
	if n < len(p.st.pages) {
		return ErrPageBudget
	}
	p.st.cfg.MaxPages = n
	return nil
}

// Placement reads the published placement of one sprite.
func (p *SpriteAtlasPacker) Placement(id string) (Placement, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pl, ok := p.st.placements[id]
	return pl, ok
}

// Sprite reads the request that produced one placement.
func (p *SpriteAtlasPacker) Sprite(id string) (SpriteReq, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	req, ok := p.st.sprites[id]
	return req, ok
}

// Stats returns the live accounting.
func (p *SpriteAtlasPacker) Stats() AtlasStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return AtlasStats{
		Sprites:  len(p.st.sprites),
		Pages:    len(p.st.pages),
		UsedArea: p.st.usedArea,
		PageArea: p.st.pageArea,
		Repacks:  p.st.repacks,
		Rejected: p.st.rejected,
	}
}

const snapshotMagic = "gsb.sprite-atlas.v1"

type snapshotShelf struct {
	Y      int `json:"y"`
	Height int `json:"height"`
	NextX  int `json:"next_x"`
}

type snapshotRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type snapshotPage struct {
	Width   int             `json:"width"`
	Height  int             `json:"height"`
	Shelves []snapshotShelf `json:"shelves"`
	Holes   []snapshotRect  `json:"holes"`
}

type snapshotDoc struct {
	Magic      string         `json:"magic"`
	Version    int            `json:"version"`
	Checksum   string         `json:"checksum"`
	Config     AtlasConfig    `json:"config"`
	Order      []string       `json:"order"`
	Sprites    []SpriteReq    `json:"sprites"`
	Pages      []snapshotPage `json:"pages"`
	Placements []Placement    `json:"placements"`
	UsedArea   int64          `json:"used_area"`
	PageArea   int64          `json:"page_area"`
	Repacks    int            `json:"repacks"`
	Rejected   int            `json:"rejected"`
}

// Snapshot serialises configuration, sprite order, pages and placements.
func (p *SpriteAtlasPacker) Snapshot() ([]byte, error) {
	p.mu.RLock()
	doc := snapshotDoc{
		Magic:      snapshotMagic,
		Version:    1,
		Config:     p.st.cfg,
		Order:      append([]string(nil), p.st.order...),
		Sprites:    make([]SpriteReq, 0, len(p.st.order)),
		Pages:      make([]snapshotPage, 0, len(p.st.pages)),
		Placements: make([]Placement, 0, len(p.st.order)),
		UsedArea:   p.st.usedArea,
		PageArea:   p.st.pageArea,
		Repacks:    p.st.repacks,
		Rejected:   p.st.rejected,
	}
	for _, id := range p.st.order {
		doc.Sprites = append(doc.Sprites, p.st.sprites[id])
		doc.Placements = append(doc.Placements, p.st.placements[id])
	}
	for _, page := range p.st.pages {
		sp := snapshotPage{
			Width:   page.Width,
			Height:  page.Height,
			Shelves: make([]snapshotShelf, 0, len(page.Shelves)),
			Holes:   make([]snapshotRect, 0, len(page.Holes)),
		}
		for _, shelf := range page.Shelves {
			sp.Shelves = append(sp.Shelves, snapshotShelf(shelf))
		}
		for _, hole := range page.Holes {
			sp.Holes = append(sp.Holes, snapshotRect(hole))
		}
		doc.Pages = append(doc.Pages, sp)
	}
	p.mu.RUnlock()

	sum, err := snapshotChecksum(doc)
	if err != nil {
		return nil, err
	}
	doc.Checksum = sum
	return json.Marshal(doc)
}

// Restore replaces the whole atlas with a snapshot, all or nothing.
func (p *SpriteAtlasPacker) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}
	var doc snapshotDoc
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return ErrBadSnapshot
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrBadSnapshot
	}
	if doc.Magic != snapshotMagic || doc.Version != 1 || doc.Checksum == "" {
		return ErrBadSnapshot
	}
	want, err := snapshotChecksum(doc)
	if err != nil || want != doc.Checksum {
		return ErrBadSnapshot
	}
	cs, err := restoreState(doc)
	if err != nil {
		return ErrBadSnapshot
	}
	p.mu.Lock()
	p.st = cs
	p.mu.Unlock()
	return nil
}

func validateConfig(cfg AtlasConfig) error {
	if cfg.PageWidth <= 0 || cfg.PageHeight <= 0 || cfg.MaxPages <= 0 ||
		cfg.Padding < 0 || cfg.MaxArea <= 0 || cfg.MaxSprite <= 0 {
		return ErrBadConfig
	}
	return nil
}

func validateSprite(s SpriteReq, maxSprite int) error {
	if strings.TrimSpace(s.ID) == "" || s.Width <= 0 || s.Height <= 0 ||
		s.Width > maxSprite || s.Height > maxSprite {
		return ErrBadSprite
	}
	return nil
}

func cloneState(src atlasState) atlasState {
	dst := atlasState{
		cfg:        src.cfg,
		pages:      make([]atlasPage, len(src.pages)),
		order:      append([]string(nil), src.order...),
		sprites:    make(map[string]SpriteReq, len(src.sprites)),
		placements: make(map[string]Placement, len(src.placements)),
		usedArea:   src.usedArea,
		pageArea:   src.pageArea,
		repacks:    src.repacks,
		rejected:   src.rejected,
	}
	for i, page := range src.pages {
		dst.pages[i] = atlasPage{
			Width:   page.Width,
			Height:  page.Height,
			Shelves: append([]atlasShelf(nil), page.Shelves...),
			Holes:   append([]atlasRect(nil), page.Holes...),
		}
	}
	for id, req := range src.sprites {
		dst.sprites[id] = req
	}
	for id, pl := range src.placements {
		dst.placements[id] = pl
	}
	return dst
}

func (s *atlasState) place(req SpriteReq, allowRotate bool) (Placement, error) {
	var candidate placementCandidate
	found := false
	for i := range s.pages {
		if cand, ok := chooseInPage(&s.pages[i], req, s.cfg.Padding, allowRotate); ok {
			candidate = cand
			candidate.page = i
			found = true
			break
		}
	}

	if !found {
		empty := atlasPage{Width: s.cfg.PageWidth, Height: s.cfg.PageHeight}
		cand, ok := chooseInPage(&empty, req, s.cfg.Padding, allowRotate)
		if !ok {
			return Placement{}, ErrBadSprite
		}
		if err := s.checkNewPageBudget(); err != nil {
			return Placement{}, err
		}
		candidate = cand
		candidate.page = len(s.pages)
		candidate.newPage = true
	}

	if candidate.newPage {
		s.pages = append(s.pages, atlasPage{
			Width:  s.cfg.PageWidth,
			Height: s.cfg.PageHeight,
		})
		s.pageArea += pageArea(s.cfg.PageWidth, s.cfg.PageHeight)
	}
	page := &s.pages[candidate.page]
	if candidate.hole >= 0 {
		hole := page.Holes[candidate.hole]
		page.Holes = append(page.Holes[:candidate.hole], page.Holes[candidate.hole+1:]...)
		rightWidth := hole.Width - candidate.width
		bottomHeight := hole.Height - candidate.height
		if rightWidth > 0 {
			page.Holes = append(page.Holes, atlasRect{
				X:      candidate.x + candidate.width,
				Y:      candidate.y,
				Width:  rightWidth,
				Height: hole.Height,
			})
		}
		if bottomHeight > 0 {
			page.Holes = append(page.Holes, atlasRect{
				X:      candidate.x,
				Y:      candidate.y + candidate.height,
				Width:  candidate.width,
				Height: bottomHeight,
			})
		}
	} else if candidate.shelf == len(page.Shelves) {
		page.Shelves = append(page.Shelves, candidate.newShelf)
	} else {
		page.Shelves[candidate.shelf].NextX = nextShelfX(candidate.x, candidate.width, s.cfg.Padding, page.Width)
	}

	pl := makePlacement(req.ID, candidate, page)
	s.sprites[req.ID] = req
	s.placements[req.ID] = pl
	s.order = append(s.order, req.ID)
	s.usedArea += spriteArea(candidate.width, candidate.height)
	return pl, nil
}

func (s *atlasState) checkNewPageBudget() error {
	if len(s.pages) >= s.cfg.MaxPages {
		return ErrPageBudget
	}
	area := pageArea(s.cfg.PageWidth, s.cfg.PageHeight)
	if area > math.MaxInt64-s.pageArea || s.pageArea+area > s.cfg.MaxArea {
		return ErrAreaBudget
	}
	return nil
}

func chooseInPage(page *atlasPage, req SpriteReq, padding int, allowRotate bool) (placementCandidate, bool) {
	if cand, ok := tryPageOrientation(page, req.Width, req.Height, false, padding); ok {
		return cand, true
	}
	if allowRotate {
		if cand, ok := tryPageOrientation(page, req.Height, req.Width, true, padding); ok {
			return cand, true
		}
	}
	return placementCandidate{}, false
}

func tryPageOrientation(page *atlasPage, width, height int, rotated bool, padding int) (placementCandidate, bool) {
	if page.Width <= 0 || page.Height <= 0 || width <= 0 || height <= 0 {
		return placementCandidate{}, false
	}
	if idx, x, y, ok := bestHole(page.Holes, width, height); ok {
		return placementCandidate{
			x:       x,
			y:       y,
			width:   width,
			height:  height,
			rotated: rotated,
			hole:    idx,
			shelf:   -1,
		}, true
	}
	if x, y, shelf, created, _, ok := shelfFit(page, width, height, padding); ok {
		return placementCandidate{
			x:        x,
			y:        y,
			width:    width,
			height:   height,
			rotated:  rotated,
			hole:     -1,
			shelf:    shelf,
			newShelf: created,
		}, true
	}
	return placementCandidate{}, false
}

func bestHole(holes []atlasRect, width, height int) (int, int, int, bool) {
	best := -1
	bestDiff := int64(math.MaxInt64)
	want := spriteArea(width, height)
	for i, hole := range holes {
		if hole.Width < width || hole.Height < height {
			continue
		}
		diff := spriteArea(hole.Width, hole.Height) - want
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			best = i
			bestDiff = diff
		}
	}
	if best < 0 {
		return 0, 0, 0, false
	}
	return best, holes[best].X, holes[best].Y, true
}

func shelfFit(page *atlasPage, width, height, padding int) (int, int, int, atlasShelf, int, bool) {
	if len(page.Shelves) == 0 {
		if width > page.Width || height > page.Height {
			return 0, 0, 0, atlasShelf{}, 0, false
		}
		next := nextShelfX(0, width, padding, page.Width)
		return 0, 0, 0, atlasShelf{Y: 0, Height: height, NextX: next}, next, true
	}

	last := len(page.Shelves) - 1
	shelf := page.Shelves[last]
	if height <= shelf.Height && shelf.NextX >= 0 && shelf.NextX <= page.Width &&
		width <= page.Width-shelf.NextX {
		next := nextShelfX(shelf.NextX, width, padding, page.Width)
		return shelf.NextX, shelf.Y, last, atlasShelf{}, next, true
	}

	if width > page.Width || height > page.Height {
		return 0, 0, 0, atlasShelf{}, 0, false
	}
	base := shelf.Y + shelf.Height
	target := page.Height - height
	if base < 0 || base > target || padding > target-base {
		return 0, 0, 0, atlasShelf{}, 0, false
	}
	y := base + padding
	next := nextShelfX(0, width, padding, page.Width)
	return 0, y, len(page.Shelves), atlasShelf{Y: y, Height: height, NextX: next}, next, true
}

func nextShelfX(x, width, padding, pageWidth int) int {
	if x >= pageWidth || width >= pageWidth-x {
		return pageWidth
	}
	if padding >= pageWidth-(x+width) {
		return pageWidth
	}
	return x + width + padding
}

func makePlacement(id string, cand placementCandidate, page *atlasPage) Placement {
	return Placement{
		ID:         id,
		Page:       cand.page,
		X:          cand.x,
		Y:          cand.y,
		Width:      cand.width,
		Height:     cand.height,
		Rotated:    cand.rotated,
		U0:         float32(cand.x) / float32(page.Width),
		V0:         float32(cand.y) / float32(page.Height),
		U1:         float32(cand.x+cand.width) / float32(page.Width),
		V1:         float32(cand.y+cand.height) / float32(page.Height),
		PageWidth:  page.Width,
		PageHeight: page.Height,
	}
}

func spriteArea(width, height int) int64 {
	if width <= 0 || height <= 0 {
		return 0
	}
	if int64(width) > math.MaxInt64/int64(height) {
		return math.MaxInt64
	}
	return int64(width) * int64(height)
}

func pageArea(width, height int) int64 {
	return spriteArea(width, height)
}

func snapshotChecksum(doc snapshotDoc) (string, error) {
	doc.Checksum = ""
	payload, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func restoreState(doc snapshotDoc) (atlasState, error) {
	if doc.Version != 1 || doc.Magic != snapshotMagic || validateConfig(doc.Config) != nil {
		return atlasState{}, ErrBadSnapshot
	}
	if doc.Repacks < 0 || doc.Rejected < 0 || doc.UsedArea < 0 || doc.PageArea < 0 {
		return atlasState{}, ErrBadSnapshot
	}
	if len(doc.Pages) > doc.Config.MaxPages {
		return atlasState{}, ErrBadSnapshot
	}
	pages := make([]atlasPage, len(doc.Pages))
	var totalPageArea int64
	for i, sp := range doc.Pages {
		if sp.Width <= 0 || sp.Height <= 0 || sp.Width != doc.Config.PageWidth || sp.Height != doc.Config.PageHeight {
			return atlasState{}, ErrBadSnapshot
		}
		area := pageArea(sp.Width, sp.Height)
		if area > math.MaxInt64-totalPageArea {
			return atlasState{}, ErrBadSnapshot
		}
		totalPageArea += area
		page := atlasPage{
			Width:   sp.Width,
			Height:  sp.Height,
			Shelves: make([]atlasShelf, 0, len(sp.Shelves)),
			Holes:   make([]atlasRect, 0, len(sp.Holes)),
		}
		for _, ss := range sp.Shelves {
			if ss.Height <= 0 || ss.Height > sp.Height || ss.Y < 0 || ss.Y > sp.Height-ss.Height ||
				ss.NextX < 0 || ss.NextX > sp.Width {
				return atlasState{}, ErrBadSnapshot
			}
			page.Shelves = append(page.Shelves, atlasShelf(ss))
		}
		for _, sr := range sp.Holes {
			if sr.Width <= 0 || sr.Height <= 0 || sr.Width > sp.Width || sr.Height > sp.Height ||
				sr.X < 0 || sr.Y < 0 || sr.X > sp.Width-sr.Width || sr.Y > sp.Height-sr.Height {
				return atlasState{}, ErrBadSnapshot
			}
			page.Holes = append(page.Holes, atlasRect(sr))
		}
		for a := 0; a < len(page.Holes); a++ {
			for b := a + 1; b < len(page.Holes); b++ {
				if rectsOverlap(page.Holes[a], page.Holes[b]) {
					return atlasState{}, ErrBadSnapshot
				}
			}
		}
		pages[i] = page
	}
	if totalPageArea > doc.Config.MaxArea || totalPageArea != doc.PageArea {
		return atlasState{}, ErrBadSnapshot
	}
	if len(doc.Order) != len(doc.Sprites) || len(doc.Order) != len(doc.Placements) {
		return atlasState{}, ErrBadSnapshot
	}

	state := atlasState{
		cfg:        doc.Config,
		pages:      pages,
		order:      append([]string(nil), doc.Order...),
		sprites:    make(map[string]SpriteReq, len(doc.Sprites)),
		placements: make(map[string]Placement, len(doc.Placements)),
		pageArea:   totalPageArea,
		repacks:    doc.Repacks,
		rejected:   doc.Rejected,
	}
	for _, req := range doc.Sprites {
		if validateSprite(req, doc.Config.MaxSprite) != nil {
			return atlasState{}, ErrBadSnapshot
		}
		if _, exists := state.sprites[req.ID]; exists {
			return atlasState{}, ErrBadSnapshot
		}
		state.sprites[req.ID] = req
	}
	for _, pl := range doc.Placements {
		if _, exists := state.placements[pl.ID]; exists || pl.Page < 0 || pl.Page >= len(pages) {
			return atlasState{}, ErrBadSnapshot
		}
		req, exists := state.sprites[pl.ID]
		if !exists {
			return atlasState{}, ErrBadSnapshot
		}
		page := pages[pl.Page]
		if pl.Width <= 0 || pl.Height <= 0 || pl.Width > page.Width || pl.Height > page.Height ||
			pl.X < 0 || pl.Y < 0 || pl.X > page.Width-pl.Width || pl.Y > page.Height-pl.Height {
			return atlasState{}, ErrBadSnapshot
		}
		if pl.PageWidth != page.Width || pl.PageHeight != page.Height {
			return atlasState{}, ErrBadSnapshot
		}
		if pl.Rotated {
			if pl.Width != req.Height || pl.Height != req.Width {
				return atlasState{}, ErrBadSnapshot
			}
		} else if pl.Width != req.Width || pl.Height != req.Height {
			return atlasState{}, ErrBadSnapshot
		}
		if pl.U0 != float32(pl.X)/float32(page.Width) ||
			pl.V0 != float32(pl.Y)/float32(page.Height) ||
			pl.U1 != float32(pl.X+pl.Width)/float32(page.Width) ||
			pl.V1 != float32(pl.Y+pl.Height)/float32(page.Height) {
			return atlasState{}, ErrBadSnapshot
		}
		state.placements[pl.ID] = pl
		state.usedArea += spriteArea(pl.Width, pl.Height)
	}
	if state.usedArea != doc.UsedArea {
		return atlasState{}, ErrBadSnapshot
	}
	seen := make(map[string]struct{}, len(doc.Order))
	for _, id := range doc.Order {
		if _, duplicate := seen[id]; duplicate {
			return atlasState{}, ErrBadSnapshot
		}
		if _, ok := state.sprites[id]; !ok {
			return atlasState{}, ErrBadSnapshot
		}
		if _, ok := state.placements[id]; !ok {
			return atlasState{}, ErrBadSnapshot
		}
		seen[id] = struct{}{}
	}
	if len(seen) != len(state.sprites) {
		return atlasState{}, ErrBadSnapshot
	}
	pls := make([]Placement, 0, len(state.placements))
	for _, pl := range state.placements {
		pls = append(pls, pl)
	}
	for i := 0; i < len(pls); i++ {
		for j := i + 1; j < len(pls); j++ {
			a, b := pls[i], pls[j]
			if a.Page != b.Page {
				continue
			}
			if rectsOverlap(
				atlasRect{X: a.X, Y: a.Y, Width: a.Width, Height: a.Height},
				atlasRect{X: b.X, Y: b.Y, Width: b.Width, Height: b.Height},
			) {
				return atlasState{}, ErrBadSnapshot
			}
		}
	}
	for pageIndex, page := range pages {
		for _, hole := range page.Holes {
			for _, pl := range state.placements {
				if pl.Page != pageIndex {
					continue
				}
				if rectsOverlap(hole, atlasRect{X: pl.X, Y: pl.Y, Width: pl.Width, Height: pl.Height}) {
					return atlasState{}, ErrBadSnapshot
				}
			}
		}
	}
	return state, nil
}

func rectsOverlap(a, b atlasRect) bool {
	return a.X < b.X+b.Width && b.X < a.X+a.Width && a.Y < b.Y+b.Height && b.Y < a.Y+a.Height
}
