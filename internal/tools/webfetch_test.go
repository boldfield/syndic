package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeReferee struct {
	allow bool
}

func (f *fakeReferee) Allow(ctx context.Context, rawURL string) (bool, string, error) {
	return f.allow, "", nil
}

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

	screener := NewURLScreener(20)
	referee := &fakeReferee{allow: true}
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	webTools := NewWebFetchTools(screener, referee, fetcher, "turn1", "author", "call1", nil)
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

	screener := NewURLScreener(20)
	referee := &fakeReferee{allow: true}
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	webTools := NewWebFetchTools(screener, referee, fetcher, "turn1", "author", "call1", nil)
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

	screener := NewURLScreener(20)
	referee := &fakeReferee{allow: true}
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	webTools := NewWebFetchTools(screener, referee, fetcher, "turn1", "author", "call1", nil)
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

// TestWebFetchRedirectLimit verifies that the fourth redirect hop is refused.
func TestWebFetchRedirectLimit(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()

	callCount := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount <= 3 {
			w.Header().Set("Location", server.URL+"?hop="+string(rune('A'+callCount)))
			w.WriteHeader(http.StatusSeeOther)
		} else {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("final"))
		}
	})

	screener := NewURLScreener(20)
	referee := &fakeReferee{allow: true}
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	webTools := NewWebFetchTools(screener, referee, fetcher, "turn1", "author", "call1", nil)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != "redirect_limit" {
		t.Fatalf("expected redirect_limit error, got %q", result.ErrorCode)
	}
}

// TestWebFetchResultSizeLimit verifies that oversized results produce an error.
func TestWebFetchResultSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write more than DefaultMaxResultBytes.
		for i := 0; i < DefaultMaxResultBytes+1000; i++ {
			_, _ = w.Write([]byte("x"))
		}
	}))
	defer server.Close()

	screener := NewURLScreener(20)
	referee := &fakeReferee{allow: true}
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	webTools := NewWebFetchTools(screener, referee, fetcher, "turn1", "author", "call1", nil)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != ErrCodeTooLarge {
		t.Fatalf("expected %s error, got %q", ErrCodeTooLarge, result.ErrorCode)
	}
}

// TestWebFetchRefereeDenies verifies that a referee denial produces an error.
func TestWebFetchRefereeDenies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	screener := NewURLScreener(20)
	referee := &fakeReferee{allow: false}
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	webTools := NewWebFetchTools(screener, referee, fetcher, "turn1", "author", "call1", nil)
	args, _ := json.Marshal(map[string]string{"url": server.URL})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != RuleReferee {
		t.Fatalf("expected %s error, got %q", RuleReferee, result.ErrorCode)
	}
}

// TestWebFetchInvalidJSON verifies that invalid JSON in args produces an error.
func TestWebFetchInvalidJSON(t *testing.T) {
	screener := NewURLScreener(20)
	referee := &fakeReferee{allow: true}
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	webTools := NewWebFetchTools(screener, referee, fetcher, "turn1", "author", "call1", nil)
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
	screener := NewURLScreener(20)
	referee := &fakeReferee{allow: true}
	fetcher := &fakeFetcher{client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	webTools := NewWebFetchTools(screener, referee, fetcher, "turn1", "author", "call1", nil)
	args, _ := json.Marshal(map[string]string{"url": ""})
	result, err := webTools.Fetch(context.Background(), "call1", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.ErrorCode != ErrCodeBadArguments {
		t.Fatalf("expected %s error, got %q", ErrCodeBadArguments, result.ErrorCode)
	}
}
