package acceptance

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	protocol = "Protocol"
	authorOK = "the answer is 42"
)

func init() {
	cover(protocol, "approve path reaches `approved` with the candidate hash in the answer", "TestProtocol_ApproveWithHash")
	cover(protocol, "`VerdictProseApprove` yields repair then INCOMPLETE `malformed_twice`", "TestProtocol_ProseApproveRepairThenMalformedTwice")
	cover(protocol, "`VerdictWrongHash` yields `rejected` and INCOMPLETE with no repair", "TestProtocol_WrongHashRejected")
	cover(protocol, "`VerdictApproveWithChanges` yields `revision_request`, INCOMPLETE, no repair", "TestProtocol_ApproveWithChangesRevisionRequest")
	cover(protocol, "`VerdictTwoEnvelopes` is malformed", "TestProtocol_TwoEnvelopesMalformed")
	cover(protocol, "`VerdictBlockWithEndMarker` parses to the first delimiter and the block opens", "TestProtocol_BlockWithEndMarker")
	cover(protocol, "a block ends INCOMPLETE with the block `open`", "TestProtocol_BlockEndsIncomplete")
	cover(protocol, "an author result with status `truncated` yields `author_failed:truncated` and nothing is dispatched to reviewers", "TestProtocol_AuthorTruncated")
	cover(protocol, "a reviewer result with mismatched `ReportedModel` fails `CheckResult` and is never parsed", "TestProtocol_ReviewerModelMismatch")
	cover(protocol, "solo with status `refused` is `incomplete`, not `solo_complete`", "TestProtocol_SoloRefused")
}

// verdict is a reviewer step whose reply is built from the request it
// answers, so the candidate hash and attempt id are the ones the engine sent.
func verdict(build func(candidateHash, attemptID string) string) fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return build(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
}

// reviewScripts scripts the author's one reply and the reviewer's steps.
func reviewScripts(reviewer ...fake.Step) map[string][]fake.Step {
	return map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": reviewer,
	}
}

// reviewCalls returns the requests the participant's adapter received in the
// review phase. A terminal delivery after the turn is a call of its own phase
// and not counted.
func (s *suite) calls(participant, phase string) []transport.CompleteRequest {
	var out []transport.CompleteRequest
	for _, call := range s.Adapters[participant].Calls() {
		if call.Request.Envelope.Phase == phase {
			out = append(out, call.Request)
		}
	}
	return out
}

// attemptRows returns the turn's attempts of the phase for the participant.
func (s *suite) attemptRows(t *testing.T, turnID, participant, phase string) []journal.AttemptRow {
	t.Helper()
	rows, err := s.Store.ListAttempts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	var out []journal.AttemptRow
	for _, r := range rows {
		if r.Participant == participant && r.Phase == phase {
			out = append(out, r)
		}
	}
	return out
}

// wantTurn requires the turn record to be terminal in state with reason.
func wantTurn(t *testing.T, turn wire.TurnReply, state, reason string) {
	t.Helper()
	if turn.State != state || turn.TerminalReason == nil || *turn.TerminalReason != reason {
		got := "<none>"
		if turn.TerminalReason != nil {
			got = *turn.TerminalReason
		}
		t.Fatalf("turn is %q with reason %q, want %q with reason %q", turn.State, got, state, reason)
	}
}

// endReview submits a review turn and waits for it to end, returning the
// turn record the protocol gives for it.
func endReview(t *testing.T, s *suite, submissionID string) (turnID string, turn wire.TurnReply) {
	t.Helper()
	c := s.Dial(t)
	turnID = s.Submit(t, c, submissionID)
	s.WaitTerminal(turnID)
	return turnID, s.Turn(t, c, turnID)
}

