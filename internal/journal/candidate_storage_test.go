package journal_test

import (
	"testing"

	"github.com/boldfield/syndic/internal/candidate"
	"github.com/boldfield/syndic/internal/journal"
)

// TestLegacyV1StoredCandidateReadability verifies that a candidate stored with a
// historical v1 hash remains readable from journal storage unchanged, without
// rehashing. This tests the acceptance criterion "Legacy stored candidates remain
// readable without rehashing".
func TestLegacyV1StoredCandidateReadability(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	conn := db.Conn()
	store := journal.NewStore(db)

	// Set up conversation, run, and turn
	mustExec(t, conn, `INSERT INTO conversations (id, workspace) VALUES ('conv-1', NULL)`)
	mustExec(t, conn, `INSERT INTO runs (id, manifest, manifest_hash) VALUES ('run-1', '{}', 'mh')`)
	mustExec(t, conn, `INSERT INTO turns (id, run_id, mode, conversation_id) VALUES ('turn-1', 'run-1', 'solo', 'conv-1')`)

	// The golden v1 hash as it was computed historically
	const goldenV1Hash = "8c24d1efe000fe81f3d653df3740ed0c201461beeaf46df78a8433659dd5878e"

	// The content that was used to compute the v1 hash
	const candidateContent = "hello"

	// Create a candidate persisted with the golden v1 hash
	candID, err := store.CreateCandidate("turn-1", candidateContent, goldenV1Hash)
	if err != nil {
		t.Fatalf("CreateCandidate failed: %v", err)
	}

	// Read the candidate back from storage through the production path
	candRow, err := store.GetCandidate(candID)
	if err != nil {
		t.Fatalf("GetCandidate failed: %v", err)
	}

	// Assertion 1: The stored hash equals the golden byte for byte
	if candRow.ContentHash != goldenV1Hash {
		t.Errorf("Stored hash mismatch.\nExpected: %s\nGot:      %s", goldenV1Hash, candRow.ContentHash)
	}

	// Assertion 2: Recompute HashV1 from the same content and verify it reproduces
	// the golden hash. This verifies the hash is deterministic.
	v1Identity := candidate.IdentityV1{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte(candidateContent),
		EvidenceIDs:     []string{"ev1", "ev2"},
	}

	recomputedV1Hash, err := candidate.HashV1(v1Identity)
	if err != nil {
		t.Fatalf("Failed to recompute v1 hash: %v", err)
	}

	if recomputedV1Hash != goldenV1Hash {
		t.Errorf("HashV1 recomputation failed.\nExpected: %s\nGot:      %s", goldenV1Hash, recomputedV1Hash)
	}

	// Assertion 3: Compute the v2 hash from the same content and verify it does NOT
	// equal the v1 hash. This demonstrates that the stored candidate was not silently
	// rehashed into v2 and that v1 and v2 hashes remain distinct.
	v2Identity := candidate.Identity{
		ManifestHash:    "abc123",
		TurnID:          "turn1",
		AuthorAttemptID: "attempt1",
		Content:         []byte(candidateContent),
		Evidence: []candidate.EvidencePair{
			{CallID: "ev1", ResultSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{CallID: "ev2", ResultSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		ContextHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Round:       1,
		ChangesHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	}

	v2Hash, err := candidate.Hash(v2Identity)
	if err != nil {
		t.Fatalf("Failed to compute v2 hash: %v", err)
	}

	if v2Hash == goldenV1Hash {
		t.Errorf("v2 hash should differ from v1 hash, but both are %s", goldenV1Hash)
	}

	// Final verification: the candidate read from storage still has the v1 hash
	// and was never migrated to v2
	if candRow.ContentHash != recomputedV1Hash {
		t.Errorf("Stored candidate hash diverged after retrieval.\nStored: %s\nRecomputed: %s", candRow.ContentHash, recomputedV1Hash)
	}
}
