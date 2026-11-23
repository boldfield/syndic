package journal_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

type reviewFixture struct {
	turn    string
	attempt map[string]string // label -> attempt id
	verdict map[string]string // label -> verdict id
}

// buildReviews journals three rounds of a two-reviewer turn. r1 has a
// malformed original and an accepted repair in round 0; r2 blocks in rounds 0
// and 1 (maintaining the first block in round 1) and approves in round 2
// withdrawing both. With accept set, each accepted verdict's record is written
// as the engine does at acceptance; otherwise the records are left to be
// materialized from the rows alone.
func buildReviews(t *testing.T, store *journal.Store, accept bool) reviewFixture {
	t.Helper()
	f := reviewFixture{attempt: map[string]string{}, verdict: map[string]string{}}
	hashes := []string{"h0", "h1", "h2"}
	err := store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		if f.turn, err = tx.CreateTurn(runID, "review", nil); err != nil {
			return err
		}
		prev := ""
		for i, h := range hashes {
			if prev, err = tx.SetLatestCandidate(f.turn, "content "+h, h, i, prev); err != nil {
				return err
			}
		}
		attempt := func(label, participant, repairOf string) (string, error) {
			id, err := tx.BeginAttempt(f.turn, participant, "review", 1, nil, repairOf)
			f.attempt[label] = id
			return id, err
		}
		accepted := func(label, participant string, idx int, hash, parsed, status, commentary string, states ...journal.ReviewBlockState) (string, error) {
			aid := f.attempt[label]
			if err := tx.SetAttemptCommentary(aid, commentary); err != nil {
				return "", err
			}
			vid, err := tx.RecordVerdict(aid, hash, parsed, "", status)
			f.verdict[label] = vid
			return vid, err
		}
		record := func(label, participant string, idx int, hash, kind string, states ...journal.ReviewBlockState) error {
			if !accept {
				return nil
			}
			return tx.RecordAcceptedReview(journal.AcceptedReview{
				TurnID: f.turn, VerdictID: f.verdict[label], AttemptID: f.attempt[label],
				Participant: participant, RosterIndex: idx, CandidateHash: hash, Kind: kind, States: states,
			})
		}

		// Round 0: r2's block; r1's malformed original, then its accepted repair.
		if _, err := attempt("r2-0", "r2", ""); err != nil {
			return err
		}
		v, err := accepted("r2-0", "r2", 1, "h0", "block", "valid", "c r2-0")
		if err != nil {
			return err
		}
		if _, err := tx.OpenBlockFor(v, "b1", "h0", "high", "evidence one", "remedy one"); err != nil {
			return err
		}
		if err := record("r2-0", "r2", 1, "h0", "block"); err != nil {
			return err
		}
		if _, err := attempt("r1-0-orig", "r1", ""); err != nil {
			return err
		}
		if _, err := accepted("r1-0-orig", "r1", 0, "h0", "", "malformed", ""); err != nil {
			return err
		}
		if _, err := attempt("r1-0-repair", "r1", f.attempt["r1-0-orig"]); err != nil {
			return err
		}
		if _, err := accepted("r1-0-repair", "r1", 0, "h0", "approve", "valid", "c r1-0"); err != nil {
			return err
		}
		if err := record("r1-0-repair", "r1", 0, "h0", "approve"); err != nil {
			return err
		}
		// A repair that failed too is not accepted.
		if _, err := attempt("r1-0-bad", "r1", f.attempt["r1-0-repair"]); err != nil {
			return err
		}
		if _, err := accepted("r1-0-bad", "r1", 0, "h0", "", "malformed", ""); err != nil {
			return err
		}

		// Round 1: r2 opens a second block and maintains the first.
		if _, err := attempt("r2-1", "r2", ""); err != nil {
			return err
		}
		if v, err = accepted("r2-1", "r2", 1, "h1", "block", "valid", "c r2-1"); err != nil {
			return err
		}
		b2, err := tx.OpenBlockFor(v, "b2", "h1", "low", "evidence two", "remedy two")
		if err != nil {
			return err
		}
		if err := record("r2-1", "r2", 1, "h1", "block", journal.ReviewBlockState{BlockID: "b1", State: "maintained"}); err != nil {
			return err
		}

		// Round 2: r2 approves, withdrawing both. r1 asks for changes.
		if _, err := attempt("r1-2", "r1", ""); err != nil {
			return err
		}
		if _, err := accepted("r1-2", "r1", 0, "h2", "", "revision_request", "c r1-2"); err != nil {
			return err
		}
		if err := record("r1-2", "r1", 0, "h2", "approve-with-changes"); err != nil {
			return err
		}
		if _, err := attempt("r2-2", "r2", ""); err != nil {
			return err
		}
		if _, err := accepted("r2-2", "r2", 1, "h2", "approve", "valid", "c r2-2"); err != nil {
			return err
		}
		b1 := f.verdict["r2-0"] + ":b1"
		if err := tx.WithdrawBlock(b1, "r2", "h2"); err != nil {
			return err
		}
		if err := tx.WithdrawBlock(b2, "r2", "h2"); err != nil {
			return err
		}
		return record("r2-2", "r2", 1, "h2", "approve",
			journal.ReviewBlockState{BlockID: "b1", State: "withdrawn"}, journal.ReviewBlockState{BlockID: "b2", State: "withdrawn"})
	})
	if err != nil {
		t.Fatalf("build reviews: %v", err)
	}
	return f
}

