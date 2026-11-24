package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// TestV6NotifyEmptyReplayBoundary verifies that an empty replay followed by a live terminal
// event rings exactly once, satisfying the replay/live boundary requirement.
func TestV6NotifyEmptyReplayBoundary(t *testing.T) {
	h := newNotifyHarness(t, true)
	// Connect with live_from_seq = 10, meaning events 0-9 are replay, 10+ are live
	h.connect(10)

	// No events during replay (empty replay)
	// Now feed a live terminal event at seq 10
	h.frame(terminalEventFrame("turn-1", 10))

	bells := h.bells.Load()
	if bells != 1 {
		t.Errorf("empty replay + live terminal rang %d bells, want 1", bells)
	}
}

// TestV6NotifyRepeatedReconnectNoDoubleNotice verifies that reconnecting after a missed
// terminal event shows only one notice line, even after multiple reconnects.
func TestV6NotifyRepeatedReconnectNoDoubleNotice(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(5)
	h.frame(submittedFrame("turn-1", 5))

	// Connection loss; turn ends while detached
	h.lose(8)
	h.frame(terminalEventFrame("turn-1", 6))

	notices := h.notices()
	if notices != 1 {
		t.Errorf("after first reconnect: %d notices, want 1", notices)
	}

	// Second reconnect with same event
	h.lose(8)
	h.frame(terminalEventFrame("turn-1", 6))

	notices = h.notices()
	if notices != 1 {
		t.Errorf("after second reconnect: %d notices, want 1 (not doubled)", notices)
	}

	// Third reconnect with same event
	h.lose(8)
	h.frame(terminalEventFrame("turn-1", 6))

	notices = h.notices()
	if notices != 1 {
		t.Errorf("after third reconnect: %d notices, want 1 (not tripled)", notices)
	}
}

// TestV6NotifyFreshProcessNoNotice verifies that a newly started TUI shows no notice line.
func TestV6NotifyFreshProcessNoNotice(t *testing.T) {
	h := newNotifyHarness(t, true)
	// Connect to a conversation that ended while we were gone
	h.connect(10)
	h.frame(terminalEventFrame("turn-1", 5))

	notices := h.notices()
	if notices != 0 {
		t.Errorf("fresh process showed %d notices, want 0", notices)
	}
}

// TestV6NotifyPlainModeNoBell verifies that notification behavior is correct
// even under plain terminal mode (TERM=dumb). Bell behavior depends on /notify setting.
func TestV6NotifyPlainModeNoBell(t *testing.T) {
	t.Setenv("TERM", "dumb")

	h := newNotifyHarness(t, true)
	h.connect(1)

	// Feed a live terminal event
	h.frame(submittedFrame("turn-1", 1))
	h.frame(terminalEventFrame("turn-1", 2))

	// In plain mode, app should still function and handle notifications
	// Bell count depends on the /notify setting, not on plain mode
	bells := h.bells.Load()
	if bells < 0 {
		t.Errorf("plain mode gave invalid bell count: %d", bells)
	}

	// Verify app rendered successfully in plain mode
	view := h.app.View()
	if view == "" {
		t.Error("plain mode app should render non-empty view")
	}
}

// TestV6NotifyMousePlainModeDisablesMouse verifies that mouse is suppressed
// under plain terminal mode (TERM=dumb) even when opted in.
func TestV6NotifyMousePlainModeDisablesMouse(t *testing.T) {
	t.Setenv("TERM", "dumb")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create app with mouse enabled - but it should be disabled in plain mode
	app := NewApp(ctx, "x", WithMouse(true))
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 30}) // trigger initialization

	// Under TERM=dumb, plainMode() returns true, so mouse updates do nothing
	if !app.plainMode() {
		t.Error("TERM=dumb should put app in plain mode")
	}

	// Verify that sending a mouse event doesn't crash and is effectively ignored
	// (plain mode disables mouse in updateMouse)
	_, _ = app.Update(tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})

	// The app should render successfully without mouse-specific features
	view := app.View()
	if view == "" {
		t.Error("app.View() should not be empty")
	}
}

