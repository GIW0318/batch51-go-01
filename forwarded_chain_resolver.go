// Package task resolves the real client address behind a proxy chain.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Forwarding header families the resolver understands.
const (
	HeaderForwarded = "forwarded"
	HeaderXFF       = "x-forwarded-for"
)

// Hop roles recorded in an explanation trace.
const (
	RoleClient  = "client"
	RoleProxy   = "proxy"
	RoleIgnored = "ignored"
)

// Reasons attached to a resolution result.
const (
	ReasonDirect        = "direct"
	ReasonStripped      = "stripped_trusted_hops"
	ReasonUntrustedPeer = "untrusted_peer"
	ReasonCached        = "cached"
)

// Errors returned by the resolver.
var (
	ErrBadCIDR        = errors.New("cidr is not parseable")
	ErrBadAddress     = errors.New("forwarding header address is not parseable")
	ErrEmptyElement   = errors.New("forwarding header contains an empty element")
	ErrObfuscated     = errors.New("obfuscated forwarding identifier is not allowed")
	ErrUnknownToken   = errors.New("forwarding header contains an unknown token")
	ErrHeaderTooLarge = errors.New("forwarding header exceeds the configured size")
	ErrChainTooLong   = errors.New("forwarding chain exceeds the configured length")
	ErrHeaderTooLong  = ErrHeaderTooLarge
	ErrBadPolicy      = errors.New("forwarding header family is not recognised")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// TrustConfig bounds the parser and the result cache.
type TrustConfig struct {
	Header          string
	MaxChain        int
	MaxHeaderBytes  int
	MaxCacheEntries int
	AllowObfuscated bool
}

// ClientInfo is the resolved origin of a request.
type ClientInfo struct {
	Client   netip.Addr `json:"client"`
	Proto    string     `json:"proto"`
	Listener string     `json:"listener"`
	Hops     int        `json:"hops"`
	Header   string     `json:"header"`
	Cached   bool       `json:"cached"`
}

// Hop is one element of the forwarding chain.
type Hop struct {
	Index      int        `json:"index"`
	Raw        string     `json:"raw"`
	Addr       netip.Addr `json:"addr"`
	Trusted    bool       `json:"trusted"`
	Role       string     `json:"role"`
	Obfuscated bool       `json:"obfuscated"`
}

// Trace explains how a chain was resolved.
type Trace struct {
	Listener string     `json:"listener"`
	Header   string     `json:"header"`
	Hops     []Hop      `json:"hops"`
	Client   netip.Addr `json:"client"`
	Proto    string     `json:"proto"`
	Reason   string     `json:"reason"`
	Conflict bool       `json:"conflict"`
}

// CacheStats exposes the result cache counters.
type CacheStats struct {
	Entries int `json:"entries"`
	Hits    int `json:"hits"`
	Misses  int `json:"misses"`
}

// ResolverStats exposes the request counters.
type ResolverStats struct {
	Requests      int `json:"requests"`
	Rejected      int `json:"rejected"`
	Conflicts     int `json:"conflicts"`
	Invalidations int `json:"invalidations"`
}

// ForwardedChainResolver owns the per listener trust table and result cache.
type ForwardedChainResolver struct {
	mu sync.Mutex

	cfg           TrustConfig
	defaultHeader string

	trusted  map[string][]netip.Prefix
	policies map[string]string

	cache      map[cacheKey]cachedResolution
	cacheOrder []cacheKey
	cacheHits  int
	cacheMiss  int

	stats ResolverStats
}

type cacheKey struct {
	listener string
	remote   string
	headers  [sha256.Size]byte
}

type cachedResolution struct {
	info  ClientInfo
	trace Trace
}

type parsedHop struct {
	raw        string
	addr       netip.Addr
	proto      string
	obfuscated bool
}

type resolvedChain struct {
	client   netip.Addr
	proto    string
	reason   string
	hops     []Hop
	hasValue bool
}

const snapshotVersion = 1

type snapshotPayload struct {
	Version       int                 `json:"version"`
	DefaultHeader string              `json:"default_header"`
	Trust         map[string][]string `json:"trust"`
	Policies      map[string]string   `json:"policies"`
	Stats         ResolverStats       `json:"stats"`
}

// NewForwardedChainResolver builds a resolver with the given policy.
func NewForwardedChainResolver(cfg TrustConfig) *ForwardedChainResolver {
	header, err := normalizeHeaderName(cfg.Header)
	if err != nil {
		header = HeaderXFF
	}
	return &ForwardedChainResolver{
		cfg:           cfg,
		defaultHeader: header,
		trusted:       make(map[string][]netip.Prefix),
		policies:      make(map[string]string),
		cache:         make(map[cacheKey]cachedResolution),
	}
}

// TrustCIDR adds one trusted proxy network to a listener.
func (g *ForwardedChainResolver) TrustCIDR(listener, cidr string) error {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadCIDR, err)
	}
	prefix = prefix.Masked()

	g.mu.Lock()
	defer g.mu.Unlock()

	for _, existing := range g.trusted[listener] {
		if existing == prefix {
			return nil
		}
	}
	g.trusted[listener] = append(g.trusted[listener], prefix)
	g.invalidateCacheLocked()
	return nil
}

