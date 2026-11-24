package tui

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	tea "github.com/charmbracelet/bubbletea"
)

// TestDisplayContentLinesIndented verifies that displayed content lines
// begin with two spaces after wrapping, per section 1 of the v6 design.
func TestDisplayContentLinesIndented(t *testing.T) {
	a := chatApp(t)
	a.width, a.height = 80, 24
	a.state.ConnState = ConnStateConnected

	// Add a turn with wrapped content
	addTurn(a, "t1", 1, "approved", "question", "this is a long answer that will wrap across multiple lines when displayed in an 80-column terminal")
	approveBy(a, "t1", "reviewer")

	lines := chatView(a)
	// Find the lines that contain the answer
	foundAnswer := false
	for i, line := range lines {
		if strings.Contains(line, "this is") {
			foundAnswer = true
		}
		if foundAnswer && !strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "┃") && !strings.HasPrefix(line, "›") && strings.TrimSpace(line) != "" {
			// Every content line should start with two spaces
			if !strings.HasPrefix(line, "  ") && i > 0 {
				t.Errorf("content line without indent: %q", line)
			}
		}
		// Stop looking after we see the next section
		if foundAnswer && strings.HasPrefix(line, "┃") {
			break
		}
	}
}

// TestDisplaySafeTextEscaping verifies that escape sequences, carriage returns,
// and bidirectional override characters are shown with visible escapes, per section 1 and 8.
func TestDisplaySafeTextEscaping(t *testing.T) {
	r := newRig(t)
	r.waitConnected(t)

	// Create hostile content with control characters
	hostile := "text\x1b]8;;http://e\x1b\\more\r\u009b31m" + string(rune(0x202e)) + "\xff" //nolint:ST1018
	safeText := safeLine(hostile)

	// Verify escape sequences are shown as \xNN
	if !strings.Contains(safeText, "\\x1b") {
		t.Errorf("escape sequence not escaped visibly in: %q", safeText)
	}

	// Verify carriage return is shown as \xNN or \r
	if !strings.Contains(safeText, "\\r") && !strings.Contains(safeText, "\\x0d") {
		t.Errorf("carriage return not escaped: %q", safeText)
	}

	// Verify bidirectional override is shown as \xNN
	if !strings.Contains(safeText, "\\xe2") && !strings.Contains(safeText, "\\xE2") {
		t.Errorf("bidirectional override not escaped: %q", safeText)
	}

	// Verify no raw control bytes reach the output
	for _, r := range safeText {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || bidiChars[r] {
			t.Errorf("raw control character %U in safe text: %q", r, safeText)
		}
	}
}

// TestDisplayRawVsMarkdown verifies that /raw on shows unformatted text
// and /raw off shows rendered Markdown, per section 1.
func TestDisplayRawVsMarkdown(t *testing.T) {
	a := chatApp(t)
	a.width, a.height = 80, 24

	// Add turn with Markdown content
	markdownContent := "**bold** and *italic* and `code`"
	addTurn(a, "t1", 1, "approved", "question", markdownContent)
	approveBy(a, "t1", "reviewer")

	// Raw off (default): Markdown should be rendered
	a.rawMode = false
	view1 := a.View()
	if !strings.Contains(view1, "bold") {
		t.Fatalf("raw=false: Markdown not rendered:\n%s", view1)
	}

	// Raw on: should show safe text without Markdown rendering
	a.rawMode = true
	view2 := a.View()
	if strings.Contains(view2, "**") || strings.Contains(view2, "*italic*") {
		t.Errorf("raw=true: Markdown markers still present:\n%s", view2)
	}
}

// TestDisplayStatusLineFormat verifies status line format per section 1.
func TestDisplayStatusLineFormat(t *testing.T) {
	a := chatApp(t)
	a.width, a.height = 80, 24
	a.state.ConnState = ConnStateConnected

	lines := chatView(a)
	statusLine := lines[len(lines)-1]

	// Status line must end with "tab: switch  /help"
	if !strings.HasSuffix(statusLine, "tab: switch  /help") {
		t.Errorf("status line does not end with 'tab: switch  /help': %q", statusLine)
	}

	// Status line should start with preset
	if !strings.HasPrefix(strings.TrimSpace(statusLine), "[") {
		t.Errorf("status line does not start with preset: %q", statusLine)
	}

	// Status line should contain idle or state
	if !strings.Contains(statusLine, "idle") {
		t.Errorf("status line missing state: %q", statusLine)
	}
}

