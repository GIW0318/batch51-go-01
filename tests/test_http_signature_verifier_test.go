package task

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-09"
)

var sigNow = time.Date(2026, 5, 11, 7, 30, 0, 0, time.UTC)

const sigSecret = "gateway-shared-secret"

func testVerifierConfig() impl.VerifierConfig {
	return impl.VerifierConfig{
		Clock:           func() time.Time { return sigNow },
		MaxSkew:         2 * time.Minute,
		MaxValidity:     10 * time.Minute,
		MaxNoncesPerKey: 8,
		MaxComponents:   8,
		MaxBaseLen:      4096,
		MaxBody:         1 << 16,
	}
}

func testHeaderValue(headers []impl.Header, name string) string {
	values := make([]string, 0, 1)
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			values = append(values, strings.Join(strings.Fields(h.Value), " "))
		}
	}
	return strings.Join(values, ", ")
}

func testBase(req impl.SignedRequest) string {
	parts := make([]string, 0, 4)
	for _, comp := range strings.Split(req.SignatureInput, " ") {
		value := ""
		switch comp {
		case "@method":
			value = strings.ToUpper(req.Method)
		case "@path":
			path, _, _ := strings.Cut(req.Target, "?")
			value = path
		case "@query":
			if _, query, ok := strings.Cut(req.Target, "?"); ok {
				value = query
			}
		case "@target-uri":
			value = req.Target
		default:
			value = testHeaderValue(req.Headers, strings.TrimPrefix(comp, "header:"))
		}
		parts = append(parts, "\""+comp+"\": "+value)
	}
	return strings.Join(parts, "\n")
}

func testSign(req impl.SignedRequest) string {
	mac := hmac.New(sha256.New, []byte(sigSecret))
	mac.Write([]byte(testBase(req)))
	return hex.EncodeToString(mac.Sum(nil))
}

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func newTestVerifier(t *testing.T, cfg impl.VerifierConfig) *impl.HttpSignatureVerifier {
	t.Helper()
	v := impl.NewHttpSignatureVerifier(cfg)
	keys := []impl.KeyMaterial{
		{KeyID: "k1", Subject: "svc-order", Secret: []byte(sigSecret), NotBefore: sigNow.Add(-time.Hour), NotAfter: sigNow.Add(time.Hour)},
		{KeyID: "k2", Subject: "svc-billing", Secret: []byte(sigSecret + "-2")},
	}
	for _, key := range keys {
		if err := v.RegisterKey(key.KeyID, key); err != nil {
			t.Fatalf("register %s: %v", key.KeyID, err)
		}
	}
	return v
}

func TestRegisterKeyValidation(t *testing.T) {
	v := impl.NewHttpSignatureVerifier(testVerifierConfig())
	if err := v.RegisterKey("  ", impl.KeyMaterial{Secret: []byte("s")}); !errors.Is(err, impl.ErrBlankKey) {
		t.Fatalf("blank key id must fail with ErrBlankKey, got %v", err)
	}
	if err := v.RegisterKey("k1", impl.KeyMaterial{KeyID: "k1", Secret: []byte(sigSecret)}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := v.RegisterKey("k1", impl.KeyMaterial{KeyID: "k1", Secret: []byte(sigSecret)}); !errors.Is(err, impl.ErrKeyExists) {
		t.Fatalf("duplicate register must fail with ErrKeyExists, got %v", err)
	}
	if err := v.RegisterKey("k2", impl.KeyMaterial{KeyID: "k2", Secret: []byte(sigSecret + "-2")}); err != nil {
		t.Fatalf("second register: %v", err)
	}
	keys := v.Keys()
	if len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Fatalf("keys = %v, want the sorted pair [k1 k2]", keys)
	}
}

func TestVerifyAcceptsValidSignature(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	req := impl.SignedRequest{
		Method: "post",
		Target: "/v1/orders?b=2&a=1",
		Headers: []impl.Header{
			{Name: "X-Trace", Value: "alpha"},
			{Name: "Content-Type", Value: "application/json"},
		},
		SignatureInput: "@method @path @query header:x-trace",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(5 * time.Minute).Unix(),
		Nonce:          "n-valid",
	}
	req.Signature = testSign(req)
	signer, err := v.Verify(req)
	if err != nil {
		t.Fatalf("a correctly signed request must verify, got %v", err)
	}
	if signer.KeyID != "k1" || signer.Subject != "svc-order" {
		t.Fatalf("signer = %+v, want the k1 subject", signer)
	}
	if !signer.SignedAt.Equal(time.Unix(sigNow.Unix(), 0)) {
		t.Fatalf("signing instant = %v, want created", signer.SignedAt)
	}
	if len(signer.Components) != 4 || signer.Components[0] != "@method" {
		t.Fatalf("components = %v, want the four covered components in order", signer.Components)
	}
	if v.NonceCount("k1") != 1 {
		t.Fatalf("nonce count = %d, want the accepted nonce held once", v.NonceCount("k1"))
	}
}

