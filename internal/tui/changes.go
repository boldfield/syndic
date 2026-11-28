package tui

import (
	"fmt"
	"strings"

	"github.com/boldfield/syndic/internal/wire"
)

// renderChanges renders the changes tab showing the change set state and per-path outcomes.
func renderChanges(cs *wire.ChangeSetRecord, width int, height int) string {
	if cs == nil {
		return "no change set available"
	}

	var lines []string

	// Show the change set state at the top
	lines = append(lines, fmt.Sprintf("Change Set State: %s", safeLine(cs.State)))
	lines = append(lines, fmt.Sprintf("Changes Hash: %s", safeLine(cs.ChangesHash[:min(16, len(cs.ChangesHash))])))
	lines = append(lines, fmt.Sprintf("Snapshot Hash: %s", safeLine(cs.SnapshotHash[:min(16, len(cs.SnapshotHash))])))
	lines = append(lines, "")

	// Show paths with their outcomes
	lines = append(lines, "Paths:")
	for _, p := range cs.Paths {
		pathInfo := fmt.Sprintf("  %s (%s): %s", safeLine(p.Path), safeLine(p.Op), safeLine(p.Outcome))
		if p.BaseSHA256 != "" {
			pathInfo += fmt.Sprintf(" [base: %s]", safeLine(p.BaseSHA256[:min(8, len(p.BaseSHA256))]))
		}
		if p.NewSHA256 != "" {
			pathInfo += fmt.Sprintf(" [new: %s]", safeLine(p.NewSHA256[:min(8, len(p.NewSHA256))]))
		}
		lines = append(lines, wrapText(pathInfo, width)...)
	}

	return strings.Join(lines, "\n")
}
