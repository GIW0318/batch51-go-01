// Package task drives the OPC UA binary secure channel: chunked message
// reassembly, the two independent sequence counters and the reactivation of a
// session after the channel nonce was rotated.
package task

import "errors"

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// Chunk message types carried by the first header byte.
const (
	MsgTypeMessage = "msg"
	MsgTypeOpen    = "open"
	MsgTypeClose   = "close"
)

// Channel states reported by ChannelState.State.
const (
	ChannelOpen   = "open"
	ChannelClosed = "closed"
)

// Session states reported by SessionInfo.State.
const (
	SessionActive      = "active"
	SessionReactivated = "reactivated"
)

// ChunkHeaderSize is the fixed size of the frame header the guard parses.
const ChunkHeaderSize = 10

// Security policies and modes accepted by Open.
const (
	PolicyNone             = "None"
	PolicyBasic256Sha256   = "Basic256Sha256"
	PolicyAes128Sha256Oaep = "Aes128Sha256RsaOaep"

	ModeNone           = "None"
	ModeSign           = "Sign"
	ModeSignAndEncrypt = "SignAndEncrypt"
)

// Errors returned by the guard.
var (
	ErrBadEndpoint    = errors.New("endpoint is required")
	ErrBadPolicy      = errors.New("security policy is not supported")
	ErrBadMode        = errors.New("security mode does not fit the policy")
	ErrUnknownChannel = errors.New("unknown channel")
	ErrChannelClosed  = errors.New("channel is closed")
	ErrShortChunk     = errors.New("chunk is shorter than the header")
	ErrBadChunkType   = errors.New("chunk header carries an unknown type")
	ErrChunkTooLarge  = errors.New("chunk exceeds the negotiated size")
	ErrSequenceReplay = errors.New("chunk sequence was already consumed")
	ErrSequenceGap    = errors.New("chunk sequence jumps ahead")
	ErrBadToken       = errors.New("channel token does not match")
	ErrBadUser        = errors.New("session user is required")
	ErrBadAuthToken   = errors.New("auth token is required")
	ErrUnknownSession = errors.New("unknown session")
	ErrUserMismatch   = errors.New("auth token belongs to another session")
	ErrBadSnapshot    = errors.New("snapshot payload is invalid")
)

// Limits bounds the live tables of the guard.
type Limits struct {
	MaxChannels  int
	MaxLifetime  uint32
	MaxChunkSize int
}

// OpenSecureChannel is the OPN payload of the first chunk.
type OpenSecureChannel struct {
	SecurityPolicy string
	SecurityMode   string
	ClientNonce    []byte
	Lifetime       uint32
}

// ChannelInfo reports the negotiated channel.
type ChannelInfo struct {
	ChannelID string
	TokenID   uint32
	Lifetime  uint32
	SendSeq   uint32
	RecvSeq   uint32
	Policy    string
	Mode      string
}

// RenewRequest is the OPN payload of a token renewal.
type RenewRequest struct {
	TokenID     uint32
	ClientNonce []byte
	Lifetime    uint32
}

// SessionInfo reports one application session bound to a channel.
type SessionInfo struct {
	SessionID string
	ChannelID string
	User      string
	State     string
	TokenID   uint32
}

// Message is one reassembled message handed to the application.
type Message struct {
	ChannelID string
	Type      string
	RequestID uint32
	Body      []byte
}

// ChannelStats counts what the receive path dropped.
type ChannelStats struct {
	Received   uint64
	Duplicates uint64
	Gaps       uint64
	Aborts     uint64
	Assembled  uint64
}

// ChannelState is the observable state of one channel.
type ChannelState struct {
	ChannelID string
	TokenID   uint32
	SendSeq   uint32
	RecvSeq   uint32
	State     string
}

// OpcuaSecureChannel owns the channel table, the chunk buffers and the session
// table.
type OpcuaSecureChannel struct {
	limits Limits
}

// NewOpcuaSecureChannel builds a guard with the given caps.
func NewOpcuaSecureChannel(limits Limits) *OpcuaSecureChannel {
	return &OpcuaSecureChannel{limits: limits}
}

// Open negotiates a secure channel and issues its first token.
func (g *OpcuaSecureChannel) Open(endpoint string, req OpenSecureChannel) (ChannelInfo, error) {
	return ChannelInfo{}, errNotImplemented
}

// Accept feeds one chunk into the receive path.
func (g *OpcuaSecureChannel) Accept(channelID string, chunk []byte) ([]Message, error) {
	return nil, errNotImplemented
}

// Renew rotates the channel token and keeps both sequence counters.
func (g *OpcuaSecureChannel) Renew(channelID string, req RenewRequest) (ChannelInfo, error) {
	return ChannelInfo{}, errNotImplemented
}

// CreateSession binds an application session to a channel.
func (g *OpcuaSecureChannel) CreateSession(channelID, user string, authToken []byte) (SessionInfo, error) {
	return SessionInfo{}, errNotImplemented
}

// Reactivate brings a session back after the channel was re-opened.
func (g *OpcuaSecureChannel) Reactivate(sessionID string, authToken []byte) (SessionInfo, error) {
	return SessionInfo{}, errNotImplemented
}

// Close shuts the channel down.
func (g *OpcuaSecureChannel) Close(channelID, reason string) error {
	return errNotImplemented
}

// Messages lists the reassembled messages of one channel, oldest first.
func (g *OpcuaSecureChannel) Messages(channelID string) []Message {
	return nil
}

// Stats reads the receive counters of one channel.
func (g *OpcuaSecureChannel) Stats(channelID string) (ChannelStats, bool) {
	return ChannelStats{}, false
}

// Channel reads the current sequence and token state of one channel.
func (g *OpcuaSecureChannel) Channel(channelID string) (ChannelState, bool) {
	return ChannelState{}, false
}

// Snapshot serialises channels, chunk buffers, sessions and counters.
func (g *OpcuaSecureChannel) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *OpcuaSecureChannel) Restore(data []byte) error {
	return errNotImplemented
}
