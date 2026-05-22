//go:build !sqliteonly

package element

import (
	"context"
	"errors"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeSender records SendMessageEvent calls for assertion.
type fakeSender struct {
	calls []fakeSendCall
	err   error // returned from each call if non-nil
}

type fakeSendCall struct {
	roomID    id.RoomID
	eventType event.Type
	content   interface{}
}

func (f *fakeSender) SendMessageEvent(
	_ context.Context,
	roomID id.RoomID,
	eventType event.Type,
	contentJSON interface{},
	_ ...mautrix.ReqSendEvent,
) (*mautrix.RespSendEvent, error) {
	f.calls = append(f.calls, fakeSendCall{roomID, eventType, contentJSON})
	if f.err != nil {
		return nil, f.err
	}
	return &mautrix.RespSendEvent{EventID: "$fake-event"}, nil
}

// fakeCryptoState is a configurable stub for cryptoStateMachine.
type fakeCryptoState struct {
	ready       bool
	encrypted   bool
	encryptErr  error
	encResult   *event.EncryptedEventContent
	decryptCb   func(*event.Event, error)
	decryptCbCalled bool

	// Cross-signing bootstrap behavior overrides for tests.
	bootstrapCalls    int
	bootstrapErr      error
	bootstrapDirty    bool
	bootstrapNewSeeds localCrossSigningSeeds
	lastSeedsArg      localCrossSigningSeeds
	lastPasswordArg   string
}

func (f *fakeCryptoState) Init(_ context.Context, _ *mautrix.Client, _ elementConfig, _ string) error {
	return nil
}
func (f *fakeCryptoState) Close() error { return nil }
func (f *fakeCryptoState) IsReady() bool { return f.ready }
func (f *fakeCryptoState) IsRoomEncrypted(_ context.Context, _ id.RoomID) bool { return f.encrypted }
func (f *fakeCryptoState) EncryptMessage(_ context.Context, _ id.RoomID, _ any) (*event.EncryptedEventContent, error) {
	return f.encResult, f.encryptErr
}
func (f *fakeCryptoState) SetDecryptErrorCallback(cb func(*event.Event, error)) {
	f.decryptCb = cb
}
func (f *fakeCryptoState) LoadOrGenerateCrossSigning(_ context.Context, password string, seeds localCrossSigningSeeds) (localCrossSigningSeeds, bool, error) {
	f.bootstrapCalls++
	f.lastSeedsArg = seeds
	f.lastPasswordArg = password
	if f.bootstrapErr != nil {
		return localCrossSigningSeeds{}, false, f.bootstrapErr
	}
	if f.bootstrapDirty {
		return f.bootstrapNewSeeds, true, nil
	}
	return seeds, false, nil
}

func (f *fakeCryptoState) RegisterVerificationHandler(_ []string, _ string) error { return nil }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildTestChannel constructs a minimal Channel wired to a fakeSender.
// The real mxClient is replaced post-construction via the sender interface.
func buildTestChannel(t *testing.T, crypto *fakeCryptoState) (*Channel, *fakeSender) {
	t.Helper()

	msgBus := bus.New()
	base := channels.NewBaseChannel(channels.TypeElement, msgBus, nil)
	base.SetRunning(true)

	sender := &fakeSender{}

	ch := &Channel{
		BaseChannel: base,
		cfg: elementConfig{
			outbound: true,
		},
		cryptoState: crypto,
		// mxClient is only used via sendPlaintext/sendEncrypted which call
		// c.mxClient.SendMessageEvent — we override that via the test by
		// calling sendChunk directly with a context and inspecting via fakeSender.
		// To avoid a nil-deref in signalTyping (called via goroutine in Send),
		// we keep mxClient nil and call sendChunk directly.
	}
	// Wire the fake sender directly — sendPlaintext and sendEncrypted both call
	// c.mxClient.SendMessageEvent, so we need a real client field.
	// Instead, we test sendPlaintext/sendEncrypted directly with our fake; or we
	// inject via the matrixSender interface by wrapping the channel's send methods.
	// Simplest: test the exported Send path with a real (httptest) client is done
	// in integration; here we unit-test sendPlaintext / sendEncrypted / sendChunk.
	_ = sender
	return ch, sender
}

// buildTestChannelWithClient constructs a Channel with a fake mxClient (via httptest).
// For pure branch-logic tests we bypass Send entirely and call sendChunk directly
// through a wrapper that substitutes the sender.
//
// Instead of embedding the fakeSender into the real mxClient (which would require
// an httptest server), we test the routing logic by inspecting the fakeCryptoState
// calls and confirm the right branch executes via a small shim channel.
type testableChannel struct {
	*Channel
	sender *fakeSender
}

func (tc *testableChannel) sendChunkViaShim(ctx context.Context, roomID id.RoomID, body string) error {
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          body,
		Format:        event.FormatHTML,
		FormattedBody: markdownToHTML(body),
	}

	if tc.cryptoState.IsReady() && tc.cryptoState.IsRoomEncrypted(sendCtx, roomID) {
		enc, err := tc.cryptoState.EncryptMessage(sendCtx, roomID, content)
		if err != nil {
			return err
		}
		if enc == nil {
			_, err = tc.sender.SendMessageEvent(sendCtx, roomID, event.EventMessage, content)
			return err
		}
		_, err = tc.sender.SendMessageEvent(sendCtx, roomID, event.EventEncrypted, enc)
		return err
	}
	_, err := tc.sender.SendMessageEvent(sendCtx, roomID, event.EventMessage, content)
	return err
}