func summarize(it journal.ReviewItem) string {
	var bs []string
	for _, b := range it.Blocks {
		bs = append(bs, b.BlockID+":"+b.State)
	}
	return fmt.Sprintf("%d %s %s %s", it.Round, it.Participant, it.Kind, strings.Join(bs, " "))
}

func checkReviews(t *testing.T, store *journal.Store, f reviewFixture) {
	t.Helper()
	items, next, err := store.ReviewsPage(f.turn, []string{"r1", "r2"}, "", 50, 1<<20)
	if err != nil {
		t.Fatalf("ReviewsPage: %v", err)
	}
	if next != "" {
		t.Errorf("next cursor %q, want none", next)
	}
	want := []string{
		"0 r1 approve",
		"0 r2 block b1:opened",
		"1 r2 block b2:opened b1:maintained",
		"2 r1 approve-with-changes",
		"2 r2 approve b1:withdrawn b2:withdrawn",
	}
	var got []string
	for _, it := range items {
		got = append(got, strings.TrimSpace(summarize(it)))
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("reviews:\n got %q\nwant %q", got, want)
	}
	// The accepted repair is listed once with the repair attempt's id.
	if items[0].AttemptID != f.attempt["r1-0-repair"] {
		t.Errorf("repair item attempt = %s, want repair attempt %s", items[0].AttemptID, f.attempt["r1-0-repair"])
	}
	for _, it := range items {
		if it.AttemptID == f.attempt["r1-0-orig"] || it.AttemptID == f.attempt["r1-0-bad"] {
			t.Errorf("unaccepted attempt %s listed", it.AttemptID)
		}
		if it.CandidateHash == "" || it.Oversized || it.Record != nil {
			t.Errorf("item %+v", it)
		}
	}
	n, err := store.ReviewCount(f.turn)
	if err != nil || n != len(want) {
		t.Errorf("ReviewCount = %d, %v; want %d", n, err, len(want))
	}

	// Content refs match what retrieval returns.
	check := func(ref journal.ContentRef, body string) {
		t.Helper()
		data, total, sum, err := store.ContentRange(ref.Ref, 0, 1<<18)
		if err != nil {
			t.Fatalf("ContentRange(%s): %v", ref.Ref, err)
		}
		h := sha256.Sum256(data)
		if string(data) != body || total != ref.ByteLen || sum != ref.SHA256 || hex.EncodeToString(h[:]) != ref.SHA256 {
			t.Errorf("ref %+v retrieves %q total %d sum %s, want %q", ref, data, total, sum, body)
		}
	}
	check(items[1].Commentary, "c r2-0")
	check(*items[1].Blocks[0].Evidence, "evidence one")
	check(*items[1].Blocks[0].Remedy, "remedy one")
	check(*items[2].Blocks[1].Evidence, "evidence one") // maintained block cites its origin
	check(*items[4].Blocks[1].Remedy, "remedy two")
	if items[1].Blocks[0].Severity != "high" {
		t.Errorf("severity = %q", items[1].Blocks[0].Severity)
	}
}

func TestReviewRecordsMaterializedFromAcceptanceRows(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildReviews(t, store, false)
	checkReviews(t, store, f)
}

func TestReviewRecordsWrittenAtAcceptance(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	f := buildReviews(t, store, true)
	var n int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM review_records`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("records written at acceptance = %d, %v; want 5", n, err)
	}
	checkReviews(t, store, f)
	// Asking again writes nothing more.
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM review_records`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("records after listing = %d, %v; want 5", n, err)
	}
}

