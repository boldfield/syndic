package acceptance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const toolLoopsPara = "Tools"

func init() {
	cover(toolLoopsPara, "tool definitions include the correct tools for each role", "TestToolLoops_ToolDefinitionsForRoles")
	cover(toolLoopsPara, "at the author's call bound the next request carries no tool definitions", "TestToolLoops_AuthorAtBoundNoDefinitions")
	cover(toolLoopsPara, "tool calls in the author's reply to the final no-tool attempt ends `tool_limit`", "TestToolLoops_AuthorToolLimitEndsWithToolCalls")
	cover(toolLoopsPara, "a plain reply to the author's final no-tool attempt is the candidate", "TestToolLoops_AuthorFinalNoToolIsCandidate")
	cover(toolLoopsPara, "at the reviewer bound the next request carries no tool definitions", "TestToolLoops_ReviewerAtBoundNoDefinitions")
	cover(toolLoopsPara, "a late tool step is quarantined and executes nothing", "TestToolLoops_LateToolStepQuarantined")
	cover(toolLoopsPara, "a tool step during cancellation is abandoned and executes nothing", "TestToolLoops_ToolStepDuringCancellationAbandoned")
	cover(toolLoopsPara, "a revision request carries the tool results a block cites and no other reviewer result", "TestToolLoops_RevisionCarriesCitedResultsOnly")
	cover(toolLoopsPara, "a repair request carries no tool definitions", "TestToolLoops_RepairNoToolDefinitions")
}

// toolLoopsSubmit submits a tool loops test turn with a workspace so tools are offered.
func toolLoopsSubmit(t *testing.T, s *suite, c *client.Client, id string) (string, wire.TurnReply) {
	t.Helper()
	dir := t.TempDir()
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: id,
		Input:        "what is the answer?",
		Workspace:    dir,
	})
	if err != nil {
		t.Fatalf("submit %s: %v", id, err)
	}
	s.WaitTerminal(reply.TurnID)
	return reply.TurnID, s.Turn(t, c, reply.TurnID)
}

// toolLoopsRoster returns a roster with the given tools enabled for author and reviewers,
// with revision rounds enabled (max_rounds = 2), and with both participants having tool capability.
func toolLoopsRoster(authorTools, reviewerTools []string) string {
	toml := enginetest.RosterTOML

	toml = strings.Replace(toml, "[revision]\nmax_rounds = 0", "[revision]\nmax_rounds = 2", 1)

	toml = strings.Replace(toml, `[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]`, `[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
tools = ["`+strings.Join(authorTools, `", "`)+`"]
reviewer_tools = ["`+strings.Join(reviewerTools, `", "`)+`"]`, 1)

	hasWeb := false
	for _, t := range reviewerTools {
		if t == "web" {
			hasWeb = true
			break
		}
	}
	if !hasWeb {
		for _, t := range authorTools {
			if t == "web" {
				hasWeb = true
				break
			}
		}
	}

	if hasWeb {
		oldEndpoint := `[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0`

		newEndpoint := oldEndpoint + `
referee_model = "llama3"`

		toml = strings.Replace(toml, oldEndpoint, newEndpoint, 1)
	}

	return toml
}

// withToolCallBounds adds max_calls_per_turn and/or max_reviewer_calls to the
// global [tools] section of a roster TOML. The limits control budget on the
// author and reviewer tool call counts.
func withToolCallBounds(roster string, authorCallsPerTurn, reviewerCalls *int) string {
	var fields []string
	if authorCallsPerTurn != nil {
		fields = append(fields, fmt.Sprintf("max_calls_per_turn = %d", *authorCallsPerTurn))
	}
	if reviewerCalls != nil {
		fields = append(fields, fmt.Sprintf("max_reviewer_calls = %d", *reviewerCalls))
	}
	if len(fields) == 0 {
		return roster
	}
	return strings.Replace(roster, "[tools]\n", "[tools]\n"+strings.Join(fields, "\n")+"\n", 1)
}

