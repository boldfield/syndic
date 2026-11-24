package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// RenderMarkdown renders answer text at the given terminal width, with
// Markdown unless raw is true.
func RenderMarkdown(text string, width int, raw bool) string {
	return renderAnswerMarkdown(text, width, raw, false)
}

// renderAnswerMarkdown renders answer text for the chat tab. Raw and plain
// (no-colour) modes show unformatted safe text through SafeTextWithPrefix.
// Otherwise the bytes are escaped once into safe tokens, Markdown is parsed
// over those tokens (a visible escape is one atomic token the parser never
// looks inside) and only then does the TUI add its own SGR styling. The
// result is wrapped and indented to the same final content indentation as
// raw mode.
func renderAnswerMarkdown(text string, width int, raw bool, noColor bool) string {
	if width <= 0 {
		return ToSafeText([]byte(text))
	}
	if raw || noColor {
		return strings.Join(SafeTextWithPrefix(text, width), "\n")
	}
	lines := renderMarkdownTokens(safeTokens([]byte(text)))
	return strings.Join(wrapStyled(lines, max(width, minContentWidth)-len(contentPrefix)), "\n")
}

// mdStyle is a set of TUI-owned display styles; untrusted text never
// contributes an escape, only these do.
type mdStyle uint8

const (
	styleBold mdStyle = 1 << iota
	styleItalic
	styleCode
	styleUnderline
)

// styledTok is a safe token with the style it is displayed in.
type styledTok struct {
	safeToken
	st mdStyle
}

func (s mdStyle) sgr() string {
	var codes []string
	if s&styleBold != 0 {
		codes = append(codes, "1")
	}
	if s&styleItalic != 0 {
		codes = append(codes, "3")
	}
	if s&styleUnderline != 0 {
		codes = append(codes, "4")
	}
	if s&styleCode != 0 {
		codes = append(codes, inlineCodeSGR())
	}
	return "\x1b[" + strings.Join(codes, ";") + "m"
}

// inlineCodeSGR is the colour of code spans, declared as a light/dark pair
// and chosen from the terminal's background like every other TUI colour.
func inlineCodeSGR() string {
	if lipgloss.HasDarkBackground() {
		return "36"
	}
	return "34"
}

const sgrReset = "\x1b[0m"

// wrapStyled lays styled lines out on lines of at most width cells and
// prefixes each one. Style is closed at the end of every line and reopened
// on the next, so the prefix and following rows are never styled.
func wrapStyled(lines [][]styledTok, width int) []string {
	var out []string
	var cur strings.Builder
	col := 0
	var applied mdStyle
	flush := func() {
		if applied != 0 {
			cur.WriteString(sgrReset)
			applied = 0
		}
		out = append(out, contentPrefix+cur.String())
		cur.Reset()
		col = 0
	}
	for _, line := range lines {
		for _, t := range line {
			if col > 0 && col+t.width > width {
				flush()
			}
			if t.st != applied {
				if applied != 0 {
					cur.WriteString(sgrReset)
				}
				if t.st != 0 {
					cur.WriteString(t.st.sgr())
				}
				applied = t.st
			}
			cur.WriteString(t.text)
			col += t.width
		}
		flush()
	}
	return out
}

func isSpaceTok(t safeToken) bool { return t.text == " " }

