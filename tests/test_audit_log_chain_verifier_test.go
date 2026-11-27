package task

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-r-04"
)

func chainConfig(maxRecords, maxProof int) impl.ChainConfig {
	return impl.ChainConfig{MaxRecords: maxRecords, MaxProof: maxProof, MaxCheckpoints: 8}
}

func newVerifier(t *testing.T) *impl.AuditLogChainVerifier {
	t.Helper()
	return impl.NewAuditLogChainVerifier(chainConfig(256, 8))
}

func dumpOf(n int, salt string) []impl.Record {
	records := make([]impl.Record, 0, n)
	prev := ""
	for i := 1; i <= n; i++ {
		r := impl.Record{
			Seq:    int64(i),
			Actor:  "ops-7",
			Action: "policy.update",
			Detail: fmt.Sprintf("%s-change-%d", salt, i),
			Prev:   prev,
			At:     int64(1000 + i),
		}
		r.Digest = impl.ComputeDigest(r)
		records = append(records, r)
		prev = r.Digest
	}
	return records
}

func appendRecords(t *testing.T, v *impl.AuditLogChainVerifier, n int) []impl.Record {
	t.Helper()
	out := make([]impl.Record, 0, n)
	for i := 1; i <= n; i++ {
		r, err := v.Append("ops-7", "policy.update", fmt.Sprintf("change-%d", i), int64(1000+i))
		if err != nil {
			t.Fatalf("append %d must succeed, got %v", i, err)
		}
		out = append(out, r)
	}
	return out
}

func TestVerifyAcceptsCleanChain(t *testing.T) {
	v := newVerifier(t)
	records := appendRecords(t, v, 3)
	report, err := v.Verify()
	if err != nil {
		t.Fatalf("clean chain must verify, got %v", err)
	}
	if !report.OK || report.Code != impl.ChainOK {
		t.Fatalf("clean chain must be reported intact, got ok=%v code=%s", report.OK, report.Code)
	}
	if report.Count != 3 {
		t.Fatalf("report must cover three records, got %d", report.Count)
	}
	if v.Len() != 3 {
		t.Fatalf("log must hold three records, got %d", v.Len())
	}
	head, err := v.Head()
	if err != nil {
		t.Fatalf("head must be reported, got %v", err)
	}
	if head.Seq != 3 || head.Head != records[2].Digest {
		t.Fatalf("head must point at the newest record, got seq=%d head=%s", head.Seq, head.Head)
	}
	second, ok := v.Record(2)
	if !ok || second.Detail != "change-2" {
		t.Fatalf("record 2 must be readable, got %+v ok=%v", second, ok)
	}
}

func TestVerifyDetectsDigestMismatch(t *testing.T) {
	v := newVerifier(t)
	dump := dumpOf(3, "prod")
	dump[1].Detail = "prod-change-2-edited"
	if err := v.Load(dump); err != nil {
		t.Fatalf("dump must load for inspection, got %v", err)
	}
	report, err := v.Verify()
	if err != nil {
		t.Fatalf("verification must produce a report, got %v", err)
	}
	if report.OK {
		t.Fatalf("edited detail must be reported as a break")
	}
	if report.Code != impl.ChainDigestMismatch {
		t.Fatalf("expected %s, got %s", impl.ChainDigestMismatch, report.Code)
	}
	if report.BreakAt != 2 {
		t.Fatalf("break must be located at sequence 2, got %d", report.BreakAt)
	}
}

func TestVerifyDetectsSequenceGap(t *testing.T) {
	v := newVerifier(t)
	dump := dumpOf(3, "prod")
	dump[2].Seq = 4
	dump[2].Digest = impl.ComputeDigest(dump[2])
	if err := v.Load(dump); err != nil {
		t.Fatalf("dump must load, got %v", err)
	}
	report, err := v.Verify()
	if err != nil {
		t.Fatalf("verification must produce a report, got %v", err)
	}
	if report.OK || report.Code != impl.ChainGap {
		t.Fatalf("missing sequence must be reported as a gap, got ok=%v code=%s", report.OK, report.Code)
	}
	if report.BreakAt != 4 {
		t.Fatalf("gap must be reported at sequence 4, got %d", report.BreakAt)
	}
}

func TestVerifyDetectsPrevMismatch(t *testing.T) {
	v := newVerifier(t)
	dump := dumpOf(3, "prod")
	dump[2].Prev = dump[0].Digest
	dump[2].Digest = impl.ComputeDigest(dump[2])
	if err := v.Load(dump); err != nil {
		t.Fatalf("dump must load, got %v", err)
	}
	report, err := v.Verify()
	if err != nil {
		t.Fatalf("verification must produce a report, got %v", err)
	}
	if report.OK || report.Code != impl.ChainPrevMismatch {
		t.Fatalf("rewired predecessor must be reported, got ok=%v code=%s", report.OK, report.Code)
	}
	if report.BreakAt != 3 {
		t.Fatalf("break must be located at sequence 3, got %d", report.BreakAt)
	}
}

