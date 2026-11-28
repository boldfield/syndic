package acceptance

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/candidate"
	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// These tests run the engine's real author and reviewer tool loops against
// fake adapters. Only model replies are scripted: the engine's own executor
// reads the temporary workspace and produces every tool result, so a test
// counts effects through the journal and the executor hook, never by trusting
// a scripted result.

const (
	toolsPara         = "Tools"
	reviewerToolsPara = "Reviewer tools"
)

func init() {
	cover(toolsPara, "an author without `tool_calls: yes` is refused", "TestTools_AuthorWithoutToolCallsRefused")
	cover(toolsPara, "the author's tool set and the reviewers' tool set are independent: definitions are offered per role", "TestTools_DefinitionsOfferedPerRole")
	cover(toolsPara, "a call over the bound is answered `call_limit` and the next author attempt carries no tool definitions and is the last", "TestTools_AuthorBoundFinalNoToolAttempt")
	cover(toolsPara, "a tool step in reply to the final tool-free author attempt ends the turn `tool_limit` and nothing more is dispatched", "TestTools_AuthorToolStepAtBoundEndsToolLimit")
	cover(toolsPara, "a tool step that arrives after the turn was cancelled is quarantined and runs nothing", "TestTools_LateToolStepQuarantined")
	cover(toolsPara, "a call naming a tool the role does not have is answered with a tool error and the other calls in the step still run", "TestTools_RoleGateAnswersCallAndRunsTheRest")
	cover(toolsPara, "a result with calls under finish `length` fails acceptance and executes nothing", "TestTools_CallsUnderLengthFailAcceptance")
	cover(toolsPara, "a result with calls under finish `stop` is malformed", "TestTools_CallsUnderStopAreMalformed")
	cover(toolsPara, "a result with calls under a content-filter finish fails acceptance and executes nothing", "TestTools_CallsUnderContentFilterFailAcceptance")
	cover(toolsPara, "the reviewer's request contains every author tool result verbatim", "TestTools_ReviewerRequestCarriesAuthorResultsVerbatim")
	cover(toolsPara, "the adapter executes no tool", "TestTools_AdapterExecutesNoTool")

	cover(reviewerToolsPara, "`reviewer_tools` naming `write` is invalid at roster load", "TestReviewer_WriteToolInvalidAtLoad")
	cover(reviewerToolsPara, "a reviewer without `tool_calls: yes` is refused when the preset sets `reviewer_tools`", "TestReviewer_WithoutToolCallsRefused")
	cover(reviewerToolsPara, "a reviewer's lookup result appears in that reviewer's next request and in no other participant's request in any round, except a result its block cites, which appears in the author's revision request", "TestReviewer_LookupResultStaysWithItsReviewer")
	cover(reviewerToolsPara, "a reviewer result with tool calls and envelope text is treated as a tool step", "TestReviewer_ToolCallsWithEnvelopeTextIsToolStep")
	cover(reviewerToolsPara, "at the reviewer bound the next request carries no tool definitions, and a tool call in its reply fails the reviewer", "TestReviewer_BoundWithholdsDefinitionsAndToolCallFails")
	cover(reviewerToolsPara, "the verdict record lists the reviewer's tool result ids", "TestReviewer_VerdictRecordListsResultIDs")
	cover(reviewerToolsPara, "two candidates with identical author output and different reviewer lookups have the same candidate hash", "TestReviewer_CandidateHashIgnoresLookups")
	cover(reviewerToolsPara, "a repair request carries no tool definitions", "TestReviewer_RepairCarriesNoDefinitions")
	cover(reviewerToolsPara, "a preset with author `read` and reviewer `web` is accepted by default, and refused with `tools.allow_web_with_read = false`", "TestReviewer_WebWithReadAcceptedByDefaultRefusedWhenOff")
	cover(reviewerToolsPara, "a preset with only reviewer `web` is accepted under either value, and under `false` a submit with it to a tainted conversation is refused `egress` with route `history`", "TestReviewer_WebOnlyAcceptedAndRefusedWhenTainted")
}

const (
	tlNote   = "TOOL-STEP-NOTE"
	tlFileA  = "hello from a"
	tlFileB  = "second file body"
	tlAnswer = "the answer is 41"
	tlRevise = "the answer is 42, and here is why"
)

// tlCall is a tool call as a model returns it.
func tlCall(id, name, args string) transport.ToolCall {
	return transport.ToolCall{ID: id, Type: "function", Function: transport.ToolCallFunction{Name: name, Arguments: args}}
}

func tlRead(id, path string) transport.ToolCall {
	return tlCall(id, tools.NameReadFile, `{"path":"`+path+`"}`)
}

// tlStep is a tool step: the calls, with text beside them.
func tlStep(text string, calls ...transport.ToolCall) fake.Step {
	return fake.Step{
		Kind:            fake.StepKindReply,
		Text:            text,
		Status:          transport.StatusToolCalls,
		TransportStatus: transport.TransportStatusOK,
		ToolCalls:       calls,
	}
}

// tlWorkspace is a workspace holding a.txt and b.txt, each with a marker only
// a lookup of it can bring into a request.
func tlWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"a.txt": tlFileA + "\n", "b.txt": tlFileB + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// tlRoster is the review roster with the "toolreview" preset: the author has
// the tool groups authorTools, every reviewer has reviewerTools (either may be
// empty), the author may make maxCalls calls in the turn, each reviewer
// maxReviewerCalls in a round, and there are rounds revision rounds. One
// reviewer is named "reviewer" unless others are given.
func tlRoster(rounds, maxCalls, maxReviewerCalls int, authorTools, reviewerTools string, reviewers ...string) string {
	if len(reviewers) == 0 {
		reviewers = []string{"reviewer"}
	}
	quoted := make([]string, len(reviewers))
	for i, id := range reviewers {
		quoted[i] = `"` + id + `"`
	}
	preset := "\n[presets.toolreview]\nmode = \"review\"\nauthor = \"author\"\nrequired_reviewers = [" + strings.Join(quoted, ", ") + "]\n"
	if authorTools != "" {
		preset += "tools = [" + authorTools + "]\n"
	}
	if reviewerTools != "" {
		preset += "reviewer_tools = [" + reviewerTools + "]\n"
	}
	return revRoster(rounds, 5, reviewers...) + preset +
		"\n[tools]\nmax_calls_per_turn = " + strconv.Itoa(maxCalls) + "\nmax_reviewer_calls = " + strconv.Itoa(maxReviewerCalls) + "\n"
}

