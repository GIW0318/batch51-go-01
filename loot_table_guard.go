// Package task resolves level loot from weighted tables: every roll is
// independent, a pity counter guarantees the configured quality, and the
// counter follows the player across the scenes of one scene group.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"strings"
	"sync"
)

// Loot qualities.
const (
	QualityCommon = "common"
	QualityRare   = "rare"
	QualityEpic   = "epic"
)

// Audit actions.
const (
	AuditRegisterTable = "register_table"
	AuditRegisterScene = "register_scene"
	AuditRoll          = "roll"
	AuditSwitchScene   = "switch_scene"
	AuditResetPity     = "reset_pity"
)

// Errors returned by the guard.
var (
	ErrBadConfig     = errors.New("limits are invalid")
	ErrBadTable      = errors.New("loot table is invalid")
	ErrTableExists   = errors.New("loot table already registered")
	ErrUnknownTable  = errors.New("loot table not found")
	ErrBadScene      = errors.New("scene is invalid or not assigned")
	ErrSceneExists   = errors.New("scene already belongs to another group")
	ErrUnknownScene  = errors.New("scene not registered")
	ErrBadRequest    = errors.New("roll request is invalid")
	ErrBadCount      = errors.New("roll count is outside the batch budget")
	ErrSceneMismatch = errors.New("table scene group does not match the player scene")
	ErrUnknownPlayer = errors.New("player not found")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Limits bounds the guard and fixes the pity rule.
type Limits struct {
	MaxRollsPerBatch   int
	MaxEntriesPerTable int
	MaxAudit           int
	PityQuality        string
	PityThreshold      int
}

// LootEntry is one weighted item of a table.
type LootEntry struct {
	ItemID  string
	Quality string
	Weight  int
}

// LootTable is the drop table of one scene group.
type LootTable struct {
	TableID    string
	SceneGroup string
	Entries    []LootEntry
}

// Random is the injectable source used to pick an entry. Intn must return a
// value in [0, n).
type Random interface {
	Intn(n int) int
}

// RollRequest asks for a batch of drops for one player.
type RollRequest struct {
	PlayerID string
	SceneID  string
	TableID  string
	Count    int
	Source   Random
}

// Drop is one resolved item.
type Drop struct {
	ItemID  string
	Quality string
	Pity    bool
}

// RollResult is the outcome of one roll batch.
type RollResult struct {
	PlayerID   string
	TableID    string
	SceneGroup string
	Drops      []Drop
	PityBefore int
	PityAfter  int
	Forced     int
	Seq        int64
}

// PityState is the pity counter of the group the player currently stands in.
type PityState struct {
	PlayerID   string
	SceneGroup string
	Counter    int
	Threshold  int
}

// AuditEntry is one line of the loot audit log.
type AuditEntry struct {
	Seq      int64
	Action   string
	PlayerID string
	SceneID  string
	Detail   string
}

// GuardStats sums what the guard has done since construction or restore.
type GuardStats struct {
	Tables   int
	Scenes   int
	Rolls    int
	Drops    int
	Forced   int
	Switches int
}

// playerState is the current scene and the per group pity counters of one
// player. Counters are keyed by scene group so a scene change cannot mix two
// groups together.
type playerState struct {
	Scene    string
	Counters map[string]int
}

// LootTableGuard owns the tables, the scene map and the pity counters.
type LootTableGuard struct {
	mu      sync.Mutex
	limits  Limits
	tables  map[string]LootTable
	scenes  map[string]string
	players map[string]*playerState
	seq     int64
	audit   []AuditEntry
	stats   GuardStats
}

// NewLootTableGuard validates the limits and builds a guard.
func NewLootTableGuard(limits Limits) (*LootTableGuard, error) {
	if limits.MaxRollsPerBatch <= 0 ||
		limits.MaxEntriesPerTable <= 0 ||
		limits.MaxAudit <= 0 ||
		!knownQuality(limits.PityQuality) ||
		limits.PityThreshold <= 0 {
		return nil, ErrBadConfig
	}

	return &LootTableGuard{
		limits:  limits,
		tables:  make(map[string]LootTable),
		scenes:  make(map[string]string),
		players: make(map[string]*playerState),
	}, nil
}

// RegisterScene maps a scene id to a scene group.
func (g *LootTableGuard) RegisterScene(sceneID, group string) error {
	if strings.TrimSpace(sceneID) == "" || strings.TrimSpace(group) == "" {
		return ErrBadScene
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if existing, ok := g.scenes[sceneID]; ok {
		if existing == group {
			return nil
		}
		return ErrSceneExists
	}

	g.scenes[sceneID] = group
	g.stats.Scenes = len(g.scenes)
	g.appendAuditLocked(AuditRegisterScene, "", sceneID, "group="+group)
	return nil
}

// SceneGroupOf reads the scene group of a registered scene.
func (g *LootTableGuard) SceneGroupOf(sceneID string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	group, ok := g.scenes[sceneID]
	return group, ok
}

// RegisterTable validates and stores one weighted loot table.
func (g *LootTableGuard) RegisterTable(table LootTable) error {
	table.Entries = cloneEntries(table.Entries)
	if err := validateTable(table, g.limits); err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.tables[table.TableID]; ok {
		return ErrTableExists
	}

	g.tables[table.TableID] = table
	g.stats.Tables = len(g.tables)
	g.appendAuditLocked(AuditRegisterTable, "", "", "table="+table.TableID+" group="+table.SceneGroup)
	return nil
}

// TableOf reads one registered table.
func (g *LootTableGuard) TableOf(tableID string) (LootTable, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	table, ok := g.tables[tableID]
	if !ok {
		return LootTable{}, false
	}
	return cloneTable(table), true
}

// Roll resolves a batch of independent drops for one player.
func (g *LootTableGuard) Roll(req RollRequest) (RollResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	table, ok := g.tables[req.TableID]
	if !ok {
		return RollResult{}, ErrUnknownTable
	}
	if strings.TrimSpace(req.PlayerID) == "" {
		return RollResult{}, ErrBadRequest
	}
	if req.Count <= 0 || req.Count > g.limits.MaxRollsPerBatch {
		return RollResult{}, ErrBadCount
	}

	player, ok := g.players[req.PlayerID]
	if !ok || player.Scene == "" {
		return RollResult{}, ErrBadScene
	}
	group, ok := g.scenes[player.Scene]
	if !ok {
		return RollResult{}, ErrBadScene
	}
	if table.SceneGroup != group {
		return RollResult{}, ErrSceneMismatch
	}

	source := req.Source
	if source == nil {
		source = defaultRandom{}
	}

	totalWeight, pityWeight := tableWeights(table, g.limits.PityQuality)
	before := player.Counters[group]
	counter := before
	forcedCount := 0
	pityUsed := false
	drops := make([]Drop, 0, req.Count)

	for i := 0; i < req.Count; i++ {
		var drop Drop
		forced := counter >= g.limits.PityThreshold && !pityUsed
		if forced {
			drop = drawPity(table, pityWeight, g.limits.PityQuality, source)
			drop.Pity = true
			counter = 0
			pityUsed = true
			forcedCount++
		} else {
			drop = drawNatural(table, totalWeight, source)
			if drop.Quality == g.limits.PityQuality {
				counter = 0
			} else {
				counter++
			}
		}
		drops = append(drops, drop)
	}

	player.Counters[group] = counter
	g.stats.Rolls++
	g.stats.Drops += len(drops)
	g.stats.Forced += forcedCount
	seq := g.appendAuditLocked(
		AuditRoll,
		req.PlayerID,
		player.Scene,
		fmt.Sprintf("table=%s count=%d forced=%d", table.TableID, req.Count, forcedCount),
	)

	return RollResult{
		PlayerID:   req.PlayerID,
		TableID:    table.TableID,
		SceneGroup: group,
		Drops:      drops,
		PityBefore: before,
		PityAfter:  counter,
		Forced:     forcedCount,
		Seq:        seq,
	}, nil
}

// PityOf reads the pity counter of the group the player stands in.
func (g *LootTableGuard) PityOf(playerID string) (PityState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	player, ok := g.players[playerID]
	if !ok {
		return PityState{}, false
	}
	group, ok := g.scenes[player.Scene]
	if !ok {
		return PityState{}, false
	}

	return PityState{
		PlayerID:   playerID,
		SceneGroup: group,
		Counter:    player.Counters[group],
		Threshold:  g.limits.PityThreshold,
	}, true
}

// ResetPity clears the pity counter of the current group.
func (g *LootTableGuard) ResetPity(playerID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	player, ok := g.players[playerID]
	if !ok {
		return ErrUnknownPlayer
	}
	group, ok := g.scenes[player.Scene]
	if !ok {
		return ErrUnknownPlayer
	}

	player.Counters[group] = 0
	g.appendAuditLocked(AuditResetPity, playerID, player.Scene, "group="+group)
	return nil
}

// SwitchScene moves a player to another scene of the same or another group.
func (g *LootTableGuard) SwitchScene(playerID, sceneID string) error {
	if strings.TrimSpace(playerID) == "" {
		return ErrBadRequest
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	group, ok := g.scenes[sceneID]
	if !ok {
		return ErrUnknownScene
	}

	player, ok := g.players[playerID]
	if !ok {
		player = &playerState{Counters: make(map[string]int)}
		g.players[playerID] = player
	}
	if player.Counters == nil {
		player.Counters = make(map[string]int)
	}

	player.Scene = sceneID
	if _, ok := player.Counters[group]; !ok {
		player.Counters[group] = 0
	}
	g.stats.Switches++
	g.appendAuditLocked(AuditSwitchScene, playerID, sceneID, "group="+group)
	return nil
}

// CurrentScene reads the scene the player is standing in.
func (g *LootTableGuard) CurrentScene(playerID string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	player, ok := g.players[playerID]
	if !ok {
		return "", false
	}
	return player.Scene, player.Scene != ""
}

// Audit returns a copy of the audit log, oldest first.
func (g *LootTableGuard) Audit() []AuditEntry {
	g.mu.Lock()
	defer g.mu.Unlock()

	audit := make([]AuditEntry, len(g.audit))
	copy(audit, g.audit)
	return audit
}

// Stats returns the counters of the guard.
func (g *LootTableGuard) Stats() GuardStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}

// Snapshot serialises the whole state of the guard.
func (g *LootTableGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	payload := snapshotPayload{
		Version: snapshotVersion,
		Scenes:  make(map[string]string, len(g.scenes)),
		Seq:     g.seq,
		Audit:   make([]AuditEntry, len(g.audit)),
	}
	for sceneID, group := range g.scenes {
		payload.Scenes[sceneID] = group
	}
	tableIDs := make([]string, 0, len(g.tables))
	for tableID := range g.tables {
		tableIDs = append(tableIDs, tableID)
	}
	sort.Strings(tableIDs)
	for _, tableID := range tableIDs {
		table := g.tables[tableID]
		payload.Tables = append(payload.Tables, snapshotTable{
			TableID:    table.TableID,
			SceneGroup: table.SceneGroup,
			Entries:    cloneEntries(table.Entries),
		})
	}
	playerIDs := make([]string, 0, len(g.players))
	for playerID := range g.players {
		playerIDs = append(playerIDs, playerID)
	}
	sort.Strings(playerIDs)
	for _, playerID := range playerIDs {
		player := g.players[playerID]
		counters := make(map[string]int, len(player.Counters))
		for group, counter := range player.Counters {
			counters[group] = counter
		}
		payload.Players = append(payload.Players, snapshotPlayer{
			PlayerID: playerID,
			Scene:    player.Scene,
			Counters: counters,
		})
	}
	copy(payload.Audit, g.audit)
	g.mu.Unlock()

	return marshalSnapshot(payload)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *LootTableGuard) Restore(data []byte) error {
	payload, err := unmarshalSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	tables := make(map[string]LootTable, len(payload.Tables))
	for _, item := range payload.Tables {
		table := LootTable{
			TableID:    item.TableID,
			SceneGroup: item.SceneGroup,
			Entries:    cloneEntries(item.Entries),
		}
		if err := validateTable(table, g.limits); err != nil {
			return ErrBadSnapshot
		}
		if _, exists := tables[table.TableID]; exists {
			return ErrBadSnapshot
		}
		tables[table.TableID] = table
	}

	scenes := make(map[string]string, len(payload.Scenes))
	groups := make(map[string]struct{})
	for sceneID, group := range payload.Scenes {
		if strings.TrimSpace(sceneID) == "" || strings.TrimSpace(group) == "" {
			return ErrBadSnapshot
		}
		scenes[sceneID] = group
		groups[group] = struct{}{}
	}

	players := make(map[string]*playerState, len(payload.Players))
	for _, item := range payload.Players {
		if strings.TrimSpace(item.PlayerID) == "" {
			return ErrBadSnapshot
		}
		if _, ok := scenes[item.Scene]; !ok {
			return ErrBadSnapshot
		}
		if _, exists := players[item.PlayerID]; exists {
			return ErrBadSnapshot
		}
		counters := make(map[string]int, len(item.Counters))
		for group, counter := range item.Counters {
			if strings.TrimSpace(group) == "" || counter < 0 {
				return ErrBadSnapshot
			}
			if _, ok := groups[group]; !ok {
				return ErrBadSnapshot
			}
			counters[group] = counter
		}
		group := scenes[item.Scene]
		if _, ok := counters[group]; !ok {
			counters[group] = 0
		}
		players[item.PlayerID] = &playerState{Scene: item.Scene, Counters: counters}
	}

	if payload.Seq < 0 {
		return ErrBadSnapshot
	}
	var previous int64
	for _, entry := range payload.Audit {
		if entry.Seq <= previous || entry.Seq > payload.Seq || !knownAuditAction(entry.Action) {
			return ErrBadSnapshot
		}
		previous = entry.Seq
	}

	audit := make([]AuditEntry, len(payload.Audit))
	copy(audit, payload.Audit)
	if len(audit) > g.limits.MaxAudit {
		audit = append([]AuditEntry(nil), audit[len(audit)-g.limits.MaxAudit:]...)
	}

	g.mu.Lock()
	g.tables = tables
	g.scenes = scenes
	g.players = players
	g.seq = payload.Seq
	g.audit = audit
	g.stats = GuardStats{Tables: len(tables), Scenes: len(scenes)}
	g.mu.Unlock()
	return nil
}

const snapshotVersion = 1

type snapshotTable struct {
	TableID    string      `json:"table_id"`
	SceneGroup string      `json:"scene_group"`
	Entries    []LootEntry `json:"entries"`
}

type snapshotPlayer struct {
	PlayerID string         `json:"player_id"`
	Scene    string         `json:"scene"`
	Counters map[string]int `json:"counters"`
}

type snapshotPayload struct {
	Version int               `json:"version"`
	Tables  []snapshotTable   `json:"tables"`
	Scenes  map[string]string `json:"scenes"`
	Players []snapshotPlayer  `json:"players"`
	Seq     int64             `json:"seq"`
	Audit   []AuditEntry      `json:"audit"`
}

type snapshotEnvelope struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type defaultRandom struct{}

func (defaultRandom) Intn(n int) int {
	return rand.Intn(n)
}

func knownQuality(quality string) bool {
	switch quality {
	case QualityCommon, QualityRare, QualityEpic:
		return true
	default:
		return false
	}
}

func knownAuditAction(action string) bool {
	switch action {
	case AuditRegisterTable, AuditRegisterScene, AuditRoll, AuditSwitchScene, AuditResetPity:
		return true
	default:
		return false
	}
}

func validateTable(table LootTable, limits Limits) error {
	if strings.TrimSpace(table.TableID) == "" ||
		strings.TrimSpace(table.SceneGroup) == "" ||
		len(table.Entries) == 0 ||
		len(table.Entries) > limits.MaxEntriesPerTable {
		return ErrBadTable
	}

	seenItems := make(map[string]struct{}, len(table.Entries))
	totalWeight := 0
	pityWeight := 0
	for _, entry := range table.Entries {
		if !knownQuality(entry.Quality) || entry.Weight < 0 {
			return ErrBadTable
		}
		if _, exists := seenItems[entry.ItemID]; exists {
			return ErrBadTable
		}
		seenItems[entry.ItemID] = struct{}{}
		totalWeight += entry.Weight
		if entry.Quality == limits.PityQuality {
			pityWeight += entry.Weight
		}
	}
	if totalWeight <= 0 || pityWeight <= 0 {
		return ErrBadTable
	}
	return nil
}

func tableWeights(table LootTable, pityQuality string) (int, int) {
	total, pity := 0, 0
	for _, entry := range table.Entries {
		total += entry.Weight
		if entry.Quality == pityQuality {
			pity += entry.Weight
		}
	}
	return total, pity
}

func drawNatural(table LootTable, totalWeight int, source Random) Drop {
	target := randomIndex(totalWeight, source)
	accumulated := 0
	for _, entry := range table.Entries {
		accumulated += entry.Weight
		if target < accumulated {
			return Drop{ItemID: entry.ItemID, Quality: entry.Quality}
		}
	}
	return lastWeightedEntry(table, "")
}

func drawPity(table LootTable, pityWeight int, pityQuality string, source Random) Drop {
	target := randomIndex(pityWeight, source)
	accumulated := 0
	for _, entry := range table.Entries {
		if entry.Quality != pityQuality {
			continue
		}
		accumulated += entry.Weight
		if target < accumulated {
			return Drop{ItemID: entry.ItemID, Quality: entry.Quality}
		}
	}
	return lastWeightedEntry(table, pityQuality)
}

func lastWeightedEntry(table LootTable, quality string) Drop {
	for i := len(table.Entries) - 1; i >= 0; i-- {
		entry := table.Entries[i]
		if entry.Weight > 0 && (quality == "" || entry.Quality == quality) {
			return Drop{ItemID: entry.ItemID, Quality: entry.Quality}
		}
	}
	return Drop{}
}

func randomIndex(n int, source Random) int {
	value := source.Intn(n) % n
	if value < 0 {
		value += n
	}
	return value
}

func cloneEntries(entries []LootEntry) []LootEntry {
	if entries == nil {
		return nil
	}
	clone := make([]LootEntry, len(entries))
	copy(clone, entries)
	return clone
}

func cloneTable(table LootTable) LootTable {
	table.Entries = cloneEntries(table.Entries)
	return table
}

func (g *LootTableGuard) appendAuditLocked(action, playerID, sceneID, detail string) int64 {
	g.seq++
	g.audit = append(g.audit, AuditEntry{
		Seq:      g.seq,
		Action:   action,
		PlayerID: playerID,
		SceneID:  sceneID,
		Detail:   detail,
	})
	if len(g.audit) > g.limits.MaxAudit {
		g.audit = append([]AuditEntry(nil), g.audit[len(g.audit)-g.limits.MaxAudit:]...)
	}
	return g.seq
}

func marshalSnapshot(payload snapshotPayload) ([]byte, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(payloadBytes)
	return json.Marshal(snapshotEnvelope{
		Payload:  payloadBytes,
		Checksum: hex.EncodeToString(checksum[:]),
	})
}

func unmarshalSnapshot(data []byte) (*snapshotPayload, error) {
	var envelope snapshotEnvelope
	if err := decodeJSON(data, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Payload) == 0 {
		return nil, ErrBadSnapshot
	}

	expected, err := hex.DecodeString(envelope.Checksum)
	if err != nil || len(expected) != sha256.Size {
		return nil, ErrBadSnapshot
	}
	actual := sha256.Sum256(envelope.Payload)
	if !bytes.Equal(expected, actual[:]) {
		return nil, ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := decodeJSON(envelope.Payload, &payload); err != nil || payload.Version != snapshotVersion {
		return nil, ErrBadSnapshot
	}
	return &payload, nil
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrBadSnapshot
	}
	return nil
}
