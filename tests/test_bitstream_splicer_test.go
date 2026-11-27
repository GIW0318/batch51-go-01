package task

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-c-11"
)

func hdParams() impl.ParamSet {
	return impl.ParamSet{SPSID: 1, PPSID: 1, Profile: 100, Level: 41, Chroma: 1, Width: 1920, Height: 1080}
}

func sdParams() impl.ParamSet {
	p := hdParams()
	p.SPSID = 2
	p.PPSID = 2
	p.Width = 1280
	p.Height = 720
	return p
}

func idrFrame(pts int64) impl.Frame {
	return impl.Frame{PTS: pts, DTS: pts, Kind: impl.FrameIDR, Ref: true}
}

func pFrame(pts int64, ref bool) impl.Frame {
	return impl.Frame{PTS: pts, DTS: pts, Kind: impl.FrameP, Ref: ref}
}

func videoSeg(id impl.SegID, params impl.ParamSet, frames []impl.Frame) impl.Segment {
	return impl.Segment{ID: id, Kind: impl.KindVideo, Params: params, Frames: frames}
}

func audioSeg(id impl.SegID, frames []impl.Frame) impl.Segment {
	return impl.Segment{ID: id, Kind: impl.KindAudio, Frames: frames}
}

func gopFrames() []impl.Frame {
	return []impl.Frame{
		idrFrame(0),
		pFrame(10, true),
		pFrame(20, false),
		pFrame(30, false),
		idrFrame(40),
		pFrame(50, true),
	}
}

func TestAddSegmentAssignsIDsAndCounts(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	id, err := s.AddSegment(videoSeg("", hdParams(), []impl.Frame{idrFrame(0), pFrame(10, true)}))
	if err != nil {
		t.Fatalf("adding a valid video segment must succeed, got %v", err)
	}
	if id != "seg-1" {
		t.Fatalf("first auto id = %q, want seg-1", id)
	}
	id2, err := s.AddSegment(videoSeg("", hdParams(), []impl.Frame{idrFrame(0)}))
	if err != nil {
		t.Fatalf("adding the second segment must succeed, got %v", err)
	}
	if id2 != "seg-2" {
		t.Fatalf("second auto id = %q, want seg-2", id2)
	}
	st := s.Stats()
	if st.Segments != 2 || st.Frames != 3 {
		t.Fatalf("stats segments=%d frames=%d, want 2 and 3", st.Segments, st.Frames)
	}
	if st.ParamSets != 1 {
		t.Fatalf("stats param sets = %d, want 1 because both segments share one set", st.ParamSets)
	}
	infos := s.Segments()
	if len(infos) != 2 {
		t.Fatalf("segment list length = %d, want 2", len(infos))
	}
	if infos[0].ID != "seg-1" || infos[1].ID != "seg-2" {
		t.Fatalf("segment order = %q,%q, want insertion order", infos[0].ID, infos[1].ID)
	}
	if infos[0].Frames != 2 || infos[0].FirstPTS != 0 || infos[0].LastPTS != 10 {
		t.Fatalf("first segment info = %+v, want 2 frames spanning 0..10", infos[0])
	}
	if len(s.ParamSets()) != 1 {
		t.Fatalf("parameter set cache length = %d, want 1", len(s.ParamSets()))
	}
}

func TestAddSegmentRejectsBadTiming(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	if _, err := s.AddSegment(videoSeg("empty", hdParams(), nil)); !errors.Is(err, impl.ErrEmptySegment) {
		t.Fatalf("a segment without frames must fail with ErrEmptySegment, got %v", err)
	}
	backwards := []impl.Frame{idrFrame(20), pFrame(10, true)}
	if _, err := s.AddSegment(videoSeg("back", hdParams(), backwards)); !errors.Is(err, impl.ErrBadTiming) {
		t.Fatalf("decreasing presentation timestamps must fail with ErrBadTiming, got %v", err)
	}
	negative := []impl.Frame{{PTS: -5, DTS: -5, Kind: impl.FrameIDR, Ref: true}}
	if _, err := s.AddSegment(videoSeg("neg", hdParams(), negative)); !errors.Is(err, impl.ErrBadTiming) {
		t.Fatalf("negative presentation timestamps must fail with ErrBadTiming, got %v", err)
	}
	if st := s.Stats(); st.Segments != 0 || st.Frames != 0 {
		t.Fatalf("rejected segments left state behind: segments=%d frames=%d", st.Segments, st.Frames)
	}
}

