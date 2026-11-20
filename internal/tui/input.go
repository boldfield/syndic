package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
)

func newInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.CharLimit = 0
	ti.Focus()
	return ti
}

// renderInput returns the bottom region: a refusal line, then the input line.
func renderInput(in textinput.Model, refusal string, width int) string {
	clip := lipgloss.NewStyle().MaxWidth(max(width, 1))
	return clip.Render(strings.ReplaceAll(refusal, "\n", " ")) + "\n" + clip.Render(in.View())
}
