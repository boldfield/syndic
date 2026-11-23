package server_test

import (
	"encoding/json"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/wire"
)

func TestReviewsRequest(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Submit a review turn
	release := h.Hold("reviewer")
	reply, err := c.Submit(ctx, reviewReq("reviews-1"))
	if err != nil {
		t.Fatal(err)
	}

	// Release all holds so turn can complete
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateApproved {
		t.Fatalf("terminal state = %q, want %q", row.TerminalState, engine.StateApproved)
	}

	// Request reviews for the turn
	reviews, err := c.Reviews(ctx, reply.TurnID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if reviews == nil || len(reviews.Items) == 0 {
		t.Fatalf("reviews = %+v, want items", reviews)
	}
	items, err := reviews.DecodeItems()
	if err != nil {
		t.Fatal(err)
	}

	// Verify structure
	for i, item := range items {
		if item.Kind == "" {
			t.Fatalf("item %d has no kind", i)
		}
		if item.Participant == "" {
			t.Fatalf("item %d has no participant", i)
		}
		if item.AttemptID == "" {
			t.Fatalf("item %d has no attempt_id", i)
		}
		if !item.Oversized && item.Commentary == nil {
			t.Fatalf("item %d is not oversized but has no commentary", i)
		}
	}
}

func TestReviewsEmptyForSoloTurn(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Submit a solo turn
	release := h.Hold("author")
	reply, err := c.Submit(ctx, soloReq("solo-1"))
	if err != nil {
		t.Fatal(err)
	}

	// Release hold
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateSoloComplete {
		t.Fatalf("terminal state = %q, want %q", row.TerminalState, engine.StateSoloComplete)
	}

	// Request reviews - should be empty
	reviews, err := c.Reviews(ctx, reply.TurnID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(reviews.Items) != 0 {
		t.Fatalf("solo turn reviews = %d items, want 0", len(reviews.Items))
	}
	if reviews.NextCursor != nil {
		t.Fatal("solo turn reviews has next_cursor, want nil")
	}
}

func TestReviewsMissingTurnID(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Request reviews for non-existent turn
	_, err := c.Reviews(ctx, "not-a-turn-id", nil, nil)
	wantCode(t, err, wire.ErrorCodeNotFound)
}

func TestReviewsWithLimit(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Submit a review turn
	release := h.Hold("reviewer")
	reply, err := c.Submit(ctx, reviewReq("reviews-limit"))
	if err != nil {
		t.Fatal(err)
	}

	// Release all holds
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateApproved {
		t.Fatalf("terminal state = %q, want %q", row.TerminalState, engine.StateApproved)
	}

	// Request with limit
	limit := 1
	reviews, err := c.Reviews(ctx, reply.TurnID, nil, &limit)
	if err != nil {
		t.Fatal(err)
	}

	if len(reviews.Items) != 1 {
		t.Fatalf("reviews with limit 1 = %d items, want 1", len(reviews.Items))
	}
}

func TestConversationIncludesReviewCount(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Submit a review turn
	release := h.Hold("reviewer")
	reply, err := c.Submit(ctx, reviewReq("review-count-1"))
	if err != nil {
		t.Fatal(err)
	}

	// Release all holds
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateApproved {
		t.Fatalf("terminal state = %q, want %q", row.TerminalState, engine.StateApproved)
	}

	// Get conversation and check review_count
	conv, err := c.Conversation(ctx, reply.ConversationID[:8])
	if err != nil {
		t.Fatal(err)
	}

	if len(conv.Turns) != 1 {
		t.Fatalf("conversation has %d turns, want 1", len(conv.Turns))
	}

	turn := conv.Turns[0]
	if turn.ReviewCount == nil {
		t.Fatal("turn has nil ReviewCount, want a value")
	}
	if *turn.ReviewCount == 0 {
		t.Fatal("turn ReviewCount is 0, want > 0")
	}
}

func TestConversationSoloTurnNoReviewCount(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Submit a solo turn
	release := h.Hold("author")
	reply, err := c.Submit(ctx, soloReq("solo-count"))
	if err != nil {
		t.Fatal(err)
	}

	// Release hold
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateSoloComplete {
		t.Fatalf("terminal state = %q, want %q", row.TerminalState, engine.StateSoloComplete)
	}

	// Get conversation and check review_count
	conv, err := c.Conversation(ctx, reply.ConversationID[:8])
	if err != nil {
		t.Fatal(err)
	}

	if len(conv.Turns) != 1 {
		t.Fatalf("conversation has %d turns, want 1", len(conv.Turns))
	}

	turn := conv.Turns[0]
	if turn.ReviewCount != nil {
		t.Fatalf("solo turn has ReviewCount %d, want nil", *turn.ReviewCount)
	}
}

func TestReviewsDeterministicPaging(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Submit a review turn
	release := h.Hold("reviewer")
	reply, err := c.Submit(ctx, reviewReq("paging-1"))
	if err != nil {
		t.Fatal(err)
	}

	// Release all holds so turn can complete
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateApproved {
		t.Fatalf("terminal state = %q, want %q", row.TerminalState, engine.StateApproved)
	}

	// Page through reviews with limit=1
	var allItems []json.RawMessage
	var cursor *string
	for {
		limit := 1
		reviews, err := c.Reviews(ctx, reply.TurnID, cursor, &limit)
		if err != nil {
			t.Fatal(err)
		}

		if len(reviews.Items) == 0 {
			break
		}
		allItems = append(allItems, reviews.Items...)
		if reviews.NextCursor == nil {
			break
		}
		cursor = reviews.NextCursor
	}

	if len(allItems) == 0 {
		t.Fatalf("expected at least one review, got %d", len(allItems))
	}

	// Get all reviews at once
	reviews, err := c.Reviews(ctx, reply.TurnID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(reviews.Items) != len(allItems) {
		t.Fatalf("paged %d items, got %d all at once", len(allItems), len(reviews.Items))
	}

	if reviews.NextCursor != nil {
		t.Fatalf("full page has cursor, want nil")
	}
}

func TestReviewsLimitExceeded(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Submit a review turn
	release := h.Hold("reviewer")
	reply, err := c.Submit(ctx, reviewReq("limit-exceeded"))
	if err != nil {
		t.Fatal(err)
	}

	// Release all holds
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateApproved {
		t.Fatalf("terminal state = %q, want %q", row.TerminalState, engine.StateApproved)
	}

	// Request with limit > 50
	limit := 100
	reviews, err := c.Reviews(ctx, reply.TurnID, nil, &limit)
	if err != nil {
		t.Fatal(err)
	}

	// Should be clamped to max
	if len(reviews.Items) > 50 {
		t.Fatalf("got %d items, expected at most 50", len(reviews.Items))
	}
}

func TestReviewsBadCursor(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Submit a review turn
	release := h.Hold("reviewer")
	reply, err := c.Submit(ctx, reviewReq("bad-cursor"))
	if err != nil {
		t.Fatal(err)
	}

	// Release all holds
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateApproved {
		t.Fatalf("terminal state = %q, want %q", row.TerminalState, engine.StateApproved)
	}

	// Request with bad cursor
	badCursor := "not-a-real-cursor"
	_, err = c.Reviews(ctx, reply.TurnID, &badCursor, nil)
	wantCode(t, err, wire.ErrorCodeBadRequest)
}

func TestReviewsNegativeLimit(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// Submit a review turn
	release := h.Hold("reviewer")
	reply, err := c.Submit(ctx, reviewReq("neg-limit"))
	if err != nil {
		t.Fatal(err)
	}

	// Release all holds
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateApproved {
		t.Fatalf("terminal state = %q, want %q", row.TerminalState, engine.StateApproved)
	}

	// Request with negative limit
	negLimit := -1
	_, err = c.Reviews(ctx, reply.TurnID, nil, &negLimit)
	wantCode(t, err, wire.ErrorCodeBadRequest)
}
