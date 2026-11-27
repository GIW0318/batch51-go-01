package task

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-13"
)

var keyBase = time.Date(2026, 5, 11, 6, 0, 0, 0, time.UTC)

const keyPrincipal = "svc/host.example.com"

func testKeytabConfig() impl.KeytabConfig {
	return impl.KeytabConfig{
		Grace:       24 * time.Hour,
		MaxVersions: 8,
		Now:         func() time.Time { return keyBase },
	}
}

func installKey(t *testing.T, g *impl.KeytabRotationGuard, version int, enctype impl.Enctype, material string, at time.Time) impl.InstallResult {
	t.Helper()
	res, err := g.Install(impl.KeyEntry{
		Principal: keyPrincipal,
		Version:   version,
		Enctype:   enctype,
		Material:  material,
	}, at)
	if err != nil {
		t.Fatalf("install v%d/%d: %v", version, enctype, err)
	}
	return res
}

func promoteKey(t *testing.T, g *impl.KeytabRotationGuard, version int, at time.Time) {
	t.Helper()
	if err := g.Promote(keyPrincipal, version, at); err != nil {
		t.Fatalf("promote v%d: %v", version, err)
	}
}

func TestInstallStagesNewGeneration(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	res := installKey(t, g, 2, impl.EnctypeAES256, "material-two", keyBase)
	if !res.Entry.Staged {
		t.Fatalf("a freshly installed key = %+v, want it staged", res.Entry)
	}
	if res.Entry.Version != 2 || res.Entry.Enctype != impl.EnctypeAES256 {
		t.Fatalf("installed entry = %+v, want version 2 AES256", res.Entry)
	}
	if res.Generation != 1 {
		t.Fatalf("generation after the first install = %d, want 1", res.Generation)
	}
	if got := g.Generation(); got != 1 {
		t.Fatalf("guard generation = %d, want 1", got)
	}
	entries := g.Entries(keyPrincipal)
	if len(entries) != 1 || !entries[0].Staged {
		t.Fatalf("entries = %+v, want the single staged key", entries)
	}
	if _, err := g.Active(keyPrincipal); !errors.Is(err, impl.ErrNoActiveKey) {
		t.Fatalf("a keytab with only staged keys must fail Active with ErrNoActiveKey, got %v", err)
	}
}

func TestInstallRejectsVersionRegression(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 3, impl.EnctypeAES256, "three", keyBase)
	if _, err := g.Install(impl.KeyEntry{
		Principal: keyPrincipal,
		Version:   2,
		Enctype:   impl.EnctypeRC4,
		Material:  "late-two",
	}, keyBase); !errors.Is(err, impl.ErrVersionRegression) {
		t.Fatalf("installing an older version must fail with ErrVersionRegression, got %v", err)
	}
	if got := len(g.Entries(keyPrincipal)); got != 1 {
		t.Fatalf("entries after a rejected install = %d, want 1", got)
	}
}

func TestSameVersionHoldsMultipleEnctypes(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 2, impl.EnctypeAES256, "two-aes256", keyBase)
	installKey(t, g, 2, impl.EnctypeAES128, "two-aes128", keyBase)
	entries := g.Entries(keyPrincipal)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want two entries at version 2", entries)
	}
	seen := map[impl.Enctype]bool{}
	for _, e := range entries {
		if e.Version != 2 {
			t.Fatalf("entry %+v is not at version 2", e)
		}
		seen[e.Enctype] = true
	}
	if !seen[impl.EnctypeAES256] || !seen[impl.EnctypeAES128] {
		t.Fatalf("encryption types after the second install = %v, want AES256 and AES128", seen)
	}
}

func TestPromoteMakesStagedGenerationActive(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "material-one", keyBase)
	activatedAt := keyBase.Add(5 * time.Minute)
	promoteKey(t, g, 1, activatedAt)
	active, err := g.Active(keyPrincipal)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if active.Version != 1 || active.Enctype != impl.EnctypeAES256 {
		t.Fatalf("active key = %+v, want version 1 AES256", active)
	}
	if active.Staged {
		t.Fatalf("the promoted key is still marked staged")
	}
	if !active.ActivatedAt.Equal(activatedAt) {
		t.Fatalf("activation instant = %v, want %v", active.ActivatedAt, activatedAt)
	}
	if got := g.Generation(); got != 2 {
		t.Fatalf("generation = %d after install plus promote, want 2", got)
	}
	if got := g.Stats().Promotions; got != 1 {
		t.Fatalf("promotion counter = %d, want 1", got)
	}
}

