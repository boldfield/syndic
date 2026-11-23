package server_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// reviewManifest names a preset whose required reviewers are rb, ra, rc: the
// roster order the tests below expect, which differs from alphabetical order.
const reviewManifest = `{"roster":{"default_preset":"p","presets":{"p":{"mode":"review","author":"au","required_reviewers":["rb","ra","rc"]}}}}`

type seedBlock struct{ id, evidence, remedy string }

// seedVerdict is one verdict journaled in order. A valid one is accepted.
type seedVerdict struct {
	participant string
	roster      int
	round       int
	kind        string // approve or block
	status      string // valid or malformed
	open        []seedBlock
	maintain    []string
}

// seedTurn journals verdicts as the engine would, over rounds candidates. With
// accept set each accepted verdict's record is written at acceptance;
// otherwise the turn looks like one accepted before records existed.
func seedTurn(t *testing.T, store *journal.Store, rounds int, accept bool, vs []seedVerdict) string {
	t.Helper()
	var turn string
	err := store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(reviewManifest))
		if err != nil {
			return err
		}
		if turn, err = tx.CreateTurn(runID, "review", nil); err != nil {
			return err
		}
		prev := ""
		for i := 0; i < rounds; i++ {
			if prev, err = tx.SetLatestCandidate(turn, fmt.Sprintf("content %d", i), fmt.Sprintf("h%d", i), i, prev); err != nil {
				return err
			}
		}
		for _, v := range vs {
			hash := fmt.Sprintf("h%d", v.round)
			a, err := tx.BeginAttempt(turn, v.participant, "review", 1, nil, "")
			if err != nil {
				return err
			}
			if err := tx.SetAttemptCommentary(a, "commentary of "+v.participant); err != nil {
				return err
			}
			parsed := v.kind
			if v.status != "valid" {
				parsed = ""
			}
			vid, err := tx.RecordVerdict(a, hash, parsed, "", v.status)
			if err != nil {
				return err
			}
			for _, b := range v.open {
				if _, err := tx.OpenBlockFor(vid, b.id, hash, "low", b.evidence, b.remedy); err != nil {
					return err
				}
			}
			if !accept || v.status != "valid" {
				continue
			}
			var states []journal.ReviewBlockState
			for _, id := range v.maintain {
				states = append(states, journal.ReviewBlockState{BlockID: id, State: journal.ReviewBlockMaintained})
			}
			if err := tx.RecordAcceptedReview(journal.AcceptedReview{TurnID: turn, VerdictID: vid, AttemptID: a,
				Participant: v.participant, RosterIndex: v.roster, CandidateHash: hash, Kind: v.kind, States: states}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	return turn
}

// walkReviews pages the turn to exhaustion, returning the raw items and the
// number of pages, and failing if any frame exceeded the cap.
func walkReviews(t *testing.T, s *serving, turn string, limit int) (items []json.RawMessage, pages int) {
	t.Helper()
	c := s.dial()
	s.resetFrames()
	var cursor *string
	for {
		var lp *int
		if limit > 0 {
			lp = &limit
		}
		page, err := c.Reviews(ctxT(t), turn, cursor, lp)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(page.Items) == 0 {
			t.Fatalf("page %d is empty", pages)
		}
		items = append(items, page.Items...)
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
		if pages > 1000 {
			t.Fatal("paging does not terminate")
		}
	}
	if s.maxFrame() > wire.MaxFrameBytes {
		t.Fatalf("reply frame of %d bytes exceeds the %d cap", s.maxFrame(), wire.MaxFrameBytes)
	}
	return items, pages
}

func decodeItem(t *testing.T, raw json.RawMessage) wire.ReviewItem {
	t.Helper()
	var it wire.ReviewItem
	if err := json.Unmarshal(raw, &it); err != nil {
		t.Fatal(err)
	}
	return it
}

func keysOf(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Items come in round then roster order (the manifest's, not dispatch order),
// the cursor resumes where the page ended, and the last page has none.
func TestReviewsPagingOrderAndContinuation(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprintf("accepted_record_written=%v", accept), func(t *testing.T) {
			h := newHarness(t)
			s := serve(t, h.Engine, h.Store, h.Hub)
			// 60 items: more than one maximum page. Reviewers beyond the
			// manifest's three sort after them, by name.
			const rounds = 6
			wantOrder := []string{"rb", "ra", "rc", "x0", "x1", "x2", "x3", "x4", "x5", "x6"}
			var vs []seedVerdict
			for r := 0; r < rounds; r++ {
				for i := len(wantOrder) - 1; i >= 0; i-- { // dispatch order, not roster order
					vs = append(vs, seedVerdict{participant: wantOrder[i], roster: i, round: r, kind: "approve", status: "valid"})
				}
			}
			per := len(wantOrder)
			turn := seedTurn(t, h.Store, rounds, accept, vs)

			for _, limit := range []int{0, 1, 7} {
				items, pages := walkReviews(t, s, turn, limit)
				if len(items) != per*rounds {
					t.Fatalf("limit %d: %d items, want %d", limit, len(items), per*rounds)
				}
				wantPages := 2 // default page is 50
				if limit > 0 {
					wantPages = (per*rounds + limit - 1) / limit
				}
				if pages != wantPages {
					t.Fatalf("limit %d: %d pages, want %d", limit, pages, wantPages)
				}
				for i, raw := range items {
					it := decodeItem(t, raw)
					want := wantOrder[i%per]
					if it.Round != i/per || it.Participant != want {
						t.Fatalf("item %d = round %d %s, want round %d %s", i, it.Round, it.Participant, i/per, want)
					}
					blocks, ok := keysOf(t, raw)["blocks"]
					if !ok || string(blocks) != "[]" {
						t.Fatalf("item %d blocks = %s, want []", i, blocks)
					}
				}
			}

			// A limit over 50 is clamped; a limit under 1 is refused.
			c := s.dial()
			big := 500
			page, err := c.Reviews(ctxT(t), turn, nil, &big)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 50 || page.NextCursor == nil {
				t.Fatalf("limit 500: %d items, cursor %v; want 50 and a cursor", len(page.Items), page.NextCursor)
			}
			zero := 0
			_, err = c.Reviews(ctxT(t), turn, nil, &zero)
			wantCode(t, err, wire.ErrorCodeBadRequest)
			bad := "no-such-verdict"
			_, err = c.Reviews(ctxT(t), turn, &bad, nil)
			wantCode(t, err, wire.ErrorCodeBadRequest)
		})
	}
}

// A verdict naming thousands of blocks lists in short form, with no blocks or
// commentary, and its record ref returns every block.
func TestReviewsOversizedShortFormRecord(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(fmt.Sprintf("accepted_record_written=%v", accept), func(t *testing.T) {
			h := newHarness(t)
			s := serve(t, h.Engine, h.Store, h.Hub)
			n := 3000
			if testing.Short() {
				n = 300
			}
			id := func(i int) string { return fmt.Sprintf("%s-%d", strings.Repeat("x", 1024), i) }
			var opened []seedBlock
			var ids []string
			for i := 0; i < n; i++ {
				opened = append(opened, seedBlock{id(i), "e", "r"})
				ids = append(ids, id(i))
			}
			turn := seedTurn(t, h.Store, 2, accept, []seedVerdict{
				{participant: "rb", roster: 0, round: 0, kind: "block", status: "valid", open: opened},
				{participant: "rb", roster: 0, round: 1, kind: "block", status: "valid", maintain: ids},
				{participant: "ra", roster: 1, round: 1, kind: "approve", status: "valid"},
			})
			items, _ := walkReviews(t, s, turn, 0)
			if len(items) != 3 {
				t.Fatalf("%d items, want 3", len(items))
			}
			for _, raw := range items[:2] {
				m := keysOf(t, raw)
				if string(m["oversized"]) != "true" || m["record"] == nil {
					t.Fatalf("item %s: want oversized with a record", raw)
				}
				if _, ok := m["blocks"]; ok {
					t.Fatalf("short form carries blocks: %.200s", raw)
				}
				if _, ok := m["commentary"]; ok {
					t.Fatalf("short form carries commentary: %.200s", raw)
				}
				if len(raw) > journal.ReviewOversizedBytes {
					t.Fatalf("short form is %d bytes", len(raw))
				}
				rec := decodeItem(t, raw).Record
				body, err := s.dial().Content(ctxT(t), rec.Ref)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(body)
				if hex.EncodeToString(sum[:]) != rec.SHA256 || int64(len(body)) != rec.ByteLen {
					t.Fatalf("record content does not match its ref %+v", rec)
				}
				var full journal.ReviewRecord
				if err := json.Unmarshal(body, &full); err != nil {
					t.Fatal(err)
				}
				if len(full.Blocks) != n {
					t.Fatalf("record has %d blocks, want %d", len(full.Blocks), n)
				}
				for i, b := range full.Blocks {
					if b.BlockID != id(i) {
						t.Fatalf("record block %d id differs", i)
					}
				}
			}
			if m := keysOf(t, items[2]); m["oversized"] != nil || m["blocks"] == nil {
				t.Fatalf("small item %s should be full form", items[2])
			}
		})
	}
}

