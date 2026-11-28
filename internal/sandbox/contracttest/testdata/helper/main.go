// The runtime otherwise holds a cgroup file open for GOMAXPROCS, which
// the descriptor probe would report as inherited.
//
//go:debug containermaxprocs=0

// Command helper is the program the sandbox contract suite runs inside a
// backend. It is built from source at test time. Every mode prints "ready"
// first, then one line per probe: "ok <detail>" when the operation succeeded
// and "denied <error>" when it failed. A read of a path that does not exist
// (ENOENT) prints "absent <error>" instead of "denied", so that the suite can
// tell a protected path that is not there from one that is there but
// unreadable.
//
//	helper read <path>...             open and read each file
//	helper write <path>...            create each file
//	helper connect <network> <addr>.. dial each address and write a byte
//	helper signal <pid>               send SIGUSR1 to pid
//	helper env                        print the environment, one "env" line each
//	helper fds [scan]                 print descriptors above 2 open at start
//	helper detach <dir> exit|sleep    start a detached beater in dir, then exit or sleep
//	helper beat <dir>                 (internal) the detached beater
//	helper procs                      start a child, then list /proc (Linux)
//	helper pause                      (internal) the child: wait for end of input
//	helper suid <path>                run path, report its name and user IDs (Linux)
//	helper mounts                     list the mount points of /proc/self/mountinfo (Linux)
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Beat file names, shared with the suite.
const (
	beatFile  = "beat"
	stopFile  = "stop"
	beatEvery = 50 * time.Millisecond
	// maxLife bounds a beater that a non-enforcing backend fails to contain,
	// so that no process from the suite outlives the test binary for long.
	maxLife = 15 * time.Second
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: helper <mode> ...")
		os.Exit(2)
	}
	mode, args := os.Args[1], os.Args[2:]

	// Probe descriptors before anything below opens one. "fds scan" forces
	// the fallback that does not read the descriptor directory.
	openFDs := openDescriptors(mode == "fds" && len(args) > 0 && args[0] == "scan")
	if mode == "beat" {
		beat(args[0])
		return
	}
	if mode == "pause" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	fmt.Println("ready")
	switch mode {
	case "read":
		for _, p := range args {
			b, err := os.ReadFile(p)
			switch {
			case errors.Is(err, syscall.ENOENT):
				fmt.Printf("absent %s: %v\n", p, err)
			case err != nil:
				fmt.Printf("denied %s: %v\n", p, err)
			default:
				fmt.Printf("ok %s: %d bytes\n", p, len(b))
			}
		}
	case "write":
		for _, p := range args {
			if err := os.WriteFile(p, []byte("written"), 0o644); err != nil {
				fmt.Printf("denied %s: %v\n", p, err)
			} else {
				fmt.Printf("ok %s\n", p)
			}
		}
	case "connect":
		for i := 0; i+1 < len(args); i += 2 {
			connect(args[i], args[i+1])
		}
	case "signal":
		pid, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Printf("denied bad pid: %v\n", err)
			break
		}
		if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
			fmt.Printf("denied %v\n", err)
		} else {
			fmt.Println("ok signalled")
		}
	case "env":
		for _, kv := range os.Environ() {
			fmt.Printf("env %s\n", kv)
		}
	case "fds":
		for _, fd := range openFDs {
			fmt.Printf("fd %d\n", fd)
		}
	case "detach":
		detach(args[0], args[1])
	case "procs":
		procs()
	case "suid":
		suid(args[0])
	case "mounts":
		mounts()
	default:
		fmt.Printf("denied unknown mode %q\n", mode)
		os.Exit(2)
	}
}

func connect(network, addr string) {
	c, err := net.DialTimeout(network, addr, 5*time.Second)
	if err != nil {
		fmt.Printf("denied %s %s: %v\n", network, addr, err)
		return
	}
	defer c.Close()
	if _, err := c.Write([]byte("x")); err != nil {
		fmt.Printf("denied %s %s: %v\n", network, addr, err)
		return
	}
	fmt.Printf("ok %s %s\n", network, addr)
}

// openDescriptors returns every descriptor above 2 that is open, sorted. It
// lists the process's descriptor directory with raw system calls, so that
// nothing of the Go runtime (the network poller in particular) opens a
// descriptor first, and excludes the directory's own. Where the directory
// cannot be read, as under a backend that denies /dev, it falls back to
// probing every number up to the open-file limit with fstat.
func openDescriptors(forceScan bool) []int {
	if !forceScan {
		if fds, ok := listDescriptorDir(); ok {
			return fds
		}
	}
	return scanDescriptors()
}

