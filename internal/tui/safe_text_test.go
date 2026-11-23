package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestToSafeText_C0Controls(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "NUL character",
			data: []byte("hello\x00world"),
			want: "hello\\x00world",
		},
		{
			name: "BEL character",
			data: []byte("test\x07bell"),
			want: "test\\x07bell",
		},
		{
			name: "CR character",
			data: []byte("line\rreturn"),
			want: "line\\x0dreturn",
		},
		{
			name: "LF is kept",
			data: []byte("line\nfeed"),
			want: "line\nfeed",
		},
		{
			name: "Tab is expanded",
			data: []byte("before\tafter"),
			want: "before  after",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToSafeText(tt.data)
			if got != tt.want {
				t.Errorf("ToSafeText(%q) = %q, want %q", tt.data, got, tt.want)
			}
		})
	}
}

func TestToSafeText_DELAndC1(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "DEL character",
			data: []byte("test\x7fend"),
			want: "test\\x7fend",
		},
		{
			name: "C1 control as invalid UTF-8",
			data: []byte("test\x80end"),
			want: "test\\x80end",
		},
		{
			name: "C1 high control as invalid UTF-8",
			data: []byte("test\x9fend"),
			want: "test\\x9fend",
		},
		{
			name: "valid UTF-8 C1: U+0085 (NEL)",
			data: []byte("test\xc2\x85end"), // U+0085 encoded as UTF-8
			want: "test\\xc2\\x85end",
		},
		{
			name: "valid UTF-8 C1: U+009B (8-bit CSI)",
			data: []byte("test\xc2\x9bend"), // U+009B encoded as UTF-8
			want: "test\\xc2\\x9bend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToSafeText(tt.data)
			if got != tt.want {
				t.Errorf("ToSafeText(%q) = %q, want %q", tt.data, got, tt.want)
			}
		})
	}
}

func TestToSafeText_InvalidUTF8(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "lone high byte",
			data: []byte("test\xffend"),
			want: "test\\xffend",
		},
		{
			name: "incomplete UTF-8 sequence",
			data: []byte("test\xc3end"), // incomplete UTF-8 start
			want: "test\\xc3end",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToSafeText(tt.data)
			if got != tt.want {
				t.Errorf("ToSafeText(%q) = %q, want %q", tt.data, got, tt.want)
			}
		})
	}
}

func TestToSafeText_BidiCharacters(t *testing.T) {
	bidiChars := map[string]struct {
		rune rune
		name string
		code string
	}{
		"LRE": {'‪', "LEFT-TO-RIGHT EMBEDDING", "\\u202a"},
		"RLE": {'‫', "RIGHT-TO-LEFT EMBEDDING", "\\u202b"},
		"PDF": {'‬', "POP DIRECTIONAL FORMATTING", "\\u202c"},
		"LRO": {'‭', "LEFT-TO-RIGHT OVERRIDE", "\\u202d"},
		"RLO": {'‮', "RIGHT-TO-LEFT OVERRIDE", "\\u202e"},
		"LRI": {'⁦', "LEFT-TO-RIGHT ISOLATE", "\\u2066"},
		"RLI": {'⁧', "RIGHT-TO-LEFT ISOLATE", "\\u2067"},
		"FSI": {'⁨', "FIRST STRONG ISOLATE", "\\u2068"},
		"PDI": {'⁩', "POP DIRECTIONAL ISOLATE", "\\u2069"},
	}

	for key, bc := range bidiChars {
		t.Run(key+" ("+bc.name+")", func(t *testing.T) {
			testStr := "test" + string(bc.rune) + "text"
			data := []byte(testStr)
			got := ToSafeText(data)
			want := "test" + bc.code + "text"
			if got != want {
				t.Errorf("ToSafeText(%q) = %q, want %q", data, got, want)
			}
		})
	}
}

