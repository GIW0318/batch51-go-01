package task

import (
	"encoding/binary"
	"errors"
	"testing"

	impl "gsb/batch51-go-safety-b-15"
)

func channelLimits() impl.Limits {
	return impl.Limits{MaxChannels: 8, MaxLifetime: 3600, MaxChunkSize: 1024}
}

// frame builds one chunk: message type, chunk type, sequence, request id, body.
func frame(msgType, chunkType byte, seq, requestID uint32, body string) []byte {
	out := make([]byte, impl.ChunkHeaderSize+len(body))
	out[0] = msgType
	out[1] = chunkType
	binary.LittleEndian.PutUint32(out[2:6], seq)
	binary.LittleEndian.PutUint32(out[6:10], requestID)
	copy(out[impl.ChunkHeaderSize:], body)
	return out
}

func mustOpenChannel(t *testing.T, g *impl.OpcuaSecureChannel) impl.ChannelInfo {
	t.Helper()
	info, err := g.Open("opc.tcp://plant-1:4840/ua", impl.OpenSecureChannel{
		SecurityPolicy: impl.PolicyBasic256Sha256,
		SecurityMode:   impl.ModeSignAndEncrypt,
		ClientNonce:    []byte("nonce-0001"),
		Lifetime:       1200,
	})
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	return info
}

func TestOpenValidatesEndpointPolicyAndMode(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	if _, err := g.Open("  ", impl.OpenSecureChannel{SecurityPolicy: impl.PolicyNone, SecurityMode: impl.ModeNone}); !errors.Is(err, impl.ErrBadEndpoint) {
		t.Fatalf("blank endpoint must fail with ErrBadEndpoint, got %v", err)
	}
	if _, err := g.Open("opc.tcp://x:4840", impl.OpenSecureChannel{SecurityPolicy: "RC4", SecurityMode: impl.ModeSign}); !errors.Is(err, impl.ErrBadPolicy) {
		t.Fatalf("an unsupported policy must fail with ErrBadPolicy, got %v", err)
	}
	if _, err := g.Open("opc.tcp://x:4840", impl.OpenSecureChannel{SecurityPolicy: impl.PolicyNone, SecurityMode: impl.ModeSignAndEncrypt}); !errors.Is(err, impl.ErrBadMode) {
		t.Fatalf("signing under the None policy must fail with ErrBadMode, got %v", err)
	}
	if _, err := g.Open("opc.tcp://x:4840", impl.OpenSecureChannel{SecurityPolicy: impl.PolicyBasic256Sha256, SecurityMode: impl.ModeNone}); !errors.Is(err, impl.ErrBadMode) {
		t.Fatalf("the None mode under a secure policy must fail with ErrBadMode, got %v", err)
	}
}

func TestOpenInitialisesSequences(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	if info.ChannelID == "" {
		t.Fatalf("open must return a channel id")
	}
	if info.TokenID != 1 {
		t.Fatalf("first token id = %d, want 1", info.TokenID)
	}
	if info.SendSeq != 1 {
		t.Fatalf("send sequence starts at %d, want 1", info.SendSeq)
	}
	if info.RecvSeq != 0 {
		t.Fatalf("receive sequence starts at %d, want 0", info.RecvSeq)
	}
	if info.Policy != impl.PolicyBasic256Sha256 || info.Mode != impl.ModeSignAndEncrypt {
		t.Fatalf("channel policy/mode = %q/%q, want the negotiated pair echoed", info.Policy, info.Mode)
	}
	state, ok := g.Channel(info.ChannelID)
	if !ok {
		t.Fatalf("the channel must be readable after open")
	}
	if state.TokenID != 1 || state.SendSeq != 1 || state.RecvSeq != 0 {
		t.Fatalf("channel state = %+v, want token 1 send 1 recv 0", state)
	}
	if state.State != impl.ChannelOpen {
		t.Fatalf("channel state = %q, want %q", state.State, impl.ChannelOpen)
	}
}

func TestAcceptFinalChunkReturnsMessage(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	msgs, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 7, "read request"))
	if err != nil {
		t.Fatalf("accept a final chunk: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("accept returned %d messages, want 1", len(msgs))
	}
	if msgs[0].Type != impl.MsgTypeMessage {
		t.Fatalf("message type = %q, want %q", msgs[0].Type, impl.MsgTypeMessage)
	}
	if msgs[0].RequestID != 7 {
		t.Fatalf("request id = %d, want 7", msgs[0].RequestID)
	}
	if string(msgs[0].Body) != "read request" {
		t.Fatalf("message body = %q, want the chunk payload", msgs[0].Body)
	}
	if msgs[0].ChannelID != info.ChannelID {
		t.Fatalf("message channel = %q, want %q", msgs[0].ChannelID, info.ChannelID)
	}
	state, _ := g.Channel(info.ChannelID)
	if state.RecvSeq != 1 {
		t.Fatalf("receive sequence = %d after one chunk, want 1", state.RecvSeq)
	}
	stats, ok := g.Stats(info.ChannelID)
	if !ok || stats.Received != 1 || stats.Assembled != 1 {
		t.Fatalf("stats = %+v ok=%v, want received 1 assembled 1", stats, ok)
	}
	if len(g.Messages(info.ChannelID)) != 1 {
		t.Fatalf("the message log must hold the assembled message")
	}
}

