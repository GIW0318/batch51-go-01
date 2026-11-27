// Package task attests the integrity of a game client before login and before
// a match: it issues one shot challenges, compares the module hashes against
// the pinned manifest and grades the session.
package task

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Attestation verdict actions.
const (
	ActionAllow   = "allow"
	ActionLimited = "limited"
	ActionDeny    = "deny"
)

// Verdict reason codes.
const (
	ReasonVerified       = "verified"
	ReasonModuleMismatch = "module_mismatch"
	ReasonModuleMissing  = "module_missing"
	ReasonModuleDirty    = "module_dirty"
	ReasonUnsigned       = "unsigned"
	ReasonRevoked        = "session_revoked"
)

// Errors reported by the attestation service.
var (
	ErrBadSession        = errors.New("session id is required")
	ErrSessionRevoked    = errors.New("session is revoked")
	ErrChallengeNotFound = errors.New("challenge not found")
	ErrChallengeUsed     = errors.New("challenge already consumed")
	ErrChallengeExpired  = errors.New("challenge expired")
	ErrChallengeLimit    = errors.New("challenge cap reached")
	ErrBadProof          = errors.New("proof is invalid")
	ErrBadManifest       = errors.New("manifest is invalid")
	ErrManifestLimit     = errors.New("manifest exceeds the module cap")
	ErrBadPolicy         = errors.New("attestation policy is invalid")
	ErrBadModule         = errors.New("module name is required")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// ModuleEntry is one pinned module of a manifest version.
type ModuleEntry struct {
	Name     string
	Hash     string
	Size     int
	Critical bool
}

// Manifest is one immutable manifest version.
type Manifest struct {
	Entries []ModuleEntry
}

// ModuleProof is the client side reading of one module.
type ModuleProof struct {
	Name   string
	Hash   string
	Size   int
	ReadOK bool
}

// Proof is the client answer to one challenge.
type Proof struct {
	ChallengeID string
	Session     string
	Modules     []ModuleProof
	Signature   string
}

// Challenge is one one shot attestation ticket.
type Challenge struct {
	ID          string
	Session     string
	Nonce       string
	ManifestVer string
	IssuedAt    time.Time
	ExpiresAt   time.Time
}

// AttestPolicy is the attestation policy in force.
type AttestPolicy struct {
	ChallengeTTL     time.Duration
	RequireSignature bool
	OnMismatch       string
	OnReadFailure    string
}

// Verdict is the graded outcome of one session.
type Verdict struct {
	Session     string
	Action      string
	Reason      string
	ManifestVer string
}

// Reason is one explainable entry of a session verdict.
type Reason struct {
	Code   string
	Module string
	Detail string
}

// AttestStats aggregates the attestation counters.
type AttestStats struct {
	ChallengesIssued int
	ChallengesUsed   int
	Verified         int
	Allowed          int
	Limited          int
	Denied           int
}

// AttestLimits bounds the attestation service.
type AttestLimits struct {
	MaxModules    int
	MaxModuleSize int
	MaxChallenges int
}

type challengeRecord struct {
	ID          string
	Session     string
	Nonce       string
	ManifestVer string
	IssuedAt    time.Time
	ExpiresAt   time.Time
	Consumed    bool
}

func (c challengeRecord) challenge() Challenge {
	return Challenge{
		ID:          c.ID,
		Session:     c.Session,
		Nonce:       c.Nonce,
		ManifestVer: c.ManifestVer,
		IssuedAt:    c.IssuedAt,
		ExpiresAt:   c.ExpiresAt,
	}
}

type gradedReason struct {
	Reason
	action string
}

const (
	snapshotMagic   = "gsb.client-integrity-attestation"
	snapshotVersion = 1
)

type snapshotEnvelope struct {
	Magic   string          `json:"magic"`
	Version int             `json:"version"`
	Digest  string          `json:"digest"`
	State   json.RawMessage `json:"state"`
}

type snapshotState struct {
	CurrentManifest string                       `json:"current_manifest"`
	Manifests       map[string]Manifest          `json:"manifests"`
	Challenges      map[string]challengeSnapshot `json:"challenges"`
	Policy          AttestPolicy                 `json:"policy"`
	DirtyModules    []string                     `json:"dirty_modules"`
	RevokedSessions []string                     `json:"revoked_sessions"`
	LastReasons     map[string][]Reason          `json:"last_reasons"`
	Stats           AttestStats                  `json:"stats"`
}

type challengeSnapshot struct {
	ID          string    `json:"id"`
	Session     string    `json:"session"`
	Nonce       string    `json:"nonce"`
	ManifestVer string    `json:"manifest_ver"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Consumed    bool      `json:"consumed"`
}

// ClientIntegrityAttestation owns the challenge table, the manifests and the
// session verdicts.
type ClientIntegrityAttestation struct {
	mu      sync.Mutex
	limits  AttestLimits
	clock   func() time.Time
	ready   bool
	policy  AttestPolicy
	current string

	manifests  map[string]Manifest
	challenges map[string]challengeRecord
	dirty      map[string]bool
	revoked    map[string]bool
	lastReason map[string][]Reason
	stats      AttestStats
}

// NewClientIntegrityAttestation builds a service with the given caps and clock.
func NewClientIntegrityAttestation(limits AttestLimits, clock func() time.Time) *ClientIntegrityAttestation {
	if clock == nil {
		clock = time.Now
	}
	return &ClientIntegrityAttestation{
		limits:     limits,
		clock:      clock,
		ready:      true,
		policy:     defaultPolicy(),
		manifests:  make(map[string]Manifest),
		challenges: make(map[string]challengeRecord),
		dirty:      make(map[string]bool),
		revoked:    make(map[string]bool),
		lastReason: make(map[string][]Reason),
	}
}

func defaultPolicy() AttestPolicy {
	return AttestPolicy{
		ChallengeTTL:  5 * time.Minute,
		OnMismatch:    ActionDeny,
		OnReadFailure: ActionLimited,
	}
}

func (a *ClientIntegrityAttestation) initLocked() {
	if a.clock == nil {
		a.clock = time.Now
	}
	if !a.ready {
		a.policy = defaultPolicy()
		a.ready = true
	}
	if a.manifests == nil {
		a.manifests = make(map[string]Manifest)
	}
	if a.challenges == nil {
		a.challenges = make(map[string]challengeRecord)
	}
	if a.dirty == nil {
		a.dirty = make(map[string]bool)
	}
	if a.revoked == nil {
		a.revoked = make(map[string]bool)
	}
	if a.lastReason == nil {
		a.lastReason = make(map[string][]Reason)
	}
}

// IssueChallenge hands out one fresh challenge for a session.
func (a *ClientIntegrityAttestation) IssueChallenge(session string) (Challenge, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	if strings.TrimSpace(session) == "" {
		return Challenge{}, ErrBadSession
	}
	if a.revoked[session] {
		return Challenge{}, ErrSessionRevoked
	}
	if a.current == "" {
		return Challenge{}, ErrBadManifest
	}
	if _, ok := a.manifests[a.current]; !ok {
		return Challenge{}, ErrBadManifest
	}
	if !validPolicy(a.policy) {
		return Challenge{}, ErrBadPolicy
	}

	now := a.clock()
	if a.limits.MaxChallenges > 0 {
		live := 0
		for _, challenge := range a.challenges {
			if !challenge.Consumed && !now.After(challenge.ExpiresAt) {
				live++
			}
		}
		if live >= a.limits.MaxChallenges {
			return Challenge{}, ErrChallengeLimit
		}
	}

	id, err := a.uniqueToken(func(token string) bool {
		_, exists := a.challenges[token]
		return exists
	})
	if err != nil {
		return Challenge{}, err
	}
	nonce, err := a.uniqueToken(func(token string) bool {
		for _, challenge := range a.challenges {
			if challenge.Nonce == token {
				return true
			}
		}
		return false
	})
	if err != nil {
		return Challenge{}, err
	}

	record := challengeRecord{
		ID:          id,
		Session:     session,
		Nonce:       nonce,
		ManifestVer: a.current,
		IssuedAt:    now,
		ExpiresAt:   now.Add(a.policy.ChallengeTTL),
	}
	a.challenges[id] = record
	a.stats.ChallengesIssued++
	return record.challenge(), nil
}

func (a *ClientIntegrityAttestation) uniqueToken(exists func(string) bool) (string, error) {
	for {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		token := hex.EncodeToString(raw[:])
		if !exists(token) {
			return token, nil
		}
	}
}

// Verify grades one proof against the challenged manifest version.
func (a *ClientIntegrityAttestation) Verify(session string, proof Proof) (Verdict, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	if strings.TrimSpace(session) == "" {
		return Verdict{}, ErrBadProof
	}
	if a.revoked[session] {
		return Verdict{}, ErrSessionRevoked
	}
	if proof.Session != session {
		return Verdict{}, ErrBadProof
	}

	challenge, ok := a.challenges[proof.ChallengeID]
	if !ok || challenge.Session != session {
		return Verdict{}, ErrChallengeNotFound
	}
	if challenge.Consumed {
		return Verdict{}, ErrChallengeUsed
	}
	now := a.clock()
	if now.After(challenge.ExpiresAt) {
		return Verdict{}, ErrChallengeExpired
	}
	if !validPolicy(a.policy) {
		return Verdict{}, ErrBadPolicy
	}
	if a.policy.RequireSignature && strings.TrimSpace(proof.Signature) == "" {
		return Verdict{}, ErrBadProof
	}

	manifest, ok := a.manifests[challenge.ManifestVer]
	if !ok {
		return Verdict{}, ErrBadManifest
	}

	challenge.Consumed = true
	a.challenges[challenge.ID] = challenge
	a.stats.ChallengesUsed++

	action, primary, reasons := a.grade(manifest, proof.Modules)
	verdict := Verdict{
		Session:     session,
		Action:      action,
		Reason:      primary,
		ManifestVer: challenge.ManifestVer,
	}
	a.lastReason[session] = cloneReasons(reasons)
	a.stats.Verified++
	switch action {
	case ActionAllow:
		a.stats.Allowed++
	case ActionLimited:
		a.stats.Limited++
	case ActionDeny:
		a.stats.Denied++
	}
	return verdict, nil
}

func (a *ClientIntegrityAttestation) grade(manifest Manifest, modules []ModuleProof) (string, string, []Reason) {
	proofByName := make(map[string]ModuleProof, len(modules))
	for _, module := range modules {
		proofByName[module.Name] = module
	}
	manifestNames := make(map[string]bool, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		manifestNames[entry.Name] = true
	}

	graded := make([]gradedReason, 0, len(manifest.Entries))
	add := func(code, module, detail, action string) {
		graded = append(graded, gradedReason{
			Reason: Reason{Code: code, Module: module, Detail: detail},
			action: action,
		})
	}

	for _, entry := range manifest.Entries {
		if a.dirty[entry.Name] {
			add(ReasonModuleDirty, entry.Name, "module is marked dirty", ActionDeny)
			continue
		}
		module, ok := proofByName[entry.Name]
		if !ok {
			add(ReasonModuleMissing, entry.Name, "module missing from proof", a.policy.OnReadFailure)
			continue
		}
		if !module.ReadOK {
			add(ReasonModuleMissing, entry.Name, "module read failed", a.policy.OnReadFailure)
			continue
		}
		if module.Hash != entry.Hash || module.Size != entry.Size {
			add(ReasonModuleMismatch, entry.Name, "module hash or size differs", a.policy.OnMismatch)
		}
	}

	// Extra modules are ignored by the manifest comparison. An explicitly dirty
	// extra module is still unsafe if the client presents it.
	for module := range a.dirty {
		if manifestNames[module] {
			continue
		}
		if _, presented := proofByName[module]; presented {
			add(ReasonModuleDirty, module, "module is marked dirty", ActionDeny)
		}
	}

	if len(graded) == 0 {
		reasons := []Reason{{Code: ReasonVerified}}
		return ActionAllow, ReasonVerified, reasons
	}

	action := ActionAllow
	for _, reason := range graded {
		action = worseAction(action, reason.action)
	}
	sortGraded(graded)
	primary := graded[0].Code
	for _, reason := range graded {
		if reason.action == action {
			primary = reason.Code
			break
		}
	}

	reasons := make([]Reason, 0, len(graded))
	seen := make(map[string]bool, len(graded))
	for _, reason := range graded {
		key := reason.Code + "\x00" + reason.Module
		if seen[key] {
			continue
		}
		seen[key] = true
		reasons = append(reasons, reason.Reason)
	}
	return action, primary, reasons
}

// SetManifest installs a new manifest version.
func (a *ClientIntegrityAttestation) SetManifest(ver string, manifest Manifest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	if err := a.validateManifest(ver, manifest); err != nil {
		return err
	}
	a.manifests[ver] = cloneManifest(manifest)
	a.current = ver
	return nil
}

func (a *ClientIntegrityAttestation) validateManifest(ver string, manifest Manifest) error {
	if strings.TrimSpace(ver) == "" || len(manifest.Entries) == 0 {
		return ErrBadManifest
	}
	if a.limits.MaxModules > 0 && len(manifest.Entries) > a.limits.MaxModules {
		return ErrManifestLimit
	}
	names := make(map[string]bool, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Hash) == "" || entry.Size < 0 {
			return ErrBadManifest
		}
		if names[entry.Name] {
			return ErrBadManifest
		}
		names[entry.Name] = true
		if a.limits.MaxModuleSize > 0 && entry.Size > a.limits.MaxModuleSize {
			return ErrManifestLimit
		}
	}
	return nil
}

// SetPolicy installs a new attestation policy.
func (a *ClientIntegrityAttestation) SetPolicy(p AttestPolicy) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	if !validPolicy(p) {
		return ErrBadPolicy
	}
	a.policy = p
	return nil
}

func validPolicy(p AttestPolicy) bool {
	return p.ChallengeTTL > 0 && validAction(p.OnMismatch) && validAction(p.OnReadFailure)
}

func validAction(action string) bool {
	return action == ActionAllow || action == ActionLimited || action == ActionDeny
}

func actionRank(action string) int {
	switch action {
	case ActionDeny:
		return 2
	case ActionLimited:
		return 1
	default:
		return 0
	}
}

func worseAction(left, right string) string {
	if actionRank(right) > actionRank(left) {
		return right
	}
	return left
}

// MarkModuleDirty flags one module as tampered with.
func (a *ClientIntegrityAttestation) MarkModuleDirty(module string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	if strings.TrimSpace(module) == "" {
		return ErrBadModule
	}
	a.dirty[module] = true
	return nil
}

// Revoke fences a session off for good.
func (a *ClientIntegrityAttestation) Revoke(session string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	if strings.TrimSpace(session) == "" {
		return ErrBadSession
	}
	a.revoked[session] = true
	return nil
}

// Explain lists the reasons attached to the last verdict of a session.
func (a *ClientIntegrityAttestation) Explain(session string) []Reason {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	reasons := cloneReasons(a.lastReason[session])
	sortReasons(reasons)
	return reasons
}

// Stats returns a copy of the attestation counters.
func (a *ClientIntegrityAttestation) Stats() AttestStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()
	return a.stats
}

// Snapshot serialises the challenge table, the manifests and the verdicts.
func (a *ClientIntegrityAttestation) Snapshot() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	state := snapshotState{
		CurrentManifest: a.current,
		Manifests:       cloneManifests(a.manifests),
		Challenges:      make(map[string]challengeSnapshot, len(a.challenges)),
		Policy:          a.policy,
		DirtyModules:    sortedKeys(a.dirty),
		RevokedSessions: sortedKeys(a.revoked),
		LastReasons:     cloneLastReasons(a.lastReason),
		Stats:           a.stats,
	}
	for id, challenge := range a.challenges {
		state.Challenges[id] = challengeSnapshot{
			ID:          challenge.ID,
			Session:     challenge.Session,
			Nonce:       challenge.Nonce,
			ManifestVer: challenge.ManifestVer,
			IssuedAt:    challenge.IssuedAt,
			ExpiresAt:   challenge.ExpiresAt,
			Consumed:    challenge.Consumed,
		}
	}

	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	envelope := snapshotEnvelope{
		Magic:   snapshotMagic,
		Version: snapshotVersion,
		Digest:  hex.EncodeToString(digest[:]),
		State:   raw,
	}
	return json.Marshal(envelope)
}

// Restore replaces the service state with a snapshot, all or nothing.
func (a *ClientIntegrityAttestation) Restore(data []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.initLocked()

	state, ok := decodeSnapshot(data)
	if !ok || !a.validSnapshotState(state) {
		return ErrBadSnapshot
	}

	manifests := make(map[string]Manifest, len(state.Manifests))
	for ver, manifest := range state.Manifests {
		manifests[ver] = cloneManifest(manifest)
	}
	challenges := make(map[string]challengeRecord, len(state.Challenges))
	for id, challenge := range state.Challenges {
		challenges[id] = challengeRecord{
			ID:          challenge.ID,
			Session:     challenge.Session,
			Nonce:       challenge.Nonce,
			ManifestVer: challenge.ManifestVer,
			IssuedAt:    challenge.IssuedAt,
			ExpiresAt:   challenge.ExpiresAt,
			Consumed:    challenge.Consumed,
		}
	}
	dirty := make(map[string]bool, len(state.DirtyModules))
	for _, module := range state.DirtyModules {
		dirty[module] = true
	}
	revoked := make(map[string]bool, len(state.RevokedSessions))
	for _, session := range state.RevokedSessions {
		revoked[session] = true
	}
	lastReasons := make(map[string][]Reason, len(state.LastReasons))
	for session, reasons := range state.LastReasons {
		copied := cloneReasons(reasons)
		sortReasons(copied)
		lastReasons[session] = copied
	}

	a.current = state.CurrentManifest
	a.manifests = manifests
	a.challenges = challenges
	a.policy = state.Policy
	a.dirty = dirty
	a.revoked = revoked
	a.lastReason = lastReasons
	a.stats = state.Stats
	a.ready = true
	return nil
}

func decodeSnapshot(data []byte) (snapshotState, bool) {
	var envelope snapshotEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return snapshotState{}, false
	}
	if err := ensureEOF(decoder); err != nil {
		return snapshotState{}, false
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion || len(envelope.State) == 0 {
		return snapshotState{}, false
	}
	digest := sha256.Sum256(envelope.State)
	if envelope.Digest != hex.EncodeToString(digest[:]) {
		return snapshotState{}, false
	}

	var state snapshotState
	stateDecoder := json.NewDecoder(bytes.NewReader(envelope.State))
	stateDecoder.DisallowUnknownFields()
	if err := stateDecoder.Decode(&state); err != nil {
		return snapshotState{}, false
	}
	if err := ensureEOF(stateDecoder); err != nil {
		return snapshotState{}, false
	}
	return state, true
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing json value")
	}
	return err
}

func (a *ClientIntegrityAttestation) validSnapshotState(state snapshotState) bool {
	if !validPolicy(state.Policy) || !validStats(state.Stats) {
		return false
	}
	if state.Manifests == nil || state.Challenges == nil || state.LastReasons == nil {
		return false
	}
	for ver, manifest := range state.Manifests {
		if a.validateManifest(ver, manifest) != nil {
			return false
		}
	}
	if len(state.Manifests) == 0 {
		if state.CurrentManifest != "" {
			return false
		}
	} else if _, ok := state.Manifests[state.CurrentManifest]; !ok {
		return false
	}

	for id, challenge := range state.Challenges {
		if challenge.ID != id || strings.TrimSpace(challenge.Session) == "" ||
			strings.TrimSpace(challenge.Nonce) == "" || strings.TrimSpace(challenge.ManifestVer) == "" ||
			challenge.IssuedAt.IsZero() || !challenge.ExpiresAt.After(challenge.IssuedAt) {
			return false
		}
		if _, ok := state.Manifests[challenge.ManifestVer]; !ok {
			return false
		}
	}
	if duplicateOrBlank(state.DirtyModules) || duplicateOrBlank(state.RevokedSessions) {
		return false
	}
	for session, reasons := range state.LastReasons {
		if strings.TrimSpace(session) == "" || len(reasons) == 0 {
			return false
		}
		for _, reason := range reasons {
			if strings.TrimSpace(reason.Code) == "" {
				return false
			}
		}
	}
	return true
}

func validStats(stats AttestStats) bool {
	return stats.ChallengesIssued >= 0 && stats.ChallengesUsed >= 0 && stats.Verified >= 0 &&
		stats.Allowed >= 0 && stats.Limited >= 0 && stats.Denied >= 0
}

func duplicateOrBlank(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" || seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func cloneManifest(manifest Manifest) Manifest {
	cloned := Manifest{Entries: make([]ModuleEntry, len(manifest.Entries))}
	copy(cloned.Entries, manifest.Entries)
	return cloned
}

func cloneManifests(manifests map[string]Manifest) map[string]Manifest {
	cloned := make(map[string]Manifest, len(manifests))
	for ver, manifest := range manifests {
		cloned[ver] = cloneManifest(manifest)
	}
	return cloned
}

func cloneLastReasons(reasons map[string][]Reason) map[string][]Reason {
	cloned := make(map[string][]Reason, len(reasons))
	for session, entries := range reasons {
		cloned[session] = cloneReasons(entries)
	}
	return cloned
}

func cloneReasons(reasons []Reason) []Reason {
	if reasons == nil {
		return nil
	}
	cloned := make([]Reason, len(reasons))
	copy(cloned, reasons)
	return cloned
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortGraded(reasons []gradedReason) {
	sort.SliceStable(reasons, func(i, j int) bool {
		if reasons[i].Code != reasons[j].Code {
			return reasons[i].Code < reasons[j].Code
		}
		if reasons[i].Module != reasons[j].Module {
			return reasons[i].Module < reasons[j].Module
		}
		return reasons[i].Detail < reasons[j].Detail
	})
}

func sortReasons(reasons []Reason) {
	sort.SliceStable(reasons, func(i, j int) bool {
		if reasons[i].Code != reasons[j].Code {
			return reasons[i].Code < reasons[j].Code
		}
		if reasons[i].Module != reasons[j].Module {
			return reasons[i].Module < reasons[j].Module
		}
		return reasons[i].Detail < reasons[j].Detail
	})
}
