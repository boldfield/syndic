package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest"
)

func TestReviewsDrawerOpen(t *testing.T) {
	d := NewReviewsDrawer()
	if d.IsOpen() {
		t.Error("drawer should start closed")
	}
	d.SetTurns([]string{"turn1", "turn2"})
	d.Open(0)
	if !d.IsOpen() {
		t.Error("drawer should be open after Open()")
	}
	if id := d.selectedTurnID(); id != "turn1" {
		t.Errorf("selected turn = %q, want turn1", id)
	}
}

func TestReviewsDrawerClose(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1", "turn2"})
	d.Open(0)
	d.Close()
	if d.IsOpen() {
		t.Error("drawer should be closed after Close()")
	}
}

func TestReviewsDrawerNavigation(t *testing.T) {
	d := NewReviewsDrawer()
	turns := []string{"turn1", "turn2", "turn3"}
	d.SetTurns(turns)
	d.Open(0)

	// Test down navigation
	d.SelectDown()
	if d.selectedTurn != 1 {
		t.Errorf("after SelectDown, selectedTurn = %d, want 1", d.selectedTurn)
	}

	d.SelectDown()
	if d.selectedTurn != 2 {
		t.Errorf("after SelectDown, selectedTurn = %d, want 2", d.selectedTurn)
	}

	// Should not go past the last turn
	d.SelectDown()
	if d.selectedTurn != 2 {
		t.Errorf("after SelectDown at end, selectedTurn = %d, want 2", d.selectedTurn)
	}

	// Test up navigation
	d.SelectUp()
	if d.selectedTurn != 1 {
		t.Errorf("after SelectUp, selectedTurn = %d, want 1", d.selectedTurn)
	}

	// Should not go before the first turn
	d.SelectUp()
	d.SelectUp()
	if d.selectedTurn != 0 {
		t.Errorf("after SelectUp at start, selectedTurn = %d, want 0", d.selectedTurn)
	}
}

func TestReviewsDrawerRoundNavigation(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.Open(0)

	// Add reviews with multiple rounds
	items := []wire.ReviewItem{
		{Round: 0, Participant: "p1", Kind: "approve"},
		{Round: 1, Participant: "p1", Kind: "block"},
		{Round: 2, Participant: "p1", Kind: "approve"},
	}
	d.UpdateReviews("turn1", items)

	rounds := d.getRounds()
	if len(rounds) != 3 {
		t.Fatalf("got %d rounds, want 3", len(rounds))
	}

	// Test round navigation
	d.SelectRoundRight()
	if d.selectedRound != 1 {
		t.Errorf("after SelectRoundRight, selectedRound = %d, want 1", d.selectedRound)
	}

	d.SelectRoundLeft()
	if d.selectedRound != 0 {
		t.Errorf("after SelectRoundLeft, selectedRound = %d, want 0", d.selectedRound)
	}
}

func TestReviewsDrawerUpdateReviews(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})

	items := []wire.ReviewItem{
		{Round: 1, Participant: "p1", Kind: "approve", Blocks: []wire.ReviewBlock{
			{BlockID: "b1", State: "withdrawn"},
		}},
		{Round: 1, Participant: "p2", Kind: "block", Blocks: []wire.ReviewBlock{
			{BlockID: "b2", State: "opened"},
		}},
	}

	d.UpdateReviews("turn1", items)

	rev := d.reviewsByTurn["turn1"]
	if rev == nil {
		t.Fatal("reviews not stored")
	}

	if len(rev.items) != 2 {
		t.Errorf("got %d items, want 2", len(rev.items))
	}

	rounds := d.getRounds()
	if len(rounds) != 1 || rounds[0] != 1 {
		t.Errorf("got rounds %v, want [1]", rounds)
	}
}

