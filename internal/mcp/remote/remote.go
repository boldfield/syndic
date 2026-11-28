// Package remote connects to one remote MCP server. Package mcp imports no
// network package, so the transport lives here.
package remote

// This file is how the engine connects to one remote server (v9 delta,
// section 7, "Connection", "Addresses" and "No ambient authority"). Before
// every connection it validates the destination, resolves the name once,
// applies v5's address rule and connects to the address that passed. It
// follows no redirect and keeps no cookie. It adds nothing to a request: the
// caller's headers are the roster's, and no other server's credential can
// reach it because a Transport belongs to one server.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/mcp"
	"github.com/boldfield/syndic/internal/tools"
)

// remoteDialTimeout bounds one connection attempt to a validated address.
const remoteDialTimeout = 10 * time.Second

// remotePrivateRanges are the ranges address_allow may lie inside: RFC 1918
// and the unique-local range.
var remotePrivateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// Config is what the roster states for one remote server.
type Config struct {
	// URL is the server's configured url.
	URL string
	// AddressAllow are ranges the server's own host may resolve into.
	AddressAllow []string
	// AuthOrigins are origins the login may use; each has the server's own
	// exceptions and no others.
	AuthOrigins []string
	// Credential is the server's static credential; nil sends none. It is
	// attached to requests to the configured origin only.
	Credential *mcp.Credential
}

// Transport is the http.RoundTripper for one remote server. Unlike a
// default transport it resolves each destination itself, so the address that
// passed the rule is the address connected to.
type Transport struct {
	// own holds the hosts the roster names: the url's and each auth origin's.
	own   map[string]bool
	allow []netip.Prefix
	cred  *mcp.Credential

	// Test seams; unexported so production code cannot weaken the rules.
	resolver  tools.Resolver
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	tlsConfig *tls.Config
}

// NewTransport validates the roster's settings and returns the
// transport. An address_allow range outside RFC 1918 and the unique-local
// range is refused, as the load rule refuses it.
func NewTransport(cfg Config) (*Transport, error) {
	t := &Transport{own: map[string]bool{}, resolver: net.DefaultResolver, cred: cfg.Credential}
	for _, raw := range append([]string{cfg.URL}, cfg.AuthOrigins...) {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("remote server: %q is not a usable URL", raw)
		}
		t.own[strings.ToLower(u.Hostname())] = true
	}
	for _, s := range cfg.AddressAllow {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			// A bare address is a single-address range, as the roster reads it.
			a, aerr := netip.ParseAddr(s)
			if aerr != nil || a.Zone() != "" {
				return nil, fmt.Errorf("remote server: address_allow %q is not an address range", s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits())
		if !remoteInPrivate(p) {
			return nil, fmt.Errorf("remote server: address_allow %q is not inside RFC 1918 or the unique-local range", s)
		}
		t.allow = append(t.allow, p)
	}
	return t, nil
}

func remoteInPrivate(p netip.Prefix) bool {
	for _, r := range remotePrivateRanges {
		if r.Bits() <= p.Bits() && r.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// isLoopbackLiteral reports whether host is one of the three loopback
// literals the design names.
func isLoopbackLiteral(host string) bool {
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}

// RoundTrip sends one request to a validated address. It never follows a
// redirect: a 3xx response is returned as it is.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	switch req.URL.Scheme {
	case "https":
	case "http":
		if !isLoopbackLiteral(host) {
			return nil, refuseRemote("plain http is allowed only for a loopback literal host")
		}
	default:
		return nil, refuseRemote("scheme must be https")
	}
	if host == "" {
		return nil, refuseRemote("destination has no host")
	}
	if req.URL.User != nil {
		return nil, refuseRemote("destination carries credentials in its URL")
	}
	if t.cred != nil {
		req = req.Clone(req.Context())
		// Drop any credential-bearing header the caller set, then attach the
		// credential only when the destination is the configured origin.
		for k := range t.cred.HeaderFor(t.cred.Origin()) {
			req.Header.Del(k)
		}
		for k, v := range t.cred.HeaderFor(req.URL.String()) {
			req.Header[http.CanonicalHeaderKey(k)] = v
		}
	}
	addrs, err := t.resolve(req.Context(), host)
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
		TLSClientConfig:   t.tlsConfig,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return t.dialPinned(ctx, network, addrs, port)
		},
	}
	defer tr.CloseIdleConnections()
	return tr.RoundTrip(req)
}

// resolve returns the addresses to connect to, every one of which passed the
// rule, so a mixed resolution is refused.
func (t *Transport) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	own := t.own[strings.ToLower(host)]
	if own && isLoopbackLiteral(host) {
		// A configured loopback literal is connected on loopback, with no
		// lookup of the name.
		if strings.EqualFold(host, "localhost") {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr(host)}, nil
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		addrs, err = t.resolver.LookupNetIP(ctx, "ip", strings.ToLower(host))
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
	}
	if len(addrs) == 0 {
		return nil, refuseRemote("name did not resolve")
	}
	for _, a := range addrs {
		if own && t.allowed(a) {
			continue
		}
		if r := tools.ValidateAddress(a); r != nil {
			return nil, r
		}
	}
	return addrs, nil
}

// allowed reports whether a is a private address inside an address_allow
// range. Link-local, multicast, unspecified and loopback are never inside the
// private ranges, so no setting can admit them.
func (t *Transport) allowed(a netip.Addr) bool {
	a = a.Unmap()
	if a.Zone() != "" {
		return false
	}
	for _, p := range t.allow {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// dialPinned connects to the first validated address that accepts.
func (t *Transport) dialPinned(ctx context.Context, network string, addrs []netip.Addr, port string) (net.Conn, error) {
	dial := t.dial
	if dial == nil {
		d := &net.Dialer{Timeout: remoteDialTimeout}
		dial = d.DialContext
	}
	lastErr := errors.New("no address")
	for _, a := range addrs {
		c, err := dial(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func refuseRemote(reason string) error {
	return &tools.RefusalError{Rule: tools.RuleAddress, Reason: reason}
}
