package task

import (
	"context"
	"errors"
	"sync"
	"testing"

	impl "gsb/batch51-go-safety-b-11"
)

func guardLimits() impl.Limits {
	return impl.Limits{MaxSessions: 4, MaxCmdWindow: 8, MaxConnsPerSession: 2}
}

func loginOn(t *testing.T, g *impl.IscsiSessionGuardP3, conns ...string) impl.LoginResult {
	t.Helper()
	res, err := g.Login(context.Background(), impl.LoginRequest{
		Initiator: "iqn.1994-05.com.example:backup-a",
		Target:    "iqn.2001-04.com.example:storage",
		Portal:    "10.20.0.11:3260",
		ConnIDs:   conns,
		Params:    map[string]string{"AuthMethod": "CHAP"},
	})
	if err != nil {
		t.Fatalf("login must succeed, got %v", err)
	}
	return res
}

func TestLoginRejectsBlankIdentity(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	base := impl.LoginRequest{
		Initiator: "iqn.1994-05.com.example:backup-a",
		Target:    "iqn.2001-04.com.example:storage",
		Portal:    "10.20.0.11:3260",
		ConnIDs:   []string{"conn-1"},
		Params:    map[string]string{"AuthMethod": "None"},
	}
	cases := []struct {
		name string
		req  impl.LoginRequest
	}{
		{"blank initiator", func() impl.LoginRequest { r := base; r.Initiator = "   "; return r }()},
		{"blank target", func() impl.LoginRequest { r := base; r.Target = ""; return r }()},
		{"blank portal", func() impl.LoginRequest { r := base; r.Portal = " "; return r }()},
		{"no connection", func() impl.LoginRequest { r := base; r.ConnIDs = nil; return r }()},
		{"duplicate connection", func() impl.LoginRequest { r := base; r.ConnIDs = []string{"conn-1", "conn-1"}; return r }()},
	}
	for _, c := range cases {
		if _, err := g.Login(context.Background(), c.req); !errors.Is(err, impl.ErrBadLogin) {
			t.Fatalf("%s must be rejected with ErrBadLogin, got %v", c.name, err)
		}
		if _, ok := g.Session("session-1"); ok {
			t.Fatalf("%s must not leave a session behind", c.name)
		}
	}
}

func TestLoginNegotiatesTextKeys(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	res, err := g.Login(context.Background(), impl.LoginRequest{
		Initiator: "iqn.1994-05.com.example:backup-a",
		Target:    "iqn.2001-04.com.example:storage",
		Portal:    "10.20.0.11:3260",
		ConnIDs:   []string{"conn-1"},
		Params: map[string]string{
			"MaxRecvDataSegmentLength": "8192",
			"AuthMethod":               "CHAP",
			"ErrorRecoveryLevel":       "0",
		},
	})
	if err != nil {
		t.Fatalf("login must succeed, got %v", err)
	}
	if res.Status != impl.StatusGood {
		t.Fatalf("login status = %d, want %d", res.Status, impl.StatusGood)
	}
	if res.SessionID == "" {
		t.Fatalf("login must return a session id")
	}
	if res.CmdSN != 1 {
		t.Fatalf("first cmdsn = %d, want 1", res.CmdSN)
	}
	want := []string{"AuthMethod", "ErrorRecoveryLevel", "MaxRecvDataSegmentLength"}
	if len(res.TextKeys) != len(want) {
		t.Fatalf("text keys = %v, want %v", res.TextKeys, want)
	}
	for i, k := range want {
		if res.TextKeys[i] != k {
			t.Fatalf("text keys = %v, want the sorted list %v", res.TextKeys, want)
		}
	}
	bad := impl.LoginRequest{
		Initiator: "iqn.1994-05.com.example:backup-a",
		Target:    "iqn.2001-04.com.example:storage",
		Portal:    "10.20.0.11:3260",
		ConnIDs:   []string{"conn-9"},
		Params:    map[string]string{"AuthMethod": "KRB5"},
	}
	if _, err := g.Login(context.Background(), bad); !errors.Is(err, impl.ErrLoginRejected) {
		t.Fatalf("unknown auth method must be rejected with ErrLoginRejected, got %v", err)
	}
	bad.Params = map[string]string{"AuthMethod": "CHAP", "HeaderDigest": "  "}
	if _, err := g.Login(context.Background(), bad); !errors.Is(err, impl.ErrLoginRejected) {
		t.Fatalf("blank parameter value must be rejected with ErrLoginRejected, got %v", err)
	}
}

