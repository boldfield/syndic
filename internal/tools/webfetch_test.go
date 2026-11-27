package tools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

type fakeFetcher struct {
	client *http.Client
}

func (f *fakeFetcher) Fetch(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "WebFetch/1.0")
	return f.client.Do(req)
}

// TestWebFetchBasic verifies that a simple fetch returns the response body.
func TestWebFetchBasic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "" {
		t.Fatalf("expected no error, got %q", result.ErrorCode)
	}
	if string(result.Content) != "hello" {
		t.Fatalf("expected body %q, got %q", "hello", string(result.Content))
	}
}

// TestWebFetchSameHostRedirect verifies that redirects to the same host are followed.
func TestWebFetchSameHostRedirect(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Location", server.URL+"/redirected")
			w.WriteHeader(http.StatusSeeOther)
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("final"))
		}
	})

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "" {
		t.Fatalf("expected no error, got %q", result.ErrorCode)
	}
	if string(result.Content) != "final" {
		t.Fatalf("expected body %q, got %q", "final", string(result.Content))
	}
}

// TestWebFetchCrossHostRedirect verifies that cross-host redirects are returned as labeled data.
func TestWebFetchCrossHostRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://other.example.com/page")
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer server.Close()

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "" {
		t.Fatalf("expected no error, got %q", result.ErrorCode)
	}
	content := string(result.Content)
	if !contains(content, "redirect to a different host:") {
		t.Fatalf("expected labeled redirect, got %q", content)
	}
	if !contains(content, "https://other.example.com/page") {
		t.Fatalf("expected URL in redirect, got %q", content)
	}
}

// TestWebFetchRedirectLimit verifies that 3 same-host redirects are followed and the 4th is refused.
func TestWebFetchRedirectLimit(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Send 4 redirects, then would send 200 on the 5th (but that should not be called).
		if callCount <= 4 {
			w.Header().Set("Location", server.URL+"?hop="+string(rune('A'+callCount)))
			w.WriteHeader(http.StatusSeeOther)
		} else {
			t.Fatalf("5th call made - 4th redirect hop was not refused")
		}
	})

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "redirect_limit" {
		t.Fatalf("expected redirect_limit error at 4th hop, got %q", result.ErrorCode)
	}
	if callCount != 4 {
		t.Fatalf("expected 4 calls, got %d", callCount)
	}
}

// TestWebFetchThreeRedirectsSuccess verifies that 3 same-host redirects can be successfully followed.
func TestWebFetchThreeRedirectsSuccess(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			w.Header().Set("Location", server.URL+"/redirect1")
			w.WriteHeader(http.StatusSeeOther)
		case 2:
			w.Header().Set("Location", server.URL+"/redirect2")
			w.WriteHeader(http.StatusSeeOther)
		case 3:
			w.Header().Set("Location", server.URL+"/final")
			w.WriteHeader(http.StatusSeeOther)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success after 3 redirects"))
		}
	})

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "" {
		t.Fatalf("expected no error, got %q", result.ErrorCode)
	}
	if string(result.Content) != "success after 3 redirects" {
		t.Fatalf("expected final content, got %q", string(result.Content))
	}
	if callCount != 4 {
		t.Fatalf("expected 4 calls (3 redirects + final request), got %d", callCount)
	}
}

// TestWebFetchResultSizeLimit verifies that oversized results produce an error with size info.
func TestWebFetchResultSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write more than DefaultMaxResultBytes.
		for i := 0; i < DefaultMaxResultBytes+1000; i++ {
			_, _ = w.Write([]byte("x"))
		}
	}))
	defer server.Close()

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != ErrCodeTooLarge {
		t.Fatalf("expected %s error, got %q", ErrCodeTooLarge, result.ErrorCode)
	}
	if len(result.Content) == 0 {
		t.Fatalf("expected error message in content, got empty")
	}
	// Verify message format: should say "result exceeds" not the wrong comparison
	if string(result.Content) != "result exceeds "+string(rune(DefaultMaxResultBytes))+" bytes" {
		// Just check that it contains "exceeds" to avoid hardcoding numbers
		if !contains(string(result.Content), "exceeds") {
			t.Errorf("expected message containing 'exceeds', got %q", string(result.Content))
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i < len(s); i++ {
		if i+len(substr) <= len(s) && s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestWebFetchCustomMaxResultBytes verifies that maxResultBytes is configurable.
func TestWebFetchCustomMaxResultBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write exactly 1000 bytes.
		for i := 0; i < 1000; i++ {
			_, _ = w.Write([]byte("x"))
		}
	}))
	defer server.Close()

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 500)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != ErrCodeTooLarge {
		t.Fatalf("expected too_large error with custom max, got %q", result.ErrorCode)
	}
}

