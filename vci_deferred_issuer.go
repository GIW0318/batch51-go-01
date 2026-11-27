// Package task runs the credential issuance service: it hands out one shot
// offers, checks the holder of key proof and drives deferred issuance
// transactions that are polled after a manual review.
package task

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

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

type transactionRecord struct {
	TransactionID string    `json:"transaction_id"`
	Tenant        string    `json:"tenant"`
	Subject       string    `json:"subject"`
	OfferID       string    `json:"offer_id"`
	GrantID       string    `json:"grant_id"`
	Status        string    `json:"status"`
	Credential    string    `json:"credential,omitempty"`
	Polls         int       `json:"polls"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	NextPollAt    time.Time `json:"next_poll_at"`
}

// DeferredCredentialIssuer owns offers, grants, consumed nonces and the
// deferred issuance transactions.
type DeferredCredentialIssuer struct {
	cfg IssuerConfig

	mu             sync.RWMutex
	offers         map[string]Offer
	offerOrder     []string
	grants         map[string]Grant
	transactions   map[string]*transactionRecord
	consumedNonces map[string]struct{}
}

const (
	snapshotMagic   = "vci-deferred-issuer"
	snapshotVersion = 1
)

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type snapshotState struct {
	Offers         []Offer             `json:"offers"`
	Grants         []Grant             `json:"grants"`
	Transactions   []transactionRecord `json:"transactions"`
	ConsumedNonces []string            `json:"consumed_nonces"`
}

// NewDeferredCredentialIssuer builds an issuer with the given caps.
func NewDeferredCredentialIssuer(cfg IssuerConfig) *DeferredCredentialIssuer {
	return &DeferredCredentialIssuer{
		cfg:            cfg,
		offers:         make(map[string]Offer),
		grants:         make(map[string]Grant),
		transactions:   make(map[string]*transactionRecord),
		consumedNonces: make(map[string]struct{}),
	}
}

// CreateOffer records one offer and returns it with its expiry.
func (i *DeferredCredentialIssuer) CreateOffer(req OfferRequest) (Offer, error) {
	if strings.TrimSpace(req.Tenant) == "" ||
		strings.TrimSpace(req.Subject) == "" ||
		strings.TrimSpace(req.PreAuthCode) == "" ||
		strings.TrimSpace(req.BindingKey) == "" {
		return Offer{}, ErrMissingField
	}

	batchSize := req.BatchSize
	if batchSize <= 0 {
		batchSize = 1
	}
	if batchSize > i.cfg.MaxBatch {
		return Offer{}, ErrBatchTooLarge
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	now := time.Now()
	if i.openOfferCountLocked(req.Tenant, now) >= i.cfg.MaxPendingPerTenant {
		return Offer{}, ErrTenantQuota
	}

	id, err := i.uniqueTokenLocked("offer-", func(candidate string) bool {
		_, exists := i.offers[candidate]
		return exists
	})
	if err != nil {
		return Offer{}, err
	}

	offer := Offer{
		ID:             id,
		Tenant:         req.Tenant,
		Subject:        req.Subject,
		CredentialType: req.CredentialType,
		PreAuthCode:    req.PreAuthCode,
		BindingKey:     req.BindingKey,
		Deferred:       req.Deferred,
		BatchSize:      batchSize,
		ExpiresAt:      now.Add(i.cfg.OfferTTL),
	}
	i.offers[id] = offer
	i.offerOrder = append(i.offerOrder, id)
	return offer, nil
}

// Redeem exchanges an offer and its pre authorized code for a grant.
func (i *DeferredCredentialIssuer) Redeem(offerID, preAuthCode string) (Grant, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	offer, ok := i.offers[offerID]
	if !ok {
		return Grant{}, ErrOfferNotFound
	}
	if offer.Redeemed {
		return Grant{}, ErrOfferConsumed
	}
	if preAuthCode != offer.PreAuthCode {
		return Grant{}, ErrBadPreAuth
	}

	now := time.Now()
	if !now.Before(offer.ExpiresAt) {
		return Grant{}, ErrOfferExpired
	}

	grantID, err := i.uniqueTokenLocked("grant-", func(candidate string) bool {
		_, exists := i.grants[candidate]
		return exists
	})
	if err != nil {
		return Grant{}, err
	}
	nonce, err := i.uniqueNonceLocked()
	if err != nil {
		return Grant{}, err
	}

	grant := Grant{
		ID:         grantID,
		OfferID:    offer.ID,
		Tenant:     offer.Tenant,
		Subject:    offer.Subject,
		BindingKey: offer.BindingKey,
		CNonce:     nonce,
		Audience:   i.cfg.IssuerID,
		Deferred:   offer.Deferred,
		ExpiresAt:  now.Add(i.cfg.GrantTTL),
	}
	offer.Redeemed = true
	i.offers[offer.ID] = offer
	i.grants[grant.ID] = grant
	return grant, nil
}

// SubmitProof consumes the grant nonce and starts or finishes issuance.
func (i *DeferredCredentialIssuer) SubmitProof(grantID string, proof ProofJWT) (Issued, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	grant, ok := i.grants[grantID]
	if !ok {
		return Issued{}, ErrGrantNotFound
	}

	now := time.Now()
	if !now.Before(grant.ExpiresAt) {
		return Issued{}, ErrGrantExpired
	}
	if proof.CNonce != grant.CNonce {
		return Issued{}, ErrNonceMismatch
	}
	if _, consumed := i.consumedNonces[grant.CNonce]; consumed {
		return Issued{}, ErrNonceUsed
	}
	if proof.BindingKey != grant.BindingKey {
		return Issued{}, ErrBindingMismatch
	}
	if proof.Audience != i.cfg.IssuerID {
		return Issued{}, ErrAudienceMismatch
	}

	if grant.Deferred && i.tenantPendingLocked(grant.Tenant, now) >= i.cfg.MaxPendingPerTenant {
		return Issued{}, ErrTenantQuota
	}

	transactionID, err := i.uniqueTokenLocked("txn-", func(candidate string) bool {
		_, exists := i.transactions[candidate]
		return exists
	})
	if err != nil {
		return Issued{}, err
	}

	status := StatusPending
	credential := ""
	if !grant.Deferred {
		status = StatusIssued
		credential = i.credentialFor(transactionID)
	}
	transaction := &transactionRecord{
		TransactionID: transactionID,
		Tenant:        grant.Tenant,
		Subject:       grant.Subject,
		OfferID:       grant.OfferID,
		GrantID:       grant.ID,
		Status:        status,
		Credential:    credential,
		CreatedAt:     now,
		ExpiresAt:     now.Add(i.cfg.TransactionTTL),
	}

	i.consumedNonces[grant.CNonce] = struct{}{}
	i.transactions[transactionID] = transaction
	return i.viewLocked(transaction), nil
}

// Poll advances or reads one issuance transaction.
func (i *DeferredCredentialIssuer) Poll(transactionID string) (Issued, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	transaction, ok := i.transactions[transactionID]
	if !ok {
		return Issued{}, ErrTransactionMissing
	}

	now := time.Now()
	switch transaction.Status {
	case StatusIssued:
		return i.viewLocked(transaction), nil
	case StatusCancelled:
		return Issued{}, ErrTransactionTermin
	}

	if i.transactionExpiredLocked(transaction, now) {
		transaction.Status = StatusCancelled
		transaction.NextPollAt = time.Time{}
		return Issued{}, ErrTransactionTermin
	}
	if transaction.Polls >= i.cfg.MaxPolls {
		return Issued{}, ErrPollExhausted
	}
	if !transaction.NextPollAt.IsZero() && now.Before(transaction.NextPollAt) {
		return Issued{}, ErrPollTooSoon
	}

	transaction.Polls++
	if transaction.Polls >= i.cfg.ReviewPolls {
		transaction.Status = StatusIssued
		transaction.Credential = i.credentialFor(transaction.TransactionID)
		transaction.NextPollAt = time.Time{}
	} else if i.cfg.PollInterval > 0 {
		transaction.NextPollAt = now.Add(i.cfg.PollInterval)
	} else {
		transaction.NextPollAt = time.Time{}
	}
	return i.viewLocked(transaction), nil
}

// Cancel drops a transaction that has not produced a credential yet.
func (i *DeferredCredentialIssuer) Cancel(transactionID string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	transaction, ok := i.transactions[transactionID]
	if !ok {
		return ErrTransactionMissing
	}
	switch transaction.Status {
	case StatusIssued:
		return ErrTransactionTermin
	case StatusCancelled:
		return nil
	}

	transaction.Status = StatusCancelled
	transaction.Credential = ""
	transaction.NextPollAt = time.Time{}
	return nil
}

// GrantFor reads one grant record.
func (i *DeferredCredentialIssuer) GrantFor(grantID string) (Grant, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	grant, ok := i.grants[grantID]
	return grant, ok
}

// TransactionStatus reports the state of one transaction.
func (i *DeferredCredentialIssuer) TransactionStatus(transactionID string) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	transaction, ok := i.transactions[transactionID]
	if !ok {
		return "", ErrTransactionMissing
	}
	if transaction.Status == StatusPending && i.transactionExpiredLocked(transaction, time.Now()) {
		transaction.Status = StatusCancelled
		transaction.NextPollAt = time.Time{}
	}
	return transaction.Status, nil
}

// NonceConsumed reports whether one c_nonce was already spent.
func (i *DeferredCredentialIssuer) NonceConsumed(cnonce string) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	_, consumed := i.consumedNonces[cnonce]
	return consumed
}

// TenantPending counts the transactions of one tenant that produced no
// credential yet.
func (i *DeferredCredentialIssuer) TenantPending(tenant string) int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.tenantPendingLocked(tenant, time.Now())
}

// Offers lists the offers of one tenant, oldest first.
func (i *DeferredCredentialIssuer) Offers(tenant string) []Offer {
	i.mu.RLock()
	defer i.mu.RUnlock()

	offers := make([]Offer, 0)
	for _, id := range i.offerOrder {
		offer, ok := i.offers[id]
		if ok && offer.Tenant == tenant {
			offers = append(offers, offer)
		}
	}
	return offers
}

// Snapshot serialises offers, grants, consumed nonces and transactions.
func (i *DeferredCredentialIssuer) Snapshot() ([]byte, error) {
	i.mu.RLock()
	state := snapshotState{
		Offers:         make([]Offer, 0, len(i.offerOrder)),
		Grants:         make([]Grant, 0, len(i.grants)),
		Transactions:   make([]transactionRecord, 0, len(i.transactions)),
		ConsumedNonces: make([]string, 0, len(i.consumedNonces)),
	}
	for _, id := range i.offerOrder {
		if offer, ok := i.offers[id]; ok {
			state.Offers = append(state.Offers, offer)
		}
	}
	grantIDs := make([]string, 0, len(i.grants))
	for id := range i.grants {
		grantIDs = append(grantIDs, id)
	}
	sort.Strings(grantIDs)
	for _, id := range grantIDs {
		state.Grants = append(state.Grants, i.grants[id])
	}
	transactionIDs := make([]string, 0, len(i.transactions))
	for id := range i.transactions {
		transactionIDs = append(transactionIDs, id)
	}
	sort.Strings(transactionIDs)
	for _, id := range transactionIDs {
		state.Transactions = append(state.Transactions, *i.transactions[id])
	}
	for nonce := range i.consumedNonces {
		state.ConsumedNonces = append(state.ConsumedNonces, nonce)
	}
	sort.Strings(state.ConsumedNonces)
	i.mu.RUnlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  payload,
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (i *DeferredCredentialIssuer) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if !strings.EqualFold(envelope.Checksum, hex.EncodeToString(sum[:])) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil {
		return ErrBadSnapshot
	}
	offers, offerOrder, grants, transactions, consumedNonces, ok := buildRestoredState(state)
	if !ok {
		return ErrBadSnapshot
	}

	i.mu.Lock()
	i.offers = offers
	i.offerOrder = offerOrder
	i.grants = grants
	i.transactions = transactions
	i.consumedNonces = consumedNonces
	i.mu.Unlock()
	return nil
}

func (i *DeferredCredentialIssuer) openOfferCountLocked(tenant string, now time.Time) int {
	count := 0
	for _, id := range i.offerOrder {
		offer, ok := i.offers[id]
		if ok && offer.Tenant == tenant && !offer.Redeemed && now.Before(offer.ExpiresAt) {
			count++
		}
	}
	return count
}

func (i *DeferredCredentialIssuer) tenantPendingLocked(tenant string, now time.Time) int {
	count := 0
	for _, transaction := range i.transactions {
		if transaction.Tenant == tenant && transaction.Status == StatusPending &&
			!i.transactionExpiredLocked(transaction, now) {
			count++
		}
	}
	return count
}

func (i *DeferredCredentialIssuer) transactionExpiredLocked(transaction *transactionRecord, now time.Time) bool {
	return !transaction.ExpiresAt.IsZero() && !now.Before(transaction.ExpiresAt)
}

func (i *DeferredCredentialIssuer) viewLocked(transaction *transactionRecord) Issued {
	return Issued{
		TransactionID: transaction.TransactionID,
		Credential:    transaction.Credential,
		Status:        transaction.Status,
		Tenant:        transaction.Tenant,
		Polls:         transaction.Polls,
		IntervalSecs:  pollIntervalSeconds(i.cfg.PollInterval),
	}
}

func (i *DeferredCredentialIssuer) credentialFor(transactionID string) string {
	return "credential-" + transactionID
}

func (i *DeferredCredentialIssuer) uniqueNonceLocked() (string, error) {
	return i.uniqueTokenLocked("", func(candidate string) bool {
		if _, consumed := i.consumedNonces[candidate]; consumed {
			return true
		}
		for _, grant := range i.grants {
			if grant.CNonce == candidate {
				return true
			}
		}
		return false
	})
}

func (i *DeferredCredentialIssuer) uniqueTokenLocked(prefix string, exists func(string) bool) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		buf := make([]byte, 16)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		candidate := prefix + hex.EncodeToString(buf)
		if !exists(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("unable to allocate a unique identifier")
}

func pollIntervalSeconds(interval time.Duration) int {
	if interval <= 0 {
		return 0
	}
	seconds := int64(interval / time.Second)
	if interval%time.Second != 0 {
		seconds++
	}
	return int(seconds)
}

func buildRestoredState(state snapshotState) (map[string]Offer, []string, map[string]Grant, map[string]*transactionRecord, map[string]struct{}, bool) {
	offers := make(map[string]Offer, len(state.Offers))
	offerOrder := make([]string, 0, len(state.Offers))
	for _, offer := range state.Offers {
		if offer.ID == "" || offer.Tenant == "" || offer.Subject == "" ||
			offer.PreAuthCode == "" || offer.BindingKey == "" || offer.BatchSize < 1 {
			return nil, nil, nil, nil, nil, false
		}
		if _, duplicate := offers[offer.ID]; duplicate {
			return nil, nil, nil, nil, nil, false
		}
		offers[offer.ID] = offer
		offerOrder = append(offerOrder, offer.ID)
	}

	grants := make(map[string]Grant, len(state.Grants))
	grantNonces := make(map[string]struct{}, len(state.Grants))
	grantOffers := make(map[string]struct{}, len(state.Grants))
	for _, grant := range state.Grants {
		if grant.ID == "" || grant.OfferID == "" || grant.Tenant == "" || grant.Subject == "" ||
			grant.BindingKey == "" || grant.CNonce == "" {
			return nil, nil, nil, nil, nil, false
		}
		if _, duplicate := grants[grant.ID]; duplicate {
			return nil, nil, nil, nil, nil, false
		}
		if _, duplicate := grantNonces[grant.CNonce]; duplicate {
			return nil, nil, nil, nil, nil, false
		}
		offer, found := offers[grant.OfferID]
		if !found || !offer.Redeemed || offer.Tenant != grant.Tenant || offer.Subject != grant.Subject {
			return nil, nil, nil, nil, nil, false
		}
		grants[grant.ID] = grant
		grantNonces[grant.CNonce] = struct{}{}
		grantOffers[grant.OfferID] = struct{}{}
	}
	for _, offer := range state.Offers {
		_, hasGrant := grantOffers[offer.ID]
		if offer.Redeemed != hasGrant {
			return nil, nil, nil, nil, nil, false
		}
	}

	transactions := make(map[string]*transactionRecord, len(state.Transactions))
	for idx := range state.Transactions {
		transaction := state.Transactions[idx]
		if transaction.TransactionID == "" || transaction.Tenant == "" || transaction.Subject == "" ||
			transaction.OfferID == "" || transaction.GrantID == "" || transaction.Polls < 0 {
			return nil, nil, nil, nil, nil, false
		}
		if _, duplicate := transactions[transaction.TransactionID]; duplicate {
			return nil, nil, nil, nil, nil, false
		}
		switch transaction.Status {
		case StatusPending, StatusCancelled:
			if transaction.Credential != "" {
				return nil, nil, nil, nil, nil, false
			}
		case StatusIssued:
			if transaction.Credential == "" {
				return nil, nil, nil, nil, nil, false
			}
		default:
			return nil, nil, nil, nil, nil, false
		}
		grant, found := grants[transaction.GrantID]
		if !found || grant.Tenant != transaction.Tenant || grant.Subject != transaction.Subject ||
			grant.OfferID != transaction.OfferID {
			return nil, nil, nil, nil, nil, false
		}
		transactions[transaction.TransactionID] = &transaction
	}

	consumedNonces := make(map[string]struct{}, len(state.ConsumedNonces))
	for _, nonce := range state.ConsumedNonces {
		if nonce == "" {
			return nil, nil, nil, nil, nil, false
		}
		if _, duplicate := consumedNonces[nonce]; duplicate {
			return nil, nil, nil, nil, nil, false
		}
		if _, found := grantNonces[nonce]; !found {
			return nil, nil, nil, nil, nil, false
		}
		consumedNonces[nonce] = struct{}{}
	}

	return offers, offerOrder, grants, transactions, consumedNonces, true
}
