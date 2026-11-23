package acceptance

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/tui"
	"github.com/boldfield/syndic/internal/wire"
)

const revisionPara = "Revision"

func init() {
	cover(revisionPara, "one block followed by a revised candidate and two approvals yields `approved` with round count 1", "TestRevision_BlockThenRevisedApproved")
	cover(revisionPara, "a block maintained through `max_rounds` yields `rounds_exhausted` with the block open", "TestRevision_BlockMaintainedExhaustsRounds")
	cover(revisionPara, "`max_rounds = 0` reproduces every alpha result", "TestRevision_ZeroRoundsIsAlpha")
	cover(revisionPara, "a reviewer failure in any round yields `incomplete` and no further author dispatch", "TestRevision_ReviewerFailureAnyRound")
	cover(revisionPara, "an identical answer over an identical change set yields `revision_unchanged`", "TestRevision_UnchangedRevision")
	cover(revisionPara, "an identical answer over a changed change set does not", "TestRevision_ChangedChangeSetIsAChange")
	cover(revisionPara, "an approval of round 0 does not count for round 1", "TestRevision_EarlierApprovalDoesNotCarry")
	cover(revisionPara, "a reviewer cannot withdraw another reviewer's block", "TestRevision_CannotWithdrawOthersBlock")
	cover(revisionPara, "`approve` with an own block maintained is malformed", "TestRevision_ApproveWithOwnBlockMaintainedMalformed")
	cover(revisionPara, "a `block` verdict that omits one of the reviewer's own open blocks is malformed and the omitted block stays open", "TestRevision_OmittedBlockStateMalformed")
	cover(revisionPara, "no reviewer request contains the round limit or another reviewer's block", "TestRevision_ReviewerRequestsBlind")
	cover(revisionPara, "a round that would exceed the turn budget is not started", "TestRevision_RoundOverBudgetNotStarted")
	cover(revisionPara, "late results from an earlier round are quarantined", "TestRevision_LateEarlierRoundQuarantined")
	cover(revisionPara, "`submit` with `max_rounds` above the preset's ceiling or below 0 creates no turn", "TestRevision_SubmitMaxRoundsOutOfRange")
	cover(revisionPara, "a roster whose default exceeds its ceiling, or whose ceiling exceeds 5, is refused at load", "TestRevision_RosterLimitsRefusedAtLoad")
	cover(revisionPara, "`status` reports the default and the ceiling in force", "TestRevision_StatusReportsLimits")
	cover(revisionPara, "`max_rounds` on a submit overrides the roster in both directions within the ceiling", "TestRevision_SubmitOverridesBothDirections")
	cover(revisionPara, "`--rounds 0` yields no revision on a roster whose default is 2", "TestRevision_RoundsZeroFlagNoRevision")
	cover(revisionPara, "the turn record carries the limit and the ceiling in force", "TestRevision_TurnRecordCarriesLimits")
	cover(revisionPara, "a revision request carries the tool results a block cites and no other reviewer result", "TestRevision_RevisionCarriesCitedResultsOnly")
}

const (
	revAnswer  = "the answer is 41"
	revRevised = "the answer is 42, and here is why"
)

// revRoster is the review roster with the round limit and ceiling given, and
// the required reviewers named (one "reviewer" when none are), each in a pool
// wide enough to review at once.
func revRoster(rounds, ceiling int, reviewers ...string) string {
	toml := strings.Replace(enginetest.RosterTOML, "max_rounds = 0",
		fmt.Sprintf("max_rounds = %d\nmax_rounds_ceiling = %d", rounds, ceiling), 1)
	if len(reviewers) == 0 {
		return toml
	}
	start := strings.Index(toml, "[[participants]]\nid = \"reviewer\"")
	toml = toml[:start] + toml[strings.Index(toml, "[[endpoints]]"):]
	var extra, quoted []string
	for _, id := range reviewers {
		extra = append(extra, "[[participants]]\nid = \""+id+"\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \"reviewers\"\naccept_unknown_truncation = true\n")
		quoted = append(quoted, `"`+id+`"`)
	}
	toml = strings.Replace(toml, "[[endpoints]]", strings.Join(extra, "\n")+"\n[[endpoints]]", 1)
	toml = strings.Replace(toml, `required_reviewers = ["reviewer"]`, "required_reviewers = ["+strings.Join(quoted, ", ")+"]", 1)
	return toml + "\n[pools.reviewers]\nconcurrency = 10\n"
}

// revAcks are the replies to the terminal deliveries that follow a turn.
func revAcks(steps ...fake.Step) []fake.Step {
	return append(steps, enginetest.Reply("ok"), enginetest.Reply("ok"), enginetest.Reply("ok"))
}

// revAuthor answers each author dispatch in order with the texts, then
// acknowledges deliveries.
func revAuthor(texts ...string) []fake.Step {
	steps := make([]fake.Step, len(texts))
	for i, text := range texts {
		steps[i] = enginetest.Reply(text)
	}
	return revAcks(steps...)
}

// revApprove0 and revBlock0 are first-round verdicts (VERDICT v1), built from
// the request so they echo the candidate and attempt the engine sent.
func revApprove0() fake.Step { return verdict(fake.VerdictApprove) }

func revBlock0(blockID, evidence string) fake.Step {
	return verdict(func(hash, attempt string) string {
		return fake.VerdictBlock(hash, attempt, blockID, "high", evidence, "fix it", "wrong")
	})
}

// revV2 is a later-round verdict (VERDICT v2) of the decision for the round,
// with the block-state lines given. A block opens blockID with evidence.
func revV2(round int, decision, blockID, evidence string, states ...string) fake.Step {
	return verdict(func(hash, attempt string) string {
		lines := []string{"VERDICT v2", "candidate: " + hash, "attempt: " + attempt, "round: " + strconv.Itoa(round)}
		for _, s := range states {
			lines = append(lines, "block-state: "+s)
		}
		lines = append(lines, "verdict: "+decision)
		if decision == "block" {
			lines = append(lines, "severity: high", "block_id: "+blockID, "evidence: "+evidence, "remedy: fix it")
		}
		return strings.Join(append(lines, "reason: r", "END VERDICT"), "\n")
	})
}

// revMalformed has no envelope at all.
func revMalformed() fake.Step { return enginetest.Reply("I have looked at it and have thoughts.") }

