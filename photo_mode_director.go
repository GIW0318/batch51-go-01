// Package task drives the photo mode of an open world game: it freezes the
// world clock, sanitises the scene, clamps the free camera and runs the
// screenshot queue.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// EntityKind names a class of scene entities.
type EntityKind string

// Entity kinds that can be hidden from the viewfinder.
const (
	KindPlayer  EntityKind = "player"
	KindMonster EntityKind = "monster"
	KindProp    EntityKind = "prop"
	KindLoot    EntityKind = "loot"
)

// Capture ticket states.
const (
	TicketQueued    = "queued"
	TicketDone      = "done"
	TicketCancelled = "cancelled"
)

// Errors reported by the director.
var (
	ErrBlankPlayer        = errors.New("player id is required")
	ErrAlreadyInPhotoMode = errors.New("player is already in photo mode")
	ErrSessionLimit       = errors.New("photo mode session limit reached")
	ErrUnknownSession     = errors.New("unknown photo mode session")
	ErrBadEntityKind      = errors.New("unknown entity kind")
	ErrBadCapture         = errors.New("capture request is invalid")
	ErrQueueFull          = errors.New("capture queue is full")
	ErrStorageFull        = errors.New("capture storage budget exhausted")
	ErrTicketNotFound     = errors.New("capture ticket not found")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// Vec3 is a point in world space.
type Vec3 struct {
	X float64
	Y float64
	Z float64
}

// SceneBounds is the closed axis aligned box the free camera may move in.
type SceneBounds struct {
	Min Vec3
	Max Vec3
}

// Sphere blocks the camera. A pose inside the sphere is pushed to its surface.
type Sphere struct {
	Center Vec3
	Radius float64
}

// PhotoConfig bounds one director instance.
type PhotoConfig struct {
	Bounds      SceneBounds
	MinDistance float64
	MaxPerFrame int
	MaxPending  int
	MaxBytes    int64
	MaxSessions int
	Obstacles   []Sphere
}

// CameraPose is the free camera placement. Yaw and Pitch are never touched by
// the clamping logic.
type CameraPose struct {
	Position Vec3
	Target   Vec3
	Yaw      float64
	Pitch    float64
}

// CaptureRequest describes one screenshot the player asked for.
type CaptureRequest struct {
	Width  int
	Height int
	Bytes  int64
	Label  string
}

// Session is one photo mode session owned by a player.
type Session struct {
	ID        string
	PlayerID  string
	EnteredAt time.Time
	WorldTime time.Time
	Seq       int64
}

// Ticket is one queued screenshot.
type Ticket struct {
	ID        string
	SessionID string
	Number    int
	Frame     int
	Bytes     int64
	Label     string
	State     string
}

// LightingState tracks the ambient timeline, which keeps running in photo mode.
type LightingState struct {
	Elapsed time.Duration
}

type sessionState struct {
	session Session
	pose    CameraPose
	hidden  map[EntityKind]struct{}
}

const snapshotVersion = 1

type snapshotPayload struct {
	Version        int               `json:"version"`
	Sessions       []snapshotSession `json:"sessions"`
	Tickets        []Ticket          `json:"tickets"`
	NextSessionSeq int64             `json:"next_session_seq"`
	NextTicketSeq  int64             `json:"next_ticket_seq"`
}

type snapshotSession struct {
	Session Session      `json:"session"`
	Pose    CameraPose   `json:"pose"`
	Hidden  []EntityKind `json:"hidden"`
}

type snapshotEnvelope struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

var entityKinds = []EntityKind{KindLoot, KindMonster, KindPlayer, KindProp}

// PhotoModeDirector owns the live sessions and the screenshot queue.
type PhotoModeDirector struct {
	cfg PhotoConfig

	mu             sync.RWMutex
	sessions       map[string]*sessionState
	players        map[string]string
	tickets        map[string]*Ticket
	nextSessionSeq int64
	nextTicketSeq  int64
}

// NewPhotoModeDirector builds a director with the given limits.
func NewPhotoModeDirector(cfg PhotoConfig) *PhotoModeDirector {
	cfg.Obstacles = append([]Sphere(nil), cfg.Obstacles...)
	return &PhotoModeDirector{
		cfg:            cfg,
		sessions:       make(map[string]*sessionState),
		players:        make(map[string]string),
		tickets:        make(map[string]*Ticket),
		nextSessionSeq: 1,
		nextTicketSeq:  1,
	}
}

// Enter opens a photo mode session for one player at the given instant.
func (p *PhotoModeDirector) Enter(playerID string, at time.Time) (Session, error) {
	if strings.TrimSpace(playerID) == "" {
		return Session{}, ErrBlankPlayer
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.players[playerID]; ok {
		return Session{}, ErrAlreadyInPhotoMode
	}
	if len(p.sessions) >= p.cfg.MaxSessions {
		return Session{}, ErrSessionLimit
	}

	seq := p.nextSessionSeq
	p.nextSessionSeq++
	session := Session{
		ID:        fmt.Sprintf("session-%d", seq),
		PlayerID:  playerID,
		EnteredAt: at,
		WorldTime: at,
		Seq:       seq,
	}
	p.sessions[session.ID] = &sessionState{
		session: session,
		hidden:  make(map[EntityKind]struct{}),
	}
	p.players[playerID] = session.ID
	return session, nil
}

// Session reads one live session.
func (p *PhotoModeDirector) Session(sessionID string) (Session, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return Session{}, false
	}
	return s.session, true
}

// Sessions lists live sessions ordered by session sequence.
func (p *PhotoModeDirector) Sessions() []Session {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]Session, 0, len(p.sessions))
	for _, s := range p.sessions {
		out = append(out, s.session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// SetCamera stores a clamped copy of the requested pose.
func (p *PhotoModeDirector) SetCamera(sessionID string, pose CameraPose) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return ErrUnknownSession
	}
	s.pose = clampPose(pose, p.cfg)
	return nil
}

