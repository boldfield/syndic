package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/wire"
	"github.com/mattn/go-runewidth"
)

var stripT0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// revisingState is a review turn in its second round, 30s in, with half
// its token limit estimated.
func revisingState() State {
	s := NewState()
	s.ConnState = ConnStateConnected
	s.Status = &wire.StatusReply{DefaultPreset: "review"}
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{
		ID: "t1", State: "reviewers_dispatched", Preset: "review", SubmittedAt: stripT0,
		DeadlineDur: 15 * time.Minute, EstimatedTokens: 1500, LimitTokens: 3000, Round: 1, MaxRounds: 3,
	}
	return s
}

// assertStatusLine checks the shape every status line has: exactly w cells,
// the key hint at the right edge.
func assertStatusLine(t *testing.T, w int, line string) {
	t.Helper()
	if got := runewidth.StringWidth(line); got != w {
		t.Errorf("w=%d: line is %d cells: %q", w, got, line)
	}
	if !strings.HasSuffix(line, keyHint) {
		t.Errorf("w=%d: line does not end with %q: %q", w, keyHint, line)
	}
}

// TestStatusLineOverflowOrder walks each threshold of the addendum's order of
// giving way: the budget meter goes first, then ctrl+o: reviews, then round
// n/m, then the turn state shortens, then the elapsed time goes; below that
// the turn state, then the right part, then the cut from the right.
func TestStatusLineOverflowOrder(t *testing.T) {
	s := revisingState()
	now := stripT0.Add(30 * time.Second)
	const (
		both   = "ctrl+o: reviews  " + keyHint
		tokens = "est. 1500 / 3000"
		state  = "reviewers_dispatched"
		el     = "30s / 15m0s"
	)
	cases := []struct {
		w    int
		want string
	}{
		{122, wantLine("review · "+state+" · "+el+" · [████░░░░] "+tokens+" · round 1/3", 122, both)},
		{121, wantLine("review · "+state+" · "+el+" · "+tokens+" · round 1/3", 121, both)},
		{111, wantLine("review · "+state+" · "+el+" · "+tokens+" · round 1/3", 111, both)},
		{110, wantLine("review · "+state+" · "+el+" · "+tokens+" · round 1/3", 110, keyHint)},
		{94, wantLine("review · "+state+" · "+el+" · "+tokens+" · round 1/3", 94, keyHint)},
		{93, wantLine("review · "+state+" · "+el+" · "+tokens, 93, keyHint)},
		{82, wantLine("review · "+state+" · "+el+" · "+tokens, 82, keyHint)},
		{81, wantLine("review · revi… · "+el+" · "+tokens, 81, keyHint)},
		{67, wantLine("review · revi… · "+el+" · "+tokens, 67, keyHint)},
		{66, wantLine("review · revi… · "+tokens, 66, keyHint)},
		{53, wantLine("review · revi… · "+tokens, 53, keyHint)},
		// Below the rule's steps 1 to 5: the state, then the hints, then the cut.
		{52, wantLine("review · "+tokens, 52, keyHint)},
		{45, wantLine("review · "+tokens, 45, keyHint)},
		{44, "review · " + tokens},
		{25, "review · " + tokens},
		{24, "review · est. 1500 / 30…"},
	}
	for _, c := range cases {
		got := renderStatusLine(s, c.w, now, true, false)
		if got != c.want {
			t.Errorf("w=%d:\n got %q\nwant %q", c.w, got, c.want)
		}
		if runewidth.StringWidth(got) > c.w {
			t.Errorf("w=%d: line is %d cells", c.w, runewidth.StringWidth(got))
		}
	}
}

// TestStatusLineKeepsTokensAndHint checks every width from the smallest
// terminal up, with and without a quick profile and a limit.
func TestStatusLineKeepsTokensAndHint(t *testing.T) {
	now := stripT0.Add(75 * time.Second)
	for _, tc := range []struct {
		name   string
		edit   func(*TurnInfo)
		tokens string
	}{
		{"limit", func(*TurnInfo) {}, "est. 1500 / 3000"},
		{"no limit", func(ti *TurnInfo) { ti.LimitTokens = 0 }, "est. 1500"},
		{"quick", func(ti *TurnInfo) { ti.QuickProfile = ptr("fast") }, "est. 1500 / 3000"},
		{"quick, no limit", func(ti *TurnInfo) { ti.QuickProfile, ti.LimitTokens = ptr("fast"), 0 }, "est. 1500"},
	} {
		s := revisingState()
		ti := s.Turns["t1"]
		tc.edit(&ti)
		s.Turns["t1"] = ti
		for w := 80; w <= 140; w++ {
			for _, reviews := range []bool{false, true} {
				got := renderStatusLine(s, w, now, reviews, false)
				assertStatusLine(t, w, got)
				if !strings.Contains(got, tc.tokens) {
					t.Errorf("%s, w=%d: tokens/limit lost: %q", tc.name, w, got)
				}
				if strings.Contains(got, reviewsHint) && !reviews {
					t.Errorf("%s, w=%d: reviews hint off the chat tab: %q", tc.name, w, got)
				}
			}
		}
	}
}

