// Package task mixes layered camera trauma: every shake source keeps its own
// trauma, priority and phase, the amplitude follows the squared trauma and the
// phase keeps running while a higher priority layer suppresses it.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// Errors returned by the director.
var (
	ErrBadConfig     = errors.New("shake config is invalid")
	ErrBadSource     = errors.New("shake source id is empty")
	ErrBadAmount     = errors.New("trauma amount must be positive and finite")
	ErrBadPriority   = errors.New("shake priority must not be negative")
	ErrSourceLimit   = errors.New("shake source table is full")
	ErrUnknownSource = errors.New("shake source is not registered")
	ErrBadMode       = errors.New("camera mode must be first or third")
	ErrBadDuration   = errors.New("decay duration must not be negative")
	ErrBadTime       = errors.New("sample instant is before the previous sample")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Camera modes.
const (
	ModeFirstPerson = "first"
	ModeThirdPerson = "third"
)

const fullTurn = 2 * math.Pi

// Vec3 is a point in world space.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// Camera is the view the shake is layered onto.
type Camera struct {
	Pos      Vec3    `json:"pos"`
	Distance float64 `json:"distance"`
}

// Offset is one sampled camera shake.
type Offset struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Z    float64 `json:"z"`
	Roll float64 `json:"roll"`
}

// ShakeConfig tunes the amplitude, the decay and the layer weights.
type ShakeConfig struct {
	MaxAmplitude     float64 `json:"max_amplitude"`
	DecayPerSecond   float64 `json:"decay_per_second"`
	Frequency        float64 `json:"frequency"`
	SuppressedWeight float64 `json:"suppressed_weight"`
	FirstPersonScale float64 `json:"first_person_scale"`
	ThirdPersonScale float64 `json:"third_person_scale"`
	MaxSources       int     `json:"max_sources"`
}

// SourceState is one shake layer as seen from the outside.
type SourceState struct {
	Source   string  `json:"source"`
	Trauma   float64 `json:"trauma"`
	Priority int     `json:"priority"`
	Phase    float64 `json:"phase"`
}

type shakeLayer struct {
	// trauma is the part accumulated by completed frames. pendingQuadrature
	// holds the square sum of injections made since the last Sample/Decay call.
	// Keeping the two apart is what makes a burst compose by quadrature while
	// successive frames still accumulate linearly.
	trauma            float64
	pendingQuadrature float64
	priority          int
	phase             float64
}

// ShakeDirector owns the shake layers, the freeze clock and the camera mode.
type ShakeDirector struct {
	mu       sync.Mutex
	cfg      ShakeConfig
	cfgValid bool
	mode     string
	sources  map[string]*shakeLayer

	paused                bool
	sawPausedSample       bool
	resumeBaselinePending bool
	clock                 float64
	lastSample            time.Time
	hasLastSample         bool
}

// NewShakeDirector builds a director with the given config.
func NewShakeDirector(cfg ShakeConfig) *ShakeDirector {
	return &ShakeDirector{
		cfg:      cfg,
		cfgValid: validConfig(cfg),
		mode:     ModeThirdPerson,
		sources:  make(map[string]*shakeLayer),
	}
}

