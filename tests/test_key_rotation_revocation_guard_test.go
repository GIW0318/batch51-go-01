package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-r-06"
)

const (
	testTenant   = "tenant-mall"
	materialOne  = "material-2026-01"
	materialTwo  = "material-2026-02"
	materialThree = "material-2026-03"
)

func testRotationConfig() impl.RotationConfig {
	return impl.RotationConfig{MaxVersions: 6, MaxAudit: 16, MaxInflight: 8}
}

func newTestRotationGuard(t *testing.T, tenant string, cfg impl.RotationConfig) *impl.KeyRotationRevocationGuard {
	t.Helper()
	g, err := impl.NewKeyRotationRevocationGuard(tenant, cfg)
	if err != nil {
		t.Fatalf("building the guard must succeed, got %v", err)
	}
	if g == nil {
		t.Fatalf("guard must not be nil")
	}
	return g
}

func mustRotate(t *testing.T, g *impl.KeyRotationRevocationGuard, version, material string, now int64) impl.KeyVersion {
	t.Helper()
	v, err := g.Rotate(version, []byte(material), now)
	if err != nil {
		t.Fatalf("rotating %s must succeed, got %v", version, err)
	}
	return v
}

func TestNewGuardRejectsBlankTenant(t *testing.T) {
	if _, err := impl.NewKeyRotationRevocationGuard("   ", testRotationConfig()); !errors.Is(err, impl.ErrBadTenant) {
		t.Fatalf("blank tenant must be rejected with ErrBadTenant, got %v", err)
	}
	if _, err := impl.NewKeyRotationRevocationGuard("", testRotationConfig()); !errors.Is(err, impl.ErrBadTenant) {
		t.Fatalf("empty tenant must be rejected with ErrBadTenant, got %v", err)
	}
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	if got := g.Current(); got != "" {
		t.Fatalf("fresh guard current version = %q, want empty", got)
	}
	if got := len(g.Versions()); got != 0 {
		t.Fatalf("fresh guard version count = %d, want 0", got)
	}
}

func TestNewGuardRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  impl.RotationConfig
	}{
		{"version cap", impl.RotationConfig{MaxVersions: 0, MaxAudit: 4, MaxInflight: 4}},
		{"audit cap", impl.RotationConfig{MaxVersions: 4, MaxAudit: -1, MaxInflight: 4}},
		{"in-flight cap", impl.RotationConfig{MaxVersions: 4, MaxAudit: 4, MaxInflight: 0}},
	}
	for _, c := range cases {
		if _, err := impl.NewKeyRotationRevocationGuard(testTenant, c.cfg); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("bad %s must be rejected with ErrBadConfig, got %v", c.name, err)
		}
	}
}

func TestRotateActivatesFirstVersion(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	v := mustRotate(t, g, "k-2026-01", materialOne, 1000)
	if v.Version != "k-2026-01" {
		t.Fatalf("rotated version label = %q, want k-2026-01", v.Version)
	}
	if v.State != impl.VersionActive {
		t.Fatalf("first version state = %q, want %q", v.State, impl.VersionActive)
	}
	if v.Refs != 0 {
		t.Fatalf("fresh version refs = %d, want 0", v.Refs)
	}
	if v.RotatedAt != 1000 {
		t.Fatalf("rotatedAt = %d, want 1000", v.RotatedAt)
	}
	if got := g.Current(); got != "k-2026-01" {
		t.Fatalf("current version = %q, want k-2026-01", got)
	}
	versions := g.Versions()
	if len(versions) != 1 || versions[0].Version != "k-2026-01" {
		t.Fatalf("versions = %+v, want one entry for k-2026-01", versions)
	}
	stats := g.Stats()
	if stats.Rotations != 1 || stats.Versions != 1 {
		t.Fatalf("stats = %+v, want rotations=1 versions=1", stats)
	}
	events := g.Events()
	if len(events) != 1 {
		t.Fatalf("event log length = %d, want 1", len(events))
	}
	if events[0].Action != impl.ActionRotated || events[0].Version != "k-2026-01" {
		t.Fatalf("event = %+v, want a rotation of k-2026-01", events[0])
	}
	if events[0].Seq <= 0 {
		t.Fatalf("event seq = %d, want a positive number", events[0].Seq)
	}
}

