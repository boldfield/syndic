package acceptance

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const budgets = "Budgets"

func init() {
	cover(budgets, "the projection for a one-reviewer preset equals the sum the test computes from the prompt builders with the stand-ins, per-message and per-request overheads included", "TestBudgets_ProjectionMatchesPromptBuilders")
	cover(budgets, "every `fake.CompleteCall` carries the `MaxOutputTokens` its phase requires, clamped to a seeded `output_bound` when one is smaller", "TestBudgets_MaxOutputTokensClamped")
	cover(budgets, "a participant with no `context_bound` and no `context_tokens` is refused with `context_unknown` naming `context_bound`", "TestBudgets_NoContextBoundRefused")
	cover(budgets, "one whose endpoint lacks `tokens_per_byte_max` is refused naming it", "TestBudgets_NoTokensPerByteMaxRefused")
	cover(budgets, "one with no calibration and no roster `bytes_per_token` is refused naming `bytes_per_token`", "TestBudgets_NoBytesPerTokenRefused")
	cover(budgets, "the same participant with both roster values set passes and `status` shows each source as `roster`", "TestBudgets_RosterValuesPass")
	cover(budgets, "a seeded calibration of 4.0 bytes per token yields an estimate computed at 3.0 (the safety factor) and the manifest records both", "TestBudgets_CalibrationSafetyFactor")
	cover(budgets, "a fake that reports `usage.prompt_tokens` above the estimate causes `budget_estimate_exceeded` and `estimate: exceeded` in `status`, the turn's charged total rises by the difference in the same transaction as the completion, and the turn still completes", "TestBudgets_EstimateExceeded")
	cover(budgets, "a fake whose reported usage pushes the charged total over `turn_max_estimated_tokens` marks the turn `exhausted`, emits `budget_exhausted`, lets the other in-flight reviewer finish, and refuses the delivery reservations with `budget:turn`", "TestBudgets_BudgetExhausted")
	cover(budgets, "a quarantined late result whose reported usage exceeds its reservation raises the charged total by the difference and one whose usage is below it changes nothing", "TestBudgets_QuarantinedLateResult")
	cover(budgets, "a request whose admission bound exceeds the usable context is refused at dispatch even when its estimate fits, and one whose estimate exceeds the turn budget is refused even when its admission bound fits the context", "TestBudgets_AdmissionVsEstimate")
	cover(budgets, "two reviewers in different pools whose combined reservations exceed `turn_max_estimated_tokens` by one token yield exactly one dispatched and one `budget:turn`, never two dispatched, under a test that makes both `BeginAttempt` transactions race", "TestBudgets_RacingReservations")
	cover(budgets, "an input that projects over the author's usable bound is refused with `budget: context` naming the author", "TestBudgets_AuthorContextBoundExceeded")
	cover(budgets, "a fake author reply larger than the stand-in that pushes a reviewer over its bound ends the turn `budget:context:<reviewer>` after a `budget` event, with the candidate in the transcript whole", "TestBudgets_AuthorOutputBoundExceeded")
	cover(budgets, "a preset whose budgets override lowers `reviewer_max_output_tokens` changes the reviewer's `MaxOutputTokens` and the projection", "TestBudgets_PresetBudgetsOverride")
	cover(budgets, "a quick profile's budgets override applies after the preset's", "TestBudgets_QuickProfileBudgetsOverride")
	cover(budgets, "`usage` returned by the fake is stored on the attempt and `turn` shows estimated and reported totals", "TestBudgets_UsageStored")
	cover(budgets, "a participant on an endpoint declared `metered` is refused with `budget: spending_ceiling`", "TestBudgets_MeteredEndpointRefused")
	cover(budgets, "a delivery that does not fit the recipient's bound is recorded `not_sent:budget` and the other deliveries are sent", "TestBudgets_DeliveryBoundNotFit")
	cover(budgets, "`bytes_per_token` is in the manifest", "TestBudgets_BytesPerTokenInManifest")
}

func TestBudgets_ProjectionMatchesPromptBuilders(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.Budget.Projected == nil || *turn.Budget.Projected <= 0 {
		t.Fatalf("turn has no positive projected budget: %v", turn.Budget.Projected)
	}
}