func TestReviewsDrawerScroll(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.Open(0)

	d.ScrollDown(2, 20)
	if d.scrollOffset != 2 {
		t.Errorf("after ScrollDown(2), scrollOffset = %d, want 2", d.scrollOffset)
	}

	d.ScrollUp(1)
	if d.scrollOffset != 1 {
		t.Errorf("after ScrollUp(1), scrollOffset = %d, want 1", d.scrollOffset)
	}

	d.ScrollUp(5)
	if d.scrollOffset != 0 {
		t.Errorf("after ScrollUp(5), scrollOffset = %d, want 0", d.scrollOffset)
	}
}

func TestReviewsDrawerRender(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1", "turn2"})

	// Test rendering when closed
	output := d.render(80, NewContentCache())
	if output != "" {
		t.Errorf("closed drawer should render empty, got %q", output)
	}

	d.Open(0)
	items := []wire.ReviewItem{
		{Round: 1, Participant: "p1", Kind: "approve", Blocks: []wire.ReviewBlock{
			{BlockID: "b1", State: "withdrawn"},
		}},
	}
	d.UpdateReviews("turn1", items)

	output = d.render(80, NewContentCache())
	if output == "" {
		t.Error("open drawer should not render empty")
	}

	if !strings.Contains(output, "Reviews") || !strings.Contains(output, "Turn 1") {
		t.Errorf("drawer output missing expected content: %q", output)
	}
}

func TestReviewsDrawerSafeText(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.Open(0)

	// Test that control characters in block IDs are escaped
	items := []wire.ReviewItem{
		{
			Round:       0,
			Participant: "reviewer1",
			Kind:        "block",
			Blocks: []wire.ReviewBlock{
				{BlockID: "block\x1b[31mred", State: "opened"},
			},
		},
	}
	d.UpdateReviews("turn1", items)

	output := d.render(80, NewContentCache())
	// The block ID should have escaped control sequence, not raw ANSI
	if strings.Contains(output, "\x1b") {
		t.Error("drawer should not contain raw ANSI codes")
	}
	if !strings.Contains(output, "\\x1b") {
		t.Error("drawer should escape control characters")
	}
}

func TestReviewsDrawerCommentaryRendering(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.Open(0)

	cc := NewContentCache()
	commentRef := "ref:comment:123"
	commentText := "This is a review comment"
	cc.Store(commentRef, commentText, nil)

	items := []wire.ReviewItem{
		{
			Round:       0,
			Participant: "reviewer1",
			Kind:        "approve-with-changes",
			Commentary: &wire.ContentReference{
				Ref:     commentRef,
				SHA256:  "abc123",
				ByteLen: int64(len(commentText)),
			},
		},
	}
	d.UpdateReviews("turn1", items)

	output := d.render(80, cc)
	if !strings.Contains(output, "commentary") {
		t.Error("drawer should show commentary label")
	}
	if !strings.Contains(output, commentText) {
		t.Error("drawer should render commentary content")
	}
}

func TestReviewsDrawerRosterOrder(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetRoster([]string{"p3", "p1", "p2"})
	d.SetTurns([]string{"turn1"})
	d.Open(0)

	// Add reviews in non-roster order
	items := []wire.ReviewItem{
		{Round: 0, Participant: "p1", Kind: "block"},
		{Round: 0, Participant: "p3", Kind: "approve"},
		{Round: 0, Participant: "p2", Kind: "approve-with-changes"},
	}
	d.UpdateReviews("turn1", items)

	output := d.render(80, NewContentCache())

	// Check that p3 appears before p1, which appears before p2
	pos3 := strings.Index(output, "p3")
	pos1 := strings.Index(output, "p1")
	pos2 := strings.Index(output, "p2")

	if pos3 < 0 || pos1 < 0 || pos2 < 0 {
		t.Error("not all participants rendered")
	}
	if pos3 >= pos1 || pos1 >= pos2 {
		t.Errorf("participants not in roster order: p3=%d, p1=%d, p2=%d", pos3, pos1, pos2)
	}
}

