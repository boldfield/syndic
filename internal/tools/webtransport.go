package tools

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// Rules reported by the referee gate and the address rule (design v5 delta,
// section 6, "Public web egress" and "Referee").
const (
	RuleReferee = "referee"
	RuleAddress = "address"
)

// dialTimeout bounds one connection attempt to a validated address.
const dialTimeout = 10 * time.Second

// Referee answers the one allow-or-deny question the design asks of every
// fetch and every redirect target. Any error is a deny; the endpoint referee
// is one implementation and the fake adapter scripts another.
type Referee interface {
	Allow(ctx context.Context, rawURL string) (allow bool, reason string, err error)
}

// ValidateAddress applies the address rule: it refuses a loopback, link-local,
// private (RFC 1918 and unique-local), multicast or unspecified address, an
// IPv4-mapped form of one of those, and an IPv4-compatible IPv6 form.
func ValidateAddress(addr netip.Addr) *RefusalError {
	// netip's classifiers do not all unmap (IsUnspecified does not), so
	// classify the unmapped address.
	a := addr.Unmap()
	switch {
	case !a.IsValid():
		return refusal(RuleAddress, "address is not valid")
	case a.Zone() != "":
		return refusal(RuleAddress, "address carries a zone")
	case a.IsLoopback():
		return refusal(RuleAddress, "address is loopback")
	case a.IsPrivate():
		return refusal(RuleAddress, "address is private")
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast():
		return refusal(RuleAddress, "address is link-local")
	case a.IsMulticast(), a.IsInterfaceLocalMulticast():
		return refusal(RuleAddress, "address is multicast")
	case a.IsUnspecified():
		return refusal(RuleAddress, "address is unspecified")
	}
	if a.Is6() {
		b := a.As16()
		zero := true
		for _, c := range b[:12] {
			zero = zero && c == 0
		}
		if zero {
			return refusal(RuleAddress, "address is an IPv4-compatible form")
		}
	}
	return nil
}

// PinnedFetcher sends screened, refereed GET requests to validated public
// addresses. For each URL the order is fixed: scheme rule and pre-send checks,
// the referee, and only after both allow, name resolution (once), the address
// rule over every resolved address, and a connection to an address that
// passed. The request keeps its URL, so the HTTP Host header and the TLS
// server name remain the original hostname. Redirects are never followed: a
// 3xx response is returned to the caller, which sends the target through Fetch
// as a new URL.
type PinnedFetcher struct {
	screener *URLScreener
	referee  Referee
	resolver Resolver

	// Test seams; unexported so production code cannot weaken the rules.
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	tlsConfig *tls.Config
}

// NewPinnedFetcher returns a fetcher. All three components are required: a
// missing referee refuses rather than authorizing.
func NewPinnedFetcher(s *URLScreener, r Referee, res Resolver) (*PinnedFetcher, error) {
	if s == nil || r == nil || res == nil {
		return nil, errors.New("pinned fetcher needs a screener, a referee and a resolver")
	}
	return &PinnedFetcher{screener: s, referee: r, resolver: res}, nil
}

// WithNetwork sets the dialer and TLS configuration of the pinned connection,
// as SearchNetwork does for a searcher, so a test outside this package can
// carry a connection to a local listener and record the validated address it
// was asked to dial. Its Resolver is not used: the fetcher keeps the one it was
// built with. The daemon calls it with nothing.
func (f *PinnedFetcher) WithNetwork(n *SearchNetwork) *PinnedFetcher {
	if n != nil {
		f.dial, f.tlsConfig = n.Dial, n.TLSConfig
	}
	return f
}

// Fetch sends one GET. A refusal is a *RefusalError.
func (f *PinnedFetcher) Fetch(ctx context.Context, rawURL string) (*http.Response, error) {
	if v := f.screener.Screen(rawURL); !v.Allowed {
		return nil, refusal(v.Rule, v.Reason)
	}
	req, err := NewGETRequest(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	allow, _, err := f.referee.Allow(ctx, rawURL)
	if err != nil || !allow {
		return nil, refusal(RuleReferee, "referee did not allow the URL")
	}
	addrs, err := f.resolve(ctx, req)
	if err != nil {
		return nil, err
	}
	port := req.URL.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[req.URL.Scheme]
	}
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		TLSClientConfig:   f.tlsConfig,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return f.dialPinned(ctx, network, addrs, port)
		},
	}
	return tr.RoundTrip(req)
}

// resolve returns the addresses to connect to. An IP literal is not looked up.
// Every address must pass the address rule, so a mixed resolution is refused.
func (f *PinnedFetcher) resolve(ctx context.Context, req *http.Request) ([]netip.Addr, error) {
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(req.URL.Hostname()); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		host, _ := canonicalHost(req.URL)
		addrs, err = f.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
	}
	if len(addrs) == 0 {
		return nil, refusal(RuleAddress, "name did not resolve")
	}
	for _, a := range addrs {
		if r := ValidateAddress(a); r != nil {
			return nil, r
		}
	}
	return addrs, nil
}

// dialPinned connects to the first validated address that accepts.
func (f *PinnedFetcher) dialPinned(ctx context.Context, network string, addrs []netip.Addr, port string) (net.Conn, error) {
	dial := f.dial
	if dial == nil {
		d := &net.Dialer{Timeout: dialTimeout}
		dial = d.DialContext
	}
	var lastErr error
	for _, a := range addrs {
		c, err := dial(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
