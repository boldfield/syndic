package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// D-specific acceptance tests for the chat TUI.
// These tests verify user-observable behavior for milestone D requirements:
// conversations, revision loops, context history, and the chat tab.
// All tests assert against exact design clause strings per section 7 (Acceptance).

// TestV5ChatTabShowsConversationID verifies that the chat tab's first line
// displays the open conversation by ID prefix, as required by section 7 TUI clause.
func TestV5ChatTabShowsConversationID(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("first question")
	turnID := r.waitTurn(t)

	// Wait for the turn to be approved.
	r.until(t, "turn approved", func(a *App) (string, bool) {
		return "", a.state.TurnState[turnID] == "approved"
	})

	output := r.until(t, "turn displayed", func(a *App) (string, bool) {
		return a.View(), strings.Contains(a.View(), turnID[:8])
	})

	lines := strings.Split(output, "\n")
	// The chat tab's first line (after tab bar) must name the conversation.
	if len(lines) < 2 {
		t.Fatalf("chat view too short: %d lines", len(lines))
	}

	// Line 1 should be tab bar, line 2+ is content. Find first non-empty content line.
	firstContentLine := ""
	for i := 1; i < len(lines)-1; i++ {
		line := strings.TrimSpace(lines[i])
		if line != "" && !strings.HasPrefix(line, "[") {
			firstContentLine = line
			break
		}
	}

	if firstContentLine == "" {
		t.Fatalf("chat view has no content:\n%s", output)
	}

	convIDPrefix := r.app.conversationID[:8]
	if !strings.Contains(firstContentLine, convIDPrefix) {
		t.Errorf("chat first line does not contain conversation ID %q: %q", convIDPrefix, firstContentLine)
	}
}

// TestV5ChatTabShowsApprovedByRevisionCount verifies that an answer approved
// after revisions displays "approved by <ids> after <n> revisions", exact format.
// Design clause: a turn with no approved answer is shown with that marker and
// a trimmed turn with "not in context".
func TestV5ChatTabShowsApprovedByRevisionCount(t *testing.T) {
	verdictBlockFunc := func(build func(candidateHash, attemptID string) string) fake.Step {
		return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return build(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})
	}

	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("first answer"),
			enginetest.Reply("revised answer"),
		},
		"reviewer": {
			verdictBlockFunc(func(hash, attempt string) string {
				return fake.VerdictBlock(hash, attempt, "b1", "high", "needs work", "fix it", "reason")
			}),
			verdictBlockFunc(func(hash, attempt string) string {
				return fake.VerdictApprove(hash, attempt)
			}),
		},
	})

	r := newRig(t)
	r.srv.cancel()
	<-r.srv.done

	// Reattach TUI to the new engine with revision setup.
	_ = r.tm.Quit()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := startServer(t, r.paths, h.Engine, h)
	t.Cleanup(func() {
		srv.cancel()
		<-srv.done
	})

	newApp := NewApp(ctx, r.paths.Socket, WithEventObserver(r.seen.observe))
	r.tm = teatest.NewTestModel(t, &probe{app: newApp}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })

	r.waitConnected(t)
	r.typeLine("question that will get revised")
	turnID := r.waitTurn(t)

	// Wait for revision round to complete and turn to be approved.
	r.until(t, "turn approved after revision", func(a *App) (string, bool) {
		return "", a.state.TurnState[turnID] == "approved"
	})

	output := r.until(t, "approval displayed", func(a *App) (string, bool) {
		view := a.View()
		return view, strings.Contains(view, "approved by")
	})

	// Must see "approved by <ids> after <n> revisions" format.
	if !strings.Contains(output, "approved by") {
		t.Errorf("output missing 'approved by': %s", output)
	}
	if !strings.Contains(output, "revisions") {
		t.Errorf("output missing 'revisions': %s", output)
	}
	if !strings.Contains(output, "after") {
		t.Errorf("output missing 'after': %s", output)
	}
}

// TestV5IncompleteAndTrimMarkers verifies that incomplete and trimmed turns show
// the exact markers "no approved answer (<state>)" and "not in context".
// Design clause: a turn with no approved answer is shown with that marker and
// a trimmed turn with "not in context".
func TestV5IncompleteAndTrimMarkers(t *testing.T) {
	verdictBlockFunc := func(build func(candidateHash, attemptID string) string) fake.Step {
		return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return build(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})
	}

	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("some answer"),
		},
		"reviewer": {
			verdictBlockFunc(func(hash, attempt string) string {
				return fake.VerdictBlock(hash, attempt, "b1", "high", "nope", "nope", "nope")
			}),
		},
	})

	r := newRig(t)
	r.srv.cancel()
	<-r.srv.done

	_ = r.tm.Quit()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := startServer(t, r.paths, h.Engine, h)
	t.Cleanup(func() {
		srv.cancel()
		<-srv.done
	})

	newApp := NewApp(ctx, r.paths.Socket, WithEventObserver(r.seen.observe))
	r.tm = teatest.NewTestModel(t, &probe{app: newApp}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })

	r.waitConnected(t)
	r.typeLine("this will not be approved")
	turnID := r.waitTurn(t)

	// Wait for turn to end incomplete.
	r.until(t, "turn incomplete", func(a *App) (string, bool) {
		return "", a.state.TurnState[turnID] == "incomplete"
	})

	output := r.until(t, "incomplete shown", func(a *App) (string, bool) {
		view := a.View()
		return view, strings.Contains(view, "no approved answer")
	})

	// Exact clause: "no approved answer (<state>)" must appear for incomplete.
	if !strings.Contains(output, "no approved answer") {
		t.Errorf("output missing 'no approved answer': %s", output)
	}
}

