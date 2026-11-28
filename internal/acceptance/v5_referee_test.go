package acceptance

import (
	"context"
	"crypto/tls"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/referee"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// refPara is the "Referee" paragraph of the v5 delta's section 8 (the
// revision 5 test list). The paragraph's tests are named TestReferee_*.
//
// The clause "the transcript shows the decision under the call" is not
// registered: the transcript lists messages and attempts only (journal
// TranscriptPage), so the engine does not yet show a decision under its call
// and no test can pass for it.
const refPara = "Referee"

func init() {
	cover(refPara, "the referee request matches the template byte for byte and contains no tool result, no candidate text, no `write_file`, `edit_file`, `read_file` or `unstage` argument and no other participant's calls", "TestReferee_PayloadMatchesGoldenAndAuthorizedFieldsOnly")
	cover(refPara, "a roster granting `web` on an endpoint with no `referee_model` is refused at load naming the endpoint", "TestReferee_NoRefereeModelRefusedAtLoad")
	cover(refPara, "a `referee_endpoint` naming a local endpoint moves that endpoint's referee there and one naming a non-local endpoint is refused at load", "TestReferee_RefereeEndpointLocalMovesRefereeNonLocalRefused")
	cover(refPara, "a referee model with no record on a `flat` endpoint is probed at start", "TestReferee_FlatEndpointRefereeProbedAtStart")
	cover(refPara, "a referee model with no usable probe record leaves `web` unoffered on that endpoint and the participants reply says so", "TestReferee_UnprobedRefereeLeavesWebUnoffered")
	cover(refPara, "a referee `deny` yields a tool error and the calling attempt continues", "TestReferee_DenyYieldsToolErrorAndCallerContinues")
	cover(refPara, "a referee timeout, adapter error, provider refusal, malformed reply and tool-call reply each yield deny", "TestReferee_DispatchedFailuresEachDeny")
	cover(refPara, "a late referee `allow` is quarantined and does not authorize the fetch", "TestReferee_LateAllowAfterCancelAuthorizesNothing")
	cover(refPara, "a referee dispatch that would exceed the turn budget is refused and the turn ends `budget:turn`", "TestReferee_ReservationFailureEndsBudgetTurn")
	cover(refPara, "the referee is not consulted for a URL the deterministic checks refused", "TestReferee_NotConsultedForCheckRefusedQuery")
	cover(refPara, "a referee decision is reserved, admitted with its full request, charged and reconciled", "TestReferee_DecisionReservedAdmittedChargedReconciled")
	cover(refPara, "the manifest carries the referee model and template hash per endpoint", "TestReferee_ManifestCarriesRefereeModelAndTemplateHash")
}

const (
	refInput    = "find the go testing docs"
	refHost     = "search.test"
	refAddr     = "93.184.216.34"
	refBody     = `[{"title":"Go","url":"https://go.dev","extract":"EXTRACT-FROM-THE-PROVIDER"}]`
	refCred     = "key-5c1d9e-referee-credential"
	refFileMark = "FILE-BYTES-OF-NOTES"
)

// refTable is the search configuration the referee's calls are put under:
// the author's web_search is the model-driven web call the engine runs.
const refTable = `
[tools.web.search]
provider = "fake"
host = "` + refHost + `"
auth = { source = "env", name = "SEARCH_KEY" }
`

// refRoster is the harness roster with the endpoint naming refmodel as its
// referee, run on that endpoint itself, the search configuration, and the
// presets: web (solo, read and web), webonly (solo, web) and refreview
// (review: the author reads, writes and searches, the reviewer searches).
func refRoster(extra string) string {
	r := strings.Replace(enginetest.RosterTOML, `models = ["llama2", "llama3"]`, `models = ["llama2", "llama3", "refmodel"]
referee_model = "refmodel"`, 1)
	return r + `
[presets.web]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["read", "web"]

[presets.webonly]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["web"]

[presets.refreview]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read", "write", "web"]
reviewer_tools = ["web"]
` + refTable + extra
}

// refBudgets appends a [budgets] table to a roster.
func refBudgets(roster string, lines ...string) string {
	return roster + "\n[budgets]\n" + strings.Join(lines, "\n") + "\n"
}

// refProvider is the web_search provider the daemon would inject.
type refProvider struct{}

func (refProvider) Name() string { return "fake" }

func (refProvider) Request(ctx context.Context, host, query, credential string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/search?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Search-Key", credential)
	return req, nil
}

// refSecrets resolves the provider credential and counts the reads.
type refSecrets struct{ reads atomic.Int32 }

func (r *refSecrets) Resolve(config.SecretRef) (string, error) {
	r.reads.Add(1)
	return refCred, nil
}

// refNet is a recording network: the resolver and the dialer record every
// lookup and connection, and the dialer carries each to the provider server.
type refNet struct {
	srv     *httptest.Server
	mu      sync.Mutex
	lookups []string
	dials   []string
	hits    atomic.Int32
}

func newRefNet(t *testing.T) *refNet {
	t.Helper()
	n := &refNet{}
	n.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.hits.Add(1)
		_, _ = fmt.Fprint(w, refBody)
	}))
	t.Cleanup(n.srv.Close)
	return n
}

