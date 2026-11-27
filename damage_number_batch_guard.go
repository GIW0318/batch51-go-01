// Package task batches the floating damage numbers of a combat replay. Events
// are grouped per replay frame, merged per target and kept alive for a fixed
// number of game frames so the display does not depend on wall clock time.
package task

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Damage elements understood by the replay pipeline.
const (
	ElementPhysical = "physical"
	ElementFire     = "fire"
	ElementIce      = "ice"
	ElementPoison   = "poison"
)

// Style prefixes produced by StyleFor.
const (
	StyleNormalPrefix = "normal_"
	StyleCritPrefix   = "crit_"
)

// Audit actions appended by the guard.
const (
	AuditBatchFrame    = "batch_frame"
	AuditExpire        = "expire"
	AuditPlaybackSpeed = "playback_speed"
	AuditReset         = "reset"
)

// Errors returned by the guard.
var (
	ErrBadConfig      = errors.New("batch configuration is invalid")
	ErrBadEvent       = errors.New("damage event is invalid")
	ErrUnknownElement = errors.New("damage element is unknown")
	ErrFrameClosed    = errors.New("replay frame is already batched")
	ErrFrameOrder     = errors.New("replay frame is older than the last batched frame")
	ErrPendingLimit   = errors.New("pending damage events reached the limit")
	ErrTextLimit      = errors.New("live floating texts reached the limit")
	ErrBadSpeed       = errors.New("playback speed is invalid")
	ErrBadAdvance     = errors.New("frame advance must be positive")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

const (
	defaultPlaybackSpeed = 1000
	minPlaybackSpeed     = 1
	maxPlaybackSpeed     = 8000
	snapshotVersion      = 1
	noBatchedFrame       = int64(-1)
	maxFrameValue        = int64(^uint64(0) >> 1)
)

// BatchConfig bounds the replay buffers and the audit log.
type BatchConfig struct {
	FrameRate  int
	LifeFrames int64
	MaxPending int
	MaxTexts   int
	MaxAudit   int
}

// DamageEvent is one hit produced by the combat simulation.
type DamageEvent struct {
	Seq      int64
	Frame    int64
	TargetID string
	SourceID string
	Amount   int64
	Crit     bool
	Element  string
}

// FloatingText is the merged number shown for one target on one frame.
type FloatingText struct {
	TargetID  string
	Frame     int64
	Amount    int64
	Hits      int
	CritCount int
	Element   string
	Style     string
	ExpireAt  int64
}

// ReplayFrame is one exported frame of the replay.
type ReplayFrame struct {
	Frame int64
	Texts []FloatingText
}

// AuditEntry is one recorded guard action.
type AuditEntry struct {
	Seq    int64
	Action string
	Frame  int64
	Detail string
}

// GuardStats counts the work done by the guard.
type GuardStats struct {
	Submitted int
	Batched   int
	Merged    int
	Expired   int
}

// DamageNumberBatchGuard owns the pending events, the batched frames and the
// live floating texts of one replay stream.
type DamageNumberBatchGuard struct {
	mu sync.RWMutex

	cfg          BatchConfig
	playbackRate int
	currentFrame int64
	lastBatched  int64

	pending      map[int64][]DamageEvent
	pendingCount int
	frames       map[int64][]FloatingText
	live         []FloatingText

	stats        GuardStats
	audit        []AuditEntry
	nextAuditSeq int64
}

type snapshotFrame struct {
	Frame int64          `json:"frame"`
	Texts []FloatingText `json:"texts"`
}

type snapshotPending struct {
	Frame  int64         `json:"frame"`
	Events []DamageEvent `json:"events"`
}

type snapshotState struct {
	Version       int               `json:"version"`
	CurrentFrame  int64             `json:"current_frame"`
	PlaybackSpeed int               `json:"playback_speed"`
	BatchedFrames []snapshotFrame   `json:"batched_frames"`
	LiveTexts     []FloatingText    `json:"live_texts"`
	PendingEvents []snapshotPending `json:"pending_events"`
	Stats         GuardStats        `json:"stats"`
	Audit         []AuditEntry      `json:"audit"`
	NextAuditSeq  int64             `json:"next_audit_seq"`
}

// NewDamageNumberBatchGuard validates the configuration and builds a guard.
func NewDamageNumberBatchGuard(cfg BatchConfig) (*DamageNumberBatchGuard, error) {
	if cfg.FrameRate <= 0 || cfg.LifeFrames <= 0 || cfg.MaxPending <= 0 || cfg.MaxTexts <= 0 || cfg.MaxAudit <= 0 {
		return nil, ErrBadConfig
	}
	return &DamageNumberBatchGuard{
		cfg:          cfg,
		playbackRate: defaultPlaybackSpeed,
		lastBatched:  noBatchedFrame,
		pending:      make(map[int64][]DamageEvent),
		frames:       make(map[int64][]FloatingText),
		nextAuditSeq: 1,
	}, nil
}

// Submit accepts one damage event for a not yet batched replay frame.
func (g *DamageNumberBatchGuard) Submit(ev DamageEvent) error {
	if err := validateEvent(ev); err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, closed := g.frames[ev.Frame]; closed {
		return ErrFrameClosed
	}
	if g.lastBatched != noBatchedFrame && ev.Frame < g.lastBatched {
		return ErrFrameOrder
	}
	if g.pendingCount >= g.cfg.MaxPending {
		return ErrPendingLimit
	}

	g.pending[ev.Frame] = append(g.pending[ev.Frame], ev)
	g.pendingCount++
	g.stats.Submitted++
	return nil
}

// BatchFrame merges every pending event of the frame into floating texts.
func (g *DamageNumberBatchGuard) BatchFrame(frame int64) ([]FloatingText, error) {
	if frame < 0 {
		return nil, ErrBadEvent
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if _, closed := g.frames[frame]; closed {
		return nil, ErrFrameClosed
	}
	if g.lastBatched != noBatchedFrame && frame < g.lastBatched {
		return nil, ErrFrameOrder
	}

	events := g.pending[frame]
	texts := g.mergeFrameLocked(frame, events)

	now := g.currentFrame
	if frame > now {
		now = frame
	}
	keptLive := make([]FloatingText, 0, len(g.live)+len(texts))
	expiredOnCommit := 0
	for _, text := range g.live {
		if text.ExpireAt <= now {
			expiredOnCommit++
			continue
		}
		keptLive = append(keptLive, text)
	}
	liveAdditions := 0
	for _, text := range texts {
		if text.ExpireAt > now {
			liveAdditions++
		}
	}
	if len(keptLive)+liveAdditions > g.cfg.MaxTexts {
		return nil, ErrTextLimit
	}

	delete(g.pending, frame)
	g.pendingCount -= len(events)
	g.frames[frame] = cloneTexts(texts)
	g.lastBatched = frame
	g.currentFrame = now
	g.live = keptLive
	g.stats.Expired += expiredOnCommit

	for _, text := range texts {
		if text.ExpireAt > now {
			g.live = append(g.live, text)
		}
	}
	sortTextsByFrameAndTarget(g.live)

	g.stats.Batched += len(texts)
	g.stats.Merged += len(events) - len(texts)
	g.appendAuditLocked(AuditBatchFrame, frame, "texts="+strconv.Itoa(len(texts)))

	return cloneTexts(texts), nil
}

// PendingFrames lists the frames that still hold unbatched events.
func (g *DamageNumberBatchGuard) PendingFrames() []int64 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	frames := make([]int64, 0, len(g.pending))
	for frame, events := range g.pending {
		if len(events) != 0 {
			frames = append(frames, frame)
		}
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i] < frames[j] })
	return frames
}

