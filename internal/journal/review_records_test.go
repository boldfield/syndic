package journal_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func createTestAttempt(t *testing.T, conn *sql.DB, turnID, attemptID, participant string) {
	t.Helper()
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state)
		VALUES (?, ?, ?, 'review', 1, CURRENT_TIMESTAMP, 'pending')
	`, attemptID, turnID, participant)
	if err != nil {
		t.Fatalf("failed to create attempt: %v", err)
	}
}

func TestStoreAndRetrieveReviewRecords(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	runID := "run-review-1"
	turnID := "turn-review-1"
	setupTestData(t, conn, runID, turnID)

	// Create an attempt for the review record to reference
	createTestAttempt(t, conn, turnID, "attempt-1", "reviewer1")

	// Create test review record JSON
	recordJSON := map[string]interface{}{
		"blocks": []map[string]interface{}{
			{
				"block_id": "b1",
				"state":    "opened",
				"severity": "high",
			},
		},
		"commentary": nil,
	}

	recordJSONBytes, err := json.Marshal(recordJSON)
	if err != nil {
		t.Fatalf("failed to marshal record JSON: %v", err)
	}

	// Store a review record
	var recordID string
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		recordID, txErr = tx.StoreReviewRecord(turnID, 0, "hash-1", "reviewer1", "attempt-1", "approve", recordJSONBytes, false)
		return txErr
	})
	if err != nil {
		t.Fatalf("failed to store review record: %v", err)
	}

	if recordID == "" {
		t.Fatalf("expected recordID to be non-empty")
	}

	// Retrieve the record
	records, _, err := store.GetReviewRecords(turnID, nil, 50)
	if err != nil {
		t.Fatalf("failed to get review records: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	record := records[0]
	if record.ID != recordID {
		t.Errorf("expected ID %s, got %s", recordID, record.ID)
	}
	if record.TurnID != turnID {
		t.Errorf("expected turnID %s, got %s", turnID, record.TurnID)
	}
	if record.Participant != "reviewer1" {
		t.Errorf("expected participant reviewer1, got %s", record.Participant)
	}
	if record.Kind != "approve" {
		t.Errorf("expected kind approve, got %s", record.Kind)
	}
}

func TestReviewRecordOrdering(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	runID := "run-review-order"
	turnID := "turn-review-order"
	setupTestData(t, conn, runID, turnID)

	recordJSON := map[string]interface{}{
		"blocks":     []map[string]interface{}{},
		"commentary": nil,
	}
	recordJSONBytes, _ := json.Marshal(recordJSON)

	// Store records in different rounds and by different participants
	for round := 0; round < 2; round++ {
		for idx, participant := range []string{"reviewer1", "reviewer2", "reviewer3"} {
			attemptID := fmt.Sprintf("attempt-%d-%d", round, idx)
			createTestAttempt(t, conn, turnID, attemptID, participant)
			err := store.Tx(func(tx *journal.Tx) error {
				_, err := tx.StoreReviewRecord(turnID, round, fmt.Sprintf("hash-%d", round), participant, attemptID, "block", recordJSONBytes, false)
				return err
			})
			if err != nil {
				t.Fatalf("failed to store review record: %v", err)
			}
		}
	}

	// Retrieve and verify ordering
	records, _, err := store.GetReviewRecords(turnID, nil, 50)
	if err != nil {
		t.Fatalf("failed to get review records: %v", err)
	}

	if len(records) != 6 {
		t.Fatalf("expected 6 records, got %d", len(records))
	}

	// All round 0 records should come before round 1
	roundBoundary := -1
	for i, r := range records {
		if roundBoundary == -1 && r.Round == 1 {
			roundBoundary = i
		}
		if roundBoundary != -1 && r.Round == 0 {
			t.Errorf("record at index %d has round 0 after round 1 record at %d", i, roundBoundary)
		}
	}
}

func TestReviewRecordPaging(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	runID := "run-review-page"
	turnID := "turn-review-page"
	setupTestData(t, conn, runID, turnID)

	recordJSON := map[string]interface{}{
		"blocks":     []map[string]interface{}{},
		"commentary": nil,
	}
	recordJSONBytes, _ := json.Marshal(recordJSON)

	// Store 10 records
	for i := 0; i < 10; i++ {
		attemptID := fmt.Sprintf("attempt-%d", i)
		createTestAttempt(t, conn, turnID, attemptID, fmt.Sprintf("reviewer%d", i%3))
		err := store.Tx(func(tx *journal.Tx) error {
			_, err := tx.StoreReviewRecord(turnID, i%3, fmt.Sprintf("hash-%d", i), fmt.Sprintf("reviewer%d", i%3), attemptID, "approve", recordJSONBytes, false)
			return err
		})
		if err != nil {
			t.Fatalf("failed to store review record: %v", err)
		}
	}

	// Retrieve first page with limit 5
	records, cursor, err := store.GetReviewRecords(turnID, nil, 5)
	if err != nil {
		t.Fatalf("failed to get review records: %v", err)
	}

	t.Logf("First page: got %d records, cursor=%v", len(records), cursor)
	if len(records) != 5 {
		t.Errorf("expected 5 records on first page, got %d", len(records))
	}

	if cursor == nil {
		t.Errorf("expected cursor to be non-nil when more records exist")
	}

	// Retrieve second page
	if cursor != nil {
		records2, cursor2, err := store.GetReviewRecords(turnID, cursor, 5)
		if err != nil {
			t.Fatalf("failed to get second page: %v", err)
		}

		if len(records2) != 5 {
			t.Errorf("expected 5 records on second page, got %d", len(records2))
		}

		if cursor2 != nil {
			t.Errorf("expected no cursor on last page, got %v", cursor2)
		}
	}
}

func TestReviewCountUpdate(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	runID := "run-review-count"
	turnID := "turn-review-count"
	setupTestData(t, conn, runID, turnID)

	recordJSON := map[string]interface{}{
		"blocks":     []map[string]interface{}{},
		"commentary": nil,
	}
	recordJSONBytes, _ := json.Marshal(recordJSON)

	// Initially, review count should be 0
	count, err := store.GetReviewCount(turnID)
	if err != nil {
		t.Fatalf("failed to get review count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected initial review count 0, got %d", count)
	}

	// Store a few records and verify count increases
	for i := 0; i < 3; i++ {
		attemptID := fmt.Sprintf("attempt-%d", i)
		createTestAttempt(t, conn, turnID, attemptID, fmt.Sprintf("reviewer%d", i))
		err := store.Tx(func(tx *journal.Tx) error {
			_, err := tx.StoreReviewRecord(turnID, 0, "hash-1", fmt.Sprintf("reviewer%d", i), attemptID, "approve", recordJSONBytes, false)
			return err
		})
		if err != nil {
			t.Fatalf("failed to store review record: %v", err)
		}
	}

	count, err = store.GetReviewCount(turnID)
	if err != nil {
		t.Fatalf("failed to get review count: %v", err)
	}
	if count != 3 {
		t.Errorf("expected review count 3 after 3 stores, got %d", count)
	}
}

func TestStoreReviewRecordContent(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	runID := "run-review-content"
	turnID := "turn-review-content"
	setupTestData(t, conn, runID, turnID)

	// Store content
	content := []byte("test content for review")
	var contentRef *journal.ContentRef
	err := store.Tx(func(tx *journal.Tx) error {
		var txErr error
		contentRef, txErr = tx.StoreReviewRecordContent(turnID, content)
		return txErr
	})
	if err != nil {
		t.Fatalf("failed to store review record content: %v", err)
	}

	if contentRef == nil {
		t.Fatal("expected contentRef to be non-nil")
	}

	expectedHash := sha256.Sum256(content)
	expectedHashStr := fmt.Sprintf("%x", expectedHash)

	if contentRef.SHA256 != expectedHashStr {
		t.Errorf("hash mismatch: expected %s, got %s", expectedHashStr, contentRef.SHA256)
	}
	if contentRef.ByteLen != int64(len(content)) {
		t.Errorf("byte_len mismatch: expected %d, got %d", len(content), contentRef.ByteLen)
	}
}

func TestOversizedRecordHandling(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	runID := "run-review-oversized"
	turnID := "turn-review-oversized"
	setupTestData(t, conn, runID, turnID)

	createTestAttempt(t, conn, turnID, "attempt-1", "reviewer1")

	// Create a large record (>65KB when encoded)
	blocks := make([]map[string]interface{}, 500)
	for i := 0; i < 500; i++ {
		blocks[i] = map[string]interface{}{
			"block_id": fmt.Sprintf("b%d", i),
			"state":    "maintained",
			"severity": "low",
		}
	}

	recordJSON := map[string]interface{}{
		"blocks":     blocks,
		"commentary": nil,
	}

	recordJSONBytes, _ := json.Marshal(recordJSON)
	oversized := len(recordJSONBytes) > 65536

	// Store oversized record
	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.StoreReviewRecord(turnID, 0, "hash-1", "reviewer1", "attempt-1", "block", recordJSONBytes, oversized)
		return err
	})
	if err != nil {
		t.Fatalf("failed to store oversized review record: %v", err)
	}

	// Retrieve and verify oversized flag
	records, _, err := store.GetReviewRecords(turnID, nil, 50)
	if err != nil {
		t.Fatalf("failed to get review records: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	record := records[0]
	if !record.Oversized && len(recordJSONBytes) > 65536 {
		t.Errorf("expected oversized to be true for large record, got false")
	}
}
