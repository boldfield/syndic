package server_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// Tests of the tool-evidence and apply protocol (v5 delta section 6): the
// turn record's change set, the ordered evidence events, content refs that
// page evidence larger than a frame, and an apply that survives a lost reply.

func toolCall(id, name, args string) transport.ToolCall {
	return transport.ToolCall{ID: id, Type: "function", Function: transport.ToolCallFunction{Name: name, Arguments: args}}
}

func writeCall(id, path, content string) transport.ToolCall {
	args, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return toolCall(id, tools.NameWriteFile, string(args))
}

func toolStep(calls ...transport.ToolCall) fake.Step {
	return fake.Step{
		Kind:            fake.StepKindReply,
		Text:            "calling a tool",
		Status:          transport.StatusToolCalls,
		TransportStatus: transport.TransportStatusOK,
		ToolCalls:       calls,
	}
}

// toolRoster gives the author read and write in a review preset, and its
// reviewers read.
const toolRoster = enginetest.RosterTOML + `
[presets.readreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "write"]
reviewer_tools = ["read"]

[tools]
max_calls_per_turn = 5

[budgets]
turn_max_estimated_tokens = 100000000
`

func toolSubmit(id, workspace string) wire.SubmitRequest {
	preset := "readreview"
	return wire.SubmitRequest{SubmissionID: id, Input: "question", Preset: &preset, Workspace: workspace}
}

func workspaceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello from a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stagedServer runs a review turn in which the author stages the given files
// and the reviewer approves, and serves it.
func stagedServer(t *testing.T, files map[string]string, reviewer []fake.Step, opts ...enginetest.Option) (*enginetest.Harness, *serving, string, wire.SubmitReply) {
	t.Helper()
	var calls []transport.ToolCall
	for _, name := range []string{"n1.txt", "n2.txt"} {
		if body, ok := files[name]; ok {
			calls = append(calls, writeCall("w-"+name, name, body))
		}
	}
	if reviewer == nil {
		reviewer = []fake.Step{approve()}
	}
	h := enginetest.New(t, toolRoster, map[string][]fake.Step{
		"author":   {toolStep(calls...), enginetest.Reply(authorText)},
		"reviewer": reviewer,
	}, opts...)
	dir := workspaceDir(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	reply, err := h.Engine.Submit(toolSubmit("s1", dir))
	if err != nil {
		t.Fatal(err)
	}
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateApproved {
		t.Fatalf("turn ended %q %q, want approved", row.TerminalState, row.TerminalReason)
	}
	return h, s, dir, reply
}

func allEvidence(t *testing.T, c *client.Client, turnID string, limit int) []wire.EvidenceItem {
	t.Helper()
	var items []wire.EvidenceItem
	cursor := ""
	for pages := 0; pages < 1000; pages++ {
		page, err := c.Evidence(ctxT(t), turnID, cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, page.Items...)
		if page.NextCursor == nil {
			return items
		}
		cursor = *page.NextCursor
	}
	t.Fatal("evidence did not end")
	return nil
}

func kindsOf(items []wire.EvidenceItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Kind)
	}
	return out
}

