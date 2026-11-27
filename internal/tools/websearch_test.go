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
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

const (
	searchHost = "search.test"
	searchCred = "key-7f3a9c0d-secret"
)

// fakeSearchProvider is the test provider: GET https://<host>/search?q=<query>
// with the credential in a header. Tests only; the package has no production
// provider.
type fakeSearchProvider struct {
	name string
	// request, when set, replaces the request built, so a test can hand the
	// engine a provider that breaks the rules.
	request func(ctx context.Context, host, query, credential string) (*http.Request, error)
}

func (p *fakeSearchProvider) Name() string {
	if p.name == "" {
		return "fake"
	}
	return p.name
}

func (p *fakeSearchProvider) Request(ctx context.Context, host, query, credential string) (*http.Request, error) {
	if p.request != nil {
		return p.request(ctx, host, query, credential)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/search?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Search-Key", credential)
	return req, nil
}

// searchRig is a searcher over stubs that record every referee question,
// lookup, dial and credential read.
type searchRig struct {
	s        *PinnedSearcher
	screener *URLScreener
	res      *stubResolver
	ref      *stubReferee
	provider *fakeSearchProvider
	cred     CredentialSource
	net      SearchNetwork
	dials    []string
	creds    atomic.Int32
}

// providerServer is a TLS server standing in for the provider: it answers
// every request with status and body and records the requests it received.
type providerServer struct {
	ts   *httptest.Server
	mu   sync.Mutex
	seen []*http.Request
}

func newProviderServer(t *testing.T, status int, body string) *providerServer {
	t.Helper()
	ps := &providerServer{}
	ps.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.mu.Lock()
		ps.seen = append(ps.seen, r.Clone(context.Background()))
		ps.mu.Unlock()
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(ps.ts.Close)
	return ps
}

func (ps *providerServer) requests() []*http.Request {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return append([]*http.Request(nil), ps.seen...)
}

// newSearchRig returns a rig whose dial seam carries the pinned connection to
// target, or fails the test when target is empty, and whose resolver answers
// addrs.
func newSearchRig(t *testing.T, screener *URLScreener, target string, addrs map[string][]string, allow bool) *searchRig {
	t.Helper()
	g := &searchRig{screener: screener, res: &stubResolver{addrs: addrs}, ref: &stubReferee{allow: allow}, provider: &fakeSearchProvider{}}
	g.cred = func() (string, error) {
		g.creds.Add(1)
		return searchCred, nil
	}
	g.net = SearchNetwork{
		Resolver: g.res,
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			g.dials = append(g.dials, addr)
			if target == "" {
				t.Errorf("dial reached for %s", addr)
				return nil, errors.New("unexpected dial")
			}
			var d net.Dialer
			return d.DialContext(ctx, network, target)
		},
		TLSConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the test server's certificate is self-signed
	}
	g.withResolver(t, g.res)
	return g
}

// withResolver rebuilds the rig's searcher over r, keeping its screener,
// referee, provider, dial seam and credential counter.
func (g *searchRig) withResolver(t *testing.T, r Resolver) {
	t.Helper()
	n := g.net
	n.Resolver = r
	s, err := NewPinnedSearcher(g.screener, g.ref, g.provider, searchHost, g.cred, &n)
	if err != nil {
		t.Fatal(err)
	}
	g.s = s
}

