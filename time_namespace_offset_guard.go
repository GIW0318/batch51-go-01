// Package task keeps the realtime and boottime offsets of many time
// namespaces and performs the conversions that must agree on one generation.
package task

import "errors"

// errNotImplemented is returned by behaviour that still has to be written.
var errNotImplemented = errors.New("not implemented")

// InitialGeneration is the generation a freshly created namespace carries.
const InitialGeneration int64 = 1

// MaxSlewPPM bounds the slew rate a caller may ask for.
const MaxSlewPPM int64 = 1000

// Errors reported by the guard.
var (
	ErrInvalidNamespace = errors.New("namespace name is not usable")
	ErrNamespaceExists  = errors.New("namespace already registered")
	ErrNamespaceLimit   = errors.New("namespace limit reached")
	ErrUnknownHandle    = errors.New("unknown namespace handle")
	ErrStaleHandle      = errors.New("namespace handle is stale")
	ErrOffsetRange      = errors.New("offset is outside the accepted range")
	ErrInvalidStamp     = errors.New("guest timestamp is not usable")
	ErrBadSnapshot      = errors.New("snapshot payload is invalid")
)

// Handle identifies one live registration of a time namespace. The token
// changes every time the namespace is registered again.
type Handle struct {
	Namespace string
	Token     uint64
}

// TimeOffset is the pair of offsets applied to a namespace clock plus the
// slew rate the runtime asked for. Times are nanoseconds.
type TimeOffset struct {
	RealtimeNanos int64
	BoottimeNanos int64
	SlewPPM       int64
}

// HostTime is one sample of the host clocks, read in a single call.
type HostTime struct {
	RealtimeNanos int64
	BoottimeNanos int64
}

// TimeSnapshot is the namespace view of one host sample.
type TimeSnapshot struct {
	Namespace      string
	Generation     int64
	RealtimeNanos  int64
	BoottimeNanos  int64
	RealtimeOffset int64
	BoottimeOffset int64
}

// Deadline is a guest deadline translated onto the host boottime clock.
type Deadline struct {
	Namespace         string
	Generation        int64
	HostBoottimeNanos int64
	RemainingNanos    int64
	Expired           bool
}

// NamespaceInfo is the registry view of one namespace.
type NamespaceInfo struct {
	Namespace      string
	Generation     int64
	RealtimeOffset int64
	BoottimeOffset int64
	Live           bool
}

// GuardConfig carries the host clock source and the hard bounds.
type GuardConfig struct {
	HostClock      func() HostTime
	MaxNamespaces  int
	MaxOffsetNanos int64
}

// TimeNamespaceOffsetGuard owns the namespace registry and the generation
// counters.
type TimeNamespaceOffsetGuard struct {
	cfg GuardConfig
}

// NewTimeNamespaceOffsetGuard builds a guard around the given config.
func NewTimeNamespaceOffsetGuard(cfg GuardConfig) *TimeNamespaceOffsetGuard {
	return &TimeNamespaceOffsetGuard{cfg: cfg}
}

// CreateNamespace registers a namespace and hands back a handle.
func (g *TimeNamespaceOffsetGuard) CreateNamespace(namespace string) (Handle, error) {
	return Handle{}, errNotImplemented
}

// Handle looks up the handle of a live namespace.
func (g *TimeNamespaceOffsetGuard) Handle(namespace string) (Handle, error) {
	return Handle{}, errNotImplemented
}

// ApplyOffset swaps in a new offset pair and returns the new generation.
func (g *TimeNamespaceOffsetGuard) ApplyOffset(h Handle, off TimeOffset) (int64, error) {
	return 0, errNotImplemented
}

// Now reads the namespace clock as one generation consistent triple.
func (g *TimeNamespaceOffsetGuard) Now(h Handle) (TimeSnapshot, error) {
	return TimeSnapshot{}, errNotImplemented
}

// TranslateDeadline converts a guest realtime deadline onto the host clock.
func (g *TimeNamespaceOffsetGuard) TranslateDeadline(h Handle, guestDeadline int64) (Deadline, error) {
	return Deadline{}, errNotImplemented
}

// Resolve converts a guest realtime timestamp back onto the host clock.
func (g *TimeNamespaceOffsetGuard) Resolve(h Handle, guestStamp int64) (int64, error) {
	return 0, errNotImplemented
}

// Generation reports the offset generation a handle currently sees.
func (g *TimeNamespaceOffsetGuard) Generation(h Handle) (int64, error) {
	return 0, errNotImplemented
}

// Release drops a namespace registration and invalidates its handle.
func (g *TimeNamespaceOffsetGuard) Release(h Handle) error {
	return errNotImplemented
}

// Namespaces lists the live namespaces ordered by name.
func (g *TimeNamespaceOffsetGuard) Namespaces() []NamespaceInfo {
	return nil
}

// Snapshot serialises the registry, the offsets and the generation counters.
func (g *TimeNamespaceOffsetGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole registry with a snapshot, all or nothing.
func (g *TimeNamespaceOffsetGuard) Restore(data []byte) error {
	return errNotImplemented
}