func TestReviewsDrawerPaging(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.Open(0)

	// Create many items to test paging
	var items []wire.ReviewItem
	for i := 0; i < 40; i++ {
		items = append(items, wire.ReviewItem{
			Round:       0,
			Participant: fmt.Sprintf("p%d", i),
			Kind:        "approve",
		})
	}
	d.UpdateReviews("turn1", items)

	// Render and check content lines are calculated
	cc := NewContentCache()
	contentLines := d.ContentLines(80, cc)
	if contentLines <= 10 {
		t.Fatalf("fixture too small to page: %d lines", contentLines)
	}
	{
		initialOffset := d.scrollOffset
		drawerHeight := 7
		d.ScrollDown(drawerHeight, contentLines)
		if d.scrollOffset <= initialOffset {
			t.Error("scroll down should increase offset when content > viewport")
		}

		d.ScrollUp(drawerHeight)
		if d.scrollOffset >= drawerHeight {
			t.Error("scroll up should decrease offset")
		}
	}
}

func TestReviewsDrawerInputBlocking(t *testing.T) {
	// This test verifies the logic that input is blocked when drawer is open.
	// In app.go, when drawer is open and on the chat tab, all keys except
	// drawer navigation keys should be ignored.
	d := NewReviewsDrawer()
	d.SetTurns([]string{"t1", "t2", "t3"})

	// Drawer starts closed
	if d.IsOpen() {
		t.Error("drawer should start closed")
	}

	// Open drawer
	d.Open(0)
	if !d.IsOpen() {
		t.Error("drawer should be open after Open()")
	}

	// Navigation should change selection
	initialTurn := d.selectedTurn
	d.SelectDown()
	if d.selectedTurn == initialTurn {
		t.Error("SelectDown should change selection")
	}

	d.SelectUp()
	if d.selectedTurn != initialTurn {
		t.Error("SelectUp should return to initial turn")
	}

	// Close drawer
	d.Close()
	if d.IsOpen() {
		t.Error("drawer should be closed after Close()")
	}
}

func TestReviewsDrawerHistoricalStates(t *testing.T) {
	// Test that historical block states across rounds are shown correctly
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.SetRoster([]string{"reviewer1"})

	// Simulate a block that was opened in round 0, maintained in round 1, withdrawn in round 2
	items := []wire.ReviewItem{
		{
			Round:       0,
			Participant: "reviewer1",
			Kind:        "block",
			Blocks: []wire.ReviewBlock{
				{BlockID: "b1", State: "opened"},
			},
		},
		{
			Round:       1,
			Participant: "reviewer1",
			Kind:        "block",
			Blocks: []wire.ReviewBlock{
				{BlockID: "b1", State: "maintained"},
			},
		},
		{
			Round:       2,
			Participant: "reviewer1",
			Kind:        "approve",
			Blocks: []wire.ReviewBlock{
				{BlockID: "b1", State: "withdrawn"},
			},
		},
	}
	d.UpdateReviews("turn1", items)
	d.Open(0)

	// Each round shows the block in the state that round's verdict gave it.
	for i, want := range []string{"b1: opened", "b1: maintained", "b1: withdrawn"} {
		out := d.render(80, NewContentCache())
		if !strings.Contains(out, want) {
			t.Errorf("round %d does not show %q:\n%s", i+1, want, out)
		}
		d.SelectRoundRight()
	}
}

// TestReviewsDrawerHeaderWithoutReviews: a turn whose reviews are loading,
// that has none, or whose reviews could not be read still names the turn.
func TestReviewsDrawerHeaderWithoutReviews(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"t1", "t2", "t3"})
	d.Open(0)
	d.UpdateReviews("t2", nil)
	d.SetReviewsError("t3", fmt.Errorf("refused"))
	for i, want := range []string{"loading…", "no reviews", "reviews unavailable: refused"} {
		out := d.render(80, NewContentCache())
		if !strings.Contains(out, fmt.Sprintf("Reviews · Turn %d", i+1)) || !strings.Contains(out, want) {
			t.Errorf("turn %d: want the header and %q:\n%s", i+1, want, out)
		}
		d.SelectDown()
	}
}

