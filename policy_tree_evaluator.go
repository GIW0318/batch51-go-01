// Package task evaluates certificate policy trees for a validated path and
// reports which policies a relying party may accept.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
)

// AnyPolicyOID is the special anyPolicy identifier.
const AnyPolicyOID = "2.5.29.32.0"

// Reasons carried by PolicyResult and Trace.
const (
	ReasonOK            = "ok"
	ReasonNoValidPolicy = "no_valid_policy"
)

const (
	snapshotMagic   = "gsb-policy-tree-cache"
	snapshotVersion = 1
)

// Errors surfaced by the evaluator.
var (
	ErrEmptyChain      = errors.New("certificate chain is empty")
	ErrChainBroken     = errors.New("certificate chain is not linked")
	ErrTooManyPolicies = errors.New("too many policies on one certificate")
	ErrMappingDepth    = errors.New("policy mapping depth exceeded")
	ErrNodeBudget      = errors.New("policy tree node budget exceeded")
	ErrUnknownPolicy   = errors.New("policy was not part of the last evaluation")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// EvalConfig bounds the work one evaluation may perform.
type EvalConfig struct {
	MaxNodes           int
	MaxMappingDepth    int
	MaxPoliciesPerCert int
	CacheSize          int
}

// PolicyOID is one certificatePolicies entry.
type PolicyOID struct {
	OID       string
	Qualifier string
}

// PolicyMap is one issuerDomainPolicy to subjectDomainPolicy pair.
type PolicyMap struct {
	Issuer  string
	Subject string
}

// CertView is the policy relevant projection of one certificate. A negative
// skip counter means the corresponding extension is absent.
type CertView struct {
	Subject          string
	Issuer           string
	Fingerprint      string
	Policies         []PolicyOID
	Mappings         []PolicyMap
	RequireExplicit  int
	InhibitMapping   int
	InhibitAnyPolicy int
	SelfIssued       bool
}

// PolicyRequest describes what the relying party asks for.
type PolicyRequest struct {
	InitialPolicies []string
	RequireExplicit bool
	TrustAnchor     string
}

// PolicyResult is the accepted policy set of one path.
type PolicyResult struct {
	Valid    bool
	Policies []string
	Reason   string
	Steps    int
	ChainFP  string
}

// TraceStep is one certificate visit inside an explanation.
type TraceStep struct {
	Index     int
	Subject   string
	Before    []string
	After     []string
	SkipCerts int
	Note      string
}

// Trace explains how one policy travelled along the last evaluated path.
type Trace struct {
	OID      string
	ChainFP  string
	Steps    []TraceStep
	Accepted bool
	Reason   string
}

// PolicyTreeEvaluator owns the bounded result cache keyed by path fingerprint
// and initial policy set.
type PolicyTreeEvaluator struct {
	cfg EvalConfig

	mu    sync.Mutex
	cache map[string]evalRecord
	order []string
	last  *lastEvaluation
	nodes int
}

type evalRecord struct {
	Result PolicyResult
	Traces map[string]Trace
}

type lastEvaluation struct {
	key    string
	record evalRecord
}

type certContext struct {
	cert           CertView
	concrete       map[string]struct{}
	hasAny         bool
	mappings       map[string][]string
	anyAllowed     bool
	mappingAllowed bool
	explicitActive bool
	skipCerts      int
}

type originRun struct {
	oid   string
	steps []TraceStep
	final map[string]struct{}
}

// NewPolicyTreeEvaluator builds an evaluator with the given caps.
func NewPolicyTreeEvaluator(cfg EvalConfig) *PolicyTreeEvaluator {
	return &PolicyTreeEvaluator{
		cfg:   cfg,
		cache: make(map[string]evalRecord),
	}
}

// Evaluate walks the chain from the trust anchor to the leaf certificate.
func (e *PolicyTreeEvaluator) Evaluate(chain []CertView, req PolicyRequest) (PolicyResult, error) {
	norm, err := e.Normalize(chain)
	if err != nil {
		return PolicyResult{}, err
	}
	if err := e.checkEvaluationCaps(norm); err != nil {
		return PolicyResult{}, err
	}
	fp, err := fingerprintChain(norm)
	if err != nil {
		return PolicyResult{}, err
	}
	key := makeCacheKey(fp, req)

	e.mu.Lock()
	defer e.mu.Unlock()

	if rec, ok := e.cachedLocked(key); ok {
		e.last = &lastEvaluation{key: key, record: cloneRecord(rec)}
		return cloneResult(rec.Result), nil
	}

	rec := e.compute(norm, req, fp)
	e.nodes += nodeCount(norm)
	if e.cfg.CacheSize > 0 {
		e.storeLocked(key, rec)
	}
	e.last = &lastEvaluation{key: key, record: cloneRecord(rec)}
	return cloneResult(rec.Result), nil
}

// ValidPolicy reports whether one policy survived the last evaluated path.
func (e *PolicyTreeEvaluator) ValidPolicy(chain []CertView, oid string) (bool, error) {
	norm, err := e.Normalize(chain)
	if err != nil {
		return false, err
	}
	initial := make([]string, 0, len(norm[0].Policies))
	for _, policy := range norm[0].Policies {
		if policy.OID != "" {
			initial = append(initial, policy.OID)
		}
	}
	res, err := e.Evaluate(chain, PolicyRequest{InitialPolicies: initial})
	if err != nil {
		return false, err
	}
	if !res.Valid {
		return false, nil
	}
	for _, policy := range res.Policies {
		if policy == oid {
			return true, nil
		}
	}
	return false, nil
}

// Explain returns the per certificate reasoning of the last evaluation.
func (e *PolicyTreeEvaluator) Explain(oid string) (Trace, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.last == nil {
		return Trace{}, ErrUnknownPolicy
	}
	tr, ok := e.last.record.Traces[oid]
	if !ok {
		return Trace{}, ErrUnknownPolicy
	}
	return cloneTrace(tr), nil
}

// Cached reports whether the pair is already in the result cache.
func (e *PolicyTreeEvaluator) Cached(chain []CertView, req PolicyRequest) bool {
	if len(chain) == 0 {
		return false
	}
	for i := 1; i < len(chain); i++ {
		if chain[i].Issuer != chain[i-1].Subject {
			return false
		}
	}
	fp, err := fingerprintChain(chain)
	if err != nil {
		return false
	}
	key := makeCacheKey(fp, req)
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.cachedLocked(key)
	return ok
}

// Invalidate drops every cached result.
func (e *PolicyTreeEvaluator) Invalidate() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache = make(map[string]evalRecord)
	e.order = nil
	return nil
}

