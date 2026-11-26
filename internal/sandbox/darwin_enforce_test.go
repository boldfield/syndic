//go:build darwin && cgo

package sandbox_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
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
	"github.com/boldfield/syndic/internal/sandbox/contracttest"
)

// sandboxTestEnv opts in to the enforcement suite. With it set, an
// unsupported backend fails the test rather than skipping it.
const sandboxTestEnv = "SYNDIC_SANDBOX_TEST"

// TestMacOSSandboxExecEnforcement runs the contract suite against the macOS
// backend, then the clauses only macOS can express. It runs only when
// SYNDIC_SANDBOX_TEST=1.
func TestMacOSSandboxExecEnforcement(t *testing.T) {
	if os.Getenv(sandboxTestEnv) != "1" {
		t.Skip(sandboxTestEnv + "=1 is not set; the macOS enforcement suite is opt-in")
	}
	b := macBackend(t)
	enforced, reason := b.Capabilities()
	if ok, missing := sandbox.Supported(enforced, true); !ok {
		t.Fatalf("backend unsupported (missing %s): %s", missing, reason)
	}
	if enforced != sandbox.Caps(sandbox.F, sandbox.N1, sandbox.N2, sandbox.P, sandbox.D) {
		t.Errorf("enforced %s, want D,F,N1,N2,P", enforced)
	}
	t.Logf("backend %s on %s/%s, configuration %v", b.Name(), runtime.GOOS, runtime.GOARCH, b.Config())

	t.Run("no_member_idle", func(t *testing.T) { testNoMemberIdle(t) })
	contracttest.Run(t, b, contracttest.Fixtures{
		Passthrough: map[string]string{"SYNDIC_CONTRACT_PASS": "passed"},
		KillGrace:   200 * time.Millisecond,
	})

	helper := contracttest.BuildHelper(t)
	t.Run("launchctl_submit", func(t *testing.T) { testLaunchctlSubmit(t, b) })
	t.Run("launchctl_bootstrap", func(t *testing.T) { testLaunchctlBootstrap(t, b) })
	t.Run("nested_sandbox", func(t *testing.T) { testNestedSandbox(t, b) })
	t.Run("allow_token_fixed", func(t *testing.T) { testAllowTokenFixed(t, b) })
	t.Run("listener_only", func(t *testing.T) { testListenerOnly(t, b, helper) })
	t.Run("fork_storm", func(t *testing.T) { testForkStorm(t, b) })
	t.Run("no_member_after", func(t *testing.T) { testNoMemberIdle(t) })
}

func macBackend(t *testing.T) *sandbox.MacOSSandboxExec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sup := sandbox.Supervisor{
		Command:  []string{exe, sandbox.SupervisorArg},
		Sequence: sandbox.Sequence{KillGrace: 200 * time.Millisecond, CleanupDeadline: 10 * time.Second},
	}
	return sandbox.NewMacOSSandboxExec(context.Background(), sup, macDefaultSystem())
}

func macDefaultSystem() sandbox.SystemLocations {
	return sandbox.SystemLocations{
		Path:          strings.Split(contracttest.DefaultPath, ":"),
		ReadOnlyPaths: contracttest.DefaultReadOnlyPaths(),
		Devices:       sandbox.DeviceNodes,
	}
}

// macCall is one command's directories under the macOS backend.
type macCall struct {
	scratch, tmp, outside string
}

