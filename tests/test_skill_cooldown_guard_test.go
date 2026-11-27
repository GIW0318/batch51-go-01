package task

import (
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-game-r-09"
)

func baseCooldownConfig() impl.CooldownConfig {
	return impl.CooldownConfig{MaxCasters: 32, MaxAudit: 8}
}

func newCooldownGuard(t *testing.T, cfg impl.CooldownConfig) *impl.SkillCooldownGuard {
	t.Helper()
	g, err := impl.NewSkillCooldownGuard(cfg)
	if err != nil {
		t.Fatalf("new guard: %v", err)
	}
	return g
}

func burstSkill() impl.SkillDef {
	return impl.SkillDef{
		ID:             "burst",
		CooldownTicks:  30,
		MaxCharges:     2,
		InitialCharges: 2,
		RechargeTicks:  15,
	}
}

func TestNewGuardValidatesConfig(t *testing.T) {
	if _, err := impl.NewSkillCooldownGuard(impl.CooldownConfig{MaxCasters: 0, MaxAudit: 8}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero caster limit must fail with ErrBadConfig, got %v", err)
	}
	if _, err := impl.NewSkillCooldownGuard(impl.CooldownConfig{MaxCasters: 8, MaxAudit: 0}); !errors.Is(err, impl.ErrBadConfig) {
		t.Fatalf("zero audit limit must fail with ErrBadConfig, got %v", err)
	}
	g := newCooldownGuard(t, baseCooldownConfig())
	if got := g.CurrentTick(); got != 0 {
		t.Fatalf("fresh tick = %d, want 0", got)
	}
	if _, ok := g.BattleID(); ok {
		t.Fatalf("a fresh guard must not report a running battle")
	}
	if st := g.Stats(); st != (impl.GuardStats{}) {
		t.Fatalf("fresh stats = %+v, want all counters zero", st)
	}
}

func TestRegisterSkillValidation(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	bad := []impl.SkillDef{
		{ID: "  ", CooldownTicks: 10, MaxCharges: 1, InitialCharges: 1},
		{ID: "neg", CooldownTicks: -1, MaxCharges: 1, InitialCharges: 1},
		{ID: "zero-charges", CooldownTicks: 10, MaxCharges: 0, InitialCharges: 0},
		{ID: "too-many-initial", CooldownTicks: 10, MaxCharges: 2, InitialCharges: 3},
		{ID: "negative-initial", CooldownTicks: 10, MaxCharges: 2, InitialCharges: -1},
		{ID: "no-recharge", CooldownTicks: 10, MaxCharges: 2, InitialCharges: 2, RechargeTicks: 0},
	}
	for i, def := range bad {
		if err := g.RegisterSkill(def); !errors.Is(err, impl.ErrBadSkill) {
			t.Fatalf("skill %d must fail with ErrBadSkill, got %v", i, err)
		}
	}
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register a valid skill: %v", err)
	}
	if err := g.RegisterSkill(burstSkill()); !errors.Is(err, impl.ErrSkillExists) {
		t.Fatalf("duplicate skill must fail with ErrSkillExists, got %v", err)
	}
	def, ok := g.SkillDefOf("burst")
	if !ok {
		t.Fatalf("registered skill must be readable")
	}
	if def.CooldownTicks != 30 || def.MaxCharges != 2 || def.InitialCharges != 2 || def.RechargeTicks != 15 {
		t.Fatalf("stored skill = %+v, want the registered definition", def)
	}
	if _, ok := g.SkillDefOf("missing"); ok {
		t.Fatalf("an unknown skill must not be readable")
	}
}

func TestCastRequiresActiveBattle(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if _, err := g.Cast("caster-1", "burst"); !errors.Is(err, impl.ErrNoBattle) {
		t.Fatalf("casting before a battle must fail with ErrNoBattle, got %v", err)
	}
	if err := g.BeginBattle("  ", 0); !errors.Is(err, impl.ErrBadBattle) {
		t.Fatalf("blank battle id must fail with ErrBadBattle, got %v", err)
	}
	if err := g.BeginBattle("b1", 100); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if got := g.CurrentTick(); got != 100 {
		t.Fatalf("tick after begin = %d, want 100", got)
	}
	if err := g.BeginBattle("b2", 0); !errors.Is(err, impl.ErrBattleActive) {
		t.Fatalf("starting a second battle must fail with ErrBattleActive, got %v", err)
	}
}

