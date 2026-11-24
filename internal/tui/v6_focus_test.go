package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// These tests are the v6 section 8 list, drawer and prompt acceptance
// scenarios: keys go in through App.Update, what the user sees is read from
// App.View, and what a command did is read from the engine's journal.

// reviewedApp is a chat of two settled turns with the drawer's reviews
// delivered as the fetch would deliver them. Turn 1 has three rounds, one
// block each in the states opened, maintained and withdrawn, with a long
// evidence text in round 1; turn 2 has no reviews.
func reviewedApp(t *testing.T) *App {
	t.Helper()
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "first question", "first answer")
	addTurn(a, "t2", 2, "approved", "second question", "second answer")
	a.state.Status.Participants = []wire.Participant{{ID: "muse"}, {ID: "codex"}}
	var evidence []string
	for i := range 40 {
		evidence = append(evidence, fmt.Sprintf("evidence-line-%02d", i))
	}
	a.content.Store("ev1", strings.Join(evidence, "\n"), nil)
	a.content.Store("note", "tail-of-the-commentary", nil)
	ref := func(r string) *wire.ContentReference { return &wire.ContentReference{Ref: r} }
	a.Update(reviewsMsg{turnID: "t1", items: []wire.ReviewItem{
		{Round: 0, Participant: "codex", Kind: "block", Blocks: []wire.ReviewBlock{{BlockID: "b1", State: "opened", Evidence: ref("ev1")}}, Commentary: ref("note")},
		{Round: 0, Participant: "muse", Kind: "approve-with-changes"},
		{Round: 1, Participant: "codex", Kind: "block", Blocks: []wire.ReviewBlock{{BlockID: "b1", State: "maintained"}}},
		{Round: 2, Participant: "codex", Kind: "approve", Blocks: []wire.ReviewBlock{{BlockID: "b1", State: "withdrawn"}}},
	}})
	a.Update(reviewsMsg{turnID: "t2"})
	return a
}

func keys(a *App, ts ...tea.KeyType) {
	for _, k := range ts {
		press(a, tea.KeyMsg{Type: k})
	}
}

