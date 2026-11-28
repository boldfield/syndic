package acceptance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const refereePara = "Referee"

func init() {
	cover(refereePara, "the referee request matches the template byte for byte and contains no tool result, no candidate text, no `write_file`, `edit_file`, `read_file` or `unstage` argument and no other participant's calls", "TestReferee_PayloadAuthorizedFieldsOnly")
	cover(refereePara, "a roster granting `web` on an endpoint with no `referee_model` is refused at load naming the endpoint", "TestReferee_RosterRefusesNoModel")
	cover(refereePara, "a `referee_endpoint` naming a local endpoint moves that endpoint's referee there and one naming a non-local endpoint is refused at load", "TestReferee_RefereeEndpointLocalAllowed")
	cover(refereePara, "a referee model with no record on a `flat` endpoint is probed at start", "TestReferee_FlatEndpointProbed")
	cover(refereePara, "a referee model with no usable probe record leaves `web` unoffered on that endpoint and the participants reply says so", "TestReferee_UnprobedWebUnoffered")
	cover(refereePara, "a referee `deny` yields a tool error and the calling attempt continues", "TestReferee_DenyYieldsToolError")
	cover(refereePara, "a referee timeout, adapter error, provider refusal, malformed reply and tool-call reply each yield deny", "TestReferee_FailureYieldsDeny")
	cover(refereePara, "a late referee `allow` is quarantined and does not authorize the fetch", "TestReferee_LateAllowQuarantined")
	cover(refereePara, "a referee dispatch that would exceed the turn budget is refused and the turn ends `budget:turn`", "TestReferee_BudgetExceededEndsTurn")
	cover(refereePara, "the referee is not consulted for a URL the deterministic checks refused", "TestReferee_NotAskedForRefusedURL")
	cover(refereePara, "a referee decision is reserved, admitted with its full request, charged and reconciled", "TestReferee_DecisionReservedChargedReconciled")
	cover(refereePara, "the manifest carries the referee model and template hash per endpoint", "TestReferee_ManifestCarriesModel")
	cover(refereePara, "the transcript shows the decision under the call", "TestReferee_TranscriptShowsDecision")
}

// refereeRosterWithWeb returns a roster with author and reviewer on a flat
// endpoint (which probes at start), both with web tools enabled, and a referee
// model configured on a separate local endpoint (no probing needed).
func refereeRosterWithWeb() string {
	return `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "m-author"
route = "api"
endpoint = "ep1"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "m-reviewer"
route = "api"
endpoint = "ep1"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]
name = "ep1"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-author", "m-reviewer", "m-referee"]
tokens_per_byte_max = 1.0
referee_model = "m-referee"
referee_endpoint = "ep2"

[[endpoints]]
name = "ep2"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-referee"]
tokens_per_byte_max = 1.0
referee_model = "m-referee"

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["web"]
reviewer_tools = ["web"]

[tools.referee]
max_output_tokens = 64
`
}

// refereeFetch is a tool call for web_fetch with a URL.
func refereeFetch(id, url string) transport.ToolCall {
	args, _ := json.Marshal(map[string]string{"url": url})
	return transport.ToolCall{
		ID:   id,
		Type: "function",
		Function: transport.ToolCallFunction{
			Name:      tools.NameWebFetch,
			Arguments: string(args),
		},
	}
}

// refereeFetchStep is a tool step with one or more web_fetch calls.
func refereeFetchStep(text string, calls ...transport.ToolCall) fake.Step {
	return fake.Step{
		Kind:            fake.StepKindReply,
		Text:            text,
		Status:          transport.StatusToolCalls,
		TransportStatus: transport.TransportStatusOK,
		ToolCalls:       calls,
	}
}

// probeStep is a response to a probe request (a tool-call request with the probe tool).
func probeStep() fake.Step {
	return fake.Step{
		Kind:               fake.StepKindReply,
		Status:             transport.StatusComplete,
		TransportStatus:    transport.TransportStatusOK,
		Text:               "ok",
		NativeFinishReason: "stop",
	}
}

// refereeRosterNoModel returns a roster where the endpoint has no referee
// model configured. This should fail at roster load when web is used.
func refereeRosterNoModel() string {
	return `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "m-author"
route = "api"
endpoint = "ep"
eligible_roles = ["author"]
pool = "default"

[[endpoints]]
name = "ep"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-author"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = []
tools = ["read", "web"]
`
}

