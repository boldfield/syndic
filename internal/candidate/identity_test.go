package candidate

import (
	"strings"
	"testing"
)

func TestCanonicalFormatV2(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{CallID: "call2", ResultSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	expected := "syndic-candidate/v2\nmanifest:abc123\nturn:turn1\nauthor_attempt:attempt1\ncontent_sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\ncontent_len:5\nevidence:call1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa,call2:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\ncontext:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\nround:0\nchanges:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\n"
	if string(canonical) != expected {
		t.Errorf("Canonical format mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(canonical))
	}
}

func TestCanonicalFormatV2WithNone(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "",
		Round:       1,
		ChangesHash: "",
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	expected := "syndic-candidate/v2\nmanifest:abc123\nturn:turn1\nauthor_attempt:attempt1\ncontent_sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\ncontent_len:5\nevidence:call1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ncontext:none\nround:1\nchanges:none\n"
	if string(canonical) != expected {
		t.Errorf("Canonical format mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(canonical))
	}
}

func TestCanonicalFormatV1Backward(t *testing.T) {
	id := IdentityV1{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1", "ev2"},
	}

	canonical, err := CanonicalV1(id)
	if err != nil {
		t.Fatalf("CanonicalV1 failed: %v", err)
	}

	expected := "syndic-candidate/v1\nmanifest:abc123\nturn:turn1\nauthor_attempt:attempt1\ncontent_sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\ncontent_len:5\nevidence:ev1,ev2\n"
	if string(canonical) != expected {
		t.Errorf("CanonicalV1 format mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(canonical))
	}
}

func TestHashValue(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{CallID: "call2", ResultSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	hash, err := Hash(id)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	// Golden hash must be computed correctly
	if len(hash) != 64 {
		t.Errorf("Hash should be 64 hex characters, got %d", len(hash))
	}

	// Hash should be lowercase hex
	for _, c := range hash {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			t.Errorf("Hash contains invalid character: %c", c)
		}
	}
}

func TestHashConsistency(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	hash1, err := Hash(id)
	if err != nil {
		t.Fatalf("First Hash failed: %v", err)
	}

	hash2, err := Hash(id)
	if err != nil {
		t.Fatalf("Second Hash failed: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("Hash not consistent: %s vs %s", hash1, hash2)
	}
}

func TestHashChangesWithResultSHA256(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	hash1, err := Hash(id1)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	hash2, err := Hash(id2)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Hash should change when result_sha256 changes")
	}
}

func TestHashChangesWithEvidenceOrder(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{CallID: "call2", ResultSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call2", ResultSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	hash1, err := Hash(id1)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	hash2, err := Hash(id2)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Hash should change when evidence order changes")
	}
}

// Sentinel collision tests: ensure "none" and empty string are distinct
func TestContextHashNoneCollision(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	canonical, err := Canonical(id1)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	// Both should produce "context:none" but we validate that the literal "none" string is rejected
	if !strings.Contains(string(canonical), "context:none\n") {
		t.Error("Should produce context:none for empty context_hash")
	}

	// Test that literal "none" is rejected as invalid
	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		ContextHash:     "none",
		Round:           0,
		ChangesHash:     "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	_, err = Canonical(id2)
	if err == nil {
		t.Error("Should reject literal 'none' string for context_hash")
	}
}

func TestChangesHashNoneCollision(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		ContextHash:     "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:           0,
		ChangesHash:     "",
	}

	canonical, err := Canonical(id1)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	if !strings.Contains(string(canonical), "changes:none\n") {
		t.Error("Should produce changes:none for empty changes_hash")
	}

	// Test that literal "none" is rejected as invalid
	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		ContextHash:     "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:           0,
		ChangesHash:     "none",
	}

	_, err = Canonical(id2)
	if err == nil {
		t.Error("Should reject literal 'none' string for changes_hash")
	}
}

func TestHashV1Consistency(t *testing.T) {
	id := IdentityV1{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1", "ev2"},
	}

	hash1, err := HashV1(id)
	if err != nil {
		t.Fatalf("First HashV1 failed: %v", err)
	}

	hash2, err := HashV1(id)
	if err != nil {
		t.Fatalf("Second HashV1 failed: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("HashV1 not consistent: %s vs %s", hash1, hash2)
	}
}

func TestV1LegacyCanonicalFormat(t *testing.T) {
	id := IdentityV1{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{},
	}

	canonical, err := CanonicalV1(id)
	if err != nil {
		t.Fatalf("CanonicalV1 failed: %v", err)
	}

	expected := "syndic-candidate/v1\nmanifest:abc123\nturn:turn1\nauthor_attempt:attempt1\ncontent_sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\ncontent_len:5\nevidence:none\n"
	if string(canonical) != expected {
		t.Errorf("CanonicalV1 format mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(canonical))
	}
}

func TestNoEvidence(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		ContextHash:     "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:           0,
		ChangesHash:     "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	if !strings.Contains(string(canonical), "evidence:none\n") {
		t.Error("Should have evidence:none when no evidence pairs")
	}
}

func TestEmptyContent(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte{},
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	if !strings.Contains(string(canonical), "content_len:0\n") {
		t.Error("Should have content_len:0 for empty content")
	}
}

func TestNonUTF8Content(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte{0xFF, 0xFE, 0xFD},
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical should not error on non-UTF8 content: %v", err)
	}

	if !strings.Contains(string(canonical), "content_len:3\n") {
		t.Error("Should correctly count non-UTF8 bytes")
	}
}

func TestHashChangesWithContextHash(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	hash1, err := Hash(id1)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	hash2, err := Hash(id2)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Hash should change when context hash changes")
	}
}

