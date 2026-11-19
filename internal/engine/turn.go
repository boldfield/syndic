package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
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
// before anything else is dispatched.
func (e *Engine) runAuthor(t *openTurn, req transport.CompleteRequest, attemptID string) {
	res := e.complete(t.author, req)
	if t.mode != modeSolo {
		proceed := e.completeAuthorReview(t, attemptID, req, res)
		e.release(t.author)
		if proceed {
			e.runReviewers(t)
		}
		return
	}
	e.completeAuthor(t, attemptID, req, res)
	e.release(t.author)
}

// complete calls the participant's adapter and turns a Go error into an
// error result.
func (e *Engine) complete(participant string, req transport.CompleteRequest) transport.CompleteResult {
	res, err := e.adapterFor(participant).Complete(context.Background(), req)
	if err != nil {
		res = transport.CompleteResult{
			Envelope:        req.Envelope,
			Status:          transport.StatusError,
			TransportStatus: "adapter_error:" + err.Error(),
		}
	}
	return res
}

func (e *Engine) adapterFor(id string) transport.Adapter {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.adapters[id]
}

// dispatchAuthor records the request message and the attempt, and moves the
// turn to author_dispatched, all in one transaction. e.mu is held.
func (e *Engine) dispatchAuthor(t *openTurn) (transport.CompleteRequest, string, error) {
	var p config.Participant
	for _, candidate := range e.roster.Participants {
		if candidate.ID == t.author {
			p = candidate
			break
		}
	}

	msgs := prompt.Author(t.input)
	maxOut := budget.ClampOutput(*t.eff.Budgets.AuthorMaxOutputTokens, e.built.Participants[t.author].OutputBound)
	deadline := e.attemptDeadline(t)

	var req transport.CompleteRequest
	var attemptID string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		estimated, err := e.reserve(tx, t, t.author, msgs, maxOut)
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
	e.armAttempt(t, attemptID, deadline)
	return req, attemptID, nil
}

// completeAuthor runs accept, gate and the terminal path for the author's
// result in one transaction.
func (e *Engine) completeAuthor(t *openTurn, attemptID string, req transport.CompleteRequest, res transport.CompleteResult) {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.built.Participants[t.author].Expected
	gate := transport.CheckResult(req, res, expected)

	quarantined, exceeded := false, false
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		quarantined, exceeded = false, false
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
				State:       ParticipantIdle,
			}); err != nil {
				return err
			}
		}
		if gate == nil {
			return e.endTurnTerminalPath(tx, emit, t, StateSoloComplete, reasonSoloOK, false, []string{})
		}
		return e.endTurnTerminalPath(tx, emit, t, StateIncomplete, reasonAuthorFailed+authorFailedStatus(res, gate), false, []string{})
	})
	e.attemptDone(t, attemptID)
	if err == nil && exceeded {
		e.markExceeded(t.author)
	}
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
	if e.lastInflight(t.author) {
		delete(e.running, t.author)
	}
	e.open = nil
	e.turnEnded(t)
}

// finishLocked is finishTurn with e.mu held. It ends the turn, opens holds
// for in-flight attempts, and calls adapter.Cancel() on them.
func (e *Engine) finishLocked(t *openTurn, state, reason string) {
	inflight := e.getInflightAttempts(t)
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return e.endTurnTerminalPath(tx, emit, t, state, reason, false, inflight)
	})
	if err != nil {
		return
	}

	if e.open == t {
		e.open = nil
		e.turnEnded(t)
	}

	// Call cancelInflightAttempts without holding the lock to avoid deadlock
	e.mu.Unlock()
	e.cancelInflightAttempts(inflight)
	e.mu.Lock()
}

// getInflightAttempts returns all in-flight attempts for a turn.
// In-flight means Complete has been called but result not recorded.
func (e *Engine) getInflightAttempts(t *openTurn) []string {
	var ids []string
	for id := range t.pending {
		ids = append(ids, id)
	}
	return ids
}

// getPoolMembers returns the {endpoint_name, model} pairs for all participants in a pool.
func (e *Engine) getPoolMembers(poolName string) []journal.GroupMember {
	var members []journal.GroupMember
	seen := make(map[string]bool)
	for _, p := range e.roster.Participants {
		if e.poolOf(p.ID) == poolName {
			ep := e.endpointFor(p.ID)
			key := ep.Name + ":" + p.Model
			if !seen[key] {
				seen[key] = true
				members = append(members, journal.GroupMember{
					EndpointName: ep.Name,
					Model:        p.Model,
				})
			}
		}
	}
	return members
}

func (e *Engine) endpointFor(participantID string) config.EndpointProfile {
	for _, p := range e.roster.Participants {
		if p.ID == participantID {
			for _, ep := range e.roster.Endpoints {
				if ep.Name == p.Endpoint {
					return ep
				}
			}
		}
	}
	return config.EndpointProfile{}
}

