package tui

import (
	"fmt"
	"strings"

	"github.com/boldfield/syndic/internal/wire"
)

// renderChanges renders the changes tab showing the change set state, per-path outcomes, and diffs.
// The content map (if provided) can supply the actual file content for rendering diffs.
func renderChanges(cs *wire.ChangeSetRecord, width int, height int) string {
	if cs == nil {
		return "no change set available"
	}

	var lines []string

	// Show change set metadata at the top
	lines = append(lines, fmt.Sprintf("Change Set State: %s", safeLine(cs.State)))
	lines = append(lines, fmt.Sprintf("Hash: %s", safeLine(cs.ChangesHash[:min(16, len(cs.ChangesHash))])))
	if cs.SnapshotHash != "" {
		lines = append(lines, fmt.Sprintf("Snapshot: %s", safeLine(cs.SnapshotHash[:min(16, len(cs.SnapshotHash))])))
	}
	lines = append(lines, "")

	// Show paths with their outcomes
	for _, p := range cs.Paths {
		outcome := formatOutcome(p.Outcome)
		operation := p.Op
		if operation == "" {
			operation = "modify"
		}

		header := fmt.Sprintf("%s [%s] %s", p.Path, operation, outcome)
		lines = append(lines, header)

		// Show base and new hashes
		if p.BaseSHA256 != "" {
			lines = append(lines, fmt.Sprintf("  base: %s", p.BaseSHA256[:min(16, len(p.BaseSHA256))]))
		} else {
			lines = append(lines, "  base: (none)")
		}
		if p.NewSHA256 != "" {
			lines = append(lines, fmt.Sprintf("  new:  %s", p.NewSHA256[:min(16, len(p.NewSHA256))]))
		}

		// Add spacing between paths
		lines = append(lines, "")
	}

	// Clip to height
	if height > 0 && len(lines) > height {
		lines = lines[:height-1]
		lines = append(lines, fmt.Sprintf("… %d more paths", len(cs.Paths)-(len(lines)/4)))
	}

	return strings.Join(lines, "\n")
}

// formatOutcome renders a path outcome as a user-facing string.
func formatOutcome(outcome string) string {
	switch outcome {
	case "pending":
		return "pending"
	case "applied":
		return "✓ applied"
	case "conflict":
		return "✗ conflict"
	case "not applied":
		return "not applied"
	default:
		return outcome
	}
}