func TestPromoteRejectsOlderVersion(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "one", keyBase)
	promoteKey(t, g, 1, keyBase)
	installKey(t, g, 2, impl.EnctypeAES256, "two", keyBase)
	promoteKey(t, g, 2, keyBase)
	if err := g.Promote(keyPrincipal, 1, keyBase); !errors.Is(err, impl.ErrStalePromotion) {
		t.Fatalf("re-promoting an older version must fail with ErrStalePromotion, got %v", err)
	}
	active, err := g.Active(keyPrincipal)
	if err != nil || active.Version != 2 {
		t.Fatalf("active after the rejected promotion = %+v err=%v, want version 2", active, err)
	}
}

func TestVerifyTriesActiveGenerationsNewestFirst(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "material-one", keyBase)
	promoteKey(t, g, 1, keyBase)
	installKey(t, g, 2, impl.EnctypeAES256, "material-two", keyBase)
	promoteKey(t, g, 2, keyBase)
	installKey(t, g, 3, impl.EnctypeAES256, "material-three", keyBase)
	promoteKey(t, g, 3, keyBase)
	payload := "service-ticket-body"
	ticket := impl.Ticket{
		Principal: keyPrincipal,
		Payload:   payload,
		Checksum:  impl.Seal("material-one", payload),
	}
	use, err := g.Verify(ticket)
	if err != nil {
		t.Fatalf("verify a ticket sealed with the oldest active key: %v", err)
	}
	if use.Version != 1 || use.Enctype != impl.EnctypeAES256 {
		t.Fatalf("key used = %+v, want version 1 AES256", use)
	}
	if got := g.Stats().Verifications; got != 1 {
		t.Fatalf("verification counter = %d, want 1", got)
	}
	unmatched := impl.Ticket{Principal: keyPrincipal, Payload: payload, Checksum: "0000"}
	if _, err := g.Verify(unmatched); !errors.Is(err, impl.ErrNoMatchingKey) {
		t.Fatalf("a ticket no key can open must fail with ErrNoMatchingKey, got %v", err)
	}
	if got := g.Stats().Misses; got != 1 {
		t.Fatalf("miss counter = %d, want 1", got)
	}
}

func TestVerifyRejectsUnknownVersion(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "one", keyBase)
	promoteKey(t, g, 1, keyBase)
	ticket := impl.Ticket{
		Principal: keyPrincipal,
		KVNO:      9,
		Enctype:   impl.EnctypeAES256,
		Payload:   "body",
		Checksum:  impl.Seal("one", "body"),
	}
	if _, err := g.Verify(ticket); !errors.Is(err, impl.ErrUnknownVersion) {
		t.Fatalf("a ticket for an absent version must fail with ErrUnknownVersion, got %v", err)
	}
}

func TestVerifyRejectsStagedKey(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "staged-one", keyBase)
	ticket := impl.Ticket{
		Principal: keyPrincipal,
		KVNO:      1,
		Enctype:   impl.EnctypeAES256,
		Payload:   "body",
		Checksum:  impl.Seal("staged-one", "body"),
	}
	if _, err := g.Verify(ticket); !errors.Is(err, impl.ErrStagedKey) {
		t.Fatalf("a staged key must not verify tickets, want ErrStagedKey, got %v", err)
	}
}

func TestRetireWaitsForGracePeriod(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "one", keyBase)
	promoteKey(t, g, 1, keyBase)
	installKey(t, g, 2, impl.EnctypeAES256, "two", keyBase.Add(10*time.Hour))
	promoteKey(t, g, 2, keyBase.Add(10*time.Hour))
	if err := g.Retire(keyPrincipal, 1, keyBase.Add(11*time.Hour)); !errors.Is(err, impl.ErrGraceNotElapsed) {
		t.Fatalf("retiring inside the grace period must fail with ErrGraceNotElapsed, got %v", err)
	}
	entries := g.Entries(keyPrincipal)
	retired := 0
	for _, e := range entries {
		if e.Version == 1 && e.Retired {
			retired++
		}
	}
	if retired != 0 {
		t.Fatalf("a rejected retirement already marked version 1 retired")
	}
	if err := g.Retire(keyPrincipal, 1, keyBase.Add(35*time.Hour)); err != nil {
		t.Fatalf("retiring after the grace period: %v", err)
	}
	if got := g.Stats().Retires; got != 1 {
		t.Fatalf("retire counter = %d, want 1", got)
	}
}

func TestRetireRejectsActiveKey(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "one", keyBase)
	promoteKey(t, g, 1, keyBase)
	installKey(t, g, 2, impl.EnctypeAES256, "two", keyBase)
	promoteKey(t, g, 2, keyBase)
	if err := g.Retire(keyPrincipal, 2, keyBase.Add(48*time.Hour)); !errors.Is(err, impl.ErrActiveKey) {
		t.Fatalf("retiring the active key must fail with ErrActiveKey, got %v", err)
	}
	active, err := g.Active(keyPrincipal)
	if err != nil || active.Version != 2 || active.Retired {
		t.Fatalf("active after the rejected retirement = %+v err=%v, want version 2 still live", active, err)
	}
}

