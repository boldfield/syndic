// Package contracttest is the enforcement suite every real sandbox backend
// must pass, one subtest per clause of the "Sandbox, on each real backend"
// paragraph of the v7 delta, section 9, that concerns a backend alone.
//
// The suite runs a small helper program, built from testdata/helper with the
// Go toolchain at test time and placed in each call's temporary directory, as
// the self-test of section 3 places its helper.
package contracttest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

// Clause names one subtest of the suite.
type Clause string

const (
	// Baseline is a positive control: the command runs, reads its scratch
	// directory and writes its scratch and temporary directories. Without
	// it a backend that runs nothing would pass every denial.
	Baseline Clause = "baseline"
	// ReadOutside: a read outside the scratch, temporary and system
	// locations fails, whether the backend denies the path or hides it.
	ReadOutside Clause = "read_outside"
	// ProtectedAbsent: a protected path is not found (ENOENT), neither in
	// the live workspace nor in the scratch directory. A backend that
	// exposes the path but denies the read (EACCES) fails this clause: the
	// design requires that the command finds no such file.
	ProtectedAbsent Clause = "protected_absent"
	// WriteOutside: a write outside the scratch and temporary directories
	// fails and leaves no file.
	WriteOutside Clause = "write_outside"
	// OtherScratch: another participant's scratch directory is unreadable.
	OtherScratch Clause = "other_scratch"
	// NoSignal: the command cannot signal the test process.
	NoSignal Clause = "no_signal"
	// Descriptors: no descriptor beyond the three is inherited.
	Descriptors Clause = "descriptors"
	// NoNetwork: with no listener, a connection of any family fails,
	// loopback included, and no listener sees it.
	NoNetwork Clause = "no_network"
	// Environment: the environment is exactly the fixed set and the
	// passthrough names.
	Environment Clause = "environment"
	// DetachShellExit: a helper that starts a new session and detaches does
	// not survive the shell's exit.
	DetachShellExit Clause = "detach_shell_exit"
	// DetachTimeout: the same, when the call is terminated at its timeout.
	DetachTimeout Clause = "detach_timeout"
	// DetachTerminate: the same, when the call is terminated early.
	DetachTerminate Clause = "detach_terminate"
	// ConfirmAfterTerminate: ConfirmEmpty returns after termination.
	ConfirmAfterTerminate Clause = "confirm_after_terminate"
)

// AllClauses returns every clause in the order the suite runs them.
func AllClauses() []Clause {
	return []Clause{
		Baseline,
		ReadOutside,
		ProtectedAbsent,
		WriteOutside,
		OtherScratch,
		NoSignal,
		Descriptors,
		NoNetwork,
		Environment,
		DetachShellExit,
		DetachTimeout,
		DetachTerminate,
		ConfirmAfterTerminate,
	}
}

// Fixtures is what a backend's test supplies to the suite.
type Fixtures struct {
	// Root is where the suite creates each subtest's directories: the
	// workspace, the scratch directories, the temporary directory and a
	// directory outside every allowed location. Empty means t.TempDir().
	Root string
	// Path is tools.exec.path; empty means the design default.
	Path string
	// ReadOnlyPaths is tools.exec.read_only_paths; nil means the design
	// default for the platform.
	ReadOnlyPaths []string
	// Passthrough holds env_passthrough names and the engine's values for
	// them.
	Passthrough map[string]string
	// Timeout is the call timeout of the timeout clause; zero means one
	// second.
	Timeout time.Duration
	// KillGrace is the wait between SIGTERM and SIGKILL; zero means 200ms.
	KillGrace time.Duration
	// CleanupDeadline bounds ConfirmEmpty; zero means the design default of
	// ten seconds.
	CleanupDeadline time.Duration
}

// DefaultPath is the design default of tools.exec.path.
const DefaultPath = "/usr/local/bin:/usr/bin:/bin"

// DefaultReadOnlyPaths returns the design default of
// tools.exec.read_only_paths for the platform.
func DefaultReadOnlyPaths() []string {
	if runtime.GOOS == "darwin" {
		return []string{"/usr", "/System/Library", "/Library/Developer/CommandLineTools", "/private/etc/ssl"}
	}
	return []string{"/usr", "/lib", "/lib64", "/etc/ssl", "/etc/pki", "/etc/alternatives"}
}

// Names shared with testdata/helper.
const (
	helperName = "helper"
	beatFile   = "beat"
	stopFile   = "stop"
	beatEvery  = 50 * time.Millisecond
	// protectedName is a protected file present in the live workspace and
	// never materialised in the scratch directory.
	protectedName = ".env"
)