// AddTrauma injects trauma into one source layer. Injections made before the
// next Sample belong to the same frame and combine as a square sum.
func (d *ShakeDirector) AddTrauma(source string, amount float64, priority int) error {
	if !d.cfgValid {
		return ErrBadConfig
	}
	if strings.TrimSpace(source) == "" {
		return ErrBadSource
	}
	if !finitePositive(amount) {
		return ErrBadAmount
	}
	if priority < 0 {
		return ErrBadPriority
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.sources == nil {
		d.sources = make(map[string]*shakeLayer)
	}
	layer, ok := d.sources[source]
	if !ok {
		if len(d.sources) >= d.cfg.MaxSources {
			return ErrSourceLimit
		}
		layer = &shakeLayer{phase: phaseSeed(source)}
		d.sources[source] = layer
	}

	layer.priority = priority
	square := amount * amount
	if math.IsInf(square, 0) || square >= 1 || layer.pendingQuadrature+square >= 1 {
		layer.pendingQuadrature = 1
	} else {
		layer.pendingQuadrature += square
	}
	return nil
}

// Sample mixes the layers into one offset for the given instant.
func (d *ShakeDirector) Sample(camera Camera, at time.Time) (Offset, error) {
	if !d.cfgValid {
		return Offset{}, ErrBadConfig
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.hasLastSample && at.Before(d.lastSample) {
		return Offset{}, ErrBadTime
	}

	d.commitPendingLocked()

	delta := time.Duration(0)
	if d.hasLastSample {
		delta = at.Sub(d.lastSample)
	}
	switch {
	case d.paused:
		d.sawPausedSample = true
	case d.resumeBaselinePending:
		// There was no sample during the pause, so the only observable resume
		// boundary is this first sample. Absorb the pause without moving phase.
		d.resumeBaselinePending = false
	case delta > 0:
		d.advanceLocked(delta)
	}
	d.lastSample = at
	d.hasLastSample = true

	if d.sources == nil || len(d.sources) == 0 {
		return Offset{}, nil
	}

	names := d.sourceNamesLocked()
	highestPriority := -1
	for _, name := range names {
		layer := d.sources[name]
		if effectiveTrauma(layer) > 0 && layer.priority > highestPriority {
			highestPriority = layer.priority
		}
	}
	if highestPriority < 0 {
		return Offset{}, nil
	}

	scale := d.cameraScaleLocked()
	var mixed Offset
	for _, name := range names {
		layer := d.sources[name]
		trauma := effectiveTrauma(layer)
		if trauma <= 0 {
			continue
		}

		amplitude := trauma * trauma * scale
		if amplitude > d.cfg.MaxAmplitude {
			amplitude = d.cfg.MaxAmplitude
		}
		if layer.priority < highestPriority {
			amplitude *= d.cfg.SuppressedWeight
		}

		phase := normalizedPhase(layer.phase)
		mixed.X += amplitude * math.Sin(phase)
		mixed.Y += amplitude * math.Sin(phase*1.37+2.1)
		mixed.Z += amplitude * math.Cos(phase*0.91+4.2)
		mixed.Roll += amplitude * math.Sin(phase*0.57+1.7)
	}

	maxAmplitude := d.cfg.MaxAmplitude
	return Offset{
		X:    clampOffset(mixed.X, maxAmplitude),
		Y:    clampOffset(mixed.Y, maxAmplitude),
		Z:    clampOffset(mixed.Z, maxAmplitude),
		Roll: clampOffset(mixed.Roll, maxAmplitude),
	}, nil
}

// SetPaused freezes or resumes the phase clock.
func (d *ShakeDirector) SetPaused(paused bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if paused == d.paused {
		return
	}
	if paused {
		d.paused = true
		d.sawPausedSample = false
		d.resumeBaselinePending = false
		return
	}

	d.paused = false
	d.resumeBaselinePending = !d.sawPausedSample && d.hasLastSample
	d.sawPausedSample = false
}

// RemoveSource drops one shake layer.
func (d *ShakeDirector) RemoveSource(source string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.sources == nil {
		return ErrUnknownSource
	}
	if _, ok := d.sources[source]; !ok {
		return ErrUnknownSource
	}
	delete(d.sources, source)
	return nil
}

// SetCameraMode switches the amplitude baseline between first and third person.
func (d *ShakeDirector) SetCameraMode(mode string) error {
	if mode != ModeFirstPerson && mode != ModeThirdPerson {
		return ErrBadMode
	}

	d.mu.Lock()
	d.mode = mode
	d.mu.Unlock()
	return nil
}

// Amplitude reports the current offset ceiling of one source.
func (d *ShakeDirector) Amplitude(source string) (float64, bool) {
	if !d.cfgValid {
		return 0, false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	layer, ok := d.sources[source]
	if !ok {
		return 0, false
	}
	amplitude := effectiveTrauma(layer)
	amplitude = amplitude * amplitude * d.cameraScaleLocked()
	if amplitude > d.cfg.MaxAmplitude {
		amplitude = d.cfg.MaxAmplitude
	}
	return amplitude, true
}

// Sources lists the registered shake layers, ascending.
func (d *ShakeDirector) Sources() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sourceNamesLocked()
}

// Paused reports whether the phase clock is frozen.
func (d *ShakeDirector) Paused() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.paused
}

// Decay bleeds trauma out of every layer.
func (d *ShakeDirector) Decay(dt time.Duration) error {
	if dt < 0 {
		return ErrBadDuration
	}
	if !d.cfgValid {
		return ErrBadConfig
	}
	if dt == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.sources == nil {
		return nil
	}
	d.commitPendingLocked()
	drop := d.cfg.DecayPerSecond * dt.Seconds()
	for _, layer := range d.sources {
		if drop >= layer.trauma {
			layer.trauma = 0
			continue
		}
		layer.trauma -= drop
	}
	return nil
}

type snapshotPayload struct {
	Version int             `json:"version"`
	Mode    string          `json:"mode"`
	Paused  bool            `json:"paused"`
	Clock   float64         `json:"clock"`
	Sources []snapshotLayer `json:"sources"`

	LastSample            time.Time `json:"last_sample"`
	HasLastSample         bool      `json:"has_last_sample"`
	SawPausedSample       bool      `json:"saw_paused_sample"`
	ResumeBaselinePending bool      `json:"resume_baseline_pending"`
}

type snapshotLayer struct {
	Source            string  `json:"source"`
	Trauma            float64 `json:"trauma"`
	BaseTrauma        float64 `json:"base_trauma"`
	PendingQuadrature float64 `json:"pending_quadrature"`
	Priority          int     `json:"priority"`
	Phase             float64 `json:"phase"`
}

// Snapshot serialises the layers, the mode, the freeze flag and the clock.
func (d *ShakeDirector) Snapshot() ([]byte, error) {
	if !d.cfgValid {
		return nil, ErrBadConfig
	}

	d.mu.Lock()
	payload := snapshotPayload{
		Version:               1,
		Mode:                  d.mode,
		Paused:                d.paused,
		Clock:                 d.clock,
		LastSample:            d.lastSample,
		HasLastSample:         d.hasLastSample,
		SawPausedSample:       d.sawPausedSample,
		ResumeBaselinePending: d.resumeBaselinePending,
		Sources:               make([]snapshotLayer, 0, len(d.sources)),
	}
	for _, name := range d.sourceNamesLocked() {
		layer := d.sources[name]
		payload.Sources = append(payload.Sources, snapshotLayer{
			Source:            name,
			Trauma:            effectiveTrauma(layer),
			BaseTrauma:        layer.trauma,
			PendingQuadrature: layer.pendingQuadrature,
			Priority:          layer.priority,
			Phase:             layer.phase,
		})
	}
	d.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (d *ShakeDirector) Restore(data []byte) error {
	if !d.cfgValid {
		return ErrBadConfig
	}

	payload, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(payload, d.cfg); err != nil {
		return ErrBadSnapshot
	}

	sources := make(map[string]*shakeLayer, len(payload.Sources))
	for _, item := range payload.Sources {
		sources[item.Source] = &shakeLayer{
			trauma:            item.BaseTrauma,
			pendingQuadrature: item.PendingQuadrature,
			priority:          item.Priority,
			phase:             item.Phase,
		}
	}

	d.mu.Lock()
	d.sources = sources
	d.mode = payload.Mode
	d.paused = payload.Paused
	d.sawPausedSample = payload.SawPausedSample
	d.resumeBaselinePending = payload.ResumeBaselinePending
	d.clock = payload.Clock
	d.lastSample = payload.LastSample
	d.hasLastSample = payload.HasLastSample
	d.mu.Unlock()
	return nil
}

func (d *ShakeDirector) commitPendingLocked() {
	if d.sources == nil {
		return
	}
	for _, layer := range d.sources {
		layer.trauma = effectiveTrauma(layer)
		layer.pendingQuadrature = 0
	}
}

func (d *ShakeDirector) advanceLocked(delta time.Duration) {
	seconds := delta.Seconds()
	d.clock += seconds
	increment := seconds * d.cfg.Frequency * fullTurn
	for _, layer := range d.sources {
		layer.phase += increment
	}
}

func (d *ShakeDirector) cameraScaleLocked() float64 {
	if d.mode == ModeFirstPerson {
		return d.cfg.FirstPersonScale
	}
	return d.cfg.ThirdPersonScale
}

func (d *ShakeDirector) sourceNamesLocked() []string {
	names := make([]string, 0, len(d.sources))
	for name := range d.sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func effectiveTrauma(layer *shakeLayer) float64 {
	if layer == nil {
		return 0
	}
	value := layer.trauma
	if layer.pendingQuadrature > 0 {
		value += math.Sqrt(layer.pendingQuadrature)
	}
	if value > 1 {
		return 1
	}
	if value < 0 || math.IsNaN(value) {
		return 0
	}
	return value
}

func normalizedPhase(phase float64) float64 {
	if math.IsNaN(phase) || math.IsInf(phase, 0) {
		return 0
	}
	phase = math.Mod(phase, fullTurn)
	if phase < 0 {
		phase += fullTurn
	}
	return phase
}

func phaseSeed(source string) float64 {
	hash := uint64(14695981039346656037)
	for i := 0; i < len(source); i++ {
		hash ^= uint64(source[i])
		hash *= 1099511628211
	}
	return float64(hash>>11) / float64(uint64(1)<<53) * fullTurn
}

func clampOffset(value, limit float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	if value > limit {
		return limit
	}
	if value < -limit {
		return -limit
	}
	return value
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func validConfig(cfg ShakeConfig) bool {
	return finitePositive(cfg.MaxAmplitude) &&
		finitePositive(cfg.Frequency) &&
		finitePositive(cfg.FirstPersonScale) &&
		finitePositive(cfg.ThirdPersonScale) &&
		!math.IsNaN(cfg.DecayPerSecond) && !math.IsInf(cfg.DecayPerSecond, 0) && cfg.DecayPerSecond >= 0 &&
		!math.IsNaN(cfg.SuppressedWeight) && cfg.SuppressedWeight >= 0 && cfg.SuppressedWeight <= 1 &&
		cfg.MaxSources >= 1
}

func decodeSnapshot(data []byte) (*snapshotPayload, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, ErrBadSnapshot
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var payload snapshotPayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrBadSnapshot
	}
	return &payload, nil
}

func validateSnapshot(payload *snapshotPayload, cfg ShakeConfig) error {
	if payload == nil || payload.Version != 1 {
		return ErrBadSnapshot
	}
	if payload.Mode != ModeFirstPerson && payload.Mode != ModeThirdPerson {
		return ErrBadSnapshot
	}
	if payload.Clock < 0 || math.IsNaN(payload.Clock) || math.IsInf(payload.Clock, 0) {
		return ErrBadSnapshot
	}
	if len(payload.Sources) > cfg.MaxSources {
		return ErrBadSnapshot
	}
	if !payload.HasLastSample && !payload.LastSample.IsZero() {
		return ErrBadSnapshot
	}

	seen := make(map[string]struct{}, len(payload.Sources))
	for _, item := range payload.Sources {
		if strings.TrimSpace(item.Source) == "" {
			return ErrBadSnapshot
		}
		if _, ok := seen[item.Source]; ok {
			return ErrBadSnapshot
		}
		seen[item.Source] = struct{}{}
		if item.Priority < 0 ||
			item.Trauma < 0 || item.Trauma > 1 || math.IsNaN(item.Trauma) || math.IsInf(item.Trauma, 0) ||
			item.BaseTrauma < 0 || item.BaseTrauma > 1 || math.IsNaN(item.BaseTrauma) || math.IsInf(item.BaseTrauma, 0) ||
			item.PendingQuadrature < 0 || item.PendingQuadrature > 1 || math.IsNaN(item.PendingQuadrature) || math.IsInf(item.PendingQuadrature, 0) ||
			item.Phase < 0 || math.IsNaN(item.Phase) || math.IsInf(item.Phase, 0) {
			return ErrBadSnapshot
		}
		expected := item.BaseTrauma + math.Sqrt(item.PendingQuadrature)
		if expected > 1 {
			expected = 1
		}
		if math.Abs(expected-item.Trauma) > 1e-9 {
			return ErrBadSnapshot
		}
	}
	return nil
}
