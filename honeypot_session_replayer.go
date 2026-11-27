// Package task turns captured interactive intrusion traffic into a replayable
// timeline of commands and their output.
package task

import (
	"errors"
	"strings"
	"sync"
	"unicode/utf8"
)

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
	mu       sync.RWMutex
	idleMs   int64
	order    []string
	sessions map[string]*sessionRecord
}

type sessionRecord struct {
	meta   SessionMeta
	chunks []Chunk
}

// Escape-parser states for lineEditor.
const (
	escNormal = iota
	escEsc
	escCSI
	escOSC
)

// lineEditor replays a raw terminal byte stream. ANSI escape sequences are
// dropped, CRLF and a bare carriage return become line boundaries (the line
// discipline maps the Enter key to a newline) and backspace erases the cell
// before the cursor. The editor never mutates the input it is fed.
type lineEditor struct {
	cells     []rune
	col       int
	lines     []string
	esc       int
	pendingCR bool
}

func (e *lineEditor) Write(raw string) {
	i := 0
	for i < len(raw) {
		b := raw[i]
		switch e.esc {
		case escCSI:
			i++
			if b >= 0x40 && b <= 0x7e {
				e.esc = escNormal
			}
			continue
		case escOSC:
			i++
			switch b {
			case 0x07:
				e.esc = escNormal
			case 0x1b:
				e.esc = escEsc
			}
			continue
		case escEsc:
			i++
			switch b {
			case '[':
				e.esc = escCSI
			case ']':
				e.esc = escOSC
			default:
				e.esc = escNormal
			}
			continue
		}
		if b == 0x1b {
			e.esc = escEsc
			i++
			continue
		}
		if e.pendingCR {
			e.pendingCR = false
			e.flush()
			if b == '\n' {
				i++
				continue
			}
		}
		if b < 0x80 {
			e.handleByte(b)
			i++
		} else {
			r, size := utf8.DecodeRuneInString(raw[i:])
			e.print(r)
			i += size
		}
	}
}

func (e *lineEditor) handleByte(b byte) {
	switch {
	case b == '\n':
		e.flush()
	case b == '\r':
		e.pendingCR = true
	case b == '\b' || b == 0x7f:
		if e.col > 0 {
			e.cells = append(e.cells[:e.col-1], e.cells[e.col:]...)
			e.col--
		}
	case b == '\t' || b >= 0x20:
		e.print(rune(b))
	default:
		// Other C0 control characters carry no command text.
	}
}

func (e *lineEditor) print(r rune) {
	if e.col == len(e.cells) {
		e.cells = append(e.cells, r)
	} else {
		e.cells[e.col] = r
	}
	e.col++
}

func (e *lineEditor) flush() {
	e.lines = append(e.lines, string(e.cells))
	e.cells = nil
	e.col = 0
}

