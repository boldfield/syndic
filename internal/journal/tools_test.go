package journal_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

var errForcedRollback = errors.New("forced rollback")

// toolFixture is a database with one run, turn and attempt, opened at a path
// the test can close and reopen to simulate a restart.
type toolFixture struct {
	t    *testing.T
	path string
	db   *journal.DB
	s    *journal.Store
}

func newToolFixture(t *testing.T) *toolFixture {
	t.Helper()
	f := &toolFixture{t: t, path: filepath.Join(t.TempDir(), "tools.db")}
	f.open()
	setupTestData(t, f.db.Conn(), "run1", "turn1")
	createTestAttempt(t, f.db.Conn(), "attempt1", "turn1", "author")
	return f
}

func (f *toolFixture) open() {
	f.t.Helper()
	db, err := journal.Open(f.path)
	if err != nil {
		f.t.Fatalf("open: %v", err)
	}
	f.db = db
	f.s = journal.NewStore(db)
	f.t.Cleanup(func() { _ = f.db.Close() })
}

// restart closes the database and reopens it from disk with a new Store.
func (f *toolFixture) restart() {
	f.t.Helper()
	if err := f.db.Close(); err != nil {
		f.t.Fatalf("close: %v", err)
	}
	f.open()
}

func (f *toolFixture) tx(fn func(tx *journal.Tx) error) {
	f.t.Helper()
	if err := f.s.Tx(fn); err != nil {
		f.t.Fatalf("tx: %v", err)
	}
}

func (f *toolFixture) put(body string) string {
	f.t.Helper()
	var sum string
	f.tx(func(tx *journal.Tx) error {
		var err error
		sum, err = tx.PutToolContent([]byte(body))
		return err
	})
	return sum
}

func (f *toolFixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.Conn().QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatalf("count: %v", err)
	}
	return n
}

func TestToolContentIsHashAddressed(t *testing.T) {
	f := newToolFixture(t)
	sum := f.put("hello")
	if sum != computeSHA256(t, "hello") {
		t.Fatalf("hash = %s", sum)
	}
	if again := f.put("hello"); again != sum {
		t.Fatalf("second put = %s, want %s", again, sum)
	}
	f.restart()
	var got []byte
	f.tx(func(tx *journal.Tx) error {
		var err error
		got, err = tx.GetToolContent(sum)
		return err
	})
	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
	data, total, hash, err := f.s.ContentRange("tool:"+sum, 1, 3)
	if err != nil || string(data) != "ell" || total != 5 || hash != sum {
		t.Fatalf("ContentRange = %q %d %s %v", data, total, hash, err)
	}
	err = f.s.Tx(func(tx *journal.Tx) error {
		_, err := tx.GetToolContent("nope")
		return err
	})
	if !errors.Is(err, journal.ErrNoSuchContent) {
		t.Fatalf("missing content err = %v", err)
	}
	if _, err := f.db.Conn().Exec(`DELETE FROM tool_content`); err == nil {
		t.Fatal("tool_content must be immutable")
	}
}

func TestToolCallRejectsUncitedContent(t *testing.T) {
	f := newToolFixture(t)
	err := f.s.Tx(func(tx *journal.Tx) error {
		return tx.StoreToolCall(journal.ToolCall{ID: "c1", AttemptID: "attempt1", Name: "read_file", ArgsSHA256: "missing"})
	})
	if err == nil {
		t.Fatal("tool call citing hash absent from the content store must fail")
	}
}

