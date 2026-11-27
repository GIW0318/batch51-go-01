// Package task scores domain names for algorithmically generated patterns and
// explains the character distribution features behind each verdict.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Bands returned by Classify and Explain.
const (
	BandBenign     = "benign"
	BandSuspicious = "suspicious"
	BandDGA        = "dga"
)

// Errors returned by the classifier.
var (
	ErrBadDomain   = errors.New("domain name is malformed")
	ErrBadParams   = errors.New("classifier parameters are invalid")
	ErrBadSnapshot = errors.New("snapshot payload is invalid")
)

// Params holds the scoring weights and thresholds. Zero values fall back to the
// defaults documented on DefaultParams when the classifier is built.
type Params struct {
	DigitWeight         int
	VowelFloor          int
	BaseLongLabel       int
	LongLabelLen        int
	CoverageFloor       int
	MinDictLabel        int
	RunLimit            int
	RunPenalty          int
	HyphenLimit         int
	HyphenPenalty       int
	DGAThreshold        int
	SuspiciousThreshold int
	MaxCache            int
}

// DefaultParams returns the tuned defaults used by the service.
func DefaultParams() Params {
	return Params{
		DigitWeight:         40,
		VowelFloor:          25,
		BaseLongLabel:       10,
		LongLabelLen:        7,
		CoverageFloor:       30,
		MinDictLabel:        6,
		RunLimit:            4,
		RunPenalty:          5,
		HyphenLimit:         2,
		HyphenPenalty:       5,
		DGAThreshold:        65,
		SuspiciousThreshold: 40,
		MaxCache:            256,
	}
}

// Verdict is the compact classification result.
type Verdict struct {
	Domain     string
	Label      string
	Score      int
	Band       string
	Generation int64
}

// Explanation is the auditable feature breakdown behind a verdict.
type Explanation struct {
	Domain       string
	Label        string
	Length       int
	Digits       int
	Vowels       int
	MaxRun       int
	DictCoverage int
	Hyphens      int
	Score        int
	Band         string
	Features     []string
	Generation   int64
}

// Stats counts classification work.
type Stats struct {
	Classifications int
	CacheHits       int
	CacheMisses     int
	Evictions       int
	Reloads         int
}

// DgaDomainClassifier owns the dictionary generation, the whitelist and the
// per generation result cache.
type DgaDomainClassifier struct {
	mu sync.Mutex

	params     Params
	dict       []string
	dictSet    map[string]struct{}
	dictMaxLen int
	whitelist  map[string]bool
	generation int64

	// cache keys include the generation even though old entries are dropped on
	// reload; this makes accidental cross-generation reuse impossible.
	cache map[string]Explanation
	order []string

	stats Stats
}

// NewDgaDomainClassifier builds a classifier and validates the parameters.
func NewDgaDomainClassifier(params Params) (*DgaDomainClassifier, error) {
	p, err := normalizeParams(params)
	if err != nil {
		return nil, err
	}
	return &DgaDomainClassifier{
		params:     p,
		dictSet:    make(map[string]struct{}),
		whitelist:  make(map[string]bool),
		generation: 1,
		cache:      make(map[string]Explanation),
	}, nil
}

