package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Event types understood by the replayer.
const (
	EventExtend    = "EV_EXTEND"
	EventAction    = "EV_ACTION"
	EventNoAction  = "EV_NO_ACTION"
	EventSeparator = "EV_SEPARATOR"
)

// Bank names. A bank is one PCR algorithm family.
const (
	BankSHA1   = "sha1"
	BankSHA256 = "sha256"
	BankSHA384 = "sha384"
	BankSHA512 = "sha512"
)

// Comparison statuses reported per PCR.
const (
	StatusMatch        = "match"
	StatusMismatch     = "mismatch"
	StatusUndetermined = "undetermined"
)

// Overall verdicts of a comparison.
const (
	VerdictMatch        = "match"
	VerdictMismatch     = "mismatch"
	VerdictUndetermined = "undetermined"
)

// Replay errors.
var (
	ErrEmptyLog        = errors.New("event log is empty")
	ErrMalformedEvent  = errors.New("event record is malformed")
	ErrTruncatedLog    = errors.New("event log is truncated")
	ErrDigestLength    = errors.New("digest length does not match the declared algorithm")
	ErrLogTooLarge     = errors.New("event log exceeds the event cap")
	ErrEventTooLarge   = errors.New("event payload exceeds the size cap")
	ErrPCRRange        = errors.New("pcr index is out of range")
	ErrUnsupportedBank = errors.New("requested bank is not supported")
	ErrUnsupportedAlg  = errors.New("event algorithm is not supported")
	ErrSeparatorOrder  = errors.New("separator appears after an extension on the same pcr")
	ErrNoReplay        = errors.New("no replay result is cached")
	ErrUnknownPCR      = errors.New("pcr was never extended by the cached replay")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Event is one parsed record of the measured boot log.
type Event struct {
	Index  int    `json:"index"`
	PCR    int    `json:"pcr"`
	Type   string `json:"type"`
	Alg    string `json:"alg"`
	Digest []byte `json:"digest"`
	Data   []byte `json:"data"`
}

// EventLog is the parsed form of a measurement log.
type EventLog struct {
	Fingerprint string  `json:"fingerprint"`
	Events      []Event `json:"events"`
}

// PCRValue is one replayed PCR inside one bank.
type PCRValue struct {
	PCR   int    `json:"pcr"`
	Bank  string `json:"bank"`
	Value []byte `json:"value"`
}

// PCRCheck is the per PCR outcome of a comparison.
type PCRCheck struct {
	PCR            int    `json:"pcr"`
	Bank           string `json:"bank"`
	Status         string `json:"status"`
	FirstDivergent int    `json:"first_divergent"`
}

// Report is the comparison outcome against live PCR values.
type Report struct {
	Verdict string     `json:"verdict"`
	Checks  []PCRCheck `json:"checks"`
}

// TraceStep is one extension inside a PCR chain.
type TraceStep struct {
	Index  int    `json:"index"`
	Type   string `json:"type"`
	Before []byte `json:"before"`
	After  []byte `json:"after"`
}

// Trace explains how one PCR reached its replayed value.
type Trace struct {
	PCR   int         `json:"pcr"`
	Bank  string      `json:"bank"`
	Steps []TraceStep `json:"steps"`
}

// ReplayConfig bounds one replayer.
type ReplayConfig struct {
	MaxEvents     int
	MaxEventData  int
	MaxPCR        int
	SupportedAlgs []string
}

// EventLogReplayer parses, replays and compares measured boot logs.
type EventLogReplayer struct {
	cfg ReplayConfig
}

// NewEventLogReplayer builds a replayer with the given caps.
func NewEventLogReplayer(cfg ReplayConfig) *EventLogReplayer {
	return &EventLogReplayer{cfg: cfg}
}

// Parse turns the textual log into events and computes its fingerprint.
func (r *EventLogReplayer) Parse(log []byte) (EventLog, error) {
	return EventLog{}, errNotImplemented
}

// Replay extends the PCRs of one bank following the log order.
func (r *EventLogReplayer) Replay(log EventLog, bank string) ([]PCRValue, error) {
	return nil, errNotImplemented
}

// Compare grades the cached replay against live PCR values.
func (r *EventLogReplayer) Compare(live []PCRValue) (Report, error) {
	return Report{}, errNotImplemented
}

// Explain returns the extension chain of one PCR from the cached replay.
func (r *EventLogReplayer) Explain(pcr int) (Trace, error) {
	return Trace{}, errNotImplemented
}

// Events returns a copy of the events of the last parsed log, in log order.
func (r *EventLogReplayer) Events() []Event {
	return nil
}

// Banks lists the digest algorithms present in the last parsed log, sorted.
func (r *EventLogReplayer) Banks() []string {
	return nil
}

// Fingerprint returns the fingerprint of the last parsed log.
func (r *EventLogReplayer) Fingerprint() string {
	return ""
}

// CacheHits counts how many replays were served from the result cache.
func (r *EventLogReplayer) CacheHits() int {
	return 0
}

// Snapshot serialises the parsed log and the replay cache.
func (r *EventLogReplayer) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (r *EventLogReplayer) Restore(data []byte) error {
	return errNotImplemented
}
