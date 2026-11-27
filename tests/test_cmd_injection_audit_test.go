package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-r-03"
)

func auditConfig(maxPrograms int) impl.AuditConfig {
	return impl.AuditConfig{
		MaxPrograms:    maxPrograms,
		MaxArgs:        4,
		MaxAudit:       512,
		MaxLinkHops:    8,
		AllowedEnvKeys: []string{"PATH", "LANG"},
	}
}

func newScreener(t *testing.T) *impl.CmdInjectionAudit {
	t.Helper()
	s := impl.NewCmdInjectionAudit(auditConfig(8))
	if err := s.AllowProgram("/usr/bin/systemctl", impl.AllowOptions{}); err != nil {
		t.Fatalf("whitelist entry must be accepted, got %v", err)
	}
	return s
}

func cleanInvocation() impl.Invocation {
	return impl.Invocation{
		Program: "/usr/bin/systemctl",
		Args:    []string{"status", "nginx"},
		Env:     []string{"PATH=/usr/bin", "LANG=C"},
		Source:  "svc-deploy",
	}
}

func TestAllowProgramRejectsRelativePath(t *testing.T) {
	s := impl.NewCmdInjectionAudit(auditConfig(4))
	if err := s.AllowProgram("bin/systemctl", impl.AllowOptions{}); !errors.Is(err, impl.ErrNotAbsolute) {
		t.Fatalf("relative path must be refused with ErrNotAbsolute, got %v", err)
	}
	if got := len(s.Programs()); got != 0 {
		t.Fatalf("refused entry must not be stored, table holds %d", got)
	}
}

func TestAllowProgramEnforcesTableLimit(t *testing.T) {
	s := impl.NewCmdInjectionAudit(auditConfig(1))
	if err := s.AllowProgram("/usr/bin/systemctl", impl.AllowOptions{}); err != nil {
		t.Fatalf("first entry must be accepted, got %v", err)
	}
	if err := s.AllowProgram("/usr/bin/journalctl", impl.AllowOptions{}); !errors.Is(err, impl.ErrProgramLimit) {
		t.Fatalf("entry over the cap must be refused with ErrProgramLimit, got %v", err)
	}
	programs := s.Programs()
	if len(programs) != 1 || programs[0] != "/usr/bin/systemctl" {
		t.Fatalf("table must keep the first entry only, got %v", programs)
	}
}

func TestCheckDeniesUnlistedProgram(t *testing.T) {
	s := newScreener(t)
	inv := cleanInvocation()
	inv.Program = "/usr/bin/curl"
	v, err := s.Check(inv)
	if err != nil {
		t.Fatalf("unlisted program must produce a verdict, got %v", err)
	}
	if v.Allowed {
		t.Fatalf("unlisted program must be refused")
	}
	if v.Code != impl.CodeNotWhitelisted {
		t.Fatalf("expected %s, got %s", impl.CodeNotWhitelisted, v.Code)
	}
	audit := s.Audit()
	if len(audit) != 1 || audit[0].Allowed || audit[0].Program != "/usr/bin/curl" || audit[0].Source != "svc-deploy" {
		t.Fatalf("refusal must be audited with program and source, got %+v", audit)
	}
}

func TestCheckAllowsWhitelistedProgram(t *testing.T) {
	s := newScreener(t)
	v, err := s.Check(cleanInvocation())
	if err != nil {
		t.Fatalf("clean invocation must produce a verdict, got %v", err)
	}
	if !v.Allowed || v.Code != impl.CodeAllowed {
		t.Fatalf("clean invocation must be allowed, got %s/%s", v.Code, v.Reason)
	}
	if v.Program != "/usr/bin/systemctl" {
		t.Fatalf("verdict must carry the resolved program, got %s", v.Program)
	}
	if v.Seq != 1 {
		t.Fatalf("first verdict must take sequence 1, got %d", v.Seq)
	}
	audit := s.Audit()
	if len(audit) != 1 || audit[0].Seq != 1 || audit[0].Code != impl.CodeAllowed || audit[0].Argc != 2 {
		t.Fatalf("allow must be audited with sequence and argument count, got %+v", audit)
	}
}

