// Package task gates system clock steps: manual jumps need a one shot ticket,
// every applied step records a wall clock reading and a monotonic watermark,
// and time sensitive subscribers are paused while the clock is being moved.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Clock sources accepted by SetSource.
const (
	SourceNTP     = "ntp"
	SourceManual  = "manual"
	SourceUnknown = "unknown"
)

// Subscriber states reported while the clock moves.
const (
	SubscriberActive = "active"
	SubscriberPaused = "paused"
)

// Ticket states reported by Ticket and by Status.
const (
	TicketIssued  = "issued"
	TicketUsed    = "used"
	TicketRevoked = "revoked"
)

// Errors returned by the guard.
var (
	ErrBadSource           = errors.New("unknown clock source")
	ErrBadTimestamp        = errors.New("wall clock reading is required")
	ErrBadMono             = errors.New("monotonic reading must not be negative")
	ErrUntrustedSource     = errors.New("clock source is not trusted")
	ErrNoStep              = errors.New("clock reading did not move")
	ErrMonoRegression      = errors.New("monotonic reading went backwards")
	ErrApprovalRequired    = errors.New("manual clock step requires a ticket")
	ErrBadReason           = errors.New("clock step reason is required")
	ErrBadActor            = errors.New("clock step actor is required")
	ErrTicketLimit         = errors.New("too many pending clock tickets")
	ErrTicketUnknown       = errors.New("clock ticket is unknown")
	ErrTicketMismatch      = errors.New("clock ticket covers a different step")
	ErrTicketRevoked       = errors.New("clock ticket was revoked")
	ErrBadSubscriber       = errors.New("subscriber name is required")
	ErrDuplicateSubscriber = errors.New("subscriber is already registered")
	ErrUnknownSubscriber   = errors.New("subscriber is not registered")
	ErrSubscriberLimit     = errors.New("subscriber limit reached")
	ErrBadSnapshot         = errors.New("snapshot payload is invalid")
)

// ClockPolicy bounds the tickets, subscribers and watermark history.
type ClockPolicy struct {
	ManualStepLimit time.Duration
	NTPStepLimit    time.Duration
	MaxTickets      int
	MaxSubscribers  int
	MaxWatermarks   int
}

// Ticket is one approved clock step.
type Ticket struct {
	ID     string
	Delta  time.Duration
	Source string
	Actor  string
	Reason string
	State  string
	Seq    int64
}

// ClockStatus is the current view of the guarded clock.
type ClockStatus struct {
	Wall        time.Time
	Mono        int64
	Source      string
	Steps       int64
	Paused      bool
	Pending     int
	Subscribers int
}

// Watermark is one recorded clock reading.
type Watermark struct {
	Wall   time.Time
	Mono   int64
	Delta  time.Duration
	Source string
	Seq    int64
}

// SubscriberState is the pause state of one downstream subsystem.
type SubscriberState struct {
	Name   string
	State  string
	Pauses int64
}

// ClockStepGuard owns the clock reading, the tickets and the subscribers.
type ClockStepGuard struct {
	cfg ClockPolicy
}

// NewClockStepGuard builds a guard with the given policy.
func NewClockStepGuard(cfg ClockPolicy) *ClockStepGuard {
	return &ClockStepGuard{cfg: cfg}
}

// SetSource records where the next clock reading comes from.
func (g *ClockStepGuard) SetSource(src string) error {
	return errNotImplemented
}

// SetWall seeds the baseline wall clock and monotonic readings.
func (g *ClockStepGuard) SetWall(t time.Time, mono int64) error {
	return errNotImplemented
}

// Request asks for a ticket covering one manual clock step.
func (g *ClockStepGuard) Request(delta time.Duration, reason, actor string) (Ticket, error) {
	return Ticket{}, errNotImplemented
}

// Apply moves the clock to the given readings, consuming a matching ticket.
func (g *ClockStepGuard) Apply(t time.Time, mono int64) error {
	return errNotImplemented
}

// Status reports the current clock, counters and pause state.
func (g *ClockStepGuard) Status() ClockStatus {
	return ClockStatus{}
}

// Subscribe registers one time sensitive downstream subsystem.
func (g *ClockStepGuard) Subscribe(name string) error {
	return errNotImplemented
}

// Unsubscribe removes one downstream subsystem.
func (g *ClockStepGuard) Unsubscribe(name string) error {
	return errNotImplemented
}

// Subscribers lists the registered subsystems, ordered by name.
func (g *ClockStepGuard) Subscribers() []SubscriberState {
	return nil
}

// Ticket reads one ticket by id.
func (g *ClockStepGuard) Ticket(id string) (Ticket, bool) {
	return Ticket{}, false
}

// RevokeTicket voids one unused ticket.
func (g *ClockStepGuard) RevokeTicket(id string) error {
	return errNotImplemented
}

// Pending reports how many tickets are still unused.
func (g *ClockStepGuard) Pending() int {
	return 0
}

// Watermarks returns the retained clock readings, oldest first.
func (g *ClockStepGuard) Watermarks() []Watermark {
	return nil
}

// Snapshot serialises readings, watermarks, tickets and subscribers.
func (g *ClockStepGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *ClockStepGuard) Restore(data []byte) error {
	return errNotImplemented
}