// tlSubmit submits under "toolreview" in dir and returns the turn id.
func (s *suite) tlSubmit(t *testing.T, c *client.Client, id, dir string) string {
	t.Helper()
	return s.tlSubmitReply(t, c, id, dir).TurnID
}

func (s *suite) tlSubmitReply(t *testing.T, c *client.Client, id, dir string) wire.SubmitReply {
	t.Helper()
	preset := "toolreview"
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: id, Input: "what is the answer?", Preset: &preset, Workspace: dir})
	if err != nil {
		t.Fatalf("submit %s: %v", id, err)
	}
	return reply
}

// tlRun submits, waits for the turn to end and returns its id and record.
func (s *suite) tlRun(t *testing.T, c *client.Client, id, dir string) (string, wire.TurnReply) {
	t.Helper()
	turnID := s.tlSubmit(t, c, id, dir)
	s.WaitTerminal(turnID)
	return turnID, s.Turn(t, c, turnID)
}

// tlRequests returns the requests the participant received, in order, except
// terminal deliveries.
func (s *suite) tlRequests(participant string) []transport.CompleteRequest {
	var out []transport.CompleteRequest
	for _, call := range s.Adapters[participant].Calls() {
		if call.Request.Envelope.Phase != "terminal_delivery" {
			out = append(out, call.Request)
		}
	}
	return out
}

// tlCalls returns the participant's journaled calls for the turn in step
// order, once each has its result: a step's execution outlives the terminal
// transaction of a cancel that lands during it.
func (s *suite) tlCalls(t *testing.T, turnID, participant string) []journal.ToolCall {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		calls, err := s.Store.ToolCallsByTurn(turnID, participant)
		if err != nil {
			t.Fatal(err)
		}
		settled := true
		for _, c := range calls {
			settled = settled && c.Result != nil
		}
		if settled || time.Now().After(deadline) {
			return calls
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// tlWantCalls checks the journaled calls' statuses and error codes in order.
func tlWantCalls(t *testing.T, calls []journal.ToolCall, want ...[2]string) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("%d calls journaled, want %d: %+v", len(calls), len(want), calls)
	}
	for i, c := range calls {
		code := ""
		if c.Result != nil {
			code = c.Result.ErrorCode
		}
		if c.Status != want[i][0] || code != want[i][1] {
			t.Errorf("call %d (%s): status %q code %q, want %q %q", i, c.Name, c.Status, code, want[i][0], want[i][1])
		}
	}
}

// tlToolMessage returns the tool message answering call id in req, or nil.
func tlToolMessage(req transport.CompleteRequest, id string) *transport.Message {
	for i, m := range req.Messages {
		if m.Role == "tool" && m.ToolCallID == id {
			return &req.Messages[i]
		}
	}
	return nil
}

