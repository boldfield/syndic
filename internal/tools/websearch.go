package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/journal"
)

// SearchProvider is the injection seam for a web search vendor (v5 delta
// section 6, pre-send check 6). The build supplies implementations; this
// package ships none, and no vendor is chosen: with no provider injected,
// web_search is not offered. A provider only describes its request. Screening,
// the referee, name resolution, the address rule, pinning and the credential's
// scope are PinnedSearcher's, so a provider cannot weaken them.
type SearchProvider interface {
	// Name is the identifier [tools.web.search].provider selects.
	Name() string
	// Host is the canonical hostname the provider is reached at: lowercase,
	// punycode, no port, not an IP literal. Every request must be to it.
	Host() string
	// NewRequest returns the HTTPS request that sends query, carrying
	// credential. It is called only after the query is allowed and the host's
	// addresses are validated, so the credential is never read for a refused
	// query. The request must be to Host() and must not carry the query or the
	// credential anywhere but the provider's own request.
	NewRequest(ctx context.Context, query, credential string) (*http.Request, error)
}

// SearchClient sends one allowed query to the provider and returns its
// response. *PinnedSearcher is the production implementation and owns the
// checks, so WebSearchTools adds none of them.
type SearchClient interface {
	Search(ctx context.Context, query string) (*http.Response, error)
}

// CheckSearchHost reports why web_search must not be offered for p: its host
// must be a hostname (never an IP literal) that passes the shape rules and the
// allowlist, when one is set. It performs no name resolution.
func CheckSearchHost(p SearchProvider, policy *HostPolicy) error {
	if p == nil {
		return errors.New("no search provider")
	}
	host := p.Host()
	if host == "" || looksLikeIP(host) || strings.ContainsAny(host, "/:@ ") {
		return fmt.Errorf("search provider host %q is not a hostname", host)
	}
	s := NewURLScreener(0)
	s.SetHostPolicy(policy)
	if v := s.Screen("https://" + host + "/"); !v.Allowed {
		return fmt.Errorf("search provider host refused by %s: %s", v.Rule, v.Reason)
	}
	return nil
}

// SearchOffered reports whether web_search is offered: the roster configures
// [tools.web.search], a supported provider of that name was injected, and its
// host passes CheckSearchHost. Without all three it is not offered.
func SearchOffered(cfg *config.SearchConfig, p SearchProvider, policy *HostPolicy) bool {
	if cfg == nil || cfg.Auth == nil || p == nil || p.Name() != cfg.Provider || cfg.Host != p.Host() {
		return false
	}
	return CheckSearchHost(p, policy) == nil
}

// PinnedSearcher sends screened, refereed queries to a validated address of
// the provider. The order is fixed, as for a fetch: the query is checked by
// the canary and shape rules, as a URL carrying it as a query value on the
// provider's host (the allowlist included); the referee is asked; and only
// after both allow is the provider's hostname resolved, once, every address
// checked by the address rule and the connection pinned to one that passed.
// The credential is resolved only then, and is given only to the provider's
// request to its own host. A refused query therefore causes no name
// resolution, no connection and no secret read.
type PinnedSearcher struct {
	fetch    *PinnedFetcher
	provider SearchProvider
	auth     config.SecretRef
	secrets  config.SecretResolver
}

// NewPinnedSearcher returns a searcher. All components are required: a
// missing referee refuses rather than authorizing. screener should have the
// roster's allowlist set and be shared with the turn's fetches.
func NewPinnedSearcher(s *URLScreener, r Referee, res Resolver, p SearchProvider, auth config.SecretRef, secrets config.SecretResolver) (*PinnedSearcher, error) {
	f, err := NewPinnedFetcher(s, r, res)
	if err != nil {
		return nil, errors.New("pinned searcher needs a screener, a referee and a resolver")
	}
	if p == nil || secrets == nil || auth.Name == "" {
		return nil, errors.New("pinned searcher needs a provider and a secret reference with a resolver")
	}
	return &PinnedSearcher{fetch: f, provider: p, auth: auth, secrets: secrets}, nil
}

// queryURL is the URL the canary, shape rules and allowlist see for query.
func (s *PinnedSearcher) queryURL(query string) string {
	return "https://" + s.provider.Host() + "/?q=" + url.QueryEscape(query)
}

