package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/mcp/authtest"
	"github.com/boldfield/syndic/internal/tools"
)

func discoverLoopback(t *testing.T, s *authtest.Server, origins ...string) (*Discovery, error) {
	t.Helper()
	d, err := NewDiscoverer(Config{URL: s.ResourceURL(), AuthOrigins: origins})
	if err != nil {
		t.Fatal(err)
	}
	return d.Discover(context.Background())
}

func noCredential(t *testing.T, s *authtest.Server) {
	t.Helper()
	for _, r := range s.Requests() {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("%s %s carried a credential", r.Method, r.Path)
		}
	}
}

func TestDiscoverListedLoopbackIssuer(t *testing.T) {
	for _, challenge := range []bool{false, true} {
		s := authtest.New(t, func(c *authtest.Config) { c.ChallengeMetadata = challenge; c.IssuerPath = "/tenant" })
		got, err := discoverLoopback(t, s, s.URL)
		if err != nil {
			t.Fatalf("challenge=%v: %v", challenge, err)
		}
		if got.Resource != s.ResourceURL() || got.Issuer != s.IssuerURL() || got.Metadata.TokenEndpoint != s.URL+authtest.TokenPath {
			t.Errorf("unexpected discovery %+v", got)
		}
		noCredential(t, s)
	}
}