// verdictRows returns the turn's verdict rows from the journal.
func (s *suite) verdictRows(t *testing.T, turnID string) []journal.VerdictRow {
	t.Helper()
	rows, err := s.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// noRepair requires that none of the turn's attempts is a repair.
func noRepair(t *testing.T, turn wire.TurnReply) {
	t.Helper()
	for _, a := range turn.Attempts {
		if a.RepairOf != nil {
			t.Fatalf("attempt %s is a repair of %s; none was expected", a.ID, *a.RepairOf)
		}
	}
}

func TestProtocol_ApproveWithHash(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if turn.CandidateHash == nil || *turn.CandidateHash == "" {
		t.Fatalf("approved turn has no candidate hash: %+v", turn)
	}
	reviews := s.calls("reviewer", "review")
	if len(reviews) != 1 {
		t.Fatalf("reviewer received %d review requests, want 1", len(reviews))
	}
	// The hash in the answer is the one the reviewer was asked about and
	// approved, and the one announced when the candidate froze.
	if got := reviews[0].Envelope.CandidateHash; got != *turn.CandidateHash {
		t.Fatalf("answer hash %s, reviewer was shown %s", *turn.CandidateHash, got)
	}
	frozen := s.WaitEvent(func(ev event) bool {
		f, ok := body[wire.EvCandidateFrozen](ev, wire.EventKindCandidateFrozen)
		return ok && f.TurnID == turnID
	})
	if f, _ := body[wire.EvCandidateFrozen](frozen, wire.EventKindCandidateFrozen); f.CandidateHash != *turn.CandidateHash {
		t.Fatalf("candidate_frozen hash %s, turn hash %s", f.CandidateHash, *turn.CandidateHash)
	}
	rows, err := s.Store.ListVerdicts(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Parsed != "approve" || rows[0].ValidationStatus != "valid" || rows[0].CandidateHash != *turn.CandidateHash {
		t.Fatalf("verdicts = %+v, want one valid approve of %s", rows, *turn.CandidateHash)
	}
	if len(turn.Verdicts) != 1 || !turn.Verdicts[0].Parsed || turn.Verdicts[0].ValidationStatus != "valid" {
		t.Fatalf("turn verdicts = %+v", turn.Verdicts)
	}
	if len(turn.OpenBlocks) != 0 {
		t.Fatalf("approved turn has open blocks %+v", turn.OpenBlocks)
	}
}

func TestProtocol_ProseApproveRepairThenMalformedTwice(t *testing.T) {
	t.Parallel()
	prose := enginetest.Reply(fake.VerdictProseApprove())
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(prose, prose))
	turnID, turn := endReview(t, s, "s1")

	wantTurn(t, turn, engine.StateIncomplete, "malformed_twice:reviewer")
	reviews := s.attemptRows(t, turnID, "reviewer", "review")
	if len(reviews) != 2 {
		t.Fatalf("reviewer had %d review attempts, want the original and one repair", len(reviews))
	}
	first, second := reviews[0], reviews[1]
	if first.RepairOf != nil {
		t.Fatalf("first attempt is a repair of %s", *first.RepairOf)
	}
	if second.RepairOf == nil || *second.RepairOf != first.ID {
		t.Fatalf("second attempt repairs %v, want %s", second.RepairOf, first.ID)
	}
	if n := len(s.calls("reviewer", "review")); n != 2 {
		t.Fatalf("reviewer was called %d times, want 2: no second repair", n)
	}
	rows := s.verdictRows(t, turnID)
	if len(rows) != 2 {
		t.Fatalf("verdicts = %+v, want one malformed row per attempt", rows)
	}
	for _, r := range rows {
		if r.ValidationStatus != "malformed" || r.Parsed != "" && r.Parsed != "none" {
			t.Fatalf("verdict %+v, want malformed and unparsed", r)
		}
	}
	for _, v := range turn.Verdicts {
		if v.Parsed || v.ValidationStatus != "malformed" {
			t.Fatalf("turn verdict %+v, want malformed and unparsed", v)
		}
	}
}

func TestProtocol_WrongHashRejected(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictWrongHash)))
	turnID, turn := endReview(t, s, "s1")

	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:rejected")
	rows := s.verdictRows(t, turnID)
	if len(rows) != 1 || rows[0].ValidationStatus != "rejected" {
		t.Fatalf("verdicts = %+v, want one rejected", rows)
	}
	if want := *turn.CandidateHash; rows[0].CandidateHash == want {
		t.Fatalf("rejected verdict echoes the real hash %s", want)
	}
	noRepair(t, turn)
	if n := len(s.calls("reviewer", "review")); n != 1 {
		t.Fatalf("reviewer was called %d times, want 1", n)
	}
}

