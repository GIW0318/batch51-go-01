package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-game-a-13"
)

func directorConfig() impl.DirectorConfig {
	return impl.DirectorConfig{
		Cameras: []impl.CameraInfo{
			{CameraID: "cam-a", Label: "top"},
			{CameraID: "cam-b", Label: "mid"},
			{CameraID: "cam-c", Label: "pit"},
		},
		MinDwell:         10 * time.Second,
		Hysteresis:       20,
		SwitchBudget:     5,
		ScoreThreshold:   30,
		OverrideCooldown: 30 * time.Second,
		Delay:            5 * time.Second,
	}
}

func directorStart() time.Time {
	return time.Date(2026, 5, 4, 18, 0, 0, 0, time.UTC)
}

func observe(t *testing.T, d *impl.SpectatorCameraDirector, kind, camera string, at time.Time) {
	t.Helper()
	if err := d.Observe(impl.MatchEvent{Kind: kind, CameraID: camera, At: at}); err != nil {
		t.Fatalf("observe %s on %s: %v", kind, camera, err)
	}
}

func TestObserveAccumulatesScores(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	observe(t, d, impl.EventKill, "cam-a", start)
	observe(t, d, impl.EventUltimate, "cam-a", start.Add(time.Second))
	observe(t, d, impl.EventObjective, "cam-b", start.Add(2*time.Second))
	a, err := d.Score("cam-a")
	if err != nil || a != 50 {
		t.Fatalf("cam-a score = %d, %v; want 50", a, err)
	}
	b, err := d.Score("cam-b")
	if err != nil || b != 20 {
		t.Fatalf("cam-b score = %d, %v; want 20", b, err)
	}
	if _, err := d.Score("cam-z"); !errors.Is(err, impl.ErrUnknownCamera) {
		t.Fatalf("unknown camera score = %v, want ErrUnknownCamera", err)
	}
}

func TestObserveRejectsUnknownCamera(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	if err := d.Observe(impl.MatchEvent{Kind: impl.EventKill, CameraID: "cam-z", At: start}); !errors.Is(err, impl.ErrUnknownCamera) {
		t.Fatalf("event on unknown camera = %v, want ErrUnknownCamera", err)
	}
	if err := d.Observe(impl.MatchEvent{Kind: impl.EventKill, CameraID: "cam-a", At: start}); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	if score, err := d.Score("cam-a"); err != nil || score != impl.WeightKill {
		t.Fatalf("cam-a score after rejected event = %d, %v; want %d", score, err, impl.WeightKill)
	}
}

func TestObserveRejectsUnknownEventKind(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	if err := d.Observe(impl.MatchEvent{Kind: "boss_spawn", CameraID: "cam-a", At: start}); !errors.Is(err, impl.ErrBadEvent) {
		t.Fatalf("unknown event kind = %v, want ErrBadEvent", err)
	}
	observe(t, d, impl.EventTeamfight, "cam-a", start)
	if score, err := d.Score("cam-a"); err != nil || score != impl.WeightTeamfight {
		t.Fatalf("cam-a score = %d, %v; want %d", score, err, impl.WeightTeamfight)
	}
}

func TestObserveRejectsOutOfOrderEvent(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	observe(t, d, impl.EventKill, "cam-a", start.Add(5*time.Second))
	if err := d.Observe(impl.MatchEvent{Kind: impl.EventUltimate, CameraID: "cam-b", At: start}); !errors.Is(err, impl.ErrEventOutOfOrder) {
		t.Fatalf("older event = %v, want ErrEventOutOfOrder", err)
	}
	if score, err := d.Score("cam-b"); err != nil || score != 0 {
		t.Fatalf("cam-b score after out of order event = %d, %v; want 0", score, err)
	}
	observe(t, d, impl.EventUltimate, "cam-b", start.Add(6*time.Second))
	if score, err := d.Score("cam-b"); err != nil || score != impl.WeightUltimate {
		t.Fatalf("cam-b score = %d, %v; want %d", score, err, impl.WeightUltimate)
	}
}