func TestAddSegmentEnforcesSegmentAndFrameLimits(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{MaxSegments: 2})
	for _, id := range []impl.SegID{"a", "b"} {
		if _, err := s.AddSegment(videoSeg(id, hdParams(), []impl.Frame{idrFrame(0)})); err != nil {
			t.Fatalf("segment %s must be accepted under the segment cap, got %v", id, err)
		}
	}
	if _, err := s.AddSegment(videoSeg("c", hdParams(), []impl.Frame{idrFrame(0)})); !errors.Is(err, impl.ErrSegmentLimit) {
		t.Fatalf("third segment must hit the segment cap with ErrSegmentLimit, got %v", err)
	}
	if st := s.Stats(); st.Segments != 2 {
		t.Fatalf("segment count = %d, want the cap 2", st.Segments)
	}

	budget := impl.NewBitstreamSplicer(impl.Limits{MaxFrames: 3})
	if _, err := budget.AddSegment(videoSeg("x", hdParams(), []impl.Frame{idrFrame(0), pFrame(10, true)})); err != nil {
		t.Fatalf("two frames must fit the frame budget, got %v", err)
	}
	if _, err := budget.AddSegment(videoSeg("y", hdParams(), []impl.Frame{idrFrame(0), pFrame(10, true)})); !errors.Is(err, impl.ErrFrameBudget) {
		t.Fatalf("exceeding the frame budget must fail with ErrFrameBudget, got %v", err)
	}
	if st := budget.Stats(); st.Frames != 2 {
		t.Fatalf("frame count after a rejected segment = %d, want 2", st.Frames)
	}
}

func TestAddSegmentEnforcesParamSetCache(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{MaxParamSets: 1})
	if _, err := s.AddSegment(videoSeg("a", hdParams(), []impl.Frame{idrFrame(0)})); err != nil {
		t.Fatalf("first parameter set must be cached, got %v", err)
	}
	if _, err := s.AddSegment(videoSeg("b", hdParams(), []impl.Frame{idrFrame(0)})); err != nil {
		t.Fatalf("an identical parameter set must be reused, got %v", err)
	}
	if got := len(s.ParamSets()); got != 1 {
		t.Fatalf("parameter set cache length = %d, want 1 after dedupe", got)
	}
	if _, err := s.AddSegment(videoSeg("c", sdParams(), []impl.Frame{idrFrame(0)})); !errors.Is(err, impl.ErrParamSetLimit) {
		t.Fatalf("a second distinct parameter set must hit ErrParamSetLimit, got %v", err)
	}
	if st := s.Stats(); st.Segments != 2 || st.ParamSets != 1 {
		t.Fatalf("stats segments=%d param sets=%d, want 2 and 1", st.Segments, st.ParamSets)
	}
}

func TestAddSegmentRequiresIDRForVideo(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	noIDR := []impl.Frame{pFrame(0, true), pFrame(10, true)}
	if _, err := s.AddSegment(videoSeg("v", hdParams(), noIDR)); !errors.Is(err, impl.ErrNoIDR) {
		t.Fatalf("a video segment without an idr frame must fail with ErrNoIDR, got %v", err)
	}
	if _, err := s.AddSegment(audioSeg("a1", noIDR)); err != nil {
		t.Fatalf("an audio segment must not need an idr frame, got %v", err)
	}
	st := s.Stats()
	if st.Segments != 1 || st.Frames != 2 {
		t.Fatalf("stats segments=%d frames=%d, want 1 and 2", st.Segments, st.Frames)
	}
	infos := s.Segments()
	if len(infos) != 1 || infos[0].Kind != impl.KindAudio {
		t.Fatalf("segment list = %+v, want exactly one audio segment", infos)
	}
}