// Classify scores one domain name and returns its band.
func (c *DgaDomainClassifier) Classify(domain string) (Verdict, error) {
	normalized, err := normalizeDomain(domain)
	if err != nil {
		return Verdict{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	ex := c.classifyLocked(normalized)
	c.stats.Classifications++
	return verdictFromExplanation(ex), nil
}

// Explain returns the feature breakdown behind one verdict.
func (c *DgaDomainClassifier) Explain(domain string) (Explanation, error) {
	normalized, err := normalizeDomain(domain)
	if err != nil {
		return Explanation{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	ex := c.classifyLocked(normalized)
	c.stats.Classifications++
	return cloneExplanation(ex), nil
}

// Reload publishes a new dictionary and parameter generation in one step.
func (c *DgaDomainClassifier) Reload(dict []string, params Params) error {
	p, err := normalizeParams(params)
	if err != nil {
		return err
	}
	entries, wordSet, maxLen := normalizeDictionary(dict)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.params = p
	c.dict = entries
	c.dictSet = wordSet
	c.dictMaxLen = maxLen
	c.generation++
	c.cache = make(map[string]Explanation)
	c.order = nil
	c.stats.Reloads++
	return nil
}

// Whitelist adds exact domains that must always grade as benign.
func (c *DgaDomainClassifier) Whitelist(domains []string) error {
	normalized := make([]string, 0, len(domains))
	for _, domain := range domains {
		name, err := normalizeDomain(domain)
		if err != nil {
			return err
		}
		normalized = append(normalized, name)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	removed := false
	for _, domain := range normalized {
		c.whitelist[domain] = true
		key := c.cacheKeyLocked(domain)
		if _, ok := c.cache[key]; ok {
			delete(c.cache, key)
			removed = true
		}
	}
	if removed {
		c.compactOrderLocked()
	}
	return nil
}

// Generation returns the generation of the current dictionary and parameters.
func (c *DgaDomainClassifier) Generation() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

// CacheSize returns the number of cached verdicts.
func (c *DgaDomainClassifier) CacheSize() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cache)
}

// ClearCache drops every cached verdict of the current generation.
func (c *DgaDomainClassifier) ClearCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]Explanation)
	c.order = nil
}

// Snapshot serialises the generation, dictionary, whitelist and counters.
func (c *DgaDomainClassifier) Snapshot() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	whitelist := make([]string, 0, len(c.whitelist))
	for domain := range c.whitelist {
		whitelist = append(whitelist, domain)
	}
	sort.Strings(whitelist)

	payload := snapshotPayload{
		Version:    snapshotVersion,
		Generation: c.generation,
		Params:     c.params,
		Dict:       append([]string(nil), c.dict...),
		Whitelist:  whitelist,
		Stats:      c.stats,
		Cache:      make([]snapshotCacheEntry, 0, len(c.cache)),
	}
	seen := make(map[string]bool, len(c.cache))
	for _, key := range c.order {
		ex, ok := c.cache[key]
		if !ok || seen[ex.Domain] {
			continue
		}
		seen[ex.Domain] = true
		payload.Cache = append(payload.Cache, snapshotCacheEntry{
			Domain:      ex.Domain,
			Explanation: cloneExplanation(ex),
		})
	}
	// A restored snapshot must not lose cache entries even if an older snapshot
	// carried an order slice with stale keys.
	for key, ex := range c.cache {
		if seen[ex.Domain] {
			continue
		}
		if !strings.HasPrefix(key, cacheGenerationPrefix(c.generation)) {
			continue
		}
		seen[ex.Domain] = true
		payload.Cache = append(payload.Cache, snapshotCacheEntry{
			Domain:      ex.Domain,
			Explanation: cloneExplanation(ex),
		})
	}

	return json.Marshal(payload)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (c *DgaDomainClassifier) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Version != snapshotVersion || payload.Generation < 1 {
		return ErrBadSnapshot
	}
	p, err := normalizeParams(payload.Params)
	if err != nil {
		return ErrBadSnapshot
	}
	entries, wordSet, maxLen := normalizeDictionary(payload.Dict)

	whitelist := make(map[string]bool, len(payload.Whitelist))
	for _, domain := range payload.Whitelist {
		name, err := normalizeDomain(domain)
		if err != nil {
			return ErrBadSnapshot
		}
		whitelist[name] = true
	}

	if !validStats(payload.Stats) || len(payload.Cache) > p.MaxCache {
		return ErrBadSnapshot
	}

	cache := make(map[string]Explanation, len(payload.Cache))
	order := make([]string, 0, len(payload.Cache))
	for _, entry := range payload.Cache {
		name, err := normalizeDomain(entry.Domain)
		if err != nil || name != entry.Domain {
			return ErrBadSnapshot
		}
		ex := entry.Explanation
		if ex.Domain != name || ex.Generation != payload.Generation {
			return ErrBadSnapshot
		}
		if ex.Score < 0 || ex.Score > 100 || !validBand(ex.Band) || !validFeatures(ex.Features) {
			return ErrBadSnapshot
		}
		key := cacheKey(payload.Generation, name)
		if _, exists := cache[key]; exists {
			return ErrBadSnapshot
		}
		cache[key] = cloneExplanation(ex)
		order = append(order, key)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.params = p
	c.dict = entries
	c.dictSet = wordSet
	c.dictMaxLen = maxLen
	c.whitelist = whitelist
	c.generation = payload.Generation
	c.cache = cache
	c.order = order
	c.stats = payload.Stats
	return nil
}

