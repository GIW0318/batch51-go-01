// Package task quantises high resolution audio down to a target bit depth with
// triangular dither, error feedback noise shaping and per channel
// decorrelation.
package task

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
	"sync"
)

// Bit depth window of the quantiser.
const (
	MinBits = 4
	MaxBits = 24
)

// Channel and block windows.
const (
	MaxChannels    = 8
	MaxBlockFrames = 4096
)

// Shaping curve window: coefficient count and absolute coefficient limit.
const (
	MaxCurveLen  = 32
	MaxCurveCoef = 1.0
)

// Errors returned by the shaper. A rejected call leaves the quantiser state
// untouched.
var (
	ErrBitsRange      = errors.New("target bit depth out of range")
	ErrCurveLength    = errors.New("shaping curve too long")
	ErrCurveCoeff     = errors.New("shaping coefficient out of range")
	ErrChannelCount   = errors.New("channel count out of range")
	ErrRaggedInput    = errors.New("sample count is not a multiple of the channel count")
	ErrFramesRange    = errors.New("frame count out of range")
	ErrUnknownChannel = errors.New("channel index out of range")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// DitherStats is the observable state of the shaper.
type DitherStats struct {
	Bits            int    `json:"bits"`
	Channels        int    `json:"channels"`
	CurveLen        int    `json:"curve_len"`
	ClipGuard       bool   `json:"clip_guard"`
	ProcessedFrames int    `json:"processed_frames"`
	Clipped         int    `json:"clipped"`
	Seed            uint64 `json:"seed"`
}

type ditherRNG struct {
	state   uint64
	initial uint64
}

func newDitherRNG(seed uint64) ditherRNG {
	initial := mix64(seed)
	return ditherRNG{state: initial, initial: initial}
}

func (r *ditherRNG) reset() {
	r.state = r.initial
}

func (r *ditherRNG) nextUint64() uint64 {
	r.state += 0x9e3779b97f4a7c15
	return mix64(r.state)
}

func (r *ditherRNG) uniform() float64 {
	// Keep 53 bits so the result is exactly representable as a float64.
	return float64(r.nextUint64()>>11) * (1.0 / (1 << 53))
}

func (r *ditherRNG) triangular() float64 {
	// TPDF: the difference of two independent rectangular distributions.
	return r.uniform() - r.uniform()
}

func mix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func channelSeed(seed uint64, channels, channel int) uint64 {
	// Both the channel index and the block layout participate in the stream
	// selection. This decorrelates stereo channels and also keeps a mono export
	// from sharing the left-channel sequence with a stereo export.
	x := seed ^ (uint64(channels) * 0x9e3779b97f4a7c15)
	x ^= uint64(channel+1) * 0xd1b54a32d192ed03
	return mix64(x)
}

type channelState struct {
	history []float64
	rng     ditherRNG
}

// DitherNoiseShaper owns the per channel error history and random substreams.
type DitherNoiseShaper struct {
	mu sync.RWMutex

	bits      int
	clipGuard bool
	seed      uint64
	channels  int
	curve     []float64
	states    [MaxChannels]channelState

	processedFrames uint64
	clipped         uint64
}

// NewDitherNoiseShaper builds a shaper for the given bit depth; a depth
// outside the window falls back to sixteen bits. The clip guard starts
// enabled and the seed starts at zero.
func NewDitherNoiseShaper(bits int) *DitherNoiseShaper {
	if bits < MinBits || bits > MaxBits {
		bits = 16
	}
	s := &DitherNoiseShaper{
		bits:      bits,
		clipGuard: true,
	}
	s.initChannelSeedsLocked()
	return s
}

func (s *DitherNoiseShaper) initChannelSeedsLocked() {
	for ch := 0; ch < MaxChannels; ch++ {
		s.states[ch].rng = newDitherRNG(channelSeed(s.seed, s.channels, ch))
	}
}

func (s *DitherNoiseShaper) configureChannelsLocked(channels int) {
	if s.channels == channels {
		return
	}
	s.channels = channels
	for ch := 0; ch < MaxChannels; ch++ {
		s.states[ch].history = make([]float64, len(s.curve))
		s.states[ch].rng = newDitherRNG(channelSeed(s.seed, channels, ch))
	}
}

func (s *DitherNoiseShaper) resetHistoriesLocked() {
	for ch := 0; ch < MaxChannels; ch++ {
		if len(s.curve) == 0 {
			s.states[ch].history = nil
			continue
		}
		s.states[ch].history = make([]float64, len(s.curve))
	}
}

func (s *DitherNoiseShaper) validChannelLocked(ch int) bool {
	if ch < 0 || ch >= MaxChannels {
		return false
	}
	return s.channels == 0 || ch < s.channels
}

// SetTargetBits changes the target bit depth.
func (s *DitherNoiseShaper) SetTargetBits(bits int) error {
	if bits < MinBits || bits > MaxBits {
		return ErrBitsRange
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bits == bits {
		return nil
	}
	s.bits = bits
	// Feedback errors are expressed in quantiser code units, so they cannot be
	// carried across a depth change.
	s.resetHistoriesLocked()
	return nil
}

// SetShapeCurve installs the error feedback coefficients, most recent error
// first. An empty curve disables shaping.
func (s *DitherNoiseShaper) SetShapeCurve(curve []float64) error {
	if len(curve) > MaxCurveLen {
		return ErrCurveLength
	}
	for _, coefficient := range curve {
		if math.IsNaN(coefficient) || math.IsInf(coefficient, 0) || math.Abs(coefficient) > MaxCurveCoef {
			return ErrCurveCoeff
		}
	}

	installed := append([]float64(nil), curve...)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.curve = installed
	s.resetHistoriesLocked()
	return nil
}

// SetSeed sets the master seed the per channel dither substreams derive from.
func (s *DitherNoiseShaper) SetSeed(seed uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seed = seed
	s.initChannelSeedsLocked()
	return nil
}

// Process quantises one interleaved block and returns the same number of
// samples.
func (s *DitherNoiseShaper) Process(in []float32, channels int) ([]int16, error) {
	if channels < 1 || channels > MaxChannels {
		return nil, ErrChannelCount
	}
	if len(in)%channels != 0 {
		return nil, ErrRaggedInput
	}
	frames := len(in) / channels
	if frames > MaxBlockFrames {
		return nil, ErrFramesRange
	}

	out := make([]int16, len(in))

	s.mu.Lock()
	defer s.mu.Unlock()
	s.configureChannelsLocked(channels)

	fullScale := int64(1) << (s.bits - 1)
	minCode := -fullScale
	maxCode := fullScale - 1
	ditherScale := float64(1)
	if s.bits > 16 {
		ditherScale = float64(int64(1) << (s.bits - 16))
	}
	curveLen := len(s.curve)

	for frame := 0; frame < frames; frame++ {
		for ch := 0; ch < channels; ch++ {
			index := frame*channels + ch
			state := &s.states[ch]

			sample := float64(in[index])
			if s.clipGuard {
				if sample > 1 {
					sample = 1
					s.clipped++
				} else if sample < -1 {
					sample = -1
					s.clipped++
				}
			}

			target := sample * float64(fullScale)
			for i := 0; i < curveLen; i++ {
				target += s.curve[i] * state.history[curveLen-1-i]
			}

			code := roundToInt64(target + state.rng.triangular()*ditherScale)
			if s.clipGuard {
				if code < minCode {
					code = minCode
				} else if code > maxCode {
					code = maxCode
				}
			}
			out[index] = codeToInt16(code, s.bits, s.clipGuard)

			if curveLen > 0 {
				copy(state.history[1:], state.history[:curveLen-1])
				state.history[curveLen-1] = float64(code) - target
			}
		}
	}
	s.processedFrames += uint64(frames)
	return out, nil
}

func roundToInt64(value float64) int64 {
	if math.IsNaN(value) {
		return 0
	}
	if value >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	if value <= float64(math.MinInt64) {
		return math.MinInt64
	}
	return int64(math.Round(value))
}

func codeToInt16(code int64, bits int, clipGuard bool) int16 {
	// int16 is the public sample container. At depths above sixteen bits the
	// target-domain code is rounded into that container so full-scale material
	// cannot wrap merely because the container is narrower than the grid. With
	// the guard disabled, conversion wraps as required instead.
	if bits > 16 {
		shift := uint(bits - 16)
		code = (code + (int64(1) << (shift - 1))) >> shift
		if clipGuard {
			if code > math.MaxInt16 {
				code = math.MaxInt16
			} else if code < math.MinInt16 {
				code = math.MinInt16
			}
		}
	}
	return int16(code)
}

// ResetChannel clears the error history of one channel and restarts its dither
// substream from the initial state.
func (s *DitherNoiseShaper) ResetChannel(ch int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validChannelLocked(ch) {
		return ErrUnknownChannel
	}
	if len(s.curve) == 0 {
		s.states[ch].history = nil
	} else {
		s.states[ch].history = make([]float64, len(s.curve))
	}
	s.states[ch].rng.reset()
	return nil
}

// ErrorSpectrum returns the retained error feedback values of one channel,
// oldest first.
func (s *DitherNoiseShaper) ErrorSpectrum(ch int) ([]float64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.validChannelLocked(ch) {
		return nil, ErrUnknownChannel
	}
	history := s.states[ch].history
	if len(history) == 0 {
		return []float64{}, nil
	}
	return append([]float64(nil), history...), nil
}

// SetClipGuard toggles the pre quantisation limiter and its counter.
func (s *DitherNoiseShaper) SetClipGuard(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clipGuard = on
	return nil
}

// Stats reports the configured depth, the curve length and the counters.
func (s *DitherNoiseShaper) Stats() DitherStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return DitherStats{
		Bits:            s.bits,
		Channels:        s.channels,
		CurveLen:        len(s.curve),
		ClipGuard:       s.clipGuard,
		ProcessedFrames: int(s.processedFrames),
		Clipped:         int(s.clipped),
		Seed:            s.seed,
	}
}

const (
	snapshotMagic      = "DNSSHP01"
	snapshotVersion    = byte(1)
	snapshotHeaderSize = len(snapshotMagic) + 4
	snapshotCRCSize    = 4
	snapshotFixedSize  = 1 + 1 + 1 + 1 + 8 + 8 + 8 + 4
)

func appendUint64(dst []byte, value uint64) []byte {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], value)
	return append(dst, buf[:]...)
}

