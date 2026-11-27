// Package task plans fur shell layers for one frame: it maps camera distance
// and fur length onto a shell count, shares the read only density atlas across
// viewports and trims the shell stack when the frame budget runs out.
package task

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
)

// Errors reported by the shell pipeline.
var (
	ErrNotConfigured = errors.New("fur pipeline is not configured")
	ErrBadConfig     = errors.New("fur configuration is invalid")
	ErrBadGroom      = errors.New("groom payload is invalid")
	ErrUnknownGroom  = errors.New("groom is not registered")
	ErrBadView       = errors.New("view parameters are invalid")
	ErrBadDensity    = errors.New("density map is invalid")
	ErrBadPlan       = errors.New("shell layer plan is invalid")
	ErrStalePlan     = errors.New("shell layer plan belongs to an older groom generation")
	ErrBadFrame      = errors.New("frame number must strictly increase")
	ErrNoShell       = errors.New("shell index is out of range")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Vec2 is a texture coordinate pair in the unit square.
type Vec2 struct {
	U float32 `json:"u"`
	V float32 `json:"v"`
}

// FurConfig is the immutable pipeline configuration.
type FurConfig struct {
	// MaxShells caps the number of shells any single groom may plan.
	MaxShells int `json:"max_shells"`
	// ShellBudget caps the shell instances built during one frame, all
	// viewports and all grooms together.
	ShellBudget int `json:"shell_budget"`
	// SampleBudget caps the density samples taken during one frame.
	SampleBudget int `json:"sample_budget"`
	// LODDistances are ascending camera distances that close a LOD band.
	LODDistances []float32 `json:"lod_distances"`
	// LODShells holds the shell count of every band; it must be one longer
	// than LODDistances and must not grow with distance.
	LODShells []int `json:"lod_shells"`
	// HysteresisBand is the half width of the band in which the previous
	// LOD band keeps winning.
	HysteresisBand float32 `json:"hysteresis_band"`
	// FurLengthMax is the longest groom the pipeline accepts, in metres.
	FurLengthMax float32 `json:"fur_length_max"`
	// MinStep is the smallest accepted shell spacing, in metres.
	MinStep float32 `json:"min_step"`
}

// GroomData is the per groom payload pushed by the grooming tool.
type GroomData struct {
	ID        string  `json:"id"`
	FurLength float32 `json:"fur_length"`
	BaseAlpha float32 `json:"base_alpha"`
}

// DensityMap is a single channel coverage texture laid over the groom.
type DensityMap struct {
	ID     string    `json:"id"`
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Values []float32 `json:"values"`
}

// ViewParams describes one viewport request for one groom.
type ViewParams struct {
	ViewportID string  `json:"viewport_id"`
	GroomID    string  `json:"groom_id"`
	CameraDist float32 `json:"camera_dist"`
}

// LayerPlan is the read only product of PlanLayers. Render threads share the
// same plan and only build shells from it.
type LayerPlan struct {
	ViewportID string  `json:"viewport_id"`
	GroomID    string  `json:"groom_id"`
	Generation int64   `json:"generation"`
	Band       int     `json:"band"`
	FullShells int     `json:"full_shells"`
	Shells     int     `json:"shells"`
	Trimmed    int     `json:"trimmed"`
	Step       float32 `json:"step"`
}

// ShellInstance is one shell of the stack, ready to be drawn.
type ShellInstance struct {
	GroomID    string  `json:"groom_id"`
	ViewportID string  `json:"viewport_id"`
	Index      int     `json:"index"`
	Offset     float32 `json:"offset"`
	Alpha      float32 `json:"alpha"`
	Culled     bool    `json:"culled"`
}

// FurStats is the frame snapshot of the pipeline counters.
type FurStats struct {
	Grooms     int   `json:"grooms"`
	Plans      int   `json:"plans"`
	Frame      int64 `json:"frame"`
	Shells     int   `json:"shells"`
	Samples    int   `json:"samples"`
	Generation int64 `json:"generation"`
}

type groomRecord struct {
	data       GroomData
	generation int64
}

type planSlot struct {
	plan       LayerPlan
	cameraDist float32
	order      int64
	built      int
}

type builtShell struct {
	groomID    string
	viewportID string
	alpha      float32
}

// FurShellPipeline owns the groom table, the density atlas, the per viewport
// LOD memory and the frame counters.
type FurShellPipeline struct {
	mu sync.Mutex

	configured bool
	cfg        FurConfig
	grooms     map[string]*groomRecord
	densities  map[string]DensityMap
	views      map[string]int

	frame         int64
	plans         int
	shells        int
	samples       int
	activePlans   []*planSlot
	built         []builtShell
	nextPlanOrder int64
}

// NewFurShellPipeline builds an unconfigured pipeline.
func NewFurShellPipeline() *FurShellPipeline {
	return &FurShellPipeline{
		grooms:    make(map[string]*groomRecord),
		densities: make(map[string]DensityMap),
		views:     make(map[string]int),
	}
}

// Configure installs a validated configuration.
func (p *FurShellPipeline) Configure(cfg FurConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := validateConfig(cfg); err != nil {
		return ErrBadConfig
	}

	p.cfg = cloneConfig(cfg)
	p.configured = true
	if p.grooms == nil {
		p.grooms = make(map[string]*groomRecord)
	}
	if p.densities == nil {
		p.densities = make(map[string]DensityMap)
	}
	if p.views == nil {
		p.views = make(map[string]int)
	}
	for id, band := range p.views {
		if band < 0 || band >= len(p.cfg.LODShells) {
			delete(p.views, id)
		}
	}
	p.clearFrameLocked()
	return nil
}

// BeginFrame opens a new frame and resets the per frame counters.
func (p *FurShellPipeline) BeginFrame(frame int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return ErrNotConfigured
	}
	if frame <= p.frame {
		return ErrBadFrame
	}
	p.frame = frame
	p.clearFrameLocked()
	return nil
}

