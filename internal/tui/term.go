package tui

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
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

// ProgramOptions returns the Bubble Tea options the capabilities allow. The
// alternate screen is used only when the terminal has one; mouse reporting is
// enabled only when explicitly requested and not suppressed by terminal mode.
func ProgramOptions(caps TerminalCapabilities) []tea.ProgramOption {
	var opts []tea.ProgramOption
	if caps.AltScreen {
		opts = append(opts, tea.WithAltScreen())
	}
	return opts
}

// ProgramOptionsWithMouse returns Bubble Tea options including mouse support when enabled.
func ProgramOptionsWithMouse(caps TerminalCapabilities, enableMouse bool) []tea.ProgramOption {
	opts := ProgramOptions(caps)
	if enableMouse && caps.Color {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	return opts
}

// ApplyTerminalCapabilities makes every style render without colour or other
// escape sequences when the terminal has no colour, whatever lipgloss would
// have detected on its own.
func ApplyTerminalCapabilities(caps TerminalCapabilities) {
	if !caps.Color {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}
