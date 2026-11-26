package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/config"
)

type recordingResolver struct {
	mu    sync.Mutex
	hosts []string
}

func (r *recordingResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

type recordingTransport struct {
	reqs []*http.Request
}

func (t *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.reqs = append(t.reqs, r)
	return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
}

func mustPolicy(t *testing.T, entries ...string) *HostPolicy {
	t.Helper()
	p, err := NewHostPolicy(entries)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHostPolicyMatching(t *testing.T) {
	p := mustPolicy(t, "example.com", "*.wild.org", "**.deep.net", "**.bücher.de")
	cases := []struct {
		host string
		want bool
	}{
		{"example.com", true},
		{"EXAMPLE.com", true},
		{"example.com.", true},
		{"www.example.com", false}, // exact, not suffix
		{"badexample.com", false},
		{"wild.org", false}, // * excludes the apex
		{"a.wild.org", true},
		{"a.b.wild.org", true},
		{"notwild.org", false},
		{"deep.net", true}, // ** includes the apex
		{"a.deep.net", true},
		{"xdeep.net", false},
		{"xn--bcher-kva.de", true},
		{"www.bücher.de", true},
		{"", false},
	}
	for _, c := range cases {
		if got := p.Match(c.host); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.host, got, c.want)
		}
	}
	var nilPolicy *HostPolicy
	if !nilPolicy.Match("anything.example") {
		t.Error("nil policy must allow every host")
	}
}

func TestHostPolicyInvalidEntriesError(t *testing.T) {
	for _, e := range []string{"", "*", "**", "**.", "*.", "*.com", "a.*.com", "ex*.com", "example.com:443",
		"example.com/x", "127.0.0.1", "[::1]", "::1", "2130706433", "0x7f.1", "exa_mple..com", "a..b.com", "https://example.com"} {
		if p, err := NewHostPolicy([]string{"good.example.com", e}); err == nil {
			t.Errorf("entry %q accepted: %+v", e, p)
		}
		if p, err := NewHostPolicy([]string{e}); err == nil {
			t.Errorf("sole entry %q accepted (would widen to allow-all): %+v", e, p)
		}
	}
	if p, err := NewHostPolicy(nil); err != nil || p != nil {
		t.Errorf("empty list = %v, %v; want nil, nil", p, err)
	}
}

func TestHostPolicyFromConfig(t *testing.T) {
	if p, err := HostPolicyFromConfig(nil); p != nil || err != nil {
		t.Fatalf("nil config: %v %v", p, err)
	}
	p, err := HostPolicyFromConfig(&config.WebConfig{AllowedHosts: []string{"**.example.com"}})
	if err != nil || !p.Match("example.com") {
		t.Fatalf("config policy: %v %v", p, err)
	}
	if _, err := HostPolicyFromConfig(&config.WebConfig{AllowedHosts: []string{"*"}}); err == nil {
		t.Fatal("malformed config entry accepted")
	}
}

func TestScreenSchemeRuleRunsWithoutAllowlist(t *testing.T) {
	s := NewURLScreener(0)
	for _, u := range []string{"ftp://example.com/", "file:///etc/passwd", "javascript://x", "example.com/path", "gopher://example.com", "FTP://example.com"} {
		wantRule(t, s.Screen(u), RuleScheme)
	}
	for _, u := range []string{"http://example.com/", "HTTPS://example.com/"} {
		if v := s.Screen(u); !v.Allowed {
			t.Errorf("%s refused: %+v", u, v)
		}
	}
}

func TestScreenUserinfoAndEmptyHostRefused(t *testing.T) {
	s := NewURLScreener(0)
	wantRule(t, s.Screen("http://user:pass@example.com/"), RuleUserinfo)
	wantRule(t, s.Screen("http://user@example.com/"), RuleUserinfo)
	wantRule(t, s.Screen("http:///x"), RuleShape)
}

func TestScreenAllowlistRules(t *testing.T) {
	s := NewURLScreener(0)
	s.SetHostPolicy(mustPolicy(t, "example.com", "*.wild.org", "**.bücher.de"))
	for _, u := range []string{
		"http://example.com/", "https://EXAMPLE.com:443/x", "http://example.com:8080/", "http://example.com./",
		"http://a.wild.org/", "http://xn--bcher-kva.de/", "http://www.xn--bcher-kva.de/",
	} {
		if v := s.Screen(u); !v.Allowed {
			t.Errorf("%s refused: %+v", u, v)
		}
	}
	for _, u := range []string{
		"http://www.example.com/", "http://evil.org/", "http://wild.org/", "http://example.com.evil.org/",
		"http://93.184.216.34/", "http://127.0.0.1:80/", "http://[::1]/", "http://[::ffff:127.0.0.1]/",
		"http://[fe80::1%25eth0]/", "http://2130706433/", "http://0x7f.0.0.1/",
	} {
		wantRule(t, s.Screen(u), RuleAllowlist)
	}
}

