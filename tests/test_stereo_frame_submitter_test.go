package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-b-19"
)

func testBudget() impl.StereoBudget {
	return impl.StereoBudget{MaxItemsPerEye: 2, MaxVertsPerEye: 1000, MaxReprojections: 2}
}

func newSubmitter() *impl.StereoFrameSubmitter {
	return impl.NewStereoFrameSubmitter(testBudget())
}

func hmd(frame int) impl.HmdState {
	return impl.HmdState{Frame: frame, StartTs: int64(frame) * 1000, RefreshHz: 90}
}

func poseAt(x float32) impl.Pose {
	return impl.Pose{Position: impl.Vec3{X: x, Y: 0, Z: 0}, Yaw: x * 2, Pitch: 0, IPD: 0.064}
}

func openFrame(t *testing.T, s *impl.StereoFrameSubmitter, frame int) {
	t.Helper()
	if err := s.BeginFrame(hmd(frame)); err != nil {
		t.Fatalf("BeginFrame(%d) must succeed, got %v", frame, err)
	}
}

func finishFrame(t *testing.T, s *impl.StereoFrameSubmitter) {
	t.Helper()
	if err := s.Submit(impl.EyeLeft, nil); err != nil {
		t.Fatalf("Submit(left) must succeed, got %v", err)
	}
	if err := s.Submit(impl.EyeRight, nil); err != nil {
		t.Fatalf("Submit(right) must succeed, got %v", err)
	}
	if _, err := s.EndFrame(); err != nil {
		t.Fatalf("EndFrame must succeed, got %v", err)
	}
}

func TestBeginFrameOpensFrameAndNumbersIt(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 5)
	if err := s.BeginFrame(hmd(6)); !errors.Is(err, impl.ErrFrameOpen) {
		t.Fatalf("second BeginFrame = %v, want ErrFrameOpen", err)
	}
	if err := s.Submit(impl.EyeLeft, nil); err != nil {
		t.Fatalf("Submit(left) must succeed, got %v", err)
	}
	if err := s.Submit(impl.EyeRight, nil); err != nil {
		t.Fatalf("Submit(right) must succeed, got %v", err)
	}
	report, err := s.EndFrame()
	if err != nil {
		t.Fatalf("EndFrame must succeed, got %v", err)
	}
	if report.Frame != 5 {
		t.Fatalf("report frame = %d, want 5", report.Frame)
	}
	if err := s.BeginFrame(hmd(5)); !errors.Is(err, impl.ErrBadHmd) {
		t.Fatalf("reused frame number = %v, want ErrBadHmd", err)
	}
	openFrame(t, s, 6)
	if st := s.Stats(); st.Frames != 1 {
		t.Fatalf("completed frames = %d, want 1", st.Frames)
	}
}

func TestBeginFrameRejectsBadHmd(t *testing.T) {
	s := newSubmitter()
	if err := s.BeginFrame(impl.HmdState{Frame: 1, StartTs: 0, RefreshHz: 90}); !errors.Is(err, impl.ErrBadHmd) {
		t.Fatalf("zero start timestamp = %v, want ErrBadHmd", err)
	}
	if err := s.BeginFrame(impl.HmdState{Frame: 1, StartTs: 1000, RefreshHz: 0}); !errors.Is(err, impl.ErrBadHmd) {
		t.Fatalf("zero refresh rate = %v, want ErrBadHmd", err)
	}
	if err := s.BeginFrame(impl.HmdState{Frame: 1, StartTs: -5, RefreshHz: 90}); !errors.Is(err, impl.ErrBadHmd) {
		t.Fatalf("negative start timestamp = %v, want ErrBadHmd", err)
	}
	openFrame(t, s, 1)
}

func TestSetEyePoseRejectsBadEyeAndClosedFrame(t *testing.T) {
	s := newSubmitter()
	if err := s.SetEyePose(impl.EyeLeft, poseAt(1), 100); !errors.Is(err, impl.ErrFrameClosed) {
		t.Fatalf("SetEyePose outside a frame = %v, want ErrFrameClosed", err)
	}
	openFrame(t, s, 1)
	if err := s.SetEyePose(impl.Eye(7), poseAt(1), 100); !errors.Is(err, impl.ErrBadEye) {
		t.Fatalf("SetEyePose with eye 7 = %v, want ErrBadEye", err)
	}
	if err := s.SetEyePose(impl.EyeLeft, impl.Pose{}, 100); !errors.Is(err, impl.ErrBadPose) {
		t.Fatalf("SetEyePose with an empty pose = %v, want ErrBadPose", err)
	}
	if err := s.SetEyePose(impl.EyeLeft, poseAt(1), 100); err != nil {
		t.Fatalf("SetEyePose must succeed, got %v", err)
	}
}

