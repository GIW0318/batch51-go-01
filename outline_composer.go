// Package task composes selection outlines from a depth buffer, a normal
// buffer and a target mask, and keeps the per target colour and width unique
// under a per frame pixel budget.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

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

type outlineStyleEntry struct {
	Style  OutlineStyle
	Slot   int
	MaskID uint8
}

type outlineState struct {
	styles    map[string]outlineStyleEntry
	selective map[string]bool
	widthPx   float32
	occlusion OcclusionMode
	stats     OutlineStats
}

// OutlineComposer owns the style table, the selective set and the pixel
// budget accounting.
type OutlineComposer struct {
	cfg   OutlineConfig
	mu    sync.Mutex
	state atomic.Pointer[outlineState]
}

// NewOutlineComposer validates the configuration and creates an empty style
// table.
func NewOutlineComposer(cfg OutlineConfig) (*OutlineComposer, error) {
	if cfg.MaxStyles <= 0 ||
		!positiveFinite(cfg.MaxWidthPx) ||
		!positiveFinite(cfg.DepthThreshold) ||
		!finiteInRange(cfg.NormalThreshold, -1, 1) ||
		cfg.MaxPixels <= 0 ||
		cfg.MaxTexPixels <= 0 {
		return nil, ErrBadConfig
	}
	initialWidth := float32(1)
	if cfg.MaxWidthPx < initialWidth {
		initialWidth = cfg.MaxWidthPx
	}
	c := &OutlineComposer{cfg: cfg}
	c.state.Store(&outlineState{
		styles:    make(map[string]outlineStyleEntry),
		selective: make(map[string]bool),
		widthPx:   initialWidth,
		occlusion: OcclusionNone,
	})
	return c, nil
}

// SetStyle installs or replaces one style and swaps the table atomically.
func (c *OutlineComposer) SetStyle(id string, st OutlineStyle) error {
	if !c.validStyle(id, st) {
		return ErrBadStyle
	}
	st.ID = id

	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.state.Load()
	next := *current
	next.styles = cloneStyles(current.styles)
	if entry, ok := current.styles[id]; ok {
		entry.Style = st
		next.styles[id] = entry
	} else {
		if len(current.styles) >= c.cfg.MaxStyles {
			return ErrStyleLimit
		}
		slot := nextFreeSlot(next.styles, c.cfg.MaxStyles)
		if slot == 0 {
			return ErrStyleLimit
		}
		next.styles[id] = outlineStyleEntry{
			Style:  st,
			Slot:   slot,
			MaskID: chooseMaskID(id, slot, next.styles),
		}
	}
	c.state.Store(&next)
	return nil
}

// RemoveStyle drops one style from the table.
func (c *OutlineComposer) RemoveStyle(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.state.Load()
	if _, ok := current.styles[id]; !ok {
		return ErrUnknownStyle
	}
	next := *current
	next.styles = cloneStyles(current.styles)
	delete(next.styles, id)
	next.selective = cloneSelective(current.selective)
	delete(next.selective, id)
	c.state.Store(&next)
	return nil
}

// SetSelective turns outline composition on or off for one target.
func (c *OutlineComposer) SetSelective(id string, on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.state.Load()
	if _, ok := current.styles[id]; !ok {
		return ErrUnknownStyle
	}
	next := *current
	next.selective = cloneSelective(current.selective)
	if on {
		next.selective[id] = true
	} else {
		delete(next.selective, id)
	}
	c.state.Store(&next)
	return nil
}

// Selective reports whether one target takes part in composition.
func (c *OutlineComposer) Selective(id string) bool {
	current := c.state.Load()
	if _, ok := current.styles[id]; !ok {
		return false
	}
	return current.selective[id]
}

// Priority reports the priority of one style, or minus one when unknown.
func (c *OutlineComposer) Priority(id string) int {
	current := c.state.Load()
	if entry, ok := current.styles[id]; ok {
		return entry.Style.Priority
	}
	return -1
}

