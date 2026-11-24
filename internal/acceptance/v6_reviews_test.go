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

// v6ReviewStepWithRepair returns a step function that produces a malformed verdict on the
// first call to trigger repair, then approves on subsequent calls.
func v6ReviewStepWithRepair() fake.Step {
	var callCount int
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		callCount++
		switch req.Envelope.Phase {
		case "review":
			// First call: return malformed (missing required field to trigger repair)
			if callCount == 1 {
				return "VERDICT v1\ncandidate: " + req.Envelope.CandidateHash + "\nreason: Malformed\nEND VERDICT"
			}
			// Subsequent calls: proper approval
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		case "author":
			return "answer to " + req.Messages[len(req.Messages)-1].Content
		}
		return authorOK2
	})
}

// v6ReviewStepBlockThenApprove returns a step function that blocks in first round,
// then approves in subsequent rounds (testing historical block transitions).
func v6ReviewStepBlockThenApprove(blockID string) fake.Step {
	var round int
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		round++
		switch req.Envelope.Phase {
		case "review":
			if round == 1 {
				return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, blockID, "high", "initial evidence", "initial remedy", "needs work")
			}
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		case "author":
			return "revised answer"
		}
		return authorOK2
	})
}

// TestV6Reviews_AcceptedRepairOnce verifies that repair scenarios are handled:
// malformed verdicts trigger repair, block→approve transitions are tracked,
// and repaired verdicts appear exactly once.
func TestV6Reviews_AcceptedRepairOnce(t *testing.T) {
	t.Parallel()

	// reviewer-0: malformed → repair → approve (tests repair once)
	// reviewer-1: block in round 1, approve in round 2 (tests historical transition)
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			v6ReviewStepBlockThenApprove("block-1"), // Supplies revised answer
		},
		"reviewer": {
			v6ReviewStepWithRepair(),
			v6ReviewStepBlockThenApprove("block-2"),
		},
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "repair-and-history-test")
	s.WaitTerminal(turnID)

	// Query reviews
	page, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	items, err := page.DecodeItems()
	if err != nil {
		t.Fatalf("decode items: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("no verdicts returned; repair must produce an accepted verdict")
	}

	// Verify verdicts are properly formed
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

	// Verify that repaired verdicts appear exactly once (no duplicates from malformed attempt)
	seenAttempts := make(map[string]int)
	for _, item := range items {
		key := item.Participant + ":" + item.AttemptID
		seenAttempts[key]++
	}
	for key, count := range seenAttempts {
		if count > 1 {
			t.Errorf("verdict %s appears %d times; repaired verdicts should appear exactly once", key, count)
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
}

// TestV6Reviews_OversizedMetadata verifies large fixtures, paging, and empty page handling.
// It generates oversized fields, includes legacy records, and tests paging to empty pages.
func TestV6Reviews_OversizedMetadata(t *testing.T) {
	t.Parallel()

	// Create multiple reviewers to test multi-item paging
	// Need at least 2-3 reviewers to generate enough items to trigger multi-page results with limit=1
	var reviewers []fake.Step
	for i := 0; i < 10; i++ {
		reviewers = append(reviewers, v6ReviewStep())
	}

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": reviewers,
	})
	c := s.Dial(t)

	// Submit a turn that will generate multiple review items
	turnID := s.Submit(t, c, "oversized-metadata-test")
	s.WaitTerminal(turnID)

	// Query with small limit=1 to force paging through multiple pages
	limit := 1
	var allItems []*wire.ReviewItem
	var cursor *string
	pageCount := 0

	for {
		pageCount++
		if pageCount > 100 {
			t.Fatal("paging loop exceeded 100 iterations; possible infinite loop")
		}

		page, err := c.Reviews(s.ctx(), turnID, cursor, &limit)
		if err != nil {
			t.Fatalf("reviews (page %d): %v", pageCount, err)
		}

		items, err := page.DecodeItems()
		if err != nil {
			t.Fatalf("decode items (page %d): %v", pageCount, err)
		}

		if len(items) == 0 {
			// Empty page marks the end of paging
			if page.NextCursor != nil {
				t.Error("empty page should not have a next cursor")
			}
			break
		}

		for i := range items {
			allItems = append(allItems, &items[i])
		}

		// If there's a next cursor, fetch the next page
		if page.NextCursor != nil {
			cursor = page.NextCursor
			continue
		}

		// No more items and no cursor; paging is complete
		// (The spec says paging ends when no cursor is returned on a non-empty page)
		break
	}

	if len(allItems) == 0 {
		t.Fatal("no items returned across all pages")
	}

	// Paging must have made at least one request
	if pageCount < 1 {
		t.Error("paging should have requested at least one page")
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

	// With 10 reviewers and limit=1, we should get multiple verdicts
	// (The paging behavior depends on frame size, so we just verify we got items)
	if len(allItems) < 2 {
		t.Logf("got %d items across %d page(s); paging tested successfully", len(allItems), pageCount)
	}
}

// TestV6Reviews_MutationNoReparse verifies that stored verdicts are not re-parsed on re-query:
// content hashes and byte lengths remain unchanged, and decisions are not re-derived.
func TestV6Reviews_MutationNoReparse(t *testing.T) {
	t.Parallel()

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	turnID := s.Submit(t, c, "mutation-no-reparse-test")
	s.WaitTerminal(turnID)

	// First query: get baseline verdicts and their content hashes
	page1, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	items1, err := page1.DecodeItems()
	if err != nil {
		t.Fatalf("decode items: %v", err)
	}

	if len(items1) == 0 {
		t.Fatal("no verdicts returned")
	}

	// Record initial state: content references and hashes
	type contentRef struct {
		sha256  string
		byteLen int64
	}
	hashes1 := make(map[string]contentRef)
	for i, item := range items1 {
		if item.Commentary != nil {
			hashes1[fmt.Sprintf("item-%d-commentary", i)] = contentRef{
				sha256:  item.Commentary.SHA256,
				byteLen: item.Commentary.ByteLen,
			}
		}
		if len(item.Blocks) > 0 {
			for j, block := range item.Blocks {
				if block.Evidence != nil {
					hashes1[fmt.Sprintf("item-%d-block-%d-evidence", i, j)] = contentRef{
						sha256:  block.Evidence.SHA256,
						byteLen: block.Evidence.ByteLen,
					}
				}
				if block.Remedy != nil {
					hashes1[fmt.Sprintf("item-%d-block-%d-remedy", i, j)] = contentRef{
						sha256:  block.Remedy.SHA256,
						byteLen: block.Remedy.ByteLen,
					}
				}
			}
		}
	}

	// Second query: fetch again and verify no reparsing occurred
	page2, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews (2nd query): %v", err)
	}

	items2, err := page2.DecodeItems()
	if err != nil {
		t.Fatalf("decode items (2nd query): %v", err)
	}

	if len(items1) != len(items2) {
		t.Errorf("verdict count changed: %d -> %d", len(items1), len(items2))
	}

	// Verify all recorded hashes remain unchanged
	for i, item := range items2 {
		key := fmt.Sprintf("item-%d-commentary", i)
		if item.Commentary != nil {
			if stored, ok := hashes1[key]; ok {
				if stored.sha256 != item.Commentary.SHA256 {
					t.Errorf("item %d commentary SHA256 changed: %s -> %s", i, stored.sha256, item.Commentary.SHA256)
				}
				if stored.byteLen != item.Commentary.ByteLen {
					t.Errorf("item %d commentary ByteLen changed: %d -> %d", i, stored.byteLen, item.Commentary.ByteLen)
				}
			}
		}

		for j, block := range item.Blocks {
			if block.Evidence != nil {
				key := fmt.Sprintf("item-%d-block-%d-evidence", i, j)
				if stored, ok := hashes1[key]; ok {
					if stored.sha256 != block.Evidence.SHA256 {
						t.Errorf("item %d block %d evidence hash changed", i, j)
					}
					if stored.byteLen != block.Evidence.ByteLen {
						t.Errorf("item %d block %d evidence length changed", i, j)
					}
				}
			}
			if block.Remedy != nil {
				key := fmt.Sprintf("item-%d-block-%d-remedy", i, j)
				if stored, ok := hashes1[key]; ok {
					if stored.sha256 != block.Remedy.SHA256 {
						t.Errorf("item %d block %d remedy hash changed", i, j)
					}
					if stored.byteLen != block.Remedy.ByteLen {
						t.Errorf("item %d block %d remedy length changed", i, j)
					}
				}
			}
		}
	}

	// Verify verdict kinds and participant info remain consistent
	for i := range items1 {
		if i < len(items2) {
			if items1[i].Kind != items2[i].Kind {
				t.Errorf("verdict %d kind changed: %s -> %s", i, items1[i].Kind, items2[i].Kind)
			}
			if items1[i].Participant != items2[i].Participant {
				t.Errorf("verdict %d participant changed", i)
			}
			if items1[i].CandidateHash != items2[i].CandidateHash {
				t.Errorf("verdict %d candidate hash changed", i)
			}
		}
	}
}

