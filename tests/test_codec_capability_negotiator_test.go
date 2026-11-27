package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-game-c-13"
)

func tvCaps() []impl.Capability {
	return []impl.Capability{
		{Codec: impl.CodecH264, Profile: "main", Level: 41, MaxWidth: 1920, MaxHeight: 1080, Hardware: true},
		{Codec: impl.CodecH264, Profile: "baseline", Level: 31, MaxWidth: 1280, MaxHeight: 720, Hardware: true},
		{Codec: impl.CodecHEVC, Profile: "main", Level: 51, MaxWidth: 3840, MaxHeight: 2160, HDR: true, Hardware: true},
	}
}

func phoneCaps() []impl.Capability {
	return []impl.Capability{
		{Codec: impl.CodecH264, Profile: "baseline", Level: 31, MaxWidth: 1280, MaxHeight: 720, Hardware: true},
	}
}

func tvRequest(w, h int) impl.PlaybackReq {
	return impl.PlaybackReq{Device: "tv", Codec: impl.CodecH264, Width: w, Height: h}
}

func TestDefaultsAndLadder(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if got := n.ProbeTTL(); got != impl.DefaultProbeTTL {
		t.Fatalf("default probe ttl = %v, want %v", got, impl.DefaultProbeTTL)
	}
	ladder := n.Ladder()
	want := impl.DefaultLadder()
	if len(want) != 3 {
		t.Fatalf("default ladder length = %d, want 3", len(want))
	}
	if want[0].Profile != "main" || want[0].Level != 41 || !want[0].Hardware {
		t.Fatalf("first rung = %+v, want main level 41 on hardware", want[0])
	}
	if want[2].Profile != "baseline" || want[2].Level != 30 || want[2].Hardware {
		t.Fatalf("last rung = %+v, want the software baseline rung", want[2])
	}
	if len(ladder) != len(want) {
		t.Fatalf("ladder in force = %d rungs, want %d", len(ladder), len(want))
	}
	for i := range want {
		if ladder[i] != want[i] {
			t.Fatalf("rung %d = %+v, want %+v", i, ladder[i], want[i])
		}
	}
	sw := impl.SoftwareRung()
	if sw.Hardware || sw.Profile != "baseline" || sw.Level != 30 {
		t.Fatalf("software rung = %+v, want baseline level 30 without hardware", sw)
	}
}

func TestRegisterDeviceValidation(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("", tvCaps()); !errors.Is(err, impl.ErrBadDevice) {
		t.Fatalf("empty device id must fail with ErrBadDevice, got %v", err)
	}
	if err := n.RegisterDevice("empty", nil); !errors.Is(err, impl.ErrBadDevice) {
		t.Fatalf("an empty capability list must fail with ErrBadDevice, got %v", err)
	}
	badCodec := []impl.Capability{{Codec: impl.Codec("vp8"), Profile: "main", Level: 40, MaxWidth: 640, MaxHeight: 480}}
	if err := n.RegisterDevice("bad", badCodec); !errors.Is(err, impl.ErrBadCodec) {
		t.Fatalf("an unknown codec must fail with ErrBadCodec, got %v", err)
	}
	badLevel := []impl.Capability{{Codec: impl.CodecH264, Profile: "main", Level: 0, MaxWidth: 640, MaxHeight: 480}}
	if err := n.RegisterDevice("bad", badLevel); !errors.Is(err, impl.ErrBadCapability) {
		t.Fatalf("a zero level must fail with ErrBadCapability, got %v", err)
	}
	if _, err := n.Probe("bad", "h264"); !errors.Is(err, impl.ErrProbeFailed) {
		t.Fatalf("a device whose registration failed must stay unknown, got %v", err)
	}

	single := impl.NewCodecCapabilityNegotiator(impl.Limits{MaxDevices: 1})
	if err := single.RegisterDevice("one", tvCaps()); err != nil {
		t.Fatalf("first device must be accepted, got %v", err)
	}
	if err := single.RegisterDevice("two", tvCaps()); !errors.Is(err, impl.ErrDeviceLimit) {
		t.Fatalf("a second device must hit ErrDeviceLimit, got %v", err)
	}

	narrow := impl.NewCodecCapabilityNegotiator(impl.Limits{MaxCaps: 1})
	if err := narrow.RegisterDevice("tv", tvCaps()); !errors.Is(err, impl.ErrCapabilityLimit) {
		t.Fatalf("three capabilities against a cap of one must fail with ErrCapabilityLimit, got %v", err)
	}
}

