package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	defaultPrompt = "> "
	reasonPrompt  = "reason> " // while a confirmation waits for its answer
	yesNoPrompt   = "(y/n)> "  // while a question waits for y or n
)

// maxInputRows is the most lines the input shows; a longer input scrolls
// within them, so the active tab keeps its room.
const maxInputRows = 5

// newInput returns the input: plain, unstyled and multi-line. Enter is the
// App's (submit); Alt+Enter inserts a newline.
func newInput() textarea.Model {
	ti := textarea.New()
	ti.ShowLineNumbers = false
	ti.CharLimit = 0
	ti.MaxHeight = 0
	ti.FocusedStyle, ti.BlurredStyle = textarea.Style{}, textarea.Style{}
	ti.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter"))
	ti.SetHeight(maxInputRows)
	setInputPrompt(&ti, defaultPrompt, 80)
	ti.Focus()
	return ti
}

// setInputPrompt shows prompt on the input's first line and indents the lines
// below it to match, then fits the input to width.
func setInputPrompt(in *textarea.Model, prompt string, width int) {
	in.Prompt = prompt
	pad := strings.Repeat(" ", ansi.StringWidth(prompt))
	in.SetPromptFunc(ansi.StringWidth(prompt), func(line int) string {
		if line == 0 {
			return prompt
		}
		return pad
	})
	in.SetWidth(max(width, 1))
}

// inputLines is how many screen lines the input's text takes at its width,
// counting a line that wraps once per row.
func inputLines(in textarea.Model) int {
	w := max(in.Width(), 1)
	n := 0
	for _, l := range strings.Split(in.Value(), "\n") {
		n += ansi.StringWidth(l)/w + 1
	}
	return n
}

// normalizePaste keeps a paste's line breaks as typed: CRLF and lone CR become
// one newline each, rather than the two or none a terminal might deliver.
func normalizePaste(runes []rune) string {
	s := strings.ReplaceAll(string(runes), "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// renderInput returns the bottom region: a refusal line, then the input's
// first rows lines.
func renderInput(in textarea.Model, refusal string, width, rows int) string {
	clip := lipgloss.NewStyle().MaxWidth(max(width, 1))
	lines := strings.Split(in.View(), "\n")
	lines = lines[:min(max(rows, 1), len(lines))]
	for i, l := range lines {
		lines[i] = clip.Render(l)
	}
	return clip.Render(strings.ReplaceAll(refusal, "\n", " ")) + "\n" + strings.Join(lines, "\n")
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
