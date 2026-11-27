// Package task guards SMB2 dialect negotiation and session establishment.
package task

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SMB2 dialect identifiers understood by the gateway.
const (
	Dialect202 = 0x0202
	Dialect210 = 0x0210
	Dialect300 = 0x0300
	Dialect302 = 0x0302
	Dialect311 = 0x0311
)

// Session states.
const (
	SessionPending = "pending"
	SessionActive  = "active"
	SessionClosed  = "closed"
)

// Errors returned by the guard.
var (
	ErrNoCommonDialect = errors.New("no dialect supported by both sides")
	ErrSigningRequired = errors.New("signing is required by policy")
	ErrPendingLimit    = errors.New("pending session limit reached")
	ErrSessionLimit    = errors.New("concurrent session limit reached")
	ErrUnknownSession  = errors.New("unknown session")
	ErrSessionClosed   = errors.New("session is closed")
	ErrBadCredentials  = errors.New("user name and security blob are required")
	ErrTreeLimit       = errors.New("tree connect limit reached for the session")
	ErrBadPath         = errors.New("tree path is not acceptable")
	ErrSignatureFailed = errors.New("message signature does not verify")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")

	// ErrPreauthChain is returned when a SESSION_SETUP message is replayed or
	// arrives out of sequence. Either condition permanently closes the session.
	ErrPreauthChain = errors.New("preauth integrity chain is broken or replayed")
	// ErrSessionNotActive is returned for operations that require a session
	// whose authentication has completed.
	ErrSessionNotActive = errors.New("session is not active")
)

// Smb2Policy bounds the session tables and lists the acceptable shares.
type Smb2Policy struct {
	Dialects           []uint16
	RequireSigning     bool
	MaxSessions        int
	MaxTreesPerSession int
	MaxPending         int
	AllowedShares      []string
}

// NegotiateRequest is one NEGOTIATE request.
type NegotiateRequest struct {
	ClientGuid      string
	Dialects        []uint16
	SigningRequired bool
	PreauthSalt     string
}

// NegotiateResponse is the NEGOTIATE reply.
type NegotiateResponse struct {
	Dialect         uint16 `json:"dialect"`
	SigningRequired bool   `json:"signing_required"`
	SessionID       string `json:"session_id"`
	PreauthHash     string `json:"preauth_hash"`
}

// SessionSetupRequest carries the security blob of one setup round.
type SessionSetupRequest struct {
	MessageID    uint64
	SecurityBlob string
	Username     string
	Signed       bool
	Signature    string
}

// Session is the per connection state kept by the gateway.
type Session struct {
	ID              string    `json:"id"`
	Username        string    `json:"username"`
	State           string    `json:"state"`
	SigningRequired bool      `json:"signing_required"`
	PreauthHash     string    `json:"preauth_hash"`
	Trees           int       `json:"trees"`
	Seq             int64     `json:"seq"`
	Created         time.Time `json:"created"`
}

// Tree is one authorized tree connect.
type Tree struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Share     string `json:"share"`
	Path      string `json:"path"`
}

// Smb2Message is one signed message presented for verification.
type Smb2Message struct {
	SessionID string
	MessageID uint64
	Command   string
	Payload   string
	Signed    bool
	Signature string
}

// Smb2Stats exposes the counters of the guard.
type Smb2Stats struct {
	Sessions int `json:"sessions"`
	Pending  int `json:"pending"`
	Trees    int `json:"trees"`
	Rejected int `json:"rejected"`
	Chained  int `json:"chained"`
}

// sessionRecord contains the wire-visible Session plus the append-only chain
// and tree index that are never recomputed from client input.
type sessionRecord struct {
	info        Session
	chain       []string
	trees       []Tree
	treeByPath  map[string]Tree
	lastSetupID uint64
	hasSetup    bool
}

const (
	snapshotMagic   = "gsb.smb2.session-guard"
	snapshotVersion = 1
)

// Smb2SessionGuard owns the session table and the preauth hash chains.
type Smb2SessionGuard struct {
	mu sync.Mutex

	cfg           Smb2Policy
	allowedShares map[string]struct{}
	sessions      map[string]*sessionRecord
	nextSessionID uint64
	nextTreeID    uint64
	rejected      int
	chained       int
}

type negotiateChainRequest struct {
	ClientGuid      string   `json:"client_guid"`
	Dialects        []uint16 `json:"dialects"`
	SigningRequired bool     `json:"signing_required"`
	PreauthSalt     string   `json:"preauth_salt"`
}