func TestLoginEnforcesSessionLimit(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(impl.Limits{MaxSessions: 1, MaxCmdWindow: 8, MaxConnsPerSession: 2})
	if _, err := g.Login(context.Background(), impl.LoginRequest{
		Initiator: "iqn.a", Target: "iqn.t", Portal: "10.0.0.1:3260",
		ConnIDs: []string{"conn-1"}, Params: map[string]string{"AuthMethod": "None"},
	}); err != nil {
		t.Fatalf("first login must succeed, got %v", err)
	}
	if _, err := g.Login(context.Background(), impl.LoginRequest{
		Initiator: "iqn.b", Target: "iqn.t", Portal: "10.0.0.1:3260",
		ConnIDs: []string{"conn-2"}, Params: map[string]string{"AuthMethod": "None"},
	}); !errors.Is(err, impl.ErrSessionLimit) {
		t.Fatalf("second session must hit ErrSessionLimit, got %v", err)
	}
	wide := impl.NewIscsiSessionGuardP3(impl.Limits{MaxSessions: 2, MaxCmdWindow: 8, MaxConnsPerSession: 1})
	if _, err := wide.Login(context.Background(), impl.LoginRequest{
		Initiator: "iqn.a", Target: "iqn.t", Portal: "10.0.0.1:3260",
		ConnIDs: []string{"conn-1", "conn-2"}, Params: map[string]string{"AuthMethod": "None"},
	}); !errors.Is(err, impl.ErrBadLogin) {
		t.Fatalf("more connections than allowed must be rejected with ErrBadLogin, got %v", err)
	}
}

func TestExecuteAllocatesMonotonicCmdSN(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	res := loginOn(t, g, "conn-1")
	for i := 1; i <= 3; i++ {
		st, err := g.Execute(res.SessionID, impl.Command{ITT: uint32(i), ConnID: "conn-1"})
		if err != nil {
			t.Fatalf("execute command %d: %v", i, err)
		}
		if st != impl.StatusGood {
			t.Fatalf("command %d status = %d, want %d", i, st, impl.StatusGood)
		}
	}
	info, ok := g.Session(res.SessionID)
	if !ok {
		t.Fatalf("session must stay readable")
	}
	if info.NextCmdSN != 4 {
		t.Fatalf("next cmdsn = %d, want 4 after three commands", info.NextCmdSN)
	}
	if info.ExpCmdSN != 1 {
		t.Fatalf("exp cmdsn = %d, want 1 while nothing is acknowledged", info.ExpCmdSN)
	}
	stream := g.Responses("conn-1")
	if len(stream) != 3 {
		t.Fatalf("response stream length = %d, want 3", len(stream))
	}
	for i, r := range stream {
		if r.CmdSN != uint32(i+1) {
			t.Fatalf("response %d cmdsn = %d, want %d", i, r.CmdSN, i+1)
		}
		if r.StatSN != uint32(i+1) {
			t.Fatalf("response %d statsn = %d, want %d", i, r.StatSN, i+1)
		}
		if r.ITT != uint32(i+1) {
			t.Fatalf("response %d itt = %d, want %d", i, r.ITT, i+1)
		}
	}
}

func TestExecuteWindowExhaustedDoesNotReuse(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(impl.Limits{MaxSessions: 2, MaxCmdWindow: 2, MaxConnsPerSession: 1})
	res := loginOn(t, g, "conn-1")
	for i := 1; i <= 2; i++ {
		if _, err := g.Execute(res.SessionID, impl.Command{ITT: uint32(i), ConnID: "conn-1"}); err != nil {
			t.Fatalf("command %d inside the window must run, got %v", i, err)
		}
	}
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 3, ConnID: "conn-1"}); !errors.Is(err, impl.ErrWindowExhausted) {
		t.Fatalf("third command must hit ErrWindowExhausted, got %v", err)
	}
	if _, ok := g.Response(res.SessionID, 3); ok {
		t.Fatalf("a rejected command must not leave a response behind")
	}
	if err := g.Ack(res.SessionID, 3); err != nil {
		t.Fatalf("ack of the full window: %v", err)
	}
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 3, ConnID: "conn-1"}); err != nil {
		t.Fatalf("command after ack must run, got %v", err)
	}
	r, ok := g.Response(res.SessionID, 3)
	if !ok {
		t.Fatalf("acknowledged command must record a response")
	}
	if r.CmdSN != 3 {
		t.Fatalf("reissued command got cmdsn %d, want 3 without recycling 1 or 2", r.CmdSN)
	}
	info, _ := g.Session(res.SessionID)
	if info.NextCmdSN != 4 {
		t.Fatalf("next cmdsn = %d, want 4", info.NextCmdSN)
	}
}

