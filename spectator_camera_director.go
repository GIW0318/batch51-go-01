// Package task runs the automatic observer director of a match: it scores
// candidate cameras, switches with hysteresis and a switch budget, and hands
// control back and forth with a human operator.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"sync"
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
	mu  sync.RWMutex
	cfg DirectorConfig

	cameras map[string]struct{}
	order   []string
	scores  map[string]int
	targets map[string]string

	initialized   bool
	currentID     string
	currentShot   CameraShot
	lastSwitch    time.Time
	hasLastSwitch bool
	lastEventAt   time.Time
	hasLastEvent  bool
	switchCount   int

	override      *manualOverride
	cooldownUntil time.Time
	hasCooldown   bool

	criticalPending bool
	criticalCamera  string
	criticalTarget  string
}

type manualOverride struct {
	cameraID string
	until    time.Time
}

const snapshotVersion = 1

type snapshotOverride struct {
	CameraID string    `json:"camera_id"`
	Until    time.Time `json:"until"`
}

type directorSnapshot struct {
	Version         int               `json:"version"`
	Scores          []CameraScore     `json:"scores"`
	Targets         map[string]string `json:"targets,omitempty"`
	Initialized     bool              `json:"initialized"`
	CurrentID       string            `json:"current_id"`
	CurrentShot     CameraShot        `json:"current_shot"`
	LastSwitch      time.Time         `json:"last_switch"`
	HasLastSwitch   bool              `json:"has_last_switch"`
	LastEventAt     time.Time         `json:"last_event_at"`
	HasLastEvent    bool              `json:"has_last_event"`
	SwitchCount     int               `json:"switch_count"`
	Override        *snapshotOverride `json:"override,omitempty"`
	CooldownUntil   time.Time         `json:"cooldown_until"`
	HasCooldown     bool              `json:"has_cooldown"`
	CriticalPending bool              `json:"critical_pending"`
	CriticalCamera  string            `json:"critical_camera"`
	CriticalTarget  string            `json:"critical_target"`
}

// NewSpectatorCameraDirector builds a director from the match camera list.
func NewSpectatorCameraDirector(cfg DirectorConfig) *SpectatorCameraDirector {
	cfg.Cameras = append([]CameraInfo(nil), cfg.Cameras...)
	d := &SpectatorCameraDirector{
		cfg:     cfg,
		cameras: make(map[string]struct{}, len(cfg.Cameras)),
		order:   make([]string, 0, len(cfg.Cameras)),
		scores:  make(map[string]int, len(cfg.Cameras)),
		targets: make(map[string]string, len(cfg.Cameras)),
	}
	for _, camera := range cfg.Cameras {
		if _, ok := d.cameras[camera.CameraID]; ok {
			continue
		}
		d.cameras[camera.CameraID] = struct{}{}
		d.order = append(d.order, camera.CameraID)
		d.scores[camera.CameraID] = 0
	}
	return d
}

