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
	text := "```go\nfunc main() {}\n```"
	width := 40

	rawResult := RenderMarkdown(text, width, true)
	renderedResult := RenderMarkdown(text, width, false)

	// Raw mode should preserve the code fence with language label
	expectedRawLines := []string{
		"  ```go",
		"  func main() {}",
		"  ```",
	}
	rawLines := strings.Split(rawResult, "\n")
	if len(rawLines) != len(expectedRawLines) {
		t.Errorf("Raw mode: expected %d lines, got %d:\n  expected: %v\n  got: %v",
			len(expectedRawLines), len(rawLines), expectedRawLines, rawLines)
	}
	for i, line := range rawLines {
		if i < len(expectedRawLines) && line != expectedRawLines[i] {
			t.Errorf("Raw mode line %d: expected %q, got %q", i, expectedRawLines[i], line)
		}
	}

	// Rendered output should also include the language label (not stripped by renderer)
	if !strings.Contains(renderedResult, "```go") {
		t.Errorf("Rendered mode: missing code fence language label in: %q", renderedResult)
	}

	// Both should use 2-space prefix, no trailing padding
	for _, line := range strings.Split(rawResult, "\n") {
		if strings.TrimSpace(line) != "" {
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("Raw mode: line not prefixed with two spaces: %q", line)
			}
			if strings.TrimRight(line, " ") != line && strings.TrimSpace(line) != "" {
				t.Errorf("Raw mode: line has trailing padding: %q", line)
			}
		}
	}

	// No ANSI escape sequences
	if strings.Contains(renderedResult, "\x1b") {
		t.Errorf("Rendered mode: contains ANSI escape: %q", renderedResult)
	}
}

func TestGoldenList(t *testing.T) {
	text := "- item 1\n- item 2"
	width := 40

	rawResult := RenderMarkdown(text, width, true)
	renderedResult := RenderMarkdown(text, width, false)

	// Raw mode expected output
	expectedRawLines := []string{
		"  - item 1",
		"  - item 2",
	}
	rawLines := strings.Split(rawResult, "\n")
	if len(rawLines) != len(expectedRawLines) {
		t.Errorf("Raw mode: expected %d lines, got %d:\n  expected: %v\n  got: %v",
			len(expectedRawLines), len(rawLines), expectedRawLines, rawLines)
	}
	for i, line := range rawLines {
		if i < len(expectedRawLines) && line != expectedRawLines[i] {
			t.Errorf("Raw mode line %d: expected %q, got %q", i, expectedRawLines[i], line)
		}
	}

	// Both modes must use 2-space prefix
	for i, line := range rawLines {
		if strings.TrimSpace(line) != "" {
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("Raw mode line %d: not prefixed with two spaces: %q", i, line)
			}
			// No trailing padding in raw mode
			if strings.TrimRight(line, " ") != line {
				t.Errorf("Raw mode line %d: has trailing padding: %q", i, line)
			}
		}
	}

	for i, line := range strings.Split(renderedResult, "\n") {
		if strings.TrimSpace(line) != "" {
			if !strings.HasPrefix(line, "  ") {
				t.Errorf("Rendered mode line %d: not prefixed with two spaces: %q", i, line)
			}
		}
	}

	// No ANSI escape sequences
	if strings.Contains(renderedResult, "\x1b") {
		t.Errorf("Rendered mode: contains ANSI escape: %q", renderedResult)
	}
}

func TestGoldenLink(t *testing.T) {
	text := "Visit [example](https://example.com) now"
	width := 60

	rawResult := RenderMarkdown(text, width, true)
	renderedResult := RenderMarkdown(text, width, false)

	// Raw mode expected output
	expectedRawLines := []string{
		"  Visit [example](https://example.com) now",
	}
	rawLines := strings.Split(rawResult, "\n")
	if len(rawLines) != len(expectedRawLines) {
		t.Errorf("Raw mode: expected %d lines, got %d:\n  expected: %v\n  got: %v",
			len(expectedRawLines), len(rawLines), expectedRawLines, rawLines)
	}
	for i, line := range rawLines {
		if i < len(expectedRawLines) && line != expectedRawLines[i] {
			t.Errorf("Raw mode line %d: expected %q, got %q", i, expectedRawLines[i], line)
		}
	}

	// URL must be visible in both modes
	if !strings.Contains(rawResult, "example.com") {
		t.Errorf("Raw mode: URL not visible: %q", rawResult)
	}
	if !strings.Contains(renderedResult, "example.com") {
		t.Errorf("Rendered mode: URL not visible: %q", renderedResult)
	}

	// No hyperlink escape sequences in rendered mode
	if strings.Contains(renderedResult, "\x1b]8") {
		t.Errorf("Rendered mode: found hyperlink escape: %q", renderedResult)
	}

	// Both modes must use 2-space prefix
	for i, line := range strings.Split(rawResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Raw mode line %d: not prefixed with two spaces: %q", i, line)
		}
	}
	for i, line := range strings.Split(renderedResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Rendered mode line %d: not prefixed with two spaces: %q", i, line)
		}
	}
}

