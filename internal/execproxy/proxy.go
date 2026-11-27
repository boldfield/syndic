// Package execproxy is the allowlist proxy through which a command reaches
// the hosts the operator listed and nothing else (design v7 delta, section 4).
// One Proxy serves one call: it has its own listener and its own random
// credential, and Revoke ends both. The proxy inspects no body and opens no
// TLS; it is a CONNECT tunnel and a plain HTTP forwarder.
package execproxy

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
)

// credentialUser is the user name in the proxy credential; the secret is the
// password.
const credentialUser = "syndic"

// dialTimeout bounds one connection attempt to a validated address.
const dialTimeout = 10 * time.Second

// Journal records the call's proxy connections. RecordConnection runs before
// the connection is opened; a refused connection carries status 403 or 407
// and an allowed one 0. CloseConnection records the bytes each way.
type Journal interface {
	RecordConnection(host string, port int, allowed bool, refusalStatus int) (id string, err error)
	CloseConnection(id string, bytesUp, bytesDown int64) error
}

// StoreJournal journals one call's connections in the journal store (F-03).
type StoreJournal struct {
	Store  *journal.Store
	CallID string
}

// RecordConnection implements Journal.
func (j StoreJournal) RecordConnection(host string, port int, allowed bool, status int) (string, error) {
	var id string
	err := j.Store.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordExecConnection(j.CallID, host, port, allowed, status)
		id = c.ID
		return err
	})
	return id, err
}

// CloseConnection implements Journal.
func (j StoreJournal) CloseConnection(id string, up, down int64) error {
	return j.Store.Tx(func(tx *journal.Tx) error {
		_, err := tx.CloseExecConnection(id, up, down)
		return err
	})
}

// Resolver resolves a hostname. *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Config describes one call's proxy.
type Config struct {
	// Patterns is tools.exec.network_allow. An entry is a host pattern as the
	// web allowlist reads it, optionally followed by ":port" to name a port
	// other than 443 and 80 for that pattern.
	Patterns []string
	// Network is "tcp" (Addr is a loopback address, "127.0.0.1:0" if empty)
	// or "unix" (Addr is the socket path).
	Network string
	Addr    string
	// Journal records connections.
	Journal Journal
	// Check reports whether the call is executing, the turn open and the
	// attempt's epoch current; a non-nil error refuses the connection.
	Check func() error
	// Resolver defaults to net.DefaultResolver.
	Resolver Resolver
}

// Proxy is one call's listener.
type Proxy struct {
	cfg      Config
	ln       net.Listener
	srv      *http.Server
	secret   string
	policies map[int]*tools.HostPolicy // 0: the default ports 443 and 80

	// Test seam; unexported so production code cannot weaken the rules.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	revoked  bool
	conns    map[net.Conn]struct{}
	inflight sync.WaitGroup
	served   chan struct{}
}

// New opens the call's listener and starts serving.
func New(cfg Config) (*Proxy, error) {
	if cfg.Journal == nil || cfg.Check == nil {
		return nil, errors.New("proxy needs a journal and a check")
	}
	if len(cfg.Patterns) == 0 {
		return nil, errors.New("proxy needs at least one host pattern")
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	policies, err := parsePatterns(cfg.Patterns)
	if err != nil {
		return nil, err
	}
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return nil, fmt.Errorf("credential: %w", err)
	}
	var ln net.Listener
	switch cfg.Network {
	case "unix":
		if cfg.Addr == "" {
			return nil, errors.New("unix proxy needs a socket path")
		}
		ln, err = net.Listen("unix", cfg.Addr)
	case "tcp", "":
		cfg.Network = "tcp"
		addr := cfg.Addr
		if addr == "" {
			addr = "127.0.0.1:0"
		}
		host, _, serr := net.SplitHostPort(addr)
		if serr != nil {
			return nil, fmt.Errorf("proxy address: %w", serr)
		}
		if ip, perr := netip.ParseAddr(host); perr != nil || !ip.IsLoopback() {
			return nil, errors.New("a tcp proxy listens on loopback only")
		}
		ln, err = net.Listen("tcp", addr)
	default:
		return nil, fmt.Errorf("unknown proxy network %q", cfg.Network)
	}
	if err != nil {
		return nil, fmt.Errorf("proxy listen: %w", err)
	}
	p := &Proxy{
		cfg:      cfg,
		ln:       ln,
		secret:   hex.EncodeToString(buf[:]),
		policies: policies,
		conns:    make(map[net.Conn]struct{}),
		served:   make(chan struct{}),
	}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.srv = &http.Server{Handler: http.HandlerFunc(p.handle), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		defer close(p.served)
		_ = p.srv.Serve(ln)
	}()
	return p, nil
}

