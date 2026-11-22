package journal_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/boldfield/syndic/internal/journal"
)

// convFixture is a test database with conversations, turns, and messages.
type convFixture struct {
	db    *journal.DB
	store *journal.Store
}

func newConvFixture(t *testing.T) *convFixture {
	t.Helper()
	db, cleanup := openTestDB(t)
	t.Cleanup(cleanup)
	store := journal.NewStore(db)
	return &convFixture{db: db, store: store}
}

// createConversation creates a conversation with optional workspace.
func (f *convFixture) createConversation(t *testing.T, workspace *string) string {
	t.Helper()
	convID, err := f.store.CreateConversation(workspace)
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	return convID
}

// createTurn creates a turn in a conversation.
func (f *convFixture) createTurn(t *testing.T, run, convID string) string {
	t.Helper()
	var turnID string
	err := f.store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turnID, txErr = tx.CreateTurnWithConversation(run, "review", nil, convID)
		return txErr
	})
	if err != nil {
		t.Fatalf("CreateTurnWithConversation: %v", err)
	}
	return turnID
}

// addMessage adds an input message to a turn.
func (f *convFixture) addMessage(t *testing.T, turnID, content string) {
	t.Helper()
	_, _, _, err := f.store.AppendMessage(turnID, "author", "user", nil, []byte(content), nil)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
}

func TestConversationsTableExists(t *testing.T) {
	f := newConvFixture(t)

	var count int
	err := f.db.Conn().QueryRow(`
		SELECT COUNT(*) FROM conversations
	`).Scan(&count)
	if err != nil {
		t.Fatalf("query conversations table: %v", err)
	}
	// Fresh database should have no conversations yet
	if count != 0 {
		t.Errorf("fresh database has %d conversations, want 0", count)
	}
}

func TestSetConversationWorkspaceAdopts(t *testing.T) {
	f := newConvFixture(t)

	// Create a conversation with null workspace
	convID := f.createConversation(t, nil)

	// Set the workspace
	err := f.store.SetConversationWorkspace(convID, "/home/user/project")
	if err != nil {
		t.Fatalf("SetConversationWorkspace: %v", err)
	}

	// Verify the workspace was set
	var workspace sql.NullString
	err = f.db.Conn().QueryRow(`
		SELECT workspace FROM conversations WHERE id = ?
	`, convID).Scan(&workspace)
	if err != nil {
		t.Fatalf("query conversation: %v", err)
	}
	if !workspace.Valid || workspace.String != "/home/user/project" {
		t.Errorf("workspace = %v, want /home/user/project", workspace)
	}
}

func TestSetConversationWorkspaceRejectsMismatch(t *testing.T) {
	f := newConvFixture(t)

	// Create a conversation with a workspace
	workspace1 := "/home/user/project1"
	convID := f.createConversation(t, &workspace1)

	// Try to set a different workspace (should reject)
	err := f.store.SetConversationWorkspace(convID, "/home/user/project2")
	if err == nil {
		t.Error("SetConversationWorkspace accepted mismatched workspace")
	}
	// Check that it's a workspace_mismatch error
	if !errors.Is(err, journal.ErrWorkspaceMismatch) {
		t.Errorf("expected workspace_mismatch error, got: %v", err)
	}
}

func TestListConversationsEmpty(t *testing.T) {
	f := newConvFixture(t)

	conversations, cursor, err := f.store.ListConversations(nil, "", 10)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(conversations) != 0 {
		t.Errorf("expected 0 conversations, got %d", len(conversations))
	}
	if cursor != "" {
		t.Errorf("expected empty cursor, got %q", cursor)
	}
}

func TestListConversationsIncludesAllWithNilWorkspace(t *testing.T) {
	f := newConvFixture(t)

	// Create conversations with and without workspace
	workspace := "/home/user/project"
	convWithWS := f.createConversation(t, &workspace)
	convNoWS := f.createConversation(t, nil)

	// List without workspace filter (nil includes ALL conversations)
	conversations, _, err := f.store.ListConversations(nil, "", 10)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}

	// Should get both conversations
	if len(conversations) != 2 {
		t.Errorf("expected 2 conversations, got %d", len(conversations))
	}

	// Check both are in the list
	found := make(map[string]bool)
	for _, c := range conversations {
		found[c.ID] = true
	}
	if !found[convWithWS] || !found[convNoWS] {
		t.Errorf("expected both conversations to be found")
	}
}

