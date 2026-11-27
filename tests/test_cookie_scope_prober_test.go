package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-safety-c-44"
)

const testHost = "app.example.test"

func cookie(name, value, path string) impl.CookieSpec {
	return impl.CookieSpec{Name: name, Value: value, Path: path}
}

func newProber(maxOps, maxPerHost int) *impl.CookieScopeProber {
	return impl.NewCookieScopeProber(impl.Limits{MaxOps: maxOps, MaxPerHost: maxPerHost})
}

func store(t *testing.T, p *impl.CookieScopeProber, host string, c impl.CookieSpec) impl.SetResult {
	t.Helper()
	result, err := p.Set(host, c)
	if err != nil {
		t.Fatalf("set %s: %v", c.Name, err)
	}
	return result
}

func names(jar impl.CookieJar) []string {
	out := make([]string, 0, len(jar.Cookies))
	for _, c := range jar.Cookies {
		out = append(out, c.Name)
	}
	return out
}

func mustJar(t *testing.T, p *impl.CookieScopeProber, host string) impl.CookieJar {
	t.Helper()
	jar, err := p.Jar(host)
	if err != nil {
		t.Fatalf("jar %s: %v", host, err)
	}
	return jar
}

func TestSetRejectsBadNameAndValue(t *testing.T) {
	p := newProber(64, 8)
	if _, err := p.Set(testHost, impl.CookieSpec{Value: "gsb-value"}); !errors.Is(err, impl.ErrBadName) {
		t.Fatalf("a cookie without a name must fail with ErrBadName, got %v", err)
	}
	if _, err := p.Set(testHost, impl.CookieSpec{Name: "token", Value: "real-secret"}); !errors.Is(err, impl.ErrBadValue) {
		t.Fatalf("a non marker value must fail with ErrBadValue, got %v", err)
	}
	if _, err := p.Set("  ", cookie("token", "gsb-token", "/")); !errors.Is(err, impl.ErrBadHost) {
		t.Fatalf("a blank host must fail with ErrBadHost, got %v", err)
	}
	if got := len(mustJar(t, p, testHost).Cookies); got != 0 {
		t.Fatalf("rejected cookies must not be stored, jar holds %d", got)
	}
}

func TestSetDefaultsHostOnlyCookie(t *testing.T) {
	p := newProber(64, 8)
	result := store(t, p, testHost, cookie("token", "gsb-token-1", ""))
	if !result.Accepted {
		t.Fatalf("a marker cookie must be accepted, got %+v", result)
	}
	if result.Reason != impl.ReasonStored {
		t.Fatalf("reason = %q, want %q", result.Reason, impl.ReasonStored)
	}
	if result.Prefix != "" {
		t.Fatalf("a plain cookie must not carry a prefix, got %q", result.Prefix)
	}
	jar := mustJar(t, p, testHost)
	if len(jar.Cookies) != 1 {
		t.Fatalf("jar = %+v, want the single cookie", jar)
	}
	stored := jar.Cookies[0]
	if stored.Domain != testHost || !stored.HostOnly {
		t.Fatalf("stored cookie = %+v, want a host only cookie on the set host", stored)
	}
	if stored.Path != "/" {
		t.Fatalf("defaulted path = %q, want /", stored.Path)
	}
	if result.StoreID == "" {
		t.Fatalf("store id must not be empty")
	}
}

func TestSetRejectsForeignDomain(t *testing.T) {
	p := newProber(64, 8)
	foreign := cookie("token", "gsb-token", "/")
	foreign.Domain = "other.test"
	if _, err := p.Set(testHost, foreign); !errors.Is(err, impl.ErrDomainMismatch) {
		t.Fatalf("a foreign domain must fail with ErrDomainMismatch, got %v", err)
	}
	single := cookie("token", "gsb-token", "/")
	single.Domain = "test"
	if _, err := p.Set(testHost, single); !errors.Is(err, impl.ErrDomainMismatch) {
		t.Fatalf("a single label domain must fail with ErrDomainMismatch, got %v", err)
	}
	badPath := cookie("token", "gsb-token", "app")
	if _, err := p.Set(testHost, badPath); !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("a path without a leading slash must fail with ErrBadPath, got %v", err)
	}
	if got := len(mustJar(t, p, testHost).Cookies); got != 0 {
		t.Fatalf("rejected cookies must not be stored, jar holds %d", got)
	}
}

