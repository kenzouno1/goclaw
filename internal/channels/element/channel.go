//go:build !sqliteonly

package element

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/safego"
)

const (
	// maxMessageLen keeps each Matrix event body well below the 64 KB hard limit.
	maxMessageLen = 4000
	// sendTimeout bounds a single room_send round-trip to the homeserver.
	// 30s accommodates the first-use megolm ShareGroupSession round-trip for
	// encrypted rooms; subsequent sends reuse the session and complete faster.
	sendTimeout = 30 * time.Second
)

// elementConfig is the resolved internal config (populated by Factory).
type elementConfig struct {
	homeserver      string
	userID          string
	accessToken     string
	deviceID        string // Matrix device_id; persisted after first login for E2EE key continuity
	pickleKey       string // 32-byte base64 key for Olm session pickles
	dataDir         string // gateway data directory; empty disables E2EE crypto store
	instanceID      string // channel instance name (used as crypto subdir path component)
	outbound        bool
	inbound         bool
	autoJoinInvites bool
	allowFrom       []string
	historyLimit    int

	// Cross-signing configuration.
	// password is the Matrix account password; only non-empty when keep_password=true.
	// Needed to perform UIA (User-Interactive Auth) on first cross-signing key upload.
	password        string
	// crossSigningSeeds holds persisted cross-signing seeds so restarts import rather than re-upload.
	crossSigningSeeds localCrossSigningSeeds
	// allowVerifyFrom is the allowlist of Matrix user IDs for incoming SAS verification auto-accept.
	allowVerifyFrom []string
	// credsWriter allows Bootstrap to persist updated seeds back to encrypted DB storage.
	credsWriter credsWriterIface
	// disableCrossSigning skips cross-signing bootstrap entirely when true.
	disableCrossSigning bool
}

// Channel implements the Element (Matrix) channel using mautrix-go for both
// inbound (long-poll /sync) and outbound (room_send) traffic.
//
// The helper field is declared in crypto_helper_goolm.go (!sqliteonly && goolm) or
// crypto_helper_stub.go (!sqliteonly && !goolm) to isolate the CGO-free cryptohelper
// import behind the goolm build tag.
type Channel struct {
	*channels.BaseChannel

	cfg      elementConfig
	mxClient *mautrix.Client

	// cryptoState holds the per-instance E2EE helper (nil when goolm tag absent or
	// dataDir not configured). Declared via embedded interface so both build paths
	// compile without exposing cryptohelper types to the non-goolm build.
	cryptoState cryptoStateMachine

	startupTS int64 // ms since epoch; messages older than this are ignored
	syncWG    sync.WaitGroup
	syncCtx   context.Context
	syncStop  context.CancelFunc
}

// Compile-time interface assertions.
var _ channels.Channel = (*Channel)(nil)

// New constructs an Element channel from internal config.
func New(cfg elementConfig, msgBus *bus.MessageBus) (*Channel, error) {
	base := channels.NewBaseChannel(channels.TypeElement, msgBus, cfg.allowFrom)
	if cfg.historyLimit > 0 {
		base.SetHistoryLimit(cfg.historyLimit)
	}

	mx, err := mautrix.NewClient(cfg.homeserver, id.UserID(cfg.userID), cfg.accessToken)
	if err != nil {
		return nil, fmt.Errorf("element: build matrix client: %w", err)
	}
	// Set DeviceID on the client so cryptohelper.Init can verify identity
	// (Init checks client.DeviceID != "" before proceeding).
	if cfg.deviceID != "" {
		mx.DeviceID = id.DeviceID(cfg.deviceID)
	}

	return &Channel{
		BaseChannel: base,
		cfg:         cfg,
		mxClient:    mx,
		cryptoState: newCryptoState(),
	}, nil
}

