package engine

import (
	"errors"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
	"github.com/boldfield/syndic/internal/workspace"
)

// runTurn hands the turn's author dispatch to the scheduler. The dispatch
// runs at once when the author's pool has a free slot and queues otherwise.
func (e *Engine) runTurn(t *openTurn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open != t {
		return
	}
	if err := e.schedule(t, t.author, func() { e.startAuthor(t) }); err != nil {
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
	}
}

// startAuthor begins the author's attempt and calls the adapter on its own
// goroutine. It runs with e.mu held and the pool slot taken.
func (e *Engine) startAuthor(t *openTurn) {
	req, attemptID, err := e.dispatchAuthor(t)
	if err != nil {
		if r, ok := asRefusal(err); ok {
			e.finishBudget(t, r)
			e.releaseLocked(t.author)
			return
		}
		// Nothing was written for the dispatch; end the turn so the engine
		// does not stay busy.
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		e.releaseLocked(t.author)
		return
	}
	go e.runAuthor(t, req, attemptID)
}

// runAuthor calls the author, handles its result and gives back the pool slot
// before anything else is dispatched. An accepted tool step keeps the slot
// while its calls run and the next author attempt is dispatched.
func (e *Engine) runAuthor(t *openTurn, req transport.CompleteRequest, attemptID string) {
	res, fail := e.complete(t, t.author, req)
	var proceed, step bool
	if t.mode != modeSolo {
		proceed, step = e.completeAuthorReview(t, attemptID, req, res, fail)
	} else {
		step = e.completeAuthor(t, attemptID, req, res, fail)
	}
	if step {
		e.executeToolStep(t, req, attemptID, res)
		return
	}
	e.release(t.author)
	if proceed {
		e.runReviewers(t)
	}
}

// executeToolStep runs the author's accepted tool step with the author's pool
// slot still taken and e.mu not held: every call's intent is journaled, then
// each is checked and its effect run in order, re-checking the turn before
// each effect. It then hands the next author attempt, carrying the results,
// to the scheduler, or ends the turn when the step could not run. A turn that
// ended while the step ran (a cancel, the turn deadline) has had the rest of
// the step abandoned by its re-checks, and nothing more is dispatched.
func (e *Engine) executeToolStep(t *openTurn, req transport.CompleteRequest, attemptID string, res transport.CompleteResult) {
	e.mu.Lock()
	remaining := e.authorCallsRemaining(t)
	hook := e.beforeEffect
	var patterns []string
	if e.roster != nil && e.roster.Tools != nil {
		patterns = e.roster.Tools.ProtectedPaths
	}
	e.mu.Unlock()

	out, err := e.performToolStep(t, t.author, req, attemptID, res, remaining, patterns, hook)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open != t {
		e.releaseLocked(t.author)
		return
	}
	switch {
	case errors.Is(err, errToolStepIDs):
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"tool_call_ids")
	case err != nil:
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
	case out.ended != "":
		// The step ended but the turn is still open here, which only an epoch
		// moved outside a terminal path could cause; the results of a step
		// that ended are never dispatched.
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"tool_step:"+out.ended)
	default:
		t.toolCallsUsed += out.used
		e.releaseLocked(t.author)
		if err := e.schedule(t, t.author, func() { e.startAuthorStep(t, req, res, out) }); err != nil {
			e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		}
		return
	}
	e.releaseLocked(t.author)
}

// performToolStep executes the step of caller, the author or a required
// reviewer, over the conversation's confined workspace, opened for this step
// and closed after it. The effects are the caller's: a web query is decided
// by the referee of the caller's endpoint and accounted to the caller. e.mu
// is not held.
func (e *Engine) performToolStep(t *openTurn, caller string, req transport.CompleteRequest, attemptID string, res transport.CompleteResult, remaining int, patterns []string, hook func(turnID, attemptID, name string)) (toolStepOutcome, error) {
	var fs *workspace.Confined
	if t.workspace != "" {
		var err error
		if fs, err = workspace.OpenConfined(t.workspace, patterns); err != nil {
			return toolStepOutcome{}, err
		}
		defer func() { _ = fs.Close() }()
		fs.SetAfterComponent(e.afterWalk)
	}
	effects := e.toolEffects(fs, t, caller, attemptID, hook)
	return runToolStep(e.store, toolStep{
		attemptID: attemptID,
		calls:     res.ToolCalls,
		granted:   req.Tools,
		effects:   effects,
		remaining: remaining,
	})
}

