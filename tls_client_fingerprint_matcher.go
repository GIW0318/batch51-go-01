// Package task extracts TLS client hello fingerprints, matches them against a
// versioned library of known clients and annotates proxy forwarded connections.
package task

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Reason labels attached to a match result.
const (
	ReasonExact   = "exact"
	ReasonSetOnly = "set_only"
	ReasonNoEntry = "no_entry"
	ReasonProxied = "proxied"
)

// MatchedBy labels naming the fingerprint field that produced the hit.
const (
	MatchedByOrder = "order"
	MatchedBySet   = "set"
	MatchedByNone  = "none"
)

// Errors returned by the matcher.
var (
	ErrEmptyHello     = errors.New("client hello has no cipher suites")
	ErrHelloTooLarge  = errors.New("client hello exceeds the configured limits")
	ErrBadLibrary     = errors.New("fingerprint library entry is invalid")
	ErrBadFingerprint = errors.New("fingerprint has no hash material")
	ErrBadOptions     = errors.New("matcher options are invalid")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// ClientHello is the subset of a TLS client hello that the extractor needs.
type ClientHello struct {
	ServerName   string
	Ciphers      []uint16
	Extensions   []uint16
	Curves       []uint16
	PointFormats []uint8
	ALPN         []string
	ProxyChain   []string
}

// Fingerprint is the stable identity extracted from one client hello.
type Fingerprint struct {
	OrderHash      string
	SetHash        string
	CipherCount    int
	ExtensionCount int
	CurveCount     int
	ALPN           string
	ProxyHops      int
	Trusted        bool
	Canonical      string
}

// LibraryEntry is one known client or tool in the fingerprint library.
type LibraryEntry struct {
	Name      string
	OrderHash string
	SetHash   string
	Malicious bool
}

// MatchResult is the explainable outcome of one library lookup.
type MatchResult struct {
	Client         string
	Tool           string
	Malicious      bool
	Trusted        bool
	LibraryVersion int64
	Reason         string
	MatchedBy      string
}

// Options bounds the accepted hello size.
type Options struct {
	MaxExtensions   int
	MaxCiphers      int
	RequireSetMatch bool
}

// Stats counts extraction and lookup work.
type Stats struct {
	Extracts    int
	Matches     int
	Reloads     int
	ProxyMarked int
}

// TlsClientFingerprintMatcher owns the library generation and the counters.
type TlsClientFingerprintMatcher struct {
	mu          sync.Mutex
	opts        Options
	library     []LibraryEntry
	version     int64
	initialized bool
	stats       Stats
}

// NewTlsClientFingerprintMatcher builds a matcher with an empty library.
func NewTlsClientFingerprintMatcher(opts Options) (*TlsClientFingerprintMatcher, error) {
	if opts.MaxExtensions < 0 || opts.MaxCiphers < 0 {
		return nil, ErrBadOptions
	}
	return &TlsClientFingerprintMatcher{
		opts:    opts,
		version: 1,
	}, nil
}

// Extract normalises a client hello and returns its order sensitive and set
// insensitive hashes.
func (m *TlsClientFingerprintMatcher) Extract(hello ClientHello) (Fingerprint, error) {
	if len(hello.Ciphers) == 0 {
		return Fingerprint{}, ErrEmptyHello
	}
	if m.opts.MaxExtensions > 0 && len(hello.Extensions) > m.opts.MaxExtensions {
		return Fingerprint{}, ErrHelloTooLarge
	}
	if m.opts.MaxCiphers > 0 && len(hello.Ciphers) > m.opts.MaxCiphers {
		return Fingerprint{}, ErrHelloTooLarge
	}

	ciphers := meaningfulCiphers(hello.Ciphers)
	extensions := meaningfulExtensions(hello.Extensions)
	curves := meaningfulCurves(hello.Curves)
	pointFormats := append([]uint8(nil), hello.PointFormats...)
	alpn := append([]string(nil), hello.ALPN...)

	orderCanonical := canonicalString(ciphers, extensions, curves, pointFormats, alpn)
	setCanonical := canonicalString(
		sortedUniqueUint16(ciphers),
		sortedUniqueUint16(extensions),
		sortedUniqueUint16(curves),
		sortedUniqueUint8(pointFormats),
		sortedUniqueStrings(alpn),
	)

	fp := Fingerprint{
		OrderHash:      digest(orderCanonical),
		SetHash:        digest(setCanonical),
		CipherCount:    len(ciphers),
		ExtensionCount: len(extensions),
		CurveCount:     len(curves),
		ALPN:           strings.Join(alpn, ","),
		ProxyHops:      len(hello.ProxyChain),
		Trusted:        len(hello.ProxyChain) == 0,
		Canonical:      orderCanonical,
	}

	m.mu.Lock()
	m.stats.Extracts++
	if fp.ProxyHops > 0 {
		m.stats.ProxyMarked++
	}
	m.mu.Unlock()
	return fp, nil
}

// Match looks the fingerprint up in the current library generation.
func (m *TlsClientFingerprintMatcher) Match(fp Fingerprint) (MatchResult, error) {
	orderHash, setHash, err := fingerprintHashes(fp)
	if err != nil {
		return MatchResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.stats.Matches++
	proxied := fp.ProxyHops > 0
	result := MatchResult{
		Client:         "unknown",
		Trusted:        fp.Trusted && !proxied,
		LibraryVersion: m.version,
		Reason:         ReasonNoEntry,
		MatchedBy:      MatchedByNone,
	}

	orderIndex := -1
	setIndex := -1
	for i, entry := range m.library {
		if orderIndex < 0 && orderHash != "" && entry.OrderHash == orderHash {
			if !m.opts.RequireSetMatch || (setHash != "" && entry.SetHash == setHash) {
				orderIndex = i
			}
		}
		if setIndex < 0 && setHash != "" && entry.SetHash == setHash {
			setIndex = i
		}
	}

	matchedIndex := orderIndex
	if matchedIndex < 0 {
		matchedIndex = setIndex
	}
	if matchedIndex < 0 {
		return result, nil
	}

	entry := m.library[matchedIndex]
	if entry.Malicious {
		result.Tool = entry.Name
		result.Malicious = true
	} else {
		result.Client = entry.Name
	}

	if orderIndex >= 0 {
		result.Reason = ReasonExact
		result.MatchedBy = MatchedByOrder
	} else {
		result.Reason = ReasonSetOnly
		result.MatchedBy = MatchedBySet
	}
	if proxied {
		result.Reason = ReasonProxied
	}
	return result, nil
}

// Canonical returns the order sensitive preimage that was hashed.
func (m *TlsClientFingerprintMatcher) Canonical(fp Fingerprint) (string, error) {
	if fp.Canonical == "" {
		return "", ErrBadFingerprint
	}
	return fp.Canonical, nil
}

// Compare grades how similar two fingerprints are.
func (m *TlsClientFingerprintMatcher) Compare(a, b Fingerprint) (int, error) {
	orderA, setA, err := fingerprintHashes(a)
	if err != nil {
		return 0, err
	}
	orderB, setB, err := fingerprintHashes(b)
	if err != nil {
		return 0, err
	}

	if orderA == orderB && setA == setB {
		return 100, nil
	}
	if setA != "" && setA == setB {
		return 50, nil
	}
	return 0, nil
}

// Reload publishes a new library generation in one step.
func (m *TlsClientFingerprintMatcher) Reload(lib []LibraryEntry) error {
	next, err := validatedLibrary(lib)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.initialized {
		m.version++
	}
	m.initialized = true
	m.library = next
	m.stats.Reloads++
	return nil
}

// LibraryVersion returns the generation of the active library.
func (m *TlsClientFingerprintMatcher) LibraryVersion() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version
}

// Snapshot serialises the library, its version and the counters.
func (m *TlsClientFingerprintMatcher) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	payload := snapshotPayload{
		Format:         snapshotFormat,
		LibraryVersion: m.version,
		Library:        append([]LibraryEntry(nil), m.library...),
		Stats:          m.stats,
		Initialized:    m.initialized,
	}
	payload.Checksum = snapshotChecksum(payload)
	return json.Marshal(payload)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *TlsClientFingerprintMatcher) Restore(data []byte) error {
	payload, err := decodeSnapshot(data)
	if err != nil {
		return err
	}
	next, err := validatedLibrary(payload.Library)
	if err != nil {
		return ErrBadSnapshot
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.library = next
	m.version = payload.LibraryVersion
	m.initialized = payload.Initialized
	m.stats = payload.Stats
	return nil
}

// Stats returns a copy of the counters.
func (m *TlsClientFingerprintMatcher) Stats() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

const (
	snapshotFormat = "tls-client-fingerprint-matcher/v1"
	paddingExt     = uint16(21)
)

type snapshotPayload struct {
	Format         string         `json:"format"`
	LibraryVersion int64          `json:"library_version"`
	Library        []LibraryEntry `json:"library"`
	Stats          Stats          `json:"stats"`
	Initialized    bool           `json:"initialized"`
	Checksum       string         `json:"checksum"`
}

func meaningfulCiphers(values []uint16) []uint16 {
	out := make([]uint16, 0, len(values))
	for _, value := range values {
		if !isGREASE(value) {
			out = append(out, value)
		}
	}
	return out
}

func meaningfulExtensions(values []uint16) []uint16 {
	out := make([]uint16, 0, len(values))
	for _, value := range values {
		if value != paddingExt && !isGREASE(value) {
			out = append(out, value)
		}
	}
	return out
}

func meaningfulCurves(values []uint16) []uint16 {
	out := make([]uint16, 0, len(values))
	for _, value := range values {
		if !isGREASE(value) {
			out = append(out, value)
		}
	}
	return out
}

func isGREASE(value uint16) bool {
	return byte(value>>8) == byte(value) && byte(value)&0x0f == 0x0a
}

func canonicalString(ciphers, extensions, curves []uint16, pointFormats []uint8, alpn []string) string {
	var b strings.Builder
	b.WriteString("c=")
	writeUint16s(&b, ciphers)
	b.WriteString(";e=")
	writeUint16s(&b, extensions)
	b.WriteString(";cur=")
	writeUint16s(&b, curves)
	b.WriteString(";pf=")
	writeUint8s(&b, pointFormats)
	b.WriteString(";alpn=")
	for i, protocol := range alpn {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(protocol)
	}
	return b.String()
}

func writeUint16s(b *strings.Builder, values []uint16) {
	for i, value := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	}
}

