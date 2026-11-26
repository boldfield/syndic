package engine

import (
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

	// Get prior web calls from the turn. Each call records name and URL only.
	priorCalls := e.getRefereeCallLog(t, participant)

	// Build the referee payload with the web call and prior calls.
	payload := referee.Payload{
		CallName:   webCall.Name,
		CallURL:    webCall.URL,
		UserInput:  t.input,
		PriorCalls: priorCalls,
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

	return e.dispatchRefereeLocked(t, participant, refEndpoint, callID, webCall, req)
}

// dispatchRefereeLocked issues a synchronous referee attempt with proper
// accounting. It is called with e.mu held and returns with e.mu held.
func (e *Engine) dispatchRefereeLocked(t *openTurn, participant string, refEndpoint string, callID string, webCall referee.WebCall, req transport.CompleteRequest) (*RefereeDecision, error) {
	// If the turn is not open, fail fast
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

		// Admission is computed over the rendered request.
		// For now we estimate it as the byte length of the messages.
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

	// Get adapter for the referee endpoint (which may be different from participant endpoint)
	refAdapter := e.adapterForEndpoint(refEndpoint, req.Envelope.Participant)
	if refAdapter == nil {
		refAdapter = e.adapterFor(req.Envelope.Participant)
	}

	// Call the adapter with the mutex released.
	e.mu.Unlock()
	res, fail := e.completeWith(t, refAdapter, req)
	e.mu.Lock()

	// Record the result in the journal.
	var wasLate bool
	var checkErr error
	var decision *RefereeDecision

	checkErr = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		// Run identity, epoch and completion checks. A late result is quarantined.
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

		if !accepted {
			wasLate = true
		}

		// Check result validity: allow or deny with one line of reason,
		// no tool calls, complete status.
		isValid := !wasLate && fail == nil && res.Status == transport.StatusComplete && len(res.ToolCalls) == 0

		// Parse the decision.
		var verdict referee.Decision
		reasonBytes := []byte(res.Text)
		if isValid {
			parsed, err := referee.Parse(res.Text, res.Status, false)
			if err != nil {
				isValid = false
				reasonBytes = []byte(err.Error())
				verdict = referee.DecisionDeny
			} else {
				verdict = parsed.Decision
			}
		} else {
			verdict = referee.DecisionDeny
		}

		// Store reason in content store
		reasonSHA256, err := e.store.StoreContent(reasonBytes)
		if err != nil {
			return err
		}

		// Set decision for return
		if isValid {
			parsed, _ := referee.Parse(res.Text, res.Status, false)
			decision = &RefereeDecision{
				Verdict: verdict,
				Reason:  parsed.Reason,
			}
		} else {
			decision = &RefereeDecision{
				Verdict: referee.DecisionDeny,
				Reason:  "referee: invalid result",
			}
		}

		// Record usage
		if res.Usage != nil {
			if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
				return err
			}
		}

		// Settle the attempt: if late, quarantine; if failed, hold; otherwise settle.
		if wasLate {
			_, err = e.settleQuarantined(tx, emit, t.id, attemptID, res)
		} else if fail != nil {
			_, err = e.failedIn(tx, emit, participant, attemptID, fail)
		} else {
			_, err = e.settle(tx, emit, t.id, attemptID, res.Usage, true)
		}
		if err != nil {
			return err
		}

		// Journal the referee decision event with attempt_id, call_id, verdict, and reason_sha256
		payloadStr := fmt.Sprintf(`{"attempt_id":%q,"call_id":%q,"verdict":%q,"reason_sha256":%q}`,
			attemptID, callID, string(verdict), reasonSHA256)
		return tx.AppendEvent("referee_decision", []byte(payloadStr))
	})

	if checkErr != nil {
		return &RefereeDecision{
			Verdict: referee.DecisionDeny,
			Reason:  "referee: journaling failed",
		}, nil
	}

	// If the result was late, it's quarantined and does not authorize
	if wasLate {
		e.releaseLocked(participant)
		return &RefereeDecision{
			Verdict: referee.DecisionDeny,
			Reason:  "referee: late result",
		}, nil
	}

	e.releaseLocked(participant)
	return decision, nil
}

// adapterForEndpoint returns an adapter for a specific endpoint name, or nil if not found.
// The endpoint must be one that has at least one participant or is a referee endpoint.
func (e *Engine) adapterForEndpoint(endpointName string, fallback string) transport.Adapter {
	// Try to find a participant on this endpoint
	for _, p := range e.roster.Participants {
		if p.Endpoint == endpointName {
			return e.adapterFor(p.ID)
		}
	}
	// If not found, return nil and fall back to participant's own adapter
	return nil
}

// completeWith calls a specific adapter for a request.
func (e *Engine) completeWith(t *openTurn, adapter transport.Adapter, req transport.CompleteRequest) (transport.CompleteResult, *failure) {
	if adapter == nil {
		return transport.CompleteResult{}, &failure{
			attemptStatus:   transport.StatusError,
			transportStatus: "adapter_unavailable",
			retry:           false,
			hold:            true,
			state:           "",
		}
	}
	ours := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.cancelled[req.Envelope.AttemptID]
	}
	res, f := callOnce(adapter, req, ours)
	if f != nil && f.retry && e.waitToRetry(t, req) && e.recordRetry(t, req) {
		res, f = callOnce(adapter, req, ours)
	}
	e.returned(req.Envelope.AttemptID)
	return res, f
}

// getRefereeCallLog collects the participant's web_fetch and web_search calls
// from earlier in this turn, returning them as name and URL only.
func (e *Engine) getRefereeCallLog(t *openTurn, participant string) []referee.WebCall {
	// TODO: This should read from the turn's tool calls to find prior web_fetch
	// and web_search calls by this participant and return them as (name, URL) tuples.
	// For now, return an empty slice.
	return []referee.WebCall{}
}