// startAuthorStep begins the author attempt that follows an executed tool
// step, carrying the step and its results, and calls the adapter on its own
// goroutine. It runs with e.mu held and the pool slot taken.
func (e *Engine) startAuthorStep(t *openTurn, prior transport.CompleteRequest, res transport.CompleteResult, out toolStepOutcome) {
	var (
		req       transport.CompleteRequest
		attemptID string
		msgs      []transport.Message
		request   string
	)
	defs, limited, err := e.authorToolDefs(t)
	if err == nil {
		msgs, request, err = e.toolStepMessages(prior, res, out, limited)
	}
	if err == nil {
		state := StateAuthorDispatched
		if t.revising {
			state = StateRevisionDispatched
		}
		t.toolLimited = limited
		// A continuation is a later dispatch of a round already started: it
		// is checked by reservation alone, never by the round's projection.
		req, attemptID, err = e.dispatchAuthorWith(t, msgs, defs, request, state, senderEngine, false)
	}
	if err != nil {
		if r, ok := asRefusal(err); ok {
			e.finishBudget(t, r)
		} else {
			e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		}
		e.releaseLocked(t.author)
		return
	}
	go e.runAuthor(t, req, attemptID)
}

// returned records that the attempt's Complete call has returned. A hold on
// the attempt then occupies a slot of its own.
func (e *Engine) returned(attemptID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.calling, attemptID)
	delete(e.cancelled, attemptID)
	if h := e.holds[attemptID]; h != nil {
		h.calling = false
	}
}

func (e *Engine) adapterFor(id string) transport.Adapter {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.adapters[id]
}

// adapterForAttempt is the adapter a held attempt was sent through: the one
// its hold carries, a referee's, or else its participant's.
func (e *Engine) adapterForAttempt(h *hold) transport.Adapter {
	if h.adapter != nil {
		return h.adapter
	}
	return e.adapterFor(h.participant)
}

// dispatchAuthor records the request message and the first author attempt
// of the round, and moves the turn to author_dispatched or
// revision_dispatched, all in one transaction. e.mu is held.
func (e *Engine) dispatchAuthor(t *openTurn) (transport.CompleteRequest, string, error) {
	var msgs []transport.Message
	state, request := StateAuthorDispatched, t.input
	if t.revising {
		var err error
		if msgs, err = e.authorRevisionMessages(t); err != nil {
			return transport.CompleteRequest{}, "", err
		}
		// The journaled request is the feedback message, as a reviewer's is
		// its fenced prompt.
		state, request = StateRevisionDispatched, msgs[len(msgs)-1].Content
	} else {
		msgs = prompt.AuthorWithHistory(t.history, t.input)
	}
	defs, limited, err := e.authorToolDefs(t)
	if err != nil {
		return transport.CompleteRequest{}, "", err
	}
	if limited {
		msgs = append(msgs, transport.Message{Role: "user", Content: toolLimitStatement})
	}
	t.toolLimited = limited
	return e.dispatchAuthorWith(t, msgs, defs, request, state, senderUser, t.revising)
}

// dispatchAuthorWith records the request message, from sender to the author,
// and the attempt carrying msgs and the tool definitions defs, reserves its
// budget and emits the turn state, all in one transaction. roundStart marks
// the first author attempt of a revision round, which also requires the
// round's projection to fit what is left of the turn budget. e.mu is held.
func (e *Engine) dispatchAuthorWith(t *openTurn, msgs []transport.Message, defs []transport.Tool, request, state, sender string, roundStart bool) (transport.CompleteRequest, string, error) {
	var p config.Participant
	for _, candidate := range e.roster.Participants {
		if candidate.ID == t.author {
			p = candidate
			break
		}
	}
	maxOut := budget.ClampOutput(*t.eff.Budgets.AuthorMaxOutputTokens, e.built.Participants[t.author].OutputBound)
	deadline := e.attemptDeadline(t)

	var req transport.CompleteRequest
	var attemptID string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		if roundStart {
			if err := e.checkRoundFits(tx, t, msgs, defs, maxOut); err != nil {
				return err
			}
		}
		estimated, err := e.reserve(tx, t, t.author, msgs, defs, maxOut)
		if err != nil {
			return err
		}
		msgID, sum, size, err := tx.AppendMessage(t.id, phaseAuthor, sender, []string{t.author}, []byte(request), nil)
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindMessageAppended, wire.EvMessageAppended{
			MsgID:   msgID,
			TurnID:  t.id,
			Phase:   phaseAuthor,
			Sender:  sender,
			SHA256:  sum,
			ByteLen: int64(size),
		}); err != nil {
			return err
		}
		attemptID, err = tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:                t.id,
			Participant:           t.author,
			Phase:                 phaseAuthor,
			Epoch:                 turn.Epoch,
			Deadline:              &deadline,
			MaxOutputTokens:       maxOut,
			EstimatedPromptTokens: estimated,
		})
		if err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
			AttemptID:             attemptID,
			Participant:           t.author,
			Phase:                 phaseAuthor,
			Epoch:                 int64(turn.Epoch),
			Deadline:              deadline.Format(time.RFC3339Nano),
			MaxOutputTokens:       int64(maxOut),
			EstimatedPromptTokens: int64(estimated),
		}); err != nil {
			return err
		}
		if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
			Participant: t.author,
			State:       ParticipantRunning,
		}); err != nil {
			return err
		}
		if err := emit(t.id, wire.EventKindTurnState, wire.EvTurnState{TurnID: t.id, State: state}); err != nil {
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
			Tools:           defs,
			MaxOutputTokens: maxOut,
			Deadline:        deadline,
		}
		return nil
	})
	if err != nil {
		return transport.CompleteRequest{}, "", err
	}
	e.markRunning(t.author)
	e.armAttempt(t, attemptID, deadline)
	return req, attemptID, nil
}

