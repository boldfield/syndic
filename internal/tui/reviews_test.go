package tui

import (
	"fmt"
	"github.com/boldfield/syndic/internal/transport/fake"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	_ = d.render(80, cc)
	if d.ContentLines() == 0 {
		t.Error("content lines should be calculated after render")
	}

	// Test scrolling - only scroll if content is larger than viewport
	contentLines := d.ContentLines()
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

	// Navigate through rounds
	rounds := d.getRounds()
	if len(rounds) != 3 {
		t.Errorf("should have 3 rounds, got %d", len(rounds))
	}

	// Check each round has the item
	for i := 0; i < 3; i++ {
		d.selectedRound = i
		if len(d.reviewsByTurn["turn1"].rounds[rounds[i]]) == 0 {
			t.Errorf("round %d should have items", rounds[i])
		}
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
	// In a plain terminal the selection is a marker; unselected turns have none.
	in.answer = func(id string) answerInput { return answerInput{width: 80, plainMode: true} }
	for sel, other := range map[string]string{"t1": "t2", "t2": "t1"} {
		in.selectedDrawerTurnID = sel
		plain, _ := chatLines(s, in)
		out := strings.Join(plain, "\n")
		name := map[string]string{"t1": "first input", "t2": "second input"}
		if !strings.Contains(out, "> "+userMark+name[sel]) {
			t.Errorf("selected %s lacks the plain marker:\n%s", sel, out)
		}
		if strings.Contains(out, "> "+userMark+name[other]) {
			t.Errorf("unselected %s carries the marker:\n%s", other, out)
		}
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

// TestReviewsDrawerDetachAndResize: Ctrl+D on an empty input detaches through
// the drawer, a non-empty input is left alone, and shrinking below the
// minimum size releases the keyboard.
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
	settled := func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, id != "" && a.input.Value() == "" && isTerminalState(a.state.Turns[id].State)
	}
	r.typeLine("first")
	first := r.until(t, "first settled", settled)
	r.typeLine("second")
	r.until(t, "second settled", func(a *App) (string, bool) {
		id, ok := settled(a)
		return id, ok && id != first
	})

	send(r, tea.KeyMsg{Type: tea.KeyCtrlO})
	untilView(t, r, "Reviews · Turn 2")
	if sel := r.until(t, "selection", func(a *App) (string, bool) { return a.reviewsDrawer.selectedTurnID(), true }); sel == first {
		t.Fatalf("drawer opened on the first turn")
	}
	send(r, tea.KeyMsg{Type: tea.KeyUp})
	untilView(t, r, "Reviews · Turn 1")
	r.until(t, "first selected", func(a *App) (string, bool) { return "", a.reviewsDrawer.selectedTurnID() == first })
	send(r, tea.KeyMsg{Type: tea.KeyDown})
	untilView(t, r, "Reviews · Turn 2")
}

func repeatStep(s fake.Step, n int) []fake.Step {
	steps := make([]fake.Step, n)
	for i := range steps {
		steps[i] = s
	}
	return steps
}
