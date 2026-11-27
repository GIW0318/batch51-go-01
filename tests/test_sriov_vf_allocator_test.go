package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-d-01"
)

func allocConfig() impl.AllocatorConfig {
	return impl.AllocatorConfig{
		MaxVfsPerPF:     8,
		MaxVfsPerTenant: 4,
		MaxVlansPerVf:   3,
		PfMacs:          map[string]string{"pf0": "aa:bb:cc:00:00:01"},
	}
}

func mustEnable(t *testing.T, a *impl.SriovVfAllocator, pf string, count int) []impl.VF {
	t.Helper()
	vfs, err := a.Enable(pf, count)
	if err != nil {
		t.Fatalf("enable %s with %d functions: %v", pf, count, err)
	}
	return vfs
}

func TestEnableCreatesOrderedFunctions(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	vfs := mustEnable(t, a, "pf0", 3)
	if len(vfs) != 3 {
		t.Fatalf("enabled functions = %d, want 3", len(vfs))
	}
	for i, vf := range vfs {
		want := fmt.Sprintf("pf0:vf%d", i)
		if vf.ID != want {
			t.Fatalf("function %d id = %q, want %q", i, vf.ID, want)
		}
		if vf.Index != i {
			t.Fatalf("function %d index = %d, want %d", i, vf.Index, i)
		}
		if vf.PF != "pf0" {
			t.Fatalf("function %d pf = %q, want pf0", i, vf.PF)
		}
		if vf.State != impl.VFIdle {
			t.Fatalf("function %d state = %q, want %q", i, vf.State, impl.VFIdle)
		}
		if vf.Generation != 0 {
			t.Fatalf("fresh function %d generation = %d, want 0", i, vf.Generation)
		}
	}
	if got := a.FreeCount("pf0"); got != 3 {
		t.Fatalf("free functions = %d, want 3", got)
	}
	if got := len(a.Vfs("pf0")); got != 3 {
		t.Fatalf("listed functions = %d, want 3", got)
	}
	if got := a.FreeCount("pf-missing"); got != 0 {
		t.Fatalf("free functions of an unknown pf = %d, want 0", got)
	}
}

func TestEnableRejectsBadCount(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	for _, count := range []int{0, -1, 9} {
		if _, err := a.Enable("pf0", count); !errors.Is(err, impl.ErrBadCount) {
			t.Fatalf("enable with count %d must fail with ErrBadCount, got %v", count, err)
		}
	}
	if _, err := a.Enable("   ", 1); !errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("enable with a blank pf must fail with ErrBadRequest, got %v", err)
	}
	if got := len(a.Vfs("pf0")); got != 0 {
		t.Fatalf("rejected enables left %d functions, want 0", got)
	}
}

func TestEnableSameCountKeepsFunctions(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	first := mustEnable(t, a, "pf0", 2)
	again := mustEnable(t, a, "pf0", 2)
	if len(again) != 2 {
		t.Fatalf("re-enable returned %d functions, want 2", len(again))
	}
	for i := range first {
		if first[i].ID != again[i].ID {
			t.Fatalf("re-enable rebuilt function %d: %q became %q", i, first[i].ID, again[i].ID)
		}
	}
	grown := mustEnable(t, a, "pf0", 4)
	if len(grown) != 4 {
		t.Fatalf("grown pool has %d functions, want 4", len(grown))
	}
	if grown[2].ID != "pf0:vf2" || grown[3].ID != "pf0:vf3" {
		t.Fatalf("growth must append vf2 and vf3, got %q and %q", grown[2].ID, grown[3].ID)
	}
	if got := a.FreeCount("pf0"); got != 4 {
		t.Fatalf("free functions after growth = %d, want 4", got)
	}
}

