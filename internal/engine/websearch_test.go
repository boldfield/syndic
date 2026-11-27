package engine_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

const searchSecretValue = "sekrit-credential-value"

const searchTOML = `
[tools.web.search]
provider = "fakesearch"
host = "search.example.com"
auth = { source = "env", name = "SEARCH_KEY" }
`

// fakeSearchProvider is a test-only provider: it describes a request to its
// own host and ships in no production code.
type fakeSearchProvider struct{}

func (fakeSearchProvider) Name() string { return "fakesearch" }
func (fakeSearchProvider) Host() string { return "search.example.com" }
func (fakeSearchProvider) NewRequest(ctx context.Context, query, credential string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://search.example.com/?q="+query, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	return req, nil
}

// recordingResolver counts name lookups and answers with addrs.
type recordingResolver struct {
	mu    sync.Mutex
	hosts []string
	addrs []netip.Addr
}

func (r *recordingResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, host)
	return r.addrs, nil
}

func (r *recordingResolver) lookups() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hosts)
}

// recordingSecrets counts credential reads.
type recordingSecrets struct {
	mu sync.Mutex
	n  int
}

func (s *recordingSecrets) Resolve(config.SecretRef) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return searchSecretValue, nil
}

func (s *recordingSecrets) reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func offeredNames(reqs []transport.CompleteRequest) map[string]bool {
	out := map[string]bool{}
	for _, r := range reqs {
		for _, d := range r.Tools {
			out[d.Function.Name] = true
		}
	}
	return out
}

func searchScript(query string, replies ...fake.Step) []fake.Step {
	args := `{"query":"` + query + `"}`
	script := []fake.Step{toolStepWith(call("s1", tools.NameWebSearch, args))}
	script = append(script, replies...)
	return append(script, enginetest.Reply("final answer"))
}

func runSearch(t *testing.T, opts []enginetest.Option, script []fake.Step) (*enginetest.Harness, string) {
	t.Helper()
	opts = append(opts, refereeOpts()...)
	h := enginetest.New(t, refereeRoster(searchTOML), map[string][]fake.Step{"author": script}, opts...)
	return h, submitWeb(t, h)
}

