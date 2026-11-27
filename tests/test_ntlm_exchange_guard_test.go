package task

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	impl "gsb/batch51-go-safety-a-01"
)

func guardConfig() impl.GuardConfig {
	return impl.GuardConfig{
		MaxPending:            8,
		MaxPerSource:          4,
		MaxReputation:         50,
		MaxAuditEvents:        64,
		RequireChannelBinding: true,
	}
}

func sourceAt(n int) impl.Endpoint {
	return impl.Endpoint{
		Addr: fmt.Sprintf("10.20.0.%d:51000", n),
		Host: fmt.Sprintf("ws-%02d.corp.example", n),
	}
}

func micOf(nonce, clientNonce, user string) string {
	sum := sha256.Sum256([]byte(nonce + "|" + clientNonce + "|" + user))
	return hex.EncodeToString(sum[:])
}

func bindingOf(label string) []byte {
	return []byte("tls-server-end-point:" + label)
}

// goodMessage builds an authenticate message that satisfies every documented
// precondition of one live challenge.
func goodMessage(ch impl.ChallengeMsg, user, binding string) impl.AuthenticateMsg {
	clientNonce := "a1b2c3d4e5f60718"
	return impl.AuthenticateMsg{
		User:            user,
		Domain:          "CORP",
		Version:         2,
		Flags:           impl.FlagUnicode | impl.FlagNTLM | impl.FlagExtSession | impl.FlagChannelBound,
		ChallengeNonce:  ch.Nonce,
		ClientNonce:     clientNonce,
		Response:        "9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a3928",
		MIC:             micOf(ch.Nonce, clientNonce, user),
		ChannelBindings: bindingOf(binding),
	}
}

func TestBeginIssuesChallengeBoundToConnection(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ep := sourceAt(1)
	ch, err := g.Begin("conn-1", ep)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if len(ch.Nonce) != 32 {
		t.Fatalf("challenge nonce %q has %d characters, want 32 hex characters", ch.Nonce, len(ch.Nonce))
	}
	if _, err := hex.DecodeString(ch.Nonce); err != nil {
		t.Fatalf("challenge nonce %q is not hexadecimal: %v", ch.Nonce, err)
	}
	if ch.ConnectionID != "conn-1" || ch.SourceAddr != ep.Addr {
		t.Fatalf("challenge bound to conn=%q src=%q, want conn-1 and %q", ch.ConnectionID, ch.SourceAddr, ep.Addr)
	}
	want := impl.FlagUnicode | impl.FlagNTLM | impl.FlagExtSession
	if ch.Flags&want != want {
		t.Fatalf("challenge flags %#x miss the mandatory bits %#x", ch.Flags, want)
	}
	if got := g.Pending(); got != 1 {
		t.Fatalf("pending handshakes = %d, want 1", got)
	}
	again, ok := g.ChallengeFor("conn-1")
	if !ok {
		t.Fatalf("a pending connection must expose its challenge")
	}
	if again.Nonce != ch.Nonce {
		t.Fatalf("challenge nonce changed between begin and lookup: %q then %q", ch.Nonce, again.Nonce)
	}
	if _, ok := g.ChallengeFor("conn-404"); ok {
		t.Fatalf("unknown connection must not expose a challenge")
	}
}

func TestBeginRejectsBlankConnection(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	for _, conn := range []string{"", "   ", "\t\n"} {
		if _, err := g.Begin(conn, sourceAt(2)); !errors.Is(err, impl.ErrEmptyConnection) {
			t.Fatalf("begin with connection id %q returned %v, want ErrEmptyConnection", conn, err)
		}
	}
	if got := g.Pending(); got != 0 {
		t.Fatalf("rejected begins left %d pending handshakes, want 0", got)
	}
	if _, err := g.Begin("conn-2", sourceAt(3)); err != nil {
		t.Fatalf("first begin: %v", err)
	}
	if _, err := g.Begin("conn-2", sourceAt(3)); !errors.Is(err, impl.ErrHandshakeReused) {
		t.Fatalf("second begin on the same connection returned %v, want ErrHandshakeReused", err)
	}
	if got := g.Pending(); got != 1 {
		t.Fatalf("pending handshakes = %d, want the single original handshake", got)
	}
}

