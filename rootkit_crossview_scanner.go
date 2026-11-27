// Package task compares two independent host object views, attributes the set
// difference to hiding and keeps the evidence across restarts.
package task

import "errors"

// errNotImplemented is returned by behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Object kinds enumerated by the scanner.
const (
	KindProcesses = "processes"
	KindModules   = "modules"
	KindListeners = "listeners"
)

// Attribution verdicts.
const (
	VerdictHiddenFromDirect = "hidden_from_direct"
	VerdictOnlyInDirect     = "only_in_direct"
)

// Errors returned by the scanner.
var (
	ErrUnknownKind      = errors.New("unknown object kind")
	ErrMissingCollector = errors.New("collector is not configured")
	ErrWindowExceeded   = errors.New("collection window exceeded")
	ErrUnknownItem      = errors.New("diff item is unknown")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Object is one item reported by a collector. Key is the stable identity of
// the object and Name is only a display hint.
type Object struct {
	Kind   string
	Key    string
	Name   string
	SeenAt int64
}

// Collector enumerates one view of the host. now is the wall clock of the
// current round and must be used for SeenAt bookkeeping.
type Collector interface {
	Collect(kind string, now int64) ([]Object, error)
}

// Collectors holds the direct system call view and the conventional view.
type Collectors struct {
	Direct       Collector
	Conventional Collector
}

// Policy bounds one round of enumeration.
type Policy struct {
	TimeWindow        int64
	MaxObjectsPerKind int
	MaxRecollect      int
	Now               func() int64
}

// View is the paired result of one enumeration round.
type View struct {
	Kind         string
	Direct       []Object
	Conventional []Object
	WindowStart  int64
	WindowEnd    int64
	Truncated    bool
	Recollects   int
}

// DiffItem is one object missing from exactly one view.
type DiffItem struct {
	Kind        string
	Key         string
	Name        string
	MissingFrom string
}

// DiffReport is the ordered set difference between the two views.
type DiffReport struct {
	Items        []DiffItem
	HiddenCount  int
	PhantomCount int
}

// Attribution grades one diff item.
type Attribution struct {
	Kind     string
	Key      string
	Verdict  string
	Severity int
}

// Finding is one attributed diff item kept by the scanner.
type Finding struct {
	Kind     string
	Key      string
	Name     string
	Verdict  string
	Severity int
	Seq      int64
}

// ScanResult summarises one full scan.
type ScanResult struct {
	Kinds         []string
	Diff          DiffReport
	NewFindings   int
	Indeterminate bool
}

// Stats counts enumeration and scan work.
type Stats struct {
	Enumerations  int
	Recollects    int
	Scans         int
	Indeterminate int
}

// RootkitCrossViewScanner owns the paired views, the findings and the counters.
type RootkitCrossViewScanner struct {
	collectors Collectors
	policy     Policy
}

// NewRootkitCrossViewScanner builds a scanner over the two collectors.
func NewRootkitCrossViewScanner(collectors Collectors, policy Policy) *RootkitCrossViewScanner {
	return &RootkitCrossViewScanner{collectors: collectors, policy: policy}
}

// Enumerate collects both views of one kind inside a bounded time window.
func (s *RootkitCrossViewScanner) Enumerate(kind string) (View, error) {
	return View{}, errNotImplemented
}

// Diff computes the set difference of a view.
func (s *RootkitCrossViewScanner) Diff(view View) (DiffReport, error) {
	return DiffReport{}, errNotImplemented
}

// Attribute explains which side is missing one object.
func (s *RootkitCrossViewScanner) Attribute(item DiffItem) (Attribution, error) {
	return Attribution{}, errNotImplemented
}

// Scan enumerates every kind, diffs the views and appends new findings.
func (s *RootkitCrossViewScanner) Scan() (ScanResult, error) {
	return ScanResult{}, errNotImplemented
}

// Findings returns a copy of the findings, oldest first.
func (s *RootkitCrossViewScanner) Findings() []Finding {
	return nil
}

// Snapshot serialises findings, sequence counter and statistics.
func (s *RootkitCrossViewScanner) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *RootkitCrossViewScanner) Restore(data []byte) error {
	return errNotImplemented
}

// Stats returns a copy of the counters.
func (s *RootkitCrossViewScanner) Stats() Stats {
	return Stats{}
}

// Reset drops every finding and counter.
func (s *RootkitCrossViewScanner) Reset() {
}
