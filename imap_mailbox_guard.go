// Package task keeps the server side of an IMAP mailbox consistent: the UID
// space of every mailbox, the CONDSTORE modification sequence and the bounded
// change log a QRESYNC client replays against.
package task

import "errors"

// errNotImplemented marks behaviour that still needs to be written.
var errNotImplemented = errors.New("not implemented")

// UID aliases the identifier handed out by Append.
type UID = uint32

// Change kinds written to the modification log.
const (
	KindAppend  = "append"
	KindFlags   = "flags"
	KindExpunge = "expunge"
)

// Resync reasons carried by Delta.Reason.
const (
	ReasonWindowTrimmed = "window_trimmed"
	ReasonValidityLost  = "uidvalidity_changed"
)

// Errors returned by the guard.
var (
	ErrBadOwner      = errors.New("user and mailbox are required")
	ErrBadMessage    = errors.New("message body is required")
	ErrNoMailbox     = errors.New("mailbox has not been selected yet")
	ErrMailboxLimit  = errors.New("mailbox table is full")
	ErrModSeqAhead   = errors.New("modseq is newer than the mailbox state")
	ErrStaleValidity = errors.New("uidvalidity does not match the current mailbox")
	ErrBadSnapshot   = errors.New("snapshot payload is invalid")
)

// Limits bounds the live tables of the guard.
type Limits struct {
	MaxChangeLog int
	MaxMailboxes int
}

// RawMessage is the message body and its initial flag set.
type RawMessage struct {
	Body  string
	Flags []string
}

// MailboxView is what SELECT reports back to the client.
type MailboxView struct {
	User        string
	Mailbox     string
	UIDValidity uint32
	UIDNext     uint32
	ModSeq      uint64
	Messages    int
}

// FlagChange is one entry of the modification log.
type FlagChange struct {
	User    string
	Mailbox string
	UID     uint32
	Kind    string
	ModSeq  uint64
	Flags   []string
	Added   []string
	Removed []string
}

// Delta is the answer of FetchSince.
type Delta struct {
	User        string
	Mailbox     string
	UIDValidity uint32
	Since       uint64
	ModSeq      uint64
	Changes     []FlagChange
	FullResync  bool
	Reason      string
}

// ImapMailboxGuard owns the per mailbox UID space, the modification log and
// the change counters.
type ImapMailboxGuard struct {
	limits Limits
}

// NewImapMailboxGuard builds a guard with the given caps.
func NewImapMailboxGuard(limits Limits) *ImapMailboxGuard {
	return &ImapMailboxGuard{limits: limits}
}

// Select creates the mailbox on first use and reports its view.
func (g *ImapMailboxGuard) Select(user, mailbox string) (MailboxView, error) {
	return MailboxView{}, errNotImplemented
}

// Append stores a message and returns its permanent UID.
func (g *ImapMailboxGuard) Append(user, mailbox string, msg RawMessage) (UID, error) {
	return 0, errNotImplemented
}

// StoreFlags applies the synced flag set to the given UIDs.
func (g *ImapMailboxGuard) StoreFlags(user, mailbox string, uids []uint32, modseq uint64) ([]FlagChange, error) {
	return nil, errNotImplemented
}

// Expunge removes the given UIDs and reports the ones that were removed.
func (g *ImapMailboxGuard) Expunge(user, mailbox string, uids []uint32) ([]uint32, error) {
	return nil, errNotImplemented
}

// FetchSince replays the modification log after modseq.
func (g *ImapMailboxGuard) FetchSince(user, mailbox string, modseq uint64) (Delta, error) {
	return Delta{}, errNotImplemented
}

// Fetch reads the live messages of the given UIDs under one uidvalidity.
func (g *ImapMailboxGuard) Fetch(user, mailbox string, uidValidity uint32, uids []uint32) ([]RawMessage, error) {
	return nil, errNotImplemented
}

// UIDValidity reports the current uidvalidity of the mailbox.
func (g *ImapMailboxGuard) UIDValidity(user, mailbox string) (uint32, error) {
	return 0, errNotImplemented
}

// BumpUIDValidity rebuilds the mailbox under a new uidvalidity.
func (g *ImapMailboxGuard) BumpUIDValidity(user, mailbox string) (uint32, error) {
	return 0, errNotImplemented
}

// Changes returns the retained modification log, oldest first.
func (g *ImapMailboxGuard) Changes(user, mailbox string) []FlagChange {
	return nil
}

// Tombstones lists the UIDs that were expunged and must never come back.
func (g *ImapMailboxGuard) Tombstones(user, mailbox string) []uint32 {
	return nil
}

// Snapshot serialises every mailbox, its change log and its counters.
func (g *ImapMailboxGuard) Snapshot() ([]byte, error) {
	return nil, errNotImplemented
}

// Restore replaces the whole state with a snapshot, all or nothing.
func (g *ImapMailboxGuard) Restore(data []byte) error {
	return errNotImplemented
}
