package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-game-c-08"
)

func newShaper(t *testing.T, bits int, seed uint64) *impl.DitherNoiseShaper {
	t.Helper()
	s := impl.NewDitherNoiseShaper(bits)
	if err := s.SetSeed(seed); err != nil {
		t.Fatalf("set seed: %v", err)
	}
	return s
}

func TestNewShaperDefaults(t *testing.T) {
	s := impl.NewDitherNoiseShaper(0)
	stats := s.Stats()
	if stats.Bits != 16 {
		t.Fatalf("default bit depth = %d, want 16", stats.Bits)
	}
	if !stats.ClipGuard {
		t.Fatalf("clip guard must start enabled")
	}
	if stats.CurveLen != 0 {
		t.Fatalf("default curve length = %d, want 0", stats.CurveLen)
	}
	if stats.ProcessedFrames != 0 || stats.Clipped != 0 {
		t.Fatalf("fresh counters = %d frames, %d clipped; want zero", stats.ProcessedFrames, stats.Clipped)
	}
}

func TestSetTargetBitsValidatesRange(t *testing.T) {
	s := newShaper(t, 16, 1)
	if err := s.SetTargetBits(impl.MinBits - 1); !errors.Is(err, impl.ErrBitsRange) {
		t.Fatalf("depth below the window must fail with ErrBitsRange, got %v", err)
	}
	if err := s.SetTargetBits(impl.MaxBits + 1); !errors.Is(err, impl.ErrBitsRange) {
		t.Fatalf("depth above the window must fail with ErrBitsRange, got %v", err)
	}
	if got := s.Stats().Bits; got != 16 {
		t.Fatalf("rejected depth must keep 16 bits, got %d", got)
	}
	if err := s.SetTargetBits(8); err != nil {
		t.Fatalf("eight bits must be accepted, got %v", err)
	}
	if got := s.Stats().Bits; got != 8 {
		t.Fatalf("stats bit depth = %d, want 8", got)
	}
	if err := s.SetTargetBits(impl.MaxBits); err != nil {
		t.Fatalf("the exact upper bound must be accepted, got %v", err)
	}
}

func TestSetShapeCurveRejectsOverlongCurve(t *testing.T) {
	s := newShaper(t, 16, 1)
	curve := make([]float64, impl.MaxCurveLen+1)
	for i := range curve {
		curve[i] = 0.1
	}
	if err := s.SetShapeCurve(curve); !errors.Is(err, impl.ErrCurveLength) {
		t.Fatalf("curve above the coefficient window must fail with ErrCurveLength, got %v", err)
	}
	if got := s.Stats().CurveLen; got != 0 {
		t.Fatalf("rejected curve must not be installed, curve length = %d", got)
	}
}

func TestSetShapeCurveRejectsOutOfRangeCoefficient(t *testing.T) {
	s := newShaper(t, 16, 1)
	if err := s.SetShapeCurve([]float64{0.5, 1.2}); !errors.Is(err, impl.ErrCurveCoeff) {
		t.Fatalf("coefficient above one must fail with ErrCurveCoeff, got %v", err)
	}
	if err := s.SetShapeCurve([]float64{-1.1}); !errors.Is(err, impl.ErrCurveCoeff) {
		t.Fatalf("coefficient below minus one must fail with ErrCurveCoeff, got %v", err)
	}
	if err := s.SetShapeCurve([]float64{0.5, 0.25}); err != nil {
		t.Fatalf("valid curve: %v", err)
	}
	if got := s.Stats().CurveLen; got != 2 {
		t.Fatalf("installed curve length = %d, want 2", got)
	}
	if err := s.SetShapeCurve(nil); err != nil {
		t.Fatalf("clearing the curve: %v", err)
	}
	if got := s.Stats().CurveLen; got != 0 {
		t.Fatalf("cleared curve length = %d, want 0", got)
	}
}

func TestProcessRejectsBadChannelCount(t *testing.T) {
	s := newShaper(t, 16, 1)
	if _, err := s.Process(nil, 0); !errors.Is(err, impl.ErrChannelCount) {
		t.Fatalf("zero channels must fail with ErrChannelCount, got %v", err)
	}
	if _, err := s.Process(make([]float32, impl.MaxChannels+1), impl.MaxChannels+1); !errors.Is(err, impl.ErrChannelCount) {
		t.Fatalf("more channels than the window must fail with ErrChannelCount, got %v", err)
	}
}

