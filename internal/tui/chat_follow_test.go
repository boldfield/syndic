package tui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/tui/followtest"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// chatFollowApp is a chat laid out 80 wide with 9 rows for the chat itself.
func chatFollowApp(t *testing.T) *App {
	t.Helper()
	a := chatApp(t)
	a.height = 14
	a.workDir, a.conversationID, a.convTurns = "/ws", "0123456789abcdef", 2
	if a.answerHeight() != 9 {
		t.Fatalf("answer height = %d, want 9", a.answerHeight())
	}
	return a
}

func numbered(prefix string, n int) string {
	var ls []string
	for i := 0; i < n; i++ {
		ls = append(ls, fmt.Sprintf("%s %d", prefix, i))
	}
	return strings.Join(ls, "\n")
}

// window is the chat rows the view draws, below the tab bar and the conversation line.
func window(a *App) []string {
	lines := strings.Split(ansi.Strip(a.View()), "\n")
	out := make([]string, a.answerHeight())
	for i := range out {
		out[i] = strings.TrimRight(lines[2+i], " ")
	}
	return out
}

func conversationLine(a *App) string { return strings.Split(ansi.Strip(a.View()), "\n")[1] }

// wantLastRow fails unless the view shows the chat's last row.
func wantLastRow(t *testing.T, a *App, what string) {
	t.Helper()
	l := a.layoutChat()
	if want := max(len(l.lines)-a.answerHeight(), 0); l.first != want || !l.bottom {
		t.Fatalf("%s: first row %d (bottom %t), want %d at the bottom", what, l.first, l.bottom, want)
	}
	w := window(a)
	last := strings.TrimRight(ansi.Strip(l.lines[len(l.lines)-1]), " ")
	if got := w[min(len(l.lines), len(w))-1]; got != last {
		t.Fatalf("%s: the last row shown is %q, want %q", what, got, last)
	}
}

func pressN(a *App, k tea.KeyType, n int) {
	for i := 0; i < n; i++ {
		press(a, tea.KeyMsg{Type: k})
	}
}

func newLines(t *testing.T, a *App) int { t.Helper(); return a.layoutChat().count }

func TestChatFollowsATallAnswerAtTheBottom(t *testing.T) {
	a := chatFollowApp(t)
	addTurn(a, "t1", 1, "approved", "question", "short")
	wantLastRow(t, a, "short")
	addTurn(a, "t2", 2, "approved", "again", numbered("ans line", 40))
	wantLastRow(t, a, "tall answer")
	w := strings.Join(window(a), "\n")
	if !strings.Contains(w, "ans line 39") || !strings.Contains(w, "approved by") || strings.Contains(w, "ans line 20\n") {
		t.Fatalf("the view does not show the last rows of the answer:\n%s", w)
	}
}

func TestChatFollowsProgressBlockAndInput(t *testing.T) {
	a := chatFollowApp(t)
	addTurn(a, "t1", 1, "approved", "question", numbered("ans line", 30))
	wantLastRow(t, a, "start")
	addTurn(a, "t2", 2, "author_dispatched", "next", "")
	a.state.CurrentTurn = "t2"
	wantLastRow(t, a, "a progress line")
	ti := a.state.Turns["t2"]
	ti.State = "incomplete"
	hash := "abcdef1234567890t2"
	ti.CandidateHash = &hash
	a.state.Turns["t2"] = ti
	a.openBlocksByTurn["t2"] = []wire.OpenBlock{{ID: "b1", Severity: "major", State: "open", Evidence: "e", Remedy: "do this"}}
	wantLastRow(t, a, "a block")
	addTurn(a, "t3", 3, "author_dispatched", "third question", "")
	wantLastRow(t, a, "a new turn's input and its progress line")
}

func TestChatAwayCountsNewLines(t *testing.T) {
	a := chatFollowApp(t)
	addTurn(a, "t1", 1, "approved", "question", numbered("ans line", 30))
	pressN(a, tea.KeyPgUp, 1)
	before := a.layoutChat()
	if before.bottom {
		t.Fatal("PgUp left the chat at the bottom")
	}
	if strings.Contains(conversationLine(a), "new lines") {
		t.Fatalf("a count with nothing new: %q", conversationLine(a))
	}
	addTurn(a, "t2", 2, "approved", "again", numbered("more", 5))
	after := a.layoutChat()
	if after.first != before.first {
		t.Fatalf("first row moved from %d to %d as rows arrived", before.first, after.first)
	}
	n := len(after.lines) - len(before.lines)
	line := conversationLine(a)
	want := "↓ " + strconv.Itoa(n) + " new lines · "
	if !strings.Contains(line, want) || !strings.HasSuffix(line, "%") || strings.Index(line, "new lines") > strings.Index(line, "earlier") {
		t.Fatalf("conversation line %q, want %q before the turns-above count and the percentage", line, want)
	}
}

