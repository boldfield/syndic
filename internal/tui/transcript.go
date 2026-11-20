package tui

import (
	"fmt"
	"strings"

	"github.com/boldfield/syndic/internal/wire"
)

// transcriptInput is everything the transcript tab needs beyond the State.
type transcriptInput struct {
	content *ContentCache
	items   []wire.TranscriptItem
	blocks  []wire.OpenBlock
}

// transcriptTurn picks the turn the transcript tab is about.
func transcriptTurn(s State) string {
	if s.CurrentTurn != "" {
		return s.CurrentTurn
	}
	best := ""
	for id, ti := range s.Turns {
		if best == "" {
			best = id
			continue
		}
		b := s.Turns[best]
		if ti.SubmittedAt.After(b.SubmittedAt) || (ti.SubmittedAt.Equal(b.SubmittedAt) && id > best) {
			best = id
		}
	}
	return best
}

// itemHeader formats the header line for a transcript item.
func itemHeader(item wire.TranscriptItem, s State) string {
	parts := []string{}

	// Determine if message or attempt and get info
	if strings.HasPrefix(item.Ref, "msg:") {
		msgID := strings.TrimPrefix(item.Ref, "msg:")
		if msg, ok := s.Messages[msgID]; ok {
			parts = append(parts, fmt.Sprintf("[%s]", msg.Phase))
			parts = append(parts, msg.Sender)
			return strings.Join(parts, " ")
		}
	}

	if strings.HasPrefix(item.Ref, "attempt:") {
		attID := strings.TrimPrefix(item.Ref, "attempt:")
		if att, ok := s.Attempts[attID]; ok {
			parts = append(parts, fmt.Sprintf("[%s]", att.Phase))
			parts = append(parts, att.Participant)

			// Add attempt status
			if att.Status != "" {
				parts = append(parts, att.Status)
			}

			// Add transport status if not ok
			if att.TransportStatus != "" && att.TransportStatus != "ok" {
				parts = append(parts, att.TransportStatus)
			}

			// Add scrub count
			if att.ScrubCount > 0 {
				parts = append(parts, fmt.Sprintf("scrub:%d", att.ScrubCount))
			}

			// Add estimated tokens with 'est.' label
			if att.EstimatedTokens > 0 {
				parts = append(parts, fmt.Sprintf("est. %d", att.EstimatedTokens))
			}

			// Add reported tokens
			if att.ReportedPrompt != nil && att.ReportedCompletion != nil {
				parts = append(parts, fmt.Sprintf("reported %d+%d", *att.ReportedPrompt, *att.ReportedCompletion))
			}

			// Add verdict classification for review attempts
			if att.Phase == "review" && att.VerdictClass != "" {
				parts = append(parts, att.VerdictClass)
			}

			// Add attempt ID suffix
			parts = append(parts, attID[:min(8, len(attID))])

			// Add repair marker if applicable
			if att.RepairOf != nil {
				parts = append(parts, "repair-of:"+(*att.RepairOf)[:min(8, len(*att.RepairOf))])
			}

			return strings.Join(parts, " ")
		}
	}

	return item.Ref
}

// bodyWithStatus returns the body and whether it's loading or has no output.
func bodyWithStatus(cc *ContentCache, ref string) (string, string) {
	item := cc.Get(ref)
	if item.Err != nil {
		return "error: " + firstLine(item.Err.Error()), "error"
	}
	if !item.Ready {
		return "loading…", "loading"
	}
	if item.Data == "" {
		return "(no output)", "no-output"
	}
	return strings.TrimRight(item.Data, "\n"), "ok"
}

// renderBodyLines splits a body into lines for display.
func renderBodyLines(body string) []string {
	return strings.Split(body, "\n")
}