func TestRetiredKeyStopsVerifyingTickets(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "one", keyBase)
	promoteKey(t, g, 1, keyBase)
	installKey(t, g, 2, impl.EnctypeAES256, "two", keyBase)
	promoteKey(t, g, 2, keyBase)
	if err := g.Retire(keyPrincipal, 1, keyBase.Add(48*time.Hour)); err != nil {
		t.Fatalf("retire version 1: %v", err)
	}
	ticket := impl.Ticket{
		Principal: keyPrincipal,
		KVNO:      1,
		Enctype:   impl.EnctypeAES256,
		Payload:   "late-ticket",
		Checksum:  impl.Seal("one", "late-ticket"),
	}
	if _, err := g.Verify(ticket); !errors.Is(err, impl.ErrRetired) {
		t.Fatalf("a retired key must not verify tickets, want ErrRetired, got %v", err)
	}
	live := impl.Ticket{
		Principal: keyPrincipal,
		KVNO:      2,
		Enctype:   impl.EnctypeAES256,
		Payload:   "fresh-ticket",
		Checksum:  impl.Seal("two", "fresh-ticket"),
	}
	if _, err := g.Verify(live); err != nil {
		t.Fatalf("the successor key must still verify: %v", err)
	}
}

func TestEntriesAreOrderedByVersionThenEnctype(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "one-aes256", keyBase)
	installKey(t, g, 1, impl.EnctypeAES128, "one-aes128", keyBase)
	installKey(t, g, 2, impl.EnctypeAES128, "two-aes128", keyBase)
	installKey(t, g, 3, impl.EnctypeRC4, "three-rc4", keyBase)
	installKey(t, g, 3, impl.EnctypeAES256, "three-aes256", keyBase)
	type pair struct {
		version int
		enctype impl.Enctype
	}
	want := []pair{
		{3, impl.EnctypeAES256},
		{3, impl.EnctypeRC4},
		{2, impl.EnctypeAES128},
		{1, impl.EnctypeAES128},
		{1, impl.EnctypeAES256},
	}
	entries := g.Entries(keyPrincipal)
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v, want %d rows", entries, len(want))
	}
	for i, w := range want {
		if entries[i].Version != w.version || entries[i].Enctype != w.enctype {
			t.Fatalf("entry %d = v%d/%d, want v%d/%d", i, entries[i].Version, entries[i].Enctype, w.version, w.enctype)
		}
	}
}

func TestWriteFileAndLoadFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.keytab")
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "one", keyBase)
	promoteKey(t, g, 1, keyBase)
	installKey(t, g, 2, impl.EnctypeAES256, "two", keyBase)
	if err := g.WriteFile(path); err != nil {
		t.Fatalf("write keytab: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("written keytab is missing or empty: %v", err)
	}
	reloaded := impl.NewKeytabRotationGuard(testKeytabConfig())
	if err := reloaded.LoadFile(path); err != nil {
		t.Fatalf("load keytab: %v", err)
	}
	entries := reloaded.Entries(keyPrincipal)
	if len(entries) != 2 {
		t.Fatalf("entries after reload = %+v, want both generations", entries)
	}
	active, err := reloaded.Active(keyPrincipal)
	if err != nil || active.Version != 1 {
		t.Fatalf("active after reload = %+v err=%v, want version 1", active, err)
	}
	ticket := impl.Ticket{
		Principal: keyPrincipal,
		Payload:   "body",
		Checksum:  impl.Seal("one", "body"),
	}
	if _, err := reloaded.Verify(ticket); err != nil {
		t.Fatalf("verify against the reloaded keytab: %v", err)
	}
}

func TestWriteFileFailureKeepsExistingKeytab(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.keytab")
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "one", keyBase)
	if err := g.WriteFile(path); err != nil {
		t.Fatalf("write keytab: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the keytab: %v", err)
	}
	if err := g.WriteFile(t.TempDir()); err == nil {
		t.Fatalf("writing onto an existing directory must fail")
	}
	if err := g.WriteFile(filepath.Join(dir, "missing", "svc.keytab")); err == nil {
		t.Fatalf("writing into a missing directory must fail")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the original keytab disappeared: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a failed write changed the existing keytab")
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewKeytabRotationGuard(testKeytabConfig())
	installKey(t, g, 1, impl.EnctypeAES256, "one", keyBase)
	if err := g.Restore([]byte("definitely not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	entries := g.Entries(keyPrincipal)
	if len(entries) != 1 || entries[0].Version != 1 {
		t.Fatalf("entries after a rejected restore = %+v, want version 1 untouched", entries)
	}
	if got := g.Generation(); got != 1 {
		t.Fatalf("generation after a rejected restore = %d, want 1", got)
	}
}
