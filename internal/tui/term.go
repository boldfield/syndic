package tui

import (
	"os"
)

// TerminalMode describes how the terminal should be rendered.
type TerminalMode int

const (
	TerminalModeNormal TerminalMode = iota
	TerminalModePlain               // TERM=dumb, TERM unset, NO_COLOR set, or stdout not a TTY
)

// DetectTerminalMode checks the terminal environment and returns the appropriate mode.
// It returns TerminalModePlain if:
// - stdout is not a TTY
// - TERM=dumb or TERM is unset
// - NO_COLOR environment variable is set
func DetectTerminalMode() (TerminalMode, error) {
	// Check if stdout is a TTY
	if !isStdoutTTY() {
		return TerminalModePlain, nil
	}

	// Check TERM environment variable
	term := os.Getenv("TERM")
	if term == "" || term == "dumb" {
		return TerminalModePlain, nil
	}

	// Check NO_COLOR environment variable
	if os.Getenv("NO_COLOR") != "" {
		return TerminalModePlain, nil
	}

	return TerminalModeNormal, nil
}

// isStdoutTTY checks if stdout is connected to a terminal.
func isStdoutTTY() bool {
	stat, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	// Check if stdout is a character device (TTY)
	return (stat.Mode() & os.ModeCharDevice) != 0
}
