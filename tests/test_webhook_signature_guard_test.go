package task

import (
	"errors"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-r-05"
)

const (
	testWindow    = int64(60)
	testSecretOne = "channel-secret-alpha"
	testSecretTwo = "channel-secret-beta"
)

func newTestGuard(t *testing.T, now *int64) *impl.WebhookSignatureGuard {
	t.Helper()
	g, err := impl.NewWebhookSignatureGuard(impl.GuardConfig{
		WindowSeconds: testWindow,
		MaxAudit:      8,
		MaxReplay:     64,
		Clock:         func() int64 { return *now },
	})
	if err != nil {
		t.Fatalf("building the guard must succeed, got %v", err)
	}
	if g == nil {
		t.Fatalf("guard must not be nil")
	}
	return g
}

func signedBody() []byte {
	return []byte(`{"event":"paid","order":"A-1001","amount":1200}`)
}

func TestNewGuardRejectsBadConfig(t *testing.T) {
	if _, err := impl.NewWebhookSignatureGuard(impl.GuardConfig{WindowSeconds: 0, MaxAudit: 4, MaxReplay: 4}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero window must be rejected with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewWebhookSignatureGuard(impl.GuardConfig{WindowSeconds: 30, MaxAudit: 0, MaxReplay: 4}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero audit cap must be rejected with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewWebhookSignatureGuard(impl.GuardConfig{WindowSeconds: 30, MaxAudit: 4, MaxReplay: 0}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero replay cap must be rejected with ErrBadConfig, got %v", err)
	}
	now := int64(1000)
	g := newTestGuard(t, &now)
	if got := g.KeyCount(); got != 0 {
		t.Fatalf("fresh guard key count = %d, want 0", got)
	}
	if got := g.ReplayEntries(); got != 0 {
		t.Fatalf("fresh guard replay count = %d, want 0", got)
	}
}

func TestAddKeyValidatesVersionAndSecret(t *testing.T) {
	now := int64(2000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("   ", []byte(testSecretOne)); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank version must be rejected with ErrBlankKey, got %v", err)
	}
	if err := g.AddKey("v1", nil); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("empty secret must be rejected with ErrBlankKey, got %v", err)
	}
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("re-registering the same secret must be idempotent, got %v", err)
	}
	if err := g.AddKey("v1", []byte(testSecretTwo)); !errors.Is(err, impl.ErrKeyConflict) {
		t.Fatalf("a second secret for one version must report ErrKeyConflict, got %v", err)
	}
	if got := g.KeyCount(); got != 1 {
		t.Fatalf("key count = %d, want 1", got)
	}
	if err := g.AddKey("v2", []byte(testSecretTwo)); err != nil {
		t.Fatalf("second version: %v", err)
	}
	if got := g.CurrentVersion(); got != "v2" {
		t.Fatalf("current version = %q, want v2", got)
	}
	if got := g.KeyCount(); got != 2 {
		t.Fatalf("key count = %d, want 2", got)
	}
}

func TestSignRejectsUnknownVersion(t *testing.T) {
	now := int64(3000)
	g := newTestGuard(t, &now)
	if _, err := g.Sign("v9", now, signedBody()); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("signing with an unknown version must report ErrUnknownKey, got %v", err)
	}
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	sig, err := g.Sign("v1", now, signedBody())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(sig) != 64 {
		t.Fatalf("signature length = %d, want 64 lower case hex characters", len(sig))
	}
	if sig != strings.ToLower(sig) {
		t.Fatalf("signature must be lower case hex, got %q", sig)
	}
	again, err := g.Sign("v1", now, signedBody())
	if err != nil {
		t.Fatalf("signing twice: %v", err)
	}
	if again != sig {
		t.Fatalf("same input produced %q then %q", sig, again)
	}
	other, err := g.Sign("v1", now, []byte(`{"event":"paid","order":"A-1002","amount":1200}`))
	if err != nil {
		t.Fatalf("signing another body: %v", err)
	}
	if other == sig {
		t.Fatalf("different bodies must not share a signature")
	}
}

