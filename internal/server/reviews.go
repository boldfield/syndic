package server

import (
	"encoding/json"
	"errors"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/wire"
)

// buildReviews answers a reviews request from the journal: the accepted
// verdicts of a turn, in round then roster order, as many as encode in one frame.
func buildReviews(store *journal.Store, id string, turnID string, cursor *string, limit *int) (*wire.ReviewsReply, error) {
	turn, err := store.GetTurn(turnID)
	if err != nil {
		return nil, err
	}
	roster, err := turnRoster(store, turn)
	if err != nil {
		return nil, err
	}

	cursorStr := ""
	if cursor != nil {
		cursorStr = *cursor
	}
	pageLimit := journal.ReviewMaxLimit
	if limit != nil {
		if *limit < 1 {
			return nil, &wire.Error{Code: wire.ErrorCodeBadRequest}
		}
		pageLimit = min(*limit, journal.ReviewMaxLimit)
	}

	// The journal packs items against a budget that ignores the reply
	// envelope; ask for more than one frame holds and trim to what encodes.
	items, next, err := store.ReviewsPage(turnID, roster, cursorStr, pageLimit, 2*wire.MaxFrameBytes)
	if err != nil {
		if errors.Is(err, journal.ErrBadReviewCursor) {
			return nil, &wire.Error{Code: wire.ErrorCodeBadRequest}
		}
		return nil, err
	}

	raw := make([]json.RawMessage, len(items))
	cursors := make([]string, len(items))
	for i, item := range items {
		if raw[i], err = json.Marshal(item); err != nil {
			return nil, err
		}
		cursors[i] = item.VerdictID
	}
	return fitReviewsPage(id, raw, cursors, next)
}

// turnRoster is the turn's required reviewers in preset order, as its run
// manifest recorded them. A turn whose manifest names no preset (none was
// recorded) falls back to the roster its review records carry, then to the
// order its reviewers first ran.
func turnRoster(store *journal.Store, turn *journal.TurnRow) ([]string, error) {
	if run, err := store.GetRun(turn.RunID); err == nil {
		if roster, ok := manifestReviewers(run.Manifest, turn.Preset, turn.QuickProfile); ok {
			return roster, nil
		}
	}
	roster, err := store.GetTurnRoster(turn.ID)
	if err != nil || len(roster) > 0 {
		return roster, err
	}
	attempts, err := store.ListAttempts(turn.ID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, a := range attempts {
		if a.Phase == "review" && !seen[a.Participant] {
			seen[a.Participant] = true
			roster = append(roster, a.Participant)
		}
	}
	return roster, nil
}

// manifestReviewers resolves the preset a turn ran under, as config.Roster
// does (explicit preset, else its quick profile's, else the default), and
// returns its required reviewers.
func manifestReviewers(manifest json.RawMessage, preset, quick string) ([]string, bool) {
	var m struct {
		Roster struct {
			DefaultPreset string `json:"default_preset"`
			Presets       map[string]struct {
				RequiredReviewers []string `json:"required_reviewers"`
			} `json:"presets"`
			Quick map[string]struct {
				Preset string `json:"preset"`
			} `json:"quick"`
		} `json:"roster"`
	}
	if json.Unmarshal(manifest, &m) != nil {
		return nil, false
	}
	name := preset
	if name == "" {
		name = m.Roster.Quick[quick].Preset
	}
	if name == "" {
		name = m.Roster.DefaultPreset
	}
	p, ok := m.Roster.Presets[name]
	return p.RequiredReviewers, ok
}

// fitReviewsPage keeps the longest prefix of items whose reply fits in
// one frame. A page that is cut resumes after the last kept item, through
// itemCursors[i]; one that is not keeps next. A first item that does not fit
// alone is errReplyTooLarge.
func fitReviewsPage(id string, items []json.RawMessage, itemCursors []string, next string) (*wire.ReviewsReply, error) {
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
