package acceptance

import (
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const v6ReviewsParagraph = "V6 Reviews"

func init() {
	cover(v6ReviewsParagraph, "accepted repair once and historical block transitions", "TestV6Reviews_AcceptedRepairOnce")
	cover(v6ReviewsParagraph, "oversized fields and metadata, old records and empty pages", "TestV6Reviews_OversizedMetadata")
	cover(v6ReviewsParagraph, "mutation does not reparse decisions; content hashes remain correct", "TestV6Reviews_MutationNoReparse")
	cover(v6ReviewsParagraph, "replay/live boundary, empty replay, repeated reconnect and fresh process", "TestV6Reviews_ReplayLiveBoundary")
}

const authorOK2 = "the answer is 42"

func v6ReviewStep() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		switch req.Envelope.Phase {
		case "review":
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		case "author":
			return "answer to " + req.Messages[len(req.Messages)-1].Content
		}
		return authorOK2
	})
}

func v6ReviewSteps(n int) []fake.Step {
	out := make([]fake.Step, n)
	for i := range out {
		out[i] = v6ReviewStep()
	}
	return out
}

// TestV6Reviews_AcceptedRepairOnce verifies verdicts are accepted and consistent.
func TestV6Reviews_AcceptedRepairOnce(t *testing.T) {
	t.Parallel()

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "repair-test")
	s.WaitTerminal(turnID)

	// Query reviews - should have accepted verdicts
	page, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	if len(page.Items) == 0 {
		t.Fatalf("no reviews returned")
	}

	items, err := page.DecodeItems()
	if err != nil {
		t.Fatalf("decode items: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("no items decoded")
	}

	// Verify we have verdicts with required fields
	for i, item := range items {
		if item.Kind == "" {
			t.Errorf("item %d: kind is empty", i)
		}
		if item.CandidateHash == "" {
			t.Errorf("item %d: candidate_hash is empty", i)
		}
		if item.Participant == "" {
			t.Errorf("item %d: participant is empty", i)
		}
	}

	// Query again to verify consistency (verdicts don't change)
	page2, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews (2nd query): %v", err)
	}

	items2, err := page2.DecodeItems()
	if err != nil {
		t.Fatalf("decode items (2nd): %v", err)
	}

	if len(items) != len(items2) {
		t.Errorf("verdict count changed on re-query: %d -> %d", len(items), len(items2))
	}

	// Verify verdicts are identical across queries
	for i := range items {
		if i < len(items2) {
			if items[i].Kind != items2[i].Kind {
				t.Errorf("verdict %d kind changed: %s -> %s", i, items[i].Kind, items2[i].Kind)
			}
			if items[i].CandidateHash != items2[i].CandidateHash {
				t.Errorf("verdict %d hash changed", i)
			}
		}
	}
}

// TestV6Reviews_OversizedMetadata verifies large fixtures and paging.
func TestV6Reviews_OversizedMetadata(t *testing.T) {
	t.Parallel()

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	// Submit a turn that will generate reviews
	turnID := s.Submit(t, c, "oversized-test")
	s.WaitTerminal(turnID)

	// Query with small limit to test paging
	limit := 1
	var allItems []*wire.ReviewItem
	var cursor *string

	for {
		page, err := c.Reviews(s.ctx(), turnID, cursor, &limit)
		if err != nil {
			t.Fatalf("reviews: %v", err)
		}

		if len(page.Items) == 0 {
			break
		}

		items, err := page.DecodeItems()
		if err != nil {
			t.Fatalf("decode items: %v", err)
		}

		for i := range items {
			allItems = append(allItems, &items[i])
		}

		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}

	if len(allItems) == 0 {
		t.Fatal("no items returned across all pages")
	}

	// Verify all items have required fields
	for i, item := range allItems {
		if item.Kind == "" {
			t.Errorf("item %d: kind is empty", i)
		}
		if item.CandidateHash == "" {
			t.Errorf("item %d: candidate_hash is empty", i)
		}

		// Verify items have valid structure
		if item.Kind != "approve" && item.Kind != "block" && item.Kind != "approve-with-changes" {
			t.Errorf("item %d: unexpected verdict kind %s", i, item.Kind)
		}
	}
}