// SetPlaybackSpeed stores the replay speed in permille of normal speed.
func (g *DamageNumberBatchGuard) SetPlaybackSpeed(permille int) error {
	if permille < minPlaybackSpeed || permille > maxPlaybackSpeed {
		return ErrBadSpeed
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	old := g.playbackRate
	g.playbackRate = permille
	g.appendAuditLocked(AuditPlaybackSpeed, g.currentFrame, strconv.Itoa(old)+"->"+strconv.Itoa(permille))
	return nil
}

// PlaybackSpeed reports the configured replay speed in permille.
func (g *DamageNumberBatchGuard) PlaybackSpeed() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.playbackRate
}

// AdvanceFrames moves the game clock and returns the texts that expired.
func (g *DamageNumberBatchGuard) AdvanceFrames(frames int64) (int, error) {
	if frames <= 0 {
		return 0, ErrBadAdvance
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.currentFrame = addFrameValue(g.currentFrame, frames)
	removed := g.expireLocked()
	if removed > 0 {
		g.appendAuditLocked(AuditExpire, g.currentFrame, "expired="+strconv.Itoa(removed))
	}
	return removed, nil
}

// Expire drops every text whose lifespan ended at the current game frame.
func (g *DamageNumberBatchGuard) Expire() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	removed := g.expireLocked()
	g.appendAuditLocked(AuditExpire, g.currentFrame, "expired="+strconv.Itoa(removed))
	return removed
}

