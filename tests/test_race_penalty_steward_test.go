package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-game-a-14"
)

func raceRules() impl.RaceRules {
	return impl.RaceRules{
		HalfWidth:             6,
		CutTolerance:          3,
		MinSamples:            4,
		AppealWindow:          time.Hour,
		TrackPenaltySeconds:   3,
		ReversePenaltySeconds: 5,
		ContactPenaltySeconds: 2,
	}
}

func raceStart() time.Time {
	return time.Date(2026, 6, 7, 15, 0, 0, 0, time.UTC)
}

func lapSamples(start time.Time, offsets ...float64) []impl.PoseSample {
	out := make([]impl.PoseSample, 0, len(offsets))
	for i, offset := range offsets {
		out = append(out, impl.PoseSample{
			Seq:      i + 1,
			Lap:      1,
			At:       start.Add(time.Duration(i) * time.Second),
			Offset:   offset,
			Progress: float64(i) * 100,
		})
	}
	return out
}

func judgeTrackIncident(t *testing.T, s *impl.RaceSteward) impl.Incident {
	t.Helper()
	created := s.ObserveLap("car-1", lapSamples(raceStart(), 0, 7, 7, 7, 0))
	if len(created) != 1 {
		t.Fatalf("track limits lap = %+v, want one incident", created)
	}
	if _, err := s.Judge(created[0].ID); err != nil {
		t.Fatalf("judge incident: %v", err)
	}
	return created[0]
}

func TestObserveLapShortSamplesRecordsGap(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	start := raceStart()
	incidents := s.ObserveLap("car-1", lapSamples(start, 0, 0))
	if len(incidents) != 0 {
		t.Fatalf("sparse lap produced incidents: %+v", incidents)
	}
	gaps := s.Gaps()
	if len(gaps) != 1 || gaps[0].CarID != "car-1" || gaps[0].Reason != "short_lap" {
		t.Fatalf("gaps = %+v, want one short_lap gap for car-1", gaps)
	}
	s.ObserveLap("car-1", lapSamples(start.Add(time.Minute), 0, 0, 0, 0, 0))
	if got := len(s.Gaps()); got != 1 {
		t.Fatalf("clean lap added a gap, total %d", got)
	}
}

func TestObserveLapOutOfOrderRecordsGap(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	start := raceStart()
	samples := lapSamples(start, 0, 0, 0, 0, 0)
	samples[2].At = start.Add(-time.Minute)
	samples[2].Seq = 9
	if incidents := s.ObserveLap("car-1", samples); len(incidents) != 0 {
		t.Fatalf("out of order lap produced incidents: %+v", incidents)
	}
	gaps := s.Gaps()
	if len(gaps) != 1 || gaps[0].Reason != "out_of_order" {
		t.Fatalf("gaps = %+v, want one out_of_order gap", gaps)
	}
	s.ObserveLap("car-1", lapSamples(start.Add(time.Minute), 0, 0, 0, 0, 0))
	if got := len(s.Gaps()); got != 1 {
		t.Fatalf("ordered lap added a gap, total %d", got)
	}
}

func TestObserveLapFlagsOffTrack(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	incidents := s.ObserveLap("car-1", lapSamples(raceStart(), 0, 6.5, 6.5, 6.5, 0))
	if len(incidents) != 1 {
		t.Fatalf("off track lap = %+v, want exactly one incident", incidents)
	}
	inc := incidents[0]
	if inc.Kind != impl.IncidentTrackLimits {
		t.Fatalf("incident kind = %q, want %q", inc.Kind, impl.IncidentTrackLimits)
	}
	if inc.Status != impl.StatusOpen {
		t.Fatalf("incident status = %q, want %q", inc.Status, impl.StatusOpen)
	}
	if inc.PenaltySeconds != 3 {
		t.Fatalf("incident penalty = %d, want 3", inc.PenaltySeconds)
	}
	if inc.CarID != "car-1" || inc.ID == "" {
		t.Fatalf("incident = %+v, want a named incident of car-1", inc)
	}
}

func TestObserveLapAllowsTouchingBoundary(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	start := raceStart()
	if incidents := s.ObserveLap("car-1", lapSamples(start, 6, 6, 6, 6, 6)); len(incidents) != 0 {
		t.Fatalf("offset exactly on the white line must be legal, got %+v", incidents)
	}
	if gaps := s.Gaps(); len(gaps) != 0 {
		t.Fatalf("legal lap recorded gaps: %+v", gaps)
	}
	if incidents := s.ObserveLap("car-1", lapSamples(start.Add(time.Minute), 6.2, 6.2, 6.2, 0, 0)); len(incidents) != 1 {
		t.Fatalf("crossing the line must produce one incident, got %+v", incidents)
	}
}