func TestCheckCompatibilityIdentical(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	a, err := s.AddSegment(videoSeg("a", hdParams(), []impl.Frame{idrFrame(0)}))
	if err != nil {
		t.Fatalf("add a: %v", err)
	}
	b, err := s.AddSegment(videoSeg("b", hdParams(), []impl.Frame{idrFrame(0)}))
	if err != nil {
		t.Fatalf("add b: %v", err)
	}
	rep, err := s.CheckCompatibility(a, b)
	if err != nil {
		t.Fatalf("compatibility check must succeed, got %v", err)
	}
	if rep.A != a || rep.B != b {
		t.Fatalf("report ids = %q,%q, want %q,%q", rep.A, rep.B, a, b)
	}
	if !rep.Compatible {
		t.Fatalf("identical parameter sets must be compatible, got %+v", rep)
	}
	if len(rep.Reasons) != 0 {
		t.Fatalf("compatible segments must report no reasons, got %v", rep.Reasons)
	}
	if rep.NeedParamSet || rep.NeedReset {
		t.Fatalf("compatible segments must need neither a new parameter set nor a reset, got %+v", rep)
	}
}

func TestCheckCompatibilityMismatch(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	a, err := s.AddSegment(videoSeg("a", hdParams(), []impl.Frame{idrFrame(0)}))
	if err != nil {
		t.Fatalf("add a: %v", err)
	}
	b, err := s.AddSegment(videoSeg("b", sdParams(), []impl.Frame{idrFrame(0)}))
	if err != nil {
		t.Fatalf("add b: %v", err)
	}
	rep, err := s.CheckCompatibility(a, b)
	if err != nil {
		t.Fatalf("compatibility check must succeed, got %v", err)
	}
	if rep.Compatible {
		t.Fatalf("different resolutions must not be compatible, got %+v", rep)
	}
	if !rep.NeedParamSet || !rep.NeedReset {
		t.Fatalf("incompatible segments must need a new parameter set and a reset point, got %+v", rep)
	}
	if len(rep.Reasons) == 0 {
		t.Fatalf("incompatible segments must explain at least one reason")
	}
	if !sort.StringsAreSorted(rep.Reasons) {
		t.Fatalf("reason codes must be sorted, got %v", rep.Reasons)
	}
	found := false
	for _, reason := range rep.Reasons {
		if reason == "width" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a width difference must be reported as the reason width, got %v", rep.Reasons)
	}

	mixed, err := s.AddSegment(audioSeg("m", []impl.Frame{pFrame(0, true)}))
	if err != nil {
		t.Fatalf("add audio: %v", err)
	}
	kindRep, err := s.CheckCompatibility(a, mixed)
	if err != nil {
		t.Fatalf("video against audio must still return a report, got %v", err)
	}
	if kindRep.Compatible {
		t.Fatalf("a video segment must not be compatible with an audio segment")
	}
	kindFound := false
	for _, reason := range kindRep.Reasons {
		if reason == "kind" {
			kindFound = true
		}
	}
	if !kindFound {
		t.Fatalf("a kind difference must be reported as the reason kind, got %v", kindRep.Reasons)
	}

	if _, err := s.CheckCompatibility(a, impl.SegID("missing")); !errors.Is(err, impl.ErrUnknownSegment) {
		t.Fatalf("unknown right segment must fail with ErrUnknownSegment, got %v", err)
	}
	if _, err := s.CheckCompatibility(impl.SegID("missing"), a); !errors.Is(err, impl.ErrUnknownSegment) {
		t.Fatalf("unknown left segment must fail with ErrUnknownSegment, got %v", err)
	}
}