func newMacCall(t *testing.T) macCall {
	t.Helper()
	root := t.TempDir()
	c := macCall{
		scratch: filepath.Join(root, "scratch"),
		tmp:     filepath.Join(root, "tmp-call"),
		outside: filepath.Join(root, "outside"),
	}
	for _, d := range []string{c.scratch, c.tmp, c.outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func (c macCall) spec(proxy *sandbox.Listener, argv ...string) sandbox.Spec {
	return sandbox.Spec{
		Argv:       argv,
		ScratchDir: c.scratch,
		TempDir:    c.tmp,
		Env:        sandbox.Env(sandbox.EnvConfig{Path: contracttest.DefaultPath, TempDir: c.tmp}),
		System:     macDefaultSystem(),
		Timeout:    time.Minute,
		Proxy:      proxy,
	}
}

// run runs argv under b to completion, confirms the containment empty, and
// returns its output lines.
func (c macCall) run(t *testing.T, b sandbox.Backend, proxy *sandbox.Listener, argv ...string) []string {
	t.Helper()
	proc, err := b.Start(context.Background(), c.spec(proxy, argv...))
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
	return strings.Split(strings.TrimRight(string(b2), "\n"), "\n")
}

// shell runs a script under b with /bin/sh.
func (c macCall) shell(t *testing.T, b sandbox.Backend, script string) []string {
	t.Helper()
	return c.run(t, b, nil, "/bin/sh", "-c", script)
}

// field returns the value of the line "<key> <value>" in lines.
func field(t *testing.T, lines []string, key string) string {
	t.Helper()
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, key+" "); ok {
			return v
		}
	}
	t.Fatalf("no %q line in %q", key, lines)
	return ""
}

// testNoMemberIdle: with no call running under them, a fresh pair of tokens
// identifies no process on the runner, sandboxed by another profile or not.
func testNoMemberIdle(t *testing.T) {
	allowDir, denyDir := t.TempDir(), t.TempDir()
	allow, deny := filepath.Join(allowDir, "allow"), filepath.Join(denyDir, "deny")
	for _, p := range []string{allow, deny} {
		if err := os.WriteFile(p, nil, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []*string{&allow, &deny} {
		r, err := filepath.EvalSymlinks(*d)
		if err != nil {
			t.Fatal(err)
		}
		*d = r
	}
	members, err := sandbox.ProfileMembers(allow, deny)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Errorf("a scan with no call running finds members %v", members)
	}
}

// waitFile waits up to d for path to exist.
func waitFile(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	_, err := os.Stat(path)
	return err == nil
}

// escapeWait is how long a process started through launchd has to show
// itself before the attempt counts as refused.
const escapeWait = 3 * time.Second

func launchctl(args ...string) ([]byte, error) {
	return exec.Command("/bin/launchctl", args...).CombinedOutput()
}

func label(what string) string {
	return fmt.Sprintf("org.syndic.test.%s.%d.%d", what, os.Getpid(), time.Now().UnixNano())
}

// testLaunchctlSubmit: a command cannot start a process outside the sandbox
// with launchctl submit. The same submission from the test process, as a
// control, starts its job.
func testLaunchctlSubmit(t *testing.T, b sandbox.Backend) {
	c := newMacCall(t)
	control, marker := filepath.Join(c.outside, "control"), filepath.Join(c.outside, "escaped")

	cl := label("submit-control")
	t.Cleanup(func() { _, _ = launchctl("remove", cl) })
	if out, err := launchctl("submit", "-l", cl, "--", "/bin/sh", "-c", "echo x > "+control); err != nil {
		t.Fatalf("control launchctl submit: %v\n%s", err, out)
	}
	if !waitFile(control, 10*time.Second) {
		t.Fatalf("control: launchctl submit from outside the sandbox started nothing")
	}

	l := label("submit")
	t.Cleanup(func() { _, _ = launchctl("remove", l) })
	lines := c.shell(t, b, fmt.Sprintf(`/bin/launchctl submit -l %s -- /bin/sh -c 'echo x > %s'; echo "submit $?"`, l, marker))
	t.Logf("inside the sandbox: %q", lines)
	if waitFile(marker, escapeWait) {
		t.Errorf("launchctl submit from inside the sandbox started a process outside it")
	}
	if out, err := launchctl("list", l); err == nil {
		t.Errorf("launchd knows the job submitted from inside the sandbox:\n%s", out)
	}
}

// bootstrapDomain returns the first launchd domain in which bootstrap works
// from the test process, the control, which it runs with a job writing to
// control.
func bootstrapDomain(t *testing.T, dir, control string) string {
	t.Helper()
	uid := strconv.Itoa(os.Getuid())
	var errs []string
	for _, domain := range []string{"gui/" + uid, "user/" + uid} {
		l := label("bootstrap-control")
		plist := filepath.Join(dir, l+".plist")
		writePlist(t, plist, l, control)
		out, err := launchctl("bootstrap", domain, plist)
		t.Cleanup(func() { _, _ = launchctl("bootout", domain+"/"+l) })
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v: %s", domain, err, strings.TrimSpace(string(out))))
			continue
		}
		if waitFile(control, 10*time.Second) {
			return domain
		}
		errs = append(errs, domain+": the job did not run")
	}
	t.Fatalf("control: launchctl bootstrap from outside the sandbox started nothing: %s", strings.Join(errs, "; "))
	return ""
}

