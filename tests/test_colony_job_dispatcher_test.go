package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-game-a-15"
)

func colonyConfig() impl.ColonyConfig {
	return impl.ColonyConfig{
		MaxJobsPerTick: 4,
		ReserveTimeout: 10 * time.Minute,
		HungerLimit:    70,
		FatigueLimit:   70,
		HealthFloor:    20,
	}
}

func colonyStart() time.Time {
	return time.Date(2026, 7, 9, 8, 0, 0, 0, time.UTC)
}

func healthyResident(id string, skill, distance int) impl.Resident {
	return impl.Resident{ID: id, Skill: skill, Health: 100, Hunger: 10, Fatigue: 10, Distance: distance}
}

func addResident(t *testing.T, d *impl.ColonyJobDispatcher, r impl.Resident) {
	t.Helper()
	if err := d.AddResident(r); err != nil {
		t.Fatalf("add resident %s: %v", r.ID, err)
	}
}

func addJob(t *testing.T, d *impl.ColonyJobDispatcher, j impl.Job) {
	t.Helper()
	if err := d.AddJob(j); err != nil {
		t.Fatalf("add job %s: %v", j.ID, err)
	}
}

func eventsOfKind(events []impl.ColonyEvent, kind string) []impl.ColonyEvent {
	out := make([]impl.ColonyEvent, 0, len(events))
	for _, ev := range events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func TestAddResidentRejectsBlank(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	if err := d.AddResident(impl.Resident{ID: "  "}); !errors.Is(err, impl.ErrBadResident) {
		t.Fatalf("blank resident id = %v, want ErrBadResident", err)
	}
	addResident(t, d, healthyResident("r1", 4, 2))
	got, ok := d.Resident("r1")
	if !ok || got.Skill != 4 || got.Distance != 2 {
		t.Fatalf("resident = %+v, %v; want r1 with skill 4", got, ok)
	}
	if _, ok := d.Resident("  "); ok {
		t.Fatalf("rejected resident must not be registered")
	}
}

func TestAddResidentRejectsDuplicate(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 4, 2))
	if err := d.AddResident(healthyResident("r1", 9, 0)); !errors.Is(err, impl.ErrDuplicateResident) {
		t.Fatalf("duplicate resident = %v, want ErrDuplicateResident", err)
	}
	got, ok := d.Resident("r1")
	if !ok || got.Skill != 4 {
		t.Fatalf("resident after duplicate add = %+v, %v; want the original record", got, ok)
	}
}

func TestAddJobRejectsDuplicate(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addJob(t, d, impl.Job{ID: "j1", Priority: 2, MinSkill: 3, Slots: 1})
	if err := d.AddJob(impl.Job{ID: "j1", Priority: 5, MinSkill: 1, Slots: 9}); !errors.Is(err, impl.ErrDuplicateJob) {
		t.Fatalf("duplicate job = %v, want ErrDuplicateJob", err)
	}
	got, ok := d.Job("j1")
	if !ok || got.Priority != 2 || got.Slots != 1 {
		t.Fatalf("job after duplicate add = %+v, %v; want the original record", got, ok)
	}
}

func TestAssignPicksHighestScore(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 5, 5))
	addResident(t, d, healthyResident("r2", 3, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 2, MinSkill: 3, Slots: 2})
	got := d.Assign(colonyStart())
	if len(got) != 2 {
		t.Fatalf("assignments = %+v, want two", got)
	}
	if got[0].ResidentID != "r2" || got[0].JobID != "j1" || got[0].Score != 230 {
		t.Fatalf("first assignment = %+v, want r2 with score 230", got[0])
	}
	if got[1].ResidentID != "r1" || got[1].Score != 225 {
		t.Fatalf("second assignment = %+v, want r1 with score 225", got[1])
	}
	if got[0].Seq != 1 || got[1].Seq != 2 || got[0].State != impl.AssignmentActive {
		t.Fatalf("assignment sequencing = %+v, want active reservations 1 and 2", got)
	}
	current, ok := d.AssignmentOf("r2")
	if !ok || current.JobID != "j1" {
		t.Fatalf("live assignment of r2 = %+v, %v; want j1", current, ok)
	}
}

func TestAssignRespectsSkillRequirement(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r-low", 2, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 3, MinSkill: 5, Slots: 1})
	if got := d.Assign(colonyStart()); len(got) != 0 {
		t.Fatalf("low skill resident must not be assigned, got %+v", got)
	}
	if _, ok := d.AssignmentOf("r-low"); ok {
		t.Fatalf("low skill resident must stay free")
	}
	addResident(t, d, healthyResident("r-high", 6, 0))
	got := d.Assign(colonyStart().Add(time.Minute))
	if len(got) != 1 || got[0].ResidentID != "r-high" || got[0].JobID != "j1" {
		t.Fatalf("assignments = %+v, want r-high on j1", got)
	}
}

