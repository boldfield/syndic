package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// bidiChars are Unicode bidirectional override and isolate characters that must be escaped.
var bidiChars = map[rune]bool{
	'‪': true, // LEFT-TO-RIGHT EMBEDDING
	'‫': true, // RIGHT-TO-LEFT EMBEDDING
	'‬': true, // POP DIRECTIONAL FORMATTING
	'‭': true, // LEFT-TO-RIGHT OVERRIDE
	'‮': true, // RIGHT-TO-LEFT OVERRIDE
	'⁦': true, // LEFT-TO-RIGHT ISOLATE
	'⁧': true, // RIGHT-TO-LEFT ISOLATE
	'⁨': true, // FIRST STRONG ISOLATE
	'⁩': true, // POP DIRECTIONAL ISOLATE
}

// ToSafeText converts untrusted bytes to safe terminal text.
// - C0 controls (0x00-0x1F) except LF (0x0A) and tab (0x09) are escaped as \xNN
// - DEL (0x7F) is escaped as \xNN
// - C1 controls (0x80-0x9F) are escaped as \xNN
// - Unicode bidi overrides/isolates are escaped as \uXXXX
// - Invalid UTF-8 bytes are escaped as \xNN
// - Tab (0x09) is expanded to spaces according to tabStop
func ToSafeText(data []byte) string {
	const tabStop = 4
	var out strings.Builder
	col := 0

	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)

		if r == utf8.RuneError {
			// Invalid UTF-8: escape the first byte as \xNN
			fmt.Fprintf(&out, "\\x%02x", data[0])
			col += 4
			data = data[1:]
			continue
		}

		data = data[size:]

		switch {
		case r == '\t':
			// Expand tab to next multiple of tabStop
			spacesToAdd := tabStop - (col % tabStop)
			for i := 0; i < spacesToAdd; i++ {
				out.WriteRune(' ')
			}
			col += spacesToAdd

		case r == '\n':
			// Line feed: keep as-is, reset column
			out.WriteRune(r)
			col = 0

		case r < 0x20 && r != '\n':
			// C0 control (except LF): escape as \xHH
			fmt.Fprintf(&out, "\\x%02x", r)
			col += 4

		case r == 0x7F:
			// DEL: escape as \xHH
			fmt.Fprintf(&out, "\\x%02x", r)
			col += 4

		case r >= 0x80 && r <= 0x9F:
			// C1 control: escape as \xHH (UTF-8 encoded)
			for _, b := range []byte(string(r)) {
				fmt.Fprintf(&out, "\\x%02x", b)
			}
			col += len([]byte(string(r))) * 4

		case bidiChars[r]:
			// Bidi override/isolate: escape as \uXXXX
			fmt.Fprintf(&out, "\\u%04x", r)
			col += 6

		default:
			// Regular character
			out.WriteRune(r)
			// Update column width based on character width
			w := runeWidth(r)
			if w > 0 {
				col += w
			}
		}
	}

	return out.String()
}

// runeWidth returns the display width of a rune.
// This is a simple version; for full support we'd use go-runewidth.
func runeWidth(r rune) int {
	if r < 0x20 || r == 0x7F {
		return 0
	}
	if unicode.IsControl(r) {
		return 0
	}
	// For now, assume most characters are 1 wide; wide characters would need external library
	return 1
}

// SafeTextWithPrefix wraps text to width and prefixes each line with two spaces.
// Lines are separated by newlines and wrapped at width cells.
func SafeTextWithPrefix(text string, width int) []string {
	const prefix = "  "
	const tabStop = 4
	const minWidth = 1

	if width < minWidth {
		width = minWidth
	}

	// First pass: escape the text to safe form
	safeText := ToSafeText([]byte(text))

	// Split into paragraphs on newlines
	var out []string
	for _, para := range strings.Split(safeText, "\n") {
		// Wrap paragraph to width
		lines := wrapParagraph(para, width-len(prefix), tabStop)
		// Add prefix to each line
		for _, line := range lines {
			out = append(out, prefix+line)
		}
	}

	return out
}

// wrapParagraph wraps a single paragraph to width cells, handling tabs.
// The width is the available width for content (before prefix is added).
func wrapParagraph(para string, width int, tabStop int) []string {
	const minWidth = 1
	if width < minWidth {
		width = minWidth
	}

	var lines []string
	var cur strings.Builder
	col := 0

	for _, r := range para {
		switch r {
		case '\t':
			// Expand tab to next multiple of tabStop
			spacesToAdd := tabStop - (col % tabStop)
			for i := 0; i < spacesToAdd; i++ {
				if col >= width {
					lines = append(lines, cur.String())
					cur.Reset()
					col = 0
				}
				cur.WriteRune(' ')
				col++
			}

		default:
			w := 1 // Assume single-width rune
			if col > 0 && col+w > width {
				// Need to wrap
				lines = append(lines, cur.String())
				cur.Reset()
				col = 0
			}
			cur.WriteRune(r)
			col += w
		}
	}

	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}

	// Ensure at least one line
	if len(lines) == 0 {
		lines = []string{""}
	}

	return lines
}
