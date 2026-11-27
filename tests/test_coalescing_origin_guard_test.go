package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-b-06"
)

func testPolicy() impl.OriginPolicy {
	return impl.OriginPolicy{
		AllowedSchemes:    []string{"https"},
		MaxOriginsPerConn: 6,
		MaxAdvertised:     4,
		MaxCacheEntries:   32,
	}
}

func testConn() impl.ConnInfo {
	return impl.ConnInfo{
		ID:       "conn-1",
		Protocol: "h2",
		Local:    "10.0.0.5",
		Remote:   "203.0.113.10",
		Port:     443,
		Cert:     impl.Certificate{SANs: []string{"api.example.com", "*.example.net"}},
	}
}

func registered(t *testing.T) *impl.CoalescingOriginGuard {
	t.Helper()
	g := impl.NewCoalescingOriginGuard(testPolicy())
	if err := g.RegisterConn(testConn()); err != nil {
		t.Fatalf("register connection: %v", err)
	}
	return g
}

func TestRegisterConnStoresAndDedupes(t *testing.T) {
	g := impl.NewCoalescingOriginGuard(testPolicy())
	conn := testConn()
	if err := g.RegisterConn(conn); err != nil {
		t.Fatalf("first registration must succeed, got %v", err)
	}
	if got := g.Stats().Conns; got != 1 {
		t.Fatalf("connection count = %d, want 1", got)
	}
	conn.Port = 8443
	if err := g.RegisterConn(conn); err != nil {
		t.Fatalf("re-registration of the same id must succeed, got %v", err)
	}
	if got := g.Stats().Conns; got != 1 {
		t.Fatalf("connection count after re-registration = %d, want 1", got)
	}
	ok, err := g.CanCoalesce(conn, "https://api.example.com")
	if err != nil {
		t.Fatalf("coalescing probe on the updated connection: %v", err)
	}
	if ok {
		t.Fatalf("connection on port 8443 must not serve the default 443 origin")
	}
	if got := g.OriginsOf("conn-404"); got != nil {
		t.Fatalf("unknown connection must have no advertised origins, got %v", got)
	}
	if err := g.RegisterConn(impl.ConnInfo{ID: " ", Protocol: "h2", Port: 443}); !errors.Is(err, impl.ErrBadConn) {
		t.Fatalf("blank connection id must fail with ErrBadConn, got %v", err)
	}
	if err := g.RegisterConn(impl.ConnInfo{ID: "conn-9", Protocol: "h2", Port: 443}); !errors.Is(err, impl.ErrBadCertificate) {
		t.Fatalf("connection without subject alternative names must fail with ErrBadCertificate, got %v", err)
	}
}

func TestCanCoalesceCoveredAuthorityAllows(t *testing.T) {
	g := registered(t)
	conn := testConn()
	for _, authority := range []string{"https://api.example.com", "https://API.Example.COM.:443"} {
		ok, err := g.CanCoalesce(conn, authority)
		if err != nil {
			t.Fatalf("coalescing %q must not fail, got %v", authority, err)
		}
		if !ok {
			t.Fatalf("authority %q is covered by the certificate and must coalesce", authority)
		}
	}
}

func TestCanCoalesceUncoveredAuthorityRejects(t *testing.T) {
	g := registered(t)
	conn := testConn()
	ok, err := g.CanCoalesce(conn, "https://evil.example.org")
	if err != nil {
		t.Fatalf("an uncovered authority is a policy answer, not a failure, got %v", err)
	}
	if ok {
		t.Fatalf("authority outside every subject alternative name must not coalesce")
	}
}

func TestCanCoalesceRejectsPortAndSchemeMismatch(t *testing.T) {
	g := registered(t)
	conn := testConn()
	ok, err := g.CanCoalesce(conn, "https://api.example.com:8443")
	if err != nil {
		t.Fatalf("explicit port probe: %v", err)
	}
	if ok {
		t.Fatalf("authority on port 8443 must not be carried by a 443 connection")
	}
	ok, err = g.CanCoalesce(conn, "http://api.example.com")
	if err != nil {
		t.Fatalf("plain http probe: %v", err)
	}
	if ok {
		t.Fatalf("plain http origin must never coalesce on this guard")
	}
}

