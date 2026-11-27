package task

import "errors"

// errNotImplemented is returned by every behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

// OpenFlags controls how a queue name is resolved.
type OpenFlags int

// Open flag bits.
const (
	FlagCreate OpenFlags = 1 << iota
	FlagExclusive
	FlagNonBlock
)

// Priority bounds accepted by Send.
const (
	MinPriority = 0
	MaxPriority = 31
)

// Queue errors.
var (
	ErrInvalidName     = errors.New("tenant, job and queue name are required")
	ErrNotFound        = errors.New("queue does not exist")
	ErrExists          = errors.New("queue already exists")
	ErrCrossTenant     = errors.New("queue name is owned by another tenant")
	ErrQueueLimit      = errors.New("queue limit reached")
	ErrInvalidHandle   = errors.New("handle is not valid for this guard")
	ErrInvalidMessage  = errors.New("message payload is required")
	ErrInvalidPriority = errors.New("priority is out of range")
	ErrInvalidCount    = errors.New("receive count must be positive")
	ErrMessageTooLarge = errors.New("message payload exceeds the per message cap")
	ErrWouldBlock      = errors.New("queue quota reached and the handle is non blocking")
	ErrTimeout         = errors.New("queue quota reached and the blocking window elapsed")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Message is one queue element.
type Message struct {
	Seq      int64  `json:"seq"`
	Priority int    `json:"priority"`
	Data     []byte `json:"data"`
}

// Handle is an open descriptor of one queue.
type Handle struct {
	ID       string `json:"id"`
	Tenant   string `json:"tenant"`
	Job      string `json:"job"`
	Name     string `json:"name"`
	NonBlock bool   `json:"non_block"`
	token    uint64
}

// NotifyRequest registers the single notification of a queue.
type NotifyRequest struct {
	UserData string `json:"user_data"`
}

// QueueStats is the aggregated accounting of one job.
type QueueStats struct {
	Tenant        string `json:"tenant"`
	Job           string `json:"job"`
	Queues        int    `json:"queues"`
	Messages      int    `json:"messages"`
	Bytes         int    `json:"bytes"`
	NotifyPending int    `json:"notify_pending"`
	Notifications int64  `json:"notifications"`
}

// QueueConfig bounds one guard.
type QueueConfig struct {
	MaxQueues           int
	MaxMessagesPerQueue int
	MaxBytesPerQueue    int
	MaxMessageBytes     int
	NotifyLeaseMs       int
	BlockTimeoutMs      int
}

// PosixMqueueGuard owns the queue table, the message bodies and the counters.
type PosixMqueueGuard struct {
	cfg QueueConfig
}

// NewPosixMqueueGuard builds a guard with the given caps.
func NewPosixMqueueGuard(cfg QueueConfig) *PosixMqueueGuard {
	return &PosixMqueueGuard{cfg: cfg}
}

// Open resolves a queue name for one tenant and job.
func (g *PosixMqueueGuard) Open(tenant, job, name string, flags OpenFlags) (Handle, error) {
	return Handle{}, errNotImplemented
}

// Send appends one message at the given priority.
func (g *PosixMqueueGuard) Send(h Handle, msg Message, prio int) error {
	return errNotImplemented
}

// Receive removes up to max messages in priority then arrival order.
func (g *PosixMqueueGuard) Receive(h Handle, max int) ([]Message, error) {
	return nil, errNotImplemented
}

// Notify registers the single notification of a queue.
func (g *PosixMqueueGuard) Notify(h Handle, req NotifyRequest) error {
	return errNotImplemented
}

// Close releases one descriptor.
func (g *PosixMqueueGuard) Close(h Handle) error {
	return errNotImplemented
}

// Stats reads the aggregated accounting of one job.
func (g *PosixMqueueGuard) Stats(tenant, job string) (QueueStats, error) {
	return QueueStats{}, errNotImplemented
}

// Pending counts the queued messages of one handle.
func (g *PosixMqueueGuard) Pending(h Handle) int {
	return 0
}

// Unlink drops a queue and its messages.
func (g *PosixMqueueGuard) Unlink(tenant, job string) error {
	return errNotImplemented
}

// Snapshot serialises the queue table, the messages and the notifications.
func (g *PosixMqueueGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *PosixMqueueGuard) Restore(data []byte) error {
	return errNotImplemented
}