func TestDiscoverFallsBackAcrossLocations(t *testing.T) {
	s := authtest.New(t, func(c *authtest.Config) {
		c.Missing = []string{"/.well-known/oauth-protected-resource" + authtest.ResourcePath, "/.well-known/oauth-authorization-server"}
	})
	if _, err := discoverLoopback(t, s, s.URL); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverUnlistedLoopbackIssuerRefusedBeforeConnection(t *testing.T) {
	s := authtest.New(t)
	_, err := discoverLoopback(t, s)
	var ref *tools.RefusalError
	if !errors.As(err, &ref) {
		t.Fatalf("want a refusal, got %v", err)
	}
	for _, r := range s.Requests() {
		if strings.Contains(r.Path, "authorization-server") || strings.Contains(r.Path, "openid-configuration") {
			t.Errorf("connected to the issuer: %s", r.Path)
		}
	}
	noCredential(t, s)
}

func TestDiscoverLoopbackEndpointNeedsListing(t *testing.T) {
	s := authtest.New(t)
	other := s.Other(t)
	s.Set(func(c *authtest.Config) { c.TokenBase = other })
	if _, err := discoverLoopback(t, s, s.URL); err == nil {
		t.Fatal("token endpoint on an unlisted loopback origin was accepted")
	}
	got, err := discoverLoopback(t, s, s.URL, other)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata.TokenEndpoint != other+authtest.TokenPath {
		t.Errorf("token endpoint %q", got.Metadata.TokenEndpoint)
	}
}

func TestDiscoverRejectsBadMetadata(t *testing.T) {
	for name, mut := range map[string]func(*authtest.Config){
		"no S256":         func(c *authtest.Config) { c.NoS256 = true },
		"resource":        func(c *authtest.Config) { c.Resource = "http://127.0.0.1:1/other" },
		"issuer mismatch": func(c *authtest.Config) { c.PRMIssuer = "http://127.0.0.1:1" },
	} {
		s := authtest.New(t, mut)
		if _, err := discoverLoopback(t, s, s.URL, "http://127.0.0.1:1"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// fakeRT serves a public issuer's documents without any network.
type fakeRT struct {
	// issuer is the issuer the resource names; empty is https://issuer.example.
	issuer string
	token  string
	seen   []string
	auth   []string
}

func (f *fakeRT) RoundTrip(r *http.Request) (*http.Response, error) {
	f.seen = append(f.seen, r.URL.String())
	f.auth = append(f.auth, r.Header.Get("Authorization")+r.Header.Get("Cookie"))
	body := ""
	iss := f.issuer
	if iss == "" {
		iss = "https://issuer.example"
	}
	switch r.URL.String() {
	case "https://mcp.example/.well-known/oauth-protected-resource/mcp":
		body = `{"resource":"https://mcp.example/mcp","authorization_servers":["` + iss + `"]}`
	case iss + "/.well-known/oauth-authorization-server":
		body = `{"issuer":"` + iss + `","authorization_endpoint":"` + iss + `/a",` +
			`"token_endpoint":"` + f.token + `","code_challenge_methods_supported":["S256"]}`
	}
	status := http.StatusOK
	if body == "" {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

type fakeResolver struct{}

func (fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

func publicDiscoverer(t *testing.T, token string, origins ...string) (*Discoverer, *fakeRT) {
	t.Helper()
	rt := &fakeRT{token: token}
	d, err := newDiscoverer(Config{URL: "https://mcp.example/mcp", AuthOrigins: origins}, rt)
	if err != nil {
		t.Fatal(err)
	}
	d.resolver = fakeResolver{}
	// Connections to an unlisted origin are dialed by the discoverer itself;
	// send them to a local server that answers from rt.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := r.Clone(r.Context())
		req.URL = &url.URL{Scheme: "https", Host: r.Host, Path: r.URL.Path}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	d.tlsConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test server's certificate names no real host
	d.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	return d, rt
}

func TestDiscoverPublicIssuerPrivateTokenEndpoint(t *testing.T) {
	for _, tok := range []string{"https://10.0.0.5/token", "http://127.0.0.1:9/token", "https://[fe80::1]/token", "https://localhost/token"} {
		d, rt := publicDiscoverer(t, tok)
		if _, err := d.Discover(context.Background()); err == nil {
			t.Errorf("%s accepted", tok)
		}
		for i, a := range rt.auth {
			if a != "" {
				t.Errorf("request %s carried a credential", rt.seen[i])
			}
		}
	}
	d, _ := publicDiscoverer(t, "https://10.0.0.5/token", "https://10.0.0.5")
	if _, err := d.Discover(context.Background()); err != nil {
		t.Errorf("listed origin refused: %v", err)
	}
	d, _ = publicDiscoverer(t, "https://issuer.example/token")
	if _, err := d.Discover(context.Background()); err != nil {
		t.Errorf("public endpoint refused: %v", err)
	}
}

func TestDiscoverPrivateIssuerEndToEnd(t *testing.T) {
	for _, iss := range []string{"https://10.0.0.5", "https://192.168.1.1:8443"} {
		rt := &fakeRT{issuer: iss, token: iss + "/token"}
		d, err := newDiscoverer(Config{URL: "https://mcp.example/mcp"}, rt)
		if err != nil {
			t.Fatal(err)
		}
		d.resolver = fakeResolver{}
		if _, err := d.Discover(context.Background()); err == nil {
			t.Errorf("%s accepted", iss)
		}
		for _, u := range rt.seen {
			if strings.HasPrefix(u, iss) {
				t.Errorf("connected to the unlisted issuer: %s", u)
			}
		}
		rt = &fakeRT{issuer: iss, token: iss + "/token"}
		d, err = newDiscoverer(Config{URL: "https://mcp.example/mcp", AuthOrigins: []string{iss}, AddressAllow: []string{"10.0.0.0/8", "192.168.0.0/16"}}, rt)
		if err != nil {
			t.Fatal(err)
		}
		d.resolver = fakeResolver{}
		got, err := d.Discover(context.Background())
		if err != nil {
			t.Fatalf("%s listed: %v", iss, err)
		}
		if got.Issuer != iss {
			t.Errorf("issuer %q", got.Issuer)
		}
		fetched := false
		for _, u := range rt.seen {
			fetched = fetched || strings.HasPrefix(u, iss+"/.well-known/")
		}
		if !fetched {
			t.Errorf("%s listed but its metadata was not fetched: %v", iss, rt.seen)
		}
	}
}

// rebindResolver answers the first lookup of a name with first and every
// later one with later.
type rebindResolver struct {
	first, later netip.Addr
	calls        map[string]int
}

func (r *rebindResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.calls[host]++
	if r.calls[host] == 1 {
		return []netip.Addr{r.first}, nil
	}
	return []netip.Addr{r.later}, nil
}

func TestDiscoverConnectsToValidatedAddress(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"issuer":"https://example.com","authorization_endpoint":"https://example.com/a",`+
			`"token_endpoint":"https://example.com/token","code_challenge_methods_supported":["S256"]}`)
	}))
	srv.StartTLS()
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	public := netip.MustParseAddr("93.184.216.34")

	rt := &fakeRT{issuer: "https://example.com"}
	d, err := newDiscoverer(Config{URL: "https://mcp.example/mcp"}, rt)
	if err != nil {
		t.Fatal(err)
	}
	res := &rebindResolver{first: public, later: netip.MustParseAddr("10.0.0.9"), calls: map[string]int{}}
	d.resolver = res
	var dialed []string
	d.tlsConfig = &tls.Config{RootCAs: pool}
	d.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	if _, err := d.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(dialed) == 0 {
		t.Fatal("no connection to the issuer")
	}
	for _, a := range dialed {
		if a != "93.184.216.34:443" {
			t.Errorf("dialed %s, not the validated address", a)
		}
	}
	if res.calls["example.com"] != 1 {
		t.Errorf("issuer resolved %d times", res.calls["example.com"])
	}
	for _, u := range rt.seen {
		if strings.HasPrefix(u, "https://example.com") {
			t.Errorf("issuer request left the pinned path: %s", u)
		}
	}

	// A name that resolves to a private address at the check is refused
	// before any dial.
	dialed = nil
	d, _ = newDiscoverer(Config{URL: "https://mcp.example/mcp"}, &fakeRT{issuer: "https://example.com"})
	d.resolver = &rebindResolver{first: netip.MustParseAddr("127.0.0.1"), later: public, calls: map[string]int{}}
	d.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Error("dialed")
		return nil, errors.New("dialed")
	}
	if _, err := d.Discover(context.Background()); err == nil {
		t.Error("loopback resolution accepted")
	}
}
