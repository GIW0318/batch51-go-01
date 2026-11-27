package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-safety-c-09"
)

func queueConfig() impl.QueueConfig {
	return impl.QueueConfig{
		MaxQueues:           8,
		MaxMessagesPerQueue: 4,
		MaxBytesPerQueue:    256,
		MaxMessageBytes:     64,
		NotifyLeaseMs:       50,
		BlockTimeoutMs:      5,
	}
}

func payload(s string) impl.Message {
	return impl.Message{Data: []byte(s)}
}

func mustOpen(t *testing.T, g *impl.PosixMqueueGuard, tenant, job, name string, flags impl.OpenFlags) impl.Handle {
	t.Helper()
	h, err := g.Open(tenant, job, name, flags)
	if err != nil {
		t.Fatalf("open %s/%s/%s: %v", tenant, job, name, err)
	}
	return h
}

func TestOpenCreatesQueueAndReportsStats(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if h.ID == "" || h.Tenant != "tenant-a" || h.Job != "import" || h.Name != "jobs" {
		t.Fatalf("handle = %+v, want an id bound to tenant-a/import/jobs", h)
	}
	if got := g.Pending(h); got != 0 {
		t.Fatalf("fresh queue holds %d messages, want 0", got)
	}
	stats, err := g.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Queues != 1 || stats.Messages != 0 || stats.Bytes != 0 {
		t.Fatalf("fresh stats = %+v, want one empty queue", stats)
	}
	if stats.NotifyPending != 0 || stats.Notifications != 0 {
		t.Fatalf("fresh stats = %+v, want no notification activity", stats)
	}
	again := mustOpen(t, g, "tenant-a", "import", "jobs", 0)
	if again.ID != h.ID {
		t.Fatalf("reopening the same queue produced a different queue id")
	}
}

func TestOpenRejectsBlankIdentity(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	if _, err := g.Open("  ", "import", "jobs", impl.FlagCreate); !errors.Is(err, impl.ErrInvalidName) {
		t.Fatalf("blank tenant must fail with ErrInvalidName, got %v", err)
	}
	if _, err := g.Open("tenant-a", "", "jobs", impl.FlagCreate); !errors.Is(err, impl.ErrInvalidName) {
		t.Fatalf("blank job must fail with ErrInvalidName, got %v", err)
	}
	if _, err := g.Open("tenant-a", "import", "", impl.FlagCreate); !errors.Is(err, impl.ErrInvalidName) {
		t.Fatalf("blank queue name must fail with ErrInvalidName, got %v", err)
	}
	if _, err := g.Stats("tenant-a", "import"); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("stats for a rejected job must fail with ErrNotFound, got %v", err)
	}
}

func TestOpenWithoutCreateFailsForMissingQueue(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	if _, err := g.Open("tenant-a", "import", "jobs", 0); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("opening a missing queue without create must fail with ErrNotFound, got %v", err)
	}
	if _, err := g.Open("tenant-a", "import", "jobs", impl.FlagExclusive); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("exclusive open of a missing queue must fail with ErrNotFound, got %v", err)
	}
}

func TestOpenExclusiveRejectsExistingQueue(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if _, err := g.Open("tenant-a", "import", "jobs", impl.FlagCreate|impl.FlagExclusive); !errors.Is(err, impl.ErrExists) {
		t.Fatalf("exclusive create over an existing queue must fail with ErrExists, got %v", err)
	}
	stats, err := g.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Queues != 1 {
		t.Fatalf("queue count after a rejected exclusive open = %d, want 1", stats.Queues)
	}
}

func TestOpenRejectsCrossTenantNameReuse(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if _, err := g.Open("tenant-b", "import", "jobs", impl.FlagCreate); !errors.Is(err, impl.ErrCrossTenant) {
		t.Fatalf("another tenant reusing the queue name must fail with ErrCrossTenant, got %v", err)
	}
	stats, err := g.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("the owner job must stay readable, got %v", err)
	}
	if stats.Queues != 1 {
		t.Fatalf("owner job queue count = %d, want 1", stats.Queues)
	}
	if _, err := g.Stats("tenant-b", "import"); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("the rejected tenant must own no queue, got %v", err)
	}
}

func TestOpenEnforcesQueueLimit(t *testing.T) {
	cfg := queueConfig()
	cfg.MaxQueues = 2
	g := impl.NewPosixMqueueGuard(cfg)
	mustOpen(t, g, "tenant-a", "import", "q1", impl.FlagCreate)
	mustOpen(t, g, "tenant-a", "import", "q2", impl.FlagCreate)
	if _, err := g.Open("tenant-a", "import", "q3", impl.FlagCreate); !errors.Is(err, impl.ErrQueueLimit) {
		t.Fatalf("third queue must hit the cap with ErrQueueLimit, got %v", err)
	}
	if _, err := g.Open("tenant-a", "export", "q1", impl.FlagCreate); !errors.Is(err, impl.ErrQueueLimit) {
		t.Fatalf("a second job must not bypass the queue cap, got %v", err)
	}
	if _, err := g.Stats("tenant-a", "import"); err != nil {
		t.Fatalf("stats after the rejected opens: %v", err)
	}
}

