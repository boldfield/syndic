package journal_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

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
	_, _, _, err := f.store.AppendMessage(turnID, "input", "user", nil, []byte(content), nil)
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
	// Create conversations with similar IDs (they'll have different generated IDs)
	conv1 := f.createConversation(t, &ws)
	conv2 := f.createConversation(t, &ws)

	// Find a common prefix if the IDs start the same
	minLen := len(conv1)
	if len(conv2) < minLen {
		minLen = len(conv2)
	}

	// Find shortest common prefix by comparing characters
	commonPrefixLen := 0
	for i := 0; i < minLen; i++ {
		if conv1[i] == conv2[i] {
			commonPrefixLen++
		} else {
			break
		}
	}

	if commonPrefixLen > 0 {
		// Use a prefix that matches both
		prefix := conv1[:commonPrefixLen]
		_, err := f.store.GetConversation(prefix)
		if err == nil {
			t.Fatal("GetConversation should return error for ambiguous prefix")
		}
		if !errors.Is(err, journal.ErrAmbiguousConversationID) {
			t.Errorf("expected ErrAmbiguousConversationID, got: %v", err)
		}
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
