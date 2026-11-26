package tools

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

const canaryText = "sk-live-ABCDEFGH" // exactly 16 bytes

func b64u(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
func hx(s string) string   { return hex.EncodeToString([]byte(s)) }
func pct(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		fmt.Fprintf(&b, "%%%02X", s[i])
	}
	return b.String()
}

func screenerWith(t *testing.T, content string) *URLScreener {
	t.Helper()
	s := NewURLScreener(0)
	s.AddBytes(RouteSnapshotRead, []byte(content))
	return s
}

func wantRule(t *testing.T, v Verdict, rule string) {
	t.Helper()
	if v.Allowed || v.Rule != rule {
		t.Fatalf("verdict = %+v, want refused by %q", v, rule)
	}
	if strings.Contains(v.Reason, "sk-live") {
		t.Fatalf("reason leaks matched bytes: %q", v.Reason)
	}
}

func wantAllowed(t *testing.T, v Verdict) {
	t.Helper()
	if !v.Allowed {
		t.Fatalf("verdict = %+v, want allowed", v)
	}
}

// encodings maps each name to an encoder of the 16-byte canary.
var encodings = map[string]func(string) string{
	"percent":    pct,
	"base64url":  b64u,
	"hex":        hx,
	"b64ofhex":   func(s string) string { return b64u(hx(s)) },
	"hexofb64":   func(s string) string { return hx(b64u(s)) },
	"b64ofb64":   func(s string) string { return b64u(b64u(s)) },
	"b64urlpad":  func(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) },
	"b64stdpad":  func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) },
	"b64stdraw":  func(s string) string { return base64.RawStdEncoding.EncodeToString([]byte(s)) },
	"b64urlraw":  b64u,
	"plain-text": func(s string) string { return s },
}

func TestCanaryEncodingsInEveryLocation(t *testing.T) {
	for name, enc := range encodings {
		e := enc(canaryText)
		urls := map[string]string{
			"query": "https://example.com/p?q=" + url_escapeLoose(e),
			"path":  "https://example.com/a/" + url_escapeLoose(e) + "/b",
			"host":  "https://" + e + ".example.com/",
		}
		for loc, u := range urls {
			if loc == "host" && (name == "percent" || strings.ContainsAny(e, "+/=")) {
				continue // not representable in a hostname label
			}
			t.Run(name+"/"+loc, func(t *testing.T) {
				wantRule(t, screenerWith(t, canaryText).Screen(u), RuleCanary)
			})
		}
	}
}

// url_escapeLoose escapes only characters that would change URL structure.
func url_escapeLoose(s string) string {
	r := strings.NewReplacer("/", "%2F", "?", "%3F", "#", "%23", "&", "%26", " ", "%20")
	return r.Replace(s)
}

func TestCanaryFifteenBytesAllowed(t *testing.T) {
	s := screenerWith(t, canaryText)
	c := canaryText[:15]
	wantAllowed(t, s.Screen("https://example.com/p?q="+c))
	wantAllowed(t, s.Screen("https://example.com/"+c))
	wantAllowed(t, s.Screen("https://"+c+".example.com/"))
	wantAllowed(t, s.Screen("https://example.com/p?q="+b64u(c)))
	wantRule(t, s.Screen("https://example.com/p?q="+canaryText), RuleCanary)
	wantRule(t, s.Screen("https://example.com/"+canaryText), RuleCanary)
	wantRule(t, s.Screen("https://"+canaryText+".example.com/"), RuleCanary)
}

func TestCanaryRunAtOffsetInsideDecodedValue(t *testing.T) {
	s := screenerWith(t, "zzzzzzzz"+canaryText+"yyyy")
	v := "XX-" + canaryText + "-YY"
	wantRule(t, s.Screen("https://example.com/?q="+b64u(v)), RuleCanary)
	wantRule(t, s.Screen("https://example.com/?q="+hx(v)), RuleCanary)
	wantRule(t, s.Screen("https://example.com/?q="+pct(v)), RuleCanary)
}