// Start launches the inbound sync loop (if enabled). Outbound is stateless
// — it just reuses the same mautrix client.
func (c *Channel) Start(ctx context.Context) error {
	if c.cfg.inbound {
		c.startupTS = time.Now().UnixMilli()
		// Register our sync handlers FIRST so cryptohelper.Init can layer on top.
		// cryptohelper.Init registers its own EventEncrypted handler via OnEventType;
		// mautrix DefaultSyncer chains handlers in registration order — the helper's
		// handler auto-decrypts and re-dispatches as m.room.message, which our
		// handleMessageEvent above then receives as plaintext.
		c.registerSyncHandlers()

		// Wire per-instance E2EE crypto store when dataDir is configured (goolm build only).
		if c.cfg.dataDir != "" {
			// Register decrypt error callback BEFORE Init so the first /sync is covered.
			c.cryptoState.SetDecryptErrorCallback(c.onDecryptError)
			if err := c.cryptoState.Init(ctx, c.mxClient, c.cfg, c.Name()); err != nil {
				// Non-fatal: log and continue. Channel works for plaintext rooms.
				slog.Error("element: crypto init failed, E2EE disabled for this instance",
					"name", c.Name(), "error", err)
			} else {
				c.bootstrapCrossSigning(ctx)
				c.registerVerification()
			}
		}

		// detached lifetime: bound by Stop(), not the caller's ctx
		c.syncCtx, c.syncStop = context.WithCancel(context.Background())
		c.syncWG.Add(1)
		go func() {
			defer c.syncWG.Done()
			defer safego.Recover(nil, "channel", "element", "name", c.Name())
			c.runSyncLoop(c.syncCtx)
		}()
	}

	c.SetRunning(true)
	return nil
}

// bootstrapCrossSigning generates or imports cross-signing keys after a successful
// crypto init. Failures are logged and swallowed so the channel still starts.
// Empty credsWriter or disableCrossSigning=true skip the bootstrap entirely.
func (c *Channel) bootstrapCrossSigning(ctx context.Context) {
	if c.cfg.disableCrossSigning {
		slog.Info("element: cross-signing disabled by config", "name", c.Name())
		return
	}
	if c.cfg.credsWriter == nil {
		// No persistence wired (Factory used without write-back) — skip to avoid
		// regenerating seeds on every restart.
		return
	}
	newSeeds, dirty, err := c.cryptoState.LoadOrGenerateCrossSigning(
		ctx, c.cfg.password, c.cfg.crossSigningSeeds,
	)
	if err != nil {
		slog.Warn("element: cross-signing bootstrap failed (continuing without)",
			"name", c.Name(),
			"error", err,
			"hint", "for MAS deployments without MSC3967, reset cross-signing via MAS UI",
		)
		return
	}
	if !dirty {
		return
	}
	if perr := c.persistSeeds(ctx, newSeeds); perr != nil {
		slog.Warn("element: persist cross-signing seeds failed",
			"name", c.Name(), "error", perr)
	}
}

// registerVerification installs the SAS verification handler after cross-signing
// has been bootstrapped. No-op when the allowlist is empty (verification disabled).
// Failures are logged and swallowed — verification is opt-in functionality.
func (c *Channel) registerVerification() {
	if c.cfg.disableCrossSigning || len(c.cfg.allowVerifyFrom) == 0 {
		return
	}
	if err := c.cryptoState.RegisterVerificationHandler(
		c.cfg.allowVerifyFrom,
		c.cfg.userID,
	); err != nil {
		slog.Warn("element: verification handler registration failed",
			"name", c.Name(), "error", err)
	}
}

// persistSeeds writes updated cross-signing seeds back into the encrypted creds blob.
// The store merges with existing creds (preserves access_token, device_id, pickle_key).
// Seed strings are base64url-encoded raw seeds — never logged anywhere.
func (c *Channel) persistSeeds(ctx context.Context, seeds localCrossSigningSeeds) error {
	blob, err := json.Marshal(map[string]string{
		"master_seed":       seeds.MasterKey,
		"self_signing_seed": seeds.SelfSigningKey,
		"user_signing_seed": seeds.UserSigningKey,
	})
	if err != nil {
		return fmt.Errorf("marshal seeds: %w", err)
	}
	return c.cfg.credsWriter.UpdateCredentials(ctx, c.cfg.instanceID, blob)
}

// Stop halts the sync loop and marks the channel stopped.
func (c *Channel) Stop(ctx context.Context) error {
	if c.syncStop != nil {
		c.syncStop()
	}
	c.mxClient.StopSync()

	done := make(chan struct{})
	go func() {
		c.syncWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("element: stop timeout, sync loop did not exit", "name", c.Name())
	case <-time.After(10 * time.Second):
		slog.Warn("element: stop timeout (10s), sync loop did not exit", "name", c.Name())
	}

	// Close crypto helper AFTER stopping sync to avoid races on the DB connection.
	if err := c.cryptoState.Close(); err != nil {
		slog.Warn("element: crypto close error", "name", c.Name(), "error", err)
	}

	c.SetRunning(false)
	return nil
}
