package task

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"

	impl "gsb/batch51-go-safety-c-01"
)

const (
	testSector     = 512
	testEntries    = 128
	testEntrySize  = 128
	testDeviceSize = int64(2 << 20)
)

func gptHeaderBytes(myLBA, altLBA, entriesLBA uint64, corrupt bool) []byte {
	h := make([]byte, testSector)
	copy(h[0:8], "EFI PART")
	binary.LittleEndian.PutUint32(h[8:12], 0x00010000)
	binary.LittleEndian.PutUint32(h[12:16], 92)
	binary.LittleEndian.PutUint64(h[24:32], myLBA)
	binary.LittleEndian.PutUint64(h[32:40], altLBA)
	binary.LittleEndian.PutUint64(h[40:48], 34)
	binary.LittleEndian.PutUint64(h[48:56], 2047)
	binary.LittleEndian.PutUint64(h[56:64], 0x1122334455667788)
	binary.LittleEndian.PutUint64(h[72:80], entriesLBA)
	binary.LittleEndian.PutUint32(h[80:84], testEntries)
	binary.LittleEndian.PutUint32(h[84:88], testEntrySize)
	binary.LittleEndian.PutUint32(h[88:92], crc32.ChecksumIEEE(make([]byte, testEntries*testEntrySize)))
	binary.LittleEndian.PutUint32(h[16:20], crc32.ChecksumIEEE(h[0:92]))
	if corrupt {
		h[20] ^= 0xff
	}
	return h
}

func buildImage(size int64, primaryOK, backupOK bool) []byte {
	img := make([]byte, size)
	img[510], img[511] = 0x55, 0xaa
	last := uint64(size/testSector) - 1
	copy(img[testSector:], gptHeaderBytes(1, last, 2, !primaryOK))
	copy(img[int64(last)*testSector:], gptHeaderBytes(last, 1, last-uint64(testEntries*testEntrySize/testSector), !backupOK))
	return img
}

func guardConfig() impl.GuardConfig {
	return impl.GuardConfig{
		SectorSize:    testSector,
		ReserveBytes:  1 << 20,
		MaxTickets:    4,
		MaxEvidence:   4,
		MaxWriteQueue: 8,
	}
}

func registerTestDevice(t *testing.T, g *impl.BootRegionGuard, dev string) {
	t.Helper()
	if err := g.RegisterDevice(dev, buildImage(testDeviceSize, true, true)); err != nil {
		t.Fatalf("register device %q: %v", dev, err)
	}
}

func TestRegisterDeviceBuildsGPTLayout(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	registerTestDevice(t, g, "sda")
	layout, err := g.Inspect("sda")
	if err != nil {
		t.Fatalf("inspect registered device: %v", err)
	}
	if layout.Scheme != impl.SchemeGPT {
		t.Fatalf("scheme = %q, want %q", layout.Scheme, impl.SchemeGPT)
	}
	if layout.Size != testDeviceSize {
		t.Fatalf("size = %d, want %d", layout.Size, testDeviceSize)
	}
	if !layout.PrimaryValid || !layout.BackupValid {
		t.Fatalf("headers primary=%v backup=%v, want both valid", layout.PrimaryValid, layout.BackupValid)
	}
	if len(layout.Regions) < 4 {
		t.Fatalf("regions = %+v, want at least the mbr, primary header, primary table and backup areas", layout.Regions)
	}
	for _, r := range layout.Regions {
		if r.Start >= r.End {
			t.Fatalf("region %+v has an empty or reversed range", r)
		}
	}
	if layout.Fingerprint == "" {
		t.Fatalf("layout fingerprint must not be empty")
	}
	again, err := g.Inspect("sda")
	if err != nil {
		t.Fatalf("second inspect: %v", err)
	}
	if again.Fingerprint != layout.Fingerprint {
		t.Fatalf("fingerprint changed between inspects: %q then %q", layout.Fingerprint, again.Fingerprint)
	}
	if got := g.Fingerprint("sda"); got != layout.Fingerprint {
		t.Fatalf("Fingerprint() = %q, want %q", got, layout.Fingerprint)
	}
}

func TestInspectUnknownDeviceRejected(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	if _, err := g.Inspect("sdz"); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("inspect of an unregistered device = %v, want ErrUnknownDevice", err)
	}
	if _, err := g.Inspect(""); !errors.Is(err, impl.ErrBadDevice) {
		t.Fatalf("inspect of an empty device name = %v, want ErrBadDevice", err)
	}
}

func TestRegisterDeviceRejectsTooSmallImage(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	if err := g.RegisterDevice("sdb", make([]byte, testSector)); !errors.Is(err, impl.ErrDeviceTooSmall) {
		t.Fatalf("one sector image = %v, want ErrDeviceTooSmall", err)
	}
	if _, err := g.Inspect("sdb"); !errors.Is(err, impl.ErrUnknownDevice) {
		t.Fatalf("rejected image must not be registered, got %v", err)
	}
}

