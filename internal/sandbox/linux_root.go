//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

func init() {
	containers[containNamespaces] = func(config json.RawMessage) (Container, error) {
		var plan nsPlan
		if err := json.Unmarshal(config, &plan); err != nil {
			return nil, fmt.Errorf("namespace plan: %w", err)
		}
		return &namespaces{plan: plan}, nil
	}
}

// namespaces is the container of the namespace backend, run by a supervisor
// that is process 1 of its PID namespace. Everything the command starts is
// in that namespace and nothing else is, so the supervisor signals all of it
// with kill(-1) and finds it empty when kill(-1, 0) finds no process.
type namespaces struct {
	plan nsPlan
}

// Staging names under the staging tmpfs.
const (
	stagingOld = "old" // the host's root, once the staging tmpfs is the root
	stagingNew = "new" // the command's root while it is built
)

// Prepare builds the command's root and enters it, and leaves the calling
// thread, from which the command starts, with no capability and
// no_new_privs set. It locks the calling goroutine to its thread for good:
// capabilities and no_new_privs belong to a thread, and only this one gives
// them up.
func (n *namespaces) Prepare(cmd *exec.Cmd) error {
	runtime.LockOSThread()
	if err := buildRoot(n.plan); err != nil {
		return err
	}
	// Not dumpable: the command, running as the same identity, can neither
	// trace the supervisor nor see it in a proc mounted with hidepid.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl dumpable: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl no_new_privs: %w", err)
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("clear ambient capabilities: %w", err)
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var none [2]unix.CapUserData
	if err := unix.Capset(&hdr, &none[0]); err != nil {
		return fmt.Errorf("drop capabilities: %w", err)
	}
	cmd.Dir = n.plan.Workdir
	return nil
}

// buildRoot makes every mount private, mounts a staging tmpfs over the
// staging directory and pivots into it, with the host's root under it at
// old; builds the command's root on a second tmpfs at new from the plan,
// binding each source from under old; pivots into new and detaches the
// staging root and the host's root with it; and makes the new root
// read-only.
func buildRoot(plan nsPlan) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	if err := unix.Mount("tmpfs", plan.Staging, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700"); err != nil {
		return fmt.Errorf("mount staging tmpfs: %w", err)
	}
	if err := os.Mkdir(filepath.Join(plan.Staging, stagingOld), 0o700); err != nil {
		return err
	}
	if err := unix.Chdir(plan.Staging); err != nil {
		return err
	}
	if err := unix.PivotRoot(".", stagingOld); err != nil {
		return fmt.Errorf("pivot_root to staging: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return err
	}
	newRoot := "/" + stagingNew
	if err := os.Mkdir(newRoot, 0o755); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", newRoot, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
		return fmt.Errorf("mount root tmpfs: %w", err)
	}
	for _, m := range plan.Mounts {
		if err := mountOne(newRoot, "/"+stagingOld, m); err != nil {
			return fmt.Errorf("mount %s: %w", m.Target, err)
		}
	}
	// pivot_root(".", ".") stacks the staging root on the new one; the
	// detach takes it off, with the host's root beneath it.
	if err := unix.Chdir(newRoot); err != nil {
		return err
	}
	if err := unix.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("pivot_root to the new root: %w", err)
	}
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach the old root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return err
	}
	if err := unix.Mount("", "/", "", unix.MS_BIND|unix.MS_REMOUNT|flagsReadOnly, ""); err != nil {
		return fmt.Errorf("make the root read-only: %w", err)
	}
	return nil
}

// mountOne makes m's mount point under root and mounts m there, a bind
// taking its source from under old.
func mountOne(root, old string, m nsMount) error {
	dst := filepath.Join(root, m.Target)
	if m.Kind == mountProc {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		// hidepid=2: a process the command may not trace, the supervisor
		// among them, is absent from its proc.
		return unix.Mount("proc", dst, "proc", m.Flags, "hidepid=2")
	}
	src := filepath.Join(old, m.Source)
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := makeMountPoint(dst, fi.IsDir()); err != nil {
		return err
	}
	// Not recursive: a mount beneath a source is not carried into the root.
	if err := unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind: %w", err)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(dst, &st); err != nil {
		return err
	}
	if err := unix.Mount("", dst, "", unix.MS_BIND|unix.MS_REMOUNT|m.Flags|lockedFlags(int64(st.Flags)), ""); err != nil {
		return fmt.Errorf("remount: %w", err)
	}
	return nil
}

// makeMountPoint makes dst, a directory or an empty file, unless it exists,
// as it does under a read-only bind of its parent.
func makeMountPoint(dst string, dir bool) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	if dir {
		return os.MkdirAll(dst, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// lockedFlags returns the mount flags of a bind, from its statfs flags, that
// a remount inside a user namespace must keep: the host's mount locks them.
func lockedFlags(st int64) uintptr {
	var flags uintptr
	for _, f := range []struct {
		st int64
		ms uintptr
	}{
		{unix.ST_RDONLY, unix.MS_RDONLY},
		{unix.ST_NOSUID, unix.MS_NOSUID},
		{unix.ST_NODEV, unix.MS_NODEV},
		{unix.ST_NOEXEC, unix.MS_NOEXEC},
		{unix.ST_NOATIME, unix.MS_NOATIME},
		{unix.ST_NODIRATIME, unix.MS_NODIRATIME},
		{unix.ST_RELATIME, unix.MS_RELATIME},
	} {
		if st&f.st != 0 {
			flags |= f.ms
		}
	}
	if st&(unix.ST_NOATIME|unix.ST_RELATIME) == 0 {
		flags |= unix.MS_STRICTATIME
	}
	return flags
}

// Signal signals every process of the PID namespace but the supervisor.
func (n *namespaces) Signal(sig syscall.Signal) error {
	err := unix.Kill(-1, sig)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

// Remains reaps whatever has ended and been re-parented to the supervisor,
// then reports whether any other process of the PID namespace exists.
func (n *namespaces) Remains() bool {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || pid <= 0 {
			break
		}
	}
	return !errors.Is(unix.Kill(-1, 0), unix.ESRCH)
}
