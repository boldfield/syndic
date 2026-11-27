package journal

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrDuplicateSubmission = errors.New("duplicate submission_id")

// AllocateEngineRun allocates a new engine run without a manifest, in "probing" state.
// The manifest is finalized later with FinalizeEngineRunManifest.
// Returns the run ID.
func (s *Store) AllocateEngineRun() (runID string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		runID, txErr = tx.AllocateEngineRun()
		return txErr
	})
	return runID, err
}

// AllocateEngineRun allocates a new engine run without a manifest, in "probing" state.
// Manifest and manifest_hash are NULL until finalization.
func (tx *Tx) AllocateEngineRun() (runID string, err error) {
	runID, err = generateID()
	if err != nil {
		return "", err
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash, kind, state)
		VALUES (?, NULL, NULL, 'engine', 'probing')
	`, runID); err != nil {
		return "", fmt.Errorf("insert allocated engine run: %w", err)
	}

	return runID, nil
}

// FinalizeEngineRunManifest writes the manifest to an allocated engine run and transitions it to "started" state.
// The manifest must be valid JSON; the hash is computed by the store.
func (s *Store) FinalizeEngineRunManifest(runID string, manifest json.RawMessage) (manifestHash string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		manifestHash, txErr = tx.FinalizeEngineRunManifest(runID, manifest)
		return txErr
	})
	return manifestHash, err
}

// FinalizeEngineRunManifest writes the manifest to an allocated engine run and transitions it to "started" state.
// Only succeeds if the run is in "probing" state. Returns error if no row is updated or run is already started.
func (tx *Tx) FinalizeEngineRunManifest(runID string, manifest json.RawMessage) (manifestHash string, err error) {
	if !json.Valid(manifest) {
		return "", fmt.Errorf("invalid JSON manifest")
	}

	manifestHash = computeSHA256(manifest)

	result, err := tx.tx.Exec(`
		UPDATE runs SET manifest = ?, manifest_hash = ?, state = 'started' WHERE id = ? AND state = 'probing'
	`, string(manifest), manifestHash, runID)
	if err != nil {
		return "", fmt.Errorf("finalize engine run manifest: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return "", fmt.Errorf("finalize failed: run not in probing state or run does not exist")
	}

	return manifestHash, nil
}

// AbandonProbingEngineRuns marks every engine run still in "probing" state as
// "abandoned" and returns their ids. A run left probing by a crash never
// received a manifest; recovery closes it so the new run is the only live
// one. Nothing is re-sent: probe requests are journaled as attempts, and
// their uncertain-capacity holds are recovered from those rows.
func (tx *Tx) AbandonProbingEngineRuns() ([]string, error) {
	rows, err := tx.tx.Query(`SELECT id FROM runs WHERE kind = 'engine' AND state = 'probing' ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("query probing runs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan probing run: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate probing runs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close probing runs: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.tx.Exec(`UPDATE runs SET state = 'abandoned' WHERE id = ? AND state = 'probing'`, id); err != nil {
			return nil, fmt.Errorf("abandon probing run: %w", err)
		}
	}
	return ids, nil
}

// RunOpenedSeq returns the seq of the run_opened event for runID. The run id
// in the payload identifies it exactly.
func (s *Store) RunOpenedSeq(runID string) (seq int64, found bool, err error) {
	err = s.db.conn.QueryRow(`
		SELECT id FROM events
		WHERE event_type = 'run_opened' AND json_extract(data, '$.run_id') = ?
		ORDER BY id LIMIT 1
	`, runID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("query run_opened: %w", err)
	}
	return seq, true, nil
}

// CreateProbeRun creates a new run of kind 'probe' with the given manifest, returning the run ID and manifest hash.
// The manifest must be valid JSON; the hash is computed by the store.
func (s *Store) CreateProbeRun(manifest json.RawMessage) (runID, manifestHash string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		runID, manifestHash, txErr = tx.CreateProbeRun(manifest)
		return txErr
	})
	return runID, manifestHash, err
}

