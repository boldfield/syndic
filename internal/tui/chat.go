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

	// Helper to get the candidate content for this turn.
	getCandidate := func() string {
		// Find the latest completed attempt for this turn to display.
		// For approved/solo/terminal turns, find the review or author attempt.
		// For in-progress turns, find the author attempt if drafts are enabled.
		var bestAttempt string
		var bestEpoch int64 = -1

		switch ti.State {
		case "approved", "incomplete", "overridden", "cancelled":
			// For terminal states, use the latest attempt (review or author).
			for id, ai := range s.Attempts {
				if ai.TurnID != turnID || ai.Status != "complete" {
					continue
				}
				if ai.Epoch > bestEpoch || (ai.Epoch == bestEpoch && id > bestAttempt) {
					bestAttempt = id
					bestEpoch = ai.Epoch
				}
			}
		case "solo_complete":
			// For solo, use the author attempt.
			for id, ai := range s.Attempts {
				if ai.TurnID != turnID || ai.Phase != "author" || ai.Status != "complete" {
					continue
				}
				if ai.Epoch > bestEpoch || (ai.Epoch == bestEpoch && id > bestAttempt) {
					bestAttempt = id
					bestEpoch = ai.Epoch
				}
			}
		default:
			// For in-progress turns, use author attempt if drafts enabled.
			if in.drafts {
				for id, ai := range s.Attempts {
					if ai.TurnID != turnID || ai.Phase != "author" || ai.Status != "complete" {
						continue
					}
					if ai.Epoch > bestEpoch || (ai.Epoch == bestEpoch && id > bestAttempt) {
						bestAttempt = id
						bestEpoch = ai.Epoch
					}
				}
			}
		}

		if bestAttempt != "" {
			return body(in.content, "attempt:"+bestAttempt)
		} else if ti.CandidateHash != nil {
			return "loading…"
		}
		return ""
	}

	switch ti.State {
	case "approved":
		if content := getCandidate(); content != "" {
			lines = append(lines, content)
		}
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
		if content := getCandidate(); content != "" {
			lines = append(lines, content)
		}

	case "incomplete":
		if ti.TerminalReason != nil {
			lines = append(lines, *ti.TerminalReason)
		}
		lines = append(lines, blockLines(blockSlice(in.blockDetail))...)
		if ti.CandidateHash != nil {
			lines = append(lines, "INCOMPLETE, not approved")
			if content := getCandidate(); content != "" {
				lines = append(lines, content)
			}
		} else {
			lines = append(lines, "no approved answer (incomplete)")
		}

	case "overridden":
		if content := getCandidate(); content != "" {
			lines = append(lines, content)
		}
		reason := ""
		if ti.TerminalReason != nil && *ti.TerminalReason != "override" {
			reason = *ti.TerminalReason
		}
		lines = append(lines, "HUMAN OVERRIDE: "+reason)

	case "cancelled":
		lines = append(lines, "CANCELLED, not approved")
		if ti.CandidateHash != nil {
			if content := getCandidate(); content != "" {
				lines = append(lines, content)
			}
		}

	default:
		// Turn in progress - show draft banner if drafts enabled.
		if s.CurrentTurn == turnID && in.drafts {
			draftText := "UNREVIEWED DRAFT"
			if in.plainMode {
				draftText = "!! UNREVIEWED DRAFT !!"
			} else {
				draftText = draftBanner.Render(draftText)
			}
			lines = append(lines, draftText)
			if content := getCandidate(); content != "" {
				lines = append(lines, content)
			}
		}
	}

	return lines
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
