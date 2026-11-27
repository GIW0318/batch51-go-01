package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Device states reported by Enable, State and Devices.
const (
	SwapActive    = "active"
	SwapSuspended = "suspended"
	SwapLocked    = "locked"
	SwapExpired   = "expired"
)

// Hibernate actions a per device policy can request.
const (
	HibernateDiscard = "discard"
	HibernateRetain  = "retain"
)

// Guard errors.
var (
	ErrInvalidDevice  = errors.New("device name is required")
	ErrUnknownDevice  = errors.New("unknown swap device")
	ErrDeviceLimit    = errors.New("device limit reached")
	ErrDeviceReplaced = errors.New("device identity changed for an enabled swap device")
	ErrUnknownVersion = errors.New("swap metadata version is not understood")
	ErrUnknownAction  = errors.New("unknown hibernate action")
	ErrNotActive      = errors.New("swap is not in the active state")
	ErrNotLocked      = errors.New("swap device is not locked")
	ErrBusy           = errors.New("swap devices must be suspended first")
	ErrKeyExpired     = errors.New("volume key has outlived its residency budget")
	ErrLocked         = errors.New("volume key was retained and must be unlocked first")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// SupportedVersions lists the swap header metadata versions the guard writes
// and accepts.
var SupportedVersions = []int{1, 2}

// DevicePolicy is the per device policy supplied when a swap device is enabled.
type DevicePolicy struct {
	DeviceID        string `json:"device_id"`
	Version         int    `json:"version"`
	KDF             string `json:"kdf"`
	HibernateAction string `json:"hibernate_action"`
}

// SwapState is the observable state of one encrypted swap device.
type SwapState struct {
	Device      string `json:"device"`
	DeviceID    string `json:"device_id"`
	State       string `json:"state"`
	KeyEpoch    int    `json:"key_epoch"`
	KeyHandle   string `json:"key_handle"`
	Generation  int    `json:"generation"`
	MetaCopies  int    `json:"meta_copies"`
	EnabledAt   int64  `json:"enabled_at"`
	SuspendedAt int64  `json:"suspended_at"`
}

// SwapPolicy bounds one guard.
type SwapPolicy struct {
	MaxDevices            int
	MaxKeyLifetimeSeconds int64
	MaxMetaCopies         int
	Now                   func() int64
	Probe                 func(dev string) error
}

// SwapKeyGuard owns the in memory volume keys and the device states.
type SwapKeyGuard struct {
	cfg SwapPolicy
}

// NewSwapKeyGuard builds a guard with the given policy.
func NewSwapKeyGuard(cfg SwapPolicy) *SwapKeyGuard {
	return &SwapKeyGuard{cfg: cfg}
}

// Enable binds a fresh in memory volume key to one swap device.
func (g *SwapKeyGuard) Enable(dev string, policy DevicePolicy) (SwapState, error) {
	return SwapState{}, errNotImplemented
}

// Suspend freezes the swap devices while keeping their keys resident.
func (g *SwapKeyGuard) Suspend() error {
	return errNotImplemented
}

// Resume unfreezes the swap devices after a suspend.
func (g *SwapKeyGuard) Resume() error {
	return errNotImplemented
}

// Hibernate prepares the devices for a hibernate and either discards or
// retains the volume keys according to the per device policy.
func (g *SwapKeyGuard) Hibernate(prepare bool) error {
	return errNotImplemented
}

// Unlock releases a device whose key was retained across a hibernate.
func (g *SwapKeyGuard) Unlock(dev string) error {
	return errNotImplemented
}

// Disable drops one device and wipes its key.
func (g *SwapKeyGuard) Disable(dev string) error {
	return errNotImplemented
}

// State reads one device record.
func (g *SwapKeyGuard) State(dev string) (SwapState, bool) {
	return SwapState{}, false
}

// Devices lists the managed devices ordered by device name.
func (g *SwapKeyGuard) Devices() []SwapState {
	return nil
}

// KeyEpoch reports how many keys were generated for one device.
func (g *SwapKeyGuard) KeyEpoch(dev string) (int, error) {
	return 0, errNotImplemented
}

// Snapshot serialises the device table without any key material.
func (g *SwapKeyGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *SwapKeyGuard) Restore(data []byte) error {
	return errNotImplemented
}
