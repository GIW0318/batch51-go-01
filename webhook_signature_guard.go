// Package task verifies inbound webhook calls against a rotating key set, a
// fixed timestamp window and a bounded replay cache. Every decision, accepted
// or refused, is appended to an audit trail before the caller sees the verdict.
package task

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reason codes attached to a verdict and to an audit record.
const (
	ReasonSignatureOK       = "reason_signature_ok"
	ReasonTimestampSkew     = "reason_timestamp_skew"
	ReasonSignatureMismatch = "reason_signature_mismatch"
	ReasonReplay            = "reason_replay"
	ReasonUnknownVersion    = "reason_unknown_version"
	ReasonKeyRetired        = "reason_key_retired"
	ReasonMalformedRequest  = "reason_malformed_request"
	ReasonReplayCapacity    = "reason_replay_capacity"
)

// Lifecycle states of a registered key version.
const (
	KeyActive   = "active"
	KeyRotating = "rotating"
	KeyRetired  = "retired"
)

// Errors reported by the guard.
var (
	ErrBadConfig      = errors.New("window, audit cap and replay cap must all be positive")
	ErrBlankKey       = errors.New("key version and secret are required")
	ErrKeyConflict    = errors.New("key version already bound to a different secret")
	ErrUnknownKey     = errors.New("unknown key version")
	ErrKeyRetired     = errors.New("key version is retired")
	ErrStaleTimestamp = errors.New("timestamp outside the accepted window")
	ErrBadSignature   = errors.New("signature does not match the raw body")
	ErrReplay         = errors.New("signature was already accepted inside the window")
	ErrReplayCapacity = errors.New("replay cache is full of live entries")
	ErrBadRequest     = errors.New("request is missing version, timestamp or signature")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// GuardConfig is the immutable setup of one guard instance. Clock may be nil,
// in which case the wall clock in Unix seconds is used.
type GuardConfig struct {
	WindowSeconds int64
	MaxAudit      int
	MaxReplay     int
	Clock         func() int64
}

// SignedRequest is one inbound call exactly as the transport delivered it.
// Body carries the original bytes; nothing here is re-encoded.
type SignedRequest struct {
	Version   string
	Timestamp int64
	Signature string
	Body      []byte
}

// Verdict is the explainable outcome handed back to the caller.
type Verdict struct {
	Accepted bool
	Version  string
	Reason   string
	Seq      int64
}

// AuditRecord is one line of the decision trail.
type AuditRecord struct {
	Seq       int64
	Version   string
	Timestamp int64
	Reason    string
	Signature string
	Accepted  bool
}

// GuardStats counts the traffic seen by the guard.
type GuardStats struct {
	Accepted int
	Rejected int
	Replayed int
	Keys     int
}

// keyEntry is the stored material and lifecycle state of one version.
type keyEntry struct {
	version   string
	secret    []byte
	state     string
	rotatedAt int64
}

// replayEntry remembers one accepted signature until it can no longer be
// replayed inside the window.
type replayEntry struct {
	version   string
	expiresAt int64
}

// WebhookSignatureGuard owns the key table, the replay cache and the audit log.
type WebhookSignatureGuard struct {
	mu     sync.RWMutex
	cfg    GuardConfig
	keys   map[string]*keyEntry
	order  []string
	replay map[string]replayEntry
	audit  []AuditRecord
	stats  GuardStats
	seq    int64
}

const (
	snapshotFormat = "gsb.webhook-signature-guard"
	snapshotSchema = 1
)

type snapshotKey struct {
	Version   string `json:"version"`
	Secret    []byte `json:"secret"`
	State     string `json:"state"`
	RotatedAt int64  `json:"rotated_at"`
}

type snapshotReplay struct {
	Signature string `json:"signature"`
	Version   string `json:"version"`
	ExpiresAt int64  `json:"expires_at"`
}

type snapshotPayload struct {
	Keys   []snapshotKey    `json:"keys"`
	Replay []snapshotReplay `json:"replay"`
	Seq    int64            `json:"seq"`
	Audit  []AuditRecord    `json:"audit"`
	Stats  GuardStats       `json:"stats"`
}

type snapshotEnvelope struct {
	Format  string          `json:"format"`
	Version int             `json:"version"`
	Digest  string          `json:"digest"`
	Payload json.RawMessage `json:"payload"`
}

// NewWebhookSignatureGuard validates the caps and builds an empty guard.
func NewWebhookSignatureGuard(cfg GuardConfig) (*WebhookSignatureGuard, error) {
	if cfg.WindowSeconds <= 0 || cfg.MaxAudit <= 0 || cfg.MaxReplay <= 0 {
		return nil, ErrBadConfig
	}
	return &WebhookSignatureGuard{
		cfg:    cfg,
		keys:   make(map[string]*keyEntry),
		replay: make(map[string]replayEntry),
	}, nil
}

// AddKey registers a secret under a version label, idempotent for equal input.
func (g *WebhookSignatureGuard) AddKey(version string, secret []byte) error {
	version = strings.TrimSpace(version)
	if version == "" || len(secret) == 0 {
		return ErrBlankKey
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if existing, ok := g.keys[version]; ok {
		if hmac.Equal(existing.secret, secret) {
			return nil
		}
		return ErrKeyConflict
	}

	for _, existing := range g.keys {
		if existing.state == KeyActive {
			existing.state = KeyRotating
		}
	}

	g.keys[version] = &keyEntry{
		version: version,
		secret:  append([]byte(nil), secret...),
		state:   KeyActive,
	}
	g.order = append(g.order, version)
	return nil
}

// RetireKey takes a version out of service without deleting its audit trail.
func (g *WebhookSignatureGuard) RetireKey(version string) error {
	version = strings.TrimSpace(version)

	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.keys[version]
	if !ok {
		return ErrUnknownKey
	}
	if entry.state != KeyRetired {
		entry.state = KeyRetired
		entry.rotatedAt = g.nowLocked()
	}
	return nil
}

// Sign computes the wire signature of one payload for one key version.
func (g *WebhookSignatureGuard) Sign(version string, timestamp int64, body []byte) (string, error) {
	version = strings.TrimSpace(version)

	g.mu.RLock()
	defer g.mu.RUnlock()

	entry, ok := g.keys[version]
	if !ok {
		return "", ErrUnknownKey
	}
	if entry.state == KeyRetired {
		return "", ErrKeyRetired
	}
	return signHex(entry.secret, version, timestamp, body), nil
}

// Verify grades one inbound request and records the decision.
func (g *WebhookSignatureGuard) Verify(req SignedRequest) (Verdict, error) {
	version := strings.TrimSpace(req.Version)
	signature := strings.TrimSpace(req.Signature)

	g.mu.Lock()
	defer g.mu.Unlock()

	// The comparison, replay registration and audit write all happen while the
	// same exclusive lock is held so a delivery can only be accepted once.
	if version == "" || signature == "" {
		return g.rejectLocked(version, req.Timestamp, signature, ReasonMalformedRequest, ErrBadRequest)
	}

	entry, ok := g.keys[version]
	if !ok {
		return g.rejectLocked(version, req.Timestamp, signature, ReasonUnknownVersion, ErrUnknownKey)
	}
	if entry.state == KeyRetired {
		return g.rejectLocked(version, req.Timestamp, signature, ReasonKeyRetired, ErrKeyRetired)
	}

	now := g.nowLocked()
	if outsideWindow(req.Timestamp, now, g.cfg.WindowSeconds) {
		return g.rejectLocked(version, req.Timestamp, signature, ReasonTimestampSkew, ErrStaleTimestamp)
	}

	provided, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(provided, signBytes(entry.secret, version, req.Timestamp, req.Body)) {
		return g.rejectLocked(version, req.Timestamp, signature, ReasonSignatureMismatch, ErrBadSignature)
	}

	// The replay key is the signature itself, normalised to lower-case hex so
	// case variants of the same wire signature cannot obtain a second slot.
	replayKey := hex.EncodeToString(provided)
	if previous, ok := g.replay[replayKey]; ok && now <= previous.expiresAt {
		return g.rejectLocked(version, req.Timestamp, signature, ReasonReplay, ErrReplay)
	}

	g.purgeExpiredLocked(now)
	if len(g.replay) >= g.cfg.MaxReplay {
		return g.rejectLocked(version, req.Timestamp, signature, ReasonReplayCapacity, ErrReplayCapacity)
	}

	g.replay[replayKey] = replayEntry{
		version:   version,
		expiresAt: replayExpiry(req.Timestamp, g.cfg.WindowSeconds),
	}
	return g.acceptLocked(version, req.Timestamp, signature)
}

// PurgeExpired drops replay entries that can no longer be replayed and reports
// how many went away.
func (g *WebhookSignatureGuard) PurgeExpired(now int64) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.purgeExpiredLocked(now)
}

// ReplayEntries reports how many live replay records are held.
func (g *WebhookSignatureGuard) ReplayEntries() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.replay)
}

