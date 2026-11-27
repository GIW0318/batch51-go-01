// Package task parses IES style photometric profiles, resamples them into
// lookup tables and packs those tables into a slot atlas that lights sample
// by handle.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Errors returned by the atlas.
var (
	ErrBadFormat     = errors.New("ies payload does not follow the expected layout")
	ErrBadType       = errors.New("ies photometric type is not A, B or C")
	ErrBadAngles     = errors.New("ies angle map is not strictly increasing or out of range")
	ErrBadCount      = errors.New("ies value count does not match the angle maps")
	ErrBadValue      = errors.New("ies candela value is negative or not finite")
	ErrBadResolution = errors.New("resample resolution is invalid for the profile")
	ErrBadLimits     = errors.New("atlas limits are invalid")
	ErrAtlasFull     = errors.New("atlas has no room for another slot")
	ErrNoSlot        = errors.New("atlas slot is not packed")
	ErrBadLight      = errors.New("light handle is empty")
	ErrBadBudget     = errors.New("compact budget must not be negative")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// PhotoTypeC, PhotoTypeB and PhotoTypeA are the three angular conventions.
const (
	PhotoTypeA = "A"
	PhotoTypeB = "B"
	PhotoTypeC = "C"
)

const (
	snapshotMagic   = "ies-atlas-snapshot"
	snapshotVersion = 1
)

// AtlasLimits bounds the slot table, the packed bytes and the quarantine.
type AtlasLimits struct {
	MaxSlots      int `json:"max_slots"`
	MaxBytes      int `json:"max_bytes"`
	MoveBudget    int `json:"move_budget"`
	MaxQuarantine int `json:"max_quarantine"`
}

// Profile is one parsed photometric curve.
type Profile struct {
	Type      string    `json:"type"`
	HMAP      []float64 `json:"hmap"`
	VMAP      []float64 `json:"vmap"`
	Values    []float64 `json:"values"`
	Symmetric bool      `json:"symmetric"`
}

// LUT is a resampled lookup table, vertical major.
type LUT struct {
	Type      string    `json:"type"`
	Symmetric bool      `json:"symmetric"`
	HRes      int       `json:"h_res"`
	VRes      int       `json:"v_res"`
	Values    []float64 `json:"values"`
	HMin      float64   `json:"h_min"`
	HMax      float64   `json:"h_max"`
	VMin      float64   `json:"v_min"`
	VMax      float64   `json:"v_max"`
}

// SlotID identifies one atlas slot.
type SlotID int

// SlotInfo describes one packed slot.
type SlotInfo struct {
	ID        SlotID `json:"id"`
	Class     int    `json:"class"`
	HRes      int    `json:"h_res"`
	VRes      int    `json:"v_res"`
	Symmetric bool   `json:"symmetric"`
}

// Atlas is the outcome of one pack pass.
type Atlas struct {
	Slots []SlotInfo `json:"slots"`
	Bytes int        `json:"bytes"`
}

// QuarantineEntry records one rejected payload and its original hash.
type QuarantineEntry struct {
	Hash   string `json:"hash"`
	Reason string `json:"reason"`
}

type slotRecord struct {
	live  bool
	class int
	lut   LUT
}

// IesAtlas owns the slot table, the light handles and the quarantine list.
type IesAtlas struct {
	mu         sync.RWMutex
	limits     AtlasLimits
	slots      []slotRecord
	bytes      int
	lights     map[string]SlotID
	quarantine []QuarantineEntry
}

// NewIesAtlas builds an atlas with the given limits.
func NewIesAtlas(limits AtlasLimits) *IesAtlas {
	return &IesAtlas{
		limits: limits,
		lights: make(map[string]SlotID),
	}
}

// Parse reads one IESLUT payload and marks rotationally symmetric curves.
func (a *IesAtlas) Parse(raw []byte) (Profile, error) {
	profile, err := parseProfile(raw)
	if err != nil {
		a.addQuarantine(raw, err)
		return Profile{}, err
	}
	return profile, nil
}

func parseProfile(raw []byte) (Profile, error) {
	lines := splitLines(raw)
	if len(lines) < 5 {
		return Profile{}, ErrBadFormat
	}

	header := strings.Fields(strings.TrimSpace(lines[0]))
	if len(header) != 2 || header[0] != "IESLUT" || header[1] != "1" {
		return Profile{}, ErrBadFormat
	}

	typeFields := strings.Fields(strings.TrimSpace(lines[1]))
	if len(typeFields) != 2 || typeFields[0] != "TYPE" || !validPhotoType(typeFields[1]) {
		return Profile{}, ErrBadType
	}
	typ := typeFields[1]

	hmap, err := parseAngleLine(lines[2], "HMAP")
	if err != nil {
		return Profile{}, err
	}
	vmap, err := parseAngleLine(lines[3], "VMAP")
	if err != nil {
		return Profile{}, err
	}
	if err := validateAngleMap(typ, true, hmap); err != nil {
		return Profile{}, err
	}
	if err := validateAngleMap(typ, false, vmap); err != nil {
		return Profile{}, err
	}

	totalFields := strings.Fields(strings.TrimSpace(lines[4]))
	if len(totalFields) != 2 || totalFields[0] != "TOTALS" {
		return Profile{}, ErrBadCount
	}
	totals, err := strconv.Atoi(totalFields[1])
	if err != nil || totals < 0 {
		return Profile{}, ErrBadCount
	}
	expected, ok := checkedProduct(len(hmap), len(vmap))
	if !ok || totals != expected {
		return Profile{}, ErrBadCount
	}

	values, ok := parseNumberTokens(strings.Join(lines[5:], " "))
	if !ok || len(values) != totals {
		return Profile{}, ErrBadCount
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return Profile{}, ErrBadValue
		}
	}

	return Profile{
		Type:      typ,
		HMAP:      hmap,
		VMAP:      vmap,
		Values:    values,
		Symmetric: detectSymmetric(typ, len(hmap), len(vmap), values),
	}, nil
}

