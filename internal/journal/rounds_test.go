package journal_test

import (
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

type roundFixture struct {
	db     *journal.DB
	store  *journal.Store
	turnID string
}

func newRoundFixture(t *testing.T) *roundFixture {
	t.Helper()
	db, cleanup := openTestDB(t)
	t.Cleanup(cleanup)
	setupTestData(t, db.Conn(), "run-1", "turn-1")
	store := journal.NewStore(db)
	return &roundFixture{db: db, store: store, turnID: "turn-1"}
}

func TestGetRoundInfo(t *testing.T) {
	f := newRoundFixture(t)
	info, err := f.store.GetRoundInfo(f.turnID)
	if err != nil {
		t.Fatalf("GetRoundInfo: %v", err)
	}
	if info == nil {
		t.Fatal("GetRoundInfo returned nil")
	}
	if info.CurrentRound != 0 {
		t.Fatalf("expected CurrentRound=0, got %d", info.CurrentRound)
	}
	if info.MaxRoundsCeiling != 5 {
		t.Fatalf("expected MaxRoundsCeiling=5, got %d", info.MaxRoundsCeiling)
	}
}

func TestCanAdvanceRound(t *testing.T) {
	f := newRoundFixture(t)
	can, err := f.store.CanAdvanceRound(f.turnID)
	if err != nil {
		t.Fatalf("CanAdvanceRound: %v", err)
	}
	if can {
		t.Fatal("expected cannot advance round when max_rounds_in_effect is 0")
	}
}

func TestLatestCandidate(t *testing.T) {
	f := newRoundFixture(t)
	candidateID1, _, err := f.store.LatestCandidate(f.turnID)
	if err != nil {
		t.Fatalf("LatestCandidate first call: %v", err)
	}
	if candidateID1 != "" {
		t.Fatalf("expected empty candidate, got %s", candidateID1)
	}

	cid, err := f.store.CreateCandidate(f.turnID, "c1", "H1")
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}

	candidateID2, round2, err := f.store.LatestCandidate(f.turnID)
	if err != nil {
		t.Fatalf("LatestCandidate second call: %v", err)
	}
	if candidateID2 != cid {
		t.Fatalf("expected %s, got %s", cid, candidateID2)
	}
	if round2 != 0 {
		t.Fatalf("expected round 0, got %d", round2)
	}
}

func TestCandidateForRound(t *testing.T) {
	f := newRoundFixture(t)
	cid, err := f.store.CreateCandidate(f.turnID, "c1", "H1")
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}

	found, err := f.store.CandidateForRound(f.turnID, 0)
	if err != nil {
		t.Fatalf("CandidateForRound: %v", err)
	}
	if found != cid {
		t.Fatalf("expected %s, got %s", cid, found)
	}

	notFound, err := f.store.CandidateForRound(f.turnID, 1)
	if err != nil {
		t.Fatalf("CandidateForRound round 1: %v", err)
	}
	if notFound != "" {
		t.Fatalf("expected empty for round 1, got %s", notFound)
	}
}

func TestBlocksForRound(t *testing.T) {
	f := newRoundFixture(t)
	attemptID, err := f.store.BeginAttempt(f.turnID, "reviewer", "review", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}

	cid, err := f.store.CreateCandidate(f.turnID, "c1", "H1")
	if err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}

	vid, err := f.store.RecordVerdict(attemptID, "H1", "block", "bad", "valid")
	if err != nil {
		t.Fatalf("RecordVerdict: %v", err)
	}

	if err := f.store.OpenBlock(vid, "b1", "H1", "high", "ev", "fix"); err != nil {
		t.Fatalf("OpenBlock: %v", err)
	}

	blocks, err := f.store.BlocksForRound(f.turnID, 0)
	if err != nil {
		t.Fatalf("BlocksForRound: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].ID != "b1" {
		t.Fatalf("expected b1, got %s", blocks[0].ID)
	}

	_ = cid
}
