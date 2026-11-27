package task

import (
	"errors"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-27"
)

func newClassifier(t *testing.T, params impl.Params) *impl.DgaDomainClassifier {
	t.Helper()
	c, err := impl.NewDgaDomainClassifier(params)
	if err != nil {
		t.Fatalf("build classifier: %v", err)
	}
	return c
}

func TestClassifyFlagsDgaRegistrableLabel(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	v, err := c.Classify("k7q3x9m2v4z8.com")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if v.Score != 65 {
		t.Fatalf("score = %d, want 65", v.Score)
	}
	if v.Band != impl.BandDGA {
		t.Fatalf("band = %q, want %q", v.Band, impl.BandDGA)
	}
	if v.Label != "k7q3x9m2v4z8" {
		t.Fatalf("label = %q, want the registrable label", v.Label)
	}
	if v.Domain != "k7q3x9m2v4z8.com" {
		t.Fatalf("domain = %q, want the normalized name", v.Domain)
	}
	if v.Generation != 1 {
		t.Fatalf("generation = %d, want 1", v.Generation)
	}
}

func TestClassifySuspiciousBand(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	v, err := c.Classify("kq3f9xztv.com")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if v.Score != 53 {
		t.Fatalf("score = %d, want 53", v.Score)
	}
	if v.Band != impl.BandSuspicious {
		t.Fatalf("band = %q, want %q", v.Band, impl.BandSuspicious)
	}
	if v.Label != "kq3f9xztv" {
		t.Fatalf("label = %q, want kq3f9xztv", v.Label)
	}
}

func TestExplainReportsFeatureBreakdown(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	ex, err := c.Explain("k7q3x9m2v4z8.com")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if ex.Length != 12 || ex.Digits != 6 || ex.Vowels != 0 {
		t.Fatalf("length=%d digits=%d vowels=%d, want 12/6/0", ex.Length, ex.Digits, ex.Vowels)
	}
	if ex.MaxRun != 1 || ex.DictCoverage != 0 || ex.Hyphens != 0 {
		t.Fatalf("maxrun=%d coverage=%d hyphens=%d, want 1/0/0", ex.MaxRun, ex.DictCoverage, ex.Hyphens)
	}
	if ex.Score != 65 || ex.Band != impl.BandDGA {
		t.Fatalf("score=%d band=%q, want 65/dga", ex.Score, ex.Band)
	}
	want := []string{"digit_heavy", "long_label", "no_dict_match", "vowel_low"}
	if len(ex.Features) != len(want) {
		t.Fatalf("features = %v, want %v", ex.Features, want)
	}
	for i, name := range want {
		if ex.Features[i] != name {
			t.Fatalf("features = %v, want the sorted list %v", ex.Features, want)
		}
	}
}

func TestClassifyRejectsMalformedDomain(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	bad := []string{"", "bad domain.com", "..com", "-abc.com", "abc-.com", "a..b.com", "under_score.com", "a_b.com"}
	for _, domain := range bad {
		if _, err := c.Classify(domain); !errors.Is(err, impl.ErrBadDomain) {
			t.Fatalf("domain %q must be rejected with ErrBadDomain, got %v", domain, err)
		}
	}
}

func TestClassifyNormalizesCaseAndTrailingDot(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	first, err := c.Classify("EXAMPLE.COM.")
	if err != nil {
		t.Fatalf("classify upper case: %v", err)
	}
	if first.Domain != "example.com" {
		t.Fatalf("domain = %q, want example.com", first.Domain)
	}
	second, err := c.Classify("example.com")
	if err != nil {
		t.Fatalf("classify lower case: %v", err)
	}
	if second.Score != first.Score || second.Band != first.Band || second.Label != first.Label {
		t.Fatalf("case variant verdict %+v differs from %+v", second, first)
	}
	if got := c.Stats().CacheHits; got != 1 {
		t.Fatalf("cache hits = %d, want 1 for the case variant", got)
	}
}

