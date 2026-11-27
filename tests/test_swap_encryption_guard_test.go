package task

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-d-07"
)

func swapConfig() impl.GuardConfig {
	return impl.GuardConfig{
		MaxVolumes:        8,
		BootID:            "boot-7f31",
		PageSize:          4096,
		MaxPagesPerVolume: 100000,
	}
}

func enableVolume(t *testing.T, g *impl.SwapEncryptionGuard, device, area string) impl.SwapVolume {
	t.Helper()
	v, err := g.Enable(device, impl.SwapRequest{Area: area, Priority: 10, SizeMB: 512, Encrypted: true})
	if err != nil {
		t.Fatalf("enable %s must succeed, got %v", device, err)
	}
	return v
}

func TestEnableCreatesActiveVolume(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/nvme0n1p3", "swap-a")
	if v.ID == "" {
		t.Fatalf("volume id must not be empty")
	}
	if v.Device != "/dev/nvme0n1p3" || v.Area != "swap-a" {
		t.Fatalf("volume identity mismatch: %+v", v)
	}
	if v.Epoch != 1 {
		t.Fatalf("first key epoch = %d, want 1", v.Epoch)
	}
	if v.State != impl.VolumeActive {
		t.Fatalf("volume state = %q, want %q", v.State, impl.VolumeActive)
	}
	if v.BootID != "boot-7f31" {
		t.Fatalf("volume boot id = %q, want the configured boot", v.BootID)
	}
	state, err := g.Verify(v.ID)
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if state.Epoch != 1 || state.Pages != 0 || state.InFlight != 0 {
		t.Fatalf("fresh volume state mismatch: %+v", state)
	}
	if len(state.Epochs) != 1 || state.Epochs[0].Epoch != 1 {
		t.Fatalf("epoch history = %+v, want a single epoch 1", state.Epochs)
	}
	if got := len(g.Volumes()); got != 1 {
		t.Fatalf("live volumes = %d, want 1", got)
	}
}

func TestEnableRejectsBadInput(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	if _, err := g.Enable("   ", impl.SwapRequest{Area: "swap-a"}); !errors.Is(err, impl.ErrBlankDevice) {
		t.Fatalf("blank device must fail with ErrBlankDevice, got %v", err)
	}
	if _, err := g.Enable("/dev/sda2", impl.SwapRequest{Area: "  "}); !errors.Is(err, impl.ErrBlankArea) {
		t.Fatalf("blank area must fail with ErrBlankArea, got %v", err)
	}
	enableVolume(t, g, "/dev/sda2", "swap-a")
	if _, err := g.Enable("/dev/sda2", impl.SwapRequest{Area: "swap-b"}); !errors.Is(err, impl.ErrDeviceBusy) {
		t.Fatalf("second volume on one device must fail with ErrDeviceBusy, got %v", err)
	}
	if got := len(g.Volumes()); got != 1 {
		t.Fatalf("live volumes = %d, want 1", got)
	}
}

func TestSwapOutAccumulatesPages(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdb1", "swap-a")
	mark, err := g.SwapOut(v.ID, 3)
	if err != nil {
		t.Fatalf("swap out must succeed, got %v", err)
	}
	if mark.VolumeID != v.ID || mark.Epoch != 1 || mark.Pages != 3 {
		t.Fatalf("page mark mismatch: %+v", mark)
	}
	if mark.Area != "swap-a" {
		t.Fatalf("page mark area = %q, want the primary area", mark.Area)
	}
	if _, err := g.SwapOut(v.ID, 2); err != nil {
		t.Fatalf("second swap out must succeed, got %v", err)
	}
	state, err := g.Verify(v.ID)
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if state.Pages != 5 {
		t.Fatalf("swapped pages = %d, want 5", state.Pages)
	}
	if state.Writes != 2 {
		t.Fatalf("write batches = %d, want 2", state.Writes)
	}
	if state.InFlight != 5 {
		t.Fatalf("in flight pages = %d, want 5", state.InFlight)
	}
}

