package task

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-12"
)

// fakeDirectory is a stand in for the upstream directory server.
type fakeDirectory struct {
	mu        sync.Mutex
	reply     impl.UpstreamReply
	err       error
	delay     time.Duration
	calls     int
	inflight  int
	peak      int
	lastDN    string
	lastLevel impl.SecurityLevel
}

func (f *fakeDirectory) Bind(dn, password string, level impl.SecurityLevel) (impl.UpstreamReply, error) {
	f.mu.Lock()
	f.calls++
	f.inflight++
	if f.inflight > f.peak {
		f.peak = f.inflight
	}
	f.lastDN = dn
	f.lastLevel = level
	reply, err, delay := f.reply, f.err, f.delay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mu.Lock()
	f.inflight--
	f.mu.Unlock()
	return reply, err
}

func (f *fakeDirectory) snapshot() (calls, peak int, lastDN string, lastLevel impl.SecurityLevel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.peak, f.lastDN, f.lastLevel
}

func testProxyConfig(f *fakeDirectory) impl.ProxyConfig {
	return impl.ProxyConfig{
		Upstream:         f,
		MaxConnsPerLevel: 8,
		RebindBudget:     1,
		LockoutWindow:    time.Minute,
		Now:              func() time.Time { return time.Date(2026, 4, 7, 8, 0, 0, 0, time.UTC) },
	}
}

func TestNormalizeDNTrimsAndLowercasesAttributeTypes(t *testing.T) {
	g := impl.NewLdapBindProxyGuard(testProxyConfig(&fakeDirectory{}))
	got, err := g.NormalizeDN("  CN = Alice ,  OU = People , DC = Example  ")
	if err != nil {
		t.Fatalf("normalize a well formed entry name: %v", err)
	}
	if want := "cn=Alice,ou=People,dc=Example"; got != want {
		t.Fatalf("normalized name = %q, want %q", got, want)
	}
	again, err := g.NormalizeDN("CN=Alice,OU=People,DC=Example")
	if err != nil {
		t.Fatalf("normalize the already tight name: %v", err)
	}
	if again != got {
		t.Fatalf("normalization is not idempotent: %q then %q", got, again)
	}
}

func TestNormalizeDNRejectsMalformedNames(t *testing.T) {
	g := impl.NewLdapBindProxyGuard(testProxyConfig(&fakeDirectory{}))
	for _, dn := range []string{"", "   ", "CNAlice", "=alice", "cn=alice,,ou=people"} {
		if _, err := g.NormalizeDN(dn); !errors.Is(err, impl.ErrBadDN) {
			t.Fatalf("entry name %q must fail with ErrBadDN, got %v", dn, err)
		}
	}
}

func TestBindForwardsToUpstreamAndRecordsConnection(t *testing.T) {
	fake := &fakeDirectory{}
	g := impl.NewLdapBindProxyGuard(testProxyConfig(fake))
	id, err := g.Bind("CN=Alice,OU=People", "pw-alice", impl.LevelTLS)
	if err != nil {
		t.Fatalf("bind alice: %v", err)
	}
	if id == "" {
		t.Fatalf("bind returned an empty connection id")
	}
	calls, _, lastDN, lastLevel := fake.snapshot()
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
	if lastDN != "cn=Alice,ou=People" {
		t.Fatalf("upstream saw entry name %q, want the normalized one", lastDN)
	}
	if lastLevel != impl.LevelTLS {
		t.Fatalf("upstream saw level %d, want LevelTLS", lastLevel)
	}
	ci, ok := g.Conn(id)
	if !ok {
		t.Fatalf("connection %q is not in the pool", id)
	}
	if !ci.Bound || ci.DN != "cn=Alice,ou=People" || ci.Level != impl.LevelTLS {
		t.Fatalf("connection = %+v, want a bound TLS connection for alice", ci)
	}
	if ci.Binds != 1 {
		t.Fatalf("connection bind count = %d, want 1", ci.Binds)
	}
	st := g.Stats()
	if st.Binds != 1 || st.PoolMisses != 1 || st.PoolHits != 0 {
		t.Fatalf("stats = %+v, want one miss and one bind", st)
	}
}

func TestBindReusesIdleConnectionForSameIdentity(t *testing.T) {
	fake := &fakeDirectory{}
	g := impl.NewLdapBindProxyGuard(testProxyConfig(fake))
	first, err := g.Bind("CN=Alice,OU=People", "pw-one", impl.LevelTLS)
	if err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if err := g.Release(first); err != nil {
		t.Fatalf("release: %v", err)
	}
	state := g.PoolState(impl.LevelTLS)
	if state.InUse != 0 || state.Idle != 1 || state.Total != 1 {
		t.Fatalf("pool state after release = %+v, want zero in use and one idle", state)
	}
	second, err := g.Bind("CN=Alice,OU=People", "pw-two", impl.LevelTLS)
	if err != nil {
		t.Fatalf("second bind: %v", err)
	}
	if second != first {
		t.Fatalf("pooled connection changed from %q to %q, want the idle one reused", first, second)
	}
	calls, _, _, _ := fake.snapshot()
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2 because a reused connection re-binds", calls)
	}
	if got := g.Stats().PoolHits; got != 1 {
		t.Fatalf("pool hits = %d, want 1", got)
	}
	if got := g.PoolState(impl.LevelTLS).Total; got != 1 {
		t.Fatalf("pool total = %d, want the single reused connection", got)
	}
}

