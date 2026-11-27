package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// openTurn is the in-memory cache of the one turn that is not yet terminal.
type openTurn struct {
	id              string
	mode            string
	author          string
	input           string
	history         *history.Document // frozen at submit; no turns when none
	contextHash     string            // hash of the frozen history; empty when none
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
	// outputs is the Text of each accepted reviewer attempt of the current
	// candidate, by attempt id, for the terminal delivery.
	outputs map[string]string

	// Revision state, owned by Engine.mu. round is the round of the current
	// candidate, 0 for the first; maxRounds is the limit in force for the turn.
	// results holds each required reviewer's accepted verdict on the current
	// candidate and is replaced when a revision is frozen.
	round       int
	maxRounds   int
	candidateID string
	// frozenVersion is the snapshot version the current candidate was
	// frozen on.
	frozenVersion int
	changeLines   []string
	// pkt is the change-set diffs and author evidence the current
	// candidate's reviewers receive, set when a reviewer is dispatched.
	pkt      reviewerPacket
	revising bool
	results  map[string]reviewResult

	// Tool state, owned by Engine.mu. workspace is the conversation's
	// workspace root, empty when it has none, in which case no file tool is
	// offered. toolCallsUsed counts the author's calls that counted against
	// tools.max_calls_per_turn; toolLimited is set once the author has been
	// dispatched without definitions because the bound was reached.
	workspace     string
	toolCallsUsed int
	toolLimited   bool
	// screener applies the pre-send checks to the turn's web queries; its
	// canary corpus holds every file-derived byte sequence the turn has
	// carried to the model, seeded from the history at submit and fed by
	// each file-tool effect. It is safe for concurrent use.
	screener *tools.URLScreener
	// reviewerCalls counts each reviewer's calls in the current round against
	// tools.max_reviewer_calls.
	reviewerCalls map[string]int

	// delivering is set once the turn has ended and its terminal deliveries
	// are handed to the scheduler. They run after e.open has moved on.
	delivering    bool
	deliveryState string
}