func TestVerifyRangeDetectsTruncatedRange(t *testing.T) {
	v := newVerifier(t)
	appendRecords(t, v, 2)
	report, err := v.VerifyRange(1, 5)
	if err != nil {
		t.Fatalf("truncated window must produce a report, got %v", err)
	}
	if report.OK {
		t.Fatalf("window beyond the head must not be reported intact")
	}
	if report.Code != impl.ChainTruncated {
		t.Fatalf("expected %s, got %s", impl.ChainTruncated, report.Code)
	}
	if report.BreakAt != 3 {
		t.Fatalf("first missing sequence must be 3, got %d", report.BreakAt)
	}
}

func TestImportRejectsWrongPrev(t *testing.T) {
	v := newVerifier(t)
	appendRecords(t, v, 2)
	incoming := impl.Record{
		Seq:    3,
		Actor:  "ops-7",
		Action: "policy.update",
		Detail: "imported",
		Prev:   "fp-of-another-chain",
		At:     1100,
	}
	incoming.Digest = impl.ComputeDigest(incoming)
	if _, err := v.Import(incoming); !errors.Is(err, impl.ErrPrevMismatch) {
		t.Fatalf("record binding another head must be refused with ErrPrevMismatch, got %v", err)
	}
	if v.Len() != 2 {
		t.Fatalf("refused record must not enter the log, holds %d", v.Len())
	}
}

func TestImportRejectsDigestMismatch(t *testing.T) {
	v := newVerifier(t)
	records := appendRecords(t, v, 2)
	incoming := impl.Record{
		Seq:    3,
		Actor:  "ops-7",
		Action: "policy.update",
		Detail: "imported",
		Prev:   records[1].Digest,
		At:     1100,
	}
	incoming.Digest = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := v.Import(incoming); !errors.Is(err, impl.ErrDigestMismatch) {
		t.Fatalf("record with a forged digest must be refused with ErrDigestMismatch, got %v", err)
	}
	if v.Len() != 2 {
		t.Fatalf("refused record must not enter the log, holds %d", v.Len())
	}
}

func TestAppendRejectsBlankActor(t *testing.T) {
	v := newVerifier(t)
	if _, err := v.Append("   ", "policy.update", "detail", 1000); !errors.Is(err, impl.ErrBadRecord) {
		t.Fatalf("record without an actor must be refused, got %v", err)
	}
	if _, err := v.Append("ops-7", "", "detail", 1000); !errors.Is(err, impl.ErrBadRecord) {
		t.Fatalf("record without an action must be refused, got %v", err)
	}
	if v.Len() != 0 {
		t.Fatalf("refused records must not enter the log, holds %d", v.Len())
	}
}

func TestAppendEnforcesCapacity(t *testing.T) {
	v := impl.NewAuditLogChainVerifier(chainConfig(2, 4))
	appendRecords(t, v, 2)
	if _, err := v.Append("ops-7", "policy.update", "third", 1200); !errors.Is(err, impl.ErrCapacity) {
		t.Fatalf("append over the cap must be refused with ErrCapacity, got %v", err)
	}
	if v.Len() != 2 {
		t.Fatalf("full log must keep two records, holds %d", v.Len())
	}
}

func TestRangeProofCoversExactWindow(t *testing.T) {
	v := newVerifier(t)
	appendRecords(t, v, 4)
	proof, err := v.RangeProof(2, 3)
	if err != nil {
		t.Fatalf("proof must be produced, got %v", err)
	}
	if proof.From != 2 || proof.To != 3 || proof.Count != 2 {
		t.Fatalf("proof window must be [2,3] with two records, got %+v", proof)
	}
	if len(proof.Digests) != 2 {
		t.Fatalf("proof must carry two digests, got %d", len(proof.Digests))
	}
	if proof.Root != impl.ProofRoot(proof.Digests) {
		t.Fatalf("proof root must fold its own digests")
	}
	if err := v.VerifyProof(proof); err != nil {
		t.Fatalf("untouched proof must verify, got %v", err)
	}
	proof.Digests[0] = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := v.VerifyProof(proof); !errors.Is(err, impl.ErrProofMismatch) {
		t.Fatalf("edited proof must be refused with ErrProofMismatch, got %v", err)
	}
}