// TestWebFetchInvalidJSON verifies that invalid JSON in args produces an error.
func TestWebFetchInvalidJSON(t *testing.T) {
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	result, err := webTools.Fetch(context.Background(), "call1", []byte("not json"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != ErrCodeBadArguments {
		t.Fatalf("expected %s error, got %q", ErrCodeBadArguments, result.ErrorCode)
	}
}

// TestWebFetchEmptyURL verifies that an empty URL produces an error.
func TestWebFetchEmptyURL(t *testing.T) {
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	args, _ := json.Marshal(map[string]string{"url": ""})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != ErrCodeBadArguments {
		t.Fatalf("expected %s error, got %q", ErrCodeBadArguments, result.ErrorCode)
	}
}

// TestWebFetchNon2xxStatus verifies that non-2xx responses return an error.
func TestWebFetchNon2xxStatus(t *testing.T) {
	for _, statusCode := range []int{404, 500, 403, 400} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte("error page"))
		}))

		fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
		webTools := NewWebFetchTools(fetcher, 0)
		args, _ := json.Marshal(map[string]string{"url": server.URL})
		result, err := webTools.Fetch(context.Background(), "call1", args)
		server.Close()

		if err != nil {
			t.Fatalf("unexpected error for %d: %v", statusCode, err)
		}
		if result.ErrorCode != "http_error" {
			t.Fatalf("expected http_error for %d, got %q", statusCode, result.ErrorCode)
		}
		if !contains(string(result.Content), "HTTP") {
			t.Errorf("expected HTTP status in content for %d, got %q", statusCode, string(result.Content))
		}
	}
}

// TestWebFetchCrossHostRedirectSizeLimit verifies that cross-host redirects are size-checked.
func TestWebFetchCrossHostRedirectSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a redirect to a very long URL
		longPath := "/page?"
		for i := 0; i < 5000; i++ {
			longPath += "x"
		}
		w.Header().Set("Location", "https://other.example.com"+longPath)
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer server.Close()

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 1000) // Set small limit
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != ErrCodeTooLarge {
		t.Fatalf("expected %s error for oversized cross-host redirect, got %q", ErrCodeTooLarge, result.ErrorCode)
	}
}

// TestWebFetchCrossHostRedirectLabel verifies that cross-host redirects are labeled clearly.
func TestWebFetchCrossHostRedirectLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://other.example.com/page")
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer server.Close()

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "" {
		t.Fatalf("expected no error, got %q", result.ErrorCode)
	}
	content := string(result.Content)
	if !contains(content, "redirect to a different host:") {
		t.Fatalf("expected labeled cross-host redirect, got %q", content)
	}
	if !contains(content, "https://other.example.com/page") {
		t.Fatalf("expected URL in labeled redirect, got %q", content)
	}
}

// TestWebFetchRedirectThresholdReferee verifies that same-host redirects are
// passed through the WebFetcher, which applies screening and refereeing.
// This test uses a custom fetcher that simulates referee denial.
type denyingFetcher struct {
	callCount int
	denyURL   string
	client    *http.Client
}

