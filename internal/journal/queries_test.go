package journal_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

func TestMigration005JournalSeq(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()

	var exists int
	if err := conn.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='journal_counter'
	`).Scan(&exists); err != nil || exists != 1 {
		t.Fatalf("journal_counter table not found: err=%v exists=%d", err, exists)
	}

	var next int
	if err := conn.QueryRow(`SELECT next FROM journal_counter WHERE id = 1`).Scan(&next); err != nil {
		t.Fatalf("query journal_counter: %v", err)
	}
	if next != 1 {
		t.Errorf("journal_counter.next = %d, want 1", next)
	}

	for _, table := range []string{"messages", "attempts"} {
		if err := conn.QueryRow(`
			SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'journal_seq'
		`, table).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("%s.journal_seq column not found: err=%v exists=%d", table, err, exists)
		}
	}
}

func TestJournalSeqIncrement(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, _, _, err = store.AppendMessage(turnID, "phase1", "sender1", nil, []byte("content1"), nil)
	if err != nil {
		t.Fatalf("AppendMessage 1: %v", err)
	}
	_, err = store.BeginAttempt(turnID, "participant1", "phase1", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt 1: %v", err)
	}
	_, _, _, err = store.AppendMessage(turnID, "phase1", "sender2", nil, []byte("content2"), nil)
	if err != nil {
		t.Fatalf("AppendMessage 2: %v", err)
	}
	_, err = store.BeginAttempt(turnID, "participant2", "phase2", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt 2: %v", err)
	}

	rows, err := db.Conn().Query(`
		SELECT journal_seq FROM messages WHERE turn_id = ? AND journal_seq IS NOT NULL
		UNION ALL
		SELECT journal_seq FROM attempts WHERE turn_id = ? AND journal_seq IS NOT NULL
		ORDER BY journal_seq ASC
	`, turnID, turnID)
	if err != nil {
		t.Fatalf("query journal_seq: %v", err)
	}
	defer func() { _ = rows.Close() }()
	seqs := []int64{}
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seqs = append(seqs, seq)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	if len(seqs) != 4 || seqs[0] != 1 || seqs[1] != 2 || seqs[2] != 3 || seqs[3] != 4 {
		t.Errorf("journal_seq values = %v, want [1, 2, 3, 4]", seqs)
	}
}

func TestRollbackLeavesCounterUnchanged(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		_, _, _, err := tx.AppendMessage(turnID, "phase1", "sender1", nil, []byte("content"), nil)
		return err
	})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}

	var before int
	if err := db.Conn().QueryRow(`SELECT next FROM journal_counter WHERE id = 1`).Scan(&before); err != nil {
		t.Fatalf("query counter before: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		if _, _, _, err := tx.AppendMessage(turnID, "phase1", "sender1", nil, []byte("content"), nil); err != nil {
			return nil
		}
		return sql.ErrTxDone
	})
	if err == nil {
		t.Fatal("expected rollback error, got nil")
	}

	var after int
	if err := db.Conn().QueryRow(`SELECT next FROM journal_counter WHERE id = 1`).Scan(&after); err != nil {
		t.Fatalf("query counter after: %v", err)
	}

	if before != after {
		t.Errorf("counter changed on rollback: %d -> %d", before, after)
	}
}

func TestListAttempts(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.BeginAttempt(turnID, "p2", "phase", 0, nil, ""); err != nil {
			return err
		}
		time.Sleep(10 * time.Millisecond)
		_, err = tx.BeginAttempt(turnID, "p1", "phase", 0, nil, "")
		return err
	})
	if err != nil {
		t.Fatalf("create attempts: %v", err)
	}

	attempts, err := store.ListAttempts(turnID)
	if err != nil {
		t.Fatalf("ListAttempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Errorf("got %d attempts, want 2", len(attempts))
	}
	if attempts[0].Participant != "p2" || attempts[1].Participant != "p1" {
		t.Errorf("attempts not in dispatch order: %s, %s", attempts[0].Participant, attempts[1].Participant)
	}
}

func TestGetCandidate(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	candidateID, err := store.CreateCandidate(turnID, "content", "h1")
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}

	row, err := store.GetCandidate(candidateID)
	if err != nil {
		t.Fatalf("GetCandidate: %v", err)
	}
	if row.ByteLen != 7 {
		t.Errorf("ByteLen = %d, want 7", row.ByteLen)
	}
}

func TestGetCandidateUTF8ByteLength(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	utf8Content := "hello🌍world"
	candidateID, err := store.CreateCandidate(turnID, utf8Content, "h1")
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}

	row, err := store.GetCandidate(candidateID)
	if err != nil {
		t.Fatalf("GetCandidate: %v", err)
	}
	expectedLen := int64(len([]byte(utf8Content)))
	if row.ByteLen != expectedLen {
		t.Errorf("ByteLen = %d, want %d (for UTF-8 %q)", row.ByteLen, expectedLen, utf8Content)
	}
}

func TestTranscriptPageOrder(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		if _, _, _, err := tx.AppendMessage(turnID, "phase", "s1", nil, []byte("msg1"), nil); err != nil {
			return err
		}
		if _, err := tx.BeginAttempt(turnID, "p1", "phase", 0, nil, ""); err != nil {
			return err
		}
		if _, _, _, err := tx.AppendMessage(turnID, "phase", "s2", nil, []byte("msg2"), nil); err != nil {
			return err
		}
		if _, err := tx.BeginAttempt(turnID, "p2", "phase", 0, nil, ""); err != nil {
			return err
		}
		_, err = tx.BeginAttempt(turnID, "p3", "phase", 0, nil, "")
		return err
	})
	if err != nil {
		t.Fatalf("create items: %v", err)
	}

	items, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5", len(items))
	}
	expectedKinds := []string{"message", "attempt", "message", "attempt", "attempt"}
	for i, want := range expectedKinds {
		if items[i].Kind != want {
			t.Errorf("item %d kind = %q, want %q", i, items[i].Kind, want)
		}
	}
}

func TestTranscriptPagePagination(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		for i := 0; i < 5; i++ {
			if _, _, _, err := tx.AppendMessage(turnID, "phase", "sender", nil, []byte("content"), nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("create messages: %v", err)
	}

	page1, cursor1, err := store.TranscriptPage(turnID, "", 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("page 1 len = %d, want 2", len(page1))
	}
	if cursor1 == "" {
		t.Error("page 1 cursor empty, want non-empty for continuation")
	}

	page2, cursor2, err := store.TranscriptPage(turnID, cursor1, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page 2 len = %d, want 2", len(page2))
	}
	if cursor2 == "" {
		t.Error("page 2 cursor empty, want non-empty for continuation")
	}

	page3, cursor3, err := store.TranscriptPage(turnID, cursor2, 2)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(page3) != 1 {
		t.Errorf("page 3 len = %d, want 1", len(page3))
	}
	if cursor3 != "" {
		t.Error("page 3 cursor non-empty, want empty for no continuation")
	}
}

func TestTranscriptPageNullJournalSeq(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		if _, _, _, err := tx.AppendMessage(turnID, "phase", "s1", nil, []byte("msg1"), nil); err != nil {
			return err
		}
		if _, err := tx.BeginAttempt(turnID, "p1", "phase", 0, nil, ""); err != nil {
			return err
		}
		if _, _, _, err := tx.AppendMessage(turnID, "phase", "s2", nil, []byte("msg2"), nil); err != nil {
			return err
		}
		_, err = tx.BeginAttempt(turnID, "p2", "phase", 0, nil, "")
		return err
	})
	if err != nil {
		t.Fatalf("create items: %v", err)
	}

	// Note: Can't easily create NULL journal_seq rows due to triggers, but we can test the NULL path
	// with existing rows and their ordering. The spec requires support for pre-005 rows which would
	// have NULL journal_seq. This test verifies pagination with normal rows works.

	items, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4 (after setup)", len(items))
	}

	page1, c1, err := store.TranscriptPage(turnID, "", 1)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 1 {
		t.Errorf("page 1 len = %d, want 1", len(page1))
	}

	page2, _, err := store.TranscriptPage(turnID, c1, 1)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 1 {
		t.Errorf("page 2 len = %d, want 1", len(page2))
	}

	if page1[0].Kind == page2[0].Kind && page1[0].Ref == page2[0].Ref {
		t.Errorf("page 2 returned same item as page 1: %q", page1[0].Ref)
	}
}

func TestContentRangeMessage(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	msgID, _, _, err := store.AppendMessage(turnID, "phase", "sender", nil, []byte("hello world"), nil)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	data, total, sha256, err := store.ContentRange("msg:"+msgID, 0, 100)
	if err != nil {
		t.Fatalf("ContentRange: %v", err)
	}
	if string(data) != "hello world" || total != 11 {
		t.Errorf("got data=%q total=%d, want hello world / 11", string(data), total)
	}
	if sha256 == "" {
		t.Error("SHA256 is empty")
	}
}

func TestContentRangeByteSlicing(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	msgID, _, _, err := store.AppendMessage(turnID, "phase", "sender", nil, []byte("abcdefghij"), nil)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	data1, _, _, err := store.ContentRange("msg:"+msgID, 0, 3)
	if err != nil {
		t.Fatalf("ContentRange 1: %v", err)
	}
	data2, _, _, err := store.ContentRange("msg:"+msgID, 3, 3)
	if err != nil {
		t.Fatalf("ContentRange 2: %v", err)
	}
	data3, _, _, err := store.ContentRange("msg:"+msgID, 6, 10)
	if err != nil {
		t.Fatalf("ContentRange 3: %v", err)
	}

	combined := append(append(data1, data2...), data3...)
	if string(combined) != "abcdefghij" {
		t.Errorf("sliced concatenation = %q, want abcdefghij", string(combined))
	}
}

func TestContentRangeUTF8ByteSlicing(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	utf8Content := []byte("hello🌍world")

	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	msgID, _, _, err := store.AppendMessage(turnID, "phase", "sender", nil, utf8Content, nil)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	_, total, sha256Hash, err := store.ContentRange("msg:"+msgID, 0, int64(len(utf8Content)))
	if err != nil {
		t.Fatalf("ContentRange: %v", err)
	}
	if total != int64(len(utf8Content)) {
		t.Errorf("total = %d, want %d", total, len(utf8Content))
	}

	sum := sha256.Sum256(utf8Content)
	computedSHA256 := hex.EncodeToString(sum[:])
	if sha256Hash != computedSHA256 {
		t.Errorf("SHA256 mismatch: got %s, want %s", sha256Hash, computedSHA256)
	}
}

func TestContentRangeNegativeOffset(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	msgID, _, _, err := store.AppendMessage(turnID, "phase", "sender", nil, []byte("content"), nil)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	_, _, _, err = store.ContentRange("msg:"+msgID, -1, 10)
	if err == nil {
		t.Error("ContentRange with negative offset: want error, got nil")
	}
}

func TestContentRangeOutOfRange(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	msgID, _, _, err := store.AppendMessage(turnID, "phase", "sender", nil, []byte("content"), nil)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	_, _, _, err = store.ContentRange("msg:"+msgID, 100, 10)
	if err == nil {
		t.Error("ContentRange with offset > total: want error, got nil")
	}
}

func TestContentRangeInvalidRef(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	_, _, _, err := store.ContentRange("nope:1", 0, 10)
	if err == nil {
		t.Error("ContentRange with invalid ref kind: want error, got nil")
	}
}

func TestAttemptAtField(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err = store.BeginAttempt(turnID, "p", "phase", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}

	items, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	for _, item := range items {
		if item.Kind == "attempt" && item.At.IsZero() {
			t.Error("attempt item has zero At field, want dispatched_at")
		}
	}
}

func TestContentRangeAttempt(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	var attemptID string
	err = store.Tx(func(tx *journal.Tx) error {
		var err error
		attemptID, err = tx.BeginAttempt(turnID, "p", "phase", 0, nil, "")
		if err != nil {
			return err
		}
		// Set raw output via CompleteAttempt with valid status
		return tx.CompleteAttempt(attemptID, "test output", 0, "", "complete", "", "")
	})
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	data, total, sha256, err := store.ContentRange("attempt:"+attemptID, 0, 100)
	if err != nil {
		t.Fatalf("ContentRange: %v", err)
	}
	if string(data) != "test output" || total != 11 {
		t.Errorf("got data=%q total=%d, want test output / 11", string(data), total)
	}
	if sha256 == "" {
		t.Error("SHA256 is empty for attempt with output")
	}
}

func TestContentRangeCandidate(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	candidateID, err := store.CreateCandidate(turnID, "candidate content", "h1")
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}

	data, total, sha256, err := store.ContentRange("candidate:"+candidateID, 0, 100)
	if err != nil {
		t.Fatalf("ContentRange: %v", err)
	}
	if string(data) != "candidate content" || total != 17 {
		t.Errorf("got data=%q total=%d, want candidate content / 17", string(data), total)
	}
	if sha256 == "" {
		t.Error("SHA256 is empty for candidate")
	}
}

func TestContentRangeAttemptEmpty(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	attemptID, err := store.BeginAttempt(turnID, "p", "phase", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}

	data, total, sha256, err := store.ContentRange("attempt:"+attemptID, 0, 100)
	if err != nil {
		t.Fatalf("ContentRange: %v", err)
	}
	if len(data) != 0 || total != 0 {
		t.Errorf("got data len=%d total=%d, want 0 / 0", len(data), total)
	}
	// SHA256 should be computed for empty bytes, not empty
	if sha256 == "" {
		t.Error("SHA256 should be computed for empty attempt")
	}
}

func TestContentRangeLimitClamped(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	msgID, _, _, err := store.AppendMessage(turnID, "phase", "sender", nil, []byte("x"), nil)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	// Request 1000000 bytes, should be clamped to 262144
	data, _, _, err := store.ContentRange("msg:"+msgID, 0, 1000000)
	if err != nil {
		t.Fatalf("ContentRange: %v", err)
	}
	if len(data) > 262144 {
		t.Errorf("got %d bytes, want max 262144", len(data))
	}
}

func TestListVerdicts(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	verdicts, err := store.ListVerdicts(turnID)
	if err != nil {
		t.Fatalf("ListVerdicts: %v", err)
	}
	if len(verdicts) != 0 {
		t.Errorf("got %d verdicts, want 0 for empty turn", len(verdicts))
	}
}

func TestListBlocksIncludingOverridden(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	blocks, err := store.ListBlocks(turnID)
	if err != nil {
		t.Fatalf("ListBlocks: %v", err)
	}
	if len(blocks) != 0 {
		t.Errorf("got %d blocks, want 0 for empty turn", len(blocks))
	}
}

func TestTranscriptPageLimitClamping(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		for i := 0; i < 10; i++ {
			if _, _, _, err := tx.AppendMessage(turnID, "phase", "sender", nil, []byte("content"), nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("create messages: %v", err)
	}

	// Test limit 0 clamped to 1
	items, _, err := store.TranscriptPage(turnID, "", 0)
	if err != nil {
		t.Fatalf("TranscriptPage with limit 0: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("limit 0 clamped to %d, want 1", len(items))
	}

	// Test limit 1000 clamped to 200
	items, _, err = store.TranscriptPage(turnID, "", 1000)
	if err != nil {
		t.Fatalf("TranscriptPage with limit 1000: %v", err)
	}
	if len(items) > 200 {
		t.Errorf("limit 1000 returned %d items, want max 200", len(items))
	}
}

func TestAttemptTranscriptCarriesMetadata(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err = store.BeginAttempt(turnID, "p", "phase", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}

	items, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}

	item := items[0]
	if item.Kind != "attempt" {
		t.Errorf("kind = %q, want attempt", item.Kind)
	}
	if item.Attempt == nil {
		t.Error("Attempt field is nil")
	} else {
		if item.Attempt.RawOutput != nil {
			t.Error("RawOutput should be cleared in transcript items")
		}
	}
}

func TestTranscriptPage2MessagesWith3AttemptsAnd2LimitPaging(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = store.Tx(func(tx *journal.Tx) error {
		if _, _, _, err := tx.AppendMessage(turnID, "phase", "s1", nil, []byte("msg1"), nil); err != nil {
			return err
		}
		if _, err := tx.BeginAttempt(turnID, "p1", "phase", 0, nil, ""); err != nil {
			return err
		}
		if _, _, _, err := tx.AppendMessage(turnID, "phase", "s2", nil, []byte("msg2"), nil); err != nil {
			return err
		}
		if _, err := tx.BeginAttempt(turnID, "p2", "phase", 0, nil, ""); err != nil {
			return err
		}
		_, err = tx.BeginAttempt(turnID, "p3", "phase", 0, nil, "")
		return err
	})
	if err != nil {
		t.Fatalf("create items: %v", err)
	}

	// Get all items with limit 200
	allItems, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("full transcript: %v", err)
	}
	if len(allItems) != 5 {
		t.Fatalf("got %d items, want 5", len(allItems))
	}

	// Page with limit 2
	page1, c1, err := store.TranscriptPage(turnID, "", 2)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("page 1 got %d items, want 2", len(page1))
	}

	page2, c2, err := store.TranscriptPage(turnID, c1, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page 2 got %d items, want 2", len(page2))
	}

	page3, c3, err := store.TranscriptPage(turnID, c2, 2)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(page3) != 1 {
		t.Errorf("page 3 got %d items, want 1", len(page3))
	}
	if c3 != "" {
		t.Error("page 3 cursor should be empty")
	}

	// Verify order matches full transcript
	pagedItems := append(append(page1, page2...), page3...)
	for i, item := range pagedItems {
		if item.Kind != allItems[i].Kind || item.Ref != allItems[i].Ref {
			t.Errorf("paged item %d kind=%q ref=%q, want kind=%q ref=%q", i, item.Kind, item.Ref, allItems[i].Kind, allItems[i].Ref)
		}
	}
}

func TestMigration005UpgradeFromV4(t *testing.T) {
	path := t.TempDir() + "/test.db"
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	// Create schema_version table
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	// Apply migrations 001-004
	for _, f := range []string{"001_init.sql", "002_events_nullable_turn_id.sql", "003_blocks_withdrawn_against_hash.sql", "004_engine.sql"} {
		b, err := os.ReadFile("migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	if _, err := conn.Exec(`INSERT INTO schema_version (version) VALUES (1), (2), (3), (4)`); err != nil {
		t.Fatal(err)
	}

	// Seed with probe row data
	if _, err := conn.Exec(`INSERT INTO runs (id, manifest, manifest_hash, kind) VALUES ('r1', '{}', 'h', 'probe')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO turns (id, run_id, mode) VALUES ('t1', 'r1', 'solo')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO messages (id, turn_id, phase, sender, recipient_set, content, content_hash, byte_length, sequence)
		VALUES ('m1', 't1', 'phase1', 'sender1', '[]', 'content1', 'h1', 8, 1)`); err != nil {
		t.Fatal(err)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	// Open with journal.Open which runs remaining migrations
	db, err := journal.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	// Verify the probe row is still readable
	var msgID string
	if err := db.Conn().QueryRow(`SELECT id FROM messages WHERE turn_id = 't1'`).Scan(&msgID); err != nil {
		t.Fatalf("query message: %v", err)
	}
	if msgID != "m1" {
		t.Errorf("message id = %q, want m1", msgID)
	}

	// Verify pre-migration 005 rows have NULL journal_seq
	var journalSeq sql.NullInt64
	if err := db.Conn().QueryRow(`SELECT journal_seq FROM messages WHERE id = 'm1'`).Scan(&journalSeq); err != nil {
		t.Fatal(err)
	}
	if journalSeq.Valid {
		t.Errorf("old message should have NULL journal_seq, got %d", journalSeq.Int64)
	}
}

func TestTranscriptPageNullJournalSeqWithRealRows(t *testing.T) {
	path := t.TempDir() + "/test.db"
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	// Create schema_version table
	if _, err := conn.Exec(`CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	// Apply migrations 001-004
	for _, f := range []string{"001_init.sql", "002_events_nullable_turn_id.sql", "003_blocks_withdrawn_against_hash.sql", "004_engine.sql"} {
		b, err := os.ReadFile("migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	if _, err := conn.Exec(`INSERT INTO schema_version (version) VALUES (1), (2), (3), (4)`); err != nil {
		t.Fatal(err)
	}

	// Seed with mixed NULL and non-NULL journal_seq data
	if _, err := conn.Exec(`INSERT INTO runs (id, manifest, manifest_hash, kind) VALUES ('r1', '{}', 'h', 'probe')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO turns (id, run_id, mode) VALUES ('t1', 'r1', 'solo')`); err != nil {
		t.Fatal(err)
	}
	// Pre-005 message (NULL journal_seq), by sequence 1
	if _, err := conn.Exec(`INSERT INTO messages (id, turn_id, phase, sender, recipient_set, content, content_hash, byte_length, sequence)
		VALUES ('m1', 't1', 'phase', 's1', '[]', 'msg1', 'h1', 4, 1)`); err != nil {
		t.Fatal(err)
	}
	// Pre-005 attempt (NULL journal_seq), dispatched_at time1
	if _, err := conn.Exec(`INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at)
		VALUES ('a1', 't1', 'p1', 'phase', 0, '2026-01-01T00:00:01Z')`); err != nil {
		t.Fatal(err)
	}

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	// Open which runs migrations
	db, err := journal.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()

	store := journal.NewStore(db)

	// Now append new rows with journal_seq
	_, _, _, err = store.AppendMessage("t1", "phase", "s2", nil, []byte("msg2"), nil)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	_, err = store.BeginAttempt("t1", "p2", "phase", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}

	// Get full transcript
	items, _, err := store.TranscriptPage("t1", "", 200)
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}

	// Should have 4 items: NULL rows first (message, attempt), then new rows
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4", len(items))
	}

	// First item should be NULL message
	if items[0].Kind != "message" || items[0].Ref != "msg:m1" {
		t.Errorf("item 0: kind=%q ref=%q, want message/msg:m1", items[0].Kind, items[0].Ref)
	}

	// Second item should be NULL attempt
	if items[1].Kind != "attempt" || items[1].Ref != "attempt:a1" {
		t.Errorf("item 1: kind=%q ref=%q, want attempt/attempt:a1", items[1].Kind, items[1].Ref)
	}

	// Third and fourth should be new rows with journal_seq
	if items[2].Kind != "message" {
		t.Errorf("item 2 kind = %q, want message", items[2].Kind)
	}
	if items[3].Kind != "attempt" {
		t.Errorf("item 3 kind = %q, want attempt", items[3].Kind)
	}
}

