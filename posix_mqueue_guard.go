package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// errNotImplemented is kept for source compatibility with the starter file.
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
	guardID  uint64
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

type queueKey struct {
	tenant string
	job    string
	name   string
}

type queueState struct {
	id            string
	tenant        string
	job           string
	name          string
	messages      []Message
	bytes         int
	notify        *NotifyRequest
	notifications int64
	open          int
	leaseUntil    time.Time
	leaseGen      uint64
	leaseTimer    *time.Timer
}

type handleState struct {
	queue    *queueState
	nonBlock bool
}

// PosixMqueueGuard owns the queue table, the message bodies and the counters.
type PosixMqueueGuard struct {
	cfg QueueConfig

	mu          sync.Mutex
	id          uint64
	queues      map[queueKey]*queueState
	nameOwners  map[string]string
	nameCounts  map[string]int
	handles     map[uint64]*handleState
	nextHandle  uint64
	nextQueueID uint64
	nextSeq     int64
	waitCh      chan struct{}
}

var guardIDCounter uint64

// NewPosixMqueueGuard builds a guard with the given caps.
func NewPosixMqueueGuard(cfg QueueConfig) *PosixMqueueGuard {
	return &PosixMqueueGuard{
		cfg:         cfg,
		id:          atomic.AddUint64(&guardIDCounter, 1),
		queues:      make(map[queueKey]*queueState),
		nameOwners:  make(map[string]string),
		nameCounts:  make(map[string]int),
		handles:     make(map[uint64]*handleState),
		nextQueueID: 1,
		nextSeq:     1,
		waitCh:      make(chan struct{}),
	}
}

// Open resolves a queue name for one tenant and job.
func (g *PosixMqueueGuard) Open(tenant, job, name string, flags OpenFlags) (Handle, error) {
	if !validIdentity(tenant) || !validIdentity(job) || !validIdentity(name) {
		return Handle{}, ErrInvalidName
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()
	g.purgeExpiredLocked(time.Now())

	key := queueKey{tenant: tenant, job: job, name: name}
	if q := g.queues[key]; q != nil {
		if q.tenant != tenant {
			return Handle{}, ErrCrossTenant
		}
		if flags&FlagExclusive != 0 {
			return Handle{}, ErrExists
		}
		if owner := g.nameOwners[name]; owner != "" && owner != tenant {
			return Handle{}, ErrCrossTenant
		}
		g.clearLeaseLocked(q)
		return g.openHandleLocked(q, flags), nil
	}

	if owner := g.nameOwners[name]; owner != "" && owner != tenant {
		return Handle{}, ErrCrossTenant
	}
	if flags&FlagCreate == 0 {
		return Handle{}, ErrNotFound
	}
	if g.queueLimitReachedLocked() {
		return Handle{}, ErrQueueLimit
	}

	q := &queueState{
		id:     g.newQueueIDLocked(),
		tenant: tenant,
		job:    job,
		name:   name,
	}
	g.queues[key] = q
	g.nameOwners[name] = tenant
	g.nameCounts[name]++
	return g.openHandleLocked(q, flags), nil
}

// Send appends one message at the given priority.
func (g *PosixMqueueGuard) Send(h Handle, msg Message, prio int) error {
	deadline := time.Time{}
	validated := false

	for {
		g.mu.Lock()
		g.ensureLocked()
		hs, err := g.handleLocked(h)
		if err != nil {
			g.mu.Unlock()
			return err
		}

		if !validated {
			if prio < MinPriority || prio > MaxPriority {
				g.mu.Unlock()
				return ErrInvalidPriority
			}
			if len(msg.Data) == 0 {
				g.mu.Unlock()
				return ErrInvalidMessage
			}
			if len(msg.Data) > g.cfg.MaxMessageBytes {
				g.mu.Unlock()
				return ErrMessageTooLarge
			}
			deadline = time.Now().Add(time.Duration(g.cfg.BlockTimeoutMs) * time.Millisecond)
			validated = true
		}

		q := hs.queue
		full := len(q.messages)+1 > g.cfg.MaxMessagesPerQueue ||
			q.bytes > g.cfg.MaxBytesPerQueue ||
			len(msg.Data) > g.cfg.MaxBytesPerQueue-q.bytes
		if full {
			if hs.nonBlock {
				g.mu.Unlock()
				return ErrWouldBlock
			}
			remaining := time.Until(deadline)
			if remaining <= 0 {
				g.mu.Unlock()
				return ErrTimeout
			}
			waitCh := g.waitCh
			g.mu.Unlock()

			timer := time.NewTimer(remaining)
			select {
			case <-waitCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-timer.C:
			}
			continue
		}

		queued := Message{
			Seq:      g.nextSeq,
			Priority: prio,
			Data:     append([]byte(nil), msg.Data...),
		}
		g.nextSeq++
		g.insertMessageLocked(q, queued)
		q.bytes += len(queued.Data)
		if q.notify != nil {
			q.notify = nil
			q.notifications++
		}
		g.broadcastLocked()
		g.mu.Unlock()
		return nil
	}
}

// Receive removes up to max messages in priority then arrival order.
func (g *PosixMqueueGuard) Receive(h Handle, max int) ([]Message, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()
	hs, err := g.handleLocked(h)
	if err != nil {
		return nil, err
	}
	if max <= 0 {
		return nil, ErrInvalidCount
	}

	q := hs.queue
	n := max
	if n > len(q.messages) {
		n = len(q.messages)
	}
	out := make([]Message, n)
	for i := 0; i < n; i++ {
		out[i] = Message{
			Seq:      q.messages[i].Seq,
			Priority: q.messages[i].Priority,
			Data:     append([]byte(nil), q.messages[i].Data...),
		}
		q.bytes -= len(q.messages[i].Data)
	}
	if n == len(q.messages) {
		q.messages = nil
	} else {
		q.messages = append([]Message(nil), q.messages[n:]...)
	}
	if n > 0 {
		g.broadcastLocked()
	}
	return out, nil
}

// Notify registers the single notification of a queue.
func (g *PosixMqueueGuard) Notify(h Handle, req NotifyRequest) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()
	hs, err := g.handleLocked(h)
	if err != nil {
		return err
	}
	reqCopy := req
	hs.queue.notify = &reqCopy
	return nil
}