// CreateProbeRun creates a new run of kind 'probe' with the given manifest, returning the run ID and manifest hash.
func (tx *Tx) CreateProbeRun(manifest json.RawMessage) (runID, manifestHash string, err error) {
	if !json.Valid(manifest) {
		return "", "", fmt.Errorf("invalid JSON manifest")
	}

	runID, err = generateID()
	if err != nil {
		return "", "", err
	}

	manifestHash = computeSHA256(manifest)

	if _, err := tx.tx.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash, kind, state)
		VALUES (?, ?, ?, 'probe', 'started')
	`, runID, string(manifest), manifestHash); err != nil {
		return "", "", fmt.Errorf("insert probe run: %w", err)
	}

	return runID, manifestHash, nil
}

// CreateEngineRun creates a new run of kind 'engine' with the given manifest, returning the run ID and manifest hash.
// The manifest must be valid JSON; the hash is computed by the store.
// This is the legacy method that combines allocation and finalization; new code should use AllocateEngineRun + FinalizeEngineRunManifest.
func (s *Store) CreateEngineRun(manifest json.RawMessage) (runID, manifestHash string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		runID, manifestHash, txErr = tx.CreateEngineRun(manifest)
		return txErr
	})
	return runID, manifestHash, err
}

// CreateEngineRun creates a new run of kind 'engine' with the given manifest, returning the run ID and manifest hash.
func (tx *Tx) CreateEngineRun(manifest json.RawMessage) (runID, manifestHash string, err error) {
	if !json.Valid(manifest) {
		return "", "", fmt.Errorf("invalid JSON manifest")
	}

	runID, err = generateID()
	if err != nil {
		return "", "", err
	}

	manifestHash = computeSHA256(manifest)

	if _, err := tx.tx.Exec(`
		INSERT INTO runs (id, manifest, manifest_hash, kind, state)
		VALUES (?, ?, ?, 'engine', 'started')
	`, runID, string(manifest), manifestHash); err != nil {
		return "", "", fmt.Errorf("insert engine run: %w", err)
	}

	return runID, manifestHash, nil
}

// ProbeRecord represents a probe run record with its manifest and metadata.
type ProbeRecord struct {
	RunID     string
	CreatedAt time.Time
	Manifest  json.RawMessage
}

// LatestProbeRecord returns the most recently created probe run for the given endpoint and model.
// It considers probe runs (kind='probe') and handles both old bare export format and wrapped format.
// Returns found=false with nil error if no matching record is found.
func (s *Store) LatestProbeRecord(endpointName, model string) (rec ProbeRecord, found bool, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		rec, found, txErr = tx.LatestProbeRecord(endpointName, model)
		return txErr
	})
	return rec, found, err
}

// LatestProbeRecord returns the most recently created probe run for the given endpoint and model.
func (tx *Tx) LatestProbeRecord(endpointName, model string) (ProbeRecord, bool, error) {
	rows, err := tx.tx.Query(`
		SELECT id, created_at, manifest
		FROM runs
		WHERE kind = 'probe'
		ORDER BY created_at DESC, rowid DESC
	`)
	if err != nil {
		return ProbeRecord{}, false, fmt.Errorf("query probe runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var runID, createdAtStr string
		var manifestStr string

		if err := rows.Scan(&runID, &createdAtStr, &manifestStr); err != nil {
			return ProbeRecord{}, false, fmt.Errorf("scan probe run: %w", err)
		}

		manifest := json.RawMessage(manifestStr)

		// Try to match the manifest against both formats
		var matchesEndpoint, matchesModel bool

		// Try bare format first: {endpoint_name, model, ...}
		var bareExport map[string]interface{}
		if err := json.Unmarshal(manifest, &bareExport); err == nil {
			if ep, ok := bareExport["endpoint_name"].(string); ok && ep != "" {
				matchesEndpoint = (ep == endpointName)
			}
			if m, ok := bareExport["model"].(string); ok && m != "" {
				matchesModel = (m == model)
			}
		}

		if matchesEndpoint && matchesModel {
			createdAt, err := parseTime(createdAtStr)
			if err != nil {
				return ProbeRecord{}, false, fmt.Errorf("parse created_at: %w", err)
			}
			return ProbeRecord{
				RunID:     runID,
				CreatedAt: createdAt,
				Manifest:  manifest,
			}, true, nil
		}

		// Try wrapped format: {export: {endpoint_name, model, ...}, ...}
		var wrapped struct {
			Export struct {
				EndpointName string `json:"endpoint_name"`
				Model        string `json:"model"`
			} `json:"export"`
		}
		if err := json.Unmarshal(manifest, &wrapped); err == nil {
			if wrapped.Export.EndpointName != "" && wrapped.Export.Model != "" && wrapped.Export.EndpointName == endpointName && wrapped.Export.Model == model {
				createdAt, err := parseTime(createdAtStr)
				if err != nil {
					return ProbeRecord{}, false, fmt.Errorf("parse created_at: %w", err)
				}
				return ProbeRecord{
					RunID:     runID,
					CreatedAt: createdAt,
					Manifest:  manifest,
				}, true, nil
			}
		}
	}

	if err := rows.Err(); err != nil {
		return ProbeRecord{}, false, fmt.Errorf("iterate probe runs: %w", err)
	}

	return ProbeRecord{}, false, nil
}

// TurnParams contains the parameters for creating an engine turn.
type TurnParams struct {
	RunID             string
	Mode              string
	Preset            string
	QuickProfile      string
	SubmissionID      string
	SubmissionHash    string
	AttemptDeadlineMS int64
	TurnDeadlineMS    int64
	TurnDeadline      time.Time
	BudgetJSON        json.RawMessage
	ConversationID    string
	MaxRoundsInEffect *int
	MaxRoundsCeiling  *int
	// ContextHash is the hash of the frozen history document, nil when the
	// turn has no history. FileTainted is the conversation-wide taint.
	ContextHash *string
	FileTainted bool
	// Attachments are the files read at submit. They are committed with the
	// turn: records, bases, taint (FileTainted is forced on) and the
	// attachments_hash the turn carries, "none" when there are none.
	Attachments []AttachmentContent
}

// CreateEngineTurn creates a new engine turn with the given parameters.
// Returns an error that satisfies errors.Is(err, ErrDuplicateSubmission) on duplicate SubmissionID.
func (tx *Tx) CreateEngineTurn(p TurnParams) (turnID string, err error) {
	if p.SubmissionID == "" {
		return "", fmt.Errorf("SubmissionID is required")
	}

	convID := p.ConversationID
	if convID == "" {
		convID, err = tx.CreateConversation(nil)
		if err != nil {
			return "", fmt.Errorf("create conversation: %w", err)
		}
	}

	turnID, err = generateID()
	if err != nil {
		return "", err
	}

	attachments, attachmentsHash, err := preparedAttachments(p.Attachments)
	if err != nil {
		return "", err
	}

	var deadlineStr *string
	if !p.TurnDeadline.IsZero() {
		ds := p.TurnDeadline.Format(time.RFC3339Nano)
		deadlineStr = &ds
	}

	var budgetJSONStr *string
	if len(p.BudgetJSON) > 0 {
		s := string(p.BudgetJSON)
		budgetJSONStr = &s
	}

	if _, err := tx.tx.Exec(`
		INSERT INTO turns (id, run_id, mode, whole_turn_deadline, submission_id, submission_hash, preset, quick_profile, attempt_deadline_ms, turn_deadline_ms, budget_json, conversation_id, max_rounds_in_effect, max_rounds_ceiling, context_hash, file_tainted, submitted_at, attachments_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, turnID, p.RunID, p.Mode, deadlineStr, p.SubmissionID, p.SubmissionHash, p.Preset, p.QuickProfile, p.AttemptDeadlineMS, p.TurnDeadlineMS, budgetJSONStr, convID, p.MaxRoundsInEffect, p.MaxRoundsCeiling, p.ContextHash, p.FileTainted || len(attachments) > 0, time.Now().UTC().Format(time.RFC3339Nano), attachmentsHash); err != nil {
		if isUniqueConstraintError(err, "submission_id") {
			return "", ErrDuplicateSubmission
		}
		return "", fmt.Errorf("insert engine turn: %w", err)
	}
	if err := tx.recordAttachments(turnID, attachments); err != nil {
		return "", err
	}

	return turnID, nil
}