func TestRotateRejectsBlankVersionOrMaterial(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	if _, err := g.Rotate("   ", []byte(materialOne), 1000); !errors.Is(err, impl.ErrBadVersion) {
		t.Fatalf("blank version label must report ErrBadVersion, got %v", err)
	}
	if _, err := g.Rotate("k-2026-01", nil, 1000); !errors.Is(err, impl.ErrBadMaterial) {
		t.Fatalf("nil material must report ErrBadMaterial, got %v", err)
	}
	if _, err := g.Rotate("k-2026-01", []byte{}, 1000); !errors.Is(err, impl.ErrBadMaterial) {
		t.Fatalf("empty material must report ErrBadMaterial, got %v", err)
	}
	if got := len(g.Versions()); got != 0 {
		t.Fatalf("rejected rotations created %d versions, want 0", got)
	}
	if got := g.Current(); got != "" {
		t.Fatalf("rejected rotations changed the current version to %q", got)
	}
}

func TestRotateMovesPreviousToRotating(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	mustRotate(t, g, "k-2026-02", materialTwo, 2000)
	if got := g.Current(); got != "k-2026-02" {
		t.Fatalf("current version = %q, want k-2026-02", got)
	}
	versions := g.Versions()
	if len(versions) != 2 {
		t.Fatalf("version count = %d, want 2", len(versions))
	}
	if versions[0].Version != "k-2026-01" || versions[1].Version != "k-2026-02" {
		t.Fatalf("versions must stay in rotation order, got %+v", versions)
	}
	if versions[0].State != impl.VersionRotating {
		t.Fatalf("previous version state = %q, want %q", versions[0].State, impl.VersionRotating)
	}
	if versions[1].State != impl.VersionActive {
		t.Fatalf("newest version state = %q, want %q", versions[1].State, impl.VersionActive)
	}
	if got := g.Stats().Rotations; got != 2 {
		t.Fatalf("rotation counter = %d, want 2", got)
	}
}

func TestRotateRejectsConflictingMaterial(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	if _, err := g.Rotate("k-2026-01", []byte(materialTwo), 1100); !errors.Is(err, impl.ErrVersionConflict) {
		t.Fatalf("re-using a label with different material must report ErrVersionConflict, got %v", err)
	}
	if got := len(g.Versions()); got != 1 {
		t.Fatalf("version count = %d, want 1 after the refused rotation", got)
	}
	if got := g.Current(); got != "k-2026-01" {
		t.Fatalf("current version = %q, want k-2026-01", got)
	}
	if got := g.Stats().Rotations; got != 1 {
		t.Fatalf("rotation counter = %d, want 1 after the refused rotation", got)
	}
}

func TestResolveUnknownVersion(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	if _, err := g.Resolve("k-404"); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("resolving an unknown version must report ErrUnknownVersion, got %v", err)
	}
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	v, err := g.Resolve("k-2026-01")
	if err != nil {
		t.Fatalf("resolving the live version: %v", err)
	}
	if v.Version != "k-2026-01" || v.State != impl.VersionActive {
		t.Fatalf("resolved version = %+v, want the active k-2026-01", v)
	}
	if v.Refs != 0 {
		t.Fatalf("resolved refs = %d, want 0", v.Refs)
	}
}

