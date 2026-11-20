package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
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

	turnID := openReviewTurn(t, r)

	r.until(t, "approved turn", func(a *App) (string, bool) {
		s := a.state
		if ti, ok := s.Turns[turnID]; ok && ti.State == "approved" {
			return turnID, true
		}
		return "", false
	})

	send(r, tea.KeyMsg{Type: tea.KeyTab})

	r.until(t, "transcript items loaded", func(a *App) (string, bool) {
		return "", len(a.transcriptsByTurn[turnID]) > 0
	})

	r.until(t, "bodies fetched", func(a *App) (string, bool) {
		for _, item := range a.transcriptsByTurn[turnID] {
			ci := a.content.Get(item.Ref)
			if !ci.Ready {
				return "", false
			}
		}
		return "", true
	})

	v := view(t, r)
	if !strings.Contains(v, "Turn:") {
		t.Fatalf("transcript view should contain 'Turn:', got:\n%s", v)
	}

	if !strings.Contains(v, "[author]") || !strings.Contains(v, "[review]") {
		t.Fatalf("transcript should show phases, got:\n%s", v)
	}

	if strings.Contains(v, "loading…") {
		t.Fatalf("transcript should show loaded bodies, got:\n%s", v)
	}
}

func TestTranscriptWithBlocks(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer")},
		"reviewer": {approve()},
	})
	r := startTranscriptRig(t, h)

	turnID := openReviewTurn(t, r)

	r.until(t, "approved turn", func(a *App) (string, bool) {
		s := a.state
		if ti, ok := s.Turns[turnID]; ok && ti.State == "approved" {
			return turnID, true
		}
		return "", false
	})

	send(r, tea.KeyMsg{Type: tea.KeyTab})

	r.until(t, "bodies fetched", func(a *App) (string, bool) {
		for _, item := range a.transcriptsByTurn[turnID] {
			ci := a.content.Get(item.Ref)
			if !ci.Ready {
				return "", false
			}
		}
		return "", true
	})

	v := view(t, r)

	if !strings.Contains(v, "Turn:") {
		t.Fatalf("expected transcript view with Turn info, got:\n%s", v)
	}

	if strings.Contains(v, "loading…") {
		t.Fatalf("bodies should be fetched, got:\n%s", v)
	}
}

func TestTranscriptWithLargeOutput(t *testing.T) {
	largeOutput := strings.Repeat("x", 10000)
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(largeOutput)},
		"reviewer": {approve()},
	})
	r := startTranscriptRig(t, h)

	turnID := openReviewTurn(t, r)

	r.until(t, "approved turn", func(a *App) (string, bool) {
		if ti, ok := a.state.Turns[turnID]; ok && ti.State == "approved" {
			return turnID, true
		}
		return "", false
	})

	send(r, tea.KeyMsg{Type: tea.KeyTab})

	r.until(t, "bodies fetched", func(a *App) (string, bool) {
		for _, item := range a.transcriptsByTurn[turnID] {
			ci := a.content.Get(item.Ref)
			if !ci.Ready {
				return "", false
			}
		}
		return "", true
	})

	v1 := view(t, r)
	if !strings.Contains(v1, "Turn:") {
		t.Fatalf("expected transcript view, got:\n%s", v1)
	}

	if strings.Contains(v1, "loading…") {
		t.Fatalf("large output should be loaded, got:\n%s", v1)
	}
}

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
