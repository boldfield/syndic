package tui

import (
	"regexp"
	"strings"
)

// RenderMarkdown renders answer text at the given terminal width, with
// Markdown unless raw is true.
func RenderMarkdown(text string, width int, raw bool) string {
	return renderAnswerMarkdown(text, width, raw, false)
}

// renderAnswerMarkdown renders answer text for the chat tab. Raw and plain
// (no-colour) modes show unformatted safe text. Otherwise the text is first
// converted to safe text, then Markdown is rendered from that safe text into
// plain lines without any terminal escape, and the result goes through the
// same wrap-and-indent boundary as raw mode, so both use the same final
// content indentation. Escaped text is idempotent under that boundary.
func renderAnswerMarkdown(text string, width int, raw bool, noColor bool) string {
	if width <= 0 {
		return ToSafeText([]byte(text))
	}
	if !raw && !noColor {
		text = renderMarkdownLines(ToSafeText([]byte(text)))
	}
	return strings.Join(SafeTextWithPrefix(text, width), "\n")
}

var (
	fenceRe      = regexp.MustCompile("^\\s*(```|~~~)\\s*(\\S*)")
	headingRe    = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	bulletRe     = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	orderedRe    = regexp.MustCompile(`^(\s*)(\d+[.)])\s+(.*)$`)
	inlineCodeRe = regexp.MustCompile("`([^`]+)`")
	linkRe       = regexp.MustCompile(`\[([^\[\]]+)\]\(([^()\s]+)\)`)
	boldRe       = regexp.MustCompile(`(\*\*|__)(\S|\S[^\n]*?\S)(\*\*|__)`)
	italicStarRe = regexp.MustCompile(`(^|[^\w*])\*(\S|[^\s*][^*\n]*?[^\s*])\*([^\w*]|$)`)
	italicUndRe  = regexp.MustCompile(`(^|[^\w_])_(\S|[^\s_][^_\n]*?[^\s_])_([^\w_]|$)`)
)

// renderMarkdownLines turns safe text into display lines: lists get bullets,
// headings and emphasis lose their markers, links show their URL, inline
// code loses its backticks and a fenced block shows its language label above
// the code, indented. Nothing here emits an escape sequence.
func renderMarkdownLines(safe string) string {
	var out []string
	inFence := false
	for _, line := range strings.Split(safe, "\n") {
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			if inFence {
				inFence = false
				continue
			}
			inFence = true
			if m[2] != "" {
				out = append(out, "["+m[2]+"]")
			}
			continue
		}
		if inFence {
			out = append(out, "  "+line)
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			out = append(out, renderInline(m[1]))
		} else if m := bulletRe.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+"• "+renderInline(m[2]))
		} else if m := orderedRe.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+m[2]+" "+renderInline(m[3]))
		} else {
			out = append(out, renderInline(line))
		}
	}
	return strings.Join(out, "\n")
}

// renderInline renders inline Markdown of one line. Code spans are kept
// verbatim, minus their backticks; everything between them is rendered.
func renderInline(line string) string {
	var b strings.Builder
	last := 0
	for _, loc := range inlineCodeRe.FindAllStringSubmatchIndex(line, -1) {
		b.WriteString(renderSpans(line[last:loc[0]]))
		b.WriteString(line[loc[2]:loc[3]])
		last = loc[1]
	}
	b.WriteString(renderSpans(line[last:]))
	return b.String()
}

func renderSpans(s string) string {
	s = linkRe.ReplaceAllString(s, "$1 ($2)")
	s = boldRe.ReplaceAllString(s, "$2")
	s = italicStarRe.ReplaceAllString(s, "$1$2$3")
	s = italicUndRe.ReplaceAllString(s, "$1$2$3")
	return s
}