// Run executes every clause against backend as a subtest of t. Subtests run
// one after another, never in parallel.
func Run(t *testing.T, backend sandbox.Backend, fx Fixtures) {
	t.Helper()
	helper := BuildHelper(t)
	for _, c := range AllClauses() {
		fn := clauses[c]
		t.Run(string(c), func(t *testing.T) {
			fn(t, newCall(t, backend, fx, helper))
		})
	}
}

// BuildHelper builds testdata/helper with the Go toolchain into a temporary
// directory and returns the binary's path.
func BuildHelper(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("contracttest: cannot locate the helper source")
	}
	src := filepath.Join(filepath.Dir(file), "testdata", "helper")
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("contracttest: Go toolchain not found: %v", err)
	}
	out := filepath.Join(t.TempDir(), helperName)
	cmd := exec.Command(goTool, "build", "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("contracttest: building helper: %v\n%s", err, b)
	}
	return out
}

var clauses = map[Clause]func(*testing.T, *call){
	Baseline:              testBaseline,
	ReadOutside:           testReadOutside,
	ProtectedAbsent:       testProtectedAbsent,
	WriteOutside:          testWriteOutside,
	OtherScratch:          testOtherScratch,
	NoSignal:              testNoSignal,
	Descriptors:           testDescriptors,
	NoNetwork:             testNoNetwork,
	Environment:           testEnvironment,
	DetachShellExit:       func(t *testing.T, c *call) { testDetach(t, c, "exit") },
	DetachTimeout:         func(t *testing.T, c *call) { testDetach(t, c, "timeout") },
	DetachTerminate:       func(t *testing.T, c *call) { testDetach(t, c, "terminate") },
	ConfirmAfterTerminate: testConfirmAfterTerminate,
}

// call is one subtest's directories and the spec its command runs under.
type call struct {
	backend   sandbox.Backend
	fx        Fixtures
	workspace string // the live workspace, holding a protected file
	scratch   string // this participant's scratch directory
	other     string // another participant's scratch directory
	outside   string // a directory outside every allowed location
	tmp       string // the per-call temporary directory
	helper    string // the helper, inside tmp
	env       []string
}

func newCall(t *testing.T, backend sandbox.Backend, fx Fixtures, helperBin string) *call {
	t.Helper()
	root := fx.Root
	if root == "" {
		root = t.TempDir()
	} else {
		dir, err := os.MkdirTemp(root, "contract-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		root = dir
	}
	c := &call{
		backend:   backend,
		fx:        fx,
		workspace: filepath.Join(root, "workspace"),
		scratch:   filepath.Join(root, "scratch-a"),
		other:     filepath.Join(root, "scratch-b"),
		outside:   filepath.Join(root, "outside"),
		tmp:       filepath.Join(root, "tmp-call"),
	}
	for _, d := range []string{c.workspace, c.scratch, c.other, c.outside, c.tmp} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(c.workspace, protectedName), "SECRET=workspace\n", 0o600)
	writeFile(t, filepath.Join(c.workspace, "main.go"), "package main\n", 0o644)
	writeFile(t, filepath.Join(c.scratch, "main.go"), "package main\n", 0o644)
	writeFile(t, filepath.Join(c.other, "notes.txt"), "other participant\n", 0o644)
	writeFile(t, filepath.Join(c.outside, "secret.txt"), "outside\n", 0o644)

	b, err := os.ReadFile(helperBin)
	if err != nil {
		t.Fatal(err)
	}
	c.helper = filepath.Join(c.tmp, helperName)
	writeFile(t, c.helper, string(b), 0o755)

	// Registered after the directories, so it runs before they are removed:
	// stop any beater a backend failed to contain, so that nothing writes
	// into a directory while it is being removed.
	t.Cleanup(func() { stopBeater(t, c.tmp) })

	c.env = sandbox.Env(sandbox.EnvConfig{
		Path:        c.path(),
		TempDir:     c.tmp,
		Passthrough: passthroughNames(fx.Passthrough),
		Source:      fx.Passthrough,
	})
	return c
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func passthroughNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	return names
}

func (c *call) path() string {
	if c.fx.Path != "" {
		return c.fx.Path
	}
	return DefaultPath
}

