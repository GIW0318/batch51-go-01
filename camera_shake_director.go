// Package task mixes layered camera trauma: every shake source keeps its own
// trauma, priority and phase, the amplitude follows the squared trauma and the
// phase keeps running while a higher priority layer suppresses it.
package task

import (
	"errors"
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

// ShakeDirector owns the shake layers, the freeze clock and the camera mode.
type ShakeDirector struct {
	cfg ShakeConfig
}

// NewShakeDirector builds a director with the given config.
func NewShakeDirector(cfg ShakeConfig) *ShakeDirector {
	return &ShakeDirector{cfg: cfg}
}

// AddTrauma injects trauma into one source layer.
func (d *ShakeDirector) AddTrauma(source string, amount float64, priority int) error {
	return errors.New("not implemented")
}

// Sample mixes the layers into one offset for the given instant.
func (d *ShakeDirector) Sample(camera Camera, at time.Time) (Offset, error) {
	return Offset{}, errors.New("not implemented")
}

// SetPaused freezes or resumes the phase clock.
func (d *ShakeDirector) SetPaused(paused bool) {
}

// RemoveSource drops one shake layer.
func (d *ShakeDirector) RemoveSource(source string) error {
	return errors.New("not implemented")
}

// SetCameraMode switches the amplitude baseline between first and third person.
func (d *ShakeDirector) SetCameraMode(mode string) error {
	return errors.New("not implemented")
}

// Amplitude reports the current offset ceiling of one source.
func (d *ShakeDirector) Amplitude(source string) (float64, bool) {
	return 0, false
}

// Sources lists the registered shake layers, ascending.
func (d *ShakeDirector) Sources() []string {
	return nil
}

// Paused reports whether the phase clock is frozen.
func (d *ShakeDirector) Paused() bool {
	return false
}

// Decay bleeds trauma out of every layer.
func (d *ShakeDirector) Decay(dt time.Duration) error {
	return errors.New("not implemented")
}

// Snapshot serialises the layers, the mode, the freeze flag and the clock.
func (d *ShakeDirector) Snapshot() ([]byte, error) {
	return nil, errors.New("not implemented")
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (d *ShakeDirector) Restore(data []byte) error {
	return errors.New("not implemented")
}