func TestCanaryThirdLevelNotDecoded(t *testing.T) {
	s := screenerWith(t, canaryText)
	wantAllowed(t, s.Screen("https://example.com/?q="+b64u(b64u(b64u(canaryText)))))
	wantAllowed(t, s.Screen("https://example.com/?q="+hx(hx(hx(canaryText)))))
	wantAllowed(t, s.Screen("https://example.com/?q="+b64u(hx(b64u(canaryText)))))
	// Percent-decoding is not a level.
	wantRule(t, s.Screen("https://example.com/?q="+pct(b64u(b64u(canaryText)))), RuleCanary)
}

func TestCanaryAcrossComponentsAndKeysAndUserinfo(t *testing.T) {
	s := screenerWith(t, "docs/design/v5-delta-secret")
	wantRule(t, s.Screen("https://example.com/docs/design/v5-delta"), RuleCanary)
	wantRule(t, s.Screen("https://example.com/?docs/design/v5-delta=1"), RuleCanary)
	wantRule(t, s.Screen("https://docs%2Fdesign%2Fv5-delta@example.com/"), RuleCanary)
	wantRule(t, s.Screen("https://docs:design%2Fv5-delta-secret@example.com/"), RuleCanary)
	s2 := screenerWith(t, canaryText)
	wantRule(t, s2.Screen("https://example.com/?"+canaryText+"=1"), RuleCanary)
	wantRule(t, s2.Screen("https://"+canaryText+"@example.com/"), RuleCanary)
	wantRule(t, s2.Screen("https://"+b64u(canaryText)+"@example.com/"), RuleCanary)
}

func TestCanaryStandardBase64PlusAndEscapedSlash(t *testing.T) {
	raw := strings.Repeat("\xfb\xff\xfe", 6)[:16]
	s := screenerWith(t, raw)
	e := base64.StdEncoding.EncodeToString([]byte(raw))
	if !strings.Contains(e, "+") || !strings.Contains(e, "/") {
		t.Fatalf("fixture %q needs + and /", e)
	}
	wantRule(t, s.Screen("https://example.com/p?q="+e), RuleCanary)
	esc := strings.ReplaceAll(e, "/", "%2F")
	wantRule(t, s.Screen("https://example.com/p?q="+esc), RuleCanary)
	wantRule(t, s.Screen("https://example.com/"+esc), RuleCanary)
	wantRule(t, s.Screen("https://example.com/?"+esc+"=1"), RuleCanary)
}

func TestCanaryInvalidPercentEscapesDoNotDisableDecoding(t *testing.T) {
	s := screenerWith(t, canaryText)
	wantRule(t, s.Screen("https://example.com/?q=%zz"+pct(canaryText)), RuleCanary)
	wantRule(t, s.Screen("https://example.com/?q="+pct(canaryText)+"%"), RuleCanary)
	wantRule(t, s.Screen("https://example.com/?q="+pct(canaryText)+"%4"), RuleCanary)
}

func TestCanaryPlusLiteralAndFormSpace(t *testing.T) {
	s := screenerWith(t, "ab+cdef/ghijklmn")
	wantRule(t, s.Screen("https://example.com/ab+cdef/ghijklmn"), RuleCanary)
	wantRule(t, s.Screen("https://example.com/?ab+cdef/ghijklmn=1"), RuleCanary)
	s = screenerWith(t, "abcd+efghijklmno")
	wantRule(t, s.Screen("https://example.com/?abcd+efghijklmno=1"), RuleCanary)
	wantRule(t, s.Screen("https://example.com/?q=abcd+efghijklmno"), RuleCanary)

	s = screenerWith(t, "my secret key 12")
	wantRule(t, s.Screen("https://example.com/p?q=my+secret+key+12"), RuleCanary)
	wantRule(t, s.Screen("https://example.com/p?my+secret+key+12=1"), RuleCanary)
	wantRule(t, s.Screen("https://example.com/p?q=my%20secret%20key%2012"), RuleCanary)
}

