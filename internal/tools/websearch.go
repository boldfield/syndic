package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Error codes for web_search tool.
const (
	ErrCodeSearchRefused = "refused"
	ErrCodeSearchFetch   = "fetch_error"
	ErrCodeSearchStatus  = "http_error"
)

// SearchProvider sends one screened, refereed web search query and returns
// results. *PinnedSearcher is the production implementation and owns screening,
// the referee, address validation and pinning. Each query is passed to Search
// exactly once and counted exactly once.
type SearchProvider interface {
	Search(ctx context.Context, query string) (*http.Response, error)
}

// WebSearchTools implements the web_search tool.
type WebSearchTools struct {
	provider       SearchProvider
	maxResultBytes int
}

// NewWebSearchTools returns the search tool. maxResultBytes <= 0 selects
// DefaultMaxResultBytes.
func NewWebSearchTools(p SearchProvider, maxResultBytes int) *WebSearchTools {
	if maxResultBytes <= 0 {
		maxResultBytes = DefaultMaxResultBytes
	}
	return &WebSearchTools{provider: p, maxResultBytes: maxResultBytes}
}

func searchError(code, format string, a ...any) Result {
	return Result{Content: []byte(fmt.Sprintf(format, a...)), ErrorCode: code}
}

// Search submits query to the search provider. The provider's hostname is
// subject to the same address rules as web_fetch. Results are limited by size
// and formatted as a list of entries, each containing title, URL and snippet.
func (t *WebSearchTools) Search(ctx context.Context, query string) (Result, error) {
	if query == "" {
		return searchError(ErrCodeBadArguments, "query cannot be empty"), nil
	}
	resp, err := t.provider.Search(ctx, query)
	if err != nil {
		var r *RefusalError
		if errors.As(err, &r) {
			return searchError(ErrCodeSearchRefused, "search refused: %s", r.Error()), nil
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return searchError(ErrCodeSearchFetch, "search failed"), nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxResultBytes)+1))
	if err != nil {
		return searchError(ErrCodeSearchFetch, "reading the response failed"), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body = []byte(fmt.Sprintf("HTTP status %d\n%s", resp.StatusCode, body))
		if len(body) > t.maxResultBytes {
			return searchError(ErrCodeTooLarge, "result too large: more than %d bytes", t.maxResultBytes), nil
		}
		return Result{Content: body, ErrorCode: ErrCodeSearchStatus}, nil
	}
	if len(body) > t.maxResultBytes {
		return searchError(ErrCodeTooLarge, "result too large: more than %d bytes", t.maxResultBytes), nil
	}
	return Result{Content: body}, nil
}