func TestNewRejectsInvalidParams(t *testing.T) {
	inverted := impl.DefaultParams()
	inverted.DGAThreshold = 30
	if _, err := impl.NewDgaDomainClassifier(inverted); !errors.Is(err, impl.ErrBadParams) {
		t.Fatalf("inverted thresholds must fail with ErrBadParams, got %v", err)
	}
	negative := impl.DefaultParams()
	negative.DigitWeight = -1
	if _, err := impl.NewDgaDomainClassifier(negative); !errors.Is(err, impl.ErrBadParams) {
		t.Fatalf("negative weight must fail with ErrBadParams, got %v", err)
	}
	badCache := impl.DefaultParams()
	badCache.MaxCache = -4
	if _, err := impl.NewDgaDomainClassifier(badCache); !errors.Is(err, impl.ErrBadParams) {
		t.Fatalf("negative cache size must fail with ErrBadParams, got %v", err)
	}
}

func TestReloadAppliesParamsAndBumpsGeneration(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	if v, err := c.Classify("k7q3x9m2v4z8.com"); err != nil || v.Score != 65 {
		t.Fatalf("baseline classify = %+v, %v; want score 65", v, err)
	}
	lighter := impl.DefaultParams()
	lighter.DigitWeight = 8
	if err := c.Reload(nil, lighter); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := c.Generation(); got != 2 {
		t.Fatalf("generation = %d, want 2", got)
	}
	v, err := c.Classify("k7q3x9m2v4z8.com")
	if err != nil {
		t.Fatalf("classify after reload: %v", err)
	}
	if v.Score != 49 {
		t.Fatalf("score after reload = %d, want 49", v.Score)
	}
	if v.Generation != 2 {
		t.Fatalf("verdict generation = %d, want 2", v.Generation)
	}
	if v.Band != impl.BandSuspicious {
		t.Fatalf("band after reload = %q, want suspicious", v.Band)
	}
	if got := c.Stats().Reloads; got != 1 {
		t.Fatalf("reload counter = %d, want 1", got)
	}
}

