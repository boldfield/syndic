package server_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

func TestConversationAPIReadsWork(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	convs, err := c.Conversations(ctx, nil, "", 10)
	if err != nil {
		t.Fatalf("Conversations failed: %v", err)
	}
	if convs.Items == nil {
		t.Fatal("expected Items to be non-nil slice")
	}
	if len(convs.Items) != 0 {
		t.Fatalf("expected no conversations initially, got %d", len(convs.Items))
	}
}

func TestUnknownConversationError(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := c.Conversation(ctx, "unknown-id-prefix")
	if err == nil {
		t.Fatal("expected error for unknown conversation")
	}

	var wireErr *wire.Error
	if !asWireError(err, &wireErr) || wireErr.Code != "not_found" {
		t.Fatalf("expected not_found error, got %v", err)
	}
}

func TestConversationReturnsResolvedID(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sr, err := c.Submit(ctx, soloReq("test-conv-1"))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	cr, err := c.Conversation(ctx, sr.ConversationID[:16])
	if err != nil {
		t.Fatalf("Conversation failed: %v", err)
	}

	if cr.ID != sr.ConversationID {
		t.Fatalf("expected full ID %s, got %s", sr.ConversationID, cr.ID)
	}
}

func TestReconnectRetrievesLatestState(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sr, err := c.Submit(ctx, soloReq("test-state-1"))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	cr, err := c.Conversation(ctx, sr.ConversationID)
	if err != nil {
		t.Fatalf("Conversation failed: %v", err)
	}

	if len(cr.Turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(cr.Turns))
	}

	turn := cr.Turns[0]
	if turn.ID != sr.TurnID {
		t.Fatalf("expected turn ID %s, got %s", sr.TurnID, turn.ID)
	}

	if turn.State == "" {
		t.Fatal("expected non-empty state for running turn")
	}
}

func TestConversationListPaging(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cr, err := c.Conversations(ctx, nil, "", 1)
	if err != nil {
		t.Fatalf("Conversations failed: %v", err)
	}

	if len(cr.Items) != 0 {
		t.Fatalf("expected 0 items initially, got %d", len(cr.Items))
	}
}

func TestConversationFrameLimit(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sr, err := c.Submit(ctx, soloReq("test-frame-1"))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	cr, err := c.Conversation(ctx, sr.ConversationID)
	if err != nil {
		t.Fatalf("Conversation failed: %v", err)
	}

	bodyJSON, _ := json.Marshal(cr)
	frameJSON, _ := json.Marshal(wire.Frame{
		V:    wire.ProtocolVersion,
		ID:   "test",
		Type: "reply",
		Kind: "conversation",
		Body: bodyJSON,
	})
	encodedLen := len(frameJSON) + 1

	if encodedLen > wire.MaxFrameBytes {
		t.Fatalf("frame size %d exceeds MaxFrameBytes %d", encodedLen, wire.MaxFrameBytes)
	}
}

func TestPrefixResolution(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sr, err := c.Submit(ctx, soloReq("test-prefix-1"))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	for i := 8; i <= len(sr.ConversationID); i++ {
		prefix := sr.ConversationID[:i]
		cr, err := c.Conversation(ctx, prefix)
		if err != nil {
			t.Fatalf("Conversation with prefix %s failed: %v", prefix, err)
		}
		if cr.ID != sr.ConversationID {
			t.Fatalf("prefix %s: expected ID %s, got %s", prefix, sr.ConversationID, cr.ID)
		}
	}
}

func asWireError(err error, we **wire.Error) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*wire.Error); ok {
		*we = e
		return true
	}
	return false
}
