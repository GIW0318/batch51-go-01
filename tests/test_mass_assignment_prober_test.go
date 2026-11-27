package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-c-43"
)

func copyValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

type fakeStore struct {
	mu         sync.Mutex
	objects    map[string]map[string]string
	seq        int
	creates    int
	updates    int
	deletes    int
	reads      int
	failDelete bool
	persist    func(impl.Endpoint, map[string]string) map[string]string
	started    chan struct{}
	release    chan struct{}
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string]map[string]string{}}
}

func (s *fakeStore) Create(endpoint impl.Endpoint, values map[string]string) (string, map[string]string, error) {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creates++
	s.seq++
	id := fmt.Sprintf("obj-%d", s.seq)
	stored := copyValues(values)
	if s.persist != nil {
		stored = s.persist(endpoint, values)
	}
	s.objects[id] = stored
	return id, copyValues(values), nil
}

func (s *fakeStore) Update(endpoint impl.Endpoint, id string, values map[string]string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates++
	object, ok := s.objects[id]
	if !ok {
		return nil, fmt.Errorf("object %s not found", id)
	}
	for k, v := range values {
		object[k] = v
	}
	return copyValues(values), nil
}

func (s *fakeStore) Read(endpoint impl.Endpoint, id string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	object, ok := s.objects[id]
	if !ok {
		return nil, fmt.Errorf("object %s not found", id)
	}
	return copyValues(object), nil
}

func (s *fakeStore) Delete(endpoint impl.Endpoint, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes++
	if s.failDelete {
		return fmt.Errorf("delete refused for %s", id)
	}
	delete(s.objects, id)
	return nil
}

func (s *fakeStore) counts() (creates, updates, deletes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creates, s.updates, s.deletes
}

func (s *fakeStore) object(id string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyValues(s.objects[id])
}

func (s *fakeStore) liveObjects() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

func testCatalog() impl.Catalog {
	return impl.Catalog{Endpoints: []impl.EndpointSpec{
		{
			Endpoint: impl.Endpoint{ID: "ep-orders-create", Kind: impl.KindCreate, Resource: "orders"},
			Fields: []impl.Field{
				{Name: "amount", Type: impl.TypeInt},
				{Name: "note", Type: impl.TypeString},
			},
		},
		{
			Endpoint: impl.Endpoint{ID: "ep-orders-update", Kind: impl.KindUpdate, Resource: "orders", Baseline: "base-1"},
			Fields: []impl.Field{
				{Name: "amount", Type: impl.TypeInt},
				{Name: "note", Type: impl.TypeString, ReadOnly: true},
				{Name: "status", Type: impl.TypeEnum, Values: []string{"open", "closed"}},
			},
		},
		{
			Endpoint: impl.Endpoint{ID: "ep-users-create", Kind: impl.KindCreate, Resource: "users"},
			Fields: []impl.Field{
				{Name: "role", Type: impl.TypeString},
			},
		},
	}}
}

func createEndpoint() impl.Endpoint {
	return impl.Endpoint{ID: "ep-orders-create", Kind: impl.KindCreate, Resource: "orders"}
}

func updateEndpoint() impl.Endpoint {
	return impl.Endpoint{ID: "ep-orders-update", Kind: impl.KindUpdate, Resource: "orders", Baseline: "base-1"}
}

func fieldSet(endpoint string, fields ...impl.Field) impl.FieldSet {
	return impl.FieldSet{Endpoint: endpoint, Fields: fields}
}

func newProber(store impl.Store, maxProbes, batchSize int) *impl.MassAssignmentProber {
	return impl.NewMassAssignmentProber(store, testCatalog(), impl.Limits{MaxProbes: maxProbes, BatchSize: batchSize})
}