func TestListConversationsFilterByWorkspace(t *testing.T) {
	f := newConvFixture(t)

	// Create conversations with different workspaces
	ws1 := "/home/user/project1"
	ws2 := "/home/user/project2"
	conv1 := f.createConversation(t, &ws1)
	conv2 := f.createConversation(t, &ws1)
	conv3 := f.createConversation(t, &ws2)

	// List conversations for workspace 1
	conversations, _, err := f.store.ListConversations(&ws1, "", 10)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}

	if len(conversations) != 2 {
		t.Errorf("expected 2 conversations for %s, got %d", ws1, len(conversations))
	}

	// Check that conv1 and conv2 are in the list (order doesn't matter for now)
	found := make(map[string]bool)
	for _, c := range conversations {
		found[c.ID] = true
	}
	if !found[conv1] || !found[conv2] {
		t.Errorf("expected conversations %s and %s not found", conv1, conv2)
	}
	if found[conv3] {
		t.Errorf("unexpected conversation %s found", conv3)
	}
}

func TestListConversationsNullWorkspaceFilter(t *testing.T) {
	f := newConvFixture(t)

	// Create conversations with and without workspace
	ws := "/home/user/project"
	convWithWS := f.createConversation(t, &ws)
	convNoWS := f.createConversation(t, nil)

	// List only null-workspace conversations
	emptyStr := ""
	conversations, _, err := f.store.ListConversations(&emptyStr, "", 10)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}

	if len(conversations) != 1 {
		t.Errorf("expected 1 conversation with null workspace, got %d", len(conversations))
	}
	if conversations[0].ID != convNoWS {
		t.Errorf("expected conversation %s, got %s", convNoWS, conversations[0].ID)
	}
	if conversations[0].Workspace != nil {
		t.Errorf("expected null workspace, got %v", conversations[0].Workspace)
	}

	// Ensure the workspace one is not in the list
	for _, c := range conversations {
		if c.ID == convWithWS {
			t.Errorf("workspace conversation %s should not be in null-workspace list", convWithWS)
		}
	}
}

func TestListConversationsOrderByLatestActivity(t *testing.T) {
	f := newConvFixture(t)

	// Create a run first
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	ws := "/home/user/project"

	// Create conversations in a specific order
	conv1 := f.createConversation(t, &ws)
	conv2 := f.createConversation(t, &ws)

	// Add turns to conv1
	turn1 := f.createTurn(t, runID, conv1)
	f.addMessage(t, turn1, "first")

	// Add turns to conv2
	turn2 := f.createTurn(t, runID, conv2)
	f.addMessage(t, turn2, "second")

	// List conversations - conv2 should be first (most recent)
	conversations, _, err := f.store.ListConversations(&ws, "", 10)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}

	if len(conversations) != 2 {
		t.Errorf("expected 2 conversations, got %d", len(conversations))
		return
	}

	// Since both turns were created in sequence, conv2 should have the most recent activity
	if conversations[0].ID != conv2 {
		t.Errorf("expected first conversation to be %s (most recent), got %s", conv2, conversations[0].ID)
	}
}

func TestGetConversationByID(t *testing.T) {
	f := newConvFixture(t)

	ws := "/home/user/project"
	convID := f.createConversation(t, &ws)

	// Get by exact ID
	conv, err := f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	if conv.ID != convID {
		t.Errorf("expected conversation %s, got %s", convID, conv.ID)
	}
	if conv.Workspace == nil || *conv.Workspace != ws {
		t.Errorf("expected workspace %s, got %v", ws, conv.Workspace)
	}
}

func TestGetConversationByPrefix(t *testing.T) {
	f := newConvFixture(t)

	ws := "/home/user/project"
	convID := f.createConversation(t, &ws)
	prefix := convID[:8] // Get first 8 characters as prefix

	// Get by prefix
	conv, err := f.store.GetConversation(prefix)
	if err != nil {
		t.Fatalf("GetConversation with prefix: %v", err)
	}

	if conv.ID != convID {
		t.Errorf("expected conversation %s, got %s", convID, conv.ID)
	}
}

