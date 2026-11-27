package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch51-go-game-c-01"
)

const eps = 1e-9

func closeTo(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9
}

func closeBed(got []float64, want []float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			return false
		}
	}
	return true
}

func unitQuatZ(deg float64) impl.Quaternion {
	half := deg * math.Pi / 360
	return impl.Quaternion{W: math.Cos(half), Z: math.Sin(half)}
}

func TestNewEncoderClampsConfiguration(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 0})
	if e.Order() != impl.MinOrder {
		t.Fatalf("order for an unset configuration = %d, want %d", e.Order(), impl.MinOrder)
	}
	if e.Channels() != 4 {
		t.Fatalf("channels for order 1 = %d, want 4", e.Channels())
	}
	if e.Norm() != impl.NormSN3D {
		t.Fatalf("default normalisation = %q, want %q", e.Norm(), impl.NormSN3D)
	}
	if e.Taps() != impl.DefaultTaps || e.BlockSize() != impl.DefaultBlockSize {
		t.Fatalf("defaults taps=%d block=%d, want %d and %d", e.Taps(), e.BlockSize(), impl.DefaultTaps, impl.DefaultBlockSize)
	}
	second := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 2})
	if second.Channels() != 9 {
		t.Fatalf("channels for order 2 = %d, want 9", second.Channels())
	}
	third := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 9, Norm: impl.NormN3D})
	if third.Order() != impl.MinOrder || third.Channels() != 4 {
		t.Fatalf("out of range order collapsed to order=%d channels=%d, want 1 and 4", third.Order(), third.Channels())
	}
	if third.Norm() != impl.NormN3D {
		t.Fatalf("explicit normalisation = %q, want %q", third.Norm(), impl.NormN3D)
	}
}

func TestEncodeFrontSourceFirstOrder(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	bed, err := e.Encode(impl.Source{ID: "kick", Azimuth: 0, Elevation: 0, Gain: 1})
	if err != nil {
		t.Fatalf("encoding a frontal source must succeed, got %v", err)
	}
	if !closeBed(bed, []float64{1, 0, 0, 1}) {
		t.Fatalf("frontal bed = %v, want [1 0 0 1]", bed)
	}
	if !closeBed(e.Bed(), bed) {
		t.Fatalf("Bed() = %v, want the same values returned by Encode: %v", e.Bed(), bed)
	}
}

func TestEncodeAzimuthAndElevationMapping(t *testing.T) {
	left := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	bed, err := left.Encode(impl.Source{ID: "gtr", Azimuth: 90, Elevation: 0, Gain: 1})
	if err != nil {
		t.Fatalf("encoding a source at the left axis must succeed, got %v", err)
	}
	if !closeBed(bed, []float64{1, 1, 0, 0}) {
		t.Fatalf("left bed = %v, want [1 1 0 0]", bed)
	}
	top := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	bed, err = top.Encode(impl.Source{ID: "vox", Azimuth: 0, Elevation: 90, Gain: 1})
	if err != nil {
		t.Fatalf("encoding an overhead source must succeed, got %v", err)
	}
	if !closeBed(bed, []float64{1, 0, 1, 0}) {
		t.Fatalf("overhead bed = %v, want [1 0 1 0]", bed)
	}
	back := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	bed, err = back.Encode(impl.Source{ID: "bass", Azimuth: -90, Elevation: 0, Gain: 1})
	if err != nil {
		t.Fatalf("encoding a right axis source must succeed, got %v", err)
	}
	if !closeBed(bed, []float64{1, -1, 0, 0}) {
		t.Fatalf("right axis bed = %v, want [1 -1 0 0]", bed)
	}
}