// submissionHash is the SHA-256 over the canonical JSON of the request
// members that define a submission, as sent. The struct's field order is the
// sorted key order.
func submissionHash(req wire.SubmitRequest) (string, error) {
	body, err := json.Marshal(struct {
		AllowProvisional *bool   `json:"allow_provisional"`
		ContextFromTurn  *string `json:"context_from_turn"`
		ConversationID   *string `json:"conversation_id"`
		Input            string  `json:"input"`
		MaxRounds        *int    `json:"max_rounds"`
		Preset           *string `json:"preset"`
		QuickProfile     *string `json:"quick_profile"`
		Workspace        string  `json:"workspace"`
	}{req.AllowProvisional, req.ContextFromTurn, req.ConversationID, req.Input, req.MaxRounds, req.Preset, req.QuickProfile, req.Workspace})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// revisionLimits returns the round limit a turn runs with when the client
// names none, and the ceiling the client's value is held to, from the resolved
// revision settings. A solo turn has no loop.
func revisionLimits(eff config.Effective) (maxRounds, ceiling int) {
	maxRounds, ceiling = defaultMaxRounds, defaultMaxRoundsCeiling
	if eff.Revision.MaxRounds != nil {
		maxRounds = *eff.Revision.MaxRounds
	}
	if eff.Revision.MaxRoundsCeiling != nil {
		ceiling = *eff.Revision.MaxRoundsCeiling
	}
	return maxRounds, ceiling
}

func strPtr(s string) *string { return &s }

func refusal(code string) *wire.Error { return &wire.Error{Code: code} }

func findParticipant(participants []config.Participant, id string) *config.Participant {
	for i := range participants {
		if participants[i].ID == id {
			return &participants[i]
		}
	}
	return nil
}

// Submit admits a turn, or refuses it with a *wire.Error. A refused
// submission writes nothing.
func (e *Engine) Submit(req wire.SubmitRequest) (wire.SubmitReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		if e.runState == "probing" {
			return wire.SubmitReply{}, refusal(wire.ErrorCodeProbing)
		}
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
		same := prior.SubmissionHash == hash
		if !same {
			if same, err = replaysLegacy(req, prior.SubmissionHash); err != nil {
				return wire.SubmitReply{}, err
			}
		}
		if !same {
			conflict := refusal(wire.ErrorCodeSubmissionConflict)
			conflict.TurnID = strPtr(prior.ID)
			return wire.SubmitReply{}, conflict
		}
		reply := wire.SubmitReply{TurnID: prior.ID, Preset: prior.Preset, ConversationID: prior.ConversationID}
		if prior.ContextHash != nil {
			reply.ContextHash = *prior.ContextHash
		}
		stored, ok, err := readBudget(prior)
		if err != nil {
			return wire.SubmitReply{}, err
		}
		if ok {
			reply.EstimatedTokens = int64(stored.Projected)
			reply.WorstCaseEstimatedTokens = int64(stored.WorstCaseProjected)
		}
		if prior.QuickProfile != "" {
			reply.QuickProfile = strPtr(prior.QuickProfile)
		}
		return reply, nil
	}

	if e.stopping {
		return wire.SubmitReply{}, refusal(wire.ErrorCodeStopping)
	}

	if e.open != nil {
		busy := refusal(wire.ErrorCodeBusy)
		busy.TurnID = strPtr(e.open.id)
		return wire.SubmitReply{}, busy
	}

	if len(e.orphanedHolds) > 0 {
		orphanedErr := refusal(wire.ErrorCodeOrphanedHolds)
		orphanedHolds := make([]wire.Hold, 0, len(e.orphanedHolds))
		for _, h := range e.orphanedHolds {
			orphanedHolds = append(orphanedHolds, wire.Hold{
				Pool:      h.pool,
				AttemptID: h.attemptID,
				Group:     h.group,
			})
		}
		body, err := json.Marshal(orphanedHolds)
		if err != nil {
			return wire.SubmitReply{}, err
		}
		orphanedErr.Holds = body
		return wire.SubmitReply{}, orphanedErr
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

	if eff.Preset.Mode == config.ModeDeliberate {
		invalid := refusal(wire.ErrorCodePresetInvalid)
		invalid.Detail = strPtr("deliberate_mode_unsupported")
		return wire.SubmitReply{}, invalid
	}

	for _, id := range eff.Preset.RequiredReviewers {
		p := e.built.Participants[id]
		if p.EligibleRequiredReviewer == transport.TriNo {
			invalid := refusal(wire.ErrorCodePresetInvalid)
			invalid.Detail = strPtr("ineligible_required_reviewer:" + id)
			return wire.SubmitReply{}, invalid
		}
		if p.EligibleRequiredReviewer == transport.TriUnknown {
			rosterParticipant := findParticipant(e.roster.Participants, id)
			if rosterParticipant == nil || rosterParticipant.AcceptUnknownTruncation == nil || !*rosterParticipant.AcceptUnknownTruncation {
				invalid := refusal(wire.ErrorCodePresetInvalid)
				invalid.Detail = strPtr("unknown_required_reviewer_not_accepted:" + id)
				return wire.SubmitReply{}, invalid
			}
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

	// Check tool capability requirement: tool-granted roles must have tool_calls: yes
	if len(eff.Preset.Tools) > 0 {
		if mp := e.built.Participants[eff.Preset.Author]; mp.ToolCalls != transport.TriYes {
			invalid := refusal(wire.ErrorCodePresetInvalid)
			invalid.Detail = strPtr("no_tool_calls:" + eff.Preset.Author)
			return wire.SubmitReply{}, invalid
		}
	}
	if len(eff.Preset.ReviewerTools) > 0 {
		for _, id := range eff.Preset.RequiredReviewers {
			if mp := e.built.Participants[id]; mp.ToolCalls != transport.TriYes {
				invalid := refusal(wire.ErrorCodePresetInvalid)
				invalid.Detail = strPtr("no_tool_calls:" + id)
				return wire.SubmitReply{}, invalid
			}
		}
	}

	maxRounds, ceiling := revisionLimits(eff)
	if req.MaxRounds != nil {
		if *req.MaxRounds < 0 || *req.MaxRounds > ceiling {
			bad := refusal(wire.ErrorCodeMaxRounds)
			bad.Ceiling = &ceiling
			return wire.SubmitReply{}, bad
		}
		maxRounds = *req.MaxRounds
	}

	attempt := eff.Deadlines.Attempt.Duration
	turn := eff.Deadlines.Turn.Duration
	turnDeadline := e.clock.Now().UTC().Add(turn)

	var (
		turnID, convID, contextHash, workspace string
		doc                                    *history.Document
		taintedTurns                           map[string]bool
		screener                               *tools.URLScreener
		rec                                    budgetRecord
	)
	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		var err error
		convID, err = tx.AdoptConversation(req.ConversationID, req.Workspace)
		switch {
		case errors.Is(err, journal.ErrNoSuchContent):
			unknown := refusal(wire.ErrorCodeNotFound)
			unknown.Detail = strPtr("conversation_unknown")
			return unknown
		case errors.Is(err, journal.ErrWorkspaceMismatch):
			return refusal(wire.ErrorCodeWorkspaceMismatch)
		case err != nil:
			return err
		}
		if req.ConversationID == nil {
			if err := emit("", wire.EventKindConversationCreated, wire.EvConversationCreated{ConversationID: convID}); err != nil {
				return err
			}
		}
		// The tools' workspace is the conversation's (v5 delta section 6),
		// which an earlier submit may have set.
		conv, err := tx.GetConversation(convID)
		if err != nil {
			return err
		}
		if conv.Workspace != nil {
			workspace = *conv.Workspace
		}
		// The history is rendered once, here, and admission, the stored
		// document and the context hash are all taken from these bytes.
		if doc, taintedTurns, err = renderHistory(tx, convID, req.ContextFromTurn); err != nil {
			return err
		}
		if screener, err = e.newTurnScreener(); err != nil {
			return err
		}
		if err := seedHistoryCorpus(tx, screener, doc, taintedTurns); err != nil {
			return err
		}
		// The taint comes from every turn the journal records for the
		// conversation, not from the history in view, so a trim cannot
		// launder it. This turn is tainted too when its preset grants read
		// or write to any role.
		tainted, err := tx.ConversationFileTainted(convID)
		if err != nil {
			return err
		}
		if we := egressRefusal(e.roster, eff, tainted); we != nil {
			return we
		}
		tainted = tainted || eff.Preset.GrantsFileTools()
		if rec, err = e.projectBudget(eff, req.Input, doc, maxRounds); err != nil {
			return err
		}
		budgetJSON, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if eff.Deadlines.Provisional != nil && *eff.Deadlines.Provisional &&
			(req.AllowProvisional == nil || !*req.AllowProvisional) {
			return refusal(wire.ErrorCodeProvisional)
		}
		var ctxHash *string
		if hash, bytes := frozenHistory(doc); bytes != nil {
			if err := tx.StoreHistoryDocument(hash, bytes); err != nil {
				return err
			}
			contextHash, ctxHash = hash, &hash
		}
		turnID, err = tx.CreateEngineTurn(journal.TurnParams{
			ConversationID:    convID,
			ContextHash:       ctxHash,
			FileTainted:       tainted,
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
			MaxRoundsInEffect: &maxRounds,
			MaxRoundsCeiling:  &ceiling,
		})
		if err != nil {
			return err
		}
		submitted := wire.EvTurnSubmitted{
			TurnID:          turnID,
			SubmissionID:    req.SubmissionID,
			Preset:          eff.PresetName,
			EstimatedTokens: int64(rec.Projected),
			ConversationID:  convID,
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
		if we, ok := asWireError(err); ok {
			return wire.SubmitReply{}, we
		}
		return wire.SubmitReply{}, fmt.Errorf("engine: submit: %w", err)
	}

	t := &openTurn{
		id:              turnID,
		mode:            eff.Preset.Mode,
		author:          eff.Preset.Author,
		input:           req.Input,
		history:         doc,
		contextHash:     contextHash,
		eff:             eff,
		attemptDeadline: attempt,
		turnDeadline:    turnDeadline,
		workspace:       workspace,
		screener:        screener,
		required:        eff.Preset.RequiredReviewers,
		approved:        map[string]bool{},
		results:         map[string]reviewResult{},
		maxRounds:       maxRounds,
		stopAttempt:     map[string]func() bool{},
		pending:         map[string]time.Time{},
	}
	e.open = t
	e.armTurn(t)
	if e.open == t {
		go e.runTurn(t)
	}

	reply := wire.SubmitReply{TurnID: turnID, Preset: eff.PresetName, EstimatedTokens: int64(rec.Projected), WorstCaseEstimatedTokens: int64(rec.WorstCaseProjected), ConversationID: convID, ContextHash: contextHash}
	if eff.QuickProfile != "" {
		reply.QuickProfile = strPtr(eff.QuickProfile)
	}
	return reply, nil
}