func TestSendAndReceiveHonourPriorityAndFifo(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if err := g.Send(h, payload("low-1"), 1); err != nil {
		t.Fatalf("send low-1: %v", err)
	}
	if err := g.Send(h, payload("high"), 5); err != nil {
		t.Fatalf("send high: %v", err)
	}
	if err := g.Send(h, payload("low-2"), 1); err != nil {
		t.Fatalf("send low-2: %v", err)
	}
	got, err := g.Receive(h, 10)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("received %d messages, want 3", len(got))
	}
	want := []string{"high", "low-1", "low-2"}
	for i, w := range want {
		if string(got[i].Data) != w {
			t.Fatalf("message %d = %q, want %q (priority descending then fifo)", i, got[i].Data, w)
		}
	}
	if got := g.Pending(h); got != 0 {
		t.Fatalf("pending after draining = %d, want 0", got)
	}
}

func TestReceiveRespectsMaxAndRejectsInvalidCount(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	for i, prio := range []int{3, 7, 5} {
		if err := g.Send(h, payload(string(rune('a'+i))), prio); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	if _, err := g.Receive(h, 0); !errors.Is(err, impl.ErrInvalidCount) {
		t.Fatalf("receive with a zero count must fail with ErrInvalidCount, got %v", err)
	}
	if _, err := g.Receive(h, -2); !errors.Is(err, impl.ErrInvalidCount) {
		t.Fatalf("receive with a negative count must fail with ErrInvalidCount, got %v", err)
	}
	first, err := g.Receive(h, 2)
	if err != nil {
		t.Fatalf("receive two: %v", err)
	}
	if len(first) != 2 || string(first[0].Data) != "b" || string(first[1].Data) != "c" {
		t.Fatalf("first batch = %+v, want b then c", first)
	}
	if got := g.Pending(h); got != 1 {
		t.Fatalf("pending after a partial receive = %d, want 1", got)
	}
	rest, err := g.Receive(h, 9)
	if err != nil {
		t.Fatalf("receive rest: %v", err)
	}
	if len(rest) != 1 || string(rest[0].Data) != "a" {
		t.Fatalf("remaining batch = %+v, want a", rest)
	}
}

func TestSendEnforcesCountQuotaNonBlocking(t *testing.T) {
	cfg := queueConfig()
	cfg.MaxMessagesPerQueue = 2
	g := impl.NewPosixMqueueGuard(cfg)
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate|impl.FlagNonBlock)
	if err := g.Send(h, payload("one"), 1); err != nil {
		t.Fatalf("send one: %v", err)
	}
	if err := g.Send(h, payload("two"), 2); err != nil {
		t.Fatalf("send two: %v", err)
	}
	if err := g.Send(h, payload("three"), 3); !errors.Is(err, impl.ErrWouldBlock) {
		t.Fatalf("third message must be rejected with ErrWouldBlock, got %v", err)
	}
	if got := g.Pending(h); got != 2 {
		t.Fatalf("pending after the rejected send = %d, want 2", got)
	}
	got, err := g.Receive(h, 2)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(got) != 2 || string(got[0].Data) != "two" || string(got[1].Data) != "one" {
		t.Fatalf("queued messages = %+v, want two then one", got)
	}
}

func TestSendEnforcesByteQuotaWithTimeout(t *testing.T) {
	cfg := queueConfig()
	cfg.MaxBytesPerQueue = 8
	cfg.BlockTimeoutMs = 1
	g := impl.NewPosixMqueueGuard(cfg)
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if err := g.Send(h, payload("abcd"), 1); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := g.Send(h, payload("efgh"), 1); err != nil {
		t.Fatalf("second send: %v", err)
	}
	if err := g.Send(h, payload("ij"), 1); !errors.Is(err, impl.ErrTimeout) {
		t.Fatalf("blocking send past the byte cap must fail with ErrTimeout, got %v", err)
	}
	stats, err := g.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Bytes != 8 || stats.Messages != 2 {
		t.Fatalf("stats after the rejected send = %+v, want 2 messages of 8 bytes", stats)
	}
}