type negotiateChainResponse struct {
	Dialect         uint16 `json:"dialect"`
	SigningRequired bool   `json:"signing_required"`
	SessionID       string `json:"session_id"`
}

type setupChainRequest struct {
	MessageID    uint64 `json:"message_id"`
	Username     string `json:"username"`
	SecurityBlob string `json:"security_blob"`
	Signed       bool   `json:"signed"`
	Signature    string `json:"signature"`
}

type setupChainResponse struct {
	State    string `json:"state"`
	Username string `json:"username"`
	Seq      int64  `json:"seq"`
	Error    string `json:"error,omitempty"`
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type snapshotPayload struct {
	NextSessionID uint64            `json:"next_session_id"`
	NextTreeID    uint64            `json:"next_tree_id"`
	Rejected      int               `json:"rejected"`
	Chained       int               `json:"chained"`
	Sessions      []snapshotSession `json:"sessions"`
}

type snapshotSession struct {
	ID                 string    `json:"id"`
	Username           string    `json:"username"`
	State              string    `json:"state"`
	SigningRequired    bool      `json:"signing_required"`
	PreauthHash        string    `json:"preauth_hash"`
	Seq                int64     `json:"seq"`
	Created            time.Time `json:"created"`
	LastSetupMessageID uint64    `json:"last_setup_message_id"`
	HasSetup           bool      `json:"has_setup"`
	Chain              []string  `json:"chain"`
	Trees              []Tree    `json:"trees"`
}

// NewSmb2SessionGuard builds a guard for one file gateway.
func NewSmb2SessionGuard(cfg Smb2Policy) *Smb2SessionGuard {
	cfgCopy := clonePolicy(cfg)
	allowed := make(map[string]struct{}, len(cfgCopy.AllowedShares))
	for _, share := range cfgCopy.AllowedShares {
		allowed[share] = struct{}{}
	}
	return &Smb2SessionGuard{
		cfg:           cfgCopy,
		allowedShares: allowed,
		sessions:      make(map[string]*sessionRecord),
	}
}

func clonePolicy(cfg Smb2Policy) Smb2Policy {
	cfg.Dialects = append([]uint16(nil), cfg.Dialects...)
	cfg.AllowedShares = append([]string(nil), cfg.AllowedShares...)
	return cfg
}

// Negotiate picks the dialect and opens a pending session.
func (g *Smb2SessionGuard) Negotiate(req NegotiateRequest) (NegotiateResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	dialect, ok := highestCommonDialect(g.cfg.Dialects, req.Dialects)
	if !ok {
		return NegotiateResponse{}, g.rejectLocked(ErrNoCommonDialect)
	}
	signingRequired := g.cfg.RequireSigning || req.SigningRequired
	if signingRequired && !dialectSupportsSigning(dialect) {
		return NegotiateResponse{}, g.rejectLocked(ErrSigningRequired)
	}
	if limitReached(g.pendingCountLocked(), g.cfg.MaxPending) {
		return NegotiateResponse{}, g.rejectLocked(ErrPendingLimit)
	}

	sessionID := g.newSessionIDLocked()
	initialHash := makeChainDigest("", "NEGOTIATE",
		negotiateChainRequest{
			ClientGuid:      req.ClientGuid,
			Dialects:        append([]uint16(nil), req.Dialects...),
			SigningRequired: req.SigningRequired,
			PreauthSalt:     req.PreauthSalt,
		},
		negotiateChainResponse{
			Dialect:         dialect,
			SigningRequired: signingRequired,
			SessionID:       sessionID,
		},
	)
	record := &sessionRecord{
		info: Session{
			ID:              sessionID,
			State:           SessionPending,
			SigningRequired: signingRequired,
			PreauthHash:     initialHash,
			Created:         time.Now().UTC(),
		},
		chain:      []string{initialHash},
		treeByPath: make(map[string]Tree),
	}
	g.sessions[sessionID] = record
	g.chained++

	return NegotiateResponse{
		Dialect:         dialect,
		SigningRequired: signingRequired,
		SessionID:       sessionID,
		PreauthHash:     initialHash,
	}, nil
}

// SessionSetup completes authentication for a pending session.
func (g *Smb2SessionGuard) SessionSetup(sessionID string, req SessionSetupRequest) (Session, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.sessions[sessionID]
	if !ok {
		return Session{}, g.rejectLocked(ErrUnknownSession)
	}
	if record.info.State == SessionClosed {
		return Session{}, g.rejectLocked(ErrSessionClosed)
	}

	if record.hasSetup {
		// A replay repeats an old id; a gap means the transcript has been
		// spliced. Both break the preauth chain and permanently close it.
		if record.lastSetupID == ^uint64(0) || req.MessageID != record.lastSetupID+1 {
			g.closeRecordLocked(record)
			return Session{}, g.rejectLocked(ErrPreauthChain)
		}
	}

	if record.info.SigningRequired {
		if !req.Signed {
			g.appendSetupLocked(record, req, "signature_required")
			g.closeRecordLocked(record)
			return Session{}, g.rejectLocked(ErrSignatureFailed)
		}
		if !signatureMatches(record.info.PreauthHash, req.MessageID, req.SecurityBlob, req.Signature) {
			g.appendSetupLocked(record, req, "signature_failed")
			return Session{}, g.rejectLocked(ErrSignatureFailed)
		}
	} else if req.Signed && !signatureMatches(record.info.PreauthHash, req.MessageID, req.SecurityBlob, req.Signature) {
		g.appendSetupLocked(record, req, "signature_failed")
		return Session{}, g.rejectLocked(ErrSignatureFailed)
	}

	if strings.TrimSpace(req.Username) == "" || strings.TrimSpace(req.SecurityBlob) == "" {
		g.appendSetupLocked(record, req, "bad_credentials")
		return Session{}, g.rejectLocked(ErrBadCredentials)
	}

	if record.info.State != SessionActive &&
		limitReached(g.activeCountLocked(), g.cfg.MaxSessions) {
		g.appendSetupLocked(record, req, "session_limit")
		return Session{}, g.rejectLocked(ErrSessionLimit)
	}

	record.info.Seq++
	record.info.Username = req.Username
	record.info.State = SessionActive
	digest := g.appendSetupLocked(record, req, "")
	record.info.PreauthHash = digest
	return record.info, nil
}

// TreeConnect authorizes one share path on an active session.
func (g *Smb2SessionGuard) TreeConnect(sessionID, path string) (Tree, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.sessions[sessionID]
	if !ok {
		return Tree{}, g.rejectLocked(ErrUnknownSession)
	}
	switch record.info.State {
	case SessionClosed:
		return Tree{}, g.rejectLocked(ErrSessionClosed)
	case SessionPending:
		return Tree{}, g.rejectLocked(ErrSessionNotActive)
	}

	normalized, share, err := normalizeTreePath(path)
	if err != nil {
		return Tree{}, g.rejectLocked(ErrBadPath)
	}
	if _, allowed := g.allowedShares[share]; !allowed {
		return Tree{}, g.rejectLocked(ErrBadPath)
	}
	if existing, ok := record.treeByPath[normalized]; ok {
		return existing, nil
	}
	if limitReached(len(record.trees), g.cfg.MaxTreesPerSession) {
		return Tree{}, g.rejectLocked(ErrTreeLimit)
	}

	tree := Tree{
		ID:        g.newTreeIDLocked(),
		SessionID: sessionID,
		Share:     share,
		Path:      normalized,
	}
	record.trees = append(record.trees, tree)
	record.treeByPath[normalized] = tree
	record.info.Trees = len(record.trees)
	return tree, nil
}

// VerifySigned checks the signature of one message against its session.
func (g *Smb2SessionGuard) VerifySigned(msg Smb2Message) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.sessions[msg.SessionID]
	if !ok {
		return g.rejectLocked(ErrUnknownSession)
	}
	if record.info.State == SessionClosed {
		return g.rejectLocked(ErrSessionClosed)
	}

	if record.info.SigningRequired && !msg.Signed {
		g.closeRecordLocked(record)
		return g.rejectLocked(ErrSignatureFailed)
	}
	if msg.Signed && !signatureMatches(record.info.PreauthHash, msg.MessageID, msg.Payload, msg.Signature) {
		return g.rejectLocked(ErrSignatureFailed)
	}
	return nil
}

