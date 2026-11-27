package task

import (
	"errors"
	"math"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-c-15"
)

func frameAt(id string, sharp, subject, color float64, hash uint64) impl.Frame {
	return impl.Frame{
		ID:           id,
		Width:        1920,
		Height:       1080,
		Sharpness:    sharp,
		SubjectRatio: subject,
		Colorfulness: color,
		Hash:         hash,
		Dark:         0.1,
		Bright:       0.1,
	}
}

type entry struct {
	frame impl.Frame
	ts    int64
}

func addAll(t *testing.T, s *impl.CoverFrameSelector, entries []entry) {
	t.Helper()
	for _, e := range entries {
		if err := s.AddCandidate(e.frame, e.ts); err != nil {
			t.Fatalf("adding candidate at %d must succeed, got %v", e.ts, err)
		}
	}
}

func threeCandidates() []entry {
	return []entry{
		{frameAt("a", 0.9, 0.8, 0.5, 0x00000000000000FF), 0},
		{frameAt("b", 0.5, 0.5, 0.5, 0xFFFFFFFFFFFFFF00), 10},
		{frameAt("c", 0.7, 0.9, 0.3, 0x00FF00000000FF00), 20},
	}
}

func TestDefaults(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	if got := s.Quota(); got != impl.DefaultQuota {
		t.Fatalf("default quota = %d, want %d", got, impl.DefaultQuota)
	}
	w := s.Weights()
	if math.Abs(w.Sharpness-impl.DefaultWeightSharp) > 1e-9 ||
		math.Abs(w.Subject-impl.DefaultWeightSubject) > 1e-9 ||
		math.Abs(w.Color-impl.DefaultWeightColor) > 1e-9 {
		t.Fatalf("default weights = %+v, want the documented split", w)
	}
	if st := s.Stats(); st.Candidates != 0 || st.Selected != 0 {
		t.Fatalf("fresh stats = %+v, want all counters zero", st)
	}
	if _, err := s.Select(); !errors.Is(err, impl.ErrNoCandidates) {
		t.Fatalf("selecting from an empty pool must fail with ErrNoCandidates, got %v", err)
	}
}

func TestAddCandidateValidation(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	if err := s.AddCandidate(frameAt("", 0.5, 0.5, 0.5, 1), 0); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("an empty frame id must fail with ErrBadFrame, got %v", err)
	}
	noSize := frameAt("x", 0.5, 0.5, 0.5, 1)
	noSize.Width = 0
	if err := s.AddCandidate(noSize, 0); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("a zero width must fail with ErrBadFrame, got %v", err)
	}
	tooSharp := frameAt("y", 1.5, 0.5, 0.5, 1)
	if err := s.AddCandidate(tooSharp, 0); !errors.Is(err, impl.ErrBadFrame) {
		t.Fatalf("a sharpness above one must fail with ErrBadFrame, got %v", err)
	}
	if err := s.AddCandidate(frameAt("z", 0.5, 0.5, 0.5, 1), -1); !errors.Is(err, impl.ErrBadTS) {
		t.Fatalf("a negative timestamp must fail with ErrBadTS, got %v", err)
	}
	if err := s.AddCandidate(frameAt("ok", 0.5, 0.5, 0.5, 1), 30); err != nil {
		t.Fatalf("a valid candidate must be accepted, got %v", err)
	}
	if err := s.AddCandidate(frameAt("dup", 0.6, 0.6, 0.6, 2), 30); !errors.Is(err, impl.ErrDuplicateTS) {
		t.Fatalf("a duplicate timestamp must fail with ErrDuplicateTS, got %v", err)
	}
	if st := s.Stats(); st.Candidates != 1 {
		t.Fatalf("candidate count = %d, want only the accepted frame", st.Candidates)
	}
}

func TestAddCandidateEnforcesPoolLimit(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{MaxCandidates: 2})
	if err := s.AddCandidate(frameAt("a", 0.5, 0.5, 0.5, 1), 0); err != nil {
		t.Fatalf("first candidate: %v", err)
	}
	if err := s.AddCandidate(frameAt("b", 0.5, 0.5, 0.5, 2), 10); err != nil {
		t.Fatalf("second candidate: %v", err)
	}
	if err := s.AddCandidate(frameAt("c", 0.5, 0.5, 0.5, 3), 20); !errors.Is(err, impl.ErrCandidateLimit) {
		t.Fatalf("the third candidate must hit ErrCandidateLimit, got %v", err)
	}
	if st := s.Stats(); st.Candidates != 2 {
		t.Fatalf("candidate count = %d, want the cap 2", st.Candidates)
	}
}

