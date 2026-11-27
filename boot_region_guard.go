// Package task protects the boot region of block devices: the MBR, the GPT
// primary and backup headers, the partition entry arrays and the reserved
// prefix. Writes into those areas are refused unless a one shot ticket was
// issued for a partition table change.
package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// Layout schemes reported by Inspect.
const (
	SchemeMBR = "mbr"
	SchemeGPT = "gpt"
)

// Region kinds reported inside a Layout.
const (
	RegionMBR          = "mbr"
	RegionGPTHeader    = "gpt_primary_header"
	RegionGPTPartTable = "gpt_primary_table"
	RegionGPTBackup    = "gpt_backup"
	RegionData         = "data"
)

// Decision reasons attached to an Authorize answer.
const (
	ReasonTicketIssued   = "ticket_issued"
	ReasonProtected      = "protected_region"
	ReasonDataWrite      = "data_write"
	ReasonTicketRejected = "ticket_rejected"
)

// Ticket states reported by Tickets.
const (
	TicketIssued  = "issued"
	TicketUsed    = "used"
	TicketRevoked = "revoked"
)

// Sector and reserve defaults.
const (
	DefaultSectorSize   = 512
	DefaultReserveBytes = 1 << 20
)

// Errors returned by the guard.
var (
	ErrBadDevice        = errors.New("device name is required")
	ErrUnknownDevice    = errors.New("device is not registered")
	ErrDeviceTooSmall   = errors.New("device image is too small for a partition table")
	ErrBadMBR           = errors.New("mbr signature is missing")
	ErrProtectedWrite   = errors.New("write falls into a protected boot region")
	ErrApprovalRequired = errors.New("write requires a one time approval ticket")
	ErrBadReason        = errors.New("write reason is required")
	ErrTicketUnknown    = errors.New("approval ticket is unknown")
	ErrTicketUsed       = errors.New("approval ticket was already used")
	ErrTicketMismatch   = errors.New("approval ticket does not cover the write")
	ErrTicketLimit      = errors.New("too many pending approval tickets")
	ErrOutOfRange       = errors.New("write is outside the device")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
	ErrSnapshotUnknown  = errors.New("evidence snapshot is unknown")
	ErrEvidenceLimit    = errors.New("evidence snapshot limit reached")
	ErrQueueFull        = errors.New("device write queue is full")
)

// GuardConfig bounds the live tables of one guard.
type GuardConfig struct {
	SectorSize    int
	ReserveBytes  int64
	MaxTickets    int
	MaxEvidence   int
	MaxWriteQueue int
}

// Region is one protected or usable byte range of a device.
type Region struct {
	Name  string
	Kind  string
	Start int64
	End   int64
}

// Layout is the boot region footprint rebuilt on take over.
type Layout struct {
	Device       string
	Size         int64
	Scheme       string
	SectorSize   int
	ReserveBytes int64
	PrimaryValid bool
	BackupValid  bool
	MBRCRC       uint32
	PrimaryCRC   uint32
	BackupCRC    uint32
	Fingerprint  string
	HalfWrite    bool
	Regions      []Region
}

// WriteRequest asks permission for one range write.
type WriteRequest struct {
	Offset int64
	Data   []byte
	Reason string
}

// Decision is the explainable answer to one Authorize call.
type Decision struct {
	Allowed   bool
	Ticket    string
	Reason    string
	Offset    int64
	Length    int
	Protected []string
}

// SnapshotID names one evidence copy kept for a device.
type SnapshotID string

// Evidence is a copy of a boot region kept before a repair.
type Evidence struct {
	ID     SnapshotID
	Device string
	Region string
	Bytes  int
	Reason string
	Seq    int64
}

// TicketRecord is one issued approval ticket.
type TicketRecord struct {
	ID     string
	Offset int64
	Length int
	State  string
	Reason string
	Seq    int64
}

// RepairReport describes a repair of the GPT primary or backup header.
type RepairReport struct {
	Repaired bool
	Source   string
	Region   string
	Bytes    int
}

// GuardStats counts the traffic of one device.
type GuardStats struct {
	Writes      int64
	Rejected    int64
	Repairs     int64
	UsedTickets int64
	Pending     int
}

// BootRegionGuard owns layouts, tickets, evidence and counters per device.
type BootRegionGuard struct {
	cfg GuardConfig
}

// NewBootRegionGuard builds a guard with the given caps.
func NewBootRegionGuard(cfg GuardConfig) *BootRegionGuard {
	return &BootRegionGuard{cfg: cfg}
}

// RegisterDevice takes over one device image and rebuilds its layout.
func (g *BootRegionGuard) RegisterDevice(dev string, image []byte) error {
	return errNotImplemented
}

// Inspect rebuilds and returns the layout of a registered device.
func (g *BootRegionGuard) Inspect(dev string) (Layout, error) {
	return Layout{}, errNotImplemented
}

// Regions lists the protected and usable ranges of a device, by start offset.
func (g *BootRegionGuard) Regions(dev string) []Region {
	return nil
}

// Authorize issues a one shot ticket for a partition table change.
func (g *BootRegionGuard) Authorize(dev string, req WriteRequest) (Decision, error) {
	return Decision{}, errNotImplemented
}

// Write applies one range write, consuming a matching ticket when needed.
func (g *BootRegionGuard) Write(dev string, off int64, data []byte) error {
	return errNotImplemented
}

// Read returns the current bytes of one range.
func (g *BootRegionGuard) Read(dev string, off int64, n int) ([]byte, error) {
	return nil, errNotImplemented
}

// RestoreRegion rewrites a protected region from an evidence copy.
func (g *BootRegionGuard) RestoreRegion(dev string, snap SnapshotID) error {
	return errNotImplemented
}

// Repair reconciles the primary and backup GPT headers.
func (g *BootRegionGuard) Repair(dev string) (RepairReport, error) {
	return RepairReport{}, errNotImplemented
}

// Evidence lists the retained evidence copies of a device, oldest first.
func (g *BootRegionGuard) Evidence(dev string) []Evidence {
	return nil
}

// Tickets lists the tickets issued for a device, oldest first.
func (g *BootRegionGuard) Tickets(dev string) []TicketRecord {
	return nil
}

// RevokeTickets voids every unused ticket of a device.
func (g *BootRegionGuard) RevokeTickets(dev string) error {
	return errNotImplemented
}

// Stats reads the counters of one device.
func (g *BootRegionGuard) Stats(dev string) GuardStats {
	return GuardStats{}
}

// Fingerprint returns the layout fingerprint of one device.
func (g *BootRegionGuard) Fingerprint(dev string) string {
	return ""
}

// Snapshot serialises every device layout, ticket and evidence copy.
func (g *BootRegionGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *BootRegionGuard) Restore(data []byte) error {
	return errNotImplemented
}
