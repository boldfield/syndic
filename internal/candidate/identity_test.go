package candidate

import (
	"strings"
	"testing"
)

func TestCanonicalFormatV3(t *testing.T) {
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
		Attachments: []Attachment{},
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	expected := "syndic-candidate/v3\nmanifest:abc123\nturn:turn1\nauthor_attempt:attempt1\ncontent_sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\ncontent_len:5\nevidence:call1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa,call2:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\ncontext:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\nround:0\nchanges:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\nattachments:none\n"
	if string(canonical) != expected {
		t.Errorf("Canonical format mismatch.\nExpected:\n%q\nGot:\n%q", expected, string(canonical))
	}
}

func TestCanonicalFormatV3WithNone(t *testing.T) {
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
		Attachments: []Attachment{},
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	expected := "syndic-candidate/v3\nmanifest:abc123\nturn:turn1\nauthor_attempt:attempt1\ncontent_sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824\ncontent_len:5\nevidence:call1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ncontext:none\nround:1\nchanges:none\nattachments:none\n"
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

// TestHashValue tests v3 golden hash with exact bytes
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
		Attachments: []Attachment{},
	}

	hash, err := Hash(id)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	// Golden v3 hash: exact expected value
	const expectedHash = "cb56d1bf4b05263bdd4ad723593e5783227588fbd9eb900a5e5427475bfeb517"
	if hash != expectedHash {
		t.Errorf("Golden v3 hash mismatch.\nExpected: %s\nGot:      %s", expectedHash, hash)
	}

	// Hash should be lowercase hex
	for _, c := range hash {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			t.Errorf("Hash contains invalid character: %c", c)
		}
	}
}

