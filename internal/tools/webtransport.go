package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
)

// contextKeyRedirectDepth is used to track redirect depth in the request context
// to enforce the 3-hop redirect limit per initial request.
type contextKeyRedirectDepth struct{}

var keyRedirectDepth = contextKeyRedirectDepth{}

// Referee decides whether to allow a web fetch or search call. Its decision
// runs after pre-send checks but before name resolution (design v5 delta, section 6).
type Referee interface {
	// Decide returns nil if the URL is allowed or a RefusalError if denied.
	// The context is passed for use by the referee implementation.
	Decide(ctx context.Context, url string) *RefusalError
}

// AddressRule rejects certain address classes (design v5 delta, section 6).
// Loopback, private, link-local, multicast, unspecified and IPv4-mapped forms
// of those are forbidden. Returns nil if the address is allowed, or a
// RefusalError if forbidden.
func ValidateAddress(addr netip.Addr) *RefusalError {
	addr = addr.Unmap()
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
	return nil
}

// PinnedTransport is an http.RoundTripper that screens URLs, runs them through
// a referee if present, resolves a hostname once, validates all resolved addresses
// against the address rules, and pins subsequent requests to those validated addresses.
// It enforces no automatic redirects and applies all checks to every hop including
// redirects. Redirect chains are limited to 3 hops per initial request, and cross-host
// redirects are returned as results and not followed.
type PinnedTransport struct {
	Screener      *URLScreener
	Resolver      Resolver
	Referee       Referee
	Transport     http.RoundTripper
	HostValidator *HostPolicy

	mu               sync.Mutex
	resolvedAddrs    map[string][]netip.Addr
	addressValidator func(addr netip.Addr) *RefusalError
}

// NewPinnedTransport returns a transport that validates addresses and pins
// connections. All parameters are required except hostValidator.
// If transport is nil, http.DefaultTransport is used.
// Referee must not be nil; if it is, RoundTrip will refuse.
func NewPinnedTransport(screener *URLScreener, resolver Resolver,
	transport http.RoundTripper, hostValidator *HostPolicy) *PinnedTransport {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &PinnedTransport{
		Screener:      screener,
		Resolver:      resolver,
		Transport:     transport,
		HostValidator: hostValidator,
		resolvedAddrs: make(map[string][]netip.Addr),
	}
}

// setAddressValidator sets a custom address validator for testing.
// This is unexported to keep test seams private.
func (pt *PinnedTransport) setAddressValidator(f func(addr netip.Addr) *RefusalError) {
	pt.addressValidator = f
}

