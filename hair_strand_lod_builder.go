// Package task clusters guide strands into scalp sections, builds hair strand
// LOD meshes with hard vertex caps and switches levels with hysteresis and
// cross fading.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors returned by the builder.
var (
	ErrBadInput       = errors.New("strand input is invalid")
	ErrTooManyGuides  = errors.New("guide count exceeds the limit")
	ErrClusterLimit   = errors.New("cluster limit reached")
	ErrUnknownCluster = errors.New("strand cluster is unknown")
	ErrBadLevel       = errors.New("lod level is out of range")
	ErrBadFade        = errors.New("fade duration is invalid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Vec3 is a point or a direction in model space.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// StrandLimits bounds the cluster table and the level meshes.
type StrandLimits struct {
	MaxClusters        int     `json:"max_clusters"`
	MaxGuides          int     `json:"max_guides"`
	LevelCount         int     `json:"level_count"`
	LevelDistance      float64 `json:"level_distance"`
	VertexBudget       int     `json:"vertex_budget"`
	MaxStrandsPerLevel int     `json:"max_strands_per_level"`
	StrandVertices     int     `json:"strand_vertices"`
	CardVertices       int     `json:"card_vertices"`
	StrandsPerCard     int     `json:"strands_per_card"`
	HysteresisRatio    float64 `json:"hysteresis_ratio"`
}

// GuideStrand is one simulated guide hair.
type GuideStrand struct {
	ID        string  `json:"id"`
	Bundle    string  `json:"bundle"`
	Section   string  `json:"section"`
	Root      Vec3    `json:"root"`
	Strands   int     `json:"strands"`
	Coverage  float64 `json:"coverage"`
	WindPhase float64 `json:"wind_phase"`
}

// StrandCluster is one scalp section worth of guides.
type StrandCluster struct {
	ID       string   `json:"id"`
	Section  string   `json:"section"`
	Guides   []string `json:"guides"`
	Strands  int      `json:"strands"`
	Coverage float64  `json:"coverage"`
	Root     Vec3     `json:"root"`
	Phase    float64  `json:"phase"`
}

// Camera is the view used to pick levels.
type Camera struct {
	Position Vec3 `json:"position"`
	Forward  Vec3 `json:"forward"`
}

// LodMesh is one generated level of one cluster.
type LodMesh struct {
	ClusterID   string  `json:"cluster_id"`
	Level       int     `json:"level"`
	StrandCount int     `json:"strand_count"`
	VertexCount int     `json:"vertex_count"`
	CardCount   int     `json:"card_count"`
	Merged      int     `json:"merged"`
	Phase       float64 `json:"phase"`
}

// LodSelection is one cluster entry of a frame selection.
type LodSelection struct {
	ClusterID   string `json:"cluster_id"`
	Level       int    `json:"level"`
	VertexCount int    `json:"vertex_count"`
	Fading      bool   `json:"fading"`
	FromLevel   int    `json:"from_level"`
}

// FadeWindow tracks one in-flight level transition.
type FadeWindow struct {
	ClusterID string        `json:"cluster_id"`
	FromLevel int           `json:"from_level"`
	ToLevel   int           `json:"to_level"`
	Elapsed   time.Duration `json:"elapsed"`
	Duration  time.Duration `json:"duration"`
}

type clusterData struct {
	cluster StrandCluster
	guides  []GuideStrand
}

// HairStrandLod owns the cluster table, the level meshes and the fade state.
type HairStrandLod struct {
	mu        sync.RWMutex
	limits    StrandLimits
	clusters  []StrandCluster
	data      map[string]clusterData
	clusterOf map[string]string
	meshes    map[string]map[int]LodMesh
	fade      time.Duration
	fades     map[string]FadeWindow
	selected  map[string]int
}

// NewHairStrandLod builds a builder with the given caps.
func NewHairStrandLod(limits StrandLimits) *HairStrandLod {
	return &HairStrandLod{
		limits:    limits,
		data:      make(map[string]clusterData),
		clusterOf: make(map[string]string),
		meshes:    make(map[string]map[int]LodMesh),
		fades:     make(map[string]FadeWindow),
		selected:  make(map[string]int),
	}
}

// Cluster groups guide strands by scalp section.
func (h *HairStrandLod) Cluster(guides []GuideStrand) ([]StrandCluster, error) {
	if len(guides) == 0 {
		return nil, ErrBadInput
	}

	h.mu.RLock()
	maxGuides := h.limits.MaxGuides
	maxClusters := h.limits.MaxClusters
	h.mu.RUnlock()

	seenIDs := make(map[string]struct{}, len(guides))
	bundleSection := make(map[string]string)
	for _, guide := range guides {
		if strings.TrimSpace(guide.ID) == "" || strings.TrimSpace(guide.Section) == "" {
			return nil, ErrBadInput
		}
		if _, exists := seenIDs[guide.ID]; exists {
			return nil, ErrBadInput
		}
		if guide.Strands <= 0 {
			return nil, ErrBadInput
		}
		if !(guide.Coverage >= 0 && guide.Coverage <= 1) {
			return nil, ErrBadInput
		}
		if guide.WindPhase < 0 {
			return nil, ErrBadInput
		}
		seenIDs[guide.ID] = struct{}{}
		if guide.Bundle != "" {
			if _, exists := bundleSection[guide.Bundle]; !exists {
				bundleSection[guide.Bundle] = guide.Section
			}
		}
	}

	if len(guides) > maxGuides {
		return nil, ErrTooManyGuides
	}

	grouped := make(map[string][]GuideStrand)
	for _, guide := range guides {
		section := guide.Section
		if guide.Bundle != "" {
			section = bundleSection[guide.Bundle]
		}
		grouped[section] = append(grouped[section], guide)
	}
	if len(grouped) > maxClusters {
		return nil, ErrClusterLimit
	}

	sections := make([]string, 0, len(grouped))
	for section := range grouped {
		sections = append(sections, section)
	}
	sort.Strings(sections)

	clusters := make([]StrandCluster, 0, len(sections))
	data := make(map[string]clusterData, len(sections))
	clusterOf := make(map[string]string, len(guides))
	for _, section := range sections {
		clusterGuides := grouped[section]
		sort.Slice(clusterGuides, func(i, j int) bool {
			return clusterGuides[i].ID < clusterGuides[j].ID
		})

		cluster := StrandCluster{
			ID:      section,
			Section: section,
			Guides:  make([]string, 0, len(clusterGuides)),
		}
		var root Vec3
		for _, guide := range clusterGuides {
			cluster.Guides = append(cluster.Guides, guide.ID)
			cluster.Strands += guide.Strands
			cluster.Coverage += guide.Coverage
			cluster.Phase += guide.WindPhase
			root.X += guide.Root.X
			root.Y += guide.Root.Y
			root.Z += guide.Root.Z
			clusterOf[guide.ID] = section
		}
		inv := 1.0 / float64(len(clusterGuides))
		cluster.Coverage *= inv
		cluster.Phase *= inv
		cluster.Root = Vec3{X: root.X * inv, Y: root.Y * inv, Z: root.Z * inv}
		clusters = append(clusters, cluster)
		data[section] = clusterData{cluster: cluster, guides: append([]GuideStrand(nil), clusterGuides...)}
	}

	h.mu.Lock()
	h.clusters = clusters
	h.data = data
	h.clusterOf = clusterOf
	h.meshes = make(map[string]map[int]LodMesh)
	h.fades = make(map[string]FadeWindow)
	h.selected = make(map[string]int)
	h.mu.Unlock()

	return cloneClusters(clusters), nil
}

// BuildLod generates one LOD level for a cluster.
func (h *HairStrandLod) BuildLod(clusterID string, level int) (LodMesh, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	data, ok := h.data[clusterID]
	if !ok {
		return LodMesh{}, ErrUnknownCluster
	}
	if level < 0 || level >= h.limits.LevelCount {
		return LodMesh{}, ErrBadLevel
	}

	mesh := h.buildMesh(data.cluster, level)
	if h.meshes[clusterID] == nil {
		h.meshes[clusterID] = make(map[int]LodMesh)
	}
	h.meshes[clusterID][level] = mesh
	return mesh, nil
}

type selectEntry struct {
	cluster   StrandCluster
	id        string
	distance  float64
	target    int
	cost      int
	prev      int
	hasPrev   bool
	from      int
	fading    bool
	locked    bool
	dropped   bool
	wasActive bool
}

// SelectLod picks a level per cluster for the camera and fits the budget.
func (h *HairStrandLod) SelectLod(view Camera, budget int) []LodSelection {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.clusters) == 0 || h.limits.LevelCount <= 0 {
		h.fades = make(map[string]FadeWindow)
		h.selected = make(map[string]int)
		return nil
	}

	budget = h.effectiveBudget(budget)
	maxLevel := h.limits.LevelCount - 1
	entries := make([]selectEntry, 0, len(h.clusters))
	total := 0
	for _, cluster := range h.clusters {
		entry := selectEntry{
			cluster:  cluster,
			id:       cluster.ID,
			distance: distanceTo(view.Position, cluster.Root),
		}
		entry.target = h.levelForDistance(entry.distance)
		if previous, ok := h.selected[cluster.ID]; ok && previous >= 0 && previous <= maxLevel {
			entry.prev = previous
			entry.hasPrev = true
		}
		if window, ok := h.fades[cluster.ID]; ok && window.Duration > 0 &&
			window.FromLevel >= 0 && window.FromLevel <= maxLevel &&
			window.ToLevel >= 0 && window.ToLevel <= maxLevel {
			entry.target = window.ToLevel
			entry.from = window.FromLevel
			entry.fading = true
			entry.locked = true
			entry.wasActive = true
			entry.cost = h.meshForLocked(cluster, window.FromLevel).VertexCount +
				h.meshForLocked(cluster, window.ToLevel).VertexCount
		} else {
			entry.cost = h.meshForLocked(cluster, entry.target).VertexCount
		}
		total += entry.cost
		entries = append(entries, entry)
	}

	// The farthest cluster pays first: step it down until the highest level,
	// then remove it if the frame still does not fit. Existing fades are locked
	// to their two levels and are removed whole when necessary.
	for total > budget {
		index := farthestEntry(entries)
		if index < 0 {
			break
		}
		entry := &entries[index]
		if !entry.locked && entry.target < maxLevel {
			oldCost := entry.cost
			entry.target++
			entry.cost = h.meshForLocked(entry.cluster, entry.target).VertexCount
			total += entry.cost - oldCost
			continue
		}
		entry.dropped = true
		total -= entry.cost
	}

	// Start new transitions near clusters first. A transition is only started
	// when the old and new levels fit together; otherwise the switch is delayed
	// and the old level remains selected for the frame.
	nearFirst := make([]int, 0, len(entries))
	for i := range entries {
		if !entries[i].dropped {
			nearFirst = append(nearFirst, i)
		}
	}
	sort.Slice(nearFirst, func(i, j int) bool {
		a, b := entries[nearFirst[i]], entries[nearFirst[j]]
		if a.distance != b.distance {
			return a.distance < b.distance
		}
		return a.id < b.id
	})

	nextFades := make(map[string]FadeWindow)
	for _, index := range nearFirst {
		entry := &entries[index]
		if entry.wasActive {
			nextFades[entry.id] = h.fades[entry.id]
			continue
		}
		if !entry.hasPrev || entry.target == entry.prev || h.fade <= 0 {
			continue
		}

		oldCost := h.meshForLocked(entry.cluster, entry.prev).VertexCount
		newCost := entry.cost
		if total+oldCost <= budget {
			total += oldCost
			entry.cost = oldCost + newCost
			entry.from = entry.prev
			entry.fading = true
			nextFades[entry.id] = FadeWindow{
				ClusterID: entry.id,
				FromLevel: entry.prev,
				ToLevel:   entry.target,
				Duration:  h.fade,
			}
			continue
		}

		total = total - newCost + oldCost
		entry.target = entry.prev
		entry.cost = oldCost
		entry.fading = false
		entry.from = 0
	}

	// A budget change can make even a delayed switch impossible (for example,
	// when the old level is now over budget). Keep the hard cap by dropping the
	// farthest remaining cluster.
	for total > budget {
		index := farthestEntry(entries)
		if index < 0 {
			break
		}
		entries[index].dropped = true
		total -= entries[index].cost
	}

	nextSelected := make(map[string]int, len(entries))
	selections := make([]LodSelection, 0, len(entries))
	finalFades := make(map[string]FadeWindow)
	for i := range entries {
		entry := &entries[i]
		if entry.dropped {
			continue
		}
		nextSelected[entry.id] = entry.target
		if entry.fading {
			if window, ok := nextFades[entry.id]; ok {
				finalFades[entry.id] = window
			}
		}
		selections = append(selections, LodSelection{
			ClusterID:   entry.id,
			Level:       entry.target,
			VertexCount: entry.cost,
			Fading:      entry.fading,
			FromLevel:   entry.from,
		})
	}
	sort.Slice(selections, func(i, j int) bool {
		return selections[i].ClusterID < selections[j].ClusterID
	})

	h.selected = nextSelected
	h.fades = finalFades
	return selections
}

// SetFade configures the cross fade duration.
func (h *HairStrandLod) SetFade(d time.Duration) error {
	if d < 0 {
		return ErrBadFade
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fade = d
	if d == 0 {
		h.fades = make(map[string]FadeWindow)
	}
	return nil
}

// AdvanceFade advances every active fade window and returns the survivors.
func (h *HairStrandLod) AdvanceFade(elapsed time.Duration) []FadeWindow {
	h.mu.Lock()
	defer h.mu.Unlock()

	if elapsed > 0 {
		for id, window := range h.fades {
			window.Elapsed += elapsed
			if window.Elapsed >= window.Duration {
				delete(h.fades, id)
				continue
			}
			h.fades[id] = window
		}
	}
	return sortedFadeWindows(h.fades)
}

// Fades returns the active fade windows, ordered by cluster id.
func (h *HairStrandLod) Fades() []FadeWindow {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return sortedFadeWindows(h.fades)
}

// ClusterOf resolves the cluster a guide belongs to.
func (h *HairStrandLod) ClusterOf(strandID string) (string, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	clusterID, ok := h.clusterOf[strandID]
	return clusterID, ok
}

// Mesh returns a generated level mesh.
func (h *HairStrandLod) Mesh(clusterID string, level int) (LodMesh, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	levels, ok := h.meshes[clusterID]
	if !ok {
		return LodMesh{}, false
	}
	mesh, ok := levels[level]
	return mesh, ok
}

const lodSnapshotVersion = 1

type snapshotCluster struct {
	Cluster StrandCluster `json:"cluster"`
	Guides  []GuideStrand `json:"guides"`
}

type snapshotMesh struct {
	ClusterID string    `json:"cluster_id"`
	Levels    []LodMesh `json:"levels"`
}

type snapshotSelection struct {
	ClusterID string `json:"cluster_id"`
	Level     int    `json:"level"`
}

type snapshotState struct {
	Limits   StrandLimits        `json:"limits"`
	Fade     time.Duration       `json:"fade"`
	Clusters []snapshotCluster   `json:"clusters"`
	Meshes   []snapshotMesh      `json:"meshes"`
	Fades    []FadeWindow        `json:"fades"`
	Selected []snapshotSelection `json:"selected"`
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

// Snapshot serialises clusters, meshes and fade state.
func (h *HairStrandLod) Snapshot() ([]byte, error) {
	h.mu.RLock()
	state := snapshotState{
		Limits:   h.limits,
		Fade:     h.fade,
		Clusters: make([]snapshotCluster, 0, len(h.clusters)),
		Meshes:   make([]snapshotMesh, 0, len(h.meshes)),
		Fades:    sortedFadeWindows(h.fades),
		Selected: make([]snapshotSelection, 0, len(h.selected)),
	}
	for _, cluster := range h.clusters {
		data := h.data[cluster.ID]
		state.Clusters = append(state.Clusters, snapshotCluster{
			Cluster: cloneCluster(cluster),
			Guides:  append([]GuideStrand(nil), data.guides...),
		})
	}

	meshClusterIDs := make([]string, 0, len(h.meshes))
	for clusterID := range h.meshes {
		meshClusterIDs = append(meshClusterIDs, clusterID)
	}
	sort.Strings(meshClusterIDs)
	for _, clusterID := range meshClusterIDs {
		levels := h.meshes[clusterID]
		levelNumbers := make([]int, 0, len(levels))
		for level := range levels {
			levelNumbers = append(levelNumbers, level)
		}
		sort.Ints(levelNumbers)
		entry := snapshotMesh{ClusterID: clusterID, Levels: make([]LodMesh, 0, len(levelNumbers))}
		for _, level := range levelNumbers {
			entry.Levels = append(entry.Levels, levels[level])
		}
		state.Meshes = append(state.Meshes, entry)
	}

	selectedIDs := make([]string, 0, len(h.selected))
	for clusterID := range h.selected {
		selectedIDs = append(selectedIDs, clusterID)
	}
	sort.Strings(selectedIDs)
	for _, clusterID := range selectedIDs {
		state.Selected = append(state.Selected, snapshotSelection{
			ClusterID: clusterID,
			Level:     h.selected[clusterID],
		})
	}
	h.mu.RUnlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	checksum := sha256.Sum256(payload)
	envelope, err := json.Marshal(snapshotEnvelope{
		Version:  lodSnapshotVersion,
		Checksum: hex.EncodeToString(checksum[:]),
		State:    payload,
	})
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return envelope, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (h *HairStrandLod) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := decodeStrict(data, &envelope); err != nil ||
		envelope.Version != lodSnapshotVersion || envelope.Checksum == "" || len(envelope.State) == 0 {
		return ErrBadSnapshot
	}
	checksum := sha256.Sum256(envelope.State)
	if hex.EncodeToString(checksum[:]) != envelope.Checksum {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := decodeStrict(envelope.State, &state); err != nil || state.Fade < 0 {
		return ErrBadSnapshot
	}

	clusters := make([]StrandCluster, 0, len(state.Clusters))
	dataByCluster := make(map[string]clusterData, len(state.Clusters))
	clusterOf := make(map[string]string)
	seenCluster := make(map[string]struct{}, len(state.Clusters))
	seenGuide := make(map[string]struct{})
	for _, saved := range state.Clusters {
		cluster := cloneCluster(saved.Cluster)
		if strings.TrimSpace(cluster.ID) == "" || strings.TrimSpace(cluster.Section) == "" {
			return ErrBadSnapshot
		}
		if _, exists := seenCluster[cluster.ID]; exists {
			return ErrBadSnapshot
		}
		if len(saved.Guides) == 0 || len(cluster.Guides) != len(saved.Guides) {
			return ErrBadSnapshot
		}
		seenCluster[cluster.ID] = struct{}{}

		guides := append([]GuideStrand(nil), saved.Guides...)
		sort.Slice(guides, func(i, j int) bool { return guides[i].ID < guides[j].ID })
		guideIDs := make([]string, 0, len(guides))
		for _, guide := range guides {
			if strings.TrimSpace(guide.ID) == "" || guide.Strands <= 0 {
				return ErrBadSnapshot
			}
			if _, exists := seenGuide[guide.ID]; exists {
				return ErrBadSnapshot
			}
			seenGuide[guide.ID] = struct{}{}
			guideIDs = append(guideIDs, guide.ID)
			clusterOf[guide.ID] = cluster.ID
		}
		sort.Strings(cluster.Guides)
		if len(cluster.Guides) != len(guideIDs) {
			return ErrBadSnapshot
		}
		for i := range guideIDs {
			if cluster.Guides[i] != guideIDs[i] {
				return ErrBadSnapshot
			}
		}
		clusters = append(clusters, cluster)
		dataByCluster[cluster.ID] = clusterData{cluster: cluster, guides: guides}
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].ID < clusters[j].ID })

	meshes := make(map[string]map[int]LodMesh, len(state.Meshes))
	for _, saved := range state.Meshes {
		if _, exists := dataByCluster[saved.ClusterID]; !exists {
			return ErrBadSnapshot
		}
		if _, exists := meshes[saved.ClusterID]; exists {
			return ErrBadSnapshot
		}
		levels := make(map[int]LodMesh, len(saved.Levels))
		for _, mesh := range saved.Levels {
			if mesh.ClusterID != saved.ClusterID ||
				mesh.Level < 0 || mesh.Level >= state.Limits.LevelCount ||
				mesh.VertexCount < 0 || mesh.StrandCount < 0 || mesh.CardCount < 0 || mesh.Merged < 0 {
				return ErrBadSnapshot
			}
			if _, exists := levels[mesh.Level]; exists {
				return ErrBadSnapshot
			}
			levels[mesh.Level] = mesh
		}
		meshes[saved.ClusterID] = levels
	}

	fades := make(map[string]FadeWindow, len(state.Fades))
	for _, window := range state.Fades {
		if _, exists := dataByCluster[window.ClusterID]; !exists {
			return ErrBadSnapshot
		}
		if _, exists := fades[window.ClusterID]; exists || window.Duration <= 0 ||
			window.Elapsed < 0 || window.Elapsed >= window.Duration ||
			window.FromLevel < 0 || window.FromLevel >= state.Limits.LevelCount ||
			window.ToLevel < 0 || window.ToLevel >= state.Limits.LevelCount {
			return ErrBadSnapshot
		}
		fades[window.ClusterID] = window
	}

	selected := make(map[string]int, len(state.Selected))
	for _, entry := range state.Selected {
		if _, exists := dataByCluster[entry.ClusterID]; !exists {
			return ErrBadSnapshot
		}
		if _, exists := selected[entry.ClusterID]; exists ||
			entry.Level < 0 || entry.Level >= state.Limits.LevelCount {
			return ErrBadSnapshot
		}
		selected[entry.ClusterID] = entry.Level
	}

	h.mu.Lock()
	h.limits = state.Limits
	h.clusters = clusters
	h.data = dataByCluster
	h.clusterOf = clusterOf
	h.meshes = meshes
	h.fade = state.Fade
	h.fades = fades
	h.selected = selected
	h.mu.Unlock()
	return nil
}

func (h *HairStrandLod) buildMesh(cluster StrandCluster, level int) LodMesh {
	strandCount := cluster.Strands
	for i := 0; i < level; i++ {
		strandCount = ceilDiv(strandCount, 2)
	}
	desired := strandCount
	merged := 0
	if h.limits.MaxStrandsPerLevel > 0 && strandCount > h.limits.MaxStrandsPerLevel {
		strandCount = h.limits.MaxStrandsPerLevel
		merged = desired - strandCount
	}

	cardCount := 0
	vertexCount := 0
	if level == h.limits.LevelCount-1 {
		perCard := h.limits.StrandsPerCard
		if perCard <= 0 {
			perCard = 1
		}
		cardCount = ceilDiv(strandCount, perCard)
		vertexCount = cardCount * h.limits.CardVertices
	} else {
		vertexCount = strandCount * h.limits.StrandVertices
	}
	if vertexCount < 0 {
		vertexCount = 0
	}
	return LodMesh{
		ClusterID:   cluster.ID,
		Level:       level,
		StrandCount: strandCount,
		VertexCount: vertexCount,
		CardCount:   cardCount,
		Merged:      merged,
		Phase:       cluster.Phase,
	}
}

func (h *HairStrandLod) meshForLocked(cluster StrandCluster, level int) LodMesh {
	if levels, ok := h.meshes[cluster.ID]; ok {
		if mesh, exists := levels[level]; exists {
			return mesh
		}
	}
	mesh := h.buildMesh(cluster, level)
	if h.meshes[cluster.ID] == nil {
		h.meshes[cluster.ID] = make(map[int]LodMesh)
	}
	h.meshes[cluster.ID][level] = mesh
	return mesh
}

func (h *HairStrandLod) levelForDistance(distance float64) int {
	maxLevel := h.limits.LevelCount - 1
	if maxLevel <= 0 {
		return 0
	}
	if math.IsNaN(distance) || distance <= 0 || h.limits.LevelDistance <= 0 ||
		math.IsNaN(h.limits.LevelDistance) {
		return 0
	}
	raw := distance / h.limits.LevelDistance
	if raw >= float64(maxLevel) {
		return maxLevel
	}
	level := int(math.Floor(raw))
	if level < 0 {
		return 0
	}
	if level > maxLevel {
		level = maxLevel
	}
	return level
}

func (h *HairStrandLod) effectiveBudget(budget int) int {
	if budget < 0 {
		budget = 0
	}
	if h.limits.VertexBudget < budget {
		budget = h.limits.VertexBudget
	}
	if budget < 0 {
		budget = 0
	}
	return budget
}

func farthestEntry(entries []selectEntry) int {
	index := -1
	for i := range entries {
		if entries[i].dropped {
			continue
		}
		if index < 0 || entries[i].distance > entries[index].distance ||
			(entries[i].distance == entries[index].distance && entries[i].id > entries[index].id) {
			index = i
		}
	}
	return index
}

func distanceTo(a, b Vec3) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	dz := a.Z - b.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func ceilDiv(value, divisor int) int {
	if value <= 0 {
		return 0
	}
	if divisor <= 0 {
		divisor = 1
	}
	quotient := value / divisor
	if value%divisor != 0 {
		quotient++
	}
	return quotient
}

func cloneCluster(cluster StrandCluster) StrandCluster {
	clone := cluster
	clone.Guides = append([]string(nil), cluster.Guides...)
	return clone
}

func cloneClusters(clusters []StrandCluster) []StrandCluster {
	clones := make([]StrandCluster, len(clusters))
	for i, cluster := range clusters {
		clones[i] = cloneCluster(cluster)
	}
	return clones
}

func sortedFadeWindows(windows map[string]FadeWindow) []FadeWindow {
	if len(windows) == 0 {
		return nil
	}
	ids := make([]string, 0, len(windows))
	for id := range windows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]FadeWindow, 0, len(ids))
	for _, id := range ids {
		out = append(out, windows[id])
	}
	return out
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("snapshot has trailing data")
		}
		return err
	}
	return nil
}
