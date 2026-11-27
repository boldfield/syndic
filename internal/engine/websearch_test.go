package engine_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// End-to-end tests of web_search (v5 delta section 6, pre-send check 6): a
// configured, injected provider is offered and dispatched through a turn;
// without one the tool is not offered; a refused query causes no referee
// question, no resolution, no connection and no credential read; the
// credential never reaches the journal; the bounded raw result is journaled
// against the call.

const (
	searchHost  = "search.test"
	searchCred  = "key-7f3a9c0d-secret-credential"
	searchBody  = `[{"title":"Go","url":"https://go.dev","extract":"The Go language"}]`
	searchAddr  = "93.184.216.34"
	longLine    = "hello from a very long file line"
	longContent = longLine + "\n"
)

// searchRoster is refereeRoster (the local endpoint names refmodel as its
// referee, preset web gives the author read and web) with the search table,
// a web-only solo preset and a review preset with write.
func searchRoster(extra string) string {
	return refereeRoster(`
[presets.webonly]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["web"]

[presets.webreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "write", "web"]
` + extra)
}

const searchTable = `
[tools.web.search]
provider = "fake"
host = "` + searchHost + `"
auth = { source = "env", name = "SEARCH_KEY" }
`

// fakeProvider is the test provider: GET https://<host>/search?q=<query>
// with the credential in a header. The production engine has none.
type fakeProvider struct{}

func (fakeProvider) Name() string { return "fake" }

func (fakeProvider) Request(ctx context.Context, host, query, credential string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/search?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Search-Key", credential)
	return req, nil
}

// recordingSecrets counts every credential read.
type recordingSecrets struct {
	reads atomic.Int32
	refs  []config.SecretRef
	mu    sync.Mutex
}

func (r *recordingSecrets) Resolve(ref config.SecretRef) (string, error) {
	r.reads.Add(1)
	r.mu.Lock()
	r.refs = append(r.refs, ref)
	r.mu.Unlock()
	return searchCred, nil
}

// searchNet is a recording network: the resolver records each lookup and the
// number of adapter calls the author's endpoint had seen by then, the dialer
// records each address and carries the connection to the provider server.
type searchNet struct {
	h       *enginetest.Harness
	server  *providerServer
	mu      sync.Mutex
	lookups []string
	callsAt []int // adapter calls to the author's endpoint at each lookup
	dials   []string
	addrs   []string
}

func (n *searchNet) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lookups = append(n.lookups, host)
	n.callsAt = append(n.callsAt, len(n.h.Adapters["author"].Calls()))
	var out []netip.Addr
	for _, a := range n.addrs {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

func (n *searchNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.mu.Lock()
	n.dials = append(n.dials, addr)
	n.mu.Unlock()
	if n.server == nil {
		return nil, errors.New("no provider server")
	}
	var d net.Dialer
	return d.DialContext(ctx, network, n.server.ts.Listener.Addr().String())
}

func (n *searchNet) network() *tools.SearchNetwork {
	return &tools.SearchNetwork{
		Resolver:  n,
		Dial:      n.dial,
		TLSConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the test server's certificate is self-signed
	}
}

func (n *searchNet) counts() (lookups, dials int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.lookups), len(n.dials)
}

// providerServer stands in for the provider: a TLS server that records each
// request and answers body, after waiting on block when set.
type providerServer struct {
	ts    *httptest.Server
	block chan struct{}
	mu    sync.Mutex
	seen  []*http.Request
	got   chan struct{} // closed once the first request arrived
}

