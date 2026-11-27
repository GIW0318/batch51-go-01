package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-11"
)

var guardBase = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func testGuardConfig() impl.GuardConfig {
	return impl.GuardConfig{
		ChallengeTTL: 2 * time.Minute,
		MaxPending:   8,
		MaxAttempts:  3,
		Now:          func() time.Time { return guardBase },
	}
}

func standardFlags() uint32 {
	return impl.FlagUnicode | impl.FlagSign | impl.FlagNTLM |
		impl.FlagExtendedSession | impl.FlagTargetInfo
}

func mustChallenge(t *testing.T, g *impl.NtlmChallengeGuard, conn, account string) impl.Challenge {
	t.Helper()
	ch, err := g.Challenge(conn, account, standardFlags())
	if err != nil {
		t.Fatalf("challenge for %s/%s: %v", conn, account, err)
	}
	return ch
}

func echoResponse(ch impl.Challenge) impl.Response {
	return impl.Response{
		ConnID:         ch.ConnID,
		Account:        ch.Account,
		Nonce:          ch.Nonce,
		ClientFlags:    ch.Flags,
		TargetInfoEcho: ch.TargetInfo,
		ChannelBinding: "cbt:9f31",
		MicValid:       true,
		NtlmV2:         true,
	}
}

func TestChallengeIssuesNonceBoundToConnectionAndAccount(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	ch := mustChallenge(t, g, "conn-1", "alice")
	if ch.ConnID != "conn-1" {
		t.Fatalf("challenge conn = %q, want conn-1", ch.ConnID)
	}
	if ch.Account != "alice" {
		t.Fatalf("challenge account = %q, want alice", ch.Account)
	}
	if len(ch.Nonce) < 16 {
		t.Fatalf("nonce %q is too short, want at least 16 characters", ch.Nonce)
	}
	if !ch.ExpiresAt.Equal(guardBase.Add(2 * time.Minute)) {
		t.Fatalf("challenge expiry = %v, want issue instant plus the two minute TTL", ch.ExpiresAt)
	}
	if ch.Flags&impl.FlagSign == 0 {
		t.Fatalf("challenge flags %#x dropped the signing bit the server must force", ch.Flags)
	}
	if ch.Flags&impl.FlagExtendedSession == 0 {
		t.Fatalf("challenge flags %#x dropped the extended session bit", ch.Flags)
	}
	if ch.TargetInfo == "" {
		t.Fatalf("challenge carries no target info")
	}
	if got := g.Pending("conn-1"); got != 1 {
		t.Fatalf("pending after one challenge = %d, want 1", got)
	}
	other := mustChallenge(t, g, "conn-2", "bob")
	if other.Nonce == ch.Nonce {
		t.Fatalf("two challenges share the nonce %q", ch.Nonce)
	}
}

func TestVerifyAcceptsEchoedFlagsAndAdvancesSeq(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	first := mustChallenge(t, g, "conn-1", "alice")
	res, err := g.Verify(echoResponse(first))
	if err != nil {
		t.Fatalf("verify a matching response: %v", err)
	}
	if res.Account != "alice" || res.ConnID != "conn-1" {
		t.Fatalf("result = %+v, want alice on conn-1", res)
	}
	if res.Seq != 1 {
		t.Fatalf("first accepted seq = %d, want 1", res.Seq)
	}
	if res.Flags&impl.FlagSign == 0 {
		t.Fatalf("accepted flags %#x lost the signing bit", res.Flags)
	}
	if got := g.Pending("conn-1"); got != 0 {
		t.Fatalf("pending after a successful verify = %d, want 0", got)
	}
	second := mustChallenge(t, g, "conn-1", "alice")
	res2, err := g.Verify(echoResponse(second))
	if err != nil {
		t.Fatalf("verify the second handshake: %v", err)
	}
	if res2.Seq != 2 {
		t.Fatalf("second accepted seq = %d, want 2", res2.Seq)
	}
	st := g.Stats()
	if st.Issued != 2 || st.Accepted != 2 || st.Rejected != 0 {
		t.Fatalf("stats = %+v, want 2 issued and 2 accepted", st)
	}
}

func TestChallengeRejectsBlankIdentity(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	if _, err := g.Challenge("", "alice", standardFlags()); !errors.Is(err, impl.ErrBadIdentity) {
		t.Fatalf("blank connection must fail with ErrBadIdentity, got %v", err)
	}
	if _, err := g.Challenge("conn-1", "   ", standardFlags()); !errors.Is(err, impl.ErrBadIdentity) {
		t.Fatalf("blank account must fail with ErrBadIdentity, got %v", err)
	}
	if got := g.Stats().Issued; got != 0 {
		t.Fatalf("issued counter = %d after two rejected calls, want 0", got)
	}
}

