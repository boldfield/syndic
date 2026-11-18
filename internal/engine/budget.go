package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// Budget names carried by the budget error and event.
const (
	budgetSpendingCeiling = "spending_ceiling"
	budgetContextUnknown  = "context_unknown"
	budgetContext         = "context"
	budgetTurn            = "turn_max_estimated_tokens"
)

// estimateExceeded is the participant estimate state after a request's
// reported prompt tokens passed its estimate.
const estimateExceeded = "exceeded"

// Terminal reasons for a refused dispatch.
const (
	reasonBudgetContext = "budget:context:"
	reasonBudgetTurn    = "budget:turn"
)

// Stand-ins for the values a projection cannot know. They only fix a byte
// length: a candidate hash is 64 hex characters, an attempt id or delimiter
// 32.
var (
	standInHash  = strings.Repeat("0", 64)
	standInToken = strings.Repeat("0", 32)
)

// budgetRecord is the turn's budget_json. Charged is the running total the
// turn limit applies to: usage reported for completed attempts plus the
// reservations still outstanding, of which Reserved is the part not yet
// reconciled.
type budgetRecord struct {
	Projected int  `json:"projected"`
	Reserved  int  `json:"reserved"`
	Charged   int  `json:"charged"`
	Limit     int  `json:"limit"`
	Exhausted bool `json:"exhausted"`
}

func readBudget(turn *journal.TurnRow) (budgetRecord, bool, error) {
	if len(turn.BudgetJSON) == 0 {
		return budgetRecord{}, false, nil
	}
	var rec budgetRecord
	if err := json.Unmarshal(turn.BudgetJSON, &rec); err != nil {
		return budgetRecord{}, false, fmt.Errorf("engine: budget record: %w", err)
	}
	return rec, true, nil
}

func writeBudget(tx *journal.Tx, turnID string, rec budgetRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("engine: budget record: %w", err)
	}
	return tx.SetTurnBudget(turnID, data)
}

// budgetRefusal is a dispatch the budget refused. Nothing was written for it.
type budgetRefusal struct {
	kind        string // budgetContext or budgetTurn
	participant string
	admission   int
	estimated   int
	limit       int
}

func (r *budgetRefusal) Error() string { return "engine: budget refused: " + r.kind }

func (r *budgetRefusal) reason() string {
	if r.kind == budgetContext {
		return reasonBudgetContext + r.participant
	}
	return reasonBudgetTurn
}

func (r *budgetRefusal) event(turnID string) wire.EvBudget {
	ev := wire.EvBudget{TurnID: turnID, Budget: r.kind, Limit: int64(r.limit)}
	if r.kind == budgetContext {
		ev.Participant = strPtr(r.participant)
		ev.Admission = i64Ptr(r.admission)
	} else {
		ev.Estimated = i64Ptr(r.estimated)
	}
	return ev
}

func i64Ptr(n int) *int64 {
	v := int64(n)
	return &v
}

// partBudget is a participant's budget constants for one turn.
type partBudget struct {
	id               string
	tokensPerByteMax float64
	bytesPerToken    float64
	contextBound     int
	outputBound      *int
	overheads        budget.Overheads
	margin           float64
}

// budgetFor returns the participant's constants, or the name of the first
// one it lacks: context_bound, tokens_per_byte_max, bytes_per_token.
func (e *Engine) budgetFor(eff config.Effective, id string) (partBudget, string) {
	mp := e.built.Participants[id]
	pb := partBudget{
		id:          id,
		outputBound: mp.OutputBound,
		overheads: budget.Overheads{
			MessageTokens: *eff.Budgets.MessageOverheadTokens,
			RequestTokens: *eff.Budgets.RequestOverheadTokens,
		},
		margin: *eff.Budgets.ContextMargin,
	}
	ep := e.endpoint(e.participantConfig(id).Endpoint)
	switch {
	case mp.ContextBound == nil:
		return pb, "context_bound"
	case ep.TokensPerByteMax == nil:
		return pb, "tokens_per_byte_max"
	case mp.BytesPerToken == nil:
		return pb, "bytes_per_token"
	}
	pb.contextBound = *mp.ContextBound
	pb.tokensPerByteMax = *ep.TokensPerByteMax
	pb.bytesPerToken = *mp.BytesPerToken
	return pb, ""
}

func (pb partBudget) usable() int { return budget.UsableBound(pb.contextBound, pb.margin) }

func (pb partBudget) admission(msgs []transport.Message) int {
	return budget.AdmissionTokens(msgs, pb.tokensPerByteMax, pb.overheads)
}

func (pb partBudget) estimate(msgs []transport.Message) int {
	return budget.EstimateTokens(msgs, pb.bytesPerToken, pb.overheads)
}

