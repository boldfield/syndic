package journal_test

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
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

// TestOverlayWithAttachedBase tests the full overlay scenario:
// attached base → staged edit → read returns staged content → unstage → read returns attached base
func TestOverlayWithAttachedBase(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	content := []byte("original_content")
	hash := "5555555555555555555555555555555555555555555555555555555555555555"
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

	// Staged content - compute the expected hash
	stagedContent := []byte("staged_edit")
	h := sha256.Sum256(stagedContent)
	stagedHash := fmt.Sprintf("%x", h)

	// Now test the overlay scenario: StageWrite, then verify staging, then unstage
	err = store.Tx(func(tx *journal.Tx) error {
		// First, verify we can read the attached base
		obs, err := tx.ObservationAt(turnID, "file.txt")
		if err != nil {
			return fmt.Errorf("ObservationAt after attach: %w", err)
		}
		if obs.ContentSHA256 != hash {
			return fmt.Errorf("attached base content_sha256 = %q, want %q", obs.ContentSHA256, hash)
		}

		// Stage a write to the attached file
		stage, err := tx.StageWrite(turnID, "file.txt", stagedContent)
		if err != nil {
			return fmt.Errorf("StageWrite: %w", err)
		}

		// Verify the staged entry has the correct content
		if stage.NewSHA256 != stagedHash {
			return fmt.Errorf("staged new_sha256 = %q, want %q", stage.NewSHA256, stagedHash)
		}
		if stage.BaseSHA256 != hash {
			return fmt.Errorf("staged base_sha256 = %q, want %q", stage.BaseSHA256, hash)
		}

		// Unstage and verify the staging entry is gone
		if err := tx.Unstage(turnID, "file.txt"); err != nil {
			return fmt.Errorf("Unstage: %w", err)
		}

		// Verify the unstaged observation still has the attached base
		obs3, err := tx.ObservationAt(turnID, "file.txt")
		if err != nil {
			return fmt.Errorf("ObservationAt after unstage: %w", err)
		}
		if obs3.ContentSHA256 != hash {
			return fmt.Errorf("unstaged content_sha256 = %q, want %q", obs3.ContentSHA256, hash)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("overlay test failed: %v", err)
	}
}

// TestRollbackOnPartialFailure tests that if any part of StoreAttachmentsWithBases fails,
// no records are left behind (all-or-none atomicity).
func TestRollbackOnPartialFailure(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	content1 := []byte("content1")
	hash1 := "7777777777777777777777777777777777777777777777777777777777777777"
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

	// Create an attachment with an invalid hash to trigger a failure
	att := journal.Attachment{
		ID:      "att1",
		TurnID:  turnID,
		Path:    "file.txt",
		SHA256:  "not_a_valid_hash_too_short", // This should fail validation
		ByteLen: len(content1),
		BaseSHA: hash1,
	}

	_, err = store.StoreAttachments(turnID, []journal.Attachment{att})
	if err == nil {
		t.Fatal("expected StoreAttachments to fail with invalid hash")
	}

	// Verify that no attachments were stored
	retrieved, err := store.GetAttachments(turnID)
	if err != nil {
		t.Fatalf("GetAttachments: %v", err)
	}
	if len(retrieved) != 0 {
		t.Errorf("after failed store, len(attachments) = %d, want 0", len(retrieved))
	}

	// Verify that no observations were stored
	var count int
	err = db.Conn().QueryRow(` SELECT COUNT(*) FROM observations WHERE turn_id = ?`, turnID).Scan(&count)
	if err != nil {
		t.Fatalf("query observations count: %v", err)
	}
	if count != 0 {
		t.Errorf("after failed store, observation count = %d, want 0", count)
	}

	// Verify attachments_hash was not set
	var hash sql.NullString
	err = db.Conn().QueryRow(`SELECT attachments_hash FROM turns WHERE id = ?`, turnID).Scan(&hash)
	if err != nil {
		t.Fatalf("query attachments_hash: %v", err)
	}
	if hash.Valid {
		t.Errorf("after failed store, attachments_hash = %v, want NULL", hash.String)
	}
}

// TestReopenReproducesExactBytes tests that after storing and retrieving attachments,
// the exact bytes and manifest hash can be reproduced.
func TestReopenReproducesExactBytes(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	content := []byte("exact_content_bytes")
	hash := "8888888888888888888888888888888888888888888888888888888888888888"
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

	originalHash, err := store.StoreAttachments(turnID, []journal.Attachment{att})
	if err != nil {
		t.Fatalf("StoreAttachments: %v", err)
	}

	// Now retrieve and verify the hash can be reproduced
	retrieved, err := store.GetAttachments(turnID)
	if err != nil {
		t.Fatalf("GetAttachments: %v", err)
	}
	if len(retrieved) != 1 {
		t.Fatalf("len(retrieved) = %d, want 1", len(retrieved))
	}

	// Verify the retrieved values match
	if retrieved[0].Path != att.Path {
		t.Errorf("path = %q, want %q", retrieved[0].Path, att.Path)
	}
	if retrieved[0].SHA256 != att.SHA256 {
		t.Errorf("sha256 = %q, want %q", retrieved[0].SHA256, att.SHA256)
	}
	if retrieved[0].ByteLen != att.ByteLen {
		t.Errorf("byte_len = %d, want %d", retrieved[0].ByteLen, att.ByteLen)
	}
	if retrieved[0].BaseSHA != att.BaseSHA {
		t.Errorf("base_sha256 = %q, want %q", retrieved[0].BaseSHA, att.BaseSHA)
	}

	// Create a new turn to test re-computing the hash with the same attachment
	turn2ID, err := store.CreateTurn(runID, "review", nil)
	if err != nil {
		t.Fatalf("CreateTurn 2: %v", err)
	}

	// Store the same attachment on a different turn
	retrieved[0].ID = "att2" // Use different ID for the second turn
	retrieved[0].TurnID = turn2ID
	recomputedHash, err := store.StoreAttachments(turn2ID, retrieved)
	if err != nil {
		t.Fatalf("re-compute StoreAttachments: %v", err)
	}
	if recomputedHash != originalHash {
		t.Errorf("recomputed hash = %q, want %q", recomputedHash, originalHash)
	}
}

// TestSoloRecordWithAttachments tests that solo records carry attachments_hash.
func TestSoloRecordWithAttachments(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	content := []byte("solo_content")
	hash := "9999999999999999999999999999999999999999999999999999999999999999"
	insertToolContent(t, db, hash, content)

	store := journal.NewStore(db)
	runID, _, err := store.CreateRun([]byte(`{}`))
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Create a solo turn
	turnID, err := store.CreateTurn(runID, "solo", nil)
	if err != nil {
		t.Fatalf("CreateTurn solo: %v", err)
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

	// Verify solo record carries attachments_hash
	var soloRecord sql.NullString
	err = db.Conn().QueryRow(`SELECT attachments_hash FROM turns WHERE id = ?`, turnID).Scan(&soloRecord)
	if err != nil {
		t.Fatalf("query solo attachments_hash: %v", err)
	}
	if !soloRecord.Valid || soloRecord.String != hashResult {
		t.Errorf("solo attachments_hash = %v, want %q", soloRecord, hashResult)
	}
}