// parsePatterns groups patterns by the port they name.
func parsePatterns(entries []string) (map[int]*tools.HostPolicy, error) {
	groups := make(map[int][]string)
	for _, e := range entries {
		port := 0
		if i := strings.LastIndexByte(e, ':'); i >= 0 {
			n, err := strconv.Atoi(e[i+1:])
			if err != nil || n < 1 || n > 65535 {
				return nil, fmt.Errorf("network_allow entry %q: bad port", e)
			}
			port, e = n, e[:i]
		}
		groups[port] = append(groups[port], e)
	}
	out := make(map[int]*tools.HostPolicy, len(groups))
	for port, list := range groups {
		pol, err := tools.NewHostPolicy(list)
		if err != nil {
			return nil, err
		}
		out[port] = pol
	}
	return out, nil
}

// allowed reports whether host and port match a pattern. An IP literal never
// matches. No name is resolved.
func (p *Proxy) allowed(host string, port int) bool {
	h := strings.TrimSuffix(host, ".")
	if h == "" || strings.ContainsAny(h, ":[]") {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return false
	}
	if isNumericName(h) {
		return false
	}
	if port == 443 || port == 80 {
		if pol := p.policies[0]; pol != nil && pol.Match(h) {
			return true
		}
	}
	if pol := p.policies[port]; pol != nil && pol.Match(h) {
		return true
	}
	return false
}

// isNumericName reports a final label that is all digits or 0x hex, the forms
// in which a client may read an IP literal.
func isNumericName(h string) bool {
	last := h[strings.LastIndexByte(h, '.')+1:]
	if last == "" {
		return false
	}
	d := strings.TrimPrefix(strings.ToLower(last), "0x")
	hex := len(d) != len(last)
	if d == "" {
		return hex
	}
	for _, c := range d {
		if c >= '0' && c <= '9' || hex && c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

// Network and Addr return the listener's address.
func (p *Proxy) Network() string { return p.ln.Addr().Network() }

// Addr is the listener's address: a socket path or host:port.
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// ProxyURL is the value of the proxy variables when the command reaches the
// listener at addr (host:port). For a Unix socket the caller passes the
// address of the forwarder it places in the command's namespace.
func (p *Proxy) ProxyURL(addr string) string {
	u := url.URL{Scheme: "http", User: url.UserPassword(credentialUser, p.secret), Host: addr}
	return u.String()
}

// Env returns HTTP_PROXY, HTTPS_PROXY and their lower-case forms as
// NAME=value entries, for a command that reaches the listener at addr. A
// tcp listener's own address is used when addr is empty.
func (p *Proxy) Env(addr string) []string {
	if addr == "" {
		addr = p.Addr()
	}
	v := p.ProxyURL(addr)
	return []string{"HTTP_PROXY=" + v, "HTTPS_PROXY=" + v, "http_proxy=" + v, "https_proxy=" + v}
}

// Credential returns the proxy password (for tests and forwarders).
func (p *Proxy) Credential() string { return p.secret }

func (p *Proxy) authorized(r *http.Request) bool {
	h := r.Header.Get("Proxy-Authorization")
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
	if err != nil {
		return false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return false
	}
	a := subtle.ConstantTimeCompare([]byte(user), []byte(credentialUser))
	b := subtle.ConstantTimeCompare([]byte(pass), []byte(p.secret))
	return a&b == 1
}

func (p *Proxy) isRevoked() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.revoked
}

// target extracts the host and port a request names.
func target(r *http.Request) (host string, port int, plain bool, err error) {
	var hp string
	def := "443"
	if r.Method == http.MethodConnect {
		hp = r.Host
		if r.URL != nil && r.URL.Host != "" {
			hp = r.URL.Host
		}
	} else {
		if r.URL == nil || r.URL.Scheme != "http" || r.URL.Host == "" {
			return "", 0, true, errors.New("request is not an absolute http URL")
		}
		plain, hp, def = true, r.URL.Host, "80"
	}
	h, ps, serr := net.SplitHostPort(hp)
	if serr != nil {
		h, ps = strings.Trim(hp, "[]"), def
		if strings.Contains(hp, "]") || strings.Count(hp, ":") > 1 {
			h = strings.Trim(hp, "[]")
		}
	}
	n, err := strconv.Atoi(ps)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, plain, errors.New("bad port")
	}
	return strings.ToLower(h), n, plain, nil
}