func appendFloat64(dst []byte, value float64) []byte {
	return appendUint64(dst, math.Float64bits(value))
}

// Snapshot serialises the configuration, the per channel error history, the
// random substream positions and the counters.
func (s *DitherNoiseShaper) Snapshot() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	curveLen := len(s.curve)
	payload := make([]byte, 0, snapshotFixedSize+curveLen*8+MaxChannels*16+MaxChannels*curveLen*8)
	payload = append(payload,
		snapshotVersion,
		byte(s.bits),
		byte(s.channels),
		boolByte(s.clipGuard),
	)
	payload = appendUint64(payload, s.seed)
	payload = appendUint64(payload, s.processedFrames)
	payload = appendUint64(payload, s.clipped)

	var curveLenBytes [4]byte
	binary.LittleEndian.PutUint32(curveLenBytes[:], uint32(curveLen))
	payload = append(payload, curveLenBytes[:]...)
	for _, coefficient := range s.curve {
		payload = appendFloat64(payload, coefficient)
	}

	for ch := 0; ch < MaxChannels; ch++ {
		payload = appendUint64(payload, s.states[ch].rng.state)
		payload = appendUint64(payload, s.states[ch].rng.initial)
	}
	for ch := 0; ch < MaxChannels; ch++ {
		for i := 0; i < curveLen; i++ {
			payload = appendFloat64(payload, s.states[ch].history[i])
		}
	}

	data := make([]byte, 0, snapshotHeaderSize+len(payload)+snapshotCRCSize)
	data = append(data, snapshotMagic...)
	var payloadLen [4]byte
	binary.LittleEndian.PutUint32(payloadLen[:], uint32(len(payload)))
	data = append(data, payloadLen[:]...)
	data = append(data, payload...)
	checksum := crc32.ChecksumIEEE(data)
	var checksumBytes [4]byte
	binary.LittleEndian.PutUint32(checksumBytes[:], checksum)
	data = append(data, checksumBytes[:]...)
	return data, nil
}

