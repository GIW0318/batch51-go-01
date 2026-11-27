// Package task admits inbound mutual TLS peers by rebuilding the client
// certificate chain from the leaf up to a configured trust anchor, checking
// the validity window and key usage of every certificate on the path, and
// cross checking the exact leaf serial against a revocation table before the
// pinned fingerprint of the leaf is compared with the one presented on the
// wire. Every verdict, allowed or refused, is written to a bounded audit trail
// together with the sequence number it was assigned.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Verdict codes carried by a Decision.
const (
	VerdictAllow = "mtc_allow"
	VerdictDeny  = "mtc_deny"
)

// Reason codes explain a Decision and are attached to the audit trail.
const (
	ReasonTrusted            = "mtc_trusted"
	ReasonBrokenLink         = "mtc_broken_link"
	ReasonUnknownAnchor      = "mtc_unknown_anchor"
	ReasonCertificateExpired = "mtc_certificate_expired"
	ReasonIssuerNotCA        = "mtc_issuer_not_ca"
	ReasonLeafKeyUsage       = "mtc_leaf_key_usage"
	ReasonRevoked            = "mtc_revoked"
	ReasonRevocationDown     = "mtc_revocation_unavailable"
	ReasonPinMismatch        = "mtc_pin_mismatch"
	ReasonChainDepth         = "mtc_chain_depth"
	ReasonMalformed          = "mtc_malformed"
)

// Errors returned by the guard.
var (
	ErrBadCertificate        = errors.New("certificate record is incomplete")
	ErrBadRevocation         = errors.New("revocation record is incomplete")
	ErrChainIncomplete       = errors.New("chain is missing an intermediate certificate")
	ErrChainOrder            = errors.New("chain is not ordered leaf first")
	ErrChainDepth            = errors.New("chain exceeds the configured depth")
	ErrCertificateExpired    = errors.New("certificate is outside its validity window")
	ErrNotCA                 = errors.New("issuer certificate is not a CA")
	ErrKeyUsage              = errors.New("leaf certificate lacks client auth usage")
	ErrUnknownAnchor         = errors.New("chain does not reach a trust anchor")
	ErrRevocationUnavailable = errors.New("revocation source is unavailable")
	ErrPinMismatch           = errors.New("presented fingerprint does not match the pinned leaf")
	ErrPinLimit              = errors.New("pinned fingerprint table is full")
	ErrBadPinWindow          = errors.New("pin validity window is invalid")
	ErrBadSnapshot           = errors.New("snapshot payload is invalid")
)

// CertRecord is one parsed certificate. SignedBy carries the fingerprint of
// the issuing certificate, so a chain is linked when the SignedBy value of a
// certificate equals the Fingerprint of the next one.
type CertRecord struct {
	Serial      string   `json:"serial"`
	Subject     string   `json:"subject"`
	Issuer      string   `json:"issuer"`
	Fingerprint string   `json:"fingerprint"`
	SignedBy    string   `json:"signed_by"`
	IsCA        bool     `json:"is_ca"`
	NotBefore   int64    `json:"not_before"`
	NotAfter    int64    `json:"not_after"`
	KeyUsage    []string `json:"key_usage"`
}

// RevocationRecord is one entry of the revocation table. Entries are keyed by
// the pair (issuer, serial); the subject name is only carried for reporting.
type RevocationRecord struct {
	Serial    string `json:"serial"`
	Issuer    string `json:"issuer"`
	Subject   string `json:"subject"`
	RevokedAt int64  `json:"revoked_at"`
	Reason    string `json:"reason"`
}

// PinEntry keeps one leaf fingerprint with the window it stays on record.
type PinEntry struct {
	Fingerprint string `json:"fingerprint"`
	NotBefore   int64  `json:"not_before"`
	NotAfter    int64  `json:"not_after"`
}

// Decision is the admission verdict for one presented chain.
type Decision struct {
	Allowed bool     `json:"allowed"`
	Code    string   `json:"code"`
	Reason  string   `json:"reason"`
	Leaf    string   `json:"leaf"`
	Depth   int      `json:"depth"`
	Chain   []string `json:"chain"`
	Seq     int64    `json:"seq"`
}

