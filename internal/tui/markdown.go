package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// RenderMarkdown is a test helper that renders text with optional Markdown and no-color mode.
// It's kept for backward compatibility with existing tests.
func RenderMarkdown(text string, width int, raw bool) string {
	return renderAnswerMarkdown(text, width, raw, false)
}

// renderAnswerMarkdown renders answer text as Markdown when appropriate,
// applying safe text escaping and two-space prefixing to all output lines.
// When raw is true, only safe text escaping is applied; Markdown rendering is skipped.
// When noColor is true (e.g., plain terminal or TERM=dumb), Markdown rendering is disabled.
func renderAnswerMarkdown(text string, width int, raw bool, noColor bool) string {
	if width <= 0 {
		return ToSafeText([]byte(text))
	}

	if raw || noColor {
		// Raw or no-color mode: skip Markdown rendering, only apply safe text with prefix.
		return strings.Join(SafeTextWithPrefix(text, width), "\n")
	}

	// Only render if text contains Markdown syntax that benefits from rendering.
	if !hasMarkdownSyntax(text) {
		return strings.Join(SafeTextWithPrefix(text, width), "\n")
	}

	// Render as Markdown using a plain-text, no-color renderer.
	rendered, err := renderMarkdownPlainText(text, width)
	if err != nil {
		// Fallback to raw safe text if rendering fails.
		return strings.Join(SafeTextWithPrefix(text, width), "\n")
	}

	// Apply safe text handling to the rendered output with prefix.
	return strings.Join(SafeTextWithPrefix(rendered, width), "\n")
}

// hasMarkdownSyntax detects if text contains Markdown syntax that should be rendered.
// It detects: fenced code blocks, headers, lists, emphasis, inline code, links.
func hasMarkdownSyntax(text string) bool {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Fenced code blocks (``` or ~~~)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			return true
		}

		// Headers (# at start with space after)
		if strings.HasPrefix(trimmed, "#") && len(trimmed) > 1 && trimmed[1] == ' ' {
			return true
		}

		// Bullet lists (-, *, + at start with space after)
		if (strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ")) &&
			len(trimmed) > 2 {
			return true
		}

		// Numbered lists (digit(s) followed by . or ) and space at start of line)
		if len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' {
			i := 1
			for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
				i++
			}
			if i < len(trimmed) && i+1 < len(trimmed) {
				if (trimmed[i] == '.' || trimmed[i] == ')') && trimmed[i+1] == ' ' {
					return true
				}
			}
		}
	}

	// Inline Markdown patterns
	if strings.Contains(text, "*") || strings.Contains(text, "_") ||
		strings.Contains(text, "`") || strings.Contains(text, "[") {
		return true
	}

	return false
}

// renderMarkdownPlainText uses Glamour to render Markdown to plain text without ANSI codes.
// It uses the "notty" style to avoid emitting color escape sequences.
func renderMarkdownPlainText(text string, width int) (string, error) {
	// Use "notty" style which produces plain text without ANSI escape sequences
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("notty"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return "", err
	}

	// Render the Markdown text
	out, err := r.Render(text)
	if err != nil {
		return "", err
	}

	// Remove trailing newlines added by Glamour
	return strings.TrimRight(out, "\n"), nil
}
