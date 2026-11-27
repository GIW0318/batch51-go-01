package task

import (
	"errors"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-c-18"
)

type sysrqClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *sysrqClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *sysrqClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type sysrqPort struct {
	mu     sync.Mutex
	writes []string
}

func (p *sysrqPort) Write(keys string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, keys)
	return nil
}

func (p *sysrqPort) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.writes)
}

func (p *sysrqPort) Writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.writes))
	copy(out, p.writes)
	return out
}

func newSysrqGuard(port impl.TriggerPort, cfg impl.Config) *impl.SysrqTriggerGuard {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Unix(1700000000, 0) }
	}
	if cfg.MaxAudit == 0 {
		cfg.MaxAudit = 16
	}
	if cfg.MaxTickets == 0 {
		cfg.MaxTickets = 8
	}
	if cfg.DedupWindow == 0 {
		cfg.DedupWindow = 100 * time.Millisecond
	}
	if cfg.TicketTTL == 0 {
		cfg.TicketTTL = 2 * time.Second
	}
	return impl.NewSysrqTriggerGuard(cfg, port)
}

func sysrqCaller() impl.Caller {
	return impl.Caller{PID: 4242, UID: 0, Caps: 1 << 21}
}

func TestAllowedKeyIsWrittenAndExplained(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{RequiredCap: 1 << 21})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	if got := g.Mask(); got != "7" {
		t.Fatalf("mask = %q, want %q", got, "7")
	}
	if got := g.MaskGeneration(); got != 1 {
		t.Fatalf("mask generation = %d, want 1", got)
	}
	verdicts, err := g.Explain("e")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if len(verdicts) != 1 {
		t.Fatalf("explain returned %d verdicts, want 1", len(verdicts))
	}
	if verdicts[0].Key != "e" || verdicts[0].Tier != impl.TierAllow || !verdicts[0].Enabled {
		t.Fatalf("explain verdict = %+v, want an enabled allow tier key", verdicts[0])
	}
	res, err := g.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if !res.Written || res.Deduped || res.Generation != 1 {
		t.Fatalf("trigger result = %+v, want a written result of generation 1", res)
	}
	if port.Count() != 1 || port.Writes()[0] != "e" {
		t.Fatalf("port writes = %v, want exactly e", port.Writes())
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "z", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("unknown key must report ErrUnknownKey, got %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrUnknownKey) {
		t.Fatalf("an empty key list must report ErrUnknownKey, got %v", err)
	}
	if port.Count() != 0 {
		t.Fatalf("rejected keys must not reach the port, got %v", port.Writes())
	}
}

func TestForbiddenKeyIsAlwaysRejected(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if err := g.SetMask("1"); err != nil {
		t.Fatalf("set all enabled mask: %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "i", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrKeyForbidden) {
		t.Fatalf("forbidden key must report ErrKeyForbidden even with a full mask, got %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "k", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrKeyForbidden) {
		t.Fatalf("second forbidden key must report ErrKeyForbidden, got %v", err)
	}
	if port.Count() != 0 {
		t.Fatalf("forbidden keys must not reach the port, got %v", port.Writes())
	}
}

func TestMaskZeroDisablesEverything(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if err := g.SetMask("0"); err != nil {
		t.Fatalf("set zero mask: %v", err)
	}
	if got := g.Mask(); got != "0" {
		t.Fatalf("mask = %q, want %q", got, "0")
	}
	if _, err := g.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrKeyDisabled) {
		t.Fatalf("a fully disabled mask must report ErrKeyDisabled, got %v", err)
	}
	if err := g.SetMask("1"); err != nil {
		t.Fatalf("set legacy all mask: %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "b", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrTicketRequired) {
		t.Fatalf("mask 1 must enable every class, so a crash key must ask for a ticket, got %v", err)
	}
	if port.Count() != 0 {
		t.Fatalf("no trigger should have reached the port, got %v", port.Writes())
	}
}

func TestBadMaskLeavesTheInstalledMaskAlone(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if got := g.Mask(); got != "7" {
		t.Fatalf("initial mask = %q, want the fully enabled 7", got)
	}
	for _, bad := range []string{"", "abc", "16", "-1", "0x7"} {
		if err := g.SetMask(bad); !errors.Is(err, impl.ErrBadMask) {
			t.Fatalf("mask %q must be rejected with ErrBadMask, got %v", bad, err)
		}
	}
	if got := g.Mask(); got != "7" {
		t.Fatalf("rejected masks changed the installed mask to %q", got)
	}
	if got := g.MaskGeneration(); got != 1 {
		t.Fatalf("rejected masks bumped the generation to %d, want 1", got)
	}
}

func TestCallerCapabilityIsChecked(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{RequiredCap: 1 << 21})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	weak := impl.Caller{PID: 10, UID: 1000, Caps: 1 << 2}
	if _, err := g.Trigger(impl.Request{Keys: "e", Caller: weak}); !errors.Is(err, impl.ErrCallerDenied) {
		t.Fatalf("a caller without the required capability must be denied, got %v", err)
	}
	if port.Count() != 0 {
		t.Fatalf("denied callers must not reach the port, got %v", port.Writes())
	}
	res, err := g.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()})
	if err != nil {
		t.Fatalf("trigger with the capability: %v", err)
	}
	if !res.Written {
		t.Fatalf("trigger with the capability = %+v, want written", res)
	}
}

