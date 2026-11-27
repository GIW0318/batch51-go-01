package task

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-03"
)

// clock is the injected time source of the policy.
type clock struct {
	t time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time      { return c.t }
func (c *clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func policyAt(c *clock) impl.IssuerPolicy {
	return impl.IssuerPolicy{
		Realm:                  "urn:corp:federation",
		MaxLifetimeSeconds:     600,
		DefaultLifetimeSeconds: 300,
		RenewWindowSeconds:     120,
		MaxTokensPerPrincipal:  4,
		MaxTokensPerAudience:   8,
		MaxTombstones:          16,
		Now:                    c.Now,
	}
}

func issueRequest(principal, appliesTo, requestID string) impl.RequestSecurityToken {
	return impl.RequestSecurityToken{
		Principal:     principal,
		AppliesTo:     appliesTo,
		RequestID:     requestID,
		ClientEntropy: []byte("client-entropy-" + requestID),
	}
}

func mustIssue(t *testing.T, w *impl.WsTrustIssuer, req impl.RequestSecurityToken) impl.Response {
	t.Helper()
	r, err := w.Issue(req)
	if err != nil {
		t.Fatalf("issue %s for %s: %v", req.RequestID, req.Principal, err)
	}
	return r
}

func TestIssueReturnsBoundToken(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	r := mustIssue(t, w, issueRequest("alice", "urn:app:payroll", "req-1"))
	if r.TokenID == "" || r.Family == "" {
		t.Fatalf("token id %q family %q must both be set", r.TokenID, r.Family)
	}
	if r.Principal != "alice" || r.AppliesTo != "urn:app:payroll" {
		t.Fatalf("response bound to %q/%q, want alice/urn:app:payroll", r.Principal, r.AppliesTo)
	}
	if r.LifetimeSeconds != 300 {
		t.Fatalf("lifetime = %d seconds, want the policy default 300", r.LifetimeSeconds)
	}
	if !r.ExpiresAt.Equal(c.Now().Add(300 * time.Second)) {
		t.Fatalf("expiry = %v, want %v", r.ExpiresAt, c.Now().Add(300*time.Second))
	}
	if len(r.ProofKey) != 64 {
		t.Fatalf("proof key %q has %d characters, want 64 hex characters", r.ProofKey, len(r.ProofKey))
	}
	if _, err := hex.DecodeString(r.ProofKey); err != nil {
		t.Fatalf("proof key %q is not hexadecimal: %v", r.ProofKey, err)
	}
	if r.Renewed || r.Idempotent {
		t.Fatalf("fresh issue reported renewed=%v idempotent=%v", r.Renewed, r.Idempotent)
	}
	if got := w.TokensFor("alice"); got != 1 {
		t.Fatalf("live tokens for alice = %d, want 1", got)
	}
	if got := w.Stats().Issued; got != 1 {
		t.Fatalf("issued counter = %d, want 1", got)
	}
}

func TestIssueValidatesRequest(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	blank := issueRequest("   ", "urn:app:payroll", "req-blank")
	if _, err := w.Issue(blank); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("blank principal returned %v, want ErrMissingField", err)
	}
	noAud := issueRequest("alice", "", "req-noaud")
	if _, err := w.Issue(noAud); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("blank audience returned %v, want ErrMissingField", err)
	}
	if got := w.TokensFor("alice"); got != 0 {
		t.Fatalf("rejected requests created %d tokens, want 0", got)
	}
	explicit := issueRequest("alice", "urn:app:payroll", "req-2")
	explicit.LifetimeSeconds = 90
	r := mustIssue(t, w, explicit)
	if r.LifetimeSeconds != 90 {
		t.Fatalf("lifetime = %d seconds, want the requested 90", r.LifetimeSeconds)
	}
	if !r.ExpiresAt.Equal(c.Now().Add(90 * time.Second)) {
		t.Fatalf("expiry = %v, want %v", r.ExpiresAt, c.Now().Add(90*time.Second))
	}
}

func TestIssueRejectsOverlongLifetime(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	req := issueRequest("bob", "urn:app:payroll", "req-long")
	req.LifetimeSeconds = 601
	if _, err := w.Issue(req); !errors.Is(err, impl.ErrLifetimeTooLong) {
		t.Fatalf("lifetime above the cap returned %v, want ErrLifetimeTooLong", err)
	}
	if got := w.TokensFor("bob"); got != 0 {
		t.Fatalf("rejected request left %d live tokens, want 0", got)
	}
	stats := w.Stats()
	if stats.Issued != 0 || stats.Rejected != 1 {
		t.Fatalf("stats = %+v, want issued 0 rejected 1", stats)
	}
}

