package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch51-go-game-c-07"
)

func hallIR(decay float32, tail int) impl.ImpulseResponse {
	return impl.ImpulseResponse{Name: "hall", Decay: decay, TailFrames: tail}
}

func loadPreset(t *testing.T, m *impl.ReverbTailManager, id string, ir impl.ImpulseResponse) {
	t.Helper()
	if err := m.LoadPreset(id, ir); err != nil {
		t.Fatalf("load preset %q: %v", id, err)
	}
}

func activatePreset(t *testing.T, m *impl.ReverbTailManager, id string, fadeMs int) {
	t.Helper()
	if err := m.ActivatePreset(id, fadeMs); err != nil {
		t.Fatalf("activate preset %q: %v", id, err)
	}
}

func TestLoadPresetValidatesIdentityAndKernel(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	if err := m.LoadPreset("   ", hallIR(0.5, 8)); !errors.Is(err, impl.ErrBlankPresetID) {
		t.Fatalf("blank preset id must fail with ErrBlankPresetID, got %v", err)
	}
	if err := m.LoadPreset("a", hallIR(0, 8)); !errors.Is(err, impl.ErrInvalidIR) {
		t.Fatalf("zero decay must fail with ErrInvalidIR, got %v", err)
	}
	if err := m.LoadPreset("a", hallIR(1, 8)); !errors.Is(err, impl.ErrInvalidIR) {
		t.Fatalf("unit decay must fail with ErrInvalidIR, got %v", err)
	}
	if err := m.LoadPreset("a", hallIR(0.5, 0)); !errors.Is(err, impl.ErrInvalidIR) {
		t.Fatalf("zero tail length must fail with ErrInvalidIR, got %v", err)
	}
	loadPreset(t, m, "a", hallIR(0.5, 8))
	if err := m.LoadPreset("a", hallIR(0.5, 8)); !errors.Is(err, impl.ErrDuplicatePreset) {
		t.Fatalf("duplicate preset id must fail with ErrDuplicatePreset, got %v", err)
	}
	if got := m.Stats().Presets; got != 1 {
		t.Fatalf("loaded presets = %d, want 1", got)
	}
}

func TestLoadPresetEnforcesSlotBudget(t *testing.T) {
	m := impl.NewReverbTailManager(2)
	loadPreset(t, m, "a", hallIR(0.5, 8))
	loadPreset(t, m, "b", hallIR(0.6, 8))
	err := m.LoadPreset("c", hallIR(0.7, 8))
	if !errors.Is(err, impl.ErrSlotBudget) {
		t.Fatalf("third preset with a budget of two must fail with ErrSlotBudget, got %v", err)
	}
	if got := m.Stats().Presets; got != 2 {
		t.Fatalf("loaded presets = %d, want the budget 2", got)
	}
	if err := m.ActivatePreset("c", 0); !errors.Is(err, impl.ErrUnknownPreset) {
		t.Fatalf("a rejected preset must not become activatable, got %v", err)
	}
}

func TestActivatePresetRequiresLoadedPreset(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	if err := m.ActivatePreset("ghost", 0); !errors.Is(err, impl.ErrUnknownPreset) {
		t.Fatalf("unknown preset must fail with ErrUnknownPreset, got %v", err)
	}
	if err := m.ActivatePreset("", 0); !errors.Is(err, impl.ErrUnknownPreset) {
		t.Fatalf("blank preset id must fail with ErrUnknownPreset, got %v", err)
	}
	if got := m.Stats().Active; got != "" {
		t.Fatalf("active preset = %q, want none", got)
	}
}

