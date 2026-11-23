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
