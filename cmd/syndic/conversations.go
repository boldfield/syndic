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
)

func cmdConversations(fs *flag.FlagSet, args []string) int {
	all := fs.Bool("all", false, "list all conversations, not just those for the current workspace")

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

	// Connect to daemon
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cl, err := client.Dial(ctx, paths.Socket, "syndic-conversations", "0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to connect to daemon: %v\n", err)
		return 2
	}
	defer func() { _ = cl.Close() }()

	// Get current working directory
	var workspace *string
	if !*all {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to get working directory: %v\n", err)
			return 2
		}
		workspace = &wd
	}

	// List conversations with pagination
	cursor := ""
	for {
		reply, err := cl.Conversations(ctx, workspace, cursor, 200)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to list conversations: %v\n", err)
			return 2
		}

		for _, conv := range reply.Items {
			// Print conversation with workspace
			fmt.Printf("%s  %s  %d turns", conv.ID, conv.LastSubmittedAt, conv.TurnCount)
			if conv.Workspace != nil {
				fmt.Printf("  %s", *conv.Workspace)
			} else {
				fmt.Printf("  (no workspace)")
			}
			if conv.Preview != "" {
				fmt.Printf("  %s", conv.Preview)
			}
			fmt.Println()
		}

		// Check if there are more conversations
		if reply.NextCursor == nil || *reply.NextCursor == "" {
			break
		}
		cursor = *reply.NextCursor
	}

	return 0
}
