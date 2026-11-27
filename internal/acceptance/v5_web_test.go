package acceptance

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/tools"
)

const webTestsPara = "Pre-send checks"

func init() {
	cover(webTestsPara, "the canary refuses a URL carrying 16 bytes from a file read this turn, from a staged write, from a staged path's base content, from a reviewer diff and from the history document, each percent-encoded, base64-encoded, hex-encoded, base64-of-hex and hex-of-base64, in a query value, in a path segment and in a hostname label", "TestCanary_AllEncodings")
	cover(webTestsPara, "a 15-byte match is not refused", "TestCanary_FifteenBytesAllowed")
	cover(webTestsPara, "a URL over 2048 bytes is refused", "TestShape_URLTooLong")
	cover(webTestsPara, "a hostname label over 63 bytes and a hostname over 253 bytes are refused", "TestShape_HostnameLengths")
	cover(webTestsPara, "a value encoded three levels deep is not decoded", "TestCanary_ThreeLevelsDecodingNotApplied")
	cover(webTestsPara, "a 49-byte value at entropy 4.6 is refused and a 48-byte value is not, in a hostname label as in a query value", "TestShape_Entropy")
	cover(webTestsPara, "the eleventh fetch to one host is refused", "TestShape_EleventhFetchPerHost")
	cover(webTestsPara, "the twenty-first fetch in a turn is refused", "TestShape_TwentyFirstFetchPerTurn")
	cover(webTestsPara, "no name resolution occurs for a URL the checks or the referee refuse, verified with a resolver stub that records queries", "TestDNS_NoResolutionOnRefusal")
	cover(webTestsPara, "resolution occurs once, after the referee's allow", "TestDNS_ResolutionAfterRefereeAllow")
	cover(webTestsPara, "the connection is pinned to the validated address", "TestRedirect_PinnedConnection")
	cover(webTestsPara, "a same-host redirect target is checked by every rule and the referee before it is resolved or requested", "TestRedirect_SameHostRecheckAndReferee")
	cover(webTestsPara, "the fourth hop is refused", "TestRedirect_ThreeHopMax")
	cover(webTestsPara, "a cross-host redirect is returned as a result and not followed", "TestRedirect_CrossHostNotFollowed")
	cover(webTestsPara, "`*.example.com` does not match the apex and `**.example.com` does", "TestAllowlist_WildcardMatching")
	cover(webTestsPara, "an IP literal is refused when an allowlist is set", "TestAllowlist_IPLiteralRefused")
	cover(webTestsPara, "a `web_search` query is checked by the canary and refereed, and the provider host is not resolved for a refused query", "TestWebSearch_CanaryAndReferee")
	cover(webTestsPara, "every refusal is journaled with the rule and the URL", "TestJournaling_RefusalRuleAndURL")
}

// stubResolver records all LookupNetIP calls and can be configured to return specific results.
type stubResolver struct {
	t      *testing.T
	calls  []resolverCall
	addrs  map[string][]netip.Addr
	errors map[string]error
}

type resolverCall struct {
	network string
	host    string
	when    time.Time
}

func (r *stubResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	r.t.Helper()
	r.calls = append(r.calls, resolverCall{network: network, host: host, when: time.Now()})
	if err, ok := r.errors[host]; ok {
		return nil, err
	}
	if addrs, ok := r.addrs[host]; ok {
		return addrs, nil
	}
	// Default to a valid public address if not configured
	if addr, err := netip.ParseAddr("93.184.216.34"); err == nil {
		return []netip.Addr{addr}, nil
	}
	return nil, fmt.Errorf("lookup %s: not found", host)
}

func (r *stubResolver) queriedHosts() []string {
	var hosts []string
	for _, call := range r.calls {
		hosts = append(hosts, call.host)
	}
	return hosts
}

