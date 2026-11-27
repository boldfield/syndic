package tui

import (
	"testing"

	"github.com/boldfield/syndic/internal/tui/followtest"
)

// followView adapts the bare model to the shared case table.
type followView struct {
	f      *Follow
	rows   []FollowRow
	height int
}

func (v *followView) layout() (int, bool, int) { return v.f.Layout(v.rows, v.height) }

func (v *followView) SetRows(rows []followtest.Row) {
	v.rows = v.rows[:0:0]
	for _, r := range rows {
		v.rows = append(v.rows, FollowRow{r.Item, r.Char, r.End})
	}
	v.layout()
}

func (v *followView) SetHeight(h int) { v.height = h; v.layout() }

func (v *followView) Press(k followtest.Key) {
	switch k {
	case followtest.Up:
		v.f.Scroll(-1)
	case followtest.Down:
		v.f.Scroll(1)
	case followtest.PageUp:
		v.f.Scroll(-v.height)
	case followtest.PageDown:
		v.f.Scroll(v.height)
	case followtest.Home:
		v.f.Home()
	case followtest.End:
		v.f.End()
	}
}

func (v *followView) First() int     { first, _, _ := v.layout(); return first }
func (v *followView) AtBottom() bool { _, bottom, _ := v.layout(); return bottom }
func (v *followView) Count() int     { _, _, count := v.layout(); return count }

func TestFollowCases(t *testing.T) {
	followtest.Run(t, func() followtest.View { return &followView{f: NewFollow()} })
}

func followRows(items ...[]followtest.Row) []FollowRow {
	var rows []FollowRow
	for _, r := range followtest.Join(items...) {
		rows = append(rows, FollowRow{r.Item, r.Char, r.End})
	}
	return rows
}

type followWant struct {
	first  int
	bottom bool
	count  int
}

func checkLayout(t *testing.T, f *Follow, rows []FollowRow, height int, want followWant) {
	t.Helper()
	first, bottom, count := f.Layout(rows, height)
	if got := (followWant{first, bottom, count}); got != want {
		t.Fatalf("Layout = %+v, want %+v", got, want)
	}
	if f.AtBottom() != want.bottom {
		t.Fatalf("AtBottom = %t, want %t", f.AtBottom(), want.bottom)
	}
}

func TestFollowStartsAtBottom(t *testing.T) {
	var zero Follow
	checkLayout(t, &zero, followRows(followtest.Lines("a", 9)), 4, followWant{5, true, 0})
	checkLayout(t, NewFollow(), followRows(followtest.Lines("a", 9)), 4, followWant{5, true, 0})
	checkLayout(t, NewFollow(), nil, 4, followWant{0, true, 0})
}

func TestFollowMovementBeforeLayoutStaysAtBottom(t *testing.T) {
	f := NewFollow()
	f.Scroll(-3)
	f.Home()
	checkLayout(t, f, followRows(followtest.Lines("a", 9)), 4, followWant{5, true, 0})
}

// The model keeps no row number: the same anchor resolves to a different row when the rows
// above it change.
func TestFollowAnchorIsContentNotRow(t *testing.T) {
	f := NewFollow()
	f.Layout(followRows(followtest.Lines("a", 2), followtest.Lines("b", 10)), 4)
	f.Scroll(-4)
	checkLayout(t, f, followRows(followtest.Lines("a", 2), followtest.Lines("b", 10)), 4, followWant{4, false, 0})
	if f.anchor != (followPos{"b", 20}) {
		t.Fatalf("anchor = %+v, want b at 20", f.anchor)
	}
	checkLayout(t, f, followRows(followtest.Lines("a", 7), followtest.Lines("b", 10)), 4, followWant{9, false, 0})
	checkLayout(t, f, followRows(followtest.Lines("b", 10)), 4, followWant{2, false, 0})
	checkLayout(t, f, followRows(followtest.Lines("z", 1), followtest.Lines("b", 10)), 4, followWant{3, false, 0})
}

func TestFollowPageAndHome(t *testing.T) {
	rows := followRows(followtest.Lines("a", 10))
	f := NewFollow()
	f.Layout(rows, 4)
	f.Scroll(-4)
	checkLayout(t, f, rows, 4, followWant{2, false, 0})
	f.Scroll(-4)
	checkLayout(t, f, rows, 4, followWant{0, false, 0})
	f.Scroll(3)
	checkLayout(t, f, rows, 4, followWant{3, false, 0})
	f.Scroll(100)
	checkLayout(t, f, rows, 4, followWant{6, true, 0})
	f.Home()
	checkLayout(t, f, rows, 4, followWant{0, false, 0})
	f.End()
	checkLayout(t, f, rows, 4, followWant{6, true, 0})
}