func TestAssignRespectsSlotLimit(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 5, 0))
	addResident(t, d, healthyResident("r2", 5, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 1, MinSkill: 1, Slots: 1})
	got := d.Assign(colonyStart())
	if len(got) != 1 {
		t.Fatalf("assignments = %+v, want a single slot booking", got)
	}
	if _, ok := d.AssignmentOf("r1"); !ok {
		t.Fatalf("the first resident must hold the only slot")
	}
	if _, ok := d.AssignmentOf("r2"); ok {
		t.Fatalf("the second resident must stay free when the station is full")
	}
}

func TestAssignRespectsTickBudget(t *testing.T) {
	cfg := colonyConfig()
	cfg.MaxJobsPerTick = 1
	d := impl.NewColonyJobDispatcher(cfg)
	addResident(t, d, healthyResident("r1", 5, 0))
	addResident(t, d, healthyResident("r2", 5, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 1, MinSkill: 1, Slots: 1})
	addJob(t, d, impl.Job{ID: "j2", Priority: 1, MinSkill: 1, Slots: 1})
	if got := d.Assign(colonyStart()); len(got) != 1 {
		t.Fatalf("assignments = %+v, want exactly one within the tick budget", got)
	}
}

func TestAssignSkipsUnfitResident(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	exhausted := healthyResident("r1", 5, 0)
	exhausted.Fatigue = 90
	addResident(t, d, exhausted)
	addJob(t, d, impl.Job{ID: "j1", Priority: 1, MinSkill: 1, Slots: 1})
	if got := d.Assign(colonyStart()); len(got) != 0 {
		t.Fatalf("exhausted resident must not be assigned, got %+v", got)
	}
	if err := d.SetCondition("r1", 100, 10, 10); err != nil {
		t.Fatalf("set condition: %v", err)
	}
	if got := d.Assign(colonyStart().Add(time.Minute)); len(got) != 1 || got[0].ResidentID != "r1" {
		t.Fatalf("assignments after rest = %+v, want r1 on j1", got)
	}
}

func TestTickPreemptsExhaustedResident(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 5, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 1, MinSkill: 1, Slots: 1})
	start := colonyStart()
	if got := d.Assign(start); len(got) != 1 {
		t.Fatalf("assign = %+v, want r1 on j1", got)
	}
	if err := d.SetCondition("r1", 100, 10, 95); err != nil {
		t.Fatalf("set condition: %v", err)
	}
	events := d.Tick(start.Add(time.Minute))
	preempted := eventsOfKind(events, impl.EventPreempted)
	if len(preempted) != 1 {
		t.Fatalf("tick events = %+v, want a single preemption", events)
	}
	if preempted[0].ResidentID != "r1" || preempted[0].JobID != "j1" || preempted[0].Reason != impl.ReasonFatigue {
		t.Fatalf("preemption event = %+v, want fatigue preemption of r1 from j1", preempted[0])
	}
	if reclaimed := eventsOfKind(events, impl.EventReclaimed); len(reclaimed) != 0 {
		t.Fatalf("unexpected reclaim events: %+v", reclaimed)
	}
	if _, ok := d.AssignmentOf("r1"); ok {
		t.Fatalf("preempted resident must have no reservation")
	}
	addResident(t, d, healthyResident("r2", 5, 0))
	if got := d.Assign(start.Add(2 * time.Minute)); len(got) != 1 || got[0].ResidentID != "r2" {
		t.Fatalf("assignments after preemption = %+v, want the slot reused by r2", got)
	}
}

func TestPreemptReleasesSlot(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 5, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 1, MinSkill: 1, Slots: 1})
	start := colonyStart()
	if got := d.Assign(start); len(got) != 1 {
		t.Fatalf("assign = %+v, want r1 on j1", got)
	}
	if err := d.Preempt("r1", "j1", "  "); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason = %v, want ErrBadReason", err)
	}
	if _, ok := d.AssignmentOf("r1"); !ok {
		t.Fatalf("rejected preemption must keep the reservation")
	}
	if err := d.Preempt("r1", "j1", "supervisor"); err != nil {
		t.Fatalf("preempt: %v", err)
	}
	if _, ok := d.AssignmentOf("r1"); ok {
		t.Fatalf("preempted resident must have no reservation")
	}
	if got := d.Assign(start.Add(time.Minute)); len(got) != 1 {
		t.Fatalf("reassign after preemption = %+v, want the slot to be reusable", got)
	}
}

func TestPreemptUnknownAssignment(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 5, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 1, MinSkill: 1, Slots: 1})
	if err := d.Preempt("r1", "j1", "supervisor"); !errors.Is(err, impl.ErrNotAssigned) {
		t.Fatalf("preempt without reservation = %v, want ErrNotAssigned", err)
	}
	start := colonyStart()
	if got := d.Assign(start); len(got) != 1 {
		t.Fatalf("assign = %+v, want r1 on j1", got)
	}
	if err := d.Preempt("r1", "j-other", "supervisor"); !errors.Is(err, impl.ErrNotAssigned) {
		t.Fatalf("preempt with the wrong job = %v, want ErrNotAssigned", err)
	}
	if err := d.Preempt("r1", "j1", "supervisor"); err != nil {
		t.Fatalf("preempt with the right job: %v", err)
	}
}

