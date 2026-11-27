//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/sys/unix"
)

// NamespaceProxyAddr is the loopback address, inside the command's network
// namespace, at which the call's proxy listener is reachable under N2. The
// namespace's loopback is its own, not the host's, so the address is the
// same for every call and the engine names it in HTTP_PROXY.
const NamespaceProxyAddr = "127.0.0.1:3128"

// proxySocketTarget is the path in the command's root at which the call's
// listener socket is mounted.
const proxySocketTarget = "/.syndic-proxy.sock"

// nsProxy is the one network exception of a command's namespace.
type nsProxy struct {
	// Listen is the loopback address the forwarder listens on.
	Listen string `json:"listen"`
	// Socket is the path in the command's root of the listener's Unix
	// socket.
	Socket string `json:"socket"`
}

// planProxy adds the call's listener to plan: its Unix socket bound into the
// command's root, and a forwarder from the namespace's loopback to it
// (v7 delta, section 4, "Per-call listener").
func planProxy(plan nsPlan, l Listener) (nsPlan, error) {
	if l.Network != "unix" {
		return nsPlan{}, fmt.Errorf("the proxy listener must be a Unix socket, not %s", l.Network)
	}
	if !filepath.IsAbs(l.Address) {
		return nsPlan{}, fmt.Errorf("proxy listener %q is not an absolute path", l.Address)
	}
	src, err := filepath.EvalSymlinks(l.Address)
	if err != nil {
		return nsPlan{}, fmt.Errorf("proxy listener: %w", err)
	}
	fi, err := os.Stat(src)
	if err != nil {
		return nsPlan{}, fmt.Errorf("proxy listener: %w", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return nsPlan{}, fmt.Errorf("proxy listener %q is not a socket", l.Address)
	}
	mounts := append(append([]nsMount{}, plan.Mounts...),
		nsMount{Kind: mountBind, Source: src, Target: proxySocketTarget, Flags: flagsReadWrite})
	sort.SliceStable(mounts, func(i, j int) bool { return mounts[i].Target < mounts[j].Target })
	mounts, err = checkMounts(mounts)
	if err != nil {
		return nsPlan{}, err
	}
	plan.Mounts = mounts
	plan.Proxy = &nsProxy{Listen: NamespaceProxyAddr, Socket: proxySocketTarget}
	return plan, nil
}

// startProxy brings the namespace's loopback up, the one interface it will
// have, and forwards each connection to it at p.Listen to the Unix socket
// at p.Socket. It runs in the supervisor, which the command cannot reach,
// and ends with it.
func startProxy(p nsProxy) error {
	if err := loopbackUp(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", p.Listen)
	if err != nil {
		return fmt.Errorf("proxy forwarder: %w", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				continue
			}
			go forward(c, p.Socket)
		}
	}()
	return nil
}

// loopbackUp sets the namespace's loopback interface up.
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("loopback: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("loopback flags: %w", err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("loopback up: %w", err)
	}
	return nil
}

// forward copies between a connection and the listener's socket until both
// directions end.
func forward(c net.Conn, socket string) {
	defer func() { _ = c.Close() }()
	u, err := net.Dial("unix", socket)
	if err != nil {
		return
	}
	defer func() { _ = u.Close() }()
	var wg sync.WaitGroup
	wg.Add(2)
	half := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}
	go half(u, c)
	go half(c, u)
	wg.Wait()
}
