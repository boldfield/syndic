package journal_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/candidate"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
)

// toolsFixture is one review turn with an author attempt and two reviewers,
// each with a tool-step attempt, as the section 6 tool loops produce. The
// reviewer's verdict attempt and verdict follow its tool step in time, so a
// test records them with giveVerdict after the step's results.
type toolsFixture struct {
	run, turn string
	author    string            // author tool-step attempt
	step      map[string]string // reviewer -> tool-step attempt
	verdictAt map[string]string // reviewer -> verdict attempt
	verdict   map[string]string // reviewer -> verdict id
	candidate string
}

func buildToolsTurn(t *testing.T, store *journal.Store) toolsFixture {
	t.Helper()
	f := toolsFixture{step: map[string]string{}, verdictAt: map[string]string{}, verdict: map[string]string{}}
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		if f.run, _, err = tx.CreateRun(json.RawMessage(`{}`)); err != nil {
			return err
		}
		if f.turn, err = tx.CreateTurn(f.run, "review", nil); err != nil {
			return err
		}
		if f.author, err = tx.BeginAttempt(f.turn, "author", "author", 1, nil, ""); err != nil {
			return err
		}
		if f.candidate, err = tx.SetLatestCandidate(f.turn, "answer", "h0", 0, ""); err != nil {
			return err
		}
		for _, r := range []string{"r1", "r2"} {
			if f.step[r], err = tx.BeginAttempt(f.turn, r, "review", 1, nil, ""); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// giveVerdict is the reviewer's next attempt after its tool step: it begins
// the verdict attempt and records the verdict, in the order the loop does.
func giveVerdict(t *testing.T, store *journal.Store, f *toolsFixture, reviewer string) {
	t.Helper()
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		if f.verdictAt[reviewer], err = tx.BeginAttempt(f.turn, reviewer, "review", 1, nil, ""); err != nil {
			return err
		}
		f.verdict[reviewer], err = tx.RecordVerdict(f.verdictAt[reviewer], "h0", "approve", "", "valid")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// verdictSeq reads the journal sequence a verdict took when it was recorded.
func verdictSeq(t *testing.T, db *journal.DB, verdictID string) int64 {
	t.Helper()
	var seq int64
	if err := db.Conn().QueryRow(`SELECT journal_seq FROM verdicts WHERE id = ?`, verdictID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func shaBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func eventKinds(t *testing.T, store *journal.Store, since int64) []string {
	t.Helper()
	events, err := store.EventsSince(since, 1000)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(events))
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// A tool call journaled as accepted intent later gains exactly one result;
// its status, hash and length come from that result, and the arguments and
// result bytes are stored by hash.
func TestToolCallLifecycle(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	args := []byte(`{"path":"main.go"}`)
	result := []byte("package main\n")

	var call journal.ToolCall
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		call, err = tx.RecordToolCall(f.author, 0, "call_abc", "read_file", "read", args)
		if err != nil {
			return err
		}
		got, err := tx.GetToolCall(call.ID)
		if err != nil {
			return err
		}
		if got.Status != journal.ToolCallAccepted || got.Result != nil || got.ArgsSHA256 != shaBytes(args) || got.ArgsByteLen != int64(len(args)) ||
			got.TurnID != f.turn || got.Participant != "author" || got.ProviderCallID != "call_abc" || got.Class != "read" {
			return fmt.Errorf("accepted call = %+v", got)
		}
		res, err := tx.RecordToolResult(call.ID, journal.ToolCallExecuted, result, "")
		if err != nil {
			return err
		}
		if res.ResultSHA256 != shaBytes(result) || res.ResultByteLen != int64(len(result)) {
			return fmt.Errorf("result = %+v", res)
		}
		got, err = tx.GetToolCall(call.ID)
		if err != nil {
			return err
		}
		if got.Status != journal.ToolCallExecuted || got.Result == nil || got.Result.ID != res.ID || got.Result.ResultSHA256 != shaBytes(result) {
			return fmt.Errorf("executed call = %+v", got)
		}
		if _, err := tx.RecordToolResult(call.ID, journal.ToolCallError, []byte("late"), "epoch"); err == nil {
			return fmt.Errorf("second result accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if kinds := eventKinds(t, store, 0); strings.Join(kinds, ",") != "tool_call_accepted,tool_result" {
		t.Fatalf("events = %v", kinds)
	}

	// The error and abandoned forms, and their rules.
	err = store.Tx(func(tx *journal.Tx) error {
		c1, err := tx.RecordToolCall(f.author, 1, "call_err", "web_fetch", "web", []byte(`{"url":"http://x"}`))
		if err != nil {
			return err
		}
		if _, err := tx.RecordToolResult(c1.ID, journal.ToolCallError, []byte("denied"), ""); err == nil {
			return fmt.Errorf("error without code accepted")
		}
		res, err := tx.RecordToolResult(c1.ID, journal.ToolCallError, []byte("denied: referee"), "referee_deny")
		if err != nil {
			return err
		}
		if res.ErrorCode != "referee_deny" || res.ResultSHA256 != shaBytes([]byte("denied: referee")) {
			return fmt.Errorf("error result = %+v", res)
		}
		c2, err := tx.RecordToolCall(f.author, 2, "call_ab", "read_file", "read", []byte(`{}`))
		if err != nil {
			return err
		}
		if _, err := tx.RecordToolResult(c2.ID, journal.ToolCallAbandoned, []byte("x"), ""); err == nil {
			return fmt.Errorf("abandoned with content accepted")
		}
		if _, err := tx.RecordToolResult(c2.ID, journal.ToolCallAbandoned, nil, ""); err != nil {
			return err
		}
		if _, err := tx.RecordToolResult(c2.ID, journal.ToolCallExecuted, []byte("x"), "x"); err == nil {
			return fmt.Errorf("executed with error code accepted")
		}
		if _, err := tx.RecordToolCall(f.author, 3, "call_bad", "exec", "shell", []byte(`{}`)); err == nil {
			return fmt.Errorf("unknown class accepted")
		}
		if _, err := tx.RecordToolCall(f.author, 0, "call_dup", "read_file", "read", []byte(`{}`)); err == nil {
			return fmt.Errorf("duplicate call index accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls, err := store.ToolCallsByTurn(f.turn, "author")
	if err != nil || len(calls) != 3 {
		t.Fatalf("calls = %d, %v", len(calls), err)
	}
	if calls[0].Status != "executed" || calls[1].Status != "error" || calls[2].Status != "abandoned" {
		t.Fatalf("statuses = %s %s %s", calls[0].Status, calls[1].Status, calls[2].Status)
	}

	// The schema refuses an executed result without its length or hash, and a
	// probe attempt cannot own a call.
	c := db.Conn()
	if _, err := c.Exec(`INSERT INTO tool_calls (id, attempt_id, turn_id, participant, call_index, provider_call_id, name, class, args_sha256, recorded_at)
		VALUES ('rawcall', ?, ?, 'author', 9, 'raw', 'read_file', 'read', ?, 'now')`, f.author, f.turn, shaBytes(args)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(`INSERT INTO tool_results (id, tool_call_id, status, result_sha256, result_byte_len, recorded_at)
		VALUES ('bad', 'rawcall', 'executed', ?, NULL, 'now')`, shaBytes(result)); err == nil {
		t.Fatal("executed result without byte length accepted")
	}
	if _, err := c.Exec(`INSERT INTO tool_results (id, tool_call_id, status, recorded_at) VALUES ('bad', 'rawcall', 'executed', 'now')`); err == nil {
		t.Fatal("executed result without hash accepted")
	}
	if _, err := c.Exec(`INSERT INTO tool_results (id, tool_call_id, status, result_sha256, result_byte_len, recorded_at)
		VALUES ('bad', 'rawcall', 'error', ?, 1, 'now')`, shaBytes(result)); err == nil {
		t.Fatal("error result without code accepted")
	}
	if _, err := c.Exec(`INSERT INTO tool_results (id, tool_call_id, status, result_sha256, result_byte_len, recorded_at)
		VALUES ('bad', 'rawcall', 'executed', 'nosuchhash', 1, 'now')`); err == nil {
		t.Fatal("result hash outside the content store accepted")
	}
	if _, err := c.Exec(`UPDATE tool_calls SET name = 'x' WHERE id = ?`, call.ID); err == nil {
		t.Fatal("tool_calls update succeeded")
	}
	if _, err := c.Exec(`INSERT INTO attempts (id, participant, phase, epoch, dispatched_at, probe_run_id, probe_endpoint, probe_model, probe_pool, probe_group, probe_step)
		VALUES ('probe1', 'p', 'probe', 1, '2026-01-01T00:00:00Z', ?, 'ep', 'm', 'pool', '[]', 'tools')`, f.run); err != nil {
		t.Fatal(err)
	}
	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordToolCall("probe1", 0, "c", "read_file", "read", nil)
		return err
	})
	if err == nil {
		t.Fatal("probe attempt owned a tool call")
	}
}

// A failed transaction drops the records and the events they emitted
// together, whether fn returns an error after a successful record or a
// constraint fails midway.
func TestAtomicRecordAndEventRollback(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	before, err := store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	countRows := func(table string) int {
		var n int
		if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	sentinel := errors.New("caller aborts")
	err = store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.RecordToolCall(f.author, 0, "c0", "read_file", "read", []byte(`{}`)); err != nil {
			return err
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "a.txt", journal.ObservationFile, []byte("A")); err != nil {
			return err
		}
		if _, err := tx.StageWrite(f.turn, "a.txt", []byte("B")); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx error = %v", err)
	}
	after, err := store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("events after rollback: seq %d -> %d", before, after)
	}
	if kinds := eventKinds(t, store, before); len(kinds) != 0 {
		t.Fatalf("events survived rollback: %v", kinds)
	}
	for _, table := range []string{"tool_calls", "observations", "staging", "tool_content"} {
		if n := countRows(table); n != 0 {
			t.Fatalf("%s rows survived rollback: %d", table, n)
		}
	}

	// A constraint failure on the second record rolls back the first record
	// and its event too.
	err = store.Tx(func(tx *journal.Tx) error {
		call, err := tx.RecordToolCall(f.author, 0, "c0", "read_file", "read", []byte(`{}`))
		if err != nil {
			return err
		}
		if _, err := tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte("ok"), ""); err != nil {
			return err
		}
		_, err = tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte("again"), "")
		return err
	})
	if err == nil {
		t.Fatal("second result accepted")
	}
	if n := countRows("tool_calls") + countRows("tool_results"); n != 0 {
		t.Fatalf("rows survived failed tx: %d", n)
	}
	if kinds := eventKinds(t, store, before); len(kinds) != 0 {
		t.Fatalf("events survived failed tx: %v", kinds)
	}

	// The same sequence committed leaves the records and their events, in order.
	err = store.Tx(func(tx *journal.Tx) error {
		call, err := tx.RecordToolCall(f.author, 0, "c0", "read_file", "read", []byte(`{}`))
		if err != nil {
			return err
		}
		if _, err := tx.RecordToolResult(call.ID, journal.ToolCallExecuted, []byte("ok"), ""); err != nil {
			return err
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "a.txt", journal.ObservationFile, []byte("A")); err != nil {
			return err
		}
		if _, err := tx.StageWrite(f.turn, "a.txt", []byte("B")); err != nil {
			return err
		}
		return tx.Unstage(f.turn, "a.txt")
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "tool_call_accepted,tool_result,observation,staged,unstaged"
	if kinds := eventKinds(t, store, before); strings.Join(kinds, ",") != want {
		t.Fatalf("events = %v", kinds)
	}
	events, err := store.EventsSince(before, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.TurnID != f.turn {
			t.Fatalf("event %s on turn %q, want %q", e.Kind, e.TurnID, f.turn)
		}
	}
	var staged struct {
		Path       string `json:"path"`
		BaseSHA256 string `json:"base_sha256"`
		NewSHA256  string `json:"new_sha256"`
	}
	if err := json.Unmarshal(events[3].Data, &staged); err != nil {
		t.Fatal(err)
	}
	if staged.Path != "a.txt" || staged.BaseSHA256 != shaBytes([]byte("A")) || staged.NewSHA256 != shaBytes([]byte("B")) {
		t.Fatalf("staged payload = %+v", staged)
	}
}

// Every piece of evidence (arguments, results, observed contents and
// listings, staged content and its base) is rebuilt byte for byte from its
// hash after the database is closed and reopened, through Content and through
// ContentRange's content ref.
func TestHashAddressedEvidenceSurvivesRestart(t *testing.T) {
	path := t.TempDir() + "/tools.db"
	db, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)

	args := []byte(`{"path":"src/a.go"}`)
	result := []byte("package a\n\nfunc A() {}\n")
	base := []byte("package a\n")
	listing := []byte("f 4\na.go\nd 3\nsub\n")
	staged := []byte("package a\n\nfunc B() {}\n")
	binary := []byte{0, 1, 2, 0xff, 0xfe, '\n'}

	var callID string
	var obsFile, obsDir, obsAbsent journal.Observation
	err = store.Tx(func(tx *journal.Tx) error {
		call, err := tx.RecordToolCall(f.author, 0, "c0", "read_file", "read", args)
		if err != nil {
			return err
		}
		callID = call.ID
		if _, err := tx.RecordToolResult(call.ID, journal.ToolCallExecuted, result, ""); err != nil {
			return err
		}
		if obsFile, err = tx.RecordObservation(f.turn, f.author, "src/a.go", journal.ObservationFile, base); err != nil {
			return err
		}
		if obsDir, err = tx.RecordObservation(f.turn, f.author, "src", journal.ObservationListing, listing); err != nil {
			return err
		}
		if obsAbsent, err = tx.RecordObservation(f.turn, f.author, "src/new.go", journal.ObservationAbsent, nil); err != nil {
			return err
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "bin\xff", journal.ObservationFile, binary); err != nil {
			return err
		}
		if _, err := tx.StageWrite(f.turn, "src/a.go", staged); err != nil {
			return err
		}
		if _, err := tx.StageWrite(f.turn, "src/new.go", []byte("created")); err != nil {
			return err
		}
		_, err = tx.FreezeChangeSet(f.turn, f.candidate, "snaphash", "changeshash")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store = journal.NewStore(db)

	rebuild := func(hash string, want []byte) {
		t.Helper()
		got, err := store.Content(hash)
		if err != nil {
			t.Fatalf("content %s: %v", hash, err)
		}
		if string(got) != string(want) || shaBytes(got) != hash {
			t.Fatalf("content %s = %q, want %q", hash, got, want)
		}
		data, total, sum, err := store.ContentRange("content:"+hash, 0, 1<<20)
		if err != nil {
			t.Fatalf("content range %s: %v", hash, err)
		}
		if string(data) != string(want) || total != int64(len(want)) || sum != hash {
			t.Fatalf("content range %s = %q total %d sum %s", hash, data, total, sum)
		}
	}

	calls, err := store.ToolCallsByTurn(f.turn, "")
	if err != nil || len(calls) != 1 || calls[0].ID != callID {
		t.Fatalf("calls = %+v, %v", calls, err)
	}
	rebuild(calls[0].ArgsSHA256, args)
	if calls[0].Result == nil || calls[0].Result.ResultByteLen != int64(len(result)) {
		t.Fatalf("result = %+v", calls[0].Result)
	}
	rebuild(calls[0].Result.ResultSHA256, result)

	obs, err := store.Observations(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted bytewise by path: "bin\xff" < "src" < "src/a.go" < "src/new.go".
	if len(obs) != 4 || obs[0].Path != "bin\xff" || obs[1].Path != "src" || obs[2].Path != "src/a.go" || obs[3].Path != "src/new.go" {
		t.Fatalf("observations = %+v", obs)
	}
	if obs[2].ID != obsFile.ID || obs[2].SnapshotVersion != 1 || obs[1].ID != obsDir.ID || obs[1].SnapshotVersion != 2 ||
		obs[3].ID != obsAbsent.ID || obs[3].SnapshotVersion != 3 || obs[0].SnapshotVersion != 4 {
		t.Fatalf("versions = %+v", obs)
	}
	rebuild(obs[2].ContentSHA256, base)
	rebuild(obs[1].ContentSHA256, listing)
	rebuild(obs[0].ContentSHA256, binary)
	if obs[3].ContentSHA256 != "" || obs[3].Kind != journal.ObservationAbsent {
		t.Fatalf("absent observation = %+v", obs[3])
	}

	entries, err := store.Staging(f.turn)
	if err != nil || len(entries) != 2 {
		t.Fatalf("staging = %+v, %v", entries, err)
	}
	if entries[0].Path != "src/a.go" || entries[0].BaseSHA256 != shaBytes(base) || entries[1].Path != "src/new.go" || entries[1].BaseSHA256 != "" {
		t.Fatalf("staging = %+v", entries)
	}
	rebuild(entries[0].BaseSHA256, base)
	rebuild(entries[0].NewSHA256, staged)
	rebuild(entries[1].NewSHA256, []byte("created"))

	cs, err := store.ChangeSetForTurn(f.turn)
	if err != nil {
		t.Fatal(err)
	}
	if cs.SnapshotVersion != 4 || cs.SnapshotHash != "snaphash" || cs.ChangesHash != "changeshash" || cs.State != journal.ChangeSetAppliable || len(cs.Paths) != 2 {
		t.Fatalf("change set = %+v", cs)
	}
	if cs.Paths[0].Op != "modify" || cs.Paths[0].BaseSHA256 != shaBytes(base) || cs.Paths[0].NewLen != int64(len(staged)) || cs.Paths[0].Outcome != journal.PathPending {
		t.Fatalf("path 0 = %+v", cs.Paths[0])
	}
	if cs.Paths[1].Op != "create" || cs.Paths[1].BaseSHA256 != "" || cs.Paths[1].NewLen != 7 {
		t.Fatalf("path 1 = %+v", cs.Paths[1])
	}
	// The diff a reviewer saw can be regenerated: base and new both resolve.
	rebuild(cs.Paths[0].BaseSHA256, base)
	rebuild(cs.Paths[0].NewSHA256, staged)

	if _, err := store.Content(shaBytes([]byte("never stored"))); !errors.Is(err, journal.ErrNoSuchContent) {
		t.Fatalf("missing content error = %v", err)
	}
	if _, _, _, err := store.ContentRange("content:"+shaBytes([]byte("never stored")), 0, 10); !errors.Is(err, journal.ErrNoSuchContent) {
		t.Fatalf("missing content range error = %v", err)
	}
}

// The snapshot record holds one observation per path at a version unique in
// the turn; a second observation of a path, a foreign attempt and an absence
// with content are refused.
func TestSnapshotRecordInvariants(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	other := buildToolsTurn(t, store)

	err := store.Tx(func(tx *journal.Tx) error {
		if v, err := tx.SnapshotVersion(f.turn); err != nil || v != 0 {
			return fmt.Errorf("initial version = %d, %v", v, err)
		}
		o1, err := tx.RecordObservation(f.turn, f.author, "p", journal.ObservationFile, []byte("one"))
		if err != nil {
			return err
		}
		if _, err := tx.RecordObservation(f.turn, f.step["r1"], "p", journal.ObservationFile, []byte("two")); err == nil {
			return fmt.Errorf("second observation of p accepted")
		}
		if _, err := tx.RecordObservation(f.turn, f.step["r1"], "p", journal.ObservationAbsent, nil); err == nil {
			return fmt.Errorf("contradicting absence accepted")
		}
		if _, err := tx.RecordObservation(f.turn, other.author, "q", journal.ObservationFile, []byte("x")); err == nil {
			return fmt.Errorf("attempt of another turn accepted")
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "q", journal.ObservationAbsent, []byte("x")); err == nil {
			return fmt.Errorf("absence with content accepted")
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "q", "x", nil); err == nil {
			return fmt.Errorf("unknown kind accepted")
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "", journal.ObservationAbsent, nil); err == nil {
			return fmt.Errorf("empty path accepted")
		}
		o2, err := tx.RecordObservation(f.turn, f.step["r1"], "q", journal.ObservationAbsent, nil)
		if err != nil {
			return err
		}
		if o1.SnapshotVersion != 1 || o2.SnapshotVersion != 2 {
			return fmt.Errorf("versions = %d %d", o1.SnapshotVersion, o2.SnapshotVersion)
		}
		got, err := tx.ObservationAt(f.turn, "p")
		if err != nil || got.ID != o1.ID || got.ContentSHA256 != shaBytes([]byte("one")) {
			return fmt.Errorf("observation at p = %+v, %v", got, err)
		}
		if _, err := tx.ObservationAt(f.turn, "zz"); !errors.Is(err, journal.ErrNotObserved) {
			return fmt.Errorf("unobserved path error = %v", err)
		}
		// Another turn's record is a separate namespace.
		if _, err := tx.RecordObservation(other.turn, other.author, "p", journal.ObservationFile, []byte("other")); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := db.Conn()
	if _, err := c.Exec(`UPDATE observations SET kind = 'a', content_sha256 = NULL WHERE turn_id = ?`, f.turn); err == nil {
		t.Fatal("observation update succeeded")
	}
	if _, err := c.Exec(`DELETE FROM observations WHERE turn_id = ?`, f.turn); err == nil {
		t.Fatal("observation delete succeeded")
	}
	if _, err := c.Exec(`INSERT INTO observations (id, turn_id, attempt_id, snapshot_version, path, kind, content_sha256, recorded_at)
		VALUES ('x', ?, ?, 1, X'7A', 'a', NULL, 'now')`, f.turn, f.author); err == nil {
		t.Fatal("duplicate snapshot version accepted by schema")
	}
}

// Staging requires an observed path, fixes the base at first staging, keeps
// it when re-staged, and forgets the path on unstage; directories cannot be
// staged. Every change is journaled.
func TestStagingKeepsFirstBase(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	before, _ := store.MaxSeq()

	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.StageWrite(f.turn, "a", []byte("new")); !errors.Is(err, journal.ErrNotObserved) {
			return fmt.Errorf("unobserved stage error = %v", err)
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "a", journal.ObservationFile, []byte("base")); err != nil {
			return err
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "dir", journal.ObservationListing, []byte("")); err != nil {
			return err
		}
		if _, err := tx.RecordObservation(f.turn, f.author, "n", journal.ObservationAbsent, nil); err != nil {
			return err
		}
		if _, err := tx.StageWrite(f.turn, "dir", []byte("x")); err == nil {
			return fmt.Errorf("staging a directory accepted")
		}
		e1, err := tx.StageWrite(f.turn, "a", []byte("v1"))
		if err != nil {
			return err
		}
		e2, err := tx.StageWrite(f.turn, "a", []byte("v2"))
		if err != nil {
			return err
		}
		if e1.BaseSHA256 != shaBytes([]byte("base")) || e2.BaseSHA256 != e1.BaseSHA256 || e2.NewSHA256 != shaBytes([]byte("v2")) || !e2.StagedAt.Equal(e1.StagedAt) {
			return fmt.Errorf("entries = %+v %+v", e1, e2)
		}
		n, err := tx.StageWrite(f.turn, "n", []byte("created"))
		if err != nil {
			return err
		}
		if n.BaseSHA256 != "" {
			return fmt.Errorf("absent base = %q", n.BaseSHA256)
		}
		entries, err := tx.Staging(f.turn)
		if err != nil || len(entries) != 2 || entries[0].Path != "a" || entries[1].Path != "n" {
			return fmt.Errorf("staging = %+v, %v", entries, err)
		}
		if err := tx.Unstage(f.turn, "n"); err != nil {
			return err
		}
		if err := tx.Unstage(f.turn, "n"); !errors.Is(err, journal.ErrNotStaged) {
			return fmt.Errorf("double unstage error = %v", err)
		}
		entries, err = tx.Staging(f.turn)
		if err != nil || len(entries) != 1 || entries[0].Path != "a" || entries[0].NewSHA256 != shaBytes([]byte("v2")) {
			return fmt.Errorf("staging after unstage = %+v, %v", entries, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "observation,observation,observation,staged,staged,staged,unstaged"
	if kinds := eventKinds(t, store, before); strings.Join(kinds, ",") != want {
		t.Fatalf("events = %v", kinds)
	}
	if _, err := db.Conn().Exec(`UPDATE staging SET base_sha256 = NULL WHERE turn_id = ?`, f.turn); err == nil {
		t.Fatal("staging base rewrite succeeded")
	}
}

// A change set freezes the staging map against the turn's candidate at the
// snapshot version it rested on; its state and per-path outcomes follow the
// append-only apply log through every boundary section 6 names.
func TestChangeSetApplyStateFromLog(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	other := buildToolsTurn(t, store)

	var csID string
	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.FreezeChangeSet(f.turn, f.candidate, "s", "c"); !errors.Is(err, journal.ErrNoChangeSet) {
			return fmt.Errorf("empty freeze error = %v", err)
		}
		for _, p := range []string{"a", "b", "c"} {
			if _, err := tx.RecordObservation(f.turn, f.author, p, journal.ObservationFile, []byte("base "+p)); err != nil {
				return err
			}
			if _, err := tx.StageWrite(f.turn, p, []byte("new "+p)); err != nil {
				return err
			}
		}
		if _, err := tx.FreezeChangeSet(f.turn, other.candidate, "s", "c"); err == nil {
			return fmt.Errorf("foreign candidate accepted")
		}
		if _, err := tx.FreezeChangeSet(f.turn, f.candidate, "", "c"); err == nil {
			return fmt.Errorf("missing snapshot hash accepted")
		}
		cs, err := tx.FreezeChangeSet(f.turn, f.candidate, "snap", "changes")
		if err != nil {
			return err
		}
		csID = cs.ID
		if cs.SnapshotVersion != 3 || len(cs.Paths) != 3 || cs.State != journal.ChangeSetAppliable {
			return fmt.Errorf("frozen = %+v", cs)
		}
		if _, err := tx.FreezeChangeSet(f.turn, f.candidate, "snap", "changes"); err == nil {
			return fmt.Errorf("second change set for one candidate accepted")
		}
		if _, err := tx.ChangeSetForTurn(other.turn); !errors.Is(err, journal.ErrNoChangeSet) {
			return fmt.Errorf("turn without change set error = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	state := func() (string, map[string]string) {
		t.Helper()
		cs, err := store.GetChangeSet(csID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, p := range cs.Paths {
			out[p.Path] = p.Outcome
		}
		return cs.State, out
	}
	in := func(fn func(tx *journal.Tx) error) error { return store.Tx(fn) }

	// Nothing started: appliable, every path pending; file ops and finish are refused.
	if s, o := state(); s != journal.ChangeSetAppliable || o["a"] != journal.PathPending {
		t.Fatalf("initial = %s %v", s, o)
	}
	if err := in(func(tx *journal.Tx) error { return tx.RecordApplyFile(csID, "a", journal.PathApplied) }); !errors.Is(err, journal.ErrChangeSetState) {
		t.Fatalf("file op before start error = %v", err)
	}
	if err := in(func(tx *journal.Tx) error { _, err := tx.FinishApply(csID); return err }); !errors.Is(err, journal.ErrChangeSetState) {
		t.Fatalf("finish before start error = %v", err)
	}

	// Crash before the first rename: started with no file op is partial.
	if err := in(func(tx *journal.Tx) error { return tx.BeginApply(csID, false) }); err != nil {
		t.Fatal(err)
	}
	if s, o := state(); s != journal.ChangeSetPartial || o["a"] != journal.PathPending {
		t.Fatalf("started only = %s %v", s, o)
	}
	if err := in(func(tx *journal.Tx) error { return tx.BeginApply(csID, false) }); err != nil {
		t.Fatalf("retry on partial: %v", err)
	}
	// One file renamed, then a crash: recovery records partial with per-path outcomes.
	if err := in(func(tx *journal.Tx) error { return tx.RecordApplyFile(csID, "a", journal.PathApplied) }); err != nil {
		t.Fatal(err)
	}
	if err := in(func(tx *journal.Tx) error { return tx.RecordApplyFile(csID, "zz", journal.PathApplied) }); err == nil {
		t.Fatal("path outside the change set accepted")
	}
	if err := in(func(tx *journal.Tx) error { return tx.RecordApplyRecovered(csID) }); err != nil {
		t.Fatal(err)
	}
	if s, o := state(); s != journal.ChangeSetPartial || o["a"] != journal.PathApplied || o["b"] != journal.PathPending {
		t.Fatalf("recovered = %s %v", s, o)
	}
	if err := in(func(tx *journal.Tx) error { return tx.RecordApplyRecovered(csID) }); !errors.Is(err, journal.ErrChangeSetState) {
		t.Fatalf("second recovery error = %v", err)
	}
	// Retry: b applied, c conflicts, finished records conflict.
	err = in(func(tx *journal.Tx) error {
		if err := tx.BeginApply(csID, false); err != nil {
			return err
		}
		if err := tx.RecordApplyFile(csID, "b", journal.PathApplied); err != nil {
			return err
		}
		if err := tx.RecordApplyFile(csID, "c", journal.PathConflict); err != nil {
			return err
		}
		st, err := tx.FinishApply(csID)
		if err != nil {
			return err
		}
		if st != journal.ChangeSetConflict {
			return fmt.Errorf("finished state = %s", st)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s, o := state(); s != journal.ChangeSetConflict || o["b"] != journal.PathApplied || o["c"] != journal.PathConflict {
		t.Fatalf("after conflict = %s %v", s, o)
	}
	// A retry after conflict that crashes before finishing is partial again,
	// whatever the earlier finished op said.
	if err := in(func(tx *journal.Tx) error { return tx.BeginApply(csID, false) }); err != nil {
		t.Fatal(err)
	}
	if s, _ := state(); s != journal.ChangeSetPartial {
		t.Fatalf("retry after conflict = %s", s)
	}
	// The conflicting file now applies: finished records applied, and an
	// applied change set admits no apply and no discard.
	err = in(func(tx *journal.Tx) error {
		if err := tx.RecordApplyFile(csID, "c", journal.PathApplied); err != nil {
			return err
		}
		st, err := tx.FinishApply(csID)
		if st != journal.ChangeSetApplied {
			return fmt.Errorf("finished state = %s, %v", st, err)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if s, o := state(); s != journal.ChangeSetApplied || o["c"] != journal.PathApplied {
		t.Fatalf("applied = %s %v", s, o)
	}
	if err := in(func(tx *journal.Tx) error { return tx.BeginApply(csID, false) }); !errors.Is(err, journal.ErrChangeSetState) {
		t.Fatalf("apply of applied error = %v", err)
	}
	if err := in(func(tx *journal.Tx) error { return tx.DiscardChangeSet(csID) }); !errors.Is(err, journal.ErrChangeSetState) {
		t.Fatalf("discard of applied error = %v", err)
	}
	ops, err := store.ApplyOps(csID)
	if err != nil {
		t.Fatal(err)
	}
	wantOps := "started,started,file:a=applied,recovered=partial,started,file:b=applied,file:c=conflict,finished=conflict,started,file:c=applied,finished=applied"
	if got := describeOps(ops); got != wantOps {
		t.Fatalf("ops = %s", got)
	}
	kinds := eventKinds(t, store, 0)
	joined := strings.Join(kinds, ",")
	for _, k := range []string{"change_set_frozen", "apply_started", "apply_file", "apply_recovered", "apply_finished"} {
		if !strings.Contains(joined, k) {
			t.Fatalf("missing event %s in %v", k, kinds)
		}
	}
	if _, err := db.Conn().Exec(`DELETE FROM apply_ops`); err == nil {
		t.Fatal("apply_ops delete succeeded")
	}
}

// A change set discarded after a partial apply keeps the outcomes of the paths
// already applied, under human override or not, and a discarded change set
// admits nothing further.
func TestDiscardKeepsPerPathOutcomes(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	var csID string
	err := store.Tx(func(tx *journal.Tx) error {
		for _, p := range []string{"x", "y", "z"} {
			if _, err := tx.RecordObservation(f.turn, f.author, p, journal.ObservationAbsent, nil); err != nil {
				return err
			}
			if _, err := tx.StageWrite(f.turn, p, []byte(p)); err != nil {
				return err
			}
		}
		cs, err := tx.FreezeChangeSet(f.turn, f.candidate, "s", "c")
		if err != nil {
			return err
		}
		csID = cs.ID
		if err := tx.BeginApply(cs.ID, true); err != nil {
			return err
		}
		if err := tx.RecordApplyFile(cs.ID, "x", journal.PathApplied); err != nil {
			return err
		}
		if err := tx.RecordApplyFile(cs.ID, "y", journal.PathApplied); err != nil {
			return err
		}
		return tx.DiscardChangeSet(cs.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := store.GetChangeSet(csID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.State != journal.ChangeSetDiscarded || cs.Paths[0].Outcome != journal.PathApplied || cs.Paths[1].Outcome != journal.PathApplied || cs.Paths[2].Outcome != journal.PathPending {
		t.Fatalf("discarded = %+v", cs)
	}
	for _, p := range cs.Paths {
		if p.Op != "create" || p.BaseSHA256 != "" {
			t.Fatalf("path = %+v", p)
		}
	}
	for name, fn := range map[string]func(tx *journal.Tx) error{
		"apply":   func(tx *journal.Tx) error { return tx.BeginApply(csID, false) },
		"discard": func(tx *journal.Tx) error { return tx.DiscardChangeSet(csID) },
		"file":    func(tx *journal.Tx) error { return tx.RecordApplyFile(csID, "z", journal.PathApplied) },
	} {
		if err := store.Tx(fn); !errors.Is(err, journal.ErrChangeSetState) {
			t.Fatalf("%s on discarded error = %v", name, err)
		}
	}
	ops, err := store.ApplyOps(csID)
	if err != nil || !ops[0].Override {
		t.Fatalf("override not recorded: %+v, %v", ops, err)
	}
	// Bases, contents and the change set itself stay in the journal.
	for _, p := range cs.Paths {
		if _, err := store.Content(p.NewSHA256); err != nil {
			t.Fatalf("content %s gone: %v", p.NewSHA256, err)
		}
	}
}

func describeOps(ops []journal.ApplyOp) string {
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		switch op.OpType {
		case "file":
			parts = append(parts, "file:"+op.Path+"="+op.Outcome)
		case "started":
			parts = append(parts, "started")
		default:
			parts = append(parts, op.OpType+"="+op.State)
		}
	}
	return strings.Join(parts, ",")
}

// A verdict's evidence is the reviewer's own results and observations from
// its tool-step attempts in the same turn, recorded before the verdict's
// attempt; another reviewer's, another turn's, a later attempt's, an
// unfinished call's, and an observation beyond the version seen are refused,
// and an observation binds to one verdict.
// The real sequence: each reviewer's tool step records its calls, receives
// its results and adds its observations; the reviewer's next attempt then
// gives the verdict. Evidence must come from the verdict's own reviewer and
// turn, and must have been received before the verdict was recorded.
func TestVerdictEvidenceScope(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	other := buildToolsTurn(t, store)

	var r1Result, r1Unfinished, r2Result, otherResult string
	var r1ResultSeq int64
	var r1Obs, r2Obs, authorObs journal.Observation
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		if authorObs, err = tx.RecordObservation(f.turn, f.author, "main.go", journal.ObservationFile, []byte("m")); err != nil {
			return err
		}
		mk := func(attempt string, idx int, result bool) (journal.ToolResult, error) {
			c, err := tx.RecordToolCall(attempt, idx, fmt.Sprintf("c%d", idx), "read_file", "read", []byte(`{}`))
			if err != nil {
				return journal.ToolResult{}, err
			}
			if !result {
				return journal.ToolResult{ToolCallID: c.ID}, nil
			}
			return tx.RecordToolResult(c.ID, journal.ToolCallExecuted, []byte("r"), "")
		}
		r1, err := mk(f.step["r1"], 0, true)
		if err != nil {
			return err
		}
		r1Result, r1ResultSeq = r1.ID, r1.JournalSeq
		unfinished, err := mk(f.step["r1"], 1, false)
		if err != nil {
			return err
		}
		r1Unfinished = unfinished.ToolCallID
		r2, err := mk(f.step["r2"], 0, true)
		if err != nil {
			return err
		}
		r2Result = r2.ID
		o, err := mk(other.step["r1"], 0, true)
		if err != nil {
			return err
		}
		otherResult = o.ID
		if r1Obs, err = tx.RecordObservation(f.turn, f.step["r1"], "lib.go", journal.ObservationFile, []byte("l")); err != nil {
			return err
		}
		if r2Obs, err = tx.RecordObservation(f.turn, f.step["r2"], "util.go", journal.ObservationAbsent, nil); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Results and observations received: the reviewers' next attempts give
	// the verdicts, each taking a journal sequence after its evidence.
	giveVerdict(t, store, &f, "r1")
	giveVerdict(t, store, &f, "r2")
	giveVerdict(t, store, &other, "r1")
	if r1ResultSeq <= 0 || r1Obs.JournalSeq <= r1ResultSeq || verdictSeq(t, db, f.verdict["r1"]) <= r1Obs.JournalSeq {
		t.Fatalf("sequence: result %d, observation %d, verdict %d", r1ResultSeq, r1Obs.JournalSeq, verdictSeq(t, db, f.verdict["r1"]))
	}
	if calls, err := store.ToolCallsByTurn(f.turn, "r1"); err != nil || len(calls) != 2 || calls[0].Result == nil || calls[0].Result.JournalSeq != r1ResultSeq {
		t.Fatalf("calls = %+v, %v", calls, err)
	}
	try := func(resultIDs, obsIDs []string, version int) error {
		return store.Tx(func(tx *journal.Tx) error {
			return tx.RecordVerdictEvidence(f.verdict["r1"], version, resultIDs, obsIDs)
		})
	}
	cases := []struct {
		name string
		res  []string
		obs  []string
		ver  int
	}{
		{"other reviewer's result", []string{r2Result}, nil, 3},
		{"other reviewer's observation", nil, []string{r2Obs.ID}, 3},
		{"other turn's result", []string{otherResult}, nil, 3},
		{"author's observation", nil, []string{authorObs.ID}, 3},
		{"unfinished call", []string{r1Unfinished}, nil, 3},
		{"unknown result", []string{"nosuch"}, nil, 3},
		{"observation beyond version seen", nil, []string{r1Obs.ID}, 1},
		{"version beyond the turn's", []string{r1Result}, nil, 4},
	}
	for _, c := range cases {
		err := try(c.res, c.obs, c.ver)
		if err == nil {
			t.Fatalf("%s accepted", c.name)
		}
		if strings.HasPrefix(c.name, "other") || c.name == "author's observation" {
			if !errors.Is(err, journal.ErrEvidenceScope) {
				t.Fatalf("%s error = %v", c.name, err)
			}
		}
	}
	if ev, err := store.GetVerdictEvidence(f.verdict["r1"]); !errors.Is(err, journal.ErrNoSuchContent) {
		t.Fatalf("evidence after refusals = %+v, %v", ev, err)
	}
	// The real multi-attempt loop: results and observation from the earlier
	// tool-step attempt, verdict from the next attempt.
	if err := try([]string{r1Result}, []string{r1Obs.ID}, 2); err != nil {
		t.Fatalf("legitimate evidence refused: %v", err)
	}
	if err := try([]string{r1Result}, nil, 2); err == nil {
		t.Fatal("second evidence record accepted")
	}
	ev, err := store.GetVerdictEvidence(f.verdict["r1"])
	if err != nil || ev.SnapshotVersion != 2 || len(ev.ResultIDs) != 1 || ev.ResultIDs[0] != r1Result || len(ev.ObservationIDs) != 1 || ev.ObservationIDs[0] != r1Obs.ID {
		t.Fatalf("evidence = %+v, %v", ev, err)
	}
	// r2's verdict: its own evidence is fine, r1's bound observation is not.
	err = store.Tx(func(tx *journal.Tx) error {
		return tx.RecordVerdictEvidence(f.verdict["r2"], 3, []string{r2Result}, []string{r1Obs.ID})
	})
	if err == nil {
		t.Fatal("observation bound to two verdicts")
	}
	err = store.Tx(func(tx *journal.Tx) error {
		return tx.RecordVerdictEvidence(f.verdict["r2"], 3, []string{r2Result}, []string{r2Obs.ID})
	})
	if err != nil {
		t.Fatal(err)
	}
	// A result from an attempt dispatched after the verdict's is refused.
	err = store.Tx(func(tx *journal.Tx) error {
		late, err := tx.BeginAttempt(f.turn, "r1", "review", 1, nil, "")
		if err != nil {
			return err
		}
		c, err := tx.RecordToolCall(late, 0, "late", "read_file", "read", nil)
		if err != nil {
			return err
		}
		r, err := tx.RecordToolResult(c.ID, journal.ToolCallExecuted, nil, "")
		if err != nil {
			return err
		}
		v, err := tx.RecordVerdict(f.verdictAt["r1"], "h0", "approve", "", "valid")
		if err != nil {
			return err
		}
		err = tx.RecordVerdictEvidence(v, 3, []string{r.ID}, nil)
		if !errors.Is(err, journal.ErrEvidenceScope) {
			return fmt.Errorf("late result error = %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if kinds := eventKinds(t, store, 0); !strings.Contains(strings.Join(kinds, ","), "verdict_evidence,verdict_evidence") {
		t.Fatalf("events = %v", kinds)
	}
	if _, err := db.Conn().Exec(`DELETE FROM verdict_tool_results`); err == nil {
		t.Fatal("verdict_tool_results delete succeeded")
	}
}

// Evidence received after the verdict is refused even when its call was
// accepted, in the reviewer's own earlier tool step, before the verdict: the
// result's and the observation's receipt is ordered against the verdict's
// record, not against the attempt that made the call.
func TestVerdictEvidenceReceivedAfterVerdictRefused(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)

	// The tool step accepts two calls; only the first is answered before the
	// reviewer gives its verdict.
	var answered, pending journal.ToolCall
	var early journal.ToolResult
	err := store.Tx(func(tx *journal.Tx) error {
		var err error
		if answered, err = tx.RecordToolCall(f.step["r1"], 0, "c0", "read_file", "read", []byte(`{"path":"a"}`)); err != nil {
			return err
		}
		if pending, err = tx.RecordToolCall(f.step["r1"], 1, "c1", "read_file", "read", []byte(`{"path":"b"}`)); err != nil {
			return err
		}
		early, err = tx.RecordToolResult(answered.ID, journal.ToolCallExecuted, []byte("A"), "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	giveVerdict(t, store, &f, "r1")
	vSeq := verdictSeq(t, db, f.verdict["r1"])
	if early.JournalSeq >= vSeq {
		t.Fatalf("result seq %d not before verdict seq %d", early.JournalSeq, vSeq)
	}

	// After the verdict: the pending call's result arrives and the reviewer's
	// tool-step attempt records an observation.
	var late journal.ToolResult
	var lateObs journal.Observation
	err = store.Tx(func(tx *journal.Tx) error {
		var err error
		if late, err = tx.RecordToolResult(pending.ID, journal.ToolCallExecuted, []byte("B"), ""); err != nil {
			return err
		}
		lateObs, err = tx.RecordObservation(f.turn, f.step["r1"], "b", journal.ObservationFile, []byte("B"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if late.JournalSeq <= vSeq || lateObs.JournalSeq <= vSeq {
		t.Fatalf("late seqs %d, %d not after verdict seq %d", late.JournalSeq, lateObs.JournalSeq, vSeq)
	}
	try := func(resultIDs, obsIDs []string) error {
		return store.Tx(func(tx *journal.Tx) error {
			return tx.RecordVerdictEvidence(f.verdict["r1"], 1, resultIDs, obsIDs)
		})
	}
	if err := try([]string{early.ID, late.ID}, nil); !errors.Is(err, journal.ErrEvidenceScope) {
		t.Fatalf("late result error = %v", err)
	}
	if err := try([]string{early.ID}, []string{lateObs.ID}); !errors.Is(err, journal.ErrEvidenceScope) {
		t.Fatalf("late observation error = %v", err)
	}
	if ev, err := store.GetVerdictEvidence(f.verdict["r1"]); !errors.Is(err, journal.ErrNoSuchContent) {
		t.Fatalf("evidence after refusals = %+v, %v", ev, err)
	}
	if n := len(eventKinds(t, store, 0)); strings.Contains(strings.Join(eventKinds(t, store, 0), ","), "verdict_evidence") {
		t.Fatalf("verdict_evidence event after refusal among %d events", n)
	}
	// The result received before the verdict is evidence.
	if err := try([]string{early.ID}, nil); err != nil {
		t.Fatal(err)
	}
	ev, err := store.GetVerdictEvidence(f.verdict["r1"])
	if err != nil || len(ev.ResultIDs) != 1 || ev.ResultIDs[0] != early.ID {
		t.Fatalf("evidence = %+v, %v", ev, err)
	}

	// A verdict recorded before verdicts took a journal sequence, as rows
	// written before migration 014 were, takes no evidence at all.
	if _, err := db.Conn().Exec(`INSERT INTO verdicts (id, attempt_id, candidate_hash, parsed_verdict, validation_status)
		VALUES ('legacy', ?, 'h0', 'approve', 'valid')`, f.verdictAt["r1"]); err != nil {
		t.Fatal(err)
	}
	err = store.Tx(func(tx *journal.Tx) error {
		return tx.RecordVerdictEvidence("legacy", 1, []string{early.ID}, nil)
	})
	if !errors.Is(err, journal.ErrEvidenceScope) {
		t.Fatalf("unsequenced verdict error = %v", err)
	}
}

// Many observations and staged paths, including names with every awkward
// byte, round-trip through a restart and rebuild from their hashes.
func TestToolRecordsAtScale(t *testing.T) {
	n := 1500
	if testing.Short() {
		n = 150
	}
	path := t.TempDir() + "/scale.db"
	db, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)
	contents := map[string][]byte{}
	err = store.Tx(func(tx *journal.Tx) error {
		for i := 0; i < n; i++ {
			p := fmt.Sprintf("dir/%04d\n\xff:%d", i, i)
			base := []byte(fmt.Sprintf("base %d", i))
			staged := []byte(fmt.Sprintf("staged %d", i))
			if _, err := tx.RecordObservation(f.turn, f.author, p, journal.ObservationFile, base); err != nil {
				return err
			}
			if _, err := tx.StageWrite(f.turn, p, staged); err != nil {
				return err
			}
			contents[p] = staged
		}
		_, err := tx.FreezeChangeSet(f.turn, f.candidate, "s", "c")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store = journal.NewStore(db)
	cs, err := store.ChangeSetForTurn(f.turn)
	if err != nil || len(cs.Paths) != n || cs.SnapshotVersion != n {
		t.Fatalf("change set paths = %d version %d, %v", len(cs.Paths), cs.SnapshotVersion, err)
	}
	for i, p := range cs.Paths {
		want := contents[p.Path]
		got, err := store.Content(p.NewSHA256)
		if err != nil || string(got) != string(want) {
			t.Fatalf("path %d %q = %q, %v", i, p.Path, got, err)
		}
		if i > 0 && cs.Paths[i-1].Path >= p.Path {
			t.Fatalf("paths not sorted at %d", i)
		}
	}
	obs, err := store.Observations(f.turn)
	if err != nil || len(obs) != n {
		t.Fatalf("observations = %d, %v", len(obs), err)
	}
}

// A reviewer's post-freeze observation and tool-result evidence, recorded
// through RecordVerdictEvidence, leave the stored candidate and its frozen
// change set byte for byte unchanged, and the reviewer's result never enters
// the author's evidence that the candidate hash binds.
func TestReviewerEvidenceDoesNotRewriteFrozenCandidate(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildToolsTurn(t, store)

	const hexA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	snap, err := observation.SnapshotHash([]observation.Record{{Kind: observation.KindFile, Path: "a\nb", Value: hexA}})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := observation.ChangesHash([]observation.Change{{Path: "a\nb", Op: observation.OpCreate, BaseSHA: observation.Absent, NewSHA: hexA, NewLen: 1}}, snap)
	if err != nil {
		t.Fatal(err)
	}
	id := candidate.Identity{
		ManifestHash: "m", TurnID: f.turn, AuthorAttemptID: f.author, Content: []byte("c"),
		Evidence:    []candidate.EvidencePair{{CallID: "c0", ResultSHA256: hexA}},
		ContextHash: hexA, ChangesHash: changes, Round: 1,
	}
	hash, err := candidate.Hash(id)
	if err != nil {
		t.Fatal(err)
	}

	var candID string
	var frozen journal.ChangeSet
	err = store.Tx(func(tx *journal.Tx) error {
		var err error
		if candID, err = tx.CreateCandidate(f.turn, "c", hash); err != nil {
			return err
		}
		if _, err = tx.RecordObservation(f.turn, f.author, "a\nb", journal.ObservationAbsent, nil); err != nil {
			return err
		}
		if _, err = tx.StageWrite(f.turn, "a\nb", []byte("x")); err != nil {
			return err
		}
		frozen, err = tx.FreezeChangeSet(f.turn, candID, snap, changes)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetCandidate(candID)
	if err != nil {
		t.Fatal(err)
	}

	// The reviewer's tool step: a result and a first-touch observation that
	// extend the turn's record after the freeze, then its verdict and evidence.
	var result, obs string
	err = store.Tx(func(tx *journal.Tx) error {
		step, err := tx.BeginAttempt(f.turn, "r1", "review", 1, nil, "")
		if err != nil {
			return err
		}
		c, err := tx.RecordToolCall(step, 0, "rev1", "read_file", "read", []byte(`{}`))
		if err != nil {
			return err
		}
		r, err := tx.RecordToolResult(c.ID, journal.ToolCallExecuted, []byte("r"), "")
		if err != nil {
			return err
		}
		result = r.ID
		o, err := tx.RecordObservation(f.turn, step, "dep.go", journal.ObservationFile, []byte("d"))
		obs = o.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	giveVerdict(t, store, &f, "r1")
	if err := store.Tx(func(tx *journal.Tx) error {
		version, err := tx.SnapshotVersion(f.turn)
		if err != nil {
			return err
		}
		if version <= frozen.SnapshotVersion {
			return fmt.Errorf("reviewer observation left version %d at the frozen %d", version, frozen.SnapshotVersion)
		}
		return tx.RecordVerdictEvidence(f.verdict["r1"], version, []string{result}, []string{obs})
	}); err != nil {
		t.Fatal(err)
	}

	after, err := store.GetCandidate(candID)
	if err != nil || *after != *before || after.ContentHash != hash {
		t.Fatalf("stored candidate = %+v, %v; want %+v with hash %s", after, err, before, hash)
	}
	cs, err := store.GetChangeSet(frozen.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.SnapshotHash != snap || cs.ChangesHash != changes || cs.SnapshotVersion != frozen.SnapshotVersion || len(cs.Paths) != len(frozen.Paths) {
		t.Fatalf("frozen change set changed: %+v, was %+v", cs, frozen)
	}
	if again, err := candidate.Hash(id); err != nil || again != after.ContentHash {
		t.Fatalf("author identity hash %s, %v does not match stored %s", again, err, after.ContentHash)
	}
	if len(id.Evidence) != 1 || id.Evidence[0].CallID != "c0" {
		t.Fatalf("author evidence = %+v", id.Evidence)
	}
}
