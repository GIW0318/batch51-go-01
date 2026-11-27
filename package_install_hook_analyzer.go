// Package task grades the install hooks declared by a package manifest.
package task

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Hook stages.
const (
	StagePreinstall  = "preinstall"
	StagePostinstall = "postinstall"
)

// Capability names produced by DetectCapabilities.
const (
	CapNetworkDownload = "network_download"
	CapProcessExec     = "process_exec"
	CapCredentialRead  = "credential_read"
	CapEnvWrite        = "env_write"
	CapFileWrite       = "file_write"
)

// Assessment levels.
const (
	LevelCritical = "critical"
	LevelHigh     = "high"
	LevelMedium   = "medium"
	LevelLow      = "low"
)

// Assessment verdicts.
const (
	VerdictMalicious    = "malicious"
	VerdictSuspicious   = "suspicious"
	VerdictBenign       = "benign"
	VerdictUndetermined = "undetermined"
)

// Errors reported by the analyzer.
var (
	ErrBadStage      = errors.New("hook stage is not supported")
	ErrMissingScript = errors.New("hook script is not part of the package")
	ErrEmptyBlob     = errors.New("payload blob is empty")
	ErrBadDepth      = errors.New("depth budget must be positive")
)

// Manifest is the extracted package description.
type Manifest struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
	Hooks   []Hook            `json:"hooks"`
}

// Hook is one install time script declared by the manifest.
type Hook struct {
	Stage    string `json:"stage"`
	Script   string `json:"script"`
	Platform string `json:"platform"`
	Encoded  bool   `json:"encoded"`
}

// Evidence is one capability observed in one file.
type Evidence struct {
	Capability string `json:"capability"`
	Source     string `json:"source"`
	Snippet    string `json:"snippet"`
	Truncated  bool   `json:"truncated"`
}

// Assessment is the graded outcome for one package.
type Assessment struct {
	Package  string     `json:"package"`
	Score    int        `json:"score"`
	Level    string     `json:"level"`
	Verdict  string     `json:"verdict"`
	Opaque   bool       `json:"opaque"`
	Evidence []Evidence `json:"evidence"`
}

// Budget bounds decoding work.
type Budget struct {
	MaxDepth int
	MaxBytes int
}

// PackageInstallHookAnalyzer grades packages using the configured budget.
type PackageInstallHookAnalyzer struct {
	budget Budget
}

// NewPackageInstallHookAnalyzer builds an analyzer with the given budget.
func NewPackageInstallHookAnalyzer(budget Budget) *PackageInstallHookAnalyzer {
	return &PackageInstallHookAnalyzer{budget: budget}
}

// Extract validates the declared hooks and returns them in manifest order.
func (a *PackageInstallHookAnalyzer) Extract(m Manifest) ([]Hook, error) {
	files := canonicalFiles(m.Files)
	hooks := make([]Hook, 0, len(m.Hooks))
	for _, hook := range m.Hooks {
		if !validStage(hook.Stage) {
			return nil, ErrBadStage
		}
		script, ok := cleanPackagePath(hook.Script)
		if !ok {
			return nil, ErrMissingScript
		}
		if _, ok := files[script]; !ok {
			return nil, ErrMissingScript
		}
		hooks = append(hooks, hook)
	}
	return hooks, nil
}

