package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/candidate"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/verdict"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	reasonReviewerFailed = "reviewer_failed:"
	reasonBlock          = "block:"
	reasonRevision       = "revision_request:"
	reasonMalformed      = "malformed_twice:"

	maxDelimiterTries = 8
)

// reviewerDispatch is one begun reviewer attempt.
type reviewerDispatch struct {
	participant string
	attemptID   string
	req         transport.CompleteRequest
}

func (e *Engine) participantConfig(id string) config.Participant {
	for _, p := range e.roster.Participants {
		if p.ID == id {
			return p
		}
	}
	return config.Participant{}
}

// completeAuthorReview runs accept and gate for the author's result in a
// review-mode turn and, when it passes, freezes the candidate. It reports
// whether reviewers should be dispatched. A turn it does not carry on is
// either ended here or already terminal.
func (e *Engine) completeAuthorReview(t *openTurn, attemptID string, req transport.CompleteRequest, res transport.CompleteResult) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[t.author].Expected
	gate := transport.CheckResult(req, res, expected)

	var hash string
	var quarantined, ended bool
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		hash, quarantined, ended = "", false, false
		accepted, err := tx.AcceptResult(attemptID, req.Envelope.Epoch, "", outputOf(res))
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			return nil
		}
		if err := recordCompletion(tx, emit, t.id, t.author, attemptID, res, gate); err != nil {
			return err
		}
		if gate != nil {
			ended = true
			return endTurnIn(tx, emit, t.id, StateIncomplete, reasonAuthorFailed+authorFailedStatus(res, gate))
		}
		hash, err = candidate.Hash(candidate.Identity{
			ManifestHash:    e.manifestHash,
			TurnID:          t.id,
			AuthorAttemptID: attemptID,
			Content:         []byte(res.Text),
		})
		if err != nil {
			return err
		}
		if _, err := tx.FreezeCandidate(t.id, res.Text, hash); err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindCandidateFrozen, wire.EvCandidateFrozen{TurnID: t.id, CandidateHash: hash}); err != nil {
			return err
		}
		return emit(t.id, wire.EventKindTurnState, wire.EvTurnState{TurnID: t.id, State: StateCandidateFrozen})
	})
	if err != nil {
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		return false
	}
	delete(e.running, t.author)
	if quarantined {
		return false
	}
	if ended {
		if e.open == t {
			e.open = nil
		}
		return false
	}
	t.candidate = res.Text
	t.candidateHash = hash
	return true
}

// runReviewers begins an attempt for every required reviewer, then calls
// them all at once, each on its own goroutine, and returns without waiting.
func (e *Engine) runReviewers(t *openTurn) {
	ds, ok := e.dispatchReviewers(t)
	if !ok {
		return
	}
	for _, d := range ds {
		go func() {
			res := e.complete(d.participant, d.req)
			e.completeReviewer(t, d, res)
		}()
	}
}

// dispatchReviewers begins the attempts. turn_state{reviewers_dispatched}
// commits with the last of them, so it precedes every reviewer result.
func (e *Engine) dispatchReviewers(t *openTurn) ([]reviewerDispatch, bool) {
	ds := make([]reviewerDispatch, 0, len(t.required))
	for i, id := range t.required {
		d, err := e.beginReviewer(t, id, i == len(t.required)-1)
		if err != nil {
			e.mu.Lock()
			for _, begun := range ds {
				delete(e.running, begun.participant)
			}
			e.finishLocked(t, StateIncomplete, reasonReviewerFailed+id+":error")
			e.mu.Unlock()
			return nil, false
		}
		ds = append(ds, d)
	}
	return ds, true
}

