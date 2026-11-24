package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// offlineApp is an App at 80×24 that has not connected: keys and commands
// that send nothing can be driven without an engine.
func offlineApp(t *testing.T, opts ...Option) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := NewApp(ctx, "/nonexistent/engine.sock", append([]Option{WithWorkDir(t.TempDir())}, opts...)...)
	a.Update(resizeMsg(80, 24))
	return a
}

func press(a *App, msg tea.KeyMsg) tea.Cmd {
	_, cmd := a.update(msg)
	return cmd
}

func typeRunes(a *App, s string) { press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }

var altEnter = tea.KeyMsg{Type: tea.KeyEnter, Alt: true}

// isQuit reports whether cmd ends the program.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// inputRegion is the lines of the view between the refusal line and the status line.
func inputRegion(t *testing.T, a *App) []string {
	t.Helper()
	lines := strings.Split(ansi.Strip(a.View()), "\n")
	if len(lines) != a.height {
		t.Fatalf("view has %d lines, want %d:\n%s", len(lines), a.height, strings.Join(lines, "\n"))
	}
	rows := a.inputRows()
	out := lines[len(lines)-1-rows : len(lines)-1]
	for i, l := range out {
		out[i] = strings.TrimRight(l, " ")
	}
	return out
}

func TestAltEnterInsertsNewline(t *testing.T) {
	a := offlineApp(t)
	typeRunes(a, "héllo")
	press(a, altEnter)
	typeRunes(a, "wörld")
	if got, want := a.input.Value(), "héllo\nwörld"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if got, want := inputRegion(t, a), []string{defaultPrompt + "héllo", "  wörld"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("input lines = %q, want %q", got, want)
	}

	// Inside non-ASCII text the newline goes at the cursor, and the cursor
	// moves past it: typing goes on after the break.
	a = offlineApp(t)
	typeRunes(a, "日本語")
	press(a, tea.KeyMsg{Type: tea.KeyLeft})
	press(a, altEnter)
	typeRunes(a, "é")
	if got, want := a.input.Value(), "日本\né語"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
}

// Enter submits rather than inserting a newline; offline the line stays.
func TestEnterDoesNotInsertNewline(t *testing.T) {
	a := offlineApp(t)
	typeRunes(a, "question")
	press(a, tea.KeyMsg{Type: tea.KeyEnter})
	if got := a.input.Value(); got != "question" {
		t.Errorf("input after enter = %q, want it kept", got)
	}
	if a.refusal != "engine: not connected" {
		t.Errorf("refusal = %q", a.refusal)
	}
}

func TestMultiLinePasteKeptAsTyped(t *testing.T) {
	a := offlineApp(t)
	typeRunes(a, "see: ")
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("one\r\ntwo\rthree\n\tfour"), Paste: true})
	if got, want := a.input.Value(), "see: one\ntwo\nthree\n    four"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if got, want := inputRegion(t, a), []string{defaultPrompt + "see: one", "  two", "  three", "      four"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("input lines = %q, want %q", got, want)
	}
	// A pasted "enter" is text, not a submit.
	if a.refusal != "" {
		t.Errorf("refusal after paste = %q, want none", a.refusal)
	}
}

// At 80×24 a tall input stops at maxInputRows, and the tab bar, five lines of
// the tab and the status line still fit.
func TestTallInputLeavesTheTabItsRoom(t *testing.T) {
	a := offlineApp(t)
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("line\n", 12) + "last"), Paste: true})
	lines := strings.Split(a.View(), "\n")
	if len(lines) != 24 {
		t.Fatalf("view has %d lines, want 24", len(lines))
	}
	if a.inputRows() != maxInputRows {
		t.Errorf("input rows = %d, want %d", a.inputRows(), maxInputRows)
	}
	if a.bodyHeight() < 5 {
		t.Errorf("tab has %d lines, want at least 5", a.bodyHeight())
	}
	if !strings.HasPrefix(lines[0], "[chat]") || !strings.HasSuffix(lines[23], keyHint) {
		t.Errorf("tab bar %q, status line %q", lines[0], lines[23])
	}
	// The cursor is on the last line, so it is the one shown last.
	if got := inputRegion(t, a); got[len(got)-1] != "  last" {
		t.Errorf("input lines = %q, want the last one shown", got)
	}

	// Cleared, the input is one line again, from its start.
	press(a, tea.KeyMsg{Type: tea.KeyCtrlC})
	if got := inputRegion(t, a); len(got) != 1 || got[0] != strings.TrimRight(defaultPrompt, " ") {
		t.Errorf("input lines after ctrl+c = %q", got)
	}
}

// An input that grew past maxInputRows and is deleted back to fit shows
// from its first line again, with the cursor where it was.
func TestShrunkInputShowsFromItsFirstLine(t *testing.T) {
	a := offlineApp(t)
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb\nc\nd\ne\nf\ng"), Paste: true})
	for range 4 {
		press(a, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	if got, want := a.input.Value(), "a\nb\nc\nd\ne"; got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	if got, want := inputRegion(t, a), []string{defaultPrompt + "a", "  b", "  c", "  d", "  e"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("input lines = %q, want %q", got, want)
	}
	typeRunes(a, "!")
	if got, want := a.input.Value(), "a\nb\nc\nd\ne!"; got != want {
		t.Errorf("input after typing = %q, want %q: the cursor moved", got, want)
	}
}

func TestCtrlCClearsThenDetaches(t *testing.T) {
	a := offlineApp(t)
	typeRunes(a, "draft")
	press(a, altEnter)
	typeRunes(a, "more")
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlC}); isQuit(cmd) {
		t.Fatal("ctrl+c on a non-empty input detached")
	}
	if got := a.input.Value(); got != "" {
		t.Fatalf("input after ctrl+c = %q, want it cleared", got)
	}
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlC}); !isQuit(cmd) {
		t.Fatal("ctrl+c on an empty input did not detach")
	}
}

