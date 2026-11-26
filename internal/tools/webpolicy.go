package tools

import (
	"net"
	"net/netip"
	"strings"
)

// HostPolicy checks whether a hostname matches a set of allowlist entries.
// All matching is performed without DNS resolution, only on the string forms.
// A nil or empty allowlist returns no policy (all hosts allowed).
type HostPolicy struct {
	entries  []hostEntry
	refuseIP bool
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
// If refuseIP is true, IP literals will be refused whenever an allowlist is set.
func NewHostPolicy(entries []string, refuseIP bool) *HostPolicy {
	if len(entries) == 0 {
		return nil
	}
	p := &HostPolicy{refuseIP: refuseIP}
	for _, e := range entries {
		p.entries = append(p.entries, parseEntry(e))
	}
	return p
}

func parseEntry(s string) hostEntry {
	switch {
	case strings.HasPrefix(s, "**."):
		return hostEntry{
			pattern: strings.ToLower(s[3:]), // strip **.
			kind:    kindWildAnySubdomain,
		}
	case strings.HasPrefix(s, "*."):
		return hostEntry{
			pattern: strings.ToLower(s[2:]), // strip *.
			kind:    kindWildSubdomain,
		}
	default:
		return hostEntry{
			pattern: strings.ToLower(s),
			kind:    kindExact,
		}
	}
}

// Match reports whether host matches any entry in the policy.
// The host should be in canonical form: lowercase, punycode, default port stripped.
// IP literals are only refused (return false) if the policy was created with refuseIP=true;
// otherwise they are matched against the allowlist entries.
func (p *HostPolicy) Match(host string) bool {
	if p == nil {
		return true // no policy means all allowed
	}

	// Check if host is an IP literal
	if isIPLiteral(host) {
		if p.refuseIP {
			return false
		}
		// IP literals can only match exact entries, not patterns
		host = strings.ToLower(host)
		for _, e := range p.entries {
			if e.kind == kindExact && e.pattern == host {
				return true
			}
		}
		return false
	}

	host = strings.ToLower(host)
	for _, e := range p.entries {
		if matchesEntry(host, e) {
			return true
		}
	}
	return false
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
