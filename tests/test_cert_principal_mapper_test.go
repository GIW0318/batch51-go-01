package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-safety-a-05"
)

func mapperConfig() impl.MapperConfig {
	return impl.MapperConfig{
		DefaultTenant:     "corp",
		MaxRulesPerTenant: 8,
		MaxCacheEntries:   32,
		MaxSANs:           4,
	}
}

func mappingRule(id string, priority int, matcher, pattern, account string) impl.MappingRule {
	return impl.MappingRule{
		ID:       id,
		Priority: priority,
		Matcher:  matcher,
		Pattern:  pattern,
		Account:  account,
		Kind:     impl.KindUser,
	}
}

func certView(fingerprint, dn, cn string, sans ...impl.SAN) impl.CertView {
	return impl.CertView{
		Fingerprint: fingerprint,
		SubjectDN:   dn,
		CN:          cn,
		SANs:        sans,
		IssuerFP:    "9f1c4a77e0b2d358c6a90f2e5b7d1c40a8e6f392",
	}
}

func mustAddRule(t *testing.T, m *impl.CertPrincipalMapper, tenant string, rule impl.MappingRule) {
	t.Helper()
	if err := m.AddRule(tenant, rule); err != nil {
		t.Fatalf("add rule %s: %v", rule.ID, err)
	}
}

func TestAddRuleValidatesRule(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	valid := mappingRule("r-1", 10, impl.MatcherCN, "alice", "acct-alice")
	if err := m.AddRule("  ", valid); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("blank tenant returned %v, want ErrMissingField", err)
	}
	noID := valid
	noID.ID = ""
	if err := m.AddRule("corp", noID); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("blank rule id returned %v, want ErrMissingField", err)
	}
	noPattern := valid
	noPattern.Pattern = "  "
	if err := m.AddRule("corp", noPattern); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("blank pattern returned %v, want ErrMissingField", err)
	}
	noAccount := valid
	noAccount.Account = ""
	if err := m.AddRule("corp", noAccount); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("blank account returned %v, want ErrMissingField", err)
	}
	badMatcher := valid
	badMatcher.Matcher = "san_ip"
	if err := m.AddRule("corp", badMatcher); !errors.Is(err, impl.ErrBadMatcher) {
		t.Fatalf("unsupported matcher returned %v, want ErrBadMatcher", err)
	}
	badPriority := valid
	badPriority.Priority = -1
	if err := m.AddRule("corp", badPriority); !errors.Is(err, impl.ErrBadPriority) {
		t.Fatalf("negative priority returned %v, want ErrBadPriority", err)
	}
	mustAddRule(t, m, "corp", valid)
	if err := m.AddRule("corp", valid); !errors.Is(err, impl.ErrRuleExists) {
		t.Fatalf("duplicate rule id returned %v, want ErrRuleExists", err)
	}
	if got := len(m.Rules("corp")); got != 1 {
		t.Fatalf("rules after the rejected adds = %d, want 1", got)
	}
}

func TestAddRuleEnforcesQuota(t *testing.T) {
	cfg := mapperConfig()
	cfg.MaxRulesPerTenant = 2
	m := impl.NewCertPrincipalMapper(cfg)
	mustAddRule(t, m, "corp", mappingRule("r-1", 10, impl.MatcherCN, "alice", "acct-alice"))
	mustAddRule(t, m, "corp", mappingRule("r-2", 20, impl.MatcherCN, "bob", "acct-bob"))
	if err := m.AddRule("corp", mappingRule("r-3", 30, impl.MatcherCN, "carol", "acct-carol")); !errors.Is(err, impl.ErrRuleQuota) {
		t.Fatalf("third rule returned %v, want ErrRuleQuota", err)
	}
	if got := len(m.Rules("corp")); got != 2 {
		t.Fatalf("rules after the quota error = %d, want the cap 2", got)
	}
	mustAddRule(t, m, "lab", mappingRule("r-1", 10, impl.MatcherCN, "dave", "acct-dave"))
}