func splitLines(raw []byte) []string {
	text := strings.TrimPrefix(string(raw), "\ufeff")
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

func validPhotoType(typ string) bool {
	return typ == PhotoTypeA || typ == PhotoTypeB || typ == PhotoTypeC
}

func parseAngleLine(line, label string) ([]float64, error) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, label) {
		return nil, ErrBadAngles
	}
	rest := trimmed[len(label):]
	if rest != "" && rest[0] != ',' && !unicode.IsSpace(rune(rest[0])) {
		return nil, ErrBadAngles
	}
	values, ok := parseNumberTokens(rest)
	if !ok || len(values) == 0 {
		return nil, ErrBadAngles
	}
	return values, nil
}

func parseNumberTokens(text string) ([]float64, bool) {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	values := make([]float64, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.ParseFloat(field, 64)
		if err != nil {
			var numberError *strconv.NumError
			if errors.As(err, &numberError) && errors.Is(numberError.Err, strconv.ErrRange) {
				values = append(values, value)
				continue
			}
			return nil, false
		}
		values = append(values, value)
	}
	return values, true
}

func validateAngleMap(typ string, horizontal bool, angles []float64) error {
	if len(angles) == 0 {
		return ErrBadAngles
	}

	var minAngle, maxAngle float64
	if horizontal {
		switch typ {
		case PhotoTypeA:
			minAngle, maxAngle = -180, 180
		case PhotoTypeB:
			minAngle, maxAngle = -90, 90
		case PhotoTypeC:
			minAngle, maxAngle = 0, 360
		default:
			return ErrBadType
		}
	} else {
		switch typ {
		case PhotoTypeA, PhotoTypeB:
			minAngle, maxAngle = -90, 90
		case PhotoTypeC:
			minAngle, maxAngle = 0, 90
		default:
			return ErrBadType
		}
	}

	for i, angle := range angles {
		if math.IsNaN(angle) || math.IsInf(angle, 0) || angle < minAngle || angle > maxAngle {
			return ErrBadAngles
		}
		if i > 0 && angle <= angles[i-1] {
			return ErrBadAngles
		}
	}
	return nil
}

