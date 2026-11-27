package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-game-c-04"
)

func ledgerLimits() impl.DiarLimits {
	return impl.DiarLimits{MaxSpeakers: 4, MinConfidence: 0.6}
}

func voiceA() []float64 { return []float64{1, 0, 0} }
func voiceB() []float64 { return []float64{0, 1, 0} }

func seg(id, fileID string, start, end int64, conf float64, fp []float64) impl.Segment {
	return impl.Segment{ID: id, FileID: fileID, StartMS: start, EndMS: end, Confidence: conf, Fingerprint: fp}
}

func TestNewLedgerAppliesDefaults(t *testing.T) {
	l := impl.NewDiarizationLedger(impl.DiarLimits{})
	limits := l.Limits()
	if limits.MaxSpeakers != impl.DefaultMaxSpeakers {
		t.Fatalf("default speaker limit = %d, want %d", limits.MaxSpeakers, impl.DefaultMaxSpeakers)
	}
	if limits.GapMS != impl.DefaultGapMS || limits.MinSegmentMS != impl.DefaultMinSegmentMS {
		t.Fatalf("default gap=%d min segment=%d, want %d and %d", limits.GapMS, limits.MinSegmentMS, impl.DefaultGapMS, impl.DefaultMinSegmentMS)
	}
	if limits.MatchDistance != impl.DefaultMatchDistance || limits.GrayBand != impl.DefaultGrayBand {
		t.Fatalf("default distance=%v gray band=%v, want %v and %v", limits.MatchDistance, limits.GrayBand, impl.DefaultMatchDistance, impl.DefaultGrayBand)
	}
	if l.Threshold() != impl.DefaultMinConfidence {
		t.Fatalf("default threshold = %v, want %v", l.Threshold(), impl.DefaultMinConfidence)
	}
	if l.Slots() != 0 || l.Pending() != 0 {
		t.Fatalf("fresh ledger slots=%d pending=%d, want 0", l.Slots(), l.Pending())
	}
}

func TestAssignCreatesAndReusesSlots(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	first, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA()))
	if err != nil {
		t.Fatalf("first assignment must succeed, got %v", err)
	}
	if first.Slot != 1 || !first.Created {
		t.Fatalf("first assignment slot=%d created=%v, want 1 and true", first.Slot, first.Created)
	}
	second, err := l.Assign(seg("s2", "f1", 1200, 2000, 0.9, voiceA()))
	if err != nil {
		t.Fatalf("second assignment must succeed, got %v", err)
	}
	if second.Slot != 1 || second.Created {
		t.Fatalf("same voice assignment slot=%d created=%v, want 1 and false", second.Slot, second.Created)
	}
	third, err := l.Assign(seg("s3", "f1", 2200, 3000, 0.9, voiceB()))
	if err != nil {
		t.Fatalf("third assignment must succeed, got %v", err)
	}
	if third.Slot != 2 || !third.Created {
		t.Fatalf("different voice assignment slot=%d created=%v, want 2 and true", third.Slot, third.Created)
	}
	if l.Slots() != 2 {
		t.Fatalf("slot count = %d, want 2", l.Slots())
	}
	if got := l.Segments("f1"); len(got) != 3 {
		t.Fatalf("stored segments = %d, want 3", len(got))
	}
}

func TestAssignLowConfidenceGoesPending(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	a, err := l.Assign(seg("s1", "f1", 0, 900, 0.2, voiceA()))
	if err != nil {
		t.Fatalf("a low confidence segment must be accepted into the review queue, got %v", err)
	}
	if !a.Pending || a.Slot != 0 || a.Created {
		t.Fatalf("pending assignment slot=%d created=%v pending=%v, want 0/false/true", a.Slot, a.Created, a.Pending)
	}
	if l.Pending() != 1 {
		t.Fatalf("review queue size = %d, want 1", l.Pending())
	}
	if l.Slots() != 0 {
		t.Fatalf("slot count after a pending segment = %d, want 0", l.Slots())
	}
}