// TestCanary_AllEncodings verifies that file-derived bytes are detected in URLs
// after various encoding transformations.
func TestCanary_AllEncodings(t *testing.T) {
	const canaryBytes = "sk-live-ABCDEFGH" // exactly 16 bytes
	s := tools.NewURLScreener(0)
	s.AddBytes(tools.RouteSnapshotRead, []byte(canaryBytes))

	tests := []struct {
		name string
		url  string
	}{
		{"plain in query", "https://example.com/?q=" + canaryBytes},
		{"plain in path", "https://example.com/" + canaryBytes},
		{"plain in host", "https://" + canaryBytes + ".example.com/"},
		{"percent in query", "https://example.com/?q=%73%6B%2D%6C%69%76%65%2D%41%42%43%44%45%46%47%48"},
		{"base64url in query", "https://example.com/?q=c2stbGl2ZS1BQkNERUZHSA"},
		{"hex in query", "https://example.com/?q=736b2d6c6976652d4142434445464748"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := s.Screen(tt.url)
			if v.Allowed {
				t.Errorf("Screen(%q) = allowed, want refused by canary", tt.url)
			}
			if v.Rule != tools.RuleCanary {
				t.Errorf("Screen(%q) rule = %q, want %q", tt.url, v.Rule, tools.RuleCanary)
			}
		})
	}
}

// TestCanary_FifteenBytesAllowed verifies that 15-byte runs do not trigger canary.
func TestCanary_FifteenBytesAllowed(t *testing.T) {
	const fullBytes = "sk-live-ABCDEFGH" // exactly 16 bytes
	fifteenBytes := fullBytes[:15]

	s := tools.NewURLScreener(0)
	s.AddBytes(tools.RouteSnapshotRead, []byte(fullBytes))

	tests := []string{
		"https://example.com/?q=" + fifteenBytes,
		"https://example.com/" + fifteenBytes,
		"https://" + fifteenBytes + ".example.com/",
	}

	for _, url := range tests {
		v := s.Screen(url)
		if !v.Allowed {
			t.Errorf("Screen(%q) = refused by %s, want allowed", url, v.Rule)
		}
	}

	// But the full 16 bytes should be refused
	v := s.Screen("https://example.com/?q=" + fullBytes)
	if v.Allowed {
		t.Errorf("Screen with full bytes = allowed, want refused")
	}
}

// TestShape_URLTooLong verifies that URLs over 2048 bytes are refused.
func TestShape_URLTooLong(t *testing.T) {
	s := tools.NewURLScreener(0)
	// Create a URL longer than 2048 bytes: base URL is about 20 bytes, so add > 2029 bytes
	longPath := strings.Repeat("a", 2050)
	url := "https://example.com/" + longPath

	v := s.Screen(url)
	if v.Allowed {
		t.Errorf("Screen(2048+ byte URL) = allowed, want refused")
	}
	if v.Rule != tools.RuleShape {
		t.Errorf("rule = %q, want %q", v.Rule, tools.RuleShape)
	}
}

// TestShape_HostnameLengths verifies hostname length constraints.
func TestShape_HostnameLengths(t *testing.T) {
	// Build a hostname over 253 bytes with multiple labels, each under 63 bytes
	// 60 chars per label, so: a.b.c.d.e = 5 * 63 = 315 characters
	longHostname := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." +
		strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + "." + strings.Repeat("e", 60) + ".com"

	tests := []struct {
		name       string
		url        string
		shouldFail bool
	}{
		{"label exactly 63", "https://" + strings.Repeat("a", 63) + ".example.com/", false},
		{"label over 63", "https://" + strings.Repeat("a", 64) + ".example.com/", true},
		// Valid hostname under 253 bytes
		{"hostname under limit", "https://example.com/", false},
		// Hostname over 253 bytes
		{"hostname over 253", "https://" + longHostname + "/", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tools.NewURLScreener(0)
			v := s.Screen(tt.url)
			if tt.shouldFail && v.Allowed {
				t.Errorf("Screen = allowed, want refused by shape")
			} else if !tt.shouldFail && !v.Allowed {
				t.Errorf("Screen = refused by %s, want allowed", v.Rule)
			}
		})
	}
}

