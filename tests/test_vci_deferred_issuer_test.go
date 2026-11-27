package task

import (
	"errors"
	"fmt"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-02"
)

func issuerConfig() impl.IssuerConfig {
	return impl.IssuerConfig{
		IssuerID:            "https://issuer.corp.example",
		OfferTTL:            time.Hour,
		GrantTTL:            time.Minute,
		TransactionTTL:      time.Hour,
		PollInterval:        time.Duration(0),
		MaxPolls:            5,
		MaxPendingPerTenant: 4,
		MaxBatch:            10,
		ReviewPolls:         2,
	}
}

func offerRequest(tenant, subject string) impl.OfferRequest {
	return impl.OfferRequest{
		Tenant:         tenant,
		Subject:        subject,
		CredentialType: "IdentityCredential",
		PreAuthCode:    "pre-" + subject,
		BindingKey:     "jwk-" + subject,
		Deferred:       true,
		BatchSize:      1,
	}
}

func proofFor(g impl.Grant) impl.ProofJWT {
	return impl.ProofJWT{
		Raw:        "eyJhbGciOiJFUzI1NiJ9.proof",
		CNonce:     g.CNonce,
		BindingKey: g.BindingKey,
		Audience:   g.Audience,
		Subject:    g.Subject,
	}
}

func mustOffer(t *testing.T, i *impl.DeferredCredentialIssuer, tenant, subject string) impl.Offer {
	t.Helper()
	o, err := i.CreateOffer(offerRequest(tenant, subject))
	if err != nil {
		t.Fatalf("create offer for %s: %v", subject, err)
	}
	return o
}

func mustGrant(t *testing.T, i *impl.DeferredCredentialIssuer, o impl.Offer) impl.Grant {
	t.Helper()
	g, err := i.Redeem(o.ID, o.PreAuthCode)
	if err != nil {
		t.Fatalf("redeem offer %s: %v", o.ID, err)
	}
	return g
}

func TestCreateOfferValidatesRequest(t *testing.T) {
	i := impl.NewDeferredCredentialIssuer(issuerConfig())
	bad := []impl.OfferRequest{
		{Tenant: "  ", Subject: "s", PreAuthCode: "p", BindingKey: "k"},
		{Tenant: "t1", Subject: "", PreAuthCode: "p", BindingKey: "k"},
		{Tenant: "t1", Subject: "s", PreAuthCode: "", BindingKey: "k"},
		{Tenant: "t1", Subject: "s", PreAuthCode: "p", BindingKey: ""},
	}
	for idx, req := range bad {
		if _, err := i.CreateOffer(req); !errors.Is(err, impl.ErrMissingField) {
			t.Fatalf("request %d returned %v, want ErrMissingField", idx, err)
		}
	}
	if got := len(i.Offers("t1")); got != 0 {
		t.Fatalf("rejected requests created %d offers, want 0", got)
	}
	req := offerRequest("t1", "alice")
	req.BatchSize = 0
	o, err := i.CreateOffer(req)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	if o.ID == "" {
		t.Fatalf("offer id must not be empty")
	}
	if o.BatchSize != 1 {
		t.Fatalf("batch size = %d, want 1 when the request leaves it unset", o.BatchSize)
	}
	if o.Tenant != "t1" || o.Subject != "alice" || !o.Deferred {
		t.Fatalf("offer fields = %+v, want t1/alice/deferred", o)
	}
	if !o.ExpiresAt.After(time.Now()) {
		t.Fatalf("offer expiry %v must be in the future", o.ExpiresAt)
	}
	if o.Redeemed {
		t.Fatalf("a fresh offer must not be marked redeemed")
	}
}

