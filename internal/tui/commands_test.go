package tui

import (
	"testing"
)

func TestCommandsExist(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/help")
	r.until(t, "help to process", func(a *App) (string, bool) {
		return "", a.refusal != ""
	})

	help := r.app.refusal
	commands := []string{"/mode", "/cancel", "/why", "/override", "/release", "/drafts", "/participants", "/quit", "/resume"}
	for _, cmd := range commands {
		if !contains(help, cmd) {
			t.Errorf("command %s not in help", cmd)
		}
	}
}

func TestModeCommand(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/mode solo")
	r.until(t, "mode command processed", func(a *App) (string, bool) {
		return "", a.refusal == ""
	})

	if r.app.state.NextPreset != "solo" {
		t.Errorf("expected NextPreset to be solo, got %s", r.app.state.NextPreset)
	}
}

func TestModeInvalidPreset(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/mode nonexistent")
	r.until(t, "command to fail", func(a *App) (string, bool) {
		return "", a.refusal != ""
	})

	if !contains(r.app.refusal, "valid presets") {
		t.Errorf("expected preset list in error")
	}
}

func TestCancelNoOpenTurn(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/cancel")
	r.until(t, "command to fail", func(a *App) (string, bool) {
		return "", a.refusal != ""
	})

	if !contains(r.app.refusal, "no open turn") {
		t.Errorf("expected 'no open turn' in error")
	}
}

func TestOverrideNoReason(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	r.typeLine("/override")
	r.until(t, "command to fail", func(a *App) (string, bool) {
		return "", a.refusal != ""
	})

	if !contains(r.app.refusal, "usage") {
		t.Errorf("expected usage message")
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
