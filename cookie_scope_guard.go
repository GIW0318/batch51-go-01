// Package task governs the cookie container of an edge proxy.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// SameSite modes accepted by the policy.
const (
	SameSiteLax    = "lax"
	SameSiteStrict = "strict"
	SameSiteNone   = "none"
)

// Cookie prefixes with mandatory attributes.
const (
	PrefixHost   = "__Host-"
	PrefixSecure = "__Secure-"
)

const (
	auditRejected = "rejected"
	auditEvicted  = "evicted"
	snapshotV1    = 1
)

// Errors returned by the guard.
var (
	ErrBadName         = errors.New("cookie name is not acceptable")
	ErrBadDomain       = errors.New("cookie domain is not acceptable")
	ErrDomainMismatch  = errors.New("cookie domain does not cover the origin host")
	ErrPublicSuffix    = errors.New("cookie domain is a public suffix")
	ErrBadPath         = errors.New("cookie path is not acceptable")
	ErrPrefixViolation = errors.New("cookie prefix attributes are not satisfied")
	ErrBadSameSite     = errors.New("cookie same site value is not recognised")
	ErrSameSiteNone    = errors.New("same site none requires the secure attribute")
	ErrCookieTooLarge  = errors.New("cookie exceeds the size budget")
	ErrInsecureOrigin  = errors.New("secure cookie cannot be stored from an insecure origin")
	ErrCookieNotFound  = errors.New("cookie is not present in the container")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")

	errBadOrigin = errors.New("origin is not acceptable")
)

// CookiePolicy bounds the container and lists known public suffixes.
type CookiePolicy struct {
	PublicSuffixes []string
	MaxPerDomain   int
	MaxCookieBytes int
	MaxTotal       int
	DefaultPath    string
}

// Cookie is one stored cookie together with its scope attributes.
type Cookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	Secure   bool
	HTTPOnly bool
	SameSite string
	Expires  time.Time
	Created  time.Time
}

// Request describes the request that asks for matching cookies.
type Request struct {
	Host     string
	Path     string
	Secure   bool
	Method   string
	TopLevel bool
}

// Scope is the explainable scope of one stored cookie.
type Scope struct {
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	HostOnly bool   `json:"host_only"`
	Secure   bool   `json:"secure"`
	SameSite string `json:"same_site"`
	Quoted   bool   `json:"quoted"`
	Reason   string `json:"reason"`
}

