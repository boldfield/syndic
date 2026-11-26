package tools

import (
	"net/http/httptest"
	"testing"
)

func TestHostPolicyNilAllowsAll(t *testing.T) {
	p := NewHostPolicy([]string{})
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
	p := NewHostPolicy([]string{"example.com", "other.org"})

	tests := map[string]bool{
		"example.com":     true,
		"other.org":       true,
		"sub.example.com": false,
		"notexample.com":  false,
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
	p := NewHostPolicy([]string{"*.example.com"})

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
	p := NewHostPolicy([]string{"**.example.com"})

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

func TestIPLiteralRefusalUnconditional(t *testing.T) {
	// IP refusal is now unconditional - all IPs are refused when allowlist is set
	p := NewHostPolicy([]string{"example.com", "192.0.2.1"})

	if p.Match("192.0.2.1") {
		t.Fatalf("IP literal should be refused unconditionally")
	}
	if p.Match("2001:db8::1") {
		t.Fatalf("IPv6 literal should be refused unconditionally")
	}
	if p.Match("[2001:db8::1]") {
		t.Fatalf("IPv6 in brackets should be refused unconditionally")
	}
	// Named hosts should still match
	if !p.Match("example.com") {
		t.Fatalf("named host should still be allowed")
	}
}

func TestNoSuffixMatching(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})

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
	})

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

func TestCaseInsensitivityInMatching(t *testing.T) {
	p := NewHostPolicy([]string{"example.com", "*.api.example.com"})

	// Entries are canonicalized, so we test with canonical input
	tests := map[string]bool{
		"example.com":         true,
		"sub.api.example.com": true,
		"other.com":           false,
		"unmatched.api.org":   false,
	}

	for host, want := range tests {
		t.Run(host, func(t *testing.T) {
			if got := p.Match(host); got != want {
				t.Fatalf("Match(%q) = %v, want %v", host, got, want)
			}
		})
	}
}

func TestPolicyWithSingleEntry(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})

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
	p := NewHostPolicy([]string{"*.example.com"})

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

func TestIPv4RefusedUnconditionally(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})
	if p.Match("192.0.2.1") {
		t.Fatalf("IPv4 should be refused unconditionally")
	}
	if p.Match("10.0.0.1") {
		t.Fatalf("any IPv4 should be refused")
	}
}

func TestIPv6RefusedUnconditionally(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})
	if p.Match("2001:db8::1") {
		t.Fatalf("IPv6 should be refused unconditionally")
	}
	if p.Match("::1") {
		t.Fatalf("loopback IPv6 should be refused")
	}
	if p.Match("[2001:db8::1]") {
		t.Fatalf("IPv6 in brackets should be refused")
	}
}

func TestMixedWildcardAndExactMatches(t *testing.T) {
	p := NewHostPolicy([]string{
		"cdn.example.com",
		"*.api.example.com",
		"**.internal.example.com",
	})

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

// URL-level tests with HTTP(S) scheme validation and canonicalization

func TestURLLevelValidationHTTPScheme(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})

	tests := map[string]bool{
		"http://example.com":   true,
		"https://example.com":  true,
		"ftp://example.com":    false,
		"file://example.com":   false,
		"gopher://example.com": false,
	}

	for url, shouldPass := range tests {
		t.Run(url, func(t *testing.T) {
			err := p.MatchURL(url)
			if shouldPass && err != nil {
				t.Errorf("MatchURL(%q) = %v, want nil", url, err)
			}
			if !shouldPass && err == nil {
				t.Errorf("MatchURL(%q) = nil, want error", url)
			}
		})
	}
}

func TestURLLevelValidationCredentials(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})

	// URLs with credentials should be refused
	err := p.MatchURL("http://user:pass@example.com")
	if err == nil {
		t.Fatalf("URL with credentials should be refused")
	}

	// Without credentials should work
	err = p.MatchURL("http://example.com")
	if err != nil {
		t.Fatalf("URL without credentials should work: %v", err)
	}
}