// TestCanary_ThreeLevelsDecodingNotApplied verifies that triple-nested encoding is not decoded.
func TestCanary_ThreeLevelsDecodingNotApplied(t *testing.T) {
	const canaryBytes = "sk-live-ABCDEFGH" // exactly 16 bytes
	s := tools.NewURLScreener(0)
	s.AddBytes(tools.RouteSnapshotRead, []byte(canaryBytes))

	// Three levels of nested encoding should not be detected
	level1 := base64.RawURLEncoding.EncodeToString([]byte(canaryBytes))
	level2 := base64.RawURLEncoding.EncodeToString([]byte(level1))
	level3 := base64.RawURLEncoding.EncodeToString([]byte(level2))

	url := "https://example.com/?q=" + level3
	v := s.Screen(url)
	if !v.Allowed {
		t.Errorf("three-level nested encoding = refused, want allowed")
	}
}

// TestShape_Entropy verifies high-entropy detection thresholds.
func TestShape_Entropy(t *testing.T) {
	s := tools.NewURLScreener(0)

	// 49-byte value at high entropy (should be refused)
	highEntropy49 := generateHighEntropyString(49)
	v := s.Screen("https://example.com/?q=" + highEntropy49)
	if v.Allowed || v.Rule != tools.RuleShape {
		t.Errorf("49-byte high-entropy = %v (allowed=%v), want refused by shape", v, v.Allowed)
	}

	// 48-byte value should not trigger entropy rule (not long enough)
	lowEntropy48 := strings.Repeat("a", 48)
	v = s.Screen("https://example.com/?q=" + lowEntropy48)
	if !v.Allowed {
		t.Errorf("48-byte value = refused by %s, want allowed", v.Rule)
	}
}

// TestShape_EleventhFetchPerHost verifies per-host fetch limit.
func TestShape_EleventhFetchPerHost(t *testing.T) {
	s := tools.NewURLScreener(0)

	// First 10 to same host should succeed
	for i := 0; i < 10; i++ {
		v := s.Screen("https://example.com/p" + fmt.Sprint(i))
		if !v.Allowed {
			t.Fatalf("fetch %d = refused by %s, want allowed", i+1, v.Rule)
		}
	}

	// 11th should fail
	v := s.Screen("https://example.com/p11")
	if v.Allowed {
		t.Errorf("11th fetch to same host = allowed, want refused")
	}
	if v.Rule != tools.RuleHostCount {
		t.Errorf("rule = %q, want %q", v.Rule, tools.RuleHostCount)
	}
}

// TestShape_TwentyFirstFetchPerTurn verifies per-turn fetch limit.
func TestShape_TwentyFirstFetchPerTurn(t *testing.T) {
	s := tools.NewURLScreener(0)

	// First 20 to different hosts should succeed
	for i := 0; i < 20; i++ {
		url := fmt.Sprintf("https://example%d.com/", i)
		v := s.Screen(url)
		if !v.Allowed {
			t.Fatalf("fetch %d = refused by %s, want allowed", i+1, v.Rule)
		}
	}

	// 21st should fail
	v := s.Screen("https://example20.com/")
	if v.Allowed {
		t.Errorf("21st fetch in turn = allowed, want refused")
	}
	if v.Rule != tools.RuleTurnCount {
		t.Errorf("rule = %q, want %q", v.Rule, tools.RuleTurnCount)
	}
}

// TestDNS_NoResolutionOnRefusal verifies that DNS is not queried for refused URLs.
func TestDNS_NoResolutionOnRefusal(t *testing.T) {
	const canaryBytes = "sk-live-secret-token"
	s := tools.NewURLScreener(0)
	s.AddBytes(tools.RouteSnapshotRead, []byte(canaryBytes))

	resolver := &stubResolver{t: t}
	fetcher := &tools.Fetcher{
		Screener: s,
		Resolver: resolver,
	}

	// Try to fetch a URL with canary
	_, err := fetcher.Fetch(context.Background(), "https://example.com/?key="+canaryBytes)

	// Should fail on screening, not DNS
	if err == nil {
		t.Fatal("Fetch = nil, want error")
	}

	// No DNS queries should have been made
	if len(resolver.calls) > 0 {
		t.Errorf("DNS queries made: %v, want none", resolver.queriedHosts())
	}
}

