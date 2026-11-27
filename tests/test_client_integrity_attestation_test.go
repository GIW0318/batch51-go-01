package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-game-a-18"
)

type clockBox struct {
	now time.Time
}

func (c *clockBox) Now() time.Time { return c.now }

func newBox() *clockBox {
	return &clockBox{now: time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)}
}

func newAttest(c *clockBox) *impl.ClientIntegrityAttestation {
	return impl.NewClientIntegrityAttestation(
		impl.AttestLimits{MaxModules: 8, MaxModuleSize: 1 << 20, MaxChallenges: 4},
		c.Now,
	)
}

func defaultPolicy() impl.AttestPolicy {
	return impl.AttestPolicy{
		ChallengeTTL:     2 * time.Minute,
		RequireSignature: false,
		OnMismatch:       impl.ActionDeny,
		OnReadFailure:    impl.ActionLimited,
	}
}

func setupManifest(t *testing.T, a *impl.ClientIntegrityAttestation, ver string) []impl.ModuleEntry {
	t.Helper()
	entries := []impl.ModuleEntry{
		{Name: "core.dll", Hash: "hash-core-1", Size: 1024, Critical: true},
		{Name: "assets.pak", Hash: "hash-assets-1", Size: 2048, Critical: false},
	}
	if err := a.SetManifest(ver, impl.Manifest{Entries: entries}); err != nil {
		t.Fatalf("set manifest %s: %v", ver, err)
	}
	return entries
}

func mustPolicy(t *testing.T, a *impl.ClientIntegrityAttestation, p impl.AttestPolicy) {
	t.Helper()
	if err := a.SetPolicy(p); err != nil {
		t.Fatalf("set policy: %v", err)
	}
}

func proofFor(ch impl.Challenge, entries []impl.ModuleEntry) impl.Proof {
	mods := make([]impl.ModuleProof, 0, len(entries))
	for _, e := range entries {
		mods = append(mods, impl.ModuleProof{Name: e.Name, Hash: e.Hash, Size: e.Size, ReadOK: true})
	}
	return impl.Proof{ChallengeID: ch.ID, Session: ch.Session, Modules: mods}
}

func issue(t *testing.T, a *impl.ClientIntegrityAttestation, session string) impl.Challenge {
	t.Helper()
	ch, err := a.IssueChallenge(session)
	if err != nil {
		t.Fatalf("issue challenge for %s: %v", session, err)
	}
	return ch
}

func TestIssueChallengeRejectsBlankSession(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	setupManifest(t, a, "v1")
	for _, session := range []string{"", "   "} {
		if _, err := a.IssueChallenge(session); !errors.Is(err, impl.ErrBadSession) {
			t.Fatalf("blank session %q must fail with ErrBadSession, got %v", session, err)
		}
	}
}

func TestIssueChallengeAllocatesDistinctChallenges(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	p := defaultPolicy()
	p.ChallengeTTL = 90 * time.Second
	mustPolicy(t, a, p)
	setupManifest(t, a, "v1")
	first := issue(t, a, "sess-1")
	second := issue(t, a, "sess-1")
	if first.ID == "" || first.Nonce == "" {
		t.Fatalf("challenge must carry an id and a nonce, got %+v", first)
	}
	if first.ID == second.ID || first.Nonce == second.Nonce {
		t.Fatalf("two challenges must not share id or nonce, got %+v and %+v", first, second)
	}
	if first.ManifestVer != "v1" {
		t.Fatalf("challenge manifest version = %q, want v1", first.ManifestVer)
	}
	if !first.IssuedAt.Equal(c.now) {
		t.Fatalf("challenge issued at %v, want %v", first.IssuedAt, c.now)
	}
	if !first.ExpiresAt.Equal(c.now.Add(90 * time.Second)) {
		t.Fatalf("challenge expires at %v, want %v", first.ExpiresAt, c.now.Add(90*time.Second))
	}
	if got := a.Stats().ChallengesIssued; got != 2 {
		t.Fatalf("challenges issued = %d, want 2", got)
	}
}

func TestVerifyCleanProofAllows(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	entries := setupManifest(t, a, "v1")
	ch := issue(t, a, "sess-2")
	v, err := a.Verify("sess-2", proofFor(ch, entries))
	if err != nil {
		t.Fatalf("verify clean proof: %v", err)
	}
	if v.Action != impl.ActionAllow || v.Reason != impl.ReasonVerified {
		t.Fatalf("clean verdict = %s/%s, want allow/verified", v.Action, v.Reason)
	}
	if v.ManifestVer != "v1" {
		t.Fatalf("verdict manifest version = %q, want v1", v.ManifestVer)
	}
	stats := a.Stats()
	if stats.Allowed != 1 || stats.Verified != 1 || stats.ChallengesUsed != 1 {
		t.Fatalf("stats = %+v, want one allowed/verified/used", stats)
	}
}

