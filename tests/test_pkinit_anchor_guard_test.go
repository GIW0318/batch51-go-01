package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-14"
)

var pkBase = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func testAnchorConfig() impl.AnchorConfig {
	return impl.AnchorConfig{
		MaxChainLen: 6,
		Now:         func() time.Time { return pkBase },
	}
}

func leafCert() impl.Cert {
	return impl.Cert{
		Subject:   "CN=svc.corp.example",
		Issuer:    "CN=Corp Intermediate",
		Serial:    "1001",
		NotBefore: pkBase.Add(-24 * time.Hour),
		NotAfter:  pkBase.Add(24 * time.Hour),
		KeyUsage:  []string{"clientAuth"},
		SANs:      []string{"dns:svc.corp.example", "upn:svc@corp.example"},
	}
}

func intermediateCert() impl.Cert {
	return impl.Cert{
		Subject:   "CN=Corp Intermediate",
		Issuer:    "CN=Root A",
		Serial:    "2001",
		NotBefore: pkBase.Add(-48 * time.Hour),
		NotAfter:  pkBase.Add(48 * time.Hour),
		KeyUsage:  []string{"keyCertSign"},
	}
}

func validChain() []impl.Cert {
	return []impl.Cert{leafCert(), intermediateCert()}
}

func newPkinitGuard(t *testing.T) *impl.PkinitAnchorGuard {
	t.Helper()
	g := impl.NewPkinitAnchorGuard(testAnchorConfig())
	if err := g.AddAnchor(impl.TrustAnchor{
		ID:         "anchor-a",
		Subject:    "CN=Root A",
		Generation: 1,
		Priority:   10,
	}); err != nil {
		t.Fatalf("add the root anchor: %v", err)
	}
	return g
}

func TestVerifyBuildsTrustPathToAnchor(t *testing.T) {
	g := newPkinitGuard(t)
	res, err := g.Verify(validChain(), pkBase)
	if err != nil {
		t.Fatalf("verify a valid chain: %v", err)
	}
	if res.AnchorID != "anchor-a" {
		t.Fatalf("anchor = %q, want anchor-a", res.AnchorID)
	}
	if res.Generation != 1 {
		t.Fatalf("anchor generation = %d, want 1", res.Generation)
	}
	want := []string{"CN=svc.corp.example", "CN=Corp Intermediate"}
	if len(res.Path) != len(want) {
		t.Fatalf("path = %v, want %v", res.Path, want)
	}
	for i, subject := range want {
		if res.Path[i] != subject {
			t.Fatalf("path = %v, want %v", res.Path, want)
		}
	}
	if got := g.Stats().Chains; got != 1 {
		t.Fatalf("chain counter = %d, want 1", got)
	}
}

func TestVerifyRejectsEmptyChain(t *testing.T) {
	g := newPkinitGuard(t)
	if _, err := g.Verify(nil, pkBase); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("an empty chain must fail with ErrBadChain, got %v", err)
	}
	if _, err := g.Verify([]impl.Cert{leafCert()}, pkBase); !errors.Is(err, impl.ErrNoTrustPath) {
		t.Fatalf("a chain whose last issuer is unknown must fail with ErrNoTrustPath, got %v", err)
	}
}

func TestVerifyRejectsBrokenLinkage(t *testing.T) {
	g := newPkinitGuard(t)
	chain := validChain()
	chain[1].Subject = "CN=Some Other Intermediate"
	if _, err := g.Verify(chain, pkBase); !errors.Is(err, impl.ErrNoTrustPath) {
		t.Fatalf("a chain with a broken issuer link must fail with ErrNoTrustPath, got %v", err)
	}
	if got := g.Stats().Rejected; got != 1 {
		t.Fatalf("rejected counter = %d, want 1", got)
	}
}

func TestVerifyRejectsExpiredLeaf(t *testing.T) {
	g := newPkinitGuard(t)
	chain := validChain()
	chain[0].NotAfter = pkBase.Add(-time.Minute)
	if _, err := g.Verify(chain, pkBase); !errors.Is(err, impl.ErrExpiredCert) {
		t.Fatalf("an expired leaf must fail with ErrExpiredCert, got %v", err)
	}
	future := validChain()
	future[0].NotBefore = pkBase.Add(time.Hour)
	if _, err := g.Verify(future, pkBase); !errors.Is(err, impl.ErrNotYetValid) {
		t.Fatalf("a not yet valid leaf must fail with ErrNotYetValid, got %v", err)
	}
}

func TestVerifyRejectsLeafWithoutClientAuth(t *testing.T) {
	g := newPkinitGuard(t)
	chain := validChain()
	chain[0].KeyUsage = []string{"serverAuth", "digitalSignature"}
	if _, err := g.Verify(chain, pkBase); !errors.Is(err, impl.ErrKeyUsage) {
		t.Fatalf("a leaf without clientAuth must fail with ErrKeyUsage, got %v", err)
	}
}

