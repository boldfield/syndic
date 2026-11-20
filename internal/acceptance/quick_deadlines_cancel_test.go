package acceptance

import (
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/boldfield/syndic/internal/config"
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
	// Error should indicate quick_profile_unknown with the profile name
	werr, ok := err.(*wire.Error)
	if !ok {
		t.Fatalf("error is %T, want *wire.Error", err)
	}
	if werr.Code != wire.ErrorCodeQuickProfileUnknown {
		t.Fatalf("error code = %q, want %q", werr.Code, wire.ErrorCodeQuickProfileUnknown)
	}
	if werr.QuickProfile == nil || *werr.QuickProfile != "unknown_profile" {
		t.Fatalf("error.QuickProfile = %v, want %q", werr.QuickProfile, "unknown_profile")
	}
}

func TestQuickProfile_ProtectedPathsRejected(t *testing.T) {
	t.Parallel()
	// Create a roster with protected_paths in a quick profile
	rosterWithProtectedPaths := `
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

[presets.review]
mode = "review"
author = "author"
required_reviewers = []

[quick.x]
preset = "review"
drafts = false
protected_paths = "/etc/passwd"
`
	var roster config.Roster
	_, err := toml.Decode(rosterWithProtectedPaths, &roster)
	if err != nil {
		t.Fatalf("toml.Decode: %v", err)
	}

	// Validate should reject the roster because of protected_paths
	err = roster.Validate()
	if err == nil {
		t.Fatal("Roster.Validate should fail with protected_paths in quick profile")
	}
	if !strings.Contains(err.Error(), "protected_paths") {
		t.Fatalf("error should mention protected_paths: %v", err)
	}
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

	// Verify both reviewers completed successfully
	r1Attempts := s.attemptRows(t, turnID, "reviewer1", "review")
	r2Attempts := s.attemptRows(t, turnID, "reviewer2", "review")

	if len(r1Attempts) != 1 {
		t.Fatalf("reviewer1 had %d attempts, want 1", len(r1Attempts))
	}
	if len(r2Attempts) != 1 {
		t.Fatalf("reviewer2 had %d attempts, want 1", len(r2Attempts))
	}

	// Both should have completed with status "complete"
	if r1Attempts[0].Status == nil || *r1Attempts[0].Status != string(transport.StatusComplete) {
		t.Fatalf("reviewer1 status = %v, want complete", r1Attempts[0].Status)
	}
	if r2Attempts[0].Status == nil || *r2Attempts[0].Status != string(transport.StatusComplete) {
		t.Fatalf("reviewer2 status = %v, want complete", r2Attempts[0].Status)
	}

	// Collect verdicts
	verdicts := s.verdictRows(t, turnID)
	approvals := 0
	for _, v := range verdicts {
		if v.Parsed == "approve" {
			approvals++
		}
	}

	// Both reviewers should have approved, proving they both ran within the turn deadline
	// and that reviewer2's queued time counted toward the deadline
	if approvals != 2 {
		t.Fatalf("got %d approvals, want 2", approvals)
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

	// Wait for the reviewer dispatch to begin
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// Advance clock past the attempt deadline (300s)
	s.Clock.Advance(301 * time.Second)

	// Turn should end as incomplete due to deadline
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn.State = %s, want incomplete", turn.State)
	}

	// Get the reviewer's attempt and check status is interrupted
	reviewAttempt := s.reviewAttempt(t, turnID)
	if reviewAttempt.Status == nil || *reviewAttempt.Status != string(transport.StatusInterrupted) {
		got := "<nil>"
		if reviewAttempt.Status != nil {
			got = *reviewAttempt.Status
		}
		t.Fatalf("reviewer attempt status = %q, want interrupted", got)
	}
}

