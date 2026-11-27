package task

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"

	impl "gsb/batch51-go-safety-b-01"
)

func snoopConfig() impl.SnoopConfig {
	return impl.SnoopConfig{
		TrustedPorts:   []string{"uplink-1"},
		MaxBindings:    64,
		MaxMACsPerPort: 8,
		LeaseTTL:       time.Hour,
		ConflictHold:   30 * time.Second,
		MaxAudit:       32,
	}
}

func snoopAddr(t *testing.T, raw string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatalf("test address %q is invalid: %v", raw, err)
	}
	return addr
}

func TestIngestDiscoverCreatesBinding(t *testing.T) {
	g := impl.NewDhcpSnoopingGuard(snoopConfig())
	v, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgDiscover,
		MAC:  "AA:BB:CC:00:00:01",
		VLAN: 10,
	})
	if err != nil {
		t.Fatalf("discover on an untrusted client port must be accepted, got %v", err)
	}
	if v.Action != impl.ActionAllow {
		t.Fatalf("verdict action = %q, want %q", v.Action, impl.ActionAllow)
	}
	if v.Reason != impl.ReasonClientBound {
		t.Fatalf("verdict reason = %q, want %q", v.Reason, impl.ReasonClientBound)
	}
	b, ok := g.Binding("aa:bb:cc:00:00:01", 10)
	if !ok {
		t.Fatalf("discover must create a binding keyed by mac and vlan")
	}
	if b.Port != "edge-1" {
		t.Fatalf("binding port = %q, want edge-1", b.Port)
	}
	if !b.Active {
		t.Fatalf("new binding must be active")
	}
	if b.IP.IsValid() {
		t.Fatalf("a discover placeholder must not carry an address, got %s", b.IP)
	}
	if b.Seq <= 0 {
		t.Fatalf("binding sequence = %d, want a positive value", b.Seq)
	}
}

func TestIngestRequestFillsBinding(t *testing.T) {
	g := impl.NewDhcpSnoopingGuard(snoopConfig())
	start := time.Now()
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: "aa:bb:cc:00:00:02", VLAN: 20}); err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	v, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type:        impl.MsgRequest,
		MAC:         "aa:bb:cc:00:00:02",
		VLAN:        20,
		RequestedIP: snoopAddr(t, "10.20.0.5"),
		ServerID:    snoopAddr(t, "10.20.0.1"),
	})
	if err != nil {
		t.Fatalf("matching request must be accepted, got %v", err)
	}
	if v.Action != impl.ActionAllow || v.Reason != impl.ReasonClientBound {
		t.Fatalf("verdict = %s/%s, want allow/client_bound", v.Action, v.Reason)
	}
	b, ok := g.Binding("aa:bb:cc:00:00:02", 20)
	if !ok {
		t.Fatalf("binding disappeared after the request")
	}
	if b.IP != snoopAddr(t, "10.20.0.5") {
		t.Fatalf("binding address = %s, want 10.20.0.5", b.IP)
	}
	if b.ServerID != snoopAddr(t, "10.20.0.1") {
		t.Fatalf("binding server id = %s, want 10.20.0.1", b.ServerID)
	}
	if !b.LeaseUntil.After(start) {
		t.Fatalf("lease deadline %s must be in the future", b.LeaseUntil)
	}
	if v.Binding.IP != b.IP {
		t.Fatalf("verdict binding address = %s, want %s", v.Binding.IP, b.IP)
	}
}

func TestIngestRequestMismatchDropped(t *testing.T) {
	g := impl.NewDhcpSnoopingGuard(snoopConfig())
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: "aa:bb:cc:00:00:03", VLAN: 30}); err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgRequest, MAC: "aa:bb:cc:00:00:03", VLAN: 30,
		RequestedIP: snoopAddr(t, "10.30.0.5"), ServerID: snoopAddr(t, "10.30.0.1"),
	}); err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	v, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgRequest, MAC: "aa:bb:cc:00:00:03", VLAN: 30,
		RequestedIP: snoopAddr(t, "10.30.0.6"), ServerID: snoopAddr(t, "10.30.0.1"),
	})
	if err != nil {
		t.Fatalf("a mismatched request is a policy drop, not an internal error: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonRequestMismatch {
		t.Fatalf("verdict = %s/%s, want drop/request_mismatch", v.Action, v.Reason)
	}
	b, ok := g.Binding("aa:bb:cc:00:00:03", 30)
	if !ok {
		t.Fatalf("binding must survive a rejected request")
	}
	if b.IP != snoopAddr(t, "10.30.0.5") {
		t.Fatalf("binding address changed to %s, want 10.30.0.5", b.IP)
	}
}