// Sign produces the signature expected for one message of a session.
func (g *Smb2SessionGuard) Sign(sessionID string, messageID uint64, payload string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.sessions[sessionID]
	if !ok {
		return "", g.rejectLocked(ErrUnknownSession)
	}
	if record.info.State == SessionClosed {
		return "", g.rejectLocked(ErrSessionClosed)
	}
	return expectedSignature(record.info.PreauthHash, messageID, payload), nil
}

// CloseSession releases a session and all of its tree connects.
func (g *Smb2SessionGuard) CloseSession(sessionID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.sessions[sessionID]
	if !ok {
		return g.rejectLocked(ErrUnknownSession)
	}
	if record.info.State == SessionClosed {
		return g.rejectLocked(ErrSessionClosed)
	}
	g.closeRecordLocked(record)
	return nil
}

// Session reads one session record.
func (g *Smb2SessionGuard) Session(sessionID string) (Session, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	record, ok := g.sessions[sessionID]
	if !ok {
		return Session{}, false
	}
	return record.info, true
}

// Trees lists the tree connects of one session ordered by path.
func (g *Smb2SessionGuard) Trees(sessionID string) []Tree {
	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.sessions[sessionID]
	if !ok || len(record.trees) == 0 {
		return nil
	}
	out := make([]Tree, len(record.trees))
	copy(out, record.trees)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].ID < out[j].ID
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// PreauthDigest returns the current preauth integrity hash of a session.
func (g *Smb2SessionGuard) PreauthDigest(sessionID string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	record, ok := g.sessions[sessionID]
	if !ok {
		return "", g.rejectLocked(ErrUnknownSession)
	}
	if record.info.State == SessionClosed {
		return "", g.rejectLocked(ErrSessionClosed)
	}
	return record.info.PreauthHash, nil
}

