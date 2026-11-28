package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Checks seven and eight of the server self-test (v9 delta, section 6,
// "Self-test"); the first six are v7's, under the server policy.
const (
	// CheckWriteWorkspace: a write into a workspace copy the server was
	// granted read-only fails, and the file does not exist afterwards.
	CheckWriteWorkspace SelfTestCheck = "write_workspace_copy_denied"
	// CheckReadOtherState: a read of a file in another key's state directory
	// fails.
	CheckReadOtherState SelfTestCheck = "read_other_state_denied"
)

// AllServerSelfTestChecks returns the eight checks of the server self-test
// in the order it runs them: v7's six, then the two the server policy adds.
func AllServerSelfTestChecks() []SelfTestCheck {
	return append(AllSelfTestChecks(), CheckWriteWorkspace, CheckReadOtherState)
}

// serverHelperMode is the helper mode that reads one probe line on standard
// input, runs the probe it names and answers on standard output.
const serverHelperMode = "serve"

// ServerSelfTestConfig configures a self-test under the server policy. It is
// a SelfTestConfig whose commands are started as servers: with
// Spec.ServerPipes, the instance's directories as a server policy has them
// (the state directory is the working directory and HOME, the temporary
// directory is TMPDIR) and the helper driven over standard input and
// standard output. Its Network, Sequence, CallLimit and Gate are as for
// v7's.
type ServerSelfTestConfig struct {
	SelfTestConfig
}

// RunServersAtStart runs the server self-test of engine start. A failure
// leaves local servers unoffered for the run; the caller caches the result.
func RunServersAtStart(ctx context.Context, b Backend, cfg ServerSelfTestConfig) SelfTestResult {
	r := runServerSelfTest(ctx, b, cfg)
	r.Scope = "run"
	return r
}

// RunServersBeforeTurn runs the server self-test before the first local
// instance of a turn. A failure leaves local servers unoffered for the turn;
// the caller caches the result.
func RunServersBeforeTurn(ctx context.Context, b Backend, cfg ServerSelfTestConfig) SelfTestResult {
	r := runServerSelfTest(ctx, b, cfg)
	r.Scope = "turn"
	return r
}

func runServerSelfTest(ctx context.Context, b Backend, cfg ServerSelfTestConfig) SelfTestResult {
	st := &serverSelfTest{selfTest: selfTest{b: b, cfg: cfg.SelfTestConfig}}
	var res SelfTestResult
	root, cleanup, err := st.setup()
	if err != nil {
		for _, c := range AllServerSelfTestChecks() {
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
	for _, c := range AllServerSelfTestChecks() {
		if c == CheckProxy && !cfg.NetworkList {
			res.Checks = append(res.Checks, CheckResult{Check: c})
			continue
		}
		err := st.check(ctx, c)
		cr := CheckResult{Check: c, Ran: true, Passed: err == nil}
		if err != nil {
			cr.Detail = err.Error()
		}
		res.Checks = append(res.Checks, cr)
		if errors.Is(err, errContainmentUnverified) {
			// Nothing after this runs, and the directories stay as they are.
			keep = true
			break
		}
	}
	return res
}

type serverSelfTest struct {
	selfTest
}

func (st *serverSelfTest) check(ctx context.Context, c SelfTestCheck) error {
	switch c {
	case CheckTrue:
		r, err := st.call(ctx, "true", nil, "ping")
		if err != nil {
			return err
		}
		return observed(r, "the helper did not answer over the protocol pipes")
	case CheckReadState:
		state := st.cfg.StateDir
		if state == "" {
			state = filepath.Join(st.root, "state")
		}
		return st.checkReadDenied(ctx, "read", state, "read of a file in the engine's state directory was not denied")
	case CheckWriteOutside:
		outside := filepath.Join(st.root, "outside")
		if err := os.MkdirAll(outside, 0o700); err != nil {
			return err
		}
		return st.checkWriteDenied(ctx, "write", outside, nil, "write outside the allowed locations was not denied")
	case CheckLoopback:
		l, err := newConnCounter()
		if err != nil {
			return err
		}
		defer l.close()
		r, err := st.call(ctx, "loopback", nil, "connect-denied", l.addr())
		if err != nil {
			return err
		}
		if n := l.seen(); n != 0 {
			return fmt.Errorf("the loopback listener saw %d connection(s)", n)
		}
		return observed(r, "connection to a loopback listener was not denied")
	case CheckDetach:
		return st.checkServerDetach(ctx)
	case CheckProxy:
		return st.checkServerProxy(ctx)
	case CheckWriteWorkspace:
		ws := filepath.Join(st.root, "workspace-copy")
		if err := os.MkdirAll(ws, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(ws, "file"), []byte("workspace"), 0o600); err != nil {
			return err
		}
		return st.checkWriteDenied(ctx, "workspace", ws, &ws, "write into the workspace copy was not denied")
	case CheckReadOtherState:
		return st.checkReadOtherState(ctx)
	}
	return fmt.Errorf("unknown check %q", c)
}

// checkReadDenied has the helper read a file the engine created in dir.
func (st *serverSelfTest) checkReadDenied(ctx context.Context, name, dir, what string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "selftest-")
	if err != nil {
		return err
	}
	file := f.Name()
	defer func() { _ = os.Remove(file) }()
	_, werr := f.WriteString("secret")
	if cerr := f.Close(); werr != nil || cerr != nil {
		return errors.Join(werr, cerr)
	}
	r, err := st.call(ctx, name, nil, "read-denied", file)
	if err != nil {
		return err
	}
	return observed(r, what)
}

// checkWriteDenied has the helper create a file in dir. workspace, when not
// nil, is the read-only workspace copy the server is granted.
func (st *serverSelfTest) checkWriteDenied(ctx context.Context, name, dir string, workspace *string, what string) error {
	file := filepath.Join(dir, "probe")
	r, err := st.call(ctx, name, workspace, "write-denied", file)
	if err != nil {
		return err
	}
	if _, statErr := os.Lstat(file); statErr == nil {
		where := "outside the allowed locations"
		if workspace != nil {
			where = "in the workspace copy"
		}
		return fmt.Errorf("the file written %s exists", where)
	}
	return observed(r, what)
}

// checkReadOtherState has the helper read a file in another key's state
// directory, a sibling of the instance's own under the same parent.
func (st *serverSelfTest) checkReadOtherState(ctx context.Context) error {
	var file string
	var setupErr error
	r, err := st.callWith(ctx, "otherstate", nil, func(d serverDirs) []string {
		other := filepath.Join(filepath.Dir(d.state), "other-key-state")
		if setupErr = os.MkdirAll(other, 0o700); setupErr != nil {
			return []string{"ping"}
		}
		file = filepath.Join(other, "secret")
		setupErr = os.WriteFile(file, []byte("secret"), 0o600)
		return []string{"read-denied", file}
	})
	if file != "" {
		defer func() { _ = os.Remove(file) }()
	}
	if setupErr != nil {
		return setupErr
	}
	if err != nil {
		return err
	}
	return observed(r, "read of a file in another key's state directory was not denied")
}

func (st *serverSelfTest) checkServerDetach(ctx context.Context) error {
	lock := ""
	r, err := st.callWith(ctx, "detach", nil, func(dirs serverDirs) []string {
		lock = filepath.Join(dirs.tmp, "detach.lock")
		_ = os.WriteFile(lock, nil, 0o600)
		return []string{"detach", lock}
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
			return errors.New("the detached process survived the instance")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

func (st *serverSelfTest) checkServerProxy(ctx context.Context) error {
	if st.cfg.Network == nil {
		return errors.New("no proxy fixture configured")
	}
	r, err := st.call(ctx, "proxy", nil, "proxy-request", SelfTestHost)
	if err != nil {
		return err
	}
	if err := observed(r, "request through the instance's proxy listener failed"); err != nil {
		return err
	}
	r, err = st.callWith(ctx, "other", nil, func(d serverDirs) []string { return []string{"connect-denied", d.fx.OtherAddr} })
	if err != nil {
		return err
	}
	return observed(r, "connection to another instance's listener was not denied")
}

// serverDirs are the directories of one self-test instance.
type serverDirs struct {
	state, tmp string
	fx         *NetworkFixture
}

func (st *serverSelfTest) call(ctx context.Context, name string, workspace *string, probe ...string) (callResult, error) {
	return st.callWith(ctx, name, workspace, func(serverDirs) []string { return probe })
}

// serverPipes is what a backend's process is when it was started with
// Spec.ServerPipes.
type serverPipes interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
}

// callWith runs one server instance under the backend with a fresh state
// directory and temporary directory holding the helper, drives the helper
// over the protocol pipes with the probe line probe builds, and returns once
// the containment is confirmed empty. workspace, when not nil, is the
// read-only workspace copy the instance is granted.
func (st *serverSelfTest) callWith(ctx context.Context, name string, workspace *string, probe func(serverDirs) []string) (callResult, error) {
	st.n++
	dir := filepath.Join(st.root, fmt.Sprintf("call%d-%s", st.n, name))
	dirs := serverDirs{state: filepath.Join(dir, "state"), tmp: filepath.Join(dir, "tmp")}
	for _, d := range []string{dirs.state, dirs.tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return callResult{}, err
		}
	}
	revoke := func() {}
	if st.cfg.NetworkList && st.cfg.Network != nil {
		fx, err := st.cfg.Network()
		if err != nil {
			return callResult{}, fmt.Errorf("opening proxy fixture: %w", err)
		}
		if fx.Close != nil {
			var once sync.Once
			revoke = func() { once.Do(fx.Close) }
		}
		dirs.fx = &fx
	}
	defer revoke()
	helper := filepath.Join(dirs.tmp, "selftest-helper")
	if err := st.placeHelper(helper); err != nil {
		return callResult{}, fmt.Errorf("placing helper: %w", err)
	}
	cfg := ServerConfig{System: st.cfg.System, StateDir: dirs.state, TempDir: dirs.tmp}
	if workspace != nil {
		cfg.WorkspaceDir = *workspace
	}
	proxyURL := ""
	if dirs.fx != nil {
		cfg.NetworkAllow = []string{SelfTestHost}
		proxyURL = dirs.fx.ProxyURL
	}
	pol, err := NewServerPolicy(cfg)
	if err != nil {
		return callResult{}, err
	}
	spec := Spec{
		Argv:        st.cfg.Argv(helperCmd(helper, serverHelperMode)),
		ScratchDir:  dirs.state,
		TempDir:     dirs.tmp,
		System:      st.cfg.System,
		Env:         pol.Env(st.cfg.Path, proxyURL, nil),
		ServerPipes: true,
	}
	spec.System.ReadOnlyPaths = append(append([]string{}, st.cfg.System.ReadOnlyPaths...), pol.ReadOnly...)
	if dirs.fx != nil {
		l := dirs.fx.Listener
		spec.Proxy = &l
	}
	limit := st.cfg.CallLimit
	if limit <= 0 {
		limit = defaultCallLimit
	}
	spec.Timeout = limit
	line := strings.Join(probe(dirs), "\x1f") + "\n"
	if g := st.cfg.Gate; g != nil {
		if err := g.Enter(); err != nil {
			return callResult{}, err
		}
	}
	proc, err := st.b.Start(context.WithoutCancel(ctx), spec)
	if err != nil {
		return callResult{}, fmt.Errorf("start: %w", err)
	}
	seq := st.cfg.Sequence
	if seq == (Sequence{}) {
		seq = Sequence{KillGrace: DefaultKillGrace, CleanupDeadline: DefaultCleanupDeadline}
	}
	pipes, ok := proc.(serverPipes)
	if ok && (pipes.Stdin() == nil || pipes.Stdout() == nil || pipes.Stderr() == nil) {
		ok = false
	}
	if !ok {
		if !seq.Run(proc, false, revoke) {
			st.closeGate()
			return callResult{}, errContainmentUnverified
		}
		return callResult{}, errors.New("the backend gives a server no protocol pipes")
	}
	in, out, errOut := pipes.Stdin(), pipes.Stdout(), pipes.Stderr()

	go func() {
		_, _ = io.WriteString(in, line)
		_ = in.Close()
	}()
	go func() { _, _ = io.Copy(io.Discard, errOut) }()
	var reply bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(&reply, io.LimitReader(out, outputCap))
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
	if !seq.Run(proc, exited && w.err == nil, revoke) {
		_ = out.Close()
		_ = errOut.Close()
		st.closeGate()
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
	return callResult{exit: w.st.Code, output: reply.String()}, nil
}

func (st *serverSelfTest) closeGate() {
	if g := st.cfg.Gate; g != nil {
		g.Close("server self-test: containment not confirmed empty")
	}
}

// serverHelper is the body of the "serve" helper mode: it reads one probe
// line from standard input, runs the probe it names with SelfTestHelper and
// answers on standard output, which carries nothing else.
func serverHelper(stdin io.Reader, stdout io.Writer) int {
	line, err := bufio.NewReaderSize(io.LimitReader(stdin, outputCap), outputCap).ReadString('\n')
	if err != nil && line == "" {
		say(stdout, "no probe on standard input")
		return 1
	}
	return SelfTestHelper(strings.Split(strings.TrimSuffix(line, "\n"), "\x1f"), stdout)
}
