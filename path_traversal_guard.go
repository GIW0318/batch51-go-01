// Package task normalises caller supplied relative paths, confines them to a
// configured root directory, follows links through an injected resolver and
// records every admission decision in an audit ledger.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// Decisions recorded for one path request.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Reason codes attached to a decision and to an audit entry.
const (
	ReasonOK           = "reason_ok"
	ReasonEmpty        = "reason_empty"
	ReasonAbsolute     = "reason_absolute"
	ReasonParentEscape = "reason_parent_escape"
	ReasonOutsideRoot  = "reason_outside_root"
	ReasonLinkEscape   = "reason_link_escape"
	ReasonLinkLoop     = "reason_link_loop"
	ReasonTooLong      = "reason_path_too_long"
	ReasonEncoded      = "reason_encoded_traversal"
)

// Errors returned by the guard.
var (
	ErrEmptyPath     = errors.New("path is empty")
	ErrAbsolutePath  = errors.New("absolute path is not accepted here")
	ErrEscapeAttempt = errors.New("path climbs above the root")
	ErrOutsideRoot   = errors.New("path resolves outside the root")
	ErrLinkEscape    = errors.New("link target resolves outside the root")
	ErrLinkLoop      = errors.New("link resolution did not settle")
	ErrPathTooLong   = errors.New("path exceeds the configured limit")
	ErrBadLimit      = errors.New("limit must be positive")
	ErrBadResolver   = errors.New("link resolver must not be nil")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// GuardOptions configures one guard instance. The root directory itself is
// passed to the constructor.
type GuardOptions struct {
	MaxPathLen      int
	MaxLinkHops     int
	AuditCap        int
	CaseInsensitive bool
}

// ResolvedPath is the outcome of resolving one caller supplied path.
type ResolvedPath struct {
	Request string
	Clean   string
	Real    string
	Layers  int
	Inside  bool
	Reason  string
}

// Handle is the admission token handed back by Open.
type Handle struct {
	Clean    string
	Real     string
	ReadOnly bool
	Seq      int64
}

// AuditEntry is one recorded decision, oldest entries first.
type AuditEntry struct {
	Seq      int64
	Request  string
	Clean    string
	Real     string
	Decision string
	Reason   string
}

// PathTraversalGuard owns the root configuration, the link resolver and the
// audit ledger.
type PathTraversalGuard struct {
	mu       sync.RWMutex
	root     string
	opts     GuardOptions
	resolver func(string) (string, error)
	entries  []AuditEntry
	nextSeq  int64
	denials  int
}

type pathConfig struct {
	root            string
	maxPathLen      int
	maxLinkHops     int
	auditCap        int
	caseInsensitive bool
	resolver        func(string) (string, error)
}

type guardSnapshot struct {
	Version int          `json:"version"`
	NextSeq int64        `json:"next_seq"`
	Denials int          `json:"denials"`
	Entries []AuditEntry `json:"entries"`
}

type guardSnapshotWire struct {
	Version *int          `json:"version"`
	NextSeq *int64        `json:"next_seq"`
	Denials *int          `json:"denials"`
	Entries *[]AuditEntry `json:"entries"`
}

const snapshotVersion = 1

// NewPathTraversalGuard builds a guard for one root directory.
func NewPathTraversalGuard(root string, opts GuardOptions) (*PathTraversalGuard, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrEmptyPath
	}
	if opts.MaxPathLen <= 0 || opts.MaxLinkHops <= 0 || opts.AuditCap <= 0 {
		return nil, ErrBadLimit
	}

	cleanRoot, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	return &PathTraversalGuard{
		root:    cleanRoot,
		opts:    opts,
		nextSeq: 1,
	}, nil
}

// Normalize folds one caller supplied relative path into its canonical form.
func (g *PathTraversalGuard) Normalize(raw string) (string, error) {
	cfg := g.snapshotConfig()
	decoded, _ := decodeStable(raw)
	return normalizeDecoded(decoded, cfg.maxPathLen)
}

// Decode repeatedly unwraps percent escapes and Unicode slash variants until
// the result is stable, reporting how many changing rounds were applied.
func (g *PathTraversalGuard) Decode(raw string) (string, int) {
	return decodeStable(raw)
}

// WithinRoot reports whether an absolute path sits inside the configured root.
func (g *PathTraversalGuard) WithinRoot(abs string) (bool, error) {
	cfg := g.snapshotConfig()
	decoded, _ := decodeStable(abs)
	clean, err := canonicalAbsolute(decoded)
	if err != nil {
		return false, err
	}
	return withinCanonicalRoot(cfg.root, clean, cfg.caseInsensitive), nil
}

