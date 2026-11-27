// Package task arbitrates the target side of an iSCSI session: login
// negotiation, the per connection StatSN waterline and the per session CmdSN
// window that gates how many commands may be outstanding at once.
package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Status is the SCSI status carried by one command response.
type Status uint8

// SCSI status values exposed by Execute.
const (
	StatusGood           Status = 0x00
	StatusCheckCondition Status = 0x02
	StatusBusy           Status = 0x08
	StatusReject         Status = 0x10
)

// Login phases reported by SessionInfo.Phase.
const (
	PhaseLogin     = "login"
	PhaseFull      = "full-feature"
	PhaseFailed    = "failed"
	PhaseLoggedOut = "logged-out"
)

// Errors returned by the guard.
var (
	ErrBadLogin        = errors.New("initiator, target, portal and connection ids are required")
	ErrLoginRejected   = errors.New("login parameter is not accepted")
	ErrSessionLimit    = errors.New("session limit reached for this target")
	ErrUnknownSession  = errors.New("unknown session")
	ErrUnknownConn     = errors.New("unknown connection")
	ErrBadCommand      = errors.New("command is not usable")
	ErrWindowExhausted = errors.New("cmdsn window is exhausted")
	ErrCmdSNInvalid    = errors.New("cmdsn is outside the session window")
	ErrBadSnapshot     = errors.New("snapshot payload is invalid")
)

// Limits bounds the live tables of the guard.
type Limits struct {
	MaxSessions        int
	MaxCmdWindow       uint32
	MaxConnsPerSession int
}

// LoginRequest is the operational parameter set offered at login.
type LoginRequest struct {
	Initiator string
	Target    string
	Portal    string
	ConnIDs   []string
	Params    map[string]string
}

// LoginResult is the outcome of one successful login.
type LoginResult struct {
	SessionID string
	Status    Status
	CmdSN     uint32
	TextKeys  []string
}

// Command is one SCSI command submitted against a session.
type Command struct {
	ITT       uint32
	ConnID    string
	Immediate bool
	Read      bool
	Payload   []byte
}

// Response is the recorded answer of one command.
type Response struct {
	ITT       uint32
	ConnID    string
	CmdSN     uint32
	StatSN    uint32
	Status    Status
	Immediate bool
}

// SessionInfo is the observable state of one session.
type SessionInfo struct {
	ID          string
	Initiator   string
	Target      string
	Phase       string
	NextCmdSN   uint32
	ExpCmdSN    uint32
	ConnIDs     []string
	Immediate   uint32
	DigestDrops uint32
}

// ConnInfo is the observable state of one connection.
type ConnInfo struct {
	ID        string
	SessionID string
	StatSN    uint32
	Lost      bool
	Reason    string
}

// TaskFn is the body run by TaskManage once its CmdSN is allocated.
type TaskFn func(assignedCmdSN uint32) error

type sessionState struct {
	id          string
	initiator   string
	target      string
	portal      string
	phase       string
	nextCmdSN   uint32
	expCmdSN    uint32
	connIDs     []string
	immediate   uint32
	digestDrops uint32
	responses   map[uint32]*Response
	byCmdSN     map[uint32]*Response
	attempts    map[uint32]int
}

type connState struct {
	id        string
	sessionID string
	statSN    uint32
	lost      bool
	reason    string
	responses []*Response
	streamAt  int
}

// IscsiSessionGuardP3 owns the session table, the command window and the
// per connection response stream.
type IscsiSessionGuardP3 struct {
	mu            sync.Mutex
	taskMu        sync.Mutex
	limits        Limits
	sessions      map[string]*sessionState
	conns         map[string]*connState
	nextSessionID uint64
}

// NewIscsiSessionGuardP3 builds a guard with the given caps.
func NewIscsiSessionGuardP3(limits Limits) *IscsiSessionGuardP3 {
	return &IscsiSessionGuardP3{
		limits:        limits,
		sessions:      make(map[string]*sessionState),
		conns:         make(map[string]*connState),
		nextSessionID: 1,
	}
}

