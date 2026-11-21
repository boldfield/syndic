package tui

import (
	"os"
	"syscall"
)

// TerminalCapabilities describes the terminal's rendering capabilities.
type TerminalCapabilities struct {
	Color     bool // support for ANSI color sequences
	AltScreen bool // support for alternate screen buffer
	Mouse     bool // support for mouse reporting
}

// DetectTerminalCapabilities checks the terminal environment and returns the appropriate capabilities.
// NO_COLOR disables color only. TERM=dumb or TERM unset disables color and alternate screen/mouse.
func DetectTerminalCapabilities() TerminalCapabilities {
	term := os.Getenv("TERM")
	noColor := os.Getenv("NO_COLOR") != ""

	// TERM=dumb or unset: no color, no alt-screen, no mouse
	if term == "" || term == "dumb" {
		return TerminalCapabilities{
			Color:     false,
			AltScreen: false,
			Mouse:     false,
		}
	}

	// NO_COLOR set: no color, but alt-screen and mouse are allowed
	if noColor {
		return TerminalCapabilities{
			Color:     false,
			AltScreen: true,
			Mouse:     true,
		}
	}

	// Normal terminal: full capabilities
	return TerminalCapabilities{
		Color:     true,
		AltScreen: true,
		Mouse:     true,
	}
}

// IsStdoutTTY checks if stdout is connected to a real terminal (not a file or pipe).
// Uses isatty to reliably detect TTY status.
func IsStdoutTTY() bool {
	return isatty(os.Stdout.Fd())
}

// isatty checks if a file descriptor is associated with a terminal.
// This is more reliable than checking for ModeCharDevice which can accept other character devices.
func isatty(fd uintptr) bool {
	var stat syscall.Stat_t
	err := syscall.Fstat(int(fd), &stat)
	if err != nil {
		return false
	}
	// S_IFCHR = 0x2000 (character device)
	return (stat.Mode & 0xf000) == 0x2000
}