// Resolve walks the selected forwarding header from right to left and returns
// the first address that is not a trusted proxy.
func (g *ForwardedChainResolver) Resolve(remote netip.AddrPort, hdr http.Header, listener string) (ClientInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	info, _, err := g.resolveLocked(remote, hdr, listener)
	return info, err
}

// Explain returns the same decision as Resolve together with every hop.
func (g *ForwardedChainResolver) Explain(listener string, hdr http.Header) (Trace, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.stats.Requests++
	header, err := g.headerForListenerLocked(listener)
	if err != nil {
		g.stats.Rejected++
		return Trace{}, err
	}

	chain, present, err := parseHeaderChain(header, hdr, g.cfg)
	if err != nil {
		g.stats.Rejected++
		return Trace{}, err
	}
	if !present || len(chain) == 0 {
		return Trace{
			Listener: listener,
			Header:   header,
			Proto:    "http",
			Reason:   ReasonDirect,
		}, nil
	}

	resolved := g.resolveChainLocked(listener, chain, netip.Addr{})
	trace := Trace{
		Listener: listener,
		Header:   header,
		Hops:     resolved.hops,
		Client:   resolved.client,
		Proto:    resolved.proto,
		Reason:   resolved.reason,
	}
	other := HeaderForwarded
	if header == HeaderForwarded {
		other = HeaderXFF
	}
	if otherChain, otherPresent, otherErr := parseHeaderChain(other, hdr, g.cfg); otherErr == nil && otherPresent && len(otherChain) > 0 {
		otherResolved := g.resolveChainLocked(listener, otherChain, netip.Addr{})
		if otherResolved.hasValue && resolved.hasValue && otherResolved.client != resolved.client {
			trace.Conflict = true
			g.stats.Conflicts++
		}
	}
	return trace, nil
}

// Trusted reports whether an address belongs to a trusted network of a
// listener.
func (g *ForwardedChainResolver) Trusted(listener string, addr netip.Addr) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.trustedLocked(listener, addr)
}

// Listeners lists the listeners that have at least one trusted network.
func (g *ForwardedChainResolver) Listeners() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	listeners := make([]string, 0, len(g.trusted))
	for listener, prefixes := range g.trusted {
		if len(prefixes) > 0 {
			listeners = append(listeners, listener)
		}
	}
	sort.Strings(listeners)
	return listeners
}

// SetHeaderPolicy changes which forwarding header family a listener honours.
func (g *ForwardedChainResolver) SetHeaderPolicy(listener, header string) error {
	normalized, err := normalizeHeaderName(header)
	if err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	old, hadOverride := g.policies[listener]
	g.policies[listener] = normalized
	oldHeader := g.defaultHeader
	if hadOverride {
		oldHeader = old
	}
	if oldHeader != normalized {
		g.invalidateCacheLocked()
	}
	return nil
}

// Invalidate drops the result cache and reports how many entries were removed.
func (g *ForwardedChainResolver) Invalidate() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	removed := len(g.cache)
	g.invalidateCacheLocked()
	return removed
}

// CacheStats reports the result cache counters.
func (g *ForwardedChainResolver) CacheStats() CacheStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return CacheStats{
		Entries: len(g.cache),
		Hits:    g.cacheHits,
		Misses:  g.cacheMiss,
	}
}