func TestSwapOutRejectsBadPageCount(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdb2", "swap-a")
	if _, err := g.SwapOut(v.ID, 0); !errors.Is(err, impl.ErrBadPageCount) {
		t.Fatalf("zero pages must fail with ErrBadPageCount, got %v", err)
	}
	if _, err := g.SwapOut(v.ID, -4); !errors.Is(err, impl.ErrBadPageCount) {
		t.Fatalf("negative pages must fail with ErrBadPageCount, got %v", err)
	}
	state, _ := g.Verify(v.ID)
	if state.Pages != 0 || state.Writes != 0 {
		t.Fatalf("rejected swap out changed the accounting: %+v", state)
	}
}

func TestSwapInUnknownVolume(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	if err := g.SwapIn(impl.PageMark{VolumeID: "swap-404", Epoch: 1, Pages: 1}); !errors.Is(err, impl.ErrUnknownVolume) {
		t.Fatalf("swap in on an unknown volume must fail with ErrUnknownVolume, got %v", err)
	}
	v := enableVolume(t, g, "/dev/sdc1", "swap-a")
	if err := g.SwapIn(impl.PageMark{VolumeID: v.ID, Epoch: 7, Pages: 1}); !errors.Is(err, impl.ErrBadPageMark) {
		t.Fatalf("page mark from an epoch that never existed must fail with ErrBadPageMark, got %v", err)
	}
}

func TestRotateCreatesNewEpochAndSealsOld(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdc2", "swap-a")
	epoch, err := g.Rotate(v.ID)
	if err != nil {
		t.Fatalf("rotate must succeed, got %v", err)
	}
	if epoch.Epoch != 2 {
		t.Fatalf("rotated epoch = %d, want 2", epoch.Epoch)
	}
	if epoch.State != impl.EpochCurrent {
		t.Fatalf("new epoch state = %q, want %q", epoch.State, impl.EpochCurrent)
	}
	state, err := g.Verify(v.ID)
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if state.Epoch != 2 {
		t.Fatalf("current epoch = %d, want 2", state.Epoch)
	}
	if len(state.Epochs) != 2 {
		t.Fatalf("epoch history holds %d entries, want 2", len(state.Epochs))
	}
	if state.Epochs[0].Epoch != 1 || state.Epochs[0].State != impl.EpochSealed {
		t.Fatalf("previous epoch = %+v, want epoch 1 sealed", state.Epochs[0])
	}
	if state.Epochs[1].State != impl.EpochCurrent {
		t.Fatalf("current epoch entry = %+v, want current", state.Epochs[1])
	}
}

func TestRotateRejectsWhileInFlight(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdd1", "swap-a")
	if _, err := g.Rotate(v.ID); err != nil {
		t.Fatalf("first rotate must succeed, got %v", err)
	}
	if _, err := g.Rotate(v.ID); !errors.Is(err, impl.ErrRotationInFlight) {
		t.Fatalf("second rotate before the drain must fail with ErrRotationInFlight, got %v", err)
	}
	state, _ := g.Verify(v.ID)
	if state.Epoch != 2 {
		t.Fatalf("rejected rotate moved the epoch to %d, want 2", state.Epoch)
	}
	if _, err := g.FlushInFlight(v.ID); err != nil {
		t.Fatalf("flush must succeed, got %v", err)
	}
	epoch, err := g.Rotate(v.ID)
	if err != nil {
		t.Fatalf("rotate after the drain must succeed, got %v", err)
	}
	if epoch.Epoch != 3 {
		t.Fatalf("epoch after the second rotation = %d, want 3", epoch.Epoch)
	}
}

func TestFlushDrainsInFlight(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdd2", "swap-a")
	if _, err := g.SwapOut(v.ID, 3); err != nil {
		t.Fatalf("swap out must succeed, got %v", err)
	}
	drained, err := g.FlushInFlight(v.ID)
	if err != nil {
		t.Fatalf("flush must succeed, got %v", err)
	}
	if drained != 3 {
		t.Fatalf("flushed pages = %d, want 3", drained)
	}
	state, _ := g.Verify(v.ID)
	if state.InFlight != 0 {
		t.Fatalf("in flight pages after flush = %d, want 0", state.InFlight)
	}
	if state.Pages != 3 {
		t.Fatalf("swapped pages after flush = %d, want the pages to stay charged", state.Pages)
	}
	if again, err := g.FlushInFlight(v.ID); err != nil || again != 0 {
		t.Fatalf("second flush returned %d, %v; want 0, nil", again, err)
	}
}

