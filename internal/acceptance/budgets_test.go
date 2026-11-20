package acceptance

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
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

// budgetsRosterWithBudget returns a roster TOML with customizable budget configuration.
func budgetsRosterWithBudget(budgetConfig string) string {
	return `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

` + budgetConfig + `

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
}

// budgetsRosterWithMultipleReviewersAndBudget returns a roster with two reviewers in different pools.
func budgetsRosterWithMultipleReviewersAndBudget(budgetConfig string) string {
	return `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

` + budgetConfig + `

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
pool = "pool1"

[[participants]]
id = "reviewer2"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "pool2"

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

[pools.pool1]
concurrency = 1

[pools.pool2]
concurrency = 1

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer1", "reviewer2"]

[policy]
local_only = true
allowed_destinations = []
`
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
		t.Fatalf("projection should be positive, got %v", turn.Budget.Projected)
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

	turn := s.Turn(t, c, turnID)
	for _, a := range turn.Attempts {
		if a.Phase == "author" && int(a.MaxOutputTokens) != 4096 {
			t.Fatalf("author MaxOutputTokens %d, want 4096", a.MaxOutputTokens)
		}
		if a.Phase == "review" && int(a.MaxOutputTokens) != outputBound {
			t.Fatalf("review MaxOutputTokens %d, want clamped to %d", a.MaxOutputTokens, outputBound)
		}
		if a.Phase == "terminal_delivery" && int(a.MaxOutputTokens) != 64 {
			t.Fatalf("delivery MaxOutputTokens %d, want 64", a.MaxOutputTokens)
		}
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
	if !strings.Contains(err.Error(), "unprobed") {
		t.Fatalf("expected unprobed error, got: %v", err)
	}
}

// budgetsRosterNoTokensPerByteMax returns a roster without tokens_per_byte_max on the endpoint.
func budgetsRosterNoTokensPerByteMax() string {
	return `
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
}

