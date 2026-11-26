// Package contracttest is the enforcement suite every sandbox backend must
// pass: one subtest per backend-only clause of the "Sandbox, on each real
// backend" paragraph of section 9 of docs/design/v7-delta-exec-tool.md.
package contracttest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

// Clause names, one per subtest.
const (
	ReadOutside       = "read_outside_fails"
	ProtectedAbsent   = "protected_path_absent"
	WriteOutside      = "write_outside_fails"
	OtherScratch      = "other_scratch_unreadable"
	NoSignal          = "cannot_signal_engine"
	Descriptors       = "no_extra_descriptors"
	NoNetwork         = "no_network_any_family"
	Environment       = "environment_is_fixed_set"
	DetachOnExit      = "detached_helper_dies_on_exit"
	DetachOnTimeout   = "detached_helper_dies_on_timeout"
	DetachOnTerminate = "detached_helper_dies_on_termination"
	ConfirmEmpty      = "confirm_empty_after_termination"
)

// Fixtures is what the suite needs from the caller.
type Fixtures struct {
	// Path is the PATH value; default "/usr/local/bin:/usr/bin:/bin".
	Path string
	// ReadOnlyPaths are the system locations the backend is configured with.
	ReadOnlyPaths []string
	// Shell is the shell to run; default "/bin/sh".
	Shell string
}

func (f Fixtures) withDefaults() Fixtures {
	if f.Path == "" {
		f.Path = "/usr/local/bin:/usr/bin:/bin"
	}
	if f.Shell == "" {
		f.Shell = "/bin/sh"
	}
	return f
}

type clause struct {
	name string
	fn   func(h *harness) error
}

var clauses = []clause{
	{ReadOutside, readOutside},
	{ProtectedAbsent, protectedAbsent},
	{WriteOutside, writeOutside},
	{OtherScratch, otherScratch},
	{NoSignal, noSignal},
	{Descriptors, descriptors},
	{NoNetwork, noNetwork},
	{Environment, environment},
	{DetachOnExit, detachOnExit},
	{DetachOnTimeout, detachOnTimeout},
	{DetachOnTerminate, detachOnTerminate},
	{ConfirmEmpty, confirmEmpty},
}

// Run runs every clause as a subtest of t against backend.
func Run(t *testing.T, backend sandbox.Backend, fixtures Fixtures) {
	t.Helper()
	for _, c := range clauses {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, backend, fixtures)
			if err := c.fn(h); err != nil {
				t.Error(err)
			}
		})
	}
}

// Evaluate runs every clause without failing t and returns each clause's
// failure, nil when it passed. It shows that the suite detects a backend that
// does not enforce.
func Evaluate(t *testing.T, backend sandbox.Backend, fixtures Fixtures) map[string]error {
	t.Helper()
	out := map[string]error{}
	for _, c := range clauses {
		out[c.name] = c.fn(newHarness(t, backend, fixtures))
	}
	return out
}

type harness struct {
	t       testing.TB
	backend sandbox.Backend
	fx      Fixtures
	helper  string // helper binary, host path
	root    string
	scratch string
	other   string
	outside string
	calls   int
}

func newHarness(t testing.TB, backend sandbox.Backend, fx Fixtures) *harness {
	t.Helper()
	fx = fx.withDefaults()
	root := t.TempDir()
	h := &harness{t: t, backend: backend, fx: fx, root: root}
	h.helper = buildHelper(t, root)
	h.scratch = filepath.Join(root, "scratch")
	h.other = filepath.Join(root, "other-scratch")
	h.outside = filepath.Join(root, "outside")
	for _, d := range []string{h.scratch, h.other, h.outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// buildHelper builds testdata/helper with the Go toolchain into dir.
func buildHelper(t testing.TB, dir string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate contracttest sources")
	}
	src := filepath.Join(filepath.Dir(file), "testdata", "helper")
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the Go toolchain is needed to build the helper: %v", err)
	}
	out := filepath.Join(dir, "helper-bin")
	cmd := exec.Command(goBin, "build", "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, b)
	}
	return out
}

// result is one finished call.
type result struct {
	Out        string
	Status     sandbox.ExitStatus
	PipeHeld   bool // output pipe still open after containment was confirmed empty
	ConfirmErr error
	Tmp        string
}

type callOpts struct {
	argv        func(helper string) []string
	passthrough []string
	caller      map[string]string
	proxy       *sandbox.ProxyListener
	// afterStart runs once the process is started; it returns the
	// termination to apply ("" = wait for shell exit).
	stop func(p sandbox.Process, tmp string) error
}

// newTmp makes a per-call temporary directory holding a copy of the helper.
func (h *harness) newTmp() (tmp, helper string, err error) {
	h.calls++
	tmp = filepath.Join(h.root, fmt.Sprintf("tmp-%d", h.calls))
	if err = os.Mkdir(tmp, 0o755); err != nil {
		return
	}
	helper = filepath.Join(tmp, "helper")
	b, err := os.ReadFile(h.helper)
	if err != nil {
		return
	}
	err = os.WriteFile(helper, b, 0o755)
	return
}

