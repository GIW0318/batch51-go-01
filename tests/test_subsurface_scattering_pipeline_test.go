package task

import (
	"errors"
	"math"
	"testing"

	impl "gsb/batch51-go-game-b-20"
)

func testLimits() impl.SssLimits {
	return impl.SssLimits{MaxProfiles: 8, MaxGroups: 4, MaxKernelBudget: 100000, MaxTempBytes: 1 << 20}
}

func skinProfile(materialID uint16, radius float32) impl.DiffusionProfile {
	return impl.DiffusionProfile{
		MaterialID: materialID,
		Name:       "skin",
		Radius:     radius,
		Falloff:    0.5,
		Area:       1,
	}
}

func uniformMaterialMap(w, h int, material uint16) impl.MaterialIDMap {
	slots := make([]impl.MaterialSlot, w*h)
	for i := range slots {
		slots[i] = impl.MaterialSlot{Primary: material, PrimaryWeight: 1}
	}
	return impl.MaterialIDMap{W: w, H: h, Slots: slots}
}

func uniformThickness(w, h int, thickness float32) impl.ThicknessMap {
	values := make([]float32, w*h)
	for i := range values {
		values[i] = thickness
	}
	return impl.ThicknessMap{W: w, H: h, Thickness: values}
}

func uniformDepth(w, h int, depth float32) impl.DepthTex {
	values := make([]float32, w*h)
	for i := range values {
		values[i] = depth
	}
	return impl.DepthTex{W: w, H: h, Depth: values}
}

func uniformImage(w, h int, colour impl.RGB) impl.Image {
	pixels := make([]impl.RGB, w*h)
	for i := range pixels {
		pixels[i] = colour
	}
	return impl.Image{W: w, H: h, Pixels: pixels}
}

func preparedPipeline(t *testing.T, w, h int) *impl.SubsurfaceScatteringPipeline {
	t.Helper()
	p := impl.NewSubsurfaceScatteringPipeline(testLimits())
	if err := p.RegisterProfile("skin", skinProfile(1, 1)); err != nil {
		t.Fatalf("RegisterProfile must succeed, got %v", err)
	}
	if err := p.SetMaterialMap(uniformMaterialMap(w, h, 1)); err != nil {
		t.Fatalf("SetMaterialMap must succeed, got %v", err)
	}
	if err := p.SetThicknessMap(uniformThickness(w, h, 1)); err != nil {
		t.Fatalf("SetThicknessMap must succeed, got %v", err)
	}
	return p
}

func TestRegisterProfileValidation(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(testLimits())
	if err := p.RegisterProfile("", skinProfile(1, 1)); !errors.Is(err, impl.ErrBadProfile) {
		t.Fatalf("empty id = %v, want ErrBadProfile", err)
	}
	if err := p.RegisterProfile("wax", skinProfile(0, 1)); !errors.Is(err, impl.ErrBadProfile) {
		t.Fatalf("material id zero = %v, want ErrBadProfile", err)
	}
	if err := p.RegisterProfile("wax", skinProfile(2, 0)); !errors.Is(err, impl.ErrBadProfile) {
		t.Fatalf("zero radius = %v, want ErrBadProfile", err)
	}
	if err := p.RegisterProfile("skin", skinProfile(1, 1)); err != nil {
		t.Fatalf("RegisterProfile must succeed, got %v", err)
	}
	if err := p.RegisterProfile("skin-dup", skinProfile(1, 2)); !errors.Is(err, impl.ErrProfileExists) {
		t.Fatalf("duplicate material id = %v, want ErrProfileExists", err)
	}
	if err := p.RegisterProfile("skin", skinProfile(4, 1)); !errors.Is(err, impl.ErrProfileExists) {
		t.Fatalf("duplicate profile id = %v, want ErrProfileExists", err)
	}
	if got := p.ProfileCount(); got != 1 {
		t.Fatalf("profile count = %d, want 1", got)
	}
}

func TestRegisterProfileLimit(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(impl.SssLimits{MaxProfiles: 2, MaxGroups: 4, MaxKernelBudget: 1000})
	if err := p.RegisterProfile("a", skinProfile(1, 1)); err != nil {
		t.Fatalf("RegisterProfile must succeed, got %v", err)
	}
	if err := p.RegisterProfile("b", skinProfile(2, 1)); err != nil {
		t.Fatalf("RegisterProfile must succeed, got %v", err)
	}
	if err := p.RegisterProfile("c", skinProfile(3, 1)); !errors.Is(err, impl.ErrProfileLimit) {
		t.Fatalf("profile over the cap = %v, want ErrProfileLimit", err)
	}
	if got := p.ProfileCount(); got != 2 {
		t.Fatalf("profile count = %d, want 2", got)
	}
}