// Evidence and remedy of 600 KiB on every block are retrieved in full by
// paging the reviews and then fetching each block's content refs.
func TestReviewsLargeEvidenceAndRemedy(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	reviewers, rounds, size := 5, 6, 600<<10
	if testing.Short() {
		reviewers, rounds, size = 2, 2, 100<<10
	}
	names := []string{"rb", "ra", "rc", "rd", "re"}[:reviewers]
	var vs []seedVerdict
	want := map[string]string{} // ref -> body
	for r := 0; r < rounds; r++ {
		for i, p := range names {
			v := seedVerdict{participant: p, roster: i, round: r, kind: "block", status: "valid"}
			if r == 0 {
				ev := strings.Repeat(string(rune('a'+i)), size)
				rm := strings.Repeat(string(rune('A'+i)), size)
				v.open = []seedBlock{{p, ev, rm}}
				want["evidence:"+p] = ev
				want["remedy:"+p] = rm
			} else {
				v.maintain = []string{p}
			}
			vs = append(vs, v)
		}
	}
	turn := seedTurn(t, h.Store, rounds, true, vs)
	items, _ := walkReviews(t, s, turn, 0)
	if len(items) != reviewers*rounds {
		t.Fatalf("%d items, want %d", len(items), reviewers*rounds)
	}
	fetched := map[string]bool{}
	c := s.dial()
	for _, raw := range items {
		it := decodeItem(t, raw)
		if it.Oversized {
			t.Fatalf("one-block item is oversized: %.200s", raw)
		}
		for _, b := range it.Blocks {
			for _, ref := range []*wire.ContentReference{b.Evidence, b.Remedy} {
				if ref == nil || fetched[ref.Ref] {
					continue
				}
				fetched[ref.Ref] = true
				body, err := c.Content(ctxT(t), ref.Ref)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(body)
				kind, _, _ := strings.Cut(ref.Ref, ":")
				if string(body) != want[kind+":"+ref.Ref[strings.LastIndex(ref.Ref, ":")+1:]] || hex.EncodeToString(sum[:]) != ref.SHA256 || int64(len(body)) != ref.ByteLen {
					t.Fatalf("content of %s differs from what was recorded", ref.Ref)
				}
			}
		}
	}
	if len(fetched) != 2*reviewers {
		t.Fatalf("fetched %d refs, want %d", len(fetched), 2*reviewers)
	}
}