func TestReviewRecordsSurviveReopen(t *testing.T) {
	path := t.TempDir() + "/test.db"
	db, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := buildReviews(t, journal.NewStore(db), true)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	checkReviews(t, journal.NewStore(db), f)
}

func TestReviewRecordsOversizedShortForm(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	var turn, v1, a1 string
	err := store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		turn, _ = tx.CreateTurn(runID, "review", nil)
		if _, err := tx.CreateCandidateWithRound(turn, "c", "h0", 0, ""); err != nil {
			return err
		}
		var states []journal.ReviewBlockState
		for i := 0; i < 1000; i++ {
			states = append(states, journal.ReviewBlockState{BlockID: fmt.Sprintf("%s-%d", strings.Repeat("x", 100), i), State: "maintained"})
		}
		for _, p := range []string{"r1", "r2", "r3"} {
			a, err := tx.BeginAttempt(turn, p, "review", 1, nil, "")
			if err != nil {
				return err
			}
			v, err := tx.RecordVerdict(a, "h0", "approve", "", "valid")
			if err != nil {
				return err
			}
			if p == "r1" {
				v1, a1 = v, a
			}
			st := states
			if p == "r3" {
				st = nil
			}
			if err := tx.RecordAcceptedReview(journal.AcceptedReview{TurnID: turn, VerdictID: v, AttemptID: a,
				Participant: p, CandidateHash: "h0", Kind: "approve", States: st}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	items, next, err := store.ReviewsPage(turn, []string{"r1", "r2", "r3"}, "", 50, 1<<20)
	if err != nil || next != "" || len(items) != 3 {
		t.Fatalf("page: %d items, next %q, %v", len(items), next, err)
	}
	if !items[0].Oversized || items[0].Record == nil || items[0].AttemptID != a1 || items[2].Oversized {
		t.Fatalf("short-form flags wrong: %+v", items)
	}
	short, _ := json.Marshal(items[0])
	var m map[string]any
	if err := json.Unmarshal(short, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["blocks"]; ok || m["oversized"] != true || m["record"] == nil {
		t.Errorf("short form = %s", short)
	}
	if items[0].Record.Ref != "record:"+v1 || items[0].Record.ByteLen <= journal.ReviewOversizedBytes {
		t.Errorf("record ref = %+v", items[0].Record)
	}
	// The record is retrievable, in as many frames as it needs, and matches its ref.
	var body []byte
	for off := int64(0); off < items[0].Record.ByteLen; {
		data, total, sum, err := store.ContentRange(items[0].Record.Ref, off, 50000)
		if err != nil || total != items[0].Record.ByteLen || sum != items[0].Record.SHA256 {
			t.Fatalf("record range: %v total %d", err, total)
		}
		body = append(body, data...)
		off += int64(len(data))
	}
	h := sha256.Sum256(body)
	var full journal.ReviewRecord
	if hex.EncodeToString(h[:]) != items[0].Record.SHA256 || json.Unmarshal(body, &full) != nil || len(full.Blocks) != 1000 {
		t.Errorf("record does not match its ref (%d blocks)", len(full.Blocks))
	}

	// A frame that holds one full item at a time pages one by one; a short
	// form always fits.
	var seen []string
	cursor := ""
	for i := 0; i < 10; i++ {
		page, next, err := store.ReviewsPage(turn, []string{"r1", "r2", "r3"}, cursor, 50, 1)
		if err != nil || len(page) != 1 {
			t.Fatalf("tiny frame page: %d items, %v", len(page), err)
		}
		seen = append(seen, page[0].Participant)
		if next == "" {
			break
		}
		cursor = next
	}
	if strings.Join(seen, ",") != "r1,r2,r3" {
		t.Errorf("paged %v", seen)
	}
	if _, _, err := store.ReviewsPage(turn, nil, "nonesuch", 50, 1<<20); err == nil {
		t.Error("bad cursor accepted")
	}
	empty, next, err := store.ReviewsPage("no-turn", nil, "", 50, 1<<20)
	if err != nil || len(empty) != 0 || next != "" {
		t.Errorf("empty turn: %v %q %v", empty, next, err)
	}
}

func TestReviewRecordsImmutable(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	buildReviews(t, store, true)
	if _, err := db.Conn().Exec(`UPDATE review_records SET kind = 'block'`); err == nil {
		t.Error("update of a review record allowed")
	}
}