// Pose returns the clamped pose of one session.
func (p *PhotoModeDirector) Pose(sessionID string) (CameraPose, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return CameraPose{}, ErrUnknownSession
	}
	return s.pose, nil
}

// Hide removes entity kinds from the render and capture output only.
func (p *PhotoModeDirector) Hide(sessionID string, kinds []EntityKind) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return ErrUnknownSession
	}
	for _, kind := range kinds {
		if !validEntityKind(kind) {
			return ErrBadEntityKind
		}
	}
	for _, kind := range kinds {
		s.hidden[kind] = struct{}{}
	}
	return nil
}

// Hidden lists the hidden kinds of one session, sorted by name.
func (p *PhotoModeDirector) Hidden(sessionID string) ([]EntityKind, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return nil, ErrUnknownSession
	}
	return sortedHidden(s.hidden), nil
}

// Renderables lists the kinds still drawn in the viewfinder.
func (p *PhotoModeDirector) Renderables(sessionID string) ([]EntityKind, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return nil, ErrUnknownSession
	}
	out := make([]EntityKind, 0, len(entityKinds))
	for _, kind := range entityKinds {
		if _, hidden := s.hidden[kind]; !hidden {
			out = append(out, kind)
		}
	}
	return out, nil
}

// CollisionKinds lists the kinds that still take part in collision and combat.
func (p *PhotoModeDirector) CollisionKinds(sessionID string) ([]EntityKind, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if _, ok := p.sessions[sessionID]; !ok {
		return nil, ErrUnknownSession
	}
	return append([]EntityKind(nil), entityKinds...), nil
}

// Capture queues one screenshot and returns its ticket.
func (p *PhotoModeDirector) Capture(sessionID string, req CaptureRequest) (Ticket, error) {
	if req.Width <= 0 || req.Height <= 0 || req.Bytes <= 0 {
		return Ticket{}, ErrBadCapture
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return Ticket{}, ErrUnknownSession
	}
	if p.cfg.MaxPerFrame <= 0 {
		return Ticket{}, ErrQueueFull
	}

	pending := 0
	used := int64(0)
	for _, t := range p.tickets {
		if t.SessionID == s.session.ID {
			pending++
			used += t.Bytes
		}
	}
	if pending >= p.cfg.MaxPending {
		return Ticket{}, ErrQueueFull
	}
	if p.cfg.MaxBytes <= 0 || used > p.cfg.MaxBytes || req.Bytes > p.cfg.MaxBytes-used {
		return Ticket{}, ErrStorageFull
	}

	number := p.nextTicketSeq
	p.nextTicketSeq++
	ticket := Ticket{
		ID:        fmt.Sprintf("ticket-%d", number),
		SessionID: s.session.ID,
		Number:    int(number),
		Frame:     int((number-1)/int64(p.cfg.MaxPerFrame) + 1),
		Bytes:     req.Bytes,
		Label:     req.Label,
		State:     TicketQueued,
	}
	p.tickets[ticket.ID] = &ticket
	return ticket, nil
}

// Complete finishes one queued screenshot and releases its storage budget.
func (p *PhotoModeDirector) Complete(ticketID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	t, ok := p.tickets[ticketID]
	if !ok || t.State != TicketQueued {
		return ErrTicketNotFound
	}
	delete(p.tickets, ticketID)
	return nil
}

