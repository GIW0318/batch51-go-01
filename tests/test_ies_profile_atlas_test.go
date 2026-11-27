package task

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	impl "gsb/batch51-go-game-b-08"
)

func atlasLimits() impl.AtlasLimits {
	return impl.AtlasLimits{MaxSlots: 8, MaxBytes: 1 << 20, MoveBudget: 4, MaxQuarantine: 8}
}

func joinFloats(values []float64) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.FormatFloat(value, 'g', -1, 64)
	}
	return strings.Join(parts, ",")
}

func iesPayload(typ string, hmap, vmap, values []float64) []byte {
	var b strings.Builder
	b.WriteString("IESLUT 1\n")
	b.WriteString("TYPE " + typ + "\n")
	b.WriteString("HMAP " + joinFloats(hmap) + "\n")
	b.WriteString("VMAP " + joinFloats(vmap) + "\n")
	b.WriteString(fmt.Sprintf("TOTALS %d\n", len(values)))
	b.WriteString(joinFloats(values) + "\n")
	return []byte(b.String())
}

func rectProfile() impl.Profile {
	return impl.Profile{
		Type:   impl.PhotoTypeA,
		HMAP:   []float64{-90, 90},
		VMAP:   []float64{0, 90},
		Values: []float64{0, 10, 20, 30},
	}
}

func rectLUT(t *testing.T, a *impl.IesAtlas) impl.LUT {
	t.Helper()
	lut, err := a.Resample(rectProfile(), 3, 3)
	if err != nil {
		t.Fatalf("resample the rectangular profile: %v", err)
	}
	return lut
}

func TestParseRejectsBadHeader(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	if _, err := a.Parse([]byte("not an ies file")); !errors.Is(err, impl.ErrBadFormat) {
		t.Fatalf("a payload without the IESLUT header must fail with ErrBadFormat, got %v", err)
	}
	entries := a.Quarantine()
	if len(entries) != 1 {
		t.Fatalf("quarantine holds %d entries, want 1", len(entries))
	}
	if len(entries[0].Hash) != 16 {
		t.Fatalf("quarantine hash = %q, want a 16 character digest prefix", entries[0].Hash)
	}
	if _, err := hex.DecodeString(entries[0].Hash); err != nil {
		t.Fatalf("quarantine hash %q is not hexadecimal: %v", entries[0].Hash, err)
	}
	if entries[0].Reason == "" {
		t.Fatalf("quarantine entry must keep the rejection reason")
	}
	wrongVersion := iesPayload(impl.PhotoTypeC, []float64{0, 360}, []float64{0, 90}, []float64{1, 1, 1, 1})
	wrongVersion = []byte(strings.Replace(string(wrongVersion), "IESLUT 1", "IESLUT 2", 1))
	if _, err := a.Parse(wrongVersion); !errors.Is(err, impl.ErrBadFormat) {
		t.Fatalf("an unknown layout version must fail with ErrBadFormat, got %v", err)
	}
	if len(a.Quarantine()) != 2 {
		t.Fatalf("both rejected payloads must be quarantined, got %d", len(a.Quarantine()))
	}
}

func TestParseRejectsUnknownType(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	raw := iesPayload("D", []float64{0, 360}, []float64{0, 90}, []float64{1, 1, 1, 1})
	if _, err := a.Parse(raw); !errors.Is(err, impl.ErrBadType) {
		t.Fatalf("photometric type D must fail with ErrBadType, got %v", err)
	}
	if len(a.Quarantine()) != 1 {
		t.Fatalf("a rejected type must be quarantined, got %d entries", len(a.Quarantine()))
	}
}

func TestParseRejectsNonIncreasingAngles(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	unsorted := iesPayload(impl.PhotoTypeC, []float64{0, 90, 45}, []float64{0, 90}, []float64{1, 1, 1, 1, 1, 1})
	if _, err := a.Parse(unsorted); !errors.Is(err, impl.ErrBadAngles) {
		t.Fatalf("a decreasing horizontal map must fail with ErrBadAngles, got %v", err)
	}
	outOfRange := iesPayload(impl.PhotoTypeC, []float64{0, 360}, []float64{0, 120}, []float64{1, 1, 1, 1})
	if _, err := a.Parse(outOfRange); !errors.Is(err, impl.ErrBadAngles) {
		t.Fatalf("a vertical angle of 120 outside type C must fail with ErrBadAngles, got %v", err)
	}
}

