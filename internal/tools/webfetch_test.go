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

// TestWebFetchCrossHostRedirect verifies that cross-host redirects are returned as data.
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
	if string(result.Content) != "https://other.example.com/page" {
		t.Fatalf("expected redirect target, got %q", string(result.Content))
	}
}

// TestWebFetchRedirectLimit verifies that after three same-host redirects, the fourth is refused.
func TestWebFetchRedirectLimit(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Allow 3 redirects (hops 0, 1, 2), then return 200 on the 4th call
		// which will never be reached because we refuse the 4th hop in the loop.
		if callCount <= 3 {
			w.Header().Set("Location", server.URL+"?hop="+string(rune('A'+callCount)))
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
	if result.ErrorCode != "redirect_limit" {
		t.Fatalf("expected redirect_limit error, got %q", result.ErrorCode)
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

// TestWebFetchCompositionWithFetcherScreening verifies that the fetcher handles
// screening and refereeing, not WebFetchTools. This test demonstrates the proper
// separation of concerns where WebFetchTools only handles redirects and size limits,
// while the fetcher (e.g., PinnedFetcher) handles all screening and refereeing.
func TestWebFetchCompositionWithFetcherScreening(t *testing.T) {
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
			_, _ = w.Write([]byte("content"))
		}
	})

	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	webTools := NewWebFetchTools(fetcher, 0)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	// With a simple fakeFetcher that doesn't apply any screening or refereeing,
	// we should still get the correct result since WebFetchTools only handles
	// redirect logic and size checking.
	if result.ErrorCode != "" {
		t.Fatalf("expected no error with delegation to fetcher, got %q", result.ErrorCode)
	}
	if string(result.Content) != "content" {
		t.Fatalf("expected body to be followed through redirect, got %q", string(result.Content))
	}
}