func TestProbeReturnsHighestLevelCapability(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	cap, err := n.Probe("tv", "h264")
	if err != nil {
		t.Fatalf("probe must succeed, got %v", err)
	}
	if cap.Profile != "main" || cap.Level != 41 {
		t.Fatalf("probed capability = %+v, want the highest level h264 entry", cap)
	}
	if cap.MaxWidth != 1920 || cap.MaxHeight != 1080 || !cap.Hardware {
		t.Fatalf("probed capability = %+v, want the 1080p hardware entry", cap)
	}
	hevc, err := n.Probe("tv", "hevc")
	if err != nil {
		t.Fatalf("hevc probe: %v", err)
	}
	if hevc.Codec != impl.CodecHEVC || hevc.Level != 51 || !hevc.HDR {
		t.Fatalf("hevc capability = %+v, want the 2160p hdr entry", hevc)
	}
	if st := n.Stats(); st.Probes != 2 {
		t.Fatalf("probe count = %d, want 2 distinct codec probes", st.Probes)
	}
}

func TestProbeCachesWithinTTL(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	first, err := n.Probe("tv", "h264")
	if err != nil {
		t.Fatalf("first probe: %v", err)
	}
	second, err := n.Probe("tv", "h264")
	if err != nil {
		t.Fatalf("second probe: %v", err)
	}
	if first != second {
		t.Fatalf("cached probe = %+v, want the first result %+v", second, first)
	}
	st := n.Stats()
	if st.Probes != 1 {
		t.Fatalf("probe count = %d, want 1 while the cache is warm", st.Probes)
	}
	if st.CacheHits != 1 {
		t.Fatalf("cache hits = %d, want 1", st.CacheHits)
	}
}

func TestInvalidateForcesReprobe(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := n.Probe("tv", "h264"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if err := n.Invalidate("tv"); err != nil {
		t.Fatalf("invalidating a probed device must succeed, got %v", err)
	}
	if _, err := n.Probe("tv", "h264"); err != nil {
		t.Fatalf("probe after invalidate: %v", err)
	}
	st := n.Stats()
	if st.Probes != 2 {
		t.Fatalf("probe count = %d, want a fresh probe after invalidation", st.Probes)
	}
	if st.Invalidations != 1 {
		t.Fatalf("invalidation count = %d, want 1", st.Invalidations)
	}
	if err := n.Invalidate("ghost"); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("invalidating a device without cache must fail with ErrUnknownDevice, got %v", err)
	}
}

func TestSetProbeTTLValidation(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.SetProbeTTL(0); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("a zero ttl must fail with ErrBadTTL, got %v", err)
	}
	if err := n.SetProbeTTL(-time.Second); !errors.Is(err, impl.ErrBadTTL) {
		t.Fatalf("a negative ttl must fail with ErrBadTTL, got %v", err)
	}
	if got := n.ProbeTTL(); got != impl.DefaultProbeTTL {
		t.Fatalf("ttl after rejected changes = %v, want %v", got, impl.DefaultProbeTTL)
	}
	if err := n.SetProbeTTL(100 * time.Millisecond); err != nil {
		t.Fatalf("a positive ttl must be accepted, got %v", err)
	}
	if got := n.ProbeTTL(); got != 100*time.Millisecond {
		t.Fatalf("ttl = %v, want 100ms", got)
	}
}

func TestProbeRejectsUnknownCodecAndDevice(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := n.Probe("tv", "vp8"); !errors.Is(err, impl.ErrBadCodec) {
		t.Fatalf("an unknown codec must fail with ErrBadCodec, got %v", err)
	}
	if _, err := n.Probe("ghost", "h264"); !errors.Is(err, impl.ErrProbeFailed) {
		t.Fatalf("an unregistered device must fail with ErrProbeFailed, got %v", err)
	}
	if _, err := n.Probe("tv", "av1"); !errors.Is(err, impl.ErrProbeFailed) {
		t.Fatalf("a registered device without the codec must fail with ErrProbeFailed, got %v", err)
	}
}