// Resolve normalises, follows links and checks the real target against the root.
func (g *PathTraversalGuard) Resolve(raw string) (ResolvedPath, error) {
	cfg := g.snapshotConfig()
	rp, _, err := g.resolve(raw, cfg)
	return rp, err
}

// Open resolves a path and hands back an admission handle when it is allowed.
func (g *PathTraversalGuard) Open(raw string, readOnly bool) (Handle, error) {
	cfg := g.snapshotConfig()
	rp, seq, err := g.resolve(raw, cfg)
	if err != nil {
		return Handle{}, err
	}
	return Handle{
		Clean:    rp.Clean,
		Real:     rp.Real,
		ReadOnly: readOnly,
		Seq:      seq,
	}, nil
}

// SetLinkResolver installs the link resolver used during resolution.
func (g *PathTraversalGuard) SetLinkResolver(fn func(string) (string, error)) error {
	if fn == nil {
		return ErrBadResolver
	}
	g.mu.Lock()
	g.resolver = fn
	g.mu.Unlock()
	return nil
}

// SetMaxPathLen replaces the hard limit applied to normalised paths.
func (g *PathTraversalGuard) SetMaxPathLen(n int) error {
	if n <= 0 {
		return ErrBadLimit
	}
	g.mu.Lock()
	g.opts.MaxPathLen = n
	g.mu.Unlock()
	return nil
}

// Audit returns a copy of the retained audit entries, oldest first.
func (g *PathTraversalGuard) Audit() []AuditEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()

	out := make([]AuditEntry, len(g.entries))
	copy(out, g.entries)
	return out
}

// Rejections reports how many requests were denied since construction.
func (g *PathTraversalGuard) Rejections() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.denials
}

// Snapshot serialises the counters and the retained audit ledger.
func (g *PathTraversalGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	snap := guardSnapshot{
		Version: snapshotVersion,
		NextSeq: g.nextSeq,
		Denials: g.denials,
	}
	snap.Entries = make([]AuditEntry, len(g.entries))
	copy(snap.Entries, g.entries)
	g.mu.RUnlock()

	return json.Marshal(snap)
}

// Restore replaces the counters and the audit ledger from a snapshot, all or
// nothing.
func (g *PathTraversalGuard) Restore(data []byte) error {
	snap, err := parseSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	entries := snap.Entries
	if len(entries) > g.opts.AuditCap {
		entries = entries[len(entries)-g.opts.AuditCap:]
	}
	g.entries = make([]AuditEntry, len(entries))
	copy(g.entries, entries)
	g.nextSeq = snap.NextSeq
	g.denials = snap.Denials
	return nil
}

func (g *PathTraversalGuard) snapshotConfig() pathConfig {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return pathConfig{
		root:            g.root,
		maxPathLen:      g.opts.MaxPathLen,
		maxLinkHops:     g.opts.MaxLinkHops,
		auditCap:        g.opts.AuditCap,
		caseInsensitive: g.opts.CaseInsensitive,
		resolver:        g.resolver,
	}
}

func (g *PathTraversalGuard) resolve(raw string, cfg pathConfig) (ResolvedPath, int64, error) {
	rp := ResolvedPath{Request: raw}
	decoded, layers := decodeStable(raw)
	rp.Layers = layers

	clean, err := normalizeDecoded(decoded, cfg.maxPathLen)
	if err != nil {
		rp.Clean = clean
		rp.Reason = reasonForNormalizeError(err, layers > 0)
		seq := g.record(rp, DecisionDeny, cfg.auditCap, true)
		return rp, seq, err
	}
	rp.Clean = clean

	realPath := joinRoot(cfg.root, clean)
	rp.Real = realPath

	if cfg.resolver != nil {
		settled := false
		current := realPath
		for hop := 0; hop < cfg.maxLinkHops; hop++ {
			next, resolveErr := cfg.resolver(current)
			if resolveErr != nil {
				rp.Inside = false
				rp.Reason = reasonForResolverError(resolveErr)
				seq := g.record(rp, DecisionDeny, cfg.auditCap, true)
				return rp, seq, resolveErr
			}
			if next == current {
				settled = true
				break
			}

			target, targetErr := canonicalLinkTarget(next)
			if targetErr != nil {
				rp.Inside = false
				rp.Reason = ReasonLinkEscape
				seq := g.record(rp, DecisionDeny, cfg.auditCap, true)
				return rp, seq, ErrLinkEscape
			}
			rp.Real = target
			if !withinCanonicalRoot(cfg.root, target, cfg.caseInsensitive) {
				rp.Inside = false
				rp.Reason = ReasonLinkEscape
				seq := g.record(rp, DecisionDeny, cfg.auditCap, true)
				return rp, seq, ErrLinkEscape
			}
			if target == current {
				current = target
				settled = true
				break
			}
			current = target
		}
		if !settled {
			rp.Inside = false
			rp.Reason = ReasonLinkLoop
			seq := g.record(rp, DecisionDeny, cfg.auditCap, true)
			return rp, seq, ErrLinkLoop
		}
		realPath = current
	}

	rp.Real = realPath
	rp.Inside = true
	rp.Reason = ReasonOK
	seq := g.record(rp, DecisionAllow, cfg.auditCap, false)
	return rp, seq, nil
}

