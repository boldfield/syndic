package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/referee"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// The referee of a web call (v5 delta section 6). The endpoint a profile names
// runs it, on the profile itself or on the local endpoint its referee_endpoint
// names; each decision is an ordinary accounted attempt of the calling
// participant, reserved in the journal before it is sent, charged against the
// turn's budget, and quarantined when late.

const (
	// phaseReferee is the phase of a referee decision's attempt.
	phaseReferee = "referee"

	// eventRefereeDecision is the journaled decision of a web call. v5 delta
	// section 6 (Accounting) names the event referee_decision{call_id,
	// attempt_id, verdict, reason_sha256}; that kind is already emitted, with a
	// different payload (decision_id, decision, reason, cause), by
	// journal.Tx.RecordRefereeDecision for a bash call (v7 delta), and one
	// kind cannot carry two payloads. The web decision therefore has a kind of
	// its own, with the design's four fields. The design's name is a
	// deviation for its owner to ratify.
	eventRefereeDecision = "web_referee_decision"

	// defaultRefereeMaxOutputTokens is tools.referee.max_output_tokens when
	// the roster sets none.
	defaultRefereeMaxOutputTokens = 64
)

// Causes of a deny that the referee's reply does not itself give, beside the
// journal's referee causes.
const (
	refereeCausePoolHeld = "pool_held" // an uncertain-capacity hold keeps the pool from a dispatch
	refereeCauseLate     = "late"      // the result was late or quarantined, and authorizes nothing
	refereeCauseGate     = "gate"      // the result failed the identity or completion checks
)

// Reasons web is not offered for an endpoint's callers.
const (
	webOffNoReferee   = "no_referee_model"
	webOffUnprobed    = "referee_unprobed"
	webOffBudget      = "referee_budget_unknown"
	webOffNoAdapter   = "referee_no_adapter"
	webOffUnprobedFmt = webOffUnprobed + ":%s"
)

// errRefereeUnavailable is returned for a call whose endpoint has no usable
// referee. Web is not offered then, so no call should reach a referee.
var errRefereeUnavailable = errors.New("engine: no usable referee for the caller's endpoint")

// refereeBudgetEnded is returned when a referee dispatch could not be reserved
// and so ended the turn. It is an abandoned call, not a deny: the call has no
// answer because the turn ended.
type refereeBudgetEnded struct{ refusal *budgetRefusal }

func (e *refereeBudgetEnded) Error() string {
	return "engine: referee dispatch not reserved: " + e.refusal.reason()
}

func (e *refereeBudgetEnded) Is(target error) bool { return target == errToolAbandoned }

// refereeIdentities returns the referee models the roster names: for every
// endpoint profile that names a referee_model, the model on the profile itself
// or on its referee_endpoint. Start-time probing selects, probes and records
// each exactly as it does a participant.
var refereeIdentities = rosterRefereeIdentities

