package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-game-a-03"
)

func waveClock() time.Time {
	return time.Unix(1_700_100_000, 0).UTC()
}

func laneConfig() impl.LaneConfig {
	return impl.LaneConfig{
		WaveInterval:         30 * time.Second,
		MeleePerWave:         3,
		RangedPerWave:        3,
		CannonEvery:          4,
		SuperMinions:         2,
		MaxCompensation:      2,
		CompensationUnits:    3,
		MaxCompensationUnits: 4,
		PressureThreshold:    5,
	}
}

func startedScheduler(t *testing.T) *impl.WaveScheduler {
	t.Helper()
	s := impl.NewWaveScheduler(laneConfig())
	if err := s.Start("match-1", waveClock()); err != nil {
		t.Fatalf("start match: %v", err)
	}
	return s
}

func countWithIndex(events []impl.WaveEvent, index int) int {
	total := 0
	for _, event := range events {
		if event.Index == index {
			total++
		}
	}
	return total
}

func countAt(events []impl.WaveEvent, at time.Time) int {
	total := 0
	for _, event := range events {
		if event.At.Equal(at) {
			total++
		}
	}
	return total
}

func TestStartValidatesMatchAndConfig(t *testing.T) {
	s := impl.NewWaveScheduler(laneConfig())
	if err := s.Start("   ", waveClock()); !errors.Is(err, impl.ErrBlankMatchID) {
		t.Fatalf("blank match id must be rejected, got %v", err)
	}
	bad := laneConfig()
	bad.WaveInterval = 0
	if err := impl.NewWaveScheduler(bad).Start("match-1", waveClock()); !errors.Is(err, impl.ErrInvalidConfig) {
		t.Fatalf("a non positive wave interval must fail with ErrInvalidConfig, got %v", err)
	}
	started := startedScheduler(t)
	if got := started.MatchID(); got != "match-1" {
		t.Fatalf("MatchID() = %q, want match-1", got)
	}
	if err := started.Start("match-1", waveClock()); !errors.Is(err, impl.ErrAlreadyStarted) {
		t.Fatalf("a second start must fail with ErrAlreadyStarted, got %v", err)
	}
}

func TestTickEmitsOneWavePerLane(t *testing.T) {
	s := startedScheduler(t)
	events := s.Tick(waveClock())
	if len(events) != 3 {
		t.Fatalf("first tick events = %d, want one per lane", len(events))
	}
	seen := map[impl.Lane]bool{}
	for _, event := range events {
		if event.Kind != impl.EventWave {
			t.Fatalf("event = %+v, want kind %q", event, impl.EventWave)
		}
		if event.Index != 1 {
			t.Fatalf("event index = %d, want 1", event.Index)
		}
		if !event.At.Equal(waveClock()) {
			t.Fatalf("event at = %s, want %s", event.At, waveClock())
		}
		seen[event.Lane] = true
	}
	for _, lane := range []impl.Lane{impl.LaneTop, impl.LaneMid, impl.LaneBot} {
		if !seen[lane] {
			t.Fatalf("lane %q produced no wave, events %+v", lane, events)
		}
	}
}

func TestTickWaitsForWaveInterval(t *testing.T) {
	s := startedScheduler(t)
	if events := s.Tick(waveClock().Add(29 * time.Second)); len(events) != 0 {
		t.Fatalf("events before the interval = %+v, want none", events)
	}
	second := waveClock().Add(30 * time.Second)
	events := s.Tick(second)
	if len(events) != 3 {
		t.Fatalf("second wave events = %d, want 3", len(events))
	}
	for _, event := range events {
		if event.Index != 2 || !event.At.Equal(second) {
			t.Fatalf("second wave event = %+v, want index 2 at %s", event, second)
		}
	}
}

func TestTickCatchesUpMissedWaves(t *testing.T) {
	s := startedScheduler(t)
	events := s.Tick(waveClock().Add(60 * time.Second))
	if len(events) != 9 {
		t.Fatalf("events for three waves on three lanes = %d, want 9", len(events))
	}
	for index := 1; index <= 3; index++ {
		if got := countWithIndex(events, index); got != 3 {
			t.Fatalf("index %d appeared %d times, want 3", index, got)
		}
	}
	if got := countAt(events, waveClock()); got != 3 {
		t.Fatalf("waves at the start = %d, want 3", got)
	}
	if got := countAt(events, waveClock().Add(60*time.Second)); got != 3 {
		t.Fatalf("waves at the end of the catch up = %d, want 3", got)
	}
}