func TestLockLevelsGateTriggers(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	if err := g.SetLockLevel(1); err != nil {
		t.Fatalf("set level 1: %v", err)
	}
	if got := g.LockLevel(); got != 1 {
		t.Fatalf("lock level = %d, want 1", got)
	}
	if res, err := g.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()}); err != nil || !res.Written {
		t.Fatalf("level 1 must still allow low tier keys, got result=%+v err=%v", res, err)
	}
	if err := g.SetLockLevel(2); err != nil {
		t.Fatalf("set level 2: %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrLocked) {
		t.Fatalf("level 2 must reject every trigger with ErrLocked, got %v", err)
	}
	for _, level := range []int{-1, 3} {
		if err := g.SetLockLevel(level); !errors.Is(err, impl.ErrBadLockLevel) {
			t.Fatalf("level %d must be rejected with ErrBadLockLevel, got %v", level, err)
		}
	}
	if got := g.LockLevel(); got != 2 {
		t.Fatalf("a rejected level changed the lock level to %d, want 2", got)
	}
	if port.Count() != 1 {
		t.Fatalf("only the level 1 trigger may reach the port, got %v", port.Writes())
	}
}

func TestConfirmKeyNeedsTicket(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "s", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrTicketRequired) {
		t.Fatalf("a confirm tier key without a ticket must report ErrTicketRequired, got %v", err)
	}
	if port.Count() != 0 {
		t.Fatalf("an unconfirmed trigger must not reach the port, got %v", port.Writes())
	}
}

func TestTicketIsSingleUse(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	ticket, err := g.IssueTicket("s", sysrqCaller())
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	if ticket.Token == "" || ticket.Keys != "s" || ticket.Generation != g.MaskGeneration() {
		t.Fatalf("ticket = %+v, want a token bound to s at the current generation", ticket)
	}
	res, err := g.Trigger(impl.Request{Keys: "s", Caller: sysrqCaller(), Ticket: ticket.Token})
	if err != nil {
		t.Fatalf("confirmed trigger: %v", err)
	}
	if !res.Written {
		t.Fatalf("confirmed trigger = %+v, want written", res)
	}
	if _, err := g.Trigger(impl.Request{Keys: "s", Caller: sysrqCaller(), Ticket: ticket.Token}); !errors.Is(err, impl.ErrTicketInvalid) {
		t.Fatalf("a used ticket must report ErrTicketInvalid, got %v", err)
	}
	if port.Count() != 1 {
		t.Fatalf("port writes = %v, want exactly one", port.Writes())
	}
}

func TestTicketIsBoundToItsKey(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	if _, err := g.IssueTicket("e", sysrqCaller()); !errors.Is(err, impl.ErrTicketScope) {
		t.Fatalf("a ticket for a low tier key must be refused with ErrTicketScope, got %v", err)
	}
	ticket, err := g.IssueTicket("s", sysrqCaller())
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "u", Caller: sysrqCaller(), Ticket: ticket.Token}); !errors.Is(err, impl.ErrTicketInvalid) {
		t.Fatalf("a ticket for s must not confirm u, got %v", err)
	}
	if port.Count() != 0 {
		t.Fatalf("the mismatched ticket must not reach the port, got %v", port.Writes())
	}
}

func TestRepeatedKeyIsDeduplicatedInsideTheWindow(t *testing.T) {
	clock := &sysrqClock{now: time.Unix(1700000000, 0)}
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{Now: clock.Now, DedupWindow: 100 * time.Millisecond})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	first, err := g.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()})
	if err != nil || !first.Written {
		t.Fatalf("first trigger = %+v err=%v, want written", first, err)
	}
	second, err := g.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()})
	if err != nil {
		t.Fatalf("second trigger: %v", err)
	}
	if !second.Deduped || second.Written {
		t.Fatalf("second trigger = %+v, want a deduplicated result", second)
	}
	if second.Generation != first.Generation {
		t.Fatalf("deduplicated result generation = %d, want %d", second.Generation, first.Generation)
	}
	if port.Count() != 1 {
		t.Fatalf("deduplication wrote %d times, want 1", port.Count())
	}
	clock.Advance(200 * time.Millisecond)
	third, err := g.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()})
	if err != nil || !third.Written || third.Deduped {
		t.Fatalf("after the window the key must be written again, got result=%+v err=%v", third, err)
	}
	if port.Count() != 2 {
		t.Fatalf("port count after the window = %d, want 2", port.Count())
	}
}

