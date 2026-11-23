package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// RenderMarkdown renders Markdown text from safe text, with inline code, lists,
// emphasis, and fenced code blocks with language labels. Links are shown as text
// with the URL visible; no hyperlink escape sequence is emitted.
// When raw is true, only safe text escaping is applied; Markdown rendering is skipped.
// Wrapping and two-space prefixing are applied to all output lines.
func RenderMarkdown(text string, width int, raw bool) string {
	if width <= 0 {
		// Without width info, just apply safe text escaping.
		return ToSafeText([]byte(text))
	}

	if raw {
		// Raw mode: skip Markdown rendering, only apply safe text with prefix.
		return strings.Join(SafeTextWithPrefix(text, width), "\n")
	}

	// Render as Markdown. Use Glamour with no terminal color output but preserving structure.
	rendered, err := renderMarkdownWithGlamour(text, width)
	if err != nil {
		// Fallback to raw safe text if rendering fails.
		return strings.Join(SafeTextWithPrefix(text, width), "\n")
	}

	// Apply safe text handling to the rendered output and wrap it.
	return strings.Join(SafeTextWithPrefix(rendered, width), "\n")
}

// renderMarkdownWithGlamour uses Glamour to render Markdown to plain text.
// It outputs plain text suitable for terminal display.
func renderMarkdownWithGlamour(text string, width int) (string, error) {
	// Create a renderer that outputs plain text with no ANSI colors.
	// Set the terminal width for proper line wrapping.
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return "", err
	}

	// Render the Markdown text. Glamour will output a formatted plain-text version.
	out, err := r.Render(text)
	if err != nil {
		return "", err
	}

	// Remove any trailing newlines added by Glamour; we'll handle line breaks ourselves.
	return strings.TrimRight(out, "\n"), nil
}