// TestToolCallLifecycleReconstructsAfterRestart journals intent before
// dispatch, records the result later, then rebuilds args and result bytes
// from their hashes in a reopened database.
func TestToolCallLifecycleReconstructsAfterRestart(t *testing.T) {
	f := newToolFixture(t)
	args := `{"path":"a.go"}`
	result := "package a\n"

	f.tx(func(tx *journal.Tx) error {
		sum, err := tx.PutToolContent([]byte(args))
		if err != nil {
			return err
		}
		return tx.StoreToolCall(journal.ToolCall{ID: "c1", AttemptID: "attempt1", Name: "read_file", ArgsSHA256: sum, Class: "read"})
	})

	var calls []journal.ToolCall
	f.tx(func(tx *journal.Tx) (err error) { calls, err = tx.GetToolCallsByAttempt("attempt1"); return })
	if len(calls) != 1 || calls[0].Status != journal.ToolCallStatusAccepted || calls[0].ResultSHA256 != nil {
		t.Fatalf("before result: %+v", calls)
	}

	f.restart() // intent survives; the result arrives after the restart

	f.tx(func(tx *journal.Tx) error {
		sum, err := tx.PutToolContent([]byte(result))
		if err != nil {
			return err
		}
		return tx.StoreToolResult(journal.ToolResult{
			ID: "r1", ToolCallID: "c1", Status: journal.ToolCallStatusExecuted,
			ResultSHA256: &sum, ResultByteLen: int64Ptr(int64(len(result))), RecordedAt: time.Now(),
		})
	})

	// A second terminal record for the same call is refused.
	err := f.s.Tx(func(tx *journal.Tx) error {
		return tx.StoreToolResult(journal.ToolResult{ID: "r2", ToolCallID: "c1", Status: journal.ToolCallStatusError, RecordedAt: time.Now()})
	})
	if err == nil {
		t.Fatal("second terminal result for one call must fail")
	}

	f.restart()
	f.tx(func(tx *journal.Tx) (err error) { calls, err = tx.GetToolCallsByAttempt("attempt1"); return })
	if len(calls) != 1 || calls[0].Status != journal.ToolCallStatusExecuted || calls[0].ResultSHA256 == nil {
		t.Fatalf("after result: %+v", calls)
	}
	f.tx(func(tx *journal.Tx) error {
		gotArgs, err := tx.GetToolContent(calls[0].ArgsSHA256)
		if err != nil || string(gotArgs) != args {
			t.Errorf("args = %q, %v", gotArgs, err)
		}
		gotResult, err := tx.GetToolContent(*calls[0].ResultSHA256)
		if err != nil || string(gotResult) != result {
			t.Errorf("result = %q, %v", gotResult, err)
		}
		return nil
	})
	var results []journal.ToolResult
	f.tx(func(tx *journal.Tx) (err error) { results, err = tx.GetToolResultsByToolCall("c1"); return })
	if len(results) != 1 || results[0].ID != "r1" {
		t.Fatalf("results = %+v", results)
	}
}

func TestToolResultCannotBeChanged(t *testing.T) {
	f := newToolFixture(t)
	args := f.put("{}")
	f.tx(func(tx *journal.Tx) error {
		if err := tx.StoreToolCall(journal.ToolCall{ID: "c1", AttemptID: "attempt1", Name: "x", ArgsSHA256: args}); err != nil {
			return err
		}
		return tx.StoreToolResult(journal.ToolResult{ID: "r1", ToolCallID: "c1", Status: journal.ToolCallStatusAbandoned, RecordedAt: time.Now()})
	})
	for _, q := range []string{
		`UPDATE tool_calls SET name = 'y'`, `DELETE FROM tool_calls`,
		`UPDATE tool_results SET status = 'executed'`, `DELETE FROM tool_results`,
	} {
		if _, err := f.db.Conn().Exec(q); err == nil {
			t.Errorf("%s must be refused", q)
		}
	}
}

// TestRecordAndEventRollBackTogether checks that each record method appends
// its event in the same transaction, so a failed transaction leaves neither.
func TestRecordAndEventRollBackTogether(t *testing.T) {
	f := newToolFixture(t)
	args := f.put("{}")
	events := func() int {
		return f.count(`SELECT COUNT(*) FROM events WHERE event_type = ?`, journal.EventToolCallAccepted)
	}

	err := f.s.Tx(func(tx *journal.Tx) error {
		if err := tx.StoreToolCall(journal.ToolCall{ID: "c1", AttemptID: "attempt1", Name: "x", ArgsSHA256: args}); err != nil {
			return err
		}
		return errForcedRollback
	})
	if !errors.Is(err, errForcedRollback) {
		t.Fatalf("err = %v", err)
	}
	if n := f.count(`SELECT COUNT(*) FROM tool_calls`); n != 0 {
		t.Errorf("tool_calls rows after rollback = %d", n)
	}
	if n := events(); n != 0 {
		t.Errorf("events after rollback = %d", n)
	}

	f.tx(func(tx *journal.Tx) error {
		return tx.StoreToolCall(journal.ToolCall{ID: "c1", AttemptID: "attempt1", Name: "x", ArgsSHA256: args})
	})
	if n := f.count(`SELECT COUNT(*) FROM tool_calls`); n != 1 {
		t.Errorf("tool_calls rows after commit = %d", n)
	}
	if n := events(); n != 1 {
		t.Errorf("events after commit = %d", n)
	}
}

