// Package task registers the userfault handling ranges of sandboxed
// processes, routes page faults to the single owning handler and fails closed
// once that handler is gone.
package task

import (
	"errors"
	"time"
)

// errNotImplemented is returned by behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// DefaultPageSize is used when Config leaves PageSize at zero.
const DefaultPageSize uint64 = 4096

// Errors reported by the guard.
var (
	ErrInvalidHandler = errors.New("handler id is not usable")
	ErrHandlerExists  = errors.New("handler already registered")
	ErrUnknownHandler = errors.New("handler is not registered")
	ErrHandlerGone    = errors.New("handler is no longer alive")
	ErrInvalidRange   = errors.New("range is not usable")
	ErrUnalignedRange = errors.New("range bounds are not page aligned")
	ErrRangeOverlap   = errors.New("range overlaps another range of the same process")
	ErrRangeLimit     = errors.New("per process range limit reached")
	ErrUnknownRange   = errors.New("range is not registered")
	ErrNoHandler      = errors.New("no handler covers the faulting address")
	ErrHandlerMisses  = errors.New("handler exhausted the page retry budget")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Config bounds the guard.
type Config struct {
	PageSize            uint64
	MaxRangesPerProcess int
	HeartbeatTTL        time.Duration
	MaxHandlerMisses    int
	Now                 func() time.Time
}

// Registration asks for one handling range of a process.
type Registration struct {
	PID       int
	Start     uint64
	End       uint64
	HandlerID string
	Backup    string
}

// RangeInfo is the stored form of one registered range.
type RangeInfo struct {
	ID         uint64
	PID        int
	Start      uint64
	End        uint64
	HandlerID  string
	Backup     string
	Generation uint64
}

// Fault is one page fault reported by the kernel.
type Fault struct {
	PID  int
	Addr uint64
}

// Resolution is the handler chosen for one page fault.
type Resolution struct {
	RangeID    uint64
	PID        int
	Page       uint64
	HandlerID  string
	Backup     string
	Generation uint64
	Token      uint64
}

// ProcessStat is the accounting of one process.
type ProcessStat struct {
	Ranges      int
	Resolutions uint64
}

// UserfaultRangeGuard owns the handler table and the per process ranges.
type UserfaultRangeGuard struct {
	cfg Config
}

// NewUserfaultRangeGuard builds an empty guard.
func NewUserfaultRangeGuard(cfg Config) *UserfaultRangeGuard {
	return &UserfaultRangeGuard{cfg: cfg}
}

// RegisterHandler adds one live page fault handler.
func (g *UserfaultRangeGuard) RegisterHandler(id string) error {
	return errNotImplemented
}

// Heartbeat refreshes the liveness stamp of a handler.
func (g *UserfaultRangeGuard) Heartbeat(handlerID string, now time.Time) error {
	return errNotImplemented
}

// HandlerAlive reports whether a handler may still serve faults.
func (g *UserfaultRangeGuard) HandlerAlive(handlerID string) bool {
	return false
}

// Register stores one page aligned, non overlapping range of a process.
func (g *UserfaultRangeGuard) Register(reg Registration) (uint64, error) {
	return 0, errNotImplemented
}

// Unregister drops one whole range.
func (g *UserfaultRangeGuard) Unregister(id uint64) error {
	return errNotImplemented
}

// UnregisterRange drops the pages of a process inside a range.
func (g *UserfaultRangeGuard) UnregisterRange(pid int, start, end uint64) error {
	return errNotImplemented
}

// Handle routes one fault to the single owning handler.
func (g *UserfaultRangeGuard) Handle(fault Fault) (Resolution, error) {
	return Resolution{}, errNotImplemented
}

// ReportTimeout records one page level handler timeout.
func (g *UserfaultRangeGuard) ReportTimeout(handlerID string, page uint64) error {
	return errNotImplemented
}

// Fork creates the range table of a child process.
func (g *UserfaultRangeGuard) Fork(parentPID, childPID int) error {
	return errNotImplemented
}

// Ranges lists the ranges of one process ordered by start address.
func (g *UserfaultRangeGuard) Ranges(pid int) []RangeInfo {
	return nil
}

// Sweep removes handlers whose heartbeat expired and their ranges.
func (g *UserfaultRangeGuard) Sweep(now time.Time) []uint64 {
	return nil
}

// Stat reads the accounting of one process.
func (g *UserfaultRangeGuard) Stat(pid int) ProcessStat {
	return ProcessStat{}
}

// Snapshot serialises handlers, ranges, the generation and the token water
// marks.
func (g *UserfaultRangeGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *UserfaultRangeGuard) Restore(data []byte) error {
	return errNotImplemented
}