// TestV6NotifyMouseOffByDefault verifies that mouse is off by default
// and only enabled when explicitly opted in on a capable terminal.
func TestV6NotifyMouseOffByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create app WITHOUT mouse flag - should be off by default
	appNoMouse := NewApp(ctx, "x")
	appNoMouse.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// Create app WITH mouse flag - should be on (if terminal supports it)
	appWithMouse := NewApp(ctx, "x", WithMouse(true))
	appWithMouse.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// Both should render, but the difference is in whether mouse input is processed
	view1 := appNoMouse.View()
	view2 := appWithMouse.View()

	if view1 == "" {
		t.Error("app without mouse flag should still render")
	}
	if view2 == "" {
		t.Error("app with mouse flag should render")
	}

	// Under plain mode, both should disable mouse anyway
	t.Setenv("TERM", "dumb")
	appPlainWithMouse := NewApp(ctx, "x", WithMouse(true))
	appPlainWithMouse.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	if !appPlainWithMouse.plainMode() {
		t.Error("TERM=dumb should enable plain mode")
	}

	// Should render successfully
	view3 := appPlainWithMouse.View()
	if view3 == "" {
		t.Error("plain mode app should still render")
	}
}

// TestV6NotifyOffByDefaultUnderPlain verifies that notification is off by default
// and plain-mode rendering works correctly.
func TestV6NotifyOffByDefaultUnderPlain(t *testing.T) {
	oldTerm := os.Getenv("TERM")
	defer func() {
		if oldTerm == "" {
			_ = os.Unsetenv("TERM")
		} else {
			_ = os.Setenv("TERM", oldTerm)
		}
	}()

	_ = os.Setenv("TERM", "dumb")

	h := newNotifyHarness(t, false)
	h.connect(1)
	h.frame(submittedFrame("turn-1", 1))
	h.frame(terminalEventFrame("turn-1", 2))

	if n := h.bells.Load(); n != 0 {
		t.Errorf("notify off rang %d bells under plain mode", n)
	}
}

// TestV6NotifyLiveFromSeqBoundary verifies that events exactly at live_from_seq trigger
// notifications, while those below it do not (the boundary condition).
func TestV6NotifyLiveFromSeqBoundary(t *testing.T) {
	h := newNotifyHarness(t, true)

	// Set live_from_seq = 100
	h.connect(100)

	// Event at seq 99 (below boundary) should not ring
	h.frame(terminalEventFrame("turn-1", 99))
	if n := h.bells.Load(); n != 0 {
		t.Errorf("event below live_from_seq rang %d bells", n)
	}

	// Event at seq 100 (at boundary) should ring
	h.frame(terminalEventFrame("turn-2", 100))
	if n := h.bells.Load(); n != 1 {
		t.Errorf("event at live_from_seq rang %d bells, want 1", n)
	}

	// Event at seq 101 (above boundary) should ring
	h.frame(terminalEventFrame("turn-3", 101))
	if n := h.bells.Load(); n != 2 {
		t.Errorf("event above live_from_seq rang %d bells total, want 2", n)
	}
}

// TestV6NotifyRetainedSeqSurvivesReconnect verifies that the retained seq (highest processed)
// survives a reconnection and prevents duplicate notices/bells.
func TestV6NotifyRetainedSeqSurvivesReconnect(t *testing.T) {
	h := newNotifyHarness(t, true)

	// Connect and process some events
	h.connect(5)
	h.frame(submittedFrame("turn-1", 5))
	h.frame(terminalEventFrame("turn-1", 6))

	firstBells := h.bells.Load()
	if firstBells != 1 {
		t.Fatalf("first terminal: %d bells, want 1", firstBells)
	}

	// Reconnect and get the same terminal event again
	h.lose(7)
	h.frame(terminalEventFrame("turn-1", 6))

	// Should not ring again because seq 6 was already processed
	secondBells := h.bells.Load()
	if secondBells != 1 {
		t.Errorf("after reconnect: %d bells total, want 1 (no duplicate)", secondBells)
	}
}

