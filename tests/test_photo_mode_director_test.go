package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-game-a-11"
)

func photoConfig() impl.PhotoConfig {
	return impl.PhotoConfig{
		Bounds: impl.SceneBounds{
			Min: impl.Vec3{X: -50, Y: 0, Z: -50},
			Max: impl.Vec3{X: 50, Y: 40, Z: 50},
		},
		MinDistance: 3,
		MaxPerFrame: 2,
		MaxPending:  4,
		MaxBytes:    1000,
		MaxSessions: 2,
	}
}

func photoStart() time.Time {
	return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
}

func mustEnter(t *testing.T, p *impl.PhotoModeDirector, player string, at time.Time) impl.Session {
	t.Helper()
	s, err := p.Enter(player, at)
	if err != nil {
		t.Fatalf("enter photo mode for %q: %v", player, err)
	}
	return s
}

func TestEnterReturnsFrozenSession(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	start := photoStart()
	s := mustEnter(t, p, "player-1", start)
	if s.ID == "" {
		t.Fatalf("session id must not be empty")
	}
	if s.Seq != 1 {
		t.Fatalf("first session sequence = %d, want 1", s.Seq)
	}
	if !s.WorldTime.Equal(start) {
		t.Fatalf("session world time = %s, want frozen at %s", s.WorldTime, start)
	}
	if s.PlayerID != "player-1" {
		t.Fatalf("session player = %q, want player-1", s.PlayerID)
	}
	got, ok := p.Session(s.ID)
	if !ok || got.ID != s.ID {
		t.Fatalf("session lookup = %+v, %v; want the session just created", got, ok)
	}
	if live := p.Sessions(); len(live) != 1 {
		t.Fatalf("live sessions = %d, want 1", len(live))
	}
}

func TestEnterRejectsBlankPlayer(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	if _, err := p.Enter("   ", photoStart()); !errors.Is(err, impl.ErrBlankPlayer) {
		t.Fatalf("blank player id must be rejected with ErrBlankPlayer, got %v", err)
	}
	if live := p.Sessions(); len(live) != 0 {
		t.Fatalf("rejected enter must not open a session, got %d", len(live))
	}
}

func TestEnterRejectsDuplicatePlayer(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	start := photoStart()
	first := mustEnter(t, p, "player-1", start)
	second, err := p.Enter("player-1", start.Add(time.Second))
	if !errors.Is(err, impl.ErrAlreadyInPhotoMode) {
		t.Fatalf("second enter by the same player must fail with ErrAlreadyInPhotoMode, got %v", err)
	}
	if second.ID != "" {
		t.Fatalf("rejected enter must not return a session, got %q", second.ID)
	}
	if live := p.Sessions(); len(live) != 1 || live[0].ID != first.ID {
		t.Fatalf("live sessions = %+v, want the original session only", live)
	}
}

func TestEnterEnforcesSessionLimit(t *testing.T) {
	cfg := photoConfig()
	cfg.MaxSessions = 2
	p := impl.NewPhotoModeDirector(cfg)
	start := photoStart()
	first := mustEnter(t, p, "player-1", start)
	second := mustEnter(t, p, "player-2", start.Add(time.Second))
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("session sequences = %d,%d, want 1,2", first.Seq, second.Seq)
	}
	if _, err := p.Enter("player-3", start.Add(2*time.Second)); !errors.Is(err, impl.ErrSessionLimit) {
		t.Fatalf("third session must fail with ErrSessionLimit, got %v", err)
	}
	if live := p.Sessions(); len(live) != 2 {
		t.Fatalf("live sessions = %d, want 2", len(live))
	}
}

func TestAdvanceKeepsWorldClockWhileLightingRuns(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	start := photoStart()
	s := mustEnter(t, p, "player-1", start)
	got, err := p.Advance(s.ID, 90*time.Second)
	if err != nil {
		t.Fatalf("advance during photo mode: %v", err)
	}
	if !got.Equal(start) {
		t.Fatalf("world clock advanced to %s, want frozen at %s", got, start)
	}
	stored, ok := p.Session(s.ID)
	if !ok || !stored.WorldTime.Equal(start) {
		t.Fatalf("stored world time = %s, want frozen at %s", stored.WorldTime, start)
	}
	light, err := p.Lighting(s.ID, start.Add(90*time.Second))
	if err != nil {
		t.Fatalf("lighting lookup: %v", err)
	}
	if light.Elapsed != 90*time.Second {
		t.Fatalf("ambient elapsed = %s, want 90s", light.Elapsed)
	}
}

func TestSetCameraClampsToSceneBounds(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	s := mustEnter(t, p, "player-1", photoStart())
	pose := impl.CameraPose{
		Position: impl.Vec3{X: 200, Y: 5, Z: -300},
		Target:   impl.Vec3{X: 0, Y: 0, Z: 0},
	}
	if err := p.SetCamera(s.ID, pose); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	got, err := p.Pose(s.ID)
	if err != nil {
		t.Fatalf("read pose: %v", err)
	}
	want := impl.Vec3{X: 50, Y: 5, Z: -50}
	if got.Position != want {
		t.Fatalf("clamped position = %+v, want %+v", got.Position, want)
	}
	if got.Target != pose.Target {
		t.Fatalf("target = %+v, want %+v", got.Target, pose.Target)
	}
}

