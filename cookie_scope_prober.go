// Package task grades whether cookies issued by a site can be overwritten by
// a sibling subdomain, taking domain, path, prefix and SameSite into account.
package task

import (
	"errors"
	"sync"
)

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Cookie name prefixes with reserved meaning.
const (
	PrefixHost   = "__Host-"
	PrefixSecure = "__Secure-"
)

// SameSite modes.
const (
	SameSiteStrict = "strict"
	SameSiteLax    = "lax"
	SameSiteNone   = "none"
)

// Result reasons reported by Set and Judge.
const (
	ReasonStored         = "reason_stored"
	ReasonDeleted        = "reason_deleted"
	ReasonPrefixViolated = "reason_prefix_violated"
	ReasonEvicted        = "reason_evicted"
	ReasonHostOnly       = "reason_host_only"
	ReasonParentDomain   = "reason_parent_domain"
	ReasonAmbiguousScope = "reason_ambiguous_scope"
	PrefixInvalid        = "invalid"
)

// Errors reported by the prober.
var (
	ErrBadHost          = errors.New("host is empty or malformed")
	ErrBadName          = errors.New("cookie name is empty")
	ErrBadValue         = errors.New("cookie value must be a gsb marker")
	ErrBadPath          = errors.New("cookie path must start with a slash")
	ErrDomainMismatch   = errors.New("cookie domain does not cover the host")
	ErrInsecureSameSite = errors.New("samesite none requires the secure flag")
	ErrBadSameSite      = errors.New("samesite mode is not supported")
	ErrBudget           = errors.New("operation budget is exhausted")
	ErrNoMatches        = errors.New("no cookie matches were supplied")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// CookieSpec is one cookie attribute set that is evaluated before it is stored.
// HostOnly is filled in by the prober for cookies that carry no Domain.
type CookieSpec struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Secure   bool   `json:"secure"`
	HTTPOnly bool   `json:"http_only"`
	SameSite string `json:"same_site"`
	MaxAge   int    `json:"max_age"`
	HostOnly bool   `json:"host_only"`
}

// SetResult reports what the jar did with one cookie.
type SetResult struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason"`
	Prefix   string `json:"prefix"`
	StoreID  string `json:"store_id"`
	Evicted  int    `json:"evicted"`
}

// Request is the incoming request the matcher is asked about.
type Request struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	Secure   bool   `json:"secure"`
	Method   string `json:"method"`
	TopLevel bool   `json:"top_level"`
}

// CookieMatch is one cookie that would be sent for the request.
type CookieMatch struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Domain      string `json:"domain"`
	Path        string `json:"path"`
	HostOnly    bool   `json:"host_only"`
	SameSite    string `json:"same_site"`
	RequestHost string `json:"request_host"`
	RequestPath string `json:"request_path"`
	SendOrder   int    `json:"send_order"`
}

// ScopeVerdict is the explainable conclusion for one request target.
type ScopeVerdict struct {
	Host        string   `json:"host"`
	Path        string   `json:"path"`
	Overridable bool     `json:"overridable"`
	Reason      string   `json:"reason"`
	Winners     []string `json:"winners"`
}

// CookieJar is the set of cookies stored while acting on one host.
type CookieJar struct {
	Host    string       `json:"host"`
	Cookies []CookieSpec `json:"cookies"`
}

// Limits bounds the prober.
type Limits struct {
	MaxOps     int
	MaxPerHost int
}

// BudgetState reports how much probing room is left.
type BudgetState struct {
	Ops       int `json:"ops"`
	MaxOps    int `json:"max_ops"`
	Remaining int `json:"remaining"`
	Cookies   int `json:"cookies"`
	Verdicts  int `json:"verdicts"`
}

// CookieScopeProber owns the cookie registry and the judged combinations.
type CookieScopeProber struct {
	mu sync.Mutex
}

// NewCookieScopeProber builds an empty prober.
func NewCookieScopeProber(limits Limits) *CookieScopeProber {
	_ = limits
	return &CookieScopeProber{}
}

// Jar returns the cookies stored while acting on one host.
func (p *CookieScopeProber) Jar(host string) (CookieJar, error) {
	return CookieJar{}, errNotImplemented
}

// Set evaluates and stores one cookie for a host.
func (p *CookieScopeProber) Set(host string, c CookieSpec) (SetResult, error) {
	return SetResult{}, errNotImplemented
}

// Delete removes one cookie from a host jar.
func (p *CookieScopeProber) Delete(host, name, path string) error {
	return errNotImplemented
}

// Match returns the cookies that would be sent for one request.
func (p *CookieScopeProber) Match(req Request) ([]CookieMatch, error) {
	return nil, errNotImplemented
}

// Judge grades one match set and records the conclusion per host and path.
func (p *CookieScopeProber) Judge(matches []CookieMatch) (ScopeVerdict, error) {
	return ScopeVerdict{}, errNotImplemented
}

// Verdict reads the recorded conclusion of one host and path.
func (p *CookieScopeProber) Verdict(host, path string) (ScopeVerdict, bool) {
	return ScopeVerdict{}, false
}

// Verdicts lists the recorded combinations, sorted.
func (p *CookieScopeProber) Verdicts() []string {
	return nil
}

// Budget reports the operation budget and the stored sizes.
func (p *CookieScopeProber) Budget() (BudgetState, error) {
	return BudgetState{}, errNotImplemented
}

// Snapshot serialises the cookie registry and the judged combinations.
func (p *CookieScopeProber) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *CookieScopeProber) Restore(data []byte) error {
	return errNotImplemented
}
