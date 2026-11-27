package acceptance

import (
	"fmt"
	"strings"
	"testing"

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

// toolLoopsRoster returns a roster with the given tools enabled for author and reviewers.
func toolLoopsRoster(authorTools, reviewerTools []string) string {
	toml := enginetest.RosterTOML
	var toolLine, reviewerToolLine string
	if len(authorTools) > 0 {
		var quoted []string
		for _, t := range authorTools {
			quoted = append(quoted, fmt.Sprintf(`"%s"`, t))
		}
		toolLine = fmt.Sprintf("tools = [%s]", strings.Join(quoted, ", "))
	}
	if len(reviewerTools) > 0 {
		var quoted []string
		for _, t := range reviewerTools {
			quoted = append(quoted, fmt.Sprintf(`"%s"`, t))
		}
		reviewerToolLine = fmt.Sprintf("reviewer_tools = [%s]", strings.Join(quoted, ", "))
	}

	var toolSection string
	if toolLine != "" || reviewerToolLine != "" {
		var toolFields []string
		if toolLine != "" {
			toolFields = append(toolFields, toolLine)
		}
		if reviewerToolLine != "" {
			toolFields = append(toolFields, reviewerToolLine)
		}
		toolSection = "\n" + strings.Join(toolFields, "\n")
	}

	presetLines := fmt.Sprintf(`[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]%s

[presets.review.revision]
max_rounds = 2`, toolSection)
	toml = strings.Replace(toml, `[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[presets.solo]`, presetLines+`

[presets.solo]`, 1)

	// If web tools are needed, add referee_model to the endpoint
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
		toml = strings.Replace(toml, `[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0`, `[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0
referee_model = "llama3"`, 1)
	}

	return toml
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
						t.Errorf("ERROR: author tools wrong: read=%v write=%v web=%v", hasRead, hasWrite, hasWeb)
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
						t.Errorf("ERROR: reviewer tools wrong: read=%v write=%v web=%v", hasRead, hasWrite, hasWeb)
					}
					return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
				},
			},
		},
	})

	c := s.Dial(t)
	_, turn := revSubmit(t, s, c, "s1", nil)

	if turn.State != engine.StateApproved {
		t.Fatalf("expected approved, got state=%s", turn.State)
	}
}

// TestToolLoops_AuthorAtBoundNoDefinitions tests that at the author's call bound,
// the next request carries no tool definitions.
func TestToolLoops_AuthorAtBoundNoDefinitions(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, nil)
	roster = strings.Replace(roster, `max_rounds = 2`, `max_rounds = 2
max_calls_per_turn = 2`, 1)

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) == 0 {
						return "ERROR: expected tool definitions on first request"
					}
					return "TOOL_CALLS_RESPONSE"
				},
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			enginetest.Reply("result1"),
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) == 0 {
						return "ERROR: expected tool definitions on second request"
					}
					return "TOOL_CALLS_RESPONSE_2"
				},
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c2", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"b"}`}},
				},
			},
			enginetest.Reply("result2"),
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) > 0 {
						return "ERROR: expected no tool definitions on third request (at bound)"
					}
					return "FINAL_ANSWER"
				},
			},
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
		"reviewer": {
			enginetest.Reply(fake.VerdictApprove("h", "a")),
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
	roster = strings.Replace(roster, `max_rounds = 2`, `max_rounds = 2
max_calls_per_turn = 1`, 1)

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind:   fake.StepKindReply,
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			enginetest.Reply("result1"),
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) > 0 {
						return "ERROR: expected no tools"
					}
					return "TOOL_CALLS_RESPONSE"
				},
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c2", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"b"}`}},
				},
			},
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
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
	roster = strings.Replace(roster, `max_rounds = 2`, `max_rounds = 2
max_calls_per_turn = 1`, 1)

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind:   fake.StepKindReply,
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			enginetest.Reply("result1"),
			enginetest.Reply("the final answer"),
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
		"reviewer": {
			enginetest.Reply(fake.VerdictApprove("h", "a")),
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
	roster = strings.Replace(roster, `max_rounds = 2`, `max_rounds = 2
max_reviewer_calls = 1`, 1)

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
						return "ERROR: expected tool definitions on first reviewer attempt"
					}
					return "TOOL_CALLS"
				},
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			enginetest.Reply("file content"),
			{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					if len(req.Tools) > 0 {
						return "ERROR: expected no tool definitions at bound"
					}
					return fake.VerdictApprove("h", "a")
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

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind:   fake.StepKindReply,
				Status: transport.StatusToolCalls,
				ToolCalls: []transport.ToolCall{
					{ID: "c1", Type: "function", Function: transport.ToolCallFunction{Name: tools.NameReadFile, Arguments: `{"path":"a"}`}},
				},
			},
			enginetest.Reply("result1"),
			enginetest.Reply("ok"),
			{
				Kind:  fake.StepKindHang,
				Delay: 0,
			},
		},
		"reviewer": {
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	})

	c := s.Dial(t)
	turnID, _ := revSubmit(t, s, c, "s1", nil)
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)

	if turn.State == "" {
		t.Fatal("turn has no state")
	}
}

// TestToolLoops_ToolStepDuringCancellationAbandoned tests that a tool step
// arriving during cancellation is abandoned and not executed.
func TestToolLoops_ToolStepDuringCancellationAbandoned(t *testing.T) {
	t.Parallel()

	roster := toolLoopsRoster([]string{"read"}, nil)

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": {
			{
				Kind:  fake.StepKindHang,
				Delay: 0,
			},
		},
		"reviewer": {
			{
				Kind:  fake.StepKindHang,
				Delay: 0,
			},
		},
	})

	c := s.Dial(t)
	resp, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "test",
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	turnID := resp.TurnID

	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)

	if turn.State != engine.StateCancelled {
		t.Fatalf("expected cancelled, got %s", turn.State)
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
			enginetest.Reply("result_a"),
			enginetest.Reply("the answer"),
			enginetest.Reply("revised answer"),
			enginetest.Reply("ok"),
		},
		"reviewer": {
			fake.Step{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					return fake.VerdictBlock("hash", "attempt", "b1", "high", "result:call_a", "revise", "needs work")
				},
			},
			fake.Step{
				Kind: fake.StepKindReply,
				Build: func(req transport.CompleteRequest) string {
					return "VERDICT v2\ncandidate: hash\nattempt: att\nround: 1\nblock-state: b1 withdrawn\nverdict: approve\nreason: OK\nEND VERDICT"
				},
			},
			enginetest.Reply("ok"),
			enginetest.Reply("ok"),
		},
	})

	c := s.Dial(t)
	_, turn := revSubmit(t, s, c, "s1", revIntPtr(1))

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
						return "ERROR: repair should have no tools"
					}
					return fake.VerdictApprove("hash", "att")
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