func (h *harness) call(o callOpts) (*result, error) {
	tmp, helper, err := h.newTmp()
	if err != nil {
		return nil, err
	}
	locs := append(splitPath(h.fx.Path), h.fx.ReadOnlyPaths...)
	spec := sandbox.Spec{
		Argv:            o.argv(helper),
		Scratch:         h.scratch,
		Tmp:             tmp,
		Env:             sandbox.BuildEnv(h.fx.Path, tmp, o.proxy, o.passthrough, o.caller),
		SystemLocations: locs,
		Timeout:         30 * time.Second,
		Proxy:           o.proxy,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, err := h.backend.Start(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	var buf lockedBuffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, p.Output())
		close(done)
	}()
	res := &result{Tmp: tmp}
	if o.stop != nil {
		if err := o.stop(p, tmp); err != nil {
			p.Terminate(sandbox.Forced)
			return nil, err
		}
	}
	res.Status, err = p.Wait()
	if err != nil {
		return nil, fmt.Errorf("wait: %w", err)
	}
	p.Terminate(sandbox.Forced)
	cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	defer ccancel()
	res.ConfirmErr = p.ConfirmEmpty(cctx)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		res.PipeHeld = true
	}
	res.Out = buf.String()
	return res, nil
}

// lockedBuffer lets the result be read while a held pipe is still written.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func splitPath(p string) []string {
	return strings.Split(p, ":")
}

func direct(verb string, args ...string) func(string) []string {
	return func(helper string) []string { return append([]string{helper, verb}, args...) }
}

func (h *harness) writeFile(dir, name, content string) (string, error) {
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, []byte(content), 0o644)
}

const secret = "contract-secret-content"

func readOutside(h *harness) error {
	p, err := h.writeFile(h.outside, "secret", secret)
	if err != nil {
		return err
	}
	r, err := h.call(callOpts{argv: direct("open", p)})
	if err != nil {
		return err
	}
	if r.Status.Code == 0 || strings.Contains(r.Out, secret) {
		return fmt.Errorf("read of %s outside the allowed locations succeeded: status %+v, output %q", p, r.Status, r.Out)
	}
	return nil
}

func protectedAbsent(h *harness) error {
	dir := filepath.Join(h.outside, "protected")
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	p, err := h.writeFile(dir, "token", secret)
	if err != nil {
		return err
	}
	r, err := h.call(callOpts{argv: direct("open", p)})
	if err != nil {
		return err
	}
	if strings.Contains(r.Out, secret) || strings.TrimSpace(r.Out) != "ENOENT" {
		return fmt.Errorf("protected path %s did not read as absent: status %+v, output %q", p, r.Status, r.Out)
	}
	return nil
}

func writeOutside(h *harness) error {
	target := filepath.Join(h.outside, "created")
	r, err := h.call(callOpts{argv: direct("write", target)})
	if err != nil {
		return err
	}
	if _, serr := os.Stat(target); serr == nil {
		return fmt.Errorf("write outside the scratch and temporary directories left %s", target)
	}
	if r.Status.Code == 0 {
		return fmt.Errorf("write outside the scratch and temporary directories succeeded: %q", r.Out)
	}
	// Positive control: the permitted locations are writable.
	controls := map[string]func(string) []string{
		"scratch": func(helper string) []string {
			return []string{helper, "write", filepath.Join(h.scratch, "control")}
		},
		"tmp": func(helper string) []string {
			return []string{helper, "write", filepath.Join(filepath.Dir(helper), "control")}
		},
	}
	for name, argv := range controls {
		r, err := h.call(callOpts{argv: argv})
		if err != nil {
			return err
		}
		if r.Status.Code != 0 {
			return fmt.Errorf("write in the %s directory failed: %q", name, r.Out)
		}
	}
	return nil
}

func otherScratch(h *harness) error {
	p, err := h.writeFile(h.other, "notes", secret)
	if err != nil {
		return err
	}
	r, err := h.call(callOpts{argv: direct("open", p)})
	if err != nil {
		return err
	}
	if r.Status.Code == 0 || strings.Contains(r.Out, secret) {
		return fmt.Errorf("another participant's scratch directory was readable: %q", r.Out)
	}
	return nil
}

func noSignal(h *harness) error {
	r, err := h.call(callOpts{argv: direct("signal", fmt.Sprint(os.Getpid()))})
	if err != nil {
		return err
	}
	if r.Status.Code == 0 {
		return fmt.Errorf("the command signalled the engine process: %q", r.Out)
	}
	return nil
}

func descriptors(h *harness) error {
	// Open a descriptor that is inheritable (no close-on-exec).
	fd, err := syscall.Open(os.DevNull, syscall.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(fd) }()
	r, err := h.call(callOpts{argv: direct("fds")})
	if err != nil {
		return err
	}
	if r.Status.Code != 0 {
		return fmt.Errorf("fds helper failed: %+v %q", r.Status, r.Out)
	}
	if !strings.Contains(r.Out, "stdin_bytes=0\n") || !strings.Contains(r.Out, "extra=\n") {
		return fmt.Errorf("command inherited descriptors or a readable stdin: %q", r.Out)
	}
	return nil
}