func TestAckAdvancesWindowAndRejectsOutOfRange(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	res := loginOn(t, g, "conn-1")
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 1, ConnID: "conn-1"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := g.Ack(res.SessionID, 9); !errors.Is(err, impl.ErrCmdSNInvalid) {
		t.Fatalf("ack past next cmdsn must fail with ErrCmdSNInvalid, got %v", err)
	}
	if err := g.Ack(res.SessionID, 0); !errors.Is(err, impl.ErrCmdSNInvalid) {
		t.Fatalf("ack behind exp cmdsn must fail with ErrCmdSNInvalid, got %v", err)
	}
	if err := g.Ack(res.SessionID, 2); err != nil {
		t.Fatalf("ack of the issued window: %v", err)
	}
	info, _ := g.Session(res.SessionID)
	if info.ExpCmdSN != 2 {
		t.Fatalf("exp cmdsn = %d, want 2", info.ExpCmdSN)
	}
	if info.NextCmdSN != 2 {
		t.Fatalf("next cmdsn = %d, want 2", info.NextCmdSN)
	}
	if err := g.Ack("session-404", 2); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("ack on an unknown session must fail with ErrUnknownSession, got %v", err)
	}
}

func TestDigestFailureDropsPDUWithoutAdvancing(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	res := loginOn(t, g, "conn-1")
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 10, ConnID: "conn-1"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	info, _ := g.Session(res.SessionID)
	if info.NextCmdSN != 2 {
		t.Fatalf("next cmdsn = %d, want 2 before the digest error", info.NextCmdSN)
	}
	conn, ok := g.Conn("conn-1")
	if !ok || conn.StatSN != 1 {
		t.Fatalf("connection statsn = %d ok=%v, want 1", conn.StatSN, ok)
	}
	if err := g.DigestFailed("conn-1", 1); err != nil {
		t.Fatalf("digest failure on a known pdu: %v", err)
	}
	info, _ = g.Session(res.SessionID)
	if info.NextCmdSN != 1 {
		t.Fatalf("next cmdsn = %d after the digest drop, want the pdu position 1 back", info.NextCmdSN)
	}
	if info.DigestDrops != 1 {
		t.Fatalf("digest drop counter = %d, want 1", info.DigestDrops)
	}
	conn, _ = g.Conn("conn-1")
	if conn.StatSN != 0 {
		t.Fatalf("connection statsn = %d after the digest drop, want 0", conn.StatSN)
	}
	if _, ok := g.Response(res.SessionID, 10); ok {
		t.Fatalf("the dropped pdu must not keep its response")
	}
	st, err := g.Execute(res.SessionID, impl.Command{ITT: 10, ConnID: "conn-1"})
	if err != nil || st != impl.StatusGood {
		t.Fatalf("reissue after the digest drop got status=%d err=%v, want a clean run", st, err)
	}
	r, ok := g.Response(res.SessionID, 10)
	if !ok || r.CmdSN != 1 {
		t.Fatalf("reissued command cmdsn = %d ok=%v, want 1", r.CmdSN, ok)
	}
	if err := g.DigestFailed("conn-404", 1); !errors.Is(err, impl.ErrUnknownConn) {
		t.Fatalf("digest failure on an unknown connection must fail with ErrUnknownConn, got %v", err)
	}
}

func TestImmediateCommandSkipsWindow(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(impl.Limits{MaxSessions: 2, MaxCmdWindow: 1, MaxConnsPerSession: 1})
	res := loginOn(t, g, "conn-1")
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 1, ConnID: "conn-1"}); err != nil {
		t.Fatalf("regular command must fill the window, got %v", err)
	}
	st, err := g.Execute(res.SessionID, impl.Command{ITT: 2, ConnID: "conn-1", Immediate: true})
	if err != nil {
		t.Fatalf("immediate command must bypass a full window, got %v", err)
	}
	if st != impl.StatusGood {
		t.Fatalf("immediate command status = %d, want %d", st, impl.StatusGood)
	}
	info, _ := g.Session(res.SessionID)
	if info.NextCmdSN != 2 {
		t.Fatalf("next cmdsn = %d after an immediate command, want 2", info.NextCmdSN)
	}
	if info.Immediate != 1 {
		t.Fatalf("immediate counter = %d, want 1", info.Immediate)
	}
	r, ok := g.Response(res.SessionID, 2)
	if !ok {
		t.Fatalf("immediate command must still record a response")
	}
	if !r.Immediate {
		t.Fatalf("the response of an immediate command must be flagged as immediate")
	}
	if r.CmdSN != 0 {
		t.Fatalf("immediate response cmdsn = %d, want 0 because it is outside the window", r.CmdSN)
	}
}