func TestToSafeText_TabExpansion(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "single tab at start",
			data: "\thello",
			want: "    hello", // 4 spaces at tab stop 0
		},
		{
			name: "tab with prefix",
			data: "ab\tcd",
			want: "ab  cd", // 2 spaces to reach tab stop 4
		},
		{
			name: "multiple tabs",
			data: "a\tb\tc",
			want: "a   b   c", // 3 spaces + 'b' at 4, then 3 spaces + 'c' at 8
		},
		{
			name: "tab after newline",
			data: "a\n\tb",
			want: "a\n    b", // reset at newline, 4 spaces at tab stop 0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToSafeText([]byte(tt.data))
			if got != tt.want {
				t.Errorf("ToSafeText(%q) = %q, want %q", tt.data, got, tt.want)
			}
		})
	}
}

func TestToSafeText_AdversarialCases(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		contains []string
	}{
		{
			name:     "model output with escapes",
			data:     []byte("Here's output:\x1b[31mred\x1b[0m"),
			contains: []string{"\\x1b", "red"},
		},
		{
			name:     "filename with control chars",
			data:     []byte("file\x00name\x1ftxt"),
			contains: []string{"file", "\\x00", "name", "\\x1f", "txt"},
		},
		{
			name:     "OSC hyperlink escape",
			data:     []byte("text\x1b]8;;https://example.com\x1b\\link"),
			contains: []string{"\\x1b", "text", "link"},
		},
		{
			name:     "cursor movement escape",
			data:     []byte("before\x1b[2Jafter"),
			contains: []string{"\\x1b", "before", "after"},
		},
		{
			name:     "user text with CR",
			data:     []byte("line1\rline2"),
			contains: []string{"line1", "\\x0d", "line2"},
		},
		{
			name:     "attachment-like binary bytes",
			data:     []byte("data\x00\x01\x02\x03end"),
			contains: []string{"data", "\\x00", "\\x01", "\\x02", "\\x03", "end"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToSafeText(tt.data)
			for _, substr := range tt.contains {
				if !bytes.Contains([]byte(got), []byte(substr)) {
					t.Errorf("ToSafeText(%q) = %q, missing substring %q", tt.data, got, substr)
				}
			}
		})
	}
}

func TestToSafeText_NoRawControls(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "model output with escape sequence",
			data: []byte("Red text: \x1b[31mHello\x1b[0m"),
		},
		{
			name: "cursor movement",
			data: []byte("Position: \x1b[2J\x1b[H"),
		},
		{
			name: "C1 controls as invalid UTF-8",
			data: []byte("Error: \x80\x85\x9f"),
		},
		{
			name: "valid UTF-8 C1 controls",
			data: []byte("Error: \xc2\x85\xc2\x9b"), // U+0085 and U+009B in UTF-8
		},
		{
			name: "CR and LF",
			data: []byte("line1\r\nline2"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToSafeText(tt.data)

			// Verify no raw terminal control bytes
			dangerousBytes := []byte{0x1b, 0x07, 0x0d}
			for _, b := range dangerousBytes {
				if bytes.Contains([]byte(got), []byte{b}) {
					t.Errorf("ToSafeText output contains raw control byte 0x%02x: %q", b, got)
				}
			}

			// Verify no C1 controls in output (they should be escaped)
			// Check for raw C1 bytes (0x80-0x9f)
			// But be careful: UTF-8 continuation bytes can also be in this range
			// So we check rune-by-rune instead
			for _, r := range got {
				if r >= 0x80 && r <= 0x9f {
					// This is a raw C1 byte - not allowed
					t.Errorf("ToSafeText output contains raw C1 byte 0x%02x: %q", byte(r), got)
				}
			}
		})
	}
}

func TestToSafeText_ValidUTF8Replacement(t *testing.T) {
	// Test that valid U+FFFD is preserved, not escaped
	validReplacementChar := string(rune(0xFFFD))
	data := []byte("before" + validReplacementChar + "after")
	got := ToSafeText(data)

	// Should contain the actual replacement character, not escaped bytes
	if !strings.Contains(got, validReplacementChar) {
		t.Errorf("ToSafeText corrupted valid U+FFFD: got %q", got)
	}
	if strings.Contains(got, "\\xef\\xbf\\xbd") {
		t.Errorf("ToSafeText escaped valid U+FFFD as bytes: got %q", got)
	}
}