// tlText is everything in the messages of the requests.
func tlText(reqs ...transport.CompleteRequest) string {
	var b strings.Builder
	for _, req := range reqs {
		for _, m := range req.Messages {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// tlWantNoDefinitions checks that req is a tool-free attempt that ends with
// the statement.
func tlWantNoDefinitions(t *testing.T, who string, req transport.CompleteRequest, statement string) {
	t.Helper()
	if len(req.Tools) != 0 {
		t.Errorf("%s request offered %d tool definitions, want none", who, len(req.Tools))
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, statement) {
		t.Errorf("%s request ends with %+v, want the statement %q", who, last, statement)
	}
}

// tlResultID returns the journaled result id of the participant's call with
// the provider id, read from the journal when a scripted reply is built.
func tlResultID(s *suite, turnID, participant, providerID string) string {
	calls, err := s.Store.ToolCallsByTurn(turnID, participant)
	if err != nil {
		panic(err)
	}
	for _, c := range calls {
		if c.ProviderCallID == providerID && c.Result != nil {
			return c.Result.ID
		}
	}
	panic("no journaled result for call " + providerID)
}

// tlAcks are the replies to the terminal deliveries that follow a turn.
func tlAcks(steps ...fake.Step) []fake.Step {
	return append(steps, enginetest.Reply("ok"), enginetest.Reply("ok"), enginetest.Reply("ok"))
}

func tlApprove() fake.Step { return verdict(fake.VerdictApprove) }

func TestTools_AuthorWithoutToolCallsRefused(t *testing.T) {
	t.Parallel()
	s := newSuite(t, tlRoster(0, 3, 3, `"read"`, ""), map[string][]fake.Step{},
		enginetest.WithToolCalls("author", transport.TriNo))
	c := s.Dial(t)
	preset := "toolreview"
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "q", Preset: &preset, Workspace: tlWorkspace(t)})
	we := wantRefusal(t, err, wire.ErrorCodePresetInvalid)
	if we.Detail == nil || !strings.Contains(*we.Detail, "no_tool_calls:author") {
		t.Fatalf("refusal detail = %v, want no_tool_calls:author", we.Detail)
	}
	if n := s.journalCount(t, `SELECT COUNT(*) FROM turns`); n != 0 {
		t.Fatalf("%d turns created by the refused submit", n)
	}
	if n := len(s.Adapters["author"].Calls()); n != 0 {
		t.Fatalf("author received %d requests", n)
	}
}

// The author's set comes from `tools` and the reviewers' from
// `reviewer_tools`, and neither implies the other: with only reviewer tools
// the author is offered none, and the reviewer is offered the read tools and
// no write tool.
func TestTools_DefinitionsOfferedPerRole(t *testing.T) {
	t.Parallel()
	s := newSuite(t, tlRoster(0, 3, 3, "", `"read"`), map[string][]fake.Step{
		"author":   tlAcks(enginetest.Reply(tlAnswer)),
		"reviewer": tlAcks(tlApprove()),
	})
	c := s.Dial(t)
	_, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	author := s.tlRequests("author")
	if len(author) != 1 || len(author[0].Tools) != 0 {
		t.Fatalf("author requests = %d, first offered %d tools; want one request offering none", len(author), len(author[0].Tools))
	}
	reviewer := s.tlRequests("reviewer")
	if len(reviewer) != 1 {
		t.Fatalf("%d reviewer requests, want 1", len(reviewer))
	}
	got := map[string]bool{}
	for _, d := range reviewer[0].Tools {
		got[d.Function.Name] = true
	}
	for _, name := range []string{tools.NameReadFile, tools.NameListDir, tools.NameSearchFiles} {
		if !got[name] {
			t.Errorf("reviewer was not offered %s", name)
		}
	}
	if len(got) != 3 {
		t.Errorf("reviewer was offered %v, want exactly the read tools", got)
	}
}

// At its bound the call that exceeds it is answered `call_limit` without
// running, and the next author attempt is the last: no definitions, and the
// statement that no further calls are available. A plain reply to it is the
// candidate, which freezes and goes to the reviewer. The continuation count
// is bounded by the call bound, not by the model: exactly two author
// requests were sent.
func TestTools_AuthorBoundFinalNoToolAttempt(t *testing.T) {
	t.Parallel()
	s := newSuite(t, tlRoster(0, 1, 3, `"read"`, ""), map[string][]fake.Step{
		"author": tlAcks(
			tlStep(tlNote, tlRead("c1", "a.txt"), tlCall("c2", tools.NameListDir, `{"path":"."}`)),
			enginetest.Reply(tlAnswer),
		),
		"reviewer": tlAcks(tlApprove()),
	})
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	tlWantCalls(t, s.tlCalls(t, turnID, "author"),
		[2]string{journal.ToolCallExecuted, ""},
		[2]string{journal.ToolCallError, "call_limit"},
	)

	reqs := s.tlRequests("author")
	if len(reqs) != 2 {
		t.Fatalf("%d author requests, want 2: the bound did not bound the attempts", len(reqs))
	}
	if len(reqs[0].Tools) == 0 {
		t.Fatal("the first author request offered no tool definitions")
	}
	if m := tlToolMessage(reqs[1], "c1"); m == nil || !strings.Contains(m.Content, tlFileA) {
		t.Fatalf("the final attempt lacks the first call's result: %+v", reqs[1].Messages)
	}
	if m := tlToolMessage(reqs[1], "c2"); m == nil || strings.Contains(m.Content, tlFileA) {
		t.Fatalf("the call over the bound was not answered as a refusal: %+v", m)
	}
	tlWantNoDefinitions(t, "final author", reqs[1], "No further tool calls")
	if n := len(s.tlRequests("reviewer")); n != 1 {
		t.Fatalf("%d reviewer requests, want 1: the candidate is the final attempt's reply", n)
	}
}

// A tool step in reply to the final tool-free attempt ends the turn
// `tool_limit`. Its calls run nothing, no further author attempt is sent, and
// the reviewer is never dispatched.
func TestTools_AuthorToolStepAtBoundEndsToolLimit(t *testing.T) {
	t.Parallel()
	s := newSuite(t, tlRoster(0, 1, 3, `"read"`, ""), map[string][]fake.Step{
		"author": tlAcks(
			tlStep(tlNote, tlRead("c1", "a.txt")),
			tlStep(tlNote, tlRead("c2", "a.txt")),
			enginetest.Reply("never sent"),
		),
		"reviewer": tlAcks(tlApprove()),
	})
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	wantTurn(t, turn, engine.StateIncomplete, "tool_limit")
	tlWantCalls(t, s.tlCalls(t, turnID, "author"), [2]string{journal.ToolCallExecuted, ""})
	if reqs := s.tlRequests("author"); len(reqs) != 2 {
		t.Fatalf("%d author requests, want 2: a request followed the final attempt", len(reqs))
	}
	tlWantNoDefinitions(t, "final author", s.tlRequests("author")[1], "No further tool calls")
	if n := len(s.tlRequests("reviewer")); n != 0 {
		t.Fatalf("%d reviewer requests, want 0: no candidate was frozen", n)
	}
	if n := s.journalCount(t, `SELECT COUNT(*) FROM candidates`); n != 0 {
		t.Fatalf("%d candidates frozen", n)
	}
}

// A tool step whose result arrives after the turn was cancelled is late: it is
// quarantined at acceptance, none of its calls is journaled, and the effect
// hook, which every executed call passes, is never reached.
func TestTools_LateToolStepQuarantined(t *testing.T) {
	t.Parallel()
	var effects atomic.Int32
	s := newSuite(t, tlRoster(0, 3, 3, `"read"`, ""), map[string][]fake.Step{
		"author": tlAcks(tlStep(tlNote, tlRead("c1", "a.txt")), enginetest.Reply("never sent")),
	}, enginetest.WithBeforeToolEffect(func(_, _, _ string) { effects.Add(1) }))
	release := s.h.Hold("author")
	c := s.Dial(t)
	turnID := s.tlSubmit(t, c, "s1", tlWorkspace(t))
	s.WaitTurnState(turnID, engine.StateAuthorDispatched)
	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateCancelled, "cancelled")
	release()

	attempts := s.attemptRows(t, turnID, "author", "author")
	if len(attempts) != 1 {
		t.Fatalf("%d author attempts, want 1", len(attempts))
	}
	deadline := time.Now().Add(waitLimit)
	for {
		rows := s.attemptRows(t, turnID, "author", "author")
		if rows[0].Quarantined {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the late tool step's attempt %s was not quarantined", rows[0].ID)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if calls := s.tlCalls(t, turnID, "author"); len(calls) != 0 {
		t.Fatalf("the late step journaled %d calls: %+v", len(calls), calls)
	}
	if n := effects.Load(); n != 0 {
		t.Fatalf("%d tool effects ran for a late step, want 0", n)
	}
	if n := len(s.tlRequests("author")); n != 1 {
		t.Fatalf("%d author requests, want 1: the late result was continued", n)
	}
}

// (Not a §7 clause, so not registered: the referee-await abandonment is
// E-35's. This test is the loop's own check that a cancel during a step
// abandons its unrun calls.) A cancel that lands while a step is executing abandons the rest of its
// calls: the call that was running is journaled executed, the others
// abandoned without result bytes, no further effect runs, and the results of
// the ended step are never dispatched to the model.
func TestToolLoop_CancelDuringStepAbandonsRest(t *testing.T) {
	t.Parallel()
	var s *suite
	var effects atomic.Int32
	reached := make(chan string, 4)
	hook := func(turnID, _, _ string) {
		if effects.Add(1) == 2 {
			reached <- turnID
			if err := s.Engine.Cancel(turnID); err != nil {
				t.Errorf("cancel: %v", err)
			}
		}
	}
	s = newSuite(t, tlRoster(0, 3, 3, `"read"`, ""), map[string][]fake.Step{
		"author": tlAcks(
			tlStep(tlNote, tlRead("c1", "a.txt"), tlRead("c2", "a.txt"), tlCall("c3", tools.NameListDir, `{"path":"."}`)),
			enginetest.Reply("never sent"),
		),
	}, enginetest.WithBeforeToolEffect(hook))
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	wantTurn(t, turn, engine.StateCancelled, "cancelled")
	select {
	case got := <-reached:
		if got != turnID {
			t.Fatalf("effect reached for turn %s, want %s", got, turnID)
		}
	default:
		t.Fatal("the second effect was never reached, so nothing was cancelled during a step")
	}
	tlWantCalls(t, s.tlCalls(t, turnID, "author"),
		[2]string{journal.ToolCallExecuted, ""},
		[2]string{journal.ToolCallAbandoned, ""},
		[2]string{journal.ToolCallAbandoned, ""},
	)
	if n := effects.Load(); n != 2 {
		t.Fatalf("%d effects reached, want 2: the third call ran after the cancel", n)
	}
	for _, call := range s.tlCalls(t, turnID, "author") {
		if call.Status == journal.ToolCallAbandoned && call.Result.ResultSHA256 != "" {
			t.Errorf("abandoned call %s carries result bytes", call.ProviderCallID)
		}
	}
	if n := len(s.tlRequests("author")); n != 1 {
		t.Fatalf("%d author requests, want 1: results of an ended step were dispatched", n)
	}
}

func TestReviewer_WriteToolInvalidAtLoad(t *testing.T) {
	t.Parallel()
	h := enginetest.New(t, tlRoster(0, 3, 3, "", `"read", "write"`), map[string][]fake.Step{}, enginetest.NoStart())
	err := h.Engine.Start()
	var re *engine.RosterError
	if !errors.As(err, &re) {
		t.Fatalf("Start = %v, want a roster refusal", err)
	}
	if !strings.Contains(err.Error(), "invalid reviewer tool") {
		t.Fatalf("refusal = %q, want it to name the invalid reviewer tool", err)
	}
	// The same roster with only read loads.
	newSuite(t, tlRoster(0, 3, 3, "", `"read"`), map[string][]fake.Step{})
}

func TestReviewer_WithoutToolCallsRefused(t *testing.T) {
	t.Parallel()
	for name, v := range map[string]transport.Tri{"no": transport.TriNo, "unknown": transport.TriUnknown} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newSuite(t, tlRoster(0, 3, 3, "", `"read"`), map[string][]fake.Step{},
				enginetest.WithToolCalls("reviewer", v))
			c := s.Dial(t)
			preset := "toolreview"
			_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "q", Preset: &preset, Workspace: tlWorkspace(t)})
			we := wantRefusal(t, err, wire.ErrorCodePresetInvalid)
			if we.Detail == nil || !strings.Contains(*we.Detail, "no_tool_calls:reviewer") {
				t.Fatalf("refusal detail = %v, want no_tool_calls:reviewer", we.Detail)
			}
			if n := s.journalCount(t, `SELECT COUNT(*) FROM turns`); n != 0 {
				t.Fatalf("%d turns created by the refused submit", n)
			}
		})
	}
}