// AuditRecord is one line of the admission trail.
type AuditRecord struct {
	Seq    int64  `json:"seq"`
	Event  string `json:"event"`
	Serial string `json:"serial"`
	Issuer string `json:"issuer"`
	Depth  int    `json:"depth"`
	Code   string `json:"code"`
	At     int64  `json:"at"`
}

// GuardConfig bounds the guard tables.
type GuardConfig struct {
	MaxChainDepth int
	MaxAudit      int
	MaxPins       int
}

type revocationKey struct {
	issuer string
	serial string
}

// MtlsChainRevocationGuard owns the trust anchors, the revocation table, the
// pinned leaf fingerprints and the admission audit trail.
type MtlsChainRevocationGuard struct {
	mu              sync.RWMutex
	cfg             GuardConfig
	anchors         map[string]CertRecord
	revocations     map[revocationKey]RevocationRecord
	pins            map[string]PinEntry
	audit           []AuditRecord
	seq             int64
	revocationReady bool
}

// NewMtlsChainRevocationGuard builds a guard with the given bounds.
func NewMtlsChainRevocationGuard(cfg GuardConfig) *MtlsChainRevocationGuard {
	return &MtlsChainRevocationGuard{
		cfg:             cfg,
		anchors:         make(map[string]CertRecord),
		revocations:     make(map[revocationKey]RevocationRecord),
		pins:            make(map[string]PinEntry),
		revocationReady: true,
	}
}

// Trust registers one trust anchor. Registering the same fingerprint twice is
// idempotent and keeps the existing entry.
func (g *MtlsChainRevocationGuard) Trust(anchor CertRecord) error {
	if !completeCert(anchor) {
		return ErrBadCertificate
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.anchors == nil {
		g.anchors = make(map[string]CertRecord)
	}
	if _, exists := g.anchors[anchor.Fingerprint]; exists {
		return nil
	}
	g.anchors[anchor.Fingerprint] = copyCert(anchor)
	return nil
}

// TrustedAnchors lists the registered anchor fingerprints in ascending order.
func (g *MtlsChainRevocationGuard) TrustedAnchors() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	anchors := make([]string, 0, len(g.anchors))
	for fingerprint := range g.anchors {
		anchors = append(anchors, fingerprint)
	}
	sort.Strings(anchors)
	return anchors
}

