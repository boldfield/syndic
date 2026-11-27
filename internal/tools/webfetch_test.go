package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