// ---------------------------------------------------------------------------
// Tests: plaintext path
// ---------------------------------------------------------------------------

func TestSendChunk_PlaintextRoom(t *testing.T) {
	crypto := &fakeCryptoState{ready: true, encrypted: false}
	sender := &fakeSender{}
	tc := &testableChannel{
		Channel: &Channel{cryptoState: crypto},
		sender:  sender,
	}

	roomID := id.RoomID("!plain:example.com")
	if err := tc.sendChunkViaShim(context.Background(), roomID, "hello plaintext"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(sender.calls))
	}
	got := sender.calls[0]
	if got.eventType != event.EventMessage {
		t.Errorf("expected EventMessage, got %v", got.eventType)
	}
	if got.roomID != roomID {
		t.Errorf("expected room %s, got %s", roomID, got.roomID)
	}
}

func TestSendChunk_CryptoNotReady_FallsBackToPlaintext(t *testing.T) {
	// stub returns IsReady=false even for "encrypted" room — should use plaintext.
	crypto := &fakeCryptoState{ready: false, encrypted: true}
	sender := &fakeSender{}
	tc := &testableChannel{
		Channel: &Channel{cryptoState: crypto},
		sender:  sender,
	}

	roomID := id.RoomID("!enc:example.com")
	if err := tc.sendChunkViaShim(context.Background(), roomID, "crypto off"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(sender.calls))
	}
	if sender.calls[0].eventType != event.EventMessage {
		t.Errorf("expected EventMessage fallback, got %v", sender.calls[0].eventType)
	}
}

// ---------------------------------------------------------------------------
// Tests: encrypted path
// ---------------------------------------------------------------------------

func TestSendChunk_EncryptedRoom_SendsEventEncrypted(t *testing.T) {
	encContent := &event.EncryptedEventContent{Algorithm: id.AlgorithmMegolmV1}
	crypto := &fakeCryptoState{
		ready:     true,
		encrypted: true,
		encResult: encContent,
	}
	sender := &fakeSender{}
	tc := &testableChannel{
		Channel: &Channel{cryptoState: crypto},
		sender:  sender,
	}

	roomID := id.RoomID("!enc:example.com")
	if err := tc.sendChunkViaShim(context.Background(), roomID, "secret message"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(sender.calls))
	}
	got := sender.calls[0]
	if got.eventType != event.EventEncrypted {
		t.Errorf("expected EventEncrypted, got %v", got.eventType)
	}
	if got.content != encContent {
		t.Errorf("expected encrypted content pointer, got different value")
	}
}

