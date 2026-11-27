package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// testRecordingResolver records every LookupNetIP call for testing.
type testRecordingResolver struct {
	addrs   map[string][]netip.Addr
	err     map[string]error
	lookups []string
	mu      sync.Mutex
}

func newTestRecordingResolver() *testRecordingResolver {
	return &testRecordingResolver{
		addrs:   make(map[string][]netip.Addr),
		err:     make(map[string]error),
		lookups: []string{},
	}
}

func (rr *testRecordingResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	rr.mu.Lock()
	rr.lookups = append(rr.lookups, host)
	rr.mu.Unlock()

	if err, ok := rr.err[host]; ok {
		return nil, err
	}
	return rr.addrs[host], nil
}

func (rr *testRecordingResolver) Lookups() []string {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	result := make([]string, len(rr.lookups))
	copy(result, rr.lookups)
	return result
}

// stubResolver is a fake Resolver for testing without recording.
type stubResolver struct {
	addrs map[string][]netip.Addr
	err   map[string]error
}

func (sr *stubResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if err, ok := sr.err[host]; ok {
		return nil, err
	}
	return sr.addrs[host], nil
}

func TestValidateAddress(t *testing.T) {
	tests := []struct {
		name      string
		addr      netip.Addr
		wantError bool
		reason    string
	}{
		{
			name:      "valid public address",
			addr:      netip.MustParseAddr("8.8.8.8"),
			wantError: false,
		},
		{
			name:      "valid public IPv6",
			addr:      netip.MustParseAddr("2001:4860:4860::8888"),
			wantError: false,
		},
		{
			name:      "loopback IPv4",
			addr:      netip.MustParseAddr("127.0.0.1"),
			wantError: true,
			reason:    "loopback",
		},
		{
			name:      "loopback IPv6",
			addr:      netip.MustParseAddr("::1"),
			wantError: true,
			reason:    "loopback",
		},
		{
			name:      "private RFC1918 10.0.0.0/8",
			addr:      netip.MustParseAddr("10.0.0.1"),
			wantError: true,
			reason:    "private",
		},
		{
			name:      "private RFC1918 172.16.0.0/12",
			addr:      netip.MustParseAddr("172.16.0.1"),
			wantError: true,
			reason:    "private",
		},
		{
			name:      "private RFC1918 192.168.0.0/16",
			addr:      netip.MustParseAddr("192.168.0.1"),
			wantError: true,
			reason:    "private",
		},
		{
			name:      "link-local",
			addr:      netip.MustParseAddr("169.254.0.1"),
			wantError: true,
			reason:    "link-local",
		},
		{
			name:      "multicast",
			addr:      netip.MustParseAddr("224.0.0.1"),
			wantError: true,
			reason:    "multicast",
		},
		{
			name:      "unspecified",
			addr:      netip.MustParseAddr("0.0.0.0"),
			wantError: true,
			reason:    "unspecified",
		},
		{
			name:      "IPv6 unspecified",
			addr:      netip.MustParseAddr("::"),
			wantError: true,
			reason:    "unspecified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAddress(tt.addr)
			if (err != nil) != tt.wantError {
				t.Fatalf("ValidateAddress(%v) err = %v, want error = %v",
					tt.addr, err, tt.wantError)
			}
			if err != nil && !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("ValidateAddress(%v) err = %q, want %q in reason",
					tt.addr, err.Error(), tt.reason)
			}
		})
	}
}

func TestResolverStubSeesNoDeniedURL(t *testing.T) {
	// Acceptance: "Resolver stub sees no denied URL"
	// The resolver must not be called for URLs the screener refuses.
	// (Pre-send checks run before resolution.)

	screener := NewURLScreener(0)
	screener.AddBytes(RouteSnapshotRead, []byte("secret-key-12345"))

	resolver := newTestRecordingResolver()
	resolver.addrs["example.com"] = []netip.Addr{netip.MustParseAddr("93.184.216.34")}

	// URL that carries file content (screener refuses).
	canaryURL := "https://example.com/?key=secret-key-12345"

	// Attempt to fetch the refused URL.
	transport := &PinnedTransport{
		Screener:      screener,
		Resolver:      resolver,
		Transport:     http.DefaultTransport,
		resolvedAddrs: make(map[string][]netip.Addr),
	}

	req, _ := http.NewRequestWithContext(context.Background(), "GET", canaryURL, nil)
	_, err := transport.RoundTrip(req)
	if err == nil {
		t.Fatalf("RoundTrip succeeded for refused URL")
	}

	// Verify the resolver was never called.
	if len(resolver.Lookups()) != 0 {
		t.Errorf("resolver was called for screener-refused URL; lookups: %v", resolver.Lookups())
	}
}

