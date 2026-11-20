package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/wire"
)

// renderParticipants returns the participants tab with optional detail view.
func renderParticipants(s State, width, height int, selectedID string) string {
	width = max(width, 1)

	var lines []string

	// Author line for the current turn
	var authorID string
	var authorFamily string
	if s.CurrentTurn != "" {
		if ti, ok := s.Turns[s.CurrentTurn]; ok && ti.Author != "" {
			if pi, ok := s.Participants[ti.Author]; ok {
				authorID = ti.Author
				authorFamily = modelFamily(pi.ReportedModel)
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

	// Build table rows
	tableLines := participantsTable(s, participants, authorID, authorFamily, width)
	lines = append(lines, tableLines...)

	// Detail view for selected participant
	if selectedID != "" {
		if p, ok := s.Participants[selectedID]; ok {
			lines = append(lines, "")
			lines = append(lines, "detail view:")
			detailLines := participantDetail(p, authorID, authorFamily, width)
			lines = append(lines, detailLines...)
		}
	}

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
			lines = append(lines, fmt.Sprintf("  %s: /release %s %s", h.Pool, h.Pool, h.AttemptID))
		}
	}

	result := strings.Join(lines, "\n")
	return result
}

// participantDetail renders the detail view for a single participant.
func participantDetail(p ParticipantInfo, authorID, authorFamily string, width int) []string {
	var lines []string

	lines = append(lines, fmt.Sprintf("  id: %s", p.ID))
	lines = append(lines, fmt.Sprintf("  state: %s", p.State))
	if p.Detail != "" {
		lines = append(lines, fmt.Sprintf("  detail: %s", p.Detail))
		if p.State == "unprobed" && p.Detail == "no_record" {
			lines = append(lines, "    hint: run syndic probe, then restart the engine")
		}
	}

	lines = append(lines, fmt.Sprintf("  pool: %s", p.Pool))
	lines = append(lines, fmt.Sprintf("  model: %s", p.ReportedModel))

	if p.Identity != "" {
		lines = append(lines, fmt.Sprintf("  identity: %s", p.Identity))
	} else {
		lines = append(lines, "  identity: unknown (name-matched)")
	}

	if len(p.EligibleRoles) > 0 {
		lines = append(lines, fmt.Sprintf("  eligible roles: %s", strings.Join(p.EligibleRoles, ", ")))
	}

	if p.ProbeObservedAt != nil {
		lines = append(lines, fmt.Sprintf("  probe observed_at: %s", p.ProbeObservedAt.Format("2006-01-02 15:04:05")))
		age := formatAgeAt(p.ProbeObservedAt.Format(time.RFC3339), time.Now)
		lines = append(lines, fmt.Sprintf("  probe age: %s", age))
	}

	if p.ContextBound != nil {
		source := p.ContextSource
		if source == "" {
			source = "unknown"
		}
		lines = append(lines, fmt.Sprintf("  context_bound: %d (%s)", *p.ContextBound, source))
	}

	if p.OutputBound != nil {
		source := "probe"
		lines = append(lines, fmt.Sprintf("  output_bound: %d (%s)", *p.OutputBound, source))
	}

	if p.BytesPerToken != nil {
		source := p.BytesPerTokenSource
		if source == "" {
			source = "probe"
		}
		lines = append(lines, fmt.Sprintf("  bytes_per_token: %.2f (%s)", *p.BytesPerToken, source))
	}

	if p.Estimate != "" && p.Estimate == "exceeded" {
		lines = append(lines, "  estimate: exceeded")
	}

	if p.EligibleRequiredReviewer != "" && p.EligibleRequiredReviewer == "unknown" {
		lines = append(lines, "  truncation: unknown")
	}

	sameFamilyMark := ""
	if p.ID != authorID && authorFamily == modelFamily(p.ReportedModel) && authorFamily != "" {
		sameFamilyMark = " [same-family]"
	}
	if sameFamilyMark != "" {
		lines = append(lines, fmt.Sprintf("  mark:%s", sameFamilyMark))
	}

	return lines
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
func participantsTable(s State, participants []string, authorID, authorFamily string, width int) []string {
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

		// same-family marker only applies to reviewers, not the author
		isSameFamily := id != authorID && authorFamily == modelFamily(p.ReportedModel) && authorFamily != ""
		row := formatTableRow(id, state, p.Pool, p.ReportedModel, isSameFamily, width)
		lines = append(lines, row)
	}

	return lines
}

// formatTableRow formats a single participant row for the table.
func formatTableRow(id, state, pool, reportedModel string, isSameFamily bool, width int) string {
	// Try to fit: id (16) + state (15) + pool (12) + model (rest)
	id = padRight(truncateStringRuneSafe(id, 15), 16)
	state = padRight(truncateStringRuneSafe(state, 14), 15)
	pool = padRight(truncateStringRuneSafe(pool, 11), 12)
	model := reportedModel
	if isSameFamily {
		model = model + " [same-family]"
	}

	row := id + state + pool + model
	// Truncate by runes, not bytes, to avoid splitting multi-byte characters
	if utf8.RuneCountInString(row) > width {
		runes := []rune(row)
		row = string(runes[:width-1]) + "…"
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
// For deterministic tests, use formatAgeAt with a custom now function.
func formatAge(heldSince string) string {
	return formatAgeAt(heldSince, time.Now)
}

// formatAgeAt formats a held_since timestamp as a human-readable age using a custom now function.
func formatAgeAt(heldSince string, now func() time.Time) string {
	t, err := time.Parse(time.RFC3339, heldSince)
	if err != nil {
		return "unknown"
	}
	elapsed := now().Sub(t)
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

// truncateStringRuneSafe truncates a string to the specified number of runes,
// avoiding splitting multi-byte characters.
func truncateStringRuneSafe(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// padRight pads a string to the right with spaces to reach the target byte length.
func padRight(s string, length int) string {
	if len(s) >= length {
		return s
	}
	return s + strings.Repeat(" ", length-len(s))
}
