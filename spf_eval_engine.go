// Package task evaluates inbound sender policy framework records for one
// message, following the mechanism chain with a bounded DNS budget.
package task

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

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

var (
	errQueryBudget = errors.New("dns query or void budget exhausted")
	errBadMacro    = errors.New("invalid spf macro expansion")
	errDNS         = errors.New("dns lookup failed")
)

// Mechanism is one parsed term of a record.
type Mechanism struct {
	Qualifier string `json:"qualifier"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	CIDR      int    `json:"cidr"`

	cidrSet bool
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

type recordEntry struct {
	domain  string
	raw     string
	record  Record
	found   bool
	bad     bool
	expires time.Time
	used    time.Time
	elem    *list.Element
}

type resultKey struct {
	ip          string
	sender      string
	domain      string
	fingerprint string
}

type resultEntry struct {
	key     resultKey
	result  SpfResult
	expires time.Time
	used    time.Time
	elem    *list.Element
}

type recordCall struct {
	done       chan struct{}
	record     Record
	raw        string
	found      bool
	err        error
	queryCount int
	void       bool
}

type evalCall struct {
	done   chan struct{}
	result SpfResult
	cached bool
}

// SpfEvalEngine owns the resolver, the record cache and the result cache.
type SpfEvalEngine struct {
	cfg SpfConfig

	mu            sync.Mutex
	resolver      Resolver
	generation    uint64
	records       map[string]*recordEntry
	recordLRU     *list.List
	results       map[resultKey]*resultEntry
	resultLRU     *list.List
	recordFlights map[string]*recordCall
	evalFlights   map[resultKey]*evalCall
	stats         Stats
}

// NewSpfEvalEngine builds an engine with the given configuration.
func NewSpfEvalEngine(cfg SpfConfig) *SpfEvalEngine {
	return &SpfEvalEngine{
		cfg:           cfg,
		records:       make(map[string]*recordEntry),
		recordLRU:     list.New(),
		results:       make(map[resultKey]*resultEntry),
		resultLRU:     list.New(),
		recordFlights: make(map[string]*recordCall),
		evalFlights:   make(map[resultKey]*evalCall),
	}
}

func (e *SpfEvalEngine) ensureStateLocked() {
	if e.records == nil {
		e.records = make(map[string]*recordEntry)
	}
	if e.recordLRU == nil {
		e.recordLRU = list.New()
	}
	if e.results == nil {
		e.results = make(map[resultKey]*resultEntry)
	}
	if e.resultLRU == nil {
		e.resultLRU = list.New()
	}
	if e.recordFlights == nil {
		e.recordFlights = make(map[string]*recordCall)
	}
	if e.evalFlights == nil {
		e.evalFlights = make(map[resultKey]*evalCall)
	}
}

func (e *SpfEvalEngine) now() time.Time {
	if e.cfg.Now != nil {
		return e.cfg.Now()
	}
	return time.Now()
}

// SetResolver installs the DNS surface used by later evaluations.
func (e *SpfEvalEngine) SetResolver(r Resolver) error {
	if r == nil {
		return ErrNoResolver
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensureStateLocked()
	e.resolver = r
	e.generation++
	e.records = make(map[string]*recordEntry)
	e.recordLRU = list.New()
	e.results = make(map[resultKey]*resultEntry)
	e.resultLRU = list.New()
	e.recordFlights = make(map[string]*recordCall)
	e.evalFlights = make(map[resultKey]*evalCall)
	return nil
}

type evalOutcome struct {
	result    string
	mechanism string
	qualifier string
}

type evalContext struct {
	engine              *SpfEvalEngine
	ip                  netip.Addr
	sender              string
	senderLocal         string
	senderDomain        string
	maxQueries          int
	maxVoid             int
	queries             int
	void                int
	singleflightCounted bool
}

func (c *evalContext) reserveQuery() error {
	if c.maxQueries >= 0 && c.queries >= c.maxQueries {
		return errQueryBudget
	}
	c.queries++
	return nil
}

func (c *evalContext) chargeQueries(n int) error {
	if n <= 0 {
		return nil
	}
	if c.maxQueries >= 0 && c.queries+n > c.maxQueries {
		return errQueryBudget
	}
	c.queries += n
	return nil
}

func (c *evalContext) noteVoid() error {
	c.void++
	if c.maxVoid >= 0 && c.void > c.maxVoid {
		return errQueryBudget
	}
	return nil
}

func normalizeDomain(domain string) string {
	domain = strings.TrimSpace(domain)
	domain = strings.TrimSuffix(domain, ".")
	return strings.ToLower(domain)
}

func queryName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

func recordFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func resultCacheSender(raw, sender string) string {
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "%{s}") || strings.Contains(lower, "%{l}") || strings.Contains(lower, "%{o}") {
		return sender
	}
	if strings.Contains(lower, "include:") || strings.Contains(lower, "redirect=") {
		return sender
	}
	return ""
}

func qualifierResult(qualifier string) string {
	switch qualifier {
	case QualifierFail:
		return ResultFail
	case QualifierSoftfail:
		return ResultSoftfail
	case QualifierNeutral:
		return ResultNeutral
	default:
		return ResultPass
	}
}

func senderParts(sender string) (string, string) {
	at := strings.LastIndexByte(sender, '@')
	if at < 0 {
		return "", ""
	}
	return sender[:at], normalizeDomain(sender[at+1:])
}

// Evaluate walks the record of domain for one sender address.
func (e *SpfEvalEngine) Evaluate(ip netip.Addr, sender, domain string) (SpfResult, error) {
	ip = ip.Unmap()
	domain = normalizeDomain(domain)
	if !ip.IsValid() || strings.TrimSpace(sender) == "" || domain == "" {
		return SpfResult{}, ErrBadRequest
	}

	e.mu.Lock()
	e.ensureStateLocked()
	e.stats.Evaluations++
	resolver := e.resolver
	generation := e.generation
	e.mu.Unlock()
	if resolver == nil {
		return SpfResult{}, ErrNoResolver
	}

	local, senderDomain := senderParts(sender)
	ctx := &evalContext{
		engine:       e,
		ip:           ip,
		sender:       sender,
		senderLocal:  local,
		senderDomain: senderDomain,
		maxQueries:   e.cfg.MaxDNSQueries,
		maxVoid:      e.cfg.MaxVoidLookups,
	}

	rec, raw, found, fetchErr := e.fetchRecord(ctx, domain, false)
	if fetchErr != nil && !errors.Is(fetchErr, ErrNoRecord) && !errors.Is(fetchErr, ErrBadRecord) {
		if errors.Is(fetchErr, ErrNoResolver) {
			return SpfResult{}, ErrNoResolver
		}
		res := SpfResult{
			Result:  resultFromFetchError(fetchErr),
			IP:      ip,
			Sender:  sender,
			Domain:  domain,
			Record:  raw,
			Queries: ctx.queries,
			Void:    ctx.void,
		}
		return res, nil
	}

	if !found && errors.Is(fetchErr, ErrNoRecord) {
		raw = ""
	}
	fingerprint := recordFingerprint(raw)
	key := resultKey{ip: ip.String(), sender: resultCacheSender(raw, sender), domain: domain, fingerprint: fingerprint}
	now := e.now()

	e.mu.Lock()
	e.ensureStateLocked()
	if entry, ok := e.getResultLocked(key, now); ok {
		e.stats.Hits++
		result := entry.result
		result.Sender = sender
		result.Cached = true
		e.mu.Unlock()
		return result, nil
	}
	if call, ok := e.evalFlights[key]; ok {
		if !ctx.singleflightCounted {
			e.stats.Singleflight++
			ctx.singleflightCounted = true
		}
		e.mu.Unlock()
		<-call.done
		result := call.result
		result.Sender = sender
		result.Cached = call.cached
		return result, nil
	}
	call := &evalCall{done: make(chan struct{})}
	e.evalFlights[key] = call
	e.mu.Unlock()

	var outcome evalOutcome
	switch {
	case fetchErr == nil:
		outcome = e.evaluateRecord(rec, domain, ctx, 0)
	case errors.Is(fetchErr, ErrNoRecord):
		outcome = evalOutcome{result: ResultNone}
	default:
		outcome = evalOutcome{result: ResultPermError}
	}

	result := SpfResult{
		Result:    outcome.result,
		IP:        ip,
		Sender:    sender,
		Domain:    domain,
		Mechanism: outcome.mechanism,
		Qualifier: outcome.qualifier,
		Record:    raw,
		Queries:   ctx.queries,
		Void:      ctx.void,
	}
	cacheable := resultIsCacheable(result.Result)

	e.mu.Lock()
	call.result = result
	if e.generation == generation && cacheable {
		e.putResultLocked(key, result, now)
		call.cached = true
	}
	if e.evalFlights[key] == call {
		delete(e.evalFlights, key)
	}
	close(call.done)
	e.mu.Unlock()
	return result, nil
}

func resultFromFetchError(err error) string {
	switch {
	case errors.Is(err, ErrNoRecord):
		return ResultNone
	case errors.Is(err, ErrBadRecord), errors.Is(err, errQueryBudget), errors.Is(err, errBadMacro):
		return ResultPermError
	default:
		return ResultTempError
	}
}

func resultIsCacheable(result string) bool {
	switch result {
	case ResultPass, ResultFail, ResultSoftfail, ResultNeutral, ResultNone, ResultPermError:
		return true
	default:
		return false
	}
}

func (e *SpfEvalEngine) fetchRecord(ctx *evalContext, domain string, mechanismTriggered bool) (Record, string, bool, error) {
	if ctx == nil {
		ctx = &evalContext{engine: e, maxQueries: -1, maxVoid: -1}
	}
	domain = normalizeDomain(domain)
	now := e.now()

	e.mu.Lock()
	e.ensureStateLocked()
	if entry, ok := e.getRecordLocked(domain, now); ok {
		record := cloneRecord(entry.record)
		raw := entry.raw
		found := entry.found
		bad := entry.bad
		e.mu.Unlock()
		if bad {
			return Record{Domain: domain}, raw, false, ErrBadRecord
		}
		if !found {
			return Record{Domain: domain}, "", false, ErrNoRecord
		}
		return record, raw, true, nil
	}
	if e.resolver == nil {
		e.mu.Unlock()
		return Record{Domain: domain}, "", false, ErrNoResolver
	}
	if call, ok := e.recordFlights[domain]; ok {
		if !ctx.singleflightCounted {
			e.stats.Singleflight++
			ctx.singleflightCounted = true
		}
		e.mu.Unlock()
		<-call.done
		if err := ctx.chargeQueries(call.queryCount); err != nil {
			return Record{Domain: domain}, call.raw, false, err
		}
		if call.void {
			if err := ctx.noteVoid(); err != nil {
				return Record{Domain: domain}, call.raw, false, err
			}
		}
		return cloneRecord(call.record), call.raw, call.found, call.err
	}
	if err := ctx.reserveQuery(); err != nil {
		e.mu.Unlock()
		return Record{Domain: domain}, "", false, err
	}
	call := &recordCall{done: make(chan struct{}), queryCount: 1}
	e.recordFlights[domain] = call
	resolver := e.resolver
	generation := e.generation
	e.mu.Unlock()

	answers, err := resolver.TXT(domain)
	if err != nil {
		callErr := fmt.Errorf("%w: %v", errDNS, err)
		e.completeRecordCall(domain, generation, call, Record{Domain: domain}, "", false, callErr, false)
		return Record{Domain: domain}, "", false, callErr
	}
	if mechanismTriggered && len(answers) == 0 {
		call.void = true
		if err := ctx.noteVoid(); err != nil {
			e.completeRecordCall(domain, generation, call, Record{Domain: domain}, "", false, err, false)
			return Record{Domain: domain}, "", false, err
		}
	}

	var spfRecords []string
	for _, txt := range answers {
		if isSPFRecord(txt) {
			spfRecords = append(spfRecords, txt)
		}
	}

	if len(spfRecords) == 0 {
		record := Record{Domain: domain}
		e.completeRecordCall(domain, generation, call, record, "", false, ErrNoRecord, true)
		return record, "", false, ErrNoRecord
	}

	raw := strings.Join(spfRecords, "\n")
	if len(spfRecords) > 1 {
		record := Record{Domain: domain}
		e.completeRecordCall(domain, generation, call, record, raw, false, ErrBadRecord, true)
		return record, raw, false, ErrBadRecord
	}

	record, err := parseRecord(spfRecords[0], domain, e.cfg.MaxMechanisms)
	if err != nil {
		e.completeRecordCall(domain, generation, call, Record{Domain: domain}, raw, false, ErrBadRecord, true)
		return Record{Domain: domain}, raw, false, ErrBadRecord
	}
	e.completeRecordCall(domain, generation, call, record, raw, true, nil, true)
	return cloneRecord(record), raw, true, nil
}

func (e *SpfEvalEngine) completeRecordCall(domain string, generation uint64, call *recordCall, record Record, raw string, found bool, callErr error, cacheRecord bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.generation == generation {
		switch {
		case cacheRecord && callErr == nil:
			e.putRecordLocked(domain, record, raw, found, e.now())
		case cacheRecord && errors.Is(callErr, ErrNoRecord):
			e.putRecordLocked(domain, Record{Domain: domain}, "", false, e.now())
		case cacheRecord && found:
			e.putRecordLocked(domain, record, raw, true, e.now())
		case cacheRecord && errors.Is(callErr, ErrBadRecord):
			e.putBadRecordLocked(domain, raw, e.now())
		case errors.Is(callErr, ErrBadRecord):
			e.purgeResultsForDomainLocked(domain, "\x00")
		}
	}

	call.record = record
	call.raw = raw
	call.found = found
	call.err = callErr
	if e.recordFlights[domain] == call {
		delete(e.recordFlights, domain)
	}
	close(call.done)
}

func (e *SpfEvalEngine) evaluateRecord(record Record, currentDomain string, ctx *evalContext, depth int) evalOutcome {
	if e.cfg.MaxRecursion >= 0 && depth > e.cfg.MaxRecursion {
		return evalOutcome{result: ResultPermError}
	}

	for _, mechanism := range record.Mechanisms {
		switch mechanism.Type {
		case MechAll:
			return mechanismHit(mechanism)
		case MechIP4, MechIP6:
			if e.matchIP(mechanism, ctx.ip) {
				return mechanismHit(mechanism)
			}
		case MechA:
			matched, err := e.evaluateA(mechanism, currentDomain, ctx)
			if err != nil {
				return mechanismFailure(mechanism, err)
			}
			if matched {
				return mechanismHit(mechanism)
			}
		case MechMX:
			matched, err := e.evaluateMX(mechanism, currentDomain, ctx)
			if err != nil {
				return mechanismFailure(mechanism, err)
			}
			if matched {
				return mechanismHit(mechanism)
			}
		case MechExists:
			matched, err := e.evaluateExists(mechanism, currentDomain, ctx)
			if err != nil {
				return mechanismFailure(mechanism, err)
			}
			if matched {
				return mechanismHit(mechanism)
			}
		case MechPTR:
			matched, err := e.evaluatePTR(mechanism, currentDomain, ctx)
			if err != nil {
				return mechanismFailure(mechanism, err)
			}
			if matched {
				return mechanismHit(mechanism)
			}
		case MechInclude:
			outcome, matched, err := e.evaluateInclude(mechanism, currentDomain, ctx, depth)
			if err != nil {
				return mechanismFailure(mechanism, err)
			}
			if matched {
				return outcome
			}
		default:
			return evalOutcome{result: ResultPermError, mechanism: mechanismText(mechanism), qualifier: mechanism.Qualifier}
		}
	}

	if record.Redirect != nil {
		return e.evaluateRedirect(*record.Redirect, currentDomain, ctx, depth)
	}
	return evalOutcome{result: ResultNeutral}
}

func mechanismHit(mechanism Mechanism) evalOutcome {
	return evalOutcome{
		result:    qualifierResult(mechanism.Qualifier),
		mechanism: mechanismText(mechanism),
		qualifier: mechanism.Qualifier,
	}
}

func mechanismFailure(mechanism Mechanism, err error) evalOutcome {
	result := ResultPermError
	if errors.Is(err, errDNS) || errors.Is(err, ErrNoResolver) {
		result = ResultTempError
	}
	return evalOutcome{result: result, mechanism: mechanismText(mechanism), qualifier: mechanism.Qualifier}
}

func (e *SpfEvalEngine) evaluateA(mechanism Mechanism, currentDomain string, ctx *evalContext) (bool, error) {
	domain, err := ctx.expandDomain(mechanism.Value, currentDomain)
	if err != nil {
		return false, err
	}
	addresses, err := ctx.lookupA(domain)
	if err != nil {
		return false, err
	}
	for _, address := range addresses {
		if addressMatches(ctx.ip, address, mechanism.cidrSet, mechanism.CIDR) {
			return true, nil
		}
	}
	return false, nil
}

func (e *SpfEvalEngine) evaluateMX(mechanism Mechanism, currentDomain string, ctx *evalContext) (bool, error) {
	domain, err := ctx.expandDomain(mechanism.Value, currentDomain)
	if err != nil {
		return false, err
	}
	exchangers, err := ctx.lookupMX(domain)
	if err != nil {
		return false, err
	}
	for _, exchanger := range exchangers {
		name := queryName(exchanger)
		if name == "" {
			continue
		}
		addresses, err := ctx.lookupA(name)
		if err != nil {
			return false, err
		}
		for _, address := range addresses {
			if addressMatches(ctx.ip, address, mechanism.cidrSet, mechanism.CIDR) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (e *SpfEvalEngine) evaluateExists(mechanism Mechanism, currentDomain string, ctx *evalContext) (bool, error) {
	domain, err := ctx.expandDomain(mechanism.Value, currentDomain)
	if err != nil {
		return false, err
	}
	addresses, err := ctx.lookupA(domain)
	if err != nil {
		return false, err
	}
	return len(addresses) > 0, nil
}

func (e *SpfEvalEngine) evaluatePTR(mechanism Mechanism, currentDomain string, ctx *evalContext) (bool, error) {
	target, err := ctx.expandDomain(mechanism.Value, currentDomain)
	if err != nil {
		return false, err
	}
	names, err := ctx.lookupPTR(ctx.ip)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		name = queryName(name)
		if name == target || strings.HasSuffix(name, "."+target) {
			return true, nil
		}
	}
	return false, nil
}

func (e *SpfEvalEngine) evaluateInclude(mechanism Mechanism, currentDomain string, ctx *evalContext, depth int) (evalOutcome, bool, error) {
	target, err := ctx.expandDomain(mechanism.Value, currentDomain)
	if err != nil {
		return evalOutcome{}, false, err
	}
	if e.cfg.MaxRecursion >= 0 && depth+1 > e.cfg.MaxRecursion {
		return evalOutcome{result: ResultPermError, mechanism: mechanismText(mechanism), qualifier: mechanism.Qualifier}, true, nil
	}
	record, _, _, err := e.fetchRecord(ctx, target, true)
	if err != nil {
		switch {
		case errors.Is(err, ErrNoRecord), errors.Is(err, ErrBadRecord), errors.Is(err, errQueryBudget):
			return evalOutcome{result: ResultPermError, mechanism: mechanismText(mechanism), qualifier: mechanism.Qualifier}, true, nil
		default:
			return evalOutcome{}, false, err
		}
	}
	nested := e.evaluateRecord(record, target, ctx, depth+1)
	switch nested.result {
	case ResultPass:
		return mechanismHit(mechanism), true, nil
	case ResultFail, ResultSoftfail, ResultNeutral:
		return evalOutcome{}, false, nil
	case ResultTempError:
		return evalOutcome{result: ResultTempError, mechanism: mechanismText(mechanism), qualifier: mechanism.Qualifier}, true, nil
	default:
		return evalOutcome{result: ResultPermError, mechanism: mechanismText(mechanism), qualifier: mechanism.Qualifier}, true, nil
	}
}

func (e *SpfEvalEngine) evaluateRedirect(mechanism Mechanism, currentDomain string, ctx *evalContext, depth int) evalOutcome {
	target, err := ctx.expandDomain(mechanism.Value, currentDomain)
	if err != nil {
		return evalOutcome{result: ResultPermError, mechanism: "redirect=" + mechanism.Value}
	}
	redirectText := "redirect=" + target
	if e.cfg.MaxRecursion >= 0 && depth+1 > e.cfg.MaxRecursion {
		return evalOutcome{result: ResultPermError, mechanism: redirectText}
	}
	record, _, _, err := e.fetchRecord(ctx, target, true)
	if err != nil {
		switch {
		case errors.Is(err, ErrNoRecord), errors.Is(err, ErrBadRecord), errors.Is(err, errQueryBudget):
			return evalOutcome{result: ResultPermError, mechanism: redirectText}
		default:
			return evalOutcome{result: ResultTempError, mechanism: redirectText}
		}
	}
	nested := e.evaluateRecord(record, target, ctx, depth+1)
	switch nested.result {
	case ResultPass, ResultFail, ResultSoftfail, ResultNeutral:
		return evalOutcome{result: nested.result, mechanism: redirectText}
	case ResultTempError:
		return evalOutcome{result: ResultTempError, mechanism: redirectText}
	default:
		return evalOutcome{result: ResultPermError, mechanism: redirectText}
	}
}

func (e *SpfEvalEngine) matchIP(mechanism Mechanism, ip netip.Addr) bool {
	address, err := netip.ParseAddr(mechanism.Value)
	if err != nil {
		return false
	}
	if mechanism.Type == MechIP4 {
		if !ip.Is4() || !address.Is4() {
			return false
		}
		bits := 32
		if mechanism.cidrSet {
			bits = mechanism.CIDR
		}
		return netip.PrefixFrom(address, bits).Contains(ip)
	}
	if !ip.Is6() || !address.Is6() {
		return false
	}
	bits := 128
	if mechanism.cidrSet {
		bits = mechanism.CIDR
	}
	return netip.PrefixFrom(address, bits).Contains(ip)
}

func addressMatches(ip, address netip.Addr, cidrSet bool, cidr int) bool {
	ip = ip.Unmap()
	address = address.Unmap()
	if !ip.IsValid() || !address.IsValid() || ip.Is4() != address.Is4() {
		return false
	}
	bits := 128
	if ip.Is4() {
		bits = 32
	}
	if cidrSet {
		bits = cidr
	}
	if bits < 0 || bits > ip.BitLen() {
		return false
	}
	return netip.PrefixFrom(address, bits).Contains(ip)
}

func (c *evalContext) lookupA(name string) ([]netip.Addr, error) {
	if err := c.reserveQuery(); err != nil {
		return nil, err
	}
	c.engine.mu.Lock()
	resolver := c.engine.resolver
	c.engine.mu.Unlock()
	if resolver == nil {
		return nil, ErrNoResolver
	}
	addresses, err := resolver.A(queryName(name))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDNS, err)
	}
	if len(addresses) == 0 {
		if err := c.noteVoid(); err != nil {
			return nil, err
		}
	}
	return addresses, nil
}

func (c *evalContext) lookupMX(name string) ([]string, error) {
	if err := c.reserveQuery(); err != nil {
		return nil, err
	}
	c.engine.mu.Lock()
	resolver := c.engine.resolver
	c.engine.mu.Unlock()
	if resolver == nil {
		return nil, ErrNoResolver
	}
	exchangers, err := resolver.MX(queryName(name))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDNS, err)
	}
	if len(exchangers) == 0 {
		if err := c.noteVoid(); err != nil {
			return nil, err
		}
	}
	return exchangers, nil
}

func (c *evalContext) lookupPTR(ip netip.Addr) ([]string, error) {
	if err := c.reserveQuery(); err != nil {
		return nil, err
	}
	c.engine.mu.Lock()
	resolver := c.engine.resolver
	c.engine.mu.Unlock()
	if resolver == nil {
		return nil, ErrNoResolver
	}
	names, err := resolver.PTR(ip)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errDNS, err)
	}
	if len(names) == 0 {
		if err := c.noteVoid(); err != nil {
			return nil, err
		}
	}
	return names, nil
}

func (c *evalContext) expandDomain(template, currentDomain string) (string, error) {
	if template == "" {
		template = currentDomain
	}
	expanded, err := expandMacros(template, currentDomain, c)
	if err != nil {
		return "", err
	}
	if !validExpandedName(expanded) {
		return "", errBadMacro
	}
	expanded = queryName(expanded)
	if expanded == "" {
		return "", errBadMacro
	}
	return expanded, nil
}

func expandMacros(template, currentDomain string, ctx *evalContext) (string, error) {
	var out strings.Builder
	for i := 0; i < len(template); {
		if template[i] != '%' {
			out.WriteByte(template[i])
			i++
			continue
		}
		if i+1 >= len(template) || template[i+1] != '{' {
			return "", errBadMacro
		}
		end := strings.IndexByte(template[i+2:], '}')
		if end < 0 {
			return "", errBadMacro
		}
		end += i + 2
		value, err := macroValue(template[i+2:end], currentDomain, ctx)
		if err != nil {
			return "", err
		}
		out.WriteString(value)
		i = end + 1
	}
	expanded := out.String()
	if expanded == "" {
		return "", errBadMacro
	}
	return expanded, nil
}

func macroValue(token, currentDomain string, ctx *evalContext) (string, error) {
	lower := strings.ToLower(token)
	switch lower {
	case "s":
		if ctx.sender == "" {
			return "", errBadMacro
		}
		return ctx.sender, nil
	case "l":
		if ctx.senderLocal == "" {
			return "", errBadMacro
		}
		return ctx.senderLocal, nil
	case "o":
		if ctx.senderDomain == "" {
			return "", errBadMacro
		}
		return ctx.senderDomain, nil
	case "d":
		if currentDomain == "" {
			return "", errBadMacro
		}
		return currentDomain, nil
	case "i":
		if !ctx.ip.IsValid() {
			return "", errBadMacro
		}
		return ctx.ip.String(), nil
	}
	if len(lower) > 1 && lower[0] == 'd' {
		digits := lower[1:]
		for i := 0; i < len(digits); i++ {
			if digits[i] < '0' || digits[i] > '9' {
				return "", errBadMacro
			}
		}
		count, err := strconv.Atoi(digits)
		if err != nil || count <= 0 {
			return "", errBadMacro
		}
		if currentDomain == "" {
			return "", errBadMacro
		}
		labels := strings.Split(queryName(currentDomain), ".")
		if count > len(labels) {
			count = len(labels)
		}
		selected := append([]string(nil), labels[len(labels)-count:]...)
		for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
			selected[left], selected[right] = selected[right], selected[left]
		}
		return strings.Join(selected, "."), nil
	}
	return "", errBadMacro
}

func validExpandedName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '.', '-', '_', ':':
			continue
		default:
			return false
		}
	}
	return true
}

func isSPFRecord(raw string) bool {
	if len(raw) < len("v=spf1") || !strings.EqualFold(raw[:len("v=spf1")], "v=spf1") {
		return false
	}
	if len(raw) == len("v=spf1") {
		return true
	}
	switch raw[len("v=spf1")] {
	case ' ', '\t', '\r', '\n':
		return true
	default:
		return false
	}
}

func parseRecord(raw, domain string, maxMechanisms int) (Record, error) {
	record := Record{Raw: raw, Domain: domain}
	fields := strings.Fields(raw)
	if len(fields) == 0 || !isSPFRecord(raw) {
		return record, ErrBadRecord
	}

	var redirect *Mechanism
	redirectSeen := false
	hasAll := false
	for _, term := range fields[1:] {
		if hasAll {
			continue
		}
		if key, value, ok := strings.Cut(term, "="); ok && isModifierName(key) {
			if strings.EqualFold(key, MechRedirect) {
				if redirectSeen || value == "" || strings.Contains(value, "/") {
					return record, ErrBadRecord
				}
				redirectSeen = true
				item := Mechanism{Type: MechRedirect, Value: value}
				redirect = &item
				continue
			}
			if len(key) > 1 && strings.ContainsRune("+-~?", rune(key[0])) && strings.EqualFold(key[1:], MechRedirect) {
				return record, ErrBadRecord
			}
			continue
		}

		mechanism, err := parseMechanism(term)
		if err != nil {
			return record, ErrBadRecord
		}
		record.Mechanisms = append(record.Mechanisms, mechanism)
		if maxMechanisms >= 0 && len(record.Mechanisms) > maxMechanisms {
			return record, ErrBadRecord
		}
		if mechanism.Type == MechAll {
			hasAll = true
		}
	}
	if hasAll {
		redirect = nil
	}
	record.Redirect = redirect
	return record, nil
}

func isModifierName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			continue
		default:
			return false
		}
	}
	return true
}

func parseMechanism(term string) (Mechanism, error) {
	qualifier, body := splitQualifier(term)
	if body == "" {
		return Mechanism{}, ErrBadRecord
	}
	lower := strings.ToLower(body)

	switch {
	case lower == MechAll:
		return Mechanism{Qualifier: qualifier, Type: MechAll}, nil
	case strings.HasPrefix(lower, "ip4:"):
		return parseIPMechanism(qualifier, MechIP4, body[4:])
	case strings.HasPrefix(lower, "ip6:"):
		return parseIPMechanism(qualifier, MechIP6, body[4:])
	case lower == MechA || strings.HasPrefix(lower, "a:") || strings.HasPrefix(lower, "a/"):
		return parseAddressMechanism(qualifier, MechA, body[1:])
	case lower == MechMX || strings.HasPrefix(lower, "mx:") || strings.HasPrefix(lower, "mx/"):
		return parseAddressMechanism(qualifier, MechMX, body[2:])
	case lower == MechPTR:
		return Mechanism{Qualifier: qualifier, Type: MechPTR}, nil
	case strings.HasPrefix(lower, "ptr:"):
		value := body[4:]
		if value == "" || strings.Contains(value, "/") {
			return Mechanism{}, ErrBadRecord
		}
		return Mechanism{Qualifier: qualifier, Type: MechPTR, Value: value}, nil
	case strings.HasPrefix(lower, "include:"):
		value := body[8:]
		if value == "" || strings.Contains(value, "/") {
			return Mechanism{}, ErrBadRecord
		}
		return Mechanism{Qualifier: qualifier, Type: MechInclude, Value: value}, nil
	case strings.HasPrefix(lower, "exists:"):
		value := body[7:]
		if value == "" || strings.Contains(value, "/") {
			return Mechanism{}, ErrBadRecord
		}
		return Mechanism{Qualifier: qualifier, Type: MechExists, Value: value}, nil
	default:
		return Mechanism{}, ErrBadRecord
	}
}

func splitQualifier(term string) (string, string) {
	if term == "" {
		return QualifierPass, ""
	}
	switch term[0] {
	case '+', '-', '~', '?':
		return string(term[0]), term[1:]
	default:
		return QualifierPass, term
	}
}

func parseIPMechanism(qualifier, mechanismType, operand string) (Mechanism, error) {
	addressText, cidr, cidrSet, err := splitCIDR(operand)
	if err != nil || addressText == "" {
		return Mechanism{}, ErrBadRecord
	}
	address, err := netip.ParseAddr(addressText)
	if err != nil {
		return Mechanism{}, ErrBadRecord
	}
	bits := 0
	if mechanismType == MechIP4 {
		if !address.Is4() {
			return Mechanism{}, ErrBadRecord
		}
		bits = 32
	} else {
		if !address.Is6() {
			return Mechanism{}, ErrBadRecord
		}
		bits = 128
	}
	if !cidrSet {
		cidr = bits
	} else if cidr > bits {
		return Mechanism{}, ErrBadRecord
	}
	return Mechanism{Qualifier: qualifier, Type: mechanismType, Value: address.String(), CIDR: cidr, cidrSet: cidrSet}, nil
}

func parseAddressMechanism(qualifier, mechanismType, operand string) (Mechanism, error) {
	hadColon := strings.HasPrefix(operand, ":")
	if hadColon {
		operand = operand[1:]
	}
	domain, cidr, cidrSet, err := splitCIDR(operand)
	if err != nil || strings.Contains(domain, "/") || (hadColon && domain == "") {
		return Mechanism{}, ErrBadRecord
	}
	if cidrSet && (cidr < 0 || cidr > 128) {
		return Mechanism{}, ErrBadRecord
	}
	return Mechanism{Qualifier: qualifier, Type: mechanismType, Value: domain, CIDR: cidr, cidrSet: cidrSet}, nil
}

func splitCIDR(value string) (string, int, bool, error) {
	slash := strings.LastIndexByte(value, '/')
	if slash < 0 {
		return value, 0, false, nil
	}
	cidrText := value[slash+1:]
	if cidrText == "" {
		return "", 0, false, ErrBadRecord
	}
	for i := 0; i < len(cidrText); i++ {
		if cidrText[i] < '0' || cidrText[i] > '9' {
			return "", 0, false, ErrBadRecord
		}
	}
	cidr, err := strconv.Atoi(cidrText)
	if err != nil || cidr < 0 {
		return "", 0, false, ErrBadRecord
	}
	return value[:slash], cidr, true, nil
}

func mechanismText(mechanism Mechanism) string {
	qualifier := mechanism.Qualifier
	switch mechanism.Type {
	case MechAll:
		return qualifier + MechAll
	case MechIP4, MechIP6:
		text := qualifier + mechanism.Type + ":" + mechanism.Value
		if mechanism.cidrSet {
			text += "/" + strconv.Itoa(mechanism.CIDR)
		}
		return text
	case MechA, MechMX:
		text := qualifier + mechanism.Type
		if mechanism.Value != "" {
			text += ":" + mechanism.Value
		}
		if mechanism.cidrSet {
			text += "/" + strconv.Itoa(mechanism.CIDR)
		}
		return text
	case MechPTR:
		if mechanism.Value == "" {
			return qualifier + MechPTR
		}
		return qualifier + MechPTR + ":" + mechanism.Value
	case MechInclude, MechExists:
		return qualifier + mechanism.Type + ":" + mechanism.Value
	case MechRedirect:
		return MechRedirect + "=" + mechanism.Value
	default:
		return qualifier + mechanism.Type
	}
}

func cloneRecord(record Record) Record {
	out := record
	out.Mechanisms = append([]Mechanism(nil), record.Mechanisms...)
	if record.Redirect != nil {
		redirect := *record.Redirect
		out.Redirect = &redirect
	}
	return out
}

// Explain returns the parsed mechanism chain of one domain.
func (e *SpfEvalEngine) Explain(domain string) (Trace, error) {
	domain = normalizeDomain(domain)
	if domain == "" {
		return Trace{}, ErrBadRequest
	}
	ctx := &evalContext{engine: e, maxQueries: -1, maxVoid: -1}
	record, raw, _, err := e.fetchRecord(ctx, domain, false)
	if err != nil {
		return Trace{}, err
	}
	trace := Trace{
		Domain:  domain,
		Record:  raw,
		Result:  "parsed",
		Queries: ctx.queries,
		Void:    ctx.void,
	}
	for _, mechanism := range record.Mechanisms {
		stepDomain := mechanism.Value
		if stepDomain == "" {
			stepDomain = record.Domain
		}
		trace.Steps = append(trace.Steps, TraceStep{
			Mechanism: mechanismText(mechanism),
			Domain:    stepDomain,
			Query:     traceQueryType(mechanism.Type),
			Result:    "parsed",
		})
	}
	if record.Redirect != nil {
		trace.Steps = append(trace.Steps, TraceStep{
			Mechanism: mechanismText(*record.Redirect),
			Domain:    record.Redirect.Value,
			Query:     MechRedirect,
			Result:    "parsed",
		})
	}
	return trace, nil
}

func traceQueryType(mechanismType string) string {
	switch mechanismType {
	case MechA, MechMX, MechExists:
		return "A"
	case MechPTR:
		return "PTR"
	default:
		return ""
	}
}

// ResolveRecord fetches and parses the record of one domain.
func (e *SpfEvalEngine) ResolveRecord(domain string) (Record, error) {
	domain = normalizeDomain(domain)
	if domain == "" {
		return Record{}, ErrBadRequest
	}
	ctx := &evalContext{engine: e, maxQueries: -1, maxVoid: -1}
	record, _, _, err := e.fetchRecord(ctx, domain, false)
	if err != nil {
		return Record{}, err
	}
	return cloneRecord(record), nil
}

// Record returns the cached raw record of one domain.
func (e *SpfEvalEngine) Record(domain string) (string, bool) {
	domain = normalizeDomain(domain)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensureStateLocked()
	entry, ok := e.getRecordLocked(domain, e.now())
	if !ok {
		return "", false
	}
	if entry.bad {
		return entry.raw, true
	}
	if !entry.found {
		return "", false
	}
	return entry.raw, true
}

// Expire drops cache entries whose ttl has elapsed.
func (e *SpfEvalEngine) Expire(now time.Time) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensureStateLocked()
	removed := 0
	for domain, entry := range e.records {
		if !now.Before(entry.expires) {
			e.removeRecordLocked(domain, entry)
			removed++
		}
	}
	for key, entry := range e.results {
		if !now.Before(entry.expires) {
			e.removeResultLocked(key, entry)
			removed++
		}
	}
	return removed
}

// Stats returns the live counters.
func (e *SpfEvalEngine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensureStateLocked()
	stats := e.stats
	stats.Cache = len(e.results)
	return stats
}

func (e *SpfEvalEngine) getRecordLocked(domain string, now time.Time) (*recordEntry, bool) {
	entry, ok := e.records[domain]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expires) {
		e.removeRecordLocked(domain, entry)
		return nil, false
	}
	entry.used = now
	if entry.elem != nil {
		e.recordLRU.MoveToFront(entry.elem)
	}
	return entry, true
}

func (e *SpfEvalEngine) putRecordLocked(domain string, record Record, raw string, found bool, now time.Time) {
	if e.cfg.CacheTTL <= 0 || e.cfg.MaxCacheEntries <= 0 {
		return
	}
	if old, ok := e.records[domain]; ok {
		e.removeRecordLocked(domain, old)
	}
	entry := &recordEntry{
		domain:  domain,
		raw:     raw,
		record:  cloneRecord(record),
		found:   found,
		expires: now.Add(e.cfg.CacheTTL),
		used:    now,
	}
	entry.elem = e.recordLRU.PushFront(entry)
	e.records[domain] = entry
	e.purgeResultsForDomainLocked(domain, recordFingerprint(raw))
	for len(e.records) > e.cfg.MaxCacheEntries {
		back := e.recordLRU.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(*recordEntry)
		e.removeRecordLocked(evicted.domain, evicted)
	}
}

func (e *SpfEvalEngine) putBadRecordLocked(domain, raw string, now time.Time) {
	if e.cfg.CacheTTL <= 0 || e.cfg.MaxCacheEntries <= 0 {
		return
	}
	if old, ok := e.records[domain]; ok {
		e.removeRecordLocked(domain, old)
	}
	entry := &recordEntry{
		domain:  domain,
		raw:     raw,
		record:  Record{Domain: domain},
		bad:     true,
		expires: now.Add(e.cfg.CacheTTL),
		used:    now,
	}
	entry.elem = e.recordLRU.PushFront(entry)
	e.records[domain] = entry
	e.purgeResultsForDomainLocked(domain, recordFingerprint(raw))
	for len(e.records) > e.cfg.MaxCacheEntries {
		back := e.recordLRU.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(*recordEntry)
		e.removeRecordLocked(evicted.domain, evicted)
	}
}

func (e *SpfEvalEngine) removeRecordLocked(domain string, entry *recordEntry) {
	if current, ok := e.records[domain]; ok && current == entry {
		delete(e.records, domain)
	}
	if entry.elem != nil {
		e.recordLRU.Remove(entry.elem)
		entry.elem = nil
	}
}

func (e *SpfEvalEngine) getResultLocked(key resultKey, now time.Time) (*resultEntry, bool) {
	entry, ok := e.results[key]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expires) {
		e.removeResultLocked(key, entry)
		return nil, false
	}
	entry.used = now
	if entry.elem != nil {
		e.resultLRU.MoveToFront(entry.elem)
	}
	return entry, true
}

func (e *SpfEvalEngine) putResultLocked(key resultKey, result SpfResult, now time.Time) {
	if e.cfg.CacheTTL <= 0 || e.cfg.MaxCacheEntries <= 0 {
		return
	}
	if old, ok := e.results[key]; ok {
		e.removeResultLocked(key, old)
	}
	entry := &resultEntry{
		key:     key,
		result:  result,
		expires: now.Add(e.cfg.CacheTTL),
		used:    now,
	}
	entry.elem = e.resultLRU.PushFront(entry)
	e.results[key] = entry
	for len(e.results) > e.cfg.MaxCacheEntries {
		back := e.resultLRU.Back()
		if back == nil {
			break
		}
		evicted := back.Value.(*resultEntry)
		e.removeResultLocked(evicted.key, evicted)
	}
}

func (e *SpfEvalEngine) removeResultLocked(key resultKey, entry *resultEntry) {
	if current, ok := e.results[key]; ok && current == entry {
		delete(e.results, key)
	}
	if entry.elem != nil {
		e.resultLRU.Remove(entry.elem)
		entry.elem = nil
	}
}

func (e *SpfEvalEngine) purgeResultsForDomainLocked(domain, keepFingerprint string) {
	for key, entry := range e.results {
		if key.domain == domain && key.fingerprint != keepFingerprint {
			e.removeResultLocked(key, entry)
		}
	}
}

const snapshotVersion = 1

type snapshotRecord struct {
	Domain  string `json:"domain"`
	Raw     string `json:"raw"`
	Found   bool   `json:"found"`
	Bad     bool   `json:"bad"`
	Expires int64  `json:"expires"`
	Used    int64  `json:"used"`
}

type snapshotResult struct {
	IP          string    `json:"ip"`
	Sender      string    `json:"sender"`
	Domain      string    `json:"domain"`
	Fingerprint string    `json:"fingerprint"`
	Result      SpfResult `json:"result"`
	Expires     int64     `json:"expires"`
	Used        int64     `json:"used"`
}

type snapshotState struct {
	Version      int              `json:"version"`
	Records      []snapshotRecord `json:"records"`
	Results      []snapshotResult `json:"results"`
	Hits         int              `json:"hits"`
	Evaluations  int              `json:"evaluations"`
	Singleflight int              `json:"singleflight"`
}

// Snapshot serialises the caches and the counters.
func (e *SpfEvalEngine) Snapshot() ([]byte, error) {
	e.mu.Lock()
	e.ensureStateLocked()
	state := snapshotState{
		Version:      snapshotVersion,
		Hits:         e.stats.Hits,
		Evaluations:  e.stats.Evaluations,
		Singleflight: e.stats.Singleflight,
	}
	for elem := e.recordLRU.Front(); elem != nil; elem = elem.Next() {
		entry := elem.Value.(*recordEntry)
		state.Records = append(state.Records, snapshotRecord{
			Domain:  entry.domain,
			Raw:     entry.raw,
			Found:   entry.found,
			Bad:     entry.bad,
			Expires: entry.expires.UnixNano(),
			Used:    entry.used.UnixNano(),
		})
	}
	for elem := e.resultLRU.Front(); elem != nil; elem = elem.Next() {
		entry := elem.Value.(*resultEntry)
		state.Results = append(state.Results, snapshotResult{
			IP:          entry.key.ip,
			Sender:      entry.key.sender,
			Domain:      entry.key.domain,
			Fingerprint: entry.key.fingerprint,
			Result:      entry.result,
			Expires:     entry.expires.UnixNano(),
			Used:        entry.used.UnixNano(),
		})
	}
	e.mu.Unlock()
	return json.Marshal(state)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (e *SpfEvalEngine) Restore(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if state.Version != snapshotVersion || state.Hits < 0 || state.Evaluations < 0 || state.Singleflight < 0 {
		return ErrBadSnapshot
	}

	records := make(map[string]*recordEntry)
	recordLRU := list.New()
	for _, item := range state.Records {
		domain := normalizeDomain(item.Domain)
		if domain == "" || domain != item.Domain {
			return ErrBadSnapshot
		}
		if _, exists := records[domain]; exists {
			return ErrBadSnapshot
		}
		if item.Bad {
			if item.Raw == "" || item.Found {
				return ErrBadSnapshot
			}
		} else if !item.Found && item.Raw != "" {
			return ErrBadSnapshot
		}
		record := Record{Domain: domain}
		if item.Found {
			parsed, err := parseRecord(item.Raw, domain, e.cfg.MaxMechanisms)
			if err != nil {
				return ErrBadSnapshot
			}
			record = parsed
		}
		entry := &recordEntry{
			domain:  domain,
			raw:     item.Raw,
			record:  record,
			found:   item.Found,
			bad:     item.Bad,
			expires: time.Unix(0, item.Expires),
			used:    time.Unix(0, item.Used),
		}
		entry.elem = recordLRU.PushBack(entry)
		records[domain] = entry
	}

	results := make(map[resultKey]*resultEntry)
	resultLRU := list.New()
	for _, item := range state.Results {
		ip, err := netip.ParseAddr(item.IP)
		if err != nil || !ip.IsValid() {
			return ErrBadSnapshot
		}
		domain := normalizeDomain(item.Domain)
		if domain == "" || domain != item.Domain || item.Result.Domain != domain || item.Result.Sender == "" {
			return ErrBadSnapshot
		}
		if item.Result.IP != ip {
			return ErrBadSnapshot
		}
		if len(item.Fingerprint) != sha256.Size*2 || item.Result.Queries < 0 || item.Result.Void < 0 {
			return ErrBadSnapshot
		}
		if _, err := hex.DecodeString(item.Fingerprint); err != nil {
			return ErrBadSnapshot
		}
		if !validSnapshotResult(item.Result.Result) {
			return ErrBadSnapshot
		}
		key := resultKey{
			ip:          ip.String(),
			sender:      item.Sender,
			domain:      domain,
			fingerprint: item.Fingerprint,
		}
		if _, exists := results[key]; exists {
			return ErrBadSnapshot
		}
		entry := &resultEntry{
			key:     key,
			result:  item.Result,
			expires: time.Unix(0, item.Expires),
			used:    time.Unix(0, item.Used),
		}
		entry.elem = resultLRU.PushBack(entry)
		results[key] = entry
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.ensureStateLocked()
	e.records = records
	e.recordLRU = recordLRU
	e.results = results
	e.resultLRU = resultLRU
	e.recordFlights = make(map[string]*recordCall)
	e.evalFlights = make(map[resultKey]*evalCall)
	e.stats.Hits = state.Hits
	e.stats.Evaluations = state.Evaluations
	e.stats.Singleflight = state.Singleflight
	e.generation++
	return nil
}

func validSnapshotResult(result string) bool {
	switch result {
	case ResultPass, ResultFail, ResultSoftfail, ResultNeutral, ResultNone, ResultPermError:
		return true
	default:
		return false
	}
}
