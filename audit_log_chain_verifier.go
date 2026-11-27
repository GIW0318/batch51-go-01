// Package task keeps an append only audit log whose every record binds the
// digest of its predecessor and a dense monotonic sequence number, verifies the
// chain on demand, produces range proofs for a sealed window, and compares the
// live chain head with a signed checkpoint.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
)

// Verification codes.
const (
	ChainOK             = "alc_ok"
	ChainEmpty          = "alc_empty"
	ChainGap            = "alc_gap"
	ChainPrevMismatch   = "alc_prev_mismatch"
	ChainDigestMismatch = "alc_digest_mismatch"
	ChainTruncated      = "alc_truncated"
)

// Errors returned by the verifier.
var (
	ErrBadRecord          = errors.New("record is incomplete")
	ErrCapacity           = errors.New("audit log is full")
	ErrBadRange           = errors.New("range is inverted or outside the log")
	ErrSeqGap             = errors.New("record sequence does not follow the head")
	ErrPrevMismatch       = errors.New("record does not bind the head digest")
	ErrDigestMismatch     = errors.New("record digest does not match its content")
	ErrProofTooLarge      = errors.New("range proof window exceeds the cap")
	ErrProofMismatch      = errors.New("range proof root does not match its digests")
	ErrCheckpointMismatch = errors.New("chain head does not match the sealed checkpoint")
	ErrBadSnapshot        = errors.New("snapshot payload is invalid")
)

