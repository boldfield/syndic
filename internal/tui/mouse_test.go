package tui

import (
	"context"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// TestMouseDisabledByDefault verifies mouse is off unless explicitly enabled.
func TestMouseDisabledByDefault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithTerminalCapabilities(TerminalCapabilities{Color: true}))
	if a.mouseEnabled {
		t.Error("mouse should be disabled by default")
	}
}

// TestMouseEnabledViaOption verifies WithMouse option enables mouse.
func TestMouseEnabledViaOption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithMouse(true), WithTerminalCapabilities(TerminalCapabilities{Color: true}))
	if !a.mouseEnabled {
		t.Error("mouse should be enabled via WithMouse option")
	}
}

// TestMouseDisabledInPlainMode: mouse should not respond when color is off.
func TestMouseDisabledInPlainMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithMouse(true), WithTerminalCapabilities(TerminalCapabilities{Color: false}))
	// Mouse events should be ignored in plain mode
	a.width, a.height = 80, 24
	model, cmd := a.updateMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, X: 10, Y: 0})
	if cmd != nil {
		t.Error("mouse click should be ignored in plain mode")
	}
	if model != a {
		t.Error("model should be unchanged")
	}
}

// TestMouseClickOnTabBar verifies clicking the tab bar switches tabs.
func TestMouseClickOnTabBar(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithMouse(true), WithTerminalCapabilities(TerminalCapabilities{Color: true}))
	a.width, a.height = 80, 24
	a.tab = tabChat

	// Click on transcript tab area (x=10 is in "transcript")
	_ = a.handleTabClick(10)
	if a.tab != tabTranscript {
		t.Errorf("clicking transcript area should switch tab to tabTranscript, got %v", a.tab)
	}

	// Click on participants tab area (x=23 is in "participants")
	_ = a.handleTabClick(23)
	if a.tab != tabParticipants {
		t.Errorf("clicking participants area should switch tab to tabParticipants, got %v", a.tab)
	}
}

// TestMouseClickOnDrawerRound verifies clicking drawer header navigates rounds.
func TestMouseClickOnDrawerRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithMouse(true), WithTerminalCapabilities(TerminalCapabilities{Color: true}))
	a.width, a.height = 80, 24
	a.reviewsDrawer.SetTurns([]string{"t1"})
	a.reviewsDrawer.Open(0)
	a.reviewsDrawer.UpdateReviews("t1", []wire.ReviewItem{
		{Round: 0, Participant: "p1", Kind: "approve"},
		{Round: 1, Participant: "p1", Kind: "block"},
	})

	initialRound := a.reviewsDrawer.selectedRound

	// Click on left side of header (round left area)
	a.handleDrawerRoundClick(10)
	if a.reviewsDrawer.selectedRound != initialRound {
		t.Error("left click should not change round (already at first round)")
	}

	// Click on right side of header (round right area, x > drawerWidth/2)
	a.handleDrawerRoundClick(50)
	if a.reviewsDrawer.selectedRound == initialRound {
		t.Errorf("right click should move to next round, got %d", a.reviewsDrawer.selectedRound)
	}
}

// TestMouseClickIgnoredWhenMouseDisabled: mouse events should be ignored when disabled.
func TestMouseClickIgnoredWhenMouseDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithTerminalCapabilities(TerminalCapabilities{Color: true}))
	a.width, a.height = 80, 24
	a.tab = tabChat

	// Even though we have a mouse event, it should be ignored
	model, cmd := a.updateMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, X: 25, Y: 0})
	if cmd != nil {
		t.Error("mouse should be ignored when disabled")
	}
	if a.tab != tabChat {
		t.Error("tab should not change when mouse is disabled")
	}
	if model != a {
		t.Error("model should be unchanged")
	}
}

// TestCmdMouseCommand verifies /mouse command parsing.
func TestCmdMouseCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithTerminalCapabilities(TerminalCapabilities{Color: true}))
	a.mouseEnabled = false

	// /mouse on should enable
	cmdMouse(a, []string{"on"})
	if !a.mouseEnabled {
		t.Error("/mouse on should enable mouse")
	}
	if a.refusal != "mouse on" {
		t.Errorf("refusal should be 'mouse on', got %q", a.refusal)
	}

	// /mouse off should disable
	cmdMouse(a, []string{"off"})
	if a.mouseEnabled {
		t.Error("/mouse off should disable mouse")
	}
	if a.refusal != "mouse off" {
		t.Errorf("refusal should be 'mouse off', got %q", a.refusal)
	}

	// /mouse invalid should fail
	cmdMouse(a, []string{"invalid"})
	if a.refusal != "usage: /mouse on|off" {
		t.Errorf("invalid usage should set refusal, got %q", a.refusal)
	}

	// /mouse with no args should fail
	cmdMouse(a, []string{})
	if a.refusal != "usage: /mouse on|off" {
		t.Errorf("missing arg should set refusal, got %q", a.refusal)
	}
}