// Reviewer r1 looks up both files and its block cites the result of a.txt
// only. r1's own next request carries both results; reviewer r2's requests
// carry neither in either round; the author's first request carries neither;
// and the author's revision request carries the cited result and not the
// uncited one.
func TestReviewer_LookupResultStaysWithItsReviewer(t *testing.T) {
	t.Parallel()
	var s *suite
	block := verdict(func(hash, attempt string) string {
		// The turn is the only one of the suite, so its id is on the attempt.
		turnID := s.tlOnlyTurn()
		cited := tlResultID(s, turnID, "r1", "r1a")
		return fake.VerdictBlock(hash, attempt, "b1", "high", "see "+cited, "fix it", "wrong")
	})
	s = newSuite(t, tlRoster(1, 3, 3, `"read"`, `"read"`, "r1", "r2"), map[string][]fake.Step{
		"author": tlAcks(enginetest.Reply(tlAnswer), enginetest.Reply(tlRevise)),
		"r1": tlAcks(
			tlStep(tlNote, tlRead("r1a", "a.txt"), tlRead("r1b", "b.txt")),
			block,
			revV2(1, "approve", "", "", "b1 withdrawn"),
		),
		"r2": tlAcks(tlApprove(), revV2(1, "approve", "", "")),
	})
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	r1 := s.tlRequests("r1")
	if len(r1) != 3 {
		t.Fatalf("%d r1 requests, want a lookup, a verdict and a round-1 verdict", len(r1))
	}
	for _, id := range []string{"r1a", "r1b"} {
		if tlToolMessage(r1[1], id) == nil {
			t.Fatalf("r1's next request lacks the result of %s", id)
		}
	}
	for _, marker := range []string{tlFileA, tlFileB, tlNote} {
		if strings.Contains(tlText(s.tlRequests("r2")...), marker) {
			t.Errorf("r2 saw r1's lookup data %q", marker)
		}
	}
	author := s.tlRequests("author")
	if len(author) != 2 {
		t.Fatalf("%d author requests, want the candidate and one revision", len(author))
	}
	if first := tlText(author[0]); strings.Contains(first, tlFileA) || strings.Contains(first, tlFileB) {
		t.Fatal("r1's lookup reached the author before any block cited it")
	}
	revision := tlText(author[1])
	if strings.Count(revision, tlFileA) != 1 {
		t.Errorf("the cited result appears %d times in the revision request, want once", strings.Count(revision, tlFileA))
	}
	if strings.Contains(revision, tlFileB) || strings.Contains(revision, tlNote) {
		t.Error("the revision request carries an uncited lookup result or the step's note")
	}
	tlWantCalls(t, s.tlCalls(t, turnID, "r1"),
		[2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallExecuted, ""})
	if calls := s.tlCalls(t, turnID, "r2"); len(calls) != 0 {
		t.Fatalf("r2 journaled %d calls", len(calls))
	}
}