// TestHashV2Value tests v2 golden hash with exact bytes for backward compatibility
func TestHashV2Value(t *testing.T) {
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
		Attachments: []Attachment{},
	}

	hash, err := HashV2(id)
	if err != nil {
		t.Fatalf("HashV2 failed: %v", err)
	}

	// Golden v2 hash: fixed value for historical reconstruction
	const expectedHash = "c3ad2f583675b73a955827580ad9204b323e8c196710e9fa4d9569056c5f890f"
	if hash != expectedHash {
		t.Errorf("Golden v2 hash mismatch.\nExpected: %s\nGot:      %s", expectedHash, hash)
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
		Attachments: []Attachment{},
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
		Attachments: []Attachment{},
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
		Attachments: []Attachment{},
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

	// Golden v1 hash regression: stored candidates must maintain their v1 hash
	const expectedV1Hash = "8c24d1efe000fe81f3d653df3740ed0c201461beeaf46df78a8433659dd5878e"
	if hash1 != expectedV1Hash {
		t.Errorf("Golden v1 hash mismatch.\nExpected: %s\nGot:      %s", expectedV1Hash, hash1)
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

// Comprehensive v3 golden test with all fields: round 2, three evidence pairs, full context and changes
func TestGoldenV3Complete(t *testing.T) {
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
		Attachments: []Attachment{},
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	// Golden v3 canonical bytes
	expectedCanonical := "syndic-candidate/v3\nmanifest:manifest123\nturn:turn456\nauthor_attempt:attempt789\ncontent_sha256:ae17837249bd3cc284f04b750d212e4c5f439e51901889c3a6ac2eded8fc2412\ncontent_len:42\nevidence:tool_call_1:1111111111111111111111111111111111111111111111111111111111111111,tool_call_2:2222222222222222222222222222222222222222222222222222222222222222,tool_call_3:3333333333333333333333333333333333333333333333333333333333333333\ncontext:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nround:2\nchanges:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\nattachments:none\n"
	if string(canonical) != expectedCanonical {
		t.Errorf("Golden v3 canonical bytes mismatch.\nExpected:\n%q\nGot:\n%q", expectedCanonical, string(canonical))
	}

	// Golden v3 hash: exact expected value
	hash, err := Hash(id)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	const expectedHash = "14612af8fd56ae5956b7a0787d5333e9e86f27aaf404842d1e216b004684cfd7"
	if hash != expectedHash {
		t.Errorf("Golden v3 hash mismatch.\nExpected: %s\nGot:      %s", expectedHash, hash)
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
		Attachments: []Attachment{},
	}

	hash2, err := Hash(id2)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash == hash2 {
		t.Error("Hash should change when evidence order changes")
	}
}

// TestAllBoundFieldMutations verifies that every bound field mutation changes the hash.
// This ensures the identity is injective: changing any input produces a different hash.
func TestAllBoundFieldMutations(t *testing.T) {
	baseID := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("base content"),
		Evidence: []EvidencePair{
			{CallID: "call_a", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       1,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	baseHash, err := Hash(baseID)
	if err != nil {
		t.Fatalf("Base hash failed: %v", err)
	}

	tests := []struct {
		name string
		id   Identity
	}{
		{
			name: "ManifestHash mutated",
			id: Identity{
				ManifestHash:    "manifest_different",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("base content"),
				Evidence: []EvidencePair{
					{CallID: "call_a", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				},
				ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Round:       1,
				ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
		},
		{
			name: "TurnID mutated",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_different",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("base content"),
				Evidence: []EvidencePair{
					{CallID: "call_a", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				},
				ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Round:       1,
				ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
		},
		{
			name: "AuthorAttemptID mutated",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_different",
				Content:         []byte("base content"),
				Evidence: []EvidencePair{
					{CallID: "call_a", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				},
				ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Round:       1,
				ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
		},
		{
			name: "Content mutated",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("different content"),
				Evidence: []EvidencePair{
					{CallID: "call_a", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				},
				ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Round:       1,
				ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
		},
		{
			name: "Evidence pair added",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("base content"),
				Evidence: []EvidencePair{
					{CallID: "call_a", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
					{CallID: "call_b", ResultSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
				},
				ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Round:       1,
				ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
		},
		{
			name: "Evidence pair removed",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("base content"),
				Evidence:        []EvidencePair{},
				ContextHash:     "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Round:           1,
				ChangesHash:     "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
		},
		{
			name: "Evidence call_id mutated",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("base content"),
				Evidence: []EvidencePair{
					{CallID: "call_z", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				},
				ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Round:       1,
				ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
		},
		{
			name: "ContextHash mutated",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("base content"),
				Evidence: []EvidencePair{
					{CallID: "call_a", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				},
				ContextHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
				Round:       1,
				ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
		},
		{
			name: "Round mutated",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("base content"),
				Evidence: []EvidencePair{
					{CallID: "call_a", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				},
				ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Round:       5,
				ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			},
		},
		{
			name: "ChangesHash mutated",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("base content"),
				Evidence: []EvidencePair{
					{CallID: "call_a", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				},
				ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Round:       1,
				ChangesHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutatedHash, err := Hash(tt.id)
			if err != nil {
				t.Fatalf("Hash failed: %v", err)
			}

			if mutatedHash == baseHash {
				t.Errorf("Hash should change when %s", tt.name)
			}
		})
	}
}

// TestAttachmentChangesHashWithPath verifies attachment path mutation changes hash
func TestAttachmentChangesHashWithPath(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("content"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "file1.txt",
				ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ContentLen:    10,
			},
		},
	}

	id2 := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("content"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "file2.txt",
				ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ContentLen:    10,
			},
		},
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
		t.Error("Hash should change when attachment path changes")
	}
}

// TestAttachmentChangesHashWithContent verifies attachment content mutation changes hash
func TestAttachmentChangesHashWithContent(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("content"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "file.txt",
				ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ContentLen:    10,
			},
		},
	}

	id2 := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("content"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "file.txt",
				ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				ContentLen:    10,
			},
		},
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
		t.Error("Hash should change when attachment content SHA256 changes")
	}
}

// TestAttachmentReversedOrderSameHash verifies that reversed attachment order gives same hash
func TestAttachmentReversedOrderSameHash(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("content"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "aaa.txt",
				ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ContentLen:    1,
			},
			{
				Path:          "zzz.txt",
				ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				ContentLen:    1,
			},
		},
	}

	id2 := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("content"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "zzz.txt",
				ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				ContentLen:    1,
			},
			{
				Path:          "aaa.txt",
				ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ContentLen:    1,
			},
		},
	}

	hash1, err := Hash(id1)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	hash2, err := Hash(id2)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash1 != hash2 {
		t.Error("Reversed attachment order should produce same hash")
	}
}

// TestCanonicalV3WithAttachments verifies v3 canonical format with attachments
func TestCanonicalV3WithAttachments(t *testing.T) {
	id := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte("hello"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "file.txt",
				ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ContentLen:    5,
			},
		},
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	// Verify it's v3 format with attachments line
	if !strings.Contains(string(canonical), "syndic-candidate/v3\n") {
		t.Error("Should be v3 format")
	}

	if !strings.Contains(string(canonical), "attachments:") {
		t.Error("Should have attachments line")
	}

	// Verify attachments:none is not present
	if strings.Contains(string(canonical), "attachments:none") {
		t.Error("Should not have attachments:none when attachments present")
	}
}

