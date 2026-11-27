// Package task resolves keyboard and gamepad bindings across the default,
// the user and the scene override layer.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Scope names one binding layer.
type Scope string

// Binding layers, ordered from the weakest to the strongest.
const (
	ScopeDefault Scope = "default"
	ScopeUser    Scope = "user"
	ScopeScene   Scope = "scene"
)

// DeviceID identifies a physical input device by serial number. The literal
// KeyboardDevice addresses the keyboard, every other value is a gamepad.
type DeviceID string

// KeyboardDevice is the device id of the keyboard.
const KeyboardDevice DeviceID = "keyboard"

// Conflict kinds reported by Validate.
const (
	KindDuplicateChord = "duplicate_chord"
	KindPrefixShadow   = "prefix_shadow"
)

// MaxBindingsPerScope is the hard cap of one binding layer.
const MaxBindingsPerScope = 32

// ReservedKeys cannot be bound by any layer.
var ReservedKeys = []string{"f10", "f11", "f12"}

// Sentinel errors returned by the resolver.
var (
	ErrEmptyAction   = errors.New("action id is required")
	ErrEmptyChord    = errors.New("chord needs at least one key")
	ErrBadChord      = errors.New("chord repeats a key")
	ErrBadScope      = errors.New("scope is not supported")
	ErrReservedKey   = errors.New("chord uses a reserved key")
	ErrConflict      = errors.New("binding layer has conflicts")
	ErrSceneActive   = errors.New("a scene layer is already active")
	ErrNoActiveScene = errors.New("no scene layer is active")
	ErrUnknownScene  = errors.New("scene layer is not active")
	ErrScopeLimit    = errors.New("binding layer reached its cap")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Chord is an ordered combination of keys or gamepad buttons.
type Chord struct {
	Keys []string
}

// InputEvent is one raw input sample of a device.
type InputEvent struct {
	Device DeviceID
	Keys   []string
}

// Binding is one action bound to one chord inside one layer.
type Binding struct {
	Device DeviceID
	Scope  Scope
	Action string
	Chord  Chord
}

// Conflict describes one blocking collision inside a layer.
type Conflict struct {
	Kind   string
	Action string
	Other  string
	Chord  string
}

// ConflictError carries every conflict that refused a commit.
type ConflictError struct {
	Conflicts []Conflict
}

// Error implements the error interface.
func (e *ConflictError) Error() string { return "binding layer has conflicts" }

// Unwrap exposes the sentinel error behind the conflict list.
func (e *ConflictError) Unwrap() error { return ErrConflict }

// BindingResolver owns the layers, the staged edits and the active scene.
type BindingResolver struct {
	defaults []Binding
}

// NewBindingResolver builds a resolver on top of the factory bindings.
func NewBindingResolver(defaults []Binding) *BindingResolver {
	return &BindingResolver{defaults: defaults}
}

// Bind stages one binding for every device in the given layer.
func (r *BindingResolver) Bind(scope Scope, action string, chord Chord) error {
	return errNotImplemented
}

// BindFor stages one binding for a single device serial.
func (r *BindingResolver) BindFor(device DeviceID, scope Scope, action string, chord Chord) error {
	return errNotImplemented
}

// Unbind stages the removal of one action in the given layer.
func (r *BindingResolver) Unbind(scope Scope, action string) error {
	return errNotImplemented
}

// UnbindFor stages the removal of one device scoped binding.
func (r *BindingResolver) UnbindFor(device DeviceID, scope Scope, action string) error {
	return errNotImplemented
}

// Resolve maps one input sample to the action that currently owns it.
func (r *BindingResolver) Resolve(device DeviceID, input InputEvent) (action string, ok bool, err error) {
	return "", false, errNotImplemented
}

// Validate reports the conflicts of the staged edits in one layer.
func (r *BindingResolver) Validate(scope Scope) []Conflict {
	return nil
}

// Commit applies the staged edits of one layer, all or nothing.
func (r *BindingResolver) Commit(scope Scope) error {
	return errNotImplemented
}

// PushScene opens a scene override layer.
func (r *BindingResolver) PushScene(sceneID string) error {
	return errNotImplemented
}

// PopScene closes the active scene layer and drops its bindings.
func (r *BindingResolver) PopScene(sceneID string) error {
	return errNotImplemented
}

// Bindings lists the committed bindings of one layer by action.
func (r *BindingResolver) Bindings(scope Scope) []Binding {
	return nil
}

// Snapshot serialises the layers, the staged edits and the active scene.
func (r *BindingResolver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *BindingResolver) Restore(data []byte) error {
	return errNotImplemented
}