func TestSchemaMergesSiblingFields(t *testing.T) {
	p := newProber(newFakeStore(), 32, 2)
	got, err := p.Schema(createEndpoint())
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if got.Endpoint != "ep-orders-create" {
		t.Fatalf("field set endpoint = %q", got.Endpoint)
	}
	want := []string{"amount", "note", "status"}
	if len(got.Fields) != len(want) {
		t.Fatalf("merged fields = %+v, want the union %v", got.Fields, want)
	}
	for i, name := range want {
		if got.Fields[i].Name != name {
			t.Fatalf("merged fields = %+v, want the sorted union %v", got.Fields, want)
		}
	}
	own, err := p.Schema(updateEndpoint())
	if err != nil {
		t.Fatalf("schema update: %v", err)
	}
	for _, f := range own.Fields {
		if f.Name == "note" && !f.ReadOnly {
			t.Fatalf("the update endpoint declares note read only, the field set lost the flag")
		}
		if f.Name == "amount" && f.ReadOnly {
			t.Fatalf("amount must stay writable on the update endpoint")
		}
	}
	if len(p.Catalog().Endpoints) != 3 {
		t.Fatalf("catalog endpoints = %d, want 3", len(p.Catalog().Endpoints))
	}
}

func TestSchemaRejectsBadEndpoint(t *testing.T) {
	p := newProber(newFakeStore(), 32, 2)
	if _, err := p.Schema(impl.Endpoint{}); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("an endpoint without an id must fail with ErrBadEndpoint, got %v", err)
	}
	ghost := impl.Endpoint{ID: "ep-ghost", Kind: impl.KindCreate, Resource: "orders"}
	if _, err := p.Schema(ghost); !errors.Is(err, impl.ErrUnknownEndpoint) {
		t.Fatalf("an undeclared endpoint must fail with ErrUnknownEndpoint, got %v", err)
	}
	if _, err := p.Probe(ghost, fieldSet("ep-ghost", impl.Field{Name: "role", Type: impl.TypeString})); !errors.Is(err, impl.ErrUnknownEndpoint) {
		t.Fatalf("probing an undeclared endpoint must fail with ErrUnknownEndpoint, got %v", err)
	}
}

func TestProbeAttributesSingleFieldHits(t *testing.T) {
	store := newFakeStore()
	store.persist = func(endpoint impl.Endpoint, values map[string]string) map[string]string {
		out := map[string]string{}
		if v, ok := values["amount"]; ok {
			out["amount"] = v
		}
		return out
	}
	p := newProber(store, 8, 4)
	fields := []impl.Field{{Name: "amount", Type: impl.TypeInt}, {Name: "note", Type: impl.TypeString}}
	obs, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create", fields...))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("observations = %d, want one per field", len(obs))
	}
	if obs[0].Field != "amount" || obs[1].Field != "note" {
		t.Fatalf("observations are not ordered by field name: %+v", obs)
	}
	if !obs[0].Persisted || obs[0].ReadBack != p.Marker(fields[0]) {
		t.Fatalf("amount observation = %+v, want the marker read back", obs[0])
	}
	if obs[0].Sent != "424242" {
		t.Fatalf("integer marker = %q, want 424242", obs[0].Sent)
	}
	if obs[1].Persisted {
		t.Fatalf("note was dropped by the store and must not be reported as persisted: %+v", obs[1])
	}
	if !obs[1].Echoed {
		t.Fatalf("note was echoed by the response even though it was dropped on write")
	}
	if obs[1].Sent != "gsb-note-probe" {
		t.Fatalf("string marker = %q, want gsb-note-probe", obs[1].Sent)
	}
	if creates, _, _ := store.counts(); creates != 1 {
		t.Fatalf("store creates = %d, want one batched write", creates)
	}
}

func TestProbeBatchesLargeFieldSets(t *testing.T) {
	store := newFakeStore()
	store.persist = func(endpoint impl.Endpoint, values map[string]string) map[string]string {
		return map[string]string{}
	}
	p := newProber(store, 16, 2)
	fields := []impl.Field{
		{Name: "a", Type: impl.TypeString},
		{Name: "b", Type: impl.TypeString},
		{Name: "c", Type: impl.TypeString},
		{Name: "d", Type: impl.TypeString},
		{Name: "e", Type: impl.TypeString},
	}
	obs, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create", fields...))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(obs) != 5 {
		t.Fatalf("observations = %d, want 5", len(obs))
	}
	if creates, _, _ := store.counts(); creates != 3 {
		t.Fatalf("store creates = %d, want 3 batches of at most 2 fields", creates)
	}
	batched := 0
	for _, o := range obs {
		if o.Batched {
			batched++
		}
	}
	if batched != 4 {
		t.Fatalf("batched observations = %d, want 4 of 5", batched)
	}
	state, err := p.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if state.Batches != 3 || state.Probes != 3 {
		t.Fatalf("budget = %+v, want 3 batches and 3 probes", state)
	}
}