func TestCreateOfferEnforcesTenantAndBatchLimits(t *testing.T) {
	cfg := issuerConfig()
	cfg.MaxPendingPerTenant = 2
	cfg.MaxBatch = 4
	i := impl.NewDeferredCredentialIssuer(cfg)
	for n := 0; n < 2; n++ {
		mustOffer(t, i, "t2", fmt.Sprintf("subject-%d", n))
	}
	if _, err := i.CreateOffer(offerRequest("t2", "subject-x")); !errors.Is(err, impl.ErrTenantQuota) {
		t.Fatalf("third open offer returned %v, want ErrTenantQuota", err)
	}
	if _, err := i.CreateOffer(offerRequest("t3", "subject-y")); err != nil {
		t.Fatalf("another tenant must still get offers, got %v", err)
	}
	big := offerRequest("t4", "subject-z")
	big.BatchSize = 5
	if _, err := i.CreateOffer(big); !errors.Is(err, impl.ErrBatchTooLarge) {
		t.Fatalf("oversized batch returned %v, want ErrBatchTooLarge", err)
	}
	ok := offerRequest("t4", "subject-z")
	ok.BatchSize = 4
	if _, err := i.CreateOffer(ok); err != nil {
		t.Fatalf("batch at the cap must be accepted, got %v", err)
	}
}

func TestRedeemRequiresMatchingPreAuth(t *testing.T) {
	i := impl.NewDeferredCredentialIssuer(issuerConfig())
	o := mustOffer(t, i, "t5", "bob")
	if _, err := i.Redeem(o.ID, "pre-wrong"); !errors.Is(err, impl.ErrBadPreAuth) {
		t.Fatalf("wrong pre authorized code returned %v, want ErrBadPreAuth", err)
	}
	if _, err := i.Redeem("offer-404", o.PreAuthCode); !errors.Is(err, impl.ErrOfferNotFound) {
		t.Fatalf("unknown offer returned %v, want ErrOfferNotFound", err)
	}
	g, err := i.Redeem(o.ID, o.PreAuthCode)
	if err != nil {
		t.Fatalf("rejected redeem must not spend the offer, got %v", err)
	}
	if g.OfferID != o.ID || g.Tenant != "t5" || g.Subject != "bob" {
		t.Fatalf("grant = %+v, want the offer identity", g)
	}
	if g.Audience != "https://issuer.corp.example" {
		t.Fatalf("grant audience = %q, want the configured issuer id", g.Audience)
	}
	if len(g.CNonce) != 32 {
		t.Fatalf("grant c_nonce %q has %d characters, want 32", g.CNonce, len(g.CNonce))
	}
	if !g.ExpiresAt.After(time.Now()) {
		t.Fatalf("grant expiry %v must be in the future", g.ExpiresAt)
	}
}

func TestRedeemIsSingleUse(t *testing.T) {
	i := impl.NewDeferredCredentialIssuer(issuerConfig())
	o := mustOffer(t, i, "t6", "carol")
	first := mustGrant(t, i, o)
	if _, err := i.Redeem(o.ID, o.PreAuthCode); !errors.Is(err, impl.ErrOfferConsumed) {
		t.Fatalf("second redeem returned %v, want ErrOfferConsumed", err)
	}
	if _, ok := i.GrantFor(first.ID); !ok {
		t.Fatalf("the first grant must still be readable")
	}
	if _, ok := i.GrantFor("grant-404"); ok {
		t.Fatalf("unknown grant must not be found")
	}
}

func TestRedeemRejectsExpiredOffer(t *testing.T) {
	cfg := issuerConfig()
	cfg.OfferTTL = -time.Second
	i := impl.NewDeferredCredentialIssuer(cfg)
	o := mustOffer(t, i, "t7", "dave")
	if _, err := i.Redeem(o.ID, o.PreAuthCode); !errors.Is(err, impl.ErrOfferExpired) {
		t.Fatalf("expired offer returned %v, want ErrOfferExpired", err)
	}
	if o.Redeemed {
		t.Fatalf("a rejected redeem must not mark the offer redeemed")
	}
}

