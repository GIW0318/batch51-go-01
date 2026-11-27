package task

import (
	"encoding/binary"
	"errors"
	"testing"

	impl "gsb/batch51-go-safety-c-04"
)

func acpiTable(sig, oemID, oemTableID string, body []byte, corrupt bool) []byte {
	tbl := make([]byte, impl.TableHeaderSize+len(body))
	copy(tbl[0:4], sig)
	binary.LittleEndian.PutUint32(tbl[4:8], uint32(len(tbl)))
	tbl[8] = 2
	copy(tbl[10:16], oemID)
	copy(tbl[16:24], oemTableID)
	binary.LittleEndian.PutUint32(tbl[24:28], 1)
	copy(tbl[28:32], "INTL")
	binary.LittleEndian.PutUint32(tbl[32:36], 1)
	copy(tbl[36:], body)
	var sum int
	for i, b := range tbl {
		if i == 9 {
			continue
		}
		sum += int(b)
	}
	tbl[9] = byte(0x100 - sum)
	if corrupt {
		tbl[9] ^= 0x01
	}
	return tbl
}

func overridePolicy() impl.OverridePolicy {
	return impl.OverridePolicy{
		MaxTables:            16,
		MaxTableBytes:        4096,
		MaxLayers:            4,
		RequireDsdtCompanion: true,
	}
}

func registerTable(t *testing.T, g *impl.AcpiOverrideGuard, sig, oemTableID, origin string) impl.TableID {
	t.Helper()
	id, err := g.Register(acpiTable(sig, "OEMID1", oemTableID, []byte{0x5b, 0x82, 0x10}, false), origin)
	if err != nil {
		t.Fatalf("register %s/%s: %v", sig, oemTableID, err)
	}
	if id == "" {
		t.Fatalf("register %s/%s returned an empty id", sig, oemTableID)
	}
	return id
}

func TestValidateAcceptsWellFormedTable(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	tbl := acpiTable("DSDT", "OEMID1", "TBL00001", []byte{0x5b, 0x82, 0x10}, false)
	if err := g.Validate(tbl); err != nil {
		t.Fatalf("validate a well formed table: %v", err)
	}
	if got := len(g.Tables()); got != 0 {
		t.Fatalf("validate must not register the table, got %d tables", got)
	}
}

func TestValidateRejectsShortTable(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	if err := g.Validate(make([]byte, impl.TableHeaderSize-16)); !errors.Is(err, impl.ErrShortTable) {
		t.Fatalf("20 byte table = %v, want ErrShortTable", err)
	}
}

func TestValidateRejectsLengthMismatch(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	tbl := acpiTable("SSDT", "OEMID1", "TBL00002", []byte{0x01, 0x02}, false)
	binary.LittleEndian.PutUint32(tbl[4:8], uint32(len(tbl)+4))
	if err := g.Validate(tbl); !errors.Is(err, impl.ErrLengthMismatch) {
		t.Fatalf("length field beyond the payload = %v, want ErrLengthMismatch", err)
	}
}

func TestValidateRejectsBadChecksum(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	tbl := acpiTable("SSDT", "OEMID1", "TBL00003", []byte{0x01, 0x02}, true)
	if err := g.Validate(tbl); !errors.Is(err, impl.ErrChecksumMismatch) {
		t.Fatalf("table with a broken checksum = %v, want ErrChecksumMismatch", err)
	}
}

func TestRegisterRejectsTooLargeTable(t *testing.T) {
	cfg := overridePolicy()
	cfg.MaxTableBytes = 64
	g := impl.NewAcpiOverrideGuard(cfg)
	tbl := acpiTable("SSDT", "OEMID1", "TBL00004", make([]byte, 40), false)
	if _, err := g.Register(tbl, impl.OriginBuiltin); !errors.Is(err, impl.ErrTableTooLarge) {
		t.Fatalf("table above the size cap = %v, want ErrTableTooLarge", err)
	}
	if got := len(g.Tables()); got != 0 {
		t.Fatalf("a rejected table must not be stored, got %d", got)
	}
}