func TestIssueEnforcesPrincipalQuota(t *testing.T) {
	c := newClock()
	cfg := policyAt(c)
	cfg.MaxTokensPerPrincipal = 2
	w := impl.NewWsTrustIssuer(cfg)
	mustIssue(t, w, issueRequest("carol", "urn:app:payroll", "req-a"))
	mustIssue(t, w, issueRequest("carol", "urn:app:hr", "req-b"))
	third := issueRequest("carol", "urn:app:crm", "req-c")
	if _, err := w.Issue(third); !errors.Is(err, impl.ErrPrincipalQuota) {
		t.Fatalf("third token for one principal returned %v, want ErrPrincipalQuota", err)
	}
	if got := w.TokensFor("carol"); got != 2 {
		t.Fatalf("live tokens = %d, want the cap 2", got)
	}
	mustIssue(t, w, issueRequest("dave", "urn:app:crm", "req-d"))
}

func TestIssueEnforcesAudienceQuota(t *testing.T) {
	c := newClock()
	cfg := policyAt(c)
	cfg.MaxTokensPerAudience = 1
	w := impl.NewWsTrustIssuer(cfg)
	mustIssue(t, w, issueRequest("carol", "urn:app:payroll", "req-aud-1"))
	if _, err := w.Issue(issueRequest("dave", "urn:app:payroll", "req-aud-2")); !errors.Is(err, impl.ErrAudienceQuota) {
		t.Fatalf("second token for one audience returned %v, want ErrAudienceQuota", err)
	}
	mustIssue(t, w, issueRequest("dave", "urn:app:hr", "req-aud-3"))
}

func TestIssueIsIdempotentForRepeatedRequestID(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	first := mustIssue(t, w, issueRequest("erin", "urn:app:payroll", "req-retry"))
	second := mustIssue(t, w, issueRequest("erin", "urn:app:payroll", "req-retry"))
	if first.TokenID != second.TokenID {
		t.Fatalf("repeated request id produced token %q then %q, want one token", first.TokenID, second.TokenID)
	}
	if !second.Idempotent {
		t.Fatalf("the repeated answer must be marked idempotent")
	}
	if got := w.TokensFor("erin"); got != 1 {
		t.Fatalf("live tokens after a retry = %d, want 1", got)
	}
	if got := w.Stats().Issued; got != 1 {
		t.Fatalf("issued counter after a retry = %d, want 1", got)
	}
	other := mustIssue(t, w, issueRequest("erin", "urn:app:payroll", "req-fresh"))
	if other.TokenID == first.TokenID {
		t.Fatalf("a new request id must mint a new token")
	}
}

func TestDeriveKeyCombinesEntropy(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	server := []byte("server-entropy-01")
	client := []byte("client-entropy-01")
	got, err := w.DeriveKey(server, client)
	if err != nil {
		t.Fatalf("derive key: %v", err)
	}
	want := sha256.Sum256(append(append([]byte{}, server...), client...))
	if len(got) != len(want) {
		t.Fatalf("derived key length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("derived key differs from sha256(server||client) at byte %d", i)
		}
	}
	other, err := w.DeriveKey(server, []byte("client-entropy-02"))
	if err != nil {
		t.Fatalf("derive key with other client entropy: %v", err)
	}
	if len(other) == len(got) && string(other) == string(got) {
		t.Fatalf("a different client entropy must derive a different key")
	}
	if _, err := w.DeriveKey(nil, client); !errors.Is(err, impl.ErrEmptyEntropy) {
		t.Fatalf("empty server entropy returned %v, want ErrEmptyEntropy", err)
	}
}

func TestRenewExtendsTokenInWindow(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	req := issueRequest("frank", "urn:app:payroll", "req-r1")
	req.LifetimeSeconds = 60
	old := mustIssue(t, w, req)
	renew := impl.RequestSecurityToken{
		Principal: "frank",
		AppliesTo: "urn:app:payroll",
		TokenID:   old.TokenID,
		RequestID: "req-r1-renew",
	}
	next, err := w.Renew(renew)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !next.Renewed {
		t.Fatalf("renewal answer must be marked renewed")
	}
	if next.TokenID == old.TokenID {
		t.Fatalf("renewal reused the old token id %q", old.TokenID)
	}
	if next.Family != old.Family {
		t.Fatalf("renewal family = %q, want the original family %q", next.Family, old.Family)
	}
	trace, err := w.Explain(next.TokenID)
	if err != nil {
		t.Fatalf("explain renewed token: %v", err)
	}
	if trace.Renewals != 1 {
		t.Fatalf("renewal counter = %d, want 1", trace.Renewals)
	}
	if trace.State != impl.StateActive {
		t.Fatalf("renewed token state = %q, want %q", trace.State, impl.StateActive)
	}
	if _, err := w.Validate(old.TokenID, "urn:app:payroll"); !errors.Is(err, impl.ErrTokenSuperseded) {
		t.Fatalf("validate of the superseded token returned %v, want ErrTokenSuperseded", err)
	}
	if !w.IsRevoked(old.TokenID) {
		t.Fatalf("superseded token must be tombstoned")
	}
	if _, err := w.Validate(next.TokenID, "urn:app:payroll"); err != nil {
		t.Fatalf("renewed token must validate, got %v", err)
	}
	if got := w.Stats().Renewed; got != 1 {
		t.Fatalf("renewed counter = %d, want 1", got)
	}
}