// beginReviewer records the request message and the attempt for one
// reviewer in one transaction.
func (e *Engine) beginReviewer(t *openTurn, id string, last bool) (reviewerDispatch, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	p := e.participantConfig(id)
	maxOut := budget.ClampOutput(*t.eff.Budgets.ReviewerMaxOutputTokens, e.built.Participants[id].OutputBound)
	deadline := e.clock.Now().UTC().Add(t.attemptDeadline)
	if t.turnDeadline.Before(deadline) {
		deadline = t.turnDeadline
	}

	var d reviewerDispatch
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		delimiter, err := e.newDelimiter(t)
		if err != nil {
			return err
		}
		attemptID, err := tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:          t.id,
			Participant:     id,
			Phase:           phaseReview,
			Epoch:           turn.Epoch,
			Deadline:        &deadline,
			Delimiter:       delimiter,
			MaxOutputTokens: maxOut,
		})
		if err != nil {
			return err
		}
		msgs, err := prompt.Reviewer(prompt.ReviewInput{
			Request:       t.input,
			Candidate:     t.candidate,
			CandidateHash: t.candidateHash,
			AttemptID:     attemptID,
			Delimiter:     delimiter,
		})
		if err != nil {
			return err
		}
		// The journaled request is the user message: the request and the
		// candidate, fenced.
		content := []byte(msgs[len(msgs)-1].Content)
		msgID, sum, size, err := tx.AppendMessage(t.id, phaseReview, senderEngine, []string{id}, content, nil)
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindMessageAppended, wire.EvMessageAppended{
			MsgID:   msgID,
			TurnID:  t.id,
			Phase:   phaseReview,
			Sender:  senderEngine,
			SHA256:  sum,
			ByteLen: int64(size),
		}); err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
			AttemptID:       attemptID,
			Participant:     id,
			Phase:           phaseReview,
			Epoch:           int64(turn.Epoch),
			Deadline:        deadline.Format(time.RFC3339Nano),
			MaxOutputTokens: int64(maxOut),
		}); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
			Participant: id,
			State:       ParticipantRunning,
		}); err != nil {
			return err
		}
		if last {
			if err := emit(t.id, wire.EventKindTurnState, wire.EvTurnState{TurnID: t.id, State: StateReviewersDispatched}); err != nil {
				return err
			}
		}
		d = reviewerDispatch{
			participant: id,
			attemptID:   attemptID,
			req: transport.CompleteRequest{
				Envelope: transport.AttemptEnvelope{
					Participant:   id,
					TurnID:        t.id,
					Phase:         phaseReview,
					Epoch:         turn.Epoch,
					AttemptID:     attemptID,
					CandidateHash: t.candidateHash,
				},
				Model:           p.Model,
				Messages:        msgs,
				MaxOutputTokens: maxOut,
				Deadline:        deadline,
			},
		}
		return nil
	})
	if err != nil {
		return reviewerDispatch{}, err
	}
	e.running[id] = true
	return d, nil
}

// newDelimiter returns a delimiter that appears in neither the request nor
// the candidate. The attempt id does not affect the collision check, so a
// placeholder stands in for it until the attempt row exists.
func (e *Engine) newDelimiter(t *openTurn) (string, error) {
	for range maxDelimiterTries {
		delimiter, err := prompt.NewDelimiter(e.rand)
		if err != nil {
			return "", err
		}
		_, err = prompt.Reviewer(prompt.ReviewInput{
			Request:       t.input,
			Candidate:     t.candidate,
			CandidateHash: t.candidateHash,
			AttemptID:     "pending",
			Delimiter:     delimiter,
		})
		if errors.Is(err, prompt.ErrDelimiterCollision) {
			continue
		}
		return delimiter, err
	}
	return "", prompt.ErrDelimiterCollision
}