func TestSelectSwitchesToHotCamera(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	observe(t, d, impl.EventKill, "cam-b", start)
	observe(t, d, impl.EventTeamfight, "cam-b", start.Add(time.Second))
	observe(t, d, impl.EventObjective, "cam-b", start.Add(2*time.Second))
	observe(t, d, impl.EventUltimate, "cam-a", start.Add(3*time.Second))
	now := start.Add(4 * time.Second)
	shot, err := d.Select(now)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if shot.CameraID != "cam-b" || !shot.Switched {
		t.Fatalf("shot = %+v, want a switch to cam-b", shot)
	}
	if shot.Reason != impl.ReasonSwitch {
		t.Fatalf("reason = %q, want %q", shot.Reason, impl.ReasonSwitch)
	}
	if shot.Score != 90 {
		t.Fatalf("shot score = %d, want 90", shot.Score)
	}
	if !shot.At.Equal(now) {
		t.Fatalf("shot time = %s, want %s", shot.At, now)
	}
	if !shot.Timeline.Equal(now.Add(-5 * time.Second)) {
		t.Fatalf("shot timeline = %s, want %s", shot.Timeline, now.Add(-5*time.Second))
	}
	if got := d.SwitchCount(); got != 1 {
		t.Fatalf("switch count = %d, want 1", got)
	}
}

func TestSelectRespectsMinDwell(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	observe(t, d, impl.EventKill, "cam-b", start)
	observe(t, d, impl.EventTeamfight, "cam-b", start.Add(time.Second))
	observe(t, d, impl.EventObjective, "cam-b", start.Add(2*time.Second))
	observe(t, d, impl.EventUltimate, "cam-b", start.Add(3*time.Second))
	if shot, err := d.Select(start.Add(4 * time.Second)); err != nil || shot.CameraID != "cam-b" {
		t.Fatalf("first select = %+v, %v; want cam-b", shot, err)
	}
	for i := 0; i < 5; i++ {
		observe(t, d, impl.EventKill, "cam-a", start.Add(time.Duration(5+i)*time.Second))
	}
	shot, err := d.Select(start.Add(9 * time.Second))
	if err != nil {
		t.Fatalf("second select: %v", err)
	}
	if shot.CameraID != "cam-b" || shot.Switched {
		t.Fatalf("shot inside min dwell = %+v, want to stay on cam-b", shot)
	}
	if shot.Reason != impl.ReasonDwell {
		t.Fatalf("reason = %q, want %q", shot.Reason, impl.ReasonDwell)
	}
	if got := d.SwitchCount(); got != 1 {
		t.Fatalf("switch count = %d, want 1", got)
	}
}

func TestSelectRequiresHysteresis(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	observe(t, d, impl.EventKill, "cam-b", start)
	observe(t, d, impl.EventTeamfight, "cam-b", start.Add(time.Second))
	observe(t, d, impl.EventObjective, "cam-b", start.Add(2*time.Second))
	observe(t, d, impl.EventUltimate, "cam-b", start.Add(3*time.Second))
	if shot, err := d.Select(start); err != nil || shot.CameraID != "cam-b" {
		t.Fatalf("first select = %+v, %v; want cam-b", shot, err)
	}
	observe(t, d, impl.EventKill, "cam-a", start.Add(4*time.Second))
	observe(t, d, impl.EventKill, "cam-a", start.Add(5*time.Second))
	observe(t, d, impl.EventTeamfight, "cam-a", start.Add(6*time.Second))
	kept, err := d.Select(start.Add(11 * time.Second))
	if err != nil {
		t.Fatalf("second select: %v", err)
	}
	if kept.CameraID != "cam-b" || kept.Switched {
		t.Fatalf("shot with a small lead = %+v, want to keep cam-b inside the hysteresis band", kept)
	}
	if kept.Reason != impl.ReasonHysteresis {
		t.Fatalf("reason = %q, want %q", kept.Reason, impl.ReasonHysteresis)
	}
	observe(t, d, impl.EventTeamfight, "cam-a", start.Add(12*time.Second))
	moved, err := d.Select(start.Add(22 * time.Second))
	if err != nil {
		t.Fatalf("third select: %v", err)
	}
	if moved.CameraID != "cam-a" || !moved.Switched || moved.Reason != impl.ReasonSwitch {
		t.Fatalf("shot with a clear lead = %+v, want a switch to cam-a", moved)
	}
	if got := d.SwitchCount(); got != 2 {
		t.Fatalf("switch count = %d, want 2", got)
	}
}