// blockingResolver is a stubResolver whose lookup waits: entered is closed
// when the lookup starts, and the lookup answers once release is closed. It
// stands in for a slow DNS during which the step can end.
type blockingResolver struct {
	*stubResolver
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newBlockingResolver(res *stubResolver) *blockingResolver {
	return &blockingResolver{stubResolver: res, entered: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return r.stubResolver.LookupNetIP(ctx, network, host)
}

var searchAddrs = map[string][]string{searchHost: {"93.184.216.34"}}

// wantNothingSent checks that no referee question beyond n, no lookup, no
// dial and no credential read happened.
func (g *searchRig) wantNothingSent(t *testing.T, refereeCalls int) {
	t.Helper()
	if len(g.ref.calls) != refereeCalls || len(g.res.hosts) != 0 || len(g.dials) != 0 || g.creds.Load() != 0 {
		t.Fatalf("referee %d (want %d) lookups %d dials %d credential reads %d; a refused query sends nothing", len(g.ref.calls), refereeCalls, len(g.res.hosts), len(g.dials), g.creds.Load())
	}
}

func TestNewPinnedSearcherRequiresEveryComponent(t *testing.T) {
	cred := func() (string, error) { return "", nil }
	p := &fakeSearchProvider{}
	cases := []struct {
		name string
		s    *URLScreener
		r    Referee
		p    SearchProvider
		host string
		c    CredentialSource
	}{
		{"screener", nil, &stubReferee{}, p, searchHost, cred},
		{"referee", NewURLScreener(0), nil, p, searchHost, cred},
		{"provider", NewURLScreener(0), &stubReferee{}, nil, searchHost, cred},
		{"host", NewURLScreener(0), &stubReferee{}, p, "", cred},
		{"credential", NewURLScreener(0), &stubReferee{}, p, searchHost, nil},
	}
	for _, c := range cases {
		if _, err := NewPinnedSearcher(c.s, c.r, c.p, c.host, c.c, nil); err == nil {
			t.Errorf("%s: missing component accepted", c.name)
		}
	}
	if _, err := NewPinnedSearcher(NewURLScreener(0), &stubReferee{}, p, searchHost, cred, nil); err != nil {
		t.Fatal(err)
	}
}

// A query carrying sixteen bytes of the turn's corpus is refused by the
// canary before the referee is asked, the host resolved, a connection made or
// the credential read; so is one the shape rule refuses.
func TestSearchRefusedQuerySendsNothing(t *testing.T) {
	cases := map[string]struct {
		query string
		rule  string
	}{
		"canary plain":  {"please find " + canaryText + " online", RuleCanary},
		"canary base64": {b64u(canaryText), RuleCanary},
		"canary hex":    {hx(canaryText), RuleCanary},
		"canary b64hex": {b64u(hx(canaryText)), RuleCanary},
		"shape entropy": {"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", RuleShape},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			g := newSearchRig(t, screenerWith(t, "context "+canaryText+" context"), "", searchAddrs, true)
			_, err := g.s.Search(context.Background(), c.query, nil)
			wantRefusal(t, err, c.rule)
			g.wantNothingSent(t, 0)
		})
	}
}

// A fifteen-byte overlap is not a canary match: the query reaches the
// referee.
func TestSearchFifteenByteOverlapReachesReferee(t *testing.T) {
	g := newSearchRig(t, screenerWith(t, canaryText), "", searchAddrs, false)
	_, err := g.s.Search(context.Background(), canaryText[:15], nil)
	wantRefusal(t, err, RuleReferee)
	if len(g.ref.calls) != 1 || g.ref.calls[0] != canaryText[:15] {
		t.Fatalf("referee asked %v, want the query once", g.ref.calls)
	}
}

// A referee deny refuses the query with no resolution, dial or credential
// read; an error from the referee is returned as is, not as a deny.
func TestSearchRefereeDenyAndErrorSendNothing(t *testing.T) {
	g := newSearchRig(t, NewURLScreener(0), "", searchAddrs, false)
	_, err := g.s.Search(context.Background(), "go testing", nil)
	wantRefusal(t, err, RuleReferee)
	g.wantNothingSent(t, 1)

	boom := errors.New("turn ended")
	g = newSearchRig(t, NewURLScreener(0), "", searchAddrs, true)
	g.ref.err = boom
	_, err = g.s.Search(context.Background(), "go testing", nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the referee's error unchanged", err)
	}
	var r *RefusalError
	if errors.As(err, &r) {
		t.Fatalf("referee error became a refusal %v", r)
	}
	g.wantNothingSent(t, 1)
}

