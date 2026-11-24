package tui

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func runesKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// chooserApp is an App on /ws/here with an open list of three conversations
// whose workspace is this directory and one of another, the other last.
func chooserApp(t *testing.T, items ...wire.ConversationSummary) *App {
	t.Helper()
	a := chatApp(t)
	a.workDir = "/ws/here"
	if items == nil {
		here, elsewhere := "/ws/here", "/ws/elsewhere"
		items = []wire.ConversationSummary{
			{ID: "aaaaaaaa11", Workspace: &here, TurnCount: 1, Preview: "alpha question"},
			{ID: "bbbbbbbb22", Workspace: &here, TurnCount: 2, Preview: "beta question"},
			{ID: "cccccccc33", Workspace: &elsewhere, TurnCount: 1, Preview: "gamma question"},
		}
	}
	a.conv = &convChooser{loaded: true, items: items}
	a.resetSel()
	return a
}

func selectedID(a *App) string {
	rows := a.conv.visible()
	if a.conv.sel < 0 || a.conv.sel >= len(rows) {
		return ""
	}
	return rows[a.conv.sel].ID
}

func TestChooserViewMarksSelection(t *testing.T) {
	a := chooserApp(t)
	v := a.View()
	for _, want := range []string{"› " + "  aaaaaaaa", "alpha question", "beta question", "gamma question", "[/ws/elsewhere]", "Enter switch"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	press(a, tea.KeyMsg{Type: tea.KeyDown})
	if got := selectedID(a); got != "bbbbbbbb22" {
		t.Errorf("selected after Down = %q", got)
	}
	if v := a.View(); !strings.Contains(v, "› "+"  bbbbbbbb") {
		t.Errorf("marker did not move:\n%s", v)
	}
}

// Another workspace's row is never selected, at either end of the list.
func TestChooserSkipsOtherWorkspaceRows(t *testing.T) {
	a := chooserApp(t)
	for range 5 {
		press(a, tea.KeyMsg{Type: tea.KeyDown})
	}
	if got := selectedID(a); got != "bbbbbbbb22" {
		t.Errorf("selected after Down past the end = %q, want the last row of this workspace", got)
	}
	for range 5 {
		press(a, tea.KeyMsg{Type: tea.KeyUp})
	}
	if got := selectedID(a); got != "aaaaaaaa11" {
		t.Errorf("selected after Up past the start = %q", got)
	}

	// Another workspace's row first: the selection starts on the next one.
	here, elsewhere := "/ws/here", "/ws/elsewhere"
	a = chooserApp(t,
		wire.ConversationSummary{ID: "xxxxxxxx", Workspace: &elsewhere, Preview: "foreign"},
		wire.ConversationSummary{ID: "yyyyyyyy", Workspace: &here, Preview: "mine"})
	if got := selectedID(a); got != "yyyyyyyy" {
		t.Errorf("initial selection = %q", got)
	}
	press(a, tea.KeyMsg{Type: tea.KeyUp})
	if got := selectedID(a); got != "yyyyyyyy" {
		t.Errorf("selection after Up = %q, want it to stay", got)
	}

	// Only other-workspace rows: nothing is selected and Enter does nothing.
	a = chooserApp(t, wire.ConversationSummary{ID: "xxxxxxxx", Workspace: &elsewhere, Preview: "foreign"})
	if got := selectedID(a); got != "" {
		t.Errorf("selected %q with nothing to choose", got)
	}
	press(a, tea.KeyMsg{Type: tea.KeyDown})
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || a.conv == nil {
		t.Errorf("Enter on no selection: cmd %v, list open %v", cmd != nil, a.conv != nil)
	}
}

func TestChooserDimsOtherWorkspaceRows(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
	a := chooserApp(t)
	for _, l := range strings.Split(a.View(), "\n") {
		faint := strings.Contains(l, "\x1b[2m")
		switch {
		case strings.Contains(l, "gamma question") && !faint:
			t.Errorf("other workspace's row is not dimmed: %q", l)
		case (strings.Contains(l, "alpha question") || strings.Contains(l, "beta question")) && faint:
			t.Errorf("this workspace's row is dimmed: %q", l)
		}
	}
}

// "/" starts filtering; then every letter, n included, is the filter's.
func TestChooserFilter(t *testing.T) {
	a := chooserApp(t)
	press(a, runesKey("/"))
	press(a, runesKey("n"))
	if a.conv == nil || a.conv.filter != "n" || !a.conv.filtering {
		t.Fatalf("n in filter mode: %+v", a.conv)
	}
	press(a, tea.KeyMsg{Type: tea.KeyBackspace})
	typeRunes(a, "BETA")
	if got := a.conv.visible(); len(got) != 1 || got[0].ID != "bbbbbbbb22" || selectedID(a) != "bbbbbbbb22" {
		t.Errorf("filter BETA keeps %v, selected %q", got, selectedID(a))
	}
	if v := a.View(); strings.Contains(v, "alpha question") || !strings.Contains(v, "filter: BETA") {
		t.Errorf("filtered view:\n%s", v)
	}
	typeRunes(a, "zzz")
	if v := a.View(); !strings.Contains(v, "no conversations") || selectedID(a) != "" {
		t.Errorf("nothing matches:\n%s", v)
	}
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Error("Enter with no row switched")
	}
}

