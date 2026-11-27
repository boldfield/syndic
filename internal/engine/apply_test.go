package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// End-to-end tests of apply, discard and turn exclusion (v5 delta section 6,
// "Apply"; section 3, history annotations): the engine runs one turn or one
// apply at a time, an apply is classified and journaled per path, a retry
// skips what is done and reports a conflict by name, discard never undoes or
// deletes, and recovery writes nothing.

var applyFiles = []string{"n1.txt", "n2.txt", "n3.txt"}

// applyScripts has the author stage three new files and the reviewer decide.
func applyScripts(reviewer fake.Step, more ...fake.Step) map[string][]fake.Step {
	author := []fake.Step{
		toolStepWith(writeCall("c1", "n1.txt", "one\n"), writeCall("c2", "n2.txt", "two\n"), writeCall("c3", "n3.txt", "three\n")),
		enginetest.Reply(authorText),
	}
	return map[string][]fake.Step{"author": append(author, more...), "reviewer": {reviewer}}
}

// stagedTurn runs a review turn that stages three files and returns the
// harness, the workspace, the turn and its conversation.
func stagedTurn(t *testing.T, reviewer fake.Step, opts ...enginetest.Option) (*enginetest.Harness, string, wire.SubmitReply) {
	t.Helper()
	h := enginetest.New(t, toolRoster("5"), applyScripts(reviewer, enginetest.Reply("follow-up answer")), opts...)
	dir := workspaceDir(t)
	reply, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	return h, dir, reply
}

func wantRefusalDetail(t *testing.T, err error, code, detail string) *wire.Error {
	t.Helper()
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != code {
		t.Fatalf("err = %v, want refusal %q", err, code)
	}
	if detail != "" && (we.Detail == nil || *we.Detail != detail) {
		t.Fatalf("refusal %q detail = %v, want %q", code, we.Detail, detail)
	}
	return we
}

func fileIs(t *testing.T, dir, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, err %v; want %q", name, got, err, want)
	}
}

func fileAbsent(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatalf("%s exists or errors: %v", name, err)
	}
}

func outcomes(paths []wire.ChangePath) map[string]string {
	m := map[string]string{}
	for _, p := range paths {
		m[p.Path] = p.Outcome
	}
	return m
}

