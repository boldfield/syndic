package tui

import (
	"fmt"
	"strings"
)

// renderAnswer returns the answer tab.
func renderAnswer(s State, width, height int, cc *ContentCache, draftsEnabled bool) string {
	if s.CurrentTurn == "" {
		return ""
	}

	ti, ok := s.Turns[s.CurrentTurn]
	if !ok {
		return ""
	}

	switch ti.State {
	case "submitted", "author_dispatched", "author_failed", "candidate_frozen", "reviewers_dispatched":
		// Non-terminal states: show hint if drafts off, or draft if drafts on
		if draftsEnabled && ti.Author != "" {
			return renderDraft(s, ti, cc, width, height)
		}
		return "turn in progress"

	case "approved":
		return renderApproved(s, ti, cc, width, height)

	case "solo_complete":
		return renderSoloComplete(s, ti, cc, width, height)

	case "incomplete":
		return renderIncomplete(s, ti, cc, width, height)

	case "overridden":
		return renderOverridden(s, ti, cc, width, height)

	case "cancelled":
		return renderCancelled(s, ti, cc, width, height)

	default:
		return ""
	}
}

func renderApproved(s State, ti TurnInfo, cc *ContentCache, width, height int) string {
	var lines []string

	// Fetch candidate if needed
	if ti.CandidateHash != nil {
		ref := "candidate:" + s.CurrentTurn
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

	// Add approval line with reviewer ids and hash prefix
	if len(ti.Reviewers) > 0 && ti.CandidateHash != nil {
		hashPrefix := (*ti.CandidateHash)[:12]
		lines = append(lines, fmt.Sprintf("approved by %s against %s", strings.Join(ti.Reviewers, ", "), hashPrefix))
	}

	return strings.Join(lines, "\n")
}

func renderSoloComplete(s State, ti TurnInfo, cc *ContentCache, width, height int) string {
	var lines []string
	lines = append(lines, "UNREVIEWED (solo)")

	// Fetch candidate if available
	if ti.CandidateHash != nil {
		ref := "candidate:" + s.CurrentTurn
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

func renderIncomplete(s State, ti TurnInfo, cc *ContentCache, width, height int) string {
	var lines []string

	// Add terminal reason
	if ti.TerminalReason != nil {
		lines = append(lines, *ti.TerminalReason)
	}

	// Add open blocks with severity, evidence and remedy
	for _, blockID := range getBlocksForTurn(s, s.CurrentTurn) {
		if bi, ok := s.Blocks[blockID]; ok && bi.State == "open" {
			lines = append(lines, fmt.Sprintf("[%s] %s", bi.Severity, blockID))
			// Evidence and remedy would come from the block's details if available
		}
	}

	// Add candidate if frozen with banner
	if ti.CandidateHash != nil {
		lines = append(lines, "INCOMPLETE, not approved")
		ref := "candidate:" + s.CurrentTurn
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

func renderOverridden(s State, ti TurnInfo, cc *ContentCache, width, height int) string {
	var lines []string

	if ti.TerminalReason != nil {
		lines = append(lines, fmt.Sprintf("HUMAN OVERRIDE: %s", *ti.TerminalReason))
	} else {
		lines = append(lines, "HUMAN OVERRIDE")
	}

	// Add candidate
	if ti.CandidateHash != nil {
		ref := "candidate:" + s.CurrentTurn
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

func renderCancelled(s State, ti TurnInfo, cc *ContentCache, width, height int) string {
	var lines []string

	// Check if cancelled before candidate
	if ti.CandidateHash == nil || ti.PhaseAtCancel != nil && *ti.PhaseAtCancel == "author" {
		lines = append(lines, "cancelled before candidate")
	} else {
		lines = append(lines, "CANCELLED, not approved")

		// Add any gathered blocks
		for _, blockID := range getBlocksForTurn(s, s.CurrentTurn) {
			if bi, ok := s.Blocks[blockID]; ok {
				lines = append(lines, fmt.Sprintf("[%s] %s", bi.Severity, blockID))
			}
		}

		// Add candidate
		ref := "candidate:" + s.CurrentTurn
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

func renderDraft(s State, ti TurnInfo, cc *ContentCache, width, height int) string {
	var lines []string
	lines = append(lines, "UNREVIEWED DRAFT")

	// Fetch author's draft (attempt output)
	if len(ti.Reviewers) == 0 {
		// Solo: show author attempt
		for attemptID, ai := range s.Attempts {
			if ai.TurnID == s.CurrentTurn && ai.Phase == "author" && ai.Status == "in_progress" {
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
	} else {
		// Review: show author attempt
		for attemptID, ai := range s.Attempts {
			if ai.TurnID == s.CurrentTurn && ai.Phase == "author" && ai.Status == "in_progress" {
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
	}

	return strings.Join(lines, "\n")
}

// getBlocksForTurn returns all block IDs associated with the given turn.
func getBlocksForTurn(s State, turnID string) []string {
	var blockIDs []string
	for _, verdict := range s.Verdicts {
		// Blocks are stored with verdict-block-id format
		// Need to find attempts for this turn and their verdicts
		if attempt, ok := s.Attempts[verdict.AttemptID]; ok && attempt.TurnID == turnID {
			for blockID := range s.Blocks {
				// This is a simplified lookup; actual implementation depends on verdict/block linking
				blockIDs = append(blockIDs, blockID)
			}
		}
	}
	return blockIDs
}