func TestRangeProofRejectsOversizedWindow(t *testing.T) {
	v := impl.NewAuditLogChainVerifier(chainConfig(16, 2))
	appendRecords(t, v, 4)
	if _, err := v.RangeProof(1, 4); !errors.Is(err, impl.ErrProofTooLarge) {
		t.Fatalf("window over the proof cap must be refused with ErrProofTooLarge, got %v", err)
	}
	if _, err := v.RangeProof(3, 2); !errors.Is(err, impl.ErrBadRange) {
		t.Fatalf("inverted window must be refused with ErrBadRange, got %v", err)
	}
}

func TestSealCheckpointMatchesHead(t *testing.T) {
	v := newVerifier(t)
	records := appendRecords(t, v, 3)
	cp, err := v.Seal(2000)
	if err != nil {
		t.Fatalf("seal must succeed, got %v", err)
	}
	if cp.Seq != 3 || cp.Head != records[2].Digest || cp.At != 2000 {
		t.Fatalf("checkpoint must capture seq 3, its digest and the seal time, got %+v", cp)
	}
	if got := len(v.Checkpoints()); got != 1 {
		t.Fatalf("seal must be recorded once, table holds %d", got)
	}
}

func TestVerifyAgainstCheckpointRejectsDivergence(t *testing.T) {
	v := newVerifier(t)
	appendRecords(t, v, 3)
	cp, err := v.Seal(2000)
	if err != nil {
		t.Fatalf("seal must succeed, got %v", err)
	}
	divergent := newVerifier(t)
	if err := divergent.Load(dumpOf(3, "shadow")); err != nil {
		t.Fatalf("divergent dump must load, got %v", err)
	}
	if err := divergent.VerifyAgainst(cp); !errors.Is(err, impl.ErrCheckpointMismatch) {
		t.Fatalf("divergent chain must be refused against the checkpoint, got %v", err)
	}
	if err := v.VerifyAgainst(cp); err != nil {
		t.Fatalf("the sealed chain itself must match its checkpoint, got %v", err)
	}
}

func TestSnapshotRestoreRoundTripsChain(t *testing.T) {
	v := newVerifier(t)
	appendRecords(t, v, 3)
	if _, err := v.Seal(2000); err != nil {
		t.Fatalf("seal must succeed, got %v", err)
	}
	data, err := v.Snapshot()
	if err != nil {
		t.Fatalf("snapshot must be produced, got %v", err)
	}
	other := newVerifier(t)
	if err := other.Restore(data); err != nil {
		t.Fatalf("snapshot must restore, got %v", err)
	}
	if other.Len() != 3 {
		t.Fatalf("records must survive restore, got %d", other.Len())
	}
	if got := len(other.Checkpoints()); got != 1 {
		t.Fatalf("checkpoint must survive restore, got %d", got)
	}
	report, err := other.Verify()
	if err != nil || !report.OK {
		t.Fatalf("restored chain must verify, got ok=%v err=%v", report.OK, err)
	}
	next, err := other.Append("ops-7", "policy.update", "after-restore", 2100)
	if err != nil {
		t.Fatalf("append after restore must succeed, got %v", err)
	}
	if next.Seq != 4 {
		t.Fatalf("sequence must continue at 4 after restore, got %d", next.Seq)
	}
	if err := other.Restore([]byte("{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must be refused with ErrBadSnapshot, got %v", err)
	}
	if other.Len() != 4 {
		t.Fatalf("failed restore must leave the log untouched, holds %d", other.Len())
	}
}

func TestConcurrentAppendAssignsUniqueSequences(t *testing.T) {
	v := newVerifier(t)
	const workers = 64
	const rounds = 2
	var wg sync.WaitGroup
	failures := make(chan string, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if _, err := v.Append("ops-7", "policy.update", fmt.Sprintf("w%d-r%d", id, i), 3000); err != nil {
					failures <- "append returned error: " + err.Error()
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Fatalf("%s", msg)
	}
	if v.Len() != workers*rounds {
		t.Fatalf("log must hold %d records, got %d", workers*rounds, v.Len())
	}
	report, err := v.Verify()
	if err != nil {
		t.Fatalf("verification must produce a report, got %v", err)
	}
	if !report.OK || report.Code != impl.ChainOK {
		t.Fatalf("concurrently appended chain must be intact, got ok=%v code=%s at %d", report.OK, report.Code, report.BreakAt)
	}
	for seq := int64(1); seq <= int64(workers*rounds); seq++ {
		if _, ok := v.Record(seq); !ok {
			t.Fatalf("sequence %d is missing after concurrent appends", seq)
		}
	}
}