func wantOutcomes(t *testing.T, paths []wire.ChangePath, want map[string]string) {
	t.Helper()
	got := outcomes(paths)
	if len(got) != len(want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	for p, o := range want {
		if got[p] != o {
			t.Fatalf("outcomes %v, want %v", got, want)
		}
	}
}

func changeState(t *testing.T, h *enginetest.Harness, turnID string) string {
	t.Helper()
	cs, err := h.Store.ChangeSetForTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return cs.State
}

// halfApplied leaves the change set as a crash between renames does: apply
// started, n1 written and journaled, n2 not yet written, and n3 holding other
// content. Nothing is written by the engine.
func halfApplied(t *testing.T, h *enginetest.Harness, dir, turnID string) {
	t.Helper()
	cs, err := h.Store.ChangeSetForTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "n1.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "n3.txt"), []byte("someone else's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Tx(func(tx *journal.Tx) error {
		if err := tx.BeginApply(cs.ID, false); err != nil {
			return err
		}
		return tx.RecordApplyFile(cs.ID, "n1.txt", journal.PathApplied)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyApprovedWritesOnceAndSecondApplyWritesNothing(t *testing.T) {
	h, dir, reply := stagedTurn(t, approveStep())
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	for _, f := range applyFiles {
		fileAbsent(t, dir, f) // approval does not write
	}

	got, err := h.Engine.Apply(reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != journal.ChangeSetApplied {
		t.Fatalf("state %q, want applied", got.State)
	}
	wantOutcomes(t, got.Paths, map[string]string{"n1.txt": "applied", "n2.txt": "applied", "n3.txt": "applied"})
	fileIs(t, dir, "n2.txt", "two\n")

	// Edit a file after the apply: a second apply returns the state and
	// writes nothing.
	if err := os.WriteFile(filepath.Join(dir, "n2.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := h.Engine.Apply(reply.TurnID)
	if err != nil || again.State != journal.ChangeSetApplied {
		t.Fatalf("second apply = %+v, err %v; want applied", again, err)
	}
	fileIs(t, dir, "n2.txt", "edited\n")
}

func TestApplyOverriddenTurn(t *testing.T) {
	h, dir, reply := stagedTurn(t, blockStep("b1"))
	if row := waitEnded(t, h, reply.TurnID); row.TerminalState != engine.StateIncomplete {
		t.Fatalf("turn ended %q, want incomplete", row.TerminalState)
	}
	_, err := h.Engine.Apply(reply.TurnID)
	_ = wantRefusalDetail(t, err, wire.ErrorCodeNotAppliable, "not_approved")
	for _, f := range applyFiles {
		fileAbsent(t, dir, f)
	}

	if err := h.Engine.Override(reply.TurnID, "false alarm"); err != nil {
		t.Fatal(err)
	}
	got, err := h.Engine.Apply(reply.TurnID)
	if err != nil || got.State != journal.ChangeSetApplied {
		t.Fatalf("apply = %+v, err %v; want applied", got, err)
	}
	fileIs(t, dir, "n1.txt", "one\n")
	ops, err := h.Store.ApplyOps(mustChangeSet(t, h, reply.TurnID).ID)
	if err != nil || len(ops) == 0 || !ops[0].Override {
		t.Fatalf("apply ops %+v, err %v; want the apply recorded under override", ops, err)
	}
}

func mustChangeSet(t *testing.T, h *enginetest.Harness, turnID string) journal.ChangeSet {
	t.Helper()
	cs, err := h.Store.ChangeSetForTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestApplyRefusals(t *testing.T) {
	h, dir, reply := stagedTurn(t, approveStep())
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")

	_, err := h.Engine.Apply("no-such-turn")
	_ = wantRefusalDetail(t, err, wire.ErrorCodeNotFound, "")
	_, err = h.Engine.Discard("no-such-turn")
	_ = wantRefusalDetail(t, err, wire.ErrorCodeNotFound, "")

	// A path already present: base_changed names it, nothing is written and
	// the set stays appliable.
	if err := os.WriteFile(filepath.Join(dir, "n2.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = h.Engine.Apply(reply.TurnID)
	we := wantRefusalDetail(t, err, wire.ErrorCodeBaseChanged, "")
	if len(we.Paths) != 1 || we.Paths[0] != "n2.txt" {
		t.Fatalf("base_changed paths %v, want [n2.txt]", we.Paths)
	}
	fileIs(t, dir, "n2.txt", "mine\n")
	fileAbsent(t, dir, "n1.txt")
	fileAbsent(t, dir, "n3.txt")
	if st := changeState(t, h, reply.TurnID); st != journal.ChangeSetAppliable {
		t.Fatalf("state %q, want appliable", st)
	}

	// A workspace that cannot be opened: workspace_missing, nothing written.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	_, err = h.Engine.Apply(reply.TurnID)
	_ = wantRefusalDetail(t, err, wire.ErrorCodeWorkspaceMissing, "")
	if st := changeState(t, h, reply.TurnID); st != journal.ChangeSetAppliable {
		t.Fatalf("state %q, want appliable", st)
	}
}

func TestApplyRefusedBusyWhileTurnOpen(t *testing.T) {
	h := enginetest.New(t, toolRoster("5"), applyScripts(approveStep(), fake.Step{Kind: fake.StepKindHang}))
	dir := workspaceDir(t)
	first, err := h.Engine.Submit(toolReq("s1", "readreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, first.TurnID, engine.StateApproved, "all_approved")
	second, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "more", Preset: sp("readreview"), Workspace: dir, ConversationID: &first.ConversationID})
	if err != nil {
		t.Fatal(err)
	}

	_, err = h.Engine.Apply(first.TurnID)
	we := wantRefusalDetail(t, err, wire.ErrorCodeBusy, "turn")
	if we.TurnID == nil || *we.TurnID != second.TurnID {
		t.Fatalf("busy names turn %v, want %s", we.TurnID, second.TurnID)
	}
	_, err = h.Engine.Discard(second.TurnID)
	_ = wantRefusalDetail(t, err, wire.ErrorCodeBusy, "turn")
	fileAbsent(t, dir, "n1.txt")
}

func TestBusyWhileApplyRuns(t *testing.T) {
	entered, release := make(chan string), make(chan struct{})
	h, dir, reply := stagedTurn(t, approveStep(), enginetest.WithBeforeApply(func(turnID string) {
		entered <- turnID
		<-release
	}))
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")

	type result struct {
		reply wire.ApplyReply
		err   error
	}
	done := make(chan result, 1)
	go func() {
		r, err := h.Engine.Apply(reply.TurnID)
		done <- result{r, err}
	}()
	if got := <-entered; got != reply.TurnID {
		t.Fatalf("apply opened for %q, want %q", got, reply.TurnID)
	}

	// Each request during the apply is refused at once, naming the apply.
	_, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "more", Preset: sp("readreview"), Workspace: dir, ConversationID: &reply.ConversationID})
	we := wantRefusalDetail(t, err, wire.ErrorCodeBusy, "apply")
	if we.TurnID == nil || *we.TurnID != reply.TurnID {
		t.Fatalf("busy names turn %v, want %s", we.TurnID, reply.TurnID)
	}
	_, err = h.Engine.Apply(reply.TurnID)
	_ = wantRefusalDetail(t, err, wire.ErrorCodeBusy, "apply")
	_, err = h.Engine.Discard(reply.TurnID)
	_ = wantRefusalDetail(t, err, wire.ErrorCodeBusy, "apply")
	fileAbsent(t, dir, "n1.txt")

	close(release)
	r := <-done
	if r.err != nil || r.reply.State != journal.ChangeSetApplied {
		t.Fatalf("apply = %+v, err %v", r.reply, r.err)
	}
	// The apply is over: a submit is admitted again.
	if _, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s3", Input: "more", Preset: sp("readreview"), Workspace: dir, ConversationID: &reply.ConversationID}); err != nil {
		t.Fatalf("submit after the apply: %v", err)
	}
}

// A retry after a partial apply skips the done path, writes the pending one
// and reports the conflicting one by name; the set ends conflict.
func TestApplyPartialRetrySkipsDoneAndReportsConflict(t *testing.T) {
	h, dir, reply := stagedTurn(t, approveStep())
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	halfApplied(t, h, dir, reply.TurnID)
	if st := changeState(t, h, reply.TurnID); st != journal.ChangeSetPartial {
		t.Fatalf("state %q, want partial", st)
	}
	// An edit to the done path must survive: it is skipped, not rewritten.
	if err := os.WriteFile(filepath.Join(dir, "n1.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := h.Engine.Apply(reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != journal.ChangeSetConflict {
		t.Fatalf("state %q, want conflict", got.State)
	}
	wantOutcomes(t, got.Paths, map[string]string{"n1.txt": "applied", "n2.txt": "applied", "n3.txt": "conflict"})
	fileIs(t, dir, "n2.txt", "two\n")
	fileIs(t, dir, "n3.txt", "someone else's\n")
}

// Discard after a partial apply keeps applied files, per-path outcomes and
// the evidence, and an applied or discarded set cannot be discarded again.
func TestDiscardKeepsAppliedFilesAndEvidence(t *testing.T) {
	h, dir, reply := stagedTurn(t, approveStep())
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	before := mustChangeSet(t, h, reply.TurnID)
	halfApplied(t, h, dir, reply.TurnID)

	got, err := h.Engine.Discard(reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != journal.ChangeSetDiscarded {
		t.Fatalf("state %q, want discarded", got.State)
	}
	wantOutcomes(t, got.Paths, map[string]string{"n1.txt": "applied", "n2.txt": "pending", "n3.txt": "pending"})
	fileIs(t, dir, "n1.txt", "one\n")
	fileAbsent(t, dir, "n2.txt")

	after := mustChangeSet(t, h, reply.TurnID)
	if after.ID != before.ID || after.ChangesHash != before.ChangesHash || len(after.Paths) != len(before.Paths) {
		t.Fatalf("change set changed: %+v then %+v", before, after)
	}
	for _, p := range after.Paths {
		var body []byte
		if err := h.Store.Tx(func(tx *journal.Tx) (cerr error) { body, cerr = tx.Content(p.NewSHA256); return cerr }); err != nil || len(body) == 0 {
			t.Fatalf("content of %s is gone: %q, err %v", p.Path, body, err)
		}
	}

	_, err = h.Engine.Discard(reply.TurnID)
	_ = wantRefusalDetail(t, err, wire.ErrorCodeNotAppliable, journal.ChangeSetDiscarded)
	_, err = h.Engine.Apply(reply.TurnID)
	_ = wantRefusalDetail(t, err, wire.ErrorCodeNotAppliable, "discarded")
	fileAbsent(t, dir, "n2.txt")
}

func TestDiscardOfAppliedSetRefused(t *testing.T) {
	h, dir, reply := stagedTurn(t, approveStep())
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	if _, err := h.Engine.Apply(reply.TurnID); err != nil {
		t.Fatal(err)
	}
	_, err := h.Engine.Discard(reply.TurnID)
	_ = wantRefusalDetail(t, err, wire.ErrorCodeNotAppliable, journal.ChangeSetApplied)
	fileIs(t, dir, "n1.txt", "one\n")
}

// A restart classifies an apply a crash left unfinished, records it, and
// writes nothing to the workspace.
func TestRestartRecoversUnfinishedApplyWithoutWriting(t *testing.T) {
	h, dir, reply := stagedTurn(t, approveStep())
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	halfApplied(t, h, dir, reply.TurnID)
	// n1's rename happened but was never journaled for n2; list the tree.
	snapshot := tree(t, dir)

	h.Crash()
	s := restart(t, h, toolRoster("5"), nil)
	if got := tree(t, dir); got != snapshot {
		t.Fatalf("restart changed the workspace:\n%s\nwas\n%s", got, snapshot)
	}
	cs := mustChangeSet(t, h, reply.TurnID)
	if cs.State != journal.ChangeSetPartial {
		t.Fatalf("state %q, want partial", cs.State)
	}
	got := map[string]string{}
	for _, p := range cs.Paths {
		got[p.Path] = p.Outcome
	}
	want := map[string]string{"n1.txt": "applied", "n2.txt": "pending", "n3.txt": "conflict"}
	for p, o := range want {
		if got[p] != o {
			t.Fatalf("outcomes %v, want %v", got, want)
		}
	}
	ops, err := h.Store.ApplyOps(cs.ID)
	if err != nil || ops[len(ops)-1].OpType != "recovered" {
		t.Fatalf("apply ops %+v, err %v; want a recovered entry last", ops, err)
	}

	// The human decides: an apply now writes the pending path.
	res, err := s.Apply(reply.TurnID)
	if err != nil || res.State != journal.ChangeSetConflict {
		t.Fatalf("apply after recovery = %+v, err %v", res, err)
	}
	fileIs(t, dir, "n2.txt", "two\n")
}

func tree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(e.Name() + "=" + string(body) + "\n")
	}
	return b.String()
}

// A later turn's history carries the change set's state and each path's own
// outcome: a set discarded after a partial apply shows the applied path and
// the others as not applied.
func TestHistoryCarriesChangeSetStateAndPathOutcomes(t *testing.T) {
	h, dir, reply := stagedTurn(t, approveStep())
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	halfApplied(t, h, dir, reply.TurnID)
	if _, err := h.Engine.Discard(reply.TurnID); err != nil {
		t.Fatal(err)
	}

	next, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "more", Preset: sp("readsolo"), Workspace: dir, ConversationID: &reply.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := h.Store.HistoryDocument(next.ContextHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"changes 9\ndiscarded\n", "applied n1.txt\n", "not applied n2.txt\n", "not applied n3.txt\n"} {
		if !strings.Contains(string(doc), want) {
			t.Fatalf("history lacks %q:\n%s", want, doc)
		}
	}
}