func TestPlanSpliceAlignsToIDR(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	if _, err := s.AddSegment(videoSeg("v", hdParams(), gopFrames())); err != nil {
		t.Fatalf("add segment: %v", err)
	}
	plan, err := s.PlanSplice(2)
	if err != nil {
		t.Fatalf("planning inside a gop must succeed, got %v", err)
	}
	if plan.Requested != 2 || plan.Cut != 4 {
		t.Fatalf("plan requested=%d cut=%d, want 2 and 4", plan.Requested, plan.Cut)
	}
	if plan.Wait != 2 {
		t.Fatalf("plan wait = %d, want 2 frames waited for the next idr", plan.Wait)
	}
	if plan.Dropped != 2 {
		t.Fatalf("plan dropped = %d, want the two non reference frames in the removed window", plan.Dropped)
	}
	if plan.NeedReset {
		t.Fatalf("a cut inside one segment must not ask for a decoder reset")
	}

	aligned, err := s.PlanSplice(4)
	if err != nil {
		t.Fatalf("planning on an idr must succeed, got %v", err)
	}
	if aligned.Cut != 4 || aligned.Wait != 0 || aligned.Dropped != 0 {
		t.Fatalf("idr aligned plan cut=%d wait=%d dropped=%d, want 4/0/0", aligned.Cut, aligned.Wait, aligned.Dropped)
	}

	if _, err := s.PlanSplice(-1); !errors.Is(err, impl.ErrBadSplicePoint) {
		t.Fatalf("negative index must fail with ErrBadSplicePoint, got %v", err)
	}
	if _, err := s.PlanSplice(len(gopFrames())); !errors.Is(err, impl.ErrBadSplicePoint) {
		t.Fatalf("index at the frame count must fail with ErrBadSplicePoint, got %v", err)
	}
	if _, err := s.PlanSplice(5); !errors.Is(err, impl.ErrNoIDR) {
		t.Fatalf("a point after the last idr must fail with ErrNoIDR, got %v", err)
	}
}