func TestSetWeightsValidation(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	if err := s.SetWeights(impl.ScoreWeights{Sharpness: -0.1, Subject: 0.5, Color: 0.5}); !errors.Is(err, impl.ErrBadWeights) {
		t.Fatalf("a negative weight must fail with ErrBadWeights, got %v", err)
	}
	if err := s.SetWeights(impl.ScoreWeights{}); !errors.Is(err, impl.ErrBadWeights) {
		t.Fatalf("all zero weights must fail with ErrBadWeights, got %v", err)
	}
	w := s.Weights()
	if math.Abs(w.Sharpness-impl.DefaultWeightSharp) > 1e-9 {
		t.Fatalf("rejected weights changed the live value to %+v", w)
	}
	if err := s.SetWeights(impl.ScoreWeights{Sharpness: 1}); err != nil {
		t.Fatalf("a single non zero weight must be accepted, got %v", err)
	}
	if got := s.Weights(); math.Abs(got.Sharpness-1) > 1e-9 || got.Subject != 0 || got.Color != 0 {
		t.Fatalf("weights = %+v, want sharpness only", got)
	}
}

func TestSetQuotaValidation(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	if err := s.SetQuota(0); !errors.Is(err, impl.ErrBadQuota) {
		t.Fatalf("a zero quota must fail with ErrBadQuota, got %v", err)
	}
	if err := s.SetQuota(impl.MaxQuota + 1); !errors.Is(err, impl.ErrBadQuota) {
		t.Fatalf("a quota above the cap must fail with ErrBadQuota, got %v", err)
	}
	if got := s.Quota(); got != impl.DefaultQuota {
		t.Fatalf("quota after rejected changes = %d, want %d", got, impl.DefaultQuota)
	}
	if err := s.SetQuota(2); err != nil {
		t.Fatalf("a valid quota must be accepted, got %v", err)
	}
	if got := s.Quota(); got != 2 {
		t.Fatalf("quota = %d, want 2", got)
	}
}

func TestSetSceneCutsValidation(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	if err := s.SetSceneCuts([]int64{20, 10}); !errors.Is(err, impl.ErrBadCuts) {
		t.Fatalf("decreasing cuts must fail with ErrBadCuts, got %v", err)
	}
	if err := s.SetSceneCuts([]int64{-5}); !errors.Is(err, impl.ErrBadCuts) {
		t.Fatalf("a negative cut must fail with ErrBadCuts, got %v", err)
	}
	if err := s.SetSceneCuts([]int64{10, 10}); !errors.Is(err, impl.ErrBadCuts) {
		t.Fatalf("duplicate cuts must fail with ErrBadCuts, got %v", err)
	}
	if err := s.SetSceneCuts([]int64{10, 20, 30}); err != nil {
		t.Fatalf("strictly increasing cuts must be accepted, got %v", err)
	}
}

func TestSelectOrdersByScore(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	addAll(t, s, threeCandidates())
	got, err := s.Select()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("selected = %d, want 3 under a quota of 4", len(got))
	}
	wantIDs := []string{"a", "c", "b"}
	wantScores := []float64{0.79, 0.68, 0.50}
	for i := range wantIDs {
		if got[i].FrameID != wantIDs[i] {
			t.Fatalf("position %d = %q, want %q (order %v)", i, got[i].FrameID, wantIDs[i], wantIDs)
		}
		if math.Abs(got[i].Score-wantScores[i]) > 1e-9 {
			t.Fatalf("position %d score = %v, want %v", i, got[i].Score, wantScores[i])
		}
	}
	if st := s.Stats(); st.Selected != 3 || st.HashCalls != 3 {
		t.Fatalf("stats selected=%d hash calls=%d, want 3 and 3", st.Selected, st.HashCalls)
	}
}

func TestSelectTruncatesToQuota(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	if err := s.SetQuota(2); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	addAll(t, s, threeCandidates())
	got, err := s.Select()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("selected = %d, want the quota 2", len(got))
	}
	if got[0].FrameID != "a" || got[1].FrameID != "c" {
		t.Fatalf("selected order = %q,%q, want the two best frames", got[0].FrameID, got[1].FrameID)
	}
}

func TestSelectTieBreaksByTimestamp(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	if err := s.AddCandidate(frameAt("late", 0.6, 0.6, 0.6, 0x00000000000000FF), 100); err != nil {
		t.Fatalf("add late: %v", err)
	}
	if err := s.AddCandidate(frameAt("early", 0.6, 0.6, 0.6, 0xFFFFFFFFFFFFFF00), 50); err != nil {
		t.Fatalf("add early: %v", err)
	}
	got, err := s.Select()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("selected = %d, want 2", len(got))
	}
	if got[0].FrameID != "early" {
		t.Fatalf("first selected = %q, want the earlier timestamp on an equal score", got[0].FrameID)
	}
	if got[0].Score != got[1].Score {
		t.Fatalf("tie scores differ: %v and %v", got[0].Score, got[1].Score)
	}
}

