package contracttest

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

// plainBackend runs each command as a plain os/exec child in its own process
// group. It enforces nothing, which is what the suite must detect.
type plainBackend struct{}

func (plainBackend) Name() string                           { return "plain" }
func (plainBackend) Config() map[string]string              { return map[string]string{} }
func (plainBackend) Capabilities() (sandbox.CapSet, string) { return 0, "enforces nothing" }

func (plainBackend) Start(_ context.Context, spec sandbox.Spec) (sandbox.Process, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.ScratchDir
	cmd.Env = spec.Env
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, err
	}
	_ = pw.Close()
	p := &plainProcess{cmd: cmd, out: pr, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		p.waitErr = cmd.Wait()
	}()
	return p, nil
}

type plainProcess struct {
	cmd     *exec.Cmd
	out     *os.File
	done    chan struct{}
	waitErr error
}

func (p *plainProcess) Output() io.ReadCloser { return p.out }

func (p *plainProcess) Wait() (sandbox.ExitStatus, error) {
	<-p.done
	ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		return sandbox.ExitStatus{}, p.waitErr
	}
	if ws.Signaled() {
		return sandbox.SignalStatus(ws.Signal(), true), nil
	}
	return sandbox.ExitStatus{Code: ws.ExitStatus()}, nil
}

// Terminate signals the process group, which a detached descendant has left.
func (p *plainProcess) Terminate(sig syscall.Signal) error {
	err := syscall.Kill(-p.cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// ConfirmEmpty confirms once the process group is gone, blind to anything
// that left it.
func (p *plainProcess) ConfirmEmpty(ctx context.Context) error {
	select {
	case <-p.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		if err := syscall.Kill(-p.cmd.Process.Pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

const plainEnv = "SYNDIC_SANDBOX_PLAIN_CONTRACT"

// TestPlainBackendContract runs the suite against plainBackend. It runs only
// as the child of TestSuiteDetectsNonEnforcingBackend, which expects it to
// fail.
func TestPlainBackendContract(t *testing.T) {
	if os.Getenv(plainEnv) != "1" {
		t.Skip("run by TestSuiteDetectsNonEnforcingBackend")
	}
	closeInheritedOnExec(t)
	Run(t, plainBackend{}, Fixtures{Passthrough: map[string]string{"SYNDIC_CONTRACT_PASS": "passed"}})
}

// closeInheritedOnExec marks every descriptor above 2 close-on-exec. The
// test binary can inherit descriptors without that flag from whatever
// started it (a CI runner does), and plain os/exec would pass them on to
// every command, failing the descriptors clause for a reason that has
// nothing to do with the backend.
func closeInheritedOnExec(t *testing.T) {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatalf("list descriptors: %v", err)
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd <= 2 {
			continue
		}
		syscall.CloseOnExec(fd)
	}
}

func TestHelperBuilds(t *testing.T) {
	bin := BuildHelper(t)
	out, err := exec.Command(bin, "read", bin).CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	if want := "ready\nok "; string(out[:len(want)]) != want {
		t.Fatalf("helper output %q", out)
	}
}

func TestSuiteDetectsNonEnforcingBackend(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the suite in a child test process")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPlainBackendContract$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), plainEnv+"=1")
	out, _ := cmd.CombinedOutput()

	results := map[string]string{}
	re := regexp.MustCompile(`--- (PASS|FAIL|SKIP): TestPlainBackendContract/(\S+)`)
	for _, m := range re.FindAllStringSubmatch(string(out), -1) {
		results[m[2]] = m[1]
	}
	for _, c := range AllClauses() {
		if _, ok := results[string(c)]; !ok {
			t.Fatalf("clause %s did not run; output:\n%s", c, out)
		}
	}

	// The clauses a non-enforcing backend must fail: filesystem, signal,
	// network and containment.
	mustFail := []Clause{
		ReadOutside, ProtectedAbsent, WriteOutside, OtherScratch,
		NoSignal, NoNetwork,
		DetachShellExit, DetachTimeout, DetachTerminate,
	}
	// The clauses plain os/exec happens to satisfy; failing them would mean
	// the suite itself is broken.
	mustPass := []Clause{Baseline, Descriptors, Environment, ConfirmAfterTerminate}
	for _, c := range mustFail {
		if results[string(c)] != "FAIL" {
			t.Errorf("clause %s: %s against a non-enforcing backend, want FAIL", c, results[string(c)])
		}
	}
	for _, c := range mustPass {
		if results[string(c)] != "PASS" {
			t.Errorf("clause %s: %s against plain os/exec, want PASS", c, results[string(c)])
		}
	}
	if t.Failed() {
		t.Logf("child output:\n%s", out)
	}
}

// TestHelperReportsHighDescriptor checks the helper's descriptor probe over
// the full range: a descriptor well above 255 inherited by the helper is
// reported, by the directory listing and by the fstat fallback alike, and a
// helper given nothing beyond the three reports nothing.
func TestHelperReportsHighDescriptor(t *testing.T) {
	closeInheritedOnExec(t)
	bin := BuildHelper(t)
	const high = 300
	for _, probe := range []struct {
		name string
		args []string
	}{
		{"list", []string{"fds"}},
		{"scan", []string{"fds", "scan"}},
	} {
		t.Run(probe.name, func(t *testing.T) {
			out, err := exec.Command(bin, probe.args...).CombinedOutput()
			if err != nil {
				t.Fatalf("helper: %v\n%s", err, out)
			}
			if string(out) != "ready\n" {
				t.Errorf("with nothing inherited, helper printed %q", out)
			}

			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			defer func() { _ = w.Close() }()
			cmd := exec.Command(bin, probe.args...)
			// ExtraFiles maps entry i to descriptor 3+i; a nil entry leaves
			// that number closed, so only the high descriptor is inherited.
			cmd.ExtraFiles = make([]*os.File, high-3+1)
			cmd.ExtraFiles[high-3] = w
			out, err = cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper: %v\n%s", err, out)
			}
			if want := "ready\nfd " + strconv.Itoa(high) + "\n"; string(out) != want {
				t.Errorf("with descriptor %d inherited, helper printed %q, want %q", high, out, want)
			}
		})
	}
}
