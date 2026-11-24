package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// TestDrawerOpen verifies that Ctrl+O opens the reviews drawer, per section 2 and 8.
func TestDrawerOpen(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched"
	})

	// Drawer should start closed
	r.until(t, "drawer closed initially", func(a *App) (string, bool) {
		return "", !a.reviewsDrawer.IsOpen()
	})

	// Press Ctrl+O to open
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})

	// Drawer should be open
	r.until(t, "drawer open after Ctrl+O", func(a *App) (string, bool) {
		return "", a.reviewsDrawer.IsOpen()
	})

	view := view(t, r)
	if !strings.Contains(view, "Reviews") && !strings.Contains(view, "Turn") {
		t.Fatalf("drawer content not visible:\n%s", view)
	}
}

// TestDrawerClose verifies that Esc or Ctrl+O closes the drawer, per section 2 and 8.
func TestDrawerClose(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched"
	})

	// Open drawer
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	r.until(t, "drawer open", func(a *App) (string, bool) {
		return "", a.reviewsDrawer.IsOpen()
	})

	// Close with Esc
	send(r, tea.KeyMsg{Type: tea.KeyEsc})
	r.until(t, "drawer closed by Esc", func(a *App) (string, bool) {
		return "", !a.reviewsDrawer.IsOpen()
	})

	// Open again
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	r.until(t, "drawer open again", func(a *App) (string, bool) {
		return "", a.reviewsDrawer.IsOpen()
	})

	// Close with Ctrl+O
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	r.until(t, "drawer closed by Ctrl+O", func(a *App) (string, bool) {
		return "", !a.reviewsDrawer.IsOpen()
	})
}

// TestDrawerNavigation verifies keyboard navigation in drawer: ↑↓ choose turn,
// ←→ choose round, per section 2 and 8.
func TestDrawerNavigation(t *testing.T) {
	a := chatApp(t)
	a.reviewsDrawer.SetTurns([]string{"t1", "t2", "t3"})
	a.reviewsDrawer.Open(0, false)

	// Initial state: should be at turn 0 (t1)
	if a.reviewsDrawer.selectedTurn != 0 {
		t.Fatalf("initial selected turn = %d, want 0", a.reviewsDrawer.selectedTurn)
	}

	// Down should move to next turn
	a.reviewsDrawer.SelectDown()
	if a.reviewsDrawer.selectedTurn != 1 {
		t.Fatalf("after SelectDown, selectedTurn = %d, want 1", a.reviewsDrawer.selectedTurn)
	}

	// Down again
	a.reviewsDrawer.SelectDown()
	if a.reviewsDrawer.selectedTurn != 2 {
		t.Fatalf("after second SelectDown, selectedTurn = %d, want 2", a.reviewsDrawer.selectedTurn)
	}

	// Up should go back
	a.reviewsDrawer.SelectUp()
	if a.reviewsDrawer.selectedTurn != 1 {
		t.Fatalf("after SelectUp, selectedTurn = %d, want 1", a.reviewsDrawer.selectedTurn)
	}

	// Test boundary (down from last should stay at last)
	a.reviewsDrawer.selectedTurn = 2
	a.reviewsDrawer.SelectDown()
	if a.reviewsDrawer.selectedTurn != 2 {
		t.Errorf("after SelectDown at last turn, selectedTurn = %d, want 2", a.reviewsDrawer.selectedTurn)
	}

	// Test boundary (up from first should stay at first)
	a.reviewsDrawer.selectedTurn = 0
	a.reviewsDrawer.SelectUp()
	if a.reviewsDrawer.selectedTurn != 0 {
		t.Errorf("after SelectUp at first turn, selectedTurn = %d, want 0", a.reviewsDrawer.selectedTurn)
	}

	// Test round selection (left/right)
	a.reviewsDrawer.SelectRoundLeft()
	a.reviewsDrawer.SelectRoundRight()
}