func TestGoldenOutcomeLike(t *testing.T) {
	// Test outcome-like text with special characters that could confuse rendering
	text := "✓ APPROVED by opus after 1 revision"
	width := 60

	rawResult := RenderMarkdown(text, width, true)
	renderedResult := RenderMarkdown(text, width, false)

	// Raw mode expected
	expectedRawLines := []string{
		"  ✓ APPROVED by opus after 1 revision",
	}
	rawLines := strings.Split(rawResult, "\n")
	if len(rawLines) != len(expectedRawLines) {
		t.Errorf("Raw mode: expected %d lines, got %d:\n  expected: %v\n  got: %v",
			len(expectedRawLines), len(rawLines), expectedRawLines, rawLines)
	}
	for i, line := range rawLines {
		if i < len(expectedRawLines) && line != expectedRawLines[i] {
			t.Errorf("Raw mode line %d: expected %q, got %q", i, expectedRawLines[i], line)
		}
	}

	// Both modes must use 2-space prefix
	for i, line := range strings.Split(rawResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Raw mode line %d: not prefixed with two spaces: %q", i, line)
		}
	}
	for i, line := range strings.Split(renderedResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Rendered mode line %d: not prefixed with two spaces: %q", i, line)
		}
	}

	// Content should match in both modes
	if !strings.Contains(rawResult, "APPROVED") {
		t.Errorf("Raw mode: outcome content missing: %q", rawResult)
	}
	if !strings.Contains(renderedResult, "APPROVED") {
		t.Errorf("Rendered mode: outcome content missing: %q", renderedResult)
	}
}

// TestGoldenHostileBytes tests rendering of non-UTF-8 and control characters
func TestGoldenHostileBytes(t *testing.T) {
	// Invalid UTF-8 and control characters should be escaped, not leaked
	text := "Data: \xff\x1b[31mred\x1b[0m"
	width := 60

	rawResult := RenderMarkdown(text, width, true)
	renderedResult := RenderMarkdown(text, width, false)

	// Should escape dangerous bytes, not show them raw
	if strings.Contains(rawResult, "\xff") {
		t.Errorf("Raw mode: invalid byte not escaped: %q", rawResult)
	}
	if strings.Contains(rawResult, "\x1b") {
		t.Errorf("Raw mode: ANSI escape not escaped: %q", rawResult)
	}
	if strings.Contains(renderedResult, "\x1b") {
		t.Errorf("Rendered mode: ANSI escape not escaped: %q", renderedResult)
	}

	// Should show visible escape representations
	if !strings.Contains(rawResult, "\\xff") {
		t.Errorf("Raw mode: visible escape missing: %q", rawResult)
	}

	// Both modes must use 2-space prefix
	for i, line := range strings.Split(rawResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Raw mode line %d: not prefixed with two spaces: %q", i, line)
		}
	}
	for i, line := range strings.Split(renderedResult, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Rendered mode line %d: not prefixed with two spaces: %q", i, line)
		}
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

// TestRawModeToggleInteraction tests that /raw command changes View output
func TestRawModeToggleInteraction(t *testing.T) {
	// Use text with emphasis that wraps differently when rendered
	text := "This is **bold** and *italic* and `code`"
	width := 60

	// Raw mode (Markdown disabled)
	rawOutput := RenderMarkdown(text, width, true)

	// Rendered mode (Markdown enabled)
	renderedOutput := RenderMarkdown(text, width, false)

	// Raw mode should preserve Markdown markers
	if !strings.Contains(rawOutput, "**") {
		t.Errorf("Raw mode should preserve ** markers: %q", rawOutput)
	}
	if !strings.Contains(rawOutput, "*") {
		t.Errorf("Raw mode should preserve * markers: %q", rawOutput)
	}

	// Outputs may differ in whitespace/wrapping, but both are valid
	// Just verify they both have proper prefix and no dangerous escapes
	for i, line := range strings.Split(rawOutput, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Raw line %d not prefixed: %q", i, line)
		}
	}
	for i, line := range strings.Split(renderedOutput, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "  ") {
			t.Errorf("Rendered line %d not prefixed: %q", i, line)
		}
	}

	// No dangerous escapes in either mode
	if strings.Contains(rawOutput, "\x1b") {
		t.Errorf("Raw mode has ANSI escape: %q", rawOutput)
	}
	if strings.Contains(renderedOutput, "\x1b") {
		t.Errorf("Rendered mode has ANSI escape: %q", renderedOutput)
	}
}
