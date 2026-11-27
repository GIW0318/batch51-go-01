// Package task admits custom ACPI tables (DSDT and SSDT) during image build
// and early boot: every table header is checked for length, signature, OEM ids
// and checksum, overlapping overrides resolve by origin priority, and a batch
// of tables is applied or rolled back as one unit.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Origins accepted by Register, ordered from lowest to highest priority.
const (
	OriginBuiltin     = "builtin"
	OriginInitrdEarly = "initrd_early"
	OriginInitrdLate  = "initrd_late"
)

// Origin priorities.
const (
	PriorityBuiltin     = 10
	PriorityInitrdEarly = 20
	PriorityInitrdLate  = 30
)

// ACPI table header size.
const TableHeaderSize = 36

// Errors returned by the guard.
var (
	ErrShortTable       = errors.New("acpi table is shorter than its header")
	ErrLengthMismatch   = errors.New("acpi table length field does not match the payload")
	ErrChecksumMismatch = errors.New("acpi table checksum does not add up to zero")
	ErrBadSignature     = errors.New("acpi table signature must be four uppercase letters")
	ErrTableTooLarge    = errors.New("acpi table exceeds the configured size cap")
	ErrBadOrigin        = errors.New("acpi table origin is unknown")
	ErrTableLimit       = errors.New("acpi table limit reached")
	ErrUnknownTable     = errors.New("acpi table is not registered")
	ErrUnknownSignature = errors.New("no table with that signature is registered")
	ErrEmptyOrder       = errors.New("apply order is empty")
	ErrDsdtNeedsSsdt    = errors.New("dsdt override needs a companion ssdt")
	ErrLayerLimit       = errors.New("too many override layers for one signature")
	ErrActiveTable      = errors.New("table is active and cannot be removed")
	ErrNoRollback       = errors.New("no applied batch to roll back")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// OverridePolicy bounds the registered tables and their sizes.
type OverridePolicy struct {
	MaxTables            int
	MaxTableBytes        int
	MaxLayers            int
	RequireDsdtCompanion bool
}

// TableID identifies one registered table.
type TableID string

// Table is the metadata parsed from one ACPI table header.
type Table struct {
	ID            TableID
	Signature     string
	OEMID         string
	OEMTableID    string
	Origin        string
	Priority      int
	Length        uint32
	Revision      uint8
	SelfContained bool
	Seq           int64
}

// OverrideStats counts the registered and active tables.
type OverrideStats struct {
	Tables    int
	Active    int
	Applies   int64
	Rollbacks int64
	Rejected  int64
}

// AcpiOverrideGuard owns registered tables and the active override set.
type AcpiOverrideGuard struct {
	cfg OverridePolicy
}

// NewAcpiOverrideGuard builds a guard with the given policy.
func NewAcpiOverrideGuard(cfg OverridePolicy) *AcpiOverrideGuard {
	return &AcpiOverrideGuard{cfg: cfg}
}

// Register validates one table and keeps it for later resolution.
func (g *AcpiOverrideGuard) Register(table []byte, origin string) (TableID, error) {
	return "", errNotImplemented
}

// Validate checks the header, length, signature and checksum of one table.
func (g *AcpiOverrideGuard) Validate(table []byte) error {
	return errNotImplemented
}

// Resolve lists the registered tables of one signature, highest priority first.
func (g *AcpiOverrideGuard) Resolve(sig string) ([]Table, error) {
	return nil, errNotImplemented
}

// Apply activates one batch of tables atomically.
func (g *AcpiOverrideGuard) Apply(order []TableID) error {
	return errNotImplemented
}

// Rollback restores the override set that was active before the last Apply.
func (g *AcpiOverrideGuard) Rollback() error {
	return errNotImplemented
}

// Active reports the currently effective table of one signature.
func (g *AcpiOverrideGuard) Active(sig string) (Table, bool) {
	return Table{}, false
}

// Tables lists every registered table, oldest first.
func (g *AcpiOverrideGuard) Tables() []Table {
	return nil
}

// Deregister removes one inactive table.
func (g *AcpiOverrideGuard) Deregister(id TableID) error {
	return errNotImplemented
}

// Layers reports how many active tables carry one signature.
func (g *AcpiOverrideGuard) Layers(sig string) int {
	return 0
}

// SetSelfContained marks one DSDT as carrying its own companion tables.
func (g *AcpiOverrideGuard) SetSelfContained(id TableID, selfContained bool) error {
	return errNotImplemented
}

// Stats reads the guard counters.
func (g *AcpiOverrideGuard) Stats() OverrideStats {
	return OverrideStats{}
}

// Snapshot serialises the registered tables and the active set.
func (g *AcpiOverrideGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *AcpiOverrideGuard) Restore(data []byte) error {
	return errNotImplemented
}
