package journal_test

import (
	"database/sql"
	"testing"
)

func TestGetBlockState(t *testing.T) {
	f := newBlockFixture(t)
	f.openBlock(t, "b1", "H1")

	state, err := f.store.GetBlockState("b1")
	if err != nil {
		t.Fatalf("GetBlockState: %v", err)
	}
	if state.ID != "b1" {
		t.Fatalf("expected b1, got %s", state.ID)
	}
	if state.State != "open" {
		t.Fatalf("expected open, got %s", state.State)
	}
}

func TestGetBlocksByState(t *testing.T) {
	f := newBlockFixture(t)
	f.openBlock(t, "b1", "H1")

	blocks, err := f.store.GetBlocksByState(f.turnID, "open")
	if err != nil {
		t.Fatalf("GetBlocksByState: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 open block, got %d", len(blocks))
	}

	closed, err := f.store.GetBlocksByState(f.turnID, "withdrawn")
	if err != nil {
		t.Fatalf("GetBlocksByState withdrawn: %v", err)
	}
	if len(closed) != 0 {
		t.Fatalf("expected 0 withdrawn blocks, got %d", len(closed))
	}
}

func TestAllBlocksForTurn(t *testing.T) {
	f := newBlockFixture(t)
	cid1, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate: %v", err)
	}
	f.openBlock(t, "b1", "H1")

	_, err = f.store.SetLatestCandidate(f.turnID, "c2", "H2", 1, cid1)
	if err != nil {
		t.Fatalf("SetLatestCandidate: %v", err)
	}

	if err := f.store.WithdrawBlock("b1", "reviewer", "H2"); err != nil {
		t.Fatalf("WithdrawBlock: %v", err)
	}

	all, err := f.store.AllBlocksForTurn(f.turnID)
	if err != nil {
		t.Fatalf("AllBlocksForTurn: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 block total, got %d", len(all))
	}
	if all[0].State != "withdrawn" {
		t.Fatalf("expected withdrawn, got %s", all[0].State)
	}
}

func TestWithdrawnBlocksAgainstHash(t *testing.T) {
	f := newBlockFixture(t)
	cid1, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate: %v", err)
	}
	f.openBlock(t, "b1", "H1")

	_, err = f.store.SetLatestCandidate(f.turnID, "c2", "H2", 1, cid1)
	if err != nil {
		t.Fatalf("SetLatestCandidate: %v", err)
	}

	if err := f.store.WithdrawBlock("b1", "reviewer", "H2"); err != nil {
		t.Fatalf("WithdrawBlock: %v", err)
	}

	withdrawn, err := f.store.WithdrawnBlocksAgainstHash(f.turnID, "H2")
	if err != nil {
		t.Fatalf("WithdrawnBlocksAgainstHash: %v", err)
	}
	if len(withdrawn) != 1 {
		t.Fatalf("expected 1 withdrawn block, got %d", len(withdrawn))
	}
	if withdrawn[0].ID != "b1" {
		t.Fatalf("expected b1, got %s", withdrawn[0].ID)
	}
	if *withdrawn[0].WithdrawnAgainstHash != "H2" {
		t.Fatalf("expected H2, got %s", *withdrawn[0].WithdrawnAgainstHash)
	}

	notWithdrawn, err := f.store.WithdrawnBlocksAgainstHash(f.turnID, "H3")
	if err != nil {
		t.Fatalf("WithdrawnBlocksAgainstHash H3: %v", err)
	}
	if len(notWithdrawn) != 0 {
		t.Fatalf("expected 0 blocks withdrawn against H3, got %d", len(notWithdrawn))
	}
}

func TestBlocksForCandidate(t *testing.T) {
	f := newBlockFixture(t)
	f.openBlock(t, "b1", "H1")

	blocks, err := f.store.BlocksForCandidate(f.turnID, "H1")
	if err != nil {
		t.Fatalf("BlocksForCandidate: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block for H1, got %d", len(blocks))
	}

	otherBlocks, err := f.store.BlocksForCandidate(f.turnID, "H2")
	if err != nil {
		t.Fatalf("BlocksForCandidate H2: %v", err)
	}
	if len(otherBlocks) != 0 {
		t.Fatalf("expected 0 blocks for H2, got %d", len(otherBlocks))
	}
}

func TestBlockStatePersistence(t *testing.T) {
	f := newBlockFixture(t)
	cid1, err := f.store.SetLatestCandidate(f.turnID, "c1", "H1", 0, "")
	if err != nil {
		t.Fatalf("SetLatestCandidate: %v", err)
	}
	f.openBlock(t, "b1", "H1")

	state1, err := f.store.GetBlockState("b1")
	if err != nil {
		t.Fatalf("GetBlockState initial: %v", err)
	}
	if state1.WithdrawnAgainstHash != nil {
		t.Fatalf("expected nil withdrawn_against_hash initially")
	}

	_, err = f.store.SetLatestCandidate(f.turnID, "c2", "H2", 1, cid1)
	if err != nil {
		t.Fatalf("SetLatestCandidate: %v", err)
	}

	if err := f.store.WithdrawBlock("b1", "reviewer", "H2"); err != nil {
		t.Fatalf("WithdrawBlock: %v", err)
	}

	state2, err := f.store.GetBlockState("b1")
	if err != nil {
		t.Fatalf("GetBlockState after withdraw: %v", err)
	}
	if state2.State != "withdrawn" {
		t.Fatalf("expected withdrawn, got %s", state2.State)
	}
	if state2.WithdrawnAgainstHash == nil || *state2.WithdrawnAgainstHash != "H2" {
		t.Fatalf("expected withdrawn_against_hash=H2")
	}

	var state string
	var against, orig sql.NullString
	if err := f.db.Conn().QueryRow(`SELECT state, withdrawn_against_hash, candidate_hash FROM blocks WHERE id = 'b1'`).Scan(&state, &against, &orig); err != nil {
		t.Fatal(err)
	}
	if state != "withdrawn" || against.String != "H2" || orig.String != "H1" {
		t.Fatalf("got state=%q against=%v orig=%v", state, against, orig)
	}
}