func TestEncodeAccumulatesHalfGainSources(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	if _, err := e.Encode(impl.Source{ID: "a", Azimuth: 45, Elevation: 0, Gain: 0.5}); err != nil {
		t.Fatalf("first encode must succeed, got %v", err)
	}
	bed, err := e.Encode(impl.Source{ID: "b", Azimuth: 45, Elevation: 0, Gain: 0.5})
	if err != nil {
		t.Fatalf("second encode must succeed, got %v", err)
	}
	if e.SourceCount() != 2 {
		t.Fatalf("source count = %d, want 2", e.SourceCount())
	}
	want := []float64{1, math.Sqrt2 / 2, 0, math.Sqrt2 / 2}
	if !closeBed(bed, want) {
		t.Fatalf("accumulated bed = %v, want %v", bed, want)
	}
	if math.Abs(e.Bed()[0]-1) > 1e-9 {
		t.Fatalf("W channel after two half gain sources = %v, want 1", e.Bed()[0])
	}
}

func TestEncodeRejectsInvalidSource(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	cases := []impl.Source{
		{ID: "", Azimuth: 0, Elevation: 0, Gain: 1},
		{ID: "x", Azimuth: 0, Elevation: 120, Gain: 1},
		{ID: "x", Azimuth: 200, Elevation: 0, Gain: 1},
		{ID: "x", Azimuth: 0, Elevation: 0, Gain: -0.5},
	}
	for i, src := range cases {
		if _, err := e.Encode(src); !errors.Is(err, impl.ErrBadSource) {
			t.Fatalf("case %d: err = %v, want ErrBadSource", i, err)
		}
	}
	if e.SourceCount() != 0 {
		t.Fatalf("source count after rejected sources = %d, want 0", e.SourceCount())
	}
	if len(e.Bed()) != 4 {
		t.Fatalf("bed length after rejected sources = %d, want 4", len(e.Bed()))
	}
	for i, v := range e.Bed() {
		if v != 0 {
			t.Fatalf("bed channel %d after rejected sources = %v, want 0", i, v)
		}
	}
}

func TestEncodeTruncatesToEncoderOrder(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	bed, err := e.Encode(impl.Source{ID: "pad", Azimuth: 0, Elevation: 0, Gain: 1, Order: 3})
	if err != nil {
		t.Fatalf("encoding with an oversized source order must succeed, got %v", err)
	}
	if len(bed) != 4 {
		t.Fatalf("bed length = %d, want 4", len(bed))
	}
	if !closeBed(bed, []float64{1, 0, 0, 1}) {
		t.Fatalf("truncated bed = %v, want the first order values [1 0 0 1]", bed)
	}
	if e.Truncated() != 1 {
		t.Fatalf("truncated counter = %d, want 1", e.Truncated())
	}
	wide := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 3})
	bed, err = wide.Encode(impl.Source{ID: "pad", Azimuth: 0, Elevation: 0, Gain: 1, Order: 2})
	if err != nil {
		t.Fatalf("encoding a second order source must succeed, got %v", err)
	}
	if len(bed) != 16 {
		t.Fatalf("third order bed length = %d, want 16", len(bed))
	}
	for i := 9; i < 16; i++ {
		if bed[i] != 0 {
			t.Fatalf("channel %d of a second order source = %v, want 0", i, bed[i])
		}
	}
	if wide.Truncated() != 0 {
		t.Fatalf("truncated counter for an in range order = %d, want 0", wide.Truncated())
	}
}

func TestN3DNormalizationScalesBands(t *testing.T) {
	exact := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1, Norm: impl.NormN3D})
	bed, err := exact.Encode(impl.Source{ID: "a", Azimuth: 0, Elevation: 0, Gain: 1})
	if err != nil {
		t.Fatalf("encoding in N3D must succeed, got %v", err)
	}
	if !closeBed(bed, []float64{1, 0, 0, math.Sqrt(3)}) {
		t.Fatalf("N3D frontal bed = %v, want [1 0 0 %v]", bed, math.Sqrt(3))
	}
	sn3d := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 3})
	n3d := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 3, Norm: impl.NormN3D})
	src := impl.Source{ID: "field", Azimuth: 33, Elevation: 21, Gain: 0.8}
	a, err := sn3d.Encode(src)
	if err != nil {
		t.Fatalf("SN3D encode must succeed, got %v", err)
	}
	b, err := n3d.Encode(src)
	if err != nil {
		t.Fatalf("N3D encode must succeed, got %v", err)
	}
	if len(a) != 16 || len(b) != 16 {
		t.Fatalf("third order bed lengths = %d and %d, want 16", len(a), len(b))
	}
	for ch := 0; ch < 16; ch++ {
		band := int(math.Floor(math.Sqrt(float64(ch))))
		factor := math.Sqrt(float64(2*band + 1))
		if math.Abs(b[ch]-a[ch]*factor) > 1e-9 {
			t.Fatalf("channel %d band %d: N3D %v, SN3D %v times %v", ch, band, b[ch], a[ch], factor)
		}
	}
}