// SetWidthPx sets the pipeline wide outline width.
func (c *OutlineComposer) SetWidthPx(px float32) error {
	if !c.validWidth(px) {
		return ErrBadWidthPx
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.state.Load()
	next := *current
	next.widthPx = px
	c.state.Store(&next)
	return nil
}

// SetOcclusionMode selects how occluded outline pixels are treated.
func (c *OutlineComposer) SetOcclusionMode(mode OcclusionMode) error {
	if mode != OcclusionNone && mode != OcclusionMasked {
		return ErrBadOcclusion
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.state.Load()
	next := *current
	next.occlusion = mode
	c.state.Store(&next)
	return nil
}

type outlineTarget struct {
	entry       outlineStyleEntry
	maskID      uint8
	seeds       []int
	maskPixels  int
	touchesEdge bool
	width       int
}

// Compose writes the outline overlay of one frame.
func (c *OutlineComposer) Compose(depth DepthTex, normal NormalTex, mask MaskTex) (OutlineImage, error) {
	n, err := c.validateTextures(depth, normal, mask)
	if err != nil {
		return OutlineImage{}, err
	}

	current := c.state.Load()
	if current.occlusion != OcclusionNone && current.occlusion != OcclusionMasked {
		return OutlineImage{}, ErrBadOcclusion
	}

	styleByMask := stylesByMask(current.styles)
	targets, samples := collectTargets(current, styleByMask, depth, normal, mask, c.cfg.DepthThreshold, c.cfg.NormalThreshold)

	for _, target := range targets {
		target.width = pixelWidth(effectiveWidth(current.widthPx, target.entry.Style.WidthPx), maxUsefulWidth(mask.Width, mask.Height))
	}

	active := targets
	downgraded := 0
	skipped := 0
	var candidates []map[int]float32
	coveredPixels := 0

	for {
		candidates = make([]map[int]float32, len(active))
		coveredPixels = 0
		covered := make([]bool, n)
		for i, target := range active {
			candidates[i] = buildCandidates(target, current, styleByMask, depth, mask)
			for pixel := range candidates[i] {
				if !covered[pixel] {
					covered[pixel] = true
					coveredPixels++
				}
			}
		}
		if coveredPixels <= c.cfg.MaxPixels {
			break
		}

		decreased := false
		for _, target := range active {
			if target.width > 1 {
				decreased = true
				break
			}
		}
		if decreased {
			for _, target := range active {
				if target.width > 1 {
					target.width--
					downgraded++
				}
			}
			continue
		}

		if len(active) <= 1 {
			break
		}
		dropIndex := -1
		for i := range active {
			if len(candidates[i]) == 0 {
				dropIndex = i
				break
			}
		}
		if dropIndex >= 0 {
			active = append(active[:dropIndex], active[dropIndex+1:]...)
		} else {
			sort.SliceStable(active, func(i, j int) bool {
				if active[i].entry.Style.Priority != active[j].entry.Style.Priority {
					return active[i].entry.Style.Priority < active[j].entry.Style.Priority
				}
				if active[i].maskPixels != active[j].maskPixels {
					return active[i].maskPixels > active[j].maskPixels
				}
				return active[i].entry.Slot < active[j].entry.Slot
			})
			active = active[1:]
		}
		skipped++
	}

	winner := make([]int, n)
	for i := range winner {
		winner[i] = -1
	}
	winnerDepth := make([]float32, n)
	for targetIndex, pixels := range candidates {
		target := active[targetIndex]
		for pixel, lineDepth := range pixels {
			previous := winner[pixel]
			if previous < 0 || betterCandidate(target, lineDepth, active[previous], winnerDepth[pixel]) {
				winner[pixel] = targetIndex
				winnerDepth[pixel] = lineDepth
			}
		}
	}

	image := OutlineImage{Width: mask.Width, Height: mask.Height, Pixels: make([]RGBA, n)}
	written := 0
	rendered := make([]bool, len(active))
	for pixel := 0; pixel < n; pixel++ {
		if winner[pixel] < 0 || written >= c.cfg.MaxPixels {
			continue
		}
		targetIndex := winner[pixel]
		image.Pixels[pixel] = active[targetIndex].entry.Style.Color
		rendered[targetIndex] = true
		written++
	}

	targetCount := 0
	appliedWidth := 0
	for i, target := range active {
		if rendered[i] {
			targetCount++
			if target.width > appliedWidth {
				appliedWidth = target.width
			}
		}
	}
	if appliedWidth == 0 {
		appliedWidth = pixelWidth(current.widthPx, maxUsefulWidth(mask.Width, mask.Height))
	}

	stats := OutlineStats{
		Targets:    targetCount,
		Pixels:     written,
		Samples:    samples,
		WidthPx:    float32(appliedWidth),
		Downgraded: downgraded,
		Skipped:    skipped,
	}
	c.publishStats(stats)
	return image, nil
}

// Stats returns the accounting of the last Compose call.
func (c *OutlineComposer) Stats() OutlineStats {
	return c.state.Load().stats
}

type snapshotStyle struct {
	Slot     int     `json:"slot"`
	MaskID   uint8   `json:"mask_id"`
	ID       string  `json:"id"`
	Color    RGBA    `json:"color"`
	WidthPx  float32 `json:"width_px"`
	Priority int     `json:"priority"`
}

type outlineSnapshot struct {
	Version       int             `json:"version"`
	Styles        []snapshotStyle `json:"styles"`
	Selective     []string        `json:"selective"`
	WidthPx       float32         `json:"width_px"`
	OcclusionMode OcclusionMode   `json:"occlusion_mode"`
	Stats         OutlineStats    `json:"stats"`
}

// Snapshot serialises the style table, the selective set, the width, the
// occlusion mode and the budget counters.
func (c *OutlineComposer) Snapshot() ([]byte, error) {
	current := c.state.Load()
	styles := make([]outlineStyleEntry, 0, len(current.styles))
	for _, entry := range current.styles {
		styles = append(styles, entry)
	}
	sort.Slice(styles, func(i, j int) bool { return styles[i].Slot < styles[j].Slot })

	selective := make([]string, 0, len(current.selective))
	for id, on := range current.selective {
		if on {
			selective = append(selective, id)
		}
	}
	sort.Strings(selective)

	snapshot := outlineSnapshot{
		Version:       1,
		Styles:        make([]snapshotStyle, 0, len(styles)),
		Selective:     selective,
		WidthPx:       current.widthPx,
		OcclusionMode: current.occlusion,
		Stats:         current.stats,
	}
	for _, entry := range styles {
		snapshot.Styles = append(snapshot.Styles, snapshotStyle{
			Slot:     entry.Slot,
			MaskID:   entry.MaskID,
			ID:       entry.Style.ID,
			Color:    entry.Style.Color,
			WidthPx:  entry.Style.WidthPx,
			Priority: entry.Style.Priority,
		})
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return data, nil
}

// Restore replaces the whole composer state with a snapshot, all or nothing.
func (c *OutlineComposer) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}
	var snapshot outlineSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	if snapshot.Version != 1 ||
		len(snapshot.Styles) > c.cfg.MaxStyles ||
		!c.validWidth(snapshot.WidthPx) ||
		(snapshot.OcclusionMode != OcclusionNone && snapshot.OcclusionMode != OcclusionMasked) ||
		!validStats(snapshot.Stats) {
		return ErrBadSnapshot
	}

	styles := make(map[string]outlineStyleEntry, len(snapshot.Styles))
	slots := make(map[int]bool, len(snapshot.Styles))
	maskIDs := make(map[uint8]bool, len(snapshot.Styles))
	for _, saved := range snapshot.Styles {
		style := OutlineStyle{
			ID:       saved.ID,
			Color:    saved.Color,
			WidthPx:  saved.WidthPx,
			Priority: saved.Priority,
		}
		if saved.Slot <= 0 || saved.Slot > c.cfg.MaxStyles || slots[saved.Slot] || !c.validStyle(saved.ID, style) {
			return ErrBadSnapshot
		}
		if saved.MaskID == 0 {
			if len(snapshot.Styles) <= 255 {
				return ErrBadSnapshot
			}
		} else if maskIDs[saved.MaskID] {
			return ErrBadSnapshot
		}
		if _, exists := styles[saved.ID]; exists {
			return ErrBadSnapshot
		}
		slots[saved.Slot] = true
		if saved.MaskID != 0 {
			maskIDs[saved.MaskID] = true
		}
		styles[saved.ID] = outlineStyleEntry{Style: style, Slot: saved.Slot, MaskID: saved.MaskID}
	}

	selective := make(map[string]bool, len(snapshot.Selective))
	for _, id := range snapshot.Selective {
		if _, exists := styles[id]; !exists || selective[id] {
			return ErrBadSnapshot
		}
		selective[id] = true
	}

	next := &outlineState{
		styles:    styles,
		selective: selective,
		widthPx:   snapshot.WidthPx,
		occlusion: snapshot.OcclusionMode,
		stats:     snapshot.Stats,
	}
	c.mu.Lock()
	c.state.Store(next)
	c.mu.Unlock()
	return nil
}

func (c *OutlineComposer) validStyle(id string, style OutlineStyle) bool {
	if strings.TrimSpace(id) == "" || (style.ID != "" && style.ID != id) || !c.validStyleWidth(style.WidthPx) {
		return false
	}
	if style.Priority < 0 || style.Priority >= 99 {
		return false
	}
	return finiteInRange(style.Color.R, 0, 1) &&
		finiteInRange(style.Color.G, 0, 1) &&
		finiteInRange(style.Color.B, 0, 1) &&
		finiteInRange(style.Color.A, 0, 1)
}

func (c *OutlineComposer) validStyleWidth(px float32) bool {
	return positiveFinite(px) && px <= c.cfg.MaxWidthPx
}

func (c *OutlineComposer) validWidth(px float32) bool {
	return positiveFinite(px) && px <= c.cfg.MaxWidthPx
}

func (c *OutlineComposer) validateTextures(depth DepthTex, normal NormalTex, mask MaskTex) (int, error) {
	if depth.Width <= 0 || depth.Height <= 0 ||
		normal.Width != depth.Width || normal.Height != depth.Height ||
		mask.Width != depth.Width || mask.Height != depth.Height {
		return 0, ErrBadTexture
	}
	if depth.Width > c.cfg.MaxTexPixels/depth.Height {
		return 0, ErrBadTexture
	}
	n := depth.Width * depth.Height
	if len(depth.Data) != n || len(normal.Data) != n || len(mask.Data) != n {
		return 0, ErrBadTexture
	}
	return n, nil
}

func (c *OutlineComposer) publishStats(stats OutlineStats) {
	c.mu.Lock()
	current := c.state.Load()
	next := *current
	next.stats = stats
	c.state.Store(&next)
	c.mu.Unlock()
}

func collectTargets(current *outlineState, styleByMask map[uint8]outlineStyleEntry, depth DepthTex, normal NormalTex, mask MaskTex, depthThreshold, normalThreshold float32) ([]*outlineTarget, int) {
	byMask := make(map[uint8]*outlineTarget)
	samples := 0
	width, height := mask.Width, mask.Height

	for i, maskID := range mask.Data {
		if maskID == 0 {
			continue
		}
		entry, ok := styleByMask[maskID]
		if !ok || !current.selective[entry.Style.ID] {
			continue
		}
		target := byMask[maskID]
		if target == nil {
			target = &outlineTarget{entry: entry, maskID: maskID}
			byMask[maskID] = target
		}
		target.maskPixels++
		x, y := i%width, i/width
		if x == 0 || y == 0 || x == width-1 || y == height-1 {
			target.touchesEdge = true
		}

		edge := false
		samples += 4
		for _, neighbor := range clampedNeighbors(x, y, width, height) {
			if mask.Data[neighbor] != maskID {
				edge = true
				continue
			}
			if math.Abs(float64(depth.Data[i]-depth.Data[neighbor])) > float64(depthThreshold) {
				edge = true
				continue
			}
			if normalDot(normal.Data[i], normal.Data[neighbor]) < normalThreshold {
				edge = true
			}
		}
		if edge {
			target.seeds = append(target.seeds, i)
		}
	}

	targets := make([]*outlineTarget, 0, len(byMask))
	for _, target := range byMask {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].entry.Slot < targets[j].entry.Slot })
	return targets, samples
}

func buildCandidates(target *outlineTarget, current *outlineState, styleByMask map[uint8]outlineStyleEntry, depth DepthTex, mask MaskTex) map[int]float32 {
	candidates := make(map[int]float32)
	width, height := mask.Width, mask.Height
	if len(target.seeds) == 0 && target.touchesEdge {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				pixel := y*width + x
				if mask.Data[pixel] != target.maskID {
					continue
				}
				if minInt4(x, y, width-1-x, height-1-y) < target.width {
					addCandidate(target, candidates, pixel, depth.Data[pixel], current, styleByMask, depth, mask)
				}
			}
		}
		return candidates
	}

	for _, seed := range target.seeds {
		addCandidate(target, candidates, seed, depth.Data[seed], current, styleByMask, depth, mask)
	}
	radius := target.width - 1
	if radius <= 0 {
		return candidates
	}
	radiusSquared := radius * radius
	for _, seed := range target.seeds {
		sx, sy := seed%width, seed/width
		anchorDepth := depth.Data[seed]
		for dy := -radius; dy <= radius; dy++ {
			y := sy + dy
			if y < 0 || y >= height {
				continue
			}
			for dx := -radius; dx <= radius; dx++ {
				if dx*dx+dy*dy > radiusSquared {
					continue
				}
				x := sx + dx
				if x < 0 || x >= width {
					continue
				}
				pixel := y*width + x
				maskID := mask.Data[pixel]
				if maskID == target.maskID {
					continue
				}
				if maskID != 0 {
					other, ok := styleByMask[maskID]
					if !ok || other.Style.Priority > target.entry.Style.Priority {
						continue
					}
				}
				addCandidate(target, candidates, pixel, anchorDepth, current, styleByMask, depth, mask)
			}
		}
	}
	return candidates
}

func addCandidate(target *outlineTarget, candidates map[int]float32, pixel int, lineDepth float32, current *outlineState, styleByMask map[uint8]outlineStyleEntry, depth DepthTex, mask MaskTex) {
	if current.occlusion == OcclusionMasked && isOccluded(target, pixel, lineDepth, styleByMask, depth, mask) {
		return
	}
	if previous, exists := candidates[pixel]; !exists || lineDepth < previous {
		candidates[pixel] = lineDepth
	}
}

func isOccluded(target *outlineTarget, pixel int, lineDepth float32, styleByMask map[uint8]outlineStyleEntry, depth DepthTex, mask MaskTex) bool {
	if maskID := mask.Data[pixel]; maskID != 0 && maskID != target.maskID {
		if entry, ok := styleByMask[maskID]; ok && entry.Style.Priority > target.entry.Style.Priority && depth.Data[pixel] < lineDepth {
			return true
		}
	}
	width, height := mask.Width, mask.Height
	x, y := pixel%width, pixel/width
	for _, neighbor := range clampedNeighbors(x, y, width, height) {
		maskID := mask.Data[neighbor]
		if maskID == 0 || maskID == target.maskID {
			continue
		}
		entry, ok := styleByMask[maskID]
		if ok && entry.Style.Priority > target.entry.Style.Priority && depth.Data[neighbor] < lineDepth {
			return true
		}
	}
	return false
}

func betterCandidate(candidate *outlineTarget, candidateDepth float32, previous *outlineTarget, previousDepth float32) bool {
	if candidate.entry.Style.Priority != previous.entry.Style.Priority {
		return candidate.entry.Style.Priority > previous.entry.Style.Priority
	}
	if candidateDepth != previousDepth {
		return candidateDepth < previousDepth
	}
	return candidate.entry.Slot < previous.entry.Slot
}

func stylesByMask(styles map[string]outlineStyleEntry) map[uint8]outlineStyleEntry {
	result := make(map[uint8]outlineStyleEntry, len(styles))
	for _, entry := range styles {
		if entry.MaskID != 0 {
			result[entry.MaskID] = entry
		}
	}
	return result
}

func chooseMaskID(id string, slot int, styles map[string]outlineStyleEntry) uint8 {
	used := make(map[uint8]bool, len(styles))
	for _, entry := range styles {
		if entry.MaskID != 0 {
			used[entry.MaskID] = true
		}
	}
	if maskID, ok := explicitMaskID(id); ok && !used[maskID] {
		return maskID
	}
	if slot >= 1 && slot <= 255 && !used[uint8(slot)] {
		return uint8(slot)
	}
	for candidate := 1; candidate <= 255; candidate++ {
		if !used[uint8(candidate)] {
			return uint8(candidate)
		}
	}
	return 0
}

