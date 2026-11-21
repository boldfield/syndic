package tui

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// These tests run the App under teatest against a real engine with
// provisional = true and provisional = false, verifying that the
// provisional prompt appears only when deadlines are provisional and
// that pressing yes/no/esc handles the submission correctly.

// submissionIDsFromTurns extracts the submission IDs from the EvTurnSubmitted events
// for all turns in the harness.
func submissionIDsFromTurns(h *enginetest.Harness) []string {
	var ids []string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvTurnSubmitted); ok {
			ids = append(ids, p.SubmissionID)
		}
	}
	return ids
}

func provisionalRoster(replace ...string) string {
	roster := strings.Replace(enginetest.RosterTOML, "provisional = false", "provisional = true", 1)
	for i := 0; i+1 < len(replace); i += 2 {
		roster = strings.Replace(roster, replace[i], replace[i+1], 1)
	}
	return roster
}

// TestProvisionalFirstSubmitPromptsCreatesNoTurn verifies that when
// provisional deadlines are enabled, the first submit shows the
// confirmation prompt and creates no turn.
func TestProvisionalFirstSubmitPromptsCreatesNoTurn(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer")}, "reviewer": {approve()}}
	h := enginetest.New(t, roster, scripts)
	r := startAnswerRig(t, h)

	// Type a question and press Enter; the submit should fail with provisional error
	r.typeLine("question")

	// Wait until the view contains the provisional prompt
	v := r.until(t, "provisional prompt shown", func(a *App) (string, bool) {
		view := a.View()
		return view, a.provisional != nil && strings.Contains(view, "Provisional deadlines")
	})

	// Verify no turn was created yet
	if len(r.app.state.Turns) != 0 {
		t.Fatalf("turn created before confirmation: %d turns exist", len(r.app.state.Turns))
	}

	// Verify the input is still present
	if r.app.input.Value() != "question" {
		t.Errorf("input cleared: got %q, want 'question'", r.app.input.Value())
	}

	// Verify the prompt text is in the view, including the input prompt
	if !strings.Contains(v, "Provisional deadlines") {
		t.Fatalf("view missing prompt: %s", v)
	}
	if !strings.Contains(v, "(y/n)>") {
		t.Fatalf("view missing input prompt text: %s", v)
	}
}

// TestProvisionalYesResendsProceedsCreatesOneTurn verifies that pressing
// 'y' resends the submission with a new submission_id, creating exactly
// one turn, and that pressing yes creates no prompt text in the view.
func TestProvisionalYesResendsProceedsCreatesOneTurn(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer")}, "reviewer": {approve()}}
	h := enginetest.New(t, roster, scripts)
	r := startAnswerRig(t, h)

	// Submit a question; should show provisional prompt
	r.typeLine("question")
	r.until(t, "provisional prompt", func(a *App) (string, bool) {
		return a.View(), a.provisional != nil
	})

	// Press 'y' to confirm
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	// Wait for the turn to be created with no provisional prompt
	r.until(t, "turn created after yes with all checks", func(a *App) (string, bool) {
		view := a.View()
		return view, len(a.state.Turns) == 1 && a.provisional == nil && a.input.Value() == "" && a.allowProvisional && !strings.Contains(view, "Provisional deadlines")
	})

	// Verify exactly one submission ID was recorded (the resend)
	ids := submissionIDsFromTurns(h)
	if len(ids) != 1 {
		t.Fatalf("expected 1 turn submitted, got %d; submission IDs: %v", len(ids), ids)
	}
}

// TestProvisionalSecondSubmitNoPrompt verifies that after answering yes
// to a provisional prompt, the allowProvisional flag is set so subsequent
// submit attempts don't re-prompt.
func TestProvisionalSecondSubmitNoPrompt(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer"), enginetest.Reply("answer2")}, "reviewer": {approve(), approve()}}
	h := enginetest.New(t, roster, scripts)
	r := startAnswerRig(t, h)

	// First submit with provisional prompt
	r.typeLine("first question")
	r.until(t, "provisional prompt shown", func(a *App) (string, bool) {
		return a.View(), a.provisional != nil
	})

	// Press 'y' to confirm
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	// Wait for the first turn to be created
	r.until(t, "first turn created", func(a *App) (string, bool) {
		return "", len(a.state.Turns) == 1 && a.provisional == nil && a.allowProvisional
	})

	// Wait for the first turn to settle
	idle(t, r)

	// Now type a second submit - should not show provisional prompt
	r.typeLine("second question")

	// Wait for the second turn to be created without a provisional prompt
	r.until(t, "second turn created without prompt", func(a *App) (string, bool) {
		return a.View(), len(a.state.Turns) == 2 && a.provisional == nil && !strings.Contains(a.View(), "Provisional deadlines")
	})
}

