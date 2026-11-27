package task

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-game-b-10"
)

func shakeConfig() impl.ShakeConfig {
	return impl.ShakeConfig{
		MaxAmplitude:     1,
		DecayPerSecond:   0.5,
		Frequency:        18,
		SuppressedWeight: 0.25,
		FirstPersonScale: 0.5,
		ThirdPersonScale: 1,
		MaxSources:       16,
	}
}

func shakeCamera() impl.Camera {
	return impl.Camera{Pos: impl.Vec3{Y: 2}, Distance: 4}
}

func baseInstant() time.Time {
	return time.Unix(1_700_000_000, 0)
}

func TestAddTraumaRejectsBlankSource(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	if err := d.AddTrauma("   ", 0.5, 1); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("a blank source must fail with ErrBadSource, got %v", err)
	}
	if err := d.AddTrauma("", 0.5, 1); !errors.Is(err, impl.ErrBadSource) {
		t.Fatalf("an empty source must fail with ErrBadSource, got %v", err)
	}
	if got := len(d.Sources()); got != 0 {
		t.Fatalf("rejected injections registered %d sources, want 0", got)
	}
}

func TestAddTraumaRejectsBadAmount(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	for _, amount := range []float64{0, -0.5, math.NaN(), math.Inf(1)} {
		if err := d.AddTrauma("boom", amount, 1); !errors.Is(err, impl.ErrBadAmount) {
			t.Fatalf("amount %v must fail with ErrBadAmount, got %v", amount, err)
		}
	}
	if _, ok := d.Amplitude("boom"); ok {
		t.Fatalf("a rejected amount must not register the source")
	}
}

func TestAddTraumaRejectsNegativePriority(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	if err := d.AddTrauma("boom", 0.5, -1); !errors.Is(err, impl.ErrBadPriority) {
		t.Fatalf("a negative priority must fail with ErrBadPriority, got %v", err)
	}
	if err := d.AddTrauma("boom", 0.5, 0); err != nil {
		t.Fatalf("priority zero must be accepted, got %v", err)
	}
}

func TestAddTraumaEnforcesSourceLimit(t *testing.T) {
	cfg := shakeConfig()
	cfg.MaxSources = 2
	d := impl.NewShakeDirector(cfg)
	for _, source := range []string{"a", "b"} {
		if err := d.AddTrauma(source, 0.2, 1); err != nil {
			t.Fatalf("add source %q: %v", source, err)
		}
	}
	if err := d.AddTrauma("c", 0.2, 1); !errors.Is(err, impl.ErrSourceLimit) {
		t.Fatalf("a third source against a limit of two must fail with ErrSourceLimit, got %v", err)
	}
	if got := len(d.Sources()); got != 2 {
		t.Fatalf("source table holds %d layers, want 2", got)
	}
	if err := d.AddTrauma("a", 0.2, 1); err != nil {
		t.Fatalf("an existing source must still accept trauma at the cap: %v", err)
	}
}

func TestTraumaCappedAtOne(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	if err := d.AddTrauma("boom", 0.8, 1); err != nil {
		t.Fatalf("first injection: %v", err)
	}
	if err := d.AddTrauma("boom", 0.8, 1); err != nil {
		t.Fatalf("second injection: %v", err)
	}
	amplitude, ok := d.Amplitude("boom")
	if !ok {
		t.Fatalf("the source must be registered after two injections")
	}
	if math.Abs(amplitude-1) > 1e-9 {
		t.Fatalf("amplitude with a capped trauma = %v, want MaxAmplitude 1", amplitude)
	}
}

func TestAmplitudeUsesSquaredTrauma(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	if err := d.AddTrauma("boom", 0.5, 1); err != nil {
		t.Fatalf("inject trauma: %v", err)
	}
	amplitude, ok := d.Amplitude("boom")
	if !ok {
		t.Fatalf("the source must be registered")
	}
	if math.Abs(amplitude-0.25) > 1e-9 {
		t.Fatalf("amplitude for trauma 0.5 = %v, want 0.25 from the squared mapping", amplitude)
	}
	if _, ok := d.Amplitude("ghost"); ok {
		t.Fatalf("an unregistered source must not report an amplitude")
	}
}