func TestCanCoalesceBadAuthorityErrors(t *testing.T) {
	g := registered(t)
	conn := testConn()
	for _, authority := range []string{"", "   ", "api.example.com", "https://", "https://api.example.com:99999"} {
		if _, err := g.CanCoalesce(conn, authority); !errors.Is(err, impl.ErrBadAuthority) {
			t.Fatalf("authority %q must fail with ErrBadAuthority, got %v", authority, err)
		}
	}
}

func TestCanCoalesceWildcardCoversOneLabel(t *testing.T) {
	g := registered(t)
	ok, err := g.CanCoalesce(testConn(), "https://edge.example.net")
	if err != nil {
		t.Fatalf("wildcard probe: %v", err)
	}
	if !ok {
		t.Fatalf("*.example.net must cover the single label host edge.example.net")
	}
}

func TestAdvertiseFiltersSortsAndCaps(t *testing.T) {
	g := impl.NewCoalescingOriginGuard(testPolicy())
	frame, err := g.Advertise([]string{
		"https://cdn.example.com",
		"https://API.example.com:443",
		"https://api.example.com",
		"http://plain.example.com",
		"https://bad path.example.com",
		" ",
	})
	if err != nil {
		t.Fatalf("advertise a mixed candidate list: %v", err)
	}
	want := []string{"https://api.example.com", "https://cdn.example.com"}
	if frame.Count != len(want) {
		t.Fatalf("advertised count = %d, want %d", frame.Count, len(want))
	}
	if frame.Truncated {
		t.Fatalf("a list under the cap must not be marked truncated")
	}
	for i, origin := range want {
		if frame.Origins[i] != origin {
			t.Fatalf("advertised origins = %v, want the canonical sorted list %v", frame.Origins, want)
		}
	}
	frame, err = g.Advertise([]string{
		"https://f.example.com",
		"https://e.example.com",
		"https://d.example.com",
		"https://c.example.com",
		"https://b.example.com",
		"https://a.example.com",
	})
	if err != nil {
		t.Fatalf("advertise over the cap: %v", err)
	}
	if !frame.Truncated {
		t.Fatalf("a list above the cap must be marked truncated")
	}
	if frame.Count != 4 || len(frame.Origins) != 4 {
		t.Fatalf("advertised count = %d with %d entries, want the cap 4", frame.Count, len(frame.Origins))
	}
	for i, origin := range []string{"https://a.example.com", "https://b.example.com", "https://c.example.com", "https://d.example.com"} {
		if frame.Origins[i] != origin {
			t.Fatalf("truncated frame kept %v, want the first four sorted origins", frame.Origins)
		}
	}
}

func TestAdvertiseAllInvalidFails(t *testing.T) {
	g := impl.NewCoalescingOriginGuard(testPolicy())
	if _, err := g.Advertise([]string{"", "ftp://x.example.com", "https://"}); !errors.Is(err, impl.ErrNoOrigin) {
		t.Fatalf("a list without usable origins must fail with ErrNoOrigin, got %v", err)
	}
}

func TestInstallValidatesFrameAndConn(t *testing.T) {
	g := registered(t)
	if err := g.Install("conn-404", impl.OriginFrame{Origins: []string{"https://api.example.com"}, Count: 1}); !errors.Is(err, impl.ErrUnknownConn) {
		t.Fatalf("installing on an unknown connection must fail with ErrUnknownConn, got %v", err)
	}
	if err := g.Install("conn-1", impl.OriginFrame{}); !errors.Is(err, impl.ErrEmptyFrame) {
		t.Fatalf("an empty frame must fail with ErrEmptyFrame, got %v", err)
	}
	oversize := impl.OriginFrame{Origins: []string{"a", "b", "c", "d", "e"}, Count: 5}
	if err := g.Install("conn-1", oversize); !errors.Is(err, impl.ErrOriginLimit) {
		t.Fatalf("a frame above the advertised cap must fail with ErrOriginLimit, got %v", err)
	}
	good := impl.OriginFrame{Origins: []string{"https://api.example.com"}, Count: 1}
	if err := g.Install("conn-1", good); err != nil {
		t.Fatalf("installing a valid frame: %v", err)
	}
	got := g.OriginsOf("conn-1")
	if len(got) != 1 || got[0] != "https://api.example.com" {
		t.Fatalf("stored origins = %v, want the installed frame", got)
	}
}

