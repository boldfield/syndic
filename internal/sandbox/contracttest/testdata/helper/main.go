// Command helper is the small program the contract suite runs inside a
// sandbox. Usage: helper <verb> [args]. It is built at test time.
package main

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func errName(err error) string {
	switch {
	case os.IsNotExist(err):
		return "ENOENT"
	case os.IsPermission(err):
		return "EACCES"
	}
	return "ERR " + err.Error()
}

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "open": // open <path>: print the file's content
		b, err := os.ReadFile(args[0])
		if err != nil {
			fmt.Println(errName(err))
			os.Exit(1)
		}
		fmt.Printf("content:%s\n", b)
	case "write": // write <path>
		if err := os.WriteFile(args[0], []byte("written"), 0o600); err != nil {
			fmt.Println(errName(err))
			os.Exit(1)
		}
		fmt.Println("written")
	case "signal": // signal <pid>: signal 0 to the pid
		if err := syscall.Kill(mustInt(args[0]), 0); err != nil {
			fmt.Println("kill:", err)
			os.Exit(1)
		}
		fmt.Println("signalled")
	case "connect": // connect <network> <address>
		c, err := net.DialTimeout(args[0], args[1], 2*time.Second)
		if err != nil {
			fmt.Println("dial:", err)
			os.Exit(1)
		}
		c.Close()
		fmt.Println("connected")
	case "env":
		env := os.Environ()
		sort.Strings(env)
		for _, e := range env {
			fmt.Println(e)
		}
	case "fds":
		fds()
	case "detach": // detach <beatfile>: start a new session and return
		self, _ := os.Executable()
		attr := &os.ProcAttr{
			Files: []*os.File{nil, os.Stdout, os.Stderr},
			Sys:   &syscall.SysProcAttr{Setsid: true},
		}
		p, err := os.StartProcess(self, []string{self, "beat", args[0]}, attr)
		if err != nil {
			fmt.Println("start:", err)
			os.Exit(1)
		}
		p.Release()
	case "beat": // beat <file>: write an increasing counter until told off
		for i := 0; i < 1000; i++ {
			_ = os.WriteFile(args[0], []byte(strconv.Itoa(i)), 0o600)
			time.Sleep(20 * time.Millisecond)
		}
	case "sleep": // sleep <seconds>
		time.Sleep(time.Duration(mustInt(args[0])) * time.Second)
	default:
		os.Exit(2)
	}
}

func mustInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		os.Exit(2)
	}
	return n
}

// fds prints the open descriptors above 2 and whether stdin is at EOF.
func fds() {
	dir := "/proc/self/fd"
	if _, err := os.Stat(dir); err != nil {
		dir = "/dev/fd"
	}
	d, err := os.Open(dir)
	if err != nil {
		fmt.Println("listdir:", err)
		os.Exit(1)
	}
	names, _ := d.Readdirnames(-1)
	self := int(d.Fd())
	d.Close()
	var extra []string
	for _, n := range names {
		v, err := strconv.Atoi(n)
		if err != nil || v <= 2 || v == self {
			continue
		}
		extra = append(extra, n)
	}
	buf := make([]byte, 1)
	n, _ := os.Stdin.Read(buf)
	fmt.Printf("stdin_bytes=%d\nextra=%s\n", n, strings.Join(extra, ","))
}
