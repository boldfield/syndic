package acceptance

import (
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// These tests run the file-egress rule of the design's §7 and the admission of
// rendered tool requests through the engine's real roster load, submit and
// tool loop with fake adapters. Taint is read from the journal's turn row,
// reservations from the attempt records, and rendered sizes are recomputed
// from the requests the fake adapters actually received.

const egressPara = "Egress"

func init() {
	cover(egressPara, "a preset whose author has `write` and whose reviewer is on an endpoint outside `tools.file_egress_destinations` is refused at roster load naming the diff route", "TestEgress_WriteOnlyDiffRouteRefusedAtLoad")
	cover(egressPara, "a preset with no `read` and no `write` anywhere is accepted on any endpoints", "TestEgress_NoReadNoWriteAcceptedOnAnyEndpoint")
	cover(egressPara, "a submit under that preset to a conversation whose earlier turn ran with `read` is refused `egress` with route `history` when a participant's endpoint is outside the destinations, and accepted when all are inside", "TestEgress_TaintedConversationHistoryRoute")
	cover(egressPara, "the same refusal holds when the only tainted turn is trimmed out by `context_from_turn` and a tool-free turn written after it is in view", "TestEgress_TrimCannotClearTaint")
	cover(egressPara, "a turn's `file_tainted` is set when any earlier turn of its conversation is tainted, whether or not that turn is in the included history", "TestEgress_FileTaintedSetTransitively")
	cover(egressPara, "a request with tool definitions and tool results reserves the rendered size including definitions, schemas, call arguments and framing", "TestEgress_ToolRequestReservesRenderedSize")
	cover(egressPara, "a request whose definitions alone exceed the admission bound is refused before dispatch", "TestEgress_DefinitionsAloneExceedAdmissionBound")
	cover(egressPara, "A preset whose only tool anywhere is `web`, in an untainted conversation, is unaffected", "TestEgress_PureWebUntaintedUnaffected")
}

const (
	egFirstAnswer  = "FIRST-ANSWER-MARKER"
	egSecondAnswer = "SECOND-ANSWER-MARKER"
	egThirdAnswer  = "THIRD-ANSWER-MARKER"
)

// egEdit replaces the first old in r with new, failing if r has none.
func egEdit(t *testing.T, r, old, new string) string {
	t.Helper()
	if !strings.Contains(r, old) {
		t.Fatalf("roster has no %q", old)
	}
	return strings.Replace(r, old, new, 1)
}

// egRoster is the harness roster with the reviewer on "lan", an endpoint with
// remote execution that is outside tools.file_egress_destinations unless
// destinations names it, and the presets:
//
//	reader    solo, the author reads
//	webauthor review, the author has only web
//	plain     solo, no tools        (the pair differs only in `tools`,
//	tooled    solo, author reads     as the admission test needs)
//
// plus the harness's tool-free "review" and "solo". extra is appended
// verbatim, and budgets, when not empty, becomes the roster's [budgets].
func egRoster(t *testing.T, budgets string, extra string, destinations ...string) string {
	t.Helper()
	r := enginetest.RosterTOML
	r = egEdit(t, r, `models = ["llama2", "llama3"]`, `models = ["llama2", "llama3", "refmodel"]`)
	r = egEdit(t, r, "tokens_per_byte_max = 1.0", "tokens_per_byte_max = 1.0\nreferee_model = \"refmodel\"")
	r = egEdit(t, r, "id = \"reviewer\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"",
		"id = \"reviewer\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"lan\"")
	r = egEdit(t, r, "local_only = true", "local_only = false")
	r = egEdit(t, r, "[presets.review]", `[[endpoints]]
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

[presets.reader]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["read"]

[presets.webauthor]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["web"]

[presets.plain]
mode = "solo"
author = "author"
required_reviewers = []

[presets.tooled]
mode = "solo"
author = "author"
required_reviewers = []
tools = ["read"]

[presets.review]`)
	if budgets != "" {
		r = egEdit(t, r, "[revision]", "[budgets]\n"+budgets+"\n\n[revision]")
	}
	r += extra
	if len(destinations) > 0 {
		r += "\n[tools]\nfile_egress_destinations = [\"" + strings.Join(destinations, `", "`) + "\"]\n"
	}
	return r
}

// egOptions give every participant and referee model tool calls.
func egOptions(more ...enginetest.Option) []enginetest.Option {
	return append([]enginetest.Option{
		enginetest.WithToolCalls("author", transport.TriYes),
		enginetest.WithToolCalls("reviewer", transport.TriYes),
		enginetest.WithModelToolCalls("local", "refmodel", transport.TriYes),
		enginetest.WithModelToolCalls("lan", "refmodel", transport.TriYes),
	}, more...)
}

// egSuite starts a suite on the roster. The author's scripted turns are given
// in order, each followed by the replies the turn's terminal deliveries take.
func egSuite(t *testing.T, roster string, author, reviewer []fake.Step, more ...enginetest.Option) (*suite, *client.Client) {
	t.Helper()
	s := newSuite(t, roster, map[string][]fake.Step{"author": author, "reviewer": reviewer}, egOptions(more...)...)
	return s, s.Dial(t)
}

// egRun submits under the preset, waits for the turn to end and its terminal
// deliveries to finish, and returns the reply and the turn's record.
func (s *suite) egRun(t *testing.T, c *client.Client, id, preset string, conv, from *string) (wire.SubmitReply, wire.TurnReply) {
	t.Helper()
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: id, Input: "q " + id, Preset: &preset, ConversationID: conv, ContextFromTurn: from, Workspace: s.egWorkspace(t),
	})
	if err != nil {
		t.Fatalf("submit %s under %s: %v", id, preset, err)
	}
	return reply, s.budgetsSettled(t, c, reply.TurnID)
}

