package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// SelfTestHelperArg is the hidden subcommand of syndicd that the self-test's
// helper runs as. The helper is the engine binary itself, copied into each
// call's temporary directory (v7 delta, section 3, "Self-test"); the
// subcommand is not in the command's help.
const SelfTestHelperArg = "__selftest"

// SelfTestCheck names one of the six self-tests of the v7 delta, section 3,
// "Self-test".
type SelfTestCheck string

const (
	// CheckTrue: `true` exits 0.
	CheckTrue SelfTestCheck = "true_exits_zero"
	// CheckReadState: a read of a file the engine created in its state
	// directory fails.
	CheckReadState SelfTestCheck = "read_state_denied"
	// CheckWriteOutside: a write of a new file in a directory outside the
	// scratch, temporary and system locations fails, and the file does not
	// exist afterwards.
	CheckWriteOutside SelfTestCheck = "write_outside_denied"
	// CheckLoopback: a TCP connection to a loopback listener the engine
	// opened fails, and the listener sees no connection.
	CheckLoopback SelfTestCheck = "loopback_denied"
	// CheckDetach: a helper that starts a new session, detaches and sleeps
	// does not exist after the call ends.
	CheckDetach SelfTestCheck = "detached_helper_contained"
	// CheckProxy: with a non-empty network list, a request through the
	// call's proxy listener for the reserved name succeeds and a connection
	// to another call's listener fails.
	CheckProxy SelfTestCheck = "proxy_only"
)

// AllSelfTestChecks returns the six checks in the order the design lists and
// the self-test runs them.
func AllSelfTestChecks() []SelfTestCheck {
	return []SelfTestCheck{CheckTrue, CheckReadState, CheckWriteOutside, CheckLoopback, CheckDetach, CheckProxy}
}

// SelfTestHost is the reserved name the proxy answers itself (v7 delta,
// section 3, "Self-test", test 6).
const SelfTestHost = "selftest.syndic.invalid"

// selfTestOK is the line the helper prints when it observed the behaviour
// the self-test expects; a backend that runs nothing prints nothing.
const selfTestOK = "selftest ok"

// Bounds of every helper: one process, plus one detached sleeper for the
// detach test, and a hard time limit each.
const (
	helperDialLimit  = 2 * time.Second
	helperHTTPLimit  = 5 * time.Second
	helperSleepLimit = 30 * time.Second
	defaultCallLimit = 10 * time.Second
	outputCap        = 4096
	pipeCutoff       = time.Second
)

// NetworkFixture is what check 6 needs from the proxy: this call's listener
// and a second listener made for another call.
type NetworkFixture struct {
	// Listener is this call's proxy listener, passed to the backend as the
	// one the command may reach under N2.
	Listener Listener
	// ProxyURL is the value of the command's proxy variables, credential
	// included. For a Unix socket it names the forwarder in the command's
	// namespace.
	ProxyURL string
	// OtherAddr is the host:port of a listener made for another call.
	OtherAddr string
	// Close revokes and closes both listeners; it may be nil.
	Close func()
}

// SelfTestConfig configures a self-test run. Every field but Argv and System
// has a usable zero value.
type SelfTestConfig struct {
	// Argv builds the argument vector for a command string: the shell the
	// real commands run under.
	Argv func(command string) []string
	// Path is tools.exec.path for the command environment.
	Path string
	// System is the closed list of read-only locations a command gets.
	System SystemLocations
	// StateDir is the engine's state directory, where check 2 creates the
	// file the command must not read. Empty means a stand-in under Dir.
	StateDir string
	// Dir is where the self-test creates its directories, files and
	// listener: the state-directory stand-in, the directory outside every
	// allowed location, and each call's scratch and temporary directories.
	// Empty means a fresh directory under os.TempDir, removed afterwards.
	Dir string
	// Helper is the executable copied into each call's temporary directory.
	// Empty means os.Executable.
	Helper string
	// NetworkList is true when a network list is in force; check 6 runs only
	// then.
	NetworkList bool
	// Network opens the fixture for check 6. It is required when NetworkList
	// is set.
	Network func() (NetworkFixture, error)
	// Sequence is the termination sequence; zero means the design defaults.
	Sequence Sequence
	// CallLimit is the timeout of each check's command; zero means ten
	// seconds.
	CallLimit time.Duration
	// Gate, when not nil, is entered before each command starts.
	Gate *Gate
}

