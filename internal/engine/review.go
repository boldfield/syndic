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
func (e *Engine) completeAuthorReview(t *openTurn, attemptID string, req transport.CompleteRequest, res transport.CompleteResult, fail *failure) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[t.author].Expected
	gate := transport.CheckResult(req, res, expected)

	var hash, candidateID string
	var round int
	var lines []string
	var quarantined, ended, exceeded bool
	var term *terminal
	var held *hold
	var endReason string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		hash, candidateID, round, lines = "", "", 0, nil
		quarantined, ended, exceeded, term, held = false, false, false, nil, nil
		endReason = ""
		accepted, err := tx.AcceptResult(attemptID, req.Envelope.Epoch, "", outputOf(res))
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			exceeded, err = e.settleQuarantined(tx, emit, t.id, attemptID, res)
			return err
		}
		if exceeded, err = e.settle(tx, emit, t.id, attemptID, res.Usage, true); err != nil {
			return err
		}
		if err := recordCompletion(tx, emit, t.id, t.author, attemptID, res, gate, fail, e.lastInflight(t.author)); err != nil {
			return err
		}
		if held, err = e.failedIn(tx, emit, t.author, attemptID, fail); err != nil {
			return err
		}
		if gate != nil {
			ended = true
			endReason = reasonAuthorFailed + authorFailedStatus(res, gate)
			term, err = e.endIn(tx, emit, t, StateIncomplete, endReason)
			return err
		}
		if e.changeLines != nil {
			lines = e.changeLines(res)
		} else {
			lines = authorChangeLines(res)
		}
		if t.revising && sameRevision(t.candidate, t.changeLines, res.Text, lines) {
			// The author produced nothing new; the human decides with the
			// blocks still open.
			ended = true
			endReason = reasonRevisionUnchanged
			term, err = e.endIn(tx, emit, t, StateIncomplete, endReason)
			return err
		}
		if t.revising {
			round = t.round + 1
		}
		hash, err = candidate.Hash(candidate.Identity{
			ManifestHash:    e.manifestHash,
			TurnID:          t.id,
			AuthorAttemptID: attemptID,
			ContextHash:     t.contextHash,
			Round:           round,
			Content:         []byte(res.Text),
		})
		if err != nil {
			return err
		}
		if t.revising {
			var turn *journal.TurnRow
			if turn, err = tx.GetTurn(t.id); err != nil {
				return err
			}
			candidateID, err = tx.SetLatestCandidate(t.id, res.Text, hash, round, turn.CandidateID)
		} else {
			candidateID, err = tx.FreezeCandidate(t.id, res.Text, hash)
		}
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindCandidateFrozen, wire.EvCandidateFrozen{TurnID: t.id, CandidateHash: hash}); err != nil {
			return err
		}
		return emit(t.id, wire.EventKindTurnState, wire.EvTurnState{TurnID: t.id, State: StateCandidateFrozen})
	})
	e.attemptDone(t, attemptID)
	if err == nil && exceeded {
		e.markExceeded(t.author)
	}
	if err != nil {
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		return false
	}
	if quarantined {
		// The turn already ended; release reports the author idle.
		e.lateCommitted(attemptID, res)
		return false
	}
	e.failedCommitted(t.author, fail, held)
	if e.lastInflight(t.author) {
		delete(e.running, t.author)
	}
	if ended {
		e.commitTerminal(t, term)
		e.launchCancels(term)
		// A revision's author failed or changed nothing: the candidate and its
		// reviews exist, so the terminal state is delivered (none before one).
		e.deliverTerminal(t, StateIncomplete, endReason)
		return false
	}
	t.candidate = res.Text
	t.candidateHash = hash
	t.candidateID = candidateID
	t.changeLines = lines
	if t.revising {
		t.revising = false
		t.round = round
		t.resetRound()
	}
	return true
}

// runReviewers hands every required reviewer's dispatch to the scheduler, in
// order. Each runs when its pool has room and queues otherwise.
func (e *Engine) runReviewers(t *openTurn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, id := range t.required {
		if e.open != t {
			return
		}
		if err := e.schedule(t, id, func() { e.startReviewer(t, id) }); err != nil {
			e.finishLocked(t, StateIncomplete, reasonReviewerFailed+id+":error")
			return
		}
	}
}

// startReviewer begins the reviewer's attempt and calls the adapter on its
// own goroutine. It runs with e.mu held and the pool slot taken.
func (e *Engine) startReviewer(t *openTurn, id string) {
	d, err := e.beginReviewer(t, id, t.reviewersBegun+1 == len(t.required))
	if err != nil {
		if r, ok := asRefusal(err); ok {
			e.finishBudget(t, r)
			e.releaseLocked(id)
			return
		}
		e.finishLocked(t, StateIncomplete, reasonReviewerFailed+id+":error")
		e.releaseLocked(id)
		return
	}
	t.reviewersBegun++
	go e.runReviewer(t, d)
}

