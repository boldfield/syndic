package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"

	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

func cmdTUI(fs *flag.FlagSet, args []string) int {
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Check if stdout is a TTY; refuse to run if not
	if !tui.IsStdoutTTY() {
		fmt.Fprintf(os.Stderr, "error: standard output is not a terminal\n")
		return 2
	}

	// Get home directory
	u, err := user.Current()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to get home directory: %v\n", err)
		return 2
	}

	// Resolve socket path
	paths, err := lockfile.Resolve(os.Getenv, u.HomeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to resolve socket path: %v\n", err)
		return 2
	}

	// Detect terminal capabilities
	caps := tui.DetectTerminalCapabilities()

	// Create app
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := tui.NewApp(ctx, paths.Socket, tui.WithTerminalCapabilities(caps))

	tui.ApplyTerminalCapabilities(caps)

	// Run Bubble Tea
	p := tea.NewProgram(app, tui.ProgramOptions(caps)...)
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: TUI error: %v\n", err)
		return 2
	}

	if tuiApp, ok := finalModel.(*tui.App); ok {
		if msg := tui.ExitMessage(tuiApp); msg != "" {
			fmt.Println(msg)
		}
	}

	return 0
}
