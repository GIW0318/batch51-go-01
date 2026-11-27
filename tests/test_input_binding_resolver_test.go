package task

import (
	"errors"
	"fmt"
	"testing"

	impl "gsb/batch51-go-game-a-10"
)

func chordOf(keys ...string) impl.Chord {
	return impl.Chord{Keys: keys}
}

func keyInput(device impl.DeviceID, keys ...string) impl.InputEvent {
	return impl.InputEvent{Device: device, Keys: keys}
}

func newResolver() *impl.BindingResolver {
	return impl.NewBindingResolver([]impl.Binding{
		{Scope: impl.ScopeDefault, Action: "jump", Chord: chordOf("space")},
		{Scope: impl.ScopeDefault, Action: "quick_save", Chord: chordOf("ctrl", "s")},
	})
}

func mustResolve(t *testing.T, r *impl.BindingResolver, device impl.DeviceID, keys ...string) (string, bool) {
	t.Helper()
	action, ok, err := r.Resolve(device, keyInput(device, keys...))
	if err != nil {
		t.Fatalf("resolve %v: %v", keys, err)
	}
	return action, ok
}

func mustCommit(t *testing.T, r *impl.BindingResolver, scope impl.Scope) {
	t.Helper()
	if err := r.Commit(scope); err != nil {
		t.Fatalf("commit %s: %v", scope, err)
	}
}

func TestNewResolverResolvesDefaults(t *testing.T) {
	r := newResolver()
	action, ok := mustResolve(t, r, impl.KeyboardDevice, "space")
	if !ok || action != "jump" {
		t.Fatalf("resolve space = %q/%v, want jump/true", action, ok)
	}
	action, ok = mustResolve(t, r, impl.KeyboardDevice, "ctrl", "s")
	if !ok || action != "quick_save" {
		t.Fatalf("resolve ctrl+s = %q/%v, want quick_save/true", action, ok)
	}
	list := r.Bindings(impl.ScopeDefault)
	if len(list) != 2 {
		t.Fatalf("default bindings = %d, want 2", len(list))
	}
	if list[0].Action != "jump" || list[1].Action != "quick_save" {
		t.Fatalf("default bindings not sorted by action: %v", list)
	}
	if len(list[1].Chord.Keys) != 2 || list[1].Chord.Keys[0] != "ctrl" || list[1].Chord.Keys[1] != "s" {
		t.Fatalf("default chord = %v, want [ctrl s]", list[1].Chord.Keys)
	}
}

func TestResolveUnknownChordReturnsFalse(t *testing.T) {
	r := newResolver()
	action, ok := mustResolve(t, r, impl.KeyboardDevice, "tab")
	if ok {
		t.Fatalf("unbound chord resolved to %q", action)
	}
	if action != "" {
		t.Fatalf("unbound chord returned action %q, want empty", action)
	}
}

func TestUserLayerShadowsDefaultAction(t *testing.T) {
	r := newResolver()
	if err := r.Bind(impl.ScopeUser, "jump", chordOf("x")); err != nil {
		t.Fatalf("stage user binding: %v", err)
	}
	mustCommit(t, r, impl.ScopeUser)
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "x"); !ok || action != "jump" {
		t.Fatalf("resolve x = %q/%v, want jump/true", action, ok)
	}
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "space"); ok {
		t.Fatalf("default space binding still resolved to %q after the user rebind", action)
	}
}

func TestLongestChordWinsAcrossLayers(t *testing.T) {
	r := newResolver()
	if err := r.Bind(impl.ScopeUser, "crouch", chordOf("ctrl")); err != nil {
		t.Fatalf("stage crouch: %v", err)
	}
	mustCommit(t, r, impl.ScopeUser)
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "ctrl", "s"); !ok || action != "quick_save" {
		t.Fatalf("resolve ctrl+s = %q/%v, want the longer chord quick_save", action, ok)
	}
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "ctrl"); !ok || action != "crouch" {
		t.Fatalf("resolve ctrl = %q/%v, want crouch", action, ok)
	}
}

func TestBindRequiresCommit(t *testing.T) {
	r := newResolver()
	if err := r.Bind(impl.ScopeUser, "crouch", chordOf("ctrl", "c")); err != nil {
		t.Fatalf("stage user binding: %v", err)
	}
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "ctrl", "c"); ok {
		t.Fatalf("staged binding already resolved to %q", action)
	}
	mustCommit(t, r, impl.ScopeUser)
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "ctrl", "c"); !ok || action != "crouch" {
		t.Fatalf("resolve after commit = %q/%v, want crouch/true", action, ok)
	}
}