// Stats reports the current counters.
func (g *Smb2SessionGuard) Stats() Smb2Stats {
	g.mu.Lock()
	defer g.mu.Unlock()

	stats := Smb2Stats{Rejected: g.rejected, Chained: g.chained}
	for _, record := range g.sessions {
		switch record.info.State {
		case SessionPending:
			stats.Pending++
		case SessionActive:
			stats.Sessions++
		}
		stats.Trees += len(record.trees)
	}
	return stats
}

// Snapshot serialises sessions, trees and preauth chains.
func (g *Smb2SessionGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ids := make([]string, 0, len(g.sessions))
	for id := range g.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	sessions := make([]snapshotSession, 0, len(ids))
	for _, id := range ids {
		record := g.sessions[id]
		trees := make([]Tree, len(record.trees))
		copy(trees, record.trees)
		sort.Slice(trees, func(i, j int) bool {
			if trees[i].Path == trees[j].Path {
				return trees[i].ID < trees[j].ID
			}
			return trees[i].Path < trees[j].Path
		})
		chain := append([]string(nil), record.chain...)
		sessions = append(sessions, snapshotSession{
			ID:                 record.info.ID,
			Username:           record.info.Username,
			State:              record.info.State,
			SigningRequired:    record.info.SigningRequired,
			PreauthHash:        record.info.PreauthHash,
			Seq:                record.info.Seq,
			Created:            record.info.Created,
			LastSetupMessageID: record.lastSetupID,
			HasSetup:           record.hasSetup,
			Chain:              chain,
			Trees:              trees,
		})
	}

	payload := snapshotPayload{
		NextSessionID: g.nextSessionID,
		NextTreeID:    g.nextTreeID,
		Rejected:      g.rejected,
		Chained:       g.chained,
		Sessions:      sessions,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(payloadBytes)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(checksum[:]),
		Payload:  payloadBytes,
	}
	return json.Marshal(envelope)
}

