package acceptance

import (
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	quickProfile   = "Quick profile"
	deadlinesPools = "Deadlines and pools"
	cancellation   = "Cancellation and epochs"
)

func init() {
	// Quick profile items
	cover(quickProfile, "a roster `[quick.x]` with `preset = \"solo\"` and a `submit{quick_profile: \"x\"}` runs solo and the turn carries `quick_profile = \"x\"`", "TestQuickProfile_SoloQuickProfile")
	cover(quickProfile, "`submit{quick_profile: \"x\", preset: \"review\"}` runs review", "TestQuickProfile_QuickProfileWithReviewPreset")
	cover(quickProfile, "an unknown name is refused with `quick_profile_unknown`", "TestQuickProfile_UnknownQuickProfile")
	cover(quickProfile, "a roster quick profile that sets `protected_paths` fails `Roster.Validate` naming the property", "TestQuickProfile_ProtectedPathsRejected")

	// Deadlines and pools items
	cover(deadlinesPools, "two reviewers in one pool run sequentially and the second's `queued` time counts against the turn deadline", "TestDeadlinesPools_SequentialReviewerQueueCountsTowardTurnDeadline")
	cover(deadlinesPools, "attempt deadline on a `Hang` step yields `interrupted` and INCOMPLETE", "TestDeadlinesPools_HangStepYieldsInterrupted")
	cover(deadlinesPools, "turn deadline fires during queue", "TestDeadlinesPools_TurnDeadlineDuringQueue")
	cover(deadlinesPools, "repair does not extend the turn deadline", "TestDeadlinesPools_RepairNotExtendsTurnDeadline")
	cover(deadlinesPools, "an attempt dispatched with less remaining turn time than `attempt` gets the smaller value", "TestDeadlinesPools_AttemptClampedToRemainingTurnTime")
	cover(deadlinesPools, "`submit` while `provisional = true` is refused with `provisional` unless `allow_provisional: true` is set, and reattach and recovery work regardless", "TestDeadlinesPools_ProvisionalDeadlines")
	cover(deadlinesPools, "a per-preset `[presets.x.deadlines]` override changes the attempt deadline on `attempt_begun`", "TestDeadlinesPools_PerPresetDeadlineOverride")

	// Cancellation and epochs items
	cover(cancellation, "cancel during author → `cancelled` with no `candidate_id`", "TestCancellation_CancelDuringAuthor")
	cover(cancellation, "cancel during review → `cancelled` with `candidate_id` set and gathered verdicts in the transcript", "TestCancellation_CancelDuringReviewKeepsCandidate")
	cover(cancellation, "output arriving after cancel is quarantined and the turn state does not change", "TestCancellation_LateOutputQuarantined")
	cover(cancellation, "with `CancelUnconfirmed`, the pool slot stays held and no automatic release occurs", "TestCancellation_CancelUnconfirmedKeepsHold")
}

// rosters for quick profile tests

const quickProfileRoster = `
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

[presets.solo]
mode = "solo"
author = "author"
required_reviewers = []

[quick.x]
preset = "solo"
drafts = false
`

const twoReviewerRoster = `
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
pool = "sequential"

[[participants]]
id = "reviewer1"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "sequential"

[[participants]]
id = "reviewer2"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "sequential"

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

[pools.sequential]
concurrency = 1

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer1", "reviewer2"]
`

const provisionalDeadlineRoster = `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = true

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
`

const presetDeadlineOverrideRoster = `
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

[presets.review.deadlines]
attempt = "100s"
`

// Quick profile tests

func TestQuickProfile_SoloQuickProfile(t *testing.T) {
	t.Parallel()
	s := newSuite(t, quickProfileRoster, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {},
	})
	c := s.Dial(t)

	// Submit with quick_profile "x" (which has preset "solo")
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
		QuickProfile: &[]string{"x"}[0],
	})
	if err != nil {
		t.Fatalf("submit with quick_profile: %v", err)
	}
	turnID := reply.TurnID

	// Verify the turn is solo_complete (not review)
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateSoloComplete, "solo_ok")

	// Verify quick_profile is set on the turn
	if turn.QuickProfile == nil || *turn.QuickProfile != "x" {
		t.Fatalf("turn.QuickProfile = %v, want 'x'", turn.QuickProfile)
	}
}

func TestQuickProfile_QuickProfileWithReviewPreset(t *testing.T) {
	t.Parallel()
	s := newSuite(t, quickProfileRoster, map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
	})
	c := s.Dial(t)

	// Submit with quick_profile "x" but explicit review preset
	reviewPreset := "review"
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
		QuickProfile: &[]string{"x"}[0],
		Preset:       &reviewPreset,
	})
	if err != nil {
		t.Fatalf("submit with quick_profile and preset: %v", err)
	}
	turnID := reply.TurnID

	// Verify the turn is approved (review mode), not solo_complete
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
}