func TestCastConsumesChargeAndStartsCooldown(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 100); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	res, err := g.Cast("caster-1", "burst")
	if err != nil {
		t.Fatalf("cast: %v", err)
	}
	if res.CasterID != "caster-1" || res.SkillID != "burst" || res.Tick != 100 {
		t.Fatalf("cast receipt = %+v, want caster-1/burst at tick 100", res)
	}
	if res.ChargesLeft != 1 {
		t.Fatalf("charges left in receipt = %d, want 1", res.ChargesLeft)
	}
	if res.CooldownUntil != 130 {
		t.Fatalf("cooldown until = %d, want 130", res.CooldownUntil)
	}
	if got, ok := g.ChargesOf("caster-1", "burst"); !ok || got != 1 {
		t.Fatalf("stored charges = %d/%v, want 1/true", got, ok)
	}
	if got, ok := g.CooldownOf("caster-1", "burst"); !ok || got != 30 {
		t.Fatalf("stored cooldown = %d/%v, want 30/true", got, ok)
	}
	if !g.Castable("caster-1", "burst") {
		t.Fatalf("a caster with a charge and a fresh cooldown window must be castable")
	}
}

func TestCastValidatesCasterAndSkill(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 0); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if _, err := g.Cast("  ", "burst"); !errors.Is(err, impl.ErrBadCaster) {
		t.Fatalf("blank caster must fail with ErrBadCaster, got %v", err)
	}
	if _, err := g.Cast("caster-1", "ghost"); !errors.Is(err, impl.ErrUnknownSkill) {
		t.Fatalf("unknown skill must fail with ErrUnknownSkill, got %v", err)
	}
}

func TestCooldownCountsDownWithTicks(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	strike := impl.SkillDef{ID: "strike", CooldownTicks: 20, MaxCharges: 1, InitialCharges: 1, RechargeTicks: 20}
	if err := g.RegisterSkill(strike); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 0); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if _, err := g.Cast("caster-1", "strike"); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if got, _ := g.CooldownOf("caster-1", "strike"); got != 20 {
		t.Fatalf("cooldown right after the cast = %d, want 20", got)
	}
	if gained, err := g.AdvanceTicks(10); err != nil || gained != 0 {
		t.Fatalf("advance 10 ticks = %d/%v, want 0/nil", gained, err)
	}
	if got, _ := g.CooldownOf("caster-1", "strike"); got != 10 {
		t.Fatalf("cooldown after 10 ticks = %d, want 10", got)
	}
	if _, err := g.AdvanceTicks(5); err != nil {
		t.Fatalf("advance 5 more ticks: %v", err)
	}
	if got, _ := g.CooldownOf("caster-1", "strike"); got != 5 {
		t.Fatalf("cooldown after 15 ticks = %d, want 5", got)
	}
	if _, err := g.AdvanceTicks(5); err != nil {
		t.Fatalf("advance to the end of the cooldown: %v", err)
	}
	if got, _ := g.CooldownOf("caster-1", "strike"); got != 0 {
		t.Fatalf("cooldown at the end = %d, want 0", got)
	}
	if got, _ := g.ChargesOf("caster-1", "strike"); got != 1 {
		t.Fatalf("charges after a full recharge = %d, want 1", got)
	}
}

func TestRechargeTicksAddCharge(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	double := impl.SkillDef{ID: "double", CooldownTicks: 5, MaxCharges: 2, InitialCharges: 2, RechargeTicks: 15}
	if err := g.RegisterSkill(double); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 0); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if _, err := g.Cast("caster-1", "double"); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if got, _ := g.ChargesOf("caster-1", "double"); got != 1 {
		t.Fatalf("charges after the cast = %d, want 1", got)
	}
	if gained, err := g.AdvanceTicks(14); err != nil || gained != 0 {
		t.Fatalf("advance 14 ticks = %d/%v, want 0/nil", gained, err)
	}
	if got, _ := g.ChargesOf("caster-1", "double"); got != 1 {
		t.Fatalf("charges before the recharge tick = %d, want 1", got)
	}
	if gained, err := g.AdvanceTicks(1); err != nil || gained != 1 {
		t.Fatalf("advance to the recharge tick = %d/%v, want 1/nil", gained, err)
	}
	if got, _ := g.ChargesOf("caster-1", "double"); got != 2 {
		t.Fatalf("charges after the recharge = %d, want 2", got)
	}
	if _, err := g.AdvanceTicks(0); !errors.Is(err, impl.ErrBadAdvance) {
		t.Fatalf("advancing zero ticks must fail with ErrBadAdvance, got %v", err)
	}
}

