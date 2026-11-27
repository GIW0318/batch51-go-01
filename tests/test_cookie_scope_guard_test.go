package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-b-04"
)

func cookiePolicy() impl.CookiePolicy {
	return impl.CookiePolicy{
		PublicSuffixes: []string{"com", "com.cn", "co.uk"},
		MaxPerDomain:   3,
		MaxCookieBytes: 128,
		MaxTotal:       16,
		DefaultPath:    "/",
	}
}

func cookieTime() time.Time {
	return time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
}

func cookieNames(cookies []impl.Cookie) []string {
	names := make([]string, 0, len(cookies))
	for _, c := range cookies {
		names = append(names, c.Name)
	}
	return names
}

func hasCookieName(cookies []impl.Cookie, name string) bool {
	for _, c := range cookies {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestStoreHostOnlyCookie(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	c := impl.Cookie{Name: "sid", Value: "v1", Created: cookieTime()}
	if err := g.Store("https://app.example.com", c); err != nil {
		t.Fatalf("host only cookie must be stored, got %v", err)
	}
	stored := g.Cookies("https://app.example.com")
	if len(stored) != 1 {
		t.Fatalf("stored cookies = %d, want 1", len(stored))
	}
	if stored[0].Domain != "app.example.com" {
		t.Fatalf("host only domain = %q, want app.example.com", stored[0].Domain)
	}
	if stored[0].Path != "/" {
		t.Fatalf("default path = %q, want /", stored[0].Path)
	}
	scope, err := g.Explain("sid")
	if err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	if !scope.HostOnly {
		t.Fatalf("cookie without a domain attribute must be host only")
	}
	if scope.Domain != "app.example.com" {
		t.Fatalf("scope domain = %q, want app.example.com", scope.Domain)
	}
}

func TestStoreDomainCookieAccepted(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	c := impl.Cookie{Name: "lang", Value: "zh", Domain: ".example.com", Created: cookieTime()}
	if err := g.Store("https://app.example.com", c); err != nil {
		t.Fatalf("domain cookie must be stored, got %v", err)
	}
	scope, err := g.Explain("lang")
	if err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	if scope.HostOnly {
		t.Fatalf("a cookie with a domain attribute must not be host only")
	}
	if scope.Domain != "example.com" {
		t.Fatalf("scope domain = %q, want example.com", scope.Domain)
	}
}

func TestStoreRejectsDomainMismatch(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	c := impl.Cookie{Name: "lang", Value: "zh", Domain: "other.com", Created: cookieTime()}
	if err := g.Store("https://app.example.com", c); !errors.Is(err, impl.ErrDomainMismatch) {
		t.Fatalf("domain mismatch error = %v, want ErrDomainMismatch", err)
	}
}

func TestStoreRejectsSameSiteNoneWithoutSecure(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	c := impl.Cookie{Name: "cross", Value: "1", SameSite: "None", Secure: false, Created: cookieTime()}
	if err := g.Store("https://app.example.com", c); !errors.Is(err, impl.ErrSameSiteNone) {
		t.Fatalf("samesite none error = %v, want ErrSameSiteNone", err)
	}
}

func TestStoreRejectsHostPrefixWithDomain(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	c := impl.Cookie{
		Name: "__Host-token", Value: "abc", Secure: true, Path: "/",
		Domain: "example.com", Created: cookieTime(),
	}
	if err := g.Store("https://app.example.com", c); !errors.Is(err, impl.ErrPrefixViolation) {
		t.Fatalf("host prefix with domain error = %v, want ErrPrefixViolation", err)
	}
}

func TestStoreRejectsHostPrefixWithNonRootPath(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	c := impl.Cookie{
		Name: "__Host-token", Value: "abc", Secure: true, Path: "/app",
		Created: cookieTime(),
	}
	if err := g.Store("https://app.example.com", c); !errors.Is(err, impl.ErrPrefixViolation) {
		t.Fatalf("host prefix with non root path error = %v, want ErrPrefixViolation", err)
	}
}

func TestStoreRejectsSecurePrefixWithoutSecure(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	c := impl.Cookie{
		Name: "__Secure-token", Value: "abc", Path: "/", Secure: false,
		Created: cookieTime(),
	}
	if err := g.Store("https://app.example.com", c); !errors.Is(err, impl.ErrPrefixViolation) {
		t.Fatalf("secure prefix error = %v, want ErrPrefixViolation", err)
	}
}

func TestSelectMatchesNormalDirectoryPath(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	base := cookieTime()
	if err := g.Store("https://app.example.com", impl.Cookie{Name: "root", Value: "r", Path: "/", Created: base}); err != nil {
		t.Fatalf("store root cookie failed: %v", err)
	}
	if err := g.Store("https://app.example.com", impl.Cookie{Name: "app", Value: "a", Path: "/app", Created: base.Add(time.Minute)}); err != nil {
		t.Fatalf("store app cookie failed: %v", err)
	}
	selected := g.Select("https://app.example.com", impl.Request{
		Host:   "app.example.com",
		Path:   "/app/dashboard",
		Secure: true,
	})
	if len(selected) != 2 {
		t.Fatalf("selected cookies = %d, want 2", len(selected))
	}
	if selected[0].Path != "/app" {
		t.Fatalf("first selected path = %q, want the longer /app path", selected[0].Path)
	}
}

func TestSelectHostOnlyNotSentToSubdomain(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	if err := g.Store("https://example.com", impl.Cookie{Name: "sid", Value: "v1", Created: cookieTime()}); err != nil {
		t.Fatalf("store failed: %v", err)
	}
	if got := g.Cookies("https://example.com"); len(got) != 1 {
		t.Fatalf("origin cookie count = %d, want 1", len(got))
	}
	selected := g.Select("https://sub.example.com", impl.Request{
		Host:   "sub.example.com",
		Path:   "/",
		Secure: true,
	})
	if len(selected) != 0 {
		t.Fatalf("host only cookie leaked to a subdomain: %v", cookieNames(selected))
	}
}

func TestSelectWithholdsSecureCookieOnPlaintext(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	base := cookieTime()
	if err := g.Store("https://app.example.com", impl.Cookie{Name: "plain", Value: "p", Path: "/", Created: base}); err != nil {
		t.Fatalf("store plain cookie failed: %v", err)
	}
	if err := g.Store("https://app.example.com", impl.Cookie{Name: "sec", Value: "s", Path: "/", Secure: true, Created: base}); err != nil {
		t.Fatalf("store secure cookie failed: %v", err)
	}
	if got := g.Cookies("https://app.example.com"); len(got) != 2 {
		t.Fatalf("origin cookie count = %d, want 2", len(got))
	}
	selected := g.Select("http://app.example.com", impl.Request{
		Host:   "app.example.com",
		Path:   "/",
		Secure: false,
	})
	if len(selected) != 1 {
		t.Fatalf("plaintext selection = %d cookies, want only the insecure one", len(selected))
	}
	if selected[0].Name != "plain" {
		t.Fatalf("plaintext selection = %q, want plain", selected[0].Name)
	}
}

func TestSameNameNewerOverwrites(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	base := cookieTime()
	if err := g.Store("https://app.example.com", impl.Cookie{Name: "sid", Value: "old", Path: "/", Created: base}); err != nil {
		t.Fatalf("store old cookie failed: %v", err)
	}
	if err := g.Store("https://app.example.com", impl.Cookie{Name: "sid", Value: "new", Path: "/", Created: base.Add(time.Minute)}); err != nil {
		t.Fatalf("store new cookie failed: %v", err)
	}
	stored := g.Cookies("https://app.example.com")
	if len(stored) != 1 {
		t.Fatalf("stored cookies = %d, want 1 after overwrite", len(stored))
	}
	if stored[0].Value != "new" {
		t.Fatalf("stored value = %q, want new", stored[0].Value)
	}
}

func TestEvictRemovesExpired(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	now := cookieTime()
	c := impl.Cookie{
		Name: "gone", Value: "x", Path: "/",
		Created: now.Add(-2 * time.Hour),
		Expires: now.Add(time.Minute),
	}
	if err := g.Store("https://app.example.com", c); err != nil {
		t.Fatalf("store failed: %v", err)
	}
	if got := g.Evict(now.Add(2 * time.Minute)); got != 1 {
		t.Fatalf("evicted cookies = %d, want 1", got)
	}
	if got := g.Cookies("https://app.example.com"); len(got) != 0 {
		t.Fatalf("expired cookie is still visible: %v", cookieNames(got))
	}
}

func TestPerDomainLimitEvictsOldest(t *testing.T) {
	cfg := cookiePolicy()
	cfg.MaxPerDomain = 2
	g := impl.NewCookieScopeGuard(cfg)
	base := cookieTime()
	for i, name := range []string{"first", "second", "third"} {
		c := impl.Cookie{Name: name, Value: "v", Path: "/", Created: base.Add(time.Duration(i) * time.Minute)}
		if err := g.Store("https://app.example.com", c); err != nil {
			t.Fatalf("store %s failed: %v", name, err)
		}
	}
	stored := g.Cookies("https://app.example.com")
	if len(stored) != 2 {
		t.Fatalf("stored cookies = %d, want 2", len(stored))
	}
	if hasCookieName(stored, "first") {
		t.Fatalf("oldest cookie must be evicted at the per domain limit")
	}
	if !hasCookieName(stored, "third") {
		t.Fatalf("newest cookie must be present")
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	cfg := cookiePolicy()
	g := impl.NewCookieScopeGuard(cfg)
	c := impl.Cookie{Name: "sid", Value: "v1", Path: "/", Created: cookieTime()}
	if err := g.Store("https://app.example.com", c); err != nil {
		t.Fatalf("store failed: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewCookieScopeGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	stored := restored.Cookies("https://app.example.com")
	if len(stored) != 1 || stored[0].Name != "sid" {
		t.Fatalf("restored cookies = %v, want one sid cookie", cookieNames(stored))
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	g := impl.NewCookieScopeGuard(cookiePolicy())
	if err := g.Restore([]byte("not-a-cookie-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
}
