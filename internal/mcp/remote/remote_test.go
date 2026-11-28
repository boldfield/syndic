package remote

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/tools"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return f[host], nil
}

// newTest builds a transport whose dial is recorded and carried to l.
func newTest(t *testing.T, cfg Config, res fakeResolver, l net.Listener) (*Transport, *[]string) {
	t.Helper()
	tr, err := NewTransport(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var dialed []string
	tr.resolver = res
	tr.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		if l == nil {
			return nil, errors.New("dial refused by test")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", l.Addr().String())
	}
	return tr, &dialed
}

func get(t *testing.T, tr http.RoundTripper, raw string) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, raw, nil)
	return tr.RoundTrip(req)
}

func TestRedirectNotFollowed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	tr, _ := newTest(t, Config{URL: srv.URL}, nil, srv.Listener)
	resp, err := get(t, tr, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound || hits.Load() != 1 {
		t.Fatalf("status %d hits %d", resp.StatusCode, hits.Load())
	}
}

func TestLoopbackLiteralConnectsOnLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	tr, dialed := newTest(t, Config{URL: srv.URL}, nil, srv.Listener)
	resp, err := get(t, tr, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(*dialed) != 1 || !strings.HasPrefix((*dialed)[0], "127.0.0.1:") {
		t.Fatalf("dialed %v", *dialed)
	}
}

func TestNameResolvingToLoopbackRefused(t *testing.T) {
	var l net.Listener
	res := fakeResolver{"mcp.example.com": {netip.MustParseAddr("127.0.0.1")}}
	for _, allow := range [][]string{nil, {"10.0.0.0/8", "fc00::/7"}} {
		tr, dialed := newTest(t, Config{URL: "https://mcp.example.com/", AddressAllow: allow}, res, l)
		_, err := get(t, tr, "https://mcp.example.com/")
		var re *tools.RefusalError
		if !errors.As(err, &re) || len(*dialed) != 0 {
			t.Fatalf("allow %v: err %v dialed %v", allow, err, *dialed)
		}
	}
}

func TestPrivateNeedsAddressAllow(t *testing.T) {
	var l net.Listener
	res := fakeResolver{"mcp.example.com": {netip.MustParseAddr("10.1.2.3")}}
	tr, dialed := newTest(t, Config{URL: "https://mcp.example.com/"}, res, l)
	if _, err := get(t, tr, "https://mcp.example.com/"); err == nil || len(*dialed) != 0 {
		t.Fatalf("private address connected: %v", err)
	}
	tr, dialed = newTest(t, Config{URL: "https://mcp.example.com/", AddressAllow: []string{"10.1.0.0/16"}}, res, nil)
	// The test dial fails; only the address it was asked for matters.
	_, _ = get(t, tr, "https://mcp.example.com/")
	if len(*dialed) != 1 || (*dialed)[0] != "10.1.2.3:443" {
		t.Fatalf("dialed %v", *dialed)
	}
	// A host the roster does not name is not excepted.
	res["other.example.com"] = []netip.Addr{netip.MustParseAddr("10.1.2.3")}
	tr, dialed = newTest(t, Config{URL: "https://mcp.example.com/", AddressAllow: []string{"10.1.0.0/16"}}, res, l)
	if _, err := get(t, tr, "https://other.example.com/"); err == nil || len(*dialed) != 0 {
		t.Fatalf("unnamed host excepted: %v", err)
	}
}

func TestLinkLocalRefusedWhateverSettings(t *testing.T) {
	var l net.Listener
	res := fakeResolver{"mcp.example.com": {netip.MustParseAddr("169.254.169.254")}}
	tr, dialed := newTest(t, Config{
		URL: "https://mcp.example.com/", AddressAllow: []string{"10.0.0.0/8", "fc00::/7"},
		AuthOrigins: []string{"https://mcp.example.com"},
	}, res, l)
	if _, err := get(t, tr, "https://mcp.example.com/"); err == nil || len(*dialed) != 0 {
		t.Fatalf("link-local connected: %v", err)
	}
	if _, err := get(t, tr, "https://169.254.169.254/"); err == nil || len(*dialed) != 0 {
		t.Fatalf("link-local literal connected: %v", err)
	}
	if _, err := NewTransport(Config{URL: "https://x.example.com/", AddressAllow: []string{"169.254.0.0/16"}}); err == nil {
		t.Fatal("link-local range accepted in address_allow")
	}
}

func TestLoopbackLiteralNotExceptedWhenNotConfigured(t *testing.T) {
	var l net.Listener
	tr, dialed := newTest(t, Config{URL: "https://mcp.example.com/"}, fakeResolver{}, l)
	if _, err := get(t, tr, "http://127.0.0.1:9/"); err == nil || len(*dialed) != 0 {
		t.Fatalf("unconfigured loopback connected: %v", err)
	}
}

func TestBareAddressAllow(t *testing.T) {
	for _, tc := range []struct{ allow, ip string }{
		{"192.168.1.5", "192.168.1.5"},
		{"fd00::5", "fd00::5"},
	} {
		res := fakeResolver{"mcp.example.com": {netip.MustParseAddr(tc.ip)}}
		tr, dialed := newTest(t, Config{URL: "https://mcp.example.com/", AddressAllow: []string{tc.allow}}, res, nil)
		_, _ = get(t, tr, "https://mcp.example.com/")
		if len(*dialed) != 1 {
			t.Fatalf("%s: dialed %v", tc.allow, *dialed)
		}
	}
	// A bare address outside the private ranges is still refused.
	if _, err := NewTransport(Config{URL: "https://x.example.com/", AddressAllow: []string{"169.254.1.1"}}); err == nil {
		t.Fatal("bare link-local address_allow accepted")
	}
}