func acceptedLoginParams(params map[string]string) error {
	if auth, ok := params["AuthMethod"]; ok {
		if auth != "None" && auth != "CHAP" {
			return ErrLoginRejected
		}
	}
	for key, value := range params {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return ErrLoginRejected
		}
	}
	return nil
}

func phaseValid(phase string) bool {
	switch phase {
	case PhaseLogin, PhaseFull, PhaseFailed, PhaseLoggedOut:
		return true
	default:
		return false
	}
}

func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// Login negotiates the operational parameters and opens a session.
func (g *IscsiSessionGuardP3) Login(ctx context.Context, req LoginRequest) (LoginResult, error) {
	_ = ctx
	if strings.TrimSpace(req.Initiator) == "" || strings.TrimSpace(req.Target) == "" || strings.TrimSpace(req.Portal) == "" {
		return LoginResult{}, ErrBadLogin
	}
	if len(req.ConnIDs) == 0 {
		return LoginResult{}, ErrBadLogin
	}
	seen := make(map[string]struct{}, len(req.ConnIDs))
	for _, id := range req.ConnIDs {
		if strings.TrimSpace(id) == "" {
			return LoginResult{}, ErrBadLogin
		}
		if _, dup := seen[id]; dup {
			return LoginResult{}, ErrBadLogin
		}
		seen[id] = struct{}{}
	}
	if g.limits.MaxConnsPerSession <= 0 || len(req.ConnIDs) > g.limits.MaxConnsPerSession {
		return LoginResult{}, ErrBadLogin
	}
	if err := acceptedLoginParams(req.Params); err != nil {
		return LoginResult{}, err
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	targetSessions := 0
	for _, s := range g.sessions {
		if s.target == req.Target {
			targetSessions++
		}
	}
	if g.limits.MaxSessions <= 0 || targetSessions >= g.limits.MaxSessions {
		return LoginResult{}, ErrSessionLimit
	}
	for _, id := range req.ConnIDs {
		if _, exists := g.conns[id]; exists {
			return LoginResult{}, ErrBadLogin
		}
	}

	sessionID := fmt.Sprintf("session-%d", g.nextSessionID)
	g.nextSessionID++

	s := &sessionState{
		id:        sessionID,
		initiator: req.Initiator,
		target:    req.Target,
		portal:    req.Portal,
		phase:     PhaseFull,
		nextCmdSN: 1,
		expCmdSN:  1,
		connIDs:   copyStrings(req.ConnIDs),
		responses: make(map[uint32]*Response),
		byCmdSN:   make(map[uint32]*Response),
		attempts:  make(map[uint32]int),
	}
	g.sessions[sessionID] = s
	for _, id := range req.ConnIDs {
		g.conns[id] = &connState{id: id, sessionID: sessionID}
	}

	keys := make([]string, 0, len(req.Params))
	for key := range req.Params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return LoginResult{
		SessionID: sessionID,
		Status:    StatusGood,
		CmdSN:     1,
		TextKeys:  keys,
	}, nil
}

// Execute places one command in the session window and returns its status.
func (g *IscsiSessionGuardP3) Execute(sessionID string, cmd Command) (Status, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.sessions[sessionID]
	if !ok {
		return StatusReject, ErrUnknownSession
	}
	if cmd.ITT == 0 {
		return StatusReject, ErrBadCommand
	}
	conn, ok := g.conns[cmd.ConnID]
	if !ok || conn.sessionID != sessionID || conn.lost {
		return StatusReject, ErrUnknownConn
	}
	if _, done := s.responses[cmd.ITT]; done {
		return StatusBusy, nil
	}

	var cmdSN uint32
	if !cmd.Immediate {
		if s.nextCmdSN-s.expCmdSN >= g.limits.MaxCmdWindow {
			return StatusReject, ErrWindowExhausted
		}
		cmdSN = s.nextCmdSN
		s.nextCmdSN++
	} else {
		s.immediate++
	}

	conn.statSN++
	resp := &Response{
		ITT:       cmd.ITT,
		ConnID:    cmd.ConnID,
		CmdSN:     cmdSN,
		StatSN:    conn.statSN,
		Status:    StatusGood,
		Immediate: cmd.Immediate,
	}
	s.responses[cmd.ITT] = resp
	s.attempts[cmd.ITT]++
	if !cmd.Immediate {
		s.byCmdSN[cmdSN] = resp
	}
	conn.responses = append(conn.responses, resp)
	return StatusGood, nil
}

// TaskManage appends a task after the referenced CmdSN.
func (g *IscsiSessionGuardP3) TaskManage(sessionID string, refCmdSN uint32, fn TaskFn) error {
	g.taskMu.Lock()
	defer g.taskMu.Unlock()

	g.mu.Lock()
	s, ok := g.sessions[sessionID]
	if !ok {
		g.mu.Unlock()
		return ErrUnknownSession
	}
	if fn == nil {
		g.mu.Unlock()
		return ErrBadCommand
	}
	if refCmdSN == 0 || refCmdSN > s.nextCmdSN {
		g.mu.Unlock()
		return ErrCmdSNInvalid
	}
	if s.nextCmdSN-s.expCmdSN >= g.limits.MaxCmdWindow {
		g.mu.Unlock()
		return ErrWindowExhausted
	}
	assigned := s.nextCmdSN
	s.nextCmdSN++
	g.mu.Unlock()

	return fn(assigned)
}

// Ack acknowledges every command below expCmdSN on the session.
func (g *IscsiSessionGuardP3) Ack(sessionID string, expCmdSN uint32) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.sessions[sessionID]
	if !ok {
		return ErrUnknownSession
	}
	if expCmdSN < s.expCmdSN || expCmdSN > s.nextCmdSN {
		return ErrCmdSNInvalid
	}
	s.expCmdSN = expCmdSN
	return nil
}