// completeReviewer runs accept, gate, parse and the decision for one
// reviewer's result in one transaction.
func (e *Engine) completeReviewer(t *openTurn, d reviewerDispatch, res transport.CompleteResult) {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[d.participant].Expected
	gate := transport.CheckResult(d.req, res, expected)

	var quarantined, approvedNow bool
	var endState, endReason string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		quarantined, approvedNow, endState, endReason = false, false, "", ""
		accepted, err := tx.AcceptResult(d.attemptID, d.req.Envelope.Epoch, "", outputOf(res))
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			if err := emitCompleted(emit, t.id, d.attemptID, res, nil); err != nil {
				return err
			}
			return emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: d.participant, State: ParticipantIdle})
		}
		if err := recordCompletion(tx, emit, t.id, d.participant, d.attemptID, res, gate); err != nil {
			return err
		}
		if gate != nil {
			endState, endReason = StateIncomplete, reasonReviewerFailed+d.participant+":"+authorFailedStatus(res, gate)
			return endTurnIn(tx, emit, t.id, endState, endReason)
		}

		parsed := verdict.Parse(res.Text, t.candidateHash, d.attemptID)
		if err := tx.SetAttemptCommentary(d.attemptID, parsed.Commentary); err != nil {
			return err
		}
		v := parsed.Verdict
		decision := ""
		if v.Decision == "approve" || v.Decision == "block" {
			decision = v.Decision
		}
		echoed := v.Candidate
		if echoed == "" {
			echoed = t.candidateHash
		}
		verdictID, err := tx.RecordVerdict(d.attemptID, echoed, decision, v.Reason, string(parsed.Classification))
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindVerdictRecorded, wire.EvVerdictRecorded{
			VerdictID:        verdictID,
			AttemptID:        d.attemptID,
			Parsed:           parsed.Classification != verdict.Malformed,
			ValidationStatus: string(parsed.Classification),
		}); err != nil {
			return err
		}

		switch parsed.Classification {
		case verdict.Valid:
			if v.Decision == "block" {
				blockID, err := tx.OpenBlockFor(verdictID, v.BlockID, t.candidateHash, v.Severity, v.Evidence, v.Remedy)
				if err != nil {
					return err
				}
				if err := emit(t.id, wire.EventKindBlockOpened, wire.EvBlockOpened{BlockID: blockID, Severity: v.Severity}); err != nil {
					return err
				}
				endState, endReason = StateIncomplete, reasonBlock+blockID
				break
			}
			approvedNow = true
			if e.allApproved(t, d.participant) {
				endState, endReason = StateApproved, reasonAllApproved
			}
		case verdict.RevisionRequest:
			endState, endReason = StateIncomplete, reasonRevision+d.participant
		case verdict.Rejected:
			endState, endReason = StateIncomplete, reasonReviewerFailed+d.participant+":rejected"
		default:
			endState, endReason = StateIncomplete, reasonMalformed+d.participant
		}
		if endState == "" {
			return nil
		}
		return endTurnIn(tx, emit, t.id, endState, endReason)
	})
	delete(e.running, d.participant)
	if err != nil {
		e.finishLocked(t, StateIncomplete, fmt.Sprintf("%s%s:error", reasonReviewerFailed, d.participant))
		return
	}
	if quarantined {
		return
	}
	if approvedNow {
		t.approved[d.participant] = true
	}
	if endState != "" && e.open == t {
		e.open = nil
	}
}

// allApproved reports whether every required reviewer has a valid approve
// on the candidate, counting the one just recorded.
func (e *Engine) allApproved(t *openTurn, just string) bool {
	for _, id := range t.required {
		if id != just && !t.approved[id] {
			return false
		}
	}
	return true
}

func outputOf(res transport.CompleteResult) journal.AttemptOutput {
	return journal.AttemptOutput{
		RawScrubbed:     res.RawOutput,
		ScrubCount:      res.ScrubCount,
		Identity:        identityString(res.Identity),
		Status:          string(res.Status),
		NativeFinish:    res.NativeFinish,
		TransportStatus: res.TransportStatus,
	}
}

// recordCompletion stores usage and the gate error of an accepted result and
// emits attempt_completed and the participant going idle.
func recordCompletion(tx *journal.Tx, emit hub.Emit, turnID, participant, attemptID string, res transport.CompleteResult, gate error) error {
	var usage *wire.ReportedUsage
	if res.Usage != nil {
		if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
			return err
		}
		promptTokens, completionTokens := int64(res.Usage.Prompt), int64(res.Usage.Completion)
		usage = &wire.ReportedUsage{PromptTokens: &promptTokens, CompletionTokens: &completionTokens}
	}
	if gate != nil {
		if err := tx.SetAttemptGate(attemptID, gate.Error()); err != nil {
			return err
		}
	}
	if err := emitCompleted(emit, turnID, attemptID, res, usage); err != nil {
		return err
	}
	return emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: participant, State: ParticipantIdle})
}

func emitCompleted(emit hub.Emit, turnID, attemptID string, res transport.CompleteResult, usage *wire.ReportedUsage) error {
	return emit(turnID, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
		AttemptID:       attemptID,
		Status:          string(res.Status),
		ScrubCount:      res.ScrubCount,
		TransportStatus: res.TransportStatus,
		Usage:           usage,
	})
}
