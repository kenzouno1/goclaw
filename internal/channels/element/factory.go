//go:build !sqliteonly

// Package element implements a Matrix/Element channel via the Matrix
// Client-Server API (mautrix-go).
//
// Both inbound (long-poll /sync) and outbound (PUT /rooms/{id}/send/...) go
// through the homeserver using a single access_token — no ess-bot dependency.
//
// E2EE rooms are supported from v2 via mautrix-go cryptohelper; the helper is
// initialised per-instance in Channel.Start and closed in Channel.Stop.
package element

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// elementCreds maps the secret credentials JSON from channel_instances.credentials.
type elementCreds struct {
	// Token auth (legacy path — unchanged).
	AccessToken string `json:"access_token,omitempty"`

	// Password auth (new login flow).
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	KeepPassword bool   `json:"keep_password,omitempty"` // default false — clear password after first login

	// Persisted after first login.
	DeviceID  string `json:"device_id,omitempty"`
	PickleKey string `json:"pickle_key,omitempty"` // 32 random bytes (base64); used by cryptohelper

	// Cross-signing seeds — persisted after first bootstrap so restarts import rather than re-upload.
	// All three are base64url-encoded raw seed bytes (unpadded).
	// SECURITY: these are equivalent to the user's cross-signing private keys — treat with same care as PickleKey.
	MasterSeed      string `json:"master_seed,omitempty"`
	SelfSigningSeed string `json:"self_signing_seed,omitempty"`
	UserSigningSeed string `json:"user_signing_seed,omitempty"`
}

// elementInstanceConfig maps the non-secret config JSONB from channel_instances.config.
type elementInstanceConfig struct {
	Homeserver      string   `json:"homeserver,omitempty"`        // Matrix homeserver URL, e.g. https://matrix.example.com
	UserID          string   `json:"user_id,omitempty"`           // Matrix user_id, e.g. @bot:example.com
	OutboundEnabled *bool    `json:"outbound_enabled,omitempty"`  // default true
	InboundEnabled  *bool    `json:"inbound_enabled,omitempty"`   // default true
	AutoJoinInvites *bool    `json:"auto_join_invites,omitempty"` // default true (when inbound enabled)
	AllowFrom       []string `json:"allow_from,omitempty"`
	HistoryLimit    int      `json:"history_limit,omitempty"`
	// AllowVerifyFrom is the allowlist of Matrix user IDs from which incoming SAS verification
	// requests are auto-accepted. Defaults to AllowFrom if empty. Verification from users
	// not in this list is rejected. Applies when cross-signing is bootstrapped.
	AllowVerifyFrom []string `json:"allow_verify_from,omitempty"`
	// DisableCrossSigning skips cross-signing key bootstrap on startup. Default false.
	// Useful on MAS deployments without MSC3967 where upload requires an out-of-band reset.
	DisableCrossSigning *bool `json:"disable_cross_signing,omitempty"`
}

// Factory creates an Element channel from DB instance data.
// Signature matches channels.ChannelFactory.
// Uses token-only path — no write-back possible. For login flow, use FactoryWithCredsWriter.
// dataDir is empty string — no per-instance crypto dir will be created (tests / non-gateway use).
func Factory(name string, creds json.RawMessage, cfg json.RawMessage,
	msgBus *bus.MessageBus, _ store.PairingStore) (channels.Channel, error) {

	return factoryImpl(context.Background(), name, creds, cfg, msgBus, nil, "")
}

// FactoryWithCredsWriter returns a ContextualChannelFactory that supports the password-login
// auth path. After a successful login the issued access_token + device_id are persisted back
// to the DB via writer (the store handles AES-256-GCM encryption internally).
//
// The instance name (passed as `name` by InstanceLoader) is used as the write-back key.
// The caller's ctx (with tenant_id) is propagated so the store can scope the write correctly.
// Register via instanceLoader.RegisterContextualFactory (not RegisterFactory).
//
// Deprecated: prefer FactoryWithCredsWriterAndDataDir for production (enables E2EE crypto store).
func FactoryWithCredsWriter(writer channels.CredsWriter) channels.ContextualChannelFactory {
	return FactoryWithCredsWriterAndDataDir(writer, "")
}

// FactoryWithCredsWriterAndDataDir returns a ContextualChannelFactory that supports both
// the password-login auth path and per-instance E2EE crypto storage.
//
// dataDir is the gateway data directory (e.g. ~/.goclaw/data). A per-instance SQLite
// crypto file is created at <dataDir>/element/<instanceName>/crypto.sqlite with 0700 dir perms.
// When dataDir is empty, E2EE crypto storage is disabled (channel still works for plaintext rooms).
func FactoryWithCredsWriterAndDataDir(writer channels.CredsWriter, dataDir string) channels.ContextualChannelFactory {
	return func(ctx context.Context, name string, creds json.RawMessage, cfg json.RawMessage,
		msgBus *bus.MessageBus, _ store.PairingStore) (channels.Channel, error) {

		return factoryImpl(ctx, name, creds, cfg, msgBus, writer, dataDir)
	}
}