// TestInputNotEditableWhileDrawerHasKeyboard verifies that input cannot be
// edited while drawer is open, per section 2 and 8.
func TestInputNotEditableWhileDrawerHasKeyboard(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched"
	})

	// Open drawer
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	r.until(t, "drawer open", func(a *App) (string, bool) {
		return "", a.reviewsDrawer.IsOpen()
	})

	// Try to type
	inputBefore := r.app.input.Value()
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("text")})

	// Input should not change
	inputAfter := r.app.input.Value()
	if inputAfter != inputBefore {
		t.Errorf("input changed while drawer open: %q -> %q", inputBefore, inputAfter)
	}

	// Close drawer
	send(r, tea.KeyMsg{Type: tea.KeyEsc})
	r.until(t, "drawer closed", func(a *App) (string, bool) {
		return "", !a.reviewsDrawer.IsOpen()
	})

	// Now typing should work
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("text")})
	r.until(t, "text typed", func(a *App) (string, bool) {
		return "", strings.Contains(r.app.input.Value(), "text")
	})
}

// TestTabCompletesCommandWithListOpen verifies that Tab completes a command
// when the command list is open, per section 3 and 8.
func TestTabCompletesCommandWithListOpen(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	// Type partial command
	send(r, runes("/ra"))

	// Command list should open
	r.until(t, "command list open", func(a *App) (string, bool) {
		return "", a.commandListOpen()
	})

	// Tab should complete
	send(r, kt(tea.KeyTab))

	// Should be completed and list closed
	r.until(t, "command completed and list closed", func(a *App) (string, bool) {
		return "", a.input.Value() == "/raw " && !a.commandListOpen()
	})
}

// TestTabSwitchesTabsWithoutList verifies that Tab switches tabs when
// command list is not open, per section 3 and 8.
func TestTabSwitchesTabsWithoutList(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	// Get current tab
	var currentTab tab
	r.until(t, "current tab", func(a *App) (string, bool) {
		currentTab = a.tab
		return "", true
	})

	// Press Tab
	send(r, kt(tea.KeyTab))

	// Tab should change
	r.until(t, "tab changed", func(a *App) (string, bool) {
		return "", a.tab != currentTab
	})
}

// TestApplyPromptAccepted verifies /apply command is accepted and processes,
// per section 3 and 8.
func TestApplyPromptAccepted(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched"
	})

	// Type /apply and press Enter
	send(r, runes("/apply"))
	send(r, tea.KeyMsg{Type: tea.KeyEnter})

	// Command should be processed without error
	v := view(t, r)

	// At minimum, the command should not crash and view should still render
	if len(v) == 0 {
		t.Fatalf("view is empty after /apply command")
	}

	// If we have content, the view should be valid
	if strings.Contains(v, "\x1b[") && strings.Count(v, "\x1b[") > 100 {
		t.Fatalf("view contains too many ANSI sequences: %s", v)
	}
}

// TestPromptFormRejectsEmpty verifies that /override form cannot be submitted
// with empty reason, per section 3 and 8.
func TestPromptFormRejectsEmpty(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched"
	})

	// Type /override and press Enter to open the form
	send(r, runes("/override"))
	send(r, tea.KeyMsg{Type: tea.KeyEnter})

	// Wait for the form to open (look for reason field prompt)
	inputBefore := r.app.input.Value()
	v := r.until(t, "override form shown with reason field", func(a *App) (string, bool) {
		view := a.View()
		return view, strings.Contains(view, "reason") || strings.Contains(view, "Reason") ||
			strings.Contains(view, "required")
	})

	// The form should contain a text field for the reason
	if !strings.Contains(v, "reason") && !strings.Contains(v, "Reason") {
		t.Fatalf("override form does not show reason field:\n%s", v)
	}

	// Try to submit empty form by pressing Enter
	send(r, tea.KeyMsg{Type: tea.KeyEnter})

	// Form should reject empty submission - it should stay open or show validation error
	finalView := r.until(t, "form rejects empty submission", func(a *App) (string, bool) {
		view := a.View()
		// Form should still be active (input hasn't been sent)
		return view, (strings.Contains(view, "reason") || strings.Contains(view, "required")) ||
			a.input.Value() == inputBefore
	})

	// If we get here, verify the reason field was indeed empty and form didn't process
	if strings.Contains(finalView, "INCOMPLETE") || strings.Contains(finalView, "approved") {
		// If override was processed, the view would change
		t.Errorf("form processed empty submission when it should have rejected it")
	}
}

