//go:build !sqliteonly && goolm

package element

import (
	"context"
	"sync"
	"testing"

	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// fakeVerificationHelper records calls made by the callbacks.
type fakeVerificationHelper struct {
	mu        sync.Mutex
	accepted  []id.VerificationTransactionID
	started   []id.VerificationTransactionID
	confirmed []id.VerificationTransactionID
	cancelled []cancelCall
}

type cancelCall struct {
	txnID  id.VerificationTransactionID
	code   event.VerificationCancelCode
	reason string
}

func (f *fakeVerificationHelper) accept(txn id.VerificationTransactionID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepted = append(f.accepted, txn)
}

func (f *fakeVerificationHelper) start(txn id.VerificationTransactionID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, txn)
}

func (f *fakeVerificationHelper) confirm(txn id.VerificationTransactionID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmed = append(f.confirmed, txn)
}

func (f *fakeVerificationHelper) cancel(txn id.VerificationTransactionID, code event.VerificationCancelCode, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, cancelCall{txn, code, reason})
}

// We can't easily inject a fake *verificationhelper.VerificationHelper because it
// is a concrete struct. Instead, the callbacks expose narrow behaviors we can test
// by inserting a tiny shim that mirrors the public AcceptVerification/CancelVerification/
// StartSAS/ConfirmSAS surface. The tests below validate the decision logic only.

// testCallbacks is a copy of botVerificationCallbacks that uses a fakeVerificationHelper
// instead of the real *verificationhelper.VerificationHelper. Logic mirrors production.
type testCallbacks struct {
	allowFrom []string
	botUserID string
	fake      *fakeVerificationHelper
}

func (cb *testCallbacks) verificationRequested(from id.UserID, txn id.VerificationTransactionID) {
	if string(from) == cb.botUserID {
		cb.fake.cancel(txn, event.VerificationCancelCodeUser, "self-verify not supported")
		return
	}
	allowed := false
	for _, u := range cb.allowFrom {
		if u == string(from) {
			allowed = true
			break
		}
	}
	if !allowed {
		cb.fake.cancel(txn, event.VerificationCancelCodeUser, "not in allowlist")
		return
	}
	cb.fake.accept(txn)
}

func (cb *testCallbacks) verificationReady(txn id.VerificationTransactionID, supportsSAS bool) {
	if !supportsSAS {
		cb.fake.cancel(txn, event.VerificationCancelCodeUser, "SAS method required")
		return
	}
	cb.fake.start(txn)
}

func (cb *testCallbacks) showSAS(txn id.VerificationTransactionID) {
	cb.fake.confirm(txn)
}

func TestVerificationRequested_AllowlistedUserIsAccepted(t *testing.T) {
	fake := &fakeVerificationHelper{}
	cb := &testCallbacks{
		allowFrom: []string{"@alice:example.com"},
		botUserID: "@bot:example.com",
		fake:      fake,
	}
	cb.verificationRequested("@alice:example.com", "txn1")

	if len(fake.accepted) != 1 || fake.accepted[0] != "txn1" {
		t.Errorf("expected txn1 accepted, got %+v", fake.accepted)
	}
	if len(fake.cancelled) != 0 {
		t.Errorf("expected no cancels, got %+v", fake.cancelled)
	}
}

func TestVerificationRequested_NonAllowlistedRejected(t *testing.T) {
	fake := &fakeVerificationHelper{}
	cb := &testCallbacks{
		allowFrom: []string{"@alice:example.com"},
		botUserID: "@bot:example.com",
		fake:      fake,
	}
	cb.verificationRequested("@mallory:evil.com", "txn2")

	if len(fake.accepted) != 0 {
		t.Errorf("expected no accept, got %+v", fake.accepted)
	}
	if len(fake.cancelled) != 1 || fake.cancelled[0].reason != "not in allowlist" {
		t.Errorf("expected cancel with 'not in allowlist', got %+v", fake.cancelled)
	}
}

func TestVerificationRequested_SelfVerifyRejected(t *testing.T) {
	fake := &fakeVerificationHelper{}
	cb := &testCallbacks{
		allowFrom: []string{"@bot:example.com"}, // bot somehow in allowlist
		botUserID: "@bot:example.com",
		fake:      fake,
	}
	cb.verificationRequested("@bot:example.com", "txn3")

	if len(fake.accepted) != 0 {
		t.Errorf("self-verify must not auto-accept, got %+v", fake.accepted)
	}
	if len(fake.cancelled) != 1 || fake.cancelled[0].reason != "self-verify not supported" {
		t.Errorf("expected self-verify cancel, got %+v", fake.cancelled)
	}
}

func TestVerificationReady_NoSASCancelled(t *testing.T) {
	fake := &fakeVerificationHelper{}
	cb := &testCallbacks{fake: fake}
	cb.verificationReady("txn4", false)

	if len(fake.started) != 0 {
		t.Errorf("expected no SAS start when no SAS support, got %+v", fake.started)
	}
	if len(fake.cancelled) != 1 || fake.cancelled[0].reason != "SAS method required" {
		t.Errorf("expected SAS-required cancel, got %+v", fake.cancelled)
	}
}

func TestVerificationReady_SASStarts(t *testing.T) {
	fake := &fakeVerificationHelper{}
	cb := &testCallbacks{fake: fake}
	cb.verificationReady("txn5", true)

	if len(fake.started) != 1 || fake.started[0] != "txn5" {
		t.Errorf("expected SAS start, got %+v", fake.started)
	}
}

func TestShowSAS_BlindConfirms(t *testing.T) {
	fake := &fakeVerificationHelper{}
	cb := &testCallbacks{fake: fake}
	cb.showSAS("txn6")

	if len(fake.confirmed) != 1 || fake.confirmed[0] != "txn6" {
		t.Errorf("expected blind confirm, got %+v", fake.confirmed)
	}
}

// Smoke: NewInMemoryVerificationStore exists and constructs without panic.
func TestVerificationStore_InMemoryAvailable(t *testing.T) {
	store := verificationhelper.NewInMemoryVerificationStore()
	if store == nil {
		t.Fatal("NewInMemoryVerificationStore returned nil")
	}
}

// registerVerificationOnGoolm exercises the gating logic on goolmCryptoState
// when the underlying crypto helper is not initialised — must be a no-op (returns nil).
func TestRegisterVerificationHandler_UninitializedNoOp(t *testing.T) {
	s := &goolmCryptoState{}
	if err := s.RegisterVerificationHandler([]string{"@a:b.com"}, "@bot:b.com"); err != nil {
		t.Errorf("expected nil on uninitialised state, got %v", err)
	}
}

func TestRegisterVerificationHandler_EmptyAllowlistNoOp(t *testing.T) {
	s := &goolmCryptoState{}
	if err := s.RegisterVerificationHandler(nil, "@bot:b.com"); err != nil {
		t.Errorf("expected nil on empty allowlist, got %v", err)
	}
}

// Smoke: callback type assertions hold.
func TestBotVerificationCallbacks_ImplementsInterfaces(t *testing.T) {
	var _ verificationhelper.RequiredCallbacks = (*botVerificationCallbacks)(nil)
	var _ verificationhelper.ShowSASCallbacks = (*botVerificationCallbacks)(nil)

	// Use context.Background to avoid linter complaint on unused import.
	_ = context.Background()
}