func TestSourcesSortedAndRemove(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	if err := d.AddTrauma("b", 0.2, 1); err != nil {
		t.Fatalf("add b: %v", err)
	}
	if err := d.AddTrauma("a", 0.2, 2); err != nil {
		t.Fatalf("add a: %v", err)
	}
	sources := d.Sources()
	if len(sources) != 2 || sources[0] != "a" || sources[1] != "b" {
		t.Fatalf("sources = %v, want [a b] in ascending order", sources)
	}
	if err := d.RemoveSource("a"); err != nil {
		t.Fatalf("remove a: %v", err)
	}
	sources = d.Sources()
	if len(sources) != 1 || sources[0] != "b" {
		t.Fatalf("sources after the removal = %v, want [b]", sources)
	}
	if err := d.RemoveSource("a"); !errors.Is(err, impl.ErrUnknownSource) {
		t.Fatalf("removing an unknown source must fail with ErrUnknownSource, got %v", err)
	}
}

func TestSampleWithNoTraumaIsZero(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	offset, err := d.Sample(shakeCamera(), baseInstant())
	if err != nil {
		t.Fatalf("sample without sources: %v", err)
	}
	if offset.X != 0 || offset.Y != 0 || offset.Z != 0 || offset.Roll != 0 {
		t.Fatalf("offset without trauma = %+v, want a zero offset", offset)
	}
}

func TestSampleDeterministicForSameInstant(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	if err := d.AddTrauma("boom", 1, 1); err != nil {
		t.Fatalf("inject trauma: %v", err)
	}
	at := baseInstant()
	first, err := d.Sample(shakeCamera(), at)
	if err != nil {
		t.Fatalf("first sample: %v", err)
	}
	second, err := d.Sample(shakeCamera(), at)
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if first != second {
		t.Fatalf("two samples at the same instant differ: %+v vs %+v", first, second)
	}
	if first.X == 0 && first.Y == 0 && first.Z == 0 && first.Roll == 0 {
		t.Fatalf("a fully traumatised source must produce a non zero offset")
	}
}

func TestPauseFreezesPhase(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	if err := d.AddTrauma("boom", 1, 1); err != nil {
		t.Fatalf("inject trauma: %v", err)
	}
	at := baseInstant()
	before, err := d.Sample(shakeCamera(), at)
	if err != nil {
		t.Fatalf("sample before the pause: %v", err)
	}
	d.SetPaused(true)
	if !d.Paused() {
		t.Fatalf("Paused must report true after SetPaused(true)")
	}
	frozen, err := d.Sample(shakeCamera(), at.Add(5*time.Second))
	if err != nil {
		t.Fatalf("sample during the pause: %v", err)
	}
	if frozen != before {
		t.Fatalf("the offset moved during the pause: %+v vs %+v", frozen, before)
	}
	d.SetPaused(false)
	after, err := d.Sample(shakeCamera(), at.Add(6*time.Second))
	if err != nil {
		t.Fatalf("sample after the resume: %v", err)
	}
	if after == before {
		t.Fatalf("the phase must resume from the frozen instant, got the same offset %+v", after)
	}
}

func TestDecayReducesTrauma(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	if err := d.AddTrauma("boom", 0.8, 1); err != nil {
		t.Fatalf("inject trauma: %v", err)
	}
	if err := d.Decay(-time.Second); !errors.Is(err, impl.ErrBadDuration) {
		t.Fatalf("a negative decay duration must fail with ErrBadDuration, got %v", err)
	}
	if err := d.Decay(time.Second); err != nil {
		t.Fatalf("decay for one second: %v", err)
	}
	amplitude, ok := d.Amplitude("boom")
	if !ok {
		t.Fatalf("the source must stay registered while it decays")
	}
	if math.Abs(amplitude-0.09) > 1e-9 {
		t.Fatalf("amplitude after a one second decay = %v, want 0.3 squared = 0.09", amplitude)
	}
	if err := d.Decay(10 * time.Second); err != nil {
		t.Fatalf("decay far past zero: %v", err)
	}
	amplitude, _ = d.Amplitude("boom")
	if amplitude != 0 {
		t.Fatalf("amplitude after a long decay = %v, want 0", amplitude)
	}
}

