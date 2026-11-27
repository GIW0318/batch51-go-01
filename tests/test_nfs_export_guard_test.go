package task

import (
	"errors"
	"net/netip"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-b-08"
)

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse address %q: %v", s, err)
	}
	return a
}

func roSpec() impl.ExportSpec {
	return impl.ExportSpec{Clients: []string{"192.168.1.0/24"}, ReadOnly: true, RootSquash: true}
}

func newGuard(t *testing.T) *impl.NfsExportGuard {
	t.Helper()
	return impl.NewNfsExportGuard(impl.ExportPolicy{MaxExports: 8, MaxClientsPerExport: 4, MaxIdentityCache: 16})
}

func TestAddExportStoresAndBumpsVersion(t *testing.T) {
	g := newGuard(t)
	if err := g.AddExport("/data", roSpec()); err != nil {
		t.Fatalf("add export: %v", err)
	}
	got, ok := g.Get("/data")
	if !ok {
		t.Fatalf("added export must be readable")
	}
	if got.Version != 1 || !got.Spec.ReadOnly {
		t.Fatalf("export = version %d read_only %v, want version 1 read only", got.Version, got.Spec.ReadOnly)
	}
	if n := len(g.Exports()); n != 1 {
		t.Fatalf("export count = %d, want 1", n)
	}
	replacement := roSpec()
	replacement.ReadOnly = false
	if err := g.AddExport("/data", replacement); err != nil {
		t.Fatalf("replace export: %v", err)
	}
	got, ok = g.Get("/data")
	if !ok || got.Version != 2 || got.Spec.ReadOnly {
		t.Fatalf("replaced export = %+v ok=%v, want version 2 writable", got, ok)
	}
	if n := len(g.Exports()); n != 1 {
		t.Fatalf("export count after replacement = %d, want 1", n)
	}
}

func TestAddExportRejectsBadPath(t *testing.T) {
	g := newGuard(t)
	for _, path := range []string{"", "data", "/data/", "/data/../etc", "relative/path"} {
		if err := g.AddExport(path, roSpec()); !errors.Is(err, impl.ErrBadPath) {
			t.Fatalf("path %q must fail with ErrBadPath, got %v", path, err)
		}
	}
	if n := len(g.Exports()); n != 0 {
		t.Fatalf("rejected exports must not enter the table, count = %d", n)
	}
}

func TestAddExportRejectsConflictingSquash(t *testing.T) {
	g := newGuard(t)
	both := roSpec()
	both.AllSquash = true
	both.NoRootSquash = true
	if err := g.AddExport("/a", both); !errors.Is(err, impl.ErrConflictingSquash) {
		t.Fatalf("all_squash with no_root_squash must fail with ErrConflictingSquash, got %v", err)
	}
	conflict := roSpec()
	conflict.RootSquash = true
	conflict.NoRootSquash = true
	if err := g.AddExport("/b", conflict); !errors.Is(err, impl.ErrConflictingSquash) {
		t.Fatalf("root_squash with no_root_squash must fail with ErrConflictingSquash, got %v", err)
	}
	if n := len(g.Exports()); n != 0 {
		t.Fatalf("conflicting exports must not be stored, count = %d", n)
	}
}

func TestAddExportEnforcesLimits(t *testing.T) {
	small := impl.NewNfsExportGuard(impl.ExportPolicy{MaxExports: 2, MaxClientsPerExport: 2, MaxIdentityCache: 8})
	if err := small.AddExport("/a", roSpec()); err != nil {
		t.Fatalf("add /a: %v", err)
	}
	if err := small.AddExport("/b", roSpec()); err != nil {
		t.Fatalf("add /b: %v", err)
	}
	if err := small.AddExport("/c", roSpec()); !errors.Is(err, impl.ErrExportLimit) {
		t.Fatalf("third export must fail with ErrExportLimit, got %v", err)
	}
	wide := impl.NewNfsExportGuard(impl.ExportPolicy{MaxExports: 8, MaxClientsPerExport: 2, MaxIdentityCache: 8})
	tooMany := roSpec()
	tooMany.Clients = []string{"192.168.1.0/24", "10.0.0.0/8", "172.16.0.0/12"}
	if err := wide.AddExport("/wide", tooMany); !errors.Is(err, impl.ErrClientLimit) {
		t.Fatalf("three clients must fail with ErrClientLimit, got %v", err)
	}
	badCIDR := roSpec()
	badCIDR.Clients = []string{"192.168.1.0/33"}
	if err := wide.AddExport("/bad", badCIDR); !errors.Is(err, impl.ErrBadClient) {
		t.Fatalf("invalid cidr must fail with ErrBadClient, got %v", err)
	}
	noClients := roSpec()
	noClients.Clients = nil
	if err := wide.AddExport("/none", noClients); !errors.Is(err, impl.ErrNoClients) {
		t.Fatalf("empty client list must fail with ErrNoClients, got %v", err)
	}
}

