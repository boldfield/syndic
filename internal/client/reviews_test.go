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

func TestVerifiedReviewsPageLimitValidation(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		return true
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Test limit validation
	limit := 51
	_, _, err = client.VerifiedReviewsPage(ctx, "turn-1", nil, &limit)
	if err == nil {
		t.Fatalf("expected error for limit > 50")
	}
}

func TestVerifiedReviewsPageInvalidPagingState(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestReviews {
			return true
		}

		// Return cursor with no items - invalid paging state
		cursor := "next-cursor"
		reply := wire.ReviewsReply{
			Items:      []json.RawMessage{},
			NextCursor: &cursor,
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

	// Should fail due to invalid paging state
	_, _, err = client.VerifiedReviewsPage(ctx, "turn-1", nil, nil)
	if err == nil {
		t.Fatalf("expected error for cursor with no items")
	}
}

func TestAllReviewsMultiPage(t *testing.T) {
	callCount := 0
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestReviews {
			return true
		}

		callCount++
		var items []json.RawMessage
		var nextCursor *string

		switch callCount {
		case 1:
			// First page
			item := wire.ReviewItem{
				Round:         0,
				CandidateHash: "hash123",
				Participant:   "reviewer-1",
				AttemptID:     "attempt-1",
				Kind:          "approve",
				Blocks:        []wire.ReviewBlock{},
			}
			itemBytes, _ := json.Marshal(item)
			items = []json.RawMessage{itemBytes}
			cursor := "cursor-2"
			nextCursor = &cursor
		case 2:
			// Second page (last)
			item := wire.ReviewItem{
				Round:         1,
				CandidateHash: "hash123",
				Participant:   "reviewer-2",
				AttemptID:     "attempt-2",
				Kind:          "block",
				Blocks:        []wire.ReviewBlock{},
			}
			itemBytes, _ := json.Marshal(item)
			items = []json.RawMessage{itemBytes}
			nextCursor = nil
		}

		reply := wire.ReviewsReply{
			Items:      items,
			NextCursor: nextCursor,
		}
		replyBody, _ := wire.NewReply(frame.ID, reply)
		_ = w.Write(replyBody)

		// Return false to stop processing after the second request (when nextCursor is nil)
		return nextCursor != nil
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Test paging through all reviews
	items, err := client.AllReviews(ctx, "turn-1")
	if err != nil {
		t.Fatalf("AllReviews failed: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 total items, got %d", len(items))
	}
	if items[0].Participant != "reviewer-1" || items[1].Participant != "reviewer-2" {
		t.Fatalf("unexpected participants in results")
	}
}

func TestAllReviewsEmptyList(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestReviews {
			return true
		}

		reply := wire.ReviewsReply{
			Items:      []json.RawMessage{},
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

	// Test empty list (solo or all-failed turns)
	items, err := client.AllReviews(ctx, "turn-1")
	if err != nil {
		t.Fatalf("AllReviews failed: %v", err)
	}

	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
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

func TestFetchOversizedRecordNilItem(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		return true
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Test with nil item
	_, err = client.FetchOversizedRecord(ctx, nil)
	if err == nil {
		t.Fatalf("expected error for nil item")
	}
}

func TestFetchOversizedRecordHashMismatch(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}

		// Return content with mismatched hash
		recordBytes := []byte("wrong record content")
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

	oversizedItem := wire.ReviewItem{
		Round:         0,
		CandidateHash: "hash123",
		Participant:   "reviewer-1",
		AttemptID:     "attempt-1",
		Kind:          "approve",
		Oversized:     true,
		Record: &wire.ContentReference{
			Ref:     "record:verdict-1",
			SHA256:  "wrong_hash",
			ByteLen: 10,
		},
	}

	_, err = client.FetchOversizedRecord(ctx, &oversizedItem)
	if err == nil {
		t.Fatalf("expected error for hash mismatch")
	}
}

func TestFetchOversizedRecordByteLenMismatch(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}

		recordBytes := []byte("content")
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

	recordBytes := []byte("content")
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
			ByteLen: 100, // Wrong byte length
		},
	}

	_, err = client.FetchOversizedRecord(ctx, &oversizedItem)
	if err == nil {
		t.Fatalf("expected error for byte length mismatch")
	}
}

