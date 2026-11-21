package journal

import (
	"database/sql"
	"errors"
	"fmt"
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

// ListConversations returns a page of conversations ordered by latest activity first.
// workspace filters to a specific workspace (nil includes all workspaces; empty string matches NULL workspace).
// limit is clamped to 1-200. cursor is opaque; empty for the first page.
// Excludes null-workspace conversations from the default selection unless explicitly filtered.
func (s *Store) ListConversations(workspace *string, cursor string, limit int) (conversations []ConversationRow, nextCursor string, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		conversations, nextCursor, txErr = tx.ListConversations(workspace, cursor, limit)
		return txErr
	})
	return conversations, nextCursor, err
}

// ListConversations returns a page of conversations ordered by latest activity first.
func (tx *Tx) ListConversations(workspace *string, cursor string, limit int) ([]ConversationRow, string, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}

	want := limit + 1 // one lookahead row tells us whether another page exists

	// Parse cursor position from opaque cursor string
	var offset int64
	if cursor != "" {
		fmt.Sscanf(cursor, "%d", &offset)
	}

	// Build query to get conversations with aggregated turn data.
	// Order by max(turn rowid) DESC for deterministic ordering by latest turn.
	// If workspace is not provided, exclude null-workspace conversations.
	var rows *sql.Rows
	var err error

	if workspace == nil {
		// Default: exclude null-workspace conversations
		rows, err = tx.tx.Query(`
			SELECT
				c.id, c.created_at, c.workspace,
				NULL as last_submitted_at,
				COUNT(t.id) as turn_count,
				(EXISTS(SELECT 1 FROM turns t2 WHERE t2.conversation_id = c.id AND t2.terminal_state IS NULL)) as running,
				COALESCE((SELECT SUBSTR(m.content, 1, 80) FROM messages m
					WHERE m.turn_id = (
						SELECT t3.id FROM turns t3 WHERE t3.conversation_id = c.id
						ORDER BY t3.rowid DESC LIMIT 1
					) AND m.phase = 'input' ORDER BY m.sequence LIMIT 1), '') as preview
			FROM conversations c
			LEFT JOIN turns t ON c.id = t.conversation_id
			WHERE c.workspace IS NOT NULL
			GROUP BY c.id, c.created_at, c.workspace
			ORDER BY (SELECT MAX(t2.rowid) FROM turns t2 WHERE t2.conversation_id = c.id) DESC
			LIMIT ? OFFSET ?
		`, want, offset)
	} else if *workspace == "" {
		// Explicit empty string: match NULL workspace
		rows, err = tx.tx.Query(`
			SELECT
				c.id, c.created_at, c.workspace,
				NULL as last_submitted_at,
				COUNT(t.id) as turn_count,
				(EXISTS(SELECT 1 FROM turns t2 WHERE t2.conversation_id = c.id AND t2.terminal_state IS NULL)) as running,
				COALESCE((SELECT SUBSTR(m.content, 1, 80) FROM messages m
					WHERE m.turn_id = (
						SELECT t3.id FROM turns t3 WHERE t3.conversation_id = c.id
						ORDER BY t3.rowid DESC LIMIT 1
					) AND m.phase = 'input' ORDER BY m.sequence LIMIT 1), '') as preview
			FROM conversations c
			LEFT JOIN turns t ON c.id = t.conversation_id
			WHERE c.workspace IS NULL
			GROUP BY c.id, c.created_at, c.workspace
			ORDER BY (SELECT MAX(t2.rowid) FROM turns t2 WHERE t2.conversation_id = c.id) DESC
			LIMIT ? OFFSET ?
		`, want, offset)
	} else {
		// Explicit workspace: match that workspace
		rows, err = tx.tx.Query(`
			SELECT
				c.id, c.created_at, c.workspace,
				NULL as last_submitted_at,
				COUNT(t.id) as turn_count,
				(EXISTS(SELECT 1 FROM turns t2 WHERE t2.conversation_id = c.id AND t2.terminal_state IS NULL)) as running,
				COALESCE((SELECT SUBSTR(m.content, 1, 80) FROM messages m
					WHERE m.turn_id = (
						SELECT t3.id FROM turns t3 WHERE t3.conversation_id = c.id
						ORDER BY t3.rowid DESC LIMIT 1
					) AND m.phase = 'input' ORDER BY m.sequence LIMIT 1), '') as preview
			FROM conversations c
			LEFT JOIN turns t ON c.id = t.conversation_id
			WHERE c.workspace = ?
			GROUP BY c.id, c.created_at, c.workspace
			ORDER BY (SELECT MAX(t2.rowid) FROM turns t2 WHERE t2.conversation_id = c.id) DESC
			LIMIT ? OFFSET ?
		`, *workspace, want, offset)
	}

	if err != nil {
		return nil, "", fmt.Errorf("query conversations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var conversations []ConversationRow
	for rows.Next() {
		var c ConversationRow
		var createdAtStr string
		var lastSubmittedAtStr sql.NullString
		var running sql.NullInt64
		var preview sql.NullString

		if err := rows.Scan(&c.ID, &createdAtStr, &c.Workspace, &lastSubmittedAtStr, &c.TurnCount, &running, &preview); err != nil {
			return nil, "", fmt.Errorf("scan conversation: %w", err)
		}

		t, err := parseTime(createdAtStr)
		if err != nil {
			return nil, "", fmt.Errorf("parse created_at: %w", err)
		}
		c.CreatedAt = t

		if lastSubmittedAtStr.Valid {
			t, err := parseTime(lastSubmittedAtStr.String)
			if err != nil {
				return nil, "", fmt.Errorf("parse last_submitted_at: %w", err)
			}
			c.LastSubmittedAt = &t
		}

		if running.Valid && running.Int64 != 0 {
			c.Running = true
		}

		if preview.Valid {
			c.Preview = preview.String
		}

		conversations = append(conversations, c)
	}

	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate conversations: %w", err)
	}

	nextCursor := ""
	if len(conversations) > limit {
		conversations = conversations[:limit]
		nextCursor = fmt.Sprintf("%d", offset+int64(limit))
	}

	return conversations, nextCursor, nil
}

// GetConversation retrieves a conversation by ID or unique prefix.
// If idPrefix is a full ID, it returns that conversation exactly.
// If idPrefix is a prefix, it matches the first conversation whose ID starts with that prefix.
// Returns ErrNoSuchContent if the ID is ambiguous or not found.
func (s *Store) GetConversation(idPrefix string) (*ConversationRow, error) {
	var row *ConversationRow
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		row, txErr = tx.GetConversation(idPrefix)
		return txErr
	})
	return row, err
}

// GetConversation retrieves a conversation by ID or unique prefix.
func (tx *Tx) GetConversation(idPrefix string) (*ConversationRow, error) {
	var c ConversationRow
	var createdAtStr string
	var lastSubmittedAtStr sql.NullString
	var running sql.NullInt64
	var preview sql.NullString

	// Try exact match first, then prefix match
	err := tx.tx.QueryRow(`
		SELECT
			c.id, c.created_at, c.workspace,
			NULL as last_submitted_at,
			COUNT(t.id) as turn_count,
			(EXISTS(SELECT 1 FROM turns t2 WHERE t2.conversation_id = c.id AND t2.terminal_state IS NULL)) as running,
			COALESCE((SELECT SUBSTR(m.content, 1, 80) FROM messages m
				WHERE m.turn_id = (
					SELECT t3.id FROM turns t3 WHERE t3.conversation_id = c.id
					ORDER BY t3.rowid DESC LIMIT 1
				) AND m.phase = 'input' ORDER BY m.sequence LIMIT 1), '') as preview
		FROM conversations c
		LEFT JOIN turns t ON c.id = t.conversation_id
		WHERE c.id = ? OR c.id LIKE ? || '%'
		GROUP BY c.id, c.created_at, c.workspace
		LIMIT 2
	`, idPrefix, idPrefix).Scan(&c.ID, &createdAtStr, &c.Workspace, &lastSubmittedAtStr, &c.TurnCount, &running, &preview)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("conversation not found: %w", ErrNoSuchContent)
		}
		return nil, fmt.Errorf("query conversation: %w", err)
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
		c.Preview = preview.String
	}

	return &c, nil
}

// GetConversationTurns returns all turns in a conversation ordered by submission time.
func (s *Store) GetConversationTurns(conversationID string) ([]TurnInConversation, error) {
	var turns []TurnInConversation
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		turns, txErr = tx.GetConversationTurns(conversationID)
		return txErr
	})
	return turns, err
}

// GetConversationTurns returns all turns in a conversation ordered by submission time.
func (tx *Tx) GetConversationTurns(conversationID string) ([]TurnInConversation, error) {
	rows, err := tx.tx.Query(`
		SELECT id, terminal_state, NULL
		FROM turns
		WHERE conversation_id = ?
		ORDER BY rowid DESC
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
// Returns an error if the conversation already has a workspace set (workspace mismatch).
func (s *Store) SetConversationWorkspace(conversationID, workspace string) error {
	return s.Tx(func(tx *Tx) error {
		return tx.SetConversationWorkspace(conversationID, workspace)
	})
}

// SetConversationWorkspace sets the workspace for a conversation.
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
			return fmt.Errorf("workspace_mismatch: conversation already has workspace %q", currentWorkspace.String)
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
// for a given workspace. Returns nil if no conversation with an active turn exists for that workspace.
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
	var c ConversationRow
	var createdAtStr string
	var lastSubmittedAtStr sql.NullString
	var running sql.NullInt64
	var preview sql.NullString

	err := tx.tx.QueryRow(`
		SELECT
			c.id, c.created_at, c.workspace,
			NULL as last_submitted_at,
			COUNT(t.id) as turn_count,
			(EXISTS(SELECT 1 FROM turns t2 WHERE t2.conversation_id = c.id AND t2.terminal_state IS NULL)) as running,
			COALESCE((SELECT SUBSTR(m.content, 1, 80) FROM messages m
				WHERE m.turn_id = (
					SELECT t3.id FROM turns t3 WHERE t3.conversation_id = c.id
					ORDER BY t3.rowid DESC LIMIT 1
				) AND m.phase = 'input' ORDER BY m.sequence LIMIT 1), '') as preview
		FROM conversations c
		LEFT JOIN turns t ON c.id = t.conversation_id
		WHERE c.workspace = ?
		GROUP BY c.id, c.created_at, c.workspace
		ORDER BY (SELECT MAX(t2.rowid) FROM turns t2 WHERE t2.conversation_id = c.id) DESC
		LIMIT 1
	`, workspace).Scan(&c.ID, &createdAtStr, &c.Workspace, &lastSubmittedAtStr, &c.TurnCount, &running, &preview)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query most recent conversation: %w", err)
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
		c.Preview = preview.String
	}

	return &c, nil
}
