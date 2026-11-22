package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/wire"
)

// noWorkspace stands for the null workspace of a conversation journaled
// before workspaces existed.
const noWorkspace = "(no workspace)"

func cmdConversations(fs *flag.FlagSet, args []string) int {
	all := fs.Bool("all", false, "list every conversation with its workspace, not only those of the current directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	u, err := user.Current()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to get home directory: %v\n", err)
		return 1
	}
	paths, err := lockfile.Resolve(os.Getenv, u.HomeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to resolve socket path: %v\n", err)
		return 1
	}

	var workspace *string
	if !*all {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: failed to get working directory: %v\n", err)
			return 1
		}
		workspace = &wd
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl, err := client.Dial(ctx, paths.Socket, "syndic-conversations", "0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to connect to the engine: %v\n", err)
		return 1
	}
	defer func() { _ = cl.Close() }()

	if err := listConversations(ctx, cl, workspace, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// listConversations prints one line per conversation, latest activity first,
// following every page: id, last activity, turn count, workspace, preview.
func listConversations(ctx context.Context, api conversationAPI, workspace *string, w io.Writer) error {
	return eachConversation(ctx, api, workspace, func(c wire.ConversationSummary) bool {
		ws := noWorkspace
		if c.Workspace != nil {
			ws = *c.Workspace
		}
		running := ""
		if c.Running {
			running = " running"
		}
		_, _ = fmt.Fprintf(w, "%s  %s  %d turns%s  %s  %s\n", c.ID, c.LastSubmittedAt, c.TurnCount, running, ws, c.Preview)
		return true
	})
}
