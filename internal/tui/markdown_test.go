package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/boldfield/syndic/internal/engine/enginetest"
)

// goldenCase is one input with its exact output in both modes at width 40.
type goldenCase struct {
	name     string
	in       string
	raw      string
	rendered string
}

var goldenCases = []goldenCase{
	{
		name:     "link shows its URL, no hyperlink escape",
		in:       "See [docs](https://e.io/x) ok",
		raw:      "  See [docs](https://e.io/x) ok",
		rendered: "  See docs (\x1b[4mhttps://e.io/x\x1b[0m) ok",
	},
	{
		name:     "labeled code fence, list, emphasis and inline code",
		in:       "Intro *em* and **b** `c`\n```go\nx := 1\n\ty := 2\n```\n- a\n  more\n- b\n1. first\n2. second",
		raw:      "  Intro *em* and **b** `c`\n  ```go\n  x := 1\n      y := 2\n  ```\n  - a\n    more\n  - b\n  1. first\n  2. second",
		rendered: "  Intro \x1b[3mem\x1b[0m and \x1b[1mb\x1b[0m \x1b[36mc\x1b[0m\n  [go]\n    \x1b[36mx := 1\x1b[0m\n    \x1b[36m    y := 2\x1b[0m\n  • a\n    more\n  • b\n  1. first\n  2. second",
	},
	{
		name:     "outcome-like Markdown keeps its words and checkbox",
		in:       "**APPROVED**\n- [x] approved",
		raw:      "  **APPROVED**\n  - [x] approved",
		rendered: "  \x1b[1mAPPROVED\x1b[0m\n  • [x] approved",
	},
	{
		name:     "hostile bytes are escaped before rendering",
		in:       "bad \x1b[31m *red* \x1b[0m \xff `c`",
		raw:      "  bad \\x1b[31m *red* \\x1b[0m \\xff `c`",
		rendered: "  bad \\x1b[31m \x1b[3mred\x1b[0m \\x1b[0m \\xff \x1b[36mc\x1b[0m",
	},
	{
		name:     "plain brackets and snake_case are untouched",
		in:       "plain [x] snake_case 2*3*4",
		raw:      "  plain [x] snake_case 2*3*4",
		rendered: "  plain [x] snake_case 2\x1b[3m3\x1b[0m4",
	},
	{
		name:     "underscore runs inside identifiers are not emphasis",
		in:       "MAX__SIZE and foo__bar__baz a__b**c",
		raw:      "  MAX__SIZE and foo__bar__baz a__b**c",
		rendered: "  MAX__SIZE and foo__bar__baz a__b**c",
	},
	{
		name:     "long code delimiter and balanced link destination",
		in:       "``a ` b`` [p](https://e.io/a_(b))",
		raw:      "  ``a ` b`` [p](https://e.io/a_(b))",
		rendered: "  \x1b[36ma ` b\x1b[0m p (\x1b[4mhttps://e.io/a_(b)\x1b[0m)",
	},
	{
		name:     "closing fence cannot carry an info string",
		in:       "```\nfoo\n```bash\nbar\n```",
		raw:      "  ```\n  foo\n  ```bash\n  bar\n  ```",
		rendered: "    \x1b[36mfoo\x1b[0m\n    \x1b[36m```bash\x1b[0m\n    \x1b[36mbar\x1b[0m",
	},
	{
		name:     "ordered and heading",
		in:       "# Title #\n1. one",
		raw:      "  # Title #\n  1. one",
		rendered: "  \x1b[1mTitle\x1b[0m\n  1. one",
	},
}

func TestGoldenMarkdown(t *testing.T) {
	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			if got := RenderMarkdown(c.in, 40, true); got != c.raw {
				t.Errorf("raw:\n got %q\nwant %q", got, c.raw)
			}
			if got := RenderMarkdown(c.in, 40, false); got != c.rendered {
				t.Errorf("rendered:\n got %q\nwant %q", got, c.rendered)
			}
		})
	}
}

