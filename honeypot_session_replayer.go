// Package task turns captured interactive intrusion traffic into a replayable
// timeline of commands and their output.
package task

import "errors"

// errNotImplemented marks behaviour that still has to be supplied.
var errNotImplemented = errors.New("not implemented")

// Chunk directions found in a captured session.
const (
	DirIn  = "in"
	DirOut = "out"
)

// Errors reported by the replayer.
var (
	ErrBadDirection   = errors.New("chunk direction is neither in nor out")
	ErrEmptyConn      = errors.New("connection identifier is empty")
	ErrUnknownSession = errors.New("session was never ingested")
	ErrBadThreshold   = errors.New("idle threshold must not be negative")
)

// Chunk is one captured piece of traffic. Dir is DirIn for keystrokes sent to
// the target and DirOut for everything the target wrote back.
type Chunk struct {
	Dir  string `json:"dir"`
	AtMs int64  `json:"at_ms"`
	Data string `json:"data"`
}

// SessionMeta identifies the connection and the identity seen on it.
type SessionMeta struct {
	Conn     string `json:"conn"`
	Identity string `json:"identity"`
	Closed   bool   `json:"closed"`
}

// Command is one boundary found in the inbound stream.
type Command struct {
	Index      int    `json:"index"`
	Text       string `json:"text"`
	AtMs       int64  `json:"at_ms"`
	Incomplete bool   `json:"incomplete"`
}

// Entry is one command together with the output that followed it.
type Entry struct {
	Command string `json:"command"`
	Output  string `json:"output"`
	AtMs    int64  `json:"at_ms"`
	GapMs   int64  `json:"gap_ms"`
}

// Timeline is the replayed view of one session.
type Timeline struct {
	Session    string  `json:"session"`
	Entries    []Entry `json:"entries"`
	Collapsed  int     `json:"collapsed"`
	Incomplete int     `json:"incomplete"`
}

// Stats summarises what one ingested session contains.
type Stats struct {
	Chunks      int `json:"chunks"`
	Commands    int `json:"commands"`
	OutputBytes int `json:"output_bytes"`
	Repeated    int `json:"repeated"`
}

// HoneypotSessionReplayer keeps every ingested session keyed by connection and
// identity.
type HoneypotSessionReplayer struct {
	idleMs   int64
	order    []string
	sessions map[string]*sessionRecord
}

type sessionRecord struct {
	meta   SessionMeta
	chunks []Chunk
}

// NewHoneypotSessionReplayer builds a replayer whose idle cap is idleMs.
func NewHoneypotSessionReplayer(idleMs int64) *HoneypotSessionReplayer {
	return &HoneypotSessionReplayer{idleMs: idleMs, sessions: map[string]*sessionRecord{}}
}

// Ingest stores a captured session and returns its key. Importing the same
// session twice keeps a single copy.
func (r *HoneypotSessionReplayer) Ingest(meta SessionMeta, chunks []Chunk) (string, error) {
	return "", errNotImplemented
}

// Sessions lists the ingested session keys in import order.
func (r *HoneypotSessionReplayer) Sessions() []string {
	return nil
}

// Segment splits the inbound stream of one session into commands.
func (r *HoneypotSessionReplayer) Segment(key string) ([]Command, error) {
	return nil, errNotImplemented
}

// Timeline builds the command and output timeline of one session.
func (r *HoneypotSessionReplayer) Timeline(key string) (Timeline, error) {
	return Timeline{}, errNotImplemented
}

// Replay returns the timeline with idle time compressed to the configured cap.
func (r *HoneypotSessionReplayer) Replay(key string) ([]Entry, error) {
	return nil, errNotImplemented
}

// Stats summarises one ingested session.
func (r *HoneypotSessionReplayer) Stats(key string) (Stats, error) {
	return Stats{}, errNotImplemented
}

// Normalize rewrites a raw terminal fragment into plain text lines.
func (r *HoneypotSessionReplayer) Normalize(raw string) string {
	return ""
}

// CollapseRepeats folds consecutive identical lines and reports how many were
// folded away.
func (r *HoneypotSessionReplayer) CollapseRepeats(lines []string) ([]string, int) {
	return nil, 0
}

// SessionKey joins a connection and an identity into the storage key.
func (r *HoneypotSessionReplayer) SessionKey(conn, identity string) string {
	return ""
}

// IdleGaps reports the positions whose predecessor is further away than the
// threshold.
func (r *HoneypotSessionReplayer) IdleGaps(atMs []int64, thresholdMs int64) []int {
	return nil
}

// DedupChunks drops exact duplicate chunks while keeping the original order.
func (r *HoneypotSessionReplayer) DedupChunks(chunks []Chunk) []Chunk {
	return nil
}
