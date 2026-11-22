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
	newConversation := fs.Bool("new", false, "start with a new conversation")
	conversationID := fs.String("conversation", "", "open a named conversation (accepts unique prefix)")
	rounds := fs.Int("rounds", 0, "set the round limit for this process")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Validate mutually exclusive flags
	if *newConversation && *conversationID != "" {
		fmt.Fprintf(os.Stderr, "error: --new and --conversation are mutually exclusive\n")
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

	opts := []tui.Option{tui.WithTerminalCapabilities(caps)}

	if *newConversation {
		opts = append(opts, tui.WithNewConversation())
	}
	if *conversationID != "" {
		opts = append(opts, tui.WithConversationID(*conversationID))
	}
	if *rounds > 0 {
		opts = append(opts, tui.WithRounds(*rounds))
	}

	app := tui.NewApp(ctx, paths.Socket, opts...)

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