// Normalize validates linkage and per certificate policy counts.
func (e *PolicyTreeEvaluator) Normalize(chain []CertView) ([]CertView, error) {
	if len(chain) == 0 {
		return nil, ErrEmptyChain
	}
	out := make([]CertView, len(chain))
	for i, cert := range chain {
		if i > 0 && cert.Issuer != chain[i-1].Subject {
			return nil, ErrChainBroken
		}
		if e.cfg.MaxPoliciesPerCert >= 0 && len(cert.Policies) > e.cfg.MaxPoliciesPerCert {
			return nil, ErrTooManyPolicies
		}
		out[i] = cloneCert(cert)
	}
	return out, nil
}

// Fingerprint derives the stable path fingerprint used as the cache key.
func (e *PolicyTreeEvaluator) Fingerprint(chain []CertView) (string, error) {
	if len(chain) == 0 {
		return "", ErrEmptyChain
	}
	for i := 1; i < len(chain); i++ {
		if chain[i].Issuer != chain[i-1].Subject {
			return "", ErrChainBroken
		}
	}
	return fingerprintChain(chain)
}

// PolicySet returns only the accepted OIDs of one path.
func (e *PolicyTreeEvaluator) PolicySet(chain []CertView, req PolicyRequest) ([]string, error) {
	res, err := e.Evaluate(chain, req)
	if err != nil {
		return nil, err
	}
	return cloneStrings(res.Policies), nil
}