func TestBeginEnforcesPerSourceLimit(t *testing.T) {
	cfg := guardConfig()
	cfg.MaxPerSource = 2
	cfg.MaxPending = 16
	g := impl.NewNtlmExchangeGuard(cfg)
	ep := sourceAt(4)
	for i := 0; i < 2; i++ {
		if _, err := g.Begin(fmt.Sprintf("conn-src-%d", i), ep); err != nil {
			t.Fatalf("begin %d from one source: %v", i, err)
		}
	}
	if _, err := g.Begin("conn-src-2", ep); !errors.Is(err, impl.ErrSourceLimit) {
		t.Fatalf("third pending handshake from one source returned %v, want ErrSourceLimit", err)
	}
	if _, err := g.Begin("conn-src-9", sourceAt(5)); err != nil {
		t.Fatalf("a different source must still negotiate, got %v", err)
	}
	if got := g.Pending(); got != 3 {
		t.Fatalf("pending handshakes = %d, want 3", got)
	}
}

func TestPendingLimitEvictsOldestHandshake(t *testing.T) {
	cfg := guardConfig()
	cfg.MaxPending = 2
	cfg.MaxPerSource = 8
	g := impl.NewNtlmExchangeGuard(cfg)
	first, err := g.Begin("conn-old", sourceAt(6))
	if err != nil {
		t.Fatalf("begin oldest: %v", err)
	}
	if _, err := g.Begin("conn-mid", sourceAt(7)); err != nil {
		t.Fatalf("begin middle: %v", err)
	}
	if _, err := g.Begin("conn-new", sourceAt(8)); err != nil {
		t.Fatalf("begin newest: %v", err)
	}
	if got := g.Pending(); got != 2 {
		t.Fatalf("pending handshakes = %d, want the cap 2", got)
	}
	if _, err := g.Accept("conn-old", goodMessage(first, "alice", "lbl-old")); !errors.Is(err, impl.ErrHandshakeLost) {
		t.Fatalf("accept on the evicted handshake returned %v, want ErrHandshakeLost", err)
	}
	if got := g.Stats().Evicted; got != 1 {
		t.Fatalf("evicted counter = %d, want 1", got)
	}
}

func TestAcceptRejectsNTLMv1AndChargesReputation(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ep := sourceAt(9)
	ch, err := g.Begin("conn-v1", ep)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.BindChannel("conn-v1", bindingOf("v1")); err != nil {
		t.Fatalf("bind channel: %v", err)
	}
	msg := goodMessage(ch, "bob", "v1")
	msg.Version = 1
	if _, err := g.Accept("conn-v1", msg); !errors.Is(err, impl.ErrWeakResponse) {
		t.Fatalf("NTLMv1 answer returned %v, want ErrWeakResponse", err)
	}
	if got := g.ReputationFor(ep.Addr); got <= 0 {
		t.Fatalf("reputation of the source = %d, want a positive failure score", got)
	}
	if got := g.Stats().Rejected; got != 1 {
		t.Fatalf("rejected counter = %d, want 1", got)
	}
}

func TestAcceptRejectsAnonymousSession(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ch, err := g.Begin("conn-anon", sourceAt(10))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.BindChannel("conn-anon", bindingOf("anon")); err != nil {
		t.Fatalf("bind channel: %v", err)
	}
	msg := goodMessage(ch, "   ", "anon")
	if _, err := g.Accept("conn-anon", msg); !errors.Is(err, impl.ErrAnonymousSession) {
		t.Fatalf("blank user returned %v, want ErrAnonymousSession", err)
	}
	ch2, err := g.Begin("conn-anon2", sourceAt(11))
	if err != nil {
		t.Fatalf("begin second: %v", err)
	}
	msg2 := goodMessage(ch2, "carol", "anon2")
	msg2.Flags |= impl.FlagAnonymous
	if _, err := g.Accept("conn-anon2", msg2); !errors.Is(err, impl.ErrAnonymousSession) {
		t.Fatalf("anonymous flag returned %v, want ErrAnonymousSession", err)
	}
}

