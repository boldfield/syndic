package engine

import (
	"context"
	"fmt"
	"io"
	"sync"
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

// runTurn carries a submitted turn to a terminal state. It runs on its own
// goroutine and holds the engine mutex only around journal transactions and
// the in-memory state they change, never across the adapter call.
func (e *Engine) runTurn(t *openTurn) {
	if t.mode == modeSolo {
		e.runTurnSolo(t)
	} else {
		e.runTurnReview(t)
	}
}

func (e *Engine) runTurnSolo(t *openTurn) {
	req, attemptID, err := e.dispatchAuthor(t)
	if err != nil {
		// Nothing was written for the dispatch; end the turn so the engine
		// does not stay busy.
		e.finishTurn(t, StateIncomplete, reasonAuthorFailed+"error")
		return
	}

	res, err := e.adapterFor(t.author).Complete(context.Background(), req)
	if err != nil {
		res = transport.CompleteResult{
			Envelope:        req.Envelope,
			Status:          transport.StatusError,
			TransportStatus: "adapter_error:" + err.Error(),
		}
	}
	e.completeAuthor(t, attemptID, req, res)
}

func (e *Engine) runTurnReview(t *openTurn) {
	req, attemptID, err := e.dispatchAuthor(t)
	if err != nil {
		e.finishTurn(t, StateIncomplete, reasonAuthorFailed+"error")
		return
	}

	res, err := e.adapterFor(t.author).Complete(context.Background(), req)
	if err != nil {
		res = transport.CompleteResult{
			Envelope:        req.Envelope,
			Status:          transport.StatusError,
			TransportStatus: "adapter_error:" + err.Error(),
		}
	}

	e.completeAuthorReview(t, attemptID, req, res)
}

func (e *Engine) adapterFor(id string) transport.Adapter {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.adapters[id]
}

// dispatchAuthor records the request message and the attempt, and moves the
// turn to author_dispatched, all in one transaction.
func (e *Engine) dispatchAuthor(t *openTurn) (transport.CompleteRequest, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	mp := e.built.Participants[t.author]
	var p config.Participant
	for _, candidate := range e.roster.Participants {
		if candidate.ID == t.author {
			p = candidate
			break
		}
	}

	msgs := prompt.Author(t.input)
	maxOut := budget.ClampOutput(*t.eff.Budgets.AuthorMaxOutputTokens, mp.OutputBound)
	deadline := e.clock.Now().UTC().Add(t.attemptDeadline)
	if t.turnDeadline.Before(deadline) {
		deadline = t.turnDeadline
	}

	var req transport.CompleteRequest
	var attemptID string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		msgID, sum, size, err := tx.AppendMessage(t.id, phaseAuthor, senderUser, []string{t.author}, []byte(t.input), nil)
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindMessageAppended, wire.EvMessageAppended{
			MsgID:   msgID,
			TurnID:  t.id,
			Phase:   phaseAuthor,
			Sender:  senderUser,
			SHA256:  sum,
			ByteLen: int64(size),
		}); err != nil {
			return err
		}
		attemptID, err = tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:          t.id,
			Participant:     t.author,
			Phase:           phaseAuthor,
			Epoch:           turn.Epoch,
			Deadline:        &deadline,
			MaxOutputTokens: maxOut,
		})
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
			AttemptID:       attemptID,
			Participant:     t.author,
			Phase:           phaseAuthor,
			Epoch:           int64(turn.Epoch),
			Deadline:        deadline.Format(time.RFC3339Nano),
			MaxOutputTokens: int64(maxOut),
		}); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
			Participant: t.author,
			State:       ParticipantRunning,
		}); err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindTurnState, wire.EvTurnState{TurnID: t.id, State: StateAuthorDispatched}); err != nil {
			return err
		}
		req = transport.CompleteRequest{
			Envelope: transport.AttemptEnvelope{
				Participant: t.author,
				TurnID:      t.id,
				Phase:       phaseAuthor,
				Epoch:       turn.Epoch,
				AttemptID:   attemptID,
			},
			Model:           p.Model,
			Messages:        msgs,
			MaxOutputTokens: maxOut,
			Deadline:        deadline,
		}
		return nil
	})
	if err != nil {
		return transport.CompleteRequest{}, "", err
	}
	e.running[t.author] = true
	return req, attemptID, nil
}