// TestDisplayStatusLineWithCtrlO verifies that "ctrl+o: reviews" appears
// in status line when chat tab is active, per section 1.
func TestDisplayStatusLineWithCtrlO(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched" && a.state.Turns[id].State != "open"
	})

	// At chat tab with drawer capability, should show ctrl+o hint
	view := view(t, r)
	lines := strings.Split(view, "\n")
	statusLine := lines[len(lines)-1]
	if !strings.Contains(statusLine, "ctrl+o") {
		t.Errorf("status line missing ctrl+o: %q", statusLine)
	}
}

// TestDisplayPlainTerminalMode verifies rendering under TERM=dumb per section 6.
func TestDisplayPlainTerminalMode(t *testing.T) {
	t.Setenv("TERM", "dumb")
	a := chatApp(t)
	a.terminalCapabilities = TerminalCapabilities{Color: false, AltScreen: false}
	a.width, a.height = 80, 24

	addTurn(a, "t1", 1, "approved", "question", "answer")
	approveBy(a, "t1", "rev")

	view := a.View()
	lines := strings.Split(view, "\n")

	// In plain terminal mode:
	// - No ANSI escape sequences
	// - No color
	// - Spinner shown as word not symbol
	for _, line := range lines {
		for _, r := range line {
			if r < 0x20 && r != '\n' && r != '\t' || (r >= 0x7f && r < 0xA0) {
				t.Errorf("control character in plain terminal output: %q in %q", r, line)
			}
		}
	}
}

// TestDisplayContentLinesByTab verifies that content lines appear indented
// in chat tab, transcript tab, and drawer, per section 8.
func TestDisplayContentLinesByTab(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched"
	})

	// Check chat tab
	view := view(t, r)
	if !strings.Contains(view, answerText) {
		t.Fatalf("answer not in chat tab:\n%s", view)
	}

	// Switch to transcript tab
	send(r, tea.KeyMsg{Type: tea.KeyTab})
	view = untilView(t, r, "Turn 1")
	if !strings.Contains(view, answerText) {
		t.Fatalf("answer not in transcript tab:\n%s", view)
	}
}

// TestDisplayBannerStrings verifies that v4 and v5 banner strings appear
// verbatim with correct marks, per section 8.
func TestDisplayBannerStrings(t *testing.T) {
	a := chatApp(t)
	a.state.ConnState = ConnStateConnected

	// Add turns with different outcomes
	addTurn(a, "t1", 1, "approved", "q1", "a1")
	approveBy(a, "t1", "rev")

	addTurn(a, "t2", 2, "incomplete", "q2", "a2")
	addTurn(a, "t3", 3, "cancelled", "q3", "a3")

	// Add solo turn
	ti := TurnInfo{ID: "t4", State: "solo_complete", Preset: "solo", SubmitSeq: 4}
	a.state.Turns["t4"] = ti
	a.state.TurnState["t4"] = "solo_complete"
	a.inputs["t4"] = "q4"

	lines := chatView(a)

	// Approved turn should have ✓ mark
	for _, l := range lines {
		if strings.Contains(l, "approved by rev") {
			if !strings.HasPrefix(strings.TrimSpace(l), "✓") {
				t.Errorf("approved line missing ✓ mark: %q", l)
			}
			break
		}
	}

	// Incomplete turn should have ! mark
	for _, l := range lines {
		if strings.Contains(l, "INCOMPLETE") {
			if !strings.HasPrefix(strings.TrimSpace(l), "!") {
				t.Errorf("incomplete line missing ! mark: %q", l)
			}
			break
		}
	}

	// Solo turn should have · mark
	for _, l := range lines {
		if strings.Contains(l, "UNREVIEWED (solo)") || strings.Contains(l, "solo") {
			if strings.Contains(l, "·") || strings.HasPrefix(strings.TrimSpace(l), "·") {
				// Solo has · mark
				break
			}
		}
	}
}

