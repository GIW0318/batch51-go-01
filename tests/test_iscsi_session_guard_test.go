package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-c-10"
)

const (
	initiatorA = "iqn.2024-01.com.example:host01"
	initiatorB = "iqn.2024-01.com.example:host02"
)

func iscsiPolicy() impl.IscsiPolicy {
	return impl.IscsiPolicy{
		MaxSessions:             8,
		MaxSessionsPerInitiator: 2,
		MaxLunsPerSession:       3,
		MaxMaskEntries:          8,
		ChapWindow:              3,
		RequireMutualAuth:       false,
		MaskGeneration:          1,
	}
}

func loginOK(t *testing.T, g *impl.IscsiSessionGuard, portal, initiator string) impl.Session {
	t.Helper()
	s, err := g.Login(portal, impl.LoginRequest{Initiator: initiator, Target: "iqn.2024-01.com.example:storage", CHAPMethod: "chap"})
	if err != nil {
		t.Fatalf("login on %s: %v", portal, err)
	}
	return s
}

func authOK(t *testing.T, g *impl.IscsiSessionGuard, s impl.Session, mutual bool) {
	t.Helper()
	if err := g.Auth(s.ID, impl.ChapResponse{ChallengeID: s.ChallengeID, Response: "0xdeadbeef", Mutual: mutual}); err != nil {
		t.Fatalf("auth for %s: %v", s.ID, err)
	}
}

func TestLoginCreatesPendingSession(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	if s.ID == "" {
		t.Fatalf("login must return a session id")
	}
	if s.State != impl.SessionPending {
		t.Fatalf("fresh session state = %q, want %q", s.State, impl.SessionPending)
	}
	if s.Portal != "10.0.0.11:3260" || s.Initiator != initiatorA {
		t.Fatalf("session = %+v, want the requested portal and initiator", s)
	}
	if s.ChallengeID == "" {
		t.Fatalf("login must issue a chap challenge")
	}
	if s.ExpCmdSN != 0 || s.Commands != 0 || s.Luns != 0 {
		t.Fatalf("fresh session counters = %+v, want all zero", s)
	}
	sessions := g.Sessions()
	if len(sessions) != 1 || sessions[0].ID != s.ID {
		t.Fatalf("sessions = %+v, want the new session", sessions)
	}
	if got := g.MaskGeneration(); got != 1 {
		t.Fatalf("mask generation = %d, want the configured start 1", got)
	}
}

func TestLoginRejectsInvalidInput(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	req := impl.LoginRequest{Initiator: initiatorA, Target: "iqn.2024-01.com.example:storage"}
	if _, err := g.Login("  ", req); !errors.Is(err, impl.ErrInvalidLogin) {
		t.Fatalf("blank portal must fail with ErrInvalidLogin, got %v", err)
	}
	if _, err := g.Login("10.0.0.11:3260", impl.LoginRequest{Target: req.Target}); !errors.Is(err, impl.ErrInvalidLogin) {
		t.Fatalf("blank initiator must fail with ErrInvalidLogin, got %v", err)
	}
	if _, err := g.Login("10.0.0.11:3260", impl.LoginRequest{Initiator: initiatorA}); !errors.Is(err, impl.ErrInvalidLogin) {
		t.Fatalf("blank target must fail with ErrInvalidLogin, got %v", err)
	}
	if got := len(g.Sessions()); got != 0 {
		t.Fatalf("rejected logins created %d sessions, want 0", got)
	}
}

func TestLoginEnforcesSessionLimit(t *testing.T) {
	cfg := iscsiPolicy()
	cfg.MaxSessions = 1
	cfg.MaxSessionsPerInitiator = 1
	g := impl.NewIscsiSessionGuard(cfg)
	loginOK(t, g, "10.0.0.11:3260", initiatorA)
	if _, err := g.Login("10.0.0.12:3260", impl.LoginRequest{Initiator: initiatorB, Target: "iqn.2024-01.com.example:storage"}); !errors.Is(err, impl.ErrSessionLimit) {
		t.Fatalf("second session must hit the cap with ErrSessionLimit, got %v", err)
	}
	if got := len(g.Sessions()); got != 1 {
		t.Fatalf("session table size = %d, want the cap 1", got)
	}
}

