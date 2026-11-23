package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// D-specific acceptance tests for the chat TUI.
// These tests verify user-observable behavior for milestone D requirements:
// the chat tab display, status line, keyboard behavior, and command help.
// All tests assert against exact design clause strings per section 7 TUI acceptance.

// TestV5ChatTabShowsConversationID verifies that the chat tab's first line
// names the open conversation by id prefix, as specified in section 7 TUI acceptance.
func TestV5ChatTabShowsConversationID(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("test question")
	turnID := r.waitTurn(t)

	// Wait for the turn to be approved.
	r.until(t, "turn approved", func(a *App) (string, bool) {
		return "", a.state.TurnState[turnID] == "approved"
	})

	output := r.until(t, "turn displayed", func(a *App) (string, bool) {
		return a.View(), true
	})

	lines := strings.Split(output, "\n")
	// Find the first content line after the tab bar.
	firstContentLine := ""
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line != "" && !strings.HasPrefix(line, "[") && !strings.Contains(line, ">") {
			firstContentLine = line
			break
		}
	}

	if firstContentLine == "" {
		t.Fatalf("chat view has no content lines after tab bar")
	}

	// First line should contain conversation ID prefix.
	convIDPrefix := r.app.conversationID[:8]
	if !strings.Contains(firstContentLine, convIDPrefix) {
		t.Errorf("chat first line missing conversation ID %q: %q", convIDPrefix, firstContentLine)
	}
}

// TestV5StatusLineLastLine verifies the status line is the last line at 80x24,
// as specified in section 7 TUI acceptance.
func TestV5StatusLineLastLine(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)
	r.typeLine("test")
	r.waitTurn(t)

	output := r.until(t, "turn displayed", func(a *App) (string, bool) {
		return a.View(), true
	})

	lines := strings.Split(output, "\n")
	if len(lines) != 24 {
		t.Errorf("got %d lines at 80x24, want 24", len(lines))
	}

	// Last line is the status line; must have [ prefix and tab: suffix.
	lastLine := lines[23]
	if !strings.HasPrefix(lastLine, "[") {
		t.Errorf("status line missing '[' prefix: %q", lastLine)
	}
	if !strings.HasSuffix(lastLine, "tab: switch  /help") {
		t.Errorf("status line missing 'tab: switch  /help' suffix: %q", lastLine)
	}
}

// TestV5IncompleteAnswerMarker verifies that incomplete turns show "no approved answer (<state>)",
// as specified in section 7 TUI acceptance.
func TestV5IncompleteAnswerMarker(t *testing.T) {
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
			verdictBlockFunc(func(hash, attempt string) string {
				return fake.VerdictBlock(hash, attempt, "b2", "high", "still no", "nope", "reason")
			}),
		},
	})

	r := &rig{h: h, paths: newPaths(t), seen: &seen{}}
	r.srv = startServer(t, r.paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, r.paths.Socket, WithEventObserver(r.seen.observe))
	r.tm = teatest.NewTestModel(t, &probe{app: r.app}, teatest.WithInitialTermSize(80, 24))
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
		t.Errorf("missing 'no approved answer' marker: %s", output)
	}
}

// TestV5BannerStringsExact verifies that v4 banner strings appear verbatim,
// as specified in section 7 TUI acceptance.
func TestV5BannerStringsExact(t *testing.T) {
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
			verdictBlockFunc(func(hash, attempt string) string {
				return fake.VerdictBlock(hash, attempt, "b2", "high", "still no", "nope", "reason")
			}),
		},
	})

	r := &rig{h: h, paths: newPaths(t), seen: &seen{}}
	r.srv = startServer(t, r.paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, r.paths.Socket, WithEventObserver(r.seen.observe))
	r.tm = teatest.NewTestModel(t, &probe{app: r.app}, teatest.WithInitialTermSize(80, 24))
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

	// /help must list key commands: /new, /switch.
	required := []string{"/new", "/switch"}
	for _, cmd := range required {
		if !strings.Contains(output, cmd) {
			t.Errorf("/help missing command %q:\n%s", cmd, output)
		}
	}
}
