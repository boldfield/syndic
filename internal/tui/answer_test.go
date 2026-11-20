package tui

import (
	"errors"
	"strings"
	"testing"
)

func TestRenderAnswerEmptyWhenNoTurn(t *testing.T) {
	s := NewState()
	cc := NewContentCache()
	output := renderAnswer(s, 80, 24, cc, false, 0, "", "", nil)
	if output != "" {
		t.Fatalf("renderAnswer with no turn: got %q, want empty", output)
	}
}

func TestRenderAnswerApproved(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	hash := strings.Repeat("a", 64)
	s.Turns = map[string]TurnInfo{
		"t1": {
			ID:            "t1",
			State:         "approved",
			Reviewers:     []string{"r1", "r2"},
			CandidateHash: &hash,
		},
	}
	s.Attempts = map[string]AttemptInfo{
		"a1": {ID: "a1", TurnID: "t1", Phase: "review", Participant: "r1", Status: "complete"},
		"a2": {ID: "a2", TurnID: "t1", Phase: "review", Participant: "r2", Status: "complete"},
	}
	s.Verdicts = map[string]VerdictInfo{
		"v1": {ID: "v1", AttemptID: "a1", Parsed: true, Class: "valid"},
		"v2": {ID: "v2", AttemptID: "a2", Parsed: true, Class: "valid"},
	}

	cc := NewContentCache()
	cc.Store("candidate:t1", "the answer is 42", nil)

	output := renderAnswer(s, 80, 24, cc, false, 0, "t1", "t1", nil)
	if !strings.Contains(output, "the answer is 42") {
		t.Fatalf("renderAnswer approved: candidate text not found: %s", output)
	}
	if !strings.Contains(output, "approved by") {
		t.Fatalf("renderAnswer approved: approval line not found: %s", output)
	}
	if !strings.Contains(output, "aaaaaa") { // first 12 chars of hash
		t.Fatalf("renderAnswer approved: hash prefix not found: %s", output)
	}
	if !strings.Contains(output, "r1") || !strings.Contains(output, "r2") {
		t.Fatalf("renderAnswer approved: reviewer ids not found: %s", output)
	}
}

func TestRenderAnswerSoloComplete(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	hash := strings.Repeat("b", 64)
	s.Turns = map[string]TurnInfo{
		"t1": {
			ID:            "t1",
			State:         "solo_complete",
			Author:        "a1",
			CandidateHash: &hash,
		},
	}
	s.Attempts = map[string]AttemptInfo{
		"a1": {
			ID:          "a1",
			TurnID:      "t1",
			Phase:       "author",
			Status:      "complete",
			Participant: "a1",
		},
	}

	cc := NewContentCache()
	cc.Store("attempt:a1", "solo result", nil)

	output := renderAnswer(s, 80, 24, cc, false, 0, "t1", "t1", nil)
	if !strings.Contains(output, "UNREVIEWED (solo)") {
		t.Fatalf("renderAnswer solo_complete: banner not found: %s", output)
	}
	if !strings.Contains(output, "solo result") {
		t.Fatalf("renderAnswer solo_complete: candidate text not found: %s", output)
	}
}

func TestRenderAnswerIncomplete(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	reason := "block:b1"
	hash := strings.Repeat("c", 64)
	s.Turns = map[string]TurnInfo{
		"t1": {
			ID:             "t1",
			State:          "incomplete",
			TerminalReason: &reason,
			CandidateHash:  &hash,
		},
	}

	cc := NewContentCache()
	cc.Store("candidate:t1", "incomplete result", nil)

	output := renderAnswer(s, 80, 24, cc, false, 0, "t1", "t1", nil)
	if !strings.Contains(output, reason) {
		t.Fatalf("renderAnswer incomplete: reason not found: %s", output)
	}
	if !strings.Contains(output, "INCOMPLETE, not approved") {
		t.Fatalf("renderAnswer incomplete: banner not found: %s", output)
	}
	if !strings.Contains(output, "incomplete result") {
		t.Fatalf("renderAnswer incomplete: candidate text not found: %s", output)
	}
}

func TestRenderAnswerOverridden(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	reason := "user request"
	hash := strings.Repeat("d", 64)
	s.Turns = map[string]TurnInfo{
		"t1": {
			ID:             "t1",
			State:          "overridden",
			TerminalReason: &reason,
			CandidateHash:  &hash,
		},
	}

	cc := NewContentCache()
	cc.Store("candidate:t1", "overridden result", nil)

	output := renderAnswer(s, 80, 24, cc, false, 0, "t1", "t1", nil)
	if !strings.Contains(output, "HUMAN OVERRIDE") {
		t.Fatalf("renderAnswer overridden: banner not found: %s", output)
	}
	if !strings.Contains(output, reason) {
		t.Fatalf("renderAnswer overridden: reason not found: %s", output)
	}
	if !strings.Contains(output, "overridden result") {
		t.Fatalf("renderAnswer overridden: candidate text not found: %s", output)
	}
}

