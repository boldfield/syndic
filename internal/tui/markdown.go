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

	// Only apply Markdown rendering if the text contains actual Markdown elements.
	// Otherwise, preserve the plain text structure.
	if hasMarkdownSyntax(text) {
		// Render as Markdown. Use Glamour with no terminal color output but preserving structure.
		rendered, err := renderMarkdownWithGlamour(text, width)
		if err != nil {
			// Fallback to raw safe text if rendering fails.
			return strings.Join(SafeTextWithPrefix(text, width), "\n")
		}
		// Apply safe text handling to the rendered output and wrap it.
		return strings.Join(SafeTextWithPrefix(rendered, width), "\n")
	}

	// No Markdown syntax detected, use plain safe text with prefix to preserve structure.
	return strings.Join(SafeTextWithPrefix(text, width), "\n")
}

// hasMarkdownSyntax detects if text contains Markdown syntax that should be rendered.
func hasMarkdownSyntax(text string) bool {
	// Check for common Markdown patterns:
	// - Fenced code blocks (``` or ~~~)
	// - Headers (#, ##, etc.)
	// - Lists (- , * , + at line start, or numbered lists)
	// - Emphasis (*text* or _text_ or **text** or __text__)
	// - Inline code (`text`)
	// - Links [text](url)
	// - Other block elements

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for fenced code blocks
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			return true
		}

		// Check for headers
		if strings.HasPrefix(trimmed, "#") && len(trimmed) > 1 && trimmed[1] == ' ' {
			return true
		}

		// Check for bullet lists
		if (strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ")) &&
			len(trimmed) > 2 {
			return true
		}

		// Check for numbered lists (digit followed by . or ))
		for i, r := range trimmed {
			if i > 0 && i < len(trimmed)-1 {
				if r >= '0' && r <= '9' {
					next := trimmed[i+1]
					if (next == '.' || next == ')') && i+2 < len(trimmed) && trimmed[i+2] == ' ' {
						return true
					}
				}
			}
		}
	}

	// Check for inline Markdown patterns
	// These need to match non-whitespace context to avoid false positives
	if strings.Contains(text, "**") || strings.Contains(text, "__") ||
		strings.Contains(text, "`") || strings.Contains(text, "[") {
		// Likely contains emphasis, code, or links
		return true
	}

	return false
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