func TestAssignRejectsBadSegment(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	cases := []impl.Segment{
		{ID: "", FileID: "f1", StartMS: 0, EndMS: 100, Confidence: 0.9, Fingerprint: voiceA()},
		{ID: "s1", FileID: "", StartMS: 0, EndMS: 100, Confidence: 0.9, Fingerprint: voiceA()},
		{ID: "s1", FileID: "f1", StartMS: 500, EndMS: 500, Confidence: 0.9, Fingerprint: voiceA()},
		{ID: "s1", FileID: "f1", StartMS: 0, EndMS: 100, Confidence: 1.5, Fingerprint: voiceA()},
		{ID: "s1", FileID: "f1", StartMS: -1, EndMS: 100, Confidence: 0.9, Fingerprint: voiceA()},
	}
	for i, s := range cases {
		if _, err := l.Assign(s); !errors.Is(err, impl.ErrBadSegment) {
			t.Fatalf("case %d: err = %v, want ErrBadSegment", i, err)
		}
	}
	if l.Slots() != 0 || len(l.Segments("f1")) != 0 {
		t.Fatalf("state changed by rejected segments")
	}
}

func TestAssignRejectsFingerprintDimensionMismatch(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	if _, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA())); err != nil {
		t.Fatalf("first assignment must succeed, got %v", err)
	}
	short := impl.Segment{ID: "s2", FileID: "f1", StartMS: 1200, EndMS: 2000, Confidence: 0.9, Fingerprint: []float64{1, 0}}
	if _, err := l.Assign(short); !errors.Is(err, impl.ErrBadFingerprint) {
		t.Fatalf("mismatched voiceprint err = %v, want ErrBadFingerprint", err)
	}
	if len(l.Segments("f1")) != 1 {
		t.Fatalf("stored segments after a rejected voiceprint = %d, want 1", len(l.Segments("f1")))
	}
}

func TestAssignEnforcesSlotLimit(t *testing.T) {
	l := impl.NewDiarizationLedger(impl.DiarLimits{MaxSpeakers: 1, MinConfidence: 0.6})
	if _, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA())); err != nil {
		t.Fatalf("first assignment must succeed, got %v", err)
	}
	if _, err := l.Assign(seg("s2", "f1", 1200, 2000, 0.9, voiceB())); !errors.Is(err, impl.ErrSlotLimit) {
		t.Fatalf("second speaker err = %v, want ErrSlotLimit", err)
	}
	if l.Slots() != 1 {
		t.Fatalf("slot count after the limit error = %d, want 1", l.Slots())
	}
}

func TestAssignIsIdempotentPerSegmentID(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	first, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA()))
	if err != nil {
		t.Fatalf("first assignment must succeed, got %v", err)
	}
	repeat, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA()))
	if err != nil {
		t.Fatalf("repeated assignment must succeed, got %v", err)
	}
	if repeat.Slot != first.Slot || repeat.Created != first.Created {
		t.Fatalf("repeated assignment slot=%d created=%v, want %d and %v", repeat.Slot, repeat.Created, first.Slot, first.Created)
	}
	if l.Slots() != 1 || len(l.Segments("f1")) != 1 {
		t.Fatalf("state after a repeated assignment slots=%d segments=%d, want 1 and 1", l.Slots(), len(l.Segments("f1")))
	}
}

func TestMergeAdjacentSameSpeaker(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	if _, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA())); err != nil {
		t.Fatalf("first assignment must succeed, got %v", err)
	}
	if _, err := l.Assign(seg("s2", "f1", 1200, 2000, 0.9, voiceA())); err != nil {
		t.Fatalf("second assignment must succeed, got %v", err)
	}
	if err := l.Merge([]string{"s1", "s2"}); err != nil {
		t.Fatalf("merging an adjacent pair must succeed, got %v", err)
	}
	stored := l.Segments("f1")
	if len(stored) != 1 {
		t.Fatalf("stored segments after merge = %d, want 1", len(stored))
	}
	if stored[0].ID != "s1" || stored[0].StartMS != 0 || stored[0].EndMS != 2000 {
		t.Fatalf("merged segment = %+v, want s1 spanning 0 to 2000", stored[0])
	}
}