// SetGroomer installs or replaces one groom and advances its generation.
func (p *FurShellPipeline) SetGroomer(id string, groom GroomData) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return ErrNotConfigured
	}
	if err := validateGroom(id, groom, p.cfg); err != nil {
		return ErrBadGroom
	}
	if p.grooms == nil {
		p.grooms = make(map[string]*groomRecord)
	}

	generation := int64(1)
	if old, ok := p.grooms[id]; ok {
		generation = old.generation + 1
	}
	next := make(map[string]*groomRecord, len(p.grooms)+1)
	for key, value := range p.grooms {
		next[key] = value
	}
	next[id] = &groomRecord{
		data: GroomData{
			ID:        groom.ID,
			FurLength: groom.FurLength,
			BaseAlpha: groom.BaseAlpha,
		},
		generation: generation,
	}
	p.grooms = next
	return nil
}

// SetDensity installs the shared read only density map of one groom.
func (p *FurShellPipeline) SetDensity(id string, tex DensityMap) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return ErrNotConfigured
	}
	if _, ok := p.grooms[id]; !ok {
		return ErrUnknownGroom
	}
	if err := validateDensity(tex); err != nil {
		return ErrBadDensity
	}

	next := make(map[string]DensityMap, len(p.densities)+1)
	for key, value := range p.densities {
		next[key] = value
	}
	next[id] = cloneDensity(tex)
	p.densities = next
	return nil
}

// PlanLayers resolves the LOD band of one viewport and trims the stack against
// the remaining frame budget.
func (p *FurShellPipeline) PlanLayers(view ViewParams) (LayerPlan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return LayerPlan{}, ErrNotConfigured
	}
	if strings.TrimSpace(view.ViewportID) == "" || !isPositiveFinite(view.CameraDist) {
		return LayerPlan{}, ErrBadView
	}
	groom, ok := p.grooms[view.GroomID]
	if !ok {
		return LayerPlan{}, ErrUnknownGroom
	}

	band := baseBand(p.cfg, view.CameraDist)
	if previous, exists := p.views[view.ViewportID]; exists && keepsPreviousBand(p.cfg, previous, view.CameraDist) {
		band = previous
	}
	p.views[view.ViewportID] = band

	fullShells := p.cfg.LODShells[band]
	nextPlan := LayerPlan{
		ViewportID: view.ViewportID,
		GroomID:    view.GroomID,
		Generation: groom.generation,
		Band:       band,
		FullShells: fullShells,
		Shells:     fullShells,
		Step:       groom.data.FurLength / float32(fullShells),
	}

	slotIndex := -1
	for i, slot := range p.activePlans {
		if slot.plan.ViewportID == view.ViewportID && slot.plan.GroomID == view.GroomID {
			slotIndex = i
			break
		}
	}
	if slotIndex < 0 {
		p.nextPlanOrder++
		p.activePlans = append(p.activePlans, &planSlot{
			plan:       nextPlan,
			cameraDist: view.CameraDist,
			order:      p.nextPlanOrder,
		})
		slotIndex = len(p.activePlans) - 1
	} else {
		slot := p.activePlans[slotIndex]
		if !samePlanIdentity(slot.plan, nextPlan) {
			p.removeBuiltForLocked(view.ViewportID, view.GroomID)
			slot.built = 0
		}
		slot.plan = nextPlan
		slot.cameraDist = view.CameraDist
	}

	p.rebalancePlansLocked()
	p.plans = len(p.activePlans)
	return p.activePlans[slotIndex].plan, nil
}

