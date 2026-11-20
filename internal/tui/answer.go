package tui

import (
	"fmt"
	"strings"
)

// renderAnswer returns the answer tab.
func renderAnswer(s State, width, height int, cc *ContentCache, draftsEnabled bool, scrollRow int, candidateID string, turnID string) string {
	if turnID == "" {
		return ""
	}

	ti, ok := s.Turns[turnID]
	if !ok {
		return ""
	}

	switch ti.State {
	case "submitted", "author_dispatched":
		// Non-terminal states: show hint if drafts off, or draft if drafts on
		if draftsEnabled && ti.Author != "" {
			return renderDraft(s, ti, cc, width, height, turnID)
		}
		return "turn in progress"

	case "author_failed":
		return renderIncomplete(s, ti, cc, width, height, turnID, candidateID)

	case "candidate_frozen", "reviewers_dispatched":
		// Show draft if enabled, otherwise empty with hint
		if draftsEnabled {
			return renderDraft(s, ti, cc, width, height, turnID)
		}
		return "turn in progress"

	case "approved":
		return renderApproved(s, ti, cc, width, height, turnID, candidateID)

	case "solo_complete":
		return renderSoloComplete(s, ti, cc, width, height, turnID)

	case "incomplete":
		return renderIncomplete(s, ti, cc, width, height, turnID, candidateID)

	case "overridden":
		return renderOverridden(s, ti, cc, width, height, turnID, candidateID)

	case "cancelled":
		return renderCancelled(s, ti, cc, width, height, turnID, candidateID)

	default:
		return ""
	}
}

func renderApproved(s State, ti TurnInfo, cc *ContentCache, width, height int, turnID string, candidateID string) string {
	var lines []string

	// Fetch candidate if needed
	if ti.CandidateHash != nil && candidateID != "" {
		ref := "candidate:" + candidateID
		item := cc.Get(ref)
		if item.Ready {
			if item.Err != nil {
				lines = append(lines, fmt.Sprintf("error fetching candidate: %v", item.Err))
			} else {
				lines = append(lines, item.Data)
			}
		} else {
			lines = append(lines, "loading…")
		}
	}

	// Add approval line with hash prefix
	if ti.CandidateHash != nil {
		hashStr := *ti.CandidateHash
		if len(hashStr) > 12 {
			hashStr = hashStr[:12]
		}
		approvers := getApprovers(s, turnID)
		if len(approvers) > 0 {
			lines = append(lines, fmt.Sprintf("approved by %s against %s", strings.Join(approvers, ", "), hashStr))
		}
	}

	return strings.Join(lines, "\n")
}

// getApprovers returns reviewer IDs that approved in the given turn.
// Only returns reviewers with valid verdicts that actually approved.
func getApprovers(s State, turnID string) []string {
	var approvers []string
	seenReviewers := make(map[string]bool)

	// Look for verdicts with "valid" class from reviewers
	for _, v := range s.Verdicts {
		if attempt, ok := s.Attempts[v.AttemptID]; ok && attempt.TurnID == turnID && attempt.Phase == "review" {
			if v.Class == "valid" && !seenReviewers[attempt.Participant] {
				approvers = append(approvers, attempt.Participant)
				seenReviewers[attempt.Participant] = true
			}
		}
	}

	return approvers
}

func renderSoloComplete(s State, ti TurnInfo, cc *ContentCache, width, height int, turnID string) string {
	var lines []string
	lines = append(lines, "UNREVIEWED (solo)")

	// Find the author attempt and fetch its output
	for attemptID, ai := range s.Attempts {
		if ai.TurnID == turnID && ai.Phase == "author" && ai.Status == "complete" {
			ref := "attempt:" + attemptID
			item := cc.Get(ref)
			if item.Ready {
				if item.Err != nil {
					lines = append(lines, fmt.Sprintf("error: %v", item.Err))
				} else {
					lines = append(lines, item.Data)
				}
			} else {
				lines = append(lines, "loading…")
			}
			break
		}
	}

	return strings.Join(lines, "\n")
}