func writePlist(t *testing.T, path, label, marker string) {
	t.Helper()
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + label + `</string>
	<key>ProgramArguments</key>
	<array><string>/bin/sh</string><string>-c</string><string>echo x &gt; ` + marker + `</string></array>
	<key>RunAtLoad</key><true/>
</dict>
</plist>
`
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testLaunchctlBootstrap: a command cannot start a process outside the
// sandbox with launchctl bootstrap, in the domain where the test process
// can.
func testLaunchctlBootstrap(t *testing.T, b sandbox.Backend) {
	c := newMacCall(t)
	control, marker := filepath.Join(c.outside, "control"), filepath.Join(c.outside, "escaped")
	domain := bootstrapDomain(t, c.outside, control)

	l := label("bootstrap")
	plist := filepath.Join(c.scratch, "job.plist")
	writePlist(t, plist, l, marker)
	t.Cleanup(func() { _, _ = launchctl("bootout", domain+"/"+l) })
	lines := c.shell(t, b, fmt.Sprintf(`/bin/launchctl bootstrap %s %s; echo "bootstrap $?"`, domain, plist))
	t.Logf("inside the sandbox: %q", lines)
	if field(t, lines, "bootstrap") == "0" {
		t.Errorf("launchctl bootstrap from inside the sandbox succeeded")
	}
	if waitFile(marker, escapeWait) {
		t.Errorf("launchctl bootstrap from inside the sandbox started a process outside it")
	}
	if out, err := launchctl("print", domain+"/"+l); err == nil {
		t.Errorf("launchd knows the job bootstrapped from inside the sandbox:\n%s", out)
	}
}

// testNestedSandbox: a command cannot apply a second sandbox from inside,
// while it can run a program of the same directory.
func testNestedSandbox(t *testing.T, b sandbox.Backend) {
	c := newMacCall(t)
	secret := filepath.Join(c.outside, "secret")
	if err := os.WriteFile(secret, []byte("outside-secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := c.shell(t, b, fmt.Sprintf(`/usr/bin/true; echo "true $?"
/usr/bin/sandbox-exec -p '(version 1)(allow default)' /bin/cat %s; echo "nested $?"`, secret))
	t.Logf("inside the sandbox: %q", lines)
	if got := field(t, lines, "true"); got != "0" {
		t.Fatalf("/usr/bin/true exited %s inside the sandbox", got)
	}
	if field(t, lines, "nested") == "0" {
		t.Errorf("a second sandbox was applied from inside")
	}
	for _, l := range lines {
		if strings.Contains(l, "outside-secret") {
			t.Errorf("the nested command read a file outside the sandbox")
		}
	}
}

// testAllowTokenFixed: a command can read its allow token but cannot
// remove, rename or write it, which would take its processes out of the
// membership scan.
func testAllowTokenFixed(t *testing.T, b sandbox.Backend) {
	c := newMacCall(t)
	tmp, err := filepath.EvalSymlinks(c.tmp)
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(tmp, sandbox.MacAllowTokenName)
	lines := c.shell(t, b, fmt.Sprintf(`T=%s
/bin/cat "$T" >/dev/null 2>&1; echo "read $?"
/bin/rm -f "$T" 2>/dev/null; echo "rm $?"
/bin/mv "$T" "$T.moved" 2>/dev/null; echo "mv $?"
/bin/chmod u+w "$T" 2>/dev/null; echo "chmod $?"
(echo x > "$T") 2>/dev/null; echo "write $?"
/bin/test -f "$T"; echo "exists $?"`, token))
	t.Logf("inside the sandbox: %q", lines)
	if field(t, lines, "read") != "0" {
		t.Errorf("the command cannot read its allow token")
	}
	for _, op := range []string{"rm", "mv", "chmod", "write"} {
		if field(t, lines, op) == "0" {
			t.Errorf("%s of the allow token succeeded", op)
		}
	}
	if field(t, lines, "exists") != "0" {
		t.Errorf("the allow token is gone")
	}
}

// testListenerOnly: with a listener, a connection to that listener's port
// succeeds and a connection to a second local port fails.
func testListenerOnly(t *testing.T, b sandbox.Backend, helperBin string) {
	c := newMacCall(t)
	helper := filepath.Join(c.tmp, "helper")
	bin, err := os.ReadFile(helperBin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	var hits [2]atomic.Int64
	var addrs [2]string
	for i := range addrs {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		addrs[i] = l.Addr().String()
		go func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				hits[i].Add(1)
				_ = conn.Close()
			}
		}()
	}
	lines := c.run(t, b, &sandbox.Listener{Network: "tcp", Address: addrs[0]}, helper, "connect", "tcp4", addrs[0], "tcp4", addrs[1])
	if len(lines) != 3 || lines[0] != "ready" {
		t.Fatalf("helper output %q", lines)
	}
	if !strings.HasPrefix(lines[1], "ok ") {
		t.Errorf("connection to the listener failed: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "denied ") {
		t.Errorf("connection to a second local port: %q, want denied", lines[2])
	}
	time.Sleep(100 * time.Millisecond)
	if hits[0].Load() == 0 {
		t.Errorf("the listener saw no connection")
	}
	if n := hits[1].Load(); n != 0 {
		t.Errorf("the second port saw %d connections", n)
	}
}

// buildForker compiles testdata/forker with the C compiler.
func buildForker(t *testing.T, dir string) string {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Fatalf("C compiler not found: %v", err)
	}
	src, err := filepath.Abs(filepath.Join("testdata", "forker", "forker.c"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "forker")
	if b, err := exec.Command(cc, "-O2", "-o", out, src).CombinedOutput(); err != nil {
		t.Fatalf("building forker: %v\n%s", err, b)
	}
	return out
}

// testForkStorm: a helper that forks detached children, and ignores
// SIGTERM, while termination runs leaves nothing behind.
func testForkStorm(t *testing.T, b sandbox.Backend) {
	c := newMacCall(t)
	forker := buildForker(t, c.tmp)
	proc, err := b.Start(context.Background(), c.spec(nil, forker))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = proc.Output().Close() })

	var mu sync.Mutex
	pids := map[int]bool{}
	forked := make(chan struct{})
	eof := make(chan struct{})
	go func() {
		defer close(eof)
		var once sync.Once
		sc := bufio.NewScanner(proc.Output())
		for sc.Scan() {
			kind, v, _ := strings.Cut(sc.Text(), " ")
			n, _ := strconv.Atoi(v)
			switch kind {
			case "ready", "child":
				mu.Lock()
				pids[n] = true
				mu.Unlock()
			case "forked":
				if n >= 20 {
					once.Do(func() { close(forked) })
				}
			}
		}
	}()
	select {
	case <-forked:
	case <-time.After(10 * time.Second):
		t.Fatal("the forker did not fork")
	}
	// Termination begins while the forker goes on forking: SIGTERM, which
	// it ignores, the grace, then SIGKILL.
	if !(sandbox.Sequence{KillGrace: 300 * time.Millisecond, CleanupDeadline: 10 * time.Second}).Run(proc, false, nil) {
		t.Error("containment unverified")
	}
	if _, err := proc.Wait(); err != nil {
		t.Errorf("Wait: %v", err)
	}
	select {
	case <-eof:
	case <-time.After(5 * time.Second):
		t.Error("the pipe did not reach end of file")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pids) < 2 || len(pids) > 150 {
		t.Errorf("the forker reported %d processes, want between 2 and 150", len(pids))
	}
	// An orphan that has ended may wait a moment for launchd to reap it.
	deadline := time.Now().Add(2 * time.Second)
	for pid := range pids {
		for {
			err := syscall.Kill(pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("process %d of the forker remains (kill: %v)", pid, err)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if out, err := exec.Command("/usr/bin/pgrep", "-f", forker).Output(); err == nil {
		t.Errorf("processes of the forker remain: %s", out)
	}
	t.Logf("the forker reported %d processes", len(pids))
}