func TestFollowLeavingRecordsMarkAtContentEnd(t *testing.T) {
	f := NewFollow()
	f.Layout(followRows(followtest.Lines("a", 3), followtest.Lines("b", 5)), 4)
	f.Scroll(-2)
	if f.seenNone || f.seen != (followPos{"b", 50}) {
		t.Fatalf("seen = %+v (none %t), want b at 50", f.seen, f.seenNone)
	}
	f.End()
	if f.seen != (followPos{}) || f.seenNone {
		t.Fatalf("End left the mark %+v (none %t)", f.seen, f.seenNone)
	}
}

func TestFollowScrollDownMovesMarkForwardOnly(t *testing.T) {
	f := NewFollow()
	f.Layout(followRows(followtest.Lines("a", 10)), 4)
	f.Scroll(-6)
	rows := followRows(followtest.Lines("a", 10), followtest.Lines("b", 6))
	checkLayout(t, f, rows, 4, followWant{0, false, 6})
	// Showing up to a's end leaves the mark where it is.
	f.Scroll(6)
	checkLayout(t, f, rows, 4, followWant{6, false, 6})
	// Showing two rows of b moves the mark to b at 20.
	f.Scroll(2)
	checkLayout(t, f, rows, 4, followWant{8, false, 4})
	if f.seen != (followPos{"b", 20}) {
		t.Fatalf("seen = %+v, want b at 20", f.seen)
	}
	f.Scroll(-5)
	checkLayout(t, f, rows, 4, followWant{3, false, 4})
	if f.seen != (followPos{"b", 20}) {
		t.Fatalf("scrolling up moved the mark to %+v", f.seen)
	}
}

func TestFollowAnchorShrinkMovesItAndRemovalUsesPreviousOrder(t *testing.T) {
	f := NewFollow()
	f.Layout(followRows(followtest.Lines("a", 3), followtest.Lines("b", 6), followtest.Lines("c", 3), followtest.Lines("d", 6)), 4)
	f.Scroll(-10) // first row 4, b at 10
	checkLayout(t, f, followRows(followtest.Lines("a", 3), followtest.Lines("b", 6), followtest.Lines("c", 3), followtest.Lines("d", 6)), 4, followWant{4, false, 0})
	// b and c both go: the next remaining item in the previous order is d.
	checkLayout(t, f, followRows(followtest.Lines("a", 3), followtest.Lines("d", 6)), 4, followWant{3, false, 0})
	if f.anchor != (followPos{"d", 0}) {
		t.Fatalf("anchor = %+v, want d at 0", f.anchor)
	}
}

func TestFollowAnchorKeptWhenItemEndsAfterIt(t *testing.T) {
	f := NewFollow()
	rows := followRows(followtest.Wrap("a", 100, 10), followtest.Lines("b", 3))
	f.Layout(rows, 3)
	f.Scroll(-5)
	// a at 50 falls in a's last row [40, 100) but a has not shrunk past it.
	checkLayout(t, f, followRows(followtest.Wrap("a", 100, 40)[:1], []followtest.Row{{Item: "a", Char: 40, End: 100}}, followtest.Lines("b", 3)), 3, followWant{1, false, 0})
	if f.anchor != (followPos{"a", 50}) {
		t.Fatalf("anchor = %+v, want a at 50", f.anchor)
	}
}

func TestFollowMarkBeforeFirstRowCountsEverything(t *testing.T) {
	f := NewFollow()
	f.Layout(followRows(followtest.Lines("a", 6)), 4)
	f.Scroll(-2)
	checkLayout(t, f, followRows(followtest.Lines("a", 6), followtest.Lines("b", 3), followtest.Lines("c", 3)), 4, followWant{0, false, 6})
	// a goes: the anchor moves to b, and with no item before a the mark goes before the first row.
	checkLayout(t, f, followRows(followtest.Lines("b", 3), followtest.Lines("c", 3)), 4, followWant{0, false, 6})
	if !f.seenNone {
		t.Fatalf("seen = %+v, want before the first row", f.seen)
	}
	// Scrolling down past nothing seen moves the mark onto the last row shown.
	f.Scroll(1)
	checkLayout(t, f, followRows(followtest.Lines("b", 3), followtest.Lines("c", 3)), 4, followWant{1, false, 1})
}
