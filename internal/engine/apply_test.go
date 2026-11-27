package engine_test

import (
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

func TestApplyTurnNotFound(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	_, err := h.Engine.Apply("nonexistent-turn")
	_ = wantRefusal(t, err, wire.ErrorCodeNotFound)
}

func TestDiscardTurnNotFound(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	_, err := h.Engine.Discard("nonexistent-turn")
	_ = wantRefusal(t, err, wire.ErrorCodeNotFound)
}

func TestApplyNoChangeSet(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)

	var turnID string
	err := h.Store.Tx(func(tx *journal.Tx) error {
		conversationID, err := tx.AdoptConversation(nil, "")
		if err != nil {
			return err
		}
		var id string
		id, err = tx.CreateEngineTurn(journal.TurnParams{
			SubmissionID:   "s1",
			ConversationID: conversationID,
			RunID:          h.Engine.Hello().RunID,
			Mode:           "solo",
			Preset:         "solo",
		})
		if err != nil {
			return err
		}
		turnID = id
		return tx.EndTurn(id, "approved", "done")
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	_, err = h.Engine.Apply(turnID)
	_ = wantRefusal(t, err, wire.ErrorCodeNotOpen)
}

func TestDiscardNoChangeSet(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)

	var turnID string
	err := h.Store.Tx(func(tx *journal.Tx) error {
		conversationID, err := tx.AdoptConversation(nil, "")
		if err != nil {
			return err
		}
		var id string
		id, err = tx.CreateEngineTurn(journal.TurnParams{
			SubmissionID:   "s2",
			ConversationID: conversationID,
			RunID:          h.Engine.Hello().RunID,
			Mode:           "solo",
			Preset:         "solo",
		})
		if err != nil {
			return err
		}
		turnID = id
		return tx.EndTurn(id, "approved", "done")
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	_, err = h.Engine.Discard(turnID)
	_ = wantRefusal(t, err, wire.ErrorCodeNotOpen)
}

// TestApplyBusyWhenTurnOpen tests that apply is refused busy while a turn is open.
func TestApplyBusyWhenTurnOpen(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {enginetest.Reply("hello response")},
	})

	// Submit a turn to open it
	reply, err := h.Engine.Submit(wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "hello",
		Preset:       strPtr("review"),
	})
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	// Try to apply while the turn is open; should get busy
	_, err = h.Engine.Apply(reply.TurnID)
	refusal := wantRefusal(t, err, wire.ErrorCodeBusy)
	if refusal.TurnID == nil || *refusal.TurnID != reply.TurnID {
		t.Errorf("busy error should name the open turn ID, got TurnID=%v", refusal.TurnID)
	}
	if refusal.Detail == nil || *refusal.Detail != "turn is open" {
		t.Errorf("busy error should detail 'turn is open', got %v", refusal.Detail)
	}
}

func strPtr(s string) *string { return &s }

// TestSubmitBusyWhenApplyRunning tests that submit is refused busy while an
// apply is running. This requires a blocking hook in the workspace I/O.
func TestSubmitBusyWhenApplyRunning(t *testing.T) {
	t.Skip("requires blocking hook in workspace I/O to test concurrent state")
}

// TestApplySuccessful tests a successful apply of an approved turn.
func TestApplySuccessful(t *testing.T) {
	t.Skip("requires creating a turn with an approved candidate and change set")
}

// TestApplyPartialRetrySkipsDone tests that a partial retry of an apply skips
// paths that are already done and reports conflicts by name.
func TestApplyPartialRetrySkipsDone(t *testing.T) {
	t.Skip("requires partial apply state and retry scenario")
}

// TestBaseChangedNoWrite tests that base_changed leaves the workspace
// untouched and returns paths in the error.
func TestBaseChangedNoWrite(t *testing.T) {
	t.Skip("requires base_changed scenario during apply")
}

// TestWorkspaceMissingRefusal tests that a missing workspace is refused
// properly.
func TestWorkspaceMissingRefusal(t *testing.T) {
	t.Skip("requires missing workspace scenario")
}

// TestDiscardPreservesApplied tests that discard after a partial apply keeps
// applied files and evidence, and discard of an applied/discarded set is refused.
func TestDiscardPreservesApplied(t *testing.T) {
	t.Skip("requires partial apply then discard scenario")
}

// TestRecoveryWritesNothing tests that recovery at engine start writes nothing
// to the workspace.
func TestRecoveryWritesNothing(t *testing.T) {
	t.Skip("requires unfinished apply state across restart")
}