func TestUnbindRequiresCommit(t *testing.T) {
	r := newResolver()
	if err := r.Unbind(impl.ScopeDefault, "jump"); err != nil {
		t.Fatalf("stage unbind: %v", err)
	}
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "space"); !ok || action != "jump" {
		t.Fatalf("staged unbind already removed the live binding: %q/%v", action, ok)
	}
	mustCommit(t, r, impl.ScopeDefault)
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "space"); ok {
		t.Fatalf("binding still resolved to %q after commit of the unbind", action)
	}
	if got := len(r.Bindings(impl.ScopeDefault)); got != 1 {
		t.Fatalf("default bindings after unbind = %d, want 1", got)
	}
}

func TestValidateReportsDuplicateChord(t *testing.T) {
	r := newResolver()
	if err := r.Bind(impl.ScopeUser, "alpha", chordOf("k")); err != nil {
		t.Fatalf("stage alpha: %v", err)
	}
	if err := r.Bind(impl.ScopeUser, "beta", chordOf("k")); err != nil {
		t.Fatalf("stage beta: %v", err)
	}
	conflicts := r.Validate(impl.ScopeUser)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want exactly one duplicate chord", conflicts)
	}
	got := conflicts[0]
	if got.Kind != impl.KindDuplicateChord {
		t.Fatalf("conflict kind = %q, want %q", got.Kind, impl.KindDuplicateChord)
	}
	if got.Action != "alpha" || got.Other != "beta" {
		t.Fatalf("conflict actions = %q/%q, want alpha/beta", got.Action, got.Other)
	}
	if got.Chord != "k" {
		t.Fatalf("conflict chord = %q, want k", got.Chord)
	}
	if conflicts := r.Validate(impl.ScopeDefault); len(conflicts) != 0 {
		t.Fatalf("untouched layer reported conflicts: %v", conflicts)
	}
}

func TestCommitRejectsConflictAndKeepsLiveTable(t *testing.T) {
	r := newResolver()
	if err := r.Bind(impl.ScopeUser, "alpha", chordOf("k")); err != nil {
		t.Fatalf("stage alpha: %v", err)
	}
	if err := r.Bind(impl.ScopeUser, "beta", chordOf("k")); err != nil {
		t.Fatalf("stage beta: %v", err)
	}
	if err := r.Commit(impl.ScopeUser); !errors.Is(err, impl.ErrConflict) {
		t.Fatalf("conflicting commit must yield ErrConflict, got %v", err)
	}
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "k"); ok {
		t.Fatalf("rejected commit leaked binding %q into the live table", action)
	}
	if conflicts := r.Validate(impl.ScopeUser); len(conflicts) != 0 {
		t.Fatalf("rejected commit kept staged edits: %v", conflicts)
	}
	if err := r.Bind(impl.ScopeUser, "gamma", chordOf("j")); err != nil {
		t.Fatalf("stage gamma: %v", err)
	}
	mustCommit(t, r, impl.ScopeUser)
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "j"); !ok || action != "gamma" {
		t.Fatalf("resolve after a clean commit = %q/%v, want gamma/true", action, ok)
	}
}

func TestBindRejectsReservedKey(t *testing.T) {
	r := newResolver()
	for _, key := range []string{"f10", "f11", "f12"} {
		if err := r.Bind(impl.ScopeUser, "quit", chordOf(key)); !errors.Is(err, impl.ErrReservedKey) {
			t.Fatalf("reserved key %s must yield ErrReservedKey, got %v", key, err)
		}
	}
	if err := r.BindFor(impl.DeviceID("pad-sn-01"), impl.ScopeDefault, "quit", chordOf("f10")); !errors.Is(err, impl.ErrReservedKey) {
		t.Fatalf("reserved key on a device must yield ErrReservedKey, got %v", err)
	}
	if conflicts := r.Validate(impl.ScopeUser); len(conflicts) != 0 {
		t.Fatalf("reserved key rejection left staged edits: %v", conflicts)
	}
	if _, ok := mustResolve(t, r, impl.KeyboardDevice, "f10"); ok {
		t.Fatalf("reserved key f10 resolved to a binding")
	}
}