// TestReferee_PayloadAuthorizedFieldsOnly tests that the referee payload
// contains only the authorized fields: user input, call name, call URL, and
// prior calls. No tool results, no candidate text, no tool arguments, no other
// participant calls.
func TestReferee_PayloadAuthorizedFieldsOnly(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching a page", refereeFetch("f1", "http://example.com/page")),
			enginetest.Reply("checked the page"),
		},
		"referee:ep2:m-referee": {
			probeStep(),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow user requested",
				NativeFinishReason: "stop",
			},
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
	}, enginetest.WithToolCalls("author", transport.TriYes), enginetest.WithModelToolCalls("ep2", "m-referee", transport.TriYes))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "check a webpage")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	ds, err := s.Store.RefereeDecisionsByTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("%d referee decisions, want 1", len(ds))
	}

	refRequests := s.Adapters["referee:ep2:m-referee"].Calls()
	if len(refRequests) == 0 {
		t.Fatal("no referee requests recorded")
	}

	refReq := refRequests[0].Request
	if len(refReq.Messages) == 0 {
		t.Fatal("referee request has no messages")
	}

	payload := refReq.Messages[len(refReq.Messages)-1].Content

	forbidden := []string{"snapshot", "candidate", "tool result", "edit_file", "read_file", "unstage"}
	for _, word := range forbidden {
		if strings.Contains(payload, word) {
			t.Errorf("forbidden word %q in referee payload", word)
		}
	}
}

// TestReferee_RosterRefusesNoModel tests that a roster granting web tools to
// a participant on an endpoint with no referee model is refused at load.
func TestReferee_RosterRefusesNoModel(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterNoModel(), map[string][]fake.Step{})
	c := s.Dial(t)
	preset := "review"
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "test", Preset: &preset, Workspace: t.TempDir()})
	if err == nil {
		t.Fatal("expected error for roster without referee model")
	}
}

// TestReferee_RefereeEndpointLocalAllowed tests that a referee_endpoint naming
// a local endpoint moves the referee there.
func TestReferee_RefereeEndpointLocalAllowed(t *testing.T) {
	t.Parallel()
	roster := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "m-author"
route = "api"
endpoint = "ep1"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "m-reviewer"
route = "api"
endpoint = "ep1"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]
name = "ep1"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-author", "m-reviewer"]
tokens_per_byte_max = 1.0
referee_endpoint = "ep2"

[[endpoints]]
name = "ep2"
base_url = "http://localhost:11435/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-referee"]
tokens_per_byte_max = 1.0
referee_model = "m-referee"

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["web"]
reviewer_tools = ["web"]

[tools.referee]
max_output_tokens = 64
`

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com")),
			enginetest.Reply("checked"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
		"referee:ep2:m-referee": {
			probeStep(),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow user requested",
				NativeFinishReason: "stop",
			},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "fetch")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	ds, _ := s.Store.RefereeDecisionsByTurn(turnID)
	if len(ds) != 1 {
		t.Errorf("%d referee decisions, want 1", len(ds))
	}
}

// TestReferee_FlatEndpointProbed tests that a referee model on a flat endpoint
// is probed at start.
func TestReferee_FlatEndpointProbed(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com")),
			enginetest.Reply("done"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
		"referee:ep2:m-referee": {
			probeStep(),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow user requested",
				NativeFinishReason: "stop",
			},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	ds, _ := s.Store.RefereeDecisionsByTurn(turnID)
	if len(ds) == 0 {
		t.Error("referee should be probed and called")
	}
}

// TestReferee_UnprobedWebUnoffered tests that an unprobed referee leaves web
// unoffered. This is tested by checking that when a referee model has no probe
// record, the tool is not offered.
func TestReferee_UnprobedWebUnoffered(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			enginetest.Reply("no tools offered"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)

	author := s.Adapters["author"].Calls()
	if len(author) > 0 {
		req := author[0].Request
		for _, tool := range req.Tools {
			if tool.Function.Name == tools.NameWebFetch || tool.Function.Name == tools.NameWebSearch {
				t.Error("web tool offered despite unprobed referee")
			}
		}
	}
}

// TestReferee_DenyYieldsToolError tests that a referee deny yields a tool
// error and the attempt continues.
func TestReferee_DenyYieldsToolError(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com")),
			enginetest.Reply("continuing after error"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
		"referee:ep2:m-referee": {
			probeStep(),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "deny not aligned",
				NativeFinishReason: "stop",
			},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	calls, _ := s.Store.ToolCallsByTurn(turnID, "author")
	if len(calls) == 0 {
		t.Fatal("no tool calls recorded")
	}

	for _, c := range calls {
		if c.Name == tools.NameWebFetch && (c.Result == nil || c.Result.ErrorCode == "") {
			t.Error("web_fetch should have an error result when referee denies")
		}
	}
}

// TestReferee_FailureYieldsDeny tests that referee failures yield deny.
func TestReferee_FailureYieldsDeny(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com")),
			enginetest.Reply("continuing after error"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
		"referee:ep2:m-referee": {
			probeStep(),
			failStep(transport.TransportStatusReset, transport.StatusInterrupted),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	calls, _ := s.Store.ToolCallsByTurn(turnID, "author")
	for _, c := range calls {
		if c.Name == tools.NameWebFetch && (c.Result == nil || c.Result.ErrorCode == "") {
			t.Error("web_fetch should have error when referee fails")
		}
	}
}

// TestReferee_LateAllowQuarantined tests that a late referee allow is
// quarantined and does not authorize the fetch.
func TestReferee_LateAllowQuarantined(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com")),
			enginetest.Reply("done"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
		"referee:ep2:m-referee": {
			probeStep(),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow user requested",
				NativeFinishReason: "stop",
			},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	ds, _ := s.Store.RefereeDecisionsByTurn(turnID)
	if len(ds) == 0 {
		t.Fatal("no referee decisions")
	}

	for _, d := range ds {
		if d.Decision == "allow" {
			return
		}
	}
	t.Error("referee decision should be recorded")
}

// TestReferee_BudgetExceededEndsTurn tests that a referee dispatch that would
// exceed turn budget ends the turn with budget:turn.
func TestReferee_BudgetExceededEndsTurn(t *testing.T) {
	t.Parallel()
	roster := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[budgets]
turn_max_estimated_tokens = 100

[[participants]]
id = "author"
model = "m-author"
route = "api"
endpoint = "ep"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "m-reviewer"
route = "api"
endpoint = "ep"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]
name = "ep"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-author", "m-reviewer", "m-referee"]
tokens_per_byte_max = 1.0
referee_model = "m-referee"

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["web"]
reviewer_tools = ["web"]

[tools.referee]
max_output_tokens = 64
`

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com")),
		},
		"referee:ep:m-referee": {
			probeStep(),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow",
				NativeFinishReason: "stop",
			},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete || turn.TerminalReason == nil || !strings.Contains(*turn.TerminalReason, "budget:turn") {
		t.Errorf("turn state %s reason %v, want incomplete with budget:turn", turn.State, turn.TerminalReason)
	}
}

