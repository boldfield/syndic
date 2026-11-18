package journal_test

import (
	"database/sql"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

type blockFixture struct {
	db        *journal.DB
	store     *journal.Store
	turnID    string
	attemptID string
}

func newBlockFixture(t *testing.T) *blockFixture {
	t.Helper()
	db, cleanup := openTestDB(t)
	t.Cleanup(cleanup)
	setupTestData(t, db.Conn(), "run-1", "turn-1")
	store := journal.NewStore(db)
	attemptID, err := store.BeginAttempt("turn-1", "reviewer", "review", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}
	return &blockFixture{db: db, store: store, turnID: "turn-1", attemptID: attemptID}
}

func (f *blockFixture) openBlock(t *testing.T, blockID, hash string) {
	t.Helper()
	vid, err := f.store.RecordVerdict(f.attemptID, hash, "block", "bad", "valid")
	if err != nil {
		t.Fatalf("RecordVerdict: %v", err)
	}
	if err := f.store.OpenBlock(vid, blockID, hash, "high", "ev", "fix"); err != nil {
		t.Fatalf("OpenBlock: %v", err)
	}
}

func (f *blockFixture) openIDs(t *testing.T) []string {
	t.Helper()
	blocks, err := f.store.OpenBlocks(f.turnID)
	if err != nil {
		t.Fatalf("OpenBlocks: %v", err)
	}
	var ids []string
	for _, b := range blocks {
		ids = append(ids, b.ID)
	}
	return ids
}

func TestBlockSurvivesRevisionAndWithdrawal(t *testing.T) {
	f := newBlockFixture(t)
	if _, err := f.store.CreateCandidate(f.turnID, "c1", "H1"); err != nil {
		t.Fatal(err)
	}
	f.openBlock(t, "b1", "H1")
	if _, err := f.store.CreateCandidate(f.turnID, "c2", "H2"); err != nil {
		t.Fatal(err)
	}
	if ids := f.openIDs(t); len(ids) != 1 || ids[0] != "b1" {
		t.Fatalf("expected b1 open after second candidate, got %v", ids)
	}

	for _, bad := range []string{"H1", ""} {
		if err := f.store.WithdrawBlock("b1", bad); err == nil {
			t.Fatalf("WithdrawBlock(%q) should error", bad)
		}
	}
	if ids := f.openIDs(t); len(ids) != 1 {
		t.Fatalf("block should stay open after rejected withdrawals, got %v", ids)
	}

	if err := f.store.WithdrawBlock("b1", "H2"); err != nil {
		t.Fatalf("WithdrawBlock H2: %v", err)
	}
	if ids := f.openIDs(t); len(ids) != 0 {
		t.Fatalf("expected no open blocks, got %v", ids)
	}

	var state string
	var against, orig sql.NullString
	if err := f.db.Conn().QueryRow(`SELECT state, withdrawn_against_hash, candidate_hash FROM blocks WHERE id = 'b1'`).Scan(&state, &against, &orig); err != nil {
		t.Fatal(err)
	}
	if state != "withdrawn" || against.String != "H2" || orig.String != "H1" {
		t.Fatalf("got state=%q against=%v orig=%v", state, against, orig)
	}

	if err := f.store.WithdrawBlock("b1", "H3"); err == nil {
		t.Fatal("WithdrawBlock on withdrawn block should error")
	}
	if err := f.store.OverrideBlock("b1"); err == nil {
		t.Fatal("OverrideBlock on withdrawn block should error")
	}
}

func TestOverrideBlock(t *testing.T) {
	f := newBlockFixture(t)
	f.openBlock(t, "b1", "H1")
	if err := f.store.OverrideBlock("b1"); err != nil {
		t.Fatalf("OverrideBlock: %v", err)
	}
	if ids := f.openIDs(t); len(ids) != 0 {
		t.Fatalf("expected no open blocks, got %v", ids)
	}
	if err := f.store.OverrideBlock("b1"); err == nil {
		t.Fatal("second OverrideBlock should error")
	}
	if err := f.store.WithdrawBlock("b1", "H2"); err == nil {
		t.Fatal("WithdrawBlock on overridden block should error")
	}
	if err := f.store.OverrideBlock("missing"); err == nil {
		t.Fatal("OverrideBlock on unknown block should error")
	}
}

func TestOpenBlockRequiresBlockVerdict(t *testing.T) {
	f := newBlockFixture(t)
	for _, parsed := range []string{"approve", ""} {
		vid, err := f.store.RecordVerdict(f.attemptID, "H1", parsed, "r", "valid")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.OpenBlock(vid, "b-"+parsed, "H1", "low", "e", "r"); err == nil {
			t.Fatalf("OpenBlock on parsed=%q verdict should error", parsed)
		}
	}
	if ids := f.openIDs(t); len(ids) != 0 {
		t.Fatalf("expected no blocks, got %v", ids)
	}
}

func TestRecordVerdictValidationStatus(t *testing.T) {
	f := newBlockFixture(t)
	for _, status := range []string{"valid", "rejected", "malformed", "revision_request"} {
		vid, err := f.store.RecordVerdict(f.attemptID, "H1", "block", "r", status)
		if err != nil {
			t.Fatalf("status %q: %v", status, err)
		}
		var got string
		if err := f.db.Conn().QueryRow(`SELECT validation_status FROM verdicts WHERE id = ?`, vid).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != status {
			t.Fatalf("stored %q, want %q", got, status)
		}
	}
	if _, err := f.store.RecordVerdict(f.attemptID, "H1", "block", "r", "bogus"); err == nil {
		t.Fatal("unknown validation status should error")
	}
	if _, err := f.store.RecordVerdict(f.attemptID, "H1", "maybe", "r", "valid"); err == nil {
		t.Fatal("unknown parsed value should error")
	}
}

func TestCreateCandidateStoresHash(t *testing.T) {
	f := newBlockFixture(t)
	id, err := f.store.CreateCandidate(f.turnID, "body", "given-hash")
	if err != nil {
		t.Fatal(err)
	}
	var content, hash string
	if err := f.db.Conn().QueryRow(`SELECT content, content_hash FROM candidates WHERE id = ?`, id).Scan(&content, &hash); err != nil {
		t.Fatal(err)
	}
	if content != "body" || hash != "given-hash" {
		t.Fatalf("got %q %q", content, hash)
	}
}