// Evidence larger than a frame is never inline: the turn record and the
// evidence page carry hashes and refs, every frame stays under the cap, and
// the exact hashes resolve to the full bytes through content paging.
func TestLargeEvidenceIsRefsAndHashesResolveToFullContent(t *testing.T) {
	size := 3 << 19 // 1.5 MiB, over the frame cap
	if testing.Short() {
		size = 300 << 10 // still over one content page
	}
	big := strings.Repeat("0123456789abcdef", size/16)
	h, s, _, reply := stagedServer(t, map[string]string{"n1.txt": big}, nil,
		enginetest.WithContextBound("author", 10_000_000), enginetest.WithContextBound("reviewer", 10_000_000))
	c := s.dial()
	ctx := ctxT(t)

	s.resetFrames()
	turn, err := c.Turn(ctx, reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	cs := turn.ChangeSet
	if cs == nil || cs.State != journal.ChangeSetAppliable || len(cs.Paths) != 1 {
		t.Fatalf("change set = %+v, want one appliable path", cs)
	}
	p := cs.Paths[0]
	if p.Path != "n1.txt" || p.Op != "create" || p.NewLen != int64(len(big)) || p.BaseRef != nil || p.Outcome != journal.PathPending {
		t.Fatalf("path = %+v", p)
	}
	if p.NewRef != wire.ContentRef(p.NewSHA256) {
		t.Fatalf("new ref %q does not name %q", p.NewRef, p.NewSHA256)
	}

	items := allEvidence(t, c, reply.TurnID, 0)
	kinds := kindsOf(items)
	want := []string{wire.EventKindToolCallAccepted, wire.EventKindStaged, wire.EventKindToolResult, wire.EventKindChangeSetFrozen}
	if i := indexOfSubsequence(kinds, want); i < 0 {
		t.Fatalf("evidence kinds %v do not hold %v in order", kinds, want)
	}
	for i := 1; i < len(items); i++ {
		if items[i].Seq <= items[i-1].Seq {
			t.Fatalf("evidence out of order at %d: %d after %d", i, items[i].Seq, items[i-1].Seq)
		}
	}

	// The call's arguments, journaled by hash, resolve to the full transcript
	// of what the author sent, including the large content.
	var call struct {
		ArgsSHA256 string `json:"args_sha256"`
	}
	for _, it := range items {
		if it.Kind == wire.EventKindToolCallAccepted {
			if err := json.Unmarshal(it.Data, &call); err != nil {
				t.Fatal(err)
			}
		}
	}
	args, err := c.Content(ctx, wire.ContentRef(call.ArgsSHA256))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(args, []byte(big[:1000])) || len(args) < len(big) {
		t.Fatalf("args resolved to %d bytes, want the full %d byte write", len(args), len(big))
	}
	got, err := c.Content(ctx, p.NewRef)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != big {
		t.Fatalf("change set content resolved to %d bytes, want %d", len(got), len(big))
	}
	if n := s.maxFrame(); n == 0 || n >= wire.MaxFrameBytes {
		t.Fatalf("largest frame = %d, want under %d", n, wire.MaxFrameBytes)
	}
	_ = h
}

func indexOfSubsequence(have, want []string) int {
	j := 0
	for i, k := range have {
		if j < len(want) && k == want[j] {
			j++
			if j == len(want) {
				return i
			}
		}
	}
	return -1
}

// Evidence pages resume exactly where the last page ended, whatever the
// limit, and a bad cursor is a bad request.
func TestEvidencePagingAndErrors(t *testing.T) {
	_, s, _, reply := stagedServer(t, map[string]string{"n1.txt": "one\n", "n2.txt": "two\n"}, nil)
	c := s.dial()
	whole := allEvidence(t, c, reply.TurnID, 0)
	paged := allEvidence(t, c, reply.TurnID, 1)
	if len(whole) == 0 || len(paged) != len(whole) {
		t.Fatalf("whole %d items, paged %d", len(whole), len(paged))
	}
	for i := range whole {
		if whole[i].Seq != paged[i].Seq || whole[i].Kind != paged[i].Kind {
			t.Fatalf("item %d: whole %d %s, paged %d %s", i, whole[i].Seq, whole[i].Kind, paged[i].Seq, paged[i].Kind)
		}
	}

	_, err := c.Evidence(ctxT(t), reply.TurnID, "not-a-seq", 0)
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.ErrorCodeBadRequest {
		t.Fatalf("bad cursor: %v, want bad_request", err)
	}
	_, err = c.Evidence(ctxT(t), "no-such-turn", "", 0)
	if !errors.As(err, &we) || we.Code != wire.ErrorCodeNotFound {
		t.Fatalf("unknown turn: %v, want not_found", err)
	}
}

// A turn with no tools has no change set and no verdict evidence, and its
// turn record is still answered: a verdict recorded without evidence is not
// an error.
func TestTurnWithoutEvidenceOrChangeSet(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	reply, err := c.Submit(ctxT(t), reviewReq("plain-1"))
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, h, reply.TurnID)
	turn, err := c.Turn(ctxT(t), reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.ChangeSet != nil || len(turn.Verdicts) != 1 || len(turn.Verdicts[0].ResultIDs) != 0 || len(turn.Verdicts[0].ObservationIDs) != 0 {
		t.Fatalf("turn = change set %+v, verdicts %+v", turn.ChangeSet, turn.Verdicts)
	}
	if items := allEvidence(t, c, reply.TurnID, 0); len(items) != 0 {
		t.Fatalf("evidence of a toolless turn: %v", kindsOf(items))
	}
}

// A reviewer's lookup is the evidence of its verdict: the verdict names the
// result and the observation, and the evidence events give their hashes.
func TestVerdictEvidenceIsExposed(t *testing.T) {
	read := toolStep(toolCall("r1", tools.NameReadFile, `{"path":"a.txt"}`))
	_, s, _, reply := stagedServer(t, map[string]string{"n1.txt": "one\n"}, []fake.Step{read, approve()})
	c := s.dial()
	turn, err := c.Turn(ctxT(t), reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.Verdicts) != 1 || len(turn.Verdicts[0].ResultIDs) != 1 || len(turn.Verdicts[0].ObservationIDs) == 0 {
		t.Fatalf("verdicts = %+v, want one with a result and an observation", turn.Verdicts)
	}
	byID := map[string]json.RawMessage{}
	items := allEvidence(t, c, reply.TurnID, 0)
	for _, it := range items {
		var d struct {
			ResultID string `json:"result_id"`
		}
		if it.Kind == wire.EventKindToolResult && json.Unmarshal(it.Data, &d) == nil {
			byID[d.ResultID] = it.Data
		}
	}
	data, ok := byID[turn.Verdicts[0].ResultIDs[0]]
	if !ok {
		t.Fatalf("no tool_result event for result %s in %v", turn.Verdicts[0].ResultIDs[0], kindsOf(items))
	}
	var res struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	body, err := c.Content(ctxT(t), wire.ContentRef(res.SHA256))
	if err != nil || !bytes.Contains(body, []byte("hello from a")) {
		t.Fatalf("result content = %q, %v", body, err)
	}
	if indexOfSubsequence(kindsOf(items), []string{wire.EventKindVerdictEvidence}) < 0 {
		t.Fatalf("no verdict_evidence event in %v", kindsOf(items))
	}
}

// The apply's reply is lost: the client's connection is dropped while the
// apply runs. The apply completes regardless, a retry from a new connection
// gets the journaled result, and nothing is written or journaled twice.
func TestApplyLostReplyRetry(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h, s, dir, reply := stagedServer(t, map[string]string{"n1.txt": "one\n", "n2.txt": "two\n"}, nil,
		enginetest.WithBeforeApply(func(string) {
			select {
			case entered <- struct{}{}:
				<-release
			default:
			}
		}))
	a := s.dial()
	type result struct {
		reply wire.ApplyReply
		err   error
	}
	done := make(chan result, 1)
	go func() {
		r, err := a.Apply(ctxT(t), reply.TurnID)
		done <- result{r, err}
	}()
	<-entered
	_ = a.Close() // the reply will have nowhere to go
	if r := <-done; r.err == nil {
		t.Fatalf("apply on a closed connection succeeded: %+v", r.reply)
	}
	close(release)

	b := s.dial()
	var got wire.ApplyReply
	deadline := ctxT(t)
	for {
		var err error
		got, err = b.Apply(deadline, reply.TurnID)
		var we *wire.Error
		if errors.As(err, &we) && we.Code == wire.ErrorCodeBusy {
			continue // the first apply is still running
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if got.State != journal.ChangeSetApplied {
		t.Fatalf("retry state %q, want applied", got.State)
	}
	for _, p := range got.Paths {
		if p.Outcome != journal.PathApplied {
			t.Fatalf("path %+v, want applied", p)
		}
	}
	for name, want := range map[string]string{"n1.txt": "one\n", "n2.txt": "two\n"} {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v", name, b, err)
		}
	}
	// A further retry writes nothing and journals no second apply.
	again, err := b.Apply(ctxT(t), reply.TurnID)
	if err != nil || again.State != journal.ChangeSetApplied {
		t.Fatalf("second retry = %+v, %v", again, err)
	}
	count := map[string]int{}
	for _, it := range allEvidence(t, b, reply.TurnID, 0) {
		count[it.Kind]++
	}
	if count[wire.EventKindApplyStarted] != 1 || count[wire.EventKindApplyFinished] != 1 || count[wire.EventKindApplyFile] != 2 {
		t.Fatalf("apply events = %v, want one started, one finished, two files", count)
	}
	turn, err := b.Turn(ctxT(t), reply.TurnID)
	if err != nil || turn.ChangeSet == nil || turn.ChangeSet.State != journal.ChangeSetApplied {
		t.Fatalf("turn change set = %+v, %v", turn.ChangeSet, err)
	}
	_ = h
}

// An apply refused for a changed base is refused the same way on retry, from
// a new connection, and writes nothing: the error is the journal's, not the
// connection's.
func TestApplyErrorSurvivesRetry(t *testing.T) {
	_, s, dir, reply := stagedServer(t, map[string]string{"n1.txt": "one\n", "n2.txt": "two\n"}, nil)
	// Someone else created n2.txt after the base was taken.
	if err := os.WriteFile(filepath.Join(dir, "n2.txt"), []byte("theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var first *wire.Error
	for i := 0; i < 3; i++ {
		c := s.dial() // a fresh connection each time: the earlier reply is gone
		_, err := c.Apply(ctxT(t), reply.TurnID)
		var we *wire.Error
		if !errors.As(err, &we) || we.Code != wire.ErrorCodeBaseChanged || len(we.Paths) != 1 || we.Paths[0] != "n2.txt" {
			t.Fatalf("apply %d: %v, want base_changed naming n2.txt", i, err)
		}
		if first == nil {
			first = we
		}
		_ = c.Close()
	}
	if _, err := os.Stat(filepath.Join(dir, "n1.txt")); !os.IsNotExist(err) {
		t.Fatalf("n1.txt written by a refused apply: %v", err)
	}
	c := s.dial()
	turn, err := c.Turn(ctxT(t), reply.TurnID)
	if err != nil || turn.ChangeSet == nil || turn.ChangeSet.State != journal.ChangeSetAppliable {
		t.Fatalf("change set after refusals = %+v, %v", turn.ChangeSet, err)
	}

	// Discard is idempotent in outcome too: it reports discarded, and a retry
	// is refused as not appliable with the state named.
	d, err := c.Discard(ctxT(t), reply.TurnID)
	if err != nil || d.State != journal.ChangeSetDiscarded {
		t.Fatalf("discard = %+v, %v", d, err)
	}
	_, err = s.dial().Discard(ctxT(t), reply.TurnID)
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.ErrorCodeNotAppliable {
		t.Fatalf("second discard: %v, want not_appliable", err)
	}
	items := allEvidence(t, c, reply.TurnID, 0)
	if indexOfSubsequence(kindsOf(items), []string{wire.EventKindChangeSetDiscarded}) < 0 {
		t.Fatalf("no change_set_discarded in %v", kindsOf(items))
	}
}
