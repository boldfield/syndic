package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// parentEnv makes the test binary act as an engine that starts one command
// in the directory it names and then waits to be killed; see parentMain.
const parentEnv = "SYNDIC_SANDBOX_TEST_PARENT"

// TestMain lets the test binary stand in for syndicd: started with
// SupervisorArg it is a supervisor, as syndicd is.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == SupervisorArg {
		os.Exit(Supervise(os.Stderr))
	}
	if os.Getenv(parentEnv) != "" {
		os.Exit(parentMain())
	}
	os.Exit(m.Run())
}

// testSupervisor starts the test binary as the supervisor.
func testSupervisor(tb testing.TB, seq Sequence) Supervisor {
	tb.Helper()
	exe, err := os.Executable()
	if err != nil {
		tb.Fatal(err)
	}
	return Supervisor{Command: []string{exe, SupervisorArg}, Sequence: seq}
}

func shellSpec(dir, script string) Spec {
	return Spec{
		Argv:       []string{"/bin/sh", "-c", script},
		ScratchDir: dir,
		TempDir:    dir,
		Env:        Env(EnvConfig{Path: "/usr/local/bin:/usr/bin:/bin", TempDir: dir}),
		Timeout:    time.Minute,
	}
}

// Scripts that print "ready" once their handling of SIGTERM is in place and
// then run until terminated, with a second process in their group.
const (
	scriptHonoursTerm = `sleep 1000 & echo ready; wait`
	scriptIgnoresTerm = `trap "" TERM; sleep 1000 & echo ready; wait`
)

// running is a command under the unsandboxed backend and its output.
type running struct {
	p      *supervisedProcess
	mu     sync.Mutex
	out    bytes.Buffer
	eof    chan struct{}
	waited chan struct{}
	status ExitStatus
	err    error
}

func startRunning(t *testing.T, seq Sequence, script string) *running {
	t.Helper()
	proc, err := NewNone(testSupervisor(t, seq)).Start(context.Background(), shellSpec(t.TempDir(), script))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r := &running{p: proc.(*supervisedProcess), eof: make(chan struct{}), waited: make(chan struct{})}
	go func() {
		defer close(r.eof)
		buf := make([]byte, 4096)
		for {
			n, err := r.p.Output().Read(buf)
			r.mu.Lock()
			r.out.Write(buf[:n])
			r.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(r.waited)
		r.status, r.err = r.p.Wait()
	}()
	t.Cleanup(func() {
		_ = r.p.Terminate(syscall.SIGKILL)
		_ = r.p.Output().Close()
		select {
		case <-r.p.reaped:
		case <-time.After(30 * time.Second):
			t.Errorf("supervisor did not exit")
		}
	})
	return r
}

func (r *running) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}

// waitReady waits for the command to print "ready".
func (r *running) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for !strings.Contains(r.output(), "ready") {
		select {
		case <-r.eof:
			t.Fatalf("command ended before it was ready; output %q", r.output())
		case <-deadline:
			t.Fatalf("command not ready; output %q", r.output())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (r *running) pgid() int {
	r.p.mu.Lock()
	defer r.p.mu.Unlock()
	return r.p.pid
}

func (r *running) wait(t *testing.T) ExitStatus {
	t.Helper()
	select {
	case <-r.waited:
	case <-time.After(30 * time.Second):
		t.Fatal("Wait did not return")
	}
	if r.err != nil {
		t.Fatalf("Wait: %v", r.err)
	}
	return r.status
}

// groupGone waits until no process of the group remains.
func groupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still has processes", pgid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// recorder logs what the sequence does to a Process, in order.
type recorder struct {
	Terminable
	mu      sync.Mutex
	events  []string
	confirm func(ctx context.Context) error // replaces ConfirmEmpty when set
}

func (r *recorder) log(e string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) Events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.events...)
}

func (r *recorder) Terminate(sig syscall.Signal) error {
	r.log(fmt.Sprintf("signal %d", sig))
	return r.Terminable.Terminate(sig)
}

func (r *recorder) ConfirmEmpty(ctx context.Context) error {
	r.log("confirm")
	if r.confirm != nil {
		return r.confirm(ctx)
	}
	return r.Terminable.ConfirmEmpty(ctx)
}

func (r *recorder) signalled(sig syscall.Signal) bool {
	for _, e := range r.Events() {
		if e == fmt.Sprintf("signal %d", sig) {
			return true
		}
	}
	return false
}

