// Package task moderates world, guild and private chat: it normalises the
// message text, scans the live lexicon, grades the outcome and remembers the
// appeal decisions across restarts.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Verdict actions.
const (
	ActionAllow = "allow"
	ActionWarn  = "warn"
	ActionMute  = "mute"
	ActionBlock = "block"
)

// Chat channels.
const (
	ChannelWorld   = "world"
	ChannelGuild   = "guild"
	ChannelWhisper = "whisper"
)

// Verdict reason codes.
const (
	ReasonClean       = "clean"
	ReasonLexiconHit  = "lexicon_hit"
	ReasonWhitelisted = "whitelisted"
	ReasonOversize    = "oversize"
	ReasonRateLimited = "rate_limited"
	ReasonAppealed    = "appealed"
)

// Errors reported by the moderator.
var (
	ErrBadLexicon      = errors.New("lexicon version or entries are invalid")
	ErrLexiconTooLarge = errors.New("lexicon exceeds the entry cap")
	ErrBadPolicy       = errors.New("moderation policy is invalid")
	ErrBadMessage      = errors.New("chat message is invalid")
	ErrUnknownMsg      = errors.New("unknown message")
	ErrBadReason       = errors.New("appeal reason is required")
	ErrBadRateLimit    = errors.New("rate limit must be positive")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

const (
	snapshotFormat  = "gsb/batch51-go-game-a-17/chat-content-moderator"
	snapshotVersion = 1
)

// LexEntry is one banned term of the lexicon.
type LexEntry struct {
	Term     string
	Category string
	Severity int
}

// ChatMsg is one message submitted for review.
type ChatMsg struct {
	ID      string
	Channel string
	Sender  string
	Text    string
	At      time.Time
}

// Policy is the moderation policy in force.
type Policy struct {
	WarnSeverity  int
	MuteSeverity  int
	BlockSeverity int
	Whitelist     []string
}

// Verdict is the graded outcome of one message.
type Verdict struct {
	MsgID      string
	Action     string
	Severity   int
	LexiconVer string
	Matched    []string
	Reason     string
}

// Hit is one lexicon match reported by Explain.
type Hit struct {
	Term     string
	Category string
	Severity int
}

// ModStats aggregates the moderation counters.
type ModStats struct {
	Checks    int
	Allowed   int
	Warned    int
	Muted     int
	Blocked   int
	Oversize  int
	Appealed  int
	Throttled int
}

// ModLimits bounds the moderator tables.
type ModLimits struct {
	MaxTerms         int
	MaxNormalizedLen int
	MaxWindowEntries int
}

type normalizedLexEntry struct {
	term        string
	compactTerm string
	category    string
	severity    int
	displayTerm string
}

type lexiconState struct {
	version string
	entries []normalizedLexEntry
}

type phraseState struct {
	text    string
	compact string
}

type policyState struct {
	warn      int
	mute      int
	block     int
	whitelist []phraseState
}

type verdictRecord struct {
	verdict  Verdict
	appealed bool
}

// ChatContentModerator owns the lexicon, the policy and the verdict memory.
type ChatContentModerator struct {
	mu sync.RWMutex

	limits ModLimits
	lex    *lexiconState
	policy policyState

	verdicts map[string]verdictRecord
	stats    ModStats

	rateN      int
	rateWindow time.Duration
	windows    map[string][]time.Time
}

// NewChatContentModerator builds a moderator from the caps and the policy.
func NewChatContentModerator(limits ModLimits, p Policy) *ChatContentModerator {
	return &ChatContentModerator{
		limits:   limits,
		lex:      &lexiconState{},
		policy:   lenientPolicy(p),
		verdicts: make(map[string]verdictRecord),
		windows:  make(map[string][]time.Time),
	}
}

// SetLexicon installs a new lexicon version in one atomic swap.
func (m *ChatContentModerator) SetLexicon(ver string, entries []LexEntry) error {
	next, err := buildLexicon(ver, entries, m.limits.MaxTerms)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.lex = next
	m.mu.Unlock()
	return nil
}

// Check grades one message.
func (m *ChatContentModerator) Check(msg ChatMsg) (Verdict, error) {
	if isBlankValue(msg.ID) || isBlankValue(msg.Sender) {
		return Verdict{}, ErrBadMessage
	}

	normalized := normalizeText(msg.Text)
	compact := compactText(normalized)

	m.mu.Lock()
	defer m.mu.Unlock()

	if record, ok := m.verdicts[msg.ID]; ok {
		return cloneVerdict(record.verdict), nil
	}

	lex := m.lex
	policy := m.policy
	hits := matchingEntries(lex, normalized, compact)
	oversize := m.limits.MaxNormalizedLen > 0 &&
		utf8.RuneCountInString(normalized) > m.limits.MaxNormalizedLen
	whitelisted := isWhitelisted(normalized, compact, policy.whitelist)

	rateLimited := false
	if !whitelisted && m.rateN > 0 && m.rateWindow > 0 {
		rateLimited = m.checkRateLimitLocked(msg.Sender, msg.At)
	}

	verdict := Verdict{
		MsgID:      msg.ID,
		LexiconVer: lex.version,
	}
	switch {
	case whitelisted:
		verdict.Action = ActionAllow
		verdict.Reason = ReasonWhitelisted
	case rateLimited:
		verdict.Action = ActionMute
		verdict.Severity = policy.mute
		verdict.Reason = ReasonRateLimited
		verdict.Matched = matchedTerms(hits)
	case oversize:
		verdict.Action = ActionWarn
		verdict.Severity = policy.warn
		verdict.Reason = ReasonOversize
		verdict.Matched = matchedTerms(hits)
	case len(hits) > 0:
		verdict.Severity, verdict.Action = gradeHits(hits, policy)
		verdict.Reason = ReasonLexiconHit
		verdict.Matched = matchedTerms(hits)
	default:
		verdict.Action = ActionAllow
		verdict.Reason = ReasonClean
	}

	m.stats.Checks++
	if oversize {
		m.stats.Oversize++
	}
	if rateLimited {
		m.stats.Throttled++
	}
	switch verdict.Action {
	case ActionAllow:
		m.stats.Allowed++
	case ActionWarn:
		m.stats.Warned++
	case ActionMute:
		m.stats.Muted++
	case ActionBlock:
		m.stats.Blocked++
	}

	stored := cloneVerdict(verdict)
	m.verdicts[msg.ID] = verdictRecord{verdict: stored}
	return cloneVerdict(stored), nil
}

// Explain lists the lexicon matches of one message.
func (m *ChatContentModerator) Explain(msg ChatMsg) []Hit {
	normalized := normalizeText(msg.Text)
	compact := compactText(normalized)

	m.mu.RLock()
	lex := m.lex
	m.mu.RUnlock()

	matches := matchingEntries(lex, normalized, compact)
	if len(matches) == 0 {
		return nil
	}
	hits := make([]Hit, 0, len(matches))
	for _, match := range matches {
		hits = append(hits, Hit{
			Term:     match.displayTerm,
			Category: match.category,
			Severity: match.severity,
		})
	}
	return hits
}

// SetPolicy installs a new moderation policy.
func (m *ChatContentModerator) SetPolicy(p Policy) error {
	next, err := strictPolicy(p)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.policy = next
	m.mu.Unlock()
	return nil
}

// Appeal records the appeal of one moderated message.
func (m *ChatContentModerator) Appeal(msgID string, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	record, ok := m.verdicts[msgID]
	if !ok {
		return ErrUnknownMsg
	}
	if isBlankValue(reason) {
		return ErrBadReason
	}
	if !record.appealed {
		record.appealed = true
		m.verdicts[msgID] = record
		m.stats.Appealed++
	}
	return nil
}

// SetRateLimit bounds how many messages one sender may submit per window.
func (m *ChatContentModerator) SetRateLimit(n int, window time.Duration) error {
	if n <= 0 || window <= 0 {
		return ErrBadRateLimit
	}
	m.mu.Lock()
	m.rateN = n
	m.rateWindow = window
	m.windows = make(map[string][]time.Time)
	m.mu.Unlock()
	return nil
}

// Stats returns a copy of the moderation counters.
func (m *ChatContentModerator) Stats() ModStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stats
}