func TestSetCameraModeRescalesAmplitude(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	if err := d.AddTrauma("boom", 0.5, 1); err != nil {
		t.Fatalf("inject trauma: %v", err)
	}
	third, ok := d.Amplitude("boom")
	if !ok {
		t.Fatalf("the source must be registered")
	}
	if math.Abs(third-0.25) > 1e-9 {
		t.Fatalf("third person amplitude = %v, want 0.25", third)
	}
	if err := d.SetCameraMode(impl.ModeFirstPerson); err != nil {
		t.Fatalf("switch to first person: %v", err)
	}
	first, ok := d.Amplitude("boom")
	if !ok {
		t.Fatalf("switching the camera mode must keep the shake layers")
	}
	if math.Abs(first-0.125) > 1e-9 {
		t.Fatalf("first person amplitude = %v, want 0.25 * 0.5 = 0.125", first)
	}
	if got := len(d.Sources()); got != 1 {
		t.Fatalf("source count after the mode switch = %d, want 1", got)
	}
	if err := d.SetCameraMode("orbit"); !errors.Is(err, impl.ErrBadMode) {
		t.Fatalf("an unknown camera mode must fail with ErrBadMode, got %v", err)
	}
}

func TestSampleRespectsMaxAmplitude(t *testing.T) {
	cfg := shakeConfig()
	cfg.MaxAmplitude = 0.6
	d := impl.NewShakeDirector(cfg)
	if err := d.AddTrauma("impact", 1, 5); err != nil {
		t.Fatalf("inject impact trauma: %v", err)
	}
	if err := d.AddTrauma("rumble", 1, 1); err != nil {
		t.Fatalf("inject rumble trauma: %v", err)
	}
	at := baseInstant()
	for i := 0; i < 200; i++ {
		offset, err := d.Sample(shakeCamera(), at.Add(time.Duration(i)*10*time.Millisecond))
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		for name, value := range map[string]float64{"x": offset.X, "y": offset.Y, "z": offset.Z, "roll": offset.Roll} {
			if math.Abs(value) > 0.6+1e-9 {
				t.Fatalf("sample %d component %s = %v, want it inside the 0.6 ceiling", i, name, value)
			}
		}
	}
}

func TestConcurrentAddTraumaAndSample(t *testing.T) {
	d := impl.NewShakeDirector(shakeConfig())
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if err := d.AddTrauma("rumble", 0.01, 1); err != nil {
					errs <- err
					return
				}
				if _, err := d.Sample(shakeCamera(), baseInstant()); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent trauma injection: %v", err)
	}
	amplitude, ok := d.Amplitude("rumble")
	if !ok {
		t.Fatalf("the rumble source must be registered")
	}
	if math.Abs(amplitude-1) > 1e-9 {
		t.Fatalf("amplitude after four hundred injections = %v, want the trauma cap of 1", amplitude)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	cfg := shakeConfig()
	d := impl.NewShakeDirector(cfg)
	if err := d.AddTrauma("boom", 0.5, 3); err != nil {
		t.Fatalf("inject trauma: %v", err)
	}
	if err := d.SetCameraMode(impl.ModeFirstPerson); err != nil {
		t.Fatalf("switch the camera mode: %v", err)
	}
	before, ok := d.Amplitude("boom")
	if !ok {
		t.Fatalf("the source must be registered before the snapshot")
	}
	data, err := d.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewShakeDirector(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after, ok := restored.Amplitude("boom")
	if !ok {
		t.Fatalf("the restored director lost the shake layer")
	}
	if math.Abs(after-before) > 1e-12 {
		t.Fatalf("amplitude after the restore = %v, want %v including the first person baseline", after, before)
	}
	if got := len(restored.Sources()); got != 1 {
		t.Fatalf("source count after the restore = %d, want 1", got)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	kept, _ := restored.Amplitude("boom")
	if math.Abs(kept-before) > 1e-12 {
		t.Fatalf("a rejected restore changed the amplitude to %v, want %v", kept, before)
	}
}
