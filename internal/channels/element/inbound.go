package element

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// registerSyncHandlers wires Matrix sync callbacks for message + invite events.
func (c *Channel) registerSyncHandlers() {
	syncer, ok := c.mxClient.Syncer.(*mautrix.DefaultSyncer)
	if !ok {
		// Replace with a default syncer if the client was constructed with something else.
		syncer = mautrix.NewDefaultSyncer()
		c.mxClient.Syncer = syncer
	}

	syncer.OnEventType(event.EventMessage, c.handleMessageEvent)
	syncer.OnEventType(event.EventEncrypted, c.handleEncryptedEvent)
	if c.cfg.autoJoinInvites {
		syncer.OnEventType(event.StateMember, c.handleMembershipEvent)
	}
}

// runSyncLoop runs mxClient.Sync() and restarts it on transient failure.
// Stops permanently on MUnknownToken or when ctx is cancelled.
func (c *Channel) runSyncLoop(ctx context.Context) {
	consecutiveAuthFails := 0
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.mxClient.Sync()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			// Sync returned cleanly (StopSync called externally).
			return
		}
		if errors.Is(err, mautrix.MUnknownToken) {
			consecutiveAuthFails++
			slog.Error("element: matrix sync auth failure",
				"name", c.Name(), "fails", consecutiveAuthFails, "error", err)
			if consecutiveAuthFails >= 3 {
				slog.Error("element: giving up sync after 3 auth failures", "name", c.Name())
				return
			}
		} else {
			consecutiveAuthFails = 0
			slog.Warn("element: matrix sync error, retrying", "name", c.Name(), "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// handleMessageEvent processes m.room.message events from the sync loop.
func (c *Channel) handleMessageEvent(ctx context.Context, evt *event.Event) {
	if evt == nil || evt.Sender == id.UserID(c.cfg.userID) {
		return
	}
	if evt.Timestamp < c.startupTS {
		return // skip backfilled history older than channel start
	}
	content, ok := evt.Content.Parsed.(*event.MessageEventContent)
	if !ok || content == nil {
		return
	}
	if content.MsgType != event.MsgText && content.MsgType != event.MsgNotice {
		return
	}

	body := content.Body
	if body == "" {
		return
	}

	roomID := string(evt.RoomID)
	senderID := string(evt.Sender)

	if !c.IsAllowed(senderID) && !c.IsAllowed(roomID) {
		return
	}

	// Show typing indicator while the agent processes (auto-expires after 30s).
	go c.signalTyping(evt.RoomID, true)

	// Best-effort contact tracking; senderID is the Matrix user ID (@user:server).
	if cc := c.ContactCollector(); cc != nil {
		cctx := store.WithTenantID(context.Background(), c.TenantID())
		cc.EnsureContact(cctx, c.Type(), c.Name(), senderID, senderID, "", "", "group", "user", "", "")
	}

	c.Bus().PublishInbound(bus.InboundMessage{
		Channel:  c.Name(),
		SenderID: senderID,
		ChatID:   roomID,
		Content:  body,
		PeerKind: "group", // Matrix rooms are conceptually group-like; DMs are also rooms
		UserID:   senderID,
		TenantID: c.TenantID(),
		AgentID:  c.AgentID(),
		Metadata: map[string]string{
			"matrix_event_id": string(evt.ID),
			"matrix_room_id":  roomID,
		},
	})
	_ = ctx
}

// signalTyping sends a typing notification to the room. typing=true keeps the
// indicator alive for typingDuration; typing=false clears it immediately.
// Best-effort: errors are logged but do not propagate.
func (c *Channel) signalTyping(roomID id.RoomID, typing bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dur := time.Duration(0)
	if typing {
		dur = 30 * time.Second
	}
	if _, err := c.mxClient.UserTyping(ctx, roomID, typing, dur); err != nil {
		slog.Debug("element: typing notification failed",
			"name", c.Name(), "room_id", roomID, "typing", typing, "error", err)
	}
}

// handleEncryptedEvent logs and skips m.room.encrypted events (E2EE not supported in v1).
func (c *Channel) handleEncryptedEvent(_ context.Context, evt *event.Event) {
	if evt == nil {
		return
	}
	slog.Warn("element: encrypted event skipped (E2EE not supported)",
		"name", c.Name(), "room_id", evt.RoomID, "event_id", evt.ID)
}

// handleMembershipEvent auto-joins rooms when our user is invited.
func (c *Channel) handleMembershipEvent(ctx context.Context, evt *event.Event) {
	if evt == nil || evt.StateKey == nil {
		return
	}
	if *evt.StateKey != c.cfg.userID {
		return // not about us
	}
	content, ok := evt.Content.Parsed.(*event.MemberEventContent)
	if !ok || content == nil {
		return
	}
	if content.Membership != event.MembershipInvite {
		return
	}

	joinCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := c.mxClient.JoinRoomByID(joinCtx, evt.RoomID); err != nil {
		slog.Warn("element: auto-join failed",
			"name", c.Name(), "room_id", evt.RoomID, "error", err)
		return
	}
	slog.Info("element: auto-joined room", "name", c.Name(), "room_id", evt.RoomID)
}
