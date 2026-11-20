package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// commandHandler runs a command. It reports a problem by setting a.refusal.
type commandHandler func(a *App, args []string) tea.Cmd

var commands = map[string]commandHandler{
	"quit":   cmdQuit,
	"resume": cmdResume,
	"drafts": cmdDrafts,
}

// isCommand reports whether the input line is a command rather than a prompt.
func isCommand(line string) bool { return strings.HasPrefix(line, "/") }

// dispatch runs the command named by line, which starts with "/".
func dispatch(a *App, line string) tea.Cmd {
	fields := strings.Fields(line)
	name := "/"
	if len(fields) > 0 {
		name = fields[0]
		fields = fields[1:]
	}
	h, ok := commands[strings.TrimPrefix(name, "/")]
	if !ok {
		a.refusal = "unknown command: " + name
		return nil
	}
	return h(a, fields)
}

func cmdQuit(a *App, _ []string) tea.Cmd { return a.quit() }

// cmdResume forces the same reconnect a lost connection gets.
func cmdResume(a *App, _ []string) tea.Cmd { return a.reconnect() }

// cmdDrafts sets the drafts toggle.
func cmdDrafts(a *App, args []string) tea.Cmd {
	if len(args) != 1 {
		a.refusal = "usage: /drafts on|off"
		return nil
	}
	switch args[0] {
	case "on":
		a.SetDraftsEnabled(true)
	case "off":
		a.SetDraftsEnabled(false)
	default:
		a.refusal = "usage: /drafts on|off"
	}
	return nil
}
