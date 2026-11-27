// Package followtest holds the cases of v6 addendum 1, section 4 that name no particular
// view. Every scrolling view of the TUI (the chat, the transcript and the reviews drawer)
// runs them through a small adaptor, so that all of them follow new content the same way.
package followtest

import (
	"fmt"
	"testing"
)

// Row is one drawn row: the item it belongs to and the characters [Char, End) of that item
// that it shows.
type Row struct {
	Item string
	Char int
	End  int
}

// Key is a movement the user makes.
type Key int

// The movements. Up and Down move one row; PageUp and PageDown move one page.
const (
	Up Key = iota
	Down
	PageUp
	PageDown
	Home
	End
)

func (k Key) String() string {
	return [...]string{"Up", "Down", "PageUp", "PageDown", "Home", "End"}[k]
}

// View is the adaptor a view provides. SetRows replaces the content with the given rows,
// SetHeight sets how many rows the view shows, and Press applies one movement. First,
// AtBottom and Count read the view's first shown row, whether it is at the bottom, and its
// count of new rows, as it would draw them now.
type View interface {
	SetRows([]Row)
	SetHeight(int)
	Press(Key)
	First() int
	AtBottom() bool
	Count() int
}

// Special values of Want.First.
const (
	AnyRow  = -1 // the first row is not checked
	SameRow = -2 // the first row is the one shown at the previous step
)

// Want is what the view shows after a step.
type Want struct {
	First  int
	Bottom bool
	Count  int
}

// Step sets the height (when non-zero), then the rows (when non-nil), then presses the keys
// in order, and then checks the view.
type Step struct {
	Height int
	Rows   []Row
	Press  []Key
	Want   Want
}

// Case is one acceptance case: a fresh view taken through its steps.
type Case struct {
	Name  string
	Steps []Step
}

// Wrap lays out an item of length characters in rows of width characters. An empty item
// has one empty row.
func Wrap(item string, length, width int) []Row {
	if length == 0 {
		return []Row{{item, 0, 0}}
	}
	var rows []Row
	for c := 0; c < length; c += width {
		rows = append(rows, Row{item, c, min(c+width, length)})
	}
	return rows
}

// Lines lays out an item of n rows of ten characters each.
func Lines(item string, n int) []Row { return Wrap(item, n*10, 10) }

// Join puts items one after another.
func Join(items ...[]Row) []Row {
	rows := []Row{}
	for _, it := range items {
		rows = append(rows, it...)
	}
	return rows
}

func keys(k Key, n int) []Key {
	ks := make([]Key, n)
	for i := range ks {
		ks[i] = k
	}
	return ks
}

func at(first int) Want          { return Want{First: first, Bottom: true} }
func away(first, count int) Want { return Want{First: first, Count: count} }