func newProviderServer(t *testing.T, body string) *providerServer {
	t.Helper()
	ps := &providerServer{got: make(chan struct{})}
	var once sync.Once
	ps.ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.mu.Lock()
		ps.seen = append(ps.seen, r.Clone(context.Background()))
		ps.mu.Unlock()
		once.Do(func() { close(ps.got) })
		if ps.block != nil {
			<-ps.block
		}
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

// searchHarness is a harness with the fake provider injected, the recording
// network and secrets, and the referee's model probed.
type searchHarness struct {
	*enginetest.Harness
	net     *searchNet
	secrets *recordingSecrets
	server  *providerServer
}

func newSearchHarness(t *testing.T, roster string, scripts map[string][]fake.Step, opts ...enginetest.Option) *searchHarness {
	t.Helper()
	sh := &searchHarness{secrets: &recordingSecrets{}, server: newProviderServer(t, searchBody)}
	sh.net = &searchNet{server: sh.server, addrs: []string{searchAddr}}
	opts = append(append(refereeOpts(),
		enginetest.WithToolCalls("reviewer", transport.TriYes),
		enginetest.WithSearchProvider(fakeProvider{}),
		enginetest.WithSearchNetwork(sh.net.network()),
		enginetest.WithSecrets(sh.secrets)), opts...)
	sh.Harness = enginetest.New(t, roster, scripts, opts...)
	sh.net.h = sh.Harness
	return sh
}

func searchCall(id, query string) transport.ToolCall {
	args, _ := json.Marshal(map[string]string{"query": query})
	return call(id, tools.NameWebSearch, string(args))
}

func writeCall(id, path, content string) transport.ToolCall {
	args, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return call(id, tools.NameWriteFile, string(args))
}

func denyReply() fake.Step { return enginetest.Reply("deny the query does not serve the question") }

// workspaceWith is a workspace holding the given files.
func workspaceWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func sp(s string) *string { return &s }

func toolNames(defs []transport.Tool) []string {
	var names []string
	for _, d := range defs {
		names = append(names, d.Function.Name)
	}
	return names
}

func searchOffOf(t *testing.T, h *enginetest.Harness, participant string) string {
	t.Helper()
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Participants {
		if p.ID == participant {
			if p.SearchOff == nil {
				return ""
			}
			return *p.SearchOff
		}
	}
	t.Fatalf("participant %s not in status", participant)
	return ""
}

// wantNothingSent checks that the turn put no question to the referee and
// that no lookup, connection or credential read happened.
func (sh *searchHarness) wantNothingSent(t *testing.T, turnID string) {
	t.Helper()
	lookups, dials := sh.net.counts()
	if n := len(refereeAttempts(t, sh.Harness, turnID)); n != 0 || lookups != 0 || dials != 0 || sh.secrets.reads.Load() != 0 {
		t.Fatalf("referee attempts %d lookups %d dials %d credential reads %d; a refused query sends nothing", n, lookups, dials, sh.secrets.reads.Load())
	}
}

// wantRefereeOnly checks that the turn's query reached the referee exactly
// once and went no further.
func (sh *searchHarness) wantRefereeOnly(t *testing.T, turnID string) {
	t.Helper()
	lookups, dials := sh.net.counts()
	if n := len(refereeAttempts(t, sh.Harness, turnID)); n != 1 || lookups != 0 || dials != 0 || sh.secrets.reads.Load() != 0 {
		t.Fatalf("referee attempts %d lookups %d dials %d credential reads %d; want the referee asked once and nothing sent", n, lookups, dials, sh.secrets.reads.Load())
	}
}

// journalContains reports whether any column of any table of the journal
// holds needle.
func journalContains(t *testing.T, h *enginetest.Harness, needle string) bool {
	t.Helper()
	db := h.DB.Conn()
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	_ = tables.Close()
	for _, table := range names {
		if tableContains(t, db, table, needle) {
			return true
		}
	}
	return false
}

func tableContains(t *testing.T, db *sql.DB, table, needle string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM "` + table + `"`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			switch x := v.(type) {
			case string:
				if strings.Contains(x, needle) {
					return true
				}
			case []byte:
				if strings.Contains(string(x), needle) {
					return true
				}
			}
		}
	}
	return false
}

// Without [tools.web.search] a preset with web offers neither web_search nor
// web_fetch (which has no effect yet): the author receives the read tools
// only, a web_search call is answered not_offered, and the participants
// reply names the reason.
func TestWebSearchNotOfferedWithoutConfiguredProvider(t *testing.T) {
	sh := newSearchHarness(t, searchRoster(""), map[string][]fake.Step{
		"author": {toolStepWith(searchCall("c1", "go testing")), enginetest.Reply("final answer")},
	})
	if got := searchOffOf(t, sh.Harness, "author"); got != "no_search_provider" {
		t.Fatalf("search_off = %q, want no_search_provider", got)
	}
	reply, err := sh.Engine.Submit(toolReq("s1", "web", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	if names := toolNames(requestsOf(sh.Harness, "author")[0].Tools); strings.Join(names, ",") != "read_file,list_dir,search_files" {
		t.Fatalf("offered %v, want the read tools only", names)
	}
	wantCalls(t, toolCalls(t, sh.Harness, reply.TurnID), [2]string{journal.ToolCallError, "not_offered"})
	sh.wantNothingSent(t, reply.TurnID)
}

// A configured table whose provider this build does not supply, or whose
// host fails the allowlist, leaves web_search unoffered with the reason
// shown; a bare table is not a provider.
func TestWebSearchNotOfferedWithoutSupportedProviderOrAllowedHost(t *testing.T) {
	unsupported := enginetest.New(t, searchRoster(searchTable), map[string][]fake.Step{
		"author": {toolStepWith(searchCall("c1", "go testing")), enginetest.Reply("final answer")},
	}, refereeOpts()...)
	if got := searchOffOf(t, unsupported, "author"); got != "search_provider_unsupported:fake" {
		t.Fatalf("search_off = %q, want search_provider_unsupported:fake", got)
	}
	// A web-only author is then offered nothing, and its tool step ends the
	// turn as one from a preset without executable tools does.
	reply, err := unsupported.Engine.Submit(toolReq("s1", "webonly", ""))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, unsupported, reply.TurnID, engine.StateIncomplete, "tool_step:not_offered")
	if n := len(requestsOf(unsupported, "author")[0].Tools); n != 0 {
		t.Fatalf("web-only preset offered %d tools without a supported provider", n)
	}

	offHost := newSearchHarness(t, searchRoster(searchTable+"\n[tools.web]\nallowed_hosts = [\"*.example.com\"]\n"), nil)
	if got := searchOffOf(t, offHost.Harness, "author"); got != "search_host_refused:allowlist" {
		t.Fatalf("search_off = %q, want search_host_refused:allowlist", got)
	}
	if reason := offHost.Engine.SearchOffReason(); reason != "search_host_refused:allowlist" {
		t.Fatalf("SearchOffReason = %q", reason)
	}
}

// With a configured, injected provider the author is offered web_search and
// not web_fetch; its query goes to the referee, the host is resolved once
// after the allow, the connection is pinned to the validated address, the
// credential travels in a header to the configured host, and the provider's
// raw body is journaled executed against the call and carried to the next
// attempt. The credential appears nowhere in the journal.
func TestWebSearchOfferedQueryDispatchedAndJournaled(t *testing.T) {
	sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
		"author": {toolStepWith(searchCall("c1", "go testing")), allowReply(), enginetest.Reply("final answer")},
	})
	if got := searchOffOf(t, sh.Harness, "author"); got != "" {
		t.Fatalf("search_off = %q, want offered", got)
	}
	reply, err := sh.Engine.Submit(toolReq("s1", "web", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")

	reqs := requestsOf(sh.Harness, "author")
	if len(reqs) != 3 {
		t.Fatalf("%d requests to the endpoint, want author, referee, author", len(reqs))
	}
	if names := toolNames(reqs[0].Tools); strings.Join(names, ",") != "read_file,list_dir,search_files,web_search" {
		t.Fatalf("offered %v, want the read tools and web_search, never web_fetch", names)
	}
	if reqs[1].Envelope.Phase != "referee" || !strings.Contains(reqs[1].Messages[len(reqs[1].Messages)-1].Content, `"web_search" "go testing"`) {
		t.Fatalf("referee request %+v, want the search put as web_search with its query", reqs[1])
	}

	calls := toolCalls(t, sh.Harness, reply.TurnID)
	wantCalls(t, calls, [2]string{journal.ToolCallExecuted, ""})
	body, err := sh.Store.Content(calls[0].Result.ResultSHA256)
	if err != nil || string(body) != searchBody {
		t.Fatalf("journaled result %q err %v, want the provider's raw body", body, err)
	}
	var carried bool
	for _, m := range reqs[2].Messages {
		carried = carried || (m.Role == "tool" && m.ToolCallID == "c1" && strings.Contains(m.Content, searchBody))
	}
	if !carried {
		t.Fatal("the next attempt does not carry the result")
	}

	sh.net.mu.Lock()
	lookups, callsAt, dials := sh.net.lookups, sh.net.callsAt, sh.net.dials
	sh.net.mu.Unlock()
	if len(lookups) != 1 || lookups[0] != searchHost || callsAt[0] != 2 {
		t.Fatalf("lookups %v after %v adapter calls; want one lookup of the provider host after the referee's reply", lookups, callsAt)
	}
	if len(dials) != 1 || dials[0] != searchAddr+":443" {
		t.Fatalf("dials %v, want the validated address", dials)
	}
	if sh.secrets.reads.Load() != 1 || sh.secrets.refs[0] != (config.SecretRef{Source: "env", Name: "SEARCH_KEY"}) {
		t.Fatalf("credential reads %d refs %v, want one read of the configured reference", sh.secrets.reads.Load(), sh.secrets.refs)
	}
	seen := sh.server.requests()
	if len(seen) != 1 || seen[0].Host != searchHost || seen[0].URL.Query().Get("q") != "go testing" || seen[0].Header.Get("X-Search-Key") != searchCred {
		t.Fatalf("provider saw %+v", seen)
	}
	if journalContains(t, sh.Harness, searchCred) {
		t.Fatal("the credential is in the journal")
	}
	evs := refereeEvents(t, sh.Harness)
	if len(evs) != 1 || evs[0]["verdict"] != "allow" || evs[0]["call_id"] != calls[0].ID {
		t.Fatalf("decision events %+v", evs)
	}
}

// A web-only preset needs no workspace: web_search is offered and executes
// in a turn without one.
func TestWebSearchRunsWithoutWorkspace(t *testing.T) {
	sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
		"author": {toolStepWith(searchCall("c1", "go testing")), allowReply(), enginetest.Reply("final answer")},
	})
	reply, err := sh.Engine.Submit(toolReq("s1", "webonly", ""))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	if names := toolNames(requestsOf(sh.Harness, "author")[0].Tools); strings.Join(names, ",") != "web_search" {
		t.Fatalf("offered %v, want web_search alone", names)
	}
	wantCalls(t, toolCalls(t, sh.Harness, reply.TurnID), [2]string{journal.ToolCallExecuted, ""})
	if lookups, dials := sh.net.counts(); lookups != 1 || dials != 1 {
		t.Fatalf("lookups %d dials %d", lookups, dials)
	}
}

// A query carrying sixteen bytes read from a file this turn is refused by the
// canary: the call is answered refused naming the rule, and no referee
// question, lookup, connection or credential read follows.
func TestWebSearchQueryCarryingReadBytesRefusedBeforeReferee(t *testing.T) {
	dir := workspaceWith(t, map[string]string{"notes.txt": longContent})
	sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
		"author": {
			toolStepWith(call("c1", tools.NameReadFile, `{"path":"notes.txt"}`)),
			toolStepWith(searchCall("c2", "very long file line")),
			enginetest.Reply("final answer"),
		},
	})
	reply, err := sh.Engine.Submit(toolReq("s1", "web", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	calls := toolCalls(t, sh.Harness, reply.TurnID)
	wantCalls(t, calls, [2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
	text, err := sh.Store.Content(calls[1].Result.ResultSHA256)
	if err != nil || !strings.Contains(string(text), tools.RuleCanary) {
		t.Fatalf("refusal text %q err %v, want the canary rule named", text, err)
	}
	sh.wantNothingSent(t, reply.TurnID)
}

// A referee deny refuses the query with the decision journaled and no
// resolution, connection or credential read; the turn goes on.
func TestWebSearchRefereeDenyResolvesNothing(t *testing.T) {
	sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
		"author": {toolStepWith(searchCall("c1", "go testing")), denyReply(), enginetest.Reply("final answer")},
	})
	reply, err := sh.Engine.Submit(toolReq("s1", "webonly", ""))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	calls := toolCalls(t, sh.Harness, reply.TurnID)
	wantCalls(t, calls, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
	text, _ := sh.Store.Content(calls[0].Result.ResultSHA256)
	if !strings.Contains(string(text), tools.RuleReferee) {
		t.Fatalf("refusal text %q, want the referee rule named", text)
	}
	sh.wantRefereeOnly(t, reply.TurnID)
	if evs := refereeEvents(t, sh.Harness); len(evs) != 1 || evs[0]["verdict"] != "deny" {
		t.Fatalf("decision events %+v", evs)
	}
}

// Staged content is canary material: the content of a write_file, the old
// and new text of an edit_file, and the base content of a staged path the
// author never read each refuse a query carrying their bytes before the
// referee.
func TestWebSearchQueryCarryingStagedBytesRefused(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		stage transport.ToolCall
		query string
	}{
		"write content": {
			stage: writeCall("c1", "n.txt", "brand new staged secret content xyz"),
			query: "new staged secret content",
		},
		"edit new text": {
			files: map[string]string{"e.txt": "short base\n"},
			stage: call("c1", tools.NameEditFile, `{"path":"e.txt","old":"short base","new":"replacement text that is long enough"}`),
			query: "replacement text that is long",
		},
		"edit old text": {
			files: map[string]string{"e.txt": longContent},
			stage: call("c1", tools.NameEditFile, `{"path":"e.txt","old":"`+longLine+`","new":"x"}`),
			query: "from a very long file",
		},
		"base of staged path": {
			files: map[string]string{"base.txt": "the original base content line\n"},
			stage: writeCall("c1", "base.txt", "x"),
			query: "original base content",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{"a.txt": "hello from a\n"}
			for k, v := range c.files {
				files[k] = v
			}
			sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
				"author":   {toolStepWith(c.stage), toolStepWith(searchCall("c2", c.query)), enginetest.Reply(authorText)},
				"reviewer": {approveStep()},
			})
			reply, err := sh.Engine.Submit(toolReq("s1", "webreview", workspaceWith(t, files)))
			if err != nil {
				t.Fatal(err)
			}
			wantEnd(t, sh.Harness, reply.TurnID, engine.StateApproved, "all_approved")
			wantCalls(t, toolCalls(t, sh.Harness, reply.TurnID), [2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
			sh.wantNothingSent(t, reply.TurnID)
		})
	}
}

// Model-authored arguments are not canary material: the path of a file the
// author read or wrote stays searchable and reaches the referee.
func TestWebSearchModelAuthoredPathsRemainSearchable(t *testing.T) {
	t.Run("read path", func(t *testing.T) {
		sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
			"author": {
				toolStepWith(call("c1", tools.NameReadFile, `{"path":"ordinary-search-topic-123.txt"}`)),
				toolStepWith(searchCall("c2", "ordinary-search-topic-123.txt")),
				denyReply(), enginetest.Reply("final answer"),
			},
		})
		reply, err := sh.Engine.Submit(toolReq("s1", "web", workspaceWith(t, map[string]string{"ordinary-search-topic-123.txt": "x"})))
		if err != nil {
			t.Fatal(err)
		}
		wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
		wantCalls(t, toolCalls(t, sh.Harness, reply.TurnID), [2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
		sh.wantRefereeOnly(t, reply.TurnID)
	})
	t.Run("write path", func(t *testing.T) {
		sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
			"author": {
				toolStepWith(writeCall("c1", "ordinary-write-topic-123.txt", "x")),
				toolStepWith(searchCall("c2", "ordinary-write-topic-123.txt")),
				denyReply(), enginetest.Reply(authorText),
			},
			"reviewer": {approveStep()},
		})
		reply, err := sh.Engine.Submit(toolReq("s1", "webreview", workspaceDir(t)))
		if err != nil {
			t.Fatal(err)
		}
		wantEnd(t, sh.Harness, reply.TurnID, engine.StateApproved, "all_approved")
		wantCalls(t, toolCalls(t, sh.Harness, reply.TurnID), [2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
		sh.wantRefereeOnly(t, reply.TurnID)
	})
}

// History is canary material only where it is file-derived: the answer of a
// file-tainted earlier turn refuses a later query carrying its bytes before
// the referee, while the answer of an untainted turn stays searchable.
func TestWebSearchHistoryBytesRefusedOnlyWhenTainted(t *testing.T) {
	t.Run("tainted answer", func(t *testing.T) {
		dir := workspaceWith(t, map[string]string{"notes.txt": longContent})
		sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
			"author": {
				toolStepWith(call("c1", tools.NameReadFile, `{"path":"notes.txt"}`)), enginetest.Reply("the file says " + longLine),
				toolStepWith(searchCall("c2", longLine)), enginetest.Reply("second answer"),
			},
		})
		first := submitSolo(t, sh.Harness, toolReq("s1", "web", dir))
		second := wire.SubmitRequest{SubmissionID: "s2", Input: "search it", Preset: sp("web"), Workspace: dir, ConversationID: &first.ConversationID}
		reply, err := sh.Engine.Submit(second)
		if err != nil {
			t.Fatal(err)
		}
		wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
		wantCalls(t, toolCalls(t, sh.Harness, reply.TurnID), [2]string{journal.ToolCallError, tools.ErrCodeRefused})
		sh.wantNothingSent(t, reply.TurnID)
	})
	t.Run("untainted answer", func(t *testing.T) {
		phrase := "ordinary assistant phrase with enough bytes"
		sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
			"author": {enginetest.Reply(phrase), toolStepWith(searchCall("c1", phrase)), denyReply(), enginetest.Reply("second answer")},
		})
		first := submitSolo(t, sh.Harness, toolReq("s1", "webonly", ""))
		reply, err := sh.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "search it", Preset: sp("webonly"), ConversationID: &first.ConversationID})
		if err != nil {
			t.Fatal(err)
		}
		wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
		wantCalls(t, toolCalls(t, sh.Harness, reply.TurnID), [2]string{journal.ToolCallError, tools.ErrCodeRefused})
		sh.wantRefereeOnly(t, reply.TurnID)
	})
}

// The change fields of a file-tainted earlier turn are canary material too: a
// query carrying a changed path's history line is refused before the referee.
func TestWebSearchQueryCarryingHistoryChangeFieldRefused(t *testing.T) {
	sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
		"author": {
			toolStepWith(writeCall("c1", "n.txt", "x")), enginetest.Reply(authorText),
			toolStepWith(searchCall("c2", "not applied n.txt")), enginetest.Reply("second answer"),
		},
		"reviewer": {approveStep()},
	})
	dir := workspaceDir(t)
	first, err := sh.Engine.Submit(toolReq("s1", "webreview", dir))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, first.TurnID, engine.StateApproved, "all_approved")
	if _, err := sh.Store.ChangeSetForTurn(first.TurnID); err != nil {
		t.Fatalf("first turn has no change set: %v", err)
	}
	reply, err := sh.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "search it", Preset: sp("web"), Workspace: dir, ConversationID: &first.ConversationID})
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	wantCalls(t, toolCalls(t, sh.Harness, reply.TurnID), [2]string{journal.ToolCallError, tools.ErrCodeRefused})
	sh.wantNothingSent(t, reply.TurnID)
}

// A call whose turn ends while it waits is abandoned with no effect: cancelled
// while the referee's decision is awaited, nothing is resolved or sent;
// cancelled while the provider's reply is awaited, no result is journaled.
func TestWebSearchAbandonedWhenStepEndsDuringWaits(t *testing.T) {
	t.Run("referee wait", func(t *testing.T) {
		var sh *searchHarness
		var release func()
		var once sync.Once
		hook := func(_, _, name string) {
			if name == tools.NameWebSearch {
				// The author's step has returned; the referee's call is next.
				once.Do(func() { release = sh.Hold("author") })
			}
		}
		sh = newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
			"author": {toolStepWith(searchCall("c1", "go testing")), allowReply(), enginetest.Reply("never sent")},
		}, enginetest.WithBeforeToolEffect(hook))
		reply, err := sh.Engine.Submit(toolReq("s1", "webonly", ""))
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the referee attempt", func() bool { return len(refereeAttempts(t, sh.Harness, reply.TurnID)) == 1 })
		if err := sh.Engine.Cancel(reply.TurnID); err != nil {
			t.Fatal(err)
		}
		release()
		if row := waitEnded(t, sh.Harness, reply.TurnID); row.TerminalState != engine.StateCancelled {
			t.Fatalf("turn ended %q %q, want cancelled", row.TerminalState, row.TerminalReason)
		}
		calls := toolCalls(t, sh.Harness, reply.TurnID)
		wantCalls(t, calls, [2]string{journal.ToolCallAbandoned, ""})
		if lookups, dials := sh.net.counts(); lookups != 0 || dials != 0 || sh.secrets.reads.Load() != 0 {
			t.Fatalf("lookups %d dials %d credential reads %d after the cancel", lookups, dials, sh.secrets.reads.Load())
		}
		if n := len(requestsOf(sh.Harness, "author")); n != 2 {
			t.Fatalf("%d requests, want the author's step and the referee's only", n)
		}
	})
	t.Run("network wait", func(t *testing.T) {
		sh := newSearchHarness(t, searchRoster(searchTable), map[string][]fake.Step{
			"author": {toolStepWith(searchCall("c1", "go testing")), allowReply(), enginetest.Reply("never sent")},
		})
		sh.server.block = make(chan struct{})
		reply, err := sh.Engine.Submit(toolReq("s1", "webonly", ""))
		if err != nil {
			t.Fatal(err)
		}
		<-sh.server.got
		if err := sh.Engine.Cancel(reply.TurnID); err != nil {
			t.Fatal(err)
		}
		if row := waitEnded(t, sh.Harness, reply.TurnID); row.TerminalState != engine.StateCancelled {
			t.Fatalf("turn ended %q %q, want cancelled", row.TerminalState, row.TerminalReason)
		}
		close(sh.server.block)
		calls := toolCalls(t, sh.Harness, reply.TurnID)
		wantCalls(t, calls, [2]string{journal.ToolCallAbandoned, ""})
		if calls[0].Result == nil || calls[0].Result.ResultSHA256 != "" {
			t.Fatalf("call %+v journaled a result after the cancel", calls[0])
		}
		if n := len(requestsOf(sh.Harness, "author")); n != 2 {
			t.Fatalf("%d requests, want no attempt after the cancel", n)
		}
	})
}

// The provider's reply is bounded by tools.max_result_bytes: a body over it
// is journaled as an error stating the bound, not truncated.
func TestWebSearchResultOverBoundIsError(t *testing.T) {
	sh := newSearchHarness(t, searchRoster(searchTable+"\n[tools]\nmax_result_bytes = 16\n"), map[string][]fake.Step{
		"author": {toolStepWith(searchCall("c1", "go testing")), allowReply(), enginetest.Reply("final answer")},
	})
	reply, err := sh.Engine.Submit(toolReq("s1", "webonly", ""))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	calls := toolCalls(t, sh.Harness, reply.TurnID)
	wantCalls(t, calls, [2]string{journal.ToolCallError, tools.ErrCodeTooLarge})
	text, _ := sh.Store.Content(calls[0].Result.ResultSHA256)
	if !strings.Contains(string(text), "16 bytes") {
		t.Fatalf("error text %q, want the bound stated", text)
	}
}

// reviewRoundsRoster is the search roster with revision rounds allowed, so a
// blocked candidate sends the author a revision round with its tools.
func reviewRoundsRoster() string {
	return strings.Replace(searchRoster(searchTable), "max_rounds = 0", "max_rounds = 2", 1)
}

// The diffs given to the reviewers are canary material: once a candidate has
// been reviewed, the revision-round author's query carrying sixteen bytes
// found only in the rendered diff is refused before the referee, with no
// lookup, connection or credential read. The run crosses a diff line
// boundary, so it is in the diff's rendering alone: not in the staged
// content, nor in a base, nor in any history.
func TestWebSearchQueryCarryingReviewerDiffBytesRefused(t *testing.T) {
	const content = "line one\nsecond line two\n"
	const query = "line one\n+second" // 16 bytes: the diff's "+line one\n+second line two"
	if len(query) != 16 || strings.Contains(content, query) {
		t.Fatalf("query %q must be sixteen bytes found in the diff and not in the content", query)
	}
	sh := newSearchHarness(t, reviewRoundsRoster(), map[string][]fake.Step{
		"author": {
			toolStepWith(writeCall("c1", "n.txt", content)), enginetest.Reply(authorText),
			toolStepWith(searchCall("c2", query)), enginetest.Reply(revisedText),
		},
		"reviewer": {blockStep("b1"), v2Verdict("1", "approve", "", "b1 withdrawn")},
	})
	reply, err := sh.Engine.Submit(toolReq("s1", "webreview", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateApproved, "all_approved")
	if info, err := sh.Store.GetRoundInfo(reply.TurnID); err != nil || info.CurrentRound != 1 {
		t.Fatalf("round info %+v err %v, want the query put in revision round 1", info, err)
	}
	calls := toolCalls(t, sh.Harness, reply.TurnID)
	wantCalls(t, calls, [2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
	text, err := sh.Store.Content(calls[1].Result.ResultSHA256)
	if err != nil || !strings.Contains(string(text), tools.RuleCanary) {
		t.Fatalf("refusal text %q err %v, want the canary rule named", text, err)
	}
	sh.wantNothingSent(t, reply.TurnID)
}

// The whole rendered diff is material, its headers included: a staged path's
// name is searchable in the round that staged it (the write's path is
// model-authored) and reaches the referee, but once the diff naming it has
// been given to a reviewer, the revision-round author's query for it is
// refused by the canary before the referee.
func TestWebSearchStagedPathBecomesCanaryMaterialWithTheDiff(t *testing.T) {
	const path = "ordinary-write-topic-123.txt"
	sh := newSearchHarness(t, reviewRoundsRoster(), map[string][]fake.Step{
		"author": {
			toolStepWith(writeCall("c1", path, "x")), toolStepWith(searchCall("c2", path)), denyReply(), enginetest.Reply(authorText),
			toolStepWith(searchCall("c3", path)), enginetest.Reply(revisedText),
		},
		"reviewer": {blockStep("b1"), v2Verdict("1", "approve", "", "b1 withdrawn")},
	})
	reply, err := sh.Engine.Submit(toolReq("s1", "webreview", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateApproved, "all_approved")
	calls := toolCalls(t, sh.Harness, reply.TurnID)
	wantCalls(t, calls, [2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallError, tools.ErrCodeRefused}, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
	first, _ := sh.Store.Content(calls[1].Result.ResultSHA256)
	second, _ := sh.Store.Content(calls[2].Result.ResultSHA256)
	if !strings.Contains(string(first), tools.RuleReferee) || !strings.Contains(string(second), tools.RuleCanary) {
		t.Fatalf("refusals %q then %q, want the referee's in round 0 and the canary's in the revision round", first, second)
	}
	// The one referee attempt is round 0's; the revision round's query sent
	// nothing.
	sh.wantRefereeOnly(t, reply.TurnID)
}

// reviewerSearchRoster is the search roster with the reviewer moved to an
// endpoint of its own, "lan", naming its own referee, so the referee a
// reviewer's query is put to is distinguishable from the author's: each
// endpoint's referee runs through the adapter of a participant on that
// endpoint. Presets: revweb gives the author read and write and the reviewer
// web; bothweb gives both roles web alone; revread gives the author read and
// web and the reviewer read alone.
func reviewerSearchRoster(t *testing.T) string {
	t.Helper()
	r := searchRoster(searchTable)
	edit := func(old, new string) {
		t.Helper()
		if !strings.Contains(r, old) {
			t.Fatalf("roster has no %q", old)
		}
		r = strings.Replace(r, old, new, 1)
	}
	edit("id = \"reviewer\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"", "id = \"reviewer\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"lan\"")
	edit("local_only = true", "local_only = false")
	edit("[presets.review]", `[[endpoints]]
name = "lan"
base_url = "http://192.168.1.20:11434/v1"
declared_location = "lan"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama3", "refmodel"]
referee_model = "refmodel"
tokens_per_byte_max = 1.0

[presets.revweb]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "write"]
reviewer_tools = ["web"]

[presets.bothweb]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["web"]
reviewer_tools = ["web"]

[presets.revread]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "web"]
reviewer_tools = ["read"]