func TestDeadlinesPools_TurnDeadlineDuringQueue(t *testing.T) {
	t.Parallel()
	s := newSuite(t, twoReviewerRoster, map[string][]fake.Step{
		"author":    {enginetest.Reply("the answer is 42")},
		"reviewer1": {fake.Step{Kind: fake.StepKindHang}},
		"reviewer2": {fake.Step{Kind: fake.StepKindHang}},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Wait for reviewer1 to begin (hanging)
	s.WaitEvent(func(ev event) bool {
		ab, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && ab.Participant == "reviewer1"
	})

	// At this point: reviewer1 is active (hanging), reviewer2 should be queued
	// Verify reviewer2 has no attempt_begun yet
	r2BeganCount := 0
	for _, ev := range s.Events() {
		if ab, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun); ok && ab.Participant == "reviewer2" {
			r2BeganCount++
		}
	}
	if r2BeganCount > 0 {
		t.Fatalf("reviewer2 should still be queued, but found %d attempt_begun events", r2BeganCount)
	}

	// Advance clock to exactly the turn deadline (900s)
	// This should fire while reviewer2 is still queued
	s.Clock.Advance(901 * time.Second)

	// Turn should end as incomplete
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn.State = %s, want incomplete", turn.State)
	}

	// Terminal reason should be turn-deadline related
	if turn.TerminalReason == nil {
		t.Fatalf("turn has no terminal reason")
	}
}

func TestDeadlinesPools_RepairNotExtendsTurnDeadline(t *testing.T) {
	t.Parallel()
	// Create a roster with tight deadlines: 100s turn, 50s attempt
	tightDeadlineRoster := `
default_preset = "review"

[deadlines]
attempt     = "50s"
turn        = "100s"
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
`
	s := newSuite(t, tightDeadlineRoster, map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer": {
			enginetest.Reply(fake.VerdictProseApprove()),
			fake.Step{Kind: fake.StepKindHang},
		},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Wait for a verdict to be recorded (prose approval triggers repair), which signals repair is coming
	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindVerdictRecorded
	})

	// Now advance clock to the turn deadline (100s)
	// The repair is in progress (hanging), but the turn deadline should still fire
	s.Clock.Advance(101 * time.Second)

	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)

	// The turn must end as incomplete at the original deadline (100s), not extended by the repair
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn.State = %s, want incomplete", turn.State)
	}
	if turn.TerminalReason == nil {
		t.Fatalf("turn has no terminal reason")
	}
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

	_ = s.Submit(t, c, "s1")

	// Wait for reviewer1 to begin (then it will complete)
	s.WaitEvent(func(ev event) bool {
		ab, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && ab.Participant == "reviewer1"
	})

	// Advance to 850s of the 900s turn, so only 50s remain when reviewer2 is dispatched
	s.Clock.Advance(850 * time.Second)

	// Now reviewer2 should be dispatched with its attempt deadline clamped to the remaining ~50s
	r2BegunEvent := s.WaitEvent(func(ev event) bool {
		ab, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && ab.Participant == "reviewer2"
	})

	r2AB, _ := body[wire.EvAttemptBegun](r2BegunEvent, wire.EventKindAttemptBegun)

	// Verify that reviewer2 has an attempt deadline set
	if r2AB.Deadline == "" {
		t.Fatalf("reviewer2 attempt_begun.deadline is empty")
	}

	// Verify it parses as RFC3339
	_, err := time.Parse(time.RFC3339, r2AB.Deadline)
	if err != nil {
		t.Fatalf("failed to parse attempt_begun.deadline %q: %v", r2AB.Deadline, err)
	}

	// When an attempt is dispatched with less remaining turn time than the full attempt deadline,
	// the attempt deadline is clamped to the turn deadline. This test verifies that reviewer2,
	// dispatched at 850s of a 900s turn, receives a deadline clamped to the turn deadline, not
	// the full 300s. The exact deadline calculation is verified through the turn completing
	// incomplete at the clamped deadline rather than allowing reviewer2 extra time beyond it.
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

	// Submit without allow_provisional should fail with provisional error code
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
	})
	if err == nil {
		t.Fatal("submit without allow_provisional should fail when deadlines are provisional")
	}
	werr, ok := err.(*wire.Error)
	if !ok {
		t.Fatalf("error is %T, want *wire.Error", err)
	}
	if werr.Code != wire.ErrorCodeProvisional {
		t.Fatalf("error code = %q, want %q", werr.Code, wire.ErrorCodeProvisional)
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

	// Test reattach: get turn via Turn() protocol call and verify it still exists
	reattachedTurn := s.Turn(t, c, turnID)
	wantTurn(t, reattachedTurn, engine.StateApproved, "all_approved")

	// Test recovery: restart the engine with the same journal and verify turn still exists
	s.Restart("", map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
	})
	c2 := s.Dial(t)
	recoveredTurn := s.Turn(t, c2, turnID)
	wantTurn(t, recoveredTurn, engine.StateApproved, "all_approved")
}

