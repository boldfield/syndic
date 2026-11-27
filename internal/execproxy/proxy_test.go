package execproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type rec struct {
	host   string
	port   int
	allow  bool
	status int
	id     string
	closed bool
	up     int64
	down   int64
}

type fakeJournal struct {
	mu   sync.Mutex
	recs []*rec
	log  *[]string
}

func (j *fakeJournal) RecordConnection(host string, port int, allowed bool, status int) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := &rec{host: host, port: port, allow: allowed, status: status, id: fmt.Sprint(len(j.recs))}
	j.recs = append(j.recs, r)
	if j.log != nil {
		*j.log = append(*j.log, "journal "+host)
	}
	return r.id, nil
}

func (j *fakeJournal) CloseConnection(id string, up, down int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, r := range j.recs {
		if r.id == id {
			r.closed, r.up, r.down = true, up, down
		}
	}
	return nil
}

func (j *fakeJournal) snapshot() []rec {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []rec
	for _, r := range j.recs {
		out = append(out, *r)
	}
	return out
}

type fakeResolver struct {
	mu    sync.Mutex
	calls []string
	addrs map[string]string
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	f.calls = append(f.calls, host)
	f.mu.Unlock()
	a, ok := f.addrs[host]
	if !ok {
		a = "93.184.216.34"
	}
	return []netip.Addr{netip.MustParseAddr(a)}, nil
}

func (f *fakeResolver) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type env struct {
	p     *Proxy
	j     *fakeJournal
	res   *fakeResolver
	stub  net.Listener
	mu    sync.Mutex
	ndial int
	check func() error
}

// newEnv starts a stub upstream that echoes and a proxy whose dials all go
// to the stub.
func newEnv(t *testing.T, patterns ...string) *env {
	t.Helper()
	e := &env{j: &fakeJournal{}, res: &fakeResolver{addrs: map[string]string{"priv.example.com": "10.0.0.5"}}}
	e.check = func() error { return nil }
	stub, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.stub = stub
	t.Cleanup(func() { stub.Close() })
	go func() {
		for {
			c, err := stub.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				if line, _ := br.Peek(4); string(line) == "GET " {
					req, err := http.ReadRequest(br)
					if err != nil {
						return
					}
					_ = req
					fmt.Fprint(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
					return
				}
				io.Copy(c, br)
			}()
		}
	}()
	if len(patterns) == 0 {
		patterns = []string{"example.com", "*.example.com"}
	}
	p, err := New(Config{
		Patterns: patterns, Journal: e.j, Resolver: e.res,
		Check: func() error { return e.check() },
	})
	if err != nil {
		t.Fatal(err)
	}
	p.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		e.mu.Lock()
		e.ndial++
		e.mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, "tcp", stub.Addr().String())
	}
	e.p = p
	t.Cleanup(p.Revoke)
	return e
}

func (e *env) dials_() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ndial
}

func auth(secret string) string {
	return "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("syndic:"+secret)) + "\r\n"
}

// connectReq sends CONNECT and returns the status and the open connection.
func connectReq(t *testing.T, addr, target, authHdr string) (int, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", target, target, authHdr)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return 0, c, br
	}
	return resp.StatusCode, c, br
}

func TestConnectAllowedJournaledBeforeOpen(t *testing.T) {
	e := newEnv(t)
	var order []string
	e.j.log = &order
	e.p.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		order = append(order, "dial "+addr)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", e.stub.Addr().String())
	}
	st, c, br := connectReq(t, e.p.Addr(), "api.example.com:443", auth(e.p.Credential()))
	if st != 200 {
		t.Fatalf("status %d", st)
	}
	fmt.Fprint(c, "hello")
	buf := make([]byte, 5)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo %q %v", buf, err)
	}
	if len(order) < 2 || order[0] != "journal api.example.com" || !strings.HasPrefix(order[1], "dial 93.184.216.34:443") {
		t.Fatalf("order %v", order)
	}
	c.Close()
	waitFor(t, func() bool { r := e.j.snapshot(); return len(r) == 1 && r[0].closed })
	r := e.j.snapshot()[0]
	if !r.allow || r.host != "api.example.com" || r.port != 443 || r.up != 5 || r.down != 5 {
		t.Fatalf("record %+v", r)
	}
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestPlainHTTPAllowed(t *testing.T) {
	e := newEnv(t)
	c, err := net.Dial("tcp", e.p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET http://api.example.com/x HTTP/1.1\r\nHost: api.example.com\r\n%s\r\n", auth(e.p.Credential()))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("%d %q", resp.StatusCode, b)
	}
	waitFor(t, func() bool { r := e.j.snapshot(); return len(r) == 1 && r[0].closed && r[0].port == 80 })
}

func TestUnlistedHostRefused403NoResolution(t *testing.T) {
	e := newEnv(t)
	st, _, _ := connectReq(t, e.p.Addr(), "evil.test:443", auth(e.p.Credential()))
	if st != 403 {
		t.Fatalf("status %d", st)
	}
	if e.res.count() != 0 || e.dials_() != 0 {
		t.Fatal("resolved or dialed")
	}
	r := e.j.snapshot()
	if len(r) != 1 || r[0].allow || r[0].status != 403 {
		t.Fatalf("%+v", r)
	}
}

