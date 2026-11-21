package tui

import (
	"context"
	"strings"
	"sync"
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

// recordRequests captures every submit request the App sends until the test ends.
func recordRequests(t *testing.T) func() []wire.SubmitRequest {
	t.Helper()
	var mu sync.Mutex
	var reqs []wire.SubmitRequest
	onSubmitRequest = func(r wire.SubmitRequest) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, r)
	}
	t.Cleanup(func() { onSubmitRequest = nil })
	return func() []wire.SubmitRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]wire.SubmitRequest(nil), reqs...)
	}
}

func allowsProvisional(r wire.SubmitRequest) bool {
	return r.AllowProvisional != nil && *r.AllowProvisional
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
	reqs := recordRequests(t)
	r := startAnswerRig(t, h)

	// Type a question and press Enter; the submit should fail with provisional error
	r.typeLine("question")

	// Wait until the view contains the provisional prompt
	// Turns and input are read on the event loop, not from this goroutine.
	var turns int
	var input string
	v := r.until(t, "provisional prompt shown", func(a *App) (string, bool) {
		view := a.View()
		turns, input = len(a.state.Turns), a.input.Value()
		return view, a.provisional != nil && strings.Contains(view, "Provisional deadlines")
	})

	// Verify no turn was created yet
	if turns != 0 {
		t.Fatalf("turn created before confirmation: %d turns exist", turns)
	}

	if got := reqs(); len(got) != 1 || got[0].AllowProvisional != nil {
		t.Fatalf("first submit sent %+v, want one request without allow_provisional", got)
	}

	// Verify the input is still present
	if input != "question" {
		t.Errorf("input cleared: got %q, want 'question'", input)
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
	reqs := recordRequests(t)
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

	// Let the turn settle, then check a double send did not create a second turn.
	idle(t, r)
	ids := submissionIDsFromTurns(h)
	if len(ids) != 1 {
		t.Fatalf("expected 1 turn submitted, got %d; submission IDs: %v", len(ids), ids)
	}
	got := reqs()
	if len(got) != 2 {
		t.Fatalf("expected 2 requests (refused, resend), got %+v", got)
	}
	if got[0].AllowProvisional != nil {
		t.Errorf("refused request carried allow_provisional: %+v", got[0])
	}
	if !allowsProvisional(got[1]) {
		t.Errorf("resend lacks allow_provisional true: %+v", got[1])
	}
	if got[0].SubmissionID == got[1].SubmissionID {
		t.Errorf("resend reused submission_id %q", got[1].SubmissionID)
	}
	if got[1].SubmissionID != ids[0] {
		t.Errorf("turn was created by %q, want the resend %q", ids[0], got[1].SubmissionID)
	}
}

// TestProvisionalSecondSubmitNoPrompt verifies that after answering yes
// to a provisional prompt, a second submit creates a second turn without a
// prompt, and that its request carries allow_provisional: true.
func TestProvisionalSecondSubmitNoPrompt(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer"), enginetest.Reply("answer2")}, "reviewer": {approve(), approve()}}
	h := enginetest.New(t, roster, scripts)
	reqs := recordRequests(t)
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
	idle(t, r)
	if n := len(submissionIDsFromTurns(h)); n != 2 {
		t.Fatalf("expected 2 turns, got %d", n)
	}
	got := reqs()
	if len(got) != 3 || !allowsProvisional(got[2]) {
		t.Fatalf("want 3 requests with the last allowing provisional, got %+v", got)
	}
}

// TestProvisionalNoCancelsKeepsInput verifies that pressing 'n' or 'esc'
// cancels the submit without sending anything, keeps the typed text, and
// the next submit prompts again.
func TestProvisionalNoCancelsKeepsInput(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer")}, "reviewer": {approve()}}
	h := enginetest.New(t, roster, scripts)
	reqs := recordRequests(t)
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

	if got := reqs(); len(got) != 1 {
		t.Fatalf("a request was sent after n: %+v", got)
	}

	// Now submit again - should prompt again
	send(r, tea.KeyMsg{Type: tea.KeyEnter})

	r.until(t, "provisional prompt shown again", func(a *App) (string, bool) {
		return a.View(), a.provisional != nil
	})
	if got := reqs(); len(got) != 2 || got[1].AllowProvisional != nil {
		t.Fatalf("second submit sent %+v, want a plain request", got)
	}
}

// TestProvisionalEscKeySameAsNo verifies that pressing 'esc' behaves like 'n'.
func TestProvisionalEscKeySameAsNo(t *testing.T) {
	roster := provisionalRoster()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply("answer")}, "reviewer": {approve()}}
	h := enginetest.New(t, roster, scripts)
	reqs := recordRequests(t)
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
	if got := reqs(); len(got) != 1 {
		t.Fatalf("a request was sent after esc: %+v", got)
	}
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
	reqs := recordRequests(t)
	r := startAnswerRig(t, h)

	// Submit a question
	r.typeLine("question")

	// Wait for the turn to be created (should not show provisional prompt)
	r.until(t, "turn created without banner", func(a *App) (string, bool) {
		view := a.View()
		return view, len(a.state.Turns) > 0 && a.provisional == nil && !strings.Contains(view, "deadlines: provisional")
	})
	got := reqs()
	if len(got) == 0 {
		t.Fatal("no request recorded")
	}
	for _, q := range got {
		if q.AllowProvisional != nil {
			t.Errorf("request carries allow_provisional with provisional = false: %+v", q)
		}
	}
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
	r1.until(t, "first turn created and settled", func(a *App) (string, bool) {
		if len(a.state.Turns) != 1 {
			return "", false
		}
		for id := range a.state.Turns {
			if !isTerminalState(a.state.TurnState[id]) {
				return "", false
			}
		}
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

func pressAppKey(a *App, k string) {
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
}

// A quick profile approval and the provisional question share one yes/no
// prompt: whichever opens first keeps the keyboard, the other waits, and
// answering one never clears the other's panel or the typed input.
func TestProvisionalAndApprovalNeverOpenTogether(t *testing.T) {
	_, project := quickEnv(t)
	writeQuickFile(t, project, `profile = "fast"`)
	newApp := func() *App {
		a := NewApp(context.Background(), "unused.sock", WithWorkDir(project))
		a.state.Status = &wire.StatusReply{
			RosterPath:    "/etc/roster.toml",
			QuickProfiles: []wire.QuickProfile{{Name: "fast", Preset: "solo", DefinitionSHA256: "d1"}},
		}
		return a
	}

	// Provisional first: a status refresh does not open the approval over it.
	a := newApp()
	a.input.SetValue("my question")
	a.lastSubmitInput = "my question"
	a.showProvisionalPrompt(a.state.Status)
	a.updateQuickProfileState()
	v := a.View()
	if a.approval != nil || !strings.Contains(v, "Provisional deadlines") || strings.Contains(v, approvalPromptText) || !strings.Contains(v, "(y/n)> ") {
		t.Fatalf("approval opened over the provisional prompt:\n%s", v)
	}
	pressAppKey(a, "n")
	if a.input.Value() != "my question" {
		t.Errorf("input lost: %q", a.input.Value())
	}
	if v := a.View(); !strings.Contains(v, approvalPromptText) || strings.Contains(v, "Provisional deadlines") {
		t.Fatalf("approval did not open after the provisional answer:\n%s", v)
	}
	pressAppKey(a, "n")
	if a.yesNo != nil || a.approval != nil {
		t.Fatalf("prompts left open: %+v %+v", a.yesNo, a.approval)
	}
	if a.input.Value() != "my question" {
		t.Errorf("input lost after the queued approval was declined: %q", a.input.Value())
	}

	// Approval first: the provisional refusal does not replace its panel, and keeps the input.
	a = newApp()
	a.updateQuickProfileState()
	a.input.SetValue("my question")
	a.showProvisionalPrompt(a.state.Status)
	v = a.View()
	if a.provisional != nil || !strings.Contains(v, approvalPromptText) || strings.Contains(v, "Provisional deadlines") {
		t.Fatalf("provisional prompt replaced the approval:\n%s", v)
	}
	if !strings.Contains(v, "answer the open prompt") {
		t.Errorf("no refusal shown:\n%s", v)
	}
	pressAppKey(a, "y")
	if a.yesNo != nil || a.provisional != nil {
		t.Fatalf("prompt left open after y: %+v", a.yesNo)
	}
	if a.input.Value() != "my question" {
		t.Errorf("input lost after approval: %q", a.input.Value())
	}

	// Queued approval answered yes (last: it saves the approval to the project file).
	_, project = quickEnv(t)
	writeQuickFile(t, project, `profile = "fast"`)
	a = newApp()
	a.input.SetValue("my question")
	a.showProvisionalPrompt(a.state.Status)
	a.updateQuickProfileState()
	pressAppKey(a, "n")
	pressAppKey(a, "y")
	if a.yesNo != nil || a.approval != nil || a.input.Value() != "my question" {
		t.Fatalf("after approval yes: prompts %+v %+v, input %q", a.yesNo, a.approval, a.input.Value())
	}

}