func (p *Proxy) refuse(w http.ResponseWriter, status int, host string, port int) {
	if status == http.StatusForbidden || status == http.StatusProxyAuthRequired {
		if host != "" && port >= 0 {
			_, _ = p.cfg.Journal.RecordConnection(host, port, false, status)
		}
	}
	if status == http.StatusProxyAuthRequired {
		w.Header().Set("Proxy-Authenticate", `Basic realm="syndic"`)
	}
	w.Header().Set("Connection", "close")
	http.Error(w, http.StatusText(status), status)
}

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	host, port, plain, terr := target(r)
	if !p.authorized(r) {
		if terr != nil {
			host, port = "", -1
		}
		p.refuse(w, http.StatusProxyAuthRequired, host, port)
		return
	}
	if p.isRevoked() {
		http.Error(w, "revoked", http.StatusServiceUnavailable)
		return
	}
	if terr != nil {
		http.Error(w, terr.Error(), http.StatusBadRequest)
		return
	}
	if !p.allowed(host, port) {
		p.refuse(w, http.StatusForbidden, host, port)
		return
	}
	up, id, status := p.connect(r.Context(), host, port)
	if up == nil {
		if status == http.StatusForbidden {
			p.refuse(w, status, "", -1) // already journaled by connect
		} else {
			http.Error(w, http.StatusText(status), status)
		}
		return
	}
	defer func() {
		_ = up.Close()
		p.untrack(up)
		_ = p.cfg.Journal.CloseConnection(id, up.up.Load(), up.down.Load())
	}()
	if plain {
		p.forward(w, r, up)
		return
	}
	p.tunnel(w, up)
}

// connect runs the sequence for an allowed host: the call check, resolution,
// the address rule, the journal record and the connection to the address
// that passed. A nil conn carries the status to answer with. A refusal by the
// address rule is journaled here as a 403.
func (p *Proxy) connect(ctx context.Context, host string, port int) (*countConn, string, int) {
	if err := p.cfg.Check(); err != nil {
		return nil, "", http.StatusServiceUnavailable
	}
	addrs, err := p.cfg.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, "", http.StatusBadGateway
	}
	for _, a := range addrs {
		if tools.ValidateAddress(a) != nil {
			_, _ = p.cfg.Journal.RecordConnection(host, port, false, http.StatusForbidden)
			return nil, "", http.StatusForbidden
		}
	}
	id, err := p.cfg.Journal.RecordConnection(host, port, true, 0)
	if err != nil {
		return nil, "", http.StatusServiceUnavailable
	}
	// Revocation closes the gate and then waits for dials in flight, so no
	// upstream connection is opened once Revoke has returned.
	p.mu.Lock()
	if p.revoked {
		p.mu.Unlock()
		p.closeRecord(id)
		return nil, "", http.StatusServiceUnavailable
	}
	p.inflight.Add(1)
	p.mu.Unlock()
	defer p.inflight.Done()
	if err := p.cfg.Check(); err != nil {
		p.closeRecord(id)
		return nil, "", http.StatusServiceUnavailable
	}
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	dial := p.dial
	if dial == nil {
		d := &net.Dialer{Timeout: dialTimeout}
		dial = d.DialContext
	}
	var last error
	for _, a := range addrs {
		c, err := dial(dctx, "tcp", net.JoinHostPort(a.Unmap().String(), strconv.Itoa(port)))
		if err != nil {
			last = err
			continue
		}
		cc := &countConn{Conn: c}
		if !p.track(cc) {
			_ = c.Close()
			p.closeRecord(id)
			return nil, "", http.StatusServiceUnavailable
		}
		return cc, id, 0
	}
	_ = last
	p.closeRecord(id)
	return nil, "", http.StatusBadGateway
}

// closeRecord closes the record of a connection that was journaled but never
// opened.
func (p *Proxy) closeRecord(id string) { _ = p.cfg.Journal.CloseConnection(id, 0, 0) }

func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.revoked {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

// countConn counts the bytes each way on the upstream side.
type countConn struct {
	net.Conn
	up, down atomic.Int64
}

func (c *countConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.down.Add(int64(n))
	return n, err
}

func (c *countConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.up.Add(int64(n))
	return n, err
}

// tunnel answers a CONNECT and copies bytes both ways until either side ends.
func (p *Proxy) tunnel(w http.ResponseWriter, up *countConn) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	if !p.track(client) {
		_ = client.Close()
		return
	}
	defer func() {
		_ = client.Close()
		p.untrack(client)
	}()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		// Bytes the client sent behind the CONNECT are already buffered.
		if n := buf.Reader.Buffered(); n > 0 {
			b, _ := buf.Peek(n)
			_, _ = up.Write(b)
		}
		_, _ = io.Copy(up, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, up)
		done <- struct{}{}
	}()
	<-done
}

var hopHeaders = []string{"Proxy-Authorization", "Proxy-Connection", "Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// forward sends a plain HTTP request over up and relays the response. Bodies
// are streamed, never read.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, up *countConn) {
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return up, nil
		},
	}
	defer tr.CloseIdleConnections()
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, "upstream failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	for _, h := range hopHeaders {
		w.Header().Del(h)
	}
	w.Header().Set("Connection", "close")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// Revoke closes the listener and every tunnel and kills the credential. No
// upstream connection opens after it returns. It is safe to call more than
// once and from the termination sequence's hook.
func (p *Proxy) Revoke() {
	p.mu.Lock()
	p.revoked = true
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	p.cancel()
	_ = p.ln.Close()
	_ = p.srv.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	p.inflight.Wait()
	// A dial that completed before the wait ended registered under the lock
	// before it saw the flag or was closed by it; close any that remain.
	p.mu.Lock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	<-p.served
}
