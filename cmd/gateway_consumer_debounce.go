package cmd

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func prepareInboundDebounceMessage(msg *bus.InboundMessage, deps *ConsumerDeps) {
	if msg == nil || deps == nil || deps.Cfg == nil || msg.AgentID != "" {
		return
	}
	msg.AgentID = resolveAgentRoute(deps.Cfg, msg.Channel, msg.ChatID, msg.PeerKind)
}

func resolveInboundDebounceDelay(ctx context.Context, msg bus.InboundMessage, deps *ConsumerDeps) time.Duration {
	debounceMs := 0
	if deps != nil && deps.Cfg != nil {
		debounceMs = deps.Cfg.Gateway.InboundDebounceMs
	}
	if deps == nil || deps.AgentStore == nil || msg.AgentID == "" {
		return inboundDebounceDuration(debounceMs)
	}

	agentCtx := ctx
	if msg.TenantID != uuid.Nil {
		agentCtx = store.WithTenantID(agentCtx, msg.TenantID)
	} else {
		agentCtx = store.WithTenantID(agentCtx, store.MasterTenantID)
	}

	agentData, err := getInboundDebounceAgent(agentCtx, deps.AgentStore, msg.AgentID)
	if err != nil || agentData == nil {
		if err != nil {
			slog.Debug("inbound debounce: agent config unavailable", "agent", msg.AgentID, "error", err)
		}
		return inboundDebounceDuration(debounceMs)
	}
	if overrideMs, ok := agentData.ParseInboundDebounceMs(); ok {
		debounceMs = overrideMs
	}
	return inboundDebounceDuration(debounceMs)
}

func getInboundDebounceAgent(ctx context.Context, agentStore store.AgentStore, agentID string) (*store.AgentData, error) {
	if parsed, err := uuid.Parse(agentID); err == nil && parsed != uuid.Nil {
		return agentStore.GetByID(ctx, parsed)
	}
	return agentStore.GetByKey(ctx, agentID)
}

func inboundDebounceDuration(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
