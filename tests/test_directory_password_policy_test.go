package task

import (
	"errors"
	"fmt"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-a-04"
)

type clock struct {
	t time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 4, 1, 8, 30, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func corpPolicy() impl.Policy {
	return impl.Policy{
		Tenant:         "corp",
		MinLength:      10,
		MaxLength:      64,
		MinClasses:     3,
		HistoryDepth:   3,
		MaxAgeDays:     30,
		GraceLogins:    2,
		MaxFailures:    3,
		LockoutMinutes: 15,
		Version:        1,
	}
}

func labPolicy() impl.Policy {
	return impl.Policy{
		Tenant:         "lab",
		MinLength:      6,
		MaxLength:      32,
		MinClasses:     2,
		HistoryDepth:   2,
		MaxAgeDays:     0,
		GraceLogins:    0,
		MaxFailures:    5,
		LockoutMinutes: 5,
		Version:        1,
	}
}

func policyConfig(c *clock) impl.PolicyConfig {
	return impl.PolicyConfig{
		DefaultTenant: "corp",
		Policies: map[string]impl.Policy{
			"corp": corpPolicy(),
			"lab":  labPolicy(),
		},
		Now: c.Now,
	}
}

const basePassword = "Str0ng-Pass!"

func mustProvision(t *testing.T, d *impl.DirectoryPasswordPolicy, account, tenant, pw string) {
	t.Helper()
	if err := d.Provision(account, tenant, pw); err != nil {
		t.Fatalf("provision %s: %v", account, err)
	}
}

func mustBind(t *testing.T, d *impl.DirectoryPasswordPolicy, c *clock, account, pw string) impl.BindResult {
	t.Helper()
	r, err := d.Bind(account, pw, c.Now())
	if err != nil {
		t.Fatalf("bind %s: %v", account, err)
	}
	return r
}

func TestProvisionValidatesInput(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	if err := d.Provision("", "corp", basePassword); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("blank account returned %v, want ErrMissingField", err)
	}
	if err := d.Provision("bob", "", basePassword); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("blank tenant returned %v, want ErrMissingField", err)
	}
	if err := d.Provision("bob", "corp", ""); !errors.Is(err, impl.ErrMissingField) {
		t.Fatalf("blank password returned %v, want ErrMissingField", err)
	}
	if err := d.Provision("bob", "mystery", basePassword); !errors.Is(err, impl.ErrUnknownTenant) {
		t.Fatalf("unknown tenant returned %v, want ErrUnknownTenant", err)
	}
	if err := d.Provision("bob", "corp", "short"); !errors.Is(err, impl.ErrPasswordPolicy) {
		t.Fatalf("short password returned %v, want ErrPasswordPolicy", err)
	}
	if err := d.Provision("bob", "corp", "alllowercaseonly"); !errors.Is(err, impl.ErrPasswordPolicy) {
		t.Fatalf("single character class password returned %v, want ErrPasswordPolicy", err)
	}
	mustProvision(t, d, "bob", "corp", basePassword)
	if err := d.Provision("bob", "corp", "An0ther-Pass?"); !errors.Is(err, impl.ErrAccountExists) {
		t.Fatalf("second provision returned %v, want ErrAccountExists", err)
	}
	st, err := d.Status("bob")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Account != "bob" || st.Tenant != "corp" || st.State != impl.StateActive {
		t.Fatalf("status = %+v, want bob/corp/active", st)
	}
	if st.PolicyVersion != 1 {
		t.Fatalf("policy version = %d, want 1", st.PolicyVersion)
	}
	if st.Sessions != 0 || st.PasswordAgeDays != 0 {
		t.Fatalf("fresh account sessions=%d age=%d, want 0 and 0", st.Sessions, st.PasswordAgeDays)
	}
}