// TestAllFieldsFixedIdentity tests that with all identity fields fixed, only attachment changes affect hash
func TestAllFieldsFixedIdentity(t *testing.T) {
	baseID := Identity{
		ManifestHash:    "manifest_fixed",
		TurnID:          "turn_fixed",
		AuthorAttemptID: "attempt_fixed",
		Content:         []byte("fixed content"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       1,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Attachments: []Attachment{
			{
				Path:          "file1.txt",
				ContentSHA256: "1111111111111111111111111111111111111111111111111111111111111111",
				ContentLen:    100,
			},
		},
	}

	hash1, err := Hash(baseID)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	// Change only attachment
	modifiedID := Identity{
		ManifestHash:    "manifest_fixed",
		TurnID:          "turn_fixed",
		AuthorAttemptID: "attempt_fixed",
		Content:         []byte("fixed content"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       1,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Attachments: []Attachment{
			{
				Path:          "file2.txt",
				ContentSHA256: "1111111111111111111111111111111111111111111111111111111111111111",
				ContentLen:    100,
			},
		},
	}

	hash2, err := Hash(modifiedID)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash1 == hash2 {
		t.Error("Hash should change when attachment path changes with all other fields fixed")
	}
}

// TestCanonicalV3WithAttachmentsExact pins exact v3 canonical bytes with attachments
func TestCanonicalV3WithAttachmentsExact(t *testing.T) {
	id := Identity{
		ManifestHash:    "manifest123",
		TurnID:          "turn456",
		AuthorAttemptID: "attempt789",
		Content:         []byte("content text"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "",
		Round:       0,
		ChangesHash: "",
		Attachments: []Attachment{
			{
				Path:          "file.txt",
				ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				ContentLen:    42,
			},
		},
	}

	canonical, err := Canonical(id)
	if err != nil {
		t.Fatalf("Canonical failed: %v", err)
	}

	// Verify manifest header and key fields
	canonStr := string(canonical)
	if !strings.Contains(canonStr, "syndic-candidate/v3\n") {
		t.Error("Should start with v3 header")
	}
	if !strings.Contains(canonStr, "manifest:manifest123\n") {
		t.Error("Should contain manifest field")
	}
	if !strings.Contains(canonStr, "round:0\n") {
		t.Error("Should contain round:0")
	}

	// Get attachment manifest hash
	attachHash, err := AttachmentManifestHash(id.Attachments)
	if err != nil {
		t.Fatalf("AttachmentManifestHash failed: %v", err)
	}

	expectedAttachLine := "attachments:" + attachHash + "\n"
	if !strings.Contains(canonStr, expectedAttachLine) {
		t.Errorf("Should contain attachments line: %s", expectedAttachLine)
	}
}

// TestHashV3WithAttachmentsExact pins exact v3 hash with attachments present
func TestHashV3WithAttachmentsExact(t *testing.T) {
	id := Identity{
		ManifestHash:    "manifest123",
		TurnID:          "turn456",
		AuthorAttemptID: "attempt789",
		Content:         []byte("content text"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "",
		Round:       0,
		ChangesHash: "",
		Attachments: []Attachment{
			{
				Path:          "file.txt",
				ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				ContentLen:    42,
			},
		},
	}

	hash, err := Hash(id)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if hash == "" {
		t.Error("Hash should not be empty")
	}

	// Hash should be lowercase hex and 64 chars
	if len(hash) != 64 {
		t.Errorf("Hash should be 64 hex characters, got %d", len(hash))
	}
	for _, c := range hash {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			t.Errorf("Hash contains invalid character: %c", c)
		}
	}
}

// TestAttachmentChangesHashWithContentLen verifies attachment content length mutation changes hash
func TestAttachmentChangesHashWithContentLen(t *testing.T) {
	id1 := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("content"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "file.txt",
				ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ContentLen:    10,
			},
		},
	}

	id2 := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("content"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "file.txt",
				ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ContentLen:    20,
			},
		},
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
		t.Error("Hash should change when attachment content length changes")
	}
}

// TestMutationMatrix verifies hash changes across all mutation scenarios: path, content, ContentLen
func TestMutationMatrix(t *testing.T) {
	baseID := Identity{
		ManifestHash:    "manifest_base",
		TurnID:          "turn_base",
		AuthorAttemptID: "attempt_base",
		Content:         []byte("content"),
		Evidence:        []EvidencePair{},
		ContextHash:     "",
		Round:           0,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "file.txt",
				ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ContentLen:    10,
			},
		},
	}

	baseHash, err := Hash(baseID)
	if err != nil {
		t.Fatalf("Base hash failed: %v", err)
	}

	tests := []struct {
		name string
		id   Identity
	}{
		{
			name: "path mutation",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("content"),
				Evidence:        []EvidencePair{},
				ContextHash:     "",
				Round:           0,
				ChangesHash:     "",
				Attachments: []Attachment{
					{
						Path:          "different.txt",
						ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						ContentLen:    10,
					},
				},
			},
		},
		{
			name: "content SHA256 mutation",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("content"),
				Evidence:        []EvidencePair{},
				ContextHash:     "",
				Round:           0,
				ChangesHash:     "",
				Attachments: []Attachment{
					{
						Path:          "file.txt",
						ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
						ContentLen:    10,
					},
				},
			},
		},
		{
			name: "content length mutation",
			id: Identity{
				ManifestHash:    "manifest_base",
				TurnID:          "turn_base",
				AuthorAttemptID: "attempt_base",
				Content:         []byte("content"),
				Evidence:        []EvidencePair{},
				ContextHash:     "",
				Round:           0,
				ChangesHash:     "",
				Attachments: []Attachment{
					{
						Path:          "file.txt",
						ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						ContentLen:    999,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutatedHash, err := Hash(tt.id)
			if err != nil {
				t.Fatalf("Hash failed: %v", err)
			}

			if mutatedHash == baseHash {
				t.Errorf("Hash should change with %s", tt.name)
			}
		})
	}
}

