package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// These tests are the v6 section 8 display acceptance scenarios. They build
// an App the way the other chat tests do, drive it through Update and read
// what View draws. No model endpoint is involved. The imported reference
// frames under testdata/v6 are illustrations only; the golden frames compared
// here are App.View output.

var updateGolden = flag.Bool("update-v6-golden", false, "rewrite the v6 golden frames in testdata/v6/golden")

// asciiProfile makes View deterministic: no colour sequences whatever the
// environment of the test process.
func asciiProfile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// assertGolden compares a frame with testdata/v6/golden/<name>.txt, trailing
// spaces trimmed from every line so the files stay readable.
func assertGolden(t *testing.T, name, frame string) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", "v6", "golden", name+".txt")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden frame %s: %v (run with -update-v6-golden to write it)", path, err)
	}
	if string(want) != got {
		t.Errorf("frame differs from %s (run with -update-v6-golden to accept):\n--- got ---\n%s--- want ---\n%s", path, got, want)
	}
}

// onlyControlFree fails for any byte the terminal could act on.
func onlyControlFree(t *testing.T, what, s string) {
	t.Helper()
	for _, r := range ansi.Strip(s) {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			t.Errorf("%s holds control or bidi rune %U", what, r)
		}
	}
}

// openDrawerSettled presses ctrl+o and lets the slide finish, as the frame
// ticks would, so View shows the whole drawer.
func openDrawerSettled(t *testing.T, a *App) {
	t.Helper()
	press(a, tea.KeyMsg{Type: tea.KeyCtrlO})
	if !a.reviewsDrawer.IsOpen() {
		t.Fatal("ctrl+o did not open the drawer")
	}
	a.reviewsDrawer.animating = false
}

// outcomeApp is a chat holding one turn in the state, with the candidate
// shown, two approvals and the review count the engine reported.
func outcomeApp(t *testing.T, state string, round int, candidate bool, reviews *int, reason string) *App {
	t.Helper()
	asciiProfile(t)
	a := chatApp(t)
	text := ""
	if candidate {
		text = "the candidate text"
	}
	addTurn(a, "t1", 1, state, "the question", text)
	ti := a.state.Turns["t1"]
	ti.Round = round
	if reason != "" {
		ti.TerminalReason = &reason
	}
	a.state.Turns["t1"] = ti
	approveBy(a, "t1", "rev")
	a.reviewCounts["t1"] = reviews
	return a
}