func TestSampleProfileLookup(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(testLimits())
	if err := p.RegisterProfile("skin", skinProfile(3, 2)); err != nil {
		t.Fatalf("RegisterProfile must succeed, got %v", err)
	}
	got, err := p.SampleProfile(3)
	if err != nil {
		t.Fatalf("SampleProfile(3) must succeed, got %v", err)
	}
	if got.MaterialID != 3 || got.Radius != 2 || got.Name != "skin" {
		t.Fatalf("profile = %+v, want the registered skin profile", got)
	}
	if _, err := p.SampleProfile(0); !errors.Is(err, impl.ErrNoMaterial) {
		t.Fatalf("SampleProfile(0) = %v, want ErrNoMaterial", err)
	}
	if _, err := p.SampleProfile(9); !errors.Is(err, impl.ErrUnknownMaterial) {
		t.Fatalf("SampleProfile(9) = %v, want ErrUnknownMaterial", err)
	}
}

func TestSetMaterialMapValidation(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(testLimits())
	if err := p.SetMaterialMap(impl.MaterialIDMap{W: 2, H: 2, Slots: make([]impl.MaterialSlot, 3)}); !errors.Is(err, impl.ErrBadMaterialMap) {
		t.Fatalf("slot count mismatch = %v, want ErrBadMaterialMap", err)
	}
	bad := uniformMaterialMap(2, 2, 1)
	bad.Slots[0].PrimaryWeight = 1.5
	if err := p.SetMaterialMap(bad); !errors.Is(err, impl.ErrBadMaterialMap) {
		t.Fatalf("primary weight above one = %v, want ErrBadMaterialMap", err)
	}
	if err := p.SetMaterialMap(uniformMaterialMap(2, 2, 1)); err != nil {
		t.Fatalf("SetMaterialMap must succeed, got %v", err)
	}
}

func TestSetThicknessMapValidation(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(testLimits())
	if err := p.SetThicknessMap(impl.ThicknessMap{W: 2, H: 2, Thickness: make([]float32, 3)}); !errors.Is(err, impl.ErrBadThickness) {
		t.Fatalf("thickness count mismatch = %v, want ErrBadThickness", err)
	}
	negative := uniformThickness(2, 2, 1)
	negative.Thickness[3] = -0.5
	if err := p.SetThicknessMap(negative); !errors.Is(err, impl.ErrBadThickness) {
		t.Fatalf("negative thickness = %v, want ErrBadThickness", err)
	}
	if err := p.SetThicknessMap(uniformThickness(2, 2, 1)); err != nil {
		t.Fatalf("SetThicknessMap must succeed, got %v", err)
	}
}

func TestSetQualityControlsKernelRadius(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(testLimits())
	if err := p.SetQuality(impl.QualityLevels); !errors.Is(err, impl.ErrBadQuality) {
		t.Fatalf("quality %d = %v, want ErrBadQuality", impl.QualityLevels, err)
	}
	if err := p.SetQuality(-1); !errors.Is(err, impl.ErrBadQuality) {
		t.Fatalf("quality -1 = %v, want ErrBadQuality", err)
	}
	for level := 0; level < impl.QualityLevels; level++ {
		if err := p.SetQuality(level); err != nil {
			t.Fatalf("SetQuality(%d) must succeed, got %v", level, err)
		}
		st := p.Stats()
		if st.Quality != level {
			t.Fatalf("stats quality = %d, want %d", st.Quality, level)
		}
		if st.KernelRadius != impl.KernelRadii[level] {
			t.Fatalf("kernel radius at level %d = %d, want %d", level, st.KernelRadius, impl.KernelRadii[level])
		}
	}
}

func TestBlurRequiresMapsAndProfiles(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(testLimits())
	src := uniformImage(2, 2, impl.RGB{R: 1})
	depth := uniformDepth(2, 2, 1)
	if _, err := p.Blur(src, depth); !errors.Is(err, impl.ErrNoMaterialMap) {
		t.Fatalf("Blur without a material map = %v, want ErrNoMaterialMap", err)
	}
	if err := p.SetMaterialMap(uniformMaterialMap(2, 2, 1)); err != nil {
		t.Fatalf("SetMaterialMap must succeed, got %v", err)
	}
	if _, err := p.Blur(src, depth); !errors.Is(err, impl.ErrNoThicknessMap) {
		t.Fatalf("Blur without a thickness map = %v, want ErrNoThicknessMap", err)
	}
	if err := p.SetThicknessMap(uniformThickness(2, 2, 1)); err != nil {
		t.Fatalf("SetThicknessMap must succeed, got %v", err)
	}
	if _, err := p.Blur(src, depth); !errors.Is(err, impl.ErrNoProfile) {
		t.Fatalf("Blur without a profile = %v, want ErrNoProfile", err)
	}
	if err := p.RegisterProfile("skin", skinProfile(1, 1)); err != nil {
		t.Fatalf("RegisterProfile must succeed, got %v", err)
	}
	if _, err := p.Blur(src, depth); err != nil {
		t.Fatalf("Blur must succeed, got %v", err)
	}
}

