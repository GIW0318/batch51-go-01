// Package task issues, renews and cancels WS-Trust style security tokens for
// the federated login endpoint and keeps one token family per principal and
// audience pair.
package task

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Token states reported by Explain.
const (
	StateActive     = "active"
	StateExpired    = "expired"
	StateRevoked    = "revoked"
	StateSuperseded = "superseded"
)

// Reasons written on a tombstone.
const (
	ReasonCancelled = "cancelled"
	ReasonRenewed   = "renewed"
)

// Errors returned by the issuer.
var (
	ErrMissingField      = errors.New("principal and applies to are required")
	ErrLifetimeTooLong   = errors.New("requested lifetime exceeds the policy cap")
	ErrPrincipalQuota    = errors.New("too many live tokens for this principal")
	ErrAudienceQuota     = errors.New("too many live tokens for this audience")
	ErrUnknownToken      = errors.New("token not found")
	ErrTokenExpired      = errors.New("token expired")
	ErrTokenRevoked      = errors.New("token was revoked")
	ErrTokenSuperseded   = errors.New("token was superseded by a renewal")
	ErrAudienceMismatch  = errors.New("token was issued for another audience")
	ErrAudienceChange    = errors.New("renewal may not change the audience")
	ErrRenewWindowClosed = errors.New("renewal arrived outside the renewal window")
	ErrProofMismatch     = errors.New("proof of possession key does not match")
	ErrEmptyEntropy      = errors.New("server entropy must not be empty")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// IssuerPolicy bounds the token family tables.
type IssuerPolicy struct {
	Realm                  string
	MaxLifetimeSeconds     int
	DefaultLifetimeSeconds int
	RenewWindowSeconds     int
	MaxTokensPerPrincipal  int
	MaxTokensPerAudience   int
	MaxTombstones          int
	Now                    func() time.Time
}

// RequestSecurityToken is the RST body accepted by Issue and Renew.
type RequestSecurityToken struct {
	Principal       string
	AppliesTo       string
	LifetimeSeconds int
	ClientEntropy   []byte
	RequestID       string
	TokenID         string
}

// ProofOfPossession carries the key the client must present for Cancel.
type ProofOfPossession struct {
	Key string
}

// Response is the RSTR produced by Issue and Renew.
type Response struct {
	TokenID         string    `json:"token_id"`
	Family          string    `json:"family"`
	Principal       string    `json:"principal"`
	AppliesTo       string    `json:"applies_to"`
	LifetimeSeconds int       `json:"lifetime_seconds"`
	ProofKey        string    `json:"proof_key"`
	ExpiresAt       time.Time `json:"expires_at"`
	Renewed         bool      `json:"renewed"`
	Idempotent      bool      `json:"idempotent"`
}

// Claims is the assertion returned by Validate.
type Claims struct {
	Principal string    `json:"principal"`
	AppliesTo string    `json:"applies_to"`
	Family    string    `json:"family"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Trace is the operator view of one token.
type Trace struct {
	TokenID       string    `json:"token_id"`
	Family        string    `json:"family"`
	State         string    `json:"state"`
	Principal     string    `json:"principal"`
	AppliesTo     string    `json:"applies_to"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Renewals      int       `json:"renewals"`
	RevokedReason string    `json:"revoked_reason"`
}

// IssuerStats aggregates the counters of the issuer.
type IssuerStats struct {
	Issued     int `json:"issued"`
	Renewed    int `json:"renewed"`
	Cancelled  int `json:"cancelled"`
	Rejected   int `json:"rejected"`
	Tombstoned int `json:"tombstoned"`
}

type tokenRecord struct {
	TokenID         string    `json:"token_id"`
	Family          string    `json:"family"`
	Principal       string    `json:"principal"`
	AppliesTo       string    `json:"applies_to"`
	LifetimeSeconds int       `json:"lifetime_seconds"`
	ProofKey        string    `json:"proof_key"`
	IssuedAt        time.Time `json:"issued_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Renewals        int       `json:"renewals"`
	State           string    `json:"state"`
	RevokedReason   string    `json:"revoked_reason,omitempty"`
	Seq             uint64    `json:"seq"`
}

type familyRecord struct {
	Family    string `json:"family"`
	Principal string `json:"principal"`
	AppliesTo string `json:"applies_to"`
	Seq       uint64 `json:"seq"`
}

type familyKey struct {
	Principal string
	AppliesTo string
}

type tombstoneRecord struct {
	TokenID   string    `json:"token_id"`
	Family    string    `json:"family"`
	Principal string    `json:"principal"`
	AppliesTo string    `json:"applies_to"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
	Seq       uint64    `json:"seq"`
}

type idempotentRecord struct {
	Key         string   `json:"key"`
	Fingerprint string   `json:"fingerprint"`
	TokenID     string   `json:"token_id"`
	Response    Response `json:"response"`
}

const snapshotVersion = 1

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotData struct {
	Tokens        []tokenRecord      `json:"tokens"`
	Families      []familyRecord     `json:"families"`
	Tombstones    []tombstoneRecord  `json:"tombstones"`
	Idempotency   []idempotentRecord `json:"idempotency"`
	NextToken     uint64             `json:"next_token"`
	NextFamily    uint64             `json:"next_family"`
	ServerEntropy []byte             `json:"server_entropy"`
	Stats         IssuerStats        `json:"stats"`
}

var entropyFallback uint64

// WsTrustIssuer owns the token table, the families and the revocation
// tombstones.
type WsTrustIssuer struct {
	cfg IssuerPolicy

	mu              sync.Mutex
	serverEntropy   []byte
	nextToken       uint64
	nextFamily      uint64
	tokens          map[string]*tokenRecord
	families        map[string]*familyRecord
	familyByBinding map[familyKey]string
	tombstones      map[string]*tombstoneRecord
	idempotency     map[string]*idempotentRecord
	stats           IssuerStats
}

// NewWsTrustIssuer builds an issuer bound to one policy.
func NewWsTrustIssuer(cfg IssuerPolicy) *WsTrustIssuer {
	entropy := make([]byte, sha256.Size)
	if _, err := rand.Read(entropy); err != nil {
		fallback := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", cfg.Realm, atomic.AddUint64(&entropyFallback, 1))))
		copy(entropy, fallback[:])
	}
	return &WsTrustIssuer{
		cfg:             cfg,
		serverEntropy:   entropy,
		tokens:          make(map[string]*tokenRecord),
		families:        make(map[string]*familyRecord),
		familyByBinding: make(map[familyKey]string),
		tombstones:      make(map[string]*tombstoneRecord),
		idempotency:     make(map[string]*idempotentRecord),
	}
}

func (w *WsTrustIssuer) now() time.Time {
	if w.cfg.Now != nil {
		return w.cfg.Now()
	}
	return time.Now()
}

func normalizeRequest(req RequestSecurityToken) RequestSecurityToken {
	req.Principal = strings.TrimSpace(req.Principal)
	req.AppliesTo = strings.TrimSpace(req.AppliesTo)
	req.RequestID = strings.TrimSpace(req.RequestID)
	req.TokenID = strings.TrimSpace(req.TokenID)
	return req
}

func (w *WsTrustIssuer) effectiveLifetime(requested int) int {
	if requested == 0 {
		return w.cfg.DefaultLifetimeSeconds
	}
	return requested
}

func (w *WsTrustIssuer) lifetimeAllowed(lifetime int) bool {
	return w.cfg.MaxLifetimeSeconds < 0 || lifetime <= w.cfg.MaxLifetimeSeconds
}

func activeToken(t *tokenRecord, now time.Time) bool {
	return t.State == StateActive && now.Before(t.ExpiresAt)
}

func liveToken(t *tokenRecord, now time.Time) bool {
	return t.State != StateRevoked && now.Before(t.ExpiresAt)
}

func (w *WsTrustIssuer) countForPrincipalLocked(principal string, now time.Time) int {
	count := 0
	for _, t := range w.tokens {
		if t.Principal == principal && activeToken(t, now) {
			count++
		}
	}
	return count
}

func (w *WsTrustIssuer) countForAudienceLocked(appliesTo string, now time.Time) int {
	count := 0
	for _, t := range w.tokens {
		if t.AppliesTo == appliesTo && activeToken(t, now) {
			count++
		}
	}
	return count
}

func (w *WsTrustIssuer) rejectLocked() {
	w.stats.Rejected++
}

func (w *WsTrustIssuer) allocateTokenIDLocked() string {
	w.nextToken++
	return fmt.Sprintf("tok-%016x", w.nextToken)
}

func (w *WsTrustIssuer) allocateFamilyIDLocked() string {
	w.nextFamily++
	return fmt.Sprintf("fam-%016x", w.nextFamily)
}

func (w *WsTrustIssuer) familyLocked(principal, appliesTo string) *familyRecord {
	key := familyKey{Principal: principal, AppliesTo: appliesTo}
	if id, ok := w.familyByBinding[key]; ok {
		return w.families[id]
	}
	family := &familyRecord{
		Family:    w.allocateFamilyIDLocked(),
		Principal: principal,
		AppliesTo: appliesTo,
		Seq:       w.nextFamily,
	}
	w.families[family.Family] = family
	w.familyByBinding[key] = family.Family
	return family
}

func (w *WsTrustIssuer) proofKeyLocked(clientEntropy []byte) (string, error) {
	key, err := w.DeriveKey(w.serverEntropy, clientEntropy)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

func (w *WsTrustIssuer) addTombstoneLocked(t *tokenRecord) {
	if existing, ok := w.tombstones[t.TokenID]; ok {
		existing.Family = t.Family
		existing.Principal = t.Principal
		existing.AppliesTo = t.AppliesTo
		existing.Reason = t.RevokedReason
		existing.ExpiresAt = t.ExpiresAt
		return
	}
	w.tombstones[t.TokenID] = &tombstoneRecord{
		TokenID:   t.TokenID,
		Family:    t.Family,
		Principal: t.Principal,
		AppliesTo: t.AppliesTo,
		Reason:    t.RevokedReason,
		ExpiresAt: t.ExpiresAt,
		Seq:       t.Seq,
	}
}

func trimTombstones(tokens map[string]*tokenRecord, tombstones map[string]*tombstoneRecord, idempotency map[string]*idempotentRecord, max int) {
	if max < 0 || len(tombstones) <= max {
		return
	}
	ordered := make([]*tombstoneRecord, 0, len(tombstones))
	for _, tombstone := range tombstones {
		ordered = append(ordered, tombstone)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ExpiresAt.Equal(ordered[j].ExpiresAt) {
			return ordered[i].Seq < ordered[j].Seq
		}
		return ordered[i].ExpiresAt.Before(ordered[j].ExpiresAt)
	})
	for _, tombstone := range ordered[:len(ordered)-max] {
		delete(tombstones, tombstone.TokenID)
		delete(tokens, tombstone.TokenID)
	}
	for key, record := range idempotency {
		if _, ok := tokens[record.TokenID]; !ok {
			delete(idempotency, key)
		}
	}
}

func (w *WsTrustIssuer) enforceTombstoneCapLocked() {
	trimTombstones(w.tokens, w.tombstones, w.idempotency, w.cfg.MaxTombstones)
	w.stats.Tombstoned = len(w.tombstones)
}

func (w *WsTrustIssuer) responseFor(t *tokenRecord) Response {
	return Response{
		TokenID:         t.TokenID,
		Family:          t.Family,
		Principal:       t.Principal,
		AppliesTo:       t.AppliesTo,
		LifetimeSeconds: t.LifetimeSeconds,
		ProofKey:        t.ProofKey,
		ExpiresAt:       t.ExpiresAt,
		Renewed:         t.Renewals > 0,
	}
}

func hashFields(kind string, fields ...[]byte) string {
	h := sha256.New()
	writeField := func(value []byte) {
		var length [8]byte
		n := uint64(len(value))
		for i := range length {
			length[len(length)-1-i] = byte(n >> (8 * i))
		}
		h.Write(length[:])
		h.Write(value)
	}
	writeField([]byte(kind))
	for _, field := range fields {
		writeField(field)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func issueFingerprint(req RequestSecurityToken, lifetime int) string {
	return hashFields(
		"issue",
		[]byte(req.RequestID),
		[]byte(req.Principal),
		[]byte(req.AppliesTo),
		[]byte(strconv.Itoa(lifetime)),
		req.ClientEntropy,
	)
}

func renewFingerprint(req RequestSecurityToken, lifetime int) string {
	return hashFields(
		"renew",
		[]byte(req.RequestID),
		[]byte(req.TokenID),
		[]byte(req.AppliesTo),
		[]byte(strconv.Itoa(lifetime)),
		req.ClientEntropy,
	)
}

func idempotencyKey(kind, requestID string) string {
	return kind + "\x00" + requestID
}

func (w *WsTrustIssuer) idempotentResponseLocked(kind, requestID, fingerprint string) (Response, bool) {
	if requestID == "" {
		return Response{}, false
	}
	record, ok := w.idempotency[idempotencyKey(kind, requestID)]
	if !ok || record.Fingerprint != fingerprint {
		return Response{}, false
	}
	response := record.Response
	response.Idempotent = true
	return response, true
}

func (w *WsTrustIssuer) rememberResponseLocked(kind, requestID, fingerprint string, response Response) {
	if requestID == "" {
		return
	}
	key := idempotencyKey(kind, requestID)
	w.idempotency[key] = &idempotentRecord{
		Key:         key,
		Fingerprint: fingerprint,
		TokenID:     response.TokenID,
		Response:    response,
	}
}

// Issue answers an issue request, idempotently for a repeated request id.
func (w *WsTrustIssuer) Issue(req RequestSecurityToken) (Response, error) {
	req = normalizeRequest(req)
	lifetime := w.effectiveLifetime(req.LifetimeSeconds)
	fingerprint := issueFingerprint(req, lifetime)

	w.mu.Lock()
	defer w.mu.Unlock()

	if req.Principal == "" || req.AppliesTo == "" {
		w.rejectLocked()
		return Response{}, ErrMissingField
	}
	if req.LifetimeSeconds < 0 || !w.lifetimeAllowed(lifetime) {
		w.rejectLocked()
		return Response{}, ErrLifetimeTooLong
	}
	if response, ok := w.idempotentResponseLocked("issue", req.RequestID, fingerprint); ok {
		return response, nil
	}

	now := w.now()
	if w.cfg.MaxTokensPerPrincipal >= 0 && w.countForPrincipalLocked(req.Principal, now) >= w.cfg.MaxTokensPerPrincipal {
		w.rejectLocked()
		return Response{}, ErrPrincipalQuota
	}
	if w.cfg.MaxTokensPerAudience >= 0 && w.countForAudienceLocked(req.AppliesTo, now) >= w.cfg.MaxTokensPerAudience {
		w.rejectLocked()
		return Response{}, ErrAudienceQuota
	}

	proofKey, err := w.proofKeyLocked(req.ClientEntropy)
	if err != nil {
		w.rejectLocked()
		return Response{}, err
	}
	family := w.familyLocked(req.Principal, req.AppliesTo)
	token := &tokenRecord{
		TokenID:         w.allocateTokenIDLocked(),
		Family:          family.Family,
		Principal:       req.Principal,
		AppliesTo:       req.AppliesTo,
		LifetimeSeconds: lifetime,
		ProofKey:        proofKey,
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Duration(lifetime) * time.Second),
		State:           StateActive,
		Seq:             w.nextToken,
	}
	w.tokens[token.TokenID] = token
	response := w.responseFor(token)
	w.stats.Issued++
	w.rememberResponseLocked("issue", req.RequestID, fingerprint, response)
	return response, nil
}

// Renew extends one live token inside the renewal window.
func (w *WsTrustIssuer) Renew(req RequestSecurityToken) (Response, error) {
	req = normalizeRequest(req)
	lifetime := w.effectiveLifetime(req.LifetimeSeconds)
	fingerprint := renewFingerprint(req, lifetime)

	w.mu.Lock()
	defer w.mu.Unlock()

	token, ok := w.tokens[req.TokenID]
	if !ok {
		w.rejectLocked()
		return Response{}, ErrUnknownToken
	}
	if req.AppliesTo == "" {
		w.rejectLocked()
		return Response{}, ErrMissingField
	}
	if req.LifetimeSeconds < 0 || !w.lifetimeAllowed(lifetime) {
		w.rejectLocked()
		return Response{}, ErrLifetimeTooLong
	}
	if response, ok := w.idempotentResponseLocked("renew", req.RequestID, fingerprint); ok {
		return response, nil
	}
	if req.AppliesTo != token.AppliesTo {
		w.rejectLocked()
		return Response{}, ErrAudienceChange
	}
	if token.State == StateRevoked {
		w.rejectLocked()
		return Response{}, ErrTokenRevoked
	}
	if token.State == StateSuperseded {
		w.rejectLocked()
		return Response{}, ErrTokenSuperseded
	}

	now := w.now()
	windowStart := token.ExpiresAt.Add(-time.Duration(w.cfg.RenewWindowSeconds) * time.Second)
	if now.Before(windowStart) || !now.Before(token.ExpiresAt) {
		w.rejectLocked()
		return Response{}, ErrRenewWindowClosed
	}

	proofKey, err := w.proofKeyLocked(req.ClientEntropy)
	if err != nil {
		w.rejectLocked()
		return Response{}, err
	}
	renewed := &tokenRecord{
		TokenID:         w.allocateTokenIDLocked(),
		Family:          token.Family,
		Principal:       token.Principal,
		AppliesTo:       token.AppliesTo,
		LifetimeSeconds: lifetime,
		ProofKey:        proofKey,
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Duration(lifetime) * time.Second),
		Renewals:        token.Renewals + 1,
		State:           StateActive,
		Seq:             w.nextToken,
	}
	token.State = StateSuperseded
	token.RevokedReason = ReasonRenewed
	w.addTombstoneLocked(token)
	w.tokens[renewed.TokenID] = renewed
	w.stats.Renewed++
	w.enforceTombstoneCapLocked()

	response := w.responseFor(renewed)
	w.rememberResponseLocked("renew", req.RequestID, fingerprint, response)
	return response, nil
}

// Cancel revokes the whole token family of one token.
func (w *WsTrustIssuer) Cancel(tokenID string, proof ProofOfPossession) error {
	tokenID = strings.TrimSpace(tokenID)

	w.mu.Lock()
	defer w.mu.Unlock()

	token, ok := w.tokens[tokenID]
	if !ok {
		w.rejectLocked()
		return ErrUnknownToken
	}
	if subtle.ConstantTimeCompare([]byte(token.ProofKey), []byte(proof.Key)) != 1 {
		w.rejectLocked()
		return ErrProofMismatch
	}

	changed := false
	for _, familyToken := range w.tokens {
		if familyToken.Family != token.Family {
			continue
		}
		if familyToken.State != StateRevoked || familyToken.RevokedReason != ReasonCancelled {
			changed = true
		}
		familyToken.State = StateRevoked
		familyToken.RevokedReason = ReasonCancelled
		w.addTombstoneLocked(familyToken)
	}
	if changed {
		w.stats.Cancelled++
	}
	w.enforceTombstoneCapLocked()
	return nil
}

// Validate turns one live token into claims for the given audience.
func (w *WsTrustIssuer) Validate(tokenID, appliesTo string) (Claims, error) {
	tokenID = strings.TrimSpace(tokenID)
	appliesTo = strings.TrimSpace(appliesTo)

	w.mu.Lock()
	defer w.mu.Unlock()

	token, ok := w.tokens[tokenID]
	if !ok {
		return Claims{}, ErrUnknownToken
	}
	switch token.State {
	case StateRevoked:
		return Claims{}, ErrTokenRevoked
	case StateSuperseded:
		return Claims{}, ErrTokenSuperseded
	}
	if !w.now().Before(token.ExpiresAt) {
		return Claims{}, ErrTokenExpired
	}
	if token.AppliesTo != appliesTo {
		return Claims{}, ErrAudienceMismatch
	}
	return Claims{
		Principal: token.Principal,
		AppliesTo: token.AppliesTo,
		Family:    token.Family,
		ExpiresAt: token.ExpiresAt,
	}, nil
}

// Explain returns the operator view of one token.
func (w *WsTrustIssuer) Explain(tokenID string) (Trace, error) {
	tokenID = strings.TrimSpace(tokenID)

	w.mu.Lock()
	defer w.mu.Unlock()

	token, ok := w.tokens[tokenID]
	if !ok {
		return Trace{}, ErrUnknownToken
	}
	state := token.State
	switch state {
	case StateRevoked, StateSuperseded:
	case StateActive:
		if !w.now().Before(token.ExpiresAt) {
			state = StateExpired
		}
	}
	return Trace{
		TokenID:       token.TokenID,
		Family:        token.Family,
		State:         state,
		Principal:     token.Principal,
		AppliesTo:     token.AppliesTo,
		IssuedAt:      token.IssuedAt,
		ExpiresAt:     token.ExpiresAt,
		Renewals:      token.Renewals,
		RevokedReason: token.RevokedReason,
	}, nil
}

// TokensFor counts the live tokens of one principal.
func (w *WsTrustIssuer) TokensFor(principal string) int {
	principal = strings.TrimSpace(principal)

	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	count := 0
	for _, token := range w.tokens {
		if token.Principal == principal && liveToken(token, now) {
			count++
		}
	}
	return count
}

// FamilyOf reports the family name of one token.
func (w *WsTrustIssuer) FamilyOf(tokenID string) (string, bool) {
	tokenID = strings.TrimSpace(tokenID)

	w.mu.Lock()
	defer w.mu.Unlock()
	if token, ok := w.tokens[tokenID]; ok {
		return token.Family, true
	}
	if tombstone, ok := w.tombstones[tokenID]; ok {
		return tombstone.Family, true
	}
	return "", false
}

// IsRevoked reports whether one token id is covered by a tombstone.
func (w *WsTrustIssuer) IsRevoked(tokenID string) bool {
	tokenID = strings.TrimSpace(tokenID)

	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.tombstones[tokenID]
	return ok
}

// KeyMaterial returns the hex proof key bound to one token.
func (w *WsTrustIssuer) KeyMaterial(tokenID string) (string, bool) {
	tokenID = strings.TrimSpace(tokenID)

	w.mu.Lock()
	defer w.mu.Unlock()
	token, ok := w.tokens[tokenID]
	if !ok {
		return "", false
	}
	return token.ProofKey, true
}

// DeriveKey combines the server and client entropy into one proof key.
func (w *WsTrustIssuer) DeriveKey(serverEntropy, clientEntropy []byte) ([]byte, error) {
	if len(serverEntropy) == 0 {
		return nil, ErrEmptyEntropy
	}
	material := make([]byte, 0, len(serverEntropy)+len(clientEntropy))
	material = append(material, serverEntropy...)
	material = append(material, clientEntropy...)
	sum := sha256.Sum256(material)
	return sum[:], nil
}

// Stats returns a snapshot of the counters.
func (w *WsTrustIssuer) Stats() IssuerStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

func snapshotChecksum(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Snapshot serialises tokens, families, tombstones and counters.
func (w *WsTrustIssuer) Snapshot() ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	data := snapshotData{
		Tokens:        make([]tokenRecord, 0, len(w.tokens)),
		Families:      make([]familyRecord, 0, len(w.families)),
		Tombstones:    make([]tombstoneRecord, 0, len(w.tombstones)),
		Idempotency:   make([]idempotentRecord, 0, len(w.idempotency)),
		NextToken:     w.nextToken,
		NextFamily:    w.nextFamily,
		ServerEntropy: append([]byte(nil), w.serverEntropy...),
		Stats:         w.stats,
	}
	for _, token := range w.tokens {
		data.Tokens = append(data.Tokens, *token)
	}
	for _, family := range w.families {
		data.Families = append(data.Families, *family)
	}
	for _, tombstone := range w.tombstones {
		data.Tombstones = append(data.Tombstones, *tombstone)
	}
	for _, record := range w.idempotency {
		data.Idempotency = append(data.Idempotency, *record)
	}
	sort.Slice(data.Tokens, func(i, j int) bool { return data.Tokens[i].Seq < data.Tokens[j].Seq })
	sort.Slice(data.Families, func(i, j int) bool { return data.Families[i].Seq < data.Families[j].Seq })
	sort.Slice(data.Tombstones, func(i, j int) bool { return data.Tombstones[i].Seq < data.Tombstones[j].Seq })
	sort.Slice(data.Idempotency, func(i, j int) bool { return data.Idempotency[i].Key < data.Idempotency[j].Key })

	payload, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	envelope, err := json.Marshal(snapshotEnvelope{
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: snapshotChecksum(payload),
	})
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

func validState(state string) bool {
	return state == StateActive || state == StateSuperseded || state == StateRevoked
}

func validReason(reason string) bool {
	return reason == ReasonCancelled || reason == ReasonRenewed
}

func nonzeroTime(t time.Time) bool {
	return !t.IsZero()
}

func validateStats(stats IssuerStats) bool {
	return stats.Issued >= 0 && stats.Renewed >= 0 && stats.Cancelled >= 0 && stats.Rejected >= 0 && stats.Tombstoned >= 0
}

func (w *WsTrustIssuer) buildRestoredState(data snapshotData) (map[string]*tokenRecord, map[string]*familyRecord, map[familyKey]string, map[string]*tombstoneRecord, map[string]*idempotentRecord, error) {
	if len(data.ServerEntropy) == 0 || !validateStats(data.Stats) {
		return nil, nil, nil, nil, nil, ErrBadSnapshot
	}

	families := make(map[string]*familyRecord, len(data.Families))
	familyByBinding := make(map[familyKey]string, len(data.Families))
	var maxFamilySeq uint64
	for i := range data.Families {
		family := data.Families[i]
		if family.Family == "" || family.Principal == "" || family.AppliesTo == "" || family.Seq == 0 {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := families[family.Family]; exists {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		key := familyKey{Principal: family.Principal, AppliesTo: family.AppliesTo}
		if _, exists := familyByBinding[key]; exists {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		families[family.Family] = &family
		familyByBinding[key] = family.Family
		if family.Seq > maxFamilySeq {
			maxFamilySeq = family.Seq
		}
	}
	if data.NextFamily < maxFamilySeq {
		return nil, nil, nil, nil, nil, ErrBadSnapshot
	}

	tokens := make(map[string]*tokenRecord, len(data.Tokens))
	seenSeq := make(map[uint64]struct{}, len(data.Tokens))
	var maxTokenSeq uint64
	for i := range data.Tokens {
		token := data.Tokens[i]
		if token.TokenID == "" || token.Family == "" || token.Principal == "" || token.AppliesTo == "" || token.ProofKey == "" || token.Seq == 0 || !validState(token.State) {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := tokens[token.TokenID]; exists {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := seenSeq[token.Seq]; exists {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if token.LifetimeSeconds <= 0 || token.Renewals < 0 || !nonzeroTime(token.IssuedAt) || !nonzeroTime(token.ExpiresAt) || token.ExpiresAt.Before(token.IssuedAt) {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if decoded, err := hex.DecodeString(token.ProofKey); err != nil || len(decoded) != sha256.Size {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		family, ok := families[token.Family]
		if !ok || family.Principal != token.Principal || family.AppliesTo != token.AppliesTo {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		tokens[token.TokenID] = &token
		seenSeq[token.Seq] = struct{}{}
		if token.Seq > maxTokenSeq {
			maxTokenSeq = token.Seq
		}
	}
	if data.NextToken < maxTokenSeq {
		return nil, nil, nil, nil, nil, ErrBadSnapshot
	}

	tombstones := make(map[string]*tombstoneRecord, len(data.Tombstones))
	for i := range data.Tombstones {
		tombstone := data.Tombstones[i]
		if tombstone.TokenID == "" || tombstone.Family == "" || tombstone.Principal == "" || tombstone.AppliesTo == "" || tombstone.Seq == 0 || !validReason(tombstone.Reason) || !nonzeroTime(tombstone.ExpiresAt) {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := tombstones[tombstone.TokenID]; exists {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		token, ok := tokens[tombstone.TokenID]
		if !ok || token.Family != tombstone.Family || token.Principal != tombstone.Principal || token.AppliesTo != tombstone.AppliesTo || token.Seq != tombstone.Seq || !token.ExpiresAt.Equal(tombstone.ExpiresAt) {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if token.State == StateActive || token.RevokedReason != tombstone.Reason {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		tombstones[tombstone.TokenID] = &tombstone
	}
	for _, token := range tokens {
		_, tombstoned := tombstones[token.TokenID]
		if token.State == StateActive && tombstoned {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if token.State != StateActive && !tombstoned {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
	}

	idempotency := make(map[string]*idempotentRecord, len(data.Idempotency))
	for i := range data.Idempotency {
		record := data.Idempotency[i]
		if record.Key == "" || record.Fingerprint == "" || record.TokenID == "" || record.Response.TokenID != record.TokenID {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, exists := idempotency[record.Key]; exists {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		if _, ok := tokens[record.TokenID]; !ok {
			return nil, nil, nil, nil, nil, ErrBadSnapshot
		}
		idempotency[record.Key] = &record
	}

	now := w.now()
	principalCounts := make(map[string]int)
	audienceCounts := make(map[string]int)
	for _, token := range tokens {
		if !activeToken(token, now) {
			continue
		}
		principalCounts[token.Principal]++
		audienceCounts[token.AppliesTo]++
	}
	if w.cfg.MaxTokensPerPrincipal >= 0 {
		for _, count := range principalCounts {
			if count > w.cfg.MaxTokensPerPrincipal {
				return nil, nil, nil, nil, nil, ErrBadSnapshot
			}
		}
	}
	if w.cfg.MaxTokensPerAudience >= 0 {
		for _, count := range audienceCounts {
			if count > w.cfg.MaxTokensPerAudience {
				return nil, nil, nil, nil, nil, ErrBadSnapshot
			}
		}
	}

	trimTombstones(tokens, tombstones, idempotency, w.cfg.MaxTombstones)
	data.Stats.Tombstoned = len(tombstones)
	return tokens, families, familyByBinding, tombstones, idempotency, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (w *WsTrustIssuer) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != snapshotVersion || len(envelope.Payload) == 0 || envelope.Checksum == "" || subtle.ConstantTimeCompare([]byte(envelope.Checksum), []byte(snapshotChecksum(envelope.Payload))) != 1 {
		return ErrBadSnapshot
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	var state snapshotData
	if err := decoder.Decode(&state); err != nil {
		return ErrBadSnapshot
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrBadSnapshot
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	tokens, families, familyByBinding, tombstones, idempotency, err := w.buildRestoredState(state)
	if err != nil {
		return ErrBadSnapshot
	}
	w.serverEntropy = append([]byte(nil), state.ServerEntropy...)
	w.nextToken = state.NextToken
	w.nextFamily = state.NextFamily
	w.tokens = tokens
	w.families = families
	w.familyByBinding = familyByBinding
	w.tombstones = tombstones
	w.idempotency = idempotency
	w.stats = state.Stats
	w.stats.Tombstoned = len(tombstones)
	return nil
}
