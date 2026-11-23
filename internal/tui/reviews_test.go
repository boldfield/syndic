package tui

import (
	"testing"

	"github.com/boldfield/syndic/internal/wire"
)

func TestReviewsDrawerOpen(t *testing.T) {
	d := NewReviewsDrawer()
	if d.IsOpen() {
		t.Error("drawer should start closed")
	}
	d.SetTurns([]string{"turn1", "turn2"})
	d.Open(0)
	if !d.IsOpen() {
		t.Error("drawer should be open after Open()")
	}
	if id := d.selectedTurnID(); id != "turn1" {
		t.Errorf("selected turn = %q, want turn1", id)
	}
}

func TestReviewsDrawerClose(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1", "turn2"})
	d.Open(0)
	d.Close()
	if d.IsOpen() {
		t.Error("drawer should be closed after Close()")
	}
}

func TestReviewsDrawerNavigation(t *testing.T) {
	d := NewReviewsDrawer()
	turns := []string{"turn1", "turn2", "turn3"}
	d.SetTurns(turns)
	d.Open(0)

	// Test down navigation
	d.SelectDown()
	if d.selectedTurn != 1 {
		t.Errorf("after SelectDown, selectedTurn = %d, want 1", d.selectedTurn)
	}

	d.SelectDown()
	if d.selectedTurn != 2 {
		t.Errorf("after SelectDown, selectedTurn = %d, want 2", d.selectedTurn)
	}

	// Should not go past the last turn
	d.SelectDown()
	if d.selectedTurn != 2 {
		t.Errorf("after SelectDown at end, selectedTurn = %d, want 2", d.selectedTurn)
	}

	// Test up navigation
	d.SelectUp()
	if d.selectedTurn != 1 {
		t.Errorf("after SelectUp, selectedTurn = %d, want 1", d.selectedTurn)
	}

	// Should not go before the first turn
	d.SelectUp()
	d.SelectUp()
	if d.selectedTurn != 0 {
		t.Errorf("after SelectUp at start, selectedTurn = %d, want 0", d.selectedTurn)
	}
}

func TestReviewsDrawerRoundNavigation(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.Open(0)

	// Add reviews with multiple rounds
	items := []wire.ReviewItem{
		{Round: 0, Participant: "p1", Kind: "approve"},
		{Round: 1, Participant: "p1", Kind: "block"},
		{Round: 2, Participant: "p1", Kind: "approve"},
	}
	d.UpdateReviews("turn1", items)

	rounds := d.getRounds()
	if len(rounds) != 3 {
		t.Fatalf("got %d rounds, want 3", len(rounds))
	}

	// Test round navigation
	d.SelectRoundRight()
	if d.selectedRound != 1 {
		t.Errorf("after SelectRoundRight, selectedRound = %d, want 1", d.selectedRound)
	}

	d.SelectRoundLeft()
	if d.selectedRound != 0 {
		t.Errorf("after SelectRoundLeft, selectedRound = %d, want 0", d.selectedRound)
	}
}

func TestReviewsDrawerUpdateReviews(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})

	items := []wire.ReviewItem{
		{Round: 1, Participant: "p1", Kind: "approve", Blocks: []wire.ReviewBlock{
			{BlockID: "b1", State: "withdrawn"},
		}},
		{Round: 1, Participant: "p2", Kind: "block", Blocks: []wire.ReviewBlock{
			{BlockID: "b2", State: "opened"},
		}},
	}

	d.UpdateReviews("turn1", items)

	rev := d.reviewsByTurn["turn1"]
	if rev == nil {
		t.Fatal("reviews not stored")
	}

	if len(rev.items) != 2 {
		t.Errorf("got %d items, want 2", len(rev.items))
	}

	rounds := d.getRounds()
	if len(rounds) != 1 || rounds[0] != 1 {
		t.Errorf("got rounds %v, want [1]", rounds)
	}
}

func TestReviewsDrawerScroll(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.Open(0)

	d.ScrollDown(2, 20)
	if d.scrollOffset != 2 {
		t.Errorf("after ScrollDown(2), scrollOffset = %d, want 2", d.scrollOffset)
	}

	d.ScrollUp(1)
	if d.scrollOffset != 1 {
		t.Errorf("after ScrollUp(1), scrollOffset = %d, want 1", d.scrollOffset)
	}

	d.ScrollUp(5)
	if d.scrollOffset != 0 {
		t.Errorf("after ScrollUp(5), scrollOffset = %d, want 0", d.scrollOffset)
	}
}

func TestReviewsDrawerRender(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1", "turn2"})

	// Test rendering when closed
	output := d.render(80, NewContentCache())
	if output != "" {
		t.Errorf("closed drawer should render empty, got %q", output)
	}

	d.Open(0)
	items := []wire.ReviewItem{
		{Round: 1, Participant: "p1", Kind: "approve", Blocks: []wire.ReviewBlock{
			{BlockID: "b1", State: "withdrawn"},
		}},
	}
	d.UpdateReviews("turn1", items)

	output = d.render(80, NewContentCache())
	if output == "" {
		t.Error("open drawer should not render empty")
	}

	if !stringContains(output, "Reviews") || !stringContains(output, "Turn 1") {
		t.Errorf("drawer output missing expected content: %q", output)
	}
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
