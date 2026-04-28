// Package element implements a Matrix/Element channel via the Matrix
// Client-Server API (mautrix-go).
//
// Both inbound (long-poll /sync) and outbound (PUT /rooms/{id}/send/...) go
// through the homeserver using a single access_token — no ess-bot dependency.
//
// E2EE rooms are not supported in v1; encrypted events are logged and skipped.
package element

import (
	"encoding/json"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// elementCreds maps the secret credentials JSON from channel_instances.credentials.
type elementCreds struct {
	AccessToken string `json:"access_token,omitempty"` // Matrix homeserver access token (used for both inbound + outbound)
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
}

// Factory creates an Element channel from DB instance data.
// Signature matches channels.ChannelFactory.
func Factory(name string, creds json.RawMessage, cfg json.RawMessage,
	msgBus *bus.MessageBus, _ store.PairingStore) (channels.Channel, error) {

	var c elementCreds
	if len(creds) > 0 {
		if err := json.Unmarshal(creds, &c); err != nil {
			return nil, fmt.Errorf("decode element credentials: %w", err)
		}
	}
	var ic elementInstanceConfig
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &ic); err != nil {
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

	// Both directions need the same Matrix credentials.
	if ic.Homeserver == "" {
		return nil, fmt.Errorf("element: homeserver is required")
	}
	if ic.UserID == "" {
		return nil, fmt.Errorf("element: user_id is required")
	}
	if c.AccessToken == "" {
		return nil, fmt.Errorf("element: access_token is required")
	}

	autoJoin := true
	if ic.AutoJoinInvites != nil {
		autoJoin = *ic.AutoJoinInvites
	}

	internalCfg := elementConfig{
		homeserver:      ic.Homeserver,
		userID:          ic.UserID,
		accessToken:     c.AccessToken,
		outbound:        outbound,
		inbound:         inbound,
		autoJoinInvites: autoJoin,
		allowFrom:       ic.AllowFrom,
		historyLimit:    ic.HistoryLimit,
	}

	ch, err := New(internalCfg, msgBus)
	if err != nil {
		return nil, err
	}
	ch.SetName(name)
	return ch, nil
}
