//go:build darwin && cgo

package sandbox

/*
#include <stdlib.h>
#include <sys/types.h>
#include <libproc.h>
#include <sys/proc_info.h>

// sandbox_check is private to the system library and has no header; it is
// declared here by hand. It returns 0 when pid may perform operation on the
// filter's argument, 1 when it may not, and with no operation 1 when pid is
// sandboxed at all.
extern int sandbox_check(pid_t pid, const char *operation, int type, ...);

#define SYNDIC_SANDBOX_FILTER_PATH 1
#define SYNDIC_SANDBOX_CHECK_NO_REPORT 0x40000000

// cgo cannot call a variadic function; these fix its arguments.
static int syndic_sandboxed(pid_t pid) {
	return sandbox_check(pid, NULL, 0);
}

static int syndic_check_read(pid_t pid, const char *path) {
	return sandbox_check(pid, "file-read-data", SYNDIC_SANDBOX_FILTER_PATH | SYNDIC_SANDBOX_CHECK_NO_REPORT, path);
}

// syndic_zombie reports whether pid has ended and awaits its parent's wait.
static int syndic_zombie(pid_t pid) {
	struct proc_bsdinfo info;
	int n = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, (int)sizeof info);
	return n == (int)sizeof info && info.pbi_status == 5; // SZOMB
}
*/
import "C"

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func init() {
	containers[containSandboxProfile] = func(config json.RawMessage) (Container, error) {
		var files macContainer
		if err := json.Unmarshal(config, &files); err != nil {
			return nil, fmt.Errorf("profile container: %w", err)
		}
		if files.Profile == "" || files.AllowToken == "" || files.DenyToken == "" {
			return nil, errors.New("profile container: incomplete configuration")
		}
		return &profileContainer{files: files, m: membership{allow: files.AllowToken, deny: files.DenyToken}}, nil
	}
}

// Bounds of the termination scans: rounds of stopping before the set is
// taken as it stands, and rounds of killing before the supervisor's own
// polling takes over.
const (
	maxStopRounds = 200
	maxKillRounds = 200
)

// macProbeTimeout bounds support detection.
const macProbeTimeout = 10 * time.Second

// membership is the test that identifies a call's processes by the profile
// they inherit.
type membership struct {
	allow, deny string
}

// memberC reports whether pid is a live process of the call: it is
// sandboxed, may read the allow token, and may not read the deny token. A
// process that has ended and awaits its parent is not counted: it holds
// nothing and runs nothing.
func (m membership) memberC(pid C.pid_t, allow, deny *C.char) bool {
	return C.syndic_sandboxed(pid) == 1 &&
		C.syndic_check_read(pid, allow) == 0 &&
		C.syndic_check_read(pid, deny) == 1 &&
		C.syndic_zombie(pid) == 0
}

// scan returns the members among every process on the host, sorted.
func (m membership) scan() ([]int, error) {
	pids, err := listPIDs()
	if err != nil {
		return nil, err
	}
	allow := C.CString(m.allow)
	defer C.free(unsafe.Pointer(allow))
	deny := C.CString(m.deny)
	defer C.free(unsafe.Pointer(deny))
	self := os.Getpid()
	var members []int
	for _, pid := range pids {
		if pid <= 0 || pid == self {
			continue
		}
		if m.memberC(C.pid_t(pid), allow, deny) {
			members = append(members, pid)
		}
	}
	slices.Sort(members)
	return members, nil
}

// listPIDs returns the ID of every process on the host.
func listPIDs() ([]int, error) {
	n := int(C.proc_listallpids(nil, 0))
	if n <= 0 {
		return nil, errors.New("proc_listallpids: cannot count processes")
	}
	for {
		buf := make([]C.pid_t, n+64)
		got := int(C.proc_listallpids(unsafe.Pointer(&buf[0]), C.int(len(buf)*int(unsafe.Sizeof(buf[0])))))
		if got <= 0 {
			return nil, errors.New("proc_listallpids: cannot list processes")
		}
		if got < len(buf) {
			pids := make([]int, got)
			for i := range pids {
				pids[i] = int(buf[i])
			}
			return pids, nil
		}
		n = 2 * got
	}
}

// signal sends sig to every member.
func (m membership) signal(sig syscall.Signal) error {
	set, err := m.scan()
	if err != nil {
		return err
	}
	for _, pid := range set {
		_ = unix.Kill(pid, sig)
	}
	return nil
}

