// Package task manages the keytab of a service principal: several key
// generations of different encryption types coexist, a new key is staged
// before it becomes active, an old key may only be retired after its grace
// period, and the whole table is replaced atomically on disk.
package task

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Enctype is the encryption type of one keytab entry.
type Enctype int

// Encryption types the guard understands.
const (
	EnctypeAES128 Enctype = 17
	EnctypeAES256 Enctype = 18
	EnctypeRC4    Enctype = 23
)

// Errors returned by the keytab guard.
var (
	ErrBadEntry          = errors.New("keytab entry is incomplete")
	ErrVersionRegression = errors.New("key version is lower than the newest stored one")
	ErrUnknownVersion    = errors.New("key version is not in the keytab")
	ErrNoActiveKey       = errors.New("principal has no active key")
	ErrStalePromotion    = errors.New("only a version newer than the active one can be promoted")
	ErrActiveKey         = errors.New("the active key cannot be retired")
	ErrGraceNotElapsed   = errors.New("retirement grace period has not elapsed")
	ErrRetired           = errors.New("key version is retired")
	ErrStagedKey         = errors.New("key version is staged but not active")
	ErrNoMatchingKey     = errors.New("no active key matches the ticket checksum")
	ErrVersionBudget     = errors.New("key version budget for this principal is exhausted")
	ErrBadKeytab         = errors.New("keytab file is malformed")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// KeyEntry is one key of one principal.
type KeyEntry struct {
	Principal   string
	Version     int
	Enctype     Enctype
	Material    string
	Staged      bool
	Retired     bool
	InstalledAt time.Time
	ActivatedAt time.Time
	RetiredAt   time.Time
}

// Ticket is a service ticket handed in for verification.
type Ticket struct {
	Principal string
	// KVNO zero means the verifier picks the key itself, newest first.
	KVNO     int
	Enctype  Enctype
	Payload  string
	Checksum string
}

// KeyUse reports which key verified a ticket.
type KeyUse struct {
	Principal string
	Version   int
	Enctype   Enctype
}

// InstallResult reports one accepted keytab write.
type InstallResult struct {
	Entry      KeyEntry
	Generation uint64
}

// KeytabConfig bounds one principal keytab.
type KeytabConfig struct {
	// Grace is how long a superseded key stays usable after activation of its
	// successor.
	Grace time.Duration
	// MaxVersions caps the version count kept per principal.
	MaxVersions int
	// Now is the clock used when a caller does not pass an instant.
	Now func() time.Time
}

// KeytabStats counts keytab activity since the process started.
type KeytabStats struct {
	Installs      int
	Promotions    int
	Retires       int
	Verifications int
	Misses        int
}

type principalState struct {
	entries       []KeyEntry
	activeVersion int
}

// KeytabRotationGuard owns the per principal key generations and the on disk
// keytab image.
type KeytabRotationGuard struct {
	cfg KeytabConfig

	mu         sync.RWMutex
	writeMu    sync.Mutex
	principals map[string]*principalState
	generation uint64
	stats      KeytabStats
}

const imageFormat = "gsb-keytab-rotation-guard/v1"

type imagePayload struct {
	Generation uint64             `json:"generation"`
	Stats      KeytabStats        `json:"stats"`
	Principals []principalPayload `json:"principals"`
}

type principalPayload struct {
	Principal     string     `json:"principal"`
	ActiveVersion int        `json:"active_version"`
	Entries       []KeyEntry `json:"entries"`
}

type imageEnvelope struct {
	Format  string          `json:"format"`
	Digest  string          `json:"digest"`
	Payload json.RawMessage `json:"payload"`
}

// NewKeytabRotationGuard builds a guard with the given grace period.
func NewKeytabRotationGuard(cfg KeytabConfig) *KeytabRotationGuard {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &KeytabRotationGuard{
		cfg:        cfg,
		principals: make(map[string]*principalState),
	}
}

// Install adds a new generation to the keytab of one principal. A fresh key is
// staged and only usable after Promote.
func (g *KeytabRotationGuard) Install(entry KeyEntry, at time.Time) (InstallResult, error) {
	if !validEntry(entry) {
		return InstallResult{}, ErrBadEntry
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()

	ps := g.principals[entry.Principal]
	if ps != nil {
		if entry.Version < highestVersion(ps.entries) {
			return InstallResult{}, ErrVersionRegression
		}
		for _, existing := range ps.entries {
			if existing.Version == entry.Version && existing.Enctype == entry.Enctype {
				if existing.Material != entry.Material {
					return InstallResult{}, ErrBadEntry
				}
				return InstallResult{Entry: existing, Generation: g.generation}, nil
			}
		}
	}

	if g.cfg.MaxVersions > 0 && ps != nil {
		if !versionExists(ps.entries, entry.Version) &&
			versionCount(ps.entries) >= g.cfg.MaxVersions {
			return InstallResult{}, ErrVersionBudget
		}
	}

	if at.IsZero() {
		at = g.now()
	}
	entry.Staged = true
	entry.Retired = false
	entry.InstalledAt = at
	entry.ActivatedAt = time.Time{}
	entry.RetiredAt = time.Time{}

	if ps == nil {
		ps = &principalState{}
		g.principals[entry.Principal] = ps
	}
	ps.entries = append(ps.entries, entry)
	sortEntries(ps.entries)
	g.generation++
	g.stats.Installs++

	return InstallResult{Entry: entry, Generation: g.generation}, nil
}

// Promote makes one staged version the active key.
func (g *KeytabRotationGuard) Promote(principal string, version int, at time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	ps := g.principals[principal]
	if ps == nil || !versionExists(ps.entries, version) {
		return ErrUnknownVersion
	}
	if ps.activeVersion > 0 && version <= ps.activeVersion {
		return ErrStalePromotion
	}

	hasStaged := false
	for _, entry := range ps.entries {
		if entry.Version != version {
			continue
		}
		if entry.Retired {
			return ErrRetired
		}
		if entry.Staged {
			hasStaged = true
		}
	}
	if !hasStaged {
		return ErrStalePromotion
	}

	if at.IsZero() {
		at = g.now()
	}
	for i := range ps.entries {
		if ps.entries[i].Version == version {
			ps.entries[i].Staged = false
			ps.entries[i].ActivatedAt = at
			ps.entries[i].RetiredAt = time.Time{}
		}
	}
	ps.activeVersion = version
	g.generation++
	g.stats.Promotions++
	return nil
}

// Active returns the active key of one principal.
func (g *KeytabRotationGuard) Active(principal string) (KeyEntry, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	ps := g.principals[principal]
	if ps == nil || ps.activeVersion == 0 {
		return KeyEntry{}, ErrNoActiveKey
	}
	for _, entry := range ps.entries {
		if entry.Version == ps.activeVersion && !entry.Staged && !entry.Retired {
			return entry, nil
		}
	}
	return KeyEntry{}, ErrNoActiveKey
}

// Retire marks one superseded version as retired once its grace period has
// elapsed.
func (g *KeytabRotationGuard) Retire(principal string, version int, at time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	ps := g.principals[principal]
	if ps == nil || !versionExists(ps.entries, version) {
		return ErrUnknownVersion
	}
	if ps.activeVersion == version {
		return ErrActiveKey
	}

	hasStaged := false
	hasRetired := false
	for _, entry := range ps.entries {
		if entry.Version != version {
			continue
		}
		if entry.Staged {
			hasStaged = true
		}
		if entry.Retired {
			hasRetired = true
		}
	}
	if hasStaged {
		return ErrStagedKey
	}
	if hasRetired {
		return ErrRetired
	}

	_, successorAt, ok := nextActiveVersion(ps.entries, version)
	if !ok {
		return ErrGraceNotElapsed
	}
	if at.IsZero() {
		at = g.now()
	}
	grace := g.cfg.Grace
	if grace < 0 {
		grace = 0
	}
	if successorAt.IsZero() || at.Before(successorAt.Add(grace)) {
		return ErrGraceNotElapsed
	}

	for i := range ps.entries {
		if ps.entries[i].Version == version {
			ps.entries[i].Retired = true
			ps.entries[i].RetiredAt = at
		}
	}
	g.generation++
	g.stats.Retires++
	return nil
}

// Verify checks a ticket against the keytab, newest usable key first.
func (g *KeytabRotationGuard) Verify(t Ticket) (KeyUse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ps := g.principals[t.Principal]
	if t.KVNO == 0 {
		if ps == nil {
			g.stats.Misses++
			return KeyUse{}, ErrNoMatchingKey
		}
		for i := len(ps.entries) - 1; i >= 0; i-- {
			entry := ps.entries[i]
			if entry.Staged || entry.Retired {
				continue
			}
			if t.Enctype != 0 && entry.Enctype != t.Enctype {
				continue
			}
			if checksumMatches(entry.Material, t.Payload, t.Checksum) {
				g.stats.Verifications++
				return KeyUse{Principal: entry.Principal, Version: entry.Version, Enctype: entry.Enctype}, nil
			}
		}
		g.stats.Misses++
		return KeyUse{}, ErrNoMatchingKey
	}

	if ps == nil || !versionExists(ps.entries, t.KVNO) {
		return KeyUse{}, ErrUnknownVersion
	}

	allStaged := true
	allRetired := true
	matchingStaged := false
	matchingFound := false
	for _, entry := range ps.entries {
		if entry.Version != t.KVNO {
			continue
		}
		if !entry.Staged {
			allStaged = false
		}
		if !entry.Retired {
			allRetired = false
		}
		if t.Enctype == 0 || entry.Enctype == t.Enctype {
			matchingFound = true
			if entry.Staged {
				matchingStaged = true
			}
		}
	}
	if allRetired {
		return KeyUse{}, ErrRetired
	}
	if allStaged {
		return KeyUse{}, ErrStagedKey
	}
	if t.Enctype != 0 && matchingStaged {
		return KeyUse{}, ErrStagedKey
	}
	if !matchingFound {
		g.stats.Misses++
		return KeyUse{}, ErrNoMatchingKey
	}

	for _, entry := range ps.entries {
		if entry.Version != t.KVNO || entry.Staged || entry.Retired {
			continue
		}
		if t.Enctype != 0 && entry.Enctype != t.Enctype {
			continue
		}
		if checksumMatches(entry.Material, t.Payload, t.Checksum) {
			g.stats.Verifications++
			return KeyUse{Principal: entry.Principal, Version: entry.Version, Enctype: entry.Enctype}, nil
		}
	}

	g.stats.Misses++
	return KeyUse{}, ErrNoMatchingKey
}

// Entries lists the keytab of one principal, newest version first and then by
// encryption type in ascending order.
func (g *KeytabRotationGuard) Entries(principal string) []KeyEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()

	ps := g.principals[principal]
	if ps == nil {
		return nil
	}
	entries := append([]KeyEntry(nil), ps.entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Version != entries[j].Version {
			return entries[i].Version > entries[j].Version
		}
		return entries[i].Enctype < entries[j].Enctype
	})
	return entries
}

