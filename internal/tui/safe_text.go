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
	var toks []safeToken
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

	var out []string
	var cur strings.Builder
	col := 0
	flush := func() {
		out = append(out, contentPrefix+cur.String())
		cur.Reset()
		col = 0
	}
	pending := false // a line is open, so a trailing LF does not add another
	for _, t := range safeTokens([]byte(text)) {
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
		out = []string{contentPrefix}
	}
	return out
}