// runReviewer calls the reviewer and handles its result. The slot is given
// back when Complete has returned and its result handled. A repair goes back
// through the scheduler, so it queues behind work already waiting in its pool.
func (e *Engine) runReviewer(t *openTurn, d reviewerDispatch) {
	res, fail := e.complete(t, d.participant, d.req)
	repair := e.completeReviewer(t, d, res, fail)

	e.mu.Lock()
	defer e.mu.Unlock()
	e.releaseLocked(d.participant)
	if repair == nil || e.open != t {
		return
	}
	err := e.schedule(t, d.participant, func() { e.startRepair(t, d, *repair) })
	if err != nil {
		e.finishLocked(t, StateIncomplete, fmt.Sprintf("%s%s:error", reasonReviewerFailed, d.participant))
	}
}

// startRepair begins the repair attempt for a malformed verdict and calls the
// adapter on its own goroutine. It runs with e.mu held and the pool slot
// taken.
func (e *Engine) startRepair(t *openTurn, orig reviewerDispatch, original string) {
	var rd *reviewerDispatch
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		var err error
		rd, err = e.beginRepair(tx, emit, t, orig, original)
		return err
	})
	if err != nil {
		if r, ok := asRefusal(err); ok {
			e.finishBudget(t, r)
		} else {
			e.finishLocked(t, StateIncomplete, fmt.Sprintf("%s%s:error", reasonReviewerFailed, orig.participant))
		}
		e.releaseLocked(orig.participant)
		return
	}
	e.markRunning(orig.participant)
	e.armAttempt(t, rd.attemptID, rd.req.Deadline)
	go e.runReviewer(t, *rd)
}

// beginReviewer records the request message and the attempt for one
// reviewer in one transaction. e.mu is held.
func (e *Engine) beginReviewer(t *openTurn, id string, last bool) (reviewerDispatch, error) {
	p := e.participantConfig(id)
	maxOut := budget.ClampOutput(*t.eff.Budgets.ReviewerMaxOutputTokens, e.built.Participants[id].OutputBound)
	deadline := e.attemptDeadline(t)

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
		blocks, err := tx.ReviewerOpenBlocks(t.id, id)
		if err != nil {
			return err
		}
		msgs, err := t.reviewerMessages(attemptID, delimiter, blocks)
		if err != nil {
			return err
		}
		estimated, err := e.reserve(tx, t, id, msgs, maxOut)
		if err != nil {
			return err
		}
		if err := tx.SetAttemptEstimate(attemptID, estimated); err != nil {
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
			AttemptID:             attemptID,
			Participant:           id,
			Phase:                 phaseReview,
			Epoch:                 int64(turn.Epoch),
			Deadline:              deadline.Format(time.RFC3339Nano),
			MaxOutputTokens:       int64(maxOut),
			EstimatedPromptTokens: int64(estimated),
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
	e.markRunning(id)
	e.armAttempt(t, d.attemptID, d.req.Deadline)
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
		_, err = t.reviewerMessages("pending", delimiter, nil)
		if errors.Is(err, prompt.ErrDelimiterCollision) {
			continue
		}
		return delimiter, err
	}
	return "", prompt.ErrDelimiterCollision
}