func TestVerifyMismatchedModuleDenies(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	entries := setupManifest(t, a, "v1")
	ch := issue(t, a, "sess-3")
	tampered := make([]impl.ModuleEntry, len(entries))
	copy(tampered, entries)
	tampered[0].Hash = "hash-core-evil"
	v, err := a.Verify("sess-3", proofFor(ch, tampered))
	if err != nil {
		t.Fatalf("verify tampered proof: %v", err)
	}
	if v.Action != impl.ActionDeny || v.Reason != impl.ReasonModuleMismatch {
		t.Fatalf("tampered verdict = %s/%s, want deny/module_mismatch", v.Action, v.Reason)
	}
	reasons := a.Explain("sess-3")
	found := false
	for _, r := range reasons {
		if r.Code == impl.ReasonModuleMismatch && r.Module == "core.dll" {
			found = true
		}
	}
	if !found {
		t.Fatalf("explain must report the mismatched module, got %+v", reasons)
	}
	if got := a.Stats().Denied; got != 1 {
		t.Fatalf("denied counter = %d, want 1", got)
	}
}

func TestVerifyReplayRejected(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	entries := setupManifest(t, a, "v1")
	ch := issue(t, a, "sess-4")
	proof := proofFor(ch, entries)
	if _, err := a.Verify("sess-4", proof); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := a.Verify("sess-4", proof); !errors.Is(err, impl.ErrChallengeUsed) {
		t.Fatalf("replayed challenge must fail with ErrChallengeUsed, got %v", err)
	}
	if got := a.Stats().Allowed; got != 1 {
		t.Fatalf("allowed counter = %d, want 1 after a replay", got)
	}
}

func TestVerifyExpiredChallenge(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	entries := setupManifest(t, a, "v1")
	ch := issue(t, a, "sess-5")
	c.now = c.now.Add(3 * time.Minute)
	if _, err := a.Verify("sess-5", proofFor(ch, entries)); !errors.Is(err, impl.ErrChallengeExpired) {
		t.Fatalf("expired challenge must fail with ErrChallengeExpired, got %v", err)
	}
}

func TestVerifyUnknownChallenge(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	setupManifest(t, a, "v1")
	proof := impl.Proof{ChallengeID: "ch-999", Session: "sess-6", Signature: ""}
	if _, err := a.Verify("sess-6", proof); !errors.Is(err, impl.ErrChallengeNotFound) {
		t.Fatalf("unknown challenge must fail with ErrChallengeNotFound, got %v", err)
	}
}

func TestVerifyForeignSessionRejected(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	entries := setupManifest(t, a, "v1")
	ch := issue(t, a, "sess-7")
	wrongOwner := proofFor(ch, entries)
	wrongOwner.Session = "sess-other"
	if _, err := a.Verify("sess-7", wrongOwner); !errors.Is(err, impl.ErrBadProof) {
		t.Fatalf("proof of another session must fail with ErrBadProof, got %v", err)
	}
	stolen := proofFor(ch, entries)
	stolen.Session = "sess-8"
	if _, err := a.Verify("sess-8", stolen); !errors.Is(err, impl.ErrChallengeNotFound) {
		t.Fatalf("a challenge must not verify under another session, got %v", err)
	}
}

func TestRevokeSessionRejectsVerify(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	entries := setupManifest(t, a, "v1")
	ch := issue(t, a, "sess-9")
	if err := a.Revoke("sess-9"); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if err := a.Revoke("sess-9"); err != nil {
		t.Fatalf("repeated revoke must stay idempotent, got %v", err)
	}
	if _, err := a.Verify("sess-9", proofFor(ch, entries)); !errors.Is(err, impl.ErrSessionRevoked) {
		t.Fatalf("verify after revoke must fail with ErrSessionRevoked, got %v", err)
	}
	if _, err := a.IssueChallenge("sess-9"); !errors.Is(err, impl.ErrSessionRevoked) {
		t.Fatalf("challenge after revoke must fail with ErrSessionRevoked, got %v", err)
	}
	if _, err := a.IssueChallenge(""); !errors.Is(err, impl.ErrBadSession) {
		t.Fatalf("blank session must fail with ErrBadSession, got %v", err)
	}
}

