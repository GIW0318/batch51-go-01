package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-r-01"
)

const (
	certNow  = int64(1000)
	certLate = int64(2000)
)

func anchorCert() impl.CertRecord {
	return impl.CertRecord{
		Serial:      "anchor-1",
		Subject:     "CN=Corp Root",
		Issuer:      "CN=Corp Root",
		Fingerprint: "fp-anchor",
		SignedBy:    "fp-anchor",
		IsCA:        true,
		NotBefore:   0,
		NotAfter:    9000,
		KeyUsage:    []string{"certSign"},
	}
}

func rootCert() impl.CertRecord {
	return impl.CertRecord{
		Serial:      "root-1",
		Subject:     "CN=Corp Issuing Root",
		Issuer:      "CN=Corp Root",
		Fingerprint: "fp-root",
		SignedBy:    "fp-anchor",
		IsCA:        true,
		NotBefore:   0,
		NotAfter:    8000,
		KeyUsage:    []string{"certSign"},
	}
}

func interCert() impl.CertRecord {
	return impl.CertRecord{
		Serial:      "inter-1",
		Subject:     "CN=Corp Client CA",
		Issuer:      "CN=Corp Issuing Root",
		Fingerprint: "fp-inter",
		SignedBy:    "fp-root",
		IsCA:        true,
		NotBefore:   0,
		NotAfter:    7000,
		KeyUsage:    []string{"certSign"},
	}
}

func leafCert() impl.CertRecord {
	return impl.CertRecord{
		Serial:      "leaf-1",
		Subject:     "CN=svc-payments",
		Issuer:      "CN=Corp Client CA",
		Fingerprint: "fp-leaf",
		SignedBy:    "fp-inter",
		IsCA:        false,
		NotBefore:   0,
		NotAfter:    6000,
		KeyUsage:    []string{"clientAuth"},
	}
}

func fullChain() []impl.CertRecord {
	return []impl.CertRecord{leafCert(), interCert(), rootCert()}
}

func newGuard(t *testing.T) *impl.MtlsChainRevocationGuard {
	t.Helper()
	g := impl.NewMtlsChainRevocationGuard(impl.GuardConfig{MaxChainDepth: 5, MaxAudit: 256, MaxPins: 4})
	if err := g.Trust(anchorCert()); err != nil {
		t.Fatalf("trust anchor must be accepted, got %v", err)
	}
	return g
}

func TestTrustRejectsIncompleteAnchor(t *testing.T) {
	g := impl.NewMtlsChainRevocationGuard(impl.GuardConfig{MaxChainDepth: 5, MaxAudit: 16, MaxPins: 2})
	if err := g.Trust(impl.CertRecord{}); !errors.Is(err, impl.ErrBadCertificate) {
		t.Fatalf("incomplete anchor must be refused with ErrBadCertificate, got %v", err)
	}
	if got := len(g.TrustedAnchors()); got != 0 {
		t.Fatalf("refused anchor must not be stored, table holds %d", got)
	}
}

func TestCheckChainOrdersLeafToAnchor(t *testing.T) {
	g := newGuard(t)
	got, err := g.CheckChain(fullChain(), certNow)
	if err != nil {
		t.Fatalf("clean chain must verify, got %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ordered chain must keep three certificates, got %d", len(got))
	}
	want := []string{"fp-leaf", "fp-inter", "fp-root"}
	for i := range want {
		if got[i].Fingerprint != want[i] {
			t.Fatalf("position %d must be %s, got %s", i, want[i], got[i].Fingerprint)
		}
	}
	anchors := g.TrustedAnchors()
	if len(anchors) != 1 || anchors[0] != "fp-anchor" {
		t.Fatalf("anchor table must list fp-anchor, got %v", anchors)
	}
}

func TestCheckChainRejectsMissingIntermediate(t *testing.T) {
	g := newGuard(t)
	chain := []impl.CertRecord{leafCert(), rootCert()}
	if _, err := g.CheckChain(chain, certNow); !errors.Is(err, impl.ErrChainIncomplete) {
		t.Fatalf("skipped intermediate must be reported as ErrChainIncomplete, got %v", err)
	}
}

func TestCheckChainRejectsMisorderedInput(t *testing.T) {
	g := newGuard(t)
	chain := []impl.CertRecord{leafCert(), rootCert(), interCert()}
	if _, err := g.CheckChain(chain, certNow); !errors.Is(err, impl.ErrChainOrder) {
		t.Fatalf("misordered chain must be reported as ErrChainOrder, got %v", err)
	}
}

