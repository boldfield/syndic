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

func TestTranscriptReviewTurn(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer")},
		"reviewer": {approve()},
	})
	r := startTranscriptRig(t, h)

	// Submit a review turn
	turnID := openReviewTurn(t, r)

	// Wait for the turn to be approved
	r.until(t, "approved turn", func(a *App) (string, bool) {
		s := a.state
		if ti, ok := s.Turns[turnID]; ok && ti.State == "approved" {
			return turnID, true
		}
		return "", false
	})

	// Switch to transcript tab by sending Tab key
	send(r, tea.KeyMsg{Type: tea.KeyTab})

	// View should contain the turn ID and show transcript items
	v := view(t, r)
	if !strings.Contains(v, "Turn:") {
		t.Fatalf("transcript view should contain 'Turn:', got:\n%s", v)
	}
}

func TestTranscriptWithBlocks(t *testing.T) {
	blockVerdict := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "medium", "line 3 divides by zero", "guard the divisor", "unsafe")
	})

	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer")},
		"reviewer": {blockVerdict},
	})
	r := startTranscriptRig(t, h)

	turnID := openReviewTurn(t, r)

	// Wait for incomplete state
	r.until(t, "incomplete turn", func(a *App) (string, bool) {
		s := a.state
		if ti, ok := s.Turns[turnID]; ok && ti.State == "incomplete" {
			return turnID, true
		}
		return "", false
	})

	// Switch to transcript and verify it shows
	send(r, tea.KeyMsg{Type: tea.KeyTab})
	v := view(t, r)

	if !strings.Contains(v, "Turn:") {
		t.Fatalf("expected transcript view with Turn info, got:\n%s", v)
	}
}

func TestTranscriptWithLargeOutput(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(strings.Repeat("x", 10000))},
		"reviewer": {approve()},
	})
	r := startTranscriptRig(t, h)

	turnID := openReviewTurn(t, r)

	// Wait for approval
	r.until(t, "approved turn", func(a *App) (string, bool) {
		if ti, ok := a.state.Turns[turnID]; ok && ti.State == "approved" {
			return turnID, true
		}
		return "", false
	})

	send(r, tea.KeyMsg{Type: tea.KeyTab})
	v := view(t, r)

	if !strings.Contains(v, "Turn:") {
		t.Fatalf("expected transcript view, got:\n%s", v)
	}
}

// startTranscriptRig serves the harness and runs an App on it under teatest.
func startTranscriptRig(t *testing.T, h *enginetest.Harness) *rig {
	t.Helper()
	paths := newPaths(t)
	r := &rig{h: h, paths: paths, seen: &seen{}}
	r.srv = startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, paths.Socket)
	r.tm = teatest.NewTestModel(t, &probe{app: r.app}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })
	r.waitConnected(t)
	return r
}
