package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/boldfield/syndic/internal/wire"
)

// renderParticipants returns the participants tab.
func renderParticipants(s State, width, height int) string {
	width = max(width, 1)
	height = max(height, 1)

	var lines []string

	// Author line for the current turn
	if s.CurrentTurn != "" {
		if ti, ok := s.Turns[s.CurrentTurn]; ok && ti.Author != "" {
			if pi, ok := s.Participants[ti.Author]; ok {
				lines = append(lines, fmt.Sprintf("author: %s (%s)", ti.Author, pi.ReportedModel))
				lines = append(lines, "")
			}
		}
	}

	// Participants table
	if len(s.Participants) == 0 {
		lines = append(lines, "no participants")
		return strings.Join(lines, "\n")
	}

	// Sort participants for consistent display
	var participants []string
	for id := range s.Participants {
		participants = append(participants, id)
	}
	sort.Strings(participants)

	// Extract author's model family for same-family comparison
	var authorFamily string
	if s.CurrentTurn != "" {
		if ti, ok := s.Turns[s.CurrentTurn]; ok && ti.Author != "" {
			if pi, ok := s.Participants[ti.Author]; ok {
				authorFamily = modelFamily(pi.ReportedModel)
			}
		}
	}

	// Build table rows
	tableLines := participantsTable(s, participants, authorFamily, width)
	lines = append(lines, tableLines...)

	// Pool information
	lines = append(lines, "")
	if len(s.Pools) == 0 {
		lines = append(lines, "no pools")
	} else {
		poolLines := poolsInfo(s, width)
		lines = append(lines, poolLines...)
	}

	// Orphaned holds
	if len(s.OrphanedHolds) > 0 {
		lines = append(lines, "")
		lines = append(lines, "orphaned holds:")
		for _, h := range s.OrphanedHolds {
			lines = append(lines, fmt.Sprintf("  attempt %s: /release %s %s", h.AttemptID, h.Pool, h.AttemptID))
		}
	}

	result := strings.Join(lines, "\n")
	return result
}

// modelFamily normalizes a model name for family matching. Two models are in
// the same family if their normalized forms are equal. The normalization
// lowercases the name and cuts it at the first `:`, `-`, `.` or digit.
func modelFamily(name string) string {
	if name == "" {
		return ""
	}
	name = strings.ToLower(name)
	for i, r := range name {
		if r == ':' || r == '-' || r == '.' || unicode.IsDigit(r) {
			return name[:i]
		}
	}
	return name
}

// participantsTable renders the participants table, dropping columns as needed
// to fit within the width limit. Each row is a participant; columns show state,
// pool, model, and other properties.
func participantsTable(s State, participants []string, authorFamily string, width int) []string {
	var lines []string

	// For 80 columns, we need to fit columns while keeping it readable
	// Core columns: id, state, pool, reported_model
	// Optional: identity, eligible_roles, probe_observed_at, context/output bounds, bytes_per_token

	// Header and rows
	header := "id" + strings.Repeat(" ", 15) + "state" + strings.Repeat(" ", 10) + "pool" + strings.Repeat(" ", 8) + "model"
	lines = append(lines, header)
	lines = append(lines, strings.Repeat("-", min(80, width)))

	for _, id := range participants {
		p := s.Participants[id]
		state := p.State
		if p.Detail != "" {
			state = state + ":" + p.Detail
		}

		row := formatTableRow(id, state, p.Pool, p.ReportedModel, authorFamily == modelFamily(p.ReportedModel) && authorFamily != "", width)
		lines = append(lines, row)
	}

	return lines
}

// formatTableRow formats a single participant row for the table.
func formatTableRow(id, state, pool, reportedModel string, isSameFamily bool, width int) string {
	// Try to fit: id (16) + state (15) + pool (12) + model (rest)
	id = padRight(truncateID(id, 15), 16)
	state = padRight(truncateString(state, 14), 15)
	pool = padRight(truncateString(pool, 11), 12)
	model := reportedModel
	if isSameFamily {
		model = model + " [same-family]"
	}

	row := id + state + pool + model
	if len(row) > width {
		row = row[:width-1] + "…"
	}
	return row
}

// poolsInfo renders information about pools and their holds.
func poolsInfo(s State, width int) []string {
	var lines []string
	lines = append(lines, "pools:")

	var poolNames []string
	for name := range s.Pools {
		poolNames = append(poolNames, name)
	}
	sort.Strings(poolNames)

	for _, name := range poolNames {
		pool := s.Pools[name]
		line := fmt.Sprintf("  %s (concurrency: %d)", name, pool.Concurrency)
		lines = append(lines, line)

		if len(pool.Holds) > 0 {
			for _, h := range pool.Holds {
				age := formatAge(h.HeldSince)
				members := formatGroupMembers(h.Group)
				holdLine := fmt.Sprintf("    hold: attempt %s, age %s, group: %s", h.AttemptID, age, members)
				lines = append(lines, holdLine)
			}
		}
	}

	return lines
}

// formatAge formats a held_since timestamp as a human-readable age.
func formatAge(heldSince string) string {
	t, err := time.Parse(time.RFC3339, heldSince)
	if err != nil {
		return "unknown"
	}
	elapsed := time.Since(t)
	if elapsed < time.Minute {
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	}
	if elapsed < time.Hour {
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	}
	return fmt.Sprintf("%dh", int(elapsed.Hours()))
}

// formatGroupMembers formats the group members as a compact string.
func formatGroupMembers(group []wire.GroupMember) string {
	if len(group) == 0 {
		return "(empty)"
	}
	if len(group) == 1 {
		return fmt.Sprintf("%s:%s", group[0].EndpointName, group[0].Model)
	}
	return fmt.Sprintf("%s:%s +%d", group[0].EndpointName, group[0].Model, len(group)-1)
}

// truncateID truncates an ID to the specified length.
func truncateID(id string, maxLen int) string {
	if len(id) <= maxLen {
		return id
	}
	return id[:maxLen]
}

// truncateString truncates a string to the specified length.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

// padRight pads a string to the right with spaces to reach the target length.
func padRight(s string, length int) string {
	if len(s) >= length {
		return s
	}
	return s + strings.Repeat(" ", length-len(s))
}