func TestGetConversationNotFound(t *testing.T) {
	f := newConvFixture(t)

	_, err := f.store.GetConversation("nonexistent")
	if err == nil {
		t.Fatal("GetConversation should return error for non-existent conversation")
	}
	if !errors.Is(err, journal.ErrNoSuchContent) {
		t.Errorf("expected ErrNoSuchContent, got: %v", err)
	}
}

func TestGetConversationAmbiguousPrefix(t *testing.T) {
	f := newConvFixture(t)

	ws := "/home/user/project"
	// Create conversations with deterministic IDs for testing ambiguous prefix
	conv1 := "aaaa-test-1111-bbbb"
	conv2 := "aaaa-test-2222-cccc"

	// Directly insert them into the database
	_, err := f.db.Conn().Exec(`INSERT INTO conversations (id, workspace) VALUES (?, ?)`, conv1, ws)
	if err != nil {
		t.Fatalf("insert conv1: %v", err)
	}

	_, err = f.db.Conn().Exec(`INSERT INTO conversations (id, workspace) VALUES (?, ?)`, conv2, ws)
	if err != nil {
		t.Fatalf("insert conv2: %v", err)
	}

	// Use a prefix that matches both: "aaaa-test"
	prefix := "aaaa-test"
	_, err = f.store.GetConversation(prefix)
	if err == nil {
		t.Fatal("GetConversation should return error for ambiguous prefix")
	}
	if !errors.Is(err, journal.ErrAmbiguousConversationID) {
		t.Errorf("expected ErrAmbiguousConversationID, got: %v", err)
	}
}

func TestGetConversationTurnsInOrder(t *testing.T) {
	f := newConvFixture(t)

	// Create a run and conversation
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	convID := f.createConversation(t, nil)

	// Create multiple turns
	turn1 := f.createTurn(t, runID, convID)
	turn2 := f.createTurn(t, runID, convID)

	// Get turns from conversation
	turns, err := f.store.GetConversationTurns(convID)
	if err != nil {
		t.Fatalf("GetConversationTurns: %v", err)
	}

	if len(turns) != 2 {
		t.Errorf("expected 2 turns, got %d", len(turns))
	}

	// Turns should be ordered by submission time (oldest first, per design spec §3)
	if turns[0].ID != turn1 {
		t.Errorf("expected first turn to be %s (oldest), got %s", turn1, turns[0].ID)
	}
	if turns[1].ID != turn2 {
		t.Errorf("expected second turn to be %s (most recent), got %s", turn2, turns[1].ID)
	}
}

func TestGetMostRecentConversationForWorkspace(t *testing.T) {
	f := newConvFixture(t)

	// Create a run first
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	ws := "/home/user/project"

	// Create multiple conversations
	_ = f.createConversation(t, &ws) // earlier conversation
	conv2 := f.createConversation(t, &ws)

	// Add a turn to conv2 (making it more recent)
	turn2 := f.createTurn(t, runID, conv2)
	f.addMessage(t, turn2, "content")

	// Get the most recent conversation
	conv, err := f.store.GetMostRecentConversationForWorkspace(ws)
	if err != nil {
		t.Fatalf("GetMostRecentConversationForWorkspace: %v", err)
	}

	if conv == nil {
		t.Fatal("expected conversation, got nil")
	}

	if conv.ID != conv2 {
		t.Errorf("expected conversation %s (most recent), got %s", conv2, conv.ID)
	}
}

func TestGetMostRecentConversationForWorkspaceEmpty(t *testing.T) {
	f := newConvFixture(t)

	ws := "/home/user/project"

	// Get most recent conversation (none exists)
	conv, err := f.store.GetMostRecentConversationForWorkspace(ws)
	if err != nil {
		t.Fatalf("GetMostRecentConversationForWorkspace: %v", err)
	}

	if conv != nil {
		t.Errorf("expected nil, got conversation %v", conv)
	}
}