// TurnRow represents a row from the turns table.
type TurnRow struct {
	ID, RunID, Mode                     string
	TurnDeadline                        *time.Time
	TerminalState, TerminalReason       string
	SubmissionID, SubmissionHash        string
	Epoch                               int
	PhaseAtCancel, Preset, QuickProfile string
	CandidateID                         string
	AttemptDeadlineMS, TurnDeadlineMS   int64
	BudgetJSON                          json.RawMessage
	ConversationID                      string
	MaxRoundsInEffect, MaxRoundsCeiling *int
	ContextHash                         *string
	FileTainted                         bool
	// AttachmentsHash is the turn's attachment manifest hash ("none" when it
	// has no attachments); empty for a turn created before it was recorded.
	AttachmentsHash string
}

// ErrTurnNotFound is returned by GetTurn for an unknown turn id.
var ErrTurnNotFound = errors.New("turn not found")

// GetTurn retrieves a turn by ID.
func (s *Store) GetTurn(turnID string) (*TurnRow, error) {
	var row *TurnRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		row, txErr = tx.GetTurn(turnID)
		return txErr
	})
	return row, err
}

// GetTurn retrieves a turn by ID within a transaction.
func (tx *Tx) GetTurn(turnID string) (*TurnRow, error) {
	row, err := tx.getTurnByID(turnID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTurnNotFound
		}
		return nil, err
	}
	return row, nil
}

