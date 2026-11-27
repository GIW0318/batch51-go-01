package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-08"
)

type clock struct {
	now int64
}

func (c *clock) Now() int64 { return c.now }

func guardConfig(clk *clock) impl.SwapPolicy {
	return impl.SwapPolicy{
		MaxDevices:            4,
		MaxKeyLifetimeSeconds: 600,
		MaxMetaCopies:         2,
		Now:                   clk.Now,
		Probe:                 func(string) error { return nil },
	}
}

func policyFor(deviceID, action string) impl.DevicePolicy {
	return impl.DevicePolicy{DeviceID: deviceID, Version: 2, KDF: "argon2id", HibernateAction: action}
}

func TestEnableReturnsActiveDeviceWithKey(t *testing.T) {
	clk := &clock{now: 100}
	g := impl.NewSwapKeyGuard(guardConfig(clk))
	st, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard))
	if err != nil {
		t.Fatalf("enable must succeed, got %v", err)
	}
	if st.State != impl.SwapActive {
		t.Fatalf("state = %q, want %q", st.State, impl.SwapActive)
	}
	if st.KeyHandle == "" {
		t.Fatalf("an enabled device must hold a key handle")
	}
	if st.KeyEpoch != 1 {
		t.Fatalf("key epoch = %d, want the first generation", st.KeyEpoch)
	}
	if st.DeviceID != "disk-sn-1" {
		t.Fatalf("device id = %q, want the identity from the policy", st.DeviceID)
	}
	if st.MetaCopies <= 0 || st.MetaCopies > 2 {
		t.Fatalf("metadata copies = %d, want between one and the configured cap 2", st.MetaCopies)
	}
	if st.EnabledAt != 100 {
		t.Fatalf("enabled at = %d, want the injected clock 100", st.EnabledAt)
	}
	devices := g.Devices()
	if len(devices) != 1 || devices[0].Device != "/dev/sda2" {
		t.Fatalf("devices = %+v, want exactly /dev/sda2", devices)
	}
	read, ok := g.State("/dev/sda2")
	if !ok || read.KeyHandle != st.KeyHandle || read.State != impl.SwapActive {
		t.Fatalf("state read back = %+v ok=%v, want the enabled record", read, ok)
	}
	epoch, err := g.KeyEpoch("/dev/sda2")
	if err != nil {
		t.Fatalf("key epoch: %v", err)
	}
	if epoch != 1 {
		t.Fatalf("key epoch read back = %d, want 1", epoch)
	}
}

func TestEnableRejectsBlankDevice(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{}))
	if _, err := g.Enable("   ", policyFor("disk-sn-1", impl.HibernateDiscard)); !errors.Is(err, impl.ErrInvalidDevice) {
		t.Fatalf("blank device must fail with ErrInvalidDevice, got %v", err)
	}
	if _, err := g.Enable("", policyFor("disk-sn-1", impl.HibernateDiscard)); !errors.Is(err, impl.ErrInvalidDevice) {
		t.Fatalf("empty device must fail with ErrInvalidDevice, got %v", err)
	}
	if got := len(g.Devices()); got != 0 {
		t.Fatalf("rejected enables created %d devices, want 0", got)
	}
}

func TestEnableRejectsUnknownMetadataVersion(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{}))
	policy := policyFor("disk-sn-1", impl.HibernateDiscard)
	policy.Version = 99
	if _, err := g.Enable("/dev/sda2", policy); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("unknown header version must fail with ErrUnknownVersion, got %v", err)
	}
	if got := len(g.Devices()); got != 0 {
		t.Fatalf("rejected enable created %d devices, want 0", got)
	}
}

func TestEnableIsIdempotentForSameDevice(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{now: 10}))
	first, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard))
	if err != nil {
		t.Fatalf("first enable: %v", err)
	}
	second, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard))
	if err != nil {
		t.Fatalf("second enable must be idempotent, got %v", err)
	}
	if second.KeyHandle != first.KeyHandle {
		t.Fatalf("re-enabling the same device replaced the key")
	}
	if second.KeyEpoch != 1 {
		t.Fatalf("key epoch after a repeated enable = %d, want 1", second.KeyEpoch)
	}
	if got := len(g.Devices()); got != 1 {
		t.Fatalf("device table size = %d, want 1", got)
	}
}

func TestEnableRejectsDeviceReplacement(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{}))
	first, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := g.Enable("/dev/sda2", policyFor("disk-sn-9", impl.HibernateDiscard)); !errors.Is(err, impl.ErrDeviceReplaced) {
		t.Fatalf("a swapped device identity must fail with ErrDeviceReplaced, got %v", err)
	}
	read, ok := g.State("/dev/sda2")
	if !ok {
		t.Fatalf("the original device record must survive a rejected replacement")
	}
	if read.DeviceID != "disk-sn-1" || read.KeyHandle != first.KeyHandle || read.State != impl.SwapActive {
		t.Fatalf("record after rejected replacement = %+v, want the untouched original", read)
	}
}