func maxConnStatSN(conn *connState) uint32 {
	var max uint32
	for _, r := range conn.responses[conn.streamAt:] {
		if r.StatSN > max {
			max = r.StatSN
		}
	}
	return max
}

func removeConnResponse(conn *connState, target *Response) (int, bool) {
	for i, r := range conn.responses {
		if r == target {
			copy(conn.responses[i:], conn.responses[i+1:])
			conn.responses[len(conn.responses)-1] = nil
			conn.responses = conn.responses[:len(conn.responses)-1]
			return i, true
		}
	}
	return 0, false
}

// DigestFailed drops the PDU that failed its digest check on a connection.
func (g *IscsiSessionGuardP3) DigestFailed(connID string, cmdSN uint32) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[connID]
	if !ok {
		return ErrUnknownConn
	}
	s, ok := g.sessions[conn.sessionID]
	if !ok {
		return ErrUnknownConn
	}
	s.digestDrops++

	resp := s.byCmdSN[cmdSN]
	if resp == nil || resp.ConnID != connID {
		return nil
	}
	delete(s.byCmdSN, cmdSN)
	if current, exists := s.responses[resp.ITT]; exists && current == resp {
		delete(s.responses, resp.ITT)
	}
	if index, removed := removeConnResponse(conn, resp); removed && index < conn.streamAt {
		conn.streamAt--
	}
	conn.statSN = maxConnStatSN(conn)
	if cmdSN >= s.expCmdSN && cmdSN+1 == s.nextCmdSN {
		s.nextCmdSN = cmdSN
	}
	return nil
}

// ConnectionLost marks a connection unusable and records the reason.
func (g *IscsiSessionGuardP3) ConnectionLost(connID, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[connID]
	if !ok {
		return ErrUnknownConn
	}
	conn.lost = true
	conn.reason = reason
	return nil
}

