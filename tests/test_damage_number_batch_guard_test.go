package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-r-07"
)

func newDamageGuard(t *testing.T, cfg impl.BatchConfig) *impl.DamageNumberBatchGuard {
	t.Helper()
	g, err := impl.NewDamageNumberBatchGuard(cfg)
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	return g
}

func baseDamageConfig() impl.BatchConfig {
	return impl.BatchConfig{
		FrameRate:  30,
		LifeFrames: 5,
		MaxPending: 64,
		MaxTexts:   32,
		MaxAudit:   8,
	}
}

func hitEvent(seq, frame int64, target string, amount int64) impl.DamageEvent {
	return impl.DamageEvent{
		Seq:      seq,
		Frame:    frame,
		TargetID: target,
		SourceID: "hero-1",
		Amount:   amount,
		Element:  impl.ElementPhysical,
	}
}

func TestNewGuardValidatesConfig(t *testing.T) {
	bad := []impl.BatchConfig{
		{FrameRate: 0, LifeFrames: 5, MaxPending: 8, MaxTexts: 8, MaxAudit: 8},
		{FrameRate: 30, LifeFrames: 0, MaxPending: 8, MaxTexts: 8, MaxAudit: 8},
		{FrameRate: 30, LifeFrames: 5, MaxPending: 0, MaxTexts: 8, MaxAudit: 8},
		{FrameRate: 30, LifeFrames: 5, MaxPending: 8, MaxTexts: 0, MaxAudit: 8},
		{FrameRate: 30, LifeFrames: 5, MaxPending: 8, MaxTexts: 8, MaxAudit: 0},
	}
	for i, cfg := range bad {
		if _, err := impl.NewDamageNumberBatchGuard(cfg); !errors.Is(err, impl.ErrBadConfig) {
			t.Fatalf("config %d must fail with ErrBadConfig, got %v", i, err)
		}
	}
	g := newDamageGuard(t, baseDamageConfig())
	if got := g.PlaybackSpeed(); got != 1000 {
		t.Fatalf("default playback speed = %d, want 1000", got)
	}
	if st := g.Stats(); st != (impl.GuardStats{}) {
		t.Fatalf("fresh stats = %+v, want all counters zero", st)
	}
	if got := len(g.Live()); got != 0 {
		t.Fatalf("fresh live texts = %d, want 0", got)
	}
	if got := len(g.PendingFrames()); got != 0 {
		t.Fatalf("fresh pending frames = %d, want 0", got)
	}
}

