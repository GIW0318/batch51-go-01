// Package task runs the credential issuance service: it hands out one shot
// offers, checks the holder of key proof and drives deferred issuance
// transactions that are polled after a manual review.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Transaction states reported by Poll and TransactionStatus.
const (
	StatusPending   = "pending"
	StatusIssued    = "issued"
	StatusCancelled = "cancelled"
)

// Errors returned by the issuer.
var (
	ErrMissingField       = errors.New("tenant, subject, pre authorized code and binding key are required")
	ErrBatchTooLarge      = errors.New("requested batch size exceeds the configured cap")
	ErrTenantQuota        = errors.New("too many open offers for this tenant")
	ErrOfferNotFound      = errors.New("offer not found")
	ErrOfferExpired       = errors.New("offer expired")
	ErrOfferConsumed      = errors.New("offer already redeemed")
	ErrBadPreAuth         = errors.New("pre authorized code mismatch")
	ErrGrantNotFound      = errors.New("grant not found")
	ErrGrantExpired       = errors.New("grant expired")
	ErrNonceMismatch      = errors.New("proof nonce does not match the grant")
	ErrNonceUsed          = errors.New("proof nonce already consumed")
	ErrBindingMismatch    = errors.New("proof key does not match the granted binding")
	ErrAudienceMismatch   = errors.New("proof audience does not name this issuer")
	ErrTransactionMissing = errors.New("transaction not found")
	ErrTransactionTermin  = errors.New("transaction already reached a terminal state")
	ErrPollTooSoon        = errors.New("poll arrived before the advertised interval")
	ErrPollExhausted      = errors.New("transaction exhausted its poll budget")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// IssuerConfig bounds the issuance tables.
type IssuerConfig struct {
	IssuerID            string
	OfferTTL            time.Duration
	GrantTTL            time.Duration
	TransactionTTL      time.Duration
	PollInterval        time.Duration
	MaxPolls            int
	MaxPendingPerTenant int
	MaxBatch            int
	ReviewPolls         int
}

// OfferRequest asks for one credential offer.
type OfferRequest struct {
	Tenant         string
	Subject        string
	CredentialType string
	PreAuthCode    string
	BindingKey     string
	Deferred       bool
	BatchSize      int
}

// Offer is the redeemable pair of a pre authorized code and a subject.
type Offer struct {
	ID             string    `json:"id"`
	Tenant         string    `json:"tenant"`
	Subject        string    `json:"subject"`
	CredentialType string    `json:"credential_type"`
	PreAuthCode    string    `json:"pre_auth_code"`
	BindingKey     string    `json:"binding_key"`
	Deferred       bool      `json:"deferred"`
	BatchSize      int       `json:"batch_size"`
	ExpiresAt      time.Time `json:"expires_at"`
	Redeemed       bool      `json:"redeemed"`
}

// Grant is the short lived authorisation produced by redeeming an offer.
type Grant struct {
	ID         string    `json:"id"`
	OfferID    string    `json:"offer_id"`
	Tenant     string    `json:"tenant"`
	Subject    string    `json:"subject"`
	BindingKey string    `json:"binding_key"`
	CNonce     string    `json:"c_nonce"`
	Audience   string    `json:"audience"`
	Deferred   bool      `json:"deferred"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// ProofJWT is the holder of key proof presented with one grant.
type ProofJWT struct {
	Raw        string
	CNonce     string
	BindingKey string
	Audience   string
	Subject    string
}

// Issued is the answer of SubmitProof and Poll.
type Issued struct {
	TransactionID string `json:"transaction_id"`
	Credential    string `json:"credential"`
	Status        string `json:"status"`
	Tenant        string `json:"tenant"`
	Polls         int    `json:"polls"`
	IntervalSecs  int    `json:"interval_secs"`
}

// DeferredCredentialIssuer owns offers, grants, consumed nonces and the
// deferred issuance transactions.
type DeferredCredentialIssuer struct {
	cfg IssuerConfig
}

// NewDeferredCredentialIssuer builds an issuer with the given caps.
func NewDeferredCredentialIssuer(cfg IssuerConfig) *DeferredCredentialIssuer {
	return &DeferredCredentialIssuer{cfg: cfg}
}

// CreateOffer records one offer and returns it with its expiry.
func (i *DeferredCredentialIssuer) CreateOffer(req OfferRequest) (Offer, error) {
	return Offer{}, errNotImplemented
}

// Redeem exchanges an offer and its pre authorized code for a grant.
func (i *DeferredCredentialIssuer) Redeem(offerID, preAuthCode string) (Grant, error) {
	return Grant{}, errNotImplemented
}

// SubmitProof consumes the grant nonce and starts or finishes issuance.
func (i *DeferredCredentialIssuer) SubmitProof(grantID string, proof ProofJWT) (Issued, error) {
	return Issued{}, errNotImplemented
}

// Poll advances or reads one issuance transaction.
func (i *DeferredCredentialIssuer) Poll(transactionID string) (Issued, error) {
	return Issued{}, errNotImplemented
}

// Cancel drops a transaction that has not produced a credential yet.
func (i *DeferredCredentialIssuer) Cancel(transactionID string) error {
	return errNotImplemented
}

// GrantFor reads one grant record.
func (i *DeferredCredentialIssuer) GrantFor(grantID string) (Grant, bool) {
	return Grant{}, false
}

// TransactionStatus reports the state of one transaction.
func (i *DeferredCredentialIssuer) TransactionStatus(transactionID string) (string, error) {
	return "", errNotImplemented
}

// NonceConsumed reports whether one c_nonce was already spent.
func (i *DeferredCredentialIssuer) NonceConsumed(cnonce string) bool {
	return false
}

// TenantPending counts the transactions of one tenant that produced no
// credential yet.
func (i *DeferredCredentialIssuer) TenantPending(tenant string) int {
	return 0
}

// Offers lists the offers of one tenant, oldest first.
func (i *DeferredCredentialIssuer) Offers(tenant string) []Offer {
	return nil
}

// Snapshot serialises offers, grants, consumed nonces and transactions.
func (i *DeferredCredentialIssuer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (i *DeferredCredentialIssuer) Restore(data []byte) error {
	return errNotImplemented
}