// Reconnect brings a lost connection back on its session.
func (g *IscsiSessionGuardP3) Reconnect(sessionID, connID string) (ConnInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.sessions[sessionID]; !ok {
		return ConnInfo{}, ErrUnknownSession
	}
	conn, ok := g.conns[connID]
	if !ok || conn.sessionID != sessionID {
		return ConnInfo{}, ErrUnknownConn
	}
	conn.lost = false
	conn.reason = ""
	conn.statSN = 0
	conn.streamAt = len(conn.responses)
	return ConnInfo{ID: conn.id, SessionID: conn.sessionID, StatSN: conn.statSN, Lost: conn.lost, Reason: conn.reason}, nil
}

// Session reads one session record.
func (g *IscsiSessionGuardP3) Session(id string) (SessionInfo, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.sessions[id]
	if !ok {
		return SessionInfo{}, false
	}
	return SessionInfo{
		ID:          s.id,
		Initiator:   s.initiator,
		Target:      s.target,
		Phase:       s.phase,
		NextCmdSN:   s.nextCmdSN,
		ExpCmdSN:    s.expCmdSN,
		ConnIDs:     copyStrings(s.connIDs),
		Immediate:   s.immediate,
		DigestDrops: s.digestDrops,
	}, true
}

// Conn reads one connection record.
func (g *IscsiSessionGuardP3) Conn(id string) (ConnInfo, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[id]
	if !ok {
		return ConnInfo{}, false
	}
	return ConnInfo{
		ID:        conn.id,
		SessionID: conn.sessionID,
		StatSN:    conn.statSN,
		Lost:      conn.lost,
		Reason:    conn.reason,
	}, true
}

// Response returns the recorded answer of one ITT.
func (g *IscsiSessionGuardP3) Response(sessionID string, itt uint32) (Response, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.sessions[sessionID]
	if !ok {
		return Response{}, false
	}
	r, ok := s.responses[itt]
	if !ok {
		return Response{}, false
	}
	return *r, true
}

// Responses lists the response stream of one connection in StatSN order.
func (g *IscsiSessionGuardP3) Responses(connID string) []Response {
	g.mu.Lock()
	defer g.mu.Unlock()

	conn, ok := g.conns[connID]
	if !ok {
		return nil
	}
	out := make([]Response, len(conn.responses))
	for i, r := range conn.responses {
		out[i] = *r
	}
	return out
}

// Attempts reports how many times the command body of an ITT really ran.
func (g *IscsiSessionGuardP3) Attempts(sessionID string, itt uint32) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, ok := g.sessions[sessionID]
	if !ok {
		return 0
	}
	return s.attempts[itt]
}

type snapshotITT struct {
	ITT   uint32 `json:"itt"`
	Count int    `json:"count"`
}

type snapshotSession struct {
	ID          string        `json:"id"`
	Initiator   string        `json:"initiator"`
	Target      string        `json:"target"`
	Portal      string        `json:"portal"`
	Phase       string        `json:"phase"`
	NextCmdSN   uint32        `json:"nextCmdSN"`
	ExpCmdSN    uint32        `json:"expCmdSN"`
	ConnIDs     []string      `json:"connIDs"`
	Immediate   uint32        `json:"immediate"`
	DigestDrops uint32        `json:"digestDrops"`
	Responses   []Response    `json:"responses"`
	ITTs        []snapshotITT `json:"ittRecords"`
}

type snapshotConn struct {
	ID          string   `json:"id"`
	SessionID   string   `json:"sessionID"`
	StatSN      uint32   `json:"statSN"`
	Lost        bool     `json:"lost"`
	Reason      string   `json:"reason"`
	StreamAt    int      `json:"streamAt"`
	ResponseITT []uint32 `json:"responseITTs"`
}

type snapshotDoc struct {
	Version       int               `json:"version"`
	NextSessionID uint64            `json:"nextSessionID"`
	Sessions      []snapshotSession `json:"sessions"`
	Connections   []snapshotConn    `json:"connections"`
	Checksum      string            `json:"checksum"`
}