// renderTranscriptItems renders all transcript items with their bodies.
// When a repair is found, it's rendered alongside the original if width permits.
func renderTranscriptItems(s State, cc *ContentCache, items []wire.TranscriptItem, blocks []wire.OpenBlock, w int) []string {
	var lines []string
	repairOfMap := make(map[string]string) // repairOf ID -> attempt ID of repair

	// Build a map of repairs for quick lookup
	for _, item := range items {
		if strings.HasPrefix(item.Ref, "attempt:") {
			attID := strings.TrimPrefix(item.Ref, "attempt:")
			if att, ok := s.Attempts[attID]; ok && att.RepairOf != nil {
				repairOfMap[attID] = *att.RepairOf
			}
		}
	}

	// Build a map of blocks by opening attempt ID for inline rendering
	blocksByAttemptID := make(map[string][]wire.OpenBlock)
	for _, block := range blocks {
		// Block IDs are structured; find which attempt opened them
		for _, item := range items {
			if strings.HasPrefix(item.Ref, "attempt:") {
				attID := strings.TrimPrefix(item.Ref, "attempt:")
				if att, ok := s.Attempts[attID]; ok && att.Phase == "review" && att.VerdictClass != "" {
					// This is likely where the block was opened; associate it
					blocksByAttemptID[attID] = append(blocksByAttemptID[attID], block)
				}
			}
		}
	}

	rendered := make(map[string]bool) // track which items have been rendered

	for _, item := range items {
		if rendered[item.Ref] {
			continue
		}

		// Header
		header := itemHeader(item, s)

		// Check for quarantine label
		if strings.HasPrefix(item.Ref, "attempt:") {
			attID := strings.TrimPrefix(item.Ref, "attempt:")
			if att, ok := s.Attempts[attID]; ok {
				// Check if this is a quarantined late result
				if att.Status == "quarantined" {
					header = header + " quarantined (late)"
				}
			}
		}

		lines = append(lines, header)
		rendered[item.Ref] = true

		// Check if this item has a repair
		if strings.HasPrefix(item.Ref, "attempt:") {
			attID := strings.TrimPrefix(item.Ref, "attempt:")
			repairAttID := findRepairForAttempt(items, s, attID)

			if repairAttID != "" {
				// Render original and repair side-by-side or stacked
				origBody, _ := bodyWithStatus(cc, item.Ref)
				origLines := renderBodyLines(origBody)

				repairRef := "attempt:" + repairAttID
				repairBody, _ := bodyWithStatus(cc, repairRef)
				repairLines := renderBodyLines(repairBody)

				minWidth := 40
				if w >= minWidth*2+2 { // at least 40 cells each plus separator
					// Side-by-side layout
					lines = append(lines, "original ║ repair")
					lines = append(lines, strings.Repeat("─", minWidth)+"╋"+strings.Repeat("─", w-minWidth-1))
					maxLines := len(origLines)
					if len(repairLines) > maxLines {
						maxLines = len(repairLines)
					}
					for i := 0; i < maxLines; i++ {
						origLine := ""
						if i < len(origLines) {
							origLine = origLines[i]
						}
						repairLine := ""
						if i < len(repairLines) {
							repairLine = repairLines[i]
						}
						origPad := origLine + strings.Repeat(" ", minWidth-len(origLine))
						lines = append(lines, origPad[:minWidth]+" ║ "+repairLine)
					}
				} else {
					// Stacked layout
					lines = append(lines, "original:")
					lines = append(lines, origLines...)
					lines = append(lines, "")
					lines = append(lines, "repair:")
					lines = append(lines, repairLines...)
				}

				rendered[repairRef] = true
			} else {
				// Not a repair, render body normally
				body, status := bodyWithStatus(cc, item.Ref)
				if status == "ok" {
					lines = append(lines, renderBodyLines(body)...)
				} else {
					lines = append(lines, body)
				}

				// Render any blocks that were opened by this attempt
				if blocks, ok := blocksByAttemptID[attID]; ok {
					for _, block := range blocks {
						lines = append(lines, fmt.Sprintf("  [block %s] %s: %s", block.ID[:min(8, len(block.ID))], block.Severity, block.State))
						if block.Evidence != "" {
							lines = append(lines, fmt.Sprintf("    Evidence: %s", block.Evidence))
						}
						if block.Remedy != "" {
							lines = append(lines, fmt.Sprintf("    Remedy: %s", block.Remedy))
						}
					}
				}
			}
		} else {
			// Message, render body normally
			body, status := bodyWithStatus(cc, item.Ref)
			if status == "ok" {
				lines = append(lines, renderBodyLines(body)...)
			} else {
				lines = append(lines, body)
			}
		}

		// Add blank line between items for readability
		lines = append(lines, "")
	}

	return lines
}

// findRepairForAttempt finds the repair attempt for the given attempt ID.
func findRepairForAttempt(items []wire.TranscriptItem, s State, attID string) string {
	for _, item := range items {
		if strings.HasPrefix(item.Ref, "attempt:") {
			repairAttID := strings.TrimPrefix(item.Ref, "attempt:")
			if att, ok := s.Attempts[repairAttID]; ok && att.RepairOf != nil && *att.RepairOf == attID {
				return repairAttID
			}
		}
	}
	return ""
}

// renderTranscript returns the transcript tab unclipped; the App scrolls it.
func renderTranscript(s State, w, h int, in transcriptInput) string {
	turnID := transcriptTurn(s)
	if turnID == "" {
		return "no turn"
	}

	ti, ok := s.Turns[turnID]
	if !ok {
		return "turn not found"
	}

	var lines []string

	// Add turn info at the top
	lines = append(lines, fmt.Sprintf("Turn: %s (state: %s)", ti.ID, ti.State))

	if ti.TerminalReason != nil {
		lines = append(lines, fmt.Sprintf("Terminal reason: %s", *ti.TerminalReason))
	}

	lines = append(lines, "")

	// Add all transcript items
	lines = append(lines, renderTranscriptItems(s, in.content, in.items, in.blocks, w)...)

	return strings.Join(lines, "\n")
}
