package local

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/mcp/wire"
	"github.com/boldfield/syndic/internal/sandbox"
)

// bareProcess hides the pipes of a server process, as a backend that does
// not honor Spec.ServerPipes returns one.
type bareProcess struct{ sandbox.Process }

// pipeBackend wraps the fake backend to rename it and, with bare, to return
// processes without server pipes.
type pipeBackend struct {
	*sandbox.Fake
	name string
	bare bool
}

func (b *pipeBackend) Name() string {
	if b.name != "" {
		return b.name
	}
	return b.Fake.Name()
}

func (b *pipeBackend) Start(ctx context.Context, spec sandbox.Spec) (sandbox.Process, error) {
	p, err := b.Fake.Start(ctx, spec)
	if err != nil || !b.bare {
		return p, err
	}
	return bareProcess{p}, nil
}

var longRunning = sandbox.FakeScript{Runtime: time.Hour}

func newInstances(t *testing.T, scripts ...sandbox.FakeScript) (*mcp.Instances, *pipeBackend, *journal.Store, *sandbox.Gate) {
	t.Helper()
	db, err := journal.Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := journal.NewStore(db)
	b := &pipeBackend{Fake: sandbox.NewFake(sandbox.Required(false), scripts...)}
	gate := &sandbox.Gate{}
	m := New(Config{
		Backend:  b,
		Sequence: sandbox.Sequence{KillGrace: 20 * time.Millisecond, CleanupDeadline: 100 * time.Millisecond},
		Gate:     gate,
		Journal:  store,
		Spec:     func(mcp.InstanceRequest) sandbox.Spec { return sandbox.Spec{Argv: []string{"srv"}} },
	})
	return m, b, store, gate
}

func request(kind, owner string) mcp.InstanceRequest {
	return mcp.InstanceRequest{InstanceRecord: mcp.InstanceRecord{Server: "srv", Kind: kind, Owner: owner}}
}

func eventKinds(t *testing.T, s *journal.Store) []string {
	t.Helper()
	evs, err := s.EventsSince(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		if strings.HasPrefix(e.Kind, "mcp_instance") || e.Kind == "mcp_containment_unverified" {
			kinds = append(kinds, e.Kind)
		}
	}
	return kinds
}