// Every inherited outcome word, with its mark in column 0 and the exact
// "· <n> reviews" suffix, as View draws it.
func TestV6OutcomeWordsMarksAndReviewCount(t *testing.T) {
	cases := []struct {
		name      string
		state     string
		round     int
		candidate bool
		reviews   *int
		reason    string
		want      string
		absent    []string
	}{
		{"approved", "approved", 0, true, ptr(2), "", "✓ approved by rev against abcdef123456 · 2 reviews", []string{"revision"}},
		{"one revision", "approved", 1, true, ptr(3), "", "✓ approved by rev after 1 revision against abcdef123456 · 3 reviews", []string{"1 revisions"}},
		{"two revisions", "approved", 2, true, ptr(4), "", "✓ approved by rev after 2 revisions against abcdef123456 · 4 reviews", nil},
		{"incomplete", "incomplete", 0, true, ptr(1), "rounds_exhausted", "! INCOMPLETE, not approved · 1 reviews", []string{"✓"}},
		{"override", "overridden", 0, true, ptr(2), "good enough", "! HUMAN OVERRIDE: good enough · 2 reviews", []string{"INCOMPLETE"}},
		{"cancelled", "cancelled", 0, true, ptr(0), "", "× CANCELLED, not approved · 0 reviews", []string{"✓"}},
		{"cancelled early", "cancelled", 0, false, ptr(0), "", "× cancelled before candidate · 0 reviews", nil},
		{"solo", "solo_complete", 0, true, nil, "", "· UNREVIEWED (solo)", []string{"✓"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := outcomeApp(t, c.state, c.round, c.candidate, c.reviews, c.reason)
			v := a.View()
			lines := strings.Split(v, "\n")
			n := 0
			for _, l := range lines {
				if strings.TrimRight(l, " ") == c.want {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("want exactly one line %q in column 0, found %d:\n%s", c.want, n, v)
			}
			for _, bad := range c.absent {
				if strings.Contains(v, bad) {
					t.Errorf("view holds %q:\n%s", bad, v)
				}
			}
			// The one-character mark and a space begin the outcome line and
			// no other line of the turn begins with an outcome mark.
			marks := 0
			for _, l := range lines {
				if strings.HasPrefix(l, "✓ ") || strings.HasPrefix(l, "! ") || strings.HasPrefix(l, "× ") || strings.HasPrefix(l, "· ") {
					marks++
				}
			}
			if marks != 1 {
				t.Errorf("%d lines begin with an outcome mark, want 1:\n%s", marks, v)
			}
		})
	}
}

// A running turn with drafts on shows the unreviewed draft banner with its
// mark, in colour and in a plain terminal.
func TestV6UnreviewedDraftBanner(t *testing.T) {
	asciiProfile(t)
	for _, plain := range []bool{false, true} {
		caps := TerminalCapabilities{Color: true, AltScreen: true}
		want := "· UNREVIEWED DRAFT"
		if plain {
			caps = TerminalCapabilities{}
			want = "· !! UNREVIEWED DRAFT !!"
		}
		a := runningApp(t, caps, 80, 24)
		a.drafts = ptr(true)
		a.state.Attempts["au"] = AttemptInfo{ID: "au", TurnID: "t1", Phase: "author", Status: "complete"}
		a.content.Store("attempt:au", "draft body", nil)
		v := a.View()
		found := false
		for _, l := range strings.Split(v, "\n") {
			if strings.TrimRight(l, " ") == want {
				found = true
			}
		}
		if !found || !strings.Contains(v, "draft body") {
			t.Errorf("plain=%v: no %q line with the draft:\n%s", plain, want, v)
		}
	}
}

// Hostile content, wrapped long and short, in raw and Markdown mode: no
// control or bidi rune reaches the output, each is shown as a visible escape,
// every line fits, and no content line loses its two-space indent.
func TestV6AdversarialContentThroughChat(t *testing.T) {
	asciiProfile(t)
	hostile := "start \x1b[31mred\x1b[0m \x1b]8;;http://evil\x1b\\link\r\u202eRTL\u2066iso\u0085c1 \x00nul\x7fdel " +
		strings.Repeat("long", 40) + "TAIL\n\n**bold** `code`\n\ttabbed"
	for _, raw := range []bool{false, true} {
		a := chatApp(t)
		a.rawMode = raw
		addTurn(a, "t1", 1, "approved", "user \x1b[2Jinput\u202e with \x1b]0;title\x07", hostile)
		approveBy(a, "t1", "rev\x1b[1m\u202ex")
		a.reviewCounts["t1"] = ptr(1)
		for _, width := range []int{80, 100} {
			a.Update(tea.WindowSizeMsg{Width: width, Height: 40})
			v := a.View()
			onlyControlFree(t, "chat", v)
			for _, want := range []string{`\x1b`, `\x0d`, `\u202e`, `\u2066`, `\xc2\x85`, `\x00`, `\x7f`, "TAIL"} {
				if want == `\xc2\x85` {
					want = `\x`
				}
				if !strings.Contains(v, want) {
					t.Errorf("raw=%v width=%d: view lacks %q:\n%s", raw, width, want, v)
				}
			}
			for i, l := range strings.Split(v, "\n") {
				if w := lipgloss.Width(l); w > width {
					t.Errorf("raw=%v width=%d: line %d is %d cells: %q", raw, width, i, w, l)
				}
			}
			markdown := strings.Contains(v, "**bold**")
			if markdown != raw {
				t.Errorf("raw=%v: Markdown markers shown = %v:\n%s", raw, markdown, v)
			}
			// Content lines carry the two-space indent: the wrapped tail of
			// the long word is on an indented line, not in column 0.
			for _, l := range strings.Split(v, "\n") {
				if strings.Contains(l, "TAIL") && !strings.HasPrefix(l, "  ") {
					t.Errorf("raw=%v width=%d: wrapped content line lost its indent: %q", raw, width, l)
				}
			}
		}
	}
}

// Markdown on shows rendered text without markers; /raw on shows the source.
func TestV6RawAndMarkdownThroughCommands(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "**bold** and `code`")
	approveBy(a, "t1", "rev")
	v := a.View()
	if !strings.Contains(v, "bold") || !strings.Contains(v, "code") {
		t.Fatalf("rendered view lacks the text:\n%s", v)
	}
	if strings.Contains(v, "**") || strings.Contains(v, "`") {
		t.Errorf("Markdown markers remain with raw off:\n%s", v)
	}
	typeRunes(a, "/raw on")
	press(a, tea.KeyMsg{Type: tea.KeyEnter})
	v = a.View()
	if !strings.Contains(v, "**bold** and `code`") {
		t.Errorf("raw on does not show the source:\n%s", v)
	}
	typeRunes(a, "/raw off")
	press(a, tea.KeyMsg{Type: tea.KeyEnter})
	if v = a.View(); strings.Contains(v, "**") || strings.Contains(v, "`") {
		t.Errorf("raw off did not render again:\n%s", v)
	}
}