// Stats returns the guard wide counters.
func (g *KeytabRotationGuard) Stats() KeytabStats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.stats
}

// Generation is the monotonic counter of keytab mutations.
func (g *KeytabRotationGuard) Generation() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.generation
}

// WriteFile replaces the on disk keytab with the current table atomically.
func (g *KeytabRotationGuard) WriteFile(path string) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()

	data, err := g.snapshotBytes()
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// LoadFile replaces the in memory table with the keytab stored on disk.
func (g *KeytabRotationGuard) LoadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	payload, err := decodeImage(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadKeytab, err)
	}
	principals, err := g.validatePayload(payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadKeytab, err)
	}
	g.commitPayload(principals, payload)
	return nil
}

// Snapshot serialises the keytab, the counters and the generation watermark.
func (g *KeytabRotationGuard) Snapshot() ([]byte, error) {
	return g.snapshotBytes()
}

// Restore replaces the whole keytab with a snapshot, all or nothing.
func (g *KeytabRotationGuard) Restore(data []byte) error {
	payload, err := decodeImage(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	principals, err := g.validatePayload(payload)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	g.commitPayload(principals, payload)
	return nil
}

// Seal derives the checksum a ticket carries for one payload under one key.
func Seal(material, payload string) string {
	mac := hmac.New(sha256.New, []byte(material))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func (g *KeytabRotationGuard) initLocked() {
	if g.principals == nil {
		g.principals = make(map[string]*principalState)
	}
}

func (g *KeytabRotationGuard) now() time.Time {
	if g.cfg.Now != nil {
		return g.cfg.Now()
	}
	return time.Now()
}

func (g *KeytabRotationGuard) snapshotBytes() ([]byte, error) {
	g.mu.RLock()
	payload := g.payloadLocked()
	g.mu.RUnlock()

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	envelope := imageEnvelope{
		Format:  imageFormat,
		Digest:  hex.EncodeToString(sum[:]),
		Payload: raw,
	}
	return json.Marshal(envelope)
}

func (g *KeytabRotationGuard) payloadLocked() imagePayload {
	payload := imagePayload{
		Generation: g.generation,
		Stats:      g.stats,
		Principals: make([]principalPayload, 0, len(g.principals)),
	}
	names := make([]string, 0, len(g.principals))
	for principal := range g.principals {
		names = append(names, principal)
	}
	sort.Strings(names)
	for _, principal := range names {
		ps := g.principals[principal]
		entries := append([]KeyEntry(nil), ps.entries...)
		payload.Principals = append(payload.Principals, principalPayload{
			Principal:     principal,
			ActiveVersion: ps.activeVersion,
			Entries:       entries,
		})
	}
	return payload
}

func (g *KeytabRotationGuard) validatePayload(payload *imagePayload) (map[string]*principalState, error) {
	if payload.Stats.Installs < 0 || payload.Stats.Promotions < 0 ||
		payload.Stats.Retires < 0 || payload.Stats.Verifications < 0 || payload.Stats.Misses < 0 {
		return nil, errors.New("negative counter in image")
	}

	principals := make(map[string]*principalState, len(payload.Principals))
	for _, row := range payload.Principals {
		if strings.TrimSpace(row.Principal) == "" {
			return nil, errors.New("empty principal in image")
		}
		if _, exists := principals[row.Principal]; exists {
			return nil, errors.New("duplicate principal in image")
		}
		if len(row.Entries) == 0 {
			return nil, errors.New("principal has no key entries")
		}

		entries := append([]KeyEntry(nil), row.Entries...)
		seen := make(map[[2]int]struct{}, len(entries))
		versions := make(map[int]struct{}, len(entries))
		for _, entry := range entries {
			if !validEntry(entry) || entry.Principal != row.Principal {
				return nil, errors.New("invalid key entry in image")
			}
			if entry.Staged && entry.Retired {
				return nil, errors.New("key entry is both staged and retired")
			}
			key := [2]int{entry.Version, int(entry.Enctype)}
			if _, exists := seen[key]; exists {
				return nil, errors.New("duplicate key version and enctype in image")
			}
			seen[key] = struct{}{}
			versions[entry.Version] = struct{}{}
		}
		if g.cfg.MaxVersions > 0 && len(versions) > g.cfg.MaxVersions {
			return nil, errors.New("image exceeds configured version budget")
		}

		sortEntries(entries)
		activeVersion := 0
		for _, entry := range entries {
			if !entry.Staged && !entry.Retired && entry.Version > activeVersion {
				activeVersion = entry.Version
			}
		}
		if row.ActiveVersion != activeVersion {
			return nil, errors.New("active version marker is inconsistent")
		}
		principals[row.Principal] = &principalState{
			entries:       entries,
			activeVersion: activeVersion,
		}
	}
	return principals, nil
}

func (g *KeytabRotationGuard) commitPayload(principals map[string]*principalState, payload *imagePayload) {
	g.mu.Lock()
	defer g.mu.Unlock()

	generation := payload.Generation
	if generation < g.generation {
		generation = g.generation
	}
	g.principals = principals
	g.stats = payload.Stats
	g.generation = generation
}

func decodeImage(data []byte) (*imagePayload, error) {
	var envelope imageEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.Format != imageFormat || len(envelope.Payload) == 0 || envelope.Digest == "" {
		return nil, errors.New("unrecognised image header")
	}
	sum := sha256.Sum256(envelope.Payload)
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(envelope.Digest)) != 1 {
		return nil, errors.New("image digest mismatch")
	}

	var payload imagePayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}()

	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func validEntry(entry KeyEntry) bool {
	if strings.TrimSpace(entry.Principal) == "" || strings.TrimSpace(entry.Material) == "" || entry.Version < 1 {
		return false
	}
	switch entry.Enctype {
	case EnctypeAES128, EnctypeAES256, EnctypeRC4:
		return true
	default:
		return false
	}
}

func checksumMatches(material, payload, checksum string) bool {
	expected := Seal(material, payload)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(checksum)) == 1
}

