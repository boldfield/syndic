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
		return strings.Join(SafeTextWithPrefix(text, width), "\n")
	}

	if !hasMarkdownSyntax(text) {
		return strings.Join(SafeTextWithPrefix(text, width), "\n")
	}

	rendered, err := renderMarkdownPlainText(text, width)
	if err != nil {
		return strings.Join(SafeTextWithPrefix(text, width), "\n")
	}

	return strings.Join(SafeTextWithPrefix(rendered, width), "\n")
}

// hasMarkdownSyntax detects if text contains Markdown syntax that should be rendered.
// It detects: fenced code blocks, headers, lists, emphasis, inline code, links.
func hasMarkdownSyntax(text string) bool {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
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
			j := 1
			for j < len(trimmed) && trimmed[j] >= '0' && trimmed[j] <= '9' {
				j++
			}
			if j < len(trimmed) && j+1 < len(trimmed) {
				if (trimmed[j] == '.' || trimmed[j] == ')') && trimmed[j+1] == ' ' {
					return true
				}
			}
		}

		// Numbered lists at start of line (1. item)
		if i == 0 && len(trimmed) > 2 && trimmed[0] >= '0' && trimmed[0] <= '9' {
			j := 1
			for j < len(trimmed) && trimmed[j] >= '0' && trimmed[j] <= '9' {
				j++
			}
			if j < len(trimmed) && j+1 < len(trimmed) {
				if (trimmed[j] == '.' || trimmed[j] == ')') && trimmed[j+1] == ' ' {
					return true
				}
			}
		}
	}

	// Check for paired emphasis markers
	if strings.Contains(text, "*") || strings.Contains(text, "_") ||
		strings.Contains(text, "`") || strings.Contains(text, "[") {
		return true
	}

	return false
}

// renderMarkdownPlainText uses Glamour to render Markdown to plain text without ANSI codes.
// If the input contains code fences with language labels, skip Glamour rendering to preserve labels.
// Otherwise, post-process Glamour's output to remove formatting artifacts.
func renderMarkdownPlainText(text string, width int) (string, error) {
	// If input contains code fences with language labels, don't render through Glamour.
	// This preserves the language label which Glamour's notty renderer drops.
	if hasCodeFenceWithLabel(text) {
		// Return empty to signal that caller should use raw mode instead
		return text, nil
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("notty"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return "", err
	}

	out, err := r.Render(text)
	if err != nil {
		return "", err
	}

	out = strings.TrimRight(out, "\n")
	lines := strings.Split(out, "\n")

	// Post-process: strip Glamour's margins (leading spaces) and trailing padding
	var result []string
	for _, line := range lines {
		// Strip trailing whitespace
		line = strings.TrimRight(line, " ")
		// Glamour adds margin indentation; strip common leading spaces
		// but preserve structure for lists and code
		line = stripGlamourMargin(line)
		result = append(result, line)
	}

	// Remove leading blank lines
	for len(result) > 0 && strings.TrimSpace(result[0]) == "" {
		result = result[1:]
	}

	// Remove trailing blank lines
	for len(result) > 0 && strings.TrimSpace(result[len(result)-1]) == "" {
		result = result[:len(result)-1]
	}

	return strings.Join(result, "\n"), nil
}

// hasCodeFenceWithLabel checks if text contains code fences with language labels.
func hasCodeFenceWithLabel(text string) bool {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Check for ``` or ~~~ followed by a language identifier
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			rest := trimmed[3:]
			// If there's anything after the opening fence, it's a language label
			if rest != "" && rest != "`" && rest != "~" {
				return true
			}
		}
	}
	return false
}

// stripGlamourMargin removes Glamour's standard 2-space margin from a line,
// but preserves meaningful indentation for lists and code blocks.
func stripGlamourMargin(line string) string {
	if len(line) < 2 {
		return line
	}
	// Check if line starts with 2+ spaces
	if line[0] == ' ' && line[1] == ' ' {
		// Check what comes after the two spaces
		rest := line[2:]
		if rest == "" || rest[0] == ' ' {
			// It's either a blank or a list/code item with extra indent, don't strip
			return line
		}
		if rest[0] == '-' || rest[0] == '*' || rest[0] == '+' {
			// It's a bullet list item; keep one space of indent but remove margin
			return " " + rest
		}
		// Check for bullet character (•)
		if len(rest) > 0 && strings.HasPrefix(rest, "•") {
			return " " + rest
		}
		// Regular text with Glamour margin; strip the margin
		return rest
	}
	return line
}
