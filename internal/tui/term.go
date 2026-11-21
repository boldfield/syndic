package tui

import (
	"os"

	"golang.org/x/sys/unix"
)

// TerminalCapabilities describes the terminal's rendering capabilities.
type TerminalCapabilities struct {
	Color     bool // support for ANSI color sequences
	AltScreen bool // support for alternate screen buffer
}

// DetectTerminalCapabilities checks the terminal environment and returns the appropriate capabilities.
// NO_COLOR disables color only. TERM=dumb or TERM unset disables color and alternate screen.
func DetectTerminalCapabilities() TerminalCapabilities {
	term := os.Getenv("TERM")
	noColor := os.Getenv("NO_COLOR") != ""

	// TERM=dumb or unset: no color, no alt-screen
	if term == "" || term == "dumb" {
		return TerminalCapabilities{
			Color:     false,
			AltScreen: false,
		}
	}

	// NO_COLOR set: no color, but alt-screen is allowed
	if noColor {
		return TerminalCapabilities{
			Color:     false,
			AltScreen: true,
		}
	}

	// Normal terminal: full capabilities
	return TerminalCapabilities{
		Color:     true,
		AltScreen: true,
	}
}

// IsStdoutTTY checks if stdout is connected to a real terminal (not a file or pipe).
// Uses isatty to reliably detect TTY status.
func IsStdoutTTY() bool {
	return isatty(os.Stdout.Fd())
}

// isatty checks if a file descriptor is associated with a terminal.
// Uses ioctl to properly detect TTY status, avoiding false positives from other character devices.
func isatty(fd uintptr) bool {
	_, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	return err == nil
}