// The transcript and drawer show hostile reviewer text safely too.
func TestV6AdversarialContentInDrawer(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "incomplete", "q", "answer")
	a.reviewsDrawer.SetTurns([]string{"t1"})
	a.content.Store("c-hostile", "note \x1b[2J\u202e gone\rback "+strings.Repeat("w", 120)+"END", nil)
	a.Update(reviewsMsg{turnID: "t1", items: []wire.ReviewItem{{
		Round: 0, Participant: "rev\x1b[31m\u202e", Kind: "block\x1b",
		Blocks:     []wire.ReviewBlock{{BlockID: "b\x1b1", State: "opened\u2067", Evidence: &wire.ContentReference{Ref: "c-hostile"}}},
		Commentary: &wire.ContentReference{Ref: "c-hostile"},
	}}})
	openDrawerSettled(t, a)
	v := a.View()
	onlyControlFree(t, "drawer", v)
	for _, want := range []string{`\x1b`, `\u202e`, `\u2067`, "END"} {
		if !strings.Contains(v, want) {
			t.Errorf("drawer lacks %q:\n%s", want, v)
		}
	}
	for i, l := range strings.Split(v, "\n") {
		if w := lipgloss.Width(l); w > 80 {
			t.Errorf("line %d is %d cells: %q", i, w, l)
		}
	}
}

// The identity banner, folded banners, drawer, reserved attached line, a
// six-line input and the status line fit 80x24 exactly, and the frame is the
// golden one. The attached line stays empty: attachments are the E side's.
func TestV6Crowded80x24GoldenFrame(t *testing.T) {
	asciiProfile(t)
	a := crowded(t, 80, 24)
	a.workDir = "/work/space"
	addTurn(a, "t1", 1, "incomplete", "which flag turns on the race detector?", "Pass -race to go test.")
	approveBy(a, "t1", "alpha")
	a.reviewCounts["t1"] = ptr(2)
	a.state.CurrentTurn = ""
	a.reviewsDrawer.SetTurns([]string{"t1"})
	a.reviewsDrawer.SetRoster([]string{"alpha"})
	a.Update(reviewsMsg{turnID: "t1", items: []wire.ReviewItem{{
		Round: 0, Participant: "alpha", Kind: "block",
		Blocks: []wire.ReviewBlock{{BlockID: "b1", State: "opened"}},
	}}})
	openDrawerSettled(t, a)
	a.want.Attached = true
	v := a.View()
	lines := strings.Split(v, "\n")
	if len(lines) != 24 {
		t.Fatalf("frame has %d lines, want 24:\n%s", len(lines), v)
	}
	at := lineOf(t, lines, "identity: unknown(name-matched)")
	if lines[at+1] != "+4 more" {
		t.Errorf("second banner line = %q, want +4 more", lines[at+1])
	}
	if !strings.HasPrefix(lines[0], "[chat]") || !strings.Contains(lines[23], "tab: switch") {
		t.Errorf("tab bar %q, status line %q", lines[0], lines[23])
	}
	drawerAt := lineOf(t, lines, "Reviews · Turn 1")
	if drawerAt <= at+1 {
		t.Errorf("drawer at line %d is not below the banners (line %d)", drawerAt, at)
	}
	if !strings.Contains(v, "alpha (block)") || !strings.Contains(v, "b1: opened") {
		t.Errorf("drawer lacks the review:\n%s", v)
	}
	body := at - 1
	if body < minBodyLines {
		t.Errorf("chat keeps %d lines, want at least %d", body, minBodyLines)
	}
	if !strings.Contains(lines[1], "which flag") && !strings.Contains(v, "INCOMPLETE") {
		t.Errorf("chat lost its turn:\n%s", v)
	}
	input := strings.Join(lines[len(lines)-1-maxInputViewport:len(lines)-1], "\n")
	if !strings.Contains(input, "i8") || strings.Contains(input, "i1\n") {
		t.Errorf("input is not scrolled to its cursor:\n%s", input)
	}
	assertGolden(t, "crowded-80x24-drawer", v)
}