func TestBindRejectsBadInput(t *testing.T) {
	r := newResolver()
	if err := r.Bind(impl.ScopeUser, "  ", chordOf("k")); !errors.Is(err, impl.ErrEmptyAction) {
		t.Fatalf("blank action must yield ErrEmptyAction, got %v", err)
	}
	if err := r.Bind(impl.ScopeUser, "alpha", impl.Chord{}); !errors.Is(err, impl.ErrEmptyChord) {
		t.Fatalf("empty chord must yield ErrEmptyChord, got %v", err)
	}
	if err := r.Bind(impl.Scope("world"), "alpha", chordOf("k")); !errors.Is(err, impl.ErrBadScope) {
		t.Fatalf("unknown scope must yield ErrBadScope, got %v", err)
	}
	if err := r.Bind(impl.ScopeUser, "alpha", chordOf("k", "k")); !errors.Is(err, impl.ErrBadChord) {
		t.Fatalf("repeated key must yield ErrBadChord, got %v", err)
	}
	if err := r.Bind(impl.ScopeScene, "alpha", chordOf("k")); !errors.Is(err, impl.ErrNoActiveScene) {
		t.Fatalf("scene binding without a scene must yield ErrNoActiveScene, got %v", err)
	}
}

func TestSceneLayerOverridesUserLayer(t *testing.T) {
	r := newResolver()
	if err := r.Bind(impl.ScopeUser, "crouch", chordOf("ctrl")); err != nil {
		t.Fatalf("stage user binding: %v", err)
	}
	mustCommit(t, r, impl.ScopeUser)
	if err := r.PushScene("pause"); err != nil {
		t.Fatalf("push scene: %v", err)
	}
	if err := r.PushScene("map"); !errors.Is(err, impl.ErrSceneActive) {
		t.Fatalf("second scene must yield ErrSceneActive, got %v", err)
	}
	if err := r.Bind(impl.ScopeScene, "crouch", chordOf("shift")); err != nil {
		t.Fatalf("stage scene crouch: %v", err)
	}
	if err := r.Bind(impl.ScopeScene, "open_map", chordOf("m")); err != nil {
		t.Fatalf("stage scene open_map: %v", err)
	}
	mustCommit(t, r, impl.ScopeScene)
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "shift"); !ok || action != "crouch" {
		t.Fatalf("resolve shift = %q/%v, want the scene crouch", action, ok)
	}
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "ctrl"); ok {
		t.Fatalf("user crouch still resolved to %q under the scene layer", action)
	}
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "m"); !ok || action != "open_map" {
		t.Fatalf("resolve m = %q/%v, want open_map", action, ok)
	}
}

func TestPopSceneDropsSceneBindings(t *testing.T) {
	r := newResolver()
	if err := r.PushScene("pause"); err != nil {
		t.Fatalf("push scene: %v", err)
	}
	if err := r.Bind(impl.ScopeScene, "open_map", chordOf("m")); err != nil {
		t.Fatalf("stage scene binding: %v", err)
	}
	mustCommit(t, r, impl.ScopeScene)
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "m"); !ok || action != "open_map" {
		t.Fatalf("resolve m inside the scene = %q/%v, want open_map", action, ok)
	}
	if err := r.PopScene("map"); !errors.Is(err, impl.ErrUnknownScene) {
		t.Fatalf("popping another scene must yield ErrUnknownScene, got %v", err)
	}
	if err := r.PopScene("pause"); err != nil {
		t.Fatalf("pop scene: %v", err)
	}
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "m"); ok {
		t.Fatalf("scene binding still resolved to %q after the scene closed", action)
	}
	if got := len(r.Bindings(impl.ScopeScene)); got != 0 {
		t.Fatalf("scene bindings after pop = %d, want 0", got)
	}
	if err := r.PopScene("pause"); !errors.Is(err, impl.ErrUnknownScene) {
		t.Fatalf("second pop must yield ErrUnknownScene, got %v", err)
	}
}