// ctrl+o opens the drawer on the newest turn; ↑/↓ move between turns, ←/→
// between rounds, PgDn/PgUp scroll; Esc closes. Each state is read from View.
func TestV6DrawerKeysThroughUpdateAndView(t *testing.T) {
	a := reviewedApp(t)
	if v := a.View(); strings.Contains(v, "Reviews ·") {
		t.Fatalf("drawer shows before it is opened:\n%s", v)
	}

	openDrawerSettled(t, a)
	v := a.View()
	if !strings.Contains(v, "Reviews · Turn 2") || !strings.Contains(v, "no reviews") {
		t.Fatalf("drawer opens on the newest turn, which has no reviews:\n%s", v)
	}

	// ↑ selects turn 1; its three rounds are listed and round 1 shown, the
	// reviewers in roster order with their verdict kinds and block states.
	keys(a, tea.KeyUp)
	v = a.View()
	for _, want := range []string{"Reviews · Turn 1 · [*1* 2 3]", "codex (block)", "b1: opened", "muse (approve-with-changes)", "evidence:", "evidence-line-00"} {
		if !strings.Contains(v, want) {
			t.Fatalf("turn 1 round 1 lacks %q:\n%s", want, v)
		}
	}
	if strings.Index(v, "muse (approve") > strings.Index(v, "codex (block)") {
		t.Errorf("reviewers are not in roster order:\n%s", v)
	}
	if strings.Contains(v, "maintained") || strings.Contains(v, "withdrawn") {
		t.Errorf("round 1 shows another round's states:\n%s", v)
	}
	// The selected turn's input is highlighted in colour and marked in a
	// plain terminal.
	lipgloss.SetColorProfile(termenv.TrueColor)
	for _, l := range strings.Split(a.View(), "\n") {
		styled := strings.Contains(l, "\x1b[")
		switch {
		case strings.Contains(l, "first question") && !styled:
			t.Errorf("the selected input is not highlighted: %q", l)
		case strings.Contains(l, "second question") && styled:
			t.Errorf("an unselected input is highlighted: %q", l)
		}
	}
	lipgloss.SetColorProfile(termenv.Ascii)
	a.terminalCapabilities = TerminalCapabilities{}
	if pv := a.View(); !strings.Contains(pv, selectedMark+"first question") || !strings.Contains(pv, userMark+"second question") {
		t.Errorf("the plain terminal does not mark the selected input:\n%s", pv)
	}
	a.terminalCapabilities = TerminalCapabilities{Color: true, AltScreen: true}

	// → shows round 2, then round 3; ← goes back.
	keys(a, tea.KeyRight)
	v = a.View()
	if !strings.Contains(v, "Reviews · Turn 1 · [1 *2* 3]") || !strings.Contains(v, "b1: maintained") || strings.Contains(v, "b1: opened") || strings.Contains(v, "muse") {
		t.Fatalf("round 2:\n%s", v)
	}
	keys(a, tea.KeyRight)
	v = a.View()
	if !strings.Contains(v, "Reviews · Turn 1 · [1 2 *3*]") || !strings.Contains(v, "codex (approve)") || !strings.Contains(v, "b1: withdrawn") {
		t.Fatalf("round 3:\n%s", v)
	}
	keys(a, tea.KeyRight) // past the last round: stays
	if v = a.View(); !strings.Contains(v, "[1 2 *3*]") {
		t.Fatalf("→ past the last round moved:\n%s", v)
	}
	keys(a, tea.KeyLeft, tea.KeyLeft)
	v = a.View()
	if !strings.Contains(v, "[*1* 2 3]") || !strings.Contains(v, "b1: opened") {
		t.Fatalf("← did not return to round 1:\n%s", v)
	}

	// The evidence is taller than the drawer: PgDn reaches its tail and the
	// commentary, PgUp the header again.
	if strings.Contains(v, "evidence-line-39") || strings.Contains(v, "tail-of-the-commentary") {
		t.Fatalf("the drawer shows more than its height:\n%s", v)
	}
	var seen string
	for range 20 {
		keys(a, tea.KeyPgDown)
		seen += a.View()
	}
	v = a.View()
	if !strings.Contains(seen, "evidence-line-39") || !strings.Contains(v, "tail-of-the-commentary") {
		t.Fatalf("PgDn never reached the end:\n%s", v)
	}
	if strings.Contains(v, "Reviews · Turn 1") {
		t.Errorf("the header stayed while scrolled to the end:\n%s", v)
	}
	for range 20 {
		keys(a, tea.KeyPgUp)
	}
	if v = a.View(); !strings.Contains(v, "Reviews · Turn 1 · [*1* 2 3]") || strings.Contains(v, "tail-of-the-commentary") {
		t.Fatalf("PgUp did not return to the top:\n%s", v)
	}

	// ↓ returns to turn 2, whose empty state shows again; Esc closes.
	keys(a, tea.KeyDown)
	if v = a.View(); !strings.Contains(v, "Reviews · Turn 2") || !strings.Contains(v, "no reviews") {
		t.Fatalf("↓ did not return to turn 2:\n%s", v)
	}
	keys(a, tea.KeyEsc)
	a.reviewsDrawer.animating = false
	if v = a.View(); strings.Contains(v, "Reviews ·") || a.reviewsDrawer.IsOpen() {
		t.Fatalf("Esc left the drawer open:\n%s", v)
	}
}

// While the drawer has the keyboard, typing, paste and Enter reach nothing.
func TestV6DrawerHoldsTheKeyboard(t *testing.T) {
	a := reviewedApp(t)
	openDrawerSettled(t, a)
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("typed")})
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted"), Paste: true})
	if cmd := press(a, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Errorf("Enter in the drawer ran %v", cmd)
	}
	if got := a.input.Value(); got != "" {
		t.Errorf("the input took %q while the drawer had the keyboard", got)
	}
	// After Esc typing works again.
	keys(a, tea.KeyEsc)
	press(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if got := a.input.Value(); got != "x" {
		t.Errorf("typing after Esc: input = %q", got)
	}
}

// A page of sixty reviewers is longer than the drawer; PgDn reaches the last
// and PgUp the first. The cursor requests that build such a list are the
// client's (client/reviews_test.go); this is the list as the drawer shows it.
func TestV6DrawerLongListPages(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "a")
	var items []wire.ReviewItem
	for i := range 60 {
		items = append(items, wire.ReviewItem{Round: 0, Participant: fmt.Sprintf("p%02d", i), Kind: "approve"})
	}
	a.Update(reviewsMsg{turnID: "t1", items: items})
	openDrawerSettled(t, a)
	v := a.View()
	if !strings.Contains(v, "p00 (approve)") || strings.Contains(v, "p59") {
		t.Fatalf("first page:\n%s", v)
	}
	for range 30 {
		keys(a, tea.KeyPgDown)
	}
	if v = a.View(); !strings.Contains(v, "p59 (approve)") || strings.Contains(v, "p00 (approve)") {
		t.Fatalf("last page:\n%s", v)
	}
	for range 30 {
		keys(a, tea.KeyPgUp)
	}
	if v = a.View(); !strings.Contains(v, "p00 (approve)") {
		t.Fatalf("back at the top:\n%s", v)
	}
}

