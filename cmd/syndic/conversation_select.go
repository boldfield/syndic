package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strconv"

	"github.com/boldfield/syndic/internal/wire"
)

// maxRoundsLimit is the most revisions any turn may have (v5 delta section 4).
const maxRoundsLimit = 5

// errSelection marks a conversation the user named that cannot be opened:
// unknown, ambiguous, or in another workspace. The command exits 2 for it.
var errSelection = errors.New("cannot select that conversation")

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
	notify          bool
	mouse           bool
}

// parseStartupFlags parses --new, --conversation, --rounds, --notify and --mouse. A returned
// error is a usage error: the command exits 2 before it connects to anything.
func parseStartupFlags(fs *flag.FlagSet, args []string) (startupFlags, error) {
	var f startupFlags
	fs.BoolVar(&f.newConversation, "new", false, "start with an empty conversation")
	fs.StringVar(&f.conversation, "conversation", "", "open the conversation with this id, or any unique prefix of it")
	fs.Var(&f.rounds, "rounds", "revisions allowed per turn in this process, 0 to 5")
	fs.BoolVar(&f.notify, "notify", false, "ring the terminal on a live terminal event")
	fs.BoolVar(&f.mouse, "mouse", false, "enable mouse input for tabs and drawer")
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() > 0 {
		return f, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if f.newConversation && f.conversation != "" {
		return f, fmt.Errorf("--new and --conversation are mutually exclusive")
	}
	return f, nil
}

// openConversation is the conversation a start-up selected.
type openConversation struct {
	id    string
	turns []string // turn ids, oldest first
}

func turnIDs(c wire.ConversationReply) []string {
	ids := make([]string, 0, len(c.Turns))
	for _, t := range c.Turns {
		ids = append(ids, t.ID)
	}
	return ids
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
		var we *wire.Error
		if errors.As(err, &we) && (we.Code == wire.ErrorCodeNotFound || we.Code == wire.ErrorCodeAmbiguous) {
			return nil, fmt.Errorf("conversation %q: %s: %w", prefix, we.Code, errSelection)
		}
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
		return nil, fmt.Errorf("conversation %q: unknown: %w", prefix, errSelection)
	}
	if found.Workspace != nil && *found.Workspace != workspace {
		return nil, fmt.Errorf("conversation %s belongs to %s, not %s: %w", conv.ID, *found.Workspace, workspace, errSelection)
	}
	return &openConversation{id: conv.ID, turns: turnIDs(conv)}, nil
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
	return &openConversation{id: conv.ID, turns: turnIDs(conv)}, nil
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
