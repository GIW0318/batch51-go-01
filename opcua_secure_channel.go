// Package task drives the OPC UA binary secure channel: chunked message
// reassembly, the two independent sequence counters and the reactivation of a
// session after the channel nonce was rotated.
package task

import (
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
)

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
	PolicyNone                = "None"
	PolicyBasic256Sha256      = "Basic256Sha256"
	PolicyAes128Sha256Oaep    = "Aes128Sha256RsaOaep"
	PolicyAes128Sha256RsaOaep = PolicyAes128Sha256Oaep

	ModeNone           = "None"
	ModeSign           = "Sign"
	ModeSignAndEncrypt = "SignAndEncrypt"
)

// Errors returned by the guard.
var (
	ErrBadEndpoint    = errors.New("endpoint is required")
	ErrBadPolicy      = errors.New("security policy is not supported")
	ErrBadMode        = errors.New("security mode does not fit the policy")
	ErrChannelLimit   = errors.New("channel limit reached")
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

type assemblyKey struct {
	messageType byte
	requestID   uint32
}

type pendingMessage struct {
	key  assemblyKey
	body []byte
}

type channelRecord struct {
	id          string
	endpoint    string
	tokenID     uint32
	sendSeq     uint32
	recvSeq     uint32
	lifetime    uint32
	policy      string
	mode        string
	state       string
	reason      string
	clientNonce []byte
	pending     *pendingMessage
	messages    []Message
	stats       ChannelStats
}

type sessionRecord struct {
	id        string
	channelID string
	user      string
	authToken []byte
	state     string
	tokenID   uint32
}

// OpcuaSecureChannel owns the channel table, the chunk buffers and the session
// table.
type OpcuaSecureChannel struct {
	mu            sync.Mutex
	limits        Limits
	channels      map[string]*channelRecord
	sessions      map[string]*sessionRecord
	nextChannelID uint64
	nextSessionID uint64
}

// NewOpcuaSecureChannel builds a guard with the given caps.
func NewOpcuaSecureChannel(limits Limits) *OpcuaSecureChannel {
	return &OpcuaSecureChannel{
		limits:   limits,
		channels: make(map[string]*channelRecord),
		sessions: make(map[string]*sessionRecord),
	}
}

// Open negotiates a secure channel and issues its first token.
func (g *OpcuaSecureChannel) Open(endpoint string, req OpenSecureChannel) (ChannelInfo, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ChannelInfo{}, ErrBadEndpoint
	}
	if err := validatePolicyMode(req.SecurityPolicy, req.SecurityMode); err != nil {
		return ChannelInfo{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureTablesLocked()

	if g.limits.MaxChannels <= 0 || len(g.channels) >= g.limits.MaxChannels {
		return ChannelInfo{}, ErrChannelLimit
	}

	channelID := g.nextChannelIDLocked()
	channel := &channelRecord{
		id:          channelID,
		endpoint:    endpoint,
		tokenID:     1,
		sendSeq:     1,
		recvSeq:     0,
		lifetime:    clampLifetime(req.Lifetime, g.limits.MaxLifetime),
		policy:      req.SecurityPolicy,
		mode:        req.SecurityMode,
		state:       ChannelOpen,
		clientNonce: cloneBytes(req.ClientNonce),
	}
	g.channels[channelID] = channel
	return channelInfo(channel), nil
}

// Accept feeds one chunk into the receive path.
func (g *OpcuaSecureChannel) Accept(channelID string, chunk []byte) ([]Message, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureTablesLocked()

	channel, ok := g.channels[channelID]
	if !ok {
		return nil, ErrUnknownChannel
	}
	if channel.state == ChannelClosed {
		return nil, ErrChannelClosed
	}
	if len(chunk) < ChunkHeaderSize {
		return nil, ErrShortChunk
	}

	messageType, ok := chunkMessageType(chunk[0])
	if !ok {
		return nil, ErrBadChunkType
	}
	chunkType := chunk[1]
	if chunkType != 'C' && chunkType != 'F' && chunkType != 'A' {
		return nil, ErrBadChunkType
	}
	if g.limits.MaxChunkSize <= 0 || len(chunk) > g.limits.MaxChunkSize {
		return nil, ErrChunkTooLarge
	}

	sequence := binary.LittleEndian.Uint32(chunk[2:6])
	requestID := binary.LittleEndian.Uint32(chunk[6:10])
	expected := channel.recvSeq + 1
	if sequence < expected {
		channel.stats.Duplicates++
		return nil, ErrSequenceReplay
	}
	if sequence > expected {
		channel.stats.Gaps++
		return nil, ErrSequenceGap
	}

	// From here on the chunk is the one expected by the receive side. Advancing
	// the watermark and mutating the reassembly buffer happen under the same
	// lock, so concurrent callers cannot both consume the same sequence.
	channel.recvSeq = sequence
	channel.stats.Received++
	payload := chunk[ChunkHeaderSize:]
	key := assemblyKey{messageType: chunk[0], requestID: requestID}

	switch chunkType {
	case 'A':
		channel.pending = nil
		channel.stats.Aborts++
		return nil, nil
	case 'C':
		if channel.pending == nil || channel.pending.key != key {
			channel.pending = &pendingMessage{key: key, body: cloneBytes(payload)}
		} else {
			channel.pending.body = append(channel.pending.body, payload...)
		}
		return nil, nil
	default: // 'F'
		body := cloneBytes(payload)
		if channel.pending != nil {
			if channel.pending.key == key {
				body = append(channel.pending.body, payload...)
			}
			// A final chunk belonging to another request closes the stale
			// assembly as well. It must never be joined to a later request.
			channel.pending = nil
		}
		message := Message{
			ChannelID: channelID,
			Type:      messageType,
			RequestID: requestID,
			Body:      body,
		}
		channel.messages = append(channel.messages, cloneMessage(message))
		channel.stats.Assembled++
		return []Message{cloneMessage(message)}, nil
	}
}

// Renew rotates the channel token and keeps both sequence counters.
func (g *OpcuaSecureChannel) Renew(channelID string, req RenewRequest) (ChannelInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureTablesLocked()

	channel, ok := g.channels[channelID]
	if !ok {
		return ChannelInfo{}, ErrUnknownChannel
	}
	if channel.state == ChannelClosed {
		return ChannelInfo{}, ErrChannelClosed
	}
	if req.TokenID != channel.tokenID {
		return ChannelInfo{}, ErrBadToken
	}

	channel.tokenID++
	channel.lifetime = clampLifetime(req.Lifetime, g.limits.MaxLifetime)
	channel.clientNonce = cloneBytes(req.ClientNonce)
	return channelInfo(channel), nil
}

// CreateSession binds an application session to a channel.
func (g *OpcuaSecureChannel) CreateSession(channelID, user string, authToken []byte) (SessionInfo, error) {
	if strings.TrimSpace(user) == "" {
		return SessionInfo{}, ErrBadUser
	}
	if len(authToken) == 0 {
		return SessionInfo{}, ErrBadAuthToken
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureTablesLocked()

	channel, ok := g.channels[channelID]
	if !ok {
		return SessionInfo{}, ErrUnknownChannel
	}
	if channel.state == ChannelClosed {
		return SessionInfo{}, ErrChannelClosed
	}

	sessionID := g.nextSessionIDLocked()
	session := &sessionRecord{
		id:        sessionID,
		channelID: channelID,
		user:      user,
		authToken: cloneBytes(authToken),
		state:     SessionActive,
		tokenID:   channel.tokenID,
	}
	g.sessions[sessionID] = session
	return sessionInfo(session), nil
}

// Reactivate brings a session back after the channel was re-opened.
func (g *OpcuaSecureChannel) Reactivate(sessionID string, authToken []byte) (SessionInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureTablesLocked()

	session, ok := g.sessions[sessionID]
	if !ok {
		return SessionInfo{}, ErrUnknownSession
	}
	if subtle.ConstantTimeCompare(session.authToken, authToken) != 1 {
		return SessionInfo{}, ErrUserMismatch
	}
	channel, ok := g.channels[session.channelID]
	if !ok {
		return SessionInfo{}, ErrUnknownChannel
	}
	if channel.state == ChannelClosed {
		return SessionInfo{}, ErrChannelClosed
	}

	session.state = SessionReactivated
	session.tokenID = channel.tokenID
	return sessionInfo(session), nil
}

// Close shuts the channel down.
func (g *OpcuaSecureChannel) Close(channelID, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureTablesLocked()

	channel, ok := g.channels[channelID]
	if !ok {
		return ErrUnknownChannel
	}
	if channel.state == ChannelClosed {
		return nil
	}
	channel.state = ChannelClosed
	channel.reason = reason
	channel.pending = nil
	return nil
}

// Messages lists the reassembled messages of one channel, oldest first.
func (g *OpcuaSecureChannel) Messages(channelID string) []Message {
	g.mu.Lock()
	defer g.mu.Unlock()

	channel, ok := g.channels[channelID]
	if !ok {
		return nil
	}
	return cloneMessages(channel.messages)
}

// Stats reads the receive counters of one channel.
func (g *OpcuaSecureChannel) Stats(channelID string) (ChannelStats, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	channel, ok := g.channels[channelID]
	if !ok {
		return ChannelStats{}, false
	}
	return channel.stats, true
}

// Channel reads the current sequence and token state of one channel.
func (g *OpcuaSecureChannel) Channel(channelID string) (ChannelState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	channel, ok := g.channels[channelID]
	if !ok {
		return ChannelState{}, false
	}
	return ChannelState{
		ChannelID: channel.id,
		TokenID:   channel.tokenID,
		SendSeq:   channel.sendSeq,
		RecvSeq:   channel.recvSeq,
		State:     channel.state,
	}, true
}

// Snapshot serialises channels, chunk buffers, sessions and counters.
func (g *OpcuaSecureChannel) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ensureTablesLocked()

	snapshot := snapshotState{
		Magic:         snapshotMagic,
		Version:       snapshotVersion,
		NextChannelID: g.nextChannelID,
		NextSessionID: g.nextSessionID,
	}

	channelIDs := make([]string, 0, len(g.channels))
	for id := range g.channels {
		channelIDs = append(channelIDs, id)
	}
	sort.Strings(channelIDs)
	for _, id := range channelIDs {
		channel := g.channels[id]
		saved := channelSnapshot{
			ChannelID:   channel.id,
			Endpoint:    channel.endpoint,
			TokenID:     channel.tokenID,
			SendSeq:     channel.sendSeq,
			RecvSeq:     channel.recvSeq,
			Lifetime:    channel.lifetime,
			Policy:      channel.policy,
			Mode:        channel.mode,
			State:       channel.state,
			Reason:      channel.reason,
			ClientNonce: cloneBytes(channel.clientNonce),
			Messages:    cloneMessages(channel.messages),
			Stats:       channel.stats,
		}
		if channel.pending != nil {
			saved.Pending = &pendingSnapshot{
				MessageType: channel.pending.key.messageType,
				RequestID:   channel.pending.key.requestID,
				Body:        cloneBytes(channel.pending.body),
			}
		}
		snapshot.Channels = append(snapshot.Channels, saved)
	}

	sessionIDs := make([]string, 0, len(g.sessions))
	for id := range g.sessions {
		sessionIDs = append(sessionIDs, id)
	}
	sort.Strings(sessionIDs)
	for _, id := range sessionIDs {
		session := g.sessions[id]
		snapshot.Sessions = append(snapshot.Sessions, sessionSnapshot{
			SessionID: session.id,
			ChannelID: session.channelID,
			User:      session.user,
			AuthToken: cloneBytes(session.authToken),
			State:     session.state,
			TokenID:   session.tokenID,
		})
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *OpcuaSecureChannel) Restore(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var snapshot snapshotState
	if err := decoder.Decode(&snapshot); err != nil {
		return ErrBadSnapshot
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrBadSnapshot
	}

	channels, sessions, err := g.buildRestoredTables(snapshot)
	if err != nil {
		return ErrBadSnapshot
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.channels = channels
	g.sessions = sessions
	g.nextChannelID = snapshot.NextChannelID
	g.nextSessionID = snapshot.NextSessionID
	return nil
}

const (
	snapshotMagic   = "gsb/batch51-go-safety-b-15/opcua-secure-channel"
	snapshotVersion = 1
)

type pendingSnapshot struct {
	MessageType byte   `json:"messageType"`
	RequestID   uint32 `json:"requestId"`
	Body        []byte `json:"body,omitempty"`
}

type channelSnapshot struct {
	ChannelID   string           `json:"channelId"`
	Endpoint    string           `json:"endpoint"`
	TokenID     uint32           `json:"tokenId"`
	SendSeq     uint32           `json:"sendSeq"`
	RecvSeq     uint32           `json:"recvSeq"`
	Lifetime    uint32           `json:"lifetime"`
	Policy      string           `json:"policy"`
	Mode        string           `json:"mode"`
	State       string           `json:"state"`
	Reason      string           `json:"reason,omitempty"`
	ClientNonce []byte           `json:"clientNonce,omitempty"`
	Pending     *pendingSnapshot `json:"pending,omitempty"`
	Messages    []Message        `json:"messages,omitempty"`
	Stats       ChannelStats     `json:"stats"`
}

type sessionSnapshot struct {
	SessionID string `json:"sessionId"`
	ChannelID string `json:"channelId"`
	User      string `json:"user"`
	AuthToken []byte `json:"authToken"`
	State     string `json:"state"`
	TokenID   uint32 `json:"tokenId"`
}

type snapshotState struct {
	Magic         string            `json:"magic"`
	Version       int               `json:"version"`
	NextChannelID uint64            `json:"nextChannelId"`
	NextSessionID uint64            `json:"nextSessionId"`
	Channels      []channelSnapshot `json:"channels"`
	Sessions      []sessionSnapshot `json:"sessions"`
}

func (g *OpcuaSecureChannel) buildRestoredTables(snapshot snapshotState) (map[string]*channelRecord, map[string]*sessionRecord, error) {
	if snapshot.Magic != snapshotMagic || snapshot.Version != snapshotVersion {
		return nil, nil, ErrBadSnapshot
	}
	if g.limits.MaxChannels <= 0 || len(snapshot.Channels) > g.limits.MaxChannels {
		return nil, nil, ErrBadSnapshot
	}

	channels := make(map[string]*channelRecord, len(snapshot.Channels))
	for _, saved := range snapshot.Channels {
		if saved.ChannelID == "" || saved.Endpoint == "" || saved.TokenID == 0 || saved.SendSeq == 0 {
			return nil, nil, ErrBadSnapshot
		}
		if _, exists := channels[saved.ChannelID]; exists {
			return nil, nil, ErrBadSnapshot
		}
		if saved.State != ChannelOpen && saved.State != ChannelClosed {
			return nil, nil, ErrBadSnapshot
		}
		if err := validatePolicyMode(saved.Policy, saved.Mode); err != nil {
			return nil, nil, ErrBadSnapshot
		}
		if saved.Lifetime > g.limits.MaxLifetime {
			return nil, nil, ErrBadSnapshot
		}

		channel := &channelRecord{
			id:          saved.ChannelID,
			endpoint:    saved.Endpoint,
			tokenID:     saved.TokenID,
			sendSeq:     saved.SendSeq,
			recvSeq:     saved.RecvSeq,
			lifetime:    saved.Lifetime,
			policy:      saved.Policy,
			mode:        saved.Mode,
			state:       saved.State,
			reason:      saved.Reason,
			clientNonce: cloneBytes(saved.ClientNonce),
			messages:    cloneMessages(saved.Messages),
			stats:       saved.Stats,
		}
		if saved.Pending != nil {
			if _, ok := chunkMessageType(saved.Pending.MessageType); !ok {
				return nil, nil, ErrBadSnapshot
			}
			channel.pending = &pendingMessage{
				key: assemblyKey{
					messageType: saved.Pending.MessageType,
					requestID:   saved.Pending.RequestID,
				},
				body: cloneBytes(saved.Pending.Body),
			}
		}
		for _, message := range channel.messages {
			if message.ChannelID != saved.ChannelID {
				return nil, nil, ErrBadSnapshot
			}
			if _, ok := namedMessageType(message.Type); !ok {
				return nil, nil, ErrBadSnapshot
			}
		}
		channels[saved.ChannelID] = channel
	}

	sessions := make(map[string]*sessionRecord, len(snapshot.Sessions))
	for _, saved := range snapshot.Sessions {
		if saved.SessionID == "" || strings.TrimSpace(saved.User) == "" || len(saved.AuthToken) == 0 || saved.TokenID == 0 {
			return nil, nil, ErrBadSnapshot
		}
		if _, exists := sessions[saved.SessionID]; exists {
			return nil, nil, ErrBadSnapshot
		}
		if _, exists := channels[saved.ChannelID]; !exists {
			return nil, nil, ErrBadSnapshot
		}
		if saved.State != SessionActive && saved.State != SessionReactivated {
			return nil, nil, ErrBadSnapshot
		}
		sessions[saved.SessionID] = &sessionRecord{
			id:        saved.SessionID,
			channelID: saved.ChannelID,
			user:      saved.User,
			authToken: cloneBytes(saved.AuthToken),
			state:     saved.State,
			tokenID:   saved.TokenID,
		}
	}
	return channels, sessions, nil
}

func (g *OpcuaSecureChannel) ensureTablesLocked() {
	if g.channels == nil {
		g.channels = make(map[string]*channelRecord)
	}
	if g.sessions == nil {
		g.sessions = make(map[string]*sessionRecord)
	}
}

func (g *OpcuaSecureChannel) nextChannelIDLocked() string {
	for {
		g.nextChannelID++
		id := "channel-" + strconv.FormatUint(g.nextChannelID, 10)
		if _, exists := g.channels[id]; !exists {
			return id
		}
	}
}

func (g *OpcuaSecureChannel) nextSessionIDLocked() string {
	for {
		g.nextSessionID++
		id := "session-" + strconv.FormatUint(g.nextSessionID, 10)
		if _, exists := g.sessions[id]; !exists {
			return id
		}
	}
}

func validatePolicyMode(policy, mode string) error {
	switch policy {
	case PolicyNone:
		if mode != ModeNone {
			return ErrBadMode
		}
	case PolicyBasic256Sha256, PolicyAes128Sha256RsaOaep:
		if mode == ModeNone || (mode != ModeSign && mode != ModeSignAndEncrypt) {
			return ErrBadMode
		}
	default:
		return ErrBadPolicy
	}
	return nil
}

func clampLifetime(requested, maximum uint32) uint32 {
	if requested > maximum {
		return maximum
	}
	return requested
}

func chunkMessageType(value byte) (string, bool) {
	switch value {
	case 'M':
		return MsgTypeMessage, true
	case 'O':
		return MsgTypeOpen, true
	case 'C':
		return MsgTypeClose, true
	default:
		return "", false
	}
}

func namedMessageType(value string) (byte, bool) {
	switch value {
	case MsgTypeMessage:
		return 'M', true
	case MsgTypeOpen:
		return 'O', true
	case MsgTypeClose:
		return 'C', true
	default:
		return 0, false
	}
}

func channelInfo(channel *channelRecord) ChannelInfo {
	return ChannelInfo{
		ChannelID: channel.id,
		TokenID:   channel.tokenID,
		Lifetime:  channel.lifetime,
		SendSeq:   channel.sendSeq,
		RecvSeq:   channel.recvSeq,
		Policy:    channel.policy,
		Mode:      channel.mode,
	}
}

func sessionInfo(session *sessionRecord) SessionInfo {
	return SessionInfo{
		SessionID: session.id,
		ChannelID: session.channelID,
		User:      session.user,
		State:     session.state,
		TokenID:   session.tokenID,
	}
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

func cloneMessage(message Message) Message {
	message.Body = cloneBytes(message.Body)
	return message
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	result := make([]Message, len(messages))
	for i, message := range messages {
		result[i] = cloneMessage(message)
	}
	return result
}