// getTurnByID is an unexported helper that retrieves a turn row by ID.
func (tx *Tx) getTurnByID(turnID string) (*TurnRow, error) {
	var row TurnRow
	var deadlineStr sql.NullString
	var terminalState sql.NullString
	var terminalReason sql.NullString
	var submissionID sql.NullString
	var submissionHash sql.NullString
	var phaseAtCancel sql.NullString
	var preset sql.NullString
	var quickProfile sql.NullString
	var candidateID sql.NullString
	var attemptDeadlineMS sql.NullInt64
	var turnDeadlineMS sql.NullInt64
	var budgetJSONStr sql.NullString
	var maxRoundsInEffect sql.NullInt64
	var maxRoundsCeiling sql.NullInt64
	var contextHash sql.NullString
	var conversationID sql.NullString
	var fileTainted bool
	var attachmentsHash sql.NullString

	if err := tx.tx.QueryRow(`
		SELECT id, run_id, mode, whole_turn_deadline, terminal_state, terminal_reason, submission_id, submission_hash, epoch, phase_at_cancel, preset, quick_profile, candidate_id, attempt_deadline_ms, turn_deadline_ms, budget_json, conversation_id, max_rounds_in_effect, max_rounds_ceiling, context_hash, file_tainted, attachments_hash
		FROM turns WHERE id = ?
	`, turnID).Scan(
		&row.ID, &row.RunID, &row.Mode, &deadlineStr, &terminalState, &terminalReason, &submissionID, &submissionHash, &row.Epoch, &phaseAtCancel, &preset, &quickProfile, &candidateID, &attemptDeadlineMS, &turnDeadlineMS, &budgetJSONStr, &conversationID, &maxRoundsInEffect, &maxRoundsCeiling, &contextHash, &fileTainted, &attachmentsHash,
	); err != nil {
		return nil, err
	}

	row.AttachmentsHash = attachmentsHash.String

	if deadlineStr.Valid {
		t, err := parseTime(deadlineStr.String)
		if err != nil {
			return nil, fmt.Errorf("parse whole_turn_deadline: %w", err)
		}
		row.TurnDeadline = &t
	}

	if terminalState.Valid {
		row.TerminalState = terminalState.String
	}

	if terminalReason.Valid {
		row.TerminalReason = terminalReason.String
	}

	if submissionID.Valid {
		row.SubmissionID = submissionID.String
	}

	if submissionHash.Valid {
		row.SubmissionHash = submissionHash.String
	}

	if phaseAtCancel.Valid {
		row.PhaseAtCancel = phaseAtCancel.String
	}

	if preset.Valid {
		row.Preset = preset.String
	}

	if quickProfile.Valid {
		row.QuickProfile = quickProfile.String
	}

	if candidateID.Valid {
		row.CandidateID = candidateID.String
	}

	if conversationID.Valid {
		row.ConversationID = conversationID.String
	}

	if attemptDeadlineMS.Valid {
		row.AttemptDeadlineMS = attemptDeadlineMS.Int64
	}

	if turnDeadlineMS.Valid {
		row.TurnDeadlineMS = turnDeadlineMS.Int64
	}

	if budgetJSONStr.Valid {
		row.BudgetJSON = json.RawMessage(budgetJSONStr.String)
	}

	if maxRoundsInEffect.Valid {
		v := int(maxRoundsInEffect.Int64)
		row.MaxRoundsInEffect = &v
	}

	if maxRoundsCeiling.Valid {
		v := int(maxRoundsCeiling.Int64)
		row.MaxRoundsCeiling = &v
	}

	if contextHash.Valid {
		row.ContextHash = &contextHash.String
	}

	row.FileTainted = fileTainted

	return &row, nil
}