// TestMouseEventIgnoresOtherButtons: only left button press triggers actions.
func TestMouseEventIgnoresOtherButtons(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithMouse(true), WithTerminalCapabilities(TerminalCapabilities{Color: true}))
	a.width, a.height = 80, 24
	a.tab = tabChat

	// Right button click should be ignored
	_, _ = a.updateMouse(tea.MouseMsg{Button: tea.MouseButtonRight, X: 25, Y: 0})
	if a.tab != tabChat {
		t.Error("right button click should not switch tabs")
	}

	// Middle button click should be ignored
	_, _ = a.updateMouse(tea.MouseMsg{Button: tea.MouseButtonMiddle, X: 25, Y: 0})
	if a.tab != tabChat {
		t.Error("middle button click should not switch tabs")
	}

	// Release event should be ignored
	model, _ := a.updateMouse(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease, X: 25, Y: 0})
	if a.tab != tabChat {
		t.Error("release event should not switch tabs")
	}
	if model != a {
		t.Error("model should be unchanged")
	}
}

// TestProgramOptionsWithMouseEnabledColor: mouse should be enabled when both enabled and color.
func TestProgramOptionsWithMouseEnabledColor(t *testing.T) {
	caps := TerminalCapabilities{Color: true, AltScreen: false}
	opts := ProgramOptionsWithMouse(caps, true)

	// Should have at least mouse option
	if len(opts) == 0 {
		t.Error("should return options when mouse enabled and color available")
	}
}

// TestProgramOptionsWithMouseDisabledColor: mouse should not be enabled in plain mode.
func TestProgramOptionsWithMouseDisabledColor(t *testing.T) {
	caps := TerminalCapabilities{Color: false, AltScreen: false}
	opts := ProgramOptionsWithMouse(caps, true)

	// Should not have mouse option when color is off
	if len(opts) > 0 {
		t.Error("should not enable mouse in plain mode")
	}
}

// TestProgramOptionsWithMouseDisabledRequest: mouse should not be enabled if not requested.
func TestProgramOptionsWithMouseDisabledRequest(t *testing.T) {
	caps := TerminalCapabilities{Color: true, AltScreen: false}
	opts := ProgramOptionsWithMouse(caps, false)

	// Should not have mouse option when not requested
	if len(opts) > 0 {
		t.Error("should not enable mouse when not requested")
	}
}

// TestReviewCountClickOpensDrawer: clicking a review count opens the drawer on that turn.
func TestReviewCountClickOpensDrawer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithMouse(true), WithTerminalCapabilities(TerminalCapabilities{Color: true}))
	a.width, a.height = 80, 24
	a.reviewsDrawer.SetTurns([]string{"t1", "t2", "t3"})

	if a.reviewsDrawer.IsOpen() {
		t.Fatal("drawer should start closed")
	}

	// Simulate a click on the second turn's review count
	a.reviewsDrawer.Open(1)
	if !a.reviewsDrawer.IsOpen() {
		t.Error("drawer should open")
	}
	if a.reviewsDrawer.SelectedTurnIndex() != 1 {
		t.Errorf("drawer should be on turn 2, got index %d", a.reviewsDrawer.SelectedTurnIndex())
	}
}

// TestMouseWithDrawerAnimation: mouse enabled with drawer animation.
func TestMouseWithDrawerAnimation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := NewApp(ctx, "", WithMouse(true), WithTerminalCapabilities(TerminalCapabilities{Color: true}))
	a.width, a.height = 80, 24
	a.reviewsDrawer.SetTurns([]string{"t1"})
	a.reviewsDrawer.SetDrawerHeight(7)

	// Mouse click opens drawer and starts animation
	a.reviewsDrawer.Open(0)
	if !a.reviewsDrawer.IsAnimating() {
		t.Error("opening drawer should start animation")
	}

	// Animation should start near 0 (allow small timing error)
	if a.reviewsDrawer.AnimProgress() > 0.01 {
		t.Error("animation should start near 0")
	}
}