// Stats reports the request counters.
func (g *ForwardedChainResolver) Stats() ResolverStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// Snapshot serialises the trust table and the header policies.
func (g *ForwardedChainResolver) Snapshot() ([]byte, error) {
	g.mu.Lock()
	payload := snapshotPayload{
		Version:       snapshotVersion,
		DefaultHeader: g.defaultHeader,
		Trust:         make(map[string][]string, len(g.trusted)),
		Policies:      make(map[string]string, len(g.policies)),
		Stats:         g.stats,
	}
	listeners := make([]string, 0, len(g.trusted))
	for listener := range g.trusted {
		listeners = append(listeners, listener)
	}
	sort.Strings(listeners)
	for _, listener := range listeners {
		prefixes := g.trusted[listener]
		encoded := make([]string, 0, len(prefixes))
		for _, prefix := range prefixes {
			encoded = append(encoded, prefix.String())
		}
		sort.Strings(encoded)
		payload.Trust[listener] = encoded
	}
	for listener, policy := range g.policies {
		payload.Policies[listener] = policy
	}
	g.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the trust table with a snapshot, all or nothing.
func (g *ForwardedChainResolver) Restore(data []byte) error {
	payload, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	trusted := make(map[string][]netip.Prefix, len(payload.Trust))
	for listener, encoded := range payload.Trust {
		prefixes := make([]netip.Prefix, 0, len(encoded))
		seen := make(map[netip.Prefix]struct{}, len(encoded))
		for _, raw := range encoded {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err != nil {
				return fmt.Errorf("%w: invalid cidr %q", ErrBadSnapshot, raw)
			}
			prefix = prefix.Masked()
			if _, exists := seen[prefix]; exists {
				continue
			}
			seen[prefix] = struct{}{}
			prefixes = append(prefixes, prefix)
		}
		if len(prefixes) > 0 {
			trusted[listener] = prefixes
		}
	}
	policies := make(map[string]string, len(payload.Policies))
	for listener, raw := range payload.Policies {
		policy, err := normalizeHeaderName(raw)
		if err != nil {
			return fmt.Errorf("%w: invalid header policy %q", ErrBadSnapshot, raw)
		}
		policies[listener] = policy
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.defaultHeader = payload.DefaultHeader
	g.trusted = trusted
	g.policies = policies
	g.cache = make(map[cacheKey]cachedResolution)
	g.cacheOrder = nil
	g.stats = payload.Stats
	return nil
}

func (g *ForwardedChainResolver) resolveLocked(remote netip.AddrPort, hdr http.Header, listener string) (ClientInfo, Trace, error) {
	g.stats.Requests++

	normalizedRemote, err := normalizeRemote(remote)
	if err != nil {
		g.stats.Rejected++
		return ClientInfo{}, Trace{}, err
	}
	header, err := g.headerForListenerLocked(listener)
	if err != nil {
		g.stats.Rejected++
		return ClientInfo{}, Trace{}, err
	}

	key := cacheKey{
		listener: listener,
		remote:   normalizedRemote.Addr().String(),
		headers:  fingerprintHeaders(hdr),
	}
	if cached, ok := g.cache[key]; ok {
		g.cacheHits++
		if cached.trace.Conflict {
			g.stats.Conflicts++
		}
		info := cached.info
		info.Cached = true
		trace := cloneTrace(cached.trace)
		trace.Reason = ReasonCached
		return info, trace, nil
	}
	g.cacheMiss++

	info, trace, err := g.resolveUncachedLocked(normalizedRemote, hdr, listener, header)
	if err != nil {
		g.stats.Rejected++
		return ClientInfo{}, Trace{}, err
	}
	if trace.Conflict {
		g.stats.Conflicts++
	}
	g.storeCacheLocked(key, info, trace)
	return info, trace, nil
}

func (g *ForwardedChainResolver) resolveUncachedLocked(remote netip.AddrPort, hdr http.Header, listener, header string) (ClientInfo, Trace, error) {
	direct := func(reason string, chain []parsedHop) (ClientInfo, Trace, error) {
		info := ClientInfo{
			Client:   remote.Addr(),
			Proto:    "http",
			Listener: listener,
			Hops:     len(chain),
			Header:   header,
		}
		trace := Trace{
			Listener: listener,
			Header:   header,
			Client:   remote.Addr(),
			Proto:    "http",
			Reason:   reason,
			Hops:     g.explainChainLocked(listener, chain, -1),
		}
		return info, trace, nil
	}

	if !g.trustedLocked(listener, remote.Addr()) {
		return direct(ReasonUntrustedPeer, nil)
	}

	chain, present, err := parseHeaderChain(header, hdr, g.cfg)
	if err != nil {
		return ClientInfo{}, Trace{}, err
	}
	if !present || len(chain) == 0 {
		return direct(ReasonDirect, nil)
	}

	resolved := g.resolveChainLocked(listener, chain, remote.Addr())
	info := ClientInfo{
		Client:   resolved.client,
		Proto:    resolved.proto,
		Listener: listener,
		Hops:     len(chain),
		Header:   header,
	}
	trace := Trace{
		Listener: listener,
		Header:   header,
		Hops:     resolved.hops,
		Client:   resolved.client,
		Proto:    resolved.proto,
		Reason:   resolved.reason,
	}

	other := HeaderForwarded
	if header == HeaderForwarded {
		other = HeaderXFF
	}
	if otherChain, otherPresent, otherErr := parseHeaderChain(other, hdr, g.cfg); otherErr == nil && otherPresent && len(otherChain) > 0 {
		otherResolved := g.resolveChainLocked(listener, otherChain, remote.Addr())
		if otherResolved.hasValue && resolved.hasValue && otherResolved.client != resolved.client {
			trace.Conflict = true
		}
	}

	return info, trace, nil
}

func (g *ForwardedChainResolver) resolveChainLocked(listener string, chain []parsedHop, fallback netip.Addr) resolvedChain {
	clientIndex := -1
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].obfuscated || !chain[i].addr.IsValid() || !g.trustedLocked(listener, chain[i].addr) {
			clientIndex = i
			break
		}
	}

	result := resolvedChain{
		client: fallback,
		proto:  "http",
		reason: ReasonStripped,
	}
	if clientIndex >= 0 {
		result.client = chain[clientIndex].addr
		if chain[clientIndex].proto != "" {
			result.proto = chain[clientIndex].proto
		}
	}
	result.hasValue = result.client.IsValid()
	result.hops = g.explainChainLocked(listener, chain, clientIndex)
	return result
}

