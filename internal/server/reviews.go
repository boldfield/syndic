package server

import (
	"errors"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// buildReviews answers a reviews request from the journal.
// It retrieves paged accepted verdicts for a turn, handling oversized items.
func buildReviews(engine Engine, store *journal.Store, id string, turnID string, cursor *string, limit *int) (*wire.ReviewsReply, error) {
	// Verify the turn exists
	_, err := store.GetTurn(turnID)
	if err != nil {
		return nil, err
	}

	// Get roster order from the turn's recorded roster, not current engine preset.
	// First try to infer from existing review records, then fall back to engine status.
	roster, err := store.GetTurnRoster(turnID)
	if err != nil {
		return nil, err
	}
	if len(roster) == 0 {
		status, err := engine.Status()
		if err != nil {
			return nil, err
		}
		for _, p := range status.Participants {
			roster = append(roster, p.ID)
		}
	}

	// Parse cursor and limit
	cursorStr := ""
	if cursor != nil {
		cursorStr = *cursor
	}
	pageLimit := journal.ReviewMaxLimit
	if limit != nil {
		if *limit < 1 {
			return nil, &wire.Error{Code: wire.ErrorCodeBadRequest}
		}
		if *limit < journal.ReviewMaxLimit {
			pageLimit = *limit
		}
	}

	// Get a page of reviews. Pass a generous frameBytes to get as many items as fit
	// within the item limit, then trim to what fits in the encoded reply.
	items, nextCursor, err := store.ReviewsPage(turnID, roster, cursorStr, pageLimit, wire.MaxFrameBytes*2)
	if err != nil {
		if errors.Is(err, journal.ErrBadReviewCursor) {
			return nil, &wire.Error{Code: wire.ErrorCodeBadRequest}
		}
		return nil, err
	}

	// Build wire-formatted items
	wireItems := make([]wire.ReviewItem, len(items))
	for i, item := range items {
		reviewItem := wire.ReviewItem{
			Round:         item.Round,
			CandidateHash: item.CandidateHash,
			Participant:   item.Participant,
			AttemptID:     item.AttemptID,
			Kind:          item.Kind,
			Oversized:     item.Oversized,
		}

		if item.Oversized {
			// Short form: include the record reference
			if item.Record != nil {
				reviewItem.Record = &wire.ContentReference{
					Ref:     item.Record.Ref,
					SHA256:  item.Record.SHA256,
					ByteLen: item.Record.ByteLen,
				}
			}
		} else {
			// Full form: include blocks and commentary
			if len(item.Blocks) > 0 {
				reviewItem.Blocks = make([]wire.ReviewBlock, len(item.Blocks))
				for j, block := range item.Blocks {
					reviewBlock := wire.ReviewBlock{
						BlockID: block.BlockID,
						State:   block.State,
					}
					if block.Severity != "" {
						reviewBlock.Severity = &block.Severity
					}
					if block.Evidence != nil {
						reviewBlock.Evidence = &wire.ContentReference{
							Ref:     block.Evidence.Ref,
							SHA256:  block.Evidence.SHA256,
							ByteLen: block.Evidence.ByteLen,
						}
					}
					if block.Remedy != nil {
						reviewBlock.Remedy = &wire.ContentReference{
							Ref:     block.Remedy.Ref,
							SHA256:  block.Remedy.SHA256,
							ByteLen: block.Remedy.ByteLen,
						}
					}
					reviewItem.Blocks[j] = reviewBlock
				}
			}
			if item.Commentary.Ref != "" {
				reviewItem.Commentary = &wire.ContentReference{
					Ref:     item.Commentary.Ref,
					SHA256:  item.Commentary.SHA256,
					ByteLen: item.Commentary.ByteLen,
				}
			}
		}

		wireItems[i] = reviewItem
	}

	// Build item cursors for fitReviewsPage
	itemCursors := make([]string, len(items))
	for i, item := range items {
		itemCursors[i] = item.VerdictID
	}

	// Trim to what fits in the encoded reply
	return fitReviewsPage(id, wireItems, itemCursors, nextCursor)
}

// fitReviewsPage keeps the longest prefix of items whose reply fits in
// one frame. A page that is cut resumes after the last kept item, through
// itemCursors[i]; one that is not keeps next. A first item that does not fit
// alone is errReplyTooLarge.
func fitReviewsPage(id string, items []wire.ReviewItem, itemCursors []string, next string) (*wire.ReviewsReply, error) {
	reply := &wire.ReviewsReply{Items: items}
	if next != "" {
		reply.NextCursor = &next
	}
	if replyFits(id, reply) {
		return reply, nil
	}
	// Frame size grows with the item count, so the longest fitting prefix is found by bisection.
	lo, hi := 0, len(items)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		cursor := itemCursors[mid-1]
		if replyFits(id, &wire.ReviewsReply{Items: items[:mid], NextCursor: &cursor}) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 {
		return nil, errReplyTooLarge
	}
	cursor := itemCursors[lo-1]
	return &wire.ReviewsReply{Items: items[:lo], NextCursor: &cursor}, nil
}