func TestRulesSortedByPriority(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("r-50", 50, impl.MatcherCN, "carol", "acct-carol"))
	mustAddRule(t, m, "corp", mappingRule("r-10", 10, impl.MatcherCN, "alice", "acct-alice"))
	mustAddRule(t, m, "corp", mappingRule("r-30", 30, impl.MatcherCN, "bob", "acct-bob"))
	rules := m.Rules("corp")
	if len(rules) != 3 {
		t.Fatalf("rules = %d, want 3", len(rules))
	}
	want := []string{"r-10", "r-30", "r-50"}
	for i, id := range want {
		if rules[i].ID != id {
			t.Fatalf("rule %d = %q, want %q in priority order", i, rules[i].ID, id)
		}
	}
	if err := m.RemoveRule("corp", "r-404"); !errors.Is(err, impl.ErrRuleNotFound) {
		t.Fatalf("removing an unknown rule returned %v, want ErrRuleNotFound", err)
	}
	if err := m.RemoveRule("corp", "r-30"); err != nil {
		t.Fatalf("remove rule: %v", err)
	}
	left := m.Rules("corp")
	if len(left) != 2 || left[0].ID != "r-10" || left[1].ID != "r-50" {
		t.Fatalf("rules after removal = %+v, want r-10 then r-50", left)
	}
}

func TestResolveMatchesFullDN(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("dn-alice", 10, impl.MatcherDN,
		"CN=Alice,OU=People,DC=Corp,DC=Example", "acct-alice"))
	cert := certView("fp-alice-01", "cn=alice,ou=people,dc=corp,dc=example", "Alice")
	p, err := m.Resolve("corp", cert)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.Account != "acct-alice" || p.Kind != impl.KindUser {
		t.Fatalf("principal = %+v, want acct-alice as a user", p)
	}
	if p.RuleID != "dn-alice" || p.Tenant != "corp" {
		t.Fatalf("principal rule=%q tenant=%q, want dn-alice and corp", p.RuleID, p.Tenant)
	}
	if p.Fingerprint != cert.Fingerprint {
		t.Fatalf("principal fingerprint = %q, want %q", p.Fingerprint, cert.Fingerprint)
	}
	if p.Matched == "" {
		t.Fatalf("principal must record the value that matched")
	}
	defaulted, err := m.Resolve("", cert)
	if err != nil {
		t.Fatalf("resolve with a blank tenant must use the default tenant: %v", err)
	}
	if defaulted.Tenant != "corp" || defaulted.Account != "acct-alice" {
		t.Fatalf("default tenant resolve = %+v, want corp/acct-alice", defaulted)
	}
	if _, err := m.Resolve("corp", impl.CertView{SubjectDN: "cn=alice"}); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("certificate without a fingerprint returned %v, want ErrMissingField", err)
	}
}

func TestResolveNormalizesDNSAndEmail(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("dns-api", 10, impl.MatcherSANDNS,
		"api.corp.example.", "acct-api"))
	cert := certView("fp-api-01", "cn=api", "api",
		impl.SAN{Type: impl.SANDNS, Value: "API.Corp.Example"},
		impl.SAN{Type: impl.SANEmail, Value: "nobody@corp.example"})
	p, err := m.Resolve("corp", cert)
	if err != nil {
		t.Fatalf("resolve by san dns: %v", err)
	}
	if p.Account != "acct-api" {
		t.Fatalf("principal account = %q, want acct-api", p.Account)
	}
	mustAddRule(t, m, "lab", mappingRule("mail-alice", 10, impl.MatcherSANEmail,
		"alice@corp.example", "acct-alice"))
	mailCert := certView("fp-mail-01", "cn=alice", "alice",
		impl.SAN{Type: impl.SANEmail, Value: "alice@CORP.EXAMPLE"})
	mp, err := m.Resolve("lab", mailCert)
	if err != nil {
		t.Fatalf("resolve by san email: %v", err)
	}
	if mp.Account != "acct-alice" {
		t.Fatalf("email principal account = %q, want acct-alice", mp.Account)
	}
}

func TestResolveRejectsUnknownCertificate(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	lonely := certView("fp-lonely", "cn=bob,ou=people,dc=corp,dc=example", "bob")
	if _, err := m.Resolve("corp", lonely); !errors.Is(err, impl.ErrNoMapping) {
		t.Fatalf("resolve without rules returned %v, want ErrNoMapping", err)
	}
	mustAddRule(t, m, "corp", mappingRule("dn-other", 10, impl.MatcherDN,
		"cn=someone-else,ou=people,dc=corp,dc=example", "acct-other"))
	if _, err := m.Resolve("corp", lonely); !errors.Is(err, impl.ErrNoMapping) {
		t.Fatalf("certificate only matching the common name returned %v, want ErrNoMapping", err)
	}
	if got := m.Stats().Rejected; got != 2 {
		t.Fatalf("rejected counter = %d, want 2", got)
	}
}

