package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/manifest"
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

// Phases of a deliberate turn, as named in a budget error and the projection.
const (
	phaseProposal          = "proposal"
	phaseCritique          = "critique"
	phaseIntegration       = "integration"
	phaseIntegrationRepair = "integration_repair"
	phaseReviewRepair      = "review_repair"
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
// reconciled. Projected is the first-round estimate; WorstCaseProjected is
// the worst case across all configured rounds.
type budgetRecord struct {
	Projected          int  `json:"projected"`
	WorstCaseProjected int  `json:"worst_case_projected"`
	Reserved           int  `json:"reserved"`
	Charged            int  `json:"charged"`
	Limit              int  `json:"limit"`
	Exhausted          bool `json:"exhausted"`
	// Phases lists a deliberate turn's first-round projection, one entry per
	// projected request.
	Phases []wire.PhaseEstimate `json:"phases,omitempty"`
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
	ep := e.endpoint(e.participantConfig(id).Endpoint)
	return budgetConstants(eff, id, e.built.Participants[id], ep)
}

// budgetConstants is budgetFor for an identity with a manifest record and the
// profile of the endpoint it runs on, such as a participant's or a referee's.
func budgetConstants(eff config.Effective, id string, mp manifest.Participant, ep config.EndpointProfile) (partBudget, string) {
	pb := partBudget{
		id:          id,
		outputBound: mp.OutputBound,
		overheads: budget.Overheads{
			MessageTokens: *eff.Budgets.MessageOverheadTokens,
			RequestTokens: *eff.Budgets.RequestOverheadTokens,
		},
		margin: *eff.Budgets.ContextMargin,
	}
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
	if mp.CalibratedBytesPerToken != nil {
		// The manifest applied the roster's safety; the turn's effective
		// safety may be overridden by the preset or quick profile.
		pb.bytesPerToken = budget.EffectiveBytesPerToken(*mp.CalibratedBytesPerToken, *eff.Budgets.CalibrationSafety)
	}
	return pb, ""
}

func (pb partBudget) usable() int { return budget.UsableBound(pb.contextBound, pb.margin) }

// admission is the admission bound of a request carrying msgs and the tool
// definitions defs.
func (pb partBudget) admission(msgs []transport.Message, defs []transport.Tool) int {
	return budget.RequestAdmissionTokens(msgs, defs, pb.tokensPerByteMax, pb.overheads)
}

// admissionWithAttachments is the admission bound including attachment bytes,
// accounting for them arithmetically as part of an additional message.
func (pb partBudget) admissionWithAttachments(msgs []transport.Message, defs []transport.Tool, attachmentBytes int) int {
	if attachmentBytes == 0 {
		return pb.admission(msgs, defs)
	}
	// Add a user message with attachment content and calculate its token cost.
	extendedMsgs := append(slices.Clone(msgs), transport.Message{
		Role:    "user",
		Content: "", // empty content, we'll account for bytes separately
	})
	base := budget.RequestEstimateTokens(extendedMsgs, defs, pb.bytesPerToken, pb.overheads)
	// Now add the byte cost of attachments (integer ceiling division for bytes->tokens).
	tokensFromBytes := (attachmentBytes + int(pb.bytesPerToken) - 1) / int(pb.bytesPerToken)
	return satAdd(base, tokensFromBytes)
}

// estimate is the estimate of a request carrying msgs and the tool
// definitions defs.
func (pb partBudget) estimate(msgs []transport.Message, defs []transport.Tool) int {
	return budget.RequestEstimateTokens(msgs, defs, pb.bytesPerToken, pb.overheads)
}

// estimateWithAttachments is the estimate including attachment bytes,
// accounting for them arithmetically as part of an additional message.
func (pb partBudget) estimateWithAttachments(msgs []transport.Message, defs []transport.Tool, attachmentBytes int) int {
	if attachmentBytes == 0 {
		return pb.estimate(msgs, defs)
	}
	// Add a user message with attachment content and calculate its token cost.
	extendedMsgs := append(slices.Clone(msgs), transport.Message{
		Role:    "user",
		Content: "", // empty content, we'll account for bytes separately
	})
	base := budget.RequestEstimateTokens(extendedMsgs, defs, pb.bytesPerToken, pb.overheads)
	// Now add the byte cost of attachments (integer ceiling division for bytes->tokens).
	tokensFromBytes := (attachmentBytes + int(pb.bytesPerToken) - 1) / int(pb.bytesPerToken)
	return satAdd(base, tokensFromBytes)
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

// proposerOutput, critiqueOutput and integratorOutput are the
// MaxOutputTokens a deliberate-mode request of this participant carries in
// each role, clamped to its probe output bound. A deliberate preset never
// reads author_max_output_tokens.
func (pb partBudget) proposerOutput(eff config.Effective) int {
	return pb.maxOutput(*eff.Budgets.ProposerMaxOutputTokens)
}

func (pb partBudget) critiqueOutput(eff config.Effective) int {
	return pb.maxOutput(*eff.Budgets.CritiqueMaxOutputTokens)
}

func (pb partBudget) integratorOutput(eff config.Effective) int {
	return pb.maxOutput(*eff.Budgets.IntegratorMaxOutputTokens)
}

// proposalStandIn and critiqueStandIn are the stand-in texts for a proposal
// and a critique this participant writes; candidateStandIn is the stand-in
// for the integrator's candidate and its disposition together.
func (pb partBudget) proposalStandIn(eff config.Effective) string {
	return pb.standIn(pb.proposerOutput(eff))
}

func (pb partBudget) critiqueStandIn(eff config.Effective) string {
	return pb.standIn(pb.critiqueOutput(eff))
}

func (pb partBudget) candidateStandIn(eff config.Effective) string {
	return pb.standIn(pb.integratorOutput(eff))
}

func budgetError(kind string) *wire.Error {
	err := refusal(wire.ErrorCodeBudget)
	err.Budget = strPtr(kind)
	return err
}

// roleTools are the tool definitions the effective preset gives the author
// and the reviewers, from internal/tools.
type roleTools struct {
	author   []transport.Tool
	reviewer []transport.Tool
}

func (e *Engine) roleTools(eff config.Effective) (roleTools, error) {
	author, err := e.groupDefs(eff.Preset.Tools)
	if err != nil {
		return roleTools{}, err
	}
	reviewer, err := e.groupDefs(eff.Preset.ReviewerTools)
	if err != nil {
		return roleTools{}, err
	}
	return roleTools{author: author, reviewer: reviewer}, nil
}

// participantBudgets checks that no id is on a metered endpoint without
// external spending control, and returns each id's budget constants, refusing
// with the first constant an id lacks.
func (e *Engine) participantBudgets(eff config.Effective, ids []string) (map[string]partBudget, error) {
	for _, id := range ids {
		ep := e.endpoint(e.participantConfig(id).Endpoint)
		if ep.DeclaredBilling == "metered" && ep.SpendingControl != "external" {
			err := budgetError(budgetSpendingCeiling)
			err.Participant = strPtr(id)
			return nil, err
		}
	}
	consts := make(map[string]partBudget, len(ids))
	for _, id := range ids {
		pb, missing := e.budgetFor(eff, id)
		if missing != "" {
			err := budgetError(budgetContextUnknown)
			err.Participant = strPtr(id)
			err.Missing = strPtr(missing)
			return nil, err
		}
		consts[id] = pb
	}
	return consts, nil
}

// projectBudget runs the submit-time budget checks and returns the record to
// store on the turn. It computes the first-round projection and reports the
// worst-case projection across all configured rounds, refusing only on the
// first-round projection.
//
// Each projected author and review dispatch carries the definitions the
// preset gives that role; a repair or delivery dispatch carries none. No tool
// calls or results are projected: those attempts are reserved when they are
// dispatched. Attachment bytes (including framing and content) are accounted
// for in every dispatch that carries them.
func (e *Engine) projectBudget(eff config.Effective, input string, doc *history.Document, maxRounds int, attachments []journal.AttachmentContent) (budgetRecord, error) {
	if eff.Preset.Mode == config.ModeDeliberate {
		return e.projectDeliberate(eff, input, doc, attachments)
	}
	ids := []string{eff.Preset.Author}
	ids = append(ids, eff.Preset.RequiredReviewers...)

	consts, err := e.participantBudgets(eff, ids)
	if err != nil {
		return budgetRecord{}, err
	}

	defs, err := e.roleTools(eff)
	if err != nil {
		return budgetRecord{}, err
	}

	limit := *eff.Budgets.TurnMaxEstimatedTokens
	total := 0

	attachmentBytes := attachmentRequestBytes(attachments, len(standInToken))

	// fits checks the recipient's admission bound and adds the request's
	// estimate and output bound to the aggregate, including attachment bytes.
	fits := func(pb partBudget, msgs []transport.Message, toolDefs []transport.Tool, maxOut int, includeAttachments bool) error {
		var adm, est int
		if includeAttachments {
			adm = pb.admissionWithAttachments(msgs, toolDefs, attachmentBytes)
			est = pb.estimateWithAttachments(msgs, toolDefs, attachmentBytes)
		} else {
			adm = pb.admission(msgs, toolDefs)
			est = pb.estimate(msgs, toolDefs)
		}
		if !budget.Fits(adm, maxOut, pb.usable()) {
			err := budgetError(budgetContext)
			err.Participant = strPtr(pb.id)
			err.Admission = i64Ptr(satAdd(adm, maxOut))
			err.Limit = i64Ptr(pb.usable())
			return err
		}
		total = satAdd(total, satAdd(est, maxOut))
		return nil
	}

	var deliveryTemplate *prompt.DeliveryInput
	deliveryCost := 0

	author := consts[eff.Preset.Author]
	authorOut := author.maxOutput(*eff.Budgets.AuthorMaxOutputTokens)
	if err := fits(author, prompt.AuthorWithHistory(doc, input), defs.author, authorOut, true); err != nil {
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
			History:       doc,
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
			if err := fits(rv, msgs, defs.reviewer, out, true); err != nil {
				return budgetRecord{}, err
			}
			repair, err := prompt.Repair(review, output, standInToken)
			if err != nil {
				return budgetRecord{}, err
			}
			if err := fits(rv, repair, nil, out, true); err != nil {
				return budgetRecord{}, err
			}
			delivery.ReviewerOutputs = append(delivery.ReviewerOutputs, prompt.ReviewerOutput{Participant: id, Output: output})
		}
		deliveryCost = deliveryRecipientsCost(eff, consts, ids, delivery, 0)
		total = satAdd(total, deliveryCost)
		deliveryTemplate = &delivery
	}

	if total > limit {
		err := budgetError(budgetTurn)
		err.Estimated = i64Ptr(total)
		err.Limit = i64Ptr(limit)
		return budgetRecord{}, err
	}

	firstRound := total
	worstCase := firstRound

	// For the worst case across all rounds, add the cost of every permitted
	// revision round, from the revision prompt builders over the turn's real
	// history. See revisionRoundCost for what a round is assumed to carry.
	if eff.Preset.Mode != modeSolo {
		for round := 1; round <= maxRounds; round++ {
			cost, err := revisionRoundCost(eff, consts, defs, input, doc, round, attachmentBytes)
			if err != nil {
				return budgetRecord{}, err
			}
			worstCase = satAdd(worstCase, cost)
		}
		// The one terminal delivery carries every block of the turn: up to
		// one per required reviewer per round, round zero included. It
		// replaces the block-free delivery the first-round figure prices.
		if maxRounds > 0 && deliveryTemplate != nil {
			full := deliveryRecipientsCost(eff, consts, ids, *deliveryTemplate, maxRounds+1)
			worstCase = satAdd(worstCase-deliveryCost, full)
		}
	}

	return budgetRecord{Projected: firstRound, WorstCaseProjected: worstCase, Limit: limit}, nil
}

// deliveryRecipientsCost is the estimate plus output bound of the terminal
// delivery summed over every recipient, with blocksPerReviewer stand-in blocks
// for each required reviewer in the delivery.
func deliveryRecipientsCost(eff config.Effective, consts map[string]partBudget, ids []string, delivery prompt.DeliveryInput, blocksPerReviewer int) int {
	delivery.OpenBlocks = nil
	for _, id := range eff.Preset.RequiredReviewers {
		rv := consts[id]
		out := rv.maxOutput(*eff.Budgets.ReviewerMaxOutputTokens)
		for i := 0; i < blocksPerReviewer; i++ {
			delivery.OpenBlocks = append(delivery.OpenBlocks, prompt.OpenBlock{
				BlockID:  longBlockID(rv, out, id, i),
				State:    "open",
				Severity: "SEVERE",
				Evidence: "x",
				Remedy:   "x",
			})
		}
	}
	msgs := prompt.Delivery(delivery)
	total := 0
	for _, id := range ids {
		pb := consts[id]
		out := pb.maxOutput(*eff.Budgets.DeliveryMaxTokens)
		total = satAdd(total, satAdd(pb.estimate(msgs, nil), out))
	}
	return total
}

// longBlockID is a block_id that spends a verdict's whole output bound. The
// verdict parser puts no length limit on block_id, and the revision prompts
// render it three times (both fences and the Block ID line) against once for
// evidence or remedy, so an ID-heavy block is the largest a verdict can make.
// The participant and index keep the IDs distinct.
func longBlockID(rv partBudget, out int, participant string, i int) string {
	return fmt.Sprintf("%s-%d-%s", participant, i, rv.standIn(out))
}

// revisionRoundCost is the most the round-th revision round (1-based) can add
// to the turn's charge: the author's revision with its output bound, and each
// required reviewer's review and one repair, all built by the revision prompt
// builders over the turn's history. The revision and each review carry their
// role's definitions; a repair carries none. Attachment bytes are included in
// the author's revision and each reviewer's review and repair.
//
// The candidate and every reviewer output are as long as their output bounds
// allow. A reviewer's output bound caps the block text one verdict can open,
// and a block it does not withdraw stays open, so by round N a reviewer may
// hold N verdicts' worth of open blocks: N blocks, each carrying one output
// bound of text between its evidence and remedy.
func revisionRoundCost(eff config.Effective, consts map[string]partBudget, defs roleTools, input string, doc *history.Document, round int, attachmentBytes int) (int, error) {
	if doc == nil {
		doc = history.New()
	}
	author := consts[eff.Preset.Author]
	authorOut := author.maxOutput(*eff.Budgets.AuthorMaxOutputTokens)
	candidate := author.standIn(authorOut)

	feedback := make([]prompt.RevisionFeedback, len(eff.Preset.RequiredReviewers))
	for i, id := range eff.Preset.RequiredReviewers {
		rv := consts[id]
		feedback[i] = prompt.RevisionFeedback{Output: rv.standIn(rv.maxOutput(*eff.Budgets.ReviewerMaxOutputTokens))}
	}
	msgs, err := prompt.AuthorRevision(prompt.AuthorRevisionInput{
		Input:             input,
		PreviousCandidate: candidate,
		ReviewerFeedback:  feedback,
		Delimiter:         standInToken,
		HistoryDoc:        doc,
	})
	if err != nil {
		return 0, err
	}
	est := author.estimateWithAttachments(msgs, defs.author, attachmentBytes)
	total := satAdd(est, authorOut)

	for _, id := range eff.Preset.RequiredReviewers {
		rv := consts[id]
		out := rv.maxOutput(*eff.Budgets.ReviewerMaxOutputTokens)
		open := make([]prompt.OpenBlockForRevision, round)
		for i := range open {
			open[i] = prompt.OpenBlockForRevision{
				BlockID:  longBlockID(rv, out, id, i),
				Severity: "SEVERE",
				Evidence: "x",
				Remedy:   "x",
			}
		}
		in := prompt.ReviewerRevisionInput{
			Request:       input,
			Candidate:     candidate,
			CandidateHash: standInHash,
			OpenBlocks:    open,
			AttemptID:     standInToken,
			Delimiter:     standInToken,
			Round:         round,
			HistoryDoc:    doc,
		}
		review, err := prompt.ReviewerRevision(in)
		if err != nil {
			return 0, err
		}
		reviewEst := rv.estimateWithAttachments(review, defs.reviewer, attachmentBytes)
		total = satAdd(total, satAdd(reviewEst, out))
		repair, err := prompt.RepairRevision(in, rv.standIn(out), standInToken)
		if err != nil {
			return 0, err
		}
		repairEst := rv.estimateWithAttachments(repair, nil, attachmentBytes)
		total = satAdd(total, satAdd(repairEst, out))
	}
	return total, nil
}

func satAdd(a, b int) int {
	const maxInt = int(^uint(0) >> 1)
	if a > maxInt-b {
		return maxInt
	}
	return a + b
}

// reserve is the dispatch check. Inside the Tx that begins the attempt it
// checks the real request, its messages and the tool definitions defs sent
// with them, against the recipient's usable bound, then adds the request's
// estimate and output bound to the turn's running total and writes it, or
// returns a *budgetRefusal. It returns the request's estimate. msgs and defs
// are the values the caller places in the request's Messages and Tools.
func (e *Engine) reserve(tx *journal.Tx, t *openTurn, id string, msgs []transport.Message, defs []transport.Tool, maxOut int) (int, error) {
	pb, missing := e.budgetFor(t.eff, id)
	if missing != "" {
		return 0, fmt.Errorf("engine: participant %q lacks %s", id, missing)
	}
	return reserveWith(tx, t, pb, msgs, defs, maxOut)
}

// reserveWith is reserve for an identity whose constants are already known.
func reserveWith(tx *journal.Tx, t *openTurn, pb partBudget, msgs []transport.Message, defs []transport.Tool, maxOut int) (int, error) {
	id := pb.id
	adm := pb.admission(msgs, defs)
	if !budget.Fits(adm, maxOut, pb.usable()) {
		return 0, &budgetRefusal{kind: budgetContext, participant: id, admission: satAdd(adm, maxOut), limit: pb.usable()}
	}
	est := pb.estimate(msgs, defs)

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
	e.launchCancels(e.endLocked(t, StateIncomplete, r.reason(), func(tx *journal.Tx, emit hub.Emit) error {
		return emit(t.id, wire.EventKindBudget, r.event(t.id))
	}))
}

// settleQuarantined stores the usage of a quarantined result and charges it.
func (e *Engine) settleQuarantined(tx *journal.Tx, emit hub.Emit, turnID, attemptID string, res transport.CompleteResult) (bool, error) {
	if res.Usage != nil {
		if err := tx.SetAttemptUsage(attemptID, res.Usage.Prompt, res.Usage.Completion); err != nil {
			return false, err
		}
	}
	exceeded, err := e.settle(tx, emit, turnID, attemptID, res.Usage, false)
	if err != nil {
		return false, err
	}
	return exceeded, e.releaseLateIn(tx, emit, attemptID, res)
}

// zeroReader supplies the random bytes of a projected prompt's delimiters;
// only their length matters.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// projectDeliberate is projectBudget for a deliberate preset. It builds every
// phase's prompts with the real builders over stand-ins: a proposal and a
// critique for each proposer, the integration and its repair, each required
// reviewer's review and its repair, and the deliveries. Each prompt must fit
// its recipient's usable bound, naming the participant and the phase
// otherwise, and the sum must fit the turn limit. As in review mode only the
// first round is refused; no revision round is priced. Attachment bytes are
// accounted for in proposal, critique, integration, and review phases.
//
// Proposal, critique and review requests carry the definitions the preset's
// reviewer_tools gives; integration carries those of tools; a repair or a
// delivery carries none.
func (e *Engine) projectDeliberate(eff config.Effective, input string, doc *history.Document, attachments []journal.AttachmentContent) (budgetRecord, error) {
	proposers := eff.Preset.DeliberateProposers()
	integrator := eff.Preset.DeliberateIntegrator()
	reviewers := eff.Preset.EffectiveRequiredReviewers()
	ids := slices.Clone(proposers)
	for _, id := range append([]string{integrator}, reviewers...) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	consts, err := e.participantBudgets(eff, ids)
	if err != nil {
		return budgetRecord{}, err
	}
	defs, err := e.roleTools(eff)
	if err != nil {
		return budgetRecord{}, err
	}

	limit := *eff.Budgets.TurnMaxEstimatedTokens
	total := 0
	var phases []wire.PhaseEstimate

	attachmentBytes := attachmentRequestBytes(attachments, len(standInToken))

	// fitsDeliberate checks admission bound for a deliberate phase, optionally
	// including attachment bytes. Proposal, critique, integration, repair, and
	// review phases receive attachments; delivery phase does not.
	fits := func(phase string, pb partBudget, msgs []transport.Message, toolDefs []transport.Tool, maxOut int) error {
		includeAttachments := phase == phaseProposal || phase == phaseCritique || phase == phaseIntegration || phase == phaseIntegrationRepair || phase == phaseReview || phase == phaseReviewRepair
		var adm, est int
		if includeAttachments {
			adm = pb.admissionWithAttachments(msgs, toolDefs, attachmentBytes)
			est = pb.estimateWithAttachments(msgs, toolDefs, attachmentBytes)
		} else {
			adm = pb.admission(msgs, toolDefs)
			est = pb.estimate(msgs, toolDefs)
		}
		if !budget.Fits(adm, maxOut, pb.usable()) {
			err := budgetError(budgetContext)
			err.Participant = strPtr(pb.id)
			err.Phase = strPtr(phase)
			err.Admission = i64Ptr(satAdd(adm, maxOut))
			err.Limit = i64Ptr(pb.usable())
			return err
		}
		total = satAdd(total, satAdd(est, maxOut))
		phases = append(phases, wire.PhaseEstimate{Phase: phase, Participant: pb.id, EstimatedTokens: int64(est), MaxOutputTokens: int64(maxOut)})
		return nil
	}

	rnd := zeroReader{}
	proposals := make([]string, len(proposers))
	critiques := make([]string, len(proposers))
	for i, id := range proposers {
		proposals[i] = consts[id].proposalStandIn(eff)
		critiques[i] = consts[id].critiqueStandIn(eff)
	}

	for _, id := range proposers {
		pb := consts[id]
		if err := fits(phaseProposal, pb, prompt.Proposal(doc, input), defs.reviewer, pb.proposerOutput(eff)); err != nil {
			return budgetRecord{}, err
		}
	}
	for i, id := range proposers {
		pb := consts[id]
		msgs, err := prompt.Critique(doc, input, proposals, i, rnd)
		if err != nil {
			return budgetRecord{}, err
		}
		if err := fits(phaseCritique, pb, msgs, defs.reviewer, pb.critiqueOutput(eff)); err != nil {
			return budgetRecord{}, err
		}
	}

	ib := consts[integrator]
	intOut := ib.integratorOutput(eff)
	result := ib.candidateStandIn(eff)
	msgs, err := prompt.Integration(doc, input, proposals, critiques, slices.Index(proposers, integrator), rnd)
	if err != nil {
		return budgetRecord{}, err
	}
	if err := fits(phaseIntegration, ib, msgs, defs.author, intOut); err != nil {
		return budgetRecord{}, err
	}
	if err := fits(phaseIntegrationRepair, ib, prompt.IntegrationRepair(msgs, result), nil, intOut); err != nil {
		return budgetRecord{}, err
	}

	// The candidate and its disposition share the integrator's one bound.
	half := len(result) / 2
	candidate, disposition := result[:half], result[half:]
	base := prompt.ReviewInput{
		Request:       input,
		Candidate:     candidate,
		CandidateHash: standInHash,
		AttemptID:     standInToken,
		Delimiter:     standInToken,
		History:       doc,
	}
	delivery := prompt.DeliveryInput{
		TerminalState:  StateIncomplete,
		TerminalReason: reasonBlock + standInToken,
		CandidateHash:  standInHash,
	}
	for _, id := range reviewers {
		rv := consts[id]
		out := rv.maxOutput(*eff.Budgets.ReviewerMaxOutputTokens)
		output := rv.standIn(out)
		own := slices.Index(proposers, id)
		msgs, err := prompt.DeliberateReview(base, proposals, critiques, disposition, own, rnd)
		if err != nil {
			return budgetRecord{}, err
		}
		if err := fits(phaseReview, rv, msgs, defs.reviewer, out); err != nil {
			return budgetRecord{}, err
		}
		repair, err := prompt.Repair(base, output, standInToken)
		if err != nil {
			return budgetRecord{}, err
		}
		// The repair carries the deliberate review's user message.
		repair[1].Content = msgs[1].Content
		if err := fits(phaseReviewRepair, rv, repair, nil, out); err != nil {
			return budgetRecord{}, err
		}
		delivery.ReviewerOutputs = append(delivery.ReviewerOutputs, prompt.ReviewerOutput{Participant: id, Output: output})
	}

	// Each recipient's delivery carries a block-free delivery; the deliveries
	// go to every participant of the turn.
	dmsgs := prompt.Delivery(delivery)
	for _, id := range ids {
		pb := consts[id]
		out := pb.maxOutput(*eff.Budgets.DeliveryMaxTokens)
		if err := fits(phaseDelivery, pb, dmsgs, nil, out); err != nil {
			return budgetRecord{}, err
		}
	}

	if total > limit {
		err := budgetError(budgetTurn)
		err.Estimated = i64Ptr(total)
		err.Limit = i64Ptr(limit)
		return budgetRecord{}, err
	}
	return budgetRecord{Projected: total, WorstCaseProjected: total, Limit: limit, Phases: phases}, nil
}
