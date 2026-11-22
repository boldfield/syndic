package server_test

import (
	"context"
	"fmt"
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
	c1 := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Submit on the first connection
	sr, err := c1.Submit(ctx, soloReq("test-state-1"))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	// Wait for the turn to complete so we can submit another one
	for {
		tr, err := c1.Turn(ctx, sr.TurnID)
		if err != nil {
			t.Fatalf("Turn failed: %v", err)
		}
		if tr.State != "" && tr.State != "submitted" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Get conversation state on first connection
	cr1, err := c1.Conversation(ctx, sr.ConversationID)
	if err != nil {
		t.Fatalf("Conversation on c1 failed: %v", err)
	}

	if len(cr1.Turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(cr1.Turns))
	}

	if cr1.Turns[0].State == "" {
		t.Fatal("expected non-empty state for turn")
	}

	// Reconnect with a new client connection
	c2 := s.dial()

	// Get conversation state on the new connection and verify it matches
	cr2, err := c2.Conversation(ctx, sr.ConversationID)
	if err != nil {
		t.Fatalf("Conversation on c2 failed: %v", err)
	}

	if len(cr2.Turns) != 1 {
		t.Fatalf("expected 1 turn on c2, got %d", len(cr2.Turns))
	}

	if cr2.Turns[0].State != cr1.Turns[0].State {
		t.Fatalf("state diverged between connections: %s vs %s", cr1.Turns[0].State, cr2.Turns[0].State)
	}
}