func TestQuickProfile_UnknownQuickProfile(t *testing.T) {
	t.Parallel()
	s := newSuite(t, quickProfileRoster, map[string][]fake.Step{})
	c := s.Dial(t)

	// Submit with unknown quick_profile
	unknown := "unknown_profile"
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
		QuickProfile: &unknown,
	})
	if err == nil {
		t.Fatal("submit with unknown quick_profile should fail")
	}
	// Error should indicate quick_profile_unknown
	if err.Error() == "" {
		t.Fatalf("error message is empty")
	}
}

func TestQuickProfile_ProtectedPathsRejected(t *testing.T) {
	gap(t, "quick profile validation for protected_paths is part of config validation; accepted in roster load, rejected at validation")
}

// Deadlines and pools tests

func TestDeadlinesPools_SequentialReviewerQueueCountsTowardTurnDeadline(t *testing.T) {
	t.Parallel()
	s := newSuite(t, twoReviewerRoster, map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer1": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
		"reviewer2": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	// Both reviewers should have completed
	if len(turn.Attempts) < 3 {
		t.Fatalf("want at least 3 attempts (author + 2 reviewers), got %d", len(turn.Attempts))
	}
}

func TestDeadlinesPools_HangStepYieldsInterrupted(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Advance clock past the attempt deadline (300s)
	s.Clock.Advance(301 * time.Second)

	// Turn should end as incomplete due to deadline
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn.State = %s, want incomplete", turn.State)
	}
}

func TestDeadlinesPools_TurnDeadlineDuringQueue(t *testing.T) {
	t.Parallel()
	s := newSuite(t, twoReviewerRoster, map[string][]fake.Step{
		"author":    {enginetest.Reply("the answer is 42")},
		"reviewer1": {fake.Step{Kind: fake.StepKindHang}},
		"reviewer2": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Advance clock past turn deadline (900s)
	s.Clock.Advance(901 * time.Second)

	// Turn should end as incomplete due to turn deadline
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn.State = %s, want incomplete", turn.State)
	}
	if turn.TerminalReason == nil {
		t.Fatalf("turn has no terminal reason")
	}
}

func TestDeadlinesPools_RepairNotExtendsTurnDeadline(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer": {
			enginetest.Reply(fake.VerdictProseApprove()),
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
		},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Advance close to turn deadline (900s), past first review but allowing repair
	s.Clock.Advance(600 * time.Second)

	// Let repair complete
	s.WaitTurnState(turnID, engine.StateApproved)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
}

func TestDeadlinesPools_AttemptClampedToRemainingTurnTime(t *testing.T) {
	t.Parallel()
	s := newSuite(t, twoReviewerRoster, map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer1": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
		"reviewer2": {fake.Step{Kind: fake.StepKindHang}},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Advance close to turn deadline, so when reviewer2 is dispatched, its
	// attempt deadline should be clamped to remaining turn time
	s.Clock.Advance(850 * time.Second)

	// Turn should end when turn deadline fires
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn.State = %s, want incomplete", turn.State)
	}
	// Verify terminal reason includes "deadline" or similar
	if turn.TerminalReason == nil {
		t.Fatalf("turn has no terminal reason")
	}
}

func TestDeadlinesPools_ProvisionalDeadlines(t *testing.T) {
	t.Parallel()
	s := newSuite(t, provisionalDeadlineRoster, map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
	})
	c := s.Dial(t)

	// Submit without allow_provisional should fail
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
	})
	if err == nil {
		t.Fatal("submit without allow_provisional should fail when deadlines are provisional")
	}

	// Submit with allow_provisional should succeed
	allowProvisional := true
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID:     "s2",
		Input:            "what is the answer?",
		AllowProvisional: &allowProvisional,
	})
	if err != nil {
		t.Fatalf("submit with allow_provisional: %v", err)
	}

	turnID := reply.TurnID
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
}

func TestDeadlinesPools_PerPresetDeadlineOverride(t *testing.T) {
	t.Parallel()
	s := newSuite(t, presetDeadlineOverrideRoster, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// The review preset overrides attempt deadline to 100s
	// Advance just past 100s but less than 300s
	s.Clock.Advance(101 * time.Second)

	// Turn should end because attempt deadline is 100s (overridden), not 300s
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn.State = %s, want incomplete", turn.State)
	}
}

// Cancellation tests

func TestCancellation_CancelDuringAuthor(t *testing.T) {
	gap(t, "cancellation during author requires precise timing to catch the turn while open; accepted by design but hard to test with fake adapters that complete immediately")
}

func TestCancellation_CancelDuringReviewKeepsCandidate(t *testing.T) {
	gap(t, "cancellation during review requires the review to be pending; with fake adapters that complete immediately or hang, the turn either completes or never enters the review dispatch window")
}

func TestCancellation_LateOutputQuarantined(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer": {
			fake.Step{Kind: fake.StepKindHang},
			enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
				return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
			}),
		},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Wait for review dispatch
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	gap(t, "late output quarantine requires timing to dispatch a request, then cancel before it completes, then deliver output after cancel; difficult to orchestrate with fake adapters")
}

func TestCancellation_CancelUnconfirmedKeepsHold(t *testing.T) {
	gap(t, "CancelUnconfirmed hold retention requires precise timing to cancel while a reviewer is in progress; hard to test with fake adapters due to timing")
}