func TestLoginEnforcesPerInitiatorLimitConcurrently(t *testing.T) {
	cfg := iscsiPolicy()
	cfg.MaxSessions = 8
	cfg.MaxSessionsPerInitiator = 1
	g := impl.NewIscsiSessionGuard(cfg)
	var wg sync.WaitGroup
	var mu sync.Mutex
	started, limited := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := g.Login(fmt.Sprintf("10.0.0.%d:3260", i), impl.LoginRequest{Initiator: initiatorA, Target: "iqn.2024-01.com.example:storage"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				started++
			case errors.Is(err, impl.ErrSessionLimit):
				limited++
			}
		}(i)
	}
	wg.Wait()
	if started != 1 {
		t.Fatalf("concurrent logins admitted %d sessions for one initiator, want exactly 1", started)
	}
	if limited != 7 {
		t.Fatalf("concurrent logins reported %d limit errors, want 7", limited)
	}
	if got := len(g.Sessions()); got != 1 {
		t.Fatalf("session table size after the race = %d, want 1", got)
	}
}

func TestAuthRequiresMutualAnswer(t *testing.T) {
	cfg := iscsiPolicy()
	cfg.RequireMutualAuth = true
	g := impl.NewIscsiSessionGuard(cfg)
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	if err := g.Auth(s.ID, impl.ChapResponse{ChallengeID: s.ChallengeID, Response: "0xdeadbeef"}); !errors.Is(err, impl.ErrMutualAuthRequired) {
		t.Fatalf("missing target answer must fail with ErrMutualAuthRequired, got %v", err)
	}
	pending := g.Sessions()[0]
	if pending.State != impl.SessionPending {
		t.Fatalf("state after an incomplete handshake = %q, want pending", pending.State)
	}
	authOK(t, g, s, true)
	active := g.Sessions()[0]
	if active.State != impl.SessionActive {
		t.Fatalf("state after a complete handshake = %q, want active", active.State)
	}
}

func TestAuthRejectsChallengeMismatchAndReuse(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	if err := g.Auth(s.ID, impl.ChapResponse{ChallengeID: "chal-404", Response: "0x01"}); !errors.Is(err, impl.ErrChallengeMismatch) {
		t.Fatalf("wrong challenge id must fail with ErrChallengeMismatch, got %v", err)
	}
	authOK(t, g, s, false)
	if err := g.Auth(s.ID, impl.ChapResponse{ChallengeID: s.ChallengeID, Response: "0x02"}); !errors.Is(err, impl.ErrChallengeReused) {
		t.Fatalf("answering a consumed challenge must fail with ErrChallengeReused, got %v", err)
	}
}

func TestAuthEnforcesAttemptWindow(t *testing.T) {
	cfg := iscsiPolicy()
	cfg.ChapWindow = 2
	cfg.RequireMutualAuth = true
	g := impl.NewIscsiSessionGuard(cfg)
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	for i := 0; i < 2; i++ {
		if err := g.Auth(s.ID, impl.ChapResponse{ChallengeID: s.ChallengeID, Response: "0x00"}); !errors.Is(err, impl.ErrMutualAuthRequired) {
			t.Fatalf("attempt %d must fail with ErrMutualAuthRequired, got %v", i, err)
		}
	}
	if err := g.Auth(s.ID, impl.ChapResponse{ChallengeID: s.ChallengeID, Response: "0x00", Mutual: true}); !errors.Is(err, impl.ErrAuthLocked) {
		t.Fatalf("attempt past the window must fail with ErrAuthLocked, got %v", err)
	}
	if _, err := g.MapLun(s.ID, 0, initiatorA); !errors.Is(err, impl.ErrNotAuthenticated) {
		t.Fatalf("a locked session must stay unauthenticated, got %v", err)
	}
}

