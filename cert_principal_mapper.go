// Package task maps a client certificate identity onto an internal account for
// the mTLS ingress gateway: ordered matching rules per tenant, conflict
// reporting and a per fingerprint result cache.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
)

// Matchers supported by a mapping rule.
const (
	MatcherDN       = "dn"
	MatcherCN       = "cn"
	MatcherSANDNS   = "san_dns"
	MatcherSANEmail = "san_email"
	MatcherSANURI   = "san_uri"
	MatcherIssuerFP = "issuer_fp"
)

// Principal kinds produced by a rule.
const (
	KindUser    = "user"
	KindService = "service"
)

// SAN types carried by a certificate view.
const (
	SANDNS   = "dns"
	SANEmail = "email"
	SANURI   = "uri"
)

// Trace reasons reported by Explain.
const (
	ReasonMatched  = "matched"
	ReasonNoMatch  = "no_match"
	ReasonConflict = "conflict"
	ReasonRevoked  = "revoked"
)

// Errors returned by the mapper.
var (
	ErrMissingField    = errors.New("rule id, pattern, account and certificate fingerprint are required")
	ErrBadMatcher      = errors.New("matcher is not supported")
	ErrBadPriority     = errors.New("rule priority must not be negative")
	ErrRuleExists      = errors.New("rule already exists for this tenant")
	ErrRuleQuota       = errors.New("too many rules for this tenant")
	ErrRuleNotFound    = errors.New("rule not found")
	ErrNoMapping       = errors.New("no rule maps this certificate")
	ErrMappingConflict = errors.New("two rules at the same priority map this certificate")
	ErrCertRevoked     = errors.New("certificate fingerprint is revoked")
	ErrSANQuota        = errors.New("certificate carries too many subject alternative names")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// MapperConfig bounds the rule and cache tables.
type MapperConfig struct {
	DefaultTenant     string
	MaxRulesPerTenant int
	MaxCacheEntries   int
	MaxSANs           int
}

// MappingRule is one ordered matcher of one tenant.
type MappingRule struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
	Matcher  string `json:"matcher"`
	Pattern  string `json:"pattern"`
	Account  string `json:"account"`
	Kind     string `json:"kind"`
}

// SAN is one subject alternative name of the presented certificate.
type SAN struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// CertView is the certificate as seen by the gateway.
type CertView struct {
	Fingerprint string `json:"fingerprint"`
	SubjectDN   string `json:"subject_dn"`
	CN          string `json:"cn"`
	SANs        []SAN  `json:"sans"`
	IssuerFP    string `json:"issuer_fp"`
	Revoked     bool   `json:"revoked"`
}

// Principal is the account resolved for one certificate.
type Principal struct {
	Account     string `json:"account"`
	Kind        string `json:"kind"`
	Tenant      string `json:"tenant"`
	RuleID      string `json:"rule_id"`
	Matched     string `json:"matched"`
	Fingerprint string `json:"fingerprint"`
}

// Trace is the operator view of one resolve attempt.
type Trace struct {
	Tenant      string   `json:"tenant"`
	Fingerprint string   `json:"fingerprint"`
	Evaluated   []string `json:"evaluated"`
	Winner      string   `json:"winner"`
	Conflict    bool     `json:"conflict"`
	Reason      string   `json:"reason"`
}

// MapperStats aggregates the counters of the mapper.
type MapperStats struct {
	Resolved  int `json:"resolved"`
	CacheHits int `json:"cache_hits"`
	Rejected  int `json:"rejected"`
	Evicted   int `json:"evicted"`
}

type compiledRule struct {
	rule    MappingRule
	idKey   string
	pattern string
}

type tenantRuleSet struct {
	rules []compiledRule
	byID  map[string]struct{}
}

type cacheKey struct {
	tenant      string
	fingerprint string
}

type resolveFlight struct {
	done      chan struct{}
	rules     *tenantRuleSet
	epoch     uint64
	principal Principal
	err       error
}

type snapshotCacheEntry struct {
	Tenant      string    `json:"tenant"`
	Fingerprint string    `json:"fingerprint"`
	Principal   Principal `json:"principal"`
}