func TestCompleteAccumulatesOutput(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 5, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 1, MinSkill: 1, Slots: 1})
	start := colonyStart()
	if got := d.Assign(start); len(got) != 1 {
		t.Fatalf("assign = %+v, want r1 on j1", got)
	}
	if err := d.Complete("j1", "r1", 4); err != nil {
		t.Fatalf("complete: %v", err)
	}
	produced, err := d.Produced("j1")
	if err != nil || produced != 4 {
		t.Fatalf("produced = %d, %v; want 4", produced, err)
	}
	if _, ok := d.AssignmentOf("r1"); ok {
		t.Fatalf("completed reservation must be released")
	}
	if err := d.Complete("j1", "r1", 4); !errors.Is(err, impl.ErrNotAssigned) {
		t.Fatalf("duplicate complete = %v, want ErrNotAssigned", err)
	}
	if produced, err := d.Produced("j1"); err != nil || produced != 4 {
		t.Fatalf("produced after duplicate complete = %d, %v; want 4", produced, err)
	}
	if got := d.Assign(start.Add(time.Minute)); len(got) != 1 {
		t.Fatalf("reassign = %+v, want the slot to be reusable", got)
	}
	if err := d.Complete("j1", "r1", 3); err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if produced, err := d.Produced("j1"); err != nil || produced != 7 {
		t.Fatalf("produced after second batch = %d, %v; want 7", produced, err)
	}
}

func TestTickReclaimsExpiredReservation(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 5, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 1, MinSkill: 1, Slots: 1})
	start := colonyStart()
	if got := d.Assign(start); len(got) != 1 {
		t.Fatalf("assign = %+v, want r1 on j1", got)
	}
	events := d.Tick(start.Add(10*time.Minute + time.Second))
	reclaimed := eventsOfKind(events, impl.EventReclaimed)
	if len(reclaimed) != 1 {
		t.Fatalf("tick events = %+v, want a single reclaim", events)
	}
	if reclaimed[0].ResidentID != "r1" || reclaimed[0].JobID != "j1" || reclaimed[0].Reason != impl.ReasonTimeout {
		t.Fatalf("reclaim event = %+v, want reservation_timeout for r1 on j1", reclaimed[0])
	}
	if preempted := eventsOfKind(events, impl.EventPreempted); len(preempted) != 0 {
		t.Fatalf("unexpected preemption events: %+v", preempted)
	}
	if _, ok := d.AssignmentOf("r1"); ok {
		t.Fatalf("expired reservation must be released")
	}
	if got := d.Assign(start.Add(11 * time.Minute)); len(got) != 1 {
		t.Fatalf("assign after reclaim = %+v, want the slot to be reusable", got)
	}
}

func TestSnapshotRestoreKeepsProducedAndResidents(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 5, 2))
	addJob(t, d, impl.Job{ID: "j1", Priority: 3, MinSkill: 2, Slots: 1})
	start := colonyStart()
	if got := d.Assign(start); len(got) != 1 {
		t.Fatalf("assign = %+v, want r1 on j1", got)
	}
	if err := d.Complete("j1", "r1", 4); err != nil {
		t.Fatalf("complete: %v", err)
	}
	snap, err := d.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("snapshot = %d bytes, %v", len(snap), err)
	}
	restored := impl.NewColonyJobDispatcher(colonyConfig())
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	produced, err := restored.Produced("j1")
	if err != nil || produced != 4 {
		t.Fatalf("restored produced = %d, %v; want 4", produced, err)
	}
	resident, ok := restored.Resident("r1")
	if !ok || resident.Skill != 5 || resident.Distance != 2 {
		t.Fatalf("restored resident = %+v, %v; want r1 skill 5", resident, ok)
	}
	job, ok := restored.Job("j1")
	if !ok || job.Priority != 3 || job.Slots != 1 {
		t.Fatalf("restored job = %+v, %v; want j1 priority 3", job, ok)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	d := impl.NewColonyJobDispatcher(colonyConfig())
	addResident(t, d, healthyResident("r1", 5, 0))
	addJob(t, d, impl.Job{ID: "j1", Priority: 1, MinSkill: 1, Slots: 1})
	if got := d.Assign(colonyStart()); len(got) != 1 {
		t.Fatalf("assign = %+v, want r1 on j1", got)
	}
	if err := d.Complete("j1", "r1", 4); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := d.Restore([]byte("###broken###")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore = %v, want ErrBadSnapshot", err)
	}
	if produced, err := d.Produced("j1"); err != nil || produced != 4 {
		t.Fatalf("produced after failed restore = %d, %v; want 4", produced, err)
	}
	if _, ok := d.Resident("r1"); !ok {
		t.Fatalf("failed restore must keep the residents")
	}
}