func revIntPtr(n int) *int { return &n }

// revSubmit submits a review turn with the round limit, waits for it to end
// and returns its record.
func revSubmit(t *testing.T, s *suite, c *client.Client, id string, maxRounds *int) (string, wire.TurnReply) {
	t.Helper()
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: id, Input: "what is the answer?", MaxRounds: maxRounds})
	if err != nil {
		t.Fatalf("submit %s: %v", id, err)
	}
	s.WaitTerminal(reply.TurnID)
	return reply.TurnID, s.Turn(t, c, reply.TurnID)
}

// revStates are the turn_state values the turn announced, in order.
func (s *suite) revStates(turnID string) []string {
	var out []string
	for _, ev := range s.Events() {
		if st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState); ok && st.TurnID == turnID {
			out = append(out, st.State)
		}
	}
	return out
}

// revRounds counts the revision rounds the turn started.
func (s *suite) revRounds(turnID string) int {
	n := 0
	for _, st := range s.revStates(turnID) {
		if st == engine.StateRevisionDispatched {
			n++
		}
	}
	return n
}

// revOwnOpen returns the reviewer-assigned ids of the participant's open blocks.
func (s *suite) revOwnOpen(t *testing.T, turnID, participant string) []string {
	t.Helper()
	var ids []string
	err := s.Store.Tx(func(tx *journal.Tx) error {
		blocks, err := tx.ReviewerOpenBlocks(turnID, participant)
		for _, b := range blocks {
			ids = append(ids, b.ReviewerBlockID)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// revBlocks returns every block of the turn by its reviewer-assigned id.
func (s *suite) revBlocks(t *testing.T, turnID string) map[string]journal.BlockState {
	t.Helper()
	blocks, err := s.Store.AllBlocksForTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]journal.BlockState{}
	for _, b := range blocks {
		if b.ReviewerBlockID == nil {
			t.Fatalf("block %s has no reviewer id", b.ID)
		}
		out[*b.ReviewerBlockID] = b
	}
	return out
}

// revWantBlocks requires the turn's blocks to be exactly those named, each in
// its state.
func (s *suite) revWantBlocks(t *testing.T, turnID string, want map[string]string) map[string]journal.BlockState {
	t.Helper()
	got := s.revBlocks(t, turnID)
	if len(got) != len(want) {
		t.Fatalf("blocks = %+v, want %v", got, want)
	}
	for id, state := range want {
		if b, ok := got[id]; !ok || b.State != state {
			t.Fatalf("block %s = %+v, want %s (all: %+v)", id, b, state, got)
		}
	}
	return got
}

// revValidations are the validation statuses of the participant's verdicts,
// in the order of its review attempts.
func (s *suite) revValidations(t *testing.T, turnID, participant string) []string {
	t.Helper()
	byAttempt := map[string]string{}
	for _, v := range s.verdictRows(t, turnID) {
		byAttempt[v.AttemptID] = v.ValidationStatus
	}
	var out []string
	for _, a := range s.attemptRows(t, turnID, participant, "review") {
		if st, ok := byAttempt[a.ID]; ok {
			out = append(out, st)
		}
	}
	return out
}

func revWantCalls(t *testing.T, s *suite, participant, phase string, n int) []transport.CompleteRequest {
	t.Helper()
	calls := s.calls(participant, phase)
	if len(calls) != n {
		t.Fatalf("%s received %d %s requests, want %d", participant, len(calls), phase, n)
	}
	return calls
}

func revText(req transport.CompleteRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// revFeedback is the user message of an author revision request that carries
// the reviewers' outputs.
func revFeedback(t *testing.T, req transport.CompleteRequest) string {
	t.Helper()
	for _, m := range req.Messages {
		if m.Role == "user" && strings.Contains(m.Content, "reviewer 1") {
			return m.Content
		}
	}
	t.Fatalf("revision request has no feedback message: %+v", req.Messages)
	return ""
}

func TestRevision_BlockThenRevisedApproved(t *testing.T) {
	t.Parallel()
	s := newSuite(t, revRoster(2, 5, "r1", "r2"), map[string][]fake.Step{
		"author": revAuthor(revAnswer, revRevised),
		"r1":     revAcks(revBlock0("b1", "the sum is off by one"), revV2(1, "approve", "", "", "b1 withdrawn")),
		"r2":     revAcks(revApprove0(), revV2(1, "approve", "", "")),
	})
	c := s.Dial(t)
	turnID, turn := revSubmit(t, s, c, "s1", nil)

	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if turn.Round != 1 {
		t.Fatalf("round count = %d, want 1", turn.Round)
	}
	want := []string{engine.StateSubmitted, engine.StateAuthorDispatched, engine.StateCandidateFrozen, engine.StateReviewersDispatched,
		engine.StateRevisionDispatched, engine.StateCandidateFrozen, engine.StateReviewersDispatched, engine.StateApproved}
	if got := s.revStates(turnID); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("turn states = %v, want %v", got, want)
	}
	authors := revWantCalls(t, s, "author", "author", 2)
	if fb := revFeedback(t, authors[1]); !strings.Contains(fb, "the sum is off by one") || !strings.Contains(fb, "reviewer 2") {
		t.Fatalf("revision feedback lacks both reviewers' outputs: %q", fb)
	}

	// Both reviewers reviewed the revised candidate, and the turn's candidate
	// is that one.
	r1, r2 := revWantCalls(t, s, "r1", "review", 2), revWantCalls(t, s, "r2", "review", 2)
	revised := r1[1].Envelope.CandidateHash
	if revised == r1[0].Envelope.CandidateHash || r2[1].Envelope.CandidateHash != revised {
		t.Fatalf("round 1 reviews are of %s and %s, round 0 of %s", revised, r2[1].Envelope.CandidateHash, r1[0].Envelope.CandidateHash)
	}
	if turn.CandidateHash == nil || *turn.CandidateHash != revised {
		t.Fatalf("turn candidate = %v, want the revised %s", turn.CandidateHash, revised)
	}
	b1 := s.revWantBlocks(t, turnID, map[string]string{"b1": "withdrawn"})["b1"]
	if b1.WithdrawnAgainstHash == nil || *b1.WithdrawnAgainstHash != revised {
		t.Fatalf("b1 withdrawn against %v, want the revised candidate %s", b1.WithdrawnAgainstHash, revised)
	}
	for _, b := range turn.OpenBlocks {
		if b.State == "open" {
			t.Fatalf("turn reports an open block: %+v", turn.OpenBlocks)
		}
	}
}

func TestRevision_BlockMaintainedExhaustsRounds(t *testing.T) {
	t.Parallel()
	s := newSuite(t, revRoster(2, 5), map[string][]fake.Step{
		"author": revAuthor(revAnswer, revRevised, revRevised+" (again)"),
		"reviewer": revAcks(
			revBlock0("b1", "e1"),
			revV2(1, "block", "b2", "e2", "b1 maintained"),
			revV2(2, "block", "b3", "e3", "b1 maintained", "b2 maintained"),
		),
	})
	c := s.Dial(t)
	turnID, turn := revSubmit(t, s, c, "s1", nil)

	wantTurn(t, turn, engine.StateIncomplete, "rounds_exhausted")
	if turn.Round != 2 || s.revRounds(turnID) != 2 {
		t.Fatalf("round count = %d with %d rounds started, want 2", turn.Round, s.revRounds(turnID))
	}
	revWantCalls(t, s, "author", "author", 3)
	revWantCalls(t, s, "reviewer", "review", 3)
	s.revWantBlocks(t, turnID, map[string]string{"b1": "open", "b2": "open", "b3": "open"})
	if got := s.revOwnOpen(t, turnID, "reviewer"); strings.Join(got, ",") != "b1,b2,b3" {
		t.Fatalf("reviewer's open blocks = %v", got)
	}
}

func TestRevision_ZeroRoundsIsAlpha(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		reviewer []fake.Step
		state    string
		reason   *regexp.Regexp
	}{
		{"approve", []fake.Step{revApprove0()}, engine.StateApproved, regexp.MustCompile(`^all_approved$`)},
		{"block", []fake.Step{revBlock0("b1", "e")}, engine.StateIncomplete, regexp.MustCompile(`^block:.+`)},
		{"approve with changes", []fake.Step{verdict(fake.VerdictApproveWithChanges)}, engine.StateIncomplete, regexp.MustCompile(`^revision_request:reviewer$`)},
		{"malformed twice", []fake.Step{revMalformed(), revMalformed()}, engine.StateIncomplete, regexp.MustCompile(`^malformed_twice:reviewer$`)},
		{"wrong hash", []fake.Step{verdict(fake.VerdictWrongHash)}, engine.StateIncomplete, regexp.MustCompile(`^reviewer_failed:reviewer:rejected$`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSuite(t, revRoster(0, 5), map[string][]fake.Step{
				"author":   revAuthor(revAnswer),
				"reviewer": revAcks(tc.reviewer...),
			})
			c := s.Dial(t)
			turnID, turn := revSubmit(t, s, c, "s1", nil)
			if turn.State != tc.state || turn.TerminalReason == nil || !tc.reason.MatchString(*turn.TerminalReason) {
				t.Fatalf("turn ended %q %v, want %q matching %s", turn.State, turn.TerminalReason, tc.state, tc.reason)
			}
			if turn.Round != 0 || s.revRounds(turnID) != 0 {
				t.Fatalf("round %d with %d revisions, want none", turn.Round, s.revRounds(turnID))
			}
			revWantCalls(t, s, "author", "author", 1)
			if tc.name == "block" {
				if b := s.revWantBlocks(t, turnID, map[string]string{"b1": "open"})["b1"]; *turn.TerminalReason != "block:"+b.ID {
					t.Fatalf("reason %q does not name block %s", *turn.TerminalReason, b.ID)
				}
			}
		})
	}
}

func TestRevision_ReviewerFailureAnyRound(t *testing.T) {
	t.Parallel()
	failures := []struct {
		name   string
		steps  []fake.Step
		reason string
	}{
		{"transport", []fake.Step{{Kind: fake.StepKindReply, Status: transport.StatusError, TransportStatus: "http_500"}}, "reviewer_failed:r2:http_500"},
		{"gate", []fake.Step{{Kind: fake.StepKindReply, Text: "x", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}}, "reviewer_failed:r2:truncated"},
		{"second malformed", []fake.Step{revMalformed(), revMalformed()}, "malformed_twice:r2"},
		{"deadline", []fake.Step{{Kind: fake.StepKindHang}}, "reviewer_failed:r2:timeout"},
	}
	for round := 0; round <= 1; round++ {
		for _, f := range failures {
			t.Run(fmt.Sprintf("round %d %s", round, f.name), func(t *testing.T) {
				t.Parallel()
				// r1 blocks the first candidate and approves the revision; r2
				// fails in the round under test, having approved before it.
				r2 := append([]fake.Step(nil), f.steps...)
				if round == 1 {
					r2 = append([]fake.Step{revApprove0()}, r2...)
				}
				s := newSuite(t, revRoster(2, 5, "r1", "r2"), map[string][]fake.Step{
					"author": revAuthor(revAnswer, revRevised, revRevised+" again"),
					"r1":     revAcks(revBlock0("b1", "e"), revV2(1, "approve", "", "", "b1 withdrawn")),
					"r2":     revAcks(r2...),
				})
				c := s.Dial(t)
				reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
				if err != nil {
					t.Fatal(err)
				}
				reason := f.reason
				if f.name == "deadline" {
					seen := 0
					s.qdcBegunBy("r2", "review", func(wire.EvAttemptBegun) bool { seen++; return seen == round+1 })
					s.Clock.Advance(301 * time.Second)
				}
				s.WaitTerminal(reply.TurnID)
				wantTurn(t, s.Turn(t, c, reply.TurnID), engine.StateIncomplete, reason)
				if n := s.revRounds(reply.TurnID); n != round {
					t.Fatalf("%d rounds started, want %d", n, round)
				}
				// No author dispatch follows the failure.
				revWantCalls(t, s, "author", "author", round+1)
			})
		}
	}
}

// revChangeSuite runs one block, then a revision whose answer and change set
// are given against the first candidate's.
func revChangeSuite(t *testing.T, revised string, first, second []string) (*suite, string, wire.TurnReply) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	lines := enginetest.WithAuthorChangeLines(func(transport.CompleteResult) []string {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return first
		}
		return second
	})
	s := newSuite(t, revRoster(2, 5), map[string][]fake.Step{
		"author":   revAuthor(revAnswer, revised),
		"reviewer": revAcks(revBlock0("b1", "e"), revV2(1, "approve", "", "", "b1 withdrawn")),
	}, lines)
	c := s.Dial(t)
	turnID, turn := revSubmit(t, s, c, "s1", nil)
	return s, turnID, turn
}

