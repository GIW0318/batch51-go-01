package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-d-10"
)

func iocCode(dir, size uint32, typ, nr uint8) uint32 {
	return (dir&impl.IocDirMask)<<impl.IocDirShift |
		(size&impl.IocSizeMask)<<impl.IocSizeShift |
		uint32(typ)<<impl.IocTypeShift |
		uint32(nr)<<impl.IocNrShift
}

func ioctlGuard() *impl.IoctlPolicyGuard {
	return impl.NewIoctlPolicyGuard(impl.GuardConfig{MaxAuditPerDevice: 16})
}

func registerDevice(t *testing.T, g *impl.IoctlPolicyGuard, device string, rules []impl.Rule) {
	t.Helper()
	if err := g.Register(device, impl.RuleSet{Device: device, Rules: rules}); err != nil {
		t.Fatalf("register %s: %v", device, err)
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	g := ioctlGuard()
	good := impl.Rule{ID: "r1", Code: iocCode(impl.DirRead, 8, 'A', 1), Name: "get-status", Allow: true}
	if err := g.Register("  ", impl.RuleSet{Rules: []impl.Rule{good}}); !errors.Is(err, impl.ErrBlankDevice) {
		t.Fatalf("blank device must fail with ErrBlankDevice, got %v", err)
	}
	if err := g.Register("/dev/null0", impl.RuleSet{Device: "/dev/null0"}); !errors.Is(err, impl.ErrEmptyRuleSet) {
		t.Fatalf("empty rule set must fail with ErrEmptyRuleSet, got %v", err)
	}
	if err := g.Register("/dev/null0", impl.RuleSet{Rules: []impl.Rule{{ID: "  ", Code: good.Code}}}); !errors.Is(err, impl.ErrBlankRule) {
		t.Fatalf("blank rule id must fail with ErrBlankRule, got %v", err)
	}
	dup := []impl.Rule{good, {ID: "r1", Code: iocCode(impl.DirRead, 4, 'B', 2), Allow: true}}
	if err := g.Register("/dev/null0", impl.RuleSet{Rules: dup}); !errors.Is(err, impl.ErrDuplicateRule) {
		t.Fatalf("duplicate rule id must fail with ErrDuplicateRule, got %v", err)
	}
	if _, err := g.Generation("/dev/null0"); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("a rejected registration must not create the device, got %v", err)
	}
}

func TestRegisterRejectsCodeMismatch(t *testing.T) {
	g := ioctlGuard()
	// A request number with no direction but a non zero size can never match a
	// well formed ioctl and must not enter a rule set.
	bad := impl.Rule{ID: "r-bad", Code: iocCode(impl.DirNone, 12, 'C', 3), Allow: true}
	if err := g.Register("/dev/badctl", impl.RuleSet{Rules: []impl.Rule{bad}}); !errors.Is(err, impl.ErrRuleMismatch) {
		t.Fatalf("inconsistent request code must fail with ErrRuleMismatch, got %v", err)
	}
	if _, err := g.Generation("/dev/badctl"); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("a rejected rule set must not create the device, got %v", err)
	}
}