func TestVerifyRejectsUnknownNonce(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	mustChallenge(t, g, "conn-1", "alice")
	resp := impl.Response{
		ConnID:      "conn-1",
		Account:     "alice",
		Nonce:       "0000000000000000",
		ClientFlags: standardFlags() | impl.FlagSign,
		MicValid:    true,
		NtlmV2:      true,
	}
	if _, err := g.Verify(resp); !errors.Is(err, impl.ErrUnknownChallenge) {
		t.Fatalf("an unseen nonce must fail with ErrUnknownChallenge, got %v", err)
	}
	if got := g.Stats().Rejected; got != 1 {
		t.Fatalf("rejected counter = %d, want 1", got)
	}
}

func TestVerifyRejectsReusedNonce(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	ch := mustChallenge(t, g, "conn-1", "alice")
	if _, err := g.Verify(echoResponse(ch)); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := g.Verify(echoResponse(ch)); !errors.Is(err, impl.ErrChallengeReused) {
		t.Fatalf("a second response on one challenge must fail with ErrChallengeReused, got %v", err)
	}
	if got := g.Stats().Accepted; got != 1 {
		t.Fatalf("accepted counter = %d after replaying a challenge, want 1", got)
	}
}

func TestVerifyRejectsNonceFromAnotherConnection(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	ch := mustChallenge(t, g, "conn-1", "alice")
	resp := echoResponse(ch)
	resp.ConnID = "conn-2"
	if _, err := g.Verify(resp); !errors.Is(err, impl.ErrChallengeMismatch) {
		t.Fatalf("a challenge echoed on the wrong connection must fail with ErrChallengeMismatch, got %v", err)
	}
	if got := g.Pending("conn-1"); got != 1 {
		t.Fatalf("pending after a mismatched attempt = %d, want the challenge kept for 1", got)
	}
	if _, err := g.Verify(echoResponse(ch)); err != nil {
		t.Fatalf("the rightful connection must still verify: %v", err)
	}
}

func TestVerifyRejectsExpiredChallenge(t *testing.T) {
	now := guardBase
	cfg := testGuardConfig()
	cfg.Now = func() time.Time { return now }
	g := impl.NewNtlmChallengeGuard(cfg)
	ch := mustChallenge(t, g, "conn-1", "alice")
	now = guardBase.Add(2*time.Minute + time.Second)
	if _, err := g.Verify(echoResponse(ch)); !errors.Is(err, impl.ErrChallengeExpired) {
		t.Fatalf("a challenge past its deadline must fail with ErrChallengeExpired, got %v", err)
	}
	now = guardBase
	if _, err := g.Verify(echoResponse(ch)); !errors.Is(err, impl.ErrChallengeExpired) {
		t.Fatalf("an expired challenge must stay expired when the clock moves back, got %v", err)
	}
	if got := g.Stats().Expired; got != 1 {
		t.Fatalf("expired counter = %d, want 1", got)
	}
}

func TestVerifyRejectsSigningDowngrade(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	ch := mustChallenge(t, g, "conn-1", "alice")
	resp := echoResponse(ch)
	resp.ClientFlags = ch.Flags &^ impl.FlagSign
	if _, err := g.Verify(resp); !errors.Is(err, impl.ErrDowngradeRefused) {
		t.Fatalf("dropping the signing bit must fail with ErrDowngradeRefused, got %v", err)
	}
	if got := g.Pending("conn-1"); got != 1 {
		t.Fatalf("pending after a downgrade attempt = %d, want the challenge kept for 1", got)
	}
}

func TestVerifyRejectsMissingChannelBinding(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	if err := g.SetPolicy(impl.Policy{
		Account:               "alice",
		RequireSigning:        true,
		RequireNTLMv2:         true,
		RequireChannelBinding: true,
	}); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if p := g.Policy("alice"); !p.RequireChannelBinding {
		t.Fatalf("stored policy = %+v, want channel binding required", p)
	}
	if p := g.Policy("bob"); !p.RequireSigning || p.RequireChannelBinding {
		t.Fatalf("default policy = %+v, want signing required and channel binding optional", p)
	}
	ch := mustChallenge(t, g, "conn-1", "alice")
	resp := echoResponse(ch)
	resp.ChannelBinding = ""
	if _, err := g.Verify(resp); !errors.Is(err, impl.ErrChannelBindingRequired) {
		t.Fatalf("a missing channel binding must fail with ErrChannelBindingRequired, got %v", err)
	}
	retry := echoResponse(ch)
	if _, err := g.Verify(retry); err != nil {
		t.Fatalf("a response carrying the channel binding must verify: %v", err)
	}
}

func TestVerifyRejectsWeakResponse(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	ch := mustChallenge(t, g, "conn-1", "alice")
	resp := echoResponse(ch)
	resp.NtlmV2 = false
	if _, err := g.Verify(resp); !errors.Is(err, impl.ErrWeakResponse) {
		t.Fatalf("an NTLMv1 shaped response must fail with ErrWeakResponse, got %v", err)
	}
	badMic := echoResponse(ch)
	badMic.MicValid = false
	if _, err := g.Verify(badMic); !errors.Is(err, impl.ErrMicInvalid) {
		t.Fatalf("a failed integrity check must fail with ErrMicInvalid, got %v", err)
	}
	if got := g.Stats().Accepted; got != 0 {
		t.Fatalf("accepted counter = %d after weak responses, want 0", got)
	}
}