func TestProcessRejectsRaggedInput(t *testing.T) {
	s := newShaper(t, 16, 1)
	if _, err := s.Process(make([]float32, 5), 2); !errors.Is(err, impl.ErrRaggedInput) {
		t.Fatalf("odd sample count on two channels must fail with ErrRaggedInput, got %v", err)
	}
	tooMany := make([]float32, 2*(impl.MaxBlockFrames+1))
	if _, err := s.Process(tooMany, 2); !errors.Is(err, impl.ErrFramesRange) {
		t.Fatalf("frames above the block window must fail with ErrFramesRange, got %v", err)
	}
}

func TestProcessQuantisesMidScaleSamples(t *testing.T) {
	s := newShaper(t, 16, 11)
	out, err := s.Process([]float32{0.5}, 1)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("returned %d samples, want 1", len(out))
	}
	if out[0] < 16382 || out[0] > 16385 {
		t.Fatalf("half scale sample = %d, want a value within one dither step of 16383.5", out[0])
	}
	if got := s.Stats().ProcessedFrames; got != 1 {
		t.Fatalf("processed frames = %d, want 1", got)
	}
}

func TestProcessIsDeterministicForSameSeed(t *testing.T) {
	in := make([]float32, 64)
	for i := range in {
		in[i] = 0.25
	}
	a := newShaper(t, 16, 42)
	b := newShaper(t, 16, 42)
	outA, err := a.Process(in, 1)
	if err != nil {
		t.Fatalf("process with the first shaper: %v", err)
	}
	outB, err := b.Process(in, 1)
	if err != nil {
		t.Fatalf("process with the second shaper: %v", err)
	}
	if len(outA) != len(in) || len(outB) != len(in) {
		t.Fatalf("returned %d and %d samples, want %d", len(outA), len(outB), len(in))
	}
	for i := range outA {
		if outA[i] != outB[i] {
			t.Fatalf("sample %d differs between two runs with the same seed: %d vs %d", i, outA[i], outB[i])
		}
	}
}

func TestProcessDiffersAcrossSeeds(t *testing.T) {
	in := make([]float32, 128)
	for i := range in {
		in[i] = 0.25
	}
	a := newShaper(t, 16, 7)
	b := newShaper(t, 16, 8)
	outA, err := a.Process(in, 1)
	if err != nil {
		t.Fatalf("process with seed seven: %v", err)
	}
	outB, err := b.Process(in, 1)
	if err != nil {
		t.Fatalf("process with seed eight: %v", err)
	}
	same := true
	for i := range outA {
		if outA[i] != outB[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatalf("two different seeds produced an identical dither sequence")
	}
}

func TestProcessInterleavesChannels(t *testing.T) {
	s := newShaper(t, 16, 21)
	in := []float32{0.5, -0.5, 0.5, -0.5, 0.5, -0.5}
	out, err := s.Process(in, 2)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("returned %d samples, want the interleaved length %d", len(out), len(in))
	}
	for i, v := range out {
		if i%2 == 0 && v < 16000 {
			t.Fatalf("left channel sample %d = %d, want a positive half scale code", i, v)
		}
		if i%2 == 1 && v > -16000 {
			t.Fatalf("right channel sample %d = %d, want a negative half scale code", i, v)
		}
	}
}

func TestClipGuardClampsAndCounts(t *testing.T) {
	s := newShaper(t, 16, 3)
	out, err := s.Process([]float32{1.5}, 1)
	if err != nil {
		t.Fatalf("process hot sample: %v", err)
	}
	if out[0] != 32767 {
		t.Fatalf("sample above full scale = %d, want the positive full scale code 32767", out[0])
	}
	if got := s.Stats().Clipped; got != 1 {
		t.Fatalf("clip counter = %d, want 1", got)
	}
	out, err = s.Process([]float32{-1.5}, 1)
	if err != nil {
		t.Fatalf("process hot negative sample: %v", err)
	}
	if out[0] != -32768 {
		t.Fatalf("sample below full scale = %d, want the negative full scale code -32768", out[0])
	}
	if got := s.Stats().Clipped; got != 2 {
		t.Fatalf("clip counter = %d, want 2", got)
	}
}

func TestClipGuardDisabledWrapsInsteadOfClamping(t *testing.T) {
	s := newShaper(t, 16, 3)
	if err := s.SetClipGuard(false); err != nil {
		t.Fatalf("disable the clip guard: %v", err)
	}
	out, err := s.Process([]float32{1.5}, 1)
	if err != nil {
		t.Fatalf("process hot sample: %v", err)
	}
	if out[0] >= 0 {
		t.Fatalf("sample above full scale with the guard off = %d, want the wrapped negative code", out[0])
	}
	if got := s.Stats().Clipped; got != 0 {
		t.Fatalf("clip counter with the guard off = %d, want 0", got)
	}
}