func checkedProduct(left, right int) (int, bool) {
	maxInt := int(^uint(0) >> 1)
	if left < 0 || right < 0 || (right != 0 && left > maxInt/right) {
		return 0, false
	}
	return left * right, true
}

func detectSymmetric(typ string, hRes, vRes int, values []float64) bool {
	if typ != PhotoTypeC || hRes <= 0 || vRes <= 0 {
		return false
	}
	for row := 0; row < vRes; row++ {
		base := values[row*hRes]
		for col := 1; col < hRes; col++ {
			if values[row*hRes+col] != base {
				return false
			}
		}
	}
	return true
}

func (a *IesAtlas) addQuarantine(raw []byte, reason error) {
	sum := sha256.Sum256(raw)
	entry := QuarantineEntry{
		Hash:   hex.EncodeToString(sum[:])[:16],
		Reason: reason.Error(),
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	limit := a.limits.MaxQuarantine
	if limit <= 0 {
		return
	}
	if len(a.quarantine) >= limit {
		drop := len(a.quarantine) - limit + 1
		a.quarantine = append([]QuarantineEntry(nil), a.quarantine[drop:]...)
	}
	a.quarantine = append(a.quarantine, entry)
}

// Resample turns a profile into a bilinear lookup table of the asked size.
func (a *IesAtlas) Resample(p Profile, hRes, vRes int) (LUT, error) {
	if hRes < 1 || vRes < 2 {
		return LUT{}, ErrBadResolution
	}
	if err := validateProfile(p); err != nil {
		return LUT{}, err
	}

	symmetric := detectSymmetric(p.Type, len(p.HMAP), len(p.VMAP), p.Values)
	if !symmetric && hRes < 2 {
		return LUT{}, ErrBadResolution
	}
	outH := hRes
	if symmetric {
		outH = 1
	}
	if _, ok := checkedProduct(outH, vRes); !ok {
		return LUT{}, ErrBadResolution
	}

	values := make([]float64, outH*vRes)
	for row := 0; row < vRes; row++ {
		v := gridValue(p.VMAP[0], p.VMAP[len(p.VMAP)-1], row, vRes)
		for col := 0; col < outH; col++ {
			h := p.HMAP[0]
			if !symmetric {
				h = gridValue(p.HMAP[0], p.HMAP[len(p.HMAP)-1], col, outH)
			}
			values[row*outH+col] = interpProfileAt(p, h, v)
		}
	}

	return LUT{
		Type:      p.Type,
		Symmetric: symmetric,
		HRes:      outH,
		VRes:      vRes,
		Values:    values,
		HMin:      p.HMAP[0],
		HMax:      p.HMAP[len(p.HMAP)-1],
		VMin:      p.VMAP[0],
		VMax:      p.VMAP[len(p.VMAP)-1],
	}, nil
}

func validateProfile(p Profile) error {
	if !validPhotoType(p.Type) {
		return ErrBadType
	}
	if err := validateAngleMap(p.Type, true, p.HMAP); err != nil {
		return err
	}
	if err := validateAngleMap(p.Type, false, p.VMAP); err != nil {
		return err
	}
	expected, ok := checkedProduct(len(p.HMAP), len(p.VMAP))
	if !ok || expected != len(p.Values) {
		return ErrBadCount
	}
	for _, value := range p.Values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return ErrBadValue
		}
	}
	return nil
}

func gridValue(minAngle, maxAngle float64, index, count int) float64 {
	if count <= 1 || index <= 0 {
		return minAngle
	}
	if index >= count-1 {
		return maxAngle
	}
	return minAngle + (maxAngle-minAngle)*float64(index)/float64(count-1)
}

func clampAngle(value, minAngle, maxAngle float64) float64 {
	if value < minAngle {
		return minAngle
	}
	if value > maxAngle {
		return maxAngle
	}
	return value
}