type snapshotEnvelope struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type snapshotVerdict struct {
	Verdict  Verdict `json:"verdict"`
	Appealed bool    `json:"appealed"`
}

type snapshotWindow struct {
	Sender string      `json:"sender"`
	Times  []time.Time `json:"times"`
}

type snapshotPayload struct {
	LexiconVersion string            `json:"lexicon_version"`
	Lexicon        []LexEntry        `json:"lexicon"`
	Policy         Policy            `json:"policy"`
	Verdicts       []snapshotVerdict `json:"verdicts"`
	Stats          ModStats          `json:"stats"`
	RateLimit      int               `json:"rate_limit"`
	RateWindowNS   int64             `json:"rate_window_ns"`
	Windows        []snapshotWindow  `json:"windows,omitempty"`
}

// Snapshot serialises the lexicon, the verdicts and the counters.
func (m *ChatContentModerator) Snapshot() ([]byte, error) {
	m.mu.RLock()
	payload := m.snapshotPayloadLocked()
	m.mu.RUnlock()

	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(rawPayload)
	envelope := snapshotEnvelope{
		Format:   snapshotFormat,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  rawPayload,
	}
	return json.Marshal(envelope)
}

// Restore replaces the moderator state with a snapshot, all or nothing.
func (m *ChatContentModerator) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return badSnapshot("envelope", err)
	}
	if envelope.Format != snapshotFormat || envelope.Version != snapshotVersion {
		return badSnapshot("format", nil)
	}
	sum := sha256.Sum256(envelope.Payload)
	if !strings.EqualFold(envelope.Checksum, hex.EncodeToString(sum[:])) {
		return badSnapshot("checksum", nil)
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return badSnapshot("payload", err)
	}

	lex, err := restoreLexicon(payload, m.limits.MaxTerms)
	if err != nil {
		return badSnapshot("lexicon", err)
	}
	policy, err := strictPolicy(payload.Policy)
	if err != nil {
		return badSnapshot("policy", err)
	}
	if !validStats(payload.Stats) {
		return badSnapshot("stats", nil)
	}

	verdicts := make(map[string]verdictRecord, len(payload.Verdicts))
	for _, saved := range payload.Verdicts {
		verdict := cloneVerdict(saved.Verdict)
		if verdict.MsgID == "" || !validVerdict(verdict) {
			return badSnapshot("verdict", nil)
		}
		if _, duplicate := verdicts[verdict.MsgID]; duplicate {
			return badSnapshot("duplicate verdict", nil)
		}
		verdicts[verdict.MsgID] = verdictRecord{
			verdict:  verdict,
			appealed: saved.Appealed,
		}
	}

	if payload.RateLimit < 0 || payload.RateWindowNS < 0 {
		return badSnapshot("rate limit", nil)
	}
	if (payload.RateLimit == 0) != (payload.RateWindowNS == 0) {
		return badSnapshot("rate window", nil)
	}
	windows := make(map[string][]time.Time, len(payload.Windows))
	for _, saved := range payload.Windows {
		if saved.Sender == "" {
			return badSnapshot("window sender", nil)
		}
		if _, duplicate := windows[saved.Sender]; duplicate {
			return badSnapshot("duplicate window", nil)
		}
		if m.limits.MaxWindowEntries >= 0 && len(saved.Times) > m.limits.MaxWindowEntries {
			return badSnapshot("window size", nil)
		}
		times := append([]time.Time(nil), saved.Times...)
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		windows[saved.Sender] = times
	}

	m.mu.Lock()
	m.lex = lex
	m.policy = policy
	m.verdicts = verdicts
	m.stats = payload.Stats
	m.rateN = payload.RateLimit
	m.rateWindow = time.Duration(payload.RateWindowNS)
	m.windows = windows
	m.mu.Unlock()
	return nil
}

