package journal_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// stepFixture is one open turn with an author attempt begun at the turn's
// current epoch, as the engine begins them, so its result can be accepted.
type stepFixture struct {
	store   *journal.Store
	turn    string
	attempt string
	epoch   int
}

func newStepFixture(t *testing.T) *stepFixture {
	t.Helper()
	db, cleanup := openTestDB(t)
	t.Cleanup(cleanup)
	f := &stepFixture{store: journal.NewStore(db)}
	err := f.store.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		if f.turn, err = tx.CreateTurn(run, "review", nil); err != nil {
			return err
		}
		turn, err := tx.GetTurn(f.turn)
		if err != nil {
			return err
		}
		f.epoch = turn.Epoch
		f.attempt, err = tx.BeginAttempt(f.turn, "author", "author", f.epoch, nil, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// accept accepts the attempt's result under status at epoch, as the engine's
// acceptance does, and reports whether it was accepted or quarantined.
func (f *stepFixture) accept(t *testing.T, epoch int, status string) bool {
	t.Helper()
	var accepted bool
	err := f.store.Tx(func(tx *journal.Tx) error {
		var err error
		accepted, err = tx.AcceptResult(f.attempt, epoch, "", journal.AttemptOutput{RawScrubbed: "{}", Status: status, TransportStatus: "ok"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func (f *stepFixture) record(calls ...journal.StepCall) ([]journal.ToolCall, error) {
	var out []journal.ToolCall
	err := f.store.Tx(func(tx *journal.Tx) error {
		var err error
		out, err = tx.RecordToolStep(f.attempt, calls)
		return err
	})
	return out, err
}

func (f *stepFixture) journaled(t *testing.T) []journal.ToolCall {
	t.Helper()
	calls, err := f.store.ToolCallsByTurn(f.turn, "")
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func (f *stepFixture) live(t *testing.T) (bool, string) {
	t.Helper()
	var ok bool
	var reason string
	if err := f.store.Tx(func(tx *journal.Tx) error {
		var err error
		ok, reason, err = tx.ToolStepLive(f.attempt)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ok, reason
}

var stepCalls = []journal.StepCall{
	{ProviderCallID: "p0", Name: "read_file", Class: "read", Args: []byte(`{"path":"a"}`)},
	{ProviderCallID: "p1", Name: "invented", Class: journal.ToolClassNone, Args: []byte(`not json`)},
	{ProviderCallID: "p2", Name: "web_fetch", Class: "web", Args: []byte(`{"url":"https://example.com/"}`)},
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

// The calls of an accepted tool step are journaled as accepted intent in the
// order returned, each with its event, and only once.
func TestRecordToolStep(t *testing.T) {
	f := newStepFixture(t)
	if !f.accept(t, f.epoch, "tool_calls") {
		t.Fatal("step not accepted")
	}
	calls, err := f.record(stepCalls...)
	if err != nil {
		t.Fatal(err)
	}
	got := f.journaled(t)
	if len(calls) != len(stepCalls) || len(got) != len(stepCalls) {
		t.Fatalf("recorded %d, journaled %d, want %d", len(calls), len(got), len(stepCalls))
	}
	for i, c := range got {
		want := stepCalls[i]
		if c.ID != calls[i].ID || c.CallIndex != i || c.ProviderCallID != want.ProviderCallID || c.Name != want.Name ||
			c.Class != want.Class || c.Status != journal.ToolCallAccepted || c.Result != nil {
			t.Fatalf("call %d = %+v, want %+v", i, c, want)
		}
		body, err := f.store.Content(c.ArgsSHA256)
		if err != nil || string(body) != string(want.Args) {
			t.Fatalf("call %d args = %q (%v), want %q", i, body, err, want.Args)
		}
	}
	if n := countKind(eventKinds(t, f.store, 0), "tool_call_accepted"); n != len(stepCalls) {
		t.Fatalf("%d tool_call_accepted events, want %d", n, len(stepCalls))
	}
	if _, err := f.record(stepCalls...); !errors.Is(err, journal.ErrToolStepRecorded) {
		t.Fatalf("second record: %v", err)
	}
	if got := f.journaled(t); len(got) != len(stepCalls) {
		t.Fatalf("second record journaled %d calls", len(got))
	}
	if _, err := f.record(); err == nil {
		t.Fatal("empty step recorded")
	}
}

// A call naming no tool is journaled under class none, so that the tool error
// answering it can be journaled; a call of an executable class still needs a
// name, and a step holding such a call is journaled not at all.
func TestRecordToolStepNamelessCall(t *testing.T) {
	f := newStepFixture(t)
	if !f.accept(t, f.epoch, "tool_calls") {
		t.Fatal("step not accepted")
	}
	nameless := journal.StepCall{ProviderCallID: "p9", Name: "", Class: "read", Args: []byte(`{}`)}
	if _, err := f.record(stepCalls[0], nameless); err == nil {
		t.Fatal("nameless read call recorded")
	}
	if got := f.journaled(t); len(got) != 0 {
		t.Fatalf("refused step left %d calls journaled", len(got))
	}
	if n := countKind(eventKinds(t, f.store, 0), "tool_call_accepted"); n != 0 {
		t.Fatalf("refused step left %d events", n)
	}
	nameless.Class = journal.ToolClassNone
	calls, err := f.record(nameless, stepCalls[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Name != "" || calls[0].Class != journal.ToolClassNone || calls[1].Name != "read_file" {
		t.Fatalf("recorded %+v", calls)
	}
	// The nameless call takes a result like any other.
	err = f.store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordToolResult(calls[0].ID, journal.ToolCallError, []byte("malformed"), "malformed_call")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.journaled(t); got[0].Status != journal.ToolCallError || got[0].Result.ErrorCode != "malformed_call" {
		t.Fatalf("nameless call = %+v", got[0])
	}
}

// Only an accepted tool step is recorded. A pending attempt, a late result
// quarantined at acceptance, a result the gate failed and a result under any
// other status are refused, and nothing of them is journaled.
func TestRecordToolStepRefusesUnacceptedStep(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f *stepFixture)
	}{
		{"pending", func(*testing.T, *stepFixture) {}},
		{"quarantined late", func(t *testing.T, f *stepFixture) {
			if err := f.store.Tx(func(tx *journal.Tx) error { _, err := tx.BumpEpoch(f.turn); return err }); err != nil {
				t.Fatal(err)
			}
			if f.accept(t, f.epoch, "tool_calls") {
				t.Fatal("late result accepted")
			}
		}},
		{"after the turn ended", func(t *testing.T, f *stepFixture) {
			if err := f.store.Tx(func(tx *journal.Tx) error { return tx.EndTurn(f.turn, "cancelled", "cancelled") }); err != nil {
				t.Fatal(err)
			}
			if f.accept(t, f.epoch, "tool_calls") {
				t.Fatal("result after the turn ended accepted")
			}
		}},
		{"gate failed", func(t *testing.T, f *stepFixture) {
			if !f.accept(t, f.epoch, "tool_calls") {
				t.Fatal("step not accepted")
			}
			if err := f.store.Tx(func(tx *journal.Tx) error { return tx.SetAttemptGate(f.attempt, "identity mismatch") }); err != nil {
				t.Fatal(err)
			}
		}},
		{"complete", func(t *testing.T, f *stepFixture) {
			if !f.accept(t, f.epoch, "complete") {
				t.Fatal("result not accepted")
			}
		}},
		{"truncated", func(t *testing.T, f *stepFixture) {
			if !f.accept(t, f.epoch, "truncated") {
				t.Fatal("result not accepted")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStepFixture(t)
			tc.prepare(t, f)
			if _, err := f.record(stepCalls...); !errors.Is(err, journal.ErrNotToolStep) {
				t.Fatalf("err = %v, want ErrNotToolStep", err)
			}
			if got := f.journaled(t); len(got) != 0 {
				t.Fatalf("%d calls journaled", len(got))
			}
			if n := countKind(eventKinds(t, f.store, 0), "tool_call_accepted"); n != 0 {
				t.Fatalf("%d tool_call_accepted events", n)
			}
		})
	}
	err := newStepFixture(t).store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordToolStep("nope", stepCalls)
		return err
	})
	if err == nil {
		t.Fatal("unknown attempt recorded")
	}
}

// ToolStepLive holds while the turn is open at the attempt's epoch, and names
// why not after an epoch bump or the turn's end.
func TestToolStepLive(t *testing.T) {
	f := newStepFixture(t)
	if ok, reason := f.live(t); !ok || reason != "" {
		t.Fatalf("open turn: live %t, reason %q", ok, reason)
	}
	if err := f.store.Tx(func(tx *journal.Tx) error { _, err := tx.BumpEpoch(f.turn); return err }); err != nil {
		t.Fatal(err)
	}
	if ok, reason := f.live(t); ok || reason != journal.StepEndedEpoch {
		t.Fatalf("after epoch bump: live %t, reason %q", ok, reason)
	}
	if err := f.store.Tx(func(tx *journal.Tx) error { return tx.EndTurn(f.turn, "cancelled", "cancelled") }); err != nil {
		t.Fatal(err)
	}
	if ok, reason := f.live(t); ok || reason != journal.StepEndedTurn {
		t.Fatalf("after cancel: live %t, reason %q", ok, reason)
	}

	// A turn ended at the attempt's epoch reports the end, not the epoch.
	g := newStepFixture(t)
	if err := g.store.Tx(func(tx *journal.Tx) error { return tx.EndTurn(g.turn, "incomplete", "deadline:turn") }); err != nil {
		t.Fatal(err)
	}
	if ok, reason := g.live(t); ok || reason != journal.StepEndedTurn {
		t.Fatalf("ended turn: live %t, reason %q", ok, reason)
	}
	if err := g.store.Tx(func(tx *journal.Tx) error { _, _, err := tx.ToolStepLive("nope"); return err }); err == nil {
		t.Fatal("unknown attempt reported")
	}
}
