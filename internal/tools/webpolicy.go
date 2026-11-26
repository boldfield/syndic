package tools

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// HostPolicy checks whether a hostname matches a set of allowlist entries.
// All matching is performed without DNS resolution, only on the string forms.
// A nil or empty allowlist returns no policy (all hosts allowed).
// IP literals are unconditionally refused when an allowlist is set.
type HostPolicy struct {
	entries []hostEntry
}

type hostEntry struct {
	pattern string
	kind    hostKind
}

type hostKind int

const (
	kindExact            hostKind = iota
	kindWildSubdomain             // *.example.com - matches subdomains, not apex
	kindWildAnySubdomain          // **.example.com - matches apex and subdomains
)

// NewHostPolicy creates a policy from a list of allowlist entries.
// Each entry can be an exact hostname, a subdomain pattern (*.example.com),
// or a wildcard pattern that includes the apex (**.example.com).
// Entries are canonicalized (lowercase, punycode).
// IP literals are unconditionally refused whenever an allowlist is set.
func NewHostPolicy(entries []string) *HostPolicy {
	if len(entries) == 0 {
		return nil
	}
	p := &HostPolicy{}
	for _, e := range entries {
		if entry, ok := parseEntry(e); ok {
			p.entries = append(p.entries, entry)
		}
	}
	if len(p.entries) == 0 {
		return nil
	}
	return p
}

// parseEntry parses and canonicalizes an allowlist entry.
// It returns the parsed entry and a boolean indicating success.
func parseEntry(s string) (hostEntry, bool) {
	switch {
	case strings.HasPrefix(s, "**."):
		baseDomain := s[3:]
		canonical, ok := canonicalizeEntry(baseDomain)
		if !ok {
			return hostEntry{}, false
		}
		return hostEntry{
			pattern: canonical,
			kind:    kindWildAnySubdomain,
		}, true
	case strings.HasPrefix(s, "*."):
		baseDomain := s[2:]
		canonical, ok := canonicalizeEntry(baseDomain)
		if !ok {
			return hostEntry{}, false
		}
		return hostEntry{
			pattern: canonical,
			kind:    kindWildSubdomain,
		}, true
	default:
		canonical, ok := canonicalizeEntry(s)
		if !ok {
			return hostEntry{}, false
		}
		return hostEntry{
			pattern: canonical,
			kind:    kindExact,
		}, true
	}
}

// canonicalizeEntry canonicalizes a hostname using IDNA punycode and lowercase.
func canonicalizeEntry(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	// Apply IDNA to punycode IDNs
	a, err := hostProfile.ToASCII(s)
	if err != nil {
		// Fall back to lowercase if IDNA fails
		a = strings.ToLower(s)
	}
	// Strip trailing dot after IDNA processing
	return strings.TrimSuffix(strings.ToLower(a), "."), true
}

// Match reports whether host matches any entry in the policy.
// The host should be in canonical form: lowercase, punycode, default port stripped.
// IP literals are unconditionally refused when a policy is set.
func (p *HostPolicy) Match(host string) bool {
	if p == nil {
		return true // no policy means all allowed
	}

	// IP literals are always refused when an allowlist is set
	if isIPLiteral(host) {
		return false
	}

	for _, e := range p.entries {
		if matchesEntry(host, e) {
			return true
		}
	}
	return false
}

// MatchURL validates an HTTP(S) URL against the policy, returning an error if refused.
// It checks the scheme, canonicalizes the hostname, and matches against entries.
// IP literals are unconditionally refused. No name resolution occurs.
func (p *HostPolicy) MatchURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL does not parse: %w", err)
	}

	// Check HTTP(S) scheme first
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https, not %q", u.Scheme)
	}

	// Extract and canonicalize hostname
	host, ok := canonicalHost(u)
	if !ok {
		return fmt.Errorf("hostname is not valid")
	}

	// IP literals are always refused when matching a URL
	if isIPLiteral(host) {
		return fmt.Errorf("IP literals are refused when an allowlist is set")
	}

	// Check if hostname matches allowlist (or if no policy, allow all)
	if !p.Match(host) {
		return fmt.Errorf("hostname %q is not in the allowlist", host)
	}

	// Check for credentials or cookies - these are present if User is not nil
	if u.User != nil {
		return fmt.Errorf("URL must not contain credentials (userinfo)")
	}

	return nil
}

func isIPLiteral(host string) bool {
	// Check if it's an IPv6 literal in brackets
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return true
	}
	// Try to parse as an IPv4 or IPv6 address
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	// Check if it looks like an IPv4 address (without full parsing)
	if net.ParseIP(host) != nil {
		return true
	}
	return false
}

func matchesEntry(host string, e hostEntry) bool {
	switch e.kind {
	case kindExact:
		return host == e.pattern
	case kindWildSubdomain:
		// *.example.com matches foo.example.com but not example.com
		if host == e.pattern {
			return false // doesn't match apex
		}
		if strings.HasSuffix(host, "."+e.pattern) {
			return true
		}
		return false
	case kindWildAnySubdomain:
		// **.example.com matches example.com and foo.example.com
		if host == e.pattern {
			return true
		}
		if strings.HasSuffix(host, "."+e.pattern) {
			return true
		}
		return false
	}
	return false
}
