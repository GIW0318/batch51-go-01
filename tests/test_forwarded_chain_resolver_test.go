package task

import (
	"errors"
	"net/http"
	"net/netip"
	"testing"

	impl "gsb/batch51-go-safety-b-05"
)

func forwardedConfig() impl.TrustConfig {
	return impl.TrustConfig{
		Header:          impl.HeaderXFF,
		MaxChain:        8,
		MaxHeaderBytes:  2048,
		MaxCacheEntries: 16,
	}
}

func forwardedRemote(t *testing.T, raw string) netip.AddrPort {
	t.Helper()
	addr, err := netip.ParseAddrPort(raw)
	if err != nil {
		t.Fatalf("test remote %q is invalid: %v", raw, err)
	}
	return addr
}

func xffHeader(value string) http.Header {
	return http.Header{"X-Forwarded-For": []string{value}}
}

func forwardedHeader(value string) http.Header {
	return http.Header{"Forwarded": []string{value}}
}

func TestTrustCIDRRegistersListener(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	if !g.Trusted("edge", netip.MustParseAddr("10.1.2.3")) {
		t.Fatalf("address inside the trusted network must be trusted")
	}
	if g.Trusted("edge", netip.MustParseAddr("198.51.100.1")) {
		t.Fatalf("address outside the trusted network must not be trusted")
	}
	listeners := g.Listeners()
	if len(listeners) != 1 || listeners[0] != "edge" {
		t.Fatalf("listeners = %v, want [edge]", listeners)
	}
}

func TestTrustCIDRRejectsInvalidCIDR(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "300.0.0.0/8"); !errors.Is(err, impl.ErrBadCIDR) {
		t.Fatalf("invalid cidr error = %v, want ErrBadCIDR", err)
	}
}

func TestResolveWithoutHeadersUsesRemote(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	info, err := g.Resolve(forwardedRemote(t, "198.51.100.20:5555"), http.Header{}, "edge")
	if err != nil {
		t.Fatalf("direct resolution failed: %v", err)
	}
	if info.Client != netip.MustParseAddr("198.51.100.20") {
		t.Fatalf("client = %s, want 198.51.100.20", info.Client)
	}
	if info.Hops != 0 {
		t.Fatalf("hops = %d, want 0", info.Hops)
	}
	if info.Listener != "edge" {
		t.Fatalf("listener = %q, want edge", info.Listener)
	}
}

func TestResolveStripsSingleTrustedProxy(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	info, err := g.Resolve(forwardedRemote(t, "10.1.2.3:1000"), xffHeader("198.51.100.7"), "edge")
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if info.Client != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("client = %s, want 198.51.100.7", info.Client)
	}
	if info.Hops != 1 {
		t.Fatalf("hops = %d, want 1", info.Hops)
	}
	if info.Header != impl.HeaderXFF {
		t.Fatalf("header = %q, want %q", info.Header, impl.HeaderXFF)
	}
}

func TestResolveMultiHopNormalizesPort(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	info, err := g.Resolve(
		forwardedRemote(t, "10.1.2.3:1000"),
		xffHeader("203.0.113.9:443, 10.0.0.5"),
		"edge",
	)
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if info.Client != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("client = %s, want 203.0.113.9 with the port removed", info.Client)
	}
	if info.Hops != 2 {
		t.Fatalf("hops = %d, want 2", info.Hops)
	}
}

func TestResolveStopsAtFirstUntrusted(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	info, err := g.Resolve(
		forwardedRemote(t, "10.1.2.3:1000"),
		xffHeader("203.0.113.9, 198.51.100.7"),
		"edge",
	)
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if info.Client != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("client = %s, want the rightmost untrusted 198.51.100.7", info.Client)
	}
	if info.Hops != 2 {
		t.Fatalf("hops = %d, want 2", info.Hops)
	}
}

func TestResolveIgnoresHeadersFromUntrustedRemote(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	info, err := g.Resolve(
		forwardedRemote(t, "198.51.100.20:5555"),
		xffHeader("203.0.113.9"),
		"edge",
	)
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if info.Client != netip.MustParseAddr("198.51.100.20") {
		t.Fatalf("client = %s, want the direct peer 198.51.100.20", info.Client)
	}
	if info.Hops != 0 {
		t.Fatalf("hops = %d, want 0", info.Hops)
	}
}

