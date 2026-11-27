// Package task moderates world, guild and private chat: it normalises the
// message text, scans the live lexicon, grades the outcome and remembers the
// appeal decisions across restarts.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

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

// ChatContentModerator owns the lexicon, the policy and the verdict memory.
type ChatContentModerator struct {
	limits ModLimits
}

// NewChatContentModerator builds a moderator from the caps and the policy.
func NewChatContentModerator(limits ModLimits, p Policy) *ChatContentModerator {
	return &ChatContentModerator{limits: limits}
}

// SetLexicon installs a new lexicon version in one atomic swap.
func (m *ChatContentModerator) SetLexicon(ver string, entries []LexEntry) error {
	return errNotImplemented
}

// Check grades one message.
func (m *ChatContentModerator) Check(msg ChatMsg) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Explain lists the lexicon matches of one message.
func (m *ChatContentModerator) Explain(msg ChatMsg) []Hit {
	return nil
}

// SetPolicy installs a new moderation policy.
func (m *ChatContentModerator) SetPolicy(p Policy) error {
	return errNotImplemented
}

// Appeal records the appeal of one moderated message.
func (m *ChatContentModerator) Appeal(msgID string, reason string) error {
	return errNotImplemented
}

// SetRateLimit bounds how many messages one sender may submit per window.
func (m *ChatContentModerator) SetRateLimit(n int, window time.Duration) error {
	return errNotImplemented
}

// Stats returns a copy of the moderation counters.
func (m *ChatContentModerator) Stats() ModStats {
	return ModStats{}
}

// Snapshot serialises the lexicon, the verdicts and the counters.
func (m *ChatContentModerator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the moderator state with a snapshot, all or nothing.
func (m *ChatContentModerator) Restore(data []byte) error {
	return errNotImplemented
}
