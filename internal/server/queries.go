package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/boldfield/syndic/internal/history"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/verdict"
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
		// Read the turn row again, after the state, so a turn that ended in
		// between is not reported with a terminal state but no candidate.
		turn, err = store.GetTurn(turnID)
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
		reply.CandidateID = &turn.CandidateID
	}

	if turn.TerminalReason != "" {
		reply.TerminalReason = &turn.TerminalReason
	}

	ref, err := store.TurnInputRef(turnID)
	if err != nil {
		return nil, err
	}
	if ref != "" {
		reply.InputRef = &ref
	}
	if turn.ContextHash != nil {
		from, err := contextFromTurn(store, *turn.ContextHash)
		if err != nil {
			return nil, err
		}
		reply.ContextFromTurn = &from
	}
	if turn.Mode != "solo" {
		reply.MaxRounds = turn.MaxRoundsInEffect
		info, err := store.GetRoundInfo(turnID)
		if err != nil {
			return nil, err
		}
		reply.Round = info.CurrentRound
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
			Quarantined: a.Quarantined,
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

	// Build verdict records. A verdict parsed unless it was malformed: an
	// approve-with-changes parses but stores no decision, so the stored
	// decision cannot tell.
	for _, v := range verdicts {
		verdictRecord := wire.VerdictRecord{
			ID:               v.ID,
			AttemptID:        v.AttemptID,
			Parsed:           v.ValidationStatus != string(verdict.Malformed),
			ValidationStatus: v.ValidationStatus,
			Reason:           v.Reason,
		}
		// Get verdict evidence (tool results and observations it used)
		evidence, err := store.GetVerdictEvidence(v.ID)
		if err != nil {
			if !errors.Is(err, journal.ErrNoSuchContent) {
				return nil, fmt.Errorf("verdict %s evidence: %w", v.ID, err)
			}
			// No evidence is the normal case; evidence is only recorded for tool-based verdicts
		}
		verdictRecord.ResultIDs = evidence.ResultIDs
		verdictRecord.ObservationIDs = evidence.ObservationIDs

		// Populate tool calls, results, and observations for protocol exposure
		verdictRecord.ToolCalls = []wire.ToolCall{}
		verdictRecord.ToolResults = []wire.ToolResult{}
		verdictRecord.Observations = []wire.Observation{}

		// Fetch tool calls and results through a transaction
		if err := store.Tx(func(tx *journal.Tx) error {
			for _, callID := range evidence.ResultIDs {
				call, err := tx.GetToolCall(callID)
				if err == nil && call.Result != nil {
					// Add tool call
					verdictRecord.ToolCalls = append(verdictRecord.ToolCalls, wire.ToolCall{
						ID:        call.ID,
						AttemptID: call.AttemptID,
						Name:      call.Name,
						ArgsHash:  call.ArgsSHA256,
						Status:    call.Result.Status,
					})
					// Add tool result with content ref
					verdictRecord.ToolResults = append(verdictRecord.ToolResults, wire.ToolResult{
						ID:      call.Result.ResultSHA256[:8],
						CallID:  call.ID,
						SHA256:  call.Result.ResultSHA256,
						ByteLen: call.Result.ResultByteLen,
						Status:  call.Result.Status,
					})
				}
			}
			// Fetch observations
			allObs, err := tx.Observations(turnID)
			if err == nil {
				for _, obs := range allObs {
					for _, obsID := range evidence.ObservationIDs {
						if obs.ID == obsID && obs.ContentSHA256 != "" {
							verdictRecord.Observations = append(verdictRecord.Observations, wire.Observation{
								Path:    obs.Path,
								Kind:    obs.Kind,
								SHA256:  obs.ContentSHA256,
								ByteLen: 0,
							})
						}
					}
				}
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("populating verdict evidence: %w", err)
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
			Evidence: b.Evidence,
			Remedy:   b.Remedy,
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

	// Get change set state if the turn has a candidate
	cs, err := store.ChangeSetForTurn(turnID)
	if err == nil {
		csState := &wire.ChangeSetState{
			State: cs.State,
			Paths: make([]wire.ChangeSetPath, len(cs.Paths)),
		}
		for i, p := range cs.Paths {
			csState.Paths[i] = wire.ChangeSetPath{
				Path:    p.Path,
				Outcome: p.Outcome,
			}
		}
		reply.ChangeSet = csState
	} else if !errors.Is(err, journal.ErrNoChangeSet) {
		return nil, fmt.Errorf("change set for turn %s: %w", turnID, err)
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

// contextFromTurn is the first earlier turn the frozen history document under
// hash carries: the trim point the turn was submitted with, or the
// conversation's first turn when nothing was trimmed.
func contextFromTurn(store *journal.Store, hash string) (string, error) {
	b, err := store.HistoryDocument(hash)
	if err != nil {
		return "", err
	}
	doc, err := history.Parse(b)
	if err != nil {
		return "", fmt.Errorf("history document %s: %w", hash, err)
	}
	included := doc.Included()
	if len(included) == 0 {
		return "", fmt.Errorf("history document %s carries no turn", hash)
	}
	return included[0].ID, nil
}

// errReplyTooLarge reports a reply that cannot fit in one frame even trimmed
// as far as its contract allows; it is answered error{code: "frame"}.
var errReplyTooLarge = errors.New("reply exceeds the frame limit")

// replyFits reports whether body, sent as the reply to id, encodes into one
// frame the way the wire Writer writes it (version set, newline included).
func replyFits(id string, body any) bool {
	f, err := wire.NewReply(id, body)
	if err != nil {
		return false
	}
	f.V = wire.ProtocolVersion
	data, err := json.Marshal(f)
	return err == nil && len(data)+1 <= wire.MaxFrameBytes
}

// fitConversationsPage keeps the longest prefix of items whose reply fits in
// one frame. A page that is cut resumes after the last kept item, through
// itemCursors[i]; one that is not keeps next. A first item that does not fit
// alone is errReplyTooLarge.
func fitConversationsPage(id string, items []wire.ConversationSummary, itemCursors []string, next string) (*wire.ConversationsReply, error) {
	reply := &wire.ConversationsReply{Items: items}
	if next != "" {
		reply.NextCursor = &next
	}
	if replyFits(id, reply) {
		return reply, nil
	}
	// Frame size grows with the item count, so the longest fitting prefix is found by bisection.
	lo, hi := 0, len(items)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		cursor := itemCursors[mid-1]
		if replyFits(id, &wire.ConversationsReply{Items: items[:mid], NextCursor: &cursor}) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 {
		return nil, errReplyTooLarge
	}
	cursor := itemCursors[lo-1]
	return &wire.ConversationsReply{Items: items[:lo], NextCursor: &cursor}, nil
}

// listConversations answers a conversations request from the journal.
func listConversations(store *journal.Store, id string, workspace *string, cursor string, limit int) (*wire.ConversationsReply, error) {
	page, err := store.ListConversationsPage(workspace, cursor, limit)
	if err != nil {
		return nil, err
	}
	items := make([]wire.ConversationSummary, len(page.Rows))
	for i, c := range page.Rows {
		lastSubmittedAt := ""
		if c.LastSubmittedAt != nil {
			lastSubmittedAt = c.LastSubmittedAt.Format(time.RFC3339)
		}
		items[i] = wire.ConversationSummary{
			ID:              c.ID,
			CreatedAt:       c.CreatedAt.Format(time.RFC3339),
			Workspace:       c.Workspace,
			LastSubmittedAt: lastSubmittedAt,
			TurnCount:       c.TurnCount,
			Running:         c.Running,
			Preview:         c.Preview,
		}
	}
	return fitConversationsPage(id, items, page.ItemCursors, page.NextCursor)
}

// getConversation answers a conversation request from the journal. The id may
// be any unique prefix; the reply names the full id. A turn that has not
// ended reports its latest journaled turn_state.
func getConversation(store *journal.Store, openState func(turnID string) (string, error), id, prefix string) (*wire.ConversationReply, error) {
	conv, err := store.GetConversation(prefix)
	if err != nil {
		return nil, err
	}
	turns, err := store.GetConversationTurns(conv.ID)
	if err != nil {
		return nil, err
	}
	reply := &wire.ConversationReply{ID: conv.ID, Turns: make([]wire.ConversationTurnSummary, len(turns))}
	for i, t := range turns {
		state := ""
		if t.TerminalState != nil {
			state = *t.TerminalState
		} else if state, err = openState(t.ID); err != nil {
			return nil, err
		} else if state == "" {
			state = "submitted"
		}
		summary := wire.ConversationTurnSummary{ID: t.ID, State: state}

		// Add review_count for non-solo turns
		turnRow, err := store.GetTurn(t.ID)
		if err != nil {
			return nil, err
		}
		if turnRow.Mode != "solo" {
			count, err := store.ReviewCount(t.ID)
			if err != nil {
				return nil, err
			}
			summary.ReviewCount = &count
		}

		reply.Turns[i] = summary
	}
	if !replyFits(id, reply) {
		return nil, errReplyTooLarge
	}
	return reply, nil
}
