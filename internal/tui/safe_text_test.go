package tui

import (
	"bytes"
	"testing"
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
			name: "C1 control",
			data: []byte("test\x80end"),
			want: "test\\x80end",
		},
		{
			name: "C1 high control",
			data: []byte("test\x9fend"),
			want: "test\\x9fend",
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
			name:     "event details with C1",
			data:     []byte("Error: \x80\x81\x9f"),
			contains: []string{"Error", "\\x80", "\\x81", "\\x9f"},
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
