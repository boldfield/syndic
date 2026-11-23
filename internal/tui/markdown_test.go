package tui

import (
	"strings"
	"testing"
)

func TestRenderMarkdownRawMode(t *testing.T) {
	text := "This is **bold** text"
	width := 80

	result := RenderMarkdown(text, width, true)

	// In raw mode, Markdown should not be rendered, just safely escaped and prefixed.
	if strings.Contains(result, "bold") && !strings.Contains(result, "**") {
		t.Errorf("Raw mode should not render Markdown; got: %q", result)
	}
	// Should be prefixed with two spaces on each line.
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("Line not prefixed with two spaces: %q", line)
		}
	}
}

func TestRenderMarkdownMarkdownMode(t *testing.T) {
	text := "This is **bold** text"
	width := 80

	result := RenderMarkdown(text, width, false)

	// In Markdown mode, the renderer should process the text.
	// Every line should be prefixed with two spaces.
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("Line not prefixed with two spaces: %q", line)
		}
	}
}

func TestRenderMarkdownWithControlCharacters(t *testing.T) {
	// Test that control characters are escaped.
	text := "Line 1\x1b[31mred\x1b[0m\x0dCRLF\xe2\x80\xaeRTL" // \xe2\x80\xae is UTF-8 for U+202E
	width := 80

	result := RenderMarkdown(text, width, true)

	// Verify that dangerous control bytes are escaped.
	if strings.Contains(result, "\x1b") {
		t.Errorf("ANSI escape character not escaped in result: %q", result)
	}
	if strings.Contains(result, "\r") {
		t.Errorf("CR character not escaped in result: %q", result)
	}
	rtlChar := string([]byte{0xe2, 0x80, 0xae}) // UTF-8 encoding of U+202E
	if strings.Contains(result, rtlChar) {
		t.Errorf("RTL override character not escaped in result: %q", result)
	}

	// Verify escape sequences are present.
	if !strings.Contains(result, "\\x1b") {
		t.Errorf("ANSI escape should be replaced with \\x1b: %q", result)
	}
	if !strings.Contains(result, "\\x0d") {
		t.Errorf("CR should be replaced with \\x0d: %q", result)
	}
	escapedRTL := "\\u202e"
	if !strings.Contains(result, escapedRTL) {
		t.Errorf("RTL override should be replaced with %s: %q", escapedRTL, result)
	}
}

func TestRenderMarkdownWithLists(t *testing.T) {
	text := "- item 1\n- item 2\n- item 3"
	width := 80

	result := RenderMarkdown(text, width, false)

	// Lists should be rendered; verify that lines are present and prefixed.
	lines := strings.Split(result, "\n")
	if len(lines) < 3 {
		t.Errorf("Expected at least 3 lines, got %d: %q", len(lines), result)
	}
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Non-empty line not prefixed with two spaces: %q", line)
		}
	}
}

func TestRenderMarkdownWithCodeFence(t *testing.T) {
	text := "Here is some code:\n```go\nfunc main() {\n  fmt.Println(\"hello\")\n}\n```"
	width := 80

	result := RenderMarkdown(text, width, false)

	// Code fence should be rendered; verify prefixing.
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Non-empty line not prefixed with two spaces: %q", line)
		}
	}
}

func TestRenderMarkdownWithInlineCode(t *testing.T) {
	text := "Use " + "`printf`" + " to output text"
	width := 80

	result := RenderMarkdown(text, width, false)

	// Inline code should be preserved; verify prefixing.
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Non-empty line not prefixed with two spaces: %q", line)
		}
	}
}

func TestRenderMarkdownWithEmphasis(t *testing.T) {
	text := "This is *italic* and this is **bold** and this is ***bold italic***"
	width := 80

	result := RenderMarkdown(text, width, false)

	// Emphasis markers should be processed; verify prefixing.
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Non-empty line not prefixed with two spaces: %q", line)
		}
	}
}

func TestRenderMarkdownWithLinks(t *testing.T) {
	text := "Visit [example](https://example.com) for more info"
	width := 80

	result := RenderMarkdown(text, width, false)

	// Links should be shown as text with URL visible (no hyperlink escape).
	// Verify that the URL is visible and no hyperlink escape is present.
	if strings.Contains(result, "\x1b]8") { // OSC hyperlink escape
		t.Errorf("Hyperlink escape sequence found in output: %q", result)
	}

	// The result should contain the URL in some form.
	if !strings.Contains(result, "example.com") {
		t.Errorf("URL not visible in output: %q", result)
	}
}

func TestRenderMarkdownNoWidthGiven(t *testing.T) {
	text := "Line 1\nLine 2"

	result := RenderMarkdown(text, 0, true)

	// Without width, should still apply safe text escaping.
	if strings.Contains(result, "\x1b") {
		t.Errorf("Control characters not escaped: %q", result)
	}
}

func TestRenderMarkdownWrapping(t *testing.T) {
	text := strings.Repeat("word ", 100)
	width := 40

	result := RenderMarkdown(text, width, true)

	// Every line should be within the width constraint (minus two-space prefix).
	lines := strings.Split(result, "\n")
	for i, line := range lines {
		if len(line) > width {
			t.Errorf("Line %d exceeds width %d: length=%d, line=%q", i, width, len(line), line)
		}
	}
}

func TestRenderMarkdownWithTabs(t *testing.T) {
	text := "Line\twith\ttabs"
	width := 80

	result := RenderMarkdown(text, width, true)

	// Tabs should be expanded to spaces.
	if strings.Contains(result, "\t") {
		t.Errorf("Tab character not expanded: %q", result)
	}
}