func TestCloseReleasesHandle(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if err := g.Send(h, payload("one"), 1); err != nil {
		t.Fatalf("send: %v", err)
	}
	other := impl.NewPosixMqueueGuard(queueConfig())
	if err := other.Send(h, payload("x"), 1); !errors.Is(err, impl.ErrInvalidHandle) {
		t.Fatalf("a handle must not work on another guard, got %v", err)
	}
	if err := g.Close(h); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := g.Send(h, payload("two"), 1); !errors.Is(err, impl.ErrInvalidHandle) {
		t.Fatalf("sending on a closed handle must fail with ErrInvalidHandle, got %v", err)
	}
	if err := g.Close(h); !errors.Is(err, impl.ErrInvalidHandle) {
		t.Fatalf("closing twice must fail with ErrInvalidHandle, got %v", err)
	}
	reopened := mustOpen(t, g, "tenant-a", "import", "jobs", 0)
	if got := g.Pending(reopened); got != 1 {
		t.Fatalf("queue content must survive a descriptor close, pending = %d want 1", got)
	}
}

func TestNotifyIsSingleShotAndReplaceable(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if err := g.Notify(h, impl.NotifyRequest{UserData: "worker-1"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	stats, err := g.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.NotifyPending != 1 || stats.Notifications != 0 {
		t.Fatalf("stats after registration = %+v, want one pending and no trigger", stats)
	}
	if err := g.Send(h, payload("one"), 1); err != nil {
		t.Fatalf("send one: %v", err)
	}
	stats, err = g.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("stats after trigger: %v", err)
	}
	if stats.NotifyPending != 0 || stats.Notifications != 1 {
		t.Fatalf("stats after trigger = %+v, want the registration consumed", stats)
	}
	if err := g.Send(h, payload("two"), 1); err != nil {
		t.Fatalf("send two: %v", err)
	}
	stats, err = g.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("stats after second send: %v", err)
	}
	if stats.Notifications != 1 {
		t.Fatalf("the notification fired %d times, want exactly once", stats.Notifications)
	}
	if err := g.Notify(h, impl.NotifyRequest{UserData: "worker-2"}); err != nil {
		t.Fatalf("second notify: %v", err)
	}
	if err := g.Send(h, payload("three"), 1); err != nil {
		t.Fatalf("send three: %v", err)
	}
	stats, err = g.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("stats after replacement trigger: %v", err)
	}
	if stats.Notifications != 2 || stats.NotifyPending != 0 {
		t.Fatalf("replaced registration stats = %+v, want two triggers and none pending", stats)
	}
}

func TestUnlinkDropsQueue(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if err := g.Send(h, payload("one"), 1); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := g.Unlink("tenant-a", "import"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if _, err := g.Stats("tenant-a", "import"); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("stats after unlink must fail with ErrNotFound, got %v", err)
	}
	if _, err := g.Open("tenant-a", "import", "jobs", 0); !errors.Is(err, impl.ErrNotFound) {
		t.Fatalf("opening an unlinked queue without create must fail with ErrNotFound, got %v", err)
	}
	fresh := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if got := g.Pending(fresh); got != 0 {
		t.Fatalf("recreated queue still holds %d messages, want 0", got)
	}
}

func TestSnapshotRestorePreservesQueuesAndOrder(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	for _, item := range []struct {
		body string
		prio int
	}{{"b", 5}, {"a", 1}, {"c", 3}} {
		if err := g.Send(h, payload(item.body), item.prio); err != nil {
			t.Fatalf("send %s: %v", item.body, err)
		}
	}
	if err := g.Notify(h, impl.NotifyRequest{UserData: "worker"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewPosixMqueueGuard(queueConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	stats, err := restored.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("restored stats: %v", err)
	}
	if stats.Messages != 3 || stats.Bytes != 3 || stats.NotifyPending != 1 {
		t.Fatalf("restored stats = %+v, want three messages, three bytes and a pending notification", stats)
	}
	reopened := mustOpen(t, restored, "tenant-a", "import", "jobs", 0)
	got, err := restored.Receive(reopened, 5)
	if err != nil {
		t.Fatalf("receive after restore: %v", err)
	}
	want := []string{"b", "c", "a"}
	if len(got) != len(want) {
		t.Fatalf("restored receive returned %d messages, want %d", len(got), len(want))
	}
	for i, w := range want {
		if string(got[i].Data) != w {
			t.Fatalf("restored message %d = %q, want %q", i, got[i].Data, w)
		}
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	g := impl.NewPosixMqueueGuard(queueConfig())
	h := mustOpen(t, g, "tenant-a", "import", "jobs", impl.FlagCreate)
	if err := g.Send(h, payload("one"), 2); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := g.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := g.Pending(h); got != 1 {
		t.Fatalf("rejected restore changed the queue, pending = %d want 1", got)
	}
	stats, err := g.Stats("tenant-a", "import")
	if err != nil {
		t.Fatalf("live stats after rejected restore: %v", err)
	}
	if stats.Messages != 1 || stats.Bytes != 3 {
		t.Fatalf("live stats after rejected restore = %+v, want the original message", stats)
	}
}
