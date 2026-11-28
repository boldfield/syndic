package main

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/boldfield/syndic/internal/sandbox"
)

// selfTestNetwork opens the fixture of the self-test's proxy check: a
// listener for the command under test that answers the reserved self-test
// name with 200 to a request carrying its credential, and a second listener,
// made for another call, that the command must not reach.
func selfTestNetwork() (sandbox.NetworkFixture, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return sandbox.NetworkFixture{}, err
	}
	secret := hex.EncodeToString(buf[:])

	var closers []func()
	closeAll := func() {
		for _, c := range closers {
			c()
		}
	}
	fail := func(err error) (sandbox.NetworkFixture, error) {
		closeAll()
		return sandbox.NetworkFixture{}, err
	}

	var ln net.Listener
	var listener sandbox.Listener
	if fixtureUnix {
		dir, err := os.MkdirTemp("", "syndic-st")
		if err != nil {
			return fail(err)
		}
		closers = append(closers, func() { _ = os.RemoveAll(dir) })
		sock := filepath.Join(dir, "p.sock")
		if ln, err = net.Listen("unix", sock); err != nil {
			return fail(err)
		}
		listener = sandbox.Listener{Network: "unix", Address: sock}
	} else {
		var err error
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return fail(err)
		}
		listener = sandbox.Listener{Network: "tcp", Address: ln.Addr().String()}
	}
	srv := &http.Server{Handler: selfTestHandler(secret), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	closers = append(closers, func() { _ = srv.Close() })

	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fail(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := other.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	closers = append(closers, func() { _ = other.Close(); wg.Wait() })

	addr := fixtureProxyAddr
	if addr == "" {
		addr = listener.Address
	}
	u := url.URL{Scheme: "http", User: url.UserPassword("syndic", secret), Host: addr}
	var once sync.Once
	return sandbox.NetworkFixture{
		Listener:  listener,
		ProxyURL:  u.String(),
		OtherAddr: other.Addr().String(),
		Close:     func() { once.Do(closeAll) },
	}, nil
}

// selfTestHandler answers the reserved self-test name for a request that
// presents the fixture's credential and refuses everything else.
func selfTestHandler(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := parseProxyAuth(r)
		if !ok || pass != secret {
			http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
			return
		}
		if r.Method == http.MethodConnect || r.URL.Hostname() != sandbox.SelfTestHost {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// parseProxyAuth reads the Basic Proxy-Authorization credential.
func parseProxyAuth(r *http.Request) (user, pass string, ok bool) {
	h := r.Header.Get("Proxy-Authorization")
	if h == "" {
		return "", "", false
	}
	req := &http.Request{Header: http.Header{"Authorization": {h}}}
	return req.BasicAuth()
}