type mapperSnapshot struct {
	Version int                      `json:"version"`
	Rules   map[string][]MappingRule `json:"rules"`
	Revoked []string                 `json:"revoked"`
	Cache   []snapshotCacheEntry     `json:"cache"`
	Stats   MapperStats              `json:"stats"`
}

const snapshotVersion = 1

// CertPrincipalMapper owns the per tenant rule sets, the revoked fingerprints
// and the resolution cache.
type CertPrincipalMapper struct {
	cfg MapperConfig

	mu         sync.Mutex
	epoch      uint64
	tenants    map[string]*tenantRuleSet
	cache      map[cacheKey]Principal
	cacheOrder []cacheKey
	flights    map[cacheKey]*resolveFlight
	revoked    map[string]struct{}
	stats      MapperStats
}

// NewCertPrincipalMapper builds a mapper with the given caps.
func NewCertPrincipalMapper(cfg MapperConfig) *CertPrincipalMapper {
	cfg.DefaultTenant = strings.TrimSpace(cfg.DefaultTenant)
	if cfg.MaxRulesPerTenant < 0 {
		cfg.MaxRulesPerTenant = 0
	}
	if cfg.MaxCacheEntries < 0 {
		cfg.MaxCacheEntries = 0
	}
	if cfg.MaxSANs < 0 {
		cfg.MaxSANs = 0
	}
	return &CertPrincipalMapper{
		cfg:     cfg,
		tenants: make(map[string]*tenantRuleSet),
		cache:   make(map[cacheKey]Principal),
		flights: make(map[cacheKey]*resolveFlight),
		revoked: make(map[string]struct{}),
	}
}

// AddRule appends one rule to a tenant rule set.
func (m *CertPrincipalMapper) AddRule(tenant string, rule MappingRule) error {
	tenant = strings.TrimSpace(tenant)
	if tenant == "" || strings.TrimSpace(rule.ID) == "" ||
		strings.TrimSpace(rule.Pattern) == "" || strings.TrimSpace(rule.Account) == "" {
		return ErrMissingField
	}

	compiled, err := compileRule(rule)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.tenants[tenant]
	if current != nil {
		if _, exists := current.byID[compiled.idKey]; exists {
			return ErrRuleExists
		}
	}
	if len(currentRules(current)) >= m.cfg.MaxRulesPerTenant {
		return ErrRuleQuota
	}

	next := make([]compiledRule, 0, len(currentRules(current))+1)
	next = append(next, currentRules(current)...)
	next = append(next, compiled)
	sortRules(next)

	byID := make(map[string]struct{}, len(next))
	for _, item := range next {
		byID[item.idKey] = struct{}{}
	}
	m.tenants[tenant] = &tenantRuleSet{rules: next, byID: byID}
	m.dropTenantCache(tenant)
	return nil
}