func TestVerifyAcceptsFreshSignature(t *testing.T) {
	now := int64(4000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	sig, err := g.Sign("v1", now, signedBody())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	v, err := g.Verify(impl.SignedRequest{Version: "v1", Timestamp: now, Signature: sig, Body: signedBody()})
	if err != nil {
		t.Fatalf("a fresh request must be accepted, got %v", err)
	}
	if !v.Accepted {
		t.Fatalf("verdict marked the fresh request as refused")
	}
	if v.Reason != impl.ReasonSignatureOK {
		t.Fatalf("reason = %q, want %q", v.Reason, impl.ReasonSignatureOK)
	}
	if v.Version != "v1" {
		t.Fatalf("verdict version = %q, want v1", v.Version)
	}
	if v.Seq <= 0 {
		t.Fatalf("verdict sequence = %d, want a positive number", v.Seq)
	}
	stats := g.Stats()
	if stats.Accepted != 1 || stats.Rejected != 0 || stats.Replayed != 0 || stats.Keys != 1 {
		t.Fatalf("stats after one accepted call = %+v, want accepted=1 rejected=0 replayed=0 keys=1", stats)
	}
	audit := g.Audit()
	if len(audit) != 1 {
		t.Fatalf("audit length = %d, want 1", len(audit))
	}
	if !audit[0].Accepted || audit[0].Seq != v.Seq || audit[0].Signature != sig {
		t.Fatalf("audit record = %+v, want the accepted call with seq %d", audit[0], v.Seq)
	}
}

func TestVerifyRejectsStaleTimestamp(t *testing.T) {
	now := int64(5000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	stale := now - testWindow - 1
	sig, err := g.Sign("v1", stale, signedBody())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := g.Verify(impl.SignedRequest{Version: "v1", Timestamp: stale, Signature: sig, Body: signedBody()}); !errors.Is(err, impl.ErrStaleTimestamp) {
		t.Fatalf("a request older than the window must report ErrStaleTimestamp, got %v", err)
	}
	audit := g.Audit()
	if len(audit) != 1 {
		t.Fatalf("refused call must still be audited, got %d records", len(audit))
	}
	if audit[0].Reason != impl.ReasonTimestampSkew {
		t.Fatalf("audit reason = %q, want %q", audit[0].Reason, impl.ReasonTimestampSkew)
	}
	if audit[0].Accepted {
		t.Fatalf("stale call must be recorded as refused")
	}
	if got := g.Stats().Rejected; got != 1 {
		t.Fatalf("rejected counter = %d, want 1", got)
	}
}

func TestVerifyRejectsFutureTimestamp(t *testing.T) {
	now := int64(6000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	ahead := now + testWindow + 5
	sig, err := g.Sign("v1", ahead, signedBody())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := g.Verify(impl.SignedRequest{Version: "v1", Timestamp: ahead, Signature: sig, Body: signedBody()}); !errors.Is(err, impl.ErrStaleTimestamp) {
		t.Fatalf("a timestamp ahead of the window must report ErrStaleTimestamp, got %v", err)
	}
	if got := g.ReplayEntries(); got != 0 {
		t.Fatalf("refused call must not enter the replay cache, got %d entries", got)
	}
}

func TestVerifyRejectsTamperedBody(t *testing.T) {
	now := int64(7000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	signed := signedBody()
	tampered := []byte(`{"event":"paid","order":"A-1001","amount":9999}`)
	sig, err := g.Sign("v1", now, signed)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := g.Verify(impl.SignedRequest{Version: "v1", Timestamp: now, Signature: sig, Body: tampered}); !errors.Is(err, impl.ErrBadSignature) {
		t.Fatalf("a tampered body must report ErrBadSignature, got %v", err)
	}
	audit := g.Audit()
	if len(audit) != 1 || audit[0].Reason != impl.ReasonSignatureMismatch {
		t.Fatalf("audit = %+v, want one signature mismatch record", audit)
	}
	if got := g.ReplayEntries(); got != 0 {
		t.Fatalf("a refused body must not consume a replay slot, got %d entries", got)
	}
}

func TestVerifyRejectsReplay(t *testing.T) {
	now := int64(8000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	sig, err := g.Sign("v1", now, signedBody())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req := impl.SignedRequest{Version: "v1", Timestamp: now, Signature: sig, Body: signedBody()}
	if _, err := g.Verify(req); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if _, err := g.Verify(req); !errors.Is(err, impl.ErrReplay) {
		t.Fatalf("second delivery of the same signature must report ErrReplay, got %v", err)
	}
	if got := g.Stats().Replayed; got != 1 {
		t.Fatalf("replayed counter = %d, want 1", got)
	}
	audit := g.Audit()
	if len(audit) != 2 {
		t.Fatalf("audit length = %d, want 2", len(audit))
	}
	if audit[1].Reason != impl.ReasonReplay || audit[1].Accepted {
		t.Fatalf("second audit record = %+v, want an accepted=false replay record", audit[1])
	}
	if got := g.ReplayEntries(); got != 1 {
		t.Fatalf("replay cache holds %d entries, want the single accepted signature", got)
	}
}

func TestRetiredKeyRejected(t *testing.T) {
	now := int64(9000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key v1: %v", err)
	}
	if err := g.AddKey("v2", []byte(testSecretTwo)); err != nil {
		t.Fatalf("add key v2: %v", err)
	}
	sig, err := g.Sign("v1", now, signedBody())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := g.RetireKey("v1"); err != nil {
		t.Fatalf("retire v1: %v", err)
	}
	if err := g.RetireKey("v1"); err != nil {
		t.Fatalf("retiring the same version twice must be idempotent, got %v", err)
	}
	if err := g.RetireKey("v404"); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("retiring an unknown version must report ErrUnknownKey, got %v", err)
	}
	if _, err := g.Sign("v1", now, signedBody()); !errors.Is(err, impl.ErrKeyRetired) {
		t.Fatalf("signing with a retired version must report ErrKeyRetired, got %v", err)
	}
	if _, err := g.Verify(impl.SignedRequest{Version: "v1", Timestamp: now, Signature: sig, Body: signedBody()}); !errors.Is(err, impl.ErrKeyRetired) {
		t.Fatalf("verifying with a retired version must report ErrKeyRetired, got %v", err)
	}
	if got := g.CurrentVersion(); got != "v2" {
		t.Fatalf("current version = %q, want v2", got)
	}
	if got := g.KeyCount(); got != 2 {
		t.Fatalf("retiring must keep the version on record, key count = %d want 2", got)
	}
}

func TestAuditIsCappedAndOrdered(t *testing.T) {
	now := int64(10000)
	g, err := impl.NewWebhookSignatureGuard(impl.GuardConfig{
		WindowSeconds: testWindow,
		MaxAudit:      3,
		MaxReplay:     64,
		Clock:         func() int64 { return now },
	})
	if err != nil {
		t.Fatalf("building the guard: %v", err)
	}
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	seqs := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		body := []byte(`{"event":"paid","order":"A-20` + string(rune('0'+i)) + `"}`)
		at := now + int64(i)
		sig, err := g.Sign("v1", at, body)
		if err != nil {
			t.Fatalf("sign %d: %v", i, err)
		}
		v, err := g.Verify(impl.SignedRequest{Version: "v1", Timestamp: at, Signature: sig, Body: body})
		if err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
		seqs = append(seqs, v.Seq)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("verdict sequence must keep growing, got %v", seqs)
		}
	}
	audit := g.Audit()
	if len(audit) != 3 {
		t.Fatalf("retained audit records = %d, want the configured cap 3", len(audit))
	}
	for i, want := range seqs[2:] {
		if audit[i].Seq != want {
			t.Fatalf("retained record %d has seq %d, want the newest window %d", i, audit[i].Seq, want)
		}
	}
	for i := 1; i < len(audit); i++ {
		if audit[i].Seq <= audit[i-1].Seq {
			t.Fatalf("retained audit sequence is not increasing: %+v", audit)
		}
	}
}

func TestConcurrentVerifyAcceptsOneSignature(t *testing.T) {
	now := int64(11000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	sig, err := g.Sign("v1", now, signedBody())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req := impl.SignedRequest{Version: "v1", Timestamp: now, Signature: sig, Body: signedBody()}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, replayed := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.Verify(req)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, impl.ErrReplay):
				replayed++
			}
		}()
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("concurrent deliveries accepted %d times, want exactly 1", accepted)
	}
	if replayed != 15 {
		t.Fatalf("concurrent deliveries reported %d replays, want 15", replayed)
	}
	if got := g.Stats().Accepted; got != 1 {
		t.Fatalf("accepted counter = %d, want 1", got)
	}
}