func segmentIndex(angles []float64, value float64) int {
	if len(angles) < 2 || value <= angles[0] {
		return 0
	}
	if value >= angles[len(angles)-1] {
		return len(angles) - 2
	}
	low, high := 0, len(angles)-1
	for low+1 < high {
		middle := low + (high-low)/2
		if angles[middle] <= value {
			low = middle
		} else {
			high = middle
		}
	}
	return low
}

func isPeriodicMap(typ string, angles []float64) bool {
	if typ != PhotoTypeC || len(angles) < 2 {
		return false
	}
	return math.Abs(angles[0]) <= 1e-12 && math.Abs(angles[len(angles)-1]-360) <= 1e-12
}

func interpProfileAt(p Profile, h, v float64) float64 {
	h = clampAngle(h, p.HMAP[0], p.HMAP[len(p.HMAP)-1])
	v = clampAngle(v, p.VMAP[0], p.VMAP[len(p.VMAP)-1])

	if len(p.VMAP) == 1 {
		return interpProfileRow(p, 0, h)
	}
	row := segmentIndex(p.VMAP, v)
	low := interpProfileRow(p, row, h)
	high := interpProfileRow(p, row+1, h)
	t := (v - p.VMAP[row]) / (p.VMAP[row+1] - p.VMAP[row])
	return lerp(low, high, t)
}

func interpProfileRow(p Profile, row int, h float64) float64 {
	count := len(p.HMAP)
	data := p.Values[row*count : (row+1)*count]
	if count == 1 {
		return data[0]
	}

	periodic := isPeriodicMap(p.Type, p.HMAP)
	if periodic && h >= p.HMAP[count-1] {
		h = p.HMAP[0]
	}
	if h <= p.HMAP[0] {
		return data[0]
	}
	if !periodic && h >= p.HMAP[count-1] {
		return data[count-1]
	}

	idx := segmentIndex(p.HMAP, h)
	right := data[idx+1]
	if periodic && idx == count-2 {
		right = data[0]
	}
	t := (h - p.HMAP[idx]) / (p.HMAP[idx+1] - p.HMAP[idx])
	return lerp(data[idx], right, t)
}

func lerp(low, high, t float64) float64 {
	if t <= 0 {
		return low
	}
	if t >= 1 {
		return high
	}
	return low + (high-low)*t
}

// Pack places the lookup tables into size bucketed atlas slots.
func (a *IesAtlas) Pack(luts []LUT) (Atlas, error) {
	if a.limits.MaxSlots < 0 || a.limits.MaxBytes < 0 {
		return Atlas{}, ErrBadLimits
	}

	copies := make([]LUT, len(luts))
	classes := make([]int, len(luts))
	for i := range luts {
		if err := validateLUT(luts[i]); err != nil {
			return Atlas{}, err
		}
		class, err := bucketClass(luts[i])
		if err != nil {
			return Atlas{}, err
		}
		copies[i] = cloneLUT(luts[i])
		classes[i] = class
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	plan := make([]slotRecord, len(a.slots))
	copy(plan, a.slots)
	planBytes := a.bytes
	maxInt := int(^uint(0) >> 1)

	for i := range copies {
		target := -1
		for slot := range plan {
			if !plan[slot].live && plan[slot].class == classes[i] {
				target = slot
				break
			}
		}
		if target >= 0 {
			plan[target].live = true
			plan[target].lut = copies[i]
			continue
		}

		if len(plan) >= a.limits.MaxSlots {
			return Atlas{}, ErrAtlasFull
		}
		size, ok := slotByteSize(classes[i])
		if !ok || planBytes > maxInt-size || planBytes+size > a.limits.MaxBytes {
			return Atlas{}, ErrAtlasFull
		}
		plan = append(plan, slotRecord{live: true, class: classes[i], lut: copies[i]})
		planBytes += size
	}

	a.slots = plan
	a.bytes = planBytes
	return a.atlasViewLocked(), nil
}

func validateLUT(lut LUT) error {
	if !validPhotoType(lut.Type) {
		return ErrBadType
	}
	if lut.HRes < 1 || lut.VRes < 1 {
		return ErrBadResolution
	}
	expected, ok := checkedProduct(lut.HRes, lut.VRes)
	if !ok || expected != len(lut.Values) {
		return ErrBadCount
	}
	if math.IsNaN(lut.HMin) || math.IsInf(lut.HMin, 0) ||
		math.IsNaN(lut.HMax) || math.IsInf(lut.HMax, 0) ||
		math.IsNaN(lut.VMin) || math.IsInf(lut.VMin, 0) ||
		math.IsNaN(lut.VMax) || math.IsInf(lut.VMax, 0) ||
		lut.HMin > lut.HMax || lut.VMin > lut.VMax {
		return ErrBadAngles
	}
	for _, value := range lut.Values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return ErrBadValue
		}
	}
	return nil
}