// TestToolLoops_ToolDefinitionsForRoles tests that tool definitions include
// the correct tools for each role.
func TestToolLoops_ToolDefinitionsForRoles(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read", "write"}, []string{"read", "web"})

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					hasRead, hasWrite, hasWeb := false, false, false
					for _, tool := range req.Tools {
						if tool.Type == "function" {
							name := tool.Function.Name
							if name == tools.NameReadFile || name == tools.NameListDir || name == tools.NameSearchFiles {
								hasRead = true
							}
							if name == tools.NameWriteFile || name == tools.NameEditFile || name == tools.NameUnstage {
								hasWrite = true
							}
							if name == tools.NameWebFetch || name == tools.NameWebSearch {
								hasWeb = true
							}
						}
					}
					if !hasRead || !hasWrite || hasWeb {
						t.Errorf("author tools wrong: read=%v write=%v web=%v", hasRead, hasWrite, hasWeb)
					}
					return "answer"
				},
			},
		},
		"reviewer": {
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					hasRead, hasWrite, hasWeb := false, false, false
					for _, tool := range req.Tools {
						if tool.Type == "function" {
							name := tool.Function.Name
							if name == tools.NameReadFile || name == tools.NameListDir || name == tools.NameSearchFiles {
								hasRead = true
							}
							if name == tools.NameWriteFile || name == tools.NameEditFile || name == tools.NameUnstage {
								hasWrite = true
							}
							if name == tools.NameWebFetch || name == tools.NameWebSearch {
								hasWeb = true
							}
						}
					}
					if !hasRead || hasWrite || !hasWeb {
						t.Errorf("reviewer tools wrong: read=%v write=%v web=%v", hasRead, hasWrite, hasWeb)
					}
					return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
				},
			},
		},
	})

	c := s.Dial(t)
	_, turn := toolLoopsSubmit(t, s, c, "s1")

	if turn.State != engine.StateApproved {
		reason := ""
		if turn.TerminalReason != nil {
			reason = *turn.TerminalReason
		}
		t.Fatalf("expected approved, got state=%s reason=%s", turn.State, reason)
	}
}

// TestToolLoops_AuthorAtBoundNoDefinitions tests that at the author's call bound,
// the next request carries no tool definitions.
func TestToolLoops_AuthorAtBoundNoDefinitions(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, nil)
	callsPerTurn := 2
	roster = withToolCallBounds(roster, &callsPerTurn, nil)

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) == 0 {
						t.Errorf("expected tool definitions on first request")
					}
					return "TOOL_CALLS_RESPONSE"
				},
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) == 0 {
						t.Errorf("expected tool definitions on second request")
					}
					return "TOOL_CALLS_RESPONSE_2"
				},
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c2", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"b"}`}},
				},
			},
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) > 0 {
						t.Errorf("expected no tool definitions on third request (at bound)")
					}
					return "FINAL_ANSWER"
				},
			},
			enginetest.Reply("ok"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	})

	c := s.Dial(t)
	_, turn := revSubmit(t, s, c, "s1", nil)

	if turn.State != engine.StateApproved {
		t.Fatalf("unexpected turn state: %s", turn.State)
	}
}

// TestToolLoops_AuthorToolLimitEndsWithToolCalls tests that tool calls in the
// final no-tool attempt end the turn with tool_limit.
func TestToolLoops_AuthorToolLimitEndsWithToolCalls(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, nil)
	callsPerTurn := 1
	roster = withToolCallBounds(roster, &callsPerTurn, nil)

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind:   fake.StepKindReply,
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) > 0 {
						t.Errorf("expected no tools on final attempt (at limit)")
					}
					return "TOOL_CALLS_RESPONSE"
				},
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c2", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"b"}`}},
				},
			},
		},
		"reviewer": {
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	})

	c := s.Dial(t)
	_, turn := revSubmit(t, s, c, "s1", nil)

	if turn.State != engine.StateIncomplete || turn.TerminalReason == nil || !strings.Contains(*turn.TerminalReason, "tool_limit") {
		t.Fatalf("expected incomplete with tool_limit, got state=%s reason=%v", turn.State, turn.TerminalReason)
	}
}

// TestToolLoops_AuthorFinalNoToolIsCandidate tests that a plain reply to the
// final no-tool attempt becomes the candidate.
func TestToolLoops_AuthorFinalNoToolIsCandidate(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, nil)
	callsPerTurn := 1
	roster = withToolCallBounds(roster, &callsPerTurn, nil)

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind:   fake.StepKindReply,
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			enginetest.Reply("the final answer"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	})

	c := s.Dial(t)
	_, turn := revSubmit(t, s, c, "s1", nil)

	if turn.State != engine.StateApproved {
		t.Fatalf("expected approved, got %s", turn.State)
	}
}

// TestToolLoops_ReviewerAtBoundNoDefinitions tests that at the reviewer's call
// bound, the next request carries no tool definitions.
func TestToolLoops_ReviewerAtBoundNoDefinitions(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, []string{"read"})
	reviewerCalls := 1
	roster = withToolCallBounds(roster, nil, &reviewerCalls)

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			enginetest.Reply("the answer"),
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
		"reviewer": {
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) == 0 {
						t.Errorf("expected tool definitions on first reviewer attempt")
					}
					return "TOOL_CALLS"
				},
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) > 0 {
						t.Errorf("expected no tool definitions at bound")
					}
					return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
				},
			},
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	})

	c := s.Dial(t)
	_, turn := revSubmit(t, s, c, "s1", nil)

	if turn.State != engine.StateApproved {
		t.Fatalf("expected approved, got state=%s", turn.State)
	}
}

