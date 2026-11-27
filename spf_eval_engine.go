// Package task evaluates inbound sender policy framework records for one
// message, following the mechanism chain with a bounded DNS budget.
package task

import (
	"errors"
	"net/netip"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Evaluation results.
const (
	ResultPass      = "pass"
	ResultFail      = "fail"
	ResultSoftfail  = "softfail"
	ResultNeutral   = "neutral"
	ResultNone      = "none"
	ResultPermError = "permerror"
	ResultTempError = "temperror"
)

// Mechanism qualifiers.
const (
	QualifierPass     = "+"
	QualifierFail     = "-"
	QualifierSoftfail = "~"
	QualifierNeutral  = "?"
)

// Mechanism types.
const (
	MechAll      = "all"
	MechInclude  = "include"
	MechA        = "a"
	MechMX       = "mx"
	MechPTR      = "ptr"
	MechExists   = "exists"
	MechIP4      = "ip4"
	MechIP6      = "ip6"
	MechRedirect = "redirect"
)

// Errors returned by the engine.
var (
	ErrBadRequest  = errors.New("address, sender and domain are required")
	ErrNoResolver  = errors.New("resolver is required")
	ErrNoRecord    = errors.New("domain has no spf record")
	ErrBadRecord   = errors.New("spf record is malformed")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Mechanism is one parsed term of a record.
type Mechanism struct {
	Qualifier string `json:"qualifier"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	CIDR      int    `json:"cidr"`
}

// Record is one parsed SPF record.
type Record struct {
	Raw        string      `json:"raw"`
	Domain     string      `json:"domain"`
	Mechanisms []Mechanism `json:"mechanisms"`
	Redirect   *Mechanism  `json:"redirect"`
}

// SpfResult is the outcome of one evaluation.
type SpfResult struct {
	Result    string     `json:"result"`
	IP        netip.Addr `json:"ip"`
	Sender    string     `json:"sender"`
	Domain    string     `json:"domain"`
	Mechanism string     `json:"mechanism"`
	Qualifier string     `json:"qualifier"`
	Record    string     `json:"record"`
	Queries   int        `json:"queries"`
	Void      int        `json:"void"`
	Cached    bool       `json:"cached"`
}

// TraceStep is one evaluated or parsed mechanism in a trace.
type TraceStep struct {
	Mechanism string `json:"mechanism"`
	Domain    string `json:"domain"`
	Query     string `json:"query"`
	Matched   bool   `json:"matched"`
	Result    string `json:"result"`
	Detail    string `json:"detail"`
}

// Trace is the explainable view of one record.
type Trace struct {
	Domain  string      `json:"domain"`
	Record  string      `json:"record"`
	Steps   []TraceStep `json:"steps"`
	Result  string      `json:"result"`
	Queries int         `json:"queries"`
	Void    int         `json:"void"`
}

// Resolver is the DNS surface the engine is allowed to use.
type Resolver interface {
	TXT(name string) ([]string, error)
	A(name string) ([]netip.Addr, error)
	MX(name string) ([]string, error)
	PTR(addr netip.Addr) ([]string, error)
}

// SpfConfig bounds the query budget, the recursion and the result cache.
type SpfConfig struct {
	MaxDNSQueries   int
	MaxVoidLookups  int
	MaxRecursion    int
	MaxMechanisms   int
	CacheTTL        time.Duration
	MaxCacheEntries int
	Now             func() time.Time
}

// Stats is a copy of the engine counters.
type Stats struct {
	Cache        int `json:"cache"`
	Hits         int `json:"hits"`
	Evaluations  int `json:"evaluations"`
	Singleflight int `json:"singleflight"`
}

// SpfEvalEngine owns the resolver, the record cache and the result cache.
type SpfEvalEngine struct {
	cfg SpfConfig
}

// NewSpfEvalEngine builds an engine with the given configuration.
func NewSpfEvalEngine(cfg SpfConfig) *SpfEvalEngine {
	return &SpfEvalEngine{cfg: cfg}
}

// SetResolver installs the DNS surface used by later evaluations.
func (e *SpfEvalEngine) SetResolver(r Resolver) error {
	return errNotImplemented
}

// Evaluate walks the record of domain for one sender address.
func (e *SpfEvalEngine) Evaluate(ip netip.Addr, sender, domain string) (SpfResult, error) {
	return SpfResult{}, errNotImplemented
}

// Explain returns the parsed mechanism chain of one domain.
func (e *SpfEvalEngine) Explain(domain string) (Trace, error) {
	return Trace{}, errNotImplemented
}

// ResolveRecord fetches and parses the record of one domain.
func (e *SpfEvalEngine) ResolveRecord(domain string) (Record, error) {
	return Record{}, errNotImplemented
}

// Record returns the cached raw record of one domain.
func (e *SpfEvalEngine) Record(domain string) (string, bool) {
	return "", false
}

// Expire drops cache entries whose ttl has elapsed.
func (e *SpfEvalEngine) Expire(now time.Time) int {
	return 0
}

// Stats returns the live counters.
func (e *SpfEvalEngine) Stats() Stats {
	return Stats{}
}

// Snapshot serialises the caches and the counters.
func (e *SpfEvalEngine) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (e *SpfEvalEngine) Restore(data []byte) error {
	return errNotImplemented
}