func TestSelectIdleReturnsFirstCamera(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	shot, err := d.Select(directorStart())
	if err != nil {
		t.Fatalf("select without events: %v", err)
	}
	if shot.CameraID != "cam-a" || shot.Switched {
		t.Fatalf("idle shot = %+v, want cam-a without a switch", shot)
	}
	if shot.Reason != impl.ReasonIdle {
		t.Fatalf("reason = %q, want %q", shot.Reason, impl.ReasonIdle)
	}
	if got := d.SwitchCount(); got != 0 {
		t.Fatalf("switch count = %d, want 0", got)
	}
}

func TestSelectHonorsSwitchBudget(t *testing.T) {
	cfg := directorConfig()
	cfg.SwitchBudget = 1
	d := impl.NewSpectatorCameraDirector(cfg)
	start := directorStart()
	observe(t, d, impl.EventKill, "cam-b", start)
	observe(t, d, impl.EventTeamfight, "cam-b", start.Add(time.Second))
	observe(t, d, impl.EventObjective, "cam-b", start.Add(2*time.Second))
	observe(t, d, impl.EventUltimate, "cam-b", start.Add(3*time.Second))
	if shot, err := d.Select(start.Add(4 * time.Second)); err != nil || shot.CameraID != "cam-b" {
		t.Fatalf("first select = %+v, %v; want cam-b", shot, err)
	}
	for i := 0; i < 8; i++ {
		observe(t, d, impl.EventKill, "cam-a", start.Add(time.Duration(5+i)*time.Second))
	}
	shot, err := d.Select(start.Add(20 * time.Second))
	if err != nil {
		t.Fatalf("second select: %v", err)
	}
	if shot.CameraID != "cam-b" || shot.Switched {
		t.Fatalf("shot with an exhausted budget = %+v, want to stay on cam-b", shot)
	}
	if shot.Reason != impl.ReasonBudget {
		t.Fatalf("reason = %q, want %q", shot.Reason, impl.ReasonBudget)
	}
	if got := d.SwitchCount(); got != 1 {
		t.Fatalf("switch count = %d, want 1", got)
	}
}

func TestOverrideTakesManualCamera(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	if err := d.Override("cam-c", start.Add(10*time.Minute)); err != nil {
		t.Fatalf("override: %v", err)
	}
	shot, err := d.Select(start.Add(time.Minute))
	if err != nil {
		t.Fatalf("select during override: %v", err)
	}
	if shot.CameraID != "cam-c" || shot.Switched || shot.Reason != impl.ReasonManual {
		t.Fatalf("manual shot = %+v, want cam-c under manual control", shot)
	}
	if !shot.Timeline.Equal(start.Add(time.Minute - 5*time.Second)) {
		t.Fatalf("manual shot timeline = %s, want %s", shot.Timeline, start.Add(time.Minute-5*time.Second))
	}
	if got := d.SwitchCount(); got != 0 {
		t.Fatalf("manual takeover must not consume the switch budget, got %d", got)
	}
}