func TestProbeBisectsMultipleHits(t *testing.T) {
	store := newFakeStore()
	p := newProber(store, 16, 3)
	fields := []impl.Field{
		{Name: "role", Type: impl.TypeString},
		{Name: "tier", Type: impl.TypeString},
		{Name: "note", Type: impl.TypeString},
	}
	obs, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create", fields...))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(obs) != 3 {
		t.Fatalf("observations = %d, want 3 attributed fields", len(obs))
	}
	for _, o := range obs {
		if !o.Persisted || !o.Bisected {
			t.Fatalf("observation %+v, want every hit attributed by a single field probe", o)
		}
		if o.BatchID == "" {
			t.Fatalf("bisected observation lost its batch id")
		}
	}
	if creates, _, _ := store.counts(); creates != 4 {
		t.Fatalf("store creates = %d, want 1 batch plus 3 single field probes", creates)
	}
}

func TestProbeCleansUpCreatedObjects(t *testing.T) {
	store := newFakeStore()
	p := newProber(store, 16, 1)
	fields := []impl.Field{
		{Name: "role", Type: impl.TypeString},
		{Name: "tier", Type: impl.TypeString},
	}
	if _, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create", fields...)); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if live := store.liveObjects(); live != 0 {
		t.Fatalf("live objects after probing = %d, want every probe object deleted", live)
	}
	created, _, deletes := store.counts()
	_ = created
	ledger := p.Created()
	if len(ledger) != 2 {
		t.Fatalf("created ledger = %v, want the two probe objects", ledger)
	}
	if deletes != 2 {
		t.Fatalf("store deletes = %d, want 2", deletes)
	}
	if state, _ := p.Budget(); state.CleanupPending != 0 {
		t.Fatalf("cleanup pending = %d, want 0", state.CleanupPending)
	}
}

func TestProbeCleanupFailureMarked(t *testing.T) {
	store := newFakeStore()
	store.failDelete = true
	p := newProber(store, 8, 1)
	obs, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create", impl.Field{Name: "role", Type: impl.TypeString}))
	if err != nil {
		t.Fatalf("a failed cleanup must not fail the probe: %v", err)
	}
	if len(obs) != 1 || !obs[0].CleanupFailed {
		t.Fatalf("observation = %+v, want the cleanup failure recorded", obs)
	}
	pending := p.CleanupState()
	if len(pending) != 1 {
		t.Fatalf("cleanup state = %v, want the leaked object", pending)
	}
	if state, _ := p.Budget(); state.CleanupPending != 1 {
		t.Fatalf("cleanup pending = %d, want 1", state.CleanupPending)
	}
	findings, err := p.Diff(obs)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(findings) != 1 || !findings[0].CleanupNeeded {
		t.Fatalf("finding = %+v, want the cleanup flag propagated", findings)
	}
}

func TestProbeStopsAtBudget(t *testing.T) {
	store := newFakeStore()
	store.persist = func(endpoint impl.Endpoint, values map[string]string) map[string]string {
		return map[string]string{}
	}
	p := newProber(store, 1, 2)
	fields := []impl.Field{
		{Name: "a", Type: impl.TypeString},
		{Name: "b", Type: impl.TypeString},
		{Name: "c", Type: impl.TypeString},
		{Name: "d", Type: impl.TypeString},
	}
	if _, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create", fields...)); !errors.Is(err, impl.ErrProbeBudget) {
		t.Fatalf("the second batch must fail with ErrProbeBudget, got %v", err)
	}
	if creates, _, _ := store.counts(); creates != 1 {
		t.Fatalf("store creates = %d, want the single batch the budget allowed", creates)
	}
	state, err := p.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if state.Probes != 1 || state.Remaining != 0 {
		t.Fatalf("budget = %+v, want 1 used and 0 left", state)
	}
	if live := store.liveObjects(); live != 0 {
		t.Fatalf("live objects = %d, want the first batch deleted even when the second batch is refused", live)
	}
}

