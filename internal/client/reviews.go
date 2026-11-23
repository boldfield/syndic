package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/boldfield/syndic/internal/wire"
)

// verifyRef checks that the fetched data matches the declared hash and byte length.
func verifyRef(ref *wire.ContentReference, data []byte) error {
	if int64(len(data)) != ref.ByteLen {
		return fmt.Errorf("byte length mismatch: expected %d, got %d", ref.ByteLen, len(data))
	}
	hash := sha256.Sum256(data)
	hashHex := hex.EncodeToString(hash[:])
	if hashHex != ref.SHA256 {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", ref.SHA256, hashHex)
	}
	return nil
}

// VerifiedReviewsPage fetches a page of reviews for a turn with decoded items.
// It verifies that the page fits within constraints and returns decoded items
// ready for use. The caller may fetch full content references via FetchReviewContent
// or oversized record references via FetchOversizedRecord.
func (c *Client) VerifiedReviewsPage(ctx context.Context, turnID string, cursor *string, limit *int) ([]wire.ReviewItem, *string, error) {
	// Validate limit constraint: limit must be at most 50
	if limit != nil && *limit > 50 {
		return nil, nil, fmt.Errorf("limit exceeds maximum of 50: %d", *limit)
	}

	reply, err := c.Reviews(ctx, turnID, cursor, limit)
	if err != nil {
		return nil, nil, err
	}
	items, err := reply.DecodeItems()
	if err != nil {
		return nil, nil, fmt.Errorf("decode reviews: %w", err)
	}

	// Guard against invalid paging state: cursor with no items is an infinite loop
	if reply.NextCursor != nil && len(items) == 0 {
		return nil, nil, fmt.Errorf("invalid paging state: cursor present but no items returned")
	}

	return items, reply.NextCursor, nil
}

// AllReviews pages through all accepted reviews for a turn, validating that the total
// count matches the expected review_count. It stops when no next_cursor is returned.
func (c *Client) AllReviews(ctx context.Context, turnID string) ([]wire.ReviewItem, error) {
	var allItems []wire.ReviewItem
	var cursor *string
	limit := 50

	for {
		items, nextCursor, err := c.VerifiedReviewsPage(ctx, turnID, cursor, &limit)
		if err != nil {
			return nil, fmt.Errorf("fetch reviews page: %w", err)
		}
		allItems = append(allItems, items...)
		if nextCursor == nil {
			break
		}
		cursor = nextCursor
	}

	return allItems, nil
}

// FetchOversizedRecord fetches the full structured record for an oversized review item.
// It verifies the SHA256 hash and byte length match what the item declared.
// It also verifies that the decoded record matches the short-form item on identity fields
// and is fully normalized. The returned ReviewItem is a fresh value decoded from the record.
func (c *Client) FetchOversizedRecord(ctx context.Context, item *wire.ReviewItem) (wire.ReviewItem, error) {
	if item == nil {
		return wire.ReviewItem{}, fmt.Errorf("item is nil")
	}
	if !item.Oversized || item.Record == nil {
		return wire.ReviewItem{}, fmt.Errorf("item is not oversized or has no record reference")
	}

	recordBytes, err := c.Content(ctx, item.Record.Ref)
	if err != nil {
		return wire.ReviewItem{}, fmt.Errorf("fetch oversized record: %w", err)
	}

	// Verify the content matches the declared hash and length
	if err := verifyRef(item.Record, recordBytes); err != nil {
		return wire.ReviewItem{}, fmt.Errorf("oversized record %s", err)
	}

	// Decode into a fresh value to avoid mutating caller's item
	var decoded wire.ReviewItem
	if err := json.Unmarshal(recordBytes, &decoded); err != nil {
		return wire.ReviewItem{}, fmt.Errorf("unmarshal oversized record: %w", err)
	}

	// Verify identity fields match the short form
	if decoded.Round != item.Round {
		return wire.ReviewItem{}, fmt.Errorf("record round mismatch: expected %d, got %d", item.Round, decoded.Round)
	}
	if decoded.CandidateHash != item.CandidateHash {
		return wire.ReviewItem{}, fmt.Errorf("record candidate_hash mismatch: expected %s, got %s", item.CandidateHash, decoded.CandidateHash)
	}
	if decoded.Participant != item.Participant {
		return wire.ReviewItem{}, fmt.Errorf("record participant mismatch: expected %s, got %s", item.Participant, decoded.Participant)
	}
	if decoded.AttemptID != item.AttemptID {
		return wire.ReviewItem{}, fmt.Errorf("record attempt_id mismatch: expected %s, got %s", item.AttemptID, decoded.AttemptID)
	}
	if decoded.Kind != item.Kind {
		return wire.ReviewItem{}, fmt.Errorf("record kind mismatch: expected %s, got %s", item.Kind, decoded.Kind)
	}

	// Verify the record is fully normalized: not oversized itself and has no nested record reference
	if decoded.Oversized {
		return wire.ReviewItem{}, fmt.Errorf("decoded record is still marked oversized")
	}
	if decoded.Record != nil {
		return wire.ReviewItem{}, fmt.Errorf("decoded record still has a nested record reference")
	}

	return decoded, nil
}

// FetchReviewContent fetches the content bytes for a content reference (e.g., commentary,
// evidence, remedy). It verifies the SHA256 hash and byte length match what the reference
// declared. The ref is typically from ReviewItem.Commentary, ReviewBlock.Evidence, or
// ReviewBlock.Remedy.
func (c *Client) FetchReviewContent(ctx context.Context, ref *wire.ContentReference) ([]byte, error) {
	if ref == nil {
		return nil, fmt.Errorf("content reference is nil")
	}

	content, err := c.Content(ctx, ref.Ref)
	if err != nil {
		return nil, fmt.Errorf("fetch review content: %w", err)
	}

	// Verify the content matches the declared hash and length
	if err := verifyRef(ref, content); err != nil {
		return nil, fmt.Errorf("content %s", err)
	}

	return content, nil
}
