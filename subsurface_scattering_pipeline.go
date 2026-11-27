// Package task runs the subsurface scattering pass: diffusion profiles are
// grouped per material, thickness drives the kernel and depth gates the taps.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
)

// QualityLevels is how many quality steps the pass supports.
const QualityLevels = 4

// MaxDepthDelta is the depth difference above which a tap is not sampled.
const MaxDepthDelta = 0.01

// KernelRadii is the box kernel radius used by each quality level.
var KernelRadii = [QualityLevels]int{4, 3, 2, 1}

const (
	defaultMaxProfiles     = 64
	defaultMaxGroups       = 8
	defaultMaxKernelBudget = 1 << 20
	defaultMaxTempBytes    = 64 << 20
	tempBytesPerSample     = 16
	minimumThicknessScale  = 0.20
	snapshotVersion        = 1
)

// RGB is one linear colour sample.
type RGB struct {
	R float32 `json:"r"`
	G float32 `json:"g"`
	B float32 `json:"b"`
}

// DiffusionProfile describes how far light travels inside one material.
type DiffusionProfile struct {
	MaterialID uint16  `json:"material_id"`
	Name       string  `json:"name"`
	Radius     float32 `json:"radius"`
	Falloff    float32 `json:"falloff"`
	Area       float32 `json:"area"`
}

// MaterialSlot is the material coverage of one pixel; the secondary material
// takes the rest of the weight.
type MaterialSlot struct {
	Primary       uint16  `json:"primary"`
	Secondary     uint16  `json:"secondary"`
	PrimaryWeight float32 `json:"primary_weight"`
}

// MaterialIDMap is the per pixel material coverage of the frame.
type MaterialIDMap struct {
	W     int            `json:"w"`
	H     int            `json:"h"`
	Slots []MaterialSlot `json:"slots"`
}

// ThicknessMap is the per pixel scattering thickness of the frame.
type ThicknessMap struct {
	W         int       `json:"w"`
	H         int       `json:"h"`
	Thickness []float32 `json:"thickness"`
}

// DepthTex is the depth buffer handed to the blur.
type DepthTex struct {
	W     int       `json:"w"`
	H     int       `json:"h"`
	Depth []float32 `json:"depth"`
}

// Image is one colour buffer of the pass.
type Image struct {
	W      int   `json:"w"`
	H      int   `json:"h"`
	Pixels []RGB `json:"pixels"`
}

// SssLimits bounds one pipeline. Zero values mean "use the default".
type SssLimits struct {
	MaxProfiles     int `json:"max_profiles"`
	MaxGroups       int `json:"max_groups"`
	MaxKernelBudget int `json:"max_kernel_budget"`
	MaxTempBytes    int `json:"max_temp_bytes"`
}

// SSSStats is the per pipeline counter block.
type SSSStats struct {
	Frames       int `json:"frames"`
	Profiles     int `json:"profiles"`
	Groups       int `json:"groups"`
	Quality      int `json:"quality"`
	KernelRadius int `json:"kernel_radius"`
	Samples      int `json:"samples"`
	Degrades     int `json:"degrades"`
	TempBytes    int `json:"temp_bytes"`
}