func TestProbeConcurrentSameEndpointBusy(t *testing.T) {
	store := newFakeStore()
	store.started = make(chan struct{}, 1)
	store.release = make(chan struct{})
	p := newProber(store, 8, 1)
	done := make(chan error, 1)
	go func() {
		_, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create", impl.Field{Name: "role", Type: impl.TypeString}))
		done <- err
	}()
	select {
	case <-store.started:
	case <-time.After(3 * time.Second):
		close(store.release)
		t.Fatalf("the first probe never reached the store")
	}
	if _, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create", impl.Field{Name: "tier", Type: impl.TypeString})); !errors.Is(err, impl.ErrBusy) {
		close(store.release)
		t.Fatalf("reentering the same endpoint must fail with ErrBusy, got %v", err)
	}
	close(store.release)
	store.release = nil
	if err := <-done; err != nil {
		t.Fatalf("first probe: %v", err)
	}
	store.started = nil
	if _, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create", impl.Field{Name: "tier", Type: impl.TypeString})); err != nil {
		t.Fatalf("the endpoint must be free again after the probe finished: %v", err)
	}
}

func TestProbeRejectsEmptyAndNilInputs(t *testing.T) {
	p := newProber(newFakeStore(), 8, 2)
	if _, err := p.Probe(createEndpoint(), impl.FieldSet{Endpoint: "ep-orders-create"}); !errors.Is(err, impl.ErrEmptyFieldSet) {
		t.Fatalf("an empty field set must fail with ErrEmptyFieldSet, got %v", err)
	}
	foreign := fieldSet("ep-users-create", impl.Field{Name: "role", Type: impl.TypeString})
	if _, err := p.Probe(createEndpoint(), foreign); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("a field set for another endpoint must fail with ErrBadEndpoint, got %v", err)
	}
	bare := impl.NewMassAssignmentProber(nil, testCatalog(), impl.Limits{MaxProbes: 4, BatchSize: 2})
	if _, err := bare.Probe(createEndpoint(), fieldSet("ep-orders-create", impl.Field{Name: "role", Type: impl.TypeString})); !errors.Is(err, impl.ErrNilStore) {
		t.Fatalf("a nil store must fail with ErrNilStore, got %v", err)
	}
}

func TestDiffGroupsAndSortsFindings(t *testing.T) {
	p := newProber(newFakeStore(), 8, 2)
	obs := []impl.FieldObservation{
		{Endpoint: "ep-users-create", Resource: "users", Field: "role", Persisted: false},
		{Endpoint: "ep-orders-create", Resource: "orders", Field: "note", Persisted: true},
		{Endpoint: "ep-orders-create", Resource: "orders", Field: "amount", Persisted: true},
	}
	findings, err := p.Diff(obs)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want one per endpoint", len(findings))
	}
	if findings[0].Endpoint != "ep-orders-create" || findings[1].Endpoint != "ep-users-create" {
		t.Fatalf("findings are not ordered by endpoint: %+v", findings)
	}
	if len(findings[0].Fields) != 2 || findings[0].Fields[0] != "amount" || findings[0].Fields[1] != "note" {
		t.Fatalf("finding fields = %v, want the sorted union", findings[0].Fields)
	}
	if len(findings[0].Persisted) != 2 || findings[0].Persisted[0] != "amount" {
		t.Fatalf("persisted fields = %v, want the sorted hits", findings[0].Persisted)
	}
	if len(findings[1].Persisted) != 0 {
		t.Fatalf("a field that was not persisted must not appear as a hit: %v", findings[1].Persisted)
	}
	if _, err := p.Diff(nil); !errors.Is(err, impl.ErrNoObservations) {
		t.Fatalf("diffing nothing must fail with ErrNoObservations, got %v", err)
	}
}

func TestDiffFlagsUnattributedBatchHits(t *testing.T) {
	p := newProber(newFakeStore(), 8, 3)
	obs := []impl.FieldObservation{
		{Endpoint: "ep-orders-create", Resource: "orders", Field: "note", Persisted: true, Batched: true, Bisected: false},
		{Endpoint: "ep-orders-create", Resource: "orders", Field: "amount", Persisted: true, Batched: true, Bisected: false},
		{Endpoint: "ep-users-create", Resource: "users", Field: "role", Persisted: true, Batched: false, Bisected: false},
	}
	findings, err := p.Diff(obs)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(findings))
	}
	if !findings[0].RequiresBisect {
		t.Fatalf("a batch with two unattributed hits must require a bisect: %+v", findings[0])
	}
	if findings[1].RequiresBisect {
		t.Fatalf("a single field probe never requires a bisect: %+v", findings[1])
	}
}