func TestDuplicateInFlightITTExecutesOnce(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	res := loginOn(t, g, "conn-1")
	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	good := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := g.Execute(res.SessionID, impl.Command{ITT: 77, ConnID: "conn-1"})
			if err != nil {
				return
			}
			if st == impl.StatusGood {
				mu.Lock()
				good++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if got := g.Attempts(res.SessionID, 77); got != 1 {
		t.Fatalf("command body ran %d times, want exactly 1", got)
	}
	if good != 1 {
		t.Fatalf("%d submitters observed a good status, want exactly 1", good)
	}
	if _, ok := g.Response(res.SessionID, 77); !ok {
		t.Fatalf("the single run must leave one retrievable response")
	}
}

func TestExecuteUnknownSessionAndLostConnection(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	if _, err := g.Execute("session-404", impl.Command{ITT: 1, ConnID: "conn-1"}); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("execute on an unknown session must fail with ErrUnknownSession, got %v", err)
	}
	res := loginOn(t, g, "conn-1", "conn-2")
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 0, ConnID: "conn-1"}); !errors.Is(err, impl.ErrBadCommand) {
		t.Fatalf("itt 0 must fail with ErrBadCommand, got %v", err)
	}
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 5, ConnID: "conn-404"}); !errors.Is(err, impl.ErrUnknownConn) {
		t.Fatalf("a foreign connection must fail with ErrUnknownConn, got %v", err)
	}
	if err := g.ConnectionLost("conn-1", "fibre cut"); err != nil {
		t.Fatalf("connection lost: %v", err)
	}
	conn, ok := g.Conn("conn-1")
	if !ok || !conn.Lost {
		t.Fatalf("connection must be marked lost, got %+v ok=%v", conn, ok)
	}
	if conn.Reason != "fibre cut" {
		t.Fatalf("lost reason = %q, want the operator note", conn.Reason)
	}
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 6, ConnID: "conn-1"}); !errors.Is(err, impl.ErrUnknownConn) {
		t.Fatalf("a lost connection must reject commands with ErrUnknownConn, got %v", err)
	}
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 7, ConnID: "conn-2"}); err != nil {
		t.Fatalf("the surviving connection must still run commands, got %v", err)
	}
	if err := g.ConnectionLost("conn-404", "x"); !errors.Is(err, impl.ErrUnknownConn) {
		t.Fatalf("lost on an unknown connection must fail with ErrUnknownConn, got %v", err)
	}
}

func TestTaskManageAssignsCmdSNInOrder(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	res := loginOn(t, g, "conn-1")
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 1, ConnID: "conn-1"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got []uint32
	record := func(cmdSN uint32) error {
		got = append(got, cmdSN)
		return nil
	}
	if err := g.TaskManage(res.SessionID, 1, record); err != nil {
		t.Fatalf("first task manage: %v", err)
	}
	if err := g.TaskManage(res.SessionID, 2, record); err != nil {
		t.Fatalf("second task manage: %v", err)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("task callbacks saw %v, want [2 3] in submission order", got)
	}
	info, _ := g.Session(res.SessionID)
	if info.NextCmdSN != 4 {
		t.Fatalf("next cmdsn = %d after two tasks, want 4", info.NextCmdSN)
	}
}

func TestTaskManageRejectsStaleReference(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	res := loginOn(t, g, "conn-1")
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 1, ConnID: "conn-1"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	called := false
	fn := func(uint32) error { called = true; return nil }
	if err := g.TaskManage(res.SessionID, 5, fn); !errors.Is(err, impl.ErrCmdSNInvalid) {
		t.Fatalf("reference past the issued waterline must fail with ErrCmdSNInvalid, got %v", err)
	}
	if err := g.TaskManage(res.SessionID, 0, fn); !errors.Is(err, impl.ErrCmdSNInvalid) {
		t.Fatalf("reference 0 must fail with ErrCmdSNInvalid, got %v", err)
	}
	if err := g.TaskManage(res.SessionID, 1, nil); !errors.Is(err, impl.ErrBadCommand) {
		t.Fatalf("nil task body must fail with ErrBadCommand, got %v", err)
	}
	if called {
		t.Fatalf("a rejected task must never run")
	}
	info, _ := g.Session(res.SessionID)
	if info.NextCmdSN != 2 {
		t.Fatalf("next cmdsn = %d, want 2 because rejected tasks must not allocate", info.NextCmdSN)
	}
}