// An oversized verdict whose record could not be fetched stays in its short
// form: reviewer and kind, no blocks, and the engine's refusal is said.
func TestV6DrawerOversizedShortForm(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "a")
	a.Update(reviewsMsg{
		turnID: "t1",
		items: []wire.ReviewItem{{Round: 0, Participant: "huge", Kind: "block", Oversized: true,
			Record: &wire.ContentReference{Ref: "rec", SHA256: "00", ByteLen: 9 << 20}}},
		warn: fmt.Errorf("record rec: hash mismatch"),
	})
	openDrawerSettled(t, a)
	v := a.View()
	if !strings.Contains(v, "huge (block)") || strings.Contains(v, "•") {
		t.Errorf("short form shows blocks or lacks the reviewer:\n%s", v)
	}
	if !strings.Contains(v, "reviews: some content is unavailable: record rec: hash mismatch") {
		t.Errorf("the unavailable record is not said:\n%s", v)
	}
}

// A refused or failed reviews fetch shows its reason in the drawer.
func TestV6DrawerFetchFailure(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "a")
	a.Update(reviewsMsg{turnID: "t1", err: fmt.Errorf("engine went away")})
	openDrawerSettled(t, a)
	if v := a.View(); !strings.Contains(v, "reviews unavailable: engine went away") {
		t.Errorf("fetch failure not shown:\n%s", v)
	}
}

// Typing / lists the commands; ↑/↓ move the selection without editing the
// input; Tab completes the selected one; Esc closes the list; short help
// shows on an empty input and goes when typing starts.
func TestV6CommandCompletionAndShortHelp(t *testing.T) {
	asciiProfile(t)
	a := chatApp(t)
	typeRunes(a, "/re")
	v := a.View()
	for _, want := range []string{"/release", "/resume"} {
		if !strings.Contains(v, want) {
			t.Fatalf("/re does not list %s:\n%s", want, v)
		}
	}
	for _, other := range []string{"/override", "/raw", "/quit"} {
		if strings.Contains(v, other) {
			t.Errorf("/re lists %s:\n%s", other, v)
		}
	}
	first := commandListMatches("/re")[0].name
	second := commandListMatches("/re")[1].name
	if strings.Contains(v, "› /") {
		t.Errorf("a row is selected before any arrow:\n%s", v)
	}
	keys(a, tea.KeyDown)
	if got := a.input.Value(); got != "/re" || !strings.Contains(a.View(), "› /"+first) {
		t.Errorf("↓ did not select %s without editing the input %q:\n%s", first, got, a.View())
	}
	keys(a, tea.KeyDown)
	if got := a.input.Value(); got != "/re" {
		t.Errorf("an arrow edited the input: %q", got)
	}
	if v = a.View(); !strings.Contains(v, "› /"+second) || strings.Contains(v, "› /"+first) {
		t.Errorf("the selection did not move to %s:\n%s", second, v)
	}
	tabBefore := a.tab
	keys(a, tea.KeyTab)
	if got := a.input.Value(); got != "/"+second+" " || a.tab != tabBefore {
		t.Errorf("Tab: input = %q, tab %v -> %v; want the completed command and the same tab", got, tabBefore, a.tab)
	}
	if v = a.View(); strings.Contains(v, "› /") {
		t.Errorf("the list stayed open after completion:\n%s", v)
	}

	a.clearInput()
	typeRunes(a, "/ra")
	if v = a.View(); !strings.Contains(v, "/raw on|off") || !strings.Contains(v, "[off]") {
		t.Fatalf("/raw row lacks its description or value:\n%s", v)
	}
	keys(a, tea.KeyEsc)
	if v = a.View(); strings.Contains(v, "/raw on|off") || a.input.Value() != "/ra" {
		t.Errorf("Esc did not close the list and keep the input %q:\n%s", a.input.Value(), v)
	}
	typeRunes(a, "w")
	if v = a.View(); !strings.Contains(v, "/raw") {
		t.Errorf("editing did not reopen the list:\n%s", v)
	}

	a.clearInput()
	typeRunes(a, "?")
	if v = a.View(); !strings.Contains(v, "? hide") {
		t.Fatalf("? on an empty input shows no short help:\n%s", v)
	}
	typeRunes(a, "x")
	if v = a.View(); strings.Contains(v, "? hide") || a.input.Value() != "x" {
		t.Errorf("typing did not hide the short help (input %q):\n%s", a.input.Value(), v)
	}
}