func TestAllocateLeasesLowestIndex(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 2)
	lease, err := a.Allocate("pf0", impl.VFRequest{
		Tenant: "tenant-a",
		MAC:    "02:00:00:00:01:01",
		VLANs:  []uint16{100, 200},
	})
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if lease.VFID != "pf0:vf0" {
		t.Fatalf("leased function = %q, want the lowest free index pf0:vf0", lease.VFID)
	}
	if lease.PF != "pf0" || lease.Tenant != "tenant-a" {
		t.Fatalf("lease pf=%q tenant=%q, want pf0/tenant-a", lease.PF, lease.Tenant)
	}
	if lease.MAC != "02:00:00:00:01:01" {
		t.Fatalf("lease mac = %q, want the requested address", lease.MAC)
	}
	if len(lease.VLANs) != 2 || lease.VLANs[0] != 100 || lease.VLANs[1] != 200 {
		t.Fatalf("lease vlans = %v, want [100 200]", lease.VLANs)
	}
	if lease.Trust {
		t.Fatalf("lease trust = true, want the requested false")
	}
	if lease.Generation != 1 {
		t.Fatalf("lease generation = %d, want 1 after the first lease", lease.Generation)
	}
	vfs := a.Vfs("pf0")
	if vfs[0].State != impl.VFLeased {
		t.Fatalf("function 0 state = %q, want %q", vfs[0].State, impl.VFLeased)
	}
	if vfs[1].State != impl.VFIdle {
		t.Fatalf("function 1 state = %q, want %q", vfs[1].State, impl.VFIdle)
	}
	if got := a.FreeCount("pf0"); got != 1 {
		t.Fatalf("free functions = %d, want 1", got)
	}
	second, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-b"})
	if err != nil {
		t.Fatalf("allocate second function: %v", err)
	}
	if second.VFID != "pf0:vf1" {
		t.Fatalf("second lease = %q, want pf0:vf1", second.VFID)
	}
}

func TestAllocateRejectsBadRequest(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 2)
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "   "}); !errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("blank tenant must fail with ErrBadRequest, got %v", err)
	}
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "t", VLANs: []uint16{1, 2, 3, 4}}); !errors.Is(err, impl.ErrVlanLimit) {
		t.Fatalf("four vlans must fail with ErrVlanLimit, got %v", err)
	}
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "t", VLANs: []uint16{5, 5}}); !errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("duplicate vlans must fail with ErrBadRequest, got %v", err)
	}
	if _, err := a.Allocate("pf9", impl.VFRequest{Tenant: "t"}); !errors.Is(err, impl.ErrUnknownPF) {
		t.Fatalf("allocation on an unknown pf must fail with ErrUnknownPF, got %v", err)
	}
	if got := a.FreeCount("pf0"); got != 2 {
		t.Fatalf("rejected allocations consumed functions, free = %d, want 2", got)
	}
}

func TestAllocateEnforcesTenantQuota(t *testing.T) {
	cfg := allocConfig()
	cfg.MaxVfsPerTenant = 1
	a := impl.NewSriovVfAllocator(cfg)
	mustEnable(t, a, "pf0", 3)
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-a"}); err != nil {
		t.Fatalf("first lease of tenant-a: %v", err)
	}
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-a"}); !errors.Is(err, impl.ErrTenantQuota) {
		t.Fatalf("second lease must hit the tenant quota, got %v", err)
	}
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-b"}); err != nil {
		t.Fatalf("another tenant must still get a function, got %v", err)
	}
	if got := a.FreeCount("pf0"); got != 1 {
		t.Fatalf("free functions = %d, want 1", got)
	}
}

func TestAllocateExhaustedPool(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 2)
	for i := 0; i < 2; i++ {
		if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: fmt.Sprintf("tenant-%d", i)}); err != nil {
			t.Fatalf("lease %d: %v", i, err)
		}
	}
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-x"}); !errors.Is(err, impl.ErrNoFreeVF) {
		t.Fatalf("allocation on a full pool must fail with ErrNoFreeVF, got %v", err)
	}
	if got := a.FreeCount("pf0"); got != 0 {
		t.Fatalf("free functions = %d, want 0", got)
	}
}

