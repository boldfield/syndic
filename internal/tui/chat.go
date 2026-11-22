package tui

import (
	"sort"
	"strings"

	"github.com/boldfield/syndic/internal/wire"
)

// chatInput is everything the chat tab needs beyond the State.
type chatInput struct {
	content         *ContentCache
	draftsMap       map[string]bool
	plainMode       bool
	blockDetail     map[string]wire.OpenBlock
	candidateIDMap  map[string]string
	overrideReasons map[string]string
	width           int
}

// chatRefs lists all content refs the chat tab needs across all turns.
func chatRefs(s State, candidateIDMap map[string]string, draftsMap map[string]bool) []string {
	var refs []string
	seen := map[string]bool{}

	// Collect turns in conversation scope, ordered by submission.
	type turnEntry struct {
		id string
		ti TurnInfo
	}
	var turns []turnEntry
	for id, ti := range s.Turns {
		if !s.inScope(id) {
			continue
		}
		turns = append(turns, turnEntry{id, ti})
	}
	sort.Slice(turns, func(i, j int) bool {
		if turns[i].ti.SubmitSeq != turns[j].ti.SubmitSeq {
			return turns[i].ti.SubmitSeq < turns[j].ti.SubmitSeq
		}
		if !turns[i].ti.SubmittedAt.Equal(turns[j].ti.SubmittedAt) {
			return turns[i].ti.SubmittedAt.Before(turns[j].ti.SubmittedAt)
		}
		return turns[i].id < turns[j].id
	})

	// For each turn, collect refs using the same logic as answerRefs
	for _, entry := range turns {
		ti := entry.ti
		turnID := entry.id

		switch ti.State {
		case "approved", "incomplete", "overridden", "cancelled":
			// Use candidate if available
			if ti.CandidateHash != nil && candidateIDMap[turnID] != "" {
				ref := "candidate:" + candidateIDMap[turnID]
				if !seen[ref] {
					refs = append(refs, ref)
					seen[ref] = true
				}
			}
		case "solo_complete":
			// Find the author attempt for solo mode
			if id := authorAttempt(s, turnID); id != "" {
				ref := "attempt:" + id
				if !seen[ref] {
					refs = append(refs, ref)
					seen[ref] = true
				}
			}
		default:
			// For turns in progress, include author attempt if drafts enabled for this turn
			if draftsMap[turnID] {
				if id := authorAttempt(s, turnID); id != "" {
					ref := "attempt:" + id
					if !seen[ref] {
						refs = append(refs, ref)
						seen[ref] = true
					}
				}
			}
		}
	}
	return refs
}

// renderChat returns the chat tab showing the conversation, oldest at top, unclipped.
func renderChat(s State, in chatInput, width int) string {
	var lines []string

	// Collect turns in conversation scope, ordered by submission.
	type turnEntry struct {
		id string
		ti TurnInfo
	}
	var turns []turnEntry
	for id, ti := range s.Turns {
		if !s.inScope(id) {
			continue
		}
		turns = append(turns, turnEntry{id, ti})
	}
	sort.Slice(turns, func(i, j int) bool {
		if turns[i].ti.SubmitSeq != turns[j].ti.SubmitSeq {
			return turns[i].ti.SubmitSeq < turns[j].ti.SubmitSeq
		}
		if !turns[i].ti.SubmittedAt.Equal(turns[j].ti.SubmittedAt) {
			return turns[i].ti.SubmittedAt.Before(turns[j].ti.SubmittedAt)
		}
		return turns[i].id < turns[j].id
	})

	if len(turns) == 0 {
		return ""
	}

	// Render each turn using renderAnswer for proper banner handling
	for _, entry := range turns {
		turnID := entry.id

		// Build answerInput for this turn
		ai := answerInput{
			content:        in.content,
			drafts:         in.draftsMap[turnID],
			candidateID:    in.candidateIDMap[turnID],
			openBlocks:     blockSliceFromMap(in.blockDetail),
			overrideReason: in.overrideReasons[turnID],
			plainMode:      in.plainMode,
		}

		// Render the turn using the existing renderAnswer logic
		turnLines := renderAnswer(s, turnID, ai)
		if turnLines != "" {
			lines = append(lines, turnLines)
		}

		// Add separator between turns
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// blockSliceFromMap converts a map to a slice of OpenBlocks.
func blockSliceFromMap(blocks map[string]wire.OpenBlock) []wire.OpenBlock {
	var slice []wire.OpenBlock
	for _, b := range blocks {
		slice = append(slice, b)
	}
	return slice
}

// countRevisions counts how many revisions were made for a turn.
// This is determined by counting distinct epoch values in the attempts for this turn.
func countRevisions(s State, turnID string) int {
	rounds := map[int64]bool{}
	for _, ai := range s.Attempts {
		if ai.TurnID == turnID && ai.Phase == "author" && ai.Status == "complete" && ai.Epoch > 0 {
			rounds[ai.Epoch] = true
		}
	}
	// If there's only 1 or 0 rounds, there were 0 revisions (first attempt doesn't count as a revision).
	if len(rounds) <= 1 {
		return 0
	}
	return len(rounds) - 1
}