// The conversation chooser: /conversations opens it with the newest
// selected, ↓ moves the mark, Esc closes it and leaves the conversation,
// Enter switches to the selected one. Typed keys never reach the input.
func TestV6ChooserThroughUpdateAndView(t *testing.T) {
	r := startConvRig(t, 4)
	runTurn(t, r.driver, "first question")
	first := r.app.conversationID
	r.command("/new", "new conversation")
	runTurn(t, r.driver, "second question")
	second := r.app.conversationID
	if first == second {
		t.Fatal("/new did not start another conversation")
	}

	markedRow := func(v string) string {
		for _, l := range strings.Split(v, "\n") {
			if strings.HasPrefix(l, "› ") && (strings.Contains(l, first[:8]) || strings.Contains(l, second[:8])) {
				return l
			}
		}
		return ""
	}

	v := r.command("/conversations", "first question", "second question", "Enter switch")
	if got := selectedID(r.app); got != second {
		t.Fatalf("selected %q, want the newest %q", got, second)
	}
	if row := markedRow(v); !strings.Contains(row, second[:8]) || !strings.Contains(row, "second question") {
		t.Fatalf("the mark is not on the newest row:\n%s", v)
	}
	r.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zz")})
	r.send(tea.KeyMsg{Type: tea.KeyDown})
	v = r.app.View()
	if got := selectedID(r.app); got != first {
		t.Fatalf("↓ selected %q, want %q", got, first)
	}
	if row := markedRow(v); !strings.Contains(row, first[:8]) || strings.Contains(row, second[:8]) {
		t.Fatalf("the mark did not move:\n%s", v)
	}

	// Esc closes the chooser and the conversation stays.
	r.send(tea.KeyMsg{Type: tea.KeyEsc})
	v = r.app.View()
	if r.app.conv != nil || strings.Contains(v, "Enter switch") || r.app.conversationID != second {
		t.Fatalf("Esc: chooser open=%v conversation %q (want %q):\n%s", r.app.conv != nil, r.app.conversationID, second, v)
	}

	// Enter switches to the selected row.
	r.command("/conversations", "first question")
	r.send(tea.KeyMsg{Type: tea.KeyDown})
	r.send(tea.KeyMsg{Type: tea.KeyEnter})
	r.until("the switch to the first conversation", func(string) bool { return r.app.conversationID == first })
	v = r.app.View()
	if r.app.conv != nil || strings.Contains(v, "Enter switch") {
		t.Errorf("the chooser stayed open after Enter:\n%s", v)
	}
	if !strings.Contains(v, "first question") || strings.Contains(v, "second question") {
		t.Errorf("the chat does not show the switched-to conversation:\n%s", v)
	}
	if got := r.app.input.Value(); got != "" {
		t.Errorf("chooser keys reached the input: %q", got)
	}
}

// turnOverrides counts the times the engine took the turn to overridden.
func turnOverrides(r *rig, turn string) int {
	n := 0
	for _, st := range r.h.TurnStates(turn) {
		if st == "overridden" {
			n++
		}
	}
	return n
}

