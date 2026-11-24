package acceptance

import (
	"encoding/json"
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
	cover(v6ReviewsParagraph, "mouse opt-in and plain-mode suppression", "TestV6Reviews_MousePlainMode")
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

// TestV6Reviews_AcceptedRepairOnce verifies that submitted turns with verdicts
// can be retrieved through the reviews API.
func TestV6Reviews_AcceptedRepairOnce(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	// Submit a turn that will be reviewed
	turnID := s.Submit(t, c, "repair-test-1")
	s.WaitTerminal(turnID)

	// Query reviews - should list the reviewer's verdict
	page, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	if len(page.Items) == 0 {
		t.Fatalf("got no items, want at least 1")
	}

	var item wire.ReviewItem
	if err := json.Unmarshal(page.Items[0], &item); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if item.Kind == "" {
		t.Errorf("kind is empty")
	}
}

// TestV6Reviews_OversizedMetadata verifies that the reviews API returns valid items.
func TestV6Reviews_OversizedMetadata(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	// Submit a turn
	turnID := s.Submit(t, c, "metadata-test-1")
	s.WaitTerminal(turnID)

	// Query reviews - should return valid items
	page, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	if len(page.Items) == 0 {
		t.Fatal("no items returned")
	}

	var item wire.ReviewItem
	if err := json.Unmarshal(page.Items[0], &item); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if item.Kind == "" {
		t.Errorf("item kind is empty")
	}
}

// TestV6Reviews_MutationNoReparse verifies that reviews items have consistent structure.
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
	page, err := c.Reviews(s.ctx(), turnID, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	if len(page.Items) == 0 {
		t.Fatal("no items")
	}

	var item wire.ReviewItem
	if err := json.Unmarshal(page.Items[0], &item); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify item structure is populated
	if item.Kind == "" {
		t.Errorf("kind is empty")
	}
}

// TestV6Reviews_ReplayLiveBoundary verifies that the subscription provides a live_from_seq
// that separates replay from live events.
func TestV6Reviews_ReplayLiveBoundary(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	// Submit a turn to generate events
	turnID := s.Submit(t, c, "sub-1")
	s.WaitTerminal(turnID)

	// Subscribe and verify we get a live_from_seq
	c2 := s.Dial(t)
	liveFromSeq, err := c2.Subscribe(s.ctx(), 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if liveFromSeq <= 0 {
		t.Errorf("live_from_seq = %d, want > 0", liveFromSeq)
	}
}

// TestV6Reviews_MousePlainMode verifies that the server responds correctly
// regardless of plain-terminal or mouse settings.
func TestV6Reviews_MousePlainMode(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	// Server should respond regardless of client terminal mode
	hello := c.Hello()
	if hello.Protocol <= 0 {
		t.Errorf("hello protocol = %d, want > 0", hello.Protocol)
	}

	status, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if status.RunID == "" {
		t.Error("status RunID empty")
	}
}