func TestSafeTextWithPrefix_WidthCompliance(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
	}{
		{
			name:  "ASCII text",
			text:  "Hello, World!",
			width: 20,
		},
		{
			name:  "Wide CJK characters",
			text:  "日本語日本語日本語",
			width: 20,
		},
		{
			name:  "Emoji",
			text:  "👨👩👧👦👨👩👧👦",
			width: 20,
		},
		{
			name:  "Mixed ASCII and wide",
			text:  "Hello 世界 test",
			width: 25,
		},
		{
			name:  "small width",
			text:  "abcdef",
			width: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeTextWithPrefix(tt.text, tt.width)

			for i, line := range got {
				// Check prefix exists
				if !strings.HasPrefix(line, "  ") {
					t.Errorf("Line %d missing two-space prefix: %q", i, line)
				}

				// Check width compliance - the full line should not exceed width
				cellWidth := runewidth.StringWidth(line)
				if cellWidth > tt.width {
					t.Errorf("Line %d exceeds width %d (got %d cells): %q", i, tt.width, cellWidth, line)
				}
			}
		})
	}
}

func TestSafeTextWithPrefix_AllLinesHavePrefix(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
	}{
		{
			name:  "single line",
			text:  "hello",
			width: 20,
		},
		{
			name:  "multi-line",
			text:  "line1\nline2\nline3",
			width: 20,
		},
		{
			name:  "long text that wraps",
			text:  "This is a very long text that should wrap to multiple lines when formatted",
			width: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeTextWithPrefix(tt.text, tt.width)

			if len(got) == 0 {
				t.Errorf("SafeTextWithPrefix returned empty result for %q", tt.text)
				return
			}

			for i, line := range got {
				if !strings.HasPrefix(line, "  ") {
					t.Errorf("Line %d (of %d) missing prefix: %q", i, len(got), line)
				}
			}
		})
	}
}

func TestSafeTextWithPrefix_EscapesAtomic(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "escape should not split across lines",
			text: "ab\x1bcd",
			want: []string{"  ab\\x1b", "  cd"},
		},
		{
			name: "long escape token at boundary",
			text: "ab\u202ecd", // RTL override
			want: []string{"  ab\\u202e", "  cd"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeTextWithPrefix(tt.text, 10) // narrow width to force wrapping
			// Just verify escapes are not split - each escape should appear whole on a line
			fullOutput := strings.Join(got, "")
			for _, part := range tt.want {
				// Check that the escape from want appears in some line
				escaped := strings.TrimPrefix(part, "  ")
				if !strings.Contains(fullOutput, escaped) {
					t.Errorf("SafeTextWithPrefix(%q, 10) missing escape %q in output %v", tt.text, escaped, got)
				}
			}
		})
	}
}

func TestSafeTextWithPrefix(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{
			name:  "simple text",
			text:  "hello",
			width: 20,
			want:  []string{"  hello"},
		},
		{
			name:  "text with newline",
			text:  "line1\nline2",
			width: 20,
			want:  []string{"  line1", "  line2"},
		},
		{
			name:  "text with control char",
			text:  "hello\x00world",
			width: 20,
			want:  []string{"  hello\\x00world"},
		},
		{
			name:  "text with tab",
			text:  "hello\tworld",
			width: 20,
			want:  []string{"  hello   world"},
		},
		{
			name:  "long text wrapping",
			text:  "a very long text that should be wrapped to fit in the width",
			width: 20,
			want: []string{
				"  a very long text t",
				"  hat should be wrap",
				"  ped to fit in the ",
				"  width",
			},
		},
		{
			name:  "text with trailing newline",
			text:  "hello\n",
			width: 20,
			want:  []string{"  hello"},
		},
		{
			name:  "empty text",
			text:  "",
			width: 20,
			want:  []string{"  "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeTextWithPrefix(tt.text, tt.width)
			if len(got) != len(tt.want) {
				t.Errorf("SafeTextWithPrefix(%q, %d) = %d lines, want %d lines", tt.text, tt.width, len(got), len(tt.want))
				t.Logf("got: %v", got)
				t.Logf("want: %v", tt.want)
				return
			}
			for i, line := range got {
				if line != tt.want[i] {
					t.Errorf("SafeTextWithPrefix(%q, %d) line %d = %q, want %q", tt.text, tt.width, i, line, tt.want[i])
				}
			}
		})
	}
}