// BuildShells expands a plan into drawable shell instances.
func (p *FurShellPipeline) BuildShells(plan LayerPlan) ([]ShellInstance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return nil, ErrNotConfigured
	}
	groom, ok := p.grooms[plan.GroomID]
	if !ok {
		return nil, ErrUnknownGroom
	}
	if plan.Generation != groom.generation {
		return nil, ErrStalePlan
	}
	if plan.Shells < 0 || plan.Shells > p.cfg.MaxShells {
		return nil, ErrBadPlan
	}

	slotIndex := -1
	for i, slot := range p.activePlans {
		if slot.plan.ViewportID == plan.ViewportID && slot.plan.GroomID == plan.GroomID {
			slotIndex = i
			break
		}
	}
	if slotIndex >= 0 {
		if p.activePlans[slotIndex].built != 0 {
			return nil, ErrBadPlan
		}
	}
	if !validPlan(plan, p.cfg) {
		return nil, ErrBadPlan
	}
	if plan.Shells > p.cfg.ShellBudget-p.shells {
		return nil, ErrBadPlan
	}

	shells := make([]ShellInstance, 0, plan.Shells)
	for i := 0; i < plan.Shells; i++ {
		alpha := groom.data.BaseAlpha * float32(plan.FullShells-i) / float32(plan.FullShells)
		shells = append(shells, ShellInstance{
			GroomID:    plan.GroomID,
			ViewportID: plan.ViewportID,
			Index:      i,
			Offset:     float32(i) * plan.Step,
			Alpha:      alpha,
			Culled:     false,
		})
	}
	for _, shell := range shells {
		p.built = append(p.built, builtShell{
			groomID:    shell.GroomID,
			viewportID: shell.ViewportID,
			alpha:      shell.Alpha,
		})
	}
	p.shells = len(p.built)
	if slotIndex >= 0 {
		p.activePlans[slotIndex].built = plan.Shells
	}
	return shells, nil
}

// ResolveAlpha samples the density atlas and returns the shaded alpha of one
// built shell.
func (p *FurShellPipeline) ResolveAlpha(shell int, uv Vec2) (float32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return 0, ErrNotConfigured
	}
	if shell < 0 || shell >= len(p.built) {
		return 0, ErrNoShell
	}
	resolved := p.built[shell]
	density, ok := p.densities[resolved.groomID]
	if !ok {
		return resolved.alpha, nil
	}
	if p.samples >= p.cfg.SampleBudget {
		return resolved.alpha, nil
	}
	sampled := sampleDensity(density, uv)
	p.samples++
	return resolved.alpha * sampled, nil
}

// Generation reports the current generation of one groom.
func (p *FurShellPipeline) Generation(id string) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return 0, ErrNotConfigured
	}
	groom, ok := p.grooms[id]
	if !ok {
		return 0, ErrUnknownGroom
	}
	return groom.generation, nil
}