func TestUntrustedOfferDroppedAndAudited(t *testing.T) {
	g := impl.NewDhcpSnoopingGuard(snoopConfig())
	v, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgOffer, MAC: "ff:ff:ff:00:00:01", VLAN: 40,
		RequestedIP: snoopAddr(t, "10.40.0.9"), ServerID: snoopAddr(t, "10.40.0.1"),
		Lease: 48 * time.Hour,
	})
	if err != nil {
		t.Fatalf("an untrusted server frame is dropped without an internal error: %v", err)
	}
	if v.Action != impl.ActionDrop || v.Reason != impl.ReasonUntrustedServer {
		t.Fatalf("verdict = %s/%s, want drop/untrusted_server", v.Action, v.Reason)
	}
	if _, ok := g.Binding("ff:ff:ff:00:00:01", 40); ok {
		t.Fatalf("a server frame must never create a client binding")
	}
	audit := g.Audit()
	if len(audit) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(audit))
	}
	if audit[0].Reason != impl.ReasonUntrustedServer {
		t.Fatalf("audit reason = %q, want %q", audit[0].Reason, impl.ReasonUntrustedServer)
	}
	if got := g.Stats().Alerts; got != 1 {
		t.Fatalf("alert counter = %d, want 1", got)
	}
}

func TestTrustedAckAllowed(t *testing.T) {
	g := impl.NewDhcpSnoopingGuard(snoopConfig())
	v, err := g.Ingest("uplink-1", true, impl.DhcpMessage{
		Type: impl.MsgAck, MAC: "aa:bb:cc:00:00:04", VLAN: 50,
		RequestedIP: snoopAddr(t, "10.50.0.5"), ServerID: snoopAddr(t, "10.50.0.1"),
	})
	if err != nil {
		t.Fatalf("ack on a trusted port must be accepted, got %v", err)
	}
	if v.Action != impl.ActionAllow || v.Reason != impl.ReasonTrustedServer {
		t.Fatalf("verdict = %s/%s, want allow/trusted_server", v.Action, v.Reason)
	}
	if _, ok := g.Binding("aa:bb:cc:00:00:04", 50); ok {
		t.Fatalf("a server frame must not fabricate a client binding")
	}
}

func TestDeclineRevokesBinding(t *testing.T) {
	g := impl.NewDhcpSnoopingGuard(snoopConfig())
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: "aa:bb:cc:00:00:05", VLAN: 60}); err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	ip := snoopAddr(t, "10.60.0.5")
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgRequest, MAC: "aa:bb:cc:00:00:05", VLAN: 60,
		RequestedIP: ip, ServerID: snoopAddr(t, "10.60.0.1"),
	}); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	v, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgDecline, MAC: "aa:bb:cc:00:00:05", VLAN: 60, RequestedIP: ip,
	})
	if err != nil {
		t.Fatalf("decline must be accepted, got %v", err)
	}
	if v.Action != impl.ActionAllow || v.Reason != impl.ReasonDeclined {
		t.Fatalf("verdict = %s/%s, want allow/declined", v.Action, v.Reason)
	}
	if _, ok := g.Binding("aa:bb:cc:00:00:05", 60); ok {
		t.Fatalf("decline must withdraw the binding")
	}
}

func TestCheckSourceAcceptsBoundIP(t *testing.T) {
	g := impl.NewDhcpSnoopingGuard(snoopConfig())
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: "aa:bb:cc:00:00:06", VLAN: 70}); err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgRequest, MAC: "aa:bb:cc:00:00:06", VLAN: 70,
		RequestedIP: snoopAddr(t, "10.70.0.5"), ServerID: snoopAddr(t, "10.70.0.1"),
	}); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if err := g.CheckSource("edge-1", "aa:bb:cc:00:00:06", snoopAddr(t, "10.70.0.5")); err != nil {
		t.Fatalf("bound source must be accepted, got %v", err)
	}
}

func TestCheckSourceRejectsWrongPort(t *testing.T) {
	g := impl.NewDhcpSnoopingGuard(snoopConfig())
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: "aa:bb:cc:00:00:07", VLAN: 80}); err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgRequest, MAC: "aa:bb:cc:00:00:07", VLAN: 80,
		RequestedIP: snoopAddr(t, "10.80.0.5"), ServerID: snoopAddr(t, "10.80.0.1"),
	}); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	err := g.CheckSource("edge-2", "aa:bb:cc:00:00:07", snoopAddr(t, "10.80.0.5"))
	if !errors.Is(err, impl.ErrSourcePort) {
		t.Fatalf("wrong port error = %v, want ErrSourcePort", err)
	}
}

func TestExpireRemovesLease(t *testing.T) {
	cfg := snoopConfig()
	cfg.LeaseTTL = time.Minute
	g := impl.NewDhcpSnoopingGuard(cfg)
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: "aa:bb:cc:00:00:08", VLAN: 90}); err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgRequest, MAC: "aa:bb:cc:00:00:08", VLAN: 90,
		RequestedIP: snoopAddr(t, "10.90.0.5"), ServerID: snoopAddr(t, "10.90.0.1"),
	}); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	removed := g.Expire(time.Now().Add(2 * time.Minute))
	if removed != 1 {
		t.Fatalf("expired bindings = %d, want 1", removed)
	}
	if _, ok := g.Binding("aa:bb:cc:00:00:08", 90); ok {
		t.Fatalf("expired binding is still readable")
	}
	if got := g.Stats().Expired; got != 1 {
		t.Fatalf("expired counter = %d, want 1", got)
	}
}