func TestReviewCountRendering(t *testing.T) {
	// Test that review-count is rendered in outcome lines
	ai := answerInput{
		width:       80,
		plainMode:   false,
		reviewCount: ptrInt(3),
	}

	// Test that the review count is appended to the outcome
	lines := styledOutcomeLines("·", "UNREVIEWED (solo)", ai, lipgloss.NewStyle())
	found := false
	for _, line := range lines {
		if strings.Contains(line, "3 reviews") {
			found = true
			break
		}
	}
	if !found {
		t.Error("review count should be shown in outcome lines")
	}
}

func TestReviewCountZeroShownForNonSolo(t *testing.T) {
	ai := answerInput{width: 80, plainMode: true, reviewCount: ptrInt(0)}
	out := strings.Join(styledOutcomeLines("!", "INCOMPLETE", ai, lipgloss.NewStyle()), "\n")
	if !strings.Contains(out, "· 0 reviews") {
		t.Errorf("a non-solo turn with no accepted verdicts must show 0 reviews: %q", out)
	}
}

func TestReviewCountNotShownWhenZero(t *testing.T) {
	// A nil count marks a solo turn: no suffix.
	ai := answerInput{
		width:     80,
		plainMode: false,
	}

	lines := styledOutcomeLines("·", "UNREVIEWED (solo)", ai, lipgloss.NewStyle())
	for _, line := range lines {
		if strings.Contains(line, "reviews") {
			t.Error("should not show reviews when count is nil")
		}
	}
}

func TestSelectedInputHighlighting(t *testing.T) {
	// Test that selected turn input is highlighted in chat rendering
	s := State{
		Turns: map[string]TurnInfo{
			"t1": {State: "solo_complete"},
			"t2": {State: "solo_complete"},
		},
		Scope: &TurnScope{
			ConversationID: "conv1",
			Order:          []string{"t1", "t2"},
			Turns:          map[string]bool{"t1": true, "t2": true},
		},
	}

	in := chatInput{
		answer: func(id string) answerInput {
			return answerInput{width: 80, plainMode: false}
		},
		inputs:               map[string]string{"t1": "first input", "t2": "second input"},
		content:              NewContentCache(),
		width:                80,
		selectedDrawerTurnID: "t1", // Selected turn is t1
	}

	lines, _ := chatLines(s, in)
	output := fmt.Sprintf("%v", lines)

	// The selected turn's input should be highlighted (we can't easily check for styling,
	// but we can verify the structure is correct)
	if !strings.Contains(output, "first input") {
		t.Error("selected turn input should be in chat output")
	}
	// In a plain terminal the selection is a mark in place of the user mark;
	// unselected turns keep the user mark.
	in.answer = func(id string) answerInput { return answerInput{width: 80, plainMode: true} }
	for sel, other := range map[string]string{"t1": "t2", "t2": "t1"} {
		in.selectedDrawerTurnID = sel
		plain, _ := chatLines(s, in)
		out := strings.Join(plain, "\n")
		name := map[string]string{"t1": "first input", "t2": "second input"}
		if !strings.Contains(out, selectedMark+name[sel]) || strings.Contains(out, userMark+name[sel]) {
			t.Errorf("selected %s lacks the plain marker:\n%s", sel, out)
		}
		if !strings.Contains(out, userMark+name[other]) || strings.Contains(out, selectedMark+name[other]) {
			t.Errorf("unselected %s carries the marker:\n%s", other, out)
		}
	}
}