func TestLatchPoseReturnsLatestPoseUnderFrame(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 1)
	if err := s.SetEyePose(impl.EyeLeft, poseAt(1), 100); err != nil {
		t.Fatalf("SetEyePose must succeed, got %v", err)
	}
	if err := s.SetEyePose(impl.EyeLeft, poseAt(2), 200); err != nil {
		t.Fatalf("SetEyePose must succeed, got %v", err)
	}
	latched, err := s.LatchPose(impl.EyeLeft, 150)
	if err != nil {
		t.Fatalf("LatchPose must succeed, got %v", err)
	}
	if latched.Position.X != 1 {
		t.Fatalf("latched pose x = %v, want the newest pose at or before 150 (1)", latched.Position.X)
	}
	finishFrame(t, s)
	openFrame(t, s, 2)
	latched, err = s.LatchPose(impl.EyeLeft, 250)
	if err != nil {
		t.Fatalf("LatchPose must succeed, got %v", err)
	}
	if latched.Position.X != 2 {
		t.Fatalf("latched pose x = %v, want the newest pose at or before 250 (2)", latched.Position.X)
	}
}

func TestLatchPoseOnlyOncePerFrame(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 1)
	if err := s.SetEyePose(impl.EyeLeft, poseAt(1), 100); err != nil {
		t.Fatalf("SetEyePose must succeed, got %v", err)
	}
	if _, err := s.LatchPose(impl.EyeLeft, 150); err != nil {
		t.Fatalf("LatchPose must succeed, got %v", err)
	}
	if _, err := s.LatchPose(impl.EyeLeft, 160); !errors.Is(err, impl.ErrAlreadyLatched) {
		t.Fatalf("second LatchPose in the same frame = %v, want ErrAlreadyLatched", err)
	}
	finishFrame(t, s)
	openFrame(t, s, 2)
	if err := s.SetEyePose(impl.EyeLeft, poseAt(3), 2100); err != nil {
		t.Fatalf("SetEyePose must succeed, got %v", err)
	}
	latched, err := s.LatchPose(impl.EyeLeft, 2200)
	if err != nil {
		t.Fatalf("LatchPose in the next frame must succeed, got %v", err)
	}
	if latched.Position.X != 3 {
		t.Fatalf("latched pose x = %v, want 3", latched.Position.X)
	}
	if st := s.Stats(); st.Latches != 2 {
		t.Fatalf("latch count = %d, want 2", st.Latches)
	}
}

func TestLatchRequiresPose(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 1)
	if _, err := s.LatchPose(impl.EyeRight, 10); !errors.Is(err, impl.ErrNoPose) {
		t.Fatalf("LatchPose without a reported pose = %v, want ErrNoPose", err)
	}
	if err := s.SetEyePose(impl.EyeRight, poseAt(4), 50); err != nil {
		t.Fatalf("SetEyePose must succeed, got %v", err)
	}
	if _, err := s.LatchPose(impl.EyeRight, 60); err != nil {
		t.Fatalf("LatchPose after a reported pose must succeed, got %v", err)
	}
}

func TestShareVisibilityFiltersPerEye(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 1)
	vis := impl.VisibilitySet{Frame: 1, Items: []impl.SharedItem{
		{ID: 3, InLeft: true, InRight: true, Verts: 8},
		{ID: 1, InLeft: true, InRight: false, Verts: 4},
		{ID: 2, InLeft: false, InRight: true, Verts: 6},
	}}
	if err := s.ShareVisibility(vis); err != nil {
		t.Fatalf("ShareVisibility must succeed, got %v", err)
	}
	left, err := s.PerEyeCull(impl.EyeLeft)
	if err != nil {
		t.Fatalf("PerEyeCull(left) must succeed, got %v", err)
	}
	if len(left) != 2 || left[0].ID != 1 || left[1].ID != 3 {
		t.Fatalf("left cull = %+v, want ids 1 and 3 ascending", left)
	}
	if left[0].Eye != impl.EyeLeft || left[0].Verts != 4 {
		t.Fatalf("left item = %+v, want eye left with 4 verts", left[0])
	}
	right, err := s.PerEyeCull(impl.EyeRight)
	if err != nil {
		t.Fatalf("PerEyeCull(right) must succeed, got %v", err)
	}
	if len(right) != 2 || right[0].ID != 2 || right[1].ID != 3 {
		t.Fatalf("right cull = %+v, want ids 2 and 3 ascending", right)
	}
}

