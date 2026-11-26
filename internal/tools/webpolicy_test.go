package tools

import (
	"testing"
)

func TestHostPolicyNilAllowsAll(t *testing.T) {
	p := NewHostPolicy([]string{}, false)
	if p != nil {
		t.Fatalf("empty allowlist should return nil policy")
	}
	p = (*HostPolicy)(nil)
	if !p.Match("example.com") {
		t.Fatalf("nil policy should allow all hosts")
	}
	if !p.Match("192.0.2.1") {
		t.Fatalf("nil policy should allow all IPs")
	}
}

func TestExactHostMatching(t *testing.T) {
	p := NewHostPolicy([]string{"example.com", "other.org"}, false)

	tests := map[string]bool{
		"example.com":     true,
		"other.org":       true,
		"sub.example.com": false,
		"notexample.com":  false,
		"Example.com":     true, // case-insensitive
		"EXAMPLE.COM":     true, // case-insensitive
	}

	for host, want := range tests {
		t.Run(host, func(t *testing.T) {
			if got := p.Match(host); got != want {
				t.Fatalf("Match(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestWildcardSubdomainPattern(t *testing.T) {
	p := NewHostPolicy([]string{"*.example.com"}, false)

	tests := map[string]bool{
		"example.com":          false, // * excludes apex
		"sub.example.com":      true,
		"foo.example.com":      true,
		"deep.sub.example.com": true, // deeper subdomains match
		"example.org":          false,
		"notexample.com":       false,
		"sub.example.org":      false,
	}

	for host, want := range tests {
		t.Run(host, func(t *testing.T) {
			if got := p.Match(host); got != want {
				t.Fatalf("Match(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestWildcardAnyPattern(t *testing.T) {
	p := NewHostPolicy([]string{"**.example.com"}, false)

	tests := map[string]bool{
		"example.com":          true, // ** includes apex
		"sub.example.com":      true,
		"foo.example.com":      true,
		"deep.sub.example.com": true,
		"example.org":          false,
		"notexample.com":       false,
		"sub.example.org":      false,
	}

	for host, want := range tests {
		t.Run(host, func(t *testing.T) {
			if got := p.Match(host); got != want {
				t.Fatalf("Match(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestIPLiteralRefusal(t *testing.T) {
	t.Run("allowed_without_refuseIP", func(t *testing.T) {
		// When refuseIP is false, IPs can match if explicitly listed
		p := NewHostPolicy([]string{"192.0.2.1", "example.com"}, false)
		if !p.Match("192.0.2.1") {
			t.Fatalf("explicitly listed IP should be allowed when refuseIP=false")
		}
		if !p.Match("192.0.2.1") {
			t.Fatalf("IPv4 literal should match exact entry")
		}
	})

	t.Run("refused_with_refuseIP", func(t *testing.T) {
		p := NewHostPolicy([]string{"192.0.2.1", "example.com"}, true)
		if p.Match("192.0.2.1") {
			t.Fatalf("IP literal should be refused when refuseIP=true")
		}
		if p.Match("2001:db8::1") {
			t.Fatalf("IPv6 literal should be refused when refuseIP=true")
		}
		// But named hosts should still match
		if !p.Match("example.com") {
			t.Fatalf("named host should still be allowed when refuseIP=true")
		}
	})

	t.Run("IPv6_literal", func(t *testing.T) {
		p := NewHostPolicy([]string{"example.com"}, true)
		if p.Match("::1") {
			t.Fatalf("IPv6 literal should be refused with refuseIP=true")
		}
	})
}

func TestNoSuffixMatching(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"}, false)

	tests := map[string]bool{
		"example.com":    true,
		"notexample.com": false, // suffix match should NOT work
		"xexample.com":   false,
	}

	for host, want := range tests {
		t.Run(host, func(t *testing.T) {
			if got := p.Match(host); got != want {
				t.Fatalf("Match(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestMultiplePatternsMatching(t *testing.T) {
	p := NewHostPolicy([]string{
		"exact.com",
		"*.wildcard.org",
		"**.both.net",
	}, false)

	tests := map[string]bool{
		"exact.com":        true,
		"wildcard.org":     false,
		"sub.wildcard.org": true,
		"both.net":         true,
		"sub.both.net":     true,
		"other.com":        false,
	}

	for host, want := range tests {
		t.Run(host, func(t *testing.T) {
			if got := p.Match(host); got != want {
				t.Fatalf("Match(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestCaseInsensitivity(t *testing.T) {
	p := NewHostPolicy([]string{"EXAMPLE.COM", "*.WILDCARD.ORG"}, false)

	tests := []string{
		"example.com",
		"Example.Com",
		"EXAMPLE.COM",
		"sub.wildcard.org",
		"Sub.Wildcard.Org",
		"SUB.WILDCARD.ORG",
	}

	for _, host := range tests {
		if !p.Match(host) {
			t.Fatalf("Match(%q) = false, want true (case-insensitive)", host)
		}
	}
}

func TestPolicyWithSingleEntry(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"}, false)

	if p == nil {
		t.Fatalf("non-empty allowlist should create a policy")
	}

	if !p.Match("example.com") {
		t.Fatalf("exact entry should match")
	}

	if p.Match("other.com") {
		t.Fatalf("non-matching entry should not match")
	}
}

func TestWildcardDeepSubdomains(t *testing.T) {
	p := NewHostPolicy([]string{"*.example.com"}, false)

	tests := map[string]bool{
		"example.com":         false,
		"a.example.com":       true,
		"a.b.example.com":     true,
		"a.b.c.d.example.com": true,
	}

	for host, want := range tests {
		t.Run(host, func(t *testing.T) {
			if got := p.Match(host); got != want {
				t.Fatalf("Match(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestNilPolicyMatchesEverything(t *testing.T) {
	var p *HostPolicy

	tests := []string{
		"example.com",
		"sub.example.com",
		"192.0.2.1",
		"::1",
		"[2001:db8::1]",
		"anything",
	}

	for _, host := range tests {
		if !p.Match(host) {
			t.Fatalf("nil policy should match everything, got false for %q", host)
		}
	}
}

func TestIPv4Literals(t *testing.T) {
	t.Run("refused_with_allowlist", func(t *testing.T) {
		p := NewHostPolicy([]string{"example.com"}, true)
		if p.Match("192.0.2.1") {
			t.Fatalf("IPv4 should be refused with allowlist and refuseIP=true")
		}
		if p.Match("10.0.0.1") {
			t.Fatalf("any IPv4 should be refused")
		}
	})

	t.Run("allowed_without_refuseIP", func(t *testing.T) {
		p := NewHostPolicy([]string{"example.com"}, false)
		// IPv4 not matching any entry should still not match
		if p.Match("192.0.2.1") {
			t.Fatalf("unlisted IPv4 should not match")
		}
	})

	t.Run("exact_match_without_refuseIP", func(t *testing.T) {
		p := NewHostPolicy([]string{"192.0.2.1"}, false)
		if !p.Match("192.0.2.1") {
			t.Fatalf("exact IP should match")
		}
		if p.Match("192.0.2.2") {
			t.Fatalf("different IP should not match")
		}
	})
}

func TestIPv6Literals(t *testing.T) {
	t.Run("refused_with_allowlist", func(t *testing.T) {
		p := NewHostPolicy([]string{"example.com"}, true)
		if p.Match("2001:db8::1") {
			t.Fatalf("IPv6 should be refused with allowlist and refuseIP=true")
		}
		if p.Match("::1") {
			t.Fatalf("any IPv6 should be refused")
		}
	})

	t.Run("in_brackets", func(t *testing.T) {
		p := NewHostPolicy([]string{"example.com"}, true)
		if p.Match("[2001:db8::1]") {
			t.Fatalf("IPv6 in brackets should be refused with refuseIP=true")
		}
	})
}

func TestEmptyPatternIsNoop(t *testing.T) {
	// Empty pattern should be treated as no policy
	p := NewHostPolicy([]string{}, false)
	if p != nil {
		t.Fatalf("empty allowlist should return nil")
	}
}

func TestMixedWildcardAndExactMatches(t *testing.T) {
	p := NewHostPolicy([]string{
		"cdn.example.com",
		"*.api.example.com",
		"**.internal.example.com",
	}, true)

	tests := map[string]bool{
		"cdn.example.com":              true,
		"other.example.com":            false,
		"api.example.com":              false, // * doesn't match apex
		"v1.api.example.com":           true,
		"v2.api.example.com":           true,
		"deep.v1.api.example.com":      true,
		"internal.example.com":         true, // ** matches apex
		"prod.internal.example.com":    true,
		"db.prod.internal.example.com": true,
		"192.0.2.1":                    false, // IP refused
	}

	for host, want := range tests {
		t.Run(host, func(t *testing.T) {
			if got := p.Match(host); got != want {
				t.Fatalf("Match(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestCanonicalFormAssumptions(t *testing.T) {
	// Test that the policy works correctly when given canonical form input
	// (lowercase, punycode would be pre-processed, default port stripped)
	p := NewHostPolicy([]string{
		"example.com",
		"*.api.example.com",
	}, false)

	// These should all work since they're already in canonical form
	if !p.Match("example.com") {
		t.Fatalf("canonical exact host should match")
	}
	if !p.Match("v1.api.example.com") {
		t.Fatalf("canonical wildcard host should match")
	}
}
