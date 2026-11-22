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
	// outputs is the Text of each accepted reviewer attempt, by attempt id,
	// for the terminal delivery.
	outputs map[string]string

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

// renderHistoryWithData builds a history document from conversation turns with real message data loaded from the tx.
// contextFromTurn filters which turns to include (empty means all).
// Returns the bytes and hash of the canonical document.
func (e *Engine) renderHistoryWithData(tx *journal.Tx, turns []journal.TurnInConversation, contextFromTurn *string) ([]byte, string, error) {
	doc := history.New()
	for _, t := range turns {
		if t.TerminalState == nil {
			continue
		}
		input, err := tx.GetTurnInput(t.ID)
		if err != nil {
			return nil, "", err
		}
		answer, err := tx.GetTurnAnswer(t.ID)
		if err != nil {
			return nil, "", err
		}
		turn := history.Turn{
			ID:     t.ID,
			State:  *t.TerminalState,
			Input:  input,
			Answer: answer,
			Label:  "reviewed",
		}
		if err := doc.AddTurn(turn); err != nil {
			return nil, "", fmt.Errorf("add turn to history: %w", err)
		}
	}

	if contextFromTurn != nil {
		if err := doc.SetContextFromTurn(*contextFromTurn); err != nil {
			return nil, "", fmt.Errorf("set context from turn: %w", err)
		}
	}

	return doc.Bytes(), doc.Hash(), nil
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
		if prior.SubmissionHash != hash {
			conflict := refusal(wire.ErrorCodeSubmissionConflict)
			conflict.TurnID = strPtr(prior.ID)
			return wire.SubmitReply{}, conflict
		}
		reply := wire.SubmitReply{TurnID: prior.ID, Preset: prior.Preset, ConversationID: prior.ConversationID}
		stored, ok, err := readBudget(prior)
		if err != nil {
			return wire.SubmitReply{}, err
		}
		if ok {
			reply.EstimatedTokens = int64(stored.Projected)
		}
		if prior.QuickProfile != "" {
			reply.QuickProfile = strPtr(prior.QuickProfile)
		}
		if prior.ContextHash != nil {
			reply.ContextHash = *prior.ContextHash
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

	// For budget projection, we build history with empty bytes as a conservative estimate.
	// The real history (with actual input/answer bytes) is rendered inside the tx.
	var historyBytes []byte
	if req.ConversationID != nil {
		convID := *req.ConversationID
		conv, err := e.store.GetConversation(convID)
		if err != nil {
			if errors.Is(err, journal.ErrNoSuchContent) {
				bad := refusal(wire.ErrorCodeBadRequest)
				bad.Detail = strPtr("unknown_conversation")
				return wire.SubmitReply{}, bad
			}
			return wire.SubmitReply{}, fmt.Errorf("get conversation: %w", err)
		}

		if conv.Workspace != nil && *conv.Workspace != req.Workspace {
			return wire.SubmitReply{}, refusal(wire.ErrorCodeWorkspaceMismatch)
		}

		turns, err := e.store.GetConversationTurns(convID)
		if err != nil {
			return wire.SubmitReply{}, fmt.Errorf("get conversation turns: %w", err)
		}

		// Build history document with empty input/answer for projection (conservative estimate).
		// Using empty bytes underestimates admission, which is safer: if the projection passes,
		// and the real history is larger, the admission check inside the tx will catch it.
		historyDoc := history.New()
		for _, t := range turns {
			if t.TerminalState == nil {
				continue
			}
			turn := history.Turn{
				ID:     t.ID,
				State:  *t.TerminalState,
				Input:  []byte{},
				Answer: []byte{},
				Label:  "reviewed",
			}
			if err := historyDoc.AddTurn(turn); err != nil {
				return wire.SubmitReply{}, fmt.Errorf("add turn to history: %w", err)
			}
		}

		if req.ContextFromTurn != nil {
			if err := historyDoc.SetContextFromTurn(*req.ContextFromTurn); err != nil {
				return wire.SubmitReply{}, fmt.Errorf("set context from turn: %w", err)
			}
		}

		historyBytes = historyDoc.Bytes()
	}

	rec, err := e.projectBudget(eff, req.Input, historyBytes)
	if err != nil {
		return wire.SubmitReply{}, err
	}
	budgetJSON, err := json.Marshal(rec)
	if err != nil {
		return wire.SubmitReply{}, err
	}

	if eff.Deadlines.Provisional != nil && *eff.Deadlines.Provisional &&
		(req.AllowProvisional == nil || !*req.AllowProvisional) {
		return wire.SubmitReply{}, refusal(wire.ErrorCodeProvisional)
	}

	maxRoundsInEffect := eff.Revision.MaxRounds
	maxRoundsCeiling := eff.Revision.MaxRoundsCeiling
	if req.MaxRounds != nil {
		if *req.MaxRounds < 0 || (maxRoundsCeiling != nil && *req.MaxRounds > *maxRoundsCeiling) {
			bad := refusal(wire.ErrorCodeMaxRounds)
			if maxRoundsCeiling != nil {
				bad.Ceiling = maxRoundsCeiling
			}
			return wire.SubmitReply{}, bad
		}
		maxRoundsInEffect = req.MaxRounds
	}

	attempt := eff.Deadlines.Attempt.Duration
	turnDuration := eff.Deadlines.Turn.Duration
	turnDeadline := e.clock.Now().UTC().Add(turnDuration)

	var turnID string
	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		var err error
		convID := ""
		if req.ConversationID != nil {
			convID = *req.ConversationID
		}

		contextHash := ""
		var frozenHistoryBytes []byte
		conversationTainted := false
		if convID != "" {
			conv, err := tx.GetConversation(convID)
			if err != nil {
				if errors.Is(err, journal.ErrNoSuchContent) {
					bad := refusal(wire.ErrorCodeBadRequest)
					bad.Detail = strPtr("unknown_conversation")
					return bad
				}
				return fmt.Errorf("get conversation: %w", err)
			}

			if conv.Workspace != nil && *conv.Workspace != req.Workspace {
				return refusal(wire.ErrorCodeWorkspaceMismatch)
			}

			if conv.Workspace == nil {
				if err := tx.SetConversationWorkspace(convID, req.Workspace); err != nil {
					return fmt.Errorf("set conversation workspace: %w", err)
				}
			}

			turns, err := tx.GetConversationTurns(convID)
			if err != nil {
				return fmt.Errorf("get conversation turns: %w", err)
			}

			historyBytesRendered, contextHashRendered, err := e.renderHistoryWithData(tx, turns, req.ContextFromTurn)
			if err != nil {
				return fmt.Errorf("render history with data: %w", err)
			}
			contextHash = contextHashRendered
			frozenHistoryBytes = historyBytesRendered

			// Compute conversation-wide transitive taint: if any earlier turn is tainted,
			// this conversation is tainted, so the new turn will be marked tainted too.
			for _, t := range turns {
				if t.TerminalState != nil {
					turn, err := tx.GetTurn(t.ID)
					if err != nil {
						return fmt.Errorf("get turn for taint check: %w", err)
					}
					if turn.FileTainted {
						conversationTainted = true
						break
					}
				}
			}
		}

		turnID, err = tx.CreateEngineTurn(journal.TurnParams{
			RunID:             e.runID,
			Mode:              eff.Preset.Mode,
			Preset:            eff.PresetName,
			QuickProfile:      eff.QuickProfile,
			SubmissionID:      req.SubmissionID,
			SubmissionHash:    hash,
			AttemptDeadlineMS: attempt.Milliseconds(),
			TurnDeadlineMS:    turnDuration.Milliseconds(),
			TurnDeadline:      turnDeadline,
			BudgetJSON:        budgetJSON,
			ConversationID:    convID,
			MaxRoundsInEffect: maxRoundsInEffect,
			MaxRoundsCeiling:  maxRoundsCeiling,
			ContextHash:       contextHash,
			Workspace:         req.Workspace,
		})
		if err != nil {
			return err
		}
		if len(frozenHistoryBytes) > 0 {
			if err := tx.SetTurnHistoryBytes(turnID, frozenHistoryBytes); err != nil {
				return fmt.Errorf("store history bytes: %w", err)
			}
		}

		// Persist transitive taint: if the conversation has any tainted turns,
		// this new turn is also tainted.
		if conversationTainted {
			if err := tx.SetTurnFileTainted(turnID, true); err != nil {
				return fmt.Errorf("set turn file tainted: %w", err)
			}
		}

		submitted := wire.EvTurnSubmitted{
			TurnID:          turnID,
			SubmissionID:    req.SubmissionID,
			Preset:          eff.PresetName,
			EstimatedTokens: int64(rec.Projected),
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

	turn, fetchErr := e.store.GetTurn(turnID)
	if fetchErr != nil {
		return wire.SubmitReply{}, fmt.Errorf("engine: get turn: %w", fetchErr)
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

	reply := wire.SubmitReply{TurnID: turnID, Preset: eff.PresetName, EstimatedTokens: int64(rec.Projected), ConversationID: turn.ConversationID}
	if eff.QuickProfile != "" {
		reply.QuickProfile = strPtr(eff.QuickProfile)
	}
	if turn.ContextHash != nil {
		reply.ContextHash = *turn.ContextHash
	}
	return reply, nil
}