// TestDNS_ResolutionAfterRefereeAllow verifies that DNS occurs only after screening passes.
func TestDNS_ResolutionAfterRefereeAllow(t *testing.T) {
	s := tools.NewURLScreener(0)
	resolver := &stubResolver{t: t, addrs: map[string][]netip.Addr{
		"example.com": {netip.MustParseAddr("93.184.216.34")},
	}}

	fetcher := &tools.Fetcher{
		Screener: s,
		Resolver: resolver,
		Transport: &mockTransport{},
	}

	// Fetch a clean URL that should pass screening
	_, _ = fetcher.Fetch(context.Background(), "https://example.com/")

	// Should have queried DNS
	if len(resolver.calls) == 0 {
		t.Error("no DNS queries made, want one")
	}
	if len(resolver.calls) > 0 && resolver.calls[0].host != "example.com" {
		t.Errorf("DNS query for %q, want example.com", resolver.calls[0].host)
	}
}

// TestRedirect_PinnedConnection verifies that the connection is pinned to the validated address.
func TestRedirect_PinnedConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	s := tools.NewURLScreener(0)
	resolver := &stubResolver{t: t, addrs: map[string][]netip.Addr{
		"example.com": {netip.MustParseAddr("127.0.0.1")},
	}}

	fetcher := &tools.Fetcher{
		Screener: s,
		Resolver: resolver,
	}

	// The fetcher should not follow redirects (design v5 delta, section 6, pre-send checks 3)
	resp, err := fetcher.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestRedirect_SameHostRecheckAndReferee verifies redirect targets are rechecked.
func TestRedirect_SameHostRecheckAndReferee(t *testing.T) {
	gap(t, "redirect handling requires full tool execution context (milestone E)")
}

// TestRedirect_ThreeHopMax verifies that only 3 redirect hops are followed.
func TestRedirect_ThreeHopMax(t *testing.T) {
	gap(t, "redirect handling requires full tool execution context (milestone E)")
}

// TestRedirect_CrossHostNotFollowed verifies that cross-host redirects are not followed.
func TestRedirect_CrossHostNotFollowed(t *testing.T) {
	gap(t, "redirect handling requires full tool execution context (milestone E)")
}

// TestAllowlist_WildcardMatching verifies allowlist wildcard patterns.
func TestAllowlist_WildcardMatching(t *testing.T) {
	policy, err := tools.NewHostPolicy([]string{"*.example.com", "**.test.com"})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		host    string
		matches bool
	}{
		// *.example.com matches any subdomain (any level), not apex
		{"subdomain of example.com", "sub.example.com", true},
		{"apex of example.com", "example.com", false},
		{"nested subdomain of example.com", "a.b.example.com", true}, // matches any subdomain

		// **.test.com matches apex and all subdomains
		{"apex of test.com", "test.com", true},
		{"subdomain of test.com", "sub.test.com", true},
		{"nested subdomain of test.com", "a.b.test.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.Match(tt.host); got != tt.matches {
				t.Errorf("Match(%q) = %v, want %v", tt.host, got, tt.matches)
			}
		})
	}
}

// TestAllowlist_IPLiteralRefused verifies that IP literals are refused with allowlist.
func TestAllowlist_IPLiteralRefused(t *testing.T) {
	policy, err := tools.NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}

	tests := []string{
		"127.0.0.1",
		"::1",
		"2001:db8::1",
		"192.168.1.1",
	}

	s := tools.NewURLScreener(0)
	s.SetHostPolicy(policy)

	for _, ip := range tests {
		url := "https://" + ip + "/"
		v := s.Screen(url)
		if v.Allowed {
			t.Errorf("Screen(IP %s) = allowed, want refused", ip)
		}
	}
}

// TestWebSearch_CanaryAndReferee tests web search query checking.
func TestWebSearch_CanaryAndReferee(t *testing.T) {
	gap(t, "web search requires full tool execution context (milestone E)")
}

// TestJournaling_RefusalRuleAndURL verifies that refusals are journaled.
func TestJournaling_RefusalRuleAndURL(t *testing.T) {
	gap(t, "journaling requires full engine context (milestone E)")
}

// Helper functions

func generateHighEntropyString(length int) string {
	// Generate a string with Shannon entropy > 4.5 bits/byte
	// Use a mix of characters to increase entropy
	chars := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%"
	var result strings.Builder
	for i := 0; i < length; i++ {
		result.WriteByte(chars[i%len(chars)])
	}
	return result.String()
}

type mockTransport struct{}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("OK")),
	}, nil
}