func TestHashChangesWithRound(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       1,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	hash1, err := Hash(id1)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	hash2, err := Hash(id2)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Hash should change when round changes")
	}
}

func TestHashChangesWithChangesHash(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}

	hash1, err := Hash(id1)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}
	hash2, err := Hash(id2)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Hash should change when changes hash changes")
	}
}

func TestManifestHashEmpty(t *testing.T) {
	id := Identity{
		ManifestHash:    "",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		Round:           0,
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when manifest hash is empty")
	}
}

func TestTurnIDContainsNewline(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn\n1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		Round:           0,
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when turn id contains newline")
	}
}

func TestEvidenceCallIDContainsColon(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call:1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		Round: 0,
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when call_id contains colon")
	}
}

func TestEvidenceResultSHA256Invalid(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "not-hex"},
		},
		Round: 0,
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when result_sha256 is not valid 64-hex")
	}
}

func TestNegativeRound(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		Round:           -1,
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when round is negative")
	}
}

func TestContextHashContainsNewline(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		ContextHash:     "ctx\n1",
		Round:           0,
		ChangesHash:     "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when context hash contains newline")
	}
}

func TestChangesHashContainsNewline(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		ContextHash:     "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:           0,
		ChangesHash:     "changes\r1",
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when changes hash contains carriage return")
	}
}

// Comprehensive v2 golden test with all fields
func TestGoldenV2Complete(t *testing.T) {
	id := Identity{
		ManifestHash:    "manifest123",
		TurnID:          "turn456",
		AuthorAttemptID: "attempt789",
		Content:         []byte("The answer to the ultimate question is 42."),
		Evidence: []EvidencePair{
			{CallID: "tool_call_1", ResultSHA256: "1111111111111111111111111111111111111111111111111111111111111111"},
			{CallID: "tool_call_2", ResultSHA256: "2222222222222222222222222222222222222222222222222222222222222222"},
			{CallID: "tool_call_3", ResultSHA256: "3333333333333333333333333333333333333333333333333333333333333333"},
		},
		ContextHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Round:       2,
		ChangesHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	// Verify structure
	lines := strings.Split(string(canonical), "\n")
	if lines[0] != "syndic-candidate/v2" {
		t.Errorf("First line should be syndic-candidate/v2, got %q", lines[0])
	}

	// Compute hash and verify it's valid
	hash, err := Hash(id)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if !isSHA256Hex(hash) {
		t.Errorf("Hash should be valid SHA-256 hex, got %q", hash)
	}

	// Verify that changing evidence order changes hash
	id2 := Identity{
		ManifestHash:    "manifest123",
		TurnID:          "turn456",
		AuthorAttemptID: "attempt789",
		Content:         []byte("The answer to the ultimate question is 42."),
		Evidence: []EvidencePair{
			{CallID: "tool_call_3", ResultSHA256: "3333333333333333333333333333333333333333333333333333333333333333"},
			{CallID: "tool_call_2", ResultSHA256: "2222222222222222222222222222222222222222222222222222222222222222"},
			{CallID: "tool_call_1", ResultSHA256: "1111111111111111111111111111111111111111111111111111111111111111"},
		},
		ContextHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Round:       2,
		ChangesHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}

	hash2, err := Hash(id2)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash == hash2 {
		t.Error("Hash should change when evidence order changes")
	}
}