// Errors returned by the pipeline.
var (
	ErrBadProfile      = errors.New("diffusion profile is not usable")
	ErrProfileExists   = errors.New("diffusion profile id or material is already registered")
	ErrProfileLimit    = errors.New("diffusion profile limit reached")
	ErrNoMaterial      = errors.New("material id zero means no material")
	ErrUnknownMaterial = errors.New("material id has no diffusion profile")
	ErrBadMaterialMap  = errors.New("material id map is not usable")
	ErrBadThickness    = errors.New("thickness map is not usable")
	ErrBadQuality      = errors.New("quality level is outside the supported range")
	ErrBadBudget       = errors.New("kernel budget is not usable")
	ErrNoProfile       = errors.New("no diffusion profile is registered")
	ErrNoMaterialMap   = errors.New("no material id map was set")
	ErrNoThicknessMap  = errors.New("no thickness map was set")
	ErrBadImage        = errors.New("image, material or depth buffer sizes disagree")
	ErrFrameBudget     = errors.New("kernel budget cannot be met even at the lowest quality")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

type profileEntry struct {
	id      string
	profile DiffusionProfile
}

type materialGroup struct {
	profile DiffusionProfile
	area    float64
}

// SubsurfaceScatteringPipeline owns the profile registry, the per frame maps
// and the quality and budget state.
type SubsurfaceScatteringPipeline struct {
	mu sync.Mutex

	limits SssLimits

	profiles   map[uint16]profileEntry
	profileIDs map[string]uint16

	materialMap  *MaterialIDMap
	thicknessMap *ThicknessMap

	quality      int
	kernelBudget int
	counters     SSSStats
}

// NewSubsurfaceScatteringPipeline builds a pipeline with the given caps.
func NewSubsurfaceScatteringPipeline(limits SssLimits) *SubsurfaceScatteringPipeline {
	p := &SubsurfaceScatteringPipeline{
		limits:       limits,
		profiles:     make(map[uint16]profileEntry),
		profileIDs:   make(map[string]uint16),
		kernelBudget: effectiveLimit(limits.MaxKernelBudget, defaultMaxKernelBudget),
	}
	return p
}

func effectiveLimit(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func (p *SubsurfaceScatteringPipeline) maxProfiles() int {
	return effectiveLimit(p.limits.MaxProfiles, defaultMaxProfiles)
}

func (p *SubsurfaceScatteringPipeline) maxGroups() int {
	return effectiveLimit(p.limits.MaxGroups, defaultMaxGroups)
}

func (p *SubsurfaceScatteringPipeline) maxKernelBudget() int {
	return effectiveLimit(p.limits.MaxKernelBudget, defaultMaxKernelBudget)
}

func (p *SubsurfaceScatteringPipeline) maxTempBytes() int {
	return effectiveLimit(p.limits.MaxTempBytes, defaultMaxTempBytes)
}

func validProfile(id string, profile DiffusionProfile) bool {
	if id == "" || strings.TrimSpace(profile.Name) == "" || profile.MaterialID == 0 {
		return false
	}
	if math.IsNaN(float64(profile.Radius)) || math.IsInf(float64(profile.Radius), 0) || profile.Radius <= 0 {
		return false
	}
	if math.IsNaN(float64(profile.Falloff)) || math.IsInf(float64(profile.Falloff), 0) {
		return false
	}
	if math.IsNaN(float64(profile.Area)) || math.IsInf(float64(profile.Area), 0) {
		return false
	}
	return true
}

// RegisterProfile adds one diffusion profile under the given identifier.
func (p *SubsurfaceScatteringPipeline) RegisterProfile(id string, profile DiffusionProfile) error {
	if !validProfile(id, profile) {
		return ErrBadProfile
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.profileIDs[id]; ok {
		return ErrProfileExists
	}
	if _, ok := p.profiles[profile.MaterialID]; ok {
		return ErrProfileExists
	}
	if len(p.profiles) >= p.maxProfiles() {
		return ErrProfileLimit
	}
	p.profileIDs[id] = profile.MaterialID
	p.profiles[profile.MaterialID] = profileEntry{id: id, profile: profile}
	return nil
}

// SampleProfile looks a diffusion profile up by material id.
func (p *SubsurfaceScatteringPipeline) SampleProfile(materialID uint16) (DiffusionProfile, error) {
	if materialID == 0 {
		return DiffusionProfile{}, ErrNoMaterial
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.profiles[materialID]
	if !ok {
		return DiffusionProfile{}, ErrUnknownMaterial
	}
	return entry.profile, nil
}

// ProfileCount reports how many profiles are registered.
func (p *SubsurfaceScatteringPipeline) ProfileCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.profiles)
}

func validDimensions(w, h, count int) bool {
	if w <= 0 || h <= 0 || count < 0 {
		return false
	}
	area := int64(w) * int64(h)
	return area >= 0 && area == int64(count)
}

func validPrimaryWeight(weight float32) bool {
	if math.IsNaN(float64(weight)) || math.IsInf(float64(weight), 0) {
		return false
	}
	return weight >= 0 && weight <= 1
}

func validateMaterialMap(tex MaterialIDMap) error {
	if !validDimensions(tex.W, tex.H, len(tex.Slots)) {
		return ErrBadMaterialMap
	}
	for _, slot := range tex.Slots {
		if !validPrimaryWeight(slot.PrimaryWeight) {
			return ErrBadMaterialMap
		}
	}
	return nil
}

func validateThicknessMap(tex ThicknessMap) error {
	if !validDimensions(tex.W, tex.H, len(tex.Thickness)) {
		return ErrBadThickness
	}
	for _, thickness := range tex.Thickness {
		if math.IsNaN(float64(thickness)) || math.IsInf(float64(thickness), 0) || thickness < 0 {
			return ErrBadThickness
		}
	}
	return nil
}

func cloneMaterialMap(tex MaterialIDMap) MaterialIDMap {
	clone := MaterialIDMap{W: tex.W, H: tex.H, Slots: make([]MaterialSlot, len(tex.Slots))}
	copy(clone.Slots, tex.Slots)
	return clone
}

func cloneThicknessMap(tex ThicknessMap) ThicknessMap {
	clone := ThicknessMap{W: tex.W, H: tex.H, Thickness: make([]float32, len(tex.Thickness))}
	copy(clone.Thickness, tex.Thickness)
	return clone
}

// SetMaterialMap installs the per pixel material coverage of the frame.
func (p *SubsurfaceScatteringPipeline) SetMaterialMap(tex MaterialIDMap) error {
	clone := cloneMaterialMap(tex)
	if err := validateMaterialMap(clone); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.materialMap = &clone
	return nil
}

// SetThicknessMap installs the per pixel thickness of the frame.
func (p *SubsurfaceScatteringPipeline) SetThicknessMap(tex ThicknessMap) error {
	clone := cloneThicknessMap(tex)
	if err := validateThicknessMap(clone); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.thicknessMap = &clone
	return nil
}

// SetQuality selects the quality level of the next blur.
func (p *SubsurfaceScatteringPipeline) SetQuality(level int) error {
	if level < 0 || level >= QualityLevels {
		return ErrBadQuality
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.quality = level
	return nil
}

// SetKernelBudget replaces the per frame sample budget.
func (p *SubsurfaceScatteringPipeline) SetKernelBudget(n int) error {
	if n <= 0 || n > p.maxKernelBudget() {
		return ErrBadBudget
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kernelBudget = n
	return nil
}

// Stats reports the counters of the pipeline.
func (p *SubsurfaceScatteringPipeline) Stats() SSSStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.statsLocked()
}

func (p *SubsurfaceScatteringPipeline) statsLocked() SSSStats {
	stats := p.counters
	stats.Profiles = len(p.profiles)
	stats.Quality = p.quality
	stats.KernelRadius = KernelRadii[p.quality]
	return stats
}

func checkFrameSize(src Image, depth DepthTex, mat MaterialIDMap, thickness ThicknessMap) bool {
	if !validDimensions(src.W, src.H, len(src.Pixels)) {
		return false
	}
	if !validDimensions(depth.W, depth.H, len(depth.Depth)) {
		return false
	}
	return src.W == mat.W && src.H == mat.H && src.W == thickness.W && src.H == thickness.H &&
		depth.W == src.W && depth.H == src.H
}

func frameCounts(pixelCount int64, radius int) (samples int64, tempBytes int64) {
	edge := int64(2*radius + 1)
	samples = pixelCount * edge * edge
	tempBytes = samples * tempBytesPerSample
	return samples, tempBytes
}

func (p *SubsurfaceScatteringPipeline) fitsBudget(pixelCount int64, quality int) (bool, int64, int64) {
	samples, tempBytes := frameCounts(pixelCount, KernelRadii[quality])
	return samples <= int64(p.kernelBudget) && tempBytes <= int64(p.maxTempBytes()), samples, tempBytes
}

func (p *SubsurfaceScatteringPipeline) chooseQuality(pixelCount int64) (int64, int64, bool) {
	quality := p.quality
	degraded := 0
	for {
		fits, samples, tempBytes := p.fitsBudget(pixelCount, quality)
		if fits {
			p.quality = quality
			p.counters.Degrades += degraded
			return samples, tempBytes, true
		}
		if quality >= QualityLevels-1 {
			p.quality = quality
			p.counters.Degrades += degraded
			return samples, tempBytes, false
		}
		quality++
		degraded++
	}
}

func collectMaterialAreas(mat MaterialIDMap) map[uint16]float64 {
	areas := make(map[uint16]float64)
	for _, slot := range mat.Slots {
		primaryWeight := float64(slot.PrimaryWeight)
		secondaryWeight := 1 - primaryWeight
		if slot.Primary != 0 && primaryWeight > 0 {
			areas[slot.Primary] += primaryWeight
		}
		if slot.Secondary != 0 && secondaryWeight > 0 {
			areas[slot.Secondary] += secondaryWeight
		}
	}
	return areas
}

func blendProfiles(a, b DiffusionProfile, wa, wb float64) DiffusionProfile {
	total := wa + wb
	if total <= 0 {
		return a
	}
	return DiffusionProfile{
		MaterialID: a.MaterialID,
		Name:       a.Name,
		Radius:     float32((float64(a.Radius)*wa + float64(b.Radius)*wb) / total),
		Falloff:    float32((float64(a.Falloff)*wa + float64(b.Falloff)*wb) / total),
		Area:       float32((float64(a.Area)*wa + float64(b.Area)*wb) / total),
	}
}

func buildGroups(areas map[uint16]float64, maxGroups int, profiles map[uint16]profileEntry) ([]materialGroup, map[uint16]int) {
	if len(areas) == 0 {
		return nil, map[uint16]int{}
	}

	ids := make([]uint16, 0, len(areas))
	for id := range areas {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ai, aj := areas[ids[i]], areas[ids[j]]
		if ai == aj {
			return ids[i] < ids[j]
		}
		return ai > aj
	})

	groups := make([]materialGroup, 0, maxGroups)
	groupByMaterial := make(map[uint16]int, len(ids))
	if len(ids) <= maxGroups {
		for _, id := range ids {
			entry := profiles[id]
			groupByMaterial[id] = len(groups)
			groups = append(groups, materialGroup{profile: entry.profile, area: areas[id]})
		}
		return groups, groupByMaterial
	}

	singletons := maxGroups - 1
	if singletons < 0 {
		singletons = 0
	}
	for i := 0; i < singletons; i++ {
		id := ids[i]
		groupByMaterial[id] = len(groups)
		groups = append(groups, materialGroup{profile: profiles[id].profile, area: areas[id]})
	}

	mergedID := len(groups)
	mergedArea := 0.0
	var radius, falloff, profileArea float64
	for _, id := range ids[singletons:] {
		area := areas[id]
		pf := profiles[id].profile
		mergedArea += area
		radius += float64(pf.Radius) * area
		falloff += float64(pf.Falloff) * area
		profileArea += float64(pf.Area) * area
	}
	merged := materialGroup{area: mergedArea}
	if mergedArea > 0 {
		merged.profile = DiffusionProfile{
			Name:    "merged",
			Radius:  float32(radius / mergedArea),
			Falloff: float32(falloff / mergedArea),
			Area:    float32(profileArea / mergedArea),
		}
	} else {
		merged.profile = profiles[ids[singletons]].profile
	}
	for _, id := range ids[singletons:] {
		groupByMaterial[id] = mergedID
	}
	groups = append(groups, merged)
	return groups, groupByMaterial
}

func groupProfileFor(material uint16, groups []materialGroup, groupByMaterial map[uint16]int) (DiffusionProfile, bool) {
	group, ok := groupByMaterial[material]
	if !ok {
		return DiffusionProfile{}, false
	}
	return groups[group].profile, true
}

func pixelProfile(slot MaterialSlot, groups []materialGroup, groupByMaterial map[uint16]int) (DiffusionProfile, bool) {
	primaryWeight := float64(slot.PrimaryWeight)
	secondaryWeight := 1 - primaryWeight

	primaryProfile, hasPrimary := DiffusionProfile{}, false
	if slot.Primary != 0 && primaryWeight > 0 {
		primaryProfile, hasPrimary = groupProfileFor(slot.Primary, groups, groupByMaterial)
	}
	secondaryProfile, hasSecondary := DiffusionProfile{}, false
	if slot.Secondary != 0 && secondaryWeight > 0 {
		secondaryProfile, hasSecondary = groupProfileFor(slot.Secondary, groups, groupByMaterial)
	}

	switch {
	case hasPrimary && hasSecondary:
		if slot.Primary == slot.Secondary {
			return primaryProfile, true
		}
		return blendProfiles(primaryProfile, secondaryProfile, primaryWeight, secondaryWeight), true
	case hasPrimary:
		return primaryProfile, true
	case hasSecondary:
		return secondaryProfile, true
	default:
		return DiffusionProfile{}, false
	}
}

func diffusionSigma(profile DiffusionProfile, thickness float32) float64 {
	thickScale := 0.0
	if thickness > 0 {
		thickScale = float64(thickness) / (1 + float64(thickness))
	}
	return float64(profile.Radius) * (minimumThicknessScale + (1-minimumThicknessScale)*thickScale)
}

func sampleRadius(qualityRadius int, profile DiffusionProfile, thickness float32) int {
	sigma := diffusionSigma(profile, thickness)
	if math.IsNaN(sigma) || sigma <= 0 {
		return 0
	}
	threeSigma := sigma * 3
	if threeSigma >= float64(qualityRadius) {
		return qualityRadius
	}
	radius := int(math.Ceil(threeSigma))
	if radius < 1 {
		radius = 1
	}
	return radius
}

func tapWeight(dx, dy int, profile DiffusionProfile, thickness float32) float64 {
	sigma := diffusionSigma(profile, thickness)
	if sigma <= 0 || math.IsNaN(sigma) {
		return 0
	}
	distance2 := float64(dx*dx + dy*dy)
	falloff := float64(profile.Falloff)
	if falloff < 0 {
		falloff = 0
	}
	base := 1 + distance2/(sigma*sigma)
	// Normalising the selected taps keeps both depth-cut foregrounds and
	// reduced quality kernels at the same overall brightness.
	return math.Pow(base, -(1 + falloff))
}

func filterPixel(x, y, w, h int, src Image, depth DepthTex, thickness ThicknessMap, radius int, profile DiffusionProfile, thick float32) RGB {
	centerDepth := float64(depth.Depth[y*w+x])
	if math.IsNaN(centerDepth) {
		return src.Pixels[y*w+x]
	}

	selectedRadius := sampleRadius(radius, profile, thick)
	var sumWeight float64
	var red, green, blue float64
	for dy := -selectedRadius; dy <= selectedRadius; dy++ {
		sy := y + dy
		if sy < 0 || sy >= h {
			continue
		}
		for dx := -selectedRadius; dx <= selectedRadius; dx++ {
			sx := x + dx
			if sx < 0 || sx >= w {
				continue
			}
			tapDepth := float64(depth.Depth[sy*w+sx])
			if math.IsNaN(tapDepth) || math.Abs(tapDepth-centerDepth) > MaxDepthDelta {
				continue
			}
			weight := tapWeight(dx, dy, profile, thick)
			if weight <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
				continue
			}
			px := src.Pixels[sy*w+sx]
			red += float64(px.R) * weight
			green += float64(px.G) * weight
			blue += float64(px.B) * weight
			sumWeight += weight
		}
	}
	if sumWeight <= 0 {
		return src.Pixels[y*w+x]
	}
	return RGB{
		R: float32(red / sumWeight),
		G: float32(green / sumWeight),
		B: float32(blue / sumWeight),
	}
}

// Blur runs the depth aware diffusion filter over one frame.
func (p *SubsurfaceScatteringPipeline) Blur(src Image, depth DepthTex) (Image, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.materialMap == nil {
		return Image{}, ErrNoMaterialMap
	}
	if p.thicknessMap == nil {
		return Image{}, ErrNoThicknessMap
	}
	if len(p.profiles) == 0 {
		return Image{}, ErrNoProfile
	}
	if !checkFrameSize(src, depth, *p.materialMap, *p.thicknessMap) {
		return Image{}, ErrBadImage
	}

	for _, slot := range p.materialMap.Slots {
		if slot.Primary != 0 {
			if _, ok := p.profiles[slot.Primary]; !ok {
				return Image{}, ErrUnknownMaterial
			}
		}
		if slot.Secondary != 0 {
			if _, ok := p.profiles[slot.Secondary]; !ok {
				return Image{}, ErrUnknownMaterial
			}
		}
	}

	pixelCount := int64(src.W) * int64(src.H)
	samples, tempBytes, fits := p.chooseQuality(pixelCount)
	if !fits {
		return Image{}, ErrFrameBudget
	}

	areas := collectMaterialAreas(*p.materialMap)
	groups, groupByMaterial := buildGroups(areas, p.maxGroups(), p.profiles)

	out := Image{W: src.W, H: src.H, Pixels: make([]RGB, len(src.Pixels))}
	radius := KernelRadii[p.quality]
	for y := 0; y < src.H; y++ {
		for x := 0; x < src.W; x++ {
			index := y*src.W + x
			profile, ok := pixelProfile(p.materialMap.Slots[index], groups, groupByMaterial)
			if !ok {
				out.Pixels[index] = src.Pixels[index]
				continue
			}
			out.Pixels[index] = filterPixel(x, y, src.W, src.H, src, depth, *p.thicknessMap, radius, profile, p.thicknessMap.Thickness[index])
		}
	}

	p.counters.Frames++
	p.counters.Groups = len(groups)
	p.counters.Samples = int(samples)
	p.counters.TempBytes = int(tempBytes)
	return out, nil
}

type snapshotProfile struct {
	ID      string           `json:"id"`
	Profile DiffusionProfile `json:"profile"`
}

type snapshotDocument struct {
	Version      int               `json:"version"`
	Profiles     []snapshotProfile `json:"profiles"`
	MaterialMap  *MaterialIDMap    `json:"material_map"`
	ThicknessMap *ThicknessMap     `json:"thickness_map"`
	Quality      int               `json:"quality"`
	KernelBudget int               `json:"kernel_budget"`
	Stats        SSSStats          `json:"stats"`
	Checksum     string            `json:"checksum"`
}

func (doc *snapshotDocument) checksum() (string, error) {
	doc.Checksum = ""
	encoded, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// Snapshot serialises the profiles, the maps, the quality and the budget.
func (p *SubsurfaceScatteringPipeline) Snapshot() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ids := make([]uint16, 0, len(p.profiles))
	for id := range p.profiles {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	doc := snapshotDocument{
		Version:      snapshotVersion,
		Profiles:     make([]snapshotProfile, 0, len(ids)),
		Quality:      p.quality,
		KernelBudget: p.kernelBudget,
		Stats:        p.statsLocked(),
	}
	for _, id := range ids {
		entry := p.profiles[id]
		doc.Profiles = append(doc.Profiles, snapshotProfile{ID: entry.id, Profile: entry.profile})
	}
	if p.materialMap != nil {
		cloned := cloneMaterialMap(*p.materialMap)
		doc.MaterialMap = &cloned
	}
	if p.thicknessMap != nil {
		cloned := cloneThicknessMap(*p.thicknessMap)
		doc.ThicknessMap = &cloned
	}

	checksum, err := doc.checksum()
	if err != nil {
		return nil, fmt.Errorf("encode snapshot: %w", err)
	}
	doc.Checksum = checksum
	return json.Marshal(doc)
}

func (p *SubsurfaceScatteringPipeline) restoreDocument(doc snapshotDocument) error {
	if doc.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if doc.Checksum == "" {
		return ErrBadSnapshot
	}
	expectedChecksum := doc.Checksum
	checksum, err := doc.checksum()
	if err != nil || checksum != expectedChecksum {
		return ErrBadSnapshot
	}
	if len(doc.Profiles) > p.maxProfiles() {
		return ErrBadSnapshot
	}
	if doc.Quality < 0 || doc.Quality >= QualityLevels {
		return ErrBadSnapshot
	}
	if doc.KernelBudget <= 0 || doc.KernelBudget > p.maxKernelBudget() {
		return ErrBadSnapshot
	}

	profiles := make(map[uint16]profileEntry, len(doc.Profiles))
	profileIDs := make(map[string]uint16, len(doc.Profiles))
	for _, record := range doc.Profiles {
		if !validProfile(record.ID, record.Profile) {
			return ErrBadSnapshot
		}
		if _, exists := profiles[record.Profile.MaterialID]; exists {
			return ErrBadSnapshot
		}
		if _, exists := profileIDs[record.ID]; exists {
			return ErrBadSnapshot
		}
		profiles[record.Profile.MaterialID] = profileEntry{id: record.ID, profile: record.Profile}
		profileIDs[record.ID] = record.Profile.MaterialID
	}

	var materialMap *MaterialIDMap
	if doc.MaterialMap != nil {
		cloned := cloneMaterialMap(*doc.MaterialMap)
		if err := validateMaterialMap(cloned); err != nil {
			return ErrBadSnapshot
		}
		materialMap = &cloned
	}
	var thicknessMap *ThicknessMap
	if doc.ThicknessMap != nil {
		cloned := cloneThicknessMap(*doc.ThicknessMap)
		if err := validateThicknessMap(cloned); err != nil {
			return ErrBadSnapshot
		}
		thicknessMap = &cloned
	}

	stats := doc.Stats
	if stats.Frames < 0 || stats.Profiles != len(profiles) || stats.Groups < 0 ||
		stats.Groups > p.maxGroups() || stats.Quality != doc.Quality ||
		stats.KernelRadius != KernelRadii[doc.Quality] || stats.Samples < 0 ||
		stats.Degrades < 0 || stats.TempBytes < 0 {
		return ErrBadSnapshot
	}

	p.profiles = profiles
	p.profileIDs = profileIDs
	p.materialMap = materialMap
	p.thicknessMap = thicknessMap
	p.quality = doc.Quality
	p.kernelBudget = doc.KernelBudget
	stats.Profiles = len(profiles)
	stats.Quality = doc.Quality
	stats.KernelRadius = KernelRadii[doc.Quality]
	p.counters = stats
	return nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *SubsurfaceScatteringPipeline) Restore(data []byte) error {
	var doc snapshotDocument
	if len(data) == 0 {
		return ErrBadSnapshot
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ErrBadSnapshot
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restoreDocument(doc)
}