func TestSessionIsolation(t *testing.T) {
	g := impl.NewIscsiSessionGuardP3(guardLimits())
	a := loginOn(t, g, "conn-a")
	b := loginOn(t, g, "conn-b")
	for i := 1; i <= 2; i++ {
		if _, err := g.Execute(a.SessionID, impl.Command{ITT: uint32(i), ConnID: "conn-a"}); err != nil {
			t.Fatalf("session a command %d: %v", i, err)
		}
	}
	if _, err := g.Execute(b.SessionID, impl.Command{ITT: 1, ConnID: "conn-b"}); err != nil {
		t.Fatalf("session b command: %v", err)
	}
	infoA, _ := g.Session(a.SessionID)
	infoB, _ := g.Session(b.SessionID)
	if infoA.NextCmdSN != 3 {
		t.Fatalf("session a next cmdsn = %d, want 3", infoA.NextCmdSN)
	}
	if infoB.NextCmdSN != 2 {
		t.Fatalf("session b next cmdsn = %d, want 2 and untouched by session a", infoB.NextCmdSN)
	}
	if len(g.Responses("conn-a")) != 2 {
		t.Fatalf("session a response stream = %d entries, want 2", len(g.Responses("conn-a")))
	}
	if len(g.Responses("conn-b")) != 1 {
		t.Fatalf("session b response stream = %d entries, want 1", len(g.Responses("conn-b")))
	}
	if _, ok := g.Response(b.SessionID, 2); ok {
		t.Fatalf("session b must not see the itt of session a")
	}
}

func TestSnapshotRestoreKeepsWindow(t *testing.T) {
	limits := guardLimits()
	g := impl.NewIscsiSessionGuardP3(limits)
	res := loginOn(t, g, "conn-1")
	for i := 1; i <= 2; i++ {
		if _, err := g.Execute(res.SessionID, impl.Command{ITT: uint32(i), ConnID: "conn-1"}); err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
	}
	if err := g.Ack(res.SessionID, 3); err != nil {
		t.Fatalf("ack: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewIscsiSessionGuardP3(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	info, ok := restored.Session(res.SessionID)
	if !ok {
		t.Fatalf("restored guard lost the session")
	}
	if info.NextCmdSN != 3 || info.ExpCmdSN != 3 {
		t.Fatalf("restored window=(%d,%d), want (3,3)", info.NextCmdSN, info.ExpCmdSN)
	}
	r, ok := restored.Response(res.SessionID, 2)
	if !ok || r.StatSN != 2 {
		t.Fatalf("restored response statsn = %d ok=%v, want 2", r.StatSN, ok)
	}
	if len(restored.Responses("conn-1")) != 2 {
		t.Fatalf("restored response stream = %d entries, want 2", len(restored.Responses("conn-1")))
	}
	if _, err := restored.Execute(res.SessionID, impl.Command{ITT: 3, ConnID: "conn-1"}); err != nil {
		t.Fatalf("execute after restore: %v", err)
	}
	r, ok = restored.Response(res.SessionID, 3)
	if !ok || r.CmdSN != 3 || r.StatSN != 3 {
		t.Fatalf("post restore command got cmdsn=%d statsn=%d ok=%v, want 3 and 3", r.CmdSN, r.StatSN, ok)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	limits := guardLimits()
	g := impl.NewIscsiSessionGuardP3(limits)
	res := loginOn(t, g, "conn-1")
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 1, ConnID: "conn-1"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := g.Restore([]byte("{\"sessions\":[{\"id\":")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	info, ok := g.Session(res.SessionID)
	if !ok {
		t.Fatalf("a rejected restore must not touch the live session")
	}
	if info.NextCmdSN != 2 || info.ExpCmdSN != 1 {
		t.Fatalf("live window=(%d,%d) after a rejected restore, want (2,1)", info.NextCmdSN, info.ExpCmdSN)
	}
	if _, err := g.Execute(res.SessionID, impl.Command{ITT: 2, ConnID: "conn-1"}); err != nil {
		t.Fatalf("the live session must keep working, got %v", err)
	}
}