func TestMissingCredential407(t *testing.T) {
	e := newEnv(t)
	for _, h := range []string{"", auth("wrong")} {
		st, _, _ := connectReq(t, e.p.Addr(), "api.example.com:443", h)
		if st != 407 {
			t.Fatalf("status %d", st)
		}
	}
	if e.dials_() != 0 || e.res.count() != 0 {
		t.Fatal("upstream work without credential")
	}
}

func TestIPLiteralRefused(t *testing.T) {
	e := newEnv(t)
	for _, tgt := range []string{"93.184.216.34:443", "[2001:db8::1]:443", "127.0.0.1:443", "2130706433:443", "0x7f.1:443"} {
		st, _, _ := connectReq(t, e.p.Addr(), tgt, auth(e.p.Credential()))
		if st != 403 {
			t.Fatalf("%s: status %d", tgt, st)
		}
	}
	if e.dials_() != 0 || e.res.count() != 0 {
		t.Fatal("upstream work")
	}
}

func TestPrivateResolutionRefused(t *testing.T) {
	e := newEnv(t)
	st, _, _ := connectReq(t, e.p.Addr(), "priv.example.com:443", auth(e.p.Credential()))
	if st != 403 || e.dials_() != 0 {
		t.Fatalf("status %d dials %d", st, e.dials_())
	}
}

func TestWildcardNotApexAndPorts(t *testing.T) {
	e := newEnv(t, "*.example.com", "api.other.test:8443")
	cred := auth(e.p.Credential())
	for tgt, want := range map[string]int{
		"example.com:443":     403,
		"a.example.com:443":   200,
		"a.example.com:8443":  403,
		"api.other.test:8443": 200,
		"api.other.test:443":  403,
	} {
		st, c, _ := connectReq(t, e.p.Addr(), tgt, cred)
		c.Close()
		if st != want {
			t.Errorf("%s: %d want %d", tgt, st, want)
		}
	}
}

func TestRevokeClosesTunnelAndRefusesNew(t *testing.T) {
	e := newEnv(t)
	st, c, br := connectReq(t, e.p.Addr(), "api.example.com:443", auth(e.p.Credential()))
	if st != 200 {
		t.Fatal(st)
	}
	before := e.dials_()
	e.p.Revoke()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("tunnel still open")
	}
	if _, err := net.DialTimeout("tcp", e.p.Addr(), time.Second); err == nil {
		t.Fatal("listener still open")
	}
	if e.dials_() != before {
		t.Fatal("dial after revoke")
	}
	e.p.Revoke() // idempotent
}

func TestCheckFailureRefusesAndAbandoned(t *testing.T) {
	e := newEnv(t)
	e.check = func() error { return errors.New("turn cancelled") }
	st, _, _ := connectReq(t, e.p.Addr(), "api.example.com:443", auth(e.p.Credential()))
	if st != 503 || e.dials_() != 0 {
		t.Fatalf("status %d dials %d", st, e.dials_())
	}
}

func TestRevokeRacingRequests(t *testing.T) {
	e := newEnv(t)
	cred := auth(e.p.Credential())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", e.p.Addr())
			if err != nil {
				return
			}
			defer c.Close()
			fmt.Fprintf(c, "CONNECT api.example.com:443 HTTP/1.1\r\nHost: api.example.com:443\r\n%s\r\n", cred)
			c.SetReadDeadline(time.Now().Add(2 * time.Second))
			io.Copy(io.Discard, c)
		}()
	}
	e.p.Revoke()
	after := e.dials_()
	wg.Wait()
	time.Sleep(50 * time.Millisecond)
	if e.dials_() != after {
		t.Fatal("dial after Revoke returned")
	}
}

func TestTwoListenersDoNotShareCredentials(t *testing.T) {
	a, b := newEnv(t), newEnv(t)
	if a.p.Credential() == b.p.Credential() {
		t.Fatal("same credential")
	}
	st, _, _ := connectReq(t, a.p.Addr(), "api.example.com:443", auth(b.p.Credential()))
	if st != 407 {
		t.Fatalf("status %d", st)
	}
}

func TestEnvAndUnixSocket(t *testing.T) {
	e := newEnv(t)
	env := e.p.Env("")
	if len(env) != 4 || !strings.HasPrefix(env[0], "HTTP_PROXY=http://syndic:") || !strings.HasPrefix(env[3], "https_proxy=") {
		t.Fatal(env)
	}
	sock := t.TempDir() + "/p.sock"
	p, err := New(Config{Patterns: []string{"example.com"}, Network: "unix", Addr: sock, Journal: e.j, Check: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Revoke()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT x.test:443 HTTP/1.1\r\nHost: x.test:443\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != 407 {
		t.Fatalf("%v %v", resp, err)
	}
}