func TestCheckServesDeclaredOrigin(t *testing.T) {
	g := registered(t)
	if err := g.Install("conn-1", impl.OriginFrame{Origins: []string{"https://api.example.com"}, Count: 1}); err != nil {
		t.Fatalf("install frame: %v", err)
	}
	v, err := g.Check(impl.ConnInfo{ID: "conn-1"}, impl.Request{
		Authority: "https://api.example.com",
		Method:    "GET",
		Path:      "/img/logo.png",
		TLS:       true,
	})
	if err != nil {
		t.Fatalf("check a declared origin: %v", err)
	}
	if v.Action != impl.ActionServe || v.Status != impl.StatusServe || v.Reason != impl.ReasonServed {
		t.Fatalf("verdict = action %q status %d reason %q, want serve/0/served", v.Action, v.Status, v.Reason)
	}
	if v.ConnID != "conn-1" {
		t.Fatalf("verdict connection = %q, want conn-1", v.ConnID)
	}
	if v.Origin != "https://api.example.com" {
		t.Fatalf("verdict origin = %q, want the canonical origin", v.Origin)
	}
	stats := g.Stats()
	if stats.Checks != 1 || stats.Served != 1 {
		t.Fatalf("stats checks=%d served=%d, want 1 and 1", stats.Checks, stats.Served)
	}
}

func TestCheckUndeclaredOriginMisdirects(t *testing.T) {
	g := registered(t)
	if err := g.Install("conn-1", impl.OriginFrame{Origins: []string{"https://api.example.com"}, Count: 1}); err != nil {
		t.Fatalf("install frame: %v", err)
	}
	v, err := g.Check(impl.ConnInfo{ID: "conn-1"}, impl.Request{
		Authority: "https://img.example.net",
		Method:    "GET",
		Path:      "/pixel.gif",
		TLS:       true,
	})
	if err != nil {
		t.Fatalf("check an undeclared origin: %v", err)
	}
	if v.Action != impl.ActionMisdirect || v.Status != impl.StatusMisdirect {
		t.Fatalf("verdict = action %q status %d, want misdirect/421", v.Action, v.Status)
	}
	if v.Reason != impl.ReasonNotDeclared {
		t.Fatalf("verdict reason = %q, want %q", v.Reason, impl.ReasonNotDeclared)
	}
	if got := g.Stats().Misdirected; got != 1 {
		t.Fatalf("misdirected counter = %d, want 1", got)
	}
}

func TestCheckRefusesMalformedRequest(t *testing.T) {
	g := registered(t)
	if err := g.Install("conn-1", impl.OriginFrame{Origins: []string{"https://api.example.com"}, Count: 1}); err != nil {
		t.Fatalf("install frame: %v", err)
	}
	v, err := g.Check(impl.ConnInfo{ID: "conn-1"}, impl.Request{Authority: "not an authority", Method: "GET", Path: "/", TLS: true})
	if err != nil {
		t.Fatalf("check a malformed authority: %v", err)
	}
	if v.Action != impl.ActionRefuse || v.Status != impl.StatusRefuse || v.Reason != impl.ReasonBadAuthority {
		t.Fatalf("verdict = action %q status %d reason %q, want refuse/400/bad authority", v.Action, v.Status, v.Reason)
	}
	v, err = g.Check(impl.ConnInfo{ID: "conn-1"}, impl.Request{Authority: "https://api.example.com", Method: "GET", Path: "/", TLS: false})
	if err != nil {
		t.Fatalf("check a cleartext request: %v", err)
	}
	if v.Action != impl.ActionRefuse || v.Reason != impl.ReasonSchemeRejected {
		t.Fatalf("cleartext request verdict = %q/%q, want refuse with the scheme reason", v.Action, v.Reason)
	}
	if _, err := g.Check(impl.ConnInfo{ID: "conn-404"}, impl.Request{Authority: "https://api.example.com", TLS: true}); !errors.Is(err, impl.ErrUnknownConn) {
		t.Fatalf("checking on an unregistered connection must fail with ErrUnknownConn, got %v", err)
	}
}

