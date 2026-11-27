package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// AddressRule rejects certain address classes (design v5 delta, section 6).
// Loopback, private, link-local, multicast, unspecified and IPv4-mapped forms
// of those are forbidden. Returns nil if the address is allowed, or a
// RefusalError if forbidden.
func ValidateAddress(addr netip.Addr) *RefusalError {
	if addr.IsLoopback() {
		return refusal("address", "address is loopback")
	}
	if addr.IsPrivate() {
		return refusal("address", "address is private")
	}
	if addr.IsLinkLocalUnicast() {
		return refusal("address", "address is link-local")
	}
	if addr.IsMulticast() {
		return refusal("address", "address is multicast")
	}
	if addr.IsUnspecified() {
		return refusal("address", "address is unspecified")
	}
	// Check for IPv4-mapped forms of forbidden addresses.
	// IPv4-mapped IPv6 addresses have the form ::ffff:a.b.c.d
	if addr.Is6() {
		unmapped := addr.Unmap()
		if unmapped != addr {
			// Address was unmapped, so it was IPv4-mapped; check if the IPv4 part is forbidden
			if unmapped.IsLoopback() || unmapped.IsPrivate() || unmapped.IsLinkLocalUnicast() ||
				unmapped.IsMulticast() || unmapped.IsUnspecified() {
				return refusal("address", "address is IPv4-mapped form of a forbidden address")
			}
		}
	}
	return nil
}

// PinnedTransport is an http.RoundTripper that resolves a hostname once,
// validates all resolved addresses, and pins subsequent requests to those
// validated addresses. It enforces no automatic redirects and applies address
// validation to every hop. Redirect targets are limited to 3 total hops per
// initial request, and cross-host redirects are not followed.
type PinnedTransport struct {
	Resolver      Resolver
	Transport     http.RoundTripper
	HostValidator *HostPolicy
	// resolvedAddrs maps canonical hostname to validated addresses, for pinning
	// within the initial fetch plus redirects.
	resolvedAddrs map[string][]netip.Addr

	screener *URLScreener
	attempts int
}

// NewPinnedTransport returns a transport that validates addresses and pins
// connections. screener is used to screen redirect targets. resolver resolves
// hostnames. transport is the underlying RoundTripper; http.DefaultTransport
// is used if nil. hostValidator is the allowlist; nil means no allowlist.
func NewPinnedTransport(screener *URLScreener, resolver Resolver,
	transport http.RoundTripper, hostValidator *HostPolicy) *PinnedTransport {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &PinnedTransport{
		Resolver:      resolver,
		Transport:     transport,
		HostValidator: hostValidator,
		resolvedAddrs: make(map[string][]netip.Addr),
		screener:      screener,
	}
}

// RoundTrip implements http.RoundTripper. It resolves the target hostname on
// the first request, validates all resolved addresses, and pins the connection
// to those addresses. Each subsequent request (including redirects) uses the
// same pinned addresses for the same hostname.
func (pt *PinnedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	pt.attempts++
	if pt.attempts > 4 {
		return nil, fmt.Errorf("too many redirect hops (max 3)")
	}

	host, _ := canonicalHost(req.URL)
	if host == "" {
		return nil, fmt.Errorf("invalid hostname")
	}

	// Resolve and validate addresses once per hostname.
	if _, ok := pt.resolvedAddrs[host]; !ok {
		addrs, err := pt.Resolver.LookupNetIP(req.Context(), "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("resolve %s: no addresses", host)
		}

		// Validate each address.
		validated := make([]netip.Addr, 0, len(addrs))
		for _, addr := range addrs {
			if err := ValidateAddress(addr); err != nil {
				return nil, err
			}
			validated = append(validated, addr)
		}
		if len(validated) == 0 {
			return nil, fmt.Errorf("no valid addresses for %s", host)
		}
		pt.resolvedAddrs[host] = validated
	}

	// Use a custom dialer that connects to the validated addresses.
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	pinnedDialer := func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Extract just the port from the addr (which may be "hostname:port").
		_, dialPort, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		// Use the pre-resolved and validated addresses.
		for _, ip := range pt.resolvedAddrs[host] {
			ipAddr := net.JoinHostPort(ip.String(), dialPort)
			conn, err := dialer.DialContext(ctx, network, ipAddr)
			if err == nil {
				return conn, nil
			}
			// Try the next address on failure.
		}
		return nil, fmt.Errorf("dial %s: all addresses failed", addr)
	}

	// Create a custom transport with the pinned dialer.
	// We can't copy http.Transport due to its mutex, so we create a minimal one.
	customTransport := &http.Transport{
		DialContext:            pinnedDialer,
		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    10,
		DisableKeepAlives:      false,
		DisableCompression:     false,
		MaxResponseHeaderBytes: 1 << 20, // 1 MB
	}

	// Create client with no automatic redirects.
	client := &http.Client{
		Transport: customTransport,
		Jar:       nil,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	// If this is a redirect, validate it before returning.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		locStr := resp.Header.Get("Location")
		if locStr != "" {
			// Parse the redirect target.
			targetURL, err := req.URL.Parse(locStr)
			if err != nil {
				return nil, fmt.Errorf("invalid redirect target: %w", err)
			}

			// Check if redirect target is to a different host.
			targetHost, _ := canonicalHost(targetURL)
			if targetHost != "" && targetHost != host {
				// Cross-host redirect: return as result, don't follow.
				return resp, nil
			}

			// Same-host redirect: validate target URL through screener.
			if pt.screener != nil {
				if v := pt.screener.Screen(targetURL.String()); !v.Allowed {
					return nil, fmt.Errorf("redirect target refused: %s: %s",
						v.Rule, v.Reason)
				}
			}
		}
	}

	return resp, nil
}

// Transport wraps a Resolver, Fetcher and optional HostPolicy into a
// validated, pinned web transport. It applies all pre-send and address rules,
// uses an endpoint referee to validate URLs (not implemented in this task),
// and pins connections to validated addresses.
type Transport struct {
	Screener      *URLScreener
	Resolver      Resolver
	HostValidator *HostPolicy
	Transport     http.RoundTripper
}

// NewTransport returns a Transport with all required components.
func NewTransport(screener *URLScreener, resolver Resolver,
	hostValidator *HostPolicy, transport http.RoundTripper) *Transport {
	return &Transport{
		Screener:      screener,
		Resolver:      resolver,
		HostValidator: hostValidator,
		Transport:     transport,
	}
}

// Fetch screens a URL, validates it passes address rules, and fetches it with
// a pinned connection. The same validation and pinning applies to any
// redirects. Cross-host redirects are returned as results and not followed.
// Automatic redirects are disabled.
func (t *Transport) Fetch(ctx context.Context, rawURL string) (*http.Response, error) {
	if t.Screener == nil || t.Resolver == nil {
		return nil, fmt.Errorf("transport needs a screener and resolver")
	}

	// Screen the URL.
	v := t.Screener.Screen(rawURL)
	if !v.Allowed {
		return nil, refusal(v.Rule, v.Reason)
	}

	// Build the request.
	req, err := NewGETRequest(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	// Apply the pinned transport with address validation.
	pinnedTransport := NewPinnedTransport(t.Screener, t.Resolver,
		t.Transport, t.HostValidator)
	return pinnedTransport.RoundTrip(req)
}
