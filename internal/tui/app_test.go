package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	_ "github.com/charmbracelet/x/exp/teatest"
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

	msg := tea.KeyMsg{Type: tea.KeyTab}
	newApp, _ := app.Update(msg)
	updatedApp := newApp.(*App)

	if updatedApp.activeTab != TabTranscript {
		t.Fatal("expected tab to switch to Transcript")
	}

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

	for _, ch := range "hello" {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}}
		newApp, _ := app.Update(msg)
		app = newApp.(*App)
	}

	if app.inputLine != "hello" {
		t.Fatalf("expected input 'hello', got '%s'", app.inputLine)
	}

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

	app.inputLine = "/unknown"

	newApp, _ := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = newApp.(*App)

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

	if app.inputLine != "some input" {
		t.Fatalf("expected input to be preserved, got '%s'", app.inputLine)
	}
}

// TestReconnectingStateShownInStrip tests that reconnecting state is shown in strip.
func TestReconnectingStateShownInStrip(t *testing.T) {
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

// TestStateHandlesEngineStopping tests that engine_stopping triggers reconnect.
func TestStateHandlesEngineStopping(t *testing.T) {
	s := State{ConnState: ConnStateConnected}
	frame := wire.Frame{
		Seq:  1,
		Kind: "engine_stopping",
	}

	newS := Apply(s, frame)
	if newS.ConnState != ConnStateReconnecting {
		t.Fatalf("expected ConnState to be Reconnecting, got %s", newS.ConnState)
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

// TestSubmitResultStoresCurrentTurn tests that a successful submit stores the turn ID.
func TestSubmitResultStoresCurrentTurn(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.state.ConnState = ConnStateConnected

	msg := SubmitResultMsg{TurnID: "turn-123"}
	newApp, _ := app.Update(msg)
	app = newApp.(*App)

	if app.state.CurrentTurn != "turn-123" {
		t.Fatalf("expected CurrentTurn to be 'turn-123', got '%s'", app.state.CurrentTurn)
	}
}

// TestSubmitResultClearsInputOnSuccess tests that successful submit clears input.
func TestSubmitResultClearsInputOnSuccess(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.state.ConnState = ConnStateConnected
	app.inputLine = "some input"
	app.refusal = ""

	msg := SubmitResultMsg{TurnID: "turn-123"}
	newApp, _ := app.Update(msg)
	app = newApp.(*App)

	if app.inputLine != "" {
		t.Fatalf("expected input to be cleared, got '%s'", app.inputLine)
	}
}

// TestSubmitResultErrorShowsRefusal tests that submit error shows refusal message.
func TestSubmitResultErrorShowsRefusal(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.state.ConnState = ConnStateConnected
	app.inputLine = "some input"

	errMsg := "busy"
	msg := SubmitResultMsg{Err: &wire.Error{Code: "busy"}, RefusalDetail: errMsg}
	newApp, _ := app.Update(msg)
	app = newApp.(*App)

	if app.refusal == "" {
		t.Fatal("expected refusal message")
	}
	if app.inputLine != "some input" {
		t.Fatalf("expected input to be preserved on error, got '%s'", app.inputLine)
	}
}

// TestConnStateConnectedTriggersHello tests that ConnStateConnected triggers hello.
func TestConnStateConnectedTriggersHello(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")

	msg := ConnStateMsg{State: ConnStateConnected}
	_, cmd := app.Update(msg)

	if cmd == nil {
		t.Fatal("expected command to send hello")
	}
}

// TestCurrentTurnGetter tests the CurrentTurn getter method.
func TestCurrentTurnGetter(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.state.CurrentTurn = "turn-789"

	if app.CurrentTurn() != "turn-789" {
		t.Fatalf("expected CurrentTurn() to return 'turn-789', got '%s'", app.CurrentTurn())
	}
}

// TestEventHandlingContinuesStream tests that event handling returns a command to continue streaming.
func TestEventHandlingContinuesStream(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.state.ConnState = ConnStateConnected
	app.connector = newConnector("/tmp/test.sock")

	frame := wire.Frame{Seq: 1, Kind: "engine_started"}
	msg := EventMsg{Frame: frame}

	_, cmd := app.Update(msg)

	if cmd == nil {
		t.Fatal("expected command to continue streaming events")
	}
}

// TestEventErrorTriggersReconnect tests that event error triggers reconnect.
func TestEventErrorTriggersReconnect(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.state.ConnState = ConnStateConnected
	app.connector = newConnector("/tmp/test.sock")

	msg := EventMsg{Err: errors.New("connection lost")}

	newApp, cmd := app.Update(msg)
	app = newApp.(*App)

	if app.state.ConnState != ConnStateReconnecting {
		t.Fatalf("expected ConnState to be Reconnecting after event error")
	}

	if cmd == nil {
		t.Fatal("expected reconnect command")
	}
}

// TestGetterMethods tests that getter methods are correctly exported.
func TestGetterMethods(t *testing.T) {
	app := NewApp(context.Background(), "/tmp/test.sock")
	app.state.CurrentTurn = "test-turn"

	turn := app.CurrentTurn()
	if turn != "test-turn" {
		t.Fatalf("CurrentTurn() returned %q, expected %q", turn, "test-turn")
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
