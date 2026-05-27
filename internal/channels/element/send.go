//go:build !sqliteonly

package element

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// matrixSender is the subset of *mautrix.Client used by sendChunk.
// Extracted for unit-testability — *mautrix.Client satisfies this interface.
type matrixSender interface {
	SendMessageEvent(ctx context.Context, roomID id.RoomID, eventType event.Type, contentJSON interface{}, extra ...mautrix.ReqSendEvent) (*mautrix.RespSendEvent, error)
}

// Compile-time guard: *mautrix.Client must satisfy matrixSender.
var _ matrixSender = (*mautrix.Client)(nil)

// Send delivers an outbound message to a Matrix room via the homeserver
// Client-Server API (PUT /rooms/{id}/send/m.room.message). Markdown is
// rendered to HTML and sent as `formatted_body` with `body` as plain-text
// fallback (per Matrix m.room.message spec).
//
// Encrypted rooms (where cryptoState.IsRoomEncrypted returns true) use
// cryptoState.EncryptMessage → SendMessageEvent(EventEncrypted). Plaintext
// rooms use the existing m.room.message path unchanged.
func (c *Channel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return fmt.Errorf("element channel not running")
	}
	if !c.cfg.outbound {
		return fmt.Errorf("element: outbound disabled for instance %q", c.Name())
	}
	if msg.ChatID == "" {
		return fmt.Errorf("element: empty chat_id (room_id) for send")
	}

	roomID := id.RoomID(msg.ChatID)
	// Clear typing indicator before returning — must run for NO_REPLY too,
	// otherwise the indicator hangs until Matrix's 30s auto-expire.
	go c.signalTyping(roomID, false)

	if msg.Content == "" {
		return nil // NO_REPLY semantics
	}

	for _, chunk := range channels.ChunkMarkdown(msg.Content, maxMessageLen) {
		if err := c.sendChunk(ctx, roomID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// sendChunk emits one Matrix message event for the given body.
// When the room is encrypted and the crypto helper is ready, it emits
// m.room.encrypted; otherwise it emits a plaintext m.room.message.
func (c *Channel) sendChunk(ctx context.Context, roomID id.RoomID, body string) error {
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          body,
		Format:        event.FormatHTML,
		FormattedBody: markdownToHTML(body),
	}

	// Attempt encrypted send when the crypto helper is active and the room
	// has an m.room.encryption state event (populated by cryptohelper on /sync).
	if c.cryptoState.IsReady() && c.cryptoState.IsRoomEncrypted(sendCtx, roomID) {
		return c.sendEncrypted(sendCtx, roomID, content)
	}
	return c.sendPlaintext(sendCtx, roomID, content)
}

// sendPlaintext emits an unencrypted m.room.message event.
func (c *Channel) sendPlaintext(ctx context.Context, roomID id.RoomID, content *event.MessageEventContent) error {
	_, err := c.mxClient.SendMessageEvent(ctx, roomID, event.EventMessage, content)
	if err != nil {
		return fmt.Errorf("element: send to %s: %w", roomID, err)
	}
	return nil
}

// sendEncrypted encrypts content via the crypto helper and emits m.room.encrypted.
// Falls back to plaintext if EncryptMessage returns nil (helper not ready).
func (c *Channel) sendEncrypted(ctx context.Context, roomID id.RoomID, content *event.MessageEventContent) error {
	enc, err := c.cryptoState.EncryptMessage(ctx, roomID, content)
	if err != nil {
		return fmt.Errorf("element: encrypt for %s: %w", roomID, err)
	}
	if enc == nil {
		// Helper reported not ready at encrypt time; fall back to plaintext.
		return c.sendPlaintext(ctx, roomID, content)
	}
	_, err = c.mxClient.SendMessageEvent(ctx, roomID, event.EventEncrypted, enc)
	if err != nil {
		return fmt.Errorf("element: send encrypted to %s: %w", roomID, err)
	}
	return nil
}