// CheckResult is the outcome of one check.
type CheckResult struct {
	Check SelfTestCheck
	// Ran is false for a check that does not apply (check 6 without a
	// network list).
	Ran bool
	// Passed is true when the backend behaved as the design requires.
	Passed bool
	// Detail says what went wrong when Passed is false.
	Detail string
}

// SelfTestResult is the per-check outcome of one self-test run.
type SelfTestResult struct {
	// Scope is "run" for the self-test at engine start and "turn" for the one
	// before a turn's first exec call.
	Scope  string
	Checks []CheckResult
}

// Passed reports whether every check that applies passed.
func (r SelfTestResult) Passed() bool { return len(r.Failed()) == 0 }

// Failed returns the failed checks, in order.
func (r SelfTestResult) Failed() []CheckResult {
	var out []CheckResult
	for _, c := range r.Checks {
		if c.Ran && !c.Passed {
			out = append(out, c)
		}
	}
	return out
}

// Result returns the named check's result.
func (r SelfTestResult) Result(c SelfTestCheck) (CheckResult, bool) {
	for _, cr := range r.Checks {
		if cr.Check == c {
			return cr, true
		}
	}
	return CheckResult{}, false
}

// Err is nil when every check passed, and otherwise names each failed check
// and what went wrong, for the participants reply.
func (r SelfTestResult) Err() error {
	failed := r.Failed()
	if len(failed) == 0 {
		return nil
	}
	parts := make([]string, len(failed))
	for i, f := range failed {
		parts[i] = fmt.Sprintf("%s (%s)", f.Check, f.Detail)
	}
	return fmt.Errorf("sandbox self-test failed: %s", strings.Join(parts, "; "))
}

// RunAtStart runs the self-test of engine start. A failure leaves exec
// unoffered for the run; the caller caches the result and decides.
func RunAtStart(ctx context.Context, b Backend, cfg SelfTestConfig) SelfTestResult {
	r := runSelfTest(ctx, b, cfg)
	r.Scope = "run"
	return r
}

// RunBeforeTurn runs the self-test before the first exec call of a turn. A
// failure leaves exec unoffered for the turn; the caller caches the result
// and decides.
func RunBeforeTurn(ctx context.Context, b Backend, cfg SelfTestConfig) SelfTestResult {
	r := runSelfTest(ctx, b, cfg)
	r.Scope = "turn"
	return r
}

func runSelfTest(ctx context.Context, b Backend, cfg SelfTestConfig) SelfTestResult {
	st := &selfTest{b: b, cfg: cfg}
	var res SelfTestResult
	root, cleanup, err := st.setup()
	if err != nil {
		for _, c := range AllSelfTestChecks() {
			res.Checks = append(res.Checks, CheckResult{Check: c, Ran: c != CheckProxy || cfg.NetworkList, Detail: err.Error()})
		}
		return res
	}
	keep := false
	defer func() {
		if !keep {
			cleanup()
		}
	}()
	st.root = root
	for _, c := range AllSelfTestChecks() {
		if c == CheckProxy && !cfg.NetworkList {
			res.Checks = append(res.Checks, CheckResult{Check: c})
			continue
		}
		detail, err := st.run(ctx, c)
		res.Checks = append(res.Checks, CheckResult{Check: c, Ran: true, Passed: detail == "", Detail: detail})
		if errors.Is(err, errContainmentUnverified) {
			// Nothing after this runs, and the directories stay as they are.
			keep = true
			break
		}
	}
	return res
}

type selfTest struct {
	b    Backend
	cfg  SelfTestConfig
	root string
	n    int
}

func (st *selfTest) setup() (string, func(), error) {
	if st.cfg.Dir != "" {
		root, err := os.MkdirTemp(st.cfg.Dir, "selftest-")
		if err != nil {
			return "", nil, err
		}
		return root, func() { _ = os.RemoveAll(root) }, nil
	}
	root, err := os.MkdirTemp("", "syndic-selftest-")
	if err != nil {
		return "", nil, err
	}
	return root, func() { _ = os.RemoveAll(root) }, nil
}