// Stats returns a copy of the counters.
func (c *DgaDomainClassifier) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

func (c *DgaDomainClassifier) classifyLocked(domain string) Explanation {
	key := c.cacheKeyLocked(domain)
	if ex, ok := c.cache[key]; ok {
		c.stats.CacheHits++
		return cloneExplanation(ex)
	}

	c.stats.CacheMisses++
	ex := c.evaluateLocked(domain)
	c.addCacheLocked(key, ex)
	return ex
}

func (c *DgaDomainClassifier) evaluateLocked(domain string) Explanation {
	labels := strings.Split(domain, ".")
	// All labels except the final TLD participate. The second-to-last label is
	// the registrable label; everything before it is a subdomain label.
	candidates := labels[:len(labels)-1]

	var best Explanation
	bestScore := -1
	for _, label := range candidates {
		ex := scoreLabel(label, c.params, c.dictSet, c.dictMaxLen)
		if ex.Score > bestScore {
			best = ex
			bestScore = ex.Score
		}
	}
	best.Domain = domain
	best.Generation = c.generation

	if c.whitelist[domain] {
		best.Score = 0
		best.Band = BandBenign
		best.Features = nil
		return best
	}
	if c.popularSuffixExemptLocked(labels, &best) {
		best.Score = 0
		best.Band = BandBenign
		best.Features = nil
		return best
	}

	best.Band = bandForScore(best.Score, c.params)
	return best
}

func (c *DgaDomainClassifier) addCacheLocked(key string, ex Explanation) {
	if _, exists := c.cache[key]; exists {
		c.cache[key] = cloneExplanation(ex)
		return
	}
	c.cache[key] = cloneExplanation(ex)
	c.order = append(c.order, key)
	maxCache := c.params.MaxCache
	for len(c.cache) > maxCache && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		if _, ok := c.cache[oldest]; ok {
			delete(c.cache, oldest)
			c.stats.Evictions++
		}
	}
}

func (c *DgaDomainClassifier) compactOrderLocked() {
	kept := c.order[:0]
	for _, key := range c.order {
		if _, ok := c.cache[key]; ok {
			kept = append(kept, key)
		}
	}
	c.order = kept
}

func (c *DgaDomainClassifier) cacheKeyLocked(domain string) string {
	return cacheKey(c.generation, domain)
}

// popularSuffixExemptLocked is a deliberately narrow brand-name escape hatch:
// numeric brand labels on an established suffix are not algorithmically
// generated merely because their digits score highly. It never exempts a
// subdomain label, and a random alphanumeric label still has to be whitelisted
// explicitly.
func (c *DgaDomainClassifier) popularSuffixExemptLocked(labels []string, best *Explanation) bool {
	if len(labels) < 2 || !popularTLD(labels[len(labels)-1]) {
		return false
	}
	registrable := labels[len(labels)-2]
	if registrable == "" || best.Label != registrable {
		return false
	}
	if len(registrable) > c.params.MinDictLabel {
		return false
	}
	for i := 0; i < len(registrable); i++ {
		if registrable[i] < '0' || registrable[i] > '9' {
			return false
		}
	}
	return true
}

func verdictFromExplanation(ex Explanation) Verdict {
	return Verdict{
		Domain:     ex.Domain,
		Label:      ex.Label,
		Score:      ex.Score,
		Band:       ex.Band,
		Generation: ex.Generation,
	}
}

func cloneExplanation(ex Explanation) Explanation {
	if ex.Features != nil {
		ex.Features = append([]string(nil), ex.Features...)
	}
	return ex
}