// DropGroomer removes a groom together with its density map and its plans.
func (p *FurShellPipeline) DropGroomer(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return ErrNotConfigured
	}
	if _, ok := p.grooms[id]; !ok {
		return ErrUnknownGroom
	}

	nextGrooms := make(map[string]*groomRecord, len(p.grooms)-1)
	for key, value := range p.grooms {
		if key != id {
			nextGrooms[key] = value
		}
	}
	nextDensities := make(map[string]DensityMap, len(p.densities))
	for key, value := range p.densities {
		if key != id {
			nextDensities[key] = value
		}
	}
	nextPlans := make([]*planSlot, 0, len(p.activePlans))
	for _, slot := range p.activePlans {
		if slot.plan.GroomID != id {
			nextPlans = append(nextPlans, slot)
		}
	}
	p.grooms = nextGrooms
	p.densities = nextDensities
	p.activePlans = nextPlans
	p.plans = len(nextPlans)
	p.removeBuiltForLocked("", id)
	return nil
}

// Stats returns the counters of the current frame.
func (p *FurShellPipeline) Stats() FurStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return FurStats{}
	}
	var generation int64
	for _, groom := range p.grooms {
		if groom.generation > generation {
			generation = groom.generation
		}
	}
	return FurStats{
		Grooms:     len(p.grooms),
		Plans:      p.plans,
		Frame:      p.frame,
		Shells:     p.shells,
		Samples:    p.samples,
		Generation: generation,
	}
}

type snapshotGroom struct {
	ID         string  `json:"id"`
	Generation int64   `json:"generation"`
	FurLength  float32 `json:"fur_length"`
	BaseAlpha  float32 `json:"base_alpha"`
}

type snapshotDensity struct {
	GroomID string     `json:"groom_id"`
	Texture DensityMap `json:"texture"`
}

type snapshotView struct {
	ViewportID string `json:"viewport_id"`
	Band       int    `json:"band"`
}

type snapshotPlan struct {
	Plan       LayerPlan `json:"plan"`
	CameraDist float32   `json:"camera_dist"`
	Order      int64     `json:"order"`
	Built      int       `json:"built"`
}

type snapshotShell struct {
	GroomID    string  `json:"groom_id"`
	ViewportID string  `json:"viewport_id"`
	Alpha      float32 `json:"alpha"`
}

type furSnapshot struct {
	Version     int               `json:"version"`
	Config      FurConfig         `json:"config"`
	Grooms      []snapshotGroom   `json:"grooms"`
	Densities   []snapshotDensity `json:"densities"`
	Views       []snapshotView    `json:"views"`
	Frame       int64             `json:"frame"`
	Plans       int               `json:"plans"`
	Shells      int               `json:"shells"`
	Samples     int               `json:"samples"`
	ActivePlans []snapshotPlan    `json:"active_plans"`
	Built       []snapshotShell   `json:"built"`
	NextPlanOrd int64             `json:"next_plan_order"`
}