// RemoveRule drops one rule from a tenant rule set.
func (m *CertPrincipalMapper) RemoveRule(tenant, ruleID string) error {
	tenant = strings.TrimSpace(tenant)
	ruleID = strings.TrimSpace(ruleID)
	if tenant == "" || ruleID == "" {
		return ErrMissingField
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.tenants[tenant]
	if current == nil {
		return ErrRuleNotFound
	}
	if _, exists := current.byID[ruleID]; !exists {
		return ErrRuleNotFound
	}

	next := make([]compiledRule, 0, len(current.rules)-1)
	for _, item := range current.rules {
		if item.idKey != ruleID {
			next = append(next, item)
		}
	}
	if len(next) == 0 {
		delete(m.tenants, tenant)
	} else {
		byID := make(map[string]struct{}, len(next))
		for _, item := range next {
			byID[item.idKey] = struct{}{}
		}
		m.tenants[tenant] = &tenantRuleSet{rules: next, byID: byID}
	}
	m.dropTenantCache(tenant)
	return nil
}

// Rules lists the rules of one tenant in ascending priority order.
func (m *CertPrincipalMapper) Rules(tenant string) []MappingRule {
	tenant = strings.TrimSpace(tenant)
	if tenant == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current := m.tenants[tenant]
	if current == nil {
		return nil
	}
	out := make([]MappingRule, len(current.rules))
	for i, item := range current.rules {
		out[i] = item.rule
	}
	return out
}

// Resolve maps one certificate onto one internal account.
func (m *CertPrincipalMapper) Resolve(tenant string, cert CertView) (Principal, error) {
	m.mu.Lock()

	tenant, fingerprint, err := m.validateRequest(tenant, cert)
	if err != nil {
		m.stats.Rejected++
		m.mu.Unlock()
		return Principal{}, err
	}
	if cert.Revoked {
		m.stats.Rejected++
		m.mu.Unlock()
		return Principal{}, ErrCertRevoked
	}
	if _, revoked := m.revoked[fingerprint]; revoked {
		m.stats.Rejected++
		m.mu.Unlock()
		return Principal{}, ErrCertRevoked
	}
	if len(cert.SANs) > m.cfg.MaxSANs {
		m.stats.Rejected++
		m.mu.Unlock()
		return Principal{}, ErrSANQuota
	}

	key := cacheKey{tenant: tenant, fingerprint: fingerprint}
	if principal, ok := m.cache[key]; ok {
		m.stats.CacheHits++
		m.mu.Unlock()
		return principal, nil
	}

	rules := m.tenants[tenant]
	if flight, ok := m.flights[key]; ok && flight.rules == rules && flight.epoch == m.epoch {
		m.mu.Unlock()
		<-flight.done

		m.mu.Lock()
		if m.epoch == flight.epoch {
			if flight.err != nil {
				m.stats.Rejected++
			} else {
				m.stats.CacheHits++
			}
		}
		principal, flightErr := flight.principal, flight.err
		m.mu.Unlock()
		return principal, flightErr
	}

	flight := &resolveFlight{
		done:  make(chan struct{}),
		rules: rules,
		epoch: m.epoch,
	}
	m.flights[key] = flight
	m.mu.Unlock()

	principal, evalErr := evaluateWithRules(tenant, fingerprint, currentRules(rules), cert)

	m.mu.Lock()
	if _, revoked := m.revoked[fingerprint]; revoked {
		principal = Principal{}
		evalErr = ErrCertRevoked
	}
	if m.epoch == flight.epoch {
		if evalErr != nil {
			m.stats.Rejected++
		} else {
			m.stats.Resolved++
			if m.tenants[tenant] == rules {
				m.putCacheLocked(key, principal)
			}
		}
	}
	flight.principal = principal
	flight.err = evalErr
	if m.flights[key] == flight {
		delete(m.flights, key)
	}
	close(flight.done)
	m.mu.Unlock()
	return principal, evalErr
}

// Explain reports how one certificate was judged.
func (m *CertPrincipalMapper) Explain(tenant string, cert CertView) (Trace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tenant, fingerprint, err := m.validateRequest(tenant, cert)
	if err != nil {
		return Trace{}, err
	}
	trace := Trace{Tenant: tenant, Fingerprint: fingerprint}
	if cert.Revoked {
		trace.Reason = ReasonRevoked
		return trace, nil
	}
	if _, revoked := m.revoked[fingerprint]; revoked {
		trace.Reason = ReasonRevoked
		return trace, nil
	}
	if len(cert.SANs) > m.cfg.MaxSANs {
		return trace, ErrSANQuota
	}

	rules := currentRules(m.tenants[tenant])
	trace.Evaluated = make([]string, 0, len(rules))
	matches := make([]ruleMatch, 0)
	for _, item := range rules {
		trace.Evaluated = append(trace.Evaluated, item.rule.ID)
		matched := matchRule(item, cert)
		if len(matched) != 0 {
			matches = append(matches, ruleMatch{rule: item, values: matched})
		}
	}

	decision := decide(matches)
	switch decision.reason {
	case decisionNoMatch:
		trace.Reason = ReasonNoMatch
	case decisionConflict:
		trace.Conflict = true
		trace.Reason = ReasonConflict
	case decisionMatched:
		trace.Winner = decision.principal.RuleID
		trace.Reason = ReasonMatched
	}
	return trace, nil
}

// ResolveMany maps a batch of certificates of one tenant.
func (m *CertPrincipalMapper) ResolveMany(tenant string, certs []CertView) ([]Principal, error) {
	out := make([]Principal, 0, len(certs))
	for _, cert := range certs {
		principal, err := m.Resolve(tenant, cert)
		if err != nil {
			return nil, err
		}
		out = append(out, principal)
	}
	return out, nil
}

// Invalidate revokes one certificate fingerprint and drops its cache entry.
func (m *CertPrincipalMapper) Invalidate(certFP string) error {
	certFP = strings.TrimSpace(certFP)
	if certFP == "" {
		return ErrMissingField
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[certFP] = struct{}{}
	m.dropFingerprintCache(certFP)
	return nil
}

// Revoked reports whether one fingerprint was invalidated.
func (m *CertPrincipalMapper) Revoked(certFP string) bool {
	certFP = strings.TrimSpace(certFP)
	if certFP == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.revoked[certFP]
	return ok
}

// CacheSize reports how many fingerprint results are cached.
func (m *CertPrincipalMapper) CacheSize() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.cache)
}