// Pending lists the unfinished tickets of one session by ticket number.
func (p *PhotoModeDirector) Pending(sessionID string) ([]Ticket, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if _, ok := p.sessions[sessionID]; !ok {
		return nil, ErrUnknownSession
	}
	out := make([]Ticket, 0)
	for _, t := range p.tickets {
		if t.SessionID == sessionID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

// StorageUsed reports the bytes held by the unfinished tickets of a session.
func (p *PhotoModeDirector) StorageUsed(sessionID string) (int64, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if _, ok := p.sessions[sessionID]; !ok {
		return 0, ErrUnknownSession
	}
	var used int64
	for _, t := range p.tickets {
		if t.SessionID == sessionID {
			used += t.Bytes
		}
	}
	return used, nil
}

// Advance is the world clock hook; photo mode must swallow the delta.
func (p *PhotoModeDirector) Advance(sessionID string, delta time.Duration) (time.Time, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return time.Time{}, ErrUnknownSession
	}
	return s.session.WorldTime, nil
}

// Lighting reports how long the ambient timeline has been running at now.
func (p *PhotoModeDirector) Lighting(sessionID string, now time.Time) (LightingState, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return LightingState{}, ErrUnknownSession
	}
	return LightingState{Elapsed: now.Sub(s.session.EnteredAt)}, nil
}

// Exit closes one session and drops its unfinished tickets.
func (p *PhotoModeDirector) Exit(sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.sessions[sessionID]
	if !ok {
		return ErrUnknownSession
	}
	delete(p.sessions, sessionID)
	if sid, ok := p.players[s.session.PlayerID]; ok && sid == sessionID {
		delete(p.players, s.session.PlayerID)
	}
	for id, t := range p.tickets {
		if t.SessionID == sessionID {
			delete(p.tickets, id)
		}
	}
	return nil
}

// Snapshot serialises sessions, poses, hidden sets and the ticket queue.
func (p *PhotoModeDirector) Snapshot() ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	payload := snapshotPayload{
		Version:        snapshotVersion,
		Sessions:       make([]snapshotSession, 0, len(p.sessions)),
		Tickets:        make([]Ticket, 0, len(p.tickets)),
		NextSessionSeq: p.nextSessionSeq,
		NextTicketSeq:  p.nextTicketSeq,
	}
	for _, s := range p.sessions {
		payload.Sessions = append(payload.Sessions, snapshotSession{
			Session: s.session,
			Pose:    s.pose,
			Hidden:  sortedHidden(s.hidden),
		})
	}
	sort.Slice(payload.Sessions, func(i, j int) bool {
		return payload.Sessions[i].Session.Seq < payload.Sessions[j].Session.Seq
	})
	for _, t := range p.tickets {
		payload.Tickets = append(payload.Tickets, *t)
	}
	sort.Slice(payload.Tickets, func(i, j int) bool {
		return payload.Tickets[i].Number < payload.Tickets[j].Number
	})

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	envelope := snapshotEnvelope{
		Payload:  raw,
		Checksum: hex.EncodeToString(sum[:]),
	}
	return json.Marshal(envelope)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *PhotoModeDirector) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if !bytes.Equal([]byte(envelope.Checksum), []byte(hex.EncodeToString(sum[:]))) {
		return ErrBadSnapshot
	}

	var payload snapshotPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil || payload.Version != snapshotVersion {
		return ErrBadSnapshot
	}
	if payload.NextSessionSeq < 1 || payload.NextTicketSeq < 1 {
		return ErrBadSnapshot
	}

	newSessions := make(map[string]*sessionState, len(payload.Sessions))
	newPlayers := make(map[string]string, len(payload.Sessions))
	var maxSessionSeq int64
	for _, snap := range payload.Sessions {
		session := snap.Session
		if session.ID == "" || strings.TrimSpace(session.PlayerID) == "" || session.Seq < 1 {
			return ErrBadSnapshot
		}
		if !session.WorldTime.Equal(session.EnteredAt) {
			return ErrBadSnapshot
		}
		if _, exists := newSessions[session.ID]; exists {
			return ErrBadSnapshot
		}
		if _, exists := newPlayers[session.PlayerID]; exists {
			return ErrBadSnapshot
		}
		if len(newSessions) >= p.cfg.MaxSessions {
			return ErrBadSnapshot
		}

		hidden := make(map[EntityKind]struct{}, len(snap.Hidden))
		for _, kind := range snap.Hidden {
			if !validEntityKind(kind) {
				return ErrBadSnapshot
			}
			if _, exists := hidden[kind]; exists {
				return ErrBadSnapshot
			}
			hidden[kind] = struct{}{}
		}
		newSessions[session.ID] = &sessionState{
			session: session,
			pose:    snap.Pose,
			hidden:  hidden,
		}
		newPlayers[session.PlayerID] = session.ID
		if session.Seq > maxSessionSeq {
			maxSessionSeq = session.Seq
		}
	}
	if payload.NextSessionSeq <= maxSessionSeq {
		return ErrBadSnapshot
	}

	newTickets := make(map[string]*Ticket, len(payload.Tickets))
	pendingBySession := make(map[string]int)
	bytesBySession := make(map[string]int64)
	var maxTicketNumber int64
	for i := range payload.Tickets {
		ticket := payload.Tickets[i]
		if ticket.ID == "" || ticket.SessionID == "" || ticket.Number < 1 || ticket.Frame < 1 ||
			ticket.Bytes <= 0 || ticket.State != TicketQueued {
			return ErrBadSnapshot
		}
		if _, exists := newSessions[ticket.SessionID]; !exists {
			return ErrBadSnapshot
		}
		if _, exists := newTickets[ticket.ID]; exists {
			return ErrBadSnapshot
		}
		for _, existing := range newTickets {
			if existing.Number == ticket.Number {
				return ErrBadSnapshot
			}
		}
		pendingBySession[ticket.SessionID]++
		if pendingBySession[ticket.SessionID] > p.cfg.MaxPending {
			return ErrBadSnapshot
		}
		if p.cfg.MaxBytes <= 0 || bytesBySession[ticket.SessionID] > p.cfg.MaxBytes ||
			ticket.Bytes > p.cfg.MaxBytes-bytesBySession[ticket.SessionID] {
			return ErrBadSnapshot
		}
		bytesBySession[ticket.SessionID] += ticket.Bytes
		tk := ticket
		newTickets[ticket.ID] = &tk
		if int64(ticket.Number) > maxTicketNumber {
			maxTicketNumber = int64(ticket.Number)
		}
	}
	if payload.NextTicketSeq <= maxTicketNumber {
		return ErrBadSnapshot
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions = newSessions
	p.players = newPlayers
	p.tickets = newTickets
	p.nextSessionSeq = payload.NextSessionSeq
	p.nextTicketSeq = payload.NextTicketSeq
	return nil
}

