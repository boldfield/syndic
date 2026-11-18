package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrNoSuchHold is returned when a hold is not found or already released.
var ErrNoSuchHold = errors.New("no such hold")

// AttemptParams contains parameters for BeginAttemptWith.
type AttemptParams struct {
	TurnID                string
	Participant           string
	Phase                 string
	Epoch                 int
	Deadline              *time.Time
	RepairOf              string
	Delimiter             string
	MaxOutputTokens       int
	EstimatedPromptTokens int
}

// GroupMember represents a member of a resource group.
type GroupMember struct {
	EndpointName string `json:"endpoint_name"`
	Model        string `json:"model"`
}

// Hold represents a capacity hold.
type Hold struct {
	AttemptID      string
	Pool           string
	EndpointName   string
	Model          string
	Group          []GroupMember
	HeldSince      time.Time
	ReleasedAt     *time.Time
	ReleasedHow    string
	ReleasedReason string
}

// BeginAttemptWith inserts a pending attempt with the given parameters.
// It also inserts the first attempt_dispatches row with dispatch_index 0.
func (tx *Tx) BeginAttemptWith(p AttemptParams) (string, error) {
	attemptID, err := generateID()
	if err != nil {
		return "", err
	}

	var deadlineStr *string
	if p.Deadline != nil {
		ds := p.Deadline.Format(time.RFC3339)
		deadlineStr = &ds
	}

	var repairOfPtr *string
	if p.RepairOf != "" {
		repairOfPtr = &p.RepairOf
	}

	dispatchedAt := time.Now().UTC().Format(time.RFC3339)

	journalSeq, err := tx.nextJournalSeq()
	if err != nil {
		return "", err
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, deadline, state, repair_of, delimiter, max_output_tokens, estimated_prompt_tokens, journal_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?)
	`, attemptID, p.TurnID, p.Participant, p.Phase, p.Epoch, dispatchedAt, deadlineStr, repairOfPtr, p.Delimiter, p.MaxOutputTokens, p.EstimatedPromptTokens, journalSeq); err != nil {
		return "", fmt.Errorf("insert attempt: %w", err)
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO attempt_dispatches (attempt_id, dispatch_index, dispatched_at)
		VALUES (?, 0, ?)
	`, attemptID, dispatchedAt); err != nil {
		return "", fmt.Errorf("insert attempt dispatch: %w", err)
	}

	return attemptID, nil
}

// RecordRetryDispatch increments the retry_count and inserts the next attempt_dispatches row.
// It errors unless the attempt is in pending state.
func (tx *Tx) RecordRetryDispatch(attemptID string) (int, error) {
	var state string
	if err := tx.tx.QueryRow(`
		SELECT state FROM attempts WHERE id = ?
	`, attemptID).Scan(&state); err != nil {
		return 0, fmt.Errorf("query attempt state: %w", err)
	}

	if state != "pending" {
		return 0, fmt.Errorf("attempt not in pending state: %s", state)
	}

	var currentRetryCount int
	if err := tx.tx.QueryRow(`
		SELECT COALESCE(retry_count, 0) FROM attempts WHERE id = ?
	`, attemptID).Scan(&currentRetryCount); err != nil {
		return 0, fmt.Errorf("query retry count: %w", err)
	}

	newRetryCount := currentRetryCount + 1

	if _, err := tx.tx.Exec(`
		UPDATE attempts SET retry_count = ? WHERE id = ?
	`, newRetryCount, attemptID); err != nil {
		return 0, fmt.Errorf("update retry count: %w", err)
	}

	dispatchedAt := time.Now().UTC().Format(time.RFC3339)

	if _, err := tx.tx.Exec(`
		INSERT INTO attempt_dispatches (attempt_id, dispatch_index, dispatched_at)
		VALUES (?, ?, ?)
	`, attemptID, newRetryCount, dispatchedAt); err != nil {
		return 0, fmt.Errorf("insert retry dispatch: %w", err)
	}

	return newRetryCount, nil
}

// SetAttemptGate sets the gate_error field for an attempt.
func (tx *Tx) SetAttemptGate(attemptID, gateError string) error {
	if _, err := tx.tx.Exec(`
		UPDATE attempts SET gate_error = ? WHERE id = ?
	`, gateError, attemptID); err != nil {
		return fmt.Errorf("update gate error: %w", err)
	}
	return nil
}

