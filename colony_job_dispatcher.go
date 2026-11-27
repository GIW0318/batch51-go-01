// Package task dispatches colony residents to work stations: it scores the
// match, keeps reservations, preempts exhausted workers and carries the colony
// state across a reload.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Assignment states.
const (
	AssignmentActive = "active"
)

// Colony event kinds.
const (
	EventAssigned      = "assigned"
	EventPreempted     = "preempted"
	EventReclaimed     = "reclaimed"
	EventDeferred      = "deferred"
	EventShiftReplaced = "shift_replaced"
)

// Reasons attached to colony events.
const (
	ReasonHunger   = "hunger"
	ReasonFatigue  = "fatigue"
	ReasonTimeout  = "reservation_timeout"
	ReasonDeferred = "deferred"
)

// Errors reported by the dispatcher.
var (
	ErrBadResident       = errors.New("resident id is required")
	ErrDuplicateResident = errors.New("resident already exists")
	ErrBadJob            = errors.New("job id is required")
	ErrDuplicateJob      = errors.New("job already exists")
	ErrUnknownResident   = errors.New("unknown resident")
	ErrUnknownJob        = errors.New("unknown job")
	ErrNotAssigned       = errors.New("resident is not assigned to this job")
	ErrBadReason         = errors.New("preemption reason is required")
	ErrBadUnits          = errors.New("completed units must be positive")
	ErrBadShift          = errors.New("shift boundary is invalid")
	ErrBadSnapshot       = errors.New("snapshot payload is invalid")
)

// ColonyConfig is the dispatch policy of one colony.
type ColonyConfig struct {
	MaxJobsPerTick int
	ReserveTimeout time.Duration
	HungerLimit    int
	FatigueLimit   int
	HealthFloor    int
}

// Resident is one colonist available for work.
type Resident struct {
	ID       string
	Skill    int
	Health   int
	Hunger   int
	Fatigue  int
	Distance int
}

// Job is one work station of the colony.
type Job struct {
	ID       string
	Priority int
	MinSkill int
	Slots    int
}

// Assignment is one resident to job reservation.
type Assignment struct {
	ResidentID string
	JobID      string
	Score      int
	Seq        int64
	State      string
	At         time.Time
}

// ColonyEvent is one entry of the dispatch log.
type ColonyEvent struct {
	Kind       string
	ResidentID string
	JobID      string
	At         time.Time
	Reason     string
}

// ColonyJobDispatcher owns residents, jobs and the dispatch state.
type ColonyJobDispatcher struct {
	cfg ColonyConfig
}

// NewColonyJobDispatcher builds a dispatcher from the colony policy.
func NewColonyJobDispatcher(cfg ColonyConfig) *ColonyJobDispatcher {
	return &ColonyJobDispatcher{cfg: cfg}
}

// AddResident registers one colonist.
func (d *ColonyJobDispatcher) AddResident(r Resident) error {
	return errNotImplemented
}

// AddJob registers one work station.
func (d *ColonyJobDispatcher) AddJob(j Job) error {
	return errNotImplemented
}

// Assign dispatches the residents available at now within this tick budget.
func (d *ColonyJobDispatcher) Assign(now time.Time) []Assignment {
	return nil
}

// Tick runs the periodic upkeep: preemption, reservation reclaim and shift
// replacement.
func (d *ColonyJobDispatcher) Tick(now time.Time) []ColonyEvent {
	return nil
}

// Preempt releases one reservation by hand.
func (d *ColonyJobDispatcher) Preempt(residentID, jobID string, reason string) error {
	return errNotImplemented
}

// Complete books the output of one reservation and frees the station slot.
func (d *ColonyJobDispatcher) Complete(jobID, residentID string, units int) error {
	return errNotImplemented
}

// Produced reports the booked output of one job.
func (d *ColonyJobDispatcher) Produced(jobID string) (int, error) {
	return 0, errNotImplemented
}

// SetCondition overwrites the condition of one resident.
func (d *ColonyJobDispatcher) SetCondition(residentID string, health, hunger, fatigue int) error {
	return errNotImplemented
}

// SetShift records the next shift boundary.
func (d *ColonyJobDispatcher) SetShift(next time.Time) error {
	return errNotImplemented
}

// AssignmentOf reads the live reservation of one resident.
func (d *ColonyJobDispatcher) AssignmentOf(residentID string) (Assignment, bool) {
	return Assignment{}, false
}

// Resident reads one registered colonist.
func (d *ColonyJobDispatcher) Resident(residentID string) (Resident, bool) {
	return Resident{}, false
}

// Job reads one registered work station.
func (d *ColonyJobDispatcher) Job(jobID string) (Job, bool) {
	return Job{}, false
}

// Snapshot serialises residents, jobs, reservations and booked output.
func (d *ColonyJobDispatcher) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the colony state with a snapshot, all or nothing.
func (d *ColonyJobDispatcher) Restore(data []byte) error {
	return errNotImplemented
}