func noNetwork(h *harness) error {
	type target struct{ network, addr string }
	var targets []target
	var accepted []func() int
	add := func(network, addr string) {
		l, err := net.Listen(network, addr)
		if err != nil {
			return // the family is unavailable on this host
		}
		h.t.Cleanup(func() { _ = l.Close() })
		ch := make(chan struct{}, 16)
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
				ch <- struct{}{}
			}
		}()
		targets = append(targets, target{network, l.Addr().String()})
		accepted = append(accepted, func() int { return len(ch) })
	}
	add("tcp4", "127.0.0.1:0")
	add("tcp6", "[::1]:0")
	add("unix", filepath.Join(h.outside, "sock"))
	if len(targets) == 0 {
		return errors.New("no listener could be opened")
	}
	for i, tg := range targets {
		r, err := h.call(callOpts{argv: direct("connect", tg.network, tg.addr)})
		if err != nil {
			return err
		}
		if r.Status.Code == 0 || accepted[i]() > 0 {
			return fmt.Errorf("a %s connection to %s succeeded with no listener permitted: %q", tg.network, tg.addr, r.Out)
		}
	}
	return nil
}

func environment(h *harness) error {
	caller := map[string]string{
		"SYNDIC_SECRET": "must-not-pass",
		"BASH_ENV":      "/nonexistent",
		"ENV":           "/nonexistent",
		"KEEP_ME":       "kept",
	}
	passthrough := []string{"KEEP_ME", "ABSENT_NAME"}
	r, err := h.call(callOpts{argv: direct("env"), passthrough: passthrough, caller: caller})
	if err != nil {
		return err
	}
	want := strings.Join(sandbox.BuildEnv(h.fx.Path, r.Tmp, nil, passthrough, caller), "\n")
	got := strings.TrimSpace(r.Out)
	if got != want {
		return fmt.Errorf("environment is not the fixed set:\ngot:\n%s\nwant:\n%s", got, want)
	}
	return nil
}

// beatState reads the detached helper's counter.
func beatState(tmp string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(tmp, "beat"))
	return string(b), err == nil
}

// waitBeating waits until the detached helper is demonstrably running.
func waitBeating(tmp string) error {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a, ok := beatState(tmp)
		if ok {
			time.Sleep(100 * time.Millisecond)
			if b, _ := beatState(tmp); b != a {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("the detached helper never started")
}

func checkDead(r *result) error {
	a, _ := beatState(r.Tmp)
	time.Sleep(300 * time.Millisecond)
	b, _ := beatState(r.Tmp)
	if a != b {
		return errors.New("a detached helper survived the call")
	}
	if r.PipeHeld {
		return errors.New("the output pipe is still held open after containment was confirmed empty")
	}
	if r.ConfirmErr != nil {
		return fmt.Errorf("ConfirmEmpty: %w", r.ConfirmErr)
	}
	return nil
}

const detachScript = `"$0" detach "$1"`

func shellArgv(h *harness, script string) func(string) []string {
	return func(helper string) []string {
		return []string{h.fx.Shell, "-c", script, helper, filepath.Join(filepath.Dir(helper), "beat")}
	}
}

func detachOnExit(h *harness) error {
	r, err := h.call(callOpts{argv: shellArgv(h, detachScript)})
	if err != nil {
		return err
	}
	return checkDead(r)
}

// terminating runs a shell that detaches a helper then sleeps, and ends it
// by the termination sequence after the helper is beating.
func terminating(h *harness, grace time.Duration) error {
	r, err := h.call(callOpts{
		argv: shellArgv(h, detachScript+`; "$0" sleep 30`),
		stop: func(p sandbox.Process, tmp string) error {
			if err := waitBeating(tmp); err != nil {
				return err
			}
			time.Sleep(grace)
			p.Terminate(sandbox.Graceful)
			time.Sleep(200 * time.Millisecond)
			p.Terminate(sandbox.Forced)
			return nil
		},
	})
	if err != nil {
		return err
	}
	if !r.Status.Terminated && r.Status.Signal == 0 && r.Status.Code == 0 {
		return fmt.Errorf("terminated command reports a plain success: %+v", r.Status)
	}
	return checkDead(r)
}

func detachOnTimeout(h *harness) error   { return terminating(h, 300*time.Millisecond) }
func detachOnTerminate(h *harness) error { return terminating(h, 0) }

func confirmEmpty(h *harness) error {
	r, err := h.call(callOpts{
		argv: direct("sleep", "30"),
		stop: func(p sandbox.Process, _ string) error {
			time.Sleep(200 * time.Millisecond)
			p.Terminate(sandbox.Graceful)
			return nil
		},
	})
	if err != nil {
		return err
	}
	if r.ConfirmErr != nil {
		return fmt.Errorf("ConfirmEmpty did not return after termination: %w", r.ConfirmErr)
	}
	return nil
}