func TestParseRejectsCountMismatch(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	tooMany := iesPayload(impl.PhotoTypeC, []float64{0, 360}, []float64{0, 90}, []float64{1, 1, 1, 1, 1, 1})
	if _, err := a.Parse(tooMany); !errors.Is(err, impl.ErrBadCount) {
		t.Fatalf("two maps of two angles with six values must fail with ErrBadCount, got %v", err)
	}
	tooFew := iesPayload(impl.PhotoTypeC, []float64{0, 180, 360}, []float64{0, 90}, []float64{1, 1, 1})
	if _, err := a.Parse(tooFew); !errors.Is(err, impl.ErrBadCount) {
		t.Fatalf("three values for six map entries must fail with ErrBadCount, got %v", err)
	}
}

func TestParseRejectsNegativeValue(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	raw := iesPayload(impl.PhotoTypeC, []float64{0, 360}, []float64{0, 90}, []float64{1, -2, 1, 1})
	if _, err := a.Parse(raw); !errors.Is(err, impl.ErrBadValue) {
		t.Fatalf("a negative candela value must fail with ErrBadValue, got %v", err)
	}
	if len(a.Quarantine()) != 1 {
		t.Fatalf("a rejected value must be quarantined, got %d entries", len(a.Quarantine()))
	}
}

func TestParseMarksSymmetricProfile(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	raw := iesPayload(impl.PhotoTypeC,
		[]float64{0, 90, 180, 270, 360},
		[]float64{0, 90},
		[]float64{1, 1, 1, 1, 1, 0.5, 0.5, 0.5, 0.5, 0.5})
	profile, err := a.Parse(raw)
	if err != nil {
		t.Fatalf("parse a symmetric profile: %v", err)
	}
	if !profile.Symmetric {
		t.Fatalf("a type C profile with identical rows must be marked symmetric")
	}
	lut, err := a.Resample(profile, 4, 2)
	if err != nil {
		t.Fatalf("resample the symmetric profile: %v", err)
	}
	if !lut.Symmetric {
		t.Fatalf("the lookup table of a symmetric profile must be marked symmetric")
	}
	if lut.HRes != 1 {
		t.Fatalf("a symmetric profile must collapse to one horizontal column, got %d", lut.HRes)
	}
	if len(lut.Values) != 2 {
		t.Fatalf("stored values = %d, want one column of two rows", len(lut.Values))
	}
	asymmetric := iesPayload(impl.PhotoTypeC,
		[]float64{0, 180, 360},
		[]float64{0, 90},
		[]float64{1, 0.8, 1, 0.5, 0.4, 0.5})
	profile, err = a.Parse(asymmetric)
	if err != nil {
		t.Fatalf("parse an asymmetric profile: %v", err)
	}
	if profile.Symmetric {
		t.Fatalf("a profile whose rows differ must not be marked symmetric")
	}
}

func TestResampleRejectsBadResolution(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	if _, err := a.Resample(rectProfile(), 0, 3); !errors.Is(err, impl.ErrBadResolution) {
		t.Fatalf("a horizontal resolution of zero must fail with ErrBadResolution, got %v", err)
	}
	if _, err := a.Resample(rectProfile(), 3, 1); !errors.Is(err, impl.ErrBadResolution) {
		t.Fatalf("a vertical resolution of one must fail with ErrBadResolution, got %v", err)
	}
	if _, err := a.Resample(rectProfile(), 1, 2); !errors.Is(err, impl.ErrBadResolution) {
		t.Fatalf("an asymmetric profile with one column must fail with ErrBadResolution, got %v", err)
	}
	lut, err := a.Resample(rectProfile(), 3, 3)
	if err != nil {
		t.Fatalf("a valid resolution must be accepted: %v", err)
	}
	if lut.HRes != 3 || lut.VRes != 3 {
		t.Fatalf("lookup table size = %dx%d, want 3x3", lut.HRes, lut.VRes)
	}
	if lut.HMin != -90 || lut.HMax != 90 || lut.VMin != 0 || lut.VMax != 90 {
		t.Fatalf("lookup table ranges = h[%v,%v] v[%v,%v], want h[-90,90] v[0,90]", lut.HMin, lut.HMax, lut.VMin, lut.VMax)
	}
}

func TestResampleInterpolatesBilinear(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	lut := rectLUT(t, a)
	if len(lut.Values) != 9 {
		t.Fatalf("values of a 3x3 table = %d, want 9", len(lut.Values))
	}
	want := []float64{0, 5, 10, 10, 15, 20, 20, 25, 30}
	for i, value := range want {
		if math.Abs(lut.Values[i]-value) > 1e-9 {
			t.Fatalf("value %d = %v, want %v (vertical major bilinear grid)", i, lut.Values[i], value)
		}
	}
}

