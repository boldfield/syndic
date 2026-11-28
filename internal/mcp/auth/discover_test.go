package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
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
	token string
	seen  []string
	auth  []string
}

func (f *fakeRT) RoundTrip(r *http.Request) (*http.Response, error) {
	f.seen = append(f.seen, r.URL.String())
	f.auth = append(f.auth, r.Header.Get("Authorization")+r.Header.Get("Cookie"))
	body := ""
	switch r.URL.String() {
	case "https://mcp.example/.well-known/oauth-protected-resource/mcp":
		body = `{"resource":"https://mcp.example/mcp","authorization_servers":["https://issuer.example"]}`
	case "https://issuer.example/.well-known/oauth-authorization-server":
		body = `{"issuer":"https://issuer.example","authorization_endpoint":"https://issuer.example/a",` +
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

func TestDiscoverPrivateIssuerRefusedBeforeConnection(t *testing.T) {
	rt := &fakeRT{}
	d, err := newDiscoverer(Config{URL: "https://mcp.example/mcp"}, rt)
	if err != nil {
		t.Fatal(err)
	}
	d.resolver = fakeResolver{}
	if err := d.checkOrigin(context.Background(), "issuer", mustURL(t, "https://192.168.1.1")); err == nil {
		t.Error("private issuer accepted")
	}
	if len(rt.seen) != 0 {
		t.Errorf("connected: %v", rt.seen)
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