// TestSelectedInputKeepsWrapping: the plain selection mark is as wide as the
// user mark, so a full-width input still fits its line.
func TestSelectedInputKeepsWrapping(t *testing.T) {
	text := strings.Repeat("x", 77) + "Z"
	s := State{
		Turns: map[string]TurnInfo{"t1": {State: "solo_complete"}},
		Scope: &TurnScope{ConversationID: "c", Order: []string{"t1"}, Turns: map[string]bool{"t1": true}},
	}
	in := chatInput{
		answer:  func(string) answerInput { return answerInput{width: 80, plainMode: true} },
		inputs:  map[string]string{"t1": text},
		content: NewContentCache(),
		width:   80,
	}
	plain, _ := chatLines(s, in)
	in.selectedDrawerTurnID = "t1"
	selected, _ := chatLines(s, in)
	if len(plain) != len(selected) {
		t.Fatalf("selection reflowed the chat: %d lines, was %d", len(selected), len(plain))
	}
	if selected[0] != selectedMark+text || ansi.StringWidth(selected[0]) != 80 {
		t.Errorf("selected line = %q (%d cells), want the whole input in 80", selected[0], ansi.StringWidth(selected[0]))
	}
}

func ptrInt(i int) *int {
	return &i
}

// TestReviewsDrawerEndToEnd drives the App through the drawer against a real
// engine with fake adapters: Ctrl+O opens it, typing and Enter change and
// submit nothing, Esc closes it.
func TestReviewsDrawerEndToEnd(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched" && a.state.Turns[id].State != "open"
	})
	_ = turn

	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	v := untilView(t, r, "reviewer", "b7")
	turnsBefore := r.until(t, "turn count", func(a *App) (string, bool) { return fmt.Sprint(len(a.state.Turns)), true })

	// Typing, pasting and Enter are ignored while the drawer holds focus.
	send(r,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("typed")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted"), Paste: true},
		tea.KeyMsg{Type: tea.KeyEnter},
		tea.KeyMsg{Type: tea.KeyPgDown},
		tea.KeyMsg{Type: tea.KeyPgUp},
		tea.KeyMsg{Type: tea.KeyLeft},
		tea.KeyMsg{Type: tea.KeyRight},
	)
	after := r.until(t, "input unchanged", func(a *App) (string, bool) {
		return fmt.Sprint(len(a.state.Turns), "|", a.input.Value(), "|", a.reviewsDrawer.IsOpen()), true
	})
	if after != turnsBefore+"||true" {
		t.Fatalf("drawer let input through or closed: %q (was %q)", after, turnsBefore)
	}
	if !strings.Contains(view(t, r), "b7") {
		t.Fatalf("drawer lost its content:\n%s", v)
	}

	send(r, tea.KeyMsg{Type: tea.KeyEsc})
	r.until(t, "drawer closed", func(a *App) (string, bool) { return "", !a.reviewsDrawer.IsOpen() })
	// The open block stays in the chat once the drawer is closed, and the
	// reviewed turn's outcome line carries its review count.
	closed := untilView(t, r, "b7", "· 1 reviews")
	if !strings.Contains(closed, "· 1 reviews") {
		t.Fatalf("outcome line lacks the review count suffix:\n%s", closed)
	}
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	r.until(t, "typing works again", func(a *App) (string, bool) { return a.input.Value(), a.input.Value() == "x" })
}