func TestObserveLapIgnoresSingleExcursion(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	start := raceStart()
	if incidents := s.ObserveLap("car-1", lapSamples(start, 0, 7, 0, 7, 0)); len(incidents) != 0 {
		t.Fatalf("isolated excursions must stay below the tolerance, got %+v", incidents)
	}
	if incidents := s.ObserveLap("car-1", lapSamples(start.Add(time.Minute), 0, 7, 7, 7, 0)); len(incidents) != 1 {
		t.Fatalf("three consecutive excursions must be judged, got %+v", incidents)
	}
}

func TestObserveLapFlagsReverseProgress(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	samples := lapSamples(raceStart(), 0, 0, 0, 0, 0)
	for i := range samples {
		samples[i].Progress = 500 - float64(i*20)
	}
	incidents := s.ObserveLap("car-1", samples)
	if len(incidents) != 1 || incidents[0].Kind != impl.IncidentReverse {
		t.Fatalf("reverse lap = %+v, want one reverse incident", incidents)
	}
	if incidents[0].PenaltySeconds != 5 {
		t.Fatalf("reverse penalty = %d, want 5", incidents[0].PenaltySeconds)
	}
}

func TestJudgeUnknownIncident(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	if _, err := s.Judge("inc-missing"); !errors.Is(err, impl.ErrUnknownIncident) {
		t.Fatalf("unknown incident = %v, want ErrUnknownIncident", err)
	}
	inc := judgeTrackIncident(t, s)
	penalty, err := s.Judge(inc.ID)
	if err != nil || penalty.Seconds != 3 || penalty.CarID != "car-1" || penalty.Warning {
		t.Fatalf("judged penalty = %+v, %v; want three seconds for car-1", penalty, err)
	}
}

func TestJudgeIsIdempotent(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	inc := judgeTrackIncident(t, s)
	first, err := s.Judge(inc.ID)
	if err != nil {
		t.Fatalf("first judge: %v", err)
	}
	second, err := s.Judge(inc.ID)
	if err != nil {
		t.Fatalf("second judge: %v", err)
	}
	if first != second {
		t.Fatalf("repeated judge = %+v then %+v, want the same penalty", first, second)
	}
	stored, ok := s.Incident(inc.ID)
	if !ok || stored.Status != impl.StatusJudged {
		t.Fatalf("stored incident = %+v, %v; want status judged", stored, ok)
	}
}

func TestApplyRejectsDuplicateIncident(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	if err := s.RecordResult("race-1", "car-1", 60000); err != nil {
		t.Fatalf("record result: %v", err)
	}
	inc := judgeTrackIncident(t, s)
	penalty, err := s.Judge(inc.ID)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if err := s.Apply("race-1", "car-1", penalty); err != nil {
		t.Fatalf("apply: %v", err)
	}
	rows, err := s.Standings("race-1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("standings = %+v, %v", rows, err)
	}
	if rows[0].TotalMillis != 63000 || rows[0].PenaltyMillis != 3000 || rows[0].Position != 1 {
		t.Fatalf("standing = %+v, want 63000 with a 3000 penalty", rows[0])
	}
	if err := s.Apply("race-1", "car-1", penalty); !errors.Is(err, impl.ErrAlreadyApplied) {
		t.Fatalf("second apply = %v, want ErrAlreadyApplied", err)
	}
	rows, err = s.Standings("race-1")
	if err != nil || len(rows) != 1 || rows[0].TotalMillis != 63000 {
		t.Fatalf("standing after duplicate apply = %+v, %v; want 63000", rows, err)
	}
}

func TestReportContactSplitsBlame(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	start := raceStart()
	inc, err := s.ReportContact(impl.Contact{
		ContactID:  "contact-1",
		ReporterID: "car-1",
		OtherID:    "car-2",
		At:         start,
		Lap:        2,
		Severity:   2,
		YieldSpace: 0.25,
	})
	if err != nil {
		t.Fatalf("report contact: %v", err)
	}
	if inc.Kind != impl.IncidentContact || inc.CarID != "car-1" || inc.OtherID != "car-2" {
		t.Fatalf("contact incident = %+v, want a contact of car-1 against car-2", inc)
	}
	if inc.Responsibility != 75 {
		t.Fatalf("responsibility = %d, want 75 for a reporter who left 25 percent space", inc.Responsibility)
	}
	if inc.PenaltySeconds != 4 {
		t.Fatalf("contact penalty = %d, want 4", inc.PenaltySeconds)
	}
	penalty, err := s.Judge(inc.ID)
	if err != nil || penalty.Seconds != 4 {
		t.Fatalf("contact penalty = %+v, %v; want four seconds", penalty, err)
	}
}