// factoryImpl is the shared implementation for all factory variants.
func factoryImpl(
	ctx context.Context,
	name string,
	rawCreds json.RawMessage,
	rawCfg json.RawMessage,
	msgBus *bus.MessageBus,
	writer channels.CredsWriter,
	dataDir string,
) (channels.Channel, error) {

	var c elementCreds
	if len(rawCreds) > 0 {
		if err := json.Unmarshal(rawCreds, &c); err != nil {
			return nil, fmt.Errorf("decode element credentials: %w", err)
		}
	}
	var ic elementInstanceConfig
	if len(rawCfg) > 0 {
		if err := json.Unmarshal(rawCfg, &ic); err != nil {
			return nil, fmt.Errorf("decode element config: %w", err)
		}
	}

	outbound := true
	if ic.OutboundEnabled != nil {
		outbound = *ic.OutboundEnabled
	}
	inbound := true
	if ic.InboundEnabled != nil {
		inbound = *ic.InboundEnabled
	}
	if !outbound && !inbound {
		return nil, fmt.Errorf("element: at least one of outbound_enabled or inbound_enabled must be true")
	}

	if ic.Homeserver == "" {
		return nil, fmt.Errorf("element: homeserver is required")
	}
	if ic.UserID == "" {
		return nil, fmt.Errorf("element: user_id is required")
	}

	// Reconcile lingering state: if a previous login already produced access_token
	// (post-login state) and the user re-submitted username/password via the UI,
	// the store-side credentials merge leaves both present in the DB. Treat the
	// access_token as authoritative — it's the already-authenticated state — and
	// drop the now-redundant login fields so the DB gets cleaned on next persist.
	var dirty bool
	if c.AccessToken != "" && (c.Username != "" || c.Password != "") {
		slog.Warn("element: both auth methods present; using access_token and clearing username/password",
			"instance", name)
		c.Username = ""
		c.Password = ""
		dirty = true
	}

	// Validate auth shape: must have exactly one of token OR (username+password).
	if err := validateAuthShape(&c); err != nil {
		return nil, err
	}

	// Login flow: exchange username+password for access_token+device_id.
	if c.AccessToken == "" {
		// loginIfNeeded returns loggedIn=true when it mutated creds.
		loggedIn, err := loginIfNeeded(ctx, ic.Homeserver, &c)
		if err != nil {
			return nil, err
		}
		if loggedIn {
			dirty = true
		}
	}

	// Generate pickle_key on first use (token path included — cryptohelper needs it for both paths).
	if ensurePickleKey(&c) {
		dirty = true
	}

	// Clear password unless caller opted in to keeping it.
	if !c.KeepPassword && c.Password != "" {
		c.Password = ""
		dirty = true
	}

	// Persist mutated creds back to the DB.
	if dirty && writer != nil {
		// Warn with device_id before persisting so operators can recover if persist fails.
		slog.Warn("element.login.persisting_device",
			"instance", name,
			"device_id", c.DeviceID,
			"user_id", ic.UserID,
		)
		if err := persistCreds(ctx, writer, name, &c); err != nil {
			// Non-fatal: log and continue — channel still works this session.
			// On restart without access_token persisted, login will be retried.
			return nil, fmt.Errorf("element: persist credentials after login: %w", err)
		}
	}

	autoJoin := true
	if ic.AutoJoinInvites != nil {
		autoJoin = *ic.AutoJoinInvites
	}

	disableCS := false
	if ic.DisableCrossSigning != nil {
		disableCS = *ic.DisableCrossSigning
	}

	internalCfg := elementConfig{
		homeserver:      ic.Homeserver,
		userID:          ic.UserID,
		accessToken:     c.AccessToken,
		deviceID:        c.DeviceID,
		pickleKey:       c.PickleKey,
		dataDir:         dataDir,
		instanceID:      name,
		outbound:        outbound,
		inbound:         inbound,
		autoJoinInvites: autoJoin,
		allowFrom:       ic.AllowFrom,
		historyLimit:    ic.HistoryLimit,
		// Cross-signing bootstrap inputs.
		password: c.Password,
		crossSigningSeeds: localCrossSigningSeeds{
			MasterKey:      c.MasterSeed,
			SelfSigningKey: c.SelfSigningSeed,
			UserSigningKey: c.UserSigningSeed,
		},
		allowVerifyFrom:     ic.AllowVerifyFrom,
		disableCrossSigning: disableCS,
	}
	if writer != nil {
		internalCfg.credsWriter = writer
	}

	ch, err := New(internalCfg, msgBus)
	if err != nil {
		return nil, err
	}
	ch.SetName(name)
	return ch, nil
}

// persistCreds serializes the updated creds to JSON and writes them to the DB via writer.
// The store is responsible for AES-256-GCM encryption; this function passes plaintext.
// instanceID is the channel instance name (DB lookup key).
func persistCreds(ctx context.Context, writer channels.CredsWriter, instanceID string, c *elementCreds) error {
	blob, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	return writer.UpdateCredentials(ctx, instanceID, blob)
}