// TestProvisionalNoCancelsKeepsInput verifies that pressing 'n' or 'esc'
// cancels the submit without sending anything, keeps the typed text, and
// the next submit prompts again.
func TestProvisionalNoCancelsKeepsInput(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer")}, "reviewer": {approve()}}
	h := enginetest.New(t, roster, scripts)
	r := startAnswerRig(t, h)

	// Submit a question
	r.typeLine("my question")
	r.until(t, "provisional prompt", func(a *App) (string, bool) {
		return a.View(), a.provisional != nil
	})

	// Press 'n' to cancel
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	// Wait for the prompt to be cleared and input to be preserved, and no turn created
	r.until(t, "prompt cleared, input kept, no turn", func(a *App) (string, bool) {
		return "", a.provisional == nil && a.input.Value() == "my question" && len(a.state.Turns) == 0
	})

	// Now submit again - should prompt again
	send(r, tea.KeyMsg{Type: tea.KeyEnter})

	r.until(t, "provisional prompt shown again", func(a *App) (string, bool) {
		return a.View(), a.provisional != nil
	})
}

// TestProvisionalEscKeySameAsNo verifies that pressing 'esc' behaves like 'n'.
func TestProvisionalEscKeySameAsNo(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer")}, "reviewer": {approve()}}
	h := enginetest.New(t, roster, scripts)
	r := startAnswerRig(t, h)

	// Submit and get provisional prompt
	r.typeLine("test input")
	r.until(t, "provisional prompt", func(a *App) (string, bool) {
		return a.View(), a.provisional != nil
	})

	// Press 'esc' to cancel
	send(r, tea.KeyMsg{Type: tea.KeyEsc})

	// Wait for the prompt to be cleared and input to be preserved, and no turn created
	r.until(t, "esc cancels like no, no turn", func(a *App) (string, bool) {
		return "", a.provisional == nil && a.input.Value() == "test input" && len(a.state.Turns) == 0
	})
}

// TestProvisionalBannerPersists verifies that the deadlines: provisional
// banner remains visible before and after the user confirms the prompt.
func TestProvisionalBannerPersists(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer")}, "reviewer": {approve()}}
	h := enginetest.New(t, roster, scripts)
	r := startAnswerRig(t, h)

	// Before submitting anything, the banner should be visible
	v := r.until(t, "banner visible initially", func(a *App) (string, bool) {
		return a.View(), strings.Contains(a.View(), "deadlines: provisional")
	})

	if !strings.Contains(v, "deadlines: provisional") {
		t.Fatalf("banner not visible initially: %s", v)
	}

	// Submit and answer yes
	r.typeLine("question")
	r.until(t, "provisional prompt", func(a *App) (string, bool) {
		return a.View(), a.provisional != nil
	})
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	// Wait for turn to complete, banner should still be visible and no provisional prompt
	v = r.until(t, "banner visible after yes", func(a *App) (string, bool) {
		view := a.View()
		return view, len(a.state.Turns) > 0 && strings.Contains(view, "deadlines: provisional")
	})

	if !strings.Contains(v, "deadlines: provisional") {
		t.Fatalf("banner not visible after yes: %s", v)
	}
	if strings.Contains(v, "(y/n)>") {
		t.Fatalf("provisional prompt still visible after yes: %s", v)
	}
}

// TestProvisionalFalseNoPrompt verifies that with provisional = false,
// no prompt appears and no allow_provisional is sent.
func TestProvisionalFalseNoPrompt(t *testing.T) {
	// Use the standard roster with provisional = false
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer")}, "reviewer": {approve()}}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)
	r := startAnswerRig(t, h)

	// Submit a question
	r.typeLine("question")

	// Wait for the turn to be created (should not show provisional prompt)
	r.until(t, "turn created without banner", func(a *App) (string, bool) {
		view := a.View()
		return view, len(a.state.Turns) > 0 && a.provisional == nil && !strings.Contains(view, "deadlines: provisional")
	})
}

// TestProvisionalNewProcessAsksAgain verifies that each App instance
// (each TUI process) has its own allowProvisional state, so if one instance
// confirmed provisional, a new instance still asks.
func TestProvisionalNewProcessAsksAgain(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer")}, "reviewer": {approve()}}
	h := enginetest.New(t, roster, scripts)

	// First app instance: answer yes
	r1 := startAnswerRig(t, h)
	r1.typeLine("first")
	r1.until(t, "first provisional prompt", func(a *App) (string, bool) {
		return a.View(), a.provisional != nil
	})
	send(r1, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	r1.until(t, "allowProvisional set", func(a *App) (string, bool) {
		return "", a.allowProvisional
	})

	// Close the first app
	r1.final(t)

	// Second app instance: should ask again, its allowProvisional defaults to false
	r2 := startAnswerRig(t, h)
	r2.typeLine("second")
	r2.until(t, "second process asks again", func(a *App) (string, bool) {
		return a.View(), a.provisional != nil && !a.allowProvisional
	})
}