// Close releases one descriptor.
func (g *PosixMqueueGuard) Close(h Handle) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()
	hs, err := g.handleLocked(h)
	if err != nil {
		return err
	}

	delete(g.handles, h.token)
	q := hs.queue
	q.open--
	if q.open == 0 && q.notify != nil {
		if g.cfg.NotifyLeaseMs > 0 {
			q.leaseGen++
			q.leaseUntil = time.Now().Add(time.Duration(g.cfg.NotifyLeaseMs) * time.Millisecond)
			g.armLeaseTimerLocked(q)
		} else {
			g.removeQueueLocked(q)
		}
	}
	g.broadcastLocked()
	return nil
}

// Stats reads the aggregated accounting of one job.
func (g *PosixMqueueGuard) Stats(tenant, job string) (QueueStats, error) {
	if !validIdentity(tenant) || !validIdentity(job) {
		return QueueStats{}, ErrInvalidName
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()
	g.purgeExpiredLocked(time.Now())

	stats := QueueStats{Tenant: tenant, Job: job}
	found := false
	for _, q := range g.queues {
		if q.tenant != tenant || q.job != job {
			continue
		}
		found = true
		stats.Queues++
		stats.Messages += len(q.messages)
		stats.Bytes += q.bytes
		if q.notify != nil {
			stats.NotifyPending++
		}
		stats.Notifications += q.notifications
	}
	if !found {
		return QueueStats{}, ErrNotFound
	}
	return stats, nil
}

// Pending counts the queued messages of one handle.
func (g *PosixMqueueGuard) Pending(h Handle) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()
	hs, err := g.handleLocked(h)
	if err != nil {
		return 0
	}
	return len(hs.queue.messages)
}