func (m *ChatContentModerator) snapshotPayloadLocked() snapshotPayload {
	payload := snapshotPayload{
		LexiconVersion: m.lex.version,
		Policy: Policy{
			WarnSeverity:  m.policy.warn,
			MuteSeverity:  m.policy.mute,
			BlockSeverity: m.policy.block,
		},
		Stats:        m.stats,
		RateLimit:    m.rateN,
		RateWindowNS: int64(m.rateWindow),
	}

	for _, entry := range m.lex.entries {
		payload.Lexicon = append(payload.Lexicon, LexEntry{
			Term:     entry.displayTerm,
			Category: entry.category,
			Severity: entry.severity,
		})
	}
	for _, phrase := range m.policy.whitelist {
		payload.Policy.Whitelist = append(payload.Policy.Whitelist, phrase.text)
	}

	for _, record := range m.verdicts {
		payload.Verdicts = append(payload.Verdicts, snapshotVerdict{
			Verdict:  cloneVerdict(record.verdict),
			Appealed: record.appealed,
		})
	}
	sort.Slice(payload.Verdicts, func(i, j int) bool {
		return payload.Verdicts[i].Verdict.MsgID < payload.Verdicts[j].Verdict.MsgID
	})

	for sender, times := range m.windows {
		payload.Windows = append(payload.Windows, snapshotWindow{
			Sender: sender,
			Times:  append([]time.Time(nil), times...),
		})
	}
	sort.Slice(payload.Windows, func(i, j int) bool {
		return payload.Windows[i].Sender < payload.Windows[j].Sender
	})
	return payload
}