// TestStatusLineNoLimitShedsNothingForTheMeter: without a limit there is no
// meter, so the first step must not take the token estimate with it.
func TestStatusLineNoLimitShedsNothingForTheMeter(t *testing.T) {
	s := revisingState()
	ti := s.Turns["t1"]
	ti.LimitTokens = 0
	ti.QuickProfile = ptr("fast")
	s.Turns["t1"] = ti
	now := stripT0.Add(30 * time.Second)
	want := "review · quick:fast · reviewers_dispatched · 30s / 15m0s · est. 1500 · round 1/3  ctrl+o: reviews  " + keyHint
	if got := renderStatusLine(s, runewidth.StringWidth(want), now, true, false); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	// One cell short, the reviews hint goes and the estimate stays.
	want = "review · quick:fast · reviewers_dispatched · 30s / 15m0s · est. 1500 · round 1/3" + strings.Repeat(" ", 18) + keyHint
	if got := renderStatusLine(s, runewidth.StringWidth(want), now, true, false); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestStatusLineIdle keeps both hints with no turn open.
func TestStatusLineIdle(t *testing.T) {
	s := revisingState()
	s.CurrentTurn = ""
	got := renderStatusLine(s, 80, stripT0, true, false)
	want := "review · idle" + strings.Repeat(" ", 80-13-35) + "ctrl+o: reviews  " + keyHint
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := renderStrip(s, 80, stripT0, false); got != "review · idle" {
		t.Errorf("idle strip = %q", got)
	}
}

// TestStatusLineShortensWideStatesByCell keeps the line valid UTF-8 and
// within width when the turn state has multi-byte and wide characters.
func TestStatusLineShortensWideStatesByCell(t *testing.T) {
	s := revisingState()
	ti := s.Turns["t1"]
	ti.State = "révision_審査中_dispatché"
	s.Turns["t1"] = ti
	for w := 80; w <= 100; w++ {
		got := renderStatusLine(s, w, stripT0, true, false)
		if !utf8.ValidString(got) {
			t.Fatalf("w=%d: invalid UTF-8: %q", w, got)
		}
		assertStatusLine(t, w, got)
	}
}

// TestAppStatusLineHintsByTab: ctrl+o: reviews only while the chat tab is
// active, keyHint always, a space between the fields and the hints.
func TestAppStatusLineHintsByTab(t *testing.T) {
	a := chatApp(t)
	a.state = revisingState()
	a.now = func() time.Time { return stripT0.Add(30 * time.Second) }
	for _, tb := range []tab{tabChat, tabTranscript, tabParticipants} {
		a.tab = tb
		for _, w := range []int{80, 100, 110, 111, 121, 122} {
			got := a.statusLine(w)
			assertStatusLine(t, w, got)
			if want := tb == tabChat && w >= 111; strings.Contains(got, reviewsHint) != want {
				t.Errorf("tab %d, w=%d: reviews hint shown, want %v: %q", tb, w, want, got)
			}
			if !strings.Contains(got, "  "+keyHint) {
				t.Errorf("tab %d, w=%d: fields touch the hint: %q", tb, w, got)
			}
		}
	}
	a.tab = tabChat
	if got := a.statusLine(122); !strings.HasSuffix(got, " ctrl+o: reviews  "+keyHint) {
		t.Errorf("chat tab at 122: %q", got)
	}
}

// TestStatusLineConnectingKeepsBothHints: the fixed connecting line keeps
// ctrl+o: reviews beside keyHint on the chat tab, and sheds it before the
// line itself when narrow.
func TestStatusLineConnectingKeepsBothHints(t *testing.T) {
	s := NewState()
	s.ConnState = ConnStateConnecting
	const fixed = "engine: connecting"
	right := reviewsHint + "  " + keyHint
	want := fixed + strings.Repeat(" ", 80-len(fixed)-len(right)) + right
	if got := renderStatusLine(s, 80, stripT0, true, false); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	w := len(fixed) + 2 + len(right)
	if got := renderStatusLine(s, w, stripT0, true, false); got != fixed+"  "+right {
		t.Errorf("w=%d: got %q", w, got)
	}
	want = fixed + strings.Repeat(" ", w-1-len(fixed)-len(keyHint)) + keyHint
	if got := renderStatusLine(s, w-1, stripT0, true, false); got != want {
		t.Errorf("w=%d: got %q\nwant %q", w-1, got, want)
	}
	if got := renderStatusLine(s, 80, stripT0, false, false); strings.Contains(got, reviewsHint) || !strings.HasSuffix(got, "  "+keyHint) {
		t.Errorf("without reviews: %q", got)
	}
}
