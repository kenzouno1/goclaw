//go:build !sqliteonly

package element

import (
	"log/slog"

	"maunium.net/go/mautrix/event"
)

// onDecryptError is the DecryptErrorCallback wired onto the cryptohelper before Init.
// It logs a structured warning and does NOT publish anything to the message bus —
// guarding against corrupted or attacker-controlled plaintext reaching the agent.
//
// Registered via cryptoStateMachine.SetDecryptErrorCallback in Channel.Start.
// Signature matches cryptohelper.DecryptErrorCallback: func(*event.Event, error).
func (c *Channel) onDecryptError(evt *event.Event, err error) {
	if evt == nil {
		slog.Warn("element: decrypt failed for nil event", "name", c.Name(), "error", err)
		return
	}
	slog.Warn("element: decrypt failed — event not delivered to agent",
		"name", c.Name(),
		"room_id", evt.RoomID,
		"event_id", evt.ID,
		"sender", evt.Sender,
		"error", err,
	)
	// Intentionally no bus.PublishInbound — corrupted body must never reach the agent.
}