// Observe adds one scored event to the candidate cameras.
func (d *SpectatorCameraDirector) Observe(ev MatchEvent) error {
	if !d.hasCamera(ev.CameraID) {
		return ErrUnknownCamera
	}
	weight, ok := eventWeight(ev.Kind)
	if !ok {
		return ErrBadEvent
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.hasLastEvent && ev.At.Before(d.lastEventAt) {
		return ErrEventOutOfOrder
	}

	d.scores[ev.CameraID] += weight
	d.lastEventAt = ev.At
	d.hasLastEvent = true
	d.targets[ev.CameraID] = ev.Target

	// A manual takeover overrides all automatic decisions. Events observed
	// while it is active only update the scores; they must not cause a later
	// switch to a historical shot.
	if ev.Kind == EventObjectiveCritical && d.override == nil {
		d.criticalPending = true
		d.criticalCamera = ev.CameraID
		d.criticalTarget = ev.Target
	}
	return nil
}

// Select decides which camera is on air at now.
func (d *SpectatorCameraDirector) Select(now time.Time) (CameraShot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.selectLocked(now), nil
}

// Override gives one camera to the human operator until the given instant.
func (d *SpectatorCameraDirector) Override(cameraID string, until time.Time) error {
	if !d.hasCamera(cameraID) {
		return ErrUnknownCamera
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.override != nil {
		return ErrAlreadyOverridden
	}

	d.override = &manualOverride{cameraID: cameraID, until: until}
	d.hasCooldown = false
	d.criticalPending = false
	d.criticalCamera = ""
	d.criticalTarget = ""

	d.initialized = true
	d.currentID = cameraID
	d.currentShot = CameraShot{
		CameraID: cameraID,
		Target:   d.targets[cameraID],
		Score:    d.scores[cameraID],
		Reason:   ReasonManual,
	}
	return nil
}

// Release hands control back from the human operator.
func (d *SpectatorCameraDirector) Release(cameraID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.override == nil || d.override.cameraID != cameraID {
		return ErrNotOverridden
	}

	currentID := d.override.cameraID
	d.cooldownUntil = d.override.until.Add(d.cfg.OverrideCooldown)
	d.hasCooldown = true
	d.override = nil
	d.criticalPending = false
	d.criticalCamera = ""
	d.criticalTarget = ""

	d.initialized = true
	d.currentID = currentID
	d.currentShot.CameraID = currentID
	d.currentShot.Target = d.targets[currentID]
	d.currentShot.Score = d.scores[currentID]
	d.currentShot.Switched = false
	d.currentShot.Reason = ReasonCooldown
	return nil
}

// Score reads the accumulated score of one camera.
func (d *SpectatorCameraDirector) Score(cameraID string) (int, error) {
	if !d.hasCamera(cameraID) {
		return 0, ErrUnknownCamera
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.scores[cameraID], nil
}

// Scores lists the score table ordered by score, then camera id.
func (d *SpectatorCameraDirector) Scores() []CameraScore {
	d.mu.RLock()
	out := make([]CameraScore, 0, len(d.order))
	for _, cameraID := range d.order {
		out = append(out, CameraScore{CameraID: cameraID, Score: d.scores[cameraID]})
	}
	d.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].CameraID < out[j].CameraID
	})
	return out
}

// Current returns the shot currently on air.
func (d *SpectatorCameraDirector) Current() (CameraShot, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if !d.initialized || d.currentID == "" {
		return CameraShot{}, false
	}
	shot := d.currentShot
	shot.CameraID = d.currentID
	shot.Score = d.scores[d.currentID]
	if target, ok := d.targets[d.currentID]; ok {
		shot.Target = target
	}
	return shot, true
}

// SwitchCount reports how many automatic switches this match has used.
func (d *SpectatorCameraDirector) SwitchCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.switchCount
}