func TestSetAcceptsParentDomain(t *testing.T) {
	p := newProber(64, 8)
	parent := cookie("token", "gsb-token", "/")
	parent.Domain = ".example.test"
	result := store(t, p, testHost, parent)
	if !result.Accepted {
		t.Fatalf("a parent domain cookie must be accepted, got %+v", result)
	}
	jar := mustJar(t, p, testHost)
	if len(jar.Cookies) != 1 {
		t.Fatalf("jar = %+v, want the single cookie", jar)
	}
	stored := jar.Cookies[0]
	if stored.Domain != "example.test" {
		t.Fatalf("stored domain = %q, want the leading dot stripped", stored.Domain)
	}
	if stored.HostOnly {
		t.Fatalf("a cookie with a Domain attribute must not be host only")
	}
}

func TestSetRejectsInsecureSameSiteNone(t *testing.T) {
	p := newProber(64, 8)
	none := cookie("token", "gsb-token", "/")
	none.SameSite = impl.SameSiteNone
	if _, err := p.Set(testHost, none); !errors.Is(err, impl.ErrInsecureSameSite) {
		t.Fatalf("samesite none without secure must fail with ErrInsecureSameSite, got %v", err)
	}
	none.Secure = true
	if result := store(t, p, testHost, none); !result.Accepted {
		t.Fatalf("samesite none with secure must be accepted, got %+v", result)
	}
	unknown := cookie("token", "gsb-token", "/")
	unknown.SameSite = "weird"
	if _, err := p.Set(testHost, unknown); !errors.Is(err, impl.ErrBadSameSite) {
		t.Fatalf("an unknown samesite mode must fail with ErrBadSameSite, got %v", err)
	}
}

func TestSetSecurePrefixNeedsSecure(t *testing.T) {
	p := newProber(64, 8)
	plain := cookie("__Secure-token", "gsb-token", "/")
	result := store(t, p, testHost, plain)
	if result.Accepted {
		t.Fatalf("a __Secure- cookie without the secure flag must be refused")
	}
	if result.Prefix != impl.PrefixInvalid || result.Reason != impl.ReasonPrefixViolated {
		t.Fatalf("refusal = %+v, want the prefix violation marked", result)
	}
	if got := len(mustJar(t, p, testHost).Cookies); got != 0 {
		t.Fatalf("a refused prefixed cookie must not be stored, jar holds %d", got)
	}
	secure := cookie("__Secure-token", "gsb-token", "/")
	secure.Secure = true
	secureResult := store(t, p, testHost, secure)
	if !secureResult.Accepted || secureResult.Prefix != impl.PrefixSecure {
		t.Fatalf("a secure __Secure- cookie must be accepted with its prefix, got %+v", secureResult)
	}
	host := cookie("__Host-token", "gsb-token", "/")
	host.Secure = true
	hostResult := store(t, p, testHost, host)
	if !hostResult.Accepted || hostResult.Prefix != impl.PrefixHost {
		t.Fatalf("a secure __Host- cookie on path / must be accepted with its prefix, got %+v", hostResult)
	}
}

func TestSetReplacesSameCookie(t *testing.T) {
	p := newProber(64, 8)
	first := store(t, p, testHost, cookie("token", "gsb-token-1", "/"))
	second := store(t, p, testHost, cookie("token", "gsb-token-2", "/"))
	if first.StoreID != second.StoreID {
		t.Fatalf("setting the same name, domain and path must reuse the slot: %q vs %q", first.StoreID, second.StoreID)
	}
	jar := mustJar(t, p, testHost)
	if len(jar.Cookies) != 1 {
		t.Fatalf("jar = %+v, want the replacement, not a duplicate", jar)
	}
	if jar.Cookies[0].Value != "gsb-token-2" {
		t.Fatalf("stored value = %q, want the latest one", jar.Cookies[0].Value)
	}
	other := store(t, p, testHost, cookie("token", "gsb-token-3", "/a"))
	if other.StoreID == first.StoreID {
		t.Fatalf("a different path must get its own slot")
	}
	if got := len(mustJar(t, p, testHost).Cookies); got != 2 {
		t.Fatalf("jar holds %d cookies, want 2", got)
	}
}

func TestSetDeletesViaNegativeMaxAge(t *testing.T) {
	p := newProber(64, 8)
	store(t, p, testHost, cookie("token", "gsb-token", "/a"))
	kill := cookie("token", "gsb-token", "/a")
	kill.MaxAge = -1
	result := store(t, p, testHost, kill)
	if result.Accepted || result.Reason != impl.ReasonDeleted {
		t.Fatalf("a negative max age must delete, got %+v", result)
	}
	if got := len(mustJar(t, p, testHost).Cookies); got != 0 {
		t.Fatalf("jar holds %d cookies after deletion, want 0", got)
	}
	if err := p.Delete(testHost, "token", "/a"); err != nil {
		t.Fatalf("deleting an absent cookie must be idempotent, got %v", err)
	}
}