// Text renders the normalized stream: finished lines joined by newlines, plus
// any unterminated line at the end without a trailing newline.
func (e *lineEditor) Text() string {
	if e.pendingCR {
		e.pendingCR = false
		e.flush()
	}
	var sb strings.Builder
	for _, line := range e.lines {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if len(e.cells) > 0 {
		sb.WriteString(string(e.cells))
	}
	return sb.String()
}

func (e *lineEditor) pending() string {
	if e.pendingCR {
		e.pendingCR = false
		e.flush()
	}
	if len(e.cells) == 0 {
		return ""
	}
	return string(e.cells)
}

// linesSnapshot returns every finished line, finalizing a trailing carriage
// return as its own line boundary first.
func (e *lineEditor) linesSnapshot() []string {
	if e.pendingCR {
		e.pendingCR = false
		e.flush()
	}
	return e.lines
}

// NewHoneypotSessionReplayer builds a replayer whose idle cap is idleMs.
func NewHoneypotSessionReplayer(idleMs int64) *HoneypotSessionReplayer {
	return &HoneypotSessionReplayer{idleMs: idleMs, sessions: map[string]*sessionRecord{}}
}

// Ingest stores a captured session and returns its key. Importing the same
// session twice keeps a single copy.
func (r *HoneypotSessionReplayer) Ingest(meta SessionMeta, chunks []Chunk) (string, error) {
	if meta.Conn == "" {
		return "", ErrEmptyConn
	}
	for _, chunk := range chunks {
		if chunk.Dir != DirIn && chunk.Dir != DirOut {
			return "", ErrBadDirection
		}
	}

	key := r.SessionKey(meta.Conn, meta.Identity)
	cleaned := r.DedupChunks(chunks)
	stored := append([]Chunk(nil), cleaned...)

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[key]; ok {
		return key, nil
	}
	r.sessions[key] = &sessionRecord{meta: meta, chunks: stored}
	r.order = append(r.order, key)
	return key, nil
}

// Sessions lists the ingested session keys in import order.
func (r *HoneypotSessionReplayer) Sessions() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// Segment splits the inbound stream of one session into commands.
func (r *HoneypotSessionReplayer) Segment(key string) ([]Command, error) {
	r.mu.RLock()
	rec, ok := r.sessions[key]
	r.mu.RUnlock()
	if !ok {
		return nil, ErrUnknownSession
	}

	var editor lineEditor
	commands := make([]Command, 0)
	var lastInAt int64
	for _, chunk := range rec.chunks {
		if chunk.Dir != DirIn {
			continue
		}
		lastInAt = chunk.AtMs
		flushed := len(editor.linesSnapshot())
		editor.Write(chunk.Data)
		finished := editor.linesSnapshot()
		for ; flushed < len(finished); flushed++ {
			text := finished[flushed]
			if text == "" {
				continue
			}
			commands = append(commands, Command{
				Index: len(commands),
				Text:  text,
				AtMs:  chunk.AtMs,
			})
		}
	}
	if text := editor.pending(); text != "" {
		commands = append(commands, Command{
			Index:      len(commands),
			Text:       text,
			AtMs:       lastInAt,
			Incomplete: true,
		})
	}
	return commands, nil
}

// buildEntries walks the raw chunks once, reconstructing commands and attaching
// every outbound chunk to the command currently in effect.
func (r *HoneypotSessionReplayer) buildEntries(key string) ([]Entry, int, int, error) {
	r.mu.RLock()
	rec, ok := r.sessions[key]
	r.mu.RUnlock()
	if !ok {
		return nil, 0, 0, ErrUnknownSession
	}

	var editor lineEditor
	entries := make([]Entry, 0)
	incomplete := 0
	var lastInAt int64
	for _, chunk := range rec.chunks {
		switch chunk.Dir {
		case DirIn:
			lastInAt = chunk.AtMs
			flushed := len(editor.linesSnapshot())
			editor.Write(chunk.Data)
			finished := editor.linesSnapshot()
			for ; flushed < len(finished); flushed++ {
				text := finished[flushed]
				if text == "" {
					continue
				}
				entries = append(entries, Entry{Command: text, AtMs: chunk.AtMs})
			}
		case DirOut:
			if len(entries) > 0 {
				entries[len(entries)-1].Output += chunk.Data
			}
		}
	}
	if text := editor.pending(); text != "" {
		entries = append(entries, Entry{Command: text, AtMs: lastInAt})
		incomplete++
	}

	gapCap := r.idleMs
	if gapCap < 0 {
		gapCap = 0
	}
	for i := range entries {
		if i == 0 {
			entries[i].GapMs = 0
			continue
		}
		gap := entries[i].AtMs - entries[i-1].AtMs
		if gap < 0 {
			gap = 0
		}
		if gap > gapCap {
			gap = gapCap
		}
		entries[i].GapMs = gap
	}
	texts := make([]string, len(entries))
	for i, entry := range entries {
		texts[i] = entry.Command
	}
	_, folded := r.CollapseRepeats(texts)
	return entries, folded, incomplete, nil
}

// Timeline builds the command and output timeline of one session.
func (r *HoneypotSessionReplayer) Timeline(key string) (Timeline, error) {
	entries, collapsed, incomplete, err := r.buildEntries(key)
	if err != nil {
		return Timeline{}, err
	}
	return Timeline{
		Session:    key,
		Entries:    entries,
		Collapsed:  collapsed,
		Incomplete: incomplete,
	}, nil
}

// Replay returns the timeline with idle time compressed to the configured cap.
func (r *HoneypotSessionReplayer) Replay(key string) ([]Entry, error) {
	entries, _, _, err := r.buildEntries(key)
	if err != nil {
		return nil, err
	}
	replayed := make([]Entry, len(entries))
	copy(replayed, entries)
	var offset int64
	for i := range replayed {
		offset += replayed[i].GapMs
		replayed[i].AtMs = offset
	}
	return replayed, nil
}

// Stats summarises one ingested session.
func (r *HoneypotSessionReplayer) Stats(key string) (Stats, error) {
	r.mu.RLock()
	rec, ok := r.sessions[key]
	r.mu.RUnlock()
	if !ok {
		return Stats{}, ErrUnknownSession
	}

	commands, err := r.Segment(key)
	if err != nil {
		return Stats{}, err
	}
	var outputBytes int
	for _, chunk := range rec.chunks {
		if chunk.Dir == DirOut {
			outputBytes += len(chunk.Data)
		}
	}
	texts := make([]string, len(commands))
	for i, command := range commands {
		texts[i] = command.Text
	}
	_, repeated := r.CollapseRepeats(texts)
	return Stats{
		Chunks:      len(rec.chunks),
		Commands:    len(commands),
		OutputBytes: outputBytes,
		Repeated:    repeated,
	}, nil
}

// Normalize rewrites a raw terminal fragment into plain text lines.
func (r *HoneypotSessionReplayer) Normalize(raw string) string {
	var editor lineEditor
	editor.Write(raw)
	return editor.Text()
}

// CollapseRepeats folds consecutive identical lines and reports how many were
// folded away.
func (r *HoneypotSessionReplayer) CollapseRepeats(lines []string) ([]string, int) {
	kept := make([]string, 0, len(lines))
	folded := 0
	for _, line := range lines {
		if len(kept) > 0 && kept[len(kept)-1] == line {
			folded++
			continue
		}
		kept = append(kept, line)
	}
	return kept, folded
}

// SessionKey joins a connection and an identity into the storage key.
func (r *HoneypotSessionReplayer) SessionKey(conn, identity string) string {
	return conn + "|" + identity
}

// IdleGaps reports the positions whose predecessor is further away than the
// threshold. The threshold comparison is greater-than-or-equal and the
// returned index is the later element of each gap.
func (r *HoneypotSessionReplayer) IdleGaps(atMs []int64, thresholdMs int64) []int {
	gaps := make([]int, 0)
	for i := 1; i < len(atMs); i++ {
		if atMs[i]-atMs[i-1] >= thresholdMs {
			gaps = append(gaps, i)
		}
	}
	return gaps
}

// DedupChunks drops exact duplicate chunks while keeping the original order.
func (r *HoneypotSessionReplayer) DedupChunks(chunks []Chunk) []Chunk {
	seen := make(map[Chunk]bool, len(chunks))
	out := make([]Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		if seen[chunk] {
			continue
		}
		seen[chunk] = true
		out = append(out, chunk)
	}
	return out
}