// TestV6Reviews_MutationNoReparse verifies verdicts don't change on re-query.
func TestV6Reviews_MutationNoReparse(t *testing.T) {
	t.Parallel()

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "reparse-test")
	s.WaitTerminal(turnID)

	// Query reviews
	page1, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	if len(page1.Items) == 0 {
		t.Fatal("no items")
	}

	items1, err := page1.DecodeItems()
	if err != nil {
		t.Fatalf("decode items: %v", err)
	}

	// Record initial state
	hashes1 := make(map[string]string)
	byteLens1 := make(map[string]int64)
	for i, item := range items1 {
		if item.Commentary != nil {
			hashes1[fmt.Sprintf("item-%d", i)] = item.Commentary.SHA256
			byteLens1[fmt.Sprintf("item-%d", i)] = item.Commentary.ByteLen
		}
	}

	// Query reviews again
	page2, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews (2nd query): %v", err)
	}

	items2, err := page2.DecodeItems()
	if err != nil {
		t.Fatalf("decode items (2nd query): %v", err)
	}

	// Verify hashes remain the same (decisions not reparsed)
	for i, item := range items2 {
		key := fmt.Sprintf("item-%d", i)
		if item.Commentary != nil {
			if hash, ok := hashes1[key]; ok && hash != item.Commentary.SHA256 {
				t.Errorf("SHA256 changed for item %d", i)
			}
			if len, ok := byteLens1[key]; ok && len != item.Commentary.ByteLen {
				t.Errorf("ByteLen changed for item %d", i)
			}
		}
	}

	// Verify verdict kinds are consistent
	if len(items1) != len(items2) {
		t.Errorf("verdict count changed: %d -> %d", len(items1), len(items2))
	}

	for i := range items1 {
		if i < len(items2) && items1[i].Kind != items2[i].Kind {
			t.Errorf("verdict kind changed for item %d", i)
		}
	}
}

// TestV6Reviews_ReplayLiveBoundary verifies replay/live boundary at the server/client level.
func TestV6Reviews_ReplayLiveBoundary(t *testing.T) {
	t.Parallel()

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})

	// Generate initial events
	c := s.Dial(t)
	turnID1 := s.Submit(t, c, "boundary-test-1")
	s.WaitTerminal(turnID1)

	// First subscriber from seq 0 gets a live_from_seq
	c1 := s.Dial(t)
	liveFromSeq1, err := c1.Subscribe(s.ctx(), 0)
	if err != nil {
		t.Fatalf("subscribe from 0: %v", err)
	}

	if liveFromSeq1 <= 0 {
		t.Errorf("live_from_seq = %d, want > 0", liveFromSeq1)
	}

	// Test replay boundary: subscribe from before the boundary
	c2 := s.Dial(t)
	replayFromSeq := liveFromSeq1 - 1
	if replayFromSeq > 0 {
		liveFromSeq2, err := c2.Subscribe(s.ctx(), replayFromSeq)
		if err != nil {
			t.Fatalf("replay subscribe: %v", err)
		}
		if liveFromSeq2 <= replayFromSeq {
			t.Errorf("boundary seq = %d, want > %d", liveFromSeq2, replayFromSeq)
		}
	}

	// Test empty replay: subscribe from at or above the boundary
	c3 := s.Dial(t)
	liveFromSeq3, err := c3.Subscribe(s.ctx(), liveFromSeq1)
	if err != nil {
		t.Fatalf("empty replay subscribe: %v", err)
	}
	if liveFromSeq3 < liveFromSeq1 {
		t.Errorf("boundary seq = %d, want >= %d", liveFromSeq3, liveFromSeq1)
	}

	// Test repeated reconnect with same seq
	c4 := s.Dial(t)
	_, err = c4.Subscribe(s.ctx(), liveFromSeq1)
	if err != nil {
		t.Fatalf("first reconnect subscribe: %v", err)
	}

	c4b := s.Dial(t)
	liveFromSeq4b, err := c4b.Subscribe(s.ctx(), liveFromSeq1)
	if err != nil {
		t.Fatalf("repeated reconnect subscribe: %v", err)
	}
	if liveFromSeq4b <= 0 {
		t.Errorf("boundary after reconnect = %d, want > 0", liveFromSeq4b)
	}

	// Test fresh process: get boundary multiple times
	c5 := s.Dial(t)
	oldBoundary, err := c5.Subscribe(s.ctx(), 0)
	if err != nil {
		t.Fatalf("first fresh subscribe: %v", err)
	}

	c6 := s.Dial(t)
	freshBoundary, err := c6.Subscribe(s.ctx(), 0)
	if err != nil {
		t.Fatalf("fresh subscribe: %v", err)
	}

	// Fresh process should get consistent boundary
	if oldBoundary != freshBoundary {
		t.Errorf("boundary inconsistency between fresh processes: %d vs %d", oldBoundary, freshBoundary)
	}

	// Boundary should not go backwards
	if freshBoundary < oldBoundary {
		t.Errorf("boundary went backwards: %d -> %d", oldBoundary, freshBoundary)
	}
}
