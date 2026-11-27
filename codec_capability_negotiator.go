// Package task negotiates a playable codec, profile and level for a media
// source against the capabilities actually probed on a device, stepping down a
// fallback ladder when the preferred rung is not available.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Known codec identifiers.
const (
	CodecH264 Codec = "h264"
	CodecHEVC Codec = "hevc"
	CodecAV1  Codec = "av1"
)

// Decision codes returned with every negotiation.
const (
	CodeExact    = "exact"
	CodeStepDown = "step_down"
	CodeSoftware = "software"
)

// Contract defaults applied when Limits or the configuration leaves a field
// unset.
const (
	DefaultMaxDevices    = 64
	DefaultMaxCapsDevice = 8
	DefaultProbeTTL      = 30 * time.Second
)

// Errors reported by the negotiator.
var (
	ErrBadDevice       = errors.New("device id or capability list is empty")
	ErrBadCapability   = errors.New("capability fields are out of range")
	ErrBadCodec        = errors.New("unknown codec")
	ErrBadRequest      = errors.New("playback request fields are invalid")
	ErrDeviceLimit     = errors.New("device table is full")
	ErrCapabilityLimit = errors.New("capability list exceeds the per device cap")
	ErrUnknownDevice   = errors.New("device has no cache entries")
	ErrProbeFailed     = errors.New("device did not report this codec")
	ErrBadLadder       = errors.New("fallback ladder is empty or invalid")
	ErrBadTTL          = errors.New("probe ttl must be positive")
	ErrNoPlayable      = errors.New("no rung of the ladder can play the request")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Codec is one coded video format.
type Codec string

// DeviceID identifies one playback device.
type DeviceID string

// Capability is one codec, profile and level combination the device reported.
type Capability struct {
	Codec     Codec
	Profile   string
	Level     int
	MaxWidth  int
	MaxHeight int
	HDR       bool
	Hardware  bool
}

// PlaybackReq is the media source that has to be played.
type PlaybackReq struct {
	Device  DeviceID
	Codec   Codec
	Profile string
	Level   int
	Width   int
	Height  int
	HDR     bool
}

// Rung is one step of the fallback ladder.
type Rung struct {
	Profile  string
	Level    int
	Hardware bool
}

// Decision is the explainable outcome of one negotiation.
type Decision struct {
	Device   DeviceID
	Codec    Codec
	Profile  string
	Level    int
	Width    int
	Height   int
	HDR      bool
	Software bool
	Degraded bool
	Step     int
	Code     string
}

// Reason is one human readable line of a decision explanation.
type Reason string

// NegotiationStats is the running counter set of one negotiator.
type NegotiationStats struct {
	Probes        int
	CacheHits     int
	Expired       int
	Negotiations  int
	Software      int
	Degraded      int
	Invalidations int
}

// Limits bounds the device table and the probe cache.
type Limits struct {
	MaxDevices  int
	MaxCaps     int
	MaxCacheAge time.Duration
}

type probeCacheEntry struct {
	capability Capability
	expiresAt  time.Time
	accounted  bool
	last       PlaybackReq
}

// CodecCapabilityNegotiator owns the device table, the probe cache, the
// fallback ladder and the counters.
type CodecCapabilityNegotiator struct {
	mu      sync.Mutex
	limits  Limits
	devices map[DeviceID][]Capability
	cache   map[DeviceID]map[Codec]probeCacheEntry
	pending map[PlaybackReq]struct{}
	ladder  []Rung
	ttl     time.Duration
	stats   NegotiationStats
}

// NewCodecCapabilityNegotiator builds a negotiator for the given caps.
func NewCodecCapabilityNegotiator(limits Limits) *CodecCapabilityNegotiator {
	limits = normalizeLimits(limits)
	ttl := DefaultProbeTTL
	if limits.MaxCacheAge < ttl {
		ttl = limits.MaxCacheAge
	}
	return &CodecCapabilityNegotiator{
		limits:  limits,
		devices: make(map[DeviceID][]Capability),
		cache:   make(map[DeviceID]map[Codec]probeCacheEntry),
		ladder:  DefaultLadder(),
		ttl:     ttl,
	}
}

func normalizeLimits(limits Limits) Limits {
	if limits.MaxDevices <= 0 {
		limits.MaxDevices = DefaultMaxDevices
	}
	if limits.MaxCaps <= 0 {
		limits.MaxCaps = DefaultMaxCapsDevice
	}
	if limits.MaxCacheAge <= 0 {
		limits.MaxCacheAge = DefaultProbeTTL
	}
	return limits
}

func isKnownCodec(codec Codec) bool {
	switch codec {
	case CodecH264, CodecHEVC, CodecAV1:
		return true
	default:
		return false
	}
}

func validateCapability(capability Capability) error {
	if !isKnownCodec(capability.Codec) {
		return ErrBadCodec
	}
	if capability.Level <= 0 || capability.MaxWidth <= 0 || capability.MaxHeight <= 0 {
		return ErrBadCapability
	}
	return nil
}

func validLadder(ladder []Rung) bool {
	if len(ladder) == 0 {
		return false
	}
	for _, rung := range ladder {
		if rung.Profile == "" || rung.Level <= 0 {
			return false
		}
	}
	return true
}

func cloneRungs(ladder []Rung) []Rung {
	if ladder == nil {
		return nil
	}
	cloned := make([]Rung, len(ladder))
	copy(cloned, ladder)
	return cloned
}

// DefaultLadder returns the built in fallback ladder.
func DefaultLadder() []Rung {
	return []Rung{
		{Profile: "main", Level: 41, Hardware: true},
		{Profile: "main", Level: 31, Hardware: true},
		{Profile: "baseline", Level: 30, Hardware: false},
	}
}

// SoftwareRung returns the built in software decoding rung.
func SoftwareRung() Rung {
	return Rung{Profile: "baseline", Level: 30, Hardware: false}
}

// RegisterDevice records the capabilities a probe would find on a device.
func (n *CodecCapabilityNegotiator) RegisterDevice(device DeviceID, caps []Capability) error {
	if device == "" || len(caps) == 0 {
		return ErrBadDevice
	}
	if len(caps) > n.limits.MaxCaps {
		return ErrCapabilityLimit
	}
	for _, capability := range caps {
		if err := validateCapability(capability); err != nil {
			return err
		}
	}

	cloned := make([]Capability, len(caps))
	copy(cloned, caps)

	n.mu.Lock()
	defer n.mu.Unlock()

	if _, exists := n.devices[device]; !exists && len(n.devices) >= n.limits.MaxDevices {
		return ErrDeviceLimit
	}
	n.devices[device] = cloned
	n.dropCacheLocked(device)
	return nil
}

// Probe reports one codec capability of a device, caching the answer for the
// configured TTL.
func (n *CodecCapabilityNegotiator) Probe(device DeviceID, codec string) (Capability, error) {
	requested := Codec(codec)
	if !isKnownCodec(requested) {
		return Capability{}, ErrBadCodec
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	return n.probeLocked(device, requested)
}

func (n *CodecCapabilityNegotiator) probeLocked(device DeviceID, codec Codec) (Capability, error) {
	caps, registered := n.devices[device]
	if !registered {
		return Capability{}, ErrProbeFailed
	}
	best, found := highestCapability(caps, codec)
	if !found {
		return Capability{}, ErrProbeFailed
	}

	now := time.Now()
	if byCodec := n.cache[device]; byCodec != nil {
		if entry, cached := byCodec[codec]; cached {
			if now.Before(entry.expiresAt) {
				n.stats.CacheHits++
				return entry.capability, nil
			}
			delete(byCodec, codec)
			delete(n.pending, entry.last)
			n.stats.Expired++
			if len(byCodec) == 0 {
				delete(n.cache, device)
			}
		}
	}

	n.stats.Probes++
	if n.cache[device] == nil {
		n.cache[device] = make(map[Codec]probeCacheEntry)
	}
	n.cache[device][codec] = probeCacheEntry{
		capability: best,
		expiresAt:  now.Add(n.ttl),
	}
	return best, nil
}

func highestCapability(caps []Capability, codec Codec) (Capability, bool) {
	var best Capability
	found := false
	for _, capability := range caps {
		if capability.Codec != codec {
			continue
		}
		if !found || capability.Level > best.Level {
			best = capability
			found = true
		}
	}
	return best, found
}

// Negotiate chooses a playable rung for the request.
func (n *CodecCapabilityNegotiator) Negotiate(req PlaybackReq) (Decision, error) {
	if req.Width <= 0 || req.Height <= 0 {
		return Decision{}, ErrBadRequest
	}
	if !isKnownCodec(req.Codec) {
		return Decision{}, ErrBadCodec
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	n.stats.Negotiations++
	caps := n.devices[req.Device]
	if _, err := n.probeLocked(req.Device, req.Codec); err == nil {
		if _, restored := n.pending[req]; restored && n.stats.Negotiations > 0 {
			delete(n.pending, req)
			n.stats.Negotiations--
		}
		if byCodec := n.cache[req.Device]; byCodec != nil {
			if entry, ok := byCodec[req.Codec]; ok {
				entry.accounted = true
				entry.last = req
				byCodec[req.Codec] = entry
			}
		}
		for step, rung := range n.ladder {
			if _, matched := matchingCapability(caps, req, rung); !matched {
				continue
			}
			decision := Decision{
				Device:   req.Device,
				Codec:    req.Codec,
				Profile:  rung.Profile,
				Level:    rung.Level,
				Width:    req.Width,
				Height:   req.Height,
				HDR:      req.HDR,
				Software: !rung.Hardware,
				Step:     step,
			}
			if step == 0 {
				decision.Code = CodeExact
			} else {
				decision.Code = CodeStepDown
				decision.Degraded = true
				n.stats.Degraded++
			}
			if decision.Software {
				n.stats.Software++
			}
			return decision, nil
		}
	}

	n.stats.Software++
	n.stats.Degraded++
	return softwareDecision(req), nil
}

func matchingCapability(caps []Capability, req PlaybackReq, rung Rung) (Capability, bool) {
	var best Capability
	found := false
	for _, capability := range caps {
		if capability.Codec != req.Codec || capability.Profile != rung.Profile {
			continue
		}
		if req.Profile != "" && capability.Profile != req.Profile {
			continue
		}
		if capability.Level < rung.Level {
			continue
		}
		if req.Width > capability.MaxWidth || req.Height > capability.MaxHeight {
			continue
		}
		if rung.Hardware && !capability.Hardware {
			continue
		}
		if req.HDR && !capability.HDR {
			continue
		}
		if !found || capability.Level > best.Level {
			best = capability
			found = true
		}
	}
	return best, found
}

func softwareDecision(req PlaybackReq) Decision {
	rung := SoftwareRung()
	return Decision{
		Device:   req.Device,
		Codec:    req.Codec,
		Profile:  rung.Profile,
		Level:    rung.Level,
		Width:    req.Width,
		Height:   req.Height,
		HDR:      req.HDR,
		Software: true,
		Degraded: true,
		Step:     -1,
		Code:     CodeSoftware,
	}
}

// SetFallbackLadder replaces the fallback ladder.
func (n *CodecCapabilityNegotiator) SetFallbackLadder(ladder []Rung) error {
	if !validLadder(ladder) {
		return ErrBadLadder
	}
	cloned := cloneRungs(ladder)
	n.mu.Lock()
	n.ladder = cloned
	n.mu.Unlock()
	return nil
}

// Ladder reports the ladder currently in force.
func (n *CodecCapabilityNegotiator) Ladder() []Rung {
	n.mu.Lock()
	defer n.mu.Unlock()
	return cloneRungs(n.ladder)
}

// Invalidate drops every probe cache entry of one device.
func (n *CodecCapabilityNegotiator) Invalidate(device DeviceID) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	byCodec := n.cache[device]
	if len(byCodec) == 0 {
		return ErrUnknownDevice
	}
	n.dropCacheLocked(device)
	n.stats.Invalidations++
	return nil
}

// ReportOverflow writes a runtime decoder queue overflow back into the
// capability of one device.
func (n *CodecCapabilityNegotiator) ReportOverflow(device DeviceID, width, height int) error {
	if width <= 0 || height <= 0 {
		return ErrBadRequest
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	caps, registered := n.devices[device]
	if !registered {
		return ErrProbeFailed
	}

	reducedWidth := width - 1
	if reducedWidth < 1 {
		reducedWidth = 1
	}
	reducedHeight := height - 1
	if reducedHeight < 1 {
		reducedHeight = 1
	}

	changed := false
	for i := range caps {
		if caps[i].MaxWidth < width || caps[i].MaxHeight < height {
			continue
		}
		if caps[i].MaxWidth > reducedWidth {
			caps[i].MaxWidth = reducedWidth
		}
		if caps[i].MaxHeight > reducedHeight {
			caps[i].MaxHeight = reducedHeight
		}
		changed = true
	}
	if changed {
		n.devices[device] = caps
		n.dropCacheLocked(device)
	}
	return nil
}

func (n *CodecCapabilityNegotiator) dropCacheLocked(device DeviceID) {
	delete(n.cache, device)
	for key := range n.pending {
		if key.Device == device {
			delete(n.pending, key)
		}
	}
}

// Explain renders one decision as ordered reason lines.
func (n *CodecCapabilityNegotiator) Explain(decision Decision) []Reason {
	source := "hw"
	if decision.Software {
		source = "sw"
	}
	return []Reason{
		Reason(fmt.Sprintf("rung=%d", decision.Step)),
		Reason("source=" + source),
	}
}

// SetProbeTTL replaces the probe cache lifetime.
func (n *CodecCapabilityNegotiator) SetProbeTTL(ttl time.Duration) error {
	if ttl <= 0 || ttl > n.limits.MaxCacheAge {
		return ErrBadTTL
	}
	n.mu.Lock()
	n.ttl = ttl
	n.mu.Unlock()
	return nil
}

// ProbeTTL reports the probe cache lifetime in force.
func (n *CodecCapabilityNegotiator) ProbeTTL() time.Duration {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ttl
}

// Stats returns a copy of the running counters.
func (n *CodecCapabilityNegotiator) Stats() NegotiationStats {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.stats
}

const snapshotVersion = 1

type snapshotEnvelope struct {
	Version  int             `json:"version"`
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

type snapshotState struct {
	Devices  []snapshotDevice `json:"devices"`
	Cache    []snapshotCache  `json:"cache"`
	Ladder   []Rung           `json:"ladder"`
	TTLNanos int64            `json:"ttl_nanos"`
	Stats    NegotiationStats `json:"stats"`
}

type snapshotDevice struct {
	ID   DeviceID     `json:"id"`
	Caps []Capability `json:"caps"`
}

type snapshotCache struct {
	Device    DeviceID    `json:"device"`
	Codec     Codec       `json:"codec"`
	Cap       Capability  `json:"capability"`
	ExpiresAt int64       `json:"expires_at_nanos"`
	Accounted bool        `json:"accounted"`
	Last      PlaybackReq `json:"last"`
}

// Snapshot serialises the device table, the probe cache, the ladder, the ttl
// and the counters.
func (n *CodecCapabilityNegotiator) Snapshot() ([]byte, error) {
	n.mu.Lock()
	state := n.snapshotLocked()
	n.mu.Unlock()

	payload, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	envelope, err := json.Marshal(snapshotEnvelope{
		Version:  snapshotVersion,
		Payload:  payload,
		Checksum: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return nil, err
	}
	return envelope, nil
}

func (n *CodecCapabilityNegotiator) snapshotLocked() snapshotState {
	deviceIDs := make([]string, 0, len(n.devices))
	for device := range n.devices {
		deviceIDs = append(deviceIDs, string(device))
	}
	sort.Strings(deviceIDs)

	state := snapshotState{
		Devices:  make([]snapshotDevice, 0, len(deviceIDs)),
		Ladder:   cloneRungs(n.ladder),
		TTLNanos: int64(n.ttl),
		Stats:    n.stats,
	}
	for _, rawDevice := range deviceIDs {
		device := DeviceID(rawDevice)
		caps := make([]Capability, len(n.devices[device]))
		copy(caps, n.devices[device])
		state.Devices = append(state.Devices, snapshotDevice{ID: device, Caps: caps})
	}

	cacheDevices := make([]string, 0, len(n.cache))
	for device := range n.cache {
		cacheDevices = append(cacheDevices, string(device))
	}
	sort.Strings(cacheDevices)
	for _, rawDevice := range cacheDevices {
		device := DeviceID(rawDevice)
		byCodec := n.cache[device]
		codecs := make([]string, 0, len(byCodec))
		for codec := range byCodec {
			codecs = append(codecs, string(codec))
		}
		sort.Strings(codecs)
		for _, rawCodec := range codecs {
			codec := Codec(rawCodec)
			entry := byCodec[codec]
			state.Cache = append(state.Cache, snapshotCache{
				Device:    device,
				Codec:     codec,
				Cap:       entry.capability,
				ExpiresAt: entry.expiresAt.UnixNano(),
				Accounted: entry.accounted,
				Last:      entry.last,
			})
		}
	}
	return state
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (n *CodecCapabilityNegotiator) Restore(data []byte) error {
	var envelope snapshotEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != snapshotVersion || len(envelope.Payload) == 0 {
		return ErrBadSnapshot
	}
	sum := sha256.Sum256(envelope.Payload)
	if envelope.Checksum != hex.EncodeToString(sum[:]) {
		return ErrBadSnapshot
	}

	var state snapshotState
	if err := json.Unmarshal(envelope.Payload, &state); err != nil {
		return ErrBadSnapshot
	}
	devices, cache, err := n.validateSnapshotState(state)
	if err != nil {
		return ErrBadSnapshot
	}

	n.mu.Lock()
	n.devices = devices
	n.cache = cache
	n.pending = make(map[PlaybackReq]struct{})
	for _, byCodec := range cache {
		for _, entry := range byCodec {
			if entry.accounted && entry.last.Width > 0 && entry.last.Height > 0 {
				n.pending[entry.last] = struct{}{}
			}
		}
	}
	n.ladder = cloneRungs(state.Ladder)
	n.ttl = time.Duration(state.TTLNanos)
	n.stats = state.Stats
	n.mu.Unlock()
	return nil
}

func (n *CodecCapabilityNegotiator) validateSnapshotState(state snapshotState) (map[DeviceID][]Capability, map[DeviceID]map[Codec]probeCacheEntry, error) {
	if !validLadder(state.Ladder) || state.TTLNanos <= 0 {
		return nil, nil, ErrBadSnapshot
	}
	if time.Duration(state.TTLNanos) > n.limits.MaxCacheAge {
		return nil, nil, ErrBadSnapshot
	}
	if len(state.Devices) > n.limits.MaxDevices {
		return nil, nil, ErrBadSnapshot
	}
	if !validStats(state.Stats) {
		return nil, nil, ErrBadSnapshot
	}

	devices := make(map[DeviceID][]Capability, len(state.Devices))
	for _, device := range state.Devices {
		if device.ID == "" || len(device.Caps) == 0 || len(device.Caps) > n.limits.MaxCaps {
			return nil, nil, ErrBadSnapshot
		}
		if _, duplicate := devices[device.ID]; duplicate {
			return nil, nil, ErrBadSnapshot
		}
		caps := make([]Capability, len(device.Caps))
		copy(caps, device.Caps)
		for _, capability := range caps {
			if validateCapability(capability) != nil {
				return nil, nil, ErrBadSnapshot
			}
		}
		devices[device.ID] = caps
	}

	cache := make(map[DeviceID]map[Codec]probeCacheEntry)
	for _, entry := range state.Cache {
		if entry.Device == "" || entry.ExpiresAt <= 0 || validateCapability(entry.Cap) != nil || entry.Cap.Codec != entry.Codec {
			return nil, nil, ErrBadSnapshot
		}
		if entry.Accounted && (entry.Last.Device != entry.Device || entry.Last.Codec != entry.Codec || entry.Last.Width <= 0 || entry.Last.Height <= 0) {
			return nil, nil, ErrBadSnapshot
		}
		caps, known := devices[entry.Device]
		if !known {
			return nil, nil, ErrBadSnapshot
		}
		if _, reported := highestCapability(caps, entry.Codec); !reported {
			return nil, nil, ErrBadSnapshot
		}
		if cache[entry.Device] == nil {
			cache[entry.Device] = make(map[Codec]probeCacheEntry)
		}
		if _, duplicate := cache[entry.Device][entry.Codec]; duplicate {
			return nil, nil, ErrBadSnapshot
		}
		cache[entry.Device][entry.Codec] = probeCacheEntry{
			capability: entry.Cap,
			expiresAt:  time.Unix(0, entry.ExpiresAt),
			accounted:  entry.Accounted,
			last:       entry.Last,
		}
	}
	return devices, cache, nil
}

func validStats(stats NegotiationStats) bool {
	return stats.Probes >= 0 &&
		stats.CacheHits >= 0 &&
		stats.Expired >= 0 &&
		stats.Negotiations >= 0 &&
		stats.Software >= 0 &&
		stats.Degraded >= 0 &&
		stats.Invalidations >= 0
}