func TestVerifyRejectsMissingMethodComponent(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	req := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		SignatureInput: "@path @query",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-nomethod",
	}
	req.Signature = testSign(req)
	if _, err := v.Verify(req); !errors.Is(err, impl.ErrMissingMethodComponent) {
		t.Fatalf("a signature that does not cover @method must fail, got %v", err)
	}
}

func TestVerifyRejectsDuplicateAndMixedCaseComponents(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	dup := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		Headers:        []impl.Header{{Name: "X-Trace", Value: "a"}},
		SignatureInput: "@method header:x-trace header:x-trace",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-dup",
	}
	dup.Signature = testSign(dup)
	if _, err := v.Verify(dup); !errors.Is(err, impl.ErrDuplicateComponent) {
		t.Fatalf("a repeated component must fail with ErrDuplicateComponent, got %v", err)
	}
	upper := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		Headers:        []impl.Header{{Name: "X-Trace", Value: "a"}},
		SignatureInput: "@method header:X-Trace",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-case",
	}
	upper.Signature = testSign(upper)
	if _, err := v.Verify(upper); !errors.Is(err, impl.ErrComponentCase) {
		t.Fatalf("an upper case header component must fail with ErrComponentCase, got %v", err)
	}
}

func TestVerifyRejectsUnknownComponent(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	req := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		SignatureInput: "@method @body",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-unknown",
	}
	req.Signature = testSign(req)
	if _, err := v.Verify(req); !errors.Is(err, impl.ErrUnknownComponent) {
		t.Fatalf("an unsupported component must fail with ErrUnknownComponent, got %v", err)
	}
}

func TestSigningBaseNormalizesHeaderWhitespace(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	req := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping?z=1",
		Headers:        []impl.Header{{Name: "X-Trace", Value: "  alpha \t\t beta  "}},
		SignatureInput: "@method header:x-trace",
	}
	base, err := v.SigningBase(req)
	if err != nil {
		t.Fatalf("signing base: %v", err)
	}
	want := "\"@method\": GET\n\"header:x-trace\": alpha beta"
	if base != want {
		t.Fatalf("base = %q, want %q", base, want)
	}
}

func TestSigningBaseKeepsQueryOrder(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	req := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/items?zeta=1&alpha=2&mid=3",
		SignatureInput: "@method @path @query",
	}
	base, err := v.SigningBase(req)
	if err != nil {
		t.Fatalf("signing base: %v", err)
	}
	want := "\"@method\": GET\n\"@path\": /v1/items\n\"@query\": zeta=1&alpha=2&mid=3"
	if base != want {
		t.Fatalf("base = %q, want the query kept in wire order %q", base, want)
	}
}

func TestVerifyRejectsClockWindowViolations(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	stale := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		SignatureInput: "@method @path",
		KeyID:          "k1",
		Created:        sigNow.Add(-5 * time.Minute).Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-stale",
	}
	stale.Signature = testSign(stale)
	if _, err := v.Verify(stale); !errors.Is(err, impl.ErrClockSkew) {
		t.Fatalf("a created instant five minutes old must fail with ErrClockSkew, got %v", err)
	}
	expired := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		SignatureInput: "@method @path",
		KeyID:          "k1",
		Created:        sigNow.Add(-time.Minute).Unix(),
		Expires:        sigNow.Add(-3 * time.Minute).Unix(),
		Nonce:          "n-expired",
	}
	expired.Signature = testSign(expired)
	if _, err := v.Verify(expired); !errors.Is(err, impl.ErrSignatureExpired) {
		t.Fatalf("an expired signature must fail with ErrSignatureExpired, got %v", err)
	}
}

func TestVerifyRejectsDigestMismatchWithoutConsumingNonce(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	body := "amount=100"
	req := impl.SignedRequest{
		Method:         "post",
		Target:         "/v1/pay",
		Headers:        []impl.Header{{Name: "Content-Digest", Value: "sha-256=" + digestOf("amount=999")}},
		Body:           strings.NewReader(body),
		SignatureInput: "@method @path header:content-digest",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-digest",
	}
	req.Signature = testSign(req)
	if _, err := v.Verify(req); !errors.Is(err, impl.ErrDigestMismatch) {
		t.Fatalf("a body that does not match the digest must fail with ErrDigestMismatch, got %v", err)
	}
	if v.NonceCount("k1") != 0 {
		t.Fatalf("a digest failure must not burn the nonce, count = %d", v.NonceCount("k1"))
	}
}

func TestVerifyRejectsReplayedNonce(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	makeReq := func() impl.SignedRequest {
		req := impl.SignedRequest{
			Method:         "get",
			Target:         "/v1/ping",
			SignatureInput: "@method @path",
			KeyID:          "k1",
			Created:        sigNow.Unix(),
			Expires:        sigNow.Add(time.Minute).Unix(),
			Nonce:          "n-replay",
		}
		req.Signature = testSign(req)
		return req
	}
	if _, err := v.Verify(makeReq()); err != nil {
		t.Fatalf("first use of the nonce must pass, got %v", err)
	}
	if _, err := v.Verify(makeReq()); !errors.Is(err, impl.ErrNonceReplay) {
		t.Fatalf("second use of the same nonce must fail with ErrNonceReplay, got %v", err)
	}
	if v.NonceCount("k1") != 1 {
		t.Fatalf("nonce count = %d, want the single accepted nonce", v.NonceCount("k1"))
	}
}

