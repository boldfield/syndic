package journal

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
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
// A cell is one display column; uses runewidth to handle wide characters correctly.
// Design §3 specifies: "the first line of the first input cut to 80 cells".
func firstLine(text string, maxCells int) string {
	// Find first newline
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	// Cut to maxCells display width, handling wide characters (CJK, emoji, etc.)
	return runewidth.Truncate(text, maxCells, "")
}

// convColumns is the single column list behind every conversation query; the
// FROM clause must alias conversations as c. last_submitted_at comes from the
// durable turns.submitted_at of the latest turn (the same turn the ordering key
// picks); preview is the first line of the first turn's input: its first user message.
const convColumns = `
	c.id, c.created_at, c.workspace,
	(SELECT t2.submitted_at FROM turns t2 WHERE t2.conversation_id = c.id ORDER BY t2.rowid DESC LIMIT 1),
	(SELECT COUNT(*) FROM turns t2 WHERE t2.conversation_id = c.id),
	EXISTS(SELECT 1 FROM turns t2 WHERE t2.conversation_id = c.id AND t2.terminal_state IS NULL),
	(SELECT m.content FROM messages m
		WHERE m.turn_id = (
			SELECT t3.id FROM turns t3 WHERE t3.conversation_id = c.id
			ORDER BY t3.rowid ASC LIMIT 1
		) AND m.phase = 'author' AND m.sender = 'user' ORDER BY m.sequence LIMIT 1)`