func TestRevision_UnchangedRevision(t *testing.T) {
	t.Parallel()
	patch := []string{"a.txt write " + strings.Repeat("0", 64) + " " + strings.Repeat("1", 64) + " 5"}
	for name, lines := range map[string][]string{"with a change set": patch, "without one": nil} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, turnID, turn := revChangeSuite(t, revAnswer, lines, append([]string(nil), lines...))
			wantTurn(t, turn, engine.StateIncomplete, "revision_unchanged")
			// The human decides: the block is open and the unchanged revision
			// was never reviewed.
			s.revWantBlocks(t, turnID, map[string]string{"b1": "open"})
			revWantCalls(t, s, "reviewer", "review", 1)
			revWantCalls(t, s, "author", "author", 2)
			if turn.Round != 0 {
				t.Fatalf("round = %d, want 0: no revised candidate was frozen", turn.Round)
			}
		})
	}
}

func TestRevision_ChangedChangeSetIsAChange(t *testing.T) {
	t.Parallel()
	zero, one, two := strings.Repeat("0", 64), strings.Repeat("1", 64), strings.Repeat("2", 64)
	cases := map[string]struct {
		revised       string
		first, second []string
	}{
		"same answer, corrected patch": {revAnswer, []string{"a.txt write " + zero + " " + one + " 5"}, []string{"a.txt write " + zero + " " + two + " 5"}},
		"same answer, patch added":     {revAnswer, nil, []string{"a.txt write " + zero + " " + one + " 5"}},
		"changed answer, same patch":   {revRevised, []string{"a.txt write " + zero + " " + one + " 5"}, []string{"a.txt write " + zero + " " + one + " 5"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, turnID, turn := revChangeSuite(t, tc.revised, tc.first, tc.second)
			wantTurn(t, turn, engine.StateApproved, "all_approved")
			if turn.Round != 1 {
				t.Fatalf("round = %d, want 1", turn.Round)
			}
			reviews := revWantCalls(t, s, "reviewer", "review", 2)
			if reviews[0].Envelope.CandidateHash == reviews[1].Envelope.CandidateHash {
				t.Fatal("the revision was reviewed under the first candidate's hash")
			}
			s.revWantBlocks(t, turnID, map[string]string{"b1": "withdrawn"})
		})
	}
}

