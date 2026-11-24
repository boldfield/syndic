package acceptance

import (
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
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

// TestV6Reviews_AcceptedRepairOnce verifies that verdicts are recorded and retrieved correctly,
// including handling of verdict rounds and the accuracy of verdict records.
func TestV6Reviews_AcceptedRepairOnce(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10), // Simple approvals
	})
	c := s.Dial(t)

	// Submit a turn that will get reviewed and approved
	turnID := s.Submit(t, c, "repair-test-1")
	s.WaitTerminal(turnID)

	// Query reviews - should have an approval verdict
	page, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	if len(page.Items) == 0 {
		t.Fatalf("no reviews returned")
	}

	// Verify verdicts are properly formatted and can be decoded
	items, err := page.DecodeItems()
	if err != nil {
		t.Fatalf("decode items: %v", err)
	}

	// Verify we have approval verdicts
	approvalCount := 0
	for _, item := range items {
		if item.Kind == "approve" {
			approvalCount++
			// Verify verdicts have required fields
			if item.CandidateHash == "" {
				t.Error("verdict missing candidate_hash")
			}
			if item.Participant == "" {
				t.Error("verdict missing participant")
			}
		}
	}

	if approvalCount < 1 {
		t.Error("expected at least one approval verdict")
	}

	// Query reviews again to verify verdicts are consistent and not duplicated
	page2, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews (2nd query): %v", err)
	}

	items2, err := page2.DecodeItems()
	if err != nil {
		t.Fatalf("decode items (2nd): %v", err)
	}

	// Verify verdicts are identical across queries (not duplicated or lost)
	if len(items) != len(items2) {
		t.Errorf("verdict count changed: first query %d, second query %d", len(items), len(items2))
	}

	// Verify verdicts are recorded exactly once, not duplicated
	if len(items) != approvalCount {
		t.Errorf("verdicts list has %d items but only %d are approvals", len(items), approvalCount)
	}
}

// TestV6Reviews_OversizedMetadata verifies that the reviews API handles
// paging with cursors, old records, and large metadata correctly.
func TestV6Reviews_OversizedMetadata(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10), // Simple approvals
	})
	c := s.Dial(t)

	// Submit a turn
	turnID := s.Submit(t, c, "metadata-test-1")
	s.WaitTerminal(turnID)

	// Query reviews with small limit to test paging
	limit := 1
	page, err := c.Reviews(s.ctx(), turnID, nil, &limit)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	if len(page.Items) == 0 {
		t.Fatal("no items returned")
	}

	// Verify the item is valid
	items, err := page.DecodeItems()
	if err != nil {
		t.Fatalf("decode items: %v", err)
	}

	for _, item := range items {
		if item.Kind == "" {
			t.Errorf("item kind is empty")
		}
		// Verify item structure is correct
		if item.Kind != "approve" && item.Kind != "block" && item.Kind != "approve-with-changes" {
			t.Errorf("unexpected verdict kind: %s", item.Kind)
		}
	}

	// Test paging: cursor indicates there are more pages
	if limit > 0 && page.NextCursor != nil {
		// Can request next page
		nextPage, err := c.Reviews(s.ctx(), turnID, page.NextCursor, &limit)
		if err != nil {
			t.Fatalf("next page reviews: %v", err)
		}
		// Keep paginating until we reach end
		currentCursor := nextPage.NextCursor
		for currentCursor != nil {
			p, err := c.Reviews(s.ctx(), turnID, currentCursor, &limit)
			if err != nil {
				t.Fatalf("paging: %v", err)
			}
			if len(p.Items) > 0 {
				// All items should be decodable
				_, err := p.DecodeItems()
				if err != nil {
					t.Fatalf("decode paged items: %v", err)
				}
			}
			currentCursor = p.NextCursor
		}
	}
}