func TestEnableEnforcesDeviceLimitConcurrently(t *testing.T) {
	cfg := guardConfig(&clock{})
	cfg.MaxDevices = 2
	g := impl.NewSwapKeyGuard(cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	enabled, limited := 0, 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dev := "/dev/disk" + string(rune('a'+i))
			_, err := g.Enable(dev, policyFor("sn-"+string(rune('a'+i)), impl.HibernateDiscard))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				enabled++
			case errors.Is(err, impl.ErrDeviceLimit):
				limited++
			}
		}(i)
	}
	wg.Wait()
	if enabled != 2 {
		t.Fatalf("concurrent enables admitted %d devices, want exactly the cap 2", enabled)
	}
	if limited != 8 {
		t.Fatalf("concurrent enables reported %d limit errors, want 8", limited)
	}
	if got := len(g.Devices()); got != 2 {
		t.Fatalf("device table size after the race = %d, want 2", got)
	}
}

func TestSuspendAndResumeKeepTheSameKey(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{now: 5}))
	enabled, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := g.Suspend(); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	suspended, ok := g.State("/dev/sda2")
	if !ok || suspended.State != impl.SwapSuspended {
		t.Fatalf("state after suspend = %+v ok=%v, want suspended", suspended, ok)
	}
	if suspended.KeyHandle != enabled.KeyHandle {
		t.Fatalf("suspend must keep the volume key resident")
	}
	if suspended.SuspendedAt != 5 {
		t.Fatalf("suspended at = %d, want the injected clock 5", suspended.SuspendedAt)
	}
	if err := g.Resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	resumed, ok := g.State("/dev/sda2")
	if !ok || resumed.State != impl.SwapActive {
		t.Fatalf("state after resume = %+v ok=%v, want active", resumed, ok)
	}
	if resumed.KeyHandle != enabled.KeyHandle || resumed.KeyEpoch != 1 {
		t.Fatalf("resume changed the key: %+v", resumed)
	}
}

func TestSuspendRequiresActiveState(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{}))
	if err := g.Suspend(); !errors.Is(err, impl.ErrNotActive) {
		t.Fatalf("suspend without devices must fail with ErrNotActive, got %v", err)
	}
	if _, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard)); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := g.Suspend(); err != nil {
		t.Fatalf("first suspend: %v", err)
	}
	if err := g.Suspend(); !errors.Is(err, impl.ErrNotActive) {
		t.Fatalf("suspending twice must fail with ErrNotActive, got %v", err)
	}
}

func TestResumeRequiresSuspendedState(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{}))
	if err := g.Resume(); !errors.Is(err, impl.ErrNotActive) {
		t.Fatalf("resume without devices must fail with ErrNotActive, got %v", err)
	}
	if _, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard)); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := g.Resume(); !errors.Is(err, impl.ErrNotActive) {
		t.Fatalf("resuming an active device must fail with ErrNotActive, got %v", err)
	}
	st, _ := g.State("/dev/sda2")
	if st.State != impl.SwapActive {
		t.Fatalf("state after rejected resume = %q, want active", st.State)
	}
}

func TestSuspendRejectsExpiredKey(t *testing.T) {
	clk := &clock{now: 0}
	cfg := guardConfig(clk)
	cfg.MaxKeyLifetimeSeconds = 60
	g := impl.NewSwapKeyGuard(cfg)
	if _, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard)); err != nil {
		t.Fatalf("enable: %v", err)
	}
	clk.now = 61
	if err := g.Suspend(); !errors.Is(err, impl.ErrKeyExpired) {
		t.Fatalf("suspending past the residency budget must fail with ErrKeyExpired, got %v", err)
	}
	st, ok := g.State("/dev/sda2")
	if !ok || st.State != impl.SwapExpired {
		t.Fatalf("state after key expiry = %+v ok=%v, want expired", st, ok)
	}
	if st.KeyHandle != "" {
		t.Fatalf("an expired device must not keep a usable key handle")
	}
}

func TestHibernateDiscardRegeneratesKeyOnResume(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{now: 1}))
	enabled, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := g.Suspend(); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := g.Hibernate(true); err != nil {
		t.Fatalf("hibernate: %v", err)
	}
	locked, ok := g.State("/dev/sda2")
	if !ok || locked.State != impl.SwapLocked {
		t.Fatalf("state after hibernate = %+v ok=%v, want locked", locked, ok)
	}
	if locked.KeyHandle != "" {
		t.Fatalf("hibernate with discard must drop the volume key")
	}
	if err := g.Resume(); err != nil {
		t.Fatalf("resume after a discarded hibernate must regenerate a key, got %v", err)
	}
	woken, ok := g.State("/dev/sda2")
	if !ok || woken.State != impl.SwapActive {
		t.Fatalf("state after resume = %+v ok=%v, want active", woken, ok)
	}
	if woken.KeyEpoch != enabled.KeyEpoch+1 {
		t.Fatalf("key epoch after wake = %d, want %d", woken.KeyEpoch, enabled.KeyEpoch+1)
	}
	if woken.KeyHandle == "" || woken.KeyHandle == enabled.KeyHandle {
		t.Fatalf("wake after discard must hold a freshly generated key")
	}
}

