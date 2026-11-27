// Package task bakes per-channel diffusion profiles for skin subsurface
// scattering and serves the pre-integrated table while a newer bake is still
// in flight.
package task

import "errors"

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Errors returned by the baker.
var (
	ErrBadConfig      = errors.New("scatter config is invalid")
	ErrRowLimit       = errors.New("row count exceeds the bake limit")
	ErrProfileLimit   = errors.New("profile limit reached")
	ErrUnknownProfile = errors.New("profile is not baked")
	ErrNoPendingBake  = errors.New("no bake is pending")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Spectrum holds one value per diffusion channel.
type Spectrum struct {
	R float64 `json:"r"`
	G float64 `json:"g"`
	B float64 `json:"b"`
}

// BakeLimits bounds the live tables of the baker.
type BakeLimits struct {
	MaxProfiles int `json:"max_profiles"`
	MaxRows     int `json:"max_rows"`
	RowBudget   int `json:"row_budget"`
}

// ThicknessPoint is one anchor of the thickness to radius curve.
type ThicknessPoint struct {
	Thickness float64 `json:"thickness"`
	Radius    float64 `json:"radius"`
}

// ScatterConfig is the material description handed to one bake.
type ScatterConfig struct {
	Scatter      Spectrum         `json:"scatter"`
	MeanFreePath Spectrum         `json:"mean_free_path"`
	Curve        []ThicknessPoint `json:"curve"`
	Curvature    float64          `json:"curvature"`
	ThicknessMin float64          `json:"thickness_min"`
	ThicknessMax float64          `json:"thickness_max"`
	MaxRadius    float64          `json:"max_radius"`
	Rows         int              `json:"rows"`
}

// ProfileRow is one row of the pre-integrated table.
type ProfileRow struct {
	Index     int      `json:"index"`
	Thickness float64  `json:"thickness"`
	Radius    float64  `json:"radius"`
	Diffusion Spectrum `json:"diffusion"`
}

// ProfileTable is the committed table served to the renderer.
type ProfileTable struct {
	ID           string       `json:"id"`
	Version      int64        `json:"version"`
	Rows         []ProfileRow `json:"rows"`
	MeanFreePath Spectrum     `json:"mean_free_path"`
	TotalEnergy  Spectrum     `json:"total_energy"`
	Complete     bool         `json:"complete"`
}

// BakeProgress describes one bake job.
type BakeProgress struct {
	ID       string `json:"id"`
	Baked    int    `json:"baked"`
	Total    int    `json:"total"`
	Version  int64  `json:"version"`
	Complete bool   `json:"complete"`
}

// SubsurfaceProfile owns the committed tables and the in-flight bake job.
type SubsurfaceProfile struct {
	limits BakeLimits
}

// NewSubsurfaceProfile builds a baker with the given caps.
func NewSubsurfaceProfile(limits BakeLimits) *SubsurfaceProfile {
	return &SubsurfaceProfile{limits: limits}
}

// Bake validates the config and starts a budgeted bake for one profile id.
func (p *SubsurfaceProfile) Bake(id string, cfg ScatterConfig) (ProfileTable, error) {
	return ProfileTable{}, errNotImplemented
}

// Advance bakes up to budget rows of the pending job and commits when done.
func (p *SubsurfaceProfile) Advance(budget int) (BakeProgress, error) {
	return BakeProgress{}, errNotImplemented
}

// Sample interpolates the committed table over thickness and applies the
// radial falloff for the requested radius.
func (p *SubsurfaceProfile) Sample(id string, radius, thickness float64) (Spectrum, error) {
	return Spectrum{}, errNotImplemented
}

// Invalidate drops the committed table and any pending job of one id.
func (p *SubsurfaceProfile) Invalidate(id string) error {
	return errNotImplemented
}

// Profile returns a copy of the committed table.
func (p *SubsurfaceProfile) Profile(id string) (ProfileTable, bool) {
	return ProfileTable{}, false
}

// Pending reports the in-flight bake job of one id.
func (p *SubsurfaceProfile) Pending(id string) (BakeProgress, bool) {
	return BakeProgress{}, false
}

// Version returns the committed version of one id.
func (p *SubsurfaceProfile) Version(id string) (int64, error) {
	return 0, errNotImplemented
}

// ChangedRows lists the rows whose thickness or radius differs from the
// committed table of one id.
func (p *SubsurfaceProfile) ChangedRows(id string, cfg ScatterConfig) ([]int, error) {
	return nil, errNotImplemented
}

// Count returns the number of committed tables.
func (p *SubsurfaceProfile) Count() int {
	return 0
}

// Snapshot serialises the committed tables and the pending job.
func (p *SubsurfaceProfile) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *SubsurfaceProfile) Restore(data []byte) error {
	return errNotImplemented
}