func TestBindAcceptsCorrectPassword(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	mustProvision(t, d, "alice", "corp", basePassword)
	r := mustBind(t, d, c, "alice", basePassword)
	if !r.Allowed || r.MustChange {
		t.Fatalf("clean bind allowed=%v mustChange=%v, want true and false", r.Allowed, r.MustChange)
	}
	if r.State != impl.StateActive {
		t.Fatalf("state = %q, want %q", r.State, impl.StateActive)
	}
	if r.SessionID == "" {
		t.Fatalf("a successful bind must mint a session")
	}
	if r.Tenant != "corp" {
		t.Fatalf("result tenant = %q, want corp", r.Tenant)
	}
	if r.AttemptsLeft != 3 {
		t.Fatalf("attempts left = %d, want the policy allowance 3", r.AttemptsLeft)
	}
	if got := len(d.Sessions("alice")); got != 1 {
		t.Fatalf("live sessions = %d, want 1", got)
	}
	if err := d.Authorize("alice", r.SessionID); err != nil {
		t.Fatalf("a session minted by a clean bind must authorise, got %v", err)
	}
}

func TestBindRejectsWrongPasswordAndCountsAttempts(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	mustProvision(t, d, "alice", "corp", basePassword)
	r, err := d.Bind("alice", "Wr0ng-Pass!", c.Now())
	if !errors.Is(err, impl.ErrBadPassword) {
		t.Fatalf("wrong password returned %v, want ErrBadPassword", err)
	}
	if r.AttemptsLeft != 2 {
		t.Fatalf("attempts left = %d, want 2 after one failure", r.AttemptsLeft)
	}
	if r.SessionID != "" {
		t.Fatalf("a failed bind must not mint a session")
	}
	st, err := d.Status("alice")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Failures != 1 || st.Sessions != 0 {
		t.Fatalf("status = %+v, want one failure and no session", st)
	}
	if _, err := d.Bind("nobody", basePassword, c.Now()); !errors.Is(err, impl.ErrAccountNotFound) {
		t.Fatalf("bind of an unknown account returned %v, want ErrAccountNotFound", err)
	}
}

func TestBindLocksAfterRepeatedFailures(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	mustProvision(t, d, "alice", "corp", basePassword)
	for i := 0; i < 2; i++ {
		if _, err := d.Bind("alice", "Wr0ng-Pass!", c.Now()); !errors.Is(err, impl.ErrBadPassword) {
			t.Fatalf("failure %d returned %v, want ErrBadPassword", i, err)
		}
	}
	if _, err := d.Bind("alice", "Wr0ng-Pass!", c.Now()); !errors.Is(err, impl.ErrAccountLocked) {
		t.Fatalf("third failure returned %v, want ErrAccountLocked", err)
	}
	st, err := d.Status("alice")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.State != impl.StateLocked || st.Failures != 3 {
		t.Fatalf("status = %+v, want locked with three failures", st)
	}
	if _, err := d.Bind("alice", basePassword, c.Now()); !errors.Is(err, impl.ErrAccountLocked) {
		t.Fatalf("correct password while locked returned %v, want ErrAccountLocked", err)
	}
	c.Advance(16 * time.Minute)
	r := mustBind(t, d, c, "alice", basePassword)
	if r.State != impl.StateActive {
		t.Fatalf("state after the lockout window = %q, want %q", r.State, impl.StateActive)
	}
	st, err = d.Status("alice")
	if err != nil {
		t.Fatalf("status after unlock: %v", err)
	}
	if st.Failures != 0 {
		t.Fatalf("failure counter after a successful bind = %d, want 0", st.Failures)
	}
}

func TestBindExpiresPasswordWithoutGrace(t *testing.T) {
	c := newClock()
	cfg := policyConfig(c)
	p := corpPolicy()
	p.GraceLogins = 0
	cfg.Policies["corp"] = p
	d := impl.NewDirectoryPasswordPolicy(cfg)
	mustProvision(t, d, "alice", "corp", basePassword)
	c.Advance(31 * 24 * time.Hour)
	if _, err := d.Bind("alice", basePassword, c.Now()); !errors.Is(err, impl.ErrPasswordExpired) {
		t.Fatalf("expired password without grace returned %v, want ErrPasswordExpired", err)
	}
	st, err := d.Status("alice")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.State != impl.StateExpired {
		t.Fatalf("state = %q, want %q", st.State, impl.StateExpired)
	}
	if st.Sessions != 0 {
		t.Fatalf("an expired bind must not leave a session, got %d", st.Sessions)
	}
}