func TestProtocol_ApproveWithChangesRevisionRequest(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApproveWithChanges)))
	turnID, turn := endReview(t, s, "s1")

	wantTurn(t, turn, engine.StateIncomplete, "revision_request:reviewer")
	rows := s.verdictRows(t, turnID)
	if len(rows) != 1 || rows[0].ValidationStatus != "revision_request" {
		t.Fatalf("verdicts = %+v, want one revision_request", rows)
	}
	noRepair(t, turn)
	if n := len(s.calls("reviewer", "review")); n != 1 {
		t.Fatalf("reviewer was called %d times, want 1", n)
	}
	if len(turn.OpenBlocks) != 0 {
		t.Fatalf("a revision request opened blocks %+v", turn.OpenBlocks)
	}
}

func TestProtocol_TwoEnvelopesMalformed(t *testing.T) {
	t.Parallel()
	two := verdict(fake.VerdictTwoEnvelopes)
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(two, two))
	turnID, turn := endReview(t, s, "s1")

	rows := s.verdictRows(t, turnID)
	if len(rows) == 0 {
		t.Fatal("no verdict recorded for the two-envelope output")
	}
	for _, r := range rows {
		if r.ValidationStatus != "malformed" {
			t.Fatalf("verdict %+v, want malformed", r)
		}
	}
	for _, v := range turn.Verdicts {
		if v.Parsed {
			t.Fatalf("turn verdict %+v was parsed", v)
		}
	}
	wantTurn(t, turn, engine.StateIncomplete, "malformed_twice:reviewer")
	if len(turn.OpenBlocks) != 0 || turn.State == engine.StateApproved {
		t.Fatalf("two envelopes were taken as a verdict: %+v", turn)
	}
}

func TestProtocol_BlockWithEndMarker(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictBlockWithEndMarker)))
	turnID, turn := endReview(t, s, "s1")

	rows := s.verdictRows(t, turnID)
	if len(rows) != 1 || rows[0].ValidationStatus != "valid" || rows[0].Parsed != "block" {
		t.Fatalf("verdicts = %+v, want one valid block", rows)
	}
	// The verdict ends at the first END VERDICT line: the reason is what came
	// before it, and the text after is commentary outside the envelope.
	if !strings.Contains(rows[0].Reason, "This is a reason with") || strings.Contains(rows[0].Reason, "plus more text") {
		t.Fatalf("verdict reason = %q, want the text before the first delimiter only", rows[0].Reason)
	}
	reviewer := s.attemptRows(t, turnID, "reviewer", "review")
	if len(reviewer) != 1 || reviewer[0].Commentary == nil || !strings.Contains(*reviewer[0].Commentary, "after the marker") {
		t.Fatalf("reviewer attempt = %+v, want the text after the first delimiter kept as commentary", reviewer)
	}
	blockID := rows[0].ID + ":b1"
	wantTurn(t, turn, engine.StateIncomplete, "block:"+blockID)
	if len(turn.OpenBlocks) != 1 || turn.OpenBlocks[0].ID != blockID || turn.OpenBlocks[0].State != "open" || turn.OpenBlocks[0].Severity != "high" {
		t.Fatalf("open blocks = %+v, want %s open", turn.OpenBlocks, blockID)
	}
	blocks, err := s.Store.OpenBlocks(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].Evidence != "test" || blocks[0].Remedy != "fix it" || blocks[0].ReviewerBlockID != "b1" {
		t.Fatalf("stored blocks = %+v, want evidence and remedy from before the delimiter", blocks)
	}
	s.WaitEvent(func(ev event) bool {
		b, ok := body[wire.EvBlockOpened](ev, wire.EventKindBlockOpened)
		return ok && b.BlockID == blockID
	})
}

