// Package task schedules the minion waves of the three lanes of one MOBA
// match: wave cadence, siege minion cadence, super minion replacement after a
// high ground tower falls and the bounded pressure compensation budget.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Lane identifies one of the three lanes.
type Lane string

// The three lanes of a match.
const (
	LaneTop Lane = "top"
	LaneMid Lane = "mid"
	LaneBot Lane = "bot"
)

// Event kinds emitted by the scheduler.
const (
	EventWave      = "wave"
	EventStructure = "structure"
	EventPressure  = "pressure"
	EventPause     = "pause"
)

// Errors returned by the scheduler.
var (
	ErrBlankMatchID           = errors.New("match id is required")
	ErrAlreadyStarted         = errors.New("match is already started")
	ErrNotStarted             = errors.New("match is not started")
	ErrInvalidConfig          = errors.New("lane configuration is invalid")
	ErrUnknownLane            = errors.New("lane is unknown")
	ErrInvalidWaveIndex       = errors.New("wave index must be positive")
	ErrInvalidTier            = errors.New("structure tier is invalid")
	ErrStructureAlreadyBroken = errors.New("structure tier is already broken")
	ErrPressureBelowThreshold = errors.New("lane pressure is below the compensation threshold")
	ErrCompensationExhausted  = errors.New("compensation budget is exhausted")
	ErrAlreadyPaused          = errors.New("match is already paused")
	ErrNotPaused              = errors.New("match is not paused")
	ErrBadSnapshot            = errors.New("snapshot payload is invalid")
)

// LaneConfig tunes the wave schedule of one match.
type LaneConfig struct {
	WaveInterval         time.Duration `json:"wave_interval"`
	FirstWaveDelay       time.Duration `json:"first_wave_delay"`
	MeleePerWave         int           `json:"melee_per_wave"`
	RangedPerWave        int           `json:"ranged_per_wave"`
	CannonEvery          int           `json:"cannon_every"`
	SuperMinions         int           `json:"super_minions"`
	MaxCompensation      int           `json:"max_compensation"`
	CompensationUnits    int           `json:"compensation_units"`
	MaxCompensationUnits int           `json:"max_compensation_units"`
	PressureThreshold    int           `json:"pressure_threshold"`
}

// Wave is the composition of one lane wave.
type Wave struct {
	Lane         Lane      `json:"lane"`
	Index        int       `json:"index"`
	At           time.Time `json:"at"`
	Melee        int       `json:"melee"`
	Ranged       int       `json:"ranged"`
	Cannon       int       `json:"cannon"`
	Super        int       `json:"super"`
	Compensation bool      `json:"compensation"`
}

// WaveEvent is one schedule event of one lane.
type WaveEvent struct {
	Kind   string    `json:"kind"`
	Lane   Lane      `json:"lane"`
	Index  int       `json:"index"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}

// LaneStatus is the operator view of one lane.
type LaneStatus struct {
	Lane                Lane      `json:"lane"`
	LastIndex           int       `json:"last_index"`
	NextAt              time.Time `json:"next_at"`
	HighGroundBroken    bool      `json:"high_ground_broken"`
	PendingCompensation int       `json:"pending_compensation"`
	CompensationWaves   int       `json:"compensation_waves"`
}

// laneState is the mutable schedule of a single lane.
type laneState struct {
	lastIndex           int
	highGroundAt        time.Time
	highGround          bool
	pendingCompensation int
	compensationWaves   int
	compensationUnits   int
}

// WaveScheduler owns the match clock and the three lane schedules.
type WaveScheduler struct {
	cfg      LaneConfig
	matchID  string
	started  bool
	now      time.Time
	paused   bool
	pausedAt time.Time
}

// NewWaveScheduler builds a scheduler with the given lane configuration.
func NewWaveScheduler(cfg LaneConfig) *WaveScheduler {
	return &WaveScheduler{cfg: cfg}
}

// Start opens the match clock at the given instant.
func (s *WaveScheduler) Start(matchID string, at time.Time) error {
	return errNotImplemented
}

// Tick advances the clock and returns every wave generated since the last tick.
func (s *WaveScheduler) Tick(now time.Time) []WaveEvent {
	return nil
}

// WaveFor computes the composition of wave n of one lane without generating it.
func (s *WaveScheduler) WaveFor(lane Lane, n int) (Wave, error) {
	return Wave{}, errNotImplemented
}

// ApplyStructureBreak records that a tower of the given tier fell.
func (s *WaveScheduler) ApplyStructureBreak(lane Lane, tier int, at time.Time) error {
	return errNotImplemented
}

// ReportPressure records that a lane is being pushed into its base.
func (s *WaveScheduler) ReportPressure(lane Lane, deficit int, at time.Time) error {
	return errNotImplemented
}

// Compensations reports how many compensation waves a lane already generated.
func (s *WaveScheduler) Compensations(lane Lane) int {
	return 0
}

// PendingCompensation reports how many compensation waves are queued.
func (s *WaveScheduler) PendingCompensation(lane Lane) int {
	return 0
}

// NextWaveAt reports when the next wave of a lane is due.
func (s *WaveScheduler) NextWaveAt(lane Lane) (time.Time, error) {
	return time.Time{}, errNotImplemented
}

// WaveIndex reports the highest wave index already generated for a lane.
func (s *WaveScheduler) WaveIndex(lane Lane) (int, error) {
	return 0, errNotImplemented
}

// LaneStatus reads the whole operator view of one lane.
func (s *WaveScheduler) LaneStatus(lane Lane) (LaneStatus, error) {
	return LaneStatus{}, errNotImplemented
}

// Pause freezes the wave clock of the match.
func (s *WaveScheduler) Pause(at time.Time) error {
	return errNotImplemented
}

// Resume restarts the wave clock of the match.
func (s *WaveScheduler) Resume(at time.Time) error {
	return errNotImplemented
}

// Events returns a copy of the schedule event log, oldest first.
func (s *WaveScheduler) Events() []WaveEvent {
	return nil
}

// MatchID returns the match the scheduler was started with.
func (s *WaveScheduler) MatchID() string {
	return ""
}

// Snapshot serialises the clock, the lane schedules and the budgets.
func (s *WaveScheduler) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *WaveScheduler) Restore(data []byte) error {
	return errNotImplemented
}
