package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/boldfield/syndic/internal/wire"
)

// chatInput is everything the chat tab needs beyond the State.
type chatInput struct {
	content     *ContentCache
	drafts      bool
	plainMode   bool
	blockDetail map[string]wire.OpenBlock
	width       int
}

// chatRefs lists all content refs the chat tab needs across all turns.
func chatRefs(s State) []string {
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

	// For each turn, collect refs for its answer/candidate.
	for _, entry := range turns {
		ti := entry.ti
		switch ti.State {
		case "approved", "incomplete", "overridden", "cancelled":
			if ti.CandidateHash != nil && len(s.Attempts) > 0 {
				// Find the candidate ID for this turn.
				for id, ai := range s.Attempts {
					if ai.TurnID == entry.id && ai.Phase == "review" && ai.Status == "complete" {
						ref := "attempt:" + id
						if !seen[ref] {
							refs = append(refs, ref)
							seen[ref] = true
						}
						break
					}
				}
			}
		case "solo_complete":
			// Find the author attempt for solo mode.
			for id, ai := range s.Attempts {
				if ai.TurnID == entry.id && ai.Phase == "author" && ai.Status == "complete" {
					ref := "attempt:" + id
					if !seen[ref] {
						refs = append(refs, ref)
						seen[ref] = true
					}
					break
				}
			}
		default:
			// For turns in progress, check if drafts should be shown.
			for id, ai := range s.Attempts {
				if ai.TurnID == entry.id && ai.Phase == "author" && ai.Status == "complete" {
					ref := "attempt:" + id
					if !seen[ref] {
						refs = append(refs, ref)
						seen[ref] = true
					}
					break
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

	// Render each turn.
	for _, entry := range turns {
		turnID := entry.id
		turnLines := renderChatTurn(s, turnID, in, width)
		lines = append(lines, turnLines...)
		lines = append(lines, "") // Separator between turns.
	}

	return strings.Join(lines, "\n")
}

// renderChatTurn returns the lines for one turn in the chat tab.
func renderChatTurn(s State, turnID string, in chatInput, width int) []string {
	ti, ok := s.Turns[turnID]
	if !ok {
		return nil
	}

	var lines []string

	// Show user input (from the first user message in this turn's transcript).
	inputLines := getChatTurnInput(s, turnID)
	if len(inputLines) > 0 {
		lines = append(lines, inputLines...)
	}

	// Get open blocks and override reason for this turn.
	openBlocks := in.blockDetail
	overrideReason := ""

	// Show the turn's outcome based on its state.
	candidate := func() {
		if refs := answerRefs(s, turnID, answerInput{
			content:        in.content,
			drafts:         in.drafts,
			candidateID:    candidateIDForTurn(s, turnID),
			openBlocks:     blockSlice(openBlocks),
			overrideReason: overrideReason,
			plainMode:      in.plainMode,
		}); len(refs) > 0 {
			lines = append(lines, body(in.content, refs[0]))
		} else if ti.CandidateHash != nil {
			lines = append(lines, "loading…")
		}
	}

	switch ti.State {
	case "approved":
		candidate()
		if ti.CandidateHash != nil {
			hash := *ti.CandidateHash
			if len(hash) > 12 {
				hash = hash[:12]
			}
			// Use ti.Round if set, otherwise fall back to counting revisions from epochs.
			revisionCount := ti.Round
			if revisionCount == 0 {
				revisionCount = countRevisions(s, turnID)
			}
			if revisionCount > 0 {
				lines = append(lines, fmt.Sprintf("approved by %s after %d revisions against %s", strings.Join(approvers(s, turnID), ", "), revisionCount, hash))
			} else {
				lines = append(lines, fmt.Sprintf("approved by %s against %s", strings.Join(approvers(s, turnID), ", "), hash))
			}
		}

	case "solo_complete":
		lines = append(lines, "UNREVIEWED (solo)")
		candidate()

	case "incomplete":
		if ti.TerminalReason != nil {
			lines = append(lines, *ti.TerminalReason)
		}
		lines = append(lines, blockLines(blockSlice(openBlocks))...)
		if ti.CandidateHash != nil {
			lines = append(lines, "INCOMPLETE, not approved")
			candidate()
		} else {
			lines = append(lines, "no approved answer (incomplete)")
		}

	case "overridden":
		candidate()
		reason := overrideReason
		if reason == "" && ti.TerminalReason != nil && *ti.TerminalReason != "override" {
			reason = *ti.TerminalReason
		}
		lines = append(lines, "HUMAN OVERRIDE: "+reason)

	case "cancelled":
		lines = append(lines, "CANCELLED, not approved")
		if ti.CandidateHash != nil {
			candidate()
		}

	default:
		// Turn in progress - show draft banner if drafts enabled.
		if s.CurrentTurn == turnID {
			if in.drafts {
				if refs := answerRefs(s, turnID, answerInput{
					content:        in.content,
					drafts:         true,
					candidateID:    "",
					openBlocks:     blockSlice(openBlocks),
					overrideReason: "",
					plainMode:      in.plainMode,
				}); len(refs) > 0 {
					draftText := "UNREVIEWED DRAFT"
					if in.plainMode {
						draftText = "!! UNREVIEWED DRAFT !!"
					} else {
						draftText = draftBanner.Render(draftText)
					}
					lines = append(lines, draftText, body(in.content, refs[0]))
				}
			} else {
				lines = append(lines, "turn in progress")
			}
		}
	}

	return lines
}

// getChatTurnInput retrieves the user input for a turn from the transcript.
func getChatTurnInput(s State, turnID string) []string {
	// Look for user input in messages (this would typically come from transcript).
	// For now, return empty - this will be populated once transcript loading is working.
	return []string{}
}

// candidateIDForTurn returns the candidate ID for a turn, if available.
func candidateIDForTurn(s State, turnID string) string {
	for id, ai := range s.Attempts {
		if ai.TurnID == turnID && ai.Phase == "review" && ai.Status == "complete" {
			return id
		}
	}
	return ""
}

// blockSlice converts a map to a slice of OpenBlocks.
func blockSlice(blocks map[string]wire.OpenBlock) []wire.OpenBlock {
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