func TestSuspendBlocksWritesAndResumeRestores(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sde1", "swap-a")
	if err := g.Suspend(v.ID); err != nil {
		t.Fatalf("suspend must succeed, got %v", err)
	}
	if _, err := g.SwapOut(v.ID, 1); !errors.Is(err, impl.ErrVolumeSuspended) {
		t.Fatalf("swap out on a suspended volume must fail with ErrVolumeSuspended, got %v", err)
	}
	if err := g.Suspend(v.ID); !errors.Is(err, impl.ErrVolumeSuspended) {
		t.Fatalf("suspending twice must fail with ErrVolumeSuspended, got %v", err)
	}
	if err := g.Resume(v.ID); err != nil {
		t.Fatalf("resume must succeed, got %v", err)
	}
	if err := g.Resume(v.ID); !errors.Is(err, impl.ErrVolumeNotSuspended) {
		t.Fatalf("resuming an active volume must fail with ErrVolumeNotSuspended, got %v", err)
	}
	if _, err := g.SwapOut(v.ID, 1); err != nil {
		t.Fatalf("swap out after resume must succeed, got %v", err)
	}
	if err := g.Suspend("swap-404"); !errors.Is(err, impl.ErrUnknownVolume) {
		t.Fatalf("suspending an unknown volume must fail with ErrUnknownVolume, got %v", err)
	}
}

func TestSuspendKeepsPagesWithoutPlaintext(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sde2", "swap-a")
	if _, err := g.SwapOut(v.ID, 4); err != nil {
		t.Fatalf("swap out must succeed, got %v", err)
	}
	if err := g.Suspend(v.ID); err != nil {
		t.Fatalf("suspend must succeed, got %v", err)
	}
	state, err := g.Verify(v.ID)
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if state.State != impl.VolumeSuspended {
		t.Fatalf("volume state = %q, want %q", state.State, impl.VolumeSuspended)
	}
	if state.Pages != 4 {
		t.Fatalf("pages after suspend = %d, want the in flight pages to survive", state.Pages)
	}
	if state.InFlight != 0 {
		t.Fatalf("in flight pages after suspend = %d, want 0", state.InFlight)
	}
	if state.PlaintextPages != 0 {
		t.Fatalf("suspend wrote %d pages in plaintext, want 0", state.PlaintextPages)
	}
}

func TestConcurrentRotateSingleWinner(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdf1", "swap-a")
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := make(chan struct{})
	won, inflight, other := 0, 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-started
			_, err := g.Rotate(v.ID)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, impl.ErrRotationInFlight):
				inflight++
			default:
				other++
			}
		}()
	}
	close(started)
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d concurrent rotations succeeded, want exactly 1", won)
	}
	if inflight != workers-1 {
		t.Fatalf("%d rotations reported ErrRotationInFlight, want %d", inflight, workers-1)
	}
	if other != 0 {
		t.Fatalf("%d rotations failed with an unexpected error", other)
	}
	state, _ := g.Verify(v.ID)
	if state.Epoch != 2 {
		t.Fatalf("epoch after the race = %d, want 2", state.Epoch)
	}
}