func TestProtocol_BlockEndsIncomplete(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(func(hash, attempt string) string {
		return fake.VerdictBlock(hash, attempt, "b7", "medium", "line 3 divides by zero", "guard the divisor", "unsafe")
	})))
	turnID, turn := endReview(t, s, "s1")

	rows := s.verdictRows(t, turnID)
	if len(rows) != 1 || rows[0].Parsed != "block" || rows[0].ValidationStatus != "valid" {
		t.Fatalf("verdicts = %+v, want one valid block", rows)
	}
	blockID := rows[0].ID + ":b7"
	wantTurn(t, turn, engine.StateIncomplete, "block:"+blockID)
	if len(turn.OpenBlocks) != 1 || turn.OpenBlocks[0] != (wire.OpenBlock{ID: blockID, Severity: "medium", State: "open"}) {
		t.Fatalf("open blocks = %+v, want %s open at medium", turn.OpenBlocks, blockID)
	}
	if turn.State == engine.StateApproved {
		t.Fatal("a blocked turn was approved")
	}
	noRepair(t, turn)
}

func TestProtocol_AuthorTruncated(t *testing.T) {
	t.Parallel()
	scripts := reviewScripts(verdict(fake.VerdictApprove))
	scripts["author"] = []fake.Step{{Kind: fake.StepKindReply, Text: "cut off", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}}
	s := newSuite(t, enginetest.RosterTOML, scripts)
	turnID, turn := endReview(t, s, "s1")

	wantTurn(t, turn, engine.StateIncomplete, "author_failed:truncated")
	if turn.CandidateHash != nil {
		t.Fatalf("a truncated author froze candidate %s", *turn.CandidateHash)
	}
	if n := len(s.Adapters["reviewer"].Calls()); n != 0 {
		t.Fatalf("reviewer was called %d times, want none", n)
	}
	if rows := s.attemptRows(t, turnID, "reviewer", "review"); len(rows) != 0 {
		t.Fatalf("reviewer attempts begun: %+v", rows)
	}
	for _, ev := range s.Events() {
		if _, ok := body[wire.EvCandidateFrozen](ev, wire.EventKindCandidateFrozen); ok {
			t.Fatal("candidate_frozen was emitted")
		}
	}
}

func TestProtocol_ReviewerModelMismatch(t *testing.T) {
	t.Parallel()
	// The reply is a well-formed approval of the right candidate; only the
	// model it reports is not the one the roster names. If the engine parsed
	// it the turn would be approved.
	step := verdict(fake.VerdictApprove)
	step.ReportedModel = "some-other-model"
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(step))
	turnID, turn := endReview(t, s, "s1")

	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:gate:identity mismatch")
	if rows := s.verdictRows(t, turnID); len(rows) != 0 {
		t.Fatalf("verdicts = %+v, want none: the output must never be parsed", rows)
	}
	if len(turn.Verdicts) != 0 {
		t.Fatalf("turn verdicts = %+v", turn.Verdicts)
	}
	reviewer := s.attemptRows(t, turnID, "reviewer", "review")
	if len(reviewer) != 1 || reviewer[0].GateError == nil || !strings.Contains(*reviewer[0].GateError, "identity mismatch") {
		t.Fatalf("reviewer attempt = %+v, want a recorded gate error", reviewer)
	}
	noRepair(t, turn)
}

func TestProtocol_SoloRefused(t *testing.T) {
	t.Parallel()
	scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindReply, Text: "I cannot help with that", Status: transport.StatusRefused, TransportStatus: transport.TransportStatusOK}}}
	s := newSuite(t, enginetest.RosterTOML, scripts)
	c := s.Dial(t)
	turnID := s.SubmitPreset(t, c, "s1", "solo")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateIncomplete, "author_failed:refused")
	if turn.State == engine.StateSoloComplete {
		t.Fatal("a refused solo answer was presented as solo_complete")
	}
	if turn.Mode != "solo" {
		t.Fatalf("mode = %q", turn.Mode)
	}
}
