// Package task gates system clock steps: manual jumps need a one shot ticket,
// every applied step records a wall clock reading and a monotonic watermark,
// and time sensitive subscribers are paused while the clock is being moved.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

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

// ticketTTL bounds how long an unused ticket stays pending before it is
// voided and stops occupying the pending quota.
const ticketTTL = 2 * time.Minute

// snapshotVersion tags the snapshot payload layout.
const snapshotVersion = 1

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
	ID        string
	Delta     time.Duration
	Source    string
	Actor     string
	Reason    string
	State     string
	Seq       int64
	ExpiresAt time.Time
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
// One mutex guards every field so a clock step, its watermark and the
// subscriber pause flip always commit together.
type ClockStepGuard struct {
	cfg ClockPolicy
	mu  sync.Mutex

	source   string
	wall     time.Time
	mono     int64
	seeded   bool
	steps    int64
	paused   bool
	ntpAccum time.Duration

	tickets   map[string]*Ticket
	ticketSeq int64

	watermarks []Watermark
	wmSeq      int64

	subscribers map[string]*SubscriberState
}

// NewClockStepGuard builds a guard with the given policy.
func NewClockStepGuard(cfg ClockPolicy) *ClockStepGuard {
	return &ClockStepGuard{
		cfg:         cfg,
		tickets:     make(map[string]*Ticket),
		subscribers: make(map[string]*SubscriberState),
	}
}

// SetSource records where the next clock reading comes from.
func (g *ClockStepGuard) SetSource(src string) error {
	switch src {
	case SourceNTP, SourceManual, SourceUnknown:
	default:
		return ErrBadSource
	}
	g.mu.Lock()
	g.source = src
	g.mu.Unlock()
	return nil
}

// SetWall seeds the baseline wall clock and monotonic readings.
func (g *ClockStepGuard) SetWall(t time.Time, mono int64) error {
	if t.IsZero() {
		return ErrBadTimestamp
	}
	if mono < 0 {
		return ErrBadMono
	}
	g.mu.Lock()
	g.wall = t
	g.mono = mono
	g.seeded = true
	g.ntpAccum = 0
	g.mu.Unlock()
	return nil
}

// Request asks for a ticket covering one manual clock step.
func (g *ClockStepGuard) Request(delta time.Duration, reason, actor string) (Ticket, error) {
	reason = strings.TrimSpace(reason)
	actor = strings.TrimSpace(actor)
	if reason == "" {
		return Ticket{}, ErrBadReason
	}
	if actor == "" {
		return Ticket{}, ErrBadActor
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	g.sweepExpiredLocked(now)
	if g.pendingLocked() >= g.cfg.MaxTickets {
		return Ticket{}, ErrTicketLimit
	}
	g.ticketSeq++
	tk := &Ticket{
		ID:        fmt.Sprintf("ticket-%d", g.ticketSeq),
		Delta:     delta,
		Source:    SourceManual,
		Actor:     actor,
		Reason:    reason,
		State:     TicketIssued,
		Seq:       g.ticketSeq,
		ExpiresAt: now.Add(ticketTTL),
	}
	g.tickets[tk.ID] = tk
	return *tk, nil
}

// Apply moves the clock to the given readings, consuming a matching ticket.
func (g *ClockStepGuard) Apply(t time.Time, mono int64) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.seeded || t.IsZero() {
		return ErrBadTimestamp
	}
	if mono < 0 {
		return ErrBadMono
	}
	if g.source != SourceNTP && g.source != SourceManual {
		return ErrUntrustedSource
	}
	if t.Equal(g.wall) {
		return ErrNoStep
	}
	if mono < g.mono {
		return ErrMonoRegression
	}
	delta := t.Sub(g.wall)
	limit := g.cfg.ManualStepLimit
	if g.source == SourceNTP {
		limit = g.cfg.NTPStepLimit
	}
	over := absDuration(delta) > limit
	if g.source == SourceNTP && !over {
		// Smooth NTP slews are judged by their cumulative drift, so many
		// small steps cannot sneak past the threshold one by one.
		over = absDuration(g.ntpAccum+delta) > g.cfg.NTPStepLimit
	}

	var matched *Ticket
	if over {
		g.sweepExpiredLocked(time.Now())
		issued := false
		for _, tk := range g.tickets {
			if tk.State != TicketIssued {
				continue
			}
			issued = true
			if tk.Delta == delta {
				matched = tk
			}
		}
		if matched == nil {
			if issued {
				return ErrTicketMismatch
			}
			return ErrApprovalRequired
		}
	}

	// From here the step, the watermark and the pause flip commit together.
	g.wall = t
	g.mono = mono
	g.steps++
	if g.source == SourceNTP {
		if matched != nil {
			g.ntpAccum = 0
		} else {
			g.ntpAccum += delta
		}
	}
	if matched != nil {
		matched.State = TicketUsed
	}
	g.wmSeq++
	g.watermarks = append(g.watermarks, Watermark{
		Wall:   t,
		Mono:   mono,
		Delta:  delta,
		Source: g.source,
		Seq:    g.wmSeq,
	})
	if cap := g.cfg.MaxWatermarks; cap > 0 && len(g.watermarks) > cap {
		g.watermarks = append([]Watermark(nil), g.watermarks[len(g.watermarks)-cap:]...)
	}
	if over && !g.paused {
		g.paused = true
		for _, sub := range g.subscribers {
			sub.State = SubscriberPaused
			sub.Pauses++
		}
	} else if !over && g.paused {
		g.paused = false
		for _, sub := range g.subscribers {
			sub.State = SubscriberActive
		}
	}
	return nil
}