func scoreLabel(label string, p Params, dictSet map[string]struct{}, dictMaxLen int) Explanation {
	n := len(label)
	digits := 0
	vowels := 0
	hyphens := 0
	maxRun := 0
	run := 0
	for i := 0; i < n; i++ {
		ch := label[i]
		switch {
		case ch >= '0' && ch <= '9':
			digits++
			run = 0
		case ch == '-':
			hyphens++
			run = 0
		case ch >= 'a' && ch <= 'z':
			if isVowel(ch) {
				vowels++
				run = 0
			} else {
				run++
				if run > maxRun {
					maxRun = run
				}
			}
		default:
			run = 0
		}
	}

	coverage := dictionaryCoverage(label, dictSet, dictMaxLen)
	score := int64(0)
	features := make([]string, 0, 6)

	if n >= p.LongLabelLen {
		score += int64(p.BaseLongLabel)
		features = append(features, "long_label")
	}
	if digits > 0 {
		score += int64(p.DigitWeight) * int64(digits) / int64(n)
		features = append(features, "digit_heavy")
	}
	if n > 0 && int64(vowels)*100/int64(n) < int64(p.VowelFloor) {
		score += 15
		features = append(features, "vowel_low")
	}
	if maxRun > p.RunLimit {
		score += int64(maxRun-p.RunLimit) * int64(p.RunPenalty)
		features = append(features, "long_consonant_run")
	}
	if coverage < p.CoverageFloor && n >= p.MinDictLabel {
		score += 20
		features = append(features, "no_dict_match")
	}
	if hyphens > p.HyphenLimit {
		score += int64(p.HyphenPenalty)
		features = append(features, "hyphen_heavy")
	}
	if score > 100 {
		score = 100
	}
	sort.Strings(features)

	return Explanation{
		Label:        label,
		Length:       n,
		Digits:       digits,
		Vowels:       vowels,
		MaxRun:       maxRun,
		DictCoverage: coverage,
		Hyphens:      hyphens,
		Score:        int(score),
		Band:         bandForScore(int(score), p),
		Features:     features,
	}
}

func dictionaryCoverage(label string, dictSet map[string]struct{}, maxLen int) int {
	n := len(label)
	if n == 0 || maxLen < minDictWordLen {
		return 0
	}
	covered := 0
	for i := 0; i < n; {
		end := i + maxLen
		if end > n {
			end = n
		}
		matched := 0
		for j := end; j >= i+minDictWordLen; j-- {
			if _, ok := dictSet[label[i:j]]; ok {
				matched = j - i
				break
			}
		}
		if matched > 0 {
			covered += matched
			i += matched
		} else {
			i++
		}
	}
	return covered * 100 / n
}

func bandForScore(score int, p Params) string {
	if score >= p.DGAThreshold {
		return BandDGA
	}
	if score >= p.SuspiciousThreshold {
		return BandSuspicious
	}
	return BandBenign
}

func isVowel(ch byte) bool {
	switch ch {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	default:
		return false
	}
}

const (
	minDictWordLen  = 3
	snapshotVersion = 1
)

func cacheGenerationPrefix(generation int64) string {
	return strconv.FormatInt(generation, 10) + "|"
}

func cacheKey(generation int64, domain string) string {
	return cacheGenerationPrefix(generation) + domain
}