// listDescriptorDir reads /dev/fd, which names the open descriptors on Linux
// (through /proc/self/fd) and macOS. ok is false when it cannot be read.
func listDescriptorDir() (fds []int, ok bool) {
	dir, err := syscall.Open("/dev/fd", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, false
	}
	defer syscall.Close(dir)
	buf := make([]byte, 8192)
	for {
		n, err := syscall.ReadDirent(dir, buf)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return nil, false
		}
		if n <= 0 {
			break
		}
		var names []string
		_, _, names = syscall.ParseDirent(buf[:n], -1, names)
		for _, name := range names {
			fd, err := strconv.Atoi(name)
			if err != nil || fd <= 2 || fd == dir {
				continue
			}
			fds = append(fds, fd)
		}
	}
	// Reading a directory on macOS duplicates its descriptor while the read
	// lasts, and the duplicate is listed: keep only what is still open.
	open := fds[:0]
	for _, fd := range fds {
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) == nil {
			open = append(open, fd)
		}
	}
	sort.Ints(open)
	return open, true
}

// scanDescriptors probes every number below the soft open-file limit, capped
// at 1<<20 (the usual Linux ceiling for the limit itself).
func scanDescriptors() []int {
	limit := 1 << 20
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err == nil && rl.Cur < uint64(limit) {
		limit = int(rl.Cur)
	}
	var fds []int
	for fd := 3; fd < limit; fd++ {
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) == nil {
			fds = append(fds, fd)
		}
	}
	return fds
}

// detach starts a beater in a new session with no descriptor on the pipe,
// waits for its first beat, and then exits or sleeps.
func detach(dir, then string) {
	self, err := os.Executable()
	if err != nil {
		fmt.Printf("denied executable: %v\n", err)
		return
	}
	cmd := exec.Command(self, "beat", dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Printf("denied start: %v\n", err)
		return
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, beatFile)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Println("denied no beat")
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Println("ok detached")
	if then == "sleep" {
		time.Sleep(maxLife)
	}
}

// beat writes an increasing counter to dir/beat until dir/stop exists, a
// write fails, or maxLife passes.
func beat(dir string) {
	start := time.Now()
	for n := 0; time.Since(start) < maxLife; n++ {
		if _, err := os.Stat(filepath.Join(dir, stopFile)); err == nil {
			return
		}
		if err := os.WriteFile(filepath.Join(dir, beatFile), []byte(strconv.Itoa(n)), 0o644); err != nil {
			return
		}
		time.Sleep(beatEvery)
	}
}

// procs starts a child that waits, prints "self <pid>" and "child <pid>",
// then one "proc <pid>" line for each process directory in /proc, and ends
// the child.
func procs() {
	self, err := os.Executable()
	if err != nil {
		fmt.Printf("denied executable: %v\n", err)
		return
	}
	cmd := exec.Command(self, "pause")
	in, err := cmd.StdinPipe()
	if err != nil {
		fmt.Printf("denied pipe: %v\n", err)
		return
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("denied start: %v\n", err)
		return
	}
	defer func() {
		_ = in.Close()
		_ = cmd.Wait()
	}()
	fmt.Printf("self %d\nchild %d\n", os.Getpid(), cmd.Process.Pid)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		fmt.Printf("denied /proc: %v\n", err)
		return
	}
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			fmt.Printf("proc %d\n", pid)
		}
	}
}

// suid runs path with no argument and, once it has executed, prints "exec
// <name> <uid> <euid>" from its /proc status, read before it is reaped so
// that a program that exits at once is still seen. A program that gained
// privilege may be hidden from its own parent; that prints "hidden".
func suid(path string) {
	want := filepath.Base(path)
	if len(want) > 15 {
		want = want[:15]
	}
	cmd := exec.Command(path)
	if err := cmd.Start(); err != nil {
		fmt.Printf("denied start: %v\n", err)
		return
	}
	defer func() { _ = cmd.Wait() }()
	status := filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "status")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f, err := os.Open(status)
		if err != nil {
			fmt.Printf("hidden %v\n", err)
			return
		}
		var name string
		var uids []string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, _ := strings.Cut(sc.Text(), ":")
			switch k {
			case "Name":
				name = strings.TrimSpace(v)
			case "Uid":
				uids = strings.Fields(v)
			}
		}
		_ = f.Close()
		if name == want && len(uids) >= 2 {
			fmt.Printf("exec %s %s %s\n", name, uids[0], uids[1])
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	fmt.Println("denied never executed")
}

// mounts prints "mount <path>" for each mount point the process sees.
func mounts() {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		fmt.Printf("denied mountinfo: %v\n", err)
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if f := strings.Fields(line); len(f) > 4 {
			fmt.Printf("mount %s\n", f[4])
		}
	}
}