// AddRevocation appends one entry to the revocation table.
func (g *MtlsChainRevocationGuard) AddRevocation(rec RevocationRecord) error {
	if strings.TrimSpace(rec.Serial) == "" || strings.TrimSpace(rec.Issuer) == "" {
		return ErrBadRevocation
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.revocations == nil {
		g.revocations = make(map[revocationKey]RevocationRecord)
	}
	g.revocations[revocationKey{issuer: rec.Issuer, serial: rec.Serial}] = rec
	return nil
}

// SetRevocationSource marks the revocation table as reachable or not. While it
// is unreachable every admission is refused.
func (g *MtlsChainRevocationGuard) SetRevocationSource(available bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.revocationReady = available
}

// IsRevoked reports whether the exact (issuer, serial) pair is on the
// revocation table. It fails with ErrRevocationUnavailable while the source is
// unreachable.
func (g *MtlsChainRevocationGuard) IsRevoked(serial, issuer string) (bool, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if !g.revocationReady {
		return false, ErrRevocationUnavailable
	}
	_, revoked := g.revocations[revocationKey{issuer: issuer, serial: serial}]
	return revoked, nil
}

// CheckChain rebuilds and validates the chain, returning it leaf first.
func (g *MtlsChainRevocationGuard) CheckChain(chain []CertRecord, now int64) ([]CertRecord, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.checkChainLocked(chain, now)
}

// Decide produces the admission verdict for a presented chain.
func (g *MtlsChainRevocationGuard) Decide(chain []CertRecord, now int64, peerFingerprint string) (Decision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.seq++
	decision := Decision{
		Code:  VerdictDeny,
		Depth: len(chain),
		Chain: fingerprints(chain),
		Seq:   g.seq,
	}
	if len(chain) > 0 {
		decision.Leaf = chain[0].Fingerprint
	}

	validated, err := g.checkChainLocked(chain, now)
	if err != nil {
		decision.Reason = reasonForError(err)
		g.recordDecisionLocked(decision, chain, now)
		return decision, nil
	}

	decision.Chain = fingerprints(validated)
	decision.Depth = len(validated)
	leaf := validated[0]
	decision.Leaf = leaf.Fingerprint

	if !g.revocationReady {
		decision.Reason = ReasonRevocationDown
		g.recordDecisionLocked(decision, validated, now)
		return decision, nil
	}
	revoked, revErr := g.isRevokedLocked(leaf.Serial, leaf.Issuer)
	if revErr != nil {
		decision.Reason = ReasonRevocationDown
		g.recordDecisionLocked(decision, validated, now)
		return decision, nil
	}
	if revoked {
		decision.Reason = ReasonRevoked
		g.recordDecisionLocked(decision, validated, now)
		return decision, nil
	}

	if !g.fingerprintMatchesLocked(leaf.Fingerprint, peerFingerprint, now) {
		decision.Reason = ReasonPinMismatch
		g.recordDecisionLocked(decision, validated, now)
		return decision, nil
	}

	decision.Allowed = true
	decision.Code = VerdictAllow
	decision.Reason = ReasonTrusted
	g.recordDecisionLocked(decision, validated, now)
	return decision, nil
}

// Pin records the leaf fingerprint that may be presented between startAt and
// endAt.
func (g *MtlsChainRevocationGuard) Pin(cert CertRecord, startAt, endAt int64) error {
	if !completeCert(cert) {
		return ErrBadCertificate
	}
	if startAt > endAt {
		return ErrBadPinWindow
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pins == nil {
		g.pins = make(map[string]PinEntry)
	}
	if _, exists := g.pins[cert.Fingerprint]; exists {
		return nil
	}
	if g.cfg.MaxPins <= 0 || len(g.pins) >= g.cfg.MaxPins {
		return ErrPinLimit
	}
	g.pins[cert.Fingerprint] = PinEntry{
		Fingerprint: cert.Fingerprint,
		NotBefore:   startAt,
		NotAfter:    endAt,
	}
	return nil
}

// Pins returns a copy of the pin table ordered by fingerprint.
func (g *MtlsChainRevocationGuard) Pins() []PinEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()

	pins := make([]PinEntry, 0, len(g.pins))
	for _, pin := range g.pins {
		pins = append(pins, pin)
	}
	sort.Slice(pins, func(i, j int) bool {
		return pins[i].Fingerprint < pins[j].Fingerprint
	})
	return pins
}

// Audit returns a copy of the admission trail, oldest first.
func (g *MtlsChainRevocationGuard) Audit() []AuditRecord {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]AuditRecord(nil), g.audit...)
}