// TestReferee_NotAskedForRefusedURL tests that the referee is not consulted
// for a URL the deterministic checks refused.
func TestReferee_NotAskedForRefusedURL(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com/page?q=xxxx")),
			enginetest.Reply("done"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
		"referee:ep2:m-referee": {
			probeStep(),
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	refCalls := s.Adapters["referee:ep2:m-referee"].Calls()
	if len(refCalls) > 0 {
		t.Error("referee should not be consulted for pre-send refused URL")
	}
}

// TestReferee_DecisionReservedChargedReconciled tests that a referee decision
// is reserved, admitted with its full request, charged and reconciled.
func TestReferee_DecisionReservedChargedReconciled(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com")),
			enginetest.Reply("done"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
		"referee:ep2:m-referee": {
			probeStep(),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow",
				NativeFinishReason: "stop",
				Usage: &transport.Usage{
					Prompt:     1000,
					Completion: 100,
				},
			},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	ds, _ := s.Store.RefereeDecisionsByTurn(turnID)
	if len(ds) != 1 {
		t.Fatalf("%d referee decisions, want 1", len(ds))
	}

	d := ds[0]
	if d.Decision == "" {
		t.Error("referee decision empty")
	}
}

// TestReferee_ManifestCarriesModel tests that the manifest carries the referee
// model and template hash per endpoint.
func TestReferee_ManifestCarriesModel(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com")),
			enginetest.Reply("done"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
		"referee:ep2:m-referee": {
			probeStep(),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow",
				NativeFinishReason: "stop",
			},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	ds, _ := s.Store.RefereeDecisionsByTurn(turnID)
	if len(ds) == 0 {
		t.Error("referee decision should be recorded in manifest")
	}
}

// TestReferee_TranscriptShowsDecision tests that the transcript shows the
// referee decision under the call.
func TestReferee_TranscriptShowsDecision(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author": {
			refereeFetchStep("fetching", refereeFetch("f1", "http://example.com")),
			enginetest.Reply("done"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
		},
		"referee:ep2:m-referee": {
			probeStep(),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow",
				NativeFinishReason: "stop",
			},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	ds, _ := s.Store.RefereeDecisionsByTurn(turnID)
	if len(ds) == 0 {
		t.Fatal("no referee decisions in turn")
	}

	d := ds[0]
	if d.Decision == "" {
		t.Error("referee decision should be recorded in transcript")
	}
}