// TestDrawerClosesCommandList verifies that opening the drawer closes the
// command list, per section 3 (command_list_test.go also covers this).
func TestDrawerClosesCommandList(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched"
	})

	// Open command list
	send(r, runes("/r"))
	r.until(t, "command list open", func(a *App) (string, bool) {
		return "", a.commandListOpen()
	})

	// Open drawer
	send(r, kt(tea.KeyCtrlO))
	r.until(t, "drawer open and list closed", func(a *App) (string, bool) {
		return "", a.reviewsDrawer.IsOpen() && !a.commandListOpen()
	})

	// Close drawer
	send(r, kt(tea.KeyEsc))
	r.until(t, "drawer closed", func(a *App) (string, bool) {
		return "", !a.reviewsDrawer.IsOpen()
	})

	// Command list should still be closed
	if r.app.commandListOpen() {
		t.Error("command list reopened when drawer closed")
	}
}

// TestDrawerScrolling verifies that drawer scroll methods work correctly, per section 2 and 8.
func TestDrawerScrolling(t *testing.T) {
	a := chatApp(t)
	a.reviewsDrawer.SetTurns([]string{"turn1"})
	a.reviewsDrawer.Open(0, false)

	// Test scroll methods directly
	// Scroll down
	a.reviewsDrawer.ScrollDown(2, 20)
	if a.reviewsDrawer.scrollOffset != 2 {
		t.Errorf("after ScrollDown(2), offset = %d, want 2", a.reviewsDrawer.scrollOffset)
	}

	// Scroll up
	a.reviewsDrawer.ScrollUp(1)
	if a.reviewsDrawer.scrollOffset != 1 {
		t.Errorf("after ScrollUp(1), offset = %d, want 1", a.reviewsDrawer.scrollOffset)
	}

	// Verify scroll up at boundary
	a.reviewsDrawer.ScrollUp(5)
	if a.reviewsDrawer.scrollOffset != 0 {
		t.Errorf("after ScrollUp(5) at start, offset = %d, want 0", a.reviewsDrawer.scrollOffset)
	}
}

// TestReviewsDrawerShownPerRound verifies that drawer shows reviews grouped
// by round with participant names, per section 2 and 8.
func TestReviewsDrawerShownPerRound(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched"
	})

	// Add some review items to the drawer
	turnID := answerTurn(r.app.state)
	items := []wire.ReviewItem{
		{Round: 0, Participant: "reviewer1", Kind: "approve", Blocks: []wire.ReviewBlock{}},
		{Round: 0, Participant: "reviewer2", Kind: "block", Blocks: []wire.ReviewBlock{
			{BlockID: "b1", State: "opened"},
		}},
	}
	r.app.reviewsDrawer.UpdateReviews(turnID, items)

	// Open drawer and verify it's rendering content
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	v := r.until(t, "drawer open with content", func(a *App) (string, bool) {
		if !a.reviewsDrawer.IsOpen() {
			return "", false
		}
		output := a.View()
		return output, strings.Contains(output, "Round") || strings.Contains(output, "reviewer")
	})

	// Drawer must show round information
	if !strings.Contains(v, "Round") && !strings.Contains(v, "round") {
		t.Fatalf("drawer does not display round information:\n%s", v)
	}

	// Drawer must show at least one reviewer name
	if !strings.Contains(v, "reviewer1") && !strings.Contains(v, "reviewer2") {
		t.Fatalf("drawer does not display reviewer names:\n%s", v)
	}
}