func TestSubmitValidatesEvent(t *testing.T) {
	g := newDamageGuard(t, baseDamageConfig())
	cases := []struct {
		name string
		ev   impl.DamageEvent
		want error
	}{
		{"zero seq", impl.DamageEvent{Seq: 0, Frame: 1, TargetID: "mob-1", Amount: 10, Element: impl.ElementFire}, impl.ErrBadEvent},
		{"negative frame", impl.DamageEvent{Seq: 1, Frame: -1, TargetID: "mob-1", Amount: 10, Element: impl.ElementFire}, impl.ErrBadEvent},
		{"blank target", impl.DamageEvent{Seq: 1, Frame: 1, TargetID: "  ", Amount: 10, Element: impl.ElementFire}, impl.ErrBadEvent},
		{"zero amount", impl.DamageEvent{Seq: 1, Frame: 1, TargetID: "mob-1", Amount: 0, Element: impl.ElementFire}, impl.ErrBadEvent},
		{"unknown element", impl.DamageEvent{Seq: 1, Frame: 1, TargetID: "mob-1", Amount: 10, Element: "arcane"}, impl.ErrUnknownElement},
		{"blank element", impl.DamageEvent{Seq: 1, Frame: 1, TargetID: "mob-1", Amount: 10}, impl.ErrUnknownElement},
	}
	for _, tc := range cases {
		if err := g.Submit(tc.ev); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	if got := len(g.PendingFrames()); got != 0 {
		t.Fatalf("rejected events left %d pending frames, want 0", got)
	}
	if err := g.Submit(hitEvent(1, 7, "mob-1", 10)); err != nil {
		t.Fatalf("submit a valid event: %v", err)
	}
	pending := g.PendingFrames()
	if len(pending) != 1 || pending[0] != 7 {
		t.Fatalf("pending frames = %v, want [7]", pending)
	}
}

func TestBatchMergesSameTargetInFrame(t *testing.T) {
	g := newDamageGuard(t, baseDamageConfig())
	if err := g.Submit(hitEvent(1, 7, "mob-a", 30)); err != nil {
		t.Fatalf("submit first hit: %v", err)
	}
	if err := g.Submit(hitEvent(2, 7, "mob-a", 12)); err != nil {
		t.Fatalf("submit second hit: %v", err)
	}
	if err := g.Submit(hitEvent(3, 7, "mob-b", 5)); err != nil {
		t.Fatalf("submit other target: %v", err)
	}
	texts, err := g.BatchFrame(7)
	if err != nil {
		t.Fatalf("batch frame 7: %v", err)
	}
	if len(texts) != 2 {
		t.Fatalf("batched texts = %d, want 2", len(texts))
	}
	if texts[0].TargetID != "mob-a" || texts[0].Amount != 42 || texts[0].Hits != 2 {
		t.Fatalf("merged text = %+v, want mob-a with 42 over 2 hits", texts[0])
	}
	if texts[1].TargetID != "mob-b" || texts[1].Amount != 5 || texts[1].Hits != 1 {
		t.Fatalf("second text = %+v, want mob-b with 5 over 1 hit", texts[1])
	}
	st := g.Stats()
	if st.Submitted != 3 || st.Batched != 2 || st.Merged != 1 {
		t.Fatalf("stats = %+v, want submitted 3, batched 2, merged 1", st)
	}
}

func TestBatchOrdersTargetsAndExportsFrames(t *testing.T) {
	g := newDamageGuard(t, baseDamageConfig())
	for i, ev := range []impl.DamageEvent{
		hitEvent(1, 3, "mob-b", 7),
		hitEvent(2, 3, "mob-a", 9),
		hitEvent(3, 5, "mob-c", 11),
	} {
		if err := g.Submit(ev); err != nil {
			t.Fatalf("submit event %d: %v", i, err)
		}
	}
	first, err := g.BatchFrame(3)
	if err != nil {
		t.Fatalf("batch frame 3: %v", err)
	}
	if len(first) != 2 || first[0].TargetID != "mob-a" || first[1].TargetID != "mob-b" {
		t.Fatalf("frame 3 texts = %+v, want mob-a then mob-b", first)
	}
	if _, err := g.BatchFrame(5); err != nil {
		t.Fatalf("batch frame 5: %v", err)
	}
	replay := g.ExportReplay()
	if len(replay) != 2 {
		t.Fatalf("replay frames = %d, want 2", len(replay))
	}
	if replay[0].Frame != 3 || replay[1].Frame != 5 {
		t.Fatalf("replay frame order = %d,%d, want 3,5", replay[0].Frame, replay[1].Frame)
	}
	if len(replay[0].Texts) != 2 || len(replay[1].Texts) != 1 {
		t.Fatalf("replay text counts = %d,%d, want 2,1", len(replay[0].Texts), len(replay[1].Texts))
	}
	if got := len(g.PendingFrames()); got != 0 {
		t.Fatalf("pending frames after batching = %d, want 0", got)
	}
}

func TestClosedFrameRejectsResubmission(t *testing.T) {
	g := newDamageGuard(t, baseDamageConfig())
	if err := g.Submit(hitEvent(1, 2, "mob-a", 10)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := g.BatchFrame(2); err != nil {
		t.Fatalf("batch frame 2: %v", err)
	}
	if _, err := g.BatchFrame(2); !errors.Is(err, impl.ErrFrameClosed) {
		t.Fatalf("rebatching frame 2 must fail with ErrFrameClosed, got %v", err)
	}
	if err := g.Submit(hitEvent(2, 2, "mob-b", 10)); !errors.Is(err, impl.ErrFrameClosed) {
		t.Fatalf("submitting into a closed frame must fail with ErrFrameClosed, got %v", err)
	}
	if got := len(g.Live()); got != 1 {
		t.Fatalf("live texts = %d, want the single batched text", got)
	}
}

func TestBatchFrameOrderIsEnforced(t *testing.T) {
	g := newDamageGuard(t, baseDamageConfig())
	empty, err := g.BatchFrame(5)
	if err != nil {
		t.Fatalf("batch empty frame 5: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty frame produced %d texts, want 0", len(empty))
	}
	if _, err := g.BatchFrame(4); !errors.Is(err, impl.ErrFrameOrder) {
		t.Fatalf("batching an older frame must fail with ErrFrameOrder, got %v", err)
	}
	if _, err := g.BatchFrame(5); !errors.Is(err, impl.ErrFrameClosed) {
		t.Fatalf("rebatching frame 5 must fail with ErrFrameClosed, got %v", err)
	}
}

func TestExpiryUsesGameFrames(t *testing.T) {
	cfg := baseDamageConfig()
	cfg.LifeFrames = 5
	g := newDamageGuard(t, cfg)
	if err := g.Submit(hitEvent(1, 10, "mob-a", 10)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := g.BatchFrame(10); err != nil {
		t.Fatalf("batch frame 10: %v", err)
	}
	live := g.Live()
	if len(live) != 1 {
		t.Fatalf("live texts = %d, want 1", len(live))
	}
	if live[0].ExpireAt != 15 {
		t.Fatalf("expire frame = %d, want 15", live[0].ExpireAt)
	}
	if got, err := g.AdvanceFrames(4); err != nil || got != 0 {
		t.Fatalf("advance 4 frames = %d/%v, want 0/nil", got, err)
	}
	if got := len(g.Live()); got != 1 {
		t.Fatalf("live texts before the expiry frame = %d, want 1", got)
	}
	if got, err := g.AdvanceFrames(1); err != nil || got != 1 {
		t.Fatalf("advance to the expiry frame = %d/%v, want 1/nil", got, err)
	}
	if got := len(g.Live()); got != 0 {
		t.Fatalf("live texts after expiry = %d, want 0", got)
	}
	if got := g.Expire(); got != 0 {
		t.Fatalf("expire with nothing left = %d, want 0", got)
	}
	if _, err := g.AdvanceFrames(0); !errors.Is(err, impl.ErrBadAdvance) {
		t.Fatalf("advancing zero frames must fail with ErrBadAdvance, got %v", err)
	}
}

func TestPlaybackSpeedValidation(t *testing.T) {
	g := newDamageGuard(t, baseDamageConfig())
	if got := g.PlaybackSpeed(); got != 1000 {
		t.Fatalf("initial speed = %d, want 1000", got)
	}
	if err := g.SetPlaybackSpeed(2000); err != nil {
		t.Fatalf("set 2x speed: %v", err)
	}
	if got := g.PlaybackSpeed(); got != 2000 {
		t.Fatalf("speed after update = %d, want 2000", got)
	}
	for _, bad := range []int{0, -500, 8001} {
		if err := g.SetPlaybackSpeed(bad); !errors.Is(err, impl.ErrBadSpeed) {
			t.Fatalf("speed %d must fail with ErrBadSpeed, got %v", bad, err)
		}
	}
	if err := g.SetPlaybackSpeed(1); err != nil {
		t.Fatalf("set the slowest speed: %v", err)
	}
	if got := g.PlaybackSpeed(); got != 1 {
		t.Fatalf("speed after slowest update = %d, want 1", got)
	}
}

func TestPendingLimitRejectsExtraEvents(t *testing.T) {
	cfg := baseDamageConfig()
	cfg.MaxPending = 2
	g := newDamageGuard(t, cfg)
	if err := g.Submit(hitEvent(1, 1, "mob-a", 10)); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if err := g.Submit(hitEvent(2, 1, "mob-b", 10)); err != nil {
		t.Fatalf("submit second: %v", err)
	}
	if err := g.Submit(hitEvent(3, 1, "mob-c", 10)); !errors.Is(err, impl.ErrPendingLimit) {
		t.Fatalf("third event must fail with ErrPendingLimit, got %v", err)
	}
	if got := len(g.PendingFrames()); got != 1 {
		t.Fatalf("pending frames = %d, want the accepted frame", got)
	}
}

func TestTextLimitRejectsTheFrame(t *testing.T) {
	cfg := baseDamageConfig()
	cfg.MaxTexts = 1
	g := newDamageGuard(t, cfg)
	if err := g.Submit(hitEvent(1, 1, "mob-a", 10)); err != nil {
		t.Fatalf("submit first: %v", err)
	}
	if err := g.Submit(hitEvent(2, 1, "mob-b", 10)); err != nil {
		t.Fatalf("submit second: %v", err)
	}
	if _, err := g.BatchFrame(1); !errors.Is(err, impl.ErrTextLimit) {
		t.Fatalf("batching two texts into a cap of one must fail with ErrTextLimit, got %v", err)
	}
	if got := len(g.Live()); got != 0 {
		t.Fatalf("a rejected batch left %d live texts, want 0", got)
	}
	if pending := g.PendingFrames(); len(pending) != 1 || pending[0] != 1 {
		t.Fatalf("pending frames after a rejected batch = %v, want [1]", pending)
	}
}

func TestStyleForElementAndCrit(t *testing.T) {
	g := newDamageGuard(t, baseDamageConfig())
	normal, err := g.StyleFor(impl.DamageEvent{Element: impl.ElementFire})
	if err != nil {
		t.Fatalf("style for normal fire hit: %v", err)
	}
	if normal != "normal_fire" {
		t.Fatalf("normal fire style = %q, want normal_fire", normal)
	}
	crit, err := g.StyleFor(impl.DamageEvent{Element: impl.ElementIce, Crit: true})
	if err != nil {
		t.Fatalf("style for crit ice hit: %v", err)
	}
	if crit != "crit_ice" {
		t.Fatalf("crit ice style = %q, want crit_ice", crit)
	}
	if _, err := g.StyleFor(impl.DamageEvent{Element: "arcane"}); !errors.Is(err, impl.ErrUnknownElement) {
		t.Fatalf("unknown element style must fail with ErrUnknownElement, got %v", err)
	}
}

func TestExportReplaySkipsPendingEvents(t *testing.T) {
	g := newDamageGuard(t, baseDamageConfig())
	if err := g.Submit(hitEvent(1, 1, "mob-a", 10)); err != nil {
		t.Fatalf("submit frame 1: %v", err)
	}
	if err := g.Submit(hitEvent(2, 2, "mob-b", 20)); err != nil {
		t.Fatalf("submit frame 2: %v", err)
	}
	if _, err := g.BatchFrame(1); err != nil {
		t.Fatalf("batch frame 1: %v", err)
	}
	replay := g.ExportReplay()
	if len(replay) != 1 {
		t.Fatalf("replay frames = %d, want only the batched frame", len(replay))
	}
	if replay[0].Frame != 1 || len(replay[0].Texts) != 1 || replay[0].Texts[0].TargetID != "mob-a" {
		t.Fatalf("replay frame = %+v, want frame 1 with mob-a", replay[0])
	}
	if got := len(g.Live()); got != 1 {
		t.Fatalf("live texts = %d, want 1", got)
	}
}

func TestAuditLogIsCappedAndResetClearsState(t *testing.T) {
	cfg := baseDamageConfig()
	cfg.MaxAudit = 3
	g := newDamageGuard(t, cfg)
	for frame := int64(1); frame <= 4; frame++ {
		if err := g.Submit(hitEvent(frame, frame, "mob-a", 10)); err != nil {
			t.Fatalf("submit frame %d: %v", frame, err)
		}
		if _, err := g.BatchFrame(frame); err != nil {
			t.Fatalf("batch frame %d: %v", frame, err)
		}
	}
	audit := g.Audit()
	if len(audit) != 3 {
		t.Fatalf("audit len = %d, want the configured cap 3", len(audit))
	}
	for i := 1; i < len(audit); i++ {
		if audit[i].Seq <= audit[i-1].Seq {
			t.Fatalf("audit sequence must strictly increase, got %+v", audit)
		}
	}
	if audit[len(audit)-1].Action != impl.AuditBatchFrame {
		t.Fatalf("newest audit action = %q, want batch_frame", audit[len(audit)-1].Action)
	}
	if st := g.Stats(); st.Submitted != 4 || st.Batched != 4 {
		t.Fatalf("stats before reset = %+v, want 4 submitted and 4 batched", st)
	}
	if err := g.Reset(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := len(g.Live()); got != 0 {
		t.Fatalf("live texts after reset = %d, want 0", got)
	}
	if got := len(g.PendingFrames()); got != 0 {
		t.Fatalf("pending frames after reset = %d, want 0", got)
	}
	if st := g.Stats(); st != (impl.GuardStats{}) {
		t.Fatalf("stats after reset = %+v, want zero", st)
	}
	audit = g.Audit()
	if len(audit) != 1 || audit[0].Action != impl.AuditReset {
		t.Fatalf("audit after reset = %+v, want a single reset entry", audit)
	}
}

func TestSnapshotRestoreKeepsReplayState(t *testing.T) {
	g := newDamageGuard(t, baseDamageConfig())
	if err := g.SetPlaybackSpeed(1500); err != nil {
		t.Fatalf("set speed: %v", err)
	}
	if err := g.Submit(hitEvent(1, 2, "mob-a", 40)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := g.BatchFrame(2); err != nil {
		t.Fatalf("batch frame 2: %v", err)
	}
	if _, err := g.AdvanceFrames(1); err != nil {
		t.Fatalf("advance one frame: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newDamageGuard(t, baseDamageConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.PlaybackSpeed(); got != 1500 {
		t.Fatalf("restored speed = %d, want 1500", got)
	}
	live := restored.Live()
	if len(live) != 1 || live[0].TargetID != "mob-a" || live[0].Amount != 40 {
		t.Fatalf("restored live texts = %+v, want mob-a with 40", live)
	}
	if got := len(restored.ExportReplay()); got != 1 {
		t.Fatalf("restored replay frames = %d, want 1", got)
	}
	if _, err := restored.BatchFrame(2); !errors.Is(err, impl.ErrFrameClosed) {
		t.Fatalf("rebatching a restored frame must fail with ErrFrameClosed, got %v", err)
	}
	if err := restored.Restore([]byte("{broken-replay-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(restored.Live()); got != 1 {
		t.Fatalf("a rejected restore changed live texts to %d, want 1", got)
	}
}

func TestConcurrentSubmitAndBatch(t *testing.T) {
	cfg := baseDamageConfig()
	cfg.MaxPending = 128
	cfg.MaxTexts = 64
	g := newDamageGuard(t, cfg)
	const workers = 32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := g.Submit(hitEvent(int64(i+1), 9, "mob-"+string(rune('a'+i%26))+"-"+string(rune('a'+i/26)), 10)); err != nil {
				t.Errorf("concurrent submit %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	pending := g.PendingFrames()
	if len(pending) != 1 || pending[0] != 9 {
		t.Fatalf("pending frames after the race = %v, want [9]", pending)
	}
	texts, err := g.BatchFrame(9)
	if err != nil {
		t.Fatalf("batch frame 9: %v", err)
	}
	if len(texts) != workers {
		t.Fatalf("batched texts = %d, want %d", len(texts), workers)
	}
	var total int64
	for _, text := range texts {
		total += text.Amount
	}
	if total != 10*workers {
		t.Fatalf("total damage = %d, want %d", total, 10*workers)
	}
	const batching = 8
	errs := make([]error, batching)
	for i := 0; i < batching; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = g.BatchFrame(10)
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		if !errors.Is(err, impl.ErrFrameClosed) {
			t.Fatalf("concurrent batch returned %v, want nil or ErrFrameClosed", err)
		}
	}
	if ok != 1 {
		t.Fatalf("frame 10 got %d successful batches, want exactly 1", ok)
	}
}