func TestActivatePresetValidatesFadeLength(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 64))
	if err := m.ActivatePreset("a", -1); !errors.Is(err, impl.ErrBadFade) {
		t.Fatalf("negative fade must fail with ErrBadFade, got %v", err)
	}
	if err := m.ActivatePreset("a", impl.MaxFadeMs+1); !errors.Is(err, impl.ErrBadFade) {
		t.Fatalf("fade above the window must fail with ErrBadFade, got %v", err)
	}
	activatePreset(t, m, "a", 0)
	if got := m.Stats().Active; got != "a" {
		t.Fatalf("active preset = %q, want a", got)
	}
	if got := m.Stats().Switches; got != 0 {
		t.Fatalf("switch counter = %d, want 0 for the first activation", got)
	}
}

func TestSetWetDryValidatesMix(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 64))
	if err := m.SetWetDry("a", 1.1, 0); !errors.Is(err, impl.ErrWetDryRange) {
		t.Fatalf("wet above one must fail with ErrWetDryRange, got %v", err)
	}
	if err := m.SetWetDry("a", 0.1, -0.1); !errors.Is(err, impl.ErrWetDryRange) {
		t.Fatalf("negative dry must fail with ErrWetDryRange, got %v", err)
	}
	if err := m.SetWetDry("a", 0.6, 0.6); !errors.Is(err, impl.ErrWetDryRange) {
		t.Fatalf("wet plus dry above one must fail with ErrWetDryRange, got %v", err)
	}
	if err := m.SetWetDry("ghost", 0.3, 0.3); !errors.Is(err, impl.ErrUnknownPreset) {
		t.Fatalf("unknown preset must fail with ErrUnknownPreset, got %v", err)
	}
	if err := m.SetWetDry("a", 0.3, 0.4); err != nil {
		t.Fatalf("valid mix: %v", err)
	}
}

func TestProcessRejectsBadFrameCounts(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 64))
	activatePreset(t, m, "a", 0)
	if _, err := m.Process(make([]float32, 4), 5); !errors.Is(err, impl.ErrFramesMismatch) {
		t.Fatalf("buffer shorter than the frame count must fail with ErrFramesMismatch, got %v", err)
	}
	if _, err := m.Process(nil, 0); !errors.Is(err, impl.ErrBlockRange) {
		t.Fatalf("zero frames must fail with ErrBlockRange, got %v", err)
	}
	if _, err := m.Process(make([]float32, impl.MaxBlockFrames+1), impl.MaxBlockFrames+1); !errors.Is(err, impl.ErrBlockRange) {
		t.Fatalf("frames above the block window must fail with ErrBlockRange, got %v", err)
	}
	if _, err := m.Process(make([]float32, 4), 4); err != nil {
		t.Fatalf("a matching block must render, got %v", err)
	}
}

func TestProcessPassesDrySignalUnchanged(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 64))
	activatePreset(t, m, "a", 0)
	if err := m.SetWetDry("a", 0, 1); err != nil {
		t.Fatalf("set mix: %v", err)
	}
	in := []float32{0.1, 0.2, 0.3}
	out, err := m.Process(in, len(in))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("returned %d samples, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("sample %d = %v, want the dry signal %v", i, out[i], in[i])
		}
	}
}

func TestProcessAppliesDecayTail(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 64))
	activatePreset(t, m, "a", 0)
	if err := m.SetWetDry("a", 1, 0); err != nil {
		t.Fatalf("set mix: %v", err)
	}
	out, err := m.Process([]float32{1, 0, 0, 0}, 4)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	want := []float32{1, 0.5, 0.25, 0.125}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("sample %d = %v, want the decayed tail %v", i, out[i], want[i])
		}
	}
}

