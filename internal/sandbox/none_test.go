package sandbox_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
	"github.com/boldfield/syndic/internal/sandbox/contracttest"
)

// noneBackend returns the unsandboxed backend with the test binary as its
// supervisor; the package's TestMain makes it one.
func noneBackend(t *testing.T) *sandbox.None {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return sandbox.NewNone(sandbox.Supervisor{
		Command:  []string{exe, sandbox.SupervisorArg},
		Sequence: sandbox.Sequence{KillGrace: 200 * time.Millisecond, CleanupDeadline: 10 * time.Second},
	})
}

// leakDescriptor opens a pipe whose descriptors are not close-on-exec, as a
// careless engine or the program that started it might hold, and returns
// them.
func leakDescriptor(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	for _, f := range []*os.File{r, w} {
		if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_SETFD, 0); errno != 0 {
			t.Fatalf("clear close-on-exec: %v", errno)
		}
	}
}

// TestNoneDescriptors checks that the command's open descriptors are
// exactly 0, 1 and 2, even when the engine holds an inheritable one.
func TestNoneDescriptors(t *testing.T) {
	leakDescriptor(t)
	helper := contracttest.BuildHelper(t)
	dir := t.TempDir()
	for _, mode := range [][]string{{"fds"}, {"fds", "scan"}} {
		proc, err := noneBackend(t).Start(context.Background(), sandbox.Spec{
			Argv:       append([]string{helper}, mode...),
			ScratchDir: dir,
			TempDir:    dir,
			Env:        sandbox.Env(sandbox.EnvConfig{Path: contracttest.DefaultPath, TempDir: dir}),
			Timeout:    time.Minute,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		out := make(chan []byte, 1)
		go func() {
			b, _ := io.ReadAll(proc.Output())
			out <- b
		}()
		if st, err := proc.Wait(); err != nil || st.Code != 0 {
			t.Fatalf("Wait: %+v %v", st, err)
		}
		select {
		case b := <-out:
			if !bytes.Equal(b, []byte("ready\n")) {
				t.Errorf("%v: command reported descriptors beyond the three: %q", mode, b)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the pipe did not reach end of file")
		}
		if !(sandbox.Sequence{KillGrace: time.Second, CleanupDeadline: 10 * time.Second}).Run(proc, true, nil) {
			t.Error("containment unverified")
		}
		_ = proc.Output().Close()
	}
}

const noneContractEnv = "SYNDIC_SANDBOX_NONE_CONTRACT"

// TestNoneContract runs the contract suite against the unsandboxed backend.
// It runs only as the child of TestNoneContractClauses, which expects most
// clauses to fail.
func TestNoneContract(t *testing.T) {
	if os.Getenv(noneContractEnv) != "1" {
		t.Skip("run by TestNoneContractClauses")
	}
	contracttest.Run(t, noneBackend(t), contracttest.Fixtures{
		Passthrough: map[string]string{"SYNDIC_CONTRACT_PASS": "passed"},
		KillGrace:   200 * time.Millisecond,
	})
}

// TestNoneContractClauses checks that the unsandboxed backend passes the D
// clause and is reported as failing the filesystem, network and
// detached-process clauses.
func TestNoneContractClauses(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the suite in a child test process")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNoneContract$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), noneContractEnv+"=1")
	out, _ := cmd.CombinedOutput()

	results := map[string]string{}
	re := regexp.MustCompile(`--- (PASS|FAIL|SKIP): TestNoneContract/(\S+)`)
	for _, m := range re.FindAllStringSubmatch(string(out), -1) {
		results[m[2]] = m[1]
	}
	for _, c := range contracttest.AllClauses() {
		if _, ok := results[string(c)]; !ok {
			t.Fatalf("clause %s did not run; output:\n%s", c, out)
		}
	}
	mustFail := []contracttest.Clause{
		contracttest.ReadOutside, contracttest.ProtectedAbsent, contracttest.WriteOutside, contracttest.OtherScratch,
		contracttest.NoSignal, contracttest.NoNetwork,
		contracttest.DetachShellExit, contracttest.DetachTimeout, contracttest.DetachTerminate,
	}
	mustPass := []contracttest.Clause{
		contracttest.Baseline, contracttest.Descriptors, contracttest.Environment, contracttest.ConfirmAfterTerminate,
	}
	for _, c := range mustFail {
		if results[string(c)] != "FAIL" {
			t.Errorf("clause %s: %s under sandbox none, want FAIL", c, results[string(c)])
		}
	}
	for _, c := range mustPass {
		if results[string(c)] != "PASS" {
			t.Errorf("clause %s: %s under sandbox none, want PASS", c, results[string(c)])
		}
	}
	if t.Failed() {
		t.Logf("child output:\n%s", out)
	}
}