func TestCheckEnforcesArgLimit(t *testing.T) {
	s := newScreener(t)
	inv := cleanInvocation()
	inv.Args = []string{"restart", "nginx", "--now", "--force", "--no-block"}
	v, err := s.Check(inv)
	if err != nil {
		t.Fatalf("argument overflow must produce a verdict, got %v", err)
	}
	if v.Allowed {
		t.Fatalf("five arguments over a cap of four must be refused")
	}
	if v.Code != impl.CodeArgLimit {
		t.Fatalf("expected %s, got %s", impl.CodeArgLimit, v.Code)
	}
}

func TestCheckRejectsShellSeparators(t *testing.T) {
	s := newScreener(t)
	for _, arg := range []string{"status;reboot", "status|tee", "status&&reboot", "status>out"} {
		inv := cleanInvocation()
		inv.Args = []string{arg}
		v, err := s.Check(inv)
		if err != nil {
			t.Fatalf("argument %q must produce a verdict, got %v", arg, err)
		}
		if v.Allowed || v.Code != impl.CodeMetachar {
			t.Fatalf("argument %q must be refused as metachar, got allowed=%v code=%s", arg, v.Allowed, v.Code)
		}
	}
}

func TestCheckRejectsCommandSubstitution(t *testing.T) {
	s := newScreener(t)
	for _, arg := range []string{"$(id)", "log`whoami`", "x$(cat /etc/passwd)"} {
		inv := cleanInvocation()
		inv.Args = []string{arg}
		v, err := s.Check(inv)
		if err != nil {
			t.Fatalf("argument %q must produce a verdict, got %v", arg, err)
		}
		if v.Allowed || v.Code != impl.CodeMetachar {
			t.Fatalf("argument %q must be refused as metachar, got allowed=%v code=%s", arg, v.Allowed, v.Code)
		}
	}
}

func TestEscapeQuotesSingleQuotes(t *testing.T) {
	s := impl.NewCmdInjectionAudit(auditConfig(4))
	got, err := s.Escape([]string{"printf", "it's fine"})
	if err != nil {
		t.Fatalf("escape must succeed, got %v", err)
	}
	want := `'printf' 'it'\''s fine'`
	if got != want {
		t.Fatalf("escaped words mismatch, want %s got %s", want, got)
	}
}

func TestEscapeEmptyArgument(t *testing.T) {
	s := impl.NewCmdInjectionAudit(auditConfig(4))
	got, err := s.Escape([]string{"echo", ""})
	if err != nil {
		t.Fatalf("escape must succeed, got %v", err)
	}
	want := `'echo' ''`
	if got != want {
		t.Fatalf("empty argument must become two quotes, want %s got %s", want, got)
	}
	if _, err := s.Escape(nil); !errors.Is(err, impl.ErrBadInvocation) {
		t.Fatalf("escape of no arguments must be refused, got %v", err)
	}
}

func TestEnvAcceptsAllowlistedAssignment(t *testing.T) {
	s := impl.NewCmdInjectionAudit(auditConfig(4))
	if err := s.CheckEnv([]string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}); err != nil {
		t.Fatalf("allowlisted assignments must pass, got %v", err)
	}
	if err := s.CheckEnv(nil); err != nil {
		t.Fatalf("empty environment must pass, got %v", err)
	}
}

func TestEnvRejectsMalformedEntry(t *testing.T) {
	s := impl.NewCmdInjectionAudit(auditConfig(4))
	if err := s.CheckEnv([]string{"PATH=/usr/bin", "broken"}); !errors.Is(err, impl.ErrBadInvocation) {
		t.Fatalf("entry without an equals sign must be refused, got %v", err)
	}
}

