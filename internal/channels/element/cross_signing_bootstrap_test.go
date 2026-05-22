//go:build !sqliteonly

package element

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// recordingCredsWriter captures UpdateCredentials calls for assertion.
type recordingCredsWriter struct {
	mu     sync.Mutex
	calls  int
	lastID string
	last   []byte
	err    error
}

func (w *recordingCredsWriter) UpdateCredentials(_ context.Context, instanceID string, plaintext []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	w.lastID = instanceID
	w.last = append([]byte(nil), plaintext...)
	return w.err
}

func newBootstrapChannel(crypto *fakeCryptoState, writer credsWriterIface, cfg elementConfig) *Channel {
	base := channels.NewBaseChannel(channels.TypeElement, bus.New(), nil)
	cfg.credsWriter = writer
	cfg.instanceID = "test-instance"
	ch := &Channel{
		BaseChannel: base,
		cfg:         cfg,
		cryptoState: crypto,
	}
	ch.SetName("test-instance")
	return ch
}

func TestBootstrapCrossSigning_DisabledSkips(t *testing.T) {
	crypto := &fakeCryptoState{}
	writer := &recordingCredsWriter{}
	ch := newBootstrapChannel(crypto, writer, elementConfig{
		disableCrossSigning: true,
		password:            "hunter2",
	})

	ch.bootstrapCrossSigning(context.Background())

	if crypto.bootstrapCalls != 0 {
		t.Errorf("expected 0 bootstrap calls when disabled, got %d", crypto.bootstrapCalls)
	}
	if writer.calls != 0 {
		t.Errorf("expected 0 writer calls when disabled, got %d", writer.calls)
	}
}

func TestBootstrapCrossSigning_NoWriterSkips(t *testing.T) {
	crypto := &fakeCryptoState{}
	ch := newBootstrapChannel(crypto, nil, elementConfig{password: "hunter2"})

	ch.bootstrapCrossSigning(context.Background())

	if crypto.bootstrapCalls != 0 {
		t.Errorf("expected 0 bootstrap calls when no writer, got %d", crypto.bootstrapCalls)
	}
}

func TestBootstrapCrossSigning_DirtyPersists(t *testing.T) {
	crypto := &fakeCryptoState{
		bootstrapDirty: true,
		bootstrapNewSeeds: localCrossSigningSeeds{
			MasterKey:      "master-bytes",
			SelfSigningKey: "self-bytes",
			UserSigningKey: "user-bytes",
		},
	}
	writer := &recordingCredsWriter{}
	ch := newBootstrapChannel(crypto, writer, elementConfig{password: "hunter2"})

	ch.bootstrapCrossSigning(context.Background())

	if crypto.bootstrapCalls != 1 {
		t.Errorf("expected 1 bootstrap call, got %d", crypto.bootstrapCalls)
	}
	if crypto.lastPasswordArg != "hunter2" {
		t.Errorf("expected password passed through, got %q", crypto.lastPasswordArg)
	}
	if writer.calls != 1 {
		t.Fatalf("expected 1 writer call, got %d", writer.calls)
	}
	if writer.lastID != "test-instance" {
		t.Errorf("expected instanceID test-instance, got %q", writer.lastID)
	}
	// Persisted blob must include all 3 seed keys.
	blob := string(writer.last)
	for _, key := range []string{"master_seed", "self_signing_seed", "user_signing_seed"} {
		if !contains(blob, key) {
			t.Errorf("persisted blob missing key %q: %s", key, blob)
		}
	}
}

func TestBootstrapCrossSigning_ImportPathDoesNotPersist(t *testing.T) {
	// dirty=false → import path; seeds passed through, no persist call.
	existing := localCrossSigningSeeds{
		MasterKey:      "m",
		SelfSigningKey: "s",
		UserSigningKey: "u",
	}
	crypto := &fakeCryptoState{bootstrapDirty: false}
	writer := &recordingCredsWriter{}
	ch := newBootstrapChannel(crypto, writer, elementConfig{
		password:          "hunter2",
		crossSigningSeeds: existing,
	})

	ch.bootstrapCrossSigning(context.Background())

	if crypto.bootstrapCalls != 1 {
		t.Errorf("expected 1 bootstrap call, got %d", crypto.bootstrapCalls)
	}
	if crypto.lastSeedsArg != existing {
		t.Errorf("expected seeds forwarded, got %+v", crypto.lastSeedsArg)
	}
	if writer.calls != 0 {
		t.Errorf("expected 0 writer calls on import path, got %d", writer.calls)
	}
}

func TestBootstrapCrossSigning_ErrorIsNonFatal(t *testing.T) {
	crypto := &fakeCryptoState{bootstrapErr: errors.New("MAS reset required")}
	writer := &recordingCredsWriter{}
	ch := newBootstrapChannel(crypto, writer, elementConfig{password: "hunter2"})

	// Must not panic; bootstrap swallows error.
	ch.bootstrapCrossSigning(context.Background())

	if crypto.bootstrapCalls != 1 {
		t.Errorf("expected 1 bootstrap call, got %d", crypto.bootstrapCalls)
	}
	if writer.calls != 0 {
		t.Errorf("expected 0 writer calls on bootstrap failure, got %d", writer.calls)
	}
}

func TestBootstrapCrossSigning_PersistFailureSwallowed(t *testing.T) {
	crypto := &fakeCryptoState{
		bootstrapDirty: true,
		bootstrapNewSeeds: localCrossSigningSeeds{
			MasterKey: "m", SelfSigningKey: "s", UserSigningKey: "u",
		},
	}
	writer := &recordingCredsWriter{err: errors.New("db down")}
	ch := newBootstrapChannel(crypto, writer, elementConfig{password: "hunter2"})

	// Persist failure must not panic or propagate.
	ch.bootstrapCrossSigning(context.Background())

	if writer.calls != 1 {
		t.Errorf("expected 1 persist attempt, got %d", writer.calls)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