func TestCrossfadeKeepsTheOldTailRunning(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 4096))
	loadPreset(t, m, "b", hallIR(0.8, 4096))
	activatePreset(t, m, "a", 0)
	if err := m.SetWetDry("a", 1, 0); err != nil {
		t.Fatalf("set mix for a: %v", err)
	}
	if err := m.SetWetDry("b", 1, 0); err != nil {
		t.Fatalf("set mix for b: %v", err)
	}
	if _, err := m.Process([]float32{1}, 1); err != nil {
		t.Fatalf("prime the tail: %v", err)
	}
	activatePreset(t, m, "b", 1)
	if got := m.Stats().Switches; got != 1 {
		t.Fatalf("switch counter = %d, want 1 after the preset change", got)
	}
	fade := impl.SampleRate / 1000
	out, err := m.Process(make([]float32, fade), fade)
	if err != nil {
		t.Fatalf("process during the fade: %v", err)
	}
	if out[0] < 0.45 || out[0] > 0.51 {
		t.Fatalf("first fade sample = %v, want the old tail around 0.49 instead of a restarted tail", out[0])
	}
	if math.Abs(float64(out[fade-1])) > 0.001 {
		t.Fatalf("last fade sample = %v, want the silent new preset tail", out[fade-1])
	}
}

func TestCrossfadeEndsOnTheNewPresetDecay(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 4096))
	loadPreset(t, m, "b", hallIR(0.9, 4096))
	activatePreset(t, m, "a", 0)
	if err := m.SetWetDry("a", 1, 0); err != nil {
		t.Fatalf("set mix for a: %v", err)
	}
	if err := m.SetWetDry("b", 1, 0); err != nil {
		t.Fatalf("set mix for b: %v", err)
	}
	if _, err := m.Process([]float32{1}, 1); err != nil {
		t.Fatalf("prime the tail: %v", err)
	}
	activatePreset(t, m, "b", 1)
	fade := impl.SampleRate / 1000
	if _, err := m.Process(make([]float32, fade), fade); err != nil {
		t.Fatalf("run the fade: %v", err)
	}
	out, err := m.Process([]float32{1, 0, 0, 0}, 4)
	if err != nil {
		t.Fatalf("process after the fade: %v", err)
	}
	want := []float32{1, 0.9, 0.81, 0.729}
	for i := range want {
		if math.Abs(float64(out[i]-want[i])) > 1e-5 {
			t.Fatalf("sample %d = %v, want the new preset decay %v", i, out[i], want[i])
		}
	}
}

func TestTailRemainingCountsDown(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 8))
	activatePreset(t, m, "a", 0)
	if err := m.SetWetDry("a", 1, 0); err != nil {
		t.Fatalf("set mix: %v", err)
	}
	if _, err := m.Process([]float32{1}, 1); err != nil {
		t.Fatalf("prime the tail: %v", err)
	}
	if got := m.TailRemaining(); got != 8 {
		t.Fatalf("tail remaining after the impulse = %d, want 8", got)
	}
	if _, err := m.Process(make([]float32, 3), 3); err != nil {
		t.Fatalf("process three silent frames: %v", err)
	}
	if got := m.TailRemaining(); got != 5 {
		t.Fatalf("tail remaining = %d, want 5 after three silent frames", got)
	}
	if _, err := m.Process(make([]float32, 5), 5); err != nil {
		t.Fatalf("process five more silent frames: %v", err)
	}
	if got := m.TailRemaining(); got != 0 {
		t.Fatalf("tail remaining = %d, want 0 once the tail window elapsed", got)
	}
	if _, err := m.Process(make([]float32, 1), 1); err != nil {
		t.Fatalf("process past the tail window: %v", err)
	}
	if got := m.TailRemaining(); got != 0 {
		t.Fatalf("tail remaining = %d, want the count to stay at zero", got)
	}
}

func TestFreezeHoldsTheTail(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 64))
	activatePreset(t, m, "a", 0)
	if err := m.SetWetDry("a", 1, 0); err != nil {
		t.Fatalf("set mix: %v", err)
	}
	if _, err := m.Process([]float32{1}, 1); err != nil {
		t.Fatalf("prime the tail: %v", err)
	}
	if err := m.Freeze(true); err != nil {
		t.Fatalf("freeze on: %v", err)
	}
	if !m.Stats().Frozen {
		t.Fatalf("stats must report the frozen state")
	}
	out, err := m.Process(make([]float32, 3), 3)
	if err != nil {
		t.Fatalf("process while frozen: %v", err)
	}
	for i := range out {
		if out[i] != 1 {
			t.Fatalf("frozen sample %d = %v, want the held tail 1", i, out[i])
		}
	}
	if err := m.Freeze(false); err != nil {
		t.Fatalf("freeze off: %v", err)
	}
	out, err = m.Process([]float32{0}, 1)
	if err != nil {
		t.Fatalf("process after unfreezing: %v", err)
	}
	if out[0] != 0.5 {
		t.Fatalf("sample after unfreezing = %v, want the decay to resume at 0.5", out[0])
	}
}