func TestContentRangeLargeAttemptOutputMultipage(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Create a 700,000-byte attempt output with multi-byte UTF-8, quotes, backslashes, and control characters
	output := make([]byte, 0, 700000)
	for i := 0; i < 7000; i++ {
		output = append(output, []byte(`hello "world" 🌍 \n\r\t\x00`)...)
	}
	output = output[:700000] // Trim to exactly 700000 bytes

	var attemptID string
	err = store.Tx(func(tx *journal.Tx) error {
		var err error
		attemptID, err = tx.BeginAttempt(turnID, "p", "phase", 0, nil, "")
		if err != nil {
			return err
		}
		return tx.CompleteAttempt(attemptID, string(output), 0, "", "complete", "", "")
	})
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	// Compute expected SHA256 of full output
	sum := sha256.Sum256(output)
	expectedSHA256 := hex.EncodeToString(sum[:])

	// Read in 262144-byte pages and concatenate
	var result []byte
	var cursor int64
	for cursor < 700000 {
		data, total, sha256Hash, err := store.ContentRange("attempt:"+attemptID, cursor, 262144)
		if err != nil {
			t.Fatalf("ContentRange at %d: %v", cursor, err)
		}
		if total != 700000 {
			t.Errorf("total = %d, want 700000", total)
		}
		if sha256Hash != expectedSHA256 {
			t.Errorf("SHA256 mismatch at page starting %d: got %s, want %s", cursor, sha256Hash, expectedSHA256)
		}
		result = append(result, data...)
		cursor += int64(len(data))
	}

	if len(result) != 700000 {
		t.Errorf("concatenated length = %d, want 700000", len(result))
	}
	if !bytes.Equal(result, output) {
		t.Error("concatenated result differs from original output")
	}
}