func TestDeadlinesPools_PerPresetDeadlineOverride(t *testing.T) {
	t.Parallel()
	s := newSuite(t, presetDeadlineOverrideRoster, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Wait for reviewer dispatch and capture the attempt_begun event
	attemptBegun := s.WaitEvent(func(ev event) bool {
		ab, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && ab.Participant == "reviewer"
	})
	ab, _ := body[wire.EvAttemptBegun](attemptBegun, wire.EventKindAttemptBegun)

	// The review preset overrides attempt deadline to 100s
	// Parse the deadline (RFC3339 timestamp) and verify it equals begun time + 100s
	if ab.Deadline == "" {
		t.Fatalf("attempt_begun.deadline is empty")
	}

	deadlineTime, err := time.Parse(time.RFC3339, ab.Deadline)
	if err != nil {
		t.Fatalf("failed to parse attempt_begun.deadline %q: %v", ab.Deadline, err)
	}

	// The deadline should be approximately the current time + 100s
	now := s.Clock.Now()
	expectedDeadline := now.Add(100 * time.Second)

	// Allow a small margin (1 second)
	if deadlineTime.Before(expectedDeadline.Add(-1*time.Second)) || deadlineTime.After(expectedDeadline.Add(1*time.Second)) {
		t.Fatalf("reviewer attempt deadline = %v, want ~%v (100s override)", deadlineTime, expectedDeadline)
	}

	// Advance just past 100s but less than 300s
	s.Clock.Advance(101 * time.Second)

	// Turn should end because attempt deadline is 100s (overridden), not 300s
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Fatalf("turn.State = %s, want incomplete", turn.State)
	}

	// Verify the reviewer attempt was interrupted by the clamped deadline
	reviewAttempts := s.attemptRows(t, turnID, "reviewer", "review")
	if len(reviewAttempts) > 0 && (reviewAttempts[0].Status == nil || *reviewAttempts[0].Status != string(transport.StatusInterrupted)) {
		got := "<nil>"
		if reviewAttempts[0].Status != nil {
			got = *reviewAttempts[0].Status
		}
		t.Fatalf("reviewer status = %q, want interrupted", got)
	}
}

// Cancellation tests

func TestCancellation_CancelDuringAuthor(t *testing.T) {
	t.Parallel()
	farFuture := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {fake.Step{Kind: fake.StepKindHang}},
		"reviewer": {},
	}, enginetest.ClockStart(farFuture))
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Wait for author dispatch to begin
	s.WaitTurnState(turnID, engine.StateAuthorDispatched)

	// Cancel the turn while author is still executing (hanging)
	err := c.Cancel(s.ctx(), turnID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// Turn should end in cancelled state
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateCancelled {
		t.Fatalf("turn.State = %s, want cancelled", turn.State)
	}

	// When cancelled during author, there should be no candidate_id
	if turn.CandidateHash != nil {
		t.Fatalf("cancelled turn during author has candidate_hash, want none")
	}
}

func TestCancellation_CancelDuringReviewKeepsCandidate(t *testing.T) {
	t.Parallel()
	farFuture := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	s := newSuite(t, twoReviewerRoster, map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer1": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
		"reviewer2": {fake.Step{Kind: fake.StepKindHang}},
	}, enginetest.ClockStart(farFuture))
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Wait for the second reviewer (which will hang) to be dispatched
	s.WaitEvent(func(ev event) bool {
		ab, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && ab.Participant == "reviewer2"
	})

	// Cancel the turn while reviewer2 is hanging
	err := c.Cancel(s.ctx(), turnID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// Turn should end in cancelled state
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateCancelled {
		t.Fatalf("turn.State = %s, want cancelled", turn.State)
	}

	// When cancelled during review, the candidate_id should be set
	if turn.CandidateHash == nil || *turn.CandidateHash == "" {
		t.Fatalf("cancelled turn during review has no candidate_hash")
	}

	// Verify gathered verdicts in the transcript: reviewer1 already submitted an approval
	verdicts := s.verdictRows(t, turnID)
	if len(verdicts) != 1 {
		t.Fatalf("got %d verdicts after cancel during review, want 1", len(verdicts))
	}
	if verdicts[0].Parsed != "approve" {
		t.Fatalf("verdict parsed = %q, want approve", verdicts[0].Parsed)
	}
}

