package history

import (
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
		IsIncomplete: false,
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
		IsIncomplete: false,
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
		IsIncomplete: false,
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
		Label:        "unreviewed",
		IsIncomplete: true,
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
		Label:        "unreviewed",
		IsIncomplete: true,
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
		IsIncomplete: false,
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
		IsIncomplete: false,
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
		IsIncomplete: false,
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
		IsIncomplete: false,
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
		IsIncomplete: false,
	}); err != nil {
		t.Fatalf("AddTurn failed: %v", err)
	}

	if doc1.Hash() == doc2.Hash() {
		t.Errorf("different labels should produce different hashes")
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
			IsIncomplete: false,
		},
		{
			ID:           "turn-2",
			State:        "approved",
			Input:        []byte("Second question"),
			Answer:       []byte("Second answer"),
			Label:        "reviewed",
			IsIncomplete: false,
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
			IsIncomplete: false,
		},
		{
			ID:           "turn-2",
			State:        "approved",
			Input:        []byte("What is 3+3?"),
			Answer:       []byte("6"),
			Label:        "reviewed",
			IsIncomplete: false,
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

	// Should have 4 messages: 2 inputs + 2 answers
	if len(messages) != 4 {
		t.Errorf("expected 4 messages, got %d: %v", len(messages), messages)
	}

	expectedSequence := []string{
		"What is 2+2?",
		"4",
		"What is 3+3?",
		"6",
	}

	for i, expected := range expectedSequence {
		if messages[i] != expected {
			t.Errorf("message %d: expected %q, got %q", i, expected, messages[i])
		}
	}
}

func TestDocumentRenderForAuthorWithMissingAnswer(t *testing.T) {
	doc := New()
	turn := Turn{
		ID:           "turn-1",
		State:        "incomplete",
		Input:        []byte("Original question"),
		Answer:       []byte(""),
		Label:        "unreviewed",
		IsIncomplete: true,
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

	if messages[0] != "Original question" {
		t.Errorf("first message should be input, got %q", messages[0])
	}

	if !strings.Contains(messages[1], "no approved answer") {
		t.Errorf("second message should contain missing-answer marker, got %q", messages[1])
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
		IsIncomplete: false,
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

	if messages[0] != input {
		t.Errorf("input not preserved: expected %q, got %q", input, messages[0])
	}

	if messages[1] != answer {
		t.Errorf("answer not preserved: expected %q, got %q", answer, messages[1])
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
		IsIncomplete: false,
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
			IsIncomplete: false,
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