// run runs one check and returns "" when it passed, else what went wrong.
func (st *selfTest) run(ctx context.Context, c SelfTestCheck) (string, error) {
	var err error
	switch c {
	case CheckTrue:
		err = st.checkTrue(ctx)
	case CheckReadState:
		err = st.checkReadState(ctx)
	case CheckWriteOutside:
		err = st.checkWriteOutside(ctx)
	case CheckLoopback:
		err = st.checkLoopback(ctx)
	case CheckDetach:
		err = st.checkDetach(ctx)
	case CheckProxy:
		err = st.checkProxy(ctx)
	}
	if err != nil {
		return err.Error(), err
	}
	return "", nil
}

// errContainmentUnverified is a call whose containment could not be
// confirmed empty: the run gate is closed, no later command starts, and the
// directories are kept.
var errContainmentUnverified = errors.New("containment not confirmed empty")

type callResult struct {
	exit   int
	output string
}

// call runs one command under the backend with a fresh scratch and temporary
// directory holding the helper, and returns once the containment is
// confirmed empty.
func (st *selfTest) call(ctx context.Context, name string, build func(helper string, fx *NetworkFixture) string) (callResult, error) {
	st.n++
	dir := filepath.Join(st.root, fmt.Sprintf("call%d-%s", st.n, name))
	scratch, tmp := filepath.Join(dir, "scratch"), filepath.Join(dir, "tmp")
	for _, d := range []string{scratch, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return callResult{}, err
		}
	}
	var proxy *NetworkFixture
	revoke := func() {}
	if st.cfg.NetworkList && st.cfg.Network != nil {
		// A network list is in force, so every command gets what a real one
		// would: its own listener and credential, revoked when its
		// termination begins.
		fx, err := st.cfg.Network()
		if err != nil {
			return callResult{}, fmt.Errorf("opening proxy fixture: %w", err)
		}
		if fx.Close != nil {
			var once sync.Once
			revoke = func() { once.Do(fx.Close) }
		}
		proxy = &fx
	}
	defer revoke()
	helper := filepath.Join(tmp, "selftest-helper")
	if err := st.placeHelper(helper); err != nil {
		return callResult{}, fmt.Errorf("placing helper: %w", err)
	}
	env := EnvConfig{Path: st.cfg.Path, TempDir: tmp}
	spec := Spec{
		Argv:       st.cfg.Argv(build(helper, proxy)),
		ScratchDir: scratch,
		TempDir:    tmp,
		System:     st.cfg.System,
	}
	if proxy != nil {
		l := proxy.Listener
		spec.Proxy = &l
		env.ProxyURL = proxy.ProxyURL
	}
	spec.Env = Env(env)
	limit := st.cfg.CallLimit
	if limit <= 0 {
		limit = defaultCallLimit
	}
	spec.Timeout = limit
	if g := st.cfg.Gate; g != nil {
		if err := g.Enter(); err != nil {
			return callResult{}, err
		}
	}
	proc, err := st.b.Start(context.WithoutCancel(ctx), spec)
	if err != nil {
		return callResult{}, fmt.Errorf("start: %w", err)
	}

	out := proc.Output()
	var buf bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(&buf, io.LimitReader(out, outputCap))
		_, _ = io.Copy(io.Discard, out)
	}()
	type waited struct {
		st  ExitStatus
		err error
	}
	waitc := make(chan waited, 1)
	go func() {
		s, err := proc.Wait()
		waitc <- waited{s, err}
	}()

	timer := time.NewTimer(limit)
	defer timer.Stop()
	var (
		w        waited
		exited   bool
		timedOut bool
	)
	select {
	case w = <-waitc:
		exited = true
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
	}
	seq := st.cfg.Sequence
	if seq == (Sequence{}) {
		seq = Sequence{KillGrace: DefaultKillGrace, CleanupDeadline: DefaultCleanupDeadline}
	}
	confirmed := seq.Run(proc, exited && w.err == nil, revoke)
	if !confirmed {
		_ = out.Close()
		if g := st.cfg.Gate; g != nil {
			g.Close("self-test: containment not confirmed empty")
		}
		return callResult{}, errContainmentUnverified
	}
	if !exited {
		w = <-waitc
	}
	select {
	case <-readDone:
	case <-time.After(pipeCutoff):
		_ = out.Close()
		<-readDone
	}
	switch {
	case ctx.Err() != nil:
		return callResult{}, ctx.Err()
	case timedOut:
		return callResult{}, fmt.Errorf("timed out after %s", limit)
	case w.err != nil:
		return callResult{}, fmt.Errorf("wait: %w", w.err)
	}
	return callResult{exit: w.st.Code, output: buf.String()}, nil
}

