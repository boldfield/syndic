package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// bidiChars are Unicode bidirectional override and isolate characters that must be escaped.
var bidiChars = map[rune]bool{
	'\u202a': true, // LEFT-TO-RIGHT EMBEDDING
	'\u202b': true, // RIGHT-TO-LEFT EMBEDDING
	'\u202c': true, // POP DIRECTIONAL FORMATTING
	'\u202d': true, // LEFT-TO-RIGHT OVERRIDE
	'\u202e': true, // RIGHT-TO-LEFT OVERRIDE
	'\u2066': true, // LEFT-TO-RIGHT ISOLATE
	'\u2067': true, // RIGHT-TO-LEFT ISOLATE
	'\u2068': true, // FIRST STRONG ISOLATE
	'\u2069': true, // POP DIRECTIONAL ISOLATE
}

// safeToken is one displayed unit of safe text: a printable rune, an expanded
// tab space, a visible escape (atomic, never split across lines) or a newline.
type safeToken struct {
	text  string
	width int
	nl    bool
}

// safeTokens is the single escaping pass over untrusted bytes. It reports
// token boundaries so wrapping never re-parses its own output.
//   - C0 controls except LF and tab, and DEL, become \xNN
//   - C1 controls (U+0080-U+009F) become the \xNN of their UTF-8 bytes
//   - bidi overrides and isolates become \uXXXX
//   - bytes that are not valid UTF-8 become \xNN
//   - tab expands to the next multiple of four columns
func safeTokens(data []byte) []safeToken {
	const tabStop = 4
	// Most bytes are a token of their own; sizing for them once keeps a long answer,
	// drawn again on every frame, from growing the slice over and over.
	toks := make([]safeToken, 0, len(data))
	col := 0
	esc := func(s string) {
		toks = append(toks, safeToken{text: s, width: len(s)})
		col += len(s)
	}

	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			esc(fmt.Sprintf("\\x%02x", data[0]))
			data = data[1:]
			continue
		}
		raw := data[:size]
		data = data[size:]

		switch {
		case r == '\t':
			n := tabStop - (col % tabStop)
			for i := 0; i < n; i++ {
				toks = append(toks, safeToken{text: " ", width: 1})
			}
			col += n
		case r == '\n':
			toks = append(toks, safeToken{text: "\n", nl: true})
			col = 0
		case r < 0x20 || r == 0x7F:
			esc(fmt.Sprintf("\\x%02x", r))
		case r >= 0x80 && r <= 0x9F:
			for _, b := range raw {
				esc(fmt.Sprintf("\\x%02x", b))
			}
		case bidiChars[r]:
			esc(fmt.Sprintf("\\u%04x", r))
		default:
			w := max(runewidth.RuneWidth(r), 0)
			toks = append(toks, safeToken{text: string(raw), width: w})
			col += w
		}
	}
	return toks
}

// ToSafeText converts untrusted bytes to safe terminal text; see safeTokens
// for the rules. Original bytes are never modified, only their rendering.
func ToSafeText(data []byte) string {
	var out strings.Builder
	for _, t := range safeTokens(data) {
		out.WriteString(t.text)
	}
	return out.String()
}

// contentPrefix indents every terminal line of displayed content.
const contentPrefix = "  "

// minContentWidth is the narrowest full line SafeTextWithPrefix honours: the
// prefix plus the widest atomic token (a six-cell \uXXXX escape). Narrower
// requests are widened to it, so no line is ever wider than max(width, 8).
const minContentWidth = len(contentPrefix) + 6

// SafeTextWithPrefix is the single content-to-terminal boundary for untrusted
// text: it escapes the raw text once, wraps it so every line, prefix included,
// fits max(width, minContentWidth) cells, and prefixes every terminal line
// with two spaces. Callers pass raw text, never already-escaped text.
func SafeTextWithPrefix(text string, width int) []string {
	width = max(width, minContentWidth) - len(contentPrefix)
	out := wrapTokens(safeTokens([]byte(text)), width)
	for i, l := range out {
		out[i] = contentPrefix + l
	}
	return out
}

// wrapTokens lays safe tokens out on lines of at most width cells. An escape
// is never split, so a line holding only one wide token may exceed width.
// A trailing LF ends the last line rather than opening another.
func wrapTokens(toks []safeToken, width int) []string {
	var out []string
	var cur strings.Builder
	col := 0
	flush := func() {
		out = append(out, cur.String())
		cur.Reset()
		col = 0
	}
	pending := false // a line is open, so a trailing LF does not add another
	for _, t := range toks {
		if t.nl {
			flush()
			pending = false
			continue
		}
		if col > 0 && col+t.width > width {
			flush()
		}
		cur.WriteString(t.text)
		col += t.width
		pending = true
	}
	if pending {
		flush()
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// safeLine is ToSafeText for a value shown on one line, such as an id, a
// name, a path or a status detail: a line feed is escaped too, so the value
// can neither break a row nor reach column 0.
func safeLine(s string) string {
	var out strings.Builder
	for _, t := range safeTokens([]byte(s)) {
		if t.nl {
			out.WriteString("\\x0a")
			continue
		}
		out.WriteString(t.text)
	}
	return out.String()
}
