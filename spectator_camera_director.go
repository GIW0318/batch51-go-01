// Package task runs the automatic observer director of a match: it scores
// candidate cameras, switches with hysteresis and a switch budget, and hands
// control back and forth with a human operator.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Event kinds observed by the director.
const (
	EventKill              = "kill"
	EventTeamfight         = "teamfight"
	EventObjective         = "objective"
	EventUltimate          = "ultimate"
	EventObjectiveCritical = "objective_critical"
)

// Action weights of the event kinds.
const (
	WeightKill              = 40
	WeightTeamfight         = 30
	WeightObjective         = 20
	WeightUltimate          = 10
	WeightObjectiveCritical = 50
)

// Reasons attached to a selected shot.
const (
	ReasonSwitch     = "reason_switch"
	ReasonDwell      = "reason_dwell"
	ReasonHysteresis = "reason_hysteresis"
	ReasonIdle       = "reason_idle"
	ReasonBudget     = "reason_budget"
	ReasonManual     = "reason_manual"
	ReasonCooldown   = "reason_cooldown"
	ReasonCritical   = "reason_critical"
)

// Errors reported by the director.
var (
	ErrUnknownCamera     = errors.New("unknown camera")
	ErrBadEvent          = errors.New("unknown match event")
	ErrEventOutOfOrder   = errors.New("match event is out of order")
	ErrAlreadyOverridden = errors.New("camera is already under manual control")
	ErrNotOverridden     = errors.New("no manual takeover is active")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// CameraInfo is one candidate camera of the match.
type CameraInfo struct {
	CameraID string
	Label    string
}

// DirectorConfig holds the static camera list and the switching policy.
type DirectorConfig struct {
	Cameras          []CameraInfo
	MinDwell         time.Duration
	Hysteresis       int
	SwitchBudget     int
	ScoreThreshold   int
	OverrideCooldown time.Duration
	Delay            time.Duration
}

// MatchEvent is one scored action reported by the match server.
type MatchEvent struct {
	Kind     string
	CameraID string
	Target   string
	At       time.Time
}

// CameraShot is the shot the director settles on at one instant.
type CameraShot struct {
	CameraID string
	Target   string
	Score    int
	Switched bool
	Reason   string
	At       time.Time
	Timeline time.Time
}

// CameraScore is one entry of the score table.
type CameraScore struct {
	CameraID string
	Score    int
}

// SpectatorCameraDirector owns the score table and the switching state.
type SpectatorCameraDirector struct {
	cfg DirectorConfig
}

// NewSpectatorCameraDirector builds a director from the match camera list.
func NewSpectatorCameraDirector(cfg DirectorConfig) *SpectatorCameraDirector {
	return &SpectatorCameraDirector{cfg: cfg}
}

// Observe adds one scored event to the candidate cameras.
func (d *SpectatorCameraDirector) Observe(ev MatchEvent) error {
	return errNotImplemented
}

// Select decides which camera is on air at now.
func (d *SpectatorCameraDirector) Select(now time.Time) (CameraShot, error) {
	return CameraShot{}, errNotImplemented
}

// Override gives one camera to the human operator until the given instant.
func (d *SpectatorCameraDirector) Override(cameraID string, until time.Time) error {
	return errNotImplemented
}

// Release hands control back from the human operator.
func (d *SpectatorCameraDirector) Release(cameraID string) error {
	return errNotImplemented
}

// Score reads the accumulated score of one camera.
func (d *SpectatorCameraDirector) Score(cameraID string) (int, error) {
	return 0, errNotImplemented
}

// Scores lists the score table ordered by score, then camera id.
func (d *SpectatorCameraDirector) Scores() []CameraScore {
	return nil
}

// Current returns the shot currently on air.
func (d *SpectatorCameraDirector) Current() (CameraShot, bool) {
	return CameraShot{}, false
}

// SwitchCount reports how many automatic switches this match has used.
func (d *SpectatorCameraDirector) SwitchCount() int {
	return 0
}

// Snapshot serialises the score table and the switching state.
func (d *SpectatorCameraDirector) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the director state with a snapshot, all or nothing.
func (d *SpectatorCameraDirector) Restore(data []byte) error {
	return errNotImplemented
}
