package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-a-09"
)

func defaultProfile() impl.Profile {
	return impl.Profile{
		Version: impl.CurrentVersion,
		Fields: map[string]impl.Value{
			impl.FieldSubtitleSize:  impl.IntValue(140),
			impl.FieldHighContrast:  impl.BoolValue(false),
			impl.FieldReduceMotion:  impl.BoolValue(false),
			impl.FieldScreenShake:   impl.BoolValue(true),
			impl.FieldColorBlind:    impl.TextValue(impl.ColorBlindNone),
			impl.FieldHUDScale:      impl.IntValue(100),
			impl.FieldHoldToConfirm: impl.BoolValue(false),
		},
	}
}

func accountProfile(fields map[string]impl.Value) impl.Profile {
	return impl.Profile{Version: impl.CurrentVersion, Fields: fields}
}

func newProfileStore() *impl.AccessibilityProfile {
	return impl.NewAccessibilityProfile(defaultProfile())
}

func TestNewProfileExposesDefaults(t *testing.T) {
	store := newProfileStore()
	defaults := store.Defaults()
	if defaults.Version != impl.CurrentVersion {
		t.Fatalf("defaults version = %d, want %d", defaults.Version, impl.CurrentVersion)
	}
	if len(defaults.Fields) != len(impl.KnownFields) {
		t.Fatalf("default fields = %d, want %d", len(defaults.Fields), len(impl.KnownFields))
	}
	if got := defaults.Fields[impl.FieldSubtitleSize]; got.Kind != impl.KindInt || got.Int != 140 {
		t.Fatalf("default subtitle size = %+v, want int 140", got)
	}
	if got := defaults.Fields[impl.FieldScreenShake]; got.Kind != impl.KindBool || !got.Bool {
		t.Fatalf("default screen shake = %+v, want bool true", got)
	}
}

func TestResolveFallsBackToDefaults(t *testing.T) {
	store := newProfileStore()
	eff, err := store.Resolve("", "")
	if err != nil {
		t.Fatalf("resolve without layers: %v", err)
	}
	if got := eff.Fields[impl.FieldSubtitleSize]; got.Int != 140 {
		t.Fatalf("resolved subtitle size = %d, want default 140", got.Int)
	}
	if got := eff.Sources[impl.FieldSubtitleSize]; got != impl.SourceDefault {
		t.Fatalf("source of subtitle size = %q, want %q", got, impl.SourceDefault)
	}
	if got := eff.Fields[impl.FieldColorBlind].Str; got != impl.ColorBlindNone {
		t.Fatalf("resolved color blind mode = %q, want %q", got, impl.ColorBlindNone)
	}
	if len(eff.Conflict) != 0 {
		t.Fatalf("defaults must not report forced conflicts, got %v", eff.Conflict)
	}
}

func TestApplyRejectsUnknownField(t *testing.T) {
	store := newProfileStore()
	if err := store.Apply("  ", accountProfile(map[string]impl.Value{impl.FieldHUDScale: impl.IntValue(120)})); !errors.Is(err, impl.ErrEmptyAccount) {
		t.Fatalf("blank account must yield ErrEmptyAccount, got %v", err)
	}
	if err := store.Apply("acct-1", accountProfile(map[string]impl.Value{"font_family": impl.TextValue("serif")})); !errors.Is(err, impl.ErrUnknownField) {
		t.Fatalf("unknown field must yield ErrUnknownField, got %v", err)
	}
	stale := impl.Profile{Version: impl.CurrentVersion - 1, Fields: map[string]impl.Value{impl.FieldHUDScale: impl.IntValue(120)}}
	if err := store.Apply("acct-1", stale); !errors.Is(err, impl.ErrUnsupportedVersion) {
		t.Fatalf("stale version must yield ErrUnsupportedVersion, got %v", err)
	}
	eff, err := store.Resolve("acct-1", "")
	if err != nil {
		t.Fatalf("resolve after rejected applies: %v", err)
	}
	if got := eff.Sources[impl.FieldHUDScale]; got != impl.SourceDefault {
		t.Fatalf("rejected apply changed the hud scale source to %q, want %q", got, impl.SourceDefault)
	}
}