func TestPurgeExpiredDropsOldEntries(t *testing.T) {
	now := int64(12000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	sig, err := g.Sign("v1", now, signedBody())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := g.Verify(impl.SignedRequest{Version: "v1", Timestamp: now, Signature: sig, Body: signedBody()}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got := g.ReplayEntries(); got != 1 {
		t.Fatalf("replay cache holds %d entries, want 1", got)
	}
	if got := g.PurgeExpired(now + testWindow - 1); got != 0 {
		t.Fatalf("purge before the window ends removed %d entries, want 0", got)
	}
	if got := g.ReplayEntries(); got != 1 {
		t.Fatalf("live replay entry disappeared early, got %d entries", got)
	}
	if got := g.PurgeExpired(now + testWindow); got != 1 {
		t.Fatalf("purge at the window edge removed %d entries, want 1", got)
	}
	if got := g.ReplayEntries(); got != 0 {
		t.Fatalf("replay cache holds %d entries after the purge, want 0", got)
	}
}

func TestSnapshotRestoreKeepsReplayCache(t *testing.T) {
	now := int64(13000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	first := signedBody()
	sig, err := g.Sign("v1", now, first)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req := impl.SignedRequest{Version: "v1", Timestamp: now, Signature: sig, Body: first}
	v, err := g.Verify(req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored, err := impl.NewWebhookSignatureGuard(impl.GuardConfig{
		WindowSeconds: testWindow,
		MaxAudit:      8,
		MaxReplay:     64,
		Clock:         func() int64 { return now },
	})
	if err != nil {
		t.Fatalf("building the restored guard: %v", err)
	}
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.CurrentVersion(); got != "v1" {
		t.Fatalf("restored current version = %q, want v1", got)
	}
	if got := restored.ReplayEntries(); got != 1 {
		t.Fatalf("restored replay cache holds %d entries, want 1", got)
	}
	if _, err := restored.Verify(req); !errors.Is(err, impl.ErrReplay) {
		t.Fatalf("a snapshot restore must still block the replay, got %v", err)
	}
	second := []byte(`{"event":"paid","order":"A-3002","amount":500}`)
	sig2, err := restored.Sign("v1", now, second)
	if err != nil {
		t.Fatalf("sign after restore: %v", err)
	}
	v2, err := restored.Verify(impl.SignedRequest{Version: "v1", Timestamp: now, Signature: sig2, Body: second})
	if err != nil {
		t.Fatalf("verify after restore: %v", err)
	}
	if v2.Seq <= v.Seq {
		t.Fatalf("sequence after restore = %d, must continue past %d", v2.Seq, v.Seq)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	now := int64(14000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	sig, err := g.Sign("v1", now, signedBody())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := g.Verify(impl.SignedRequest{Version: "v1", Timestamp: now, Signature: sig, Body: signedBody()}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := g.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must report ErrBadSnapshot, got %v", err)
	}
	if got := g.KeyCount(); got != 1 {
		t.Fatalf("failed restore dropped keys, count = %d want 1", got)
	}
	if got := g.ReplayEntries(); got != 1 {
		t.Fatalf("failed restore dropped replay records, count = %d want 1", got)
	}
	if got := g.Stats().Accepted; got != 1 {
		t.Fatalf("failed restore reset the counters, accepted = %d want 1", got)
	}
	if got := g.CurrentVersion(); got != "v1" {
		t.Fatalf("failed restore changed the current version to %q", got)
	}
}

func TestVerifyRejectsIncompleteRequest(t *testing.T) {
	now := int64(15000)
	g := newTestGuard(t, &now)
	if err := g.AddKey("v1", []byte(testSecretOne)); err != nil {
		t.Fatalf("add key: %v", err)
	}
	if _, err := g.Verify(impl.SignedRequest{Version: "", Timestamp: now, Signature: "abcd", Body: signedBody()}); !errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("a request without a version must report ErrBadRequest, got %v", err)
	}
	if _, err := g.Verify(impl.SignedRequest{Version: "v1", Timestamp: now, Signature: "   ", Body: signedBody()}); !errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("a request without a signature must report ErrBadRequest, got %v", err)
	}
	audit := g.Audit()
	if len(audit) != 2 {
		t.Fatalf("both malformed calls must be audited, got %d records", len(audit))
	}
	for i, record := range audit {
		if record.Reason != impl.ReasonMalformedRequest {
			t.Fatalf("audit record %d reason = %q, want %q", i, record.Reason, impl.ReasonMalformedRequest)
		}
		if record.Accepted {
			t.Fatalf("audit record %d must mark the call as refused", i)
		}
	}
	if got := g.Stats().Rejected; got != 2 {
		t.Fatalf("rejected counter = %d, want 2", got)
	}
}