func TestRegisterDeviceRejectsBadMBRSignature(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	img := buildImage(testDeviceSize, true, true)
	img[510], img[511] = 0x00, 0x00
	if err := g.RegisterDevice("sdc", img); !errors.Is(err, impl.ErrBadMBR) {
		t.Fatalf("image without the 0x55aa signature = %v, want ErrBadMBR", err)
	}
}

func TestWriteRejectsProtectedPrefix(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	registerTestDevice(t, g, "sda")
	if err := g.Write("sda", 0, []byte{0x01, 0x02}); !errors.Is(err, impl.ErrProtectedWrite) {
		t.Fatalf("write to offset 0 = %v, want ErrProtectedWrite", err)
	}
	if err := g.Write("sda", 4096, []byte{0x03}); !errors.Is(err, impl.ErrProtectedWrite) {
		t.Fatalf("write into the reserved prefix = %v, want ErrProtectedWrite", err)
	}
	if got := g.Stats("sda"); got.Rejected != 2 {
		t.Fatalf("rejected counter = %d, want 2", got.Rejected)
	}
	if got := g.Stats("sda"); got.Writes != 0 {
		t.Fatalf("write counter = %d, want 0 for refused writes", got.Writes)
	}
}

func TestWriteRejectsGPTHeaderWithoutTicket(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	registerTestDevice(t, g, "sda")
	if err := g.Write("sda", testSector, make([]byte, 92)); !errors.Is(err, impl.ErrProtectedWrite) {
		t.Fatalf("write over the primary GPT header = %v, want ErrProtectedWrite", err)
	}
	layout, err := g.Inspect("sda")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(layout.Regions) == 0 {
		t.Fatalf("layout must keep its regions after a refused write")
	}
}

func TestAuthorizePartitionTableChangeAllowsWrite(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	registerTestDevice(t, g, "sda")
	dec, err := g.Authorize("sda", impl.WriteRequest{Offset: 1024, Data: make([]byte, testEntrySize), Reason: "resize partition 2"})
	if err != nil {
		t.Fatalf("authorize partition table change: %v", err)
	}
	if !dec.Allowed || dec.Ticket == "" {
		t.Fatalf("decision allowed=%v ticket=%q, want an issued ticket", dec.Allowed, dec.Ticket)
	}
	if dec.Reason != impl.ReasonTicketIssued {
		t.Fatalf("decision reason = %q, want %q", dec.Reason, impl.ReasonTicketIssued)
	}
	payload := bytes.Repeat([]byte{0x7a}, testEntrySize)
	if err := g.Write("sda", 1024, payload); err != nil {
		t.Fatalf("ticketed write into the partition table: %v", err)
	}
	got, err := g.Read("sda", 1024, testEntrySize)
	if err != nil {
		t.Fatalf("read back the partition entry: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read back %x, want %x", got, payload)
	}
	if got := g.Stats("sda"); got.UsedTickets != 1 || got.Writes != 1 {
		t.Fatalf("used tickets=%d writes=%d, want 1 and 1", got.UsedTickets, got.Writes)
	}
}

func TestTicketIsSingleUse(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	registerTestDevice(t, g, "sda")
	if _, err := g.Authorize("sda", impl.WriteRequest{Offset: 1024, Data: make([]byte, testEntrySize), Reason: "rewrite entry"}); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if err := g.Write("sda", 1024, make([]byte, testEntrySize)); err != nil {
		t.Fatalf("first ticketed write: %v", err)
	}
	if err := g.Write("sda", 1024, make([]byte, testEntrySize)); !errors.Is(err, impl.ErrTicketUsed) {
		t.Fatalf("second write with the spent ticket = %v, want ErrTicketUsed", err)
	}
	tickets := g.Tickets("sda")
	if len(tickets) != 1 || tickets[0].State != impl.TicketUsed {
		t.Fatalf("tickets = %+v, want exactly one spent ticket", tickets)
	}
}

func TestWriteWithoutMatchingTicketRejected(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	registerTestDevice(t, g, "sda")
	if _, err := g.Authorize("sda", impl.WriteRequest{Offset: 1024, Data: make([]byte, testEntrySize), Reason: "rewrite entry"}); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if err := g.Write("sda", 1024+testEntrySize, make([]byte, testEntrySize)); !errors.Is(err, impl.ErrTicketMismatch) {
		t.Fatalf("write outside the ticket range = %v, want ErrTicketMismatch", err)
	}
	if got := g.Stats("sda"); got.UsedTickets != 0 {
		t.Fatalf("used tickets = %d, want 0 after a mismatched write", got.UsedTickets)
	}
}

func TestAuthorizeRejectsBlankReason(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	registerTestDevice(t, g, "sda")
	if _, err := g.Authorize("sda", impl.WriteRequest{Offset: 1024, Data: make([]byte, testEntrySize), Reason: "   "}); !errors.Is(err, impl.ErrBadReason) {
		t.Fatalf("blank reason = %v, want ErrBadReason", err)
	}
	if got := len(g.Tickets("sda")); got != 0 {
		t.Fatalf("pending tickets = %d, want 0 after a rejected request", got)
	}
}

