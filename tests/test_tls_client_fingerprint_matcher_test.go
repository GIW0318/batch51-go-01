package task

import (
	"errors"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-28"
)

func referenceHello() impl.ClientHello {
	return impl.ClientHello{
		ServerName:   "shop.example.test",
		Ciphers:      []uint16{4865, 4866},
		Extensions:   []uint16{0, 10, 11, 16, 43},
		Curves:       []uint16{29, 23},
		PointFormats: []uint8{0},
		ALPN:         []string{"h2", "http/1.1"},
	}
}

func newMatcher(t *testing.T, opts impl.Options) *impl.TlsClientFingerprintMatcher {
	t.Helper()
	m, err := impl.NewTlsClientFingerprintMatcher(opts)
	if err != nil {
		t.Fatalf("build matcher: %v", err)
	}
	return m
}

func extractFingerprint(t *testing.T, m *impl.TlsClientFingerprintMatcher, hello impl.ClientHello) impl.Fingerprint {
	t.Helper()
	fp, err := m.Extract(hello)
	if err != nil {
		t.Fatalf("extract fingerprint: %v", err)
	}
	return fp
}

func TestExtractProducesStableHashes(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	first := extractFingerprint(t, m, referenceHello())
	second := extractFingerprint(t, m, referenceHello())
	if first != second {
		t.Fatalf("same hello produced different fingerprints: %+v and %+v", first, second)
	}
	if len(first.OrderHash) != 64 || len(first.SetHash) != 64 {
		t.Fatalf("hash lengths = %d/%d, want two 64 character hex digests", len(first.OrderHash), len(first.SetHash))
	}
	if first.CipherCount != 2 || first.ExtensionCount != 5 || first.CurveCount != 2 {
		t.Fatalf("counts = %d/%d/%d, want 2/5/2", first.CipherCount, first.ExtensionCount, first.CurveCount)
	}
	if first.ALPN != "h2,http/1.1" {
		t.Fatalf("alpn = %q, want h2,http/1.1", first.ALPN)
	}
	if !first.Trusted || first.ProxyHops != 0 {
		t.Fatalf("trusted=%v hops=%d, want a direct connection", first.Trusted, first.ProxyHops)
	}
	if got := m.Stats().Extracts; got != 2 {
		t.Fatalf("extract counter = %d, want 2", got)
	}
}

func TestExtractDifferentCiphersChangeBothHashes(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	base := extractFingerprint(t, m, referenceHello())
	changedHello := referenceHello()
	changedHello.Ciphers = append(changedHello.Ciphers, 4867)
	changed := extractFingerprint(t, m, changedHello)
	if base.OrderHash == changed.OrderHash {
		t.Fatalf("order hash must change when the cipher list changes")
	}
	if base.SetHash == changed.SetHash {
		t.Fatalf("set hash must change when the cipher set changes")
	}
	if changed.CipherCount != 3 {
		t.Fatalf("cipher count = %d, want 3", changed.CipherCount)
	}
}

func TestCanonicalFormatIsExplicit(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fp := extractFingerprint(t, m, referenceHello())
	got, err := m.Canonical(fp)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	want := "c=4865,4866;e=0,10,11,16,43;cur=29,23;pf=0;alpn=h2,http/1.1"
	if got != want {
		t.Fatalf("canonical = %q, want %q", got, want)
	}
}

func TestExtractRejectsEmptyHello(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	if _, err := m.Extract(impl.ClientHello{Extensions: []uint16{0, 10}}); !errors.Is(err, impl.ErrEmptyHello) {
		t.Fatalf("hello without ciphers must fail with ErrEmptyHello, got %v", err)
	}
	if _, err := m.Extract(impl.ClientHello{}); !errors.Is(err, impl.ErrEmptyHello) {
		t.Fatalf("empty hello must fail with ErrEmptyHello, got %v", err)
	}
}

func TestExtractEnforcesHelloLimits(t *testing.T) {
	m := newMatcher(t, impl.Options{MaxExtensions: 4, MaxCiphers: 1})
	hello := referenceHello()
	if _, err := m.Extract(hello); !errors.Is(err, impl.ErrHelloTooLarge) {
		t.Fatalf("five extensions against a cap of four must fail with ErrHelloTooLarge, got %v", err)
	}
	small := referenceHello()
	small.Extensions = small.Extensions[:4]
	if _, err := m.Extract(small); !errors.Is(err, impl.ErrHelloTooLarge) {
		t.Fatalf("two ciphers against a cap of one must fail with ErrHelloTooLarge, got %v", err)
	}
}

