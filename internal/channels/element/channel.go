package element

import (
	"context"
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
	sendTimeout = 15 * time.Second
)

// elementConfig is the resolved internal config (populated by Factory).
type elementConfig struct {
	homeserver      string
	userID          string
	accessToken     string
	outbound        bool
	inbound         bool
	autoJoinInvites bool
	allowFrom       []string
	historyLimit    int
}

// Channel implements the Element (Matrix) channel using mautrix-go for both
// inbound (long-poll /sync) and outbound (room_send) traffic.
type Channel struct {
	*channels.BaseChannel

	cfg      elementConfig
	mxClient *mautrix.Client

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

	return &Channel{
		BaseChannel: base,
		cfg:         cfg,
		mxClient:    mx,
	}, nil
}

// Start launches the inbound sync loop (if enabled). Outbound is stateless
// — it just reuses the same mautrix client.
func (c *Channel) Start(ctx context.Context) error {
	if c.cfg.inbound {
		c.startupTS = time.Now().UnixMilli()
		c.registerSyncHandlers()

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

	c.SetRunning(false)
	return nil
}