// beginRepair creates a repair attempt for a malformed verdict and returns
// a reviewerDispatch for it.
func (e *Engine) beginRepair(tx *journal.Tx, emit hub.Emit, t *openTurn, originalDispatch reviewerDispatch, originalOutput string) (*reviewerDispatch, error) {
	p := e.participantConfig(originalDispatch.participant)
	maxOut := budget.ClampOutput(*t.eff.Budgets.ReviewerMaxOutputTokens, e.built.Participants[originalDispatch.participant].OutputBound)
	deadline := e.attemptDeadline(t)

	turn, err := tx.GetTurn(t.id)
	if err != nil {
		return nil, err
	}

	originalAttempt, err := tx.GetAttempt(originalDispatch.attemptID)
	if err != nil {
		return nil, err
	}

	var delimiter string
	if originalAttempt.Delimiter != nil {
		delimiter = *originalAttempt.Delimiter
	}

	repairAttemptID, err := tx.BeginAttemptWith(journal.AttemptParams{
		TurnID:          t.id,
		Participant:     originalDispatch.participant,
		Phase:           phaseReview,
		Epoch:           turn.Epoch,
		Deadline:        &deadline,
		Delimiter:       delimiter,
		MaxOutputTokens: maxOut,
		RepairOf:        originalDispatch.attemptID,
	})
	if err != nil {
		return nil, err
	}
	blocks, err := tx.ReviewerOpenBlocks(t.id, originalDispatch.participant)
	if err != nil {
		return nil, err
	}
	msgs, err := t.repairMessages(originalDispatch.attemptID, delimiter, blocks, originalOutput, repairAttemptID)
	if err != nil {
		return nil, err
	}
	estimated, err := e.reserve(tx, t, originalDispatch.participant, msgs, maxOut)
	if err != nil {
		return nil, err
	}
	if err := tx.SetAttemptEstimate(repairAttemptID, estimated); err != nil {
		return nil, err
	}
	content := []byte(msgs[len(msgs)-1].Content)
	msgID, sum, size, err := tx.AppendMessage(t.id, phaseReview, senderEngine, []string{originalDispatch.participant}, content, nil)
	if err != nil {
		return nil, err
	}
	if err := emit(t.id, wire.EventKindMessageAppended, wire.EvMessageAppended{
		MsgID:   msgID,
		TurnID:  t.id,
		Phase:   phaseReview,
		Sender:  senderEngine,
		SHA256:  sum,
		ByteLen: int64(size),
	}); err != nil {
		return nil, err
	}
	repairOfPtr := &originalDispatch.attemptID
	if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
		AttemptID:             repairAttemptID,
		Participant:           originalDispatch.participant,
		Phase:                 phaseReview,
		Epoch:                 int64(turn.Epoch),
		Deadline:              deadline.Format(time.RFC3339Nano),
		MaxOutputTokens:       int64(maxOut),
		EstimatedPromptTokens: int64(estimated),
		RepairOf:              repairOfPtr,
	}); err != nil {
		return nil, err
	}
	if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
		Participant: originalDispatch.participant,
		State:       ParticipantRunning,
	}); err != nil {
		return nil, err
	}
	return &reviewerDispatch{
		participant: originalDispatch.participant,
		attemptID:   repairAttemptID,
		req: transport.CompleteRequest{
			Envelope: transport.AttemptEnvelope{
				Participant:   originalDispatch.participant,
				TurnID:        t.id,
				Phase:         phaseReview,
				Epoch:         turn.Epoch,
				AttemptID:     repairAttemptID,
				CandidateHash: t.candidateHash,
			},
			Model:           p.Model,
			Messages:        msgs,
			MaxOutputTokens: maxOut,
			Deadline:        deadline,
		},
	}, nil
}

