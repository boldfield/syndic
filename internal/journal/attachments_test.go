package journal_test

import (
	"database/sql"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

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
		SHA256:  "hash1",
		ByteLen: 5,
		BaseSHA: "hash1",
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
	if retrieved[0].BaseSHA != "hash1" {
		t.Errorf("base_sha256 = %q, want %q", retrieved[0].BaseSHA, "hash1")
	}
}

func TestStoreAttachments_Multiple_Sorted(t *testing.T) {
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

	attachments := []journal.Attachment{
		{ID: "att3", TurnID: turnID, Path: "z.txt", SHA256: "hash3", ByteLen: 8, BaseSHA: "hash3"},
		{ID: "att1", TurnID: turnID, Path: "a.txt", SHA256: "hash1", ByteLen: 8, BaseSHA: "hash1"},
		{ID: "att2", TurnID: turnID, Path: "m.txt", SHA256: "hash2", ByteLen: 8, BaseSHA: "hash2"},
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

func TestSetTurnAttachmentsHash(t *testing.T) {
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

	testHash := "a" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b" + "b"

	err = store.SetTurnAttachmentsHash(turnID, testHash)
	if err != nil {
		t.Fatalf("SetTurnAttachmentsHash: %v", err)
	}

	var stored sql.NullString
	if err := db.Conn().QueryRow(`SELECT attachments_hash FROM turns WHERE id = ?`, turnID).Scan(&stored); err != nil {
		t.Fatalf("query attachments_hash: %v", err)
	}
	if !stored.Valid || stored.String != testHash {
		t.Errorf("attachments_hash = %v, want %q", stored, testHash)
	}
}

func TestSetTurnAttachmentsHash_None(t *testing.T) {
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

	err = store.SetTurnAttachmentsHash(turnID, "none")
	if err != nil {
		t.Fatalf("SetTurnAttachmentsHash: %v", err)
	}

	var stored sql.NullString
	if err := db.Conn().QueryRow(`SELECT attachments_hash FROM turns WHERE id = ?`, turnID).Scan(&stored); err != nil {
		t.Fatalf("query attachments_hash: %v", err)
	}
	if !stored.Valid || stored.String != "none" {
		t.Errorf("attachments_hash = %v, want %q", stored, "none")
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

	runID := "run-1"
	turnID := "turn-1"
	setupTestData(t, conn, runID, turnID)

	_, err := conn.Exec(`
		INSERT INTO attachments (id, turn_id, path, sha256, byte_len, recorded_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, "att1", turnID, "file.txt", "hash1", 7)
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