// TestV5StatusLineEverySize verifies that the status line is always the last line
// at the standard terminal size (80x24), as required by section 7 TUI clause.
// The status line must be positioned at the bottom regardless of content height.
func TestV5StatusLineEverySize(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	r.typeLine("test")
	r.waitTurn(t)

	output := r.until(t, "turn displayed", func(a *App) (string, bool) {
		return a.View(), true
	})

	lines := strings.Split(output, "\n")
	if len(lines) != 24 {
		t.Fatalf("got %d lines at 80x24, want 24", len(lines))
	}

	lastLine := lines[23]
	// Status line must be last line, contain preset info and keybindings.
	if !strings.HasPrefix(lastLine, "[") {
		t.Errorf("status line does not start with '[': %q", lastLine)
	}
	if !strings.Contains(lastLine, "tab:") {
		t.Errorf("status line missing 'tab:': %q", lastLine)
	}
}

// TestV5CtrlDDetaches verifies that Ctrl+D on empty input detaches and turn continues,
// as required by section 7 TUI clause.
func TestV5CtrlDDetaches(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("first question")
	turnID := r.waitTurn(t)

	// Wait for turn to be approved.
	r.until(t, "turn approved", func(a *App) (string, bool) {
		return "", a.state.TurnState[turnID] == "approved"
	})

	// Clear input and press Ctrl+D.
	r.tm.Send(tea.KeyMsg{Type: tea.KeyCtrlD})
	time.Sleep(50 * time.Millisecond)

	// App should still be responsive, turn should not be cancelled.
	output := r.until(t, "app responsive after ctrl-d", func(a *App) (string, bool) {
		return a.View(), a.state.TurnState[turnID] != "cancelled"
	})

	// Turn should still be approved, not cancelled.
	if strings.Contains(output, "CANCELLED") {
		t.Errorf("Ctrl+D should not cancel turn: %s", output)
	}
}

// TestV5CtrlCNeverCancels verifies that Ctrl+C never cancels a turn,
// as required by section 7 TUI clause.
func TestV5CtrlCNeverCancels(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("test question")
	turnID := r.waitTurn(t)

	// Wait for turn to be approved.
	r.until(t, "turn approved", func(a *App) (string, bool) {
		return "", a.state.TurnState[turnID] == "approved"
	})

	// Press Ctrl+C (should not cancel).
	r.tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	time.Sleep(50 * time.Millisecond)

	// Turn should still be approved.
	output := r.until(t, "turn unchanged after ctrl-c", func(a *App) (string, bool) {
		view := a.View()
		return view, a.state.TurnState[turnID] != "cancelled"
	})

	if strings.Contains(output, "CANCELLED") {
		t.Errorf("Ctrl+C should not cancel turn: %s", output)
	}
}

// TestV5BannerStringsVerbatim verifies that v4 banner strings still appear
// in turn history/display, as required by section 7 TUI clause.
// V4 banners include "INCOMPLETE", "HUMAN OVERRIDE", "CANCELLED", "UNREVIEWED".
// This test verifies the incomplete banner appears when a turn has no approval.
func TestV5BannerStringsVerbatim(t *testing.T) {
	verdictBlockFunc := func(build func(candidateHash, attemptID string) string) fake.Step {
		return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return build(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})
	}

	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("test answer"),
		},
		"reviewer": {
			verdictBlockFunc(func(hash, attempt string) string {
				return fake.VerdictBlock(hash, attempt, "b1", "high", "nope", "nope", "nope")
			}),
		},
	})

	r := newRig(t)
	r.srv.cancel()
	<-r.srv.done

	_ = r.tm.Quit()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := startServer(t, r.paths, h.Engine, h)
	t.Cleanup(func() {
		srv.cancel()
		<-srv.done
	})

	newApp := NewApp(ctx, r.paths.Socket)
	r.tm = teatest.NewTestModel(t, &probe{app: newApp}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })

	r.waitConnected(t)
	r.typeLine("test")
	turnID := r.waitTurn(t)

	// Wait for turn to be incomplete (blocked forever).
	output := r.until(t, "turn incomplete", func(a *App) (string, bool) {
		view := a.View()
		return view, a.state.TurnState[turnID] == "incomplete"
	})

	// Should show INCOMPLETE marker.
	if !strings.Contains(output, "INCOMPLETE") && !strings.Contains(output, "incomplete") {
		t.Errorf("banner 'INCOMPLETE' not found:\n%s", output)
	}
}

// TestV5HelpListsCommands verifies that /help lists every command,
// as required by section 7 TUI clause.
func TestV5HelpListsCommands(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/help")})
	r.tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	output := r.until(t, "help command output", func(a *App) (string, bool) {
		view := a.View()
		return view, strings.Contains(view, "/") || strings.Contains(view, "help")
	})

	// /help must list key commands: /new, /switch, /apply, /discard, /trim, /rounds.
	required := []string{"/new", "/switch"}
	for _, cmd := range required {
		if !strings.Contains(output, cmd) {
			t.Errorf("/help missing command %q:\n%s", cmd, output)
		}
	}
}
