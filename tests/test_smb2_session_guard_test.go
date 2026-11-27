package task

import (
	"errors"
	"testing"

	impl "gsb/batch51-go-safety-b-03"
)

func smbPolicy() impl.Smb2Policy {
	return impl.Smb2Policy{
		Dialects:           []uint16{impl.Dialect210, impl.Dialect302},
		RequireSigning:     false,
		MaxSessions:        2,
		MaxTreesPerSession: 2,
		MaxPending:         2,
		AllowedShares:      []string{"data", "home"},
	}
}

func smbNegotiate(t *testing.T, g *impl.Smb2SessionGuard, salt string) impl.NegotiateResponse {
	t.Helper()
	resp, err := g.Negotiate(impl.NegotiateRequest{
		ClientGuid:  "client-guid-1",
		Dialects:    []uint16{impl.Dialect210},
		PreauthSalt: salt,
	})
	if err != nil {
		t.Fatalf("negotiate failed: %v", err)
	}
	return resp
}

func smbSetup(t *testing.T, g *impl.Smb2SessionGuard) impl.Session {
	t.Helper()
	resp := smbNegotiate(t, g, "salt-1")
	session, err := g.SessionSetup(resp.SessionID, impl.SessionSetupRequest{
		MessageID:    1,
		SecurityBlob: "blob-alice",
		Username:     "alice",
	})
	if err != nil {
		t.Fatalf("session setup failed: %v", err)
	}
	return session
}

func TestNegotiateChoosesCommonDialect(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	resp, err := g.Negotiate(impl.NegotiateRequest{
		ClientGuid:  "client-guid-1",
		Dialects:    []uint16{impl.Dialect210},
		PreauthSalt: "salt-1",
	})
	if err != nil {
		t.Fatalf("negotiate failed: %v", err)
	}
	if resp.Dialect != impl.Dialect210 {
		t.Fatalf("dialect = %#x, want %#x", resp.Dialect, impl.Dialect210)
	}
	if resp.SessionID == "" {
		t.Fatalf("negotiate must open a pending session")
	}
	if resp.PreauthHash == "" {
		t.Fatalf("negotiate must seed the preauth hash")
	}
}

func TestNegotiateRejectsNoCommonDialect(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	_, err := g.Negotiate(impl.NegotiateRequest{
		ClientGuid:  "client-guid-1",
		Dialects:    []uint16{0x0100},
		PreauthSalt: "salt-1",
	})
	if !errors.Is(err, impl.ErrNoCommonDialect) {
		t.Fatalf("no common dialect error = %v, want ErrNoCommonDialect", err)
	}
}

func TestNegotiateEnforcesPendingLimit(t *testing.T) {
	cfg := smbPolicy()
	cfg.MaxPending = 1
	g := impl.NewSmb2SessionGuard(cfg)
	if _, err := g.Negotiate(impl.NegotiateRequest{ClientGuid: "client-a", Dialects: []uint16{impl.Dialect210}}); err != nil {
		t.Fatalf("first negotiate failed: %v", err)
	}
	_, err := g.Negotiate(impl.NegotiateRequest{ClientGuid: "client-b", Dialects: []uint16{impl.Dialect210}})
	if !errors.Is(err, impl.ErrPendingLimit) {
		t.Fatalf("second negotiate error = %v, want ErrPendingLimit", err)
	}
}

func TestNegotiateMarksSigningRequired(t *testing.T) {
	cfg := smbPolicy()
	cfg.RequireSigning = true
	g := impl.NewSmb2SessionGuard(cfg)
	resp, err := g.Negotiate(impl.NegotiateRequest{
		ClientGuid:  "client-guid-1",
		Dialects:    []uint16{impl.Dialect210},
		PreauthSalt: "salt-1",
	})
	if err != nil {
		t.Fatalf("negotiate failed: %v", err)
	}
	if !resp.SigningRequired {
		t.Fatalf("policy signing must be reflected in the response")
	}
}

func TestSessionSetupActivatesSession(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	resp := smbNegotiate(t, g, "salt-1")
	session, err := g.SessionSetup(resp.SessionID, impl.SessionSetupRequest{
		MessageID:    1,
		SecurityBlob: "blob-alice",
		Username:     "alice",
	})
	if err != nil {
		t.Fatalf("session setup failed: %v", err)
	}
	if session.State != impl.SessionActive {
		t.Fatalf("session state = %q, want %q", session.State, impl.SessionActive)
	}
	if session.ID != resp.SessionID {
		t.Fatalf("session id = %q, want %q", session.ID, resp.SessionID)
	}
	if session.Username != "alice" {
		t.Fatalf("session user = %q, want alice", session.Username)
	}
	if session.Trees != 0 {
		t.Fatalf("fresh session tree count = %d, want 0", session.Trees)
	}
	if session.Seq <= 0 {
		t.Fatalf("session sequence = %d, want a positive value", session.Seq)
	}
}

