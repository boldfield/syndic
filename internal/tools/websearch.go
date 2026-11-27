package tools

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/boldfield/syndic/internal/journal"
)

// Error code of a web search that failed after the checks allowed it: the
// provider could not be reached, or its request could not be built. A refusal
// uses ErrCodeRefused, a non-2xx reply ErrCodeHTTPStatus and an over-bound
// body ErrCodeTooLarge, as a fetch does.
const ErrCodeSearch = "search_error"

// RuleProvider is the rule a search is refused by when the provider's request
// does not address the configured host over https with GET, no body and no
// credential in the URL: the engine sends only what the design permits.
const RuleProvider = "provider"

// searchScheme is the only scheme a provider request may use: the credential
// travels with it.
const searchScheme = "https"

// SearchProvider is the vendor-specific part of web_search (v5 delta section
// 6, pre-send check 6) and the injection seam for the operator-selected
// integration: the engine takes its providers through engine.Options, and
// [tools.web.search].provider names one of them. The provider renders a query
// as a request and nothing more. Everything that protects the query route
// belongs to the engine and runs before Request is called: the canary and
// shape checks, the allowlist, the referee, name resolution of the configured
// host under the address rule, and the pinned connection. The provider never
// resolves a name, opens a connection or sees a query the checks refused.
//
// No vendor has been chosen. This package has no production provider; the
// fake providers live in tests.
type SearchProvider interface {
	// Name is the value [tools.web.search].provider selects this provider by.
	Name() string
	// Request renders one allowed query as the single GET request the
	// provider receives, addressed to host over https. credential is the
	// resolved [tools.web.search].auth secret; it goes in a request header,
	// never in the URL. The engine checks the request against the rules
	// RuleProvider names and refuses one that breaks them.
	Request(ctx context.Context, host, query, credential string) (*http.Request, error)
}

// CredentialSource resolves the provider credential. A searcher calls it
// once per query, only after the referee has allowed the query, the
// provider's address has passed the address rule and the step's re-check has
// passed again after that wait, and the value goes into that query's request
// alone; it is never journaled. Resolving it is itself a wait (a secret may
// be read from a file), so the request it is placed in is sent only after the
// step's re-check has passed once more.
type CredentialSource func() (string, error)

// SearchNetwork is the network under a searcher: the resolver of the provider
// host and, for tests, the dialer and TLS configuration of the pinned
// connection. A nil *SearchNetwork, Resolver or Dial selects
// net.DefaultResolver, a net.Dialer with dialTimeout and the default TLS
// configuration; the daemon sets none of them.
type SearchNetwork struct {
	Resolver  Resolver
	Dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	TLSConfig *tls.Config
}

// QueryURL renders a web_search query as the URL the pre-send checks screen:
// the provider host with the query as the one query value. The design checks
// a query "exactly as a URL is", so the canary, the shape rules, the allowlist
// and the fetch counts all run on this form, with no name resolution.
func QueryURL(host, query string) string {
	return searchScheme + "://" + host + "/?q=" + url.QueryEscape(query)
}

// SearchHostAllowed reports whether the provider host would pass the scheme,
// shape and allowlist checks, as the offering of web_search requires (v5
// delta section 6, pre-send check 6). It counts nothing against any cap and
// resolves nothing; nil means the host passes.
func SearchHostAllowed(host string, policy *HostPolicy) *RefusalError {
	rawURL := searchScheme + "://" + host + "/"
	if r := checkScheme(rawURL); r != nil {
		return r
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return refusal(RuleShape, "provider host is not a valid hostname")
	}
	if _, v := shapeCheck(rawURL, u); v != nil {
		return refusal(v.Rule, v.Reason)
	}
	if r := checkCredentials(u); r != nil {
		return r
	}
	return policy.checkHost(u)
}

// PinnedSearcher sends allowed web_search queries to the configured provider
// over a connection pinned to a validated address. For each query the order
// is fixed: the pre-send checks on the query as a URL (canary, shape,
// allowlist, counts), the referee, the caller's re-check, and only then name
// resolution of the provider host (once), the address rule over every
// resolved address, the caller's re-check again, the credential, the
// provider's request held to the rules, the caller's re-check a third time,
// and a connection to an address that passed. Every wait is followed by a
// re-check before the next effect. A refused query causes no resolution, no
// connection and no credential read; a step that ends during the resolution
// causes no connection and no credential read; a step that ends during the
// credential's resolution causes no connection and sends nothing.
type PinnedSearcher struct {
	screener   *URLScreener
	referee    Referee
	provider   SearchProvider
	host       string
	credential CredentialSource
	net        SearchNetwork
}