func normalizeParams(params Params) (Params, error) {
	defaults := DefaultParams()
	p := params
	fallback := func(value, def int) int {
		if value == 0 {
			return def
		}
		return value
	}
	p.DigitWeight = fallback(p.DigitWeight, defaults.DigitWeight)
	p.VowelFloor = fallback(p.VowelFloor, defaults.VowelFloor)
	p.BaseLongLabel = fallback(p.BaseLongLabel, defaults.BaseLongLabel)
	p.LongLabelLen = fallback(p.LongLabelLen, defaults.LongLabelLen)
	p.CoverageFloor = fallback(p.CoverageFloor, defaults.CoverageFloor)
	p.MinDictLabel = fallback(p.MinDictLabel, defaults.MinDictLabel)
	p.RunLimit = fallback(p.RunLimit, defaults.RunLimit)
	p.RunPenalty = fallback(p.RunPenalty, defaults.RunPenalty)
	p.HyphenLimit = fallback(p.HyphenLimit, defaults.HyphenLimit)
	p.HyphenPenalty = fallback(p.HyphenPenalty, defaults.HyphenPenalty)
	p.DGAThreshold = fallback(p.DGAThreshold, defaults.DGAThreshold)
	p.SuspiciousThreshold = fallback(p.SuspiciousThreshold, defaults.SuspiciousThreshold)
	p.MaxCache = fallback(p.MaxCache, defaults.MaxCache)

	if p.DigitWeight < 0 || p.BaseLongLabel < 0 || p.RunPenalty < 0 || p.HyphenPenalty < 0 {
		return Params{}, ErrBadParams
	}
	if p.VowelFloor < 0 || p.VowelFloor > 100 || p.CoverageFloor < 0 || p.CoverageFloor > 100 {
		return Params{}, ErrBadParams
	}
	if p.LongLabelLen < 0 || p.MinDictLabel < 0 || p.RunLimit < 0 || p.HyphenLimit < 0 {
		return Params{}, ErrBadParams
	}
	if p.MaxCache < 1 {
		return Params{}, ErrBadParams
	}
	if p.DGAThreshold < 0 || p.DGAThreshold > 100 || p.SuspiciousThreshold < 0 || p.SuspiciousThreshold > 100 {
		return Params{}, ErrBadParams
	}
	if p.DGAThreshold <= p.SuspiciousThreshold {
		return Params{}, ErrBadParams
	}
	return p, nil
}

func normalizeDictionary(dict []string) ([]string, map[string]struct{}, int) {
	entries := make([]string, 0, len(dict))
	wordSet := make(map[string]struct{}, len(dict))
	maxLen := 0
	for _, raw := range dict {
		word := strings.ToLower(strings.TrimSpace(raw))
		if word == "" {
			continue
		}
		if !isASCIIWord(word) {
			encoded, err := encodeIDNLabel(word)
			if err != nil {
				continue
			}
			word = encoded
		}
		if len(word) < minDictWordLen {
			continue
		}
		if _, exists := wordSet[word]; exists {
			continue
		}
		wordSet[word] = struct{}{}
		entries = append(entries, word)
		if len(word) > maxLen {
			maxLen = len(word)
		}
	}
	return entries, wordSet, maxLen
}

func isASCIIWord(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func normalizeDomain(input string) (string, error) {
	if input == "" || !utf8.ValidString(input) {
		return "", ErrBadDomain
	}
	s := strings.ToLower(input)
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= '\uff01' && r <= '\uff5e':
			return r - 0xfee0
		case r == '\u3002', r == '\uff61':
			return '.'
		default:
			return r
		}
	}, s)
	if strings.HasSuffix(s, ".") {
		s = s[:len(s)-1]
	}
	if s == "" {
		return "", ErrBadDomain
	}

	rawLabels := strings.Split(s, ".")
	if len(rawLabels) < 2 {
		return "", ErrBadDomain
	}
	labels := make([]string, 0, len(rawLabels))
	for _, raw := range rawLabels {
		if raw == "" {
			return "", ErrBadDomain
		}
		label, err := normalizeLabel(raw)
		if err != nil {
			return "", ErrBadDomain
		}
		if len(label) == 0 || len(label) > 63 {
			return "", ErrBadDomain
		}
		labels = append(labels, label)
	}
	normalized := strings.Join(labels, ".")
	if len(normalized) > 253 {
		return "", ErrBadDomain
	}
	return normalized, nil
}

func normalizeLabel(label string) (string, error) {
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return "", ErrBadDomain
	}
	nonASCII := false
	for _, r := range label {
		if r < unicode.MaxASCII {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return "", ErrBadDomain
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", ErrBadDomain
		}
		nonASCII = true
	}
	if !nonASCII {
		return label, nil
	}
	encoded, err := encodeIDNLabel(label)
	if err != nil {
		return "", ErrBadDomain
	}
	return encoded, nil
}