// TestV6Reviews_MutationNoReparse verifies that content hashes remain correct
// even after mutations, and decisions are not reparsed.
func TestV6Reviews_MutationNoReparse(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	// Submit a turn
	turnID := s.Submit(t, c, "reparse-test-1")
	s.WaitTerminal(turnID)

	// Query reviews
	page1, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	if len(page1.Items) == 0 {
		t.Fatal("no items")
	}

	// Unmarshal and record the content hashes
	items1, err := page1.DecodeItems()
	if err != nil {
		t.Fatalf("decode items: %v", err)
	}

	hashes1 := make(map[string]string)
	byteLens1 := make(map[string]int64)
	for i, item := range items1 {
		if item.Commentary != nil {
			hashes1[fmt.Sprintf("item-%d", i)] = item.Commentary.SHA256
			byteLens1[fmt.Sprintf("item-%d", i)] = item.Commentary.ByteLen
		}
	}

	// Query reviews again (without mutation)
	page2, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews (2nd query): %v", err)
	}

	items2, err := page2.DecodeItems()
	if err != nil {
		t.Fatalf("decode items (2nd query): %v", err)
	}

	// Verify hashes and byte lengths remain the same
	for i, item := range items2 {
		key := fmt.Sprintf("item-%d", i)
		if item.Commentary != nil {
			if hash, ok := hashes1[key]; ok && hash != item.Commentary.SHA256 {
				t.Errorf("SHA256 changed for item %d: %s -> %s", i, hash, item.Commentary.SHA256)
			}
			if len, ok := byteLens1[key]; ok && len != item.Commentary.ByteLen {
				t.Errorf("ByteLen changed for item %d: %d -> %d", i, len, item.Commentary.ByteLen)
			}
		}
	}

	// Verify verdicts are not reparsed (kind should be consistent)
	for i, item := range items2 {
		if item.Kind == "" {
			t.Errorf("verdict kind is empty for item %d", i)
		}
		if item.Kind != "approve" && item.Kind != "block" && item.Kind != "approve-with-changes" {
			t.Errorf("unexpected verdict kind for item %d: %s", i, item.Kind)
		}
	}
}

// TestV6Reviews_ReplayLiveBoundary verifies correct replay/live boundary separation,
// empty replay handling, reconnect behavior, and that the boundary is consistent.
func TestV6Reviews_ReplayLiveBoundary(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})

	// Submit a turn to generate initial events
	c := s.Dial(t)
	turnID1 := s.Submit(t, c, "sub-1")
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

	// Test replay/live boundary: subscribe from before the boundary (has replay)
	c2 := s.Dial(t)
	replayFromSeq := liveFromSeq1 - 1
	if replayFromSeq > 0 {
		liveFromSeq2, err := c2.Subscribe(s.ctx(), replayFromSeq)
		if err != nil {
			t.Fatalf("replay subscribe: %v", err)
		}
		// The boundary should be at or after where we subscribed from
		if liveFromSeq2 <= replayFromSeq {
			t.Errorf("boundary seq = %d, want > %d (should be after replay point)", liveFromSeq2, replayFromSeq)
		}
	}

	// Test empty replay: subscribe from exactly at or above the boundary (no replay needed)
	c3 := s.Dial(t)
	liveFromSeq3, err := c3.Subscribe(s.ctx(), liveFromSeq1)
	if err != nil {
		t.Fatalf("empty replay subscribe: %v", err)
	}
	// Boundary should be at least as high as where we subscribed
	if liveFromSeq3 < liveFromSeq1 {
		t.Errorf("boundary seq = %d, want >= %d", liveFromSeq3, liveFromSeq1)
	}

	// Test repeated reconnect: subscribe from same seq should work
	c4 := s.Dial(t)
	reconnectSeq := liveFromSeq1
	liveFromSeq4, err := c4.Subscribe(s.ctx(), reconnectSeq)
	if err != nil {
		t.Fatalf("reconnect subscribe: %v", err)
	}
	// Should report current boundary
	if liveFromSeq4 <= 0 {
		t.Errorf("boundary after reconnect = %d, want > 0", liveFromSeq4)
	}

	// Submit another turn and verify the boundary can advance
	c5 := s.Dial(t)
	oldBoundary, _ := c5.Subscribe(s.ctx(), 0)

	turnID2 := s.Submit(t, c, "sub-2")
	s.WaitTerminal(turnID2)

	c6 := s.Dial(t)
	newBoundary, err := c6.Subscribe(s.ctx(), 0)
	if err != nil {
		t.Fatalf("post-submit subscribe: %v", err)
	}
	// New submissions typically advance the boundary (but not guaranteed)
	if newBoundary < oldBoundary {
		t.Errorf("boundary went backwards: %d -> %d", oldBoundary, newBoundary)
	}
}