func TestChatCountClearsAtTheBottomAndFollows(t *testing.T) {
	a := chatFollowApp(t)
	addTurn(a, "t1", 1, "approved", "question", numbered("ans line", 30))
	pressN(a, tea.KeyUp, 3)
	addTurn(a, "t2", 2, "approved", "again", "x")
	if newLines(t, a) == 0 {
		t.Fatal("no new lines counted")
	}
	pressN(a, tea.KeyDown, 100)
	if l := a.layoutChat(); !l.bottom || l.count != 0 || strings.Contains(conversationLine(a), "new lines") {
		t.Fatalf("at the last row: bottom %t, count %d, line %q", l.bottom, l.count, conversationLine(a))
	}
	wantLastRow(t, a, "scrolled back down")
	addTurn(a, "t3", 3, "approved", "once more", numbered("tail", 4))
	wantLastRow(t, a, "the next rows")
}

func TestChatEndAndHome(t *testing.T) {
	a := chatFollowApp(t)
	addTurn(a, "t1", 1, "approved", "question", numbered("ans line", 30))
	pressN(a, tea.KeyPgUp, 2)
	pressN(a, tea.KeyEnd, 1)
	wantLastRow(t, a, "End")
	pressN(a, tea.KeyHome, 1)
	if l := a.layoutChat(); l.first != 0 || l.bottom {
		t.Fatalf("Home: first %d, bottom %t, want the first row and away", l.first, l.bottom)
	}
	addTurn(a, "t2", 2, "approved", "again", "more")
	if l := a.layoutChat(); l.first != 0 || l.bottom || l.count == 0 {
		t.Fatalf("after Home a row arrived: first %d, bottom %t, count %d", l.first, l.bottom, l.count)
	}
	pressN(a, tea.KeyEnd, 1)
	wantLastRow(t, a, "End again")
	// With text in the input, Home and End still move the input's cursor.
	typeRunes(a, "abc")
	pressN(a, tea.KeyHome, 1)
	wantLastRow(t, a, "Home with text typed")
}

func TestChatShorterThanTheViewStaysAtTheBottom(t *testing.T) {
	a := chatFollowApp(t)
	addTurn(a, "t1", 1, "approved", "question", "short")
	for _, k := range []tea.KeyType{tea.KeyHome, tea.KeyPgUp, tea.KeyUp} {
		pressN(a, k, 1)
		if l := a.layoutChat(); !l.bottom || l.first != 0 {
			t.Fatalf("%v on short content: first %d, bottom %t", k, l.first, l.bottom)
		}
	}
	addTurn(a, "t2", 2, "approved", "again", numbered("ans line", 15))
	wantLastRow(t, a, "the next rows")
}

func TestChatScrolledToTheLastRowByHandFollows(t *testing.T) {
	a := chatFollowApp(t)
	addTurn(a, "t1", 1, "approved", "question", numbered("ans line", 30))
	pressN(a, tea.KeyUp, 2)
	pressN(a, tea.KeyDown, 2)
	wantLastRow(t, a, "by hand")
	addTurn(a, "t2", 2, "approved", "again", numbered("more", 6))
	wantLastRow(t, a, "after rows arrived")
}

func TestChatNarrowerHoldsTheAnchor(t *testing.T) {
	a := chatFollowApp(t)
	long := strings.Repeat("word ", 120)
	addTurn(a, "t1", 1, "approved", long, numbered("ans line", 20))
	_, rows, _, _ := chatRows(a.state, a.chatIn())
	first := -1
	for i, r := range rows {
		if r.Item == "t1/input" && r.Char > 0 {
			first = i + 1 // the second row of the input
			break
		}
	}
	chatScrollTo(a, first)
	anchor := a.layoutChat().first
	_, rows, _, _ = chatRows(a.state, a.chatIn())
	at := rows[anchor]
	a.update(tea.WindowSizeMsg{Width: 45, Height: 14})
	_, rows, _, _ = chatRows(a.state, a.chatIn())
	l := a.layoutChat()
	if l.bottom {
		t.Fatal("narrowing took the view to the bottom")
	}
	got := rows[l.first]
	if got.Item != at.Item || got.Char > at.Char || (got.End <= at.Char && l.first+1 < len(rows) && rows[l.first+1].Item == at.Item) {
		t.Fatalf("first row after narrowing is %+v, want the row of %s that contains character %d", got, at.Item, at.Char)
	}
}

