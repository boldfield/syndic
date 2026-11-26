//go:build linux

package sandbox_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
	"github.com/boldfield/syndic/internal/sandbox/contracttest"
)

// sandboxTestEnv opts in to the enforcement suite, which needs a host that
// permits unprivileged user namespaces. With it set, an unsupported backend
// fails the test rather than skipping it.
const sandboxTestEnv = "SYNDIC_SANDBOX_TEST"

// TestLinuxNamespacesEnforcement runs the contract suite against the
// namespace backend, then the clauses only Linux can express. It runs only
// when SYNDIC_SANDBOX_TEST=1.
func TestLinuxNamespacesEnforcement(t *testing.T) {
	if os.Getenv(sandboxTestEnv) != "1" {
		t.Skip(sandboxTestEnv + "=1 is not set; the namespace enforcement suite is opt-in")
	}
	b := namespaceBackend(t)
	enforced, reason := b.Capabilities()
	if ok, missing := sandbox.Supported(enforced, false); !ok {
		t.Fatalf("backend unsupported (missing %s): %s", missing, reason)
	}
	if enforced != sandbox.Caps(sandbox.F, sandbox.N1, sandbox.P, sandbox.D) {
		t.Errorf("enforced %s, want D,F,N1,P", enforced)
	}
	t.Logf("backend %s, configuration %v", b.Name(), b.Config())

	contracttest.Run(t, b, contracttest.Fixtures{
		Passthrough: map[string]string{"SYNDIC_CONTRACT_PASS": "passed"},
		KillGrace:   200 * time.Millisecond,
	})

	helper := contracttest.BuildHelper(t)
	t.Run("proc_own_only", func(t *testing.T) { testProcOwnOnly(t, b, helper) })
	t.Run("setuid_no_privilege", func(t *testing.T) { testSetuidNoPrivilege(t, b, helper) })
	t.Run("old_root_unreachable", func(t *testing.T) { testOldRootUnreachable(t, b, helper) })
}

func namespaceBackend(t *testing.T) *sandbox.LinuxNamespaces {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sup := sandbox.Supervisor{
		Command:  []string{exe, sandbox.SupervisorArg},
		Sequence: sandbox.Sequence{KillGrace: 200 * time.Millisecond, CleanupDeadline: 10 * time.Second},
	}
	return sandbox.NewLinuxNamespaces(context.Background(), sup, defaultSystem())
}

func defaultSystem() sandbox.SystemLocations {
	return sandbox.SystemLocations{
		Path:          strings.Split(contracttest.DefaultPath, ":"),
		ReadOnlyPaths: contracttest.DefaultReadOnlyPaths(),
		Devices:       sandbox.DeviceNodes,
	}
}

// nsCall is one command's directories under the namespace backend.
type nsCall struct {
	scratch, tmp, outside, helper string
}

