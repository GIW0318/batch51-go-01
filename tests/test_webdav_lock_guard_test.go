package task

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-b-18"
)

func clockBase() time.Time {
	return time.Date(2026, 2, 3, 9, 0, 0, 0, time.UTC)
}

func lockPolicy() impl.LockPolicy {
	return impl.LockPolicy{MaxTimeout: time.Hour, MaxLocks: 64}
}

func exclusiveRequest(timeout time.Duration) impl.LockRequest {
	return impl.LockRequest{Owner: "alice", Depth: impl.DepthInfinity, Scope: impl.ScopeExclusive, Timeout: timeout}
}

func sharedRequest(timeout time.Duration) impl.LockRequest {
	return impl.LockRequest{Owner: "bob", Depth: impl.DepthZero, Scope: impl.ScopeShared, Timeout: timeout}
}

func mustLock(t *testing.T, g *impl.WebdavLockGuard, path string, req impl.LockRequest) impl.LockToken {
	t.Helper()
	tok, err := g.Lock(path, req)
	if err != nil {
		t.Fatalf("lock %s failed: %v", path, err)
	}
	return tok
}

func TestLockValidatesPathDepthScopeTimeout(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	if _, err := g.Lock("", exclusiveRequest(10*time.Minute)); !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("empty path = %v, want ErrBadPath", err)
	}
	if _, err := g.Lock("dav/a", exclusiveRequest(10*time.Minute)); !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("relative path = %v, want ErrBadPath", err)
	}
	badDepth := exclusiveRequest(10 * time.Minute)
	badDepth.Depth = "1"
	if _, err := g.Lock("/dav/a", badDepth); !errors.Is(err, impl.ErrBadDepth) {
		t.Fatalf("depth 1 = %v, want ErrBadDepth", err)
	}
	badScope := exclusiveRequest(10 * time.Minute)
	badScope.Scope = "write"
	if _, err := g.Lock("/dav/a", badScope); !errors.Is(err, impl.ErrBadScope) {
		t.Fatalf("scope write = %v, want ErrBadScope", err)
	}
	if _, err := g.Lock("/dav/a", exclusiveRequest(0)); !errors.Is(err, impl.ErrBadTimeout) {
		t.Fatalf("zero timeout = %v, want ErrBadTimeout", err)
	}
}

func TestLockGrantsExclusiveDepthInfinity(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	t0 := clockBase()
	g.SetClock(t0)
	tok := mustLock(t, g, "/dav/a", exclusiveRequest(10*time.Minute))
	if !strings.HasPrefix(tok.Token, "opaquelocktoken:") {
		t.Fatalf("token = %q, want an opaquelocktoken prefix", tok.Token)
	}
	if tok.Path != "/dav/a" || tok.Owner != "alice" {
		t.Fatalf("granted lock = %+v, want path /dav/a owner alice", tok)
	}
	if tok.Depth != impl.DepthInfinity || tok.Scope != impl.ScopeExclusive {
		t.Fatalf("depth/scope = %q/%q, want infinity/exclusive", tok.Depth, tok.Scope)
	}
	if !tok.Expires.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("expiry = %v, want %v", tok.Expires, t0.Add(10*time.Minute))
	}
	if tok.Rev == 0 {
		t.Fatalf("lock revision must advance")
	}
	read, ok := g.LockOf(tok.Token)
	if !ok || read.Path != "/dav/a" {
		t.Fatalf("lock of token = %+v ok=%v", read, ok)
	}
	if locks := g.LocksOn("/dav/a"); len(locks) != 1 || locks[0].Token != tok.Token {
		t.Fatalf("locks on path = %+v, want exactly the granted token", locks)
	}
}

func TestDepthInfinityLockBlocksChildLock(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	mustLock(t, g, "/dav/a", exclusiveRequest(10*time.Minute))
	if _, err := g.Lock("/dav/a/b", exclusiveRequest(10*time.Minute)); !errors.Is(err, impl.ErrLockConflict) {
		t.Fatalf("child lock under a depth infinity lock = %v, want ErrLockConflict", err)
	}
	if locks := g.LocksOn("/dav/a/b"); len(locks) != 0 {
		t.Fatalf("rejected child lock left %d entries behind", len(locks))
	}
}