// With an allowlist set, a provider host off it is refused before the referee
// and nothing is sent; SearchHostAllowed reports the same for the offering.
func TestSearchAllowlistRefusesProviderHost(t *testing.T) {
	policy, err := NewHostPolicy([]string{"*.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewURLScreener(0)
	s.SetHostPolicy(policy)
	g := newSearchRig(t, s, "", searchAddrs, true)
	_, err = g.s.Search(context.Background(), "go testing", nil)
	wantRefusal(t, err, RuleAllowlist)
	g.wantNothingSent(t, 0)

	if r := SearchHostAllowed(searchHost, policy); r == nil || r.Rule != RuleAllowlist {
		t.Fatalf("SearchHostAllowed(%s) = %v, want allowlist refusal", searchHost, r)
	}
	if r := SearchHostAllowed("api.example.com", policy); r != nil {
		t.Fatalf("api.example.com refused: %v", r)
	}
	if r := SearchHostAllowed("api.example.com", nil); r != nil {
		t.Fatalf("no allowlist refused: %v", r)
	}
	for _, bad := range []string{"", "user@host.test", strings.Repeat("a", 64) + ".test"} {
		if r := SearchHostAllowed(bad, nil); r == nil {
			t.Errorf("SearchHostAllowed(%q) allowed", bad)
		}
	}
	// An IP literal is refused whenever an allowlist is set, as for a fetch.
	for _, ip := range []string{"203.0.113.9", "[2001:db8::1]"} {
		if r := SearchHostAllowed(ip, policy); r == nil || r.Rule != RuleAllowlist {
			t.Errorf("SearchHostAllowed(%q) with an allowlist = %v, want allowlist refusal", ip, r)
		}
	}
}

// The recheck runs after the referee's allow and before any resolution; its
// error is returned as is and nothing is sent.
func TestSearchRecheckAfterRefereeStopsBeforeResolution(t *testing.T) {
	g := newSearchRig(t, NewURLScreener(0), "", searchAddrs, true)
	stale := errors.New("step ended")
	_, err := g.s.Search(context.Background(), "go testing", func() error { return stale })
	if !errors.Is(err, stale) {
		t.Fatalf("err = %v, want the recheck's error", err)
	}
	g.wantNothingSent(t, 1)
}

// Resolution is a wait. When the step ends while DNS is in flight, the
// re-check after the resolution stops the query before the credential is read
// or a connection made: the referee allowed, the lookup happened, and nothing
// else did.
func TestSearchRecheckAfterResolutionStopsBeforeCredential(t *testing.T) {
	g := newSearchRig(t, NewURLScreener(0), "", searchAddrs, true)
	dns := newBlockingResolver(g.res)
	g.withResolver(t, dns)
	stale := errors.New("step ended")
	var ended atomic.Bool
	recheck := func() error {
		if ended.Load() {
			return stale
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := g.s.Search(context.Background(), "go testing", recheck)
		done <- err
	}()
	select {
	case <-dns.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the lookup never started: the referee's allow and the first re-check should lead to resolution")
	}
	// The step ends while DNS is still waiting; then DNS answers.
	ended.Store(true)
	close(dns.release)
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Search did not return after the lookup was released")
	}
	if !errors.Is(err, stale) {
		t.Fatalf("err = %v, want the recheck's error", err)
	}
	var r *RefusalError
	if errors.As(err, &r) {
		t.Fatalf("recheck error became a refusal %v", r)
	}
	if len(g.ref.calls) != 1 || len(g.res.hosts) != 1 {
		t.Fatalf("referee %v lookups %v, want one each before the step ended", g.ref.calls, g.res.hosts)
	}
	if len(g.dials) != 0 || g.creds.Load() != 0 {
		t.Fatalf("dials %v credential reads %d; a step that ended during DNS sends nothing", g.dials, g.creds.Load())
	}
}

// A provider host resolving to a forbidden address is refused by the address
// rule after the referee, with no dial and no credential read.
func TestSearchAddressRuleRefusesPrivateProvider(t *testing.T) {
	for name, addrs := range map[string][]string{"loopback": {"127.0.0.1"}, "private": {"10.0.0.5"}, "mixed": {"93.184.216.34", "192.168.0.1"}} {
		t.Run(name, func(t *testing.T) {
			g := newSearchRig(t, NewURLScreener(0), "", map[string][]string{searchHost: addrs}, true)
			_, err := g.s.Search(context.Background(), "go testing", nil)
			wantRefusal(t, err, RuleAddress)
			if len(g.res.hosts) != 1 || len(g.dials) != 0 || g.creds.Load() != 0 {
				t.Fatalf("lookups %d dials %d credential reads %d", len(g.res.hosts), len(g.dials), g.creds.Load())
			}
		})
	}
}

// An allowed query is resolved once, after the referee, and sent over a
// connection pinned to the validated address with the credential in a header
// to the configured host; the provider's body comes back as is.
func TestSearchAllowedQueryIsPinnedAndCarriesCredential(t *testing.T) {
	body := `[{"title":"Go","url":"https://go.dev","extract":"The Go language"}]`
	ps := newProviderServer(t, http.StatusOK, body)
	g := newSearchRig(t, NewURLScreener(0), ps.ts.Listener.Addr().String(), searchAddrs, true)
	resp, err := g.s.Search(context.Background(), "go testing", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != body {
		t.Fatalf("body %q err %v", got, err)
	}
	if len(g.ref.calls) != 1 || len(g.res.hosts) != 1 || g.res.hosts[0] != searchHost {
		t.Fatalf("referee %v lookups %v, want one each", g.ref.calls, g.res.hosts)
	}
	if len(g.dials) != 1 || g.dials[0] != "93.184.216.34:443" {
		t.Fatalf("dials %v, want the validated address on 443", g.dials)
	}
	if g.creds.Load() != 1 {
		t.Fatalf("credential read %d times, want once", g.creds.Load())
	}
	seen := ps.requests()
	if len(seen) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(seen))
	}
	r := seen[0]
	if r.Method != http.MethodGet || r.Host != searchHost || r.URL.Query().Get("q") != "go testing" || r.Header.Get("X-Search-Key") != searchCred {
		t.Fatalf("provider received %s %s host %s header %q", r.Method, r.URL, r.Host, r.Header.Get("X-Search-Key"))
	}
	if strings.Contains(r.URL.String(), searchCred) {
		t.Fatalf("credential in URL %s", r.URL)
	}
}

// A provider request that breaks the rules is refused before any connection:
// another host, in the URL or the Host header, http, POST, a body, or the
// credential in the URL.
func TestSearchProviderRequestHeldToRules(t *testing.T) {
	build := func(method, rawURL string, withBody bool) func(context.Context, string, string, string) (*http.Request, error) {
		return func(ctx context.Context, _, _, cred string) (*http.Request, error) {
			var body io.Reader
			if withBody {
				body = strings.NewReader("q")
			}
			return http.NewRequestWithContext(ctx, method, strings.ReplaceAll(rawURL, "$CRED", cred), body)
		}
	}
	cases := map[string]func(context.Context, string, string, string) (*http.Request, error){
		"other host":        build(http.MethodGet, "https://other.test/search", false),
		"http":              build(http.MethodGet, "http://"+searchHost+"/search", false),
		"post":              build(http.MethodPost, "https://"+searchHost+"/search", false),
		"body":              build(http.MethodGet, "https://"+searchHost+"/search", true),
		"credential in url": build(http.MethodGet, "https://"+searchHost+"/search?key=$CRED", false),
		"userinfo":          build(http.MethodGet, "https://u:p@"+searchHost+"/search", false),
		"other port":        build(http.MethodGet, "https://"+searchHost+":8443/search", false),
		"host header": func(ctx context.Context, _, _, _ string) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+searchHost+"/search", nil)
			if err != nil {
				return nil, err
			}
			req.Host = "other.test"
			return req, nil
		},
		"nil request": func(context.Context, string, string, string) (*http.Request, error) {
			return nil, nil
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			g := newSearchRig(t, NewURLScreener(0), "", searchAddrs, true)
			g.provider.request = req
			_, err := g.s.Search(context.Background(), "go testing", nil)
			wantRefusal(t, err, RuleProvider)
			if len(g.dials) != 0 {
				t.Fatalf("dialed %v", g.dials)
			}
		})
	}
	g := newSearchRig(t, NewURLScreener(0), "", searchAddrs, true)
	g.provider.request = func(context.Context, string, string, string) (*http.Request, error) {
		return nil, errors.New("vendor down")
	}
	_, err := g.s.Search(context.Background(), "go testing", nil)
	var r *RefusalError
	if err == nil || errors.As(err, &r) || len(g.dials) != 0 {
		t.Fatalf("provider error: err %v dials %v; want a plain error and no dial", err, g.dials)
	}
}

