//go:build !sqliteonly

package element

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

// --- helpers ---

// mockMatrixServer stands up an httptest server implementing just the /login endpoint.
// loginCount is incremented for every POST /login call.
func mockMatrixServer(t *testing.T, loginCount *atomic.Int64, accessToken, deviceID string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		loginCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]string{
			"access_token": accessToken,
			"device_id":    deviceID,
			"user_id":      "@bot:example.com",
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	// Minimal well-known stub so mautrix doesn't complain.
	mux.HandleFunc("/.well-known/matrix/client", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	return httptest.NewServer(mux)
}

func newTestBus() *bus.MessageBus {
	return bus.New()
}

// stubCredsWriter records the last written blob.
type stubCredsWriter struct {
	called     int
	lastID     string
	lastBlob   []byte
}

func (w *stubCredsWriter) UpdateCredentials(_ context.Context, instanceID string, encrypted []byte) error {
	w.called++
	w.lastID = instanceID
	w.lastBlob = encrypted
	return nil
}

// --- tests ---

// TestTokenPathUnchanged verifies that an access_token-only config does not
// call /login and creates a channel successfully (regression guard).
func TestTokenPathUnchanged(t *testing.T) {
	var loginCount atomic.Int64
	srv := mockMatrixServer(t, &loginCount, "tok_abc", "DEVICE1")
	defer srv.Close()

	creds := elementCreds{AccessToken: "syt_existingtoken"}
	ic := elementInstanceConfig{
		Homeserver: srv.URL,
		UserID:     "@bot:example.com",
	}

	credsRaw, _ := json.Marshal(creds)
	cfgRaw, _ := json.Marshal(ic)

	writer := &stubCredsWriter{}
	factory := FactoryWithCredsWriter(writer)

	ch, err := factory(context.Background(), "test-element", credsRaw, cfgRaw, newTestBus(), nil)
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	if loginCount.Load() != 0 {
		t.Errorf("expected 0 /login calls for token path, got %d", loginCount.Load())
	}
	// pickle_key should be generated (dirty write-back).
	if writer.called == 0 {
		t.Error("expected creds writer called once (for pickle_key generation)")
	}
}

// TestLoginPathFirstRun verifies that username+password triggers /login once,
// then writes back access_token + device_id.
func TestLoginPathFirstRun(t *testing.T) {
	var loginCount atomic.Int64
	srv := mockMatrixServer(t, &loginCount, "tok_fresh", "DEVICE_FRESH")
	defer srv.Close()

	creds := elementCreds{Username: "bot", Password: "s3cr3t"}
	ic := elementInstanceConfig{
		Homeserver: srv.URL,
		UserID:     "@bot:example.com",
	}

	credsRaw, _ := json.Marshal(creds)
	cfgRaw, _ := json.Marshal(ic)

	writer := &stubCredsWriter{}
	factory := FactoryWithCredsWriter(writer)

	ch, err := factory(context.Background(), "test-element", credsRaw, cfgRaw, newTestBus(), nil)
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	if loginCount.Load() != 1 {
		t.Errorf("expected 1 /login call, got %d", loginCount.Load())
	}
	if writer.called == 0 {
		t.Error("expected creds writer to be called after login")
	}

	// Verify the blob contains access_token and device_id, but NOT password.
	var persisted elementCreds
	if err := json.Unmarshal(writer.lastBlob, &persisted); err != nil {
		t.Fatalf("unmarshal persisted creds: %v", err)
	}
	if persisted.AccessToken != "tok_fresh" {
		t.Errorf("expected access_token=tok_fresh, got %q", persisted.AccessToken)
	}
	if persisted.DeviceID != "DEVICE_FRESH" {
		t.Errorf("expected device_id=DEVICE_FRESH, got %q", persisted.DeviceID)
	}
	if persisted.Password != "" {
		t.Errorf("expected password cleared from blob, got %q", persisted.Password)
	}
	if persisted.PickleKey == "" {
		t.Error("expected pickle_key to be set")
	}
}

// TestLoginSecondStartupNoRelogin verifies that when access_token is already
// present (from a previous login+persist), /login is NOT called again.
// After first login the password is cleared and only access_token + device_id
// are retained — that is the shape that arrives on second startup.
func TestLoginSecondStartupNoRelogin(t *testing.T) {
	var loginCount atomic.Int64
	srv := mockMatrixServer(t, &loginCount, "tok_new", "DEVICE_NEW")
	defer srv.Close()

	// Simulate stored creds from previous run: password cleared, token persisted.
	// Username is absent — the factory uses token path when access_token is set.
	creds := elementCreds{
		AccessToken: "tok_stored",
		DeviceID:    "DEVICE_STORED",
		PickleKey:   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", // 32-byte base64 stub
	}
	ic := elementInstanceConfig{
		Homeserver: srv.URL,
		UserID:     "@bot:example.com",
	}

	credsRaw, _ := json.Marshal(creds)
	cfgRaw, _ := json.Marshal(ic)

	writer := &stubCredsWriter{}
	factory := FactoryWithCredsWriter(writer)

	ch, err := factory(context.Background(), "test-element", credsRaw, cfgRaw, newTestBus(), nil)
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	if loginCount.Load() != 0 {
		t.Errorf("expected 0 /login calls on second startup (stored token), got %d", loginCount.Load())
	}
}

// TestBothAuthMethodsPrefersAccessToken verifies that when both access_token and
// username+password are present (e.g. from a stale store merge after the user
// re-submitted login fields post-authentication), the factory treats the
// access_token as authoritative, drops username/password, and persists the
// cleaned creds back to the store.
func TestBothAuthMethodsPrefersAccessToken(t *testing.T) {
	var loginCount atomic.Int64
	srv := mockMatrixServer(t, &loginCount, "tok_should_not_be_used", "DEV_should_not_be_used")
	defer srv.Close()

	creds := elementCreds{
		AccessToken: "tok_existing",
		Username:    "bot",
		Password:    "pass",
	}
	ic := elementInstanceConfig{
		Homeserver: srv.URL,
		UserID:     "@bot:example.com",
	}

	credsRaw, _ := json.Marshal(creds)
	cfgRaw, _ := json.Marshal(ic)

	writer := &stubCredsWriter{}
	factory := FactoryWithCredsWriter(writer)

	ch, err := factory(context.Background(), "test-element", credsRaw, cfgRaw, newTestBus(), nil)
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	if loginCount.Load() != 0 {
		t.Errorf("expected 0 /login calls when access_token present, got %d", loginCount.Load())
	}
	if writer.called == 0 {
		t.Fatal("expected creds writer called to persist cleaned credentials")
	}
	var persisted elementCreds
	if err := json.Unmarshal(writer.lastBlob, &persisted); err != nil {
		t.Fatalf("decode persisted creds: %v", err)
	}
	if persisted.AccessToken != "tok_existing" {
		t.Errorf("expected access_token preserved, got %q", persisted.AccessToken)
	}
	if persisted.Username != "" || persisted.Password != "" {
		t.Errorf("expected username/password cleared, got username=%q password=%q",
			persisted.Username, persisted.Password)
	}
}

// TestRejectNeitherAuthMethod verifies that creds with no auth material are rejected.
func TestRejectNeitherAuthMethod(t *testing.T) {
	creds := elementCreds{}
	ic := elementInstanceConfig{
		Homeserver: "https://matrix.example.com",
		UserID:     "@bot:example.com",
	}

	credsRaw, _ := json.Marshal(creds)
	cfgRaw, _ := json.Marshal(ic)

	_, err := Factory("test-element", credsRaw, cfgRaw, newTestBus(), nil)
	if err == nil {
		t.Fatal("expected error when no auth method supplied")
	}
}

// TestPasswordClearedUnlessKeepPassword verifies default clear-after-login behaviour.
func TestPasswordClearedUnlessKeepPassword(t *testing.T) {
	var loginCount atomic.Int64
	srv := mockMatrixServer(t, &loginCount, "tok_123", "DEV_123")
	defer srv.Close()

	creds := elementCreds{Username: "bot", Password: "s3cr3t", KeepPassword: true}
	ic := elementInstanceConfig{
		Homeserver: srv.URL,
		UserID:     "@bot:example.com",
	}
	credsRaw, _ := json.Marshal(creds)
	cfgRaw, _ := json.Marshal(ic)

	writer := &stubCredsWriter{}
	factory := FactoryWithCredsWriter(writer)

	_, err := factory(context.Background(), "test-element", credsRaw, cfgRaw, newTestBus(), nil)
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}

	var persisted elementCreds
	if err := json.Unmarshal(writer.lastBlob, &persisted); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// With keep_password=true, password should still be there.
	if persisted.Password != "s3cr3t" {
		t.Errorf("expected password retained with keep_password=true, got %q", persisted.Password)
	}
}