func (m *ChatContentModerator) checkRateLimitLocked(sender string, at time.Time) bool {
	capacity := m.limits.MaxWindowEntries
	if capacity <= 0 {
		delete(m.windows, sender)
		return false
	}

	window := m.windows[sender]
	cutoff := at.Add(-m.rateWindow)
	kept := window[:0]
	for _, timestamp := range window {
		if timestamp.After(cutoff) {
			kept = append(kept, timestamp)
		}
	}
	window = kept
	limited := len(window) >= m.rateN

	window = append(window, at)
	sort.Slice(window, func(i, j int) bool { return window[i].Before(window[j]) })
	if len(window) > capacity {
		window = window[len(window)-capacity:]
	}
	m.windows[sender] = window
	return limited
}

func buildLexicon(ver string, entries []LexEntry, maxTerms int) (*lexiconState, error) {
	if isBlankValue(ver) || len(entries) == 0 {
		return nil, ErrBadLexicon
	}
	if len(entries) > maxTerms {
		return nil, ErrLexiconTooLarge
	}

	next := &lexiconState{version: ver, entries: make([]normalizedLexEntry, 0, len(entries))}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		term := normalizeText(entry.Term)
		if term == "" || entry.Severity <= 0 {
			return nil, ErrBadLexicon
		}
		if _, duplicate := seen[term]; duplicate {
			return nil, ErrBadLexicon
		}
		seen[term] = struct{}{}
		next.entries = append(next.entries, normalizedLexEntry{
			term:        term,
			compactTerm: compactText(term),
			category:    entry.Category,
			severity:    entry.Severity,
			displayTerm: entry.Term,
		})
	}
	sort.Slice(next.entries, func(i, j int) bool {
		if next.entries[i].displayTerm == next.entries[j].displayTerm {
			return next.entries[i].term < next.entries[j].term
		}
		return next.entries[i].displayTerm < next.entries[j].displayTerm
	})
	return next, nil
}

func restoreLexicon(payload snapshotPayload, maxTerms int) (*lexiconState, error) {
	if payload.LexiconVersion == "" && len(payload.Lexicon) == 0 {
		return &lexiconState{}, nil
	}
	return buildLexicon(payload.LexiconVersion, payload.Lexicon, maxTerms)
}

func strictPolicy(p Policy) (policyState, error) {
	if p.WarnSeverity <= 0 ||
		p.MuteSeverity <= p.WarnSeverity ||
		p.BlockSeverity <= p.MuteSeverity {
		return policyState{}, ErrBadPolicy
	}
	next := policyState{
		warn:  p.WarnSeverity,
		mute:  p.MuteSeverity,
		block: p.BlockSeverity,
	}
	seen := make(map[string]struct{}, len(p.Whitelist))
	for _, raw := range p.Whitelist {
		normalized := normalizeText(raw)
		if normalized == "" {
			return policyState{}, ErrBadPolicy
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		next.whitelist = append(next.whitelist, phraseState{
			text:    normalized,
			compact: compactText(normalized),
		})
	}
	sort.Slice(next.whitelist, func(i, j int) bool {
		return next.whitelist[i].text < next.whitelist[j].text
	})
	return next, nil
}

func lenientPolicy(p Policy) policyState {
	next := policyState{
		warn:  p.WarnSeverity,
		mute:  p.MuteSeverity,
		block: p.BlockSeverity,
	}
	seen := make(map[string]struct{}, len(p.Whitelist))
	for _, raw := range p.Whitelist {
		normalized := normalizeText(raw)
		if normalized == "" {
			continue
		}
		if _, duplicate := seen[normalized]; duplicate {
			continue
		}
		seen[normalized] = struct{}{}
		next.whitelist = append(next.whitelist, phraseState{
			text:    normalized,
			compact: compactText(normalized),
		})
	}
	sort.Slice(next.whitelist, func(i, j int) bool {
		return next.whitelist[i].text < next.whitelist[j].text
	})
	return next
}

func matchingEntries(lex *lexiconState, text string, compact string) []normalizedLexEntry {
	if lex == nil || len(lex.entries) == 0 {
		return nil
	}
	matches := make([]normalizedLexEntry, 0)
	for _, entry := range lex.entries {
		if strings.Contains(text, entry.term) ||
			(entry.compactTerm != "" && strings.Contains(compact, entry.compactTerm)) {
			matches = append(matches, entry)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].displayTerm < matches[j].displayTerm
	})
	return matches
}

