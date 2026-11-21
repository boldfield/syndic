package tui

import (
	"context"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
)

func TestProvisionalPrompt(t *testing.T) {
	a := NewApp(context.Background(), "socket", WithWorkDir(t.TempDir()))
	status := &wire.StatusReply{
		RosterPath: "/etc/roster.toml",
	}
	a.showProvisionalPrompt(status)
	if a.provisional == nil {
		t.Fatal("provisional prompt not set")
	}
	if a.provisional.rosterPath != "/etc/roster.toml" {
		t.Errorf("rosterPath = %q, want /etc/roster.toml", a.provisional.rosterPath)
	}
	if len(a.panel) == 0 {
		t.Fatal("panel not set")
	}
	if a.input.Prompt != provisionalDeadlinePrompt {
		t.Errorf("prompt = %q, want %q", a.input.Prompt, provisionalDeadlinePrompt)
	}
}

func TestCancelProvisional(t *testing.T) {
	a := NewApp(context.Background(), "socket", WithWorkDir(t.TempDir()))
	status := &wire.StatusReply{RosterPath: "/etc/roster.toml"}
	a.showProvisionalPrompt(status)
	a.cancelProvisional("test cancel")
	if a.provisional != nil {
		t.Fatal("provisional not cancelled")
	}
	if a.refusal != "test cancel" {
		t.Errorf("refusal = %q, want 'test cancel'", a.refusal)
	}
	if a.input.Prompt != defaultPrompt {
		t.Errorf("prompt = %q, want %q", a.input.Prompt, defaultPrompt)
	}
}
