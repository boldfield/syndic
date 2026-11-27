package tui

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// statusItems are the status line's items by role, each already drawn as the
// text the terminal will receive. An empty string means the item is absent.
// Meter and tokens are one item: the meter, one space, the counter.
type statusItems struct {
	preset  string
	next    string // next: <preset>, when /mode has set the next turn's preset
	quick   string // quick:<name>
	state   string
	elapsed string // elapsed/deadline
	meter   string
	tokens  string // tokens/limit
	round   string
	chatTab bool // the chat tab is active: ctrl+o: reviews precedes the key hint
}

const statusSep = " · "

// left joins the items present, in v6 section 1's order, by statusSep.
func (it statusItems) left() string {
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	add(it.preset)
	add(it.next)
	add(it.quick)
	add(it.state)
	add(it.elapsed)
	if it.meter != "" && it.tokens != "" {
		add(it.meter + " " + it.tokens)
	} else {
		add(it.tokens)
	}
	add(it.round)
	return strings.Join(parts, statusSep)
}

// cellWidth is the width of s in terminal cells.
func cellWidth(s string) int { return runewidth.StringWidth(s) }

// cutCells returns the longest prefix of s of at most n cells, dropping whole
// a wide character that would straddle the cut. Combining characters stay
// with their base.
func cutCells(s string, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	w := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		gw := runewidth.StringWidth(g.Str())
		if w+gw > n {
			break
		}
		w += gw
		b.WriteString(g.Str())
	}
	return b.String()
}

// fitStatusLine lays out the status line by v6 addendum 1 section 1, width
// cells at most. It is a pure function of the items and the width.
func fitStatusLine(it statusItems, width int) string {
	if width <= 0 {
		return ""
	}
	next, meter, reviews, round := it.next, it.meter, it.chatTab, it.round
	state, elapsed := it.state, it.elapsed

	build := func(withState, withRight bool) (string, string) {
		cur := it
		cur.next, cur.meter, cur.round, cur.state, cur.elapsed = next, meter, round, state, elapsed
		if !withState {
			cur.state, cur.elapsed, cur.round = "", "", ""
		}
		right := ""
		if withRight {
			right = keyHint
			if reviews {
				right = reviewsHint + "  " + keyHint
			}
		}
		return cur.left(), right
	}
	try := func(withState bool) (string, bool) {
		l, r := build(withState, true)
		if cellWidth(l)+2+cellWidth(r) > width {
			return "", false
		}
		pad := width - cellWidth(l) - cellWidth(r)
		return l + strings.Repeat(" ", pad) + r, true
	}

	steps := []func(){
		func() { next = "" },
		func() { meter = "" },
		func() { reviews = false },
		func() { round = "" },
		func() {
			if cellWidth(state) > 5 {
				state = cutCells(state, 4) + "…"
			}
		},
		func() { elapsed = "" },
	}
	if line, ok := try(true); ok {
		return line
	}
	for _, step := range steps {
		step()
		if line, ok := try(true); ok {
			return line
		}
	}
	// Step 6: the turn state is removed.
	if line, ok := try(false); ok {
		return line
	}
	// Step 7: the right part is removed whole; step 8: cut from the right.
	l, _ := build(false, false)
	if cellWidth(l) <= width {
		return l
	}
	return cutCells(l, width-1) + "…"
}
