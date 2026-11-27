package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// stubSearchProvider is a fake SearchProvider for testing.
type stubSearchProvider struct {
	err    error
	status int
}

func (s *stubSearchProvider) Search(ctx context.Context, query string) (*http.Response, error) {
	if s.err != nil {
		return nil, s.err
	}
	status := http.StatusOK
	if s.status != 0 {
		status = s.status
	}
	body := fmt.Sprintf("Title 1\nhttps://example.com/1\nSnippet about %s\n---\nTitle 2\nhttps://example.com/2\nAnother result", query)
	resp := &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	return resp, nil
}

func TestWebSearchTools_Search_Success(t *testing.T) {
	provider := &stubSearchProvider{}
	tools := NewWebSearchTools(provider, 1000)
	res, err := tools.Search(context.Background(), "go programming")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if res.ErrorCode != "" {
		t.Fatalf("Got error code %q, want no error", res.ErrorCode)
	}
	if len(res.Content) == 0 {
		t.Fatal("Got empty content")
	}
	expected := "Title 1\nhttps://example.com/1\nSnippet about go programming\n---\nTitle 2\nhttps://example.com/2\nAnother result"
	if string(res.Content) != expected {
		t.Fatalf("Unexpected content: %q", string(res.Content))
	}
}

func TestWebSearchTools_Search_EmptyQuery(t *testing.T) {
	provider := &stubSearchProvider{}
	tools := NewWebSearchTools(provider, 1000)
	res, err := tools.Search(context.Background(), "")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if res.ErrorCode != ErrCodeBadArguments {
		t.Fatalf("Got error code %q, want %q", res.ErrorCode, ErrCodeBadArguments)
	}
}

func TestWebSearchTools_Search_ProviderRefusal(t *testing.T) {
	provider := &stubSearchProvider{
		err: &RefusalError{Reason: "search_blocked"},
	}
	tools := NewWebSearchTools(provider, 1000)
	res, err := tools.Search(context.Background(), "blocked query")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if res.ErrorCode != ErrCodeSearchRefused {
		t.Fatalf("Got error code %q, want %q", res.ErrorCode, ErrCodeSearchRefused)
	}
}

func TestWebSearchTools_Search_HTTPError(t *testing.T) {
	provider := &stubSearchProvider{status: http.StatusInternalServerError}
	tools := NewWebSearchTools(provider, 1000)
	res, err := tools.Search(context.Background(), "query")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if res.ErrorCode != ErrCodeSearchStatus {
		t.Fatalf("Got error code %q, want %q", res.ErrorCode, ErrCodeSearchStatus)
	}
}

func TestWebSearchTools_Search_ResultTooLarge(t *testing.T) {
	provider := &stubSearchProvider{}
	tools := NewWebSearchTools(provider, 10) // very small limit
	res, err := tools.Search(context.Background(), "query")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if res.ErrorCode != ErrCodeTooLarge {
		t.Fatalf("Got error code %q, want %q", res.ErrorCode, ErrCodeTooLarge)
	}
}
