// Package task screens command executions before they reach a shell: the
// program is resolved through a modelled symlink table and matched against a
// whitelist by its real path, every argument is scanned for shell
// metacharacters, environment assignments are validated against a key
// allowlist, and every verdict lands in a bounded audit trail.
package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
)

// Verdict codes.
const (
	CodeAllowed        = "cma_allowed"
	CodeNotWhitelisted = "cma_not_whitelisted"
	CodeArgLimit       = "cma_arg_limit"
	CodeMetachar       = "cma_metachar"
	CodeLinkEscape     = "cma_link_escape"
	CodeEnvKey         = "cma_env_key"
)

// Errors returned by the screener.
var (
	ErrBadInvocation   = errors.New("invocation is incomplete")
	ErrNotAbsolute     = errors.New("program path must be absolute")
	ErrBadAllowRule    = errors.New("whitelist entry is incomplete")
	ErrProgramLimit    = errors.New("whitelist table is full")
	ErrLinkCycle       = errors.New("symlink table contains a cycle")
	ErrLinkDepth       = errors.New("symlink chain exceeds the hop limit")
	ErrMetachar        = errors.New("argument carries a shell metacharacter")
	ErrEnvKey          = errors.New("environment key is not allowlisted")
	ErrEnvSubstitution = errors.New("environment value carries command substitution")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// AllowOptions is the per program part of a whitelist entry.
type AllowOptions struct {
	MaxArgs  int      `json:"max_args"`
	AllowEnv []string `json:"allow_env"`
}

// Invocation is one execution request captured by the broker.
type Invocation struct {
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Env     []string `json:"env"`
	Source  string   `json:"source"`
}

// Verdict is the screening outcome for one Invocation.
type Verdict struct {
	Allowed bool   `json:"allowed"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Program string `json:"program"`
	Seq     int64  `json:"seq"`
}

// AuditEvent is one line of the screening trail.
type AuditEvent struct {
	Seq     int64  `json:"seq"`
	Source  string `json:"source"`
	Program string `json:"program"`
	Code    string `json:"code"`
	Allowed bool   `json:"allowed"`
	Argc    int    `json:"argc"`
}

// AuditConfig bounds the whitelist, the argument count and the audit trail.
type AuditConfig struct {
	MaxPrograms    int
	MaxArgs        int
	MaxAudit       int
	MaxLinkHops    int
	AllowedEnvKeys []string
}

// CmdInjectionAudit owns the whitelist, the modelled symlink table, the per
// source refusal counters and the audit trail.
type CmdInjectionAudit struct {
	mu      sync.RWMutex
	cfg     AuditConfig
	allowed map[string]AllowOptions
	links   map[string]string
	denied  map[string]int
	seq     int64
	audit   []AuditEvent
}

// NewCmdInjectionAudit builds a screener with the given bounds.
func NewCmdInjectionAudit(cfg AuditConfig) *CmdInjectionAudit {
	cfg.AllowedEnvKeys = append([]string(nil), cfg.AllowedEnvKeys...)
	return &CmdInjectionAudit{
		cfg:     cfg,
		allowed: make(map[string]AllowOptions),
		links:   make(map[string]string),
		denied:  make(map[string]int),
	}
}

// AllowProgram adds one absolute program path to the whitelist.
func (s *CmdInjectionAudit) AllowProgram(program string, opts AllowOptions) error {
	if program == "" || opts.MaxArgs < 0 {
		return ErrBadAllowRule
	}
	if !path.IsAbs(program) {
		return ErrNotAbsolute
	}

	program = path.Clean(program)
	opts.AllowEnv = append([]string(nil), opts.AllowEnv...)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.allowed[program]; exists {
		return nil
	}
	if s.cfg.MaxPrograms <= 0 || len(s.allowed) >= s.cfg.MaxPrograms {
		return ErrProgramLimit
	}
	s.allowed[program] = opts
	return nil
}

// Programs lists the whitelisted real paths in ascending order.
func (s *CmdInjectionAudit) Programs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	programs := make([]string, 0, len(s.allowed))
	for program := range s.allowed {
		programs = append(programs, program)
	}
	sort.Strings(programs)
	return programs
}

// RegisterLink records one symlink so later resolutions can follow it.
func (s *CmdInjectionAudit) RegisterLink(link, target string) error {
	if link == "" || target == "" {
		return ErrBadInvocation
	}
	if !path.IsAbs(link) || !path.IsAbs(target) {
		return ErrNotAbsolute
	}

	link = path.Clean(link)
	target = path.Clean(target)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.links[link] = target
	return nil
}

// Resolve follows the modelled symlink table until it reaches a real path.
func (s *CmdInjectionAudit) Resolve(program string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resolveLocked(program)
}

// Check screens one invocation and records the verdict.
func (s *CmdInjectionAudit) Check(inv Invocation) (Verdict, error) {
	if inv.Program == "" {
		return Verdict{}, ErrBadInvocation
	}
	if !path.IsAbs(inv.Program) {
		return Verdict{}, ErrNotAbsolute
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkLocked(inv), nil
}

// ScanArgs reports the first argument that carries a shell metacharacter.
func (s *CmdInjectionAudit) ScanArgs(args []string) error {
	for _, arg := range args {
		if hasShellMetachar(arg) {
			return ErrMetachar
		}
	}
	return nil
}

// CheckEnv validates environment assignments against the key allowlist.
func (s *CmdInjectionAudit) CheckEnv(env []string) error {
	s.mu.RLock()
	allowed := make(map[string]struct{}, len(s.cfg.AllowedEnvKeys))
	for _, key := range s.cfg.AllowedEnvKeys {
		if key != "" {
			allowed[key] = struct{}{}
		}
	}
	s.mu.RUnlock()
	return validateEnv(env, allowed)
}

// Escape renders arguments as single quoted shell words.
func (s *CmdInjectionAudit) Escape(args []string) (string, error) {
	if len(args) == 0 {
		return "", ErrBadInvocation
	}

	var b strings.Builder
	for i, arg := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(arg, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String(), nil
}

// Denied reports how many invocations from one source were refused.
func (s *CmdInjectionAudit) Denied(source string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.denied[source]
}

// Audit returns a copy of the screening trail, oldest first.
func (s *CmdInjectionAudit) Audit() []AuditEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]AuditEvent(nil), s.audit...)
}

// Snapshot serialises the whitelist, the symlink table, the refusal counters
// and the audit sequence watermark.
func (s *CmdInjectionAudit) Snapshot() ([]byte, error) {
	type allowEntry struct {
		Path    string       `json:"path"`
		Options AllowOptions `json:"options"`
	}
	type linkEntry struct {
		Link   string `json:"link"`
		Target string `json:"target"`
	}
	type snapshot struct {
		Version   int            `json:"version"`
		Whitelist []allowEntry   `json:"whitelist"`
		Links     []linkEntry    `json:"links"`
		Denied    map[string]int `json:"denied"`
		Seq       int64          `json:"seq"`
		Audit     []AuditEvent   `json:"audit,omitempty"`
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := snapshot{
		Version:   1,
		Whitelist: make([]allowEntry, 0, len(s.allowed)),
		Links:     make([]linkEntry, 0, len(s.links)),
		Denied:    make(map[string]int, len(s.denied)),
		Seq:       s.seq,
		Audit:     append([]AuditEvent(nil), s.audit...),
	}
	for program, opts := range s.allowed {
		snap.Whitelist = append(snap.Whitelist, allowEntry{
			Path: program,
			Options: AllowOptions{
				MaxArgs:  opts.MaxArgs,
				AllowEnv: append([]string(nil), opts.AllowEnv...),
			},
		})
	}
	for link, target := range s.links {
		snap.Links = append(snap.Links, linkEntry{Link: link, Target: target})
	}
	for source, count := range s.denied {
		snap.Denied[source] = count
	}
	sort.Slice(snap.Whitelist, func(i, j int) bool { return snap.Whitelist[i].Path < snap.Whitelist[j].Path })
	sort.Slice(snap.Links, func(i, j int) bool { return snap.Links[i].Link < snap.Links[j].Link })

	return json.Marshal(snap)
}

// Restore replaces the whole screener state from a snapshot, all or nothing.
func (s *CmdInjectionAudit) Restore(data []byte) error {
	type allowEntry struct {
		Path    string       `json:"path"`
		Options AllowOptions `json:"options"`
	}
	type linkEntry struct {
		Link   string `json:"link"`
		Target string `json:"target"`
	}
	type snapshot struct {
		Version   int            `json:"version"`
		Whitelist []allowEntry   `json:"whitelist"`
		Links     []linkEntry    `json:"links"`
		Denied    map[string]int `json:"denied"`
		Seq       int64          `json:"seq"`
		Audit     []AuditEvent   `json:"audit"`
	}

	s.mu.RLock()
	cfg := s.cfg
	cfg.AllowedEnvKeys = append([]string(nil), s.cfg.AllowedEnvKeys...)
	s.mu.RUnlock()

	var snap snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snap); err != nil {
		return ErrBadSnapshot
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrBadSnapshot
	}
	if snap.Version != 1 || snap.Whitelist == nil || snap.Links == nil || snap.Denied == nil || snap.Seq < 0 {
		return ErrBadSnapshot
	}
	if len(snap.Whitelist) > cfg.MaxPrograms {
		return ErrBadSnapshot
	}

	allowed := make(map[string]AllowOptions, len(snap.Whitelist))
	for _, entry := range snap.Whitelist {
		if entry.Path == "" || entry.Options.MaxArgs < 0 || !path.IsAbs(entry.Path) {
			return ErrBadSnapshot
		}
		program := path.Clean(entry.Path)
		if _, duplicate := allowed[program]; duplicate {
			return ErrBadSnapshot
		}
		allowed[program] = AllowOptions{
			MaxArgs:  entry.Options.MaxArgs,
			AllowEnv: append([]string(nil), entry.Options.AllowEnv...),
		}
	}

	links := make(map[string]string, len(snap.Links))
	for _, entry := range snap.Links {
		if entry.Link == "" || entry.Target == "" || !path.IsAbs(entry.Link) || !path.IsAbs(entry.Target) {
			return ErrBadSnapshot
		}
		link := path.Clean(entry.Link)
		target := path.Clean(entry.Target)
		if _, duplicate := links[link]; duplicate {
			return ErrBadSnapshot
		}
		links[link] = target
	}

	denied := make(map[string]int, len(snap.Denied))
	for source, count := range snap.Denied {
		if count < 0 {
			return ErrBadSnapshot
		}
		denied[source] = count
	}

	var previous int64
	for _, event := range snap.Audit {
		if event.Seq <= previous || event.Seq > snap.Seq || event.Argc < 0 {
			return ErrBadSnapshot
		}
		previous = event.Seq
	}
	audit := append([]AuditEvent(nil), snap.Audit...)
	if cfg.MaxAudit <= 0 {
		audit = nil
	} else if len(audit) > cfg.MaxAudit {
		audit = append([]AuditEvent(nil), audit[len(audit)-cfg.MaxAudit:]...)
	}

	s.mu.Lock()
	s.allowed = allowed
	s.links = links
	s.denied = denied
	s.seq = snap.Seq
	s.audit = audit
	s.mu.Unlock()
	return nil
}

func (s *CmdInjectionAudit) resolveLocked(program string) (string, error) {
	if program == "" {
		return "", ErrBadInvocation
	}
	if !path.IsAbs(program) {
		return "", ErrNotAbsolute
	}

	current := path.Clean(program)
	seen := make(map[string]struct{})
	hops := 0
	for {
		if _, duplicate := seen[current]; duplicate {
			return "", ErrLinkCycle
		}
		seen[current] = struct{}{}

		next, isLink := s.links[current]
		if !isLink {
			return current, nil
		}
		if _, duplicate := seen[next]; duplicate {
			return "", ErrLinkCycle
		}
		hops++
		if hops > s.cfg.MaxLinkHops {
			return "", ErrLinkDepth
		}
		current = next
	}
}

func (s *CmdInjectionAudit) checkLocked(inv Invocation) Verdict {
	program := path.Clean(inv.Program)
	argc := len(inv.Args)

	if hasShellMetachar(program) {
		return s.recordLocked(inv.Source, program, program, CodeMetachar, false, argc)
	}

	resolved, err := s.resolveLocked(program)
	if err != nil {
		return s.recordLocked(inv.Source, program, program, CodeLinkEscape, false, argc)
	}

	entry, allowed := s.allowed[resolved]
	if !allowed {
		code := CodeNotWhitelisted
		if _, rawAllowed := s.allowed[program]; rawAllowed && program != resolved {
			code = CodeLinkEscape
		}
		return s.recordLocked(inv.Source, resolved, resolved, code, false, argc)
	}

	maxArgs := entry.MaxArgs
	if maxArgs == 0 {
		maxArgs = s.cfg.MaxArgs
	}
	if argc > maxArgs {
		return s.recordLocked(inv.Source, resolved, resolved, CodeArgLimit, false, argc)
	}
	if s.ScanArgs(inv.Args) != nil {
		return s.recordLocked(inv.Source, resolved, resolved, CodeMetachar, false, argc)
	}

	envKeys := make(map[string]struct{}, len(s.cfg.AllowedEnvKeys)+len(entry.AllowEnv))
	for _, key := range s.cfg.AllowedEnvKeys {
		if key != "" {
			envKeys[key] = struct{}{}
		}
	}
	for _, key := range entry.AllowEnv {
		if key != "" {
			envKeys[key] = struct{}{}
		}
	}
	if err := validateEnv(inv.Env, envKeys); err != nil {
		code := CodeEnvKey
		if errors.Is(err, ErrEnvSubstitution) {
			code = CodeMetachar
		}
		return s.recordLocked(inv.Source, resolved, resolved, code, false, argc)
	}

	return s.recordLocked(inv.Source, resolved, resolved, CodeAllowed, true, argc)
}

func (s *CmdInjectionAudit) recordLocked(source, auditProgram, verdictProgram, code string, allowed bool, argc int) Verdict {
	s.seq++
	event := AuditEvent{
		Seq:     s.seq,
		Source:  source,
		Program: auditProgram,
		Code:    code,
		Allowed: allowed,
		Argc:    argc,
	}
	if s.cfg.MaxAudit > 0 {
		if len(s.audit) >= s.cfg.MaxAudit {
			copy(s.audit, s.audit[len(s.audit)-s.cfg.MaxAudit+1:])
			s.audit = s.audit[:s.cfg.MaxAudit-1]
		}
		s.audit = append(s.audit, event)
	}
	if !allowed {
		s.denied[source]++
	}

	return Verdict{
		Allowed: allowed,
		Code:    code,
		Reason:  code,
		Program: verdictProgram,
		Seq:     s.seq,
	}
}

func hasShellMetachar(value string) bool {
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case ';', '|', '&', '<', '>', '(', ')', '[', ']', '{', '}', '`', '\n', '\r', '\t':
			return true
		case '$':
			if i+1 < len(value) && value[i+1] == '(' {
				return true
			}
		}
	}
	return false
}

func validateEnv(env []string, allowed map[string]struct{}) error {
	for _, assignment := range env {
		separator := strings.IndexByte(assignment, '=')
		if separator < 0 {
			return ErrBadInvocation
		}
		key := assignment[:separator]
		if _, ok := allowed[key]; !ok {
			return ErrEnvKey
		}
		value := assignment[separator+1:]
		if strings.Contains(value, "$(") || strings.ContainsRune(value, '`') {
			return ErrEnvSubstitution
		}
	}
	return nil
}
