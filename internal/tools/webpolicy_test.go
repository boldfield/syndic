package tools

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostPolicyNilAllowsAll(t *testing.T) {
	p, err := NewHostPolicy([]string{})
	if err != nil {
		t.Fatalf("empty allowlist should not error")
	}
	if p != nil {
		t.Fatalf("empty allowlist should return nil policy")
	}
	p = (*HostPolicy)(nil)
	if !p.Match("example.com") {
		t.Fatalf("nil policy should allow all hosts")
	}
	// IP literals are still refused at MatchURL level even with nil policy
}

func TestExactHostMatching(t *testing.T) {
	p, err := NewHostPolicy([]string{"example.com", "other.org"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"*.example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"**.example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"example.com", "192.0.2.1"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{
		"exact.com",
		"*.wildcard.org",
		"**.both.net",
	})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"example.com", "*.api.example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"*.example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	if p.Match("192.0.2.1") {
		t.Fatalf("IPv4 should be refused unconditionally")
	}
	if p.Match("10.0.0.1") {
		t.Fatalf("any IPv4 should be refused")
	}
}

func TestIPv6RefusedUnconditionally(t *testing.T) {
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
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
	p, err := NewHostPolicy([]string{
		"cdn.example.com",
		"*.api.example.com",
		"**.internal.example.com",
	})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	// URLs with credentials should be refused
	err = p.MatchURL("http://user:pass@example.com")
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
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"*.example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	// IP literals should be refused even if explicitly in allowlist
	p2, err := NewHostPolicy([]string{"192.0.2.1", "example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

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
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	// Test that pre-send checks (design v5 section 6) refuse URLs before any DNS resolution.
	// MatchURL is string-based and contains no resolver calls; refused URLs are caught at this layer.
	// Integration test with fake resolver/transport should verify complete path in web_fetch task.
	tests := []struct {
		name string
		url  string
	}{
		{
			name: "off-allowlist host - refused by allowlist check",
			url:  "http://notallowed.example.com/path",
		},
		{
			name: "IP literal v4 - refused when allowlist is set",
			url:  "http://192.0.2.1/",
		},
		{
			name: "IP literal v6 bracketed - refused when allowlist is set",
			url:  "http://[2001:db8::1]/",
		},
		{
			name: "wrong scheme - refused before allowlist check",
			url:  "ftp://example.com/",
		},
		{
			name: "url with userinfo - refused before allowlist check",
			url:  "http://user:pass@example.com/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := p.MatchURL(tc.url)
			if err == nil {
				t.Fatalf("URL %q should have been refused", tc.url)
			}
			if err.Error() == "" {
				t.Fatalf("error message should not be empty for %q", tc.url)
			}
		})
	}

	// Verify that allowed URLs pass (and would reach resolver/fetch execution)
	allowedURL := "http://example.com/path"
	if err := p.MatchURL(allowedURL); err != nil {
		t.Fatalf("MatchURL(%q) should allow valid URL: %v", allowedURL, err)
	}
}

func TestURLCanonicalIPHandling(t *testing.T) {
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	// Test that bracketed IPv6 is refused
	err = p.MatchURL("http://[::1]")
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
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	// MatchURL only checks for explicit credentials in the URL
	err = p.MatchURL("http://example.com/path")
	if err != nil {
		t.Fatalf("URL without credentials should pass: %v", err)
	}

	// But it refuses if credentials are present
	err = p.MatchURL("http://user@example.com/path")
	if err == nil {
		t.Fatalf("URL with user should be refused")
	}
}

func TestEntryValidation(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		wantErr bool
	}{
		{
			name:    "valid exact",
			entries: []string{"example.com"},
			wantErr: false,
		},
		{
			name:    "valid subdomain wildcard",
			entries: []string{"*.example.com"},
			wantErr: false,
		},
		{
			name:    "valid any subdomain wildcard",
			entries: []string{"**.example.com"},
			wantErr: false,
		},
		{
			name:    "invalid empty entry",
			entries: []string{""},
			wantErr: true,
		},
		{
			name:    "invalid incomplete wildcard",
			entries: []string{"*."},
			wantErr: true,
		},
		{
			name:    "invalid no dot in wildcard pattern",
			entries: []string{"*.example"},
			wantErr: true,
		},
		{
			name:    "invalid no dot in double wildcard pattern",
			entries: []string{"**.example"},
			wantErr: true,
		},
		{
			name:    "invalid suffix matching",
			entries: []string{".example.com"},
			wantErr: true,
		},
		{
			name:    "invalid path in pattern",
			entries: []string{"example.com/path"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewHostPolicy(tc.entries)
			if tc.wantErr && err == nil {
				t.Fatalf("NewHostPolicy(%v) should error but didn't", tc.entries)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("NewHostPolicy(%v) should not error: %v", tc.entries, err)
			}
			if !tc.wantErr && p == nil && len(tc.entries) > 0 {
				t.Fatalf("NewHostPolicy(%v) should return non-nil policy for valid entries", tc.entries)
			}
		})
	}
}

func TestRejectBareWildcard(t *testing.T) {
	tests := []string{
		"*",
		"**",
		"**.",
		"*.example",  // wildcard but no dot in base domain
		"**.example", // wildcard but no dot in base domain
	}

	for _, entry := range tests {
		t.Run(entry, func(t *testing.T) {
			p, err := NewHostPolicy([]string{entry})
			if err == nil {
				t.Fatalf("NewHostPolicy should reject %q", entry)
			}
			if p != nil {
				t.Fatalf("NewHostPolicy should return nil for rejected entry %q", entry)
			}
		})
	}
}

func TestTrailingDotHandling(t *testing.T) {
	p, err := NewHostPolicy([]string{"example.com."})
	if err != nil {
		t.Fatalf("failed to create policy with trailing dot: %v", err)
	}

	// Host without dot should match after canonicalization
	if !p.Match("example.com") {
		t.Fatalf("example.com should match trailing-dot entry")
	}

	// Also test wildcard with trailing dot
	p2, err := NewHostPolicy([]string{"*.example.com."})
	if err != nil {
		t.Fatalf("failed to create policy with trailing dot in wildcard: %v", err)
	}

	if !p2.Match("sub.example.com") {
		t.Fatalf("sub.example.com should match *.example.com. entry")
	}
}

func TestIDNHandling(t *testing.T) {
	// Test IDN domain: bücher.de becomes xn--bcher-kva.de in punycode
	p, err := NewHostPolicy([]string{"bücher.de"})
	if err != nil {
		t.Fatalf("failed to create policy with IDN: %v", err)
	}

	// Should match both the unicode and punycode forms after canonicalization
	if !p.Match("xn--bcher-kva.de") {
		t.Fatalf("xn--bcher-kva.de should match bücher.de entry")
	}

	// Also test wildcard with IDN
	p2, err := NewHostPolicy([]string{"**.bücher.de"})
	if err != nil {
		t.Fatalf("failed to create policy with IDN wildcard: %v", err)
	}

	if !p2.Match("xn--bcher-kva.de") {
		t.Fatalf("xn--bcher-kva.de should match **.bücher.de entry")
	}
	if !p2.Match("sub.xn--bcher-kva.de") {
		t.Fatalf("sub.xn--bcher-kva.de should match **.bücher.de entry")
	}
}

func TestURLWithNonDefaultPorts(t *testing.T) {
	p, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	// Non-default ports should be stripped during canonicalization
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "http default port 80",
			url:     "http://example.com:80",
			wantErr: false,
		},
		{
			name:    "https default port 443",
			url:     "https://example.com:443",
			wantErr: false,
		},
		{
			name:    "http non-default port",
			url:     "http://example.com:8080",
			wantErr: false,
		},
		{
			name:    "https non-default port",
			url:     "https://example.com:8443",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := p.MatchURL(tc.url)
			if tc.wantErr && err == nil {
				t.Fatalf("MatchURL(%q) should error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("MatchURL(%q) should not error: %v", tc.url, err)
			}
		})
	}
}