func writeUint8s(b *strings.Builder, values []uint8) {
	for i, value := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatUint(uint64(value), 10))
	}
}

func sortedUniqueUint16(values []uint16) []uint16 {
	out := append([]uint16(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	for _, value := range out {
		if n == 0 || out[n-1] != value {
			out[n] = value
			n++
		}
	}
	return out[:n]
}

func sortedUniqueUint8(values []uint8) []uint8 {
	out := append([]uint8(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	for _, value := range out {
		if n == 0 || out[n-1] != value {
			out[n] = value
			n++
		}
	}
	return out[:n]
}

func sortedUniqueStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	n := 0
	for _, value := range out {
		if n == 0 || out[n-1] != value {
			out[n] = value
			n++
		}
	}
	return out[:n]
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func fingerprintHashes(fp Fingerprint) (string, string, error) {
	order, err := normalizedHash(fp.OrderHash)
	if err != nil {
		return "", "", ErrBadFingerprint
	}
	set, err := normalizedHash(fp.SetHash)
	if err != nil {
		return "", "", ErrBadFingerprint
	}
	if order == "" && set == "" {
		return "", "", ErrBadFingerprint
	}
	return order, set, nil
}

func normalizedHash(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !isHex64(value) {
		return "", ErrBadFingerprint
	}
	return strings.ToLower(value), nil
}

func isHex64(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func validatedLibrary(lib []LibraryEntry) ([]LibraryEntry, error) {
	out := make([]LibraryEntry, len(lib))
	for i, entry := range lib {
		if strings.TrimSpace(entry.Name) == "" || !isHex64(entry.OrderHash) || !isHex64(entry.SetHash) {
			return nil, ErrBadLibrary
		}
		entry.OrderHash = strings.ToLower(entry.OrderHash)
		entry.SetHash = strings.ToLower(entry.SetHash)
		out[i] = entry
	}
	return out, nil
}

func snapshotChecksum(payload snapshotPayload) string {
	payload.Checksum = ""
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func decodeSnapshot(data []byte) (snapshotPayload, error) {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return snapshotPayload{}, ErrBadSnapshot
	}
	if payload.Format != snapshotFormat || payload.LibraryVersion < 1 {
		return snapshotPayload{}, ErrBadSnapshot
	}
	if payload.Stats.Extracts < 0 || payload.Stats.Matches < 0 || payload.Stats.Reloads < 0 || payload.Stats.ProxyMarked < 0 {
		return snapshotPayload{}, ErrBadSnapshot
	}
	if !isHex64(payload.Checksum) {
		return snapshotPayload{}, ErrBadSnapshot
	}
	got := snapshotChecksum(payload)
	if subtle.ConstantTimeCompare([]byte(got), []byte(payload.Checksum)) != 1 {
		return snapshotPayload{}, ErrBadSnapshot
	}
	return payload, nil
}