func boolByte(value bool) byte {
	if value {
		return 1
	}
	return 0
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (s *DitherNoiseShaper) Restore(data []byte) error {
	candidate, err := decodeSnapshot(data)
	if err != nil {
		return ErrBadSnapshot
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.bits = candidate.bits
	s.channels = candidate.channels
	s.clipGuard = candidate.clipGuard
	s.seed = candidate.seed
	s.processedFrames = candidate.processedFrames
	s.clipped = candidate.clipped
	s.curve = candidate.curve
	s.states = candidate.states
	return nil
}

type snapshotState struct {
	bits            int
	channels        int
	clipGuard       bool
	seed            uint64
	processedFrames uint64
	clipped         uint64
	curve           []float64
	states          [MaxChannels]channelState
}

func decodeSnapshot(data []byte) (*snapshotState, error) {
	if len(data) < snapshotHeaderSize+snapshotFixedSize+snapshotCRCSize {
		return nil, ErrBadSnapshot
	}
	if string(data[:len(snapshotMagic)]) != snapshotMagic {
		return nil, ErrBadSnapshot
	}
	payloadLen := int(binary.LittleEndian.Uint32(data[len(snapshotMagic):snapshotHeaderSize]))
	if payloadLen < snapshotFixedSize || snapshotHeaderSize+payloadLen+snapshotCRCSize != len(data) {
		return nil, ErrBadSnapshot
	}
	payloadEnd := snapshotHeaderSize + payloadLen
	expectedCRC := binary.LittleEndian.Uint32(data[payloadEnd:])
	if crc32.ChecksumIEEE(data[:payloadEnd]) != expectedCRC {
		return nil, ErrBadSnapshot
	}

	payload := data[snapshotHeaderSize:payloadEnd]
	pos := 0
	version := payload[pos]
	pos++
	if version != snapshotVersion {
		return nil, ErrBadSnapshot
	}
	bits := int(payload[pos])
	pos++
	channels := int(payload[pos])
	pos++
	clipGuardRaw := payload[pos]
	pos++
	if bits < MinBits || bits > MaxBits || channels < 0 || channels > MaxChannels || clipGuardRaw > 1 {
		return nil, ErrBadSnapshot
	}
	seed := binary.LittleEndian.Uint64(payload[pos:])
	pos += 8
	processedFrames := binary.LittleEndian.Uint64(payload[pos:])
	pos += 8
	clipped := binary.LittleEndian.Uint64(payload[pos:])
	pos += 8
	curveLen := int(binary.LittleEndian.Uint32(payload[pos:]))
	pos += 4
	if curveLen < 0 || curveLen > MaxCurveLen {
		return nil, ErrBadSnapshot
	}
	expectedLen := snapshotFixedSize + curveLen*8 + MaxChannels*16 + MaxChannels*curveLen*8
	if len(payload) != expectedLen {
		return nil, ErrBadSnapshot
	}

	candidate := &snapshotState{
		bits:            bits,
		channels:        channels,
		clipGuard:       clipGuardRaw == 1,
		seed:            seed,
		processedFrames: processedFrames,
		clipped:         clipped,
		curve:           make([]float64, curveLen),
	}
	for i := 0; i < curveLen; i++ {
		coefficient := math.Float64frombits(binary.LittleEndian.Uint64(payload[pos:]))
		pos += 8
		if math.IsNaN(coefficient) || math.IsInf(coefficient, 0) || math.Abs(coefficient) > MaxCurveCoef {
			return nil, ErrBadSnapshot
		}
		candidate.curve[i] = coefficient
	}
	for ch := 0; ch < MaxChannels; ch++ {
		candidate.states[ch].rng.state = binary.LittleEndian.Uint64(payload[pos:])
		pos += 8
		candidate.states[ch].rng.initial = binary.LittleEndian.Uint64(payload[pos:])
		pos += 8
	}
	for ch := 0; ch < MaxChannels; ch++ {
		candidate.states[ch].history = make([]float64, curveLen)
		for i := 0; i < curveLen; i++ {
			value := math.Float64frombits(binary.LittleEndian.Uint64(payload[pos:]))
			pos += 8
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, ErrBadSnapshot
			}
			candidate.states[ch].history[i] = value
		}
	}
	if pos != len(payload) {
		return nil, ErrBadSnapshot
	}
	return candidate, nil
}