func (g *PathTraversalGuard) record(rp ResolvedPath, decision string, auditCap int, denied bool) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	seq := g.nextSeq
	g.nextSeq++
	entry := AuditEntry{
		Seq:      seq,
		Request:  rp.Request,
		Clean:    rp.Clean,
		Real:     rp.Real,
		Decision: decision,
		Reason:   rp.Reason,
	}
	g.entries = append(g.entries, entry)
	if len(g.entries) > auditCap {
		drop := len(g.entries) - auditCap
		copy(g.entries, g.entries[drop:])
		g.entries = g.entries[:auditCap]
	}
	if denied {
		g.denials++
	}
	return seq
}

func normalizeDecoded(decoded string, maxPathLen int) (string, error) {
	if strings.TrimSpace(decoded) == "" {
		return "", ErrEmptyPath
	}
	if isAbsolutePath(decoded) {
		return "", ErrAbsolutePath
	}

	segments := strings.Split(strings.ReplaceAll(decoded, `\`, "/"), "/")
	stack := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch segment {
		case "", ".":
			continue
		case "..":
			if len(stack) == 0 {
				return "", ErrEscapeAttempt
			}
			stack = stack[:len(stack)-1]
		default:
			stack = append(stack, segment)
		}
	}

	clean := strings.Join(stack, "/")
	if len(clean) > maxPathLen {
		return clean, ErrPathTooLong
	}
	return clean, nil
}

func joinRoot(root, clean string) string {
	if clean == "" {
		return root
	}
	if strings.HasSuffix(root, "/") {
		return root + clean
	}
	return root + "/" + clean
}

func canonicalAbsolute(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrEmptyPath
	}
	if !isAbsolutePath(path) {
		return "", ErrAbsolutePath
	}
	return canonicalSlashPath(path), nil
}

func canonicalLinkTarget(target string) (string, error) {
	decoded, _ := decodeStable(target)
	if strings.TrimSpace(decoded) == "" || !isAbsolutePath(decoded) {
		return "", ErrLinkEscape
	}
	clean, err := canonicalAbsolute(decoded)
	if err != nil {
		return "", ErrLinkEscape
	}
	return clean, nil
}

func canonicalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", ErrEmptyPath
	}
	root = replaceSlashVariants(root)
	root = strings.ReplaceAll(root, `\`, "/")
	if isAbsolutePath(root) {
		return canonicalSlashPath(root), nil
	}
	return canonicalRelativeLiteral(root), nil
}

func canonicalSlashPath(path string) string {
	path = strings.ReplaceAll(path, `\`, "/")
	if isDrivePath(path) {
		drive := path[:2]
		parts := cleanAbsoluteSegments(strings.Split(path[2:], "/"))
		if len(parts) == 0 {
			return drive + "/"
		}
		return drive + "/" + strings.Join(parts, "/")
	}
	parts := cleanAbsoluteSegments(strings.Split(path, "/"))
	if len(parts) == 0 {
		return "/"
	}
	return "/" + strings.Join(parts, "/")
}

func cleanAbsoluteSegments(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, part)
		}
	}
	return out
}

func canonicalRelativeLiteral(path string) string {
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 && out[len(out)-1] != ".." {
				out = out[:len(out)-1]
				continue
			}
			out = append(out, part)
		default:
			out = append(out, part)
		}
	}
	return strings.Join(out, "/")
}

func withinCanonicalRoot(root, abs string, caseInsensitive bool) bool {
	if root == "" || abs == "" {
		return false
	}

	prefix := root
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	if abs == root {
		return true
	}
	if caseInsensitive && strings.EqualFold(abs, root) {
		return true
	}
	if len(abs) < len(prefix) {
		return false
	}
	if caseInsensitive {
		return strings.EqualFold(abs[:len(prefix)], prefix)
	}
	return abs[:len(prefix)] == prefix
}

func isAbsolutePath(path string) bool {
	if path == "" {
		return false
	}
	if path[0] == '/' || path[0] == '\\' {
		return true
	}
	return isDrivePath(path)
}

func isDrivePath(path string) bool {
	return len(path) >= 2 && isASCIIAlpha(path[0]) && path[1] == ':'
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func decodeStable(raw string) (string, int) {
	current := raw
	rounds := 0
	for {
		next := decodeRound(current)
		if next == current {
			return current, rounds
		}
		current = next
		rounds++
	}
}

func decodeRound(raw string) string {
	decoded := percentDecode(raw)
	return replaceSlashVariants(decoded)
}

func percentDecode(raw string) string {
	var out strings.Builder
	out.Grow(len(raw))
	changed := false
	for i := 0; i < len(raw); {
		if raw[i] == '%' && i+2 < len(raw) {
			hi, okHi := hexValue(raw[i+1])
			lo, okLo := hexValue(raw[i+2])
			if okHi && okLo {
				out.WriteByte(hi<<4 | lo)
				i += 3
				changed = true
				continue
			}
		}
		out.WriteByte(raw[i])
		i++
	}
	if !changed {
		return raw
	}
	return out.String()
}

func hexValue(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	default:
		return 0, false
	}
}

func replaceSlashVariants(raw string) string {
	original := raw
	changed := false
	var out strings.Builder
	out.Grow(len(raw))
	for len(raw) > 0 {
		r, size := utf8.DecodeRuneInString(raw)
		if size == 0 {
			break
		}
		switch r {
		case '／', '＼', '∕', '⁄', '∖', '⧸', '⧹', '╱', '╲', '﹨':
			out.WriteByte('/')
			changed = true
		default:
			out.WriteRune(r)
		}
		raw = raw[size:]
	}
	if !changed {
		return original
	}
	return out.String()
}

func reasonForNormalizeError(err error, encoded bool) string {
	switch {
	case errors.Is(err, ErrEmptyPath):
		return ReasonEmpty
	case errors.Is(err, ErrAbsolutePath):
		return ReasonAbsolute
	case errors.Is(err, ErrEscapeAttempt):
		if encoded {
			return ReasonEncoded
		}
		return ReasonParentEscape
	case errors.Is(err, ErrOutsideRoot):
		return ReasonOutsideRoot
	case errors.Is(err, ErrPathTooLong):
		return ReasonTooLong
	default:
		return ReasonEncoded
	}
}

func reasonForResolverError(err error) string {
	switch {
	case errors.Is(err, ErrLinkEscape):
		return ReasonLinkEscape
	case errors.Is(err, ErrLinkLoop):
		return ReasonLinkLoop
	case errors.Is(err, ErrOutsideRoot):
		return ReasonOutsideRoot
	case errors.Is(err, ErrEscapeAttempt):
		return ReasonParentEscape
	case errors.Is(err, ErrAbsolutePath):
		return ReasonAbsolute
	default:
		return ReasonLinkEscape
	}
}

func parseSnapshot(data []byte) (guardSnapshot, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return guardSnapshot{}, ErrBadSnapshot
	}

	var wire guardSnapshotWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return guardSnapshot{}, ErrBadSnapshot
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return guardSnapshot{}, ErrBadSnapshot
	}
	if wire.Version == nil || wire.NextSeq == nil || wire.Denials == nil || wire.Entries == nil {
		return guardSnapshot{}, ErrBadSnapshot
	}

	snap := guardSnapshot{
		Version: *wire.Version,
		NextSeq: *wire.NextSeq,
		Denials: *wire.Denials,
		Entries: *wire.Entries,
	}

	if snap.Version != snapshotVersion || snap.NextSeq <= 0 || snap.Denials < 0 || snap.Entries == nil {
		return guardSnapshot{}, ErrBadSnapshot
	}
	if int64(snap.Denials) > snap.NextSeq-1 {
		return guardSnapshot{}, ErrBadSnapshot
	}

	lastSeq := int64(0)
	denyEntries := 0
	for _, entry := range snap.Entries {
		if entry.Seq <= lastSeq || entry.Seq >= snap.NextSeq {
			return guardSnapshot{}, ErrBadSnapshot
		}
		lastSeq = entry.Seq
		switch entry.Decision {
		case DecisionAllow:
			if entry.Reason != ReasonOK {
				return guardSnapshot{}, ErrBadSnapshot
			}
		case DecisionDeny:
			if !isDenyReason(entry.Reason) {
				return guardSnapshot{}, ErrBadSnapshot
			}
			denyEntries++
		default:
			return guardSnapshot{}, ErrBadSnapshot
		}
	}
	if denyEntries > snap.Denials {
		return guardSnapshot{}, ErrBadSnapshot
	}
	return snap, nil
}

func isDenyReason(reason string) bool {
	switch reason {
	case ReasonEmpty, ReasonAbsolute, ReasonParentEscape, ReasonOutsideRoot,
		ReasonLinkEscape, ReasonLinkLoop, ReasonTooLong, ReasonEncoded:
		return true
	default:
		return false
	}
}
