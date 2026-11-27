//go:build linux

package sandbox_test

import (
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

// stub accepts connections on l and counts those that send a byte.
func stub(t *testing.T, l net.Listener) *atomic.Int64 {
	t.Helper()
	t.Cleanup(func() { _ = l.Close() })
	var hits atomic.Int64
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				if n, _ := c.Read(make([]byte, 1)); n > 0 {
					hits.Add(1)
				}
			}()
		}
	}()
	return &hits
}

func unixStub(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	return path, stub(t, l)
}

// testListenerOnly: through its call's listener a command reaches the
// stub behind it; a direct connection past the proxy, to the host's
// loopback or another address, fails; and it cannot reach another call's
// listener, by its socket or through the namespace's loopback.
func testListenerOnly(t *testing.T, b sandbox.Backend, helper string) {
	own, ownHits := unixStub(t)
	other, otherHits := unixStub(t)
	hostL, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostHits := stub(t, hostL)
	host := hostL.Addr().String()

	lines := newNSCall(t, helper).runProxy(t, b, &sandbox.Listener{Network: "unix", Address: own}, "connect",
		"tcp4", sandbox.NamespaceProxyAddr,
		"tcp4", host,
		"tcp4", "192.0.2.1:80",
		"unix", other,
		"unix", own+".absent",
	)
	if len(lines) != 5 {
		t.Fatalf("helper output %q", lines)
	}
	if !strings.HasPrefix(lines[0], "ok ") {
		t.Errorf("connection to the call's listener failed: %q", lines[0])
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "denied ") {
			t.Errorf("a connection past the call's listener succeeded: %q", l)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if ownHits.Load() != 1 {
		t.Errorf("the call's listener saw %d requests, want 1", ownHits.Load())
	}
	if n := hostHits.Load(); n != 0 {
		t.Errorf("the host's loopback saw %d connections from the command", n)
	}
	if n := otherHits.Load(); n != 0 {
		t.Errorf("another call's listener saw %d connections", n)
	}

	// Without a listener the namespace's loopback is as down as before.
	lines = newNSCall(t, helper).run(t, b, "connect", "tcp4", sandbox.NamespaceProxyAddr)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "denied ") {
		t.Errorf("a call without a listener reached %s: %q", sandbox.NamespaceProxyAddr, lines)
	}
}