func isWhitelisted(text string, compact string, whitelist []phraseState) bool {
	for _, phrase := range whitelist {
		if strings.Contains(text, phrase.text) ||
			(phrase.compact != "" && strings.Contains(compact, phrase.compact)) {
			return true
		}
	}
	return false
}

func gradeHits(hits []normalizedLexEntry, policy policyState) (int, string) {
	severity := 0
	for _, hit := range hits {
		if hit.severity > severity {
			severity = hit.severity
		}
	}
	switch {
	case policy.block > 0 && severity >= policy.block:
		return severity, ActionBlock
	case policy.mute > 0 && severity >= policy.mute:
		return severity, ActionMute
	case policy.warn > 0 && severity >= policy.warn:
		return severity, ActionWarn
	default:
		return severity, ActionAllow
	}
}

func matchedTerms(hits []normalizedLexEntry) []string {
	if len(hits) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(hits))
	terms := make([]string, 0, len(hits))
	for _, hit := range hits {
		if _, duplicate := seen[hit.displayTerm]; duplicate {
			continue
		}
		seen[hit.displayTerm] = struct{}{}
		terms = append(terms, hit.displayTerm)
	}
	sort.Strings(terms)
	return terms
}

func cloneVerdict(verdict Verdict) Verdict {
	if verdict.Matched != nil {
		verdict.Matched = append([]string(nil), verdict.Matched...)
	}
	return verdict
}

func normalizeText(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for _, r := range text {
		if isZeroWidth(r) {
			continue
		}
		r = fullWidthToASCII(r)
		r = unicode.ToLower(r)
		if unicode.IsSpace(r) {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(r)
	}

	normalized := collapseWhitespace(builder.String())
	normalized = restoreSplitSeparators(normalized)
	normalized = foldCyrillicHomoglyphs(normalized)
	return collapseWhitespace(normalized)
}

func compactText(text string) string {
	return strings.ReplaceAll(text, " ", "")
}

func collapseWhitespace(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	started := false
	pendingSpace := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			if started {
				pendingSpace = true
			}
			continue
		}
		if pendingSpace {
			builder.WriteByte(' ')
			pendingSpace = false
		}
		builder.WriteRune(r)
		started = true
	}
	return builder.String()
}

func fullWidthToASCII(r rune) rune {
	switch {
	case r == '\u3000':
		return ' '
	case r >= '\uFF01' && r <= '\uFF5E':
		return r - 0xFEE0
	default:
		return r
	}
}

func isZeroWidth(r rune) bool {
	switch r {
	case '\u00AD', '\u034F', '\u061C', '\u17B4', '\u17B5', '\u180E',
		'\u200B', '\u200C', '\u200D', '\u200E', '\u200F',
		'\u202A', '\u202B', '\u202C', '\u202D', '\u202E',
		'\u2060', '\u2061', '\u2062', '\u2063', '\u2064',
		'\u2066', '\u2067', '\u2068', '\u2069', '\uFEFF':
		return true
	}
	return (r >= '\uFE00' && r <= '\uFE0F') ||
		(r >= '\U000E0100' && r <= '\U000E01EF')
}

func isBlankValue(value string) bool {
	for _, r := range value {
		if isZeroWidth(r) || unicode.IsSpace(r) {
			continue
		}
		return false
	}
	return true
}