// CookieEvent is one audit entry of the container.
type CookieEvent struct {
	Seq    int64  `json:"seq"`
	Action string `json:"action"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Reason string `json:"reason"`
}

// CookieStats exposes the counters of the container.
type CookieStats struct {
	Total    int `json:"total"`
	Domains  int `json:"domains"`
	Stored   int `json:"stored"`
	Rejected int `json:"rejected"`
	Evicted  int `json:"evicted"`
}

type cookieKey struct {
	domain string
	path   string
	name   string
}

type storedCookie struct {
	Cookie   Cookie
	HostOnly bool
	LastUsed time.Time
	Order    uint64
}

// CookieScopeGuard owns the cookie container and its per domain indexes.
type CookieScopeGuard struct {
	mu sync.RWMutex

	cfg            CookiePolicy
	publicSuffixes map[string]struct{}

	byKey     map[cookieKey]*storedCookie
	byDomain  map[string]map[*storedCookie]struct{}
	byPathLen map[int]map[*storedCookie]struct{}

	audit       []CookieEvent
	nextEventID int64
	nextOrder   uint64

	storedCount   int
	rejectedCount int
	evictedCount  int
}

// NewCookieScopeGuard builds a guard with the given policy.
func NewCookieScopeGuard(cfg CookiePolicy) *CookieScopeGuard {
	cfg.PublicSuffixes = append([]string(nil), cfg.PublicSuffixes...)
	if cfg.DefaultPath == "" {
		cfg.DefaultPath = "/"
	}

	suffixes := make(map[string]struct{}, len(cfg.PublicSuffixes))
	for _, suffix := range cfg.PublicSuffixes {
		suffix = canonicalDomain(suffix)
		if suffix != "" {
			suffixes[suffix] = struct{}{}
		}
	}

	return &CookieScopeGuard{
		cfg:            cfg,
		publicSuffixes: suffixes,
		byKey:          make(map[cookieKey]*storedCookie),
		byDomain:       make(map[string]map[*storedCookie]struct{}),
		byPathLen:      make(map[int]map[*storedCookie]struct{}),
	}
}

// Store validates and inserts one cookie for an origin.
func (g *CookieScopeGuard) Store(origin string, c Cookie) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	scheme, originHost, originErr := parseOrigin(origin)
	auditDomain := c.Domain
	if auditDomain == "" && originErr == nil {
		auditDomain = originHost
	}

	if !validCookieName(c.Name) {
		return g.rejectLocked(c.Name, auditDomain, ErrBadName)
	}
	if originErr != nil {
		return g.rejectLocked(c.Name, auditDomain, originErr)
	}

	path := c.Path
	if path == "" {
		path = g.cfg.DefaultPath
	}
	if !validCookiePath(path) {
		return g.rejectLocked(c.Name, auditDomain, ErrBadPath)
	}

	rawDomain := c.Domain
	hostOnly := rawDomain == ""
	domain := originHost
	if !hostOnly {
		var err error
		domain, err = normalizeCookieDomain(rawDomain)
		if err != nil {
			return g.rejectLocked(c.Name, rawDomain, err)
		}
		if g.isPublicSuffix(domain) {
			return g.rejectLocked(c.Name, domain, ErrPublicSuffix)
		}
		if !domainCoversHost(originHost, domain) {
			return g.rejectLocked(c.Name, domain, ErrDomainMismatch)
		}
	}

	sameSite, err := normalizeSameSite(c.SameSite)
	if err != nil {
		return g.rejectLocked(c.Name, domain, err)
	}

	if strings.HasPrefix(c.Name, PrefixHost) &&
		(!c.Secure || path != "/" || rawDomain != "") {
		return g.rejectLocked(c.Name, domain, ErrPrefixViolation)
	}
	if strings.HasPrefix(c.Name, PrefixSecure) && !c.Secure {
		return g.rejectLocked(c.Name, domain, ErrPrefixViolation)
	}

	if c.Secure && scheme != "https" {
		return g.rejectLocked(c.Name, domain, ErrInsecureOrigin)
	}
	if sameSite == SameSiteNone && !c.Secure {
		return g.rejectLocked(c.Name, domain, ErrSameSiteNone)
	}
	if g.cfg.MaxCookieBytes > 0 && len(c.Name)+len(c.Value) > g.cfg.MaxCookieBytes {
		return g.rejectLocked(c.Name, domain, ErrCookieTooLarge)
	}

	c.Domain = domain
	c.Path = path
	c.SameSite = sameSite
	if c.Created.IsZero() {
		c.Created = time.Now()
	}

	key := cookieKey{domain: c.Domain, path: c.Path, name: c.Name}
	if existing := g.byKey[key]; existing != nil {
		if c.Created.Before(existing.Cookie.Created) {
			g.storedCount++
			return nil
		}
		existing.Cookie = c
		existing.HostOnly = hostOnly
		if c.Created.After(existing.LastUsed) {
			existing.LastUsed = c.Created
		}
		g.storedCount++
		return nil
	}

	record := &storedCookie{
		Cookie:   c,
		HostOnly: hostOnly,
		LastUsed: c.Created,
		Order:    g.nextOrder,
	}
	g.nextOrder++
	g.addRecordLocked(record)
	g.enforceLimitsLocked(domain, record)
	g.storedCount++
	return nil
}

// Select returns the cookies that must be sent on a request, longest path
// first and oldest creation first inside one path.
func (g *CookieScopeGuard) Select(origin string, req Request) []Cookie {
	host := normalizeHost(req.Host)
	if host == "" {
		_, originHost, err := parseOrigin(origin)
		if err != nil {
			return nil
		}
		host = originHost
	}

	requestPath := req.Path
	if requestPath == "" {
		requestPath = "/"
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	matches := make([]*storedCookie, 0)
	for _, record := range g.domainCandidatesLocked(host) {
		if record.HostOnly && record.Cookie.Domain != host {
			continue
		}
		if !cookiePathMatches(record.Cookie.Path, requestPath) {
			continue
		}
		if record.Cookie.Secure && !req.Secure {
			continue
		}
		if expiredAt(record.Cookie, now) {
			continue
		}
		matches = append(matches, record)
	}

	sort.Slice(matches, func(i, j int) bool {
		left, right := matches[i], matches[j]
		if len(left.Cookie.Path) != len(right.Cookie.Path) {
			return len(left.Cookie.Path) > len(right.Cookie.Path)
		}
		if !left.Cookie.Created.Equal(right.Cookie.Created) {
			return left.Cookie.Created.Before(right.Cookie.Created)
		}
		if left.Cookie.Domain != right.Cookie.Domain {
			return left.Cookie.Domain < right.Cookie.Domain
		}
		return left.Cookie.Name < right.Cookie.Name
	})

	result := make([]Cookie, 0, len(matches))
	for _, record := range matches {
		if record.LastUsed.Before(now) {
			record.LastUsed = now
		}
		result = append(result, record.Cookie)
	}
	return result
}

// Delete removes every cookie with this name for the origin host.
func (g *CookieScopeGuard) Delete(origin, name string) error {
	_, host, err := parseOrigin(origin)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	removed := 0
	for _, record := range g.domainCandidatesLocked(host) {
		if record.HostOnly && record.Cookie.Domain != host {
			continue
		}
		if record.Cookie.Name == name {
			g.removeRecordLocked(record)
			removed++
		}
	}
	if removed == 0 {
		return ErrCookieNotFound
	}
	return nil
}

// Evict removes cookies whose expiry passed and reports how many were removed.
func (g *CookieScopeGuard) Evict(now time.Time) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	expired := make([]*storedCookie, 0)
	for _, record := range g.byKey {
		if expiredAt(record.Cookie, now) {
			expired = append(expired, record)
		}
	}
	sort.Slice(expired, func(i, j int) bool {
		return storedCookieLess(expired[i], expired[j])
	})

	for _, record := range expired {
		g.evictRecordLocked(record, "expired")
	}
	return len(expired)
}

// Explain reports the effective scope of one stored cookie by name.
func (g *CookieScopeGuard) Explain(name string) (Scope, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	candidates := make([]*storedCookie, 0, 1)
	for _, record := range g.byKey {
		if record.Cookie.Name == name {
			candidates = append(candidates, record)
		}
	}
	if len(candidates) == 0 {
		return Scope{}, ErrCookieNotFound
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if !left.Cookie.Created.Equal(right.Cookie.Created) {
			return left.Cookie.Created.Before(right.Cookie.Created)
		}
		if left.Cookie.Domain != right.Cookie.Domain {
			return left.Cookie.Domain < right.Cookie.Domain
		}
		if left.Cookie.Path != right.Cookie.Path {
			return left.Cookie.Path < right.Cookie.Path
		}
		return left.Order < right.Order
	})

	record := candidates[0]
	return Scope{
		Name:     record.Cookie.Name,
		Domain:   record.Cookie.Domain,
		Path:     record.Cookie.Path,
		HostOnly: record.HostOnly,
		Secure:   record.Cookie.Secure,
		SameSite: record.Cookie.SameSite,
		Quoted:   isQuotedValue(record.Cookie.Value),
		Reason:   scopeReason(record),
	}, nil
}

// Cookies lists the cookies visible to an origin host ordered by domain and
// path.
func (g *CookieScopeGuard) Cookies(origin string) []Cookie {
	_, host, err := parseOrigin(origin)
	if err != nil {
		return nil
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	records := make([]*storedCookie, 0)
	for _, record := range g.domainCandidatesLocked(host) {
		if record.HostOnly && record.Cookie.Domain != host {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		left, right := records[i], records[j]
		if left.Cookie.Domain != right.Cookie.Domain {
			return left.Cookie.Domain < right.Cookie.Domain
		}
		if left.Cookie.Path != right.Cookie.Path {
			return left.Cookie.Path < right.Cookie.Path
		}
		if !left.Cookie.Created.Equal(right.Cookie.Created) {
			return left.Cookie.Created.Before(right.Cookie.Created)
		}
		return left.Cookie.Name < right.Cookie.Name
	})

	result := make([]Cookie, 0, len(records))
	for _, record := range records {
		result = append(result, record.Cookie)
	}
	return result
}

// Clear removes every cookie visible to an origin host and reports the count.
func (g *CookieScopeGuard) Clear(origin string) int {
	_, host, err := parseOrigin(origin)
	if err != nil {
		return 0
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	records := make([]*storedCookie, 0)
	for _, record := range g.domainCandidatesLocked(host) {
		if record.HostOnly && record.Cookie.Domain != host {
			continue
		}
		records = append(records, record)
	}
	for _, record := range records {
		g.removeRecordLocked(record)
	}
	return len(records)
}

// Audit returns a copy of the audit log, oldest first.
func (g *CookieScopeGuard) Audit() []CookieEvent {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]CookieEvent(nil), g.audit...)
}

// Stats reports the current counters.
func (g *CookieScopeGuard) Stats() CookieStats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.statsLocked()
}

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type snapshotCookie struct {
	Cookie   Cookie    `json:"cookie"`
	HostOnly bool      `json:"host_only"`
	LastUsed time.Time `json:"last_used"`
	Order    uint64    `json:"order"`
}

type snapshotState struct {
	Cookies     []snapshotCookie `json:"cookies"`
	Audit       []CookieEvent    `json:"audit"`
	Stats       CookieStats      `json:"stats"`
	NextEventID int64            `json:"next_event_id"`
	NextOrder   uint64           `json:"next_order"`
}

// Snapshot serialises the whole container.
func (g *CookieScopeGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	state := g.snapshotStateLocked()
	g.mu.RUnlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Version:  snapshotV1,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  payload,
	}
	return json.Marshal(envelope)
}

// Restore replaces the state with a snapshot, all or nothing.
func (g *CookieScopeGuard) Restore(data []byte) error {
	state, err := decodeSnapshot(data, g.publicSuffixes)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.byKey = state.byKey
	g.byDomain = state.byDomain
	g.byPathLen = state.byPathLen
	g.audit = state.audit
	g.nextEventID = state.nextEventID
	g.nextOrder = state.nextOrder
	g.storedCount = state.storedCount
	g.rejectedCount = state.rejectedCount
	g.evictedCount = state.evictedCount
	return nil
}

func (g *CookieScopeGuard) rejectLocked(name, domain string, err error) error {
	g.rejectedCount++
	g.nextEventID++
	g.audit = append(g.audit, CookieEvent{
		Seq:    g.nextEventID,
		Action: auditRejected,
		Name:   name,
		Domain: canonicalDomain(domain),
		Reason: err.Error(),
	})
	return err
}

func (g *CookieScopeGuard) addRecordLocked(record *storedCookie) {
	key := cookieKey{
		domain: record.Cookie.Domain,
		path:   record.Cookie.Path,
		name:   record.Cookie.Name,
	}
	g.byKey[key] = record

	domainSet := g.byDomain[record.Cookie.Domain]
	if domainSet == nil {
		domainSet = make(map[*storedCookie]struct{})
		g.byDomain[record.Cookie.Domain] = domainSet
	}
	domainSet[record] = struct{}{}

	pathLen := len(record.Cookie.Path)
	pathSet := g.byPathLen[pathLen]
	if pathSet == nil {
		pathSet = make(map[*storedCookie]struct{})
		g.byPathLen[pathLen] = pathSet
	}
	pathSet[record] = struct{}{}
}

func (g *CookieScopeGuard) removeRecordLocked(record *storedCookie) {
	key := cookieKey{
		domain: record.Cookie.Domain,
		path:   record.Cookie.Path,
		name:   record.Cookie.Name,
	}
	delete(g.byKey, key)

	domainSet := g.byDomain[record.Cookie.Domain]
	delete(domainSet, record)
	if len(domainSet) == 0 {
		delete(g.byDomain, record.Cookie.Domain)
	}

	pathLen := len(record.Cookie.Path)
	pathSet := g.byPathLen[pathLen]
	delete(pathSet, record)
	if len(pathSet) == 0 {
		delete(g.byPathLen, pathLen)
	}
}

func (g *CookieScopeGuard) evictRecordLocked(record *storedCookie, reason string) {
	g.removeRecordLocked(record)
	g.evictedCount++
	g.nextEventID++
	g.audit = append(g.audit, CookieEvent{
		Seq:    g.nextEventID,
		Action: auditEvicted,
		Name:   record.Cookie.Name,
		Domain: record.Cookie.Domain,
		Reason: reason,
	})
}

func (g *CookieScopeGuard) enforceLimitsLocked(domain string, protected *storedCookie) {
	if g.cfg.MaxPerDomain > 0 {
		for len(g.byDomain[domain]) > g.cfg.MaxPerDomain {
			victim := leastRecentlyUsedSet(g.byDomain[domain], protected)
			if victim == nil {
				break
			}
			g.evictRecordLocked(victim, "per-domain limit")
		}
	}
	if g.cfg.MaxTotal > 0 {
		for len(g.byKey) > g.cfg.MaxTotal {
			victim := g.leastRecentlyUsedLocked(protected)
			if victim == nil {
				break
			}
			g.evictRecordLocked(victim, "total limit")
		}
	}
}

func leastRecentlyUsedSet(records map[*storedCookie]struct{}, protected *storedCookie) *storedCookie {
	var victim *storedCookie
	for record := range records {
		if record == protected {
			continue
		}
		if victim == nil || storedCookieLess(record, victim) {
			victim = record
		}
	}
	return victim
}

func (g *CookieScopeGuard) leastRecentlyUsedLocked(protected *storedCookie) *storedCookie {
	var victim *storedCookie
	for _, record := range g.byKey {
		if record == protected {
			continue
		}
		if victim == nil || storedCookieLess(record, victim) {
			victim = record
		}
	}
	return victim
}

func storedCookieLess(left, right *storedCookie) bool {
	if !left.LastUsed.Equal(right.LastUsed) {
		return left.LastUsed.Before(right.LastUsed)
	}
	if !left.Cookie.Created.Equal(right.Cookie.Created) {
		return left.Cookie.Created.Before(right.Cookie.Created)
	}
	return left.Order < right.Order
}

func (g *CookieScopeGuard) domainCandidatesLocked(host string) []*storedCookie {
	if host == "" {
		return nil
	}

	seen := make(map[*storedCookie]struct{})
	candidates := make([]*storedCookie, 0)
	add := func(domain string) {
		for record := range g.byDomain[domain] {
			if _, ok := seen[record]; ok {
				continue
			}
			seen[record] = struct{}{}
			candidates = append(candidates, record)
		}
	}

	add(host)
	if net.ParseIP(host) == nil {
		for i := strings.IndexByte(host, '.'); i >= 0; i = strings.IndexByte(host, '.') {
			host = host[i+1:]
			add(host)
		}
	}
	return candidates
}

func (g *CookieScopeGuard) isPublicSuffix(domain string) bool {
	if _, ok := g.publicSuffixes[domain]; ok {
		return true
	}
	return net.ParseIP(domain) == nil && !strings.Contains(domain, ".")
}

func (g *CookieScopeGuard) statsLocked() CookieStats {
	return CookieStats{
		Total:    len(g.byKey),
		Domains:  len(g.byDomain),
		Stored:   g.storedCount,
		Rejected: g.rejectedCount,
		Evicted:  g.evictedCount,
	}
}

func (g *CookieScopeGuard) snapshotStateLocked() snapshotState {
	records := make([]*storedCookie, 0, len(g.byKey))
	for _, record := range g.byKey {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		left, right := records[i], records[j]
		if left.Cookie.Domain != right.Cookie.Domain {
			return left.Cookie.Domain < right.Cookie.Domain
		}
		if left.Cookie.Path != right.Cookie.Path {
			return left.Cookie.Path < right.Cookie.Path
		}
		if left.Cookie.Name != right.Cookie.Name {
			return left.Cookie.Name < right.Cookie.Name
		}
		return left.Order < right.Order
	})

	state := snapshotState{
		Cookies:     make([]snapshotCookie, 0, len(records)),
		Audit:       append([]CookieEvent(nil), g.audit...),
		Stats:       g.statsLocked(),
		NextEventID: g.nextEventID,
		NextOrder:   g.nextOrder,
	}
	for _, record := range records {
		state.Cookies = append(state.Cookies, snapshotCookie{
			Cookie:   record.Cookie,
			HostOnly: record.HostOnly,
			LastUsed: record.LastUsed,
			Order:    record.Order,
		})
	}
	return state
}

type decodedState struct {
	byKey         map[cookieKey]*storedCookie
	byDomain      map[string]map[*storedCookie]struct{}
	byPathLen     map[int]map[*storedCookie]struct{}
	audit         []CookieEvent
	nextEventID   int64
	nextOrder     uint64
	storedCount   int
	rejectedCount int
	evictedCount  int
}

func decodeSnapshot(data []byte, suffixes map[string]struct{}) (*decodedState, error) {
	var envelope snapshotEnvelope
	if err := decodeJSON(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.Version != snapshotV1 {
		return nil, fmt.Errorf("unsupported snapshot version %d", envelope.Version)
	}
	if len(envelope.Payload) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Payload), []byte("null")) {
		return nil, errors.New("snapshot payload is empty")
	}

	want, err := hex.DecodeString(envelope.Checksum)
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("snapshot checksum is invalid")
	}
	got := sha256.Sum256(envelope.Payload)
	if !bytes.Equal(got[:], want) {
		return nil, errors.New("snapshot checksum does not match")
	}

	var state snapshotState
	if err := decodeJSON(envelope.Payload, &state); err != nil {
		return nil, err
	}
	if state.Stats.Total < 0 || state.Stats.Domains < 0 || state.Stats.Stored < 0 ||
		state.Stats.Rejected < 0 || state.Stats.Evicted < 0 {
		return nil, errors.New("snapshot counters are negative")
	}
	if state.Stats.Total != len(state.Cookies) {
		return nil, errors.New("snapshot total does not match its cookies")
	}

	decoded := &decodedState{
		byKey:         make(map[cookieKey]*storedCookie, len(state.Cookies)),
		byDomain:      make(map[string]map[*storedCookie]struct{}),
		byPathLen:     make(map[int]map[*storedCookie]struct{}),
		audit:         append([]CookieEvent(nil), state.Audit...),
		nextEventID:   state.NextEventID,
		nextOrder:     state.NextOrder,
		storedCount:   state.Stats.Stored,
		rejectedCount: state.Stats.Rejected,
		evictedCount:  state.Stats.Evicted,
	}

	var maxOrder uint64
	var maxEventID int64
	for _, event := range decoded.audit {
		if event.Seq <= 0 {
			return nil, errors.New("snapshot audit sequence is invalid")
		}
		if event.Seq > maxEventID {
			maxEventID = event.Seq
		}
	}
	if decoded.nextEventID < maxEventID {
		return nil, errors.New("snapshot event sequence is invalid")
	}

	for _, snapshotRecord := range state.Cookies {
		c := snapshotRecord.Cookie
		if !validCookieName(c.Name) || !validCookiePath(c.Path) || c.Domain == "" {
			return nil, errors.New("snapshot cookie is invalid")
		}
		if canonicalDomain(c.Domain) != c.Domain {
			return nil, errors.New("snapshot cookie domain is not canonical")
		}
		if !snapshotRecord.HostOnly {
			if _, ok := suffixes[c.Domain]; ok || (net.ParseIP(c.Domain) == nil && !strings.Contains(c.Domain, ".")) {
				return nil, errors.New("snapshot cookie domain is a public suffix")
			}
		}
		if c.Created.IsZero() || snapshotRecord.LastUsed.IsZero() {
			return nil, errors.New("snapshot cookie timestamps are invalid")
		}
		if snapshotRecord.Order > maxOrder {
			maxOrder = snapshotRecord.Order
		}

		record := &storedCookie{
			Cookie:   c,
			HostOnly: snapshotRecord.HostOnly,
			LastUsed: snapshotRecord.LastUsed,
			Order:    snapshotRecord.Order,
		}
		key := cookieKey{domain: c.Domain, path: c.Path, name: c.Name}
		if _, exists := decoded.byKey[key]; exists {
			return nil, errors.New("snapshot contains duplicate cookies")
		}
		decoded.byKey[key] = record

		domainSet := decoded.byDomain[c.Domain]
		if domainSet == nil {
			domainSet = make(map[*storedCookie]struct{})
			decoded.byDomain[c.Domain] = domainSet
		}
		domainSet[record] = struct{}{}

		pathLen := len(c.Path)
		pathSet := decoded.byPathLen[pathLen]
		if pathSet == nil {
			pathSet = make(map[*storedCookie]struct{})
			decoded.byPathLen[pathLen] = pathSet
		}
		pathSet[record] = struct{}{}
	}

	if state.Stats.Domains != len(decoded.byDomain) {
		return nil, errors.New("snapshot domain count does not match its cookies")
	}
	if decoded.nextOrder <= maxOrder {
		decoded.nextOrder = maxOrder + 1
	}
	return decoded, nil
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("snapshot contains trailing data")
		}
		return err
	}
	return nil
}

func parseOrigin(origin string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", errBadOrigin
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := canonicalDomain(parsed.Hostname())
	if host == "" {
		return "", "", errBadOrigin
	}
	return scheme, host, nil
}

func normalizeHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		_, host, err := parseOrigin(raw)
		if err != nil {
			return ""
		}
		return host
	}
	if strings.HasPrefix(raw, "[") {
		if host, _, err := net.SplitHostPort(raw); err == nil {
			raw = host
		} else {
			raw = strings.TrimPrefix(strings.TrimSuffix(raw, "]"), "[")
		}
	} else if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	return canonicalDomain(raw)
}

func canonicalDomain(domain string) string {
	domain = strings.TrimSpace(strings.ToLower(domain))
	domain = strings.TrimLeft(domain, ".")
	domain = strings.TrimRight(domain, ".")
	if strings.HasPrefix(domain, "[") && strings.HasSuffix(domain, "]") {
		domain = strings.TrimSuffix(strings.TrimPrefix(domain, "["), "]")
	}
	return domain
}

func normalizeCookieDomain(raw string) (string, error) {
	domain := canonicalDomain(raw)
	if domain == "" || !validDomainSyntax(domain) {
		return "", ErrBadDomain
	}
	return domain, nil
}

func validDomainSyntax(domain string) bool {
	if net.ParseIP(strings.Trim(domain, "[]")) != nil {
		return true
	}
	for _, r := range domain {
		if r <= ' ' || r == 0x7f {
			return false
		}
		switch r {
		case '/', '\\', ':', ';', ',', '=', '@', '[', ']', '?', '#', '"', '\'':
			return false
		}
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" {
			return false
		}
	}
	return true
}

func domainCoversHost(host, domain string) bool {
	if host == domain {
		return true
	}
	if net.ParseIP(host) != nil || net.ParseIP(domain) != nil {
		return false
	}
	return strings.HasSuffix(host, "."+domain)
}

func validCookieName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if ch <= 0x20 || ch >= 0x7f {
			return false
		}
		switch ch {
		case '(', ')', '<', '>', '@', ',', ';', ':', '\\', '"', '/', '[', ']', '?', '=', '{', '}':
			return false
		}
	}
	return true
}

func validCookiePath(path string) bool {
	if path == "" || path[0] != '/' {
		return false
	}
	for i := 0; i < len(path); i++ {
		if path[i] < 0x20 || path[i] == 0x7f || path[i] == ';' {
			return false
		}
	}
	return true
}

func normalizeSameSite(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case SameSiteLax:
		return SameSiteLax, nil
	case SameSiteStrict:
		return SameSiteStrict, nil
	case SameSiteNone:
		return SameSiteNone, nil
	default:
		return "", ErrBadSameSite
	}
}

func cookiePathMatches(cookiePath, requestPath string) bool {
	if cookiePath == requestPath || cookiePath == "/" {
		return true
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	if strings.HasSuffix(cookiePath, "/") {
		return true
	}
	return requestPath[len(cookiePath)] == '/'
}

func expiredAt(cookie Cookie, now time.Time) bool {
	return !cookie.Expires.IsZero() && !cookie.Expires.After(now)
}

func isQuotedValue(value string) bool {
	return len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"'
}

func scopeReason(record *storedCookie) string {
	reason := "domain=" + record.Cookie.Domain
	if record.HostOnly {
		reason = "host-only"
	}
	if record.Cookie.Secure {
		reason += ", secure"
	}
	if record.Cookie.SameSite != "" {
		reason += ", same-site=" + record.Cookie.SameSite
	}
	if strings.HasPrefix(record.Cookie.Name, PrefixHost) {
		reason += ", __Host- constrained"
	} else if strings.HasPrefix(record.Cookie.Name, PrefixSecure) {
		reason += ", __Secure- constrained"
	}
	return reason
}