func TestConversationPreviewFromFirstTurn(t *testing.T) {
	f := newConvFixture(t)

	// Create a run and conversation
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	convID := f.createConversation(t, nil)

	// Create first turn with a message
	turnID1 := f.createTurn(t, runID, convID)
	content1 := "First turn message"
	f.addMessage(t, turnID1, content1)

	// Create second turn
	turnID2 := f.createTurn(t, runID, convID)
	content2 := "Second turn message"
	f.addMessage(t, turnID2, content2)

	// Get the conversation
	conv, err := f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	// Preview should be from FIRST turn, not last
	if conv.Preview != content1 {
		t.Errorf("preview = %q, want %q (from first turn)", conv.Preview, content1)
	}
}

func TestConversationPreviewFirstLineOnly(t *testing.T) {
	f := newConvFixture(t)

	// Create a run and conversation
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	convID := f.createConversation(t, nil)

	// Create a turn with a multi-line message
	turnID := f.createTurn(t, runID, convID)
	content := "First line\nSecond line\nThird line"
	f.addMessage(t, turnID, content)

	// Get the conversation
	conv, err := f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	// Preview should only be the first line
	if conv.Preview != "First line" {
		t.Errorf("preview = %q, want %q (first line only)", conv.Preview, "First line")
	}
}

func TestConversationTurnCountDeterministic(t *testing.T) {
	f := newConvFixture(t)

	// Create a run and conversation
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	convID := f.createConversation(t, nil)

	// Create multiple turns
	for i := 0; i < 3; i++ {
		f.createTurn(t, runID, convID)
	}

	// Get the conversation
	conv, err := f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	if conv.TurnCount != 3 {
		t.Errorf("turn count = %d, want 3", conv.TurnCount)
	}
}

func TestConversationRunningDetection(t *testing.T) {
	f := newConvFixture(t)

	// Create a run and conversation
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	convID := f.createConversation(t, nil)

	// Get conversation with no turns
	conv, err := f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	if conv.Running {
		t.Errorf("empty conversation should not be running")
	}

	// Create a turn but don't terminate it
	turnID := f.createTurn(t, runID, convID)

	// Get conversation again
	conv, err = f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	if !conv.Running {
		t.Errorf("conversation with open turn should be running")
	}

	// Terminate the turn
	err = f.store.SetTurnTerminal(turnID, "solo_complete")
	if err != nil {
		t.Fatalf("SetTurnTerminal: %v", err)
	}

	// Get conversation again
	conv, err = f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	if conv.Running {
		t.Errorf("conversation with terminated turn should not be running")
	}
}

func TestListConversationsPaginationDeterministic(t *testing.T) {
	f := newConvFixture(t)

	// Create a run first to add turns to conversations
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	ws := "/home/user/project"

	// Create 5 conversations with turns to establish ordering
	for i := 0; i < 5; i++ {
		conv := f.createConversation(t, &ws)
		// Add a turn to each to establish rowid ordering
		turn := f.createTurn(t, runID, conv)
		f.addMessage(t, turn, "message "+string(rune('a'+i)))
	}

	// List with limit 2 (should get most recent 2)
	conversations1, cursor1, err := f.store.ListConversations(&ws, "", 2)
	if err != nil {
		t.Fatalf("ListConversations first page: %v", err)
	}

	if len(conversations1) != 2 {
		t.Errorf("expected 2 conversations on first page, got %d", len(conversations1))
	}
	if cursor1 == "" {
		t.Error("expected non-empty cursor for second page")
	}

	// Get second page
	conversations2, cursor2, err := f.store.ListConversations(&ws, cursor1, 2)
	if err != nil {
		t.Fatalf("ListConversations second page: %v", err)
	}

	if len(conversations2) != 2 {
		t.Errorf("expected 2 conversations on second page, got %d", len(conversations2))
	}
	if cursor2 == "" {
		t.Error("expected non-empty cursor for third page")
	}

	// Get third page
	conversations3, cursor3, err := f.store.ListConversations(&ws, cursor2, 2)
	if err != nil {
		t.Fatalf("ListConversations third page: %v", err)
	}

	if len(conversations3) != 1 {
		t.Errorf("expected 1 conversation on third page, got %d", len(conversations3))
	}
	if cursor3 != "" {
		t.Error("expected empty cursor at end of pagination")
	}

	// Check that all conversations are unique
	seen := make(map[string]bool)
	for _, c := range append(append(conversations1, conversations2...), conversations3...) {
		if seen[c.ID] {
			t.Errorf("duplicate conversation %s", c.ID)
		}
		seen[c.ID] = true
	}
	if len(seen) != 5 {
		t.Errorf("expected 5 unique conversations, got %d", len(seen))
	}
}

