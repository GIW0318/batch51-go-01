// Package task negotiates a playable codec, profile and level for a media
// source against the capabilities actually probed on a device, stepping down a
// fallback ladder when the preferred rung is not available.
package task

import (
	"errors"
	"time"
)

// errNotImplemented marks behaviour that still needs work.
var errNotImplemented = errors.New("not implemented")

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

// CodecCapabilityNegotiator owns the device table, the probe cache, the
// fallback ladder and the counters.
type CodecCapabilityNegotiator struct {
	limits Limits
}

// NewCodecCapabilityNegotiator builds a negotiator for the given caps.
func NewCodecCapabilityNegotiator(limits Limits) *CodecCapabilityNegotiator {
	return &CodecCapabilityNegotiator{limits: limits}
}

// DefaultLadder returns the built in fallback ladder.
func DefaultLadder() []Rung {
	return nil
}

// SoftwareRung returns the built in software decoding rung.
func SoftwareRung() Rung {
	return Rung{}
}

// RegisterDevice records the capabilities a probe would find on a device.
func (n *CodecCapabilityNegotiator) RegisterDevice(device DeviceID, caps []Capability) error {
	return errNotImplemented
}

// Probe reports one codec capability of a device, caching the answer for the
// configured TTL.
func (n *CodecCapabilityNegotiator) Probe(device DeviceID, codec string) (Capability, error) {
	return Capability{}, errNotImplemented
}

// Negotiate chooses a playable rung for the request.
func (n *CodecCapabilityNegotiator) Negotiate(req PlaybackReq) (Decision, error) {
	return Decision{}, errNotImplemented
}

// SetFallbackLadder replaces the fallback ladder.
func (n *CodecCapabilityNegotiator) SetFallbackLadder(ladder []Rung) error {
	return errNotImplemented
}

// Ladder reports the ladder currently in force.
func (n *CodecCapabilityNegotiator) Ladder() []Rung {
	return nil
}

// Invalidate drops every probe cache entry of one device.
func (n *CodecCapabilityNegotiator) Invalidate(device DeviceID) error {
	return errNotImplemented
}

// ReportOverflow writes a runtime decoder queue overflow back into the
// capability of one device.
func (n *CodecCapabilityNegotiator) ReportOverflow(device DeviceID, width, height int) error {
	return errNotImplemented
}

// Explain renders one decision as ordered reason lines.
func (n *CodecCapabilityNegotiator) Explain(decision Decision) []Reason {
	return nil
}

// SetProbeTTL replaces the probe cache lifetime.
func (n *CodecCapabilityNegotiator) SetProbeTTL(ttl time.Duration) error {
	return errNotImplemented
}

// ProbeTTL reports the probe cache lifetime in force.
func (n *CodecCapabilityNegotiator) ProbeTTL() time.Duration {
	return 0
}

// Stats returns a copy of the running counters.
func (n *CodecCapabilityNegotiator) Stats() NegotiationStats {
	return NegotiationStats{}
}

// Snapshot serialises the device table, the probe cache, the ladder, the ttl
// and the counters.
func (n *CodecCapabilityNegotiator) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (n *CodecCapabilityNegotiator) Restore(data []byte) error {
	return errNotImplemented
}
