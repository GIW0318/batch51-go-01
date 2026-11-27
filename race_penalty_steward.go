// Package task is the stewards desk of a racing event: it reads lap samples,
// grades track limits and contacts, keeps penalties and settles appeals.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still has to be built.
var errNotImplemented = errors.New("not implemented")

// Incident kinds produced by the steward.
const (
	IncidentTrackLimits = "track_limits"
	IncidentReverse     = "reverse"
	IncidentContact     = "contact"
)

// Incident status values.
const (
	StatusOpen     = "open"
	StatusJudged   = "judged"
	StatusAppealed = "appealed"
	StatusFinal    = "final"
)

// Errors reported by the steward.
var (
	ErrUnknownIncident    = errors.New("unknown incident")
	ErrAlreadyApplied     = errors.New("incident is already applied")
	ErrCarMismatch        = errors.New("incident does not belong to this car")
	ErrAppealWindowClosed = errors.New("appeal window is closed")
	ErrNotJudged          = errors.New("incident is not judged yet")
	ErrBadContact         = errors.New("contact report is invalid")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// RaceRules is the sporting regulation of one championship.
type RaceRules struct {
	HalfWidth             float64
	CutTolerance          int
	MinSamples            int
	AppealWindow          time.Duration
	TrackPenaltySeconds   int
	ReversePenaltySeconds int
	ContactPenaltySeconds int
}

// PoseSample is one position sample of a lap.
type PoseSample struct {
	Seq      int
	Lap      int
	At       time.Time
	Offset   float64
	Progress float64
}

// Incident is one judged or pending rule breach.
type Incident struct {
	ID             string
	Kind           string
	CarID          string
	OtherID        string
	Lap            int
	At             time.Time
	Status         string
	Responsibility int
	PenaltySeconds int
}

// Penalty is the sentence of one incident for one car.
type Penalty struct {
	IncidentID string
	CarID      string
	Seconds    int
	Warning    bool
	Reason     string
}

// Contact is a collision report filed by one of the cars involved.
type Contact struct {
	ContactID  string
	ReporterID string
	OtherID    string
	At         time.Time
	Lap        int
	Severity   int
	YieldSpace float64
}

// Evidence is what a team submits with an appeal.
type Evidence struct {
	SubmittedAt time.Time
	Samples     []PoseSample
}

// Verdict is the outcome of one appeal.
type Verdict struct {
	IncidentID string
	CarID      string
	Upheld     bool
	OldSeconds int
	NewSeconds int
	Delta      int
	Message    string
}

// Standing is one row of the classification.
type Standing struct {
	RaceID        string
	CarID         string
	RawMillis     int64
	PenaltyMillis int64
	TotalMillis   int64
	Position      int
	Provisional   bool
}

// Gap records a lap that was too sparse or out of order to judge.
type Gap struct {
	CarID  string
	Lap    int
	Reason string
	At     time.Time
}

// Reward is one prize line of a settled race.
type Reward struct {
	RaceID string
	CarID  string
	Points int
	Amount int
}

// RaceSteward owns incidents, penalties and the classification.
type RaceSteward struct {
	rules RaceRules
}

// NewRaceSteward builds a steward from the sporting regulation.
func NewRaceSteward(rules RaceRules) *RaceSteward {
	return &RaceSteward{rules: rules}
}

// ObserveLap grades one uploaded lap and returns the incidents it produced.
func (s *RaceSteward) ObserveLap(carID string, samples []PoseSample) []Incident {
	return nil
}

// Judge turns one incident into a penalty.
func (s *RaceSteward) Judge(incidentID string) (Penalty, error) {
	return Penalty{}, errNotImplemented
}

// Apply adds one penalty to a car in a race.
func (s *RaceSteward) Apply(raceID, carID string, p Penalty) error {
	return errNotImplemented
}

// Appeal reviews one judged incident with fresh evidence.
func (s *RaceSteward) Appeal(incidentID, carID string, evidence Evidence) (Verdict, error) {
	return Verdict{}, errNotImplemented
}

// Standings returns the classification of one race.
func (s *RaceSteward) Standings(raceID string) ([]Standing, error) {
	return nil, errNotImplemented
}

// ReportContact files a collision report and returns the merged incident.
func (s *RaceSteward) ReportContact(c Contact) (Incident, error) {
	return Incident{}, errNotImplemented
}

// GiveBack records that a car handed the gained position back on the next
// corner, which downgrades a track limits penalty to a warning.
func (s *RaceSteward) GiveBack(incidentID string, evidence Evidence) (Incident, error) {
	return Incident{}, errNotImplemented
}

// Settle pays the prizes of a race once.
func (s *RaceSteward) Settle(raceID string) ([]Reward, error) {
	return nil, errNotImplemented
}

// Gaps lists the laps that could not be judged.
func (s *RaceSteward) Gaps() []Gap {
	return nil
}

// Incident reads one incident by id.
func (s *RaceSteward) Incident(incidentID string) (Incident, bool) {
	return Incident{}, false
}

// Incidents lists every incident in creation order.
func (s *RaceSteward) Incidents() []Incident {
	return nil
}

// RecordResult stores the raw time of one car in one race.
func (s *RaceSteward) RecordResult(raceID, carID string, rawMillis int64) error {
	return errNotImplemented
}

// Rewards lists the prize lines once a race has been settled.
func (s *RaceSteward) Rewards(raceID string) ([]Reward, error) {
	return nil, errNotImplemented
}

// Snapshot serialises incidents, penalties and the classification.
func (s *RaceSteward) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the steward state with a snapshot, all or nothing.
func (s *RaceSteward) Restore(data []byte) error {
	return errNotImplemented
}