func TestSetSlotBudgetEvictsLeastRecentlyUsed(t *testing.T) {
	m := impl.NewReverbTailManager(3)
	loadPreset(t, m, "a", hallIR(0.5, 64))
	loadPreset(t, m, "b", hallIR(0.6, 64))
	loadPreset(t, m, "c", hallIR(0.7, 64))
	activatePreset(t, m, "a", 0)
	if err := m.SetSlotBudget(2); err != nil {
		t.Fatalf("shrink the budget: %v", err)
	}
	if got := m.Stats().SlotBudget; got != 2 {
		t.Fatalf("slot budget = %d, want 2", got)
	}
	if got := m.Stats().Presets; got != 2 {
		t.Fatalf("loaded presets = %d, want the inactive least recently used preset evicted", got)
	}
	if err := m.ActivatePreset("b", 0); !errors.Is(err, impl.ErrUnknownPreset) {
		t.Fatalf("the evicted preset must fail with ErrUnknownPreset, got %v", err)
	}
	if err := m.ActivatePreset("c", 0); err != nil {
		t.Fatalf("the newer preset must stay loadable, got %v", err)
	}
	if err := m.SetSlotBudget(0); !errors.Is(err, impl.ErrBadSlotBudget) {
		t.Fatalf("zero slot budget must fail with ErrBadSlotBudget, got %v", err)
	}
}

func TestStatsTracksPresetsAndFrames(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 64))
	activatePreset(t, m, "a", 0)
	if _, err := m.Process(make([]float32, 12), 12); err != nil {
		t.Fatalf("process twelve frames: %v", err)
	}
	stats := m.Stats()
	if stats.ProcessedFrames != 12 {
		t.Fatalf("processed frames = %d, want 12", stats.ProcessedFrames)
	}
	if stats.Presets != 1 {
		t.Fatalf("loaded presets = %d, want 1", stats.Presets)
	}
	if stats.SlotBudget != 4 {
		t.Fatalf("slot budget = %d, want 4", stats.SlotBudget)
	}
	if stats.Active != "a" {
		t.Fatalf("active preset = %q, want a", stats.Active)
	}
	if stats.Frozen {
		t.Fatalf("a fresh manager must not report the frozen state")
	}
}

func TestSnapshotRestoreKeepsTailAndMix(t *testing.T) {
	m := impl.NewReverbTailManager(4)
	loadPreset(t, m, "a", hallIR(0.5, 64))
	activatePreset(t, m, "a", 0)
	if err := m.SetWetDry("a", 1, 0); err != nil {
		t.Fatalf("set mix: %v", err)
	}
	if _, err := m.Process([]float32{1}, 1); err != nil {
		t.Fatalf("prime the tail: %v", err)
	}
	remaining := m.TailRemaining()
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewReverbTailManager(4)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Stats().Active; got != "a" {
		t.Fatalf("restored active preset = %q, want a", got)
	}
	if got := restored.TailRemaining(); got != remaining {
		t.Fatalf("restored tail remaining = %d, want %d", got, remaining)
	}
	out, err := restored.Process([]float32{0, 0}, 2)
	if err != nil {
		t.Fatalf("process after restore: %v", err)
	}
	want := []float32{0.5, 0.25}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("restored sample %d = %v, want the continued tail %v", i, out[i], want[i])
		}
	}
	if err := restored.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Stats().Active; got != "a" {
		t.Fatalf("rejected restore must not touch live state, active = %q", got)
	}
}