func TestAcceptIntermediateThenFinalAssembles(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	first, err := g.Accept(info.ChannelID, frame('M', 'C', 1, 21, "hello "))
	if err != nil {
		t.Fatalf("accept the intermediate chunk: %v", err)
	}
	if len(first) != 0 {
		t.Fatalf("an intermediate chunk must not complete a message, got %d", len(first))
	}
	state, _ := g.Channel(info.ChannelID)
	if state.RecvSeq != 1 {
		t.Fatalf("receive sequence = %d after the intermediate chunk, want 1", state.RecvSeq)
	}
	second, err := g.Accept(info.ChannelID, frame('M', 'F', 2, 21, "world"))
	if err != nil {
		t.Fatalf("accept the final chunk: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("the final chunk completed %d messages, want 1", len(second))
	}
	if string(second[0].Body) != "hello world" {
		t.Fatalf("reassembled body = %q, want hello world", second[0].Body)
	}
	if second[0].RequestID != 21 {
		t.Fatalf("reassembled request id = %d, want 21", second[0].RequestID)
	}
	state, _ = g.Channel(info.ChannelID)
	if state.RecvSeq != 2 {
		t.Fatalf("receive sequence = %d after two chunks, want 2", state.RecvSeq)
	}
}

func TestAcceptRejectsMalformedChunk(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(impl.Limits{MaxChannels: 8, MaxLifetime: 3600, MaxChunkSize: 32})
	info := mustOpenChannel(t, g)
	if _, err := g.Accept(info.ChannelID, []byte{'M', 'F', 1}); !errors.Is(err, impl.ErrShortChunk) {
		t.Fatalf("a chunk shorter than the header must fail with ErrShortChunk, got %v", err)
	}
	if _, err := g.Accept(info.ChannelID, frame('Z', 'F', 1, 1, "x")); !errors.Is(err, impl.ErrBadChunkType) {
		t.Fatalf("an unknown message type must fail with ErrBadChunkType, got %v", err)
	}
	if _, err := g.Accept(info.ChannelID, frame('M', 'Z', 1, 1, "x")); !errors.Is(err, impl.ErrBadChunkType) {
		t.Fatalf("an unknown chunk type must fail with ErrBadChunkType, got %v", err)
	}
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 1, "0123456789012345678901234567890123456789")); !errors.Is(err, impl.ErrChunkTooLarge) {
		t.Fatalf("an oversized chunk must fail with ErrChunkTooLarge, got %v", err)
	}
	state, _ := g.Channel(info.ChannelID)
	if state.RecvSeq != 0 {
		t.Fatalf("receive sequence = %d after malformed chunks, want 0", state.RecvSeq)
	}
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 2, "ok")); err != nil {
		t.Fatalf("a well formed chunk after the rejects must be accepted, got %v", err)
	}
}

func TestAcceptRejectsDuplicateSequence(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 5, "one")); err != nil {
		t.Fatalf("accept the first chunk: %v", err)
	}
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 5, "one again")); !errors.Is(err, impl.ErrSequenceReplay) {
		t.Fatalf("a replayed sequence must fail with ErrSequenceReplay, got %v", err)
	}
	state, _ := g.Channel(info.ChannelID)
	if state.RecvSeq != 1 {
		t.Fatalf("receive sequence = %d after a replay, want it to stay 1", state.RecvSeq)
	}
	stats, _ := g.Stats(info.ChannelID)
	if stats.Duplicates != 1 {
		t.Fatalf("duplicate counter = %d, want 1", stats.Duplicates)
	}
	if len(g.Messages(info.ChannelID)) != 1 {
		t.Fatalf("a replayed chunk must not append a second message")
	}
}

func TestAcceptRejectsSequenceGap(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 3, 9, "late")); !errors.Is(err, impl.ErrSequenceGap) {
		t.Fatalf("a chunk that skips sequence 1 and 2 must fail with ErrSequenceGap, got %v", err)
	}
	state, _ := g.Channel(info.ChannelID)
	if state.RecvSeq != 0 {
		t.Fatalf("receive sequence = %d after a gap, want 0", state.RecvSeq)
	}
	stats, _ := g.Stats(info.ChannelID)
	if stats.Gaps != 1 {
		t.Fatalf("gap counter = %d, want 1", stats.Gaps)
	}
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 9, "first")); err != nil {
		t.Fatalf("the missing chunk must still be accepted, got %v", err)
	}
}