// completeAuthor runs accept, gate and the terminal path for the author's
// result in one transaction.
func (e *Engine) completeAuthor(t *openTurn, attemptID string, req transport.CompleteRequest, res transport.CompleteResult) {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[t.author].Expected
	gate := transport.CheckResult(req, res, expected)

	quarantined := false
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		accepted, err := tx.AcceptResult(attemptID, req.Envelope.Epoch, "", journal.AttemptOutput{
			RawScrubbed:     res.RawOutput,
			ScrubCount:      res.ScrubCount,
			Identity:        identityString(res.Identity),
			Status:          string(res.Status),
			NativeFinish:    res.NativeFinish,
			TransportStatus: res.TransportStatus,
		})
		if err != nil {
			return err
		}
		if !accepted {
			// A late result for a superseded epoch or a terminal turn is
			// quarantined and nothing further happens.
			quarantined = true
			return nil
		}

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
		if err := emit(t.id, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
			AttemptID:       attemptID,
			Status:          string(res.Status),
			ScrubCount:      res.ScrubCount,
			TransportStatus: res.TransportStatus,
			Usage:           usage,
		}); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
			Participant: t.author,
			State:       ParticipantIdle,
		}); err != nil {
			return err
		}
		if gate == nil {
			return endTurnIn(tx, emit, t.id, StateSoloComplete, reasonSoloOK)
		}
		return endTurnIn(tx, emit, t.id, StateIncomplete, reasonAuthorFailed+authorFailedStatus(res, gate))
	})
	if quarantined {
		return
	}
	if err != nil {
		// The transaction left no durable change. End the turn on its own so
		// the engine does not stay busy; if that fails too, the turn stays
		// open in memory exactly as it is in the journal.
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		return
	}
	delete(e.running, t.author)
	e.open = nil
}

// completeAuthorReview runs accept, gate and reviewer dispatch for the author's result.
func (e *Engine) completeAuthorReview(t *openTurn, attemptID string, req transport.CompleteRequest, res transport.CompleteResult) {
	e.mu.Lock()

	expected := e.built.Participants[t.author].Expected
	manifestHash := e.manifestHash

	e.mu.Unlock()

	gate := transport.CheckResult(req, res, expected)

	var authorOutput string
	var candidateHash string
	quarantined := false
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		accepted, err := tx.AcceptResult(attemptID, req.Envelope.Epoch, "", journal.AttemptOutput{
			RawScrubbed:     res.RawOutput,
			ScrubCount:      res.ScrubCount,
			Identity:        identityString(res.Identity),
			Status:          string(res.Status),
			NativeFinish:    res.NativeFinish,
			TransportStatus: res.TransportStatus,
		})
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			return nil
		}

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
		if err := emit(t.id, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
			AttemptID:       attemptID,
			Status:          string(res.Status),
			ScrubCount:      res.ScrubCount,
			TransportStatus: res.TransportStatus,
			Usage:           usage,
		}); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
			Participant: t.author,
			State:       ParticipantIdle,
		}); err != nil {
			return err
		}

		if gate != nil {
			return endTurnIn(tx, emit, t.id, StateIncomplete, reasonAuthorFailed+authorFailedStatus(res, gate))
		}

		authorOutput = res.Text
		hash, err := hashCandidate(manifestHash, t.id, attemptID, res.Text)
		if err != nil {
			return err
		}
		candidateHash = hash

		_, err = tx.FreezeCandidate(t.id, res.Text, hash)
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindCandidateFrozen, wire.EvCandidateFrozen{
			TurnID:        t.id,
			CandidateHash: hash,
		}); err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindTurnState, wire.EvTurnState{
			TurnID: t.id,
			State:  StateCandidateFrozen,
		}); err != nil {
			return err
		}

		return nil
	})
	if quarantined {
		return
	}
	if err != nil {
		e.finishTurn(t, StateIncomplete, reasonAuthorFailed+"error")
		return
	}

	e.mu.Lock()
	delete(e.running, t.author)
	e.mu.Unlock()

	reviewers := t.eff.Preset.RequiredReviewers
	dispatchReviewers(e, t, candidateHash, authorOutput, reviewers)
}