func TestCheckChainRejectsExpiredCertificate(t *testing.T) {
	g := newGuard(t)
	stale := interCert()
	stale.NotAfter = 900
	chain := []impl.CertRecord{leafCert(), stale, rootCert()}
	if _, err := g.CheckChain(chain, certNow); !errors.Is(err, impl.ErrCertificateExpired) {
		t.Fatalf("expired intermediate must be refused, got %v", err)
	}
}

func TestCheckChainRejectsNonCAIssuer(t *testing.T) {
	g := newGuard(t)
	notCA := interCert()
	notCA.IsCA = false
	chain := []impl.CertRecord{leafCert(), notCA, rootCert()}
	if _, err := g.CheckChain(chain, certNow); !errors.Is(err, impl.ErrNotCA) {
		t.Fatalf("issuer without the CA bit must be refused, got %v", err)
	}
}

func TestCheckChainRejectsLeafWithoutClientAuth(t *testing.T) {
	g := newGuard(t)
	leaf := leafCert()
	leaf.KeyUsage = []string{"serverAuth"}
	chain := []impl.CertRecord{leaf, interCert(), rootCert()}
	if _, err := g.CheckChain(chain, certNow); !errors.Is(err, impl.ErrKeyUsage) {
		t.Fatalf("leaf without clientAuth must be refused, got %v", err)
	}
}

func TestCheckChainRejectsUnknownAnchor(t *testing.T) {
	g := newGuard(t)
	loose := rootCert()
	loose.SignedBy = "fp-nobody"
	chain := []impl.CertRecord{leafCert(), interCert(), loose}
	if _, err := g.CheckChain(chain, certNow); !errors.Is(err, impl.ErrUnknownAnchor) {
		t.Fatalf("chain ending outside the anchor table must be refused, got %v", err)
	}
}

func TestDecideDeniesRevokedSerial(t *testing.T) {
	g := newGuard(t)
	if err := g.AddRevocation(impl.RevocationRecord{
		Serial:    leafCert().Serial,
		Issuer:    leafCert().Issuer,
		Subject:   leafCert().Subject,
		RevokedAt: 500,
		Reason:    "key compromise",
	}); err != nil {
		t.Fatalf("revocation entry must be accepted, got %v", err)
	}
	d, err := g.Decide(fullChain(), certNow, "fp-leaf")
	if err != nil {
		t.Fatalf("revoked chain must produce a verdict, got error %v", err)
	}
	if d.Allowed {
		t.Fatalf("revoked serial must not be admitted")
	}
	if d.Code != impl.VerdictDeny || d.Reason != impl.ReasonRevoked {
		t.Fatalf("expected deny/%s, got %s/%s", impl.ReasonRevoked, d.Code, d.Reason)
	}
}

func TestDecideFailsClosedWhenRevocationSourceDown(t *testing.T) {
	g := newGuard(t)
	g.SetRevocationSource(false)
	d, err := g.Decide(fullChain(), certNow, "fp-leaf")
	if err != nil {
		t.Fatalf("verdict must be produced even when the source is down, got %v", err)
	}
	if d.Allowed {
		t.Fatalf("admission must fail closed while the revocation source is unreachable")
	}
	if d.Reason != impl.ReasonRevocationDown {
		t.Fatalf("expected reason %s, got %s", impl.ReasonRevocationDown, d.Reason)
	}
}

func TestDecideRejectsPinnedFingerprintMismatch(t *testing.T) {
	g := newGuard(t)
	if err := g.Pin(leafCert(), 0, 5000); err != nil {
		t.Fatalf("pin must be accepted, got %v", err)
	}
	d, err := g.Decide(fullChain(), certNow, "fp-attacker")
	if err != nil {
		t.Fatalf("mismatch must produce a verdict, got error %v", err)
	}
	if d.Allowed {
		t.Fatalf("peer fingerprint outside the pin table must not be admitted")
	}
	if d.Reason != impl.ReasonPinMismatch {
		t.Fatalf("expected reason %s, got %s", impl.ReasonPinMismatch, d.Reason)
	}
}

func TestDecideAllowsCleanChainAndAudits(t *testing.T) {
	g := newGuard(t)
	if err := g.Pin(leafCert(), 0, 5000); err != nil {
		t.Fatalf("pin must be accepted, got %v", err)
	}
	first, err := g.Decide(fullChain(), certNow, "fp-leaf")
	if err != nil {
		t.Fatalf("clean chain must be admitted, got %v", err)
	}
	if !first.Allowed || first.Code != impl.VerdictAllow {
		t.Fatalf("clean chain must be allowed, got %s/%s", first.Code, first.Reason)
	}
	if first.Leaf != "fp-leaf" || first.Depth != 3 || first.Seq != 1 {
		t.Fatalf("verdict must carry leaf, depth 3 and seq 1, got leaf=%s depth=%d seq=%d",
			first.Leaf, first.Depth, first.Seq)
	}
	second, err := g.Decide(fullChain(), certLate, "fp-leaf")
	if err != nil {
		t.Fatalf("second verdict must be produced, got %v", err)
	}
	if second.Seq != 2 {
		t.Fatalf("second verdict must take seq 2, got %d", second.Seq)
	}
	audit := g.Audit()
	if len(audit) != 2 {
		t.Fatalf("audit trail must hold two records, got %d", len(audit))
	}
	if audit[0].Seq != 1 || audit[0].Event != "allow" || audit[0].Serial != "leaf-1" || audit[1].Seq != 2 {
		t.Fatalf("audit records must be ordered and name the leaf serial, got %+v", audit)
	}
}