func TestSharedAndExclusiveSemanticsOnSamePath(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	first := mustLock(t, g, "/dav/a", sharedRequest(5*time.Minute))
	second := mustLock(t, g, "/dav/a", sharedRequest(5*time.Minute))
	if first.Token == second.Token {
		t.Fatalf("two shared locks received the same token %q", first.Token)
	}
	locks := g.LocksOn("/dav/a")
	if len(locks) != 2 {
		t.Fatalf("locks on path = %d, want 2 shared locks", len(locks))
	}
	for _, lock := range locks {
		if lock.Scope != impl.ScopeShared {
			t.Fatalf("lock %+v is not shared", lock)
		}
	}
	if _, err := g.Lock("/dav/a", exclusiveRequest(5*time.Minute)); !errors.Is(err, impl.ErrLockConflict) {
		t.Fatalf("exclusive lock over shared locks = %v, want ErrLockConflict", err)
	}
	if locks := g.LocksOn("/dav/a"); len(locks) != 2 {
		t.Fatalf("rejected exclusive lock changed the table, len = %d", len(locks))
	}
}

func TestUnlockRemovesToken(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	tok := mustLock(t, g, "/dav/a", exclusiveRequest(5*time.Minute))
	if err := g.Unlock("/dav/a", tok.Token); err != nil {
		t.Fatalf("unlock failed: %v", err)
	}
	if locks := g.LocksOn("/dav/a"); len(locks) != 0 {
		t.Fatalf("locks after unlock = %+v, want none", locks)
	}
	if _, ok := g.LockOf(tok.Token); ok {
		t.Fatalf("released token is still readable")
	}
	if err := g.Unlock("/dav/a", tok.Token); !errors.Is(err, impl.ErrLockNotFound) {
		t.Fatalf("second unlock = %v, want ErrLockNotFound", err)
	}
}

func TestRefreshExtendsExpiry(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	t0 := clockBase()
	g.SetClock(t0)
	tok := mustLock(t, g, "/dav/a", exclusiveRequest(10*time.Minute))
	g.SetClock(t0.Add(5 * time.Minute))
	refreshed, err := g.Refresh(tok.Token, 20*time.Minute)
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if !refreshed.Expires.Equal(t0.Add(25 * time.Minute)) {
		t.Fatalf("refreshed expiry = %v, want %v", refreshed.Expires, t0.Add(25*time.Minute))
	}
	if refreshed.Rev <= tok.Rev {
		t.Fatalf("refresh revision = %d, want greater than %d", refreshed.Rev, tok.Rev)
	}
	if read, ok := g.LockOf(tok.Token); !ok || !read.Expires.Equal(refreshed.Expires) {
		t.Fatalf("stored lock after refresh = %+v ok=%v", read, ok)
	}
}

func TestRefreshExpiredLockRejected(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	t0 := clockBase()
	g.SetClock(t0)
	tok := mustLock(t, g, "/dav/a", exclusiveRequest(time.Minute))
	g.SetClock(t0.Add(2 * time.Minute))
	if _, err := g.Refresh(tok.Token, 5*time.Minute); !errors.Is(err, impl.ErrLockExpired) {
		t.Fatalf("refresh of an expired lock = %v, want ErrLockExpired", err)
	}
}

func TestRefreshClampedToPolicyMax(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	t0 := clockBase()
	g.SetClock(t0)
	tok := mustLock(t, g, "/dav/a", exclusiveRequest(10*time.Minute))
	g.SetClock(t0.Add(time.Minute))
	refreshed, err := g.Refresh(tok.Token, 10*time.Hour)
	if err != nil {
		t.Fatalf("refresh with oversized timeout failed: %v", err)
	}
	if !refreshed.Expires.Equal(t0.Add(time.Hour + time.Minute)) {
		t.Fatalf("refreshed expiry = %v, want %v", refreshed.Expires, t0.Add(time.Hour+time.Minute))
	}
}

func TestCheckIfMatchesTokenAndETag(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	if err := g.Tag("/dav/a", "\"v1\""); err != nil {
		t.Fatalf("tag failed: %v", err)
	}
	tok := mustLock(t, g, "/dav/a", exclusiveRequest(5*time.Minute))
	result, err := g.CheckIf("/dav/a", []impl.IfCondition{{Token: tok.Token, ETag: "\"v1\""}})
	if err != nil {
		t.Fatalf("check if failed: %v", err)
	}
	if !result.OK || result.Status != 0 {
		t.Fatalf("matching conditions = %+v, want OK with status 0", result)
	}
}

func TestCheckIfETagMismatchIs412(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	if err := g.Tag("/dav/a", "\"v1\""); err != nil {
		t.Fatalf("tag failed: %v", err)
	}
	tok := mustLock(t, g, "/dav/a", exclusiveRequest(5*time.Minute))
	result, err := g.CheckIf("/dav/a", []impl.IfCondition{{Token: tok.Token, ETag: "\"v2\""}})
	if err != nil {
		t.Fatalf("check if failed: %v", err)
	}
	if result.OK || result.Status != impl.StatusPreconditionFailed {
		t.Fatalf("stale etag = %+v, want status 412", result)
	}
	if result.Reason != impl.ReasonETagMismatch {
		t.Fatalf("reason = %q, want %q", result.Reason, impl.ReasonETagMismatch)
	}
}

