package task

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	impl "gsb/batch51-go-safety-c-07"
)

// dg renders n copies of the byte b as lower case hex.
func dg(b byte, n int) string {
	return strings.Repeat(fmt.Sprintf("%02x", b), n)
}

// line renders one log record in the wire format the parser consumes.
func line(pcr int, typ, alg, digestHex, dataHex string) string {
	return fmt.Sprintf("%d|%s|%s|%s|%s\n", pcr, typ, alg, digestHex, dataHex)
}

func replayConfig() impl.ReplayConfig {
	return impl.ReplayConfig{
		MaxEvents:     16,
		MaxEventData:  64,
		MaxPCR:        8,
		SupportedAlgs: []string{impl.BankSHA1, impl.BankSHA256},
	}
}

func sameValues(a, b []impl.PCRValue) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].PCR != b[i].PCR || a[i].Bank != b[i].Bank || !bytes.Equal(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}

func TestParseReadsEventsAndFingerprint(t *testing.T) {
	body := line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "aabb") +
		line(1, impl.EventAction, impl.BankSHA256, dg(0x22, 32), "") +
		line(0, impl.EventNoAction, impl.BankSHA256, dg(0x33, 32), "")
	r := impl.NewEventLogReplayer(replayConfig())
	parsed, err := r.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse must succeed, got %v", err)
	}
	if len(parsed.Events) != 3 {
		t.Fatalf("parsed %d events, want 3", len(parsed.Events))
	}
	second := parsed.Events[1]
	if second.Index != 1 || second.PCR != 1 || second.Type != impl.EventAction || second.Alg != impl.BankSHA256 {
		t.Fatalf("second event = %+v, want index 1 on pcr 1 of type EV_ACTION", second)
	}
	if len(second.Digest) != 32 || second.Digest[0] != 0x22 {
		t.Fatalf("second event digest = %x, want 32 bytes starting with 0x22", second.Digest)
	}
	if len(parsed.Events[0].Data) != 2 || parsed.Events[0].Data[0] != 0xaa {
		t.Fatalf("first event data = %x, want the decoded payload aabb", parsed.Events[0].Data)
	}
	if got := len(r.Events()); got != 3 {
		t.Fatalf("replayer keeps %d events, want 3", got)
	}
	fp := r.Fingerprint()
	if fp == "" {
		t.Fatalf("fingerprint must not be empty")
	}
	banks := r.Banks()
	if len(banks) != 1 || banks[0] != impl.BankSHA256 {
		t.Fatalf("banks = %v, want [sha256]", banks)
	}
	other := impl.NewEventLogReplayer(replayConfig())
	if _, err := other.Parse([]byte(body)); err != nil {
		t.Fatalf("re-parse of the same log: %v", err)
	}
	if other.Fingerprint() != fp {
		t.Fatalf("fingerprint is not stable: %q vs %q", other.Fingerprint(), fp)
	}
	changed := impl.NewEventLogReplayer(replayConfig())
	if _, err := changed.Parse([]byte(line(0, impl.EventExtend, impl.BankSHA256, dg(0x44, 32), ""))); err != nil {
		t.Fatalf("parse changed log: %v", err)
	}
	if changed.Fingerprint() == fp {
		t.Fatalf("different logs must not share a fingerprint")
	}
}

func TestParseRejectsMalformedAndEmptyLogs(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	if _, err := r.Parse(nil); !errors.Is(err, impl.ErrEmptyLog) {
		t.Fatalf("empty log must fail with ErrEmptyLog, got %v", err)
	}
	if _, err := r.Parse([]byte("0|EV_EXTEND|sha256\n")); !errors.Is(err, impl.ErrMalformedEvent) {
		t.Fatalf("short record must fail with ErrMalformedEvent, got %v", err)
	}
	if _, err := r.Parse([]byte("x|EV_EXTEND|sha256|" + dg(0x11, 32) + "|\n")); !errors.Is(err, impl.ErrMalformedEvent) {
		t.Fatalf("non numeric pcr must fail with ErrMalformedEvent, got %v", err)
	}
	if _, err := r.Parse([]byte(line(0, impl.EventExtend, impl.BankSHA256, "zz", ""))); !errors.Is(err, impl.ErrMalformedEvent) {
		t.Fatalf("invalid digest hex must fail with ErrMalformedEvent, got %v", err)
	}
}

func TestParseRejectsTruncatedLog(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	noNewline := strings.TrimSuffix(line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), ""), "\n")
	if _, err := r.Parse([]byte(noNewline)); !errors.Is(err, impl.ErrTruncatedLog) {
		t.Fatalf("record without a trailing newline must fail with ErrTruncatedLog, got %v", err)
	}
	partial := line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") + "0|EV_EXTEND|sha256|abcd"
	if _, err := r.Parse([]byte(partial)); !errors.Is(err, impl.ErrTruncatedLog) {
		t.Fatalf("half written trailing record must fail with ErrTruncatedLog, got %v", err)
	}
}