func TestSubmitProofChecksNonceAndBinding(t *testing.T) {
	i := impl.NewDeferredCredentialIssuer(issuerConfig())
	g := mustGrant(t, i, mustOffer(t, i, "t8", "erin"))
	badNonce := proofFor(g)
	badNonce.CNonce = "00000000000000000000000000000000"
	if _, err := i.SubmitProof(g.ID, badNonce); !errors.Is(err, impl.ErrNonceMismatch) {
		t.Fatalf("foreign nonce returned %v, want ErrNonceMismatch", err)
	}
	if i.NonceConsumed(badNonce.CNonce) {
		t.Fatalf("a rejected proof must not consume its nonce")
	}
	badKey := proofFor(g)
	badKey.BindingKey = "jwk-someone-else"
	if _, err := i.SubmitProof(g.ID, badKey); !errors.Is(err, impl.ErrBindingMismatch) {
		t.Fatalf("foreign binding key returned %v, want ErrBindingMismatch", err)
	}
	if i.NonceConsumed(g.CNonce) {
		t.Fatalf("the grant nonce must stay spendable after a binding failure")
	}
	if _, err := i.SubmitProof("grant-404", proofFor(g)); !errors.Is(err, impl.ErrGrantNotFound) {
		t.Fatalf("unknown grant returned %v, want ErrGrantNotFound", err)
	}
}

func TestSubmitProofChecksAudience(t *testing.T) {
	i := impl.NewDeferredCredentialIssuer(issuerConfig())
	g := mustGrant(t, i, mustOffer(t, i, "t9", "frank"))
	p := proofFor(g)
	p.Audience = "https://other-issuer.example"
	if _, err := i.SubmitProof(g.ID, p); !errors.Is(err, impl.ErrAudienceMismatch) {
		t.Fatalf("foreign audience returned %v, want ErrAudienceMismatch", err)
	}
	if i.NonceConsumed(g.CNonce) {
		t.Fatalf("a proof for another audience must not consume the nonce")
	}
}

func TestSubmitProofConsumesNonce(t *testing.T) {
	i := impl.NewDeferredCredentialIssuer(issuerConfig())
	g := mustGrant(t, i, mustOffer(t, i, "t10", "grace"))
	if i.NonceConsumed(g.CNonce) {
		t.Fatalf("a fresh grant nonce must not be reported as consumed")
	}
	got, err := i.SubmitProof(g.ID, proofFor(g))
	if err != nil {
		t.Fatalf("submit proof: %v", err)
	}
	if !i.NonceConsumed(g.CNonce) {
		t.Fatalf("accepted proof must mark its c_nonce consumed")
	}
	if got.TransactionID == "" {
		t.Fatalf("proof submission must open a transaction")
	}
	if got.Status != impl.StatusPending {
		t.Fatalf("deferred offer produced status %q, want %q", got.Status, impl.StatusPending)
	}
	if got.Tenant != "t10" {
		t.Fatalf("transaction tenant = %q, want t10", got.Tenant)
	}
}

func TestSubmitProofIssuesImmediately(t *testing.T) {
	i := impl.NewDeferredCredentialIssuer(issuerConfig())
	req := offerRequest("t11", "heidi")
	req.Deferred = false
	o, err := i.CreateOffer(req)
	if err != nil {
		t.Fatalf("create offer: %v", err)
	}
	g := mustGrant(t, i, o)
	got, err := i.SubmitProof(g.ID, proofFor(g))
	if err != nil {
		t.Fatalf("submit proof: %v", err)
	}
	if got.Status != impl.StatusIssued {
		t.Fatalf("non deferred offer produced status %q, want %q", got.Status, impl.StatusIssued)
	}
	if got.Credential == "" {
		t.Fatalf("an issued transaction must carry a credential")
	}
	if got.Polls != 0 {
		t.Fatalf("an immediately issued transaction reported %d polls, want 0", got.Polls)
	}
}

func TestSubmitProofDefersToTransaction(t *testing.T) {
	cfg := issuerConfig()
	cfg.PollInterval = 90 * time.Second
	i := impl.NewDeferredCredentialIssuer(cfg)
	g := mustGrant(t, i, mustOffer(t, i, "t12", "ivan"))
	got, err := i.SubmitProof(g.ID, proofFor(g))
	if err != nil {
		t.Fatalf("submit proof: %v", err)
	}
	if got.Status != impl.StatusPending || got.TransactionID == "" {
		t.Fatalf("deferred submission = %+v, want a pending transaction", got)
	}
	if got.IntervalSecs != 90 {
		t.Fatalf("advertised interval = %d seconds, want 90", got.IntervalSecs)
	}
	if got.Credential != "" {
		t.Fatalf("a pending transaction must not carry a credential yet")
	}
	state, err := i.TransactionStatus(got.TransactionID)
	if err != nil {
		t.Fatalf("transaction status: %v", err)
	}
	if state != impl.StatusPending {
		t.Fatalf("transaction state = %q, want %q", state, impl.StatusPending)
	}
	if _, err := i.TransactionStatus("txn-404"); !errors.Is(err, impl.ErrTransactionMissing) {
		t.Fatalf("unknown transaction returned %v, want ErrTransactionMissing", err)
	}
}