func submitWeb(t *testing.T, h *enginetest.Harness) string {
	t.Helper()
	reply, err := h.Engine.Submit(toolReq("s1", "web", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	return reply.TurnID
}

func TestWebSearchNotOfferedWithoutProvider(t *testing.T) {
	// [tools.web.search] is configured and the referee usable, but no provider
	// was injected: neither web tool is offered, and a call is not_offered.
	h, turn := runSearch(t, nil, searchScript("cats"))
	wantEnd(t, h, turn, engine.StateSoloComplete, "solo_ok")
	names := offeredNames(requestsOf(h, "author"))
	if names[tools.NameWebSearch] || names[tools.NameWebFetch] {
		t.Fatalf("web tools offered with no provider: %v", names)
	}
}

func TestWebSearchNotOfferedForOtherProviderName(t *testing.T) {
	h := enginetest.New(t, refereeRoster(strings.Replace(searchTOML, "fakesearch", "othervendor", 1)),
		map[string][]fake.Step{"author": {enginetest.Reply("answer")}},
		append(refereeOpts(), enginetest.WithSearch(fakeSearchProvider{}, &recordingResolver{}, &recordingSecrets{}))...)
	turn := submitWeb(t, h)
	wantEnd(t, h, turn, engine.StateSoloComplete, "solo_ok")
	if names := offeredNames(requestsOf(h, "author")); names[tools.NameWebSearch] {
		t.Fatalf("web_search offered for an unsupported provider name: %v", names)
	}
}

func TestWebSearchOfferedDispatchedAndJournaled(t *testing.T) {
	res, secrets := &recordingResolver{}, &recordingSecrets{}
	opts := []enginetest.Option{enginetest.WithSearch(fakeSearchProvider{}, res, secrets)}
	h := enginetest.New(t, refereeRoster(searchTOML), map[string][]fake.Step{"author": searchScript("cats", allowReply())}, append(opts, refereeOpts()...)...)
	h.Engine.SetSearchClient(func(_ *tools.URLScreener, ref tools.Referee) (tools.SearchClient, error) {
		return clientFunc(func(ctx context.Context, q string) (*http.Response, error) {
			if ok, _, err := ref.Allow(ctx, q); err != nil || !ok {
				t.Errorf("referee: %v %v", ok, err)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("raw results for " + q))}, nil
		}), nil
	})
	turn := submitWeb(t, h)
	wantEnd(t, h, turn, engine.StateSoloComplete, "solo_ok")

	names := offeredNames(requestsOf(h, "author"))
	if !names[tools.NameWebSearch] || names[tools.NameWebFetch] {
		t.Fatalf("offered %v, want web_search and no web_fetch", names)
	}
	calls := toolCalls(t, h, turn)
	if len(calls) != 1 || calls[0].Status != journal.ToolCallExecuted {
		t.Fatalf("calls %+v, want one executed web_search", calls)
	}
	content, err := h.Store.Content(calls[0].Result.ResultSHA256)
	if err != nil || string(content) != "raw results for cats" {
		t.Fatalf("journaled result %q, %v", content, err)
	}
	if n := len(refereeAttempts(t, h, turn)); n != 1 {
		t.Fatalf("%d referee attempts, want 1", n)
	}
}

type clientFunc func(ctx context.Context, q string) (*http.Response, error)

func (f clientFunc) Search(ctx context.Context, q string) (*http.Response, error) { return f(ctx, q) }

// A query the referee denies sends nothing: the provider host is not resolved,
// no connection is made and the credential is not read.
func TestWebSearchRefusedQuerySendsNoDNSOrNetwork(t *testing.T) {
	res, secrets := &recordingResolver{addrs: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, &recordingSecrets{}
	deny := enginetest.Reply("deny the query is not about the task")
	h, turn := runSearch(t, []enginetest.Option{enginetest.WithSearch(fakeSearchProvider{}, res, secrets)}, searchScript("cats", deny))
	wantEnd(t, h, turn, engine.StateSoloComplete, "solo_ok")
	calls := toolCalls(t, h, turn)
	if len(calls) != 1 || calls[0].Status != journal.ToolCallError || calls[0].Result.ErrorCode != tools.ErrCodeRefused {
		t.Fatalf("calls %+v, want one refused error", calls)
	}
	if res.lookups() != 0 || secrets.reads() != 0 {
		t.Fatalf("refused query: %d lookups, %d secret reads", res.lookups(), secrets.reads())
	}
}

// A provider host resolving to a private address is refused after one lookup,
// before any credential read or connection; the credential is in no journal
// row.
func TestWebSearchPrivateAddressRefusedCredentialNeverJournaled(t *testing.T) {
	res, secrets := &recordingResolver{addrs: []netip.Addr{netip.MustParseAddr("10.0.0.5")}}, &recordingSecrets{}
	h, turn := runSearch(t, []enginetest.Option{enginetest.WithSearch(fakeSearchProvider{}, res, secrets)}, searchScript("cats", allowReply()))
	wantEnd(t, h, turn, engine.StateSoloComplete, "solo_ok")
	calls := toolCalls(t, h, turn)
	if len(calls) != 1 || calls[0].Status != journal.ToolCallError {
		t.Fatalf("calls %+v, want one error", calls)
	}
	if res.lookups() != 1 || secrets.reads() != 0 {
		t.Fatalf("%d lookups, %d secret reads; want 1 and 0", res.lookups(), secrets.reads())
	}
	for _, c := range calls {
		if blob, _ := h.Store.Content(c.Result.ResultSHA256); bytes.Contains(blob, []byte(searchSecretValue)) {
			t.Fatal("credential in a journaled result")
		}
	}
}
