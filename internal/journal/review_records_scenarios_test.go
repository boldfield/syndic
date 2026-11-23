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

// Replacing the stored text behind a record's refs changes neither what the
// drawer lists nor the agreement between each ref and the bytes it retrieves.
func TestReviewRecordsContentRefsSurviveSourceReplacement(t *testing.T) {
	for _, accept := range []bool{true, false} {
		t.Run(fmt.Sprintf("accept=%v", accept), func(t *testing.T) {
			db, cleanup := openTestDB(t)
			defer cleanup()
			store := journal.NewStore(db)
			f := buildReviews(t, store, accept)
			if !accept {
				// Materialize, then replace the sources.
				if _, _, err := store.ReviewsPage(f.turn, []string{"r1", "r2"}, "", 50, 1<<20); err != nil {
					t.Fatal(err)
				}
			}
			conn := db.Conn()
			if _, err := conn.Exec(`UPDATE attempts SET commentary = 'totally different text'`); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(`UPDATE blocks SET evidence = 'replaced evidence text', remedy = 'replaced remedy'`); err != nil {
				t.Fatal(err)
			}
			checkReviews(t, store, f)
		})
	}
}

// buildBig journals one reviewer opening n blocks with 1 KiB ids in round 0
// and maintaining them all in round 1; a second reviewer approves in each.
func buildBig(t *testing.T, store *journal.Store, n int, accept bool) (turn string) {
	t.Helper()
	err := store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		if turn, err = tx.CreateTurn(runID, "review", nil); err != nil {
			return err
		}
		prev := ""
		for i, h := range []string{"h0", "h1"} {
			if prev, err = tx.SetLatestCandidate(turn, "content "+h, h, i, prev); err != nil {
				return err
			}
		}
		verdict := func(p, hash, parsed, status string, idx int, states []journal.ReviewBlockState, open func(v string) error) error {
			a, err := tx.BeginAttempt(turn, p, "review", 1, nil, "")
			if err != nil {
				return err
			}
			v, err := tx.RecordVerdict(a, hash, parsed, "", status)
			if err != nil {
				return err
			}
			if open != nil {
				if err := open(v); err != nil {
					return err
				}
			}
			if !accept {
				return nil
			}
			return tx.RecordAcceptedReview(journal.AcceptedReview{TurnID: turn, VerdictID: v, AttemptID: a,
				Participant: p, RosterIndex: idx, CandidateHash: hash, Kind: parsed, States: states})
		}
		id := func(i int) string { return fmt.Sprintf("%s-%d", strings.Repeat("x", 1024), i) }
		var maintained []journal.ReviewBlockState
		for i := 0; i < n; i++ {
			maintained = append(maintained, journal.ReviewBlockState{BlockID: id(i), State: journal.ReviewBlockMaintained})
		}
		err = verdict("r1", "h0", "block", "valid", 0, nil, func(v string) error {
			for i := 0; i < n; i++ {
				if _, err := tx.OpenBlockFor(v, id(i), "h0", "low", "e", "r"); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := verdict("r2", "h0", "approve", "valid", 1, nil, nil); err != nil {
			return err
		}
		if err := verdict("r1", "h1", "block", "valid", 0, maintained, nil); err != nil {
			return err
		}
		return verdict("r2", "h1", "approve", "valid", 1, nil, nil)
	})
	if err != nil {
		t.Fatalf("build big: %v", err)
	}
	return turn
}

// A verdict naming thousands of blocks lists in short form whose record ref
// round-trips, whether its record was written at acceptance or materialized
// later from the structured rows.
func TestReviewRecordsOversizedAtScale(t *testing.T) {
	n := 3000
	if testing.Short() {
		n = 150
	}
	for _, accept := range []bool{true, false} {
		t.Run(fmt.Sprintf("accept=%v", accept), func(t *testing.T) {
			db, cleanup := openTestDB(t)
			defer cleanup()
			store := journal.NewStore(db)
			turn := buildBig(t, store, n, accept)
			roster := []string{"r1", "r2"}
			var items []journal.ReviewItem
			cursor := ""
			for i := 0; i < 20; i++ {
				page, next, err := store.ReviewsPage(turn, roster, cursor, 50, 4096)
				if err != nil {
					t.Fatal(err)
				}
				enc, _ := json.Marshal(page)
				if len(page) > 1 && len(enc) > 4096 {
					t.Fatalf("page of %d items is %d bytes, over the frame", len(page), len(enc))
				}
				items = append(items, page...)
				if next == "" {
					break
				}
				cursor = next
			}
			if len(items) != 4 {
				t.Fatalf("listed %d items, want 4", len(items))
			}
			for i, it := range items {
				big := it.Participant == "r1"
				if big != it.Oversized {
					t.Fatalf("item %d (%s round %d) oversized = %v", i, it.Participant, it.Round, it.Oversized)
				}
				if !big {
					continue
				}
				var body []byte
				for off := int64(0); off < it.Record.ByteLen; {
					data, total, sum, err := store.ContentRange(it.Record.Ref, off, 1<<18)
					if err != nil || total != it.Record.ByteLen || sum != it.Record.SHA256 {
						t.Fatalf("record range: %v total %d", err, total)
					}
					body = append(body, data...)
					off += int64(len(data))
				}
				h := sha256.Sum256(body)
				var full journal.ReviewRecord
				if hex.EncodeToString(h[:]) != it.Record.SHA256 || json.Unmarshal(body, &full) != nil || len(full.Blocks) != n {
					t.Errorf("round %d record does not match its ref (%d blocks)", it.Round, len(full.Blocks))
				}
			}
		})
	}
}

// Five reviewers over six rounds, each block carrying large evidence and
// remedy, page completely in bounded frames and every ref fetches intact.
func TestReviewRecordsLargeContentPaging(t *testing.T) {
	size := 600 << 10
	if testing.Short() {
		size = 8 << 10
	}
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	roster := []string{"r1", "r2", "r3", "r4", "r5"}
	bodies := map[string]string{}
	var turn string
	err := store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		if turn, err = tx.CreateTurn(runID, "review", nil); err != nil {
			return err
		}
		prev := ""
		for round := 0; round < 6; round++ {
			hash := fmt.Sprintf("h%d", round)
			if prev, err = tx.SetLatestCandidate(turn, "content "+hash, hash, round, prev); err != nil {
				return err
			}
			for idx, p := range roster {
				a, err := tx.BeginAttempt(turn, p, "review", 1, nil, "")
				if err != nil {
					return err
				}
				v, err := tx.RecordVerdict(a, hash, "block", "", "valid")
				if err != nil {
					return err
				}
				bid := fmt.Sprintf("%s-b%d", p, round)
				ev := strings.Repeat(fmt.Sprintf("e%d%d", idx, round), size/3)
				rm := strings.Repeat(fmt.Sprintf("r%d%d", idx, round), size/3)
				if _, err := tx.OpenBlockFor(v, bid, hash, "low", ev, rm); err != nil {
					return err
				}
				bodies["evidence:"+v+":"+bid] = ev
				if err := tx.RecordAcceptedReview(journal.AcceptedReview{TurnID: turn, VerdictID: v, AttemptID: a,
					Participant: p, RosterIndex: idx, CandidateHash: hash, Kind: "block"}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const frame = 8 << 10
	var items []journal.ReviewItem
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 100 {
			t.Fatal("paging does not end")
		}
		page, next, err := store.ReviewsPage(turn, roster, cursor, 50, frame)
		if err != nil {
			t.Fatal(err)
		}
		enc, _ := json.Marshal(page)
		if len(page) == 0 || (len(page) > 1 && len(enc) > frame) {
			t.Fatalf("page of %d items is %d bytes", len(page), len(enc))
		}
		items = append(items, page...)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(items) != 30 {
		t.Fatalf("listed %d items, want 30", len(items))
	}
	fetch := func(ref journal.ContentRef) {
		t.Helper()
		h := sha256.New()
		var got int64
		for got < ref.ByteLen || got == 0 {
			data, total, _, err := store.ContentRange(ref.Ref, got, 1<<18)
			if err != nil || total != ref.ByteLen {
				t.Fatalf("fetch %s: %v total %d want %d", ref.Ref, err, total, ref.ByteLen)
			}
			h.Write(data)
			got += int64(len(data))
			if len(data) == 0 {
				break
			}
		}
		if got != ref.ByteLen || hex.EncodeToString(h.Sum(nil)) != ref.SHA256 {
			t.Errorf("ref %s: %d bytes, hash mismatch", ref.Ref, got)
		}
	}
	for _, it := range items {
		if it.Oversized || len(it.Blocks) != 1 {
			t.Fatalf("item %+v", it)
		}
		fetch(it.Commentary)
		fetch(*it.Blocks[0].Evidence)
		fetch(*it.Blocks[0].Remedy)
	}
}

// A solo turn and a turn whose reviewers all failed list nothing.
func TestReviewRecordsEmptyTurns(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	var solo, failed string
	err := store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		if solo, err = tx.CreateTurn(runID, "solo", nil); err != nil {
			return err
		}
		if failed, err = tx.CreateTurn(runID, "review", nil); err != nil {
			return err
		}
		if _, err := tx.SetLatestCandidate(failed, "c", "h0", 0, ""); err != nil {
			return err
		}
		for _, p := range []string{"r1", "r2"} {
			a, err := tx.BeginAttempt(failed, p, "review", 1, nil, "")
			if err != nil {
				return err
			}
			if _, err := tx.RecordVerdict(a, "h0", "", "", "malformed"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, turn := range map[string]string{"solo": solo, "failed": failed} {
		items, next, err := store.ReviewsPage(turn, []string{"r1", "r2"}, "", 50, 1<<20)
		if err != nil || len(items) != 0 || next != "" {
			t.Errorf("%s: %d items, next %q, %v", name, len(items), next, err)
		}
		if n, err := store.ReviewCount(turn); err != nil || n != 0 {
			t.Errorf("%s: ReviewCount = %d, %v", name, n, err)
		}
	}
}