func TestBlurPreservesUniformImage(t *testing.T) {
	p := preparedPipeline(t, 3, 3)
	src := uniformImage(3, 3, impl.RGB{R: 1, G: 0.5, B: 0.25})
	out, err := p.Blur(src, uniformDepth(3, 3, 1))
	if err != nil {
		t.Fatalf("Blur must succeed, got %v", err)
	}
	if out.W != 3 || out.H != 3 || len(out.Pixels) != 9 {
		t.Fatalf("output shape = %dx%d with %d pixels, want 3x3 with 9", out.W, out.H, len(out.Pixels))
	}
	for i, px := range out.Pixels {
		if math.Abs(float64(px.R-1)) > 1e-5 || math.Abs(float64(px.G-0.5)) > 1e-5 || math.Abs(float64(px.B-0.25)) > 1e-5 {
			t.Fatalf("pixel %d = %+v, want the uniform input colour", i, px)
		}
	}
	if st := p.Stats(); st.Frames != 1 || st.Samples <= 0 {
		t.Fatalf("stats after one blur = %+v, want one frame with samples counted", st)
	}
}

func TestBlurStopsAtDepthDiscontinuity(t *testing.T) {
	p := preparedPipeline(t, 3, 1)
	src := impl.Image{W: 3, H: 1, Pixels: []impl.RGB{
		{R: 1, G: 0, B: 0},
		{R: 0, G: 0, B: 1},
		{R: 0, G: 0, B: 1},
	}}
	depth := impl.DepthTex{W: 3, H: 1, Depth: []float32{1, 100, 100}}
	out, err := p.Blur(src, depth)
	if err != nil {
		t.Fatalf("Blur must succeed, got %v", err)
	}
	if len(out.Pixels) != 3 {
		t.Fatalf("output pixels = %d, want 3", len(out.Pixels))
	}
	front := out.Pixels[0]
	if math.Abs(float64(front.R-1)) > 1e-5 || math.Abs(float64(front.B)) > 1e-5 {
		t.Fatalf("foreground pixel = %+v, want pure red with no background bleed", front)
	}
	back := out.Pixels[1]
	if math.Abs(float64(back.B-1)) > 1e-5 || math.Abs(float64(back.R)) > 1e-5 {
		t.Fatalf("background pixel = %+v, want pure blue with no foreground bleed", back)
	}
}

func TestBlurRejectsSizeMismatch(t *testing.T) {
	p := preparedPipeline(t, 2, 2)
	if _, err := p.Blur(uniformImage(3, 3, impl.RGB{R: 1}), uniformDepth(3, 3, 1)); !errors.Is(err, impl.ErrBadImage) {
		t.Fatalf("image larger than the maps = %v, want ErrBadImage", err)
	}
	if _, err := p.Blur(uniformImage(2, 2, impl.RGB{R: 1}), uniformDepth(3, 2, 1)); !errors.Is(err, impl.ErrBadImage) {
		t.Fatalf("depth buffer of the wrong size = %v, want ErrBadImage", err)
	}
	if _, err := p.Blur(uniformImage(2, 2, impl.RGB{R: 1}), uniformDepth(2, 2, 1)); err != nil {
		t.Fatalf("matching sizes must be accepted, got %v", err)
	}
}

func TestBlurMergesMaterialGroups(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(impl.SssLimits{MaxProfiles: 8, MaxGroups: 2, MaxKernelBudget: 100000})
	for id := uint16(1); id <= 4; id++ {
		if err := p.RegisterProfile(string(rune('a'+id)), skinProfile(id, float32(id))); err != nil {
			t.Fatalf("RegisterProfile(%d) must succeed, got %v", id, err)
		}
	}
	mat := impl.MaterialIDMap{W: 4, H: 1, Slots: []impl.MaterialSlot{
		{Primary: 1, PrimaryWeight: 1},
		{Primary: 2, PrimaryWeight: 1},
		{Primary: 3, PrimaryWeight: 1},
		{Primary: 4, PrimaryWeight: 1},
	}}
	if err := p.SetMaterialMap(mat); err != nil {
		t.Fatalf("SetMaterialMap must succeed, got %v", err)
	}
	if err := p.SetThicknessMap(uniformThickness(4, 1, 1)); err != nil {
		t.Fatalf("SetThicknessMap must succeed, got %v", err)
	}
	if _, err := p.Blur(uniformImage(4, 1, impl.RGB{R: 1}), uniformDepth(4, 1, 1)); err != nil {
		t.Fatalf("Blur must succeed, got %v", err)
	}
	if st := p.Stats(); st.Groups <= 0 || st.Groups > 2 {
		t.Fatalf("material groups after the blur = %d, want between 1 and the cap of 2", st.Groups)
	}
}