func TestSequenceSIGTERMSuffices(t *testing.T) {
	const grace = 30 * time.Second
	r := startRunning(t, Sequence{KillGrace: grace, CleanupDeadline: 10 * time.Second}, scriptHonoursTerm)
	r.waitReady(t)
	rec := &recorder{Terminable: r.p}
	start := time.Now()
	if !(Sequence{KillGrace: grace, CleanupDeadline: 10 * time.Second}).Run(rec, false, nil) {
		t.Fatal("containment unverified")
	}
	if elapsed := time.Since(start); elapsed >= grace {
		t.Errorf("sequence took %v, the whole grace period", elapsed)
	}
	if rec.signalled(syscall.SIGKILL) {
		t.Errorf("a command that exits on SIGTERM was sent SIGKILL: %q", rec.Events())
	}
	if st := r.wait(t); st.Signal != int(syscall.SIGTERM) || !st.Terminated {
		t.Errorf("status %+v, want SIGTERM and terminated", st)
	}
}

func TestSequenceKillsAfterGrace(t *testing.T) {
	const grace = 300 * time.Millisecond
	seq := Sequence{KillGrace: grace, CleanupDeadline: 10 * time.Second}
	r := startRunning(t, seq, scriptIgnoresTerm)
	r.waitReady(t)
	pgid := r.pgid()
	rec := &recorder{Terminable: r.p}
	start := time.Now()
	if !seq.Run(rec, false, nil) {
		t.Fatal("containment unverified")
	}
	if elapsed := time.Since(start); elapsed < grace {
		t.Errorf("SIGKILL after %v, before the grace period of %v", elapsed, grace)
	}
	if !rec.signalled(syscall.SIGKILL) {
		t.Errorf("a command that ignores SIGTERM was not sent SIGKILL: %q", rec.Events())
	}
	if st := r.wait(t); st.Signal != int(syscall.SIGKILL) || !st.Terminated {
		t.Errorf("status %+v, want SIGKILL and terminated", st)
	}
	groupGone(t, pgid)
}

func TestSequenceSkipsGraceOnShellExit(t *testing.T) {
	const grace = 30 * time.Second
	seq := Sequence{KillGrace: grace, CleanupDeadline: 10 * time.Second}
	r := startRunning(t, seq, `exit 3`)
	if st := r.wait(t); st.Code != 3 || st.Signal != 0 || st.Terminated {
		t.Errorf("status %+v, want exit 3 not terminated", st)
	}
	rec := &recorder{Terminable: r.p}
	start := time.Now()
	if !seq.Run(rec, true, nil) {
		t.Fatal("containment unverified")
	}
	if elapsed := time.Since(start); elapsed >= grace {
		t.Errorf("sequence took %v: the grace wait was not skipped", elapsed)
	}
	want := []string{"signal 15", "signal 9", "confirm"}
	if got := rec.Events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events %q, want %q", got, want)
	}
}

func TestSequenceRevokesBeforeAnyWait(t *testing.T) {
	seq := Sequence{KillGrace: 100 * time.Millisecond, CleanupDeadline: 10 * time.Second}
	r := startRunning(t, seq, scriptIgnoresTerm)
	r.waitReady(t)
	rec := &recorder{Terminable: r.p}
	if !seq.Run(rec, false, func() { rec.log("revoke") }) {
		t.Fatal("containment unverified")
	}
	got := rec.Events()
	if len(got) < 2 || got[0] != "revoke" || got[1] != "signal 15" {
		t.Errorf("events %q: the revocation hook did not run first", got)
	}
	for i, e := range got {
		if e == "revoke" && i != 0 {
			t.Errorf("revocation hook ran more than once: %q", got)
		}
	}
}

func TestSequenceUnverifiedAtCleanupDeadline(t *testing.T) {
	const deadline = 300 * time.Millisecond
	seq := Sequence{KillGrace: 50 * time.Millisecond, CleanupDeadline: deadline}
	r := startRunning(t, seq, scriptHonoursTerm)
	r.waitReady(t)
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	rec := &recorder{Terminable: r.p, confirm: func(context.Context) error {
		<-never // ignores its context and never returns while the test runs
		return nil
	}}
	start := time.Now()
	if seq.Run(rec, false, nil) {
		t.Fatal("confirmed with a ConfirmEmpty that never returns")
	}
	elapsed := time.Since(start)
	if elapsed < deadline {
		t.Errorf("gave up after %v, before the cleanup deadline of %v", elapsed, deadline)
	}
	if elapsed > deadline+10*time.Second {
		t.Errorf("waited %v, far beyond the cleanup deadline of %v", elapsed, deadline)
	}
	if !rec.signalled(syscall.SIGKILL) {
		t.Errorf("SIGKILL not sent: %q", rec.Events())
	}
}