// SetAttemptCommentary sets the commentary field for an attempt.
func (tx *Tx) SetAttemptCommentary(attemptID, commentary string) error {
	if _, err := tx.tx.Exec(`
		UPDATE attempts SET commentary = ? WHERE id = ?
	`, commentary, attemptID); err != nil {
		return fmt.Errorf("update commentary: %w", err)
	}
	return nil
}

// SetAttemptUsage sets the usage fields for an attempt.
func (tx *Tx) SetAttemptUsage(attemptID string, prompt, completion int) error {
	if _, err := tx.tx.Exec(`
		UPDATE attempts SET usage_prompt = ?, usage_completion = ? WHERE id = ?
	`, prompt, completion, attemptID); err != nil {
		return fmt.Errorf("update usage: %w", err)
	}
	return nil
}

// OpenHold stores a capacity hold.
// The Group is sorted by endpoint name then model and stored as JSON.
// Opening a second hold for the same attempt or an empty group is an error.
func (tx *Tx) OpenHold(h Hold) error {
	if len(h.Group) == 0 {
		return fmt.Errorf("group cannot be empty")
	}

	var existingCount int
	if err := tx.tx.QueryRow(`
		SELECT COUNT(*) FROM capacity_holds WHERE attempt_id = ?
	`, h.AttemptID).Scan(&existingCount); err != nil {
		return fmt.Errorf("check existing hold: %w", err)
	}

	if existingCount > 0 {
		return fmt.Errorf("hold already exists for this attempt")
	}

	sorted := make([]GroupMember, len(h.Group))
	copy(sorted, h.Group)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].EndpointName != sorted[j].EndpointName {
			return sorted[i].EndpointName < sorted[j].EndpointName
		}
		return sorted[i].Model < sorted[j].Model
	})

	groupJSON, err := json.Marshal(sorted)
	if err != nil {
		return fmt.Errorf("marshal group: %w", err)
	}

	heldSince := h.HeldSince.UTC().Format(time.RFC3339)

	if _, err := tx.tx.Exec(`
		INSERT INTO capacity_holds (attempt_id, pool, endpoint_name, model, group_json, held_since)
		VALUES (?, ?, ?, ?, ?, ?)
	`, h.AttemptID, h.Pool, h.EndpointName, h.Model, string(groupJSON), heldSince); err != nil {
		return fmt.Errorf("insert hold: %w", err)
	}

	return nil
}

// ReleaseHold releases a hold, recording how and reason.
// It errors if the hold is already released or does not exist.
// Valid values for how are: server_confirmed, late_result, operator, operator_timeout.
func (tx *Tx) ReleaseHold(attemptID, how, reason string) error {
	validHow := map[string]bool{
		"server_confirmed": true,
		"late_result":      true,
		"operator":         true,
		"operator_timeout": true,
	}

	if !validHow[how] {
		return fmt.Errorf("invalid release method: %s", how)
	}

	var currentReleasedAt sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT released_at FROM capacity_holds WHERE attempt_id = ?
	`, attemptID).Scan(&currentReleasedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchHold
		}
		return fmt.Errorf("query hold: %w", err)
	}

	if currentReleasedAt.Valid {
		return ErrNoSuchHold
	}

	releasedAt := time.Now().UTC().Format(time.RFC3339)

	if _, err := tx.tx.Exec(`
		UPDATE capacity_holds SET released_at = ?, released_how = ?, released_reason = ? WHERE attempt_id = ?
	`, releasedAt, how, reason, attemptID); err != nil {
		return fmt.Errorf("update hold: %w", err)
	}

	return nil
}

// UnreleasedHolds returns all holds with null released_at, oldest first.
func (tx *Tx) UnreleasedHolds() ([]Hold, error) {
	rows, err := tx.tx.Query(`
		SELECT attempt_id, pool, endpoint_name, model, group_json, held_since
		FROM capacity_holds
		WHERE released_at IS NULL
		ORDER BY held_since ASC, attempt_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query unreleased holds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var holds []Hold
	for rows.Next() {
		var h Hold
		var heldSinceStr string
		var groupJSONStr string

		if err := rows.Scan(&h.AttemptID, &h.Pool, &h.EndpointName, &h.Model, &groupJSONStr, &heldSinceStr); err != nil {
			return nil, fmt.Errorf("scan hold: %w", err)
		}

		heldSince, err := parseTime(heldSinceStr)
		if err != nil {
			return nil, fmt.Errorf("parse held_since: %w", err)
		}
		h.HeldSince = heldSince

		var group []GroupMember
		if err := json.Unmarshal([]byte(groupJSONStr), &group); err != nil {
			return nil, fmt.Errorf("unmarshal group: %w", err)
		}
		h.Group = group

		holds = append(holds, h)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate holds: %w", err)
	}

	return holds, nil
}