func TestCanaryRoutes(t *testing.T) {
	routes := []Route{RouteSnapshotRead, RouteStagedWrite, RouteBase, RouteDiff, RouteHistory}
	for _, r := range routes {
		s := NewURLScreener(0)
		s.AddBytes(r, []byte("prefix "+canaryText+" suffix"))
		wantRule(t, s.Screen("https://example.com/?q="+b64u(canaryText)), RuleCanary)
		wantRule(t, s.Screen("https://example.com/?q="+hx(canaryText)), RuleCanary)
	}
	// All routes feed one turn-wide corpus.
	s := NewURLScreener(0)
	for i, r := range routes {
		s.AddBytes(r, []byte(fmt.Sprintf("route-%d-content-%02d", i, i)))
	}
	for i := range routes {
		wantRule(t, s.Screen(fmt.Sprintf("https://example.com/route-%d-content-%02d", i, i)), RuleCanary)
	}
}

func TestCanaryEntriesAreSeparate(t *testing.T) {
	s := NewURLScreener(0)
	s.AddBytes(RouteSnapshotRead, []byte("abcdefgh"))
	s.AddBytes(RouteDiff, []byte("ijklmnop"))
	wantAllowed(t, s.Screen("https://example.com/abcdefghijklmnop"))
	s.AddBytes(RouteBase, []byte("abcdefghijklmnop"))
	wantRule(t, s.Screen("https://example.com/abcdefghijklmnop"), RuleCanary)
}

func TestCanaryEmptyCorpusAllows(t *testing.T) {
	wantAllowed(t, NewURLScreener(0).Screen("https://example.com/"+canaryText))
}

func TestShapeURLAndHostnameLengths(t *testing.T) {
	s := NewURLScreener(0)
	base := "https://example.com/?q="
	wantAllowed(t, s.Screen(base+strings.Repeat("a", 2048-len(base))))
	wantRule(t, s.Screen(base+strings.Repeat("a", 2049-len(base))), RuleShape)

	l63, l64 := strings.Repeat("a", 63), strings.Repeat("a", 64)
	wantAllowed(t, s.Screen("https://"+l63+".example.com/"))
	wantRule(t, s.Screen("https://"+l64+".example.com/"), RuleShape)

	// 253-byte hostname allowed, 254 refused.
	mk := func(n int) string {
		var parts []string
		for total := 0; total < n; {
			l := 60
			if n-total < l+1 {
				l = n - total
			}
			if l <= 0 {
				break
			}
			parts = append(parts, strings.Repeat("b", l))
			total += l + 1
		}
		return strings.Join(parts, ".")
	}
	h253 := mk(253)
	if len(h253) != 253 {
		t.Fatalf("fixture length %d", len(h253))
	}
	wantAllowed(t, s.Screen("https://"+h253+"/"))
	wantRule(t, s.Screen("https://"+h253+"c/"), RuleShape)
}

// entropyFixture returns a string of n bytes whose entropy is about 4.58 bits
// per byte: 24 distinct letters, near-uniform.
func entropyFixture(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte(byte('a' + i%24))
	}
	return b.String()
}

func TestShapeEntropyBoundary48And49(t *testing.T) {
	if highEntropy(entropyFixture(48)) || !highEntropy(entropyFixture(49)) {
		t.Fatal("highEntropy boundary wrong")
	}
	if highEntropy(strings.Repeat("a", 200)) {
		t.Fatal("low entropy value refused")
	}
	v48, v49 := entropyFixture(48), entropyFixture(49)
	cases := map[string]func(string) string{
		"query": func(v string) string { return "https://example.com/?q=" + v },
		"path":  func(v string) string { return "https://example.com/" + v },
		"label": func(v string) string { return "https://" + v + ".example.com/" },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			s := NewURLScreener(0)
			wantAllowed(t, s.Screen(mk(v48)))
			wantRule(t, s.Screen(mk(v49)), RuleShape)
			// Low entropy at any length under the URL cap is fine.
			wantAllowed(t, s.Screen(mk(strings.Repeat("a", 60))))
		})
	}
}