// FindSubmission finds a turn by its submission_id.
func (s *Store) FindSubmission(submissionID string) (turn *TurnRow, found bool, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		turn, found, txErr = tx.FindSubmission(submissionID)
		return txErr
	})
	return turn, found, err
}

// FindSubmission finds a turn by its submission_id within a transaction.
func (tx *Tx) FindSubmission(submissionID string) (*TurnRow, bool, error) {
	var row TurnRow
	var deadlineStr sql.NullString
	var terminalState sql.NullString
	var terminalReason sql.NullString
	var submissionID_ sql.NullString
	var submissionHash sql.NullString
	var phaseAtCancel sql.NullString
	var preset sql.NullString
	var quickProfile sql.NullString
	var candidateID sql.NullString
	var attemptDeadlineMS sql.NullInt64
	var turnDeadlineMS sql.NullInt64
	var budgetJSONStr sql.NullString
	var maxRoundsInEffect sql.NullInt64
	var maxRoundsCeiling sql.NullInt64
	var contextHash sql.NullString
	var fileTainted bool
	var attachmentsHash sql.NullString

	if err := tx.tx.QueryRow(`
		SELECT id, run_id, mode, whole_turn_deadline, terminal_state, terminal_reason, submission_id, submission_hash, epoch, phase_at_cancel, preset, quick_profile, candidate_id, attempt_deadline_ms, turn_deadline_ms, budget_json, conversation_id, max_rounds_in_effect, max_rounds_ceiling, context_hash, file_tainted, attachments_hash
		FROM turns WHERE submission_id = ?
	`, submissionID).Scan(
		&row.ID, &row.RunID, &row.Mode, &deadlineStr, &terminalState, &terminalReason, &submissionID_, &submissionHash, &row.Epoch, &phaseAtCancel, &preset, &quickProfile, &candidateID, &attemptDeadlineMS, &turnDeadlineMS, &budgetJSONStr, &row.ConversationID, &maxRoundsInEffect, &maxRoundsCeiling, &contextHash, &fileTainted, &attachmentsHash,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("query turn by submission_id: %w", err)
	}

	row.AttachmentsHash = attachmentsHash.String

	if deadlineStr.Valid {
		t, err := parseTime(deadlineStr.String)
		if err != nil {
			return nil, false, fmt.Errorf("parse whole_turn_deadline: %w", err)
		}
		row.TurnDeadline = &t
	}

	if terminalState.Valid {
		row.TerminalState = terminalState.String
	}

	if terminalReason.Valid {
		row.TerminalReason = terminalReason.String
	}

	if submissionID_.Valid {
		row.SubmissionID = submissionID_.String
	}

	if submissionHash.Valid {
		row.SubmissionHash = submissionHash.String
	}

	if phaseAtCancel.Valid {
		row.PhaseAtCancel = phaseAtCancel.String
	}

	if preset.Valid {
		row.Preset = preset.String
	}

	if quickProfile.Valid {
		row.QuickProfile = quickProfile.String
	}

	if candidateID.Valid {
		row.CandidateID = candidateID.String
	}

	if attemptDeadlineMS.Valid {
		row.AttemptDeadlineMS = attemptDeadlineMS.Int64
	}

	if turnDeadlineMS.Valid {
		row.TurnDeadlineMS = turnDeadlineMS.Int64
	}

	if budgetJSONStr.Valid {
		row.BudgetJSON = json.RawMessage(budgetJSONStr.String)
	}

	if maxRoundsInEffect.Valid {
		v := int(maxRoundsInEffect.Int64)
		row.MaxRoundsInEffect = &v
	}

	if maxRoundsCeiling.Valid {
		v := int(maxRoundsCeiling.Int64)
		row.MaxRoundsCeiling = &v
	}

	if contextHash.Valid {
		row.ContextHash = &contextHash.String
	}

	row.FileTainted = fileTainted

	return &row, true, nil
}

