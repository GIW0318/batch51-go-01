// Package task is the stewards desk of a racing event: it reads lap samples,
// grades track limits and contacts, keeps penalties and settles appeals.
package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

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

type contactClaim struct {
	ReporterID     string
	OtherID        string
	Responsibility int
	Severity       int
	YieldSpace     float64
	At             time.Time
}

type incidentRecord struct {
	incident    Incident
	warning     bool
	judged      bool
	penalty     Penalty
	givenBack   bool
	givenBackAt time.Time
	claims      []contactClaim
}

type appliedPenalty struct {
	RaceID  string
	CarID   string
	Seconds int
	Warning bool
}

type resultState struct {
	RawMillis     int64
	PenaltyMillis int64
}

type settlementState struct {
	Rewards []Reward
}

type snapshotIncident struct {
	Incident
	Warning     bool           `json:"warning"`
	Judged      bool           `json:"judged"`
	Penalty     Penalty        `json:"penalty"`
	GivenBack   bool           `json:"given_back"`
	GivenBackAt time.Time      `json:"given_back_at"`
	Claims      []contactClaim `json:"claims"`
}

type snapshotEnvelope struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	State    json.RawMessage `json:"state"`
	Checksum string          `json:"checksum"`
}

type snapshotState struct {
	NextIncident int64                             `json:"next_incident"`
	Incidents    []snapshotIncident                `json:"incidents"`
	Gaps         []Gap                             `json:"gaps"`
	Results      map[string]map[string]resultState `json:"results"`
	Applied      map[string]appliedPenalty         `json:"applied"`
	Contacts     map[string]string                 `json:"contacts"`
	Settlements  map[string]settlementState        `json:"settlements"`
}

const (
	snapshotFormat  = "gsb/race-penalty-steward"
	snapshotVersion = 1
)

var (
	errNoRaceResult  = errors.New("race result does not exist")
	errBadRaceResult = errors.New("invalid race result")
	errGiveBackKind  = errors.New("give back applies only to track limits")
)

// RaceSteward owns incidents, penalties and the classification.
type RaceSteward struct {
	rules RaceRules

	mu            sync.RWMutex
	nextIncident  int64
	incidentOrder []string
	incidents     map[string]*incidentRecord
	gaps          []Gap
	results       map[string]map[string]*resultState
	applied       map[string]appliedPenalty
	contacts      map[string]string
	settlements   map[string]settlementState
}

// NewRaceSteward builds a steward from the sporting regulation.
func NewRaceSteward(rules RaceRules) *RaceSteward {
	s := &RaceSteward{rules: rules}
	s.initLocked()
	return s
}

// ObserveLap grades one uploaded lap and returns the incidents it produced.
func (s *RaceSteward) ObserveLap(carID string, samples []PoseSample) []Incident {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()

	if len(samples) < s.rules.MinSamples {
		s.gaps = append(s.gaps, gapForSamples(carID, samples, "short_lap"))
		return []Incident{}
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].Seq <= samples[i-1].Seq || !samples[i].At.After(samples[i-1].At) {
			s.gaps = append(s.gaps, gapForSamples(carID, samples, "out_of_order"))
			return []Incident{}
		}
	}

	type breach struct {
		index    int
		priority int
		kind     string
		sample   PoseSample
	}
	breaches := make([]breach, 0, 2)

	tolerance := s.rules.CutTolerance
	if tolerance < 1 {
		tolerance = 1
	}
	offCount := 0
	offStart := -1
	for i, sample := range samples {
		if math.Abs(sample.Offset) > s.rules.HalfWidth {
			if offCount == 0 {
				offStart = i
			}
			offCount++
			if offCount == tolerance {
				breaches = append(breaches, breach{
					index:    offStart,
					priority: 0,
					kind:     IncidentTrackLimits,
					sample:   samples[offStart],
				})
			}
			continue
		}
		offCount = 0
		offStart = -1
	}

	reverseSegments := 0
	reverseStart := -1
	for i := 1; i < len(samples); i++ {
		if samples[i].Progress < samples[i-1].Progress {
			if reverseSegments == 0 {
				reverseStart = i - 1
			}
			reverseSegments++
			if reverseSegments == 2 {
				breaches = append(breaches, breach{
					index:    reverseStart,
					priority: 1,
					kind:     IncidentReverse,
					sample:   samples[reverseStart],
				})
			}
			continue
		}
		reverseSegments = 0
		reverseStart = -1
	}

	sort.SliceStable(breaches, func(i, j int) bool {
		if breaches[i].index != breaches[j].index {
			return breaches[i].index < breaches[j].index
		}
		return breaches[i].priority < breaches[j].priority
	})

	created := make([]Incident, 0, len(breaches))
	for _, item := range breaches {
		seconds := s.rules.TrackPenaltySeconds
		if item.kind == IncidentReverse {
			seconds = s.rules.ReversePenaltySeconds
		}
		inc := Incident{
			Kind:           item.kind,
			CarID:          carID,
			Lap:            item.sample.Lap,
			At:             item.sample.At,
			Status:         StatusOpen,
			PenaltySeconds: seconds,
		}
		created = append(created, s.addIncidentLocked(inc))
	}
	return created
}

