package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Session states.
const (
	SessionPending = "pending"
	SessionActive  = "active"
)

// Guard errors.
var (
	ErrInvalidLogin       = errors.New("portal, initiator and target are required")
	ErrUnknownSession     = errors.New("unknown session")
	ErrSessionLimit       = errors.New("session limit reached")
	ErrChallengeMismatch  = errors.New("chap challenge does not match the session")
	ErrChallengeReused    = errors.New("chap challenge was already consumed")
	ErrMutualAuthRequired = errors.New("target side chap answer is required")
	ErrAuthLocked         = errors.New("chap attempt window exhausted")
	ErrNotAuthenticated   = errors.New("session has not completed the login stages")
	ErrInitiatorMismatch  = errors.New("initiator does not own this session")
	ErrLunDenied          = errors.New("lun is not inside the initiator mask")
	ErrLunLimit           = errors.New("per session lun limit reached")
	ErrInvalidMask        = errors.New("lun mask is invalid")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// LoginRequest is the first stage of an iSCSI login.
type LoginRequest struct {
	Initiator  string `json:"initiator"`
	Target     string `json:"target"`
	CHAPMethod string `json:"chap_method"`
}

// ChapResponse answers the challenge issued at login.
type ChapResponse struct {
	ChallengeID string `json:"challenge_id"`
	Response    string `json:"response"`
	Mutual      bool   `json:"mutual"`
}

// Session is one negotiated iSCSI session.
type Session struct {
	ID          string `json:"id"`
	Portal      string `json:"portal"`
	Initiator   string `json:"initiator"`
	Target      string `json:"target"`
	State       string `json:"state"`
	ChallengeID string `json:"challenge_id"`
	ExpCmdSN    int    `json:"exp_cmd_sn"`
	Commands    int    `json:"commands"`
	Luns        int    `json:"luns"`
}

// LunGrant is the outcome of a successful LUN mapping.
type LunGrant struct {
	SessionID      string `json:"session_id"`
	Lun            int    `json:"lun"`
	Initiator      string `json:"initiator"`
	MaskGeneration int    `json:"mask_generation"`
}

// IscsiPolicy bounds one guard.
type IscsiPolicy struct {
	MaxSessions             int
	MaxSessionsPerInitiator int
	MaxLunsPerSession       int
	MaxMaskEntries          int
	ChapWindow              int
	RequireMutualAuth       bool
	MaskGeneration          int
}

// sessionState is the internal mutable view of one session.
type sessionState struct {
	Session
	attempts   int
	consumed   bool
	locked     bool
	lunSet     map[int]struct{}
	mask       []int
	maskGen    int
	lastCmdSN  int
	hasCommand bool
}

// IscsiSessionGuard owns the session table and the per initiator LUN masks.
type IscsiSessionGuard struct {
	mu             sync.Mutex
	cfg            IscsiPolicy
	sessions       map[string]*sessionState
	masks          map[string][]int
	maskGeneration int
	nextID         int
}

// NewIscsiSessionGuard builds a guard with the given policy.
func NewIscsiSessionGuard(cfg IscsiPolicy) *IscsiSessionGuard {
	return &IscsiSessionGuard{
		cfg:            cfg,
		sessions:       make(map[string]*sessionState),
		masks:          make(map[string][]int),
		maskGeneration: cfg.MaskGeneration,
		nextID:         1,
	}
}

// normalizeInitiator folds case and trims trailing/leading blanks so IQN
// comparison never depends on the raw wire string.
func normalizeInitiator(iqn string) string {
	return strings.ToLower(strings.TrimSpace(iqn))
}

// Login opens a session in the pending state and issues a CHAP challenge.
func (g *IscsiSessionGuard) Login(portal string, req LoginRequest) (Session, error) {
	portal = strings.TrimSpace(portal)
	initiator := normalizeInitiator(req.Initiator)
	target := strings.TrimSpace(req.Target)
	if portal == "" || initiator == "" || target == "" {
		return Session{}, ErrInvalidLogin
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.sessions) >= g.cfg.MaxSessions {
		return Session{}, ErrSessionLimit
	}
	perInitiator := 0
	for _, s := range g.sessions {
		if s.Initiator == initiator {
			perInitiator++
		}
	}
	if perInitiator >= g.cfg.MaxSessionsPerInitiator {
		return Session{}, ErrSessionLimit
	}

	id := fmt.Sprintf("sess-%06d", g.nextID)
	g.nextID++
	mask := append([]int(nil), g.masks[initiator]...)
	sess := &sessionState{
		Session: Session{
			ID:          id,
			Portal:      portal,
			Initiator:   initiator,
			Target:      target,
			State:       SessionPending,
			ChallengeID: fmt.Sprintf("chal-%06d", g.nextID),
		},
		lunSet:  make(map[int]struct{}),
		mask:    mask,
		maskGen: g.maskGeneration,
	}
	g.sessions[id] = sess
	return sess.Session, nil
}

// Auth completes the CHAP handshake, including the mutual answer when the
// policy requires it.
func (g *IscsiSessionGuard) Auth(sessionID string, chap ChapResponse) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	sess, ok := g.sessions[sessionID]
	if !ok {
		return ErrUnknownSession
	}
	if sess.locked {
		return ErrAuthLocked
	}
	if sess.consumed {
		return ErrChallengeReused
	}
	fail := func(err error) error {
		sess.attempts++
		if sess.attempts >= g.cfg.ChapWindow {
			sess.locked = true
		}
		return err
	}
	if chap.ChallengeID != sess.ChallengeID {
		return fail(ErrChallengeMismatch)
	}
	if g.cfg.RequireMutualAuth && !chap.Mutual {
		return fail(ErrMutualAuthRequired)
	}
	sess.consumed = true
	sess.State = SessionActive
	return nil
}