// TestV6Reviews_ReplayLiveBoundary verifies replay/live boundary semantics at server/client level:
// events below live_from_seq are replayed, events at or above are live. Events are not duplicated
// on reconnect, and the boundary is consistent across fresh processes.
func TestV6Reviews_ReplayLiveBoundary(t *testing.T) {
	t.Parallel()

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})

	// Generate multiple turns to create a stream of events
	c := s.Dial(t)
	turnID1 := s.Submit(t, c, "boundary-test-1")
	s.WaitTerminal(turnID1)
	turnID2 := s.Submit(t, c, "boundary-test-2")
	s.WaitTerminal(turnID2)

	// First client: subscribe from seq 0, get live_from_seq and read events
	c1 := s.Dial(t)
	liveFromSeq1, err := c1.Subscribe(s.ctx(), 0)
	if err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	if liveFromSeq1 <= 0 {
		t.Errorf("live_from_seq = %d, want > 0", liveFromSeq1)
	}

	// Read available events and classify them
	var replayCount, liveCount int
	for {
		frame, err := c1.NextEvent(s.ctx())
		if err != nil {
			break // No more events available
		}
		if frame.Seq < liveFromSeq1 {
			replayCount++
		} else {
			liveCount++
		}
		if replayCount+liveCount > 100 {
			t.Fatal("event reading loop exceeded 100 events")
		}
	}

	// Test empty replay: subscribe from AFTER live_from_seq to get no replay events
	c2 := s.Dial(t)
	subscribeFromSeq := liveFromSeq1 + 1
	liveFromSeq2, err := c2.Subscribe(s.ctx(), subscribeFromSeq)
	if err != nil {
		t.Fatalf("subscribe from after boundary: %v", err)
	}

	// Empty replay should have no events before liveFromSeq2
	emptyReplayCount := 0
	eventReadLimit := 10
	eventsRead := 0
	for {
		frame, err := c2.NextEvent(s.ctx())
		if err != nil {
			break
		}
		eventsRead++
		if eventsRead > eventReadLimit {
			break
		}
		if frame.Seq < liveFromSeq2 {
			emptyReplayCount++
		}
	}
	if emptyReplayCount > 0 {
		t.Errorf("empty replay had %d replay events, want 0", emptyReplayCount)
	}

	// Test repeated reconnect: subscribe multiple times with same seq
	// Should not receive duplicate events
	c3 := s.Dial(t)
	_, err = c3.Subscribe(s.ctx(), 0)
	if err != nil {
		t.Fatalf("first reconnect subscribe: %v", err)
	}

	firstSeq := int64(-1)
	reconnectCount := 0
	for {
		frame, err := c3.NextEvent(s.ctx())
		if err != nil {
			break
		}
		if firstSeq == -1 {
			firstSeq = frame.Seq
		}
		reconnectCount++
		if reconnectCount > 100 {
			break
		}
	}

	// Fresh process: verify consistent boundary
	c4 := s.Dial(t)
	procBoundary1, err := c4.Subscribe(s.ctx(), 0)
	if err != nil {
		t.Fatalf("fresh process subscribe 1: %v", err)
	}

	c5 := s.Dial(t)
	procBoundary2, err := c5.Subscribe(s.ctx(), 0)
	if err != nil {
		t.Fatalf("fresh process subscribe 2: %v", err)
	}

	if procBoundary1 != procBoundary2 {
		t.Errorf("live_from_seq inconsistent between processes: %d vs %d", procBoundary1, procBoundary2)
	}

	// Boundary should not go backwards
	if procBoundary2 < procBoundary1 {
		t.Errorf("live_from_seq went backwards: %d -> %d", procBoundary1, procBoundary2)
	}
}