func TestFetchOversizedRecordIdentityMismatch(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}

		// Create a record with different participant
		recordItem := wire.ReviewItem{
			Round:         0,
			CandidateHash: "hash123",
			Participant:   "reviewer-different",
			AttemptID:     "attempt-1",
			Kind:          "approve",
			Blocks:        []wire.ReviewBlock{},
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

	recordItem := wire.ReviewItem{
		Round:         0,
		CandidateHash: "hash123",
		Participant:   "reviewer-different",
		AttemptID:     "attempt-1",
		Kind:          "approve",
		Blocks:        []wire.ReviewBlock{},
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

	_, err = client.FetchOversizedRecord(ctx, &oversizedItem)
	if err == nil {
		t.Fatalf("expected error for identity mismatch")
	}
}

func TestFetchOversizedRecordCallerNotMutatedOnFailure(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}

		// Return a record with wrong participant (will fail identity check)
		recordItem := wire.ReviewItem{
			Round:         0,
			CandidateHash: "hash123",
			Participant:   "reviewer-different",
			AttemptID:     "attempt-1",
			Kind:          "approve",
			Blocks:        []wire.ReviewBlock{},
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

	recordItem := wire.ReviewItem{
		Round:         0,
		CandidateHash: "hash123",
		Participant:   "reviewer-different",
		AttemptID:     "attempt-1",
		Kind:          "approve",
		Blocks:        []wire.ReviewBlock{},
	}
	recordBytes, _ := json.Marshal(recordItem)
	recordHash := sha256.Sum256(recordBytes)

	// Create an oversized item with specific values to check for mutation
	originalItem := wire.ReviewItem{
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
	testItem := originalItem

	// Attempt to fetch - should fail due to identity mismatch
	_, err = client.FetchOversizedRecord(ctx, &testItem)
	if err == nil {
		t.Fatalf("expected error for identity mismatch")
	}

	// Verify caller's item was not mutated
	if testItem.Oversized != originalItem.Oversized {
		t.Fatalf("caller's Oversized field was mutated: before %v, after %v", originalItem.Oversized, testItem.Oversized)
	}
	if testItem.Record == nil {
		t.Fatalf("caller's Record reference was cleared")
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
}

func TestFetchReviewContentByteLenMismatch(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}

		content := []byte("test content")
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

	content := []byte("test content")
	contentHash := sha256.Sum256(content)

	ref := &wire.ContentReference{
		Ref:     "commentary:attempt-1",
		SHA256:  hex.EncodeToString(contentHash[:]),
		ByteLen: 100, // Wrong byte length
	}

	_, err = client.FetchReviewContent(ctx, ref)
	if err == nil {
		t.Fatalf("expected byte length mismatch error")
	}
}

func TestFetchReviewContentCancellation(t *testing.T) {
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestContent {
			return true
		}

		// Simulate slow content response
		time.Sleep(100 * time.Millisecond)

		content := []byte("test content")
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

	// Use separate context for Dial; cancel only the fetch context
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	client, err := Dial(dialCtx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	content := []byte("test content")
	contentHash := sha256.Sum256(content)
	ref := &wire.ContentReference{
		Ref:     "commentary:attempt-1",
		SHA256:  hex.EncodeToString(contentHash[:]),
		ByteLen: int64(len(content)),
	}

	// Create a context that cancels quickly for the fetch
	fetchCtx, fetchCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer fetchCancel()

	// Should fail due to context cancellation
	_, err = client.FetchReviewContent(fetchCtx, ref)
	if err == nil {
		t.Fatalf("expected context cancellation error")
	}
	select {
	case <-fetchCtx.Done():
		// Context is done, as expected
	default:
		t.Fatalf("expected fetch context to be cancelled")
	}
}

func TestAllReviewsPagingCancellation(t *testing.T) {
	callCount := 0
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		if frame.Type != wire.RequestReviews {
			return true
		}

		callCount++
		var items []json.RawMessage
		var nextCursor *string

		switch callCount {
		case 1:
			// First page
			item := wire.ReviewItem{
				Round:         0,
				CandidateHash: "hash123",
				Participant:   "reviewer-1",
				AttemptID:     "attempt-1",
				Kind:          "approve",
				Blocks:        []wire.ReviewBlock{},
			}
			itemBytes, _ := json.Marshal(item)
			items = []json.RawMessage{itemBytes}
			cursor := "cursor-2"
			nextCursor = &cursor
		case 2:
			// Simulate slow second page response
			time.Sleep(100 * time.Millisecond)
			item := wire.ReviewItem{
				Round:         1,
				CandidateHash: "hash123",
				Participant:   "reviewer-2",
				AttemptID:     "attempt-2",
				Kind:          "block",
				Blocks:        []wire.ReviewBlock{},
			}
			itemBytes, _ := json.Marshal(item)
			items = []json.RawMessage{itemBytes}
			nextCursor = nil
		}

		reply := wire.ReviewsReply{
			Items:      items,
			NextCursor: nextCursor,
		}
		replyBody, _ := wire.NewReply(frame.ID, reply)
		_ = w.Write(replyBody)

		return nextCursor != nil
	}))
	defer server.Close()

	// Use separate context for Dial
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	client, err := Dial(dialCtx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Create a context that will be cancelled after the first page
	pagingCtx, pagingCancel := context.WithCancel(context.Background())
	defer pagingCancel()

	// Use a custom loop to allow cancellation between pages
	var cursor *string
	limit := 50

	// First page should succeed
	items, nextCursor, err := client.VerifiedReviewsPage(pagingCtx, "turn-1", cursor, &limit)
	if err != nil {
		t.Fatalf("first page failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item on first page, got %d", len(items))
	}
	if nextCursor == nil {
		t.Fatalf("expected next cursor on first page")
	}
	cursor = nextCursor

	// Cancel context before fetching second page
	pagingCancel()

	// Second page should fail due to cancellation
	_, _, err = client.VerifiedReviewsPage(pagingCtx, "turn-1", cursor, &limit)
	if err == nil {
		t.Fatalf("expected context cancellation error on second page")
	}
}