func TestMatchOrdersByPathLength(t *testing.T) {
	p := newProber(64, 8)
	store(t, p, testHost, cookie("root", "gsb-root", "/"))
	store(t, p, testHost, cookie("mid", "gsb-mid", "/a"))
	store(t, p, testHost, cookie("deep", "gsb-deep", "/a/b"))
	matches, err := p.Match(impl.Request{Host: testHost, Path: "/a/b/c", Method: "GET", TopLevel: true})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("matches = %+v, want all three path scopes", matches)
	}
	want := []string{"deep", "mid", "root"}
	for i, name := range want {
		if matches[i].Name != name {
			t.Fatalf("match order = %v, want the longest path first %v", matches, want)
		}
		if matches[i].SendOrder != i+1 {
			t.Fatalf("send order of %q = %d, want %d", matches[i].Name, matches[i].SendOrder, i+1)
		}
	}
	partial, err := p.Match(impl.Request{Host: testHost, Path: "/a/x", Method: "GET", TopLevel: true})
	if err != nil {
		t.Fatalf("match partial: %v", err)
	}
	if len(partial) != 2 || partial[0].Name != "mid" {
		t.Fatalf("matches for /a/x = %+v, want mid and root only", partial)
	}
	if _, err := p.Match(impl.Request{Host: "", Path: "/"}); !errors.Is(err, impl.ErrBadHost) {
		t.Fatalf("a request without a host must fail with ErrBadHost, got %v", err)
	}
}

func TestMatchRespectsSecureFlag(t *testing.T) {
	p := newProber(64, 8)
	secure := cookie("session", "gsb-session", "/")
	secure.Secure = true
	store(t, p, testHost, secure)
	store(t, p, testHost, cookie("plain", "gsb-plain", "/"))
	matches, err := p.Match(impl.Request{Host: testHost, Path: "/", Method: "GET", Secure: false, TopLevel: true})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 || matches[0].Name != "plain" {
		t.Fatalf("insecure request matches = %+v, want only the plain cookie", matches)
	}
	matches, err = p.Match(impl.Request{Host: testHost, Path: "/", Method: "GET", Secure: true, TopLevel: true})
	if err != nil {
		t.Fatalf("match secure: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("secure request matches = %+v, want both cookies", matches)
	}
}

func TestMatchSameSiteFiltering(t *testing.T) {
	p := newProber(64, 8)
	strict := cookie("strict", "gsb-strict", "/")
	strict.SameSite = impl.SameSiteStrict
	store(t, p, testHost, strict)
	lax := cookie("lax", "gsb-lax", "/")
	lax.SameSite = impl.SameSiteLax
	store(t, p, testHost, lax)
	none := cookie("none", "gsb-none", "/")
	none.SameSite = impl.SameSiteNone
	none.Secure = true
	store(t, p, testHost, none)
	cross, err := p.Match(impl.Request{Host: testHost, Path: "/", Method: "GET", Secure: true, TopLevel: false})
	if err != nil {
		t.Fatalf("match cross site get: %v", err)
	}
	got := map[string]bool{}
	for _, m := range cross {
		got[m.Name] = true
	}
	if got["strict"] {
		t.Fatalf("a strict cookie must not be sent on a cross site request: %+v", cross)
	}
	if !got["lax"] || !got["none"] {
		t.Fatalf("cross site get matches = %+v, want lax and none", cross)
	}
	post, err := p.Match(impl.Request{Host: testHost, Path: "/", Method: "POST", Secure: true, TopLevel: false})
	if err != nil {
		t.Fatalf("match cross site post: %v", err)
	}
	for _, m := range post {
		if m.Name == "lax" {
			t.Fatalf("a lax cookie must not be sent on a cross site POST: %+v", post)
		}
	}
	top, err := p.Match(impl.Request{Host: testHost, Path: "/", Method: "POST", Secure: true, TopLevel: true})
	if err != nil {
		t.Fatalf("match top level: %v", err)
	}
	if len(top) != 3 {
		t.Fatalf("top level matches = %+v, want all three cookies", top)
	}
}

func TestMatchDedupesByNameLongestPath(t *testing.T) {
	p := newProber(64, 8)
	store(t, p, testHost, cookie("token", "gsb-root", "/"))
	store(t, p, testHost, cookie("token", "gsb-deep", "/a"))
	matches, err := p.Match(impl.Request{Host: testHost, Path: "/a/b", Method: "GET", TopLevel: true})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want one cookie per name", matches)
	}
	if matches[0].Value != "gsb-deep" || matches[0].Path != "/a" {
		t.Fatalf("winner = %+v, want the longest path value", matches[0])
	}
}

