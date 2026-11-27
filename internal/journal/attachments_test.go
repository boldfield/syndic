package journal_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/boldfield/syndic/internal/candidate"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
)

func openAttachStore(t *testing.T, path string) (*journal.DB, *journal.Store) {
	t.Helper()
	db, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return db, journal.NewStore(db)
}

func newAttachRun(t *testing.T, store *journal.Store) string {
	t.Helper()
	var run string
	if err := store.Tx(func(tx *journal.Tx) error {
		var err error
		run, _, err = tx.CreateRun(json.RawMessage(`{}`))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return run
}

func admit(store *journal.Store, run, sub string, atts []journal.AttachmentContent) (turn string, err error) {
	err = store.Tx(func(tx *journal.Tx) error {
		var txErr error
		turn, txErr = tx.CreateEngineTurn(journal.TurnParams{RunID: run, Mode: "solo", SubmissionID: sub, SubmissionHash: "h", Attachments: atts})
		return txErr
	})
	return turn, err
}

func countRows(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAttachmentsReopenReproducesBytesAndManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.db")
	db, store := openAttachStore(t, path)
	run := newAttachRun(t, store)
	atts := []journal.AttachmentContent{
		{Path: "z/b.txt", Content: []byte("second\n")},
		{Path: "a.txt", Content: []byte("first")},
		{Path: "empty.txt", Content: []byte{}},
	}
	turn, err := admit(store, run, "s1", atts)
	if err != nil {
		t.Fatal(err)
	}
	if atts[0].Path != "z/b.txt" {
		t.Fatal("caller's slice was reordered")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, store = openAttachStore(t, path)
	defer func() { _ = db.Close() }()
	got, err := store.TurnAttachments(turn)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a.txt": "first", "empty.txt": "", "z/b.txt": "second\n"}
	if len(got) != 3 || got[0].Path != "a.txt" || got[1].Path != "empty.txt" || got[2].Path != "z/b.txt" {
		t.Fatalf("attachments = %+v", got)
	}
	var manifest []candidate.Attachment
	for _, a := range got {
		if string(a.Content) != want[a.Path] || a.ByteLen != int64(len(want[a.Path])) || a.SHA256 != shaBytes([]byte(want[a.Path])) {
			t.Fatalf("attachment %s = %+v", a.Path, a)
		}
		manifest = append(manifest, candidate.Attachment{Path: a.Path, ContentSHA256: a.SHA256, ContentLen: a.ByteLen})
	}
	wantHash, err := candidate.AttachmentManifestHash(manifest)
	if err != nil || wantHash == "none" {
		t.Fatalf("manifest hash %q, %v", wantHash, err)
	}
	row, err := store.GetTurn(turn)
	if err != nil {
		t.Fatal(err)
	}
	if !row.FileTainted {
		t.Error("attachment turn is not file-tainted")
	}
	obs, err := store.Observations(turn)
	if err != nil || len(obs) != 3 {
		t.Fatalf("observations = %+v, %v", obs, err)
	}
	for _, o := range obs {
		if o.AttemptID != "" || o.Kind != journal.ObservationFile || o.ContentSHA256 != shaBytes([]byte(want[o.Path])) {
			t.Errorf("base %+v", o)
		}
	}
	hash := storedAttachmentsHash(t, path, turn)
	if hash != wantHash {
		t.Fatalf("stored attachments_hash = %s, recomputed %s", hash, wantHash)
	}
}

func storedAttachmentsHash(t *testing.T, path, turn string) string {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var h sql.NullString
	if err := raw.QueryRow(`SELECT attachments_hash FROM turns WHERE id = ?`, turn).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h.String
}

func TestSoloTurnWithoutAttachmentsCarriesNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.db")
	db, store := openAttachStore(t, path)
	defer func() { _ = db.Close() }()
	turn, err := admit(store, newAttachRun(t, store), "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if h := storedAttachmentsHash(t, path, turn); h != "none" {
		t.Fatalf("attachments_hash = %q", h)
	}
	row, _ := store.GetTurn(turn)
	if row.FileTainted {
		t.Error("turn without attachments is tainted")
	}
	if got, err := store.TurnAttachments(turn); err != nil || got != nil {
		t.Fatalf("attachments = %v, %v", got, err)
	}
}

func TestAttachmentAdmissionIsAllOrNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.db")
	db, store := openAttachStore(t, path)
	defer func() { _ = db.Close() }()
	run := newAttachRun(t, store)

	// Fail the second attachment record mid-set, after the turn, the first
	// record and both bases' predecessors are written.
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`CREATE TRIGGER boom BEFORE INSERT ON turn_attachments WHEN CAST(NEW.path AS TEXT) = 'b.txt'
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	_, err = admit(store, run, "s1", []journal.AttachmentContent{{Path: "a.txt", Content: []byte("a")}, {Path: "b.txt", Content: []byte("b")}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	for _, tbl := range []string{"turns", "turn_attachments", "observations", "tool_content", "events", "conversations"} {
		if n := countRows(t, raw, `SELECT COUNT(*) FROM `+tbl); n != 0 {
			t.Errorf("%s has %d rows after failed admission", tbl, n)
		}
	}
	if _, err := raw.Exec(`DROP TRIGGER boom`); err != nil {
		t.Fatal(err)
	}
	// The same submission now admits cleanly.
	if _, err := admit(store, run, "s1", []journal.AttachmentContent{{Path: "a.txt", Content: []byte("a")}, {Path: "b.txt", Content: []byte("b")}}); err != nil {
		t.Fatal(err)
	}
}

func TestAttachmentValidationWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.db")
	db, store := openAttachStore(t, path)
	defer func() { _ = db.Close() }()
	run := newAttachRun(t, store)
	for name, atts := range map[string][]journal.AttachmentContent{
		"duplicate": {{Path: "a", Content: []byte("1")}, {Path: "a", Content: []byte("2")}},
		"empty":     {{Path: "", Content: []byte("1")}},
		"newline":   {{Path: "a\nb", Content: []byte("1")}},
	} {
		if _, err := admit(store, run, "s-"+name, atts); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	raw, _ := sql.Open("sqlite", "file:"+path)
	defer func() { _ = raw.Close() }()
	if n := countRows(t, raw, `SELECT COUNT(*) FROM turns`); n != 0 {
		t.Fatalf("%d turns", n)
	}
}

func TestAttachmentRecordsAreFixed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.db")
	db, store := openAttachStore(t, path)
	defer func() { _ = db.Close() }()
	run := newAttachRun(t, store)
	with, err := admit(store, run, "s1", []journal.AttachmentContent{{Path: "a.txt", Content: []byte("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	none, err := admit(store, run, "s2", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	for _, q := range []string{
		`UPDATE turns SET attachments_hash = 'none' WHERE id = '` + with + `'`,
		`UPDATE turns SET attachments_hash = 'x' WHERE id = '` + none + `'`,
		`UPDATE turn_attachments SET byte_len = 9`,
		`DELETE FROM turn_attachments`,
	} {
		if _, err := raw.Exec(q); err == nil {
			t.Errorf("%s: succeeded", q)
		}
	}
	// A second manifest member cannot be added to an admitted turn, nor any to
	// a turn admitted with none, nor a record whose length or base disagree.
	sum := shaBytes([]byte("hello"))
	var obsID string
	if err := raw.QueryRow(`SELECT id FROM observations WHERE turn_id = ?`, with).Scan(&obsID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO turn_attachments (turn_id, path, sha256, byte_len, observation_id) VALUES ('` + none + `', 'a.txt', '` + sum + `', 5, '` + obsID + `')`,
		`INSERT INTO turn_attachments (turn_id, path, sha256, byte_len, observation_id) VALUES ('` + with + `', 'b.txt', '` + sum + `', 5, '` + obsID + `')`,
	} {
		if _, err := raw.Exec(q); err == nil {
			t.Errorf("%s: succeeded", q)
		}
	}
	if _, err := store.TurnAttachments(with); err != nil {
		t.Fatal(err)
	}
}

