// Package task manages scene reverb presets: impulse response slots, tail
// crossfades between presets, freeze and block continuous processing.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// Sample rate of the processing chain, in hertz.
const (
	SampleRate = 48000
)

// Fade and block windows.
const (
	MaxFadeMs      = 500
	MaxBlockFrames = 1024
)

// Impulse response window.
const (
	MinTailFrames = 1
	MaxTailFrames = 96000
)

// Slot budget window.
const (
	MaxSlotBudget = 32
)

// TailSilenceThreshold is the absolute input level below which a frame counts
// as silence for the tail age.
const TailSilenceThreshold = 0.001

// Errors returned by the manager. A rejected call leaves slots, presets and
// processing state untouched.
var (
	ErrBlankPresetID  = errors.New("preset id is required")
	ErrDuplicatePreset = errors.New("preset already loaded")
	ErrUnknownPreset  = errors.New("unknown preset")
	ErrInvalidIR      = errors.New("impulse response out of range")
	ErrSlotBudget     = errors.New("impulse response slot budget reached")
	ErrBadFade        = errors.New("fade length out of range")
	ErrWetDryRange    = errors.New("wet and dry mix out of range")
	ErrFramesMismatch = errors.New("frame count does not match the sample buffer")
	ErrBlockRange     = errors.New("frame count out of range")
	ErrBadSlotBudget  = errors.New("slot budget out of range")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// ImpulseResponse is one reverb preset kernel: Decay is the per frame tail
// multiplier and TailFrames the nominal tail length.
type ImpulseResponse struct {
	Name       string  `json:"name"`
	Decay      float32 `json:"decay"`
	TailFrames int     `json:"tail_frames"`
}

// ReverbStats is the observable state of the manager.
type ReverbStats struct {
	Presets         int    `json:"presets"`
	Active          string `json:"active"`
	SlotBudget      int    `json:"slot_budget"`
	Frozen          bool   `json:"frozen"`
	Switches        int    `json:"switches"`
	ProcessedFrames int    `json:"processed_frames"`
}

// ReverbTailManager owns the preset slots and the two live processing chains.
type ReverbTailManager struct {
	slotBudget int
}

// NewReverbTailManager builds an empty manager with the given slot budget.
func NewReverbTailManager(slotBudget int) *ReverbTailManager {
	return &ReverbTailManager{slotBudget: slotBudget}
}

// LoadPreset stores an impulse response under id and takes one processing
// slot.
func (m *ReverbTailManager) LoadPreset(id string, ir ImpulseResponse) error {
	return errNotImplemented
}

// Preload fetches an impulse response ahead of time without occupying a
// processing slot.
func (m *ReverbTailManager) Preload(id string) error {
	return errNotImplemented
}

// ActivatePreset switches the active preset, crossfading the old tail into the
// new chain over fadeMs milliseconds.
func (m *ReverbTailManager) ActivatePreset(id string, fadeMs int) error {
	return errNotImplemented
}

// SetWetDry sets the wet and dry mix of one loaded preset.
func (m *ReverbTailManager) SetWetDry(id string, wet, dry float32) error {
	return errNotImplemented
}

// Process runs one block through the active chain and returns the mixed
// output.
func (m *ReverbTailManager) Process(in []float32, frames int) ([]float32, error) {
	return nil, errNotImplemented
}

// Freeze holds the current tail in place until it is released.
func (m *ReverbTailManager) Freeze(on bool) error {
	return errNotImplemented
}

// TailRemaining reports how many frames of tail are left on the active chain.
func (m *ReverbTailManager) TailRemaining() int {
	return 0
}

// SetSlotBudget changes the number of impulse response slots, evicting the
// least recently used preset that is not active.
func (m *ReverbTailManager) SetSlotBudget(n int) error {
	return errNotImplemented
}

// Stats reports slots, the active preset and the processed frame counter.
func (m *ReverbTailManager) Stats() ReverbStats {
	return ReverbStats{}
}

// Snapshot serialises slots, presets, both processing chains and the fade
// progress.
func (m *ReverbTailManager) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *ReverbTailManager) Restore(data []byte) error {
	return errNotImplemented
}