// Snapshot serialises anchors, revocations, pins, the audit trail and the
// verdict counter.
func (g *MtlsChainRevocationGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	body := snapshotBody{
		Version:                   snapshotVersion,
		Anchors:                   make([]CertRecord, 0, len(g.anchors)),
		Revocations:               make([]RevocationRecord, 0, len(g.revocations)),
		Pins:                      make([]PinEntry, 0, len(g.pins)),
		Audit:                     append([]AuditRecord(nil), g.audit...),
		Seq:                       g.seq,
		RevocationSourceAvailable: g.revocationReady,
	}
	for _, anchor := range g.anchors {
		body.Anchors = append(body.Anchors, copyCert(anchor))
	}
	for _, rec := range g.revocations {
		body.Revocations = append(body.Revocations, rec)
	}
	for _, pin := range g.pins {
		body.Pins = append(body.Pins, pin)
	}
	sort.Slice(body.Anchors, func(i, j int) bool {
		return body.Anchors[i].Fingerprint < body.Anchors[j].Fingerprint
	})
	sort.Slice(body.Revocations, func(i, j int) bool {
		if body.Revocations[i].Issuer != body.Revocations[j].Issuer {
			return body.Revocations[i].Issuer < body.Revocations[j].Issuer
		}
		return body.Revocations[i].Serial < body.Revocations[j].Serial
	})
	sort.Slice(body.Pins, func(i, j int) bool {
		return body.Pins[i].Fingerprint < body.Pins[j].Fingerprint
	})

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole guard state from a snapshot, all or nothing.
func (g *MtlsChainRevocationGuard) Restore(data []byte) error {
	var envelope snapshotEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return ErrBadSnapshot
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Version != snapshotVersion || envelope.Checksum == "" || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if hex.EncodeToString(sum[:]) != envelope.Checksum {
		return ErrBadSnapshot
	}

	var body snapshotBody
	bodyDecoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	bodyDecoder.DisallowUnknownFields()
	if err := bodyDecoder.Decode(&body); err != nil {
		return ErrBadSnapshot
	}
	if err := ensureJSONEnd(bodyDecoder); err != nil || body.Version != snapshotVersion || body.Seq < 0 {
		return ErrBadSnapshot
	}

	anchors := make(map[string]CertRecord, len(body.Anchors))
	for _, anchor := range body.Anchors {
		if !completeCert(anchor) {
			return ErrBadSnapshot
		}
		if _, exists := anchors[anchor.Fingerprint]; exists {
			return ErrBadSnapshot
		}
		anchors[anchor.Fingerprint] = copyCert(anchor)
	}

	revocations := make(map[revocationKey]RevocationRecord, len(body.Revocations))
	for _, rec := range body.Revocations {
		if strings.TrimSpace(rec.Serial) == "" || strings.TrimSpace(rec.Issuer) == "" {
			return ErrBadSnapshot
		}
		revocations[revocationKey{issuer: rec.Issuer, serial: rec.Serial}] = rec
	}

	pins := make(map[string]PinEntry, len(body.Pins))
	for _, pin := range body.Pins {
		if strings.TrimSpace(pin.Fingerprint) == "" || pin.NotBefore > pin.NotAfter {
			return ErrBadSnapshot
		}
		if _, exists := pins[pin.Fingerprint]; exists {
			return ErrBadSnapshot
		}
		pins[pin.Fingerprint] = pin
	}
	if g.cfg.MaxPins <= 0 || len(pins) > g.cfg.MaxPins {
		if len(pins) > 0 {
			return fmt.Errorf("%w: snapshot contains %d pins, limit is %d", ErrPinLimit, len(pins), g.cfg.MaxPins)
		}
	}

	audit := append([]AuditRecord(nil), body.Audit...)
	var previousSeq int64
	for _, rec := range audit {
		if rec.Seq <= previousSeq || rec.Seq > body.Seq {
			return ErrBadSnapshot
		}
		previousSeq = rec.Seq
	}
	if g.cfg.MaxAudit <= 0 {
		audit = nil
	} else if len(audit) > g.cfg.MaxAudit {
		audit = append([]AuditRecord(nil), audit[len(audit)-g.cfg.MaxAudit:]...)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.anchors = anchors
	g.revocations = revocations
	g.pins = pins
	g.audit = audit
	g.seq = body.Seq
	g.revocationReady = body.RevocationSourceAvailable
	return nil
}

const snapshotVersion = 1

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotBody struct {
	Version                   int                `json:"version"`
	Anchors                   []CertRecord       `json:"anchors"`
	Revocations               []RevocationRecord `json:"revocations"`
	Pins                      []PinEntry         `json:"pins"`
	Audit                     []AuditRecord      `json:"audit"`
	Seq                       int64              `json:"seq"`
	RevocationSourceAvailable bool               `json:"revocation_source_available"`
}

func (g *MtlsChainRevocationGuard) checkChainLocked(chain []CertRecord, now int64) ([]CertRecord, error) {
	if len(chain) == 0 {
		return nil, ErrChainIncomplete
	}
	if len(chain) > g.cfg.MaxChainDepth {
		return nil, ErrChainDepth
	}
	for _, cert := range chain {
		if !completeCert(cert) {
			return nil, ErrBadCertificate
		}
	}

	for i := 0; i < len(chain)-1; i++ {
		expected := chain[i].SignedBy
		if expected == chain[i+1].Fingerprint {
			continue
		}
		for j := i + 1; j < len(chain); j++ {
			if chain[j].Fingerprint == expected {
				return nil, ErrChainOrder
			}
		}
		return nil, ErrChainIncomplete
	}

	last := chain[len(chain)-1]
	anchor, anchored := g.anchors[last.SignedBy]
	if !anchored {
		return nil, ErrUnknownAnchor
	}
	for i := 1; i < len(chain); i++ {
		if !chain[i].IsCA {
			return nil, ErrNotCA
		}
	}
	if !anchor.IsCA {
		return nil, ErrNotCA
	}
	for _, cert := range chain {
		if now < cert.NotBefore || now > cert.NotAfter {
			return nil, ErrCertificateExpired
		}
	}
	if anchor.Fingerprint != last.Fingerprint && (now < anchor.NotBefore || now > anchor.NotAfter) {
		return nil, ErrCertificateExpired
	}
	if !hasClientAuth(chain[0].KeyUsage) {
		return nil, ErrKeyUsage
	}

	ordered := make([]CertRecord, len(chain))
	for i, cert := range chain {
		ordered[i] = copyCert(cert)
	}
	return ordered, nil
}

func (g *MtlsChainRevocationGuard) isRevokedLocked(serial, issuer string) (bool, error) {
	if !g.revocationReady {
		return false, ErrRevocationUnavailable
	}
	_, revoked := g.revocations[revocationKey{issuer: issuer, serial: serial}]
	return revoked, nil
}

func (g *MtlsChainRevocationGuard) fingerprintMatchesLocked(leafFingerprint, peerFingerprint string, now int64) bool {
	if peerFingerprint != "" && peerFingerprint != leafFingerprint {
		return false
	}

	activePins := 0
	leafPinned := false
	for _, pin := range g.pins {
		if now < pin.NotBefore || now > pin.NotAfter {
			continue
		}
		activePins++
		if pin.Fingerprint == leafFingerprint {
			leafPinned = true
		}
	}
	if activePins == 0 {
		return true
	}
	return leafPinned && peerFingerprint == leafFingerprint
}

func (g *MtlsChainRevocationGuard) recordDecisionLocked(decision Decision, chain []CertRecord, now int64) {
	record := AuditRecord{
		Seq:   decision.Seq,
		Event: "deny",
		Depth: decision.Depth,
		Code:  decision.Reason,
		At:    now,
	}
	if decision.Allowed {
		record.Event = "allow"
	}
	if len(chain) > 0 {
		record.Serial = chain[0].Serial
		record.Issuer = chain[0].Issuer
	}
	g.audit = append(g.audit, record)
	if g.cfg.MaxAudit <= 0 {
		g.audit = nil
		return
	}
	if len(g.audit) > g.cfg.MaxAudit {
		g.audit = append([]AuditRecord(nil), g.audit[len(g.audit)-g.cfg.MaxAudit:]...)
	}
}

func reasonForError(err error) string {
	switch {
	case errors.Is(err, ErrChainIncomplete), errors.Is(err, ErrChainOrder):
		return ReasonBrokenLink
	case errors.Is(err, ErrUnknownAnchor):
		return ReasonUnknownAnchor
	case errors.Is(err, ErrCertificateExpired):
		return ReasonCertificateExpired
	case errors.Is(err, ErrNotCA):
		return ReasonIssuerNotCA
	case errors.Is(err, ErrKeyUsage):
		return ReasonLeafKeyUsage
	case errors.Is(err, ErrChainDepth):
		return ReasonChainDepth
	default:
		return ReasonMalformed
	}
}

func completeCert(cert CertRecord) bool {
	return strings.TrimSpace(cert.Serial) != "" &&
		strings.TrimSpace(cert.Subject) != "" &&
		strings.TrimSpace(cert.Issuer) != "" &&
		strings.TrimSpace(cert.Fingerprint) != "" &&
		strings.TrimSpace(cert.SignedBy) != ""
}

func copyCert(cert CertRecord) CertRecord {
	cert.KeyUsage = append([]string(nil), cert.KeyUsage...)
	return cert
}

func fingerprints(chain []CertRecord) []string {
	values := make([]string, len(chain))
	for i, cert := range chain {
		values[i] = cert.Fingerprint
	}
	return values
}

func hasClientAuth(usages []string) bool {
	for _, usage := range usages {
		if usage == "clientAuth" {
			return true
		}
	}
	return false
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