// kill stops every member until two successive scans return the same set,
// so that a process that keeps forking cannot outrun the scan, kills that
// set, then scans and kills until a scan finds none.
func (m membership) kill() error {
	var prev []int
	for round := 0; round < maxStopRounds; round++ {
		set, err := m.scan()
		if err != nil {
			return err
		}
		for _, pid := range set {
			_ = unix.Kill(pid, syscall.SIGSTOP)
		}
		if round > 0 && slices.Equal(set, prev) {
			break
		}
		prev = set
	}
	for _, pid := range prev {
		_ = unix.Kill(pid, syscall.SIGKILL)
	}
	for round := 0; round < maxKillRounds; round++ {
		set, err := m.scan()
		if err != nil {
			return err
		}
		if len(set) == 0 {
			return nil
		}
		for _, pid := range set {
			_ = unix.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

// profileMembers returns the members of the call the two tokens identify.
func profileMembers(allowToken, denyToken string) ([]int, error) {
	return membership{allow: allowToken, deny: denyToken}.scan()
}

// profileContainer contains a command by running it under the call's
// profile through sandbox-exec, and finds what it contains by membership.
type profileContainer struct {
	files macContainer
	m     membership
}

// Prepare runs the command through sandbox-exec, which applies the profile
// to itself and then executes the command, resolved against the command's
// PATH.
func (c *profileContainer) Prepare(cmd *exec.Cmd) error {
	cmd.Args = append([]string{sandboxExecPath, "-f", c.files.Profile}, cmd.Args...)
	return nil
}

// Signal signals every member; SIGKILL stops them all first.
func (c *profileContainer) Signal(sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		return c.m.kill()
	}
	return c.m.signal(sig)
}

// Remains reports whether a scan finds a member. A scan that fails cannot
// confirm anything, and counts as one that found a member.
func (c *profileContainer) Remains() bool {
	set, err := c.m.scan()
	return err != nil || len(set) > 0
}

// release removes the call's profile and tokens once nothing remains.
func (c *profileContainer) release() {
	removeMacCall(c.files)
}

// macSupport reads the macOS version and tests the mechanism.
func macSupport(ctx context.Context, b *MacOSSandboxExec) (version, reason string) {
	version, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return "", fmt.Sprintf("%s: cannot read the macOS version: %v", MacOSSandboxExecName, err)
	}
	ctx, cancel := context.WithTimeout(ctx, macProbeTimeout)
	defer cancel()
	if err := macProbe(ctx, b.sys); err != nil {
		return version, fmt.Sprintf("%s: %v", MacOSSandboxExecName, err)
	}
	return version, ""
}

// macProbeScript is the throwaway command: it reports whether it can read
// the deny token, then waits to be terminated.
const macProbeScript = `if /bin/cat "$1" >/dev/null 2>&1; then echo exposed; else echo ready; fi; exec /bin/sleep 30`

// macProbe checks that the configured locations make a profile, then runs
// the probe command under a profile of its own and checks that sandbox-exec
// enforces it, that sandbox_check reports it sandboxed, that the membership
// test identifies it and nothing else, and that termination leaves no
// member.
func macProbe(ctx context.Context, sys SystemLocations) error {
	if _, err := os.Stat(sandboxExecPath); err != nil {
		return fmt.Errorf("sandbox-exec: %w", err)
	}
	if C.syndic_sandboxed(C.pid_t(os.Getpid())) != 0 {
		return errors.New("sandbox_check reports the engine itself sandboxed, or fails")
	}
	root, err := os.MkdirTemp("", "syndic-sbxprobe-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()
	scratch, tmp := root+"/scratch", root+"/tmp"
	for _, d := range []string{scratch, tmp} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
	}
	configured, err := writeMacCall(Spec{ScratchDir: scratch, TempDir: tmp, System: sys})
	if err != nil {
		return fmt.Errorf("the configured locations make no profile: %w", err)
	}
	removeMacCall(configured)

	files, err := writeMacCall(Spec{ScratchDir: scratch, TempDir: tmp, System: SystemLocations{Path: []string{"/bin"}, Devices: DeviceNodes}})
	if err != nil {
		return fmt.Errorf("probe profile: %w", err)
	}
	defer removeMacCall(files)
	m := membership{allow: files.AllowToken, deny: files.DenyToken}

	cmd := exec.Command(sandboxExecPath, "-f", files.Profile, "/bin/sh", "-c", macProbeScript, "sh", files.DenyToken)
	cmd.Dir = scratch
	cmd.Env = []string{"PATH=/bin"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sandbox-exec: %w", err)
	}
	waited := make(chan struct{})
	go func() {
		defer close(waited)
		// Wait closes the pipe, so the line is read first.
		_ = cmd.Wait()
	}()
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(out).ReadString('\n')
		lines <- line
	}()
	defer func() {
		_ = cmd.Process.Kill()
		<-waited
	}()
	var line string
	select {
	case line = <-lines:
	case <-ctx.Done():
		return fmt.Errorf("the probe command did not report: %w", ctx.Err())
	}
	switch line {
	case "ready\n":
	case "exposed\n":
		return errors.New("sandbox-exec did not deny the probe command a path its profile denies")
	default:
		return fmt.Errorf("the probe command did not run under sandbox-exec: %q", bytes.TrimSpace(stderr.Bytes()))
	}

	pid := cmd.Process.Pid
	cpid := C.pid_t(pid)
	allow := C.CString(m.allow)
	defer C.free(unsafe.Pointer(allow))
	deny := C.CString(m.deny)
	defer C.free(unsafe.Pointer(deny))
	if r := C.syndic_sandboxed(cpid); r != 1 {
		return fmt.Errorf("sandbox_check returns %d for the probe command, not 1 (sandboxed)", int(r))
	}
	if r := C.syndic_check_read(cpid, allow); r != 0 {
		return fmt.Errorf("sandbox_check returns %d for the allow token, not 0 (allowed)", int(r))
	}
	if r := C.syndic_check_read(cpid, deny); r != 1 {
		return fmt.Errorf("sandbox_check returns %d for the deny token, not 1 (denied)", int(r))
	}
	set, err := m.scan()
	if err != nil {
		return err
	}
	if !slices.Equal(set, []int{pid}) {
		return fmt.Errorf("the membership scan finds %v, not only the probe command %d", set, pid)
	}
	if err := m.kill(); err != nil {
		return err
	}
	select {
	case <-waited:
	case <-ctx.Done():
		return errors.New("the probe command did not end on termination")
	}
	if set, err := m.scan(); err != nil || len(set) != 0 {
		return fmt.Errorf("the membership scan finds %v after termination (%v)", set, err)
	}
	return nil
}
