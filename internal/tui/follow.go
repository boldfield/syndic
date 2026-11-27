package tui

// FollowRow describes one drawn row of a scrolling view: the id of the item
// the row belongs to and the character offsets within that item at which the
// row begins and ends (End is exclusive; a row's End is at least its Char+1). Item ids are opaque, unique within a view and stable across
// renders; the rows of one item are contiguous and in increasing Char order.
type FollowRow struct {
	Item string
	Char int
	End  int
}

// followPoint names a row by content: the item and the character at which
// that row begins. none marks "before the first row". The seen mark is the
// exception: its char is the offset at which the last seen row ended.
type followPoint struct {
	item string
	char int
	none bool
}

// Follow decides which row a scrolling view shows first, whether the view is
// at the bottom and how many rows are new. It holds no row number between
// renders: away from the bottom it keeps an anchor (the row first shown) and
// a seen mark (the row at which the last shown row ended), both by content.
// The zero value is a model at the bottom.
type Follow struct {
	away   bool
	anchor followPoint
	seen   followPoint
	order  []string // distinct item ids of the rows last laid out
}

// NewFollow returns a model at the bottom.
func NewFollow() *Follow { return &Follow{} }

// followIndex maps each item to the range of its rows.
type followIndex map[string][2]int

func indexFollowRows(rows []FollowRow) (followIndex, []string) {
	idx := followIndex{}
	var order []string
	for i, r := range rows {
		if span, ok := idx[r.Item]; ok {
			idx[r.Item] = [2]int{span[0], i}
			continue
		}
		idx[r.Item] = [2]int{i, i}
		order = append(order, r.Item)
	}
	return idx, order
}

// rowAt returns the last row of the item that begins at or before char: the
// row that contains char, or the item's last row when the item is shorter.
func rowAt(rows []FollowRow, span [2]int, char int) int {
	at := span[0]
	for i := span[0]; i <= span[1]; i++ {
		if rows[i].Char > char {
			break
		}
		at = i
	}
	return at
}

// neighbour finds the item that stands in for a removed one: the next item
// of the previous layout that remains (forward), or the nearest earlier one
// (backward). It returns "" when none remains.
func (f *Follow) neighbour(gone string, idx followIndex, forward bool) string {
	at := -1
	for i, id := range f.order {
		if id == gone {
			at = i
			break
		}
	}
	if at < 0 {
		return ""
	}
	if forward {
		for _, id := range f.order[at+1:] {
			if _, ok := idx[id]; ok {
				return id
			}
		}
		return ""
	}
	for i := at - 1; i >= 0; i-- {
		if _, ok := idx[f.order[i]]; ok {
			return f.order[i]
		}
	}
	return ""
}

// seenRows resolves the seen mark against rows and returns how many rows
// from the top count as seen, moving the mark when its item changed or went.
func (f *Follow) seenRows(rows []FollowRow, idx followIndex) int {
	if f.seen.none {
		return 0
	}
	if span, ok := idx[f.seen.item]; ok {
		at := rowAt(rows, span, f.seen.char-1)
		if at == span[1] && rows[at].End < f.seen.char {
			f.seen.char = rows[at].End // the item now ends before the mark
		}
		return at + 1
	}
	if id := f.neighbour(f.seen.item, idx, false); id != "" {
		span := idx[id]
		f.seen = followPoint{item: id, char: rows[span[1]].End}
		return span[1] + 1
	}
	f.seen = followPoint{none: true}
	return 0
}

// Layout resolves the model against the current rows and the view's height.
// It returns the first row to show, whether the view is at the bottom, and
// the count of new rows (zero at the bottom). It applies the design's order:
// resolve the anchor, clamp to the content, then test for the bottom.
func (f *Follow) Layout(rows []FollowRow, height int) (first int, atBottom bool, newRows int) {
	if height < 1 {
		height = 1
	}
	n := len(rows)
	last := max(0, n-height)
	idx, order := indexFollowRows(rows)
	defer func() { f.order = order }()

	if !f.away {
		return last, true, 0
	}
	moved := false
	if span, ok := idx[f.anchor.item]; ok {
		first = rowAt(rows, span, f.anchor.char)
		if first == span[1] && rows[first].End <= f.anchor.char {
			f.anchor.char = rows[first].Char // an item that shrank moves it to its last row
		}
	} else {
		moved = true
		if id := f.neighbour(f.anchor.item, idx, true); id != "" {
			first = idx[id][0]
		} else if id := f.neighbour(f.anchor.item, idx, false); id != "" {
			first = idx[id][0]
		} else {
			first = last
		}
	}
	if first >= last {
		f.toBottom()
		return last, true, 0
	}
	if moved {
		f.anchor = followPoint{item: rows[first].Item, char: rows[first].Char}
	}
	return first, false, n - f.seenRows(rows, idx)
}

func (f *Follow) toBottom() {
	f.away = false
	f.anchor = followPoint{}
	f.seen = followPoint{}
}

// show places the view with row first at the top, leaving or returning to the
// bottom as the clamped position dictates. Called after Layout, so the mark
// is already resolved against rows.
func (f *Follow) show(rows []FollowRow, height, first int) {
	if height < 1 {
		height = 1
	}
	n := len(rows)
	last := max(0, n-height)
	first = min(max(first, 0), last)
	if first >= last {
		f.toBottom()
		return
	}
	idx, _ := indexFollowRows(rows)
	seen := n // leaving the bottom: everything drawn so far was seen
	if f.away {
		seen = f.seenRows(rows, idx)
	}
	seen = max(seen, first+height)
	f.away = true
	f.anchor = followPoint{item: rows[first].Item, char: rows[first].Char}
	f.seen = followPoint{item: rows[seen-1].Item, char: rows[seen-1].End}
}

// Scroll moves the view by delta rows (negative is up) and returns the same
// results as Layout for the new position.
func (f *Follow) Scroll(rows []FollowRow, height, delta int) (int, bool, int) {
	first, _, _ := f.Layout(rows, height)
	f.show(rows, height, first+delta)
	return f.Layout(rows, height)
}

// Home moves to the first row.
func (f *Follow) Home(rows []FollowRow, height int) (int, bool, int) {
	f.Layout(rows, height)
	f.show(rows, height, 0)
	return f.Layout(rows, height)
}

// End returns to the bottom and resumes following.
func (f *Follow) End(rows []FollowRow, height int) (int, bool, int) {
	f.toBottom()
	return f.Layout(rows, height)
}
