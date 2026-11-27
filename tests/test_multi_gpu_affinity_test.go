package task

import (
	"errors"
	"fmt"
	"testing"

	impl "gsb/batch51-go-game-b-13"
)

func newMGPU(t *testing.T, mode string) *impl.MultiGpuAffinity {
	t.Helper()
	m, err := impl.NewMultiGpuAffinity(impl.MGPUConfig{
		MaxAdapters:    4,
		QueueDepth:     4,
		MaxSharedBytes: 1 << 28,
		MaxCopyBytes:   1 << 24,
		Mode:           mode,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	for _, id := range []string{"gpu-a", "gpu-b"} {
		if err := m.RegisterAdapter(impl.AdapterInfo{ID: id, Name: id, MemoryMB: 8192}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	return m
}

func TestRegisterAdapterValidatesAndDeduplicates(t *testing.T) {
	if _, err := impl.NewMultiGpuAffinity(impl.MGPUConfig{MaxAdapters: 2, QueueDepth: 4, MaxSharedBytes: 1 << 20, MaxCopyBytes: 1 << 16, Mode: ""}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("blank mode must fail with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewMultiGpuAffinity(impl.MGPUConfig{MaxAdapters: 2, QueueDepth: 0, MaxSharedBytes: 1 << 20, MaxCopyBytes: 1 << 16, Mode: impl.ModeAlternate}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero queue depth must fail with ErrBadConfig, got %v", err)
	}
	m, err := impl.NewMultiGpuAffinity(impl.MGPUConfig{MaxAdapters: 2, QueueDepth: 2, MaxSharedBytes: 1 << 20, MaxCopyBytes: 1 << 16, Mode: impl.ModeAlternate})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	if err := m.RegisterAdapter(impl.AdapterInfo{ID: "  "}); !errors.Is(err, impl.ErrBadAdapter) {
		t.Fatalf("blank adapter id must fail with ErrBadAdapter, got %v", err)
	}
	if err := m.RegisterAdapter(impl.AdapterInfo{ID: "gpu-a", QueueDepth: -1}); !errors.Is(err, impl.ErrBadAdapter) {
		t.Fatalf("negative queue depth must fail with ErrBadAdapter, got %v", err)
	}
	if err := m.RegisterAdapter(impl.AdapterInfo{ID: "gpu-a", Name: "primary"}); err != nil {
		t.Fatalf("register first adapter: %v", err)
	}
	if err := m.RegisterAdapter(impl.AdapterInfo{ID: "gpu-a", Name: "again"}); !errors.Is(err, impl.ErrDuplicateAdapter) {
		t.Fatalf("duplicate adapter must fail with ErrDuplicateAdapter, got %v", err)
	}
	if err := m.RegisterAdapter(impl.AdapterInfo{ID: "gpu-b", Name: "secondary"}); err != nil {
		t.Fatalf("register second adapter: %v", err)
	}
	if err := m.RegisterAdapter(impl.AdapterInfo{ID: "gpu-c", Name: "third"}); !errors.Is(err, impl.ErrAdapterLimit) {
		t.Fatalf("third adapter must fail with ErrAdapterLimit, got %v", err)
	}
	st := m.Stats()
	if st.Adapters != 2 || st.Enabled != 2 {
		t.Fatalf("stats adapters=%d enabled=%d, want 2 and 2", st.Adapters, st.Enabled)
	}
}

func TestAssignFrameAlternatesAdapters(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	want := []string{"gpu-a", "gpu-b", "gpu-a", "gpu-b"}
	for i, adapter := range want {
		number := int64(i + 1)
		as, err := m.AssignFrame(impl.FrameReq{Number: number, Passes: 2 + i})
		if err != nil {
			t.Fatalf("assign frame %d: %v", number, err)
		}
		if as.Frame != number {
			t.Fatalf("assignment frame = %d, want %d", as.Frame, number)
		}
		if as.Adapter != adapter {
			t.Fatalf("frame %d went to %s, want %s in alternate mode", number, as.Adapter, adapter)
		}
		if as.Passes != 2+i {
			t.Fatalf("frame %d passes = %d, want %d", number, as.Passes, 2+i)
		}
		if as.Deferred {
			t.Fatalf("frame %d must not be deferred with a free queue", number)
		}
	}
	if got := m.Stats().Pending; got != 4 {
		t.Fatalf("pending frames = %d, want 4", got)
	}
}

func TestAssignFrameRejectsBadFrame(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	if _, err := m.AssignFrame(impl.FrameReq{Number: 0, Passes: 1}); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("frame number zero must fail with ErrBadFrame, got %v", err)
	}
	if _, err := m.AssignFrame(impl.FrameReq{Number: 3, Passes: 0}); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("zero passes must fail with ErrBadFrame, got %v", err)
	}
	if got := m.Stats().Pending; got != 0 {
		t.Fatalf("rejected requests must not enter the queue, got %d pending", got)
	}
	if err := m.Submit(3); !errors.Is(err, impl.ErrUnknownFrame) {
		t.Fatalf("submitting a never assigned frame must fail with ErrUnknownFrame, got %v", err)
	}
}

func TestQueueDepthDefersFrame(t *testing.T) {
	m, err := impl.NewMultiGpuAffinity(impl.MGPUConfig{
		MaxAdapters:    4,
		QueueDepth:     1,
		MaxSharedBytes: 1 << 20,
		MaxCopyBytes:   1 << 16,
		Mode:           impl.ModeAlternate,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	for _, id := range []string{"gpu-a", "gpu-b"} {
		if err := m.RegisterAdapter(impl.AdapterInfo{ID: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	for _, number := range []int64{1, 2} {
		if _, err := m.AssignFrame(impl.FrameReq{Number: number, Passes: 1}); err != nil {
			t.Fatalf("assign frame %d: %v", number, err)
		}
	}
	deferred, err := m.AssignFrame(impl.FrameReq{Number: 3, Passes: 1})
	if err != nil {
		t.Fatalf("assign frame 3: %v", err)
	}
	if !deferred.Deferred {
		t.Fatalf("frame 3 must be deferred because gpu-a is at its queue depth")
	}
	if deferred.Reason == "" {
		t.Fatalf("a deferred frame must carry a reason")
	}
	st := m.Stats()
	if st.Deferred != 1 {
		t.Fatalf("deferred counter = %d, want 1", st.Deferred)
	}
	if st.Pending != 2 {
		t.Fatalf("pending frames = %d, want the two accepted frames", st.Pending)
	}
	if err := m.Complete(1); err != nil {
		t.Fatalf("complete frame 1: %v", err)
	}
	again, err := m.AssignFrame(impl.FrameReq{Number: 3, Passes: 1})
	if err != nil {
		t.Fatalf("assign frame 3 after the slot freed: %v", err)
	}
	if again.Deferred {
		t.Fatalf("frame 3 must fit after gpu-a released a slot")
	}
	if again.Adapter != "gpu-a" {
		t.Fatalf("frame 3 adapter = %s, want gpu-a", again.Adapter)
	}
}

func TestSubmitCompleteFreesQueueSlot(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	if _, err := m.AssignFrame(impl.FrameReq{Number: 1, Passes: 3}); err != nil {
		t.Fatalf("assign frame 1: %v", err)
	}
	if err := m.Submit(1); err != nil {
		t.Fatalf("submit frame 1: %v", err)
	}
	st := m.Stats()
	if st.InFlight != 1 {
		t.Fatalf("in flight frames = %d, want 1", st.InFlight)
	}
	if st.Pending != 0 {
		t.Fatalf("pending frames after submit = %d, want 0", st.Pending)
	}
	if err := m.Submit(1); !errors.Is(err, impl.ErrUnknownFrame) {
		t.Fatalf("submitting an in flight frame again must fail with ErrUnknownFrame, got %v", err)
	}
	if err := m.Complete(1); err != nil {
		t.Fatalf("complete frame 1: %v", err)
	}
	st = m.Stats()
	if st.InFlight != 0 || st.Pending != 0 {
		t.Fatalf("counters after completion in_flight=%d pending=%d, want 0 and 0", st.InFlight, st.Pending)
	}
	if err := m.Complete(1); !errors.Is(err, impl.ErrUnknownFrame) {
		t.Fatalf("completing a finished frame must fail with ErrUnknownFrame, got %v", err)
	}
	next, err := m.AssignFrame(impl.FrameReq{Number: 2, Passes: 1})
	if err != nil {
		t.Fatalf("assign frame 2: %v", err)
	}
	if next.Adapter != "gpu-b" {
		t.Fatalf("frame 2 adapter = %s, want gpu-b on the alternate schedule", next.Adapter)
	}
}

func TestAssignViewportsDistributesRoundRobin(t *testing.T) {
	m, err := impl.NewMultiGpuAffinity(impl.MGPUConfig{
		MaxAdapters:    4,
		QueueDepth:     4,
		MaxSharedBytes: 1 << 20,
		MaxCopyBytes:   1 << 16,
		Mode:           impl.ModeSplit,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	for _, id := range []string{"gpu-a", "gpu-b", "gpu-c"} {
		if err := m.RegisterAdapter(impl.AdapterInfo{ID: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	vps := []impl.Viewport{
		{ID: "vp-0", Width: 1920, Height: 1080},
		{ID: "vp-1", Width: 1280, Height: 720},
		{ID: "vp-2", Width: 1280, Height: 720},
		{ID: "vp-3", Width: 800, Height: 600},
	}
	as, err := m.AssignViewports(vps)
	if err != nil {
		t.Fatalf("assign viewports: %v", err)
	}
	if len(as) != len(vps) {
		t.Fatalf("assignments = %d, want %d", len(as), len(vps))
	}
	want := []string{"gpu-a", "gpu-b", "gpu-c", "gpu-a"}
	for i, adapter := range want {
		if as[i].Viewport != vps[i].ID {
			t.Fatalf("assignment %d viewport = %q, want %q", i, as[i].Viewport, vps[i].ID)
		}
		if as[i].Adapter != adapter {
			t.Fatalf("viewport %s went to %s, want %s", vps[i].ID, as[i].Adapter, adapter)
		}
		if as[i].Deferred {
			t.Fatalf("viewport %s must not be deferred with free queues", vps[i].ID)
		}
	}
	if _, err := m.AssignViewports(nil); err != nil {
		t.Fatalf("an empty viewport list must be accepted, got %v", err)
	}
}

func TestAcquireSharedDeduplicatesKey(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	first, err := m.AcquireShared("albedo-atlas", impl.Size{Width: 1024, Height: 1024})
	if err != nil {
		t.Fatalf("acquire shared: %v", err)
	}
	if first.Key != "albedo-atlas" || first.Bytes != 1048576 {
		t.Fatalf("shared res key=%q bytes=%d, want albedo-atlas and 1048576", first.Key, first.Bytes)
	}
	second, err := m.AcquireShared("albedo-atlas", impl.Size{Width: 1024, Height: 1024})
	if err != nil {
		t.Fatalf("acquire shared again: %v", err)
	}
	if second.Bytes != first.Bytes {
		t.Fatalf("second acquire bytes = %d, want %d", second.Bytes, first.Bytes)
	}
	if got := m.Stats().SharedBytes; got != 1048576 {
		t.Fatalf("shared bytes = %d, want the key counted once", got)
	}
	other, err := m.AcquireShared("normal-atlas", impl.Size{Width: 512, Height: 512})
	if err != nil {
		t.Fatalf("acquire second key: %v", err)
	}
	if other.Bytes != 262144 {
		t.Fatalf("second key bytes = %d, want 262144", other.Bytes)
	}
	if got := m.Stats().SharedBytes; got != 1310720 {
		t.Fatalf("shared bytes = %d, want 1048576+262144", got)
	}
}

func TestAcquireSharedRejectsSizeMismatch(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	if _, err := m.AcquireShared("  ", impl.Size{Width: 8, Height: 8}); !errors.Is(err, impl.ErrBadShared) {
		t.Fatalf("blank key must fail with ErrBadShared, got %v", err)
	}
	if _, err := m.AcquireShared("depth", impl.Size{Width: 0, Height: 64}); !errors.Is(err, impl.ErrBadShared) {
		t.Fatalf("zero width must fail with ErrBadShared, got %v", err)
	}
	if _, err := m.AcquireShared("depth", impl.Size{Width: 256, Height: 256}); err != nil {
		t.Fatalf("acquire shared: %v", err)
	}
	if _, err := m.AcquireShared("depth", impl.Size{Width: 128, Height: 256}); !errors.Is(err, impl.ErrSharedMismatch) {
		t.Fatalf("a different size for the same key must fail with ErrSharedMismatch, got %v", err)
	}
	if got := m.Stats().SharedBytes; got != 65536 {
		t.Fatalf("shared bytes after the rejected request = %d, want 65536", got)
	}
}

func TestAcquireSharedEnforcesByteLimit(t *testing.T) {
	m, err := impl.NewMultiGpuAffinity(impl.MGPUConfig{
		MaxAdapters:    4,
		QueueDepth:     4,
		MaxSharedBytes: 1 << 20,
		MaxCopyBytes:   1 << 16,
		Mode:           impl.ModeAlternate,
	})
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	if err := m.RegisterAdapter(impl.AdapterInfo{ID: "gpu-a"}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	if err := m.RegisterAdapter(impl.AdapterInfo{ID: "gpu-b"}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	if _, err := m.AcquireShared("huge", impl.Size{Width: 1024, Height: 1024}); err != nil {
		t.Fatalf("the exactly fitting resource must be accepted, got %v", err)
	}
	if _, err := m.AcquireShared("extra", impl.Size{Width: 16, Height: 16}); !errors.Is(err, impl.ErrSharedLimit) {
		t.Fatalf("over budget acquire must fail with ErrSharedLimit, got %v", err)
	}
	if got := m.Stats().SharedBytes; got != 1048576 {
		t.Fatalf("shared bytes after the rejected request = %d, want 1048576", got)
	}
	if _, ok := m.AcquireShared("huge", impl.Size{Width: 1024, Height: 1024}); ok != nil {
		t.Fatalf("re-reading a registered key must stay allowed, got %v", ok)
	}
}

func TestSyncCrossAdapterRejectsUnknownKey(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	if _, err := m.AcquireShared("albedo", impl.Size{Width: 64, Height: 64}); err != nil {
		t.Fatalf("acquire shared: %v", err)
	}
	if err := m.SyncCrossAdapter([]impl.FenceToken{{Key: "ghost", Adapter: "gpu-b", Seq: 1, Write: true}}); !errors.Is(err, impl.ErrUnknownShared) {
		t.Fatalf("an unknown shared key must fail with ErrUnknownShared, got %v", err)
	}
	if err := m.SyncCrossAdapter([]impl.FenceToken{{Key: "albedo", Adapter: "gpu-a", Seq: 1, Write: true}}); err != nil {
		t.Fatalf("a valid fence token must be accepted, got %v", err)
	}
	mixed := []impl.FenceToken{
		{Key: "albedo", Adapter: "gpu-b", Seq: 2, Write: false},
		{Key: "ghost", Adapter: "gpu-a", Seq: 3, Write: true},
	}
	if err := m.SyncCrossAdapter(mixed); !errors.Is(err, impl.ErrUnknownShared) {
		t.Fatalf("a batch with an unknown key must fail with ErrUnknownShared, got %v", err)
	}
	if err := m.SyncCrossAdapter([]impl.FenceToken{{Key: "albedo", Adapter: "gpu-b", Seq: 2, Write: false}}); err != nil {
		t.Fatalf("the batch must not apply partially, seq 2 must still be free: %v", err)
	}
}

func TestSyncCrossAdapterRejectsRepeatedSequence(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	if _, err := m.AcquireShared("albedo", impl.Size{Width: 64, Height: 64}); err != nil {
		t.Fatalf("acquire shared: %v", err)
	}
	if err := m.SyncCrossAdapter([]impl.FenceToken{{Key: "albedo", Adapter: "gpu-a", Seq: 5, Write: true}}); err != nil {
		t.Fatalf("first stamp: %v", err)
	}
	if err := m.SyncCrossAdapter([]impl.FenceToken{{Key: "albedo", Adapter: "gpu-b", Seq: 5, Write: false}}); !errors.Is(err, impl.ErrFenceOrder) {
		t.Fatalf("a repeated fence sequence must fail with ErrFenceOrder, got %v", err)
	}
	if err := m.SyncCrossAdapter([]impl.FenceToken{{Key: "albedo", Adapter: "gpu-b", Seq: 0, Write: false}}); !errors.Is(err, impl.ErrFenceOrder) {
		t.Fatalf("a non positive fence sequence must fail with ErrFenceOrder, got %v", err)
	}
	if err := m.SyncCrossAdapter([]impl.FenceToken{{Key: "albedo", Adapter: "gpu-b", Seq: 6, Write: false}}); err != nil {
		t.Fatalf("a higher fence sequence must be accepted, got %v", err)
	}
}

func TestDisableAdapterRoutesNewWorkToPrimary(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	if err := m.DisableAdapter("gpu-b"); err != nil {
		t.Fatalf("disable gpu-b: %v", err)
	}
	info := m.FallbackState()
	if !info.Active {
		t.Fatalf("fallback must be active after disabling one of two adapters")
	}
	if len(info.Disabled) != 1 || info.Disabled[0] != "gpu-b" {
		t.Fatalf("fallback disabled list = %v, want [gpu-b]", info.Disabled)
	}
	if got := m.Stats().Enabled; got != 1 {
		t.Fatalf("enabled adapters = %d, want 1", got)
	}
	for _, number := range []int64{1, 2, 3} {
		as, err := m.AssignFrame(impl.FrameReq{Number: number, Passes: 1})
		if err != nil {
			t.Fatalf("assign frame %d: %v", number, err)
		}
		if as.Adapter != "gpu-a" {
			t.Fatalf("frame %d went to %s after the fallback, want gpu-a", number, as.Adapter)
		}
		if as.Deferred {
			t.Fatalf("frame %d must be accepted by the remaining adapter", number)
		}
	}
}

func TestDisableAdapterRejectsUnknownAndLastAdapter(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	if err := m.DisableAdapter("gpu-zz"); !errors.Is(err, impl.ErrUnknownAdapter) {
		t.Fatalf("disabling an unknown adapter must fail with ErrUnknownAdapter, got %v", err)
	}
	if err := m.DisableAdapter("gpu-b"); err != nil {
		t.Fatalf("disable gpu-b: %v", err)
	}
	if err := m.DisableAdapter("gpu-a"); !errors.Is(err, impl.ErrNoAdapter) {
		t.Fatalf("disabling the last enabled adapter must fail with ErrNoAdapter, got %v", err)
	}
	if got := m.Stats().Enabled; got != 1 {
		t.Fatalf("the last adapter must stay enabled, got %d", got)
	}
	if err := m.DisableAdapter("gpu-b"); err != nil {
		t.Fatalf("disabling an already disabled adapter must be idempotent, got %v", err)
	}
	info := m.FallbackState()
	if len(info.Disabled) != 1 {
		t.Fatalf("disabled list = %v, want a single entry", info.Disabled)
	}
}

func TestSnapshotRestoreKeepsAssignmentsAndFallback(t *testing.T) {
	cfg := impl.MGPUConfig{
		MaxAdapters:    4,
		QueueDepth:     4,
		MaxSharedBytes: 1 << 24,
		MaxCopyBytes:   1 << 16,
		Mode:           impl.ModeAlternate,
	}
	m, err := impl.NewMultiGpuAffinity(cfg)
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	for _, id := range []string{"gpu-a", "gpu-b"} {
		if err := m.RegisterAdapter(impl.AdapterInfo{ID: id}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	if _, err := m.AssignFrame(impl.FrameReq{Number: 1, Passes: 2}); err != nil {
		t.Fatalf("assign frame 1: %v", err)
	}
	if err := m.Submit(1); err != nil {
		t.Fatalf("submit frame 1: %v", err)
	}
	if _, err := m.AssignFrame(impl.FrameReq{Number: 2, Passes: 1}); err != nil {
		t.Fatalf("assign frame 2: %v", err)
	}
	if err := m.DisableAdapter("gpu-b"); err != nil {
		t.Fatalf("disable gpu-b: %v", err)
	}
	if _, err := m.AcquireShared("albedo", impl.Size{Width: 128, Height: 128}); err != nil {
		t.Fatalf("acquire shared: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored, err := impl.NewMultiGpuAffinity(cfg)
	if err != nil {
		t.Fatalf("new restored scheduler: %v", err)
	}
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	info := restored.FallbackState()
	if !info.Active || len(info.Disabled) != 1 || info.Disabled[0] != "gpu-b" {
		t.Fatalf("restored fallback = %+v, want active with gpu-b disabled", info)
	}
	st := restored.Stats()
	if st.Enabled != 1 || st.InFlight != 1 {
		t.Fatalf("restored stats enabled=%d in_flight=%d, want 1 and 1", st.Enabled, st.InFlight)
	}
	if st.SharedBytes != 16384 {
		t.Fatalf("restored shared bytes = %d, want 16384", st.SharedBytes)
	}
	as, err := restored.AssignFrame(impl.FrameReq{Number: 3, Passes: 1})
	if err != nil {
		t.Fatalf("assign after restore: %v", err)
	}
	if as.Adapter != "gpu-a" {
		t.Fatalf("frame 3 after restore went to %s, want gpu-a", as.Adapter)
	}
	if err := restored.Complete(1); err != nil {
		t.Fatalf("the restored in flight frame must be completable, got %v", err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	m := newMGPU(t, impl.ModeAlternate)
	if err := m.Restore([]byte(fmt.Sprintf("%s", "{not json"))); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if err := m.Restore(nil); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("empty snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	st := m.Stats()
	if st.Adapters != 2 || st.Enabled != 2 {
		t.Fatalf("live adapters after a rejected restore adapters=%d enabled=%d, want 2 and 2", st.Adapters, st.Enabled)
	}
	if _, err := m.AssignFrame(impl.FrameReq{Number: 1, Passes: 1}); err != nil {
		t.Fatalf("the live scheduler must keep accepting frames, got %v", err)
	}
}