func TestSampleClampsAtTheMapEdges(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	if _, err := a.Pack([]impl.LUT{rectLUT(t, a)}); err != nil {
		t.Fatalf("pack: %v", err)
	}
	inside, err := a.Sample(0, 0, 45)
	if err != nil {
		t.Fatalf("sample the centre: %v", err)
	}
	if math.Abs(inside-15) > 1e-9 {
		t.Fatalf("centre sample = %v, want 15", inside)
	}
	high, err := a.Sample(0, 400, 400)
	if err != nil {
		t.Fatalf("sample far above the maps: %v", err)
	}
	if math.Abs(high-30) > 1e-9 {
		t.Fatalf("sample clamped to the top corner = %v, want 30", high)
	}
	low, err := a.Sample(0, -400, -400)
	if err != nil {
		t.Fatalf("sample far below the maps: %v", err)
	}
	if math.Abs(low-0) > 1e-9 {
		t.Fatalf("sample clamped to the bottom corner = %v, want 0", low)
	}
}

func TestPackAssignsSlotsDeterministically(t *testing.T) {
	luts := []impl.LUT{
		{Type: impl.PhotoTypeA, HRes: 2, VRes: 2, Values: []float64{1, 2, 3, 4}, HMin: -90, HMax: 90, VMin: 0, VMax: 90},
		{Type: impl.PhotoTypeC, HRes: 3, VRes: 3, Values: make([]float64, 9), HMin: 0, HMax: 360, VMin: 0, VMax: 90},
	}
	first := impl.NewIesAtlas(atlasLimits())
	atlasA, err := first.Pack(luts)
	if err != nil {
		t.Fatalf("first pack: %v", err)
	}
	second := impl.NewIesAtlas(atlasLimits())
	atlasB, err := second.Pack(luts)
	if err != nil {
		t.Fatalf("second pack: %v", err)
	}
	if !reflect.DeepEqual(atlasA.Slots, atlasB.Slots) {
		t.Fatalf("slot layout differs between two identical packs: %+v vs %+v", atlasA.Slots, atlasB.Slots)
	}
	if atlasA.Bytes != atlasB.Bytes {
		t.Fatalf("packed bytes differ between two identical packs: %d vs %d", atlasA.Bytes, atlasB.Bytes)
	}
	if len(atlasA.Slots) != 2 {
		t.Fatalf("packed slots = %d, want 2", len(atlasA.Slots))
	}
	if atlasA.Slots[0].ID != 0 || atlasA.Slots[1].ID != 1 {
		t.Fatalf("slot ids = %d,%d, want 0,1", atlasA.Slots[0].ID, atlasA.Slots[1].ID)
	}
}

func TestPackEnforcesLimits(t *testing.T) {
	luts := []impl.LUT{
		{Type: impl.PhotoTypeA, HRes: 2, VRes: 2, Values: make([]float64, 4), HMin: -90, HMax: 90},
		{Type: impl.PhotoTypeA, HRes: 2, VRes: 2, Values: make([]float64, 4), HMin: -90, HMax: 90},
	}
	narrowSlots := impl.NewIesAtlas(impl.AtlasLimits{MaxSlots: 1, MaxBytes: 1 << 20})
	if _, err := narrowSlots.Pack(luts); !errors.Is(err, impl.ErrAtlasFull) {
		t.Fatalf("two tables against one slot must fail with ErrAtlasFull, got %v", err)
	}
	narrowBytes := impl.NewIesAtlas(impl.AtlasLimits{MaxSlots: 8, MaxBytes: 100})
	big := impl.LUT{Type: impl.PhotoTypeA, HRes: 3, VRes: 3, Values: make([]float64, 9), HMin: -90, HMax: 90}
	if _, err := narrowBytes.Pack([]impl.LUT{big}); !errors.Is(err, impl.ErrAtlasFull) {
		t.Fatalf("a 128 byte slot against a 100 byte atlas must fail with ErrAtlasFull, got %v", err)
	}
}