func cloneLUT(lut LUT) LUT {
	lut.Values = append([]float64(nil), lut.Values...)
	return lut
}

func bucketClass(lut LUT) (int, error) {
	largest := lut.HRes
	if lut.VRes > largest {
		largest = lut.VRes
	}
	if largest < 1 {
		return 0, ErrBadResolution
	}

	class := 1
	maxInt := int(^uint(0) >> 1)
	for class < largest {
		if class > maxInt/2 {
			return 0, ErrAtlasFull
		}
		class <<= 1
	}
	return class, nil
}

func slotByteSize(class int) (int, bool) {
	maxInt := int(^uint(0) >> 1)
	if class <= 0 || class > maxInt/class {
		return 0, false
	}
	square := class * class
	if square > maxInt/8 {
		return 0, false
	}
	return square * 8, true
}

func (a *IesAtlas) atlasViewLocked() Atlas {
	slots := make([]SlotInfo, 0, len(a.slots))
	for index, record := range a.slots {
		if !record.live {
			continue
		}
		slots = append(slots, SlotInfo{
			ID:        SlotID(index),
			Class:     record.class,
			HRes:      record.lut.HRes,
			VRes:      record.lut.VRes,
			Symmetric: record.lut.Symmetric,
		})
	}
	return Atlas{Slots: slots, Bytes: a.bytes}
}

// Sample reads one packed slot at the given horizontal and vertical angles.
func (a *IesAtlas) Sample(slot SlotID, h, v float64) (float64, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if slot < 0 || int(slot) >= len(a.slots) || !a.slots[int(slot)].live {
		return 0, ErrNoSlot
	}
	return sampleLUT(a.slots[int(slot)].lut, h, v)
}

func sampleLUT(lut LUT, h, v float64) (float64, error) {
	if math.IsNaN(h) || math.IsNaN(v) {
		return 0, ErrBadValue
	}
	if lut.HRes < 1 || lut.VRes < 1 {
		return 0, ErrNoSlot
	}
	expected, ok := checkedProduct(lut.HRes, lut.VRes)
	if !ok || expected != len(lut.Values) {
		return 0, ErrBadValue
	}

	h = clampAngle(h, lut.HMin, lut.HMax)
	v = clampAngle(v, lut.VMin, lut.VMax)
	if lut.VRes == 1 {
		return interpGridRow(lut, 0, h), nil
	}

	row := gridSegmentIndex(v, lut.VMin, lut.VMax, lut.VRes)
	low := interpGridRow(lut, row, h)
	high := interpGridRow(lut, row+1, h)
	t := gridFraction(v, lut.VMin, lut.VMax, lut.VRes, row)
	return lerp(low, high, t), nil
}

func interpGridRow(lut LUT, row int, h float64) float64 {
	count := lut.HRes
	data := lut.Values[row*count : (row+1)*count]
	if count == 1 {
		return data[0]
	}

	periodic := lut.Type == PhotoTypeC &&
		math.Abs(lut.HMin) <= 1e-12 && math.Abs(lut.HMax-360) <= 1e-12
	if periodic && h >= lut.HMax {
		h = lut.HMin
	}
	if h <= lut.HMin {
		return data[0]
	}
	if !periodic && h >= lut.HMax {
		return data[count-1]
	}

	idx := gridSegmentIndex(h, lut.HMin, lut.HMax, count)
	right := data[idx+1]
	if periodic && idx == count-2 {
		right = data[0]
	}
	t := gridFraction(h, lut.HMin, lut.HMax, count, idx)
	return lerp(data[idx], right, t)
}