func TestAuthorizeLongestPrefixWins(t *testing.T) {
	g := newGuard(t)
	if err := g.AddExport("/data", roSpec()); err != nil {
		t.Fatalf("add /data: %v", err)
	}
	team := roSpec()
	team.ReadOnly = true
	if err := g.AddExport("/data/team", team); err != nil {
		t.Fatalf("add /data/team: %v", err)
	}
	client := mustAddr(t, "192.168.1.20")
	exp, err := g.Authorize(client, "/data/team/docs/a.txt")
	if err != nil {
		t.Fatalf("authorize nested path: %v", err)
	}
	if exp.Path != "/data/team" {
		t.Fatalf("matched export = %q, want the longest prefix /data/team", exp.Path)
	}
	exp, err = g.Authorize(client, "/data/other/x")
	if err != nil {
		t.Fatalf("authorize sibling path: %v", err)
	}
	if exp.Path != "/data" {
		t.Fatalf("matched export = %q, want /data", exp.Path)
	}
}

func TestAuthorizeClientDenied(t *testing.T) {
	g := newGuard(t)
	if err := g.AddExport("/data", roSpec()); err != nil {
		t.Fatalf("add export: %v", err)
	}
	if _, err := g.Authorize(mustAddr(t, "10.0.0.7"), "/data/x"); !errors.Is(err, impl.ErrClientDenied) {
		t.Fatalf("client outside the export range must fail with ErrClientDenied, got %v", err)
	}
	if got := g.Stats().Denied; got != 1 {
		t.Fatalf("denied counter = %d, want 1", got)
	}
}

func TestAuthorizeNoExportAndRevoke(t *testing.T) {
	g := newGuard(t)
	if _, err := g.Authorize(mustAddr(t, "192.168.1.5"), "/missing/x"); !errors.Is(err, impl.ErrNoExport) {
		t.Fatalf("unknown path must fail with ErrNoExport, got %v", err)
	}
	if _, err := g.Authorize(mustAddr(t, "192.168.1.5"), "data/x"); !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("relative request path must fail with ErrBadPath, got %v", err)
	}
	if err := g.Revoke("/missing"); !errors.Is(err, impl.ErrNoExport) {
		t.Fatalf("revoking an unknown export must fail with ErrNoExport, got %v", err)
	}
	if err := g.AddExport("/data", roSpec()); err != nil {
		t.Fatalf("add export: %v", err)
	}
	if err := g.Revoke("/data"); err != nil {
		t.Fatalf("revoke export: %v", err)
	}
	if _, ok := g.Get("/data"); ok {
		t.Fatalf("revoked export must disappear")
	}
	if _, err := g.Authorize(mustAddr(t, "192.168.1.5"), "/data/x"); !errors.Is(err, impl.ErrNoExport) {
		t.Fatalf("authorizing a revoked export must fail with ErrNoExport, got %v", err)
	}
	if err := g.Revoke("/data"); !errors.Is(err, impl.ErrNoExport) {
		t.Fatalf("revoking twice must fail with ErrNoExport, got %v", err)
	}
}

func TestAuthorizePathEscapeRejected(t *testing.T) {
	g := newGuard(t)
	if err := g.AddExport("/data", roSpec()); err != nil {
		t.Fatalf("add export: %v", err)
	}
	if _, err := g.Authorize(mustAddr(t, "192.168.1.5"), "/data/../etc/passwd"); !errors.Is(err, impl.ErrPathEscapes) {
		t.Fatalf("dot dot request must fail with ErrPathEscapes, got %v", err)
	}
}

