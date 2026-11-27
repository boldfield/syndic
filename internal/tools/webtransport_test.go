package tools

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

type stubResolver struct {
	mu    sync.Mutex
	hosts []string
	addrs map[string][]string
}

func (r *stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
	var out []netip.Addr
	for _, s := range r.addrs[host] {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

type stubReferee struct {
	calls []string
	allow bool
	err   error
}

func (r *stubReferee) Allow(_ context.Context, u string) (bool, string, error) {
	r.calls = append(r.calls, u)
	return r.allow, "stub", r.err
}

type pinRig struct {
	f     *PinnedFetcher
	res   *stubResolver
	ref   *stubReferee
	dials []string
}

// newPinRig returns a fetcher whose dial seam records the address and carries
// the connection to target (or fails the test when target is empty).
func newPinRig(t *testing.T, target string, addrs map[string][]string) *pinRig {
	t.Helper()
	g := &pinRig{res: &stubResolver{addrs: addrs}, ref: &stubReferee{allow: true}}
	f, err := NewPinnedFetcher(NewURLScreener(0), g.ref, g.res)
	if err != nil {
		t.Fatal(err)
	}
	f.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		g.dials = append(g.dials, addr)
		if target == "" {
			t.Errorf("dial reached for %s", addr)
			return nil, errors.New("unexpected dial")
		}
		var d net.Dialer
		return d.DialContext(ctx, network, target)
	}
	g.f = f
	return g
}

func wantRefusal(t *testing.T, err error, rule string) {
	t.Helper()
	var r *RefusalError
	if !errors.As(err, &r) || r.Rule != rule {
		t.Fatalf("err = %v, want refusal rule %q", err, rule)
	}
}

func TestValidateAddress(t *testing.T) {
	bad := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "fc00::1", "fd12::1",
		"169.254.1.1", "fe80::1", "224.0.0.1", "ff02::1", "ff01::1", "0.0.0.0", "::",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.1.1", "::ffff:224.0.0.1",
		"::ffff:0.0.0.0", "::ffff:192.168.0.1", "::127.0.0.1", "fe80::1%eth0",
	}
	for _, s := range bad {
		a := netip.MustParseAddr(s)
		if r := ValidateAddress(a); r == nil || r.Rule != RuleAddress {
			t.Errorf("%s: not refused: %v", s, r)
		}
	}
	for _, s := range []string{"93.184.216.34", "8.8.8.8", "2606:4700::1111", "::ffff:8.8.8.8"} {
		if r := ValidateAddress(netip.MustParseAddr(s)); r != nil {
			t.Errorf("%s refused: %v", s, r)
		}
	}
}

func TestForbiddenResolutionsRefusedWithoutDial(t *testing.T) {
	cases := map[string][]string{
		"loopback": {"127.0.0.1"},
		"private":  {"10.0.0.5"},
		"mapped":   {"::ffff:127.0.0.1"},
		"mapped0":  {"::ffff:0.0.0.0"},
		"mixed":    {"93.184.216.34", "192.168.0.1"},
		"mixed6":   {"2606:4700::1111", "fe80::1"},
	}
	for name, addrs := range cases {
		t.Run(name, func(t *testing.T) {
			g := newPinRig(t, "", map[string][]string{"bad.test": addrs})
			_, err := g.f.Fetch(context.Background(), "http://bad.test/")
			wantRefusal(t, err, RuleAddress)
			if len(g.dials) != 0 {
				t.Fatalf("dialed %v", g.dials)
			}
		})
	}
}

func TestForbiddenIPLiteralsRefused(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1/", "http://[::1]/", "http://[::ffff:10.0.0.1]/", "http://169.254.169.254/", "http://0.0.0.0/"} {
		g := newPinRig(t, "", nil)
		_, err := g.f.Fetch(context.Background(), u)
		wantRefusal(t, err, RuleAddress)
		if len(g.res.hosts) != 0 || len(g.dials) != 0 {
			t.Errorf("%s: lookups %v dials %v", u, g.res.hosts, g.dials)
		}
	}
}

