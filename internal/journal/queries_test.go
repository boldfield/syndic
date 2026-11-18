package journal_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

	store.AppendMessage(turnID, "phase1", "sender1", nil, []byte("content1"), nil)
	store.BeginAttempt(turnID, "participant1", "phase1", 0, nil, "")
	store.AppendMessage(turnID, "phase1", "sender2", nil, []byte("content2"), nil)
	store.BeginAttempt(turnID, "participant2", "phase2", 0, nil, "")

	rows, _ := db.Conn().Query(`
		SELECT journal_seq FROM messages WHERE turn_id = ? AND journal_seq IS NOT NULL
		UNION ALL
		SELECT journal_seq FROM attempts WHERE turn_id = ? AND journal_seq IS NOT NULL
		ORDER BY journal_seq ASC
	`, turnID, turnID)
	defer rows.Close()
	seqs := []int64{}
	for rows.Next() {
		var seq int64
		rows.Scan(&seq)
		seqs = append(seqs, seq)
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

	store.Tx(func(tx *journal.Tx) error {
		tx.AppendMessage(turnID, "phase1", "sender1", nil, []byte("content"), nil)
		return nil
	})

	var before int
	db.Conn().QueryRow(`SELECT next FROM journal_counter WHERE id = 1`).Scan(&before)

	store.Tx(func(tx *journal.Tx) error {
		if _, _, _, err := tx.AppendMessage(turnID, "phase1", "sender1", nil, []byte("content"), nil); err != nil {
			return nil
		}
		return sql.ErrTxDone
	})

	var after int
	db.Conn().QueryRow(`SELECT next FROM journal_counter WHERE id = 1`).Scan(&after)

	if before != after {
		t.Errorf("counter changed on rollback: %d -> %d", before, after)
	}
}

func TestListAttempts(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	store.Tx(func(tx *journal.Tx) error {
		tx.BeginAttempt(turnID, "p2", "phase", 0, nil, "")
		time.Sleep(10 * time.Millisecond)
		tx.BeginAttempt(turnID, "p1", "phase", 0, nil, "")
		return nil
	})

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
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	candidateID, _ := store.CreateCandidate(turnID, "content", "h1")

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
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	utf8Content := "hello🌍world"
	candidateID, _ := store.CreateCandidate(turnID, utf8Content, "h1")

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
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	store.Tx(func(tx *journal.Tx) error {
		tx.AppendMessage(turnID, "phase", "s1", nil, []byte("msg1"), nil)
		tx.BeginAttempt(turnID, "p1", "phase", 0, nil, "")
		tx.AppendMessage(turnID, "phase", "s2", nil, []byte("msg2"), nil)
		tx.BeginAttempt(turnID, "p2", "phase", 0, nil, "")
		tx.BeginAttempt(turnID, "p3", "phase", 0, nil, "")
		return nil
	})

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
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	store.Tx(func(tx *journal.Tx) error {
		for i := 0; i < 5; i++ {
			tx.AppendMessage(turnID, "phase", "sender", nil, []byte("content"), nil)
		}
		return nil
	})

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
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	store.Tx(func(tx *journal.Tx) error {
		tx.AppendMessage(turnID, "phase", "s1", nil, []byte("msg1"), nil)
		tx.BeginAttempt(turnID, "p1", "phase", 0, nil, "")
		tx.AppendMessage(turnID, "phase", "s2", nil, []byte("msg2"), nil)
		tx.BeginAttempt(turnID, "p2", "phase", 0, nil, "")
		return nil
	})

	db.Conn().Exec(`UPDATE messages SET journal_seq = NULL WHERE id IN (SELECT id FROM messages LIMIT 1)`)
	db.Conn().Exec(`UPDATE attempts SET journal_seq = NULL WHERE id IN (SELECT id FROM attempts LIMIT 1)`)

	items, _, err := store.TranscriptPage(turnID, "", 200)
	if err != nil {
		t.Fatalf("TranscriptPage: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4", len(items))
	}

	page1, c1, _ := store.TranscriptPage(turnID, "", 1)
	if len(page1) != 1 {
		t.Errorf("page 1 len = %d, want 1", len(page1))
	}

	page2, _, _ := store.TranscriptPage(turnID, c1, 1)
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
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	msgID, _, _, _ := store.AppendMessage(turnID, "phase", "sender", nil, []byte("hello world"), nil)

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
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	msgID, _, _, _ := store.AppendMessage(turnID, "phase", "sender", nil, []byte("abcdefghij"), nil)

	data1, _, _, _ := store.ContentRange("msg:"+msgID, 0, 3)
	data2, _, _, _ := store.ContentRange("msg:"+msgID, 3, 3)
	data3, _, _, _ := store.ContentRange("msg:"+msgID, 6, 10)

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

	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	msgID, _, _, _ := store.AppendMessage(turnID, "phase", "sender", nil, utf8Content, nil)

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
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	msgID, _, _, _ := store.AppendMessage(turnID, "phase", "sender", nil, []byte("content"), nil)

	_, _, _, err := store.ContentRange("msg:"+msgID, -1, 10)
	if err == nil {
		t.Error("ContentRange with negative offset: want error, got nil")
	}
}

func TestContentRangeOutOfRange(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)

	var runID, turnID string
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	msgID, _, _, _ := store.AppendMessage(turnID, "phase", "sender", nil, []byte("content"), nil)

	_, _, _, err := store.ContentRange("msg:"+msgID, 100, 10)
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
	store.Tx(func(tx *journal.Tx) error {
		var err error
		runID, _, err = tx.CreateRun([]byte("{}"))
		if err != nil {
			return err
		}
		turnID, err = tx.CreateTurn(runID, "solo", nil)
		return err
	})

	store.BeginAttempt(turnID, "p", "phase", 0, nil, "")

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