func TestConversationCreatedAtTimestampPresent(t *testing.T) {
	f := newConvFixture(t)

	before := time.Now().Add(-time.Second)
	convID, err := f.store.CreateConversation(nil)
	after := time.Now().Add(time.Second)

	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	conv, err := f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	// Allow 2 second tolerance for time precision differences
	if conv.CreatedAt.Before(before) || conv.CreatedAt.After(after) {
		t.Errorf("created_at = %v, expected between %v and %v", conv.CreatedAt, before, after)
	}
}

func TestConversationUniqueID(t *testing.T) {
	f := newConvFixture(t)

	// Create two conversations
	conv1, err := f.store.CreateConversation(nil)
	if err != nil {
		t.Fatalf("CreateConversation 1: %v", err)
	}

	conv2, err := f.store.CreateConversation(nil)
	if err != nil {
		t.Fatalf("CreateConversation 2: %v", err)
	}

	// IDs should be different
	if conv1 == conv2 {
		t.Errorf("created conversations have same ID: %s", conv1)
	}
}

func TestListConversationsMalformedCursor(t *testing.T) {
	f := newConvFixture(t)

	// Test various malformed cursors
	testCases := []string{
		"not_a_valid_cursor",
		"123",
		"abc,",
		",abc",
		"not_a_number,abc",
	}

	for _, cursor := range testCases {
		_, _, err := f.store.ListConversations(nil, cursor, 10)
		if err == nil {
			t.Errorf("ListConversations should reject malformed cursor: %q", cursor)
		}
		if !errors.Is(err, journal.ErrMalformedCursor) {
			t.Errorf("expected ErrMalformedCursor for cursor %q, got: %v", cursor, err)
		}
	}
}