func TestBindUsesGraceThenExpires(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	mustProvision(t, d, "alice", "corp", basePassword)
	c.Advance(31 * 24 * time.Hour)
	first := mustBind(t, d, c, "alice", basePassword)
	if !first.Allowed || !first.MustChange {
		t.Fatalf("grace login allowed=%v mustChange=%v, want true and true", first.Allowed, first.MustChange)
	}
	if first.State != impl.StateExpiredGrace {
		t.Fatalf("state = %q, want %q", first.State, impl.StateExpiredGrace)
	}
	if first.GraceRemaining != 1 {
		t.Fatalf("grace remaining = %d, want 1 after one grace login", first.GraceRemaining)
	}
	second := mustBind(t, d, c, "alice", basePassword)
	if second.GraceRemaining != 0 {
		t.Fatalf("grace remaining = %d, want 0", second.GraceRemaining)
	}
	if got := d.GraceUsed("alice"); got != 2 {
		t.Fatalf("grace used = %d, want the policy budget 2", got)
	}
	if _, err := d.Bind("alice", basePassword, c.Now()); !errors.Is(err, impl.ErrPasswordExpired) {
		t.Fatalf("bind after the grace budget returned %v, want ErrPasswordExpired", err)
	}
}

func TestChangeValidatesOldAndNewPassword(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	mustProvision(t, d, "alice", "corp", basePassword)
	if _, err := d.Change("alice", "Wr0ng-Pass!", "An0ther-Pass?"); !errors.Is(err, impl.ErrBadPassword) {
		t.Fatalf("wrong current password returned %v, want ErrBadPassword", err)
	}
	if _, err := d.Change("alice", basePassword, "short"); !errors.Is(err, impl.ErrPasswordPolicy) {
		t.Fatalf("short new password returned %v, want ErrPasswordPolicy", err)
	}
	if _, err := d.Change("alice", basePassword, "alllowercaseonly"); !errors.Is(err, impl.ErrPasswordPolicy) {
		t.Fatalf("single class new password returned %v, want ErrPasswordPolicy", err)
	}
	res, err := d.Change("alice", basePassword, "An0ther-Pass?")
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if !res.Changed {
		t.Fatalf("change result must report the password as changed")
	}
	if st, err := d.Status("alice"); err != nil || st.MustChange {
		t.Fatalf("a completed change must clear the forced change flag, status=%+v err=%v", st, err)
	}
	if err := d.Provision("carol", "corp", basePassword); err != nil {
		t.Fatalf("provision carol: %v", err)
	}
	if _, err := d.Change("nobody", basePassword, "An0ther-Pass?"); !errors.Is(err, impl.ErrAccountNotFound) {
		t.Fatalf("change of an unknown account returned %v, want ErrAccountNotFound", err)
	}
}

func TestChangeRejectsHistoryReuse(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	mustProvision(t, d, "alice", "corp", basePassword)
	if _, err := d.Change("alice", basePassword, "An0ther-Pass?"); err != nil {
		t.Fatalf("first change: %v", err)
	}
	if _, err := d.Change("alice", "An0ther-Pass?", basePassword); !errors.Is(err, impl.ErrPasswordReused) {
		t.Fatalf("reuse of the previous password returned %v, want ErrPasswordReused", err)
	}
	if _, err := d.Change("alice", "An0ther-Pass?", "Th1rd-Pass#word"); err != nil {
		t.Fatalf("second change: %v", err)
	}
	if _, err := d.Change("alice", "Th1rd-Pass#word", "An0ther-Pass?"); !errors.Is(err, impl.ErrPasswordReused) {
		t.Fatalf("reuse of an older password returned %v, want ErrPasswordReused", err)
	}
	current := "Th1rd-Pass#word"
	for i := 0; i < 4; i++ {
		next := fmt.Sprintf("Passw0rd!-%02d", i)
		if _, err := d.Change("alice", current, next); err != nil {
			t.Fatalf("rolling change %d: %v", i, err)
		}
		current = next
	}
	if got := d.HistoryDepth("alice"); got != 3 {
		t.Fatalf("history depth = %d, want the policy cap 3", got)
	}
}

