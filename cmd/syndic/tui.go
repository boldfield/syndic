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

	// Create app
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	app := tui.NewApp(ctx, paths.Socket)

	// Run Bubble Tea
	p := tea.NewProgram(app, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: TUI error: %v\n", err)
		return 2
	}

	// Print exit message if there's an open turn
	if tuiApp, ok := finalModel.(*tui.App); ok {
		if tuiApp.CurrentTurn() != "" {
			fmt.Printf("Turn %s is still running and will continue\n", tuiApp.CurrentTurn())
		}
	}

	return 0
}
