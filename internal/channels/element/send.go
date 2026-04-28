package element

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// Send delivers an outbound message to a Matrix room via the homeserver
// Client-Server API (PUT /rooms/{id}/send/m.room.message). Markdown is
// rendered to HTML and sent as `formatted_body` with `body` as plain-text
// fallback (per Matrix m.room.message spec).
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
	if msg.Content == "" {
		return nil // NO_REPLY semantics
	}

	roomID := id.RoomID(msg.ChatID)
	// Clear typing indicator before sending the actual reply.
	go c.signalTyping(roomID, false)

	for _, chunk := range channels.ChunkMarkdown(msg.Content, maxMessageLen) {
		if err := c.sendChunk(ctx, roomID, chunk); err != nil {
			return err
		}
	}
	return nil
}

// sendChunk emits one m.room.message event with markdown body + HTML formatted_body.
func (c *Channel) sendChunk(ctx context.Context, roomID id.RoomID, body string) error {
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          body,
		Format:        event.FormatHTML,
		FormattedBody: markdownToHTML(body),
	}

	_, err := c.mxClient.SendMessageEvent(sendCtx, roomID, event.EventMessage, content)
	if err != nil {
		return fmt.Errorf("element: send to %s: %w", roomID, err)
	}
	return nil
}