func TestListConversationsPaginationKeepsPreviousRows(t *testing.T) {
	// This test verifies that pagination ordering is deterministic based on max_rowid and id
	f := newConvFixture(t)

	// Create a run first
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	ws := "/home/user/project"

	// Create 5 conversations with turns to establish stable ordering
	for i := 0; i < 5; i++ {
		conv := f.createConversation(t, &ws)
		turn := f.createTurn(t, runID, conv)
		f.addMessage(t, turn, "message "+string(rune('a'+i)))
	}

	// Get all pages in sequence without modifications
	var allConvs []journal.ConversationRow
	var cursor string
	for page := 0; page < 3; page++ {
		pageConvs, nextCursor, err := f.store.ListConversations(&ws, cursor, 2)
		if err != nil {
			t.Fatalf("ListConversations page %d: %v", page, err)
		}
		allConvs = append(allConvs, pageConvs...)
		cursor = nextCursor
	}

	// Check that we got all 5 conversations
	if len(allConvs) != 5 {
		t.Errorf("expected 5 total conversations, got %d", len(allConvs))
	}

	// Check for duplicates
	seen := make(map[string]bool)
	for _, c := range allConvs {
		if seen[c.ID] {
			t.Errorf("duplicate conversation %s in pagination", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestConversationLastSubmittedAtNilWhenNoTurns(t *testing.T) {
	f := newConvFixture(t)

	convID := f.createConversation(t, nil)

	// Get the conversation - should have nil LastSubmittedAt
	conv, err := f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	if conv.LastSubmittedAt != nil {
		t.Errorf("expected nil LastSubmittedAt for empty conversation, got %v", conv.LastSubmittedAt)
	}
}

func TestConversationPreviewMultibyteTruncation(t *testing.T) {
	f := newConvFixture(t)

	// Create a run and conversation
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	convID := f.createConversation(t, nil)

	// Create a turn with multi-byte content and newline
	turnID := f.createTurn(t, runID, convID)
	// Create a string longer than 80 bytes but with multi-byte characters
	// "你好世界" is 12 bytes (3 bytes per char), so 70 of them is 210 bytes
	// We want to truncate at 80 chars (runes), not bytes
	content := strings.Repeat("🎉", 90) + "\nSecond line"
	f.addMessage(t, turnID, content)

	// Get the conversation
	conv, err := f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	// Preview should be truncated to first line, then to 80 runes
	expectedLength := 80
	actualLength := len([]rune(conv.Preview))
	if actualLength > expectedLength {
		t.Errorf("preview length %d exceeds 80 runes, got %d", expectedLength, actualLength)
	}

	// Should not contain the second line
	if strings.Contains(conv.Preview, "Second line") {
		t.Errorf("preview should not contain second line")
	}

	// Should be valid UTF-8
	if !utf8.ValidString(conv.Preview) {
		t.Errorf("preview is not valid UTF-8")
	}
}

func TestListConversationsPaginationWithNoTurnConversations(t *testing.T) {
	f := newConvFixture(t)

	// Create a run for adding turns to some conversations
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	ws := "/home/user/project"

	// Create 3 conversations with turns and 2 without
	// Conversations without turns will have max_rowid = -1 and must still paginate correctly
	convWithTurns := make([]string, 3)
	for i := 0; i < 3; i++ {
		conv := f.createConversation(t, &ws)
		convWithTurns[i] = conv
		turn := f.createTurn(t, runID, conv)
		f.addMessage(t, turn, "message "+string(rune('a'+i)))
	}

	// Create 2 conversations without turns (no-turn conversations)
	for i := 0; i < 2; i++ {
		f.createConversation(t, &ws)
	}

	// Paginate through all conversations with limit=2
	var allConvs []journal.ConversationRow
	var cursor string
	for page := 0; page < 3; page++ {
		pageConvs, nextCursor, err := f.store.ListConversations(&ws, cursor, 2)
		if err != nil {
			t.Fatalf("ListConversations page %d: %v", page, err)
		}
		allConvs = append(allConvs, pageConvs...)
		if len(pageConvs) == 0 {
			break
		}
		cursor = nextCursor
	}

	// Check that we got all 5 conversations
	if len(allConvs) != 5 {
		t.Errorf("expected 5 total conversations, got %d", len(allConvs))
	}

	// Check for duplicates and verify no-turn conversations are present
	seen := make(map[string]bool)
	noTurnCount := 0
	for _, c := range allConvs {
		if seen[c.ID] {
			t.Errorf("duplicate conversation %s in pagination", c.ID)
		}
		seen[c.ID] = true
		if c.TurnCount == 0 {
			noTurnCount++
		}
	}

	if noTurnCount != 2 {
		t.Errorf("expected 2 no-turn conversations, got %d", noTurnCount)
	}
}

func TestListConversationsPaginationStabilityWithMutation(t *testing.T) {
	f := newConvFixture(t)
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	ws := "/home/user/project"

	// 4 conversations with turns, plus 3 with none (all tie on key -1, separated by id).
	var all []string
	for i := 0; i < 4; i++ {
		conv := f.createConversation(t, &ws)
		all = append(all, conv)
		f.addMessage(t, f.createTurn(t, runID, conv), "m")
	}
	for i := 0; i < 3; i++ {
		all = append(all, f.createConversation(t, &ws))
	}

	page1, cursor, err := f.store.ListConversations(&ws, "", 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	seen := map[string]int{}
	for _, c := range page1 {
		seen[c.ID]++
	}

	// Mutate unseen rows: the oldest turn-bearing conversation and a no-turn one,
	// which would jump above the cursor if the key were recomputed live.
	var oldest string
	for _, id := range all[:4] {
		if seen[id] == 0 {
			oldest = id // all[0] is the oldest unseen
			break
		}
	}
	if oldest == "" {
		t.Fatal("no unseen turn-bearing conversation")
	}
	f.addMessage(t, f.createTurn(t, runID, oldest), "late")
	f.addMessage(t, f.createTurn(t, runID, all[6]), "late")

	for cursor != "" {
		var page []journal.ConversationRow
		page, cursor, err = f.store.ListConversations(&ws, cursor, 2)
		if err != nil {
			t.Fatalf("next page: %v", err)
		}
		for _, c := range page {
			seen[c.ID]++
		}
	}
	for _, id := range all {
		if seen[id] != 1 {
			t.Errorf("conversation %s seen %d times, want exactly once", id, seen[id])
		}
	}
}

func TestConversationSubmittedAtReal(t *testing.T) {
	f := newConvFixture(t)
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	conv := f.createConversation(t, nil)

	before := time.Now().Add(-2 * time.Second)
	t1 := f.createTurn(t, runID, conv)
	time.Sleep(5 * time.Millisecond)
	t2 := f.createTurn(t, runID, conv)
	after := time.Now().Add(2 * time.Second)

	turns, err := f.store.GetConversationTurns(conv)
	if err != nil {
		t.Fatalf("GetConversationTurns: %v", err)
	}
	if len(turns) != 2 || turns[0].ID != t1 || turns[1].ID != t2 {
		t.Fatalf("unexpected turn order: %+v", turns)
	}
	for _, tr := range turns {
		if tr.SubmittedAt == nil || tr.SubmittedAt.Before(before) || tr.SubmittedAt.After(after) {
			t.Errorf("turn %s submitted_at = %v, want within [%v, %v]", tr.ID, tr.SubmittedAt, before, after)
		}
	}
	if !turns[1].SubmittedAt.After(*turns[0].SubmittedAt) {
		t.Errorf("submitted_at not increasing: %v then %v", turns[0].SubmittedAt, turns[1].SubmittedAt)
	}

	row, err := f.store.GetConversation(conv)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if row.LastSubmittedAt == nil || !row.LastSubmittedAt.Equal(*turns[1].SubmittedAt) {
		t.Errorf("last_submitted_at = %v, want %v", row.LastSubmittedAt, turns[1].SubmittedAt)
	}

	// The list query reports the same value.
	listed, _, err := f.store.ListConversations(nil, "", 10)
	if err != nil || len(listed) != 1 || listed[0].LastSubmittedAt == nil || !listed[0].LastSubmittedAt.Equal(*turns[1].SubmittedAt) {
		t.Errorf("listed last_submitted_at mismatch: %+v err %v", listed, err)
	}
}

func TestGetConversationExactIDWinsOverLongerIDs(t *testing.T) {
	f := newConvFixture(t)
	for _, id := range []string{"abc", "abcd"} {
		if _, err := f.db.Conn().Exec(`INSERT INTO conversations (id) VALUES (?)`, id); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	got, err := f.store.GetConversation("abc")
	if err != nil || got.ID != "abc" {
		t.Fatalf("exact ID: got %+v err %v", got, err)
	}
	if _, err := f.store.GetConversation("ab"); !errors.Is(err, journal.ErrAmbiguousConversationID) {
		t.Errorf("prefix ab: want ambiguous, got %v", err)
	}
}

func TestConversationPreviewDisplayWidth(t *testing.T) {
	f := newConvFixture(t)

	// Create a run and conversation
	runID, _, err := f.store.CreateRun([]byte("{}"))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	convID := f.createConversation(t, nil)

	// Create a turn with wide characters (CJK, emoji)
	// CJK characters are 2 cells wide, emoji can be 1-2+ cells
	turnID := f.createTurn(t, runID, convID)
	// 40 emoji characters (assume 2 cells each) = 80 cells
	content := strings.Repeat("🎉", 40) + "\nSecond line"
	f.addMessage(t, turnID, content)

	// Get the conversation
	conv, err := f.store.GetConversation(convID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}

	// Use runewidth to check the display width
	width := 0
	for _, r := range conv.Preview {
		w := runewidth.RuneWidth(r)
		if w > 0 {
			width += w
		}
	}

	if width > 80 {
		t.Errorf("preview display width %d exceeds 80 cells", width)
	}

	// Should not contain the second line
	if strings.Contains(conv.Preview, "Second line") {
		t.Errorf("preview should not contain second line")
	}

	// Should be valid UTF-8
	if !utf8.ValidString(conv.Preview) {
		t.Errorf("preview is not valid UTF-8")
	}
}