func TestBindKeepsConnectionsSeparatePerLevel(t *testing.T) {
	fake := &fakeDirectory{}
	g := impl.NewLdapBindProxyGuard(testProxyConfig(fake))
	simple, err := g.Bind("CN=Alice,OU=People", "pw", impl.LevelSimple)
	if err != nil {
		t.Fatalf("simple bind: %v", err)
	}
	tls, err := g.Bind("CN=Alice,OU=People", "pw", impl.LevelTLS)
	if err != nil {
		t.Fatalf("tls bind: %v", err)
	}
	if simple == tls {
		t.Fatalf("a simple and a TLS bind share connection %q", simple)
	}
	if got := g.PoolState(impl.LevelSimple).Total; got != 1 {
		t.Fatalf("simple pool total = %d, want 1", got)
	}
	if got := g.PoolState(impl.LevelTLS).Total; got != 1 {
		t.Fatalf("tls pool total = %d, want 1", got)
	}
	if calls, _, _, lastLevel := fake.snapshot(); calls != 2 || lastLevel != impl.LevelTLS {
		t.Fatalf("upstream calls = %d last level = %d, want 2 calls ending on TLS", calls, lastLevel)
	}
}

func TestBindRejectsUnsupportedLevel(t *testing.T) {
	fake := &fakeDirectory{}
	g := impl.NewLdapBindProxyGuard(testProxyConfig(fake))
	for _, level := range []impl.SecurityLevel{impl.SecurityLevel(-1), impl.SecurityLevel(7)} {
		if _, err := g.Bind("CN=Alice,OU=People", "pw", level); !errors.Is(err, impl.ErrBadLevel) {
			t.Fatalf("level %d must fail with ErrBadLevel, got %v", level, err)
		}
	}
	if calls, _, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("upstream calls = %d after two rejected levels, want 0", calls)
	}
}

func TestBindRejectsEmptyEntryNameForAuthenticatedLevel(t *testing.T) {
	g := impl.NewLdapBindProxyGuard(testProxyConfig(&fakeDirectory{}))
	if _, err := g.Bind("", "", impl.LevelSimple); !errors.Is(err, impl.ErrBadDN) {
		t.Fatalf("an empty entry name on a simple bind must fail with ErrBadDN, got %v", err)
	}
	if _, err := g.Bind("   ", "pw", impl.LevelTLS); !errors.Is(err, impl.ErrBadDN) {
		t.Fatalf("a blank entry name on a TLS bind must fail with ErrBadDN, got %v", err)
	}
}

func TestRebindMovesConnectionToNewIdentity(t *testing.T) {
	fake := &fakeDirectory{}
	g := impl.NewLdapBindProxyGuard(testProxyConfig(fake))
	id, err := g.Bind("CN=Alice,OU=People", "pw-alice", impl.LevelTLS)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := g.Rebind(id, "  OU=Service , CN = Backup ", "pw-backup"); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	calls, _, lastDN, lastLevel := fake.snapshot()
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want a second bind for the new identity", calls)
	}
	if lastDN != "ou=Service,cn=Backup" {
		t.Fatalf("rebind forwarded %q, want the normalized new name", lastDN)
	}
	if lastLevel != impl.LevelTLS {
		t.Fatalf("rebind changed the security level to %d", lastLevel)
	}
	ci, ok := g.Conn(id)
	if !ok || !ci.Bound || ci.DN != "ou=Service,cn=Backup" {
		t.Fatalf("connection after rebind = %+v, want the new identity on the same id", ci)
	}
	if ci.Binds != 2 {
		t.Fatalf("connection bind count = %d, want 2", ci.Binds)
	}
	if got := g.Stats().Rebinds; got != 1 {
		t.Fatalf("rebind counter = %d, want 1", got)
	}
}

func TestRebindRejectsUnknownConnection(t *testing.T) {
	g := impl.NewLdapBindProxyGuard(testProxyConfig(&fakeDirectory{}))
	if err := g.Rebind("conn-404", "CN=Alice,OU=People", "pw"); !errors.Is(err, impl.ErrUnknownConn) {
		t.Fatalf("rebinding an unknown connection must fail with ErrUnknownConn, got %v", err)
	}
}

func TestReleaseIsIdempotentAndFreesOneSlot(t *testing.T) {
	g := impl.NewLdapBindProxyGuard(testProxyConfig(&fakeDirectory{}))
	id, err := g.Bind("CN=Alice,OU=People", "pw", impl.LevelTLS)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := g.Release(id); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := g.Release(id); err != nil {
		t.Fatalf("releasing twice must stay idempotent, got %v", err)
	}
	state := g.PoolState(impl.LevelTLS)
	if state.Idle != 1 || state.Total != 1 || state.InUse != 0 {
		t.Fatalf("pool state = %+v, want one idle connection and no double accounting", state)
	}
	if got := g.Stats().Released; got != 1 {
		t.Fatalf("release counter = %d, want 1", got)
	}
}