// MapLun grants one LUN to the session when the initiator mask allows it.
func (g *IscsiSessionGuard) MapLun(sessionID string, lun int, initiator string) (LunGrant, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	sess, ok := g.sessions[sessionID]
	if !ok {
		return LunGrant{}, ErrUnknownSession
	}
	if sess.State != SessionActive {
		return LunGrant{}, ErrNotAuthenticated
	}
	if normalizeInitiator(initiator) != sess.Initiator {
		return LunGrant{}, ErrInitiatorMismatch
	}
	grant := LunGrant{
		SessionID:      sess.ID,
		Lun:            lun,
		Initiator:      sess.Initiator,
		MaskGeneration: sess.maskGen,
	}
	if _, held := sess.lunSet[lun]; held {
		return grant, nil
	}
	if !lunInMask(sess.mask, lun) {
		return LunGrant{}, ErrLunDenied
	}
	if len(sess.lunSet) >= g.cfg.MaxLunsPerSession {
		return LunGrant{}, ErrLunLimit
	}
	sess.lunSet[lun] = struct{}{}
	sess.Luns = len(sess.lunSet)
	return grant, nil
}

func lunInMask(mask []int, lun int) bool {
	i := sort.SearchInts(mask, lun)
	return i < len(mask) && mask[i] == lun
}

// Recover advances the command window and replays a duplicate CmdSN
// idempotently. A regressed CmdSN means the initiator rebooted, so the
// stale session is invalidated instead of being reused.
func (g *IscsiSessionGuard) Recover(sessionID string, cmdSN int) (Session, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	sess, ok := g.sessions[sessionID]
	if !ok {
		return Session{}, ErrUnknownSession
	}
	if sess.State != SessionActive {
		return Session{}, ErrNotAuthenticated
	}
	if sess.hasCommand {
		if cmdSN == sess.lastCmdSN {
			return sess.Session, nil
		}
		if cmdSN < sess.lastCmdSN {
			delete(g.sessions, sessionID)
			return Session{}, ErrUnknownSession
		}
	}
	sess.hasCommand = true
	sess.lastCmdSN = cmdSN
	sess.Commands++
	sess.ExpCmdSN = cmdSN + 1
	return sess.Session, nil
}

// Logout closes a session and releases its LUNs.
func (g *IscsiSessionGuard) Logout(sessionID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.sessions[sessionID]; !ok {
		return ErrUnknownSession
	}
	delete(g.sessions, sessionID)
	return nil
}