func TestAuthorizeRejectsHeaderChange(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	registerTestDevice(t, g, "sda")
	dec, err := g.Authorize("sda", impl.WriteRequest{Offset: testSector, Data: make([]byte, 92), Reason: "patch header"})
	if !errors.Is(err, impl.ErrProtectedWrite) {
		t.Fatalf("authorize over the GPT header = %v, want ErrProtectedWrite", err)
	}
	if dec.Allowed {
		t.Fatalf("header change must not be allowed")
	}
	if dec.Reason != impl.ReasonProtected {
		t.Fatalf("decision reason = %q, want %q", dec.Reason, impl.ReasonProtected)
	}
	if got := len(g.Tickets("sda")); got != 0 {
		t.Fatalf("pending tickets = %d, want 0", got)
	}
}

func TestWriteDataRegionNeedsNoTicket(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	registerTestDevice(t, g, "sda")
	payload := []byte("filesystem-block-0001")
	if err := g.Write("sda", 1<<20, payload); err != nil {
		t.Fatalf("write into the data region: %v", err)
	}
	got, err := g.Read("sda", 1<<20, len(payload))
	if err != nil {
		t.Fatalf("read back the data region: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read back %q, want %q", got, payload)
	}
	stats := g.Stats("sda")
	if stats.Writes != 1 || stats.Rejected != 0 {
		t.Fatalf("writes=%d rejected=%d, want 1 and 0", stats.Writes, stats.Rejected)
	}
	if err := g.Write("sda", testDeviceSize-2, []byte{1, 2, 3, 4}); !errors.Is(err, impl.ErrOutOfRange) {
		t.Fatalf("write past the end of the device = %v, want ErrOutOfRange", err)
	}
}

func TestTicketLimitEnforced(t *testing.T) {
	cfg := guardConfig()
	cfg.MaxTickets = 2
	g := impl.NewBootRegionGuard(cfg)
	registerTestDevice(t, g, "sda")
	for i := 0; i < 2; i++ {
		if _, err := g.Authorize("sda", impl.WriteRequest{Offset: int64(1024 + i*testEntrySize), Data: make([]byte, testEntrySize), Reason: "entry edit"}); err != nil {
			t.Fatalf("authorize %d: %v", i, err)
		}
	}
	_, err := g.Authorize("sda", impl.WriteRequest{Offset: int64(1024 + 2*testEntrySize), Data: make([]byte, testEntrySize), Reason: "entry edit"})
	if !errors.Is(err, impl.ErrTicketLimit) {
		t.Fatalf("third pending ticket = %v, want ErrTicketLimit", err)
	}
	if got := len(g.Tickets("sda")); got != 2 {
		t.Fatalf("pending tickets = %d, want the configured cap 2", got)
	}
}

func TestHalfWrittenDeviceDetected(t *testing.T) {
	g := impl.NewBootRegionGuard(guardConfig())
	if err := g.RegisterDevice("sdd", buildImage(testDeviceSize, false, false)); err != nil {
		t.Fatalf("register device with two damaged headers: %v", err)
	}
	layout, err := g.Inspect("sdd")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if layout.PrimaryValid || layout.BackupValid {
		t.Fatalf("headers primary=%v backup=%v, want both invalid", layout.PrimaryValid, layout.BackupValid)
	}
	if !layout.HalfWrite {
		t.Fatalf("layout must flag a half written boot region when neither header validates")
	}
	if g.Fingerprint("sdd") == "" {
		t.Fatalf("a damaged device still needs a fingerprint for the audit log")
	}
}

func TestSnapshotRestoreKeepsTicketHistory(t *testing.T) {
	cfg := guardConfig()
	g := impl.NewBootRegionGuard(cfg)
	registerTestDevice(t, g, "sda")
	if _, err := g.Authorize("sda", impl.WriteRequest{Offset: 1024, Data: make([]byte, testEntrySize), Reason: "rewrite entry"}); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if err := g.Write("sda", 1024, make([]byte, testEntrySize)); err != nil {
		t.Fatalf("ticketed write: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewBootRegionGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	before, err := g.Inspect("sda")
	if err != nil {
		t.Fatalf("inspect original: %v", err)
	}
	after, err := restored.Inspect("sda")
	if err != nil {
		t.Fatalf("inspect restored: %v", err)
	}
	if after.Fingerprint != before.Fingerprint {
		t.Fatalf("restored fingerprint = %q, want %q", after.Fingerprint, before.Fingerprint)
	}
	if err := restored.Write("sda", 1024, make([]byte, testEntrySize)); !errors.Is(err, impl.ErrTicketUsed) {
		t.Fatalf("spent ticket after restore = %v, want ErrTicketUsed", err)
	}
	if got := restored.Stats("sda"); got.Writes != 1 || got.Rejected != 1 {
		t.Fatalf("restored counters writes=%d rejected=%d, want 1 and 1", got.Writes, got.Rejected)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	if _, err := restored.Inspect("sda"); err != nil {
		t.Fatalf("a rejected restore must keep the live state, got %v", err)
	}
}