func TestChooserNStartsNewConversationAndKeepsTurn(t *testing.T) {
	a := chooserApp(t)
	a.conversationID = "aaaaaaaa11"
	a.state.CurrentTurn = "t-running"
	a.state.TurnConversation = map[string]string{"t-running": "aaaaaaaa11"}
	press(a, runesKey("n"))
	if a.conv != nil || a.conversationID != "" {
		t.Errorf("after n: list %v, conversation %q", a.conv != nil, a.conversationID)
	}
	if a.state.CurrentTurn != "t-running" {
		t.Errorf("n changed the running turn to %q", a.state.CurrentTurn)
	}
	if !strings.Contains(a.View(), "keeps running") {
		t.Errorf("no running-turn notice:\n%s", a.View())
	}
}

func TestChooserEscCloses(t *testing.T) {
	a := chooserApp(t)
	press(a, tea.KeyMsg{Type: tea.KeyEsc})
	if a.conv != nil {
		t.Error("Esc left the list open")
	}
	a = chooserApp(t)
	press(a, runesKey("/"))
	press(a, tea.KeyMsg{Type: tea.KeyEsc})
	if a.conv != nil {
		t.Error("Esc in filter mode left the list open")
	}
}

// Typing while the list is open does not reach the input line.
func TestChooserKeysDoNotEditInput(t *testing.T) {
	a := chooserApp(t)
	typeRunes(a, "hello")
	if a.input.Value() != "" {
		t.Errorf("input = %q", a.input.Value())
	}
}

// An empty list, and one that cannot be read, are said once and do not read again.
func TestConversationsMsgFillsOnce(t *testing.T) {
	a := chatApp(t)
	a.conv = &convChooser{sel: -1}
	if v := a.View(); !strings.Contains(v, "loading") {
		t.Errorf("list before reply:\n%s", v)
	}
	_, cmd := a.onConversations(conversationsMsg{gen: a.gen})
	if cmd != nil || !a.conv.loaded {
		t.Errorf("empty reply: cmd %v, loaded %v", cmd != nil, a.conv.loaded)
	}
	if v := a.View(); !strings.Contains(v, "no conversations") {
		t.Errorf("empty list:\n%s", v)
	}
	a.conv = &convChooser{sel: -1}
	_, cmd = a.onConversations(conversationsMsg{gen: a.gen, err: &wire.Error{Code: "boom"}})
	if cmd != nil || a.conv.err == nil || len(a.panel) != 0 {
		t.Errorf("error reply: cmd %v, err %v, panel %v", cmd != nil, a.conv.err, a.panel)
	}
	// A reply for a closed list is dropped.
	a.conv = nil
	a.onConversations(conversationsMsg{gen: a.gen})
	if a.conv != nil {
		t.Error("a late reply reopened the list")
	}
}

// Text from a conversation's record or workspace cannot break a row.
func TestChooserSafeText(t *testing.T) {
	hostile := "p\x1b]8;;http://x\x1b\\L\x1b[2J\u202e\nINJECTED"
	lines := conversationChooserLines(t, hostile, "/w/"+hostile, 120)
	assertSafeRunes(t, "chooser", strings.Join(lines, "\n"))
	assertNoInjectedRow(t, "chooser", lines)
}

// Enter on the selected row switches to it, through the same resolution as
// /switch; a row of another workspace was never selectable to begin with.
func TestChooserEnterSwitchesAndKeepsRunningTurn(t *testing.T) {
	r := startConvRig(t, 4)
	runTurn(t, r.driver, "first question")
	first := r.app.conversationID
	r.command("/new", "new conversation")
	runTurn(t, r.driver, "second question")
	second := r.app.conversationID
	submitElsewhere(t, r, "/ws/elsewhere", "foreign question")

	r.command("/conversations", "first question", "second question", "foreign question")
	r.send(tea.KeyMsg{Type: tea.KeyDown})
	if got := selectedID(r.app); got != first {
		t.Fatalf("selected %q, want %q", got, first)
	}
	// A turn running elsewhere is left alone by the switch.
	r.app.state.CurrentTurn = "t-running"
	r.send(tea.KeyMsg{Type: tea.KeyEnter})
	r.until("the switch", func(string) bool { return r.app.conversationID == first })
	if r.app.conv != nil {
		t.Error("the list stayed open after Enter")
	}
	if r.app.state.CurrentTurn != "t-running" {
		t.Errorf("the switch changed the running turn to %q", r.app.state.CurrentTurn)
	}
	if r.app.conversationID == second {
		t.Error("still on the second conversation")
	}
}