func TestPollHonoursAdvertisedInterval(t *testing.T) {
	cfg := issuerConfig()
	cfg.PollInterval = time.Hour
	cfg.ReviewPolls = 2
	i := impl.NewDeferredCredentialIssuer(cfg)
	g := mustGrant(t, i, mustOffer(t, i, "t13", "judy"))
	sub, err := i.SubmitProof(g.ID, proofFor(g))
	if err != nil {
		t.Fatalf("submit proof: %v", err)
	}
	first, err := i.Poll(sub.TransactionID)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if first.Status != impl.StatusPending || first.Polls != 1 {
		t.Fatalf("first poll = %+v, want a pending answer with poll count 1", first)
	}
	if _, err := i.Poll(sub.TransactionID); !errors.Is(err, impl.ErrPollTooSoon) {
		t.Fatalf("poll inside the interval returned %v, want ErrPollTooSoon", err)
	}
}

func TestPollReturnsTheSameCredential(t *testing.T) {
	cfg := issuerConfig()
	cfg.ReviewPolls = 1
	i := impl.NewDeferredCredentialIssuer(cfg)
	g := mustGrant(t, i, mustOffer(t, i, "t14", "karl"))
	sub, err := i.SubmitProof(g.ID, proofFor(g))
	if err != nil {
		t.Fatalf("submit proof: %v", err)
	}
	first, err := i.Poll(sub.TransactionID)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if first.Status != impl.StatusIssued {
		t.Fatalf("poll after the required review returned %q, want %q", first.Status, impl.StatusIssued)
	}
	if first.Credential == "" {
		t.Fatalf("issued transaction must return a credential")
	}
	second, err := i.Poll(sub.TransactionID)
	if err != nil {
		t.Fatalf("second poll on an issued transaction: %v", err)
	}
	if second.Credential != first.Credential {
		t.Fatalf("poll returned a different credential: %q then %q", first.Credential, second.Credential)
	}
	if second.TransactionID != first.TransactionID {
		t.Fatalf("poll changed the transaction id: %q then %q", first.TransactionID, second.TransactionID)
	}
	if _, err := i.Poll("txn-404"); !errors.Is(err, impl.ErrTransactionMissing) {
		t.Fatalf("unknown transaction returned %v, want ErrTransactionMissing", err)
	}
}

func TestCancelMakesTransactionTerminal(t *testing.T) {
	i := impl.NewDeferredCredentialIssuer(issuerConfig())
	g := mustGrant(t, i, mustOffer(t, i, "t15", "laura"))
	sub, err := i.SubmitProof(g.ID, proofFor(g))
	if err != nil {
		t.Fatalf("submit proof: %v", err)
	}
	if err := i.Cancel(sub.TransactionID); err != nil {
		t.Fatalf("cancel pending transaction: %v", err)
	}
	if err := i.Cancel(sub.TransactionID); err != nil {
		t.Fatalf("cancel must be idempotent, got %v", err)
	}
	state, err := i.TransactionStatus(sub.TransactionID)
	if err != nil {
		t.Fatalf("status after cancel: %v", err)
	}
	if state != impl.StatusCancelled {
		t.Fatalf("state after cancel = %q, want %q", state, impl.StatusCancelled)
	}
	if err := i.Cancel("txn-404"); !errors.Is(err, impl.ErrTransactionMissing) {
		t.Fatalf("cancel of an unknown transaction returned %v, want ErrTransactionMissing", err)
	}
	req := offerRequest("t15", "laura-2")
	req.Deferred = false
	o2, err := i.CreateOffer(req)
	if err != nil {
		t.Fatalf("create second offer: %v", err)
	}
	g2 := mustGrant(t, i, o2)
	done, err := i.SubmitProof(g2.ID, proofFor(g2))
	if err != nil {
		t.Fatalf("submit second proof: %v", err)
	}
	if err := i.Cancel(done.TransactionID); !errors.Is(err, impl.ErrTransactionTermin) {
		t.Fatalf("cancel of an issued transaction returned %v, want ErrTransactionTermin", err)
	}
}

