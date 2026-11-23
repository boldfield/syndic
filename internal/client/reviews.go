package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/boldfield/syndic/internal/wire"
)

// VerifiedReviewsPage fetches a page of reviews for a turn with decoded items.
// It verifies that the page fits within constraints and returns decoded items
// ready for use. The caller may fetch full content references via FetchReviewContent
// or oversized record references via FetchOversizedRecord.
func (c *Client) VerifiedReviewsPage(ctx context.Context, turnID string, cursor *string, limit *int) ([]wire.ReviewItem, *string, error) {
	reply, err := c.Reviews(ctx, turnID, cursor, limit)
	if err != nil {
		return nil, nil, err
	}
	items, err := reply.DecodeItems()
	if err != nil {
		return nil, nil, fmt.Errorf("decode reviews: %w", err)
	}
	return items, reply.NextCursor, nil
}

// FetchOversizedRecord fetches the full structured record for an oversized review item.
// It verifies the SHA256 hash and byte length match what the item declared.
// The returned ReviewRecord can be unmarshaled into a typed structure.
func (c *Client) FetchOversizedRecord(ctx context.Context, item *wire.ReviewItem) (wire.ReviewItem, error) {
	if !item.Oversized || item.Record == nil {
		return *item, fmt.Errorf("item is not oversized or has no record reference")
	}

	recordBytes, err := c.Content(ctx, item.Record.Ref)
	if err != nil {
		return wire.ReviewItem{}, fmt.Errorf("fetch oversized record: %w", err)
	}

	// Verify the content matches the declared hash and length
	if int64(len(recordBytes)) != item.Record.ByteLen {
		return wire.ReviewItem{}, fmt.Errorf("oversized record byte length mismatch: expected %d, got %d", item.Record.ByteLen, len(recordBytes))
	}
	hash := sha256.Sum256(recordBytes)
	hashHex := hex.EncodeToString(hash[:])
	if hashHex != item.Record.SHA256 {
		return wire.ReviewItem{}, fmt.Errorf("oversized record sha256 mismatch: expected %s, got %s", item.Record.SHA256, hashHex)
	}

	// Unmarshal the record into the item
	if err := json.Unmarshal(recordBytes, &item); err != nil {
		return wire.ReviewItem{}, fmt.Errorf("unmarshal oversized record: %w", err)
	}

	item.Oversized = false
	item.Record = nil
	return *item, nil
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
	if int64(len(content)) != ref.ByteLen {
		return nil, fmt.Errorf("content byte length mismatch: expected %d, got %d", ref.ByteLen, len(content))
	}
	hash := sha256.Sum256(content)
	hashHex := hex.EncodeToString(hash[:])
	if hashHex != ref.SHA256 {
		return nil, fmt.Errorf("content sha256 mismatch: expected %s, got %s", ref.SHA256, hashHex)
	}

	return content, nil
}
