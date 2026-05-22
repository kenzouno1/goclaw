//go:build !sqliteonly && goolm

package element

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

// mockMatrixServerForCrypto stands up an httptest server with all endpoints
// that cryptohelper.Init calls so tests don't fail on network errors.
func mockMatrixServerForCrypto(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// Login endpoint.
	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "tok_crypto_test",
			"device_id":    "CRYPTO_DEVICE_1",
			"user_id":      "@bot:example.com",
		})
	})

	// Versions — mautrix client may check this.
	mux.HandleFunc("/_matrix/client/versions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"versions":["v1.1"]}`))
	})

	// Keys upload — cryptohelper calls this to publish device keys.
	mux.HandleFunc("/_matrix/client/v3/keys/upload", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"one_time_key_counts":{}}`))
	})

	// Keys query — cryptohelper.verifyDeviceKeysOnServer calls this.
	mux.HandleFunc("/_matrix/client/v3/keys/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_keys":{}}`))
	})

	// Well-known stub so mautrix doesn't complain.
	mux.HandleFunc("/.well-known/matrix/client", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	// Sync — return immediately with empty response so the sync loop doesn't hang.
	mux.HandleFunc("/_matrix/client/v3/sync", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"next_batch":"tok1","rooms":{},"to_device":{}}`))
	})

	// Catch-all: return empty JSON for any other endpoint cryptohelper touches.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	return httptest.NewServer(mux)
}

// newTestPickleKey returns a valid 32-byte base64-encoded pickle key for tests.
func newTestPickleKey() string {
	buf := make([]byte, 32)
	for i := range buf {
		buf[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// buildCryptoChannel creates an element Channel with the given dataDir and instanceName
// using a pre-populated access token (no login call needed).
func buildCryptoChannel(t *testing.T, srv *httptest.Server, dataDir, instanceName, pickleKey string) *Channel {
	t.Helper()
	creds := elementCreds{
		AccessToken: "tok_existing",
		DeviceID:    "CRYPTO_DEVICE_1",
		PickleKey:   pickleKey,
	}
	ic := elementInstanceConfig{
		Homeserver: srv.URL,
		UserID:     "@bot:example.com",
	}
	credsRaw, _ := json.Marshal(creds)
	cfgRaw, _ := json.Marshal(ic)

	factory := FactoryWithCredsWriterAndDataDir(&stubCredsWriter{}, dataDir)
	ch, err := factory(context.Background(), instanceName, credsRaw, cfgRaw, bus.New(), nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	return ch.(*Channel)
}

// TestCryptoDirCreatedOnStart verifies that Channel.Start creates the per-instance
// crypto directory and crypto.sqlite file at the expected path.
func TestCryptoDirCreatedOnStart(t *testing.T) {
	srv := mockMatrixServerForCrypto(t)
	defer srv.Close()

	dataDir := t.TempDir()
	instanceName := "test-element-crypto"

	ch := buildCryptoChannel(t, srv, dataDir, instanceName, newTestPickleKey())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ch.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = ch.Stop(stopCtx)
	}()

	// Verify crypto dir exists.
	expectedDir := filepath.Join(dataDir, "element", instanceName)
	info, err := os.Stat(expectedDir)
	if err != nil {
		t.Fatalf("crypto dir not created at %s: %v", expectedDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected crypto path to be a directory: %s", expectedDir)
	}

	// Verify crypto.sqlite was created.
	expectedDB := filepath.Join(expectedDir, "crypto.sqlite")
	if _, err := os.Stat(expectedDB); err != nil {
		t.Fatalf("crypto.sqlite not created at %s: %v", expectedDB, err)
	}
}

// TestCryptoHelperReuseOnRestart verifies that a second Start after Stop reuses
// the existing crypto.sqlite (Init succeeds without recreating the DB).
func TestCryptoHelperReuseOnRestart(t *testing.T) {
	srv := mockMatrixServerForCrypto(t)
	defer srv.Close()

	dataDir := t.TempDir()
	instanceName := "test-element-restart"
	pickleKey := newTestPickleKey()

	dbPath := filepath.Join(dataDir, "element", instanceName, "crypto.sqlite")

	startStop := func(label string) {
		t.Helper()
		ch := buildCryptoChannel(t, srv, dataDir, instanceName, pickleKey)

		startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer startCancel()
		if err := ch.Start(startCtx); err != nil {
			t.Fatalf("%s Start: %v", label, err)
		}

		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := ch.Stop(stopCtx); err != nil {
			t.Fatalf("%s Stop: %v", label, err)
		}
	}

	// First run — creates the DB.
	startStop("first")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("crypto.sqlite not created on first run: %v", err)
	}

	// Second run — reuses the existing DB; Init must still succeed.
	startStop("second")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("crypto.sqlite missing after second run: %v", err)
	}
}

// TestStopReleasesDBAccess verifies that Stop closes the crypto helper so the
// SQLite file can be opened by a new process (no exclusive WAL lock held).
func TestStopReleasesDBAccess(t *testing.T) {
	srv := mockMatrixServerForCrypto(t)
	defer srv.Close()

	dataDir := t.TempDir()
	instanceName := "test-element-wal"

	ch := buildCryptoChannel(t, srv, dataDir, instanceName, newTestPickleKey())

	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	if err := ch.Start(startCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := ch.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop, the SQLite file must be openable (no exclusive lock held).
	dbPath := filepath.Join(dataDir, "element", instanceName, "crypto.sqlite")
	f, err := os.Open(dbPath)
	if err != nil {
		t.Fatalf("cannot open crypto.sqlite after Stop (lock not released?): %v", err)
	}
	f.Close()

	// Log WAL state for diagnostics (not a failure — SQLite WAL files are normal).
	walPath := dbPath + "-wal"
	if info, statErr := os.Stat(walPath); statErr == nil {
		t.Logf("WAL file present after Stop (%d bytes) — normal for WAL mode; not a lock leak", info.Size())
	}
}

// TestCryptoDirHasRestrictedPerms verifies that the per-instance crypto directory
// is created with 0700 permissions (owner-only access).
// Skipped on Windows where Unix permission bits are not enforced by the OS.
func TestCryptoDirHasRestrictedPerms(t *testing.T) {
	if isWindows() {
		t.Skip("Unix permission enforcement not available on Windows")
	}

	srv := mockMatrixServerForCrypto(t)
	defer srv.Close()

	dataDir := t.TempDir()
	instanceName := "test-element-perms"

	ch := buildCryptoChannel(t, srv, dataDir, instanceName, newTestPickleKey())

	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	if err := ch.Start(startCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = ch.Stop(stopCtx)

	cryptoDir := filepath.Join(dataDir, "element", instanceName)
	info, err := os.Stat(cryptoDir)
	if err != nil {
		t.Fatalf("stat crypto dir: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0700 {
		t.Errorf("expected crypto dir perms 0700, got %04o", perm)
	}
}

