// Package task drives the photo mode of an open world game: it freezes the
// world clock, sanitises the scene, clamps the free camera and runs the
// screenshot queue.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

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

// PhotoModeDirector owns the live sessions and the screenshot queue.
type PhotoModeDirector struct {
	cfg PhotoConfig
}

// NewPhotoModeDirector builds a director with the given limits.
func NewPhotoModeDirector(cfg PhotoConfig) *PhotoModeDirector {
	return &PhotoModeDirector{cfg: cfg}
}

// Enter opens a photo mode session for one player at the given instant.
func (p *PhotoModeDirector) Enter(playerID string, at time.Time) (Session, error) {
	return Session{}, errNotImplemented
}

// Session reads one live session.
func (p *PhotoModeDirector) Session(sessionID string) (Session, bool) {
	return Session{}, false
}

// Sessions lists live sessions ordered by session sequence.
func (p *PhotoModeDirector) Sessions() []Session {
	return nil
}

// SetCamera stores a clamped copy of the requested pose.
func (p *PhotoModeDirector) SetCamera(sessionID string, pose CameraPose) error {
	return errNotImplemented
}

// Pose returns the clamped pose of one session.
func (p *PhotoModeDirector) Pose(sessionID string) (CameraPose, error) {
	return CameraPose{}, errNotImplemented
}

// Hide removes entity kinds from the render and capture output only.
func (p *PhotoModeDirector) Hide(sessionID string, kinds []EntityKind) error {
	return errNotImplemented
}

// Hidden lists the hidden kinds of one session, sorted by name.
func (p *PhotoModeDirector) Hidden(sessionID string) ([]EntityKind, error) {
	return nil, errNotImplemented
}

// Renderables lists the kinds still drawn in the viewfinder.
func (p *PhotoModeDirector) Renderables(sessionID string) ([]EntityKind, error) {
	return nil, errNotImplemented
}

// CollisionKinds lists the kinds that still take part in collision and combat.
func (p *PhotoModeDirector) CollisionKinds(sessionID string) ([]EntityKind, error) {
	return nil, errNotImplemented
}

// Capture queues one screenshot and returns its ticket.
func (p *PhotoModeDirector) Capture(sessionID string, req CaptureRequest) (Ticket, error) {
	return Ticket{}, errNotImplemented
}

// Complete finishes one queued screenshot and releases its storage budget.
func (p *PhotoModeDirector) Complete(ticketID string) error {
	return errNotImplemented
}

// Pending lists the unfinished tickets of one session by ticket number.
func (p *PhotoModeDirector) Pending(sessionID string) ([]Ticket, error) {
	return nil, errNotImplemented
}

// StorageUsed reports the bytes held by the unfinished tickets of a session.
func (p *PhotoModeDirector) StorageUsed(sessionID string) (int64, error) {
	return 0, errNotImplemented
}

// Advance is the world clock hook; photo mode must swallow the delta.
func (p *PhotoModeDirector) Advance(sessionID string, delta time.Duration) (time.Time, error) {
	return time.Time{}, errNotImplemented
}

// Lighting reports how long the ambient timeline has been running at now.
func (p *PhotoModeDirector) Lighting(sessionID string, now time.Time) (LightingState, error) {
	return LightingState{}, errNotImplemented
}

// Exit closes one session and drops its unfinished tickets.
func (p *PhotoModeDirector) Exit(sessionID string) error {
	return errNotImplemented
}

// Snapshot serialises sessions, poses, hidden sets and the ticket queue.
func (p *PhotoModeDirector) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (p *PhotoModeDirector) Restore(data []byte) error {
	return errNotImplemented
}