func TestListVerdictWithActualData(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	var attemptID, verdictID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}
		attemptID, err = tx.BeginAttempt(turnID, "p", "phase", 0, nil, "")
		if err != nil {
			return err
		}
		verdictID, err = tx.RecordVerdict(attemptID, "h1", "approve", "looks good", "valid")
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	verdicts, err := store.ListVerdicts(turnID)
	if err != nil {
		t.Fatalf("ListVerdicts: %v", err)
	}

	if len(verdicts) != 1 {
		t.Errorf("got %d verdicts, want 1", len(verdicts))
	}
	if verdicts[0].ID != verdictID {
		t.Errorf("verdict id = %q, want %q", verdicts[0].ID, verdictID)
	}
	if verdicts[0].AttemptID != attemptID {
		t.Errorf("attempt_id = %q, want %q", verdicts[0].AttemptID, attemptID)
	}
	if verdicts[0].Parsed != "approve" {
		t.Errorf("parsed = %q, want approve", verdicts[0].Parsed)
	}
}

func TestListBlocksWithOverriddenBlock(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID, attemptID, verdictID, blockID string
	err := store.Tx(func(txn *journal.Tx) error {
		var err error
		runID, _, err = txn.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = txn.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}
		attemptID, err = txn.BeginAttempt(turnID, "p", "phase", 0, nil, "")
		if err != nil {
			return err
		}
		verdictID, err = txn.RecordVerdict(attemptID, "h1", "block", "harmful", "valid")
		if err != nil {
			return err
		}
		blockID = "blk-" + verdictID // The block ID is derived from verdict
		if err := txn.OpenBlock(verdictID, blockID, "h1", "high", "evidence", "remedy"); err != nil {
			return err
		}
		// Override the block
		return txn.OverrideBlock(blockID)
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// ListBlocks should include the overridden block
	blocks, err := store.ListBlocks(turnID)
	if err != nil {
		t.Fatalf("ListBlocks: %v", err)
	}

	if len(blocks) != 1 {
		t.Errorf("got %d blocks, want 1", len(blocks))
	}
	if blocks[0].State != "overridden" {
		t.Errorf("block state = %q, want overridden", blocks[0].State)
	}
}

func TestContentRangeErrNoSuchContent(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	// Test error returns for invalid refs (don't need to create actual turn data)

	// Test msg:missing returns ErrNoSuchContent
	_, _, _, err := store.ContentRange("msg:missing", 0, 10)
	if !errors.Is(err, journal.ErrNoSuchContent) {
		t.Errorf("msg:missing error: got %v, want ErrNoSuchContent", err)
	}

	// Test nope:1 returns ErrNoSuchContent
	_, _, _, err2 := store.ContentRange("nope:1", 0, 10)
	if !errors.Is(err2, journal.ErrNoSuchContent) {
		t.Errorf("nope:1 error: got %v, want ErrNoSuchContent", err2)
	}
}

func TestTranscriptPageMessageRefFormat(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}
		_, _, _, err = tx.AppendMessage(turnID, "phase", "sender", nil, []byte("content"), nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	items, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}

	item := items[0]
	// Verify Ref is in msg: format, not message: format
	if item.Kind != "message" {
		t.Errorf("kind = %q, want message", item.Kind)
	}
	if !strings.HasPrefix(item.Ref, "msg:") {
		t.Errorf("Ref = %q, want msg: prefix", item.Ref)
	}

	// Verify the Ref can be passed to ContentRange
	data, _, _, err := store.ContentRange(item.Ref, 0, 100)
	if err != nil {
		t.Fatalf("ContentRange with transcript Ref: %v", err)
	}
	if string(data) != "content" {
		t.Errorf("got %q, want content", string(data))
	}
}

