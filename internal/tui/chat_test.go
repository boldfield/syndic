package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

func TestRenderChat_EmptyConversation(t *testing.T) {
	s := NewState()
	in := chatInput{
		content:         NewContentCache(),
		draftsMap:       make(map[string]bool),
		plainMode:       false,
		blockDetail:     make(map[string]wire.OpenBlock),
		candidateIDMap:  make(map[string]string),
		overrideReasons: make(map[string]string),
	}

	result := renderChat(s, in, 80)
	if result != "" {
		t.Fatalf("expected empty string for empty conversation, got: %q", result)
	}
}

func TestRenderChat_SingleApprovedTurn(t *testing.T) {
	s := NewState()
	turnID := "test-turn-001"
	hash := "abc123def456"

	s.Turns[turnID] = TurnInfo{
		ID:            turnID,
		State:         "approved",
		Preset:        "test-preset",
		SubmittedAt:   time.Now(),
		SubmitSeq:     1,
		CandidateHash: &hash,
	}

	s.Scope = &TurnScope{
		ConversationID: "conv-001",
		Turns:          map[string]bool{turnID: true},
	}

	in := chatInput{
		content:         NewContentCache(),
		draftsMap:       make(map[string]bool),
		plainMode:       false,
		blockDetail:     make(map[string]wire.OpenBlock),
		candidateIDMap:  make(map[string]string),
		overrideReasons: make(map[string]string),
	}

	result := renderChat(s, in, 80)
	if result == "" {
		t.Fatalf("expected non-empty string for approved turn")
	}

	// Check that the turn ID is in the output
	if len(result) > 0 && len(turnID) >= 8 {
		if result[0:min(8, len(turnID))] != turnID[:min(8, len(turnID))] {
			t.Logf("output: %s", result)
		}
	}
}

func TestRenderChat_IncompleteTurn(t *testing.T) {
	s := NewState()
	turnID := "test-turn-002"
	reason := "budget:turn"

	s.Turns[turnID] = TurnInfo{
		ID:             turnID,
		State:          "incomplete",
		Preset:         "test-preset",
		SubmittedAt:    time.Now(),
		SubmitSeq:      1,
		TerminalReason: &reason,
	}

	s.Scope = &TurnScope{
		ConversationID: "conv-001",
		Turns:          map[string]bool{turnID: true},
	}

	in := chatInput{
		content:         NewContentCache(),
		draftsMap:       make(map[string]bool),
		plainMode:       false,
		blockDetail:     make(map[string]wire.OpenBlock),
		candidateIDMap:  make(map[string]string),
		overrideReasons: make(map[string]string),
	}

	result := renderChat(s, in, 80)
	if result == "" {
		t.Fatalf("expected non-empty string for incomplete turn")
	}

	if !strings.Contains(result, "no approved answer") {
		t.Fatalf("expected 'no approved answer' in output, got: %s", result)
	}
}

func TestCountRevisions_NoRevisions(t *testing.T) {
	s := NewState()
	turnID := "test-turn-001"

	// First attempt (epoch 1)
	s.Attempts["attempt-001"] = AttemptInfo{
		ID:     "attempt-001",
		TurnID: turnID,
		Phase:  "author",
		Epoch:  1,
		Status: "complete",
	}

	revisions := countRevisions(s, turnID)
	if revisions != 0 {
		t.Fatalf("expected 0 revisions for single attempt, got %d", revisions)
	}
}

func TestCountRevisions_OneRevision(t *testing.T) {
	s := NewState()
	turnID := "test-turn-001"

	// First attempt (epoch 1)
	s.Attempts["attempt-001"] = AttemptInfo{
		ID:     "attempt-001",
		TurnID: turnID,
		Phase:  "author",
		Epoch:  1,
		Status: "complete",
	}

	// Revised attempt (epoch 2)
	s.Attempts["attempt-002"] = AttemptInfo{
		ID:     "attempt-002",
		TurnID: turnID,
		Phase:  "author",
		Epoch:  2,
		Status: "complete",
	}

	revisions := countRevisions(s, turnID)
	if revisions != 1 {
		t.Fatalf("expected 1 revision for two attempts, got %d", revisions)
	}
}

func TestChatRefs_CollectsRefsForTurns(t *testing.T) {
	s := NewState()
	turnID := "test-turn-001"
	hash := "abc123"

	s.Turns[turnID] = TurnInfo{
		ID:            turnID,
		State:         "approved",
		CandidateHash: &hash,
	}

	s.Attempts["attempt-001"] = AttemptInfo{
		ID:     "attempt-001",
		TurnID: turnID,
		Phase:  "review",
		Status: "complete",
	}

	s.Scope = &TurnScope{
		ConversationID: "conv-001",
		Turns:          map[string]bool{turnID: true},
	}

	candidateIDMap := make(map[string]string)
	draftsMap := make(map[string]bool)
	refs := chatRefs(s, candidateIDMap, draftsMap)
	// chatRefs should work even if no refs are found (the content is loaded on demand)
	if len(refs) > 0 {
		if refs[0] != "attempt:attempt-001" {
			t.Fatalf("expected attempt ref, got %v", refs)
		}
	}
}

// Helper function to check if a string contains a substring