func rosterRefereeIdentities(roster *config.Roster) []refereeIdentity {
	var out []refereeIdentity
	for _, ep := range roster.Endpoints {
		if ep.RefereeModel == "" {
			continue
		}
		id := refereeIdentity{Endpoint: ep.Name, Model: ep.RefereeModel}
		if ep.RefereeEndpoint != "" {
			id.Endpoint = ep.RefereeEndpoint
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// refereeAdapter returns the adapter a referee identity runs through and its
// version: the adapter of a participant on the identity's endpoint when there
// is one, else one built for the identity.
func (e *Engine) refereeAdapter(roster *config.Roster, adapters map[string]transport.Adapter, versions map[string]string, r refereeIdentity) (transport.Adapter, string, error) {
	for _, p := range roster.Participants {
		if p.Endpoint == r.Endpoint && adapters[p.ID] != nil {
			return adapters[p.ID], versions[p.ID], nil
		}
	}
	ep := endpointOf(roster, r.Endpoint)
	if ep == nil {
		return nil, "", fmt.Errorf("endpoint %q not found", r.Endpoint)
	}
	return e.newAdapter(config.Participant{ID: manifest.RefereeLabel(r.Endpoint, r.Model), Endpoint: r.Endpoint, Model: r.Model, Route: "api"}, *ep)
}

// addRefereeAdapters adds an adapter and version for each referee identity of
// the roster, under its label. An identity whose adapter cannot be built stays
// out: its endpoint's web tools are then not offered.
func (e *Engine) addRefereeAdapters(roster *config.Roster, adapters map[string]transport.Adapter, versions map[string]string) {
	for _, r := range refereeIdentities(roster) {
		a, version, err := e.refereeAdapter(roster, adapters, versions, r)
		if err != nil {
			log.Printf("engine: adapter for referee %s/%s: %v", r.Endpoint, r.Model, err)
			continue
		}
		label := manifest.RefereeLabel(r.Endpoint, r.Model)
		adapters[label] = a
		versions[label] = version
	}
}

// refereeOf returns the referee of an endpoint profile's callers and the
// profile it runs on. ok is false when the profile names none.
func (e *Engine) refereeOf(endpoint string) (manifest.Referee, config.EndpointProfile, bool) {
	ref, ok := e.built.Referees[endpoint]
	if !ok {
		return manifest.Referee{}, config.EndpointProfile{}, false
	}
	return ref, e.endpoint(ref.RunEndpoint), true
}

// webOffReason is why the web tools are not offered to the callers of the
// endpoint, or empty when they may be: its profile must name a referee model
// with a usable probe record and the constants a reservation needs, and an
// adapter must exist to run it. e.mu is held.
func (e *Engine) webOffReason(endpoint string) string {
	ref, run, ok := e.refereeOf(endpoint)
	switch {
	case !ok:
		return webOffNoReferee
	case !ref.Usable:
		detail := ref.Detail
		if detail == "" {
			detail = "no_record"
		}
		return fmt.Sprintf(webOffUnprobedFmt, detail)
	case ref.ContextBound == nil || ref.BytesPerToken == nil || run.TokensPerByteMax == nil:
		return webOffBudget
	case e.adapters[manifest.RefereeLabel(ref.RunEndpoint, ref.Model)] == nil:
		return webOffNoAdapter
	}
	return ""
}

// participantHoldsWeb reports whether any preset gives the participant the web
// tools, as author or as a required reviewer.
func (e *Engine) participantHoldsWeb(id string) bool {
	for _, preset := range e.roster.Presets {
		if preset.Author == id && slices.Contains(preset.Tools, tools.GroupWeb) {
			return true
		}
		if slices.Contains(preset.RequiredReviewers, id) && slices.Contains(preset.ReviewerTools, tools.GroupWeb) {
			return true
		}
	}
	return false
}

// refereeMaxOutput is tools.referee.max_output_tokens, 64 when unset.
func (e *Engine) refereeMaxOutput() int {
	if e.roster != nil && e.roster.Tools != nil && e.roster.Tools.Referee != nil && e.roster.Tools.Referee.MaxOutputTokens != nil {
		return *e.roster.Tools.Referee.MaxOutputTokens
	}
	return defaultRefereeMaxOutputTokens
}

// refereeCall is one web call to be refereed. callID is the journal's id of the
// accepted call, name and target its tool name and its URL or query.
type refereeCall struct {
	turnID string
	caller string
	callID string
	name   string
	target string
}

// refereeOutcome is the decision for one call. Allow authorizes the fetch;
// anything else is a deny, and cause says why when the reply did not give it.
// attemptID is empty when nothing was dispatched.
type refereeOutcome struct {
	allow     bool
	reason    string
	cause     string
	attemptID string
	// step and calls: the result was a read step, not a decision.
	step  bool
	calls []transport.ToolCall
	text  string
}

// refereeDecisionEvent is the payload of the journaled web_referee_decision of a
// web call. The reason text is in the content store under reason_sha256.
type refereeDecisionEvent struct {
	CallID       string `json:"call_id"`
	AttemptID    string `json:"attempt_id,omitempty"`
	Verdict      string `json:"verdict"`
	ReasonSHA256 string `json:"reason_sha256"`
	Cause        string `json:"cause,omitempty"`
}

// dispatchReferee asks the referee for a decision on one web call that passed
// the pre-send checks, and returns it. It runs inside a tool effect with the
// caller's pool slot still held and e.mu not held, so the caller's slot is the
// referee request's: the caller waits for the answer, and the pool's counters
// are the caller's. A hold on the caller's pool still governs: with one in
// place nothing is dispatched and the call is denied.
//
// Only an allow from an accepted, identity- and completion-checked result of
// the current epoch authorizes the call. A reservation that does not fit ends
// the turn budget and returns an error that is errToolAbandoned: it is not a
// deny. Any other error leaves no decision and the call must not run.
func (e *Engine) dispatchReferee(c refereeCall) (refereeOutcome, error) {
	e.mu.Lock()
	t := e.open
	if t == nil || t.id != c.turnID || e.turnExpired(t) {
		e.mu.Unlock()
		return refereeOutcome{}, errToolAbandoned
	}
	endpoint := e.participantConfig(c.caller).Endpoint
	ref, run, ok := e.refereeOf(endpoint)
	if !ok || e.webOffReason(endpoint) != "" {
		e.mu.Unlock()
		return refereeOutcome{}, errRefereeUnavailable
	}
	if e.poolHeld(e.poolOf(c.caller)) {
		out := refereeOutcome{reason: "capacity is held", cause: refereeCausePoolHeld}
		err := e.tx(func(tx *journal.Tx, emit hub.Emit) error { return e.recordRefereeDecision(tx, emit, t.id, c, out) })
		e.mu.Unlock()
		return out, err
	}
	adapter := e.adapters[manifest.RefereeLabel(ref.RunEndpoint, ref.Model)]
	calls, err := e.priorWebCalls(t.id, c)
	if err != nil {
		e.mu.Unlock()
		return refereeOutcome{}, err
	}
	req, attemptID, err := e.beginReferee(t, c, ref, run, calls)
	if err != nil {
		defer e.mu.Unlock()
		if r, ok := asRefusal(err); ok {
			e.finishBudget(t, r)
			return refereeOutcome{}, &refereeBudgetEnded{refusal: r}
		}
		return refereeOutcome{}, err
	}
	e.refereeAdapters[attemptID] = adapter
	e.mu.Unlock()

	ours := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.cancelled[attemptID]
	}
	res, fail := callOnce(adapter, req, ours)
	e.returned(attemptID)

	e.mu.Lock()
	defer e.mu.Unlock()
	out, err := e.completeReferee(t, c, ref, req, attemptID, res, fail)
	// Once the result is recorded the attempt is no longer pending, so no
	// hold of it can open later, and any hold opened by now carries the
	// adapter itself. Had the record failed, the attempt would still be
	// pending and a terminal path could yet hold it and need the adapter.
	if err == nil {
		delete(e.refereeAdapters, attemptID)
	}
	return out, err
}

// poolHeld reports whether an uncertain-capacity hold applies to the pool and
// its request is no longer in flight. e.mu is held.
func (e *Engine) poolHeld(name string) bool {
	for _, h := range e.holds {
		if h.calling {
			continue
		}
		if slices.Contains(h.appliedPools, name) {
			return true
		}
	}
	return false
}

// priorWebCalls are the caller's own web_fetch and web_search calls of the
// turn that precede the call, as name and URL or query only. e.mu is held.
func (e *Engine) priorWebCalls(turnID string, c refereeCall) ([]referee.WebCall, error) {
	var prior []referee.WebCall
	err := e.tx(func(tx *journal.Tx, _ hub.Emit) error {
		recorded, err := tx.ToolCallsByTurn(turnID, c.caller)
		if err != nil {
			return err
		}
		for _, call := range recorded {
			if call.ID == c.callID {
				return nil
			}
			if call.Class != "web" || (call.Name != tools.NameWebFetch && call.Name != tools.NameWebSearch) {
				continue
			}
			args, err := tx.Content(call.ArgsSHA256)
			if err != nil {
				return err
			}
			prior = append(prior, referee.WebCall{Name: call.Name, URL: webTarget(call.Name, args)})
		}
		return nil
	})
	return prior, err
}

// webTarget is the URL of a web_fetch call's arguments or the query of a
// web_search call's.
func webTarget(name string, args []byte) string {
	var parsed struct {
		URL   string `json:"url"`
		Query string `json:"query"`
	}
	if json.Unmarshal(args, &parsed) != nil {
		return ""
	}
	if name == tools.NameWebSearch {
		return parsed.Query
	}
	return parsed.URL
}

// beginReferee reserves the referee request in the turn's budget and records
// its attempt, in one transaction. The request carries the fixed template and
// the payload and nothing else; tools.referee.max_output_tokens bounds its
// output and the reservation uses that bound. It returns a *budgetRefusal
// when the request does not fit. e.mu is held.
func (e *Engine) beginReferee(t *openTurn, c refereeCall, ref manifest.Referee, run config.EndpointProfile, prior []referee.WebCall) (transport.CompleteRequest, string, error) {
	msgs := referee.PromptMessages(referee.Payload{
		UserInput:  t.input,
		CallName:   c.name,
		CallURL:    c.target,
		PriorCalls: prior,
	})
	return e.beginRefereeWith(t, c.caller, ref, run, msgs, nil, e.refereeMaxOutput())
}

// beginRefereeWith reserves a referee request of msgs and the tool definitions
// defs, bounded to maxOutput tokens, and records its attempt, in one
// transaction. e.mu is held.
func (e *Engine) beginRefereeWith(t *openTurn, caller string, ref manifest.Referee, run config.EndpointProfile, msgs []transport.Message, defs []transport.Tool, maxOutput int) (transport.CompleteRequest, string, error) {
	pb, missing := budgetConstants(t.eff, manifest.RefereeLabel(ref.RunEndpoint, ref.Model), ref.Participant, run)
	if missing != "" {
		return transport.CompleteRequest{}, "", fmt.Errorf("engine: referee %s lacks %s", pb.id, missing)
	}
	maxOut := budget.ClampOutput(maxOutput, ref.OutputBound)
	deadline := e.attemptDeadline(t)

	var req transport.CompleteRequest
	var attemptID string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		turn, err := tx.GetTurn(t.id)
		if err != nil {
			return err
		}
		estimated, err := reserveWith(tx, t, pb, msgs, defs, maxOut)
		if err != nil {
			return err
		}
		attemptID, err = tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:                t.id,
			Participant:           caller,
			Phase:                 phaseReferee,
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
			Participant:           caller,
			Phase:                 phaseReferee,
			Epoch:                 int64(turn.Epoch),
			Deadline:              deadline.Format(time.RFC3339Nano),
			MaxOutputTokens:       int64(maxOut),
			EstimatedPromptTokens: int64(estimated),
		}); err != nil {
			return err
		}
		req = transport.CompleteRequest{
			Envelope: transport.AttemptEnvelope{
				Participant: caller,
				TurnID:      t.id,
				Phase:       phaseReferee,
				Epoch:       turn.Epoch,
				AttemptID:   attemptID,
			},
			Model:           ref.Model,
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
	// The attempt is in flight with the caller's slot; the turn's deadline,
	// not a timer of its own, ends a call the adapter never returns.
	e.calling[attemptID] = true
	t.pending[attemptID] = deadline
	return req, attemptID, nil
}

// completeReferee accepts the referee's result, charges it, and journals the
// decision, in one transaction. An allow comes only from a result that was
// accepted for the current epoch, passed the identity and completion checks
// and parsed as an allow; a late or quarantined result, a failed gate, an
// error or a timeout denies. A failure that leaves the work's outcome unknown
// opens a capacity hold. e.mu is held.
func (e *Engine) completeReferee(t *openTurn, c refereeCall, ref manifest.Referee, req transport.CompleteRequest, attemptID string, res transport.CompleteResult, fail *failure) (refereeOutcome, error) {
	return e.completeRefereeWith(t, c.caller, ref, req, attemptID, res, fail, func(tx *journal.Tx, emit hub.Emit, out refereeOutcome) error {
		return e.recordRefereeDecision(tx, emit, t.id, c, out)
	})
}

// completeRefereeWith is completeReferee for a decision recorded by record.
// When the request offered tool definitions, an accepted, checked result that
// finishes with the tool-call status and carries calls is a read step, not a
// decision: it is charged and journaled like any attempt, record is not
// called, and the outcome has step set and carries the calls. e.mu is held.
func (e *Engine) completeRefereeWith(t *openTurn, caller string, ref manifest.Referee, req transport.CompleteRequest, attemptID string, res transport.CompleteResult, fail *failure, record func(*journal.Tx, hub.Emit, refereeOutcome) error) (refereeOutcome, error) {
	gate := transport.CheckResult(req, res, ref.Expected)
	var out refereeOutcome
	var held *hold
	quarantined := false
	exceeded := false
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		out, held, quarantined, exceeded = refereeOutcome{attemptID: attemptID}, nil, false, false
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
			// Late: the epoch moved on or the turn ended. It is charged and
			// quarantined, and authorizes nothing.
			quarantined = true
			out.cause = refereeCauseLate
			if exceeded, err = e.settleQuarantined(tx, emit, t.id, attemptID, res); err != nil {
				return err
			}
			return record(tx, emit, out)
		}

		var usage *wire.ReportedUsage
		if res.Usage != nil {
			if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
				return err
			}
			prompt, completion := int64(res.Usage.Prompt), int64(res.Usage.Completion)
			usage = &wire.ReportedUsage{PromptTokens: &prompt, CompletionTokens: &completion}
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
		// The failure's state is the referee's, not the caller's: only the
		// hold shows on the caller.
		var uncertain *failure
		if fail != nil && fail.hold {
			uncertain = &failure{hold: true}
		}
		if held, err = e.failedIn(tx, emit, caller, attemptID, uncertain); err != nil {
			return err
		}
		if held != nil && e.lastInflight(caller) {
			if err := emit("", wire.EventKindParticipantState, wire.EvParticipantState{Participant: caller, State: ParticipantUnknown}); err != nil {
				return err
			}
		}

		// Anything but a complete, call-free reply is a deny named by what it
		// was; only then does a failed identity or completion check name it.
		parsed, perr := referee.Parse(res.Text, res.Status, len(res.ToolCalls) > 0)
		out.reason = parsed.Reason
		switch {
		case fail != nil:
			out.cause = refereeFailureCause(fail)
		case len(req.Tools) > 0 && gate == nil && transport.IsToolStep(res):
			out.step, out.calls, out.text = true, res.ToolCalls, res.Text
			return nil
		case perr != nil && (res.Status != transport.StatusComplete || len(res.ToolCalls) > 0):
			out.cause, out.reason = refereeParseCause(res), perr.Error()
		case gate != nil:
			out.cause = refereeCauseGate
		case perr != nil:
			out.cause, out.reason = journal.RefereeCauseMalformed, perr.Error()
		default:
			if err := tx.SetAttemptText(attemptID, res.Text); err != nil {
				return err
			}
			if parsed.Decision == referee.DecisionAllow {
				out.allow = true
			} else {
				out.cause = journal.RefereeCauseAnswer
			}
		}
		return record(tx, emit, out)
	})
	delete(t.pending, attemptID)
	if err != nil {
		// No decision was recorded and none is returned: the call must not run.
		return refereeOutcome{attemptID: attemptID}, err
	}
	if exceeded {
		e.markExceeded(caller)
	}
	if quarantined {
		e.lateCommitted(attemptID, res)
		return out, nil
	}
	if held != nil {
		e.failedCommitted(caller, &failure{hold: true}, held)
	}
	return out, nil
}