func TestAuthorizeCIDRAndSingleHost(t *testing.T) {
	g := newGuard(t)
	spec := roSpec()
	spec.Clients = []string{"192.168.1.0/24", "10.9.9.9"}
	if err := g.AddExport("/srv", spec); err != nil {
		t.Fatalf("add export: %v", err)
	}
	exp, err := g.Authorize(mustAddr(t, "192.168.1.7"), "/srv/share/file")
	if err != nil {
		t.Fatalf("cidr client: %v", err)
	}
	if !exp.Spec.ReadOnly {
		t.Fatalf("authorization must carry the export read only flag")
	}
	if _, err := g.Authorize(mustAddr(t, "10.9.9.9"), "/srv/share/file"); err != nil {
		t.Fatalf("single host client: %v", err)
	}
	if _, err := g.Authorize(mustAddr(t, "10.9.9.10"), "/srv/share/file"); !errors.Is(err, impl.ErrClientDenied) {
		t.Fatalf("neighbouring single host must be denied, got %v", err)
	}
	if got := g.Stats().Authorized; got != 2 {
		t.Fatalf("authorized counter = %d, want 2", got)
	}
}

func TestMapIdentityRootSquashed(t *testing.T) {
	g := newGuard(t)
	spec := roSpec()
	spec.RootSquash = true
	if err := g.AddExport("/home", spec); err != nil {
		t.Fatalf("add export: %v", err)
	}
	m, err := g.MapIdentity("/home", impl.Credential{Method: impl.MethodSys, User: "root", UID: 0, GIDs: []uint32{0}, Client: mustAddr(t, "192.168.1.5")})
	if err != nil {
		t.Fatalf("map root: %v", err)
	}
	if m.UID != impl.DefaultAnonymousUID || !m.Anonymous {
		t.Fatalf("mapped root = uid %d anonymous %v, want the anonymous uid", m.UID, m.Anonymous)
	}
	if len(m.GIDs) != 1 || m.GIDs[0] != impl.DefaultAnonymousGID {
		t.Fatalf("mapped root gids = %v, want the anonymous gid", m.GIDs)
	}
	if m.Export != "/home" || m.Version != 1 {
		t.Fatalf("mapped identity export = %q version %d, want /home version 1", m.Export, m.Version)
	}
}

func TestMapIdentityNoRootSquashWhitelisted(t *testing.T) {
	g := newGuard(t)
	spec := roSpec()
	spec.RootSquash = false
	spec.NoRootSquash = true
	spec.NoRootHosts = []string{"192.168.1.0/24"}
	if err := g.AddExport("/exports/nfs", spec); err != nil {
		t.Fatalf("add export: %v", err)
	}
	inside, err := g.MapIdentity("/exports/nfs", impl.Credential{Method: impl.MethodSys, User: "root", UID: 0, GIDs: []uint32{0}, Client: mustAddr(t, "192.168.1.5")})
	if err != nil {
		t.Fatalf("map whitelisted root: %v", err)
	}
	if inside.UID != 0 || inside.Anonymous {
		t.Fatalf("whitelisted root = uid %d anonymous %v, want uid 0 kept", inside.UID, inside.Anonymous)
	}
	outside, err := g.MapIdentity("/exports/nfs", impl.Credential{Method: impl.MethodSys, User: "root", UID: 0, GIDs: []uint32{0}, Client: mustAddr(t, "10.1.1.1")})
	if err != nil {
		t.Fatalf("map root outside the whitelist: %v", err)
	}
	if outside.UID != impl.DefaultAnonymousUID || !outside.Anonymous {
		t.Fatalf("root outside the whitelist = uid %d anonymous %v, want anonymous", outside.UID, outside.Anonymous)
	}
}

func TestMapIdentityAllSquash(t *testing.T) {
	g := newGuard(t)
	spec := roSpec()
	spec.AllSquash = true
	spec.RootSquash = false
	if err := g.AddExport("/scratch", spec); err != nil {
		t.Fatalf("add export: %v", err)
	}
	for _, uid := range []uint32{0, 1000} {
		m, err := g.MapIdentity("/scratch", impl.Credential{Method: impl.MethodSys, User: "someone", UID: uid, Client: mustAddr(t, "192.168.1.9")})
		if err != nil {
			t.Fatalf("map uid %d: %v", uid, err)
		}
		if m.UID != impl.DefaultAnonymousUID || !m.Anonymous {
			t.Fatalf("uid %d under all_squash = %d anonymous %v, want anonymous", uid, m.UID, m.Anonymous)
		}
	}
}