// Score grades one package by capability combination.
func (a *PackageInstallHookAnalyzer) Score(m Manifest, hooks []Hook) (Assessment, error) {
	if a.budget.MaxDepth <= 0 {
		return Assessment{}, ErrBadDepth
	}
	files := canonicalFiles(m.Files)
	seenScripts := make(map[string]bool)
	var evidence []Evidence
	opaque := false

	for _, hook := range hooks {
		if !validStage(hook.Stage) {
			return Assessment{}, ErrBadStage
		}
		script, ok := cleanPackagePath(hook.Script)
		if !ok {
			return Assessment{}, ErrMissingScript
		}
		body, ok := files[script]
		if !ok {
			return Assessment{}, ErrMissingScript
		}
		if seenScripts[script] {
			continue
		}
		seenScripts[script] = true

		text := body
		truncated := false
		if hook.Encoded {
			if strings.TrimSpace(text) == "" {
				return Assessment{}, ErrEmptyBlob
			}
			decoded, _, cut, err := a.DecodeLayers(text, a.budget)
			if err != nil {
				if errors.Is(err, ErrEmptyBlob) {
					return Assessment{}, ErrEmptyBlob
				}
				opaque = true
				continue
			}
			text = decoded
			truncated = cut
		}
		if strings.TrimSpace(text) == "" {
			return Assessment{}, ErrEmptyBlob
		}
		if !isTextPayload(text) {
			opaque = true
			continue
		}

		found := a.DetectCapabilities(script, text)
		if truncated {
			for i := range found {
				found[i].Truncated = true
			}
		}
		evidence = a.MergeEvidence(evidence, found)

		followed, err := a.FollowScripts(m, script, a.budget.MaxDepth)
		if err != nil {
			if errors.Is(err, ErrBadDepth) {
				return Assessment{}, err
			}
			continue
		}
		for _, helper := range followed {
			if seenScripts[helper] {
				continue
			}
			seenScripts[helper] = true
			helperBody, ok := files[helper]
			if !ok || strings.TrimSpace(helperBody) == "" {
				continue
			}
			if !isTextPayload(helperBody) {
				opaque = true
				continue
			}
			evidence = a.MergeEvidence(evidence, a.DetectCapabilities(helper, helperBody))
		}
	}

	evidence = a.SortedEvidence(evidence)
	score := a.CombineScore(evidence)
	level := levelForScore(score)
	verdict := verdictForScore(score)
	if opaque {
		verdict = VerdictUndetermined
	}

	return Assessment{
		Package:  m.Name,
		Score:    score,
		Level:    level,
		Verdict:  verdict,
		Opaque:   opaque,
		Evidence: evidence,
	}, nil
}