// sgrRe matches the only escapes the renderer may emit: TUI-owned styling.
var sgrRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestMarkdownNeverEmitsControls(t *testing.T) {
	for _, c := range goldenCases {
		for _, raw := range []bool{true, false} {
			out := sgrRe.ReplaceAllString(RenderMarkdown(c.in, 40, raw), "")
			if strings.ContainsAny(out, "\x1b\r\x00\x07") || strings.Contains(out, "\x9b") {
				t.Errorf("%s raw=%v: control in %q", c.name, raw, out)
			}
		}
	}
}

func TestMarkdownPlainModeIsRaw(t *testing.T) {
	in := "**b** and `c`"
	if got, want := renderAnswerMarkdown(in, 40, false, true), RenderMarkdown(in, 40, true); got != want {
		t.Fatalf("plain mode = %q, want raw %q", got, want)
	}
}

func TestMarkdownNoWidth(t *testing.T) {
	if got := RenderMarkdown("**b**", 0, false); got != "**b**" {
		t.Fatalf("got %q", got)
	}
}

// TestRawCommandChangesView drives /raw through the App and reads the View.
func TestRawCommandChangesView(t *testing.T) {
	const md = "this is **bold** text"
	h := scripted(t, enginetest.Reply(md), approve())
	r := startAnswerRig(t, h)
	r.until(t, "color on", func(a *App) (string, bool) { a.terminalCapabilities.Color = true; return "", true })
	r.typeLine("question")
	turnIn(t, r, "approved")
	v := untilView(t, r, "this is "+"\x1b[1mbold\x1b[0m"+" text")
	if strings.Contains(v, "**bold**") {
		t.Fatalf("markdown shown while rendering:\n%s", v)
	}
	r.typeLine("/raw on")
	untilView(t, r, "this is **bold** text", "raw on")
	r.typeLine("/raw off")
	untilView(t, r, "this is \x1b[1mbold\x1b[0m text", "raw off")
	r.typeLine("/raw maybe")
	untilView(t, r, "usage: /raw on|off")
}

func TestMarkdownEscapeStaysAtomicWhenWrapped(t *testing.T) {
	for _, raw := range []bool{true, false} {
		out := RenderMarkdown("abc \x1b[31m", 8, raw)
		if strings.Contains(out, "\\x1\n") || strings.Contains(out, "\\\n") {
			t.Errorf("raw=%v: escape split across lines: %q", raw, out)
		}
	}
}

func TestMarkdownEscapesAndDelimiterRuns(t *testing.T) {
	cases := []struct{ in, want string }{
		{`\*literal*`, "  *literal*"},
		{`a \[x](y) b`, "  a [x](y) b"},
		{`*a\*b*`, "  \x1b[3ma*b\x1b[0m"},
		{`foo*bar*`, "  foo\x1b[3mbar\x1b[0m"},
		{`**baz***`, "  \x1b[1mbaz\x1b[0m*"},
		{`***x***`, "  \x1b[1;3mx\x1b[0m"},
		{`snake_case_name`, "  snake_case_name"},
		{`path\to`, `  path\to`},
	}
	for _, c := range cases {
		if got := RenderMarkdown(c.in, 40, false); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestMarkdownFenceLabelKeepsSafeTokens(t *testing.T) {
	out := RenderMarkdown("```ab\x1b\n```", 8, false)
	if want := "  [ab\\x1b]"; !strings.HasPrefix(out, "  [ab") || strings.Contains(out, "\\x1\n") {
		t.Errorf("label split or altered (want like %q): %q", want, out)
	}
	for _, line := range strings.Split(RenderMarkdown("```日本語\nx\n```", 8, false), "\n") {
		if w := runewidth.StringWidth(sgrRe.ReplaceAllString(line, "")); w > 8 {
			t.Errorf("line %q is %d cells wide, over 8", line, w)
		}
	}
}

func TestMarkdownPathologicalInputIsLinear(t *testing.T) {
	for _, unit := range []string{"[", "*a ", "`a ", "[a](", "_a ", "see arr[i and x *y here. "} {
		in := strings.Repeat(unit, 65536/len(unit))
		start := time.Now()
		RenderMarkdown(in, 80, false)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%q: rendering 64 KiB took %v", unit, d)
		}
	}
}