// Snapshot serialises the score table and the switching state.
func (d *SpectatorCameraDirector) Snapshot() ([]byte, error) {
	d.mu.RLock()
	snap := directorSnapshot{
		Version:         snapshotVersion,
		Scores:          make([]CameraScore, 0, len(d.order)),
		Targets:         make(map[string]string, len(d.targets)),
		Initialized:     d.initialized,
		CurrentID:       d.currentID,
		CurrentShot:     d.currentShot,
		LastSwitch:      d.lastSwitch,
		HasLastSwitch:   d.hasLastSwitch,
		LastEventAt:     d.lastEventAt,
		HasLastEvent:    d.hasLastEvent,
		SwitchCount:     d.switchCount,
		CooldownUntil:   d.cooldownUntil,
		HasCooldown:     d.hasCooldown,
		CriticalPending: d.criticalPending,
		CriticalCamera:  d.criticalCamera,
		CriticalTarget:  d.criticalTarget,
	}
	for _, cameraID := range d.order {
		snap.Scores = append(snap.Scores, CameraScore{CameraID: cameraID, Score: d.scores[cameraID]})
	}
	for cameraID, target := range d.targets {
		snap.Targets[cameraID] = target
	}
	if d.override != nil {
		snap.Override = &snapshotOverride{CameraID: d.override.cameraID, Until: d.override.until}
	}
	d.mu.RUnlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the director state with a snapshot, all or nothing.
func (d *SpectatorCameraDirector) Restore(data []byte) error {
	if len(data) == 0 {
		return ErrBadSnapshot
	}
	var snap directorSnapshot
	if err := json.Unmarshal(data, &snap); err != nil || snap.Version != snapshotVersion {
		return ErrBadSnapshot
	}

	scores := make(map[string]int, len(d.order))
	for _, cameraID := range d.order {
		scores[cameraID] = 0
	}
	seen := make(map[string]struct{}, len(snap.Scores))
	for _, entry := range snap.Scores {
		if _, ok := d.cameras[entry.CameraID]; !ok {
			return ErrBadSnapshot
		}
		if _, duplicate := seen[entry.CameraID]; duplicate {
			return ErrBadSnapshot
		}
		if entry.Score < 0 {
			return ErrBadSnapshot
		}
		seen[entry.CameraID] = struct{}{}
		scores[entry.CameraID] = entry.Score
	}

	targets := make(map[string]string, len(snap.Targets))
	for cameraID, target := range snap.Targets {
		if _, ok := d.cameras[cameraID]; !ok {
			return ErrBadSnapshot
		}
		targets[cameraID] = target
	}

	if snap.SwitchCount < 0 || snap.SwitchCount > effectiveBudget(d.cfg.SwitchBudget)+1 {
		return ErrBadSnapshot
	}
	if snap.Initialized {
		if _, ok := d.cameras[snap.CurrentID]; !ok {
			return ErrBadSnapshot
		}
	} else if snap.CurrentID != "" {
		return ErrBadSnapshot
	}
	if snap.CurrentShot.CameraID != "" {
		if _, ok := d.cameras[snap.CurrentShot.CameraID]; !ok {
			return ErrBadSnapshot
		}
		if snap.Initialized && snap.CurrentShot.CameraID != snap.CurrentID {
			return ErrBadSnapshot
		}
		if !snap.Initialized {
			return ErrBadSnapshot
		}
	}
	if snap.CurrentShot.Score < 0 {
		return ErrBadSnapshot
	}
	if snap.CriticalCamera != "" {
		if _, ok := d.cameras[snap.CriticalCamera]; !ok {
			return ErrBadSnapshot
		}
	}
	if snap.CriticalPending && snap.CriticalCamera == "" {
		return ErrBadSnapshot
	}
	if snap.Override != nil {
		if _, ok := d.cameras[snap.Override.CameraID]; !ok {
			return ErrBadSnapshot
		}
		if !snap.Initialized || snap.CurrentID != snap.Override.CameraID {
			return ErrBadSnapshot
		}
	}

	var override *manualOverride
	if snap.Override != nil {
		override = &manualOverride{cameraID: snap.Override.CameraID, until: snap.Override.Until}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.scores = scores
	d.targets = targets
	d.initialized = snap.Initialized
	d.currentID = snap.CurrentID
	d.currentShot = snap.CurrentShot
	d.lastSwitch = snap.LastSwitch
	d.hasLastSwitch = snap.HasLastSwitch
	d.lastEventAt = snap.LastEventAt
	d.hasLastEvent = snap.HasLastEvent
	d.switchCount = snap.SwitchCount
	d.override = override
	d.cooldownUntil = snap.CooldownUntil
	d.hasCooldown = snap.HasCooldown
	d.criticalPending = snap.CriticalPending
	d.criticalCamera = snap.CriticalCamera
	d.criticalTarget = snap.CriticalTarget
	return nil
}

func eventWeight(kind string) (int, bool) {
	switch kind {
	case EventKill:
		return WeightKill, true
	case EventTeamfight:
		return WeightTeamfight, true
	case EventObjective:
		return WeightObjective, true
	case EventUltimate:
		return WeightUltimate, true
	case EventObjectiveCritical:
		return WeightObjectiveCritical, true
	default:
		return 0, false
	}
}

func (d *SpectatorCameraDirector) hasCamera(cameraID string) bool {
	_, ok := d.cameras[cameraID]
	return ok
}

func effectiveBudget(budget int) int {
	if budget < 0 {
		return 0
	}
	return budget
}

func (d *SpectatorCameraDirector) selectLocked(now time.Time) CameraShot {
	if len(d.order) == 0 {
		return CameraShot{At: now, Timeline: now.Add(-d.cfg.Delay)}
	}

	if d.override != nil && !now.Before(d.override.until) {
		d.cooldownUntil = d.override.until.Add(d.cfg.OverrideCooldown)
		d.hasCooldown = true
		d.override = nil
		d.criticalPending = false
		d.criticalCamera = ""
		d.criticalTarget = ""
	}

	if d.override != nil {
		d.initialized = true
		d.currentID = d.override.cameraID
		shot := d.makeShotLocked(d.currentID, false, ReasonManual, now)
		d.currentShot = shot
		return shot
	}

	if d.hasCooldown {
		if now.Before(d.cooldownUntil) {
			shot := d.makeShotLocked(d.currentID, false, ReasonCooldown, now)
			d.currentShot = shot
			return shot
		}
		d.hasCooldown = false
	}

	if !d.initialized {
		return d.selectInitialLocked(now)
	}

	if d.hasLastSwitch && now.Before(d.lastSwitch.Add(d.cfg.MinDwell)) {
		shot := d.makeShotLocked(d.currentID, false, ReasonDwell, now)
		d.currentShot = shot
		return shot
	}

	bestID, bestScore, tied := d.bestCandidateLocked()
	currentScore := d.scores[d.currentID]
	eligible := bestID != "" && bestID != d.currentID && !tied && bestScore-currentScore >= d.cfg.Hysteresis
	budget := effectiveBudget(d.cfg.SwitchBudget)

	if d.switchCount >= budget && d.criticalPending && d.canForceCriticalLocked() {
		target := d.criticalCamera
		if target != "" && target != d.currentID {
			d.currentID = target
			d.switchCount++
			d.lastSwitch = now
			d.hasLastSwitch = true
			shot := d.makeShotLocked(target, true, ReasonCritical, now)
			if d.criticalTarget != "" {
				shot.Target = d.criticalTarget
			}
			d.currentShot = shot
			d.criticalPending = false
			d.criticalCamera = ""
			d.criticalTarget = ""
			return shot
		}
		d.criticalPending = false
		d.criticalCamera = ""
		d.criticalTarget = ""
	}

	if !eligible {
		shot := d.makeShotLocked(d.currentID, false, ReasonHysteresis, now)
		d.currentShot = shot
		return shot
	}

	if d.switchCount >= budget {
		shot := d.makeShotLocked(d.currentID, false, ReasonBudget, now)
		d.currentShot = shot
		return shot
	}

	d.currentID = bestID
	d.switchCount++
	d.lastSwitch = now
	d.hasLastSwitch = true
	d.criticalPending = false
	d.criticalCamera = ""
	d.criticalTarget = ""
	shot := d.makeShotLocked(bestID, true, ReasonSwitch, now)
	d.currentShot = shot
	return shot
}

func (d *SpectatorCameraDirector) selectInitialLocked(now time.Time) CameraShot {
	bestID, bestScore, _ := d.bestCandidateLocked()
	budget := effectiveBudget(d.cfg.SwitchBudget)

	if bestScore >= d.cfg.ScoreThreshold {
		if d.switchCount < budget {
			d.initialized = true
			d.currentID = bestID
			d.switchCount++
			d.lastSwitch = now
			d.hasLastSwitch = true
			d.criticalPending = false
			d.criticalCamera = ""
			d.criticalTarget = ""
			shot := d.makeShotLocked(bestID, true, ReasonSwitch, now)
			d.currentShot = shot
			return shot
		}

		if d.criticalPending && d.canForceCriticalLocked() && d.criticalCamera != "" {
			d.initialized = true
			d.currentID = d.criticalCamera
			d.switchCount++
			d.lastSwitch = now
			d.hasLastSwitch = true
			shot := d.makeShotLocked(d.criticalCamera, true, ReasonCritical, now)
			if d.criticalTarget != "" {
				shot.Target = d.criticalTarget
			}
			d.currentShot = shot
			d.criticalPending = false
			d.criticalCamera = ""
			d.criticalTarget = ""
			return shot
		}

		d.initialized = true
		d.currentID = d.order[0]
		shot := d.makeShotLocked(d.currentID, false, ReasonBudget, now)
		d.currentShot = shot
		return shot
	}

	d.initialized = true
	d.currentID = d.order[0]
	shot := d.makeShotLocked(d.currentID, false, ReasonIdle, now)
	d.currentShot = shot
	return shot
}

func (d *SpectatorCameraDirector) bestCandidateLocked() (string, int, bool) {
	bestID := ""
	bestScore := 0
	tied := false
	for _, cameraID := range d.order {
		score := d.scores[cameraID]
		switch {
		case bestID == "" || score > bestScore:
			bestID = cameraID
			bestScore = score
			tied = false
		case score == bestScore:
			tied = true
		}
	}
	return bestID, bestScore, tied
}

func (d *SpectatorCameraDirector) canForceCriticalLocked() bool {
	return d.switchCount <= effectiveBudget(d.cfg.SwitchBudget)
}

func (d *SpectatorCameraDirector) makeShotLocked(cameraID string, switched bool, reason string, now time.Time) CameraShot {
	return CameraShot{
		CameraID: cameraID,
		Target:   d.targets[cameraID],
		Score:    d.scores[cameraID],
		Switched: switched,
		Reason:   reason,
		At:       now,
		Timeline: now.Add(-d.cfg.Delay),
	}
}