func TestRenewRejectedOutsideWindow(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	req := issueRequest("grace", "urn:app:payroll", "req-w1")
	req.LifetimeSeconds = 300
	r := mustIssue(t, w, req)
	early := impl.RequestSecurityToken{TokenID: r.TokenID, AppliesTo: "urn:app:payroll"}
	if _, err := w.Renew(early); !errors.Is(err, impl.ErrRenewWindowClosed) {
		t.Fatalf("renew too early returned %v, want ErrRenewWindowClosed", err)
	}
	c.Advance(200 * time.Second)
	late := impl.RequestSecurityToken{TokenID: r.TokenID, AppliesTo: "urn:app:payroll"}
	if _, err := w.Renew(late); err != nil {
		t.Fatalf("renew inside the window: %v", err)
	}
	if _, err := w.Renew(impl.RequestSecurityToken{TokenID: "tok-404"}); !errors.Is(err, impl.ErrUnknownToken) {
		t.Fatalf("renew of an unknown token returned %v, want ErrUnknownToken", err)
	}
}

func TestRenewRefusesAudienceChange(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	req := issueRequest("heidi", "urn:app:payroll", "req-ac-1")
	req.LifetimeSeconds = 60
	r := mustIssue(t, w, req)
	if _, err := w.Renew(impl.RequestSecurityToken{TokenID: r.TokenID, AppliesTo: "urn:app:hr"}); !errors.Is(err, impl.ErrAudienceChange) {
		t.Fatalf("renew to another audience returned %v, want ErrAudienceChange", err)
	}
	if _, err := w.Validate(r.TokenID, "urn:app:payroll"); err != nil {
		t.Fatalf("a refused renewal must leave the original token alive, got %v", err)
	}
}

func TestValidateRejectsExpiredToken(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	req := issueRequest("ivan", "urn:app:payroll", "req-exp")
	req.LifetimeSeconds = 60
	r := mustIssue(t, w, req)
	claims, err := w.Validate(r.TokenID, "urn:app:payroll")
	if err != nil {
		t.Fatalf("validate fresh token: %v", err)
	}
	if claims.Principal != "ivan" || claims.Family != r.Family {
		t.Fatalf("claims = %+v, want ivan in family %q", claims, r.Family)
	}
	c.Advance(61 * time.Second)
	if _, err := w.Validate(r.TokenID, "urn:app:payroll"); !errors.Is(err, impl.ErrTokenExpired) {
		t.Fatalf("validate of an expired token returned %v, want ErrTokenExpired", err)
	}
	trace, err := w.Explain(r.TokenID)
	if err != nil {
		t.Fatalf("explain expired token: %v", err)
	}
	if trace.State != impl.StateExpired {
		t.Fatalf("expired token state = %q, want %q", trace.State, impl.StateExpired)
	}
}

func TestValidateChecksAudience(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	r := mustIssue(t, w, issueRequest("judy", "urn:app:payroll", "req-aud"))
	if _, err := w.Validate(r.TokenID, "urn:app:hr"); !errors.Is(err, impl.ErrAudienceMismatch) {
		t.Fatalf("validate for another audience returned %v, want ErrAudienceMismatch", err)
	}
	claims, err := w.Validate(r.TokenID, "urn:app:payroll")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.AppliesTo != "urn:app:payroll" {
		t.Fatalf("claims audience = %q, want urn:app:payroll", claims.AppliesTo)
	}
	if !claims.ExpiresAt.Equal(r.ExpiresAt) {
		t.Fatalf("claims expiry = %v, want %v", claims.ExpiresAt, r.ExpiresAt)
	}
	if _, err := w.Validate("tok-404", "urn:app:payroll"); !errors.Is(err, impl.ErrUnknownToken) {
		t.Fatalf("validate of an unknown token returned %v, want ErrUnknownToken", err)
	}
}

