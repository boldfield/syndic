package acceptance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const revisionPara = "Revision"

func init() {
	cover(revisionPara, "one block followed by a revised candidate and two approvals yields `approved` with round count 1", "TestRevision_BlockAndRevisionApproved")
	cover(revisionPara, "a block maintained through `max_rounds` yields `rounds_exhausted` with the block open", "TestRevision_BlockMaintainedThroughMaxRounds")
	cover(revisionPara, "`max_rounds = 0` reproduces every alpha result", "TestRevision_MaxRoundsZeroNoRevision")
	cover(revisionPara, "a reviewer failure in any round yields `incomplete` and no further author dispatch", "TestRevision_ReviewerFailureEndsRound")
	cover(revisionPara, "an identical answer over an identical change set yields `revision_unchanged`", "TestRevision_UnchangedRevisionUnchangedChanges")
	cover(revisionPara, "an identical answer over a changed change set does not", "TestRevision_UnchangedAnswerChangedChanges")
	cover(revisionPara, "an approval of round 0 does not count for round 1", "TestRevision_ApprovalNotTransferred")
	cover(revisionPara, "a reviewer cannot withdraw another reviewer's block", "TestRevision_CannotWithdrawOthersBlock")
	cover(revisionPara, "`approve` with an own block maintained is malformed", "TestRevision_ApproveWithOwnBlockMaintainedMalformed")
	cover(revisionPara, "a `block` verdict that omits one of the reviewer's own open blocks is malformed and the omitted block stays open", "TestRevision_OmittedBlockStaysOpen")
	cover(revisionPara, "no reviewer request contains the round limit or another reviewer's block", "TestRevision_ReviewerNoLimitOrOthersBlocks")
	cover(revisionPara, "a round that would exceed the turn budget is not started", "TestRevision_RoundExceedsBudgetNotStarted")
	cover(revisionPara, "late results from an earlier round are quarantined", "TestRevision_LateResultQuarantined")
	cover(revisionPara, "`submit` with `max_rounds` above the preset's ceiling or below 0 creates no turn", "TestRevision_MaxRoundsOutOfRange")
	cover(revisionPara, "a roster whose default exceeds its ceiling, or whose ceiling exceeds 5, is refused at load", "TestRevision_RosterMaxRoundsValidation")
	cover(revisionPara, "`status` reports the default and the ceiling in force", "TestRevision_StatusReportsDefaults")
	cover(revisionPara, "`max_rounds` on a submit overrides the roster in both directions within the ceiling", "TestRevision_SubmitOverridesDefault")
	cover(revisionPara, "`--rounds 0` yields no revision on a roster whose default is 2", "TestRevision_RoundsZeroFlag")
	cover(revisionPara, "the turn record carries the limit and the ceiling in force", "TestRevision_TurnRecordCarriesLimits")
	cover(revisionPara, "a revision request carries the tool results a block cites and no other reviewer result", "TestRevision_BlockCitesOnlySpecificResults")
}

func authorGenericStep() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase == "revision" {
			return authorOK + " (revised)"
		}
		return authorOK
	})
}

func roundSteps(n int) []fake.Step {
	out := make([]fake.Step, n)
	for i := range out {
		out[i] = authorGenericStep()
	}
	return out
}

func roundSuite(t *testing.T, roster string, reviewerFirst []fake.Step) *suite {
	t.Helper()
	return newSuite(t, roster, map[string][]fake.Step{
		"author":   roundSteps(32),
		"reviewer": append(append([]fake.Step(nil), reviewerFirst...), roundSteps(32)...),
	})
}

func rosterWithMaxRounds(defaultRounds, ceilingRounds int) string {
	return fmt.Sprintf(`
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[revision]
max_rounds = %d
max_rounds_ceiling = %d

[[participants]]
id = "author"
model = "m-author"
route = "api"
endpoint = "ep"
eligible_roles = ["author"]
pool = "pa"

[[participants]]
id = "reviewer"
model = "m-reviewer"
route = "api"
endpoint = "ep"
eligible_roles = ["reviewer"]
pool = "pb"
accept_unknown_truncation = true

[[endpoints]]
name = "ep"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-author", "m-reviewer"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[pools.pa]
concurrency = 1

[pools.pb]
concurrency = 1

[policy]
local_only = true
allowed_destinations = []
`, defaultRounds, ceilingRounds)
}

func testBlockStep(blockID string) fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase == "review" {
			round := 0
			return fake.VerdictV2Block(req.Envelope.CandidateHash, req.Envelope.AttemptID, round, blockID, "high", "", "", "Issue found")
		}
		return authorOK
	})
}

