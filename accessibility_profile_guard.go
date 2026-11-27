// Package task merges accessibility settings from three layers: the global
// defaults, the account preset and the per device override.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

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

	rawFields map[string]json.RawMessage
}

// UnmarshalJSON keeps unknown, forward-version fields byte-for-byte while
// still decoding known fields into their typed representation.
func (p *Profile) UnmarshalJSON(data []byte) error {
	var wire struct {
		Version int                        `json:"version"`
		Fields  map[string]json.RawMessage `json:"fields"`
		Locked  map[string]bool            `json:"locked"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	p.Version = wire.Version
	p.Locked = wire.Locked
	p.Fields = nil
	p.rawFields = nil
	if wire.Fields == nil {
		return nil
	}

	p.Fields = make(map[string]Value, len(wire.Fields))
	for field, raw := range wire.Fields {
		var value Value
		if err := json.Unmarshal(raw, &value); err != nil {
			// Unknown future fields may use a representation that this build
			// does not understand. Their raw JSON is retained below.
			value = Value{}
		}
		p.Fields[field] = value
		if !isKnownField(field) {
			if p.rawFields == nil {
				p.rawFields = make(map[string]json.RawMessage)
			}
			p.rawFields[field] = append(json.RawMessage(nil), raw...)
		}
	}
	return nil
}

// MarshalJSON restores the original JSON for unknown future fields so a read
// and write cycle does not erase data owned by a newer schema.
func (p Profile) MarshalJSON() ([]byte, error) {
	if len(p.rawFields) == 0 {
		type profileWire struct {
			Version int              `json:"version"`
			Fields  map[string]Value `json:"fields"`
			Locked  map[string]bool  `json:"locked"`
		}
		return json.Marshal(profileWire{Version: p.Version, Fields: p.Fields, Locked: p.Locked})
	}

	fields := make(map[string]json.RawMessage, len(p.Fields)+len(p.rawFields))
	for field, value := range p.Fields {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		fields[field] = raw
	}
	for field, raw := range p.rawFields {
		fields[field] = append(json.RawMessage(nil), raw...)
	}
	encodedFields, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version int             `json:"version"`
		Fields  json.RawMessage `json:"fields"`
		Locked  map[string]bool `json:"locked"`
	}{
		Version: p.Version,
		Fields:  encodedFields,
		Locked:  p.Locked,
	})
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
// overrides. Published maps are treated as immutable; every write installs a
// freshly cloned map while holding the write lock.
type AccessibilityProfile struct {
	mu       sync.RWMutex
	defaults Profile
	accounts map[string]Profile
	devices  map[string]Profile
}

// NewAccessibilityProfile builds the store on top of the global defaults.
func NewAccessibilityProfile(defaults Profile) *AccessibilityProfile {
	d := cloneProfile(defaults)
	d.Version = CurrentVersion
	markUnknownLocked(d)
	return &AccessibilityProfile{
		defaults: d,
		accounts: make(map[string]Profile),
		devices:  make(map[string]Profile),
	}
}

// Defaults returns a copy of the global defaults.
func (a *AccessibilityProfile) Defaults() Profile {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneProfile(a.defaults)
}

// Apply replaces the whole account preset in one step.
func (a *AccessibilityProfile) Apply(accountID string, p Profile) error {
	accountID = normalizeID(accountID)
	if accountID == "" {
		return ErrEmptyAccount
	}
	if p.Version != CurrentVersion {
		return ErrUnsupportedVersion
	}
	if err := validateProfileFields(p, ErrUnknownField); err != nil {
		return err
	}

	next := cloneProfile(p)
	next.Version = CurrentVersion
	markUnknownLocked(next)

	a.mu.Lock()
	defer a.mu.Unlock()

	// Read-only fields belong to a newer schema. They survive an account
	// replacement even when the caller's current-schema profile omits them.
	if old, ok := a.accounts[accountID]; ok {
		for field, locked := range old.Locked {
			if !locked {
				continue
			}
			value, exists := old.Fields[field]
			if !exists {
				continue
			}
			next.Fields[field] = cloneFieldValue(field, value)
			next.Locked[field] = true
			if raw, hasRaw := old.rawFields[field]; hasRaw {
				if next.rawFields == nil {
					next.rawFields = make(map[string]json.RawMessage)
				}
				next.rawFields[field] = append(json.RawMessage(nil), raw...)
			} else if next.rawFields != nil {
				delete(next.rawFields, field)
			}
		}
	}
	a.accounts[accountID] = next
	return nil
}

// Override merges device level settings into the device layer.
func (a *AccessibilityProfile) Override(deviceID string, fields map[string]Value) error {
	deviceID = normalizeID(deviceID)
	if deviceID == "" || len(fields) == 0 {
		return ErrBadValue
	}

	updates := make(map[string]Value, len(fields))
	for field, value := range fields {
		if !isKnownField(field) {
			return ErrUnknownField
		}
		if err := validateValue(field, value); err != nil {
			return err
		}
		updates[field] = cloneFieldValue(field, value)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	device := cloneProfile(a.devices[deviceID])
	device.Version = CurrentVersion
	for field, value := range updates {
		if device.Locked[field] {
			continue
		}
		device.Fields[field] = value
	}
	a.devices[deviceID] = device
	return nil
}

// ClearOverride drops one device level setting.
func (a *AccessibilityProfile) ClearOverride(deviceID, field string) error {
	if !isKnownField(field) {
		return ErrUnknownField
	}
	deviceID = normalizeID(deviceID)
	if deviceID == "" {
		return ErrBadValue
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	device, ok := a.devices[deviceID]
	if !ok {
		return nil
	}
	next := cloneProfile(device)
	delete(next.Fields, field)
	delete(next.Locked, field)
	delete(next.rawFields, field)
	a.devices[deviceID] = next
	return nil
}

// Resolve merges the three layers for one account and one device.
func (a *AccessibilityProfile) Resolve(accountID, deviceID string) (EffectiveProfile, error) {
	accountID = normalizeID(accountID)
	deviceID = normalizeID(deviceID)

	a.mu.RLock()
	defaults := cloneProfile(a.defaults)
	account, hasAccount := a.accounts[accountID]
	device, hasDevice := a.devices[deviceID]
	a.mu.RUnlock()

	effective := EffectiveProfile{
		AccountID: accountID,
		DeviceID:  deviceID,
		Fields:    make(map[string]Value),
		Sources:   make(map[string]string),
	}

	allFields := make(map[string]struct{})
	addFields(allFields, defaults.Fields)
	if hasAccount {
		addFields(allFields, account.Fields)
	}
	if hasDevice {
		addFields(allFields, device.Fields)
	}

	for field := range allFields {
		if hasDevice {
			if value, ok := device.Fields[field]; ok {
				effective.Fields[field] = cloneFieldValue(field, value)
				effective.Sources[field] = SourceDevice
				continue
			}
		}
		if hasAccount {
			if value, ok := account.Fields[field]; ok {
				effective.Fields[field] = cloneFieldValue(field, value)
				effective.Sources[field] = SourceAccount
				continue
			}
		}
		if value, ok := defaults.Fields[field]; ok {
			effective.Fields[field] = cloneFieldValue(field, value)
			effective.Sources[field] = SourceDefault
		}
	}

	effective.Conflict = applyMutexRules(effective.Fields)
	return effective, nil
}

// Migrate converts a stored payload up the version chain.
func (a *AccessibilityProfile) Migrate(raw []byte, fromVersion int) (Profile, error) {
	if fromVersion < 1 {
		return Profile{}, ErrUnsupportedVersion
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return Profile{}, ErrBadPayload
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return Profile{}, ErrBadPayload
	}
	if rawVersion, ok := root["version"]; ok {
		if isJSONNull(rawVersion) {
			return Profile{}, ErrBadPayload
		}
		var declaredVersion int
		if err := json.Unmarshal(rawVersion, &declaredVersion); err != nil || declaredVersion < 1 {
			return Profile{}, ErrBadPayload
		}
	}
	rawFields, ok := root["fields"]
	if !ok {
		return Profile{}, ErrBadPayload
	}

	var fieldPayload map[string]json.RawMessage
	if err := json.Unmarshal(rawFields, &fieldPayload); err != nil || fieldPayload == nil {
		if fieldPayload == nil && !bytes.Equal(bytes.TrimSpace(rawFields), []byte("{}")) {
			return Profile{}, ErrBadPayload
		}
		fieldPayload = make(map[string]json.RawMessage)
	}
	fields := make(map[string]Value, len(fieldPayload))
	futureRaw := make(map[string]json.RawMessage)
	for field, rawValue := range fieldPayload {
		var value Value
		if err := json.Unmarshal(rawValue, &value); err != nil {
			value = Value{}
		}
		fields[field] = value
		if !isKnownField(field) {
			futureRaw[field] = append(json.RawMessage(nil), rawValue...)
		}
	}

	locked := make(map[string]bool)
	if rawLocked, ok := root["locked"]; ok {
		if err := json.Unmarshal(rawLocked, &locked); err != nil {
			return Profile{}, ErrBadPayload
		}
		if locked == nil {
			locked = make(map[string]bool)
		}
	}

	for version := fromVersion; version < CurrentVersion; version++ {
		switch version {
		case 1:
			renameField(fields, locked, futureRaw, "text_size", FieldSubtitleSize)
			renameField(fields, locked, futureRaw, "contrast", FieldHighContrast)
		case 2:
			value, exists := fields["motion"]
			if exists {
				if value.Kind != KindBool {
					return Profile{}, ErrBadValue
				}
				fields[FieldReduceMotion] = BoolValue(!value.Bool)
				if locked["motion"] {
					locked[FieldReduceMotion] = true
				}
				delete(fields, "motion")
				delete(locked, "motion")
				delete(futureRaw, "motion")
			}
		}
	}

	for field := range futureRaw {
		if isKnownField(field) {
			delete(futureRaw, field)
		}
	}
	markUnknownLocked(Profile{Fields: fields, Locked: locked})
	for field, value := range fields {
		if !isKnownField(field) {
			continue
		}
		if err := validateValue(field, value); err != nil {
			return Profile{}, err
		}
	}
	return Profile{
		Version:   CurrentVersion,
		Fields:    fields,
		Locked:    locked,
		rawFields: futureRaw,
	}, nil
}

// Save serialises the stored preset of one account.
func (a *AccessibilityProfile) Save(accountID string) ([]byte, error) {
	accountID = normalizeID(accountID)
	if accountID == "" {
		return nil, ErrEmptyAccount
	}

	a.mu.RLock()
	profile, ok := a.accounts[accountID]
	a.mu.RUnlock()
	if !ok {
		profile = Profile{Version: CurrentVersion}
	}
	profile = cloneProfile(profile)
	profile.Version = CurrentVersion
	markUnknownLocked(profile)
	return json.Marshal(profile)
}

// Reset drops the account preset.
func (a *AccessibilityProfile) Reset(accountID string) error {
	accountID = normalizeID(accountID)
	if accountID == "" {
		return ErrEmptyAccount
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.accounts, accountID)
	return nil
}

type snapshot struct {
	Version  int                `json:"version"`
	Defaults Profile            `json:"defaults"`
	Accounts map[string]Profile `json:"accounts"`
	Devices  map[string]Profile `json:"devices"`
}

// Snapshot serialises the defaults, the presets and the device overrides.
func (a *AccessibilityProfile) Snapshot() ([]byte, error) {
	a.mu.RLock()
	snap := snapshot{
		Version:  CurrentVersion,
		Defaults: cloneProfile(a.defaults),
		Accounts: cloneProfiles(a.accounts),
		Devices:  cloneProfiles(a.devices),
	}
	a.mu.RUnlock()
	return json.Marshal(snap)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *AccessibilityProfile) Restore(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return ErrBadSnapshot
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return ErrBadSnapshot
	}
	versionRaw, ok := root["version"]
	if !ok || isJSONNull(versionRaw) {
		return ErrBadSnapshot
	}
	var version int
	if err := json.Unmarshal(versionRaw, &version); err != nil || version < 1 {
		return ErrBadSnapshot
	}

	defaultsRaw, ok := root["defaults"]
	if !ok || isJSONNull(defaultsRaw) {
		return ErrBadSnapshot
	}
	var defaults Profile
	if err := json.Unmarshal(defaultsRaw, &defaults); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshotProfile(defaults); err != nil {
		return ErrBadSnapshot
	}

	accountsRaw, ok := root["accounts"]
	if !ok || isJSONNull(accountsRaw) {
		return ErrBadSnapshot
	}
	accounts, err := decodeSnapshotProfiles(accountsRaw)
	if err != nil {
		return ErrBadSnapshot
	}

	devicesRaw, ok := root["devices"]
	if !ok || isJSONNull(devicesRaw) {
		return ErrBadSnapshot
	}
	devices, err := decodeSnapshotProfiles(devicesRaw)
	if err != nil {
		return ErrBadSnapshot
	}

	defaults = cloneProfile(defaults)
	defaults.Version = CurrentVersion
	markUnknownLocked(defaults)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.defaults = defaults
	a.accounts = accounts
	a.devices = devices
	return nil
}

func decodeSnapshotProfiles(raw json.RawMessage) (map[string]Profile, error) {
	var stored map[string]Profile
	if err := json.Unmarshal(raw, &stored); err != nil || stored == nil {
		return nil, ErrBadSnapshot
	}

	result := make(map[string]Profile, len(stored))
	for id, profile := range stored {
		id = normalizeID(id)
		if id == "" {
			return nil, ErrBadSnapshot
		}
		if _, duplicate := result[id]; duplicate {
			return nil, ErrBadSnapshot
		}
		if err := validateSnapshotProfile(profile); err != nil {
			return nil, ErrBadSnapshot
		}
		profile = cloneProfile(profile)
		profile.Version = CurrentVersion
		markUnknownLocked(profile)
		result[id] = profile
	}
	return result, nil
}

func validateSnapshotProfile(profile Profile) error {
	if profile.Version < 1 || profile.Fields == nil || profile.Locked == nil {
		return ErrBadSnapshot
	}
	return validateProfileFields(profile, ErrBadSnapshot)
}

func validateProfileFields(profile Profile, unknownErr error) error {
	for field, value := range profile.Fields {
		if strings.TrimSpace(field) == "" {
			return unknownErr
		}
		if !isKnownField(field) {
			if !profile.Locked[field] {
				return unknownErr
			}
			continue
		}
		if err := validateValue(field, value); err != nil {
			return err
		}
	}
	return nil
}

func validateValue(field string, value Value) error {
	switch field {
	case FieldSubtitleSize, FieldHUDScale:
		if value.Kind != KindInt {
			return ErrBadValue
		}
		if field == FieldSubtitleSize && (value.Int < 100 || value.Int > 250) {
			return ErrOutOfRange
		}
		if field == FieldHUDScale && (value.Int < 50 || value.Int > 200) {
			return ErrOutOfRange
		}
	case FieldHighContrast, FieldReduceMotion, FieldScreenShake, FieldHoldToConfirm:
		if value.Kind != KindBool {
			return ErrBadValue
		}
	case FieldColorBlind:
		if value.Kind != KindText {
			return ErrBadValue
		}
	default:
		return ErrUnknownField
	}
	return nil
}

func applyMutexRules(fields map[string]Value) []string {
	var conflicts []string

	if reduceMotion, ok := fields[FieldReduceMotion]; ok && reduceMotion.Kind == KindBool && reduceMotion.Bool {
		if screenShake, ok := fields[FieldScreenShake]; ok && screenShake.Kind == KindBool && screenShake.Bool {
			fields[FieldScreenShake] = BoolValue(false)
			conflicts = append(conflicts, FieldScreenShake)
		}
	}

	if colorBlind, ok := fields[FieldColorBlind]; ok && colorBlind.Kind == KindText && colorBlind.Str != ColorBlindNone {
		if highContrast, ok := fields[FieldHighContrast]; ok && highContrast.Kind == KindBool && highContrast.Bool {
			fields[FieldHighContrast] = BoolValue(false)
			conflicts = append(conflicts, FieldHighContrast)
		}
	}

	sort.Strings(conflicts)
	return conflicts
}

func renameField(fields map[string]Value, locked map[string]bool, rawFields map[string]json.RawMessage, oldName, newName string) {
	value, exists := fields[oldName]
	if !exists {
		return
	}
	if _, alreadyExists := fields[newName]; !alreadyExists {
		fields[newName] = value
	}
	if locked[oldName] {
		locked[newName] = true
	}
	delete(fields, oldName)
	delete(locked, oldName)
	delete(rawFields, oldName)
}

func markUnknownLocked(profile Profile) {
	if profile.Locked == nil {
		return
	}
	for field := range profile.Fields {
		if !isKnownField(field) {
			profile.Locked[field] = true
		}
	}
}

func isKnownField(field string) bool {
	switch field {
	case FieldSubtitleSize, FieldHighContrast, FieldReduceMotion,
		FieldScreenShake, FieldColorBlind, FieldHUDScale, FieldHoldToConfirm:
		return true
	default:
		return false
	}
}

func addFields(dst map[string]struct{}, fields map[string]Value) {
	for field := range fields {
		dst[field] = struct{}{}
	}
}

func cloneProfile(profile Profile) Profile {
	fields := make(map[string]Value, len(profile.Fields))
	for field, value := range profile.Fields {
		fields[field] = cloneFieldValue(field, value)
	}
	locked := make(map[string]bool, len(profile.Locked))
	for field, value := range profile.Locked {
		locked[field] = value
	}
	var rawFields map[string]json.RawMessage
	if profile.rawFields != nil {
		rawFields = make(map[string]json.RawMessage, len(profile.rawFields))
		for field, raw := range profile.rawFields {
			rawFields[field] = append(json.RawMessage(nil), raw...)
		}
	}
	return Profile{Version: profile.Version, Fields: fields, Locked: locked, rawFields: rawFields}
}

func cloneValue(value Value) Value {
	return value
}

func cloneFieldValue(_ string, value Value) Value {
	return value
}

func cloneProfiles(profiles map[string]Profile) map[string]Profile {
	result := make(map[string]Profile, len(profiles))
	for id, profile := range profiles {
		result[id] = cloneProfile(profile)
	}
	return result
}

func normalizeID(id string) string {
	return strings.TrimSpace(id)
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