func checksumDoc(doc snapshotDoc) string {
	doc.Checksum = ""
	body, err := json.Marshal(doc)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Snapshot serialises sessions, connections, responses and ITT records.
func (g *IscsiSessionGuardP3) Snapshot() ([]byte, error) {
	g.mu.Lock()
	doc := snapshotDoc{
		Version:       1,
		NextSessionID: g.nextSessionID,
		Sessions:      make([]snapshotSession, 0, len(g.sessions)),
		Connections:   make([]snapshotConn, 0, len(g.conns)),
	}
	sessionIDs := make([]string, 0, len(g.sessions))
	for id := range g.sessions {
		sessionIDs = append(sessionIDs, id)
	}
	sort.Strings(sessionIDs)
	for _, id := range sessionIDs {
		s := g.sessions[id]
		ss := snapshotSession{
			ID:          s.id,
			Initiator:   s.initiator,
			Target:      s.target,
			Portal:      s.portal,
			Phase:       s.phase,
			NextCmdSN:   s.nextCmdSN,
			ExpCmdSN:    s.expCmdSN,
			ConnIDs:     copyStrings(s.connIDs),
			Immediate:   s.immediate,
			DigestDrops: s.digestDrops,
			Responses:   make([]Response, 0, len(s.responses)),
		}
		itts := make([]uint32, 0, len(s.attempts))
		for itt := range s.attempts {
			itts = append(itts, itt)
		}
		sort.Slice(itts, func(i, j int) bool { return itts[i] < itts[j] })
		for _, itt := range itts {
			ss.ITTs = append(ss.ITTs, snapshotITT{ITT: itt, Count: s.attempts[itt]})
		}
		for _, r := range s.responses {
			ss.Responses = append(ss.Responses, *r)
		}
		sort.Slice(ss.Responses, func(i, j int) bool {
			if ss.Responses[i].CmdSN != ss.Responses[j].CmdSN {
				return ss.Responses[i].CmdSN < ss.Responses[j].CmdSN
			}
			return ss.Responses[i].ITT < ss.Responses[j].ITT
		})
		doc.Sessions = append(doc.Sessions, ss)
	}
	connIDs := make([]string, 0, len(g.conns))
	for id := range g.conns {
		connIDs = append(connIDs, id)
	}
	sort.Strings(connIDs)
	for _, id := range connIDs {
		conn := g.conns[id]
		sc := snapshotConn{
			ID:          conn.id,
			SessionID:   conn.sessionID,
			StatSN:      conn.statSN,
			Lost:        conn.lost,
			Reason:      conn.reason,
			StreamAt:    conn.streamAt,
			ResponseITT: make([]uint32, 0, len(conn.responses)),
		}
		for _, r := range conn.responses {
			sc.ResponseITT = append(sc.ResponseITT, r.ITT)
		}
		doc.Connections = append(doc.Connections, sc)
	}
	g.mu.Unlock()

	doc.Checksum = checksumDoc(doc)
	return json.Marshal(doc)
}

func validateSnapshot(doc snapshotDoc, limits Limits) error {
	if doc.Version != 1 || doc.NextSessionID == 0 || doc.Checksum == "" || doc.Checksum != checksumDoc(doc) {
		return ErrBadSnapshot
	}

	perTarget := make(map[string]int)
	sessions := make(map[string]*sessionState, len(doc.Sessions))
	conns := make(map[string]*connState, len(doc.Connections))
	for _, ss := range doc.Sessions {
		if ss.ID == "" || ss.ID != strings.TrimSpace(ss.ID) || strings.TrimSpace(ss.Initiator) == "" || strings.TrimSpace(ss.Target) == "" || strings.TrimSpace(ss.Portal) == "" {
			return ErrBadSnapshot
		}
		if _, dup := sessions[ss.ID]; dup {
			return ErrBadSnapshot
		}
		if !phaseValid(ss.Phase) || ss.NextCmdSN == 0 || ss.ExpCmdSN == 0 || ss.ExpCmdSN > ss.NextCmdSN {
			return ErrBadSnapshot
		}
		if ss.NextCmdSN-ss.ExpCmdSN > limits.MaxCmdWindow {
			return ErrBadSnapshot
		}
		if len(ss.ConnIDs) == 0 || limits.MaxConnsPerSession <= 0 || len(ss.ConnIDs) > limits.MaxConnsPerSession {
			return ErrBadSnapshot
		}
		seenConns := make(map[string]struct{}, len(ss.ConnIDs))
		for _, id := range ss.ConnIDs {
			if id == "" || strings.TrimSpace(id) == "" {
				return ErrBadSnapshot
			}
			if _, dup := seenConns[id]; dup {
				return ErrBadSnapshot
			}
			seenConns[id] = struct{}{}
		}
		perTarget[ss.Target]++
		if limits.MaxSessions <= 0 || perTarget[ss.Target] > limits.MaxSessions {
			return ErrBadSnapshot
		}

		s := &sessionState{
			id:          ss.ID,
			initiator:   ss.Initiator,
			target:      ss.Target,
			portal:      ss.Portal,
			phase:       ss.Phase,
			nextCmdSN:   ss.NextCmdSN,
			expCmdSN:    ss.ExpCmdSN,
			connIDs:     copyStrings(ss.ConnIDs),
			immediate:   ss.Immediate,
			digestDrops: ss.DigestDrops,
			responses:   make(map[uint32]*Response, len(ss.Responses)),
			byCmdSN:     make(map[uint32]*Response, len(ss.Responses)),
			attempts:    make(map[uint32]int, len(ss.ITTs)),
		}
		for _, record := range ss.ITTs {
			if record.ITT == 0 || record.Count < 1 {
				return ErrBadSnapshot
			}
			if _, dup := s.attempts[record.ITT]; dup {
				return ErrBadSnapshot
			}
			s.attempts[record.ITT] = record.Count
		}
		for _, r := range ss.Responses {
			if r.ITT == 0 || r.ConnID == "" {
				return ErrBadSnapshot
			}
			if _, member := seenConns[r.ConnID]; !member {
				return ErrBadSnapshot
			}
			if r.Immediate {
				if r.CmdSN != 0 {
					return ErrBadSnapshot
				}
			} else {
				if r.CmdSN == 0 || r.CmdSN >= ss.NextCmdSN {
					return ErrBadSnapshot
				}
				if _, dup := s.byCmdSN[r.CmdSN]; dup {
					return ErrBadSnapshot
				}
			}
			if _, dup := s.responses[r.ITT]; dup {
				return ErrBadSnapshot
			}
			copied := r
			s.responses[r.ITT] = &copied
			if !r.Immediate {
				s.byCmdSN[r.CmdSN] = &copied
			}
		}
		sessions[ss.ID] = s
	}

	expectedByConn := make(map[string]map[uint32]struct{}, len(sessions))
	for _, s := range sessions {
		for _, r := range s.responses {
			set := expectedByConn[r.ConnID]
			if set == nil {
				set = make(map[uint32]struct{})
				expectedByConn[r.ConnID] = set
			}
			set[r.ITT] = struct{}{}
		}
	}

	for _, sc := range doc.Connections {
		if sc.ID == "" || strings.TrimSpace(sc.ID) == "" || sc.SessionID == "" || sc.StreamAt < 0 || sc.StreamAt > len(sc.ResponseITT) {
			return ErrBadSnapshot
		}
		if _, dup := conns[sc.ID]; dup {
			return ErrBadSnapshot
		}
		if _, ok := sessions[sc.SessionID]; !ok {
			return ErrBadSnapshot
		}
		s := sessions[sc.SessionID]
		listed := false
		for _, id := range s.connIDs {
			if id == sc.ID {
				listed = true
				break
			}
		}
		if !listed {
			return ErrBadSnapshot
		}
		conn := &connState{
			id:        sc.ID,
			sessionID: sc.SessionID,
			statSN:    sc.StatSN,
			lost:      sc.Lost,
			reason:    sc.Reason,
			streamAt:  sc.StreamAt,
		}
		seenITT := make(map[uint32]struct{}, len(sc.ResponseITT))
		for _, itt := range sc.ResponseITT {
			s := sessions[sc.SessionID]
			r, ok := s.responses[itt]
			if !ok || r.ConnID != sc.ID {
				return ErrBadSnapshot
			}
			if _, dup := seenITT[itt]; dup {
				return ErrBadSnapshot
			}
			seenITT[itt] = struct{}{}
			conn.responses = append(conn.responses, r)
		}
		if expected := expectedByConn[sc.ID]; len(expected) != len(seenITT) {
			return ErrBadSnapshot
		}
		conns[sc.ID] = conn
	}

	connCount := 0
	for _, s := range sessions {
		for _, id := range s.connIDs {
			conn, ok := conns[id]
			if !ok || conn.sessionID != s.id {
				return ErrBadSnapshot
			}
			connCount++
		}
	}
	if connCount != len(conns) {
		return ErrBadSnapshot
	}
	for id := range expectedByConn {
		if _, ok := conns[id]; !ok {
			return ErrBadSnapshot
		}
	}
	return nil
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *IscsiSessionGuardP3) Restore(data []byte) error {
	var doc snapshotDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return ErrBadSnapshot
	}
	doc.Checksum = strings.TrimSpace(doc.Checksum)

	g.mu.Lock()
	limits := g.limits
	g.mu.Unlock()
	if err := validateSnapshot(doc, limits); err != nil {
		return ErrBadSnapshot
	}

	sessions := make(map[string]*sessionState, len(doc.Sessions))
	conns := make(map[string]*connState, len(doc.Connections))
	for _, ss := range doc.Sessions {
		s := &sessionState{
			id:          ss.ID,
			initiator:   ss.Initiator,
			target:      ss.Target,
			portal:      ss.Portal,
			phase:       ss.Phase,
			nextCmdSN:   ss.NextCmdSN,
			expCmdSN:    ss.ExpCmdSN,
			connIDs:     copyStrings(ss.ConnIDs),
			immediate:   ss.Immediate,
			digestDrops: ss.DigestDrops,
			responses:   make(map[uint32]*Response, len(ss.Responses)),
			byCmdSN:     make(map[uint32]*Response, len(ss.Responses)),
			attempts:    make(map[uint32]int, len(ss.ITTs)),
		}
		for _, record := range ss.ITTs {
			s.attempts[record.ITT] = record.Count
		}
		for _, r := range ss.Responses {
			copied := r
			s.responses[r.ITT] = &copied
			if !r.Immediate {
				s.byCmdSN[r.CmdSN] = &copied
			}
		}
		sessions[ss.ID] = s
	}
	for _, sc := range doc.Connections {
		s := sessions[sc.SessionID]
		conn := &connState{
			id:        sc.ID,
			sessionID: sc.SessionID,
			statSN:    sc.StatSN,
			lost:      sc.Lost,
			reason:    sc.Reason,
			streamAt:  sc.StreamAt,
			responses: make([]*Response, 0, len(sc.ResponseITT)),
		}
		for _, itt := range sc.ResponseITT {
			conn.responses = append(conn.responses, s.responses[itt])
		}
		conns[sc.ID] = conn
	}
	nextSessionID := doc.NextSessionID
	for id := range sessions {
		if strings.HasPrefix(id, "session-") {
			n, err := strconv.ParseUint(strings.TrimPrefix(id, "session-"), 10, 64)
			if err == nil && n >= nextSessionID {
				nextSessionID = n + 1
			}
		}
	}

	g.mu.Lock()
	g.sessions = sessions
	g.conns = conns
	g.nextSessionID = nextSessionID
	g.mu.Unlock()
	return nil
}
