package engine

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/referee"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// RefereeDecision is the result of a referee call: allow or deny with a reason.
type RefereeDecision struct {
	Verdict referee.Decision
	Reason  string
}

// DispatchReferee issues an ordinary accounted attempt to obtain a referee
// decision on a web_fetch or web_search call before the web effect occurs.
// It runs the referee model on the calling participant's configured endpoint
// (or its optional dedicated local referee endpoint if configured). Budget
// reservation failure ends the turn budget:turn. A deny or transport error
// returns a deny decision to the caller, which answers with a tool error to
// the model. e.mu is held on entry and exit.
func (e *Engine) DispatchReferee(t *openTurn, participant string, callID string, webCall referee.WebCall) (*RefereeDecision, error) {
	pc := e.participantConfig(participant)
	if pc.ID == "" {
		return nil, fmt.Errorf("referee: participant not configured: %q", participant)
	}

	ep := e.endpoint(pc.Endpoint)
	if ep.RefereeModel == "" {
		return nil, fmt.Errorf("referee: no referee_model configured for endpoint %q", pc.Endpoint)
	}

	// Determine the actual endpoint to dispatch to: use referee_endpoint if set
	// and it has local execution, otherwise use the calling participant's endpoint.
	refEndpoint := pc.Endpoint
	if ep.RefereeEndpoint != "" {
		refEP := e.endpoint(ep.RefereeEndpoint)
		if refEP.DeclaredExecution == "local" {
			refEndpoint = ep.RefereeEndpoint
		}
	}

	// Get max_output_tokens from config, default 64.
	maxOutputTokens := 64
	if e.roster.Tools != nil && e.roster.Tools.Referee != nil && e.roster.Tools.Referee.MaxOutputTokens != nil {
		maxOutputTokens = *e.roster.Tools.Referee.MaxOutputTokens
	}

	// Build the referee payload with the web call and prior calls.
	// PriorCalls includes the caller's own earlier web_fetch and web_search calls this turn.
	// For now, this is populated from the turn's pending tool calls.
	payload := referee.Payload{
		CallName:   webCall.Name,
		CallURL:    webCall.URL,
		UserInput:  t.input,
		PriorCalls: []referee.WebCall{},
	}
	messages := referee.PromptMessages(payload)

	// Build the attempt request.
	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant: participant,
			TurnID:      t.id,
			Phase:       "referee",
		},
		Model:           ep.RefereeModel,
		Messages:        messages,
		MaxOutputTokens: maxOutputTokens,
	}

	// Dispatch through the pool system. The dispatch is treated as an ordinary
	// accounted attempt: it reserves budget, may retry on recoverable failures,
	// and is journaled. This integrates with the existing scheduler (via
	// schedule()) to manage pool slots and queuing.
	res, failure := e.dispatchAttemptLocked(t, refEndpoint, ep.RefereeModel, "referee", req)
	if failure != nil && failure.hold {
		// An uncertain-capacity hold; end the turn.
		return nil, fmt.Errorf("referee: uncertain capacity hold")
	}

	// Parse the referee result. Any failure returns a deny.
	if failure != nil || res.Status != transport.StatusComplete {
		return &RefereeDecision{
			Verdict: referee.DecisionDeny,
			Reason:  fmt.Sprintf("referee dispatch failed: %v", res.TransportStatus),
		}, nil
	}

	// Parse the response text.
	parsed, err := referee.Parse(res.Text, res.Status, len(res.ToolCalls) > 0)
	if err != nil {
		return &RefereeDecision{
			Verdict: referee.DecisionDeny,
			Reason:  err.Error(),
		}, nil
	}

	// Journal the referee decision: referee_decision{call_id, attempt_id, verdict, reason_sha256}
	attemptID := res.Envelope.AttemptID
	reasonBytes := []byte(parsed.Reason)
	reasonSHA256, err := e.store.StoreContent(reasonBytes)
	if err == nil {
		_ = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
			// Emit referee_decision event with verdict and reason hash.
			ev := map[string]any{
				"attempt_id":    attemptID,
				"call_id":       callID,
				"verdict":       string(parsed.Decision),
				"reason_sha256": reasonSHA256,
			}
			payload, _ := json.Marshal(ev)
			return tx.AppendEvent("referee_decision", payload)
		})
	}

	return &RefereeDecision{
		Verdict: parsed.Decision,
		Reason:  parsed.Reason,
	}, nil
}