func restoreSplitSeparators(text string) string {
	runes := []rune(text)
	var builder strings.Builder
	builder.Grow(len(text))

	for i := 0; i < len(runes); {
		if !isWordRune(runes[i]) {
			builder.WriteRune(runes[i])
			i++
			continue
		}

		wordEnd := i
		for wordEnd < len(runes) && isWordRune(runes[wordEnd]) {
			wordEnd++
		}
		if wordEnd-i != 1 {
			builder.WriteString(string(runes[i:wordEnd]))
			i = wordEnd
			continue
		}

		joined := strings.Builder{}
		joined.WriteRune(runes[i])
		count := 1
		sequenceEnd := wordEnd
		for {
			separatorStart := sequenceEnd
			separatorEnd := separatorStart
			for separatorEnd < len(runes) && !isWordRune(runes[separatorEnd]) {
				separatorEnd++
			}
			if separatorEnd == separatorStart || separatorEnd >= len(runes) {
				break
			}
			if !isSplitSeparatorRun(runes[separatorStart:separatorEnd]) {
				break
			}
			nextEnd := separatorEnd + 1
			for nextEnd < len(runes) && isWordRune(runes[nextEnd]) {
				nextEnd++
			}
			if nextEnd-separatorEnd != 1 {
				break
			}
			joined.WriteRune(runes[separatorEnd])
			count++
			sequenceEnd = nextEnd
		}
		if count >= 2 {
			builder.WriteString(joined.String())
			i = sequenceEnd
			continue
		}
		builder.WriteRune(runes[i])
		i = wordEnd
	}
	return builder.String()
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func isSplitSeparatorRun(runes []rune) bool {
	for _, r := range runes {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		return false
	}
	return true
}

func foldCyrillicHomoglyphs(text string) string {
	runes := []rune(text)
	var builder strings.Builder
	builder.Grow(len(text))

	for i := 0; i < len(runes); {
		if !isScriptRune(runes[i]) {
			builder.WriteRune(runes[i])
			i++
			continue
		}

		wordEnd := i
		hasLatin := false
		hasCyrillic := false
		allCyrillicConfusable := true
		for wordEnd < len(runes) && isScriptRune(runes[wordEnd]) {
			r := runes[wordEnd]
			if unicode.Is(unicode.Latin, r) {
				hasLatin = true
			}
			if unicode.Is(unicode.Cyrillic, r) {
				hasCyrillic = true
				if _, ok := cyrillicToLatin[r]; !ok {
					allCyrillicConfusable = false
				}
			}
			wordEnd++
		}

		if hasLatin && hasCyrillic && allCyrillicConfusable {
			for _, r := range runes[i:wordEnd] {
				if latin, ok := cyrillicToLatin[r]; ok {
					builder.WriteRune(latin)
				} else {
					builder.WriteRune(r)
				}
			}
		} else {
			builder.WriteString(string(runes[i:wordEnd]))
		}
		i = wordEnd
	}
	return builder.String()
}

func isScriptRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsMark(r)
}

var cyrillicToLatin = map[rune]rune{
	'а': 'a',
	'в': 'b',
	'е': 'e',
	'к': 'k',
	'м': 'm',
	'н': 'h',
	'о': 'o',
	'р': 'p',
	'с': 'c',
	'т': 't',
	'у': 'y',
	'х': 'x',
	'і': 'i',
	'ј': 'j',
	'ѕ': 's',
	'ԁ': 'd',
	'ԛ': 'q',
	'ԝ': 'w',
	'һ': 'h',
	'ӏ': 'l',
	'ү': 'y',
	'ѵ': 'v',
	'ѡ': 'w',
}

func validStats(stats ModStats) bool {
	values := [...]int{
		stats.Checks,
		stats.Allowed,
		stats.Warned,
		stats.Muted,
		stats.Blocked,
		stats.Oversize,
		stats.Appealed,
		stats.Throttled,
	}
	for _, value := range values {
		if value < 0 {
			return false
		}
	}
	return true
}

func validVerdict(verdict Verdict) bool {
	if verdict.Severity < 0 {
		return false
	}
	switch verdict.Action {
	case ActionAllow, ActionWarn, ActionMute, ActionBlock:
	default:
		return false
	}
	switch verdict.Reason {
	case ReasonClean, ReasonLexiconHit, ReasonWhitelisted, ReasonOversize,
		ReasonRateLimited, ReasonAppealed:
	default:
		return false
	}
	return true
}

func badSnapshot(part string, cause error) error {
	return ErrBadSnapshot
}