func highestVersion(entries []KeyEntry) int {
	highest := 0
	for _, entry := range entries {
		if entry.Version > highest {
			highest = entry.Version
		}
	}
	return highest
}

func versionExists(entries []KeyEntry, version int) bool {
	for _, entry := range entries {
		if entry.Version == version {
			return true
		}
	}
	return false
}

func versionCount(entries []KeyEntry) int {
	count := 0
	last := 0
	for _, entry := range entries {
		if entry.Version != last {
			count++
			last = entry.Version
		}
	}
	return count
}

func nextActiveVersion(entries []KeyEntry, version int) (int, time.Time, bool) {
	successorVersion := 0
	var successorAt time.Time
	for _, entry := range entries {
		if entry.Version <= version || entry.Staged || entry.Retired {
			continue
		}
		if successorVersion == 0 || entry.Version < successorVersion {
			successorVersion = entry.Version
			successorAt = entry.ActivatedAt
			continue
		}
		if entry.Version == successorVersion && entry.ActivatedAt.After(successorAt) {
			successorAt = entry.ActivatedAt
		}
	}
	return successorVersion, successorAt, successorVersion != 0
}

func sortEntries(entries []KeyEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Version != entries[j].Version {
			return entries[i].Version < entries[j].Version
		}
		return entries[i].Enctype < entries[j].Enctype
	})
}
