package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

// openTurn is the in-memory cache of the one turn that is not yet terminal.
type openTurn struct {
	id              string
	mode            string
	author          string
	input           string
	eff             config.Effective
	attemptDeadline time.Duration
	turnDeadline    time.Time

	// Timers and in-flight attempts, owned by Engine.mu.
	stopTurn    func() bool
	stopTick    func() bool
	stopAttempt map[string]func() bool
	pending     map[string]time.Time // attempt id to its deadline

	reviewersBegun int

	// Review state, owned by Engine.mu.
	required      []string
	candidate     string
	candidateHash string
	approved      map[string]bool
}

// submissionHash is the SHA-256 over the canonical JSON of the request
// members that define a submission, as sent. The struct's field order is the
// sorted key order.
func submissionHash(req wire.SubmitRequest) (string, error) {
	body, err := json.Marshal(struct {
		AllowProvisional *bool   `json:"allow_provisional"`
		Input            string  `json:"input"`
		Preset           *string `json:"preset"`
		QuickProfile     *string `json:"quick_profile"`
	}{req.AllowProvisional, req.Input, req.Preset, req.QuickProfile})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func strPtr(s string) *string { return &s }

func refusal(code string) *wire.Error { return &wire.Error{Code: code} }

// Submit admits a turn, or refuses it with a *wire.Error. A refused
// submission writes nothing.
func (e *Engine) Submit(req wire.SubmitRequest) (wire.SubmitReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		return wire.SubmitReply{}, errors.New("engine: not started")
	}

	if req.SubmissionID == "" {
		bad := refusal(wire.ErrorCodeBadRequest)
		bad.Detail = strPtr("submission_id_required")
		return wire.SubmitReply{}, bad
	}
	hash, err := submissionHash(req)
	if err != nil {
		return wire.SubmitReply{}, err
	}
	prior, found, err := e.store.FindSubmission(req.SubmissionID)
	if err != nil {
		return wire.SubmitReply{}, err
	}
	if found {
		if prior.SubmissionHash != hash {
			conflict := refusal(wire.ErrorCodeSubmissionConflict)
			conflict.TurnID = strPtr(prior.ID)
			return wire.SubmitReply{}, conflict
		}
		reply := wire.SubmitReply{TurnID: prior.ID, Preset: prior.Preset}
		if prior.QuickProfile != "" {
			reply.QuickProfile = strPtr(prior.QuickProfile)
		}
		return reply, nil
	}

	if e.open != nil {
		busy := refusal(wire.ErrorCodeBusy)
		busy.TurnID = strPtr(e.open.id)
		return wire.SubmitReply{}, busy
	}

	presetName, quickName := "", ""
	if req.Preset != nil {
		presetName = *req.Preset
	}
	if req.QuickProfile != nil {
		quickName = *req.QuickProfile
	}
	eff, err := e.roster.Resolve(presetName, quickName)
	switch {
	case errors.Is(err, config.ErrUnknownQuickProfile):
		unknown := refusal(wire.ErrorCodeQuickProfileUnknown)
		unknown.QuickProfile = strPtr(quickName)
		return wire.SubmitReply{}, unknown
	case errors.Is(err, config.ErrUnknownPreset):
		invalid := refusal(wire.ErrorCodePresetInvalid)
		invalid.Detail = strPtr(err.Error())
		return wire.SubmitReply{}, invalid
	case err != nil:
		return wire.SubmitReply{}, err
	}

	for _, id := range eff.Preset.RequiredReviewers {
		if e.built.Participants[id].EligibleRequiredReviewer == transport.TriNo {
			invalid := refusal(wire.ErrorCodePresetInvalid)
			invalid.Detail = strPtr("ineligible_required_reviewer:" + id)
			return wire.SubmitReply{}, invalid
		}
	}

	for _, id := range append([]string{eff.Preset.Author}, eff.Preset.RequiredReviewers...) {
		if mp := e.built.Participants[id]; !mp.Usable {
			unprobed := refusal(wire.ErrorCodeUnprobed)
			unprobed.Participant = strPtr(id)
			if mp.Detail != "" {
				unprobed.Detail = strPtr(mp.Detail)
			}
			return wire.SubmitReply{}, unprobed
		}
	}

	if eff.Deadlines.Provisional != nil && *eff.Deadlines.Provisional &&
		(req.AllowProvisional == nil || !*req.AllowProvisional) {
		return wire.SubmitReply{}, refusal(wire.ErrorCodeProvisional)
	}

	// Check budget at submit time
	budgetRefusal, projected, err := e.projectAndValidateBudget(req.Input, eff)
	if err != nil {
		return wire.SubmitReply{}, fmt.Errorf("projectAndValidateBudget: %w", err)
	}
	if budgetRefusal != nil {
		return wire.SubmitReply{}, budgetRefusal
	}

	maxEstimate := int64(250000)
	if eff.Budgets.TurnMaxEstimatedTokens != nil {
		maxEstimate = int64(*eff.Budgets.TurnMaxEstimatedTokens)
	}

	zero := int64(0)
	budgetRecord := wire.BudgetRecord{
		Projected: &projected,
		Reserved:  &zero,
		Charged:   &zero,
		Limit:     &maxEstimate,
		Exhausted: false,
	}
	budgetJSON, err := json.Marshal(budgetRecord)
	if err != nil {
		return wire.SubmitReply{}, fmt.Errorf("engine: marshal budget: %w", err)
	}

	attempt := eff.Deadlines.Attempt.Duration
	turn := eff.Deadlines.Turn.Duration
	turnDeadline := e.clock.Now().UTC().Add(turn)

	var turnID string
	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		var err error
		turnID, err = tx.CreateEngineTurn(journal.TurnParams{
			RunID:             e.runID,
			Mode:              eff.Preset.Mode,
			Preset:            eff.PresetName,
			QuickProfile:      eff.QuickProfile,
			SubmissionID:      req.SubmissionID,
			SubmissionHash:    hash,
			AttemptDeadlineMS: attempt.Milliseconds(),
			TurnDeadlineMS:    turn.Milliseconds(),
			TurnDeadline:      turnDeadline,
			BudgetJSON:        budgetJSON,
		})
		if err != nil {
			return err
		}
		submitted := wire.EvTurnSubmitted{
			TurnID:       turnID,
			SubmissionID: req.SubmissionID,
			Preset:       eff.PresetName,
		}
		if eff.QuickProfile != "" {
			submitted.QuickProfile = strPtr(eff.QuickProfile)
		}
		if err := emit(turnID, wire.EventKindTurnSubmitted, submitted); err != nil {
			return err
		}
		return emit(turnID, wire.EventKindTurnState, wire.EvTurnState{TurnID: turnID, State: StateSubmitted})
	})
	if err != nil {
		return wire.SubmitReply{}, fmt.Errorf("engine: submit: %w", err)
	}

	t := &openTurn{
		id:              turnID,
		mode:            eff.Preset.Mode,
		author:          eff.Preset.Author,
		input:           req.Input,
		eff:             eff,
		attemptDeadline: attempt,
		turnDeadline:    turnDeadline,
		required:        eff.Preset.RequiredReviewers,
		approved:        map[string]bool{},
		stopAttempt:     map[string]func() bool{},
		pending:         map[string]time.Time{},
	}
	e.open = t
	e.armTurn(t)
	if e.open == t {
		go e.runTurn(t)
	}

	// Extract estimated tokens from budget JSON
	estimatedTokens := projected

	reply := wire.SubmitReply{TurnID: turnID, Preset: eff.PresetName, EstimatedTokens: estimatedTokens}
	if eff.QuickProfile != "" {
		reply.QuickProfile = strPtr(eff.QuickProfile)
	}
	return reply, nil
}