func TestAuthRejectsUnknownSession(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	if err := g.Auth("sess-404", impl.ChapResponse{ChallengeID: "x", Response: "0x01"}); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("unknown session must fail with ErrUnknownSession, got %v", err)
	}
	if err := g.Auth("", impl.ChapResponse{ChallengeID: "x", Response: "0x01"}); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("empty session id must fail with ErrUnknownSession, got %v", err)
	}
}

func TestMapLunRequiresAuthentication(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	if err := g.SetLunMask(initiatorA, []int{0, 1}); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	if _, err := g.MapLun(s.ID, 0, initiatorA); !errors.Is(err, impl.ErrNotAuthenticated) {
		t.Fatalf("mapping a lun before authentication must fail with ErrNotAuthenticated, got %v", err)
	}
}

func TestMapLunHonoursMask(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	if err := g.SetLunMask(initiatorA, []int{2, 0}); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	if got := g.MaskGeneration(); got != 2 {
		t.Fatalf("mask generation after a set = %d, want 2", got)
	}
	mask := g.LunMask(initiatorA)
	if len(mask) != 2 || mask[0] != 0 || mask[1] != 2 {
		t.Fatalf("mask = %v, want the ascending pair [0 2]", mask)
	}
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	authOK(t, g, s, false)
	grant, err := g.MapLun(s.ID, 0, initiatorA)
	if err != nil {
		t.Fatalf("mapping a masked lun: %v", err)
	}
	if grant.Lun != 0 || grant.SessionID != s.ID || grant.Initiator != initiatorA {
		t.Fatalf("grant = %+v, want lun 0 bound to the session and initiator", grant)
	}
	if grant.MaskGeneration != 2 {
		t.Fatalf("grant mask generation = %d, want the current 2", grant.MaskGeneration)
	}
	if _, err := g.MapLun(s.ID, 1, initiatorA); !errors.Is(err, impl.ErrLunDenied) {
		t.Fatalf("lun outside the mask must fail with ErrLunDenied, got %v", err)
	}
	if got := g.Sessions()[0].Luns; got != 1 {
		t.Fatalf("session lun count = %d, want 1 after a single grant", got)
	}
}

func TestMapLunRejectsInitiatorMismatch(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	if err := g.SetLunMask(initiatorA, []int{0}); err != nil {
		t.Fatalf("set mask for owner: %v", err)
	}
	if err := g.SetLunMask(initiatorB, []int{0}); err != nil {
		t.Fatalf("set mask for intruder: %v", err)
	}
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	authOK(t, g, s, false)
	if _, err := g.MapLun(s.ID, 0, initiatorB); !errors.Is(err, impl.ErrInitiatorMismatch) {
		t.Fatalf("a foreign initiator must fail with ErrInitiatorMismatch, got %v", err)
	}
	if _, err := g.MapLun(s.ID, 0, initiatorA); err != nil {
		t.Fatalf("the owning initiator must still map, got %v", err)
	}
}

func TestMapLunEnforcesPerSessionLimit(t *testing.T) {
	cfg := iscsiPolicy()
	cfg.MaxLunsPerSession = 2
	g := impl.NewIscsiSessionGuard(cfg)
	if err := g.SetLunMask(initiatorA, []int{0, 1, 2}); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	authOK(t, g, s, false)
	for _, lun := range []int{0, 1} {
		if _, err := g.MapLun(s.ID, lun, initiatorA); err != nil {
			t.Fatalf("mapping lun %d: %v", lun, err)
		}
	}
	if _, err := g.MapLun(s.ID, 2, initiatorA); !errors.Is(err, impl.ErrLunLimit) {
		t.Fatalf("third grant must hit the per session cap with ErrLunLimit, got %v", err)
	}
	if _, err := g.MapLun(s.ID, 0, initiatorA); err != nil {
		t.Fatalf("re-mapping an already granted lun must stay idempotent, got %v", err)
	}
	if got := g.Sessions()[0].Luns; got != 2 {
		t.Fatalf("session lun count = %d, want 2", got)
	}
}

