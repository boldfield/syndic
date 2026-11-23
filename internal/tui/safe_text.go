package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
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

		if r == utf8.RuneError && size == 1 {
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
			w := runewidth.RuneWidth(r)
			if w > 0 {
				col += w
			}
		}
	}

	return out.String()
}

// SafeTextWithPrefix wraps text to width and prefixes each line with two spaces.
// Lines are separated by newlines and wrapped at width cells.
// The width is the target width for the full line including the two-space prefix.
func SafeTextWithPrefix(text string, width int) []string {
	const prefix = "  "
	const minWidth = 3 // minimum 3: prefix (2) + at least 1 for content

	if width < minWidth {
		width = minWidth
	}

	// First pass: escape the text to safe form
	safeText := ToSafeText([]byte(text))

	// Split into paragraphs on newlines, trim trailing empty line
	paragraphs := strings.Split(safeText, "\n")
	if len(paragraphs) > 0 && paragraphs[len(paragraphs)-1] == "" {
		paragraphs = paragraphs[:len(paragraphs)-1]
	}

	// Split into paragraphs on newlines
	var out []string
	for _, para := range paragraphs {
		// Wrap paragraph to width, accounting for the prefix
		lines := wrapParagraph(para, width-len(prefix))
		// Add prefix to each line
		for _, line := range lines {
			out = append(out, prefix+line)
		}
	}

	// If input was entirely empty or only newlines, return at least one empty prefixed line
	if len(out) == 0 {
		out = []string{prefix}
	}

	return out
}

// wrapParagraph wraps a single paragraph to width cells using runewidth for correct cell counting.
// Keeps escape sequences atomic so they don't split across lines.
// The width is the available width for content (before prefix is added).
// ToSafeText has already expanded tabs, so we don't handle tabs here.
func wrapParagraph(para string, width int) []string {
	const minWidth = 1
	if width < minWidth {
		width = minWidth
	}

	var lines []string
	var cur strings.Builder
	col := 0

	i := 0
	for i < len(para) {
		if para[i] == '\\' && i+1 < len(para) {
			// This might be an escape sequence; check what it is
			switch para[i+1] {
			case 'x':
				// \xNN escape - 4 characters total
				if i+3 < len(para) {
					escLen := 4
					w := 4 // escape sequences always render as 4 cells visually
					if col > 0 && col+w > width {
						// Need to wrap before this escape
						lines = append(lines, cur.String())
						cur.Reset()
						col = 0
					}
					cur.WriteString(para[i : i+escLen])
					col += w
					i += escLen
					continue
				}
			case 'u':
				// \uXXXX escape - 6 characters total
				if i+5 < len(para) {
					escLen := 6
					w := 6 // escape sequences always render as 6 cells visually
					if col > 0 && col+w > width {
						// Need to wrap before this escape
						lines = append(lines, cur.String())
						cur.Reset()
						col = 0
					}
					cur.WriteString(para[i : i+escLen])
					col += w
					i += escLen
					continue
				}
			}
		}

		// Regular rune - decode it properly
		r, size := utf8.DecodeRuneInString(para[i:])
		if r == utf8.RuneError && size == 0 {
			// Should not happen since input is from ToSafeText which produces valid UTF-8
			i++
			continue
		}

		w := runewidth.RuneWidth(r)
		if w < 0 {
			w = 0
		}

		if col > 0 && col+w > width && cur.Len() > 0 {
			// Need to wrap before this rune
			lines = append(lines, cur.String())
			cur.Reset()
			col = 0
		}
		cur.WriteString(para[i : i+size])
		col += w
		i += size
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