func TestCooldownReductionFloorAndValidation(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	long := impl.SkillDef{ID: "long", CooldownTicks: 40, MaxCharges: 1, InitialCharges: 1, RechargeTicks: 100}
	if err := g.RegisterSkill(long); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 0); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if _, err := g.Cast("caster-1", "long"); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if err := g.ApplyCooldownReduction("caster-1", "long", 15); err != nil {
		t.Fatalf("reduce cooldown: %v", err)
	}
	if got, _ := g.CooldownOf("caster-1", "long"); got != 25 {
		t.Fatalf("cooldown after the reduction = %d, want 25", got)
	}
	if err := g.ApplyCooldownReduction("caster-1", "long", 100); err != nil {
		t.Fatalf("reduce cooldown past zero: %v", err)
	}
	if got, _ := g.CooldownOf("caster-1", "long"); got != 0 {
		t.Fatalf("cooldown after the large reduction = %d, want the zero floor", got)
	}
	if err := g.ApplyCooldownReduction("caster-1", "long", -1); !errors.Is(err, impl.ErrBadReduction) {
		t.Fatalf("negative reduction must fail with ErrBadReduction, got %v", err)
	}
	if err := g.ApplyCooldownReduction("caster-1", "ghost", 1); !errors.Is(err, impl.ErrUnknownSkill) {
		t.Fatalf("reduction of an unknown skill must fail with ErrUnknownSkill, got %v", err)
	}
	if err := g.ApplyCooldownReduction("  ", "long", 1); !errors.Is(err, impl.ErrBadCaster) {
		t.Fatalf("reduction with a blank caster must fail with ErrBadCaster, got %v", err)
	}
}

func TestSyncTickRejectsRegression(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	if err := g.BeginBattle("b1", 100); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if _, err := g.AdvanceTicks(10); err != nil {
		t.Fatalf("advance ticks: %v", err)
	}
	if err := g.SyncTick(105); !errors.Is(err, impl.ErrTickRegression) {
		t.Fatalf("backwards sync must fail with ErrTickRegression, got %v", err)
	}
	if got := g.CurrentTick(); got != 110 {
		t.Fatalf("tick after the rejected sync = %d, want 110", got)
	}
	if err := g.SyncTick(130); err != nil {
		t.Fatalf("forward sync: %v", err)
	}
	if got := g.CurrentTick(); got != 130 {
		t.Fatalf("tick after the forward sync = %d, want 130", got)
	}
}

func TestResetBattleValidation(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.ResetBattle("  ", 0); !errors.Is(err, impl.ErrBadBattle) {
		t.Fatalf("blank battle id must fail with ErrBadBattle, got %v", err)
	}
	if err := g.BeginBattle("b1", 0); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if err := g.ResetBattle("b-404", 0); !errors.Is(err, impl.ErrUnknownBattle) {
		t.Fatalf("resetting an unknown battle must fail with ErrUnknownBattle, got %v", err)
	}
	if err := g.ResetBattle("b1", 0); err != nil {
		t.Fatalf("reset the running battle: %v", err)
	}
	if id, ok := g.BattleID(); !ok || id != "b1" {
		t.Fatalf("battle after reset = %q/%v, want b1/true", id, ok)
	}
}

func TestAuditLogIsCapped(t *testing.T) {
	cfg := baseCooldownConfig()
	cfg.MaxAudit = 2
	g := newCooldownGuard(t, cfg)
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 0); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if _, err := g.Cast("caster-1", "burst"); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if err := g.ResetBattle("b1", 0); err != nil {
		t.Fatalf("reset battle: %v", err)
	}
	audit := g.Audit()
	if len(audit) != 2 {
		t.Fatalf("audit len = %d, want the configured cap 2", len(audit))
	}
	for i := 1; i < len(audit); i++ {
		if audit[i].Seq <= audit[i-1].Seq {
			t.Fatalf("audit sequence must strictly increase, got %+v", audit)
		}
	}
	if audit[len(audit)-1].Action != impl.AuditResetBattle {
		t.Fatalf("newest audit action = %q, want reset_battle", audit[len(audit)-1].Action)
	}
}