func TestSetCameraEnforcesMinDistance(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	s := mustEnter(t, p, "player-1", photoStart())
	pose := impl.CameraPose{
		Position: impl.Vec3{X: 0, Y: 0, Z: 0},
		Target:   impl.Vec3{X: 0, Y: 0, Z: 0},
	}
	if err := p.SetCamera(s.ID, pose); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	got, err := p.Pose(s.ID)
	if err != nil {
		t.Fatalf("read pose: %v", err)
	}
	want := impl.Vec3{X: 0, Y: 0, Z: 3}
	if got.Position != want {
		t.Fatalf("position on zero distance = %+v, want %+v", got.Position, want)
	}
}

func TestSetCameraRejectsUnknownSession(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	pose := impl.CameraPose{Position: impl.Vec3{X: 5, Y: 5, Z: 5}}
	if err := p.SetCamera("no-such-session", pose); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("unknown session must be rejected with ErrUnknownSession, got %v", err)
	}
	s := mustEnter(t, p, "player-1", photoStart())
	if err := p.SetCamera(s.ID, pose); err != nil {
		t.Fatalf("live session must accept a pose, got %v", err)
	}
	got, err := p.Pose(s.ID)
	if err != nil || got.Position != pose.Position {
		t.Fatalf("pose = %+v, %v; want %+v", got.Position, err, pose.Position)
	}
}

func TestHideRecordsSortedUniqueKinds(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	s := mustEnter(t, p, "player-1", photoStart())
	if err := p.Hide(s.ID, []impl.EntityKind{impl.KindProp, impl.KindMonster, impl.KindProp}); err != nil {
		t.Fatalf("hide kinds: %v", err)
	}
	hidden, err := p.Hidden(s.ID)
	if err != nil {
		t.Fatalf("hidden lookup: %v", err)
	}
	if len(hidden) != 2 || hidden[0] != impl.KindMonster || hidden[1] != impl.KindProp {
		t.Fatalf("hidden kinds = %+v, want [monster prop]", hidden)
	}
	visible, err := p.Renderables(s.ID)
	if err != nil {
		t.Fatalf("renderables lookup: %v", err)
	}
	if len(visible) != 2 || visible[0] != impl.KindLoot || visible[1] != impl.KindPlayer {
		t.Fatalf("renderables = %+v, want [loot player]", visible)
	}
}

func TestCaptureQueuesSequentialTickets(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	s := mustEnter(t, p, "player-1", photoStart())
	var tickets []impl.Ticket
	for _, label := range []string{"a", "b", "c"} {
		tk, err := p.Capture(s.ID, impl.CaptureRequest{Width: 1920, Height: 1080, Bytes: 100, Label: label})
		if err != nil {
			t.Fatalf("capture %q: %v", label, err)
		}
		tickets = append(tickets, tk)
	}
	if tickets[0].Number != 1 || tickets[1].Number != 2 || tickets[2].Number != 3 {
		t.Fatalf("ticket numbers = %d,%d,%d, want 1,2,3", tickets[0].Number, tickets[1].Number, tickets[2].Number)
	}
	if tickets[0].Frame != 1 || tickets[1].Frame != 1 || tickets[2].Frame != 2 {
		t.Fatalf("ticket frames = %d,%d,%d, want 1,1,2", tickets[0].Frame, tickets[1].Frame, tickets[2].Frame)
	}
	for _, tk := range tickets {
		if tk.ID == "" || tk.State != impl.TicketQueued || tk.SessionID != s.ID {
			t.Fatalf("ticket = %+v, want a queued ticket of session %s", tk, s.ID)
		}
	}
	pending, err := p.Pending(s.ID)
	if err != nil {
		t.Fatalf("pending lookup: %v", err)
	}
	if len(pending) != 3 || pending[2].Number != 3 {
		t.Fatalf("pending tickets = %+v, want three by ascending number", pending)
	}
	used, err := p.StorageUsed(s.ID)
	if err != nil || used != 300 {
		t.Fatalf("storage used = %d, %v; want 300", used, err)
	}
}

func TestCaptureEnforcesStorageBudget(t *testing.T) {
	cfg := photoConfig()
	cfg.MaxBytes = 250
	cfg.MaxPending = 8
	p := impl.NewPhotoModeDirector(cfg)
	s := mustEnter(t, p, "player-1", photoStart())
	first, err := p.Capture(s.ID, impl.CaptureRequest{Width: 800, Height: 600, Bytes: 100, Label: "a"})
	if err != nil {
		t.Fatalf("first capture: %v", err)
	}
	if _, err := p.Capture(s.ID, impl.CaptureRequest{Width: 800, Height: 600, Bytes: 100, Label: "b"}); err != nil {
		t.Fatalf("second capture: %v", err)
	}
	if _, err := p.Capture(s.ID, impl.CaptureRequest{Width: 800, Height: 600, Bytes: 100, Label: "c"}); !errors.Is(err, impl.ErrStorageFull) {
		t.Fatalf("over budget capture must fail with ErrStorageFull, got %v", err)
	}
	if err := p.Complete(first.ID); err != nil {
		t.Fatalf("complete first ticket: %v", err)
	}
	used, err := p.StorageUsed(s.ID)
	if err != nil || used != 100 {
		t.Fatalf("storage after complete = %d, %v; want 100", used, err)
	}
	third, err := p.Capture(s.ID, impl.CaptureRequest{Width: 800, Height: 600, Bytes: 100, Label: "c"})
	if err != nil {
		t.Fatalf("capture after releasing budget: %v", err)
	}
	if third.Number != 3 {
		t.Fatalf("replacement ticket number = %d, want 3", third.Number)
	}
	used, err = p.StorageUsed(s.ID)
	if err != nil || used != 200 {
		t.Fatalf("storage after replacement = %d, %v; want 200", used, err)
	}
}