func TestRevision_EarlierApprovalDoesNotCarry(t *testing.T) {
	t.Parallel()
	// r1 approves the first candidate and blocks the revision; r2 blocks the
	// first and withdraws on the revision. Had r1's approval carried, the
	// revision would be approved.
	s := newSuite(t, revRoster(1, 5, "r1", "r2"), map[string][]fake.Step{
		"author": revAuthor(revAnswer, revRevised),
		"r1":     revAcks(revApprove0(), revV2(1, "block", "late", "e1")),
		"r2":     revAcks(revBlock0("b1", "e2"), revV2(1, "approve", "", "", "b1 withdrawn")),
	})
	c := s.Dial(t)
	turnID, turn := revSubmit(t, s, c, "s1", nil)

	wantTurn(t, turn, engine.StateIncomplete, "rounds_exhausted")
	r1 := revWantCalls(t, s, "r1", "review", 2)
	if r1[0].Envelope.CandidateHash == r1[1].Envelope.CandidateHash {
		t.Fatal("r1 was not asked to review the revised candidate")
	}
	s.revWantBlocks(t, turnID, map[string]string{"b1": "withdrawn", "late": "open"})
	// r1's round-0 approval is recorded against the first candidate only.
	for _, v := range s.verdictRows(t, turnID) {
		if v.AttemptID == s.attemptRows(t, turnID, "r1", "review")[0].ID && v.CandidateHash != r1[0].Envelope.CandidateHash {
			t.Fatalf("r1's first approval is recorded against %s", v.CandidateHash)
		}
	}
}

func TestRevision_CannotWithdrawOthersBlock(t *testing.T) {
	t.Parallel()
	// r2 opened nothing; its round-1 verdict claims to withdraw r1's b1. The
	// claim is malformed, the repair approves, and b1 stays open because r1
	// maintains it.
	s := newSuite(t, revRoster(1, 5, "r1", "r2"), map[string][]fake.Step{
		"author": revAuthor(revAnswer, revRevised),
		"r1":     revAcks(revBlock0("b1", "e1"), revV2(1, "block", "b2", "e2", "b1 maintained")),
		"r2":     revAcks(revApprove0(), revV2(1, "approve", "", "", "b1 withdrawn"), revV2(1, "approve", "", "")),
	})
	c := s.Dial(t)
	turnID, turn := revSubmit(t, s, c, "s1", nil)

	wantTurn(t, turn, engine.StateIncomplete, "rounds_exhausted")
	if got := s.revValidations(t, turnID, "r2"); strings.Join(got, ",") != "valid,malformed,valid" {
		t.Fatalf("r2's verdicts = %v, want valid, malformed, valid", got)
	}
	blocks := s.revWantBlocks(t, turnID, map[string]string{"b1": "open", "b2": "open"})
	if blocks["b1"].WithdrawnAgainstHash != nil {
		t.Fatalf("b1 records a withdrawal: %+v", blocks["b1"])
	}
	if got := s.revOwnOpen(t, turnID, "r1"); strings.Join(got, ",") != "b1,b2" {
		t.Fatalf("r1's open blocks = %v", got)
	}
}

