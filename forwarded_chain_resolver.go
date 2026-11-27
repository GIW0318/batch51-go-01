// Package task resolves the real client address behind a proxy chain.
package task

import (
	"errors"
	"net/http"
	"net/netip"
	"sync"
)

// errNotImplemented marks behaviour that has not been wired up yet.
var errNotImplemented = errors.New("not implemented")

// Forwarding header families the resolver understands.
const (
	HeaderForwarded = "forwarded"
	HeaderXFF       = "x-forwarded-for"
)

// Hop roles recorded in an explanation trace.
const (
	RoleClient = "client"
	RoleProxy  = "proxy"
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
	ErrBadCIDR       = errors.New("cidr is not parseable")
	ErrBadAddress    = errors.New("forwarding header address is not parseable")
	ErrEmptyElement  = errors.New("forwarding header contains an empty element")
	ErrObfuscated    = errors.New("obfuscated forwarding identifier is not allowed")
	ErrUnknownToken  = errors.New("forwarding header contains an unknown token")
	ErrChainTooLong  = errors.New("forwarding chain exceeds the configured length")
	ErrHeaderTooLong = errors.New("forwarding header exceeds the configured size")
	ErrBadPolicy     = errors.New("forwarding header family is not recognised")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
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
	mu  sync.Mutex
	cfg TrustConfig
}

// NewForwardedChainResolver builds a resolver with the given policy.
func NewForwardedChainResolver(cfg TrustConfig) *ForwardedChainResolver {
	return &ForwardedChainResolver{cfg: cfg}
}

// TrustCIDR adds one trusted proxy network to a listener.
func (g *ForwardedChainResolver) TrustCIDR(listener, cidr string) error {
	return errNotImplemented
}

// Resolve walks the selected forwarding header from right to left and returns
// the first address that is not a trusted proxy.
func (g *ForwardedChainResolver) Resolve(remote netip.AddrPort, hdr http.Header, listener string) (ClientInfo, error) {
	return ClientInfo{}, errNotImplemented
}

// Explain returns the same decision as Resolve together with every hop.
func (g *ForwardedChainResolver) Explain(listener string, hdr http.Header) (Trace, error) {
	return Trace{}, errNotImplemented
}

// Trusted reports whether an address belongs to a trusted network of a
// listener.
func (g *ForwardedChainResolver) Trusted(listener string, addr netip.Addr) bool {
	return false
}

// Listeners lists the listeners that have at least one trusted network.
func (g *ForwardedChainResolver) Listeners() []string {
	return nil
}

// SetHeaderPolicy changes which forwarding header family a listener honours.
func (g *ForwardedChainResolver) SetHeaderPolicy(listener, header string) error {
	return errNotImplemented
}

// Invalidate drops the result cache and reports how many entries were removed.
func (g *ForwardedChainResolver) Invalidate() int {
	return 0
}

// CacheStats reports the result cache counters.
func (g *ForwardedChainResolver) CacheStats() CacheStats {
	return CacheStats{}
}

// Stats reports the request counters.
func (g *ForwardedChainResolver) Stats() ResolverStats {
	return ResolverStats{}
}

// Snapshot serialises the trust table and the header policies.
func (g *ForwardedChainResolver) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the trust table with a snapshot, all or nothing.
func (g *ForwardedChainResolver) Restore(data []byte) error {
	return errNotImplemented
}