func TestCancelCascadesOverFamily(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	reqA := issueRequest("karl", "urn:app:payroll", "req-f1")
	reqA.LifetimeSeconds = 60
	first := mustIssue(t, w, reqA)
	reqB := issueRequest("karl", "urn:app:payroll", "req-f2")
	reqB.LifetimeSeconds = 60
	second := mustIssue(t, w, reqB)
	renewed, err := w.Renew(impl.RequestSecurityToken{TokenID: first.TokenID, AppliesTo: "urn:app:payroll"})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if got := w.TokensFor("karl"); got != 3 {
		t.Fatalf("live tokens before cancel = %d, want 3", got)
	}
	key, ok := w.KeyMaterial(renewed.TokenID)
	if !ok || key == "" {
		t.Fatalf("renewed token must expose its proof key")
	}
	if err := w.Cancel(renewed.TokenID, impl.ProofOfPossession{Key: key}); err != nil {
		t.Fatalf("cancel family: %v", err)
	}
	for _, id := range []string{first.TokenID, second.TokenID, renewed.TokenID} {
		if _, err := w.Validate(id, "urn:app:payroll"); !errors.Is(err, impl.ErrTokenRevoked) {
			t.Fatalf("validate of %s after family cancel returned %v, want ErrTokenRevoked", id, err)
		}
		if !w.IsRevoked(id) {
			t.Fatalf("token %s must be tombstoned after the family cancel", id)
		}
	}
	if got := w.TokensFor("karl"); got != 0 {
		t.Fatalf("live tokens after cancel = %d, want 0", got)
	}
	if got := w.Stats().Cancelled; got != 1 {
		t.Fatalf("cancelled counter = %d, want 1", got)
	}
}

func TestCancelRequiresMatchingProof(t *testing.T) {
	c := newClock()
	w := impl.NewWsTrustIssuer(policyAt(c))
	r := mustIssue(t, w, issueRequest("laura", "urn:app:payroll", "req-cancel"))
	if err := w.Cancel(r.TokenID, impl.ProofOfPossession{Key: "00"}); !errors.Is(err, impl.ErrProofMismatch) {
		t.Fatalf("cancel with a foreign proof returned %v, want ErrProofMismatch", err)
	}
	if _, err := w.Validate(r.TokenID, "urn:app:payroll"); err != nil {
		t.Fatalf("a refused cancel must leave the token alive, got %v", err)
	}
	if err := w.Cancel("tok-404", impl.ProofOfPossession{Key: "00"}); !errors.Is(err, impl.ErrUnknownToken) {
		t.Fatalf("cancel of an unknown token returned %v, want ErrUnknownToken", err)
	}
}

func TestSnapshotRestoreKeepsFamiliesAndTombstones(t *testing.T) {
	c := newClock()
	cfg := policyAt(c)
	w := impl.NewWsTrustIssuer(cfg)
	live := mustIssue(t, w, issueRequest("nina", "urn:app:payroll", "req-s1"))
	dead := mustIssue(t, w, issueRequest("nina", "urn:app:hr", "req-s2"))
	key, ok := w.KeyMaterial(dead.TokenID)
	if !ok {
		t.Fatalf("dead token must expose its proof key")
	}
	if err := w.Cancel(dead.TokenID, impl.ProofOfPossession{Key: key}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	data, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewWsTrustIssuer(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	claims, err := restored.Validate(live.TokenID, "urn:app:payroll")
	if err != nil {
		t.Fatalf("validate live token after restore: %v", err)
	}
	if claims.Family != live.Family {
		t.Fatalf("restored family = %q, want %q", claims.Family, live.Family)
	}
	if _, err := restored.Validate(dead.TokenID, "urn:app:hr"); !errors.Is(err, impl.ErrTokenRevoked) {
		t.Fatalf("validate revoked token after restore returned %v, want ErrTokenRevoked", err)
	}
	if !restored.IsRevoked(dead.TokenID) {
		t.Fatalf("restore lost the revocation tombstone")
	}
	if got, want := restored.TokensFor("nina"), 1; got != want {
		t.Fatalf("restored live tokens = %d, want %d", got, want)
	}
	if fam, ok := restored.FamilyOf(live.TokenID); !ok || fam != live.Family {
		t.Fatalf("restored family lookup = %q ok=%v, want %q", fam, ok, live.Family)
	}
	if err := restored.Restore([]byte("{oops")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot returned %v, want ErrBadSnapshot", err)
	}
	if _, err := restored.Validate(live.TokenID, "urn:app:payroll"); err != nil {
		t.Fatalf("rejected restore changed live state: %v", err)
	}
}