// tlOnlyTurn is the id of the suite's one turn.
func (s *suite) tlOnlyTurn() string {
	var id string
	if err := s.DB.Conn().QueryRow(`SELECT id FROM turns`).Scan(&id); err != nil {
		panic(err)
	}
	return id
}

// A reviewer's reply with tool calls is a tool step even when its text is a
// well-formed approval for the candidate: it is not parsed, the calls run, and
// only the reply to the continuation is the verdict.
func TestReviewer_ToolCallsWithEnvelopeTextIsToolStep(t *testing.T) {
	t.Parallel()
	step := tlStep("", tlRead("r1", "a.txt"))
	step.Build = func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	}
	s := newSuite(t, tlRoster(0, 3, 3, "", `"read"`), map[string][]fake.Step{
		"author":   tlAcks(enginetest.Reply(tlAnswer)),
		"reviewer": tlAcks(step, revBlock0("b1", "e")),
	})
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	// The envelope text of the first reply would have approved; the verdict is
	// the continuation's block.
	if turn.State != engine.StateIncomplete || turn.TerminalReason == nil || !strings.HasPrefix(*turn.TerminalReason, "block:") {
		t.Fatalf("turn = %q %v, want incomplete with an open block", turn.State, turn.TerminalReason)
	}
	reqs := s.tlRequests("reviewer")
	if len(reqs) != 2 {
		t.Fatalf("%d reviewer requests, want a tool step and its continuation", len(reqs))
	}
	rows, err := s.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AttemptID != reqs[1].Envelope.AttemptID {
		t.Fatalf("verdicts = %+v, want one, from the continuation attempt %s", rows, reqs[1].Envelope.AttemptID)
	}
	tlWantCalls(t, s.tlCalls(t, turnID, "reviewer"), [2]string{journal.ToolCallExecuted, ""})
}

// At the reviewer's bound the next request carries no definitions and says no
// further lookups are available. Tool calls in the reply to that attempt fail
// the reviewer: nothing of the step runs, no verdict is recorded and no
// request follows.
func TestReviewer_BoundWithholdsDefinitionsAndToolCallFails(t *testing.T) {
	t.Parallel()
	s := newSuite(t, tlRoster(0, 3, 1, "", `"read"`), map[string][]fake.Step{
		"author": tlAcks(enginetest.Reply(tlAnswer)),
		"reviewer": tlAcks(
			tlStep(tlNote, tlRead("r1", "a.txt")),
			tlStep(tlNote, tlRead("r2", "b.txt")),
			tlApprove(),
		),
	})
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:tool_calls_when_limited")
	reqs := s.tlRequests("reviewer")
	if len(reqs) != 2 {
		t.Fatalf("%d reviewer requests, want 2: one tool-free attempt is offered and no more", len(reqs))
	}
	if len(reqs[0].Tools) == 0 {
		t.Error("the first reviewer request offered no tool definitions")
	}
	tlWantNoDefinitions(t, "reviewer at the bound", reqs[1], "No further lookups are available")
	tlWantCalls(t, s.tlCalls(t, turnID, "reviewer"), [2]string{journal.ToolCallExecuted, ""})
	if strings.Contains(tlText(reqs[1]), tlFileB) {
		t.Error("the call in the tool-free attempt's reply ran")
	}
	rows, err := s.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("%d verdicts recorded for a failed reviewer", len(rows))
	}
}

// The verdict record lists the ids of the reviewer's tool results.
func TestReviewer_VerdictRecordListsResultIDs(t *testing.T) {
	t.Parallel()
	s := newSuite(t, tlRoster(0, 3, 3, "", `"read"`), map[string][]fake.Step{
		"author": tlAcks(enginetest.Reply(tlAnswer)),
		"reviewer": tlAcks(
			tlStep(tlNote, tlRead("r1", "a.txt"), tlRead("r2", "b.txt")),
			tlApprove(),
		),
	})
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	calls := s.tlCalls(t, turnID, "reviewer")
	tlWantCalls(t, calls, [2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallExecuted, ""})
	rows, err := s.Store.ListVerdicts(turnID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("verdicts = %+v, %v; want one", rows, err)
	}
	ev, err := s.Store.GetVerdictEvidence(rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.ResultIDs) != 2 || ev.ResultIDs[0] != calls[0].Result.ID || ev.ResultIDs[1] != calls[1].Result.ID {
		t.Fatalf("verdict evidence results = %v, want [%s %s]", ev.ResultIDs, calls[0].Result.ID, calls[1].Result.ID)
	}
}

// The candidate hash is computed from the manifest, the turn, the author's
// attempt, the context and the author's output alone. In two runs whose
// reviewers looked up different things, the turn's hash is the one
// recomputed without any lookup, so a lookup is no part of it.
func TestReviewer_CandidateHashIgnoresLookups(t *testing.T) {
	t.Parallel()
	lookups := [][]transport.ToolCall{
		{tlRead("r1", "a.txt")},
		{tlRead("r1", "b.txt"), tlRead("r2", "a.txt")},
	}
	for i, calls := range lookups {
		s := newSuite(t, tlRoster(0, 3, 3, "", `"read"`), map[string][]fake.Step{
			"author":   tlAcks(enginetest.Reply(tlAnswer)),
			"reviewer": tlAcks(tlStep(tlNote, calls...), tlApprove()),
		})
		c := s.Dial(t)
		sub := s.tlSubmitReply(t, c, "s1", tlWorkspace(t))
		s.WaitTerminal(sub.TurnID)
		turn := s.Turn(t, c, sub.TurnID)
		wantTurn(t, turn, engine.StateApproved, "all_approved")
		if turn.CandidateHash == nil {
			t.Fatalf("lookups %d: turn has no candidate hash", i)
		}
		if n := len(s.tlCalls(t, sub.TurnID, "reviewer")); n != len(calls) {
			t.Fatalf("lookups %d: the reviewer journaled %d calls, want %d", i, n, len(calls))
		}
		manifest := ""
		if mh := s.Engine.Hello().ManifestHash; mh != nil {
			manifest = *mh
		}
		want, err := candidate.Hash(candidate.Identity{
			ManifestHash:    manifest,
			TurnID:          sub.TurnID,
			AuthorAttemptID: s.attemptRows(t, sub.TurnID, "author", "author")[0].ID,
			ContextHash:     sub.ContextHash,
			Content:         []byte(tlAnswer),
		})
		if err != nil {
			t.Fatal(err)
		}
		if *turn.CandidateHash != want {
			t.Fatalf("lookups %d: candidate hash %s, recomputed from the author's output alone %s", i, *turn.CandidateHash, want)
		}
	}
}

