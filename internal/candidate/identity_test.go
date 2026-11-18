package candidate

import (
	"testing"
)

func TestCanonicalFormat(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1", "ev2"},
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	expected := "syndic-candidate/v1\nmanifest:abc123\nturn:turn1\nauthor_attempt:attempt1\ncontent_sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\ncontent_len:5\nevidence:ev1,ev2\n"
	if string(canonical) != expected {
		t.Errorf("Canonical format mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(canonical))
	}
}

func TestHashValue(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1", "ev2"},
	}

	hash, err := Hash(id)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if len(hash) != 64 {
		t.Errorf("Hash length should be 64, got %d", len(hash))
	}

	for _, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("Hash contains non-lowercase-hex character: %c", c)
		}
	}
}

func TestHashConsistency(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1", "ev2"},
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

func TestHashChangesWithManifest(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
	}

	id2 := Identity{
		ManifestHash:    "xyz789",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
	}

	hash1, _ := Hash(id1)
	hash2, _ := Hash(id2)

	if hash1 == hash2 {
		t.Error("Hash should change when manifest hash changes")
	}
}

func TestHashChangesWithTurnID(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn2",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
	}

	hash1, _ := Hash(id1)
	hash2, _ := Hash(id2)

	if hash1 == hash2 {
		t.Error("Hash should change when turn id changes")
	}
}

func TestHashChangesWithAuthorAttemptID(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt2",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
	}

	hash1, _ := Hash(id1)
	hash2, _ := Hash(id2)

	if hash1 == hash2 {
		t.Error("Hash should change when author attempt id changes")
	}
}

func TestHashChangesWithContent(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("world"),
		EvidenceIDs:     []string{"ev1"},
	}

	hash1, _ := Hash(id1)
	hash2, _ := Hash(id2)

	if hash1 == hash2 {
		t.Error("Hash should change when content changes")
	}
}

func TestHashChangesWithContentByte(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hallo"),
		EvidenceIDs:     []string{"ev1"},
	}

	hash1, _ := Hash(id1)
	hash2, _ := Hash(id2)

	if hash1 == hash2 {
		t.Error("Hash should change when one byte of content changes")
	}
}

func TestHashChangesWithEvidenceList(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1", "ev2"},
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev2", "ev1"},
	}

	hash1, _ := Hash(id1)
	hash2, _ := Hash(id2)

	if hash1 == hash2 {
		t.Error("Hash should change when evidence order changes")
	}
}

func TestEvidenceOrderMatters(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"a", "b"},
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"b", "a"},
	}

	canonical1, _ := Canonical(id1)
	canonical2, _ := Canonical(id2)

	if string(canonical1) == string(canonical2) {
		t.Error("Canonical forms should differ when evidence order differs")
	}
}

func TestNoEvidence(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{},
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	if !contains(string(canonical), "evidence:none\n") {
		t.Error("Should have evidence:none when no evidence ids")
	}
}

func TestEmptyContent(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte{},
		EvidenceIDs:     []string{"ev1"},
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	if !contains(string(canonical), "content_len:0\n") {
		t.Error("Should have content_len:0 for empty content")
	}
}

func TestNonUTF8Content(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte{0xFF, 0xFE, 0xFD},
		EvidenceIDs:     []string{},
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical should not error on non-UTF8 content: %v", err)
	}

	if !contains(string(canonical), "content_len:3\n") {
		t.Error("Should correctly count non-UTF8 bytes")
	}
}

func TestManifestHashEmpty(t *testing.T) {
	id := Identity{
		ManifestHash:    "",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{},
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
		EvidenceIDs:     []string{},
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when turn id contains newline")
	}
}

func TestEvidenceIDContainsComma(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev,1"},
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when evidence id contains comma")
	}
}

func contains(s, substr string) bool {
	for i := 0; i < len(s)-len(substr)+1; i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