// Items that each fit alone but not together are split across frames: no page
// exceeds the cap, a cut page's cursor resumes at the next item, and every item
// is seen exactly once.
func TestReviewsPageTrimmedToFrame(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	const blocks, rounds = 17, 6 // ids appear in each ref: ~55 KiB per item, under the short-form limit
	names := []string{"rb", "ra", "rc", "rd"}
	var vs []seedVerdict
	for r := 0; r < rounds; r++ {
		for i, p := range names {
			v := seedVerdict{participant: p, roster: i, round: r, kind: "block", status: "valid"}
			var ids []string
			for k := 0; k < blocks; k++ {
				id := fmt.Sprintf("%s-%s-%d", p, strings.Repeat("y", 1024), k)
				ids = append(ids, id)
				if r == 0 {
					v.open = append(v.open, seedBlock{id, "e", "r"})
				}
			}
			if r > 0 {
				v.maintain = ids
			}
			vs = append(vs, v)
		}
	}
	turn := seedTurn(t, h.Store, rounds, true, vs)

	c := s.dial()
	s.resetFrames()
	first, err := c.Reviews(ctxT(t), turn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	total := rounds * len(names)
	if len(first.Items) == 0 || len(first.Items) >= total || first.NextCursor == nil {
		t.Fatalf("first page has %d of %d items, cursor %v; want a trimmed page", len(first.Items), total, first.NextCursor)
	}
	items, pages := walkReviews(t, s, turn, 0)
	if pages < 2 || len(items) != total {
		t.Fatalf("%d items over %d pages, want %d over several", len(items), pages, total)
	}
	for i, raw := range items {
		it := decodeItem(t, raw)
		if it.Round != i/len(names) || it.Participant != names[i%len(names)] || it.Oversized {
			t.Fatalf("item %d = round %d %s oversized=%v, want round %d %s full", i, it.Round, it.Participant, it.Oversized, i/len(names), names[i%len(names)])
		}
	}
}

// A review turn in which no verdict was accepted lists nothing, has no cursor
// and counts zero reviews.
func TestReviewsAllReviewersFailed(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	turn := seedTurn(t, h.Store, 1, true, []seedVerdict{
		{participant: "rb", roster: 0, round: 0, kind: "block", status: "malformed"},
		{participant: "ra", roster: 1, round: 0, kind: "block", status: "malformed"},
	})
	c := s.dial()
	page, err := c.Reviews(ctxT(t), turn, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("reply = %+v, want no items and no cursor", page)
	}
	row, err := h.Store.GetTurn(turn)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := c.Conversation(ctxT(t), row.ConversationID[:8])
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Turns) != 1 || conv.Turns[0].ReviewCount == nil || *conv.Turns[0].ReviewCount != 0 {
		t.Fatalf("turns = %+v, want review_count 0", conv.Turns)
	}
}
