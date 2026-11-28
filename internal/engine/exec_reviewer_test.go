package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/sandbox"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// A reviewer's first bash call after freeze leaves the candidate's
// snapshot_hash unchanged. Its build extends the record, so its view shows a
// higher snapshot_version and the overlay hash of the frozen change set; the
// observations are bound to its verdict, and what its command wrote reaches
// no other place.
func TestBashReviewerFreeze(t *testing.T) {
	area := t.TempDir()
	scripts := append(sandbox.FakeSelfTestScripts("", false), sandbox.FakeSelfTestScripts("", false)...)
	scripts = append(scripts, sandbox.FakeScript{Chunks: []sandbox.FakeChunk{{Data: []byte("out\n")}}})
	be := &probeBackend{Fake: sandbox.NewFake(allCaps, scripts...), area: area}
	bashArgs, _ := json.Marshal(map[string]string{"command": "echo a > gen.txt"})

	// The author never touches a.txt, so the reviewer's build is the first
	// to observe it.
	author := []fake.Step{
		toolStepWith(call("w1", tools.NameWriteFile, `{"path":"b.txt","content":"new file\n"}`)),
		enginetest.Reply(authorText),
		enginetest.Reply(commandAllow),
	}
	var (
		h        *enginetest.Harness
		mu       sync.Mutex
		before   journal.ChangeSet
		requests []string
	)
	reviewer := []fake.Step{toolStepWith(call("r1", tools.NameBash, string(bashArgs)))}
	reviewer = append(reviewer, enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		mu.Lock()
		requests = append(requests, messagesText(req))
		mu.Unlock()
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	}))
	roster := refereeRoster(`
[presets.execreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "write"]
reviewer_tools = ["read", "exec"]

[tools]
max_calls_per_turn = 30
`)
	// The frozen change set as it stands before the reviewer's command runs.
	hook := func(turnID, _, name string) {
		if name != tools.NameBash {
			return
		}
		cs, err := h.Store.ChangeSetForTurn(turnID)
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		before = cs
		mu.Unlock()
	}
	opts := append(refereeOpts(), enginetest.WithToolCalls("reviewer", transport.TriYes), enginetest.WithBeforeToolEffect(hook), enginetest.WithExec(engine.ExecOptions{
		Backend:  be,
		Shells:   []string{fakeShell(t, "bash")},
		SelfTest: sandbox.SelfTestConfig{Dir: t.TempDir()},
		TempDir:  area,
	}))
	h = enginetest.New(t, roster, map[string][]fake.Step{"author": author, "reviewer": reviewer}, opts...)
	ws := workspaceDir(t)
	reply, err := h.Engine.Submit(toolReq("s1", "execreview", ws))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	if n := len(be.seen()); n != 1 {
		t.Fatalf("%d commands started, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(ws, "gen.txt")); !os.IsNotExist(err) {
		t.Errorf("gen.txt reached the workspace (err %v)", err)
	}
	// The scratch directory held the workspace file and the candidate's
	// staged creation.
	if names := be.seen()[0].scratchNames; len(names) != 2 || names[0] != "a.txt" || names[1] != "b.txt" {
		t.Errorf("reviewer's scratch directory held %v, want a.txt and b.txt", names)
	}
	for _, r := range requests {
		if strings.Contains(r, "gen.txt") {
			t.Error("a file the command wrote appears in a request")
		}
	}

	wantOverlay := sha256.Sum256(func() []byte {
		lines, err := observation.FrameChangeSetLines([]observation.Change{{
			Path: "b.txt", Op: observation.OpCreate, BaseSHA: observation.Absent,
			NewSHA: sha256Hex("new file\n"), NewLen: int64(len("new file\n")),
		}})
		if err != nil {
			t.Fatal(err)
		}
		return lines
	}())
	err = h.Store.Tx(func(tx *journal.Tx) error {
		after, err := tx.ChangeSetForTurn(reply.TurnID)
		if err != nil {
			return err
		}
		if after.SnapshotHash != before.SnapshotHash || after.SnapshotVersion != before.SnapshotVersion {
			t.Errorf("the candidate's snapshot moved: %+v -> %+v", before, after)
		}
		ecs, err := tx.ExecCallsByTurn(reply.TurnID)
		if err != nil || len(ecs) != 1 {
			t.Fatalf("exec calls %+v, err %v", ecs, err)
		}
		if ecs[0].View.SnapshotVersion <= before.SnapshotVersion {
			t.Errorf("view version %d, want above the frozen %d", ecs[0].View.SnapshotVersion, before.SnapshotVersion)
		}
		if ecs[0].View.OverlaySHA256 != hex.EncodeToString(wantOverlay[:]) {
			t.Errorf("overlay hash %s, want that of the frozen change set", ecs[0].View.OverlaySHA256)
		}
		verdicts, err := tx.ListVerdicts(reply.TurnID)
		if err != nil || len(verdicts) == 0 {
			t.Fatalf("verdicts %+v, err %v", verdicts, err)
		}
		ev, err := tx.GetVerdictEvidence(verdicts[len(verdicts)-1].ID)
		if err != nil {
			return err
		}
		if ev.SnapshotVersion != ecs[0].View.SnapshotVersion || len(ev.ObservationIDs) == 0 || len(ev.ResultIDs) != 1 {
			t.Errorf("verdict evidence %+v: want the build's observations and the call's result", ev)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func messagesText(req transport.CompleteRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// Across a revision round, a reviewer's command and the author's each belong
// to their own round's candidate: what one command wrote is in no other
// call's scratch directory and in no request, the second round's reviewer
// builds from the second candidate's change set, and the author's second-round
// call reaches the second round's reviewer as evidence and not the first's.
func TestBashRevisionRounds(t *testing.T) {
	area := t.TempDir()
	scripts := append(sandbox.FakeSelfTestScripts("", false), sandbox.FakeSelfTestScripts("", false)...)
	for i := 0; i < 3; i++ {
		scripts = append(scripts, sandbox.FakeScript{Chunks: []sandbox.FakeChunk{{Data: []byte("out\n")}}})
	}
	be := &probeBackend{Fake: sandbox.NewFake(allCaps, scripts...), area: area}
	reviewerArgs, _ := json.Marshal(map[string]string{"command": "true"})
	authorArgs, _ := json.Marshal(map[string]string{"command": "echo round2-marker"})

	author := []fake.Step{
		toolStepWith(call("w1", tools.NameWriteFile, `{"path":"b.txt","content":"new file\n"}`)),
		enginetest.Reply(authorText),
		enginetest.Reply(commandAllow),
		toolStepWith(
			call("w2", tools.NameWriteFile, `{"path":"c.txt","content":"second\n"}`),
			call("b2", tools.NameBash, string(authorArgs)),
		),
		enginetest.Reply(commandAllow),
		enginetest.Reply(revisedText),
		enginetest.Reply(commandAllow),
	}
	reviewer := []fake.Step{
		toolStepWith(call("r1", tools.NameBash, string(reviewerArgs))),
		blockStep("b1"),
		toolStepWith(call("r2", tools.NameBash, string(reviewerArgs))),
		v2Verdict("1", "approve", "", "b1 withdrawn"),
	}
	roster := strings.Replace(refereeRoster(`
[presets.execreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "write", "exec"]
reviewer_tools = ["read", "exec"]

[tools]
max_calls_per_turn = 30
`), "max_rounds = 0", "max_rounds = 2", 1)
	opts := append(refereeOpts(), enginetest.WithToolCalls("reviewer", transport.TriYes), enginetest.WithExec(engine.ExecOptions{
		Backend:  be,
		Shells:   []string{fakeShell(t, "bash")},
		SelfTest: sandbox.SelfTestConfig{Dir: t.TempDir()},
		TempDir:  area,
	}))
	h := enginetest.New(t, roster, map[string][]fake.Step{"author": author, "reviewer": reviewer}, opts...)
	reply, err := h.Engine.Submit(toolReq("s1", "execreview", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")

	// The calls ran in the order: round-1 reviewer, round-2 author, round-2
	// reviewer. Each has its own scratch directory, with no other's file.
	runs := be.seen()
	if len(runs) != 3 {
		t.Fatalf("%d commands started, want 3", len(runs))
	}
	want := [][]string{{"a.txt", "b.txt"}, {"a.txt", "b.txt", "c.txt"}, {"a.txt", "b.txt", "c.txt"}}
	for i, r := range runs {
		if !slices.Equal(r.scratchNames, want[i]) {
			t.Errorf("call %d: scratch directory held %v, want %v", i, r.scratchNames, want[i])
		}
		for j := 0; j < i; j++ {
			if r.scratch == runs[j].scratch {
				t.Errorf("calls %d and %d share a scratch directory", j, i)
			}
		}
	}

	// What a command wrote is in no request: the command text carries no
	// such name, so a request naming it carried the file.
	var all []transport.CompleteRequest
	for _, id := range []string{"author", "reviewer"} {
		for _, c := range h.Adapters[id].Calls() {
			all = append(all, c.Request)
		}
	}
	for _, req := range all {
		if strings.Contains(messagesText(req), "gen.txt") {
			t.Error("a file a command wrote appears in a request")
		}
	}

	// The author's second-round call is evidence of the second candidate
	// only.
	reviews := h.Adapters["reviewer"].Calls()
	firstHash := reviews[0].Request.Envelope.CandidateHash
	var sawSecond bool
	for _, c := range reviews {
		carries := strings.Contains(messagesText(c.Request), "round2-marker")
		if c.Request.Envelope.CandidateHash == firstHash {
			if carries {
				t.Error("a round-1 reviewer request carries the author's round-2 call")
			}
			continue
		}
		sawSecond = sawSecond || carries
	}
	if !sawSecond {
		t.Error("the round-2 reviewer's requests lack the author's round-2 call")
	}

	// The round-2 reviewer's build used the second candidate's change set,
	// not the first's, and the verdicts' evidence holds the reviewers' own results.
	err = h.Store.Tx(func(tx *journal.Tx) error {
		ecs, err := tx.ExecCallsByTurn(reply.TurnID)
		if err != nil || len(ecs) != 3 {
			t.Fatalf("exec calls %+v, err %v", ecs, err)
		}
		slices.SortFunc(ecs, func(x, y journal.ExecCall) int { return x.RecordedAt.Compare(y.RecordedAt) })
		if ecs[0].View.OverlaySHA256 == ecs[2].View.OverlaySHA256 {
			t.Error("the round-2 reviewer's overlay is the first candidate's change set")
		}
		if ecs[1].View.OverlaySHA256 != ecs[2].View.OverlaySHA256 {
			t.Error("the author's round-2 overlay and the round-2 reviewer's differ")
		}
		verdicts, err := tx.ListVerdicts(reply.TurnID)
		if err != nil || len(verdicts) != 2 {
			t.Fatalf("verdicts %+v, err %v", verdicts, err)
		}
		var counts []int
		for _, v := range verdicts {
			ev, err := tx.GetVerdictEvidence(v.ID)
			if err != nil {
				return err
			}
			counts = append(counts, len(ev.ResultIDs))
		}
		// The round-1 verdict rests on its one call, the round-2 verdict on
		// that reviewer's two; the author's call is in neither.
		slices.Sort(counts)
		if !slices.Equal(counts, []int{1, 2}) {
			t.Errorf("verdict result counts %v, want [1 2]", counts)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