// NewPinnedSearcher returns a searcher. Every component is required: a
// missing referee refuses rather than authorizing, and a missing credential
// source has nothing to send.
func NewPinnedSearcher(s *URLScreener, r Referee, p SearchProvider, host string, cred CredentialSource, n *SearchNetwork) (*PinnedSearcher, error) {
	if s == nil || r == nil || p == nil || host == "" || cred == nil {
		return nil, errors.New("pinned searcher needs a screener, a referee, a provider, a host and a credential source")
	}
	ps := &PinnedSearcher{screener: s, referee: r, provider: p, host: host, credential: cred}
	if n != nil {
		ps.net = *n
	}
	if ps.net.Resolver == nil {
		ps.net.Resolver = net.DefaultResolver
	}
	return ps, nil
}

// Search sends one query and returns the provider's response, unread. A
// pre-send refusal, a referee deny, a failed address rule and a provider
// request that breaks the rules are each a *RefusalError. An error from the
// referee itself is returned as is: the engine's referee reports an ended
// turn that way, and the caller must not answer the call. recheck, when set,
// runs after the referee's allow and before any resolution, again after the
// resolution and before the credential is read, and once more after the
// credential is resolved and the provider's request is built and checked,
// immediately before the request is sent: the resolution and the credential's
// resolution are waits, and the step may have ended during either. Its error
// is returned as is. The caller reads the body, re-checks again after that
// wait, and journals.
func (s *PinnedSearcher) Search(ctx context.Context, query string, recheck func() error) (*http.Response, error) {
	if v := s.screener.Screen(QueryURL(s.host, query)); !v.Allowed {
		return nil, refusal(v.Rule, v.Reason)
	}
	allow, _, err := s.referee.Allow(ctx, query)
	if err != nil {
		return nil, err
	}
	if !allow {
		return nil, refusal(RuleReferee, "referee did not allow the query")
	}
	if recheck != nil {
		if err := recheck(); err != nil {
			return nil, err
		}
	}
	addrs, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	// The resolution was a wait: the step may have ended during it, and an
	// ended step must not read the credential or send anything.
	if recheck != nil {
		if err := recheck(); err != nil {
			return nil, err
		}
	}
	cred, err := s.credential()
	if err != nil {
		return nil, fmt.Errorf("search credential: %w", err)
	}
	req, err := s.provider.Request(ctx, s.host, query, cred)
	if err != nil {
		return nil, fmt.Errorf("provider request: %w", err)
	}
	if r := s.checkRequest(req, cred); r != nil {
		return nil, r
	}
	// Resolving the credential and building the request were waits too (the
	// secret may come from a file; the provider is injected code): the step
	// may have ended during them, and an ended step must not dial or send.
	// This is the last re-check before the request leaves.
	if recheck != nil {
		if err := recheck(); err != nil {
			return nil, err
		}
	}
	tr := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		TLSClientConfig:   s.net.TLSConfig,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return s.dialPinned(ctx, network, addrs)
		},
	}
	return tr.RoundTrip(req.WithContext(ctx))
}

// resolve looks the configured host up, once, and applies the address rule to
// every address: a mixed resolution is refused.
func (s *PinnedSearcher) resolve(ctx context.Context) ([]netip.Addr, error) {
	host, _ := canonicalHost(&url.URL{Host: s.host})
	addrs, err := s.net.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, refusal(RuleAddress, "provider host did not resolve")
	}
	for _, a := range addrs {
		if r := ValidateAddress(a); r != nil {
			return nil, r
		}
	}
	return addrs, nil
}

// checkRequest holds the provider's request to the rules: GET over https to
// the configured host, no body, no userinfo, and the credential nowhere in
// the URL.
func (s *PinnedSearcher) checkRequest(req *http.Request, cred string) *RefusalError {
	switch {
	case req == nil || req.URL == nil:
		return refusal(RuleProvider, "provider built no request")
	case req.Method != http.MethodGet:
		return refusal(RuleProvider, "provider request is not a GET")
	case strings.ToLower(req.URL.Scheme) != searchScheme:
		return refusal(RuleProvider, "provider request is not https")
	case req.URL.User != nil:
		return refusal(RuleProvider, "provider request carries userinfo")
	case req.Body != nil && req.Body != http.NoBody, req.ContentLength != 0:
		return refusal(RuleProvider, "provider request carries a body")
	}
	want, _ := canonicalHost(&url.URL{Host: s.host})
	if got, _ := canonicalHost(req.URL); got != want {
		return refusal(RuleProvider, "provider request is not addressed to the configured host")
	}
	// req.Host overrides the URL's host on the wire; empty means the URL's.
	if got, _ := canonicalHost(&url.URL{Host: req.Host}); req.Host != "" && got != want {
		return refusal(RuleProvider, "provider request's Host header is not the configured host")
	}
	if port := req.URL.Port(); port != "" && port != "443" {
		return refusal(RuleProvider, "provider request is not to port 443")
	}
	if cred != "" && strings.Contains(req.URL.String(), cred) {
		return refusal(RuleProvider, "provider request carries the credential in its URL")
	}
	return nil
}