// OpenTurns returns IDs of turns whose terminal_state is null, oldest first.
func (s *Store) OpenTurns() ([]string, error) {
	var turnIDs []string
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		turnIDs, txErr = tx.OpenTurns()
		return txErr
	})
	return turnIDs, err
}

// OpenTurns returns IDs of turns whose terminal_state is null, oldest first.
func (tx *Tx) OpenTurns() ([]string, error) {
	rows, err := tx.tx.Query(`
		SELECT id FROM turns WHERE terminal_state IS NULL ORDER BY rowid ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query open turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var turnIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan turn id: %w", err)
		}
		turnIDs = append(turnIDs, id)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate open turns: %w", err)
	}

	return turnIDs, nil
}

// SetTurnBudget sets the budget_json for a turn.
func (s *Store) SetTurnBudget(turnID string, budget json.RawMessage) error {
	return s.Tx(func(tx *Tx) error {
		return tx.SetTurnBudget(turnID, budget)
	})
}

// SetTurnBudget sets the budget_json for a turn within a transaction.
func (tx *Tx) SetTurnBudget(turnID string, budget json.RawMessage) error {
	if !json.Valid(budget) {
		return fmt.Errorf("invalid JSON budget")
	}

	if _, err := tx.tx.Exec(`
		UPDATE turns SET budget_json = ? WHERE id = ?
	`, string(budget), turnID); err != nil {
		return fmt.Errorf("update turn budget: %w", err)
	}

	return nil
}

// isUniqueConstraintError checks if an error is a SQLite unique constraint violation for the given column.
func isUniqueConstraintError(err error, columnName string) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	// SQLite returns errors like "UNIQUE constraint failed: turns.submission_id"
	return strings.Contains(errMsg, fmt.Sprintf("UNIQUE constraint failed: turns.%s", columnName))
}