// A plain terminal at 80x24: no colour, no spinner, state words and ASCII
// rules, the input prompt unchanged.
func TestV6PlainTerminalGoldenFrame(t *testing.T) {
	asciiProfile(t)
	a := runningApp(t, TerminalCapabilities{}, 80, 24)
	a.workDir = "/work/space"
	a.input.SetValue("next question")
	v := a.View()
	if ansi.Strip(v) != v {
		t.Errorf("plain frame holds escape sequences: %q", v)
	}
	for _, want := range []string{"running r2", "round 1/3", "reviewers_dispatched turn in progress", "100 / 1000", a.input.Prompt + "next question"} {
		if !strings.Contains(v, want) {
			t.Errorf("plain frame lacks %q:\n%s", want, v)
		}
	}
	for _, bad := range []string{"●", "○", "█", "░", "⋯", "⣾", "⣽"} {
		if strings.Contains(v, bad) {
			t.Errorf("plain frame holds %q:\n%s", bad, v)
		}
	}
	assertFits(t, "plain 80x24", v, 80, 24)
	assertGolden(t, "plain-80x24", v)

	// The same state with colour keeps the decoration the plain one drops.
	c := runningApp(t, TerminalCapabilities{Color: true, AltScreen: true}, 80, 24)
	if cv := c.View(); !strings.Contains(cv, "●") || cv == v {
		t.Errorf("colour frame is not decorated:\n%s", cv)
	}
}

// Chat at idle with every outcome, 100x30, as a golden frame.
func TestV6OutcomesGoldenFrame(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	a.workDir = "/work/space"
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	specs := []struct {
		id, state, input, text string
		round                  int
		reviews                *int
		reason                 string
	}{
		{"t1", "approved", "make the retry loop stop on cancel", "The loop now checks ctx.Done() first.", 2, ptr(6), ""},
		{"t2", "incomplete", "drop the second repair attempt?", "Yes, it is unreachable.", 0, ptr(2), "rounds_exhausted"},
		{"t3", "solo_complete", "which flag enables the race detector?", "Pass -race to go test.", 0, nil, ""},
		{"t4", "overridden", "rewrite the README section", "Scheduler starts after recovery.", 0, ptr(1), "covered by TestRepairOnce"},
		{"t5", "cancelled", "explain why detach never cancels", "The engine is a separate process.", 0, ptr(0), ""},
	}
	for i, s := range specs {
		addTurn(a, s.id, int64(i+1), s.state, s.input, s.text)
		ti := a.state.Turns[s.id]
		ti.Round = s.round
		if s.reason != "" {
			ti.TerminalReason = &s.reason
		}
		a.state.Turns[s.id] = ti
		approveBy(a, s.id, "codex")
		a.reviewCounts[s.id] = s.reviews
	}
	v := a.View()
	for _, want := range []string{
		"✓ approved by codex after 2 revisions against abcdef123456 · 6 reviews",
		"! INCOMPLETE, not approved · 2 reviews",
		"· UNREVIEWED (solo)",
		"! HUMAN OVERRIDE: covered by TestRepairOnce · 1 reviews",
		"× CANCELLED, not approved · 0 reviews",
	} {
		if !strings.Contains(v, "\n"+want) {
			t.Errorf("frame lacks the outcome line %q in column 0:\n%s", want, v)
		}
	}
	assertGolden(t, "outcomes-100x30", v)
}

// Nothing of the E side shows: no attachment or file-list control in the
// tab bar, commands, status line or help, and an @ word is ordinary text.
func TestV6NoEOnlyControls(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "look at @internal/x.go", "ok")
	approveBy(a, "t1", "rev")
	typeRunes(a, "see @int")
	press(a, tea.KeyMsg{Type: tea.KeyTab})
	v := a.View()
	for _, bad := range []string{"files matching", "attached ", "attach", "changes", "/apply", "Apply", "Discard"} {
		if strings.Contains(strings.ToLower(v), strings.ToLower(bad)) {
			t.Errorf("view holds the E-only text %q:\n%s", bad, v)
		}
	}
	if got := a.input.Value(); !strings.Contains(got, "see @int") || strings.Contains(got, "\x00") {
		t.Errorf("an @ word was altered: %q", got)
	}
	if a.want.Attached {
		t.Error("the attached line is reserved without any attachment")
	}
	for _, e := range commandEntries() {
		for _, bad := range []string{"apply", "attach", "changes", "file"} {
			if strings.Contains(e.name, bad) || (bad != "file" && strings.Contains(strings.ToLower(e.desc), bad)) {
				t.Errorf("/%s (%s) is an E-only command", e.name, e.desc)
			}
		}
	}
	if _, ok := commands["apply"]; ok {
		t.Error("/apply is registered")
	}
	for _, tabName := range []tab{tabChat, tabTranscript, tabParticipants} {
		a.tab = tabName
		if bar := a.tabBar(); strings.Contains(bar, "changes") {
			t.Errorf("tab bar holds the changes tab: %q", bar)
		}
	}
	// Tab with the cursor in an @ word switches tabs; it attaches nothing.
	before := a.tab
	a.tab = tabChat
	typeRunes(a, " @")
	press(a, tea.KeyMsg{Type: tea.KeyTab})
	if a.tab == tabChat && before == tabChat {
		t.Error("Tab did not switch tabs")
	}
	if strings.Contains(a.View(), "attached") {
		t.Error("an attached line appeared")
	}
}