func TestJudgeSeverityLevels(t *testing.T) {
	p := newProber(newFakeStore(), 8, 2)
	cases := []struct {
		persisted []string
		confirmed bool
		severity  string
	}{
		{nil, false, impl.SeverityNone},
		{[]string{"role"}, true, impl.SeverityLow},
		{[]string{"role", "tier"}, true, impl.SeverityMedium},
		{[]string{"role", "tier", "note"}, true, impl.SeverityHigh},
	}
	for _, c := range cases {
		verdict, err := p.Judge(impl.FieldFinding{Endpoint: "ep-orders-create", Resource: "orders", Persisted: c.persisted})
		if err != nil {
			t.Fatalf("judge %v: %v", c.persisted, err)
		}
		if verdict.Confirmed != c.confirmed || verdict.Severity != c.severity {
			t.Fatalf("judge %v = %+v, want confirmed=%v severity=%q", c.persisted, verdict, c.confirmed, c.severity)
		}
		if !verdict.Conclusive {
			t.Fatalf("a fully attributed finding must be conclusive: %+v", verdict)
		}
	}
	unresolved, err := p.Judge(impl.FieldFinding{Endpoint: "ep-orders-create", Persisted: []string{"role"}, RequiresBisect: true})
	if err != nil {
		t.Fatalf("judge unresolved: %v", err)
	}
	if unresolved.Conclusive {
		t.Fatalf("a finding that still needs a bisect must not be conclusive")
	}
	if _, err := p.Judge(impl.FieldFinding{}); !errors.Is(err, impl.ErrBadFinding) {
		t.Fatalf("a finding without an endpoint must fail with ErrBadFinding, got %v", err)
	}
}

func TestProbeRestoresUpdateBaseline(t *testing.T) {
	store := newFakeStore()
	store.objects["base-1"] = map[string]string{"amount": "7", "note": "orig", "status": "open"}
	p := newProber(store, 8, 1)
	fields := []impl.Field{
		{Name: "amount", Type: impl.TypeInt},
		{Name: "note", Type: impl.TypeString},
	}
	obs, err := p.Probe(updateEndpoint(), fieldSet("ep-orders-update", fields...))
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("observations = %d, want 2", len(obs))
	}
	for _, o := range obs {
		if o.Object != "base-1" {
			t.Fatalf("update probe object = %q, want the declared baseline", o.Object)
		}
		if !o.Persisted {
			t.Fatalf("observation %+v, want the injected value persisted", o)
		}
	}
	baseline := store.object("base-1")
	want := map[string]string{"amount": "7", "note": "orig", "status": "open"}
	if len(baseline) != len(want) {
		t.Fatalf("baseline after probing = %v, want the original values restored", baseline)
	}
	for k, v := range want {
		if baseline[k] != v {
			t.Fatalf("baseline field %q = %q, want %q restored", k, baseline[k], v)
		}
	}
	if live := store.liveObjects(); live != 1 {
		t.Fatalf("live objects = %d, want only the baseline", live)
	}
}

func TestSnapshotRestoreKeepsLedgerAndBudget(t *testing.T) {
	store := newFakeStore()
	p := newProber(store, 8, 2)
	if _, err := p.Probe(createEndpoint(), fieldSet("ep-orders-create",
		impl.Field{Name: "amount", Type: impl.TypeInt},
		impl.Field{Name: "note", Type: impl.TypeString},
	)); err != nil {
		t.Fatalf("probe: %v", err)
	}
	before, err := p.Budget()
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	data, err := p.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewMassAssignmentProber(store, testCatalog(), impl.Limits{MaxProbes: 8, BatchSize: 2})
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after, err := restored.Budget()
	if err != nil {
		t.Fatalf("budget after restore: %v", err)
	}
	if after.Probes != before.Probes || after.Batches != before.Batches {
		t.Fatalf("restored budget = %+v, want the snapshot values %+v", after, before)
	}
	if len(restored.Created()) != len(p.Created()) || len(p.Created()) == 0 {
		t.Fatalf("restored object ledger = %v, want the %d probe objects", restored.Created(), len(p.Created()))
	}
	if err := restored.Restore([]byte("}broken{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	kept, err := restored.Budget()
	if err != nil || kept.Probes != before.Probes {
		t.Fatalf("a rejected restore must not touch live state, got %+v err=%v", kept, err)
	}
}
