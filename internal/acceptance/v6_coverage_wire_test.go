package acceptance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// These tests hold the v6 section 8 drawer scenarios at the sizes the design
// names, whatever -short says: the other reviews tests shrink under it. The
// suite's cleanup fails any test in which an engine frame reached the cap.

// v6Pages pages reviews with the given limit, returning every raw item and
// the number of pages. A page must hold at least one item and at most limit.
func v6Pages(t *testing.T, s *suite, c *client.Client, turn string, limit int) (raw [][]byte, pages int) {
	t.Helper()
	var cursor *string
	for {
		page, err := c.Reviews(s.ctx(), turn, cursor, &limit)
		if err != nil {
			t.Fatalf("reviews page %d: %v", pages+1, err)
		}
		pages++
		if n := len(page.Items); n < 1 || n > limit {
			t.Fatalf("page %d holds %d items, want 1 to %d", pages, n, limit)
		}
		for _, it := range page.Items {
			raw = append(raw, it)
		}
		if page.NextCursor == nil {
			return raw, pages
		}
		cursor = page.NextCursor
		if pages > 1000 {
			t.Fatal("paging does not end")
		}
	}
}

// A turn with five reviewers, six rounds, and an evidence and a remedy of
// 600 KiB on every block is retrieved completely by paging reviews and
// fetching each reference: no reply exceeds one frame and nothing is truncated.
func TestV6WireCoverage_FiveReviewersSixRoundsLargeBlocks(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	c := s.Dial(t)

	const rounds, size = 6, 600 << 10
	reviewers := []string{"rb", "ra", "r3", "r4", "r5"} // the manifest's two first, then the rest by name
	body := func(kind string, round, who int) string {
		head := fmt.Sprintf("%s-%d-%d|", kind, round, who)
		return head + strings.Repeat(kind[:1], size-len(head))
	}
	wantEvidence, wantRemedy := map[string]string{}, map[string]string{}
	var vs []v6Verdict
	for r := 0; r < rounds; r++ {
		for i, p := range reviewers {
			id := fmt.Sprintf("blk-%d-%d", r, i)
			wantEvidence[id], wantRemedy[id] = body("evidence", r, i), body("remedy", r, i)
			vs = append(vs, v6Verdict{participant: p, roster: i, round: r, kind: "block",
				opened: []v6Block{{id, wantEvidence[id], wantRemedy[id]}}})
		}
	}
	turn := v6Seed(t, s, rounds, true, vs)

	raws, pages := v6Pages(t, s, c, turn, 50)
	if len(raws) != rounds*len(reviewers) {
		t.Fatalf("%d items listed, want %d", len(raws), rounds*len(reviewers))
	}
	if pages < 1 {
		t.Fatal("no page returned")
	}
	fetched := 0
	for i, r := range raws {
		var page wire.ReviewsReply
		page.Items = append(page.Items, r)
		items, err := page.DecodeItems()
		if err != nil {
			t.Fatal(err)
		}
		it := items[0]
		if it.Round != i/len(reviewers) || it.Participant != reviewers[i%len(reviewers)] {
			t.Fatalf("item %d is round %d of %s, want round %d of %s", i, it.Round, it.Participant, i/len(reviewers), reviewers[i%len(reviewers)])
		}
		if it.Oversized || len(it.Blocks) != 1 {
			t.Fatalf("item %d is oversized or lacks its block: %.200s", i, r)
		}
		b := it.Blocks[0]
		if got := string(v6Verified(t, s, c, b.Evidence)); got != wantEvidence[b.BlockID] {
			t.Fatalf("item %d: evidence of %s has %d bytes, want %d of the recorded text", i, b.BlockID, len(got), len(wantEvidence[b.BlockID]))
		}
		if got := string(v6Verified(t, s, c, b.Remedy)); got != wantRemedy[b.BlockID] {
			t.Fatalf("item %d: remedy of %s has %d bytes, want %d of the recorded text", i, b.BlockID, len(got), len(wantRemedy[b.BlockID]))
		}
		fetched += 2
	}
	if fetched != 2*rounds*len(reviewers) {
		t.Fatalf("fetched %d references", fetched)
	}
}

// A verdict that maintains 3,000 blocks whose ids are 1 KiB each is returned
// in short form with oversized: true; fetching record returns every block id
// and state, its sha256 and byte_len match the bytes returned, and no reply
// exceeds one frame. The same holds for a verdict accepted before the record
// existed.
func TestV6WireCoverage_OversizedRecordIdsStatesAndHashes(t *testing.T) {
	t.Parallel()
	const n = 3000
	opened, ids := v6BigBlocks(n)
	vs := []v6Verdict{
		{participant: "rb", roster: 0, round: 0, kind: "block", opened: opened},
		{participant: "rb", roster: 0, round: 1, kind: "block", maintained: ids},
		{participant: "ra", roster: 1, round: 1, kind: "approve"},
	}
	for _, tc := range []struct {
		name   string
		accept bool
	}{{"accepted under this delta", true}, {"accepted before the record existed", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
			c := s.Dial(t)
			turn := v6Seed(t, s, 2, tc.accept, vs)
			raws, _ := v6Pages(t, s, c, turn, 50)
			if len(raws) != 3 {
				t.Fatalf("%d items, want 3", len(raws))
			}
			for i, want := range []string{journal.ReviewBlockOpened, journal.ReviewBlockMaintained} {
				if len(raws[i]) > 64<<10 {
					t.Fatalf("item %d is %d bytes in short form, want at most 64 KiB", i, len(raws[i]))
				}
				page := wire.ReviewsReply{}
				page.Items = append(page.Items, raws[i])
				items, err := page.DecodeItems()
				if err != nil {
					t.Fatal(err)
				}
				it := items[0]
				if !it.Oversized || it.Record == nil || it.Blocks != nil || it.Commentary != nil {
					t.Fatalf("item %d is not in short form: %.300s", i, raws[i])
				}
				v6Verified(t, s, c, it.Record) // sha256 and byte_len of the bytes returned
				full, err := c.FetchOversizedRecord(s.ctx(), &it)
				if err != nil {
					t.Fatalf("item %d: %v", i, err)
				}
				if len(full.Blocks) != n {
					t.Fatalf("item %d: the record has %d blocks, want %d", i, len(full.Blocks), n)
				}
				for k, b := range full.Blocks {
					if b.BlockID != ids[k] || b.State != want {
						t.Fatalf("item %d: record block %d is %.24s in state %q, want %.24s in state %q", i, k, b.BlockID, b.State, ids[k], want)
					}
				}
			}
			if strings.Contains(string(raws[2]), `"oversized"`) {
				t.Fatalf("the small item is in short form: %s", raws[2])
			}
		})
	}
}

// kind is the verdict's kind as the engine classified it: a reviewer's
// approve-with-changes is listed as that kind, not as approve or block, and
// the review_count the conversation reports is the number listed.
func TestV6WireCoverage_ApproveWithChangesKindListed(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApproveWithChanges)))
	c := s.Dial(t)
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	s.WaitTerminal(reply.TurnID)
	items, err := c.AllReviews(s.ctx(), reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != "approve-with-changes" || items[0].Participant != "reviewer" || len(items[0].Blocks) != 0 {
		t.Fatalf("items = %+v, want one approve-with-changes verdict of reviewer with no blocks", items)
	}
	v6Verified(t, s, c, items[0].Commentary)
	if n := reviewCountOf(t, s, c, reply.ConversationID, reply.TurnID); n != len(items) {
		t.Fatalf("review_count = %d, want the %d listed", n, len(items))
	}
}
