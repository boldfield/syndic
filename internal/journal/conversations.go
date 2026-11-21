package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ConversationRow represents a conversation from the conversations table with aggregated turn data.
type ConversationRow struct {
	ID              string
	CreatedAt       time.Time
	Workspace       *string
	LastSubmittedAt *time.Time
	TurnCount       int
	Running         bool
	Preview         string
}

// TurnInConversation represents a turn in a conversation with its state.
type TurnInConversation struct {
	ID            string
	TerminalState *string
	SubmittedAt   *time.Time
}

// ErrAmbiguousConversationID is returned when a prefix matches multiple conversations.
var ErrAmbiguousConversationID = errors.New("ambiguous conversation ID")

// ErrWorkspaceMismatch is returned when a conversation's workspace doesn't match the requested one.
var ErrWorkspaceMismatch = errors.New("workspace_mismatch")

// ErrMalformedCursor is returned when a pagination cursor is invalid.
var ErrMalformedCursor = errors.New("malformed cursor")

// firstLine returns the first line of text cut to 80 cells, handling UTF-8 safely.
// A cell is one display column; this approximates by counting runes, which over-counts wide characters.
// Design §3 specifies: "the first line of the first input cut to 80 cells".
func firstLine(text string, maxCells int) string {
	// Find first newline
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	// Cut to maxCells runes, without splitting a multi-byte sequence
	runes := []rune(text)
	if len(runes) > maxCells {
		runes = runes[:maxCells]
	}
	return string(runes)
}

// scanConvRow scans a conversation row from the query result using a Scan function.
// Expects: id, created_at, workspace, last_submitted_at, turn_count, running, preview
func scanConvRow(scan func(...any) error) (*ConversationRow, error) {
	var c ConversationRow
	var createdAtStr string
	var lastSubmittedAtStr sql.NullString
	var running sql.NullInt64
	var preview sql.NullString

	if err := scan(&c.ID, &createdAtStr, &c.Workspace, &lastSubmittedAtStr, &c.TurnCount, &running, &preview); err != nil {
		return nil, err
	}

	t, err := parseTime(createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	c.CreatedAt = t

	if lastSubmittedAtStr.Valid {
		t, err := parseTime(lastSubmittedAtStr.String)
		if err != nil {
			return nil, fmt.Errorf("parse last_submitted_at: %w", err)
		}
		c.LastSubmittedAt = &t
	}

	if running.Valid && running.Int64 != 0 {
		c.Running = true
	}

	if preview.Valid {
		c.Preview = firstLine(preview.String, 80)
	}

	return &c, nil
}

// scanConversationRow scans a conversation row from a single-row query.
func scanConversationRow(row *sql.Row) (*ConversationRow, error) {
	return scanConvRow(row.Scan)
}

// scanConversationRows scans multiple conversation rows from query results.
func scanConversationRows(rows *sql.Rows) ([]ConversationRow, error) {
	var conversations []ConversationRow
	for rows.Next() {
		c, err := scanConvRow(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		conversations = append(conversations, *c)
	}
	return conversations, rows.Err()
}

// ListConversations returns a page of conversations ordered by latest activity first.
// workspace filters to a specific workspace (nil includes all workspaces; empty string matches NULL workspace).
// limit is clamped to 1-200. cursor is opaque; empty for the first page.
// When workspace is nil, includes all conversations (automatic selection filters NULL-workspace separately).
func (s *Store) ListConversations(workspace *string, cursor string, limit int) (conversations []ConversationRow, nextCursor string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		conversations, nextCursor, txErr = tx.ListConversations(workspace, cursor, limit)
		return txErr
	})
	return conversations, nextCursor, err
}