// Every search counts against the per-host and per-turn fetch caps, as a
// fetch does.
func TestSearchCountsAgainstFetchCaps(t *testing.T) {
	s := NewURLScreener(3)
	g := newSearchRig(t, s, "", searchAddrs, false)
	for i := 0; i < 3; i++ {
		_, err := g.s.Search(context.Background(), fmt.Sprintf("query %d", i), nil)
		wantRefusal(t, err, RuleReferee)
	}
	_, err := g.s.Search(context.Background(), "query 4", nil)
	wantRefusal(t, err, RuleTurnCount)
	if len(g.ref.calls) != 3 {
		t.Fatalf("referee asked %d times, want 3", len(g.ref.calls))
	}
}

// searchFixture is a journal with one accepted call per Search, and search
// tools over a scripted searcher.
type searchFixture struct {
	t     *testing.T
	j     *journal.Store
	att   string
	calls int
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	db, err := journal.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &searchFixture{t: t, j: journal.NewStore(db)}
	err = f.j.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun([]byte(`{}`))
		if err != nil {
			return err
		}
		turn, err := tx.CreateTurn(run, "review", nil)
		if err != nil {
			return err
		}
		f.att, err = tx.BeginAttempt(turn, "author", "author", 1, nil, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// run journals a web_search call with the raw arguments and executes it.
func (f *searchFixture) run(ws *WebSearchTools, raw string, recheck func() error) (Result, journal.ToolCall, error) {
	f.t.Helper()
	var id string
	err := f.j.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordToolCall(f.att, f.calls, fmt.Sprintf("p%d", f.calls), NameWebSearch, GroupWeb, []byte(raw))
		id = c.ID
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.calls++
	if recheck == nil {
		recheck = func() error { return nil }
	}
	res, err := ws.Search(context.Background(), id, json.RawMessage(raw), recheck)
	var tc journal.ToolCall
	if jerr := f.j.Tx(func(tx *journal.Tx) (err error) { tc, err = tx.GetToolCall(id); return err }); jerr != nil {
		f.t.Fatal(jerr)
	}
	return res, tc, err
}

// scriptedSearcher answers Search from a function.
type scriptedSearcher func(ctx context.Context, query string, recheck func() error) (*http.Response, error)

func (s scriptedSearcher) Search(ctx context.Context, query string, recheck func() error) (*http.Response, error) {
	return s(ctx, query, recheck)
}

// response is a provider reply with the given status and body.
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

// wantJournaled checks the call's journaled result against what was returned.
func wantJournaled(t *testing.T, j *journal.Store, tc journal.ToolCall, res Result, status, code string) {
	t.Helper()
	if tc.Status != status || tc.Result == nil || tc.Result.ErrorCode != code {
		t.Fatalf("call %+v result %+v, want %s %q", tc, tc.Result, status, code)
	}
	body, err := j.Content(tc.Result.ResultSHA256)
	if err != nil || string(body) != string(res.Content) {
		t.Fatalf("journaled %q err %v, returned %q", body, err, res.Content)
	}
}

// An allowed search journals the provider's raw body against the call as the
// executed result, bounded: a body over the limit is an error stating it, a
// non-2xx reply an http_error.
func TestWebSearchToolsJournalsBoundedRawResult(t *testing.T) {
	f := newSearchFixture(t)
	body := `[{"title":"Go","url":"https://go.dev","extract":"The Go language"}]`
	ws := NewWebSearchTools(f.j, scriptedSearcher(func(context.Context, string, func() error) (*http.Response, error) {
		return response(200, body), nil
	}), 0)
	res, tc, err := f.run(ws, `{"query":"go"}`, nil)
	if err != nil || res.ErrorCode != "" || string(res.Content) != body {
		t.Fatalf("res %+v err %v", res, err)
	}
	wantJournaled(t, f.j, tc, res, journal.ToolCallExecuted, "")

	small := NewWebSearchTools(f.j, scriptedSearcher(func(context.Context, string, func() error) (*http.Response, error) {
		return response(200, body), nil
	}), 10)
	res, tc, err = f.run(small, `{"query":"go"}`, nil)
	if err != nil || res.ErrorCode != ErrCodeTooLarge || !strings.Contains(string(res.Content), "10 bytes") {
		t.Fatalf("res %+v err %v", res, err)
	}
	wantJournaled(t, f.j, tc, res, journal.ToolCallError, ErrCodeTooLarge)

	failing := NewWebSearchTools(f.j, scriptedSearcher(func(context.Context, string, func() error) (*http.Response, error) {
		return response(503, "down"), nil
	}), 0)
	res, tc, err = f.run(failing, `{"query":"go"}`, nil)
	if err != nil || res.ErrorCode != ErrCodeHTTPStatus || !strings.Contains(string(res.Content), "503") {
		t.Fatalf("res %+v err %v", res, err)
	}
	wantJournaled(t, f.j, tc, res, journal.ToolCallError, ErrCodeHTTPStatus)
}

// A refusal is journaled as an error result naming the rule; bad arguments
// never reach the searcher; a plain failure after the checks is search_error.
func TestWebSearchToolsJournalsRefusalWithRule(t *testing.T) {
	f := newSearchFixture(t)
	called := 0
	ws := NewWebSearchTools(f.j, scriptedSearcher(func(_ context.Context, q string, _ func() error) (*http.Response, error) {
		called++
		if q == "down" {
			return nil, errors.New("connection reset")
		}
		return nil, refusal(RuleCanary, "URL carries bytes from a file read this turn")
	}), 0)
	res, tc, err := f.run(ws, `{"query":"sk-live"}`, nil)
	if err != nil || res.ErrorCode != ErrCodeRefused || !strings.Contains(string(res.Content), RuleCanary) {
		t.Fatalf("res %+v err %v", res, err)
	}
	wantJournaled(t, f.j, tc, res, journal.ToolCallError, ErrCodeRefused)

	for _, raw := range []string{`{"query":""}`, `{"query":"   "}`, `{"q":"x"}`, `{"query":1}`} {
		res, tc, err := f.run(ws, raw, nil)
		if err != nil || res.ErrorCode != ErrCodeBadArguments {
			t.Fatalf("%s: res %+v err %v", raw, res, err)
		}
		wantJournaled(t, f.j, tc, res, journal.ToolCallError, ErrCodeBadArguments)
	}
	if called != 1 {
		t.Fatalf("searcher called %d times, want once: bad arguments never reach it", called)
	}

	res, tc, err = f.run(ws, `{"query":"down"}`, nil)
	if err != nil || res.ErrorCode != ErrCodeSearch {
		t.Fatalf("res %+v err %v", res, err)
	}
	wantJournaled(t, f.j, tc, res, journal.ToolCallError, ErrCodeSearch)
}

// When the step has ended during a wait, the call is abandoned: the recheck's
// error comes back and nothing is journaled, whether the searcher returned
// the recheck's error, a failure, or a response.
func TestWebSearchToolsAbandonsWhenRecheckFails(t *testing.T) {
	f := newSearchFixture(t)
	stale := errors.New("step ended")
	body := "late result"
	for name, s := range map[string]scriptedSearcher{
		"recheck inside": func(_ context.Context, _ string, recheck func() error) (*http.Response, error) { return nil, recheck() },
		"failure":        func(context.Context, string, func() error) (*http.Response, error) { return nil, errors.New("reset") },
		"referee refusal": func(context.Context, string, func() error) (*http.Response, error) {
			return nil, refusal(RuleReferee, "deny")
		},
		"response in hand": func(context.Context, string, func() error) (*http.Response, error) {
			return response(200, body), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			ws := NewWebSearchTools(f.j, s, 0)
			res, tc, err := f.run(ws, `{"query":"go"}`, func() error { return stale })
			if !errors.Is(err, stale) || res.ErrorCode != "" || len(res.Content) != 0 {
				t.Fatalf("res %+v err %v, want the recheck's error and no result", res, err)
			}
			if tc.Result != nil || tc.Status != journal.ToolCallAccepted {
				t.Fatalf("call %+v journaled a result although the step ended", tc)
			}
		})
	}
}

// QueryURL renders the query as the one query value of a URL on the provider
// host, so the canary and shape rules see it as they see a fetch URL.
func TestQueryURL(t *testing.T) {
	u := QueryURL(searchHost, "go testing & more")
	if u != "https://search.test/?q=go+testing+%26+more" {
		t.Fatalf("QueryURL = %s", u)
	}
	s := screenerWith(t, "secret value "+canaryText)
	wantRule(t, s.Screen(QueryURL(searchHost, "find "+canaryText)), RuleCanary)
	wantAllowed(t, s.Screen(QueryURL(searchHost, "find something else")))
}
