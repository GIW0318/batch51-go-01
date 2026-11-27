package task

import (
	"errors"
	"strings"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-38"
)

type memoryTarget struct {
	mu      sync.Mutex
	uploads int
	handler string
	bodies  map[string]string
}

func (m *memoryTarget) Upload(point impl.UploadPoint, v impl.UploadVariant) (impl.Stored, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uploads++
	if m.bodies == nil {
		m.bodies = map[string]string{}
	}
	path := "/uploads/" + v.Filename
	m.bodies[path] = v.Head + v.Body
	return impl.Stored{Path: path, Status: 201, Accepted: true}, nil
}

func (m *memoryTarget) Fetch(path string) (impl.Served, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return impl.Served{Status: 200, Handler: m.handler, Body: m.bodies[path]}, nil
}

func (m *memoryTarget) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.uploads
}

func uploadForm() impl.UploadForm {
	return impl.UploadForm{
		Action: "/upload",
		Method: "POST",
		Fields: []impl.Field{
			{Name: "title", Type: "text"},
			{Name: "avatar", Type: "file", Accept: "image/png, image/jpeg", MaxBytes: 2 << 20},
			{Name: "resume", Type: "file", Accept: "application/pdf", MaxBytes: 8 << 20},
		},
	}
}

func openProber(t *testing.T, cfg impl.ProberConfig, tg impl.Target) (*impl.UploadBypassProber, []impl.UploadPoint) {
	t.Helper()
	p := impl.NewUploadBypassProber(cfg)
	if err := p.SetTarget(tg); err != nil {
		t.Fatalf("set target must succeed, got %v", err)
	}
	points, err := p.Enumerate(uploadForm())
	if err != nil {
		t.Fatalf("enumerate must succeed, got %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("enumerated points = %d, want 2", len(points))
	}
	return p, points
}

func defaultConfig() impl.ProberConfig {
	return impl.ProberConfig{MaxUploads: 16, MaxRequests: 64,
		ScriptHandlers: []string{"php-fpm", "mod_php", "cgi"}}
}

func TestEnumerateExtractsFileFields(t *testing.T) {
	p := impl.NewUploadBypassProber(defaultConfig())
	points, err := p.Enumerate(uploadForm())
	if err != nil {
		t.Fatalf("enumerate must succeed, got %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("point count = %d, want 2 file fields", len(points))
	}
	if points[0].Field != "avatar" || points[1].Field != "resume" {
		t.Fatalf("points = %q,%q, want avatar,resume in form order", points[0].Field, points[1].Field)
	}
	if len(points[0].Accept) != 2 || points[0].Accept[0] != "image/png" || points[0].Accept[1] != "image/jpeg" {
		t.Fatalf("avatar accept = %v, want [image/png image/jpeg]", points[0].Accept)
	}
	if points[0].MaxBytes != 2<<20 {
		t.Fatalf("avatar max bytes = %d, want %d", points[0].MaxBytes, 2<<20)
	}
	if points[1].MaxBytes != 8<<20 {
		t.Fatalf("resume max bytes = %d, want %d", points[1].MaxBytes, 8<<20)
	}
}

func TestEnumerateRejectsFormWithoutFileField(t *testing.T) {
	p := impl.NewUploadBypassProber(defaultConfig())
	textOnly := impl.UploadForm{Action: "/upload", Method: "POST",
		Fields: []impl.Field{{Name: "title", Type: "text"}}}
	if _, err := p.Enumerate(textOnly); !errors.Is(err, impl.ErrNoUploadPoint) {
		t.Fatalf("text only form error = %v, want %v", err, impl.ErrNoUploadPoint)
	}
	if _, err := p.Enumerate(impl.UploadForm{Method: "POST", Fields: uploadForm().Fields}); !errors.Is(err, impl.ErrBadForm) {
		t.Fatalf("form without an action error = %v, want %v", err, impl.ErrBadForm)
	}
}

func TestVariantsCoverFiveDimensionsInFixedOrder(t *testing.T) {
	p, points := openProber(t, defaultConfig(), &memoryTarget{})
	vs, err := p.Variants(points[0])
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	want := []string{impl.DimDoubleExtension, impl.DimCaseExtension, impl.DimTrailingDot,
		impl.DimMimeMismatch, impl.DimMagicPrefix}
	if len(vs) != len(want) {
		t.Fatalf("variant count = %d, want %d", len(vs), len(want))
	}
	seen := map[string]bool{}
	for i, v := range vs {
		if v.Dimension != want[i] {
			t.Fatalf("variant %d dimension = %q, want %q", i, v.Dimension, want[i])
		}
		if v.Point != "avatar" {
			t.Fatalf("variant %d point = %q, want avatar", i, v.Point)
		}
		if v.ID == "" || seen[v.ID] {
			t.Fatalf("variant %d id = %q is empty or repeated", i, v.ID)
		}
		seen[v.ID] = true
		if v.Filename == "" {
			t.Fatalf("variant %d has an empty filename", i)
		}
	}
	again, err := p.Variants(points[0])
	if err != nil {
		t.Fatalf("second variants call must succeed, got %v", err)
	}
	for i := range again {
		if again[i].ID != vs[i].ID || again[i].Filename != vs[i].Filename || again[i].Body != vs[i].Body {
			t.Fatalf("variant %d is not stable across calls", i)
		}
	}
}

func TestVariantPayloadsCarryDistinctSentinels(t *testing.T) {
	p, points := openProber(t, defaultConfig(), &memoryTarget{})
	vs, err := p.Variants(points[0])
	if err != nil {
		t.Fatalf("variants must succeed, got %v", err)
	}
	sentinels := map[string]bool{}
	for i, v := range vs {
		if v.Sentinel == "" {
			t.Fatalf("variant %d has an empty sentinel", i)
		}
		if sentinels[v.Sentinel] {
			t.Fatalf("variant %d reuses the sentinel %q", i, v.Sentinel)
		}
		sentinels[v.Sentinel] = true
		if !strings.Contains(v.Body, v.Sentinel) {
			t.Fatalf("variant %d body does not carry its sentinel", i)
		}
	}
}

func TestVariantsRejectForeignPoint(t *testing.T) {
	p, _ := openProber(t, defaultConfig(), &memoryTarget{})
	if _, err := p.Variants(impl.UploadPoint{Field: "missing"}); !errors.Is(err, impl.ErrUnknownPoint) {
		t.Fatalf("variants for a foreign point error = %v, want %v", err, impl.ErrUnknownPoint)
	}
}

func TestSubmitRegistersUploadForCleanup(t *testing.T) {
	p, points := openProber(t, defaultConfig(), &memoryTarget{})
	vs, err := p.Variants(points[0])
	if err != nil || len(vs) < 2 {
		t.Fatalf("variants = %d err = %v, want at least two", len(vs), err)
	}
	for i := 0; i < 2; i++ {
		res, err := p.Submit(points[0], vs[i])
		if err != nil {
			t.Fatalf("submit %d must succeed, got %v", i, err)
		}
		if res.Path == "" || !res.Accepted {
			t.Fatalf("submit %d result = %+v, want an accepted upload with a stored path", i, res)
		}
	}
	if got := len(p.Uploaded()); got != 2 {
		t.Fatalf("registered uploads = %d, want 2", got)
	}
	cleaned, err := p.Cleanup()
	if err != nil {
		t.Fatalf("cleanup must succeed, got %v", err)
	}
	if cleaned != 2 {
		t.Fatalf("cleanup count = %d, want 2", cleaned)
	}
	for _, res := range p.Uploaded() {
		if !res.Cleaned {
			t.Fatalf("upload %q is still marked as live", res.VariantID)
		}
	}
	again, err := p.Cleanup()
	if err != nil {
		t.Fatalf("second cleanup must succeed, got %v", err)
	}
	if again != 0 {
		t.Fatalf("second cleanup count = %d, want 0", again)
	}
}

func TestSubmitRejectsDuplicateVariant(t *testing.T) {
	tg := &memoryTarget{}
	p, points := openProber(t, defaultConfig(), tg)
	vs, err := p.Variants(points[0])
	if err != nil || len(vs) == 0 {
		t.Fatalf("variants = %d err = %v, want at least one", len(vs), err)
	}
	if _, err := p.Submit(points[0], vs[0]); err != nil {
		t.Fatalf("first submit must succeed, got %v", err)
	}
	if _, err := p.Submit(points[0], vs[0]); !errors.Is(err, impl.ErrVariantSubmitted) {
		t.Fatalf("second submit error = %v, want %v", err, impl.ErrVariantSubmitted)
	}
	if got := tg.count(); got != 1 {
		t.Fatalf("target uploads = %d, want 1", got)
	}
}

func TestSubmitRejectsVariantFromOtherPoint(t *testing.T) {
	tg := &memoryTarget{}
	p, points := openProber(t, defaultConfig(), tg)
	other, err := p.Variants(points[1])
	if err != nil || len(other) == 0 {
		t.Fatalf("variants = %d err = %v, want at least one", len(other), err)
	}
	if _, err := p.Submit(points[0], other[0]); !errors.Is(err, impl.ErrVariantMismatch) {
		t.Fatalf("cross point submit error = %v, want %v", err, impl.ErrVariantMismatch)
	}
	if got := tg.count(); got != 0 {
		t.Fatalf("target uploads = %d, want 0", got)
	}
}

func TestSubmitEnforcesStorageQuota(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxUploads = 1
	p, points := openProber(t, cfg, &memoryTarget{})
	vs, err := p.Variants(points[0])
	if err != nil || len(vs) < 2 {
		t.Fatalf("variants = %d err = %v, want at least two", len(vs), err)
	}
	if _, err := p.Submit(points[0], vs[0]); err != nil {
		t.Fatalf("first submit must succeed, got %v", err)
	}
	if _, err := p.Submit(points[0], vs[1]); !errors.Is(err, impl.ErrStorageQuota) {
		t.Fatalf("over quota submit error = %v, want %v", err, impl.ErrStorageQuota)
	}
	if got := len(p.Uploaded()); got != 1 {
		t.Fatalf("registered uploads = %d, want 1", got)
	}
}

func TestSubmitEnforcesRequestBudget(t *testing.T) {
	cfg := defaultConfig()
	cfg.MaxRequests = 1
	p, points := openProber(t, cfg, &memoryTarget{})
	vs, err := p.Variants(points[0])
	if err != nil || len(vs) < 2 {
		t.Fatalf("variants = %d err = %v, want at least two", len(vs), err)
	}
	if _, err := p.Submit(points[0], vs[0]); err != nil {
		t.Fatalf("first submit must succeed, got %v", err)
	}
	if _, err := p.Submit(points[0], vs[1]); !errors.Is(err, impl.ErrRequestBudget) {
		t.Fatalf("over budget submit error = %v, want %v", err, impl.ErrRequestBudget)
	}
	budget, err := p.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 1 || budget.Remaining != 0 {
		t.Fatalf("budget = %+v, want used 1 remaining 0", budget)
	}
}

func TestVerifyRejectsUnknownUpload(t *testing.T) {
	p, _ := openProber(t, defaultConfig(), &memoryTarget{})
	if _, err := p.Verify(impl.UploadResult{Point: "avatar", VariantID: "ghost"}); !errors.Is(err, impl.ErrUnknownUpload) {
		t.Fatalf("verify of an unknown upload error = %v, want %v", err, impl.ErrUnknownUpload)
	}
}

func TestVerifyReportsScriptHandler(t *testing.T) {
	tg := &memoryTarget{handler: "php-fpm"}
	p, points := openProber(t, defaultConfig(), tg)
	vs, err := p.Variants(points[0])
	if err != nil || len(vs) == 0 {
		t.Fatalf("variants = %d err = %v, want at least one", len(vs), err)
	}
	res, err := p.Submit(points[0], vs[0])
	if err != nil {
		t.Fatalf("submit must succeed, got %v", err)
	}
	proof, err := p.Verify(res)
	if err != nil {
		t.Fatalf("verify must succeed, got %v", err)
	}
	if !proof.Executable {
		t.Fatalf("proof must be executable when the script handler serves the sentinel: %+v", proof)
	}
	if proof.Handler != "php-fpm" {
		t.Fatalf("proof handler = %q, want php-fpm", proof.Handler)
	}
	if proof.Path != res.Path {
		t.Fatalf("proof path = %q, want the stored path %q", proof.Path, res.Path)
	}
	if proof.VariantID != vs[0].ID {
		t.Fatalf("proof variant = %q, want %q", proof.VariantID, vs[0].ID)
	}
	if proof.Reason == "" {
		t.Fatalf("proof reason must not be empty")
	}
}

func TestJudgeMarksExecutableProofAsHit(t *testing.T) {
	p, points := openProber(t, defaultConfig(), &memoryTarget{})
	vs, err := p.Variants(points[0])
	if err != nil || len(vs) == 0 {
		t.Fatalf("variants = %d err = %v, want at least one", len(vs), err)
	}
	verdict, err := p.Judge(impl.ExecProof{VariantID: vs[0].ID, Path: "/uploads/x.png",
		Handler: "php-fpm", Executable: true, Reason: "script handler served the sentinel"})
	if err != nil {
		t.Fatalf("judge must succeed, got %v", err)
	}
	if !verdict.Hit {
		t.Fatalf("verdict must be a hit for an executable proof: %+v", verdict)
	}
	if verdict.VariantID != vs[0].ID {
		t.Fatalf("verdict variant = %q, want %q", verdict.VariantID, vs[0].ID)
	}
	if verdict.Reason == "" {
		t.Fatalf("verdict reason must not be empty")
	}
	if _, err := p.Judge(impl.ExecProof{}); !errors.Is(err, impl.ErrUnknownUpload) {
		t.Fatalf("judge without a variant error = %v, want %v", err, impl.ErrUnknownUpload)
	}
}

func TestConcurrentSubmitUploadsOnce(t *testing.T) {
	tg := &memoryTarget{}
	p, points := openProber(t, defaultConfig(), tg)
	vs, err := p.Variants(points[0])
	if err != nil || len(vs) == 0 {
		t.Fatalf("variants = %d err = %v, want at least one", len(vs), err)
	}
	const workers = 8
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, submitErr := p.Submit(points[0], vs[0])
			errs[idx] = submitErr
		}(i)
	}
	wg.Wait()
	winners := 0
	for i := 0; i < workers; i++ {
		if errs[i] == nil {
			winners++
			continue
		}
		if !errors.Is(errs[i], impl.ErrVariantSubmitted) {
			t.Fatalf("losing submit error = %v, want %v", errs[i], impl.ErrVariantSubmitted)
		}
	}
	if winners != 1 {
		t.Fatalf("successful submits = %d, want exactly 1", winners)
	}
	if got := tg.count(); got != 1 {
		t.Fatalf("target uploads = %d, want 1", got)
	}
	if got := len(p.Uploaded()); got != 1 {
		t.Fatalf("registered uploads = %d, want 1", got)
	}
}

