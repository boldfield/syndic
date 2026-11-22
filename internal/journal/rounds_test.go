package journal_test

import (
	"strings"
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
	if info.MaxRoundsCeiling != 0 {
		t.Fatalf("expected unset MaxRoundsCeiling=0, got %d", info.MaxRoundsCeiling)
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

	cid, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
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

	_, err = f.store.CreateCandidate(f.turnID, "c1", "H1")
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

}

func TestAdvanceRoundBumpsEpoch(t *testing.T) {
	f := newRoundFixture(t)

	cid1, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate round 0: %v", err)
	}

	var initialEpoch int
	if err := f.db.Conn().QueryRow(`SELECT epoch FROM turns WHERE id = ?`, f.turnID).Scan(&initialEpoch); err != nil {
		t.Fatalf("query initial epoch: %v", err)
	}

	_, err = f.store.SetLatestCandidate(f.turnID, "c2", "H2", 1, cid1)
	if err != nil {
		t.Fatalf("SetLatestCandidate round 1: %v", err)
	}

	var newEpoch int
	if err := f.db.Conn().QueryRow(`SELECT epoch FROM turns WHERE id = ?`, f.turnID).Scan(&newEpoch); err != nil {
		t.Fatalf("query new epoch: %v", err)
	}

	if newEpoch != initialEpoch+1 {
		t.Fatalf("expected epoch to bump from %d to %d, got %d", initialEpoch, initialEpoch+1, newEpoch)
	}

}

func TestBlocksPersistAcrossRounds(t *testing.T) {
	f := newRoundFixture(t)

	attemptID1, err := f.store.BeginAttempt(f.turnID, "reviewer1", "review", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt round 0: %v", err)
	}

	cid1, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate round 0: %v", err)
	}

	vid1, err := f.store.RecordVerdict(attemptID1, "H1", "block", "bad", "valid")
	if err != nil {
		t.Fatalf("RecordVerdict: %v", err)
	}

	if err := f.store.OpenBlock(vid1, "b1", "H1", "high", "ev", "fix"); err != nil {
		t.Fatalf("OpenBlock: %v", err)
	}

	blocksRound0, err := f.store.BlocksForRound(f.turnID, 0)
	if err != nil {
		t.Fatalf("BlocksForRound round 0: %v", err)
	}
	if len(blocksRound0) != 1 {
		t.Fatalf("expected 1 block in round 0, got %d", len(blocksRound0))
	}

	_, err = f.store.SetLatestCandidate(f.turnID, "c2", "H2", 1, cid1)
	if err != nil {
		t.Fatalf("SetLatestCandidate round 1: %v", err)
	}

	open, err := f.store.GetBlocksByState(f.turnID, "open")
	if err != nil {
		t.Fatalf("GetBlocksByState after advance: %v", err)
	}
	if len(open) != 1 || open[0].ID != "b1" {
		t.Fatalf("expected b1 to stay open after round advance, got %v", open)
	}
	err = f.store.Tx(func(tx *journal.Tx) error {
		ok, err := tx.AllApproved(f.turnID, nil)
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("open block must keep the turn from being approved after advance")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("AllApproved: %v", err)
	}
	if err := f.store.WithdrawBlock("b1", "reviewer1", "H2"); err != nil {
		t.Fatalf("owner withdrawal against H2: %v", err)
	}
	if open, _ = f.store.GetBlocksByState(f.turnID, "open"); len(open) != 0 {
		t.Fatalf("expected no open blocks after withdrawal, got %v", open)
	}

}

