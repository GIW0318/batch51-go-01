// Package task adjudicates skill casts on the battle server. Cooldowns and
// charge layers are advanced in game ticks, so a paused or replayed battle
// keeps the same timing regardless of wall clock time.
package task

import "errors"

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Audit actions appended by the guard.
const (
	AuditBeginBattle = "begin_battle"
	AuditResetBattle = "reset_battle"
	AuditCast        = "cast"
	AuditSyncTick    = "sync_tick"
)

// Errors returned by the guard.
var (
	ErrBadConfig      = errors.New("cooldown configuration is invalid")
	ErrBadSkill       = errors.New("skill definition is invalid")
	ErrSkillExists    = errors.New("skill already registered")
	ErrUnknownSkill   = errors.New("skill is not registered")
	ErrBadBattle      = errors.New("battle id is required")
	ErrBattleActive   = errors.New("a battle is already running")
	ErrNoBattle       = errors.New("no battle is running")
	ErrUnknownBattle  = errors.New("battle not found")
	ErrBadCaster      = errors.New("caster id is required")
	ErrOnCooldown     = errors.New("skill has no charge or is still cooling")
	ErrBadAdvance     = errors.New("tick advance must be positive")
	ErrTickRegression = errors.New("server tick must not move backwards")
	ErrBadReduction   = errors.New("cooldown reduction must not be negative")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// CooldownConfig bounds the caster table and the audit log.
type CooldownConfig struct {
	MaxCasters int
	MaxAudit   int
}

// SkillDef is the server side definition of one castable skill.
type SkillDef struct {
	ID             string
	CooldownTicks  int64
	MaxCharges     int
	InitialCharges int
	RechargeTicks  int64
}

// CastResult is the receipt of one accepted cast.
type CastResult struct {
	SkillID       string
	CasterID      string
	Tick          int64
	ChargesLeft   int
	CooldownUntil int64
}

// AuditEntry is one recorded guard action.
type AuditEntry struct {
	Seq      int64
	Action   string
	CasterID string
	SkillID  string
}

// GuardStats counts the work done by the guard.
type GuardStats struct {
	Casts          int
	Rejected       int
	RechargeGained int
	Resets         int
}

// SkillCooldownGuard owns the battle clock and the per caster charge state.
type SkillCooldownGuard struct {
	cfg CooldownConfig
}

// NewSkillCooldownGuard validates the configuration and builds a guard.
func NewSkillCooldownGuard(cfg CooldownConfig) (*SkillCooldownGuard, error) {
	return nil, errNotImplemented
}

// RegisterSkill stores a skill definition.
func (g *SkillCooldownGuard) RegisterSkill(def SkillDef) error {
	return errNotImplemented
}

// SkillDefOf reads one registered skill definition.
func (g *SkillCooldownGuard) SkillDefOf(skillID string) (SkillDef, bool) {
	return SkillDef{}, false
}

// BeginBattle opens a battle and moves the clock to the start tick.
func (g *SkillCooldownGuard) BeginBattle(battleID string, startTick int64) error {
	return errNotImplemented
}

// ResetBattle resets every caster for the restart of the same battle.
func (g *SkillCooldownGuard) ResetBattle(battleID string, startTick int64) error {
	return errNotImplemented
}

// Cast consumes one charge of a skill for a caster.
func (g *SkillCooldownGuard) Cast(casterID, skillID string) (CastResult, error) {
	return CastResult{}, errNotImplemented
}

// AdvanceTicks moves the game clock forward and returns the charges gained.
func (g *SkillCooldownGuard) AdvanceTicks(n int64) (int, error) {
	return 0, errNotImplemented
}

// SyncTick calibrates the game clock against the server tick.
func (g *SkillCooldownGuard) SyncTick(serverTick int64) error {
	return errNotImplemented
}

// ChargesOf reports the remaining charge layers of a caster skill.
func (g *SkillCooldownGuard) ChargesOf(casterID, skillID string) (int, bool) {
	return 0, false
}

// CooldownOf reports the remaining cooldown ticks of a caster skill.
func (g *SkillCooldownGuard) CooldownOf(casterID, skillID string) (int64, bool) {
	return 0, false
}

// Castable reports whether the caster can cast the skill right now.
func (g *SkillCooldownGuard) Castable(casterID, skillID string) bool {
	return false
}

// ApplyCooldownReduction shortens the remaining cooldown, never below zero.
func (g *SkillCooldownGuard) ApplyCooldownReduction(casterID, skillID string, ticks int64) error {
	return errNotImplemented
}

// CurrentTick reports the game clock of the running battle.
func (g *SkillCooldownGuard) CurrentTick() int64 {
	return 0
}

// BattleID reports the running battle id.
func (g *SkillCooldownGuard) BattleID() (string, bool) {
	return "", false
}

// Audit returns the recorded actions, oldest first.
func (g *SkillCooldownGuard) Audit() []AuditEntry {
	return nil
}

// Stats reports the counters of the guard.
func (g *SkillCooldownGuard) Stats() GuardStats {
	return GuardStats{}
}

// Snapshot serialises the battle clock and the caster states.
func (g *SkillCooldownGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the guard state with a snapshot, all or nothing.
func (g *SkillCooldownGuard) Restore(data []byte) error {
	return errNotImplemented
}
