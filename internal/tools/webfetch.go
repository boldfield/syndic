package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// WebFetcher sends GET requests to screened URLs. It is implemented by PinnedFetcher
// and can be mocked for testing.
type WebFetcher interface {
	Fetch(ctx context.Context, rawURL string) (*http.Response, error)
}

// WebFetchTools executes web_fetch calls for one attempt of one turn. Each fetch
// is screened, refereed, and pinned to a validated address. Redirects to the
// same host are followed with repeated screening and refereeing; redirects to a
// different host are returned as data. After three hops, the fourth is refused.
type WebFetchTools struct {
	screener *URLScreener
	referee  Referee
	fetcher  WebFetcher
	turnID   string
	caller   string
	callID   string
	// recordRefereeDecision is called by Fetch to record each referee decision.
	recordRefereeDecision func(ctx context.Context, url string, verdict bool, reason string) error
}

// NewWebFetchTools returns a fetcher for one attempt. All components are required.
func NewWebFetchTools(screener *URLScreener, referee Referee, fetcher WebFetcher,
	turnID, caller, callID string,
	recordRefereeDecision func(ctx context.Context, url string, verdict bool, reason string) error) *WebFetchTools {
	return &WebFetchTools{
		screener:              screener,
		referee:               referee,
		fetcher:               fetcher,
		turnID:                turnID,
		caller:                caller,
		callID:                callID,
		recordRefereeDecision: recordRefereeDecision,
	}
}

// Fetch executes a web_fetch call with the given arguments. It applies pre-send
// checks, consults the referee, and handles redirects. Same-host redirects are
// followed with repeated checks and refereeing; cross-host redirects are returned
// as data. After three hops, the fourth is refused.
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

	// Follow redirects up to 3 hops (fourth is refused).
	for hop := 0; hop < 3; hop++ {
		// Apply pre-send checks.
		verdict := w.screener.Screen(parsed.URL)
		if !verdict.Allowed {
			return Result{ErrorCode: verdict.Rule}, nil
		}

		// Consult the referee.
		if w.recordRefereeDecision != nil {
			allow, reason, err := w.referee.Allow(ctx, parsed.URL)
			if err != nil {
				_ = w.recordRefereeDecision(ctx, parsed.URL, false, err.Error())
				return Result{ErrorCode: "referee_error"}, nil
			}
			_ = w.recordRefereeDecision(ctx, parsed.URL, allow, reason)
			if !allow {
				return Result{ErrorCode: RuleReferee}, nil
			}
		} else {
			allow, _, err := w.referee.Allow(ctx, parsed.URL)
			if err != nil || !allow {
				return Result{ErrorCode: RuleReferee}, nil
			}
		}

		// Fetch the URL with pinned connection.
		resp, err := w.fetcher.Fetch(ctx, parsed.URL)
		if err != nil {
			if refErr, ok := err.(*RefusalError); ok {
				return Result{ErrorCode: refErr.Rule}, nil
			}
			return Result{ErrorCode: "fetch_error"}, nil
		}
		defer func() { _ = resp.Body.Close() }()

		// Handle redirects (3xx status).
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			location := resp.Header.Get("Location")
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

		// Not a redirect: read and return the response body.
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return Result{ErrorCode: "read_error"}, nil
		}

		// Check result size.
		maxBytes := DefaultMaxResultBytes
		if len(body) > maxBytes {
			return Result{ErrorCode: ErrCodeTooLarge}, nil
		}

		return Result{Content: body}, nil
	}

	// Fourth hop: refused.
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