func TestMatchExactLibraryEntry(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fp := extractFingerprint(t, m, referenceHello())
	lib := []impl.LibraryEntry{{Name: "chrome-120", OrderHash: fp.OrderHash, SetHash: fp.SetHash}}
	if err := m.Reload(lib); err != nil {
		t.Fatalf("reload: %v", err)
	}
	res, err := m.Match(fp)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Client != "chrome-120" {
		t.Fatalf("client = %q, want chrome-120", res.Client)
	}
	if res.Tool != "" || res.Malicious {
		t.Fatalf("ordinary client reported as tool=%q malicious=%v", res.Tool, res.Malicious)
	}
	if !res.Trusted {
		t.Fatalf("direct connection must stay trusted")
	}
	if res.Reason != impl.ReasonExact || res.MatchedBy != impl.MatchedByOrder {
		t.Fatalf("reason=%q matchedBy=%q, want exact/order", res.Reason, res.MatchedBy)
	}
	if res.LibraryVersion != 1 {
		t.Fatalf("library version = %d, want 1", res.LibraryVersion)
	}
}

func TestMatchMaliciousTool(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fp := extractFingerprint(t, m, referenceHello())
	lib := []impl.LibraryEntry{{Name: "c2-beacon", OrderHash: fp.OrderHash, SetHash: fp.SetHash, Malicious: true}}
	if err := m.Reload(lib); err != nil {
		t.Fatalf("reload: %v", err)
	}
	res, err := m.Match(fp)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if !res.Malicious {
		t.Fatalf("malicious entry must be reported as malicious")
	}
	if res.Tool != "c2-beacon" {
		t.Fatalf("tool = %q, want c2-beacon", res.Tool)
	}
	if res.Client != "unknown" {
		t.Fatalf("client = %q, want unknown for a tool match", res.Client)
	}
}

func TestMatchUnknownFingerprint(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fp := extractFingerprint(t, m, referenceHello())
	res, err := m.Match(fp)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Client != "unknown" || res.Tool != "" {
		t.Fatalf("result = %+v, want an unknown client without a tool", res)
	}
	if res.Malicious {
		t.Fatalf("unknown fingerprint must not be flagged as malicious")
	}
	if res.Reason != impl.ReasonNoEntry || res.MatchedBy != impl.MatchedByNone {
		t.Fatalf("reason=%q matchedBy=%q, want no_entry/none", res.Reason, res.MatchedBy)
	}
	if res.LibraryVersion != 1 {
		t.Fatalf("library version = %d, want 1", res.LibraryVersion)
	}
}

func TestMatchAnnotatesProxiedConnection(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	hello := referenceHello()
	hello.ProxyChain = []string{"10.0.0.5", "10.0.0.9"}
	fp := extractFingerprint(t, m, hello)
	if fp.Trusted || fp.ProxyHops != 2 {
		t.Fatalf("proxied fingerprint trusted=%v hops=%d, want false and 2", fp.Trusted, fp.ProxyHops)
	}
	lib := []impl.LibraryEntry{{Name: "chrome-120", OrderHash: fp.OrderHash, SetHash: fp.SetHash}}
	if err := m.Reload(lib); err != nil {
		t.Fatalf("reload: %v", err)
	}
	res, err := m.Match(fp)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Client != "chrome-120" {
		t.Fatalf("client = %q, want the fingerprint to still match chrome-120", res.Client)
	}
	if res.Trusted {
		t.Fatalf("proxy forwarded fingerprint must be marked untrusted")
	}
	if res.Reason != impl.ReasonProxied {
		t.Fatalf("reason = %q, want %q", res.Reason, impl.ReasonProxied)
	}
	if got := m.Stats().ProxyMarked; got != 1 {
		t.Fatalf("proxy counter = %d, want 1", got)
	}
}

func TestReloadBumpsVersionAndReplacesLibrary(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fpA := extractFingerprint(t, m, referenceHello())
	other := referenceHello()
	other.Ciphers = []uint16{4867, 4868}
	fpB := extractFingerprint(t, m, other)
	if err := m.Reload([]impl.LibraryEntry{{Name: "chrome-120", OrderHash: fpA.OrderHash, SetHash: fpA.SetHash}}); err != nil {
		t.Fatalf("first reload: %v", err)
	}
	if got := m.LibraryVersion(); got != 1 {
		t.Fatalf("version after first reload = %d, want 1", got)
	}
	if err := m.Reload([]impl.LibraryEntry{{Name: "firefox-121", OrderHash: fpB.OrderHash, SetHash: fpB.SetHash}}); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if got := m.LibraryVersion(); got != 2 {
		t.Fatalf("version after second reload = %d, want 2", got)
	}
	oldResult, err := m.Match(fpA)
	if err != nil {
		t.Fatalf("match old fingerprint: %v", err)
	}
	if oldResult.Client != "unknown" {
		t.Fatalf("old library entry still matched as %q", oldResult.Client)
	}
	newResult, err := m.Match(fpB)
	if err != nil {
		t.Fatalf("match new fingerprint: %v", err)
	}
	if newResult.Client != "firefox-121" || newResult.LibraryVersion != 2 {
		t.Fatalf("new match = %+v, want firefox-121 in version 2", newResult)
	}
	if got := m.Stats().Reloads; got != 2 {
		t.Fatalf("reload counter = %d, want 2", got)
	}
}

