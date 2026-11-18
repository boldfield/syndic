package journal_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/journal"
)

// qFixture is an open journal with one engine run and one turn.
type qFixture struct {
	db     *journal.DB
	store  *journal.Store
	turnID string
}

func newQFixture(t *testing.T) *qFixture {
	t.Helper()
	db, cleanup := openTestDB(t)
	t.Cleanup(cleanup)
	setupTestData(t, db.Conn(), "run-1", "turn-1")
	return &qFixture{db: db, store: journal.NewStore(db), turnID: "turn-1"}
}

func (f *qFixture) msg(t *testing.T, content string) string {
	t.Helper()
	id, _, _, err := f.store.AppendMessage(f.turnID, "p", "alice", nil, []byte(content), nil)
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	return "msg:" + id
}

func (f *qFixture) attempt(t *testing.T) (ref, id string) {
	t.Helper()
	id, err := f.store.BeginAttempt(f.turnID, "bob", "review", 1, nil, "")
	if err != nil {
		t.Fatalf("BeginAttempt: %v", err)
	}
	return "attempt:" + id, id
}

func refsOf(items []journal.TranscriptItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Ref
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pageAll walks the transcript with the given page size and returns every ref.
func pageAll(t *testing.T, s *journal.Store, turnID string, limit int) (refs []string, pages int) {
	t.Helper()
	cursor := ""
	for {
		items, next, err := s.TranscriptPage(turnID, cursor, limit)
		if err != nil {
			t.Fatalf("TranscriptPage(%q): %v", cursor, err)
		}
		refs = append(refs, refsOf(items)...)
		pages++
		if next == "" {
			return refs, pages
		}
		if pages > 1000 {
			t.Fatal("paging did not terminate")
		}
		cursor = next
	}
}

func TestMigration005JournalSeqSchema(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	var next int
	if err := conn.QueryRow(`SELECT next FROM journal_counter WHERE id = 1`).Scan(&next); err != nil || next != 1 {
		t.Fatalf("journal_counter.next = %d, err %v; want 1", next, err)
	}
	if _, err := conn.Exec(`INSERT INTO journal_counter (id, next) VALUES (2, 1)`); err == nil {
		t.Error("second journal_counter row accepted")
	}
	for _, table := range []string{"messages", "attempts"} {
		if !tableColumns(t, conn, table)["journal_seq"] {
			t.Errorf("%s.journal_seq missing", table)
		}
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND sql LIKE '%UNIQUE%journal_seq%'`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: unique journal_seq index count = %d, err %v", table, n, err)
		}
	}
}

// build004 creates a journal file at migration 004 holding probe rows.
func build004(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/old.db"
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, conn, `CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	for i, f := range []string{"001_init.sql", "002_events_nullable_turn_id.sql", "003_blocks_withdrawn_against_hash.sql", "004_engine.sql"} {
		b, err := os.ReadFile("migrations/" + f)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, conn, string(b))
		mustExec(t, conn, `INSERT INTO schema_version (version) VALUES (?)`, i+1)
	}
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash, kind) VALUES ('r1', '{"a":1}', 'mh', 'probe')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES ('t1', 'r1', 'solo')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode) VALUES ('t2', 'r1', 'solo')`)
	// Legacy messages, inserted out of sequence order.
	for _, m := range []struct {
		id  string
		seq int
	}{{"m3", 3}, {"m1", 1}, {"m2", 2}} {
		mustExec(t, conn, `INSERT INTO messages (id, turn_id, phase, sender, recipient_set, sequence, content, content_hash, byte_length) VALUES (?, 't1', 'p', 'alice', '[]', ?, ?, 'h', 2)`, m.id, m.seq, "c"+m.id)
	}
	// Legacy attempts: a2 and a3 share a dispatched_at; a1 is earlier but inserted last.
	for _, a := range []struct{ id, at string }{{"a2", "2020-01-01T00:00:02Z"}, {"a3", "2020-01-01T00:00:02Z"}, {"a1", "2020-01-01T00:00:01Z"}} {
		mustExec(t, conn, `INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at, state, status, scrub_count, raw_output) VALUES (?, 't1', 'bob', 'review', 0, ?, 'completed', 'complete', 2, ?)`, a.id, a.at, "out-"+a.id)
	}
	mustExec(t, conn, `INSERT INTO attempts (id, turn_id, participant, phase, epoch, dispatched_at) VALUES ('probe-a', 't2', 'p', 'review', 0, '2020-01-01T00:00:00Z')`)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMigration005OverV004ProbeRows(t *testing.T) {
	path := build004(t)
	db, err := journal.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("close: %v", err)
		}
	}()
	conn := db.Conn()

	var v int
	if err := conn.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil || v != 5 {
		t.Fatalf("version = %d, err %v; want 5", v, err)
	}

	var kind, manifest, mh string
	if err := conn.QueryRow(`SELECT kind, manifest, manifest_hash FROM runs WHERE id = 'r1'`).Scan(&kind, &manifest, &mh); err != nil {
		t.Fatal(err)
	}
	if kind != "probe" || manifest != `{"a":1}` || mh != "mh" {
		t.Errorf("run changed: %q %q %q", kind, manifest, mh)
	}
	var mode string
	var epoch int
	if err := conn.QueryRow(`SELECT mode, epoch FROM turns WHERE id = 't1'`).Scan(&mode, &epoch); err != nil || mode != "solo" || epoch != 0 {
		t.Errorf("turn changed: %q %d %v", mode, epoch, err)
	}

	var content, hash, sender string
	var seq, byteLen int
	var jseq sql.NullInt64
	if err := conn.QueryRow(`SELECT content, content_hash, sender, sequence, byte_length, journal_seq FROM messages WHERE id = 'm2'`).Scan(&content, &hash, &sender, &seq, &byteLen, &jseq); err != nil {
		t.Fatal(err)
	}
	if content != "cm2" || hash != "h" || sender != "alice" || seq != 2 || byteLen != 2 || jseq.Valid {
		t.Errorf("message changed: %q %q %q %d %d %v", content, hash, sender, seq, byteLen, jseq)
	}

	var part, phase, at, state, status, raw string
	var scrub int
	if err := conn.QueryRow(`SELECT participant, phase, dispatched_at, state, status, scrub_count, raw_output, journal_seq FROM attempts WHERE id = 'a3'`).Scan(&part, &phase, &at, &state, &status, &scrub, &raw, &jseq); err != nil {
		t.Fatal(err)
	}
	if part != "bob" || phase != "review" || at != "2020-01-01T00:00:02Z" || state != "completed" || status != "complete" || scrub != 2 || raw != "out-a3" || jseq.Valid {
		t.Errorf("attempt changed: %q %q %q %q %q %d %q %v", part, phase, at, state, status, scrub, raw, jseq)
	}
	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM attempts WHERE journal_seq IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("attempts backfilled: %d %v", n, err)
	}
	var next int
	if err := conn.QueryRow(`SELECT next FROM journal_counter`).Scan(&next); err != nil || next != 1 {
		t.Errorf("counter = %d %v; want 1", next, err)
	}
}

func TestJournalSeqDistinctIncreasingAcrossTables(t *testing.T) {
	f := newQFixture(t)
	f.msg(t, "one")
	f.attempt(t)
	if err := f.store.Tx(func(tx *journal.Tx) error {
		if _, _, _, err := tx.AppendMessage(f.turnID, "p", "a", nil, []byte("two"), nil); err != nil {
			return err
		}
		if _, err := tx.BeginAttemptWith(journal.AttemptParams{TurnID: f.turnID, Participant: "bob", Phase: "review", Epoch: 1}); err != nil {
			return err
		}
		_, err := tx.BeginAttempt(f.turnID, "bob", "review", 1, nil, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := f.db.Conn().Query(`
		SELECT journal_seq FROM (
			SELECT journal_seq, rowid AS r, 0 AS t FROM messages
			UNION ALL SELECT journal_seq, rowid, 1 FROM attempts)
		ORDER BY journal_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[1 2 3 4 5]" {
		t.Errorf("journal_seq values = %v, want [1 2 3 4 5]", got)
	}
}