// Snapshot serialises configuration, groom generations, density maps, the per
// viewport LOD memory and the frame counters.
func (p *FurShellPipeline) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return nil, ErrNotConfigured
	}

	snapshot := furSnapshot{
		Version:     1,
		Config:      cloneConfig(p.cfg),
		Grooms:      make([]snapshotGroom, 0, len(p.grooms)),
		Densities:   make([]snapshotDensity, 0, len(p.densities)),
		Views:       make([]snapshotView, 0, len(p.views)),
		Frame:       p.frame,
		Plans:       p.plans,
		Shells:      p.shells,
		Samples:     p.samples,
		ActivePlans: make([]snapshotPlan, 0, len(p.activePlans)),
		Built:       make([]snapshotShell, 0, len(p.built)),
		NextPlanOrd: p.nextPlanOrder,
	}

	ids := make([]string, 0, len(p.grooms))
	for id := range p.grooms {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		groom := p.grooms[id]
		snapshot.Grooms = append(snapshot.Grooms, snapshotGroom{
			ID:         id,
			Generation: groom.generation,
			FurLength:  groom.data.FurLength,
			BaseAlpha:  groom.data.BaseAlpha,
		})
	}

	ids = ids[:0]
	for id := range p.densities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		snapshot.Densities = append(snapshot.Densities, snapshotDensity{
			GroomID: id,
			Texture: cloneDensity(p.densities[id]),
		})
	}

	ids = ids[:0]
	for id := range p.views {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		snapshot.Views = append(snapshot.Views, snapshotView{ViewportID: id, Band: p.views[id]})
	}

	for _, slot := range p.activePlans {
		snapshot.ActivePlans = append(snapshot.ActivePlans, snapshotPlan{
			Plan:       slot.plan,
			CameraDist: slot.cameraDist,
			Order:      slot.order,
			Built:      slot.built,
		})
	}
	for _, shell := range p.built {
		snapshot.Built = append(snapshot.Built, snapshotShell{
			GroomID:    shell.groomID,
			ViewportID: shell.viewportID,
			Alpha:      shell.alpha,
		})
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole pipeline state with a snapshot, all or nothing.
func (p *FurShellPipeline) Restore(data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.configured {
		return ErrNotConfigured
	}
	if len(data) == 0 {
		return ErrBadSnapshot
	}

	var snapshot furSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Version != 1 {
		return ErrBadSnapshot
	}
	if err := validateConfig(snapshot.Config); err != nil {
		return ErrBadSnapshot
	}

	grooms := make(map[string]*groomRecord, len(snapshot.Grooms))
	for _, item := range snapshot.Grooms {
		groom := GroomData{ID: item.ID, FurLength: item.FurLength, BaseAlpha: item.BaseAlpha}
		if item.Generation < 1 || validateGroom(item.ID, groom, snapshot.Config) != nil {
			return ErrBadSnapshot
		}
		if _, exists := grooms[item.ID]; exists {
			return ErrBadSnapshot
		}
		grooms[item.ID] = &groomRecord{data: groom, generation: item.Generation}
	}

	densities := make(map[string]DensityMap, len(snapshot.Densities))
	for _, item := range snapshot.Densities {
		if _, ok := grooms[item.GroomID]; !ok || validateDensity(item.Texture) != nil {
			return ErrBadSnapshot
		}
		if _, exists := densities[item.GroomID]; exists {
			return ErrBadSnapshot
		}
		densities[item.GroomID] = cloneDensity(item.Texture)
	}

	views := make(map[string]int, len(snapshot.Views))
	for _, item := range snapshot.Views {
		if strings.TrimSpace(item.ViewportID) == "" || item.Band < 0 || item.Band >= len(snapshot.Config.LODShells) {
			return ErrBadSnapshot
		}
		if _, exists := views[item.ViewportID]; exists {
			return ErrBadSnapshot
		}
		views[item.ViewportID] = item.Band
	}

	if snapshot.Plans < 0 || snapshot.Shells < 0 || snapshot.Shells > snapshot.Config.ShellBudget || snapshot.Samples < 0 || snapshot.Samples > snapshot.Config.SampleBudget {
		return ErrBadSnapshot
	}
	if snapshot.Shells != len(snapshot.Built) || snapshot.Plans != len(snapshot.ActivePlans) {
		return ErrBadSnapshot
	}

	activePlans := make([]*planSlot, 0, len(snapshot.ActivePlans))
	seenPlans := make(map[string]struct{}, len(snapshot.ActivePlans))
	for _, item := range snapshot.ActivePlans {
		if strings.TrimSpace(item.Plan.ViewportID) == "" || !isPositiveFinite(item.CameraDist) {
			return ErrBadSnapshot
		}
		if _, ok := grooms[item.Plan.GroomID]; !ok {
			return ErrBadSnapshot
		}
		if item.Plan.Generation < 1 || item.Plan.Generation > grooms[item.Plan.GroomID].generation {
			return ErrBadSnapshot
		}
		if item.Plan.Band < 0 || item.Plan.Band >= len(snapshot.Config.LODShells) || !validPlan(item.Plan, snapshot.Config) {
			return ErrBadSnapshot
		}
		if item.Built < 0 || item.Built > item.Plan.Shells {
			return ErrBadSnapshot
		}
		key := item.Plan.ViewportID + "\x00" + item.Plan.GroomID
		if _, exists := seenPlans[key]; exists {
			return ErrBadSnapshot
		}
		seenPlans[key] = struct{}{}
		activePlans = append(activePlans, &planSlot{
			plan:       item.Plan,
			cameraDist: item.CameraDist,
			order:      item.Order,
			built:      item.Built,
		})
	}

	built := make([]builtShell, 0, len(snapshot.Built))
	for _, item := range snapshot.Built {
		if strings.TrimSpace(item.ViewportID) == "" || !finite32(item.Alpha) || item.Alpha < 0 || item.Alpha > 1 {
			return ErrBadSnapshot
		}
		if _, ok := grooms[item.GroomID]; !ok {
			return ErrBadSnapshot
		}
		built = append(built, builtShell{
			groomID:    item.GroomID,
			viewportID: item.ViewportID,
			alpha:      item.Alpha,
		})
	}

	nextOrder := snapshot.NextPlanOrd
	for _, slot := range activePlans {
		if slot.order >= nextOrder {
			nextOrder = slot.order + 1
		}
	}

	p.cfg = cloneConfig(snapshot.Config)
	p.grooms = grooms
	p.densities = densities
	p.views = views
	p.frame = snapshot.Frame
	p.plans = snapshot.Plans
	p.shells = len(built)
	p.samples = snapshot.Samples
	p.activePlans = activePlans
	p.built = built
	p.nextPlanOrder = nextOrder
	p.configured = true
	return nil
}

func (p *FurShellPipeline) clearFrameLocked() {
	p.plans = 0
	p.shells = 0
	p.samples = 0
	p.activePlans = nil
	p.built = nil
	p.nextPlanOrder = 0
}

func (p *FurShellPipeline) removeBuiltForLocked(viewportID, groomID string) {
	kept := make([]builtShell, 0, len(p.built))
	for _, shell := range p.built {
		if (viewportID == "" || shell.viewportID == viewportID) && shell.groomID == groomID {
			continue
		}
		kept = append(kept, shell)
	}
	p.built = kept
	p.shells = len(kept)
}

func (p *FurShellPipeline) rebalancePlansLocked() {
	if len(p.activePlans) == 0 {
		p.plans = 0
		return
	}

	ordered := append([]*planSlot(nil), p.activePlans...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].cameraDist == ordered[j].cameraDist {
			return ordered[i].order < ordered[j].order
		}
		return ordered[i].cameraDist < ordered[j].cameraDist
	})

	reserved := 0
	for _, slot := range ordered {
		floor := slot.built
		if floor < 0 {
			floor = 0
		}
		if floor > slot.plan.FullShells {
			floor = slot.plan.FullShells
		}
		reserved += floor
	}
	remaining := p.cfg.ShellBudget - reserved
	if remaining < 0 {
		remaining = 0
	}
	for _, slot := range ordered {
		floor := slot.built
		if floor < 0 {
			floor = 0
		}
		if floor > slot.plan.FullShells {
			floor = slot.plan.FullShells
		}
		extra := slot.plan.FullShells - floor
		if extra > remaining {
			extra = remaining
		}
		slot.plan.Shells = floor + extra
		slot.plan.Trimmed = slot.plan.FullShells - slot.plan.Shells
		remaining -= extra
	}
	p.plans = len(p.activePlans)
}