func TestPackBucketsSlotsBySize(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	luts := []impl.LUT{
		{Type: impl.PhotoTypeA, HRes: 2, VRes: 2, Values: make([]float64, 4), HMin: -90, HMax: 90},
		{Type: impl.PhotoTypeA, HRes: 3, VRes: 3, Values: make([]float64, 9), HMin: -90, HMax: 90},
	}
	atlas, err := a.Pack(luts)
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if atlas.Slots[0].Class != 2 {
		t.Fatalf("slot of a 2x2 table has class %d, want 2", atlas.Slots[0].Class)
	}
	if atlas.Slots[1].Class != 4 {
		t.Fatalf("slot of a 3x3 table has class %d, want 4", atlas.Slots[1].Class)
	}
	if atlas.Bytes != 32+128 {
		t.Fatalf("packed bytes = %d, want 2*2*8 + 4*4*8 = 160", atlas.Bytes)
	}
}

func TestSampleUnknownSlotFails(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	if _, err := a.Sample(3, 0, 0); !errors.Is(err, impl.ErrNoSlot) {
		t.Fatalf("a slot that was never packed must fail with ErrNoSlot, got %v", err)
	}
	if _, err := a.Pack([]impl.LUT{rectLUT(t, a)}); err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := a.Release(0); err != nil {
		t.Fatalf("release the only slot: %v", err)
	}
	if _, err := a.Sample(0, 0, 0); !errors.Is(err, impl.ErrNoSlot) {
		t.Fatalf("a released slot must fail with ErrNoSlot, got %v", err)
	}
	if err := a.Release(0); !errors.Is(err, impl.ErrNoSlot) {
		t.Fatalf("releasing an empty slot must fail with ErrNoSlot, got %v", err)
	}
}

func TestBindLightAndRelease(t *testing.T) {
	a := impl.NewIesAtlas(atlasLimits())
	luts := []impl.LUT{
		rectLUT(t, a),
		{Type: impl.PhotoTypeC, HRes: 2, VRes: 2, Values: make([]float64, 4), HMin: 0, HMax: 360},
	}
	if _, err := a.Pack(luts); err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := a.BindLight("light-1", 0); err != nil {
		t.Fatalf("bind light-1 to slot 0: %v", err)
	}
	if err := a.BindLight("light-2", 1); err != nil {
		t.Fatalf("bind light-2 to slot 1: %v", err)
	}
	if err := a.BindLight("  ", 0); !errors.Is(err, impl.ErrBadLight) {
		t.Fatalf("a blank light handle must fail with ErrBadLight, got %v", err)
	}
	if err := a.BindLight("light-3", 9); !errors.Is(err, impl.ErrNoSlot) {
		t.Fatalf("binding to an unpacked slot must fail with ErrNoSlot, got %v", err)
	}
	slot, ok := a.HandleFor("light-1")
	if !ok || slot != 0 {
		t.Fatalf("light-1 handle = (%d,%v), want slot 0", slot, ok)
	}
	if err := a.Release(0); err != nil {
		t.Fatalf("release slot 0: %v", err)
	}
	if _, ok := a.HandleFor("light-1"); ok {
		t.Fatalf("releasing a slot must unbind the lights that pointed at it")
	}
	slot, ok = a.HandleFor("light-2")
	if !ok || slot != 1 {
		t.Fatalf("light-2 handle = (%d,%v), want slot 1 untouched", slot, ok)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	limits := atlasLimits()
	a := impl.NewIesAtlas(limits)
	if _, err := a.Pack([]impl.LUT{rectLUT(t, a)}); err != nil {
		t.Fatalf("pack: %v", err)
	}
	if err := a.BindLight("light-1", 0); err != nil {
		t.Fatalf("bind: %v", err)
	}
	before, err := a.Sample(0, 0, 45)
	if err != nil {
		t.Fatalf("sample before the snapshot: %v", err)
	}
	data, err := a.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewIesAtlas(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after, err := restored.Sample(0, 0, 45)
	if err != nil {
		t.Fatalf("sample after the restore: %v", err)
	}
	if math.Abs(after-before) > 1e-12 {
		t.Fatalf("sample after the restore = %v, want %v", after, before)
	}
	slot, ok := restored.HandleFor("light-1")
	if !ok || slot != 0 {
		t.Fatalf("light handle after the restore = (%d,%v), want slot 0", slot, ok)
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("a corrupt payload must fail with ErrBadSnapshot, got %v", err)
	}
	kept, err := restored.Sample(0, 0, 45)
	if err != nil {
		t.Fatalf("sample after the rejected restore: %v", err)
	}
	if math.Abs(kept-before) > 1e-12 {
		t.Fatalf("a rejected restore changed the sample to %v, want %v", kept, before)
	}
}