func TestCrashKeyFaultsTheGuard(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	ticket, err := g.IssueTicket("b", sysrqCaller())
	if err != nil {
		t.Fatalf("issue crash ticket: %v", err)
	}
	res, err := g.Trigger(impl.Request{Keys: "b", Caller: sysrqCaller(), Ticket: ticket.Token})
	if err != nil || !res.Written {
		t.Fatalf("confirmed crash trigger = %+v err=%v, want written", res, err)
	}
	if !g.Faulted() {
		t.Fatalf("the guard must enter the faulted state after a crash key")
	}
	if _, err := g.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrGuardFaulted) {
		t.Fatalf("after a crash key every request must fail with ErrGuardFaulted, got %v", err)
	}
	if port.Count() != 1 {
		t.Fatalf("port writes = %v, want only the crash key", port.Writes())
	}
}

func TestOutstandingTicketsAreCapped(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{MaxTickets: 2})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	first, err := g.IssueTicket("s", sysrqCaller())
	if err != nil {
		t.Fatalf("first ticket: %v", err)
	}
	if _, err := g.IssueTicket("u", sysrqCaller()); err != nil {
		t.Fatalf("second ticket: %v", err)
	}
	if _, err := g.IssueTicket("s", sysrqCaller()); !errors.Is(err, impl.ErrTicketLimit) {
		t.Fatalf("third outstanding ticket must report ErrTicketLimit, got %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "s", Caller: sysrqCaller(), Ticket: first.Token}); err != nil {
		t.Fatalf("confirm the first ticket: %v", err)
	}
	if _, err := g.IssueTicket("s", sysrqCaller()); err != nil {
		t.Fatalf("a consumed ticket must free a slot, got %v", err)
	}
}

func TestAuditKeepsTheNewestEntries(t *testing.T) {
	clock := &sysrqClock{now: time.Unix(1700000000, 0)}
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{Now: clock.Now, MaxAudit: 3, DedupWindow: 100 * time.Millisecond})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	caller := impl.Caller{PID: 77, UID: 0, Caps: 1 << 21}
	for _, key := range []string{"e", "f", "m", "t", "w"} {
		if _, err := g.Trigger(impl.Request{Keys: key, Caller: caller}); err != nil {
			t.Fatalf("trigger %s: %v", key, err)
		}
		clock.Advance(200 * time.Millisecond)
	}
	entries := g.Audit()
	if len(entries) != 3 {
		t.Fatalf("audit length = %d, want the configured cap 3", len(entries))
	}
	want := []string{"m", "t", "w"}
	for i, expected := range want {
		if entries[i].Keys != expected || entries[i].Outcome != impl.OutcomeWritten {
			t.Fatalf("audit entry %d = %+v, want key %s as written", i, entries[i], expected)
		}
		if entries[i].CallerPID != 77 {
			t.Fatalf("audit entry %d caller = %d, want 77", i, entries[i].CallerPID)
		}
	}
	entries[0].Keys = "tampered"
	if got := g.Audit()[0].Keys; got != "m" {
		t.Fatalf("Audit must return a copy, the stored first key became %q", got)
	}
	if port.Count() != 5 {
		t.Fatalf("five distinct triggers wrote %d times, want 5", port.Count())
	}
}

func TestSnapshotRestoreKeepsMaskAndFaultState(t *testing.T) {
	port := &sysrqPort{}
	g := newSysrqGuard(port, impl.Config{})
	if err := g.SetMask("7"); err != nil {
		t.Fatalf("set mask: %v", err)
	}
	ticket, err := g.IssueTicket("b", sysrqCaller())
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	if _, err := g.Trigger(impl.Request{Keys: "b", Caller: sysrqCaller(), Ticket: ticket.Token}); err != nil {
		t.Fatalf("crash trigger: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restoredPort := &sysrqPort{}
	restored := newSysrqGuard(restoredPort, impl.Config{})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !restored.Faulted() {
		t.Fatalf("restored guard must stay in the faulted state")
	}
	if restored.MaskGeneration() != g.MaskGeneration() || restored.Mask() != "7" {
		t.Fatalf("restored mask generation=%d mask=%q, want generation %d and mask 7", restored.MaskGeneration(), restored.Mask(), g.MaskGeneration())
	}
	audit := restored.Audit()
	if len(audit) != 1 || audit[0].Keys != "b" || audit[0].Outcome != impl.OutcomeWritten {
		t.Fatalf("restored audit = %+v, want the crash key entry", audit)
	}
	if _, err := restored.Trigger(impl.Request{Keys: "e", Caller: sysrqCaller()}); !errors.Is(err, impl.ErrGuardFaulted) {
		t.Fatalf("restored guard must reject triggers with ErrGuardFaulted, got %v", err)
	}
	if err := restored.Restore([]byte("{broken")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if !restored.Faulted() {
		t.Fatalf("a rejected restore must leave the fault state alone")
	}
}