func TestDeviceScopedBindingWinsForSerial(t *testing.T) {
	r := impl.NewBindingResolver([]impl.Binding{
		{Scope: impl.ScopeDefault, Action: "jump", Chord: chordOf("pad_a")},
	})
	if err := r.BindFor(impl.DeviceID("pad-sn-01"), impl.ScopeUser, "dodge", chordOf("pad_a")); err != nil {
		t.Fatalf("stage device binding: %v", err)
	}
	mustCommit(t, r, impl.ScopeUser)
	if action, ok := mustResolve(t, r, impl.DeviceID("pad-sn-01"), "pad_a"); !ok || action != "dodge" {
		t.Fatalf("resolve pad_a on the bound serial = %q/%v, want dodge", action, ok)
	}
	if action, ok := mustResolve(t, r, impl.DeviceID("pad-sn-02"), "pad_a"); !ok || action != "jump" {
		t.Fatalf("resolve pad_a on another serial = %q/%v, want the default jump", action, ok)
	}
	list := r.Bindings(impl.ScopeUser)
	if len(list) != 1 || list[0].Device != impl.DeviceID("pad-sn-01") {
		t.Fatalf("user bindings = %+v, want one binding scoped to pad-sn-01", list)
	}
}

func TestScopeLimitEnforced(t *testing.T) {
	r := newResolver()
	for i := 1; i <= impl.MaxBindingsPerScope; i++ {
		action := fmt.Sprintf("act-%02d", i)
		key := fmt.Sprintf("k%02d", i)
		if err := r.Bind(impl.ScopeUser, action, chordOf(key)); err != nil {
			t.Fatalf("stage binding %d: %v", i, err)
		}
	}
	mustCommit(t, r, impl.ScopeUser)
	if got := len(r.Bindings(impl.ScopeUser)); got != impl.MaxBindingsPerScope {
		t.Fatalf("user bindings = %d, want %d", got, impl.MaxBindingsPerScope)
	}
	if err := r.Bind(impl.ScopeUser, "act-over", chordOf("k99")); !errors.Is(err, impl.ErrScopeLimit) {
		t.Fatalf("binding beyond the cap must yield ErrScopeLimit, got %v", err)
	}
	if err := r.Bind(impl.ScopeUser, "act-01", chordOf("zz")); err != nil {
		t.Fatalf("replacing an existing action at the cap must be allowed, got %v", err)
	}
	mustCommit(t, r, impl.ScopeUser)
	if action, ok := mustResolve(t, r, impl.KeyboardDevice, "zz"); !ok || action != "act-01" {
		t.Fatalf("resolve zz = %q/%v, want act-01", action, ok)
	}
	if got := len(r.Bindings(impl.ScopeUser)); got != impl.MaxBindingsPerScope {
		t.Fatalf("user bindings after replacement = %d, want %d", got, impl.MaxBindingsPerScope)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	src := newResolver()
	if err := src.Bind(impl.ScopeUser, "crouch", chordOf("ctrl")); err != nil {
		t.Fatalf("stage user binding: %v", err)
	}
	mustCommit(t, src, impl.ScopeUser)
	if err := src.BindFor(impl.DeviceID("pad-sn-07"), impl.ScopeUser, "dodge", chordOf("pad_a")); err != nil {
		t.Fatalf("stage device binding: %v", err)
	}
	mustCommit(t, src, impl.ScopeUser)
	if err := src.PushScene("pause"); err != nil {
		t.Fatalf("push scene: %v", err)
	}
	if err := src.Bind(impl.ScopeScene, "open_map", chordOf("m")); err != nil {
		t.Fatalf("stage scene binding: %v", err)
	}
	mustCommit(t, src, impl.ScopeScene)
	blob, err := src.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	dst := impl.NewBindingResolver(nil)
	if err := dst.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if action, ok := mustResolve(t, dst, impl.KeyboardDevice, "space"); !ok || action != "jump" {
		t.Fatalf("restored default binding = %q/%v, want jump/true", action, ok)
	}
	if action, ok := mustResolve(t, dst, impl.KeyboardDevice, "ctrl"); !ok || action != "crouch" {
		t.Fatalf("restored user binding = %q/%v, want crouch/true", action, ok)
	}
	if action, ok := mustResolve(t, dst, impl.DeviceID("pad-sn-07"), "pad_a"); !ok || action != "dodge" {
		t.Fatalf("restored device binding = %q/%v, want dodge/true", action, ok)
	}
	if action, ok := mustResolve(t, dst, impl.KeyboardDevice, "m"); !ok || action != "open_map" {
		t.Fatalf("restored scene binding = %q/%v, want open_map/true", action, ok)
	}
	if err := dst.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must yield ErrBadSnapshot, got %v", err)
	}
	if action, ok := mustResolve(t, dst, impl.KeyboardDevice, "ctrl"); !ok || action != "crouch" {
		t.Fatalf("corrupt restore changed the live table: %q/%v", action, ok)
	}
}