// Restore replaces the state with a snapshot, all or nothing.
func (g *Smb2SessionGuard) Restore(data []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil ||
		envelope.Magic != snapshotMagic ||
		envelope.Version != snapshotVersion ||
		len(envelope.Payload) == 0 {
		g.rejected++
		return ErrBadSnapshot
	}
	checksum := sha256.Sum256(envelope.Payload)
	expected := hex.EncodeToString(checksum[:])
	if subtle.ConstantTimeCompare([]byte(envelope.Checksum), []byte(expected)) != 1 {
		g.rejected++
		return ErrBadSnapshot
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Payload, &fields); err != nil {
		g.rejected++
		return ErrBadSnapshot
	}
	required := []string{"next_session_id", "next_tree_id", "rejected", "chained", "sessions"}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			g.rejected++
			return ErrBadSnapshot
		}
	}
	for key := range fields {
		switch key {
		case "next_session_id", "next_tree_id", "rejected", "chained", "sessions":
		default:
			g.rejected++
			return ErrBadSnapshot
		}
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil ||
		payload.Sessions == nil ||
		payload.Rejected < 0 ||
		payload.Chained < 0 {
		g.rejected++
		return ErrBadSnapshot
	}

	restored := make(map[string]*sessionRecord, len(payload.Sessions))
	seenTrees := make(map[string]struct{})
	chainEntries := 0
	for _, persisted := range payload.Sessions {
		if persisted.ID == "" {
			g.rejected++
			return ErrBadSnapshot
		}
		if _, exists := restored[persisted.ID]; exists {
			g.rejected++
			return ErrBadSnapshot
		}
		switch persisted.State {
		case SessionPending, SessionActive, SessionClosed:
		default:
			g.rejected++
			return ErrBadSnapshot
		}
		if persisted.Seq < 0 || (persisted.State == SessionActive && persisted.Seq == 0) {
			g.rejected++
			return ErrBadSnapshot
		}
		if persisted.State == SessionActive && strings.TrimSpace(persisted.Username) == "" {
			g.rejected++
			return ErrBadSnapshot
		}
		if len(persisted.Chain) == 0 || !validDigest(persisted.PreauthHash) ||
			persisted.Chain[len(persisted.Chain)-1] != persisted.PreauthHash {
			g.rejected++
			return ErrBadSnapshot
		}
		for _, digest := range persisted.Chain {
			if !validDigest(digest) {
				g.rejected++
				return ErrBadSnapshot
			}
		}
		chainEntries += len(persisted.Chain)

		if persisted.Trees == nil ||
			(persisted.State != SessionActive && len(persisted.Trees) != 0) {
			g.rejected++
			return ErrBadSnapshot
		}

		record := &sessionRecord{
			info: Session{
				ID:              persisted.ID,
				Username:        persisted.Username,
				State:           persisted.State,
				SigningRequired: persisted.SigningRequired,
				PreauthHash:     persisted.PreauthHash,
				Seq:             persisted.Seq,
				Created:         persisted.Created,
			},
			chain:       append([]string(nil), persisted.Chain...),
			trees:       make([]Tree, 0, len(persisted.Trees)),
			treeByPath:  make(map[string]Tree, len(persisted.Trees)),
			lastSetupID: persisted.LastSetupMessageID,
			hasSetup:    persisted.HasSetup,
		}
		for _, tree := range persisted.Trees {
			if tree.ID == "" || tree.SessionID != persisted.ID || tree.Path == "" {
				g.rejected++
				return ErrBadSnapshot
			}
			if _, exists := seenTrees[tree.ID]; exists {
				g.rejected++
				return ErrBadSnapshot
			}
			normalized, share, err := normalizeTreePath(tree.Path)
			if err != nil || normalized != tree.Path || share != tree.Share {
				g.rejected++
				return ErrBadSnapshot
			}
			if _, exists := record.treeByPath[tree.Path]; exists {
				g.rejected++
				return ErrBadSnapshot
			}
			seenTrees[tree.ID] = struct{}{}
			record.trees = append(record.trees, tree)
			record.treeByPath[tree.Path] = tree
		}
		record.info.Trees = len(record.trees)
		restored[persisted.ID] = record
	}
	if payload.Chained != chainEntries {
		g.rejected++
		return ErrBadSnapshot
	}

	g.sessions = restored
	g.nextSessionID = payload.NextSessionID
	g.nextTreeID = payload.NextTreeID
	g.rejected = payload.Rejected
	g.chained = payload.Chained
	return nil
}

func (g *Smb2SessionGuard) rejectLocked(err error) error {
	g.rejected++
	return err
}

func (g *Smb2SessionGuard) closeRecordLocked(record *sessionRecord) {
	record.info.State = SessionClosed
	record.info.Trees = 0
	record.trees = nil
	record.treeByPath = make(map[string]Tree)
}

func (g *Smb2SessionGuard) pendingCountLocked() int {
	count := 0
	for _, record := range g.sessions {
		if record.info.State == SessionPending {
			count++
		}
	}
	return count
}

func (g *Smb2SessionGuard) activeCountLocked() int {
	count := 0
	for _, record := range g.sessions {
		if record.info.State == SessionActive {
			count++
		}
	}
	return count
}

func (g *Smb2SessionGuard) newSessionIDLocked() string {
	for {
		g.nextSessionID++
		id := fmt.Sprintf("session-%d", g.nextSessionID)
		if _, exists := g.sessions[id]; !exists {
			return id
		}
	}
}