func TestMapIdentityKerberosRequiresMapping(t *testing.T) {
	g := newGuard(t)
	spec := roSpec()
	spec.Sec = impl.SecKrb5
	spec.Principals = []impl.PrincipalMap{{Principal: "nfs/node1@EXAMPLE.COM", UID: 1500, GIDs: []uint32{1500, 2000}}}
	if err := g.AddExport("/krb", spec); err != nil {
		t.Fatalf("add export: %v", err)
	}
	m, err := g.MapIdentity("/krb", impl.Credential{Method: impl.MethodKrb5, Principal: "nfs/node1@EXAMPLE.COM", Client: mustAddr(t, "192.168.1.5")})
	if err != nil {
		t.Fatalf("map known principal: %v", err)
	}
	if m.UID != 1500 || m.Method != impl.MethodKrb5 || m.Anonymous {
		t.Fatalf("mapped principal = %+v, want uid 1500 via krb5", m)
	}
	if len(m.GIDs) != 2 || m.GIDs[0] != 1500 || m.GIDs[1] != 2000 {
		t.Fatalf("mapped principal gids = %v, want the mapped group list", m.GIDs)
	}
	if _, err := g.MapIdentity("/krb", impl.Credential{Method: impl.MethodSys, User: "root", UID: 0, Client: mustAddr(t, "192.168.1.5")}); !errors.Is(err, impl.ErrIdentityUnmapped) {
		t.Fatalf("auth_sys credential on a krb5 export must fail with ErrIdentityUnmapped, got %v", err)
	}
	if _, err := g.MapIdentity("/krb", impl.Credential{Method: impl.MethodKrb5, Principal: "nfs/other@EXAMPLE.COM", Client: mustAddr(t, "192.168.1.5")}); !errors.Is(err, impl.ErrIdentityUnmapped) {
		t.Fatalf("unmapped principal must fail with ErrIdentityUnmapped, got %v", err)
	}
	if _, err := g.MapIdentity("/missing", impl.Credential{Method: impl.MethodSys, UID: 1}); !errors.Is(err, impl.ErrNoExport) {
		t.Fatalf("mapping against a missing export must fail with ErrNoExport, got %v", err)
	}
}

func TestSnapshotRestoreCacheAndCorrupt(t *testing.T) {
	cfg := impl.ExportPolicy{MaxExports: 4, MaxClientsPerExport: 4, MaxIdentityCache: 2}
	g := impl.NewNfsExportGuard(cfg)
	if err := g.AddExport("/data", roSpec()); err != nil {
		t.Fatalf("add /data: %v", err)
	}
	if err := g.AddExport("/data/team", roSpec()); err != nil {
		t.Fatalf("add /data/team: %v", err)
	}
	if _, err := g.Authorize(mustAddr(t, "192.168.1.5"), "/data/team/x"); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	for i, user := range []string{"u1", "u2", "u3"} {
		if _, err := g.MapIdentity("/data", impl.Credential{Method: impl.MethodSys, User: user, UID: uint32(1000 + i)}); err != nil {
			t.Fatalf("map %s: %v", user, err)
		}
	}
	if got := g.Stats().Cache; got != 2 {
		t.Fatalf("identity cache size = %d, want the configured cap 2", got)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewNfsExportGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Exports()); got != 2 {
		t.Fatalf("restored export count = %d, want 2", got)
	}
	team, ok := restored.Get("/data/team")
	if !ok || team.Version != 1 {
		t.Fatalf("restored /data/team = %+v ok=%v, want version 1", team, ok)
	}
	if _, err := restored.Authorize(mustAddr(t, "192.168.1.30"), "/data/team/y"); err != nil {
		t.Fatalf("authorize after restore: %v", err)
	}
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(restored.Exports()); got != 2 {
		t.Fatalf("rejected restore must keep live exports, count = %d", got)
	}
}

func TestConcurrentAuthorizeAndReplace(t *testing.T) {
	g := newGuard(t)
	if err := g.AddExport("/data", roSpec()); err != nil {
		t.Fatalf("add export: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	bad := 0
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			exp, err := g.Authorize(mustAddr(t, "192.168.1.5"), "/data/file")
			mu.Lock()
			defer mu.Unlock()
			if err != nil || exp.Path != "/data" {
				bad++
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			replacement := roSpec()
			replacement.ReadOnly = i%2 == 0
			_ = g.AddExport("/data", replacement)
		}
	}()
	wg.Wait()
	if bad != 0 {
		t.Fatalf("%d concurrent authorizations failed or matched the wrong export", bad)
	}
	if got := g.Stats().Authorized; got != 24 {
		t.Fatalf("authorized counter = %d, want 24", got)
	}
	if got := len(g.Exports()); got != 1 {
		t.Fatalf("export count = %d, want 1", got)
	}
}