func TestCacheIsKeyedByGeneration(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	if _, err := c.Classify("example.com"); err != nil {
		t.Fatalf("first classify: %v", err)
	}
	if c.CacheSize() != 1 {
		t.Fatalf("cache size = %d, want 1", c.CacheSize())
	}
	if got := c.Stats().CacheMisses; got != 1 {
		t.Fatalf("cache misses = %d, want 1", got)
	}
	if _, err := c.Classify("example.com"); err != nil {
		t.Fatalf("second classify: %v", err)
	}
	stats := c.Stats()
	if stats.CacheHits != 1 || stats.CacheMisses != 1 {
		t.Fatalf("cache hits=%d misses=%d, want 1 and 1", stats.CacheHits, stats.CacheMisses)
	}
	if err := c.Reload(nil, impl.DefaultParams()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if c.CacheSize() != 0 {
		t.Fatalf("cache size after reload = %d, want 0", c.CacheSize())
	}
	if _, err := c.Classify("example.com"); err != nil {
		t.Fatalf("classify in the new generation: %v", err)
	}
	stats = c.Stats()
	if stats.CacheMisses != 2 || c.CacheSize() != 1 {
		t.Fatalf("cache misses=%d size=%d, want 2 and 1", stats.CacheMisses, c.CacheSize())
	}
}

func TestReloadIsAtomicUnderConcurrentClassify(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	heavy := impl.DefaultParams()
	lighter := impl.DefaultParams()
	lighter.DigitWeight = 8
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 60; i++ {
			if i%2 == 0 {
				if err := c.Reload(nil, lighter); err != nil {
					t.Errorf("reload lighter: %v", err)
					return
				}
				continue
			}
			if err := c.Reload(nil, heavy); err != nil {
				t.Errorf("reload heavy: %v", err)
				return
			}
		}
	}()
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				v, err := c.Classify("k7q3x9m2v4z8.com")
				if err != nil {
					t.Errorf("concurrent classify: %v", err)
					return
				}
				wantScore, wantBand := 65, impl.BandDGA
				if v.Generation%2 == 0 {
					wantScore, wantBand = 49, impl.BandSuspicious
				}
				if v.Score != wantScore || v.Band != wantBand {
					t.Errorf("generation %d produced score=%d band=%q, want %d/%q", v.Generation, v.Score, v.Band, wantScore, wantBand)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestSnapshotRestoreKeepsGeneration(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	if _, err := c.Classify("k7q3x9m2v4z8.com"); err != nil {
		t.Fatalf("classify baseline: %v", err)
	}
	lighter := impl.DefaultParams()
	lighter.DigitWeight = 8
	if err := c.Reload(nil, lighter); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, err := c.Classify("k7q3x9m2v4z8.com"); err != nil {
		t.Fatalf("classify after reload: %v", err)
	}
	data, err := c.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	restored := newClassifier(t, impl.DefaultParams())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Generation(); got != 2 {
		t.Fatalf("restored generation = %d, want 2", got)
	}
	v, err := restored.Classify("k7q3x9m2v4z8.com")
	if err != nil {
		t.Fatalf("classify after restore: %v", err)
	}
	if v.Score != 49 || v.Generation != 2 {
		t.Fatalf("restored verdict = %+v, want score 49 in generation 2", v)
	}
	if got := restored.Stats().Classifications; got != 3 {
		t.Fatalf("restored classification counter = %d, want 3", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	if _, err := c.Classify("example.com"); err != nil {
		t.Fatalf("classify: %v", err)
	}
	if err := c.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := c.Generation(); got != 1 {
		t.Fatalf("generation after a rejected restore = %d, want 1", got)
	}
	if _, err := c.Classify("example.com"); err != nil {
		t.Fatalf("classify after rejected restore: %v", err)
	}
	if got := c.Stats().CacheHits; got != 1 {
		t.Fatalf("cache hits = %d, want the live cache to survive", got)
	}
}

func TestCacheEvictionBounded(t *testing.T) {
	params := impl.DefaultParams()
	params.MaxCache = 4
	c := newClassifier(t, params)
	for i := 0; i < 10; i++ {
		domain := "site" + string(rune('a'+i)) + ".com"
		if _, err := c.Classify(domain); err != nil {
			t.Fatalf("classify %s: %v", domain, err)
		}
	}
	if got := c.CacheSize(); got != 4 {
		t.Fatalf("cache size = %d, want the cap 4", got)
	}
	if got := c.Stats().Evictions; got != 6 {
		t.Fatalf("eviction counter = %d, want 6", got)
	}
}

func TestConcurrentClassifySameDomain(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.Classify("example.com")
			if err != nil {
				t.Errorf("concurrent classify: %v", err)
				return
			}
			if v.Score != 30 || v.Band != impl.BandBenign {
				t.Errorf("concurrent verdict = %d/%q, want 30/benign", v.Score, v.Band)
			}
		}()
	}
	wg.Wait()
	if got := c.Stats().Classifications; got != 16 {
		t.Fatalf("classification counter = %d, want 16", got)
	}
	if got := c.CacheSize(); got != 1 {
		t.Fatalf("cache size = %d, want 1 shared entry", got)
	}
}

func TestStatsCountsClassifications(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	for _, domain := range []string{"example.com", "example.com", "sample.com"} {
		if _, err := c.Classify(domain); err != nil {
			t.Fatalf("classify %s: %v", domain, err)
		}
	}
	stats := c.Stats()
	if stats.Classifications != 3 || stats.CacheMisses != 2 || stats.CacheHits != 1 {
		t.Fatalf("stats = %+v, want 3 classifications, 2 misses, 1 hit", stats)
	}
	if err := c.Reload(nil, impl.DefaultParams()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := c.Stats().Reloads; got != 1 {
		t.Fatalf("reload counter = %d, want 1", got)
	}
}

func TestClassifyRejectsOversizeDomain(t *testing.T) {
	c := newClassifier(t, impl.DefaultParams())
	longName := strings.Join([]string{
		strings.Repeat("a", 63),
		strings.Repeat("b", 63),
		strings.Repeat("c", 63),
		strings.Repeat("d", 63),
	}, ".")
	if _, err := c.Classify(longName); !errors.Is(err, impl.ErrBadDomain) {
		t.Fatalf("domain over 253 bytes must fail with ErrBadDomain, got %v", err)
	}
	longLabel := strings.Repeat("e", 64) + ".com"
	if _, err := c.Classify(longLabel); !errors.Is(err, impl.ErrBadDomain) {
		t.Fatalf("label over 63 bytes must fail with ErrBadDomain, got %v", err)
	}
}