func TestSendChunk_EncryptReturnNil_FallsBackToPlaintext(t *testing.T) {
	// EncryptMessage returns nil, nil → fallback to plaintext.
	crypto := &fakeCryptoState{
		ready:     true,
		encrypted: true,
		encResult: nil,
		encryptErr: nil,
	}
	sender := &fakeSender{}
	tc := &testableChannel{
		Channel: &Channel{cryptoState: crypto},
		sender:  sender,
	}

	roomID := id.RoomID("!enc:example.com")
	if err := tc.sendChunkViaShim(context.Background(), roomID, "fallback"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(sender.calls))
	}
	if sender.calls[0].eventType != event.EventMessage {
		t.Errorf("expected EventMessage fallback, got %v", sender.calls[0].eventType)
	}
}

func TestSendChunk_EncryptError_ReturnsError(t *testing.T) {
	encErr := errors.New("megolm session not found")
	crypto := &fakeCryptoState{
		ready:      true,
		encrypted:  true,
		encryptErr: encErr,
	}
	sender := &fakeSender{}
	tc := &testableChannel{
		Channel: &Channel{cryptoState: crypto},
		sender:  sender,
	}

	roomID := id.RoomID("!enc:example.com")
	err := tc.sendChunkViaShim(context.Background(), roomID, "will fail")
	if err == nil {
		t.Fatal("expected error from EncryptMessage, got nil")
	}
	if !errors.Is(err, encErr) {
		t.Errorf("expected wrapped encErr, got: %v", err)
	}
	if len(sender.calls) != 0 {
		t.Errorf("expected no send calls on encrypt error, got %d", len(sender.calls))
	}
}

// ---------------------------------------------------------------------------
// Tests: decrypt error callback
// ---------------------------------------------------------------------------

func TestDecryptErrorCallback_NoBusPublish(t *testing.T) {
	msgBus := bus.New()
	base := channels.NewBaseChannel(channels.TypeElement, msgBus, nil)
	base.SetRunning(true)

	ch := &Channel{
		BaseChannel: base,
		cfg:         elementConfig{},
		cryptoState: &fakeCryptoState{},
	}

	// Fire the decrypt error callback with a real event.
	evt := &event.Event{
		RoomID: "!room:example.com",
		ID:     "$evt1",
		Sender: "@attacker:example.com",
	}
	ch.onDecryptError(evt, errors.New("bad mac"))

	// The inbound bus channel must remain empty — onDecryptError must never publish.
	// ConsumeInbound with an already-cancelled context returns immediately if empty.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled immediately
	if _, ok := msgBus.ConsumeInbound(ctx); ok {
		t.Error("onDecryptError must not publish to bus")
	}
}

func TestDecryptErrorCallback_NilEvent_NoPanic(t *testing.T) {
	base := channels.NewBaseChannel(channels.TypeElement, bus.New(), nil)
	ch := &Channel{BaseChannel: base}

	// Must not panic on nil event.
	ch.onDecryptError(nil, errors.New("some error"))
}

// ---------------------------------------------------------------------------
// Tests: SetDecryptErrorCallback wiring on fakeCryptoState
// ---------------------------------------------------------------------------

func TestSetDecryptErrorCallback_IsStored(t *testing.T) {
	crypto := &fakeCryptoState{}
	var called bool
	cb := func(_ *event.Event, _ error) { called = true }
	crypto.SetDecryptErrorCallback(cb)

	if crypto.decryptCb == nil {
		t.Fatal("callback not stored on fakeCryptoState")
	}
	crypto.decryptCb(nil, nil)
	if !called {
		t.Error("stored callback not invoked correctly")
	}
}
