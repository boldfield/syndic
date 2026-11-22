package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"

	"github.com/boldfield/syndic/internal/wire"
)

// maxRoundsLimit is the most revisions any turn may have (v5 delta section 4).
const maxRoundsLimit = 5

// conversationAPI is the part of the engine client that choosing a conversation uses.
type conversationAPI interface {
	Conversations(ctx context.Context, workspace *string, cursor string, limit int) (wire.ConversationsReply, error)
	Conversation(ctx context.Context, idPrefix string) (wire.ConversationReply, error)
}

// roundsFlag is --rounds. It records whether the flag was given, so that an
// explicit 0, which means no revisions, is told apart from an absent flag.
type roundsFlag struct {
	set   bool
	value int
}

func (r *roundsFlag) String() string {
	if r == nil || !r.set {
		return ""
	}
	return strconv.Itoa(r.value)
}

func (r *roundsFlag) Set(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("%q is not an integer", s)
	}
	if n < 0 || n > maxRoundsLimit {
		return fmt.Errorf("%d is outside 0 to %d", n, maxRoundsLimit)
	}
	r.set, r.value = true, n
	return nil
}

// startupFlags are the conversation flags of the interactive command.
type startupFlags struct {
	newConversation bool
	conversation    string
	rounds          roundsFlag
}

// parseStartupFlags parses --new, --conversation and --rounds. A returned
// error is a usage error: the command exits 2 before it connects to anything.
func parseStartupFlags(fs *flag.FlagSet, args []string) (startupFlags, error) {
	var f startupFlags
	fs.BoolVar(&f.newConversation, "new", false, "start with an empty conversation")
	fs.StringVar(&f.conversation, "conversation", "", "open the conversation with this id, or any unique prefix of it")
	fs.Var(&f.rounds, "rounds", "revisions allowed per turn in this process, 0 to 5")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if f.newConversation && f.conversation != "" {
		return f, fmt.Errorf("--new and --conversation are mutually exclusive")
	}
	return f, nil
}

// openConversation is the conversation a start-up selected.
type openConversation struct {
	id    string
	turns int
}

// selectConversation chooses the conversation to open for workspace, and
// loads its turn list. It returns nil for a start with an empty conversation:
// --new, or no conversation for this workspace. Nothing is submitted.
//
// An unknown or ambiguous prefix, or a conversation whose workspace is another
// directory, is a usage error. A null workspace matches any directory, and a
// null-workspace conversation is never opened by default.
func selectConversation(ctx context.Context, api conversationAPI, f startupFlags, workspace string) (*openConversation, error) {
	if f.newConversation {
		return nil, nil
	}
	if f.conversation != "" {
		return selectByPrefix(ctx, api, f.conversation, workspace)
	}
	return selectLatest(ctx, api, workspace)
}

func selectByPrefix(ctx context.Context, api conversationAPI, prefix, workspace string) (*openConversation, error) {
	conv, err := api.Conversation(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("conversation %q: %w", prefix, err)
	}
	var found *wire.ConversationSummary
	err = eachConversation(ctx, api, nil, func(c wire.ConversationSummary) bool {
		if c.ID == conv.ID {
			found = &c
			return false
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("conversation %q: unknown", prefix)
	}
	if found.Workspace != nil && *found.Workspace != workspace {
		return nil, fmt.Errorf("conversation %s belongs to %s, not %s", conv.ID, *found.Workspace, workspace)
	}
	return &openConversation{id: conv.ID, turns: len(conv.Turns)}, nil
}

// selectLatest opens the workspace's conversation with the most recent
// submit, which the engine lists first.
func selectLatest(ctx context.Context, api conversationAPI, workspace string) (*openConversation, error) {
	var latest string
	err := eachConversation(ctx, api, &workspace, func(c wire.ConversationSummary) bool {
		if c.Workspace == nil || *c.Workspace != workspace {
			return true
		}
		latest = c.ID
		return false
	})
	if err != nil || latest == "" {
		return nil, err
	}
	conv, err := api.Conversation(ctx, latest)
	if err != nil {
		return nil, fmt.Errorf("load conversation %s: %w", latest, err)
	}
	return &openConversation{id: conv.ID, turns: len(conv.Turns)}, nil
}

// eachConversation calls visit for every listed conversation, following
// next_cursor, until visit returns false or the list ends.
func eachConversation(ctx context.Context, api conversationAPI, workspace *string, visit func(wire.ConversationSummary) bool) error {
	cursor := ""
	for {
		reply, err := api.Conversations(ctx, workspace, cursor, 200)
		if err != nil {
			return fmt.Errorf("list conversations: %w", err)
		}
		for _, c := range reply.Items {
			if !visit(c) {
				return nil
			}
		}
		if reply.NextCursor == nil || *reply.NextCursor == "" {
			return nil
		}
		cursor = *reply.NextCursor
	}
}