// Explain renders the assessment header and the evidence lines.
func (a *PackageInstallHookAnalyzer) Explain(assessment Assessment) string {
	lines := []string{fmt.Sprintf(
		"score=%d level=%s verdict=%s",
		assessment.Score, assessment.Level, assessment.Verdict,
	)}
	for _, item := range a.SortedEvidence(assessment.Evidence) {
		line := fmt.Sprintf("capability=%s source=%s snippet=%s", item.Capability, item.Source, item.Snippet)
		if item.Truncated {
			line += " truncated=true"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// DecodeLayers peels encoded layers while the budget allows it.
func (a *PackageInstallHookAnalyzer) DecodeLayers(blob string, budget Budget) (string, int, bool, error) {
	if budget.MaxDepth <= 0 {
		return "", 0, false, ErrBadDepth
	}
	if strings.TrimSpace(blob) == "" {
		return "", 0, false, ErrEmptyBlob
	}

	current := blob
	layers := 0
	for layers < budget.MaxDepth {
		if layers == 0 {
			decoded, truncated, err := decodeBase64Limited(current, budget.MaxBytes)
			if err != nil {
				return "", 0, false, err
			}
			layers++
			current = decoded
			if truncated {
				return current, layers, true, nil
			}
			if current == "" {
				return "", layers, false, ErrEmptyBlob
			}
			continue
		}

		if !looksBase64(current) {
			return current, layers, false, nil
		}
		decoded, truncated, err := decodeBase64Limited(current, budget.MaxBytes)
		if err != nil || !isTextPayload(decoded) {
			return current, layers, false, nil
		}
		layers++
		current = decoded
		if truncated {
			return current, layers, true, nil
		}
	}

	return current, layers, true, nil
}

// DetectCapabilities scans one file body and reports what it can do.
func (a *PackageInstallHookAnalyzer) DetectCapabilities(source, text string) []Evidence {
	cleanSource, ok := cleanPackagePath(source)
	if !ok {
		return nil
	}
	source = cleanSource
	seen := make(map[string]bool)
	var evidence []Evidence
	for _, rawLine := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		code := stripLineComment(rawLine)
		trimmed := strings.TrimSpace(code)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		lower := strings.ToLower(code)
		checks := []struct {
			capability string
			hit        bool
		}{
			{CapNetworkDownload, matchesAny(networkPatterns, lower)},
			{CapProcessExec, processCapability(code, lower)},
			{CapCredentialRead, matchesAny(credentialPatterns, lower)},
			{CapEnvWrite, envWriteCapability(code, lower)},
			{CapFileWrite, fileWriteCapability(code, lower)},
		}
		for _, check := range checks {
			if !check.hit || seen[check.capability] {
				continue
			}
			seen[check.capability] = true
			evidence = append(evidence, Evidence{
				Capability: check.capability,
				Source:     source,
				Snippet:    strings.TrimSuffix(rawLine, "\r"),
			})
		}
	}
	return a.SortedEvidence(evidence)
}

// FollowScripts walks the scripts a hook pulls in.
func (a *PackageInstallHookAnalyzer) FollowScripts(m Manifest, entry string, maxDepth int) ([]string, error) {
	if maxDepth <= 0 {
		return nil, ErrBadDepth
	}
	files := canonicalFiles(m.Files)
	entry, ok := cleanPackagePath(entry)
	if !ok {
		return nil, ErrMissingScript
	}
	if _, ok := files[entry]; !ok {
		return nil, ErrMissingScript
	}

	visited := map[string]bool{entry: true}
	var followed []string
	var walk func(string, int)
	walk = func(base string, depth int) {
		if depth >= maxDepth {
			return
		}
		body := files[base]
		for _, ref := range scriptReferences(body) {
			for _, candidate := range a.existingCandidates(base, ref, files) {
				if visited[candidate] {
					continue
				}
				visited[candidate] = true
				followed = append(followed, candidate)
				walk(candidate, depth+1)
			}
		}
	}
	walk(entry, 0)
	return followed, nil
}

// ResolvePath resolves a reference relative to the file that contains it.
func (a *PackageInstallHookAnalyzer) ResolvePath(base, ref string) (string, bool) {
	base, ok := cleanPackagePath(base)
	if !ok || base == "." {
		return "", false
	}
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.ContainsRune(ref, 0) || strings.Contains(ref, "://") || strings.HasPrefix(ref, "~") || strings.ContainsAny(ref, "$`") {
		return "", false
	}
	ref = strings.ReplaceAll(ref, "\\", "/")
	if strings.HasPrefix(ref, "/") || looksLikeWindowsDrive(ref) {
		return "", false
	}
	joined := path.Clean(path.Join(path.Dir(base), ref))
	if !insidePackagePath(joined) || joined == "." {
		return "", false
	}
	return joined, true
}

// CombineScore turns a capability set into one bounded score.
func (a *PackageInstallHookAnalyzer) CombineScore(evidence []Evidence) int {
	capabilities := make(map[string]bool)
	for _, item := range evidence {
		switch item.Capability {
		case CapNetworkDownload, CapProcessExec, CapCredentialRead, CapEnvWrite, CapFileWrite:
			capabilities[item.Capability] = true
		}
	}

	score := 0
	if capabilities[CapNetworkDownload] {
		score += 25
	}
	if capabilities[CapProcessExec] {
		score += 20
	}
	if capabilities[CapCredentialRead] {
		score += 30
	}
	if capabilities[CapEnvWrite] {
		score += 10
	}
	if capabilities[CapFileWrite] {
		score += 5
	}
	if capabilities[CapNetworkDownload] && capabilities[CapCredentialRead] {
		score += 40
	}
	if capabilities[CapNetworkDownload] && capabilities[CapProcessExec] {
		score += 30
	}
	if capabilities[CapCredentialRead] && capabilities[CapEnvWrite] {
		score += 20
	}
	if score > 100 {
		score = 100
	}
	return score
}

// SortedEvidence orders evidence and drops exact duplicates.
func (a *PackageInstallHookAnalyzer) SortedEvidence(evidence []Evidence) []Evidence {
	if len(evidence) == 0 {
		return nil
	}
	out := append([]Evidence(nil), evidence...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Capability != out[j].Capability {
			return out[i].Capability < out[j].Capability
		}
		return out[i].Source < out[j].Source
	})
	deduped := make([]Evidence, 0, len(out))
	seen := make(map[Evidence]bool, len(out))
	for _, item := range out {
		if seen[item] {
			continue
		}
		seen[item] = true
		deduped = append(deduped, item)
	}
	return deduped
}

// Stages lists the distinct hook stages in first seen order.
func (a *PackageInstallHookAnalyzer) Stages(hooks []Hook) []string {
	var stages []string
	seen := make(map[string]bool)
	for _, hook := range hooks {
		if seen[hook.Stage] {
			continue
		}
		seen[hook.Stage] = true
		stages = append(stages, hook.Stage)
	}
	return stages
}

// MergeEvidence appends two evidence lists while keeping the first one intact.
func (a *PackageInstallHookAnalyzer) MergeEvidence(left, right []Evidence) []Evidence {
	out := make([]Evidence, 0, len(left)+len(right))
	out = append(out, left...)
	out = append(out, right...)
	return out
}

func validStage(stage string) bool {
	return stage == StagePreinstall || stage == StagePostinstall
}

func verdictForScore(score int) string {
	switch {
	case score >= 80:
		return VerdictMalicious
	case score >= 25:
		return VerdictSuspicious
	default:
		return VerdictBenign
	}
}

func levelForScore(score int) string {
	switch {
	case score >= 80:
		return LevelCritical
	case score >= 60:
		return LevelHigh
	case score >= 25:
		return LevelMedium
	default:
		return LevelLow
	}
}

func canonicalFiles(files map[string]string) map[string]string {
	if len(files) == 0 {
		return map[string]string{}
	}
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(files))
	for _, key := range keys {
		clean, ok := cleanPackagePath(key)
		if !ok {
			continue
		}
		if _, exists := out[clean]; exists {
			continue
		}
		out[clean] = files[key]
	}
	return out
}