func TestEveryRecordMethodEmitsAnEvent(t *testing.T) {
	f := newToolFixture(t)
	h := f.put("body")
	now := time.Now()
	f.tx(func(tx *journal.Tx) error {
		steps := []func() error{
			func() error {
				return tx.StoreToolCall(journal.ToolCall{ID: "c1", AttemptID: "attempt1", Name: "x", ArgsSHA256: h})
			},
			func() error {
				return tx.StoreToolResult(journal.ToolResult{ID: "r1", ToolCallID: "c1", Status: "executed", ResultSHA256: &h, RecordedAt: now})
			},
			func() error {
				return tx.StoreObservation(journal.Observation{ID: "o1", TurnID: "turn1", AttemptID: "attempt1", SnapshotVersion: 1, Path: "a", Kind: "f", BaseSHA256: &h, ObservedAt: now})
			},
			func() error {
				return tx.StoreChangeSet(journal.ChangeSet{ID: "cs1", TurnID: "turn1", SnapshotHash: h, ChangesHash: h, CreatedAt: now})
			},
			func() error {
				return tx.StoreApplyOp(journal.ApplyOp{ID: "ao1", ChangeSetID: "cs1", OpType: journal.ApplyOpStarted, AppliedAt: now})
			},
			func() error { return tx.StoreStaging("turn1", "a", h, "") },
			func() error { return tx.UnstageFile("turn1", "a") },
		}
		for i, step := range steps {
			if err := step(); err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
		}
		return nil
	})
	for _, kind := range []string{
		journal.EventToolCallAccepted, journal.EventToolResultRecorded, journal.EventObservationStored,
		journal.EventChangeSetFrozen, journal.EventApplyOpRecorded, journal.EventPathStaged, journal.EventPathUnstaged,
	} {
		if n := f.count(`SELECT COUNT(*) FROM events WHERE event_type = ?`, kind); n != 1 {
			t.Errorf("%s events = %d, want 1", kind, n)
		}
	}
}

func TestObservationEvidenceReconstructsAfterRestart(t *testing.T) {
	f := newToolFixture(t)
	file := f.put("package a\n")
	listing := f.put("a.go\nb.go\n")
	f.tx(func(tx *journal.Tx) error {
		now := time.Now()
		for _, o := range []journal.Observation{
			{ID: "o1", TurnID: "turn1", AttemptID: "attempt1", SnapshotVersion: 1, Path: "a.go", Kind: journal.ObservationKindFile, BaseSHA256: &file, ObservedAt: now},
			{ID: "o2", TurnID: "turn1", AttemptID: "attempt1", SnapshotVersion: 2, Path: "dir", Kind: journal.ObservationKindDirectory, EntriesSHA256: &listing, ObservedAt: now},
			{ID: "o3", TurnID: "turn1", AttemptID: "attempt1", SnapshotVersion: 3, Path: "gone", Kind: journal.ObservationKindAbsent, ObservedAt: now},
		} {
			if err := tx.StoreObservation(o); err != nil {
				return err
			}
		}
		return nil
	})
	f.restart()
	f.tx(func(tx *journal.Tx) error {
		obs, err := tx.GetObservationsByTurn("turn1")
		if err != nil || len(obs) != 3 {
			t.Fatalf("observations = %+v, %v", obs, err)
		}
		if v, _ := tx.GetLatestObservationVersion("turn1"); v != 3 {
			t.Errorf("latest version = %d", v)
		}
		if b, err := tx.GetToolContent(*obs[0].BaseSHA256); err != nil || string(b) != "package a\n" {
			t.Errorf("file content = %q, %v", b, err)
		}
		if b, err := tx.GetToolContent(*obs[1].EntriesSHA256); err != nil || string(b) != "a.go\nb.go\n" {
			t.Errorf("listing = %q, %v", b, err)
		}
		if obs[2].BaseSHA256 != nil || obs[2].EntriesSHA256 != nil {
			t.Errorf("absence observation carries content: %+v", obs[2])
		}
		return nil
	})
	// Observation versions are unique per turn and path.
	err := f.s.Tx(func(tx *journal.Tx) error {
		return tx.StoreObservation(journal.Observation{ID: "o4", TurnID: "turn1", AttemptID: "attempt1", SnapshotVersion: 1, Path: "a.go", Kind: "a", ObservedAt: time.Now()})
	})
	if err == nil {
		t.Fatal("duplicate (turn, path, version) must fail")
	}
}