func counterValue(t *testing.T, db *journal.DB) int {
	t.Helper()
	var n int
	if err := db.Conn().QueryRow(`SELECT next FROM journal_counter WHERE id = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestJournalSeqRollbackLeavesCounter(t *testing.T) {
	f := newQFixture(t)
	f.msg(t, "one")
	before := counterValue(t, f.db)
	boom := errors.New("boom")
	err := f.store.Tx(func(tx *journal.Tx) error {
		if _, _, _, err := tx.AppendMessage(f.turnID, "p", "a", nil, []byte("x"), nil); err != nil {
			return err
		}
		if _, err := tx.BeginAttempt(f.turnID, "bob", "review", 1, nil, ""); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if after := counterValue(t, f.db); after != before {
		t.Errorf("counter %d -> %d across rollback", before, after)
	}
	var n int
	if err := f.db.Conn().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n); err != nil || n != 1 {
		t.Errorf("messages = %d, %v; want 1", n, err)
	}
}

func TestMessageImmutabilityTriggersStillHold(t *testing.T) {
	f := newQFixture(t)
	ref := f.msg(t, "one")
	id := strings.TrimPrefix(ref, "msg:")
	if _, err := f.db.Conn().Exec(`UPDATE messages SET journal_seq = NULL WHERE id = ?`, id); err == nil {
		t.Error("UPDATE messages accepted")
	}
	if _, err := f.db.Conn().Exec(`DELETE FROM messages WHERE id = ?`, id); err == nil {
		t.Error("DELETE messages accepted")
	}
}

func TestTranscriptPageJournalOrderMixedTx(t *testing.T) {
	f := newQFixture(t)
	var want []string
	m1 := f.msg(t, "m1")
	a1, _ := f.attempt(t)
	want = append(want, m1, a1)
	// message, attempt, attempt in one Tx continues the order.
	var m2, a2, a3 string
	if err := f.store.Tx(func(tx *journal.Tx) error {
		id, _, _, err := tx.AppendMessage(f.turnID, "p", "alice", nil, []byte("m2"), nil)
		if err != nil {
			return err
		}
		m2 = "msg:" + id
		x, err := tx.BeginAttempt(f.turnID, "bob", "review", 1, nil, "")
		if err != nil {
			return err
		}
		a2 = "attempt:" + x
		y, err := tx.BeginAttempt(f.turnID, "bob", "review", 1, nil, "")
		a3 = "attempt:" + y
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want = append(want, m2, a2, a3)

	// Every write falls inside one wall-clock second (dispatched_at has 1s resolution).
	var distinct int
	if err := f.db.Conn().QueryRow(`SELECT COUNT(DISTINCT dispatched_at) FROM attempts`).Scan(&distinct); err != nil || distinct != 1 {
		t.Skipf("writes straddled a second boundary (distinct=%d, err=%v)", distinct, err)
	}

	items, next, err := f.store.TranscriptPage(f.turnID, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Errorf("nextCursor = %q, want empty", next)
	}
	if got := refsOf(items); !sameStrings(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	for i, it := range items {
		wantKind := "attempt"
		if strings.HasPrefix(want[i], "msg:") {
			wantKind = "message"
		}
		if it.Kind != wantKind {
			t.Errorf("item %d kind = %q, want %q", i, it.Kind, wantKind)
		}
		if wantKind == "message" && !it.At.IsZero() {
			t.Errorf("message At = %v, want zero", it.At)
		}
		if wantKind == "attempt" && it.At.IsZero() {
			t.Errorf("attempt At is zero")
		}
	}
}

func TestTranscriptPagePagesFiveItems(t *testing.T) {
	f := newQFixture(t)
	f.msg(t, "m1")
	f.attempt(t)
	f.msg(t, "m2")
	f.attempt(t)
	f.attempt(t)

	whole, _, err := f.store.TranscriptPage(f.turnID, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	refs, pages := pageAll(t, f.store, f.turnID, 2)
	if pages != 3 {
		t.Errorf("pages = %d, want 3", pages)
	}
	if len(refs) != 5 || !sameStrings(refs, refsOf(whole)) {
		t.Errorf("paged refs = %v, whole = %v", refs, refsOf(whole))
	}
	seen := map[string]bool{}
	for _, r := range refs {
		if seen[r] {
			t.Errorf("duplicate %s", r)
		}
		seen[r] = true
	}
	// Exactly-full page: limit 5 leaves nothing, so no cursor.
	if _, next, err := f.store.TranscriptPage(f.turnID, "", 5); err != nil || next != "" {
		t.Errorf("limit 5: next = %q, err %v", next, err)
	}
}

func TestTranscriptPageLimitClamp(t *testing.T) {
	f := newQFixture(t)
	if err := f.store.Tx(func(tx *journal.Tx) error {
		for i := 0; i < 250; i++ {
			if _, _, _, err := tx.AppendMessage(f.turnID, "p", "a", nil, []byte("x"), nil); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	items, next, err := f.store.TranscriptPage(f.turnID, "", 0)
	if err != nil || len(items) != 1 || next == "" {
		t.Errorf("limit 0: %d items, next %q, err %v; want 1 item and a cursor", len(items), next, err)
	}
	items, next, err = f.store.TranscriptPage(f.turnID, "", 1000)
	if err != nil || len(items) != 200 || next == "" {
		t.Fatalf("limit 1000: %d items, next %q, err %v; want 200 and a cursor", len(items), next, err)
	}
	rest, next, err := f.store.TranscriptPage(f.turnID, next, 1000)
	if err != nil || len(rest) != 50 || next != "" {
		t.Errorf("rest: %d items, next %q, err %v; want 50, no cursor", len(rest), next, err)
	}
}

func TestTranscriptPageBadCursor(t *testing.T) {
	f := newQFixture(t)
	f.msg(t, "m1")
	for _, c := range []string{"garbage", "seq:x", "legacy:-1", "seq"} {
		if _, _, err := f.store.TranscriptPage(f.turnID, c, 10); err == nil {
			t.Errorf("cursor %q accepted", c)
		}
	}
}

func TestTranscriptPageEmptyTurn(t *testing.T) {
	f := newQFixture(t)
	items, next, err := f.store.TranscriptPage(f.turnID, "", 10)
	if err != nil || len(items) != 0 || next != "" {
		t.Errorf("empty turn: %d items, %q, %v", len(items), next, err)
	}
}

func TestTranscriptAttemptItemMetadata(t *testing.T) {
	f := newQFixture(t)
	refPending, _ := f.attempt(t)
	refDone, id := f.attempt(t)
	out := "héllo 🌍"
	if err := f.store.CompleteAttempt(id, out, 3, "ident", "complete", "stop", "200"); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.store.TranscriptPage(f.turnID, "", 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("items %d, err %v", len(items), err)
	}
	p, d := items[0], items[1]
	if p.Ref != refPending || d.Ref != refDone {
		t.Fatalf("refs %q %q", p.Ref, d.Ref)
	}
	if p.ByteLen != 0 || p.SHA256 != "" || p.Attempt == nil || p.Attempt.RawOutput != nil {
		t.Errorf("pending item: %+v", p)
	}
	if d.Attempt == nil || d.Attempt.RawOutput != nil {
		t.Fatalf("done item attempt = %+v", d.Attempt)
	}
	if d.Attempt.Status == nil || *d.Attempt.Status != "complete" || d.Attempt.ScrubCount == nil || *d.Attempt.ScrubCount != 3 {
		t.Errorf("status/scrub = %v/%v", d.Attempt.Status, d.Attempt.ScrubCount)
	}
	if d.ByteLen != int64(len([]byte(out))) || d.ByteLen == int64(len([]rune(out))) {
		t.Errorf("ByteLen = %d, want %d", d.ByteLen, len([]byte(out)))
	}
	_, total, sum, err := f.store.ContentRange(d.Ref, 0, 1)
	if err != nil || d.SHA256 == "" || d.SHA256 != sum || d.ByteLen != total {
		t.Errorf("item sha/len %q/%d vs content %q/%d (err %v)", d.SHA256, d.ByteLen, sum, total, err)
	}
	if d.Phase != "review" || d.Sender != "bob" {
		t.Errorf("phase/sender = %q/%q", d.Phase, d.Sender)
	}
}

func TestTranscriptCompletedEmptyOutputHash(t *testing.T) {
	f := newQFixture(t)
	ref, id := f.attempt(t)
	if err := f.store.CompleteAttempt(id, "", 0, "ident", "complete", "stop", "200"); err != nil {
		t.Fatal(err)
	}
	items, _, err := f.store.TranscriptPage(f.turnID, "", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items %d, err %v", len(items), err)
	}
	_, total, sum, err := f.store.ContentRange(ref, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].SHA256 != sum || items[0].ByteLen != total || sum == "" {
		t.Errorf("item sha/len %q/%d vs content %q/%d", items[0].SHA256, items[0].ByteLen, sum, total)
	}
}

func TestTranscriptMessageItemRefUsableForContent(t *testing.T) {
	f := newQFixture(t)
	ref := f.msg(t, "héllo")
	items, _, err := f.store.TranscriptPage(f.turnID, "", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items %d, err %v", len(items), err)
	}
	it := items[0]
	if it.Ref != ref || it.Kind != "message" || it.Sender != "alice" || it.Phase != "p" || it.ByteLen != 6 {
		t.Errorf("item = %+v", it)
	}
	data, total, sum, err := f.store.ContentRange(it.Ref, 0, 100)
	if err != nil || string(data) != "héllo" || total != 6 || sum != it.SHA256 {
		t.Errorf("content = %q %d %q err %v", data, total, sum, err)
	}
}

func TestTranscriptPageLegacyRowsFirst(t *testing.T) {
	path := build004(t)
	db, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s := journal.NewStore(db)

	// New rows in t1 after migration: attempt, message, attempt.
	var want []string
	want = append(want, "msg:m1", "msg:m2", "msg:m3", "attempt:a1", "attempt:a2", "attempt:a3")
	a, err := s.BeginAttempt("t1", "bob", "review", 1, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	mid, _, _, err := s.AppendMessage("t1", "p", "alice", nil, []byte("new"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.BeginAttempt("t1", "bob", "review", 1, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, "attempt:"+a, "msg:"+mid, "attempt:"+b)

	for _, limit := range []int{1, 2, 3, 4, 200} {
		refs, _ := pageAll(t, s, "t1", limit)
		if !sameStrings(refs, want) {
			t.Errorf("limit %d: refs = %v\nwant %v", limit, refs, want)
		}
	}
	// A second turn is unaffected by t1's rows.
	refs, _ := pageAll(t, s, "t2", 1)
	if !sameStrings(refs, []string{"attempt:probe-a"}) {
		t.Errorf("t2 refs = %v", refs)
	}
	// Legacy attempt item: At is its dispatched_at, no raw output.
	items, _, err := s.TranscriptPage("t1", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	it := items[3]
	if it.Ref != "attempt:a1" || it.At.Year() != 2020 || it.Attempt == nil || it.Attempt.RawOutput != nil || it.ByteLen != int64(len("out-a1")) {
		t.Errorf("legacy attempt item = %+v", it)
	}
}

func testPattern() []byte {
	// Multi-byte UTF-8 (2, 3 and 4 byte), quotes, backslashes, real control characters.
	return []byte("a\"b\\c\n\r\t\x01\x1f é ü € 日本語 🌍🚀 \\n \\u00e9 '\" end|")
}

func bigBody(n int) string {
	p := testPattern()
	return string(bytes.Repeat(p, n/len(p)+1)[:n])
}

func TestContentRangeAttemptPagesReassemble(t *testing.T) {
	f := newQFixture(t)
	_, id := f.attempt(t)
	// Pad the front so the first 262144-byte boundary falls inside a multi-byte rune.
	var body string
	for pad := 0; ; pad++ {
		if pad > len(testPattern()) {
			t.Fatal("no padding puts a page boundary mid-rune")
		}
		body = strings.Repeat("a", pad) + bigBody(700000)
		for !isValidUTF8(body) {
			body = body[:len(body)-1]
		}
		if body[262144]&0xC0 == 0x80 {
			break
		}
	}
	if len(body) < 699990 {
		t.Fatalf("body len %d", len(body))
	}
	if err := f.store.CompleteAttempt(id, body, 0, "i", "complete", "stop", "200"); err != nil {
		t.Fatal(err)
	}
	ref := "attempt:" + id

	split := false
	var got []byte
	var off int64
	pages := 0
	for {
		data, total, sum, err := f.store.ContentRange(ref, off, 262144)
		if err != nil {
			t.Fatal(err)
		}
		if total != int64(len(body)) || sum != sha(body) {
			t.Fatalf("total/sum = %d/%s", total, sum)
		}
		if len(data) == 0 {
			break
		}
		if len(data) > 262144 {
			t.Fatalf("page of %d bytes", len(data))
		}
		if !isValidUTF8(string(data)) {
			split = true
		}
		got = append(got, data...)
		off += int64(len(data))
		pages++
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3", pages)
	}
	if !split {
		t.Error("no page boundary split a multi-byte character; test pattern is not exercising byte slicing")
	}
	if string(got) != body || sha(string(got)) != sha(body) {
		t.Error("reassembled bytes differ from original")
	}
}

func TestContentRangeLimitClamp(t *testing.T) {
	f := newQFixture(t)
	ref := f.msg(t, strings.Repeat("x", 300000))
	data, total, _, err := f.store.ContentRange(ref, 0, 1000000)
	if err != nil || len(data) != 262144 || total != 300000 {
		t.Errorf("len %d total %d err %v; want 262144 / 300000", len(data), total, err)
	}
	if _, _, _, err := f.store.ContentRange(ref, 0, 0); err == nil {
		t.Error("limit 0 accepted")
	}
	if _, _, _, err := f.store.ContentRange(ref, 0, -5); err == nil {
		t.Error("negative limit accepted")
	}
}

func TestContentRangeThreeRefKinds(t *testing.T) {
	f := newQFixture(t)
	mref := f.msg(t, "message body")
	_, aid := f.attempt(t)
	if err := f.store.CompleteAttempt(aid, "attempt body", 0, "i", "complete", "stop", "200"); err != nil {
		t.Fatal(err)
	}
	cid, err := f.store.CreateCandidate(f.turnID, "candidate body", "identity-hash")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ ref, body string }{
		{mref, "message body"}, {"attempt:" + aid, "attempt body"}, {"candidate:" + cid, "candidate body"},
	} {
		data, total, sum, err := f.store.ContentRange(c.ref, 0, 1000)
		if err != nil || string(data) != c.body || total != int64(len(c.body)) || sum != sha(c.body) {
			t.Errorf("%s: %q %d %q err %v", c.ref, data, total, sum, err)
		}
		// Mid-range read still reports whole-body total and hash.
		data, total, sum, err = f.store.ContentRange(c.ref, 3, 4)
		if err != nil || string(data) != c.body[3:7] || total != int64(len(c.body)) || sum != sha(c.body) {
			t.Errorf("%s mid: %q %d %q err %v", c.ref, data, total, sum, err)
		}
	}
}

func TestContentRangeEdges(t *testing.T) {
	f := newQFixture(t)
	ref := f.msg(t, "abcdef")
	data, total, sum, err := f.store.ContentRange(ref, 6, 10)
	if err != nil || len(data) != 0 || total != 6 || sum != sha("abcdef") {
		t.Errorf("offset==total: %q %d %q %v", data, total, sum, err)
	}
	if _, _, _, err := f.store.ContentRange(ref, 7, 10); !errors.Is(err, journal.ErrOutOfRange) || errors.Is(err, journal.ErrNoSuchContent) {
		t.Errorf("offset beyond: %v", err)
	}
	if _, _, _, err := f.store.ContentRange(ref, -1, 10); !errors.Is(err, journal.ErrOutOfRange) {
		t.Errorf("negative offset: %v", err)
	}
	_, aid := f.attempt(t)
	data, total, sum, err = f.store.ContentRange("attempt:"+aid, 0, 10)
	if err != nil || len(data) != 0 || total != 0 || sum != sha("") {
		t.Errorf("empty attempt: %q %d %q %v", data, total, sum, err)
	}
}

func TestContentRangeNoSuchContent(t *testing.T) {
	f := newQFixture(t)
	for _, ref := range []string{"nope:1", "msg:missing", "attempt:missing", "candidate:missing", "", "msg"} {
		if _, _, _, err := f.store.ContentRange(ref, 0, 10); !errors.Is(err, journal.ErrNoSuchContent) {
			t.Errorf("%q: err = %v, want ErrNoSuchContent", ref, err)
		}
	}
}

func TestListAttemptsDispatchOrder(t *testing.T) {
	path := build004(t)
	db, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s := journal.NewStore(db)
	got, err := s.ListAttempts("t1")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	// a1 is earliest; a2 and a3 share a timestamp and keep insertion order.
	if !sameStrings(ids, []string{"a1", "a2", "a3"}) {
		t.Errorf("ids = %v", ids)
	}
	if got[0].RawOutput == nil || *got[0].RawOutput != "out-a1" {
		t.Errorf("ListAttempts should keep raw output: %v", got[0].RawOutput)
	}
	if none, err := s.ListAttempts("nope"); err != nil || len(none) != 0 {
		t.Errorf("unknown turn: %v %v", none, err)
	}
}

func TestGetCandidateByteLenIsBytes(t *testing.T) {
	f := newQFixture(t)
	cid, err := f.store.CreateCandidate(f.turnID, "héllo 🌍", "H")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.store.GetCandidate(cid)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != cid || c.TurnID != f.turnID || c.ContentHash != "H" || c.ByteLen != int64(len("héllo 🌍")) {
		t.Errorf("candidate = %+v", c)
	}
	if _, err := f.store.GetCandidate("missing"); !errors.Is(err, journal.ErrNoSuchContent) {
		t.Errorf("missing: %v", err)
	}
}

func TestListVerdictsAndBlocks(t *testing.T) {
	f := newBlockFixture(t)
	if _, err := f.store.CreateCandidate(f.turnID, "c1", "H1"); err != nil {
		t.Fatal(err)
	}
	v1, err := f.store.RecordVerdict(f.attemptID, "H1", "block", "bad", "valid")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := f.store.RecordVerdict(f.attemptID, "H1", "approve", "", "valid")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.OpenBlock(v1, "b1", "H1", "high", "ev", "fix"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.OpenBlock(v1, "b2", "H1", "low", "ev2", "fix2"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.OverrideBlock("b1"); err != nil {
		t.Fatal(err)
	}

	verdicts, err := f.store.ListVerdicts(f.turnID)
	if err != nil || len(verdicts) != 2 {
		t.Fatalf("verdicts %v err %v", verdicts, err)
	}
	byID := map[string]journal.VerdictRow{}
	for _, v := range verdicts {
		byID[v.ID] = v
	}
	if r := byID[v1]; r.AttemptID != f.attemptID || r.CandidateHash != "H1" || r.Parsed != "block" || r.Reason != "bad" || r.ValidationStatus != "valid" {
		t.Errorf("v1 = %+v", r)
	}
	if r := byID[v2]; r.Parsed != "approve" || r.Reason != "" {
		t.Errorf("v2 = %+v", r)
	}

	all, err := f.store.ListBlocks(f.turnID)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListBlocks %v err %v", all, err)
	}
	states := map[string]string{}
	for _, b := range all {
		states[b.ID] = b.State
	}
	if states["b1"] != "overridden" || states["b2"] != "open" {
		t.Errorf("states = %v", states)
	}
	if open := f.openIDs(t); !sameStrings(open, []string{"b2"}) {
		t.Errorf("OpenBlocks = %v, want [b2]", open)
	}
	if none, err := f.store.ListBlocks("other"); err != nil || len(none) != 0 {
		t.Errorf("other turn: %v %v", none, err)
	}
}

func sha(s string) string {
	return fmt.Sprintf("%x", sha256Sum([]byte(s)))
}

func isValidUTF8(s string) bool { return utf8.ValidString(s) }

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }
