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
	payload := referee.Payload{
		CallName:   webCall.Name,
		CallURL:    webCall.URL,
		UserInput:  t.input,
		PriorCalls: []referee.WebCall{}, // TODO: populate from turn's prior web calls
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

	return e.dispatchRefereeLocked(t, participant, callID, webCall, req, refEndpoint)
}

// dispatchRefereeLocked issues a synchronous referee attempt with proper
// scheduler and pool management. It is called with e.mu held and returns
// with e.mu held. This integrates the referee dispatch with the shared pool
// to prevent starvation while maintaining synchronous result delivery.
func (e *Engine) dispatchRefereeLocked(t *openTurn, participant string, callID string, webCall referee.WebCall, req transport.CompleteRequest, refEndpoint string) (*RefereeDecision, error) {
	// If the turn is not open, fail fast.
	if e.open != t {
		return &RefereeDecision{
			Verdict: referee.DecisionDeny,
			Reason:  "turn not open",
		}, nil
	}

	// Reserve budget and begin the attempt.
	attemptDeadline := e.attemptDeadline(t)
	maxOut := 64
	if e.roster.Tools != nil && e.roster.Tools.Referee != nil && e.roster.Tools.Referee.MaxOutputTokens != nil {
		maxOut = *e.roster.Tools.Referee.MaxOutputTokens
	}

	var attemptID string
	var epoch int
	var refused *budgetRefusal

	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		epoch = turn.Epoch

		// Estimate the prompt tokens from the request messages.
		estimated := 0
		for _, m := range req.Messages {
			estimated += len(m.Content)
		}

		attemptID, err = tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:                t.id,
			Participant:           participant,
			Phase:                 "referee",
			Epoch:                 epoch,
			Deadline:              &attemptDeadline,
			MaxOutputTokens:       maxOut,
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
			Participant:           participant,
			Phase:                 "referee",
			Epoch:                 int64(epoch),
			Deadline:              attemptDeadline.Format(time.RFC3339Nano),
			MaxOutputTokens:       int64(maxOut),
			EstimatedPromptTokens: int64(estimated),
		})
	})
	if err != nil {
		if refused != nil {
			// Reservation failure ends the turn budget:turn
			e.finishBudget(t, refused)
			return &RefereeDecision{
				Verdict: referee.DecisionDeny,
				Reason:  "referee: budget exhausted",
			}, nil
		}
		return &RefereeDecision{
			Verdict: referee.DecisionDeny,
			Reason:  fmt.Sprintf("referee: begin attempt failed: %v", err),
		}, nil
	}

	// Arm the attempt timer
	e.armAttempt(t, attemptID, attemptDeadline)

	// Call the adapter with the mutex released.
	e.mu.Unlock()
	res, fail := e.complete(t, participant, req)
	e.mu.Lock()

	// Record the result in the journal.
	var wasLate bool
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

		// Track whether the result was late
		if !accepted {
			wasLate = true
		}

		// If the result was not accepted (late result), quarantine it
		if !accepted && fail == nil {
			_, err = e.settleQuarantined(tx, emit, t.id, attemptID, res)
			return err
		}

		if res.Usage != nil {
			if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
				return err
			}
		}

		// Settle the attempt
		if fail != nil {
			if _, err = e.failedIn(tx, emit, participant, attemptID, fail); err != nil {
				return err
			}
		} else {
			if _, err = e.settle(tx, emit, t.id, attemptID, res.Usage, true); err != nil {
				return err
			}
		}

		// Journal the referee decision
		reasonBytes := []byte("")
		verdict := string(referee.DecisionDeny)
		if !wasLate && res.Status == transport.StatusComplete && len(res.ToolCalls) == 0 {
			reasonBytes = []byte(res.Text)
			parsed, err := referee.Parse(res.Text, res.Status, false)
			if err == nil {
				verdict = string(parsed.Decision)
			}
		}

		reasonSHA256, err := e.store.StoreContent(reasonBytes)
		if err != nil {
			return err
		}

		payload := map[string]any{
			"attempt_id":    attemptID,
			"call_id":       callID,
			"verdict":       verdict,
			"reason_sha256": reasonSHA256,
		}
		payloadBytes, _ := json.Marshal(payload)
		return tx.AppendEvent("referee_decision", payloadBytes)
	})

	// If result was late, refuse it
	if wasLate {
		return &RefereeDecision{
			Verdict: referee.DecisionDeny,
			Reason:  "referee: late result quarantined",
		}, nil
	}

	// Parse and return the decision
	if res.Status != transport.StatusComplete || len(res.ToolCalls) > 0 {
		return &RefereeDecision{
			Verdict: referee.DecisionDeny,
			Reason:  fmt.Sprintf("referee: dispatch failed or returned tool calls: %v", res.TransportStatus),
		}, nil
	}

	parsed, err := referee.Parse(res.Text, res.Status, false)
	if err != nil {
		return &RefereeDecision{
			Verdict: referee.DecisionDeny,
			Reason:  fmt.Sprintf("referee: parse failed: %v", err),
		}, nil
	}

	return &RefereeDecision{
		Verdict: parsed.Decision,
		Reason:  parsed.Reason,
	}, nil
}