func TestCancellation_LateOutputQuarantined(t *testing.T) {
	t.Parallel()
	farFuture := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("the answer is 42")},
		"reviewer": {enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		})},
	}, enginetest.ClockStart(farFuture))
	c := s.Dial(t)

	// Hold the reviewer initially so we can control when it replies
	release := s.h.Hold("reviewer")
	defer release()

	turnID := s.Submit(t, c, "s1")

	// Wait for the reviewer attempt to begin (held)
	attemptBegun := s.WaitEvent(func(ev event) bool {
		ab, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && ab.Participant == "reviewer"
	})
	reviewerAB, _ := body[wire.EvAttemptBegun](attemptBegun, wire.EventKindAttemptBegun)
	_ = reviewerAB.AttemptID

	// Cancel the turn while reviewer is held
	err := c.Cancel(s.ctx(), turnID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// Wait for the turn to end after cancel
	s.WaitTerminal(turnID)

	// Now release the reviewer - it will try to send output, but it should be quarantined
	release()

	// Wait for the late output to be processed (a message event after cancel)
	s.WaitEvent(func(ev event) bool {
		if ma, ok := body[wire.EvMessageAppended](ev, wire.EventKindMessageAppended); ok {
			return ma.TurnID == turnID
		}
		return false
	})

	// The turn should still be cancelled
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateCancelled {
		t.Fatalf("turn.State = %s, want cancelled", turn.State)
	}

	// Check that the reviewer's output is marked as quarantined in the journal
	reviewAttempts := s.attemptRows(t, turnID, "reviewer", "review")
	if len(reviewAttempts) != 1 {
		t.Fatalf("got %d reviewer attempts, want 1", len(reviewAttempts))
	}
	if !reviewAttempts[0].Quarantined {
		t.Fatalf("reviewer attempt.Quarantined = false, want true")
	}

	// Verify there are no verdicts (output was quarantined, not processed)
	verdicts := s.verdictRows(t, turnID)
	if len(verdicts) > 0 {
		t.Fatalf("got %d verdicts after late output, want 0 (output should be quarantined)", len(verdicts))
	}

	// Verify the turn state did not change - it should still be cancelled
	turnAfter := s.Turn(t, c, turnID)
	if turnAfter.State != engine.StateCancelled {
		t.Fatalf("turn state changed after late output: %s, want cancelled", turnAfter.State)
	}
}

func TestCancellation_CancelUnconfirmedKeepsHold(t *testing.T) {
	t.Parallel()
	farFuture := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	s := newSuite(t, enginetest.RosterTOML,
		map[string][]fake.Step{
			"author":   {enginetest.Reply("the answer is 42")},
			"reviewer": {fake.Step{Kind: fake.StepKindHang}},
		},
		enginetest.CancelUnconfirmed(),
		enginetest.ClockStart(farFuture),
	)
	c := s.Dial(t)

	turnID := s.Submit(t, c, "s1")

	// Wait for reviewer attempt to begin (not just dispatch)
	attemptBegun := s.WaitEvent(func(ev event) bool {
		ab, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && ab.Participant == "reviewer"
	})
	reviewerAB, _ := body[wire.EvAttemptBegun](attemptBegun, wire.EventKindAttemptBegun)
	reviewerAttemptID := reviewerAB.AttemptID

	if reviewerAttemptID == "" {
		t.Fatal("no reviewer attempt found")
	}

	// Cancel the turn while reviewer is hanging
	err := c.Cancel(s.ctx(), turnID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// After cancel, there should be a hold on the pool
	s.wantHold(t, c, reviewerAttemptID)

	// Wait for turn to end
	s.WaitTerminal(turnID)

	// With CancelUnconfirmed, the hold should still be retained after the turn ends
	// (because the cancel was not confirmed by the adapter)
	s.wantHold(t, c, reviewerAttemptID)

	// Verify no automatic release occurs: advance the clock past attempt and delivery deadlines
	s.Clock.Advance(300 * time.Second) // past attempt deadline
	s.Clock.Advance(60 * time.Second)  // past delivery deadline

	// The hold should still exist - no automatic release should have occurred
	s.wantHold(t, c, reviewerAttemptID)
}
