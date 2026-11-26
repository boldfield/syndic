package tui

import (
	"testing"

	"github.com/boldfield/syndic/internal/tui/followtest"
)

type followAdaptor struct {
	f      *Follow
	rows   []FollowRow
	height int
	first  int
	bottom bool
	count  int
}

func newFollowAdaptor() followtest.Adaptor { return &followAdaptor{f: NewFollow()} }

func (a *followAdaptor) relayout() {
	a.first, a.bottom, a.count = a.f.Layout(a.rows, a.height)
}

func (a *followAdaptor) SetRows(rows []followtest.Row) {
	a.rows = a.rows[:0]
	for _, r := range rows {
		a.rows = append(a.rows, FollowRow{Item: r.Item, Char: r.Char})
	}
	a.relayout()
}

func (a *followAdaptor) SetHeight(h int) { a.height = h; a.relayout() }

func (a *followAdaptor) Press(key string) {
	switch key {
	case "up":
		a.first, a.bottom, a.count = a.f.Scroll(a.rows, a.height, -1)
	case "down":
		a.first, a.bottom, a.count = a.f.Scroll(a.rows, a.height, 1)
	case "pgup":
		a.first, a.bottom, a.count = a.f.Scroll(a.rows, a.height, -a.height)
	case "pgdown":
		a.first, a.bottom, a.count = a.f.Scroll(a.rows, a.height, a.height)
	case "home":
		a.first, a.bottom, a.count = a.f.Home(a.rows, a.height)
	case "end":
		a.first, a.bottom, a.count = a.f.End(a.rows, a.height)
	}
}

func (a *followAdaptor) First() int     { return a.first }
func (a *followAdaptor) AtBottom() bool { return a.bottom }
func (a *followAdaptor) Count() int     { return a.count }

func TestFollowCases(t *testing.T) { followtest.Run(t, newFollowAdaptor) }

func frows(parts ...[]followtest.Row) []FollowRow {
	var out []FollowRow
	for _, p := range followtest.Join(parts...) {
		out = append(out, FollowRow{Item: p.Item, Char: p.Char})
	}
	return out
}

func TestFollowStartsAtBottom(t *testing.T) {
	f := NewFollow()
	first, bottom, n := f.Layout(frows(followtest.Item("a", 9)), 4)
	if first != 5 || !bottom || n != 0 {
		t.Fatalf("got %d %v %d", first, bottom, n)
	}
}

func TestFollowStoresNoRowNumber(t *testing.T) {
	f := NewFollow()
	rows := frows(followtest.Item("a", 3), followtest.Item("b", 20))
	f.Layout(rows, 4)
	f.Scroll(rows, 4, -8) // first row 12
	first, _, _ := f.Layout(rows, 4)
	if first != 11 {
		t.Fatalf("first=%d", first)
	}
	again, _, _ := f.Layout(frows(followtest.Item("a", 10), followtest.Item("b", 20)), 4)
	if again == first || again != 18 {
		t.Fatalf("same anchor resolved to %d after rows above changed (was %d), want 18", again, first)
	}
}

func TestFollowMarkNeverMovesBack(t *testing.T) {
	f := NewFollow()
	rows := frows(followtest.Item("a", 30))
	f.Layout(rows, 5)
	f.Scroll(rows, 5, -10)
	rows = frows(followtest.Item("a", 40))
	f.Scroll(rows, 5, 15)
	if _, _, n := f.Layout(rows, 5); n != 5 {
		t.Fatalf("count=%d, want 5", n)
	}
	f.Scroll(rows, 5, -4)
	if _, _, n := f.Layout(rows, 5); n != 5 {
		t.Fatalf("count after scrolling up=%d, want 5", n)
	}
}

func TestFollowAnchorGoneWithNoNeighbourGoesToBottom(t *testing.T) {
	f := NewFollow()
	rows := frows(followtest.Item("a", 20))
	f.Layout(rows, 5)
	f.Home(rows, 5)
	first, bottom, n := f.Layout(frows(followtest.Item("z", 20)), 5)
	if !bottom || first != 15 || n != 0 {
		t.Fatalf("got %d %v %d", first, bottom, n)
	}
}

func TestFollowAnchorGonePrefersPrevious(t *testing.T) {
	f := NewFollow()
	rows := frows(followtest.Item("a", 10), followtest.Item("b", 10))
	f.Layout(rows, 4)
	f.Scroll(rows, 4, -7)
	f.Scroll(rows, 4, 0)
	first, _, _ := f.Layout(frows(followtest.Item("a", 10), followtest.Item("c", 0)), 4)
	if first != 6 {
		t.Fatalf("first=%d", first)
	}
}