func TestAcceptRefusesDowngradedNegotiation(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ch, err := g.Begin("conn-dg", sourceAt(12))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	msg := goodMessage(ch, "dave", "dg")
	msg.Flags = impl.FlagUnicode | impl.FlagNTLM
	if _, err := g.Accept("conn-dg", msg); !errors.Is(err, impl.ErrDowngradeRefused) {
		t.Fatalf("answer without extended session security returned %v, want ErrDowngradeRefused", err)
	}
	ch2, err := g.Begin("conn-dg2", sourceAt(13))
	if err != nil {
		t.Fatalf("begin second: %v", err)
	}
	msg2 := goodMessage(ch2, "dave", "dg2")
	msg2.Flags |= impl.FlagLMKey | impl.FlagOEM
	if _, err := g.Accept("conn-dg2", msg2); !errors.Is(err, impl.ErrDowngradeRefused) {
		t.Fatalf("answer carrying LM key returned %v, want ErrDowngradeRefused", err)
	}
}

func TestAcceptVerifiesMessageIntegrity(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ch, err := g.Begin("conn-mic", sourceAt(14))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.BindChannel("conn-mic", bindingOf("mic")); err != nil {
		t.Fatalf("bind channel: %v", err)
	}
	msg := goodMessage(ch, "erin", "mic")
	msg.MIC = micOf(ch.Nonce, "ffffffffffffffff", "erin")
	if _, err := g.Accept("conn-mic", msg); !errors.Is(err, impl.ErrMicMismatch) {
		t.Fatalf("answer with a foreign MIC returned %v, want ErrMicMismatch", err)
	}
	ch2, err := g.Begin("conn-mic2", sourceAt(15))
	if err != nil {
		t.Fatalf("begin second: %v", err)
	}
	if err := g.BindChannel("conn-mic2", bindingOf("mic2")); err != nil {
		t.Fatalf("bind channel second: %v", err)
	}
	msg2 := goodMessage(ch2, "erin", "mic2")
	msg2.MIC = ""
	if _, err := g.Accept("conn-mic2", msg2); !errors.Is(err, impl.ErrMicMismatch) {
		t.Fatalf("answer without a MIC returned %v, want ErrMicMismatch", err)
	}
}

func TestAcceptHappyPathReturnsPrincipal(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ep := sourceAt(16)
	ch, err := g.Begin("conn-ok", ep)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.BindChannel("conn-ok", bindingOf("ok")); err != nil {
		t.Fatalf("bind channel: %v", err)
	}
	msg := goodMessage(ch, "frank", "ok")
	p, err := g.Accept("conn-ok", msg)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if p.Account != "frank" || p.Domain != "CORP" {
		t.Fatalf("principal account=%q domain=%q, want frank/CORP", p.Account, p.Domain)
	}
	if p.Source != ep.Addr {
		t.Fatalf("principal source = %q, want %q", p.Source, ep.Addr)
	}
	if p.Sequence <= 0 {
		t.Fatalf("principal sequence = %d, want a positive audit sequence", p.Sequence)
	}
	if got := g.Pending(); got != 0 {
		t.Fatalf("pending handshakes after accept = %d, want 0", got)
	}
	if got := g.Stats().Accepted; got != 1 {
		t.Fatalf("accepted counter = %d, want 1", got)
	}
	if _, err := g.Accept("conn-ok", msg); !errors.Is(err, impl.ErrHandshakeConsumed) {
		t.Fatalf("second accept on the same connection returned %v, want ErrHandshakeConsumed", err)
	}
}

func TestAcceptComparesChannelBinding(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ch, err := g.Begin("conn-cbt", sourceAt(17))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.BindChannel("conn-cbt", bindingOf("expected")); err != nil {
		t.Fatalf("bind channel: %v", err)
	}
	msg := goodMessage(ch, "grace", "expected")
	msg.ChannelBindings = bindingOf("other-terminator")
	if _, err := g.Accept("conn-cbt", msg); !errors.Is(err, impl.ErrChannelMismatch) {
		t.Fatalf("answer with a different channel binding returned %v, want ErrChannelMismatch", err)
	}
}