func TestShareVisibilityValidation(t *testing.T) {
	s := newSubmitter()
	if err := s.ShareVisibility(impl.VisibilitySet{Items: []impl.SharedItem{{ID: 1, InLeft: true}}}); !errors.Is(err, impl.ErrFrameClosed) {
		t.Fatalf("ShareVisibility outside a frame = %v, want ErrFrameClosed", err)
	}
	openFrame(t, s, 1)
	dup := impl.VisibilitySet{Items: []impl.SharedItem{
		{ID: 4, InLeft: true, Verts: 4},
		{ID: 4, InRight: true, Verts: 4},
	}}
	if err := s.ShareVisibility(dup); !errors.Is(err, impl.ErrBadVisibility) {
		t.Fatalf("duplicate id = %v, want ErrBadVisibility", err)
	}
	blind := impl.VisibilitySet{Items: []impl.SharedItem{{ID: 5, Verts: 4}}}
	if err := s.ShareVisibility(blind); !errors.Is(err, impl.ErrBadVisibility) {
		t.Fatalf("item invisible to both eyes = %v, want ErrBadVisibility", err)
	}
	if _, err := s.PerEyeCull(impl.EyeLeft); !errors.Is(err, impl.ErrNoVisibility) {
		t.Fatalf("PerEyeCull without a visibility set = %v, want ErrNoVisibility", err)
	}
}

func TestPerEyeCullRejectsOversizedEye(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 1)
	vis := impl.VisibilitySet{Items: []impl.SharedItem{
		{ID: 1, InLeft: true, Verts: 4},
		{ID: 2, InLeft: true, Verts: 4},
		{ID: 3, InLeft: true, Verts: 4},
	}}
	if err := s.ShareVisibility(vis); err != nil {
		t.Fatalf("ShareVisibility must succeed, got %v", err)
	}
	if _, err := s.PerEyeCull(impl.EyeLeft); !errors.Is(err, impl.ErrEyeBudget) {
		t.Fatalf("cull over the item cap = %v, want ErrEyeBudget", err)
	}
	if st := s.Stats(); st.Budget != 1 {
		t.Fatalf("budget rejections = %d, want 1", st.Budget)
	}
	if err := s.SetBudget(impl.StereoBudget{MaxItemsPerEye: 4, MaxVertsPerEye: 1000, MaxReprojections: 2}); err != nil {
		t.Fatalf("SetBudget must succeed, got %v", err)
	}
	left, err := s.PerEyeCull(impl.EyeLeft)
	if err != nil {
		t.Fatalf("cull under the raised cap must succeed, got %v", err)
	}
	if len(left) != 3 {
		t.Fatalf("left cull = %d items, want 3", len(left))
	}
}

func TestSubmitRequiresLeftThenRight(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 1)
	if err := s.Submit(impl.EyeRight, nil); !errors.Is(err, impl.ErrSubmitOrder) {
		t.Fatalf("Submit(right) before left = %v, want ErrSubmitOrder", err)
	}
	if err := s.Submit(impl.Eye(5), nil); !errors.Is(err, impl.ErrBadEye) {
		t.Fatalf("Submit with eye 5 = %v, want ErrBadEye", err)
	}
	if err := s.Submit(impl.EyeLeft, nil); err != nil {
		t.Fatalf("Submit(left) must succeed, got %v", err)
	}
	if err := s.Submit(impl.EyeLeft, nil); !errors.Is(err, impl.ErrSubmitOrder) {
		t.Fatalf("second Submit(left) = %v, want ErrSubmitOrder", err)
	}
	if err := s.Submit(impl.EyeRight, nil); err != nil {
		t.Fatalf("Submit(right) must succeed, got %v", err)
	}
	if st := s.Stats(); st.Submits != 2 {
		t.Fatalf("submit count = %d, want 2", st.Submits)
	}
}

func TestEndFrameRequiresBothEyes(t *testing.T) {
	s := newSubmitter()
	if _, err := s.EndFrame(); !errors.Is(err, impl.ErrFrameClosed) {
		t.Fatalf("EndFrame without a frame = %v, want ErrFrameClosed", err)
	}
	openFrame(t, s, 1)
	if err := s.Submit(impl.EyeLeft, nil); err != nil {
		t.Fatalf("Submit(left) must succeed, got %v", err)
	}
	if _, err := s.EndFrame(); !errors.Is(err, impl.ErrSubmitOrder) {
		t.Fatalf("EndFrame with the right eye missing = %v, want ErrSubmitOrder", err)
	}
	if err := s.Submit(impl.EyeRight, nil); err != nil {
		t.Fatalf("Submit(right) must succeed, got %v", err)
	}
	if _, err := s.EndFrame(); err != nil {
		t.Fatalf("EndFrame must succeed, got %v", err)
	}
	if _, err := s.EndFrame(); !errors.Is(err, impl.ErrFrameClosed) {
		t.Fatalf("second EndFrame = %v, want ErrFrameClosed", err)
	}
}