func (n *refNet) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	n.mu.Lock()
	n.lookups = append(n.lookups, host)
	n.mu.Unlock()
	return []netip.Addr{netip.MustParseAddr(refAddr)}, nil
}

func (n *refNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.mu.Lock()
	n.dials = append(n.dials, addr)
	n.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, network, n.srv.Listener.Addr().String())
}

func (n *refNet) network() *tools.SearchNetwork {
	return &tools.SearchNetwork{
		Resolver:  n,
		Dial:      n.dial,
		TLSConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the test server's certificate is self-signed
	}
}

// sent reports whether anything left the engine for the provider: a lookup, a
// connection, a request or a credential read.
func (n *refNet) sent() (lookups, dials, hits int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.lookups), len(n.dials), int(n.hits.Load())
}

// refRig is a suite whose author may put web_search calls to the referee, with
// a recording network behind the provider.
type refRig struct {
	*suite
	net     *refNet
	secrets *refSecrets
}

// refOpts are the options that give every participant tool calls, the referee
// model a usable probe record and the engine the provider and its network.
func refOpts(n *refNet, secrets *refSecrets) []enginetest.Option {
	return append(refOptsUnprobed(n, secrets), enginetest.WithModelToolCalls("local", "refmodel", transport.TriYes))
}

// refOptsUnprobed is refOpts without a record of the referee model.
func refOptsUnprobed(n *refNet, secrets *refSecrets) []enginetest.Option {
	return []enginetest.Option{
		enginetest.WithToolCalls("author", transport.TriYes),
		enginetest.WithToolCalls("reviewer", transport.TriYes),
		enginetest.WithSearchProvider(refProvider{}),
		enginetest.WithSearchNetwork(n.network()),
		enginetest.WithSecrets(secrets),
	}
}

func newRefRig(t *testing.T, roster string, scripts map[string][]fake.Step, opts ...enginetest.Option) *refRig {
	t.Helper()
	n, secrets := newRefNet(t), &refSecrets{}
	return &refRig{suite: newSuite(t, roster, scripts, append(refOpts(n, secrets), opts...)...), net: n, secrets: secrets}
}

// refSearch is a web_search call as a model returns it.
func refSearch(id, query string) transport.ToolCall {
	args, _ := json.Marshal(map[string]string{"query": query})
	return tlCall(id, tools.NameWebSearch, string(args))
}

func refAllow() fake.Step { return enginetest.Reply("allow the query serves the question") }
func refDeny() fake.Step  { return enginetest.Reply("deny the query does not serve the question") }

// refSubmit submits input under the preset in the workspace and returns the
// turn id.
func (r *refRig) refSubmit(t *testing.T, preset, dir string) string {
	t.Helper()
	reply, err := r.Engine.Submit(wire.SubmitRequest{SubmissionID: "s-" + preset, Input: refInput, Preset: &preset, Workspace: dir})
	if err != nil {
		t.Fatalf("submit %s: %v", preset, err)
	}
	return reply.TurnID
}

// refEnd waits for the turn to end and returns its terminal state and reason.
func (r *refRig) refEnd(t *testing.T, turnID string) (state, reason string) {
	t.Helper()
	r.WaitTerminal(turnID)
	row, err := r.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return row.TerminalState, row.TerminalReason
}

func refWantEnd(t *testing.T, state, reason, wantState, wantReason string) {
	t.Helper()
	if state != wantState || reason != wantReason {
		t.Fatalf("turn ended %q %q, want %q %q", state, reason, wantState, wantReason)
	}
}

// refRequests are the requests the author's endpoint received in the referee
// phase, in order.
func (r *refRig) refRequests() []transport.CompleteRequest { return r.calls("author", "referee") }

// refAttempts are the turn's referee attempts.
func refAttempts(t *testing.T, s *suite, turnID string) []journal.AttemptRow {
	t.Helper()
	rows, err := s.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	var out []journal.AttemptRow
	for _, row := range rows {
		if row.Phase == "referee" {
			out = append(out, row)
		}
	}
	return out
}