// Search sends one query. A refusal is a *RefusalError.
func (s *PinnedSearcher) Search(ctx context.Context, query string) (*http.Response, error) {
	if v := s.fetch.screener.Screen(s.queryURL(query)); !v.Allowed {
		return nil, refusal(v.Rule, v.Reason)
	}
	allow, _, err := s.fetch.referee.Allow(ctx, query)
	if err != nil || !allow {
		return nil, refusal(RuleReferee, "referee did not allow the query")
	}
	probe, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+s.provider.Host()+"/", nil)
	if err != nil {
		return nil, refusal(RuleShape, "provider host does not form a URL")
	}
	addrs, err := s.fetch.resolve(ctx, probe)
	if err != nil {
		return nil, err
	}
	secret, err := s.secrets.Resolve(s.auth)
	if err != nil {
		// The reference is named; its value never is.
		return nil, fmt.Errorf("resolve search credential %s", s.auth)
	}
	req, err := s.provider.NewRequest(ctx, query, secret)
	if err != nil {
		return nil, errors.New("search provider could not build the request")
	}
	if err := s.checkRequest(req); err != nil {
		return nil, err
	}
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		TLSClientConfig:   s.fetch.tlsConfig,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return s.fetch.dialPinned(ctx, network, addrs, port)
		},
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		// A transport error can quote the URL; never anything of the request's headers.
		return nil, errors.New("search request failed")
	}
	return resp, nil
}

// checkRequest enforces the credential's scope: the request goes to the
// provider's own host, over HTTPS, with no userinfo.
func (s *PinnedSearcher) checkRequest(req *http.Request) error {
	if req == nil || req.URL == nil {
		return refusal(RuleShape, "provider returned no request")
	}
	host, ok := canonicalHost(req.URL)
	if !ok || host != s.provider.Host() || req.URL.Scheme != "https" || req.URL.User != nil {
		return refusal(RuleAllowlist, "provider request is not an HTTPS request to the provider's host")
	}
	return nil
}

// searchArgs are web_search's arguments.
type searchArgs struct {
	Query string `json:"query"`
}

// WebSearchTools implements web_search for one attempt of one turn and
// journals the exact bytes it returns against the accepted call it answers.
type WebSearchTools struct {
	client         SearchClient
	j              *journal.Store
	turnID         string
	attemptID      string
	maxResultBytes int
}

// NewWebSearchTools returns the search tool. maxResultBytes <= 0 selects
// DefaultMaxResultBytes.
func NewWebSearchTools(c SearchClient, j *journal.Store, turnID, attemptID string, maxResultBytes int) *WebSearchTools {
	if maxResultBytes <= 0 {
		maxResultBytes = DefaultMaxResultBytes
	}
	return &WebSearchTools{client: c, j: j, turnID: turnID, attemptID: attemptID, maxResultBytes: maxResultBytes}
}

// WebSearch runs the call. The raw response body, bounded by
// tools.max_result_bytes, is the result: the engine does not reshape it. A
// refusal, a non-2xx status, a body over the limit and a failed request are
// error results with a code. A cancelled context returns its error and
// journals nothing, for the caller to abandon the call.
func (t *WebSearchTools) WebSearch(ctx context.Context, callID string, raw json.RawMessage) (Result, error) {
	content, err := t.search(ctx, raw)
	var te *toolError
	if err != nil && !errors.As(err, &te) {
		return Result{}, err
	}
	res, status, err := outcome(content, err)
	if err != nil {
		return Result{}, err
	}
	if jerr := t.j.Tx(func(tx *journal.Tx) error {
		_, e := tx.RecordToolResult(callID, status, res.Content, res.ErrorCode)
		return e
	}); jerr != nil {
		return Result{}, fmt.Errorf("journal result of call %s: %w", callID, jerr)
	}
	return res, nil
}

func (t *WebSearchTools) search(ctx context.Context, raw json.RawMessage) ([]byte, error) {
	var a searchArgs
	if err := decodeArgs(raw, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Query) == "" {
		return nil, toolErrorf(ErrCodeBadArguments, "query must not be empty")
	}
	resp, err := t.client.Search(ctx, a.Query)
	if err != nil {
		var r *RefusalError
		if errors.As(err, &r) {
			return nil, toolErrorf(ErrCodeRefused, "search refused: %s", r.Error())
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, toolErrorf(ErrCodeFetch, "search failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxResultBytes)+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, toolErrorf(ErrCodeFetch, "reading the response failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Redirects are not followed: a 3xx is an error status like any other.
		msg := fmt.Sprintf("HTTP status %d\n%s", resp.StatusCode, body)
		if len(msg) > t.maxResultBytes {
			return nil, toolErrorf(ErrCodeTooLarge, "result too large: more than %d bytes", t.maxResultBytes)
		}
		return nil, toolErrorf(ErrCodeHTTPStatus, "%s", msg)
	}
	if len(body) > t.maxResultBytes {
		return nil, toolErrorf(ErrCodeTooLarge, "result too large: more than %d bytes", t.maxResultBytes)
	}
	return body, nil
}

var _ SearchClient = (*PinnedSearcher)(nil)