[presets.review]`)
	// The reviewer's diffs and reads cross to the lan endpoint, which the
	// roster must name as a file-egress destination.
	return r + "\n[tools]\nfile_egress_destinations = [\"lan\"]\n"
}

// newReviewerSearchHarness is newSearchHarness over reviewerSearchRoster, with
// the lan endpoint's referee able to run.
func newReviewerSearchHarness(t *testing.T, scripts map[string][]fake.Step) *searchHarness {
	t.Helper()
	return newSearchHarness(t, reviewerSearchRoster(t), scripts, enginetest.WithModelToolCalls("lan", "refmodel", transport.TriYes))
}

// refereeRequestsOf is the referee-phase requests the participant's adapter
// saw: those of the referee of its endpoint.
func refereeRequestsOf(h *enginetest.Harness, participant string) []transport.CompleteRequest {
	var out []transport.CompleteRequest
	for _, req := range requestsOf(h, participant) {
		if req.Envelope.Phase == "referee" {
			out = append(out, req)
		}
	}
	return out
}

// A required reviewer whose preset gives it the web group is offered
// web_search, as the design gives the group to every role. Its query is
// decided by the referee of the reviewer's own endpoint, put and accounted
// as the reviewer's attempt; the provider is then dispatched, and the bounded
// raw result is journaled against the reviewer's call and carried into that
// reviewer's next request alone: the author never sees it.
func TestWebSearchReviewerQueryDecidedByItsOwnReferee(t *testing.T) {
	sh := newReviewerSearchHarness(t, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText)},
		"reviewer": {toolStepWith(searchCall("r1", "go testing")), allowReply(), approveStep()},
	})
	if got := searchOffOf(t, sh.Harness, "reviewer"); got != "" {
		t.Fatalf("reviewer search_off = %q, want offered", got)
	}
	reply, err := sh.Engine.Submit(toolReq("s1", "revweb", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateApproved, "all_approved")

	reqs := requestsOf(sh.Harness, "reviewer")
	if len(reqs) != 3 {
		t.Fatalf("%d reviewer requests, want the review, its referee and the continuation", len(reqs))
	}
	if names := toolNames(reqs[0].Tools); strings.Join(names, ",") != "web_search" {
		t.Fatalf("reviewer offered %v, want web_search alone", names)
	}
	if reqs[1].Envelope.Phase != "referee" || reqs[1].Envelope.Participant != "reviewer" || !strings.Contains(reqs[1].Messages[len(reqs[1].Messages)-1].Content, `"web_search" "go testing"`) {
		t.Fatalf("referee request %+v, want the reviewer's query put to the lan referee as the reviewer's attempt", reqs[1].Envelope)
	}
	if m := toolMessage(reqs[2], "r1"); m == nil || !strings.Contains(m.Content, searchBody) {
		t.Fatalf("the reviewer's continuation does not carry the result: %+v", reqs[2].Messages)
	}
	if n := len(refereeRequestsOf(sh.Harness, "author")); n != 0 {
		t.Fatalf("%d referee requests through the author's endpoint, want none: the reviewer's referee decides", n)
	}
	if requestsContain(requestsOf(sh.Harness, "author"), searchBody) {
		t.Fatal("the reviewer's result reached the author")
	}

	attempts := refereeAttempts(t, sh.Harness, reply.TurnID)
	if len(attempts) != 1 || attempts[0].Participant != "reviewer" {
		t.Fatalf("referee attempts %+v, want one accounted to the reviewer", attempts)
	}
	calls := reviewerCallsOf(t, sh.Harness, reply.TurnID, "reviewer")
	wantCalls(t, calls, [2]string{journal.ToolCallExecuted, ""})
	body, err := sh.Store.Content(calls[0].Result.ResultSHA256)
	if err != nil || string(body) != searchBody {
		t.Fatalf("journaled result %q err %v, want the provider's raw body against the reviewer's call", body, err)
	}
	if author := toolCalls(t, sh.Harness, reply.TurnID); len(author) != 0 {
		t.Fatalf("author calls %+v, want none", author)
	}
	evs := refereeEvents(t, sh.Harness)
	if len(evs) != 1 || evs[0]["verdict"] != "allow" || evs[0]["call_id"] != calls[0].ID {
		t.Fatalf("decision events %+v, want one allow of the reviewer's call", evs)
	}

	lookups, dials := sh.net.counts()
	if lookups != 1 || dials != 1 || sh.secrets.reads.Load() != 1 {
		t.Fatalf("lookups %d dials %d credential reads %d, want one of each", lookups, dials, sh.secrets.reads.Load())
	}
	seen := sh.server.requests()
	if len(seen) != 1 || seen[0].Host != searchHost || seen[0].URL.Query().Get("q") != "go testing" || seen[0].Header.Get("X-Search-Key") != searchCred {
		t.Fatalf("provider saw %+v", seen)
	}
	if journalContains(t, sh.Harness, searchCred) {
		t.Fatal("the credential is in the journal")
	}
}

// A reviewer's query carrying sixteen bytes of the diff it was given is
// refused by the canary before the referee: no referee attempt, no lookup,
// no connection and no credential read. The refusal is journaled against the
// reviewer's call with the rule named.
func TestWebSearchReviewerQueryCarryingDiffBytesRefused(t *testing.T) {
	const content = "line one\nsecond line two\n"
	const query = "line one\n+second" // 16 bytes found in the rendered diff alone
	if len(query) != 16 || strings.Contains(content, query) {
		t.Fatalf("query %q must be sixteen bytes found in the diff and not in the content", query)
	}
	sh := newReviewerSearchHarness(t, map[string][]fake.Step{
		"author":   {toolStepWith(writeCall("c1", "n.txt", content)), enginetest.Reply(authorText)},
		"reviewer": {toolStepWith(searchCall("r1", query)), approveStep()},
	})
	reply, err := sh.Engine.Submit(toolReq("s1", "revweb", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateApproved, "all_approved")
	if names := toolNames(requestsOf(sh.Harness, "reviewer")[0].Tools); strings.Join(names, ",") != "web_search" {
		t.Fatalf("reviewer offered %v, want web_search", names)
	}
	calls := reviewerCallsOf(t, sh.Harness, reply.TurnID, "reviewer")
	wantCalls(t, calls, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
	text, err := sh.Store.Content(calls[0].Result.ResultSHA256)
	if err != nil || !strings.Contains(string(text), tools.RuleCanary) {
		t.Fatalf("refusal text %q err %v, want the canary rule named", text, err)
	}
	sh.wantNothingSent(t, reply.TurnID)
}

// A preset whose reviewer_tools lack the web group gives the reviewer no
// web_search definition while the author, holding the group, is offered it;
// a reviewer call naming it is answered not_offered, with nothing put to any
// referee and nothing sent.
func TestWebSearchReviewerWithoutWebGroupIsNotOffered(t *testing.T) {
	sh := newReviewerSearchHarness(t, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText)},
		"reviewer": {toolStepWith(searchCall("r1", "go testing")), approveStep()},
	})
	reply, err := sh.Engine.Submit(toolReq("s1", "revread", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateApproved, "all_approved")
	if names := toolNames(requestsOf(sh.Harness, "author")[0].Tools); strings.Join(names, ",") != "read_file,list_dir,search_files,web_search" {
		t.Fatalf("author offered %v, want the read tools and web_search", names)
	}
	if names := toolNames(requestsOf(sh.Harness, "reviewer")[0].Tools); strings.Join(names, ",") != "read_file,list_dir,search_files" {
		t.Fatalf("reviewer offered %v, want the read tools only", names)
	}
	wantCalls(t, reviewerCallsOf(t, sh.Harness, reply.TurnID, "reviewer"), [2]string{journal.ToolCallError, "not_offered"})
	sh.wantNothingSent(t, reply.TurnID)
}

// The referee payload of a reviewer's call lists the reviewer's own earlier
// web calls and none of the author's: with both roles holding web, the
// author's allowed search this turn is absent from the reviewer's referee
// request, whose prior calls are none, and each decision is put to the
// caller's own endpoint referee.
func TestWebSearchReviewerRefereePayloadOmitsAuthorCalls(t *testing.T) {
	const authorQuery = "author topic query"
	const reviewerQuery = "reviewer topic query"
	sh := newReviewerSearchHarness(t, map[string][]fake.Step{
		"author":   {toolStepWith(searchCall("c1", authorQuery)), allowReply(), enginetest.Reply(authorText)},
		"reviewer": {toolStepWith(searchCall("r1", reviewerQuery)), allowReply(), approveStep()},
	})
	reply, err := sh.Engine.Submit(toolReq("s1", "bothweb", ""))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, sh.Harness, reply.TurnID, engine.StateApproved, "all_approved")
	wantCalls(t, toolCalls(t, sh.Harness, reply.TurnID), [2]string{journal.ToolCallExecuted, ""})
	wantCalls(t, reviewerCallsOf(t, sh.Harness, reply.TurnID, "reviewer"), [2]string{journal.ToolCallExecuted, ""})

	authorRef := refereeRequestsOf(sh.Harness, "author")
	reviewerRef := refereeRequestsOf(sh.Harness, "reviewer")
	if len(authorRef) != 1 || len(reviewerRef) != 1 {
		t.Fatalf("%d referee requests through the author's endpoint and %d through the reviewer's, want one each", len(authorRef), len(reviewerRef))
	}
	payload := reviewerRef[0].Messages[len(reviewerRef[0].Messages)-1].Content
	if reviewerRef[0].Envelope.Participant != "reviewer" || !strings.Contains(payload, `"web_search" "`+reviewerQuery+`"`) {
		t.Fatalf("reviewer's referee request %+v %q, want the reviewer's query as the reviewer's attempt", reviewerRef[0].Envelope, payload)
	}
	if strings.Contains(payload, authorQuery) || !strings.Contains(payload, "prior calls this turn:\nnone\n") {
		t.Fatalf("reviewer's referee payload %q, want no prior calls: the author's search is not the reviewer's", payload)
	}
	attempts := refereeAttempts(t, sh.Harness, reply.TurnID)
	var callers []string
	for _, a := range attempts {
		callers = append(callers, a.Participant)
	}
	if strings.Join(callers, ",") != "author,reviewer" {
		t.Fatalf("referee attempts accounted to %v, want the author's then the reviewer's", callers)
	}
	if lookups, dials := sh.net.counts(); lookups != 2 || dials != 2 || len(sh.server.requests()) != 2 {
		t.Fatalf("lookups %d dials %d provider requests %d, want two of each", lookups, dials, len(sh.server.requests()))
	}
}