// Stats returns a snapshot of the counters.
func (m *CertPrincipalMapper) Stats() MapperStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

// Snapshot serialises rules, revocations, counters and the cache.
func (m *CertPrincipalMapper) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	snap := mapperSnapshot{
		Version: snapshotVersion,
		Rules:   make(map[string][]MappingRule, len(m.tenants)),
		Revoked: make([]string, 0, len(m.revoked)),
		Cache:   make([]snapshotCacheEntry, 0, len(m.cache)),
		Stats:   m.stats,
	}
	for tenant, set := range m.tenants {
		rules := make([]MappingRule, len(set.rules))
		for i, item := range set.rules {
			rules[i] = item.rule
		}
		snap.Rules[tenant] = rules
	}
	for fingerprint := range m.revoked {
		snap.Revoked = append(snap.Revoked, fingerprint)
	}
	sort.Strings(snap.Revoked)
	for _, key := range m.cacheOrder {
		principal, ok := m.cache[key]
		if !ok {
			continue
		}
		snap.Cache = append(snap.Cache, snapshotCacheEntry{
			Tenant:      key.tenant,
			Fingerprint: key.fingerprint,
			Principal:   principal,
		})
	}
	return json.Marshal(snap)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *CertPrincipalMapper) Restore(data []byte) error {
	var snap mapperSnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&snap); err != nil {
		return badSnapshot(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion || snap.Stats.Resolved < 0 ||
		snap.Stats.CacheHits < 0 || snap.Stats.Rejected < 0 || snap.Stats.Evicted < 0 {
		return badSnapshot(errors.New("unsupported or invalid snapshot"))
	}

	tenants := make(map[string]*tenantRuleSet, len(snap.Rules))
	for tenant, rules := range snap.Rules {
		tenant = strings.TrimSpace(tenant)
		if tenant == "" || len(rules) > m.cfg.MaxRulesPerTenant {
			return badSnapshot(errors.New("invalid tenant rule set"))
		}
		compiled := make([]compiledRule, 0, len(rules))
		byID := make(map[string]struct{}, len(rules))
		for _, rule := range rules {
			if strings.TrimSpace(rule.ID) == "" || strings.TrimSpace(rule.Pattern) == "" ||
				strings.TrimSpace(rule.Account) == "" {
				return badSnapshot(ErrMissingField)
			}
			item, err := compileRule(rule)
			if err != nil {
				return badSnapshot(err)
			}
			if _, exists := byID[item.idKey]; exists {
				return badSnapshot(ErrRuleExists)
			}
			byID[item.idKey] = struct{}{}
			compiled = append(compiled, item)
		}
		sortRules(compiled)
		tenants[tenant] = &tenantRuleSet{rules: compiled, byID: byID}
	}

	revoked := make(map[string]struct{}, len(snap.Revoked))
	for _, fingerprint := range snap.Revoked {
		fingerprint = strings.TrimSpace(fingerprint)
		if fingerprint == "" {
			return badSnapshot(errors.New("empty revoked fingerprint"))
		}
		revoked[fingerprint] = struct{}{}
	}

	if len(snap.Cache) > m.cfg.MaxCacheEntries {
		return badSnapshot(errors.New("cache exceeds configured capacity"))
	}
	cache := make(map[cacheKey]Principal, len(snap.Cache))
	cacheOrder := make([]cacheKey, 0, len(snap.Cache))
	for _, entry := range snap.Cache {
		tenant := strings.TrimSpace(entry.Tenant)
		fingerprint := strings.TrimSpace(entry.Fingerprint)
		if tenant == "" || fingerprint == "" || entry.Principal.Tenant != tenant ||
			entry.Principal.Fingerprint != fingerprint || entry.Principal.Account == "" {
			return badSnapshot(errors.New("invalid cache entry"))
		}
		key := cacheKey{tenant: tenant, fingerprint: fingerprint}
		if _, exists := cache[key]; exists {
			return badSnapshot(errors.New("duplicate cache entry"))
		}
		cache[key] = entry.Principal
		cacheOrder = append(cacheOrder, key)
	}

	m.mu.Lock()
	m.tenants = tenants
	m.revoked = revoked
	m.cache = cache
	m.cacheOrder = cacheOrder
	m.stats = snap.Stats
	m.epoch++
	m.flights = make(map[cacheKey]*resolveFlight)
	m.mu.Unlock()
	return nil
}

func compileRule(rule MappingRule) (compiledRule, error) {
	matcher := strings.TrimSpace(rule.Matcher)
	switch matcher {
	case MatcherDN, MatcherCN, MatcherSANDNS, MatcherSANEmail, MatcherSANURI, MatcherIssuerFP:
	default:
		return compiledRule{}, ErrBadMatcher
	}
	if rule.Priority < 0 {
		return compiledRule{}, ErrBadPriority
	}
	rule.Matcher = matcher
	return compiledRule{
		rule:    rule,
		idKey:   strings.TrimSpace(rule.ID),
		pattern: normalizeValue(matcher, rule.Pattern),
	}, nil
}

func currentRules(set *tenantRuleSet) []compiledRule {
	if set == nil {
		return nil
	}
	return set.rules
}

func sortRules(rules []compiledRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].rule.Priority != rules[j].rule.Priority {
			return rules[i].rule.Priority < rules[j].rule.Priority
		}
		return rules[i].idKey < rules[j].idKey
	})
}