// Judge turns one incident into a penalty.
func (s *RaceSteward) Judge(incidentID string) (Penalty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()

	rec, ok := s.incidents[incidentID]
	if !ok {
		return Penalty{}, ErrUnknownIncident
	}
	if rec.judged {
		return rec.penalty, nil
	}

	rec.judged = true
	rec.incident.Status = StatusJudged
	rec.penalty = Penalty{
		IncidentID: rec.incident.ID,
		CarID:      rec.incident.CarID,
		Seconds:    rec.incident.PenaltySeconds,
		Warning:    rec.warning,
		Reason:     rec.incident.Kind,
	}
	return rec.penalty, nil
}

// Apply adds one penalty to a car in a race.
func (s *RaceSteward) Apply(raceID, carID string, p Penalty) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()

	rec, ok := s.incidents[p.IncidentID]
	if !ok {
		return ErrUnknownIncident
	}
	if carID != rec.incident.CarID || p.CarID != rec.incident.CarID {
		return ErrCarMismatch
	}
	if _, ok := s.applied[rec.incident.ID]; ok {
		return ErrAlreadyApplied
	}
	result, ok := s.results[raceID][carID]
	if !ok {
		return errNoRaceResult
	}

	seconds := p.Seconds
	if p.Warning {
		seconds = 0
	}
	result.PenaltyMillis += int64(seconds) * 1000
	s.applied[rec.incident.ID] = appliedPenalty{
		RaceID:  raceID,
		CarID:   carID,
		Seconds: seconds,
		Warning: p.Warning,
	}
	s.refreshSettlementLocked(raceID)
	return nil
}

// Appeal reviews one judged incident with fresh evidence.
func (s *RaceSteward) Appeal(incidentID, carID string, evidence Evidence) (Verdict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()

	rec, ok := s.incidents[incidentID]
	if !ok {
		return Verdict{}, ErrUnknownIncident
	}
	if carID != rec.incident.CarID {
		return Verdict{}, ErrCarMismatch
	}
	if !rec.judged {
		return Verdict{}, ErrNotJudged
	}
	if evidence.SubmittedAt.After(rec.incident.At.Add(s.rules.AppealWindow)) {
		return Verdict{}, ErrAppealWindowClosed
	}

	oldSeconds := rec.incident.PenaltySeconds
	newSeconds := oldSeconds
	upheld := s.validEvidence(evidence.Samples)
	if upheld {
		newSeconds = 0
		rec.incident.PenaltySeconds = 0
		rec.warning = false
		rec.incident.Status = StatusAppealed
		rec.penalty.Seconds = 0
		rec.penalty.Warning = false
		rec.penalty.Reason = rec.incident.Kind
	}

	verdict := Verdict{
		IncidentID: rec.incident.ID,
		CarID:      carID,
		Upheld:     upheld,
		OldSeconds: oldSeconds,
		NewSeconds: newSeconds,
		Delta:      newSeconds - oldSeconds,
	}
	if upheld {
		verdict.Message = "appeal upheld: penalty cancelled"
	} else {
		verdict.Message = "appeal denied: original penalty stands"
	}

	if applied, ok := s.applied[incidentID]; ok {
		s.changeAppliedPenaltyLocked(incidentID, applied, newSeconds, rec.warning)
	}
	return verdict, nil
}