func TestSpliceRebasesAndMarksReset(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	if _, err := s.AddSegment(videoSeg("head", hdParams(), []impl.Frame{idrFrame(0), pFrame(10, true), pFrame(20, true)})); err != nil {
		t.Fatalf("add head: %v", err)
	}
	if _, err := s.AddSegment(videoSeg("body", sdParams(), []impl.Frame{idrFrame(0), pFrame(10, true)})); err != nil {
		t.Fatalf("add body: %v", err)
	}
	plan, err := s.PlanSplice(0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Cut != 0 || plan.Wait != 0 {
		t.Fatalf("plan at the first idr cut=%d wait=%d, want 0/0", plan.Cut, plan.Wait)
	}
	bs, err := s.Splice(plan)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if len(bs.Frames) != 5 {
		t.Fatalf("output frame count = %d, want 5", len(bs.Frames))
	}
	want := []int64{0, 10, 20, 21, 31}
	for i, pts := range want {
		if bs.Frames[i].PTS != pts {
			t.Fatalf("frame %d pts = %d, want %d (positions %v)", i, bs.Frames[i].PTS, pts, want)
		}
		if bs.Frames[i].DTS != pts {
			t.Fatalf("frame %d dts = %d, want it to equal pts %d", i, bs.Frames[i].DTS, pts)
		}
	}
	if len(bs.ParamSets) != 2 {
		t.Fatalf("output parameter sets = %d, want 2", len(bs.ParamSets))
	}
	if len(bs.ResetPTS) != 1 || bs.ResetPTS[0] != 21 {
		t.Fatalf("reset points = %v, want exactly [21] where the parameter set changes", bs.ResetPTS)
	}
}

func TestSpliceRemovesCutWindow(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	if _, err := s.AddSegment(videoSeg("v", hdParams(), gopFrames())); err != nil {
		t.Fatalf("add segment: %v", err)
	}
	plan, err := s.PlanSplice(2)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	bs, err := s.Splice(plan)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if len(bs.Frames) != 4 {
		t.Fatalf("output frame count = %d, want 4 after removing the window", len(bs.Frames))
	}
	want := []int64{0, 10, 40, 50}
	for i, pts := range want {
		if bs.Frames[i].PTS != pts {
			t.Fatalf("frame %d pts = %d, want %d", i, bs.Frames[i].PTS, pts)
		}
	}
	if bs.Frames[2].Kind != impl.FrameIDR {
		t.Fatalf("frame 2 kind = %q, want the idr at the cut", bs.Frames[2].Kind)
	}
	st := s.Stats()
	if st.Splices != 1 || st.Dropped != 2 || st.Resets != 0 {
		t.Fatalf("stats splices=%d dropped=%d resets=%d, want 1/2/0", st.Splices, st.Dropped, st.Resets)
	}
}

func TestSpliceRejectsStalePlan(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	if _, err := s.AddSegment(videoSeg("a", hdParams(), []impl.Frame{idrFrame(0), pFrame(10, true)})); err != nil {
		t.Fatalf("add a: %v", err)
	}
	plan, err := s.PlanSplice(0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if _, err := s.AddSegment(videoSeg("b", hdParams(), []impl.Frame{idrFrame(0)})); err != nil {
		t.Fatalf("add b: %v", err)
	}
	if _, err := s.Splice(plan); !errors.Is(err, impl.ErrPlanStale) {
		t.Fatalf("a plan built before a new segment must fail with ErrPlanStale, got %v", err)
	}
	fresh, err := s.PlanSplice(0)
	if err != nil {
		t.Fatalf("replanning: %v", err)
	}
	if _, err := s.Splice(fresh); err != nil {
		t.Fatalf("a fresh plan must splice, got %v", err)
	}
}

func TestSetTimebaseScalesOutput(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	if _, err := s.AddSegment(videoSeg("v", hdParams(), []impl.Frame{idrFrame(0), pFrame(10, true), pFrame(20, true)})); err != nil {
		t.Fatalf("add segment: %v", err)
	}
	if err := s.SetTimebase(impl.Timebase{Num: 1, Den: 2}); err != nil {
		t.Fatalf("halving timebase must be accepted, got %v", err)
	}
	if got := s.Timebase(); got != (impl.Timebase{Num: 1, Den: 2}) {
		t.Fatalf("timebase = %+v, want 1/2", got)
	}
	plan, err := s.PlanSplice(0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	bs, err := s.Splice(plan)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	want := []int64{0, 5, 10}
	if len(bs.Frames) != len(want) {
		t.Fatalf("output frames = %d, want %d", len(bs.Frames), len(want))
	}
	for i, pts := range want {
		if bs.Frames[i].PTS != pts {
			t.Fatalf("frame %d pts = %d, want %d after the timebase change", i, bs.Frames[i].PTS, pts)
		}
	}
	if err := s.SetTimebase(impl.Timebase{Num: 0, Den: 1}); !errors.Is(err, impl.ErrBadTimebase) {
		t.Fatalf("a zero numerator must fail with ErrBadTimebase, got %v", err)
	}
	if got := s.Timebase(); got != (impl.Timebase{Num: 1, Den: 2}) {
		t.Fatalf("a rejected timebase must not replace the live one, got %+v", got)
	}
}

func TestSetGopAlignOffCutsImmediately(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	if _, err := s.AddSegment(videoSeg("v", hdParams(), []impl.Frame{idrFrame(0), pFrame(10, true), pFrame(20, true), pFrame(30, true)})); err != nil {
		t.Fatalf("add segment: %v", err)
	}
	if err := s.SetGopAlign(false); err != nil {
		t.Fatalf("turning alignment off must succeed, got %v", err)
	}
	if s.GopAlign() {
		t.Fatalf("alignment must be off after SetGopAlign(false)")
	}
	plan, err := s.PlanSplice(2)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Cut != 2 || plan.Wait != 0 || plan.Dropped != 0 {
		t.Fatalf("unaligned plan cut=%d wait=%d dropped=%d, want 2/0/0", plan.Cut, plan.Wait, plan.Dropped)
	}
	bs, err := s.Splice(plan)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if len(bs.Frames) != 4 {
		t.Fatalf("output frames = %d, want all 4", len(bs.Frames))
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	limits := impl.Limits{MaxSegments: 8}
	s := impl.NewBitstreamSplicer(limits)
	if _, err := s.AddSegment(videoSeg("head", hdParams(), []impl.Frame{idrFrame(0), pFrame(10, true)})); err != nil {
		t.Fatalf("add head: %v", err)
	}
	if _, err := s.AddSegment(videoSeg("body", sdParams(), []impl.Frame{idrFrame(0), pFrame(10, true)})); err != nil {
		t.Fatalf("add body: %v", err)
	}
	if err := s.SetTimebase(impl.Timebase{Num: 1, Den: 2}); err != nil {
		t.Fatalf("set timebase: %v", err)
	}
	plan, err := s.PlanSplice(0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want, err := s.Splice(plan)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	r := impl.NewBitstreamSplicer(limits)
	if err := r.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := r.Timebase(); got != (impl.Timebase{Num: 1, Den: 2}) {
		t.Fatalf("restored timebase = %+v, want 1/2", got)
	}
	if got := len(r.Segments()); got != 2 {
		t.Fatalf("restored segments = %d, want 2", got)
	}
	if got := len(r.ParamSets()); got != 2 {
		t.Fatalf("restored parameter sets = %d, want 2", got)
	}
	if st := r.Stats(); st.Segments != 2 || st.Frames != 4 {
		t.Fatalf("restored stats segments=%d frames=%d, want 2 and 4", st.Segments, st.Frames)
	}
	rplan, err := r.PlanSplice(0)
	if err != nil {
		t.Fatalf("replan after restore: %v", err)
	}
	got, err := r.Splice(rplan)
	if err != nil {
		t.Fatalf("splice after restore: %v", err)
	}
	if len(got.Frames) != len(want.Frames) {
		t.Fatalf("restored splice frames = %d, want %d", len(got.Frames), len(want.Frames))
	}
	for i := range want.Frames {
		if got.Frames[i].PTS != want.Frames[i].PTS {
			t.Fatalf("restored frame %d pts = %d, want %d", i, got.Frames[i].PTS, want.Frames[i].PTS)
		}
	}
	if len(got.ResetPTS) != len(want.ResetPTS) {
		t.Fatalf("restored reset points = %v, want %v", got.ResetPTS, want.ResetPTS)
	}
	if err := r.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if st := r.Stats(); st.Segments != 2 {
		t.Fatalf("a rejected restore must not touch the live state, segments=%d", st.Segments)
	}
}

func TestConcurrentAddSegments(t *testing.T) {
	s := impl.NewBitstreamSplicer(impl.Limits{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.AddSegment(videoSeg(impl.SegID(fmt.Sprintf("s-%02d", i)), hdParams(), []impl.Frame{idrFrame(0)}))
			if err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if failed != 0 {
		t.Fatalf("%d concurrent segment adds failed, want 0", failed)
	}
	st := s.Stats()
	if st.Segments != 12 || st.Frames != 12 {
		t.Fatalf("stats segments=%d frames=%d, want 12 and 12", st.Segments, st.Frames)
	}
	if st.ParamSets != 1 {
		t.Fatalf("param sets = %d, want 1 because every segment shares the same set", st.ParamSets)
	}
	if got := len(s.Segments()); got != 12 {
		t.Fatalf("segment list length = %d, want 12", got)
	}
}