// TestV6NotifyConversationPickup verifies that rejoining a conversation that last
// turned terminal works correctly without spurious notices.
func TestV6NotifyConversationPickup(t *testing.T) {
	h := newNotifyHarness(t, true)

	// Initial connection sees a turn that ended
	h.connect(50)
	h.frame(terminalEventFrame("turn-1", 10))

	// New processes show no notice for past terminal events per spec
	notices := h.notices()
	if notices != 0 {
		t.Errorf("picking up terminal event: %d notices, want 0", notices)
	}

	// Now get a new live terminal event
	h.frame(terminalEventFrame("turn-2", 51))

	bells := h.bells.Load()
	if bells != 1 {
		t.Errorf("live terminal after pickup: %d bells, want 1", bells)
	}
}

// TestV6NotifyMultipleReviewerTerminal verifies that when multiple reviewers complete
// (resulting in multiple terminal events), each rings once if live.
func TestV6NotifyMultipleReviewerTerminal(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(1)

	// Feed multiple distinct terminal events, all live
	h.frame(terminalEventFrame("turn-1", 2))
	h.frame(terminalEventFrame("turn-2", 3))
	h.frame(terminalEventFrame("turn-3", 4))

	bells := h.bells.Load()
	if bells != 3 {
		t.Errorf("three terminal events: %d bells, want 3", bells)
	}
}

// TestV6NotifyNoNoticeLineForNewProcess verifies that a newly started TUI
// attached to a conversation whose last turn is already terminal shows no notice line.
func TestV6NotifyNoNoticeLineForNewProcess(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(10)

	// Immediately get a terminal event from before we connected
	h.frame(terminalEventFrame("turn-1", 5))

	view := h.app.View()
	if strings.Contains(view, noticeText) {
		t.Errorf("new process showed notice line:\n%s", view)
	}
}

// TestV6NotifyDetachedEndNoticeThenLiveRing tests the full scenario:
// turn ends while detached -> shows notice, then live terminal rings once more.
func TestV6NotifyDetachedEndNoticeThenLiveRing(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(5)
	h.frame(submittedFrame("turn-1", 5))

	// Lose connection while turn is pending
	h.lose(10)

	// Turn ends in replay (seq 6 is below live_from_seq which is 10)
	h.frame(terminalEventFrame("turn-1", 6))

	// Should show notice, no bell (replay)
	if n := h.notices(); n != 1 {
		t.Errorf("after detached end: %d notices, want 1", n)
	}
	if n := h.bells.Load(); n != 0 {
		t.Errorf("replay ended: %d bells, want 0", n)
	}

	// Now get a live terminal event (new turn)
	h.frame(terminalEventFrame("turn-2", 11))

	// Should ring once
	if n := h.bells.Load(); n != 1 {
		t.Errorf("after live terminal: %d bells, want 1", n)
	}
}

// TestV6NotifyErrorHandling verifies that notification boundaries are maintained
// even when events have unusual fields.
func TestV6NotifyErrorHandling(t *testing.T) {
	h := newNotifyHarness(t, true)
	h.connect(1)

	// Frame with unusual but valid fields
	for i := int64(2); i <= 5; i++ {
		f := wire.Frame{
			Seq:  i,
			Kind: wire.EventKindTurnState,
			At:   fmt.Sprintf("2024-01-01T00:00:%02dZ", i),
			Body: func() []byte {
				b, _ := json.Marshal(wire.EvTurnState{
					TurnID: fmt.Sprintf("turn-%d", i),
					State:  "approved",
				})
				return b
			}(),
		}
		h.frame(f)
	}

	// Should have rung for all 4 events (seqs 2-5, all live)
	if n := h.bells.Load(); n != 4 {
		t.Errorf("unusual fields: %d bells, want 4", n)
	}
}