func encodeIDNLabel(label string) (string, error) {
	encoded, err := punycodeEncode(label)
	if err != nil {
		return "", err
	}
	return "xn--" + encoded, nil
}

const (
	pcBase        = 36
	pcTMin        = 1
	pcTMax        = 26
	pcSkew        = 38
	pcDamp        = 700
	pcInitialBias = 72
	pcInitialN    = 128
)

func punycodeEncode(label string) (string, error) {
	runes := []rune(label)
	out := make([]byte, 0, len(label)+8)
	basic := 0
	for _, r := range runes {
		if r < unicode.MaxASCII {
			basic++
			out = append(out, byte(r))
		}
	}
	h := basic
	if basic > 0 {
		out = append(out, '-')
	}

	n := pcInitialN
	delta := 0
	bias := pcInitialBias
	for h < len(runes) {
		m := int(^uint(0) >> 1)
		for _, r := range runes {
			if int(r) >= n && int(r) < m {
				m = int(r)
			}
		}
		if m == int(^uint(0)>>1) {
			return "", errors.New("punycode: no code point")
		}
		delta += (m - n) * (h + 1)
		n = m
		for _, r := range runes {
			cp := int(r)
			if cp < n {
				delta++
			}
			if cp != n {
				continue
			}
			q := delta
			for k := pcBase; ; k += pcBase {
				t := pcTMin
				if k <= bias {
					t = pcTMin
				} else if k >= bias+pcTMax {
					t = pcTMax
				} else {
					t = k - bias
				}
				if q < t {
					break
				}
				out = append(out, punycodeDigit(t+(q-t)%(pcBase-t)))
				q = (q - t) / (pcBase - t)
			}
			out = append(out, punycodeDigit(q))
			bias = punycodeAdapt(delta, h+1, h == basic)
			delta = 0
			h++
		}
		delta++
		n++
	}
	return string(out), nil
}

func punycodeDigit(value int) byte {
	if value < 26 {
		return byte('a' + value)
	}
	return byte('0' + value - 26)
}

func punycodeAdapt(delta, numPoints int, firstTime bool) int {
	if firstTime {
		delta /= pcDamp
	} else {
		delta /= 2
	}
	delta += delta / numPoints
	k := 0
	for delta > ((pcBase-pcTMin)*pcTMax)/2 {
		delta /= pcBase - pcTMin
		k += pcBase
	}
	return k + (pcBase-pcTMin+1)*delta/(delta+pcSkew)
}

type snapshotPayload struct {
	Version    int                  `json:"version"`
	Generation int64                `json:"generation"`
	Params     Params               `json:"params"`
	Dict       []string             `json:"dict"`
	Whitelist  []string             `json:"whitelist"`
	Stats      Stats                `json:"stats"`
	Cache      []snapshotCacheEntry `json:"cache"`
}

type snapshotCacheEntry struct {
	Domain      string      `json:"domain"`
	Explanation Explanation `json:"explanation"`
}

func validStats(stats Stats) bool {
	return stats.Classifications >= 0 && stats.CacheHits >= 0 &&
		stats.CacheMisses >= 0 && stats.Evictions >= 0 && stats.Reloads >= 0
}

func validBand(band string) bool {
	return band == BandBenign || band == BandSuspicious || band == BandDGA
}

func validFeatures(features []string) bool {
	known := map[string]bool{
		"digit_heavy":        true,
		"hyphen_heavy":       true,
		"long_consonant_run": true,
		"long_label":         true,
		"no_dict_match":      true,
		"vowel_low":          true,
	}
	previous := ""
	for i, feature := range features {
		if !known[feature] {
			return false
		}
		if i > 0 && previous > feature {
			return false
		}
		previous = feature
	}
	return true
}

func popularTLD(tld string) bool {
	switch tld {
	case "com", "net", "org", "edu", "gov", "mil", "int",
		"cn", "io", "co", "ai", "app", "dev", "info", "biz",
		"me", "tv", "cc":
		return true
	default:
		return false
	}
}