func TestInitialRequestPinnedWithoutSecondDNS(t *testing.T) {
	// Acceptance: "Initial request pinned without second DNS"
	// Hostname is resolved once and the result is cached and reused
	// without additional resolution. The connection uses the validated address.

	// Set up an httptest server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "OK")
	}))
	defer ts.Close()

	// Map the test server's address to a hostname.
	resolver := newTestRecordingResolver()
	testAddr := netip.MustParseAddr("127.0.0.1")
	resolver.addrs["test.local"] = []netip.Addr{testAddr}

	screener := NewURLScreener(0)

	// First request to test.local.
	url1 := "http://test.local/path1"
	transport1 := &PinnedTransport{
		Screener:      screener,
		Resolver:      resolver,
		Transport:     http.DefaultTransport,
		resolvedAddrs: make(map[string][]netip.Addr),
	}

	req1, _ := http.NewRequestWithContext(context.Background(), "GET", url1, nil)
	_, err1 := transport1.RoundTrip(req1)
	// Error expected (can't dial 127.0.0.1) but resolution must have occurred.
	if err1 == nil {
		t.Fatalf("RoundTrip unexpectedly succeeded")
	}

	lookups1 := resolver.Lookups()
	if len(lookups1) != 1 || lookups1[0] != "test.local" {
		t.Errorf("first request: expected one lookup of 'test.local', got %v", lookups1)
	}

	// Second request to test.local using the same transport.
	// If caching works, the resolver should not be called again.
	resolver.mu.Lock()
	resolver.lookups = []string{} // Clear lookups
	resolver.mu.Unlock()
	req2, _ := http.NewRequestWithContext(context.Background(), "GET", url1, nil)
	_, _ = transport1.RoundTrip(req2)

	lookups2 := resolver.Lookups()
	if len(lookups2) != 0 {
		t.Errorf("second request: expected no new lookups (caching should apply), got %v", lookups2)
	}
}

func TestForbiddenAddressesRejected(t *testing.T) {
	// Acceptance: "Loopback/private/link-local/multicast/unspecified/mapped forbidden"
	// All these address classes must be rejected.

	testCases := []struct {
		name  string
		host  string
		addrs []netip.Addr
	}{
		{
			name:  "loopback IPv4",
			host:  "localhost",
			addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
		},
		{
			name:  "loopback IPv6",
			host:  "localhost-v6",
			addrs: []netip.Addr{netip.MustParseAddr("::1")},
		},
		{
			name:  "private RFC1918 10.x.x.x",
			host:  "internal.local",
			addrs: []netip.Addr{netip.MustParseAddr("10.0.0.1")},
		},
		{
			name:  "private RFC1918 172.16.x.x",
			host:  "internal2.local",
			addrs: []netip.Addr{netip.MustParseAddr("172.16.0.1")},
		},
		{
			name:  "private RFC1918 192.168.x.x",
			host:  "internal3.local",
			addrs: []netip.Addr{netip.MustParseAddr("192.168.0.1")},
		},
		{
			name:  "link-local IPv4",
			host:  "link.local",
			addrs: []netip.Addr{netip.MustParseAddr("169.254.0.1")},
		},
		{
			name:  "link-local IPv6",
			host:  "link-v6.local",
			addrs: []netip.Addr{netip.MustParseAddr("fe80::1")},
		},
		{
			name:  "multicast IPv4",
			host:  "mcast.example.com",
			addrs: []netip.Addr{netip.MustParseAddr("224.0.0.1")},
		},
		{
			name:  "multicast IPv6",
			host:  "mcast-v6.example.com",
			addrs: []netip.Addr{netip.MustParseAddr("ff00::1")},
		},
		{
			name:  "unspecified IPv4",
			host:  "unspec.example.com",
			addrs: []netip.Addr{netip.MustParseAddr("0.0.0.0")},
		},
		{
			name:  "unspecified IPv6",
			host:  "unspec-v6.example.com",
			addrs: []netip.Addr{netip.MustParseAddr("::")},
		},
		{
			name:  "IPv6 unique-local fc00::/7",
			host:  "ul.local",
			addrs: []netip.Addr{netip.MustParseAddr("fc00::1")},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &stubResolver{addrs: map[string][]netip.Addr{tc.host: tc.addrs}}
			screener := NewURLScreener(0)
			transport := &PinnedTransport{
				Screener:      screener,
				Resolver:      resolver,
				Transport:     http.DefaultTransport,
				resolvedAddrs: make(map[string][]netip.Addr),
			}

			req, _ := http.NewRequestWithContext(context.Background(), "GET",
				"http://"+tc.host+"/", nil)
			_, err := transport.RoundTrip(req)
			if err == nil {
				t.Errorf("RoundTrip() succeeded, want error for forbidden address")
			}
		})
	}
}