// Live returns the floating texts that are still visible, oldest first.
func (g *DamageNumberBatchGuard) Live() []FloatingText {
	g.mu.RLock()
	defer g.mu.RUnlock()

	texts := cloneTexts(g.live)
	sortTextsByFrameAndTarget(texts)
	return texts
}

// StyleFor derives the floating text style of one incoming event.
func (g *DamageNumberBatchGuard) StyleFor(ev DamageEvent) (string, error) {
	return styleForElement(ev.Element, ev.Crit)
}

// ExportReplay returns every batched frame in ascending frame order.
func (g *DamageNumberBatchGuard) ExportReplay() []ReplayFrame {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.exportReplayLocked()
}

// Stats reports the counters of the guard.
func (g *DamageNumberBatchGuard) Stats() GuardStats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.stats
}

// Audit returns the recorded actions, oldest first.
func (g *DamageNumberBatchGuard) Audit() []AuditEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()

	entries := make([]AuditEntry, len(g.audit))
	copy(entries, g.audit)
	return entries
}

// Snapshot serialises the replay state.
func (g *DamageNumberBatchGuard) Snapshot() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	state := snapshotState{
		Version:       snapshotVersion,
		CurrentFrame:  g.currentFrame,
		PlaybackSpeed: g.playbackRate,
		BatchedFrames: g.snapshotFramesLocked(),
		LiveTexts:     cloneTexts(g.live),
		PendingEvents: g.snapshotPendingLocked(),
		Stats:         g.stats,
		Audit:         make([]AuditEntry, len(g.audit)),
		NextAuditSeq:  g.nextAuditSeq,
	}
	copy(state.Audit, g.audit)
	if state.PendingEvents == nil {
		state.PendingEvents = []snapshotPending{}
	}
	if state.LiveTexts == nil {
		state.LiveTexts = []FloatingText{}
	}
	return json.Marshal(state)
}

// Restore replaces the replay state with a snapshot, all or nothing.
func (g *DamageNumberBatchGuard) Restore(data []byte) error {
	var state snapshotState
	if err := json.Unmarshal(data, &state); err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	restored, err := g.restoreStateLocked(state)
	if err != nil {
		return ErrBadSnapshot
	}

	g.playbackRate = restored.playbackRate
	g.currentFrame = restored.currentFrame
	g.lastBatched = restored.lastBatched
	g.pending = restored.pending
	g.pendingCount = restored.pendingCount
	g.frames = restored.frames
	g.live = restored.live
	g.stats = restored.stats
	g.audit = restored.audit
	g.nextAuditSeq = restored.nextAuditSeq
	return nil
}