func TestConcurrentAllocateHasSingleWinner(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 1)
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners, losers := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := a.Allocate("pf0", impl.VFRequest{
				Tenant: "tenant-c",
				MAC:    fmt.Sprintf("02:00:00:00:0c:%02x", i),
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, impl.ErrNoFreeVF):
				losers++
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("concurrent allocations produced %d winners, want exactly 1", winners)
	}
	if losers != 15 {
		t.Fatalf("concurrent allocations produced %d empty pool errors, want 15", losers)
	}
	if got := a.FreeCount("pf0"); got != 0 {
		t.Fatalf("free functions = %d, want 0", got)
	}
}

func TestConfigureRequiresTrustForMacChange(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 1)
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-a", MAC: "02:00:00:00:01:01"}); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if _, err := a.Configure("pf0:vf0", impl.VFConfig{MAC: "02:00:00:00:01:99"}); !errors.Is(err, impl.ErrTrustRequired) {
		t.Fatalf("mac change without trust must fail with ErrTrustRequired, got %v", err)
	}
	lease, ok := a.LeaseOf("pf0:vf0")
	if !ok {
		t.Fatalf("lease must stay readable after the rejected change")
	}
	if lease.MAC != "02:00:00:00:01:01" {
		t.Fatalf("lease mac = %q, want the original address", lease.MAC)
	}
	if lease.Generation != 1 {
		t.Fatalf("lease generation = %d, want the rejected change to leave 1", lease.Generation)
	}
}

func TestConfigureRequiresTrustForVlans(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 1)
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-a"}); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if _, err := a.Configure("pf0:vf0", impl.VFConfig{VLANs: []uint16{300}}); !errors.Is(err, impl.ErrTrustRequired) {
		t.Fatalf("vlan change without trust must fail with ErrTrustRequired, got %v", err)
	}
	lease, ok := a.LeaseOf("pf0:vf0")
	if !ok {
		t.Fatalf("lease must stay readable after the rejected change")
	}
	if len(lease.VLANs) != 0 {
		t.Fatalf("lease vlans = %v, want none after the rejected change", lease.VLANs)
	}
}

func TestConfigureUpdatesSpoofCheckAndGeneration(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 1)
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-a", Trust: true}); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	state, err := a.Configure("pf0:vf0", impl.VFConfig{
		MAC:      "02:00:00:00:02:02",
		VLANs:    []uint16{110, 120},
		SpoofChk: true,
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	if state.State != impl.VFConfigured {
		t.Fatalf("state = %q, want %q", state.State, impl.VFConfigured)
	}
	if state.MAC != "02:00:00:00:02:02" {
		t.Fatalf("state mac = %q, want the new address", state.MAC)
	}
	if len(state.VLANs) != 2 || state.VLANs[0] != 110 || state.VLANs[1] != 120 {
		t.Fatalf("state vlans = %v, want [110 120]", state.VLANs)
	}
	if !state.SpoofChk {
		t.Fatalf("state spoof check = false, want the requested true")
	}
	if state.Tenant != "tenant-a" {
		t.Fatalf("state tenant = %q, want tenant-a", state.Tenant)
	}
	if !state.Trust {
		t.Fatalf("state trust = false, want the lease trust bit")
	}
	if state.Generation != 2 {
		t.Fatalf("state generation = %d, want 2 after lease plus configure", state.Generation)
	}
	lease, ok := a.LeaseOf("pf0:vf0")
	if !ok {
		t.Fatalf("lease must stay readable after configuring")
	}
	if lease.MAC != "02:00:00:00:02:02" || len(lease.VLANs) != 2 || lease.Generation != 2 {
		t.Fatalf("lease after configure = %+v, want the new mac, vlans and generation 2", lease)
	}
}

