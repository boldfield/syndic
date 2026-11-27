// Package followtest holds the table of following cases that every scrolling
// view (chat, transcript, reviews drawer) is held to. A view is driven through
// a small Adaptor; the package knows nothing of any view.
package followtest

import "testing"

// Row is one drawn row: the item it belongs to and the character offsets at
// which it begins and ends (End exclusive).
type Row struct {
	Item string
	Char int
	End  int
}

// Adaptor drives one view.
type Adaptor interface {
	SetRows(rows []Row)
	SetHeight(h int)
	// Press sends one of: "up", "down", "pgup", "pgdown", "home", "end".
	Press(key string)
	First() int
	AtBottom() bool
	Count() int
}

// Item returns n rows of one item, beginning at characters 0, 10, 20, ….
func Item(id string, n int) []Row {
	rows := make([]Row, n)
	for i := range rows {
		rows[i] = Row{Item: id, Char: i * 10, End: i*10 + 10}
	}
	return rows
}

// Wrapped returns n rows of one item, beginning at characters 0, step, 2*step, ….
func Wrapped(id string, n, step int) []Row {
	rows := make([]Row, n)
	for i := range rows {
		rows[i] = Row{Item: id, Char: i * step, End: i*step + step}
	}
	return rows
}

// Join concatenates row slices.
func Join(parts ...[]Row) []Row {
	var out []Row
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// Case is one named scenario run against a fresh Adaptor.
type Case struct {
	Name string
	Run  func(t *testing.T, a Adaptor)
}

func want(t *testing.T, a Adaptor, first int, bottom bool, count int) {
	t.Helper()
	if a.First() != first || a.AtBottom() != bottom || a.Count() != count {
		t.Fatalf("first=%d bottom=%v count=%d, want first=%d bottom=%v count=%d",
			a.First(), a.AtBottom(), a.Count(), first, bottom, count)
	}
}

// Cases is every case of v6 addendum 1 section 4 that names no specific view.
// Each starts from a fresh adaptor.
var Cases = []Case{
	{"re-wrapping unchanged content narrower adds no new rows", func(t *testing.T, a Adaptor) {
		a.SetHeight(4)
		a.SetRows(Join(Item("a", 3), Item("b", 10)))
		for i := 0; i < 4; i++ {
			a.Press("up")
		}
		want(t, a, 5, false, 0)
		a.SetRows(Join(Wrapped("a", 6, 5), Wrapped("b", 20, 5)))
		want(t, a, 10, false, 0)
	}},
	{"tall answer arrives at the bottom", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		a.SetRows(Item("q", 1))
		a.SetRows(Join(Item("q", 1), Item("a", 20)))
		want(t, a, 16, true, 0)
	}},
	{"progress, block and input arrive at the bottom", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		rows := Item("q", 10)
		a.SetRows(rows)
		for _, id := range []string{"progress", "block", "input"} {
			rows = Join(rows, Item(id, 2))
			a.SetRows(rows)
			want(t, a, len(rows)-5, true, 0)
		}
	}},
	{"rows arriving after PgUp keep the first row and count", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		rows := Join(Item("a", 10), Item("b", 10))
		a.SetRows(rows)
		a.Press("pgup")
		first := a.First()
		want(t, a, first, false, 0)
		a.SetRows(Join(rows, Item("c", 3)))
		want(t, a, first, false, 3)
	}},
	{"scrolling back down clears the count and follows", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		rows := Join(Item("a", 20))
		a.SetRows(rows)
		a.Press("pgup")
		a.SetRows(Join(rows, Item("b", 3)))
		a.Press("end")
		a.Press("pgup")
		a.Press("pgdown")
		a.Press("pgdown")
		want(t, a, 18, true, 0)
		a.SetRows(Join(rows, Item("b", 3), Item("c", 1)))
		want(t, a, 19, true, 0)
	}},
	{"End from any position follows", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		a.SetRows(Item("a", 30))
		a.Press("home")
		want(t, a, 0, false, 0)
		a.Press("end")
		want(t, a, 25, true, 0)
	}},
	{"Home on tall content stops following", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		rows := Item("a", 30)
		a.SetRows(rows)
		a.Press("home")
		want(t, a, 0, false, 0)
		a.SetRows(Join(rows, Item("b", 1)))
		want(t, a, 0, false, 1)
	}},
	{"short content stays at the bottom whatever was pressed", func(t *testing.T, a Adaptor) {
		a.SetHeight(10)
		rows := Item("a", 4)
		a.SetRows(rows)
		for _, k := range []string{"home", "pgup", "up"} {
			a.Press(k)
			want(t, a, 0, true, 0)
		}
		a.SetRows(Join(rows, Item("b", 8)))
		want(t, a, 2, true, 0)
	}},
	{"scrolled to the last row by hand follows", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		rows := Item("a", 20)
		a.SetRows(rows)
		a.Press("home")
		for i := 0; i < 15; i++ {
			a.Press("down")
		}
		want(t, a, 15, true, 0)
		a.SetRows(Join(rows, Item("b", 2)))
		want(t, a, 17, true, 0)
	}},
	{"narrower terminal keeps the row containing the anchor", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		wide := Join(Item("a", 5), Item("b", 3), Item("c", 12))
		a.SetRows(wide)
		a.Press("home")
		for i := 0; i < 6; i++ {
			a.Press("down")
		}
		want(t, a, 6, false, 0)
		// "b" at char 10 is first; above it, a wraps into twice the rows.
		narrow := Join(Item("a", 10), Item("b", 3), Item("c", 12))
		a.SetRows(narrow)
		if got := narrow[a.First()]; got.Item != "b" || got.Char != 10 {
			t.Fatalf("first row is %v, want b@10", got)
		}
		if a.AtBottom() {
			t.Fatal("view left the content")
		}
	}},
	{"an item above growing keeps the first row", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		a.SetRows(Join(Item("a", 2), Item("b", 20)))
		a.Press("home")
		a.Press("down")
		a.Press("down")
		a.Press("down")
		a.SetRows(Join(Item("a", 2), Item("b", 20)))
		before := a.First()
		a.SetRows(Join(Item("a", 9), Item("b", 20)))
		if got := a.First(); got != before+7 {
			t.Fatalf("first=%d, want %d", got, before+7)
		}
	}},
	{"anchor item removed shows the next item", func(t *testing.T, a Adaptor) {
		a.SetHeight(4)
		a.SetRows(Join(Item("a", 5), Item("b", 5), Item("c", 10)))
		a.Press("home")
		for i := 0; i < 6; i++ {
			a.Press("down")
		}
		a.SetRows(Join(Item("a", 5), Item("c", 10)))
		want(t, a, 5, false, 0)
	}},
	{"anchor item shortened shows its last row", func(t *testing.T, a Adaptor) {
		a.SetHeight(4)
		a.SetRows(Join(Item("a", 2), Item("b", 10), Item("c", 10)))
		a.Press("home")
		for i := 0; i < 7; i++ {
			a.Press("down")
		}
		a.SetRows(Join(Item("a", 2), Item("b", 3), Item("c", 10)))
		want(t, a, 4, false, 0)
	}},
	{"seen mark item shortened or removed", func(t *testing.T, a Adaptor) {
		a.SetHeight(4)
		a.SetRows(Join(Item("a", 3), Item("b", 10)))
		a.Press("pgup") // leaves the bottom with the mark at the end of b
		a.SetRows(Join(Item("a", 3), Item("b", 10), Item("c", 10)))
		want(t, a, 5, false, 10)
		a.SetRows(Join(Item("a", 3), Item("b", 4), Item("c", 10)))
		if a.Count() != 10 {
			t.Fatalf("shortened mark item: count=%d, want 10", a.Count())
		}
		a.SetRows(Join(Item("a", 3), Item("c", 10)))
		if a.Count() != 10 || a.AtBottom() {
			t.Fatalf("removed mark item: count=%d bottom=%v, want 10", a.Count(), a.AtBottom())
		}
	}},
	{"seen mark item removed with no earlier item counts every row", func(t *testing.T, a Adaptor) {
		a.SetHeight(4)
		a.SetRows(Join(Item("a", 3), Item("b", 10)))
		a.Press("pgup")
		a.SetRows(Join(Item("a", 3), Item("b", 10), Item("c", 10)))
		a.SetRows(Item("c", 10))
		want(t, a, 0, false, 10)
	}},
	{"a removal that shows the last row is at the bottom", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		a.SetRows(Join(Item("a", 10), Item("b", 10)))
		a.Press("home")
		a.SetRows(Join(Item("a", 3), Item("b", 2)))
		want(t, a, 0, true, 0)
	}},
	{"a progress row replaced ten times counts once", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		base := Item("a", 20)
		a.SetRows(base)
		a.Press("pgup")
		a.SetRows(Join(base, Item("progress", 2)))
		want(t, a, 10, false, 2)
		for i := 0; i < 10; i++ {
			a.SetRows(Join(base, Item("progress", 2)))
		}
		want(t, a, 10, false, 2)
	}},
	{"resize changing the wrapping below the mark recounts", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		a.SetRows(Join(Item("a", 10), Item("b", 5)))
		a.Press("home")
		want(t, a, 0, false, 0)
		a.SetRows(Join(Item("a", 10), Item("b", 9)))
		want(t, a, 0, false, 4)
	}},
	{"successive re-wraps keep the anchor when it lands in the last row", func(t *testing.T, a Adaptor) {
		a.SetHeight(4)
		a.SetRows(Join(Wrapped("a", 20, 5), Item("b", 10)))
		for i := 0; i < 7; i++ {
			a.Press("up")
		}
		want(t, a, 19, false, 0)
		a.SetRows(Join(Wrapped("a", 10, 10), Item("b", 10)))
		want(t, a, 9, false, 0)
		a.SetRows(Join(Wrapped("a", 20, 5), Item("b", 10)))
		want(t, a, 19, false, 0)
	}},
	{"resize that fits all content is at the bottom", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		a.SetRows(Item("a", 20))
		a.Press("home")
		a.SetRows(Item("a", 4))
		want(t, a, 0, true, 0)
		a.SetRows(Item("a", 20))
		a.SetHeight(30)
		want(t, a, 0, true, 0)
	}},
	{"starts at the bottom and resize keeps the last row", func(t *testing.T, a Adaptor) {
		a.SetHeight(5)
		a.SetRows(Item("a", 20))
		want(t, a, 15, true, 0)
		a.SetHeight(8)
		want(t, a, 12, true, 0)
		a.SetRows(Item("a", 30))
		a.SetHeight(3)
		want(t, a, 27, true, 0)
	}},
}

// Run runs every case against a fresh adaptor from newAdaptor.
func Run(t *testing.T, newAdaptor func() Adaptor) {
	t.Helper()
	for _, c := range Cases {
		t.Run(c.Name, func(t *testing.T) { c.Run(t, newAdaptor()) })
	}
}