func TestResolveEnforcesSANQuota(t *testing.T) {
	cfg := mapperConfig()
	cfg.MaxSANs = 3
	m := impl.NewCertPrincipalMapper(cfg)
	mustAddRule(t, m, "corp", mappingRule("dns-api", 10, impl.MatcherSANDNS, "api.corp.example", "acct-api"))
	tooMany := certView("fp-many", "cn=api", "api",
		impl.SAN{Type: impl.SANDNS, Value: "api.corp.example"},
		impl.SAN{Type: impl.SANDNS, Value: "a.corp.example"},
		impl.SAN{Type: impl.SANDNS, Value: "b.corp.example"},
		impl.SAN{Type: impl.SANDNS, Value: "c.corp.example"})
	if _, err := m.Resolve("corp", tooMany); !errors.Is(err, impl.ErrSANQuota) {
		t.Fatalf("certificate above the SAN cap returned %v, want ErrSANQuota", err)
	}
	atCap := certView("fp-cap", "cn=api", "api",
		impl.SAN{Type: impl.SANDNS, Value: "api.corp.example"},
		impl.SAN{Type: impl.SANDNS, Value: "a.corp.example"},
		impl.SAN{Type: impl.SANDNS, Value: "b.corp.example"})
	if _, err := m.Resolve("corp", atCap); err != nil {
		t.Fatalf("certificate at the SAN cap must resolve, got %v", err)
	}
}

func TestResolveMatchesSANURIAndIssuerFP(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("uri-payroll", 10, impl.MatcherSANURI,
		"spiffe://cluster.local/ns/payroll/sa/api", "svc-payroll"))
	cert := certView("fp-uri", "cn=api", "api",
		impl.SAN{Type: impl.SANURI, Value: "SPIFFE://Cluster.Local/ns/payroll/sa/api"})
	p, err := m.Resolve("corp", cert)
	if err != nil {
		t.Fatalf("resolve by san uri: %v", err)
	}
	if p.Account != "svc-payroll" {
		t.Fatalf("uri principal account = %q, want svc-payroll", p.Account)
	}
	mustAddRule(t, m, "lab", mappingRule("issuer-internal", 10, impl.MatcherIssuerFP,
		"9F1C4A77E0B2D358C6A90F2E5B7D1C40A8E6F392", "svc-internal"))
	issuerCert := certView("fp-issuer", "cn=node", "node")
	ip, err := m.Resolve("lab", issuerCert)
	if err != nil {
		t.Fatalf("resolve by issuer fingerprint: %v", err)
	}
	if ip.Account != "svc-internal" {
		t.Fatalf("issuer principal account = %q, want svc-internal", ip.Account)
	}
}

func TestResolveCachesByFingerprint(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("cn-alice", 10, impl.MatcherCN, "alice", "acct-alice"))
	mustAddRule(t, m, "lab", mappingRule("cn-alice", 10, impl.MatcherCN, "alice", "acct-alice"))
	cert := certView("fp-cache", "cn=alice", "alice")
	first, err := m.Resolve("corp", cert)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	second, err := m.Resolve("corp", cert)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if second.Account != first.Account || second.RuleID != first.RuleID {
		t.Fatalf("cached answer = %+v, want the first answer %+v", second, first)
	}
	if got := m.CacheSize(); got != 1 {
		t.Fatalf("cache size = %d, want 1", got)
	}
	if got := m.Stats().CacheHits; got != 1 {
		t.Fatalf("cache hits = %d, want 1", got)
	}
	if got := m.Stats().Resolved; got != 1 {
		t.Fatalf("resolved counter = %d, want 1", got)
	}
	if _, err := m.Resolve("lab", cert); err != nil {
		t.Fatalf("resolve in the second tenant: %v", err)
	}
	if got := m.CacheSize(); got != 2 {
		t.Fatalf("cache size after a second tenant = %d, want 2", got)
	}
}

