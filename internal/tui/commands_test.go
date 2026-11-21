package tui

import (
	"strings"
	"testing"
)

func TestHelpListing(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/help")
	r.until(t, "help command processed", func(a *App) (string, bool) {
		return "", a.refusal != ""
	})

	help := r.app.refusal
	commands := []string{"/mode", "/cancel", "/why", "/override", "/release", "/drafts", "/participants", "/quit", "/resume"}
	for _, cmd := range commands {
		if !strings.Contains(help, cmd) {
			t.Errorf("command %s not in help", cmd)
		}
	}
	if !strings.Contains(help, "\n") {
		t.Errorf("help should have newlines, got single line: %s", help)
	}
}

func TestModeSoloPresetStored(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/mode solo")
	r.until(t, "/mode accepted", func(a *App) (string, bool) {
		return "", a.state.NextPreset == "solo" && a.refusal == ""
	})
}

func TestCancel(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("test input")
	turnID := r.waitTurn(t)

	r.typeLine("/cancel")
	r.until(t, "/cancel to terminate the turn", func(a *App) (string, bool) {
		ti := a.state.Turns[turnID]
		return "", ti.TerminalReason != nil && *ti.TerminalReason == "cancelled"
	})
}

func TestCancelNoOpenTurn(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/cancel")
	r.until(t, "command to fail", func(a *App) (string, bool) {
		return "", a.refusal != ""
	})

	if !strings.Contains(r.app.refusal, "no open turn") {
		t.Errorf("expected 'no open turn' in error, got: %s", r.app.refusal)
	}
}

// TODO: TestWhyGateFailure - requires model mismatch or other gate failure
// func TestWhyGateFailure(t *testing.T) {
// 	// Gate errors are recorded by the engine when CheckResult fails
// 	// (e.g., model mismatch). This test would need to set up such a scenario.
// }

func TestWhyOpenTurn(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("test input")
	r.waitTurn(t)

	r.typeLine("/why")
	r.until(t, "/why on open turn to process", func(a *App) (string, bool) {
		return "", a.refusal != ""
	})

	if !strings.Contains(r.app.refusal, "still running") {
		t.Errorf("expected 'still running', got: %s", r.app.refusal)
	}
}

func TestOverrideNoReason(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/override")
	r.until(t, "command to fail", func(a *App) (string, bool) {
		return "", a.refusal != ""
	})

	if !strings.Contains(r.app.refusal, "usage") {
		t.Errorf("expected usage message, got: %s", r.app.refusal)
	}
}

func TestDraftsToggle(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/drafts on")
	r.until(t, "command to process", func(a *App) (string, bool) {
		return "", a.refusal == ""
	})

	r.typeLine("/drafts off")
	r.until(t, "command to process", func(a *App) (string, bool) {
		return "", a.refusal == ""
	})
}

func TestParticipantsTab(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/participants")
	r.until(t, "tab to change", func(a *App) (string, bool) {
		return "", a.tab == tabParticipants
	})
}

func TestModeInvalidPreset(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/mode nonexistent")
	r.until(t, "command to fail", func(a *App) (string, bool) {
		return "", a.refusal != ""
	})

	if !strings.Contains(r.app.refusal, "valid presets") {
		t.Errorf("expected preset list in error, got: %s", r.app.refusal)
	}
}