func TestRecoverTracksCmdSNIdempotently(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	authOK(t, g, s, false)
	first, err := g.Recover(s.ID, 4)
	if err != nil {
		t.Fatalf("first recover: %v", err)
	}
	if first.ExpCmdSN != 5 || first.Commands != 1 {
		t.Fatalf("after cmd 4: exp=%d commands=%d, want 5 and 1", first.ExpCmdSN, first.Commands)
	}
	replay, err := g.Recover(s.ID, 4)
	if err != nil {
		t.Fatalf("replayed recover: %v", err)
	}
	if replay.ExpCmdSN != 5 || replay.Commands != 1 {
		t.Fatalf("replayed cmd 4 changed the window: exp=%d commands=%d", replay.ExpCmdSN, replay.Commands)
	}
	next, err := g.Recover(s.ID, 9)
	if err != nil {
		t.Fatalf("second recover: %v", err)
	}
	if next.ExpCmdSN != 10 || next.Commands != 2 {
		t.Fatalf("after cmd 9: exp=%d commands=%d, want 10 and 2", next.ExpCmdSN, next.Commands)
	}
	pending := loginOK(t, g, "10.0.0.12:3260", initiatorB)
	if _, err := g.Recover(pending.ID, 1); !errors.Is(err, impl.ErrNotAuthenticated) {
		t.Fatalf("recovering a pending session must fail with ErrNotAuthenticated, got %v", err)
	}
}

func TestLogoutClosesSession(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	if err := g.SetLunMask(initiatorA, []int{0}); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	authOK(t, g, s, false)
	if _, err := g.MapLun(s.ID, 0, initiatorA); err != nil {
		t.Fatalf("map lun: %v", err)
	}
	if err := g.Logout(s.ID); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := len(g.Sessions()); got != 0 {
		t.Fatalf("session table size after logout = %d, want 0", got)
	}
	if _, err := g.Recover(s.ID, 1); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("recovering a closed session must fail with ErrUnknownSession, got %v", err)
	}
	if _, err := g.MapLun(s.ID, 0, initiatorA); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("mapping on a closed session must fail with ErrUnknownSession, got %v", err)
	}
	if err := g.Logout(s.ID); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("logging out twice must fail with ErrUnknownSession, got %v", err)
	}
}

func TestSnapshotRestoreKeepsSessionsAndMasks(t *testing.T) {
	g := impl.NewIscsiSessionGuard(iscsiPolicy())
	if err := g.SetLunMask(initiatorA, []int{0, 2}); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	s := loginOK(t, g, "10.0.0.11:3260", initiatorA)
	authOK(t, g, s, false)
	if _, err := g.MapLun(s.ID, 0, initiatorA); err != nil {
		t.Fatalf("map lun: %v", err)
	}
	if _, err := g.Recover(s.ID, 7); err != nil {
		t.Fatalf("recover: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewIscsiSessionGuard(iscsiPolicy())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	sessions := restored.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("restored session count = %d, want 1", len(sessions))
	}
	if sessions[0].ID != s.ID || sessions[0].State != impl.SessionActive {
		t.Fatalf("restored session = %+v, want the active original", sessions[0])
	}
	if sessions[0].ExpCmdSN != 8 || sessions[0].Commands != 1 {
		t.Fatalf("restored watermark exp=%d commands=%d, want 8 and 1", sessions[0].ExpCmdSN, sessions[0].Commands)
	}
	mask := restored.LunMask(initiatorA)
	if len(mask) != 2 || mask[0] != 0 || mask[1] != 2 {
		t.Fatalf("restored mask = %v, want [0 2]", mask)
	}
	if restored.MaskGeneration() != g.MaskGeneration() {
		t.Fatalf("restored mask generation = %d, want %d", restored.MaskGeneration(), g.MaskGeneration())
	}
	next, err := restored.Recover(s.ID, 8)
	if err != nil {
		t.Fatalf("recover after restore: %v", err)
	}
	if next.ExpCmdSN != 9 || next.Commands != 2 {
		t.Fatalf("recover after restore gave exp=%d commands=%d, want 9 and 2", next.ExpCmdSN, next.Commands)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(restored.Sessions()); got != 1 {
		t.Fatalf("rejected restore dropped sessions, now %d want 1", got)
	}
}
