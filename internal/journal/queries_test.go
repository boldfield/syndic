package journal_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

func testOpenDB(t *testing.T) (*journal.DB, func()) {
	tempDir := t.TempDir()
	db, err := journal.Open(tempDir + "/test.db")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	return db, func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close db: %v", err)
		}
	}
}

func TestListAttempts(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID string
	var turnID string
	var attemptID1, attemptID2 string

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		attemptID1, err = tx.BeginAttempt(turnID, "participant-1", "phase1", 0, &now, "")
		if err != nil {
			return err
		}

		now = now.Add(100 * time.Millisecond)

		attemptID2, err = tx.BeginAttempt(turnID, "participant-2", "phase1", 0, &now, "")
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	attempts, err := store.ListAttempts(turnID)
	if err != nil {
		t.Fatalf("ListAttempts failed: %v", err)
	}

	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}

	if attempts[0].ID != attemptID1 {
		t.Errorf("first attempt ID mismatch: got %s, want %s", attempts[0].ID, attemptID1)
	}

	if attempts[1].ID != attemptID2 {
		t.Errorf("second attempt ID mismatch: got %s, want %s", attempts[1].ID, attemptID2)
	}
}

func TestTranscriptPagePaging(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID string
	var turnID string

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		msgContent := []byte("message 1")
		if _, _, _, err := tx.AppendMessage(turnID, "phase1", "sender1", []string{"receiver1"}, msgContent, nil); err != nil {
			return err
		}

		msgContent = []byte("message 2")
		if _, _, _, err := tx.AppendMessage(turnID, "phase1", "sender2", []string{"receiver2"}, msgContent, nil); err != nil {
			return err
		}

		now := time.Now().UTC()
		if _, err := tx.BeginAttempt(turnID, "participant-1", "phase1", 0, &now, ""); err != nil {
			return err
		}

		if _, err := tx.BeginAttempt(turnID, "participant-2", "phase1", 0, &now, ""); err != nil {
			return err
		}

		if _, err := tx.BeginAttempt(turnID, "participant-3", "phase1", 0, &now, ""); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	items1, cursor1, err := store.TranscriptPage(turnID, "", 2)
	if err != nil {
		t.Fatalf("first page failed: %v", err)
	}

	if len(items1) != 2 {
		t.Errorf("expected 2 items on first page, got %d", len(items1))
	}

	if cursor1 == "" {
		t.Error("expected non-empty cursor for second page")
	}

	items2, cursor2, err := store.TranscriptPage(turnID, cursor1, 2)
	if err != nil {
		t.Fatalf("second page failed: %v", err)
	}

	if len(items2) != 2 {
		t.Errorf("expected 2 items on second page, got %d", len(items2))
	}

	items3, cursor3, err := store.TranscriptPage(turnID, cursor2, 2)
	if err != nil {
		t.Fatalf("third page failed: %v", err)
	}

	if len(items3) != 1 {
		t.Errorf("expected 1 item on third page, got %d", len(items3))
	}

	if cursor3 != "" {
		t.Error("expected empty cursor at end")
	}

	allItems, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("full page failed: %v", err)
	}

	if len(allItems) != 5 {
		t.Fatalf("expected 5 total items, got %d", len(allItems))
	}
}

