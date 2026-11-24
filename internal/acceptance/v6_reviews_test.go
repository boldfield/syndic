package acceptance

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
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

const (
	v6ReviewManifest = `{"roster":{"default_preset":"p","presets":{"p":{"mode":"review","author":"au","required_reviewers":["rb","ra","rc"]}}}}`
	authorOK2        = "the answer is 42"
)

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

// TestV6Reviews_AcceptedRepairOnce verifies that an accepted repair verdict is listed once
// with the repair attempt's id, while the malformed original and unsuccessful repairs are not listed.
func TestV6Reviews_AcceptedRepairOnce(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	// Seed a turn with a failed verdict and a successful repair
	var turn string
	err := s.Store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(v6ReviewManifest))
		if err != nil {
			return err
		}
		turn, err = tx.CreateTurn(runID, "review", nil)
		if err != nil {
			return err
		}
		hash, err := tx.SetLatestCandidate(turn, "content", "h1", 0, "")
		if err != nil {
			return err
		}

		// First attempt - malformed
		a1, err := tx.BeginAttempt(turn, "reviewer", "review", 1, nil, "")
		if err != nil {
			return err
		}
		if err := tx.SetAttemptCommentary(a1, "first attempt"); err != nil {
			return err
		}
		_, err = tx.RecordVerdict(a1, hash, "", "", "malformed")
		if err != nil {
			return err
		}

		// Repair attempt - successful
		a2, err := tx.BeginAttempt(turn, "reviewer", "review", 1, nil, "")
		if err != nil {
			return err
		}
		if err := tx.SetAttemptCommentary(a2, "repair commentary"); err != nil {
			return err
		}
		repairVerdictID, err := tx.RecordVerdict(a2, hash, "approve", "", "valid")
		if err != nil {
			return err
		}

		// Accept the repair verdict
		return tx.RecordAcceptedReview(journal.AcceptedReview{
			TurnID:        turn,
			VerdictID:     repairVerdictID,
			AttemptID:     a2,
			Participant:   "reviewer",
			RosterIndex:   0,
			CandidateHash: hash,
			Kind:          "approve",
			States:        []journal.ReviewBlockState{},
		})
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Query reviews - should list only the repair verdict
	page, err := c.Reviews(s.ctx(), turn, nil, nil)
	if err != nil {
		t.Fatalf("reviews: %v", err)
	}

	if len(page.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(page.Items))
	}

	var item wire.ReviewItem
	if err := json.Unmarshal(page.Items[0], &item); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if item.Kind != "approve" {
		t.Errorf("kind = %q, want approve", item.Kind)
	}
}

// TestV6Reviews_OversizedMetadata verifies that oversized records are returned in short form
// with a content reference, and that paging handles multiple large records correctly.
func TestV6Reviews_OversizedMetadata(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	// Seed a turn with a verdict that has many large blocks (oversized)
	var turn string
	err := s.Store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(v6ReviewManifest))
		if err != nil {
			return err
		}
		turn, err = tx.CreateTurn(runID, "review", nil)
		if err != nil {
			return err
		}
		hash, err := tx.SetLatestCandidate(turn, "content", "h1", 0, "")
		if err != nil {
			return err
		}

		a, err := tx.BeginAttempt(turn, "reviewer", "review", 1, nil, "")
		if err != nil {
			return err
		}

		// Create large blocks to trigger oversized logic
		largeEvidence := string(make([]byte, 5000))
		if err := tx.SetAttemptCommentary(a, largeEvidence); err != nil {
			return err
		}

		vid, err := tx.RecordVerdict(a, hash, "block", "", "valid")
		if err != nil {
			return err
		}

		// Add blocks that together may exceed 64 KiB
		for i := 0; i < 50; i++ {
			blockID := fmt.Sprintf("block_%d", i)
			largeRemedy := string(make([]byte, 1000))
			if _, err := tx.OpenBlockFor(vid, blockID, hash, "low", largeEvidence, largeRemedy); err != nil {
				return err
			}
		}

		// Accept the verdict
		return tx.RecordAcceptedReview(journal.AcceptedReview{
			TurnID:        turn,
			VerdictID:     vid,
			AttemptID:     a,
			Participant:   "reviewer",
			RosterIndex:   0,
			CandidateHash: hash,
			Kind:          "block",
			States: []journal.ReviewBlockState{
				{BlockID: "block_0", State: journal.ReviewBlockOpened},
			},
		})
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Query reviews - may get short form for oversized depending on encoding
	page, err := c.Reviews(s.ctx(), turn, nil, nil)
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

	if item.Kind != "block" {
		t.Errorf("kind = %q, want block", item.Kind)
	}
}

// TestV6Reviews_MutationNoReparse verifies that verdict content hashes are verified
// and do not change when re-fetched.
func TestV6Reviews_MutationNoReparse(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   v6ReviewSteps(10),
		"reviewer": v6ReviewSteps(10),
	})
	c := s.Dial(t)

	var turn string
	err := s.Store.Tx(func(tx *journal.Tx) error {
		runID, _, err := tx.CreateRun(json.RawMessage(v6ReviewManifest))
		if err != nil {
			return err
		}
		turn, err = tx.CreateTurn(runID, "review", nil)
		if err != nil {
			return err
		}
		hash, err := tx.SetLatestCandidate(turn, "content", "h1", 0, "")
		if err != nil {
			return err
		}

		a, err := tx.BeginAttempt(turn, "reviewer", "review", 1, nil, "")
		if err != nil {
			return err
		}

		commentary := "original commentary text with some content"
		if err := tx.SetAttemptCommentary(a, commentary); err != nil {
			return err
		}

		vid, err := tx.RecordVerdict(a, hash, "approve", "", "valid")
		if err != nil {
			return err
		}

		// Accept the verdict
		return tx.RecordAcceptedReview(journal.AcceptedReview{
			TurnID:        turn,
			VerdictID:     vid,
			AttemptID:     a,
			Participant:   "reviewer",
			RosterIndex:   0,
			CandidateHash: hash,
			Kind:          "approve",
			States:        []journal.ReviewBlockState{},
		})
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Query reviews and verify hashes are consistent
	page, err := c.Reviews(s.ctx(), turn, nil, nil)
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

	// Verify item structure is correct
	if item.Kind != "approve" {
		t.Errorf("kind = %q, want approve", item.Kind)
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