func TestAttachedBaseUnderStagingAndUnstage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.db")
	db, store := openAttachStore(t, path)
	defer func() { _ = db.Close() }()
	run := newAttachRun(t, store)
	turn, err := admit(store, run, "s1", []journal.AttachmentContent{{Path: "f.txt", Content: []byte("admitted")}})
	if err != nil {
		t.Fatal(err)
	}
	var attempt string
	if err := store.Tx(func(tx *journal.Tx) error {
		var err error
		attempt, err = tx.BeginAttempt(turn, "author", "author", 1, nil, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	baseSHA := shaBytes([]byte("admitted"))
	obs := observation.NewStore(store)
	// The live file has since changed; the reader must never be consulted.
	live := func() (string, []byte, error) {
		t.Error("live tree read for an attached path")
		return journal.ObservationFile, []byte("changed on disk"), nil
	}
	read := func() journal.Observation {
		o, err := obs.Observe(turn, attempt, "f.txt", live)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	if o := read(); o.ContentSHA256 != baseSHA || o.SnapshotVersion != 1 || o.AttemptID != "" {
		t.Fatalf("observation = %+v", o)
	}
	if err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.StageWrite(turn, "f.txt", []byte("staged")); err != nil {
			return err
		}
		st, err := tx.Staging(turn)
		if err != nil || len(st) != 1 || st[0].BaseSHA256 != baseSHA || st[0].NewSHA256 != shaBytes([]byte("staged")) {
			t.Fatalf("staging = %+v, %v", st, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if o := read(); o.ContentSHA256 != baseSHA {
		t.Fatalf("base beneath staged edit = %+v", o)
	}
	if err := store.Tx(func(tx *journal.Tx) error { return tx.Unstage(turn, "f.txt") }); err != nil {
		t.Fatal(err)
	}
	if o := read(); o.ContentSHA256 != baseSHA {
		t.Fatalf("base after unstage = %+v", o)
	}
	// A second recording of the path is still refused.
	err = store.Tx(func(tx *journal.Tx) error {
		_, err := tx.RecordObservation(turn, attempt, "f.txt", journal.ObservationFile, []byte("other"))
		return err
	})
	if !errors.Is(err, journal.ErrAlreadyObserved) {
		t.Fatalf("err = %v", err)
	}
}
