//go:build !sqliteonly

package element

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// loginRateLimit is the minimum interval between login calls per instance.
// Guards against lockout cascades when persistence fails and Factory is called repeatedly.
const loginRateLimit = time.Minute

// lastLoginAttempt tracks per-homeserver+username last login time (in-process guard only).
var lastLoginAttempt = struct {
	mu      chan struct{}
	entries map[string]time.Time
}{
	mu:      make(chan struct{}, 1),
	entries: make(map[string]time.Time),
}

func init() {
	lastLoginAttempt.mu <- struct{}{}
}

// checkLoginRateLimit returns an error if a login was attempted recently for this key.
func checkLoginRateLimit(key string) error {
	<-lastLoginAttempt.mu
	defer func() { lastLoginAttempt.mu <- struct{}{} }()
	if t, ok := lastLoginAttempt.entries[key]; ok {
		if time.Since(t) < loginRateLimit {
			return fmt.Errorf("element: login rate limit: last attempt was %s ago (min interval %s)",
				time.Since(t).Round(time.Second), loginRateLimit)
		}
	}
	lastLoginAttempt.entries[key] = time.Now()
	return nil
}

// validateHomeserverURL rejects http:// URLs except for localhost/127.0.0.1 (dev).
func validateHomeserverURL(rawURL string) error {
	if rawURL == "" {
		return nil // missing homeserver checked elsewhere
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("element: invalid homeserver URL: %w", err)
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		if host != "localhost" && host != "127.0.0.1" && !strings.HasPrefix(host, "[::1]") {
			return fmt.Errorf("element: login requires HTTPS homeserver (got http://%s); use https:// or a local address", host)
		}
		slog.Warn("element: using insecure http homeserver — only acceptable for local development",
			"homeserver", rawURL)
	}
	return nil
}

// validateAuthShape returns an error when creds supply neither or both auth methods.
func validateAuthShape(c *elementCreds) error {
	hasToken := c.AccessToken != ""
	hasLogin := c.Username != "" || c.Password != ""

	if hasToken && hasLogin {
		return fmt.Errorf("element: specify either access_token OR (username + password), not both")
	}
	if !hasToken && !hasLogin {
		return fmt.Errorf("element: credentials must include either access_token or username + password")
	}
	if hasLogin && c.Username == "" {
		return fmt.Errorf("element: username is required when using password auth")
	}
	if hasLogin && c.Password == "" {
		return fmt.Errorf("element: password is required when using password auth")
	}
	return nil
}

// ensurePickleKey generates a 32-byte random pickle key (base64) when absent.
// Returns dirty=true if the key was generated.
func ensurePickleKey(c *elementCreds) bool {
	if c.PickleKey != "" {
		return false
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is catastrophic; surface it loud.
		slog.Error("element: failed to generate pickle_key", "error", err)
		return false
	}
	c.PickleKey = base64.StdEncoding.EncodeToString(buf)
	return true
}

// loginIfNeeded performs the Matrix password login when the stored credentials
// do not already contain a valid access_token (username+password path).
//
// Returns dirty=true when creds were mutated and must be re-persisted.
// The caller is responsible for persisting dirty creds before returning to the caller.
func loginIfNeeded(ctx context.Context, homeserver string, c *elementCreds) (dirty bool, err error) {
	// If access_token already present (could be from a previous login that was persisted),
	// skip the login call — this is the "second startup" fast path.
	if c.AccessToken != "" {
		return false, nil
	}

	// Security: reject insecure homeserver URLs.
	if err := validateHomeserverURL(homeserver); err != nil {
		return false, err
	}

	// Rate-limit guard.
	rateLimitKey := homeserver + "|" + c.Username
	if err := checkLoginRateLimit(rateLimitKey); err != nil {
		return false, err
	}

	slog.Info("element: performing Matrix password login",
		"homeserver", homeserver, "username", c.Username)

	// Build a temporary client for the login call only.
	cli, err := mautrix.NewClient(homeserver, "", "")
	if err != nil {
		return false, fmt.Errorf("element: build matrix client for login: %w", err)
	}

	resp, err := cli.Login(ctx, &mautrix.ReqLogin{
		Type: mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{
			Type: mautrix.IdentifierTypeUser,
			User: c.Username,
		},
		Password:         c.Password,
		DeviceID:         id.DeviceID(c.DeviceID), // reuse stored device_id on restart; empty = new device
		StoreCredentials: true,
	})
	if err != nil {
		return false, fmt.Errorf("element: Matrix login failed: %w", err)
	}

	if resp.AccessToken == "" || resp.DeviceID == "" {
		return false, fmt.Errorf("element: Matrix login returned empty access_token or device_id")
	}

	c.AccessToken = resp.AccessToken
	c.DeviceID = string(resp.DeviceID)
	// UserID from response — use as the configured user_id if not already set.
	// (Factory validates user_id from instanceConfig separately; this is a cross-check.)

	slog.Info("element: Matrix login succeeded",
		"user_id", string(resp.UserID),
		"device_id", string(resp.DeviceID))

	return true, nil
}