// placeHelper puts the engine binary at dst: a hard link when the file
// system allows it, else a copy.
func (st *selfTest) placeHelper(dst string) error {
	src := st.cfg.Helper
	if src == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		src = exe
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// helperCmd builds the command line running the helper in mode.
func helperCmd(helper, mode string, args ...string) string {
	parts := []string{shellQuote(helper), SelfTestHelperArg, mode}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// observed checks that the helper ran and saw what the self-test expects.
func observed(r callResult, what string) error {
	if r.exit != 0 || !strings.Contains(r.output, selfTestOK) {
		return fmt.Errorf("%s: exit %d, output %q", what, r.exit, truncate(r.output))
	}
	return nil
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func (st *selfTest) checkTrue(ctx context.Context) error {
	r, err := st.call(ctx, "true", func(string, *NetworkFixture) string { return "true" })
	if err != nil {
		return err
	}
	if r.exit != 0 {
		return fmt.Errorf("true exited %d", r.exit)
	}
	return nil
}

func (st *selfTest) checkReadState(ctx context.Context) error {
	state := st.cfg.StateDir
	if state == "" {
		state = filepath.Join(st.root, "state")
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(state, "selftest-")
	if err != nil {
		return err
	}
	file := f.Name()
	defer func() { _ = os.Remove(file) }()
	_, werr := f.WriteString("engine state")
	if cerr := f.Close(); werr != nil || cerr != nil {
		return errors.Join(werr, cerr)
	}
	r, err := st.call(ctx, "read", func(h string, _ *NetworkFixture) string { return helperCmd(h, "read-denied", file) })
	if err != nil {
		return err
	}
	return observed(r, "read of a file in the state directory was not denied")
}

func (st *selfTest) checkWriteOutside(ctx context.Context) error {
	outside := filepath.Join(st.root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		return err
	}
	file := filepath.Join(outside, "probe")
	r, err := st.call(ctx, "write", func(h string, _ *NetworkFixture) string { return helperCmd(h, "write-denied", file) })
	if err != nil {
		return err
	}
	if _, statErr := os.Lstat(file); statErr == nil {
		return errors.New("the file written outside the allowed locations exists")
	}
	return observed(r, "write outside the allowed locations was not denied")
}

// connCounter counts the connections a listener accepts.
type connCounter struct {
	ln net.Listener
	n  atomic.Int64
	wg sync.WaitGroup
}

func newConnCounter() (*connCounter, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	c := &connCounter{ln: ln}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			c.n.Add(1)
			_ = conn.Close()
		}
	}()
	return c, nil
}

func (c *connCounter) close() {
	_ = c.ln.Close()
	c.wg.Wait()
}

func (c *connCounter) addr() string { return c.ln.Addr().String() }

// seen waits briefly for a connection still in flight and returns the count.
func (c *connCounter) seen() int64 {
	time.Sleep(100 * time.Millisecond)
	return c.n.Load()
}

func (st *selfTest) checkLoopback(ctx context.Context) error {
	l, err := newConnCounter()
	if err != nil {
		return err
	}
	defer l.close()
	r, err := st.call(ctx, "loopback", func(h string, _ *NetworkFixture) string { return helperCmd(h, "connect-denied", l.addr()) })
	if err != nil {
		return err
	}
	if n := l.seen(); n != 0 {
		return fmt.Errorf("the loopback listener saw %d connection(s)", n)
	}
	return observed(r, "connection to a loopback listener was not denied")
}

// checkDetach has the helper start a sleeper in a new session that holds a
// lock on a file in the call's temporary directory. The lock is the
// sleeper's identity: it is observable from the engine whatever process
// namespace the backend gives the command, and it is released only when
// the sleeper is gone.
func (st *selfTest) checkDetach(ctx context.Context) error {
	var lock string
	r, err := st.call(ctx, "detach", func(helper string, _ *NetworkFixture) string {
		lock = filepath.Join(filepath.Dir(helper), "detach.lock")
		_ = os.WriteFile(lock, nil, 0o600)
		return helperCmd(helper, "detach", lock)
	})
	if err != nil {
		return err
	}
	if err := observed(r, "detaching helper"); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for !lockFree(lock) {
		if time.Now().After(deadline) {
			return errors.New("the detached process survived the call")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// lockHeld reports whether a process holds the lock on path.
func lockHeld(path string) bool {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return errors.Is(syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB), syscall.EWOULDBLOCK)
}

// lockFree reports whether no process holds the lock on path.
func lockFree(path string) bool {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

func (st *selfTest) checkProxy(ctx context.Context) error {
	if st.cfg.Network == nil {
		return errors.New("no proxy fixture configured")
	}
	r, err := st.call(ctx, "proxy", func(h string, _ *NetworkFixture) string { return helperCmd(h, "proxy-request", SelfTestHost) })
	if err != nil {
		return err
	}
	if err := observed(r, "request through the call's proxy listener failed"); err != nil {
		return err
	}
	r, err = st.call(ctx, "other", func(h string, fx *NetworkFixture) string { return helperCmd(h, "connect-denied", fx.OtherAddr) })
	if err != nil {
		return err
	}
	return observed(r, "connection to another call's listener was not denied")
}

// SelfTestHelper is the hidden subcommand's body: one bounded probe, whose
// arguments are those after SelfTestHelperArg. It prints the line the
// self-test looks for when it observed what the self-test expects, and
// returns the process exit status.
func SelfTestHelper(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "read-denied":
		if len(args) != 2 {
			return 2
		}
		if _, err := os.ReadFile(args[1]); err == nil {
			say(stdout, "read succeeded")
			return 1
		}
		say(stdout, selfTestOK)
	case "write-denied":
		if len(args) != 2 {
			return 2
		}
		f, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_ = f.Close()
			say(stdout, "write succeeded")
			return 1
		}
		say(stdout, selfTestOK)
	case "connect-denied":
		if len(args) != 2 {
			return 2
		}
		conn, err := net.DialTimeout("tcp", args[1], helperDialLimit)
		if err == nil {
			_ = conn.Close()
			say(stdout, "connection succeeded")
			return 1
		}
		say(stdout, selfTestOK)
	case "proxy-request":
		if len(args) != 2 {
			return 2
		}
		return helperProxyRequest(args[1], stdout)
	case "detach":
		if len(args) != 2 {
			return 2
		}
		return helperDetach(args[1], stdout)
	case "sleep-locked":
		if len(args) != 2 {
			return 2
		}
		return helperSleepLocked(args[1])
	default:
		return 2
	}
	return 0
}

func helperProxyRequest(host string, stdout io.Writer) int {
	raw := os.Getenv("HTTP_PROXY")
	pu, err := url.Parse(raw)
	if raw == "" || err != nil {
		say(stdout, "no usable HTTP_PROXY")
		return 1
	}
	client := &http.Client{
		Timeout:   helperHTTPLimit,
		Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true},
	}
	resp, err := client.Get("http://" + host + "/")
	if err != nil {
		say(stdout, "request failed:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, outputCap))
	if resp.StatusCode != http.StatusOK {
		say(stdout, "status", resp.StatusCode)
		return 1
	}
	say(stdout, selfTestOK)
	return 0
}

// helperDetach starts one sleeper in a new session and exits, leaving it for
// the backend's containment to end. The sleeper's own time limit bounds it
// should the backend fail to. The helper reports success only once the
// sleeper holds the lock on lock, so that a lock the engine later finds free
// means the sleeper is gone.
func helperDetach(lock string, stdout io.Writer) int {
	exe, err := os.Executable()
	if err != nil {
		say(stdout, "no executable:", err)
		return 1
	}
	cmd := exec.Command(exe, SelfTestHelperArg, "sleep-locked", lock)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		say(stdout, "start failed:", err)
		return 1
	}
	deadline := time.Now().Add(helperDialLimit)
	for time.Now().Before(deadline) {
		if lockHeld(lock) {
			say(stdout, selfTestOK)
			return 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	say(stdout, "the sleeper never took its lock")
	return 1
}

// helperSleepLocked holds a lock on path, creating it, until the time limit.
func helperSleepLocked(path string) int {
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return 1
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 1
	}
	time.Sleep(helperSleepLimit)
	return 0
}

// say writes one line to the helper's output; a failed write has nowhere to
// be reported and leaves the line missing, which the self-test reads as a
// failure.
func say(w io.Writer, args ...any) { _, _ = fmt.Fprintln(w, args...) }