func (c *call) spec(args ...string) sandbox.Spec {
	ro := c.fx.ReadOnlyPaths
	if ro == nil {
		ro = DefaultReadOnlyPaths()
	}
	timeout := c.fx.Timeout
	if timeout == 0 {
		timeout = time.Second
	}
	return sandbox.Spec{
		Argv:       append([]string{c.helper}, args...),
		ScratchDir: c.scratch,
		TempDir:    c.tmp,
		Env:        append([]string{}, c.env...),
		System: sandbox.SystemLocations{
			Path:          strings.Split(c.path(), ":"),
			ReadOnlyPaths: append([]string{}, ro...),
			Devices:       append([]string{}, sandbox.DeviceNodes...),
		},
		Timeout: timeout,
	}
}

func (c *call) grace() time.Duration {
	if c.fx.KillGrace > 0 {
		return c.fx.KillGrace
	}
	return 200 * time.Millisecond
}

func (c *call) cleanupDeadline() time.Duration {
	if c.fx.CleanupDeadline > 0 {
		return c.fx.CleanupDeadline
	}
	return 10 * time.Second
}

// running is a started command and its captured output.
type running struct {
	c      *call
	proc   sandbox.Process
	start  time.Time
	mu     sync.Mutex
	out    bytes.Buffer
	eof    chan struct{}
	waited chan struct{}
	status sandbox.ExitStatus
	err    error
}

func (c *call) start(t *testing.T, args ...string) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	proc, err := c.backend.Start(ctx, c.spec(args...))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r := &running{c: c, proc: proc, start: time.Now(), eof: make(chan struct{}), waited: make(chan struct{})}
	go func() {
		defer close(r.eof)
		buf := make([]byte, 4096)
		for {
			n, err := proc.Output().Read(buf)
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
		r.status, r.err = proc.Wait()
	}()
	// Whatever a clause does, the process is terminated and its pipe closed
	// before the directories go.
	t.Cleanup(func() {
		_ = proc.Terminate(syscall.SIGKILL)
		_ = proc.Output().Close()
	})
	return r
}

func (r *running) output() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.String()
}

// waitFor waits until the output contains s or the deadline passes.
func (r *running) waitFor(s string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(r.output(), s) {
			return true
		}
		select {
		case <-r.eof:
			return strings.Contains(r.output(), s)
		case <-time.After(10 * time.Millisecond):
		}
	}
	return false
}

// eofWait bounds the wait for the pipe's end of file after the processes are
// gone. The end comes when the last holder of the write end exits, which on a
// loaded machine can trail the shell's exit by more than a second; output
// cut off before it would be judged as if the command had printed less.
const eofWait = 10 * time.Second