func TestOverrideRejectsUnknownCamera(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	if err := d.Override("cam-z", start.Add(time.Minute)); !errors.Is(err, impl.ErrUnknownCamera) {
		t.Fatalf("override unknown camera = %v, want ErrUnknownCamera", err)
	}
	if err := d.Override("cam-c", start.Add(time.Minute)); err != nil {
		t.Fatalf("valid override: %v", err)
	}
	shot, err := d.Select(start.Add(time.Second))
	if err != nil || shot.CameraID != "cam-c" || shot.Reason != impl.ReasonManual {
		t.Fatalf("shot after valid override = %+v, %v; want cam-c manual", shot, err)
	}
}

func TestReleaseStartsCooldown(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	if err := d.Override("cam-c", start.Add(10*time.Minute)); err != nil {
		t.Fatalf("override: %v", err)
	}
	if shot, err := d.Select(start.Add(time.Minute)); err != nil || shot.CameraID != "cam-c" {
		t.Fatalf("manual select = %+v, %v; want cam-c", shot, err)
	}
	if err := d.Release("cam-c"); err != nil {
		t.Fatalf("release: %v", err)
	}
	cooling, err := d.Select(start.Add(2 * time.Minute))
	if err != nil {
		t.Fatalf("select during cooldown: %v", err)
	}
	if cooling.Reason != impl.ReasonCooldown || cooling.CameraID != "cam-c" {
		t.Fatalf("cooldown shot = %+v, want cam-c in cooldown", cooling)
	}
	resumed, err := d.Select(start.Add(11 * time.Minute))
	if err != nil {
		t.Fatalf("select after cooldown: %v", err)
	}
	if resumed.Reason == impl.ReasonCooldown || resumed.Reason == impl.ReasonManual {
		t.Fatalf("shot after cooldown = %+v, want automatic selection again", resumed)
	}
	if resumed.CameraID != "cam-c" {
		t.Fatalf("camera after cooldown = %q, want the manual camera to stay current", resumed.CameraID)
	}
}

func TestReleaseWithoutOverrideRejected(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	if err := d.Release("cam-a"); !errors.Is(err, impl.ErrNotOverridden) {
		t.Fatalf("release without takeover = %v, want ErrNotOverridden", err)
	}
	if err := d.Override("cam-b", start.Add(time.Minute)); err != nil {
		t.Fatalf("override: %v", err)
	}
	if err := d.Release("cam-b"); err != nil {
		t.Fatalf("release active takeover: %v", err)
	}
}

func TestSnapshotRestoreKeepsScoresAndSwitchCount(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	observe(t, d, impl.EventKill, "cam-b", start)
	observe(t, d, impl.EventTeamfight, "cam-b", start.Add(time.Second))
	if shot, err := d.Select(start.Add(2 * time.Second)); err != nil || shot.CameraID != "cam-b" {
		t.Fatalf("select = %+v, %v; want cam-b", shot, err)
	}
	snap, err := d.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot = %d bytes, %v", len(snap), err)
	}
	restored := impl.NewSpectatorCameraDirector(directorConfig())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	score, err := restored.Score("cam-b")
	if err != nil || score != 70 {
		t.Fatalf("restored score = %d, %v; want 70", score, err)
	}
	if got := restored.SwitchCount(); got != 1 {
		t.Fatalf("restored switch count = %d, want 1", got)
	}
	current, ok := restored.Current()
	if !ok || current.CameraID != "cam-b" {
		t.Fatalf("restored current shot = %+v, %v; want cam-b", current, ok)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	d := impl.NewSpectatorCameraDirector(directorConfig())
	start := directorStart()
	observe(t, d, impl.EventKill, "cam-b", start)
	observe(t, d, impl.EventTeamfight, "cam-b", start.Add(time.Second))
	if _, err := d.Select(start.Add(2 * time.Second)); err != nil {
		t.Fatalf("select: %v", err)
	}
	if err := d.Restore([]byte("}{ broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	if score, err := d.Score("cam-b"); err != nil || score != 70 {
		t.Fatalf("score after failed restore = %d, %v; want the original 70", score, err)
	}
	if got := d.SwitchCount(); got != 1 {
		t.Fatalf("switch count after failed restore = %d, want 1", got)
	}
}
