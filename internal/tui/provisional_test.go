package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
)

func TestProvisionalPromptWithStatus(t *testing.T) {
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
	view := a.View()
	if !strings.Contains(view, "Provisional deadlines") {
		t.Errorf("View does not contain 'Provisional deadlines'")
	}
	if !strings.Contains(view, "Configured at: /etc/roster.toml") {
		t.Errorf("View does not contain roster path")
	}
	if !strings.Contains(view, "(y/n)>") {
		t.Errorf("View does not contain prompt '(y/n)>'")
	}
}

func TestProvisionalPromptWithoutStatus(t *testing.T) {
	a := NewApp(context.Background(), "socket", WithWorkDir(t.TempDir()))
	a.showProvisionalPrompt(nil)
	if a.provisional == nil {
		t.Fatal("provisional prompt not set")
	}
	view := a.View()
	if !strings.Contains(view, "Provisional deadlines are in effect") {
		t.Errorf("View does not contain 'Provisional deadlines are in effect'")
	}
	if !strings.Contains(view, "(y/n)>") {
		t.Errorf("View does not contain prompt '(y/n)>'")
	}
}

func TestProvisionalInputNotCleared(t *testing.T) {
	a := NewApp(context.Background(), "socket", WithWorkDir(t.TempDir()))
	a.input.SetValue("user input")
	a.lastSubmitInput = "user input"

	a.provisional = &provisionalPrompt{}

	yes := a.provisionalYes()
	if yes == nil {
		t.Fatal("provisionalYes should return a command")
	}

	if a.input.Value() != "user input" {
		t.Errorf("input was cleared before engine accepts: got %q, want 'user input'", a.input.Value())
	}
}

func TestProvisionalNoKeepsInput(t *testing.T) {
	a := NewApp(context.Background(), "socket", WithWorkDir(t.TempDir()))
	a.input.SetValue("user input")
	a.provisional = &provisionalPrompt{}
	a.panel = []string{"some panel text"}

	no := a.provisionalNo()
	if no != nil {
		t.Fatal("provisionalNo should return nil")
	}

	if a.provisional != nil {
		t.Fatal("provisional should be cleared after no")
	}

	if a.input.Value() != "user input" {
		t.Errorf("input should be kept on no: got %q", a.input.Value())
	}

	if a.refusal != "submit cancelled; input kept" {
		t.Errorf("refusal = %q, want 'submit cancelled; input kept'", a.refusal)
	}
}