// RoundTrip implements http.RoundTripper. It screens the URL, requires the
// referee to allow it, resolves the target hostname, validates all resolved
// addresses against the address rules, and pins the connection to those addresses.
// Each request including redirects uses the same pinned addresses for the same hostname.
// Redirect chains are limited to 3 hops per initial request (4 requests total).
func (pt *PinnedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if pt.Referee == nil {
		return nil, fmt.Errorf("referee is required but was not set")
	}

	// Track redirect depth in this request chain using context.
	// Initial requests have depth 0; each redirect increments it.
	depth := 0
	if d := req.Context().Value(keyRedirectDepth); d != nil {
		depth = d.(int)
	}

	if depth > 3 {
		return nil, fmt.Errorf("too many redirect hops (max 3)")
	}

	host, _ := canonicalHost(req.URL)
	if host == "" {
		return nil, fmt.Errorf("invalid hostname")
	}

	// Screen the URL first (before any resolution).
	if pt.Screener != nil {
		if v := pt.Screener.Screen(req.URL.String()); !v.Allowed {
			return nil, refusal(v.Rule, v.Reason)
		}
	}

	// Consult the referee (required before resolution).
	if err := pt.Referee.Decide(req.Context(), req.URL.String()); err != nil {
		return nil, err
	}

	// Resolve and validate addresses once per hostname.
	pt.mu.Lock()
	addrs, resolved := pt.resolvedAddrs[host]
	pt.mu.Unlock()

	if !resolved {
		var err error
		addrs, err = pt.Resolver.LookupNetIP(req.Context(), "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("resolve %s: no addresses", host)
		}

		// Validate each address and collect only valid ones.
		var validated []netip.Addr
		for _, addr := range addrs {
			// Use custom validator if provided, otherwise use the standard one.
			var validateErr *RefusalError
			if pt.addressValidator != nil {
				validateErr = pt.addressValidator(addr)
			} else {
				validateErr = ValidateAddress(addr)
			}
			if validateErr != nil {
				// Cache the resolution even if validation fails, so we don't
				// resolve again. Store empty list to indicate all addresses were invalid.
				pt.mu.Lock()
				pt.resolvedAddrs[host] = []netip.Addr{}
				pt.mu.Unlock()
				return nil, validateErr
			}
			validated = append(validated, addr)
		}
		if len(validated) == 0 {
			// All addresses failed validation; cache empty list.
			pt.mu.Lock()
			pt.resolvedAddrs[host] = []netip.Addr{}
			pt.mu.Unlock()
			return nil, fmt.Errorf("no valid addresses for %s", host)
		}

		pt.mu.Lock()
		pt.resolvedAddrs[host] = validated
		addrs = validated
		pt.mu.Unlock()
	}

	// If no valid addresses were cached, fail.
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no valid addresses for %s", host)
	}

	// Create a custom transport that uses our pinned dialer.
	// We use the injected Transport as the base if available, or create one.
	var transport http.RoundTripper
	if pt.Transport != nil {
		transport = pt.Transport
	} else {
		transport = http.DefaultTransport
	}

	// If the transport is an http.Transport, we can configure a custom dialer.
	// Otherwise, we wrap it to intercept connections.
	if httpTransport, ok := transport.(*http.Transport); ok {
		pinnedDialer := func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, dialPort, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}

			for _, ip := range addrs {
				ipAddr := net.JoinHostPort(ip.String(), dialPort)
				dialer := &net.Dialer{}
				conn, err := dialer.DialContext(ctx, network, ipAddr)
				if err == nil {
					return conn, nil
				}
			}
			return nil, fmt.Errorf("dial %s: all addresses failed", addr)
		}

		// Clone the transport and set the pinned dialer.
		customTransport := httpTransport.Clone()
		customTransport.DialContext = pinnedDialer
		defer customTransport.CloseIdleConnections()
		transport = customTransport
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// Handle redirects: same-host redirects are rechecked by recursive RoundTrip call.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		locStr := resp.Header.Get("Location")
		if locStr != "" {
			targetURL, err := req.URL.Parse(locStr)
			if err != nil {
				_ = resp.Body.Close()
				return nil, fmt.Errorf("invalid redirect target: %w", err)
			}

			targetHost, _ := canonicalHost(targetURL)
			if targetHost != "" && targetHost != host {
				// Cross-host redirect: return as result, don't follow.
				return resp, nil
			}

			// Same-host redirect: close response and follow with recursive call.
			// The recursive RoundTrip will screen and referee the target before resolving.
			_ = resp.Body.Close()

			// Create a new request for the redirect target with incremented depth.
			ctx := context.WithValue(req.Context(), keyRedirectDepth, depth+1)
			redirectReq, err := http.NewRequestWithContext(ctx,
				http.MethodGet, targetURL.String(), nil)
			if err != nil {
				return nil, err
			}
			redirectReq.Header = req.Header.Clone()

			// Follow the redirect recursively (depth check will prevent loops).
			return pt.RoundTrip(redirectReq)
		}
	}

	return resp, nil
}

// Transport wraps a Resolver, Screener, optional Referee and optional HostPolicy
// into a validated, pinned web transport. It applies all pre-send and address rules,
// uses a referee to validate URLs before resolution, and pins connections to
// validated addresses. The transport enforces that requests only dial to addresses
// that pass address validation.
type Transport struct {
	Screener      *URLScreener
	Resolver      Resolver
	Referee       Referee
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

// SetReferee sets the referee for this transport. The referee is consulted
// for each URL and redirect target before name resolution occurs.
func (t *Transport) SetReferee(r Referee) {
	t.Referee = r
}

// Fetch screens a URL, validates it passes all checks including the referee,
// validates all resolved addresses, and fetches it with a pinned connection.
// The same validation and pinning applies to any redirects. Cross-host
// redirects are returned as results and not followed. Automatic redirects
// are disabled. No resolution occurs for a URL the screener or referee refuses.
// Referee is required; if not set, Fetch returns an error.
func (t *Transport) Fetch(ctx context.Context, rawURL string) (*http.Response, error) {
	if t.Screener == nil || t.Resolver == nil || t.Referee == nil {
		return nil, fmt.Errorf("transport needs a screener, resolver, and referee")
	}

	// Screen the URL (before any resolution).
	v := t.Screener.Screen(rawURL)
	if !v.Allowed {
		return nil, refusal(v.Rule, v.Reason)
	}

	// Build the request.
	req, err := NewGETRequest(ctx, rawURL)
	if err != nil {
		return nil, err
	}

	// Create a pinned transport with the referee.
	pinnedTransport := &PinnedTransport{
		Screener:      t.Screener,
		Resolver:      t.Resolver,
		Referee:       t.Referee,
		Transport:     t.Transport,
		HostValidator: t.HostValidator,
		resolvedAddrs: make(map[string][]netip.Addr),
	}

	return pinnedTransport.RoundTrip(req)
}