func TestSetNormalizationRejectsUnknownMode(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	if err := e.SetNormalization(impl.NormN3D); err != nil {
		t.Fatalf("switching to N3D must succeed, got %v", err)
	}
	if e.Norm() != impl.NormN3D {
		t.Fatalf("mode after switch = %q, want %q", e.Norm(), impl.NormN3D)
	}
	if err := e.SetNormalization(impl.NormMode("bogus")); !errors.Is(err, impl.ErrBadNorm) {
		t.Fatalf("unknown mode err = %v, want ErrBadNorm", err)
	}
	if e.Norm() != impl.NormN3D {
		t.Fatalf("mode changed by a rejected switch = %q, want %q", e.Norm(), impl.NormN3D)
	}
}

func TestRotateIdentityKeepsBed(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	if _, err := e.Encode(impl.Source{ID: "a", Azimuth: 20, Elevation: 10, Gain: 1}); err != nil {
		t.Fatalf("encode must succeed, got %v", err)
	}
	before := e.Bed()
	if err := e.Rotate(impl.Quaternion{W: 1}); err != nil {
		t.Fatalf("identity rotation must succeed, got %v", err)
	}
	if !closeBed(e.Bed(), before) {
		t.Fatalf("bed after identity rotation = %v, want %v", e.Bed(), before)
	}
	q := e.Rotation()
	if !closeTo(q.W, 1) || !closeTo(q.X, 0) || !closeTo(q.Y, 0) || !closeTo(q.Z, 0) {
		t.Fatalf("accumulated rotation = %+v, want the identity quaternion", q)
	}
}

func TestRotateQuarterTurnMovesFrontToLeft(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	if _, err := e.Encode(impl.Source{ID: "a", Azimuth: 0, Elevation: 0, Gain: 1}); err != nil {
		t.Fatalf("encode must succeed, got %v", err)
	}
	if err := e.Rotate(unitQuatZ(90)); err != nil {
		t.Fatalf("quarter turn must succeed, got %v", err)
	}
	got := e.Bed()
	want := []float64{1, 1, 0, 0}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-9 {
			t.Fatalf("bed after a quarter turn = %v, want %v", got, want)
		}
	}
}

func TestRotateRejectsNonUnitQuaternion(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	if err := e.Rotate(impl.Quaternion{W: 1}); err != nil {
		t.Fatalf("identity rotation must succeed, got %v", err)
	}
	if err := e.Rotate(impl.Quaternion{}); !errors.Is(err, impl.ErrBadQuaternion) {
		t.Fatalf("zero quaternion err = %v, want ErrBadQuaternion", err)
	}
	if err := e.Rotate(impl.Quaternion{W: 2}); !errors.Is(err, impl.ErrBadQuaternion) {
		t.Fatalf("non unit quaternion err = %v, want ErrBadQuaternion", err)
	}
	q := e.Rotation()
	if !closeTo(q.W, 1) {
		t.Fatalf("rotation changed by a rejected quaternion = %+v, want identity", q)
	}
}