// egWorkspaces holds the one workspace each suite's turns share: a
// conversation's turns must all name the same workspace.
var egWorkspaces sync.Map

// egWorkspace is a workspace holding a.txt and b.txt, made once per suite.
func (s *suite) egWorkspace(t *testing.T) string {
	t.Helper()
	dir, _ := egWorkspaces.LoadOrStore(s, tlWorkspace(t))
	return dir.(string)
}

// egTainted is the journal's file_tainted of the turn.
func (s *suite) egTainted(t *testing.T, turnID string) bool {
	t.Helper()
	row, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return row.FileTainted
}

// egTurns scripts the author for a run of turns, each ending after its steps.
// A solo turn has no terminal delivery to the author; a review turn takes the
// acknowledgements tlAcks appends.
func egTurns(turns ...[]fake.Step) []fake.Step {
	var out []fake.Step
	for _, steps := range turns {
		out = append(out, steps...)
	}
	return out
}

// egReadTurn is an author turn that reads a.txt and then answers.
func egReadTurn(answer string) []fake.Step {
	return []fake.Step{tlStep(tlNote, tlRead("r1", "a.txt")), enginetest.Reply(answer)}
}

func egWantEgress(t *testing.T, err error, participant, route string) {
	t.Helper()
	we := wantRefusal(t, err, wire.ErrorCodeEgress)
	if we.Participant == nil || *we.Participant != participant || we.Route == nil || *we.Route != route {
		t.Fatalf("egress refusal = %+v, want participant %q route %q", we, participant, route)
	}
}

// taintedConversation runs a turn that reads and a tool-free turn after it and
// returns the conversation and the two turns. It checks that the reading turn
// is tainted by its tools (a read executed) and the later one by the history
// alone, so a refusal that follows can only come from the taint.
func (s *suite) egTaintedConversation(t *testing.T, c *client.Client) (conv, readTurn, freeTurn string) {
	t.Helper()
	read, turn := s.egRun(t, c, "read", "reader", nil, nil)
	wantTurn(t, turn, engine.StateSoloComplete, "solo_ok")
	if !s.egTainted(t, read.TurnID) {
		t.Fatal("a turn that ran with read is not file_tainted")
	}
	if calls := s.tlCalls(t, read.TurnID, "author"); len(calls) != 1 || calls[0].Name != tools.NameReadFile {
		t.Fatalf("the reading turn's journaled calls = %+v, want one read_file", calls)
	}
	conv = read.ConversationID
	free, turn := s.egRun(t, c, "free", "plain", &conv, nil)
	wantTurn(t, turn, engine.StateSoloComplete, "solo_ok")
	if len(s.Adapters["author"].Calls()) == 0 {
		t.Fatal("author never dispatched")
	}
	return conv, read.TurnID, free.TurnID
}