func (g *ForwardedChainResolver) explainChainLocked(listener string, chain []parsedHop, clientIndex int) []Hop {
	if len(chain) == 0 {
		return nil
	}
	hops := make([]Hop, len(chain))
	for i, hop := range chain {
		trusted := g.trustedLocked(listener, hop.addr)
		role := ""
		switch {
		case clientIndex < 0:
			role = RoleProxy
		case i < clientIndex:
			role = RoleIgnored
		case i == clientIndex:
			role = RoleClient
		default:
			if trusted {
				role = RoleProxy
			} else {
				role = RoleIgnored
			}
		}
		hops[i] = Hop{
			Index:      i,
			Raw:        hop.raw,
			Addr:       hop.addr,
			Trusted:    trusted,
			Role:       role,
			Obfuscated: hop.obfuscated,
		}
	}
	return hops
}

func (g *ForwardedChainResolver) headerForListenerLocked(listener string) (string, error) {
	if policy, ok := g.policies[listener]; ok {
		return normalizeHeaderName(policy)
	}
	return normalizeHeaderName(g.defaultHeader)
}

func (g *ForwardedChainResolver) trustedLocked(listener string, addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range g.trusted[listener] {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (g *ForwardedChainResolver) invalidateCacheLocked() {
	g.cache = make(map[cacheKey]cachedResolution)
	g.cacheOrder = nil
	g.stats.Invalidations++
}

func (g *ForwardedChainResolver) storeCacheLocked(key cacheKey, info ClientInfo, trace Trace) {
	if g.cfg.MaxCacheEntries <= 0 {
		return
	}
	if _, exists := g.cache[key]; exists {
		return
	}
	for len(g.cache) >= g.cfg.MaxCacheEntries && len(g.cacheOrder) > 0 {
		oldest := g.cacheOrder[0]
		g.cacheOrder = g.cacheOrder[1:]
		delete(g.cache, oldest)
	}
	info.Cached = false
	g.cache[key] = cachedResolution{info: info, trace: cloneTrace(trace)}
	g.cacheOrder = append(g.cacheOrder, key)
}

func cloneTrace(trace Trace) Trace {
	copy := trace
	if trace.Hops != nil {
		copy.Hops = append([]Hop(nil), trace.Hops...)
	}
	return copy
}

func normalizeHeaderName(header string) (string, error) {
	header = strings.ToLower(strings.TrimSpace(header))
	switch header {
	case HeaderForwarded, HeaderXFF:
		return header, nil
	default:
		return "", ErrBadPolicy
	}
}

func normalizeRemote(remote netip.AddrPort) (netip.AddrPort, error) {
	if !remote.IsValid() {
		return netip.AddrPort{}, ErrBadAddress
	}
	return netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port()), nil
}

func fingerprintHeaders(hdr http.Header) [sha256.Size]byte {
	hash := sha256.New()
	for _, name := range []string{HeaderForwarded, HeaderXFF} {
		values, present := headerValues(hdr, name)
		if present {
			hash.Write([]byte{1})
		} else {
			hash.Write([]byte{0})
		}
		for _, value := range values {
			hash.Write([]byte(strconv.Itoa(len(value))))
			hash.Write([]byte{':'})
			hash.Write([]byte(value))
			hash.Write([]byte{0})
		}
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func headerValues(hdr http.Header, name string) ([]string, bool) {
	if hdr == nil {
		return nil, false
	}
	keys := make([]string, 0)
	for key := range hdr {
		if strings.EqualFold(key, name) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, false
	}
	sort.Strings(keys)
	var values []string
	for _, key := range keys {
		values = append(values, hdr[key]...)
	}
	return values, true
}

func parseHeaderChain(header string, hdr http.Header, cfg TrustConfig) ([]parsedHop, bool, error) {
	values, present := headerValues(hdr, header)
	if !present {
		return nil, false, nil
	}

	total := 0
	for _, value := range values {
		total += len(value)
	}
	if total > cfg.MaxHeaderBytes {
		return nil, true, ErrHeaderTooLarge
	}

	combined := strings.Join(values, ",")
	var elements []string
	if header == HeaderXFF {
		elements = strings.Split(combined, ",")
	} else {
		elements = splitOutsideQuotes(combined, ',')
	}
	if len(elements) > cfg.MaxChain {
		return nil, true, ErrChainTooLong
	}
	for _, element := range elements {
		if strings.TrimSpace(element) == "" {
			return nil, true, ErrEmptyElement
		}
	}

	chain := make([]parsedHop, 0, len(elements))
	for _, element := range elements {
		var (
			hop parsedHop
			err error
		)
		if header == HeaderXFF {
			hop, err = parseXFFElement(element, cfg.AllowObfuscated)
		} else {
			hop, err = parseForwardedElement(element, cfg.AllowObfuscated)
		}
		if err != nil {
			return nil, true, err
		}
		chain = append(chain, hop)
	}
	return chain, true, nil
}

func parseXFFElement(element string, allowObfuscated bool) (parsedHop, error) {
	raw := strings.TrimSpace(element)
	if raw == "" {
		return parsedHop{}, ErrEmptyElement
	}
	if strings.HasPrefix(raw, "_") {
		if !allowObfuscated {
			return parsedHop{}, ErrObfuscated
		}
		return parsedHop{raw: raw, obfuscated: true}, nil
	}
	addr, err := normalizeAddressToken(raw)
	if err != nil {
		if looksLikeAddress(raw) {
			return parsedHop{}, ErrBadAddress
		}
		return parsedHop{}, ErrUnknownToken
	}
	return parsedHop{raw: raw, addr: addr}, nil
}

func parseForwardedElement(element string, allowObfuscated bool) (parsedHop, error) {
	raw := strings.TrimSpace(element)
	if raw == "" {
		return parsedHop{}, ErrEmptyElement
	}

	parts := splitOutsideQuotes(raw, ';')
	var (
		forValue  string
		proto     string
		haveFor   bool
		haveProto bool
	)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return parsedHop{}, ErrUnknownToken
		}
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			return parsedHop{}, ErrUnknownToken
		}
		key := strings.ToLower(strings.TrimSpace(part[:eq]))
		value := strings.TrimSpace(part[eq+1:])
		switch key {
		case "for":
			if haveFor {
				return parsedHop{}, ErrUnknownToken
			}
			unquoted, err := unquoteForwardedValue(value)
			if err != nil {
				return parsedHop{}, ErrBadAddress
			}
			forValue = unquoted
			haveFor = true
		case "proto":
			if haveProto {
				return parsedHop{}, ErrUnknownToken
			}
			unquoted, err := unquoteForwardedValue(value)
			if err != nil {
				return parsedHop{}, ErrUnknownToken
			}
			proto = unquoted
			haveProto = true
		default:
			return parsedHop{}, ErrUnknownToken
		}
	}
	if !haveFor {
		return parsedHop{}, ErrUnknownToken
	}

	if strings.HasPrefix(forValue, "_") {
		if !allowObfuscated {
			return parsedHop{}, ErrObfuscated
		}
		return parsedHop{raw: raw, proto: proto, obfuscated: true}, nil
	}
	addr, err := normalizeAddressToken(forValue)
	if err != nil {
		if looksLikeAddress(forValue) {
			return parsedHop{}, ErrBadAddress
		}
		return parsedHop{}, ErrUnknownToken
	}
	return parsedHop{raw: raw, addr: addr, proto: proto}, nil
}

func unquoteForwardedValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] != '"' {
		return value, nil
	}
	unquoted, err := strconv.Unquote(value)
	if err != nil {
		return "", err
	}
	return unquoted, nil
}

func normalizeAddressToken(raw string) (netip.Addr, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return netip.Addr{}, ErrBadAddress
	}

	if strings.HasPrefix(value, "[") {
		close := strings.IndexByte(value, ']')
		if close < 0 {
			return netip.Addr{}, ErrBadAddress
		}
		inside := value[1:close]
		rest := value[close+1:]
		if rest == "" {
			addr, err := netip.ParseAddr(inside)
			if err != nil {
				return netip.Addr{}, ErrBadAddress
			}
			return addr.Unmap(), nil
		}
		if !strings.HasPrefix(rest, ":") || len(rest) == 1 {
			return netip.Addr{}, ErrBadAddress
		}
		addrPort, err := netip.ParseAddrPort(value)
		if err != nil {
			return netip.Addr{}, ErrBadAddress
		}
		return addrPort.Addr().Unmap(), nil
	}

	if addr, err := netip.ParseAddr(value); err == nil {
		return addr.Unmap(), nil
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		return addrPort.Addr().Unmap(), nil
	}
	return netip.Addr{}, ErrBadAddress
}

func looksLikeAddress(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			return true
		case r == '.', r == ':', r == '[', r == ']', r == '%':
			return true
		}
	}
	return false
}