// dispatchAttemptLocked issues an attempt on the specified endpoint and
// returns its result. It handles budget reservation, scheduling through the
// pool system, calling the adapter, and recording the result. e.mu is held
// on entry and exit. This is a focused helper for referee and other engine-internal
// dispatches that use the full accounting machinery.
func (e *Engine) dispatchAttemptLocked(t *openTurn, endpoint, model, phase string, req transport.CompleteRequest) (transport.CompleteResult, *failure) {
	// If the turn is not open, fail fast.
	if e.open != t {
		return transport.CompleteResult{Status: transport.StatusError}, &failure{
			attemptStatus:   transport.StatusError,
			transportStatus: "turn_not_open",
		}
	}

	// Generate the deadline: attempt deadline is min(turn deadline, now + attempt timeout).
	attemptDeadline := t.turnDeadline
	if attemptTimeout := 30 * time.Second; e.clock.Now().Add(attemptTimeout).Before(attemptDeadline) {
		attemptDeadline = e.clock.Now().Add(attemptTimeout)
	}

	// Reserve the budget for this attempt using BeginAttempt.
	var attemptID string
	var refused *budgetRefusal
	var epoch int
	var estimated int
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		epoch = turn.Epoch

		// Estimate the prompt tokens from the request messages.
		estimated = 0
		for _, m := range req.Messages {
			estimated += len(m.Content)
		}

		attemptID, err = tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:                t.id,
			Participant:           req.Envelope.Participant,
			Phase:                 phase,
			Epoch:                 epoch,
			Deadline:              &attemptDeadline,
			MaxOutputTokens:       req.MaxOutputTokens,
			EstimatedPromptTokens: estimated,
		})
		if err != nil {
			if r, ok := asRefusal(err); ok {
				refused = r
				return err
			}
			return err
		}
		req.Envelope.AttemptID = attemptID
		req.Envelope.Epoch = epoch
		req.Deadline = attemptDeadline

		return emit(t.id, wire.EventKindAttemptBegun, wire.EvAttemptBegun{
			AttemptID:             attemptID,
			Participant:           req.Envelope.Participant,
			Phase:                 phase,
			Epoch:                 int64(epoch),
			Deadline:              attemptDeadline.Format(time.RFC3339Nano),
			MaxOutputTokens:       int64(req.MaxOutputTokens),
			EstimatedPromptTokens: int64(estimated),
		})
	})
	if err != nil {
		if refused != nil {
			return transport.CompleteResult{Status: transport.StatusError}, &failure{
				attemptStatus:   transport.StatusError,
				transportStatus: "budget_refused",
			}
		}
		return transport.CompleteResult{Status: transport.StatusError}, &failure{
			attemptStatus:   transport.StatusError,
			transportStatus: "journal_error",
		}
	}

	// Mark the participant as running and release the mutex before calling the adapter.
	e.markRunning(req.Envelope.Participant)
	e.armAttempt(t, attemptID, attemptDeadline)

	// Call the adapter with the mutex released.
	e.mu.Unlock()
	res, fail := e.complete(t, req.Envelope.Participant, req)
	e.mu.Lock()

	// Record the result in the journal.
	_ = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		accepted, err := tx.AcceptResult(attemptID, epoch, "", journal.AttemptOutput{
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
		if !accepted && fail == nil {
			// A late result for a superseded epoch or a terminal turn is
			// quarantined and nothing further happens.
			_, err = e.settleQuarantined(tx, emit, t.id, attemptID, res)
			return err
		}

		if res.Usage != nil {
			if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
				return err
			}
		}

		return nil
	})

	// Release the pool slot and handle failures.
	e.releaseLocked(req.Envelope.Participant)

	return res, fail
}