// endTurnTerminalPath runs the terminal path for ending a turn with holds and cancellations.
func (e *Engine) endTurnTerminalPath(tx *journal.Tx, emit hub.Emit, t *openTurn, state, reason string, isCancellation bool, inflightIDs []string) error {

	// 1. Bump epoch so no later result is accepted
	_, err := tx.BumpEpoch(t.id)
	if err != nil {
		return err
	}

	// 2. For each in-flight attempt, set cancellation and open hold
	for _, attemptID := range inflightIDs {
		if err := tx.SetCancellation(attemptID, "requested"); err != nil {
			return err
		}

		// Get the attempt's info to find its pool and endpoint
		attempt, err := tx.GetAttempt(attemptID)
		if err != nil {
			return err
		}

		participantConfig := e.participantConfig(attempt.Participant)
		poolName := e.poolOf(attempt.Participant)
		members := e.getPoolMembers(poolName)

		// Open the hold
		hold := journal.Hold{
			AttemptID:    attemptID,
			Pool:         poolName,
			EndpointName: e.endpointFor(attempt.Participant).Name,
			Model:        participantConfig.Model,
			Group:        members,
			HeldSince:    e.clock.Now(),
		}
		if err := tx.OpenHold(hold); err != nil {
			return err
		}

		// Emit capacity event: uncertain
		if err := emit(t.id, wire.EventKindCapacity, wire.EvCapacity{
			Pool:          poolName,
			HeldByAttempt: attemptID,
			State:         "uncertain",
		}); err != nil {
			return err
		}
	}

	// 3. If this is a cancellation, set phase_at_cancel to the turn's current phase.
	// Infer phase from inflight attempts or turn state.
	if isCancellation {
		var phase string

		// Try to get phase from inflight attempts
		if len(inflightIDs) > 0 {
			attempt, err := tx.GetAttempt(inflightIDs[0])
			if err == nil {
				phase = attempt.Phase
			}
		}

		// If no inflight or couldn't determine from attempts, infer from turn state
		if phase == "" {
			turn, err := tx.GetTurn(t.id)
			if err == nil {
				switch turn.TerminalState {
				case StateAuthorDispatched:
					phase = phaseAuthor
				case StateReviewersDispatched, StateApproved:
					phase = phaseReview
				}
			}
		}

		// If still no phase, derive from turn structure (what's been dispatched)
		if phase == "" {
			// If author has been dispatched/completed, we're at least in author phase
			// If reviewers have been started, we're in review phase
			if len(t.required) > 0 && t.reviewersBegun > 0 {
				phase = phaseReview
			} else {
				phase = phaseAuthor
			}
		}

		if phase != "" {
			if err := tx.SetPhaseAtCancel(t.id, phase); err != nil {
				return err
			}
		}
	}

	// 4. End turn and emit state
	if err := tx.EndTurn(t.id, state, reason); err != nil {
		return err
	}

	return emit(t.id, wire.EventKindTurnState, wire.EvTurnState{TurnID: t.id, State: state, Reason: &reason})
}

// cancelInflightAttempts calls Adapter.Cancel for each in-flight attempt. It must
// be called WITHOUT holding e.mu. The function acquires the lock only when needed
// for transactions, not for the adapter.Cancel calls.
func (e *Engine) cancelInflightAttempts(inflightIDs []string) {
	// First pass: collect adapters without holding lock for long
	type adapterInfo struct {
		attemptID   string
		adapter     transport.Adapter
		participant string
	}
	var infos []adapterInfo

	e.mu.Lock()
	for _, attemptID := range inflightIDs {
		attempt, err := e.store.GetAttempt(attemptID)
		if err != nil {
			continue
		}
		adapter := e.adapters[attempt.Participant]
		infos = append(infos, adapterInfo{attemptID, adapter, attempt.Participant})
	}
	e.mu.Unlock()

	// Second pass: call adapters without lock
	for _, info := range infos {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result, _ := info.adapter.Cancel(ctx, info.attemptID)
		cancel()

		e.mu.Lock()
		if result.ServerConfirmed == transport.TriYes {
			// Server confirmed the cancellation, release the hold
			_ = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
				if err := tx.SetCancellation(info.attemptID, "confirmed"); err != nil {
					return err
				}
				// Query the hold to get the pool name
				holds, err := tx.UnreleasedHolds()
				if err != nil {
					return err
				}
				var poolName string
				for _, h := range holds {
					if h.AttemptID == info.attemptID {
						poolName = h.Pool
						break
					}
				}
				if err := tx.ReleaseHold(info.attemptID, "server_confirmed", ""); err != nil {
					return err
				}
				if poolName != "" {
					return emit("", wire.EventKindCapacity, wire.EvCapacity{
						Pool:          poolName,
						HeldByAttempt: info.attemptID,
						State:         "released",
						How:           strPtr("server_confirmed"),
					})
				}
				return nil
			})
			// Server confirmed: release and go back to running (or idle if Complete returned)
			delete(e.cancelling, info.participant)
			delete(e.unknown, info.participant)
		} else {
			// Server didn't confirm, mark as unknown (hold stays unreleased)
			_ = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
				return tx.SetCancellation(info.attemptID, "unknown")
			})
			// Update in-memory state to unknown
			delete(e.cancelling, info.participant)
			e.unknown[info.participant] = true
		}
		e.mu.Unlock()
	}
}

// endTurnIn is a simpler terminal path for use when no holds/cancellations needed.
// Deprecated: use endTurnTerminalPath instead.
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
