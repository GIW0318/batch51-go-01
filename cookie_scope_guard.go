// Package task governs the cookie container of an edge proxy.
package task

import (
	"errors"
	"sync"
	"time"
)

// errNotImplemented marks behaviour that has not been wired up yet.
var errNotImplemented = errors.New("not implemented")

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

// CookieScopeGuard owns the cookie container and its per domain indexes.
type CookieScopeGuard struct {
	mu  sync.Mutex
	cfg CookiePolicy
}

// NewCookieScopeGuard builds a guard with the given policy.
func NewCookieScopeGuard(cfg CookiePolicy) *CookieScopeGuard {
	return &CookieScopeGuard{cfg: cfg}
}

// Store validates and inserts one cookie for an origin.
func (g *CookieScopeGuard) Store(origin string, c Cookie) error {
	return errNotImplemented
}

// Select returns the cookies that must be sent on a request, longest path
// first and oldest creation first inside one path.
func (g *CookieScopeGuard) Select(origin string, req Request) []Cookie {
	return nil
}

// Delete removes every cookie with this name for the origin host.
func (g *CookieScopeGuard) Delete(origin, name string) error {
	return errNotImplemented
}

// Evict removes cookies whose expiry passed and reports how many were removed.
func (g *CookieScopeGuard) Evict(now time.Time) int {
	return 0
}

// Explain reports the effective scope of one stored cookie by name.
func (g *CookieScopeGuard) Explain(name string) (Scope, error) {
	return Scope{}, errNotImplemented
}

// Cookies lists the cookies visible to an origin host ordered by domain and
// path.
func (g *CookieScopeGuard) Cookies(origin string) []Cookie {
	return nil
}

// Clear removes every cookie visible to an origin host and reports the count.
func (g *CookieScopeGuard) Clear(origin string) int {
	return 0
}

// Audit returns a copy of the audit log, oldest first.
func (g *CookieScopeGuard) Audit() []CookieEvent {
	return nil
}

// Stats reports the current counters.
func (g *CookieScopeGuard) Stats() CookieStats {
	return CookieStats{}
}

// Snapshot serialises the whole container.
func (g *CookieScopeGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the state with a snapshot, all or nothing.
func (g *CookieScopeGuard) Restore(data []byte) error {
	return errNotImplemented
}