func validateConfig(cfg FurConfig) error {
	if cfg.MaxShells < 2 || cfg.ShellBudget <= 0 || cfg.SampleBudget <= 0 {
		return ErrBadConfig
	}
	if !isPositiveFinite(cfg.MinStep) || !isPositiveFinite(cfg.FurLengthMax) {
		return ErrBadConfig
	}
	if len(cfg.LODDistances) == 0 || len(cfg.LODShells) != len(cfg.LODDistances)+1 {
		return ErrBadConfig
	}

	for i, distance := range cfg.LODDistances {
		if !isPositiveFinite(distance) {
			return ErrBadConfig
		}
		if i > 0 && distance <= cfg.LODDistances[i-1] {
			return ErrBadConfig
		}
	}
	for i, shells := range cfg.LODShells {
		if shells < 1 || shells > cfg.MaxShells {
			return ErrBadConfig
		}
		if i > 0 && shells > cfg.LODShells[i-1] {
			return ErrBadConfig
		}
	}

	margin := float64(cfg.LODDistances[0])
	for i := 1; i < len(cfg.LODDistances); i++ {
		gap := float64(cfg.LODDistances[i] - cfg.LODDistances[i-1])
		if gap < margin {
			margin = gap
		}
	}
	if !finite32(cfg.HysteresisBand) || cfg.HysteresisBand < 0 || float64(cfg.HysteresisBand) >= margin {
		return ErrBadConfig
	}
	return nil
}

func validateGroom(id string, groom GroomData, cfg FurConfig) error {
	if strings.TrimSpace(id) == "" || groom.ID != id {
		return ErrBadGroom
	}
	if !isPositiveFinite(groom.FurLength) || groom.FurLength > cfg.FurLengthMax {
		return ErrBadGroom
	}
	if !finite32(groom.BaseAlpha) || groom.BaseAlpha <= 0 || groom.BaseAlpha > 1 {
		return ErrBadGroom
	}
	return nil
}