func TestCheckDecodesDirSizeTypeNr(t *testing.T) {
	g := ioctlGuard()
	code := iocCode(impl.DirRead, 8, 'X', 5)
	registerDevice(t, g, "/dev/ctl0", []impl.Rule{{ID: "r-x", Code: code, Name: "read-x", Allow: true}})
	info := g.Decode(code)
	if info.Dir != impl.DirRead {
		t.Fatalf("decoded direction = %d, want %d", info.Dir, impl.DirRead)
	}
	if info.Size != 8 {
		t.Fatalf("decoded size = %d, want 8", info.Size)
	}
	if info.Type != 'X' {
		t.Fatalf("decoded type = %#x, want 'X'", info.Type)
	}
	if info.Nr != 5 {
		t.Fatalf("decoded number = %d, want 5", info.Nr)
	}
	decision, err := g.Check("/dev/ctl0", impl.IoctlRequest{Code: code, BufLen: 8}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if decision.Dir != impl.DirRead || decision.Size != 8 || decision.Type != 'X' || decision.Nr != 5 {
		t.Fatalf("decision did not carry the decoded bits: %+v", decision)
	}
	if decision.Code != code || decision.Device != "/dev/ctl0" {
		t.Fatalf("decision identity mismatch: %+v", decision)
	}
}

func TestCheckAllowsMatchingRule(t *testing.T) {
	g := ioctlGuard()
	code := iocCode(impl.DirRead|impl.DirWrite, 16, 'Q', 9)
	registerDevice(t, g, "/dev/ctl1", []impl.Rule{{ID: "r-q", Code: code, Name: "round-trip", Allow: true}})
	decision, err := g.Check("/dev/ctl1", impl.IoctlRequest{Code: code, BufLen: 16}, impl.Cred{UID: 0})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if !decision.Allow {
		t.Fatalf("matching rule must allow, got %+v", decision)
	}
	if decision.RuleID != "r-q" {
		t.Fatalf("decision rule id = %q, want r-q", decision.RuleID)
	}
	if decision.Reason != impl.ReasonAllowed {
		t.Fatalf("decision reason = %q, want %q", decision.Reason, impl.ReasonAllowed)
	}
	if decision.Gen != 1 {
		t.Fatalf("decision generation = %d, want the first generation 1", decision.Gen)
	}
}

func TestCheckDeniesWithoutRule(t *testing.T) {
	g := ioctlGuard()
	known := iocCode(impl.DirRead, 4, 'A', 1)
	unknown := iocCode(impl.DirRead, 4, 'Z', 7)
	registerDevice(t, g, "/dev/ctl2", []impl.Rule{{ID: "r-a", Code: known, Allow: true}})
	decision, err := g.Check("/dev/ctl2", impl.IoctlRequest{Code: unknown, BufLen: 4}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check must succeed for an unknown request, got %v", err)
	}
	if decision.Allow {
		t.Fatalf("a request without a rule must be denied, got %+v", decision)
	}
	if decision.Reason != impl.ReasonNoRule {
		t.Fatalf("decision reason = %q, want %q", decision.Reason, impl.ReasonNoRule)
	}
	if decision.RuleID != "" {
		t.Fatalf("denied decision must not name a rule, got %q", decision.RuleID)
	}
}

func TestCheckDeniesRevokedRule(t *testing.T) {
	g := ioctlGuard()
	code := iocCode(impl.DirRead, 4, 'R', 2)
	registerDevice(t, g, "/dev/ctl3", []impl.Rule{{ID: "r-rev", Code: code, Allow: true}})
	if err := g.Revoke("/dev/ctl3", "r-rev"); err != nil {
		t.Fatalf("revoke must succeed, got %v", err)
	}
	if err := g.Revoke("/dev/ctl3", "r-rev"); err != nil {
		t.Fatalf("revoking twice must be idempotent, got %v", err)
	}
	if err := g.Revoke("/dev/ctl3", "r-404"); !errors.Is(err, impl.ErrUnknownRule) {
		t.Fatalf("revoking an unknown rule must fail with ErrUnknownRule, got %v", err)
	}
	if err := g.Revoke("/dev/none", "r-rev"); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("revoking on an unknown device must fail with ErrUnknownDevice, got %v", err)
	}
	decision, err := g.Check("/dev/ctl3", impl.IoctlRequest{Code: code, BufLen: 4}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if decision.Allow {
		t.Fatalf("a revoked rule must be denied, got %+v", decision)
	}
	if decision.Reason != impl.ReasonRevoked {
		t.Fatalf("decision reason = %q, want %q", decision.Reason, impl.ReasonRevoked)
	}
	rules := g.Rules("/dev/ctl3")
	if len(rules) != 1 || !rules[0].Revoked {
		t.Fatalf("rule set after revoke = %+v, want the rule marked revoked", rules)
	}
}

func TestCheckDeniesPrivilegeRule(t *testing.T) {
	g := ioctlGuard()
	code := iocCode(impl.DirReadWrite, 32, 'P', 4)
	registerDevice(t, g, "/dev/ctl4", []impl.Rule{{ID: "r-priv", Code: code, Allow: true, RequireAdmin: true}})
	plain, err := g.Check("/dev/ctl4", impl.IoctlRequest{Code: code, BufLen: 32}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if plain.Allow || plain.Reason != impl.ReasonPrivilege {
		t.Fatalf("unprivileged caller got %+v, want a privilege denial", plain)
	}
	admin, err := g.Check("/dev/ctl4", impl.IoctlRequest{Code: code, BufLen: 32}, impl.Cred{UID: 0, CapAdmin: true})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if !admin.Allow {
		t.Fatalf("administrator must be allowed, got %+v", admin)
	}
}

func TestCheckDeniesOnLengthMismatch(t *testing.T) {
	g := ioctlGuard()
	code := iocCode(impl.DirRead, 8, 'L', 6)
	registerDevice(t, g, "/dev/ctl5", []impl.Rule{{ID: "r-len", Code: code, Allow: true}})
	short, err := g.Check("/dev/ctl5", impl.IoctlRequest{Code: code, BufLen: 4}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if short.Allow {
		t.Fatalf("a short buffer must be denied, got %+v", short)
	}
	if short.Reason != impl.ReasonBadLength {
		t.Fatalf("decision reason = %q, want %q", short.Reason, impl.ReasonBadLength)
	}
	long, err := g.Check("/dev/ctl5", impl.IoctlRequest{Code: code, BufLen: 16}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if long.Allow {
		t.Fatalf("an oversized buffer must be denied, got %+v", long)
	}
	exact, err := g.Check("/dev/ctl5", impl.IoctlRequest{Code: code, BufLen: 8}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if !exact.Allow {
		t.Fatalf("an exact buffer must be allowed, got %+v", exact)
	}
	malformed := iocCode(impl.DirNone, 8, 'L', 7)
	bad, err := g.Check("/dev/ctl5", impl.IoctlRequest{Code: malformed, BufLen: 8}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check must succeed, got %v", err)
	}
	if bad.Allow || bad.Reason != impl.ReasonBadCode {
		t.Fatalf("a malformed request code got %+v, want a bad code denial", bad)
	}
}

func TestCheckUnknownDevice(t *testing.T) {
	g := ioctlGuard()
	if _, err := g.Check("", impl.IoctlRequest{Code: 1}, impl.Cred{}); !errors.Is(err, impl.ErrBlankDevice) {
		t.Fatalf("blank device must fail with ErrBlankDevice, got %v", err)
	}
	if _, err := g.Check("/dev/ghost", impl.IoctlRequest{Code: 1, BufLen: 0}, impl.Cred{}); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("unknown device must fail with ErrUnknownDevice, got %v", err)
	}
	if got := g.Audit("/dev/ghost"); len(got) != 0 {
		t.Fatalf("audit of an unknown device returned %d entries, want 0", len(got))
	}
}

func TestForwardCopiesBufferAndReturnsHandlerOutput(t *testing.T) {
	g := ioctlGuard()
	code := iocCode(impl.DirReadWrite, 4, 'F', 1)
	registerDevice(t, g, "/dev/cfg0", []impl.Rule{{ID: "r-fwd", Code: code, Allow: true}})
	handler := func(req impl.IoctlRequest, in []byte) ([]byte, error) {
		out := append([]byte("r:"), in...)
		for i := range in {
			in[i] = 0
		}
		return out, nil
	}
	if err := g.SetHandler("/dev/cfg0", impl.Handler(handler)); err != nil {
		t.Fatalf("set handler must succeed, got %v", err)
	}
	buf := []byte("abcd")
	out, err := g.Forward("/dev/cfg0", impl.IoctlRequest{Code: code, BufLen: 4}, buf)
	if err != nil {
		t.Fatalf("forward must succeed, got %v", err)
	}
	if string(out) != "r:abcd" {
		t.Fatalf("forward result = %q, want r:abcd", string(out))
	}
	if string(buf) != "abcd" {
		t.Fatalf("forward mutated the caller buffer: %q", string(buf))
	}
}

func TestConcurrentForwardNoSharedBuffer(t *testing.T) {
	g := ioctlGuard()
	code := iocCode(impl.DirReadWrite, 4, 'G', 2)
	registerDevice(t, g, "/dev/cfg1", []impl.Rule{{ID: "r-fwd", Code: code, Allow: true}})
	handler := func(req impl.IoctlRequest, in []byte) ([]byte, error) {
		out := make([]byte, 0, len(in)+2)
		out = append(out, 'r', ':')
		out = append(out, in...)
		return out, nil
	}
	if err := g.SetHandler("/dev/cfg1", impl.Handler(handler)); err != nil {
		t.Fatalf("set handler must succeed, got %v", err)
	}
	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := make(chan struct{})
	mismatch := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := fmt.Sprintf("p%02d!", i)
			<-started
			out, err := g.Forward("/dev/cfg1", impl.IoctlRequest{Code: code, BufLen: 4}, []byte(payload))
			if err != nil || string(out) != "r:"+payload {
				mu.Lock()
				mismatch++
				mu.Unlock()
			}
		}(i)
	}
	close(started)
	wg.Wait()
	if mismatch != 0 {
		t.Fatalf("%d concurrent forwards returned another caller's buffer", mismatch)
	}
}

func TestForwardRejectsBadRequests(t *testing.T) {
	g := ioctlGuard()
	allow := iocCode(impl.DirReadWrite, 4, 'H', 3)
	deny := iocCode(impl.DirReadWrite, 4, 'H', 4)
	registerDevice(t, g, "/dev/cfg2", []impl.Rule{
		{ID: "r-allow", Code: allow, Allow: true},
		{ID: "r-deny", Code: deny, Allow: false},
	})
	if _, err := g.Forward("/dev/cfg2", impl.IoctlRequest{Code: allow, BufLen: 4}, []byte("abcd")); !errors.Is(err, impl.ErrNoHandler) {
		t.Fatalf("forward without a handler must fail with ErrNoHandler, got %v", err)
	}
	if err := g.SetHandler("/dev/cfg2", impl.Handler(func(req impl.IoctlRequest, in []byte) ([]byte, error) {
		return in, nil
	})); err != nil {
		t.Fatalf("set handler must succeed, got %v", err)
	}
	if _, err := g.Forward("/dev/cfg2", impl.IoctlRequest{Code: allow, BufLen: 3}, []byte("abc")); !errors.Is(err, impl.ErrBadBuffer) {
		t.Fatalf("short buffer must fail with ErrBadBuffer, got %v", err)
	}
	if _, err := g.Forward("/dev/cfg2", impl.IoctlRequest{Code: allow, BufLen: 4}, []byte("abcde")); !errors.Is(err, impl.ErrBadBuffer) {
		t.Fatalf("oversized buffer must fail with ErrBadBuffer, got %v", err)
	}
	if _, err := g.Forward("/dev/cfg2", impl.IoctlRequest{Code: deny, BufLen: 4}, []byte("abcd")); !errors.Is(err, impl.ErrNotAllowed) {
		t.Fatalf("denied rule must fail with ErrNotAllowed, got %v", err)
	}
	if _, err := g.Forward("/dev/ghost", impl.IoctlRequest{Code: allow, BufLen: 4}, []byte("abcd")); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("forward on an unknown device must fail with ErrUnknownDevice, got %v", err)
	}
	if err := g.SetHandler("/dev/ghost", impl.Handler(func(req impl.IoctlRequest, in []byte) ([]byte, error) {
		return nil, nil
	})); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("handler on an unknown device must fail with ErrUnknownDevice, got %v", err)
	}
}