// dialPinned connects to the first validated address that accepts, on 443.
func (s *PinnedSearcher) dialPinned(ctx context.Context, network string, addrs []netip.Addr) (net.Conn, error) {
	dial := s.net.Dial
	if dial == nil {
		d := &net.Dialer{Timeout: dialTimeout}
		dial = d.DialContext
	}
	var lastErr error
	for _, a := range addrs {
		c, err := dial(ctx, network, net.JoinHostPort(a.Unmap().String(), "443"))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// WebSearcher sends one screened, refereed query. *PinnedSearcher is the
// implementation and owns the checks, the referee, address validation and
// pinning, so WebSearchTools adds none of them.
type WebSearcher interface {
	Search(ctx context.Context, query string, recheck func() error) (*http.Response, error)
}

// WebSearchTools executes web_search for one turn and journals the exact
// bytes it returns, or the error text, against the accepted call it answers.
// The result is the provider's response body as received, bounded: a body
// over the bound is an error stating the bound, as a fetch's is.
type WebSearchTools struct {
	j              *journal.Store
	searcher       WebSearcher
	maxResultBytes int
}

// NewWebSearchTools returns the search tool over the journal. maxResultBytes
// <= 0 selects DefaultMaxResultBytes.
func NewWebSearchTools(j *journal.Store, s WebSearcher, maxResultBytes int) *WebSearchTools {
	if maxResultBytes <= 0 {
		maxResultBytes = DefaultMaxResultBytes
	}
	return &WebSearchTools{j: j, searcher: s, maxResultBytes: maxResultBytes}
}

type webSearchArgs struct {
	Query string `json:"query"`
}

// Search executes web_search for the accepted call callID. recheck is the
// step's re-check: it runs after the referee's allow, after the resolution,
// after the credential is resolved and the request built (immediately before
// the send) and after the network wait, and when it fails the call is
// abandoned, nothing is journaled here, and its error is returned. A refusal
// is journaled as an error result naming the rule; an executed call's result
// is the bounded raw body.
func (t *WebSearchTools) Search(ctx context.Context, callID string, raw json.RawMessage, recheck func() error) (Result, error) {
	var args webSearchArgs
	if err := decodeArgs(raw, &args); err != nil {
		return t.finish(callID, nil, err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return t.finish(callID, nil, toolErrorf(ErrCodeBadArguments, "query must not be empty"))
	}
	resp, err := t.searcher.Search(ctx, args.Query, recheck)
	if err != nil {
		// A refusal or a failure may have come after a wait (the referee, the
		// network), and the error may be the re-check's own: when the step
		// has ended meanwhile the call is abandoned, not answered.
		if rerr := recheck(); rerr != nil {
			return Result{}, rerr
		}
		var r *RefusalError
		if errors.As(err, &r) {
			return t.finish(callID, nil, toolErrorf(ErrCodeRefused, "search refused: %s", r.Error()))
		}
		return t.finish(callID, nil, toolErrorf(ErrCodeSearch, "search failed"))
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxResultBytes)+1))
	_ = resp.Body.Close()
	if err := recheck(); err != nil {
		return Result{}, err
	}
	switch {
	case readErr != nil:
		return t.finish(callID, nil, toolErrorf(ErrCodeSearch, "reading the provider's response failed"))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return t.finish(callID, nil, toolErrorf(ErrCodeHTTPStatus, "HTTP status %d from the search provider", resp.StatusCode))
	case len(body) > t.maxResultBytes:
		return t.finish(callID, nil, toolErrorf(ErrCodeTooLarge, "result too large: more than %d bytes", t.maxResultBytes))
	}
	return t.finish(callID, body, nil)
}

// finish journals the outcome of the accepted call callID, as the read tools
// do: a *toolError becomes an error result carrying its text; any other
// error is a failure of the engine itself, journals nothing and is returned.
func (t *WebSearchTools) finish(callID string, content []byte, err error) (Result, error) {
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