func TestSelectDedupesSimilarFrames(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	if err := s.AddCandidate(frameAt("soft", 0.4, 0.5, 0.5, 0x00000000000000FF), 0); err != nil {
		t.Fatalf("add soft: %v", err)
	}
	if err := s.AddCandidate(frameAt("sharp", 0.9, 0.5, 0.5, 0x00000000000000FE), 10); err != nil {
		t.Fatalf("add sharp: %v", err)
	}
	if err := s.AddCandidate(frameAt("other", 0.5, 0.5, 0.5, 0xFFFFFFFFFFFFFF00), 20); err != nil {
		t.Fatalf("add other: %v", err)
	}
	got, err := s.Select()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("selected = %d, want 2 after the near duplicate is removed", len(got))
	}
	if got[0].FrameID != "sharp" || got[1].FrameID != "other" {
		t.Fatalf("selected = %q,%q, want the sharper duplicate plus the distinct frame", got[0].FrameID, got[1].FrameID)
	}
	if st := s.Stats(); st.Deduped != 1 {
		t.Fatalf("deduped count = %d, want 1", st.Deduped)
	}
}

func TestRejectRemovesFrame(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	addAll(t, s, threeCandidates())
	if err := s.Reject(20, "  "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("a blank reason must fail with ErrBadReason, got %v", err)
	}
	if err := s.Reject(999, "watermark"); !errors.Is(err, impl.ErrUnknownCandidate) {
		t.Fatalf("rejecting an unknown timestamp must fail with ErrUnknownCandidate, got %v", err)
	}
	if err := s.Reject(0, "host holding a card"); err != nil {
		t.Fatalf("rejecting a known candidate must succeed, got %v", err)
	}
	got, err := s.Select()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("selected = %d, want 2 after one rejection", len(got))
	}
	for _, cand := range got {
		if cand.FrameID == "a" {
			t.Fatalf("rejected frame a must not be selected, got %+v", got)
		}
	}
	if st := s.Stats(); st.Rejected != 1 {
		t.Fatalf("rejected count = %d, want 1", st.Rejected)
	}
}

func TestExplainBreakdown(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	addAll(t, s, threeCandidates())
	b := s.Explain(0)
	if math.Abs(b.Total-0.79) > 1e-9 {
		t.Fatalf("explained total = %v, want 0.79", b.Total)
	}
	if math.Abs(b.Sharpness-0.9) > 1e-9 || math.Abs(b.Subject-0.8) > 1e-9 || math.Abs(b.Color-0.5) > 1e-9 {
		t.Fatalf("explained components = %+v, want the raw frame features", b)
	}
	if b.Reason != "scored" {
		t.Fatalf("explained reason = %q, want scored", b.Reason)
	}
	missing := s.Explain(777)
	if missing.Reason != "unknown" {
		t.Fatalf("unknown timestamp reason = %q, want unknown", missing.Reason)
	}
}

func TestSnapshotRestoreKeepsSelection(t *testing.T) {
	limits := impl.Limits{MaxCandidates: 32, MaxHashes: 32}
	s := impl.NewCoverFrameSelector(limits)
	addAll(t, s, threeCandidates())
	if err := s.SetQuota(2); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if err := s.SetSceneCuts([]int64{15}); err != nil {
		t.Fatalf("set cuts: %v", err)
	}
	if err := s.Reject(20, "logo overlay"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	want, err := s.Select()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	r := impl.NewCoverFrameSelector(limits)
	if err := r.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := r.Quota(); got != 2 {
		t.Fatalf("restored quota = %d, want 2", got)
	}
	if st := r.Stats(); st.Candidates != 3 || st.Rejected != 1 {
		t.Fatalf("restored stats = %+v, want 3 candidates and 1 rejection", st)
	}
	got, err := r.Select()
	if err != nil {
		t.Fatalf("select after restore: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("selected after restore = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d after restore = %+v, want %+v", i, got[i], want[i])
		}
	}
	if err := r.Restore([]byte("broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if st := r.Stats(); st.Candidates != 3 {
		t.Fatalf("a rejected restore must not touch the live state, candidates=%d", st.Candidates)
	}
}

func TestSelectEnforcesHashBudget(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{MaxHashes: 2})
	addAll(t, s, threeCandidates())
	if _, err := s.Select(); !errors.Is(err, impl.ErrHashBudget) {
		t.Fatalf("three candidates against a hash budget of two must fail with ErrHashBudget, got %v", err)
	}
	if st := s.Stats(); st.Selected != 0 {
		t.Fatalf("a rejected selection must not publish results, selected=%d", st.Selected)
	}
}

func TestConcurrentAddCandidates(t *testing.T) {
	s := impl.NewCoverFrameSelector(impl.Limits{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			frame := frameAt("f", 0.5, 0.5, 0.5, uint64(i+1)<<32)
			if err := s.AddCandidate(frame, int64(i*10)); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if failed != 0 {
		t.Fatalf("%d concurrent adds failed, want 0", failed)
	}
	if st := s.Stats(); st.Candidates != 8 {
		t.Fatalf("candidate count = %d, want 8", st.Candidates)
	}
	got, err := s.Select()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if len(got) != impl.DefaultQuota {
		t.Fatalf("selected = %d, want the default quota %d", len(got), impl.DefaultQuota)
	}
}