func TestBindChannelRequiresLiveHandshake(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	if err := g.BindChannel("conn-404", bindingOf("x")); !errors.Is(err, impl.ErrHandshakeNotFound) {
		t.Fatalf("binding an unknown connection returned %v, want ErrHandshakeNotFound", err)
	}
	if err := g.BindChannel("conn-404", nil); !errors.Is(err, impl.ErrHandshakeNotFound) {
		t.Fatalf("binding an unknown connection with an empty value returned %v, want ErrHandshakeNotFound", err)
	}
	ch, err := g.Begin("conn-cbt2", sourceAt(18))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.BindChannel("conn-cbt2", nil); !errors.Is(err, impl.ErrChannelBindingsGone) {
		t.Fatalf("empty channel binding returned %v, want ErrChannelBindingsGone", err)
	}
	if err := g.BindChannel("conn-cbt2", bindingOf("cbt2")); err != nil {
		t.Fatalf("bind channel after a rejected empty value: %v", err)
	}
	msg := goodMessage(ch, "heidi", "cbt2")
	msg.ChannelBindings = nil
	if _, err := g.Accept("conn-cbt2", msg); !errors.Is(err, impl.ErrChannelBindingsGone) {
		t.Fatalf("answer omitting a required channel binding returned %v, want ErrChannelBindingsGone", err)
	}
}

func TestAbortIsIdempotentAndBlocksAccept(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ch, err := g.Begin("conn-ab", sourceAt(19))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.Abort("conn-ab", "gateway policy"); err != nil {
		t.Fatalf("first abort: %v", err)
	}
	if err := g.Abort("conn-ab", "retry of the same alert"); err != nil {
		t.Fatalf("second abort must be idempotent, got %v", err)
	}
	if got := g.Pending(); got != 0 {
		t.Fatalf("pending handshakes after abort = %d, want 0", got)
	}
	if _, err := g.Accept("conn-ab", goodMessage(ch, "ivan", "ab")); !errors.Is(err, impl.ErrHandshakeAborted) {
		t.Fatalf("accept after abort returned %v, want ErrHandshakeAborted", err)
	}
	if err := g.Abort("conn-404", "unknown"); !errors.Is(err, impl.ErrHandshakeNotFound) {
		t.Fatalf("abort of an unknown connection returned %v, want ErrHandshakeNotFound", err)
	}
	if got := g.Stats().Aborted; got != 1 {
		t.Fatalf("aborted counter = %d, want 1 after one idempotent abort", got)
	}
}

func TestAuditSequenceCoversEveryTransition(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ch, err := g.Begin("conn-au", sourceAt(20))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.BindChannel("conn-au", bindingOf("au")); err != nil {
		t.Fatalf("bind channel: %v", err)
	}
	if _, err := g.Accept("conn-au", goodMessage(ch, "judy", "au")); err != nil {
		t.Fatalf("accept: %v", err)
	}
	events := g.Audit("conn-au")
	if len(events) != 2 {
		t.Fatalf("audit events = %d, want begin and accept", len(events))
	}
	if events[0].Action != impl.ActionBegin || events[1].Action != impl.ActionAccept {
		t.Fatalf("audit actions = %q,%q, want %q,%q", events[0].Action, events[1].Action, impl.ActionBegin, impl.ActionAccept)
	}
	if events[1].Seq <= events[0].Seq {
		t.Fatalf("audit sequence not increasing: %d then %d", events[0].Seq, events[1].Seq)
	}
	ch2, err := g.Begin("conn-au2", sourceAt(21))
	if err != nil {
		t.Fatalf("begin second: %v", err)
	}
	msg := goodMessage(ch2, "judy", "au2")
	msg.Version = 1
	if _, err := g.Accept("conn-au2", msg); !errors.Is(err, impl.ErrWeakResponse) {
		t.Fatalf("expected the weak answer to be rejected, got %v", err)
	}
	events2 := g.Audit("conn-au2")
	if len(events2) != 2 || events2[1].Action != impl.ActionReject {
		t.Fatalf("rejected connection audit = %+v, want begin followed by reject", events2)
	}
	if events2[1].Seq <= events[1].Seq {
		t.Fatalf("audit sequence restarted per connection: %d after %d", events2[1].Seq, events[1].Seq)
	}
	if len(g.Audit("conn-404")) != 0 {
		t.Fatalf("unknown connection must have an empty audit log")
	}
	stats := g.Stats()
	if stats.Begun != 2 || stats.Accepted != 1 || stats.Rejected != 1 {
		t.Fatalf("stats = %+v, want begun 2 accepted 1 rejected 1", stats)
	}
}

