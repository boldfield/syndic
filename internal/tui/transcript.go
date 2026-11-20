package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

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

func itemHeader(s State, turnID string, item wire.TranscriptItem) string {
	// Determine if this is a message or attempt
	if strings.HasPrefix(item.Ref, "msg:") {
		msgID := strings.TrimPrefix(item.Ref, "msg:")
		if msg, ok := s.Messages[msgID]; ok {
			return fmt.Sprintf("[%s] %s: %s", msg.Phase, msg.Sender, item.Ref)
		}
	}

	if strings.HasPrefix(item.Ref, "attempt:") {
		attID := strings.TrimPrefix(item.Ref, "attempt:")
		if att, ok := s.Attempts[attID]; ok {
			parts := []string{
				fmt.Sprintf("[%s]", att.Phase),
				att.Participant,
			}

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

			// Add estimated tokens
			if att.EstimatedTokens > 0 {
				parts = append(parts, fmt.Sprintf("est:%d", att.EstimatedTokens))
			}

			// Add reported tokens
			if att.ReportedPrompt != nil && att.ReportedCompletion != nil {
				parts = append(parts, fmt.Sprintf("reported:%d+%d", *att.ReportedPrompt, *att.ReportedCompletion))
			}

			// Add verdict classification for review attempts
			if att.Phase == "review" && att.VerdictClass != "" {
				parts = append(parts, att.VerdictClass)
			}

			// Add attempt ID
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

func renderTranscriptItems(s State, turnID string, cc *ContentCache) []string {
	var lines []string

	// Collect and sort all items for this turn
	type item struct {
		ref   string
		time  time.Time
		order int // 0 for messages, 1 for attempts, to maintain order
		id    string
	}

	var items []item

	// Add messages
	for _, msg := range s.Messages {
		if msg.TurnID == turnID {
			items = append(items, item{
				ref:   "msg:" + msg.ID,
				order: 0,
				id:    msg.ID,
			})
		}
	}

	// Add attempts
	for _, att := range s.Attempts {
		if att.TurnID == turnID {
			items = append(items, item{
				ref:   "attempt:" + att.ID,
				order: 1,
				time:  att.Deadline,
				id:    att.ID,
			})
		}
	}

	// Sort by order (messages first), then by ID as tiebreaker
	sort.Slice(items, func(i, j int) bool {
		if items[i].order != items[j].order {
			return items[i].order < items[j].order
		}
		return items[i].id < items[j].id
	})

	// Render each item
	for _, item := range items {
		// Get metadata via wire.TranscriptItem
		wireItem := wire.TranscriptItem{
			ID:  item.id,
			Ref: item.ref,
		}

		// Header
		header := itemHeader(s, turnID, wireItem)
		lines = append(lines, header)

		// Body - fetch the content
		bodyText := body(cc, item.ref)
		for _, line := range strings.Split(bodyText, "\n") {
			if line == "" && len(lines) > 0 && lines[len(lines)-1] == "" {
				continue // Avoid double blank lines
			}
			lines = append(lines, line)
		}

		// Add blank line between items for readability
		lines = append(lines, "")
	}

	return lines
}

// renderTranscript returns the transcript tab unclipped; the App scrolls it.
func renderTranscript(s State, width, height int) string {
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

	// Create a mock content cache for this turn
	cc := NewContentCache()

	// Add all transcript items
	lines = append(lines, renderTranscriptItems(s, turnID, cc)...)

	return strings.Join(lines, "\n")
}