func TestSnapshotRestoreKeepsRegistry(t *testing.T) {
	cfg := defaultConfig()
	p, points := openProber(t, cfg, &memoryTarget{handler: "cgi"})
	vs, err := p.Variants(points[0])
	if err != nil || len(vs) < 2 {
		t.Fatalf("variants = %d err = %v, want at least two", len(vs), err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.Submit(points[0], vs[i]); err != nil {
			t.Fatalf("submit %d must succeed, got %v", i, err)
		}
	}
	snap, err := p.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot len = %d err = %v, want a payload", len(snap), err)
	}
	restored := impl.NewUploadBypassProber(cfg)
	if err := restored.SetTarget(&memoryTarget{handler: "cgi"}); err != nil {
		t.Fatalf("set target must succeed, got %v", err)
	}
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if got := len(restored.Uploaded()); got != 2 {
		t.Fatalf("restored registry = %d, want 2", got)
	}
	budget, err := restored.Budget()
	if err != nil {
		t.Fatalf("budget must succeed, got %v", err)
	}
	if budget.Used != 2 {
		t.Fatalf("restored budget used = %d, want 2", budget.Used)
	}
	if _, err := restored.Submit(points[0], vs[0]); !errors.Is(err, impl.ErrVariantSubmitted) {
		t.Fatalf("replayed submit after restore error = %v, want %v", err, impl.ErrVariantSubmitted)
	}
	if err := restored.Restore([]byte("<<<garbage>>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want %v", err, impl.ErrBadSnapshot)
	}
	if got := len(restored.Uploaded()); got != 2 {
		t.Fatalf("registry after corrupt restore = %d, want 2", got)
	}
}