// Reset clears the replay state and keeps the configuration.
func (g *DamageNumberBatchGuard) Reset() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	oldFrame := g.currentFrame
	g.currentFrame = 0
	g.lastBatched = noBatchedFrame
	g.pending = make(map[int64][]DamageEvent)
	g.pendingCount = 0
	g.frames = make(map[int64][]FloatingText)
	g.live = nil
	g.stats = GuardStats{}
	g.audit = nil
	g.appendAuditLocked(AuditReset, oldFrame, "replay cleared")
	return nil
}

type restoreState struct {
	playbackRate int
	currentFrame int64
	lastBatched  int64
	pending      map[int64][]DamageEvent
	pendingCount int
	frames       map[int64][]FloatingText
	live         []FloatingText
	stats        GuardStats
	audit        []AuditEntry
	nextAuditSeq int64
}

func (g *DamageNumberBatchGuard) restoreStateLocked(state snapshotState) (restoreState, error) {
	var out restoreState
	if state.Version != snapshotVersion {
		return out, ErrBadSnapshot
	}
	if state.CurrentFrame < 0 || state.PlaybackSpeed < minPlaybackSpeed || state.PlaybackSpeed > maxPlaybackSpeed {
		return out, ErrBadSnapshot
	}
	if state.Stats.Submitted < 0 || state.Stats.Batched < 0 || state.Stats.Merged < 0 || state.Stats.Expired < 0 {
		return out, ErrBadSnapshot
	}
	if state.NextAuditSeq < 1 {
		return out, ErrBadSnapshot
	}

	out.playbackRate = state.PlaybackSpeed
	out.currentFrame = state.CurrentFrame
	out.frames = make(map[int64][]FloatingText, len(state.BatchedFrames))
	out.pending = make(map[int64][]DamageEvent, len(state.PendingEvents))
	out.stats = state.Stats

	var previousFrame int64
	for i, frame := range state.BatchedFrames {
		if frame.Frame < 0 || (i > 0 && frame.Frame <= previousFrame) {
			return restoreState{}, ErrBadSnapshot
		}
		previousFrame = frame.Frame
		texts, err := validateFloatingTexts(frame.Frame, frame.Texts)
		if err != nil {
			return restoreState{}, ErrBadSnapshot
		}
		out.frames[frame.Frame] = texts
		out.lastBatched = frame.Frame
	}

	batchedTexts := make(map[textIdentity]struct{})
	for _, texts := range out.frames {
		for _, text := range texts {
			batchedTexts[identityForText(text)] = struct{}{}
		}
	}

	previousFrame = 0
	for i, pending := range state.PendingEvents {
		if pending.Frame < 0 || len(pending.Events) == 0 || (i > 0 && pending.Frame <= previousFrame) {
			return restoreState{}, ErrBadSnapshot
		}
		if _, closed := out.frames[pending.Frame]; closed {
			return restoreState{}, ErrBadSnapshot
		}
		previousFrame = pending.Frame

		events := make([]DamageEvent, len(pending.Events))
		for j, ev := range pending.Events {
			if validateEvent(ev) != nil || ev.Frame != pending.Frame {
				return restoreState{}, ErrBadSnapshot
			}
			events[j] = ev
		}
		out.pending[pending.Frame] = events
		out.pendingCount += len(events)
	}
	if out.pendingCount > g.cfg.MaxPending {
		return restoreState{}, ErrBadSnapshot
	}

	out.live = make([]FloatingText, len(state.LiveTexts))
	for i, text := range state.LiveTexts {
		validated, err := validateFloatingText(text)
		if err != nil || validated != text || text.ExpireAt <= state.CurrentFrame {
			return restoreState{}, ErrBadSnapshot
		}
		if _, ok := batchedTexts[identityForText(text)]; !ok {
			return restoreState{}, ErrBadSnapshot
		}
		if i > 0 && !textComesAfter(text, out.live[i-1]) {
			return restoreState{}, ErrBadSnapshot
		}
		out.live[i] = text
	}
	if len(out.live) > g.cfg.MaxTexts {
		return restoreState{}, ErrBadSnapshot
	}

	var previousSeq int64
	audit := make([]AuditEntry, len(state.Audit))
	for i, entry := range state.Audit {
		if entry.Seq <= 0 || entry.Seq <= previousSeq || entry.Action == "" {
			return restoreState{}, ErrBadSnapshot
		}
		previousSeq = entry.Seq
		audit[i] = entry
	}
	if len(audit) != 0 && state.NextAuditSeq <= previousSeq {
		return restoreState{}, ErrBadSnapshot
	}
	if len(audit) > g.cfg.MaxAudit {
		audit = append([]AuditEntry(nil), audit[len(audit)-g.cfg.MaxAudit:]...)
	}
	out.audit = audit
	out.nextAuditSeq = state.NextAuditSeq
	return out, nil
}