func TestConversationListPaging(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create multiple conversations so we can test pagination
	for i := 0; i < 5; i++ {
		sr, err := c.Submit(ctx, soloReq(fmt.Sprintf("page-test-%d", i)))
		if err != nil {
			t.Fatalf("Submit %d failed: %v", i, err)
		}

		// Wait for turn to complete so we can submit the next one
		for {
			tr, err := c.Turn(ctx, sr.TurnID)
			if err != nil {
				t.Fatalf("Turn failed: %v", err)
			}
			if tr.State != "" && tr.State != "submitted" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Fetch with limit 2 and walk through all pages using next_cursor
	seen := make(map[string]bool)
	cursor := ""
	pageCount := 0
	for {
		pageCount++
		if pageCount > 10 {
			t.Fatal("too many pages, likely infinite loop")
		}
		cr, err := c.Conversations(ctx, nil, cursor, 2)
		if err != nil {
			t.Fatalf("Conversations failed: %v", err)
		}

		if len(cr.Items) == 0 {
			break
		}
		if len(cr.Items) > 2 {
			t.Fatalf("expected at most 2 items per page, got %d", len(cr.Items))
		}
		for _, item := range cr.Items {
			if seen[item.ID] {
				t.Fatalf("conversation %s seen twice, pagination loop detected", item.ID)
			}
			seen[item.ID] = true
		}

		if cr.NextCursor == nil || *cr.NextCursor == "" {
			break
		}
		cursor = *cr.NextCursor
	}

	if len(seen) != 5 {
		t.Fatalf("expected to see 5 conversations, saw %d", len(seen))
	}
}

func TestConversationFrameLimit(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create a reasonable number of conversations for pagination testing
	for i := 0; i < 50; i++ {
		sr, err := c.Submit(ctx, wire.SubmitRequest{
			SubmissionID: fmt.Sprintf("frame-test-%d", i),
			Input:        "test input",
			Preset:       ptrStr("solo"),
		})
		if err != nil {
			t.Fatalf("Submit failed: %v", err)
		}

		// Wait for turn to complete so we can submit the next one
		for {
			tr, err := c.Turn(ctx, sr.TurnID)
			if err != nil {
				t.Fatalf("Turn failed: %v", err)
			}
			if tr.State != "" && tr.State != "submitted" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Test that cursor-based pagination works correctly.
	// The key fix we're testing is that next_cursor is built from the last item kept,
	// not echoed from the request, so pagination doesn't loop infinitely.
	seenIDs := make(map[string]bool)
	cursor := ""
	pageCount := 0
	itemCount := 0

	for {
		pageCount++
		if pageCount > 10 {
			t.Fatal("too many pages - likely infinite loop due to bad next_cursor")
		}

		cr, err := c.Conversations(ctx, nil, cursor, 10)
		if err != nil {
			t.Fatalf("Conversations failed: %v", err)
		}

		if len(cr.Items) == 0 {
			break
		}

		// Verify no duplicate IDs across pages (would indicate a pagination loop)
		for _, item := range cr.Items {
			if seenIDs[item.ID] {
				t.Fatalf("conversation %s seen twice - pagination loop detected", item.ID)
			}
			seenIDs[item.ID] = true
		}

		itemCount += len(cr.Items)

		// If we have more items and there's a next cursor, continue
		if cr.NextCursor != nil && *cr.NextCursor != "" {
			cursor = *cr.NextCursor
		} else {
			break
		}
	}

	// Verify we saw all 50 conversations exactly once
	if itemCount != 50 {
		t.Fatalf("expected to see 50 conversations total, saw %d", itemCount)
	}
}

func ptrStr(s string) *string {
	return &s
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

func TestAmbiguousConversationPrefixError(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create two conversations with IDs that share a prefix
	sr1, err := c.Submit(ctx, soloReq("ambig-1"))
	if err != nil {
		t.Fatalf("Submit 1 failed: %v", err)
	}

	// Wait for first turn to complete
	for {
		tr, err := c.Turn(ctx, sr1.TurnID)
		if err != nil {
			t.Fatalf("Turn 1 failed: %v", err)
		}
		if tr.State != "" && tr.State != "submitted" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	sr2, err := c.Submit(ctx, soloReq("ambig-2"))
	if err != nil {
		t.Fatalf("Submit 2 failed: %v", err)
	}

	// Wait for second turn to complete
	for {
		tr, err := c.Turn(ctx, sr2.TurnID)
		if err != nil {
			t.Fatalf("Turn 2 failed: %v", err)
		}
		if tr.State != "" && tr.State != "submitted" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	id1 := sr1.ConversationID
	id2 := sr2.ConversationID

	// Find a common prefix
	commonLen := 0
	for i := 0; i < len(id1) && i < len(id2); i++ {
		if id1[i] == id2[i] {
			commonLen = i + 1
		} else {
			break
		}
	}

	if commonLen == len(id1) || commonLen == len(id2) {
		t.Skip("unable to create ambiguous prefix in this run")
	}

	ambiguousPrefix := id1[:commonLen]

	// Try to get conversation with ambiguous prefix - should return ambiguous error
	_, err = c.Conversation(ctx, ambiguousPrefix)
	if err == nil {
		t.Fatal("expected ambiguous error for prefix that matches both conversations")
	}

	var wireErr *wire.Error
	if !asWireError(err, &wireErr) || wireErr.Code != wire.ErrorCodeAmbiguous {
		t.Fatalf("expected ambiguous error, got: %v", err)
	}

	// Verify we can resolve both full IDs
	cr1, err := c.Conversation(ctx, id1)
	if err != nil {
		t.Fatalf("Conversation with full ID 1 failed: %v", err)
	}
	if cr1.ID != id1 {
		t.Fatalf("expected ID %s, got %s", id1, cr1.ID)
	}

	cr2, err := c.Conversation(ctx, id2)
	if err != nil {
		t.Fatalf("Conversation with full ID 2 failed: %v", err)
	}
	if cr2.ID != id2 {
		t.Fatalf("expected ID %s, got %s", id2, cr2.ID)
	}
}

func TestUnknownConversationPrefixNoSubmit(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Try to resolve unknown prefix before submitting
	_, err := c.ResolveConversationID(ctx, "unknown-prefix-xyz")
	if err == nil {
		t.Fatal("expected error for unknown conversation")
	}

	var wireErr *wire.Error
	if !asWireError(err, &wireErr) || wireErr.Code != wire.ErrorCodeNotFound {
		t.Fatalf("expected not_found error, got: %v", err)
	}

	// Now verify the client can resolve a known conversation
	sr, err := c.Submit(ctx, soloReq("known-conv"))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	// Wait for turn to complete
	for {
		tr, err := c.Turn(ctx, sr.TurnID)
		if err != nil {
			t.Fatalf("Turn failed: %v", err)
		}
		if tr.State != "" && tr.State != "submitted" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	resolved, err := c.ResolveConversationID(ctx, sr.ConversationID[:16])
	if err != nil {
		t.Fatalf("ResolveConversationID failed: %v", err)
	}

	if resolved != sr.ConversationID {
		t.Fatalf("expected resolved ID %s, got %s", sr.ConversationID, resolved)
	}
}

func TestMalformedConversationCursor(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create a conversation so the store isn't empty
	sr, err := c.Submit(ctx, soloReq("cursor-test"))
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	// Wait for turn to complete
	for {
		tr, err := c.Turn(ctx, sr.TurnID)
		if err != nil {
			t.Fatalf("Turn failed: %v", err)
		}
		if tr.State != "" && tr.State != "submitted" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Try with malformed cursor - should return bad_request
	_, err = c.Conversations(ctx, nil, "invalid-cursor-format", 10)
	if err == nil {
		t.Fatal("expected error for malformed cursor")
	}

	var wireErr *wire.Error
	if !asWireError(err, &wireErr) || wireErr.Code != wire.ErrorCodeBadRequest {
		t.Fatalf("expected bad_request error, got: %v", err)
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