func gridSegmentIndex(value, minValue, maxValue float64, count int) int {
	if count < 2 || value <= minValue || maxValue <= minValue {
		return 0
	}
	if value >= maxValue {
		return count - 2
	}
	position := (value - minValue) / (maxValue - minValue) * float64(count-1)
	index := int(math.Floor(position))
	if index < 0 {
		return 0
	}
	if index >= count-1 {
		return count - 2
	}
	return index
}

func gridFraction(value, minValue, maxValue float64, count, index int) float64 {
	if count <= 1 || maxValue <= minValue {
		return 0
	}
	position := (value - minValue) / (maxValue - minValue) * float64(count-1)
	return position - float64(index)
}

// BindLight points one light handle at a packed slot.
func (a *IesAtlas) BindLight(lightID string, slot SlotID) error {
	if strings.TrimSpace(lightID) == "" {
		return ErrBadLight
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if slot < 0 || int(slot) >= len(a.slots) || !a.slots[int(slot)].live {
		return ErrNoSlot
	}
	a.lights[lightID] = slot
	return nil
}

// HandleFor reads the slot a light handle currently points at.
func (a *IesAtlas) HandleFor(lightID string) (SlotID, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	slot, ok := a.lights[lightID]
	if !ok || slot < 0 || int(slot) >= len(a.slots) || !a.slots[int(slot)].live {
		return 0, false
	}
	return slot, true
}

// Release frees one slot and unbinds the lights that pointed at it.
func (a *IesAtlas) Release(slot SlotID) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if slot < 0 || int(slot) >= len(a.slots) || !a.slots[int(slot)].live {
		return ErrNoSlot
	}

	a.slots[int(slot)].live = false
	a.slots[int(slot)].lut = LUT{}
	for light, bound := range a.lights {
		if bound == slot {
			delete(a.lights, light)
		}
	}
	return nil
}

// Compact slides live slots down into the holes, spending at most budget moves.
func (a *IesAtlas) Compact(budget int) (int, error) {
	if budget < 0 {
		return 0, ErrBadBudget
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.limits.MaxSlots < 0 || a.limits.MaxBytes < 0 {
		return 0, ErrBadLimits
	}

	live := make([]int, 0, len(a.slots))
	for index, record := range a.slots {
		if record.live {
			live = append(live, index)
		}
	}

	moved := 0
	target := 0
	for _, source := range live {
		if source == target {
			target++
			continue
		}
		if moved >= budget {
			break
		}

		targetClass := a.slots[target].class
		a.slots[target] = a.slots[source]
		a.slots[source] = slotRecord{class: targetClass}
		for light, bound := range a.lights {
			if bound == SlotID(source) {
				a.lights[light] = SlotID(target)
			}
		}
		moved++
		target++
	}

	for len(a.slots) > 0 && !a.slots[len(a.slots)-1].live {
		a.slots = a.slots[:len(a.slots)-1]
	}
	a.bytes = 0
	for _, record := range a.slots {
		size, ok := slotByteSize(record.class)
		if !ok {
			return moved, ErrBadLimits
		}
		a.bytes += size
	}
	return moved, nil
}

// Quarantine lists the rejected payloads, oldest first.
func (a *IesAtlas) Quarantine() []QuarantineEntry {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]QuarantineEntry(nil), a.quarantine...)
}

type snapshotEnvelope struct {
	Magic    string          `json:"magic"`
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	State    json.RawMessage `json:"state"`
}

type snapshotSlot struct {
	Live  bool `json:"live"`
	Class int  `json:"class"`
	LUT   *LUT `json:"lut,omitempty"`
}