func validEntityKind(kind EntityKind) bool {
	switch kind {
	case KindPlayer, KindMonster, KindProp, KindLoot:
		return true
	default:
		return false
	}
}

func sortedHidden(hidden map[EntityKind]struct{}) []EntityKind {
	out := make([]EntityKind, 0, len(hidden))
	for _, kind := range entityKinds {
		if _, ok := hidden[kind]; ok {
			out = append(out, kind)
		}
	}
	return out
}

func clampPose(pose CameraPose, cfg PhotoConfig) CameraPose {
	position := pose.Position
	target := pose.Target

	if cfg.MinDistance > 0 {
		dx := position.X - target.X
		dy := position.Y - target.Y
		dz := position.Z - target.Z
		distance := math.Sqrt(dx*dx + dy*dy + dz*dz)
		switch {
		case distance == 0:
			position.Z = target.Z + cfg.MinDistance
		case distance < cfg.MinDistance:
			scale := cfg.MinDistance / distance
			position.X = target.X + dx*scale
			position.Y = target.Y + dy*scale
			position.Z = target.Z + dz*scale
		}
	}

	position.X = clamp(position.X, cfg.Bounds.Min.X, cfg.Bounds.Max.X)
	position.Y = clamp(position.Y, cfg.Bounds.Min.Y, cfg.Bounds.Max.Y)
	position.Z = clamp(position.Z, cfg.Bounds.Min.Z, cfg.Bounds.Max.Z)

	for _, obstacle := range cfg.Obstacles {
		if obstacle.Radius <= 0 {
			continue
		}
		dx := position.X - obstacle.Center.X
		dy := position.Y - obstacle.Center.Y
		dz := position.Z - obstacle.Center.Z
		distance := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if distance >= obstacle.Radius {
			continue
		}
		if distance == 0 {
			position.X = obstacle.Center.X + obstacle.Radius
			continue
		}
		scale := obstacle.Radius / distance
		position.X = obstacle.Center.X + dx*scale
		position.Y = obstacle.Center.Y + dy*scale
		position.Z = obstacle.Center.Z + dz*scale
	}

	pose.Position = position
	return pose
}

func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
