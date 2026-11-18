package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
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
		return wire.SubmitReply{}, err
	}
	if budgetRefusal != nil {
		return wire.SubmitReply{}, budgetRefusal
	}

	maxEstimate := int64(250000)
	if eff.Budgets.TurnMaxEstimatedTokens != nil {
		maxEstimate = int64(*eff.Budgets.TurnMaxEstimatedTokens)
	}

	budgetRecord := wire.BudgetRecord{
		Projected: &projected,
		Reserved:  &projected,
		Charged:   &projected,
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

// projectAndValidateBudget validates basic budget requirements.
// TODO: Implement full budget projection with real prompt builders and all validation checks.
func (e *Engine) projectAndValidateBudget(input string, eff config.Effective) (*wire.Error, int64, error) {
	// For now, just validate that all participants in the preset are probed
	// Full projection and validation will be implemented in the next iteration

	// Check that all required participants are usable
	for _, id := range append([]string{eff.Preset.Author}, eff.Preset.RequiredReviewers...) {
		if p, ok := e.built.Participants[id]; !ok || !p.Usable {
			err := refusal(wire.ErrorCodeUnprobed)
			err.Participant = strPtr(id)
			if !ok {
				err.Detail = strPtr("not_found")
			} else {
				err.Detail = strPtr(p.Detail)
			}
			return err, 0, nil
		}
	}

	// Placeholder projection of 0 for now - this will be replaced with full projection
	return nil, 0, nil
}