func TestRevision_ApproveWithOwnBlockMaintainedMalformed(t *testing.T) {
	t.Parallel()
	t.Run("repaired", func(t *testing.T) {
		t.Parallel()
		s := newSuite(t, revRoster(2, 5), map[string][]fake.Step{
			"author":   revAuthor(revAnswer, revRevised),
			"reviewer": revAcks(revBlock0("b1", "e"), revV2(1, "approve", "", "", "b1 maintained"), revV2(1, "approve", "", "", "b1 withdrawn")),
		})
		c := s.Dial(t)
		turnID, turn := revSubmit(t, s, c, "s1", nil)
		wantTurn(t, turn, engine.StateApproved, "all_approved")
		if got := s.revValidations(t, turnID, "reviewer"); strings.Join(got, ",") != "valid,malformed,valid" {
			t.Fatalf("verdicts = %v, want valid, malformed, valid", got)
		}
		rows := s.attemptRows(t, turnID, "reviewer", "review")
		if rows[2].RepairOf == nil || *rows[2].RepairOf != rows[1].ID {
			t.Fatalf("third review attempt is not the repair of the malformed one: %+v", rows[2])
		}
		s.revWantBlocks(t, turnID, map[string]string{"b1": "withdrawn"})
	})
	t.Run("repeated", func(t *testing.T) {
		t.Parallel()
		s := newSuite(t, revRoster(2, 5), map[string][]fake.Step{
			"author":   revAuthor(revAnswer, revRevised),
			"reviewer": revAcks(revBlock0("b1", "e"), revV2(1, "approve", "", "", "b1 maintained"), revV2(1, "approve", "", "", "b1 maintained")),
		})
		c := s.Dial(t)
		turnID, turn := revSubmit(t, s, c, "s1", nil)
		wantTurn(t, turn, engine.StateIncomplete, "malformed_twice:reviewer")
		s.revWantBlocks(t, turnID, map[string]string{"b1": "open"})
		revWantCalls(t, s, "author", "author", 2)
	})
}

func TestRevision_OmittedBlockStateMalformed(t *testing.T) {
	t.Parallel()
	t.Run("repaired", func(t *testing.T) {
		t.Parallel()
		// The block of round 1 says nothing of b1; its repair maintains b1.
		s := newSuite(t, revRoster(1, 5), map[string][]fake.Step{
			"author":   revAuthor(revAnswer, revRevised),
			"reviewer": revAcks(revBlock0("b1", "e1"), revV2(1, "block", "b2", "e2"), revV2(1, "block", "b2", "e2", "b1 maintained")),
		})
		c := s.Dial(t)
		turnID, turn := revSubmit(t, s, c, "s1", nil)
		wantTurn(t, turn, engine.StateIncomplete, "rounds_exhausted")
		if got := s.revValidations(t, turnID, "reviewer"); strings.Join(got, ",") != "valid,malformed,valid" {
			t.Fatalf("verdicts = %v, want valid, malformed, valid", got)
		}
		s.revWantBlocks(t, turnID, map[string]string{"b1": "open", "b2": "open"})
	})
	t.Run("repeated", func(t *testing.T) {
		t.Parallel()
		// Omitted twice: the turn ends and b1, never mentioned, is still open;
		// the malformed blocks opened nothing.
		s := newSuite(t, revRoster(1, 5), map[string][]fake.Step{
			"author":   revAuthor(revAnswer, revRevised),
			"reviewer": revAcks(revBlock0("b1", "e1"), revV2(1, "block", "b2", "e2"), revV2(1, "block", "b2", "e2")),
		})
		c := s.Dial(t)
		turnID, turn := revSubmit(t, s, c, "s1", nil)
		wantTurn(t, turn, engine.StateIncomplete, "malformed_twice:reviewer")
		blocks := s.revWantBlocks(t, turnID, map[string]string{"b1": "open"})
		if blocks["b1"].WithdrawnAgainstHash != nil {
			t.Fatalf("b1 records a withdrawal: %+v", blocks["b1"])
		}
	})
}

func TestRevision_ReviewerRequestsBlind(t *testing.T) {
	t.Parallel()
	// A limit of 4 leaves 3 rounds after the first revision: neither number
	// may reach a reviewer, nor any wording of a limit.
	s := newSuite(t, revRoster(4, 5, "r1", "r2"), map[string][]fake.Step{
		"author": revAuthor(revAnswer, revRevised),
		"r1":     revAcks(revBlock0("b1", "EVIDENCE-OF-R1"), revV2(1, "approve", "", "", "b1 withdrawn")),
		"r2":     revAcks(revBlock0("b1", "EVIDENCE-OF-R2"), revV2(1, "approve", "", "", "b1 withdrawn")),
	})
	c := s.Dial(t)
	_, turn := revSubmit(t, s, c, "s1", nil)
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	limit := regexp.MustCompile(`(?i)\b(3|4|three|four)\b|max_rounds|rounds? (remain|left)|remaining|ceiling|out of rounds|last round|final round`)
	own := map[string]string{"r1": "EVIDENCE-OF-R1", "r2": "EVIDENCE-OF-R2"}
	other := map[string]string{"r1": "EVIDENCE-OF-R2", "r2": "EVIDENCE-OF-R1"}
	for _, id := range []string{"r1", "r2"} {
		reviews := revWantCalls(t, s, id, "review", 2)
		for i, req := range reviews {
			text := revText(req)
			if m := limit.FindString(text); m != "" {
				t.Errorf("%s's round %d request mentions %q: %q", id, i, m, text)
			}
			if strings.Contains(text, other[id]) {
				t.Errorf("%s's round %d request carries the other reviewer's block", id, i)
			}
			if strings.Contains(text, revAnswer) && i == 1 {
				t.Errorf("%s's round 1 request carries the previous candidate", id)
			}
		}
		if !strings.Contains(revText(reviews[1]), own[id]) {
			t.Errorf("%s's round 1 request lacks its own open block", id)
		}
	}
}

