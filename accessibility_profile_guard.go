// Package task merges accessibility settings from three layers: the global
// defaults, the account preset and the per device override.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// CurrentVersion is the profile schema version this build writes.
const CurrentVersion = 3

// Value kinds understood by the profile.
const (
	KindBool = "bool"
	KindInt  = "int"
	KindText = "str"
)

// Layer names reported by Resolve.
const (
	SourceDefault = "default"
	SourceAccount = "account"
	SourceDevice  = "device"
)

// Field names known to the profile.
const (
	FieldSubtitleSize  = "subtitle_size"
	FieldHighContrast  = "high_contrast"
	FieldReduceMotion  = "reduce_motion"
	FieldScreenShake   = "screen_shake"
	FieldColorBlind    = "color_blind_mode"
	FieldHUDScale      = "hud_scale"
	FieldHoldToConfirm = "hold_to_confirm"
)

// KnownFields lists every field the profile understands.
var KnownFields = []string{
	FieldSubtitleSize,
	FieldHighContrast,
	FieldReduceMotion,
	FieldScreenShake,
	FieldColorBlind,
	FieldHUDScale,
	FieldHoldToConfirm,
}

// Text values accepted by color_blind_mode.
const (
	ColorBlindNone         = "none"
	ColorBlindProtanopia   = "protanopia"
	ColorBlindDeuteranopia = "deuteranopia"
	ColorBlindTritanopia   = "tritanopia"
)

// Sentinel errors returned by the profile store.
var (
	ErrEmptyAccount       = errors.New("account id is required")
	ErrUnknownField       = errors.New("field is not part of the profile")
	ErrBadValue           = errors.New("field value is malformed")
	ErrOutOfRange         = errors.New("field value is out of range")
	ErrUnsupportedVersion = errors.New("profile version is not supported")
	ErrBadPayload         = errors.New("profile payload is invalid")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// Value is one typed setting. Kind selects which member carries the data.
type Value struct {
	Kind string `json:"kind"`
	Bool bool   `json:"bool,omitempty"`
	Int  int64  `json:"int,omitempty"`
	Str  string `json:"str,omitempty"`
}

// BoolValue builds a boolean setting.
func BoolValue(v bool) Value { return Value{Kind: KindBool, Bool: v} }

// IntValue builds an integer setting.
func IntValue(v int64) Value { return Value{Kind: KindInt, Int: v} }

// TextValue builds a text setting.
func TextValue(v string) Value { return Value{Kind: KindText, Str: v} }

// Profile is one stored layer of accessibility settings.
type Profile struct {
	Version int              `json:"version"`
	Fields  map[string]Value `json:"fields"`
	Locked  map[string]bool  `json:"locked"`
}

// EffectiveProfile is the merged view a client renders.
type EffectiveProfile struct {
	AccountID string
	DeviceID  string
	Fields    map[string]Value
	Sources   map[string]string
	Conflict  []string
}

// AccessibilityProfile owns the defaults, the account presets and the device
// overrides.
type AccessibilityProfile struct {
	defaults Profile
}

// NewAccessibilityProfile builds the store on top of the global defaults.
func NewAccessibilityProfile(defaults Profile) *AccessibilityProfile {
	return &AccessibilityProfile{defaults: defaults}
}

// Defaults returns a copy of the global defaults.
func (a *AccessibilityProfile) Defaults() Profile {
	return Profile{}
}

// Apply replaces the whole account preset in one step.
func (a *AccessibilityProfile) Apply(accountID string, p Profile) error {
	return errNotImplemented
}

// Override merges device level settings into the device layer.
func (a *AccessibilityProfile) Override(deviceID string, fields map[string]Value) error {
	return errNotImplemented
}

// ClearOverride drops one device level setting.
func (a *AccessibilityProfile) ClearOverride(deviceID, field string) error {
	return errNotImplemented
}

// Resolve merges the three layers for one account and one device.
func (a *AccessibilityProfile) Resolve(accountID, deviceID string) (EffectiveProfile, error) {
	return EffectiveProfile{}, errNotImplemented
}

// Migrate converts a stored payload up the version chain.
func (a *AccessibilityProfile) Migrate(raw []byte, fromVersion int) (Profile, error) {
	return Profile{}, errNotImplemented
}

// Save serialises the stored preset of one account.
func (a *AccessibilityProfile) Save(accountID string) ([]byte, error) {
	return nil, errNotImplemented
}

// Reset drops the account preset.
func (a *AccessibilityProfile) Reset(accountID string) error {
	return errNotImplemented
}

// Snapshot serialises the defaults, the presets and the device overrides.
func (a *AccessibilityProfile) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *AccessibilityProfile) Restore(data []byte) error {
	return errNotImplemented
}
