package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

func TestDocumentPreamble(t *testing.T) {
	doc := New()
	docBytes := doc.Bytes()

	if !strings.HasPrefix(string(docBytes), "syndic-history/v1\n") {
		t.Errorf("document preamble incorrect: %q", docBytes)
	}
}

func TestDocumentAddTurn(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("What is 2+2?"),
		Answer:       []byte("The answer is 4."),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docBytes := doc.Bytes()
	docStr := string(docBytes)

	// Check that all fields are present
	if !strings.Contains(docStr, "turn 6\nturn-1\n") {
		t.Errorf("turn field not found or malformed: %q", docStr)
	}
	if !strings.Contains(docStr, "state 8\napproved\n") {
		t.Errorf("state field not found or malformed: %q", docStr)
	}
	if !strings.Contains(docStr, "input 12\nWhat is 2+2?\n") {
		t.Errorf("input field not found or malformed: %q", docStr)
	}
	if !strings.Contains(docStr, "answer 16\nThe answer is 4.\n") {
		t.Errorf("answer field not found or malformed: %q", docStr)
	}
	if !strings.Contains(docStr, "label 8\nreviewed\n") {
		t.Errorf("label field not found or malformed: %q", docStr)
	}
}

func TestDocumentNewlineRoundtrip(t *testing.T) {
	input := "Line 1\nLine 2\nLine 3"
	answer := "Answer with\nnewlines\nin it"

	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte(input),
		Answer:       []byte(answer),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docBytes := doc.Bytes()

	// Verify newlines are preserved exactly
	if !strings.Contains(string(docBytes), "input "+strconv.Itoa(len(input))+"\n"+input+"\n") {
		t.Errorf("input newlines not preserved correctly")
	}
	if !strings.Contains(string(docBytes), "answer "+strconv.Itoa(len(answer))+"\n"+answer+"\n") {
		t.Errorf("answer newlines not preserved correctly")
	}
}

func TestDocumentMultibyteContent(t *testing.T) {
	input := "こんにちは"   // "hello" in Japanese
	answer := "你好，世界！" // "hello, world!" in Chinese
	inputBytes := []byte(input)
	answerBytes := []byte(answer)

	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        inputBytes,
		Answer:       answerBytes,
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docBytes := doc.Bytes()
	docStr := string(docBytes)

	// Verify byte lengths are correct (multibyte characters count as multiple bytes)
	if !strings.Contains(docStr, "input "+strconv.Itoa(len(inputBytes))+"\n"+input+"\n") {
		t.Errorf("multibyte input not preserved correctly: got %q", docStr)
	}
	if !strings.Contains(docStr, "answer "+strconv.Itoa(len(answerBytes))+"\n"+answer+"\n") {
		t.Errorf("multibyte answer not preserved correctly: got %q", docStr)
	}
}

func TestDocumentIncompleteState(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "incomplete",
		Input:        []byte("What is 2+2?"),
		Answer:       []byte(""),
		Label:        "unreviewed (solo)",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docStr := string(doc.Bytes())

	// Answer should be replaced with missing-answer marker
	if !strings.Contains(docStr, "no approved answer (incomplete)") {
		t.Errorf("incomplete turn missing-answer marker not found: %q", docStr)
	}
	if strings.Contains(docStr, "answer 0\n\n") {
		t.Errorf("incomplete turn should not have empty answer field")
	}
}

func TestDocumentCancelledState(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "cancelled",
		Input:        []byte("Original input"),
		Answer:       []byte(""),
		Label:        "unreviewed (solo)",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docStr := string(doc.Bytes())

	// Answer should be replaced with missing-answer marker
	if !strings.Contains(docStr, "no approved answer (cancelled)") {
		t.Errorf("cancelled turn missing-answer marker not found: %q", docStr)
	}
}

func TestDocumentHashConsistency(t *testing.T) {
	doc1 := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Question"),
		Answer:       []byte("Answer"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}
	if err := doc1.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	doc2 := New()
	if err := doc2.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	hash1 := doc1.Hash()
	hash2 := doc2.Hash()

	if hash1 != hash2 {
		t.Errorf("same document produced different hashes: %s vs %s", hash1, hash2)
	}
}