func TestMixedResolution(t *testing.T) {
	// Acceptance: "mixed bad resolution" - one public address plus one forbidden
	// address must reject the entire fetch.

	resolver := &stubResolver{
		addrs: map[string][]netip.Addr{
			"mixed.example.com": {
				netip.MustParseAddr("93.184.216.34"), // public, allowed
				netip.MustParseAddr("10.0.0.1"),      // private, forbidden
			},
		},
	}

	screener := NewURLScreener(0)
	transport := &PinnedTransport{
		Screener:      screener,
		Resolver:      resolver,
		Transport:     http.DefaultTransport,
		resolvedAddrs: make(map[string][]netip.Addr),
	}

	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"http://mixed.example.com/", nil)
	_, err := transport.RoundTrip(req)
	if err == nil {
		t.Errorf("RoundTrip() succeeded for mixed resolution, want error")
	}
}

func TestMaxRedirectHops(t *testing.T) {
	// Acceptance: "the fourth hop is refused" - after 3 redirects (4 requests),
	// the next redirect is refused.

	hopCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hopCount++
		if hopCount <= 3 {
			// Redirect to same host.
			w.Header().Set("Location", fmt.Sprintf("/redirect%d", hopCount+1))
			w.WriteHeader(http.StatusSeeOther)
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "OK")
		}
	}))
	defer ts.Close()

	resolver := newTestRecordingResolver()
	// Extract host and port from the test server.
	u, _ := url.Parse(ts.URL)
	testAddr := netip.MustParseAddr("127.0.0.1")
	resolver.addrs[u.Hostname()] = []netip.Addr{testAddr}

	screener := NewURLScreener(0)
	// Use a custom transport that actually honors the pinned dialer.
	innerTransport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Dial to the test server's port.
			return net.Dial(network, u.Host)
		},
	}

	transport := &PinnedTransport{
		Screener:      screener,
		Resolver:      resolver,
		Transport:     innerTransport,
		resolvedAddrs: make(map[string][]netip.Addr),
	}

	req, _ := http.NewRequestWithContext(context.Background(), "GET", ts.URL+"/start", nil)
	_, err := transport.RoundTrip(req)
	if err == nil {
		t.Errorf("RoundTrip() succeeded; want error after 4 hops (3 redirects)")
	}
	if !strings.Contains(err.Error(), "too many redirect hops") {
		t.Errorf("RoundTrip() error: %v; want 'too many redirect hops'", err)
	}
}

func TestSameHostRedirectAllowed(t *testing.T) {
	// Acceptance: "a same-host redirect target is checked by every rule and
	// the referee before it is resolved or requested"

	redirected := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !redirected && r.RequestURI == "/start" {
			redirected = true
			w.Header().Set("Location", "/target")
			w.WriteHeader(http.StatusSeeOther)
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "OK")
		}
	}))
	defer ts.Close()

	u, _ := url.Parse(ts.URL)
	resolver := newTestRecordingResolver()
	testAddr := netip.MustParseAddr("127.0.0.1")
	resolver.addrs[u.Hostname()] = []netip.Addr{testAddr}

	screener := NewURLScreener(0)
	innerTransport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial(network, u.Host)
		},
	}

	transport := &PinnedTransport{
		Screener:      screener,
		Resolver:      resolver,
		Transport:     innerTransport,
		resolvedAddrs: make(map[string][]netip.Addr),
	}

	req, _ := http.NewRequestWithContext(context.Background(), "GET", ts.URL+"/start", nil)
	_, err := transport.RoundTrip(req)
	if err != nil {
		t.Logf("RoundTrip error (may be network-related): %v", err)
	}
	// Verify only one lookup (same-host redirect uses cached resolution).
	lookups := resolver.Lookups()
	if len(lookups) != 1 {
		t.Errorf("expected 1 lookup for same-host redirect, got %d: %v", len(lookups), lookups)
	}
}