func TestMergeRejectsDifferentSpeaker(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	if _, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA())); err != nil {
		t.Fatalf("first assignment must succeed, got %v", err)
	}
	if _, err := l.Assign(seg("s2", "f1", 1200, 2000, 0.9, voiceB())); err != nil {
		t.Fatalf("second assignment must succeed, got %v", err)
	}
	if err := l.Merge([]string{"s1", "s2"}); !errors.Is(err, impl.ErrMergeMismatch) {
		t.Fatalf("merging different speakers err = %v, want ErrMergeMismatch", err)
	}
	if len(l.Segments("f1")) != 2 {
		t.Fatalf("stored segments after a rejected merge = %d, want 2", len(l.Segments("f1")))
	}
}

func TestMergeRejectsWideGap(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	if _, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA())); err != nil {
		t.Fatalf("first assignment must succeed, got %v", err)
	}
	if _, err := l.Assign(seg("s2", "f1", 1400, 2000, 0.9, voiceA())); err != nil {
		t.Fatalf("second assignment must succeed, got %v", err)
	}
	if err := l.Merge([]string{"s1", "s2"}); !errors.Is(err, impl.ErrGapTooLarge) {
		t.Fatalf("merging across a wide gap err = %v, want ErrGapTooLarge", err)
	}
	if len(l.Segments("f1")) != 2 {
		t.Fatalf("stored segments after a rejected merge = %d, want 2", len(l.Segments("f1")))
	}
}

func TestSplitCreatesTwoSegments(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	if _, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA())); err != nil {
		t.Fatalf("assignment must succeed, got %v", err)
	}
	if err := l.Split("s1", 500*time.Millisecond); err != nil {
		t.Fatalf("split inside the segment must succeed, got %v", err)
	}
	stored := l.Segments("f1")
	if len(stored) != 2 {
		t.Fatalf("stored segments after split = %d, want 2", len(stored))
	}
	if stored[0].StartMS != 0 || stored[0].EndMS != 500 {
		t.Fatalf("left half = [%d,%d], want [0,500]", stored[0].StartMS, stored[0].EndMS)
	}
	if stored[1].StartMS != 500 || stored[1].EndMS != 1000 {
		t.Fatalf("right half = [%d,%d], want [500,1000]", stored[1].StartMS, stored[1].EndMS)
	}
	if stored[1].ID == stored[0].ID {
		t.Fatalf("both halves kept the id %q", stored[0].ID)
	}
}

func TestSplitRejectsOutOfRange(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	if _, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA())); err != nil {
		t.Fatalf("assignment must succeed, got %v", err)
	}
	for _, at := range []time.Duration{0, 1000 * time.Millisecond, -5 * time.Millisecond} {
		if err := l.Split("s1", at); !errors.Is(err, impl.ErrBadSplit) {
			t.Fatalf("split at %v err = %v, want ErrBadSplit", at, err)
		}
	}
	if err := l.Split("ghost", 500*time.Millisecond); !errors.Is(err, impl.ErrUnknownSegment) {
		t.Fatalf("split of an unknown id err = %v, want ErrUnknownSegment", err)
	}
	if len(l.Segments("f1")) != 1 {
		t.Fatalf("stored segments after rejected splits = %d, want 1", len(l.Segments("f1")))
	}
}