func cleanPackagePath(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, 0) || strings.Contains(value, "://") || strings.HasPrefix(value, "~") {
		return "", false
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(value, "/") || looksLikeWindowsDrive(value) {
		return "", false
	}
	clean := path.Clean(value)
	if !insidePackagePath(clean) || clean == "." {
		return "", false
	}
	return clean, true
}

func insidePackagePath(value string) bool {
	return value != ".." && !strings.HasPrefix(value, "../") && !strings.HasPrefix(value, "/") && value != "."
}

func looksLikeWindowsDrive(value string) bool {
	return len(value) >= 2 && unicode.IsLetter(rune(value[0])) && value[1] == ':'
}

func decodeBase64Limited(value string, maxBytes int) (string, bool, error) {
	compact := removeBase64Whitespace(value)
	if compact == "" {
		return "", false, ErrEmptyBlob
	}
	limit := int64(maxBytes) + 1
	if maxBytes < 0 {
		limit = 1
	}

	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding}
	if strings.ContainsAny(compact, "-_") && !strings.ContainsAny(compact, "+/") {
		encodings = []*base64.Encoding{base64.URLEncoding, base64.RawURLEncoding}
	}
	var lastErr error
	for _, encoding := range encodings {
		decoder := base64.NewDecoder(encoding, strings.NewReader(compact))
		decoded, err := io.ReadAll(io.LimitReader(decoder, limit))
		if err != nil {
			lastErr = err
			continue
		}
		if maxBytes < 0 {
			return "", len(decoded) > 0, nil
		}
		if len(decoded) > maxBytes {
			return string(decoded[:maxBytes]), true, nil
		}
		return string(decoded), false, nil
	}
	return "", false, lastErr
}

func removeBase64Whitespace(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}

