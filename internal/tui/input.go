package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

const (
	defaultPrompt = "> "
	reasonPrompt  = "reason> " // while a confirmation waits for its answer
	yesNoPrompt   = "(y/n)> "  // while a question waits for y or n
)

// maxInputRows is the most lines the input shows; a longer input scrolls
// within them, so the active tab keeps its room. Per v6 spec, input is 1-6 lines.
const maxInputRows = 6

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
// counting each row a line wraps onto as the textarea draws it.
func inputLines(in textarea.Model) int {
	w := max(in.Width(), 1)
	n := 0
	for _, l := range strings.Split(in.Value(), "\n") {
		n += wrappedRows([]rune(l), w)
	}
	return n
}

// wrappedRows is how many rows the textarea draws one line of text on at
// width: it follows the textarea's own word wrap (bubbles textarea wrap),
// which breaks before a word that does not fit and keeps a trailing space
// on the last row, so a line that fills its width exactly takes one more.
func wrappedRows(runes []rune, width int) int {
	var (
		rows   = 1
		line   []rune // the row being filled
		word   []rune
		spaces int
	)
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}
		if spaces > 0 {
			if uniseg.StringWidth(string(line))+uniseg.StringWidth(string(word))+spaces > width {
				rows++
				line = nil
			}
			line = append(append(line, word...), []rune(strings.Repeat(" ", spaces))...)
			spaces, word = 0, nil
		} else if uniseg.StringWidth(string(word))+runewidth.RuneWidth(word[len(word)-1]) > width {
			// The word fills a row by itself: it moves to a row of its own.
			if len(line) > 0 {
				rows++
				line = nil
			}
			line = append(line, word...)
			word = nil
		}
	}
	if uniseg.StringWidth(string(line))+uniseg.StringWidth(string(word))+spaces >= width {
		rows++
	}
	return rows
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
		// Panels and prompts carry record and workspace values; every line is
		// one safe line, so a line feed in a value cannot open an unindented row.
		// Escaping is idempotent on its own output.
		for _, w := range strings.Split(wrap.Render(safeLine(l)), "\n") {
			out = append(out, strings.TrimRight(w, " "))
		}
	}
	return strings.Join(out, "\n")
}