func TestResolveRejectedAfterRevoke(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	if err := g.Revoke("k-2026-01", "material leaked to a partner", 1500); err != nil {
		t.Fatalf("revoking the live version: %v", err)
	}
	if _, err := g.Resolve("k-2026-01"); !errors.Is(err, impl.ErrVersionRevoked) {
		t.Fatalf("resolving a revoked version must report ErrVersionRevoked, got %v", err)
	}
	if got := g.Current(); got != "" {
		t.Fatalf("revoking the current version must leave no current version, got %q", got)
	}
	if got := g.Stats().Revoked; got != 1 {
		t.Fatalf("revoked counter = %d, want 1", got)
	}
	found := false
	for _, event := range g.Events() {
		if event.Action == impl.ActionRevoked && event.Version == "k-2026-01" {
			found = true
			if event.Reason != "material leaked to a partner" {
				t.Fatalf("revocation event reason = %q, want the operator note", event.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("revocation must be written to the event log, got %+v", g.Events())
	}
}

func TestRevokeRequiresReasonAndIsIdempotent(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	if err := g.Revoke("k-2026-01", "   ", 1200); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("revoking without a reason must report ErrBadReason, got %v", err)
	}
	if _, err := g.Resolve("k-2026-01"); err != nil {
		t.Fatalf("a refused revocation must leave the version usable, got %v", err)
	}
	if err := g.Revoke("k-2026-01", "stolen laptop", 1300); err != nil {
		t.Fatalf("first revocation: %v", err)
	}
	if err := g.Revoke("k-2026-01", "stolen laptop again", 1400); err != nil {
		t.Fatalf("revoking an already revoked version must be idempotent, got %v", err)
	}
	if err := g.Revoke("k-404", "unknown", 1400); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("revoking an unknown version must report ErrUnknownVersion, got %v", err)
	}
	if got := g.Stats().Revoked; got != 1 {
		t.Fatalf("revoked counter = %d, want 1 after the repeat call", got)
	}
}

func TestAcquireReleaseTracksRefs(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	first, err := g.Acquire("k-2026-01")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if first.ID == "" {
		t.Fatalf("lease id must not be empty")
	}
	if first.Version != "k-2026-01" {
		t.Fatalf("lease version = %q, want k-2026-01", first.Version)
	}
	if first.Closed {
		t.Fatalf("a fresh lease must not be closed")
	}
	second, err := g.Acquire("k-2026-01")
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("two leases must not share the id %q", first.ID)
	}
	if got := g.RefCount("k-2026-01"); got != 2 {
		t.Fatalf("ref count = %d, want 2", got)
	}
	if err := g.Release(first); err != nil {
		t.Fatalf("releasing the first lease: %v", err)
	}
	if got := g.RefCount("k-2026-01"); got != 1 {
		t.Fatalf("ref count = %d, want 1", got)
	}
	if err := g.Release(first); !errors.Is(err, impl.ErrLeaseClosed) {
		t.Fatalf("releasing the same lease twice must report ErrLeaseClosed, got %v", err)
	}
	if got := g.RefCount("k-2026-01"); got != 1 {
		t.Fatalf("a refused release changed the ref count to %d, want 1", got)
	}
	if err := g.Release(second); err != nil {
		t.Fatalf("releasing the second lease: %v", err)
	}
	if got := g.RefCount("k-2026-01"); got != 0 {
		t.Fatalf("ref count = %d, want 0", got)
	}
	if err := g.Release(impl.Lease{ID: "lease-404", Version: "k-2026-01"}); !errors.Is(err, impl.ErrUnknownLease) {
		t.Fatalf("releasing an unknown lease must report ErrUnknownLease, got %v", err)
	}
	if got := g.Stats().Inflight; got != 0 {
		t.Fatalf("in-flight counter = %d, want 0", got)
	}
}

func TestRetireRejectsCurrentVersion(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	if err := g.Retire("k-2026-01"); !errors.Is(err, impl.ErrCurrentVersion) {
		t.Fatalf("retiring the current version must report ErrCurrentVersion, got %v", err)
	}
	mustRotate(t, g, "k-2026-02", materialTwo, 2000)
	if err := g.Retire("k-2026-01"); err != nil {
		t.Fatalf("retiring a previous version: %v", err)
	}
	if err := g.Retire("k-2026-01"); err != nil {
		t.Fatalf("retiring the same version twice must be idempotent, got %v", err)
	}
	if _, err := g.Resolve("k-2026-01"); !errors.Is(err, impl.ErrVersionRetired) {
		t.Fatalf("resolving an unreferenced retired version must report ErrVersionRetired, got %v", err)
	}
	if err := g.Retire("k-404"); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("retiring an unknown version must report ErrUnknownVersion, got %v", err)
	}
	if got := g.Stats().Retired; got != 1 {
		t.Fatalf("retired counter = %d, want 1", got)
	}
	if got := g.Current(); got != "k-2026-02" {
		t.Fatalf("current version = %q, want k-2026-02", got)
	}
}

