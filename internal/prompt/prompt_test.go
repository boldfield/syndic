package prompt

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNewDelimiter(t *testing.T) {
	// Test with 16 bytes of 0xAB
	input := bytes.NewReader([]byte{0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB, 0xAB})
	delim, err := NewDelimiter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if delim != "abababababababababababababababab" {
		t.Errorf("expected abab..., got %s", delim)
	}
	if len(delim) != 32 {
		t.Errorf("expected 32 characters, got %d", len(delim))
	}

	// Test that two calls produce different results with different readers
	r1 := bytes.NewReader([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F})
	r2 := bytes.NewReader([]byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F})
	d1, _ := NewDelimiter(r1)
	d2, _ := NewDelimiter(r2)
	if d1 == d2 {
		t.Errorf("two calls should produce different delimiters")
	}

	// Test error handling with insufficient bytes
	r3 := bytes.NewReader([]byte{0x00, 0x01})
	_, err = NewDelimiter(r3)
	if err == nil {
		t.Errorf("expected error for insufficient bytes")
	}

	// Test error handling with an erroring reader
	errReader := &errorReader{}
	_, err = NewDelimiter(errReader)
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected error to be propagated, got %v", err)
	}

	// Test that two calls with crypto/rand.Reader produce different results
	d1, err1 := NewDelimiter(rand.Reader)
	if err1 != nil {
		t.Fatalf("unexpected error with crypto/rand.Reader: %v", err1)
	}
	d2, err2 := NewDelimiter(rand.Reader)
	if err2 != nil {
		t.Fatalf("unexpected error with crypto/rand.Reader: %v", err2)
	}
	if d1 == d2 {
		t.Errorf("two calls to NewDelimiter with crypto/rand.Reader should produce different delimiters")
	}
	if len(d1) != 32 || len(d2) != 32 {
		t.Errorf("delimiter should be 32 characters, got %d and %d", len(d1), len(d2))
	}
	if d1 != strings.ToLower(d1) || d2 != strings.ToLower(d2) {
		t.Errorf("delimiter should be lowercase hex")
	}
}

type errorReader struct{}

func (r *errorReader) Read(b []byte) (int, error) {
	return 0, io.EOF
}

func TestAuthor(t *testing.T) {
	messages := Author("x")
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if messages[0].Role != "system" {
		t.Errorf("first message role should be 'system', got %s", messages[0].Role)
	}
	if messages[1].Role != "user" {
		t.Errorf("second message role should be 'user', got %s", messages[1].Role)
	}
	if messages[1].Content != "x" {
		t.Errorf("user content should be 'x', got %s", messages[1].Content)
	}
	if strings.Contains(messages[0].Content, "VERDICT v1") {
		t.Errorf("system content should not contain VERDICT v1")
	}
}