// OpenBlockFor opens a block with an engine-generated ID and stores the reviewer's ID.
func (tx *Tx) OpenBlockFor(verdictID, reviewerBlockID, candidateHash, severity, evidence, remedy string) (string, error) {
	var parsedVerdict sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT parsed_verdict FROM verdicts WHERE id = ?
	`, verdictID).Scan(&parsedVerdict); err != nil {
		return "", fmt.Errorf("query verdict: %w", err)
	}

	if !parsedVerdict.Valid || parsedVerdict.String != "block" {
		return "", fmt.Errorf("verdict is not a block")
	}

	blockID := verdictID + ":" + reviewerBlockID

	if _, err := tx.tx.Exec(`
		INSERT INTO blocks (id, verdict_id, candidate_hash, severity, evidence, remedy, state, reviewer_block_id)
		VALUES (?, ?, ?, ?, ?, ?, 'open', ?)
	`, blockID, verdictID, candidateHash, severity, evidence, remedy, reviewerBlockID); err != nil {
		return "", fmt.Errorf("insert block: %w", err)
	}

	return blockID, nil
}

// Store wrapper methods

// BeginAttemptWith wraps tx.BeginAttemptWith in a Store transaction.
func (s *Store) BeginAttemptWith(p AttemptParams) (attemptID string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		attemptID, txErr = tx.BeginAttemptWith(p)
		return txErr
	})
	return attemptID, err
}

// RecordRetryDispatch wraps tx.RecordRetryDispatch in a Store transaction.
func (s *Store) RecordRetryDispatch(attemptID string) (retryCount int, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		retryCount, txErr = tx.RecordRetryDispatch(attemptID)
		return txErr
	})
	return retryCount, err
}

// SetAttemptGate wraps tx.SetAttemptGate in a Store transaction.
func (s *Store) SetAttemptGate(attemptID, gateError string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.SetAttemptGate(attemptID, gateError)
	})
}

// SetAttemptCommentary wraps tx.SetAttemptCommentary in a Store transaction.
func (s *Store) SetAttemptCommentary(attemptID, commentary string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.SetAttemptCommentary(attemptID, commentary)
	})
}

// SetAttemptUsage wraps tx.SetAttemptUsage in a Store transaction.
func (s *Store) SetAttemptUsage(attemptID string, prompt, completion int) error {
	return s.Tx(func(tx *Tx) error {
		return tx.SetAttemptUsage(attemptID, prompt, completion)
	})
}

// OpenHold wraps tx.OpenHold in a Store transaction.
func (s *Store) OpenHold(h Hold) error {
	return s.Tx(func(tx *Tx) error {
		return tx.OpenHold(h)
	})
}

// ReleaseHold wraps tx.ReleaseHold in a Store transaction.
func (s *Store) ReleaseHold(attemptID, how, reason string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.ReleaseHold(attemptID, how, reason)
	})
}

// UnreleasedHolds wraps tx.UnreleasedHolds in a Store transaction.
func (s *Store) UnreleasedHolds() ([]Hold, error) {
	var holds []Hold
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		holds, txErr = tx.UnreleasedHolds()
		return txErr
	})
	return holds, err
}

// OpenBlockFor wraps tx.OpenBlockFor in a Store transaction.
func (s *Store) OpenBlockFor(verdictID, reviewerBlockID, candidateHash, severity, evidence, remedy string) (blockID string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		blockID, txErr = tx.OpenBlockFor(verdictID, reviewerBlockID, candidateHash, severity, evidence, remedy)
		return txErr
	})
	return blockID, err
}

// SetAttemptEstimate records the estimated prompt token count of an attempt
// whose messages could only be built once its row existed.
func (tx *Tx) SetAttemptEstimate(attemptID string, estimatedPromptTokens int) error {
	if _, err := tx.tx.Exec(`
		UPDATE attempts SET estimated_prompt_tokens = ? WHERE id = ?
	`, estimatedPromptTokens, attemptID); err != nil {
		return fmt.Errorf("update estimated prompt tokens: %w", err)
	}
	return nil
}