// Sessions lists the live sessions ordered by session id.
func (g *IscsiSessionGuard) Sessions() []Session {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]Session, 0, len(g.sessions))
	for _, sess := range g.sessions {
		out = append(out, sess.Session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// LunMask lists the masked LUNs of one initiator in ascending order.
func (g *IscsiSessionGuard) LunMask(initiator string) []int {
	g.mu.Lock()
	defer g.mu.Unlock()

	mask, ok := g.masks[normalizeInitiator(initiator)]
	if !ok {
		return nil
	}
	return append([]int(nil), mask...)
}

// SetLunMask replaces the mask of one initiator and bumps the generation.
func (g *IscsiSessionGuard) SetLunMask(initiator string, luns []int) error {
	initiator = normalizeInitiator(initiator)
	if initiator == "" {
		return ErrInvalidMask
	}
	seen := make(map[int]struct{}, len(luns))
	for _, lun := range luns {
		if lun < 0 {
			return ErrInvalidMask
		}
		seen[lun] = struct{}{}
	}
	if len(seen) > g.cfg.MaxMaskEntries {
		return ErrInvalidMask
	}
	mask := make([]int, 0, len(seen))
	for lun := range seen {
		mask = append(mask, lun)
	}
	sort.Ints(mask)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.masks[initiator] = mask
	g.maskGeneration++
	return nil
}

// MaskGeneration reports the current mask generation.
func (g *IscsiSessionGuard) MaskGeneration() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maskGeneration
}

// snapshotSession is the persisted form of one session.
type snapshotSession struct {
	Session
	Attempts   int   `json:"attempts"`
	Consumed   bool  `json:"consumed"`
	Locked     bool  `json:"locked"`
	LunList    []int `json:"lun_list"`
	Mask       []int `json:"mask"`
	MaskGen    int   `json:"mask_gen"`
	LastCmdSN  int   `json:"last_cmd_sn"`
	HasCommand bool  `json:"has_command"`
}

// snapshot is the persisted form of the whole guard.
type snapshot struct {
	Version        int               `json:"version"`
	Sessions       []snapshotSession `json:"sessions"`
	Masks          map[string][]int  `json:"masks"`
	MaskGeneration int               `json:"mask_generation"`
	NextID         int               `json:"next_id"`
}

const snapshotVersion = 1

// Snapshot serialises the session table, the masks and the command watermarks.
func (g *IscsiSessionGuard) Snapshot() ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	snap := snapshot{
		Version:        snapshotVersion,
		Masks:          make(map[string][]int, len(g.masks)),
		MaskGeneration: g.maskGeneration,
		NextID:         g.nextID,
	}
	ids := make([]string, 0, len(g.sessions))
	for id := range g.sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sess := g.sessions[id]
		luns := make([]int, 0, len(sess.lunSet))
		for lun := range sess.lunSet {
			luns = append(luns, lun)
		}
		sort.Ints(luns)
		snap.Sessions = append(snap.Sessions, snapshotSession{
			Session:    sess.Session,
			Attempts:   sess.attempts,
			Consumed:   sess.consumed,
			Locked:     sess.locked,
			LunList:    luns,
			Mask:       append([]int(nil), sess.mask...),
			MaskGen:    sess.maskGen,
			LastCmdSN:  sess.lastCmdSN,
			HasCommand: sess.hasCommand,
		})
	}
	for initiator, mask := range g.masks {
		snap.Masks[initiator] = append([]int(nil), mask...)
	}
	return json.Marshal(snap)
}

// Restore replaces the whole state with a snapshot, all or nothing. Command
// watermarks never move backwards: a session that already exists locally
// keeps the higher watermark.
func (g *IscsiSessionGuard) Restore(data []byte) error {
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ErrBadSnapshot
	}
	if snap.Version != snapshotVersion || snap.NextID < 1 || snap.MaskGeneration < 0 {
		return ErrBadSnapshot
	}

	sessions := make(map[string]*sessionState, len(snap.Sessions))
	perInitiator := make(map[string]int)
	for _, ss := range snap.Sessions {
		if ss.ID == "" || ss.Initiator == "" {
			return ErrBadSnapshot
		}
		if _, dup := sessions[ss.ID]; dup {
			return ErrBadSnapshot
		}
		if ss.State != SessionPending && ss.State != SessionActive {
			return ErrBadSnapshot
		}
		if len(ss.LunList) > g.cfg.MaxLunsPerSession || len(ss.Mask) > g.cfg.MaxMaskEntries {
			return ErrBadSnapshot
		}
		perInitiator[ss.Initiator]++
		if perInitiator[ss.Initiator] > g.cfg.MaxSessionsPerInitiator {
			return ErrBadSnapshot
		}
		lunSet := make(map[int]struct{}, len(ss.LunList))
		for _, lun := range ss.LunList {
			if lun < 0 {
				return ErrBadSnapshot
			}
			lunSet[lun] = struct{}{}
		}
		sess := &sessionState{
			Session:    ss.Session,
			attempts:   ss.Attempts,
			consumed:   ss.Consumed,
			locked:     ss.Locked,
			lunSet:     lunSet,
			mask:       append([]int(nil), ss.Mask...),
			maskGen:    ss.MaskGen,
			lastCmdSN:  ss.LastCmdSN,
			hasCommand: ss.HasCommand,
		}
		sess.Luns = len(lunSet)
		sessions[ss.ID] = sess
	}
	if len(sessions) > g.cfg.MaxSessions {
		return ErrBadSnapshot
	}
	masks := make(map[string][]int, len(snap.Masks))
	for initiator, mask := range snap.Masks {
		if initiator != normalizeInitiator(initiator) || len(mask) > g.cfg.MaxMaskEntries {
			return ErrBadSnapshot
		}
		for _, lun := range mask {
			if lun < 0 {
				return ErrBadSnapshot
			}
		}
		masks[initiator] = append([]int(nil), mask...)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	for id, sess := range sessions {
		if cur, ok := g.sessions[id]; ok && cur.ExpCmdSN > sess.ExpCmdSN {
			sess.ExpCmdSN = cur.ExpCmdSN
			sess.Commands = cur.Commands
			sess.lastCmdSN = cur.lastCmdSN
			sess.hasCommand = cur.hasCommand
		}
	}
	g.sessions = sessions
	g.masks = masks
	g.maskGeneration = snap.MaskGeneration
	g.nextID = snap.NextID
	return nil
}
