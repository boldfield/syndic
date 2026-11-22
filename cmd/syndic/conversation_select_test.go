package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
)

// fakeAPI is an engine's conversation list and turn lists, paged by pageSize.
type fakeAPI struct {
	convs    []wire.ConversationSummary // latest activity first
	turns    map[string]int
	pageSize int
	err      error
	calls    []*string // the workspace filter of each list request
}

func (f *fakeAPI) Conversations(_ context.Context, workspace *string, cursor string, _ int) (wire.ConversationsReply, error) {
	f.calls = append(f.calls, workspace)
	if f.err != nil {
		return wire.ConversationsReply{}, f.err
	}
	var items []wire.ConversationSummary
	for _, c := range f.convs {
		if workspace == nil || c.Workspace == nil || *c.Workspace == *workspace {
			items = append(items, c)
		}
	}
	start := 0
	if cursor != "" {
		for i, c := range items {
			if c.ID == cursor {
				start = i + 1
			}
		}
	}
	end := len(items)
	reply := wire.ConversationsReply{}
	if f.pageSize > 0 && start+f.pageSize < end {
		end = start + f.pageSize
		next := items[end-1].ID
		reply.NextCursor = &next
	}
	reply.Items = items[start:end]
	return reply, nil
}

func (f *fakeAPI) Conversation(_ context.Context, prefix string) (wire.ConversationReply, error) {
	var match []string
	for _, c := range f.convs {
		if strings.HasPrefix(c.ID, prefix) {
			match = append(match, c.ID)
		}
	}
	switch len(match) {
	case 0:
		return wire.ConversationReply{}, errors.New("unknown conversation")
	case 1:
		return wire.ConversationReply{ID: match[0], Turns: make([]wire.ConversationTurnSummary, f.turns[match[0]])}, nil
	}
	return wire.ConversationReply{}, errors.New("ambiguous conversation prefix")
}

func conv(id string, workspace *string) wire.ConversationSummary {
	return wire.ConversationSummary{ID: id, Workspace: workspace, LastSubmittedAt: "2026-01-01T00:00:00Z", TurnCount: 2, Preview: "p-" + id}
}

func ws(s string) *string { return &s }

func TestParseStartupFlags(t *testing.T) {
	parse := func(args ...string) (startupFlags, error) {
		fs := flag.NewFlagSet("tui", flag.ContinueOnError)
		fs.SetOutput(&bytes.Buffer{})
		return parseStartupFlags(fs, args)
	}
	if _, err := parse("--new", "--conversation", "ab"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("--new with --conversation: err = %v, want mutually exclusive", err)
	}
	for _, bad := range []string{"-1", "-3", "6", "x"} {
		if _, err := parse("--rounds", bad); err == nil {
			t.Errorf("--rounds %s accepted", bad)
		}
	}
	f, err := parse("--rounds", "0")
	if err != nil || !f.rounds.set || f.rounds.value != 0 {
		t.Errorf("--rounds 0: %+v, %v; want set to 0", f.rounds, err)
	}
	if f, err = parse(); err != nil || f.rounds.set {
		t.Errorf("no flags: %+v, %v; want rounds unset", f.rounds, err)
	}
	if f, err = parse("--rounds", "5", "--conversation", "ab"); err != nil || f.rounds.value != 5 || f.conversation != "ab" {
		t.Errorf("--rounds 5 --conversation ab: %+v, %v", f, err)
	}
}

// cmdTUI exits 2 on bad flags before it connects to anything.
func TestCmdTUIUsageErrorsExit2(t *testing.T) {
	oldErr := os.Stderr
	f, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = f
	t.Cleanup(func() { os.Stderr = oldErr; _ = f.Close() })
	for _, args := range [][]string{{"--new", "--conversation", "x"}, {"--rounds", "-1"}, {"--rounds", "9"}} {
		if code := cmdTUI(flag.NewFlagSet("tui", flag.ContinueOnError), args); code != 2 {
			t.Errorf("cmdTUI %v = %d, want 2", args, code)
		}
	}
}

func TestSelectNewOpensNothing(t *testing.T) {
	api := &fakeAPI{convs: []wire.ConversationSummary{conv("aaa", ws("/w"))}}
	open, err := selectConversation(context.Background(), api, startupFlags{newConversation: true}, "/w")
	if open != nil || err != nil || len(api.calls) != 0 {
		t.Errorf("--new: open=%v err=%v calls=%d, want nothing and no request", open, err, len(api.calls))
	}
}