func TestJarCapEvictsOldest(t *testing.T) {
	p := newProber(64, 2)
	store(t, p, testHost, cookie("one", "gsb-1", "/"))
	store(t, p, testHost, cookie("two", "gsb-2", "/"))
	result := store(t, p, testHost, cookie("three", "gsb-3", "/"))
	if result.Evicted != 1 {
		t.Fatalf("evicted = %d, want the oldest cookie dropped", result.Evicted)
	}
	jar := mustJar(t, p, testHost)
	if got := names(jar); len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("jar = %v, want the two newest cookies", got)
	}
	if state, err := p.Budget(); err != nil || state.Cookies != 2 {
		t.Fatalf("budget cookies = %d err=%v, want 2", state.Cookies, err)
	}
}

func TestJudgeParentDomainAndHostOnly(t *testing.T) {
	p := newProber(64, 8)
	hostOnly := []impl.CookieMatch{{
		Name: "token", Domain: testHost, Path: "/", HostOnly: true, RequestHost: testHost, RequestPath: "/",
	}}
	verdict, err := p.Judge(hostOnly)
	if err != nil {
		t.Fatalf("judge host only: %v", err)
	}
	if verdict.Overridable || verdict.Reason != impl.ReasonHostOnly {
		t.Fatalf("host only verdict = %+v, want not overridable", verdict)
	}
	if len(verdict.Winners) != 1 || verdict.Winners[0] != "token@app.example.test|/" {
		t.Fatalf("winners = %v, want the qualified key", verdict.Winners)
	}
	parent := []impl.CookieMatch{{
		Name: "token", Domain: "example.test", Path: "/", HostOnly: false, RequestHost: testHost, RequestPath: "/",
	}}
	verdict, err = p.Judge(parent)
	if err != nil {
		t.Fatalf("judge parent: %v", err)
	}
	if !verdict.Overridable || verdict.Reason != impl.ReasonParentDomain {
		t.Fatalf("parent domain verdict = %+v, want overridable", verdict)
	}
	duplicate := []impl.CookieMatch{
		{Name: "sid", Domain: "example.test", Path: "/", HostOnly: false, RequestHost: testHost, RequestPath: "/a/b", SendOrder: 1},
		{Name: "sid", Domain: "example.test", Path: "/a", HostOnly: false, RequestHost: testHost, RequestPath: "/a/b", SendOrder: 2},
	}
	verdict, err = p.Judge(duplicate)
	if err != nil {
		t.Fatalf("judge duplicate: %v", err)
	}
	if !verdict.Overridable || verdict.Reason != impl.ReasonAmbiguousScope {
		t.Fatalf("duplicate verdict = %+v, want the ambiguous scope reason", verdict)
	}
	if len(verdict.Winners) != 1 || verdict.Winners[0] != "sid@example.test|/a" {
		t.Fatalf("duplicate winners = %v, want the longest path winner", verdict.Winners)
	}
	recorded, ok := p.Verdict(testHost, "/a/b")
	if !ok || !recorded.Overridable {
		t.Fatalf("the verdict must be recorded per host and path, got %+v ok=%v", recorded, ok)
	}
	if len(p.Verdicts()) != 2 {
		t.Fatalf("recorded combinations = %v, want two distinct host and path keys", p.Verdicts())
	}
	if _, err := p.Judge(nil); !errors.Is(err, impl.ErrNoMatches) {
		t.Fatalf("judging nothing must fail with ErrNoMatches, got %v", err)
	}
}

func TestSnapshotRestoreKeepsJarAndVerdicts(t *testing.T) {
	p := newProber(64, 8)
	store(t, p, testHost, cookie("token", "gsb-token", "/"))
	store(t, p, testHost, cookie("session", "gsb-session", "/a"))
	matches, err := p.Match(impl.Request{Host: testHost, Path: "/a/b", Method: "GET", TopLevel: true})
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if _, err := p.Judge(matches); err != nil {
		t.Fatalf("judge: %v", err)
	}
	before, err := p.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewCookieScopeProber(impl.Limits{MaxOps: 64, MaxPerHost: 8})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	jar := mustJar(t, restored, testHost)
	if len(jar.Cookies) != 2 {
		t.Fatalf("restored jar = %+v, want both cookies", jar)
	}
	if _, ok := restored.Verdict(testHost, "/a/b"); !ok {
		t.Fatalf("the judged combination must survive a restore")
	}
	after, err := restored.Budget()
	if err != nil {
		t.Fatalf("budget after restore: %v", err)
	}
	if after.Ops != before.Ops || after.Cookies != before.Cookies {
		t.Fatalf("restored budget = %+v, want the snapshot values %+v", after, before)
	}
	if err := restored.Restore([]byte("}{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(mustJar(t, restored, testHost).Cookies); got != 2 {
		t.Fatalf("a rejected restore must not touch live state, jar holds %d", got)
	}
}