func TestBudgets_NoTokensPerByteMaxRefused(t *testing.T) {
	t.Parallel()
	s := newSuite(t, budgetsRosterNoTokensPerByteMax(), nil, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err == nil {
		t.Fatalf("expected submit to fail with budget error")
	}
	we, ok := err.(*wire.Error)
	if !ok {
		t.Fatalf("expected *wire.Error, got %T: %v", err, err)
	}
	if we.Code != wire.ErrorCodeBudget || we.Budget == nil || *we.Budget != "context_unknown" || we.Missing == nil || *we.Missing != "tokens_per_byte_max" {
		t.Fatalf("expected context_unknown with missing tokens_per_byte_max, got: %+v", we)
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
	if !strings.Contains(err.Error(), "unprobed") && !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected unprobed or budget error, got: %v", err)
	}
}

// budgetsRosterWithRosterValues returns a roster with both context_tokens and bytes_per_token set.
func budgetsRosterWithRosterValues() string {
	return `
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
}

func TestBudgets_RosterValuesPass(t *testing.T) {
	t.Parallel()
	s := newSuite(t, budgetsRosterWithRosterValues(), map[string][]fake.Step{
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
			if p.BytesPerTokenSource == "" {
				t.Fatalf("participant %s has no bytes_per_token source recorded", p.ID)
			}
			if p.ContextSource == "" {
				t.Fatalf("participant %s has no context source recorded", p.ID)
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

	status := s.Status(t, c)

	var runID string
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindEngineStarted {
			var started wire.EvEngineStarted
			if err := json.Unmarshal(ev.Body, &started); err == nil {
				runID = started.RunID
				break
			}
		}
	}
	if runID == "" {
		t.Fatalf("could not find engine_started event with runID")
	}

	runRow, err := s.Store.GetRun(runID)
	if err != nil {
		t.Fatalf("cannot get run: %v", err)
	}

	var manifestData map[string]interface{}
	if err := json.Unmarshal(runRow.Manifest, &manifestData); err != nil {
		t.Fatalf("cannot parse manifest: %v", err)
	}

	probeRecords, ok := manifestData["probe_records"].(map[string]interface{})
	if !ok {
		t.Fatalf("manifest has no probe_records")
	}

	for _, p := range status.Participants {
		if p.ID == "author" || p.ID == "reviewer" {
			if p.BytesPerTokenSource == "" {
				t.Fatalf("participant %s should have bytes_per_token source", p.ID)
			}

			if probeRec, ok := probeRecords[p.ID].(map[string]interface{}); ok {
				if cal, ok := probeRec["calibration"].(float64); ok {
					expected := budget.EffectiveBytesPerToken(calibration, 0.75)
					if cal != expected {
						t.Errorf("manifest calibration %v, want %v (original %v with 0.75 safety)", cal, expected, calibration)
					}
				}
			}
		}
	}
}

func TestBudgets_EstimateExceeded(t *testing.T) {
	t.Parallel()
	reviewStep := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
	reviewStep.Usage = &transport.Usage{Prompt: 10000, Completion: 100}

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {reviewStep},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000), enginetest.WithCalibration("reviewer", 4.0))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Fatalf("turn state %s, want %s", turn.State, engine.StateApproved)
	}

	foundExceeded := false
	for _, a := range turn.Attempts {
		if a.Phase == "review" && a.Usage != nil && a.Usage.PromptTokens != nil &&
			a.EstimatedPromptTokens > 0 && *a.Usage.PromptTokens > a.EstimatedPromptTokens {
			foundExceeded = true
			break
		}
	}
	if !foundExceeded {
		t.Fatalf("did not find review attempt with reported usage exceeding estimate")
	}

	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindBudgetEstimateExceeded {
			return
		}
	}
	t.Fatalf("did not find budget_estimate_exceeded event")
}

func TestBudgets_BudgetExhausted(t *testing.T) {
	t.Parallel()
	budgetConfig := `[budgets]
turn_max_estimated_tokens = 50000
`
	rostTOML := budgetsRosterWithBudget(budgetConfig)

	reviewStep := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
	reviewStep.Usage = &transport.Usage{Prompt: 60000, Completion: 100}

	s := newSuite(t, rostTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {reviewStep},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if !turn.Budget.Exhausted {
		t.Fatalf("budget should be exhausted after reported usage exceeds limit")
	}

	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindBudgetExhausted {
			return
		}
	}
	t.Fatalf("did not find budget_exhausted event")
}

func TestBudgets_QuarantinedLateResult(t *testing.T) {
	t.Parallel()
	t.Run("above_reservation", func(t *testing.T) {
		t.Parallel()
		reviewStep := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})
		reviewStep.Usage = &transport.Usage{Prompt: 5000, Completion: 500}

		s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
			"author":   {enginetest.Reply("author answer")},
			"reviewer": {reviewStep},
		}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

		c := s.Dial(t)
		turnID := s.Submit(t, c, "s1")
		s.WaitTerminal(turnID)

		turn := s.Turn(t, c, turnID)
		if turn.State != engine.StateApproved {
			t.Fatalf("turn state %s, want approved", turn.State)
		}

		beforeCharge := int64(0)
		for _, a := range turn.Attempts {
			if a.Phase == "review" && a.EstimatedPromptTokens > 0 {
				beforeCharge = a.EstimatedPromptTokens
				break
			}
		}
		if beforeCharge == 0 {
			t.Fatalf("no review attempt found")
		}

		if turn.Budget.Charged == nil || *turn.Budget.Charged <= beforeCharge {
			t.Fatalf("charged budget should exceed estimated reservation after high reported usage")
		}
	})

	t.Run("below_reservation", func(t *testing.T) {
		t.Parallel()
		reviewStep := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})
		reviewStep.Usage = &transport.Usage{Prompt: 100, Completion: 10}

		s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
			"author":   {enginetest.Reply("author answer")},
			"reviewer": {reviewStep},
		}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

		c := s.Dial(t)
		turnID := s.Submit(t, c, "s2")
		s.WaitTerminal(turnID)

		turn := s.Turn(t, c, turnID)
		if turn.State != engine.StateApproved {
			t.Fatalf("turn state %s, want approved", turn.State)
		}
	})
}

func TestBudgets_AdmissionVsEstimate(t *testing.T) {
	t.Parallel()
	t.Run("admission_exceeds_context", func(t *testing.T) {
		t.Parallel()
		s := newSuite(t, budgetsRosterWithBudget(""), map[string][]fake.Step{
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
	})

	t.Run("estimate_exceeds_turn_budget", func(t *testing.T) {
		t.Parallel()
		budgetConfig := `[budgets]
turn_max_estimated_tokens = 100
`
		s := newSuite(t, budgetsRosterWithBudget(budgetConfig), map[string][]fake.Step{
			"author":   {enginetest.Reply("author answer")},
			"reviewer": {verdict(fake.VerdictApprove)},
		}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

		c := s.Dial(t)
		reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
		if err == nil {
			t.Fatalf("expected submit to fail due to turn budget, got turn %s", reply.TurnID)
		}
		if !strings.Contains(err.Error(), "budget") {
			t.Fatalf("expected budget error, got: %v", err)
		}
	})
}

func TestBudgets_RacingReservations(t *testing.T) {
	t.Parallel()
	// Budget set so both reviewers can dispatch but their combined usage approaches
	// the limit, forcing one to be refused with budget:turn under racing BeginAttempt.
	// The race is forced because both reviewers' transactions contend for the same
	// turn budget in the journal, with the second seeing the first's reservation.
	budgetConfig := `[budgets]
turn_max_estimated_tokens = 100000
`
	rostTOML := budgetsRosterWithMultipleReviewersAndBudget(budgetConfig)

	var dispatchCount atomic.Int32
	var budgetTurnSeen atomic.Bool

	s := newSuite(t, rostTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("author answer")},
		"reviewer1": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			dispatchCount.Add(1)
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
		"reviewer2": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			dispatchCount.Add(1)
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer1", 128000), enginetest.WithContextBound("reviewer2", 128000))

	for i := 0; i < 3; i++ {
		c := s.Dial(t)
		reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s" + string(rune(i)), Input: "what is the answer?"})
		if err != nil {
			t.Fatalf("iteration %d: submit failed: %v", i, err)
		}

		s.WaitTerminal(reply.TurnID)

		for _, ev := range s.Events() {
			if ev.Kind == wire.EventKindBudget {
				var budgetEv wire.EvBudget
				if err := json.Unmarshal(ev.Body, &budgetEv); err == nil {
					if strings.Contains(budgetEv.Budget, "budget:turn") {
						budgetTurnSeen.Store(true)
					}
				}
			}
		}
		_ = c.Close()
	}

	if dispatchCount.Load() > 3 {
		t.Fatalf("both reviewers were dispatched in multiple iterations; dispatch count: %d", dispatchCount.Load())
	}

	if !budgetTurnSeen.Load() {
		t.Fatalf("did not see budget:turn event across iterations; race may not be forcing reservation conflict")
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
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 50))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err != nil {
		if strings.Contains(err.Error(), "budget") {
			return
		}
		t.Fatalf("unexpected submit error: %v", err)
	}

	s.WaitTerminal(reply.TurnID)
	turn := s.Turn(t, c, reply.TurnID)

	if turn.State != engine.StateIncomplete {
		t.Fatalf("expected incomplete state after budget:context event, got %s", turn.State)
	}
	if turn.TerminalReason == nil || !strings.Contains(*turn.TerminalReason, "budget:context") {
		t.Fatalf("expected terminal reason with budget:context, got %v", turn.TerminalReason)
	}

	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindBudget {
			var budgetEv wire.EvBudget
			if err := json.Unmarshal(ev.Body, &budgetEv); err == nil {
				if strings.Contains(budgetEv.Budget, "budget:context") {
					return
				}
			}
		}
	}
	t.Fatalf("did not find budget event with budget:context")
}

func budgetsRosterPresetOverride() string {
	return `
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
}

func TestBudgets_PresetBudgetsOverride(t *testing.T) {
	t.Parallel()
	s := newSuite(t, budgetsRosterPresetOverride(), map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	for _, a := range turn.Attempts {
		if a.Phase == "review" {
			if int(a.MaxOutputTokens) != 1024 {
				t.Fatalf("review MaxOutputTokens %d, want 1024 from preset override", a.MaxOutputTokens)
			}
		}
	}

	if turn.Budget.Projected == nil || *turn.Budget.Projected <= 0 {
		t.Fatalf("projection should change with override")
	}

	sDefault := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c2 := sDefault.Dial(t)
	turnID2 := sDefault.Submit(t, c2, "s2")
	sDefault.WaitTerminal(turnID2)

	turnDefault := sDefault.Turn(t, c2, turnID2)
	if turnDefault.Budget.Projected == nil {
		t.Fatalf("default projection should be set")
	}

	if *turn.Budget.Projected == *turnDefault.Budget.Projected {
		t.Errorf("projection with override %d should differ from default %d", *turn.Budget.Projected, *turnDefault.Budget.Projected)
	}
}

func budgetsRosterQuickProfile() string {
	return `
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
reviewer_max_output_tokens = 2048

[quick.test]
preset = "review"
drafts = false
[quick.test.budgets]
reviewer_max_output_tokens = 512

[policy]
local_only = true
allowed_destinations = []
`
}

func TestBudgets_QuickProfileBudgetsOverride(t *testing.T) {
	t.Parallel()
	s := newSuite(t, budgetsRosterQuickProfile(), map[string][]fake.Step{
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
	turn := s.Turn(t, c, reply.TurnID)
	for _, a := range turn.Attempts {
		if a.Phase == "review" {
			if int(a.MaxOutputTokens) != 512 {
				t.Fatalf("review MaxOutputTokens %d, want 512 from quick profile (applied after preset)", a.MaxOutputTokens)
			}
		}
	}
}

func TestBudgets_UsageStored(t *testing.T) {
	t.Parallel()
	reviewStep := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
	reviewStep.Usage = &transport.Usage{Prompt: 1000, Completion: 100}

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("author answer")},
		"reviewer": {reviewStep},
	}, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)

	var foundEstimate, foundReported bool
	for _, a := range turn.Attempts {
		if a.EstimatedPromptTokens > 0 {
			foundEstimate = true
		}
		if a.Phase == "review" && a.Usage != nil && a.Usage.PromptTokens != nil && *a.Usage.PromptTokens == 1000 {
			foundReported = true
		}
	}
	if !foundEstimate {
		t.Fatalf("did not find attempt with estimated prompt tokens")
	}
	if !foundReported {
		t.Fatalf("did not find attempt with reported usage of 1000 prompt tokens")
	}

	if turn.Budget.Charged == nil || *turn.Budget.Charged <= 0 {
		t.Fatalf("turn budget should show charged total")
	}
}

// budgetsRosterMeteredEndpoint returns a roster with one metered endpoint.
func budgetsRosterMeteredEndpoint() string {
	return `
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
}

func TestBudgets_MeteredEndpointRefused(t *testing.T) {
	t.Parallel()
	s := newSuite(t, budgetsRosterMeteredEndpoint(), nil, enginetest.WithContextBound("author", 128000), enginetest.WithContextBound("reviewer", 128000))

	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err == nil {
		t.Fatalf("expected submit to fail for metered endpoint, got turn %s", reply.TurnID)
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("expected budget error, got: %v", err)
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
		if strings.Contains(err.Error(), "budget") {
			return
		}
		t.Fatalf("unexpected submit error: %v", err)
	}

	s.WaitTerminal(reply.TurnID)
	turn := s.Turn(t, c, reply.TurnID)

	if turn.State != engine.StateApproved && turn.State != engine.StateIncomplete {
		t.Fatalf("turn state %s, want approved or incomplete", turn.State)
	}

	foundRefusedDelivery := false
	foundOtherDelivery := false
	for _, a := range turn.Attempts {
		if a.Phase == "terminal_delivery" {
			if strings.Contains(a.Status, "not_sent:budget") {
				foundRefusedDelivery = true
			} else if !strings.Contains(a.Status, "not_sent:") && a.Status != "" {
				foundOtherDelivery = true
			}
		}
	}

	if !foundRefusedDelivery {
		t.Fatalf("did not find delivery marked not_sent:budget with recipient bound of 50 tokens")
	}

	if !foundOtherDelivery {
		t.Fatalf("did not find any other delivery attempt that was sent")
	}
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

	for _, p := range status.Participants {
		if p.ID == "author" {
			if p.BytesPerTokenSource == "" {
				t.Fatalf("author participant should have bytes_per_token source recorded")
			}
		}
	}

	var runID string
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindEngineStarted {
			var started wire.EvEngineStarted
			if err := json.Unmarshal(ev.Body, &started); err == nil {
				runID = started.RunID
				break
			}
		}
	}
	if runID == "" {
		t.Fatalf("could not find engine_started event with runID")
	}

	runRow, err := s.Store.GetRun(runID)
	if err != nil {
		t.Fatalf("cannot get run: %v", err)
	}

	var manifestData map[string]interface{}
	if err := json.Unmarshal(runRow.Manifest, &manifestData); err != nil {
		t.Fatalf("cannot parse manifest: %v", err)
	}

	probeRecords, ok := manifestData["probe_records"].(map[string]interface{})
	if !ok {
		t.Fatalf("manifest has no probe_records")
	}

	authorRec, ok := probeRecords["author"].(map[string]interface{})
	if !ok {
		t.Fatalf("manifest has no author probe record")
	}

	if _, ok := authorRec["calibration"]; !ok {
		t.Fatalf("author probe record does not have calibration (bytes_per_token)")
	}
}