// ListConversations returns a page of conversations ordered by latest activity first using keyset pagination.
func (tx *Tx) ListConversations(workspace *string, cursor string, limit int) ([]ConversationRow, string, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}

	want := limit + 1 // one lookahead row tells us whether another page exists

	// Parse keyset cursor: "maxRowid,id" format
	var cursorRowid int64 = -1
	var cursorID string
	if cursor != "" {
		parts := strings.Split(cursor, ",")
		if len(parts) != 2 {
			return nil, "", fmt.Errorf("invalid cursor format: %w", ErrMalformedCursor)
		}
		r, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor rowid: %w", ErrMalformedCursor)
		}
		cursorRowid = r
		cursorID = parts[1]
		if cursorID == "" {
			return nil, "", fmt.Errorf("invalid cursor id: %w", ErrMalformedCursor)
		}
	}

	// Build the query with keyset pagination over (max_turn_rowid DESC, c.id ASC)
	// Get preview from FIRST turn (oldest)
	var rows *sql.Rows
	var err error

	whereClause := ""
	args := []interface{}{}

	if workspace != nil && *workspace == "" {
		whereClause = "WHERE c.workspace IS NULL"
	} else if workspace != nil {
		whereClause = "WHERE c.workspace = ?"
		args = append(args, *workspace)
	}
	// else workspace is nil: include all conversations

	// Use a subquery with MAX(t.rowid) to handle keyset cursor
	// Apply keyset cursor filter: we want rows where (max_rowid, id) < (cursor_rowid, cursor_id) in DESC order
	cursorWhere := ""
	if cursorRowid >= 0 {
		cursorWhere = "AND (COALESCE(cte.max_rowid, -1) < ? OR (COALESCE(cte.max_rowid, -1) = ? AND cte.id > ?))"
		args = append(args, cursorRowid, cursorRowid, cursorID)
	}

	baseQuery := `
		WITH conv_turns AS (
			SELECT c.id, c.created_at, c.workspace, COALESCE(MAX(t.rowid), -1) as max_rowid
			FROM conversations c
			LEFT JOIN turns t ON c.id = t.conversation_id
			` + whereClause + `
			GROUP BY c.id, c.created_at, c.workspace
		)
		SELECT
			cte.id, cte.created_at, cte.workspace,
			(SELECT MAX(a.dispatched_at) FROM turns t2
				LEFT JOIN attempts a ON t2.id = a.turn_id
				WHERE t2.conversation_id = cte.id) as last_submitted_at,
			COUNT(t.id) as turn_count,
			(EXISTS(SELECT 1 FROM turns t2 WHERE t2.conversation_id = cte.id AND t2.terminal_state IS NULL)) as running,
			(SELECT m.content FROM messages m
				WHERE m.turn_id = (
					SELECT t3.id FROM turns t3 WHERE t3.conversation_id = cte.id
					ORDER BY t3.rowid ASC LIMIT 1
				) AND m.phase = 'input' ORDER BY m.sequence LIMIT 1) as preview
		FROM conv_turns cte
		LEFT JOIN turns t ON cte.id = t.conversation_id
		WHERE 1=1 ` + cursorWhere + `
		GROUP BY cte.id, cte.created_at, cte.workspace, cte.max_rowid
		ORDER BY cte.max_rowid DESC, cte.id ASC
		LIMIT ?
	`
	args = append(args, want)

	rows, err = tx.tx.Query(baseQuery, args...)
	if err != nil {
		return nil, "", fmt.Errorf("query conversations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	conversations, err := scanConversationRows(rows)
	if err != nil {
		return nil, "", fmt.Errorf("scan conversations: %w", err)
	}

	var nextCursor string
	if len(conversations) > limit {
		conversations = conversations[:limit]
		// Extract the keyset from the last row for the next cursor
		lastRow := conversations[len(conversations)-1]
		// Get the max rowid for the last row (we need to query it back)
		var nextRowid int64
		err := tx.tx.QueryRow(`
			SELECT COALESCE(MAX(t.rowid), -1)
			FROM turns t
			WHERE t.conversation_id = ?
		`, lastRow.ID).Scan(&nextRowid)
		if err != nil {
			return nil, "", fmt.Errorf("get next rowid: %w", err)
		}
		nextCursor = fmt.Sprintf("%d,%s", nextRowid, lastRow.ID)
	}

	return conversations, nextCursor, nil
}

// GetConversation retrieves a conversation by ID or unique prefix.
// If idPrefix is a full ID, it returns that conversation exactly.
// If idPrefix is a prefix, it matches the first conversation whose ID starts with that prefix.
// Returns ErrAmbiguousConversationID if the prefix matches multiple conversations.
// Returns ErrNoSuchContent if the ID is not found.
func (s *Store) GetConversation(idPrefix string) (*ConversationRow, error) {
	var row *ConversationRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		row, txErr = tx.GetConversation(idPrefix)
		return txErr
	})
	return row, err
}

// GetConversation retrieves a conversation by ID or unique prefix, checking for ambiguity.
func (tx *Tx) GetConversation(idPrefix string) (*ConversationRow, error) {
	// Use substr for exact prefix matching (not LIKE which treats % and _ as wildcards).
	// substr(c.id,1,length(?)) = ? is safer for non-ASCII ids than range comparison.
	rows, err := tx.tx.Query(`
		SELECT
			c.id, c.created_at, c.workspace,
			(SELECT MAX(a.dispatched_at) FROM turns t2
				LEFT JOIN attempts a ON t2.id = a.turn_id
				WHERE t2.conversation_id = c.id) as last_submitted_at,
			COUNT(t.id) as turn_count,
			(EXISTS(SELECT 1 FROM turns t2 WHERE t2.conversation_id = c.id AND t2.terminal_state IS NULL)) as running,
			(SELECT m.content FROM messages m
				WHERE m.turn_id = (
					SELECT t3.id FROM turns t3 WHERE t3.conversation_id = c.id
					ORDER BY t3.rowid ASC LIMIT 1
				) AND m.phase = 'input' ORDER BY m.sequence LIMIT 1) as preview
		FROM conversations c
		LEFT JOIN turns t ON c.id = t.conversation_id
		WHERE c.id = ? OR substr(c.id, 1, length(?)) = ?
		GROUP BY c.id, c.created_at, c.workspace
		ORDER BY c.id
		LIMIT 2
	`, idPrefix, idPrefix, idPrefix)
	if err != nil {
		return nil, fmt.Errorf("query conversation: %w", err)
	}
	defer func() { _ = rows.Close() }()

	conversations, err := scanConversationRows(rows)
	if err != nil {
		return nil, fmt.Errorf("scan conversations: %w", err)
	}

	switch len(conversations) {
	case 0:
		return nil, fmt.Errorf("conversation not found: %w", ErrNoSuchContent)
	case 1:
		return &conversations[0], nil
	default:
		return nil, fmt.Errorf("ambiguous conversation ID %q: %w", idPrefix, ErrAmbiguousConversationID)
	}
}

