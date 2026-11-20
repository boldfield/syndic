package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// turnStatePage is how many events a turnStates catch-up reads at a time.
const turnStatePage = 1000

// turnStates follows the journal's turn_state events, so an open turn's
// current state is known without scanning the events table on every turn
// request. Each catch-up reads only the events written since the last one.
// It starts at the current run: recovery makes every turn an earlier run left
// open terminal before engine_started, so the turn_state events of a turn
// that is open now all come after it.
type turnStates struct {
	mu      sync.Mutex
	started bool
	cursor  int64 // seq of the last event read
	states  map[string]string
}

// latest returns the state in the most recent turn_state event of turnID, or
// "" if it has none. runFirstSeq is where the first catch-up starts.
func (t *turnStates) latest(store *journal.Store, runFirstSeq int64, turnID string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		t.started = true
		t.cursor = max(runFirstSeq-1, 0)
		t.states = make(map[string]string)
	}
	for {
		events, err := store.EventsSince(t.cursor, turnStatePage)
		if err != nil {
			return "", err
		}
		for _, ev := range events {
			if ev.Kind == "turn_state" {
				var data struct {
					State string `json:"state"`
				}
				if err := json.Unmarshal(ev.Data, &data); err != nil {
					return "", fmt.Errorf("turn_state event %d: %w", ev.Seq, err)
				}
				t.states[ev.TurnID] = data.State
			}
			t.cursor = ev.Seq
		}
		if len(events) < turnStatePage {
			return t.states[turnID], nil
		}
	}
}

// buildTurnRecord builds a full turn record from the journal.
// openState gives the current state of a turn that has no terminal state.
func buildTurnRecord(store *journal.Store, openState func(turnID string) (string, error), turnID string) (*wire.TurnReply, error) {
	turn, err := store.GetTurn(turnID)
	if err != nil {
		return nil, err
	}

	attempts, err := store.ListAttempts(turnID)
	if err != nil {
		return nil, err
	}

	verdicts, err := store.ListVerdicts(turnID)
	if err != nil {
		return nil, err
	}

	blocks, err := store.ListBlocks(turnID)
	if err != nil {
		return nil, err
	}

	// Get state: terminal state if available, otherwise most recent turn_state event
	state := turn.TerminalState
	if state == "" {
		state, err = openState(turnID)
		if err != nil {
			return nil, err
		}
	}

	reply := &wire.TurnReply{
		State:      state,
		Mode:       turn.Mode,
		Preset:     turn.Preset,
		Attempts:   []wire.AttemptRecord{},
		Verdicts:   []wire.VerdictRecord{},
		OpenBlocks: []wire.OpenBlock{},
	}

	if turn.QuickProfile != "" {
		reply.QuickProfile = &turn.QuickProfile
	}

	if turn.CandidateID != "" {
		candidate, err := store.GetCandidate(turn.CandidateID)
		if err != nil {
			return nil, fmt.Errorf("candidate %s: %w", turn.CandidateID, err)
		}
		reply.CandidateHash = &candidate.ContentHash
	}

	if turn.TerminalReason != "" {
		reply.TerminalReason = &turn.TerminalReason
	}

	// Build attempt records and collect aggregates
	var totalScrubCount int
	var totalEstimatedPrompt int64
	var totalPromptUsage int64
	var totalCompletionUsage int64
	hasUsage := false

	for _, a := range attempts {
		attemptRecord := wire.AttemptRecord{
			ID:          a.ID,
			Participant: a.Participant,
			Phase:       a.Phase,
			Epoch:       int64(a.Epoch),
		}

		if a.MaxOutputTokens != nil {
			attemptRecord.MaxOutputTokens = int64(*a.MaxOutputTokens)
		}

		if a.EstimatedPromptTokens != nil {
			attemptRecord.EstimatedPromptTokens = int64(*a.EstimatedPromptTokens)
			totalEstimatedPrompt += int64(*a.EstimatedPromptTokens)
		}

		if a.Status != nil {
			attemptRecord.Status = *a.Status
		}

		if a.ScrubCount != nil {
			attemptRecord.ScrubCount = *a.ScrubCount
			totalScrubCount += *a.ScrubCount
		}

		if a.TransportStatus != nil {
			attemptRecord.TransportStatus = *a.TransportStatus
		}

		if a.Deadline != nil {
			attemptRecord.Deadline = a.Deadline.Format(time.RFC3339)
		}

		if a.RepairOf != nil {
			attemptRecord.RepairOf = a.RepairOf
		}

		if a.UsagePrompt != nil || a.UsageCompletion != nil {
			var uPrompt, uCompletion *int64
			if a.UsagePrompt != nil {
				v := int64(*a.UsagePrompt)
				uPrompt = &v
				totalPromptUsage += int64(*a.UsagePrompt)
				hasUsage = true
			}
			if a.UsageCompletion != nil {
				v := int64(*a.UsageCompletion)
				uCompletion = &v
				totalCompletionUsage += int64(*a.UsageCompletion)
				hasUsage = true
			}
			attemptRecord.Usage = &wire.ReportedUsage{
				PromptTokens:     uPrompt,
				CompletionTokens: uCompletion,
			}
		}

		reply.Attempts = append(reply.Attempts, attemptRecord)
	}

	// Set aggregate values
	reply.ScrubCount = totalScrubCount
	reply.EstimatedPromptTokens = totalEstimatedPrompt
	if hasUsage {
		reply.ReportedUsage = &wire.ReportedUsage{
			PromptTokens:     &totalPromptUsage,
			CompletionTokens: &totalCompletionUsage,
		}
	}

	// Build verdict records
	for _, v := range verdicts {
		verdictRecord := wire.VerdictRecord{
			ID:               v.ID,
			AttemptID:        v.AttemptID,
			Parsed:           v.Parsed == "approve" || v.Parsed == "block",
			ValidationStatus: v.ValidationStatus,
		}
		reply.Verdicts = append(reply.Verdicts, verdictRecord)
	}

	// Build open block records
	for _, b := range blocks {
		if b.State != "open" {
			continue
		}
		blockRecord := wire.OpenBlock{
			ID:       b.ID,
			Severity: b.Severity,
			State:    b.State,
		}
		reply.OpenBlocks = append(reply.OpenBlocks, blockRecord)
	}

	// Set deadlines if available
	if turn.AttemptDeadlineMS > 0 {
		reply.Deadlines.Attempt = turn.AttemptDeadlineMS
	}
	if turn.TurnDeadlineMS > 0 {
		reply.Deadlines.Turn = turn.TurnDeadlineMS
	}

	// Parse budget JSON
	if turn.BudgetJSON != nil {
		if err := json.Unmarshal(turn.BudgetJSON, &reply.Budget); err != nil {
			return nil, fmt.Errorf("turn %s budget_json: %w", turnID, err)
		}
	}

	return reply, nil
}

