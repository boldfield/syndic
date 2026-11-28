package engine_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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