func TestDecodeBinauralFrontBedIsSymmetric(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1, Taps: 8, BlockSize: 16})
	bed, err := e.Encode(impl.Source{ID: "a", Azimuth: 0, Elevation: 0, Gain: 1})
	if err != nil {
		t.Fatalf("encode must succeed, got %v", err)
	}
	out, err := e.DecodeBinaural(bed)
	if err != nil {
		t.Fatalf("decoding a frontal bed must succeed, got %v", err)
	}
	if len(out) != 2*e.BlockSize() {
		t.Fatalf("decoded sample count = %d, want %d", len(out), 2*e.BlockSize())
	}
	nonzero := false
	for i := 0; i < e.BlockSize(); i++ {
		left, right := out[2*i], out[2*i+1]
		if math.Abs(float64(left-right)) > 1e-6 {
			t.Fatalf("frame %d left=%v right=%v, want equal channels for a frontal bed", i, left, right)
		}
		if math.Abs(float64(left)) > 1e-6 {
			nonzero = true
		}
	}
	if !nonzero {
		t.Fatalf("binaural output is silent for a non silent bed")
	}
	if want := e.Channels() * e.Taps(); e.ConvolutionCost() != want {
		t.Fatalf("convolution cost = %d, want %d", e.ConvolutionCost(), want)
	}
}

func TestDecodeBinauralRejectsBadBedLength(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1, Taps: 4, BlockSize: 8})
	if _, err := e.DecodeBinaural([]float64{1, 0, 0, 1}); err != nil {
		t.Fatalf("decoding a well formed bed must succeed, got %v", err)
	}
	if _, err := e.DecodeBinaural([]float64{1, 0}); !errors.Is(err, impl.ErrBadBedLength) {
		t.Fatalf("short bed err = %v, want ErrBadBedLength", err)
	}
	if _, err := e.DecodeBinaural(nil); !errors.Is(err, impl.ErrBadBedLength) {
		t.Fatalf("nil bed err = %v, want ErrBadBedLength", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1, Taps: 4, BlockSize: 8})
	if _, err := e.Encode(impl.Source{ID: "a", Azimuth: 0, Elevation: 0, Gain: 1}); err != nil {
		t.Fatalf("encode must succeed, got %v", err)
	}
	if err := e.Rotate(impl.Quaternion{W: 1}); err != nil {
		t.Fatalf("identity rotation must succeed, got %v", err)
	}
	data, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	restored := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 2, Norm: impl.NormN3D})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	if restored.Order() != 1 || restored.Channels() != 4 {
		t.Fatalf("restored order=%d channels=%d, want 1 and 4", restored.Order(), restored.Channels())
	}
	if restored.SourceCount() != 1 {
		t.Fatalf("restored source count = %d, want 1", restored.SourceCount())
	}
	if !closeBed(restored.Bed(), []float64{1, 0, 0, 1}) {
		t.Fatalf("restored bed = %v, want [1 0 0 1]", restored.Bed())
	}
	if err := restored.Restore([]byte("not a snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot err = %v, want ErrBadSnapshot", err)
	}
	if restored.Channels() != 4 || !closeBed(restored.Bed(), []float64{1, 0, 0, 1}) {
		t.Fatalf("state changed by a rejected snapshot: channels=%d bed=%v", restored.Channels(), restored.Bed())
	}
}

func TestResetClearsBedAndRotation(t *testing.T) {
	e := impl.NewAmbisonicEncoder(impl.AmbisonicConfig{Order: 1})
	if _, err := e.Encode(impl.Source{ID: "a", Azimuth: 30, Elevation: 5, Gain: 1, Order: 3}); err != nil {
		t.Fatalf("encode must succeed, got %v", err)
	}
	if e.SourceCount() != 1 || e.Truncated() != 1 {
		t.Fatalf("counters before reset sources=%d truncated=%d, want 1 and 1", e.SourceCount(), e.Truncated())
	}
	if err := e.Reset(); err != nil {
		t.Fatalf("reset must succeed, got %v", err)
	}
	if e.SourceCount() != 0 || e.Truncated() != 0 {
		t.Fatalf("counters after reset sources=%d truncated=%d, want 0", e.SourceCount(), e.Truncated())
	}
	bed := e.Bed()
	if len(bed) != 4 {
		t.Fatalf("bed length after reset = %d, want 4", len(bed))
	}
	for i, v := range bed {
		if v != 0 {
			t.Fatalf("bed channel %d after reset = %v, want 0", i, v)
		}
	}
	q := e.Rotation()
	if !closeTo(q.W, 1) || !closeTo(q.Z, 0) {
		t.Fatalf("rotation after reset = %+v, want identity", q)
	}
}