// terminate runs the engine's termination sequence (v7 delta, section 3,
// "Containment and termination"): SIGTERM, the grace wait unless the shell
// has exited, SIGKILL, and ConfirmEmpty bounded by the cleanup deadline. It
// then reads the pipe to its end, for at most eofWait, and closes it.
func (r *running) terminate(t *testing.T) error {
	t.Helper()
	exited := false
	select {
	case <-r.waited:
		exited = true
	default:
	}
	if err := r.proc.Terminate(syscall.SIGTERM); err != nil {
		t.Errorf("Terminate(SIGTERM): %v", err)
	}
	if !exited {
		select {
		case <-r.waited:
		case <-time.After(r.c.grace()):
		}
	}
	if err := r.proc.Terminate(syscall.SIGKILL); err != nil {
		t.Errorf("Terminate(SIGKILL): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.c.cleanupDeadline())
	defer cancel()
	confirmErr := r.proc.ConfirmEmpty(ctx)
	select {
	case <-r.waited:
	case <-ctx.Done():
		t.Errorf("Wait did not return after termination")
	}
	select {
	case <-r.eof:
	case <-time.After(eofWait):
	}
	_ = r.proc.Output().Close()
	return confirmErr
}

// finish waits for the shell to exit, then terminates and confirms.
func (r *running) finish(t *testing.T) string {
	t.Helper()
	select {
	case <-r.waited:
	case <-time.After(30 * time.Second):
		t.Errorf("command did not exit")
	}
	if err := r.terminate(t); err != nil {
		t.Errorf("ConfirmEmpty: %v", err)
	}
	return r.output()
}

// runHelper runs the helper to completion and returns its probe lines.
func (c *call) runHelper(t *testing.T, args ...string) []string {
	t.Helper()
	out := c.start(t, args...).finish(t)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[0] != "ready" {
		t.Fatalf("helper did not run under the backend; output:\n%s", out)
	}
	return lines[1:]
}

// Helper results: the first word of each probe line.
const (
	resultOK     = "ok"     // the operation succeeded
	resultDenied = "denied" // the operation failed, for a reason other than ENOENT
	resultAbsent = "absent" // a read failed with ENOENT: no such file
)

// expectAll checks that there is one probe line per probe and that each
// begins with one of the accepted results.
func expectAll(t *testing.T, lines []string, n int, what string, accepted ...string) {
	t.Helper()
	if err := checkLines(lines, n, accepted...); err != nil {
		t.Errorf("%s: %v", what, err)
	}
}

// checkLines reports how lines departs from n probe lines each beginning
// with one of the accepted results, or nil when it does not. It is the
// whole judgement of the read, write, signal and connect clauses, so that a
// test can show which results a clause rejects.
func checkLines(lines []string, n int, accepted ...string) error {
	var errs []error
	if len(lines) != n {
		errs = append(errs, fmt.Errorf("got %d probe lines, want %d: %q", len(lines), n, lines))
	}
	for _, l := range lines {
		ok := false
		for _, a := range accepted {
			if strings.HasPrefix(l, a+" ") {
				ok = true
				break
			}
		}
		if !ok {
			errs = append(errs, fmt.Errorf("want %s, got %q", strings.Join(accepted, " or "), l))
		}
	}
	return errors.Join(errs...)
}

// The command reaches its scratch directory through its working directory,
// by relative paths: a backend may mount the scratch directory at a fixed
// path of its own rather than at the engine's.

func testBaseline(t *testing.T, c *call) {
	lines := c.runHelper(t, "read", "main.go")
	expectAll(t, lines, 1, "read in scratch", resultOK)
	lines = c.runHelper(t, "write", "out.txt", filepath.Join(c.tmp, "out.txt"))
	expectAll(t, lines, 2, "write in scratch and temporary directories", resultOK)
	for _, p := range []string{filepath.Join(c.scratch, "out.txt"), filepath.Join(c.tmp, "out.txt")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the command's write is not in %s: %v", p, err)
		}
	}
}

func testReadOutside(t *testing.T, c *call) {
	lines := c.runHelper(t, "read", filepath.Join(c.outside, "secret.txt"), filepath.Join(c.workspace, "main.go"))
	expectAll(t, lines, 2, "read outside the allowed locations", resultDenied, resultAbsent)
}

func testProtectedAbsent(t *testing.T, c *call) {
	lines := c.runHelper(t, "read", filepath.Join(c.workspace, protectedName), protectedName)
	// Only "absent" satisfies this clause: the path must not be there. A
	// read that fails for any other reason means the path is exposed.
	expectAll(t, lines, 2, "read a protected path", resultAbsent)
}

func testWriteOutside(t *testing.T, c *call) {
	targets := []string{
		filepath.Join(c.outside, "new.txt"),
		filepath.Join(c.workspace, "new.txt"),
		filepath.Join(c.other, "new.txt"),
		filepath.Join(filepath.Dir(c.tmp), "new.txt"),
	}
	lines := c.runHelper(t, append([]string{"write"}, targets...)...)
	expectAll(t, lines, len(targets), "write outside the scratch and temporary directories", resultDenied)
	for _, p := range targets {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a denied write (err %v)", p, err)
		}
	}
}

func testOtherScratch(t *testing.T, c *call) {
	lines := c.runHelper(t, "read", filepath.Join(c.other, "notes.txt"))
	expectAll(t, lines, 1, "read another participant's scratch directory", resultDenied, resultAbsent)
}

func testNoSignal(t *testing.T, c *call) {
	got := make(chan os.Signal, 1)
	signal.Notify(got, syscall.SIGUSR1)
	// Ignore rather than Stop afterwards, so that a late SIGUSR1 cannot end
	// the test process.
	defer signal.Ignore(syscall.SIGUSR1)

	lines := c.runHelper(t, "signal", strconv.Itoa(os.Getpid()))
	expectAll(t, lines, 1, "signal the test process", resultDenied)
	select {
	case <-got:
		t.Errorf("the test process received SIGUSR1 from the command")
	case <-time.After(200 * time.Millisecond):
	}
}

func testDescriptors(t *testing.T, c *call) {
	lines := c.runHelper(t, "fds")
	if len(lines) != 0 {
		t.Errorf("command inherited descriptors beyond the three: %q", lines)
	}
}