func TestSetManifestRejectsBadManifest(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	good := []impl.ModuleEntry{{Name: "core.dll", Hash: "hash-core-1", Size: 16, Critical: true}}
	if err := a.SetManifest("v1", impl.Manifest{Entries: good}); err != nil {
		t.Fatalf("set manifest v1: %v", err)
	}
	bad := []struct {
		ver     string
		entries []impl.ModuleEntry
	}{
		{"", good},
		{"v2", nil},
		{"v2", []impl.ModuleEntry{{Name: "", Hash: "h", Size: 1}}},
		{"v2", []impl.ModuleEntry{{Name: "core.dll", Hash: "", Size: 1}}},
		{"v2", []impl.ModuleEntry{{Name: "core.dll", Hash: "h1", Size: 1}, {Name: "core.dll", Hash: "h2", Size: 1}}},
	}
	for i, m := range bad {
		if err := a.SetManifest(m.ver, impl.Manifest{Entries: m.entries}); !errors.Is(err, impl.ErrBadManifest) {
			t.Fatalf("manifest case %d must fail with ErrBadManifest, got %v", i, err)
		}
	}
	tiny := impl.NewClientIntegrityAttestation(
		impl.AttestLimits{MaxModules: 2, MaxModuleSize: 32, MaxChallenges: 4},
		c.Now,
	)
	three := []impl.ModuleEntry{
		{Name: "a", Hash: "ha", Size: 1},
		{Name: "b", Hash: "hb", Size: 1},
		{Name: "c", Hash: "hc", Size: 1},
	}
	if err := tiny.SetManifest("v1", impl.Manifest{Entries: three}); !errors.Is(err, impl.ErrManifestLimit) {
		t.Fatalf("module cap must fail with ErrManifestLimit, got %v", err)
	}
	big := []impl.ModuleEntry{{Name: "a", Hash: "ha", Size: 4096}}
	if err := tiny.SetManifest("v1", impl.Manifest{Entries: big}); !errors.Is(err, impl.ErrManifestLimit) {
		t.Fatalf("module size cap must fail with ErrManifestLimit, got %v", err)
	}
}

func TestMarkModuleDirtyDenies(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	entries := setupManifest(t, a, "v1")
	if err := a.MarkModuleDirty("  "); !errors.Is(err, impl.ErrBadModule) {
		t.Fatalf("blank module must fail with ErrBadModule, got %v", err)
	}
	if err := a.MarkModuleDirty("core.dll"); err != nil {
		t.Fatalf("mark module dirty: %v", err)
	}
	ch := issue(t, a, "sess-10")
	v, err := a.Verify("sess-10", proofFor(ch, entries))
	if err != nil {
		t.Fatalf("verify after dirty mark: %v", err)
	}
	if v.Action != impl.ActionDeny || v.Reason != impl.ReasonModuleDirty {
		t.Fatalf("dirty verdict = %s/%s, want deny/module_dirty", v.Action, v.Reason)
	}
}

func TestRequireSignatureRejectsUnsigned(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	p := defaultPolicy()
	p.RequireSignature = true
	mustPolicy(t, a, p)
	entries := setupManifest(t, a, "v1")
	ch := issue(t, a, "sess-11")
	if _, err := a.Verify("sess-11", proofFor(ch, entries)); !errors.Is(err, impl.ErrBadProof) {
		t.Fatalf("unsigned proof must fail with ErrBadProof, got %v", err)
	}
	signed := proofFor(ch, entries)
	signed.Signature = "sig-abc"
	v, err := a.Verify("sess-11", signed)
	if err != nil {
		t.Fatalf("verify signed proof: %v", err)
	}
	if v.Action != impl.ActionAllow {
		t.Fatalf("signed verdict action = %q, want allow", v.Action)
	}
}

func TestChallengeCapRejectsNew(t *testing.T) {
	c := newBox()
	a := impl.NewClientIntegrityAttestation(
		impl.AttestLimits{MaxModules: 8, MaxModuleSize: 1 << 20, MaxChallenges: 2},
		c.Now,
	)
	mustPolicy(t, a, defaultPolicy())
	entries := setupManifest(t, a, "v1")
	first := issue(t, a, "sess-12")
	second := issue(t, a, "sess-12")
	if _, err := a.IssueChallenge("sess-12"); !errors.Is(err, impl.ErrChallengeLimit) {
		t.Fatalf("challenge cap must fail with ErrChallengeLimit, got %v", err)
	}
	v, err := a.Verify("sess-12", proofFor(first, entries))
	if err != nil {
		t.Fatalf("the oldest live challenge must stay usable: %v", err)
	}
	if v.Action != impl.ActionAllow {
		t.Fatalf("oldest challenge verdict = %q, want allow", v.Action)
	}
	if second.ID == first.ID {
		t.Fatalf("challenge ids must differ")
	}
}

func TestSnapshotRestoreKeepsUsedChallenge(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	entries := setupManifest(t, a, "v1")
	ch := issue(t, a, "sess-13")
	proof := proofFor(ch, entries)
	if _, err := a.Verify("sess-13", proof); err != nil {
		t.Fatalf("verify: %v", err)
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newAttest(c)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := restored.Verify("sess-13", proof); !errors.Is(err, impl.ErrChallengeUsed) {
		t.Fatalf("consumed challenge must stay consumed after restore, got %v", err)
	}
	next := issue(t, restored, "sess-14")
	if next.ManifestVer != "v1" {
		t.Fatalf("restored manifest version = %q, want v1", next.ManifestVer)
	}
	if got := restored.Stats().Allowed; got != 1 {
		t.Fatalf("restored allowed counter = %d, want 1", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	c := newBox()
	a := newAttest(c)
	mustPolicy(t, a, defaultPolicy())
	setupManifest(t, a, "v1")
	if err := a.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	ch := issue(t, a, "sess-15")
	if ch.ManifestVer != "v1" {
		t.Fatalf("live state must keep working after a rejected restore, got %q", ch.ManifestVer)
	}
}
