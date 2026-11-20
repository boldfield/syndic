package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// CommandHandler handles a command and returns a tea.Cmd and an optional error message.
type CommandHandler func(a *App, args []string) (tea.Cmd, string)

// CommandTable maps command names to their handlers.
var CommandTable = map[string]CommandHandler{
	"/quit":   handleQuit,
	"/resume": handleResume,
}

// dispatchCommand routes a command to its handler.
func dispatchCommand(a *App, input string) tea.Cmd {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return nil
	}

	cmdName := parts[0]
	handler, exists := CommandTable[cmdName]
	if !exists {
		a.refusal = fmt.Sprintf("unknown command: %s", cmdName)
		a.inputLine = ""
		return nil
	}

	args := parts[1:]
	cmd, errMsg := handler(a, args)
	if errMsg != "" {
		a.refusal = errMsg
		a.inputLine = ""
	}
	return cmd
}

// handleQuit handles the /quit command.
func handleQuit(a *App, args []string) (tea.Cmd, string) {
	a.inputLine = ""
	return tea.Quit, ""
}

// handleResume handles the /resume command - forces reconnect from last seq.
func handleResume(a *App, args []string) (tea.Cmd, string) {
	a.inputLine = ""
	return a.connector.reconnectCmd(a.ctx), ""
}