func TestReloadRejectsBrokenLibrary(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fp := extractFingerprint(t, m, referenceHello())
	good := []impl.LibraryEntry{{Name: "chrome-120", OrderHash: fp.OrderHash, SetHash: fp.SetHash}}
	if err := m.Reload(good); err != nil {
		t.Fatalf("reload good library: %v", err)
	}
	if err := m.Reload([]impl.LibraryEntry{{Name: "", OrderHash: fp.OrderHash, SetHash: fp.SetHash}}); !errors.Is(err, impl.ErrBadLibrary) {
		t.Fatalf("empty entry name must fail with ErrBadLibrary, got %v", err)
	}
	if err := m.Reload([]impl.LibraryEntry{{Name: "broken", OrderHash: "deadbeef", SetHash: fp.SetHash}}); !errors.Is(err, impl.ErrBadLibrary) {
		t.Fatalf("short hash must fail with ErrBadLibrary, got %v", err)
	}
	if got := m.LibraryVersion(); got != 1 {
		t.Fatalf("version after rejected reload = %d, want 1", got)
	}
	res, err := m.Match(fp)
	if err != nil {
		t.Fatalf("match after rejected reload: %v", err)
	}
	if res.Client != "chrome-120" {
		t.Fatalf("old library must stay active, got %q", res.Client)
	}
}

func TestConcurrentMatchDuringReloadIsAtomic(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fp := extractFingerprint(t, m, referenceHello())
	benign := []impl.LibraryEntry{{Name: "chrome-120", OrderHash: fp.OrderHash, SetHash: fp.SetHash}}
	malicious := []impl.LibraryEntry{{Name: "c2-beacon", OrderHash: fp.OrderHash, SetHash: fp.SetHash, Malicious: true}}
	if err := m.Reload(benign); err != nil {
		t.Fatalf("initial reload: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 60; i++ {
			var err error
			if i%2 == 0 {
				err = m.Reload(malicious)
			} else {
				err = m.Reload(benign)
			}
			if err != nil {
				t.Errorf("reload round %d: %v", i, err)
				return
			}
		}
	}()
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				res, err := m.Match(fp)
				if err != nil {
					t.Errorf("concurrent match: %v", err)
					return
				}
				if res.LibraryVersion%2 == 0 {
					if res.Client != "chrome-120" || res.Malicious {
						t.Errorf("version %d mixed library state: %+v", res.LibraryVersion, res)
						return
					}
					continue
				}
				if res.Tool != "c2-beacon" || !res.Malicious || res.Client != "unknown" {
					t.Errorf("version %d mixed library state: %+v", res.LibraryVersion, res)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestSnapshotRestoreKeepsLibraryAndVersion(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fp := extractFingerprint(t, m, referenceHello())
	if err := m.Reload([]impl.LibraryEntry{{Name: "chrome-120", OrderHash: fp.OrderHash, SetHash: fp.SetHash}}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := m.Match(fp); err != nil {
		t.Fatalf("match before snapshot: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newMatcher(t, impl.Options{})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.LibraryVersion(); got != 1 {
		t.Fatalf("restored library version = %d, want 1", got)
	}
	res, err := restored.Match(fp)
	if err != nil {
		t.Fatalf("match after restore: %v", err)
	}
	if res.Client != "chrome-120" || res.LibraryVersion != 1 {
		t.Fatalf("restored match = %+v, want chrome-120 in version 1", res)
	}
	if got := restored.Stats().Extracts; got != 1 {
		t.Fatalf("restored extract counter = %d, want 1", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fp := extractFingerprint(t, m, referenceHello())
	if err := m.Reload([]impl.LibraryEntry{{Name: "chrome-120", OrderHash: fp.OrderHash, SetHash: fp.SetHash}}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if err := m.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := m.LibraryVersion(); got != 1 {
		t.Fatalf("version after rejected restore = %d, want 1", got)
	}
	res, err := m.Match(fp)
	if err != nil {
		t.Fatalf("match after rejected restore: %v", err)
	}
	if res.Client != "chrome-120" {
		t.Fatalf("live library must survive a rejected restore, got %q", res.Client)
	}
}

func TestStatsCountsExtractAndMatch(t *testing.T) {
	m := newMatcher(t, impl.Options{})
	fp := extractFingerprint(t, m, referenceHello())
	extractFingerprint(t, m, referenceHello())
	if err := m.Reload([]impl.LibraryEntry{{Name: "chrome-120", OrderHash: fp.OrderHash, SetHash: fp.SetHash}}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := m.Match(fp); err != nil {
			t.Fatalf("match %d: %v", i, err)
		}
	}
	stats := m.Stats()
	if stats.Extracts != 2 || stats.Matches != 2 || stats.Reloads != 1 {
		t.Fatalf("stats = %+v, want 2 extracts, 2 matches, 1 reload", stats)
	}
	if strings.TrimSpace(fp.OrderHash) == "" {
		t.Fatalf("fingerprint hash must not be blank")
	}
}
