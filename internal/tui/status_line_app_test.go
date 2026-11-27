package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func statusApp(t *testing.T) *App {
	t.Helper()
	a := chatApp(t)
	a.state = revisingState()
	a.now = func() time.Time { return stripT0.Add(30 * time.Second) }
	return a
}

// On the transcript tab the line never carries ctrl+o: reviews, and step 2
// removes nothing there: the line is what it is on the chat tab with the hint
// absent.
func TestStatusLineTranscriptTabNeverOffersReviews(t *testing.T) {
	a := statusApp(t)
	for w := 80; w <= 200; w++ {
		a.tab = tabTranscript
		got := a.statusLine(w)
		if strings.Contains(got, reviewsHint) {
			t.Fatalf("w=%d: reviews hint on the transcript tab: %q", w, got)
		}
		// Step 2 is skipped: round n/m goes only when the line does not fit
		// without the hint, so it stays wherever the chat tab's line, minus
		// its hint, would keep it.
		if w >= 94 && !strings.Contains(got, "round 1/3") {
			t.Errorf("w=%d: round removed though the line fits: %q", w, got)
		}
	}
}

// Widening from 80 to 120 columns in one process restores the removed items.
func TestStatusLineWideningRestoresItems(t *testing.T) {
	a := statusApp(t)
	a.tab = tabChat
	narrow := a.statusLine(80)
	for _, gone := range []string{"round 1/3", reviewsHint, "█"} {
		if strings.Contains(narrow, gone) {
			t.Fatalf("80 columns still shows %q: %q", gone, narrow)
		}
	}
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	wide := chatView(a)[23]
	for _, back := range []string{"round 1/3", reviewsHint} {
		if !strings.Contains(wide, back) {
			t.Errorf("120 columns lacks %q: %q", back, wide)
		}
	}
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := chatView(a)[23]; got != narrow {
		t.Errorf("narrowed again: %q, want %q", got, narrow)
	}
}

// Ctrl+O opens the drawer while ctrl+o: reviews is not shown.
func TestCtrlOOpensDrawerWithoutTheHint(t *testing.T) {
	a := statusApp(t)
	a.tab = tabChat
	if strings.Contains(a.statusLine(80), reviewsHint) {
		t.Fatal("the hint is shown at 80 columns")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	settle(t, a)
	if !a.reviewsDrawer.IsOpen() {
		t.Fatal("ctrl+o did not open the drawer")
	}
}
