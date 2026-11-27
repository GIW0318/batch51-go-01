// Package task generates fur shells along mesh normals, samples the density
// map that drives the layer count and derives cheaper shadow shells.
package task

import "errors"

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Errors returned by the generator.
var (
	ErrBadMesh      = errors.New("mesh is invalid")
	ErrBadDensity   = errors.New("density map is invalid")
	ErrBadLayers    = errors.New("layer request is invalid")
	ErrBadPolicy    = errors.New("offset policy is invalid")
	ErrVertexBudget = errors.New("fur vertex budget exceeded")
	ErrNotReady     = errors.New("density map is not loaded")
	ErrBadSnapshot  = errors.New("snapshot payload is invalid")
)

// Vec3 is a point or a direction in model space.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// UV is a texture coordinate in the unit square.
type UV struct {
	U float64 `json:"u"`
	V float64 `json:"v"`
}

// Vertex is one skinned mesh vertex.
type Vertex struct {
	Position Vec3 `json:"position"`
	Normal   Vec3 `json:"normal"`
	UV       UV   `json:"uv"`
}

// Mesh is the source surface the shells grow from.
type Mesh struct {
	Vertices []Vertex `json:"vertices"`
}

// DensityMap is a row major fur density texture.
type DensityMap struct {
	Width  int       `json:"width"`
	Height int       `json:"height"`
	Values []float64 `json:"values"`
}

// FurLimits bounds the shell set of one object.
type FurLimits struct {
	MaxLayers      int     `json:"max_layers"`
	LayerSpacing   float64 `json:"layer_spacing"`
	MaxVertices    int     `json:"max_vertices"`
	ShadowLayers   int     `json:"shadow_layers"`
	ShadowDistance float64 `json:"shadow_distance"`
}

// OffsetPolicy bends every shell with gravity and wind.
type OffsetPolicy struct {
	Gravity   Vec3    `json:"gravity"`
	Wind      Vec3    `json:"wind"`
	MaxOffset float64 `json:"max_offset"`
}

// ShellLayer is one aggregated shell across all source vertices.
type ShellLayer struct {
	Index    int     `json:"index"`
	Offset   Vec3    `json:"offset"`
	Distance float64 `json:"distance"`
	Alpha    float64 `json:"alpha"`
	Vertices int     `json:"vertices"`
	Tip      bool    `json:"tip"`
}

// ShellSet is the generated shell stack.
type ShellSet struct {
	Layers      []ShellLayer `json:"layers"`
	VertexCount int          `json:"vertex_count"`
	Shadow      bool         `json:"shadow"`
	SkinVisible bool         `json:"skin_visible"`
	Distance    float64      `json:"distance"`
	Policy      OffsetPolicy `json:"policy"`
}

// FurShellGenerator owns the last density map and the offset policy.
type FurShellGenerator struct {
	limits FurLimits
}

// NewFurShellGenerator builds a generator with the given caps.
func NewFurShellGenerator(limits FurLimits) *FurShellGenerator {
	return &FurShellGenerator{limits: limits}
}

// Generate builds the shell stack for a mesh and its density map.
func (g *FurShellGenerator) Generate(mesh Mesh, density DensityMap) (ShellSet, error) {
	return ShellSet{}, errNotImplemented
}

// SampleDensity bilinearly samples the loaded density map.
func (g *FurShellGenerator) SampleDensity(uv UV) (float64, error) {
	return 0, errNotImplemented
}

// BuildShadowShells derives a cheaper shell stack for shadow casting.
func (g *FurShellGenerator) BuildShadowShells(set ShellSet, maxLayers int) (ShellSet, error) {
	return ShellSet{}, errNotImplemented
}

// SetOffsetPolicy replaces the gravity and wind applied by later generates.
func (g *FurShellGenerator) SetOffsetPolicy(p OffsetPolicy) error {
	return errNotImplemented
}

// OffsetPolicy returns the active offset policy.
func (g *FurShellGenerator) OffsetPolicy() OffsetPolicy {
	return OffsetPolicy{}
}

// TargetLayers converts a density value to a layer count.
func (g *FurShellGenerator) TargetLayers(density float64) (int, error) {
	return 0, errNotImplemented
}

// LayerAt reads one layer out of a shell set.
func (g *FurShellGenerator) LayerAt(set ShellSet, index int) (ShellLayer, error) {
	return ShellLayer{}, errNotImplemented
}

// VertexTotal sums the vertex count of a shell set.
func (g *FurShellGenerator) VertexTotal(set ShellSet) int {
	return 0
}

// Snapshot serialises the density map and the offset policy.
func (g *FurShellGenerator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *FurShellGenerator) Restore(data []byte) error {
	return errNotImplemented
}