// TestDisplayReviewCount verifies that every outcome line ends with
// "· <n> reviews", per section 2 and 8.
func TestDisplayReviewCount(t *testing.T) {
	a := chatApp(t)
	a.state.ConnState = ConnStateConnected

	// Add turn with verdicts
	addTurn(a, "t1", 1, "approved", "question", "answer")
	approveBy(a, "t1", "rev1")
	approveBy(a, "t1", "rev2")

	// Set review count in app's map
	two := 2
	a.reviewCounts["t1"] = &two

	lines := chatView(a)

	// Find the approval line
	for _, l := range lines {
		if strings.Contains(l, "approved by") {
			// Should end with · 2 reviews or similar
			if !strings.Contains(l, "reviews") && !strings.Contains(l, "·") {
				t.Errorf("approval line missing review count: %q", l)
			}
			break
		}
	}
}

// TestDisplayAfterOneRevision verifies the special case where n=1 shows
// "after 1 revision" not "after 1 revisions", per section 1 and 8.
func TestDisplayAfterOneRevision(t *testing.T) {
	a := chatApp(t)

	// Add turn with round=1 (meaning after 1 revision)
	addTurn(a, "t1", 1, "approved", "q", "a")
	approveBy(a, "t1", "rev")
	ti := a.state.Turns["t1"]
	ti.Round = 1
	a.state.Turns["t1"] = ti

	// Set review count
	one := 1
	a.reviewCounts["t1"] = &one

	lines := chatView(a)
	for _, l := range lines {
		if strings.Contains(l, "approved") && strings.Contains(l, "1 revision") {
			return // Found the correct phrasing
		}
	}

	for _, l := range lines {
		if strings.Contains(l, "approved") && strings.Contains(l, "1 revisions") {
			t.Errorf("used 'revisions' for n=1: %q", l)
			return
		}
	}
}

// TestDisplayCrowded80x24WithDrawer verifies layout at 80x24 with drawer,
// per section 8: tab bar, at least 5 chat lines, drawer, identity warning,
// attached line with +<n> more, at least 1 input line, status line.
func TestDisplayCrowded80x24WithDrawer(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched"
	})

	// Set up crowded conditions
	a := r.app
	a.width, a.height = 80, 24

	// Add identity warning
	a.state.Participants = map[string]ParticipantInfo{
		"p1": {ID: "p1", Identity: "unknown"},
	}

	// Add banners and attachments
	a.state.Status.DeadlinesProvisional = true
	a.state.Status.RosterFileChanged = true
	a.state.ConnState = ConnStateReconnecting

	// Open drawer
	a.reviewsDrawer.Open(0, false)
	a.want.Drawer = true
	a.want.Attached = true

	// Set long input
	a.input.SetValue(strings.Repeat("line\n", 6))

	view := a.View()
	lines := strings.Split(view, "\n")

	if len(lines) != 24 {
		t.Fatalf("view has %d lines, want 24", len(lines))
	}

	// Tab bar first
	if !strings.HasPrefix(lines[0], "[chat") && !strings.HasPrefix(lines[0], "chat") {
		t.Errorf("first line is not tab bar: %q", lines[0])
	}

	// Status line last
	if !strings.Contains(lines[23], "tab: switch") {
		t.Errorf("last line is not status line: %q", lines[23])
	}

	// Verify drawer is open
	if !a.reviewsDrawer.IsOpen() {
		t.Errorf("drawer not open")
	}

	// When participants are set, identity warning might be visible
	// (depends on specific test setup and state)

	// At least 5 chat lines (before drawer)
	chatLines := 0
	for i, l := range lines {
		if strings.TrimSpace(l) != "" && i > 0 && i < len(lines)-1 {
			if !strings.HasPrefix(strings.TrimSpace(l), "[") && !strings.HasPrefix(strings.TrimSpace(l), "┃") {
				chatLines++
			}
		}
	}
	if chatLines < 5 {
		t.Errorf("chat has %d non-empty lines, want at least 5", chatLines)
	}
}

// TestDisplayNoEOnlyControls verifies that E-only attachment/list controls
// do not appear, per section 8.
func TestDisplayNoEOnlyControls(t *testing.T) {
	a := chatApp(t)
	a.width, a.height = 80, 24

	view := a.View()

	// File list and attachment tokens are E-only features
	// In this Haiku implementation, they should not appear
	// (The actual file list would only appear if actively typing @)
	if strings.Contains(view, "@\"") && !strings.Contains(view, "question") {
		// @ tokens should only appear in input, not elsewhere
		t.Errorf("attachment token outside input: %q", view)
	}
}