func TestWaveForComposition(t *testing.T) {
	s := startedScheduler(t)
	first, err := s.WaveFor(impl.LaneMid, 1)
	if err != nil {
		t.Fatalf("wave for mid lane: %v", err)
	}
	if first.Lane != impl.LaneMid || first.Index != 1 || !first.At.Equal(waveClock()) {
		t.Fatalf("wave identity = %+v, want mid lane index 1 at %s", first, waveClock())
	}
	if first.Melee != 3 || first.Ranged != 3 || first.Cannon != 0 || first.Super != 0 {
		t.Fatalf("wave composition = %+v, want 3 melee 3 ranged no cannon no super", first)
	}
	for _, n := range []int{4, 8} {
		wave, err := s.WaveFor(impl.LaneMid, n)
		if err != nil {
			t.Fatalf("wave for mid lane %d: %v", n, err)
		}
		if wave.Cannon != 1 {
			t.Fatalf("wave %d cannon = %d, want 1", n, wave.Cannon)
		}
	}
	for _, n := range []int{3, 5} {
		wave, err := s.WaveFor(impl.LaneMid, n)
		if err != nil {
			t.Fatalf("wave for mid lane %d: %v", n, err)
		}
		if wave.Cannon != 0 {
			t.Fatalf("wave %d cannon = %d, want 0", n, wave.Cannon)
		}
	}
}

func TestWaveForRejectsBadArgs(t *testing.T) {
	s := startedScheduler(t)
	if _, err := s.WaveFor(impl.Lane("jungle"), 1); !errors.Is(err, impl.ErrUnknownLane) {
		t.Fatalf("unknown lane must fail with ErrUnknownLane, got %v", err)
	}
	if _, err := s.WaveFor(impl.LaneMid, 0); !errors.Is(err, impl.ErrInvalidWaveIndex) {
		t.Fatalf("wave index 0 must fail with ErrInvalidWaveIndex, got %v", err)
	}
	if _, err := s.WaveFor(impl.LaneMid, -3); !errors.Is(err, impl.ErrInvalidWaveIndex) {
		t.Fatalf("negative wave index must fail with ErrInvalidWaveIndex, got %v", err)
	}
}

func TestOuterStructureBreakKeepsComposition(t *testing.T) {
	s := startedScheduler(t)
	s.Tick(waveClock())
	if err := s.ApplyStructureBreak(impl.LaneTop, 1, waveClock()); err != nil {
		t.Fatalf("break outer tower: %v", err)
	}
	wave, err := s.WaveFor(impl.LaneTop, 2)
	if err != nil {
		t.Fatalf("wave for top lane: %v", err)
	}
	if wave.Super != 0 {
		t.Fatalf("outer tower break must not add super minions, got %d", wave.Super)
	}
	other, err := s.WaveFor(impl.LaneMid, 2)
	if err != nil {
		t.Fatalf("wave for mid lane: %v", err)
	}
	if other.Super != 0 {
		t.Fatalf("a top lane break must not affect the mid lane, got %d super minions", other.Super)
	}
	events := s.Events()
	last := events[len(events)-1]
	if last.Kind != impl.EventStructure || last.Lane != impl.LaneTop {
		t.Fatalf("last event = %+v, want a structure event on the top lane", last)
	}
}

func TestHighGroundBreakAddsSuperMinions(t *testing.T) {
	s := startedScheduler(t)
	s.Tick(waveClock().Add(30 * time.Second))
	if err := s.ApplyStructureBreak(impl.LaneBot, 3, waveClock().Add(30*time.Second)); err != nil {
		t.Fatalf("break high ground tower: %v", err)
	}
	before, err := s.WaveFor(impl.LaneBot, 2)
	if err != nil {
		t.Fatalf("wave for bot lane 2: %v", err)
	}
	if before.Super != 0 {
		t.Fatalf("already generated wave 2 super = %d, want 0", before.Super)
	}
	after, err := s.WaveFor(impl.LaneBot, 3)
	if err != nil {
		t.Fatalf("wave for bot lane 3: %v", err)
	}
	if after.Super != 2 {
		t.Fatalf("wave 3 super = %d, want the configured 2", after.Super)
	}
	if after.Melee != 3 || after.Ranged != 3 {
		t.Fatalf("super wave composition = %+v, want 3 melee and 3 ranged still", after)
	}
	if after.Index != 3 {
		t.Fatalf("super wave index = %d, want 3", after.Index)
	}
}

func TestStructureBreakRejectsUnknownLane(t *testing.T) {
	s := startedScheduler(t)
	if err := s.ApplyStructureBreak(impl.Lane("jungle"), 3, waveClock()); !errors.Is(err, impl.ErrUnknownLane) {
		t.Fatalf("unknown lane must fail with ErrUnknownLane, got %v", err)
	}
}

func TestStructureBreakRejectsBadTier(t *testing.T) {
	s := startedScheduler(t)
	if err := s.ApplyStructureBreak(impl.LaneMid, 0, waveClock()); !errors.Is(err, impl.ErrInvalidTier) {
		t.Fatalf("tier 0 must fail with ErrInvalidTier, got %v", err)
	}
	if err := s.ApplyStructureBreak(impl.LaneMid, 4, waveClock()); !errors.Is(err, impl.ErrInvalidTier) {
		t.Fatalf("tier 4 must fail with ErrInvalidTier, got %v", err)
	}
	fresh := impl.NewWaveScheduler(laneConfig())
	if err := fresh.ApplyStructureBreak(impl.LaneMid, 2, waveClock()); !errors.Is(err, impl.ErrNotStarted) {
		t.Fatalf("a break before the match starts must fail with ErrNotStarted, got %v", err)
	}
}

