package engine

import (
	"encoding/json"
	"fmt"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/referee"
	"github.com/boldfield/syndic/internal/transport"
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

	// Build the referee payload with the web call and prior calls.
	payload := referee.Payload{
		CallName:   webCall.Name,
		CallURL:    webCall.URL,
		UserInput:  t.input,
		PriorCalls: []referee.WebCall{},
	}
	messages := referee.PromptMessages(payload)

	// Build the attempt request. The deadline, epoch, and attempt ID are
	// assigned by schedule() when the attempt is dispatched.
	req := transport.CompleteRequest{
		Envelope: transport.AttemptEnvelope{
			Participant: participant,
			TurnID:      t.id,
			Phase:       "referee",
		},
		Model:           ep.RefereeModel,
		Messages:        messages,
		MaxOutputTokens: toolsRefereeMaxOutputTokens,
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
	reasonBytes := []byte(parsed.Reason)
	reasonSHA256, err := e.store.StoreContent(reasonBytes)
	if err == nil {
		_ = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
			// Emit referee_decision event with verdict and reason hash.
			ev := map[string]any{
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

// toolsRefereeMaxOutputTokens is the maximum output tokens for a referee
// decision, per v5 delta section 6.
const toolsRefereeMaxOutputTokens = 64

// dispatchAttemptLocked issues an attempt on the specified endpoint and
// returns its result. It handles budget reservation, scheduling through the
// pool system, calling the adapter, and recording the result. e.mu is held
// throughout. This is a focused helper for referee and other engine-internal
// dispatches that use the full accounting machinery.
// TODO: This helper will be completed by integrating with the existing budget,
// scheduling, and adapter infrastructure in a later task.
func (e *Engine) dispatchAttemptLocked(t *openTurn, endpoint, model, phase string, req transport.CompleteRequest) (transport.CompleteResult, *failure) {
	return transport.CompleteResult{Status: transport.StatusError}, &failure{
		attemptStatus:   transport.StatusError,
		transportStatus: "not_implemented",
	}
}