func validateDensity(tex DensityMap) error {
	if tex.Width <= 0 || tex.Height <= 0 {
		return ErrBadDensity
	}
	limit := int(^uint(0) >> 1)
	if tex.Width > limit/tex.Height {
		return ErrBadDensity
	}
	if len(tex.Values) != tex.Width*tex.Height {
		return ErrBadDensity
	}
	for _, value := range tex.Values {
		if !finite32(value) || value < 0 || value > 1 {
			return ErrBadDensity
		}
	}
	return nil
}

func validPlan(plan LayerPlan, cfg FurConfig) bool {
	if strings.TrimSpace(plan.ViewportID) == "" || strings.TrimSpace(plan.GroomID) == "" {
		return false
	}
	if plan.Shells < 0 || plan.Shells > cfg.MaxShells {
		return false
	}
	if plan.FullShells < 1 || plan.FullShells > cfg.MaxShells {
		return false
	}
	if plan.Trimmed < 0 || plan.Trimmed > plan.FullShells || plan.Shells != plan.FullShells-plan.Trimmed {
		return false
	}
	if !isPositiveFinite(plan.Step) {
		return false
	}
	return true
}

func samePlanIdentity(a, b LayerPlan) bool {
	return a.ViewportID == b.ViewportID &&
		a.GroomID == b.GroomID &&
		a.Generation == b.Generation &&
		a.Band == b.Band &&
		a.FullShells == b.FullShells &&
		a.Step == b.Step
}

func baseBand(cfg FurConfig, distance float32) int {
	for i, limit := range cfg.LODDistances {
		if distance <= limit {
			return i
		}
	}
	return len(cfg.LODDistances)
}

func keepsPreviousBand(cfg FurConfig, previous int, distance float32) bool {
	if previous < 0 || previous >= len(cfg.LODShells) {
		return false
	}
	last := len(cfg.LODShells) - 1
	switch {
	case previous == 0:
		return distance <= cfg.LODDistances[0]+cfg.HysteresisBand
	case previous == last:
		return distance > cfg.LODDistances[len(cfg.LODDistances)-1]-cfg.HysteresisBand
	default:
		return distance > cfg.LODDistances[previous-1]-cfg.HysteresisBand &&
			distance <= cfg.LODDistances[previous]+cfg.HysteresisBand
	}
}

func sampleDensity(tex DensityMap, uv Vec2) float32 {
	u := clampUnit(uv.U)
	v := clampUnit(uv.V)
	x := float64(u)*float64(tex.Width) - 0.5
	y := float64(v)*float64(tex.Height) - 0.5
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	x1 := x0 + 1
	y1 := y0 + 1
	x0 = clampIndex(x0, tex.Width)
	x1 = clampIndex(x1, tex.Width)
	y0 = clampIndex(y0, tex.Height)
	y1 = clampIndex(y1, tex.Height)
	tx := float32(x - math.Floor(x))
	ty := float32(y - math.Floor(y))

	v00 := tex.Values[y0*tex.Width+x0]
	v10 := tex.Values[y0*tex.Width+x1]
	v01 := tex.Values[y1*tex.Width+x0]
	v11 := tex.Values[y1*tex.Width+x1]
	top := v00*(1-tx) + v10*tx
	bottom := v01*(1-tx) + v11*tx
	return top*(1-ty) + bottom*ty
}

func clampUnit(value float32) float32 {
	if math.IsNaN(float64(value)) || value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func clampIndex(index, size int) int {
	if index < 0 {
		return 0
	}
	if index >= size {
		return size - 1
	}
	return index
}

func cloneConfig(cfg FurConfig) FurConfig {
	cfg.LODDistances = append([]float32(nil), cfg.LODDistances...)
	cfg.LODShells = append([]int(nil), cfg.LODShells...)
	return cfg
}

func cloneDensity(tex DensityMap) DensityMap {
	tex.Values = append([]float32(nil), tex.Values...)
	return tex
}

func finite32(value float32) bool {
	return !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0)
}

func isPositiveFinite(value float32) bool {
	return finite32(value) && value > 0
}