// finishTurn ends a turn that has no result to record.
func (e *Engine) finishTurn(t *openTurn, state, reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.finishLocked(t, state, reason)
}

// finishLocked is finishTurn with e.mu held.
func (e *Engine) finishLocked(t *openTurn, state, reason string) {
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return endTurnIn(tx, emit, t.id, state, reason)
	})
	if err != nil {
		return
	}
	delete(e.running, t.author)
	e.open = nil
}

// endTurnIn is the terminal path: record the state and reason, bump the
// epoch so no later result is accepted, and emit turn_state.
func endTurnIn(tx *journal.Tx, emit hub.Emit, turnID, state, reason string) error {
	if err := tx.EndTurn(turnID, state, reason); err != nil {
		return err
	}
	if _, err := tx.BumpEpoch(turnID); err != nil {
		return err
	}
	return emit(turnID, wire.EventKindTurnState, wire.EvTurnState{TurnID: turnID, State: state, Reason: &reason})
}

// authorFailedStatus follows the design's "Terminal reasons" rule: the
// transport status when it is not ok, else the completion status when it is
// not complete, else the gate error.
func authorFailedStatus(res transport.CompleteResult, gate error) string {
	if res.TransportStatus != "" && res.TransportStatus != transport.TransportStatusOK {
		return res.TransportStatus
	}
	if res.Status != transport.StatusComplete {
		return string(res.Status)
	}
	return fmt.Sprintf("gate:%v", gate)
}

func identityString(id transport.ModelIdentity) string {
	if id.Value == "" {
		return id.Kind
	}
	return id.Kind + ":" + id.Value
}

func hashCandidate(manifestHash, turnID, authorAttemptID, authorOutput string) (string, error) {
	return candidate.Hash(candidate.Identity{
		ManifestHash:    manifestHash,
		TurnID:          turnID,
		AuthorAttemptID: authorAttemptID,
		Content:         []byte(authorOutput),
		EvidenceIDs:     nil,
	})
}

// dispatchReviewers dispatches all required reviewers in parallel goroutines.
func dispatchReviewers(e *Engine, t *openTurn, candidateHash, authorOutput string, reviewers []string) {
	var wgDispatch sync.WaitGroup
	wgDispatch.Add(len(reviewers))
	readyCh := make(chan struct{}, len(reviewers))

	for _, reviewerID := range reviewers {
		go func(revID string) {
			defer wgDispatch.Done()
			e.dispatchReviewerOnce(t, revID, candidateHash, authorOutput, readyCh)
		}(reviewerID)
	}

	// Wait for all reviewers to be dispatched (ready).
	for i := 0; i < len(reviewers); i++ {
		<-readyCh
	}

	// All reviewers have been dispatched; emit the turn state event.
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return emit(t.id, wire.EventKindTurnState, wire.EvTurnState{
			TurnID: t.id,
			State:  StateReviewersDispatched,
		})
	})
	if err != nil {
		e.finishTurn(t, StateIncomplete, reasonAuthorFailed+"error")
	}

	wgDispatch.Wait()
}

