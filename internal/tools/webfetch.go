package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Error codes of the web fetch tool.
const (
	ErrCodeRefused       = "refused"
	ErrCodeRedirectLimit = "redirect_limit"
	ErrCodeFetch         = "fetch_error"
	ErrCodeHTTPStatus    = "http_error"
)

// maxRedirectHops is the number of same-host redirects followed; the next one
// is refused (v5 delta section 6, pre-send check 3).
const maxRedirectHops = 3

// WebFetcher sends one screened, refereed GET. *PinnedFetcher is the
// production implementation and owns screening, the referee, address
// validation and pinning, so WebFetchTools adds none of them: each hop is
// passed to Fetch exactly once and counted exactly once.
type WebFetcher interface {
	Fetch(ctx context.Context, rawURL string) (*http.Response, error)
}

// WebFetchTools implements the GET fetch tool.
type WebFetchTools struct {
	fetcher        WebFetcher
	maxResultBytes int
}

// NewWebFetchTools returns the fetch tool. maxResultBytes <= 0 selects
// DefaultMaxResultBytes.
func NewWebFetchTools(f WebFetcher, maxResultBytes int) *WebFetchTools {
	if maxResultBytes <= 0 {
		maxResultBytes = DefaultMaxResultBytes
	}
	return &WebFetchTools{fetcher: f, maxResultBytes: maxResultBytes}
}

func fetchError(code, format string, a ...any) Result {
	return Result{Content: []byte(fmt.Sprintf(format, a...)), ErrorCode: code}
}

// Fetch retrieves rawURL. A redirect to the same host is followed through the
// fetcher as a new URL, so every check repeats; after three followed hops the
// next redirect is refused. A redirect to another host is returned as data and
// never followed. A result over the size limit is an error stating the limit.
func (t *WebFetchTools) Fetch(ctx context.Context, rawURL string) (Result, error) {
	cur := rawURL
	for hop := 0; ; hop++ {
		curURL, err := url.Parse(cur)
		if err != nil {
			return fetchError(ErrCodeBadArguments, "URL does not parse"), nil
		}
		resp, err := t.fetcher.Fetch(ctx, cur)
		if err != nil {
			var r *RefusalError
			if errors.As(err, &r) {
				return fetchError(ErrCodeRefused, "fetch refused: %s", r.Error()), nil
			}
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return fetchError(ErrCodeFetch, "fetch failed"), nil
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if loc == "" {
				return fetchError(ErrCodeHTTPStatus, "HTTP status %d without a Location", resp.StatusCode), nil
			}
			next, err := curURL.Parse(loc)
			if err != nil {
				return fetchError(ErrCodeFetch, "redirect target does not parse"), nil
			}
			curHost, _ := canonicalHost(curURL)
			nextHost, _ := canonicalHost(next)
			if curHost != nextHost {
				msg := "redirect to a different host (not followed): " + next.String()
				if len(msg) > t.maxResultBytes {
					return fetchError(ErrCodeTooLarge, "result too large: more than %d bytes", t.maxResultBytes), nil
				}
				return Result{Content: []byte(msg)}, nil
			}
			if hop >= maxRedirectHops {
				return fetchError(ErrCodeRedirectLimit, "redirect limit reached: more than %d redirects", maxRedirectHops), nil
			}
			cur = next.String()
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxResultBytes)+1))
		_ = resp.Body.Close()
		if err != nil {
			return fetchError(ErrCodeFetch, "reading the response failed"), nil
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body = []byte(fmt.Sprintf("HTTP status %d\n%s", resp.StatusCode, body))
			if len(body) > t.maxResultBytes {
				return fetchError(ErrCodeTooLarge, "result too large: more than %d bytes", t.maxResultBytes), nil
			}
			return Result{Content: body, ErrorCode: ErrCodeHTTPStatus}, nil
		}
		if len(body) > t.maxResultBytes {
			return fetchError(ErrCodeTooLarge, "result too large: more than %d bytes", t.maxResultBytes), nil
		}
		return Result{Content: body}, nil
	}
}