func TestSetTrustAllowsReconfigure(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 2)
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-a", MAC: "02:00:00:00:03:03"}); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if err := a.SetTrust("pf0:vf0", true); err != nil {
		t.Fatalf("set trust: %v", err)
	}
	state, err := a.Configure("pf0:vf0", impl.VFConfig{MAC: "02:00:00:00:04:04"})
	if err != nil {
		t.Fatalf("configure after trust: %v", err)
	}
	if state.MAC != "02:00:00:00:04:04" {
		t.Fatalf("state mac = %q, want the trusted change to apply", state.MAC)
	}
	if state.Generation != 3 {
		t.Fatalf("state generation = %d, want 3 after lease, trust and configure", state.Generation)
	}
	if err := a.SetTrust("pf0:vf1", true); !errors.Is(err, impl.ErrVFNotLeased) {
		t.Fatalf("trust on an idle function must fail with ErrVFNotLeased, got %v", err)
	}
	if err := a.SetTrust("pf0:vf9", true); !errors.Is(err, impl.ErrVFNotFound) {
		t.Fatalf("trust on an unknown function must fail with ErrVFNotFound, got %v", err)
	}
}

func TestReleaseReturnsFunctionToPool(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 2)
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-a", MAC: "02:00:00:00:05:05"}); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if err := a.Release("pf0:vf0"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := a.FreeCount("pf0"); got != 2 {
		t.Fatalf("free functions after release = %d, want 2", got)
	}
	if _, ok := a.LeaseOf("pf0:vf0"); ok {
		t.Fatalf("released function must not keep a lease")
	}
	if got := a.Vfs("pf0")[0].State; got != impl.VFIdle {
		t.Fatalf("released function state = %q, want %q", got, impl.VFIdle)
	}
	again, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-b"})
	if err != nil {
		t.Fatalf("allocate after release: %v", err)
	}
	if again.VFID != "pf0:vf0" {
		t.Fatalf("released function must be reusable, got %q", again.VFID)
	}
	if again.Generation != 3 {
		t.Fatalf("generation after lease, release and lease = %d, want 3", again.Generation)
	}
	if err := a.Release("pf0:vf9"); !errors.Is(err, impl.ErrVFNotFound) {
		t.Fatalf("releasing an unknown function must fail with ErrVFNotFound, got %v", err)
	}
}

func TestSnapshotRestoreKeepsLeases(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 2)
	if _, err := a.Allocate("pf0", impl.VFRequest{Tenant: "tenant-a", Trust: true}); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if _, err := a.Configure("pf0:vf0", impl.VFConfig{MAC: "02:00:00:00:06:06", VLANs: []uint16{400}, SpoofChk: true}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewSriovVfAllocator(allocConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	lease, ok := restored.LeaseOf("pf0:vf0")
	if !ok {
		t.Fatalf("restored allocator lost the lease")
	}
	if lease.Tenant != "tenant-a" || lease.MAC != "02:00:00:00:06:06" || len(lease.VLANs) != 1 || lease.VLANs[0] != 400 {
		t.Fatalf("restored lease = %+v, want the saved tenant, mac and vlans", lease)
	}
	if lease.Generation != 2 {
		t.Fatalf("restored generation = %d, want 2", lease.Generation)
	}
	if got := restored.FreeCount("pf0"); got != 1 {
		t.Fatalf("restored free functions = %d, want 1", got)
	}
	next, err := restored.Allocate("pf0", impl.VFRequest{Tenant: "tenant-b"})
	if err != nil {
		t.Fatalf("allocate after restore: %v", err)
	}
	if next.VFID != "pf0:vf1" {
		t.Fatalf("allocation after restore = %q, want the still free pf0:vf1", next.VFID)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	a := impl.NewSriovVfAllocator(allocConfig())
	mustEnable(t, a, "pf0", 2)
	for _, payload := range [][]byte{nil, []byte("{not-a-snapshot")} {
		if err := a.Restore(payload); !errors.Is(err, impl.ErrBadSnapshot) {
			t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
		}
	}
	if got := len(a.Vfs("pf0")); got != 2 {
		t.Fatalf("failed restore changed the pool: %d functions, want 2", got)
	}
	if got := a.FreeCount("pf0"); got != 2 {
		t.Fatalf("failed restore changed the free count: %d, want 2", got)
	}
}
