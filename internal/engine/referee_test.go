package engine_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/referee"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// refereeRoster is the harness roster with the endpoint naming refmodel as its
// referee, run on that endpoint itself, and a solo preset giving the author
// the web tools.
func refereeRoster(extra string) string {
	r := strings.Replace(enginetest.RosterTOML, `models = ["llama2", "llama3"]`, `models = ["llama2", "llama3", "refmodel"]
referee_model = "refmodel"`, 1)
	return r + `
[presets.web]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["read", "web"]
` + extra
}

func refereeOpts() []enginetest.Option {
	return []enginetest.Option{
		enginetest.WithToolCalls("author", transport.TriYes),
		enginetest.WithModelToolCalls("local", "refmodel", transport.TriYes),
	}
}

// webCall is one web call to put to the referee from inside the author's tool
// step: a call that precedes it is journaled first, as an earlier call of the
// turn.
type webCall struct{ name, target string }

// refereeRun is a turn whose author's tool step puts calls to the referee from
// inside its effect, with the author's slot held, and records what came back.
type refereeRun struct {
	h    *enginetest.Harness
	turn string
	mu   sync.Mutex
	outs []engine.RefereeOutcome
	errs []error
}

// runReferee submits the web preset. The author's script is its tool step, the
// referee's replies in order (the referee runs through the first participant
// on its endpoint, the author's adapter), and a final reply. prior are web
// calls journaled before each call of calls; each call of calls is journaled
// with the call it is put as.
func runReferee(t *testing.T, roster string, opts []enginetest.Option, replies []fake.Step, prior []webCall, calls []webCall) *refereeRun {
	t.Helper()
	run := &refereeRun{}
	var once sync.Once
	hook := func(turnID, attemptID, _ string) {
		once.Do(func() {
			for i, p := range prior {
				run.journalWeb(t, attemptID, 10+i, p)
			}
			for i, c := range calls {
				id := run.journalWeb(t, attemptID, 20+i, c)
				out, err := run.h.Engine.DispatchReferee(turnID, "author", id, c.name, c.target)
				run.mu.Lock()
				run.outs = append(run.outs, out)
				run.errs = append(run.errs, err)
				run.mu.Unlock()
			}
		})
	}
	script := append([]fake.Step{toolStepWith(call("c1", tools.NameReadFile, `{"path":"a.txt"}`))}, replies...)
	script = append(script, enginetest.Reply("final answer"))
	opts = append(opts, enginetest.WithBeforeToolEffect(hook))
	run.h = enginetest.New(t, roster, map[string][]fake.Step{"author": script}, opts...)
	reply, err := run.h.Engine.Submit(toolReq("s1", "web", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	run.turn = reply.TurnID
	return run
}

// journalWeb records a web call of the author's step at the call index and
// returns its id.
func (r *refereeRun) journalWeb(t *testing.T, attemptID string, index int, c webCall) string {
	t.Helper()
	arg := "url"
	if c.name == tools.NameWebSearch {
		arg = "query"
	}
	args, _ := json.Marshal(map[string]string{arg: c.target})
	var id string
	err := r.h.Store.Tx(func(tx *journal.Tx) error {
		rec, err := tx.RecordToolCall(attemptID, index, "p"+c.target, c.name, tools.GroupWeb, args)
		id = rec.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// done reports whether n decisions have come back.
func (r *refereeRun) done(n int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.outs) >= n
}

func allowReply() fake.Step { return enginetest.Reply("allow the page serves the question") }

// refereeEvents are the web_referee_decision events of the journal. They are
// read from the store, not the subscription, which a turn ended mid-step can
// leave short of the journal's last sequence number.
func refereeEvents(t *testing.T, h *enginetest.Harness) []map[string]string {
	t.Helper()
	events, err := h.Store.EventsSince(0, 100000)
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

// storedEvents counts the journal's events of a kind.
func storedEvents(t *testing.T, h *enginetest.Harness, kind string) int {
	t.Helper()
	events, err := h.Store.EventsSince(0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func refereeAttempts(t *testing.T, h *enginetest.Harness, turnID string) []journal.AttemptRow {
	t.Helper()
	rows, err := h.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	var out []journal.AttemptRow
	for _, r := range rows {
		if r.Phase == "referee" {
			out = append(out, r)
		}
	}
	return out
}

// An allow from a result that passed its checks authorizes the call. The
// request is the fixed template and the payload only, to the referee model,
// bounded by the default 64 output tokens; it is an ordinary attempt of the
// caller, reserved before it was sent and charged on completion, and its
// decision is journaled with the attempt and the reason in the content store.
func TestRefereeAllowIsAnAccountedAttempt(t *testing.T) {
	prior := []webCall{{tools.NameWebFetch, "https://example.com/a"}, {tools.NameWebSearch, "go testing"}}
	run := runReferee(t, refereeRoster(""), refereeOpts(), []fake.Step{allowReply()}, prior,
		[]webCall{{tools.NameWebFetch, "https://example.com/b"}})
	wantEnd(t, run.h, run.turn, engine.StateSoloComplete, "solo_ok")
	if run.errs[0] != nil || !run.outs[0].Allow || run.outs[0].Reason != "the page serves the question" {
		t.Fatalf("outcome %+v, err %v", run.outs[0], run.errs[0])
	}

	reqs := requestsOf(run.h, "author")
	if len(reqs) != 3 {
		t.Fatalf("%d requests to the endpoint, want author, referee, author", len(reqs))
	}
	ref := reqs[1]
	want := referee.PromptMessages(referee.Payload{
		UserInput: "question", CallName: tools.NameWebFetch, CallURL: "https://example.com/b",
		PriorCalls: []referee.WebCall{{Name: tools.NameWebFetch, URL: "https://example.com/a"}, {Name: tools.NameWebSearch, URL: "go testing"}},
	})
	if !reflect.DeepEqual(ref.Messages, want) {
		t.Fatalf("referee messages\n%+v\nwant\n%+v", ref.Messages, want)
	}
	if ref.Model != "refmodel" || ref.MaxOutputTokens != 64 || len(ref.Tools) != 0 || ref.Envelope.Phase != "referee" {
		t.Fatalf("referee request %+v", ref)
	}

	attempts := refereeAttempts(t, run.h, run.turn)
	if len(attempts) != 1 || attempts[0].ID != run.outs[0].AttemptID || attempts[0].Participant != "author" {
		t.Fatalf("referee attempts %+v", attempts)
	}
	a := attempts[0]
	if a.EstimatedPromptTokens == nil || *a.EstimatedPromptTokens <= 0 || a.MaxOutputTokens == nil || *a.MaxOutputTokens != 64 || a.State == "pending" {
		t.Fatalf("attempt %+v", a)
	}
	turn, err := run.h.Store.GetTurn(run.turn)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct{ Reserved, Charged int }
	if err := json.Unmarshal(turn.BudgetJSON, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Reserved != 0 || rec.Charged < *a.EstimatedPromptTokens+64 {
		t.Fatalf("budget %+v: the referee decision is reserved, then charged", rec)
	}

	evs := refereeEvents(t, run.h)
	if len(evs) != 1 || evs[0]["verdict"] != "allow" || evs[0]["attempt_id"] != a.ID {
		t.Fatalf("decision events %+v", evs)
	}
	body, err := run.h.Store.Content(evs[0]["reason_sha256"])
	if err != nil || string(body) != "the page serves the question" {
		t.Fatalf("reason content %q, err %v", body, err)
	}
}

// tools.referee.max_output_tokens bounds the referee's request and its
// reservation.
func TestRefereeMaxOutputTokensIsConfigured(t *testing.T) {
	run := runReferee(t, refereeRoster("\n[tools.referee]\nmax_output_tokens = 17\n"), refereeOpts(), []fake.Step{allowReply()}, nil,
		[]webCall{{tools.NameWebFetch, "https://example.com/"}})
	wantEnd(t, run.h, run.turn, engine.StateSoloComplete, "solo_ok")
	if got := requestsOf(run.h, "author")[1].MaxOutputTokens; got != 17 {
		t.Fatalf("MaxOutputTokens = %d, want 17", got)
	}
	a := refereeAttempts(t, run.h, run.turn)[0]
	if *a.MaxOutputTokens != 17 {
		t.Fatalf("reserved output %d, want 17", *a.MaxOutputTokens)
	}
}

// Every dispatched request that does not answer allow is a deny, with a
// tool error for the call to continue from, and the turn goes on.
func TestRefereeDispatchedRefusalIsDeny(t *testing.T) {
	refused := fake.Step{Kind: fake.StepKindReply, Text: "", Status: transport.StatusRefused, TransportStatus: transport.TransportStatusOK}
	cutOff := fake.Step{Kind: fake.StepKindReply, Text: "allow because", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}
	withCalls := toolStepWith(call("x", tools.NameReadFile, `{"path":"a.txt"}`))
	notSent := fake.Step{Kind: fake.StepKindNotSent}
	wrongModel := enginetest.Reply("allow fine")
	wrongModel.ReportedModel = "someone-else"
	cases := []struct {
		name  string
		step  fake.Step
		cause string
	}{
		{"deny answer", enginetest.Reply("deny the url carries the question"), journal.RefereeCauseAnswer},
		{"refusal", refused, journal.RefereeCauseRefusal},
		{"malformed", enginetest.Reply("maybe"), journal.RefereeCauseMalformed},
		{"two lines", enginetest.Reply("allow ok\nand more"), journal.RefereeCauseMalformed},
		{"cut off", cutOff, journal.RefereeCauseCutOff},
		{"tool calls", withCalls, journal.RefereeCauseCallsWithoutTools},
		{"not sent", notSent, journal.RefereeCauseAdapterError},
		{"identity", wrongModel, refereeCauseGate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := runReferee(t, refereeRoster(""), refereeOpts(), []fake.Step{c.step}, nil,
				[]webCall{{tools.NameWebFetch, "https://example.com/"}})
			// The turn is not ended by a deny: the author's next attempt runs.
			wantEnd(t, run.h, run.turn, engine.StateSoloComplete, "solo_ok")
			if run.errs[0] != nil || run.outs[0].Allow || run.outs[0].Cause != c.cause {
				t.Fatalf("outcome %+v, err %v; want a deny caused by %s", run.outs[0], run.errs[0], c.cause)
			}
			evs := refereeEvents(t, run.h)
			if len(evs) != 1 || evs[0]["verdict"] != "deny" || evs[0]["cause"] != c.cause || evs[0]["attempt_id"] == "" {
				t.Fatalf("decision events %+v", evs)
			}
			if holds, err := run.h.Store.UnreleasedHolds(); err != nil || len(holds) != 0 {
				t.Fatalf("holds %v, err %v: a refusal is known work", holds, err)
			}
		})
	}
}

// refereeCauseGate is the cause of a deny from a result that failed the
// identity or completion checks.
const refereeCauseGate = "gate"

// A timeout leaves the referee's work unknown: it is a deny, the work holds
// the caller's pool capacity, and while the hold stands no later decision is
// dispatched on that pool.
func TestRefereeTimeoutHoldsCapacity(t *testing.T) {
	timedOut := fake.Step{Kind: fake.StepKindReply, Status: transport.StatusInterrupted, TransportStatus: transport.TransportStatusTimeout}
	run := runReferee(t, refereeRoster(""), refereeOpts(), []fake.Step{timedOut, allowReply()}, nil, []webCall{
		{tools.NameWebFetch, "https://example.com/1"},
		{tools.NameWebFetch, "https://example.com/2"},
	})
	// The hold takes the pool's only slot, so the author's next attempt
	// waits behind it and the turn stays open.
	waitFor(t, "both decisions", func() bool { return run.done(2) })
	first, second := run.outs[0], run.outs[1]
	if run.errs[0] != nil || first.Allow || first.Cause != journal.RefereeCauseTimeout {
		t.Fatalf("first outcome %+v, err %v", first, run.errs[0])
	}
	holds, err := run.h.Store.UnreleasedHolds()
	if err != nil || len(holds) != 1 || holds[0].AttemptID != first.AttemptID || holds[0].Pool != "default" {
		t.Fatalf("holds %+v, err %v: the unknown work must hold the pool", holds, err)
	}
	if run.errs[1] != nil || second.Allow || second.Cause != "pool_held" || second.AttemptID != "" {
		t.Fatalf("second outcome %+v, err %v: nothing may be dispatched on a held pool", second, run.errs[1])
	}
	if n := len(requestsOf(run.h, "author")); n != 2 {
		t.Fatalf("%d requests, want the author's first and the one referee request", n)
	}
	st, err := run.h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Pools {
		if p.Name == "default" && len(p.Holds) != 1 {
			t.Fatalf("pool %+v, want its hold shown", p)
		}
	}
}

// An adapter error after the request may have been sent is uncertain work as
// well: a deny that holds capacity.
func TestRefereeAdapterErrorHoldsCapacity(t *testing.T) {
	broken := fake.Step{Kind: fake.StepKindError, Err: errors.New("connection broke")}
	run := runReferee(t, refereeRoster(""), refereeOpts(), []fake.Step{broken}, nil, []webCall{{tools.NameWebFetch, "https://example.com/"}})
	waitFor(t, "the decision", func() bool { return run.done(1) })
	if run.errs[0] != nil || run.outs[0].Allow || run.outs[0].Cause != journal.RefereeCauseAdapterError {
		t.Fatalf("outcome %+v, err %v", run.outs[0], run.errs[0])
	}
	if holds, err := run.h.Store.UnreleasedHolds(); err != nil || len(holds) != 1 {
		t.Fatalf("holds %+v, err %v", holds, err)
	}
}

// A result that arrives after the turn moved on (here a cancel lands while the
// referee is answering) is quarantined and charged, and an allow in it
// authorizes nothing.
func TestRefereeLateAllowAuthorizesNothing(t *testing.T) {
	var h *enginetest.Harness
	var turn string
	var once sync.Once
	late := enginetest.ReplyFunc(func(transport.CompleteRequest) string {
		once.Do(func() {
			if err := h.Engine.Cancel(turn); err != nil {
				t.Errorf("cancel: %v", err)
			}
		})
		return "allow it is fine"
	})
	run := &refereeRun{}
	hook := func(turnID, attemptID, _ string) {
		id := run.journalWeb(t, attemptID, 20, webCall{tools.NameWebFetch, "https://example.com/"})
		out, err := run.h.Engine.DispatchReferee(turnID, "author", id, tools.NameWebFetch, "https://example.com/")
		run.mu.Lock()
		run.outs, run.errs = append(run.outs, out), append(run.errs, err)
		run.mu.Unlock()
	}
	opts := append(refereeOpts(), enginetest.WithBeforeToolEffect(hook))
	h = enginetest.New(t, refereeRoster(""), map[string][]fake.Step{
		"author": {toolStepWith(call("c1", tools.NameReadFile, `{"path":"a.txt"}`)), late, enginetest.Reply("never")},
	}, opts...)
	run.h = h
	reply, err := h.Engine.Submit(toolReq("s1", "web", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	turn = reply.TurnID
	wantEnd(t, h, turn, engine.StateCancelled, "cancelled")
	// The effect ran on its own goroutine; wait for its outcome.
	waitFor(t, "the referee outcome", func() bool { return run.done(1) })
	if run.outs[0].Allow || run.outs[0].Cause != "late" {
		t.Fatalf("outcome %+v, err %v: a late allow must not authorize", run.outs[0], run.errs[0])
	}
	a := getAttempt(t, h, run.outs[0].AttemptID)
	if !a.Quarantined {
		t.Fatalf("attempt %+v, want it quarantined", a)
	}
	evs := refereeEvents(t, h)
	if len(evs) != 1 || evs[0]["verdict"] != "deny" || evs[0]["cause"] != "late" {
		t.Fatalf("decision events %+v", evs)
	}
	if n := len(requestsOf(h, "author")); n != 2 {
		t.Fatalf("%d requests: nothing is dispatched after the turn ended", n)
	}
	turnRow, err := h.Store.GetTurn(turn)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct{ Reserved int }
	if err := json.Unmarshal(turnRow.BudgetJSON, &rec); err != nil || rec.Reserved != 0 {
		t.Fatalf("budget %s, err %v: the late result is charged, not left reserved", turnRow.BudgetJSON, err)
	}
}

// A referee dispatch that does not fit what is left of the turn's budget is a
// reservation failure, not a deny: the turn ends budget:turn, nothing is sent
// or journaled for the decision, and the call has no answer.
func TestRefereeReservationFailureEndsBudgetTurn(t *testing.T) {
	calls := []webCall{{tools.NameWebFetch, "https://example.com/"}}
	probe := runReferee(t, withBudgets(refereeRoster(""), "turn_max_estimated_tokens = 1000000"), refereeOpts(), []fake.Step{allowReply()}, nil, calls)
	wantEnd(t, probe.h, probe.turn, engine.StateSoloComplete, "solo_ok")
	limit := budgetOf(t, probe.h, probe.turn).Projected

	run := runReferee(t, withBudgets(refereeRoster(""), "turn_max_estimated_tokens = "+strconv.Itoa(limit)), refereeOpts(), []fake.Step{allowReply()}, nil, calls)
	wantEnd(t, run.h, run.turn, engine.StateIncomplete, "budget:turn")
	waitFor(t, "the referee outcome", func() bool { return run.done(1) })
	if !errors.Is(run.errs[0], engine.ErrToolAbandoned) || run.outs[0].Allow || run.outs[0].AttemptID != "" {
		t.Fatalf("outcome %+v, err %v: a reservation failure is no decision", run.outs[0], run.errs[0])
	}
	if n := len(refereeAttempts(t, run.h, run.turn)); n != 0 {
		t.Fatalf("%d referee attempts begun for a dispatch that did not fit", n)
	}
	if n := len(requestsOf(run.h, "author")); n != 1 {
		t.Fatalf("%d requests sent, want only the author's first", n)
	}
	if evs := refereeEvents(t, run.h); len(evs) != 0 {
		t.Fatalf("decision events %+v: a reservation failure is not a deny", evs)
	}
	if n := storedEvents(t, run.h, wire.EventKindBudget); n != 1 {
		t.Fatalf("%d budget events, want 1", n)
	}
}

// remoteRefereeRoster puts the author on a remote endpoint whose referee runs
// on the local endpoint its referee_endpoint names, and a participant on that
// local endpoint to carry the requests.
func remoteRefereeRoster() string {
	return strings.Replace(enginetest.RosterTOML, "local_only = true\nallowed_destinations = []", "local_only = false\nallowed_destinations = [\"cloud\"]", 1) + `
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
referee_endpoint = "lan"

[presets.cloudweb]
mode = "solo"
author = "cloudauthor"
required_reviewers = []
tools = ["web"]
`
}

// The referee of a caller runs on the caller's own endpoint, or on the local
// endpoint its profile names, and nowhere else: a caller on a remote endpoint
// with a referee_endpoint has its decisions sent to that local endpoint, with
// the referee's model, and its own endpoint sees none of it.
func TestRefereeRunsOnLocalEndpointNamedByProfile(t *testing.T) {
	h := enginetest.New(t, remoteRefereeRoster(), map[string][]fake.Step{
		"cloudauthor": {enginetest.Reply("final answer")},
		"refhost":     {allowReply()},
	}, append(refereeOpts(),
		enginetest.WithToolCalls("cloudauthor", transport.TriYes),
		enginetest.WithModelToolCalls("lan", "refmodel", transport.TriYes))...)
	if reason := h.Engine.WebOffReason("cloud"); reason != "" {
		t.Fatalf("web off for cloud: %q", reason)
	}
	// Open a turn to dispatch against: the author's reply is held.
	release := h.Hold("cloudauthor")
	preset := "cloudweb"
	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "question", Preset: &preset})
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateAuthorDispatched)
	out, err := h.Engine.DispatchReferee(reply.TurnID, "cloudauthor", "call-1", tools.NameWebFetch, "https://example.com/")
	release()
	if err != nil || !out.Allow {
		t.Fatalf("outcome %+v, err %v", out, err)
	}
	reqs := requestsOf(h, "refhost")
	if len(reqs) != 1 || reqs[0].Model != "refmodel" || reqs[0].Envelope.Participant != "cloudauthor" {
		t.Fatalf("local endpoint requests %+v", reqs)
	}
	for _, r := range requestsOf(h, "cloudauthor") {
		if r.Model == "refmodel" || r.Envelope.Phase == "referee" {
			t.Fatalf("the caller's own endpoint received a referee request: %+v", r)
		}
	}
}

// web is not offered, and the participants reply says why, when the endpoint's
// referee has no usable probe record; with a record it is offered. A referee
// request is refused outright for such a caller.
func TestRefereeWithoutUsableRecordDisablesWeb(t *testing.T) {
	// No record is seeded for refmodel: the participants have theirs.
	h := enginetest.New(t, refereeRoster(""), map[string][]fake.Step{"author": {enginetest.Reply("answer")}},
		enginetest.WithToolCalls("author", transport.TriYes))
	if reason := h.Engine.WebOffReason("local"); reason != "referee_unprobed:no_record" {
		t.Fatalf("web off reason %q, want referee_unprobed:no_record", reason)
	}
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Participants {
		holdsWeb := p.ID == "author"
		switch {
		case holdsWeb && (p.WebOff == nil || *p.WebOff != "referee_unprobed:no_record"):
			t.Errorf("author web_off = %v, want the reason shown", p.WebOff)
		case !holdsWeb && p.WebOff != nil:
			t.Errorf("%s web_off = %q: it holds no web tool", p.ID, *p.WebOff)
		}
	}
	// A turn's author is not offered web either.
	run := &refereeRun{h: h}
	reply, err := h.Engine.Submit(toolReq("s1", "web", workspaceDir(t)))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	for _, r := range requestsOf(h, "author") {
		for _, def := range r.Tools {
			if def.Function.Name == tools.NameWebFetch {
				t.Fatalf("web_fetch offered with no usable referee: %+v", run)
			}
		}
	}

	withRecord := enginetest.New(t, refereeRoster(""), nil, refereeOpts()...)
	if reason := withRecord.Engine.WebOffReason("local"); reason != "" {
		t.Fatalf("web off reason %q with a usable record", reason)
	}
	if reason := withRecord.Engine.WebOffReason("nowhere"); reason != "no_referee_model" {
		t.Fatalf("web off reason %q for an endpoint naming no referee", reason)
	}
}

// With no usable referee a dispatch fails closed: nothing is sent and no
// decision exists to authorize the call.
func TestRefereeUnavailableFailsClosed(t *testing.T) {
	run := runReferee(t, refereeRoster(""), []enginetest.Option{enginetest.WithToolCalls("author", transport.TriYes)}, nil, nil,
		[]webCall{{tools.NameWebFetch, "https://example.com/"}})
	wantEnd(t, run.h, run.turn, engine.StateSoloComplete, "solo_ok")
	waitFor(t, "the referee outcome", func() bool { return run.done(1) })
	if run.errs[0] == nil || run.outs[0].Allow || run.outs[0].AttemptID != "" {
		t.Fatalf("outcome %+v, err %v: an unusable referee must not authorize", run.outs[0], run.errs[0])
	}
	if n := len(requestsOf(run.h, "author")); n != 2 {
		t.Fatalf("%d requests sent, want only the author's two", n)
	}
	if n := len(refereeAttempts(t, run.h, run.turn)); n != 0 {
		t.Fatalf("%d referee attempts begun", n)
	}
}