func TestApplyStoresAccountPreset(t *testing.T) {
	store := newProfileStore()
	if err := store.Apply("acct-2", accountProfile(map[string]impl.Value{impl.FieldHighContrast: impl.BoolValue(true)})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	eff, err := store.Resolve("acct-2", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !eff.Fields[impl.FieldHighContrast].Bool {
		t.Fatalf("resolved high contrast = %+v, want true", eff.Fields[impl.FieldHighContrast])
	}
	if got := eff.Sources[impl.FieldHighContrast]; got != impl.SourceAccount {
		t.Fatalf("source of high contrast = %q, want %q", got, impl.SourceAccount)
	}
	if got := eff.Fields[impl.FieldSubtitleSize].Int; got != 140 {
		t.Fatalf("untouched subtitle size = %d, want default 140", got)
	}
	if got := eff.Sources[impl.FieldSubtitleSize]; got != impl.SourceDefault {
		t.Fatalf("source of untouched subtitle size = %q, want %q", got, impl.SourceDefault)
	}
}

func TestOverrideRejectsBadInput(t *testing.T) {
	store := newProfileStore()
	if err := store.Override("dev-1", map[string]impl.Value{"font_family": impl.TextValue("serif")}); !errors.Is(err, impl.ErrUnknownField) {
		t.Fatalf("unknown field must yield ErrUnknownField, got %v", err)
	}
	if err := store.Override("dev-1", map[string]impl.Value{}); !errors.Is(err, impl.ErrBadValue) {
		t.Fatalf("empty override must yield ErrBadValue, got %v", err)
	}
	if err := store.Override("dev-1", map[string]impl.Value{impl.FieldSubtitleSize: {Kind: "float"}}); !errors.Is(err, impl.ErrBadValue) {
		t.Fatalf("unsupported kind must yield ErrBadValue, got %v", err)
	}
	if err := store.Override("   ", map[string]impl.Value{impl.FieldSubtitleSize: impl.IntValue(160)}); !errors.Is(err, impl.ErrBadValue) {
		t.Fatalf("blank device must yield ErrBadValue, got %v", err)
	}
}

func TestOverrideDeviceLayerWins(t *testing.T) {
	store := newProfileStore()
	if err := store.Apply("acct-3", accountProfile(map[string]impl.Value{impl.FieldSubtitleSize: impl.IntValue(180)})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := store.Override("dev-3", map[string]impl.Value{impl.FieldSubtitleSize: impl.IntValue(200)}); err != nil {
		t.Fatalf("override: %v", err)
	}
	eff, err := store.Resolve("acct-3", "dev-3")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := eff.Fields[impl.FieldSubtitleSize].Int; got != 200 {
		t.Fatalf("resolved subtitle size = %d, want the device value 200", got)
	}
	if got := eff.Sources[impl.FieldSubtitleSize]; got != impl.SourceDevice {
		t.Fatalf("source of subtitle size = %q, want %q", got, impl.SourceDevice)
	}
}

func TestClearOverrideRestoresAccountLayer(t *testing.T) {
	store := newProfileStore()
	if err := store.Apply("acct-4", accountProfile(map[string]impl.Value{impl.FieldSubtitleSize: impl.IntValue(180)})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := store.Override("dev-4", map[string]impl.Value{impl.FieldSubtitleSize: impl.IntValue(200)}); err != nil {
		t.Fatalf("override: %v", err)
	}
	if err := store.ClearOverride("dev-4", impl.FieldSubtitleSize); err != nil {
		t.Fatalf("clear override: %v", err)
	}
	eff, err := store.Resolve("acct-4", "dev-4")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := eff.Fields[impl.FieldSubtitleSize].Int; got != 180 {
		t.Fatalf("subtitle size after clearing the override = %d, want the account value 180", got)
	}
	if got := eff.Sources[impl.FieldSubtitleSize]; got != impl.SourceAccount {
		t.Fatalf("source after clearing the override = %q, want %q", got, impl.SourceAccount)
	}
	if err := store.ClearOverride("dev-4", "font_family"); !errors.Is(err, impl.ErrUnknownField) {
		t.Fatalf("clearing an unknown field must yield ErrUnknownField, got %v", err)
	}
}

func TestResolveForcedConflictScreenShake(t *testing.T) {
	store := newProfileStore()
	if err := store.Apply("acct-5", accountProfile(map[string]impl.Value{
		impl.FieldReduceMotion: impl.BoolValue(true),
		impl.FieldScreenShake:  impl.BoolValue(true),
	})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	eff, err := store.Resolve("acct-5", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if eff.Fields[impl.FieldReduceMotion].Bool != true {
		t.Fatalf("reduce motion must stay enabled, got %+v", eff.Fields[impl.FieldReduceMotion])
	}
	if eff.Fields[impl.FieldScreenShake].Bool {
		t.Fatalf("screen shake must be forced off while reduce motion is on")
	}
	if len(eff.Conflict) != 1 || eff.Conflict[0] != impl.FieldScreenShake {
		t.Fatalf("conflict list = %v, want [screen_shake]", eff.Conflict)
	}
}

func TestResolveForcedConflictHighContrast(t *testing.T) {
	store := newProfileStore()
	if err := store.Apply("acct-6", accountProfile(map[string]impl.Value{
		impl.FieldColorBlind:   impl.TextValue(impl.ColorBlindProtanopia),
		impl.FieldHighContrast: impl.BoolValue(true),
	})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	eff, err := store.Resolve("acct-6", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if eff.Fields[impl.FieldColorBlind].Str != impl.ColorBlindProtanopia {
		t.Fatalf("color blind mode = %+v, want protanopia", eff.Fields[impl.FieldColorBlind])
	}
	if eff.Fields[impl.FieldHighContrast].Bool {
		t.Fatalf("high contrast must be forced off while color blind mode is set")
	}
	if len(eff.Conflict) != 1 || eff.Conflict[0] != impl.FieldHighContrast {
		t.Fatalf("conflict list = %v, want [high_contrast]", eff.Conflict)
	}
}

func TestResolveKeepsUnrelatedFields(t *testing.T) {
	store := newProfileStore()
	if err := store.Apply("acct-7", accountProfile(map[string]impl.Value{
		impl.FieldReduceMotion: impl.BoolValue(true),
		impl.FieldSubtitleSize: impl.IntValue(200),
		impl.FieldHUDScale:     impl.IntValue(150),
	})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	eff, err := store.Resolve("acct-7", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := eff.Fields[impl.FieldSubtitleSize].Int; got != 200 {
		t.Fatalf("subtitle size = %d, want 200", got)
	}
	if got := eff.Fields[impl.FieldHUDScale].Int; got != 150 {
		t.Fatalf("hud scale = %d, want 150", got)
	}
	if len(eff.Conflict) != 1 || eff.Conflict[0] != impl.FieldScreenShake {
		t.Fatalf("conflict list = %v, want only [screen_shake]", eff.Conflict)
	}
}

func TestOverrideSubtitleSizeRange(t *testing.T) {
	store := newProfileStore()
	if err := store.Override("dev-8", map[string]impl.Value{impl.FieldSubtitleSize: impl.IntValue(99)}); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("subtitle size 99 must yield ErrOutOfRange, got %v", err)
	}
	if err := store.Override("dev-8", map[string]impl.Value{impl.FieldSubtitleSize: impl.IntValue(251)}); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("subtitle size 251 must yield ErrOutOfRange, got %v", err)
	}
	if err := store.Override("dev-8", map[string]impl.Value{impl.FieldSubtitleSize: impl.IntValue(250)}); err != nil {
		t.Fatalf("subtitle size 250 must be accepted, got %v", err)
	}
	eff, err := store.Resolve("", "dev-8")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := eff.Fields[impl.FieldSubtitleSize].Int; got != 250 {
		t.Fatalf("resolved subtitle size = %d, want 250", got)
	}
}

func TestMigrateV1Renames(t *testing.T) {
	store := newProfileStore()
	raw := []byte(`{"version":1,"fields":{"text_size":{"kind":"int","int":200},"contrast":{"kind":"bool","bool":true}}}`)
	migrated, err := store.Migrate(raw, 1)
	if err != nil {
		t.Fatalf("migrate v1: %v", err)
	}
	if migrated.Version != impl.CurrentVersion {
		t.Fatalf("migrated version = %d, want %d", migrated.Version, impl.CurrentVersion)
	}
	if got := migrated.Fields[impl.FieldSubtitleSize]; got.Kind != impl.KindInt || got.Int != 200 {
		t.Fatalf("migrated subtitle size = %+v, want int 200", got)
	}
	if got := migrated.Fields[impl.FieldHighContrast]; got.Kind != impl.KindBool || !got.Bool {
		t.Fatalf("migrated high contrast = %+v, want bool true", got)
	}
	if _, ok := migrated.Fields["text_size"]; ok {
		t.Fatalf("legacy field text_size survived the migration")
	}
}

func TestMigrateV2InvertsMotion(t *testing.T) {
	store := newProfileStore()
	raw := []byte(`{"version":2,"fields":{"motion":{"kind":"bool","bool":true},"hud_scale":{"kind":"int","int":130}}}`)
	migrated, err := store.Migrate(raw, 2)
	if err != nil {
		t.Fatalf("migrate v2: %v", err)
	}
	if migrated.Version != impl.CurrentVersion {
		t.Fatalf("migrated version = %d, want %d", migrated.Version, impl.CurrentVersion)
	}
	if got := migrated.Fields[impl.FieldReduceMotion]; got.Kind != impl.KindBool || got.Bool {
		t.Fatalf("migrated reduce motion = %+v, want bool false for motion true", got)
	}
	if got := migrated.Fields[impl.FieldHUDScale].Int; got != 130 {
		t.Fatalf("migrated hud scale = %d, want 130", got)
	}
	if _, ok := migrated.Fields["motion"]; ok {
		t.Fatalf("legacy field motion survived the migration")
	}
}

func TestMigrateRejectsBadPayload(t *testing.T) {
	store := newProfileStore()
	if _, err := store.Migrate([]byte(`{"version":1,"fields":`), 1); !errors.Is(err, impl.ErrBadPayload) {
		t.Fatalf("truncated payload must yield ErrBadPayload, got %v", err)
	}
	valid := []byte(`{"version":1,"fields":{"text_size":{"kind":"int","int":150}}}`)
	if _, err := store.Migrate(valid, 0); !errors.Is(err, impl.ErrUnsupportedVersion) {
		t.Fatalf("from version 0 must yield ErrUnsupportedVersion, got %v", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	src := newProfileStore()
	if err := src.Apply("acct-9", accountProfile(map[string]impl.Value{
		impl.FieldSubtitleSize: impl.IntValue(200),
		impl.FieldHighContrast: impl.BoolValue(true),
	})); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := src.Override("dev-9", map[string]impl.Value{impl.FieldSubtitleSize: impl.IntValue(210)}); err != nil {
		t.Fatalf("override: %v", err)
	}
	blob, err := src.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	dst := impl.NewAccessibilityProfile(impl.Profile{})
	if err := dst.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := dst.Defaults().Fields[impl.FieldSubtitleSize].Int; got != 140 {
		t.Fatalf("restored defaults subtitle size = %d, want 140", got)
	}
	eff, err := dst.Resolve("acct-9", "dev-9")
	if err != nil {
		t.Fatalf("resolve after restore: %v", err)
	}
	if got := eff.Fields[impl.FieldSubtitleSize].Int; got != 210 {
		t.Fatalf("restored device override = %d, want 210", got)
	}
	if got := eff.Sources[impl.FieldSubtitleSize]; got != impl.SourceDevice {
		t.Fatalf("restored source = %q, want %q", got, impl.SourceDevice)
	}
	if !eff.Fields[impl.FieldHighContrast].Bool {
		t.Fatalf("restored account preset lost high contrast")
	}
	eff, err = dst.Resolve("acct-9", "other-device")
	if err != nil {
		t.Fatalf("resolve on another device: %v", err)
	}
	if got := eff.Fields[impl.FieldSubtitleSize].Int; got != 200 {
		t.Fatalf("restored account preset = %d, want 200", got)
	}
	if err := dst.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must yield ErrBadSnapshot, got %v", err)
	}
	eff, err = dst.Resolve("acct-9", "dev-9")
	if err != nil {
		t.Fatalf("resolve after corrupt restore: %v", err)
	}
	if got := eff.Fields[impl.FieldSubtitleSize].Int; got != 210 {
		t.Fatalf("corrupt restore changed the live state to %d, want 210", got)
	}
}
