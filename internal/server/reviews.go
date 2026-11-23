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

	// Get roster order from engine status
	status, err := engine.Status()
	if err != nil {
		return nil, err
	}
	var roster []string
	for _, p := range status.Participants {
		roster = append(roster, p.ID)
	}

	// Parse cursor and limit
	cursorStr := ""
	if cursor != nil {
		cursorStr = *cursor
	}
	pageLimit := journal.ReviewMaxLimit
	if limit != nil && *limit > 0 && *limit < journal.ReviewMaxLimit {
		pageLimit = *limit
	}

	// Get the page of reviews, respecting the frame size limit
	items, nextCursor, err := store.ReviewsPage(turnID, roster, cursorStr, pageLimit, wire.MaxFrameBytes)
	if err != nil {
		if errors.Is(err, journal.ErrBadReviewCursor) {
			return nil, &wire.Error{Code: wire.ErrorCodeBadRequest}
		}
		return nil, err
	}

	// Build the reply
	reply := &wire.ReviewsReply{
		Items: make([]wire.ReviewItem, len(items)),
	}

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

		reply.Items[i] = reviewItem
	}

	if nextCursor != "" {
		reply.NextCursor = &nextCursor
	}

	// Verify the reply fits in one frame
	if !replyFits(id, reply) {
		return nil, errReplyTooLarge
	}

	return reply, nil
}