func splitOutsideQuotes(value string, separator byte) []string {
	parts := make([]string, 0, 1)
	start := 0
	inQuotes := false
	escaped := false
	for i := 0; i < len(value); i++ {
		switch {
		case escaped:
			escaped = false
		case value[i] == '\\' && inQuotes:
			escaped = true
		case value[i] == '"':
			inQuotes = !inQuotes
		case value[i] == separator && !inQuotes:
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	parts = append(parts, value[start:])
	return parts
}

func decodeSnapshot(data []byte) (snapshotPayload, error) {
	var payload snapshotPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return snapshotPayload{}, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return snapshotPayload{}, fmt.Errorf("%w: trailing data", ErrBadSnapshot)
		}
		return snapshotPayload{}, fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if payload.Version != snapshotVersion {
		return snapshotPayload{}, fmt.Errorf("%w: unsupported version %d", ErrBadSnapshot, payload.Version)
	}
	header, err := normalizeHeaderName(payload.DefaultHeader)
	if err != nil {
		return snapshotPayload{}, fmt.Errorf("%w: invalid default header", ErrBadSnapshot)
	}
	payload.DefaultHeader = header
	if payload.Trust == nil || payload.Policies == nil {
		return snapshotPayload{}, fmt.Errorf("%w: missing state", ErrBadSnapshot)
	}
	if payload.Stats.Requests < 0 || payload.Stats.Rejected < 0 || payload.Stats.Conflicts < 0 || payload.Stats.Invalidations < 0 {
		return snapshotPayload{}, fmt.Errorf("%w: negative counters", ErrBadSnapshot)
	}
	return payload, nil
}
