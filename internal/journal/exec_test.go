package journal_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

// execFixture is a turn with one bash call, a referee decision allowing it
// and a referee attempt to cite in reads.
type execFixture struct {
	toolsFixture
	call, decision, refAttempt string
}

func buildExecCall(t *testing.T, store *journal.Store) execFixture {
	t.Helper()
	f := execFixture{toolsFixture: buildToolsTurn(t, store)}
	err := store.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordToolCall(f.author, 0, "call_0", "bash", journal.ToolClassExec, []byte(`{"command":"ls"}`))
		if err != nil {
			return err
		}
		f.call = c.ID
		if f.refAttempt, err = tx.BeginAttempt(f.turn, "referee", "referee", 1, nil, ""); err != nil {
			return err
		}
		d, err := tx.RecordRefereeDecision(f.call, journal.RefereeAllow, "lists files", "")
		f.decision = d.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func recordExec(t *testing.T, store *journal.Store, f execFixture) {
	t.Helper()
	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordExecCall(journal.ExecCallInput{
			ToolCallID: f.call, Command: []byte("ls"), TimeoutSEffective: 120, Shell: "/bin/bash",
			View:              journal.ExecView{SnapshotVersion: 3, OverlaySHA256: "ov", ModesSHA256: "md"},
			RefereeDecisionID: f.decision, SandboxBackend: "bwrap", PolicyHash: "ph",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecCallAndResultRoundTrip(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildExecCall(t, store)
	recordExec(t, store, f)

	sig := 9
	out := []byte("a\nb\n")
	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordExecResult(f.call, journal.ExecResultInput{
			ExitCode: 137, Signal: &sig, TimedOut: true, Truncated: true, CaptureComplete: false,
			Output: out, OutputBytesTotal: 100, DurationMS: 1234,
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	calls, err := store.ExecCallsByTurn(f.turn)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls = %+v, %v", calls, err)
	}
	c := calls[0]
	if c.ToolCallID != f.call || c.CommandSHA256 != shaBytes([]byte("ls")) || c.TimeoutSEffective != 120 || c.Shell != "/bin/bash" ||
		c.View != (journal.ExecView{SnapshotVersion: 3, OverlaySHA256: "ov", ModesSHA256: "md"}) ||
		c.RefereeDecisionID != f.decision || c.SandboxBackend != "bwrap" || c.PolicyHash != "ph" {
		t.Fatalf("call = %+v", c)
	}
	r := c.Result
	if r == nil || r.ExitCode != 137 || r.Signal == nil || *r.Signal != 9 || !r.TimedOut || !r.Truncated || r.CaptureComplete ||
		r.OutputSHA256 != shaBytes(out) || r.OutputBytesKept != 4 || r.OutputBytesTotal != 100 || r.DurationMS != 1234 {
		t.Fatalf("result = %+v", r)
	}
	if body, err := store.Content(r.OutputSHA256); err != nil || string(body) != string(out) {
		t.Fatalf("content = %q, %v", body, err)
	}
	if cmd, err := store.Content(c.CommandSHA256); err != nil || string(cmd) != "ls" {
		t.Fatalf("command = %q, %v", cmd, err)
	}

	// A second result is refused, and so is an abandoned outcome beside one.
	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordExecResult(f.call, journal.ExecResultInput{})
		return err
	})
	if !errors.Is(err, journal.ErrExecResultRecorded) {
		t.Fatalf("second result: %v", err)
	}
	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordExecOutcome(f.call, journal.ExecAbandoned, journal.ExecCauseCancelled)
		return err
	})
	if !errors.Is(err, journal.ErrExecOutcomeRecorded) {
		t.Fatalf("abandoned beside result: %v", err)
	}
	kinds := strings.Join(eventKinds(t, store, 0), ",")
	for _, k := range []string{"referee_decision", "exec_call", "exec_result"} {
		if !strings.Contains(kinds, k) {
			t.Errorf("no %s event in %s", k, kinds)
		}
	}
}

func TestExecOutcomes(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildExecCall(t, store)
	recordExec(t, store, f)

	record := func(kind, cause string) error {
		return store.Tx(func(tx *journal.Tx) error {
			_, err := tx.RecordExecOutcome(f.call, kind, cause)
			return err
		})
	}
	if err := record(journal.ExecAbandoned, journal.ExecCauseTurnDeadline); err != nil {
		t.Fatal(err)
	}
	if err := record(journal.ExecContainmentUnverified, journal.ExecCauseCleanupDeadline); err != nil {
		t.Fatalf("containment_unverified beside abandoned: %v", err)
	}
	if err := record(journal.ExecAbandoned, journal.ExecCauseRecovery); !errors.Is(err, journal.ErrExecOutcomeRecorded) {
		t.Fatalf("second abandoned: %v", err)
	}
	if err := record("other", journal.ExecCauseRecovery); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := record(journal.ExecAbandoned, "bored"); err == nil {
		t.Fatal("unknown cause accepted")
	}
	err := store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordExecResult(f.call, journal.ExecResultInput{})
		return err
	})
	if !errors.Is(err, journal.ErrExecResultRecorded) {
		t.Fatalf("result for abandoned call: %v", err)
	}
	calls, err := store.ExecCallsByTurn(f.turn)
	if err != nil || len(calls) != 1 || calls[0].Result != nil || len(calls[0].Outcomes) != 2 ||
		calls[0].Outcomes[0].Kind != journal.ExecAbandoned || calls[0].Outcomes[0].Cause != journal.ExecCauseTurnDeadline ||
		calls[0].Outcomes[1].Kind != journal.ExecContainmentUnverified {
		t.Fatalf("calls = %+v, %v", calls, err)
	}
	if k := strings.Join(eventKinds(t, store, 0), ","); !strings.Contains(k, "exec_containment_unverified") || !strings.Contains(k, "exec_abandoned") {
		t.Fatalf("events = %s", k)
	}
}