// completeAuthor runs accept, gate and the terminal path for the author's
// result in one transaction. It reports whether the result is an accepted
// tool step whose calls the caller now executes; the turn then stays open.
func (e *Engine) completeAuthor(t *openTurn, attemptID string, req transport.CompleteRequest, res transport.CompleteResult, fail *failure) (step bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[t.author].Expected
	gate := transport.CheckResult(req, res, expected)
	isToolStep := gate == nil && transport.IsToolStep(res)

	quarantined, exceeded := false, false
	var term *terminal
	var held *hold
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		quarantined, exceeded, term, held, step = false, false, nil, nil, false
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
			exceeded, err = e.settleQuarantined(tx, emit, t.id, attemptID, res)
			return err
		}

		var usage *wire.ReportedUsage
		if res.Usage != nil {
			if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
				return err
			}
			promptTokens, completionTokens := int64(res.Usage.Prompt), int64(res.Usage.Completion)
			usage = &wire.ReportedUsage{PromptTokens: &promptTokens, CompletionTokens: &completionTokens}
		}
		if exceeded, err = e.settle(tx, emit, t.id, attemptID, res.Usage, true); err != nil {
			return err
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
		if e.lastInflight(t.author) {
			if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{
				Participant: t.author,
				State:       fail.participantState(),
			}); err != nil {
				return err
			}
		}
		if held, err = e.failedIn(tx, emit, t.author, attemptID, fail); err != nil {
			return err
		}
		if isToolStep {
			// Tool step accepted but not parsed; text is journaled for the
			// transcript. Its calls run after this transaction commits.
			if err = tx.SetAttemptText(attemptID, res.Text); err != nil {
				return err
			}
			if reason := e.toolStepRefusal(t, req); reason != "" {
				term, err = e.endIn(tx, emit, t, StateIncomplete, reason)
			} else {
				step = true
			}
		} else if gate == nil {
			// The solo answer is not a candidate; keep its text so later
			// turns' history can carry it.
			if err = tx.SetAttemptText(attemptID, res.Text); err != nil {
				return err
			}
			term, err = e.endIn(tx, emit, t, StateSoloComplete, reasonSoloOK)
		} else {
			term, err = e.endIn(tx, emit, t, StateIncomplete, reasonAuthorFailed+authorFailedStatus(res, gate))
		}
		return err
	})
	e.attemptDone(t, attemptID)
	if err == nil && exceeded {
		e.markExceeded(t.author)
	}
	if quarantined {
		if err == nil {
			e.lateCommitted(attemptID, res)
		}
		return false
	}
	if err != nil {
		// The transaction left no durable change. End the turn on its own so
		// the engine does not stay busy; if that fails too, the turn stays
		// open in memory exactly as it is in the journal.
		e.finishLocked(t, StateIncomplete, reasonAuthorFailed+"error")
		return false
	}
	e.failedCommitted(t.author, fail, held)
	if e.lastInflight(t.author) {
		delete(e.running, t.author)
	}
	if step {
		return true
	}
	e.commitTerminal(t, term)
	e.launchCancels(term)
	return false
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