func TestAppealWindowClosed(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	inc := judgeTrackIncident(t, s)
	start := raceStart()
	_, err := s.Appeal(inc.ID, "car-1", impl.Evidence{
		SubmittedAt: start.Add(2 * time.Hour),
		Samples:     lapSamples(start, 0, 0, 0, 0, 0),
	})
	if !errors.Is(err, impl.ErrAppealWindowClosed) {
		t.Fatalf("late appeal = %v, want ErrAppealWindowClosed", err)
	}
	stored, ok := s.Incident(inc.ID)
	if !ok || stored.Status != impl.StatusJudged || stored.PenaltySeconds != 3 {
		t.Fatalf("incident after rejected appeal = %+v, %v; want the judged state unchanged", stored, ok)
	}
}

func TestAppealUpheldCancelsPenalty(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	start := raceStart()
	if err := s.RecordResult("race-1", "car-1", 60000); err != nil {
		t.Fatalf("record result: %v", err)
	}
	inc := judgeTrackIncident(t, s)
	penalty, err := s.Judge(inc.ID)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if err := s.Apply("race-1", "car-1", penalty); err != nil {
		t.Fatalf("apply: %v", err)
	}
	verdict, err := s.Appeal(inc.ID, "car-1", impl.Evidence{
		SubmittedAt: start.Add(10 * time.Minute),
		Samples:     lapSamples(start, 0, 0, 0, 0, 0),
	})
	if err != nil {
		t.Fatalf("appeal: %v", err)
	}
	if !verdict.Upheld || verdict.OldSeconds != 3 || verdict.NewSeconds != 0 || verdict.Delta != -3 {
		t.Fatalf("verdict = %+v, want the three second penalty cancelled", verdict)
	}
	rows, err := s.Standings("race-1")
	if err != nil || len(rows) != 1 || rows[0].TotalMillis != 60000 || rows[0].PenaltyMillis != 0 {
		t.Fatalf("standing after upheld appeal = %+v, %v; want the raw time back", rows, err)
	}
}

func TestStandingsOrderedByTotalTime(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	start := raceStart()
	for _, row := range []struct {
		car string
		raw int64
	}{{"car-a", 61000}, {"car-b", 60000}, {"car-c", 62000}} {
		if err := s.RecordResult("race-1", row.car, row.raw); err != nil {
			t.Fatalf("record %s: %v", row.car, err)
		}
	}
	inc, err := s.ReportContact(impl.Contact{
		ContactID:  "contact-9",
		ReporterID: "car-b",
		OtherID:    "car-c",
		At:         start,
		Lap:        3,
		Severity:   2,
		YieldSpace: 0.5,
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	penalty, err := s.Judge(inc.ID)
	if err != nil {
		t.Fatalf("judge contact: %v", err)
	}
	if err := s.Apply("race-1", "car-b", penalty); err != nil {
		t.Fatalf("apply contact penalty: %v", err)
	}
	rows, err := s.Standings("race-1")
	if err != nil {
		t.Fatalf("standings: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("standings rows = %d, want 3", len(rows))
	}
	want := []struct {
		car   string
		total int64
		pos   int
	}{{"car-a", 61000, 1}, {"car-c", 62000, 2}, {"car-b", 64000, 3}}
	for i, w := range want {
		if rows[i].CarID != w.car || rows[i].TotalMillis != w.total || rows[i].Position != w.pos {
			t.Fatalf("row %d = %+v, want %s total %d position %d", i, rows[i], w.car, w.total, w.pos)
		}
	}
}

func TestSnapshotRestoreKeepsIncidentsAndStandings(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	if err := s.RecordResult("race-1", "car-1", 60000); err != nil {
		t.Fatalf("record result: %v", err)
	}
	inc := judgeTrackIncident(t, s)
	penalty, err := s.Judge(inc.ID)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if err := s.Apply("race-1", "car-1", penalty); err != nil {
		t.Fatalf("apply: %v", err)
	}
	snap, err := s.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot = %d bytes, %v", len(snap), err)
	}
	restored := impl.NewRaceSteward(raceRules())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Incidents()); got != 1 {
		t.Fatalf("restored incidents = %d, want 1", got)
	}
	rows, err := restored.Standings("race-1")
	if err != nil || len(rows) != 1 || rows[0].TotalMillis != 63000 {
		t.Fatalf("restored standings = %+v, %v; want 63000", rows, err)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	s := impl.NewRaceSteward(raceRules())
	judgeTrackIncident(t, s)
	if err := s.Restore([]byte("%%not-a-snapshot%%")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	if got := len(s.Incidents()); got != 1 {
		t.Fatalf("incidents after failed restore = %d, want 1", got)
	}
}