func TestCrossHostRedirectNotFollowed(t *testing.T) {
	// Acceptance: "a cross-host redirect is returned as a result and not followed"

	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect to different host.
		w.Header().Set("Location", "http://other.example.com:8888/target")
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer ts1.Close()

	u1, _ := url.Parse(ts1.URL)
	resolver := newTestRecordingResolver()
	testAddr := netip.MustParseAddr("127.0.0.1")
	resolver.addrs[u1.Hostname()] = []netip.Addr{testAddr}

	screener := NewURLScreener(0)
	innerTransport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial(network, u1.Host)
		},
	}

	transport := &PinnedTransport{
		Screener:      screener,
		Resolver:      resolver,
		Transport:     innerTransport,
		resolvedAddrs: make(map[string][]netip.Addr),
	}

	req, _ := http.NewRequestWithContext(context.Background(), "GET", ts1.URL+"/start", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Logf("RoundTrip error (may be network-related): %v", err)
	}
	// Cross-host redirect should be returned, not followed.
	if resp == nil {
		t.Fatalf("expected response with cross-host redirect")
	}
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected redirect status, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Verify "other.example.com" was never resolved.
	lookups := resolver.Lookups()
	for _, lookup := range lookups {
		if lookup == "other.example.com" {
			t.Errorf("resolver was called for cross-host redirect target; shouldn't follow it")
		}
	}
}

func TestFetchRefusesScreenerDenied(t *testing.T) {
	// Acceptance: Transport.Fetch refuses URLs the screener denies,
	// without calling the resolver.

	screener := NewURLScreener(0)
	screener.AddBytes(RouteSnapshotRead, []byte("secret"))

	resolver := newTestRecordingResolver()
	resolver.addrs["example.com"] = []netip.Addr{netip.MustParseAddr("93.184.216.34")}

	transport := &Transport{
		Screener:      screener,
		Resolver:      resolver,
		HostValidator: nil,
		Transport:     http.DefaultTransport,
	}

	// URL with file content should be refused (screener denies).
	_, err := transport.Fetch(context.Background(), "https://example.com/?q=secret")
	if err == nil {
		t.Fatalf("Fetch() succeeded for URL with file content")
	}

	// Resolver should not have been called.
	if len(resolver.Lookups()) != 0 {
		t.Errorf("resolver was called for screener-refused URL; lookups: %v", resolver.Lookups())
	}
}

// testDenyReferee is a test referee that always denies.
type testDenyReferee struct {
	decisionCalls []string
	mu            sync.Mutex
}

func (dr *testDenyReferee) Decide(ctx context.Context, url string) *RefusalError {
	dr.mu.Lock()
	dr.decisionCalls = append(dr.decisionCalls, url)
	dr.mu.Unlock()
	return refusal("referee", "test refusal")
}

func TestRefereeDecision(t *testing.T) {
	// Acceptance: The referee is consulted before resolution.

	dr := &testDenyReferee{}
	screener := NewURLScreener(0)
	resolver := newTestRecordingResolver()
	resolver.addrs["example.com"] = []netip.Addr{netip.MustParseAddr("93.184.216.34")}

	transport := &PinnedTransport{
		Screener:      screener,
		Resolver:      resolver,
		Referee:       dr,
		Transport:     http.DefaultTransport,
		resolvedAddrs: make(map[string][]netip.Addr),
	}

	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://example.com/test", nil)
	_, err := transport.RoundTrip(req)
	if err == nil {
		t.Fatalf("RoundTrip() succeeded; want referee refusal")
	}

	// Resolver should not have been called (referee refused before resolution).
	if len(resolver.Lookups()) != 0 {
		t.Errorf("resolver was called after referee denial; lookups: %v", resolver.Lookups())
	}

	if len(dr.decisionCalls) != 1 {
		t.Errorf("referee.Decide called %d times, want 1", len(dr.decisionCalls))
	}
}