func TestAcceptUnknownChannel(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	if _, err := g.Accept("channel-404", frame('M', 'F', 1, 1, "x")); !errors.Is(err, impl.ErrUnknownChannel) {
		t.Fatalf("accept on an unknown channel must fail with ErrUnknownChannel, got %v", err)
	}
	if _, ok := g.Stats("channel-404"); ok {
		t.Fatalf("an unknown channel must have no stats")
	}
	if _, ok := g.Channel("channel-404"); ok {
		t.Fatalf("an unknown channel must not be readable")
	}
	if _, err := g.Renew("channel-404", impl.RenewRequest{TokenID: 1}); !errors.Is(err, impl.ErrUnknownChannel) {
		t.Fatalf("renew on an unknown channel must fail with ErrUnknownChannel, got %v", err)
	}
}

func TestRenewRotatesTokenAndKeepsSequences(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 1, "one")); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := g.Renew(info.ChannelID, impl.RenewRequest{TokenID: 99}); !errors.Is(err, impl.ErrBadToken) {
		t.Fatalf("renew with a foreign token must fail with ErrBadToken, got %v", err)
	}
	renewed, err := g.Renew(info.ChannelID, impl.RenewRequest{TokenID: 1, Lifetime: 90000})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if renewed.TokenID != 2 {
		t.Fatalf("renewed token = %d, want 2", renewed.TokenID)
	}
	if renewed.Lifetime != channelLimits().MaxLifetime {
		t.Fatalf("renewed lifetime = %d, want it clamped to %d", renewed.Lifetime, channelLimits().MaxLifetime)
	}
	if renewed.RecvSeq != 1 || renewed.SendSeq != 1 {
		t.Fatalf("renewed sequences send/recv = %d/%d, want 1/1 untouched", renewed.SendSeq, renewed.RecvSeq)
	}
	if _, err := g.Renew(info.ChannelID, impl.RenewRequest{TokenID: 1}); !errors.Is(err, impl.ErrBadToken) {
		t.Fatalf("renewing with the retired token must fail with ErrBadToken, got %v", err)
	}
	state, _ := g.Channel(info.ChannelID)
	if state.TokenID != 2 {
		t.Fatalf("channel token = %d after renew, want 2", state.TokenID)
	}
}

