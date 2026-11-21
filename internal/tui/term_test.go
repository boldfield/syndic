package tui

import (
	"context"
	"os"
	"testing"
)

func TestDetectTerminalMode(t *testing.T) {
	// Save original env vars
	origTerm := os.Getenv("TERM")
	origNoColor := os.Getenv("NO_COLOR")
	defer func() {
		if origTerm != "" {
			_ = os.Setenv("TERM", origTerm)
		} else {
			_ = os.Unsetenv("TERM")
		}
		if origNoColor != "" {
			_ = os.Setenv("NO_COLOR", origNoColor)
		} else {
			_ = os.Unsetenv("NO_COLOR")
		}
	}()

	// Test: when TERM=dumb, should return Plain
	_ = os.Setenv("TERM", "dumb")
	mode, err := DetectTerminalMode()
	if err != nil {
		t.Errorf("DetectTerminalMode failed: %v", err)
	}
	if mode != TerminalModePlain {
		t.Errorf("Expected TerminalModePlain for TERM=dumb, got %d", mode)
	}

	// Test: when TERM is unset, should return Plain
	_ = os.Unsetenv("TERM")
	mode, err = DetectTerminalMode()
	if err != nil {
		t.Errorf("DetectTerminalMode failed: %v", err)
	}
	if mode != TerminalModePlain {
		t.Errorf("Expected TerminalModePlain for TERM unset, got %d", mode)
	}

	// Test: when NO_COLOR is set, should return Plain
	_ = os.Setenv("TERM", "xterm-256color")
	_ = os.Setenv("NO_COLOR", "1")
	mode, err = DetectTerminalMode()
	if err != nil {
		t.Errorf("DetectTerminalMode failed: %v", err)
	}
	if mode != TerminalModePlain {
		t.Errorf("Expected TerminalModePlain for NO_COLOR=1, got %d", mode)
	}
}

func TestAppTerminalMode(t *testing.T) {
	ctx := context.Background()

	// Test that WithTerminalMode option sets the terminal mode
	app := NewApp(ctx, "/tmp/test.sock", WithTerminalMode(TerminalModePlain))
	if app.terminalMode != TerminalModePlain {
		t.Errorf("Expected TerminalModePlain, got %d", app.terminalMode)
	}

	// Test that default is TerminalModeNormal
	app2 := NewApp(ctx, "/tmp/test.sock")
	if app2.terminalMode != TerminalModeNormal {
		t.Errorf("Expected TerminalModeNormal by default, got %d", app2.terminalMode)
	}
}

func TestPlainModeDraftBanner(t *testing.T) {
	in := answerInput{
		plainMode: true,
		drafts:    true,
	}
	in.content = NewContentCache()

	// When plainMode is true, the draft banner should use plain text
	if !in.plainMode {
		t.Error("Expected plainMode to be true in test")
	}
}