func TestNewHTTPRequest(t *testing.T) {
	req, err := NewHTTPRequest("http://example.com/path")
	if err != nil {
		t.Fatalf("NewHTTPRequest failed: %v", err)
	}

	// Verify it's a GET request
	if req.Method != "GET" {
		t.Fatalf("request method should be GET, got %q", req.Method)
	}

	// Verify no body
	if req.Body != nil {
		t.Fatalf("request body should be nil")
	}

	// Verify no Cookie header
	if req.Header.Get("Cookie") != "" {
		t.Fatalf("request should not have Cookie header")
	}

	// Verify no Authorization header
	if req.Header.Get("Authorization") != "" {
		t.Fatalf("request should not have Authorization header")
	}
}

func TestNewHTTPRequestRejectsUserinfo(t *testing.T) {
	_, err := NewHTTPRequest("http://user:pass@example.com/path")
	if err == nil {
		t.Fatalf("NewHTTPRequest should reject URL with userinfo")
	}
	if err.Error() != "URL must not contain credentials (userinfo)" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNewHTTPRequestRejectsInvalidScheme(t *testing.T) {
	_, err := NewHTTPRequest("ftp://example.com/path")
	if err == nil {
		t.Fatalf("NewHTTPRequest should reject non-http(s) scheme")
	}
}

func TestNewHTTPRequestValidatesURL(t *testing.T) {
	_, err := NewHTTPRequest("not a url at all")
	if err == nil {
		t.Fatalf("NewHTTPRequest should reject invalid URL")
	}
}

func TestNilPolicyWithMatchURL(t *testing.T) {
	var p *HostPolicy

	// IP literals should be allowed with nil policy (address filtering happens in fetch execution)
	err := p.MatchURL("http://192.0.2.1")
	if err != nil {
		t.Fatalf("IP literals should be allowed with nil policy: %v", err)
	}

	// Named hosts should work with nil policy
	err = p.MatchURL("http://example.com")
	if err != nil {
		t.Fatalf("MatchURL should work with nil policy for named hosts: %v", err)
	}
}

func TestNewHTTPRequestNoCredentialsWithHTTPTest(t *testing.T) {
	// Create a test server that records request headers
	var requestReceived *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestReceived = r
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Test 1: Normal URL should work
	req, err := NewHTTPRequest(server.URL + "/path")
	if err != nil {
		t.Fatalf("NewHTTPRequest failed: %v", err)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if requestReceived.Method != "GET" {
		t.Errorf("method should be GET, got %q", requestReceived.Method)
	}
	if requestReceived.Header.Get("Authorization") != "" {
		t.Errorf("Authorization header should not be set, got %q", requestReceived.Header.Get("Authorization"))
	}
	if requestReceived.Header.Get("Cookie") != "" {
		t.Errorf("Cookie header should not be set")
	}

	// Test 2: URL with userinfo should be rejected before the request is created
	_, err = NewHTTPRequest(server.URL + "/path?user=user&pass=pass")
	if err != nil {
		t.Fatalf("URL without userinfo in query should work")
	}

	// But URL with actual userinfo should be rejected
	_, err = NewHTTPRequest("http://user:pass@example.com/path")
	if err == nil {
		t.Fatalf("NewHTTPRequest should reject URL with userinfo")
	}
}