// Standings returns the classification of one race.
func (s *RaceSteward) Standings(raceID string) ([]Standing, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.standingsLocked(raceID), nil
}

// ReportContact files a collision report and returns the merged incident.
func (s *RaceSteward) ReportContact(c Contact) (Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()

	if c.ReporterID == "" || c.OtherID == "" || c.ReporterID == c.OtherID ||
		math.IsNaN(c.YieldSpace) || math.IsInf(c.YieldSpace, 0) ||
		c.YieldSpace < 0 || c.YieldSpace > 1 || c.Severity < 0 {
		return Incident{}, ErrBadContact
	}

	collisionKey := contactCollisionKey(c)
	contactKey := ""
	if c.ContactID != "" {
		contactKey = "contact:" + c.ContactID
	}

	var existingID string
	if contactKey != "" {
		existingID = s.contacts[contactKey]
	}
	if collisionID := s.contacts[collisionKey]; collisionID != "" {
		if existingID != "" && existingID != collisionID {
			return Incident{}, ErrBadContact
		}
		existingID = collisionID
	}

	if existingID != "" {
		rec, ok := s.incidents[existingID]
		if !ok || rec.incident.Kind != IncidentContact ||
			!sameContactPair(rec.incident, c) || rec.incident.Lap != c.Lap {
			return Incident{}, ErrBadContact
		}
		if contactKey != "" {
			s.contacts[contactKey] = existingID
		}
		s.contacts[collisionKey] = existingID
		if !hasContactClaim(rec, c.ReporterID) {
			rec.claims = append(rec.claims, newContactClaim(c))
		}
		return rec.incident, nil
	}

	inc := Incident{
		Kind:           IncidentContact,
		CarID:          c.ReporterID,
		OtherID:        c.OtherID,
		Lap:            c.Lap,
		At:             c.At,
		Status:         StatusOpen,
		Responsibility: contactResponsibility(c.YieldSpace),
		PenaltySeconds: s.rules.ContactPenaltySeconds * c.Severity,
	}
	inc = s.addIncidentLocked(inc)
	rec := s.incidents[inc.ID]
	rec.claims = append(rec.claims, newContactClaim(c))
	if contactKey != "" {
		s.contacts[contactKey] = inc.ID
	}
	s.contacts[collisionKey] = inc.ID
	return inc, nil
}

// GiveBack records that a car handed the gained position back on the next
// corner, which downgrades a track limits penalty to a warning.
func (s *RaceSteward) GiveBack(incidentID string, evidence Evidence) (Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()

	rec, ok := s.incidents[incidentID]
	if !ok {
		return Incident{}, ErrUnknownIncident
	}
	if rec.incident.Kind != IncidentTrackLimits {
		return Incident{}, errGiveBackKind
	}
	if rec.givenBack {
		return rec.incident, nil
	}

	rec.givenBack = true
	rec.givenBackAt = evidence.SubmittedAt
	rec.warning = true
	rec.incident.PenaltySeconds = 0
	if rec.judged {
		rec.penalty.Seconds = 0
		rec.penalty.Warning = true
	}
	if applied, ok := s.applied[incidentID]; ok {
		s.changeAppliedPenaltyLocked(incidentID, applied, 0, true)
	}
	return rec.incident, nil
}

// Settle pays the prizes of a race once.
func (s *RaceSteward) Settle(raceID string) ([]Reward, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()

	if settlement, ok := s.settlements[raceID]; ok {
		rewards := s.rewardsLocked(raceID)
		if !sameRewards(settlement.Rewards, rewards) {
			s.settlements[raceID] = settlementState{Rewards: rewards}
		}
		return cloneRewards(rewards), nil
	}

	rewards := s.rewardsLocked(raceID)
	s.settlements[raceID] = settlementState{Rewards: cloneRewards(rewards)}
	return cloneRewards(rewards), nil
}

// Gaps lists the laps that could not be judged.
func (s *RaceSteward) Gaps() []Gap {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Gap(nil), s.gaps...)
}