// Unlink drops a queue and its messages.
func (g *PosixMqueueGuard) Unlink(tenant, job string) error {
	if !validIdentity(tenant) || !validIdentity(job) {
		return ErrInvalidName
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()
	g.purgeExpiredLocked(time.Now())

	removed := false
	for key, q := range g.queues {
		if q.tenant == tenant && q.job == job {
			delete(g.queues, key)
			g.releaseQueueLocked(q)
			removed = true
		}
	}
	if !removed {
		return ErrNotFound
	}
	g.broadcastLocked()
	return nil
}

const snapshotVersion = 1

type snapshotFile struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

type snapshotState struct {
	NextQueueID uint64          `json:"next_queue_id"`
	NextSeq     int64           `json:"next_seq"`
	Queues      []snapshotQueue `json:"queues"`
}

type snapshotQueue struct {
	ID                 string         `json:"id"`
	Tenant             string         `json:"tenant"`
	Job                string         `json:"job"`
	Name               string         `json:"name"`
	Messages           []Message      `json:"messages"`
	Notify             *NotifyRequest `json:"notify,omitempty"`
	Notifications      int64          `json:"notifications"`
	LeaseUntilUnixNano int64          `json:"lease_until_unix_nano,omitempty"`
}

// Snapshot serialises the queue table, the messages and the notifications.
func (g *PosixMqueueGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()
	g.purgeExpiredLocked(time.Now())

	keys := make([]queueKey, 0, len(g.queues))
	for key := range g.queues {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].tenant != keys[j].tenant {
			return keys[i].tenant < keys[j].tenant
		}
		if keys[i].job != keys[j].job {
			return keys[i].job < keys[j].job
		}
		return keys[i].name < keys[j].name
	})

	state := snapshotState{
		NextQueueID: g.nextQueueID,
		NextSeq:     g.nextSeq,
		Queues:      make([]snapshotQueue, 0, len(keys)),
	}
	for _, key := range keys {
		q := g.queues[key]
		sq := snapshotQueue{
			ID:            q.id,
			Tenant:        q.tenant,
			Job:           q.job,
			Name:          q.name,
			Messages:      cloneMessages(q.messages),
			Notifications: q.notifications,
		}
		if q.notify != nil {
			notifyCopy := *q.notify
			sq.Notify = &notifyCopy
		}
		if q.open == 0 && !q.leaseUntil.IsZero() {
			sq.LeaseUntilUnixNano = q.leaseUntil.UnixNano()
		}
		state.Queues = append(state.Queues, sq)
	}

	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(stateBytes)
	file := snapshotFile{
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		State:    json.RawMessage(stateBytes),
	}
	return json.Marshal(file)
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *PosixMqueueGuard) Restore(data []byte) error {
	restored, err := decodeSnapshot(data, g.cfg)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()

	for _, q := range g.queues {
		if q.leaseTimer != nil {
			q.leaseTimer.Stop()
		}
		q.leaseGen++
	}
	g.queues = restored.queues
	g.nameOwners = restored.nameOwners
	g.nameCounts = restored.nameCounts
	g.handles = make(map[uint64]*handleState)
	g.nextQueueID = restored.nextQueueID
	g.nextSeq = restored.nextSeq

	now := time.Now()
	for _, q := range g.queues {
		if q.open == 0 && !q.leaseUntil.IsZero() {
			q.leaseGen++
			g.armLeaseTimerLocked(q)
		}
	}
	g.broadcastLocked()
	g.purgeExpiredLocked(now)
	return nil
}

type restoredState struct {
	queues      map[queueKey]*queueState
	nameOwners  map[string]string
	nameCounts  map[string]int
	nextQueueID uint64
	nextSeq     int64
}

