package bus

import (
	"testing"
	"time"
)

func TestInboundDebouncerMergesRapidText(t *testing.T) {
	out := make(chan InboundMessage, 1)
	d := NewInboundDebouncer(20*time.Millisecond, func(msg InboundMessage) {
		out <- msg
	})
	defer d.Stop()

	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", Content: "one"})
	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", Content: "two", Metadata: map[string]string{"message_id": "m2"}})

	got := waitInbound(t, out)
	if got.Content != "one\ntwo" {
		t.Fatalf("merged content = %q, want %q", got.Content, "one\ntwo")
	}
	if got.Metadata["message_id"] != "m2" {
		t.Fatalf("metadata should come from latest message, got %#v", got.Metadata)
	}
}

func TestInboundDebouncerDisabledPassesThrough(t *testing.T) {
	out := make(chan InboundMessage, 2)
	d := NewInboundDebouncer(0, func(msg InboundMessage) {
		out <- msg
	})

	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", Content: "one"})
	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", Content: "two"})

	if got := waitInbound(t, out); got.Content != "one" {
		t.Fatalf("first content = %q", got.Content)
	}
	if got := waitInbound(t, out); got.Content != "two" {
		t.Fatalf("second content = %q", got.Content)
	}
}

func TestInboundDebouncerDynamicDelay(t *testing.T) {
	out := make(chan InboundMessage, 2)
	d := NewInboundDebouncerFunc(func(msg InboundMessage) time.Duration {
		if msg.AgentID == "instant" {
			return 0
		}
		return 20 * time.Millisecond
	}, func(msg InboundMessage) {
		out <- msg
	})
	defer d.Stop()

	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", AgentID: "instant", Content: "one"})
	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", AgentID: "debounced", Content: "two"})
	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", AgentID: "debounced", Content: "three"})

	if got := waitInbound(t, out); got.Content != "one" {
		t.Fatalf("instant content = %q", got.Content)
	}
	if got := waitInbound(t, out); got.Content != "two\nthree" {
		t.Fatalf("debounced content = %q", got.Content)
	}
}

func TestInboundDebouncerSeparatesAgents(t *testing.T) {
	out := make(chan InboundMessage, 2)
	d := NewInboundDebouncer(20*time.Millisecond, func(msg InboundMessage) {
		out <- msg
	})
	defer d.Stop()

	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", AgentID: "agent-a", Content: "a"})
	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", AgentID: "agent-b", Content: "b"})

	first := waitInbound(t, out)
	second := waitInbound(t, out)
	got := map[string]string{first.AgentID: first.Content, second.AgentID: second.Content}
	if got["agent-a"] != "a" || got["agent-b"] != "b" {
		t.Fatalf("agent buffers = %#v, want separate flushes", got)
	}
}

func TestInboundDebouncerMediaFlushesPendingTextFirst(t *testing.T) {
	out := make(chan InboundMessage, 2)
	d := NewInboundDebouncer(time.Minute, func(msg InboundMessage) {
		out <- msg
	})
	defer d.Stop()

	d.Push(InboundMessage{Channel: "telegram", ChatID: "chat-1", SenderID: "user-1", Content: "pending"})
	d.Push(InboundMessage{
		Channel:  "telegram",
		ChatID:   "chat-1",
		SenderID: "user-1",
		Content:  "with media",
		Media:    []MediaFile{{Path: "/tmp/a.png", MimeType: "image/png"}},
	})

	if got := waitInbound(t, out); got.Content != "pending" || len(got.Media) != 0 {
		t.Fatalf("first flush = %#v, want pending text without media", got)
	}
	if got := waitInbound(t, out); got.Content != "with media" || len(got.Media) != 1 {
		t.Fatalf("second flush = %#v, want media message", got)
	}
}

func waitInbound(t *testing.T, ch <-chan InboundMessage) InboundMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for debounced message")
		return InboundMessage{}
	}
}