func TestResolverSeesNoDeniedURL(t *testing.T) {
	t.Run("screen", func(t *testing.T) {
		g := newPinRig(t, "", map[string][]string{"ok.test": {"93.184.216.34"}})
		_, err := g.f.Fetch(context.Background(), "ftp://ok.test/")
		wantRefusal(t, err, RuleScheme)
		_, err = g.f.Fetch(context.Background(), "http://ok.test/"+strings.Repeat("a", 2100))
		wantRefusal(t, err, RuleShape)
		if len(g.res.hosts) != 0 || len(g.ref.calls) != 0 || len(g.dials) != 0 {
			t.Fatalf("lookups %v referee %v dials %v", g.res.hosts, g.ref.calls, g.dials)
		}
	})
	t.Run("referee", func(t *testing.T) {
		g := newPinRig(t, "", map[string][]string{"ok.test": {"93.184.216.34"}})
		g.ref.allow = false
		_, err := g.f.Fetch(context.Background(), "http://ok.test/")
		wantRefusal(t, err, RuleReferee)
		g.ref.allow, g.ref.err = true, errors.New("boom")
		_, err = g.f.Fetch(context.Background(), "http://ok.test/")
		wantRefusal(t, err, RuleReferee)
		if len(g.ref.calls) != 2 || len(g.res.hosts) != 0 || len(g.dials) != 0 {
			t.Fatalf("referee %v lookups %v dials %v", g.ref.calls, g.res.hosts, g.dials)
		}
	})
}

func TestNewPinnedFetcherRequiresReferee(t *testing.T) {
	if _, err := NewPinnedFetcher(NewURLScreener(0), nil, &stubResolver{}); err == nil {
		t.Fatal("nil referee accepted")
	}
}

func TestPinnedConnectionPreservesHostAndOneLookup(t *testing.T) {
	var gotHost string
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotHost = r.Host
		_, _ = io.WriteString(w, "hello")
	}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), map[string][]string{"public.test": {"93.184.216.34"}})
	resp, err := g.f.Fetch(context.Background(), "http://public.test:8080/p")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "hello" || resp.StatusCode != 200 {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	if gotHost != "public.test:8080" {
		t.Errorf("Host = %q", gotHost)
	}
	if len(g.dials) != 1 || g.dials[0] != "93.184.216.34:8080" {
		t.Errorf("dials = %v", g.dials)
	}
	if len(g.res.hosts) != 1 || g.res.hosts[0] != "public.test" {
		t.Errorf("lookups = %v", g.res.hosts)
	}
	if len(g.ref.calls) != 1 {
		t.Errorf("referee calls = %v", g.ref.calls)
	}
}

func TestPinnedTLSPreservesServerName(t *testing.T) {
	var sni, host string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sni, host = r.TLS.ServerName, r.Host
	}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), map[string][]string{"example.com": {"93.184.216.34"}})
	g.f.tlsConfig = ts.Client().Transport.(*http.Transport).TLSClientConfig
	resp, err := g.f.Fetch(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if sni != "example.com" || host != "example.com" {
		t.Errorf("sni %q host %q", sni, host)
	}
	if len(g.dials) != 1 || g.dials[0] != "93.184.216.34:443" || len(g.res.hosts) != 1 {
		t.Errorf("dials %v lookups %v", g.dials, g.res.hosts)
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "http://public.test/next", http.StatusFound)
	}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), map[string][]string{"public.test": {"93.184.216.34"}})
	resp, err := g.f.Fetch(context.Background(), "http://public.test/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits != 1 || len(g.dials) != 1 {
		t.Fatalf("status %d hits %d dials %v", resp.StatusCode, hits, g.dials)
	}
}

func TestDialFallsThroughValidatedAddresses(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer ts.Close()
	g := newPinRig(t, ts.Listener.Addr().String(), map[string][]string{"public.test": {"93.184.216.34", "8.8.8.8"}})
	first := g.f.dial
	g.f.dial = func(ctx context.Context, n, a string) (net.Conn, error) {
		if strings.HasPrefix(a, "93.") {
			g.dials = append(g.dials, a)
			return nil, errors.New("refused")
		}
		return first(ctx, n, a)
	}
	resp, err := g.f.Fetch(context.Background(), "http://public.test/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(g.dials) != 2 || g.dials[1] != "8.8.8.8:80" {
		t.Fatalf("dials %v", g.dials)
	}
}