func TestTenantPendingCountsOpenTransactions(t *testing.T) {
	cfg := issuerConfig()
	cfg.ReviewPolls = 2
	i := impl.NewDeferredCredentialIssuer(cfg)
	txns := make([]string, 0, 2)
	for n := 0; n < 2; n++ {
		g := mustGrant(t, i, mustOffer(t, i, "t16", fmt.Sprintf("m-%d", n)))
		sub, err := i.SubmitProof(g.ID, proofFor(g))
		if err != nil {
			t.Fatalf("submit proof %d: %v", n, err)
		}
		txns = append(txns, sub.TransactionID)
	}
	if got := i.TenantPending("t16"); got != 2 {
		t.Fatalf("pending transactions = %d, want 2", got)
	}
	if got := i.TenantPending("t-other"); got != 0 {
		t.Fatalf("unknown tenant pending = %d, want 0", got)
	}
	for _, tx := range txns {
		if _, err := i.Poll(tx); err != nil {
			t.Fatalf("first poll of %s: %v", tx, err)
		}
		issued, err := i.Poll(tx)
		if err != nil {
			t.Fatalf("second poll of %s: %v", tx, err)
		}
		if issued.Status != impl.StatusIssued {
			t.Fatalf("transaction %s status = %q, want %q after the review budget", tx, issued.Status, impl.StatusIssued)
		}
	}
	if got := i.TenantPending("t16"); got != 0 {
		t.Fatalf("pending transactions after issuance = %d, want 0", got)
	}
}

func TestSnapshotRestoreKeepsNoncesAndTransactions(t *testing.T) {
	cfg := issuerConfig()
	cfg.ReviewPolls = 2
	i := impl.NewDeferredCredentialIssuer(cfg)
	gOpen := mustGrant(t, i, mustOffer(t, i, "t17", "nina"))
	open, err := i.SubmitProof(gOpen.ID, proofFor(gOpen))
	if err != nil {
		t.Fatalf("submit open proof: %v", err)
	}
	req := offerRequest("t17", "oscar")
	req.Deferred = false
	o2, err := i.CreateOffer(req)
	if err != nil {
		t.Fatalf("create second offer: %v", err)
	}
	gDone := mustGrant(t, i, o2)
	done, err := i.SubmitProof(gDone.ID, proofFor(gDone))
	if err != nil {
		t.Fatalf("submit done proof: %v", err)
	}
	data, err := i.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewDeferredCredentialIssuer(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !restored.NonceConsumed(gOpen.CNonce) || !restored.NonceConsumed(gDone.CNonce) {
		t.Fatalf("restore lost the consumed nonces")
	}
	if got := restored.TenantPending("t17"); got != 1 {
		t.Fatalf("restored pending transactions = %d, want 1", got)
	}
	stillOpen, err := restored.Poll(open.TransactionID)
	if err != nil {
		t.Fatalf("poll restored open transaction: %v", err)
	}
	if stillOpen.Status != impl.StatusPending || stillOpen.Polls != 1 {
		t.Fatalf("restored open transaction = %+v, want pending with poll count 1", stillOpen)
	}
	stillDone, err := restored.Poll(done.TransactionID)
	if err != nil {
		t.Fatalf("poll restored issued transaction: %v", err)
	}
	if stillDone.Credential != done.Credential {
		t.Fatalf("restored credential = %q, want %q", stillDone.Credential, done.Credential)
	}
	if err := restored.Restore([]byte("not json at all")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot returned %v, want ErrBadSnapshot", err)
	}
	if got := restored.TenantPending("t17"); got != 1 {
		t.Fatalf("rejected restore changed live state, pending = %d want 1", got)
	}
}