// Status reports the current clock, counters and pause state.
func (g *ClockStepGuard) Status() ClockStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepExpiredLocked(time.Now())
	return ClockStatus{
		Wall:        g.wall,
		Mono:        g.mono,
		Source:      g.source,
		Steps:       g.steps,
		Paused:      g.paused,
		Pending:     g.pendingLocked(),
		Subscribers: len(g.subscribers),
	}
}

// Subscribe registers one time sensitive downstream subsystem.
func (g *ClockStepGuard) Subscribe(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrBadSubscriber
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.subscribers[name]; ok {
		return ErrDuplicateSubscriber
	}
	if len(g.subscribers) >= g.cfg.MaxSubscribers {
		return ErrSubscriberLimit
	}
	state := SubscriberActive
	if g.paused {
		state = SubscriberPaused
	}
	g.subscribers[name] = &SubscriberState{Name: name, State: state}
	return nil
}

// Unsubscribe removes one downstream subsystem.
func (g *ClockStepGuard) Unsubscribe(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrBadSubscriber
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.subscribers[name]; !ok {
		return ErrUnknownSubscriber
	}
	delete(g.subscribers, name)
	return nil
}

// Subscribers lists the registered subsystems, ordered by name.
func (g *ClockStepGuard) Subscribers() []SubscriberState {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]SubscriberState, 0, len(g.subscribers))
	for _, sub := range g.subscribers {
		out = append(out, *sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Ticket reads one ticket by id.
func (g *ClockStepGuard) Ticket(id string) (Ticket, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	tk, ok := g.tickets[id]
	if !ok {
		return Ticket{}, false
	}
	return *tk, true
}

// RevokeTicket voids one unused ticket.
func (g *ClockStepGuard) RevokeTicket(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	tk, ok := g.tickets[id]
	if !ok {
		return ErrTicketUnknown
	}
	if tk.State == TicketIssued {
		tk.State = TicketRevoked
	}
	return nil
}

// Pending reports how many tickets are still unused.
func (g *ClockStepGuard) Pending() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepExpiredLocked(time.Now())
	return g.pendingLocked()
}

// Watermarks returns the retained clock readings, oldest first.
func (g *ClockStepGuard) Watermarks() []Watermark {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Watermark(nil), g.watermarks...)
}