// Incident reads one incident by id.
func (s *RaceSteward) Incident(incidentID string) (Incident, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.incidents[incidentID]
	if !ok {
		return Incident{}, false
	}
	return rec.incident, true
}

// Incidents lists every incident in creation order.
func (s *RaceSteward) Incidents() []Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Incident, 0, len(s.incidentOrder))
	for _, id := range s.incidentOrder {
		if rec, ok := s.incidents[id]; ok {
			out = append(out, rec.incident)
		}
	}
	return out
}

// RecordResult stores the raw time of one car in one race.
func (s *RaceSteward) RecordResult(raceID, carID string, rawMillis int64) error {
	if raceID == "" || carID == "" {
		return errBadRaceResult
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initLocked()

	rows := s.results[raceID]
	if rows == nil {
		rows = make(map[string]*resultState)
		s.results[raceID] = rows
	}
	if result, ok := rows[carID]; ok {
		result.RawMillis = rawMillis
	} else {
		rows[carID] = &resultState{RawMillis: rawMillis}
	}
	s.refreshSettlementLocked(raceID)
	return nil
}

// Rewards lists the prize lines once a race has been settled.
func (s *RaceSteward) Rewards(raceID string) ([]Reward, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	settlement, ok := s.settlements[raceID]
	if !ok {
		return []Reward{}, nil
	}
	return cloneRewards(settlement.Rewards), nil
}

// Snapshot serialises incidents, penalties and the classification.
func (s *RaceSteward) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nextIncident := s.nextIncident
	if nextIncident <= 0 {
		nextIncident = 1
	}
	state := snapshotState{
		NextIncident: nextIncident,
		Incidents:    make([]snapshotIncident, 0, len(s.incidentOrder)),
		Gaps:         append([]Gap(nil), s.gaps...),
		Results:      make(map[string]map[string]resultState, len(s.results)),
		Applied:      make(map[string]appliedPenalty, len(s.applied)),
		Contacts:     make(map[string]string, len(s.contacts)),
		Settlements:  make(map[string]settlementState, len(s.settlements)),
	}
	for _, id := range s.incidentOrder {
		rec, ok := s.incidents[id]
		if !ok {
			continue
		}
		state.Incidents = append(state.Incidents, snapshotIncident{
			Incident:    rec.incident,
			Warning:     rec.warning,
			Judged:      rec.judged,
			Penalty:     rec.penalty,
			GivenBack:   rec.givenBack,
			GivenBackAt: rec.givenBackAt,
			Claims:      append([]contactClaim(nil), rec.claims...),
		})
	}
	for raceID, rows := range s.results {
		state.Results[raceID] = make(map[string]resultState, len(rows))
		for carID, result := range rows {
			state.Results[raceID][carID] = *result
		}
	}
	for incidentID, applied := range s.applied {
		state.Applied[incidentID] = applied
	}
	for key, incidentID := range s.contacts {
		state.Contacts[key] = incidentID
	}
	for raceID, settlement := range s.settlements {
		state.Settlements[raceID] = settlementState{Rewards: cloneRewards(settlement.Rewards)}
	}

	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(stateJSON)
	envelope := snapshotEnvelope{
		Format:   snapshotFormat,
		Version:  snapshotVersion,
		State:    stateJSON,
		Checksum: hex.EncodeToString(checksum[:]),
	}
	return json.Marshal(envelope)
}

// Restore replaces the steward state with a snapshot, all or nothing.
func (s *RaceSteward) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ErrBadSnapshot
	}
	if envelope.Format != snapshotFormat || envelope.Version != snapshotVersion ||
		envelope.Checksum == "" || len(envelope.State) == 0 {
		return ErrBadSnapshot
	}
	checksum := sha256.Sum256(envelope.State)
	if !strings.EqualFold(envelope.Checksum, hex.EncodeToString(checksum[:])) {
		return ErrBadSnapshot
	}

	var state snapshotState
	decoder := json.NewDecoder(bytes.NewReader(envelope.State))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return ErrBadSnapshot
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrBadSnapshot
	}

	restored, err := buildRestoredState(state)
	if err != nil {
		return ErrBadSnapshot
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextIncident = restored.nextIncident
	s.incidentOrder = restored.incidentOrder
	s.incidents = restored.incidents
	s.gaps = restored.gaps
	s.results = restored.results
	s.applied = restored.applied
	s.contacts = restored.contacts
	s.settlements = restored.settlements
	return nil
}