func (g *DamageNumberBatchGuard) mergeFrameLocked(frame int64, events []DamageEvent) []FloatingText {
	type aggregate struct {
		text FloatingText
	}

	merged := make(map[string]aggregate, len(events))
	for _, ev := range events {
		current, ok := merged[ev.TargetID]
		if !ok {
			current.text = FloatingText{
				TargetID: ev.TargetID,
				Frame:    frame,
				Element:  ev.Element,
				ExpireAt: addFrameValue(frame, g.cfg.LifeFrames),
			}
		}
		current.text.Amount += ev.Amount
		current.text.Hits++
		if ev.Crit {
			current.text.CritCount++
		}
		merged[ev.TargetID] = current
	}

	targets := make([]string, 0, len(merged))
	for target := range merged {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	texts := make([]FloatingText, 0, len(targets))
	for _, target := range targets {
		text := merged[target].text
		text.Style, _ = styleForElement(text.Element, text.CritCount > 0)
		texts = append(texts, text)
	}
	sort.Slice(texts, func(i, j int) bool { return texts[i].TargetID < texts[j].TargetID })
	return texts
}

func (g *DamageNumberBatchGuard) expireLocked() int {
	if len(g.live) == 0 {
		return 0
	}

	kept := g.live[:0]
	removed := 0
	for _, text := range g.live {
		if text.ExpireAt <= g.currentFrame {
			removed++
			continue
		}
		kept = append(kept, text)
	}
	g.live = kept
	g.stats.Expired += removed
	return removed
}

func (g *DamageNumberBatchGuard) appendAuditLocked(action string, frame int64, detail string) {
	g.audit = append(g.audit, AuditEntry{
		Seq:    g.nextAuditSeq,
		Action: action,
		Frame:  frame,
		Detail: detail,
	})
	g.nextAuditSeq++
	if len(g.audit) > g.cfg.MaxAudit {
		drop := len(g.audit) - g.cfg.MaxAudit
		copy(g.audit, g.audit[drop:])
		g.audit = g.audit[:g.cfg.MaxAudit]
	}
}

func (g *DamageNumberBatchGuard) exportReplayLocked() []ReplayFrame {
	frames := make([]int64, 0, len(g.frames))
	for frame := range g.frames {
		frames = append(frames, frame)
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i] < frames[j] })

	replay := make([]ReplayFrame, 0, len(frames))
	for _, frame := range frames {
		replay = append(replay, ReplayFrame{
			Frame: frame,
			Texts: cloneTexts(g.frames[frame]),
		})
	}
	return replay
}

func (g *DamageNumberBatchGuard) snapshotFramesLocked() []snapshotFrame {
	frames := make([]int64, 0, len(g.frames))
	for frame := range g.frames {
		frames = append(frames, frame)
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i] < frames[j] })

	result := make([]snapshotFrame, 0, len(frames))
	for _, frame := range frames {
		result = append(result, snapshotFrame{Frame: frame, Texts: cloneTexts(g.frames[frame])})
	}
	return result
}