func TestStatsCounters(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 0); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if _, err := g.Cast("caster-1", "ghost"); !errors.Is(err, impl.ErrUnknownSkill) {
		t.Fatalf("unknown skill cast must fail with ErrUnknownSkill, got %v", err)
	}
	if _, err := g.Cast("caster-1", "burst"); err != nil {
		t.Fatalf("cast burst: %v", err)
	}
	if gained, err := g.AdvanceTicks(15); err != nil || gained != 1 {
		t.Fatalf("advance to recharge = %d/%v, want 1/nil", gained, err)
	}
	if err := g.ResetBattle("b1", 0); err != nil {
		t.Fatalf("reset battle: %v", err)
	}
	st := g.Stats()
	if st.Casts != 1 || st.Rejected != 1 || st.RechargeGained != 1 || st.Resets != 1 {
		t.Fatalf("stats = %+v, want casts 1, rejected 1, recharge 1, resets 1", st)
	}
}

func TestSnapshotRestoreKeepsCooldownState(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 50); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if _, err := g.Cast("caster-1", "burst"); err != nil {
		t.Fatalf("cast: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := newCooldownGuard(t, baseCooldownConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := restored.CurrentTick(); got != 50 {
		t.Fatalf("restored tick = %d, want 50", got)
	}
	if id, ok := restored.BattleID(); !ok || id != "b1" {
		t.Fatalf("restored battle = %q/%v, want b1/true", id, ok)
	}
	if got, ok := restored.ChargesOf("caster-1", "burst"); !ok || got != 1 {
		t.Fatalf("restored charges = %d/%v, want 1/true", got, ok)
	}
	if got, ok := restored.CooldownOf("caster-1", "burst"); !ok || got != 30 {
		t.Fatalf("restored cooldown = %d/%v, want 30/true", got, ok)
	}
	if _, err := restored.AdvanceTicks(10); err != nil {
		t.Fatalf("advance after restore: %v", err)
	}
	if got, _ := restored.CooldownOf("caster-1", "burst"); got != 20 {
		t.Fatalf("cooldown after ten restored ticks = %d, want 20", got)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 10); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	if _, err := g.Cast("caster-1", "burst"); err != nil {
		t.Fatalf("cast: %v", err)
	}
	if err := g.Restore([]byte("{broken-cooldown-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if id, ok := g.BattleID(); !ok || id != "b1" {
		t.Fatalf("a rejected restore changed the battle to %q/%v, want b1/true", id, ok)
	}
	if got, _ := g.ChargesOf("caster-1", "burst"); got != 1 {
		t.Fatalf("a rejected restore changed charges to %d, want 1", got)
	}
	if got := g.CurrentTick(); got != 10 {
		t.Fatalf("a rejected restore changed the tick to %d, want 10", got)
	}
}

func TestConcurrentCastsPerCaster(t *testing.T) {
	g := newCooldownGuard(t, baseCooldownConfig())
	if err := g.RegisterSkill(burstSkill()); err != nil {
		t.Fatalf("register skill: %v", err)
	}
	if err := g.BeginBattle("b1", 0); err != nil {
		t.Fatalf("begin battle: %v", err)
	}
	const casters = 32
	var wg sync.WaitGroup
	for i := 0; i < casters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			caster := "caster-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			if _, err := g.Cast(caster, "burst"); err != nil {
				t.Errorf("concurrent cast %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if st := g.Stats(); st.Casts != casters {
		t.Fatalf("casts after the race = %d, want %d", st.Casts, casters)
	}
	for i := 0; i < casters; i++ {
		caster := "caster-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if got, ok := g.ChargesOf(caster, "burst"); !ok || got != 1 {
			t.Fatalf("charges of %s = %d/%v, want 1/true", caster, got, ok)
		}
	}
}