func TestCountsPerHostCaseAndPortAndIDN(t *testing.T) {
	s := NewURLScreener(100)
	hosts := []string{"example.com", "EXAMPLE.com", "Example.COM", "example.com:443", "example.com.", "example.com:8443"}
	for i := 0; i < 10; i++ {
		wantAllowed(t, s.Screen("https://"+hosts[i%len(hosts)]+"/p"))
	}
	wantRule(t, s.Screen("https://example.com/p"), RuleHostCount)
	wantRule(t, s.Screen("https://EXAMPLE.com:443/p"), RuleHostCount)
	wantAllowed(t, s.Screen("https://other.example/p"))

	s = NewURLScreener(100)
	for i := 0; i < 10; i++ {
		u := "https://bücher.example/p"
		if i%2 == 1 {
			u = "https://BÜCHER.example/p"
		}
		wantAllowed(t, s.Screen(u))
	}
	wantRule(t, s.Screen("https://xn--bcher-kva.example/p"), RuleHostCount)
}

func TestCountsAcrossHostsAndRolesUseConfiguredTurnCap(t *testing.T) {
	s := NewURLScreener(5)
	for i := 0; i < 5; i++ {
		wantAllowed(t, s.Screen(fmt.Sprintf("https://h%d.example/", i)))
	}
	wantRule(t, s.Screen("https://h9.example/"), RuleTurnCount)

	d := NewURLScreener(0)
	for i := 0; i < 20; i++ {
		wantAllowed(t, d.Screen(fmt.Sprintf("https://h%d.example/", i/2)))
	}
	wantRule(t, d.Screen("https://h99.example/"), RuleTurnCount)
}

func TestRefusedFetchesConsumeNoBudget(t *testing.T) {
	s := screenerWith(t, canaryText)
	wantAllowed(t, s.Screen("https://a.example/"))
	// Turn-cap refusals (cap 1) must not spend b.example's budget or the total.
	s2 := NewURLScreener(1)
	wantAllowed(t, s2.Screen("https://a.example/"))
	for i := 0; i < 12; i++ {
		wantRule(t, s2.Screen("https://b.example/"), RuleTurnCount)
	}
	if s2.perHost["b.example"] != 0 || s2.totalNum != 1 {
		t.Fatalf("counters moved by refusals: host=%d total=%d", s2.perHost["b.example"], s2.totalNum)
	}
	// Canary and shape refusals spend nothing either.
	for i := 0; i < 15; i++ {
		wantRule(t, s.Screen("https://a.example/"+canaryText), RuleCanary)
		wantRule(t, s.Screen("https://a.example/?q="+entropyFixture(60)), RuleShape)
	}
	if s.perHost["a.example"] != 1 || s.totalNum != 1 {
		t.Fatalf("counters moved by refusals: host=%d total=%d", s.perHost["a.example"], s.totalNum)
	}
	for i := 0; i < 9; i++ {
		wantAllowed(t, s.Screen("https://a.example/"))
	}
	// Host-cap refusals spend no turn budget either.
	wantRule(t, s.Screen("https://a.example/"), RuleHostCount)
	wantRule(t, s.Screen("https://a.example/"), RuleHostCount)
	if s.totalNum != 10 {
		t.Fatalf("total = %d, want 10", s.totalNum)
	}
}

func TestScreenUnparseableURLRefused(t *testing.T) {
	wantRule(t, NewURLScreener(0).Screen("https://exa mple.com/%"), RuleShape)
}

func TestCountsIDNARootDotAndIPLiterals(t *testing.T) {
	s := NewURLScreener(100)
	for i := 0; i < 10; i++ {
		wantAllowed(t, s.Screen("https://example.com/"))
	}
	wantRule(t, s.Screen("https://example.com。/"), RuleHostCount)

	s = NewURLScreener(100)
	wantAllowed(t, s.Screen("https://[2606:4700:4700::1111]/"))
	wantAllowed(t, s.Screen("https://192.0.2.7:8080/"))
	for i := 0; i < 9; i++ {
		wantAllowed(t, s.Screen("https://[2606:4700:4700:0:0:0:0:1111]:443/"))
	}
	wantRule(t, s.Screen("https://[2606:4700:4700::1111]/"), RuleHostCount)
}