func TestNegotiateExactRung(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	d, err := n.Negotiate(tvRequest(1920, 1080))
	if err != nil {
		t.Fatalf("negotiate must succeed, got %v", err)
	}
	if d.Step != 0 || d.Software || d.Degraded {
		t.Fatalf("decision step=%d software=%v degraded=%v, want the first hardware rung", d.Step, d.Software, d.Degraded)
	}
	if d.Code != impl.CodeExact {
		t.Fatalf("decision code = %q, want %q", d.Code, impl.CodeExact)
	}
	if d.Codec != impl.CodecH264 || d.Profile != "main" || d.Level != 41 {
		t.Fatalf("decision = %+v, want h264 main level 41", d)
	}
	if d.Width != 1920 || d.Height != 1080 || d.Device != "tv" {
		t.Fatalf("decision echoed %dx%d for %q, want the request values", d.Width, d.Height, d.Device)
	}
	st := n.Stats()
	if st.Negotiations != 1 || st.Probes != 1 {
		t.Fatalf("stats negotiations=%d probes=%d, want 1 and 1", st.Negotiations, st.Probes)
	}
}

func TestNegotiateStepsDownLadder(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("phone", phoneCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	d, err := n.Negotiate(impl.PlaybackReq{Device: "phone", Codec: impl.CodecH264, Width: 1280, Height: 720})
	if err != nil {
		t.Fatalf("negotiate must succeed, got %v", err)
	}
	if d.Step != 2 {
		t.Fatalf("decision step = %d, want the software rung index 2", d.Step)
	}
	if !d.Software || !d.Degraded {
		t.Fatalf("decision software=%v degraded=%v, want both true after a step down", d.Software, d.Degraded)
	}
	if d.Code != impl.CodeStepDown {
		t.Fatalf("decision code = %q, want %q", d.Code, impl.CodeStepDown)
	}
	if d.Profile != "baseline" || d.Level != 30 {
		t.Fatalf("decision = %+v, want baseline level 30", d)
	}
	if st := n.Stats(); st.Degraded != 1 {
		t.Fatalf("degraded count = %d, want 1", st.Degraded)
	}
}

func TestNegotiateSoftwareFallback(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	d, err := n.Negotiate(tvRequest(2560, 1440))
	if err != nil {
		t.Fatalf("a request above every rung must still return a decision, got %v", err)
	}
	if d.Step != -1 {
		t.Fatalf("fallback step = %d, want -1", d.Step)
	}
	if !d.Software || !d.Degraded {
		t.Fatalf("fallback decision software=%v degraded=%v, want both true", d.Software, d.Degraded)
	}
	if d.Code != impl.CodeSoftware {
		t.Fatalf("fallback code = %q, want %q", d.Code, impl.CodeSoftware)
	}
	if d.Profile != "baseline" || d.Level != 30 || d.Codec != impl.CodecH264 {
		t.Fatalf("fallback decision = %+v, want the software h264 baseline rung", d)
	}
	if st := n.Stats(); st.Software != 1 {
		t.Fatalf("software fallback count = %d, want 1", st.Software)
	}
}

func TestNegotiateUnregisteredDeviceFallsBack(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	d, err := n.Negotiate(impl.PlaybackReq{Device: "ghost", Codec: impl.CodecH264, Width: 1280, Height: 720})
	if err != nil {
		t.Fatalf("an unprobeable device must not be fatal, got %v", err)
	}
	if d.Code != impl.CodeSoftware || !d.Software || d.Step != -1 {
		t.Fatalf("decision = %+v, want the software fallback for an unknown device", d)
	}
	if d.Device != "ghost" || d.Width != 1280 || d.Height != 720 {
		t.Fatalf("decision = %+v, want it to echo the request", d)
	}
}

func TestNegotiateRejectsBadRequest(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := n.Negotiate(impl.PlaybackReq{Device: "tv", Codec: impl.CodecH264, Width: 0, Height: 720}); !errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("a zero width request must fail with ErrBadRequest, got %v", err)
	}
	if _, err := n.Negotiate(impl.PlaybackReq{Device: "tv", Codec: impl.CodecH264, Width: 640, Height: 0}); !errors.Is(err, impl.ErrBadRequest) {
		t.Fatalf("a zero height request must fail with ErrBadRequest, got %v", err)
	}
	if _, err := n.Negotiate(impl.PlaybackReq{Device: "tv", Codec: impl.Codec("vp8"), Width: 640, Height: 480}); !errors.Is(err, impl.ErrBadCodec) {
		t.Fatalf("an unknown codec request must fail with ErrBadCodec, got %v", err)
	}
	if st := n.Stats(); st.Negotiations != 0 {
		t.Fatalf("rejected requests must not be counted, negotiations=%d", st.Negotiations)
	}
}

