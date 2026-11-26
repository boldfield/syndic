package contracttest

import (
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/boldfield/syndic/internal/sandbox"
)

// plainBackend is a deliberately non-enforcing backend: a plain os/exec child.
type plainBackend struct{}

func (plainBackend) Name() string              { return "plain" }
func (plainBackend) Config() map[string]string { return nil }
func (plainBackend) Capabilities() sandbox.Report {
	return sandbox.Report{Reason: "enforces nothing"}
}

func (plainBackend) Start(ctx context.Context, spec sandbox.Spec) (sandbox.Process, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Scratch
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, err
	}
	_ = pw.Close()
	return &plainProcess{cmd: cmd, out: pr}, nil
}

type plainProcess struct {
	cmd        *exec.Cmd
	out        io.Reader
	terminated atomic.Bool
}

func (p *plainProcess) Output() io.Reader { return p.out }

func (p *plainProcess) Wait() (sandbox.ExitStatus, error) {
	err := p.cmd.Wait()
	st := sandbox.ExitStatus{Terminated: p.terminated.Load()}
	if ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		st.Signal = int(ws.Signal())
	} else {
		st.Code = p.cmd.ProcessState.ExitCode()
	}
	_ = err
	return st, nil
}

func (p *plainProcess) Terminate(t sandbox.Termination) {
	p.terminated.Store(true)
	sig := syscall.SIGTERM
	if t == sandbox.Forced {
		sig = syscall.SIGKILL
	}
	_ = p.cmd.Process.Signal(sig)
}

// ConfirmEmpty claims emptiness without knowing it.
func (p *plainProcess) ConfirmEmpty(ctx context.Context) error { return ctx.Err() }

func TestSuiteDetectsNonEnforcingBackend(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("unix only")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	res := Evaluate(t, plainBackend{}, Fixtures{})
	for _, name := range []string{
		ReadOutside, ProtectedAbsent, WriteOutside, OtherScratch, NoNetwork,
		DetachOnExit, DetachOnTimeout, DetachOnTerminate,
	} {
		if res[name] == nil {
			t.Errorf("clause %s passed for a backend that enforces nothing", name)
		}
	}
	if res[Descriptors] != nil {
		t.Logf("descriptors: %v", res[Descriptors])
	}
	// The helper programs build and the harness itself works: a clause that
	// only needs a live process must pass on a plain child.
	if err := res[Environment]; err != nil {
		t.Errorf("environment clause should pass on a plain child with the fixed environment: %v", err)
	}
	if err := res[ConfirmEmpty]; err != nil {
		t.Errorf("confirm_empty: %v", err)
	}
}
