package engine

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
)

// The round limit a turn runs with when the roster sets none, and the most
// any limit may be (v5 delta section 4).
const (
	defaultMaxRounds        = 2
	defaultMaxRoundsCeiling = 5
)

// Kinds of an accepted reviewer verdict that a revision round tells apart.
const (
	resultApprove  = "approve"
	resultBlock    = "block"
	resultRevision = "revision_request"
)

// reviewResult is a required reviewer's accepted verdict on the current
// candidate: its kind and the complete output the author is sent.
type reviewResult struct {
	kind      string
	attemptID string
	text      string
}

// authorChangeLines returns the sorted change-set lines
// {path, op, base_sha256, new_sha256, new_len} an author result carries. Change
// sets arrive with tools (milestone E); until then a result carries none. It is
// a variable so tests can give a result a patch.
var authorChangeLines = func(transport.CompleteResult) []string { return nil }

// sameRevision reports whether a revision is unchanged (v5 delta section 4):
// its answer bytes and its change-set lines both equal the previous
// candidate's. Round and the snapshot line are not compared.
func sameRevision(prevAnswer string, prevLines []string, answer string, lines []string) bool {
	return prevAnswer == answer && slices.Equal(prevLines, lines)
}

// loopEnabled reports whether the turn may revise at all. With a limit of zero
// the turn behaves as it did before the loop: the first block or revision
// request ends it.
func (t *openTurn) loopEnabled() bool { return t.maxRounds > 0 }

// roundsRemain reports whether a revision may follow the current candidate.
func (t *openTurn) roundsRemain() bool { return t.round < t.maxRounds }

// resetRound clears what belongs to one candidate's review, once a revision is
// frozen. Approvals of the earlier candidate do not carry (v2 section 4).
func (t *openTurn) resetRound() {
	t.results = map[string]reviewResult{}
	t.approved = map[string]bool{}
	t.outputs = nil
	t.reviewersBegun = 0
}

func historyOf(t *openTurn) *history.Document {
	if t.history == nil {
		return history.New()
	}
	return t.history
}

// startRevision hands the author's revision dispatch to the scheduler. It runs
// with e.mu held after the last required reviewer's accepted result.
func (e *Engine) startRevision(t *openTurn) {
	t.revising = true
	if err := e.schedule(t, t.author, func() { e.startAuthor(t) }); err != nil {
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
	}
}

// authorRevisionMessages builds the author's revision dispatch: the previous
// candidate and every required reviewer's output for it, in roster order.
func (e *Engine) authorRevisionMessages(t *openTurn) ([]transport.Message, error) {
	feedback := make([]prompt.RevisionFeedback, 0, len(t.required))
	for _, id := range t.required {
		r, ok := t.results[id]
		if !ok {
			return nil, fmt.Errorf("engine: no accepted result of reviewer %s for the revision", id)
		}
		feedback = append(feedback, prompt.RevisionFeedback{Output: r.text})
	}
	for range maxDelimiterTries {
		delimiter, err := prompt.NewDelimiter(e.rand)
		if err != nil {
			return nil, err
		}
		msgs, err := prompt.AuthorRevision(prompt.AuthorRevisionInput{
			Input:             t.input,
			PreviousCandidate: t.candidate,
			ReviewerFeedback:  feedback,
			Delimiter:         delimiter,
			HistoryDoc:        historyOf(t),
		})
		if errors.Is(err, prompt.ErrDelimiterCollision) {
			continue
		}
		return msgs, err
	}
	return nil, prompt.ErrDelimiterCollision
}

// reviewerMessages builds a reviewer's request for the current candidate: the
// first round's prompt, or a later round's with the reviewer's own open blocks.
func (t *openTurn) reviewerMessages(attemptID, delimiter string, blocks []journal.ReviewerBlock) ([]transport.Message, error) {
	if t.round == 0 {
		return prompt.Reviewer(t.reviewInput(attemptID, delimiter))
	}
	return prompt.ReviewerRevision(t.reviewerRevisionInput(attemptID, delimiter, blocks))
}

// repairMessages builds the repair request for a malformed verdict, in the
// envelope of the round.
func (t *openTurn) repairMessages(originalAttemptID, delimiter string, blocks []journal.ReviewerBlock, output, repairAttemptID string) ([]transport.Message, error) {
	if t.round == 0 {
		return prompt.Repair(t.reviewInput(originalAttemptID, delimiter), output, repairAttemptID)
	}
	return prompt.RepairRevision(t.reviewerRevisionInput(originalAttemptID, delimiter, blocks), output, repairAttemptID)
}

func (t *openTurn) reviewInput(attemptID, delimiter string) prompt.ReviewInput {
	return prompt.ReviewInput{
		Request:       t.input,
		Candidate:     t.candidate,
		CandidateHash: t.candidateHash,
		AttemptID:     attemptID,
		Delimiter:     delimiter,
		History:       t.history,
	}
}

