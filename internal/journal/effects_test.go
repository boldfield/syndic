package journal_test

import (
	"errors"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func ef(state string, acked bool) journal.Effect {
	e := journal.Effect{State: state}
	if acked {
		e.AckState = state
	}
	return e
}

func TestDeriveEffectSetState(t *testing.T) {
	P, D := journal.EffectPending, journal.EffectDone
	cases := []struct {
		name      string
		effects   []journal.Effect
		discarded bool
		want      string
		eligible  int
	}{
		{"1 in flight", []journal.Effect{ef(D, false), ef(journal.EffectDispatching, false), ef(P, false)}, false, journal.EffectSetInFlight, -1},
		{"1 in flight beats discarded", []journal.Effect{ef(journal.EffectDispatching, false)}, true, journal.EffectSetInFlight, -1},
		{"1 in flight beats applied-looking", []journal.Effect{ef(D, false), ef(journal.EffectDispatching, false)}, false, journal.EffectSetInFlight, -1},
		{"2 discarded appliable", []journal.Effect{ef(P, false)}, true, journal.EffectSetDiscarded, -1},
		{"2 discarded stopped", []journal.Effect{ef(D, false), ef(journal.EffectFailed, false)}, true, journal.EffectSetDiscarded, -1},
		{"3 applied", []journal.Effect{ef(D, false), ef(D, false)}, false, journal.EffectSetApplied, -1},
		{"4 stopped failed", []journal.Effect{ef(D, false), ef(journal.EffectFailed, false), ef(P, false)}, false, journal.EffectSetStopped, -1},
		{"4 stopped uncertain", []journal.Effect{ef(journal.EffectUncertain, false), ef(P, false)}, false, journal.EffectSetStopped, -1},
		{"4 stopped skipped is eligible", []journal.Effect{ef(D, false), ef(journal.EffectSkipped, false), ef(P, false)}, false, journal.EffectSetStopped, 1},
		{"5 closed after acked skipped", []journal.Effect{ef(D, false), ef(journal.EffectSkipped, true)}, false, journal.EffectSetClosed, -1},
		{"5 closed after acked failed", []journal.Effect{ef(journal.EffectFailed, true), ef(D, false)}, false, journal.EffectSetClosed, -1},
		{"6 appliable", []journal.Effect{ef(P, false), ef(P, false)}, false, journal.EffectSetAppliable, 0},
		{"7 resumable after done", []journal.Effect{ef(D, false), ef(P, false)}, false, journal.EffectSetResumable, 1},
		{"7 resumable after ack", []journal.Effect{ef(journal.EffectUncertain, true), ef(P, false)}, false, journal.EffectSetResumable, 1},
		{"7 resumable acked skipped first", []journal.Effect{ef(journal.EffectSkipped, true), ef(P, false), ef(P, false)}, false, journal.EffectSetResumable, 1},
	}
	for _, c := range cases {
		got, el := journal.DeriveEffectSetState(c.effects, c.discarded)
		if got != c.want || el != c.eligible {
			t.Errorf("%s: got %q eligible %d, want %q eligible %d", c.name, got, el, c.want, c.eligible)
		}
	}
	for _, c := range cases {
		got, _ := journal.DeriveEffectSetState(c.effects, c.discarded)
		if got == journal.EffectSetInFlight && journal.EffectSetFinal(got) {
			t.Errorf("%s: in flight is final", c.name)
		}
	}
}

func effectInputs(n int) []journal.EffectInput {
	var out []journal.EffectInput
	for i := 0; i < n; i++ {
		out = append(out, journal.EffectInput{Server: "docs", ServerIdentity: "id1", Tool: "write", PinSHA256: "pin1", Args: []byte(`{"n":1}`)})
	}
	return out
}

func TestEffectLifecycle(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	err := store.Tx(func(tx *journal.Tx) error {
		set, err := tx.CreateEffectSet(f.turn, []byte(`["canary"]`), effectInputs(3))
		if err != nil {
			return err
		}
		if string(set.Canary) != `["canary"]` || set.State() != journal.EffectSetAppliable || len(set.Effects) != 3 {
			t.Fatalf("set = %+v", set)
		}
		if _, err := tx.CreateEffectSet(f.turn, nil, effectInputs(1)); err == nil {
			t.Error("second set accepted")
		}
		e0, e1, e2 := set.Effects[0], set.Effects[1], set.Effects[2]
		if err := tx.DispatchEffect(e1.ID); !errors.Is(err, journal.ErrEffectNotEligible) {
			t.Errorf("out of order dispatch err = %v", err)
		}
		if err := tx.DispatchEffect(e0.ID); err != nil {
			return err
		}
		if err := tx.DispatchEffect(e0.ID); !errors.Is(err, journal.ErrEffectNotEligible) {
			t.Errorf("second dispatch err = %v", err)
		}
		if s, _ := tx.EffectSet(f.turn); s.State() != journal.EffectSetInFlight {
			t.Errorf("state = %s", s.State())
		}
		if err := tx.DiscardEffectSet(f.turn); err == nil {
			t.Error("discard while in flight accepted")
		}
		if _, err := tx.RecordEffectOutcome(e0.ID, journal.EffectDone, []byte("ok")); err != nil {
			return err
		}
		if _, err := tx.RecordEffectOutcome(e0.ID, journal.EffectFailed, []byte("no")); err == nil {
			t.Error("second outcome accepted")
		}
		// skipped, rechecked, then dispatched
		if err := tx.MarkEffectSkipped(e1.ID, "pin changed"); err != nil {
			return err
		}
		if err := tx.MarkEffectSkipped(e1.ID, "identity changed"); err != nil {
			return err
		}
		if got, _ := tx.GetEffect(e1.ID); got.SkippedRule != "identity changed" {
			t.Errorf("rule = %q", got.SkippedRule)
		}
		if s, _ := tx.EffectSet(f.turn); s.State() != journal.EffectSetStopped {
			t.Errorf("state = %s", s.State())
		}
		if err := tx.DispatchEffect(e1.ID); err != nil {
			return err
		}
		if _, err := tx.RecordEffectOutcome(e1.ID, journal.EffectUncertain, nil); err != nil {
			return err
		}
		if err := tx.DispatchEffect(e2.ID); !errors.Is(err, journal.ErrEffectNotEligible) {
			t.Errorf("dispatch past uncertain err = %v", err)
		}
		if err := tx.AcknowledgeEffect(e1.ID, journal.EffectFailed); err == nil {
			t.Error("ack with wrong state accepted")
		}
		if err := tx.AcknowledgeEffect(e1.ID, journal.EffectUncertain); err != nil {
			return err
		}
		if err := tx.AcknowledgeEffect(e1.ID, journal.EffectUncertain); err == nil {
			t.Error("second ack accepted")
		}
		if s, _ := tx.EffectSet(f.turn); s.State() != journal.EffectSetResumable {
			t.Errorf("state = %s", s.State())
		}
		if err := tx.DiscardEffectSet(f.turn); err != nil {
			return err
		}
		s, _ := tx.EffectSet(f.turn)
		if s.State() != journal.EffectSetDiscarded || s.Effects[0].State != journal.EffectDone {
			t.Errorf("after discard = %s %+v", s.State(), s.Effects)
		}
		if err := tx.DispatchEffect(e2.ID); !errors.Is(err, journal.ErrEffectNotEligible) {
			t.Errorf("dispatch in final set err = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEffectFinalStatesNeverRewritten(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	var id string
	err := store.Tx(func(tx *journal.Tx) error {
		set, err := tx.CreateEffectSet(f.turn, nil, effectInputs(1))
		if err != nil {
			return err
		}
		id = set.Effects[0].ID
		if err := tx.DispatchEffect(id); err != nil {
			return err
		}
		_, err = tx.RecordEffectOutcome(id, journal.EffectFailed, []byte("err"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE effects SET state = 'done'`,
		`UPDATE effects SET state = 'pending'`,
		`UPDATE effects SET reply_sha256 = NULL, reply_byte_len = NULL`,
		`UPDATE effects SET tool = 'other'`,
		`DELETE FROM effects`,
	} {
		if _, err := db.Conn().Exec(q); err == nil {
			t.Errorf("%s succeeded", q)
		}
	}
	if _, err := db.Conn().Exec(`UPDATE effects SET ack_state = 'failed', acked_at = 'x'`); err != nil {
		t.Errorf("ack update: %v", err)
	}
	if _, err := db.Conn().Exec(`UPDATE effects SET ack_state = NULL, acked_at = NULL`); err == nil {
		t.Error("ack rewritten")
	}
}