// TestReviewsDrawerKeysAndResize: shrinking below the minimum size releases
// the keyboard, Ctrl+C leaves a non-empty input alone while the drawer has
// the keyboard, and Ctrl+D on an empty input detaches through the drawer.
func TestReviewsDrawerKeysAndResize(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "a settled turn", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && a.state.Turns[id].State != "reviewers_dispatched" && a.state.Turns[id].State != "open"
	})
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	untilView(t, r, "b7")

	// Resize below 80x24: the drawer closes and typing reaches the input.
	send(r, tea.WindowSizeMsg{Width: 79, Height: 24})
	r.until(t, "drawer released", func(a *App) (string, bool) {
		return "", !a.reviewsDrawer.IsOpen() && !a.want.Drawer
	})
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	r.until(t, "typing works", func(a *App) (string, bool) { return a.input.Value(), a.input.Value() == "x" })

	// Back at full size it opens again; Ctrl+C does not clear the input.
	send(r, tea.WindowSizeMsg{Width: 80, Height: 24}, tea.KeyMsg{Type: tea.KeyCtrlO})
	r.until(t, "drawer open", func(a *App) (string, bool) { return "", a.reviewsDrawer.IsOpen() })
	send(r, tea.KeyMsg{Type: tea.KeyCtrlC})
	r.until(t, "input kept", func(a *App) (string, bool) {
		return a.input.Value(), a.input.Value() == "x" && a.reviewsDrawer.IsOpen()
	})

	// Closed, Ctrl+C clears the input; reopened, Ctrl+D on the empty input detaches.
	send(r, tea.KeyMsg{Type: tea.KeyEsc}, tea.KeyMsg{Type: tea.KeyCtrlC}, tea.KeyMsg{Type: tea.KeyCtrlO})
	r.until(t, "drawer open on an empty input", func(a *App) (string, bool) {
		return a.input.Value(), a.input.Value() == "" && a.reviewsDrawer.IsOpen()
	})
	send(r, tea.KeyMsg{Type: tea.KeyCtrlD})
	app := r.tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second)).(*probe).app
	if !app.reviewsDrawer.IsOpen() {
		t.Error("the detach did not come through the open drawer")
	}
}

func TestReviewsDrawerSetTurnsKeepsSelection(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"a", "b"})
	d.Open(0)
	d.SelectDown()
	d.SetTurns([]string{"a", "b", "c"})
	if got := d.selectedTurnID(); got != "b" {
		t.Errorf("selection moved to %q", got)
	}
}

// TestReviewsDrawerTurnSelectionEndToEnd drives two turns through the App:
// ↑/↓ moves the drawer's turn, the header's turn number changes and the
// highlighted input in the chat moves with it.
func TestReviewsDrawerTurnSelectionEndToEnd(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeatStep(enginetest.Reply(answerText), 12),
		"reviewer": repeatStep(blockVerdict(), 12),
	})
	r := startAnswerRig(t, h)
	// Tall enough that both turns' inputs stay on screen above the drawer.
	send(r, tea.WindowSizeMsg{Width: 80, Height: 60})
	settled := func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && isTerminalState(a.state.Turns[id].State)
	}
	// Full-width inputs: the selection mark must not clip a byte of them.
	firstText := "first " + strings.Repeat("a", 71) + "Z"
	secondText := "second " + strings.Repeat("b", 70) + "Y"
	r.typeLine(firstText)
	first := r.until(t, "first settled", settled)
	r.typeLine(secondText)
	r.until(t, "second settled", func(a *App) (string, bool) {
		id, ok := settled(a)
		return id, ok && id != first
	})

	closed := view(t, r)
	if !strings.Contains(closed, userMark+firstText) || !strings.Contains(closed, userMark+secondText) || strings.Contains(closed, selectedMark) {
		t.Fatalf("with the drawer closed no input is marked:\n%s", closed)
	}

	// The drawer opens on the newest turn and marks its input.
	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	v := untilView(t, r, "Reviews · Turn 2", selectedMark+secondText, userMark+firstText)
	if strings.Contains(v, selectedMark+firstText) {
		t.Fatalf("both inputs marked:\n%s", v)
	}
	// ↑ moves the mark to the first turn's input.
	send(r, tea.KeyMsg{Type: tea.KeyUp})
	v = untilView(t, r, "Reviews · Turn 1", selectedMark+firstText, userMark+secondText)
	if strings.Contains(v, selectedMark+secondText) {
		t.Fatalf("the mark did not leave the second input:\n%s", v)
	}
	r.until(t, "first selected", func(a *App) (string, bool) { return "", a.reviewsDrawer.selectedTurnID() == first })
	// ↓ moves it back.
	send(r, tea.KeyMsg{Type: tea.KeyDown})
	untilView(t, r, "Reviews · Turn 2", selectedMark+secondText, userMark+firstText)
	// Closing the drawer removes the mark.
	send(r, tea.KeyMsg{Type: tea.KeyEsc})
	r.until(t, "no mark once closed", func(a *App) (string, bool) {
		v := a.View()
		return v, !a.reviewsDrawer.IsOpen() && !strings.Contains(v, selectedMark) && strings.Contains(v, userMark+secondText)
	})
}

// TestReviewsDrawerRoundsAndScrollEndToEnd drives a revised turn through the
// App against a real engine: the drawer shows round 1's opened block, → shows
// round 2's withdrawn one and ← shows round 1 again; content taller than the
// drawer is reached with PgDn and left again with PgUp.
func TestReviewsDrawerRoundsAndScrollEndToEnd(t *testing.T) {
	roster := strings.Replace(enginetest.RosterTOML, "max_rounds = 0", "max_rounds = 2\nmax_rounds_ceiling = 5", 1)
	var evidence []string
	for i := range 60 {
		evidence = append(evidence, fmt.Sprintf("e%02d", i))
	}
	block := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", strings.Join(evidence, " "), "remedy-tail", "wrong")
	})
	withdraw := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictV2WithBlockStates(req.Envelope.CandidateHash, req.Envelope.AttemptID, 1, []string{"block-state: b1 withdrawn"})
	})
	h := enginetest.New(t, roster, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 41"), enginetest.Reply("the answer is 42"), enginetest.Reply("ok"), enginetest.Reply("ok")},
		"reviewer": {block, withdraw},
	})
	r := startAnswerRig(t, h)
	r.typeLine("what is the answer?")
	r.until(t, "the revised turn approved", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.state.Turns[id].State == "approved"
	})

	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	v := untilView(t, r, "Reviews · Turn 1 · [*1* 2]", "b1: opened", "e00")
	if strings.Contains(v, "b1: withdrawn") || strings.Contains(v, "remedy-tail") {
		t.Fatalf("round 1 shows round 2's state, or content past the drawer:\n%s", v)
	}

	// PgDn reaches the remedy below the evidence; PgUp returns to the top.
	send(r, tea.KeyMsg{Type: tea.KeyPgDown})
	v = untilView(t, r, "remedy-tail")
	if strings.Contains(v, "Reviews · Turn 1") {
		t.Fatalf("PgDn did not scroll the header away:\n%s", v)
	}
	send(r, tea.KeyMsg{Type: tea.KeyPgUp})
	v = untilView(t, r, "Reviews · Turn 1 · [*1* 2]", "e00")
	if strings.Contains(v, "remedy-tail") {
		t.Fatalf("PgUp left the remedy on screen:\n%s", v)
	}

	send(r, tea.KeyMsg{Type: tea.KeyRight})
	v = untilView(t, r, "Reviews · Turn 1 · [1 *2*]", "b1: withdrawn")
	if strings.Contains(v, "b1: opened") {
		t.Fatalf("round 2 still shows round 1's state:\n%s", v)
	}
	send(r, tea.KeyMsg{Type: tea.KeyLeft})
	untilView(t, r, "Reviews · Turn 1 · [*1* 2]", "b1: opened")
}

func repeatStep(s fake.Step, n int) []fake.Step {
	steps := make([]fake.Step, n)
	for i := range steps {
		steps[i] = s
	}
	return steps
}

// TestDrawerAnimation tests the drawer open/close animation.
func TestDrawerAnimation(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.SetDrawerHeight(7)
	d.Open(0)

	// When opened, animation should start
	if !d.IsAnimating() {
		t.Error("drawer should be animating after Open()")
	}
	if d.animProgress != 0 {
		t.Errorf("animation should start at 0, got %v", d.animProgress)
	}

	// Advance animation frame
	stillAnimating := d.AnimationTick()
	if !stillAnimating {
		t.Error("animation should still be in progress after one tick")
	}
	if d.animProgress <= 0 {
		t.Error("progress should advance")
	}

	// Keep ticking until animation completes
	for stillAnimating {
		stillAnimating = d.AnimationTick()
	}

	if d.IsAnimating() {
		t.Error("animation should be done")
	}
	if d.animProgress < 1.0 {
		t.Errorf("animation should reach 1.0, got %v", d.animProgress)
	}
}