// scanConvRow scans a conversation row from the query result using a Scan function.
// Expects: id, created_at, workspace, last_submitted_at, turn_count, running, preview
// extra receives any additional trailing columns.
func scanConvRow(scan func(...any) error, extra ...any) (*ConversationRow, error) {
	var c ConversationRow
	var createdAtStr string
	var lastSubmittedAtStr sql.NullString
	var running sql.NullInt64
	var preview sql.NullString

	dest := append([]any{&c.ID, &createdAtStr, &c.Workspace, &lastSubmittedAtStr, &c.TurnCount, &running, &preview}, extra...)
	if err := scan(dest...); err != nil {
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
//
// Pagination guarantee: the cursor pins a snapshot (the highest turn rowid at the
// first page). Later pages order by activity as of that snapshot, so turns submitted
// between pages never move an unseen conversation across the cursor: every
// conversation present at the first page appears exactly once across all pages.
// Cursor format: "snapshot,key,id".
func (tx *Tx) ListConversations(workspace *string, cursor string, limit int) ([]ConversationRow, string, error) {
	page, err := tx.ListConversationsPage(workspace, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	return page.Rows, page.NextCursor, nil
}

// ConversationPage is one page of ListConversationsPage. ItemCursors[i] is
// the cursor that resumes after Rows[i] in the same pinned snapshot, so a
// caller that keeps only a prefix of Rows can still continue exactly after it.
type ConversationPage struct {
	Rows        []ConversationRow
	ItemCursors []string
	NextCursor  string
}

// ListConversationsPage is ListConversations plus a resume cursor per row,
// all taken inside the transaction that picked the snapshot.
func (s *Store) ListConversationsPage(workspace *string, cursor string, limit int) (page ConversationPage, err error) {
	err = s.Tx(func(tx *Tx) error {
		var txErr error
		page, txErr = tx.ListConversationsPage(workspace, cursor, limit)
		return txErr
	})
	return page, err
}

// ListConversationsPage implements ListConversations; see there for the
// cursor and snapshot rules.
func (tx *Tx) ListConversationsPage(workspace *string, cursor string, limit int) (ConversationPage, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}

	var snap, cursorKey int64
	var cursorID string
	if cursor != "" {
		parts := strings.SplitN(cursor, ",", 3)
		if len(parts) != 3 || parts[2] == "" {
			return ConversationPage{}, fmt.Errorf("invalid cursor format: %w", ErrMalformedCursor)
		}
		var err error
		if snap, err = strconv.ParseInt(parts[0], 10, 64); err != nil || snap < 0 {
			return ConversationPage{}, fmt.Errorf("invalid cursor snapshot: %w", ErrMalformedCursor)
		}
		if cursorKey, err = strconv.ParseInt(parts[1], 10, 64); err != nil || cursorKey < -1 {
			return ConversationPage{}, fmt.Errorf("invalid cursor key: %w", ErrMalformedCursor)
		}
		cursorID = parts[2]
	} else if err := tx.tx.QueryRow(`SELECT COALESCE(MAX(rowid), 0) FROM turns`).Scan(&snap); err != nil {
		return ConversationPage{}, fmt.Errorf("query snapshot: %w", err)
	}

	// key: rowid of the conversation's latest turn within the snapshot; -1 when none.
	const key = `COALESCE((SELECT MAX(tk.rowid) FROM turns tk WHERE tk.conversation_id = c.id AND tk.rowid <= @snap), -1)`

	where := "1=1"
	args := []any{sql.Named("snap", snap), sql.Named("limit", limit+1)} // one lookahead row tells us whether another page exists
	if workspace != nil && *workspace == "" {
		where += " AND c.workspace IS NULL"
	} else if workspace != nil {
		where += " AND c.workspace = @workspace"
		args = append(args, sql.Named("workspace", *workspace))
	}
	// A nil workspace includes every conversation; only automatic selection excludes legacy NULL-workspace rows.
	if cursor != "" {
		where += " AND (" + key + " < @ckey OR (" + key + " = @ckey AND c.id > @cid))"
		args = append(args, sql.Named("ckey", cursorKey), sql.Named("cid", cursorID))
	}

	rows, err := tx.tx.Query(`SELECT `+convColumns+`, `+key+` FROM conversations c WHERE `+where+
		` ORDER BY `+key+` DESC, c.id ASC LIMIT @limit`, args...)
	if err != nil {
		return ConversationPage{}, fmt.Errorf("query conversations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var conversations []ConversationRow
	var keys []int64
	for rows.Next() {
		var k int64
		c, err := scanConvRow(rows.Scan, &k)
		if err != nil {
			return ConversationPage{}, fmt.Errorf("scan conversation: %w", err)
		}
		conversations = append(conversations, *c)
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return ConversationPage{}, fmt.Errorf("scan conversations: %w", err)
	}

	more := len(conversations) > limit
	if more {
		conversations = conversations[:limit]
	}
	page := ConversationPage{Rows: conversations, ItemCursors: make([]string, len(conversations))}
	for i, c := range conversations {
		page.ItemCursors[i] = fmt.Sprintf("%d,%d,%s", snap, keys[i], c.ID)
	}
	if more {
		page.NextCursor = page.ItemCursors[limit-1]
	}
	return page, nil
}

// GetConversation retrieves a conversation by ID or unique prefix.
// If idPrefix is a full ID, it returns that conversation exactly.
// If idPrefix is a prefix, it returns an error if multiple conversations match;
// returns ErrNoSuchContent if the ID is not found.
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
	rows, err := tx.tx.Query(`SELECT `+convColumns+`
		FROM conversations c
		WHERE substr(c.id, 1, length(?)) = ?
		ORDER BY c.id = ? DESC, c.id
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
		if conversations[0].ID == idPrefix {
			return &conversations[0], nil // an exact ID wins over longer IDs it prefixes
		}
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
			t.submitted_at
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
	row := tx.tx.QueryRow(`SELECT `+convColumns+`
		FROM conversations c
		WHERE c.workspace = ?
		ORDER BY COALESCE((SELECT MAX(tk.rowid) FROM turns tk WHERE tk.conversation_id = c.id), -1) DESC, c.id ASC
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

// AdoptConversation resolves the conversation a submit belongs to inside the
// submit's transaction. A nil id creates one with the given workspace (empty
// means none). An existing id must name a conversation exactly, else the
// error satisfies errors.Is(err, ErrNoSuchContent). A conversation with no
// workspace adopts a non-empty one; one that has a workspace keeps it, and a
// different non-empty workspace is ErrWorkspaceMismatch. An empty workspace
// claims nothing.
func (tx *Tx) AdoptConversation(id *string, workspace string) (string, error) {
	var ws *string
	if workspace != "" {
		ws = &workspace
	}
	if id == nil {
		return tx.CreateConversation(ws)
	}
	var current sql.NullString
	err := tx.tx.QueryRow(`SELECT workspace FROM conversations WHERE id = ?`, *id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("conversation %q: %w", *id, ErrNoSuchContent)
	}
	if err != nil {
		return "", fmt.Errorf("query conversation: %w", err)
	}
	if ws != nil {
		if err := tx.SetConversationWorkspace(*id, workspace); err != nil {
			return "", err
		}
	}
	return *id, nil
}

// HistoryTurn is one finished turn of a conversation as history is built
// from it: the user's input and the answer the user was shown.
type HistoryTurn struct {
	ID    string
	State string
	Mode  string
	Input string
	// Answer is the frozen candidate, or for a solo turn the author's reply.
	// It is empty for a turn that ended without one.
	Answer string
	// FileTainted is the turn's own file_tainted flag: its answer may carry
	// workspace bytes (v5 delta section 6, "Egress of file-derived content").
	FileTainted bool
}

// ConversationHistory returns the conversation's finished turns, oldest
// first. A turn that is not terminal is not history yet.
func (tx *Tx) ConversationHistory(conversationID string) ([]HistoryTurn, error) {
	rows, err := tx.tx.Query(`
		SELECT t.id, t.terminal_state, t.mode, t.file_tainted,
			COALESCE((SELECT m.content FROM messages m
				WHERE m.turn_id = t.id AND m.phase = 'author' AND m.sender = 'user'
				ORDER BY m.sequence LIMIT 1), ''),
			COALESCE((SELECT c.content FROM candidates c WHERE c.id = t.candidate_id),
				(SELECT a.text FROM attempts a
					WHERE a.turn_id = t.id AND a.phase = 'author' AND a.text IS NOT NULL
					ORDER BY a.rowid DESC LIMIT 1), '')
		FROM turns t
		WHERE t.conversation_id = ? AND t.terminal_state IS NOT NULL
		ORDER BY t.rowid ASC
	`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("query conversation history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var turns []HistoryTurn
	for rows.Next() {
		var h HistoryTurn
		if err := rows.Scan(&h.ID, &h.State, &h.Mode, &h.FileTainted, &h.Input, &h.Answer); err != nil {
			return nil, fmt.Errorf("scan history turn: %w", err)
		}
		turns = append(turns, h)
	}
	return turns, rows.Err()
}

// ConversationFileTainted reports whether any turn of the conversation is
// file-tainted. It reads every turn, not the history a submit includes, so a
// trim cannot clear it.
func (tx *Tx) ConversationFileTainted(conversationID string) (bool, error) {
	var tainted bool
	if err := tx.tx.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM turns WHERE conversation_id = ? AND file_tainted)
	`, conversationID).Scan(&tainted); err != nil {
		return false, fmt.Errorf("query conversation taint: %w", err)
	}
	return tainted, nil
}

// StoreHistoryDocument stores a frozen history document under its hash. The
// same hash stored again is a no-op.
func (tx *Tx) StoreHistoryDocument(hash string, document []byte) error {
	if _, err := tx.tx.Exec(`INSERT OR IGNORE INTO history_documents (hash, document) VALUES (?, ?)`, hash, document); err != nil {
		return fmt.Errorf("store history document: %w", err)
	}
	return nil
}

// HistoryDocument returns the document stored under hash.
func (s *Store) HistoryDocument(hash string) ([]byte, error) {
	var doc []byte
	err := s.db.conn.QueryRow(`SELECT document FROM history_documents WHERE hash = ?`, hash).Scan(&doc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("history document %s: %w", hash, ErrNoSuchContent)
	}
	return doc, err
}