// A preset whose author has `write` and nothing else sends a diff to every
// reviewer. With the reviewer outside the destinations the roster is refused
// at load, naming the reviewer and the diff route and not the local author;
// with the endpoint named a destination the same roster loads and serves.
// The same roster with `write` removed loads without any destination, so the
// refusal is owed to `write` and to nothing else in the roster.
func TestEgress_WriteOnlyDiffRouteRefusedAtLoad(t *testing.T) {
	t.Parallel()
	writer := "\n[presets.writer]\nmode = \"review\"\nauthor = \"author\"\nrequired_reviewers = [\"reviewer\"]\ntools = [\"write\"]\n"
	h := enginetest.New(t, egRoster(t, "", writer), map[string][]fake.Step{}, egOptions(enginetest.NoStart())...)
	err := h.Engine.Start()
	var re *engine.RosterError
	if !errors.As(err, &re) {
		t.Fatalf("Start = %v, want a roster refusal", err)
	}
	for _, want := range []string{`preset "writer"`, `participant "reviewer"`, `route "diff"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), `participant "author"`) {
		t.Errorf("refusal names the author, which is local: %v", err)
	}

	s, c := egSuite(t, egRoster(t, "", writer, "lan"), egTurns(), nil)
	if st := s.Status(t, c); len(st.Participants) == 0 {
		t.Fatal("the roster with the destination named did not load")
	}

	readOnly := strings.Replace(writer, `tools = ["write"]`, "", 1)
	s2, c2 := egSuite(t, egRoster(t, "", readOnly), egTurns(), nil)
	if st := s2.Status(t, c2); len(st.Participants) == 0 {
		t.Fatal("the roster without write did not load")
	}
}

// A preset with no `read` and no `write` anywhere is accepted on endpoints
// outside the destinations and completes.
func TestEgress_NoReadNoWriteAcceptedOnAnyEndpoint(t *testing.T) {
	t.Parallel()
	s, c := egSuite(t, egRoster(t, "", ""), tlAcks(egTurns([]fake.Step{enginetest.Reply(egFirstAnswer)})...), tlAcks(tlApprove()))
	reply, turn := s.egRun(t, c, "s1", "review", nil, nil)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if s.egTainted(t, reply.TurnID) {
		t.Fatal("a tool-free turn in a fresh conversation is file_tainted")
	}
	if n := len(s.tlRequests("reviewer")); n != 1 {
		t.Fatalf("the reviewer on the outside endpoint received %d requests, want 1", n)
	}
}

// After a turn that ran with read, a tool-free preset whose reviewer is
// outside the destinations is refused `egress` route `history` naming the
// reviewer, though the preset grants no tool; nothing is recorded and nothing
// dispatched. A fresh conversation under the same preset is accepted, and with
// the endpoint among the destinations the tainted submit is accepted too.
func TestEgress_TaintedConversationHistoryRoute(t *testing.T) {
	t.Parallel()
	s, c := egSuite(t, egRoster(t, "", ""),
		tlAcks(egTurns(egReadTurn(egFirstAnswer), []fake.Step{enginetest.Reply(egSecondAnswer)}, []fake.Step{enginetest.Reply(egThirdAnswer)})...),
		tlAcks(tlApprove()))
	conv, _, _ := s.egTaintedConversation(t, c)

	turns := s.journalCount(t, `SELECT COUNT(*) FROM turns`)
	dispatched := len(s.Adapters["author"].Calls()) + len(s.Adapters["reviewer"].Calls())
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "whole", Input: "q", Preset: egPreset("review"), ConversationID: &conv})
	egWantEgress(t, err, "reviewer", "history")
	if n := s.journalCount(t, `SELECT COUNT(*) FROM turns`); n != turns {
		t.Fatalf("the refused submit created %d turns", n-turns)
	}
	if got := len(s.Adapters["author"].Calls()) + len(s.Adapters["reviewer"].Calls()); got != dispatched {
		t.Fatalf("the refused submit dispatched %d requests", got-dispatched)
	}

	fresh, turn := s.egRun(t, c, "fresh", "review", nil, nil)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if s.egTainted(t, fresh.TurnID) {
		t.Fatal("the tool-free turn in a fresh conversation is file_tainted")
	}

	inside, c2 := egSuite(t, egRoster(t, "", "", "lan"),
		tlAcks(egTurns(egReadTurn(egFirstAnswer), []fake.Step{enginetest.Reply(egSecondAnswer)}, []fake.Step{enginetest.Reply(egThirdAnswer)})...),
		tlAcks(tlApprove()))
	conv2, _, _ := inside.egTaintedConversation(t, c2)
	again, turn := inside.egRun(t, c2, "again", "review", &conv2, nil)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if !inside.egTainted(t, again.TurnID) {
		t.Fatal("the accepted turn in a tainted conversation is not file_tainted")
	}
}

// Trimming the only tainted turn out of the history does not launder the
// conversation: a tool-free turn written after it is in view, and the submit
// that starts from that turn is still refused on the history route, while the
// history it would carry holds nothing of the tainted turn.
func TestEgress_TrimCannotClearTaint(t *testing.T) {
	t.Parallel()
	s, c := egSuite(t, egRoster(t, "", ""),
		egTurns(egReadTurn(egFirstAnswer), []fake.Step{enginetest.Reply(egSecondAnswer)}),
		tlAcks(tlApprove()))
	conv, readTurn, freeTurn := s.egTaintedConversation(t, c)
	if readTurn == freeTurn {
		t.Fatal("the tainted turn and the tool-free turn are the same turn")
	}

	turns := s.journalCount(t, `SELECT COUNT(*) FROM turns`)
	from := freeTurn
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "trim", Input: "q", Preset: egPreset("review"), ConversationID: &conv, ContextFromTurn: &from})
	egWantEgress(t, err, "reviewer", "history")
	if n := s.journalCount(t, `SELECT COUNT(*) FROM turns`); n != turns {
		t.Fatalf("the refused submit created %d turns", n-turns)
	}

	// The same trim is accepted when the reviewer's endpoint is a destination;
	// the turn's history leaves the tainted turn out and the turn is tainted.
	inside, c2 := egSuite(t, egRoster(t, "", "", "lan"),
		tlAcks(egTurns(egReadTurn(egFirstAnswer), []fake.Step{enginetest.Reply(egSecondAnswer)}, []fake.Step{enginetest.Reply(egThirdAnswer)})...),
		tlAcks(tlApprove()))
	conv2, _, free2 := inside.egTaintedConversation(t, c2)
	reply, turn := inside.egRun(t, c2, "trim", "review", &conv2, &free2)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if !inside.egTainted(t, reply.TurnID) {
		t.Fatal("trimming the tainted turn out of view laundered the taint")
	}
	seen := tlText(inside.tlRequests("author")[len(inside.tlRequests("author"))-1])
	if strings.Contains(seen, egFirstAnswer) || !strings.Contains(seen, egSecondAnswer) {
		t.Fatalf("the trimmed turn's author request carries the wrong history: %q", seen)
	}
}

// A turn is file_tainted when any earlier turn of its conversation is, in
// view of its history or not; a turn in a fresh conversation is not. (The
// reading turn is tainted because its preset gives the author `read`, and it
// reads a file besides.)
func TestEgress_FileTaintedSetTransitively(t *testing.T) {
	t.Parallel()
	s, c := egSuite(t, egRoster(t, "", ""),
		egTurns(egReadTurn(egFirstAnswer), []fake.Step{enginetest.Reply(egSecondAnswer)},
			[]fake.Step{enginetest.Reply(egThirdAnswer)}, []fake.Step{enginetest.Reply("fresh")}),
		nil)
	conv, readTurn, freeTurn := s.egTaintedConversation(t, c)
	if !s.egTainted(t, freeTurn) {
		t.Fatal("a tool-free turn after a tainted one, with it in view, is not file_tainted")
	}

	from := freeTurn
	trimmed, turn := s.egRun(t, c, "trimmed", "plain", &conv, &from)
	wantTurn(t, turn, engine.StateSoloComplete, "solo_ok")
	if !s.egTainted(t, trimmed.TurnID) {
		t.Fatalf("a turn written with the tainted turn %s out of view is not file_tainted", readTurn)
	}
	if strings.Contains(tlText(s.tlRequests("author")[len(s.tlRequests("author"))-1]), egFirstAnswer) {
		t.Fatal("the trimmed turn's history still includes the tainted turn's answer")
	}

	fresh, turn := s.egRun(t, c, "fresh", "plain", nil, nil)
	wantTurn(t, turn, engine.StateSoloComplete, "solo_ok")
	if s.egTainted(t, fresh.TurnID) {
		t.Fatal("a tool-free turn in a fresh conversation is file_tainted")
	}
}

// A preset whose only tool anywhere is `web`, on a reviewer endpoint outside
// the destinations, is accepted in an untainted conversation and completes;
// it is refused nowhere on the history route, and its turn is not tainted.
func TestEgress_PureWebUntaintedUnaffected(t *testing.T) {
	t.Parallel()
	s, c := egSuite(t, egRoster(t, "", ""), tlAcks(egTurns([]fake.Step{enginetest.Reply(egFirstAnswer)})...), tlAcks(tlApprove()))
	reply, turn := s.egRun(t, c, "web", "webauthor", nil, nil)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if s.egTainted(t, reply.TurnID) {
		t.Fatal("a web-only turn is file_tainted")
	}
	if n := len(s.tlRequests("reviewer")); n != 1 {
		t.Fatalf("the reviewer received %d requests, want 1", n)
	}
	// The preset really is a web preset: the manifest-level check is the
	// roster's, so the same submit to a tainted conversation is refused.
	other, c2 := egSuite(t, egRoster(t, "", ""), tlAcks(egTurns(egReadTurn(egFirstAnswer), []fake.Step{enginetest.Reply(egSecondAnswer)})...), tlAcks(tlApprove()))
	conv, _, _ := other.egTaintedConversation(t, c2)
	_, err := c2.Submit(other.ctx(), wire.SubmitRequest{SubmissionID: "tainted", Input: "q", Preset: egPreset("webauthor"), ConversationID: &conv})
	egWantEgress(t, err, "reviewer", "history")
}

func egPreset(name string) *string { return &name }

// egBytesPerToken is the author's bytes per token the engine uses.
func egBytesPerToken(t *testing.T, s *suite, c *client.Client) float64 {
	t.Helper()
	p := budgetsParticipant(t, s.Status(t, c), "author")
	if p.BytesPerToken == nil {
		t.Fatal("the author has no bytes_per_token")
	}
	return *p.BytesPerToken
}

// withoutToolCalls and withoutToolMessages return copies of msgs with the
// assistant tool-call arguments, or the tool messages, removed.
func egWithoutToolCalls(msgs []transport.Message) []transport.Message {
	out := slices.Clone(msgs)
	for i := range out {
		out[i].ToolCalls = nil
	}
	return out
}

func egWithoutToolMessages(msgs []transport.Message) []transport.Message {
	var out []transport.Message
	for _, m := range msgs {
		if m.Role != "tool" {
			out = append(out, m)
		}
	}
	return out
}

// A request in the author's tool loop carries the tool definitions, the
// assistant's call arguments and the tool message with its framing. The
// estimate the engine reserves for each author attempt equals the estimate of
// the request the adapter received, recomputed here from its messages and
// definitions, and it exceeds the estimate of the same request without the
// definitions, without the call arguments, and without the tool message: the
// engine counts each of the parts and none of them is estimated outside the
// formula.
func TestEgress_ToolRequestReservesRenderedSize(t *testing.T) {
	t.Parallel()
	s, c := egSuite(t, egRoster(t, "", ""), egTurns(egReadTurn(egFirstAnswer)), nil)
	bpt := egBytesPerToken(t, s, c)
	_, turn := s.egRun(t, c, "s1", "tooled", nil, nil)
	wantTurn(t, turn, engine.StateSoloComplete, "solo_ok")

	reqs := s.tlRequests("author")
	if len(reqs) != 2 {
		t.Fatalf("%d author requests, want the call step and its continuation", len(reqs))
	}
	first, second := reqs[0], reqs[1]
	if len(first.Tools) == 0 || len(second.Tools) == 0 {
		t.Fatalf("author requests offered %d and %d definitions, want some in both", len(first.Tools), len(second.Tools))
	}
	var assistant *transport.Message
	for i, m := range second.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			assistant = &second.Messages[i]
		}
	}
	if assistant == nil || assistant.ToolCalls[0].Function.Arguments == "" {
		t.Fatal("the continuation has no assistant message carrying the call arguments")
	}
	result := tlToolMessage(second, "r1")
	if result == nil || !strings.Contains(result.Content, tlFileA) || len(result.Content) <= len(tlFileA) {
		t.Fatalf("the tool message = %+v, want the file body inside framing", result)
	}

	var want []int
	for _, req := range reqs {
		want = append(want, budget.RequestEstimateTokens(req.Messages, req.Tools, bpt, budgetsOverheads))
	}
	var got []int
	for _, a := range budgetsAttemptsOf(turn, "author") {
		got = append(got, int(a.EstimatedPromptTokens))
	}
	sort.Ints(want)
	sort.Ints(got)
	if !slices.Equal(got, want) {
		t.Fatalf("reserved estimates %v, want the rendered sizes %v", got, want)
	}

	full := budget.RequestEstimateTokens(second.Messages, second.Tools, bpt, budgetsOverheads)
	for name, less := range map[string]int{
		"definitions":    budget.RequestEstimateTokens(second.Messages, nil, bpt, budgetsOverheads),
		"call arguments": budget.RequestEstimateTokens(egWithoutToolCalls(second.Messages), second.Tools, bpt, budgetsOverheads),
		"tool message":   budget.RequestEstimateTokens(egWithoutToolMessages(second.Messages), second.Tools, bpt, budgetsOverheads),
	} {
		if less >= full {
			t.Errorf("the request without its %s estimates %d, not under the full %d", name, less, full)
		}
	}
}

// budgetsAttemptsOf returns the turn's attempts of the participant.
func budgetsAttemptsOf(turn wire.TurnReply, participant string) []wire.AttemptRecord {
	var out []wire.AttemptRecord
	for _, a := range turn.Attempts {
		if a.Participant == participant {
			out = append(out, a)
		}
	}
	return out
}

// Under one roster, one mode, one input and one context bound, the preset
// without tools is admitted and the preset whose only difference is the
// author's `read` is refused before dispatch with `budget: context` naming the
// author, the admission the rendered definitions make, and the limit; the
// refusal records no turn and sends no request. The tool definitions alone
// are what pushes the request over the bound.
func TestEgress_DefinitionsAloneExceedAdmissionBound(t *testing.T) {
	t.Parallel()
	const (
		maxOut = 64
		slack  = 40
		input  = "q s1"
	)
	defs, err := tools.ForGroups([]string{tools.GroupRead}, false)
	if err != nil || len(defs) == 0 {
		t.Fatalf("read definitions = %d, %v", len(defs), err)
	}
	msgs := prompt.Author(input)
	plain := budget.RequestAdmissionTokens(msgs, nil, 1.0, budgetsOverheads) + maxOut
	withDefs := budget.RequestAdmissionTokens(msgs, defs, 1.0, budgetsOverheads) + maxOut
	if withDefs-plain <= slack {
		t.Fatalf("premise: the definitions add %d tokens, want more than %d", withDefs-plain, slack)
	}
	bound := plain + slack

	roster := egRoster(t, "context_margin = 0.0\nauthor_max_output_tokens = 64", "")
	s, c := egSuite(t, roster, egTurns([]fake.Step{enginetest.Reply(egFirstAnswer)}), nil, enginetest.WithContextBound("author", bound))

	_, turn := s.egRun(t, c, "s1", "plain", nil, nil)
	wantTurn(t, turn, engine.StateSoloComplete, "solo_ok")
	if len(s.tlRequests("author")) != 1 {
		t.Fatalf("the tool-free control sent %d requests, want 1", len(s.tlRequests("author")))
	}

	turns := s.journalCount(t, `SELECT COUNT(*) FROM turns`)
	dispatched := len(s.Adapters["author"].Calls())
	preset := "tooled"
	_, err = c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s2", Input: input, Preset: &preset, Workspace: s.egWorkspace(t)})
	we := wantRefusal(t, err, wire.ErrorCodeBudget)
	if we.Budget == nil || *we.Budget != "context" || we.Participant == nil || *we.Participant != "author" {
		t.Fatalf("refusal = %+v, want budget context naming the author", we)
	}
	if we.Limit == nil || *we.Limit != int64(bound) {
		t.Fatalf("refusal limit = %v, want the bound %d", we.Limit, bound)
	}
	if we.Admission == nil || *we.Admission <= int64(bound) {
		t.Fatalf("refusal admission = %v, want over the bound %d", we.Admission, bound)
	}
	if n := s.journalCount(t, `SELECT COUNT(*) FROM turns`); n != turns {
		t.Fatalf("the refused submit created %d turns", n-turns)
	}
	if got := len(s.Adapters["author"].Calls()); got != dispatched {
		t.Fatalf("the refused submit dispatched %d requests", got-dispatched)
	}
}