func TestRegisterRejectsUnknownOrigin(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	tbl := acpiTable("SSDT", "OEMID1", "TBL00005", []byte{0x01}, false)
	if _, err := g.Register(tbl, "kernel_module"); !errors.Is(err, impl.ErrBadOrigin) {
		t.Fatalf("register with an unknown origin = %v, want ErrBadOrigin", err)
	}
	if got := g.Stats().Rejected; got != 1 {
		t.Fatalf("rejected counter = %d, want 1", got)
	}
}

func TestResolveOrdersByOriginPriority(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	registerTable(t, g, "SSDT", "TBL00001", impl.OriginBuiltin)
	registerTable(t, g, "SSDT", "TBL00002", impl.OriginInitrdEarly)
	late := registerTable(t, g, "SSDT", "TBL00003", impl.OriginInitrdLate)
	tables, err := g.Resolve("SSDT")
	if err != nil {
		t.Fatalf("resolve SSDT: %v", err)
	}
	if len(tables) != 3 {
		t.Fatalf("resolved %d tables, want 3", len(tables))
	}
	if tables[0].Origin != impl.OriginInitrdLate || tables[0].Priority != impl.PriorityInitrdLate {
		t.Fatalf("first table = %+v, want the late initrd layer", tables[0])
	}
	if tables[1].Origin != impl.OriginInitrdEarly {
		t.Fatalf("second table origin = %q, want %q", tables[1].Origin, impl.OriginInitrdEarly)
	}
	if tables[2].Origin != impl.OriginBuiltin || tables[2].Priority != impl.PriorityBuiltin {
		t.Fatalf("last table = %+v, want the builtin layer", tables[2])
	}
	if tables[0].ID != late {
		t.Fatalf("highest priority table id = %q, want %q", tables[0].ID, late)
	}
	for i := 1; i < len(tables); i++ {
		if tables[i].Priority >= tables[i-1].Priority {
			t.Fatalf("resolve order must be strictly descending by priority, got %+v", tables)
		}
	}
}

func TestResolveUnknownSignatureRejected(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	registerTable(t, g, "SSDT", "TBL00001", impl.OriginBuiltin)
	if _, err := g.Resolve("FACP"); !errors.Is(err, impl.ErrUnknownSignature) {
		t.Fatalf("resolve of an unregistered signature = %v, want ErrUnknownSignature", err)
	}
}

func TestApplyActivatesHighestPriority(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	builtin := registerTable(t, g, "SSDT", "TBL00001", impl.OriginBuiltin)
	early := registerTable(t, g, "SSDT", "TBL00002", impl.OriginInitrdEarly)
	late := registerTable(t, g, "SSDT", "TBL00003", impl.OriginInitrdLate)
	if err := g.Apply([]impl.TableID{builtin, early, late}); err != nil {
		t.Fatalf("apply three SSDT layers: %v", err)
	}
	active, ok := g.Active("SSDT")
	if !ok {
		t.Fatalf("expected an active SSDT after apply")
	}
	if active.Origin != impl.OriginInitrdLate || active.ID != late {
		t.Fatalf("active table = %+v, want the late layer %q", active, late)
	}
	if got := g.Layers("SSDT"); got != 3 {
		t.Fatalf("layers = %d, want 3", got)
	}
	if got := g.Stats().Applies; got != 1 {
		t.Fatalf("apply counter = %d, want 1", got)
	}
}

func TestApplyRejectsUnknownTable(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	registerTable(t, g, "SSDT", "TBL00001", impl.OriginBuiltin)
	if err := g.Apply([]impl.TableID{"table-404"}); !errors.Is(err, impl.ErrUnknownTable) {
		t.Fatalf("apply of an unknown table = %v, want ErrUnknownTable", err)
	}
	if _, ok := g.Active("SSDT"); ok {
		t.Fatalf("a failed batch must not activate anything")
	}
	if got := g.Stats().Active; got != 0 {
		t.Fatalf("active counter = %d, want 0", got)
	}
}

func TestApplyRejectsEmptyOrder(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	registerTable(t, g, "SSDT", "TBL00001", impl.OriginBuiltin)
	if err := g.Apply(nil); !errors.Is(err, impl.ErrEmptyOrder) {
		t.Fatalf("empty apply order = %v, want ErrEmptyOrder", err)
	}
	if _, ok := g.Active("SSDT"); ok {
		t.Fatalf("an empty batch must not activate anything")
	}
}