func (g *Smb2SessionGuard) newTreeIDLocked() string {
	for {
		g.nextTreeID++
		id := fmt.Sprintf("tree-%d", g.nextTreeID)
		if !g.treeIDExistsLocked(id) {
			return id
		}
	}
}

func (g *Smb2SessionGuard) treeIDExistsLocked(id string) bool {
	for _, record := range g.sessions {
		for _, tree := range record.trees {
			if tree.ID == id {
				return true
			}
		}
	}
	return false
}

func (g *Smb2SessionGuard) appendSetupLocked(record *sessionRecord, req SessionSetupRequest, errorCode string) string {
	response := setupChainResponse{
		State:    record.info.State,
		Username: record.info.Username,
		Seq:      record.info.Seq,
		Error:    errorCode,
	}
	digest := makeChainDigest(record.info.PreauthHash, "SESSION_SETUP",
		setupChainRequest{
			MessageID:    req.MessageID,
			Username:     req.Username,
			SecurityBlob: req.SecurityBlob,
			Signed:       req.Signed,
			Signature:    req.Signature,
		},
		response,
	)
	record.info.PreauthHash = digest
	record.chain = append(record.chain, digest)
	record.lastSetupID = req.MessageID
	record.hasSetup = true
	g.chained++
	return digest
}

func makeChainDigest(previous, domain string, request, response any) string {
	requestBytes, _ := json.Marshal(request)
	responseBytes, _ := json.Marshal(response)
	h := sha256.New()
	h.Write([]byte(previous))
	h.Write([]byte{0})
	h.Write([]byte(domain))
	h.Write([]byte{0})
	h.Write(requestBytes)
	h.Write([]byte{0})
	h.Write(responseBytes)
	return hex.EncodeToString(h.Sum(nil))
}

func expectedSignature(preauthHash string, messageID uint64, payload string) string {
	mac := hmac.New(sha256.New, []byte(preauthHash))
	mac.Write([]byte("SMB2_MESSAGE_SIGNATURE"))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatUint(messageID, 10)))
	mac.Write([]byte{0})
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func signatureMatches(preauthHash string, messageID uint64, payload, supplied string) bool {
	expected := expectedSignature(preauthHash, messageID, payload)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func limitReached(count, limit int) bool {
	if limit <= 0 {
		return true
	}
	return count >= limit
}

func highestCommonDialect(server, client []uint16) (uint16, bool) {
	serverSet := make(map[uint16]struct{}, len(server))
	for _, dialect := range server {
		serverSet[dialect] = struct{}{}
	}
	var best uint16
	found := false
	for _, dialect := range client {
		if _, ok := serverSet[dialect]; !ok {
			continue
		}
		if !found || dialect > best {
			best = dialect
			found = true
		}
	}
	return best, found
}

func dialectSupportsSigning(dialect uint16) bool {
	switch dialect {
	case Dialect210, Dialect300, Dialect302, Dialect311:
		return true
	default:
		return false
	}
}

func normalizeTreePath(raw string) (string, string, error) {
	path := strings.ReplaceAll(raw, "\\", "/")
	if strings.ContainsRune(path, ':') {
		return "", "", ErrBadPath
	}

	leading := 0
	for leading < len(path) && path[leading] == '/' {
		leading++
	}
	rest := path[leading:]
	segments := make([]string, 0, strings.Count(rest, "/")+1)
	for _, segment := range strings.Split(rest, "/") {
		if segment == "" {
			continue
		}
		if segment == ".." || reservedName(segment) {
			return "", "", ErrBadPath
		}
		segments = append(segments, segment)
	}
	if len(segments) < 2 {
		return "", "", ErrBadPath
	}

	prefix := ""
	if leading >= 2 {
		prefix = "//"
	} else if leading == 1 {
		prefix = "/"
	}
	normalized := prefix + strings.Join(segments, "/")
	return normalized, segments[1], nil
}

func reservedName(segment string) bool {
	name := strings.ToUpper(strings.TrimSpace(segment))
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		name = name[:dot]
	}
	switch name {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$":
		return true
	}
	if len(name) == 4 && strings.HasPrefix(name, "COM") && name[3] >= '1' && name[3] <= '9' {
		return true
	}
	if len(name) == 4 && strings.HasPrefix(name, "LPT") && name[3] >= '1' && name[3] <= '9' {
		return true
	}
	return false
}