func TestExplainDecision(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	exact, err := n.Negotiate(tvRequest(1920, 1080))
	if err != nil {
		t.Fatalf("negotiate exact: %v", err)
	}
	lines := n.Explain(exact)
	if len(lines) != 2 {
		t.Fatalf("explanation = %v, want two lines", lines)
	}
	if lines[0] != impl.Reason("rung=0") {
		t.Fatalf("first line = %q, want rung=0", lines[0])
	}
	if lines[1] != impl.Reason("source=hw") {
		t.Fatalf("second line = %q, want source=hw", lines[1])
	}
	fallback, err := n.Negotiate(tvRequest(2560, 1440))
	if err != nil {
		t.Fatalf("negotiate fallback: %v", err)
	}
	swLines := n.Explain(fallback)
	if len(swLines) != 2 || swLines[0] != impl.Reason("rung=-1") || swLines[1] != impl.Reason("source=sw") {
		t.Fatalf("fallback explanation = %v, want rung=-1 and source=sw", swLines)
	}
}

func TestSnapshotRestoreKeepsCache(t *testing.T) {
	limits := impl.Limits{MaxDevices: 4, MaxCaps: 8}
	n := impl.NewCodecCapabilityNegotiator(limits)
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := n.SetProbeTTL(5 * time.Second); err != nil {
		t.Fatalf("set ttl: %v", err)
	}
	if _, err := n.Probe("tv", "h264"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	want, err := n.Negotiate(tvRequest(1920, 1080))
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	data, err := n.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	r := impl.NewCodecCapabilityNegotiator(limits)
	if err := r.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := r.ProbeTTL(); got != 5*time.Second {
		t.Fatalf("restored ttl = %v, want 5s", got)
	}
	got, err := r.Negotiate(tvRequest(1920, 1080))
	if err != nil {
		t.Fatalf("negotiate after restore: %v", err)
	}
	if got.Step != want.Step || got.Profile != want.Profile || got.Code != want.Code {
		t.Fatalf("decision after restore = %+v, want %+v", got, want)
	}
	if st := r.Stats(); st.Probes != 1 {
		t.Fatalf("probe count after restore = %d, want the cached 1", st.Probes)
	}
	if err := r.Restore([]byte("<<broken>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if st := r.Stats(); st.Probes != 1 || st.Negotiations != 1 {
		t.Fatalf("a rejected restore must not touch the live state, got %+v", st)
	}
}

func TestConcurrentProbeCoalesces(t *testing.T) {
	n := impl.NewCodecCapabilityNegotiator(impl.Limits{})
	if err := n.RegisterDevice("tv", tvCaps()); err != nil {
		t.Fatalf("register: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	profiles := map[string]int{}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cap, err := n.Probe("tv", "h264")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				return
			}
			profiles[cap.Profile]++
		}()
	}
	wg.Wait()
	if failed != 0 {
		t.Fatalf("%d concurrent probes failed, want 0", failed)
	}
	if len(profiles) != 1 || profiles["main"] != 16 {
		t.Fatalf("probe results = %v, want 16 identical main profile answers", profiles)
	}
	if st := n.Stats(); st.Probes != 1 {
		t.Fatalf("probe count = %d, want the 16 concurrent callers to share one probe", st.Probes)
	}
}
