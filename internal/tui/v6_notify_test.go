package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

var capable = TerminalCapabilities{Color: true, AltScreen: true}

func mouseTestApp(t *testing.T, opts ...Option) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := NewApp(ctx, "", opts...)
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return a
}

func TestV6NotifyMouseOptIn(t *testing.T) {
	// Off by default on a capable terminal: no capture in the model or in the
	// program options.
	off := mouseTestApp(t, WithTerminalCapabilities(capable))
	if off.mouseEnabled {
		t.Error("mouse is on by default")
	}
	if got, base := len(ProgramOptionsWithMouse(capable, false)), len(ProgramOptions(capable)); got != base {
		t.Errorf("program options with the mouse off = %d, want %d", got, base)
	}

	// Opted in on a capable terminal: captured, and the program asks for it.
	on := mouseTestApp(t, WithTerminalCapabilities(capable), WithMouse(true))
	if !on.mouseEnabled {
		t.Error("--mouse on a capable terminal did not enable the mouse")
	}
	if got, base := len(ProgramOptionsWithMouse(capable, true)), len(ProgramOptions(capable)); got != base+1 {
		t.Errorf("program options with the mouse on = %d, want %d", got, base+1)
	}
	if cmd := cmdMouse(off, []string{"on"}); cmd == nil || !off.mouseEnabled {
		t.Error("/mouse on on a capable terminal did not enable capture")
	}
	if cmd := cmdMouse(off, []string{"off"}); cmd == nil || off.mouseEnabled {
		t.Error("/mouse off did not release capture")
	}

	// TERM=dumb (no colour): never captured, however it is asked for.
	t.Setenv("TERM", "dumb")
	caps := DetectTerminalCapabilities()
	if caps.Color {
		t.Fatalf("TERM=dumb detected as %+v", caps)
	}
	plain := mouseTestApp(t, WithTerminalCapabilities(caps), WithMouse(true))
	if plain.mouseEnabled {
		t.Error("--mouse under TERM=dumb enabled the mouse")
	}
	if got, base := len(ProgramOptionsWithMouse(caps, true)), len(ProgramOptions(caps)); got != base {
		t.Errorf("program options under TERM=dumb with --mouse = %d, want %d", got, base)
	}
	if cmd := cmdMouse(plain, []string{"on"}); cmd != nil || plain.mouseEnabled {
		t.Error("/mouse on under TERM=dumb enabled capture")
	}
	if !strings.Contains(plain.refusal, "plain mode") {
		t.Errorf("refusal = %q", plain.refusal)
	}
	before := plain.View()
	if _, cmd := plain.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress, X: 3, Y: 0}); cmd != nil {
		t.Error("a mouse event under plain mode returned a command")
	}
	if after := plain.View(); after != before || plain.mouseEnabled {
		t.Error("a mouse event under plain mode changed the state")
	}
}

func TestV6NotifyMouseReleasedWhenScreenShrinks(t *testing.T) {
	a := mouseTestApp(t, WithTerminalCapabilities(capable), WithMouse(true))
	if !a.mouseEnabled {
		t.Fatal("mouse is off")
	}
	_, cmd := a.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	if cmd == nil || a.mouseEnabled {
		t.Error("shrinking into plain mode kept the mouse")
	}
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if a.mouseEnabled {
		t.Error("growing back restored the mouse without /mouse on")
	}
}

func TestV6NotifyPlainModeBellAndNoEscape(t *testing.T) {
	t.Setenv("TERM", "dumb")
	h := newNotifyHarness(t, true) // default capabilities: plain
	if !h.app.plainMode() {
		t.Fatal("harness app is not in plain mode")
	}
	h.connect(1)
	h.frame(submittedFrame("turn-1", 1))
	if n := h.bells.Load(); n != 0 {
		t.Fatalf("a non-terminal event rang %d bells", n)
	}
	h.frame(terminalEventFrame("turn-1", 2))
	if n := h.bells.Load(); n != 1 {
		t.Errorf("a live terminal event in plain mode rang %d bells, want 1", n)
	}
	if v := h.app.View(); strings.ContainsRune(v, '\x1b') {
		t.Errorf("plain view has an escape sequence: %q", v)
	}
}

func TestV6NotifyPlainModeOffByDefaultNoBell(t *testing.T) {
	h := newNotifyHarness(t, false)
	h.connect(1)
	h.frame(submittedFrame("turn-1", 1))
	h.frame(terminalEventFrame("turn-1", 2))
	if n := h.bells.Load(); n != 0 {
		t.Errorf("notify off rang %d bells", n)
	}
}