// reviewInputWithEvidence builds the reviewer input with diffs and author evidence populated from the journal.
func (t *openTurn) reviewInputWithEvidence(attemptID, delimiter string, tx *journal.Tx) (prompt.ReviewInput, error) {
	in := t.reviewInput(attemptID, delimiter)

	// Build diffs from the change set if available
	if t.candidateID != "" {
		changeSet, err := tx.GetChangeSet(t.candidateID)
		if err != nil && !errors.Is(err, journal.ErrNoChangeSet) {
			return in, fmt.Errorf("get change set: %w", err)
		}
		if len(changeSet.Paths) > 0 {
			diffs, err := Diffs(changeSet, func(sha string) ([]byte, error) {
				return tx.Content(sha)
			})
			if err != nil {
				return in, fmt.Errorf("generate diffs: %w", err)
			}

			var diffsText bytes.Buffer
			for _, d := range diffs {
				diffsText.WriteString(FormatDiff(d))
			}
			in.Diffs = diffsText.String()
		}
	}

	// Build author evidence from tool calls
	calls, err := AuthorEvidence(tx, t.id, t.author)
	if err != nil {
		return in, fmt.Errorf("get author evidence: %w", err)
	}
	if len(calls) > 0 {
		evidence, err := FormatAuthorEvidence(calls, func(sha string) ([]byte, error) {
			return tx.Content(sha)
		})
		if err != nil {
			return in, fmt.Errorf("format author evidence: %w", err)
		}
		in.AuthorEvidence = evidence
	}

	return in, nil
}

func (t *openTurn) reviewerRevisionInput(attemptID, delimiter string, blocks []journal.ReviewerBlock) prompt.ReviewerRevisionInput {
	open := make([]prompt.OpenBlockForRevision, len(blocks))
	for i, b := range blocks {
		open[i] = prompt.OpenBlockForRevision{BlockID: b.ReviewerBlockID, Severity: b.Severity, Evidence: b.Evidence, Remedy: b.Remedy}
	}
	return prompt.ReviewerRevisionInput{
		Request:       t.input,
		Candidate:     t.candidate,
		CandidateHash: t.candidateHash,
		OpenBlocks:    open,
		AttemptID:     attemptID,
		Delimiter:     delimiter,
		Round:         t.round,
		HistoryDoc:    historyOf(t),
	}
}

// roundProjection is what the revision round about to start may add to the
// turn's charge, in the worst case it can know: the author's revision with its
// output bound and each required reviewer's review of a candidate as long as
// a candidate as long as the author's output bound allows, plus the one repair
// each may be given. authorDefs are the tool definitions the author's revision
// carries; no review or repair dispatch sends any yet.
func (e *Engine) roundProjection(tx *journal.Tx, t *openTurn, authorMsgs []transport.Message, authorDefs []transport.Tool, authorMaxOut int) (int, error) {
	pb, missing := e.budgetFor(t.eff, t.author)
	if missing != "" {
		return 0, fmt.Errorf("engine: participant %q lacks %s", t.author, missing)
	}
	total := satAdd(pb.estimate(authorMsgs, authorDefs), authorMaxOut)
	next := *t
	next.round = t.round + 1
	next.candidateHash = standInHash
	next.candidate = pb.standIn(authorMaxOut)
	for _, id := range t.required {
		blocks, err := tx.ReviewerOpenBlocks(t.id, id)
		if err != nil {
			return 0, err
		}
		msgs, err := next.reviewerMessages(standInToken, standInToken, blocks)
		if err != nil {
			return 0, err
		}
		rb, missing := e.budgetFor(t.eff, id)
		if missing != "" {
			return 0, fmt.Errorf("engine: participant %q lacks %s", id, missing)
		}
		maxOut := rb.maxOutput(*t.eff.Budgets.ReviewerMaxOutputTokens)
		total = satAdd(total, satAdd(rb.estimate(msgs, nil), maxOut))
		repair, err := next.repairMessages(standInToken, standInToken, blocks, rb.standIn(maxOut), standInToken)
		if err != nil {
			return 0, err
		}
		total = satAdd(total, satAdd(rb.estimate(repair, nil), maxOut))
	}
	return total, nil
}

// checkRoundFits refuses a revision round whose projection does not fit what
// is left of the turn's budget.
func (e *Engine) checkRoundFits(tx *journal.Tx, t *openTurn, authorMsgs []transport.Message, authorDefs []transport.Tool, authorMaxOut int) error {
	turn, err := tx.GetTurn(t.id)
	if err != nil {
		return err
	}
	rec, ok, err := readBudget(turn)
	if err != nil || !ok {
		return err
	}
	projection, err := e.roundProjection(tx, t, authorMsgs, authorDefs, authorMaxOut)
	if err != nil {
		return err
	}
	if next := satAdd(rec.Charged, projection); rec.Exhausted || next > rec.Limit {
		return &budgetRefusal{kind: budgetTurn, estimated: next, limit: rec.Limit}
	}
	return nil
}