// projectAndValidateBudget validates budget requirements and projects token usage
// according to section 4a of v4-delta-engine-tui.md. It checks for spending_ceiling,
// context_unknown, per-recipient admission bounds, and aggregate turn budgets,
// and returns the projected token count if all checks pass.
func (e *Engine) projectAndValidateBudget(input string, eff config.Effective) (*wire.Error, int64, error) {
	// Extract budget parameters
	messageOverhead := 8
	if eff.Budgets.MessageOverheadTokens != nil {
		messageOverhead = *eff.Budgets.MessageOverheadTokens
	}
	requestOverhead := 16
	if eff.Budgets.RequestOverheadTokens != nil {
		requestOverhead = *eff.Budgets.RequestOverheadTokens
	}
	contextMargin := 0.10
	if eff.Budgets.ContextMargin != nil {
		contextMargin = *eff.Budgets.ContextMargin
	}
	authorMaxOutput := 4096
	if eff.Budgets.AuthorMaxOutputTokens != nil {
		authorMaxOutput = *eff.Budgets.AuthorMaxOutputTokens
	}
	reviewerMaxOutput := 2048
	if eff.Budgets.ReviewerMaxOutputTokens != nil {
		reviewerMaxOutput = *eff.Budgets.ReviewerMaxOutputTokens
	}
	deliveryMaxTokens := 64
	if eff.Budgets.DeliveryMaxTokens != nil {
		deliveryMaxTokens = *eff.Budgets.DeliveryMaxTokens
	}
	turnMaxEstimated := int64(250000)
	if eff.Budgets.TurnMaxEstimatedTokens != nil {
		turnMaxEstimated = int64(*eff.Budgets.TurnMaxEstimatedTokens)
	}

	overheads := budget.Overheads{
		MessageTokens: messageOverhead,
		RequestTokens: requestOverhead,
	}

	// Find participant by id
	findParticipant := func(id string) *config.Participant {
		for i := range e.roster.Participants {
			if e.roster.Participants[i].ID == id {
				return &e.roster.Participants[i]
			}
		}
		return nil
	}

	// Find endpoint by name
	findEndpoint := func(name string) *config.EndpointProfile {
		for i := range e.roster.Endpoints {
			if e.roster.Endpoints[i].Name == name {
				return &e.roster.Endpoints[i]
			}
		}
		return nil
	}

	// Check spending_ceiling and context_unknown for all participants
	allParticipants := append([]string{eff.Preset.Author}, eff.Preset.RequiredReviewers...)
	for _, id := range allParticipants {
		rosterParticipant := findParticipant(id)
		if rosterParticipant == nil {
			return nil, 0, fmt.Errorf("participant %q not found in roster", id)
		}
		ep := findEndpoint(rosterParticipant.Endpoint)
		if ep == nil {
			return nil, 0, fmt.Errorf("endpoint %q not found in roster", rosterParticipant.Endpoint)
		}

		// Check spending_ceiling for metered endpoints
		if ep.DeclaredBilling == "metered" {
			err := refusal(wire.ErrorCodeBudget)
			err.Budget = strPtr("spending_ceiling")
			err.Participant = strPtr(id)
			return err, 0, nil
		}

		// Check context_unknown: tokens_per_byte_max on endpoint
		if ep.TokensPerByteMax == nil {
			err := refusal(wire.ErrorCodeBudget)
			err.Budget = strPtr("context_unknown")
			err.Participant = strPtr(id)
			err.Missing = strPtr("tokens_per_byte_max")
			return err, 0, nil
		}

		// Check context_unknown: context_bound on participant
		p := e.built.Participants[id]
		if p.ContextBound == nil {
			err := refusal(wire.ErrorCodeBudget)
			err.Budget = strPtr("context_unknown")
			err.Participant = strPtr(id)
			err.Missing = strPtr("context_bound")
			return err, 0, nil
		}

		// Check context_unknown: bytes_per_token on participant
		if p.BytesPerToken == nil {
			err := refusal(wire.ErrorCodeBudget)
			err.Budget = strPtr("context_unknown")
			err.Participant = strPtr(id)
			err.Missing = strPtr("bytes_per_token")
			return err, 0, nil
		}
	}

	// Build author prompt and measure it
	authorParticipant := findParticipant(eff.Preset.Author)
	authorEndpoint := findEndpoint(authorParticipant.Endpoint)
	authorBuilt := e.built.Participants[eff.Preset.Author]

	authorMessages := prompt.Author(input)
	authorAdmissionTokens := budget.AdmissionTokens(authorMessages, *authorEndpoint.TokensPerByteMax, overheads)
	authorEstimateTokens := budget.EstimateTokens(authorMessages, *authorBuilt.BytesPerToken, overheads)

	// Clamp author output to endpoint bound if present
	authorOutput := budget.ClampOutput(authorMaxOutput, authorBuilt.OutputBound)

	// Check author admission
	authorUsableBound := budget.UsableBound(*authorBuilt.ContextBound, contextMargin)
	if !budget.Fits(authorAdmissionTokens, authorOutput, authorUsableBound) {
		err := refusal(wire.ErrorCodeBudget)
		err.Budget = strPtr("context")
		err.Participant = strPtr(eff.Preset.Author)
		err.Admission = int64Ptr(int64(authorAdmissionTokens + authorOutput))
		err.Limit = int64Ptr(int64(authorUsableBound))
		return err, 0, nil
	}

	// Project aggregate tokens: author + each reviewer + delivery
	aggregateProjected := int64(0)
	aggregateProjected += int64(authorEstimateTokens + authorOutput)

	// Measure reviewer prompts (use stand-ins)
	for _, reviewerID := range eff.Preset.RequiredReviewers {
		reviewerParticipant := findParticipant(reviewerID)
		reviewerEndpoint := findEndpoint(reviewerParticipant.Endpoint)
		reviewerBuilt := e.built.Participants[reviewerID]

		// Create stand-in candidate from author output projection
		authorStandInBytes := budget.StandInBytes(authorOutput, *reviewerBuilt.BytesPerToken)

		// Build reviewer prompt with stand-ins
		reviewerInput := prompt.ReviewInput{
			Request:       input,
			Candidate:     string(make([]byte, authorStandInBytes)),
			CandidateHash: "0000000000000000000000000000000000000000000000000000000000000000",
			AttemptID:     "000000000000-0000-0000-0000-000000000000",
			Delimiter:     "00000000000000000000000000000000",
		}

		reviewerMessages, err := prompt.Reviewer(reviewerInput)
		if err != nil {
			return nil, 0, fmt.Errorf("build reviewer prompt: %w", err)
		}

		reviewerAdmissionTokens := budget.AdmissionTokens(reviewerMessages, *reviewerEndpoint.TokensPerByteMax, overheads)
		reviewerEstimateTokens := budget.EstimateTokens(reviewerMessages, *reviewerBuilt.BytesPerToken, overheads)
		reviewerOutput := budget.ClampOutput(reviewerMaxOutput, reviewerBuilt.OutputBound)

		// Check reviewer admission
		reviewerUsableBound := budget.UsableBound(*reviewerBuilt.ContextBound, contextMargin)
		if !budget.Fits(reviewerAdmissionTokens, reviewerOutput, reviewerUsableBound) {
			err := refusal(wire.ErrorCodeBudget)
			err.Budget = strPtr("context")
			err.Participant = strPtr(reviewerID)
			err.Admission = int64Ptr(int64(reviewerAdmissionTokens + reviewerOutput))
			err.Limit = int64Ptr(int64(reviewerUsableBound))
			return err, 0, nil
		}

		// Add reviewer projection (prompt + output + repair prompt + output)
		aggregateProjected += int64(reviewerEstimateTokens + reviewerOutput)
		// Repair: same as reviewer prompt + output
		aggregateProjected += int64(reviewerEstimateTokens + reviewerOutput)
	}

	// Add delivery projection for each participant
	for range allParticipants {
		aggregateProjected += int64(deliveryMaxTokens)
	}

	// Check aggregate against turn budget
	if aggregateProjected > turnMaxEstimated {
		err := refusal(wire.ErrorCodeBudget)
		err.Budget = strPtr("turn_max_estimated_tokens")
		err.Estimated = int64Ptr(aggregateProjected)
		err.Limit = int64Ptr(turnMaxEstimated)
		return err, 0, nil
	}

	return nil, aggregateProjected, nil
}

func int64Ptr(i int64) *int64 { return &i }