func TestStructureBreakRejectsRepeatedTier(t *testing.T) {
	s := startedScheduler(t)
	s.Tick(waveClock())
	if err := s.ApplyStructureBreak(impl.LaneMid, 3, waveClock()); err != nil {
		t.Fatalf("first high ground break: %v", err)
	}
	if err := s.ApplyStructureBreak(impl.LaneMid, 3, waveClock()); !errors.Is(err, impl.ErrStructureAlreadyBroken) {
		t.Fatalf("a repeated tier break must fail with ErrStructureAlreadyBroken, got %v", err)
	}
}

func TestPressureQuotaExhausted(t *testing.T) {
	s := startedScheduler(t)
	if err := s.ReportPressure(impl.LaneMid, 6, waveClock()); err != nil {
		t.Fatalf("first pressure report: %v", err)
	}
	if err := s.ReportPressure(impl.LaneMid, 7, waveClock()); err != nil {
		t.Fatalf("second pressure report: %v", err)
	}
	if err := s.ReportPressure(impl.LaneMid, 8, waveClock()); !errors.Is(err, impl.ErrCompensationExhausted) {
		t.Fatalf("a third compensation must exceed the quota, got %v", err)
	}
	if got := s.PendingCompensation(impl.LaneMid); got != 2 {
		t.Fatalf("pending compensations = %d, want the quota 2", got)
	}
	if got := s.Compensations(impl.LaneMid); got != 0 {
		t.Fatalf("generated compensations = %d, want 0 before a wave", got)
	}
}

func TestPressureUnitCapLimitsBonus(t *testing.T) {
	cfg := laneConfig()
	cfg.MaxCompensation = 5
	cfg.CompensationUnits = 3
	cfg.MaxCompensationUnits = 4
	s := impl.NewWaveScheduler(cfg)
	if err := s.Start("match-2", waveClock()); err != nil {
		t.Fatalf("start match: %v", err)
	}
	if err := s.ReportPressure(impl.LaneBot, 9, waveClock()); err != nil {
		t.Fatalf("first pressure report: %v", err)
	}
	if err := s.ReportPressure(impl.LaneBot, 9, waveClock()); !errors.Is(err, impl.ErrCompensationExhausted) {
		t.Fatalf("a second bonus of three units must exceed the four unit cap, got %v", err)
	}
	if got := s.PendingCompensation(impl.LaneBot); got != 1 {
		t.Fatalf("pending compensations = %d, want 1", got)
	}
}

func TestPressureRejectsLowDeficit(t *testing.T) {
	s := startedScheduler(t)
	if err := s.ReportPressure(impl.LaneTop, 4, waveClock()); !errors.Is(err, impl.ErrPressureBelowThreshold) {
		t.Fatalf("a deficit below the threshold must be ignored, got %v", err)
	}
	if got := s.PendingCompensation(impl.LaneTop); got != 0 {
		t.Fatalf("pending compensations = %d, want 0", got)
	}
	if err := s.ReportPressure(impl.Lane("jungle"), 9, waveClock()); !errors.Is(err, impl.ErrUnknownLane) {
		t.Fatalf("unknown lane must fail with ErrUnknownLane, got %v", err)
	}
}

func TestSnapshotRestoreKeepsScheduleAndBreaks(t *testing.T) {
	s := startedScheduler(t)
	s.Tick(waveClock().Add(60 * time.Second))
	if err := s.ApplyStructureBreak(impl.LaneMid, 3, waveClock().Add(60*time.Second)); err != nil {
		t.Fatalf("break high ground tower: %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewWaveScheduler(laneConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if index, err := restored.WaveIndex(impl.LaneMid); err != nil || index != 3 {
		t.Fatalf("restored wave index = %d, %v, want 3 and nil", index, err)
	}
	next, err := restored.NextWaveAt(impl.LaneMid)
	if err != nil {
		t.Fatalf("next wave at: %v", err)
	}
	if !next.Equal(waveClock().Add(90 * time.Second)) {
		t.Fatalf("restored next wave = %s, want %s", next, waveClock().Add(90*time.Second))
	}
	preview, err := restored.WaveFor(impl.LaneMid, 4)
	if err != nil {
		t.Fatalf("wave for mid lane 4: %v", err)
	}
	if preview.Super != 2 {
		t.Fatalf("restored super minions = %d, want 2", preview.Super)
	}
	events := restored.Tick(waveClock().Add(90 * time.Second))
	if len(events) != 3 {
		t.Fatalf("events after restore = %d, want 3", len(events))
	}
	for _, event := range events {
		if event.Index != 4 {
			t.Fatalf("event after restore = %+v, want index 4", event)
		}
	}
	if err := restored.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if index, err := restored.WaveIndex(impl.LaneMid); err != nil || index != 4 {
		t.Fatalf("failed restore must keep the live schedule, got %d, %v", index, err)
	}
}