// maxOutput is the MaxOutputTokens a request of this participant carries.
func (pb partBudget) maxOutput(configured int) int {
	return budget.ClampOutput(configured, pb.outputBound)
}

// standIn is text of the size a participant's output of at most maxOut tokens
// can have.
func (pb partBudget) standIn(maxOut int) string {
	return strings.Repeat("x", budget.StandInBytes(maxOut, pb.bytesPerToken))
}

func budgetError(kind string) *wire.Error {
	err := refusal(wire.ErrorCodeBudget)
	err.Budget = strPtr(kind)
	return err
}

// projectBudget runs the submit-time budget checks and returns the record to
// store on the turn.
func (e *Engine) projectBudget(eff config.Effective, input string) (budgetRecord, error) {
	ids := []string{eff.Preset.Author}
	ids = append(ids, eff.Preset.RequiredReviewers...)

	for _, id := range ids {
		if e.endpoint(e.participantConfig(id).Endpoint).DeclaredBilling == "metered" {
			err := budgetError(budgetSpendingCeiling)
			err.Participant = strPtr(id)
			return budgetRecord{}, err
		}
	}
	consts := make(map[string]partBudget, len(ids))
	for _, id := range ids {
		pb, missing := e.budgetFor(eff, id)
		if missing != "" {
			err := budgetError(budgetContextUnknown)
			err.Participant = strPtr(id)
			err.Missing = strPtr(missing)
			return budgetRecord{}, err
		}
		consts[id] = pb
	}

	limit := *eff.Budgets.TurnMaxEstimatedTokens
	total := 0

	// fits checks the recipient's admission bound and adds the request's
	// estimate and output bound to the aggregate.
	fits := func(pb partBudget, msgs []transport.Message, maxOut int) error {
		adm := pb.admission(msgs)
		if !budget.Fits(adm, maxOut, pb.usable()) {
			err := budgetError(budgetContext)
			err.Participant = strPtr(pb.id)
			err.Admission = i64Ptr(satAdd(adm, maxOut))
			err.Limit = i64Ptr(pb.usable())
			return err
		}
		total = satAdd(total, satAdd(pb.estimate(msgs), maxOut))
		return nil
	}

	author := consts[eff.Preset.Author]
	authorOut := author.maxOutput(*eff.Budgets.AuthorMaxOutputTokens)
	if err := fits(author, prompt.Author(input), authorOut); err != nil {
		return budgetRecord{}, err
	}

	if eff.Preset.Mode != modeSolo {
		candidate := author.standIn(authorOut)
		review := prompt.ReviewInput{
			Request:       input,
			Candidate:     candidate,
			CandidateHash: standInHash,
			AttemptID:     standInToken,
			Delimiter:     standInToken,
		}
		delivery := prompt.DeliveryInput{
			TerminalState:  StateIncomplete,
			TerminalReason: reasonBlock + standInToken,
			CandidateHash:  standInHash,
		}
		for _, id := range eff.Preset.RequiredReviewers {
			rv := consts[id]
			out := rv.maxOutput(*eff.Budgets.ReviewerMaxOutputTokens)
			output := rv.standIn(out)
			msgs, err := prompt.Reviewer(review)
			if err != nil {
				return budgetRecord{}, err
			}
			if err := fits(rv, msgs, out); err != nil {
				return budgetRecord{}, err
			}
			repair, err := prompt.Repair(review, output, standInToken)
			if err != nil {
				return budgetRecord{}, err
			}
			if err := fits(rv, repair, out); err != nil {
				return budgetRecord{}, err
			}
			delivery.ReviewerOutputs = append(delivery.ReviewerOutputs, prompt.ReviewerOutput{Participant: id, Output: output})
		}
		msgs := prompt.Delivery(delivery)
		for _, id := range ids {
			pb := consts[id]
			out := pb.maxOutput(*eff.Budgets.DeliveryMaxTokens)
			total = satAdd(total, satAdd(pb.estimate(msgs), out))
		}
	}

	if total > limit {
		err := budgetError(budgetTurn)
		err.Estimated = i64Ptr(total)
		err.Limit = i64Ptr(limit)
		return budgetRecord{}, err
	}
	return budgetRecord{Projected: total, Limit: limit}, nil
}

func satAdd(a, b int) int {
	const maxInt = int(^uint(0) >> 1)
	if a > maxInt-b {
		return maxInt
	}
	return a + b
}