func TestChangeRevokesSessionsAndClearsGrace(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	mustProvision(t, d, "alice", "corp", basePassword)
	mustBind(t, d, c, "alice", basePassword)
	mustBind(t, d, c, "alice", basePassword)
	c.Advance(31 * 24 * time.Hour)
	grace := mustBind(t, d, c, "alice", basePassword)
	if grace.State != impl.StateExpiredGrace {
		t.Fatalf("expected a grace login before the change, got %q", grace.State)
	}
	if got := len(d.Sessions("alice")); got != 3 {
		t.Fatalf("live sessions before the change = %d, want 3", got)
	}
	res, err := d.Change("alice", basePassword, "An0ther-Pass?")
	if err != nil {
		t.Fatalf("change inside the grace period: %v", err)
	}
	if res.SessionsRevoked != 3 {
		t.Fatalf("revoked sessions = %d, want 3", res.SessionsRevoked)
	}
	if got := len(d.Sessions("alice")); got != 0 {
		t.Fatalf("live sessions after the change = %d, want 0", got)
	}
	if got := d.GraceUsed("alice"); got != 0 {
		t.Fatalf("grace counter after the change = %d, want 0", got)
	}
	st, err := d.Status("alice")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.MustChange || st.Failures != 0 || st.PasswordAgeDays != 0 {
		t.Fatalf("status after the change = %+v, want a clean reset", st)
	}
	if st.State != impl.StateActive {
		t.Fatalf("state after the change = %q, want %q", st.State, impl.StateActive)
	}
}

func TestResetInvalidatesOldPasswordAndSessions(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	mustProvision(t, d, "alice", "corp", basePassword)
	mustBind(t, d, c, "alice", basePassword)
	if err := d.Reset("alice", "T3mp-Pass!word", true); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := len(d.Sessions("alice")); got != 0 {
		t.Fatalf("live sessions after the reset = %d, want 0", got)
	}
	if _, err := d.Bind("alice", basePassword, c.Now()); !errors.Is(err, impl.ErrBadPassword) {
		t.Fatalf("bind with the pre reset password returned %v, want ErrBadPassword", err)
	}
	r := mustBind(t, d, c, "alice", "T3mp-Pass!word")
	if !r.Allowed || !r.MustChange {
		t.Fatalf("bind with the temporary password allowed=%v mustChange=%v, want true and true", r.Allowed, r.MustChange)
	}
	if r.State != impl.StateMustChange {
		t.Fatalf("state = %q, want %q", r.State, impl.StateMustChange)
	}
	if err := d.Reset("nobody", "T3mp-Pass!word", true); !errors.Is(err, impl.ErrAccountNotFound) {
		t.Fatalf("reset of an unknown account returned %v, want ErrAccountNotFound", err)
	}
}

func TestSetPolicyBumpsVersionAndValidates(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	next := corpPolicy()
	next.MinLength = 14
	if err := d.SetPolicy("corp", next); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	if got := d.Policy("corp").Version; got != 2 {
		t.Fatalf("policy version after the update = %d, want 2", got)
	}
	if got := d.Policy("corp").MinLength; got != 14 {
		t.Fatalf("policy min length = %d, want 14", got)
	}
	broken := corpPolicy()
	broken.MinLength = 0
	if err := d.SetPolicy("corp", broken); !errors.Is(err, impl.ErrPasswordPolicy) {
		t.Fatalf("invalid policy returned %v, want ErrPasswordPolicy", err)
	}
	if got := d.Policy("corp").MinLength; got != 14 {
		t.Fatalf("rejected policy changed the live policy, min length = %d", got)
	}
	fresh := labPolicy()
	fresh.Tenant = "newco"
	if err := d.SetPolicy("newco", fresh); err != nil {
		t.Fatalf("set policy for a new tenant: %v", err)
	}
	if got := d.Policy("newco").Version; got != 1 {
		t.Fatalf("new tenant policy version = %d, want 1", got)
	}
	def := d.Policy("mystery")
	if def.Version != 0 || def.Tenant != "mystery" {
		t.Fatalf("unknown tenant policy = %+v, want version 0 bound to the tenant name", def)
	}
}