func TestRevision_RoundOverBudgetNotStarted(t *testing.T) {
	t.Parallel()
	// The first round reports far more usage than projected, so what is left
	// of a budget the first round just fits cannot cover a second.
	heavy := func(step fake.Step) fake.Step {
		step.Usage = &transport.Usage{Prompt: 10000, Completion: 100}
		return step
	}
	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{
			"author":   revAuthor(revAnswer, revRevised),
			"reviewer": revAcks(heavy(revBlock0("b1", "e")), revV2(1, "approve", "", "", "b1 withdrawn")),
		}
	}
	budget := func(limit int64) string {
		return revRoster(2, 5) + fmt.Sprintf("\n[budgets]\nturn_max_estimated_tokens = %d\n", limit)
	}

	// A generous budget starts the round, and reports the first round's projection.
	roomy := newSuite(t, budget(1000000), scripts())
	_, generous := revSubmit(t, roomy, roomy.Dial(t), "s1", nil)
	wantTurn(t, generous, engine.StateApproved, "all_approved")
	if generous.Budget.Projected == nil {
		t.Fatalf("budget = %+v, want a projection", generous.Budget)
	}

	s := newSuite(t, budget(*generous.Budget.Projected), scripts())
	c := s.Dial(t)
	turnID, turn := revSubmit(t, s, c, "s1", nil)
	wantTurn(t, turn, engine.StateIncomplete, "budget:turn")
	if n := s.revRounds(turnID); n != 0 {
		t.Fatalf("%d rounds started over the budget", n)
	}
	revWantCalls(t, s, "author", "author", 1)
	revWantCalls(t, s, "reviewer", "review", 1)
	s.revWantBlocks(t, turnID, map[string]string{"b1": "open"})
}

func TestRevision_LateEarlierRoundQuarantined(t *testing.T) {
	t.Parallel()
	// The engine waits for every required reviewer before it revises, so a
	// round-0 result can only arrive in round 1 from a request the engine no
	// longer awaits. While the revised candidate is under review, the test
	// plants such a request with round 0's epoch and lands its result through
	// the acceptance check every result passes.
	var s *suite
	var mu sync.Mutex
	var epoch0, epoch1 int
	var lateID, plantErr string
	var accepted bool
	var verdictsAtLanding int
	plant := func(req transport.CompleteRequest) {
		mu.Lock()
		defer mu.Unlock()
		epoch1 = req.Envelope.Epoch
		err := s.Store.Tx(func(tx *journal.Tx) error {
			var err error
			if lateID, err = tx.BeginAttempt(req.Envelope.TurnID, "reviewer", "review", epoch0, nil, ""); err != nil {
				return err
			}
			accepted, err = tx.AcceptResult(lateID, epoch0, "", journal.AttemptOutput{
				RawScrubbed: fake.VerdictApprove(req.Envelope.CandidateHash, lateID),
				Status:      string(transport.StatusComplete), TransportStatus: transport.TransportStatusOK,
			})
			if err != nil {
				return err
			}
			rows, err := tx.ListVerdicts(req.Envelope.TurnID)
			verdictsAtLanding = len(rows)
			return err
		})
		if err != nil {
			plantErr = err.Error()
		}
	}
	round0 := revBlock0("b1", "e")
	build0 := round0.Build
	round0.Build = func(req transport.CompleteRequest) string {
		mu.Lock()
		epoch0 = req.Envelope.Epoch
		mu.Unlock()
		return build0(req)
	}
	round1 := revV2(1, "approve", "", "", "b1 withdrawn")
	build1 := round1.Build
	round1.Build = func(req transport.CompleteRequest) string { plant(req); return build1(req) }
	s = newSuite(t, revRoster(2, 5), map[string][]fake.Step{
		"author":   revAuthor(revAnswer, revRevised),
		"reviewer": revAcks(round0, round1),
	})
	c := s.Dial(t)
	turnID, turn := revSubmit(t, s, c, "s1", nil)
	mu.Lock()
	defer mu.Unlock()

	if plantErr != "" {
		t.Fatal(plantErr)
	}
	if epoch1 <= epoch0 {
		t.Fatalf("round 1 reviews at epoch %d, round 0 at %d: the epoch did not advance", epoch1, epoch0)
	}
	if accepted {
		t.Fatal("a result of round 0's epoch was accepted in round 1")
	}
	// The late result changed nothing: the revision is approved on its own
	// review, and the late result is quarantined, never a verdict.
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	var late *journal.AttemptRow
	for _, a := range s.attemptRows(t, turnID, "reviewer", "review") {
		if a.ID == lateID {
			late = &a
		}
	}
	if late == nil || !late.Quarantined || late.Epoch != epoch0 {
		t.Fatalf("late attempt = %+v, want quarantined at epoch %d", late, epoch0)
	}
	rows := s.verdictRows(t, turnID)
	if verdictsAtLanding != 1 || len(rows) != 2 {
		t.Fatalf("verdicts when the late result landed %d, at the end %d; want 1 then 2", verdictsAtLanding, len(rows))
	}
	for _, v := range rows {
		if v.AttemptID == lateID {
			t.Fatalf("the late result was recorded as a verdict: %+v", v)
		}
	}
	s.revWantBlocks(t, turnID, map[string]string{"b1": "withdrawn"})
}

func TestRevision_SubmitMaxRoundsOutOfRange(t *testing.T) {
	t.Parallel()
	roster := revRoster(2, 5) + "\n[presets.tight]\nmode = \"review\"\nauthor = \"author\"\nrequired_reviewers = [\"reviewer\"]\n[presets.tight.revision]\nmax_rounds = 1\nmax_rounds_ceiling = 3\n"
	s := newSuite(t, roster, map[string][]fake.Step{"author": revAuthor(revAnswer), "reviewer": revAcks(revApprove0())})
	c := s.Dial(t)
	tight := "tight"
	cases := []struct {
		name    string
		preset  *string
		rounds  int
		ceiling int
	}{
		{"above the roster ceiling", nil, 6, 5},
		{"below zero", nil, -1, 5},
		{"above a preset's ceiling", &tight, 4, 3},
		{"below zero on a preset", &tight, -1, 3},
	}
	for i, tc := range cases {
		_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: fmt.Sprintf("bad%d", i), Input: "q", Preset: tc.preset, MaxRounds: revIntPtr(tc.rounds)})
		we := wantRefusal(t, err, wire.ErrorCodeMaxRounds)
		if we.Ceiling == nil || *we.Ceiling != tc.ceiling {
			t.Fatalf("%s: ceiling = %v, want %d", tc.name, we.Ceiling, tc.ceiling)
		}
	}
	if n := s.journalCount(t, `SELECT COUNT(*) FROM turns`); n != 0 {
		t.Fatalf("%d turns created by refused submits", n)
	}
	if n := countTurnSubmitted(s); n != 0 {
		t.Fatalf("%d turn_submitted events after refused submits", n)
	}
	// The ceiling itself is accepted.
	_, turn := revSubmit(t, s, c, "ok", revIntPtr(5))
	wantTurn(t, turn, engine.StateApproved, "all_approved")
}