func (e *PolicyTreeEvaluator) checkEvaluationCaps(chain []CertView) error {
	mappingLayers := 0
	for _, cert := range chain {
		if len(cert.Mappings) > 0 {
			mappingLayers++
		}
	}
	if e.cfg.MaxMappingDepth >= 0 && mappingLayers > e.cfg.MaxMappingDepth {
		return ErrMappingDepth
	}
	if e.cfg.MaxNodes >= 0 && nodeCount(chain) > e.cfg.MaxNodes {
		return ErrNodeBudget
	}
	return nil
}

func nodeCount(chain []CertView) int {
	n := len(chain)
	for _, cert := range chain {
		n += len(cert.Policies)
	}
	return n
}

func (e *PolicyTreeEvaluator) compute(chain []CertView, req PolicyRequest, fp string) evalRecord {
	contexts := buildContexts(chain, req)
	origins := effectiveInitialPolicies(chain, req)
	runs := make([]originRun, 0, len(origins))
	finalSet := make(map[string]struct{})
	for _, origin := range origins {
		run := simulateOrigin(origin, contexts)
		runs = append(runs, run)
		for policy := range run.final {
			finalSet[policy] = struct{}{}
		}
	}

	result := PolicyResult{
		Policies: sortedSet(finalSet),
		Steps:    len(chain),
		ChainFP:  fp,
	}
	if len(result.Policies) == 0 {
		result.Valid = false
		result.Policies = nil
		result.Reason = ReasonNoValidPolicy
	} else {
		result.Valid = true
		result.Reason = ReasonOK
	}

	traces := make(map[string]Trace)
	aliasSource := make(map[string]int)
	for i, run := range runs {
		accepted := false
		if _, ok := finalSet[run.oid]; ok {
			accepted = true
		}
		reason := ReasonNoValidPolicy
		if accepted {
			reason = ReasonOK
		}
		traces[run.oid] = Trace{
			OID:      run.oid,
			ChainFP:  fp,
			Steps:    cloneTraceSteps(run.steps),
			Accepted: accepted,
			Reason:   reason,
		}
		for _, step := range run.steps {
			for _, policy := range step.After {
				if _, exists := traces[policy]; exists {
					continue
				}
				if _, exists := aliasSource[policy]; !exists {
					aliasSource[policy] = i
				}
			}
		}
	}
	for policy, source := range aliasSource {
		if _, exists := traces[policy]; exists {
			continue
		}
		base := traces[runs[source].oid]
		base.OID = policy
		if _, ok := finalSet[policy]; ok {
			base.Accepted = true
			base.Reason = ReasonOK
		} else {
			base.Accepted = false
			base.Reason = ReasonNoValidPolicy
		}
		traces[policy] = base
	}

	return evalRecord{Result: result, Traces: traces}
}

func buildContexts(chain []CertView, req PolicyRequest) []certContext {
	requireSkip := -1
	mappingSkip := -1
	anySkip := -1
	if req.RequireExplicit {
		requireSkip = 0
	}

	contexts := make([]certContext, len(chain))
	for i, cert := range chain {
		requireSkip = lowerCounter(requireSkip, cert.RequireExplicit)
		mappingSkip = lowerCounter(mappingSkip, cert.InhibitMapping)
		anySkip = lowerCounter(anySkip, cert.InhibitAnyPolicy)

		contexts[i] = certContext{
			cert:           cert,
			concrete:       concretePolicySet(cert.Policies),
			hasAny:         hasAnyPolicy(cert.Policies),
			mappings:       mappingSubjects(cert.Mappings),
			anyAllowed:     anySkip != 0,
			mappingAllowed: mappingSkip != 0,
			explicitActive: req.RequireExplicit || requireSkip == 0,
			skipCerts:      preferredSkip(requireSkip, mappingSkip, anySkip),
		}

		if !cert.SelfIssued {
			requireSkip = decrementCounter(requireSkip)
			mappingSkip = decrementCounter(mappingSkip)
			anySkip = decrementCounter(anySkip)
		}
	}
	return contexts
}