func TestCanaryNoFalsePositivesOutsideContext(t *testing.T) {
	s := screenerWith(t, "my secret key 12")
	wantAllowed(t, s.Screen("https://example.com/my+secret+key+12"))
	wantRule(t, s.Screen("https://example.com/p?q=my+secret+key+12"), RuleCanary)
	s = screenerWith(t, "abcdefghijklmnop")
	wantAllowed(t, s.Screen("https://example.com/ABCDEFGHIJKLMNOP"))
	wantRule(t, s.Screen("https://ABCDEFGHIJKLMNOP.example.com/"), RuleCanary)
}

func TestShapeEntropyKeysBareParamsAndUserinfo(t *testing.T) {
	s := NewURLScreener(100)
	tok := b64u(string(entropyBytes(60)))
	wantRule(t, s.Screen("https://example.com/?"+tok), RuleShape)
	wantRule(t, s.Screen("https://example.com/?"+tok+"=1"), RuleShape)
	wantRule(t, s.Screen("https://"+tok+"@example.com/"), RuleShape)
	wantRule(t, s.Screen("https://u:"+tok+"@example.com/"), RuleShape)
	wantRule(t, s.Screen("https://example.com/?a=1;"+tok), RuleShape)
	wantRule(t, s.Screen("https://example.com/p;"+tok), RuleShape)
}

func TestCanarySemicolonSeparators(t *testing.T) {
	s := screenerWith(t, canaryText)
	wantRule(t, s.Screen("https://example.com/?a=1;q="+b64u(canaryText)), RuleCanary)
	wantRule(t, s.Screen("https://example.com/p;"+b64u(canaryText)), RuleCanary)
}