// TestHTTPSEnforcement verifies non-localhost http:// homeservers are rejected.
func TestHTTPSEnforcement(t *testing.T) {
	creds := elementCreds{Username: "bot", Password: "pass"}
	ic := elementInstanceConfig{
		Homeserver: "http://matrix.evil.com",
		UserID:     "@bot:example.com",
	}
	credsRaw, _ := json.Marshal(creds)
	cfgRaw, _ := json.Marshal(ic)

	_, err := Factory("test-element", credsRaw, cfgRaw, newTestBus(), nil)
	if err == nil {
		t.Fatal("expected error for non-local http homeserver")
	}
}

// mockMatrixServerCapturingRequest is like mockMatrixServer but also captures the raw
// request body of each /login call so the test can inspect what DeviceID was sent.
func mockMatrixServerCapturingRequest(t *testing.T, loginCount *atomic.Int64, accessToken, deviceID string, capturedBody *[]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		loginCount.Add(1)
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if capturedBody != nil {
			*capturedBody = body
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]string{
			"access_token": accessToken,
			"device_id":    deviceID,
			"user_id":      "@bot:example.com",
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/.well-known/matrix/client", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	return httptest.NewServer(mux)
}

// TestLoginReusesStoredDeviceID verifies that when stored DeviceID exists but AccessToken
// is empty (e.g. token revoked, password retained), loginIfNeeded sends the stored DeviceID
// in the Matrix login request body so the homeserver can re-issue the same device session.
func TestLoginReusesStoredDeviceID(t *testing.T) {
	var loginCount atomic.Int64
	var capturedBody []byte

	const storedDeviceID = "DEVICE_EXISTING_123"
	srv := mockMatrixServerCapturingRequest(t, &loginCount, "tok_reissued", storedDeviceID, &capturedBody)
	defer srv.Close()

	// Simulate: previous run stored device_id but access_token was revoked / not persisted.
	creds := elementCreds{
		Username: "bot",
		Password: "s3cr3t",
		DeviceID: storedDeviceID, // stored from previous login
		// AccessToken intentionally empty — triggers re-login path
	}
	ic := elementInstanceConfig{
		Homeserver: srv.URL,
		UserID:     "@bot:example.com",
	}

	credsRaw, _ := json.Marshal(creds)
	cfgRaw, _ := json.Marshal(ic)

	writer := &stubCredsWriter{}
	factory := FactoryWithCredsWriter(writer)

	ch, err := factory(context.Background(), "test-element", credsRaw, cfgRaw, newTestBus(), nil)
	if err != nil {
		t.Fatalf("factory failed: %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	if loginCount.Load() != 1 {
		t.Errorf("expected 1 /login call, got %d", loginCount.Load())
	}

	// The login request body must include the stored device_id.
	var reqBody struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("unmarshal captured request body: %v (raw: %q)", err, capturedBody)
	}
	if reqBody.DeviceID != storedDeviceID {
		t.Errorf("expected device_id=%q in login request, got %q", storedDeviceID, reqBody.DeviceID)
	}
}