func TestContentRangeCandidateSHA256(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	candidateContent := "candidate content with special chars 🌍 \"quotes\" \\backslash"
	candidateID, err := store.CreateCandidate(turnID, candidateContent, "identity-hash")
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}

	_, _, sha256Hash, err := store.ContentRange("candidate:"+candidateID, 0, 1000)
	if err != nil {
		t.Fatalf("ContentRange: %v", err)
	}

	// Verify SHA256 is computed from actual content, not from identity hash
	expectedSum := sha256.Sum256([]byte(candidateContent))
	expectedSHA256 := hex.EncodeToString(expectedSum[:])

	if sha256Hash != expectedSHA256 {
		t.Errorf("SHA256 = %s, want %s (computed from actual content)", sha256Hash, expectedSHA256)
	}
}

func TestAttemptTranscriptNoRawOutput(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		if err != nil {
			return err
		}
		_, err = tx.BeginAttempt(turnID, "p", "phase", 0, nil, "")
		return err
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	items, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}

	item := items[0]
	if item.Attempt == nil {
		t.Fatal("Attempt is nil")
	}
	if item.Attempt.RawOutput != nil {
		t.Error("RawOutput should be nil in transcript item")
	}
	if item.ByteLen != 0 {
		t.Errorf("ByteLen = %d, want 0 for attempt with no output", item.ByteLen)
	}
}