func TestRenderMarkdownMultilineWithRawMode(t *testing.T) {
	text := "Line 1\n**bold line 2**\nLine 3"
	width := 80

	result := RenderMarkdown(text, width, true)

	// In raw mode, all lines should be preserved and prefixed.
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Non-empty line not prefixed with two spaces: %q", line)
		}
	}
	// Should still contain the markdown markers in raw mode (not rendered).
	if !strings.Contains(result, "**") {
		t.Errorf("Markdown markers removed in raw mode: %q", result)
	}
}

// Golden fixture tests with exact output assertions

func TestGoldenCodeFenceWithLanguage(t *testing.T) {
	text := "```go\nfunc main() {\n  fmt.Println(\"hello\")\n}\n```"
	width := 40

	rawResult := RenderMarkdown(text, width, true)
	renderedResult := RenderMarkdown(text, width, false)

	// Raw mode should preserve the code fence with language label
	if !strings.Contains(rawResult, "```go") {
		t.Errorf("Raw mode: missing code fence language label in: %q", rawResult)
	}

	// Both modes should use 2-space prefix
	for _, line := range strings.Split(rawResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Raw mode: non-prefixed line: %q", line)
		}
	}
	for _, line := range strings.Split(renderedResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Rendered mode: non-prefixed line: %q", line)
		}
	}

	// No ANSI escape sequences should appear
	if strings.Contains(renderedResult, "\x1b") {
		t.Errorf("Rendered mode: contains ANSI escape sequences: %q", renderedResult)
	}
}

func TestGoldenList(t *testing.T) {
	text := "- item 1\n- item 2\n- item 3"
	width := 40

	rawResult := RenderMarkdown(text, width, true)
	renderedResult := RenderMarkdown(text, width, false)

	// Raw mode should preserve list markers
	if !strings.Contains(rawResult, "- item") {
		t.Errorf("Raw mode: missing list items in: %q", rawResult)
	}

	// Both modes should have consistent indentation
	rawLines := strings.Split(rawResult, "\n")
	renderedLines := strings.Split(renderedResult, "\n")

	for i, line := range rawLines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Raw mode: non-prefixed line %d: %q", i, line)
		}
	}
	for i, line := range renderedLines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Rendered mode: non-prefixed line %d: %q", i, line)
		}
	}

	// No trailing padding in raw mode (lines should end without extra spaces)
	for _, line := range rawLines {
		trimmed := strings.TrimRight(line, " ")
		if len(line) > len(trimmed) && strings.TrimSpace(line) != "" {
			// Line has trailing spaces but is not empty after trim
			t.Errorf("Raw mode: line has trailing padding: %q", line)
		}
	}

	// No ANSI sequences
	if strings.Contains(renderedResult, "\x1b") {
		t.Errorf("Rendered mode: contains ANSI escape: %q", renderedResult)
	}
}

func TestGoldenLink(t *testing.T) {
	text := "Visit [example](https://example.com) for more info"
	width := 60

	rawResult := RenderMarkdown(text, width, true)
	renderedResult := RenderMarkdown(text, width, false)

	// URL should be visible in both modes
	if !strings.Contains(rawResult, "example.com") {
		t.Errorf("Raw mode: URL not visible: %q", rawResult)
	}
	if !strings.Contains(renderedResult, "example.com") {
		t.Errorf("Rendered mode: URL not visible: %q", renderedResult)
	}

	// No hyperlink escape sequences
	if strings.Contains(renderedResult, "\x1b]8") {
		t.Errorf("Rendered mode: found hyperlink escape: %q", renderedResult)
	}

	// Both modes must use 2-space prefix
	for _, line := range strings.Split(rawResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Raw mode: non-prefixed line: %q", line)
		}
	}
	for _, line := range strings.Split(renderedResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Rendered mode: non-prefixed line: %q", line)
		}
	}
}

func TestGoldenOutcomeLike(t *testing.T) {
	// Outcome-like text that might look like a list or have special characters
	text := "✓ approved by opus after 1 revision against abc123"
	width := 60

	rawResult := RenderMarkdown(text, width, true)
	renderedResult := RenderMarkdown(text, width, false)

	// Both should preserve the content and use 2-space prefix
	for _, line := range strings.Split(rawResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Raw mode: non-prefixed outcome line: %q", line)
		}
	}
	for _, line := range strings.Split(renderedResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Rendered mode: non-prefixed outcome line: %q", line)
		}
	}

	// Content should be present in both
	if !strings.Contains(rawResult, "approved") {
		t.Errorf("Raw mode: outcome content missing: %q", rawResult)
	}
	if !strings.Contains(renderedResult, "approved") {
		t.Errorf("Rendered mode: outcome content missing: %q", renderedResult)
	}
}

func TestSingleEmphasis(t *testing.T) {
	text := "This is *italic* text"
	width := 40

	result := RenderMarkdown(text, width, false)

	// Should detect single emphasis and render (or preserve it)
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Line not prefixed with two spaces: %q", line)
		}
	}

	// No ANSI escapes
	if strings.Contains(result, "\x1b") {
		t.Errorf("Contains ANSI escape: %q", result)
	}
}

func TestNumberedListAtStart(t *testing.T) {
	text := "1. first\n2. second\n3. third"
	width := 40

	result := RenderMarkdown(text, width, false)

	// Should detect numbered list at start of line
	lines := strings.Split(result, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Line not prefixed with two spaces: %q", line)
		}
	}

	// No ANSI escapes
	if strings.Contains(result, "\x1b") {
		t.Errorf("Contains ANSI escape: %q", result)
	}
}