func lowerCounter(current, extension int) int {
	if extension < 0 {
		return current
	}
	if current < 0 || extension < current {
		return extension
	}
	return current
}

func decrementCounter(value int) int {
	if value > 0 {
		return value - 1
	}
	return value
}

func preferredSkip(requireSkip, mappingSkip, anySkip int) int {
	if anySkip >= 0 {
		return anySkip
	}
	if requireSkip >= 0 {
		return requireSkip
	}
	return mappingSkip
}

func simulateOrigin(origin string, contexts []certContext) originRun {
	state := map[string]bool{origin: false}
	steps := make([]TraceStep, 0, len(contexts))
	for i, ctx := range contexts {
		before := sortedState(state)
		mapped := false
		anyUsed := false
		missingMapped := false
		explicitDrop := false

		for policy, needsConcrete := range state {
			if !needsConcrete {
				continue
			}
			if _, ok := ctx.concrete[policy]; !ok {
				delete(state, policy)
				missingMapped = true
				continue
			}
			state[policy] = false
		}

		next := make(map[string]bool)
		for policy := range state {
			if subjects, ok := ctx.mappings[policy]; ok && ctx.mappingAllowed {
				mapped = true
				for _, subject := range subjects {
					addRequired(next, subject)
				}
				continue
			}
			if _, ok := ctx.concrete[policy]; ok {
				addOptional(next, policy)
				continue
			}
			if ctx.hasAny && ctx.anyAllowed && !ctx.explicitActive {
				addOptional(next, policy)
				anyUsed = true
				continue
			}
			if ctx.explicitActive && ctx.hasAny {
				explicitDrop = true
			}
		}
		state = next
		after := sortedState(state)
		steps = append(steps, TraceStep{
			Index:     i,
			Subject:   ctx.cert.Subject,
			Before:    before,
			After:     after,
			SkipCerts: ctx.skipCerts,
			Note:      traceNote(len(before), mapped, anyUsed, missingMapped, explicitDrop),
		})
	}

	final := make(map[string]struct{})
	for policy, needsConcrete := range state {
		if !needsConcrete {
			final[policy] = struct{}{}
		}
	}
	return originRun{
		oid:   origin,
		steps: steps,
		final: final,
	}
}

func traceNote(before int, mapped, anyUsed, missingMapped, explicitDrop bool) string {
	switch {
	case before == 0:
		return "policy not present"
	case missingMapped:
		return "mapped policy missing as explicit OID"
	case explicitDrop:
		return "explicit policy requirement not met"
	case mapped:
		return "policy mapping applied"
	case anyUsed:
		return "anyPolicy used to preserve policy"
	default:
		return "policy matched explicitly"
	}
}

func addOptional(state map[string]bool, policy string) {
	if existing, ok := state[policy]; ok {
		state[policy] = existing && false
		return
	}
	state[policy] = false
}

func addRequired(state map[string]bool, policy string) {
	if existing, ok := state[policy]; ok {
		state[policy] = existing && true
		return
	}
	state[policy] = true
}

func effectiveInitialPolicies(chain []CertView, req PolicyRequest) []string {
	raw := req.InitialPolicies
	if len(raw) == 0 {
		raw = make([]string, 0, len(chain[0].Policies))
		for _, policy := range chain[0].Policies {
			raw = append(raw, policy.OID)
		}
	}

	initial := make(map[string]struct{}, len(raw))
	hasAny := false
	for _, policy := range raw {
		if policy == "" {
			continue
		}
		if policy == AnyPolicyOID {
			hasAny = true
			continue
		}
		initial[policy] = struct{}{}
	}
	if hasAny {
		for _, cert := range chain {
			for policy := range concretePolicySet(cert.Policies) {
				initial[policy] = struct{}{}
			}
		}
	}
	return sortedSet(initial)
}