func TestResolveNormalizesBracketedIPv6(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	info, err := g.Resolve(
		forwardedRemote(t, "10.1.2.3:1000"),
		xffHeader("[2001:db8::5]:8443, 10.0.0.5"),
		"edge",
	)
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if info.Client != netip.MustParseAddr("2001:db8::5") {
		t.Fatalf("client = %s, want 2001:db8::5", info.Client)
	}
}

func TestResolveForwardedProto(t *testing.T) {
	cfg := forwardedConfig()
	cfg.Header = impl.HeaderForwarded
	g := impl.NewForwardedChainResolver(cfg)
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	info, err := g.Resolve(
		forwardedRemote(t, "10.1.2.3:1000"),
		forwardedHeader("for=203.0.113.9;proto=https, for=10.0.0.5"),
		"edge",
	)
	if err != nil {
		t.Fatalf("resolution failed: %v", err)
	}
	if info.Client != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("client = %s, want 203.0.113.9", info.Client)
	}
	if info.Proto != "https" {
		t.Fatalf("proto = %q, want https", info.Proto)
	}
	if info.Header != impl.HeaderForwarded {
		t.Fatalf("header = %q, want %q", info.Header, impl.HeaderForwarded)
	}
}

func TestResolveRejectsObfuscatedIdentifier(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	_, err := g.Resolve(forwardedRemote(t, "10.1.2.3:1000"), xffHeader("_hidden"), "edge")
	if !errors.Is(err, impl.ErrObfuscated) {
		t.Fatalf("obfuscated error = %v, want ErrObfuscated", err)
	}
}

func TestResolveRejectsOverlongChain(t *testing.T) {
	cfg := forwardedConfig()
	cfg.MaxChain = 1
	g := impl.NewForwardedChainResolver(cfg)
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	_, err := g.Resolve(
		forwardedRemote(t, "10.1.2.3:1000"),
		xffHeader("203.0.113.9, 198.51.100.7"),
		"edge",
	)
	if !errors.Is(err, impl.ErrChainTooLong) {
		t.Fatalf("overlong chain error = %v, want ErrChainTooLong", err)
	}
}

func TestResolveUsesCache(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	remote := forwardedRemote(t, "10.1.2.3:1000")
	first, err := g.Resolve(remote, xffHeader("198.51.100.7"), "edge")
	if err != nil {
		t.Fatalf("first resolution failed: %v", err)
	}
	if first.Cached {
		t.Fatalf("first resolution must not be marked as cached")
	}
	second, err := g.Resolve(remote, xffHeader("198.51.100.7"), "edge")
	if err != nil {
		t.Fatalf("second resolution failed: %v", err)
	}
	if !second.Cached {
		t.Fatalf("second resolution must be served from the cache")
	}
	if second.Client != first.Client {
		t.Fatalf("cached client = %s, want %s", second.Client, first.Client)
	}
	if got := g.CacheStats().Hits; got < 1 {
		t.Fatalf("cache hits = %d, want at least 1", got)
	}
}

func TestTrustCIDRInvalidatesCache(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	remote := forwardedRemote(t, "198.51.100.20:5555")
	first, err := g.Resolve(remote, xffHeader("203.0.113.9"), "edge")
	if err != nil {
		t.Fatalf("first resolution failed: %v", err)
	}
	if first.Client != netip.MustParseAddr("198.51.100.20") {
		t.Fatalf("untrusted peer must be the client, got %s", first.Client)
	}
	if err := g.TrustCIDR("edge", "198.51.100.0/24"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	second, err := g.Resolve(remote, xffHeader("203.0.113.9"), "edge")
	if err != nil {
		t.Fatalf("second resolution failed: %v", err)
	}
	if second.Cached {
		t.Fatalf("a trust change must invalidate the cached result")
	}
	if second.Client != netip.MustParseAddr("203.0.113.9") {
		t.Fatalf("client after trust change = %s, want 203.0.113.9", second.Client)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	cfg := forwardedConfig()
	g := impl.NewForwardedChainResolver(cfg)
	if err := g.TrustCIDR("edge", "10.0.0.0/8"); err != nil {
		t.Fatalf("trust cidr failed: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewForwardedChainResolver(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if !restored.Trusted("edge", netip.MustParseAddr("10.9.9.9")) {
		t.Fatalf("trust table missing after restore")
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	g := impl.NewForwardedChainResolver(forwardedConfig())
	if err := g.Restore([]byte("not-a-trust-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
}