func TestAuditRecordsCheckAndForwardInOrder(t *testing.T) {
	g := ioctlGuard()
	code := iocCode(impl.DirReadWrite, 4, 'J', 5)
	other := iocCode(impl.DirRead, 4, 'J', 6)
	registerDevice(t, g, "/dev/cfg3", []impl.Rule{{ID: "r-audit", Code: code, Allow: true}})
	if err := g.SetHandler("/dev/cfg3", impl.Handler(func(req impl.IoctlRequest, in []byte) ([]byte, error) {
		return append([]byte(nil), in...), nil
	})); err != nil {
		t.Fatalf("set handler must succeed, got %v", err)
	}
	if _, err := g.Check("/dev/cfg3", impl.IoctlRequest{Code: other, BufLen: 4}, impl.Cred{UID: 1000}); err != nil {
		t.Fatalf("first check must succeed, got %v", err)
	}
	if _, err := g.Check("/dev/cfg3", impl.IoctlRequest{Code: code, BufLen: 4}, impl.Cred{UID: 1000}); err != nil {
		t.Fatalf("second check must succeed, got %v", err)
	}
	if _, err := g.Forward("/dev/cfg3", impl.IoctlRequest{Code: code, BufLen: 4}, []byte("wxyz")); err != nil {
		t.Fatalf("forward must succeed, got %v", err)
	}
	entries := g.Audit("/dev/cfg3")
	if len(entries) != 3 {
		t.Fatalf("audit holds %d entries, want 3", len(entries))
	}
	wantAllow := []bool{false, true, true}
	wantReason := []string{impl.ReasonNoRule, impl.ReasonAllowed, impl.ReasonForwarded}
	for i, entry := range entries {
		if entry.Device != "/dev/cfg3" {
			t.Fatalf("entry %d device = %q", i, entry.Device)
		}
		if entry.Allow != wantAllow[i] {
			t.Fatalf("entry %d allow = %v, want %v", i, entry.Allow, wantAllow[i])
		}
		if entry.Reason != wantReason[i] {
			t.Fatalf("entry %d reason = %q, want %q", i, entry.Reason, wantReason[i])
		}
		if i > 0 && entry.Seq <= entries[i-1].Seq {
			t.Fatalf("audit sequence must increase: %d then %d", entries[i-1].Seq, entry.Seq)
		}
	}
	if entries[0].RuleID != "" || entries[1].RuleID != "r-audit" || entries[2].RuleID != "r-audit" {
		t.Fatalf("audit rule ids = %q, %q, %q", entries[0].RuleID, entries[1].RuleID, entries[2].RuleID)
	}
	if entries[2].Code != code {
		t.Fatalf("forward entry code = %#x, want %#x", entries[2].Code, code)
	}
}

