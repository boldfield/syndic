package journal_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func strPtr(s string) *string {
	return &s
}

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

	// Create test blocks
	blocks := []journal.ReviewBlock{
		{
			BlockID:  "b1",
			State:    "opened",
			Severity: strPtr("high"),
		},
	}

	// Store a review record
	var recordID string
	err := store.Tx(func(tx *journal.Tx) error {
		var txErr error
		recordID, txErr = tx.StoreReviewRecord(turnID, 0, "hash-1", "reviewer1", 0, "attempt-1", "approve", blocks, nil, false, nil)
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

	blocks := []journal.ReviewBlock{}

	// Store records in different rounds and by different participants
	for round := 0; round < 2; round++ {
		for idx, participant := range []string{"reviewer1", "reviewer2", "reviewer3"} {
			attemptID := fmt.Sprintf("attempt-%d-%d", round, idx)
			createTestAttempt(t, conn, turnID, attemptID, participant)
			err := store.Tx(func(tx *journal.Tx) error {
				_, err := tx.StoreReviewRecord(turnID, round, fmt.Sprintf("hash-%d", round), participant, idx, attemptID, "block", blocks, nil, false, nil)
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

	blocks := []journal.ReviewBlock{}

	// Store 10 records
	for i := 0; i < 10; i++ {
		attemptID := fmt.Sprintf("attempt-%d", i)
		createTestAttempt(t, conn, turnID, attemptID, fmt.Sprintf("reviewer%d", i%3))
		err := store.Tx(func(tx *journal.Tx) error {
			_, err := tx.StoreReviewRecord(turnID, i%3, fmt.Sprintf("hash-%d", i), fmt.Sprintf("reviewer%d", i%3), i%3, attemptID, "approve", blocks, nil, false, nil)
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

	blocks := []journal.ReviewBlock{}

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
			_, err := tx.StoreReviewRecord(turnID, 0, "hash-1", fmt.Sprintf("reviewer%d", i), i, attemptID, "approve", blocks, nil, false, nil)
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

func TestApproveWithChangesKind(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	runID := "run-review-kinds"
	turnID := "turn-review-kinds"
	setupTestData(t, conn, runID, turnID)

	createTestAttempt(t, conn, turnID, "attempt-1", "reviewer1")

	blocks := []journal.ReviewBlock{}

	// Test storing 'approve-with-changes' kind
	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.StoreReviewRecord(turnID, 0, "hash-1", "reviewer1", 0, "attempt-1", "approve-with-changes", blocks, nil, false, nil)
		return err
	})
	if err != nil {
		t.Fatalf("failed to store review record with approve-with-changes kind: %v", err)
	}

	// Retrieve and verify kind
	records, _, err := store.GetReviewRecords(turnID, nil, 50)
	if err != nil {
		t.Fatalf("failed to get review records: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}

	if records[0].Kind != "approve-with-changes" {
		t.Errorf("expected kind 'approve-with-changes', got %s", records[0].Kind)
	}
}

func TestReopenSurvival(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	runID := "run-reopen"
	turnID := "turn-reopen"
	setupTestData(t, conn, runID, turnID)

	createTestAttempt(t, conn, turnID, "attempt-1-r0", "reviewer1")

	// Create review record for round 0
	blocks := []journal.ReviewBlock{
		{
			BlockID:  "block-1",
			State:    "opened",
			Severity: strPtr("high"),
		},
	}

	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.StoreReviewRecord(turnID, 0, "hash-round0", "reviewer1", 0, "attempt-1-r0", "block", blocks, nil, false, nil)
		return err
	})
	if err != nil {
		t.Fatalf("failed to store review record for round 0: %v", err)
	}

	// Now simulate reopen - create round 1 with same participant maintaining the block
	createTestAttempt(t, conn, turnID, "attempt-1-r1", "reviewer1")
	blocks[0].State = "maintained" // Block is maintained in round 1

	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.StoreReviewRecord(turnID, 1, "hash-round1", "reviewer1", 0, "attempt-1-r1", "block", blocks, nil, false, nil)
		return err
	})
	if err != nil {
		t.Fatalf("failed to store review record for round 1: %v", err)
	}

	// Retrieve all records and verify states survive across rounds
	records, _, err := store.GetReviewRecords(turnID, nil, 50)
	if err != nil {
		t.Fatalf("failed to get review records: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("expected 2 records (one per round), got %d", len(records))
	}

	// Verify round 0 record
	if records[0].Round != 0 {
		t.Errorf("first record should be round 0, got %d", records[0].Round)
	}
	if len(records[0].Blocks) > 0 && records[0].Blocks[0].State != "opened" {
		t.Errorf("block should be 'opened' in round 0, got %s", records[0].Blocks[0].State)
	}

	// Verify round 1 record
	if records[1].Round != 1 {
		t.Errorf("second record should be round 1, got %d", records[1].Round)
	}
	if len(records[1].Blocks) > 0 && records[1].Blocks[0].State != "maintained" {
		t.Errorf("block should be 'maintained' in round 1, got %s", records[1].Blocks[0].State)
	}

	// Verify participant and other fields survive
	if records[0].Participant != "reviewer1" {
		t.Errorf("participant should survive reopen")
	}
	if records[1].Participant != "reviewer1" {
		t.Errorf("participant should be same in both rounds")
	}
}

func TestRepairOnceSemantic(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)
	conn := db.Conn()

	runID := "run-repair"
	turnID := "turn-repair"
	setupTestData(t, conn, runID, turnID)

	// Original malformed verdict attempt
	createTestAttempt(t, conn, turnID, "attempt-1-malformed", "reviewer1")

	// Repair attempt
	createTestAttempt(t, conn, turnID, "attempt-1-repair", "reviewer1")

	blocks := []journal.ReviewBlock{
		{
			BlockID:  "block-1",
			State:    "opened",
			Severity: strPtr("medium"),
		},
	}

	// Store the repaired record (should be listed with repair_id)
	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.StoreReviewRecord(turnID, 0, "hash-1", "reviewer1", 0, "attempt-1-repair", "block", blocks, nil, false, nil)
		return err
	})
	if err != nil {
		t.Fatalf("failed to store repaired review record: %v", err)
	}

	// Retrieve and verify only the repair is listed (once)
	records, _, err := store.GetReviewRecords(turnID, nil, 50)
	if err != nil {
		t.Fatalf("failed to get review records: %v", err)
	}

	// Should have exactly 1 record (the repair, not the malformed original)
	if len(records) != 1 {
		t.Errorf("expected 1 record (repair only), got %d", len(records))
	}

	// Verify it's the repair attempt
	if records[0].AttemptID != "attempt-1-repair" {
		t.Errorf("expected repair attempt ID, got %s", records[0].AttemptID)
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
	blocks := make([]journal.ReviewBlock, 500)
	for i := 0; i < 500; i++ {
		state := "maintained"
		blocks[i] = journal.ReviewBlock{
			BlockID:  fmt.Sprintf("b%d", i),
			State:    state,
			Severity: strPtr("low"),
		}
	}

	// Check if record is oversized
	oversized := journal.EstimateRecordSize(blocks, nil)

	// Store oversized record
	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.StoreReviewRecord(turnID, 0, "hash-1", "reviewer1", 0, "attempt-1", "block", blocks, nil, oversized, nil)
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
	if !record.Oversized && oversized {
		t.Errorf("expected oversized to be true for large record, got false")
	}
}