func TestInvalidateRevokesCertificate(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("cn-alice", 10, impl.MatcherCN, "alice", "acct-alice"))
	cert := certView("fp-revoke", "cn=alice", "alice")
	if _, err := m.Resolve("corp", cert); err != nil {
		t.Fatalf("resolve before the revocation: %v", err)
	}
	if m.Revoked(cert.Fingerprint) {
		t.Fatalf("a fresh fingerprint must not be reported as revoked")
	}
	if err := m.Invalidate(cert.Fingerprint); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if !m.Revoked(cert.Fingerprint) {
		t.Fatalf("invalidated fingerprint must report as revoked")
	}
	if got := m.CacheSize(); got != 0 {
		t.Fatalf("cache size after invalidation = %d, want 0", got)
	}
	if _, err := m.Resolve("corp", cert); !errors.Is(err, impl.ErrCertRevoked) {
		t.Fatalf("resolve of a revoked certificate returned %v, want ErrCertRevoked", err)
	}
	if err := m.Invalidate(cert.Fingerprint); err != nil {
		t.Fatalf("invalidate must be idempotent, got %v", err)
	}
	trace, err := m.Explain("corp", cert)
	if err != nil {
		t.Fatalf("explain revoked certificate: %v", err)
	}
	if trace.Reason != impl.ReasonRevoked {
		t.Fatalf("explain reason = %q, want %q", trace.Reason, impl.ReasonRevoked)
	}
}

func TestRemoveRuleDropsRule(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("cn-alice", 10, impl.MatcherCN, "alice", "acct-alice"))
	mustAddRule(t, m, "corp", mappingRule("cn-bob", 20, impl.MatcherCN, "bob", "acct-bob"))
	if err := m.RemoveRule("corp", "cn-alice"); err != nil {
		t.Fatalf("remove rule: %v", err)
	}
	aliceCert := certView("fp-alice-rm", "cn=alice", "alice")
	if _, err := m.Resolve("corp", aliceCert); !errors.Is(err, impl.ErrNoMapping) {
		t.Fatalf("resolve after removing the matching rule returned %v, want ErrNoMapping", err)
	}
	bobCert := certView("fp-bob-rm", "cn=bob", "bob")
	p, err := m.Resolve("corp", bobCert)
	if err != nil {
		t.Fatalf("resolve with the remaining rule: %v", err)
	}
	if p.Account != "acct-bob" {
		t.Fatalf("remaining rule resolved to %q, want acct-bob", p.Account)
	}
}

func TestResolveManyReturnsAllPrincipals(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("cn-alice", 10, impl.MatcherCN, "alice", "acct-alice"))
	mustAddRule(t, m, "corp", mappingRule("cn-bob", 20, impl.MatcherCN, "bob", "acct-bob"))
	batch := []impl.CertView{
		certView("fp-batch-1", "cn=alice", "alice"),
		certView("fp-batch-2", "cn=bob", "bob"),
	}
	out, err := m.ResolveMany("corp", batch)
	if err != nil {
		t.Fatalf("resolve many: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("resolved principals = %d, want 2", len(out))
	}
	if out[0].Account != "acct-alice" || out[1].Account != "acct-bob" {
		t.Fatalf("batch order = %q,%q, want acct-alice,acct-bob", out[0].Account, out[1].Account)
	}
	broken := append(batch, certView("fp-batch-3", "cn=carol", "carol"))
	if _, err := m.ResolveMany("corp", broken); !errors.Is(err, impl.ErrNoMapping) {
		t.Fatalf("batch with an unmapped certificate returned %v, want ErrNoMapping", err)
	}
}