func TestTranscriptPageLimitClamping(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID string
	var turnID string

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		for i := 0; i < 5; i++ {
			msgContent := []byte("message")
			if _, _, _, err := tx.AppendMessage(turnID, "phase1", "sender", []string{"receiver"}, msgContent, nil); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	items0, _, err := store.TranscriptPage(turnID, "", 0)
	if err != nil {
		t.Fatalf("limit 0 failed: %v", err)
	}

	if len(items0) != 1 {
		t.Errorf("limit 0 should clamp to 1, got %d items", len(items0))
	}

	items1000, _, err := store.TranscriptPage(turnID, "", 1000)
	if err != nil {
		t.Fatalf("limit 1000 failed: %v", err)
	}

	if len(items1000) != 5 {
		t.Errorf("limit 1000 should return all 5 items, got %d", len(items1000))
	}
}

func TestContentRangeMultibyte(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID string
	var turnID string
	var msgID string
	var expectedSHA256 string

	content := []byte("Hello 🌍 \"quoted\" \\backslash\n\r\ttabs\x00control")
	expectedH := sha256.Sum256(content)
	expectedSHA256 = hex.EncodeToString(expectedH[:])

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		msgID, _, _, err = tx.AppendMessage(turnID, "phase1", "sender", []string{"receiver"}, content, nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	ref := "msg:" + msgID

	chunkSize := int64(10)
	var assembled []byte

	for offset := int64(0); offset < int64(len(content)); {
		data, total, sha256, err := store.ContentRange(ref, offset, chunkSize)
		if err != nil {
			t.Fatalf("ContentRange at offset %d failed: %v", offset, err)
		}

		if total != int64(len(content)) {
			t.Errorf("ContentRange total mismatch: got %d, want %d", total, len(content))
		}

		if sha256 != expectedSHA256 {
			t.Errorf("ContentRange SHA256 mismatch: got %s, want %s", sha256, expectedSHA256)
		}

		assembled = append(assembled, data...)
		offset += int64(len(data))
	}

	if !bytes.Equal(assembled, content) {
		t.Errorf("reassembled content mismatch")
	}
}

func TestContentRange1MBLimit(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID string
	var turnID string
	var msgID string

	content := make([]byte, 300000)
	for i := range content {
		content[i] = byte(i % 256)
	}

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		msgID, _, _, err = tx.AppendMessage(turnID, "phase1", "sender", []string{"receiver"}, content, nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	ref := "msg:" + msgID

	data, total, _, err := store.ContentRange(ref, 0, 1000000)
	if err != nil {
		t.Fatalf("ContentRange failed: %v", err)
	}

	if int64(len(data)) > 262144 {
		t.Errorf("ContentRange returned %d bytes, limit is 262144", len(data))
	}

	if total != int64(len(content)) {
		t.Errorf("total mismatch: got %d, want %d", total, len(content))
	}
}

func TestContentRangeRefTypes(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID string
	var turnID string
	var msgID string
	var candidateID string

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		msgContent := []byte("message content")
		msgID, _, _, err = tx.AppendMessage(turnID, "phase1", "sender", []string{"receiver"}, msgContent, nil)
		if err != nil {
			return err
		}

		candidateContent := []byte("candidate content")
		h := sha256.Sum256(candidateContent)
		candidateHash := hex.EncodeToString(h[:])
		candidateID, err = tx.CreateCandidate(turnID, string(candidateContent), candidateHash)
		return err
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	tests := []struct {
		name        string
		ref         string
		shouldExist bool
	}{
		{"msg ref", "msg:" + msgID, true},
		{"candidate ref", "candidate:" + candidateID, true},
		{"unknown msg", "msg:missing", false},
		{"unknown candidate", "candidate:missing", false},
		{"bad type", "nope:1", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := store.ContentRange(tc.ref, 0, 100)

			if tc.shouldExist {
				if err != nil {
					t.Errorf("ContentRange failed: %v", err)
				}
			} else {
				if !errors.Is(err, journal.ErrNoSuchContent) {
					t.Errorf("expected ErrNoSuchContent, got %v", err)
				}
			}
		})
	}
}

func TestContentRangeNegativeOffset(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID string
	var turnID string
	var msgID string

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		msgContent := []byte("content")
		msgID, _, _, err = tx.AppendMessage(turnID, "phase1", "sender", []string{"receiver"}, msgContent, nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	ref := "msg:" + msgID

	_, _, _, err2 := store.ContentRange(ref, -1, 100)
	if err2 == nil {
		t.Error("expected error for negative offset")
	}
}

func TestListBlocksIncludesOverridden(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var blockID string

	var runID string
	var turnID string

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		attemptID, err := tx.BeginAttempt(turnID, "participant", "phase", 0, &now, "")
		if err != nil {
			return err
		}

		verdictID, err := tx.RecordVerdict(attemptID, "hash", "block", "reason", "valid")
		if err != nil {
			return err
		}

		blockID, err = tx.OpenBlockFor(verdictID, "reviewer-block-id", "hash", "high", "evidence", "remedy")
		if err != nil {
			return err
		}

		err = tx.OverrideBlock(blockID)
		return err
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	blocks, err := store.ListBlocks(turnID)
	if err != nil {
		t.Fatalf("ListBlocks failed: %v", err)
	}

	found := false
	for _, b := range blocks {
		if b.ID == blockID {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("overridden block %s not found in ListBlocks results", blockID)
	}
}

func TestContentRangeLargeAttemptOutput(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	content := make([]byte, 700000)
	for i := range content {
		switch i % 4 {
		case 0:
			content[i] = byte('a' + (i % 26))
		case 1:
			if i%3 == 0 {
				content[i] = '"'
			} else if i%5 == 0 {
				content[i] = '\\'
			} else {
				content[i] = '\n'
			}
		case 2:
			if i%6 == 0 {
				content[i] = 0xF0 + byte(i%4)
			} else {
				content[i] = byte('A' + (i % 26))
			}
		default:
			if i%7 == 0 {
				content[i] = 0
			} else if i%8 == 0 {
				content[i] = 0x80 + byte(i%64)
			} else {
				content[i] = byte(i % 256)
			}
		}
	}

	var runID, turnID, attemptID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		attemptID, err = tx.BeginAttempt(turnID, "participant", "phase", 0, &now, "")
		if err != nil {
			return err
		}

		contentStr := string(content)
		return tx.CompleteAttempt(attemptID, contentStr, 0, "unknown", "complete", "", "ok")
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	ref := "attempt:" + attemptID
	expectedH := sha256.Sum256(content)
	expectedSHA256 := hex.EncodeToString(expectedH[:])

	var assembled []byte
	chunkSize := int64(262144)

	for offset := int64(0); offset < int64(len(content)); {
		data, total, sha256, err := store.ContentRange(ref, offset, chunkSize)
		if err != nil {
			t.Fatalf("ContentRange at offset %d failed: %v", offset, err)
		}

		if total != int64(len(content)) {
			t.Errorf("ContentRange total mismatch at offset %d: got %d, want %d", offset, total, len(content))
		}

		if sha256 != expectedSHA256 {
			t.Errorf("ContentRange SHA256 mismatch: got %s, want %s", sha256, expectedSHA256)
		}

		assembled = append(assembled, data...)
		offset += int64(len(data))
	}

	if !bytes.Equal(assembled, content) {
		t.Errorf("reassembled large attempt output mismatch")
	}
}

func TestTranscriptPageOrderingAndDistinctness(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID, turnID string
	var msgIDs []string
	var attemptIDs []string

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		msgContent := []byte("message 1")
		msgID, _, _, err := tx.AppendMessage(turnID, "phase1", "sender1", []string{"receiver1"}, msgContent, nil)
		if err != nil {
			return err
		}
		msgIDs = append(msgIDs, msgID)

		msgContent = []byte("message 2")
		msgID, _, _, err = tx.AppendMessage(turnID, "phase1", "sender2", []string{"receiver2"}, msgContent, nil)
		if err != nil {
			return err
		}
		msgIDs = append(msgIDs, msgID)

		now := time.Now().UTC()
		attemptID, err := tx.BeginAttempt(turnID, "participant-1", "phase1", 0, &now, "")
		if err != nil {
			return err
		}
		attemptIDs = append(attemptIDs, attemptID)

		attemptID, err = tx.BeginAttempt(turnID, "participant-2", "phase1", 0, &now, "")
		if err != nil {
			return err
		}
		attemptIDs = append(attemptIDs, attemptID)

		attemptID, err = tx.BeginAttempt(turnID, "participant-3", "phase1", 0, &now, "")
		if err != nil {
			return err
		}
		attemptIDs = append(attemptIDs, attemptID)

		return nil
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	allItems, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("full page failed: %v", err)
	}

	if len(allItems) != 5 {
		t.Fatalf("expected 5 total items, got %d", len(allItems))
	}

	itemRefs := make(map[string]bool)
	for _, item := range allItems {
		if itemRefs[item.Ref] {
			t.Errorf("duplicate item ref: %s", item.Ref)
		}
		itemRefs[item.Ref] = true
	}

	items1, _, err := store.TranscriptPage(turnID, "", 2)
	if err != nil {
		t.Fatalf("first page failed: %v", err)
	}

	for i, item := range allItems {
		if i < len(items1) {
			if item.Ref != items1[i].Ref {
				t.Errorf("item %d: single-page mismatch with paged: got %s, want %s", i, item.Ref, items1[i].Ref)
			}
		}
	}
}

func TestTranscriptPageAttemptStatusAndScrubCount(t *testing.T) {
	db, cleanup := testOpenDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID, turnID, attemptID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte(`{"test":true}`))
		if err != nil {
			return err
		}

		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		attemptID, err = tx.BeginAttempt(turnID, "participant", "phase", 0, &now, "")
		if err != nil {
			return err
		}

		return tx.CompleteAttempt(attemptID, "output content", 2, "unknown", "complete", "", "ok")
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	items, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("TranscriptPage failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}

	item := items[0]
	if item.Kind != "attempt" {
		t.Errorf("expected attempt, got %s", item.Kind)
	}

	if item.Attempt == nil {
		t.Fatal("Attempt field is nil")
	}

	if item.Attempt.RawOutput != nil {
		t.Error("RawOutput should be nil in TranscriptItem")
	}

	if item.Attempt.Status == nil || *item.Attempt.Status != "complete" {
		t.Errorf("expected status 'complete', got %v", item.Attempt.Status)
	}

	if item.Attempt.ScrubCount == nil || *item.Attempt.ScrubCount != 2 {
		t.Errorf("expected scrub count 2, got %v", item.Attempt.ScrubCount)
	}
}