func TestRotateEnforcesVersionLimit(t *testing.T) {
	cfg := impl.RotationConfig{MaxVersions: 2, MaxAudit: 16, MaxInflight: 8}
	g := newTestRotationGuard(t, testTenant, cfg)
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	mustRotate(t, g, "k-2026-02", materialTwo, 2000)
	if _, err := g.Rotate("k-2026-03", []byte(materialThree), 3000); !errors.Is(err, impl.ErrVersionLimit) {
		t.Fatalf("rotating past the version cap must report ErrVersionLimit, got %v", err)
	}
	if got := len(g.Versions()); got != 2 {
		t.Fatalf("version count = %d, want the configured cap 2", got)
	}
	if got := g.Current(); got != "k-2026-02" {
		t.Fatalf("a refused rotation changed the current version to %q", got)
	}
	if got := g.Stats().Rotations; got != 2 {
		t.Fatalf("rotation counter = %d, want 2", got)
	}
}

func TestConcurrentAcquireRespectsInflightLimit(t *testing.T) {
	cfg := impl.RotationConfig{MaxVersions: 4, MaxAudit: 16, MaxInflight: 4}
	g := newTestRotationGuard(t, testTenant, cfg)
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted, refused := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := g.Acquire("k-2026-01")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				granted++
			case errors.Is(err, impl.ErrInflightLimit):
				refused++
			}
		}(i)
	}
	wg.Wait()
	if granted != 4 {
		t.Fatalf("concurrent acquires granted %d leases, want exactly the cap 4", granted)
	}
	if refused != 12 {
		t.Fatalf("concurrent acquires refused %d leases, want 12", refused)
	}
	if got := g.RefCount("k-2026-01"); got != 4 {
		t.Fatalf("ref count after the race = %d, want 4", got)
	}
	if got := g.Stats().Inflight; got != 4 {
		t.Fatalf("in-flight counter after the race = %d, want 4", got)
	}
}

func TestSnapshotRestoreKeepsCurrentAndRefs(t *testing.T) {
	cfg := testRotationConfig()
	g := newTestRotationGuard(t, testTenant, cfg)
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	oldLease, err := g.Acquire("k-2026-01")
	if err != nil {
		t.Fatalf("acquire on the first version: %v", err)
	}
	mustRotate(t, g, "k-2026-02", materialTwo, 2000)
	if _, err := g.Acquire("k-2026-02"); err != nil {
		t.Fatalf("acquire on the second version: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newTestRotationGuard(t, testTenant, cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Current(); got != "k-2026-02" {
		t.Fatalf("restored current version = %q, want k-2026-02", got)
	}
	if got := len(restored.Versions()); got != 2 {
		t.Fatalf("restored version count = %d, want 2", got)
	}
	if got := restored.RefCount("k-2026-01"); got != 1 {
		t.Fatalf("restored refs of the rotating version = %d, want 1", got)
	}
	if got := restored.RefCount("k-2026-02"); got != 1 {
		t.Fatalf("restored refs of the current version = %d, want 1", got)
	}
	stats := restored.Stats()
	if stats.Rotations != 2 || stats.Inflight != 2 {
		t.Fatalf("restored stats = %+v, want rotations=2 inflight=2", stats)
	}
	if err := restored.Release(oldLease); err != nil {
		t.Fatalf("releasing the lease carried over the restart: %v", err)
	}
	if got := restored.RefCount("k-2026-01"); got != 0 {
		t.Fatalf("ref count after the release = %d, want 0", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newTestRotationGuard(t, testTenant, testRotationConfig())
	mustRotate(t, g, "k-2026-01", materialOne, 1000)
	if _, err := g.Acquire("k-2026-01"); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := g.Restore([]byte("}{ not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if got := g.Current(); got != "k-2026-01" {
		t.Fatalf("failed restore changed the current version to %q", got)
	}
	if got := len(g.Versions()); got != 1 {
		t.Fatalf("failed restore changed the version count to %d", got)
	}
	if got := g.RefCount("k-2026-01"); got != 1 {
		t.Fatalf("failed restore changed the ref count to %d", got)
	}
	if got := g.Stats().Rotations; got != 1 {
		t.Fatalf("failed restore reset the rotation counter to %d", got)
	}
}
