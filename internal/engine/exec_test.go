package engine_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/sandbox"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// probeBackend wraps the fake backend. At the start of each command it records
// what the call's directories held, then does what a command would: it writes
// a file in each.
type probeBackend struct {
	*sandbox.Fake
	area string
	mu   sync.Mutex
	runs []probeRun
}

type probeRun struct {
	scratch, tmp string
	tmpEntries   int
	scratchNames []string
	proxy        string
}

func (p *probeBackend) Start(ctx context.Context, spec sandbox.Spec) (sandbox.Process, error) {
	if strings.HasPrefix(spec.ScratchDir, p.area) {
		run := probeRun{scratch: spec.ScratchDir, tmp: spec.TempDir}
		if spec.Proxy != nil {
			run.proxy = spec.Proxy.Address
		}
		if es, err := os.ReadDir(spec.TempDir); err == nil {
			run.tmpEntries = len(es)
		}
		if es, err := os.ReadDir(spec.ScratchDir); err == nil {
			for _, e := range es {
				run.scratchNames = append(run.scratchNames, e.Name())
			}
		}
		_ = os.WriteFile(filepath.Join(spec.TempDir, "tmp.txt"), []byte("t"), 0o600)
		_ = os.WriteFile(filepath.Join(spec.ScratchDir, "gen.txt"), []byte("g"), 0o600)
		p.mu.Lock()
		p.runs = append(p.runs, run)
		p.mu.Unlock()
	}
	return p.Fake.Start(ctx, spec)
}

func (p *probeBackend) seen() []probeRun {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]probeRun(nil), p.runs...)
}

// bashRun is a turn whose author calls bash once for each command in one
// tool step, each allowed by the referee.
type bashRun struct {
	h     *enginetest.Harness
	be    *probeBackend
	turn  string
	ws    string
	calls []journal.ToolCall
}

// runBashTurn runs the exec preset. The backend answers the self-test of
// engine start, the self-test of the turn and then one script per allowed
// command. allowed is how many commands the referee answers.
func runBashTurn(t *testing.T, commands []string, allowed int, extra string) *bashRun {
	t.Helper()
	network := strings.Contains(extra, "network_allow")
	area := t.TempDir()
	scripts := append(sandbox.FakeSelfTestScripts("", network), sandbox.FakeSelfTestScripts("", network)...)
	for i := 0; i < allowed; i++ {
		scripts = append(scripts, sandbox.FakeScript{Chunks: []sandbox.FakeChunk{{Data: []byte("out " + strconv.Itoa(i) + "\n")}}})
	}
	be := &probeBackend{Fake: sandbox.NewFake(allCaps, scripts...), area: area}
	var step []transport.ToolCall
	for i, c := range commands {
		args, _ := json.Marshal(map[string]string{"command": c})
		step = append(step, call("b"+strconv.Itoa(i), tools.NameBash, string(args)))
	}
	script := []fake.Step{toolStepWith(step...)}
	for i := 0; i < allowed; i++ {
		script = append(script, enginetest.Reply(commandAllow))
	}
	script = append(script, enginetest.Reply("final answer"))
	opts := append(refereeOpts(), enginetest.WithExec(engine.ExecOptions{
		Backend:  be,
		Shells:   []string{fakeShell(t, "bash")},
		SelfTest: sandbox.SelfTestConfig{Dir: t.TempDir()},
		TempDir:  area,
	}))
	if network {
		opts = append(opts[:len(opts)-1], enginetest.WithExec(engine.ExecOptions{
			Backend:  be,
			Shells:   []string{fakeShell(t, "bash")},
			SelfTest: sandbox.SelfTestConfig{Dir: t.TempDir(), Network: fixtureNetwork},
			TempDir:  area,
		}))
	}
	ws := workspaceDir(t)
	h := enginetest.New(t, execRoster("\n[tools]\nmax_calls_per_turn = 30\n"+extra), map[string][]fake.Step{"author": script}, opts...)
	reply, err := h.Engine.Submit(toolReq("s1", "exec", ws))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	return &bashRun{h: h, be: be, turn: reply.TurnID, ws: ws, calls: toolCalls(t, h, reply.TurnID)}
}