func TestResolveFollowsRegisteredLink(t *testing.T) {
	s := impl.NewCmdInjectionAudit(auditConfig(4))
	if err := s.RegisterLink("/opt/bin/ctl", "/usr/bin/systemctl"); err != nil {
		t.Fatalf("link must be registered, got %v", err)
	}
	if err := s.RegisterLink("/opt/bin/ctl-next", "/opt/bin/ctl"); err != nil {
		t.Fatalf("second link must be registered, got %v", err)
	}
	got, err := s.Resolve("/opt/bin/ctl-next")
	if err != nil {
		t.Fatalf("resolution must succeed, got %v", err)
	}
	if got != "/usr/bin/systemctl" {
		t.Fatalf("chained links must resolve to the real path, got %s", got)
	}
	direct, err := s.Resolve("/usr/bin/journalctl")
	if err != nil {
		t.Fatalf("plain path must resolve to itself, got %v", err)
	}
	if direct != "/usr/bin/journalctl" {
		t.Fatalf("plain path must resolve to itself, got %s", direct)
	}
}

func TestResolveDetectsLinkCycle(t *testing.T) {
	s := impl.NewCmdInjectionAudit(auditConfig(4))
	if err := s.RegisterLink("/opt/a", "/opt/b"); err != nil {
		t.Fatalf("first link must be registered, got %v", err)
	}
	if err := s.RegisterLink("/opt/b", "/opt/a"); err != nil {
		t.Fatalf("second link must be registered, got %v", err)
	}
	if _, err := s.Resolve("/opt/a"); !errors.Is(err, impl.ErrLinkCycle) {
		t.Fatalf("cyclic links must be refused with ErrLinkCycle, got %v", err)
	}
}

func TestSnapshotRestoreKeepsWhitelistAndCounters(t *testing.T) {
	s := newScreener(t)
	if err := s.RegisterLink("/opt/bin/ctl", "/usr/bin/systemctl"); err != nil {
		t.Fatalf("link must be registered, got %v", err)
	}
	denied := cleanInvocation()
	denied.Program = "/usr/bin/curl"
	if _, err := s.Check(denied); err != nil {
		t.Fatalf("refusal must produce a verdict, got %v", err)
	}
	if _, err := s.Check(cleanInvocation()); err != nil {
		t.Fatalf("allow must produce a verdict, got %v", err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must be produced, got %v", err)
	}
	other := impl.NewCmdInjectionAudit(auditConfig(8))
	if err := other.Restore(data); err != nil {
		t.Fatalf("snapshot must restore, got %v", err)
	}
	if got := other.Programs(); len(got) != 1 || got[0] != "/usr/bin/systemctl" {
		t.Fatalf("whitelist must survive restore, got %v", got)
	}
	if got := other.Denied("svc-deploy"); got != 1 {
		t.Fatalf("refusal counter must survive restore, got %d", got)
	}
	v, err := other.Check(cleanInvocation())
	if err != nil {
		t.Fatalf("verdict after restore must be produced, got %v", err)
	}
	if !v.Allowed || v.Seq != 3 {
		t.Fatalf("sequence must continue at 3 after restore, got allowed=%v seq=%d", v.Allowed, v.Seq)
	}
	if err := other.Restore([]byte("{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must be refused with ErrBadSnapshot, got %v", err)
	}
}

func TestConcurrentCheckWhileRegistering(t *testing.T) {
	s := newScreener(t)
	const workers = 24
	const rounds = 4
	var wg sync.WaitGroup
	failures := make(chan string, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				v, err := s.Check(cleanInvocation())
				if err != nil {
					failures <- "verdict returned error: " + err.Error()
					return
				}
				if !v.Allowed {
					failures <- "whitelisted program was refused during a concurrent registration"
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			path := "/usr/local/bin/tool-" + string(rune('a'+i))
			if err := s.AllowProgram(path, impl.AllowOptions{}); err != nil {
				failures <- "registration returned error: " + err.Error()
				return
			}
		}
	}()
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Fatalf("%s", msg)
	}
	audit := s.Audit()
	if len(audit) != workers*rounds {
		t.Fatalf("audit trail must hold %d records, got %d", workers*rounds, len(audit))
	}
	seen := make(map[int64]bool, len(audit))
	for _, ev := range audit {
		if seen[ev.Seq] {
			t.Fatalf("audit sequence %d was handed out twice", ev.Seq)
		}
		seen[ev.Seq] = true
	}
}