func testBlockMaintainedStep(blockID string) fake.Step {
	roundCounter := 0
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase == "review" {
			if roundCounter > 0 {
				return fake.VerdictV2ApproveMaintainedBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, roundCounter)
			}
			roundCounter++
			return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, blockID, "high", "", "", "Issue found")
		}
		return authorOK
	})
}

func testRevisionStep() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase == "revision" {
			return "revised answer with changes"
		}
		if req.Envelope.Phase == "review" {
			round := 1
			return fake.VerdictV2Approve(req.Envelope.CandidateHash, req.Envelope.AttemptID, round)
		}
		return authorOK
	})
}

func testUnchangedRevisionStep(unchangedAnswer string) fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase == "revision" {
			return unchangedAnswer
		}
		if req.Envelope.Phase == "review" {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		}
		return unchangedAnswer
	})
}

func TestRevision_BlockAndRevisionApproved(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)
	s := roundSuite(t, roster, []fake.Step{
		testBlockStep("block-1"),
		testRevisionStep(),
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("after block+revision+approval: state=%s, want %s", turn.State, engine.StateApproved)
	}

	if turn.Round != 1 {
		t.Errorf("round=%d, want 1", turn.Round)
	}

	if len(turn.OpenBlocks) > 0 {
		t.Errorf("expected no open blocks after approval, got %d", len(turn.OpenBlocks))
	}
}

func TestRevision_BlockMaintainedThroughMaxRounds(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(1, 2)
	s := roundSuite(t, roster, []fake.Step{
		testBlockMaintainedStep("block-1"),
		testRevisionStep(),
		testBlockMaintainedStep("block-1"),
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("after block maintained through max_rounds: state=%s, want %s", turn.State, engine.StateIncomplete)
	}

	if turn.TerminalReason == nil || !strings.Contains(*turn.TerminalReason, "rounds_exhausted") {
		reason := "<none>"
		if turn.TerminalReason != nil {
			reason = *turn.TerminalReason
		}
		t.Errorf("terminal_reason=%s, want rounds_exhausted", reason)
	}

	if len(turn.OpenBlocks) == 0 {
		t.Errorf("expected open blocks after rounds exhausted, got %d", len(turn.OpenBlocks))
	}
}

func TestRevision_MaxRoundsZeroNoRevision(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(0, 5)
	s := roundSuite(t, roster, []fake.Step{
		testBlockStep("block-1"),
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("with max_rounds=0 and block: state=%s, want %s", turn.State, engine.StateIncomplete)
	}

	if turn.MaxRounds == nil || *turn.MaxRounds != 0 {
		maxRoundsVal := -1
		if turn.MaxRounds != nil {
			maxRoundsVal = *turn.MaxRounds
		}
		t.Errorf("max_rounds=%d, want 0", maxRoundsVal)
	}

	if len(turn.OpenBlocks) == 0 {
		t.Errorf("expected open blocks after block with no revisions allowed")
	}
}

func TestRevision_ReviewerFailureEndsRound(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)
	s := roundSuite(t, roster, []fake.Step{
		testBlockStep("block-1"),
		failStep("unreachable", transport.StatusInterrupted),
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("after reviewer failure in revision round: state=%s, want %s", turn.State, engine.StateIncomplete)
	}
}

func TestRevision_UnchangedRevisionUnchangedChanges(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)
	unchangedAnswer := "same answer"

	steps := []fake.Step{
		testBlockStep("block-1"),
		testUnchangedRevisionStep(unchangedAnswer),
	}
	s := roundSuite(t, roster, steps)

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("after unchanged revision: state=%s, want %s", turn.State, engine.StateIncomplete)
	}

	if turn.TerminalReason == nil || !strings.Contains(*turn.TerminalReason, "revision_unchanged") {
		reason := "<none>"
		if turn.TerminalReason != nil {
			reason = *turn.TerminalReason
		}
		t.Errorf("terminal_reason=%s, want revision_unchanged", reason)
	}
}

func TestRevision_UnchangedAnswerChangedChanges(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)

	steps := []fake.Step{
		testBlockStep("block-1"),
		testRevisionStep(),
	}
	s := roundSuite(t, roster, steps)

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("with unchanged answer over changed changes: state=%s, want %s", turn.State, engine.StateApproved)
	}
}

func TestRevision_ApprovalNotTransferred(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)

	roundCounter := 0
	steps := []fake.Step{
		enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			if req.Envelope.Phase == "review" {
				defer func() { roundCounter++ }()
				if roundCounter == 0 {
					return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
				}
				return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "block-1", "high", "", "", "Issue")
			}
			return authorOK
		}),
	}
	s := roundSuite(t, roster, steps)

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("after approval doesn't count in next round: state=%s, want %s", turn.State, engine.StateIncomplete)
	}

	if len(turn.OpenBlocks) == 0 {
		t.Errorf("expected open blocks after block in round 1, got %d", len(turn.OpenBlocks))
	}
}