func TestRotateCertReevaluatesCoverage(t *testing.T) {
	g := registered(t)
	conn := testConn()
	ok, err := g.CanCoalesce(conn, "https://api.example.com")
	if err != nil || !ok {
		t.Fatalf("baseline coalescing = %v/%v, want true", ok, err)
	}
	if err := g.RotateCert("conn-1", impl.Certificate{SANs: []string{"next.example.com"}}); err != nil {
		t.Fatalf("rotate certificate: %v", err)
	}
	ok, err = g.CanCoalesce(conn, "https://next.example.com")
	if err != nil {
		t.Fatalf("coalescing the new name: %v", err)
	}
	if !ok {
		t.Fatalf("the new certificate must cover next.example.com")
	}
	ok, err = g.CanCoalesce(conn, "https://api.example.com")
	if err != nil {
		t.Fatalf("coalescing the retired name: %v", err)
	}
	if ok {
		t.Fatalf("the old name must stop coalescing after the certificate rotation")
	}
	if err := g.RotateCert("conn-404", impl.Certificate{SANs: []string{"x.example.com"}}); !errors.Is(err, impl.ErrUnknownConn) {
		t.Fatalf("rotating an unknown connection must fail with ErrUnknownConn, got %v", err)
	}
	if err := g.RotateCert("conn-1", impl.Certificate{}); !errors.Is(err, impl.ErrBadCertificate) {
		t.Fatalf("rotating to an empty certificate must fail with ErrBadCertificate, got %v", err)
	}
	ok, err = g.CanCoalesce(conn, "https://next.example.com")
	if err != nil || !ok {
		t.Fatalf("a rejected rotation must leave the live certificate in place, got %v/%v", ok, err)
	}
}

func TestSnapshotRestoreKeepsConnectionsAndFrames(t *testing.T) {
	g := registered(t)
	if err := g.Install("conn-1", impl.OriginFrame{Origins: []string{"https://api.example.com", "https://cdn.example.com"}, Count: 2}); err != nil {
		t.Fatalf("install frame: %v", err)
	}
	if _, err := g.Check(impl.ConnInfo{ID: "conn-1"}, impl.Request{Authority: "https://cdn.example.com", TLS: true}); err != nil {
		t.Fatalf("check before snapshot: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewCoalescingOriginGuard(testPolicy())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Stats().Conns; got != 1 {
		t.Fatalf("restored connection count = %d, want 1", got)
	}
	origins := restored.OriginsOf("conn-1")
	if len(origins) != 2 || origins[0] != "https://api.example.com" || origins[1] != "https://cdn.example.com" {
		t.Fatalf("restored origins = %v, want the two installed origins", origins)
	}
	if ok, err := restored.CanCoalesce(testConn(), "https://api.example.com"); err != nil || !ok {
		t.Fatalf("restored certificate coverage = %v/%v, want true", ok, err)
	}
	if got := restored.Stats().Checks; got != 1 {
		t.Fatalf("restored check counter = %d, want 1", got)
	}
	if err := restored.Restore([]byte("{oops")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Stats().Conns; got != 1 {
		t.Fatalf("a rejected restore must keep live state, connection count = %d", got)
	}
	if got := len(restored.OriginsOf("conn-1")); got != 2 {
		t.Fatalf("a rejected restore must keep the advertised set, got %d origins", got)
	}
}

func TestConcurrentChecksAndRotations(t *testing.T) {
	g := registered(t)
	if err := g.Install("conn-1", impl.OriginFrame{Origins: []string{"https://api.example.com"}, Count: 1}); err != nil {
		t.Fatalf("install frame: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	bad := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			authority := "https://api.example.com"
			if i%2 == 1 {
				authority = "https://img.example.net"
			}
			v, err := g.Check(impl.ConnInfo{ID: "conn-1"}, impl.Request{Authority: authority, Method: "GET", Path: "/x", TLS: true})
			mu.Lock()
			defer mu.Unlock()
			if err != nil || v.ConnID != "conn-1" || v.Status == 0 && v.Action != impl.ActionServe {
				bad++
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 64; i++ {
			_ = g.RotateCert("conn-1", impl.Certificate{SANs: []string{"api.example.com", "*.example.net"}})
		}
	}()
	wg.Wait()
	if bad != 0 {
		t.Fatalf("%d concurrent checks returned an error or a malformed verdict", bad)
	}
	stats := g.Stats()
	if stats.Checks != 32 {
		t.Fatalf("check counter = %d, want 32", stats.Checks)
	}
	if stats.Served+stats.Misdirected+stats.Refused != 32 {
		t.Fatalf("verdict counters served=%d misdirected=%d refused=%d do not add up to 32", stats.Served, stats.Misdirected, stats.Refused)
	}
}