func isWordTok(t safeToken) bool {
	r, size := utf8.DecodeRuneInString(t.text)
	return size == len(t.text) && (r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
}

func plainToks(toks []safeToken, st mdStyle) []styledTok {
	out := make([]styledTok, len(toks))
	for i, t := range toks {
		out[i] = styledTok{t, st}
	}
	return out
}

func stText(s string) []safeToken {
	toks := make([]safeToken, 0, len(s))
	for _, r := range s {
		toks = append(toks, safeToken{text: string(r), width: 1})
	}
	return toks
}

// splitLines splits tokens at line feeds. A trailing line feed ends the last
// line rather than opening another, as in raw mode.
func splitLines(toks []safeToken) [][]safeToken {
	var lines [][]safeToken
	var cur []safeToken
	pending := false
	for _, t := range toks {
		if t.nl {
			lines = append(lines, cur)
			cur = nil
			pending = false
			continue
		}
		cur = append(cur, t)
		pending = true
	}
	if pending {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		lines = [][]safeToken{nil}
	}
	return lines
}

func countLeadingSpaces(toks []safeToken) int {
	n := 0
	for n < len(toks) && isSpaceTok(toks[n]) {
		n++
	}
	return n
}

func trimTrailingSpaces(toks []safeToken) []safeToken {
	for len(toks) > 0 && isSpaceTok(toks[len(toks)-1]) {
		toks = toks[:len(toks)-1]
	}
	return toks
}

// fenceOf recognises a fence line: its marker, run length and info string.
func fenceOf(line []safeToken) (marker string, n int, info []safeToken, ok bool) {
	i := countLeadingSpaces(line)
	if i > 3 || i >= len(line) || (line[i].text != "`" && line[i].text != "~") {
		return "", 0, nil, false
	}
	marker = line[i].text
	j := i
	for j < len(line) && line[j].text == marker {
		j++
	}
	if j-i < 3 {
		return "", 0, nil, false
	}
	rest := line[j:]
	rest = rest[countLeadingSpaces(rest):]
	k := 0
	for k < len(rest) && !isSpaceTok(rest[k]) {
		k++
	}
	return marker, j - i, rest[:k], true
}

func allSpaces(toks []safeToken) bool { return countLeadingSpaces(toks) == len(toks) }

// renderMarkdownTokens turns safe tokens into styled display lines: lists get
// bullets, headings are bold, links show their URL, emphasis and inline code
// are styled and a fenced block shows its language label above the code.
func renderMarkdownTokens(toks []safeToken) [][]styledTok {
	var out [][]styledTok
	fence := ""
	fenceLen := 0
	for _, line := range splitLines(toks) {
		if fence != "" {
			// only a bare fence of the same kind, at least as long, closes
			if m, n, info, ok := fenceOf(line); ok && m == fence && n >= fenceLen && len(info) == 0 && allSpaces(line[countLeadingSpaces(line)+n:]) {
				fence = ""
				continue
			}
			out = append(out, append(plainToks(stText("  "), 0), plainToks(line, styleCode)...))
			continue
		}
		if m, n, info, ok := fenceOf(line); ok {
			fence, fenceLen = m, n
			if len(info) > 0 {
				label := append(plainToks(stText("["), 0), plainToks(info, 0)...)
				out = append(out, append(label, plainToks(stText("]"), 0)...))
			}
			continue
		}
		out = append(out, renderBlockLine(line))
	}
	return out
}

func renderBlockLine(line []safeToken) []styledTok {
	ind := countLeadingSpaces(line)
	rest := line[ind:]
	// heading
	if ind <= 3 {
		h := 0
		for h < len(rest) && h < 7 && rest[h].text == "#" {
			h++
		}
		if h >= 1 && h <= 6 && (h == len(rest) || isSpaceTok(rest[h])) {
			body := trimTrailingSpaces(rest[h:])
			body = body[countLeadingSpaces(body):]
			// optional closing #s
			e := len(body)
			for e > 0 && body[e-1].text == "#" {
				e--
			}
			if e < len(body) && (e == 0 || isSpaceTok(body[e-1])) {
				body = trimTrailingSpaces(body[:e])
			}
			return renderInline(body, styleBold)
		}
	}
	// bullet
	if len(rest) >= 2 && (rest[0].text == "-" || rest[0].text == "*" || rest[0].text == "+") && isSpaceTok(rest[1]) {
		out := plainToks(line[:ind], 0)
		out = append(out, plainToks(stText("• "), 0)...)
		return append(out, renderInline(rest[1+countLeadingSpaces(rest[1:]):], 0)...)
	}
	// ordered list
	d := 0
	for d < len(rest) && len(rest[d].text) == 1 && rest[d].text[0] >= '0' && rest[d].text[0] <= '9' {
		d++
	}
	if d > 0 && d+1 < len(rest) && (rest[d].text == "." || rest[d].text == ")") && isSpaceTok(rest[d+1]) {
		out := plainToks(line[:ind+d+2], 0)
		return append(out, renderInline(rest[d+1+countLeadingSpaces(rest[d+1:]):], 0)...)
	}
	return renderInline(line, 0)
}

// run counts consecutive tokens equal to text from i.
func run(toks []safeToken, i int) int {
	n := 0
	for i+n < len(toks) && toks[i+n].text == toks[i].text {
		n++
	}
	return n
}

// Bounds that keep inline parsing linear in the length of a line: nesting is
// limited and an unmatched opener never rescans what an earlier one covered.
const (
	maxInlineDepth = 8
	maxLinkDest    = 1024
)

func renderInline(toks []safeToken, st mdStyle) []styledTok {
	return renderInlineDepth(toks, st, 0)
}

func isEscapable(t safeToken) bool {
	return len(t.text) == 1 && t.text[0] > ' ' && t.text[0] < 0x7f && !isWordTok(t)
}

// matchBrackets pairs '[' with ']' in one pass; escaped brackets and those in
// code spans are not special to the caller, which skips them itself.
func matchBrackets(toks []safeToken) map[int]int {
	var m map[int]int
	var stack []int
	for i := 0; i < len(toks); i++ {
		switch toks[i].text {
		case "\\":
			i++
		case "[":
			stack = append(stack, i)
		case "]":
			if len(stack) > 0 {
				if m == nil {
					m = map[int]int{}
				}
				m[stack[len(stack)-1]] = i
				stack = stack[:len(stack)-1]
			}
		}
	}
	return m
}

// renderInlineDepth parses inline Markdown over safe tokens: code spans (any
// backtick run length), links with balanced destinations, emphasis and
// backslash escapes.
func renderInlineDepth(toks []safeToken, st mdStyle, depth int) []styledTok {
	var out []styledTok
	var brackets map[int]int
	bracketsDone := false
	codeFailed := map[int]bool{}
	emphFailed := map[string]bool{}
	for i := 0; i < len(toks); {
		t := toks[i]
		switch t.text {
		case "\\":
			if i+1 < len(toks) && isEscapable(toks[i+1]) {
				out = append(out, styledTok{toks[i+1], st})
				i += 2
				continue
			}
		case "`":
			n := run(toks, i)
			if !codeFailed[n] {
				if j := findCodeClose(toks, i+n, n); j >= 0 {
					code := toks[i+n : j]
					if len(code) >= 2 && isSpaceTok(code[0]) && isSpaceTok(code[len(code)-1]) && !allSpaces(code) {
						code = code[1 : len(code)-1]
					}
					out = append(out, plainToks(code, st|styleCode)...)
					i = j + n
					continue
				}
				codeFailed[n] = true
			}
			out = append(out, plainToks(toks[i:i+n], st)...)
			i += n
			continue
		case "[":
			if depth < maxInlineDepth {
				if !bracketsDone {
					brackets, bracketsDone = matchBrackets(toks), true
				}
				if textEnd, destEnd, ok := matchLink(toks, i, brackets); ok {
					out = append(out, renderInlineDepth(toks[i+1:textEnd], st, depth+1)...)
					out = append(out, plainToks(stText(" ("), st)...)
					out = append(out, plainToks(toks[textEnd+2:destEnd], st|styleUnderline)...)
					out = append(out, plainToks(stText(")"), st)...)
					i = destEnd + 1
					continue
				}
			}
		case "*", "_":
			n := run(toks, i)
			key := t.text + string(rune('0'+min(n, 9)))
			if n <= 3 && depth < maxInlineDepth && !emphFailed[key] && openerOK(toks, i, n) {
				if j := findEmphClose(toks, i+n, n); j >= 0 {
					sty := styleItalic
					switch n {
					case 2:
						sty = styleBold
					case 3:
						sty = styleBold | styleItalic
					}
					out = append(out, renderInlineDepth(toks[i+n:j], st|sty, depth+1)...)
					i = j + n
					continue
				}
				emphFailed[key] = true
			}
			out = append(out, plainToks(toks[i:i+n], st)...)
			i += n
			continue
		}
		out = append(out, styledTok{t, st})
		i++
	}
	return out
}

func findCodeClose(toks []safeToken, from, n int) int {
	for j := from; j < len(toks); {
		if toks[j].text != "`" {
			j++
			continue
		}
		m := run(toks, j)
		if m == n {
			return j
		}
		j += m
	}
	return -1
}

// matchLink matches [text](dest) at i, the destination allowing balanced
// parentheses and no whitespace, and bounded in length. It returns the index
// of ']' and of the closing ')'.
func matchLink(toks []safeToken, i int, brackets map[int]int) (textEnd, destEnd int, ok bool) {
	k, found := brackets[i]
	if !found || k == i+1 || k+2 >= len(toks) || toks[k+1].text != "(" {
		return 0, 0, false
	}
	depth := 1
	for j := k + 2; j < len(toks) && j < k+2+maxLinkDest; j++ {
		switch toks[j].text {
		case " ":
			return 0, 0, false
		case "\\":
			j++
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				if j == k+2 {
					return 0, 0, false
				}
				return k, j, true
			}
		}
	}
	return 0, 0, false
}

// openerOK reports whether the delimiter run at i of length n can open
// emphasis: followed by non-space and, for '_', not inside a word.
func openerOK(toks []safeToken, i, n int) bool {
	if i+n >= len(toks) || isSpaceTok(toks[i+n]) {
		return false
	}
	if i > 0 && toks[i-1].text == toks[i].text {
		return false
	}
	if i > 0 && toks[i].text == "_" && isWordTok(toks[i-1]) {
		return false
	}
	return true
}

// findEmphClose finds the closing run: exactly n identical delimiters, or
// three closing a double. A closer is not preceded by space and, for '_', not
// followed by a word character. Escaped delimiters are skipped.
func findEmphClose(toks []safeToken, from, n int) int {
	delim := toks[from-1].text
	for j := from; j < len(toks); {
		if toks[j].text == "\\" {
			j += 2
			continue
		}
		if toks[j].text != delim {
			j++
			continue
		}
		m := run(toks, j)
		if (m == n || (n == 2 && m == 3)) && j > from && !isSpaceTok(toks[j-1]) &&
			(delim != "_" || j+m >= len(toks) || !isWordTok(toks[j+m])) {
			return j
		}
		j += m
	}
	return -1
}
