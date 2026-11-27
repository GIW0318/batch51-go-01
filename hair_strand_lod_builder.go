// Package task clusters guide strands into scalp sections, builds hair strand
// LOD meshes with hard vertex caps and switches levels with hysteresis and
// cross fading.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

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

// HairStrandLod owns the cluster table, the level meshes and the fade state.
type HairStrandLod struct {
	limits StrandLimits
}

// NewHairStrandLod builds a builder with the given caps.
func NewHairStrandLod(limits StrandLimits) *HairStrandLod {
	return &HairStrandLod{limits: limits}
}

// Cluster groups guide strands by scalp section.
func (h *HairStrandLod) Cluster(guides []GuideStrand) ([]StrandCluster, error) {
	return nil, errNotImplemented
}

// BuildLod generates one LOD level for a cluster.
func (h *HairStrandLod) BuildLod(clusterID string, level int) (LodMesh, error) {
	return LodMesh{}, errNotImplemented
}

// SelectLod picks a level per cluster for the camera and fits the budget.
func (h *HairStrandLod) SelectLod(view Camera, budget int) []LodSelection {
	return nil
}

// SetFade configures the cross fade duration.
func (h *HairStrandLod) SetFade(d time.Duration) error {
	return errNotImplemented
}

// AdvanceFade advances every active fade window and returns the survivors.
func (h *HairStrandLod) AdvanceFade(elapsed time.Duration) []FadeWindow {
	return nil
}

// Fades returns the active fade windows, ordered by cluster id.
func (h *HairStrandLod) Fades() []FadeWindow {
	return nil
}

// ClusterOf resolves the cluster a guide belongs to.
func (h *HairStrandLod) ClusterOf(strandID string) (string, bool) {
	return "", false
}

// Mesh returns a generated level mesh.
func (h *HairStrandLod) Mesh(clusterID string, level int) (LodMesh, bool) {
	return LodMesh{}, false
}

// Snapshot serialises clusters, meshes and fade state.
func (h *HairStrandLod) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (h *HairStrandLod) Restore(data []byte) error {
	return errNotImplemented
}