func TestRevision_RosterLimitsRefusedAtLoad(t *testing.T) {
	t.Parallel()
	preset := func(rounds, ceiling int) string {
		return revRoster(2, 5) + fmt.Sprintf("\n[presets.review.revision]\nmax_rounds = %d\nmax_rounds_ceiling = %d\n", rounds, ceiling)
	}
	cases := map[string]string{
		"default above ceiling":          revRoster(4, 3),
		"ceiling above 5":                revRoster(2, 6),
		"preset default above ceiling":   preset(3, 2),
		"preset ceiling above 5":         preset(2, 6),
		"default above 5 with no bounds": strings.Replace(enginetest.RosterTOML, "max_rounds = 0", "max_rounds = 6", 1),
	}
	for name, roster := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := enginetest.New(t, roster, map[string][]fake.Step{}, enginetest.NoStart())
			err := h.Engine.Start()
			var re *engine.RosterError
			if !errors.As(err, &re) {
				t.Fatalf("Start = %v, want a roster refusal", err)
			}
		})
	}
	// The bounds themselves load.
	ok := newSuite(t, revRoster(5, 5), map[string][]fake.Step{})
	if st := ok.Status(t, ok.Dial(t)); st.Revision.Default != 5 || st.Revision.Ceiling != 5 {
		t.Fatalf("status revision = %+v, want 5/5", st.Revision)
	}
}

func TestRevision_StatusReportsLimits(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		roster            string
		default_, ceiling int
	}{
		"roster values":           {revRoster(3, 4), 3, 4},
		"defaults":                {strings.Replace(enginetest.RosterTOML, "[revision]\nmax_rounds = 0\n", "", 1), 2, 5},
		"default preset override": {revRoster(1, 5) + "\n[presets.review.revision]\nmax_rounds = 2\nmax_rounds_ceiling = 3\n", 2, 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newSuite(t, tc.roster, map[string][]fake.Step{})
			if st := s.Status(t, s.Dial(t)); st.Revision.Default != tc.default_ || st.Revision.Ceiling != tc.ceiling {
				t.Fatalf("status revision = %+v, want default %d ceiling %d", st.Revision, tc.default_, tc.ceiling)
			}
		})
	}
}

func TestRevision_SubmitOverridesBothDirections(t *testing.T) {
	t.Parallel()
	// The reviewer blocks every candidate, so the rounds a turn runs are the
	// limit it ran with.
	blocking := func() map[string][]fake.Step {
		return map[string][]fake.Step{
			"author": revAuthor(revAnswer, revRevised, revRevised+" 2", revRevised+" 3"),
			"reviewer": revAcks(revBlock0("b1", "e"),
				revV2(1, "block", "b2", "e", "b1 maintained"),
				revV2(2, "block", "b3", "e", "b1 maintained", "b2 maintained")),
		}
	}
	cases := []struct {
		name    string
		roster  int
		submit  int
		reason  string
		revised int
	}{
		{"raised", 1, 2, "rounds_exhausted", 2},
		{"lowered", 2, 1, "rounds_exhausted", 1},
		{"lowered to zero", 2, 0, "block:", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSuite(t, revRoster(tc.roster, 3), blocking())
			c := s.Dial(t)
			turnID, turn := revSubmit(t, s, c, "s1", revIntPtr(tc.submit))
			if turn.State != engine.StateIncomplete || turn.TerminalReason == nil || !strings.HasPrefix(*turn.TerminalReason, tc.reason) {
				t.Fatalf("turn ended %q %v, want incomplete %s", turn.State, turn.TerminalReason, tc.reason)
			}
			if n := s.revRounds(turnID); n != tc.revised || turn.Round != tc.revised {
				t.Fatalf("%d rounds (round %d), want %d", n, turn.Round, tc.revised)
			}
			revWantCalls(t, s, "author", "author", tc.revised+1)
			if turn.MaxRounds == nil || *turn.MaxRounds != tc.submit {
				t.Fatalf("turn max_rounds = %v, want %d", turn.MaxRounds, tc.submit)
			}
		})
	}
}

// revTUI drives a TUI App against the suite's socket.
type revTUI struct {
	t    *testing.T
	app  *tui.App
	msgs chan tea.Msg
}

func (d *revTUI) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				d.run(c)
			}
			return
		}
		if msg != nil {
			d.msgs <- msg
		}
	}()
}

func (d *revTUI) send(msg tea.Msg) {
	_, cmd := d.app.Update(msg)
	d.run(cmd)
}

// until feeds the App its messages until cond holds.
func (d *revTUI) until(what string, cond func() bool) {
	d.t.Helper()
	deadline := time.After(waitLimit)
	for !cond() {
		select {
		case m := <-d.msgs:
			d.send(m)
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			d.t.Fatalf("waiting for %s; view:\n%s", what, d.app.View())
		}
	}
}

func TestRevision_RoundsZeroFlagNoRevision(t *testing.T) {
	t.Parallel()
	// `syndic --rounds 0` gives the App WithRounds(0) (cmd/syndic
	// TestParseStartupFlags proves the parse); the App sends it with the
	// submit, and on a roster whose default is 2 the block ends the turn.
	s := newSuite(t, revRoster(2, 5), map[string][]fake.Step{
		"author":   revAuthor(revAnswer, revRevised),
		"reviewer": revAcks(revBlock0("b1", "e"), revV2(1, "approve", "", "", "b1 withdrawn")),
	})
	if st := s.Status(t, s.Dial(t)); st.Revision.Default != 2 {
		t.Fatalf("roster default = %d, want 2", st.Revision.Default)
	}
	var mu sync.Mutex
	var sent []wire.SubmitRequest
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &revTUI{t: t, msgs: make(chan tea.Msg, 1024), app: tui.NewApp(ctx, s.paths.Socket,
		tui.WithRounds(0), tui.WithWorkDir(t.TempDir()),
		tui.WithSubmitObserver(func(r wire.SubmitRequest) { mu.Lock(); sent = append(sent, r); mu.Unlock() }))}
	d.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	d.run(d.app.Init())
	d.until("connected", func() bool { v := d.app.View(); return v != "" && !strings.Contains(v, "engine: connecting") })
	d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("what is the answer?")})
	d.send(tea.KeyMsg{Type: tea.KeyEnter})
	d.until("the submit", func() bool { mu.Lock(); defer mu.Unlock(); return len(sent) == 1 })
	mu.Lock()
	req := sent[0]
	mu.Unlock()
	if req.MaxRounds == nil || *req.MaxRounds != 0 {
		t.Fatalf("submit max_rounds = %v, want 0", req.MaxRounds)
	}

	submitted := s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindTurnSubmitted })
	sub, _ := body[wire.EvTurnSubmitted](submitted, wire.EventKindTurnSubmitted)
	if sub.TurnID == "" {
		t.Fatalf("turn_submitted carries no turn id: %s", submitted.Body)
	}
	s.WaitTerminal(sub.TurnID)
	turn := s.Turn(t, s.Dial(t), sub.TurnID)
	if turn.State != engine.StateIncomplete || turn.TerminalReason == nil || !strings.HasPrefix(*turn.TerminalReason, "block:") {
		t.Fatalf("turn ended %q %v, want incomplete on the block", turn.State, turn.TerminalReason)
	}
	if n := s.revRounds(sub.TurnID); n != 0 {
		t.Fatalf("%d revisions started under --rounds 0", n)
	}
	revWantCalls(t, s, "author", "author", 1)
	if turn.MaxRounds == nil || *turn.MaxRounds != 0 {
		t.Fatalf("turn max_rounds = %v, want 0", turn.MaxRounds)
	}
}