func TestSnapshotRestoreKeepsChallengesAndReputation(t *testing.T) {
	cfg := guardConfig()
	g := impl.NewNtlmExchangeGuard(cfg)
	ch, err := g.Begin("conn-snap", sourceAt(22))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.BindChannel("conn-snap", bindingOf("snap")); err != nil {
		t.Fatalf("bind channel: %v", err)
	}
	msg := goodMessage(ch, "karl", "snap")
	msg.Version = 1
	if _, err := g.Accept("conn-snap", msg); !errors.Is(err, impl.ErrWeakResponse) {
		t.Fatalf("expected the weak answer to be rejected, got %v", err)
	}
	if _, err := g.Begin("conn-live", sourceAt(23)); err != nil {
		t.Fatalf("begin live: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewNtlmExchangeGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, want := restored.Pending(), g.Pending(); got != want || got != 1 {
		t.Fatalf("restored pending = %d, want %d", got, want)
	}
	if got, want := restored.ReputationFor(sourceAt(22).Addr), g.ReputationFor(sourceAt(22).Addr); got != want {
		t.Fatalf("restored reputation = %d, want %d", got, want)
	}
	if got, want := restored.Stats(), g.Stats(); got != want {
		t.Fatalf("restored stats = %+v, want %+v", got, want)
	}
	if got, want := len(restored.Audit("conn-snap")), len(g.Audit("conn-snap")); got != want {
		t.Fatalf("restored audit length = %d, want %d", got, want)
	}
	next, err := restored.Begin("conn-after", sourceAt(24))
	if err != nil {
		t.Fatalf("begin after restore: %v", err)
	}
	last := restored.Audit("conn-after")
	before := g.Audit("conn-live")
	if len(last) == 0 || len(before) == 0 {
		t.Fatalf("expected audit events for the live connection before and after restore")
	}
	if last[0].Seq <= before[0].Seq {
		t.Fatalf("audit sequence did not continue after restore: %d after %d", last[0].Seq, before[0].Seq)
	}
	if next.SourceAddr != sourceAt(24).Addr {
		t.Fatalf("challenge after restore bound to %q", next.SourceAddr)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot returned %v, want ErrBadSnapshot", err)
	}
	if got := restored.Pending(); got != 2 {
		t.Fatalf("rejected restore changed live state, pending = %d want 2", got)
	}
}

func TestConcurrentAcceptOnlyOneWins(t *testing.T) {
	g := impl.NewNtlmExchangeGuard(guardConfig())
	ch, err := g.Begin("conn-race", sourceAt(25))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := g.BindChannel("conn-race", bindingOf("race")); err != nil {
		t.Fatalf("bind channel: %v", err)
	}
	msg := goodMessage(ch, "laura", "race")
	var wins, consumed int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.Accept("conn-race", msg)
			switch {
			case err == nil:
				atomic.AddInt64(&wins, 1)
			case errors.Is(err, impl.ErrHandshakeConsumed):
				atomic.AddInt64(&consumed, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("concurrent accepts produced %d principals, want exactly 1", wins)
	}
	if consumed != 7 {
		t.Fatalf("concurrent accepts reported %d consumed errors, want 7", consumed)
	}
	if got := g.Stats().Accepted; got != 1 {
		t.Fatalf("accepted counter = %d, want 1", got)
	}
}
