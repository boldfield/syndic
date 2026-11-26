package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

// noneRunner runs real commands under the unsandboxed backend, with the test
// binary as the supervisor; TestMain makes it one.
func noneRunner(t *testing.T) *Runner {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shell, err := SelectShell(nil)
	if err != nil {
		t.Skip(err)
	}
	seq := sandbox.Sequence{KillGrace: 200 * time.Millisecond, CleanupDeadline: 10 * time.Second}
	return &Runner{
		Backend:  sandbox.NewNone(sandbox.Supervisor{Command: []string{exe, sandbox.SupervisorArg}, Sequence: seq}),
		Shell:    shell,
		Sequence: seq,
	}
}

// realCall returns a call in fresh scratch and temporary directories, HOME
// being the latter.
func realCall(t *testing.T, command string) Call {
	t.Helper()
	scratch, home := t.TempDir(), t.TempDir()
	return Call{Command: command, Spec: sandbox.Spec{
		ScratchDir: scratch,
		TempDir:    home,
		Env:        sandbox.Env(sandbox.EnvConfig{Path: "/usr/local/bin:/usr/bin:/bin", TempDir: home}),
	}}
}

func runReal(t *testing.T, r *Runner, call Call) *Result {
	t.Helper()
	res, err := r.Run(context.Background(), call)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestRealNoStartupFiles(t *testing.T) {
	r := noneRunner(t)
	call := realCall(t, `echo "BASH_ENV=${BASH_ENV-unset} ENV=${ENV-unset}"`)
	home := call.Spec.TempDir
	startup := filepath.Join(home, "startup")
	if err := os.WriteFile(startup, []byte("echo STARTUP-FILE-READ\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".bash_profile", ".bash_login", ".profile", ".bashrc"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("echo STARTUP-FILE-READ\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Even an environment naming startup files does not reach the shell.
	call.Spec.Env = append(call.Spec.Env, "BASH_ENV="+startup, "ENV="+startup)
	res := runReal(t, r, call)
	if res.Output != "BASH_ENV=unset ENV=unset\n" || res.ExitCode != 0 {
		t.Errorf("exit %d output %q", res.ExitCode, res.Output)
	}
}

func TestRealStdinAndTerminal(t *testing.T) {
	r := noneRunner(t)
	res := runReal(t, r, realCall(t,
		`if read -r line; then echo read; else echo eof; fi; `+
			`if [ -t 0 ] || [ -t 1 ] || [ -t 2 ]; then echo terminal; else echo none; fi`))
	if res.Output != "eof\nnone\n" {
		t.Errorf("output %q", res.Output)
	}
}

func TestRealInterleaving(t *testing.T) {
	r := noneRunner(t)
	res := runReal(t, r, realCall(t, `for i in 1 2 3 4 5; do echo out$i; echo err$i >&2; done`))
	var want strings.Builder
	for i := 1; i <= 5; i++ {
		want.WriteString("out" + strconv.Itoa(i) + "\nerr" + strconv.Itoa(i) + "\n")
	}
	if res.Output != want.String() {
		t.Errorf("output %q, want %q", res.Output, want.String())
	}
}

func TestRealTimeoutTerminatesChildren(t *testing.T) {
	r := noneRunner(t)
	call := realCall(t, `sleep 1000 & echo $!; wait`)
	call.TimeoutS = intp(1)
	res := runReal(t, r, call)
	if !res.TimedOut || res.TimeoutSEffective != 1 {
		t.Fatalf("timed_out %v effective %d", res.TimedOut, res.TimeoutSEffective)
	}
	if res.Signal == 0 || res.ExitCode != 128+res.Signal {
		t.Errorf("signal %d exit %d", res.Signal, res.ExitCode)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(res.Output))
	if err != nil {
		t.Fatalf("output %q: %v", res.Output, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("child %d survives: %v", pid, err)
	}
}

func TestRealGigabyte(t *testing.T) {
	r := noneRunner(t)
	call := realCall(t, `head -c 1073741824 /dev/zero`)
	call.TimeoutS = intp(120)
	res := runReal(t, r, call)
	if res.ExitCode != 0 || res.TimedOut {
		t.Fatalf("exit %d timed_out %v", res.ExitCode, res.TimedOut)
	}
	if res.OutputBytesTotal != 1<<30 || !res.Truncated || !res.CaptureComplete {
		t.Errorf("total %d truncated %v complete %v", res.OutputBytesTotal, res.Truncated, res.CaptureComplete)
	}
	if res.OutputBytesKept != DefaultMaxOutputBytes {
		t.Errorf("kept %d", res.OutputBytesKept)
	}
}