func TestConcurrentSameNonceAdmitsOne(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, replayed := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := impl.SignedRequest{
				Method:         "get",
				Target:         "/v1/ping",
				SignatureInput: "@method @path",
				KeyID:          "k1",
				Created:        sigNow.Unix(),
				Expires:        sigNow.Add(time.Minute).Unix(),
				Nonce:          "n-race",
			}
			req.Signature = testSign(req)
			_, err := v.Verify(req)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, impl.ErrNonceReplay):
				replayed++
			}
		}()
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("concurrent requests with one nonce accepted %d times, want 1", accepted)
	}
	if replayed != 11 {
		t.Fatalf("concurrent requests reported %d replays, want 11", replayed)
	}
	if v.NonceCount("k1") != 1 {
		t.Fatalf("nonce count = %d, want 1", v.NonceCount("k1"))
	}
}

func TestVerifyRejectsRevokedKey(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	if err := v.RevokeKey("k1", sigNow); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	req := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		SignatureInput: "@method @path",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-revoked",
	}
	req.Signature = testSign(req)
	if _, err := v.Verify(req); !errors.Is(err, impl.ErrKeyRevoked) {
		t.Fatalf("a signature created after revocation must fail with ErrKeyRevoked, got %v", err)
	}
	at, ok := v.Revoked("k1")
	if !ok || !at.Equal(sigNow) {
		t.Fatalf("revocation watermark = %v ok=%v, want %v", at, ok, sigNow)
	}
}

func TestNonceLedgerCapacity(t *testing.T) {
	cfg := testVerifierConfig()
	cfg.MaxNoncesPerKey = 2
	v := newTestVerifier(t, cfg)
	for i, nonce := range []string{"n-1", "n-2"} {
		req := impl.SignedRequest{
			Method:         "get",
			Target:         "/v1/ping",
			SignatureInput: "@method @path",
			KeyID:          "k1",
			Created:        sigNow.Unix(),
			Expires:        sigNow.Add(time.Minute).Unix(),
			Nonce:          nonce,
		}
		req.Signature = testSign(req)
		if _, err := v.Verify(req); err != nil {
			t.Fatalf("nonce %d must be accepted, got %v", i, err)
		}
	}
	third := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		SignatureInput: "@method @path",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-3",
	}
	third.Signature = testSign(third)
	if _, err := v.Verify(third); !errors.Is(err, impl.ErrNonceCapacity) {
		t.Fatalf("a full nonce ledger must fail with ErrNonceCapacity, got %v", err)
	}
	if v.NonceCount("k1") != 2 {
		t.Fatalf("nonce count = %d, want the cap 2", v.NonceCount("k1"))
	}
}

func TestSnapshotRestoreKeepsNoncesAndRevocations(t *testing.T) {
	cfg := testVerifierConfig()
	v := newTestVerifier(t, cfg)
	first := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		SignatureInput: "@method @path",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-keep",
	}
	first.Signature = testSign(first)
	if _, err := v.Verify(first); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := v.RevokeKey("k2", sigNow); err != nil {
		t.Fatalf("revoke k2: %v", err)
	}
	data, err := v.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewHttpSignatureVerifier(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.NonceCount("k1") != 1 {
		t.Fatalf("restored nonce count = %d, want 1", restored.NonceCount("k1"))
	}
	replay := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		SignatureInput: "@method @path",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-keep",
	}
	replay.Signature = testSign(replay)
	if _, err := restored.Verify(replay); !errors.Is(err, impl.ErrNonceReplay) {
		t.Fatalf("restored ledger must still reject the old nonce, got %v", err)
	}
	if at, ok := restored.Revoked("k2"); !ok || !at.Equal(sigNow) {
		t.Fatalf("restored revocation = %v ok=%v, want %v", at, ok, sigNow)
	}
	if got := len(restored.Keys()); got != 2 {
		t.Fatalf("restored key count = %d, want 2", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	v := newTestVerifier(t, testVerifierConfig())
	req := impl.SignedRequest{
		Method:         "get",
		Target:         "/v1/ping",
		SignatureInput: "@method @path",
		KeyID:          "k1",
		Created:        sigNow.Unix(),
		Expires:        sigNow.Add(time.Minute).Unix(),
		Nonce:          "n-live",
	}
	req.Signature = testSign(req)
	if _, err := v.Verify(req); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := v.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if v.NonceCount("k1") != 1 {
		t.Fatalf("failed restore changed the nonce count to %d", v.NonceCount("k1"))
	}
	if got := len(v.Keys()); got != 2 {
		t.Fatalf("failed restore changed the key ring to %d keys", got)
	}
}