// GetConversationTurns returns all turns in a conversation ordered by submission time (oldest first).
func (s *Store) GetConversationTurns(conversationID string) ([]TurnInConversation, error) {
	var turns []TurnInConversation
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		turns, txErr = tx.GetConversationTurns(conversationID)
		return txErr
	})
	return turns, err
}

// GetConversationTurns returns all turns in a conversation ordered by submission time (oldest first).
func (tx *Tx) GetConversationTurns(conversationID string) ([]TurnInConversation, error) {
	rows, err := tx.tx.Query(`
		SELECT t.id, t.terminal_state,
			(SELECT MAX(a.dispatched_at) FROM attempts a WHERE a.turn_id = t.id) as submitted_at
		FROM turns t
		WHERE t.conversation_id = ?
		ORDER BY t.rowid ASC
	`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("query conversation turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var turns []TurnInConversation
	for rows.Next() {
		var t TurnInConversation
		var terminalState sql.NullString
		var submittedAtStr sql.NullString

		if err := rows.Scan(&t.ID, &terminalState, &submittedAtStr); err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}

		if terminalState.Valid {
			t.TerminalState = &terminalState.String
		}

		if submittedAtStr.Valid {
			st, err := parseTime(submittedAtStr.String)
			if err != nil {
				return nil, fmt.Errorf("parse submitted_at: %w", err)
			}
			t.SubmittedAt = &st
		}

		turns = append(turns, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate turns: %w", err)
	}

	return turns, nil
}

// SetConversationWorkspace sets the workspace for a conversation that currently has NULL workspace.
// Returns ErrWorkspaceMismatch if the conversation already has a different workspace set.
func (s *Store) SetConversationWorkspace(conversationID, workspace string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.SetConversationWorkspace(conversationID, workspace)
	})
}

// SetConversationWorkspace sets the workspace for a conversation (transactional with adoption check).
func (tx *Tx) SetConversationWorkspace(conversationID, workspace string) error {
	var currentWorkspace sql.NullString
	if err := tx.tx.QueryRow(`
		SELECT workspace FROM conversations WHERE id = ?
	`, conversationID).Scan(&currentWorkspace); err != nil {
		return fmt.Errorf("query conversation workspace: %w", err)
	}

	// If the conversation already has a workspace, it's immutable
	if currentWorkspace.Valid {
		if currentWorkspace.String != workspace {
			return fmt.Errorf("%w: conversation already has workspace %q but got %q", ErrWorkspaceMismatch, currentWorkspace.String, workspace)
		}
		// Workspace already matches, nothing to do
		return nil
	}

	// Set the workspace
	if _, err := tx.tx.Exec(`
		UPDATE conversations SET workspace = ? WHERE id = ?
	`, workspace, conversationID); err != nil {
		return fmt.Errorf("update conversation workspace: %w", err)
	}

	return nil
}

// GetMostRecentConversationForWorkspace returns the most recently submitted conversation
// for a given workspace. Returns nil if no conversation exists for that workspace.
// Null-workspace conversations are excluded from this query.
func (s *Store) GetMostRecentConversationForWorkspace(workspace string) (*ConversationRow, error) {
	var row *ConversationRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		row, txErr = tx.GetMostRecentConversationForWorkspace(workspace)
		return txErr
	})
	return row, err
}

// GetMostRecentConversationForWorkspace returns the most recently submitted conversation for a workspace.
func (tx *Tx) GetMostRecentConversationForWorkspace(workspace string) (*ConversationRow, error) {
	row := tx.tx.QueryRow(`
		SELECT
			c.id, c.created_at, c.workspace,
			(SELECT MAX(a.dispatched_at) FROM turns t2
				LEFT JOIN attempts a ON t2.id = a.turn_id
				WHERE t2.conversation_id = c.id) as last_submitted_at,
			COUNT(t.id) as turn_count,
			(EXISTS(SELECT 1 FROM turns t2 WHERE t2.conversation_id = c.id AND t2.terminal_state IS NULL)) as running,
			(SELECT m.content FROM messages m
				WHERE m.turn_id = (
					SELECT t3.id FROM turns t3 WHERE t3.conversation_id = c.id
					ORDER BY t3.rowid ASC LIMIT 1
				) AND m.phase = 'input' ORDER BY m.sequence LIMIT 1) as preview
		FROM conversations c
		LEFT JOIN turns t ON c.id = t.conversation_id
		WHERE c.workspace = ?
		GROUP BY c.id, c.created_at, c.workspace
		ORDER BY COALESCE(MAX(t.rowid), -1) DESC, c.id ASC
		LIMIT 1
	`, workspace)

	c, err := scanConversationRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query most recent conversation: %w", err)
	}

	return c, nil
}