func TestSupervisorTerminatesOnControlEOF(t *testing.T) {
	seq := Sequence{KillGrace: 100 * time.Millisecond, CleanupDeadline: 10 * time.Second}
	r := startRunning(t, seq, scriptIgnoresTerm)
	r.waitReady(t)
	pgid := r.pgid()
	// The engine's end closes, as on the engine's death.
	_ = r.p.ctl.Close()
	groupGone(t, pgid)
	select {
	case <-r.p.reaped:
	case <-time.After(30 * time.Second):
		t.Fatal("supervisor did not exit")
	}
	select {
	case <-r.eof:
	case <-time.After(30 * time.Second):
		t.Error("the pipe did not reach end of file")
	}
}

// parentMain is an engine that starts one command that ignores SIGTERM,
// prints the command's process group, and waits to be killed.
func parentMain() int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("error", err)
		return 1
	}
	dir := os.Getenv(parentEnv)
	sup := Supervisor{Command: []string{exe, SupervisorArg}, Sequence: Sequence{KillGrace: 100 * time.Millisecond, CleanupDeadline: 10 * time.Second}}
	proc, err := NewNone(sup).Start(context.Background(), shellSpec(dir, scriptIgnoresTerm))
	if err != nil {
		fmt.Println("error", err)
		return 1
	}
	out := bufio.NewReader(proc.Output())
	line, err := out.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		fmt.Println("error", line, err)
		return 1
	}
	p := proc.(*supervisedProcess)
	p.mu.Lock()
	pgid := p.pid
	p.mu.Unlock()
	fmt.Println("pgid", pgid)
	time.Sleep(time.Hour)
	return 0
}

func TestSupervisorTerminatesWhenEngineKilled(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), parentEnv+"="+t.TempDir())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- line
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(30 * time.Second):
		t.Fatal("parent did not start its command")
	}
	pgid, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(line), "pgid "))
	if err != nil || pgid <= 0 {
		t.Fatalf("parent printed %q", line)
	}
	if err := syscall.Kill(pgid, 0); err != nil {
		t.Fatalf("command not running: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	groupGone(t, pgid)
}

func TestSupervisorReportsStartFailure(t *testing.T) {
	spec := shellSpec(t.TempDir(), "")
	spec.Argv = []string{"syndic-no-such-shell"}
	_, err := NewNone(testSupervisor(t, Sequence{})).Start(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "syndic-no-such-shell") {
		t.Errorf("Start of a missing program: %v", err)
	}
}

func TestSupervisorPassesExactlyThePipe(t *testing.T) {
	r := startRunning(t, Sequence{}, `cat; echo out; echo err >&2`)
	if st := r.wait(t); st.Code != 0 {
		t.Fatalf("status %+v", st)
	}
	select {
	case <-r.eof:
	case <-time.After(30 * time.Second):
		t.Fatal("the pipe did not reach end of file")
	}
	// cat returned at once: standard input is empty, /dev/null.
	if got := r.output(); got != "out\nerr\n" {
		t.Errorf("output %q, want standard output and error on the one pipe", got)
	}
}

func TestSuperviseRefusesWithoutControl(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra func(t *testing.T) []*os.File
	}{
		{"none", func(*testing.T) []*os.File { return nil }},
		{"file", func(t *testing.T) []*os.File {
			f, err := os.CreateTemp(t.TempDir(), "fd3")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.Close() })
			return []*os.File{f, f}
		}},
		{"pipe", func(t *testing.T) []*os.File {
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
			return []*os.File{pr, pw}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], SupervisorArg)
			cmd.ExtraFiles = tc.extra(t)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 2 {
				t.Fatalf("supervisor without a control descriptor: %v, want exit status 2; stderr %q", err, stderr.String())
			}
			if !strings.Contains(stderr.String(), "internal to the engine") {
				t.Errorf("stderr %q", stderr.String())
			}
		})
	}
}

func TestNoneBackend(t *testing.T) {
	n := NewNone(Supervisor{})
	if n.Name() != "none" {
		t.Errorf("name %q", n.Name())
	}
	caps, reason := n.Capabilities()
	if caps != Caps(D) {
		t.Errorf("capabilities %s, want D only", caps)
	}
	if reason != NoneNotice {
		t.Errorf("reason %q", reason)
	}
	if ok, _ := Supported(caps, false); ok {
		t.Error("the unsandboxed backend is supported under required")
	}
}
