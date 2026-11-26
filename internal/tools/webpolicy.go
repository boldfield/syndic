package tools

import (
	"fmt"
	"net"
	"net/http"
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
// Returns error if any entry is invalid or malformed.
func NewHostPolicy(entries []string) (*HostPolicy, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	p := &HostPolicy{}
	for _, e := range entries {
		entry, err := parseEntry(e)
		if err != nil {
			return nil, fmt.Errorf("invalid allowlist entry %q: %w", e, err)
		}
		p.entries = append(p.entries, entry)
	}
	if len(p.entries) == 0 {
		return nil, nil
	}
	return p, nil
}

// parseEntry parses and canonicalizes an allowlist entry.
// It returns the parsed entry and an error if the entry is invalid.
func parseEntry(s string) (hostEntry, error) {
	if s == "" {
		return hostEntry{}, fmt.Errorf("empty pattern")
	}

	// Check for paths - patterns must be hostnames only, no slashes
	if strings.Contains(s, "/") {
		return hostEntry{}, fmt.Errorf("pattern must be a hostname, not a path")
	}

	switch {
	case strings.HasPrefix(s, "**."):
		baseDomain := s[3:]
		if baseDomain == "" {
			return hostEntry{}, fmt.Errorf("pattern '**.' incomplete")
		}
		canonical, err := canonicalizeEntry(baseDomain)
		if err != nil {
			return hostEntry{}, fmt.Errorf("invalid base domain in wildcard pattern: %w", err)
		}
		if !strings.Contains(canonical, ".") {
			return hostEntry{}, fmt.Errorf("wildcard pattern must have at least one dot in base domain")
		}
		return hostEntry{
			pattern: canonical,
			kind:    kindWildAnySubdomain,
		}, nil
	case strings.HasPrefix(s, "*."):
		baseDomain := s[2:]
		if baseDomain == "" {
			return hostEntry{}, fmt.Errorf("pattern '*.' incomplete")
		}
		canonical, err := canonicalizeEntry(baseDomain)
		if err != nil {
			return hostEntry{}, fmt.Errorf("invalid base domain in wildcard pattern: %w", err)
		}
		if !strings.Contains(canonical, ".") {
			return hostEntry{}, fmt.Errorf("wildcard pattern must have at least one dot in base domain")
		}
		return hostEntry{
			pattern: canonical,
			kind:    kindWildSubdomain,
		}, nil
	default:
		canonical, err := canonicalizeEntry(s)
		if err != nil {
			return hostEntry{}, fmt.Errorf("cannot canonicalize entry: %w", err)
		}
		// No suffix matching allowed - check that exact entries don't start with dot
		if strings.HasPrefix(canonical, ".") {
			return hostEntry{}, fmt.Errorf("suffix matching is not allowed")
		}
		return hostEntry{
			pattern: canonical,
			kind:    kindExact,
		}, nil
	}
}

// canonicalizeEntry canonicalizes a hostname using IDNA punycode and lowercase.
func canonicalizeEntry(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty hostname")
	}
	// Apply IDNA to punycode IDNs
	a, err := hostProfile.ToASCII(s)
	if err != nil {
		// Fall back to lowercase if IDNA fails
		a = strings.ToLower(s)
	}
	// Strip trailing dot after IDNA processing
	return strings.TrimSuffix(strings.ToLower(a), "."), nil
}

// Match reports whether a canonicalized host matches any entry in the policy.
// The host must be in canonical form: lowercase, punycode, default port stripped.
// IP literals are unconditionally refused when a policy is set.
// A nil policy allows all hosts.
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
// IP literals are refused only when an allowlist is set (design v5 section 6 pre-send check 5).
// No name resolution occurs.
// A nil policy allows all hosts, including IP literals (address filtering happens in fetch execution).
func (p *HostPolicy) MatchURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL does not parse: %w", err)
	}

	// Check HTTP(S) scheme first - must check before anything else per design
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https, not %q", u.Scheme)
	}

	// Extract and canonicalize hostname
	host, ok := canonicalHost(u)
	if !ok {
		return fmt.Errorf("hostname is not valid")
	}

	// Check for credentials or cookies - these are present if User is not nil
	if u.User != nil {
		return fmt.Errorf("URL must not contain credentials (userinfo)")
	}

	// Check IP literals only when an allowlist is set (design v5 section 6)
	if p != nil && isIPLiteral(host) {
		return fmt.Errorf("IP literals are refused when an allowlist is set")
	}

	// Check if hostname matches allowlist (or if no policy, allow all)
	if p != nil && !p.Match(host) {
		return fmt.Errorf("hostname %q is not in the allowlist", host)
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

// NewHTTPRequest creates a new HTTP GET request for web_fetch with the required
// constraints: GET method, no body, no cookies, and no credentials.
// The request is suitable for use with a Jar-less client.
func NewHTTPRequest(rawURL string) (*http.Request, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}

	// Explicitly ensure no Cookie header is set
	req.Header.Del("Cookie")

	// Explicitly ensure no Authorization header is set
	req.Header.Del("Authorization")

	// No body is set (already nil from NewRequest with nil body)
	// No jar is used - ensured by caller using &http.Client{Jar: nil}

	return req, nil
}