func explicitMaskID(id string) (uint8, bool) {
	value, err := strconv.Atoi(id)
	if err != nil || value < 1 || value > 255 {
		return 0, false
	}
	return uint8(value), true
}

func clampedNeighbors(x, y, width, height int) [4]int {
	leftY, rightY := y, y
	upX, downX := x, x
	leftX := x - 1
	if leftX < 0 {
		leftX = 0
	}
	rightX := x + 1
	if rightX >= width {
		rightX = width - 1
	}
	upY := y - 1
	if upY < 0 {
		upY = 0
	}
	downY := y + 1
	if downY >= height {
		downY = height - 1
	}
	return [4]int{
		leftY*width + leftX,
		rightY*width + rightX,
		upY*width + upX,
		downY*width + downX,
	}
}

func normalDot(a, b Vec3) float32 {
	return a.X*b.X + a.Y*b.Y + a.Z*b.Z
}

func cloneStyles(source map[string]outlineStyleEntry) map[string]outlineStyleEntry {
	result := make(map[string]outlineStyleEntry, len(source))
	for id, entry := range source {
		result[id] = entry
	}
	return result
}

func cloneSelective(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for id, on := range source {
		result[id] = on
	}
	return result
}

func nextFreeSlot(styles map[string]outlineStyleEntry, max int) int {
	used := make(map[int]bool, len(styles))
	for _, entry := range styles {
		used[entry.Slot] = true
	}
	for slot := 1; slot <= max; slot++ {
		if !used[slot] {
			return slot
		}
	}
	return 0
}

func positiveFinite(value float32) bool {
	return value > 0 && !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0)
}

func finiteInRange(value, low, high float32) bool {
	return !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0) && value >= low && value <= high
}

func validStats(stats OutlineStats) bool {
	return stats.Targets >= 0 &&
		stats.Pixels >= 0 &&
		stats.Samples >= 0 &&
		stats.Downgraded >= 0 &&
		stats.Skipped >= 0 &&
		!math.IsNaN(float64(stats.WidthPx)) &&
		!math.IsInf(float64(stats.WidthPx), 0) &&
		stats.WidthPx >= 0
}

func effectiveWidth(global, style float32) float32 {
	if style == 1 {
		return global
	}
	return style
}

func maxUsefulWidth(width, height int) int {
	if width > height {
		return width
	}
	return height
}

func pixelWidth(px float32, limit int) int {
	if limit < 1 {
		limit = 1
	}
	value := math.Ceil(float64(px))
	if value < 1 {
		return 1
	}
	if value > float64(limit) {
		return limit
	}
	return int(value)
}

func minInt4(a, b, c, d int) int {
	result := a
	if b < result {
		result = b
	}
	if c < result {
		result = c
	}
	if d < result {
		result = d
	}
	return result
}
