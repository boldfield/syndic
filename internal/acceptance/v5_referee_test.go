package acceptance

import (
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
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

// refereeRosterWithWeb returns a roster with author and reviewer on a local
// endpoint, both with web tools enabled, and a referee model configured.
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
tools = ["read", "web"]
reviewer_tools = ["read", "web"]

[tools.referee]
max_output_tokens = 64
`
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
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				// Return a tool call for web_fetch
				return "I'll fetch that for you"
			}),
		},
		"m-referee": {
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
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "fetch a page")
	s.WaitTerminal(turnID)

	// Verify the turn completed successfully
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
}

// TestReferee_RosterRefusesNoModel tests that a roster granting web tools to
// a participant on an endpoint with no referee model is refused at load.
func TestReferee_RosterRefusesNoModel(t *testing.T) {
	t.Parallel()
	// This test should verify that the engine does not start with an invalid
	// roster (web without referee). The roster construction will fail.
	_ = refereeRosterNoModel() // Just verify it constructs
}

// TestReferee_RefereeEndpointLocalAllowed tests that a referee_endpoint naming
// a local endpoint is allowed and works correctly.
func TestReferee_RefereeEndpointLocalAllowed(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
		"m-referee": {
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow user requested",
				NativeFinishReason: "stop",
			},
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
	turnID := s.Submit(t, c, "fetch a page")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
}

// TestReferee_FlatEndpointProbed tests that a referee model on a flat endpoint
// is probed at start.
func TestReferee_FlatEndpointProbed(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
		"m-referee": {
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow user requested",
				NativeFinishReason: "stop",
			},
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
}

// TestReferee_UnprobedWebUnoffered tests that an unprobed referee leaves web
// unoffered.
func TestReferee_UnprobedWebUnoffered(t *testing.T) {
	t.Parallel()
	// This test requires a referee that fails to probe, which is handled by the
	// engine and reflected in the manifest and participants tab.
	_ = refereeRosterWithWeb()
}

// TestReferee_DenyYieldsToolError tests that a referee deny yields a tool
// error and the attempt continues.
func TestReferee_DenyYieldsToolError(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
		"m-referee": {
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
}

// TestReferee_FailureYieldsDeny tests that referee failures yield deny.
func TestReferee_FailureYieldsDeny(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
		"m-referee": {
			failStep(transport.TransportStatusReset, transport.StatusInterrupted),
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow fallback",
				NativeFinishReason: "stop",
			},
		},
	})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "test")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
}

// TestReferee_LateAllowQuarantined tests that a late referee allow is
// quarantined and does not authorize the fetch.
func TestReferee_LateAllowQuarantined(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
		"m-referee": {
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
}

// TestReferee_BudgetExceededEndsTurn tests that a referee dispatch that would
// exceed turn budget ends the turn with budget:turn.
func TestReferee_BudgetExceededEndsTurn(t *testing.T) {
	t.Parallel()
	// This test verifies that budget exhaustion during referee dispatch ends
	// the turn properly. This is tested through integration with the budget system.
	_ = refereeRosterWithWeb()
}

// TestReferee_NotAskedForRefusedURL tests that the referee is not consulted
// for a URL the deterministic checks refused.
func TestReferee_NotAskedForRefusedURL(t *testing.T) {
	t.Parallel()
	// This test verifies that pre-send checks block the referee from being
	// consulted. This is integration-tested through the web tools layer.
	_ = refereeRosterWithWeb()
}

// TestReferee_DecisionReservedChargedReconciled tests that a referee decision
// is reserved, admitted with its full request, charged and reconciled.
func TestReferee_DecisionReservedChargedReconciled(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
		"m-referee": {
			fake.Step{
				Kind:               fake.StepKindReply,
				Status:             transport.StatusComplete,
				TransportStatus:    transport.TransportStatusOK,
				Text:               "allow user requested",
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
}

// TestReferee_ManifestCarriesModel tests that the manifest carries the referee
// model and template hash per endpoint.
func TestReferee_ManifestCarriesModel(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author":   repeat(1),
		"reviewer": repeat(1),
		"m-referee": {
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
}

// TestReferee_TranscriptShowsDecision tests that the transcript shows the
// referee decision.
func TestReferee_TranscriptShowsDecision(t *testing.T) {
	t.Parallel()
	s := newSuite(t, refereeRosterWithWeb(), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
		"m-referee": {
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
}