func (d *denyingFetcher) Fetch(ctx context.Context, rawURL string) (*http.Response, error) {
	d.callCount++
	// Deny specific URL to simulate referee rejection
	if rawURL == d.denyURL {
		return nil, refusal(RuleReferee, "test: denied by referee")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	return d.client.Do(req)
}

func TestWebFetchSameHostRedirectRefereeRefusal(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Header().Set("Location", server.URL+"/denied")
			w.WriteHeader(http.StatusSeeOther)
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("content"))
		}
	})

	// Use a fetcher that denies the redirect target
	denying := &denyingFetcher{
		denyURL: server.URL + "/denied",
		client:  &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}

	webTools := NewWebFetchTools(denying, 0)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}

	// The redirect target should be refused
	if result.ErrorCode != RuleReferee {
		t.Fatalf("expected referee refusal for denied redirect target, got %q", result.ErrorCode)
	}

	// Verify that the fetcher was called twice (initial and redirect)
	if denying.callCount < 2 {
		t.Fatalf("expected at least 2 fetcher calls, got %d", denying.callCount)
	}
}

// TestWebFetchSameHostRedirectRealCompositionShapeRefusal tests that a same-host redirect
// to a URL violating a screener rule (shape) is refused without resolver or dial.
func TestWebFetchSameHostRedirectRealCompositionShapeRefusal(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// Redirect to a URL with a path that's too long (violates shape rule).
			longPath := strings.Repeat("a", 2100)
			w.Header().Set("Location", "http://ok.test/"+longPath)
			w.WriteHeader(http.StatusSeeOther)
		} else {
			t.Fatalf("second fetch should not be called (shape violation should refuse)")
		}
	})

	// Use real PinnedFetcher with test seams
	g := newPinRig(t, server.Listener.Addr().String(), map[string][]string{"ok.test": {"93.184.216.34"}})
	webTools := NewWebFetchTools(g.f, 0)

	args, _ := json.Marshal(map[string]string{"url": "http://ok.test/"})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}

	// Should be refused with shape rule
	if result.ErrorCode != RuleShape {
		t.Fatalf("expected shape refusal for oversized path in redirect, got %q", result.ErrorCode)
	}

	// Resolver should not have been called for the redirect target
	// (it was refused by the screener before reaching the resolver)
	if len(g.res.hosts) > 1 {
		t.Fatalf("resolver should not be called for redirected URL with shape violation, but hosts: %v", g.res.hosts)
	}

	// Dial should not have been called for the redirect target
	if len(g.dials) > 1 {
		t.Fatalf("dial should not be called for redirected URL with shape violation, but dials: %v", g.dials)
	}
}

// denierReferee denies specific URLs while allowing others.
type denierReferee struct {
	denyURL string
	calls   []string
}

func (r *denierReferee) Allow(_ context.Context, u string) (bool, string, error) {
	r.calls = append(r.calls, u)
	if u == r.denyURL {
		return false, "test: denied", nil
	}
	return true, "stub", nil
}

// TestWebFetchSameHostRedirectRealCompositionRefereeRefusal tests that a same-host redirect
// to a URL denied by the referee is refused without resolver or dial for the target.
func TestWebFetchSameHostRedirectRealCompositionRefereeRefusal(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// Redirect to same host but different path which the referee will deny
			w.Header().Set("Location", "http://ok.test/denied")
			w.WriteHeader(http.StatusSeeOther)
		} else {
			t.Fatalf("second fetch should not be called (referee refusal should stop)")
		}
	})

	// Create a custom PinnedFetcher with a referee that denies the redirect target
	screener := NewURLScreener(0)
	referee := &denierReferee{denyURL: "http://ok.test/denied"}
	resolver := &stubResolver{addrs: map[string][]string{"ok.test": {"93.184.216.34"}}}

	f, err := NewPinnedFetcher(screener, referee, resolver)
	if err != nil {
		t.Fatal(err)
	}

	// Set up the dial seam to record dials
	dialCount := 0
	f.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialCount++
		var d net.Dialer
		return d.DialContext(ctx, network, server.Listener.Addr().String())
	}

	webTools := NewWebFetchTools(f, 0)

	args, _ := json.Marshal(map[string]string{"url": "http://ok.test/"})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}

	// Should be refused with referee error
	if result.ErrorCode != RuleReferee {
		t.Fatalf("expected referee refusal for denied redirect target, got %q", result.ErrorCode)
	}

	// Verify that the dial was not called for the redirect target
	// (referee denial should stop before resolver)
	if dialCount > 1 {
		t.Fatalf("dial should not be called for redirected URL denied by referee, but got %d dials", dialCount)
	}

	// Verify that the referee was called for both initial URL and redirected target
	if len(referee.calls) < 2 {
		t.Fatalf("expected at least 2 referee calls, got %d: %v", len(referee.calls), referee.calls)
	}
}