// clockSnapshot is the serialised guard state.
type clockSnapshot struct {
	Version     int
	Wall        time.Time
	Mono        int64
	Source      string
	Seeded      bool
	Steps       int64
	Paused      bool
	NTPAccum    time.Duration
	WMSeq       int64
	TicketSeq   int64
	Watermarks  []Watermark
	Tickets     []Ticket
	Subscribers []SubscriberState
}

// Snapshot serialises readings, watermarks, tickets and subscribers.
func (g *ClockStepGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	snap := clockSnapshot{
		Version:    snapshotVersion,
		Wall:       g.wall,
		Mono:       g.mono,
		Source:     g.source,
		Seeded:     g.seeded,
		Steps:      g.steps,
		Paused:     g.paused,
		NTPAccum:   g.ntpAccum,
		WMSeq:      g.wmSeq,
		TicketSeq:  g.ticketSeq,
		Watermarks: append([]Watermark(nil), g.watermarks...),
	}
	for _, tk := range g.tickets {
		snap.Tickets = append(snap.Tickets, *tk)
	}
	sort.Slice(snap.Tickets, func(i, j int) bool { return snap.Tickets[i].Seq < snap.Tickets[j].Seq })
	for _, sub := range g.subscribers {
		snap.Subscribers = append(snap.Subscribers, *sub)
	}
	sort.Slice(snap.Subscribers, func(i, j int) bool { return snap.Subscribers[i].Name < snap.Subscribers[j].Name })
	g.mu.Unlock()
	data, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *ClockStepGuard) Restore(data []byte) error {
	var snap clockSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSnapshot, err)
	}
	if snap.Version != snapshotVersion || snap.Mono < 0 {
		return ErrBadSnapshot
	}
	if snap.Seeded && snap.Wall.IsZero() {
		return ErrBadSnapshot
	}
	for i, wm := range snap.Watermarks {
		if wm.Mono < 0 || (i > 0 && wm.Seq <= snap.Watermarks[i-1].Seq) {
			return ErrBadSnapshot
		}
	}
	seenTickets := make(map[string]bool, len(snap.Tickets))
	for _, tk := range snap.Tickets {
		if tk.ID == "" || seenTickets[tk.ID] {
			return ErrBadSnapshot
		}
		seenTickets[tk.ID] = true
	}
	seenSubs := make(map[string]bool, len(snap.Subscribers))
	for _, sub := range snap.Subscribers {
		if strings.TrimSpace(sub.Name) == "" || seenSubs[sub.Name] {
			return ErrBadSnapshot
		}
		seenSubs[sub.Name] = true
	}

	tickets := make(map[string]*Ticket, len(snap.Tickets))
	for _, tk := range snap.Tickets {
		tk := tk
		tickets[tk.ID] = &tk
	}
	subscribers := make(map[string]*SubscriberState, len(snap.Subscribers))
	for _, sub := range snap.Subscribers {
		sub := sub
		subscribers[sub.Name] = &sub
	}

	g.mu.Lock()
	g.wall = snap.Wall
	g.mono = snap.Mono
	g.source = snap.Source
	g.seeded = snap.Seeded
	g.steps = snap.Steps
	g.paused = snap.Paused
	g.ntpAccum = snap.NTPAccum
	g.wmSeq = snap.WMSeq
	g.ticketSeq = snap.TicketSeq
	g.watermarks = append([]Watermark(nil), snap.Watermarks...)
	g.tickets = tickets
	g.subscribers = subscribers
	g.mu.Unlock()
	return nil
}

// sweepExpiredLocked voids issued tickets past their TTL. Caller holds mu.
func (g *ClockStepGuard) sweepExpiredLocked(now time.Time) {
	for _, tk := range g.tickets {
		if tk.State == TicketIssued && now.After(tk.ExpiresAt) {
			tk.State = TicketRevoked
		}
	}
}

// pendingLocked counts unused tickets. Caller holds mu.
func (g *ClockStepGuard) pendingLocked() int {
	n := 0
	for _, tk := range g.tickets {
		if tk.State == TicketIssued {
			n++
		}
	}
	return n
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
