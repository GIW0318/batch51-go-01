package task

import (
	"errors"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-b-02"
)

func ospfPolicy() impl.AreaPolicy {
	return impl.AreaPolicy{
		RouterID:            "10.255.255.1",
		AreaID:              "0.0.0.0",
		BackboneArea:        "0.0.0.0",
		AuthKey:             "secret-1",
		HelloInterval:       10,
		DeadInterval:        40,
		MaxLSAsPerArea:      8,
		MaxNeighborsPerArea: 4,
		MaxFloodQueue:       4,
		MaxAge:              time.Hour,
		IsABR:               false,
	}
}

func ospfHello() impl.HelloPacket {
	return impl.HelloPacket{
		AreaID:        "0.0.0.0",
		RouterID:      "10.0.0.2",
		Auth:          "secret-1",
		HelloInterval: 10,
		DeadInterval:  40,
		Priority:      1,
	}
}

func ospfLSA(seq uint32, body string, priority int) impl.LSA {
	return impl.LSA{
		Type:              1,
		LinkStateID:       "10.0.0.0",
		AdvertisingRouter: "10.0.0.2",
		Seq:               seq,
		Age:               0,
		Body:              body,
		Checksum:          "sum-" + body,
		Priority:          priority,
	}
}

func lsdbHasBody(lsas []impl.LSA, body string) bool {
	for _, lsa := range lsas {
		if lsa.Body == body {
			return true
		}
	}
	return false
}

func TestHelloFirstContactIsInit(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	n, err := g.Hello("eth0", ospfHello())
	if err != nil {
		t.Fatalf("first hello must be admitted, got %v", err)
	}
	if n.State != impl.NeighborInit {
		t.Fatalf("neighbour state = %q, want %q", n.State, impl.NeighborInit)
	}
	if n.TwoWay {
		t.Fatalf("two way flag must still be false on the first hello")
	}
	if n.Iface != "eth0" || n.RouterID != "10.0.0.2" {
		t.Fatalf("neighbour identity = %s/%s, want eth0/10.0.0.2", n.Iface, n.RouterID)
	}
}

func TestHelloSecondContactBecomesTwoWay(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	if _, err := g.Hello("eth0", ospfHello()); err != nil {
		t.Fatalf("first hello failed: %v", err)
	}
	pkt := ospfHello()
	pkt.Neighbors = []string{"10.255.255.1"}
	n, err := g.Hello("eth0", pkt)
	if err != nil {
		t.Fatalf("second hello failed: %v", err)
	}
	if n.State != impl.NeighborTwoWay {
		t.Fatalf("neighbour state = %q, want %q", n.State, impl.NeighborTwoWay)
	}
	if !n.TwoWay {
		t.Fatalf("mutual visibility must set the two way flag")
	}
}

func TestHelloThirdContactBecomesFull(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	if _, err := g.Hello("eth0", ospfHello()); err != nil {
		t.Fatalf("first hello failed: %v", err)
	}
	pkt := ospfHello()
	pkt.Neighbors = []string{"10.255.255.1"}
	if _, err := g.Hello("eth0", pkt); err != nil {
		t.Fatalf("second hello failed: %v", err)
	}
	n, err := g.Hello("eth0", pkt)
	if err != nil {
		t.Fatalf("third hello failed: %v", err)
	}
	if n.State != impl.NeighborFull {
		t.Fatalf("neighbour state = %q, want %q", n.State, impl.NeighborFull)
	}
	if got := g.Stats().Neighbors; got != 1 {
		t.Fatalf("neighbour count = %d, want 1", got)
	}
}

func TestHelloRejectsAreaMismatch(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	pkt := ospfHello()
	pkt.AreaID = "0.0.0.1"
	if _, err := g.Hello("eth0", pkt); !errors.Is(err, impl.ErrAreaMismatch) {
		t.Fatalf("area mismatch error = %v, want ErrAreaMismatch", err)
	}
}

func TestHelloRejectsBadAuth(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	pkt := ospfHello()
	pkt.Auth = "wrong-key"
	if _, err := g.Hello("eth0", pkt); !errors.Is(err, impl.ErrAuthFailed) {
		t.Fatalf("auth error = %v, want ErrAuthFailed", err)
	}
}

func TestHelloRejectsIntervalMismatch(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	pkt := ospfHello()
	pkt.HelloInterval = 20
	if _, err := g.Hello("eth0", pkt); !errors.Is(err, impl.ErrIntervalMismatch) {
		t.Fatalf("interval error = %v, want ErrIntervalMismatch", err)
	}
}

func TestRecvLSANewEntryStored(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	v, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000005, "alpha", 10))
	if err != nil {
		t.Fatalf("new lsa must be accepted, got %v", err)
	}
	if v.Action != impl.ActionAccept || v.Reason != impl.ReasonNewLSA {
		t.Fatalf("verdict = %s/%s, want accept/new_lsa", v.Action, v.Reason)
	}
	if !v.Stored {
		t.Fatalf("new lsa must be stored")
	}
	if v.Key == "" {
		t.Fatalf("verdict key must identify the lsa")
	}
	lsas := g.LSDB("0.0.0.0")
	if len(lsas) != 1 {
		t.Fatalf("lsdb entries = %d, want 1", len(lsas))
	}
	if lsas[0].Body != "alpha" || lsas[0].Seq != 0x80000005 {
		t.Fatalf("stored lsa = %+v, want alpha at 0x80000005", lsas[0])
	}
	if got := g.Stats().LSAs; got != 1 {
		t.Fatalf("lsa counter = %d, want 1", got)
	}
}