// refereeFailureCause maps a failed dispatch to the cause of the deny.
func refereeFailureCause(f *failure) string {
	switch f.transportStatus {
	case transport.TransportStatusTimeout:
		return journal.RefereeCauseTimeout
	}
	return journal.RefereeCauseAdapterError
}

// refereeParseCause maps a reply that is not a complete, call-free answer to
// the cause of the deny, from its completion status and its tool calls.
func refereeParseCause(res transport.CompleteResult) string {
	switch {
	case res.Status == transport.StatusRefused:
		return journal.RefereeCauseRefusal
	case res.Status == transport.StatusTruncated:
		return journal.RefereeCauseCutOff
	case len(res.ToolCalls) > 0 && transport.IsToolStep(res):
		return journal.RefereeCauseCallsWithoutTools
	case len(res.ToolCalls) > 0:
		return journal.RefereeCauseCallsWrongStatus
	}
	return journal.RefereeCauseAdapterError
}

// recordRefereeDecision journals the decision of a web call inside the caller's
// transaction: the reason text in the content store and web_referee_decision with
// the call, the attempt and the verdict. e.mu is held.
func (e *Engine) recordRefereeDecision(tx *journal.Tx, emit hub.Emit, turnID string, c refereeCall, out refereeOutcome) error {
	sum, err := tx.StoreContent([]byte(out.reason))
	if err != nil {
		return err
	}
	verdict := journal.RefereeDeny
	if out.allow {
		verdict = journal.RefereeAllow
	}
	return emit(turnID, eventRefereeDecision, refereeDecisionEvent{
		CallID:       c.callID,
		AttemptID:    out.attemptID,
		Verdict:      verdict,
		ReasonSHA256: sum,
		Cause:        out.cause,
	})
}