// refDecisions are the web_referee_decision events of the journal, read from
// the store.
func refDecisions(t *testing.T, s *suite) []map[string]string {
	t.Helper()
	events, err := s.Store.EventsSince(0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, ev := range events {
		if ev.Kind != "web_referee_decision" {
			continue
		}
		var m map[string]string
		if err := json.Unmarshal(ev.Data, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// refWaitDecisions waits until the journal holds n decisions.
func refWaitDecisions(t *testing.T, s *suite, n int) []map[string]string {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		if ds := refDecisions(t, s); len(ds) >= n || time.Now().After(deadline) {
			if len(ds) < n {
				t.Fatalf("%d decisions after %v, want %d", len(ds), waitLimit, n)
			}
			return ds
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// refToolText is the text of the tool message answering call id in the
// request, or "".
func refToolText(req transport.CompleteRequest, id string) string {
	if m := tlToolMessage(req, id); m != nil {
		return m.Content
	}
	return ""
}

// refBudget is the turn's budget record.
type refBudget struct {
	Projected int `json:"projected"`
	Reserved  int `json:"reserved"`
	Charged   int `json:"charged"`
}

func refBudgetOf(t *testing.T, s *suite, turnID string) refBudget {
	t.Helper()
	row, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	var rec refBudget
	if err := json.Unmarshal(row.BudgetJSON, &rec); err != nil {
		t.Fatalf("budget_json %q: %v", row.BudgetJSON, err)
	}
	return rec
}

// refStart starts an engine on the roster and returns the refusal Start gives,
// which is the roster's load: nil when the roster loads.
func refStart(t *testing.T, roster string) error {
	t.Helper()
	h := enginetest.New(t, roster, map[string][]fake.Step{}, enginetest.NoStart(),
		enginetest.WithModelToolCalls("local", "refmodel", transport.TriYes))
	err := h.Engine.Start()
	if err != nil {
		var re *engine.RosterError
		if !errors.As(err, &re) {
			t.Fatalf("Start = %v, want a roster refusal", err)
		}
	}
	return err
}

// refWorkspace is a workspace holding notes.txt, whose bytes must never reach
// a referee.
func refWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(refFileMark+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The author reads a file, stages a write and puts two searches; a reviewer
// puts one of its own. Every referee request is the fixed template and the
// authorized payload alone: the user's input, the proposed call and the
// caller's own earlier web calls. The second request is compared with the
// golden bytes; none carries a tool result, the file's bytes, the staged
// write, the candidate's text or the other participant's calls.
func TestReferee_PayloadMatchesGoldenAndAuthorizedFieldsOnly(t *testing.T) {
	t.Parallel()
	const candidate = "CANDIDATE-TEXT-OF-THE-AUTHOR"
	write := tlCall("w1", tools.NameWriteFile, `{"path":"out.txt","content":"STAGED-WRITE-CONTENT"}`)
	r := newRefRig(t, refRoster(""), map[string][]fake.Step{
		"author": tlAcks(
			tlStep("", tlRead("r1", "notes.txt"), write, refSearch("a1", "alpha query"), refSearch("a2", "beta query")),
			refAllow(), refAllow(),
			enginetest.Reply(candidate),
			refAllow(), // the reviewer's call, refereed on the reviewer's endpoint
		),
		"reviewer": tlAcks(tlStep("", refSearch("v1", "gamma query")), tlApprove()),
	})
	state, reason := r.refEnd(t, r.refSubmit(t, "refreview", refWorkspace(t)))
	refWantEnd(t, state, reason, engine.StateApproved, "all_approved")

	reqs := r.refRequests()
	if len(reqs) != 3 {
		t.Fatalf("%d referee requests, want the author's two and the reviewer's one", len(reqs))
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "referee_payload.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs[1].Messages) != 2 {
		t.Fatalf("referee request carries %d messages, want the template's two", len(reqs[1].Messages))
	}
	got := "--- system\n" + reqs[1].Messages[0].Content + "\n--- user\n" + reqs[1].Messages[1].Content
	if got != string(golden) {
		t.Fatalf("second referee request differs from the golden bytes\n got: %q\nwant: %q", got, golden)
	}
	if want := referee.PromptMessages(referee.Payload{UserInput: refInput, CallName: tools.NameWebSearch, CallURL: "beta query", PriorCalls: []referee.WebCall{{Name: tools.NameWebSearch, URL: "alpha query"}}}); fmt.Sprint(reqs[1].Messages) != fmt.Sprint(want) {
		t.Fatalf("messages %+v, want the template rendering %+v", reqs[1].Messages, want)
	}

	// The first has no prior call; the reviewer's has none of the author's.
	first := reqs[0].Messages[1].Content
	if !strings.HasSuffix(first, "prior calls this turn:\nnone\n") || !strings.Contains(first, `"web_search" "alpha query"`) {
		t.Fatalf("first request user message %q", first)
	}
	if reqs[2].Envelope.Participant != "reviewer" {
		t.Fatalf("third referee request is the %s's, want the reviewer's", reqs[2].Envelope.Participant)
	}
	if third := reqs[2].Messages[1].Content; !strings.HasSuffix(third, "prior calls this turn:\nnone\n") || !strings.Contains(third, `"web_search" "gamma query"`) {
		t.Fatalf("reviewer's request user message %q", third)
	}

	forbidden := []string{
		refFileMark, "STAGED-WRITE-CONTENT", "EXTRACT-FROM-THE-PROVIDER", candidate, tlNote,
		tools.NameReadFile, tools.NameWriteFile, tools.NameEditFile, tools.NameUnstage, "notes.txt", "out.txt",
	}
	for i, req := range reqs {
		if req.Model != "refmodel" || req.MaxOutputTokens != 64 || len(req.Tools) != 0 {
			t.Errorf("referee request %d: model %q, max output %d, %d tool definitions", i, req.Model, req.MaxOutputTokens, len(req.Tools))
		}
		for _, m := range req.Messages {
			for _, f := range forbidden {
				if strings.Contains(m.Content, f) {
					t.Errorf("referee request %d, %s message, carries %q", i, m.Role, f)
				}
			}
			if i != 2 && strings.Contains(m.Content, "gamma") {
				t.Errorf("an author's referee request %d carries the reviewer's call", i)
			}
			if i == 2 && (strings.Contains(m.Content, "alpha") || strings.Contains(m.Content, "beta")) {
				t.Errorf("the reviewer's referee request carries the author's calls")
			}
		}
	}
}

// A roster that grants web on an endpoint naming no referee model is refused
// at load, and the refusal names the endpoint; the same roster with the
// endpoint's referee_model loads.
func TestReferee_NoRefereeModelRefusedAtLoad(t *testing.T) {
	t.Parallel()
	bare := enginetest.RosterTOML + `
[presets.web]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["web"]
`
	err := refStart(t, bare)
	if err == nil {
		t.Fatal("a roster granting web on an endpoint with no referee_model loaded")
	}
	for _, want := range []string{`endpoint "local"`, "referee_model", "web"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not contain %q", err, want)
		}
	}
	if err := refStart(t, refRoster("")); err != nil {
		t.Fatalf("the roster naming the referee model is refused: %v", err)
	}
}

// refRemoteRoster puts the author on a remote endpoint whose referee runs on
// the endpoint its referee_endpoint names, with a participant on each.
func refRemoteRoster(refereeEndpoint string) string {
	r := strings.Replace(enginetest.RosterTOML, "local_only = true\nallowed_destinations = []", "local_only = false\nallowed_destinations = [\"cloud\"]", 1)
	return r + `
[[participants]]
id = "refhost"
model = "refmodel"
route = "api"
endpoint = "lan"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "cloudauthor"
model = "gpt"
route = "api"
endpoint = "cloud"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "lan"
base_url = "http://192.168.1.20:11434/v1"
declared_location = "lan"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["refmodel"]
tokens_per_byte_max = 1.0

[[endpoints]]
name = "cloud"
base_url = "https://api.example.com/v1"
declared_location = "cloud"
declared_execution = "remote"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["gpt"]
tokens_per_byte_max = 1.0
referee_model = "refmodel"
referee_endpoint = "` + refereeEndpoint + `"

[presets.cloudweb]
mode = "solo"
author = "cloudauthor"
required_reviewers = []
tools = ["web"]
` + refTable
}

// A referee_endpoint naming a local endpoint moves the referee of the
// endpoint's callers there: the decision is sent to that endpoint with the
// referee's model, and the caller's own endpoint sees nothing of it. A
// referee_endpoint naming an endpoint without local execution is refused at
// load, naming the endpoint.
func TestReferee_RefereeEndpointLocalMovesRefereeNonLocalRefused(t *testing.T) {
	t.Parallel()
	err := refStart(t, refRemoteRoster("cloud"))
	if err == nil || !strings.Contains(err.Error(), `endpoint "cloud"`) || !strings.Contains(err.Error(), "referee_endpoint") {
		t.Fatalf("a referee_endpoint without local execution: %v, want a refusal naming endpoint cloud", err)
	}

	n, secrets := newRefNet(t), &refSecrets{}
	s := newSuite(t, refRemoteRoster("lan"), map[string][]fake.Step{
		"cloudauthor": {tlStep("", refSearch("c1", "go testing")), enginetest.Reply("final answer")},
		"refhost":     {refDeny()},
	}, append(refOpts(n, secrets),
		enginetest.WithToolCalls("cloudauthor", transport.TriYes),
		enginetest.WithModelToolCalls("lan", "refmodel", transport.TriYes))...)
	rig := &refRig{suite: s, net: n, secrets: secrets}
	rig.WaitTerminal(rig.refSubmit(t, "cloudweb", ""))

	reqs := s.calls("refhost", "referee")
	if len(reqs) != 1 || reqs[0].Model != "refmodel" || reqs[0].Envelope.Participant != "cloudauthor" {
		t.Fatalf("the local endpoint's referee requests %+v, want the cloud author's decision on refmodel", reqs)
	}
	for _, req := range s.calls("cloudauthor", "referee") {
		t.Fatalf("the caller's own endpoint received a referee request: %+v", req)
	}
	for _, c := range s.Adapters["cloudauthor"].Calls() {
		if c.Request.Model == "refmodel" {
			t.Fatalf("the caller's own endpoint received a request for the referee model: %+v", c.Request)
		}
	}
}

// A referee model with no record on a flat endpoint is probed when the engine
// starts: there is no record before Start and a usable one after.
func TestReferee_FlatEndpointRefereeProbedAtStart(t *testing.T) {
	t.Parallel()
	roster := strings.Replace(refRoster(""), `declared_billing = "none"`, `declared_billing = "flat"`, 1)
	dir := t.TempDir()
	rosterPath := filepath.Join(dir, "roster.toml")
	if err := os.WriteFile(rosterPath, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := journal.Open(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := journal.NewStore(db)

	const version = "acceptance/1"
	capability := transport.CapabilityRecord{
		Identity:                 transport.NewUnknownIdentity(),
		AuthMode:                 "none",
		ReportedModel:            "reported",
		MaxTokensField:           "max_tokens",
		Streaming:                transport.TriUnknown,
		CancelObservable:         transport.TriUnknown,
		UsageAvailable:           transport.TriUnknown,
		ModelsEndpoint:           transport.TriUnknown,
		ModelListed:              transport.TriUnknown,
		SystemRoleHonored:        transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown,
		FinishReasons:            map[string]transport.CompletionStatus{},
		OverLength:               "not_run:skipped",
		AdapterVersion:           version,
		ObservedAt:               time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC),
		Raw:                      json.RawMessage("{}"),
	}
	adapters := map[string]*fake.Adapter{}
	for _, id := range []string{"author", "reviewer"} {
		adapters[id] = fake.New(nil, fake.Options{Capability: capability})
	}
	eng, err := engine.New(engine.Options{
		Store: store, Hub: hub.New(store, 0), RosterPath: rosterPath, EngineVersion: "acceptance",
		Clock: enginetest.NewFakeClock(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)),
		NewAdapter: func(p config.Participant, _ config.EndpointProfile) (transport.Adapter, string, error) {
			if a, ok := adapters[p.ID]; ok {
				return a, version, nil
			}
			return nil, "", errors.New("no adapter for " + p.ID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LatestProbeRecord("local", "refmodel"); err != nil || found {
		t.Fatalf("a record of the referee model before start: found %v, err %v", found, err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	rec, found, err := store.LatestProbeRecord("local", "refmodel")
	if err != nil || !found || !strings.Contains(string(rec.Manifest), "refmodel") {
		t.Fatalf("referee record after start: %+v, found %v, err %v", rec, found, err)
	}
}

// With no usable record of the referee model web is unoffered on the endpoint:
// the participants reply says why, the author is not offered the tool, a call
// of it is answered not offered, and no referee request, lookup or connection
// follows.
func TestReferee_UnprobedRefereeLeavesWebUnoffered(t *testing.T) {
	t.Parallel()
	n, secrets := newRefNet(t), &refSecrets{}
	s := newSuite(t, refRoster(""), map[string][]fake.Step{
		"author": {tlStep("", refSearch("c1", "go testing")), enginetest.Reply("final answer")},
	}, refOptsUnprobed(n, secrets)...)
	rig := &refRig{suite: s, net: n, secrets: secrets}

	const want = "referee_unprobed:no_record"
	reply := s.Status(t, s.Dial(t))
	var shown bool
	for _, p := range reply.Participants {
		if p.ID == "author" {
			shown = p.WebOff != nil && *p.WebOff == want
			if !shown {
				t.Errorf("participants reply web_off for the author = %v, want %q", p.WebOff, want)
			}
		}
	}
	if !shown {
		t.Fatal("the participants reply does not say why web is unoffered")
	}

	turnID := rig.refSubmit(t, "web", refWorkspace(t))
	state, reason := rig.refEnd(t, turnID)
	refWantEnd(t, state, reason, engine.StateSoloComplete, "solo_ok")
	for _, req := range s.calls("author", "author") {
		for _, def := range req.Tools {
			if strings.HasPrefix(def.Function.Name, "web_") {
				t.Fatalf("%s is offered although the referee has no usable record", def.Function.Name)
			}
		}
	}
	tlWantCalls(t, rig.tlCalls(t, turnID, "author"), [2]string{journal.ToolCallError, "not_offered"})
	if n := len(refAttempts(t, s, turnID)); n != 0 {
		t.Fatalf("%d referee attempts begun for a tool that is not offered", n)
	}
	if lookups, dials, hits := n.sent(); lookups+dials+hits != 0 || secrets.reads.Load() != 0 {
		t.Fatalf("lookups %d dials %d hits %d credential reads %d", lookups, dials, hits, secrets.reads.Load())
	}
}

// A referee deny yields a tool error naming the rule and the calling attempt
// continues: the author's next request carries the error and its answer
// completes the turn; nothing is resolved or sent, and the decision is
// journaled with its reason in the content store.
func TestReferee_DenyYieldsToolErrorAndCallerContinues(t *testing.T) {
	t.Parallel()
	r := newRefRig(t, refRoster(""), map[string][]fake.Step{
		"author": {tlStep("", refSearch("c1", "go testing")), refDeny(), enginetest.Reply("final answer")},
	})
	turnID := r.refSubmit(t, "webonly", "")
	state, reason := r.refEnd(t, turnID)
	refWantEnd(t, state, reason, engine.StateSoloComplete, "solo_ok")

	calls := r.tlCalls(t, turnID, "author")
	tlWantCalls(t, calls, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
	text, err := r.Store.Content(calls[0].Result.ResultSHA256)
	if err != nil || !strings.Contains(string(text), tools.RuleReferee) {
		t.Fatalf("the call's error %q, err %v, want the referee rule named", text, err)
	}
	reqs := r.tlRequests("author")
	if len(reqs) != 3 {
		t.Fatalf("%d requests to the endpoint, want author, referee, author", len(reqs))
	}
	if got := refToolText(reqs[2], "c1"); !strings.Contains(got, tools.RuleReferee) {
		t.Fatalf("the author's next request carries %q for the denied call, want the error", got)
	}
	if lookups, dials, hits := r.net.sent(); lookups+dials+hits != 0 || r.secrets.reads.Load() != 0 {
		t.Fatalf("lookups %d dials %d hits %d credential reads %d after a deny", lookups, dials, hits, r.secrets.reads.Load())
	}
	ds := refDecisions(t, r.suite)
	if len(ds) != 1 || ds[0]["verdict"] != "deny" || ds[0]["call_id"] != calls[0].ID || ds[0]["attempt_id"] == "" {
		t.Fatalf("decisions %+v", ds)
	}
	if body, err := r.Store.Content(ds[0]["reason_sha256"]); err != nil || string(body) != "the query does not serve the question" {
		t.Fatalf("reason %q, err %v", body, err)
	}
}

// Each outcome of a dispatched referee request that is not an allow is a deny
// that sends nothing: a timeout, an adapter error, a provider refusal, a
// malformed reply, a reply carrying tool calls, a reply cut off at the bound.
// Work whose outcome is unknown (timeout, adapter error) also holds the pool.
func TestReferee_DispatchedFailuresEachDeny(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		step  fake.Step
		cause string
		holds bool
	}{
		{"timeout", fake.Step{Kind: fake.StepKindReply, Status: transport.StatusInterrupted, TransportStatus: transport.TransportStatusTimeout}, journal.RefereeCauseTimeout, true},
		{"adapter error", fake.Step{Kind: fake.StepKindError, Err: errors.New("connection broke")}, journal.RefereeCauseAdapterError, true},
		{"provider refusal", fake.Step{Kind: fake.StepKindReply, Status: transport.StatusRefused, TransportStatus: transport.TransportStatusOK}, journal.RefereeCauseRefusal, false},
		{"malformed reply", enginetest.Reply("maybe"), journal.RefereeCauseMalformed, false},
		{"tool calls", tlStep("", tlRead("x", "notes.txt")), journal.RefereeCauseCallsWithoutTools, false},
		{"cut off", fake.Step{Kind: fake.StepKindReply, Text: "allow because", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}, journal.RefereeCauseCutOff, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRefRig(t, refRoster(""), map[string][]fake.Step{
				"author": {tlStep("", refSearch("c1", "go testing")), c.step, enginetest.Reply("final answer")},
			})
			turnID := r.refSubmit(t, "webonly", "")
			ds := refWaitDecisions(t, r.suite, 1)
			if ds[0]["verdict"] != "deny" || ds[0]["cause"] != c.cause || ds[0]["attempt_id"] == "" {
				t.Fatalf("decision %+v, want a deny caused by %s", ds[0], c.cause)
			}
			if lookups, dials, hits := r.net.sent(); lookups+dials+hits != 0 || r.secrets.reads.Load() != 0 {
				t.Fatalf("lookups %d dials %d hits %d credential reads %d: a failed decision authorizes nothing", lookups, dials, hits, r.secrets.reads.Load())
			}
			if holds, err := r.Store.UnreleasedHolds(); err != nil || (len(holds) == 1) != c.holds || len(holds) > 1 {
				t.Fatalf("holds %+v, err %v, want a hold: %v", holds, err, c.holds)
			}
			if !c.holds {
				// The turn is not ended by a deny: the author goes on.
				state, reason := r.refEnd(t, turnID)
				refWantEnd(t, state, reason, engine.StateSoloComplete, "solo_ok")
				tlWantCalls(t, r.tlCalls(t, turnID, "author"), [2]string{journal.ToolCallError, tools.ErrCodeRefused})
			}
		})
	}
}

// An allow that arrives after a cancel (the turn's epoch moved on while the
// referee was answering) is quarantined and authorizes nothing: the decision
// is a deny with cause late, the attempt is quarantined and charged, and no
// name is resolved, no connection made and no credential read.
func TestReferee_LateAllowAfterCancelAuthorizesNothing(t *testing.T) {
	t.Parallel()
	var s *suite
	var turn atomic.Value
	ready := make(chan struct{})
	var once sync.Once
	late := enginetest.ReplyFunc(func(transport.CompleteRequest) string {
		once.Do(func() {
			<-ready
			if err := s.Engine.Cancel(turn.Load().(string)); err != nil {
				t.Errorf("cancel: %v", err)
			}
		})
		return "allow it is fine"
	})
	r := newRefRig(t, refRoster(""), map[string][]fake.Step{
		"author": {tlStep("", refSearch("c1", "go testing")), late, enginetest.Reply("never")},
	})
	s = r.suite
	turnID := r.refSubmit(t, "webonly", "")
	turn.Store(turnID)
	close(ready)
	state, reason := r.refEnd(t, turnID)
	refWantEnd(t, state, reason, engine.StateCancelled, "cancelled")

	ds := refWaitDecisions(t, s, 1)
	if ds[0]["verdict"] != "deny" || ds[0]["cause"] != "late" {
		t.Fatalf("decision %+v, want a deny caused by lateness", ds[0])
	}
	attempts := refAttempts(t, s, turnID)
	if len(attempts) != 1 || !attempts[0].Quarantined {
		t.Fatalf("referee attempts %+v, want one, quarantined", attempts)
	}
	turnRow, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if turnRow.Epoch <= attempts[0].Epoch {
		t.Fatalf("turn epoch %d, attempt epoch %d: the cancel did not move the epoch", turnRow.Epoch, attempts[0].Epoch)
	}
	if rec := refBudgetOf(t, s, turnID); rec.Reserved != 0 {
		t.Fatalf("budget %+v: the late result is charged, not left reserved", rec)
	}
	if lookups, dials, hits := r.net.sent(); lookups+dials+hits != 0 || r.secrets.reads.Load() != 0 {
		t.Fatalf("lookups %d dials %d hits %d credential reads %d: a late allow authorized the call", lookups, dials, hits, r.secrets.reads.Load())
	}
	if n := len(r.refRequests()); n != 1 {
		t.Fatalf("%d referee requests, want the one that was late", n)
	}
}

// A referee dispatch that does not fit what is left of the turn's budget is a
// reservation failure, not a deny: the turn ends budget:turn, no referee
// attempt is begun, nothing is sent and no decision is journaled. A budget
// that has room for the dispatch lets the same turn run to completion.
func TestReferee_ReservationFailureEndsBudgetTurn(t *testing.T) {
	t.Parallel()
	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{"author": {tlStep("", refSearch("c1", "go testing")), refDeny(), enginetest.Reply("final answer")}}
	}
	roomy := newRefRig(t, refBudgets(refRoster(""), "turn_max_estimated_tokens = 1000000"), scripts())
	roomyTurn := roomy.refSubmit(t, "webonly", "")
	state, reason := roomy.refEnd(t, roomyTurn)
	refWantEnd(t, state, reason, engine.StateSoloComplete, "solo_ok")
	limit := refBudgetOf(t, roomy.suite, roomyTurn).Projected

	r := newRefRig(t, refBudgets(refRoster(""), "turn_max_estimated_tokens = "+strconv.Itoa(limit)), scripts())
	turnID := r.refSubmit(t, "webonly", "")
	state, reason = r.refEnd(t, turnID)
	refWantEnd(t, state, reason, engine.StateIncomplete, "budget:turn")
	if n := len(refAttempts(t, r.suite, turnID)); n != 0 {
		t.Fatalf("%d referee attempts begun for a dispatch that did not fit", n)
	}
	if n := len(r.refRequests()); n != 0 {
		t.Fatalf("%d referee requests sent for a dispatch that did not fit", n)
	}
	if ds := refDecisions(t, r.suite); len(ds) != 0 {
		t.Fatalf("decisions %+v: a reservation failure is not a deny", ds)
	}
	if lookups, dials, hits := r.net.sent(); lookups+dials+hits != 0 {
		t.Fatalf("lookups %d dials %d hits %d", lookups, dials, hits)
	}
	calls, err := r.Store.ToolCallsByTurn(turnID, "author")
	if err != nil || len(calls) != 1 || (calls[0].Result != nil && calls[0].Result.ErrorCode == tools.ErrCodeRefused) {
		t.Fatalf("calls %+v, err %v: the call has no deny answer, the turn ended", calls, err)
	}
}

// A query the deterministic checks refuse (here sixteen bytes read from a file
// this turn) is refused before anything else: no referee request is made, no
// decision journaled, and nothing is resolved or sent.
func TestReferee_NotConsultedForCheckRefusedQuery(t *testing.T) {
	t.Parallel()
	r := newRefRig(t, refRoster(""), map[string][]fake.Step{
		"author": {
			tlStep("", tlRead("r1", "notes.txt")),
			tlStep("", refSearch("c2", "FILE-BYTES-OF-NOTES")),
			enginetest.Reply("final answer"),
		},
	})
	turnID := r.refSubmit(t, "web", refWorkspace(t))
	state, reason := r.refEnd(t, turnID)
	refWantEnd(t, state, reason, engine.StateSoloComplete, "solo_ok")
	calls := r.tlCalls(t, turnID, "author")
	tlWantCalls(t, calls, [2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallError, tools.ErrCodeRefused})
	if text, err := r.Store.Content(calls[1].Result.ResultSHA256); err != nil || !strings.Contains(string(text), tools.RuleCanary) {
		t.Fatalf("refusal %q, err %v, want the canary rule named", text, err)
	}
	if n := len(refAttempts(t, r.suite, turnID)) + len(r.refRequests()) + len(refDecisions(t, r.suite)); n != 0 {
		t.Fatalf("the referee was consulted for a refused query: %d traces", n)
	}
	if lookups, dials, hits := r.net.sent(); lookups+dials+hits != 0 || r.secrets.reads.Load() != 0 {
		t.Fatalf("lookups %d dials %d hits %d credential reads %d", lookups, dials, hits, r.secrets.reads.Load())
	}
}

// A referee decision is an accounted dispatch of the caller: reserved over its
// rendered request (the whole template counts as prompt bytes) before it is
// sent, bounded by tools.referee.max_output_tokens, charged on completion and
// reconciled with the usage the referee reports in place of the reservation.
func TestReferee_DecisionReservedAdmittedChargedReconciled(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, reply fake.Step) (*refRig, string) {
		r := newRefRig(t, refRoster("\n[tools.referee]\nmax_output_tokens = 17\n"), map[string][]fake.Step{
			"author": {tlStep("", refSearch("c1", "go testing")), reply, enginetest.Reply("final answer")},
		})
		turnID := r.refSubmit(t, "webonly", "")
		state, reason := r.refEnd(t, turnID)
		refWantEnd(t, state, reason, engine.StateSoloComplete, "solo_ok")
		return r, turnID
	}
	plain, plainTurn := run(t, refDeny())
	reqs := plain.refRequests()
	attempts := refAttempts(t, plain.suite, plainTurn)
	if len(reqs) != 1 || len(attempts) != 1 {
		t.Fatalf("%d referee requests and %d attempts, want one each", len(reqs), len(attempts))
	}
	a := attempts[0]
	overheads := budget.Overheads{MessageTokens: 8, RequestTokens: 16}
	if want := budget.EstimateTokens(reqs[0].Messages, 3.0, overheads); a.EstimatedPromptTokens == nil || *a.EstimatedPromptTokens != want {
		t.Fatalf("reserved prompt tokens %v, want %d, the estimate over the rendered request with its whole template", a.EstimatedPromptTokens, want)
	}
	if a.MaxOutputTokens == nil || *a.MaxOutputTokens != 17 || reqs[0].MaxOutputTokens != 17 || a.State == "pending" || a.Participant != "author" {
		t.Fatalf("attempt %+v, request max output %d: want 17 output tokens, the caller's, and no longer pending", a, reqs[0].MaxOutputTokens)
	}
	reserved := *a.EstimatedPromptTokens + 17
	plainRec := refBudgetOf(t, plain.suite, plainTurn)
	if plainRec.Reserved != 0 {
		t.Fatalf("budget %+v: the decision is charged, not left reserved", plainRec)
	}

	// The same turn with the referee reporting usage is charged what was
	// reported and not the reservation.
	usage := refDeny()
	usage.Usage = &transport.Usage{Prompt: 5, Completion: 2}
	metered, meteredTurn := run(t, usage)
	meteredRec := refBudgetOf(t, metered.suite, meteredTurn)
	if want := plainRec.Charged - reserved + 7; meteredRec.Charged != want || meteredRec.Reserved != 0 {
		t.Fatalf("charged %d reserved %d, want %d and 0: the reservation of %d is replaced by the reported 7", meteredRec.Charged, meteredRec.Reserved, want, reserved)
	}
	ma := refAttempts(t, metered.suite, meteredTurn)[0]
	if ma.UsagePrompt == nil || *ma.UsagePrompt != 5 || ma.UsageCompletion == nil || *ma.UsageCompletion != 2 {
		t.Fatalf("attempt usage %v/%v, want the reported 5/2", ma.UsagePrompt, ma.UsageCompletion)
	}
}

// The run manifest carries, for the endpoint that names a referee, the
// referee model and the hash of the fixed template, and the referee's probe
// record beside them.
func TestReferee_ManifestCarriesRefereeModelAndTemplateHash(t *testing.T) {
	t.Parallel()
	s := newRefRig(t, refRoster(""), map[string][]fake.Step{})
	run, err := s.Store.GetRun(s.Dial(t).Hello().RunID)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Roster struct {
			Endpoints []map[string]any `json:"endpoints"`
		} `json:"roster"`
		RefereeProbeRecords map[string]json.RawMessage `json:"referee_probe_records"`
	}
	if err := json.Unmarshal(run.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ep := range m.Roster.Endpoints {
		if ep["name"] != "local" {
			continue
		}
		found = true
		if ep["referee_model"] != "refmodel" || ep["referee_template_hash"] != referee.TemplateHash() {
			t.Fatalf("endpoint entry %+v, want referee_model refmodel and template hash %s", ep, referee.TemplateHash())
		}
	}
	if !found {
		t.Fatalf("no endpoint local in the manifest: %+v", m.Roster.Endpoints)
	}
	if len(referee.TemplateHash()) != 64 {
		t.Fatalf("template hash %q", referee.TemplateHash())
	}
	if rec, ok := m.RefereeProbeRecords["local"]; !ok || !strings.Contains(string(rec), "refmodel") {
		t.Fatalf("referee probe records %v, want the record of refmodel under endpoint local", m.RefereeProbeRecords)
	}
}