func TestCtrlDDetachesOnlyOnEmptyInput(t *testing.T) {
	a := offlineApp(t)
	typeRunes(a, "abc")
	press(a, tea.KeyMsg{Type: tea.KeyHome})
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlD}); isQuit(cmd) {
		t.Fatal("ctrl+d on a non-empty input detached")
	}
	if got := a.input.Value(); got != "bc" {
		t.Fatalf("input after ctrl+d = %q, want the character under the cursor deleted", got)
	}
	press(a, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlD}); !isQuit(cmd) {
		t.Fatal("ctrl+d on an empty input did not detach")
	}
}

// While /release waits for its reason, ctrl+c still clears a typed reason
// before it detaches.
func TestCtrlCInAConfirmation(t *testing.T) {
	a := offlineApp(t)
	a.confirm = &releasePrompt{pool: "default"}
	a.setPrompt(reasonPrompt)
	typeRunes(a, "because")
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlC}); isQuit(cmd) || a.input.Value() != "" {
		t.Fatalf("ctrl+c on a typed reason: quit=%v input=%q", isQuit(cmd), a.input.Value())
	}
	if got := inputRegion(t, a); got[0] != "reason>" {
		t.Errorf("input line = %q, want the reason prompt", got)
	}
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlC}); !isQuit(cmd) {
		t.Fatal("ctrl+c on an empty reason did not detach")
	}
}

func TestNormalizePaste(t *testing.T) {
	for in, want := range map[string]string{
		"a\r\nb": "a\nb",
		"a\rb":   "a\nb",
		"a\nb":   "a\nb",
		"a\r\n":  "a\n",
		"ab":     "ab",
	} {
		if got := normalizePaste([]rune(in)); got != want {
			t.Errorf("normalizePaste(%q) = %q, want %q", in, got, want)
		}
	}
}

// While a yes/no question is open, ctrl+c and ctrl+d act as they do on the
// input anywhere: ctrl+c clears a non-empty input, and on an empty input
// both detach rather than answering.
func TestCtrlKeysInAQuestion(t *testing.T) {
	answered := false
	open := func(a *App) {
		a.ask(&question{
			lines: []string{"drop turns?"},
			yes:   func(*App) tea.Cmd { answered = true; return nil },
			no:    func(*App) tea.Cmd { answered = true; return nil },
		})
	}

	a := offlineApp(t)
	typeRunes(a, "kept")
	open(a)
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlD}); isQuit(cmd) || a.input.Value() != "kept" {
		t.Fatalf("ctrl+d on a non-empty input: quit=%v input=%q", isQuit(cmd), a.input.Value())
	}
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlC}); isQuit(cmd) || a.input.Value() != "" {
		t.Fatalf("ctrl+c on a non-empty input: quit=%v input=%q", isQuit(cmd), a.input.Value())
	}
	if a.question == nil || answered {
		t.Fatal("ctrl+c answered the question")
	}
	if got := inputRegion(t, a); got[0] != "(y/n)>" {
		t.Errorf("input line = %q, want the question's prompt", got)
	}
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlC}); !isQuit(cmd) || answered {
		t.Fatalf("ctrl+c on an empty input: quit=%v answered=%v, want a detach", isQuit(cmd), answered)
	}

	a = offlineApp(t)
	open(a)
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyCtrlD}); !isQuit(cmd) || answered {
		t.Fatalf("ctrl+d on an empty input: quit=%v answered=%v, want a detach", isQuit(cmd), answered)
	}

	// esc still answers no.
	a = offlineApp(t)
	open(a)
	if press(a, tea.KeyMsg{Type: tea.KeyEsc}); a.question != nil || !answered {
		t.Fatalf("esc did not answer the question")
	}
}

// A line the textarea word-wraps onto more rows than its width alone would
// need still shows in full, down to the row the cursor is on.
func TestWordWrappedInputShowsTheCursorRow(t *testing.T) {
	a := offlineApp(t)
	word := strings.Repeat("a", 40)
	typeRunes(a, strings.Join([]string{word, word, word, "END"}, " "))
	// 40+1+40 does not fit the 78 cells after the prompt, so each word takes
	// a row: three rows, where 124 cells alone would need two.
	if got := a.inputRows(); got != 3 {
		t.Errorf("input rows = %d, want 3", got)
	}
	got := inputRegion(t, a)
	if want := []string{defaultPrompt + word, "  " + word, "  " + word + " END"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("input lines = %q, want %q", got, want)
	}
	if view := strings.Split(a.input.View(), "\n"); strings.TrimRight(view[2], " ") != "  "+word+" END" {
		t.Errorf("textarea row 3 = %q, want the cursor row", view[2])
	}
}

func TestWrappedRowsMatchesTheTextarea(t *testing.T) {
	lines := []string{
		"",
		"short",
		strings.Repeat("x", 78),
		strings.Repeat("x", 77),
		strings.Repeat("x", 200),
		strings.Repeat("word ", 40),
		strings.Repeat("日本語 ", 30),
		"a " + strings.Repeat("b", 100) + " c",
		strings.Repeat("lorem ipsum dolor sit amet, ", 6),
	}
	a := offlineApp(t)
	for _, l := range lines {
		a.input.SetValue(l)
		// The cursor is on the line, so LineInfo counts the rows the
		// textarea wraps it onto.
		if got, want := inputLines(a.input), a.input.LineInfo().Height; got != want {
			t.Errorf("inputLines(%q) = %d, textarea draws %d", l, got, want)
		}
	}
}