// buildTranscript builds a transcript for a turn, paging with cursor.
func buildTranscript(store *journal.Store, turnID string, cursor *string, limit int) (*wire.TranscriptReply, error) {
	// Verify the turn exists
	_, err := store.GetTurn(turnID)
	if err != nil {
		return nil, err
	}

	cursorStr := ""
	if cursor != nil {
		cursorStr = *cursor
	}

	items, nextCursor, err := store.TranscriptPage(turnID, cursorStr, limit)
	if err != nil {
		return nil, err
	}

	reply := &wire.TranscriptReply{
		Items: []wire.TranscriptItem{},
	}

	for _, item := range items {
		// Extract ID from Ref (e.g., "msg:123" -> "123")
		id := item.Ref
		if idx := strings.Index(item.Ref, ":"); idx >= 0 {
			id = item.Ref[idx+1:]
		}

		transcriptItem := wire.TranscriptItem{
			ID:      id,
			Ref:     item.Ref,
			SHA256:  item.SHA256,
			ByteLen: item.ByteLen,
		}
		reply.Items = append(reply.Items, transcriptItem)
	}

	if nextCursor != "" {
		reply.NextCursor = &nextCursor
	}

	return reply, nil
}

// getContent retrieves content for a ref and returns a ContentReply.
func getContent(store *journal.Store, ref string, offset, limit int64) (*wire.ContentReply, error) {
	data, total, sha256sum, err := store.ContentRange(ref, offset, limit)
	if err != nil {
		return nil, err
	}

	encoded := wire.EncodePage(data)

	return &wire.ContentReply{
		Ref:    ref,
		Offset: offset,
		Data:   encoded,
		Total:  total,
		SHA256: sha256sum,
	}, nil
}