func normalizeValue(matcher, value string) string {
	value = strings.TrimSpace(value)
	switch matcher {
	case MatcherDN, MatcherCN:
		return strings.ToLower(value)
	case MatcherSANDNS:
		return strings.ToLower(strings.TrimRight(value, "."))
	case MatcherSANEmail:
		at := strings.LastIndexByte(value, '@')
		if at < 0 {
			return value
		}
		return value[:at] + strings.ToLower(value[at:])
	case MatcherSANURI, MatcherIssuerFP:
		return strings.ToLower(value)
	default:
		return value
	}
}

func (m *CertPrincipalMapper) validateRequest(tenant string, cert CertView) (string, string, error) {
	tenant = strings.TrimSpace(tenant)
	if tenant == "" {
		tenant = m.cfg.DefaultTenant
	}
	fingerprint := strings.TrimSpace(cert.Fingerprint)
	if tenant == "" || fingerprint == "" {
		return "", "", ErrMissingField
	}
	return tenant, fingerprint, nil
}

type ruleMatch struct {
	rule   compiledRule
	values []string
}

type decisionKind int

const (
	decisionNoMatch decisionKind = iota
	decisionMatched
	decisionConflict
)

type matchDecision struct {
	kind      decisionKind
	reason    decisionKind
	principal Principal
}

func decide(matches []ruleMatch) matchDecision {
	if len(matches) == 0 {
		return matchDecision{kind: decisionNoMatch, reason: decisionNoMatch}
	}

	minPriority := matches[0].rule.rule.Priority
	atMinPriority := 0
	for _, match := range matches {
		if match.rule.rule.Priority == minPriority {
			atMinPriority++
		}
	}
	if atMinPriority > 1 {
		return matchDecision{kind: decisionConflict, reason: decisionConflict}
	}

	emailAccounts := make(map[string]map[string]struct{})
	for _, match := range matches {
		if match.rule.rule.Matcher != MatcherSANEmail {
			continue
		}
		for _, value := range match.values {
			normalized := normalizeValue(MatcherSANEmail, value)
			accounts := emailAccounts[normalized]
			if accounts == nil {
				accounts = make(map[string]struct{})
				emailAccounts[normalized] = accounts
			}
			accounts[match.rule.rule.Account] = struct{}{}
		}
	}
	if len(emailAccounts) > 1 {
		distinctAccounts := make(map[string]struct{})
		for _, accounts := range emailAccounts {
			for account := range accounts {
				distinctAccounts[account] = struct{}{}
			}
		}
		if len(distinctAccounts) > 1 {
			return matchDecision{kind: decisionConflict, reason: decisionConflict}
		}
	}

	winner := matches[0]
	return matchDecision{
		kind:   decisionMatched,
		reason: decisionMatched,
		principal: Principal{
			Account: winner.rule.rule.Account,
			Kind:    winner.rule.rule.Kind,
			RuleID:  winner.rule.rule.ID,
			Matched: winner.values[0],
		},
	}
}

