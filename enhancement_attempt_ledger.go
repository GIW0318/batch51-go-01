// Package task settles one equipment enhancement attempt at a time: it books
// the materials, rolls the curve, moves the item level, tracks the per item
// pity counter, spends protective charms and replays idempotent requests.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

// Errors reported by the ledger.
var (
	ErrBadCurve              = errors.New("enhancement curve is invalid")
	ErrBadPity               = errors.New("pity configuration is invalid")
	ErrBadInput              = errors.New("player, item and request key are required")
	ErrBadLevel              = errors.New("level is outside the curve")
	ErrMaxLevel              = errors.New("item is already at the maximum level")
	ErrDamaged               = errors.New("item must be repaired before the next attempt")
	ErrNotDamaged            = errors.New("item is not damaged")
	ErrInsufficientMaterials = errors.New("not enough materials")
	ErrNoCharm               = errors.New("no protective charm in the bag")
	ErrUnknownItem           = errors.New("unknown item")
	ErrBadSnapshot           = errors.New("snapshot payload is invalid")
)

// EnhancementCurve is the level table of one enhancement line. Rates holds one
// success chance per mille for every level below MaxLevel.
type EnhancementCurve struct {
	ID                  string
	MaxLevel            int
	Rates               []int
	MaterialsPerAttempt int
	GoldPerAttempt      int64
	DowngradeOnFail     bool
}

// PityConfig is the guaranteed success rule. A zero Threshold disables it.
type PityConfig struct {
	Threshold      int
	ResetOnSuccess bool
}

// LedgerConfig bounds one ledger.
type LedgerConfig struct {
	Seed       int64
	MaxHistory int
	MaxPity    int
}

// AttemptResult is the booked outcome of one attempt.
type AttemptResult struct {
	Player         string
	ItemID         string
	IdemKey        string
	FromLevel      int
	ToLevel        int
	Success        bool
	PityApplied    bool
	CharmUsed      bool
	MaterialsSpent int
	GoldSpent      int64
	Replayed       bool
}

// Odds is the read only view of the next attempt on one item.
type Odds struct {
	ItemID          string
	Level           int
	SuccessPerMille int
	PityCount       int
	PityThreshold   int
	Materials       int
	Gold            int64
}

// AttemptRecord is one entry of the per item log.
type AttemptRecord struct {
	Seq         int64
	Player      string
	ItemID      string
	FromLevel   int
	ToLevel     int
	Success     bool
	PityApplied bool
	CharmUsed   bool
}

// EnhanceStats summarises one ledger.
type EnhanceStats struct {
	Items      int
	Attempts   int
	Successes  int
	Failures   int
	Replays    int
	CharmsUsed int
}

// EnhancementAttemptLedger owns the item levels, the material bags and the
// request log.
type EnhancementAttemptLedger struct {
	mu sync.Mutex

	cfg      LedgerConfig
	curve    EnhancementCurve
	curveSet bool
	pity     PityConfig

	players map[string]*playerState
	items   map[string]*itemState
	idem    map[string]AttemptResult

	rng  uint64
	seq  int64
	stat EnhanceStats
}

type playerState struct {
	materials int
	charms    int
}

type itemState struct {
	level   int
	pity    int
	damaged bool
	history []AttemptRecord
}

// NewEnhancementAttemptLedger builds a ledger with the given bounds.
func NewEnhancementAttemptLedger(cfg LedgerConfig) *EnhancementAttemptLedger {
	rng := uint64(cfg.Seed) + 0x9e3779b97f4a7c15
	if rng == 0 {
		rng = 0x9e3779b97f4a7c15
	}
	return &EnhancementAttemptLedger{
		cfg:     cfg,
		players: make(map[string]*playerState),
		items:   make(map[string]*itemState),
		idem:    make(map[string]AttemptResult),
		rng:     rng,
	}
}

// SetCurve installs the enhancement curve.
func (l *EnhancementAttemptLedger) SetCurve(c EnhancementCurve) error {
	if err := checkCurve(c); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.curve = cloneCurve(c)
	l.curveSet = true
	return nil
}