// dispatchReviewerOnce dispatches a single reviewer.
func (e *Engine) dispatchReviewerOnce(t *openTurn, reviewerID, candidateHash, authorOutput string, readyCh chan struct{}) {
	var delimiter string
	var req transport.CompleteRequest
	var attemptID string

	e.mu.Lock()
	mp := e.built.Participants[reviewerID]
	var p config.Participant
	for _, candidate := range e.roster.Participants {
		if candidate.ID == reviewerID {
			p = candidate
			break
		}
	}
	maxOut := budget.ClampOutput(*t.eff.Budgets.ReviewerMaxOutputTokens, mp.OutputBound)
	deadline := e.clock.Now().UTC().Add(t.attemptDeadline)
	if t.turnDeadline.Before(deadline) {
		deadline = t.turnDeadline
	}

	genDelimiter := func(rand io.Reader) (string, error) {
		return prompt.NewDelimiter(rand)
	}

	for {
		var err error
		delimiter, err = genDelimiter(e.rand)
		if err != nil {
			e.mu.Unlock()
			e.finishTurn(t, StateIncomplete, reasonAuthorFailed+"error")
			return
		}

		err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
			turn, err := tx.GetTurn(t.id)
			if err != nil {
				return err
			}

			attemptID, err = tx.BeginAttemptWith(journal.AttemptParams{
				TurnID:          t.id,
				Participant:     reviewerID,
				Phase:           "review",
				Epoch:           turn.Epoch,
				Deadline:        &deadline,
				MaxOutputTokens: maxOut,
				Delimiter:       delimiter,
			})
			if err != nil {
				return err
			}

			reviewInput := prompt.ReviewInput{
				Request:       t.input,
				Candidate:     authorOutput,
				CandidateHash: candidateHash,
				AttemptID:     attemptID,
				Delimiter:     delimiter,
			}
			msgs, err := prompt.Reviewer(reviewInput)
			if err != nil {
				if err == prompt.ErrDelimiterCollision {
					return fmt.Errorf("delimiter_collision")
				}
				return err
			}

			msgID, sum, size, err := tx.AppendMessage(t.id, "review", "system", []string{reviewerID}, []byte(t.input), nil)
			if err != nil {
				return err
			}
			if err := emit(t.id, wire.EventKindMessageAppended, wire.EvMessageAppended{
				MsgID:   msgID,
				TurnID:  t.id,
				Phase:   "review",
				Sender:  "system",
				SHA256:  sum,
				ByteLen: int64(size),
			}); err != nil {
				return err
			}

			req = transport.CompleteRequest{
				Envelope: transport.AttemptEnvelope{
					Participant:   reviewerID,
					TurnID:        t.id,
					Phase:         "review",
					Epoch:         turn.Epoch,
					AttemptID:     attemptID,
					CandidateHash: candidateHash,
				},
				Model:           p.Model,
				Messages:        msgs,
				MaxOutputTokens: maxOut,
				Deadline:        deadline,
			}

			if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
				AttemptID:       attemptID,
				Participant:     reviewerID,
				Phase:           "review",
				Epoch:           int64(turn.Epoch),
				Deadline:        deadline.Format(time.RFC3339Nano),
				MaxOutputTokens: int64(maxOut),
			}); err != nil {
				return err
			}
			if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
				Participant: reviewerID,
				State:       ParticipantRunning,
			}); err != nil {
				return err
			}

			return nil
		})

		if err != nil {
			if err.Error() == "delimiter_collision" {
				continue
			}
			e.mu.Unlock()
			e.finishTurn(t, StateIncomplete, reasonAuthorFailed+"error")
			return
		}
		break
	}

	e.running[reviewerID] = true
	e.mu.Unlock()

	readyCh <- struct{}{}

	res, err := e.adapterFor(reviewerID).Complete(context.Background(), req)
	if err != nil {
		res = transport.CompleteResult{
			Envelope:        req.Envelope,
			Status:          transport.StatusError,
			TransportStatus: "adapter_error:" + err.Error(),
		}
	}
	e.completeReviewerResult(t, reviewerID, attemptID, req, res, candidateHash)
}