// Record is one audit entry. Prev carries the digest of the record before it
// and is empty only for the first record of a log.
type Record struct {
	Seq    int64  `json:"seq"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Detail string `json:"detail"`
	Prev   string `json:"prev"`
	Digest string `json:"digest"`
	At     int64  `json:"at"`
}

// Checkpoint is the head of the chain at the moment it was sealed.
type Checkpoint struct {
	Seq  int64  `json:"seq"`
	Head string `json:"head"`
	At   int64  `json:"at"`
}

// RangeProof is a compact statement about a closed window of records.
type RangeProof struct {
	From    int64    `json:"from"`
	To      int64    `json:"to"`
	Count   int      `json:"count"`
	Head    string   `json:"head"`
	Root    string   `json:"root"`
	Digests []string `json:"digests"`
}

// VerifyReport tells whether a window is intact and where it first broke.
type VerifyReport struct {
	OK       bool   `json:"ok"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	Count    int    `json:"count"`
	BreakAt  int64  `json:"break_at"`
	Code     string `json:"code"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// ChainConfig bounds the log, the proof window and the checkpoint table.
type ChainConfig struct {
	MaxRecords     int
	MaxProof       int
	MaxCheckpoints int
}

// AuditLogChainVerifier owns the record table, the checkpoint table and the
// verification entry points. The mutex makes sequence assignment and head
// updates atomic, so a concurrent reader never observes a half-written head.
type AuditLogChainVerifier struct {
	cfg         ChainConfig
	mu          sync.RWMutex
	records     []Record
	checkpoints []Checkpoint
}

// NewAuditLogChainVerifier builds a verifier with the given bounds.
func NewAuditLogChainVerifier(cfg ChainConfig) *AuditLogChainVerifier {
	return &AuditLogChainVerifier{cfg: cfg}
}

// ComputeDigest returns the canonical digest of a record. The Digest field of
// the argument is ignored, so a record can be hashed before it is stored.
func ComputeDigest(r Record) string {
	h := sha256.New()
	h.Write([]byte("alc-record\x00"))
	fields := []string{
		fmt.Sprintf("%d", r.Seq),
		r.Actor,
		r.Action,
		r.Detail,
		r.Prev,
		fmt.Sprintf("%d", r.At),
	}
	for _, f := range fields {
		fmt.Fprintf(h, "%d:", len(f))
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ProofRoot folds the digests of a window into one root value.
func ProofRoot(digests []string) string {
	h := sha256.New()
	h.Write([]byte("alc-proof\x00"))
	for _, d := range digests {
		fmt.Fprintf(h, "%d:", len(d))
		h.Write([]byte(d))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validRecord reports whether a record carries an actor, an action and a
// positive sequence number.
func validRecord(r Record) bool {
	return r.Seq > 0 && strings.TrimSpace(r.Actor) != "" && strings.TrimSpace(r.Action) != ""
}

// headLocked returns the sequence and digest of the chain head. The caller
// must hold the lock.
func (v *AuditLogChainVerifier) headLocked() (int64, string) {
	if len(v.records) == 0 {
		return 0, ""
	}
	last := v.records[len(v.records)-1]
	return last.Seq, last.Digest
}

// Append writes one record at the head of the chain and returns the stored
// copy with its digest filled in.
func (v *AuditLogChainVerifier) Append(actor, action, detail string, at int64) (Record, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(action) == "" {
		return Record{}, ErrBadRecord
	}
	if v.cfg.MaxRecords > 0 && len(v.records) >= v.cfg.MaxRecords {
		return Record{}, ErrCapacity
	}
	seq, prev := v.headLocked()
	r := Record{
		Seq:    seq + 1,
		Actor:  actor,
		Action: action,
		Detail: detail,
		Prev:   prev,
		At:     at,
	}
	r.Digest = ComputeDigest(r)
	v.records = append(v.records, r)
	return r, nil
}

// Import stores an externally built record after checking that its sequence
// number, predecessor binding and digest all agree with the live head.
func (v *AuditLogChainVerifier) Import(r Record) (Record, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !validRecord(r) {
		return Record{}, ErrBadRecord
	}
	if v.cfg.MaxRecords > 0 && len(v.records) >= v.cfg.MaxRecords {
		return Record{}, ErrCapacity
	}
	seq, prev := v.headLocked()
	if r.Seq != seq+1 {
		return Record{}, ErrSeqGap
	}
	if r.Prev != prev {
		return Record{}, ErrPrevMismatch
	}
	if ComputeDigest(r) != r.Digest {
		return Record{}, ErrDigestMismatch
	}
	v.records = append(v.records, r)
	return r, nil
}

// Load replaces the whole log with a dump without validating it, so a damaged
// dump can still be inspected with Verify.
func (v *AuditLogChainVerifier) Load(records []Record) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	cp := make([]Record, len(records))
	copy(cp, records)
	v.records = cp
	return nil
}

// verifyLocked walks every stored record in order and reports the first break
// whose sequence falls inside the closed window from-to. Records outside the
// window still advance the expected sequence and predecessor, because a
// damaged dump is not guaranteed to be monotonic. The caller must hold the
// lock, so the walk never observes a half-written head.
func (v *AuditLogChainVerifier) verifyLocked(from, to int64, checkTruncated bool) VerifyReport {
	report := VerifyReport{OK: true, From: from, To: to, Code: ChainOK}
	expectedSeq := int64(1)
	prevDigest := ""
	headSeq, _ := v.headLocked()
	for _, r := range v.records {
		inWindow := r.Seq >= from && r.Seq <= to
		if inWindow {
			report.Count++
		}
		var code, expected, actual string
		switch {
		case r.Seq != expectedSeq:
			code, expected, actual = ChainGap, fmt.Sprintf("%d", expectedSeq), fmt.Sprintf("%d", r.Seq)
		case r.Prev != prevDigest:
			code, expected, actual = ChainPrevMismatch, prevDigest, r.Prev
		case ComputeDigest(r) != r.Digest:
			code, expected, actual = ChainDigestMismatch, ComputeDigest(r), r.Digest
		}
		if code != "" {
			if inWindow {
				report.OK = false
				report.Code = code
				report.BreakAt = r.Seq
				report.Expected = expected
				report.Actual = actual
				return report
			}
			// The chain is already broken before the window; keep walking so
			// the window itself is still judged on its own records.
		}
		expectedSeq = r.Seq + 1
		prevDigest = r.Digest
	}
	if checkTruncated && to > headSeq {
		report.OK = false
		report.Code = ChainTruncated
		report.BreakAt = headSeq + 1
		if report.BreakAt < from {
			report.BreakAt = from
		}
	}
	return report
}

// Verify checks the whole log.
func (v *AuditLogChainVerifier) Verify() (VerifyReport, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	headSeq, _ := v.headLocked()
	report := v.verifyLocked(1, math.MaxInt64, false)
	report.To = headSeq
	return report, nil
}

// VerifyRange checks the closed window from-to and reports the first break.
func (v *AuditLogChainVerifier) VerifyRange(from, to int64) (VerifyReport, error) {
	if from > to || from < 1 {
		return VerifyReport{}, ErrBadRange
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.verifyLocked(from, to, true), nil
}

// RangeProof builds a proof for the closed window from-to.
func (v *AuditLogChainVerifier) RangeProof(from, to int64) (RangeProof, error) {
	if from > to || from < 1 {
		return RangeProof{}, ErrBadRange
	}
	if v.cfg.MaxProof > 0 && to-from+1 > int64(v.cfg.MaxProof) {
		return RangeProof{}, ErrProofTooLarge
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	headSeq, _ := v.headLocked()
	if to > headSeq {
		return RangeProof{}, ErrBadRange
	}
	proof := RangeProof{From: from, To: to}
	for _, r := range v.records {
		if r.Seq < from {
			continue
		}
		if r.Seq > to {
			continue
		}
		proof.Digests = append(proof.Digests, r.Digest)
		proof.Head = r.Digest
	}
	if int64(len(proof.Digests)) != to-from+1 {
		return RangeProof{}, ErrBadRange
	}
	proof.Count = len(proof.Digests)
	proof.Root = ProofRoot(proof.Digests)
	return proof, nil
}

// VerifyProof checks a proof without reading the log back.
func (v *AuditLogChainVerifier) VerifyProof(p RangeProof) error {
	if p.From > p.To || p.From < 1 {
		return ErrProofMismatch
	}
	if p.Count != len(p.Digests) || int64(p.Count) != p.To-p.From+1 || p.Count == 0 {
		return ErrProofMismatch
	}
	if ProofRoot(p.Digests) != p.Root {
		return ErrProofMismatch
	}
	return nil
}

// Seal records the current head as a checkpoint.
func (v *AuditLogChainVerifier) Seal(at int64) (Checkpoint, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	seq, head := v.headLocked()
	cp := Checkpoint{Seq: seq, Head: head, At: at}
	if v.cfg.MaxCheckpoints > 0 && len(v.checkpoints) >= v.cfg.MaxCheckpoints {
		v.checkpoints = append(v.checkpoints[1:], cp)
	} else {
		v.checkpoints = append(v.checkpoints, cp)
	}
	return cp, nil
}

// Checkpoints lists the sealed checkpoints, oldest first.
func (v *AuditLogChainVerifier) Checkpoints() []Checkpoint {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]Checkpoint, len(v.checkpoints))
	copy(out, v.checkpoints)
	return out
}

// VerifyAgainst compares the live chain head with one sealed checkpoint.
func (v *AuditLogChainVerifier) VerifyAgainst(cp Checkpoint) error {
	v.mu.RLock()
	defer v.mu.RUnlock()
	seq, head := v.headLocked()
	if seq != cp.Seq || head != cp.Head {
		return ErrCheckpointMismatch
	}
	return nil
}

// Truncate drops every record after seq, which models an operator pruning the
// tail of the log.
func (v *AuditLogChainVerifier) Truncate(seq int64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	kept := v.records[:0]
	for _, r := range v.records {
		if r.Seq <= seq {
			kept = append(kept, r)
		}
	}
	v.records = kept
	return nil
}

// Head reports the current head of the chain.
func (v *AuditLogChainVerifier) Head() (Checkpoint, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	seq, head := v.headLocked()
	cp := Checkpoint{Seq: seq, Head: head}
	if len(v.records) > 0 {
		cp.At = v.records[len(v.records)-1].At
	}
	return cp, nil
}

// Record reads one record by sequence number.
func (v *AuditLogChainVerifier) Record(seq int64) (Record, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for _, r := range v.records {
		if r.Seq == seq {
			return r, true
		}
	}
	return Record{}, false
}

// Len reports how many records the log holds.
func (v *AuditLogChainVerifier) Len() int64 {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return int64(len(v.records))
}

// snapshotPayload is the on-disk form of the whole verifier state.
type snapshotPayload struct {
	Records     []Record     `json:"records"`
	Checkpoints []Checkpoint `json:"checkpoints"`
}

// Snapshot serialises the records and the checkpoint table.
func (v *AuditLogChainVerifier) Snapshot() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return json.Marshal(snapshotPayload{
		Records:     v.records,
		Checkpoints: v.checkpoints,
	})
}

// Restore replaces the whole log from a snapshot, all or nothing.
func (v *AuditLogChainVerifier) Restore(data []byte) error {
	var payload snapshotPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return ErrBadSnapshot
	}
	if payload.Records == nil {
		payload.Records = []Record{}
	}
	if payload.Checkpoints == nil {
		payload.Checkpoints = []Checkpoint{}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.records = payload.Records
	v.checkpoints = payload.Checkpoints
	return nil
}