func TestPolicyAppliesPerTenant(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	if err := d.Provision("c1", "corp", "SixSix"); !errors.Is(err, impl.ErrPasswordPolicy) {
		t.Fatalf("corp accepted a six character password: %v", err)
	}
	if err := d.Provision("l1", "lab", "SixSix"); err != nil {
		t.Fatalf("lab rejected a six character password: %v", err)
	}
	if err := d.Provision("l2", "lab", "Twoclasspass"); err != nil {
		t.Fatalf("lab rejected a two class password: %v", err)
	}
	if err := d.Provision("c2", "corp", "Twoclasspass"); !errors.Is(err, impl.ErrPasswordPolicy) {
		t.Fatalf("corp accepted a two class password: %v", err)
	}
}

func TestStatusTracksPasswordAge(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	mustProvision(t, d, "alice", "corp", basePassword)
	c.Advance(10 * 24 * time.Hour)
	st, err := d.Status("alice")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.PasswordAgeDays != 10 {
		t.Fatalf("password age = %d days, want 10", st.PasswordAgeDays)
	}
	r := mustBind(t, d, c, "alice", basePassword)
	if r.PasswordAgeDays != 10 {
		t.Fatalf("bind reported password age %d days, want 10", r.PasswordAgeDays)
	}
	if _, err := d.Status("nobody"); !errors.Is(err, impl.ErrAccountNotFound) {
		t.Fatalf("status of an unknown account returned %v, want ErrAccountNotFound", err)
	}
}

func TestSnapshotRestoreKeepsHistoryAndGrace(t *testing.T) {
	c := newClock()
	cfg := policyConfig(c)
	d := impl.NewDirectoryPasswordPolicy(cfg)
	mustProvision(t, d, "alice", "corp", basePassword)
	if _, err := d.Change("alice", basePassword, "An0ther-Pass?"); err != nil {
		t.Fatalf("change: %v", err)
	}
	c.Advance(31 * 24 * time.Hour)
	mustBind(t, d, c, "alice", "An0ther-Pass?")
	before, err := d.Status("alice")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	data, err := d.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewDirectoryPasswordPolicy(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after, err := restored.Status("alice")
	if err != nil {
		t.Fatalf("status after restore: %v", err)
	}
	if after != before {
		t.Fatalf("restored status = %+v, want %+v", after, before)
	}
	if got := restored.HistoryDepth("alice"); got != 1 {
		t.Fatalf("restored history depth = %d, want 1", got)
	}
	if _, err := restored.Change("alice", "An0ther-Pass?", basePassword); !errors.Is(err, impl.ErrPasswordReused) {
		t.Fatalf("reuse after restore returned %v, want ErrPasswordReused", err)
	}
	if err := restored.Restore([]byte("<<corrupt>>")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot returned %v, want ErrBadSnapshot", err)
	}
	kept, err := restored.Status("alice")
	if err != nil {
		t.Fatalf("status after a rejected restore: %v", err)
	}
	if kept.Account != before.Account || kept.Failures != before.Failures {
		t.Fatalf("rejected restore changed live state: %+v", kept)
	}
}

func TestAuthorizeRequiresLiveSession(t *testing.T) {
	c := newClock()
	d := impl.NewDirectoryPasswordPolicy(policyConfig(c))
	if err := d.Authorize("nobody", "sess-1"); !errors.Is(err, impl.ErrAccountNotFound) {
		t.Fatalf("authorize for an unknown account returned %v, want ErrAccountNotFound", err)
	}
	mustProvision(t, d, "alice", "corp", basePassword)
	if err := d.Authorize("alice", "sess-404"); !errors.Is(err, impl.ErrSessionInvalid) {
		t.Fatalf("authorize with an unknown session returned %v, want ErrSessionInvalid", err)
	}
	r := mustBind(t, d, c, "alice", basePassword)
	if err := d.Authorize("alice", r.SessionID); err != nil {
		t.Fatalf("authorize with the bound session: %v", err)
	}
	if _, err := d.Change("alice", basePassword, "An0ther-Pass?"); err != nil {
		t.Fatalf("change: %v", err)
	}
	if err := d.Authorize("alice", r.SessionID); !errors.Is(err, impl.ErrSessionInvalid) {
		t.Fatalf("authorize with a revoked session returned %v, want ErrSessionInvalid", err)
	}
}
