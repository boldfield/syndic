package journal_test

import (
	"errors"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// stepTurn is buildToolsTurn with the turn at the epoch its attempts were
// begun at, so they are current.
func stepTurn(t *testing.T, store *journal.Store) toolsFixture {
	t.Helper()
	f := buildToolsTurn(t, store)
	err := store.Tx(func(tx *journal.Tx) error {
		turn, err := tx.GetTurn(f.turn)
		for err == nil && turn.Epoch < 1 {
			if _, err = tx.BumpEpoch(f.turn); err == nil {
				turn, err = tx.GetTurn(f.turn)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func acceptStep(t *testing.T, store *journal.Store, attemptID, status string) {
	t.Helper()
	err := store.Tx(func(tx *journal.Tx) error {
		accepted, err := tx.AcceptResult(attemptID, 1, "", journal.AttemptOutput{RawScrubbed: "{}", Status: status, TransportStatus: "ok"})
		if err == nil && !accepted {
			t.Fatal("result quarantined")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func recordStep(store *journal.Store, attemptID string, calls ...journal.StepCall) ([]journal.ToolCall, error) {
	var out []journal.ToolCall
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		out, err = tx.RecordToolStep(attemptID, calls)
		return err
	})
	return out, err
}

var stepCalls = []journal.StepCall{
	{ProviderCallID: "p0", Name: "read_file", Class: "read", Args: []byte(`{"path":"a"}`)},
	{ProviderCallID: "p1", Name: "invented", Class: journal.ToolClassNone, Args: []byte(`not json`)},
}

// The calls of an accepted tool step are journaled in order, once.
func TestRecordToolStep(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := stepTurn(t, store)
	acceptStep(t, store, f.author, "tool_calls")
	calls, err := recordStep(store, f.author, stepCalls...)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ToolCallsByTurn(f.turn, "author")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || len(got) != 2 {
		t.Fatalf("recorded %d, journaled %d", len(calls), len(got))
	}
	for i, c := range got {
		if c.ID != calls[i].ID || c.CallIndex != i || c.ProviderCallID != stepCalls[i].ProviderCallID || c.Class != stepCalls[i].Class || c.Status != journal.ToolCallAccepted {
			t.Fatalf("call %d = %+v", i, c)
		}
	}
	if kinds := eventKinds(t, store, 0); countKind(kinds, "tool_call_accepted") != 2 {
		t.Fatalf("events %v", kinds)
	}
	if _, err := recordStep(store, f.author, stepCalls...); !errors.Is(err, journal.ErrToolStepRecorded) {
		t.Fatalf("second record: %v", err)
	}
	if _, err := recordStep(store, f.author); err == nil {
		t.Fatal("empty step recorded")
	}
}

// Only an accepted tool step is recorded: a pending, quarantined, gate-failed
// or non-tool result is not, and nothing of it is journaled.
func TestRecordToolStepRefusesUnacceptedStep(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, store *journal.Store, attemptID, turnID string)
	}{
		{"pending", func(*testing.T, *journal.Store, string, string) {}},
		{"quarantined", func(t *testing.T, store *journal.Store, attemptID, _ string) {
			if err := store.Tx(func(tx *journal.Tx) error {
				accepted, err := tx.AcceptResult(attemptID, 0, "", journal.AttemptOutput{RawScrubbed: "{}", Status: "tool_calls", TransportStatus: "ok"})
				if err == nil && accepted {
					t.Fatal("stale result accepted")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"gate failed", func(t *testing.T, store *journal.Store, attemptID, _ string) {
			acceptStep(t, store, attemptID, "tool_calls")
			if err := store.Tx(func(tx *journal.Tx) error { return tx.SetAttemptGate(attemptID, "identity mismatch") }); err != nil {
				t.Fatal(err)
			}
		}},
		{"complete", func(t *testing.T, store *journal.Store, attemptID, _ string) {
			acceptStep(t, store, attemptID, "complete")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := openTestDB(t)
			defer cleanup()
			store := journal.NewStore(db)
			f := stepTurn(t, store)
			tc.prepare(t, store, f.author, f.turn)
			if _, err := recordStep(store, f.author, stepCalls...); !errors.Is(err, journal.ErrNotToolStep) {
				t.Fatalf("err = %v", err)
			}
			if got, err := store.ToolCallsByTurn(f.turn, ""); err != nil || len(got) != 0 {
				t.Fatalf("journaled %d calls, %v", len(got), err)
			}
		})
	}
}

// ToolStepLive is true while the turn is open at the attempt's epoch, and
// names why not after an epoch bump or the turn's end.
func TestToolStepLive(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := stepTurn(t, store)
	live := func() (bool, string) {
		t.Helper()
		var ok bool
		var reason string
		if err := store.Tx(func(tx *journal.Tx) error {
			var err error
			ok, reason, err = tx.ToolStepLive(f.author)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return ok, reason
	}
	if ok, reason := live(); !ok || reason != "" {
		t.Fatalf("open turn: %t %q", ok, reason)
	}
	if err := store.Tx(func(tx *journal.Tx) error { _, err := tx.BumpEpoch(f.turn); return err }); err != nil {
		t.Fatal(err)
	}
	if ok, reason := live(); ok || reason != journal.StepEndedEpoch {
		t.Fatalf("after epoch bump: %t %q", ok, reason)
	}
	if err := store.Tx(func(tx *journal.Tx) error { return tx.EndTurn(f.turn, "cancelled", "cancelled") }); err != nil {
		t.Fatal(err)
	}
	if ok, reason := live(); ok || reason != journal.StepEndedTurn {
		t.Fatalf("after cancel: %t %q", ok, reason)
	}
	if err := store.Tx(func(tx *journal.Tx) error { _, _, err := tx.ToolStepLive("nope"); return err }); err == nil {
		t.Fatal("unknown attempt live")
	}
}

func countKind(kinds []string, kind string) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}