// A malformed verdict after a lookup is repaired with the reviewer's own
// result in view and no tool definitions.
func TestReviewer_RepairCarriesNoDefinitions(t *testing.T) {
	t.Parallel()
	s := newSuite(t, tlRoster(0, 3, 3, "", `"read"`), map[string][]fake.Step{
		"author": tlAcks(enginetest.Reply(tlAnswer)),
		"reviewer": tlAcks(
			tlStep(tlNote, tlRead("r1", "a.txt")),
			enginetest.Reply("I have looked at it and have thoughts."),
			tlApprove(),
		),
	})
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	reqs := s.tlRequests("reviewer")
	if len(reqs) != 3 {
		t.Fatalf("%d reviewer requests, want a lookup, a malformed reply and a repair", len(reqs))
	}
	if len(reqs[0].Tools) == 0 || len(reqs[1].Tools) == 0 {
		t.Fatalf("the lookup and the continuation offered %d and %d tools, want definitions", len(reqs[0].Tools), len(reqs[1].Tools))
	}
	repair := reqs[2]
	rows := s.attemptRows(t, turnID, "reviewer", "review")
	var repairRow *journal.AttemptRow
	for i := range rows {
		if rows[i].ID == repair.Envelope.AttemptID {
			repairRow = &rows[i]
		}
	}
	if repairRow == nil || repairRow.RepairOf == nil {
		t.Fatalf("the third request is not a repair attempt: %+v", repairRow)
	}
	if len(repair.Tools) != 0 {
		t.Errorf("the repair request offered %d tool definitions, want none", len(repair.Tools))
	}
	if m := tlToolMessage(repair, "r1"); m == nil || !strings.Contains(m.Content, tlFileA) {
		t.Errorf("the repair lacks the reviewer's own result: %+v", repair.Messages)
	}
	tlWantCalls(t, s.tlCalls(t, turnID, "reviewer"), [2]string{journal.ToolCallExecuted, ""})
}

// A call naming a tool the role does not have is answered with a tool error
// and journaled; the other calls of the step still run. The author holds only
// `read`, the reviewer only `read`, and each names write_file beside a read.
func TestTools_RoleGateAnswersCallAndRunsTheRest(t *testing.T) {
	t.Parallel()
	var effects atomic.Int32
	write := tlCall("w", tools.NameWriteFile, `{"path":"new.txt","content":"x"}`)
	s := newSuite(t, tlRoster(0, 3, 3, `"read"`, `"read"`), map[string][]fake.Step{
		"author": tlAcks(
			tlStep(tlNote, write, tlRead("ar", "a.txt")),
			enginetest.Reply(tlAnswer),
		),
		"reviewer": tlAcks(
			tlStep(tlNote, write, tlRead("rr", "b.txt")),
			tlApprove(),
		),
	}, enginetest.WithBeforeToolEffect(func(_, _, _ string) { effects.Add(1) }))
	dir := tlWorkspace(t)
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", dir)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	for _, who := range []string{"author", "reviewer"} {
		tlWantCalls(t, s.tlCalls(t, turnID, who),
			[2]string{journal.ToolCallError, "not_offered"},
			[2]string{journal.ToolCallExecuted, ""},
		)
	}
	if n := effects.Load(); n != 2 {
		t.Fatalf("%d effects ran, want 2: only the two reads", n)
	}
	for who, id := range map[string]string{"author": "ar", "reviewer": "rr"} {
		reqs := s.tlRequests(who)
		if len(reqs) < 2 {
			t.Fatalf("%s: %d requests, want a continuation", who, len(reqs))
		}
		if m := tlToolMessage(reqs[1], "w"); m == nil || strings.Contains(m.Content, "new.txt written") {
			t.Errorf("%s: the refused call was not answered with an error: %+v", who, m)
		}
		want := tlFileA
		if who == "reviewer" {
			want = tlFileB
		}
		if m := tlToolMessage(reqs[1], id); m == nil || !strings.Contains(m.Content, want) {
			t.Errorf("%s: the read beside the refused call was not delivered: %+v", who, m)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("the refused write touched the workspace: %v", err)
	}
}

// tlBadFinishStep is a result carrying a call under a finish status that is
// not the dialect's tool-call finish.
func tlBadFinishStep(status transport.CompletionStatus) fake.Step {
	step := tlStep(tlNote, tlRead("c1", "a.txt"))
	step.Status = status
	return step
}

// tlWantNothingExecuted checks that no effect ran, no call was journaled for
// anyone, and the participant was sent no continuation.
func (s *suite) tlWantNothingExecuted(t *testing.T, turnID string, effects *atomic.Int32, who string) {
	t.Helper()
	if n := effects.Load(); n != 0 {
		t.Errorf("%d tool effects ran, want 0", n)
	}
	for _, p := range []string{"author", "reviewer"} {
		if calls := s.tlCalls(t, turnID, p); len(calls) != 0 {
			t.Errorf("%s: %d calls journaled, want 0: %+v", p, len(calls), calls)
		}
	}
	if n := len(s.tlRequests(who)); n != 1 {
		t.Errorf("%s received %d requests, want 1: the result was continued", who, n)
	}
}