func TestEpochKeyIsDeterministicAndSecret(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdg1", "swap-a")
	first, err := g.EpochKey(v.ID, 1)
	if err != nil {
		t.Fatalf("epoch key must be derivable, got %v", err)
	}
	if len(first) != 16 {
		t.Fatalf("epoch key length = %d, want 16 hex characters", len(first))
	}
	again, err := g.EpochKey(v.ID, 1)
	if err != nil {
		t.Fatalf("second derivation must succeed, got %v", err)
	}
	if again != first {
		t.Fatalf("epoch key is not stable: %q vs %q", again, first)
	}
	if _, err := g.Rotate(v.ID); err != nil {
		t.Fatalf("rotate must succeed, got %v", err)
	}
	next, err := g.EpochKey(v.ID, 2)
	if err != nil {
		t.Fatalf("new epoch key must be derivable, got %v", err)
	}
	if next == first {
		t.Fatalf("a new epoch reused the previous key %q", next)
	}
	if _, err := g.EpochKey("swap-404", 1); !errors.Is(err, impl.ErrUnknownVolume) {
		t.Fatalf("key of an unknown volume must fail with ErrUnknownVolume, got %v", err)
	}
}

func TestAreaAttachmentAccounting(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdg2", "swap-a")
	if err := g.AttachArea(v.ID, "swap-b"); err != nil {
		t.Fatalf("attaching a second area must succeed, got %v", err)
	}
	if err := g.AttachArea(v.ID, "swap-b"); !errors.Is(err, impl.ErrAreaExists) {
		t.Fatalf("attaching one area twice must fail with ErrAreaExists, got %v", err)
	}
	if _, err := g.SwapOut(v.ID, 2); err != nil {
		t.Fatalf("swap out must succeed, got %v", err)
	}
	primary, err := g.AreaStats(v.ID, "swap-a")
	if err != nil {
		t.Fatalf("area stats must succeed, got %v", err)
	}
	if primary != 2 {
		t.Fatalf("primary area pages = %d, want 2", primary)
	}
	secondary, err := g.AreaStats(v.ID, "swap-b")
	if err != nil {
		t.Fatalf("area stats on the idle area must succeed, got %v", err)
	}
	if secondary != 0 {
		t.Fatalf("idle area pages = %d, want 0", secondary)
	}
	if _, err := g.AreaStats(v.ID, "swap-zz"); !errors.Is(err, impl.ErrUnknownArea) {
		t.Fatalf("stats of an unknown area must fail with ErrUnknownArea, got %v", err)
	}
}

func TestSnapshotRestoreKeepsEpochsAndPages(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdh1", "swap-a")
	if _, err := g.SwapOut(v.ID, 5); err != nil {
		t.Fatalf("swap out must succeed, got %v", err)
	}
	key, err := g.EpochKey(v.ID, 1)
	if err != nil {
		t.Fatalf("epoch key must be derivable, got %v", err)
	}
	if _, err := g.Rotate(v.ID); err != nil {
		t.Fatalf("rotate must succeed, got %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	if bytes.Contains(data, []byte(key)) {
		t.Fatalf("snapshot leaked key material for epoch 1")
	}
	restored := impl.NewSwapEncryptionGuard(swapConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	state, err := restored.Verify(v.ID)
	if err != nil {
		t.Fatalf("verify after restore must succeed, got %v", err)
	}
	if state.Device != "/dev/sdh1" || state.Epoch != 2 || state.Pages != 5 {
		t.Fatalf("restored volume state mismatch: %+v", state)
	}
	if len(state.Epochs) != 2 || state.Epochs[0].State != impl.EpochSealed {
		t.Fatalf("restored epoch history mismatch: %+v", state.Epochs)
	}
	next := enableVolume(t, restored, "/dev/sdh2", "swap-c")
	if next.ID == v.ID {
		t.Fatalf("volume ids must not be reused after a restore")
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewSwapEncryptionGuard(swapConfig())
	v := enableVolume(t, g, "/dev/sdi1", "swap-a")
	if _, err := g.SwapOut(v.ID, 2); err != nil {
		t.Fatalf("swap out must succeed, got %v", err)
	}
	if err := g.Restore([]byte("[not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	state, err := g.Verify(v.ID)
	if err != nil {
		t.Fatalf("verify after a rejected restore must succeed, got %v", err)
	}
	if state.Pages != 2 || state.Epoch != 1 || state.State != impl.VolumeActive {
		t.Fatalf("failed restore touched the live volume: %+v", state)
	}
	if _, err := g.Rotate(v.ID); err != nil {
		t.Fatalf("live volume must keep working after the rejected restore, got %v", err)
	}
}