func TestRevision_CannotWithdrawOthersBlock(t *testing.T) {
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
endpoint = "ep"
eligible_roles = ["author"]
pool = "pa"

[[participants]]
id = "reviewer1"
model = "m-reviewer"
route = "api"
endpoint = "ep"
eligible_roles = ["reviewer"]
pool = "pb"
accept_unknown_truncation = true

[[participants]]
id = "reviewer2"
model = "m-reviewer"
route = "api"
endpoint = "ep"
eligible_roles = ["reviewer"]
pool = "pb"
accept_unknown_truncation = true

[[endpoints]]
name = "ep"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["m-author", "m-reviewer"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer1", "reviewer2"]

[presets.review.revision]
max_rounds = 2
max_rounds_ceiling = 5

[pools.pa]
concurrency = 1

[pools.pb]
concurrency = 1

[policy]
local_only = true
allowed_destinations = []
`

	scripts := map[string][]fake.Step{
		"author": roundSteps(16),
		"reviewer1": append(
			[]fake.Step{testBlockStep("block-1")},
			roundSteps(16)...,
		),
		"reviewer2": roundSteps(16),
	}

	s := newSuite(t, roster, scripts)

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("state=%s, want %s", turn.State, engine.StateIncomplete)
	}

	if len(turn.OpenBlocks) == 0 {
		t.Error("no open blocks found in verdicts")
	}
}

func TestRevision_ApproveWithOwnBlockMaintainedMalformed(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)

	roundCounter := 0
	steps := []fake.Step{
		enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			if req.Envelope.Phase == "review" {
				defer func() { roundCounter++ }()
				if roundCounter > 0 {
					return fake.VerdictV2ApproveMaintainedBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, roundCounter)
				}
				return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "block-1", "high", "", "", "Issue found")
			}
			if req.Envelope.Phase == "revision" {
				return "revised"
			}
			return authorOK
		}),
	}
	s := roundSuite(t, roster, steps)

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("after malformed approve+maintained block: state=%s, want %s", turn.State, engine.StateIncomplete)
	}
}

func TestRevision_OmittedBlockStaysOpen(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)

	roundCounter := 0
	steps := []fake.Step{
		enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
			if req.Envelope.Phase == "review" {
				defer func() { roundCounter++ }()
				if roundCounter > 0 {
					return fmt.Sprintf("VERDICT v2\nround: %d\napprove\n", roundCounter)
				}
				return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "block-1", "high", "", "", "Issue found")
			}
			if req.Envelope.Phase == "revision" {
				return "revised"
			}
			return authorOK
		}),
	}
	s := roundSuite(t, roster, steps)

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("with omitted block: state=%s, want %s", turn.State, engine.StateIncomplete)
	}

	if len(turn.OpenBlocks) == 0 {
		t.Errorf("expected open blocks when omitted from verdict, got %d", len(turn.OpenBlocks))
	}
}

func TestRevision_ReviewerNoLimitOrOthersBlocks(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)

	steps := []fake.Step{
		testBlockStep("block-1"),
		testRevisionStep(),
	}
	s := roundSuite(t, roster, steps)

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("after revision round: state=%s, want %s", turn.State, engine.StateApproved)
	}
}

func TestRevision_RoundExceedsBudgetNotStarted(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)

	steps := []fake.Step{
		testBlockStep("block-1"),
	}
	s := roundSuite(t, roster, steps)

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("state=%s, want %s", turn.State, engine.StateIncomplete)
	}

	if len(turn.OpenBlocks) == 0 {
		t.Errorf("expected open blocks after block")
	}
}

func TestRevision_LateResultQuarantined(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)

	steps := []fake.Step{
		testBlockStep("block-1"),
		testRevisionStep(),
	}
	s := roundSuite(t, roster, steps)

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("after revision round: state=%s, want %s", turn.State, engine.StateApproved)
	}
}

func TestRevision_MaxRoundsOutOfRange(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)
	s := roundSuite(t, roster, []fake.Step{})

	c := s.Dial(t)

	maxRounds := 10
	req := wire.SubmitRequest{
		SubmissionID: "submit-1",
		Input:        "write code",
		MaxRounds:    &maxRounds,
		Workspace:    "/tmp",
	}
	_, err := c.Submit(s.ctx(), req)
	if err == nil {
		t.Fatal("submit with max_rounds=10 above ceiling=5 should fail")
	}

	maxRoundsNeg := -1
	req.SubmissionID = "submit-2"
	req.MaxRounds = &maxRoundsNeg
	_, err = c.Submit(s.ctx(), req)
	if err == nil {
		t.Fatal("submit with max_rounds=-1 should fail")
	}
}

func TestRevision_RosterMaxRoundsValidation(t *testing.T) {
	t.Parallel()

	invalidRoster := rosterWithMaxRounds(5, 2)
	h := enginetest.New(t, invalidRoster, map[string][]fake.Step{
		"author":   roundSteps(2),
		"reviewer": roundSteps(2),
	}, enginetest.NoStart())

	err := h.Engine.Start()
	if err == nil {
		t.Error("roster with default=5 > ceiling=2 should fail to load")
	}

	invalidRoster2 := rosterWithMaxRounds(2, 6)
	h2 := enginetest.New(t, invalidRoster2, map[string][]fake.Step{
		"author":   roundSteps(2),
		"reviewer": roundSteps(2),
	}, enginetest.NoStart())

	err = h2.Engine.Start()
	if err == nil {
		t.Error("roster with ceiling=6 > 5 should fail to load")
	}
}

func TestRevision_StatusReportsDefaults(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 4)
	s := roundSuite(t, roster, []fake.Step{})

	c := s.Dial(t)

	status := s.Status(t, c)
	if status.Revision.Default != 2 {
		t.Errorf("revision.default=%d, want 2", status.Revision.Default)
	}
	if status.Revision.Ceiling != 4 {
		t.Errorf("revision.ceiling=%d, want 4", status.Revision.Ceiling)
	}
}

func TestRevision_SubmitOverridesDefault(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)
	s := roundSuite(t, roster, []fake.Step{})

	c := s.Dial(t)

	maxRounds := 3
	req := wire.SubmitRequest{
		SubmissionID: "submit-1",
		Input:        "write code",
		MaxRounds:    &maxRounds,
		Workspace:    "/tmp",
	}
	reply, err := c.Submit(s.ctx(), req)
	if err != nil {
		t.Fatalf("submit with max_rounds=3: %v", err)
	}

	turn := s.Turn(t, c, reply.TurnID)
	if turn.MaxRounds == nil || *turn.MaxRounds != 3 {
		maxRoundsVal := -1
		if turn.MaxRounds != nil {
			maxRoundsVal = *turn.MaxRounds
		}
		t.Errorf("max_rounds=%d, want 3", maxRoundsVal)
	}
}

func TestRevision_RoundsZeroFlag(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)

	steps := []fake.Step{
		testBlockStep("block-1"),
	}
	s := roundSuite(t, roster, steps)

	c := s.Dial(t)

	maxRounds := 0
	req := wire.SubmitRequest{
		SubmissionID: "submit-1",
		Input:        "write code",
		MaxRounds:    &maxRounds,
		Workspace:    "/tmp",
	}
	reply, err := c.Submit(s.ctx(), req)
	if err != nil {
		t.Fatalf("submit with max_rounds=0: %v", err)
	}
	s.WaitTerminal(reply.TurnID)

	turn := s.Turn(t, c, reply.TurnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("state=%s, want %s", turn.State, engine.StateIncomplete)
	}

	if turn.MaxRounds == nil || *turn.MaxRounds != 0 {
		maxRoundsVal := -1
		if turn.MaxRounds != nil {
			maxRoundsVal = *turn.MaxRounds
		}
		t.Errorf("max_rounds=%d, want 0", maxRoundsVal)
	}
}

func TestRevision_TurnRecordCarriesLimits(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)
	s := roundSuite(t, roster, []fake.Step{})

	c := s.Dial(t)

	req := wire.SubmitRequest{
		SubmissionID: "submit-1",
		Input:        "write code",
		Workspace:    "/tmp",
	}
	reply, err := c.Submit(s.ctx(), req)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	turn := s.Turn(t, c, reply.TurnID)
	if turn.MaxRounds == nil || *turn.MaxRounds != 2 {
		maxRoundsVal := -1
		if turn.MaxRounds != nil {
			maxRoundsVal = *turn.MaxRounds
		}
		t.Errorf("max_rounds=%d, want 2", maxRoundsVal)
	}
}

func TestRevision_BlockCitesOnlySpecificResults(t *testing.T) {
	t.Parallel()
	roster := rosterWithMaxRounds(2, 5)
	s := roundSuite(t, roster, []fake.Step{
		testBlockStep("block-1"),
		testRevisionStep(),
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "submit-1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("after revision with approval: state=%s, want %s", turn.State, engine.StateApproved)
	}
}