func (s *RaceSteward) initLocked() {
	if s.nextIncident <= 0 {
		s.nextIncident = 1
	}
	if s.incidents == nil {
		s.incidents = make(map[string]*incidentRecord)
	}
	if s.results == nil {
		s.results = make(map[string]map[string]*resultState)
	}
	if s.applied == nil {
		s.applied = make(map[string]appliedPenalty)
	}
	if s.contacts == nil {
		s.contacts = make(map[string]string)
	}
	if s.settlements == nil {
		s.settlements = make(map[string]settlementState)
	}
}

func (s *RaceSteward) addIncidentLocked(incident Incident) Incident {
	for {
		incident.ID = fmt.Sprintf("inc-%d", s.nextIncident)
		s.nextIncident++
		if _, exists := s.incidents[incident.ID]; !exists {
			break
		}
	}
	s.incidents[incident.ID] = &incidentRecord{incident: incident}
	s.incidentOrder = append(s.incidentOrder, incident.ID)
	return incident
}

func (s *RaceSteward) standingsLocked(raceID string) []Standing {
	rowsByCar := s.results[raceID]
	_, settled := s.settlements[raceID]
	rows := make([]Standing, 0, len(rowsByCar))
	for carID, result := range rowsByCar {
		rows = append(rows, Standing{
			RaceID:        raceID,
			CarID:         carID,
			RawMillis:     result.RawMillis,
			PenaltyMillis: result.PenaltyMillis,
			TotalMillis:   result.RawMillis + result.PenaltyMillis,
			Provisional:   !settled,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TotalMillis != rows[j].TotalMillis {
			return rows[i].TotalMillis < rows[j].TotalMillis
		}
		if rows[i].RawMillis != rows[j].RawMillis {
			return rows[i].RawMillis < rows[j].RawMillis
		}
		return rows[i].CarID < rows[j].CarID
	})
	for i := range rows {
		rows[i].Position = i + 1
	}
	return rows
}

func (s *RaceSteward) rewardsLocked(raceID string) []Reward {
	points := [...]int{25, 18, 15}
	amounts := [...]int{100000, 60000, 40000}
	rows := s.standingsLocked(raceID)
	count := len(rows)
	if count > len(points) {
		count = len(points)
	}
	rewards := make([]Reward, 0, count)
	for i := 0; i < count; i++ {
		rewards = append(rewards, Reward{
			RaceID: raceID,
			CarID:  rows[i].CarID,
			Points: points[i],
			Amount: amounts[i],
		})
	}
	return rewards
}

func (s *RaceSteward) refreshSettlementLocked(raceID string) {
	if _, ok := s.settlements[raceID]; !ok {
		return
	}
	s.settlements[raceID] = settlementState{Rewards: s.rewardsLocked(raceID)}
}

func (s *RaceSteward) changeAppliedPenaltyLocked(incidentID string, applied appliedPenalty, seconds int, warning bool) {
	if result := s.results[applied.RaceID][applied.CarID]; result != nil {
		result.PenaltyMillis += int64(seconds-applied.Seconds) * 1000
	}
	applied.Seconds = seconds
	applied.Warning = warning
	s.applied[incidentID] = applied
	s.refreshSettlementLocked(applied.RaceID)
}

func (s *RaceSteward) validEvidence(samples []PoseSample) bool {
	if len(samples) < s.rules.MinSamples {
		return false
	}
	for _, sample := range samples {
		if math.IsNaN(sample.Offset) || math.IsInf(sample.Offset, 0) ||
			math.Abs(sample.Offset) > s.rules.HalfWidth {
			return false
		}
	}
	return true
}

func gapForSamples(carID string, samples []PoseSample, reason string) Gap {
	gap := Gap{CarID: carID, Reason: reason}
	if len(samples) > 0 {
		gap.Lap = samples[0].Lap
		gap.At = samples[0].At
	}
	return gap
}

func contactResponsibility(yieldSpace float64) int {
	responsibility := 100 - int(math.Round(yieldSpace*100))
	if responsibility < 0 {
		return 0
	}
	if responsibility > 100 {
		return 100
	}
	return responsibility
}

func newContactClaim(c Contact) contactClaim {
	return contactClaim{
		ReporterID:     c.ReporterID,
		OtherID:        c.OtherID,
		Responsibility: contactResponsibility(c.YieldSpace),
		Severity:       c.Severity,
		YieldSpace:     c.YieldSpace,
		At:             c.At,
	}
}

func hasContactClaim(rec *incidentRecord, reporterID string) bool {
	for _, claim := range rec.claims {
		if claim.ReporterID == reporterID {
			return true
		}
	}
	return false
}

func sameContactPair(incident Incident, c Contact) bool {
	return (incident.CarID == c.ReporterID && incident.OtherID == c.OtherID) ||
		(incident.CarID == c.OtherID && incident.OtherID == c.ReporterID)
}

func contactCollisionKey(c Contact) string {
	first, second := c.ReporterID, c.OtherID
	if first > second {
		first, second = second, first
	}
	return fmt.Sprintf("collision:%s:%s:%d:%d", first, second, c.Lap, c.At.UnixNano())
}

func cloneRewards(rewards []Reward) []Reward {
	return append([]Reward(nil), rewards...)
}

func sameRewards(a, b []Reward) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func buildRestoredState(state snapshotState) (*RaceSteward, error) {
	if state.NextIncident <= 0 {
		return nil, ErrBadSnapshot
	}
	restored := &RaceSteward{
		nextIncident:  state.NextIncident,
		incidentOrder: make([]string, 0, len(state.Incidents)),
		incidents:     make(map[string]*incidentRecord, len(state.Incidents)),
		gaps:          append([]Gap(nil), state.Gaps...),
		results:       make(map[string]map[string]*resultState, len(state.Results)),
		applied:       make(map[string]appliedPenalty, len(state.Applied)),
		contacts:      make(map[string]string, len(state.Contacts)),
		settlements:   make(map[string]settlementState, len(state.Settlements)),
	}

	for _, snapshot := range state.Incidents {
		if snapshot.ID == "" {
			return nil, ErrBadSnapshot
		}
		if _, exists := restored.incidents[snapshot.ID]; exists {
			return nil, ErrBadSnapshot
		}
		rec := &incidentRecord{
			incident:    snapshot.Incident,
			warning:     snapshot.Warning,
			judged:      snapshot.Judged,
			penalty:     snapshot.Penalty,
			givenBack:   snapshot.GivenBack,
			givenBackAt: snapshot.GivenBackAt,
			claims:      append([]contactClaim(nil), snapshot.Claims...),
		}
		restored.incidents[snapshot.ID] = rec
		restored.incidentOrder = append(restored.incidentOrder, snapshot.ID)
	}
	for raceID, rows := range state.Results {
		if raceID == "" {
			return nil, ErrBadSnapshot
		}
		restored.results[raceID] = make(map[string]*resultState, len(rows))
		for carID, result := range rows {
			if carID == "" {
				return nil, ErrBadSnapshot
			}
			copyResult := result
			restored.results[raceID][carID] = &copyResult
		}
	}
	for incidentID, applied := range state.Applied {
		rec, ok := restored.incidents[incidentID]
		if !ok || applied.RaceID == "" || applied.CarID == "" || applied.CarID != rec.incident.CarID {
			return nil, ErrBadSnapshot
		}
		if restored.results[applied.RaceID][applied.CarID] == nil {
			return nil, ErrBadSnapshot
		}
		restored.applied[incidentID] = applied
	}
	for key, incidentID := range state.Contacts {
		if key == "" || restored.incidents[incidentID] == nil {
			return nil, ErrBadSnapshot
		}
		restored.contacts[key] = incidentID
	}
	for raceID, settlement := range state.Settlements {
		if raceID == "" {
			return nil, ErrBadSnapshot
		}
		for _, reward := range settlement.Rewards {
			if reward.RaceID != raceID || reward.CarID == "" {
				return nil, ErrBadSnapshot
			}
		}
		restored.settlements[raceID] = settlementState{
			Rewards: cloneRewards(settlement.Rewards),
		}
	}
	return restored, nil
}