func TestBindingCapacityEvictsLeastActive(t *testing.T) {
	cfg := snoopConfig()
	cfg.MaxBindings = 2
	g := impl.NewDhcpSnoopingGuard(cfg)
	for _, mac := range []string{"aa:bb:cc:00:01:01", "aa:bb:cc:00:01:02", "aa:bb:cc:00:01:03"} {
		if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: mac, VLAN: 100}); err != nil {
			t.Fatalf("discover %s failed: %v", mac, err)
		}
	}
	if _, ok := g.Binding("aa:bb:cc:00:01:01", 100); ok {
		t.Fatalf("oldest binding must be evicted when the table is full")
	}
	if _, ok := g.Binding("aa:bb:cc:00:01:03", 100); !ok {
		t.Fatalf("newest binding must be present")
	}
	if got := g.Stats().Bindings; got != 2 {
		t.Fatalf("binding count = %d, want 2", got)
	}
}

func TestPerPortMACLimitEvictsLeastActive(t *testing.T) {
	cfg := snoopConfig()
	cfg.MaxMACsPerPort = 2
	g := impl.NewDhcpSnoopingGuard(cfg)
	for _, mac := range []string{"aa:bb:cc:00:02:01", "aa:bb:cc:00:02:02", "aa:bb:cc:00:02:03"} {
		if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: mac, VLAN: 110}); err != nil {
			t.Fatalf("discover %s failed: %v", mac, err)
		}
	}
	if _, ok := g.Binding("aa:bb:cc:00:02:01", 110); ok {
		t.Fatalf("oldest mac of the port must be evicted at the port limit")
	}
	if _, ok := g.Binding("aa:bb:cc:00:02:03", 110); !ok {
		t.Fatalf("newest mac of the port must be present")
	}
	if got := len(g.PortBindings("edge-1")); got != 2 {
		t.Fatalf("port bindings = %d, want 2", got)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	cfg := snoopConfig()
	g := impl.NewDhcpSnoopingGuard(cfg)
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: "aa:bb:cc:00:03:01", VLAN: 120}); err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{
		Type: impl.MsgRequest, MAC: "aa:bb:cc:00:03:01", VLAN: 120,
		RequestedIP: snoopAddr(t, "10.120.0.5"), ServerID: snoopAddr(t, "10.120.0.1"),
	}); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewDhcpSnoopingGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	b, ok := restored.Binding("aa:bb:cc:00:03:01", 120)
	if !ok {
		t.Fatalf("binding missing after restore")
	}
	if b.IP != snoopAddr(t, "10.120.0.5") {
		t.Fatalf("restored address = %s, want 10.120.0.5", b.IP)
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	g := impl.NewDhcpSnoopingGuard(snoopConfig())
	err := g.Restore([]byte("{not a snapshot"))
	if !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
}

func TestConcurrentIngestKeepsOneBindingPerMAC(t *testing.T) {
	cfg := snoopConfig()
	cfg.MaxMACsPerPort = 64
	g := impl.NewDhcpSnoopingGuard(cfg)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mac := fmt.Sprintf("aa:bb:cc:00:04:%02x", n)
			_, err := g.Ingest("edge-1", false, impl.DhcpMessage{Type: impl.MsgDiscover, MAC: mac, VLAN: 130})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent discover failed: %v", err)
	}
	if got := g.Stats().Bindings; got != 20 {
		t.Fatalf("binding count after concurrent ingest = %d, want 20", got)
	}
	if got := len(g.PortBindings("edge-1")); got != 20 {
		t.Fatalf("port binding count = %d, want 20", got)
	}
}

func TestAuditLogCapacity(t *testing.T) {
	cfg := snoopConfig()
	cfg.MaxAudit = 5
	g := impl.NewDhcpSnoopingGuard(cfg)
	for i := 0; i < 8; i++ {
		mac := fmt.Sprintf("aa:bb:cc:00:05:%02x", i)
		if _, err := g.Ingest("edge-1", false, impl.DhcpMessage{
			Type: impl.MsgAck, MAC: mac, VLAN: 140,
			RequestedIP: snoopAddr(t, "10.140.0.9"), ServerID: snoopAddr(t, "10.140.0.1"),
		}); err != nil {
			t.Fatalf("untrusted ack %d failed: %v", i, err)
		}
	}
	audit := g.Audit()
	if len(audit) != 5 {
		t.Fatalf("audit entries = %d, want 5", len(audit))
	}
	if audit[len(audit)-1].Reason != impl.ReasonUntrustedServer {
		t.Fatalf("newest audit reason = %q, want %q", audit[len(audit)-1].Reason, impl.ReasonUntrustedServer)
	}
	if got := g.Stats().Dropped; got != 8 {
		t.Fatalf("drop counter = %d, want 8", got)
	}
}