func matchRule(rule compiledRule, cert CertView) []string {
	switch rule.rule.Matcher {
	case MatcherDN:
		if normalizeValue(MatcherDN, cert.SubjectDN) == rule.pattern {
			return []string{strings.TrimSpace(cert.SubjectDN)}
		}
	case MatcherCN:
		if normalizeValue(MatcherCN, cert.CN) == rule.pattern {
			return []string{strings.TrimSpace(cert.CN)}
		}
	case MatcherIssuerFP:
		if normalizeValue(MatcherIssuerFP, cert.IssuerFP) == rule.pattern {
			return []string{strings.TrimSpace(cert.IssuerFP)}
		}
	case MatcherSANDNS:
		return matchSANs(rule, cert.SANs, SANDNS)
	case MatcherSANEmail:
		return matchSANs(rule, cert.SANs, SANEmail)
	case MatcherSANURI:
		return matchSANs(rule, cert.SANs, SANURI)
	}
	return nil
}

func matchSANs(rule compiledRule, sans []SAN, sanType string) []string {
	var matches []string
	for _, san := range sans {
		if san.Type != sanType {
			continue
		}
		if normalizeValue(rule.rule.Matcher, san.Value) == rule.pattern {
			matches = append(matches, strings.TrimSpace(san.Value))
		}
	}
	return matches
}

func evaluateWithRules(tenant, fingerprint string, rules []compiledRule, cert CertView) (Principal, error) {
	matches := make([]ruleMatch, 0)
	for _, item := range rules {
		matched := matchRule(item, cert)
		if len(matched) != 0 {
			matches = append(matches, ruleMatch{rule: item, values: matched})
		}
	}

	result := decide(matches)
	switch result.reason {
	case decisionNoMatch:
		return Principal{}, ErrNoMapping
	case decisionConflict:
		return Principal{}, ErrMappingConflict
	}
	principal := result.principal
	principal.Tenant = tenant
	principal.Fingerprint = fingerprint
	return principal, nil
}

func (m *CertPrincipalMapper) putCacheLocked(key cacheKey, principal Principal) {
	if m.cfg.MaxCacheEntries == 0 {
		return
	}
	if _, exists := m.cache[key]; exists {
		m.cache[key] = principal
		return
	}
	for len(m.cache) >= m.cfg.MaxCacheEntries {
		if len(m.cacheOrder) == 0 {
			return
		}
		oldest := m.cacheOrder[0]
		m.cacheOrder = m.cacheOrder[1:]
		if _, exists := m.cache[oldest]; !exists {
			continue
		}
		delete(m.cache, oldest)
		m.stats.Evicted++
	}
	m.cache[key] = principal
	m.cacheOrder = append(m.cacheOrder, key)
}

func (m *CertPrincipalMapper) dropTenantCache(tenant string) {
	if len(m.cache) == 0 {
		return
	}
	for key := range m.cache {
		if key.tenant == tenant {
			delete(m.cache, key)
		}
	}
	m.compactCacheOrderLocked()
}

func (m *CertPrincipalMapper) dropFingerprintCache(fingerprint string) {
	if len(m.cache) == 0 {
		return
	}
	for key := range m.cache {
		if key.fingerprint == fingerprint {
			delete(m.cache, key)
		}
	}
	m.compactCacheOrderLocked()
}

func (m *CertPrincipalMapper) compactCacheOrderLocked() {
	next := m.cacheOrder[:0]
	for _, key := range m.cacheOrder {
		if _, exists := m.cache[key]; exists {
			next = append(next, key)
		}
	}
	m.cacheOrder = next
}

func badSnapshot(err error) error {
	return ErrBadSnapshot
}
