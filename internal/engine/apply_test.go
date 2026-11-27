package engine_test

import (
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
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