// TestChangeSetApplyStateIsDerivedAndDurable drives a change set through
// appliable, partial, conflict, applied and discarded using only append-only
// apply ops, restarting between steps.
func TestChangeSetApplyStateIsDerivedAndDurable(t *testing.T) {
	f := newToolFixture(t)
	base := f.put("old")
	newA := f.put("new a")
	newB := f.put("new b")
	snap := f.put("snapshot")
	changes := f.put("changes")
	now := time.Now()

	f.tx(func(tx *journal.Tx) error {
		if err := tx.StoreChangeSet(journal.ChangeSet{ID: "cs1", TurnID: "turn1", SnapshotHash: snap, ChangesHash: changes, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.StoreChangeSetPath(journal.ChangeSetPath{ID: "p1", ChangeSetID: "cs1", Path: "a:b.go", Op: journal.ChangeSetOpModify, BaseSHA256: &base, NewSHA256: newA, NewLen: 5}); err != nil {
			return err
		}
		return tx.StoreChangeSetPath(journal.ChangeSetPath{ID: "p2", ChangeSetID: "cs1", Path: "b.go", Op: journal.ChangeSetOpCreate, NewSHA256: newB, NewLen: 5})
	})

	state := func(want string, wantOutcomes map[string]string) {
		t.Helper()
		f.restart()
		f.tx(func(tx *journal.Tx) error {
			got, outcomes, err := tx.ComputeApplyState("cs1")
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("state = %s, want %s", got, want)
			}
			for p, o := range wantOutcomes {
				if outcomes[p] != o {
					t.Errorf("outcome[%s] = %s, want %s", p, outcomes[p], o)
				}
			}
			cs, err := tx.GetChangeSetByTurn("turn1")
			if err != nil || cs == nil || cs.State != want {
				t.Errorf("GetChangeSetByTurn state = %+v, %v; want %s", cs, err, want)
			}
			return nil
		})
	}
	op := func(o journal.ApplyOp) {
		t.Helper()
		o.ID = o.OpType + "-" + time.Now().Format(time.RFC3339Nano) + func() string {
			if o.Path != nil {
				return *o.Path
			}
			return ""
		}()
		o.ChangeSetID = "cs1"
		o.AppliedAt = time.Now()
		f.tx(func(tx *journal.Tx) error { return tx.StoreApplyOp(o) })
	}

	state(journal.ChangeSetStateAppliable, map[string]string{"a:b.go": "pending", "b.go": "pending"})

	op(journal.ApplyOp{OpType: journal.ApplyOpStarted})
	op(journal.ApplyOp{OpType: journal.ApplyOpFile, Path: strPtr("a:b.go"), NewSHA256: &newA, Outcome: strPtr("applied")})
	state(journal.ChangeSetStatePartial, map[string]string{"a:b.go": "applied", "b.go": "pending"})

	op(journal.ApplyOp{OpType: journal.ApplyOpFile, Path: strPtr("b.go"), Outcome: strPtr("conflict")})
	op(journal.ApplyOp{OpType: journal.ApplyOpFinished, FinalState: strPtr("partial")})
	state(journal.ChangeSetStatePartial, map[string]string{"a:b.go": "applied", "b.go": "conflict"})

	op(journal.ApplyOp{OpType: journal.ApplyOpDiscarded})
	state(journal.ChangeSetStateDiscarded, map[string]string{"a:b.go": "applied", "b.go": "conflict"})
}

func TestChangeSetFinishedStates(t *testing.T) {
	for _, final := range []string{"applied", "conflict"} {
		t.Run(final, func(t *testing.T) {
			f := newToolFixture(t)
			h := f.put("x")
			f.tx(func(tx *journal.Tx) error {
				if err := tx.StoreChangeSet(journal.ChangeSet{ID: "cs1", TurnID: "turn1", SnapshotHash: h, ChangesHash: h, CreatedAt: time.Now()}); err != nil {
					return err
				}
				if err := tx.StoreChangeSetPath(journal.ChangeSetPath{ID: "p1", ChangeSetID: "cs1", Path: "a", Op: "create", NewSHA256: h, NewLen: 1}); err != nil {
					return err
				}
				outcome := "applied"
				if final == "conflict" {
					outcome = "conflict"
				}
				for _, o := range []journal.ApplyOp{
					{ID: "1", OpType: journal.ApplyOpStarted},
					{ID: "2", OpType: journal.ApplyOpFile, Path: strPtr("a"), Outcome: &outcome},
					{ID: "3", OpType: journal.ApplyOpFinished, FinalState: &final},
				} {
					o.ChangeSetID, o.AppliedAt = "cs1", time.Now()
					if err := tx.StoreApplyOp(o); err != nil {
						return err
					}
				}
				state, outcomes, err := tx.ComputeApplyState("cs1")
				if err != nil || state != final || outcomes["a"] != outcome {
					t.Errorf("state=%s outcomes=%v err=%v", state, outcomes, err)
				}
				return nil
			})
		})
	}
}

func TestChangeSetRecordsAreImmutable(t *testing.T) {
	f := newToolFixture(t)
	h := f.put("x")
	f.tx(func(tx *journal.Tx) error {
		if err := tx.StoreChangeSet(journal.ChangeSet{ID: "cs1", TurnID: "turn1", SnapshotHash: h, ChangesHash: h, CreatedAt: time.Now()}); err != nil {
			return err
		}
		if err := tx.StoreChangeSetPath(journal.ChangeSetPath{ID: "p1", ChangeSetID: "cs1", Path: "a", Op: "create", NewSHA256: h, NewLen: 1}); err != nil {
			return err
		}
		return tx.StoreApplyOp(journal.ApplyOp{ID: "ao", ChangeSetID: "cs1", OpType: journal.ApplyOpStarted, AppliedAt: time.Now()})
	})
	for _, q := range []string{
		`UPDATE change_sets SET snapshot_hash = 'z'`, `DELETE FROM change_sets`,
		`UPDATE change_set_paths SET path = 'z'`, `DELETE FROM change_set_paths`,
		`UPDATE apply_ops SET op_type = 'finished'`, `DELETE FROM apply_ops`,
	} {
		if _, err := f.db.Conn().Exec(q); err == nil {
			t.Errorf("%s must be refused", q)
		}
	}
}

func TestVerdictEvidenceAndStagingSurviveRestart(t *testing.T) {
	f := newToolFixture(t)
	h1, h2 := f.put("v1"), f.put("v2")
	createTestAttemptIn(t, f.db.Conn(), "rev1", "turn1", "reviewer1", 1)
	createTestAttemptIn(t, f.db.Conn(), "rev2", "turn1", "reviewer1", 1)
	insertTestVerdict(t, f.db.Conn(), "v1", "rev2")
	f.tx(func(tx *journal.Tx) error {
		if err := tx.StoreToolCall(journal.ToolCall{ID: "c1", AttemptID: "rev1", Name: "x", ArgsSHA256: h1}); err != nil {
			return err
		}
		if _, err := tx.StoreVerdictToolResult("v1", "c1"); err == nil {
			t.Error("unfinished call must not be verdict evidence")
		}
		if err := tx.StoreToolResult(journal.ToolResult{ID: "r1", ToolCallID: "c1", Status: journal.ToolCallStatusExecuted, ResultSHA256: &h2, RecordedAt: time.Now()}); err != nil {
			return err
		}
		if rid, err := tx.StoreVerdictToolResult("v1", "c1"); err != nil || rid != "r1" {
			t.Errorf("verdict evidence link = %q, %v", rid, err)
		}
		if err := tx.StoreVerdictSnapshot("v1", 3); err != nil {
			return err
		}
		if err := tx.StoreStaging("turn1", "dir/a:b", h1, ""); err != nil {
			return err
		}
		return tx.StoreStaging("turn1", "dir/a:b", h2, "") // restage replaces
	})
	f.restart()
	f.tx(func(tx *journal.Tx) error {
		ids, err := tx.GetVerdictToolResults("v1")
		if err != nil || len(ids) != 1 || ids[0] != "r1" {
			t.Errorf("verdict evidence = %v, %v", ids, err)
		}
		if v, ok, err := tx.GetVerdictSnapshot("v1"); err != nil || !ok || v != 3 {
			t.Errorf("verdict snapshot = %d, %v, %v", v, ok, err)
		}
		st, err := tx.GetStagingByTurn("turn1")
		if err != nil || len(st) != 1 || st["dir/a:b"] != h2 {
			t.Errorf("staging = %v, %v", st, err)
		}
		body, err := tx.GetToolContent(st["dir/a:b"])
		if err != nil || string(body) != "v2" {
			t.Errorf("staged content = %q, %v", body, err)
		}
		return nil
	})
}

// Helper functions

func strPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *int64 {
	return &i
}

func createTestAttempt(t *testing.T, conn *sql.DB, attemptID, turnID, participant string) {
	t.Helper()
	createTestAttemptIn(t, conn, attemptID, turnID, participant, 1)
}

// createTestAttemptIn inserts a completed attempt in the given epoch, ordered
// after every earlier attempt by journal_seq, as the engine dispatches them.
func createTestAttemptIn(t *testing.T, conn *sql.DB, attemptID, turnID, participant string, epoch int) {
	t.Helper()
	_, err := conn.Exec(`
		INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, journal_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(journal_seq), 0) + 1 FROM attempts))
	`, attemptID, turnID, participant, "author", epoch, time.Now().Format(time.RFC3339Nano), "completed")
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
}

func insertTestVerdict(t *testing.T, conn *sql.DB, verdictID, attemptID string) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO verdicts (id, attempt_id, candidate_hash, validation_status) VALUES (?, ?, 'h', 'valid')`, verdictID, attemptID); err != nil {
		t.Fatalf("insert verdict: %v", err)
	}
}

func computeSHA256(t *testing.T, content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

func TestApplyStateCrashBoundaries(t *testing.T) {
	f := newToolFixture(t)
	h := f.put("x")
	f.tx(func(tx *journal.Tx) error {
		if err := tx.StoreChangeSet(journal.ChangeSet{ID: "cs1", TurnID: "turn1", SnapshotHash: h, SnapshotVersion: 2, ChangesHash: h, CreatedAt: time.Now()}); err != nil {
			return err
		}
		return tx.StoreChangeSetPath(journal.ChangeSetPath{ID: "p1", ChangeSetID: "cs1", Path: "a", Op: "create", NewSHA256: h, NewLen: 1})
	})
	op := func(id, typ, final string) {
		f.tx(func(tx *journal.Tx) error {
			o := journal.ApplyOp{ID: id, ChangeSetID: "cs1", OpType: typ, AppliedAt: time.Now()}
			if final != "" {
				o.FinalState = &final
			}
			return tx.StoreApplyOp(o)
		})
	}
	state := func() string {
		var st string
		f.tx(func(tx *journal.Tx) error {
			var err error
			st, _, err = tx.ComputeApplyState("cs1")
			return err
		})
		return st
	}
	if got := state(); got != journal.ChangeSetStateAppliable {
		t.Fatalf("fresh = %s", got)
	}
	op("o1", journal.ApplyOpStarted, "")
	if got := state(); got != journal.ChangeSetStatePartial {
		t.Fatalf("started only = %s, want partial", got)
	}
	op("o2", journal.ApplyOpFinished, journal.ChangeSetStateConflict)
	if got := state(); got != journal.ChangeSetStateConflict {
		t.Fatalf("finished conflict = %s", got)
	}
	op("o3", journal.ApplyOpStarted, "")
	if got := state(); got != journal.ChangeSetStatePartial {
		t.Fatalf("retry started after conflict = %s, want partial", got)
	}
	op("o4", journal.ApplyOpFinished, journal.ChangeSetStateApplied)
	if got := state(); got != journal.ChangeSetStateApplied {
		t.Fatalf("finished applied = %s", got)
	}
}

func TestObservationSnapshotInvariants(t *testing.T) {
	f := newToolFixture(t)
	obs := func(id, path string, v int) error {
		return f.s.Tx(func(tx *journal.Tx) error {
			return tx.StoreObservation(journal.Observation{ID: id, TurnID: "turn1", AttemptID: "attempt1", SnapshotVersion: v, Path: path, Kind: journal.ObservationKindAbsent, ObservedAt: time.Now()})
		})
	}
	if err := obs("o1", "a", 1); err != nil {
		t.Fatal(err)
	}
	if err := obs("o2", "b", 1); err == nil {
		t.Error("two paths must not share a snapshot version")
	}
	if err := obs("o3", "a", 2); err == nil {
		t.Error("a path must not be observed twice in a turn")
	}
	if err := obs("o4", "b", 2); err != nil {
		t.Errorf("distinct path and version: %v", err)
	}
}

func TestStagingKeepsFirstBase(t *testing.T) {
	f := newToolFixture(t)
	h1, h2, base, other := f.put("one"), f.put("two"), f.put("base"), f.put("other")
	f.tx(func(tx *journal.Tx) error {
		if err := tx.StoreStaging("turn1", "a", h1, base); err != nil {
			return err
		}
		if err := tx.StoreStaging("turn1", "a", h2, other); err != nil {
			return err
		}
		return tx.StoreStaging("turn1", "new", h1, "")
	})
	f.restart()
	f.tx(func(tx *journal.Tx) error {
		entries, err := tx.GetStagingEntriesByTurn("turn1")
		if err != nil || len(entries) != 2 {
			t.Fatalf("entries = %v, %v", entries, err)
		}
		if entries[0].Path != "a" || entries[0].BaseSHA256 != base || entries[0].NewSHA256 != h2 {
			t.Errorf("entry a = %+v", entries[0])
		}
		if b, err := tx.GetToolContent(entries[0].BaseSHA256); err != nil || string(b) != "base" {
			t.Errorf("base bytes = %q, %v", b, err)
		}
		if b, err := tx.GetToolContent(entries[0].NewSHA256); err != nil || string(b) != "two" {
			t.Errorf("staged bytes = %q, %v", b, err)
		}
		if entries[1].BaseSHA256 != journal.StagingBaseAbsent {
			t.Errorf("new path base = %q", entries[1].BaseSHA256)
		}
		return nil
	})
}

func TestStagingRejectsUnstoredBase(t *testing.T) {
	f := newToolFixture(t)
	h := f.put("one")
	err := f.s.Tx(func(tx *journal.Tx) error { return tx.StoreStaging("turn1", "a", h, "basehash") })
	if err == nil {
		t.Error("a base that is not stored content must be refused")
	}
}

// TestVerdictEvidenceComesFromTheReviewLoop models the reviewer tool loop: a
// reviewer's tool step (rev1) makes a call and an observation, and its next
// attempt (rev2) gives the verdict. That evidence is accepted; evidence from
// another reviewer, another epoch, or an attempt after the verdict is refused.
func TestVerdictEvidenceComesFromTheReviewLoop(t *testing.T) {
	f := newToolFixture(t)
	h := f.put("args")
	conn := f.db.Conn()
	createTestAttemptIn(t, conn, "other1", "turn1", "reviewer2", 1) // another reviewer's tool step
	createTestAttemptIn(t, conn, "old1", "turn1", "reviewer1", 0)   // the same reviewer, an earlier round
	createTestAttemptIn(t, conn, "rev1", "turn1", "reviewer1", 1)   // tool step
	createTestAttemptIn(t, conn, "rev2", "turn1", "reviewer1", 1)   // verdict
	createTestAttemptIn(t, conn, "rev3", "turn1", "reviewer1", 1)   // after the verdict
	insertTestVerdict(t, conn, "v1", "rev2")

	evidence := []struct{ attempt, call, obs, path string }{
		{"rev1", "c1", "o1", "a"},
		{"other1", "c2", "o2", "b"},
		{"old1", "c3", "o3", "c"},
		{"rev3", "c4", "o4", "d"},
		{"rev2", "c5", "o5", "e"},
	}
	f.tx(func(tx *journal.Tx) error {
		for i, e := range evidence {
			if err := tx.StoreToolCall(journal.ToolCall{ID: e.call, AttemptID: e.attempt, Name: "x", ArgsSHA256: h}); err != nil {
				return err
			}
			if err := tx.StoreToolResult(journal.ToolResult{ID: "r-" + e.call, ToolCallID: e.call, Status: journal.ToolCallStatusExecuted, ResultSHA256: &h, RecordedAt: time.Now()}); err != nil {
				return err
			}
			if err := tx.StoreObservation(journal.Observation{ID: e.obs, TurnID: "turn1", AttemptID: e.attempt, SnapshotVersion: i + 1, Path: e.path, Kind: journal.ObservationKindAbsent, ObservedAt: time.Now()}); err != nil {
				return err
			}
		}
		return nil
	})

	f.tx(func(tx *journal.Tx) error {
		if rid, err := tx.StoreVerdictToolResult("v1", "c1"); err != nil || rid != "r-c1" {
			t.Errorf("tool step result as evidence = %q, %v", rid, err)
		}
		if err := tx.StoreVerdictObservation("v1", "o1"); err != nil {
			t.Errorf("tool step observation as evidence: %v", err)
		}
		return nil
	})
	for _, e := range evidence[1:] {
		if err := f.s.Tx(func(tx *journal.Tx) error { _, err := tx.StoreVerdictToolResult("v1", e.call); return err }); err == nil {
			t.Errorf("result from attempt %s must not be evidence for v1", e.attempt)
		}
		if err := f.s.Tx(func(tx *journal.Tx) error { return tx.StoreVerdictObservation("v1", e.obs) }); err == nil {
			t.Errorf("observation from attempt %s must not be evidence for v1", e.attempt)
		}
	}
	f.tx(func(tx *journal.Tx) error {
		ids, err := tx.GetVerdictToolResults("v1")
		if err != nil || len(ids) != 1 || ids[0] != "r-c1" {
			t.Errorf("verdict tool results = %v, %v", ids, err)
		}
		obs, err := tx.GetVerdictObservations("v1")
		if err != nil || len(obs) != 1 || obs[0] != "o1" {
			t.Errorf("verdict observations = %v, %v", obs, err)
		}
		return nil
	})
}

func TestEvidenceRecordsMatchTheirKind(t *testing.T) {
	f := newToolFixture(t)
	h := f.put("x")
	refused := []struct {
		name string
		fn   func(tx *journal.Tx) error
	}{
		{"executed result without content", func(tx *journal.Tx) error {
			if err := tx.StoreToolCall(journal.ToolCall{ID: "c1", AttemptID: "attempt1", Name: "x", ArgsSHA256: h}); err != nil {
				return err
			}
			return tx.StoreToolResult(journal.ToolResult{ID: "r1", ToolCallID: "c1", Status: journal.ToolCallStatusExecuted, RecordedAt: time.Now()})
		}},
		{"file observation without content", func(tx *journal.Tx) error {
			return tx.StoreObservation(journal.Observation{ID: "o1", TurnID: "turn1", AttemptID: "attempt1", SnapshotVersion: 1, Path: "a", Kind: journal.ObservationKindFile, ObservedAt: time.Now()})
		}},
		{"absence with content", func(tx *journal.Tx) error {
			return tx.StoreObservation(journal.Observation{ID: "o1", TurnID: "turn1", AttemptID: "attempt1", SnapshotVersion: 1, Path: "a", Kind: journal.ObservationKindAbsent, BaseSHA256: &h, ObservedAt: time.Now()})
		}},
		{"directory without entries", func(tx *journal.Tx) error {
			return tx.StoreObservation(journal.Observation{ID: "o1", TurnID: "turn1", AttemptID: "attempt1", SnapshotVersion: 1, Path: "a", Kind: journal.ObservationKindDirectory, BaseSHA256: &h, ObservedAt: time.Now()})
		}},
	}
	for _, r := range refused {
		if err := f.s.Tx(r.fn); err == nil {
			t.Errorf("%s must be refused", r.name)
		}
	}
	f.tx(func(tx *journal.Tx) error {
		if err := tx.StoreToolCall(journal.ToolCall{ID: "c1", AttemptID: "attempt1", Name: "x", ArgsSHA256: h}); err != nil {
			return err
		}
		return tx.StoreToolResult(journal.ToolResult{ID: "r1", ToolCallID: "c1", Status: journal.ToolCallStatusError, RecordedAt: time.Now()})
	})
}