// reserve is the dispatch check. Inside the Tx that begins the attempt it
// checks the real messages against the recipient's usable bound, then adds
// the request's estimate and output bound to the turn's running total and
// writes it, or returns a *budgetRefusal. It returns the request's estimate.
func (e *Engine) reserve(tx *journal.Tx, t *openTurn, id string, msgs []transport.Message, maxOut int) (int, error) {
	pb, missing := e.budgetFor(t.eff, id)
	if missing != "" {
		return 0, fmt.Errorf("engine: participant %q lacks %s", id, missing)
	}
	adm := pb.admission(msgs)
	if !budget.Fits(adm, maxOut, pb.usable()) {
		return 0, &budgetRefusal{kind: budgetContext, participant: id, admission: satAdd(adm, maxOut), limit: pb.usable()}
	}
	est := pb.estimate(msgs)

	turn, err := tx.GetTurn(t.id)
	if err != nil {
		return 0, err
	}
	rec, ok, err := readBudget(turn)
	if err != nil {
		return 0, err
	}
	if !ok {
		return est, nil
	}
	reservation := satAdd(est, maxOut)
	next := satAdd(rec.Charged, reservation)
	if rec.Exhausted || next > rec.Limit {
		return 0, &budgetRefusal{kind: budgetTurn, estimated: next, limit: rec.Limit}
	}
	rec.Reserved = satAdd(rec.Reserved, reservation)
	rec.Charged = next
	return est, writeBudget(tx, t.id, rec)
}

// settle reconciles an attempt's reservation with what its completion
// reported, in the Tx that records the completion. It reports whether the
// attempt's prompt exceeded its estimate, for the caller to mark once the Tx
// has committed.
func (e *Engine) settle(tx *journal.Tx, emit hub.Emit, turnID, attemptID string, usage *transport.Usage, accepted bool) (exceeded bool, err error) {
	attempt, err := tx.GetAttempt(attemptID)
	if err != nil {
		return false, err
	}
	turn, err := tx.GetTurn(turnID)
	if err != nil {
		return false, err
	}
	rec, ok, err := readBudget(turn)
	if err != nil || !ok {
		return false, err
	}

	est, reservation := 0, 0
	if attempt.EstimatedPromptTokens != nil {
		est = *attempt.EstimatedPromptTokens
		reservation = est
	}
	if attempt.MaxOutputTokens != nil {
		reservation = satAdd(reservation, *attempt.MaxOutputTokens)
	}

	charge := reservation
	if usage != nil {
		reported := satAdd(usage.Prompt, usage.Completion)
		switch {
		case accepted:
			charge = reported
		case reported > reservation:
			charge = reported
		}
		if usage.Prompt > est {
			exceeded = true
			if err := emit(turnID, wire.EventKindBudgetEstimateExceeded, wire.EvBudgetEstimateExceeded{
				TurnID:    turnID,
				AttemptID: attemptID,
				Estimated: int64(est),
				Reported:  int64(usage.Prompt),
			}); err != nil {
				return false, err
			}
		}
	}

	rec.Reserved -= reservation
	if rec.Reserved < 0 {
		rec.Reserved = 0
	}
	rec.Charged = satAdd(rec.Charged-reservation, charge)
	if !rec.Exhausted && rec.Charged > rec.Limit {
		rec.Exhausted = true
		if err := emit(turnID, wire.EventKindBudgetExhausted, wire.EvBudgetExhausted{
			TurnID:  turnID,
			Charged: int64(rec.Charged),
			Limit:   int64(rec.Limit),
		}); err != nil {
			return false, err
		}
	}
	return exceeded, writeBudget(tx, turnID, rec)
}

// markExceeded records that the participant's estimate was exceeded; Status
// reports it for the rest of the run. e.mu is held.
func (e *Engine) markExceeded(id string) {
	if e.exceeded == nil {
		e.exceeded = map[string]bool{}
	}
	e.exceeded[id] = true
}

// asRefusal reports whether err is a refused dispatch.
func asRefusal(err error) (*budgetRefusal, bool) {
	var r *budgetRefusal
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}

// finishBudget ends the turn for a refused dispatch: the budget event and the
// terminal path in one Tx. e.mu is held.
func (e *Engine) finishBudget(t *openTurn, r *budgetRefusal) {
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		if err := emit(t.id, wire.EventKindBudget, r.event(t.id)); err != nil {
			return err
		}
		return endTurnIn(tx, emit, t.id, StateIncomplete, r.reason())
	})
	if err != nil {
		return
	}
	if e.open == t {
		e.open = nil
		e.turnEnded(t)
	}
}

// settleQuarantined stores the usage of a quarantined result and charges it.
func (e *Engine) settleQuarantined(tx *journal.Tx, emit hub.Emit, turnID, attemptID string, res transport.CompleteResult) (bool, error) {
	if res.Usage != nil {
		if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
			return false, err
		}
	}
	return e.settle(tx, emit, turnID, attemptID, res.Usage, false)
}