// /override with no reason opens the form: an empty Enter is refused and
// the form stays, nothing is journalled; a reason sends it and the chat
// shows it; Esc sends nothing.
func TestV6OverrideReasonForm(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := turnIn(t, r, "incomplete")

	r.typeLine("/override")
	v := untilView(t, r, "override turn "+turn, "reason>", "Type a reason for the override")
	if strings.Contains(v, "HUMAN OVERRIDE") {
		t.Fatalf("overridden before a reason:\n%s", v)
	}

	before := sent(r)
	send(r, tea.KeyMsg{Type: tea.KeyEnter})
	v = untilView(t, r, "reason required", "reason>", "override turn "+turn)
	if n := turnOverrides(r, turn); n != 0 || sent(r) != before {
		t.Fatalf("an empty reason sent something: %d overrides", n)
	}
	if strings.Contains(v, "HUMAN OVERRIDE") {
		t.Fatalf("an empty reason overrode the turn:\n%s", v)
	}

	// Esc cancels with nothing sent; typed text goes with it.
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("half")}, tea.KeyMsg{Type: tea.KeyEsc})
	v = untilView(t, r, "override cancelled; nothing sent")
	if strings.Contains(v, "reason>") || strings.Contains(v, "half") || sent(r) != before || turnOverrides(r, turn) != 0 {
		t.Fatalf("Esc left the form or sent:\n%s", v)
	}

	// A reason is sent: the engine journals it and the chat shows it.
	r.typeLine("/override")
	untilView(t, r, "reason>")
	r.typeLine("accepted after manual review")
	v = untilView(t, r, "HUMAN OVERRIDE: accepted after manual review")
	if strings.Contains(v, "reason>") || strings.Contains(v, "INCOMPLETE") {
		t.Fatalf("the form or the old outcome stayed:\n%s", v)
	}
	if n := turnOverrides(r, turn); n != 1 {
		t.Fatalf("the engine journalled %d overrides, want 1", n)
	}
}

// /release asks for a reason the same way: refused when empty, cancelled by
// Esc, sent with a reason, which frees the hold.
func TestV6ReleaseReasonForm(t *testing.T) {
	r := holdRig(t, enginetest.RosterTOML, []string{"reviewer"}, "default", 1)
	r.typeLine("/release default")
	untilView(t, r, "release held capacity on pool default", "may overlap it", "reason>")

	before := sent(r)
	send(r, tea.KeyMsg{Type: tea.KeyEnter})
	untilView(t, r, "reason required", "reason>")
	if sent(r) != before || len(r.app.state.Pools["default"].Holds) != 1 {
		t.Fatalf("an empty reason released: sent %d -> %d", before, sent(r))
	}

	send(r, tea.KeyMsg{Type: tea.KeyEsc})
	v := untilView(t, r, "release cancelled; nothing sent")
	if strings.Contains(v, "reason>") || sent(r) != before {
		t.Fatalf("Esc left the form or sent:\n%s", v)
	}

	r.typeLine("/release default")
	untilView(t, r, "reason>")
	r.typeLine("the server is idle")
	untilView(t, r, "released on pool default: ")
	r.until(t, "the hold gone", func(a *App) (string, bool) { return "", len(holdsOn(a, "default")) == 0 })
	if sent(r) != before+1 {
		t.Errorf("sent %d -> %d, want exactly one release", before, sent(r))
	}
}

// In a plain terminal (here, a colour terminal shrunk below 80x24) the form
// is the single-line reason prompt with the same refusals.
func TestV6ReasonPromptInPlainTerminal(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := turnIn(t, r, "incomplete")
	send(r, tea.WindowSizeMsg{Width: 79, Height: 24})
	r.until(t, "plain mode", func(a *App) (string, bool) { return "", a.plainMode() })

	r.typeLine("/override")
	v := untilView(t, r, "override turn "+turn, reasonPrompt)
	if strings.Contains(v, "\x1b") {
		t.Fatalf("plain prompt holds escape sequences:\n%q", v)
	}

	before := sent(r)
	send(r, tea.KeyMsg{Type: tea.KeyEnter})
	untilView(t, r, "reason required", reasonPrompt)
	if sent(r) != before || turnOverrides(r, turn) != 0 {
		t.Fatalf("an empty reason was sent")
	}
	send(r, tea.KeyMsg{Type: tea.KeyEsc})
	untilView(t, r, "override cancelled; nothing sent")

	r.typeLine("/override")
	untilView(t, r, reasonPrompt)
	r.typeLine("fine by me")
	untilView(t, r, "HUMAN OVERRIDE: fine by me")
	if n := turnOverrides(r, turn); n != 1 {
		t.Fatalf("the engine journalled %d overrides, want 1", n)
	}
}

// The v6 contract has no /apply: it is an unknown command, opens no prompt
// and sends nothing.
func TestV6ApplyIsNotACommand(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	before := sent(r)
	r.typeLine("/apply")
	v := untilView(t, r, "unknown command: /apply")
	for _, bad := range []string{"Apply", "Discard", "Not now", "reason>"} {
		if strings.Contains(v, bad) {
			t.Errorf("/apply opened a prompt holding %q:\n%s", bad, v)
		}
	}
	if sent(r) != before || len(openTurns(t, r.paths)) != 0 {
		t.Error("/apply sent something to the engine")
	}
}
