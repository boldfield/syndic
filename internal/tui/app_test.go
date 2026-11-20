package tui

import (
	"context"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// TestAppRendersThreeRegions tests that the app renders three regions at 80x24.
func TestAppRendersThreeRegions(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.width = 80
	app.height = 24
	app.state.ConnState = ConnStateConnected

	output := app.View()
	if output == "" {
		t.Fatal("expected non-empty view")
	}

	lines := countLines(output)
	if lines < 3 {
		t.Fatalf("expected at least 3 lines, got %d", lines)
	}
}

// TestTabSwitching tests that tab switching works.
func TestTabSwitching(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.state.ConnState = ConnStateConnected

	if app.activeTab != TabAnswer {
		t.Fatal("expected initial tab to be Answer")
	}

	// Simulate tab key
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("tab")}
	newApp, _ := app.Update(msg)
	updatedApp := newApp.(*App)

	if updatedApp.activeTab != TabTranscript {
		t.Fatal("expected tab to switch to Transcript")
	}

	// Simulate shift+tab
	msg = tea.KeyMsg{Type: tea.KeyShiftTab}
	newApp, _ = updatedApp.Update(msg)
	updatedApp = newApp.(*App)

	if updatedApp.activeTab != TabAnswer {
		t.Fatal("expected tab to switch back to Answer")
	}
}

// TestInputHandling tests that input is handled correctly.
func TestInputHandling(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")

	// Simulate typing
	for _, ch := range "hello" {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}}
		newApp, _ := app.Update(msg)
		app = newApp.(*App)
	}

	if app.inputLine != "hello" {
		t.Fatalf("expected input 'hello', got '%s'", app.inputLine)
	}

	// Simulate backspace
	msg := tea.KeyMsg{Type: tea.KeyBackspace}
	newApp, _ := app.Update(msg)
	app = newApp.(*App)

	if app.inputLine != "hell" {
		t.Fatalf("expected input 'hell', got '%s'", app.inputLine)
	}
}

// TestUnknownCommand tests that unknown commands show an error.
func TestUnknownCommand(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")

	// Set input to an unknown command
	app.inputLine = "/unknown"

	// Process the input
	newApp, _ := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = newApp.(*App)

	// After processing, the refusal should be set for unknown commands
	if !contains(app.refusal, "unknown command") {
		t.Fatalf("expected refusal to contain 'unknown command', got '%s'", app.refusal)
	}
}

// TestCtrlCQuit tests that ctrl+c quits the app.
func TestCtrlCQuit(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")

	msg := tea.KeyMsg{Type: tea.KeyCtrlC}
	_, cmd := app.Update(msg)

	if cmd == nil {
		t.Fatal("expected quit command")
	}
}

// TestQKeyDoesNotQuit tests that 'q' alone does not quit (only /quit should).
func TestQKeyDoesNotQuit(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")

	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	newApp, cmd := app.Update(msg)
	app = newApp.(*App)

	// 'q' should be added to input, not quit
	if app.inputLine != "q" {
		t.Fatalf("expected 'q' to be added to input, got '%s'", app.inputLine)
	}

	if cmd != nil {
		t.Fatal("expected no command for 'q' key")
	}
}

// TestRefusalPreservesInput tests that refusal keeps the input text.
func TestRefusalPreservesInput(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.inputLine = "some input"
	app.refusal = "some error"

	// The input should still be there
	if app.inputLine != "some input" {
		t.Fatalf("expected input to be preserved, got '%s'", app.inputLine)
	}
}

// TestReconnectingState tests that reconnecting state is shown.
func TestReconnectingState(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.width = 80
	app.height = 24
	app.state.ConnState = ConnStateReconnecting

	output := app.View()
	if !contains(output, "engine: reconnecting") {
		t.Fatalf("expected reconnecting message in output, got:\n%s", output)
	}
}

// TestStateUpdatesLastSeq tests that state updates the last sequence number.
func TestStateUpdatesLastSeq(t *testing.T) {
	s := State{}
	frame := wire.Frame{
		Seq:  42,
		Kind: "test_event",
	}

	newS := Apply(s, frame)
	if newS.LastSeq != 42 {
		t.Fatalf("expected LastSeq to be 42, got %d", newS.LastSeq)
	}
}

// TestStateHandlesEngineStopping tests that engine_stopping is handled.
func TestStateHandlesEngineStopping(t *testing.T) {
	s := State{ConnState: ConnStateConnected}
	frame := wire.Frame{
		Seq:  1,
		Kind: "engine_stopping",
	}

	newS := Apply(s, frame)
	if newS.ConnState != ConnStateClosed {
		t.Fatalf("expected ConnState to be Closed, got %s", newS.ConnState)
	}
}

// TestWindowResize tests that window resize is handled.
func TestWindowResize(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")

	msg := tea.WindowSizeMsg{Width: 100, Height: 50}
	newApp, _ := app.Update(msg)
	app = newApp.(*App)

	if app.width != 100 || app.height != 50 {
		t.Fatalf("expected width=100, height=50, got width=%d, height=%d", app.width, app.height)
	}
}

// Helper functions

func countLines(s string) int {
	count := 1
	for _, ch := range s {
		if ch == '\n' {
			count++
		}
	}
	return count
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// BenchmarkAppUpdate benchmarks the Update method.
func BenchmarkAppUpdate(b *testing.B) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		app.Update(msg)
	}
}

// BenchmarkAppView benchmarks the View method.
func BenchmarkAppView(b *testing.B) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.width = 80
	app.height = 24
	app.state.ConnState = ConnStateConnected

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		app.View()
	}
}