func TestCreateSessionBindsUser(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	if _, err := g.CreateSession(info.ChannelID, "   ", []byte("token-a")); !errors.Is(err, impl.ErrBadUser) {
		t.Fatalf("blank user must fail with ErrBadUser, got %v", err)
	}
	if _, err := g.CreateSession(info.ChannelID, "operator", nil); !errors.Is(err, impl.ErrBadAuthToken) {
		t.Fatalf("a missing auth token must fail with ErrBadAuthToken, got %v", err)
	}
	sess, err := g.CreateSession(info.ChannelID, "operator", []byte("token-a"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if sess.User != "operator" || sess.ChannelID != info.ChannelID {
		t.Fatalf("session = %+v, want the user and channel echoed", sess)
	}
	if sess.State != impl.SessionActive {
		t.Fatalf("session state = %q, want %q", sess.State, impl.SessionActive)
	}
	if sess.TokenID != 1 {
		t.Fatalf("session token = %d, want the channel token 1", sess.TokenID)
	}
	if _, err := g.CreateSession("channel-404", "operator", []byte("token-a")); !errors.Is(err, impl.ErrUnknownChannel) {
		t.Fatalf("create session on an unknown channel must fail with ErrUnknownChannel, got %v", err)
	}
}

func TestReactivateRejectsForeignToken(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	sess, err := g.CreateSession(info.ChannelID, "operator", []byte("token-a"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := g.Reactivate(sess.SessionID, []byte("token-b")); !errors.Is(err, impl.ErrUserMismatch) {
		t.Fatalf("reactivating with a foreign token must fail with ErrUserMismatch, got %v", err)
	}
	if _, err := g.Reactivate("session-404", []byte("token-a")); !errors.Is(err, impl.ErrUnknownSession) {
		t.Fatalf("reactivating an unknown session must fail with ErrUnknownSession, got %v", err)
	}
}

func TestReactivateReusesSessionToken(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	sess, err := g.CreateSession(info.ChannelID, "operator", []byte("token-a"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token := uint32(1)
	for i := 0; i < 2; i++ {
		renewed, err := g.Renew(info.ChannelID, impl.RenewRequest{TokenID: token})
		if err != nil {
			t.Fatalf("renew %d: %v", i, err)
		}
		token = renewed.TokenID
	}
	if token != 3 {
		t.Fatalf("channel token = %d after two renewals, want 3", token)
	}
	back, err := g.Reactivate(sess.SessionID, []byte("token-a"))
	if err != nil {
		t.Fatalf("reactivating with the original session token must work: %v", err)
	}
	if back.State != impl.SessionReactivated {
		t.Fatalf("reactivated session state = %q, want %q", back.State, impl.SessionReactivated)
	}
	if back.TokenID != 3 {
		t.Fatalf("reactivated session token = %d, want the live channel token 3", back.TokenID)
	}
	if back.SessionID != sess.SessionID {
		t.Fatalf("reactivation must keep the session id")
	}
}

func TestCloseTerminatesChannel(t *testing.T) {
	g := impl.NewOpcuaSecureChannel(channelLimits())
	info := mustOpenChannel(t, g)
	if err := g.Close(info.ChannelID, "shutdown"); err != nil {
		t.Fatalf("close: %v", err)
	}
	state, ok := g.Channel(info.ChannelID)
	if !ok || state.State != impl.ChannelClosed {
		t.Fatalf("channel state = %q ok=%v, want closed", state.State, ok)
	}
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 1, "x")); !errors.Is(err, impl.ErrChannelClosed) {
		t.Fatalf("accept on a closed channel must fail with ErrChannelClosed, got %v", err)
	}
	if err := g.Close(info.ChannelID, "again"); err != nil {
		t.Fatalf("closing twice must be idempotent, got %v", err)
	}
	if err := g.Close("channel-404", "x"); !errors.Is(err, impl.ErrUnknownChannel) {
		t.Fatalf("closing an unknown channel must fail with ErrUnknownChannel, got %v", err)
	}
}

func TestSnapshotRestoreKeepsSequences(t *testing.T) {
	limits := channelLimits()
	g := impl.NewOpcuaSecureChannel(limits)
	info := mustOpenChannel(t, g)
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 1, "one")); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 2, 2, "two")); err != nil {
		t.Fatalf("accept: %v", err)
	}
	sess, err := g.CreateSession(info.ChannelID, "operator", []byte("token-a"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	data, err := g.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("snapshot must not be empty")
	}
	restored := impl.NewOpcuaSecureChannel(limits)
	if err := restored.Restore(data); err != nil {
		t.Fatalf("restore: %v", err)
	}
	state, ok := restored.Channel(info.ChannelID)
	if !ok {
		t.Fatalf("restored guard lost the channel")
	}
	if state.RecvSeq != 2 || state.SendSeq != 1 || state.TokenID != 1 {
		t.Fatalf("restored channel = %+v, want recv 2 send 1 token 1", state)
	}
	if len(restored.Messages(info.ChannelID)) != 2 {
		t.Fatalf("restored message log holds %d entries, want 2", len(restored.Messages(info.ChannelID)))
	}
	back, err := restored.Reactivate(sess.SessionID, []byte("token-a"))
	if err != nil {
		t.Fatalf("the session must survive the restart: %v", err)
	}
	if back.User != "operator" {
		t.Fatalf("restored session user = %q, want operator", back.User)
	}
	if _, err := restored.Accept(info.ChannelID, frame('M', 'F', 3, 3, "three")); err != nil {
		t.Fatalf("accept after restore: %v", err)
	}
	state, _ = restored.Channel(info.ChannelID)
	if state.RecvSeq != 3 {
		t.Fatalf("receive sequence = %d after restore, want 3", state.RecvSeq)
	}
}

func TestRestoreRejectsCorruptSnapshot(t *testing.T) {
	limits := channelLimits()
	g := impl.NewOpcuaSecureChannel(limits)
	info := mustOpenChannel(t, g)
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 1, 1, "one")); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := g.Restore([]byte("channels:[{")); !errors.Is(err, impl.ErrBadSnapshot) {
		t.Fatalf("corrupt snapshot must fail with ErrBadSnapshot, got %v", err)
	}
	state, ok := g.Channel(info.ChannelID)
	if !ok || state.RecvSeq != 1 {
		t.Fatalf("live channel = %+v ok=%v after a rejected restore, want recv 1", state, ok)
	}
	if _, err := g.Accept(info.ChannelID, frame('M', 'F', 2, 2, "two")); err != nil {
		t.Fatalf("the live channel must keep working, got %v", err)
	}
	if len(g.Messages(info.ChannelID)) != 2 {
		t.Fatalf("message log holds %d entries, want 2", len(g.Messages(info.ChannelID)))
	}
}
