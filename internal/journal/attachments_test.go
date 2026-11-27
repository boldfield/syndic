package journal_test

import (
	"database/sql"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// insertToolContent inserts content into tool_content table for testing.
func insertToolContent(t *testing.T, db *journal.DB, sha256 string, body []byte) {
	t.Helper()
	_, err := db.Conn().Exec(`
		INSERT INTO tool_content (sha256, byte_len, body)
		VALUES (?, ?, ?)
	`, sha256, len(body), body)
	if err != nil {
		t.Fatalf("failed to insert tool_content: %v", err)
	}
}

func TestStoreAttachments_Empty(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}

	hash, err := store.StoreAttachments(turnID, nil)
	if err != nil {
		t.Fatalf("StoreAttachments: %v", err)
	}
	if hash != "none" {
		t.Errorf("hash = %q, want %q", hash, "none")
	}
}

func TestStoreAttachments_Single(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	content1 := []byte("content1")
	hash1 := "1111111111111111111111111111111111111111111111111111111111111111"
	insertToolContent(t, db, hash1, content1)

	store := journal.NewStore(db)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}

	att := journal.Attachment{
		ID:      "att1",
		TurnID:  turnID,
		Path:    "file.txt",
		SHA256:  hash1,
		ByteLen: len(content1),
		BaseSHA: hash1,
	}

	hash, err := store.StoreAttachments(turnID, []journal.Attachment{att})
	if err != nil {
		t.Fatalf("StoreAttachments: %v", err)
	}

	if hash == "" || hash == "none" {
		t.Errorf("hash = %q, want non-empty hash", hash)
	}

	retrieved, err := store.GetAttachments(turnID)
	if err != nil {
		t.Fatalf("GetAttachments: %v", err)
	}
	if len(retrieved) != 1 {
		t.Errorf("len(retrieved) = %d, want 1", len(retrieved))
	}
	if retrieved[0].Path != "file.txt" {
		t.Errorf("path = %q, want %q", retrieved[0].Path, "file.txt")
	}
	if retrieved[0].BaseSHA != hash1 {
		t.Errorf("base_sha256 = %q, want %q", retrieved[0].BaseSHA, hash1)
	}
	if retrieved[0].ByteLen != len(content1) {
		t.Errorf("byte_len = %d, want %d", retrieved[0].ByteLen, len(content1))
	}
}

func TestStoreAttachments_Multiple_Sorted(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	content1 := []byte("content1")
	content2 := []byte("content2")
	content3 := []byte("content3")
	hash1 := "1111111111111111111111111111111111111111111111111111111111111111"
	hash2 := "2222222222222222222222222222222222222222222222222222222222222222"
	hash3 := "3333333333333333333333333333333333333333333333333333333333333333"
	insertToolContent(t, db, hash1, content1)
	insertToolContent(t, db, hash2, content2)
	insertToolContent(t, db, hash3, content3)

	store := journal.NewStore(db)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}

	attachments := []journal.Attachment{
		{ID: "att3", TurnID: turnID, Path: "z.txt", SHA256: hash3, ByteLen: len(content3), BaseSHA: hash3},
		{ID: "att1", TurnID: turnID, Path: "a.txt", SHA256: hash1, ByteLen: len(content1), BaseSHA: hash1},
		{ID: "att2", TurnID: turnID, Path: "m.txt", SHA256: hash2, ByteLen: len(content2), BaseSHA: hash2},
	}

	hash, err := store.StoreAttachments(turnID, attachments)
	if err != nil {
		t.Fatalf("StoreAttachments: %v", err)
	}
	if hash == "" || hash == "none" {
		t.Errorf("hash = %q, want non-empty hash", hash)
	}

	retrieved, err := store.GetAttachments(turnID)
	if err != nil {
		t.Fatalf("GetAttachments: %v", err)
	}
	if len(retrieved) != 3 {
		t.Errorf("len(retrieved) = %d, want 3", len(retrieved))
	}

	if retrieved[0].Path != "a.txt" || retrieved[1].Path != "m.txt" || retrieved[2].Path != "z.txt" {
		t.Errorf("paths not sorted: %v", []string{retrieved[0].Path, retrieved[1].Path, retrieved[2].Path})
	}
}

// TestStoreAttachments_SetsHash tests that StoreAttachments computes and sets
// the attachments_hash correctly on the turn.
func TestStoreAttachments_SetsHash(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	content := []byte("attachment_content")
	hash := "2222222222222222222222222222222222222222222222222222222222222222"
	insertToolContent(t, db, hash, content)

	store := journal.NewStore(db)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}

	att := journal.Attachment{
		ID:      "att1",
		TurnID:  turnID,
		Path:    "file.txt",
		SHA256:  hash,
		ByteLen: len(content),
		BaseSHA: hash,
	}

	hashResult, err := store.StoreAttachments(turnID, []journal.Attachment{att})
	if err != nil {
		t.Fatalf("StoreAttachments: %v", err)
	}

	// Verify the hash was computed and is not "none"
	if hashResult == "" || hashResult == "none" {
		t.Errorf("hash = %q, want non-empty hash", hashResult)
	}

	// Verify it was set on the turn
	var stored sql.NullString
	if err := db.Conn().QueryRow(`SELECT attachments_hash FROM turns WHERE id = ?`, turnID).Scan(&stored); err != nil {
		t.Fatalf("query attachments_hash: %v", err)
	}
	if !stored.Valid || stored.String != hashResult {
		t.Errorf("stored hash = %v, want %q", stored, hashResult)
	}
}