func TestExplainReportsEvaluatedRules(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("cn-first", 10, impl.MatcherCN, "alice", "acct-alice"))
	mustAddRule(t, m, "corp", mappingRule("dn-second", 20, impl.MatcherDN, "cn=alice,ou=people", "acct-alice-dn"))
	trace, err := m.Explain("corp", certView("fp-explained", "cn=alice", "alice"))
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if len(trace.Evaluated) != 2 {
		t.Fatalf("evaluated rules = %v, want both rules", trace.Evaluated)
	}
	if trace.Evaluated[0] != "cn-first" || trace.Evaluated[1] != "dn-second" {
		t.Fatalf("evaluated order = %v, want priority order", trace.Evaluated)
	}
	if trace.Winner != "cn-first" || trace.Reason != impl.ReasonMatched {
		t.Fatalf("trace winner=%q reason=%q, want cn-first/matched", trace.Winner, trace.Reason)
	}
	if trace.Conflict {
		t.Fatalf("a single winner must not be reported as a conflict")
	}
	if trace.Fingerprint != "fp-explained" || trace.Tenant != "corp" {
		t.Fatalf("trace identity = %+v", trace)
	}
	none, err := m.Explain("corp", certView("fp-none", "cn=nobody", "nobody"))
	if err != nil {
		t.Fatalf("explain unmatched certificate: %v", err)
	}
	if none.Reason != impl.ReasonNoMatch || none.Winner != "" {
		t.Fatalf("unmatched trace = %+v, want no_match without a winner", none)
	}
}

func TestStatsCountsResolvedAndRejected(t *testing.T) {
	m := impl.NewCertPrincipalMapper(mapperConfig())
	mustAddRule(t, m, "corp", mappingRule("cn-alice", 10, impl.MatcherCN, "alice", "acct-alice"))
	if _, err := m.Resolve("corp", certView("fp-stats-1", "cn=alice", "alice")); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := m.Resolve("corp", certView("fp-stats-2", "cn=nobody", "nobody")); !errors.Is(err, impl.ErrNoMapping) {
		t.Fatalf("unmapped resolve returned %v, want ErrNoMapping", err)
	}
	if err := m.Invalidate("fp-stats-3"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if _, err := m.Resolve("corp", certView("fp-stats-3", "cn=alice", "alice")); !errors.Is(err, impl.ErrCertRevoked) {
		t.Fatalf("revoked resolve returned %v, want ErrCertRevoked", err)
	}
	stats := m.Stats()
	if stats.Resolved != 1 {
		t.Fatalf("resolved counter = %d, want 1", stats.Resolved)
	}
	if stats.Rejected != 2 {
		t.Fatalf("rejected counter = %d, want 2", stats.Rejected)
	}
}

func TestSnapshotRestoreKeepsRulesAndRevocations(t *testing.T) {
	cfg := mapperConfig()
	m := impl.NewCertPrincipalMapper(cfg)
	mustAddRule(t, m, "corp", mappingRule("cn-alice", 10, impl.MatcherCN, "alice", "acct-alice"))
	mustAddRule(t, m, "corp", mappingRule("dns-api", 20, impl.MatcherSANDNS, "api.corp.example", "acct-api"))
	live := certView("fp-snap-live", "cn=alice", "alice")
	if _, err := m.Resolve("corp", live); err != nil {
		t.Fatalf("resolve live certificate: %v", err)
	}
	dead := certView("fp-snap-dead", "cn=api", "api",
		impl.SAN{Type: impl.SANDNS, Value: "api.corp.example"})
	if err := m.Invalidate(dead.Fingerprint); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	rulesBefore := m.Rules("corp")
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewCertPrincipalMapper(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rulesAfter := restored.Rules("corp")
	if len(rulesAfter) != len(rulesBefore) {
		t.Fatalf("restored rules = %d, want %d", len(rulesAfter), len(rulesBefore))
	}
	for i := range rulesBefore {
		if rulesAfter[i].ID != rulesBefore[i].ID {
			t.Fatalf("restored rule %d = %q, want %q", i, rulesAfter[i].ID, rulesBefore[i].ID)
		}
	}
	p, err := restored.Resolve("corp", live)
	if err != nil {
		t.Fatalf("resolve live certificate after restore: %v", err)
	}
	if p.Account != "acct-alice" {
		t.Fatalf("restored principal = %q, want acct-alice", p.Account)
	}
	if _, err := restored.Resolve("corp", dead); !errors.Is(err, impl.ErrCertRevoked) {
		t.Fatalf("revoked certificate after restore returned %v, want ErrCertRevoked", err)
	}
	if got := restored.Stats().Rejected; got != 1 {
		t.Fatalf("restored rejection counter = %d, want 1", got)
	}
	if err := restored.Restore([]byte("}{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot returned %v, want ErrBadSnapshot", err)
	}
	if got := len(restored.Rules("corp")); got != len(rulesBefore) {
		t.Fatalf("rejected restore changed the rule set: %d rules", got)
	}
}