// TestToolLoops_LateToolStepQuarantined tests that a late tool step is quarantined and not executed.
func TestToolLoops_LateToolStepQuarantined(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, nil)

	var toolHeld func()
	releaseEffect := make(chan struct{})

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind:   fake.StepKindReply,
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			enginetest.Reply("the final answer"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	}, enginetest.WithBeforeToolEffect(func(turnID, attemptID, name string) {
		if toolHeld == nil {
			return
		}
		toolHeld()
		<-releaseEffect
	}))

	c := s.Dial(t)
	dir := t.TempDir()
	resp, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
		Workspace:    dir,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	turnID := resp.TurnID

	toolHeld = func() {
		s.WaitTurnState(turnID, engine.StateApproved)
		close(releaseEffect)
	}

	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)

	if turn.State != engine.StateApproved {
		t.Fatalf("expected approved, got %s", turn.State)
	}

	calls, err := s.Store.ToolCallsByTurn(turnID, "author")
	if err != nil {
		t.Fatalf("listing tool calls: %v", err)
	}

	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
}

// TestToolLoops_ToolStepDuringCancellationAbandoned tests that a tool step
// arriving during cancellation is abandoned and not executed.
func TestToolLoops_ToolStepDuringCancellationAbandoned(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, nil)

	cancellationTriggered := make(chan struct{})

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind:   fake.StepKindReply,
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			enginetest.Reply("the answer"),
		},
		"reviewer": {
			verdict(fake.VerdictApprove),
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	}, enginetest.WithBeforeToolEffect(func(turnID, attemptID, name string) {
		<-cancellationTriggered
	}))

	c := s.Dial(t)
	dir := t.TempDir()
	resp, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
		Workspace:    dir,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	turnID := resp.TurnID
	s.WaitTurnState(turnID, engine.StateAuthorDispatched)

	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	close(cancellationTriggered)

	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)

	if turn.State != engine.StateCancelled {
		t.Fatalf("expected cancelled, got %s", turn.State)
	}

	calls, err := s.Store.ToolCallsByTurn(turnID, "author")
	if err != nil {
		t.Fatalf("listing tool calls: %v", err)
	}

	if len(calls) != 0 {
		t.Fatalf("expected no tool calls during cancellation, got %d", len(calls))
	}
}

// TestToolLoops_RevisionCarriesCitedResultsOnly tests that in a revision round,
// only tool results cited in a block are transferred to the author.
func TestToolLoops_RevisionCarriesCitedResultsOnly(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, []string{"read"})

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind:   fake.StepKindReply,
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "call_a", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			enginetest.Reply("the answer"),
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					found := false
					for _, m := range req.Messages {
						if m.Role == "tool" && m.ToolCallID == "call_a" && strings.Contains(m.Content, "hello from a") {
							found = true
						}
					}
					if !found {
						t.Errorf("revision request does not contain cited result for call_a")
					}
					return "revised answer"
				},
			},
		},
		"reviewer": {
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "result:call_a", "revise", "needs work")
				},
			},
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					return fmt.Sprintf("VERDICT v2\ncandidate: %s\nattempt: %s\nround: 1\nblock-state: b1 withdrawn\nverdict: approve\nreason: OK\nEND VERDICT",
						req.Envelope.CandidateHash, req.Envelope.AttemptID)
				},
			},
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	})

	c := s.Dial(t)
	dir := t.TempDir()
	resp, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
		Workspace:    dir,
		MaxRounds:    revIntPtr(1),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	s.WaitTerminal(resp.TurnID)
	turn := s.Turn(t, c, resp.TurnID)

	if turn.State != engine.StateApproved {
		t.Fatalf("expected approved, got state=%s", turn.State)
	}
}

// TestToolLoops_RepairNoToolDefinitions tests that a repair request carries
// no tool definitions.
func TestToolLoops_RepairNoToolDefinitions(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, []string{"read"})

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			enginetest.Reply("the answer"),
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
		"reviewer": {
			enginetest.Reply("malformed verdict (no envelope)"),
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) > 0 {
						t.Errorf("repair should have no tools, but got %d", len(req.Tools))
					}
					return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
				},
			},
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	})

	c := s.Dial(t)
	_, turn := revSubmit(t, s, c, "s1", nil)

	if turn.State != engine.StateApproved {
		t.Fatalf("expected approved after repair, got state=%s", turn.State)
	}
}