func TestRefereeDecisionAndReads(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildExecCall(t, store)

	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.RecordRefereeRead(f.decision, f.refAttempt, "read_file", []byte(`{"path":"a"}`), []byte("hello"), false); err != nil {
			return err
		}
		_, err := tx.RecordRefereeRead(f.decision, f.refAttempt, "read_file", []byte(`{"path":"b"}`), nil, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var d journal.RefereeDecision
	err = store.Tx(func(tx *journal.Tx) error {
		var err error
		d, err = tx.RefereeDecisionForCall(f.call)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != f.decision || d.Decision != journal.RefereeAllow || d.Reason != "lists files" || d.Cause != "" || d.TurnID != f.turn || len(d.Reads) != 2 {
		t.Fatalf("decision = %+v", d)
	}
	r0, r1 := d.Reads[0], d.Reads[1]
	if r0.Position != 0 || r0.AttemptID != f.refAttempt || r0.Name != "read_file" || r0.ArgsSHA256 != shaBytes([]byte(`{"path":"a"}`)) ||
		r0.SHA256 != shaBytes([]byte("hello")) || r0.ByteLen != 5 {
		t.Fatalf("read 0 = %+v", r0)
	}
	if r1.Position != 1 || r1.SHA256 != "" || r1.ByteLen != 0 {
		t.Fatalf("read 1 = %+v", r1)
	}
	var null int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM referee_reads WHERE sha256 IS NULL AND byte_len IS NULL`).Scan(&null); err != nil || null != 1 {
		t.Fatalf("null read rows = %d, %v", null, err)
	}
	if k := eventKinds(t, store, 0); strings.Count(strings.Join(k, ","), "referee_read") != 2 {
		t.Fatalf("events = %v", k)
	}

	// A deny needs a cause; an allow takes none; one decision per call.
	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordRefereeDecision(f.call, journal.RefereeDeny, "no", "")
		return err
	})
	if err == nil {
		t.Error("deny without cause accepted")
	}
	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordRefereeDecision(f.call, journal.RefereeAllow, "again", "")
		return err
	})
	if err == nil {
		t.Error("second decision accepted")
	}

	// A deny round-trips by turn, and cannot start a command.
	var denied journal.RefereeDecision
	err = store.Tx(func(tx *journal.Tx) error {
		c, err := tx.RecordToolCall(f.author, 1, "call_1", "bash", journal.ToolClassExec, []byte(`{}`))
		if err != nil {
			return err
		}
		denied, err = tx.RecordRefereeDecision(c.ID, journal.RefereeDeny, "unsafe", journal.RefereeCauseAnswer)
		if err != nil {
			return err
		}
		_, err = tx.RecordExecCall(journal.ExecCallInput{
			ToolCallID: c.ID, Command: []byte("rm"), TimeoutSEffective: 1, Shell: "/bin/sh",
			RefereeDecisionID: denied.ID, SandboxBackend: "b", PolicyHash: "p",
		})
		if err == nil {
			t.Error("exec call under a deny accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	all, err := store.RefereeDecisionsByTurn(f.turn)
	if err != nil || len(all) != 2 || all[1].ID != denied.ID || all[1].Decision != journal.RefereeDeny || all[1].Cause != journal.RefereeCauseAnswer {
		t.Fatalf("decisions = %+v, %v", all, err)
	}
}

func TestExecConnectionsAndClose(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildExecCall(t, store)
	recordExec(t, store, f)

	var open, refused journal.ExecConnection
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		if open, err = tx.RecordExecConnection(f.call, "example.com", 443, true, 0); err != nil {
			return err
		}
		refused, err = tx.RecordExecConnection(f.call, "evil.test", 80, false, 403)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var conns []journal.ExecConnection
	read := func() {
		t.Helper()
		err := store.Tx(func(tx *journal.Tx) error {
			var err error
			conns, err = tx.ExecConnections(f.call)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	read()
	if len(conns) != 2 || conns[0].Close != nil || conns[0].Host != "example.com" || conns[0].Port != 443 || !conns[0].Allowed ||
		conns[1].Allowed || conns[1].RefusalStatus != 403 {
		t.Fatalf("conns = %+v", conns)
	}
	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.CloseExecConnection(open.ID, 10, 2000)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	read()
	if conns[0].ID != open.ID || conns[0].Close == nil || conns[0].Close.BytesUp != 10 || conns[0].Close.BytesDown != 2000 || conns[1].Close != nil {
		t.Fatalf("conns = %+v", conns)
	}
	for name, fn := range map[string]func(tx *journal.Tx) error{
		"close twice":    func(tx *journal.Tx) error { _, err := tx.CloseExecConnection(open.ID, 1, 1); return err },
		"close refused":  func(tx *journal.Tx) error { _, err := tx.CloseExecConnection(refused.ID, 0, 0); return err },
		"refused no 4xx": func(tx *journal.Tx) error { _, err := tx.RecordExecConnection(f.call, "h", 1, false, 500); return err },
		"allowed + 407":  func(tx *journal.Tx) error { _, err := tx.RecordExecConnection(f.call, "h", 1, true, 407); return err },
	} {
		if err := store.Tx(fn); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	byTurn := []journal.ExecConnection(nil)
	err = store.Tx(func(tx *journal.Tx) error {
		var err error
		byTurn, err = tx.ExecConnectionsByTurn(f.turn)
		return err
	})
	if err != nil || len(byTurn) != 2 {
		t.Fatalf("by turn = %+v, %v", byTurn, err)
	}
}

func TestExecTablesAreAppendOnly(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildExecCall(t, store)
	recordExec(t, store, f)
	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.RecordExecResult(f.call, journal.ExecResultInput{Output: []byte("x"), OutputBytesTotal: 1, CaptureComplete: true}); err != nil {
			return err
		}
		if _, err := tx.RecordExecOutcome(f.call, journal.ExecContainmentUnverified, journal.ExecCauseRecovery); err != nil {
			return err
		}
		if _, err := tx.RecordRefereeRead(f.decision, f.refAttempt, "list_dir", []byte(`{}`), []byte("x"), false); err != nil {
			return err
		}
		c, err := tx.RecordExecConnection(f.call, "h", 443, true, 0)
		if err != nil {
			return err
		}
		_, err = tx.CloseExecConnection(c.ID, 1, 2)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c := db.Conn()
	for _, table := range []string{"referee_decisions", "exec_calls", "exec_results", "exec_outcomes", "referee_reads", "exec_connections", "exec_connection_closes"} {
		var n int
		if err := c.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n == 0 {
			t.Errorf("%s has %d rows, %v", table, n, err)
		}
		if _, err := c.Exec(`DELETE FROM ` + table); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Errorf("delete from %s: %v", table, err)
		}
	}
	for _, stmt := range []string{
		`UPDATE referee_decisions SET reason = 'x'`,
		`UPDATE exec_calls SET shell = 'x'`,
		`UPDATE exec_results SET exit_code = 1`,
		`UPDATE exec_outcomes SET cause = 'recovery'`,
		`UPDATE referee_reads SET name = 'x'`,
		`UPDATE exec_connections SET host = 'x'`,
		`UPDATE exec_connection_closes SET bytes_up = 5`,
	} {
		if _, err := c.Exec(stmt); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Errorf("%s: %v", stmt, err)
		}
	}
}