func entropyBytes(n int) []byte {
	b := make([]byte, n)
	x := uint32(12345)
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

func TestScreenAllowsNonSTD3Hostnames(t *testing.T) {
	for _, h := range []string{"my_service.example.com", "foo-.example.com", "ab--cd.example.com"} {
		s := NewURLScreener(20)
		if v := s.Screen("https://" + h + "/"); !v.Allowed {
			t.Errorf("%s refused: %+v", h, v)
		}
	}
	s := NewURLScreener(20)
	for i := 0; i < 10; i++ {
		if v := s.Screen("https://my_service.example.com/"); !v.Allowed {
			t.Fatalf("fetch %d refused: %+v", i, v)
		}
	}
	if v := s.Screen("https://MY_Service.Example.com/"); v.Allowed {
		t.Error("case variant should share the per-host budget")
	}
}

// Tests for SetHostPolicy integration with Screen
func TestScreenWithHostPolicyAllow(t *testing.T) {
	s := NewURLScreener(0)
	policy, err := NewHostPolicy([]string{"example.com", "*.api.example.org"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	s.SetHostPolicy(policy)

	tests := []struct {
		name  string
		url   string
		allow bool
	}{
		{
			name:  "exact match allowed",
			url:   "https://example.com/path",
			allow: true,
		},
		{
			name:  "wildcard subdomain allowed",
			url:   "https://api.api.example.org/path",
			allow: true,
		},
		{
			name:  "apex not matched by *",
			url:   "https://api.example.org/",
			allow: false,
		},
		{
			name:  "off-allowlist host refused",
			url:   "https://notallowed.com/",
			allow: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := s.Screen(tc.url)
			if tc.allow && !v.Allowed {
				t.Errorf("Screen(%q) should allow, got ruled: %s", tc.url, v.Rule)
			}
			if !tc.allow && v.Allowed {
				t.Errorf("Screen(%q) should refuse", tc.url)
			}
		})
	}
}

func TestScreenRefusesInvalidScheme(t *testing.T) {
	s := NewURLScreener(0)
	policy, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	s.SetHostPolicy(policy)

	v := s.Screen("ftp://example.com/path")
	wantRule(t, v, RuleScheme)
	if !strings.Contains(v.Reason, "http or https") {
		t.Errorf("unexpected reason: %q", v.Reason)
	}
}

func TestScreenRefusesUserinfo(t *testing.T) {
	s := NewURLScreener(0)
	policy, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	s.SetHostPolicy(policy)

	v := s.Screen("https://user:pass@example.com/path")
	wantRule(t, v, RuleShape)
	if !strings.Contains(v.Reason, "credentials") && !strings.Contains(v.Reason, "userinfo") {
		t.Errorf("unexpected reason: %q", v.Reason)
	}
}

func TestScreenRefusesIPLiteralWhenPolicySet(t *testing.T) {
	s := NewURLScreener(0)
	policy, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	s.SetHostPolicy(policy)

	tests := []string{
		"https://192.0.2.1/",
		"https://[2001:db8::1]/",
		"https://10.0.0.1/path",
	}

	for _, url := range tests {
		t.Run(url, func(t *testing.T) {
			v := s.Screen(url)
			wantRule(t, v, RuleAllowlist)
			if !strings.Contains(v.Reason, "IP") && !strings.Contains(v.Reason, "literal") {
				t.Errorf("unexpected reason: %q", v.Reason)
			}
		})
	}
}

func TestScreenRefusesOffAllowlist(t *testing.T) {
	s := NewURLScreener(0)
	policy, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	s.SetHostPolicy(policy)

	v := s.Screen("https://notallowed.org/path")
	wantRule(t, v, RuleAllowlist)
}

func TestScreenWithNilPolicy(t *testing.T) {
	s := NewURLScreener(0)
	// Don't set a policy (nil)

	// IP literals should be allowed with nil policy
	v := s.Screen("https://192.0.2.1/")
	wantAllowed(t, v)

	// Various schemes should be allowed with nil policy
	v = s.Screen("https://example.com/")
	wantAllowed(t, v)
}

func TestScreenDoesNotConsumeBudgetOnRefusal(t *testing.T) {
	s := NewURLScreener(1) // max 1 fetch per turn
	policy, err := NewHostPolicy([]string{"allowed.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	s.SetHostPolicy(policy)

	// First attempt: refused by allowlist
	v := s.Screen("https://notallowed.com/")
	if v.Allowed {
		t.Fatalf("should be refused")
	}

	// Second attempt: should be allowed (refused URLs don't consume budget)
	v = s.Screen("https://allowed.com/")
	wantAllowed(t, v)

	// Third attempt: should be refused (budget exceeded, but refusal first)
	v = s.Screen("https://allowed.com/path")
	if v.Allowed || v.Rule != RuleTurnCount {
		t.Errorf("should refuse with RuleTurnCount, got %v", v)
	}
}

func TestScreenIDNHostMatching(t *testing.T) {
	s := NewURLScreener(0)
	policy, err := NewHostPolicy([]string{"bücher.de"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	s.SetHostPolicy(policy)

	// URL with punycode should match the IDN entry
	v := s.Screen("https://xn--bcher-kva.de/")
	wantAllowed(t, v)

	// URL with unicode IDN should also work (goes through canonicalization)
	v = s.Screen("https://bücher.de/")
	wantAllowed(t, v)
}

func TestScreenWildcardWithIDN(t *testing.T) {
	s := NewURLScreener(0)
	policy, err := NewHostPolicy([]string{"**.bücher.de"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	s.SetHostPolicy(policy)

	tests := []struct {
		url   string
		allow bool
	}{
		{"https://xn--bcher-kva.de/", true},
		{"https://sub.xn--bcher-kva.de/", true},
		{"https://other.com/", false},
	}

	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			v := s.Screen(tc.url)
			if tc.allow && !v.Allowed {
				t.Errorf("should allow %q", tc.url)
			}
			if !tc.allow && v.Allowed {
				t.Errorf("should refuse %q", tc.url)
			}
		})
	}
}

func TestScreenDefaultPorts(t *testing.T) {
	s := NewURLScreener(0)
	policy, err := NewHostPolicy([]string{"example.com"})
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}
	s.SetHostPolicy(policy)

	tests := []struct {
		url   string
		allow bool
	}{
		{"https://example.com:443/", true},
		{"https://example.com/", true},
		{"http://example.com:80/", true},
		{"http://example.com/", true},
		{"https://example.com:8443/", true}, // non-default port still matches
		{"http://example.com:8080/", true},  // non-default port still matches
	}

	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			v := s.Screen(tc.url)
			if tc.allow && !v.Allowed {
				t.Errorf("should allow %q", tc.url)
			}
		})
	}
}