// denierResolver returns different addresses based on call count to test address validation on redirects.
type denierResolver struct {
	hosts     []string
	callNum   int
	publicIP  string
	privateIP string
}

func (r *denierResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.hosts = append(r.hosts, host)
	r.callNum++
	var ipStr string
	switch r.callNum {
	case 1:
		ipStr = r.publicIP // First call returns public IP (initial URL)
	case 2:
		ipStr = r.privateIP // Second call returns private IP (redirect target)
	default:
		ipStr = r.publicIP // Further calls return public IP
	}
	return []netip.Addr{netip.MustParseAddr(ipStr)}, nil
}

// TestWebFetchSameHostRedirectRealCompositionAddressRefusal tests that a same-host redirect
// to an address violating address validation (private address) is refused with no dial.
func TestWebFetchSameHostRedirectRealCompositionAddressRefusal(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// Same-host redirect (hostname doesn't change, but address validation will fail)
			w.Header().Set("Location", "http://ok.test/redirected")
			w.WriteHeader(http.StatusSeeOther)
		} else {
			t.Fatalf("second fetch should not be called (address validation should refuse)")
		}
	})

	// Use real PinnedFetcher with test seams that returns different addresses on each lookup
	screener := NewURLScreener(0)
	referee := &stubReferee{allow: true}
	resolver := &denierResolver{publicIP: "93.184.216.34", privateIP: "10.0.0.1"}

	f, err := NewPinnedFetcher(screener, referee, resolver)
	if err != nil {
		t.Fatal(err)
	}

	// Set up the dial seam to record dials
	dialCount := 0
	f.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialCount++
		var d net.Dialer
		return d.DialContext(ctx, network, server.Listener.Addr().String())
	}

	webTools := NewWebFetchTools(f, 0)

	args, _ := json.Marshal(map[string]string{"url": "http://ok.test/"})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}

	// Should be refused with address validation error
	if result.ErrorCode != RuleAddress {
		t.Fatalf("expected address refusal for private address in redirect, got %q", result.ErrorCode)
	}

	// Dial should not have been called for the redirect target's private address
	if dialCount > 1 {
		t.Fatalf("dial should not be called for redirected URL with private address, but got %d dials", dialCount)
	}

	// The resolver should have been called twice (once for initial URL, once for redirect target)
	if len(resolver.hosts) < 2 {
		t.Fatalf("expected at least 2 resolver calls, got %d: %v", len(resolver.hosts), resolver.hosts)
	}
}

// TestWebFetchSameHostRedirectRealCompositionSuccess tests that all 3 same-host redirects
// through the real PinnedFetcher pass all checks and counters increment correctly.
func TestWebFetchSameHostRedirectRealCompositionSuccess(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			w.Header().Set("Location", "http://ok.test/redirect1")
			w.WriteHeader(http.StatusSeeOther)
		case 2:
			w.Header().Set("Location", "http://ok.test/redirect2")
			w.WriteHeader(http.StatusSeeOther)
		case 3:
			w.Header().Set("Location", "http://ok.test/final")
			w.WriteHeader(http.StatusSeeOther)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("success after 3 redirects"))
		}
	})

	// Use real PinnedFetcher with test seams
	g := newPinRig(t, server.Listener.Addr().String(), map[string][]string{
		"ok.test": {"93.184.216.34"}, // Public address
	})
	webTools := NewWebFetchTools(g.f, 0)

	args, _ := json.Marshal(map[string]string{"url": "http://ok.test/"})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}

	if result.ErrorCode != "" {
		t.Fatalf("expected no error, got %q", result.ErrorCode)
	}
	if string(result.Content) != "success after 3 redirects" {
		t.Fatalf("expected final content, got %q", string(result.Content))
	}

	// Verify that the HTTP handler was called 4 times (initial + 3 redirects)
	if callCount != 4 {
		t.Fatalf("expected 4 HTTP calls (3 redirects + final request), got %d", callCount)
	}
}