func TestResetChannelRestoresTheInitialSubstream(t *testing.T) {
	in := make([]float32, 64)
	for i := range in {
		in[i] = float32(i%7) * 0.1
	}
	fresh := newShaper(t, 16, 99)
	baseline, err := fresh.Process(in, 2)
	if err != nil {
		t.Fatalf("process with the reference shaper: %v", err)
	}
	m := newShaper(t, 16, 99)
	if _, err := m.Process(in, 2); err != nil {
		t.Fatalf("process before the reset: %v", err)
	}
	if err := m.ResetChannel(0); err != nil {
		t.Fatalf("reset channel: %v", err)
	}
	again, err := m.Process(in, 2)
	if err != nil {
		t.Fatalf("process after the reset: %v", err)
	}
	for frame := 0; frame < len(in)/2; frame++ {
		got := again[frame*2]
		want := baseline[frame*2]
		if got != want {
			t.Fatalf("channel zero frame %d after reset = %d, want the initial substream value %d", frame, got, want)
		}
	}
	if err := m.ResetChannel(impl.MaxChannels); !errors.Is(err, impl.ErrUnknownChannel) {
		t.Fatalf("reset of an out of range channel must fail with ErrUnknownChannel, got %v", err)
	}
}

func TestErrorSpectrumTracksPerChannel(t *testing.T) {
	s := newShaper(t, 16, 5)
	if err := s.SetShapeCurve([]float64{0.5}); err != nil {
		t.Fatalf("set curve: %v", err)
	}
	in := make([]float32, 32)
	for i := range in {
		in[i] = 0.3
	}
	if _, err := s.Process(in, 2); err != nil {
		t.Fatalf("process: %v", err)
	}
	left, err := s.ErrorSpectrum(0)
	if err != nil {
		t.Fatalf("spectrum of channel zero: %v", err)
	}
	right, err := s.ErrorSpectrum(1)
	if err != nil {
		t.Fatalf("spectrum of channel one: %v", err)
	}
	if len(left) != 1 || len(right) != 1 {
		t.Fatalf("spectrum lengths = %d and %d, want the curve length 1", len(left), len(right))
	}
	if left[0] == right[0] {
		t.Fatalf("both channels retained the same error %v, want independent feedback state", left[0])
	}
	if _, err := s.ErrorSpectrum(impl.MaxChannels); !errors.Is(err, impl.ErrUnknownChannel) {
		t.Fatalf("spectrum of an out of range channel must fail with ErrUnknownChannel, got %v", err)
	}
	if _, err := s.ErrorSpectrum(-1); !errors.Is(err, impl.ErrUnknownChannel) {
		t.Fatalf("spectrum of a negative channel must fail with ErrUnknownChannel, got %v", err)
	}
}

func TestSnapshotRestoreContinuesExactly(t *testing.T) {
	in := make([]float32, 40)
	for i := range in {
		in[i] = float32(i%11) * 0.09
	}
	m := newShaper(t, 16, 1234)
	if err := m.SetShapeCurve([]float64{0.4, 0.2}); err != nil {
		t.Fatalf("set curve: %v", err)
	}
	if _, err := m.Process(in, 1); err != nil {
		t.Fatalf("process before the snapshot: %v", err)
	}
	data, err := m.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewDitherNoiseShaper(16)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.Stats().CurveLen; got != 2 {
		t.Fatalf("restored curve length = %d, want 2", got)
	}
	if got := restored.Stats().Seed; got != 1234 {
		t.Fatalf("restored seed = %d, want 1234", got)
	}
	next, err := m.Process(in, 1)
	if err != nil {
		t.Fatalf("process after the snapshot: %v", err)
	}
	resumed, err := restored.Process(in, 1)
	if err != nil {
		t.Fatalf("process on the restored shaper: %v", err)
	}
	for i := range next {
		if next[i] != resumed[i] {
			t.Fatalf("sample %d after restore = %d, want the uninterrupted value %d", i, resumed[i], next[i])
		}
	}
	if err := restored.Restore([]byte("garbage")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := restored.Stats().CurveLen; got != 2 {
		t.Fatalf("rejected restore must not touch live state, curve length = %d", got)
	}
}