func concretePolicySet(policies []PolicyOID) map[string]struct{} {
	set := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		if policy.OID != "" && policy.OID != AnyPolicyOID {
			set[policy.OID] = struct{}{}
		}
	}
	return set
}

func hasAnyPolicy(policies []PolicyOID) bool {
	for _, policy := range policies {
		if policy.OID == AnyPolicyOID {
			return true
		}
	}
	return false
}

func mappingSubjects(mappings []PolicyMap) map[string][]string {
	result := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	for _, mapping := range mappings {
		if mapping.Issuer == "" || mapping.Subject == "" || mapping.Subject == AnyPolicyOID {
			continue
		}
		if seen[mapping.Issuer] == nil {
			seen[mapping.Issuer] = make(map[string]struct{})
		}
		if _, ok := seen[mapping.Issuer][mapping.Subject]; ok {
			continue
		}
		seen[mapping.Issuer][mapping.Subject] = struct{}{}
		result[mapping.Issuer] = append(result[mapping.Issuer], mapping.Subject)
	}
	for issuer := range result {
		sort.Strings(result[issuer])
	}
	return result
}

func sortedState(state map[string]bool) []string {
	out := make([]string, 0, len(state))
	for policy := range state {
		out = append(out, policy)
	}
	sort.Strings(out)
	return out
}

func sortedSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func makeCacheKey(fingerprint string, req PolicyRequest) string {
	initial := make([]string, 0, len(req.InitialPolicies))
	seen := make(map[string]struct{}, len(req.InitialPolicies))
	for _, policy := range req.InitialPolicies {
		if _, ok := seen[policy]; ok {
			continue
		}
		seen[policy] = struct{}{}
		initial = append(initial, policy)
	}
	sort.Strings(initial)
	explicit := "0"
	if req.RequireExplicit {
		explicit = "1"
	}
	return strings.Join([]string{
		fingerprint,
		explicit,
		req.TrustAnchor,
		strings.Join(initial, "\x00"),
	}, "\x1f")
}

func (e *PolicyTreeEvaluator) cachedLocked(key string) (evalRecord, bool) {
	rec, ok := e.cache[key]
	if !ok {
		return evalRecord{}, false
	}
	e.touchLocked(key)
	return cloneRecord(rec), true
}

func (e *PolicyTreeEvaluator) storeLocked(key string, rec evalRecord) {
	if e.cache == nil {
		e.cache = make(map[string]evalRecord)
	}
	if _, exists := e.cache[key]; !exists {
		e.order = append(e.order, key)
	} else {
		e.touchLocked(key)
	}
	e.cache[key] = cloneRecord(rec)
	for e.cfg.CacheSize > 0 && len(e.cache) > e.cfg.CacheSize && len(e.order) > 0 {
		oldest := e.order[0]
		e.order = e.order[1:]
		delete(e.cache, oldest)
	}
}

func (e *PolicyTreeEvaluator) touchLocked(key string) {
	for i, candidate := range e.order {
		if candidate != key {
			continue
		}
		copy(e.order[i:], e.order[i+1:])
		e.order = e.order[:len(e.order)-1]
		e.order = append(e.order, key)
		return
	}
}

func cloneResult(result PolicyResult) PolicyResult {
	result.Policies = cloneStrings(result.Policies)
	return result
}

func cloneRecord(record evalRecord) evalRecord {
	out := evalRecord{
		Result: cloneResult(record.Result),
		Traces: make(map[string]Trace, len(record.Traces)),
	}
	for oid, trace := range record.Traces {
		out.Traces[oid] = cloneTrace(trace)
	}
	return out
}