func TestKernelBudgetDegradesQuality(t *testing.T) {
	p := preparedPipeline(t, 3, 3)
	if err := p.SetQuality(0); err != nil {
		t.Fatalf("SetQuality must succeed, got %v", err)
	}
	if err := p.SetKernelBudget(200); err != nil {
		t.Fatalf("SetKernelBudget must succeed, got %v", err)
	}
	out, err := p.Blur(uniformImage(3, 3, impl.RGB{R: 1}), uniformDepth(3, 3, 1))
	if err != nil {
		t.Fatalf("Blur must degrade instead of failing, got %v", err)
	}
	if len(out.Pixels) != 9 {
		t.Fatalf("output pixels = %d, want 9", len(out.Pixels))
	}
	st := p.Stats()
	if st.Degrades < 1 {
		t.Fatalf("degrade count = %d, want at least one step", st.Degrades)
	}
	if st.Quality != impl.QualityLevels-1 {
		t.Fatalf("quality after degrading = %d, want the lowest level %d", st.Quality, impl.QualityLevels-1)
	}
	if st.KernelRadius != impl.KernelRadii[impl.QualityLevels-1] {
		t.Fatalf("kernel radius after degrading = %d, want %d", st.KernelRadius, impl.KernelRadii[impl.QualityLevels-1])
	}
	if st.Samples > 200 {
		t.Fatalf("samples after degrading = %d, want the budget of 200 to be respected", st.Samples)
	}
}

func TestKernelBudgetImpossibleFails(t *testing.T) {
	p := preparedPipeline(t, 3, 3)
	if err := p.SetKernelBudget(0); !errors.Is(err, impl.ErrBadBudget) {
		t.Fatalf("SetKernelBudget(0) = %v, want ErrBadBudget", err)
	}
	if err := p.SetKernelBudget(1); err != nil {
		t.Fatalf("SetKernelBudget(1) must succeed, got %v", err)
	}
	if _, err := p.Blur(uniformImage(3, 3, impl.RGB{R: 1}), uniformDepth(3, 3, 1)); !errors.Is(err, impl.ErrFrameBudget) {
		t.Fatalf("Blur under an impossible budget = %v, want ErrFrameBudget", err)
	}
}

func TestSnapshotRestoreKeepsProfilesAndQuality(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(testLimits())
	if err := p.RegisterProfile("skin", skinProfile(3, 2)); err != nil {
		t.Fatalf("RegisterProfile must succeed, got %v", err)
	}
	if err := p.SetQuality(2); err != nil {
		t.Fatalf("SetQuality must succeed, got %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot must succeed, got %v", err)
	}
	other := impl.NewSubsurfaceScatteringPipeline(testLimits())
	if err := other.Restore(data); err != nil {
		t.Fatalf("Restore must succeed, got %v", err)
	}
	if got := other.ProfileCount(); got != 1 {
		t.Fatalf("profile count after Restore = %d, want 1", got)
	}
	profile, err := other.SampleProfile(3)
	if err != nil {
		t.Fatalf("SampleProfile after Restore must succeed, got %v", err)
	}
	if profile.Radius != 2 {
		t.Fatalf("restored profile radius = %v, want 2", profile.Radius)
	}
	st := other.Stats()
	if st.Quality != 2 {
		t.Fatalf("restored quality = %d, want 2", st.Quality)
	}
	if st.KernelRadius != impl.KernelRadii[2] {
		t.Fatalf("restored kernel radius = %d, want %d", st.KernelRadius, impl.KernelRadii[2])
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	p := impl.NewSubsurfaceScatteringPipeline(testLimits())
	if err := p.Restore([]byte("not a scattering snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("Restore of a corrupt payload = %v, want ErrBadSnapshot", err)
	}
	if got := p.ProfileCount(); got != 0 {
		t.Fatalf("profile count after a failed Restore = %d, want 0", got)
	}
	if err := p.RegisterProfile("skin", skinProfile(1, 1)); err != nil {
		t.Fatalf("pipeline must stay usable after a failed Restore, got %v", err)
	}
}