// completeReviewerResult processes a reviewer's result.
func (e *Engine) completeReviewerResult(t *openTurn, reviewerID, attemptID string, req transport.CompleteRequest, res transport.CompleteResult, candidateHash string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[reviewerID].Expected

	var quarantined bool
	var approved bool
	var blockOpened bool
	var revisionRequested bool
	var failed bool

	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		accepted, err := tx.AcceptResult(attemptID, req.Envelope.Epoch, "", journal.AttemptOutput{
			RawScrubbed:     res.RawOutput,
			ScrubCount:      res.ScrubCount,
			Identity:        identityString(res.Identity),
			Status:          string(res.Status),
			NativeFinish:    res.NativeFinish,
			TransportStatus: res.TransportStatus,
		})
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			return emit(t.id, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
				AttemptID:       attemptID,
				Status:          string(res.Status),
				ScrubCount:      res.ScrubCount,
				TransportStatus: res.TransportStatus,
			})
		}

		var usage *wire.ReportedUsage
		if res.Usage != nil {
			if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
				return err
			}
			promptTokens, completionTokens := int64(res.Usage.Prompt), int64(res.Usage.Completion)
			usage = &wire.ReportedUsage{PromptTokens: &promptTokens, CompletionTokens: &completionTokens}
		}

		gate := transport.CheckResult(req, res, expected)
		if gate != nil {
			if err := tx.SetAttemptGate(attemptID, gate.Error()); err != nil {
				return err
			}
			if err := emit(t.id, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
				AttemptID:       attemptID,
				Status:          string(res.Status),
				ScrubCount:      res.ScrubCount,
				TransportStatus: res.TransportStatus,
				Usage:           usage,
			}); err != nil {
				return err
			}
			if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
				Participant: reviewerID,
				State:       ParticipantIdle,
			}); err != nil {
				return err
			}
			failed = true
			return endTurnIn(tx, emit, t.id, StateIncomplete, "reviewer_failed:"+reviewerID+":"+fmt.Sprintf("gate:%v", gate))
		}

		vd := verdict.Parse(res.Text, candidateHash, attemptID)

		if err := tx.SetAttemptCommentary(attemptID, vd.Commentary); err != nil {
			return err
		}

		validationStatus := string(vd.Classification)
		verdictID, err := tx.RecordVerdict(attemptID, candidateHash, vd.Verdict.Decision, vd.Verdict.Reason, validationStatus)
		if err != nil {
			return err
		}

		if err := emit(t.id, wire.EventKindVerdictRecorded, wire.EvVerdictRecorded{
			VerdictID:        verdictID,
			AttemptID:        attemptID,
			Parsed:           true,
			ValidationStatus: validationStatus,
		}); err != nil {
			return err
		}

		if err := emit(t.id, wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{
			AttemptID:       attemptID,
			Status:          string(res.Status),
			ScrubCount:      res.ScrubCount,
			TransportStatus: res.TransportStatus,
			Usage:           usage,
		}); err != nil {
			return err
		}

		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
			Participant: reviewerID,
			State:       ParticipantIdle,
		}); err != nil {
			return err
		}

		switch vd.Classification {
		case verdict.Valid:
			switch vd.Verdict.Decision {
			case "approve":
				approved = true
				allApprov, err := allReviewersApproved(tx, t.id, candidateHash, t.eff.Preset.RequiredReviewers)
				if err != nil {
					return err
				}
				if allApprov {
					return endTurnIn(tx, emit, t.id, StateApproved, "all_approved")
				}
			case "block":
				blockID, err := tx.OpenBlockFor(verdictID, vd.Verdict.BlockID, candidateHash, vd.Verdict.Severity, vd.Verdict.Evidence, vd.Verdict.Remedy)
				if err != nil {
					return err
				}
				if err := emit(t.id, wire.EventKindBlockOpened, wire.EvBlockOpened{
					BlockID:  blockID,
					Severity: vd.Verdict.Severity,
				}); err != nil {
					return err
				}
				blockOpened = true
				return endTurnIn(tx, emit, t.id, StateIncomplete, "block:"+blockID)
			}
		case verdict.RevisionRequest:
			revisionRequested = true
			return endTurnIn(tx, emit, t.id, StateIncomplete, "revision_request:"+reviewerID)
		case verdict.Rejected:
			failed = true
			return endTurnIn(tx, emit, t.id, StateIncomplete, "reviewer_failed:"+reviewerID+":rejected")
		case verdict.Malformed:
			failed = true
			return endTurnIn(tx, emit, t.id, StateIncomplete, "malformed_twice:"+reviewerID)
		}

		return nil
	})

	if quarantined {
		return
	}
	if err != nil {
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		return
	}

	delete(e.running, reviewerID)
	if approved || blockOpened || revisionRequested || failed {
		e.open = nil
	}
}

// allReviewersApproved checks if all required reviewers have approved the candidate.
func allReviewersApproved(tx *journal.Tx, turnID, candidateHash string, reviewers []string) (bool, error) {
	verdicts, err := tx.ListVerdicts(turnID)
	if err != nil {
		return false, err
	}

	reviewerApprovals := make(map[string]bool)
	for _, v := range verdicts {
		if v.CandidateHash != candidateHash || v.ValidationStatus != string(verdict.Valid) || v.Parsed != "approve" {
			continue
		}

		attempt, err := tx.GetAttempt(v.AttemptID)
		if err != nil {
			return false, err
		}

		reviewerApprovals[attempt.Participant] = true
	}

	for _, revID := range reviewers {
		if !reviewerApprovals[revID] {
			return false, nil
		}
	}
	return true, nil
}