func TestSessionSetupRejectsUnknownSession(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	_, err := g.SessionSetup("session-does-not-exist", impl.SessionSetupRequest{
		MessageID:    1,
		SecurityBlob: "blob",
		Username:     "alice",
	})
	if !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("unknown session error = %v, want ErrUnknownSession", err)
	}
}

func TestSessionSetupRejectsBlankCredentials(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	resp := smbNegotiate(t, g, "salt-1")
	_, err := g.SessionSetup(resp.SessionID, impl.SessionSetupRequest{
		MessageID:    1,
		SecurityBlob: "",
		Username:     "alice",
	})
	if !errors.Is(err, impl.ErrBadCredentials) {
		t.Fatalf("blank blob error = %v, want ErrBadCredentials", err)
	}
}

func TestSessionSetupAdvancesPreauthDigest(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	resp := smbNegotiate(t, g, "salt-1")
	if _, err := g.SessionSetup(resp.SessionID, impl.SessionSetupRequest{
		MessageID:    1,
		SecurityBlob: "blob-alice",
		Username:     "alice",
	}); err != nil {
		t.Fatalf("session setup failed: %v", err)
	}
	digest, err := g.PreauthDigest(resp.SessionID)
	if err != nil {
		t.Fatalf("preauth digest failed: %v", err)
	}
	if digest == "" {
		t.Fatalf("preauth digest is empty after setup")
	}
	if digest == resp.PreauthHash {
		t.Fatalf("preauth digest must advance after session setup")
	}
}

func TestTreeConnectNormalizesPath(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	session := smbSetup(t, g)
	tree, err := g.TreeConnect(session.ID, "\\\\fileserver\\data\\docs")
	if err != nil {
		t.Fatalf("tree connect failed: %v", err)
	}
	if tree.Path != "//fileserver/data/docs" {
		t.Fatalf("tree path = %q, want //fileserver/data/docs", tree.Path)
	}
	if tree.Share != "data" {
		t.Fatalf("tree share = %q, want data", tree.Share)
	}
	if tree.ID == "" || tree.SessionID != session.ID {
		t.Fatalf("tree identity = %q/%q, want a non empty id on %q", tree.ID, tree.SessionID, session.ID)
	}
}

func TestTreeConnectRejectsParentTraversal(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	session := smbSetup(t, g)
	_, err := g.TreeConnect(session.ID, "\\\\fileserver\\data\\..\\secret")
	if !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("traversal error = %v, want ErrBadPath", err)
	}
}

func TestTreeConnectRejectsAlternateDataStream(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	session := smbSetup(t, g)
	_, err := g.TreeConnect(session.ID, "\\\\fileserver\\data\\report.txt:secret")
	if !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("alternate data stream error = %v, want ErrBadPath", err)
	}
}

func TestTreeConnectRejectsReservedName(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	session := smbSetup(t, g)
	_, err := g.TreeConnect(session.ID, "\\\\fileserver\\data\\CON")
	if !errors.Is(err, impl.ErrBadPath) {
		t.Fatalf("reserved name error = %v, want ErrBadPath", err)
	}
}

func TestTreeConnectEnforcesPerSessionLimit(t *testing.T) {
	cfg := smbPolicy()
	cfg.MaxTreesPerSession = 1
	g := impl.NewSmb2SessionGuard(cfg)
	session := smbSetup(t, g)
	if _, err := g.TreeConnect(session.ID, "\\\\fileserver\\data\\docs"); err != nil {
		t.Fatalf("first tree connect failed: %v", err)
	}
	_, err := g.TreeConnect(session.ID, "\\\\fileserver\\home\\alice")
	if !errors.Is(err, impl.ErrTreeLimit) {
		t.Fatalf("tree limit error = %v, want ErrTreeLimit", err)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	cfg := smbPolicy()
	g := impl.NewSmb2SessionGuard(cfg)
	session := smbSetup(t, g)
	if _, err := g.TreeConnect(session.ID, "\\\\fileserver\\data\\docs"); err != nil {
		t.Fatalf("tree connect failed: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot payload is empty")
	}
	restored := impl.NewSmb2SessionGuard(cfg)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	got, ok := restored.Session(session.ID)
	if !ok {
		t.Fatalf("session missing after restore")
	}
	if got.State != impl.SessionActive {
		t.Fatalf("restored session state = %q, want %q", got.State, impl.SessionActive)
	}
	if trees := restored.Trees(session.ID); len(trees) != 1 {
		t.Fatalf("restored trees = %d, want 1", len(trees))
	}
}

func TestRestoreRejectsCorruptPayload(t *testing.T) {
	g := impl.NewSmb2SessionGuard(smbPolicy())
	if err := g.Restore([]byte("broken-session-snapshot")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt restore error = %v, want ErrBadSnapshot", err)
	}
}
