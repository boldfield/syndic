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
		EvidenceIDs:     []string{"ev1", "ev2"},
		ContextHash:     "ctx123",
		Round:           0,
		ChangesHash:     "changes456",
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	expected := "syndic-candidate/v2\nmanifest:abc123\nturn:turn1\nauthor_attempt:attempt1\ncontent_sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\ncontent_len:5\nevidence:ev1,ev2\ncontext:ctx123\nround:0\nchanges:changes456\n"
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
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "",
		Round:           1,
		ChangesHash:     "",
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	expected := "syndic-candidate/v2\nmanifest:abc123\nturn:turn1\nauthor_attempt:attempt1\ncontent_sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\ncontent_len:5\nevidence:ev1\ncontext:none\nround:1\nchanges:none\n"
	if string(canonical) != expected {
		t.Errorf("Canonical format mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(canonical))
	}
}

func TestCanonicalFormatV1Backward(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1", "ev2"},
		ContextHash:     "ctx123",
		Round:           0,
		ChangesHash:     "changes456",
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
		EvidenceIDs:     []string{"ev1", "ev2"},
		ContextHash:     "ctx123",
		Round:           0,
		ChangesHash:     "changes456",
	}

	hash, err := Hash(id)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	// Golden hash for v2 format with the above values
	expectedHash := "ec341de453a4e073a84d352cac19de3d9b3cf9f90b177fd77b6b0357d697cb16"
	if hash != expectedHash {
		t.Errorf("Hash mismatch.\nExpected: %s\nGot:      %s", expectedHash, hash)
	}
}

func TestHashConsistency(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1", "ev2"},
		ContextHash:     "ctx123",
		Round:           0,
		ChangesHash:     "changes456",
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
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "xyz789",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
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
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn2",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
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
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt2",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
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
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("world"),
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
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
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hallo"),
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
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
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev2", "ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
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

func TestEvidenceOrderMatters(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"a", "b"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"b", "a"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	canonical1, err := Canonical(id1)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}
	canonical2, err := Canonical(id2)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

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
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	if !strings.Contains(string(canonical), "evidence:none\n") {
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
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
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
		EvidenceIDs:     []string{},
		Round:           0,
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
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx2",
		Round:           0,
		ChangesHash:     "changes1",
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
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           1,
		ChangesHash:     "changes1",
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
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes1",
	}

	id2 := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{"ev1"},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes2",
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
		EvidenceIDs:     []string{},
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
		EvidenceIDs:     []string{},
		Round:           0,
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
		Round:           0,
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when evidence id contains comma")
	}
}

func TestNegativeRound(t *testing.T) {
	id := Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		EvidenceIDs:     []string{},
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
		EvidenceIDs:     []string{},
		ContextHash:     "ctx\n1",
		Round:           0,
		ChangesHash:     "changes1",
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
		EvidenceIDs:     []string{},
		ContextHash:     "ctx1",
		Round:           0,
		ChangesHash:     "changes\r1",
	}

	_, err := Canonical(id)
	if err == nil {
		t.Error("Should error when changes hash contains carriage return")
	}
}