func TestPinsRejectFullTable(t *testing.T) {
	g := impl.NewMtlsChainRevocationGuard(impl.GuardConfig{MaxChainDepth: 5, MaxAudit: 16, MaxPins: 1})
	if err := g.Pin(leafCert(), 0, 5000); err != nil {
		t.Fatalf("first pin must be accepted, got %v", err)
	}
	other := leafCert()
	other.Fingerprint = "fp-leaf-2"
	if err := g.Pin(other, 0, 5000); !errors.Is(err, impl.ErrPinLimit) {
		t.Fatalf("second pin over the cap must be refused with ErrPinLimit, got %v", err)
	}
	if got := len(g.Pins()); got != 1 {
		t.Fatalf("pin table must stay at one entry, got %d", got)
	}
}

func TestSnapshotRestoreKeepsAnchorsRevocationsAndPins(t *testing.T) {
	g := newGuard(t)
	if err := g.AddRevocation(impl.RevocationRecord{
		Serial: "leaf-1", Issuer: "CN=Corp Client CA", RevokedAt: 500,
	}); err != nil {
		t.Fatalf("revocation entry must be accepted, got %v", err)
	}
	if err := g.Pin(leafCert(), 0, 5000); err != nil {
		t.Fatalf("pin must be accepted, got %v", err)
	}
	if _, err := g.Decide(fullChain(), certNow, "fp-leaf"); err != nil {
		t.Fatalf("verdict must be produced, got %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must be produced, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	other := impl.NewMtlsChainRevocationGuard(impl.GuardConfig{MaxChainDepth: 5, MaxAudit: 256, MaxPins: 4})
	if err := other.Restore(data); err != nil {
		t.Fatalf("snapshot must restore, got %v", err)
	}
	if got := other.TrustedAnchors(); len(got) != 1 || got[0] != "fp-anchor" {
		t.Fatalf("anchors must survive restore, got %v", got)
	}
	if got := len(other.Pins()); got != 1 {
		t.Fatalf("pins must survive restore, got %d", got)
	}
	if got := len(other.Audit()); got != 1 {
		t.Fatalf("audit trail must survive restore, got %d", got)
	}
	d, err := other.Decide(fullChain(), certNow, "fp-leaf")
	if err != nil {
		t.Fatalf("verdict after restore must be produced, got %v", err)
	}
	if d.Allowed || d.Reason != impl.ReasonRevoked {
		t.Fatalf("restored revocation table must still deny the leaf, got %+v", d)
	}
	if d.Seq != 2 {
		t.Fatalf("verdict counter must continue at 2 after restore, got %d", d.Seq)
	}
	if err := other.Restore([]byte("{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must be refused with ErrBadSnapshot, got %v", err)
	}
}

func TestConcurrentDecideIsConsistent(t *testing.T) {
	g := newGuard(t)
	if err := g.Pin(leafCert(), 0, 5000); err != nil {
		t.Fatalf("pin must be accepted, got %v", err)
	}
	const workers = 32
	const rounds = 5
	var wg sync.WaitGroup
	errs := make(chan string, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				d, err := g.Decide(fullChain(), certNow, "fp-leaf")
				if err != nil {
					errs <- "verdict returned error: " + err.Error()
					return
				}
				if !d.Allowed {
					errs <- "clean chain denied under concurrency"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatalf("%s", msg)
	}
	audit := g.Audit()
	if len(audit) != workers*rounds {
		t.Fatalf("audit trail must hold %d records, got %d", workers*rounds, len(audit))
	}
	seen := make(map[int64]bool, len(audit))
	for _, rec := range audit {
		if seen[rec.Seq] {
			t.Fatalf("audit sequence %d was handed out twice", rec.Seq)
		}
		seen[rec.Seq] = true
	}
	for seq := int64(1); seq <= int64(len(audit)); seq++ {
		if !seen[seq] {
			t.Fatalf("audit sequence %d is missing", seq)
		}
	}
}