// SetPity installs the guaranteed success rule.
func (l *EnhancementAttemptLedger) SetPity(p PityConfig) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !validPity(p, l.cfg.MaxPity) {
		return ErrBadPity
	}
	l.pity = p
	return nil
}

// GrantMaterials adds materials to one player bag.
func (l *EnhancementAttemptLedger) GrantMaterials(player string, n int) error {
	if isBlank(player) || n < 0 {
		return ErrBadInput
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.player(player)
	p.materials = addAmount(p.materials, n)
	return nil
}

// GrantCharms adds protective charms to one player bag.
func (l *EnhancementAttemptLedger) GrantCharms(player string, n int) error {
	if isBlank(player) || n < 0 {
		return ErrBadInput
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.player(player)
	p.charms = addAmount(p.charms, n)
	return nil
}

// Materials reads the material count of one player.
func (l *EnhancementAttemptLedger) Materials(player string) int {
	if isBlank(player) {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if p := l.players[player]; p != nil {
		return p.materials
	}
	return 0
}

// Charms reads the charm count of one player.
func (l *EnhancementAttemptLedger) Charms(player string) int {
	if isBlank(player) {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if p := l.players[player]; p != nil {
		return p.charms
	}
	return 0
}

// SetLevel overwrites the level of one item.
func (l *EnhancementAttemptLedger) SetLevel(itemID string, level int) error {
	if isBlank(itemID) {
		return ErrBadInput
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.curveSet {
		return ErrBadCurve
	}
	if level < 0 || level > l.curve.MaxLevel {
		return ErrBadLevel
	}
	item := l.item(itemID)
	item.level = level
	return nil
}

// Level reads the level of one item.
func (l *EnhancementAttemptLedger) Level(itemID string) (int, error) {
	if isBlank(itemID) {
		return 0, ErrBadInput
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	item := l.items[itemID]
	if item == nil {
		return 0, nil
	}
	return item.level, nil
}

// Preview reports the odds and the cost of the next attempt.
func (l *EnhancementAttemptLedger) Preview(player string, itemID string) (Odds, error) {
	if isBlank(player) || isBlank(itemID) {
		return Odds{}, ErrBadInput
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.curveSet {
		return Odds{}, ErrBadCurve
	}
	level, pity, damaged := 0, 0, false
	if item := l.items[itemID]; item != nil {
		level, pity, damaged = item.level, item.pity, item.damaged
	}
	if damaged {
		return Odds{}, ErrDamaged
	}
	if level >= l.curve.MaxLevel {
		return Odds{}, ErrMaxLevel
	}
	rate := l.curve.Rates[level]
	if l.pity.Threshold > 0 && pity >= l.pity.Threshold {
		rate = 1000
	}
	return Odds{
		ItemID:          itemID,
		Level:           level,
		SuccessPerMille: rate,
		PityCount:       pity,
		PityThreshold:   l.pity.Threshold,
		Materials:       l.curve.MaterialsPerAttempt,
		Gold:            l.curve.GoldPerAttempt,
	}, nil
}

// Attempt books one enhancement attempt.
func (l *EnhancementAttemptLedger) Attempt(player string, itemID string, useCharm bool, idemKey string) (AttemptResult, error) {
	if isBlank(player) || isBlank(itemID) || isBlank(idemKey) {
		return AttemptResult{}, ErrBadInput
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if previous, ok := l.idem[idemKey]; ok {
		l.stat.Replays++
		previous.Replayed = true
		return previous, nil
	}
	if !l.curveSet {
		return AttemptResult{}, ErrBadCurve
	}

	item := l.items[itemID]
	from, pity, damaged := 0, 0, false
	if item != nil {
		from, pity, damaged = item.level, item.pity, item.damaged
	}
	if damaged {
		return AttemptResult{}, ErrDamaged
	}
	if from >= l.curve.MaxLevel {
		return AttemptResult{}, ErrMaxLevel
	}

	bag := l.players[player]
	if bag == nil || bag.materials < l.curve.MaterialsPerAttempt {
		return AttemptResult{}, ErrInsufficientMaterials
	}
	if useCharm && bag.charms < 1 {
		return AttemptResult{}, ErrNoCharm
	}

	pityApplied := l.pity.Threshold > 0 && pity >= l.pity.Threshold
	success := pityApplied
	if !success {
		success = l.rollPerMille() < l.curve.Rates[from]
	}

	to := from
	charmUsed := false
	damageAfter := damaged
	if success {
		to++
		pity = 0
	} else if l.curve.DowngradeOnFail {
		if useCharm {
			charmUsed = true
		} else {
			if to > 0 {
				to--
			}
			damageAfter = true
		}
	}
	if !success && l.pity.Threshold > 0 {
		pity = addAmount(pity, 1)
		if cap := l.pityCap(); pity > cap {
			pity = cap
		}
	}

	if item == nil {
		item = &itemState{}
		l.items[itemID] = item
	}
	item.level = to
	item.pity = pity
	item.damaged = damageAfter

	l.seq++
	record := AttemptRecord{
		Seq:         l.seq,
		Player:      player,
		ItemID:      itemID,
		FromLevel:   from,
		ToLevel:     to,
		Success:     success,
		PityApplied: pityApplied,
		CharmUsed:   charmUsed,
	}
	if limit := l.historyLimit(); limit > 0 {
		item.history = append(item.history, record)
		if len(item.history) > limit {
			trimmed := make([]AttemptRecord, limit)
			copy(trimmed, item.history[len(item.history)-limit:])
			item.history = trimmed
		}
	}

	bag.materials -= l.curve.MaterialsPerAttempt
	if charmUsed {
		bag.charms--
	}

	result := AttemptResult{
		Player:         player,
		ItemID:         itemID,
		IdemKey:        idemKey,
		FromLevel:      from,
		ToLevel:        to,
		Success:        success,
		PityApplied:    pityApplied,
		CharmUsed:      charmUsed,
		MaterialsSpent: l.curve.MaterialsPerAttempt,
		GoldSpent:      l.curve.GoldPerAttempt,
	}
	l.idem[idemKey] = result

	l.stat.Attempts++
	if success {
		l.stat.Successes++
	} else {
		l.stat.Failures++
	}
	if charmUsed {
		l.stat.CharmsUsed++
	}
	return result, nil
}

// Repair clears the damaged state of one item.
func (l *EnhancementAttemptLedger) Repair(itemID string) error {
	if isBlank(itemID) {
		return ErrBadInput
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	item := l.items[itemID]
	if item == nil {
		return ErrUnknownItem
	}
	if !item.damaged {
		return ErrNotDamaged
	}
	item.damaged = false
	return nil
}

// History reads the attempt log of one item, oldest first.
func (l *EnhancementAttemptLedger) History(itemID string) ([]AttemptRecord, error) {
	if isBlank(itemID) {
		return nil, ErrBadInput
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	item := l.items[itemID]
	if item == nil || len(item.history) == 0 {
		return []AttemptRecord{}, nil
	}
	history := make([]AttemptRecord, len(item.history))
	copy(history, item.history)
	return history, nil
}

// Stats reads the counters of the ledger.
func (l *EnhancementAttemptLedger) Stats() EnhanceStats {
	l.mu.Lock()
	defer l.mu.Unlock()
	stats := l.stat
	stats.Items = len(l.items)
	return stats
}

// Snapshot serialises curves, levels, bags and the request log.
func (l *EnhancementAttemptLedger) Snapshot() ([]byte, error) {
	l.mu.Lock()
	snapshot := ledgerSnapshot{
		Config:   l.cfg,
		CurveSet: l.curveSet,
		Curve:    cloneCurve(l.curve),
		Pity:     l.pity,
		RNG:      l.rng,
		Seq:      l.seq,
		Players:  make(map[string]playerSnapshot, len(l.players)),
		Items:    make(map[string]itemSnapshot, len(l.items)),
		Idem:     make(map[string]AttemptResult, len(l.idem)),
		Stats:    l.stat,
	}
	snapshot.Stats.Items = len(l.items)
	for name, player := range l.players {
		snapshot.Players[name] = playerSnapshot{Materials: player.materials, Charms: player.charms}
	}
	for id, item := range l.items {
		history := make([]AttemptRecord, len(item.history))
		copy(history, item.history)
		snapshot.Items[id] = itemSnapshot{
			Level:   item.level,
			Pity:    item.pity,
			Damaged: item.damaged,
			History: history,
		}
	}
	for key, result := range l.idem {
		snapshot.Idem[key] = result
	}
	l.mu.Unlock()

	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		Payload:  payload,
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (l *EnhancementAttemptLedger) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Magic != snapshotMagic || envelope.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var snapshot ledgerSnapshot
	if err := json.Unmarshal(envelope.Payload, &snapshot); err != nil {
		return ErrBadSnapshot
	}
	if err := validateSnapshot(snapshot); err != nil {
		return ErrBadSnapshot
	}

	players := make(map[string]*playerState, len(snapshot.Players))
	for name, player := range snapshot.Players {
		players[name] = &playerState{materials: player.Materials, charms: player.Charms}
	}
	items := make(map[string]*itemState, len(snapshot.Items))
	for id, item := range snapshot.Items {
		history := make([]AttemptRecord, len(item.History))
		copy(history, item.History)
		items[id] = &itemState{
			level:   item.Level,
			pity:    item.Pity,
			damaged: item.Damaged,
			history: history,
		}
	}
	idem := make(map[string]AttemptResult, len(snapshot.Idem))
	for key, result := range snapshot.Idem {
		idem[key] = result
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg = snapshot.Config
	l.curve = cloneCurve(snapshot.Curve)
	l.curveSet = snapshot.CurveSet
	l.pity = snapshot.Pity
	l.players = players
	l.items = items
	l.idem = idem
	l.rng = snapshot.RNG
	l.seq = snapshot.Seq
	l.stat = snapshot.Stats
	l.stat.Items = len(items)
	return nil
}

func checkCurve(c EnhancementCurve) error {
	if isBlank(c.ID) || c.MaxLevel < 1 || len(c.Rates) != c.MaxLevel {
		return ErrBadCurve
	}
	for _, rate := range c.Rates {
		if rate < 0 || rate > 1000 {
			return ErrBadCurve
		}
	}
	return nil
}

func cloneCurve(c EnhancementCurve) EnhancementCurve {
	c.Rates = append([]int(nil), c.Rates...)
	return c
}

func validPity(p PityConfig, maxPity int) bool {
	if p.Threshold < 0 {
		return false
	}
	return maxPity <= 0 || p.Threshold <= maxPity
}

func isBlank(value string) bool {
	return strings.TrimSpace(value) == ""
}

func addAmount(current, amount int) int {
	if amount <= 0 {
		return current
	}
	maxInt := int(^uint(0) >> 1)
	if current > maxInt-amount {
		return maxInt
	}
	return current + amount
}

func (l *EnhancementAttemptLedger) player(name string) *playerState {
	player := l.players[name]
	if player == nil {
		player = &playerState{}
		l.players[name] = player
	}
	return player
}

func (l *EnhancementAttemptLedger) item(id string) *itemState {
	item := l.items[id]
	if item == nil {
		item = &itemState{}
		l.items[id] = item
	}
	return item
}

func (l *EnhancementAttemptLedger) historyLimit() int {
	if l.cfg.MaxHistory <= 0 {
		return 0
	}
	return l.cfg.MaxHistory
}

func (l *EnhancementAttemptLedger) pityCap() int {
	if l.cfg.MaxPity > 0 {
		return l.cfg.MaxPity
	}
	return int(^uint(0) >> 1)
}

func (l *EnhancementAttemptLedger) rollPerMille() int {
	// SplitMix64 keeps the random stream deterministic and snapshots only need
	// to retain one uint64 instead of the internal state of math/rand.Rand.
	l.rng += 0x9e3779b97f4a7c15
	value := l.rng
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	value ^= value >> 31
	return int(value % 1000)
}

const (
	snapshotMagic   = "gsb/batch51-go-game-a-23/enhancement-ledger"
	snapshotVersion = 1
)

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

type ledgerSnapshot struct {
	Config   LedgerConfig              `json:"config"`
	CurveSet bool                      `json:"curve_set"`
	Curve    EnhancementCurve          `json:"curve"`
	Pity     PityConfig                `json:"pity"`
	RNG      uint64                    `json:"rng"`
	Seq      int64                     `json:"seq"`
	Players  map[string]playerSnapshot `json:"players"`
	Items    map[string]itemSnapshot   `json:"items"`
	Idem     map[string]AttemptResult  `json:"idempotency"`
	Stats    EnhanceStats              `json:"stats"`
}

type playerSnapshot struct {
	Materials int `json:"materials"`
	Charms    int `json:"charms"`
}

type itemSnapshot struct {
	Level   int             `json:"level"`
	Pity    int             `json:"pity"`
	Damaged bool            `json:"damaged"`
	History []AttemptRecord `json:"history"`
}

func validateSnapshot(snapshot ledgerSnapshot) error {
	if snapshot.Seq < 0 || !validPity(snapshot.Pity, snapshot.Config.MaxPity) {
		return ErrBadSnapshot
	}
	if snapshot.Players == nil || snapshot.Items == nil || snapshot.Idem == nil {
		return ErrBadSnapshot
	}
	if snapshot.CurveSet {
		if checkCurve(snapshot.Curve) != nil {
			return ErrBadSnapshot
		}
	} else if len(snapshot.Items) != 0 {
		return ErrBadSnapshot
	}
	if snapshot.Stats.Items != len(snapshot.Items) || snapshot.Stats.Attempts < 0 ||
		snapshot.Stats.Successes < 0 || snapshot.Stats.Failures < 0 ||
		snapshot.Stats.Replays < 0 || snapshot.Stats.CharmsUsed < 0 ||
		snapshot.Stats.Successes+snapshot.Stats.Failures != snapshot.Stats.Attempts {
		return ErrBadSnapshot
	}

	for name, player := range snapshot.Players {
		if isBlank(name) || player.Materials < 0 || player.Charms < 0 {
			return ErrBadSnapshot
		}
	}

	historyLimit := snapshot.Config.MaxHistory
	if historyLimit < 0 {
		historyLimit = 0
	}
	pityCap := snapshot.Config.MaxPity
	if pityCap <= 0 {
		pityCap = int(^uint(0) >> 1)
	}
	for id, item := range snapshot.Items {
		if isBlank(id) || item.Pity < 0 || item.Pity > pityCap || len(item.History) > historyLimit {
			return ErrBadSnapshot
		}
		if item.Level < 0 || item.Level > snapshot.Curve.MaxLevel {
			return ErrBadSnapshot
		}
		for _, record := range item.History {
			if record.Seq <= 0 || record.Seq > snapshot.Seq || isBlank(record.Player) ||
				record.ItemID != id || record.FromLevel < 0 || record.FromLevel > snapshot.Curve.MaxLevel ||
				record.ToLevel < 0 || record.ToLevel > snapshot.Curve.MaxLevel {
				return ErrBadSnapshot
			}
		}
	}

	for key, result := range snapshot.Idem {
		if isBlank(key) || result.IdemKey != key || isBlank(result.Player) || isBlank(result.ItemID) ||
			result.FromLevel < 0 || result.FromLevel > snapshot.Curve.MaxLevel ||
			result.ToLevel < 0 || result.ToLevel > snapshot.Curve.MaxLevel ||
			result.MaterialsSpent < 0 || result.Replayed {
			return ErrBadSnapshot
		}
	}
	return nil
}