// completeReviewer runs accept, gate, parse and the decision for one
// reviewer's result in one transaction.
func (e *Engine) completeReviewer(t *openTurn, d reviewerDispatch, res transport.CompleteResult, fail *failure) *string {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[d.participant].Expected
	gate := transport.CheckResult(d.req, res, expected)

	var quarantined, approvedNow, exceeded, revise bool
	var endState, endReason, kind string
	var repair bool
	var term *terminal
	var held *hold
	// Everything the closure decides is held in locals and applied to the open
	// turn once the transaction has committed, so a retried or rolled-back
	// transaction leaves the turn's memory as it found it.
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		quarantined, approvedNow, revise, endState, endReason, kind = false, false, false, "", "", ""
		repair, exceeded, term, held = false, false, nil, nil
		accepted, err := tx.AcceptResult(d.attemptID, d.req.Envelope.Epoch, "", outputOf(res))
		if err != nil {
			return err
		}
		if !accepted {
			quarantined = true
			if exceeded, err = e.settleQuarantined(tx, emit, t.id, d.attemptID, res); err != nil {
				return err
			}
			// The slot's release reports the participant idle.
			return emitCompleted(emit, t.id, d.attemptID, res, nil)
		}
		if exceeded, err = e.settle(tx, emit, t.id, d.attemptID, res.Usage, true); err != nil {
			return err
		}
		if err := recordCompletion(tx, emit, t.id, d.participant, d.attemptID, res, gate, fail, e.lastInflight(d.participant)); err != nil {
			return err
		}
		if err := tx.SetAttemptText(d.attemptID, res.Text); err != nil {
			return err
		}
		if held, err = e.failedIn(tx, emit, d.participant, d.attemptID, fail); err != nil {
			return err
		}
		if gate != nil {
			endState, endReason = StateIncomplete, reasonReviewerFailed+d.participant+":"+authorFailedStatus(res, gate)
			term, err = e.endIn(tx, emit, t, endState, endReason)
			return err
		}

		// Parse the verdict with context for v2 validation
		var v2Ctx *verdict.V2Context
		round, err := tx.GetRoundForCandidateHash(t.id, t.candidateHash)
		if err != nil {
			return err
		}
		blockIDs, err := tx.GetReviewerOpenBlockIDs(t.id, d.participant)
		if err != nil {
			return err
		}
		v2Ctx = &verdict.V2Context{
			Round:             round,
			OwnerOpenBlockIDs: blockIDs,
		}

		parsed := verdict.ParseWithContext(res.Text, t.candidateHash, d.attemptID, v2Ctx)
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
		case verdict.Valid, verdict.RevisionRequest:
			// A withdrawal is the reviewer's, recorded against this candidate,
			// and is looked up before a block of this verdict is opened.
			if err := withdrawBlocks(tx, t, d.participant, v.BlockStates); err != nil {
				return err
			}
			switch {
			case parsed.Classification == verdict.RevisionRequest:
				kind = resultRevision
				if !t.loopEnabled() {
					endState, endReason = StateIncomplete, reasonRevision+d.participant
				}
			case v.Decision == "block":
				blockID, err := tx.OpenBlockFor(verdictID, v.BlockID, t.candidateHash, v.Severity, v.Evidence, v.Remedy)
				if err != nil {
					return err
				}
				if err := emit(t.id, wire.EventKindBlockOpened, wire.EvBlockOpened{BlockID: blockID, Severity: v.Severity}); err != nil {
					return err
				}
				kind = resultBlock
				if !t.loopEnabled() {
					endState, endReason = StateIncomplete, reasonBlock+blockID
				}
			default:
				kind = resultApprove
				approvedNow = true
				if e.allApproved(t, d.participant) {
					endState, endReason = StateApproved, reasonAllApproved
				}
			}
			// A loop waits for every required reviewer's accepted result on
			// the candidate before it decides, so the author is sent all the
			// feedback at once.
			if endState == "" && t.loopEnabled() && e.resultsComplete(t, d.participant) {
				if t.roundsRemain() {
					revise = true
				} else {
					endState, endReason = StateIncomplete, reasonRoundsExhausted
				}
			}
		case verdict.Rejected:
			endState, endReason = StateIncomplete, reasonReviewerFailed+d.participant+":rejected"
		case verdict.Malformed:
			attempt, err := tx.GetAttempt(d.attemptID)
			if err != nil {
				return err
			}
			if attempt.RepairOf != nil {
				endState, endReason = StateIncomplete, reasonMalformed+d.participant
			} else {
				repair = true
			}
		}
		if endState == "" {
			return nil
		}
		term, err = e.endIn(tx, emit, t, endState, endReason)
		return err
	})
	e.attemptDone(t, d.attemptID)
	if err == nil && exceeded {
		e.markExceeded(d.participant)
	}
	if err != nil {
		e.finishLocked(t, StateIncomplete, fmt.Sprintf("%s%s:error", reasonReviewerFailed, d.participant))
		return nil
	}
	if quarantined {
		e.lateCommitted(d.attemptID, res)
		return nil
	}
	e.failedCommitted(d.participant, fail, held)
	if e.lastInflight(d.participant) {
		delete(e.running, d.participant)
	}
	if t.outputs == nil {
		t.outputs = map[string]string{}
	}
	t.outputs[d.attemptID] = res.Text
	if repair {
		return &res.Text
	}
	if approvedNow {
		t.approved[d.participant] = true
	}
	if kind != "" {
		if t.results == nil {
			t.results = map[string]reviewResult{}
		}
		t.results[d.participant] = reviewResult{kind: kind, attemptID: d.attemptID, text: res.Text}
	}
	if endState != "" {
		e.commitTerminal(t, term)
		e.launchCancels(term)
		e.deliverTerminal(t, endState, endReason)
		return nil
	}
	if revise {
		e.startRevision(t)
	}
	return nil
}

// resultsComplete reports whether every required reviewer has an accepted
// result on the current candidate, counting the one just recorded.
func (e *Engine) resultsComplete(t *openTurn, just string) bool {
	for _, id := range t.required {
		if _, ok := t.results[id]; id != just && !ok {
			return false
		}
	}
	return true
}

// withdrawBlocks records, against the current candidate, each of the
// reviewer's open blocks its verdict marks withdrawn. A block it maintains
// stays open.
func withdrawBlocks(tx *journal.Tx, t *openTurn, participant string, states []verdict.BlockState) error {
	if len(states) == 0 {
		return nil
	}
	open, err := tx.ReviewerOpenBlocks(t.id, participant)
	if err != nil {
		return err
	}
	for _, st := range states {
		if st.State != "withdrawn" {
			continue
		}
		for _, b := range open {
			if b.ReviewerBlockID == st.BlockID {
				if err := tx.WithdrawBlock(b.ID, participant, t.candidateHash); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
// emits attempt_completed and the participant going idle, or into the state a
// failed dispatch sets.
func recordCompletion(tx *journal.Tx, emit hub.Emit, turnID, participant, attemptID string, res transport.CompleteResult, gate error, fail *failure, idle bool) error {
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
	if !idle {
		return nil
	}
	return emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: participant, State: fail.participantState()})
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