func TestParseRejectsDigestLengthMismatch(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	if _, err := r.Parse([]byte(line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 4), ""))); !errors.Is(err, impl.ErrDigestLength) {
		t.Fatalf("short sha256 digest must fail with ErrDigestLength, got %v", err)
	}
	if _, err := r.Parse([]byte(line(0, impl.EventExtend, impl.BankSHA1, dg(0x11, 32), ""))); !errors.Is(err, impl.ErrDigestLength) {
		t.Fatalf("long sha1 digest must fail with ErrDigestLength, got %v", err)
	}
}

func TestParseEnforcesEventLimit(t *testing.T) {
	cfg := replayConfig()
	cfg.MaxEvents = 2
	r := impl.NewEventLogReplayer(cfg)
	body := line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") +
		line(1, impl.EventExtend, impl.BankSHA256, dg(0x22, 32), "") +
		line(2, impl.EventExtend, impl.BankSHA256, dg(0x33, 32), "")
	if _, err := r.Parse([]byte(body)); !errors.Is(err, impl.ErrLogTooLarge) {
		t.Fatalf("three events with a cap of two must fail with ErrLogTooLarge, got %v", err)
	}
	if got := len(r.Events()); got != 0 {
		t.Fatalf("rejected log left %d events behind, want 0", got)
	}
}

func TestParseEnforcesEventDataLimit(t *testing.T) {
	cfg := replayConfig()
	cfg.MaxEventData = 16
	r := impl.NewEventLogReplayer(cfg)
	body := line(0, impl.EventAction, impl.BankSHA256, dg(0x11, 32), dg(0xaa, 32))
	if _, err := r.Parse([]byte(body)); !errors.Is(err, impl.ErrEventTooLarge) {
		t.Fatalf("oversized event payload must fail with ErrEventTooLarge, got %v", err)
	}
	if _, err := r.Parse([]byte(line(0, impl.EventAction, impl.BankSHA256, dg(0x11, 32), dg(0xaa, 16)))); err != nil {
		t.Fatalf("payload exactly at the cap must be accepted, got %v", err)
	}
}

func TestParseRejectsPCROutOfRange(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	if _, err := r.Parse([]byte(line(8, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), ""))); !errors.Is(err, impl.ErrPCRRange) {
		t.Fatalf("pcr 8 with a cap of 8 must fail with ErrPCRRange, got %v", err)
	}
	if _, err := r.Parse([]byte(line(-1, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), ""))); !errors.Is(err, impl.ErrPCRRange) {
		t.Fatalf("negative pcr must fail with ErrPCRRange, got %v", err)
	}
	if _, err := r.Parse([]byte(line(7, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), ""))); err != nil {
		t.Fatalf("pcr 7 is inside the cap and must be accepted, got %v", err)
	}
}

func TestReplayKeepsBanksSeparate(t *testing.T) {
	body := line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") +
		line(1, impl.EventExtend, impl.BankSHA1, dg(0x22, 20), "") +
		line(0, impl.EventExtend, impl.BankSHA1, dg(0x33, 20), "")
	r := impl.NewEventLogReplayer(replayConfig())
	parsed, err := r.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sha256Values, err := r.Replay(parsed, impl.BankSHA256)
	if err != nil {
		t.Fatalf("replay sha256: %v", err)
	}
	if len(sha256Values) != 1 {
		t.Fatalf("sha256 replay returned %d values, want 1", len(sha256Values))
	}
	if sha256Values[0].PCR != 0 || sha256Values[0].Bank != impl.BankSHA256 || len(sha256Values[0].Value) != 32 {
		t.Fatalf("sha256 value = %+v, want a 32 byte value on pcr 0", sha256Values[0])
	}
	sha1Values, err := r.Replay(parsed, impl.BankSHA1)
	if err != nil {
		t.Fatalf("replay sha1: %v", err)
	}
	if len(sha1Values) != 2 || sha1Values[0].PCR != 0 || sha1Values[1].PCR != 1 {
		t.Fatalf("sha1 replay = %+v, want pcr 0 and pcr 1 in order", sha1Values)
	}
	for i, v := range sha1Values {
		if len(v.Value) != 20 {
			t.Fatalf("sha1 value %d has %d bytes, want 20", i, len(v.Value))
		}
	}
	only := impl.NewEventLogReplayer(replayConfig())
	single, err := only.Parse([]byte(line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "")))
	if err != nil {
		t.Fatalf("parse single bank log: %v", err)
	}
	reference, err := only.Replay(single, impl.BankSHA256)
	if err != nil {
		t.Fatalf("replay single bank log: %v", err)
	}
	if !bytes.Equal(reference[0].Value, sha256Values[0].Value) {
		t.Fatalf("sha1 events changed the sha256 pcr, banks are mixed")
	}
}