func TestIPLiteralAllowedByAllowlistRuleOnlyWhenNoAllowlist(t *testing.T) {
	s := NewURLScreener(0)
	if v := s.Screen("http://93.184.216.34/"); !v.Allowed {
		t.Fatalf("no allowlist must not refuse an IP literal on this rule: %+v", v)
	}
	s.SetHostPolicy(mustPolicy(t, "example.com"))
	wantRule(t, s.Screen("http://93.184.216.34/"), RuleAllowlist)
	s.SetHostPolicy(nil)
	if v := s.Screen("http://93.184.216.34/"); !v.Allowed {
		t.Fatalf("removed policy still refuses: %+v", v)
	}
}

func TestRefusedByPolicyConsumesNoBudget(t *testing.T) {
	s := NewURLScreener(1)
	s.SetHostPolicy(mustPolicy(t, "example.com"))
	for i := 0; i < 5; i++ {
		wantRule(t, s.Screen("http://evil.org/"), RuleAllowlist)
		wantRule(t, s.Screen("ftp://example.com/"), RuleScheme)
	}
	if v := s.Screen("http://example.com/"); !v.Allowed {
		t.Fatalf("refusals consumed the turn budget: %+v", v)
	}
	wantRule(t, s.Screen("http://example.com/"), RuleTurnCount)
}

func TestFetchResolvesOnlyAfterAllow(t *testing.T) {
	res := &recordingResolver{}
	tr := &recordingTransport{}
	s := NewURLScreener(0)
	s.SetHostPolicy(mustPolicy(t, "example.com"))
	f := &Fetcher{Screener: s, Resolver: res, Transport: tr}

	for u, rule := range map[string]string{
		"ftp://example.com/":      RuleScheme,
		"http://evil.org/":        RuleAllowlist,
		"http://93.184.216.34/":   RuleAllowlist,
		"http://u:p@example.com/": RuleUserinfo,
		"http://www.example.com/": RuleAllowlist,
	} {
		_, err := f.Fetch(context.Background(), u)
		var re *RefusalError
		if !errors.As(err, &re) || re.Rule != rule {
			t.Errorf("%s: err = %v, want rule %s", u, err, rule)
		}
	}
	if len(res.hosts) != 0 || len(tr.reqs) != 0 {
		t.Fatalf("refused URLs reached resolver %v / transport %d times", res.hosts, len(tr.reqs))
	}

	resp, err := f.Fetch(context.Background(), "https://Example.com/x?q=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(res.hosts) != 1 || res.hosts[0] != "Example.com" || len(tr.reqs) != 1 {
		t.Fatalf("allowed URL: resolver %v, transport %d", res.hosts, len(tr.reqs))
	}
}

func TestFetchSendsPlainGET(t *testing.T) {
	var got *http.Request
	var gotBody int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotBody = r, r.ContentLength
		http.SetCookie(w, &http.Cookie{Name: "a", Value: "b"})
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	res := &recordingResolver{}
	f := &Fetcher{Screener: NewURLScreener(0), Resolver: res}
	resp, err := f.Fetch(context.Background(), srv.URL+"/start")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got.Method != http.MethodGet || gotBody != 0 {
		t.Errorf("method %s, content length %d", got.Method, gotBody)
	}
	if got.Header.Get("Authorization") != "" || got.Header.Get("Cookie") != "" {
		t.Errorf("credentials sent: %v", got.Header)
	}
	if resp.StatusCode != http.StatusFound || got.URL.Path != "/start" {
		t.Errorf("redirect was followed: status %d, path %s", resp.StatusCode, got.URL.Path)
	}
}

func TestFetchNeverSendsURLUserinfo(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	f := &Fetcher{Screener: NewURLScreener(0), Resolver: &recordingResolver{}}
	_, err := f.Fetch(context.Background(), "http://user:pass@"+srv.Listener.Addr().String()+"/")
	var re *RefusalError
	if !errors.As(err, &re) || re.Rule != RuleUserinfo || hit {
		t.Fatalf("err = %v, server hit = %v", err, hit)
	}
	if _, err := NewGETRequest(context.Background(), "http://user:pass@example.com/"); err == nil {
		t.Fatal("NewGETRequest accepted userinfo")
	}
	if _, err := NewGETRequest(context.Background(), "ftp://example.com/"); err == nil {
		t.Fatal("NewGETRequest accepted ftp")
	}
}