// A command over 4,096 bytes is refused before the scratch build: nothing is
// observed, no referee is asked and nothing starts.
func TestBashOverlongCommandRefusedBeforeScratchBuild(t *testing.T) {
	run := runBashTurn(t, []string{strings.Repeat("x", 4097)}, 0, "")
	wantCalls(t, run.calls, [2]string{journal.ToolCallError, "command_too_long"})
	if got := len(run.be.seen()); got != 0 {
		t.Fatalf("%d commands started", got)
	}
	err := run.h.Store.Tx(func(tx *journal.Tx) error {
		obs, err := tx.Observations(run.turn)
		if err != nil || len(obs) != 0 {
			t.Errorf("observations %d, err %v: the scratch was built", len(obs), err)
		}
		ds, err := tx.RefereeDecisionsByTurn(run.turn)
		if err != nil || len(ds) != 0 {
			t.Errorf("referee decisions %d, err %v", len(ds), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// With v5's bound raised above it, the author's twenty-first bash call is
// refused with a tool error and the attempt continues.
func TestBashAuthorTwentyFirstCallRefused(t *testing.T) {
	commands := make([]string, 21)
	for i := range commands {
		commands[i] = "echo " + strconv.Itoa(i)
	}
	run := runBashTurn(t, commands, 20, "")
	want := make([][2]string, 21)
	for i := range want {
		want[i] = [2]string{journal.ToolCallExecuted, ""}
	}
	want[20] = [2]string{journal.ToolCallError, "exec_call_limit"}
	wantCalls(t, run.calls, want...)
	if got := len(run.be.seen()); got != 20 {
		t.Fatalf("%d commands started, want 20", got)
	}
}

// A reviewer's eleventh bash call is refused with a tool error.
func TestBashReviewerEleventhCallRefused(t *testing.T) {
	var (
		mu    sync.Mutex
		codes []string
		once  sync.Once
		h     *enginetest.Harness
	)
	hook := func(turnID, attemptID, _ string) {
		once.Do(func() {
			for i := 0; i < 11; i++ {
				args, _ := json.Marshal(map[string]string{"command": "true"})
				var id string
				err := h.Store.Tx(func(tx *journal.Tx) error {
					rec, err := tx.RecordToolCall(attemptID, 10+i, "r"+strconv.Itoa(i), tools.NameBash, journal.ToolClassExec, args)
					id = rec.ID
					return err
				})
				if err != nil {
					t.Error(err)
					return
				}
				code, err := h.Engine.RunBashAs(turnID, "reviewer", id, "true")
				if err != nil {
					t.Error(err)
				}
				mu.Lock()
				codes = append(codes, code)
				mu.Unlock()
			}
		})
	}
	script := []fake.Step{toolStepWith(call("c1", tools.NameReadFile, `{"path":"a.txt"}`)), enginetest.Reply("final answer")}
	opts := append(refereeOpts(), enginetest.WithBeforeToolEffect(hook))
	h = enginetest.New(t, execRoster("\n[tools]\nmax_calls_per_turn = 30\n"), map[string][]fake.Step{"author": script}, opts...)
	reply, err := h.Engine.Submit(toolReq("s1", "exec", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	mu.Lock()
	defer mu.Unlock()
	if len(codes) != 11 || codes[10] != "exec_call_limit" {
		t.Fatalf("codes %v, want the eleventh refused by the call limit", codes)
	}
	for _, c := range codes[:10] {
		if c == "exec_call_limit" {
			t.Fatalf("codes %v: a call within the bound was refused by it", codes)
		}
	}
}

// What a command writes is absent from the next call, from the staging map,
// the change set and the workspace; each call's temporary directory is empty
// at its start and gone after it.
func TestBashWritesAreDiscarded(t *testing.T) {
	run := runBashTurn(t, []string{"echo a > gen.txt", "ls"}, 2, "")
	wantCalls(t, run.calls, [2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallExecuted, ""})
	runs := run.be.seen()
	if len(runs) != 2 {
		t.Fatalf("%d commands started, want 2", len(runs))
	}
	for i, r := range runs {
		if r.tmpEntries != 0 {
			t.Errorf("call %d: temporary directory held %d entries at its start", i, r.tmpEntries)
		}
		for _, n := range r.scratchNames {
			if n == "gen.txt" {
				t.Errorf("call %d: a file an earlier command wrote is in the scratch directory", i)
			}
		}
		if len(r.scratchNames) != 1 || r.scratchNames[0] != "a.txt" {
			t.Errorf("call %d: scratch directory holds %v, want the workspace's a.txt", i, r.scratchNames)
		}
		for _, d := range []string{r.scratch, r.tmp} {
			if _, err := os.Stat(d); !os.IsNotExist(err) {
				t.Errorf("call %d: %s survives the call (err %v)", i, d, err)
			}
		}
	}
	for _, n := range []string{"gen.txt", "tmp.txt"} {
		if _, err := os.Stat(filepath.Join(run.ws, n)); !os.IsNotExist(err) {
			t.Errorf("%s reached the workspace (err %v)", n, err)
		}
	}
	if es, err := os.ReadDir(run.ws); err != nil || len(es) != 1 || es[0].Name() != "a.txt" {
		t.Errorf("workspace holds %v, err %v, want only a.txt", es, err)
	}
	err := run.h.Store.Tx(func(tx *journal.Tx) error {
		staged, err := tx.Staging(run.turn)
		if err != nil || len(staged) != 0 {
			t.Errorf("staging %d entries, err %v", len(staged), err)
		}
		if _, err := tx.ChangeSetForTurn(run.turn); err == nil {
			t.Error("a change set exists")
		}
		ecs, err := tx.ExecCallsByTurn(run.turn)
		if err != nil || len(ecs) != 2 || ecs[0].RefereeDecisionID == "" || ecs[1].Result == nil {
			t.Errorf("exec calls %+v, err %v", ecs, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Under a preset with a change set, what a command writes is not staged and
// not frozen: the change set holds only the author's own write_file.
func TestBashWritesNeverReachChangeSet(t *testing.T) {
	network := false
	area := t.TempDir()
	scripts := append(sandbox.FakeSelfTestScripts("", network), sandbox.FakeSelfTestScripts("", network)...)
	scripts = append(scripts, sandbox.FakeScript{Chunks: []sandbox.FakeChunk{{Data: []byte("out\n")}}})
	be := &probeBackend{Fake: sandbox.NewFake(allCaps, scripts...), area: area}
	bashArgs, _ := json.Marshal(map[string]string{"command": "echo a > gen.txt"})
	author := []fake.Step{
		toolStepWith(
			call("w1", tools.NameWriteFile, `{"path":"b.txt","content":"new file\n"}`),
			call("b1", tools.NameBash, string(bashArgs)),
		),
		enginetest.Reply(commandAllow),
		enginetest.Reply(authorText),
	}
	reviewer := []fake.Step{enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})}
	roster := refereeRoster(`
[presets.execreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "write", "exec"]

[tools]
max_calls_per_turn = 30
`)
	opts := append(refereeOpts(), enginetest.WithExec(engine.ExecOptions{
		Backend:  be,
		Shells:   []string{fakeShell(t, "bash")},
		SelfTest: sandbox.SelfTestConfig{Dir: t.TempDir()},
		TempDir:  area,
	}))
	h := enginetest.New(t, roster, map[string][]fake.Step{"author": author, "reviewer": reviewer}, opts...)
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
	cs, err := h.Store.ChangeSetForTurn(reply.TurnID)
	if err != nil || len(cs.Paths) != 1 || cs.Paths[0].Path != "b.txt" {
		t.Fatalf("change set = %+v, %v", cs, err)
	}
	err = h.Store.Tx(func(tx *journal.Tx) error {
		staged, err := tx.Staging(reply.TurnID)
		if err != nil {
			return err
		}
		for _, e := range staged {
			if e.Path != "b.txt" {
				t.Errorf("staging holds %q, want only b.txt", e.Path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// fixtureNetwork is the self-test's proxy fixture, for a fake backend that
// connects to nothing.
func fixtureNetwork() (sandbox.NetworkFixture, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return sandbox.NetworkFixture{}, err
	}
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = ln.Close()
		return sandbox.NetworkFixture{}, err
	}
	return sandbox.NetworkFixture{
		Listener:  sandbox.Listener{Network: "tcp", Address: ln.Addr().String()},
		ProxyURL:  "http://syndic:x@" + ln.Addr().String(),
		OtherAddr: other.Addr().String(),
		Close:     func() { _ = ln.Close(); _ = other.Close() },
	}, nil
}

// A call under a network list has its own proxy listener, revoked when the
// call ends.
func TestBashProxyListenerRevokedWhenCallEnds(t *testing.T) {
	run := runBashTurn(t, []string{"curl example.com"}, 1, "\n[tools.exec]\nnetwork_allow = [\"example.com\"]\n")
	wantCalls(t, run.calls, [2]string{journal.ToolCallExecuted, ""})
	runs := run.be.seen()
	if len(runs) != 1 || runs[0].proxy == "" {
		t.Fatalf("runs %+v: the command had no proxy listener", runs)
	}
	if c, err := net.DialTimeout("tcp", runs[0].proxy, time.Second); err == nil {
		_ = c.Close()
		t.Fatalf("the proxy listener %s still accepts after the call", runs[0].proxy)
	}
}