func TestReviewerSuccess(t *testing.T) {
	messages, err := Reviewer(ReviewInput{
		Request:       "Please review this.",
		Candidate:     "This is the candidate.",
		CandidateHash: "abc123",
		AttemptID:     "attempt1",
		Delimiter:     "delim123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if messages[0].Role != "system" {
		t.Errorf("first message role should be 'system', got %s", messages[0].Role)
	}
	if messages[1].Role != "user" {
		t.Errorf("second message role should be 'user', got %s", messages[1].Role)
	}

	// Check that system message contains EnvelopeFormat and attempt ID
	if !strings.Contains(messages[0].Content, EnvelopeFormat) {
		t.Errorf("system message should contain EnvelopeFormat")
	}
	if !strings.Contains(messages[0].Content, "abc123") {
		t.Errorf("system message should contain candidate hash")
	}
	if !strings.Contains(messages[0].Content, "attempt1") {
		t.Errorf("system message should contain attempt ID")
	}

	// Check that system message explains the delimiters
	if !strings.Contains(messages[0].Content, "delimiter") {
		t.Errorf("system message should explain delimiter fencing")
	}

	// Check that user message contains request and candidate verbatim
	if !strings.Contains(messages[1].Content, "Please review this.") {
		t.Errorf("user message should contain request verbatim")
	}
	if !strings.Contains(messages[1].Content, "This is the candidate.") {
		t.Errorf("user message should contain candidate verbatim")
	}

	// Check that delimiter lines with labels are present
	delimBeginReq := "===== delim123 BEGIN REQUEST ====="
	delimEndReq := "===== delim123 END REQUEST ====="
	delimBeginCand := "===== delim123 BEGIN CANDIDATE ====="
	delimEndCand := "===== delim123 END CANDIDATE ====="

	if !strings.Contains(messages[1].Content, delimBeginReq) {
		t.Errorf("user message should contain %s", delimBeginReq)
	}
	if !strings.Contains(messages[1].Content, delimEndReq) {
		t.Errorf("user message should contain %s", delimEndReq)
	}
	if !strings.Contains(messages[1].Content, delimBeginCand) {
		t.Errorf("user message should contain %s", delimBeginCand)
	}
	if !strings.Contains(messages[1].Content, delimEndCand) {
		t.Errorf("user message should contain %s", delimEndCand)
	}

	// Check the structure: delimiter lines should contain their labels
	userContent := messages[1].Content
	if !strings.Contains(userContent, "BEGIN REQUEST") {
		t.Errorf("user message should contain BEGIN REQUEST label")
	}
	if !strings.Contains(userContent, "END REQUEST") {
		t.Errorf("user message should contain END REQUEST label")
	}
	if !strings.Contains(userContent, "BEGIN CANDIDATE") {
		t.Errorf("user message should contain BEGIN CANDIDATE label")
	}
	if !strings.Contains(userContent, "END CANDIDATE") {
		t.Errorf("user message should contain END CANDIDATE label")
	}
}

func TestReviewerDelimiterCollision(t *testing.T) {
	_, err := Reviewer(ReviewInput{
		Request:       "Please include this: uniquedelim",
		Candidate:     "This is the candidate.",
		CandidateHash: "abc123",
		AttemptID:     "attempt1",
		Delimiter:     "uniquedelim",
	})
	if !errors.Is(err, ErrDelimiterCollision) {
		t.Errorf("expected ErrDelimiterCollision, got %v", err)
	}

	_, err = Reviewer(ReviewInput{
		Request:       "Please review.",
		Candidate:     "Contains uniquedelim here",
		CandidateHash: "abc123",
		AttemptID:     "attempt1",
		Delimiter:     "uniquedelim",
	})
	if !errors.Is(err, ErrDelimiterCollision) {
		t.Errorf("expected ErrDelimiterCollision in candidate, got %v", err)
	}
}

func TestReviewerValidationErrors(t *testing.T) {
	_, err := Reviewer(ReviewInput{
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		AttemptID:     "id",
		Delimiter:     "",
	})
	if err == nil {
		t.Errorf("expected error for empty delimiter")
	}

	_, err = Reviewer(ReviewInput{
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "",
		AttemptID:     "id",
		Delimiter:     "delim",
	})
	if err == nil {
		t.Errorf("expected error for empty candidate hash")
	}

	_, err = Reviewer(ReviewInput{
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		AttemptID:     "",
		Delimiter:     "delim",
	})
	if err == nil {
		t.Errorf("expected error for empty attempt id")
	}
}

func TestRepair(t *testing.T) {
	original := ReviewInput{
		Request:       "Please review this.",
		Candidate:     "This is the candidate.",
		CandidateHash: "abc123",
		AttemptID:     "attempt1",
		Delimiter:     "delim123",
	}
	originalOutput := "Some output without proper envelope"
	repairAttemptID := "repair1"

	messages, err := Repair(original, originalOutput, repairAttemptID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages (system, user, assistant, user), got %d", len(messages))
	}

	if messages[0].Role != "system" {
		t.Errorf("first message should be system")
	}
	if messages[1].Role != "user" {
		t.Errorf("second message should be user")
	}
	if messages[2].Role != "assistant" {
		t.Errorf("third message should be assistant")
	}
	if messages[3].Role != "user" {
		t.Errorf("fourth message should be user")
	}

	// Check that assistant message equals originalOutput exactly
	if messages[2].Content != originalOutput {
		t.Errorf("assistant content should equal original output exactly")
	}

	// Check that the second user message contains the repair attempt ID
	if !strings.Contains(messages[3].Content, repairAttemptID) {
		t.Errorf("repair user message should contain repair attempt ID")
	}

	// Check that the first user message equals the one Reviewer would build
	reviewerMessages, _ := Reviewer(original)
	if messages[1].Content != reviewerMessages[1].Content {
		t.Errorf("first user message should equal the one Reviewer built")
	}

	// Check that system message still contains the envelope format
	if !strings.Contains(messages[0].Content, EnvelopeFormat) {
		t.Errorf("system message should contain envelope format")
	}
	if !strings.Contains(messages[0].Content, repairAttemptID) {
		t.Errorf("system message should contain repair attempt ID")
	}

	// Verify that Repair system message equals Reviewer system message with repairAttemptID
	reviewerWithRepairID, _ := Reviewer(ReviewInput{
		Request:       original.Request,
		Candidate:     original.Candidate,
		CandidateHash: original.CandidateHash,
		AttemptID:     repairAttemptID,
		Delimiter:     original.Delimiter,
	})
	if messages[0].Content != reviewerWithRepairID[0].Content {
		t.Errorf("repair system message should equal reviewer system message with repair attempt ID substituted")
	}

	// Verify original attempt id does not appear in repair system message
	if strings.Contains(messages[0].Content, original.AttemptID) {
		t.Errorf("repair system message should not contain original attempt ID")
	}
}

func TestRepairErrors(t *testing.T) {
	_, err := Repair(ReviewInput{
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		AttemptID:     "id",
		Delimiter:     "",
	}, "output", "repair1")
	if err == nil {
		t.Errorf("expected error for empty delimiter")
	}

	_, err = Repair(ReviewInput{
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "",
		AttemptID:     "id",
		Delimiter:     "delim",
	}, "output", "repair1")
	if err == nil {
		t.Errorf("expected error for empty candidate hash")
	}

	// Test error handling for empty repairAttemptID
	_, err = Repair(ReviewInput{
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		AttemptID:     "id",
		Delimiter:     "delim",
	}, "output", "")
	if err == nil {
		t.Errorf("expected error for empty repair attempt id")
	}
}

func TestRepairWithEmptyOutput(t *testing.T) {
	messages, err := Repair(ReviewInput{
		Request:       "Please review this.",
		Candidate:     "This is the candidate.",
		CandidateHash: "abc123",
		AttemptID:     "attempt1",
		Delimiter:     "delim123",
	}, "", "repair1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages))
	}

	// Check that assistant message equals original output exactly (even if empty)
	if messages[2].Content != "" {
		t.Errorf("assistant content should equal original output exactly (empty string)")
	}
}

func TestDelivery(t *testing.T) {
	messages := Delivery(DeliveryInput{
		TerminalState:  "incomplete",
		TerminalReason: "timeout",
		CandidateHash:  "abc123",
		ReviewerOutputs: []ReviewerOutput{
			{
				Participant: "reviewer1",
				Output:      "VERDICT v1\ncandidate: abc123\nattempt: attempt1\nverdict: approve\nreason: looks good\nEND VERDICT\nSome commentary",
			},
			{
				Participant: "reviewer2",
				Output:      "VERDICT v1\ncandidate: abc123\nattempt: attempt1\nverdict: block\nreason: needs work\nEND VERDICT",
			},
		},
		OpenBlocks: []OpenBlock{
			{
				BlockID:  "block1",
				Severity: "high",
				Evidence: "line 5",
				Remedy:   "fix it",
			},
		},
	})

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if messages[0].Role != "system" {
		t.Errorf("first message should be system")
	}
	if messages[1].Role != "user" {
		t.Errorf("second message should be user")
	}

	// Check that user message contains all reviewer outputs
	if !strings.Contains(messages[1].Content, "reviewer1") {
		t.Errorf("user message should contain reviewer1 participant")
	}
	if !strings.Contains(messages[1].Content, "reviewer2") {
		t.Errorf("user message should contain reviewer2 participant")
	}

	// Check that reviewer outputs are verbatim including commentary
	if !strings.Contains(messages[1].Content, "Some commentary") {
		t.Errorf("user message should contain reviewer commentary outside envelope")
	}

	// Check that open blocks are included
	if !strings.Contains(messages[1].Content, "block1") {
		t.Errorf("user message should contain block ID")
	}
	if !strings.Contains(messages[1].Content, "high") {
		t.Errorf("user message should contain severity")
	}
	if !strings.Contains(messages[1].Content, "line 5") {
		t.Errorf("user message should contain evidence")
	}
	if !strings.Contains(messages[1].Content, "fix it") {
		t.Errorf("user message should contain remedy")
	}

	// Check terminal state and reason
	if !strings.Contains(messages[1].Content, "incomplete") {
		t.Errorf("user message should contain terminal state")
	}
	if !strings.Contains(messages[1].Content, "timeout") {
		t.Errorf("user message should contain terminal reason")
	}
}

func TestEnvelopeFormatConstant(t *testing.T) {
	if !strings.Contains(EnvelopeFormat, "candidate:") {
		t.Errorf("EnvelopeFormat should contain 'candidate:'")
	}
	if !strings.Contains(EnvelopeFormat, "attempt:") {
		t.Errorf("EnvelopeFormat should contain 'attempt:'")
	}
	if !strings.Contains(EnvelopeFormat, "verdict:") {
		t.Errorf("EnvelopeFormat should contain 'verdict:'")
	}
	if !strings.Contains(EnvelopeFormat, "severity:") {
		t.Errorf("EnvelopeFormat should contain 'severity:'")
	}
	if !strings.Contains(EnvelopeFormat, "block_id:") {
		t.Errorf("EnvelopeFormat should contain 'block_id:'")
	}
	if !strings.Contains(EnvelopeFormat, "evidence:") {
		t.Errorf("EnvelopeFormat should contain 'evidence:'")
	}
	if !strings.Contains(EnvelopeFormat, "remedy:") {
		t.Errorf("EnvelopeFormat should contain 'remedy:'")
	}
	if !strings.Contains(EnvelopeFormat, "reason:") {
		t.Errorf("EnvelopeFormat should contain 'reason:'")
	}
}

func TestNoDuplicateEnvelope(t *testing.T) {
	// A candidate containing VERDICT v1 should be passed through verbatim
	candidate := `This is a candidate with VERDICT v1
candidate: somehash
attempt: someid
verdict: approve
reason: test
END VERDICT`

	messages, err := Reviewer(ReviewInput{
		Request:       "test",
		Candidate:     candidate,
		CandidateHash: "newhash",
		AttemptID:     "newid",
		Delimiter:     "delim",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that the candidate is passed through verbatim, including the VERDICT v1 text
	if !strings.Contains(messages[1].Content, candidate) {
		t.Errorf("candidate with VERDICT v1 should be passed through verbatim")
	}
}

func TestNoParticipantIDInAuthor(t *testing.T) {
	messages := Author("test input")
	for _, msg := range messages {
		if strings.Contains(msg.Content, "participant") {
			t.Errorf("author messages should not contain participant id")
		}
	}
}

func TestNoParticipantIDInReviewer(t *testing.T) {
	messages, _ := Reviewer(ReviewInput{
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		AttemptID:     "id",
		Delimiter:     "delim",
	})
	for _, msg := range messages {
		if strings.Contains(msg.Content, "participant") {
			t.Errorf("reviewer messages should not contain participant id")
		}
	}
}

func TestNoParticipantIDInRepair(t *testing.T) {
	messages, _ := Repair(ReviewInput{
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		AttemptID:     "id",
		Delimiter:     "delim",
	}, "output", "repair1")
	for _, msg := range messages {
		if strings.Contains(msg.Content, "participant") {
			t.Errorf("repair messages should not contain participant id")
		}
	}
}

func TestDeliveryHasParticipantID(t *testing.T) {
	messages := Delivery(DeliveryInput{
		TerminalState:  "incomplete",
		TerminalReason: "timeout",
		CandidateHash:  "abc123",
		ReviewerOutputs: []ReviewerOutput{
			{
				Participant: "reviewer1",
				Output:      "output",
			},
		},
		OpenBlocks: []OpenBlock{},
	})

	found := false
	for _, msg := range messages {
		if strings.Contains(msg.Content, "reviewer1") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("delivery messages should contain participant id")
	}
}

func TestDeterminism(t *testing.T) {
	// Same input should produce same output
	msg1 := Author("same input")
	msg2 := Author("same input")

	if msg1[0].Content != msg2[0].Content {
		t.Errorf("Author should be deterministic")
	}
	if msg1[1].Content != msg2[1].Content {
		t.Errorf("Author should be deterministic")
	}

	rev1, _ := Reviewer(ReviewInput{
		Request:       "req",
		Candidate:     "cand",
		CandidateHash: "hash",
		AttemptID:     "id",
		Delimiter:     "delim",
	})
	rev2, _ := Reviewer(ReviewInput{
		Request:       "req",
		Candidate:     "cand",
		CandidateHash: "hash",
		AttemptID:     "id",
		Delimiter:     "delim",
	})

	if rev1[0].Content != rev2[0].Content || rev1[1].Content != rev2[1].Content {
		t.Errorf("Reviewer should be deterministic")
	}
}

func TestEnvelopeFormatVerbatim(t *testing.T) {
	messages, _ := Reviewer(ReviewInput{
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		AttemptID:     "id",
		Delimiter:     "delim",
	})

	// The envelope format should appear verbatim in the system message
	systemContent := messages[0].Content

	// Check key parts appear verbatim
	if !strings.Contains(systemContent, "VERDICT v1") {
		t.Errorf("system message should contain 'VERDICT v1'")
	}
	if !strings.Contains(systemContent, "END VERDICT") {
		t.Errorf("system message should contain 'END VERDICT'")
	}
}

func TestEnvelopeFormatExact(t *testing.T) {
	// EnvelopeFormat must match the exact design specification from v3 delta section 6
	expectedFormat := `VERDICT v1
candidate: <hash supplied by the engine>
attempt: <attempt id supplied by the engine>
verdict: approve | block
severity: low | medium | high        (block only)
block_id: <reviewer-chosen short id>  (block only)
evidence: <what in the candidate the block rests on, one or more lines>  (block only)
remedy: <what change would withdraw the block, one or more lines>  (block only)
reason: <free text, one or more lines>
END VERDICT`

	if EnvelopeFormat != expectedFormat {
		t.Errorf("EnvelopeFormat does not match the design specification.\nExpected:\n%s\n\nGot:\n%s", expectedFormat, EnvelopeFormat)
	}
}

func TestDelimiterIsHex(t *testing.T) {
	r := bytes.NewReader([]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F})
	delim, err := NewDelimiter(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be valid hex
	_, err = hex.DecodeString(delim)
	if err != nil {
		t.Errorf("delimiter should be valid hex: %v", err)
	}

	// Should be lowercase
	if delim != strings.ToLower(delim) {
		t.Errorf("delimiter should be lowercase hex")
	}
}

func TestEnvelopeFormatV2(t *testing.T) {
	if !strings.Contains(EnvelopeFormatV2, "VERDICT v2") {
		t.Errorf("EnvelopeFormatV2 should contain 'VERDICT v2'")
	}
	if !strings.Contains(EnvelopeFormatV2, "round:") {
		t.Errorf("EnvelopeFormatV2 should contain 'round:'")
	}
	if !strings.Contains(EnvelopeFormatV2, "block-state:") {
		t.Errorf("EnvelopeFormatV2 should contain 'block-state:'")
	}
	if !strings.Contains(EnvelopeFormatV2, "approve-with-changes") {
		t.Errorf("EnvelopeFormatV2 should contain 'approve-with-changes'")
	}
}

func TestAuthorRevisionSuccess(t *testing.T) {
	messages, err := AuthorRevision(AuthorRevisionInput{
		History: []HistoryTurn{
			{
				TurnID: "turn1",
				State:  "approved",
				Input:  "Create a document.",
				Answer: "Here is the document.",
				Label:  "reviewed",
			},
		},
		Input:             "Now revise it.",
		PreviousCandidate: "Here is my first attempt.",
		ReviewerFeedback: []RevisionFeedback{
			{
				ReviewerLabel: "reviewer 1",
				Output:        "VERDICT v1\ncandidate: abc123\nattempt: attempt1\nverdict: block\nreason: needs work\nEND VERDICT",
			},
		},
		Delimiter: "delim123",
		Round:     1,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Message sequence: system, history (user, assistant), original input (user),
	// previous candidate (assistant), feedback (user) = 6 messages
	if len(messages) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(messages))
	}

	if messages[0].Role != "system" {
		t.Errorf("first message should be system")
	}

	// Check message roles: user, assistant, user, assistant, user
	expectedRoles := []string{"system", "user", "assistant", "user", "assistant", "user"}
	for i, expectedRole := range expectedRoles {
		if messages[i].Role != expectedRole {
			t.Errorf("message %d: expected role %s, got %s", i, expectedRole, messages[i].Role)
		}
	}

	// Check that system message contains appropriate guidance
	if !strings.Contains(messages[0].Content, "revised") {
		t.Errorf("system message should mention revision")
	}

	// Check that history user message contains turn id
	if !strings.Contains(messages[1].Content, "turn1") {
		t.Errorf("history user message should contain turn id")
	}

	// Check that history assistant message contains answer
	if !strings.Contains(messages[2].Content, "Here is the document") {
		t.Errorf("history assistant message should contain answer")
	}

	// Check that original input message contains the input
	if !strings.Contains(messages[3].Content, "Now revise it") {
		t.Errorf("original input message should contain the input")
	}

	// Check that previous candidate message contains the candidate
	if !strings.Contains(messages[4].Content, "first attempt") {
		t.Errorf("previous candidate message should contain the candidate")
	}

	// Check that feedback message contains reviewer feedback
	if !strings.Contains(messages[5].Content, "reviewer 1") {
		t.Errorf("feedback message should contain reviewer label")
	}
	if !strings.Contains(messages[5].Content, "VERDICT v1") {
		t.Errorf("feedback message should contain verdict")
	}
}

func TestAuthorRevisionValidationErrors(t *testing.T) {
	_, err := AuthorRevision(AuthorRevisionInput{
		History:           []HistoryTurn{},
		Input:             "test",
		PreviousCandidate: "test",
		ReviewerFeedback: []RevisionFeedback{
			{ReviewerLabel: "reviewer 1", Output: "feedback"},
		},
		Delimiter: "",
		Round:     1,
	})
	if err == nil {
		t.Errorf("expected error for empty delimiter")
	}

	_, err = AuthorRevision(AuthorRevisionInput{
		History:           []HistoryTurn{},
		Input:             "test",
		PreviousCandidate: "",
		ReviewerFeedback: []RevisionFeedback{
			{ReviewerLabel: "reviewer 1", Output: "feedback"},
		},
		Delimiter: "delim",
		Round:     1,
	})
	if err == nil {
		t.Errorf("expected error for empty previous candidate")
	}

	_, err = AuthorRevision(AuthorRevisionInput{
		History:           []HistoryTurn{},
		Input:             "test",
		PreviousCandidate: "test",
		ReviewerFeedback:  []RevisionFeedback{},
		Delimiter:         "delim",
		Round:             1,
	})
	if err == nil {
		t.Errorf("expected error for empty reviewer feedback")
	}
}

func TestAuthorRevisionMultipleFeedback(t *testing.T) {
	messages, err := AuthorRevision(AuthorRevisionInput{
		History:           []HistoryTurn{},
		Input:             "test",
		PreviousCandidate: "candidate",
		ReviewerFeedback: []RevisionFeedback{
			{
				ReviewerLabel: "reviewer 1",
				Output:        "output1",
			},
			{
				ReviewerLabel: "reviewer 2",
				Output:        "output2",
			},
		},
		Delimiter: "delim",
		Round:     1,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Message sequence: system, input (user), previous candidate (assistant), feedback (user) = 4 messages
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages (no history), got %d", len(messages))
	}

	// Feedback is in the last message
	feedbackMessage := messages[3].Content

	if !strings.Contains(feedbackMessage, "reviewer 1") {
		t.Errorf("feedback message should contain reviewer 1")
	}
	if !strings.Contains(feedbackMessage, "reviewer 2") {
		t.Errorf("feedback message should contain reviewer 2")
	}
	if !strings.Contains(feedbackMessage, "output1") {
		t.Errorf("feedback message should contain output1")
	}
	if !strings.Contains(feedbackMessage, "output2") {
		t.Errorf("feedback message should contain output2")
	}
}

func TestReviewerRevisionSuccess(t *testing.T) {
	messages, err := ReviewerRevision(ReviewerRevisionInput{
		History: []HistoryTurn{
			{
				TurnID: "turn1",
				State:  "approved",
				Input:  "Create something.",
				Answer: "Here it is.",
				Label:  "reviewed",
			},
		},
		Request:       "Please create something.",
		Candidate:     "Here is the revised version.",
		CandidateHash: "abc123",
		OpenBlocks: []OpenBlockForRevision{
			{
				BlockID:  "block1",
				Severity: "high",
				Evidence: "line 5",
				Remedy:   "fix it",
			},
		},
		AttemptID: "attempt1",
		Delimiter: "delim123",
		Round:     1,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if messages[0].Role != "system" {
		t.Errorf("first message should be system")
	}
	if messages[1].Role != "user" {
		t.Errorf("second message should be user")
	}

	// Check that system message contains VERDICT v2
	if !strings.Contains(messages[0].Content, "VERDICT v2") {
		t.Errorf("system message should contain VERDICT v2")
	}

	// Check that system message contains round number
	if !strings.Contains(messages[0].Content, "round") {
		t.Errorf("system message should mention round")
	}

	// Check that system message contains block-state requirement
	if !strings.Contains(messages[0].Content, "block-state") {
		t.Errorf("system message should mention block-state requirement")
	}

	// Check that user message contains request
	if !strings.Contains(messages[1].Content, "Create something") {
		t.Errorf("user message should contain request")
	}

	// Check that user message contains history
	if !strings.Contains(messages[1].Content, "turn1") {
		t.Errorf("user message should contain history turn id")
	}

	// Check that user message contains candidate
	if !strings.Contains(messages[1].Content, "revised version") {
		t.Errorf("user message should contain candidate")
	}

	// Check that user message contains open blocks
	if !strings.Contains(messages[1].Content, "block1") {
		t.Errorf("user message should contain open block id")
	}
	if !strings.Contains(messages[1].Content, "high") {
		t.Errorf("user message should contain severity")
	}

	// Check that delimiter fences are present
	if !strings.Contains(messages[1].Content, "BEGIN REQUEST") {
		t.Errorf("user message should contain BEGIN REQUEST delimiter")
	}
	if !strings.Contains(messages[1].Content, "BEGIN HISTORY") {
		t.Errorf("user message should contain BEGIN HISTORY delimiter")
	}
	if !strings.Contains(messages[1].Content, "BEGIN CANDIDATE") {
		t.Errorf("user message should contain BEGIN CANDIDATE delimiter")
	}
}

func TestReviewerRevisionValidationErrors(t *testing.T) {
	_, err := ReviewerRevision(ReviewerRevisionInput{
		History:       []HistoryTurn{},
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		OpenBlocks:    []OpenBlockForRevision{},
		AttemptID:     "id",
		Delimiter:     "",
		Round:         1,
	})
	if err == nil {
		t.Errorf("expected error for empty delimiter")
	}

	_, err = ReviewerRevision(ReviewerRevisionInput{
		History:       []HistoryTurn{},
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "",
		OpenBlocks:    []OpenBlockForRevision{},
		AttemptID:     "id",
		Delimiter:     "delim",
		Round:         1,
	})
	if err == nil {
		t.Errorf("expected error for empty candidate hash")
	}

	_, err = ReviewerRevision(ReviewerRevisionInput{
		History:       []HistoryTurn{},
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		OpenBlocks:    []OpenBlockForRevision{},
		AttemptID:     "",
		Delimiter:     "delim",
		Round:         1,
	})
	if err == nil {
		t.Errorf("expected error for empty attempt id")
	}
}

func TestReviewerRevisionDelimiterCollision(t *testing.T) {
	_, err := ReviewerRevision(ReviewerRevisionInput{
		History:       []HistoryTurn{},
		Request:       "Contains uniquedelim here",
		Candidate:     "test",
		CandidateHash: "hash",
		OpenBlocks:    []OpenBlockForRevision{},
		AttemptID:     "id",
		Delimiter:     "uniquedelim",
		Round:         1,
	})
	if !errors.Is(err, ErrDelimiterCollision) {
		t.Errorf("expected ErrDelimiterCollision in request, got %v", err)
	}

	_, err = ReviewerRevision(ReviewerRevisionInput{
		History:       []HistoryTurn{},
		Request:       "test",
		Candidate:     "Contains uniquedelim here",
		CandidateHash: "hash",
		OpenBlocks:    []OpenBlockForRevision{},
		AttemptID:     "id",
		Delimiter:     "uniquedelim",
		Round:         1,
	})
	if !errors.Is(err, ErrDelimiterCollision) {
		t.Errorf("expected ErrDelimiterCollision in candidate, got %v", err)
	}
}

func TestReviewerRevisionMultipleBlocks(t *testing.T) {
	messages, err := ReviewerRevision(ReviewerRevisionInput{
		History:       []HistoryTurn{},
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		OpenBlocks: []OpenBlockForRevision{
			{BlockID: "block1", Severity: "low", Evidence: "e1", Remedy: "r1"},
			{BlockID: "block2", Severity: "high", Evidence: "e2", Remedy: "r2"},
		},
		AttemptID: "id",
		Delimiter: "delim",
		Round:     2,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(messages[1].Content, "block1") {
		t.Errorf("user message should contain block1")
	}
	if !strings.Contains(messages[1].Content, "block2") {
		t.Errorf("user message should contain block2")
	}
	if !strings.Contains(messages[1].Content, "withdrawn or maintained") {
		t.Errorf("user message should indicate need to mark blocks as withdrawn or maintained")
	}
}

func TestReviewerRevisionNoBlocks(t *testing.T) {
	messages, err := ReviewerRevision(ReviewerRevisionInput{
		History:       []HistoryTurn{},
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		OpenBlocks:    []OpenBlockForRevision{},
		AttemptID:     "id",
		Delimiter:     "delim",
		Round:         1,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	// Should still work, just without open blocks section
	if !strings.Contains(messages[1].Content, "BEGIN REQUEST") {
		t.Errorf("user message should still have request")
	}
}

func TestAuthorRevisionNoParticipantID(t *testing.T) {
	messages, _ := AuthorRevision(AuthorRevisionInput{
		History:           []HistoryTurn{},
		Input:             "test",
		PreviousCandidate: "test",
		ReviewerFeedback: []RevisionFeedback{
			{ReviewerLabel: "reviewer 1", Output: "feedback"},
		},
		Delimiter: "delim",
		Round:     1,
	})

	for _, msg := range messages {
		if strings.Contains(msg.Content, "participant") && !strings.Contains(msg.Content, "Participant") {
			// Allow "Participant" in capitalized form if it's part of reviewer label structure
			t.Errorf("author revision messages should not contain participant id")
		}
	}
}

func TestReviewerRevisionNoParticipantIDInBlocks(t *testing.T) {
	messages, _ := ReviewerRevision(ReviewerRevisionInput{
		History:       []HistoryTurn{},
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		OpenBlocks: []OpenBlockForRevision{
			{BlockID: "block1", Severity: "high", Evidence: "e1", Remedy: "r1"},
		},
		AttemptID: "id",
		Delimiter: "delim",
		Round:     1,
	})

	for _, msg := range messages {
		if strings.Contains(msg.Content, "reviewer_id") || strings.Contains(msg.Content, "participant_id") {
			t.Errorf("reviewer revision messages should not contain participant/reviewer ids")
		}
	}
}

func TestReviewerRevisionHistoryPreserved(t *testing.T) {
	historyTurn := HistoryTurn{
		TurnID: "turn123",
		State:  "approved",
		Input:  "Original request with specific words",
		Answer: "Answer with unique content",
		Label:  "reviewed",
	}

	messages, _ := ReviewerRevision(ReviewerRevisionInput{
		History:       []HistoryTurn{historyTurn},
		Request:       "test",
		Candidate:     "test",
		CandidateHash: "hash",
		OpenBlocks:    []OpenBlockForRevision{},
		AttemptID:     "id",
		Delimiter:     "delim",
		Round:         1,
	})

	userContent := messages[1].Content
	if !strings.Contains(userContent, "turn123") {
		t.Errorf("history turn id should be preserved")
	}
	if !strings.Contains(userContent, "Original request with specific words") {
		t.Errorf("history input should be preserved verbatim")
	}
	if !strings.Contains(userContent, "Answer with unique content") {
		t.Errorf("history answer should be preserved verbatim")
	}
	if !strings.Contains(userContent, "reviewed") {
		t.Errorf("history label should be preserved")
	}
}

func TestAuthorRevisionHistoryPreserved(t *testing.T) {
	historyTurn := HistoryTurn{
		TurnID: "turn456",
		State:  "incomplete",
		Input:  "First attempt with details",
		Answer: "no approved answer (incomplete)",
		Label:  "unreviewed (solo)",
	}

	messages, _ := AuthorRevision(AuthorRevisionInput{
		History:           []HistoryTurn{historyTurn},
		Input:             "Revision request",
		PreviousCandidate: "My answer",
		ReviewerFeedback: []RevisionFeedback{
			{ReviewerLabel: "reviewer 1", Output: "feedback"},
		},
		Delimiter: "delim",
		Round:     1,
	})

	// Message sequence: system, history user, history assistant, input, candidate, feedback
	// History turn ID and label should be in messages[1] (history user message)
	historyUserContent := messages[1].Content
	if !strings.Contains(historyUserContent, "turn456") {
		t.Errorf("history turn id should be preserved in history user message")
	}
	if !strings.Contains(historyUserContent, "First attempt with details") {
		t.Errorf("history input should be preserved verbatim in history user message")
	}
	if !strings.Contains(historyUserContent, "unreviewed (solo)") {
		t.Errorf("history label should be preserved in history user message")
	}

	// History assistant message should contain the missing answer line
	historyAssistantContent := messages[2].Content
	if !strings.Contains(historyAssistantContent, "no approved answer (incomplete)") {
		t.Errorf("missing answer line should be preserved in history assistant message")
	}
}