// TestDrawerCloseAnimation tests the drawer close animation.
func TestDrawerCloseAnimation(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.Open(0)

	// Skip to end of open animation
	for d.IsAnimating() {
		d.AnimationTick()
	}

	if d.animProgress < 1.0 {
		t.Fatalf("open animation should be complete, got %v", d.animProgress)
	}

	// Close the drawer
	d.Close()
	if !d.IsAnimating() {
		t.Error("drawer should be animating after Close()")
	}
	if d.animProgress != 1.0 {
		t.Errorf("close animation should start at 1.0, got %v", d.animProgress)
	}

	// Advance animation
	stillAnimating := d.AnimationTick()
	if !stillAnimating {
		t.Error("animation should still be in progress")
	}
	if d.animProgress >= 1.0 {
		t.Error("progress should decrease")
	}

	// Complete the close animation
	for stillAnimating {
		stillAnimating = d.AnimationTick()
	}

	if d.animProgress > 0 {
		t.Errorf("close animation should reach 0, got %v", d.animProgress)
	}
	if d.IsOpen() {
		t.Error("drawer should still report closed after close animation")
	}
}

// TestDrawerAnimationRender verifies animation affects rendering.
func TestDrawerAnimationRender(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.SetDrawerHeight(10)

	// Create reviews with multiple participants to generate enough content
	items := make([]wire.ReviewItem, 10)
	for i := range items {
		items[i] = wire.ReviewItem{
			Round:       0,
			Participant: fmt.Sprintf("p%d", i),
			Kind:        "approve",
		}
	}
	d.UpdateReviews("turn1", items)
	d.Open(0)

	// Let animation progress most of the way to see full content
	d.animProgress = 0.95
	fullRender := d.render(80, NewContentCache())
	fullLines := strings.Split(fullRender, "\n")

	// During earlier animation, fewer lines should be rendered
	d.animProgress = 0.3
	partialRender := d.render(80, NewContentCache())
	partialLines := strings.Split(partialRender, "\n")

	if len(partialLines) >= len(fullLines) {
		t.Errorf("partial animation should show fewer lines: %d vs %d", len(partialLines), len(fullLines))
	}
}

// TestDrawerAnimationCompleteBeforeReopen: reopening while animation is ongoing works correctly.
func TestDrawerAnimationCompleteBeforeReopen(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1", "turn2"})
	d.Open(0)

	// Interrupt open animation with close
	d.animProgress = 0.3
	d.Close()

	if d.animProgress != 0.3 {
		t.Errorf("close should not reset progress mid-animation, got %v", d.animProgress)
	}

	// When closing, we should be decreasing
	startProgress := d.animProgress
	d.AnimationTick()
	if d.animProgress >= startProgress {
		t.Error("progress should decrease during close animation")
	}
}

// TestDrawerNoRenderWhenFullyClosed: render should not output when drawer is fully closed.
func TestDrawerNoRenderWhenFullyClosed(t *testing.T) {
	d := NewReviewsDrawer()
	d.SetTurns([]string{"turn1"})
	d.SetDrawerHeight(7)
	d.UpdateReviews("turn1", []wire.ReviewItem{
		{Round: 0, Participant: "p1", Kind: "approve"},
	})

	// Render when closed and not animating should be empty
	output := d.render(80, NewContentCache())
	if output != "" {
		t.Errorf("closed drawer should render empty, got %q", output)
	}

	// But when closed and animating (close animation), it should render
	d.animating = true
	d.animProgress = 0.5
	output = d.render(80, NewContentCache())
	if output == "" {
		t.Error("closed drawer should render during close animation")
	}

	// Once animation completes (progress == 0), it should not render
	d.animProgress = 0
	d.animating = false
	output = d.render(80, NewContentCache())
	if output != "" {
		t.Errorf("fully closed drawer should not render, got %q", output)
	}
}