func unresolved(t *testing.T, s *journal.Store) []journal.MCPInstance {
	t.Helper()
	var out []journal.MCPInstance
	if err := s.Tx(func(tx *journal.Tx) error {
		var err error
		out, err = tx.UnresolvedMCPInstances("")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInstanceStartJournaledBeforeProcessAndEndAfterConfirmation(t *testing.T) {
	m, b, store, _ := newInstances(t, longRunning)
	inst, err := m.Open(t.Context(), request(mcp.TurnInstance, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if got := eventKinds(t, store); len(got) != 1 || got[0] != "mcp_instance_started" {
		t.Fatalf("events after start = %v", got)
	}
	if u := unresolved(t, store); len(u) != 1 || u[0].Owner != "alice" || u[0].Kind != journal.MCPInstanceTurn {
		t.Fatalf("unresolved = %+v", u)
	}
	if s := b.Starts(); len(s) != 1 || !s[0].ServerPipes {
		t.Fatalf("starts = %+v", s)
	}
	if err := inst.End(); err != nil {
		t.Fatal(err)
	}
	if got := eventKinds(t, store); len(got) != 2 || got[1] != "mcp_instance_ended" {
		t.Fatalf("events after end = %v", got)
	}
	if len(unresolved(t, store)) != 0 {
		t.Fatal("ended instance still unresolved")
	}
	if err := inst.End(); err != nil {
		t.Fatalf("second End = %v", err)
	}
	if got := eventKinds(t, store); len(got) != 2 {
		t.Fatalf("second End journaled again: %v", got)
	}
	if terms := b.Terminations(); len(terms) == 0 || terms[0].Signal != syscall.SIGTERM {
		t.Fatalf("terminations = %+v", terms)
	}
}

func TestInstanceEndNotConfirmedStaysUnresolved(t *testing.T) {
	m, _, store, _ := newInstances(t, sandbox.FakeScript{Runtime: time.Hour, NeverConfirm: true})
	inst, err := m.Open(t.Context(), request(mcp.TurnInstance, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.End(); !errors.Is(err, mcp.ErrContainmentUnverified) {
		t.Fatalf("End = %v", err)
	}
	for _, k := range eventKinds(t, store) {
		if k == "mcp_instance_ended" {
			t.Fatal("ended journaled without confirmation")
		}
	}
	if u := unresolved(t, store); len(u) != 1 || !u[0].ContainmentUnverified() {
		t.Fatalf("unresolved = %+v", u)
	}
}

func TestStartFailureIsNotOffered(t *testing.T) {
	boom := errors.New("boom")
	m, _, store, _ := newInstances(t, sandbox.FakeScript{StartErr: boom})
	_, err := mcp.Offer(t.Context(), m, request(mcp.ListingInstance, ""), time.Second,
		func(context.Context, *mcp.Instance) (int, error) { t.Fatal("listed"); return 0, nil })
	if !errors.Is(err, mcp.ErrNotOffered) || !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if len(unresolved(t, store)) != 0 {
		t.Fatal("a failed start left an unresolved instance")
	}
}

func TestOfferTimeoutIsNotOfferedAndEndsInstance(t *testing.T) {
	m, _, store, _ := newInstances(t, longRunning)
	_, err := mcp.Offer(t.Context(), m, request(mcp.ListingInstance, ""), 30*time.Millisecond,
		func(ctx context.Context, inst *mcp.Instance) (int, error) {
			_, err := wire.NewLineReader(inst.Stdout(), wire.Limits{}).Next() // the server never answers
			return 0, err
		})
	if !errors.Is(err, mcp.ErrNotOffered) {
		t.Fatalf("err = %v", err)
	}
	if len(unresolved(t, store)) != 0 {
		t.Fatal("listing instance not ended")
	}
}

func TestOfferReturnsListingAndEnds(t *testing.T) {
	m, _, store, _ := newInstances(t, longRunning)
	v, err := mcp.Offer(t.Context(), m, request(mcp.ListingInstance, ""), time.Second,
		func(context.Context, *mcp.Instance) (int, error) { return 7, nil })
	if err != nil || v != 7 {
		t.Fatalf("v, err = %d, %v", v, err)
	}
	if len(unresolved(t, store)) != 0 {
		t.Fatal("listing instance not ended")
	}
}

func TestInstancePipes(t *testing.T) {
	m, b, _, _ := newInstances(t, longRunning)
	inst, err := m.Open(t.Context(), request(mcp.TurnInstance, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	p := b.Server(0)
	go func() {
		_, _ = p.ServerOut().Write([]byte("{\"jsonrpc\":\"2.0\"}\n"))
		_, _ = p.ServerErr().Write([]byte("log line\n"))
	}()
	msg, err := wire.NewLineReader(inst.Stdout(), wire.Limits{}).Next()
	if err != nil || string(msg) != `{"jsonrpc":"2.0"}` {
		t.Fatalf("stdout message = %q, %v", msg, err)
	}
	sink := inst.Stderr().(*wire.StderrSink)
	deadline := time.Now().Add(time.Second)
	for string(sink.Bytes()) != "log line\n" {
		if time.Now().After(deadline) {
			t.Fatalf("stderr sink = %q", sink.Bytes())
		}
		time.Sleep(time.Millisecond)
	}
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 5)
		_, _ = io.ReadFull(p.ServerIn(), buf)
		got <- string(buf)
	}()
	if _, err := inst.Stdin().Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if s := <-got; s != "hello" {
		t.Fatalf("stdin = %q", s)
	}
	_ = inst.End()
}

func TestBackendWithoutPipesCannotRunServer(t *testing.T) {
	m, b, store, _ := newInstances(t, longRunning)
	b.bare = true
	if _, err := m.Open(t.Context(), request(mcp.TurnInstance, "alice")); !errors.Is(err, ErrNoServerPipes) {
		t.Fatalf("err = %v", err)
	}
	if len(unresolved(t, store)) != 0 {
		t.Fatal("instance left unresolved")
	}
}

func cancelled() mcp.CallReply {
	return mcp.CallReply{Kind: mcp.CallCancelled, Sent: true, Err: context.DeadlineExceeded}
}

func TestCancelledCallEndsTurnAndApplyInstances(t *testing.T) {
	for _, kind := range []string{mcp.TurnInstance, mcp.ApplyInstance} {
		t.Run(kind, func(t *testing.T) {
			m, b, store, _ := newInstances(t, longRunning, longRunning)
			inst, err := m.Open(t.Context(), request(kind, "o"))
			if err != nil {
				t.Fatal(err)
			}
			if r := inst.Call(t.Context(), func(context.Context) mcp.CallReply { return cancelled() }); r.Kind != mcp.CallCancelled {
				t.Fatalf("reply = %v", r.Kind)
			}
			if !inst.Ended() || len(unresolved(t, store)) != 0 {
				t.Fatal("instance not ended after cancel")
			}
			r := inst.Call(t.Context(), func(context.Context) mcp.CallReply { t.Fatal("sent"); return mcp.CallReply{} })
			if r.Kind != mcp.CallNotSent || !errors.Is(r.Err, mcp.ErrInstanceEnded) {
				t.Fatalf("later call on ended instance = %+v", r)
			}
			// A later call starts a new one.
			if _, err := m.Open(t.Context(), request(kind, "o")); err != nil {
				t.Fatal(err)
			}
			if len(b.Starts()) != 2 {
				t.Fatalf("starts = %d", len(b.Starts()))
			}
		})
	}
}

func TestNormalCallLeavesInstanceRunning(t *testing.T) {
	m, _, _, _ := newInstances(t, longRunning)
	inst, err := m.Open(t.Context(), request(mcp.TurnInstance, "o"))
	if err != nil {
		t.Fatal(err)
	}
	r := inst.Call(t.Context(), func(context.Context) mcp.CallReply { return mcp.CallReply{Kind: mcp.CallResult, Sent: true} })
	if r.Kind != mcp.CallResult || inst.Ended() {
		t.Fatalf("reply %v ended %v", r.Kind, inst.Ended())
	}
	_ = inst.End()
}

func TestRunInstanceIsSharedAndSurvivesCancel(t *testing.T) {
	m, b, store, _ := newInstances(t, longRunning, longRunning)
	a, err := m.Open(t.Context(), request(mcp.RunInstance, ""))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Open(t.Context(), request(mcp.RunInstance, ""))
	if err != nil || a != c {
		t.Fatalf("run instance not shared: %v", err)
	}
	a.Call(t.Context(), func(context.Context) mcp.CallReply { return cancelled() })
	if a.Ended() || len(b.Starts()) != 1 {
		t.Fatal("run instance ended by a cancelled call")
	}
	if err := m.EndRun(); err != nil {
		t.Fatal(err)
	}
	if len(unresolved(t, store)) != 0 {
		t.Fatal("run instance unresolved after EndRun")
	}
}

func TestGateCheckedBeforeStartAndBeforeCall(t *testing.T) {
	m, b, _, gate := newInstances(t, longRunning, longRunning)
	inst, err := m.Open(t.Context(), request(mcp.TurnInstance, "o"))
	if err != nil {
		t.Fatal(err)
	}
	gate.Close("containment unverified")
	r := inst.Call(t.Context(), func(context.Context) mcp.CallReply { t.Fatal("sent past a closed gate"); return mcp.CallReply{} })
	if r.Kind != mcp.CallNotSent || !errors.Is(r.Err, sandbox.ErrGateClosed) {
		t.Fatalf("reply = %+v", r)
	}
	if _, err := m.Open(t.Context(), request(mcp.TurnInstance, "o")); !errors.Is(err, sandbox.ErrGateClosed) {
		t.Fatalf("start past closed gate: %v", err)
	}
	if len(b.Starts()) != 1 {
		t.Fatalf("starts = %d", len(b.Starts()))
	}
	_ = inst.End()
}

func TestUnconfinedNoticeNamesServer(t *testing.T) {
	db, err := journal.Open(filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b := &pipeBackend{Fake: sandbox.NewFake(sandbox.Caps(sandbox.D), longRunning, longRunning), name: sandbox.NoneName}
	var lines []string
	m := New(Config{
		Backend:  b,
		Sequence: sandbox.Sequence{KillGrace: 20 * time.Millisecond, CleanupDeadline: 100 * time.Millisecond},
		Journal:  journal.NewStore(db),
		Spec:     func(mcp.InstanceRequest) sandbox.Spec { return sandbox.Spec{Argv: []string{"srv"}} },
		Notice:   func(l string) { lines = append(lines, l) },
	})
	for range 2 {
		inst, err := m.Open(t.Context(), request(mcp.TurnInstance, "o"))
		if err != nil {
			t.Fatal(err)
		}
		if !inst.Unconfined() {
			t.Fatal("not marked unconfined")
		}
		_ = inst.End()
	}
	if len(lines) != 1 || lines[0] != mcp.UnconfinedLine("srv", sandbox.NoneNotice) ||
		!strings.Contains(lines[0], `"srv"`) || !strings.Contains(lines[0], sandbox.NoneNotice) {
		t.Fatalf("lines = %q", lines)
	}
}

func TestBackendWithoutPipesUnconfirmedStaysUnresolved(t *testing.T) {
	m, b, store, _ := newInstances(t, sandbox.FakeScript{Runtime: time.Hour, NeverConfirm: true})
	b.bare = true
	_, err := m.Open(t.Context(), request(mcp.TurnInstance, "alice"))
	if !errors.Is(err, ErrNoServerPipes) || !errors.Is(err, mcp.ErrContainmentUnverified) {
		t.Fatalf("err = %v", err)
	}
	if len(unresolved(t, store)) != 1 {
		t.Fatalf("unresolved = %v", unresolved(t, store))
	}
	for _, k := range eventKinds(t, store) {
		if k == "mcp_instance_ended" {
			t.Fatal("end journaled without confirmation")
		}
	}
}

func TestOfferOnRunInstanceSharesItAndLeavesItRunning(t *testing.T) {
	m, b, store, _ := newInstances(t, longRunning, longRunning)
	run, err := m.Open(t.Context(), request(mcp.RunInstance, ""))
	if err != nil {
		t.Fatal(err)
	}
	var got *mcp.Instance
	_, err = mcp.Offer(t.Context(), m, request(mcp.RunInstance, ""), time.Second,
		func(_ context.Context, inst *mcp.Instance) (int, error) { got = inst; return 1, nil })
	if err != nil || got != run || run.Ended() || len(b.Starts()) != 1 {
		t.Fatalf("err %v shared %v ended %v starts %d", err, got == run, run.Ended(), len(b.Starts()))
	}
	// A run server that does not answer is not offered and is ended.
	_, err = mcp.Offer(t.Context(), m, request(mcp.RunInstance, ""), 30*time.Millisecond,
		func(ctx context.Context, _ *mcp.Instance) (int, error) { <-ctx.Done(); return 0, ctx.Err() })
	if !errors.Is(err, mcp.ErrNotOffered) || !run.Ended() || len(unresolved(t, store)) != 0 {
		t.Fatalf("err %v ended %v", err, run.Ended())
	}
}

func TestEndRunWaitsForRunInstanceStart(t *testing.T) {
	m, b, store, _ := newInstances(t, longRunning)
	opened := make(chan *mcp.Instance, 1)
	go func() {
		inst, _ := m.Open(t.Context(), request(mcp.RunInstance, ""))
		opened <- inst
	}()
	for len(b.Starts()) == 0 {
		time.Sleep(time.Millisecond)
	}
	if err := m.EndRun(); err != nil {
		t.Fatal(err)
	}
	if inst := <-opened; inst != nil && !inst.Ended() {
		t.Fatal("run instance left running after EndRun")
	}
	if len(unresolved(t, store)) != 0 {
		t.Fatal("instance unresolved")
	}
	if _, err := m.Open(t.Context(), request(mcp.RunInstance, "")); !errors.Is(err, mcp.ErrInstanceEnded) {
		t.Fatalf("open after EndRun: %v", err)
	}
}

func TestStderrScrubbed(t *testing.T) {
	l := mcp.NewScrubList()
	l.AddEnv(map[string]string{"API_KEY": "env-secret-9"})
	m := New(Config{Scrub: l, Backend: sandbox.NewFake(sandbox.Required(false))})
	w := m.Stderr("srv")
	_, _ = w.Write([]byte("bad key env-secret-9\n"))
	if got := string(w.(*wire.StderrSink).Bytes()); strings.Contains(got, "env-secret-9") || got == "" {
		t.Fatalf("stderr = %q", got)
	}
}

// A secret cut by the stderr bound leaves no part behind, and a whole secret
// ending at the bound is scrubbed whole, even when its suffix begins another
// listed value or itself.
func TestStderrSecretAtBoundIsNotLeaked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   map[string]string
		write string
		limit int
		leaks []string
	}{
		{"crosses bound", map[string]string{"K": "SUPERSECRETVALUE1234"}, "error: SUPERSECRETVALUE1234\n", 20, []string{"SUPER", "ECRET"}},
		{"suffix begins another", map[string]string{"A": "abcdXYZW", "B": "XYZW1234"}, "err: abcdXYZW and more", 13, []string{"abcd", "XYZ"}},
		{"suffix begins itself", map[string]string{"A": "S3cr3tS3cr3t"}, "err: S3cr3tS3cr3t and more", 17, []string{"S3c"}},
		{"cut value holds another", map[string]string{"A": "abXYZcd", "B": "XYZ"}, "err: abXYZcd", 10, []string{"ab", "XYZ", "c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := mcp.NewScrubList()
			l.AddEnv(tc.env)
			m := New(Config{Scrub: l, StderrLimit: tc.limit, Backend: sandbox.NewFake(sandbox.Required(false))})
			w := m.Stderr("srv")
			_, _ = w.Write([]byte(tc.write))
			got := strings.TrimPrefix(string(w.(*wire.StderrSink).Bytes()), "err")
			for _, leak := range tc.leaks {
				if strings.Contains(got, leak) {
					t.Fatalf("stderr %q leaks %q", got, leak)
				}
			}
		})
	}
}