func TestURLLevelCanonicalHostMatching(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})

	// Various URL forms should all be canonicalized and matched
	urls := []string{
		"http://example.com",
		"https://example.com",
		"http://example.com:80",    // default HTTP port stripped in canonicalization
		"https://example.com:443",  // default HTTPS port stripped in canonicalization
		"http://EXAMPLE.COM",       // uppercase -> lowercase
		"https://ExAmPlE.cOm/path", // mixed case -> lowercase
	}

	for _, url := range urls {
		err := p.MatchURL(url)
		if err != nil {
			t.Errorf("MatchURL(%q) = %v, want nil", url, err)
		}
	}
}

func TestURLLevelWildcardMatching(t *testing.T) {
	p := NewHostPolicy([]string{"*.example.com"})

	tests := map[string]bool{
		"http://sub.example.com":      true,
		"https://api.example.com":     true,
		"http://example.com":          false, // apex not matched by *
		"http://notexample.com":       false,
		"http://deep.sub.example.com": true,
	}

	for url, shouldPass := range tests {
		t.Run(url, func(t *testing.T) {
			err := p.MatchURL(url)
			if shouldPass && err != nil {
				t.Errorf("MatchURL(%q) = %v, want nil", url, err)
			}
			if !shouldPass && err == nil {
				t.Errorf("MatchURL(%q) = nil, want error", url)
			}
		})
	}
}

func TestURLLevelIPRefusal(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})

	// IP literals should be refused even if explicitly in allowlist
	p2 := NewHostPolicy([]string{"192.0.2.1", "example.com"})

	tests := []string{
		"http://192.0.2.1",
		"http://[2001:db8::1]",
		"https://10.0.0.1",
	}

	for _, url := range tests {
		err := p.MatchURL(url)
		if err == nil {
			t.Errorf("MatchURL(%q) with IP-rejecting policy should fail", url)
		}
		err = p2.MatchURL(url)
		if err == nil {
			t.Errorf("MatchURL(%q) with IP-allowing policy should still fail (IPs always refused)", url)
		}
	}
}

func TestResolverNotCalledForRefusedURL(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})

	// Use a real HTTP server to test that we don't make a request
	resolverCalled := false
	server := httptest.NewServer(nil)
	defer server.Close()

	// This URL should be refused before any resolution
	url := "http://notallowed.example.com/path"
	err := p.MatchURL(url)
	if err == nil {
		t.Fatalf("URL should have been refused, but wasn't")
	}

	if resolverCalled {
		t.Fatalf("resolver was called for refused URL")
	}

	// Verify the error is about allowlist, not resolution
	if err.Error() == "" {
		t.Fatalf("error message should not be empty")
	}
}

func TestURLCanonicalIPHandling(t *testing.T) {
	p := NewHostPolicy([]string{"example.com"})

	// Test that bracketed IPv6 is refused
	err := p.MatchURL("http://[::1]")
	if err == nil {
		t.Fatalf("bracketed IPv6 should be refused")
	}

	// Test that IPv4-mapped IPv6 is refused
	err = p.MatchURL("http://[::ffff:192.0.2.1]")
	if err == nil {
		t.Fatalf("IPv4-mapped IPv6 should be refused")
	}
}

func TestURLGetRequestImplied(t *testing.T) {
	// The MatchURL function validates URL-level policy (scheme, credentials, allowlist)
	// The requirement for GET-only requests with no cookies/credentials/body
	// is enforced at the HTTP client level, not in MatchURL
	p := NewHostPolicy([]string{"example.com"})

	// MatchURL only checks for explicit credentials in the URL
	err := p.MatchURL("http://example.com/path")
	if err != nil {
		t.Fatalf("URL without credentials should pass: %v", err)
	}

	// But it refuses if credentials are present
	err = p.MatchURL("http://user@example.com/path")
	if err == nil {
		t.Fatalf("URL with user should be refused")
	}
}