func TestSelectLatestForTheWorkspace(t *testing.T) {
	api := &fakeAPI{
		convs: []wire.ConversationSummary{conv("legacy", nil), conv("other", ws("/x")), conv("mine2", ws("/w")), conv("mine1", ws("/w"))},
		turns: map[string]int{"mine2": 4},
	}
	open, err := selectConversation(context.Background(), api, startupFlags{}, "/w")
	if err != nil || open == nil || open.id != "mine2" || open.turns != 4 {
		t.Fatalf("default: %+v, %v; want mine2 with 4 turns (never the null-workspace one)", open, err)
	}
	if len(api.calls) == 0 || api.calls[0] == nil || *api.calls[0] != "/w" {
		t.Errorf("the list was not filtered to the workspace: %v", api.calls)
	}
}

func TestSelectLatestFollowsPagesAndSkipsNullWorkspace(t *testing.T) {
	api := &fakeAPI{
		convs:    []wire.ConversationSummary{conv("l1", nil), conv("l2", nil), conv("mine", ws("/w"))},
		pageSize: 1,
	}
	open, err := selectConversation(context.Background(), api, startupFlags{}, "/w")
	if err != nil || open == nil || open.id != "mine" {
		t.Fatalf("got %+v, %v; want mine from the third page", open, err)
	}
}

func TestSelectLatestWithNoneStartsEmpty(t *testing.T) {
	api := &fakeAPI{convs: []wire.ConversationSummary{conv("legacy", nil), conv("other", ws("/x"))}}
	open, err := selectConversation(context.Background(), api, startupFlags{}, "/w")
	if open != nil || err != nil {
		t.Errorf("got %+v, %v; want an empty start", open, err)
	}
}

func TestSelectLatestReportsEngineError(t *testing.T) {
	api := &fakeAPI{err: errors.New("boom")}
	if _, err := selectConversation(context.Background(), api, startupFlags{}, "/w"); err == nil {
		t.Error("a failed list was swallowed")
	}
}

func TestSelectByPrefix(t *testing.T) {
	api := &fakeAPI{
		convs: []wire.ConversationSummary{conv("abc1", ws("/w")), conv("abc2", ws("/w")), conv("xyz1", ws("/w")), conv("far1", ws("/x")), conv("old1", nil)},
		turns: map[string]int{"xyz1": 7, "old1": 1},
	}
	sel := func(prefix string) (*openConversation, error) {
		return selectConversation(context.Background(), api, startupFlags{conversation: prefix}, "/w")
	}
	if open, err := sel("xy"); err != nil || open.id != "xyz1" || open.turns != 7 {
		t.Errorf("unique prefix: %+v, %v", open, err)
	}
	if _, err := sel("abc"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous prefix: err = %v", err)
	}
	if _, err := sel("nope"); err == nil {
		t.Error("unknown prefix accepted")
	}
	if _, err := sel("far"); err == nil || !strings.Contains(err.Error(), "/x") {
		t.Errorf("workspace mismatch: err = %v, want a refusal naming /x", err)
	}
	// A null-workspace conversation opens from any directory; the first submit adopts it.
	if open, err := sel("old"); err != nil || open.id != "old1" {
		t.Errorf("legacy conversation: %+v, %v", open, err)
	}
}

func TestListConversationsPrintsWorkspaceOfEveryOne(t *testing.T) {
	api := &fakeAPI{
		convs:    []wire.ConversationSummary{conv("c1", ws("/a")), conv("c2", nil), conv("c3", ws("/b"))},
		pageSize: 2,
	}
	var out bytes.Buffer
	if err := listConversations(context.Background(), api, nil, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines, want all 3 conversations across pages:\n%s", len(lines), out.String())
	}
	for i, want := range []string{"c1  2026-01-01T00:00:00Z  2 turns  /a  p-c1", "c2  2026-01-01T00:00:00Z  2 turns  (no workspace)  p-c2", "c3"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to contain %q", i, lines[i], want)
		}
	}
	if !strings.Contains(lines[2], "/b") {
		t.Errorf("line 3 = %q, want workspace /b", lines[2])
	}
}

func TestListConversationsFiltersToWorkspace(t *testing.T) {
	api := &fakeAPI{convs: []wire.ConversationSummary{conv("c1", ws("/a")), conv("c3", ws("/b"))}}
	var out bytes.Buffer
	w := "/a"
	if err := listConversations(context.Background(), api, &w, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "c1") || strings.Contains(got, "c3") {
		t.Errorf("output = %q, want only /a's conversation", got)
	}
}