func TestReplayIsOrderSensitiveAndSkipsNoAction(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	first, err := r.Parse([]byte(
		line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") +
			line(0, impl.EventExtend, impl.BankSHA256, dg(0x22, 32), "") +
			line(0, impl.EventNoAction, impl.BankSHA256, dg(0x99, 32), "")))
	if err != nil {
		t.Fatalf("parse first log: %v", err)
	}
	second, err := r.Parse([]byte(
		line(0, impl.EventExtend, impl.BankSHA256, dg(0x22, 32), "") +
			line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "")))
	if err != nil {
		t.Fatalf("parse second log: %v", err)
	}
	firstValues, err := r.Replay(first, impl.BankSHA256)
	if err != nil {
		t.Fatalf("replay first log: %v", err)
	}
	secondValues, err := r.Replay(second, impl.BankSHA256)
	if err != nil {
		t.Fatalf("replay second log: %v", err)
	}
	if bytes.Equal(firstValues[0].Value, secondValues[0].Value) {
		t.Fatalf("swapping two extends produced the same pcr, order is ignored")
	}
	without, err := r.Parse([]byte(
		line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") +
			line(0, impl.EventExtend, impl.BankSHA256, dg(0x22, 32), "")))
	if err != nil {
		t.Fatalf("parse no action free log: %v", err)
	}
	baseValues, err := r.Replay(without, impl.BankSHA256)
	if err != nil {
		t.Fatalf("replay no action free log: %v", err)
	}
	if !bytes.Equal(baseValues[0].Value, firstValues[0].Value) {
		t.Fatalf("EV_NO_ACTION changed the pcr value")
	}
}

