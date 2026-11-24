package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

func cmdTUI(fs *flag.FlagSet, args []string) int {
	flags, err := parseStartupFlags(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
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

	// Get current working directory for validation
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to get working directory: %v\n", err)
		return 2
	}

	opts := []tui.Option{}
	selCtx, selCancel := context.WithTimeout(context.Background(), 5*time.Second)
	open, code := chooseConversation(selCtx, paths.Socket, flags, wd)
	selCancel()
	if code != 0 {
		return code
	}
	if open != nil {
		opts = append(opts, tui.WithConversation(open.id, open.turns))
	} else {
		// An empty conversation: other conversations' turns stay out of the chat.
		opts = append(opts, tui.WithConversation("", nil))
	}
	if flags.rounds.set {
		opts = append(opts, tui.WithRounds(flags.rounds.value))
	}
	opts = append(opts, flags.appOptions()...)

	// Detect terminal capabilities
	caps := tui.DetectTerminalCapabilities()

	// Create app
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts = append(opts, tui.WithTerminalCapabilities(caps))

	app := tui.NewApp(ctx, paths.Socket, opts...)

	tui.ApplyTerminalCapabilities(caps)

	// Run Bubble Tea with mouse support if enabled
	p := tea.NewProgram(app, flags.programOptions(caps)...)
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

// chooseConversation connects, selects the conversation to open and
// disconnects. A selection that cannot be made (unknown or ambiguous prefix,
// another workspace) exits 2; an engine that cannot be reached or answered exits 1.
func chooseConversation(ctx context.Context, socket string, f startupFlags, workspace string) (*openConversation, int) {
	if f.newConversation {
		return nil, 0
	}
	cl, err := client.Dial(ctx, socket, "syndic-tui", "0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: connect to the engine: %v\n", err)
		return nil, 1
	}
	defer func() { _ = cl.Close() }()
	open, err := selectConversation(ctx, cl, f, workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if errors.Is(err, errSelection) {
			return nil, 2
		}
		return nil, 1
	}
	return open, 0
}