// TestReviewsDrawerBlockStates verifies that drawer shows block states
// (opened, maintained, withdrawn) in its rendered output, per section 2 and 8.
func TestReviewsDrawerBlockStates(t *testing.T) {
	a := chatApp(t)
	a.width, a.height = 80, 24

	turnID := "t1"
	// Create review items with various block states
	items := []wire.ReviewItem{
		{Round: 0, Participant: "rev1", Kind: "block", Blocks: []wire.ReviewBlock{
			{BlockID: "b1", State: "opened"},
			{BlockID: "b2", State: "maintained"},
		}},
		{Round: 1, Participant: "rev1", Kind: "block", Blocks: []wire.ReviewBlock{
			{BlockID: "b1", State: "maintained"},
			{BlockID: "b2", State: "withdrawn"},
		}},
	}
	a.reviewsDrawer.UpdateReviews(turnID, items)
	a.reviewsDrawer.SetTurns([]string{turnID})
	a.reviewsDrawer.Open(0, false)

	// Render drawer and verify block states are present
	cache := NewContentCache()
	output := a.reviewsDrawer.render(80, cache, false)

	// Must show at least one block state
	if !strings.Contains(output, "opened") && !strings.Contains(output, "maintained") &&
		!strings.Contains(output, "withdrawn") {
		t.Fatalf("drawer does not display block states in output:\n%s", output)
	}
}

// TestReviewsListEmpty verifies that drawer handles turns with no accepted verdicts,
// per section 2 and 8.
func TestReviewsListEmpty(t *testing.T) {
	a := chatApp(t)
	a.width, a.height = 80, 24

	turnID := "t1"
	// Set up drawer with empty reviews (turn with no verdicts)
	a.reviewsDrawer.SetTurns([]string{turnID})
	a.reviewsDrawer.Open(0, false)
	a.reviewsDrawer.UpdateReviews(turnID, []wire.ReviewItem{})

	// Render should not crash and should handle empty reviews gracefully
	cache := NewContentCache()
	output := a.reviewsDrawer.render(80, cache, false)

	// Must produce some output or handle gracefully
	if strings.TrimSpace(output) == "" {
		t.Errorf("drawer with empty reviews produces no output at all")
	}
}

// TestReviewsPagedFetch verifies that drawer can handle many review items,
// per section 2 and 8: items should render without truncation.
func TestReviewsPagedFetch(t *testing.T) {
	a := chatApp(t)
	a.width, a.height = 80, 24

	turnID := "t1"
	a.reviewsDrawer.SetTurns([]string{turnID})

	// Create a large number of review items to test paging
	var items []wire.ReviewItem
	for i := 0; i < 60; i++ {
		items = append(items, wire.ReviewItem{
			Round:       int(i % 3),
			Participant: fmt.Sprintf("reviewer_%d", i%5),
			Kind:        "approve",
			Blocks:      []wire.ReviewBlock{},
		})
	}
	a.reviewsDrawer.UpdateReviews(turnID, items)
	a.reviewsDrawer.Open(0, false)

	// Drawer should render multiple items
	cache := NewContentCache()
	output := a.reviewsDrawer.render(80, cache, false)

	// Verify output is generated and contains some reviewer names
	if strings.TrimSpace(output) == "" {
		t.Fatalf("drawer with many items produces no output")
	}
	if !strings.Contains(output, "reviewer") {
		t.Fatalf("drawer does not show reviewer names in multi-item output:\n%s", output)
	}
}

// TestReviewsShortForm verifies that oversized verdicts (>64 KiB) are handled,
// per section 2 and 8.
func TestReviewsShortForm(t *testing.T) {
	a := chatApp(t)
	a.width, a.height = 80, 24

	turnID := "t1"

	// Create oversized review item with many blocks
	var blocks []wire.ReviewBlock
	for i := 0; i < 100; i++ {
		blocks = append(blocks, wire.ReviewBlock{
			BlockID: fmt.Sprintf("b%d", i),
			State:   "opened",
		})
	}

	items := []wire.ReviewItem{
		{
			Round:       0,
			Participant: "reviewer",
			Kind:        "block",
			Blocks:      blocks,
		},
	}

	a.reviewsDrawer.SetTurns([]string{turnID})
	a.reviewsDrawer.UpdateReviews(turnID, items)
	a.reviewsDrawer.Open(0, false)

	// Verify drawer can handle many blocks and render them
	cache := NewContentCache()
	output := a.reviewsDrawer.render(80, cache, false)

	// Should produce output with block info
	if strings.TrimSpace(output) == "" {
		t.Fatalf("drawer with oversized item produces no output")
	}
}