func newNSCall(t *testing.T, helperBin string) nsCall {
	t.Helper()
	root := t.TempDir()
	c := nsCall{
		scratch: filepath.Join(root, "scratch"),
		tmp:     filepath.Join(root, "tmp-call"),
		outside: filepath.Join(root, "outside"),
	}
	for _, d := range []string{c.scratch, c.tmp, c.outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(helperBin)
	if err != nil {
		t.Fatal(err)
	}
	c.helper = filepath.Join(c.tmp, "helper")
	if err := os.WriteFile(c.helper, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return c
}

// run runs the helper under b to completion, confirms the containment
// empty, and returns its lines after "ready".
func (c nsCall) run(t *testing.T, b sandbox.Backend, args ...string) []string {
	t.Helper()
	proc, err := b.Start(context.Background(), sandbox.Spec{
		Argv:       append([]string{c.helper}, args...),
		ScratchDir: c.scratch,
		TempDir:    c.tmp,
		Env:        sandbox.Env(sandbox.EnvConfig{Path: contracttest.DefaultPath, TempDir: c.tmp}),
		System:     defaultSystem(),
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
	if _, err := proc.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !(sandbox.Sequence{KillGrace: time.Second, CleanupDeadline: 10 * time.Second}).Run(proc, true, nil) {
		t.Error("containment unverified")
	}
	var b2 []byte
	select {
	case b2 = <-out:
	case <-time.After(10 * time.Second):
		t.Fatal("the pipe did not reach end of file")
	}
	_ = proc.Output().Close()
	lines := strings.Split(strings.TrimRight(string(b2), "\n"), "\n")
	if len(lines) == 0 || lines[0] != "ready" {
		t.Fatalf("helper did not run; output:\n%s", b2)
	}
	return lines[1:]
}

// testProcOwnOnly: the command's /proc lists itself and its child, and no
// other process: not the supervisor, not the host's.
func testProcOwnOnly(t *testing.T, b sandbox.Backend, helper string) {
	lines := newNSCall(t, helper).run(t, b, "procs")
	want := map[string]bool{}
	var listed []string
	for _, l := range lines {
		kind, pid, _ := strings.Cut(l, " ")
		switch kind {
		case "self", "child":
			want[pid] = true
		case "proc":
			listed = append(listed, pid)
		default:
			t.Fatalf("unexpected line %q in %q", l, lines)
		}
	}
	if len(want) != 2 {
		t.Fatalf("helper did not report itself and its child: %q", lines)
	}
	got := map[string]bool{}
	for _, pid := range listed {
		got[pid] = true
	}
	for pid := range want {
		if !got[pid] {
			t.Errorf("/proc does not list %s: %q", pid, lines)
		}
	}
	for pid := range got {
		if !want[pid] {
			t.Errorf("/proc lists process %s, neither the command nor its child: %q", pid, lines)
		}
	}
}

// setuidCandidates are setuid-root programs a Linux host commonly has under
// the default read-only paths.
var setuidCandidates = []string{"/usr/bin/sudo", "/usr/bin/su", "/usr/bin/passwd", "/usr/bin/mount", "/usr/bin/newgrp", "/usr/bin/chsh"}

// testSetuidNoPrivilege: a setuid-root program under a read-only path runs
// with the command's own user IDs.
func testSetuidNoPrivilege(t *testing.T, b sandbox.Backend, helper string) {
	prog := ""
	for _, p := range setuidCandidates {
		var st syscall.Stat_t
		if err := syscall.Stat(p, &st); err == nil && st.Mode&syscall.S_ISUID != 0 && st.Uid == 0 {
			prog = p
			break
		}
	}
	if prog == "" {
		t.Fatalf("no setuid-root program among %q to test with", setuidCandidates)
	}
	lines := newNSCall(t, helper).run(t, b, "suid", prog)
	if len(lines) != 1 {
		t.Fatalf("want one line, got %q", lines)
	}
	f := strings.Fields(lines[0])
	if len(f) != 4 || f[0] != "exec" || f[1] != filepath.Base(prog) {
		t.Fatalf("%s did not run as expected: %q", prog, lines[0])
	}
	if f[2] != "1000" || f[3] != f[2] {
		t.Errorf("%s ran with uid %s and euid %s, want both the command's own, 1000", prog, f[2], f[3])
	}
}

// testOldRootUnreachable: the command's mounts are exactly the planned ones,
// so the host's root is mounted nowhere, and a host file outside them is
// not found by its path or through process 1's root.
func testOldRootUnreachable(t *testing.T, b sandbox.Backend, helper string) {
	c := newNSCall(t, helper)
	marker := filepath.Join(c.outside, "marker")
	if err := os.WriteFile(marker, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := c.run(t, b, "read", marker, "/proc/1/root"+marker, "/etc/passwd")
	for _, l := range lines {
		if !strings.HasPrefix(l, "absent ") && !strings.HasPrefix(l, "denied ") {
			t.Errorf("a host path is reachable: %q", l)
		}
	}
	if len(lines) != 3 {
		t.Errorf("want 3 probe lines, got %q", lines)
	}

	want := map[string]bool{"/": true, "/proc": true, sandbox.NamespaceWorkdir: true, c.tmp: true}
	sys := defaultSystem()
	for _, group := range [][]string{sys.Path, sys.ReadOnlyPaths, sys.Devices} {
		for _, p := range group {
			if _, err := os.Stat(p); err == nil {
				want[p] = true
			}
		}
	}
	got := map[string]bool{}
	for _, l := range c.run(t, b, "mounts") {
		m, ok := strings.CutPrefix(l, "mount ")
		if !ok {
			t.Fatalf("unexpected line %q", l)
		}
		got[m] = true
	}
	var extra, missing []string
	for m := range got {
		if !want[m] {
			extra = append(extra, m)
		}
	}
	for m := range want {
		if !got[m] {
			missing = append(missing, m)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	if len(extra) > 0 || len(missing) > 0 {
		t.Errorf("mounts differ from the plan: extra %q, missing %q", extra, missing)
	}
}
