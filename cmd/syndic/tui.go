package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/tui"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

func cmdTUI(fs *flag.FlagSet, args []string) int {
	newConversation := fs.Bool("new", false, "start with a new conversation")
	conversationID := fs.String("conversation", "", "open a named conversation (accepts unique prefix)")
	roundsPtr := fs.Int("rounds", -1, "set the round limit for this process (0 to disable revisions)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Validate mutually exclusive flags
	if *newConversation && *conversationID != "" {
		fmt.Fprintf(os.Stderr, "error: --new and --conversation are mutually exclusive\n")
		return 2
	}

	// Validate rounds value: must be 0 or positive, or the -1 unset sentinel
	if *roundsPtr < -1 {
		fmt.Fprintf(os.Stderr, "error: --rounds must be a non-negative integer or omitted\n")
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

	// If --conversation is specified, validate the prefix before starting the TUI
	resolvedConvID := ""
	if *conversationID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conv, err := validateConversationPrefix(ctx, paths.Socket, *conversationID, wd)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 2
		}
		resolvedConvID = conv
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
	if resolvedConvID != "" {
		opts = append(opts, tui.WithConversationID(resolvedConvID))
	}
	if *roundsPtr >= 0 {
		opts = append(opts, tui.WithRounds(*roundsPtr))
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

// validateConversationPrefix checks if the conversation prefix is valid and
// if the conversation's workspace matches the current directory.
// Returns the resolved conversation ID or an error.
func validateConversationPrefix(ctx context.Context, socket, prefix, workspace string) (string, error) {
	cl, err := client.Dial(ctx, socket, "syndic-tui", "0")
	if err != nil {
		return "", fmt.Errorf("failed to connect to daemon: %v", err)
	}
	defer func() { _ = cl.Close() }()

	// Search for conversations matching the prefix
	var matches []string
	var matchedConv *wire.ConversationSummary
	cursor := ""
	for {
		reply, err := cl.Conversations(ctx, nil, cursor, 200)
		if err != nil {
			return "", fmt.Errorf("failed to list conversations: %v", err)
		}

		for i, conv := range reply.Items {
			if len(conv.ID) >= len(prefix) && conv.ID[:len(prefix)] == prefix {
				matches = append(matches, conv.ID)
				matchedConv = &reply.Items[i]
				// Stop early if we already have multiple matches
				if len(matches) > 1 {
					break
				}
			}
		}

		// Stop pagination if we found multiple matches
		if len(matches) > 1 {
			break
		}

		// Check if there are more conversations
		if reply.NextCursor == nil || *reply.NextCursor == "" {
			break
		}
		cursor = *reply.NextCursor
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("unknown conversation prefix: %s", prefix)
	}

	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous conversation prefix: %s", prefix)
	}

	// Check workspace match for the single matched conversation
	if matchedConv != nil && matchedConv.Workspace != nil && *matchedConv.Workspace != workspace {
		return "", fmt.Errorf("workspace mismatch: conversation is in workspace %s but current directory is %s", *matchedConv.Workspace, workspace)
	}

	return matches[0], nil
}
