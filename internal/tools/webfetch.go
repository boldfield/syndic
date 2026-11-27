package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// WebFetcher sends GET requests to screened URLs. It is implemented by PinnedFetcher
// and can be mocked for testing.
type WebFetcher interface {
	Fetch(ctx context.Context, rawURL string) (*http.Response, error)
}

// WebFetchTools executes web_fetch calls for one attempt of one turn. The fetch
// request is sent through a WebFetcher (e.g., PinnedFetcher), which applies
// pre-send checks, refereeing, and address validation. WebFetchTools handles
// redirects: same-host redirects are followed with the fetch retried; cross-host
// redirects are returned as data. After three hops, the fourth is refused.
type WebFetchTools struct {
	fetcher        WebFetcher
	maxResultBytes int
}

// NewWebFetchTools returns a fetcher for one attempt. maxResultBytes <= 0 selects
// DefaultMaxResultBytes. The fetcher parameter is required.
func NewWebFetchTools(fetcher WebFetcher, maxResultBytes int) *WebFetchTools {
	if maxResultBytes <= 0 {
		maxResultBytes = DefaultMaxResultBytes
	}
	return &WebFetchTools{
		fetcher:        fetcher,
		maxResultBytes: maxResultBytes,
	}
}

// Fetch executes a web_fetch call with the given arguments. It delegates screening,
// refereeing and address validation to the WebFetcher. Same-host redirects are
// followed with the fetch retried; cross-host redirects are returned as data.
// After three hops the fourth is refused.
func (w *WebFetchTools) Fetch(ctx context.Context, callID string, args json.RawMessage) (Result, error) {
	var parsed struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &parsed); err != nil {
		return Result{ErrorCode: ErrCodeBadArguments}, nil
	}
	if parsed.URL == "" {
		return Result{ErrorCode: ErrCodeBadArguments}, nil
	}

	// Follow redirects up to 3 hops; refuse the 4th.
	for hop := 0; hop < 4; hop++ {
		if hop == 3 {
			return Result{ErrorCode: "redirect_limit"}, nil
		}

		// Fetch through the WebFetcher, which applies screening, refereeing and
		// address validation.
		resp, err := w.fetcher.Fetch(ctx, parsed.URL)
		if err != nil {
			if refErr, ok := err.(*RefusalError); ok {
				return Result{ErrorCode: refErr.Rule}, nil
			}
			return Result{ErrorCode: "fetch_error"}, nil
		}

		// Handle redirects (3xx status). Close body before continuing the loop.
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if location == "" {
				return Result{ErrorCode: "invalid_redirect"}, nil
			}

			// Parse the redirect target as an absolute URL.
			targetURL, err := url.Parse(location)
			if err != nil {
				return Result{ErrorCode: "invalid_redirect"}, nil
			}
			if !targetURL.IsAbs() {
				// Relative redirect: resolve relative to current URL.
				currentURL, _ := url.Parse(parsed.URL)
				targetURL = currentURL.ResolveReference(targetURL)
			}
			redirectTarget := targetURL.String()

			// Check if redirect is to the same host.
			currentHost := getCanonicalHost(parsed.URL)
			redirectHost := getCanonicalHost(redirectTarget)
			if currentHost != redirectHost {
				// Cross-host redirect: return as data.
				return Result{Content: []byte(redirectTarget)}, nil
			}

			// Same-host redirect: loop to fetch again with the new URL.
			parsed.URL = redirectTarget
			continue
		}

		// Not a redirect: read and return the response body, bounded by maxResultBytes.
		body, err := io.ReadAll(io.LimitReader(resp.Body, int64(w.maxResultBytes)+1))
		_ = resp.Body.Close()
		if err != nil {
			return Result{ErrorCode: "read_error"}, nil
		}

		// Check result size.
		if len(body) > w.maxResultBytes {
			msg := fmt.Sprintf("result too large (%d > %d bytes)", len(body)-1, w.maxResultBytes)
			return Result{ErrorCode: ErrCodeTooLarge, Content: []byte(msg)}, nil
		}

		return Result{Content: body}, nil
	}

	// Should not reach here.
	return Result{ErrorCode: "redirect_limit"}, nil
}

// getCanonicalHost returns the canonical hostname of a URL for comparison.
func getCanonicalHost(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host, _ := canonicalHost(parsedURL)
	return host
}