func TestEndFrameReportCountsItems(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 3)
	left := []impl.DrawItem{{ID: 1, Verts: 4, Eye: impl.EyeLeft}, {ID: 2, Verts: 4, Eye: impl.EyeLeft}}
	right := []impl.DrawItem{{ID: 1, Verts: 4, Eye: impl.EyeRight}, {ID: 2, Verts: 4, Eye: impl.EyeRight}, {ID: 3, Verts: 4, Eye: impl.EyeRight}}
	if err := s.Submit(impl.EyeLeft, left); err != nil {
		t.Fatalf("Submit(left) must succeed, got %v", err)
	}
	if err := s.Submit(impl.EyeRight, right); err != nil {
		t.Fatalf("Submit(right) must succeed, got %v", err)
	}
	report, err := s.EndFrame()
	if err != nil {
		t.Fatalf("EndFrame must succeed, got %v", err)
	}
	if report.LeftItems != 2 || report.RightItems != 3 {
		t.Fatalf("report = %d left / %d right, want 2 / 3", report.LeftItems, report.RightItems)
	}
	if st := s.Stats(); st.Frames != 1 || st.Submits != 2 {
		t.Fatalf("stats = %+v, want one frame and two submissions", st)
	}
}

func TestPerEyeCullDoesNotMutateSharedSet(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 1)
	vis := impl.VisibilitySet{Items: []impl.SharedItem{
		{ID: 10, InLeft: true, InRight: true, Verts: 4},
		{ID: 11, InLeft: true, InRight: false, Verts: 4},
	}}
	if err := s.ShareVisibility(vis); err != nil {
		t.Fatalf("ShareVisibility must succeed, got %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.PerEyeCull(impl.EyeLeft); err != nil {
			t.Fatalf("PerEyeCull must succeed, got %v", err)
		}
	}
	ids := s.SharedIDs()
	if len(ids) != 2 || ids[0] != 10 || ids[1] != 11 {
		t.Fatalf("shared ids after two culls = %v, want [10 11]", ids)
	}
	again, err := s.PerEyeCull(impl.EyeRight)
	if err != nil {
		t.Fatalf("PerEyeCull(right) must succeed, got %v", err)
	}
	if len(again) != 1 || again[0].ID != 10 {
		t.Fatalf("right cull = %+v, want only id 10", again)
	}
	ids[0] = 999
	if got := s.SharedIDs(); len(got) != 2 || got[0] != 10 {
		t.Fatalf("shared ids were aliased by the caller, got %v", got)
	}
}

func TestSnapshotRestoreKeepsFrameAndBudget(t *testing.T) {
	s := newSubmitter()
	openFrame(t, s, 5)
	if err := s.SetEyePose(impl.EyeLeft, poseAt(1), 5100); err != nil {
		t.Fatalf("SetEyePose must succeed, got %v", err)
	}
	if _, err := s.LatchPose(impl.EyeLeft, 5200); err != nil {
		t.Fatalf("LatchPose must succeed, got %v", err)
	}
	finishFrame(t, s)
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot must succeed, got %v", err)
	}
	other := impl.NewStereoFrameSubmitter(impl.StereoBudget{})
	if err := other.Restore(data); err != nil {
		t.Fatalf("Restore must succeed, got %v", err)
	}
	if st := other.Stats(); st.Frames != 1 {
		t.Fatalf("frames after Restore = %d, want 1", st.Frames)
	}
	if err := other.BeginFrame(hmd(5)); !errors.Is(err, impl.ErrBadHmd) {
		t.Fatalf("reused frame number after Restore = %v, want ErrBadHmd", err)
	}
	if err := other.BeginFrame(hmd(6)); err != nil {
		t.Fatalf("BeginFrame after Restore must succeed, got %v", err)
	}
	vis := impl.VisibilitySet{Items: []impl.SharedItem{
		{ID: 1, InLeft: true, Verts: 4},
		{ID: 2, InLeft: true, Verts: 4},
		{ID: 3, InLeft: true, Verts: 4},
	}}
	if err := other.ShareVisibility(vis); err != nil {
		t.Fatalf("ShareVisibility must succeed, got %v", err)
	}
	if _, err := other.PerEyeCull(impl.EyeLeft); !errors.Is(err, impl.ErrEyeBudget) {
		t.Fatalf("restored budget did not apply, cull = %v, want ErrEyeBudget", err)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	s := newSubmitter()
	if err := s.Restore([]byte("broken stereo snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("Restore of a corrupt payload = %v, want ErrBadSnapshot", err)
	}
	if st := s.Stats(); st.Frames != 0 {
		t.Fatalf("frames after a failed Restore = %d, want 0", st.Frames)
	}
	if err := s.BeginFrame(hmd(1)); err != nil {
		t.Fatalf("submitter must stay usable after a failed Restore, got %v", err)
	}
}