func TestAccountLocksAfterAttemptBudget(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	for i := 0; i < 3; i++ {
		ch := mustChallenge(t, g, "conn-1", "alice")
		resp := echoResponse(ch)
		resp.MicValid = false
		if _, err := g.Verify(resp); !errors.Is(err, impl.ErrMicInvalid) {
			t.Fatalf("attempt %d: want ErrMicInvalid, got %v", i+1, err)
		}
	}
	if _, err := g.Challenge("conn-1", "alice", standardFlags()); !errors.Is(err, impl.ErrAccountLocked) {
		t.Fatalf("a locked account must not mint new challenges, got %v", err)
	}
	if _, err := g.Challenge("conn-9", "alice", standardFlags()); !errors.Is(err, impl.ErrAccountLocked) {
		t.Fatalf("the lock must follow the account across connections, got %v", err)
	}
	if got := g.Stats().Locked; got != 1 {
		t.Fatalf("locked counter = %d, want 1", got)
	}
}

func TestPendingLimitRefusesExtraChallenges(t *testing.T) {
	cfg := testGuardConfig()
	cfg.MaxPending = 2
	g := impl.NewNtlmChallengeGuard(cfg)
	mustChallenge(t, g, "conn-1", "alice")
	mustChallenge(t, g, "conn-2", "bob")
	if _, err := g.Challenge("conn-3", "carol", standardFlags()); !errors.Is(err, impl.ErrPendingLimit) {
		t.Fatalf("a third pending challenge must fail with ErrPendingLimit, got %v", err)
	}
	if got := g.Pending("conn-3"); got != 0 {
		t.Fatalf("pending for the refused connection = %d, want 0", got)
	}
}

func TestResetConnectionDropsPendingState(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	first := mustChallenge(t, g, "conn-1", "alice")
	second := mustChallenge(t, g, "conn-2", "bob")
	if got := g.ResetConnection("conn-1"); got != 1 {
		t.Fatalf("reset removed %d entries, want 1", got)
	}
	if got := g.Pending("conn-1"); got != 0 {
		t.Fatalf("pending after reset = %d, want 0", got)
	}
	if _, err := g.Verify(echoResponse(first)); !errors.Is(err, impl.ErrUnknownChallenge) {
		t.Fatalf("a challenge dropped by reset must not verify, got %v", err)
	}
	if got := g.Pending("conn-2"); got != 1 {
		t.Fatalf("reset must not touch other connections, pending = %d want 1", got)
	}
	if _, err := g.Verify(echoResponse(second)); err != nil {
		t.Fatalf("the untouched connection must still verify: %v", err)
	}
}

func TestAuditAndStatsTrackOutcomes(t *testing.T) {
	g := impl.NewNtlmChallengeGuard(testGuardConfig())
	ch := mustChallenge(t, g, "conn-1", "alice")
	if _, err := g.Verify(echoResponse(ch)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	unknown := impl.Response{
		ConnID:      "conn-1",
		Account:     "alice",
		Nonce:       "ffffffffffffffff",
		ClientFlags: standardFlags(),
		MicValid:    true,
		NtlmV2:      true,
	}
	if _, err := g.Verify(unknown); !errors.Is(err, impl.ErrUnknownChallenge) {
		t.Fatalf("unknown nonce: %v", err)
	}
	events := g.Audit("conn-1")
	if len(events) != 2 {
		t.Fatalf("audit length = %d, want 2 (%+v)", len(events), events)
	}
	if events[0].Outcome != impl.OutcomeAccepted || events[0].Account != "alice" {
		t.Fatalf("first audit event = %+v, want an accepted one for alice", events[0])
	}
	if events[1].Outcome != impl.OutcomeRejected {
		t.Fatalf("second audit event = %+v, want a rejected one", events[1])
	}
	if events[1].Reason == "" {
		t.Fatalf("a rejected event must carry a reason")
	}
	if len(g.Audit("conn-2")) != 0 {
		t.Fatalf("audit for an idle connection must be empty")
	}
}

func TestSnapshotRestoreKeepsLedgerAndSeq(t *testing.T) {
	cfg := testGuardConfig()
	g := impl.NewNtlmChallengeGuard(cfg)
	first := mustChallenge(t, g, "conn-1", "alice")
	if _, err := g.Verify(echoResponse(first)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	pending := mustChallenge(t, g, "conn-2", "bob")
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewNtlmChallengeGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Pending("conn-2"); got != 1 {
		t.Fatalf("pending after restore = %d, want the minted challenge back", got)
	}
	if err := restored.Restore([]byte("{this is not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Pending("conn-2"); got != 1 {
		t.Fatalf("a rejected restore changed the ledger, pending = %d want 1", got)
	}
	res, err := restored.Verify(echoResponse(pending))
	if err != nil {
		t.Fatalf("verify after restore: %v", err)
	}
	if res.Seq != 2 {
		t.Fatalf("seq after restore = %d, want it to continue at 2", res.Seq)
	}
}