func TestPoolCapRefusesExtraConnections(t *testing.T) {
	fake := &fakeDirectory{}
	cfg := testProxyConfig(fake)
	cfg.MaxConnsPerLevel = 2
	g := impl.NewLdapBindProxyGuard(cfg)
	if _, err := g.Bind("CN=One,OU=People", "pw", impl.LevelTLS); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if _, err := g.Bind("CN=Two,OU=People", "pw", impl.LevelTLS); err != nil {
		t.Fatalf("second bind: %v", err)
	}
	if _, err := g.Bind("CN=Three,OU=People", "pw", impl.LevelTLS); !errors.Is(err, impl.ErrPoolExhausted) {
		t.Fatalf("a third connection must fail with ErrPoolExhausted, got %v", err)
	}
	if got := g.PoolState(impl.LevelTLS).Total; got != 2 {
		t.Fatalf("pool total = %d, want the configured cap 2", got)
	}
	if calls, _, _, _ := fake.snapshot(); calls != 2 {
		t.Fatalf("upstream calls = %d, want 2 because the third bind never reached the directory", calls)
	}
}

func TestUpstreamFailureIsForwardedWithoutCredential(t *testing.T) {
	fake := &fakeDirectory{reply: impl.UpstreamReply{Code: 49, Message: "invalid credentials for cn=svc", RemainingAttempts: 2}}
	g := impl.NewLdapBindProxyGuard(testProxyConfig(fake))
	_, err := g.Bind("CN=svc,OU=People", "S3cret!", impl.LevelSimple)
	if err == nil {
		t.Fatalf("a non zero upstream result code must surface as an error")
	}
	var bindErr *impl.BindError
	if !errors.As(err, &bindErr) {
		t.Fatalf("error %v is not a BindError", err)
	}
	if bindErr.Code != 49 {
		t.Fatalf("forwarded result code = %d, want 49", bindErr.Code)
	}
	if !strings.Contains(err.Error(), "invalid credentials") {
		t.Fatalf("forwarded error %q lost the upstream diagnostic", err.Error())
	}
	if strings.Contains(err.Error(), "S3cret!") {
		t.Fatalf("forwarded error %q leaks the credential", err.Error())
	}
	if got := g.PoolState(impl.LevelSimple).Total; got != 0 {
		t.Fatalf("a refused bind left %d connection in the pool, want 0", got)
	}
	if got := g.Stats().Failures; got != 1 {
		t.Fatalf("failure counter = %d, want 1", got)
	}
}

func TestConcurrentBindsForOneIdentitySerialize(t *testing.T) {
	fake := &fakeDirectory{delay: 2 * time.Millisecond}
	cfg := testProxyConfig(fake)
	cfg.MaxConnsPerLevel = 32
	g := impl.NewLdapBindProxyGuard(cfg)
	const workers = 8
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = g.Bind("CN=svc,OU=People", "pw", impl.LevelTLS)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d failed: %v", i, err)
		}
	}
	calls, peak, _, _ := fake.snapshot()
	if calls != workers {
		t.Fatalf("upstream calls = %d, want one per worker (%d)", calls, workers)
	}
	if peak != 1 {
		t.Fatalf("peak concurrent binds for one identity = %d, want 1", peak)
	}
}

func TestSnapshotOmitsCredentialAndRestoresPool(t *testing.T) {
	fake := &fakeDirectory{}
	cfg := testProxyConfig(fake)
	g := impl.NewLdapBindProxyGuard(cfg)
	id, err := g.Bind("CN=Alice,OU=People", "S3cret!", impl.LevelTLS)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := g.Release(id); err != nil {
		t.Fatalf("release: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	if strings.Contains(string(data), "S3cret!") {
		t.Fatalf("snapshot %q keeps the credential", string(data))
	}
	restored := impl.NewLdapBindProxyGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	state := restored.PoolState(impl.LevelTLS)
	if state.Total != 1 || state.Idle != 1 || state.InUse != 0 {
		t.Fatalf("restored pool state = %+v, want the idle connection back", state)
	}
	reused, err := restored.Bind("CN=Alice,OU=People", "pw", impl.LevelTLS)
	if err != nil {
		t.Fatalf("bind after restore: %v", err)
	}
	if reused != id {
		t.Fatalf("connection id after restore = %q, want %q", reused, id)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewLdapBindProxyGuard(testProxyConfig(&fakeDirectory{}))
	id, err := g.Bind("CN=Alice,OU=People", "pw", impl.LevelTLS)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := g.Restore([]byte("this is not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if _, ok := g.Conn(id); !ok {
		t.Fatalf("a rejected restore dropped the live connection %q", id)
	}
	if got := g.PoolState(impl.LevelTLS).Total; got != 1 {
		t.Fatalf("pool total after a rejected restore = %d, want 1", got)
	}
}