// Calls under `length`, or under a content-filter (refusal) finish, are not a
// tool step: the result fails acceptance as a truncated or refused one does
// today, nothing runs and nothing is journaled, in an author's reply and in a
// reviewer's.
func TestTools_CallsUnderLengthFailAcceptance(t *testing.T) {
	t.Parallel()
	tlCallsUnderFailingFinish(t, transport.StatusTruncated)
}

func TestTools_CallsUnderContentFilterFailAcceptance(t *testing.T) {
	t.Parallel()
	tlCallsUnderFailingFinish(t, transport.StatusRefused)
}

func tlCallsUnderFailingFinish(t *testing.T, status transport.CompletionStatus) {
	t.Helper()
	t.Run("author", func(t *testing.T) {
		t.Parallel()
		var effects atomic.Int32
		s := newSuite(t, tlRoster(0, 3, 3, `"read"`, ""), map[string][]fake.Step{
			"author": tlAcks(tlBadFinishStep(status), enginetest.Reply("never sent")),
		}, enginetest.WithBeforeToolEffect(func(_, _, _ string) { effects.Add(1) }))
		c := s.Dial(t)
		turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
		if turn.State != engine.StateIncomplete {
			t.Fatalf("turn = %q %v, want incomplete", turn.State, turn.TerminalReason)
		}
		s.tlWantNothingExecuted(t, turnID, &effects, "author")
		if n := len(s.tlRequests("reviewer")); n != 0 {
			t.Errorf("%d reviewer requests, want 0: nothing was frozen", n)
		}
	})
	t.Run("reviewer", func(t *testing.T) {
		t.Parallel()
		var effects atomic.Int32
		s := newSuite(t, tlRoster(0, 3, 3, "", `"read"`), map[string][]fake.Step{
			"author":   tlAcks(enginetest.Reply(tlAnswer)),
			"reviewer": tlAcks(tlBadFinishStep(status), tlApprove()),
		}, enginetest.WithBeforeToolEffect(func(_, _, _ string) { effects.Add(1) }))
		c := s.Dial(t)
		turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
		if turn.State == engine.StateApproved {
			t.Fatalf("turn approved on a reviewer result that failed acceptance")
		}
		s.tlWantNothingExecuted(t, turnID, &effects, "reviewer")
		if rows, err := s.Store.ListVerdicts(turnID); err != nil || len(rows) != 0 {
			t.Errorf("verdicts = %+v, %v; want none", rows, err)
		}
	})
}

// Calls attached to a `stop` finish are malformed: nothing runs and nothing is
// journaled. The author's reply ends the turn; the reviewer's is a malformed
// verdict, repaired, never a tool step.
func TestTools_CallsUnderStopAreMalformed(t *testing.T) {
	t.Parallel()
	t.Run("author", func(t *testing.T) {
		t.Parallel()
		var effects atomic.Int32
		s := newSuite(t, tlRoster(0, 3, 3, `"read"`, ""), map[string][]fake.Step{
			"author": tlAcks(tlBadFinishStep(transport.StatusComplete), enginetest.Reply("never sent")),
		}, enginetest.WithBeforeToolEffect(func(_, _, _ string) { effects.Add(1) }))
		c := s.Dial(t)
		turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
		if turn.State != engine.StateIncomplete {
			t.Fatalf("turn = %q %v, want incomplete", turn.State, turn.TerminalReason)
		}
		s.tlWantNothingExecuted(t, turnID, &effects, "author")
	})
	t.Run("reviewer", func(t *testing.T) {
		t.Parallel()
		var effects atomic.Int32
		s := newSuite(t, tlRoster(0, 3, 3, "", `"read"`), map[string][]fake.Step{
			"author":   tlAcks(enginetest.Reply(tlAnswer)),
			"reviewer": tlAcks(tlBadFinishStep(transport.StatusComplete), tlApprove()),
		}, enginetest.WithBeforeToolEffect(func(_, _, _ string) { effects.Add(1) }))
		c := s.Dial(t)
		turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
		if turn.State == engine.StateApproved {
			t.Fatalf("turn approved on a malformed reviewer result")
		}
		s.tlWantNothingExecuted(t, turnID, &effects, "reviewer")
	})
}

// The reviewer's request carries every author tool result verbatim, the
// same bytes the author itself was given.
func TestTools_ReviewerRequestCarriesAuthorResultsVerbatim(t *testing.T) {
	t.Parallel()
	s := newSuite(t, tlRoster(0, 3, 3, `"read"`, ""), map[string][]fake.Step{
		"author": tlAcks(
			tlStep(tlNote, tlRead("a1", "a.txt"), tlRead("a2", "b.txt")),
			enginetest.Reply(tlAnswer),
		),
		"reviewer": tlAcks(tlApprove()),
	})
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", tlWorkspace(t))
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	tlWantCalls(t, s.tlCalls(t, turnID, "author"),
		[2]string{journal.ToolCallExecuted, ""}, [2]string{journal.ToolCallExecuted, ""})
	author := s.tlRequests("author")
	reviewer := s.tlRequests("reviewer")
	if len(author) != 2 || len(reviewer) != 1 {
		t.Fatalf("%d author and %d reviewer requests, want 2 and 1", len(author), len(reviewer))
	}
	seen := tlText(reviewer[0])
	for _, id := range []string{"a1", "a2"} {
		if m := tlToolMessage(author[1], id); m == nil || m.Content == "" {
			t.Fatalf("the author's continuation lacks result %s", id)
		}
	}
	if !strings.Contains(seen, tools.NameReadFile) {
		t.Error("the reviewer's request does not name the author's calls")
	}
	for _, body := range []string{tlFileA + "\n", tlFileB + "\n"} {
		if !strings.Contains(seen, body) {
			t.Errorf("the reviewer's request lacks the author result %q verbatim", body)
		}
	}
}

