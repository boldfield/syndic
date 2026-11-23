package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

func TestVerifiedReviewsPage(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestReviews {
			return true
		}

		// Create a test review item
		item := wire.ReviewItem{
			Round:         0,
			CandidateHash: "hash123",
			Participant:   "reviewer-1",
			AttemptID:     "attempt-1",
			Kind:          "approve",
			Blocks:        []wire.ReviewBlock{},
			Commentary: &wire.ContentReference{
				Ref:     "commentary:attempt-1",
				SHA256:  "abc123",
				ByteLen: 10,
			},
		}
		itemBytes, _ := json.Marshal(item)

		reply := wire.ReviewsReply{
			Items:      []json.RawMessage{itemBytes},
			NextCursor: nil,
		}
		replyBody, _ := wire.NewReply(frame.ID, reply)
		_ = w.Write(replyBody)
		return false
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Test fetching verified reviews page
	items, nextCursor, err := client.VerifiedReviewsPage(ctx, "turn-1", nil, nil)
	if err != nil {
		t.Fatalf("VerifiedReviewsPage failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Participant != "reviewer-1" {
		t.Fatalf("unexpected participant: %s", items[0].Participant)
	}
	if nextCursor != nil {
		t.Fatalf("expected no next cursor, got %v", nextCursor)
	}
}

func TestFetchOversizedRecord(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}

		// Create a full review record
		recordItem := wire.ReviewItem{
			Round:         0,
			CandidateHash: "hash123",
			Participant:   "reviewer-1",
			AttemptID:     "attempt-1",
			Kind:          "approve",
			Blocks: []wire.ReviewBlock{
				{
					BlockID: "block-1",
					State:   "maintained",
				},
			},
			Commentary: &wire.ContentReference{
				Ref:     "commentary:attempt-1",
				SHA256:  "abc123",
				ByteLen: 10,
			},
		}
		recordBytes, _ := json.Marshal(recordItem)
		recordHash := sha256.Sum256(recordBytes)
		recordHashStr := hex.EncodeToString(recordHash[:])

		reply := wire.ContentReply{
			Ref:    "record:verdict-1",
			Offset: 0,
			Data:   wire.EncodePage(recordBytes),
			Total:  int64(len(recordBytes)),
			SHA256: recordHashStr,
		}
		replyBody, _ := wire.NewReply(frame.ID, reply)
		_ = w.Write(replyBody)
		return false
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Create an oversized item with correct hash
	recordItem := wire.ReviewItem{
		Round:         0,
		CandidateHash: "hash123",
		Participant:   "reviewer-1",
		AttemptID:     "attempt-1",
		Kind:          "approve",
		Blocks: []wire.ReviewBlock{
			{
				BlockID: "block-1",
				State:   "maintained",
			},
		},
		Commentary: &wire.ContentReference{
			Ref:     "commentary:attempt-1",
			SHA256:  "abc123",
			ByteLen: 10,
		},
	}
	recordBytes, _ := json.Marshal(recordItem)
	recordHash := sha256.Sum256(recordBytes)

	oversizedItem := wire.ReviewItem{
		Round:         0,
		CandidateHash: "hash123",
		Participant:   "reviewer-1",
		AttemptID:     "attempt-1",
		Kind:          "approve",
		Oversized:     true,
		Record: &wire.ContentReference{
			Ref:     "record:verdict-1",
			SHA256:  hex.EncodeToString(recordHash[:]),
			ByteLen: int64(len(recordBytes)),
		},
	}

	// Fetch the oversized record
	result, err := client.FetchOversizedRecord(ctx, &oversizedItem)
	if err != nil {
		t.Fatalf("FetchOversizedRecord failed: %v", err)
	}

	if result.Oversized {
		t.Fatalf("result should not be oversized after fetching")
	}
	if result.Participant != "reviewer-1" {
		t.Fatalf("expected participant reviewer-1, got %s", result.Participant)
	}
}

func TestFetchReviewContent(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}

		// Return test content
		content := []byte("test commentary")
		contentHash := sha256.Sum256(content)
		contentHashStr := hex.EncodeToString(contentHash[:])

		reply := wire.ContentReply{
			Ref:    "commentary:attempt-1",
			Offset: 0,
			Data:   wire.EncodePage(content),
			Total:  int64(len(content)),
			SHA256: contentHashStr,
		}
		replyBody, _ := wire.NewReply(frame.ID, reply)
		_ = w.Write(replyBody)
		return false
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Create a content reference
	content := []byte("test commentary")
	contentHash := sha256.Sum256(content)
	ref := &wire.ContentReference{
		Ref:     "commentary:attempt-1",
		SHA256:  hex.EncodeToString(contentHash[:]),
		ByteLen: int64(len(content)),
	}

	// Fetch the content
	fetched, err := client.FetchReviewContent(ctx, ref)
	if err != nil {
		t.Fatalf("FetchReviewContent failed: %v", err)
	}

	if string(fetched) != string(content) {
		t.Fatalf("content mismatch: expected %q, got %q", content, fetched)
	}
}

func TestFetchReviewContentMismatchedHash(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}

		// Return content with mismatched hash
		content := []byte("wrong content")
		contentHash := sha256.Sum256(content)
		contentHashStr := hex.EncodeToString(contentHash[:])

		reply := wire.ContentReply{
			Ref:    "commentary:attempt-1",
			Offset: 0,
			Data:   wire.EncodePage(content),
			Total:  int64(len(content)),
			SHA256: contentHashStr,
		}
		replyBody, _ := wire.NewReply(frame.ID, reply)
		_ = w.Write(replyBody)
		return false
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Create a content reference with wrong hash
	ref := &wire.ContentReference{
		Ref:     "commentary:attempt-1",
		SHA256:  "wrong_hash_value",
		ByteLen: 13,
	}

	// Fetch should fail due to hash mismatch
	_, err = client.FetchReviewContent(ctx, ref)
	if err == nil {
		t.Fatalf("expected hash mismatch error")
	}
	hash := sha256.Sum256([]byte("wrong content"))
	expectedErr := "content sha256 mismatch: expected wrong_hash_value, got " + hex.EncodeToString(hash[:])
	if err.Error() != expectedErr {
		t.Fatalf("unexpected error message: %v", err)
	}
}
