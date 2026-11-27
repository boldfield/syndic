package journal_test

import (
	"errors"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func mcpInput(attempt string) journal.MCPCallInput {
	return journal.MCPCallInput{
		AttemptID: attempt, Server: "docs", Tool: "search", Args: []byte(`{"q":"x"}`),
		RefereeAllowed: true, RefereeReason: "reads docs", ServerIdentity: "id1", PinSHA256: "pin1", Revision: "2025-06-18",
	}
}

func TestMCPCallLifecycle(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	err := store.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordMCPCall(mcpInput(f.author))
		if err != nil {
			return err
		}
		if c.State != journal.MCPStateSent || c.Participant != "author" || c.TurnID != f.turn {
			t.Errorf("unexpected call %+v", c)
		}
		got, err := tx.GetMCPCall(c.ID)
		if err != nil || got.State != journal.MCPStateSent || got.Revision != "2025-06-18" {
			t.Errorf("get = %+v, %v", got, err)
		}
		done, err := tx.RecordMCPOutcome(c.ID, journal.MCPStateResult, []byte("hello"), "")
		if err != nil {
			return err
		}
		if done.State != journal.MCPStateResult || done.ResultByteLen != 5 || done.ResultSHA256 == "" {
			t.Errorf("outcome = %+v", done)
		}
		if _, err := tx.RecordMCPOutcome(c.ID, journal.MCPStateAbandoned, nil, "cancelled"); !errors.Is(err, journal.ErrMCPOutcomeRecorded) {
			t.Errorf("second outcome err = %v", err)
		}
		calls, err := tx.MCPCallsByTurn(f.turn)
		if err != nil || len(calls) != 1 || calls[0].State != journal.MCPStateResult {
			t.Errorf("by turn = %+v, %v", calls, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMCPAbandonedAndValidation(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	err := store.Tx(func(tx *journal.Tx) error {
		bad := mcpInput(f.author)
		bad.PinSHA256 = ""
		if _, err := tx.RecordMCPCall(bad); err == nil {
			t.Error("call without pin accepted")
		}
		c, err := tx.RecordMCPCall(mcpInput(f.author))
		if err != nil {
			return err
		}
		if _, err := tx.RecordMCPOutcome(c.ID, journal.MCPStateAbandoned, nil, ""); err == nil {
			t.Error("abandoned without cause accepted")
		}
		if _, err := tx.RecordMCPOutcome(c.ID, journal.MCPStateError, []byte("boom"), "cancelled"); err == nil {
			t.Error("error with cause accepted")
		}
		a, err := tx.RecordMCPOutcome(c.ID, journal.MCPStateAbandoned, nil, "turn_deadline")
		if err != nil || a.State != journal.MCPStateAbandoned || a.Cause != "turn_deadline" || a.ResultSHA256 != "" {
			t.Errorf("abandoned = %+v, %v", a, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMCPPinFileAndProviderNames(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	err := store.Tx(func(tx *journal.Tx) error {
		if h, err := tx.TurnMCPPinFile(f.turn); err != nil || h != "" {
			t.Errorf("initial pin = %q, %v", h, err)
		}
		if err := tx.SetTurnMCPPinFile(f.turn, "aaa"); err != nil {
			return err
		}
		if err := tx.SetTurnMCPPinFile(f.turn, "aaa"); err != nil {
			t.Errorf("same hash again: %v", err)
		}
		if err := tx.SetTurnMCPPinFile(f.turn, "bbb"); err == nil {
			t.Error("different pin hash accepted")
		}
		names := []journal.MCPProviderName{
			{ProviderName: "mcp_0_search", Server: "docs", Tool: "search"},
			{ProviderName: "mcp_1_get_page", Server: "docs", Tool: "get.page"},
		}
		if err := tx.RecordMCPProviderNames(f.turn, "author", names); err != nil {
			return err
		}
		if err := tx.RecordMCPProviderNames(f.turn, "author", names); err == nil {
			t.Error("second mapping accepted")
		}
		if err := tx.RecordMCPProviderNames(f.turn, "r1", names[:1]); err != nil {
			return err
		}
		if err := tx.RecordMCPProviderNames(f.turn, "r2", nil); err == nil {
			t.Error("empty mapping accepted")
		}
		got, err := tx.MCPProviderNames(f.turn, "author")
		if err != nil || len(got) != 2 || got[1].Index != 1 || got[1].Tool != "get.page" {
			t.Errorf("names = %+v, %v", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Append-only.
	if _, err := db.Conn().Exec(`UPDATE mcp_provider_names SET tool = 'x'`); err == nil {
		t.Error("provider names updatable")
	}
	if _, err := db.Conn().Exec(`UPDATE turns SET mcp_pin_file_sha256 = 'zzz' WHERE id = ?`, f.turn); err == nil {
		t.Error("pin hash replaceable")
	}
}