type snapshotState struct {
	Slots      []snapshotSlot    `json:"slots"`
	Lights     map[string]SlotID `json:"lights"`
	Quarantine []QuarantineEntry `json:"quarantine"`
}

// Snapshot serialises the slots, the tables, the handles and the quarantine.
func (a *IesAtlas) Snapshot() ([]byte, error) {
	a.mu.RLock()
	state := snapshotState{
		Slots:      make([]snapshotSlot, len(a.slots)),
		Lights:     make(map[string]SlotID, len(a.lights)),
		Quarantine: append([]QuarantineEntry(nil), a.quarantine...),
	}
	for index, record := range a.slots {
		state.Slots[index].Live = record.live
		state.Slots[index].Class = record.class
		if record.live {
			lut := cloneLUT(record.lut)
			state.Slots[index].LUT = &lut
		}
	}
	for light, slot := range a.lights {
		state.Lights[light] = slot
	}
	a.mu.RUnlock()

	rawState, err := json.Marshal(state)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	sum := sha256.Sum256(rawState)
	envelope := snapshotEnvelope{
		Magic:    snapshotMagic,
		Version:  snapshotVersion,
		Checksum: hex.EncodeToString(sum[:]),
		State:    json.RawMessage(rawState),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, ErrBadSnapshot
	}
	return payload, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (a *IesAtlas) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Magic != snapshotMagic ||
		envelope.Version != snapshotVersion || len(envelope.State) == 0 {
		return ErrBadSnapshot
	}
	checksum, err := hex.DecodeString(envelope.Checksum)
	if err != nil || len(checksum) != sha256.Size {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.State)
	if !equalBytes(checksum, sum[:]) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.State, &state); err != nil {
		return ErrBadSnapshot
	}
	if a.limits.MaxSlots < 0 || a.limits.MaxBytes < 0 || a.limits.MaxQuarantine < 0 ||
		len(state.Slots) > a.limits.MaxSlots || len(state.Quarantine) > a.limits.MaxQuarantine {
		return ErrBadSnapshot
	}

	slots := make([]slotRecord, len(state.Slots))
	totalBytes := 0
	maxInt := int(^uint(0) >> 1)
	for index, slot := range state.Slots {
		if slot.Class <= 0 || slot.Class&(slot.Class-1) != 0 {
			return ErrBadSnapshot
		}
		if slot.Live {
			if slot.LUT == nil {
				return ErrBadSnapshot
			}
			if err := validateLUT(*slot.LUT); err != nil {
				return ErrBadSnapshot
			}
			class, err := bucketClass(*slot.LUT)
			if err != nil || class != slot.Class {
				return ErrBadSnapshot
			}
		} else if slot.LUT != nil {
			return ErrBadSnapshot
		}

		size, ok := slotByteSize(slot.Class)
		if !ok || totalBytes > maxInt-size {
			return ErrBadSnapshot
		}
		totalBytes += size
		if totalBytes > a.limits.MaxBytes {
			return ErrBadSnapshot
		}

		slots[index].live = slot.Live
		slots[index].class = slot.Class
		if slot.Live {
			slots[index].lut = cloneLUT(*slot.LUT)
		}
	}

	lights := make(map[string]SlotID, len(state.Lights))
	for light, slot := range state.Lights {
		if strings.TrimSpace(light) == "" || slot < 0 || int(slot) >= len(slots) || !slots[int(slot)].live {
			return ErrBadSnapshot
		}
		lights[light] = slot
	}

	quarantine := append([]QuarantineEntry(nil), state.Quarantine...)
	for _, entry := range quarantine {
		if len(entry.Hash) != 16 || entry.Reason == "" {
			return ErrBadSnapshot
		}
		if _, err := hex.DecodeString(entry.Hash); err != nil {
			return ErrBadSnapshot
		}
	}

	a.mu.Lock()
	a.slots = slots
	a.bytes = totalBytes
	a.lights = lights
	a.quarantine = quarantine
	a.mu.Unlock()
	return nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for i := range left {
		difference |= left[i] ^ right[i]
	}
	return difference == 0
}
