package server

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// buildTurnRecord builds a full turn record from the journal.
func buildTurnRecord(store *journal.Store, turnID string) (*wire.TurnReply, error) {
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

	reply := &wire.TurnReply{
		State:      turn.TerminalState,
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
		reply.CandidateHash = &turn.CandidateID
	}

	if turn.TerminalReason != "" {
		reply.TerminalReason = &turn.TerminalReason
	}

	// Build attempt records
	for _, a := range attempts {
		attemptRecord := wire.AttemptRecord{
			ID:                    a.ID,
			Participant:           a.Participant,
			Phase:                 a.Phase,
			Epoch:                 int64(a.Epoch),
			MaxOutputTokens:       int64(*a.MaxOutputTokens),
			EstimatedPromptTokens: int64(*a.EstimatedPromptTokens),
			Status:                *a.Status,
			ScrubCount:            *a.ScrubCount,
			TransportStatus:       *a.TransportStatus,
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
			}
			if a.UsageCompletion != nil {
				v := int64(*a.UsageCompletion)
				uCompletion = &v
			}
			attemptRecord.Usage = &wire.ReportedUsage{
				PromptTokens:     uPrompt,
				CompletionTokens: uCompletion,
			}
		}

		reply.Attempts = append(reply.Attempts, attemptRecord)
	}

	// Build verdict records
	for _, v := range verdicts {
		verdictRecord := wire.VerdictRecord{
			ID:               v.ID,
			AttemptID:        v.AttemptID,
			Parsed:           v.Parsed == "true",
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
		var budget wire.BudgetRecord
		if err := json.Unmarshal(turn.BudgetJSON, &budget); err == nil {
			reply.Budget = budget
		}
	}

	return reply, nil
}

// buildTranscript builds a transcript for a turn, paging with cursor.
func buildTranscript(store *journal.Store, turnID string, cursor *string, limit int) (*wire.TranscriptReply, error) {
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