func TestSetThresholdValidation(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	if err := l.SetThreshold(0.5); err != nil {
		t.Fatalf("setting a valid threshold must succeed, got %v", err)
	}
	if l.Threshold() != 0.5 {
		t.Fatalf("threshold after set = %v, want 0.5", l.Threshold())
	}
	if err := l.SetThreshold(-0.1); !errors.Is(err, impl.ErrBadThreshold) {
		t.Fatalf("negative threshold err = %v, want ErrBadThreshold", err)
	}
	if err := l.SetThreshold(1.5); !errors.Is(err, impl.ErrBadThreshold) {
		t.Fatalf("threshold past one err = %v, want ErrBadThreshold", err)
	}
	if l.Threshold() != 0.5 {
		t.Fatalf("threshold changed by a rejected value = %v, want 0.5", l.Threshold())
	}
}

func TestFinalizeOrdersEntriesAndIsIdempotent(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	for _, s := range []impl.Segment{
		seg("s3", "f1", 3000, 4000, 0.9, voiceA()),
		seg("s1", "f1", 0, 1000, 0.9, voiceA()),
		seg("s2", "f1", 1500, 2000, 0.9, voiceA()),
	} {
		if _, err := l.Assign(s); err != nil {
			t.Fatalf("assignment of %s must succeed, got %v", s.ID, err)
		}
	}
	tr, err := l.Finalize("f1")
	if err != nil {
		t.Fatalf("finalize must succeed, got %v", err)
	}
	if len(tr.Entries) != 3 {
		t.Fatalf("finalised entries = %d, want 3", len(tr.Entries))
	}
	want := []int64{0, 1500, 3000}
	for i, w := range want {
		if tr.Entries[i].StartMS != w {
			t.Fatalf("entry %d starts at %d, want %d", i, tr.Entries[i].StartMS, w)
		}
		if len(tr.Entries[i].Slots) != 1 || tr.Entries[i].Slots[0] != 1 {
			t.Fatalf("entry %d slots = %v, want [1]", i, tr.Entries[i].Slots)
		}
	}
	if tr.DurationMS != 4000 {
		t.Fatalf("transcript duration = %d, want 4000", tr.DurationMS)
	}
	if tr.Speakers != 1 {
		t.Fatalf("transcript speakers = %d, want 1", tr.Speakers)
	}
	again, err := l.Finalize("f1")
	if err != nil {
		t.Fatalf("second finalize must succeed, got %v", err)
	}
	if len(again.Entries) != len(tr.Entries) {
		t.Fatalf("second finalize entries = %d, want %d", len(again.Entries), len(tr.Entries))
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	l := impl.NewDiarizationLedger(ledgerLimits())
	if _, err := l.Assign(seg("s1", "f1", 0, 1000, 0.9, voiceA())); err != nil {
		t.Fatalf("first assignment must succeed, got %v", err)
	}
	if _, err := l.Assign(seg("s2", "f1", 1200, 2000, 0.9, voiceB())); err != nil {
		t.Fatalf("second assignment must succeed, got %v", err)
	}
	if err := l.SetThreshold(0.45); err != nil {
		t.Fatalf("setting the threshold must succeed, got %v", err)
	}
	data, err := l.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewDiarizationLedger(impl.DiarLimits{MaxSpeakers: 1})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if restored.Slots() != 2 || restored.Threshold() != 0.45 {
		t.Fatalf("restored slots=%d threshold=%v, want 2 and 0.45", restored.Slots(), restored.Threshold())
	}
	if len(restored.Segments("f1")) != 2 {
		t.Fatalf("restored segments = %d, want 2", len(restored.Segments("f1")))
	}
	if _, err := restored.Finalize("f1"); err != nil {
		t.Fatalf("finalize after restore must succeed, got %v", err)
	}
	if err := restored.Restore([]byte("null")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot err = %v, want ErrBadSnapshot", err)
	}
	if restored.Slots() != 2 {
		t.Fatalf("slot count changed by a rejected snapshot = %d, want 2", restored.Slots())
	}
}