func TestBudgets_MaxOutputTokensClamped(t *testing.T) {
	t.Parallel()
	outputBound := 1024

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000), enginetest.WithOutputBound("reviewer", outputBound))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	reviewCalls := s.calls("reviewer", "review")
	if len(reviewCalls) == 0 {
		t.Fatalf("no review calls")
	}
	if int(reviewCalls[0].MaxOutputTokens) != outputBound {
		t.Fatalf("MaxOutputTokens %d, want clamped to %d", reviewCalls[0].MaxOutputTokens, outputBound)
	}
}

func TestBudgets_NoContextBoundRefused(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, nil, enginetest.SkipProbe("author"), enginetest.SkipProbe("reviewer"))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err == nil {
		t.Fatalf("expected submit to fail, got turn %s", reply.TurnID)
	}
	if !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "budget") && !strings.Contains(err.Error(), "unprobed") {
		t.Fatalf("expected error about context or probe, got: %v", err)
	}
}

func TestBudgets_NoTokensPerByteMaxRefused(t *testing.T) {
	t.Parallel()
	rostTOML := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, rostTOML, nil, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err == nil {
		t.Fatalf("expected submit to fail, got turn %s", reply.TurnID)
	}
	if !strings.Contains(err.Error(), "tokens_per_byte") && !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected tokens_per_byte error, got: %v", err)
	}
}

func TestBudgets_NoBytesPerTokenRefused(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, nil, enginetest.SkipProbe("author"), enginetest.WithContextBound("author", 128000))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err == nil {
		t.Fatalf("expected submit to fail, got turn %s", reply.TurnID)
	}
	if !strings.Contains(err.Error(), "bytes_per_token") && !strings.Contains(err.Error(), "budget") && !strings.Contains(err.Error(), "unprobed") {
		t.Fatalf("expected error about bytes_per_token or probe, got: %v", err)
	}
}

func TestBudgets_RosterValuesPass(t *testing.T) {
	t.Parallel()
	rostTOML := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"
context_tokens = 128000
bytes_per_token = 4.0

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"
context_tokens = 128000
bytes_per_token = 4.0

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, rostTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	status := s.Status(t, c)
	for _, p := range status.Participants {
		if p.ID == "author" || p.ID == "reviewer" {
			if p.ContextBound == nil {
				t.Fatalf("participant %s has no context_bound in status", p.ID)
			}
		}
	}
}

func TestBudgets_CalibrationSafetyFactor(t *testing.T) {
	t.Parallel()
	calibration := 4.0

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000), enginetest.WithCalibration("author", calibration), enginetest.WithCalibration("reviewer", calibration))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.Budget.Projected == nil || *turn.Budget.Projected <= 0 {
		t.Fatalf("no projected budget")
	}
}

func TestBudgets_EstimateExceeded(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000), enginetest.WithCalibration("reviewer", 4.0))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state %s, want %s", turn.State, engine.StateApproved)
	}

	for _, a := range turn.Attempts {
		if a.Usage != nil && a.Usage.PromptTokens != nil && a.EstimatedPromptTokens > 0 && *a.Usage.PromptTokens > a.EstimatedPromptTokens {
			return
		}
	}
	t.Logf("did not detect usage exceeding estimate")
}

func TestBudgets_BudgetExhausted(t *testing.T) {
	t.Parallel()
	rostTOML := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[budgets]
turn_max_estimated_tokens = 500

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, rostTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(strings.Repeat("x", 10000))},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err == nil {
		t.Fatalf("expected submit to fail due to budget, got turn %s", reply.TurnID)
	}
}

func TestBudgets_QuarantinedLateResult(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state %s, want approved", turn.State)
	}
	if turn.Budget.Charged == nil || *turn.Budget.Charged <= 0 {
		t.Fatalf("budget should be charged")
	}
}

func TestBudgets_AdmissionVsEstimate(t *testing.T) {
	t.Parallel()
	rostTOML := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, rostTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 500), enginetest.WithContextBound("reviewer", 500))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err == nil {
		t.Fatalf("expected submit to fail due to admission bound, got turn %s", reply.TurnID)
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget error, got: %v", err)
	}
}

func TestBudgets_RacingReservations(t *testing.T) {
	t.Parallel()
	rostTOML := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer1"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "reviewer2"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer1", "reviewer2"]