func TestCheckIfLockedWithoutTokenIs423(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	mustLock(t, g, "/dav/a", exclusiveRequest(5*time.Minute))
	result, err := g.CheckIf("/dav/a", nil)
	if err != nil {
		t.Fatalf("check if failed: %v", err)
	}
	if result.OK || result.Status != impl.StatusLocked {
		t.Fatalf("unconditional write on a locked resource = %+v, want status 423", result)
	}
	if result.Reason != impl.ReasonLocked {
		t.Fatalf("reason = %q, want %q", result.Reason, impl.ReasonLocked)
	}
}

func TestExpireReclaimsLazilyAndSnapshotRestores(t *testing.T) {
	cfg := lockPolicy()
	g := impl.NewWebdavLockGuard(cfg)
	t0 := clockBase()
	g.SetClock(t0)
	short := mustLock(t, g, "/dav/a", exclusiveRequest(time.Minute))
	long := mustLock(t, g, "/dav/b", exclusiveRequest(10*time.Minute))
	if reclaimed := g.Expire(t0.Add(2 * time.Minute)); reclaimed != 1 {
		t.Fatalf("expire reclaimed %d locks, want 1", reclaimed)
	}
	if locks := g.LocksOn("/dav/a"); len(locks) != 0 {
		t.Fatalf("expired lock still listed: %+v", locks)
	}
	if _, ok := g.LockOf(short.Token); ok {
		t.Fatalf("expired token is still readable")
	}
	if locks := g.LocksOn("/dav/b"); len(locks) != 1 {
		t.Fatalf("live lock disappeared during expire: %+v", locks)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	restored := impl.NewWebdavLockGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	read, ok := restored.LockOf(long.Token)
	if !ok {
		t.Fatalf("restored guard lost the live token")
	}
	if !read.Expires.Equal(long.Expires) || read.Path != "/dav/b" {
		t.Fatalf("restored lock = %+v, want path /dav/b expiry %v", read, long.Expires)
	}
	if reclaimed := restored.Expire(t0.Add(11 * time.Minute)); reclaimed != 1 {
		t.Fatalf("expire after restore reclaimed %d locks, want 1", reclaimed)
	}
}

func TestMoveCarriesLockToDestination(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	tok := mustLock(t, g, "/dav/a", exclusiveRequest(10*time.Minute))
	if err := g.Move("/dav/a", "/dav/b", tok.Token); err != nil {
		t.Fatalf("move failed: %v", err)
	}
	moved, ok := g.LockOf(tok.Token)
	if !ok {
		t.Fatalf("lock vanished after the move")
	}
	if moved.Path != "/dav/b" {
		t.Fatalf("lock path after move = %q, want /dav/b", moved.Path)
	}
	if locks := g.LocksOn("/dav/a"); len(locks) != 0 {
		t.Fatalf("source path still lists locks: %+v", locks)
	}
	if _, err := g.Lock("/dav/b/child", exclusiveRequest(5*time.Minute)); !errors.Is(err, impl.ErrLockConflict) {
		t.Fatalf("moved depth infinity lock no longer covers children: %v", err)
	}
}

func TestMoveIntoLockedTargetRejected(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	source := mustLock(t, g, "/dav/a", exclusiveRequest(10*time.Minute))
	mustLock(t, g, "/dav/b", exclusiveRequest(10*time.Minute))
	if err := g.Move("/dav/a", "/dav/b", source.Token); !errors.Is(err, impl.ErrLockConflict) {
		t.Fatalf("move onto a locked target = %v, want ErrLockConflict", err)
	}
	if locks := g.LocksOn("/dav/a"); len(locks) != 1 {
		t.Fatalf("failed move changed the source lock table: %+v", locks)
	}
}

func TestConcurrentExclusiveLockSingleWinner(t *testing.T) {
	g := impl.NewWebdavLockGuard(lockPolicy())
	g.SetClock(clockBase())
	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	grants := 0
	conflicts := 0
	other := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.Lock("/dav/a", exclusiveRequest(5*time.Minute))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				grants++
			case errors.Is(err, impl.ErrLockConflict):
				conflicts++
			default:
				other++
			}
		}()
	}
	wg.Wait()
	if grants != 1 {
		t.Fatalf("concurrent exclusive locks granted %d, want exactly 1", grants)
	}
	if conflicts != workers-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, workers-1)
	}
	if other != 0 {
		t.Fatalf("unexpected errors = %d", other)
	}
	if locks := g.LocksOn("/dav/a"); len(locks) != 1 {
		t.Fatalf("lock table after concurrent locks = %d entries, want 1", len(locks))
	}
}