func TestApplyRejectsDsdtWithoutCompanion(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	dsdt := registerTable(t, g, "DSDT", "TBL00001", impl.OriginBuiltin)
	if err := g.Apply([]impl.TableID{dsdt}); !errors.Is(err, impl.ErrDsdtNeedsSsdt) {
		t.Fatalf("DSDT override without a companion SSDT = %v, want ErrDsdtNeedsSsdt", err)
	}
	if _, ok := g.Active("DSDT"); ok {
		t.Fatalf("a refused DSDT batch must not activate the table")
	}
}

func TestApplyAcceptsSelfContainedDsdt(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	dsdt := registerTable(t, g, "DSDT", "TBL00001", impl.OriginBuiltin)
	if err := g.SetSelfContained(dsdt, true); err != nil {
		t.Fatalf("mark self contained: %v", err)
	}
	if err := g.Apply([]impl.TableID{dsdt}); err != nil {
		t.Fatalf("apply a self contained DSDT: %v", err)
	}
	active, ok := g.Active("DSDT")
	if !ok || active.ID != dsdt || !active.SelfContained {
		t.Fatalf("active DSDT = %+v ok=%v, want the self contained table", active, ok)
	}
	if err := g.SetSelfContained("table-404", true); !errors.Is(err, impl.ErrUnknownTable) {
		t.Fatalf("marking an unknown table = %v, want ErrUnknownTable", err)
	}
}

func TestRollbackRestoresPreviousSet(t *testing.T) {
	g := impl.NewAcpiOverrideGuard(overridePolicy())
	early := registerTable(t, g, "SSDT", "TBL00002", impl.OriginInitrdEarly)
	late := registerTable(t, g, "SSDT", "TBL00003", impl.OriginInitrdLate)
	if err := g.Apply([]impl.TableID{late}); err != nil {
		t.Fatalf("apply the late layer: %v", err)
	}
	if err := g.Apply([]impl.TableID{early}); err != nil {
		t.Fatalf("apply the early layer: %v", err)
	}
	if active, _ := g.Active("SSDT"); active.ID != early {
		t.Fatalf("active before rollback = %+v, want the early layer", active)
	}
	if err := g.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	active, ok := g.Active("SSDT")
	if !ok || active.ID != late {
		t.Fatalf("active after rollback = %+v ok=%v, want the late layer back", active, ok)
	}
	if got := g.Stats().Rollbacks; got != 1 {
		t.Fatalf("rollback counter = %d, want 1", got)
	}
	if err := g.Deregister(late); !errors.Is(err, impl.ErrActiveTable) {
		t.Fatalf("deregistering the active table = %v, want ErrActiveTable", err)
	}
	if err := g.Rollback(); !errors.Is(err, impl.ErrNoRollback) {
		t.Fatalf("second rollback = %v, want ErrNoRollback", err)
	}
}

func TestSnapshotRestoreKeepsActiveSet(t *testing.T) {
	cfg := overridePolicy()
	g := impl.NewAcpiOverrideGuard(cfg)
	dsdt := registerTable(t, g, "DSDT", "TBL00001", impl.OriginBuiltin)
	ssdt := registerTable(t, g, "SSDT", "TBL00002", impl.OriginInitrdLate)
	if err := g.SetSelfContained(dsdt, true); err != nil {
		t.Fatalf("mark self contained: %v", err)
	}
	if err := g.Apply([]impl.TableID{dsdt, ssdt}); err != nil {
		t.Fatalf("apply the batch: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewAcpiOverrideGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if active, ok := restored.Active("SSDT"); !ok || active.ID != ssdt {
		t.Fatalf("restored SSDT = %+v ok=%v, want %q", active, ok, ssdt)
	}
	if active, ok := restored.Active("DSDT"); !ok || !active.SelfContained {
		t.Fatalf("restored DSDT = %+v ok=%v, want the self contained table", active, ok)
	}
	if got := restored.Stats().Applies; got != 1 {
		t.Fatalf("restored apply counter = %d, want 1", got)
	}
	if err := restored.Restore([]byte("{not-an-acpi-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot = %v, want ErrBadSnapshot", err)
	}
	if _, ok := restored.Active("SSDT"); !ok {
		t.Fatalf("a rejected restore must keep the active set")
	}
}
