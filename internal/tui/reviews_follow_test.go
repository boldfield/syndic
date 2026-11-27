package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/tui/followtest"
	"github.com/charmbracelet/x/ansi"
)

// drawerView adapts the reviews drawer to the shared case table.
type drawerView struct {
	d    *ReviewsDrawer
	rows []FollowRow
	cc   *ContentCache
}

func newDrawerView() *drawerView {
	v := &drawerView{d: reviewsDrawerOf(1, 8), cc: NewContentCache()}
	v.d.rowsFn = func() ([]string, []FollowRow) {
		lines := make([]string, len(v.rows))
		for i, r := range v.rows {
			lines[i] = fmt.Sprintf("%s %d", r.Item, r.Char)
		}
		return lines, v.rows
	}
	return v
}

func (v *drawerView) SetRows(rows []followtest.Row) {
	v.rows = v.rows[:0:0]
	for _, r := range rows {
		v.rows = append(v.rows, FollowRow{r.Item, r.Char, r.End})
	}
	v.d.layout(80, v.cc)
}

// SetHeight gives the drawer h rows of content under its header line.
func (v *drawerView) SetHeight(h int) { v.d.SetDrawerHeight(h + 1); v.d.layout(80, v.cc) }

func (v *drawerView) Press(k followtest.Key) {
	page := max(v.d.viewRows()-1, 1)
	switch k {
	case followtest.Up:
		v.d.Scroll(-1, 80, v.cc)
	case followtest.Down:
		v.d.Scroll(1, 80, v.cc)
	case followtest.PageUp:
		v.d.Scroll(-page, 80, v.cc)
	case followtest.PageDown:
		v.d.Scroll(page, 80, v.cc)
	case followtest.Home:
		v.d.Home(80, v.cc)
	case followtest.End:
		v.d.End()
	}
}

func (v *drawerView) First() int     { _, _, first, _, _ := v.d.layout(80, v.cc); return first }
func (v *drawerView) AtBottom() bool { _, _, _, bottom, _ := v.d.layout(80, v.cc); return bottom }
func (v *drawerView) Count() int     { _, _, _, _, count := v.d.layout(80, v.cc); return count }

func TestDrawerFollowCases(t *testing.T) {
	followtest.Run(t, func() followtest.View { return newDrawerView() })
}

// TestEveryScrollingViewPassesTheSharedCases runs the one case table against the chat, the
// transcript and the drawer, and fails if any of them is missing.
func TestEveryScrollingViewPassesTheSharedCases(t *testing.T) {
	views := map[string]func() followtest.View{
		"chat": func() followtest.View {
			v := &chatAdaptor{a: chatApp(t)}
			v.a.chatRowsFn = func() ([]string, []FollowRow, []int) {
				lines := make([]string, len(v.rows))
				for i, r := range v.rows {
					lines[i] = fmt.Sprintf("%s %d", r.Item, r.Char)
				}
				return lines, v.rows, nil
			}
			return v
		},
		"transcript": func() followtest.View { return &transcriptView{a: transcriptApp(80, 24)} },
		"drawer":     func() followtest.View { return newDrawerView() },
	}
	for _, name := range []string{"chat", "transcript", "drawer"} {
		newView, ok := views[name]
		if !ok {
			t.Fatalf("the %s is missing from the shared cases", name)
		}
		t.Run(name, func(t *testing.T) { followtest.Run(t, newView) })
	}
	if len(views) != 3 {
		t.Fatalf("%d views in the table, want the chat, the transcript and the drawer", len(views))
	}
}

func TestDrawerOpensShowingItsLastRow(t *testing.T) {
	d := reviewsDrawerOf(30, 7)
	out := strings.Split(ansi.Strip(d.render(80, NewContentCache(), true)), "\n")
	if len(out) != 7 || !strings.Contains(out[len(out)-1], "p29") {
		t.Fatalf("the drawer does not end on its last row:\n%s", strings.Join(out, "\n"))
	}
	d.Scroll(-10, 80, NewContentCache())
	d.Close(true)
	d.Open(0, true)
	if !d.AtBottom() {
		t.Fatal("reopened away from the bottom")
	}
	out = strings.Split(ansi.Strip(d.render(80, NewContentCache(), true)), "\n")
	if !strings.Contains(out[len(out)-1], "p29") {
		t.Fatalf("reopened, the drawer does not end on its last row:\n%s", strings.Join(out, "\n"))
	}
}

func TestDrawerCountIsOnItsHeaderLine(t *testing.T) {
	cc := NewContentCache()
	d := reviewsDrawerOf(30, 7)
	d.Scroll(-10, 80, cc)
	d.UpdateReviews("turn1", reviewsDrawerOf(35, 7).reviewsByTurn["turn1"].items)
	head := strings.Split(ansi.Strip(d.render(80, cc, true)), "\n")[0]
	if !strings.Contains(head, "↓ 5 new lines · ") || !strings.HasSuffix(strings.TrimRight(head, " "), "%") {
		t.Fatalf("header line %q", head)
	}
	d.End()
	if head := strings.Split(ansi.Strip(d.render(80, cc, true)), "\n")[0]; strings.Contains(head, "new lines") || !strings.HasSuffix(head, "100%") {
		t.Fatalf("header line at the bottom %q", head)
	}
}
