// Package task implements the client policy of network bound disk encryption:
// a volume binds to one Tang advertisement and thumbprint, unlocks only from
// configured servers whose advertisement still carries every pinned key, and
// keeps escrow as the fallback when no server answers.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Binding states.
const (
	BindingActive   = "active"
	BindingRotating = "rotating"
)

// Errors returned by the guard.
var (
	ErrBadVolume          = errors.New("volume name is required")
	ErrBadThumbprint      = errors.New("thumbprint is required")
	ErrBadAdvertisement   = errors.New("advertisement is malformed")
	ErrAdvKeyLimit        = errors.New("advertisement carries too many keys")
	ErrThumbprintMismatch = errors.New("advertisement thumbprint does not match the pin")
	ErrAlreadyBound       = errors.New("volume is already bound")
	ErrNotBound           = errors.New("volume has no binding")
	ErrBindingLimit       = errors.New("binding limit reached")
	ErrServerNotAllowed   = errors.New("server is not in the configured list")
	ErrDuplicateServer    = errors.New("server is already configured")
	ErrServerLimit        = errors.New("server limit reached")
	ErrServerUnreachable  = errors.New("server did not answer with an advertisement")
	ErrPinnedKeyMissing   = errors.New("advertisement lacks a pinned key")
	ErrNoEscrow           = errors.New("volume has no local escrow key")
	ErrBadEscrow          = errors.New("escrow key is required")
	ErrBadActor           = errors.New("rotation actor is required")
	ErrSameThumbprint     = errors.New("rotation reuses the current thumbprint")
	ErrNotRotating        = errors.New("volume is not rotating")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// UnlockConfig bounds the servers, keys and bindings of one guard.
type UnlockConfig struct {
	Servers      []string
	MaxKeys      int
	MaxServers   int
	MaxBindings  int
	RotateWindow time.Duration
}

// JWK is one key advertised by a Tang server.
type JWK struct {
	Thumbprint string
	Key        string
}

// Thumbprint pins one advertisement.
type Thumbprint string

// Advertisement is the key set served by one Tang server.
type Advertisement struct {
	Server     string
	Keys       []JWK
	Thumbprint string
}

// Binding is the per volume pin that survives a restart.
type Binding struct {
	Volume string
	Pin    Thumbprint
	State  string
	Keys   []string
	Seq    int64
}

// RotateRequest asks for a new pin while the old one is still usable.
type RotateRequest struct {
	Adv   Advertisement
	Pin   Thumbprint
	Actor string
}

// VolumeKey is the plaintext volume key recovered from the server.
type VolumeKey struct {
	Volume string
	Key    string
	Server string
}

// NbdeUnlockGuard owns advertisements, bindings and escrow keys.
type NbdeUnlockGuard struct {
	cfg UnlockConfig
}

// NewNbdeUnlockGuard builds a guard from the unlock configuration.
func NewNbdeUnlockGuard(cfg UnlockConfig) *NbdeUnlockGuard {
	return &NbdeUnlockGuard{cfg: cfg}
}

// AddServer appends one trusted Tang server to the configured list.
func (g *NbdeUnlockGuard) AddServer(server string) error {
	return errNotImplemented
}

// Servers lists the configured Tang servers, ordered as configured.
func (g *NbdeUnlockGuard) Servers() []string {
	return nil
}

// PublishAdv records the advertisement served by one configured server.
func (g *NbdeUnlockGuard) PublishAdv(server string, adv Advertisement) error {
	return errNotImplemented
}

// VerifyAdv validates the shape and the size of one advertisement.
func (g *NbdeUnlockGuard) VerifyAdv(adv Advertisement) error {
	return errNotImplemented
}

// Bind pins one volume to an advertisement and its thumbprint.
func (g *NbdeUnlockGuard) Bind(volume string, adv Advertisement, pin Thumbprint) (Binding, error) {
	return Binding{}, errNotImplemented
}

// Binding reads one volume binding.
func (g *NbdeUnlockGuard) Binding(volume string) (Binding, bool) {
	return Binding{}, false
}

// Unlock recovers the volume key from one configured server.
func (g *NbdeUnlockGuard) Unlock(volume, server string) (VolumeKey, error) {
	return VolumeKey{}, errNotImplemented
}

// Rotate opens a rekey window with a new advertisement and pin.
func (g *NbdeUnlockGuard) Rotate(volume string, req RotateRequest) error {
	return errNotImplemented
}

// FinishRotate closes the rekey window and adopts the new pin.
func (g *NbdeUnlockGuard) FinishRotate(volume string) error {
	return errNotImplemented
}

// SetEscrow stores the local escrow key of one volume.
func (g *NbdeUnlockGuard) SetEscrow(volume, key string) error {
	return errNotImplemented
}

// RevokeBinding drops the binding of one volume for good.
func (g *NbdeUnlockGuard) RevokeBinding(volume string) error {
	return errNotImplemented
}

// FallbackPassword returns the recovery key used when no server answers.
func (g *NbdeUnlockGuard) FallbackPassword(volume string) (string, error) {
	return "", errNotImplemented
}

// Snapshot serialises bindings, advertisements and escrow keys.
func (g *NbdeUnlockGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *NbdeUnlockGuard) Restore(data []byte) error {
	return errNotImplemented
}
