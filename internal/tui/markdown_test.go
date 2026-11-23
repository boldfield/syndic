package tui

import (
	"strings"
	"testing"

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
		rendered: "  See docs (https://e.io/x) ok",
	},
	{
		name:     "labeled code fence, list, emphasis and inline code",
		in:       "Intro *em* and **b** `c`\n```go\nx := 1\n\ty := 2\n```\n- a\n  more\n- b\n1. first\n2. second",
		raw:      "  Intro *em* and **b** `c`\n  ```go\n  x := 1\n      y := 2\n  ```\n  - a\n    more\n  - b\n  1. first\n  2. second",
		rendered: "  Intro em and b c\n  [go]\n    x := 1\n        y := 2\n  • a\n    more\n  • b\n  1. first\n  2. second",
	},
	{
		name:     "outcome-like Markdown keeps its words and checkbox",
		in:       "**APPROVED**\n- [x] approved",
		raw:      "  **APPROVED**\n  - [x] approved",
		rendered: "  APPROVED\n  • [x] approved",
	},
	{
		name:     "hostile bytes are escaped before rendering",
		in:       "bad \x1b[31m *red* \x1b[0m \xff `c`",
		raw:      "  bad \\x1b[31m *red* \\x1b[0m \\xff `c`",
		rendered: "  bad \\x1b[31m red \\x1b[0m \\xff c",
	},
	{
		name:     "plain brackets and snake_case are untouched",
		in:       "plain [x] snake_case 2*3*4",
		raw:      "  plain [x] snake_case 2*3*4",
		rendered: "  plain [x] snake_case 2*3*4",
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

func TestMarkdownNeverEmitsControls(t *testing.T) {
	for _, c := range goldenCases {
		for _, raw := range []bool{true, false} {
			if out := RenderMarkdown(c.in, 40, raw); strings.ContainsAny(out, "\x1b\r\x00\x07") || strings.Contains(out, "\x9b") {
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
	v := untilView(t, r, "this is bold text")
	if strings.Contains(v, "**bold**") {
		t.Fatalf("markdown shown while rendering:\n%s", v)
	}
	r.typeLine("/raw on")
	untilView(t, r, "this is **bold** text", "raw on")
	r.typeLine("/raw off")
	untilView(t, r, "this is bold text", "raw off")
	r.typeLine("/raw maybe")
	untilView(t, r, "usage: /raw on|off")
}