func decodeSnapshot(data []byte, cfg QueueConfig) (*restoredState, error) {
	var file snapshotFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.Version != snapshotVersion || file.Checksum == "" || len(file.State) == 0 {
		return nil, ErrBadSnapshot
	}
	sum := sha256.Sum256(file.State)
	if file.Checksum != hex.EncodeToString(sum[:]) {
		return nil, ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(file.State, &state); err != nil {
		return nil, err
	}
	if state.NextQueueID == 0 || state.NextSeq <= 0 || len(state.Queues) > cfg.MaxQueues {
		return nil, ErrBadSnapshot
	}

	restored := &restoredState{
		queues:      make(map[queueKey]*queueState, len(state.Queues)),
		nameOwners:  make(map[string]string),
		nameCounts:  make(map[string]int),
		nextQueueID: state.NextQueueID,
		nextSeq:     state.NextSeq,
	}
	seenIDs := make(map[string]struct{}, len(state.Queues))
	seenSeq := make(map[int64]struct{})
	var maxSeq int64

	for _, sq := range state.Queues {
		if !validIdentity(sq.Tenant) || !validIdentity(sq.Job) || !validIdentity(sq.Name) || sq.ID == "" {
			return nil, ErrBadSnapshot
		}
		if _, ok := seenIDs[sq.ID]; ok {
			return nil, ErrBadSnapshot
		}
		seenIDs[sq.ID] = struct{}{}

		key := queueKey{tenant: sq.Tenant, job: sq.Job, name: sq.Name}
		if _, ok := restored.queues[key]; ok {
			return nil, ErrBadSnapshot
		}
		if owner, ok := restored.nameOwners[sq.Name]; ok && owner != sq.Tenant {
			return nil, ErrBadSnapshot
		}
		if len(sq.Messages) > cfg.MaxMessagesPerQueue || sq.Notifications < 0 {
			return nil, ErrBadSnapshot
		}

		q := &queueState{
			id:            sq.ID,
			tenant:        sq.Tenant,
			job:           sq.Job,
			name:          sq.Name,
			messages:      make([]Message, 0, len(sq.Messages)),
			notifications: sq.Notifications,
		}
		for _, msg := range sq.Messages {
			if msg.Seq <= 0 || msg.Priority < MinPriority || msg.Priority > MaxPriority ||
				len(msg.Data) == 0 || len(msg.Data) > cfg.MaxMessageBytes {
				return nil, ErrBadSnapshot
			}
			if _, ok := seenSeq[msg.Seq]; ok {
				return nil, ErrBadSnapshot
			}
			seenSeq[msg.Seq] = struct{}{}
			if msg.Seq > maxSeq {
				maxSeq = msg.Seq
			}
			q.bytes += len(msg.Data)
			if q.bytes > cfg.MaxBytesPerQueue {
				return nil, ErrBadSnapshot
			}
			q.messages = append(q.messages, Message{
				Seq:      msg.Seq,
				Priority: msg.Priority,
				Data:     append([]byte(nil), msg.Data...),
			})
		}
		sort.Slice(q.messages, func(i, j int) bool {
			if q.messages[i].Priority != q.messages[j].Priority {
				return q.messages[i].Priority > q.messages[j].Priority
			}
			return q.messages[i].Seq < q.messages[j].Seq
		})
		if sq.Notify != nil {
			notifyCopy := *sq.Notify
			q.notify = &notifyCopy
		}
		if sq.LeaseUntilUnixNano < 0 {
			return nil, ErrBadSnapshot
		}
		if sq.LeaseUntilUnixNano > 0 {
			if q.notify == nil {
				return nil, ErrBadSnapshot
			}
			q.leaseUntil = time.Unix(0, sq.LeaseUntilUnixNano)
		}

		restored.queues[key] = q
		restored.nameOwners[sq.Name] = sq.Tenant
		restored.nameCounts[sq.Name]++
	}
	if state.NextSeq <= maxSeq {
		return nil, ErrBadSnapshot
	}
	return restored, nil
}

func (g *PosixMqueueGuard) ensureLocked() {
	if g.id == 0 {
		g.id = atomic.AddUint64(&guardIDCounter, 1)
	}
	if g.queues == nil {
		g.queues = make(map[queueKey]*queueState)
	}
	if g.nameOwners == nil {
		g.nameOwners = make(map[string]string)
	}
	if g.nameCounts == nil {
		g.nameCounts = make(map[string]int)
	}
	if g.handles == nil {
		g.handles = make(map[uint64]*handleState)
	}
	if g.nextQueueID == 0 {
		g.nextQueueID = 1
	}
	if g.nextSeq <= 0 {
		g.nextSeq = 1
	}
	if g.waitCh == nil {
		g.waitCh = make(chan struct{})
	}
}

func (g *PosixMqueueGuard) openHandleLocked(q *queueState, flags OpenFlags) Handle {
	g.nextHandle++
	if g.nextHandle == 0 {
		g.nextHandle++
	}
	for g.handles[g.nextHandle] != nil {
		g.nextHandle++
		if g.nextHandle == 0 {
			g.nextHandle++
		}
	}
	token := g.nextHandle
	nonBlock := flags&FlagNonBlock != 0
	g.handles[token] = &handleState{queue: q, nonBlock: nonBlock}
	q.open++
	return Handle{
		ID:       q.id,
		Tenant:   q.tenant,
		Job:      q.job,
		Name:     q.name,
		NonBlock: nonBlock,
		token:    token,
		guardID:  g.id,
	}
}

func (g *PosixMqueueGuard) handleLocked(h Handle) (*handleState, error) {
	if h.guardID == 0 || h.guardID != g.id || h.token == 0 {
		return nil, ErrInvalidHandle
	}
	hs := g.handles[h.token]
	if hs == nil || hs.queue == nil {
		return nil, ErrInvalidHandle
	}
	q := hs.queue
	if h.ID != q.id || h.Tenant != q.tenant || h.Job != q.job || h.Name != q.name {
		return nil, ErrInvalidHandle
	}
	key := queueKey{tenant: q.tenant, job: q.job, name: q.name}
	if g.queues[key] != q {
		return nil, ErrInvalidHandle
	}
	return hs, nil
}

func (g *PosixMqueueGuard) insertMessageLocked(q *queueState, msg Message) {
	idx := sort.Search(len(q.messages), func(i int) bool {
		if q.messages[i].Priority != msg.Priority {
			return q.messages[i].Priority < msg.Priority
		}
		return q.messages[i].Seq > msg.Seq
	})
	q.messages = append(q.messages, Message{})
	copy(q.messages[idx+1:], q.messages[idx:])
	q.messages[idx] = msg
}

func (g *PosixMqueueGuard) queueLimitReachedLocked() bool {
	return len(g.queues) >= g.cfg.MaxQueues
}

func (g *PosixMqueueGuard) newQueueIDLocked() string {
	for {
		id := "q-" + strconv.FormatUint(g.nextQueueID, 10)
		g.nextQueueID++
		inUse := false
		for _, q := range g.queues {
			if q.id == id {
				inUse = true
				break
			}
		}
		if !inUse {
			return id
		}
	}
}

func (g *PosixMqueueGuard) clearLeaseLocked(q *queueState) {
	if q.leaseTimer != nil {
		q.leaseTimer.Stop()
		q.leaseTimer = nil
	}
	q.leaseUntil = time.Time{}
	q.leaseGen++
}

func (g *PosixMqueueGuard) armLeaseTimerLocked(q *queueState) {
	if q.leaseTimer != nil {
		q.leaseTimer.Stop()
	}
	key := queueKey{tenant: q.tenant, job: q.job, name: q.name}
	id := q.id
	gen := q.leaseGen
	delay := time.Until(q.leaseUntil)
	if delay < 0 {
		delay = 0
	}
	q.leaseTimer = time.AfterFunc(delay, func() {
		g.expireLease(key, id, gen)
	})
}

func (g *PosixMqueueGuard) expireLease(key queueKey, id string, gen uint64) {
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureLocked()

	q := g.queues[key]
	if q == nil || q.id != id || q.open != 0 || q.leaseGen != gen || q.leaseUntil.IsZero() {
		return
	}
	if now.Before(q.leaseUntil) {
		g.armLeaseTimerLocked(q)
		return
	}
	g.removeQueueLocked(q)
}

func (g *PosixMqueueGuard) purgeExpiredLocked(now time.Time) {
	for _, q := range g.queues {
		if q.open == 0 && !q.leaseUntil.IsZero() && !now.Before(q.leaseUntil) {
			g.removeQueueLocked(q)
		}
	}
}

func (g *PosixMqueueGuard) removeQueueLocked(q *queueState) {
	key := queueKey{tenant: q.tenant, job: q.job, name: q.name}
	if g.queues[key] != q {
		return
	}
	delete(g.queues, key)
	g.releaseQueueLocked(q)
	g.broadcastLocked()
}

func (g *PosixMqueueGuard) releaseQueueLocked(q *queueState) {
	if q.leaseTimer != nil {
		q.leaseTimer.Stop()
		q.leaseTimer = nil
	}
	q.leaseGen++
	q.leaseUntil = time.Time{}

	for token, hs := range g.handles {
		if hs.queue == q {
			delete(g.handles, token)
		}
	}
	count := g.nameCounts[q.name] - 1
	if count <= 0 {
		delete(g.nameCounts, q.name)
		if g.nameOwners[q.name] == q.tenant {
			delete(g.nameOwners, q.name)
		}
	} else {
		g.nameCounts[q.name] = count
	}
}

func (g *PosixMqueueGuard) broadcastLocked() {
	close(g.waitCh)
	g.waitCh = make(chan struct{})
}

func validIdentity(s string) bool {
	return strings.TrimSpace(s) != ""
}

func cloneMessages(in []Message) []Message {
	out := make([]Message, len(in))
	for i, msg := range in {
		out[i] = Message{
			Seq:      msg.Seq,
			Priority: msg.Priority,
			Data:     append([]byte(nil), msg.Data...),
		}
	}
	return out
}