func TestRevision_TurnRecordCarriesLimits(t *testing.T) {
	t.Parallel()
	roster := revRoster(3, 4) + "\n[presets.tight]\nmode = \"review\"\nauthor = \"author\"\nrequired_reviewers = [\"reviewer\"]\n[presets.tight.revision]\nmax_rounds = 1\nmax_rounds_ceiling = 2\n"
	s := newSuite(t, roster, map[string][]fake.Step{
		"author":   revAuthor(revAnswer, revAnswer, revAnswer),
		"reviewer": revAcks(revApprove0(), revApprove0(), revApprove0()),
	})
	c := s.Dial(t)
	tight := "tight"
	cases := []struct {
		name           string
		preset         *string
		rounds         *int
		limit, ceiling int
	}{
		{"roster default", nil, nil, 3, 4},
		{"client value", nil, revIntPtr(1), 1, 4},
		{"preset default", &tight, nil, 1, 2},
	}
	for i, tc := range cases {
		reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: fmt.Sprintf("s%d", i), Input: "q", Preset: tc.preset, MaxRounds: tc.rounds})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		s.WaitTerminal(reply.TurnID)
		info, err := s.Store.GetRoundInfo(reply.TurnID)
		if err != nil || info.MaxRoundsInEffect != tc.limit || info.MaxRoundsCeiling != tc.ceiling {
			t.Fatalf("%s: turn record = %+v, err %v; want limit %d ceiling %d", tc.name, info, err, tc.limit, tc.ceiling)
		}
		if turn := s.Turn(t, c, reply.TurnID); turn.MaxRounds == nil || *turn.MaxRounds != tc.limit {
			t.Fatalf("%s: turn reply max_rounds = %v, want %d", tc.name, turn.MaxRounds, tc.limit)
		}
	}
}

func TestRevision_RevisionCarriesCitedResultsOnly(t *testing.T) {
	t.Parallel()
	// Through the engine: r2's first output is malformed and repaired. The
	// revision request carries each reviewer's accepted output, and nothing
	// of r2's other call.
	s := newSuite(t, revRoster(2, 5, "r1", "r2"), map[string][]fake.Step{
		"author": revAuthor(revAnswer, revRevised),
		"r1":     revAcks(revBlock0("b1", "EVIDENCE-R1"), revV2(1, "approve", "", "", "b1 withdrawn")),
		"r2": revAcks(enginetest.Reply("PRIVATE-FIRST-CALL has no envelope"), revBlock0("b1", "EVIDENCE-R2"),
			revV2(1, "approve", "", "", "b1 withdrawn")),
	})
	c := s.Dial(t)
	_, turn := revSubmit(t, s, c, "s1", nil)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	fb := revFeedback(t, revWantCalls(t, s, "author", "author", 2)[1])
	for _, want := range []string{"reviewer 1", "reviewer 2", "EVIDENCE-R1", "EVIDENCE-R2"} {
		if !strings.Contains(fb, want) {
			t.Errorf("revision feedback lacks %q", want)
		}
	}
	for _, not := range []string{"PRIVATE-FIRST-CALL", "r1", "r2"} {
		if regexp.MustCompile(`\b` + not + `\b`).MatchString(fb) {
			t.Errorf("revision feedback carries %q", not)
		}
	}

	// The builder the engine sends revisions with: a block citing a tool
	// result by id carries that result after it, and no other result of any
	// reviewer. Tool results reach reviewers with tools (milestone E).
	feedback := []prompt.RevisionFeedback{
		{
			Output:   fake.VerdictBlock("h", "a1", "b1", "high", "see tr-cited, it disagrees", "fix", "wrong"),
			Evidence: "see tr-cited, it disagrees",
			Results:  map[string]string{"tr-cited": "CITED-RESULT-BYTES", "tr-uncited": "UNCITED-RESULT-BYTES"},
		},
		{
			Output:  fake.VerdictApprove("h", "a2"),
			Results: map[string]string{"tr-other": "OTHER-REVIEWER-BYTES"},
		},
	}
	msgs, err := prompt.AuthorRevision(prompt.AuthorRevisionInput{
		Input:             "what is the answer?",
		PreviousCandidate: revAnswer,
		ReviewerFeedback:  feedback,
		Delimiter:         "DELIM-7f3a",
		HistoryDoc:        history.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	user := msgs[len(msgs)-1].Content
	cited := strings.Index(user, "CITED-RESULT-BYTES")
	if cited < 0 || cited < strings.Index(user, "tr-cited") || cited > strings.Index(user, "reviewer 2") {
		t.Fatalf("the cited result does not follow the block that cites it: %q", user)
	}
	for _, not := range []string{"UNCITED-RESULT-BYTES", "OTHER-REVIEWER-BYTES", "tr-other"} {
		if strings.Contains(revText(transport.CompleteRequest{Messages: msgs}), not) {
			t.Errorf("revision request carries %q", not)
		}
	}
}