// KeyCount reports how many versions are registered, retired ones included.
func (g *WebhookSignatureGuard) KeyCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.keys)
}

// CurrentVersion reports the newest version that is not retired.
func (g *WebhookSignatureGuard) CurrentVersion() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	for i := len(g.order) - 1; i >= 0; i-- {
		entry, ok := g.keys[g.order[i]]
		if ok && entry.state != KeyRetired {
			return entry.version
		}
	}
	return ""
}

// Audit returns a copy of the decision trail, oldest first.
func (g *WebhookSignatureGuard) Audit() []AuditRecord {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]AuditRecord(nil), g.audit...)
}

// Stats returns the running counters.
func (g *WebhookSignatureGuard) Stats() GuardStats {
	g.mu.RLock()
	defer g.mu.RUnlock()

	stats := g.stats
	stats.Keys = len(g.keys)
	return stats
}

// Snapshot serialises keys, replay cache, sequence, audit and counters.
func (g *WebhookSignatureGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	payload := snapshotPayload{
		Keys:  make([]snapshotKey, 0, len(g.keys)),
		Seq:   g.seq,
		Audit: append([]AuditRecord(nil), g.audit...),
		Stats: g.stats,
	}
	payload.Stats.Keys = len(g.keys)

	seen := make(map[string]struct{}, len(g.keys))
	for _, version := range g.order {
		entry, ok := g.keys[version]
		if !ok {
			continue
		}
		payload.Keys = append(payload.Keys, snapshotKey{
			Version:   entry.version,
			Secret:    append([]byte(nil), entry.secret...),
			State:     entry.state,
			RotatedAt: entry.rotatedAt,
		})
		seen[version] = struct{}{}
	}

	// Keep snapshot output complete and deterministic even if an instance was
	// built by an older restore path that did not retain registration order.
	var extras []string
	for version := range g.keys {
		if _, ok := seen[version]; !ok {
			extras = append(extras, version)
		}
	}
	sort.Strings(extras)
	for _, version := range extras {
		entry := g.keys[version]
		payload.Keys = append(payload.Keys, snapshotKey{
			Version:   entry.version,
			Secret:    append([]byte(nil), entry.secret...),
			State:     entry.state,
			RotatedAt: entry.rotatedAt,
		})
	}

	replayKeys := make([]string, 0, len(g.replay))
	for signature := range g.replay {
		replayKeys = append(replayKeys, signature)
	}
	sort.Strings(replayKeys)
	payload.Replay = make([]snapshotReplay, 0, len(replayKeys))
	for _, signature := range replayKeys {
		entry := g.replay[signature]
		payload.Replay = append(payload.Replay, snapshotReplay{
			Signature: signature,
			Version:   entry.version,
			ExpiresAt: entry.expiresAt,
		})
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return json.Marshal(snapshotEnvelope{
		Format:  snapshotFormat,
		Version: snapshotSchema,
		Digest:  hex.EncodeToString(digest[:]),
		Payload: raw,
	})
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *WebhookSignatureGuard) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Format != snapshotFormat || envelope.Version != snapshotSchema {
		return ErrBadSnapshot
	}

	digest := sha256.Sum256(envelope.Payload)
	if !hmac.Equal([]byte(hex.EncodeToString(digest[:])), []byte(envelope.Digest)) {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Seq < 0 || invalidStats(payload.Stats) {
		return ErrBadSnapshot
	}

	keys := make(map[string]*keyEntry, len(payload.Keys))
	order := make([]string, 0, len(payload.Keys))
	for _, saved := range payload.Keys {
		version := strings.TrimSpace(saved.Version)
		if version == "" || version != saved.Version || len(saved.Secret) == 0 {
			return ErrBadSnapshot
		}
		if saved.State != KeyActive && saved.State != KeyRotating && saved.State != KeyRetired {
			return ErrBadSnapshot
		}
		if _, duplicate := keys[version]; duplicate {
			return ErrBadSnapshot
		}
		keys[version] = &keyEntry{
			version:   version,
			secret:    append([]byte(nil), saved.Secret...),
			state:     saved.State,
			rotatedAt: saved.RotatedAt,
		}
		order = append(order, version)
	}
	if payload.Stats.Keys != len(keys) {
		return ErrBadSnapshot
	}

	audit := append([]AuditRecord(nil), payload.Audit...)
	var lastSeq int64
	for _, record := range audit {
		if record.Seq <= lastSeq || record.Seq > payload.Seq {
			return ErrBadSnapshot
		}
		lastSeq = record.Seq
	}
	if len(audit) > g.cfg.MaxAudit {
		audit = append([]AuditRecord(nil), audit[len(audit)-g.cfg.MaxAudit:]...)
	}

	if len(payload.Replay) > g.cfg.MaxReplay {
		return ErrBadSnapshot
	}
	replay := make(map[string]replayEntry, len(payload.Replay))
	for _, saved := range payload.Replay {
		rawSignature, err := hex.DecodeString(saved.Signature)
		if err != nil || len(rawSignature) != sha256.Size {
			return ErrBadSnapshot
		}
		if _, ok := keys[saved.Version]; !ok {
			return ErrBadSnapshot
		}
		signature := hex.EncodeToString(rawSignature)
		if _, duplicate := replay[signature]; duplicate {
			return ErrBadSnapshot
		}
		replay[signature] = replayEntry{
			version:   saved.Version,
			expiresAt: saved.ExpiresAt,
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.keys = keys
	g.order = order
	g.replay = replay
	g.audit = audit
	g.stats = payload.Stats
	g.stats.Keys = len(keys)
	g.seq = payload.Seq
	return nil
}

func (g *WebhookSignatureGuard) nowLocked() int64 {
	if g.cfg.Clock != nil {
		return g.cfg.Clock()
	}
	return time.Now().Unix()
}

func (g *WebhookSignatureGuard) purgeExpiredLocked(now int64) int {
	removed := 0
	for signature, entry := range g.replay {
		if entry.expiresAt <= now {
			delete(g.replay, signature)
			removed++
		}
	}
	return removed
}

func (g *WebhookSignatureGuard) acceptLocked(version string, timestamp int64, signature string) (Verdict, error) {
	g.seq++
	g.stats.Accepted++
	g.appendAuditLocked(AuditRecord{
		Seq:       g.seq,
		Version:   version,
		Timestamp: timestamp,
		Reason:    ReasonSignatureOK,
		Signature: signature,
		Accepted:  true,
	})
	return Verdict{Accepted: true, Version: version, Reason: ReasonSignatureOK, Seq: g.seq}, nil
}

func (g *WebhookSignatureGuard) rejectLocked(version string, timestamp int64, signature, reason string, err error) (Verdict, error) {
	g.seq++
	g.stats.Rejected++
	if errors.Is(err, ErrReplay) {
		g.stats.Replayed++
	}
	g.appendAuditLocked(AuditRecord{
		Seq:       g.seq,
		Version:   version,
		Timestamp: timestamp,
		Reason:    reason,
		Signature: signature,
		Accepted:  false,
	})
	return Verdict{Accepted: false, Version: version, Reason: reason, Seq: g.seq}, err
}

func (g *WebhookSignatureGuard) appendAuditLocked(record AuditRecord) {
	g.audit = append(g.audit, record)
	if len(g.audit) > g.cfg.MaxAudit {
		g.audit = append([]AuditRecord(nil), g.audit[len(g.audit)-g.cfg.MaxAudit:]...)
	}
}

func signHex(secret []byte, version string, timestamp int64, body []byte) string {
	return hex.EncodeToString(signBytes(secret, version, timestamp, body))
}

func signBytes(secret []byte, version string, timestamp int64, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(version))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

func outsideWindow(timestamp, now, window int64) bool {
	var difference uint64
	if timestamp >= now {
		difference = uint64(timestamp) - uint64(now)
	} else {
		difference = uint64(now) - uint64(timestamp)
	}
	return difference > uint64(window)
}

func replayExpiry(timestamp, window int64) int64 {
	if timestamp > math.MaxInt64-window {
		return math.MaxInt64
	}
	return timestamp + window
}

func invalidStats(stats GuardStats) bool {
	return stats.Accepted < 0 || stats.Rejected < 0 || stats.Replayed < 0 || stats.Keys < 0
}