func testNoNetwork(t *testing.T, c *call) {
	var hits atomic.Int64
	var args []string

	listen := func(network, addr string) {
		l, err := net.Listen(network, addr)
		if err != nil {
			t.Logf("no %s listener on %s: %v", network, addr, err)
			return
		}
		t.Cleanup(func() { _ = l.Close() })
		go func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				hits.Add(1)
				_ = conn.Close()
			}
		}()
		args = append(args, network, l.Addr().String())
	}
	listen("tcp4", "127.0.0.1:0")
	listen("tcp6", "[::1]:0")
	if runtime.GOOS == "linux" {
		listen("unix", fmt.Sprintf("@syndic-contract-%d-%d", os.Getpid(), time.Now().UnixNano()))
	}

	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
			hits.Add(1)
		}
	}()
	args = append(args, "udp4", pc.LocalAddr().String())

	lines := c.runHelper(t, append([]string{"connect"}, args...)...)
	expectAll(t, lines, len(args)/2, "connect with no listener", resultDenied)
	time.Sleep(100 * time.Millisecond)
	if n := hits.Load(); n != 0 {
		t.Errorf("listeners saw %d connections or datagrams from the command", n)
	}
}

func testEnvironment(t *testing.T, c *call) {
	lines := c.runHelper(t, "env")
	got := map[string]bool{}
	for _, l := range lines {
		got[strings.TrimPrefix(l, "env ")] = true
	}
	want := map[string]bool{}
	for _, kv := range c.env {
		want[kv] = true
	}
	for kv := range got {
		if !want[kv] {
			t.Errorf("unexpected environment entry %q", kv)
		}
	}
	for kv := range want {
		if !got[kv] {
			t.Errorf("missing environment entry %q", kv)
		}
	}
	for kv := range got {
		name, _, _ := strings.Cut(kv, "=")
		if name == "BASH_ENV" || name == "ENV" {
			t.Errorf("%s is present", name)
		}
	}
}

// testDetach starts a helper that detaches a beater into a new session, ends
// the call by the given cause, and checks that the beater no longer beats.
func testDetach(t *testing.T, c *call, cause string) {
	then := "sleep"
	if cause == "exit" {
		then = "exit"
	}
	r := c.start(t, "detach", c.tmp, then)
	if !r.waitFor("ok detached", 10*time.Second) {
		_ = r.terminate(t)
		t.Fatalf("helper did not detach; output:\n%s", r.output())
	}
	switch cause {
	case "exit":
		select {
		case <-r.waited:
		case <-time.After(10 * time.Second):
			t.Errorf("shell did not exit")
		}
	case "timeout":
		select {
		case <-r.waited:
			t.Errorf("shell exited before its timeout")
		case <-time.After(time.Until(r.start.Add(c.spec().Timeout))):
		}
	}
	if err := r.terminate(t); err != nil {
		t.Errorf("ConfirmEmpty: %v", err)
	}
	if beating(c.tmp) {
		t.Errorf("a detached descendant survived the call (%s)", cause)
	}
}

func testConfirmAfterTerminate(t *testing.T, c *call) {
	r := c.start(t, "detach", c.tmp, "sleep")
	if !r.waitFor("ok detached", 10*time.Second) {
		t.Errorf("helper did not detach; output:\n%s", r.output())
	}
	if err := r.terminate(t); err != nil {
		t.Errorf("ConfirmEmpty did not return nil after termination: %v", err)
	}
}

// beatWait bounds how long beating waits for a beat that should come every
// beatEvery. A live beater beats within a few beatEvery even on a loaded
// machine; the bound is generous so that only a dead beater reaches it.
const beatWait = time.Second

// beating reports whether a beater in dir is still writing its beat file. It
// returns as soon as the file changes, and reports false only when no beat
// has come within beatWait: the judgement waits on the beat, not on an
// elapsed time short enough for a slow machine to outlast.
func beating(dir string) bool {
	return beatsWithin(dir, beatWait)
}

func beatsWithin(dir string, d time.Duration) bool {
	read := func() string {
		b, _ := os.ReadFile(filepath.Join(dir, beatFile))
		return string(b)
	}
	before := read()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		time.Sleep(beatEvery / 5)
		if read() != before {
			return true
		}
	}
	return false
}

// stopBeater asks any beater in dir to stop and waits until it has.
func stopBeater(t *testing.T, dir string) {
	if _, err := os.Stat(filepath.Join(dir, beatFile)); err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, stopFile), nil, 0o644); err != nil {
		t.Logf("cannot stop beater: %v", err)
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for beatsWithin(dir, 10*beatEvery) {
		if time.Now().After(deadline) {
			t.Errorf("a beater in %s did not stop", dir)
			return
		}
	}
}