// The adapter executes no tool: every effect that ran passed the engine's
// executor, one per executed call, and the fake adapter, which only returned
// the scripted step, left the workspace untouched even for a write call.
func TestTools_AdapterExecutesNoTool(t *testing.T) {
	t.Parallel()
	var effects atomic.Int32
	s := newSuite(t, tlRoster(0, 3, 3, `"read"`, ""), map[string][]fake.Step{
		"author": tlAcks(
			tlStep(tlNote, tlRead("a1", "a.txt"), tlCall("w", tools.NameWriteFile, `{"path":"new.txt","content":"x"}`)),
			enginetest.Reply(tlAnswer),
		),
		"reviewer": tlAcks(tlApprove()),
	}, enginetest.WithBeforeToolEffect(func(_, _, _ string) { effects.Add(1) }))
	dir := tlWorkspace(t)
	c := s.Dial(t)
	turnID, turn := s.tlRun(t, c, "s1", dir)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	executed := 0
	for _, call := range s.tlCalls(t, turnID, "author") {
		if call.Status == journal.ToolCallExecuted {
			executed++
		}
	}
	if int(effects.Load()) != executed || executed != 1 {
		t.Fatalf("%d effects and %d executed calls, want one of each", effects.Load(), executed)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("a call outside the role ran: %v", err)
	}
}

// tlWebRoster is the review roster with a referee model on the endpoint and
// the presets "webread" (author read, reviewer web, left out when withWebRead
// is false: under allow_web_with_read = false the roster would not load),
// "webonly" (reviewer web only) and "reader" (a solo author that reads).
// webWithRead, when not empty, sets tools.allow_web_with_read for the roster.
func tlWebRoster(webWithRead string, withWebRead bool) string {
	r := enginetest.RosterTOML
	r = strings.Replace(r, `models = ["llama2", "llama3"]`, `models = ["llama2", "llama3", "refmodel"]`, 1)
	r = strings.Replace(r, "tokens_per_byte_max = 1.0", "tokens_per_byte_max = 1.0\nreferee_model = \"refmodel\"", 1)
	if withWebRead {
		r += `
[presets.webread]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["read"]
reviewer_tools = ["web"]
`
	}
	r += `
[presets.webonly]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
reviewer_tools = ["web"]

[presets.reader]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["read"]
`
	if webWithRead != "" {
		r += "\n[tools]\nallow_web_with_read = " + webWithRead + "\n"
	}
	return r
}

func (s *suite) tlSubmitPreset(c *client.Client, id, preset, dir string, conv *string) (wire.SubmitReply, error) {
	return c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: id, Input: "q " + id, Preset: &preset, Workspace: dir, ConversationID: conv})
}

// A preset with author `read` and reviewer `web` is accepted by default and
// runs to approval, and the roster that sets `tools.allow_web_with_read =
// false` is refused at load naming the reviewer given web and the author whose
// file content would reach it. (The participants reply and start log that name
// the exposed reviewers are not asserted: the engine has no such surface at
// this commit, and §7's egress paragraph is E-33's.)
func TestReviewer_WebWithReadAcceptedByDefaultRefusedWhenOff(t *testing.T) {
	t.Parallel()
	s := newSuite(t, tlWebRoster("", true), map[string][]fake.Step{
		"author":   tlAcks(enginetest.Reply(tlAnswer)),
		"reviewer": tlAcks(tlApprove()),
	}, enginetest.WithModelToolCalls("local", "refmodel", transport.TriYes))
	c := s.Dial(t)
	reply, err := s.tlSubmitPreset(c, "s1", "webread", tlWorkspace(t), nil)
	if err != nil {
		t.Fatalf("submit under author read and reviewer web: %v", err)
	}
	s.WaitTerminal(reply.TurnID)
	wantTurn(t, s.Turn(t, c, reply.TurnID), engine.StateApproved, "all_approved")

	h := enginetest.New(t, tlWebRoster("false", true), map[string][]fake.Step{}, enginetest.NoStart(),
		enginetest.WithModelToolCalls("local", "refmodel", transport.TriYes))
	err = h.Engine.Start()
	var re *engine.RosterError
	if !errors.As(err, &re) {
		t.Fatalf("Start = %v, want a roster refusal", err)
	}
	for _, want := range []string{"webread", "allow_web_with_read=false", "reviewer", "author"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

// A preset whose only tool anywhere is a reviewer's `web` is accepted under
// either value of the setting; under `false`, a submit with it to a
// conversation that a read made tainted is refused `egress` with route
// `history`, naming the reviewer, and records nothing.
func TestReviewer_WebOnlyAcceptedAndRefusedWhenTainted(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"true", "false"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			s := newSuite(t, tlWebRoster(value, false), map[string][]fake.Step{
				"author":   tlAcks(enginetest.Reply(tlAnswer), enginetest.Reply(tlAnswer), enginetest.Reply(tlAnswer)),
				"reviewer": tlAcks(tlApprove()),
			}, enginetest.WithModelToolCalls("local", "refmodel", transport.TriYes))
			c := s.Dial(t)
			fresh, err := s.tlSubmitPreset(c, "fresh", "webonly", "", nil)
			if err != nil {
				t.Fatalf("web-only preset, allow_web_with_read = %s: %v", value, err)
			}
			s.WaitTerminal(fresh.TurnID)
			wantTurn(t, s.Turn(t, c, fresh.TurnID), engine.StateApproved, "all_approved")

			read, err := s.tlSubmitPreset(c, "read", "reader", tlWorkspace(t), nil)
			if err != nil {
				t.Fatalf("reader submit: %v", err)
			}
			s.WaitTerminal(read.TurnID)
			wantTurn(t, s.Turn(t, c, read.TurnID), engine.StateSoloComplete, "solo_ok")
			conv := read.ConversationID

			before := s.journalCount(t, `SELECT COUNT(*) FROM turns`)
			_, err = s.tlSubmitPreset(c, "tainted", "webonly", "", &conv)
			if value == "true" {
				if err != nil {
					t.Fatalf("web-only submit to a tainted conversation under true: %v", err)
				}
				return
			}
			we := wantRefusal(t, err, wire.ErrorCodeEgress)
			if we.Participant == nil || *we.Participant != "reviewer" || we.Route == nil || *we.Route != "history" {
				t.Fatalf("refusal = %+v, want participant reviewer, route history", we)
			}
			if n := s.journalCount(t, `SELECT COUNT(*) FROM turns`); n != before {
				t.Fatalf("the refused submit created %d turns", n-before)
			}
		})
	}
}