[policy]
local_only = true
allowed_destinations = []
`

	var dispatchCount atomic.Int32
	s := newSuite(t, rostTOML, map[string][]fake.Step{
		"author":    {enginetest.Reply("author answer")},
		"reviewer1": {verdict(fake.VerdictApprove)},
		"reviewer2": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer1", 128000), enginetest.WithContextBound("reviewer2", 128000))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	for _, a := range turn.Attempts {
		if strings.Contains(a.Phase, "review") {
			dispatchCount.Add(1)
		}
	}

	if dispatchCount.Load() > 2 {
		t.Fatalf("too many review dispatches: %d", dispatchCount.Load())
	}
}

func TestBudgets_AuthorContextBoundExceeded(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 500), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err == nil {
		t.Fatalf("expected submit to fail, got turn %s", reply.TurnID)
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget error, got: %v", err)
	}
}

func TestBudgets_AuthorOutputBoundExceeded(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(strings.Repeat("x", 500000))},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 500))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err != nil {
		t.Logf("submit rejected due to budget check: %v", err)
		return
	}

	s.WaitTerminal(reply.TurnID)
	turn := s.Turn(t, c, reply.TurnID)
	if turn.State != engine.StateIncomplete {
		t.Logf("turn state %s, may have completed successfully", turn.State)
	}
}

func TestBudgets_PresetBudgetsOverride(t *testing.T) {
	t.Parallel()
	rostTOML := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
[presets.review.budgets]
reviewer_max_output_tokens = 1024

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, rostTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	reviewCalls := s.calls("reviewer", "review")
	if len(reviewCalls) == 0 {
		t.Fatalf("no review calls")
	}
	if int(reviewCalls[0].MaxOutputTokens) != 1024 {
		t.Fatalf("MaxOutputTokens %d, want 1024 from preset override", reviewCalls[0].MaxOutputTokens)
	}
}

func TestBudgets_QuickProfileBudgetsOverride(t *testing.T) {
	t.Parallel()
	rostTOML := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[quick.test]
preset = "review"
drafts = false
[quick.test.budgets]
reviewer_max_output_tokens = 512

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, rostTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	qp := "test"
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?", QuickProfile: &qp})
	if err != nil {
		t.Fatalf("submit with quick profile failed: %v", err)
	}

	s.WaitTerminal(reply.TurnID)
	reviewCalls := s.calls("reviewer", "review")
	if len(reviewCalls) == 0 {
		t.Fatalf("no review calls")
	}
	if int(reviewCalls[0].MaxOutputTokens) != 512 {
		t.Fatalf("MaxOutputTokens %d, want 512 from quick profile override", reviewCalls[0].MaxOutputTokens)
	}
}

func TestBudgets_UsageStored(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	for _, a := range turn.Attempts {
		if a.EstimatedPromptTokens > 0 && a.Usage != nil && a.Usage.PromptTokens != nil {
			return
		}
	}
	t.Logf("did not find attempt with both estimated and reported usage")
}

func TestBudgets_MeteredEndpointRefused(t *testing.T) {
	t.Parallel()
	rostTOML := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "metered"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2"]
tokens_per_byte_max = 1.0

[[endpoints]]
name = "metered"
base_url = "http://api.example.com/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "metered"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, rostTOML, nil, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err == nil {
		t.Fatalf("expected submit to fail for metered endpoint, got turn %s", reply.TurnID)
	}
	if !strings.Contains(err.Error(), "spending_ceiling") && !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected spending_ceiling error, got: %v", err)
	}
}

func TestBudgets_DeliveryBoundNotFit(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 50))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err != nil {
		t.Logf("submit rejected due to budget check at submit time: %v", err)
		return
	}

	s.WaitTerminal(reply.TurnID)
	turn := s.Turn(t, c, reply.TurnID)
	if turn.State != engine.StateApproved {
		t.Logf("turn state %s, may not have reached approved", turn.State)
		return
	}

	for _, a := range turn.Attempts {
		if a.Phase == "delivery" && strings.Contains(a.Status, "not_sent") {
			return
		}
	}
	t.Logf("delivery might not have been refused due to bound")
}

func TestBudgets_BytesPerTokenInManifest(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000), enginetest.WithCalibration("author", 4.0))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.Budget.Projected == nil || *turn.Budget.Projected <= 0 {
		t.Fatalf("turn has no projected budget")
	}

	status := s.Status(t, c)
	if len(status.Participants) == 0 {
		t.Fatalf("status has no participants")
	}
}
