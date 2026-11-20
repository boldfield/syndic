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

// renderTranscriptItems renders all transcript items with their bodies.
func renderTranscriptItems(s State, cc *ContentCache, items []wire.TranscriptItem) []string {
	var lines []string

	for _, item := range items {
		// Header
		header := itemHeader(item, s)
		lines = append(lines, header)

		// Body - fetch the content
		bodyText := body(cc, item.Ref)
		if bodyText == "" {
			lines = append(lines, "(no output)")
		} else {
			for _, line := range strings.Split(bodyText, "\n") {
				if line == "" && len(lines) > 0 && lines[len(lines)-1] == "" {
					continue // Avoid double blank lines
				}
				lines = append(lines, line)
			}
		}

		// Add blank line between items for readability
		lines = append(lines, "")
	}

	return lines
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
	lines = append(lines, renderTranscriptItems(s, in.content, in.items)...)

	return strings.Join(lines, "\n")
}
