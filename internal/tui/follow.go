package tui

import "slices"

// FollowRow is one row of a scrolling view as drawn at the current width: the item it
// belongs to and the characters [Char, End) of that item that it shows. Item ids are
// opaque, unique within a view and stable across renders; an item's rows are adjacent.
type FollowRow struct {
	Item string
	Char int
	End  int
}

// followPos is a place in the content: an item and a character of it.
type followPos struct {
	item string
	char int
}

// Follow decides, for one scrolling view, which row is shown first, whether the view is
// at the bottom, and how many rows are new (v6 addendum 1, section 3). It holds its place
// by content, never by a row number: away from the bottom it keeps an anchor (the item and
// character at which the first shown row begins) and a seen mark (the item and character
// at which the last shown row ended). The zero value is at the bottom.
type Follow struct {
	away     bool
	anchor   followPos
	seen     followPos
	seenNone bool // the mark is before the first row, so every row counts as new

	// The rows and height of the last layout. Movement is applied to them, and the order
	// of their items finds the neighbours of an item that has gone.
	rows   []FollowRow
	height int
}

// NewFollow returns a model at the bottom.
func NewFollow() *Follow { return &Follow{} }

// AtBottom reports whether the view was at the bottom at its last layout or movement.
func (f *Follow) AtBottom() bool { return !f.away }

// Layout places the view on rows with the given height. It returns the first row to show,
// whether the view is at the bottom, and the number of rows after the seen mark. The anchor
// is resolved to a row, the position is clamped to the content, and then the test for being
// at the bottom is applied, in that order.
func (f *Follow) Layout(rows []FollowRow, height int) (first int, bottom bool, count int) {
	height = max(height, 1)
	defer func() { f.rows, f.height = slices.Clone(rows), height }()

	last := max(len(rows)-height, 0)
	if !f.away {
		return last, true, 0
	}
	spans := followSpans(rows)
	first, ok := f.resolveAnchor(rows, spans)
	if !ok || first >= last {
		f.toBottom()
		return last, true, 0
	}
	seen := f.resolveSeen(rows, spans)
	return first, false, len(rows) - 1 - seen
}

// Scroll moves the view n rows down, or up when n is negative.
func (f *Follow) Scroll(n int) { f.move(func(first int) int { return first + n }) }

// Home moves the view to the first row.
func (f *Follow) Home() { f.move(func(int) int { return 0 }) }

// End puts the view at the bottom.
func (f *Follow) End() { f.toBottom() }

// move takes the view from its current first row to target(first), clamped to the content.
// It leaves the bottom only if the last row is then not shown.
func (f *Follow) move(target func(first int) int) {
	rows, height := f.rows, f.height
	first, _, _ := f.Layout(rows, height)
	last := max(len(rows)-height, 0)
	to := min(max(target(first), 0), last)
	if to >= last {
		f.toBottom()
		return
	}
	switch {
	case !f.away:
		// Leaving the bottom: the last shown row was the content's last row.
		f.away = true
		end := rows[len(rows)-1]
		f.seen, f.seenNone = followPos{end.Item, end.End}, false
	case to > first:
		// Scrolling down moves the mark forward to the end of the last row shown, never back.
		seen := f.resolveSeen(rows, followSpans(rows))
		shown := to + height - 1
		r := rows[shown]
		if shown > seen || (shown == seen && !f.seenNone && r.Item == f.seen.item && r.End > f.seen.char) {
			f.seen, f.seenNone = followPos{r.Item, r.End}, false
		}
	}
	f.anchor = followPos{rows[to].Item, rows[to].Char}
}

func (f *Follow) toBottom() {
	f.away = false
	f.anchor, f.seen, f.seenNone = followPos{}, followPos{}, false
}

// resolveAnchor returns the row that now contains the anchor. If the anchor's item is
// shorter than the anchor's character, the anchor moves to the start of the item's last
// row; if the item is gone, to the start of the next item that remains, else the previous
// one. It reports false when no such item remains.
func (f *Follow) resolveAnchor(rows []FollowRow, spans map[string][2]int) (int, bool) {
	if sp, ok := spans[f.anchor.item]; ok {
		if rows[sp[1]].End <= f.anchor.char {
			f.anchor.char = rows[sp[1]].Char
			return sp[1], true
		}
		r := sp[0]
		for i := sp[0]; i <= sp[1] && rows[i].Char <= f.anchor.char; i++ {
			r = i
		}
		return r, true
	}
	for _, after := range []bool{true, false} {
		if id, ok := f.neighbour(f.anchor.item, spans, after); ok {
			r := spans[id][0]
			f.anchor = followPos{id, rows[r].Char}
			return r, true
		}
	}
	return 0, false
}

// resolveSeen returns the index of the last row at or before the seen mark, or -1 when the
// mark is before the first row. If the mark's item now ends before the mark, the mark moves
// to the item's end; if the item is gone, to the end of the nearest earlier item that
// remains, else to before the first row.
func (f *Follow) resolveSeen(rows []FollowRow, spans map[string][2]int) int {
	if f.seenNone {
		return -1
	}
	sp, ok := spans[f.seen.item]
	if !ok {
		id, ok := f.neighbour(f.seen.item, spans, false)
		if !ok {
			f.seen, f.seenNone = followPos{}, true
			return -1
		}
		sp = spans[id]
		f.seen = followPos{id, rows[sp[1]].End}
	}
	if end := rows[sp[1]].End; end < f.seen.char {
		f.seen.char = end
	}
	r := sp[0] - 1
	for i := sp[0]; i <= sp[1]; i++ {
		if rows[i].Char < f.seen.char || rows[i].End <= f.seen.char {
			r = i
		}
	}
	return r
}

// neighbour finds, in the item order of the last layout, the nearest item after (or
// before) id that is present in spans.
func (f *Follow) neighbour(id string, spans map[string][2]int, after bool) (string, bool) {
	var order []string
	for i, r := range f.rows {
		if i == 0 || f.rows[i-1].Item != r.Item {
			order = append(order, r.Item)
		}
	}
	at := slices.Index(order, id)
	if at < 0 {
		return "", false
	}
	step := -1
	if after {
		step = 1
	}
	for i := at + step; i >= 0 && i < len(order); i += step {
		if _, ok := spans[order[i]]; ok {
			return order[i], true
		}
	}
	return "", false
}

// followSpans maps each item to the indexes of its first and last rows.
func followSpans(rows []FollowRow) map[string][2]int {
	spans := make(map[string][2]int)
	for i, r := range rows {
		sp, ok := spans[r.Item]
		if !ok {
			sp[0] = i
		}
		sp[1] = i
		spans[r.Item] = sp
	}
	return spans
}