func TestCaptureRejectsQueueFull(t *testing.T) {
	cfg := photoConfig()
	cfg.MaxPending = 2
	cfg.MaxBytes = 1000
	p := impl.NewPhotoModeDirector(cfg)
	s := mustEnter(t, p, "player-1", photoStart())
	for i := 0; i < 2; i++ {
		if _, err := p.Capture(s.ID, impl.CaptureRequest{Width: 640, Height: 480, Bytes: 10}); err != nil {
			t.Fatalf("capture %d: %v", i+1, err)
		}
	}
	if _, err := p.Capture(s.ID, impl.CaptureRequest{Width: 640, Height: 480, Bytes: 10}); !errors.Is(err, impl.ErrQueueFull) {
		t.Fatalf("capture beyond MaxPending must fail with ErrQueueFull, got %v", err)
	}
	pending, err := p.Pending(s.ID)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %+v, %v; want two queued tickets", pending, err)
	}
}

func TestExitReleasesSession(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	s := mustEnter(t, p, "player-1", photoStart())
	if _, err := p.Capture(s.ID, impl.CaptureRequest{Width: 800, Height: 600, Bytes: 50}); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if err := p.Exit(s.ID); err != nil {
		t.Fatalf("exit: %v", err)
	}
	if _, ok := p.Session(s.ID); ok {
		t.Fatalf("session still visible after exit")
	}
	if _, err := p.Pending(s.ID); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("pending after exit = %v, want ErrUnknownSession", err)
	}
	if _, err := p.Capture(s.ID, impl.CaptureRequest{Width: 800, Height: 600, Bytes: 50}); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("capture after exit = %v, want ErrUnknownSession", err)
	}
	if err := p.Exit(s.ID); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("second exit = %v, want ErrUnknownSession", err)
	}
}

func TestSnapshotRestoreKeepsSessionsAndQueue(t *testing.T) {
	cfg := photoConfig()
	p := impl.NewPhotoModeDirector(cfg)
	start := photoStart()
	s := mustEnter(t, p, "player-1", start)
	if err := p.Hide(s.ID, []impl.EntityKind{impl.KindMonster, impl.KindProp}); err != nil {
		t.Fatalf("hide: %v", err)
	}
	pose := impl.CameraPose{Position: impl.Vec3{X: 10, Y: 5, Z: 10}, Target: impl.Vec3{X: 0, Y: 0, Z: 0}}
	if err := p.SetCamera(s.ID, pose); err != nil {
		t.Fatalf("set camera: %v", err)
	}
	if _, err := p.Capture(s.ID, impl.CaptureRequest{Width: 800, Height: 600, Bytes: 40, Label: "first"}); err != nil {
		t.Fatalf("first capture: %v", err)
	}
	if _, err := p.Capture(s.ID, impl.CaptureRequest{Width: 800, Height: 600, Bytes: 60, Label: "second"}); err != nil {
		t.Fatalf("second capture: %v", err)
	}
	snap, err := p.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot = %d bytes, %v; want a non empty payload", len(snap), err)
	}
	restored := impl.NewPhotoModeDirector(photoConfig())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, ok := restored.Session(s.ID)
	if !ok || got.PlayerID != "player-1" || !got.WorldTime.Equal(start) {
		t.Fatalf("restored session = %+v, %v", got, ok)
	}
	hidden, err := restored.Hidden(s.ID)
	if err != nil || len(hidden) != 2 || hidden[0] != impl.KindMonster {
		t.Fatalf("restored hidden = %+v, %v", hidden, err)
	}
	restoredPose, err := restored.Pose(s.ID)
	if err != nil || restoredPose.Position != pose.Position {
		t.Fatalf("restored pose = %+v, %v; want %+v", restoredPose.Position, err, pose.Position)
	}
	pending, err := restored.Pending(s.ID)
	if err != nil || len(pending) != 2 || pending[0].Label != "first" || pending[1].Label != "second" {
		t.Fatalf("restored pending = %+v, %v", pending, err)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	p := impl.NewPhotoModeDirector(photoConfig())
	s := mustEnter(t, p, "player-1", photoStart())
	if err := p.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	if _, ok := p.Session(s.ID); !ok {
		t.Fatalf("failed restore must keep live sessions")
	}
}