func TestDocumentHashChangesWithContent(t *testing.T) {
	doc1 := New()
	if err := doc1.AddTurn(Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Question"),
		Answer:       []byte("Answer"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	doc2 := New()
	if err := doc2.AddTurn(Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Different question"),
		Answer:       []byte("Answer"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	if doc1.Hash() == doc2.Hash() {
		t.Errorf("different documents produced same hash")
	}
}

func TestDocumentHashChangesWithAnnotations(t *testing.T) {
	doc1 := New()
	if err := doc1.AddTurn(Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Question"),
		Answer:       []byte("Answer"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	doc2 := New()
	if err := doc2.AddTurn(Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Question"),
		Answer:       []byte("Answer"),
		Label:        "unreviewed (solo)",
		ChangesState: "",
		Changes:      nil,
	}); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	if doc1.Hash() == doc2.Hash() {
		t.Errorf("different labels should produce different hashes")
	}
}

func TestDocumentHashChangesWithChanges(t *testing.T) {
	doc1 := New()
	if err := doc1.AddTurn(Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Question"),
		Answer:       []byte("Answer"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	doc2 := New()
	if err := doc2.AddTurn(Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Question"),
		Answer:       []byte("Answer"),
		Label:        "reviewed",
		ChangesState: "applied",
		Changes:      []string{"file.txt applied"},
	}); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	if doc1.Hash() == doc2.Hash() {
		t.Errorf("different changes should produce different hashes")
	}
}

func TestDocumentMultipleTurns(t *testing.T) {
	doc := New()

	turns := []Turn{
		{
			ID:           "turn-1",
			State:        "approved",
			Input:        []byte("First question"),
			Answer:       []byte("First answer"),
			Label:        "reviewed",
			ChangesState: "",
			Changes:      nil,
		},
		{
			ID:           "turn-2",
			State:        "approved",
			Input:        []byte("Second question"),
			Answer:       []byte("Second answer"),
			Label:        "reviewed",
			ChangesState: "",
			Changes:      nil,
		},
	}

	for _, turn := range turns {
		if err := doc.AddTurn(turn); err != nil {
			t.Fatalf("AddTurn failed: %v", err)
		}
	}

	docStr := string(doc.Bytes())

	// Verify both turns are present
	if !strings.Contains(docStr, "turn-1") {
		t.Errorf("first turn not found in document")
	}
	if !strings.Contains(docStr, "turn-2") {
		t.Errorf("second turn not found in document")
	}
	if !strings.Contains(docStr, "First question") {
		t.Errorf("first question not found")
	}
	if !strings.Contains(docStr, "Second question") {
		t.Errorf("second question not found")
	}
}

func TestDocumentRenderForAuthor(t *testing.T) {
	doc := New()
	turns := []Turn{
		{
			ID:           "turn-1",
			State:        "approved",
			Input:        []byte("What is 2+2?"),
			Answer:       []byte("4"),
			Label:        "reviewed",
			ChangesState: "",
			Changes:      nil,
		},
		{
			ID:           "turn-2",
			State:        "approved",
			Input:        []byte("What is 3+3?"),
			Answer:       []byte("6"),
			Label:        "reviewed",
			ChangesState: "",
			Changes:      nil,
		},
	}

	for _, turn := range turns {
		if err := doc.AddTurn(turn); err != nil {
			t.Fatalf("AddTurn failed: %v", err)
		}
	}

	messages, err := doc.RenderForAuthor()
	if err != nil {
		t.Fatalf("RenderForAuthor failed: %v", err)
	}

	// Should have 4 messages: 2 user (state+input) + 2 assistant (label+answer)
	if len(messages) != 4 {
		t.Errorf("expected 4 messages, got %d: %v", len(messages), messages)
	}

	// User message should contain state and input
	if messages[0].Role != "user" {
		t.Errorf("message 0 role: expected %q, got %q", "user", messages[0].Role)
	}
	if !strings.Contains(messages[0].Content, "state: approved") {
		t.Errorf("message 0 should contain state: %q", messages[0].Content)
	}
	if !strings.Contains(messages[0].Content, "input: What is 2+2?") {
		t.Errorf("message 0 should contain input: %q", messages[0].Content)
	}

	// Assistant message should contain label and answer
	if messages[1].Role != "assistant" {
		t.Errorf("message 1 role: expected %q, got %q", "assistant", messages[1].Role)
	}
	if !strings.Contains(messages[1].Content, "label: reviewed") {
		t.Errorf("message 1 should contain label: %q", messages[1].Content)
	}
	if !strings.Contains(messages[1].Content, "answer: 4") {
		t.Errorf("message 1 should contain answer: %q", messages[1].Content)
	}

	// Same checks for turn 2
	if !strings.Contains(messages[2].Content, "state: approved") {
		t.Errorf("message 2 should contain state: %q", messages[2].Content)
	}
	if !strings.Contains(messages[2].Content, "input: What is 3+3?") {
		t.Errorf("message 2 should contain input: %q", messages[2].Content)
	}
	if !strings.Contains(messages[3].Content, "label: reviewed") {
		t.Errorf("message 3 should contain label: %q", messages[3].Content)
	}
	if !strings.Contains(messages[3].Content, "answer: 6") {
		t.Errorf("message 3 should contain answer: %q", messages[3].Content)
	}
}

func TestDocumentRenderForAuthorWithMissingAnswer(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "incomplete",
		Input:        []byte("Original question"),
		Answer:       []byte(""),
		Label:        "unreviewed (solo)",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	messages, err := doc.RenderForAuthor()
	if err != nil {
		t.Fatalf("RenderForAuthor failed: %v", err)
	}

	if len(messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(messages))
	}

	if messages[0].Role != "user" {
		t.Errorf("first message role should be 'user', got %q", messages[0].Role)
	}

	// First message should contain state and input
	if !strings.Contains(messages[0].Content, "state: incomplete") {
		t.Errorf("first message should contain state: %q", messages[0].Content)
	}
	if !strings.Contains(messages[0].Content, "input: Original question") {
		t.Errorf("first message should contain input: %q", messages[0].Content)
	}

	if messages[1].Role != "assistant" {
		t.Errorf("second message role should be 'assistant', got %q", messages[1].Role)
	}

	// Second message should contain ONLY the missing-answer marker (no label, no other fields)
	expectedContent := "no approved answer (incomplete)"
	if messages[1].Content != expectedContent {
		t.Errorf("missing-answer message should contain only missing-answer marker. expected %q, got %q", expectedContent, messages[1].Content)
	}
}

func TestDocumentByteExactRoundtrip(t *testing.T) {
	input := "user\ninput\nwith\nnewlines"
	answer := "model\nanswer\nwith\nnewlines"

	doc := New()
	turn := Turn{
		ID:           "turn-123",
		State:        "approved",
		Input:        []byte(input),
		Answer:       []byte(answer),
		Label:        "human override",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	// Parse it back
	messages, err := doc.RenderForAuthor()
	if err != nil {
		t.Fatalf("RenderForAuthor failed: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages after roundtrip, got %d", len(messages))
	}

	// User message should contain input (and state)
	if !strings.Contains(messages[0].Content, input) {
		t.Errorf("input not preserved in user message: expected %q to be in %q", input, messages[0].Content)
	}

	// Assistant message should contain answer (and label)
	if !strings.Contains(messages[1].Content, answer) {
		t.Errorf("answer not preserved in assistant message: expected %q to be in %q", answer, messages[1].Content)
	}

	// Verify the bytes can be parsed back exactly
	if !strings.Contains(messages[0].Content, "state: approved") {
		t.Errorf("state should be in user message: %q", messages[0].Content)
	}
	if !strings.Contains(messages[1].Content, "label: human override") {
		t.Errorf("label should be in assistant message: %q", messages[1].Content)
	}
}

func TestDocumentHashVerification(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("test"),
		Answer:       []byte("result"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	hash := doc.Hash()
	docBytes := doc.Bytes()

	// Verify hash matches SHA-256 of document bytes
	h := sha256.Sum256(docBytes)
	expectedHash := hex.EncodeToString(h[:])

	if hash != expectedHash {
		t.Errorf("hash mismatch: got %s, expected %s", hash, expectedHash)
	}
}

func TestDocumentLabelPreservation(t *testing.T) {
	labels := []string{
		"reviewed",
		"unreviewed (solo)",
		"human override",
	}

	for _, label := range labels {
		doc := New()
		turn := Turn{
			ID:           "turn-1",
			State:        "approved",
			Input:        []byte("question"),
			Answer:       []byte("answer"),
			Label:        label,
			ChangesState: "",
			Changes:      nil,
		}

		if err := doc.AddTurn(turn); err != nil {
			t.Fatalf("AddTurn failed: %v", err)
		}

		docStr := string(doc.Bytes())
		if !strings.Contains(docStr, "label "+strconv.Itoa(len(label))+"\n"+label+"\n") {
			t.Errorf("label %q not preserved correctly", label)
		}
	}
}

func TestDocumentRenderForReviewer(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Question"),
		Answer:       []byte("Answer"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	rendered := doc.RenderForReviewer("```data")

	// Should be wrapped in fence delimiters
	if !strings.HasPrefix(rendered, "```data\n") {
		t.Errorf("reviewer rendering should start with fence delimiter")
	}
	if !strings.HasSuffix(rendered, "\n```data\n") {
		t.Errorf("reviewer rendering should end with fence delimiter")
	}

	// Should contain the canonical document
	if !strings.Contains(rendered, "syndic-history/v1") {
		t.Errorf("reviewer rendering should contain document preamble")
	}
	if !strings.Contains(rendered, "turn-1") {
		t.Errorf("reviewer rendering should contain turn data")
	}
}

func TestDocumentWithChanges(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Code change"),
		Answer:       []byte("Modified"),
		Label:        "reviewed",
		ChangesState: "applied",
		Changes: []string{
			"src/main.go applied",
			"src/util.go not applied",
			"tests/main_test.go conflict",
		},
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docStr := string(doc.Bytes())

	// Verify changes fields are present (count them)
	if !strings.Contains(docStr, "changes 7\napplied\n") {
		t.Errorf("changes state not found: %q", docStr)
	}

	// Verify all three change entries exist (without exact byte count)
	if !strings.Contains(docStr, "src/main.go applied") {
		t.Errorf("first change not found")
	}
	if !strings.Contains(docStr, "src/util.go not applied") {
		t.Errorf("second change not found")
	}
	if !strings.Contains(docStr, "tests/main_test.go conflict") {
		t.Errorf("third change not found")
	}
}

func TestDocumentStrictParsing(t *testing.T) {
	// Create a document with valid content
	testDoc := &Document{
		buf: bytes.NewBufferString(""),
	}

	// Add a valid turn so we have something to work with
	validTurn := Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Q"),
		Answer:       []byte("A"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}
	if err := testDoc.AddTurn(validTurn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	// Valid document should parse without error
	_, err := testDoc.RenderForAuthor()
	if err != nil {
		t.Errorf("valid document should parse without error: %v", err)
	}
}

func TestDocumentValidateState(t *testing.T) {
	validStates := []string{"approved", "incomplete", "cancelled", "solo_complete", "overridden"}
	for _, state := range validStates {
		if err := ValidateState(state); err != nil {
			t.Errorf("valid state %q rejected: %v", state, err)
		}
	}

	invalidStates := []string{"pending", "unknown", "done", ""}
	for _, state := range invalidStates {
		if err := ValidateState(state); err == nil {
			t.Errorf("invalid state %q should be rejected", state)
		}
	}
}

func TestDocumentValidateLabel(t *testing.T) {
	validLabels := []string{"reviewed", "unreviewed (solo)", "human override"}
	for _, label := range validLabels {
		if err := ValidateLabel(label); err != nil {
			t.Errorf("valid label %q rejected: %v", label, err)
		}
	}

	invalidLabels := []string{"unreviewed", "pending", "auto", ""}
	for _, label := range invalidLabels {
		if err := ValidateLabel(label); err == nil {
			t.Errorf("invalid label %q should be rejected", label)
		}
	}
}

func TestDocumentGoldenFixture_SimpleApproved(t *testing.T) {
	// Golden fixture for simple approved turn
	doc := New()
	turn := Turn{
		ID:           "golden-1",
		State:        "approved",
		Input:        []byte("What is the answer?"),
		Answer:       []byte("42"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docBytes := doc.Bytes()
	hash := doc.Hash()

	// Verify the document structure matches the golden format
	if !strings.HasPrefix(string(docBytes), "syndic-history/v1\n") {
		t.Errorf("missing preamble")
	}

	// Hash should be deterministic and 64 hex characters
	if len(hash) != 64 {
		t.Errorf("hash should be 64 hex chars, got %d", len(hash))
	}
}

func TestDocumentGoldenFixture_MultiTurnWithChanges(t *testing.T) {
	// Golden fixture for multi-turn with changes
	doc := New()

	turns := []Turn{
		{
			ID:           "golden-2a",
			State:        "approved",
			Input:        []byte("Create a file"),
			Answer:       []byte("Done"),
			Label:        "reviewed",
			ChangesState: "applied",
			Changes: []string{
				"new_file.txt applied",
			},
		},
		{
			ID:           "golden-2b",
			State:        "approved",
			Input:        []byte("Edit it"),
			Answer:       []byte("Updated"),
			Label:        "reviewed",
			ChangesState: "applied",
			Changes: []string{
				"new_file.txt applied",
				"config.json not applied",
			},
		},
	}

	for _, turn := range turns {
		if err := doc.AddTurn(turn); err != nil {
			t.Fatalf("AddTurn failed: %v", err)
		}
	}

	docBytes := doc.Bytes()
	docStr := string(docBytes)

	// Verify structure
	if strings.Count(docStr, "turn ") != 2 {
		t.Errorf("expected 2 turns, got %d", strings.Count(docStr, "turn "))
	}
	if strings.Count(docStr, "changes ") != 2 {
		t.Errorf("expected 2 changes fields, got %d", strings.Count(docStr, "changes "))
	}
	if strings.Count(docStr, "change ") != 3 {
		t.Errorf("expected 3 change fields, got %d", strings.Count(docStr, "change "))
	}
}

func TestDocumentGoldenFixture_IncompleteWithMissingAnswer(t *testing.T) {
	// Golden fixture for incomplete turn with missing answer
	doc := New()
	turn := Turn{
		ID:           "golden-3",
		State:        "incomplete",
		Input:        []byte("Never finished"),
		Answer:       []byte(""),
		Label:        "unreviewed (solo)",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docBytes := doc.Bytes()
	docStr := string(docBytes)

	// Verify missing-answer marker
	// "no approved answer (incomplete)" is 31 bytes
	if !strings.Contains(docStr, "answer 31\nno approved answer (incomplete)\n") {
		t.Errorf("missing answer marker not in golden fixture: %q", docStr)
	}
}

func TestDocumentGoldenFixture_MultibyteContent(t *testing.T) {
	// Golden fixture for multibyte content
	doc := New()
	inputStr := "こんにちは世界"
	answerStr := "你好，世界！"
	turn := Turn{
		ID:           "golden-4",
		State:        "approved",
		Input:        []byte(inputStr),
		Answer:       []byte(answerStr),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docBytes := doc.Bytes()

	// Verify multibyte content is preserved exactly
	messages, err := doc.RenderForAuthor()
	if err != nil {
		t.Fatalf("RenderForAuthor failed: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	// Lossless rendering includes state and input
	if !strings.Contains(messages[0].Content, inputStr) {
		t.Errorf("multibyte input not preserved: got %q", messages[0].Content)
	}

	// Lossless rendering includes label and answer
	if !strings.Contains(messages[1].Content, answerStr) {
		t.Errorf("multibyte answer not preserved: got %q", messages[1].Content)
	}

	// Verify byte lengths are correct
	inputBytes := []byte(inputStr)
	answerBytes := []byte(answerStr)
	expectedInputLen := strconv.Itoa(len(inputBytes))
	expectedAnswerLen := strconv.Itoa(len(answerBytes))

	if !strings.Contains(string(docBytes), "input "+expectedInputLen+"\n") {
		t.Errorf("input byte length should be %d, not found in: %q", len(inputBytes), string(docBytes))
	}
	if !strings.Contains(string(docBytes), "answer "+expectedAnswerLen+"\n") {
		t.Errorf("answer byte length should be %d, not found in: %q", len(answerBytes), string(docBytes))
	}
}

// Golden fixture with pinned canonical bytes and expected hash
func TestDocumentGoldenFixture_PinnedBytes_SimpleApproved(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-abc",
		State:        "approved",
		Input:        []byte("Q"),
		Answer:       []byte("A"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docBytes := doc.Bytes()
	hash := doc.Hash()

	// Expected canonical bytes (pinned)
	expectedBytes := "syndic-history/v1\nturn 8\nturn-abc\nstate 8\napproved\ninput 1\nQ\nanswer 1\nA\nlabel 8\nreviewed\n"
	if string(docBytes) != expectedBytes {
		t.Errorf("document bytes do not match pinned golden: got %q", string(docBytes))
	}

	// Expected hash (pinned) - SHA-256 of the above bytes
	h := sha256.Sum256([]byte(expectedBytes))
	expectedHash := hex.EncodeToString(h[:])
	if hash != expectedHash {
		t.Errorf("hash mismatch: got %s, expected %s", hash, expectedHash)
	}

	// Verify hash is deterministic (same document produces same hash)
	doc2 := New()
	if err := doc2.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}
	if doc2.Hash() != hash {
		t.Errorf("hash not deterministic: got %s, expected %s", doc2.Hash(), hash)
	}
}

// Golden fixture with newlines in content
func TestDocumentGoldenFixture_PinnedBytes_WithNewlines(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-123",
		State:        "approved",
		Input:        []byte("Line1\nLine2"),
		Answer:       []byte("Answer\nLine"),
		Label:        "reviewed",
		ChangesState: "",
		Changes:      nil,
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docBytes := doc.Bytes()
	hash := doc.Hash()

	// Verify the document is well-formed
	if !strings.HasPrefix(string(docBytes), "syndic-history/v1\n") {
		t.Errorf("missing preamble")
	}

	// Verify content is preserved
	if !strings.Contains(string(docBytes), "Line1\nLine2") {
		t.Errorf("input newlines not preserved")
	}
	if !strings.Contains(string(docBytes), "Answer\nLine") {
		t.Errorf("answer newlines not preserved")
	}

	// Hash should be 64 hex characters
	if len(hash) != 64 {
		t.Errorf("hash should be 64 hex chars, got %d: %s", len(hash), hash)
	}

	// Verify byte-exact roundtrip
	h := sha256.Sum256(docBytes)
	if hex.EncodeToString(h[:]) != hash {
		t.Errorf("hash calculation mismatch")
	}
}

// Golden fixture with changes
func TestDocumentGoldenFixture_PinnedBytes_WithChanges(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-ch",
		State:        "approved",
		Input:        []byte("code"),
		Answer:       []byte("updated"),
		Label:        "reviewed",
		ChangesState: "applied",
		Changes: []string{
			"file.go applied",
			"test.go not applied",
		},
	}

	if err := doc.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	docBytes := doc.Bytes()
	hash := doc.Hash()

	// Verify changes are present and correct
	docStr := string(docBytes)
	if !strings.Contains(docStr, "changes 7\napplied\n") {
		t.Errorf("changes state not found")
	}
	if !strings.Contains(docStr, "change 15\nfile.go applied\n") {
		t.Errorf("first change not found")
	}
	if !strings.Contains(docStr, "change 19\ntest.go not applied\n") {
		t.Errorf("second change not found")
	}

	// Hash must be deterministic
	doc2 := New()
	if err := doc2.AddTurn(turn); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}
	if doc2.Hash() != hash {
		t.Errorf("hash not deterministic")
	}
}

// Test strict parsing: reject non-canonical headers
func TestDocumentStrictParsing_RejectNonCanonical(t *testing.T) {
	tests := []struct {
		name    string
		docStr  string
		wantErr bool
	}{
		{
			name:    "valid",
			docStr:  "syndic-history/v1\nturn 1\nT\nstate 1\nA\ninput 1\nI\nanswer 1\nA\nlabel 8\nreviewed\n",
			wantErr: false,
		},
		{
			name:    "leading_zero_in_length",
			docStr:  "syndic-history/v1\nturn 01\nT\nstate 1\nA\ninput 1\nI\nanswer 1\nA\nlabel 8\nreviewed\n",
			wantErr: true,
		},
		{
			name:    "multiple_spaces",
			docStr:  "syndic-history/v1\nturn  1\nT\nstate 1\nA\ninput 1\nI\nanswer 1\nA\nlabel 8\nreviewed\n",
			wantErr: true,
		},
		{
			name:    "negative_length",
			docStr:  "syndic-history/v1\nturn -1\nT\nstate 1\nA\ninput 1\nI\nanswer 1\nA\nlabel 8\nreviewed\n",
			wantErr: true,
		},
		{
			name:    "positive_sign",
			docStr:  "syndic-history/v1\nturn +1\nT\nstate 1\nA\ninput 1\nI\nanswer 1\nA\nlabel 8\nreviewed\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDoc := &Document{
				buf:   bytes.NewBufferString(tt.docStr[len("syndic-history/v1\n"):]),
				turns: []Turn{},
			}

			// Try to parse the malformed document
			_, err := testDoc.RenderForAuthor()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for malformed document, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("valid document should parse: %v", err)
				}
			}
		})
	}
}

// Test change annotation validation
func TestDocumentChangeValidation(t *testing.T) {
	tests := []struct {
		name    string
		change  string
		wantErr bool
	}{
		{"valid_applied", "file.txt applied", false},
		{"valid_not_applied", "file.txt not applied", false},
		{"valid_conflict", "file.txt conflict", false},
		{"valid_path_with_spaces", "path/with spaces/file.txt applied", false},
		{"valid_long_path", "a/b/c/d/e/f/g/h/file.txt applied", false},
		{"invalid_no_separator", "file.txt", true},
		{"invalid_bad_outcome", "file.txt pending", true},
		{"invalid_empty_path", " applied", true},
		{"invalid_no_outcome", "file.txt ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateChange(tt.change)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateChange(%q) error = %v, wantErr %v", tt.change, err, tt.wantErr)
			}
		})
	}
}

// Test that changes must be paired with ChangesState
func TestDocumentChangesRequireState(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "approved",
		Input:        []byte("Q"),
		Answer:       []byte("A"),
		Label:        "reviewed",
		ChangesState: "",                       // empty!
		Changes:      []string{"f.go applied"}, // but changes present
	}

	err := doc.AddTurn(turn)
	if err == nil {
		t.Errorf("expected error when changes present but ChangesState empty")
	}
}

// Test trim selection (context_from_turn)
func TestDocumentTrimSelection(t *testing.T) {
	doc := New()
	turns := []Turn{
		{
			ID:           "turn-1",
			State:        "approved",
			Input:        []byte("Q1"),
			Answer:       []byte("A1"),
			Label:        "reviewed",
			ChangesState: "",
			Changes:      nil,
		},
		{
			ID:           "turn-2",
			State:        "approved",
			Input:        []byte("Q2"),
			Answer:       []byte("A2"),
			Label:        "reviewed",
			ChangesState: "",
			Changes:      nil,
		},
		{
			ID:           "turn-3",
			State:        "approved",
			Input:        []byte("Q3"),
			Answer:       []byte("A3"),
			Label:        "reviewed",
			ChangesState: "",
			Changes:      nil,
		},
	}

	for _, turn := range turns {
		if err := doc.AddTurn(turn); err != nil {
			t.Fatalf("AddTurn failed: %v", err)
		}
	}

	// Without trim, all turns should be present
	fullBytes := doc.Bytes()
	if !strings.Contains(string(fullBytes), "turn-1") {
		t.Errorf("turn-1 should be in full document")
	}
	if !strings.Contains(string(fullBytes), "turn-2") {
		t.Errorf("turn-2 should be in full document")
	}
	if !strings.Contains(string(fullBytes), "turn-3") {
		t.Errorf("turn-3 should be in full document")
	}

	// With trim from turn-2, only turns 2 and 3 should be present
	if err := doc.SetContextFromTurn("turn-2"); err != nil {
		t.Fatalf("SetContextFromTurn failed: %v", err)
	}
	trimmedBytes := doc.Bytes()
	if strings.Contains(string(trimmedBytes), "turn-1") {
		t.Errorf("turn-1 should not be in trimmed document")
	}
	if !strings.Contains(string(trimmedBytes), "turn-2") {
		t.Errorf("turn-2 should be in trimmed document")
	}
	if !strings.Contains(string(trimmedBytes), "turn-3") {
		t.Errorf("turn-3 should be in trimmed document")
	}

	// Hashes should be different
	fullHash := New()
	for _, turn := range turns {
		if err := fullHash.AddTurn(turn); err != nil {
			t.Fatalf("AddTurn failed: %v", err)
		}
	}
	fullHashVal := fullHash.Hash()

	trimmedHash := New()
	for _, turn := range turns {
		if err := trimmedHash.AddTurn(turn); err != nil {
			t.Fatalf("AddTurn failed: %v", err)
		}
	}
	if err := trimmedHash.SetContextFromTurn("turn-2"); err != nil {
		t.Fatalf("SetContextFromTurn failed: %v", err)
	}
	trimmedHashVal := trimmedHash.Hash()

	if fullHashVal == trimmedHashVal {
		t.Errorf("full and trimmed documents should have different hashes")
	}
}

// Test answer is ignored for incomplete/cancelled turns
func TestDocumentAnswerIgnoredForIncomplete(t *testing.T) {
	docWithSecret := New()
	docWithoutSecret := New()

	secretAnswer := []byte("SECRET_VALUE_12345")

	turnWithSecret := Turn{
		ID:           "turn-1",
		State:        "incomplete",
		Input:        []byte("Q"),
		Answer:       secretAnswer, // This should be ignored
		Label:        "unreviewed (solo)",
		ChangesState: "",
		Changes:      nil,
	}

	turnWithoutSecret := Turn{
		ID:           "turn-1",
		State:        "incomplete",
		Input:        []byte("Q"),
		Answer:       []byte(""), // Empty
		Label:        "unreviewed (solo)",
		ChangesState: "",
		Changes:      nil,
	}

	if err := docWithSecret.AddTurn(turnWithSecret); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}
	if err := docWithoutSecret.AddTurn(turnWithoutSecret); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	// Both documents should produce the same bytes for the answer field
	// because the secret answer is ignored for incomplete state
	bytesWithSecret := string(docWithSecret.Bytes())
	bytesWithoutSecret := string(docWithoutSecret.Bytes())

	if strings.Contains(bytesWithSecret, "SECRET_VALUE") {
		t.Errorf("secret answer should not appear in incomplete turn: %q", bytesWithSecret)
	}

	if bytesWithSecret != bytesWithoutSecret {
		t.Errorf("incomplete turn with secret answer should equal one without: got %q vs %q",
			bytesWithSecret, bytesWithoutSecret)
	}

	// Hashes should also be the same
	if docWithSecret.Hash() != docWithoutSecret.Hash() {
		t.Errorf("incomplete turn hashes should match regardless of Answer field")
	}
}

// Test SetContextFromTurn with unknown ID
func TestDocumentSetContextFromTurnUnknownID(t *testing.T) {
	doc := New()
	turns := []Turn{
		{
			ID:           "turn-1",
			State:        "approved",
			Input:        []byte("Q1"),
			Answer:       []byte("A1"),
			Label:        "reviewed",
			ChangesState: "",
			Changes:      nil,
		},
	}

	for _, turn := range turns {
		if err := doc.AddTurn(turn); err != nil {
			t.Fatalf("AddTurn failed: %v", err)
		}
	}

	// Try to set context with unknown turn ID
	err := doc.SetContextFromTurn("unknown-id")
	if err == nil {
		t.Errorf("expected error for unknown turn ID, got nil")
	}
}

// Test answer is ignored for cancelled turns
func TestDocumentAnswerIgnoredForCancelled(t *testing.T) {
	docWithSecret := New()
	docWithoutSecret := New()

	secretAnswer := []byte("ANOTHER_SECRET_98765")

	turnWithSecret := Turn{
		ID:           "turn-x",
		State:        "cancelled",
		Input:        []byte("Q"),
		Answer:       secretAnswer, // This should be ignored
		Label:        "unreviewed (solo)",
		ChangesState: "",
		Changes:      nil,
	}

	turnWithoutSecret := Turn{
		ID:           "turn-x",
		State:        "cancelled",
		Input:        []byte("Q"),
		Answer:       []byte(""), // Empty
		Label:        "unreviewed (solo)",
		ChangesState: "",
		Changes:      nil,
	}

	if err := docWithSecret.AddTurn(turnWithSecret); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}
	if err := docWithoutSecret.AddTurn(turnWithoutSecret); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	// Both should produce the same bytes
	bytesWithSecret := string(docWithSecret.Bytes())
	bytesWithoutSecret := string(docWithoutSecret.Bytes())

	if strings.Contains(bytesWithSecret, "ANOTHER_SECRET") {
		t.Errorf("secret answer should not appear in cancelled turn: %q", bytesWithSecret)
	}

	if bytesWithSecret != bytesWithoutSecret {
		t.Errorf("cancelled turn with secret answer should equal one without")
	}

	// Hashes should match
	if docWithSecret.Hash() != docWithoutSecret.Hash() {
		t.Errorf("cancelled turn hashes should match regardless of Answer field")
	}
}