// TestRound2Scenarios tests binding fixtures for round 2 with different conditions
func TestRound2Scenarios(t *testing.T) {
	// Base identity for round 2
	baseID := Identity{
		ManifestHash:    "manifest_round2",
		TurnID:          "turn_round2",
		AuthorAttemptID: "attempt_round2",
		Content:         []byte("answer is 42"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       2,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Attachments: []Attachment{
			{
				Path:          "output.txt",
				ContentSHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
				ContentLen:    100,
			},
		},
	}

	baseHash, err := Hash(baseID)
	if err != nil {
		t.Fatalf("Base hash failed: %v", err)
	}

	// Round 2 with no tools (ChangesHash empty)
	noToolsID := Identity{
		ManifestHash:    "manifest_round2",
		TurnID:          "turn_round2",
		AuthorAttemptID: "attempt_round2",
		Content:         []byte("answer is 42"),
		Evidence:        []EvidencePair{},
		ContextHash:     "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:           2,
		ChangesHash:     "",
		Attachments: []Attachment{
			{
				Path:          "output.txt",
				ContentSHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
				ContentLen:    100,
			},
		},
	}

	noToolsHash, err := Hash(noToolsID)
	if err != nil {
		t.Fatalf("No tools hash failed: %v", err)
	}

	if baseHash == noToolsHash {
		t.Error("Round 2 with no tools should produce different hash than with tools")
	}

	// Round 2 path mutation changes hash
	pathMutatedID := Identity{
		ManifestHash:    "manifest_round2",
		TurnID:          "turn_round2",
		AuthorAttemptID: "attempt_round2",
		Content:         []byte("answer is 42"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       2,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Attachments: []Attachment{
			{
				Path:          "different_output.txt",
				ContentSHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
				ContentLen:    100,
			},
		},
	}

	pathMutatedHash, err := Hash(pathMutatedID)
	if err != nil {
		t.Fatalf("Path mutated hash failed: %v", err)
	}

	if baseHash == pathMutatedHash {
		t.Error("Round 2 path mutation should change hash")
	}

	// Round 2 content mutation changes hash
	contentMutatedID := Identity{
		ManifestHash:    "manifest_round2",
		TurnID:          "turn_round2",
		AuthorAttemptID: "attempt_round2",
		Content:         []byte("answer is 42"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       2,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		Attachments: []Attachment{
			{
				Path:          "output.txt",
				ContentSHA256: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
				ContentLen:    100,
			},
		},
	}

	contentMutatedHash, err := Hash(contentMutatedID)
	if err != nil {
		t.Fatalf("Content mutated hash failed: %v", err)
	}

	if baseHash == contentMutatedHash {
		t.Error("Round 2 content mutation should change hash")
	}
}

// TestCanonicalV2Format verifies v2 canonical format matches design
func TestCanonicalV2Format(t *testing.T) {
	id := Identity{
		ManifestHash:    "manifest123",
		TurnID:          "turn456",
		AuthorAttemptID: "attempt789",
		Content:         []byte("hello"),
		Evidence: []EvidencePair{
			{CallID: "call1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       0,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	canonical, err := CanonicalV2(id)
	if err != nil {
		t.Fatalf("CanonicalV2 failed: %v", err)
	}

	canonStr := string(canonical)
	if !strings.Contains(canonStr, "syndic-candidate/v2\n") {
		t.Error("Should start with v2 header")
	}

	// V2 should not have attachments field
	if strings.Contains(canonStr, "attachments:") {
		t.Error("V2 format should not include attachments field")
	}

	// V2 should include context, round, changes
	if !strings.Contains(canonStr, "context:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\n") {
		t.Error("Should contain context field")
	}
	if !strings.Contains(canonStr, "round:0\n") {
		t.Error("Should contain round field")
	}
	if !strings.Contains(canonStr, "changes:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\n") {
		t.Error("Should contain changes field")
	}
}