func TestVerifyRejectsRetiredAnchor(t *testing.T) {
	g := newPkinitGuard(t)
	if err := g.RetireAnchor("anchor-a", pkBase.Add(-time.Hour)); err != nil {
		t.Fatalf("retire the anchor: %v", err)
	}
	if _, err := g.Verify(validChain(), pkBase); !errors.Is(err, impl.ErrNoTrustPath) {
		t.Fatalf("a retired anchor must not validate chains, got %v", err)
	}
	if err := g.RetireAnchor("anchor-missing", pkBase); !errors.Is(err, impl.ErrUnknownAnchor) {
		t.Fatalf("retiring an unknown anchor must fail with ErrUnknownAnchor, got %v", err)
	}
}

func TestVerifySelectsTheAnchorTheChainReaches(t *testing.T) {
	g := impl.NewPkinitAnchorGuard(testAnchorConfig())
	for _, a := range []impl.TrustAnchor{
		{ID: "anchor-old", Subject: "CN=Root Old", Generation: 1, Priority: 10},
		{ID: "anchor-new", Subject: "CN=Root B", Generation: 4, Priority: 5},
	} {
		if err := g.AddAnchor(a); err != nil {
			t.Fatalf("add anchor %s: %v", a.ID, err)
		}
	}
	res, err := g.Verify(validChain(), pkBase)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if res.AnchorID != "anchor-new" || res.Generation != 4 {
		t.Fatalf("anchor = %s/%d, want anchor-new generation 4", res.AnchorID, res.Generation)
	}
}