func TestSnapshotRestoreKeepsRulesAndGeneration(t *testing.T) {
	g := ioctlGuard()
	first := iocCode(impl.DirRead, 8, 'K', 1)
	second := iocCode(impl.DirWrite, 8, 'K', 2)
	registerDevice(t, g, "/dev/cfg4", []impl.Rule{
		{ID: "r-k1", Code: first, Name: "read-k", Allow: true},
		{ID: "r-k2", Code: second, Name: "write-k", Allow: true, RequireAdmin: true},
	})
	gen, err := g.Generation("/dev/cfg4")
	if err != nil {
		t.Fatalf("generation must be readable, got %v", err)
	}
	if gen != 1 {
		t.Fatalf("first generation = %d, want 1", gen)
	}
	if _, err := g.Check("/dev/cfg4", impl.IoctlRequest{Code: first, BufLen: 8}, impl.Cred{UID: 1000}); err != nil {
		t.Fatalf("check before snapshot: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must succeed, got %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := ioctlGuard()
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore must succeed, got %v", err)
	}
	restoredGen, err := restored.Generation("/dev/cfg4")
	if err != nil {
		t.Fatalf("generation after restore: %v", err)
	}
	if restoredGen != gen {
		t.Fatalf("restored generation = %d, want %d", restoredGen, gen)
	}
	if rules := restored.Rules("/dev/cfg4"); len(rules) != 2 {
		t.Fatalf("restored rule count = %d, want 2", len(rules))
	}
	decision, err := restored.Check("/dev/cfg4", impl.IoctlRequest{Code: first, BufLen: 8}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check after restore: %v", err)
	}
	if !decision.Allow || decision.RuleID != "r-k1" {
		t.Fatalf("restored policy no longer allows the rule: %+v", decision)
	}
	if got := len(restored.Audit("/dev/cfg4")); got != 2 {
		t.Fatalf("restored audit holds %d entries, want the snapshot plus the new check", got)
	}
	if err := restored.Register("/dev/cfg4", impl.RuleSet{Rules: []impl.Rule{{ID: "r-next", Code: first, Allow: true}}}); err != nil {
		t.Fatalf("re-register after restore: %v", err)
	}
	bumped, err := restored.Generation("/dev/cfg4")
	if err != nil {
		t.Fatalf("generation after re-register: %v", err)
	}
	if bumped != gen+1 {
		t.Fatalf("generation after re-register = %d, want %d", bumped, gen+1)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := ioctlGuard()
	code := iocCode(impl.DirRead, 4, 'M', 1)
	registerDevice(t, g, "/dev/cfg5", []impl.Rule{{ID: "r-m", Code: code, Allow: true}})
	if _, err := g.Check("/dev/cfg5", impl.IoctlRequest{Code: code, BufLen: 4}, impl.Cred{UID: 1000}); err != nil {
		t.Fatalf("check before the rejected restore: %v", err)
	}
	if err := g.Restore([]byte("null-not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if rules := g.Rules("/dev/cfg5"); len(rules) != 1 || rules[0].ID != "r-m" {
		t.Fatalf("failed restore touched the rule set: %+v", rules)
	}
	gen, err := g.Generation("/dev/cfg5")
	if err != nil || gen != 1 {
		t.Fatalf("live generation = %d, %v; want 1, nil", gen, err)
	}
	if got := len(g.Audit("/dev/cfg5")); got != 1 {
		t.Fatalf("failed restore touched the audit trail: %d entries", got)
	}
	decision, err := g.Check("/dev/cfg5", impl.IoctlRequest{Code: code, BufLen: 4}, impl.Cred{UID: 1000})
	if err != nil {
		t.Fatalf("check after the rejected restore: %v", err)
	}
	if !decision.Allow {
		t.Fatalf("live rules must keep working after a rejected restore: %+v", decision)
	}
}