func cloneTrace(trace Trace) Trace {
	trace.Steps = cloneTraceSteps(trace.Steps)
	return trace
}

func cloneTraceSteps(steps []TraceStep) []TraceStep {
	out := make([]TraceStep, len(steps))
	for i, step := range steps {
		out[i] = step
		out[i].Before = cloneStrings(step.Before)
		out[i].After = cloneStrings(step.After)
	}
	return out
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneCert(cert CertView) CertView {
	out := cert
	if cert.Policies != nil {
		out.Policies = append([]PolicyOID(nil), cert.Policies...)
	}
	if cert.Mappings != nil {
		out.Mappings = append([]PolicyMap(nil), cert.Mappings...)
	}
	return out
}

type fingerprintCert struct {
	Subject          string      `json:"subject"`
	Issuer           string      `json:"issuer"`
	Fingerprint      string      `json:"fingerprint"`
	Policies         []PolicyOID `json:"policies"`
	Mappings         []PolicyMap `json:"mappings"`
	RequireExplicit  int         `json:"require_explicit"`
	InhibitMapping   int         `json:"inhibit_mapping"`
	InhibitAnyPolicy int         `json:"inhibit_any_policy"`
	SelfIssued       bool        `json:"self_issued"`
}

func fingerprintChain(chain []CertView) (string, error) {
	if len(chain) == 0 {
		return "", ErrEmptyChain
	}
	canonical := make([]fingerprintCert, len(chain))
	for i, cert := range chain {
		policies := append([]PolicyOID{}, cert.Policies...)
		sort.SliceStable(policies, func(a, b int) bool {
			if policies[a].OID != policies[b].OID {
				return policies[a].OID < policies[b].OID
			}
			return policies[a].Qualifier < policies[b].Qualifier
		})
		mappings := append([]PolicyMap{}, cert.Mappings...)
		sort.SliceStable(mappings, func(a, b int) bool {
			if mappings[a].Issuer != mappings[b].Issuer {
				return mappings[a].Issuer < mappings[b].Issuer
			}
			return mappings[a].Subject < mappings[b].Subject
		})
		canonical[i] = fingerprintCert{
			Subject:          cert.Subject,
			Issuer:           cert.Issuer,
			Fingerprint:      cert.Fingerprint,
			Policies:         policies,
			Mappings:         mappings,
			RequireExplicit:  cert.RequireExplicit,
			InhibitMapping:   cert.InhibitMapping,
			InhibitAnyPolicy: cert.InhibitAnyPolicy,
			SelfIssued:       cert.SelfIssued,
		}
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

type snapshotRecord struct {
	Result PolicyResult     `json:"result"`
	Traces map[string]Trace `json:"traces"`
}

type snapshotEntry struct {
	Key    string         `json:"key"`
	Record snapshotRecord `json:"record"`
}

type snapshotBody struct {
	Entries []snapshotEntry `json:"entries"`
	Order   []string        `json:"order,omitempty"`
	LastKey string          `json:"last_key,omitempty"`
	Last    *snapshotRecord `json:"last,omitempty"`
	Nodes   int             `json:"nodes"`
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Body     json.RawMessage `json:"body"`
}

// Snapshot serialises the cached evaluations and the node counter.
func (e *PolicyTreeEvaluator) Snapshot() ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	body := snapshotBody{
		Entries: make([]snapshotEntry, 0, len(e.cache)),
		Order:   append([]string(nil), e.order...),
		Nodes:   e.nodes,
	}
	seen := make(map[string]struct{}, len(e.order))
	for _, key := range e.order {
		if _, ok := seen[key]; ok {
			continue
		}
		rec, ok := e.cache[key]
		if !ok {
			continue
		}
		seen[key] = struct{}{}
		body.Entries = append(body.Entries, snapshotEntry{Key: key, Record: recordForSnapshot(rec)})
	}
	remaining := make([]string, 0)
	for key := range e.cache {
		if _, ok := seen[key]; !ok {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	for _, key := range remaining {
		body.Entries = append(body.Entries, snapshotEntry{Key: key, Record: recordForSnapshot(e.cache[key])})
	}
	if e.last != nil {
		body.LastKey = e.last.key
		last := recordForSnapshot(e.last.record)
		body.Last = &last
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		Body:     raw,
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole cache with a snapshot, all or nothing.
func (e *PolicyTreeEvaluator) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if len(envelope.Body) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Body)
	if !strings.EqualFold(envelope.Checksum, hex.EncodeToString(sum[:])) {
		return ErrBadSnapshot
	}
	var body snapshotBody
	decoder := json.NewDecoder(bytes.NewReader(envelope.Body))
	if err := decoder.Decode(&body); err != nil {
		return ErrBadSnapshot
	}
	if body.Nodes < 0 {
		return ErrBadSnapshot
	}

	restored := make(map[string]evalRecord, len(body.Entries))
	entryOrder := make([]string, 0, len(body.Entries))
	for _, entry := range body.Entries {
		if entry.Key == "" {
			return ErrBadSnapshot
		}
		if _, exists := restored[entry.Key]; exists {
			return ErrBadSnapshot
		}
		rec, ok := recordFromSnapshot(entry.Record)
		if !ok {
			return ErrBadSnapshot
		}
		restored[entry.Key] = rec
		entryOrder = append(entryOrder, entry.Key)
	}
	order := make([]string, 0, len(entryOrder))
	ordered := make(map[string]struct{}, len(entryOrder))
	for _, key := range body.Order {
		if _, ok := restored[key]; !ok {
			return ErrBadSnapshot
		}
		if _, exists := ordered[key]; exists {
			return ErrBadSnapshot
		}
		ordered[key] = struct{}{}
		order = append(order, key)
	}
	for _, key := range entryOrder {
		if _, ok := ordered[key]; ok {
			continue
		}
		order = append(order, key)
	}

	var last *lastEvaluation
	if body.Last != nil {
		rec, ok := recordFromSnapshot(*body.Last)
		if !ok {
			return ErrBadSnapshot
		}
		last = &lastEvaluation{key: body.LastKey, record: rec}
	} else if body.LastKey != "" {
		rec, ok := restored[body.LastKey]
		if !ok {
			return ErrBadSnapshot
		}
		last = &lastEvaluation{key: body.LastKey, record: cloneRecord(rec)}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache = restored
	e.order = order
	e.last = last
	e.nodes = body.Nodes
	return nil
}

func recordForSnapshot(record evalRecord) snapshotRecord {
	traces := make(map[string]Trace, len(record.Traces))
	for oid, trace := range record.Traces {
		traces[oid] = cloneTrace(trace)
	}
	return snapshotRecord{
		Result: cloneResult(record.Result),
		Traces: traces,
	}
}

func recordFromSnapshot(record snapshotRecord) (evalRecord, bool) {
	if record.Result.ChainFP == "" {
		return evalRecord{}, false
	}
	if record.Result.Valid && record.Result.Reason != ReasonOK {
		return evalRecord{}, false
	}
	if !record.Result.Valid && record.Result.Reason != ReasonNoValidPolicy {
		return evalRecord{}, false
	}
	for _, policy := range record.Result.Policies {
		if policy == "" || policy == AnyPolicyOID {
			return evalRecord{}, false
		}
	}
	traces := make(map[string]Trace, len(record.Traces))
	for oid, trace := range record.Traces {
		if oid == "" || oid == AnyPolicyOID || trace.OID != oid || trace.ChainFP != record.Result.ChainFP {
			return evalRecord{}, false
		}
		traces[oid] = cloneTrace(trace)
	}
	return evalRecord{Result: cloneResult(record.Result), Traces: traces}, true
}