func TestHibernateRetainRequiresUnlock(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{}))
	enabled, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateRetain))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := g.Suspend(); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := g.Hibernate(false); err != nil {
		t.Fatalf("hibernate with retain: %v", err)
	}
	locked, _ := g.State("/dev/sda2")
	if locked.State != impl.SwapLocked {
		t.Fatalf("state after hibernate = %q, want locked", locked.State)
	}
	if locked.KeyHandle != enabled.KeyHandle {
		t.Fatalf("hibernate with retain must keep the same volume key")
	}
	if err := g.Resume(); !errors.Is(err, impl.ErrLocked) {
		t.Fatalf("resume with a retained key must fail with ErrLocked, got %v", err)
	}
	if err := g.Unlock("/dev/sda2"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	unlocked, _ := g.State("/dev/sda2")
	if unlocked.State != impl.SwapSuspended {
		t.Fatalf("state after unlock = %q, want suspended", unlocked.State)
	}
	if err := g.Resume(); err != nil {
		t.Fatalf("resume after unlock: %v", err)
	}
	woken, _ := g.State("/dev/sda2")
	if woken.State != impl.SwapActive || woken.KeyHandle != enabled.KeyHandle || woken.KeyEpoch != 1 {
		t.Fatalf("resume after unlock = %+v, want the original key", woken)
	}
}

func TestHibernateRequiresSuspendedDevices(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{}))
	enabled, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := g.Hibernate(true); !errors.Is(err, impl.ErrBusy) {
		t.Fatalf("hibernate with an active device must fail with ErrBusy, got %v", err)
	}
	st, _ := g.State("/dev/sda2")
	if st.State != impl.SwapActive || st.KeyHandle != enabled.KeyHandle {
		t.Fatalf("rejected hibernate touched the device: %+v", st)
	}
}

func TestDisableDropsDeviceAndKey(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{}))
	for _, dev := range []string{"/dev/sda2", "/dev/sdb2"} {
		if _, err := g.Enable(dev, policyFor("sn-"+dev, impl.HibernateDiscard)); err != nil {
			t.Fatalf("enable %s: %v", dev, err)
		}
	}
	if err := g.Disable("/dev/sda2"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	devices := g.Devices()
	if len(devices) != 1 || devices[0].Device != "/dev/sdb2" {
		t.Fatalf("devices after disable = %+v, want only /dev/sdb2", devices)
	}
	if _, ok := g.State("/dev/sda2"); ok {
		t.Fatalf("disabled device must be gone from the table")
	}
	if err := g.Disable("/dev/sda2"); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("disabling a dropped device must fail with ErrUnknownDevice, got %v", err)
	}
	if err := g.Disable("/dev/nope"); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("disabling an unknown device must fail with ErrUnknownDevice, got %v", err)
	}
}

func TestSnapshotRestoreLocksKeys(t *testing.T) {
	g := impl.NewSwapKeyGuard(guardConfig(&clock{now: 20}))
	enabled, err := g.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard))
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := g.Suspend(); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := g.Resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewSwapKeyGuard(guardConfig(&clock{now: 30}))
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	devices := restored.Devices()
	if len(devices) != 1 || devices[0].Device != "/dev/sda2" {
		t.Fatalf("restored devices = %+v, want /dev/sda2", devices)
	}
	if devices[0].State != impl.SwapLocked {
		t.Fatalf("restored state = %q, want locked because no key survives a restart", devices[0].State)
	}
	if devices[0].KeyEpoch != enabled.KeyEpoch {
		t.Fatalf("restored key epoch = %d, want %d", devices[0].KeyEpoch, enabled.KeyEpoch)
	}
	if err := restored.Resume(); !errors.Is(err, impl.ErrLocked) {
		t.Fatalf("resume after restore must fail with ErrLocked, got %v", err)
	}
	reEnabled, err := restored.Enable("/dev/sda2", policyFor("disk-sn-1", impl.HibernateDiscard))
	if err != nil {
		t.Fatalf("re-enabling a restored device: %v", err)
	}
	if reEnabled.State != impl.SwapActive || reEnabled.KeyEpoch != enabled.KeyEpoch+1 {
		t.Fatalf("re-enabled record = %+v, want active with epoch %d", reEnabled, enabled.KeyEpoch+1)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	after, ok := restored.State("/dev/sda2")
	if !ok || after.State != impl.SwapActive || after.KeyHandle != reEnabled.KeyHandle {
		t.Fatalf("rejected restore touched live state: %+v ok=%v", after, ok)
	}
}