func TestRenderAnswerCancelledBeforeCandidate(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns = map[string]TurnInfo{
		"t1": {
			ID:    "t1",
			State: "cancelled",
		},
	}

	cc := NewContentCache()
	output := renderAnswer(s, 80, 24, cc, false, 0, "t1", "t1", nil)
	if !strings.Contains(output, "cancelled before candidate") {
		t.Fatalf("renderAnswer cancelled before candidate: message not found: %s", output)
	}
}

func TestRenderAnswerCancelledAfterCandidate(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	hash := strings.Repeat("e", 64)
	s.Turns = map[string]TurnInfo{
		"t1": {
			ID:            "t1",
			State:         "cancelled",
			CandidateHash: &hash,
		},
	}

	cc := NewContentCache()
	cc.Store("candidate:t1", "cancelled result", nil)

	output := renderAnswer(s, 80, 24, cc, false, 0, "t1", "t1", nil)
	if !strings.Contains(output, "CANCELLED, not approved") {
		t.Fatalf("renderAnswer cancelled after candidate: banner not found: %s", output)
	}
	if !strings.Contains(output, "cancelled result") {
		t.Fatalf("renderAnswer cancelled after candidate: candidate text not found: %s", output)
	}
}

func TestRenderAnswerLoadingContent(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	hash := strings.Repeat("f", 64)
	s.Turns = map[string]TurnInfo{
		"t1": {
			ID:            "t1",
			State:         "approved",
			Reviewers:     []string{"r1"},
			CandidateHash: &hash,
		},
	}

	cc := NewContentCache()
	// Don't store content, should show "loading…"

	output := renderAnswer(s, 80, 24, cc, false, 0, "t1", "t1", nil)
	if !strings.Contains(output, "loading…") {
		t.Fatalf("renderAnswer loading: loading message not found: %s", output)
	}
}

func TestRenderAnswerContentError(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	hash := strings.Repeat("f", 64)
	s.Turns = map[string]TurnInfo{
		"t1": {
			ID:            "t1",
			State:         "approved",
			Reviewers:     []string{"r1"},
			CandidateHash: &hash,
		},
	}

	cc := NewContentCache()
	cc.Store("candidate:t1", "", errors.New("fetch failed")) // Store with error

	output := renderAnswer(s, 80, 24, cc, false, 0, "t1", "t1", nil)
	if !strings.Contains(output, "error") {
		t.Fatalf("renderAnswer error: error message not found: %s", output)
	}
}

func TestRenderAnswerOpenTurn(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns = map[string]TurnInfo{
		"t1": {
			ID:    "t1",
			State: "submitted",
		},
	}

	cc := NewContentCache()
	output := renderAnswer(s, 80, 24, cc, false, 0, "t1", "t1", nil)
	if !strings.Contains(output, "turn in progress") {
		t.Fatalf("renderAnswer open turn: in progress message not found: %s", output)
	}
}

func TestContentCacheGet(t *testing.T) {
	cc := NewContentCache()

	// Before storing
	item := cc.Get("ref1")
	if item.Ready {
		t.Fatal("ContentCache.Get: item should not be ready")
	}

	// After storing
	cc.Store("ref1", "data", nil)
	item = cc.Get("ref1")
	if !item.Ready {
		t.Fatal("ContentCache.Get: item should be ready")
	}
	if item.Data != "data" {
		t.Fatalf("ContentCache.Get: data = %q, want %q", item.Data, "data")
	}
	if item.Err != nil {
		t.Fatal("ContentCache.Get: err should be nil")
	}
}

func TestContentCacheError(t *testing.T) {
	cc := NewContentCache()
	testErr := errors.New("test error")
	cc.Store("ref1", "", testErr)

	item := cc.Get("ref1")
	if !item.Ready {
		t.Fatal("ContentCache.Get with error: item should be ready")
	}
	if item.Data != "" {
		t.Fatalf("ContentCache.Get with error: data = %q, want empty", item.Data)
	}
	if item.Err != testErr {
		t.Fatalf("ContentCache.Get with error: err = %v, want test error", item.Err)
	}
}