// TestStoreAttachments_SetsFileTainted tests that StoreAttachments sets the
// file_tainted flag when storing attachments.
func TestStoreAttachments_SetsFileTainted(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	content := []byte("content")
	hash := "3333333333333333333333333333333333333333333333333333333333333333"
	insertToolContent(t, db, hash, content)

	store := journal.NewStore(db)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}

	att := journal.Attachment{
		ID:      "att1",
		TurnID:  turnID,
		Path:    "file.txt",
		SHA256:  hash,
		ByteLen: len(content),
		BaseSHA: hash,
	}

	_, err = store.StoreAttachments(turnID, []journal.Attachment{att})
	if err != nil {
		t.Fatalf("StoreAttachments: %v", err)
	}

	// Verify file_tainted was set to 1
	var fileTainted bool
	if err := db.Conn().QueryRow(`SELECT file_tainted FROM turns WHERE id = ?`, turnID).Scan(&fileTainted); err != nil {
		t.Fatalf("query file_tainted: %v", err)
	}
	if !fileTainted {
		t.Errorf("file_tainted = %v, want true", fileTainted)
	}
}

func TestGetAttachments_Empty(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}

	retrieved, err := store.GetAttachments(turnID)
	if err != nil {
		t.Fatalf("GetAttachments: %v", err)
	}
	if len(retrieved) != 0 {
		t.Errorf("len(retrieved) = %d, want 0", len(retrieved))
	}
}

func TestAttachmentsImmutable(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	conn := db.Conn()

	content1 := []byte("content1")
	hash1 := "1111111111111111111111111111111111111111111111111111111111111111"
	insertToolContent(t, db, hash1, content1)

	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	_, err := conn.Exec(`
		INSERT INTO attachments (id, turn_id, path, sha256, byte_len, recorded_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, "att1", turnID, "file.txt", hash1, len(content1))
	if err != nil {
		t.Fatalf("insert attachment: %v", err)
	}

	_, err = conn.Exec(`UPDATE attachments SET path = ? WHERE id = ?`, "other.txt", "att1")
	if err == nil {
		t.Errorf("UPDATE should have failed")
	}

	_, err = conn.Exec(`DELETE FROM attachments WHERE id = ?`, "att1")
	if err == nil {
		t.Errorf("DELETE should have failed")
	}
}

// TestStoreAttachments_BasesAsObservations tests that attachment bases are
// stored as observations with NULL attempt_id, so read_file can access them
// directly from the observations table.
func TestStoreAttachments_BasesAsObservations(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	content := []byte("attachment_content")
	hash := "4444444444444444444444444444444444444444444444444444444444444444"
	insertToolContent(t, db, hash, content)

	store := journal.NewStore(db)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	turnID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}

	att := journal.Attachment{
		ID:      "att1",
		TurnID:  turnID,
		Path:    "file.txt",
		SHA256:  hash,
		ByteLen: len(content),
		BaseSHA: hash,
	}

	_, err = store.StoreAttachments(turnID, []journal.Attachment{att})
	if err != nil {
		t.Fatalf("StoreAttachments: %v", err)
	}

	// Verify an observation exists for the attachment path
	var obsID sql.NullString
	var obsAttemptID sql.NullString
	var obsKind sql.NullString
	var obsContentSHA sql.NullString

	err = db.Conn().QueryRow(`
		SELECT id, attempt_id, kind, content_sha256 FROM observations
		WHERE turn_id = ? AND path = ? AND kind = 'f'
	`, turnID, []byte("file.txt")).Scan(&obsID, &obsAttemptID, &obsKind, &obsContentSHA)

	if err == sql.ErrNoRows {
		t.Fatal("attachment base not stored as observation")
	}
	if err != nil {
		t.Fatalf("query observation: %v", err)
	}

	// Verify the observation has NULL attempt_id
	if obsAttemptID.Valid {
		t.Errorf("observation attempt_id = %v, want NULL", obsAttemptID.String)
	}

	// Verify the observation has the correct kind and content
	if obsKind.String != "f" {
		t.Errorf("observation kind = %q, want 'f'", obsKind.String)
	}
	if obsContentSHA.String != hash {
		t.Errorf("observation content_sha256 = %q, want %q", obsContentSHA.String, hash)
	}
}