func TestRecvLSARejectsStaleSequence(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	if _, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000010, "alpha", 10)); err != nil {
		t.Fatalf("new lsa failed: %v", err)
	}
	v, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000005, "older", 10))
	if err != nil {
		t.Fatalf("stale lsa is a policy reject, not an internal error: %v", err)
	}
	if v.Action != impl.ActionReject || v.Reason != impl.ReasonStaleLSA {
		t.Fatalf("verdict = %s/%s, want reject/stale_lsa", v.Action, v.Reason)
	}
	if v.Stored {
		t.Fatalf("stale lsa must not be stored")
	}
	lsas := g.LSDB("0.0.0.0")
	if len(lsas) != 1 || lsas[0].Seq != 0x80000010 {
		t.Fatalf("database changed after a stale lsa: %+v", lsas)
	}
}

func TestRecvLSASameSequenceDifferentBodyRejected(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	if _, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000010, "alpha", 10)); err != nil {
		t.Fatalf("new lsa failed: %v", err)
	}
	v, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000010, "beta", 10))
	if err != nil {
		t.Fatalf("conflicting lsa is a policy reject, not an internal error: %v", err)
	}
	if v.Action != impl.ActionReject || v.Reason != impl.ReasonSeqConflict {
		t.Fatalf("verdict = %s/%s, want reject/seq_conflict", v.Action, v.Reason)
	}
	lsas := g.LSDB("0.0.0.0")
	if len(lsas) != 1 || lsas[0].Body != "alpha" {
		t.Fatalf("database changed after a sequence conflict: %+v", lsas)
	}
}

func TestRecvLSAHigherSequenceReplaces(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	if _, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000005, "old", 10)); err != nil {
		t.Fatalf("first lsa failed: %v", err)
	}
	v, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000006, "new", 10))
	if err != nil {
		t.Fatalf("newer lsa failed: %v", err)
	}
	if v.Action != impl.ActionAccept || v.Reason != impl.ReasonNewerLSA {
		t.Fatalf("verdict = %s/%s, want accept/newer_lsa", v.Action, v.Reason)
	}
	lsas := g.LSDB("0.0.0.0")
	if len(lsas) != 1 {
		t.Fatalf("lsdb entries = %d, want 1", len(lsas))
	}
	if lsas[0].Body != "new" || lsas[0].Seq != 0x80000006 {
		t.Fatalf("stored lsa = %+v, want new at 0x80000006", lsas[0])
	}
}

func TestAgeRemovesExpiredLSA(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	if _, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000005, "alpha", 10)); err != nil {
		t.Fatalf("new lsa failed: %v", err)
	}
	expired := g.Age(time.Now().Add(2 * time.Hour))
	if len(expired) != 1 {
		t.Fatalf("expired entries = %d, want 1", len(expired))
	}
	if expired[0].Reason != impl.ReasonMaxAgeLSA {
		t.Fatalf("expired reason = %q, want %q", expired[0].Reason, impl.ReasonMaxAgeLSA)
	}
	if got := len(g.LSDB("0.0.0.0")); got != 0 {
		t.Fatalf("lsdb entries after aging = %d, want 0", got)
	}
	if got := g.Stats().Purged; got != 1 {
		t.Fatalf("purged counter = %d, want 1", got)
	}
}

func TestLSDBLimitEvictsLowestPriority(t *testing.T) {
	cfg := ospfPolicy()
	cfg.MaxLSAsPerArea = 2
	g := impl.NewOspfLsaGuard(cfg)
	if _, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000005, "low", 5)); err != nil {
		t.Fatalf("low priority lsa failed: %v", err)
	}
	if _, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000006, "mid", 10)); err != nil {
		t.Fatalf("mid priority lsa failed: %v", err)
	}
	if _, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000007, "high", 20)); err != nil {
		t.Fatalf("high priority lsa failed: %v", err)
	}
	lsas := g.LSDB("0.0.0.0")
	if len(lsas) != 2 {
		t.Fatalf("lsdb entries = %d, want 2", len(lsas))
	}
	if lsdbHasBody(lsas, "low") {
		t.Fatalf("lowest priority lsa must be evicted at the table limit")
	}
	if !lsdbHasBody(lsas, "high") {
		t.Fatalf("incoming high priority lsa must be stored")
	}
}

func TestFloodQueueLimitKeepsHigherPriority(t *testing.T) {
	cfg := ospfPolicy()
	cfg.MaxFloodQueue = 1
	g := impl.NewOspfLsaGuard(cfg)
	if err := g.Flood("0.0.0.0", ospfLSA(0x80000005, "low", 5)); err != nil {
		t.Fatalf("first flood failed: %v", err)
	}
	if err := g.Flood("0.0.0.0", ospfLSA(0x80000006, "high", 20)); err != nil {
		t.Fatalf("higher priority flood must replace the queue head: %v", err)
	}
	queue := g.FloodQueue("0.0.0.0")
	if len(queue) != 1 {
		t.Fatalf("flood queue length = %d, want 1", len(queue))
	}
	if queue[0].Body != "high" {
		t.Fatalf("queued lsa = %q, want high", queue[0].Body)
	}
	if got := g.Stats().Floods; got != 2 {
		t.Fatalf("flood counter = %d, want 2", got)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	cfg := ospfPolicy()
	g := impl.NewOspfLsaGuard(cfg)
	if _, err := g.RecvLSA("0.0.0.0", ospfLSA(0x80000005, "alpha", 10)); err != nil {
		t.Fatalf("new lsa failed: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewOspfLsaGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	lsas := restored.LSDB("0.0.0.0")
	if len(lsas) != 1 || lsas[0].Body != "alpha" {
		t.Fatalf("restored database = %+v, want one alpha entry", lsas)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	g := impl.NewOspfLsaGuard(ospfPolicy())
	if err := g.Restore([]byte("not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
}