func (g *DamageNumberBatchGuard) snapshotPendingLocked() []snapshotPending {
	frames := make([]int64, 0, len(g.pending))
	for frame, events := range g.pending {
		if len(events) != 0 {
			frames = append(frames, frame)
		}
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i] < frames[j] })

	result := make([]snapshotPending, 0, len(frames))
	for _, frame := range frames {
		events := make([]DamageEvent, len(g.pending[frame]))
		copy(events, g.pending[frame])
		result = append(result, snapshotPending{Frame: frame, Events: events})
	}
	return result
}

func validateEvent(ev DamageEvent) error {
	if ev.Seq <= 0 || ev.Frame < 0 || strings.TrimSpace(ev.TargetID) == "" || ev.Amount <= 0 {
		return ErrBadEvent
	}
	if !knownElement(ev.Element) {
		return ErrUnknownElement
	}
	return nil
}

func knownElement(element string) bool {
	switch element {
	case ElementPhysical, ElementFire, ElementIce, ElementPoison:
		return true
	default:
		return false
	}
}

func styleForElement(element string, crit bool) (string, error) {
	if !knownElement(element) {
		return "", ErrUnknownElement
	}
	if crit {
		return StyleCritPrefix + element, nil
	}
	return StyleNormalPrefix + element, nil
}

type textIdentity struct {
	targetID  string
	frame     int64
	amount    int64
	hits      int
	critCount int
	element   string
	style     string
	expireAt  int64
}

func identityForText(text FloatingText) textIdentity {
	return textIdentity{
		targetID:  text.TargetID,
		frame:     text.Frame,
		amount:    text.Amount,
		hits:      text.Hits,
		critCount: text.CritCount,
		element:   text.Element,
		style:     text.Style,
		expireAt:  text.ExpireAt,
	}
}

func validateFloatingTexts(frame int64, texts []FloatingText) ([]FloatingText, error) {
	validated := make([]FloatingText, len(texts))
	for i, text := range texts {
		checked, err := validateFloatingText(text)
		if err != nil || checked != text || text.Frame != frame {
			return nil, ErrBadSnapshot
		}
		if i > 0 && !textComesAfter(text, validated[i-1]) {
			return nil, ErrBadSnapshot
		}
		validated[i] = text
	}
	return validated, nil
}

func validateFloatingText(text FloatingText) (FloatingText, error) {
	if strings.TrimSpace(text.TargetID) == "" || text.Frame < 0 || text.Amount <= 0 || text.Hits <= 0 || text.CritCount < 0 || text.CritCount > text.Hits || text.ExpireAt <= text.Frame {
		return FloatingText{}, ErrBadSnapshot
	}
	expectedStyle, err := styleForElement(text.Element, text.CritCount > 0)
	if err != nil || text.Style != expectedStyle {
		return FloatingText{}, ErrBadSnapshot
	}
	return text, nil
}

func textComesAfter(current, previous FloatingText) bool {
	if current.Frame != previous.Frame {
		return current.Frame > previous.Frame
	}
	return current.TargetID > previous.TargetID
}

func sortTextsByFrameAndTarget(texts []FloatingText) {
	sort.Slice(texts, func(i, j int) bool {
		if texts[i].Frame != texts[j].Frame {
			return texts[i].Frame < texts[j].Frame
		}
		return texts[i].TargetID < texts[j].TargetID
	})
}

func cloneTexts(texts []FloatingText) []FloatingText {
	if texts == nil {
		return nil
	}
	cloned := make([]FloatingText, len(texts))
	copy(cloned, texts)
	return cloned
}

func addFrameValue(frame, delta int64) int64 {
	if delta > 0 && frame > maxFrameValue-delta {
		return maxFrameValue
	}
	return frame + delta
}