func TestReplayIsDeterministicAndReturnsCopies(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	parsed, err := r.Parse([]byte(
		line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") +
			line(1, impl.EventExtend, impl.BankSHA256, dg(0x22, 32), "")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	first, err := r.Replay(parsed, impl.BankSHA256)
	if err != nil {
		t.Fatalf("first replay: %v", err)
	}
	original := append([]byte(nil), first[0].Value...)
	second, err := r.Replay(parsed, impl.BankSHA256)
	if err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if !sameValues(first, second) {
		t.Fatalf("replaying the same log twice returned different values")
	}
	if r.CacheHits() == 0 {
		t.Fatalf("second replay of the same log and bank must hit the cache")
	}
	for _, v := range second {
		for i := range v.Value {
			v.Value[i] ^= 0xff
		}
	}
	third, err := r.Replay(parsed, impl.BankSHA256)
	if err != nil {
		t.Fatalf("third replay: %v", err)
	}
	if !bytes.Equal(third[0].Value, original) {
		t.Fatalf("mutating a returned value corrupted the cached replay")
	}
}

func TestCompareMarksMissingLiveValueUndetermined(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	parsed, err := r.Parse([]byte(line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := r.Compare(nil); !errors.Is(err, impl.ErrNoReplay) {
		t.Fatalf("compare before replay must fail with ErrNoReplay, got %v", err)
	}
	if _, err := r.Replay(parsed, impl.BankSHA256); err != nil {
		t.Fatalf("replay: %v", err)
	}
	report, err := r.Compare(nil)
	if err != nil {
		t.Fatalf("compare without live values: %v", err)
	}
	if report.Verdict != impl.VerdictUndetermined {
		t.Fatalf("verdict without live values = %q, want %q", report.Verdict, impl.VerdictUndetermined)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != impl.StatusUndetermined {
		t.Fatalf("checks = %+v, want one undetermined pcr", report.Checks)
	}
}

func TestCompareReportsMismatchAndFirstDivergent(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	parsed, err := r.Parse([]byte(
		line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") +
			line(0, impl.EventExtend, impl.BankSHA256, dg(0x22, 32), "")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	values, err := r.Replay(parsed, impl.BankSHA256)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	good, err := r.Compare([]impl.PCRValue{{PCR: 0, Bank: impl.BankSHA256, Value: append([]byte(nil), values[0].Value...)}})
	if err != nil {
		t.Fatalf("compare matching values: %v", err)
	}
	if good.Verdict != impl.VerdictMatch || len(good.Checks) != 1 || good.Checks[0].Status != impl.StatusMatch {
		t.Fatalf("matching comparison = %+v, want a single match", good)
	}
	tampered := append([]byte(nil), values[0].Value...)
	tampered[0] ^= 0xff
	bad, err := r.Compare([]impl.PCRValue{{PCR: 0, Bank: impl.BankSHA256, Value: tampered}})
	if err != nil {
		t.Fatalf("compare tampered values: %v", err)
	}
	if bad.Verdict != impl.VerdictMismatch || len(bad.Checks) != 1 || bad.Checks[0].Status != impl.StatusMismatch {
		t.Fatalf("tampered comparison = %+v, want a single mismatch", bad)
	}
	if bad.Checks[0].FirstDivergent != 0 {
		t.Fatalf("first divergent event = %d, want the first extend at index 0", bad.Checks[0].FirstDivergent)
	}
}

func TestReplayRejectsSeparatorAfterExtend(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	bad, err := r.Parse([]byte(
		line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") +
			line(0, impl.EventSeparator, impl.BankSHA256, dg(0x00, 32), "")))
	if err != nil {
		t.Fatalf("parse separator log: %v", err)
	}
	if _, err := r.Replay(bad, impl.BankSHA256); !errors.Is(err, impl.ErrSeparatorOrder) {
		t.Fatalf("separator after an extend must fail with ErrSeparatorOrder, got %v", err)
	}
	good, err := r.Parse([]byte(
		line(0, impl.EventSeparator, impl.BankSHA256, dg(0x00, 32), "") +
			line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "")))
	if err != nil {
		t.Fatalf("parse boot separator log: %v", err)
	}
	values, err := r.Replay(good, impl.BankSHA256)
	if err != nil {
		t.Fatalf("separator first must replay, got %v", err)
	}
	if len(values) != 1 || len(values[0].Value) != 32 {
		t.Fatalf("replay after boot separator = %+v, want one 32 byte value", values)
	}
}

func TestExplainReturnsExtensionChain(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	parsed, err := r.Parse([]byte(
		line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") +
			line(1, impl.EventExtend, impl.BankSHA256, dg(0x99, 32), "") +
			line(0, impl.EventExtend, impl.BankSHA256, dg(0x22, 32), "")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	values, err := r.Replay(parsed, impl.BankSHA256)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var final []byte
	for _, v := range values {
		if v.PCR == 0 {
			final = v.Value
		}
	}
	trace, err := r.Explain(0)
	if err != nil {
		t.Fatalf("explain pcr 0: %v", err)
	}
	if trace.PCR != 0 || trace.Bank != impl.BankSHA256 || len(trace.Steps) != 2 {
		t.Fatalf("trace = %+v, want two steps on pcr 0", trace)
	}
	if trace.Steps[0].Index != 0 || trace.Steps[1].Index != 2 {
		t.Fatalf("trace indexes = %d,%d, want the log indexes 0 and 2", trace.Steps[0].Index, trace.Steps[1].Index)
	}
	if !bytes.Equal(trace.Steps[0].After, trace.Steps[1].Before) {
		t.Fatalf("chain is broken: step 0 after does not equal step 1 before")
	}
	if !bytes.Equal(trace.Steps[1].After, final) {
		t.Fatalf("last trace step does not equal the replayed pcr value")
	}
	if _, err := r.Explain(5); !errors.Is(err, impl.ErrUnknownPCR) {
		t.Fatalf("explaining an untouched pcr must fail with ErrUnknownPCR, got %v", err)
	}
	if _, err := r.Explain(9); !errors.Is(err, impl.ErrPCRRange) {
		t.Fatalf("explaining an out of range pcr must fail with ErrPCRRange, got %v", err)
	}
}

func TestSnapshotRestoreKeepsLogAndCache(t *testing.T) {
	r := impl.NewEventLogReplayer(replayConfig())
	parsed, err := r.Parse([]byte(
		line(0, impl.EventExtend, impl.BankSHA256, dg(0x11, 32), "") +
			line(2, impl.EventExtend, impl.BankSHA256, dg(0x22, 32), "")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	values, err := r.Replay(parsed, impl.BankSHA256)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	data, err := r.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload must not be empty")
	}
	restored := impl.NewEventLogReplayer(replayConfig())
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := len(restored.Events()); got != 2 {
		t.Fatalf("restored event count = %d, want 2", got)
	}
	if restored.Fingerprint() != r.Fingerprint() {
		t.Fatalf("restored fingerprint = %q, want %q", restored.Fingerprint(), r.Fingerprint())
	}
	again, err := restored.Replay(parsed, impl.BankSHA256)
	if err != nil {
		t.Fatalf("replay after restore: %v", err)
	}
	if !sameValues(values, again) {
		t.Fatalf("restored replay differs from the original")
	}
	if err := restored.Restore([]byte("{not-a-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	if got := len(restored.Events()); got != 2 {
		t.Fatalf("rejected restore dropped events, now %d want 2", got)
	}
}