func looksBase64(value string) bool {
	compact := removeBase64Whitespace(value)
	if len(compact) < 4 || len(compact)%4 == 1 {
		return false
	}
	padding := 0
	for i, r := range compact {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '-', r == '_':
			if padding > 0 {
				return false
			}
		case r == '=':
			if i < len(compact)-2 {
				return false
			}
			padding++
			if padding > 2 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func isTextPayload(value string) bool {
	if value == "" {
		return true
	}
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (a *PackageInstallHookAnalyzer) existingCandidates(base, ref string, files map[string]string) []string {
	if ref == "" || strings.ContainsAny(ref, "$`") || strings.ContainsRune(ref, 0) {
		return nil
	}
	var candidates []string
	add := func(candidate string) {
		if _, ok := files[candidate]; !ok {
			return
		}
		for _, previous := range candidates {
			if previous == candidate {
				return
			}
		}
		candidates = append(candidates, candidate)
	}
	if resolved, ok := a.ResolvePath(base, ref); ok {
		add(resolved)
	}
	if rooted, ok := cleanPackagePath(ref); ok {
		add(rooted)
	}
	return candidates
}

func stripLineComment(line string) string {
	var quote rune
	escaped := false
	for index, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if quote != '\'' && r == '\\' {
			escaped = true
			continue
		}
		if r == '\'' || r == '"' {
			if quote == 0 {
				quote = r
			} else if quote == r {
				quote = 0
			}
			continue
		}
		if quote == 0 && r == '#' {
			if index == 0 {
				return ""
			}
			previous, _ := utf8.DecodeLastRuneInString(line[:index])
			if unicode.IsSpace(previous) || strings.ContainsRune(";|&()", previous) {
				return line[:index]
			}
		}
	}
	return line
}

func scriptReferences(text string) []string {
	var refs []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		code := stripLineComment(line)
		if strings.TrimSpace(code) == "" {
			continue
		}
		for _, command := range shellCommands(code) {
			if ref := referenceFromCommand(command); ref != "" {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

func shellCommands(line string) [][]string {
	tokens := shellTokens(line)
	var commands [][]string
	var current []string
	flush := func() {
		if len(current) > 0 {
			commands = append(commands, current)
			current = nil
		}
	}
	for _, token := range tokens {
		if token == ";" || token == "|" || token == "&" || token == "&&" || token == "||" {
			flush()
			continue
		}
		current = append(current, token)
	}
	flush()
	return commands
}

func shellTokens(line string) []string {
	var tokens []string
	var current strings.Builder
	var quote rune
	escaped := false
	runes := []rune(line)
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if quote != '\'' && r == '\\' {
			if index+1 < len(runes) && shellEscapes(runes[index+1]) {
				index++
				current.WriteRune(runes[index])
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			if quote == 0 {
				quote = r
			} else if quote == r {
				quote = 0
			}
			continue
		}
		if quote == 0 {
			if unicode.IsSpace(r) {
				flush()
				continue
			}
			if strings.ContainsRune(";|&", r) {
				flush()
				tokens = append(tokens, string(r))
				continue
			}
		}
		current.WriteRune(r)
	}
	flush()
	return tokens
}

func shellEscapes(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune(`'"$`+"`"+`;&|#`, r)
}

func referenceFromCommand(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	command := strings.ReplaceAll(tokens[0], "\\", "/")
	for strings.Contains(command, "=") && !strings.HasPrefix(command, "=") {
		tokens = tokens[1:]
		if len(tokens) == 0 {
			return ""
		}
		command = strings.ReplaceAll(tokens[0], "\\", "/")
	}

	for {
		switch command {
		case "sudo", "command", "builtin", "nohup", "setsid", "time":
			tokens = skipCommandOptions(tokens[1:])
			if len(tokens) == 0 {
				return ""
			}
			command = strings.ReplaceAll(tokens[0], "\\", "/")
			continue
		case "env":
			tokens = tokens[1:]
			for len(tokens) > 0 && (strings.HasPrefix(tokens[0], "-") || strings.Contains(tokens[0], "=")) {
				tokens = tokens[1:]
			}
			if len(tokens) == 0 {
				return ""
			}
			command = strings.ReplaceAll(tokens[0], "\\", "/")
			continue
		case "timeout":
			tokens = skipCommandOptions(tokens[1:])
			if len(tokens) > 0 {
				tokens = tokens[1:]
			}
			tokens = skipCommandOptions(tokens)
			if len(tokens) == 0 {
				return ""
			}
			command = strings.ReplaceAll(tokens[0], "\\", "/")
			continue
		}
		break
	}

	base := path.Base(strings.ReplaceAll(command, "\\", "/"))
	switch base {
	case ".", "source":
		return firstPathArgument(tokens[1:])
	case "exec":
		return firstPathArgument(tokens[1:])
	case "sh", "bash", "dash", "zsh", "ksh", "ash", "fish":
		return interpreterScript(tokens[1:], true)
	case "python", "python2", "python3", "node", "nodejs", "perl", "ruby", "php", "pwsh", "powershell":
		return interpreterScript(tokens[1:], false)
	}

	if isDirectScript(command) || (strings.Contains(command, "/") && hasScriptSuffix(command)) {
		return command
	}
	return ""
}

func skipCommandOptions(tokens []string) []string {
	for len(tokens) > 0 && strings.HasPrefix(tokens[0], "-") {
		tokens = tokens[1:]
	}
	return tokens
}

func interpreterScript(tokens []string, shell bool) string {
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if token == "-c" || token == "-Command" || token == "-command" || token == "-e" {
			return ""
		}
		if token == "-File" || token == "-file" {
			if index+1 < len(tokens) {
				return tokens[index+1]
			}
			return ""
		}
		if strings.HasPrefix(token, "-") {
			continue
		}
		if shell && strings.Contains(token, "=") {
			continue
		}
		return token
	}
	return ""
}

func firstPathArgument(tokens []string) string {
	for _, token := range tokens {
		if strings.HasPrefix(token, "-") {
			continue
		}
		return token
	}
	return ""
}

func isDirectScript(command string) bool {
	return strings.HasPrefix(command, "./") || strings.HasPrefix(command, "../") || strings.HasPrefix(command, "/")
}

func hasScriptSuffix(command string) bool {
	lower := strings.ToLower(command)
	for _, suffix := range []string{".sh", ".bash", ".zsh", ".py", ".js", ".mjs", ".cjs", ".pl", ".rb", ".php", ".ps1"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

var (
	networkPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bcurl\b`),
		regexp.MustCompile(`\bwget\b`),
		regexp.MustCompile(`\b(?:https?|ftp)://`),
		regexp.MustCompile(`\binvoke-webrequest\b`),
		regexp.MustCompile(`\binvoke-restmethod\b`),
		regexp.MustCompile(`\bstart-bitstransfer\b`),
		regexp.MustCompile(`\brequests\.(?:get|post|put|delete|request)\b`),
		regexp.MustCompile(`\burllib\.request\b`),
		regexp.MustCompile(`\bhttp\.client\b`),
		regexp.MustCompile(`\bnet/http\b`),
		regexp.MustCompile(`\baxios\b`),
		regexp.MustCompile(`\bnode-fetch\b`),
		regexp.MustCompile(`\bfetch\s*\(`),
		regexp.MustCompile(`\b(?:nc|ncat|scp|rsync)\s+`),
		regexp.MustCompile(`\bgit\s+clone\b`),
		regexp.MustCompile(`\bnpm\s+(?:install|i|add)\b`),
	}
	credentialPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\.npmrc\b`),
		regexp.MustCompile(`\.pypirc\b`),
		regexp.MustCompile(`\.netrc\b`),
		regexp.MustCompile(`\.git-credentials\b`),
		regexp.MustCompile(`\.ssh(?:/|\b)`),
		regexp.MustCompile(`\bid_(?:rsa|dsa|ecdsa|ed25519)\b`),
		regexp.MustCompile(`\.aws(?:/|\b)`),
		regexp.MustCompile(`\.docker/config\.json\b`),
		regexp.MustCompile(`\.kube/config\b`),
		regexp.MustCompile(`\.config/gh/hosts\.yml\b`),
		regexp.MustCompile(`\b(?:credentials?|passwd|password|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|auth[_-]?token)\b`),
		regexp.MustCompile(`\bauthorization\s*:`),
		regexp.MustCompile(`\bbearer\s+[a-z0-9._~+/=-]+`),
		regexp.MustCompile(`(?:getenv|os\.environ|process\.env)[\[(.]*['"]?[a-z0-9_]*(?:token|secret|pass|key|cred|auth)`),
		regexp.MustCompile(`\$[a-z_][a-z0-9_]*(?:token|secret|password|passwd|pass|apikey|api_key|access_key|private_key|auth)[a-z0-9_]*`),
		regexp.MustCompile(`%[a-z_][a-z0-9_]*(?:token|secret|password|passwd|pass|key|auth)[a-z0-9_]*%`),
	}
	envAssignmentPattern = regexp.MustCompile(`^\s*(?:export\s+)?(?:path|ld_preload|ld_library_path|node_options|pythonpath|home|shell|user|ifs|bash_env|env)\s*=`)
	envApiPattern        = regexp.MustCompile(`(?:\b(?:setx|setenvironmentvariable|putenv|setenv|unsetenv)\b|\bos\.setenv\b|os\.environ\[[^\]]+\]\s*=|os\.environ\.(?:update|setdefault|pop)\s*\(|del\s+os\.environ|process\.env\.[a-z_][a-z0-9_]*\s*=|declare\s+-x\b|typeset\s+-x\b|^\s*unset\s+[a-z_][a-z0-9_]*|^\s*set\s+[a-z_][a-z0-9_]*=|\benv\s+[a-z_][a-z0-9_]*=|\$env:[a-z_][a-z0-9_]*\s*=)`)
	envExportPattern     = regexp.MustCompile(`^\s*export\s+[a-z_][a-z0-9_]*=`)
	redirectionPattern   = regexp.MustCompile(`(?:^|[^<>=!])(?:>>?|>\|)\s*([^\s;&|]+)`)
	fileWritePatterns    = []*regexp.Regexp{
		regexp.MustCompile(`\b(?:tee|truncate|dd|cp|mv|install|touch|mkdir|rm|rmdir|ln|sed\s+-i|perl\s+-pi)\s+`),
		regexp.MustCompile(`\b(?:os\.writefile|ioutil\.writefile|fs\.writefile|writefilesync|file\.writealltext|path\.write_text|shutil\.copy|fopen)\b`),
		regexp.MustCompile(`\bopen\s*\([^)]*,\s*['"](?:w|a|wb|ab|w\+|a\+)['"]`),
		regexp.MustCompile(`\b(?:chmod|chown)\s+`),
		regexp.MustCompile(`\b(?:out-file|set-content|add-content|new-item)\b`),
		regexp.MustCompile(`\b(?:os\.create|file\.create|shutil\.copyfile)\b`),
	}
	processTokenPattern = regexp.MustCompile(`(?:^|[^a-z0-9_])(exec|system|popen|nohup|setsid|eval|spawn|spawnsync|execfile|execsync)(?:[^a-z0-9_]|$)`)
	processApiPattern   = regexp.MustCompile(`\b(?:os/exec|exec\.command|child_process|subprocess\.(?:run|call|popen)|createprocess|start-process|shell_exec|passthru|proc_open)\b`)
	processShellPattern = regexp.MustCompile(`\b(?:sh|bash|dash|zsh|ksh|powershell|pwsh)\s+-c\b`)
)

func matchesAny(patterns []*regexp.Regexp, value string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func processCapability(code, lower string) bool {
	if processTokenPattern.MatchString(lower) || processApiPattern.MatchString(lower) || processShellPattern.MatchString(lower) {
		return true
	}
	for _, command := range shellCommands(code) {
		base := commandBase(command)
		switch base {
		case "sh", "bash", "dash", "zsh", "ksh", "ash", "fish", "python", "python2", "python3", "node", "nodejs", "perl", "ruby", "php", "pwsh", "powershell", "exec":
			return true
		}
		if referenceFromCommand(command) != "" && (isDirectScript(base) || (strings.Contains(base, "/") && hasScriptSuffix(base))) {
			return true
		}
	}
	return false
}

func envWriteCapability(code, lower string) bool {
	return envAssignmentPattern.MatchString(lower) || envExportPattern.MatchString(lower) || envApiPattern.MatchString(lower)
}

func fileWriteCapability(code, lower string) bool {
	if matchesAny(fileWritePatterns, lower) {
		return true
	}
	for _, match := range redirectionPattern.FindAllStringSubmatch(lower, -1) {
		if len(match) < 2 {
			continue
		}
		target := strings.Trim(match[1], `"'`)
		if target == "/dev/null" || strings.HasPrefix(target, "/dev/fd/") {
			continue
		}
		return true
	}
	return false
}

func commandBase(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	return path.Base(strings.ReplaceAll(tokens[0], "\\", "/"))
}