func TestApprovalsDoNotTransferAcrossRounds(t *testing.T) {
	f := newRoundFixture(t)

	attemptID1, err := f.store.BeginAttempt(f.turnID, "reviewer1", "review", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt round 0: %v", err)
	}
	cid1, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate round 0: %v", err)
	}
	if _, err := f.store.RecordVerdict(attemptID1, "H1", "approve", "good", "valid"); err != nil {
		t.Fatalf("RecordVerdict round 0 approve: %v", err)
	}

	approved, err := f.store.ApprovedParticipants(f.turnID)
	if err != nil {
		t.Fatalf("ApprovedParticipants round 0: %v", err)
	}
	if len(approved) != 1 || approved[0] != "reviewer1" {
		t.Fatalf("expected reviewer1 approving round 0, got %v", approved)
	}

	if _, err := f.store.SetLatestCandidate(f.turnID, "c2", "H2", 1, cid1); err != nil {
		t.Fatalf("SetLatestCandidate round 1: %v", err)
	}

	approved, err = f.store.ApprovedParticipants(f.turnID)
	if err != nil {
		t.Fatalf("ApprovedParticipants round 1: %v", err)
	}
	if len(approved) != 0 {
		t.Fatalf("round 0 approval transferred to round 1: %v", approved)
	}
	err = f.store.Tx(func(tx *journal.Tx) error {
		ok, err := tx.AllApproved(f.turnID, []string{"reviewer1"})
		if err != nil {
			return err
		}
		if ok {
			t.Fatal("AllApproved true for round 1 with only a round 0 approval")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("AllApproved: %v", err)
	}

	attemptID2, err := f.store.BeginAttempt(f.turnID, "reviewer1", "review", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt round 1: %v", err)
	}
	if _, err := f.store.RecordVerdict(attemptID2, "H2", "approve", "good", "valid"); err != nil {
		t.Fatalf("RecordVerdict round 1 approve: %v", err)
	}
	err = f.store.Tx(func(tx *journal.Tx) error {
		ok, err := tx.AllApproved(f.turnID, []string{"reviewer1"})
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("AllApproved false after a round 1 approval")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("AllApproved: %v", err)
	}
}

func TestLatestCandidateFollowsTurnPointer(t *testing.T) {
	f := newRoundFixture(t)

	cid1, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate: %v", err)
	}
	// A row that exists without the pointer moving to it is not current.
	err = f.store.Tx(func(tx *journal.Tx) error {
		_, err := tx.CreateCandidateWithRound(f.turnID, "c2", "H2", 1, cid1)
		return err
	})
	if err != nil {
		t.Fatalf("CreateCandidateWithRound: %v", err)
	}

	id, round, err := f.store.LatestCandidate(f.turnID)
	if err != nil {
		t.Fatalf("LatestCandidate: %v", err)
	}
	if id != cid1 || round != 0 {
		t.Fatalf("expected pointed candidate %s round 0, got %s round %d", cid1, id, round)
	}
}

func TestWithdrawBlockValidatesReviewerOwnership(t *testing.T) {
	f := newRoundFixture(t)

	attemptID, err := f.store.BeginAttempt(f.turnID, "reviewer1", "review", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}

	cid1, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate round 0: %v", err)
	}

	vid, err := f.store.RecordVerdict(attemptID, "H1", "block", "bad", "valid")
	if err != nil {
		t.Fatalf("RecordVerdict: %v", err)
	}

	if err := f.store.OpenBlock(vid, "b1", "H1", "high", "ev", "fix"); err != nil {
		t.Fatalf("OpenBlock: %v", err)
	}

	_, err = f.store.SetLatestCandidate(f.turnID, "c2", "H2", 1, cid1)
	if err != nil {
		t.Fatalf("SetLatestCandidate round 1: %v", err)
	}

	err = f.store.WithdrawBlock("b1", "reviewer2", "H2")
	if err == nil {
		t.Fatal("expected error when wrong reviewer withdraws block")
	}

	if !strings.Contains(err.Error(), "only the reviewer who opened the block") {
		t.Fatalf("expected ownership error, got: %v", err)
	}

	err = f.store.WithdrawBlock("b1", "reviewer1", "H2")
	if err != nil {
		t.Fatalf("expected withdrawal to succeed for correct reviewer: %v", err)
	}

}

func TestWithdrawBlockRejectsStaleHash(t *testing.T) {
	f := newRoundFixture(t)

	attemptID, err := f.store.BeginAttempt(f.turnID, "reviewer1", "review", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}

	_, err = f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate round 0: %v", err)
	}

	vid, err := f.store.RecordVerdict(attemptID, "H1", "block", "bad", "valid")
	if err != nil {
		t.Fatalf("RecordVerdict: %v", err)
	}

	if err := f.store.OpenBlock(vid, "b1", "H1", "high", "ev", "fix"); err != nil {
		t.Fatalf("OpenBlock: %v", err)
	}

	err = f.store.WithdrawBlock("b1", "reviewer1", "H1")
	if err == nil {
		t.Fatal("expected error when withdrawing against same hash as opened")
	}

	if !strings.Contains(err.Error(), "cannot withdraw block against the same candidate hash") {
		t.Fatalf("expected stale hash error, got: %v", err)
	}

}

func TestLateResultsAreQuarantinedAfterRoundAdvance(t *testing.T) {
	f := newRoundFixture(t)

	attemptID1, err := f.store.BeginAttempt(f.turnID, "reviewer1", "review", 0, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt round 0: %v", err)
	}

	cid1, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate round 0: %v", err)
	}

	_, err = f.store.SetLatestCandidate(f.turnID, "c2", "H2", 1, cid1)
	if err != nil {
		t.Fatalf("SetLatestCandidate round 1 (advances epoch): %v", err)
	}

	out := journal.AttemptOutput{
		RawScrubbed:     "result1",
		ScrubCount:      1,
		Identity:        "identity1",
		Status:          "complete",
		NativeFinish:    "finish1",
		TransportStatus: "ok",
	}

	// Try to accept the result from attempt1 (epoch 0) after the round advance (epoch 1)
	var accepted bool
	err = f.store.Tx(func(tx *journal.Tx) error {
		var txErr error
		accepted, txErr = tx.AcceptResult(attemptID1, 0, "", out)
		return txErr
	})
	if err != nil {
		t.Fatalf("AcceptResult: %v", err)
	}

	if accepted {
		t.Fatal("expected late result to be quarantined, but it was accepted")
	}

	var quarantined bool
	if err := f.db.Conn().QueryRow(`
		SELECT quarantined FROM attempts WHERE id = ?
	`, attemptID1).Scan(&quarantined); err != nil {
		t.Fatalf("query attempt quarantine flag: %v", err)
	}
	if !quarantined {
		t.Fatal("expected attempt to be quarantined")
	}

}