func TestMapMatchesWildcardDNSRule(t *testing.T) {
	g := newPkinitGuard(t)
	if err := g.AddRule(impl.MappingRule{
		ID:         "rule-dns",
		MatchKind:  impl.MatchDNS,
		Pattern:    "*.corp.example",
		Account:    "alice",
		Generation: 3,
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	m, err := g.Map("svc@corp.example", []string{"dns:node7.corp.example"}, pkBase)
	if err != nil {
		t.Fatalf("map a matching subject: %v", err)
	}
	if m.Account != "alice" || m.RuleID != "rule-dns" || m.Generation != 3 {
		t.Fatalf("mapping = %+v, want alice through rule-dns generation 3", m)
	}
	if !m.At.Equal(pkBase) {
		t.Fatalf("mapping instant = %v, want %v", m.At, pkBase)
	}
	if got := g.Stats().Mapped; got != 1 {
		t.Fatalf("mapped counter = %d, want 1", got)
	}
}

func TestMapRejectsConflictingRules(t *testing.T) {
	g := newPkinitGuard(t)
	for _, r := range []impl.MappingRule{
		{ID: "rule-dns", MatchKind: impl.MatchDNS, Pattern: "*.corp.example", Account: "alice", Generation: 1},
		{ID: "rule-upn", MatchKind: impl.MatchUPN, Pattern: "*@corp.example", Account: "alice", Generation: 2},
	} {
		if err := g.AddRule(r); err != nil {
			t.Fatalf("add rule %s: %v", r.ID, err)
		}
	}
	_, err := g.Map("svc@corp.example", []string{"dns:node7.corp.example", "upn:svc@corp.example"}, pkBase)
	if !errors.Is(err, impl.ErrMappingConflict) {
		t.Fatalf("two matching rules must fail with ErrMappingConflict, got %v", err)
	}
	if got := g.Stats().Conflicts; got != 1 {
		t.Fatalf("conflict counter = %d, want 1", got)
	}
}

func TestMapRejectsUnmatchedSubject(t *testing.T) {
	g := newPkinitGuard(t)
	if err := g.AddRule(impl.MappingRule{
		ID:         "rule-other",
		MatchKind:  impl.MatchDNS,
		Pattern:    "*.other.example",
		Account:    "bob",
		Generation: 1,
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	if _, err := g.Map("svc@corp.example", []string{"dns:node7.corp.example"}, pkBase); !errors.Is(err, impl.ErrMappingMissing) {
		t.Fatalf("an unmatched subject must fail with ErrMappingMissing, got %v", err)
	}
}

func TestIssueReturnsTicketWithAnchorAndMapping(t *testing.T) {
	g := newPkinitGuard(t)
	if err := g.AddRule(impl.MappingRule{
		ID:         "rule-dns",
		MatchKind:  impl.MatchDNS,
		Pattern:    "*.corp.example",
		Account:    "alice",
		Generation: 3,
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	if err := g.SetAccount(impl.AccountState{Account: "alice", Enabled: true, Generation: 1}); err != nil {
		t.Fatalf("set account: %v", err)
	}
	req := impl.IssueRequest{
		Chain:     validChain(),
		Principal: "svc@corp.example",
		SANs:      []string{"dns:node7.corp.example"},
	}
	ticket, err := g.Issue(req, pkBase)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if ticket.Account != "alice" || ticket.RuleID != "rule-dns" {
		t.Fatalf("ticket = %+v, want alice through rule-dns", ticket)
	}
	if ticket.AnchorID != "anchor-a" || ticket.AnchorGeneration != 1 {
		t.Fatalf("ticket anchor = %s/%d, want anchor-a generation 1", ticket.AnchorID, ticket.AnchorGeneration)
	}
	if ticket.MappingGeneration != 3 {
		t.Fatalf("ticket mapping generation = %d, want 3", ticket.MappingGeneration)
	}
	if ticket.Seq != 1 {
		t.Fatalf("first ticket seq = %d, want 1", ticket.Seq)
	}
	second, err := g.Issue(req, pkBase.Add(time.Minute))
	if err != nil {
		t.Fatalf("second issue: %v", err)
	}
	if second.Seq != 2 {
		t.Fatalf("second ticket seq = %d, want 2", second.Seq)
	}
	if got := g.Stats().Issued; got != 2 {
		t.Fatalf("issued counter = %d, want 2", got)
	}
}

func TestIssueRejectsCertificateWithoutMapping(t *testing.T) {
	g := newPkinitGuard(t)
	if err := g.SetAccount(impl.AccountState{Account: "alice", Enabled: true}); err != nil {
		t.Fatalf("set account: %v", err)
	}
	req := impl.IssueRequest{
		Chain:     validChain(),
		Principal: "svc@corp.example",
		SANs:      []string{"dns:node7.corp.example"},
	}
	if _, err := g.Issue(req, pkBase); !errors.Is(err, impl.ErrMappingMissing) {
		t.Fatalf("issuing for an unmapped certificate must fail with ErrMappingMissing, got %v", err)
	}
}

func TestStatsCountChainsAndRejections(t *testing.T) {
	g := newPkinitGuard(t)
	if _, err := g.Verify(validChain(), pkBase); err != nil {
		t.Fatalf("verify a valid chain: %v", err)
	}
	if _, err := g.Verify(nil, pkBase); !errors.Is(err, impl.ErrBadChain) {
		t.Fatalf("empty chain: %v", err)
	}
	st := g.Stats()
	if st.Chains != 2 || st.Rejected != 1 {
		t.Fatalf("stats = %+v, want two chain attempts and one rejection", st)
	}
}

func TestSnapshotRestoreKeepsAnchorsRulesAndAccounts(t *testing.T) {
	g := newPkinitGuard(t)
	if err := g.AddRule(impl.MappingRule{
		ID:         "rule-dns",
		MatchKind:  impl.MatchDNS,
		Pattern:    "*.corp.example",
		Account:    "alice",
		Generation: 3,
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	if err := g.SetAccount(impl.AccountState{Account: "alice", Enabled: true, Generation: 1}); err != nil {
		t.Fatalf("set account: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewPkinitAnchorGuard(testAnchorConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(restored.Anchors()) != 1 || len(restored.Rules()) != 1 {
		t.Fatalf("restored anchors=%d rules=%d, want one of each", len(restored.Anchors()), len(restored.Rules()))
	}
	if _, err := restored.Verify(validChain(), pkBase); err != nil {
		t.Fatalf("verify after restore: %v", err)
	}
	m, err := restored.Map("svc@corp.example", []string{"dns:node7.corp.example"}, pkBase)
	if err != nil || m.Account != "alice" {
		t.Fatalf("map after restore = %+v err=%v, want alice", m, err)
	}
	if st, ok := restored.Account("alice"); !ok || !st.Enabled {
		t.Fatalf("account state after restore = %+v ok=%v, want it enabled", st, ok)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newPkinitGuard(t)
	if err := g.AddRule(impl.MappingRule{
		ID:         "rule-dns",
		MatchKind:  impl.MatchDNS,
		Pattern:    "*.corp.example",
		Account:    "alice",
		Generation: 1,
	}); err != nil {
		t.Fatalf("add rule: %v", err)
	}
	if err := g.Restore([]byte("}{ not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if len(g.Anchors()) != 1 || len(g.Rules()) != 1 {
		t.Fatalf("a rejected restore changed the guard: anchors=%d rules=%d", len(g.Anchors()), len(g.Rules()))
	}
	if _, err := g.Verify(validChain(), pkBase); err != nil {
		t.Fatalf("the guard must keep validating after a rejected restore: %v", err)
	}
}
