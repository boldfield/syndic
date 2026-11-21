package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

const (
	defaultPrompt             = "> "
	reasonPrompt              = "reason> " // while a confirmation waits for its answer
	provisionalDeadlinePrompt = "(y/n)> "  // while provisional deadline confirmation waits
)

func newInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = defaultPrompt
	ti.CharLimit = 0
	ti.Focus()
	return ti
}

// renderInput returns the bottom region: a refusal line, then the input line.
func renderInput(in textinput.Model, refusal string, width int) string {
	clip := lipgloss.NewStyle().MaxWidth(max(width, 1))
	return clip.Render(strings.ReplaceAll(refusal, "\n", " ")) + "\n" + clip.Render(in.View())
}

// renderPanel wraps the lines of a panel to the width, so nothing a command
// shows is cut off at the edge.
func renderPanel(lines []string, width int) string {
	wrap := lipgloss.NewStyle().Width(max(width, 1))
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		for _, w := range strings.Split(wrap.Render(l), "\n") {
			out = append(out, strings.TrimRight(w, " "))
		}
	}
	return strings.Join(out, "\n")
}