func renderIncomplete(s State, ti TurnInfo, cc *ContentCache, width, height int, turnID string, candidateID string) string {
	var lines []string

	// Add terminal reason
	if ti.TerminalReason != nil {
		lines = append(lines, *ti.TerminalReason)
	}

	// Add open blocks with severity
	for _, blockID := range getBlocksForTurn(s, turnID) {
		if bi, ok := s.Blocks[blockID]; ok && bi.State == "open" {
			lines = append(lines, fmt.Sprintf("[%s] %s", bi.Severity, blockID))
		}
	}

	// Add candidate if frozen with banner
	if ti.CandidateHash != nil && candidateID != "" {
		lines = append(lines, "INCOMPLETE, not approved")
		ref := "candidate:" + candidateID
		item := cc.Get(ref)
		if item.Ready {
			if item.Err != nil {
				lines = append(lines, fmt.Sprintf("error: %v", item.Err))
			} else {
				lines = append(lines, item.Data)
			}
		} else {
			lines = append(lines, "loading…")
		}
	}

	return strings.Join(lines, "\n")
}

func renderOverridden(s State, ti TurnInfo, cc *ContentCache, width, height int, turnID string, candidateID string) string {
	var lines []string

	if ti.TerminalReason != nil {
		lines = append(lines, fmt.Sprintf("HUMAN OVERRIDE: %s", *ti.TerminalReason))
	} else {
		lines = append(lines, "HUMAN OVERRIDE")
	}

	// Add candidate
	if ti.CandidateHash != nil && candidateID != "" {
		ref := "candidate:" + candidateID
		item := cc.Get(ref)
		if item.Ready {
			if item.Err != nil {
				lines = append(lines, fmt.Sprintf("error: %v", item.Err))
			} else {
				lines = append(lines, item.Data)
			}
		} else {
			lines = append(lines, "loading…")
		}
	}

	return strings.Join(lines, "\n")
}

func renderCancelled(s State, ti TurnInfo, cc *ContentCache, width, height int, turnID string, candidateID string) string {
	var lines []string

	// Check if cancelled before candidate was frozen
	if ti.CandidateHash == nil {
		lines = append(lines, "cancelled before candidate")
	} else {
		lines = append(lines, "CANCELLED, not approved")

		// Add any gathered blocks
		for _, blockID := range getBlocksForTurn(s, turnID) {
			if bi, ok := s.Blocks[blockID]; ok && bi.State == "open" {
				lines = append(lines, fmt.Sprintf("[%s] %s", bi.Severity, blockID))
			}
		}

		// Add candidate
		if candidateID != "" {
			ref := "candidate:" + candidateID
			item := cc.Get(ref)
			if item.Ready {
				if item.Err != nil {
					lines = append(lines, fmt.Sprintf("error: %v", item.Err))
				} else {
					lines = append(lines, item.Data)
				}
			} else {
				lines = append(lines, "loading…")
			}
		}
	}

	return strings.Join(lines, "\n")
}

func renderDraft(s State, ti TurnInfo, cc *ContentCache, width, height int, turnID string) string {
	var lines []string
	lines = append(lines, "UNREVIEWED DRAFT")

	// Fetch author's draft (attempt output)
	// Show if the author attempt exists and has completed or is in progress
	for attemptID, ai := range s.Attempts {
		if ai.TurnID == turnID && ai.Phase == "author" && (ai.Status == "in_progress" || ai.Status == "complete") {
			ref := "attempt:" + attemptID
			item := cc.Get(ref)
			if item.Ready {
				if item.Err != nil {
					lines = append(lines, fmt.Sprintf("error: %v", item.Err))
				} else {
					lines = append(lines, item.Data)
				}
			} else {
				lines = append(lines, "loading…")
			}
			break
		}
	}

	return strings.Join(lines, "\n")
}

// getBlocksForTurn returns all unique block IDs associated with the given turn.
func getBlocksForTurn(s State, turnID string) []string {
	seen := make(map[string]bool)
	var blockIDs []string

	// Find all attempts for this turn that have verdicts with blocks
	for _, verdict := range s.Verdicts {
		if attempt, ok := s.Attempts[verdict.AttemptID]; ok && attempt.TurnID == turnID {
			// Look for blocks that start with this verdict ID
			verdictID := verdict.ID
			for blockID := range s.Blocks {
				// Block IDs are formatted as "<verdict_id>:<reviewer_block_id>"
				if strings.HasPrefix(blockID, verdictID+":") && !seen[blockID] {
					seen[blockID] = true
					blockIDs = append(blockIDs, blockID)
				}
			}
		}
	}

	return blockIDs
}
