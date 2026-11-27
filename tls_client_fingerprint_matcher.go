// Package task extracts TLS client hello fingerprints, matches them against a
// versioned library of known clients and annotates proxy forwarded connections.
package task

import "errors"

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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
	opts    Options
	library []LibraryEntry
	version int64
	stats   Stats
}

// NewTlsClientFingerprintMatcher builds a matcher with an empty library.
func NewTlsClientFingerprintMatcher(opts Options) (*TlsClientFingerprintMatcher, error) {
	return nil, errNotImplemented
}

// Extract normalises a client hello and returns its order sensitive and set
// insensitive hashes.
func (m *TlsClientFingerprintMatcher) Extract(hello ClientHello) (Fingerprint, error) {
	return Fingerprint{}, errNotImplemented
}

// Match looks the fingerprint up in the current library generation.
func (m *TlsClientFingerprintMatcher) Match(fp Fingerprint) (MatchResult, error) {
	return MatchResult{}, errNotImplemented
}

// Canonical returns the order sensitive preimage that was hashed.
func (m *TlsClientFingerprintMatcher) Canonical(fp Fingerprint) (string, error) {
	return "", errNotImplemented
}

// Compare grades how similar two fingerprints are.
func (m *TlsClientFingerprintMatcher) Compare(a, b Fingerprint) (int, error) {
	return 0, errNotImplemented
}

// Reload publishes a new library generation in one step.
func (m *TlsClientFingerprintMatcher) Reload(lib []LibraryEntry) error {
	return errNotImplemented
}

// LibraryVersion returns the generation of the active library.
func (m *TlsClientFingerprintMatcher) LibraryVersion() int64 {
	return 0
}

// Snapshot serialises the library, its version and the counters.
func (m *TlsClientFingerprintMatcher) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (m *TlsClientFingerprintMatcher) Restore(data []byte) error {
	return errNotImplemented
}

// Stats returns a copy of the counters.
func (m *TlsClientFingerprintMatcher) Stats() Stats {
	return Stats{}
}