func TestChatStartsAtTheBottom(t *testing.T) {
	a := chatFollowApp(t)
	for i := 1; i <= 8; i++ {
		addTurn(a, "t"+strconv.Itoa(i), int64(i), "approved", "q", numbered("ans line", 6))
	}
	wantLastRow(t, a, "start")
	// A conversation opened or switched to, and a tab shown, start at the bottom too.
	pressN(a, tea.KeyHome, 1)
	a.openConversation("c2", []string{"t1", "t2", "t3", "t4"})
	wantLastRow(t, a, "a conversation opened")
	pressN(a, tea.KeyHome, 1)
	a.setTab(tabTranscript)
	a.setTab(tabChat)
	wantLastRow(t, a, "the tab shown again")
}

func TestChatResizeAtTheBottom(t *testing.T) {
	a := chatFollowApp(t)
	addTurn(a, "t1", 1, "approved", strings.Repeat("word ", 60), numbered("ans line", 25))
	wantLastRow(t, a, "start")
	for _, sz := range []tea.WindowSizeMsg{{Width: 45, Height: 14}, {Width: 120, Height: 14}, {Width: 60, Height: 20}} {
		a.update(sz)
		wantLastRow(t, a, fmt.Sprintf("resized to %dx%d", sz.Width, sz.Height))
	}
}

func TestChatOpeningTheDrawerKeepsTheBottom(t *testing.T) {
	a := chatFollowApp(t)
	addTurn(a, "t1", 1, "approved", "question", numbered("ans line", 30))
	a.openDrawer(0)
	for i := 0; i < 10; i++ {
		a.update(drawerFrameMsg{})
	}
	wantLastRow(t, a, "drawer open")
}

// chatAdaptor drives the shared case table through the App's keys and its conversation line.
type chatAdaptor struct {
	a    *App
	rows []FollowRow
}

func (v *chatAdaptor) SetRows(rows []followtest.Row) {
	v.rows = v.rows[:0:0]
	for _, r := range rows {
		v.rows = append(v.rows, FollowRow{r.Item, r.Char, r.End})
	}
	v.a.layoutChat()
}

func (v *chatAdaptor) SetHeight(h int) { v.a.chatHeight = h; v.a.layoutChat() }

func (v *chatAdaptor) Press(k followtest.Key) {
	keys := map[followtest.Key]tea.KeyType{followtest.Up: tea.KeyUp, followtest.Down: tea.KeyDown,
		followtest.PageUp: tea.KeyPgUp, followtest.PageDown: tea.KeyPgDown, followtest.Home: tea.KeyHome, followtest.End: tea.KeyEnd}
	press(v.a, tea.KeyMsg{Type: keys[k]})
}

func (v *chatAdaptor) First() int     { return v.a.layoutChat().first }
func (v *chatAdaptor) AtBottom() bool { return v.a.layoutChat().bottom }
func (v *chatAdaptor) Count() int     { return v.a.layoutChat().count }

func TestChatFollowCases(t *testing.T) {
	followtest.Run(t, func() followtest.View {
		v := &chatAdaptor{a: chatApp(t)}
		v.a.chatRowsFn = func() ([]string, []FollowRow, []int) {
			lines := make([]string, len(v.rows))
			for i, r := range v.rows {
				lines[i] = fmt.Sprintf("%s %d", r.Item, r.Char)
			}
			return lines, v.rows, nil
		}
		return v
	})
}

func TestChatNarrowerHoldsADeepAnchor(t *testing.T) {
	words := make([]string, 200)
	for i := range words {
		words[i] = fmt.Sprintf("w%03d", i)
	}
	a := chatFollowApp(t)
	a.update(tea.WindowSizeMsg{Width: 80, Height: 14})
	addTurn(a, "t1", 1, "approved", strings.Join(words, " "), numbered("ans line", 20))
	_, rows, _, _ := chatRows(a.state, a.chatIn())
	first := -1
	for i, r := range rows {
		if r.Item == "t1/input" && r.Char > 700 {
			first = i
			break
		}
	}
	if first < 0 {
		t.Fatal("no deep input row")
	}
	chatScrollTo(a, first)
	at := rows[first]
	a.update(tea.WindowSizeMsg{Width: 30, Height: 14})
	_, rows, _, _ = chatRows(a.state, a.chatIn())
	l := a.layoutChat()
	if got := rows[l.first]; l.bottom || got.Item != at.Item || got.Char > at.Char || got.End <= at.Char {
		t.Fatalf("first row after narrowing is %+v, want the row of %s holding character %d", got, at.Item, at.Char)
	}
}