// Cases returns the table. Every case starts with a fresh view.
func Cases() []Case {
	progress := []Step{
		{Height: 4, Rows: Lines("a", 10), Press: keys(Up, 3), Want: away(3, 0)},
		{Rows: Join(Lines("a", 10), Lines("p", 1)), Want: away(3, 1)},
	}
	for range 10 {
		progress = append(progress, Step{Rows: Join(Lines("a", 10), Lines("p", 1)), Want: away(3, 1)})
	}
	progress = append(progress,
		Step{Rows: Join(Lines("a", 10), Lines("p", 2)), Want: away(3, 2)},
		Step{Rows: Join(Lines("a", 10), Lines("p", 1), Lines("b", 3)), Want: away(3, 4)},
	)

	return []Case{
		{"a view starts at the bottom", []Step{
			{Height: 4, Rows: Lines("a", 20), Want: at(16)},
		}},
		{"at the bottom an answer taller than the view arrives", []Step{
			{Height: 4, Rows: Join(Lines("in", 1), Lines("ans", 2)), Want: at(0)},
			{Rows: Join(Lines("in", 1), Lines("ans", 12)), Want: at(9)},
		}},
		{"at the bottom a progress line, a block and an input arrive", []Step{
			{Height: 4, Rows: Lines("a", 5), Want: at(1)},
			{Rows: Join(Lines("a", 5), Lines("p", 1)), Want: at(2)},
			{Rows: Join(Lines("a", 5), Lines("p", 1), Lines("b", 3)), Want: at(5)},
			{Rows: Join(Lines("a", 5), Lines("p", 1), Lines("b", 3), Lines("in", 1)), Want: at(6)},
		}},
		{"after PageUp rows arrive and the first row does not change", []Step{
			{Height: 4, Rows: Lines("a", 10), Press: []Key{PageUp}, Want: Want{First: AnyRow}},
			{Rows: Join(Lines("a", 10), Lines("b", 3)), Want: Want{First: SameRow, Count: 3}},
			{Rows: Join(Lines("a", 10), Lines("b", 3), Lines("c", 2)), Want: Want{First: SameRow, Count: 5}},
		}},
		{"scrolling down until the last row is shown clears the count and follows", []Step{
			{Height: 4, Rows: Lines("a", 10), Press: keys(Up, 3), Want: away(3, 0)},
			{Rows: Join(Lines("a", 10), Lines("b", 2)), Want: away(3, 2)},
			{Press: keys(Down, 4), Want: away(7, 1)},
			{Press: []Key{Down}, Want: at(8)},
			{Rows: Join(Lines("a", 10), Lines("b", 2), Lines("c", 1)), Want: at(9)},
		}},
		{"End shows the last row and Home on tall content stops following", []Step{
			{Height: 4, Rows: Lines("a", 10), Press: keys(Up, 5), Want: away(1, 0)},
			{Press: []Key{End}, Want: at(6)},
			{Press: []Key{Home}, Want: away(0, 0)},
			{Rows: Join(Lines("a", 10), Lines("b", 2)), Want: away(0, 2)},
			{Press: []Key{End}, Want: at(8)},
			{Rows: Join(Lines("a", 10), Lines("b", 2), Lines("c", 1)), Want: at(9)},
		}},
		{"on content shorter than the view Home, PageUp and Up stay at the bottom", []Step{
			{Height: 4, Rows: Lines("a", 3), Press: []Key{Home}, Want: at(0)},
			{Press: []Key{PageUp}, Want: at(0)},
			{Press: []Key{Up}, Want: at(0)},
			{Rows: Join(Lines("a", 3), Lines("b", 1)), Want: at(0)},
			{Rows: Join(Lines("a", 3), Lines("b", 1), Lines("c", 1)), Want: at(1)},
		}},
		{"a view scrolled to its last row by hand follows", []Step{
			{Height: 4, Rows: Lines("a", 10), Press: keys(Up, 2), Want: away(4, 0)},
			{Press: keys(Down, 2), Want: at(6)},
			{Rows: Join(Lines("a", 10), Lines("b", 3)), Want: at(9)},
		}},
		{"a narrower width wraps the rows above: the first row contains the anchor", []Step{
			{Height: 4, Rows: Join(Wrap("a", 100, 20), Wrap("b", 100, 10)), Press: keys(Up, 4), Want: away(7, 0)},
			{Rows: Join(Wrap("a", 100, 10), Wrap("b", 100, 5)), Want: away(14, 0)},
			// Wider, then narrower again: the anchor (b at 20) has not moved.
			{Rows: Join(Wrap("a", 100, 20), Wrap("b", 100, 15)), Want: away(6, 0)},
			{Rows: Join(Wrap("a", 100, 10), Wrap("b", 100, 5)), Want: away(14, 0)},
		}},
		{"successive re-wraps keep the anchor when it lands in its item's last row", []Step{
			{Height: 3, Rows: Join(Wrap("a", 100, 10), Lines("b", 3)), Press: keys(Up, 5), Want: away(5, 0)},
			{Rows: Join([]Row{{"a", 0, 40}, {"a", 40, 100}}, Lines("b", 3)), Want: away(1, 0)},
			{Rows: Join(Wrap("a", 100, 25), Lines("b", 3)), Want: away(2, 0)},
		}},
		{"an item above grows: the first row shows the same content", []Step{
			{Height: 4, Rows: Join(Lines("a", 2), Lines("b", 10), Lines("c", 1)), Press: keys(Up, 5), Want: away(4, 0)},
			{Rows: Join(Lines("a", 5), Lines("b", 10), Lines("c", 1)), Want: away(7, 0)},
		}},
		{"the anchor's item is removed: the first row is the start of the next item", []Step{
			{Height: 4, Rows: Join(Lines("a", 3), Lines("b", 3), Lines("c", 3), Lines("d", 3)), Press: keys(Up, 4), Want: away(4, 0)},
			{Rows: Join(Lines("a", 3), Lines("c", 3), Lines("d", 3)), Want: away(3, 0)},
		}},
		{"the anchor's item is removed with no next item: the start of the previous one", []Step{
			{Height: 4, Rows: Join(Lines("a", 6), Lines("b", 6)), Press: []Key{Up}, Want: away(7, 0)},
			{Rows: Lines("a", 6), Want: away(0, 0)},
		}},
		{"the anchor's item is removed with no other item: the view goes to the bottom", []Step{
			{Height: 4, Rows: Lines("a", 6), Press: []Key{Up}, Want: away(1, 0)},
			{Rows: Lines("b", 6), Want: at(2)},
		}},
		{"the anchor's item becomes shorter than the anchor: the first row is its last row", []Step{
			{Height: 4, Rows: Join(Lines("a", 3), Lines("b", 10), Lines("c", 6)), Press: keys(Up, 8), Want: away(7, 0)},
			{Rows: Join(Lines("a", 3), Lines("b", 3), Lines("c", 6)), Want: away(5, 0)},
			// The anchor moved to the start of that row, so regrowing does not bring back the old place.
			{Rows: Join(Lines("a", 3), Lines("b", 10), Lines("c", 6)), Want: away(5, 0)},
		}},
		{"the seen mark's item becomes shorter: the count is the rows after its end", []Step{
			{Height: 4, Rows: Lines("a", 10), Press: keys(Up, 4), Want: away(2, 0)},
			{Rows: Join(Lines("a", 10), Lines("b", 10)), Want: away(2, 10)},
			{Rows: Join(Lines("a", 5), Lines("b", 10)), Want: away(2, 10)},
			// The mark moved to the item's end, so the regrown rows are new.
			{Rows: Join(Lines("a", 10), Lines("b", 10)), Want: away(2, 15)},
		}},
		{"the seen mark's item is removed: the count is the rows after the earlier item", []Step{
			{Height: 4, Rows: Join(Lines("a", 6), Lines("b", 3)), Press: keys(Up, 4), Want: away(1, 0)},
			{Rows: Join(Lines("a", 6), Lines("b", 3), Lines("c", 5)), Want: away(1, 5)},
			{Rows: Join(Lines("a", 6), Lines("c", 5)), Want: away(1, 5)},
		}},
		{"the seen mark's item is removed with no earlier item: every row counts", []Step{
			{Height: 4, Rows: Lines("a", 8), Press: keys(Up, 4), Want: away(0, 0)},
			{Rows: Join(Lines("a", 8), Lines("b", 6)), Want: away(0, 6)},
			{Rows: Lines("b", 6), Want: away(0, 6)},
			{Rows: Join(Lines("b", 6), Lines("c", 1)), Want: away(0, 7)},
		}},
		{"the anchor's item is removed and the last row is shown: at the bottom", []Step{
			{Height: 4, Rows: Join(Lines("a", 3), Lines("b", 4), Lines("c", 1)), Press: []Key{Up}, Want: away(3, 0)},
			{Rows: Join(Lines("a", 3), Lines("c", 1)), Want: at(0)},
			{Rows: Join(Lines("a", 3), Lines("c", 1), Lines("d", 1)), Want: at(1)},
		}},
		{"the anchor's item becomes shorter and the last row is shown: at the bottom", []Step{
			{Height: 4, Rows: Join(Lines("a", 2), Lines("b", 8), Lines("c", 1)), Press: keys(Up, 3), Want: away(4, 0)},
			{Rows: Join(Lines("a", 2), Lines("b", 2), Lines("c", 1)), Want: at(1)},
			{Rows: Join(Lines("a", 2), Lines("b", 2), Lines("c", 2)), Want: at(2)},
		}},
		{"the seen mark's item becomes shorter and the last row is shown: at the bottom", []Step{
			{Height: 4, Rows: Lines("a", 10), Press: keys(Up, 2), Want: away(4, 0)},
			{Rows: Join(Lines("a", 10), Lines("b", 2)), Want: away(4, 2)},
			{Rows: Join(Lines("a", 3), Lines("b", 2)), Want: at(1)},
			{Rows: Join(Lines("a", 3), Lines("b", 3)), Want: at(2)},
		}},
		{"the seen mark's item is removed and the last row is shown: at the bottom", []Step{
			{Height: 4, Rows: Join(Lines("a", 3), Lines("b", 3)), Press: keys(Up, 2), Want: away(0, 0)},
			{Rows: Join(Lines("a", 3), Lines("b", 3), Lines("c", 1)), Want: away(0, 1)},
			{Rows: Join(Lines("a", 3), Lines("c", 1)), Want: at(0)},
			{Rows: Join(Lines("a", 3), Lines("c", 1), Lines("d", 1)), Want: at(1)},
		}},
		{"a progress line below replaced ten times counts its rows once", progress},
		{"a re-wrap that changes the rows below the mark recounts them", []Step{
			{Height: 4, Rows: Wrap("a", 100, 10), Press: keys(Up, 3), Want: away(3, 0)},
			{Rows: Join(Wrap("a", 100, 10), Wrap("b", 100, 10)), Want: away(3, 10)},
			{Rows: Join(Wrap("a", 100, 5), Wrap("b", 100, 5)), Want: away(6, 20)},
			{Rows: Join(Wrap("a", 100, 20), Wrap("b", 100, 20)), Want: away(1, 5)},
		}},
		{"a narrower re-wrap of the mark's item counts nothing after it", []Step{
			{Height: 4, Rows: Join(Lines("a", 3), Lines("b", 10)), Press: keys(Up, 4), Want: away(5, 0)},
			{Rows: Join(Wrap("a", 30, 5), Wrap("b", 100, 5)), Want: away(10, 0)},
		}},
		{"a re-wrap of one unchanged item counts nothing", []Step{
			{Height: 5, Rows: Wrap("a", 200, 10), Press: keys(Up, 5), Want: away(10, 0)},
			{Rows: Wrap("a", 200, 5), Want: away(20, 0)},
			{Rows: Wrap("a", 200, 15), Want: away(6, 0)},
			{Rows: Wrap("a", 200, 10), Want: away(10, 0)},
		}},
		{"moving that shows nothing past the mark does not move it", []Step{
			{Height: 4, Rows: Wrap("a", 200, 10), Press: []Key{Home}, Want: away(0, 0)},
			{Rows: Wrap("a", 400, 10), Want: away(0, 20)},
			// Wider: the mark (a at 200) falls inside the row [195, 210).
			{Rows: Wrap("a", 400, 15), Want: away(0, 13)},
			{Press: []Key{Up}, Want: away(0, 13)},
			{Press: []Key{Down}, Want: away(1, 13)},
			{Press: []Key{Up}, Want: away(0, 13)},
			{Rows: Wrap("a", 400, 10), Want: away(0, 20)},
		}},
		{"a re-wrap that makes all of the content fit puts the view at the bottom", []Step{
			{Height: 4, Rows: Lines("a", 10), Press: keys(Up, 3), Want: away(3, 0)},
			{Rows: Join(Lines("a", 10), Lines("b", 2)), Want: away(3, 2)},
			{Rows: Join(Wrap("a", 100, 50), Wrap("b", 20, 20)), Want: at(0)},
			{Rows: Join(Wrap("a", 100, 50), Wrap("b", 20, 20), Lines("c", 2)), Want: at(1)},
		}},
		{"resizing at the bottom keeps the last row shown", []Step{
			{Height: 4, Rows: Wrap("a", 100, 10), Want: at(6)},
			{Height: 6, Want: at(4)},
			{Rows: Wrap("a", 100, 5), Want: at(14)},
		}},
	}
}

// Run runs every case against a fresh view from newView.
func Run(t *testing.T, newView func() View) {
	t.Helper()
	for _, c := range Cases() {
		t.Run(c.Name, func(t *testing.T) {
			v := newView()
			prev := 0
			for i, s := range c.Steps {
				if s.Height != 0 {
					v.SetHeight(s.Height)
				}
				if s.Rows != nil {
					v.SetRows(s.Rows)
				}
				for _, k := range s.Press {
					v.Press(k)
				}
				first, bottom, count := v.First(), v.AtBottom(), v.Count()
				want := s.Want.First
				if want == SameRow {
					want = prev
				}
				if (want != AnyRow && first != want) || bottom != s.Want.Bottom || count != s.Want.Count {
					t.Fatalf("step %d: got first %d, bottom %t, count %d; want %s, bottom %t, count %d",
						i, first, bottom, count, describe(s.Want.First, want), s.Want.Bottom, s.Want.Count)
				}
				prev = first
			}
		})
	}
}

func describe(asked, want int) string {
	if asked == AnyRow {
		return "first any"
	}
	return fmt.Sprintf("first %d", want)
}
