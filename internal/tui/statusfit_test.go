package tui

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
)

const (
	fxMeter   = "[████░░░░░░]"
	fxTokens  = "48213/200000"
	fxState   = "reviewers_dispatched"
	fxElapsed = "1:12/10:00"
	fxRound   = "round 1/2"
)

func fxItems() statusItems {
	return statusItems{preset: "review", state: fxState, elapsed: fxElapsed, meter: fxMeter, tokens: fxTokens, round: fxRound, chatTab: true}
}

// wantLine builds left, padding to width, right.
func wantLine(left string, width int, right string) string {
	return left + strings.Repeat(" ", width-cellWidth(left)-cellWidth(right)) + right
}

func TestFitStatusLineFixtures(t *testing.T) {
	idle := fxItems()
	idle.state, idle.elapsed, idle.round = "idle", "", ""
	if got, want := fitStatusLine(idle, 100), wantLine("review · idle · [████░░░░░░] 48213/200000", 100, "ctrl+o: reviews  tab: switch  /help"); got != want {
		t.Errorf("idle: %q want %q", got, want)
	}
	if got, want := fitStatusLine(fxItems(), 100), wantLine("review · reviewers_dispatched · 1:12/10:00 · 48213/200000 · round 1/2", 100, "tab: switch  /help"); got != want {
		t.Errorf("100: %q want %q", got, want)
	}
	if got, want := fitStatusLine(fxItems(), 80), wantLine("review · reviewers_dispatched · 1:12/10:00 · 48213/200000", 80, "tab: switch  /help"); got != want {
		t.Errorf("80: %q want %q", got, want)
	}
	q := fxItems()
	q.quick = "quick:fastcheck123"
	want := "review · quick:fastcheck123 · revi… · 48213/200000" + strings.Repeat(" ", 12) + "tab: switch  /help"
	if got := fitStatusLine(q, 80); got != want {
		t.Errorf("quick: %q want %q", got, want)
	}
}

func TestFitStatusLineTranscriptTab(t *testing.T) {
	it := fxItems()
	it.chatTab = false
	for w := 1; w <= 200; w++ {
		if strings.Contains(fitStatusLine(it, w), "ctrl+o") {
			t.Fatalf("width %d shows the reviews hint", w)
		}
	}
}

func TestFitStatusLineEveryWidthAndState(t *testing.T) {
	states := []string{
		engine.StateSubmitted, engine.StateAuthorDispatched, engine.StateCandidateFrozen,
		engine.StateReviewersDispatched, engine.StateRevisionDispatched, engine.StateApproved,
		engine.StateSoloComplete, engine.StateIncomplete, engine.StateCancelled, engine.StateOverridden, "idle",
	}
	for _, st := range states {
		for _, quick := range []string{"", "quick:abcdefghijkl"} {
			for _, next := range []string{"", "next: abcdefghijkl"} {
				it := fxItems()
				it.state, it.quick, it.next = st, quick, next
				it.preset = "abcdefghijkl"
				it.tokens = "123456789/123456789"[:15]
				for w := 80; w <= 200; w++ {
					got := fitStatusLine(it, w)
					if cellWidth(got) != w || strings.Contains(got, "\n") {
						t.Fatalf("%s w=%d: width %d: %q", st, w, cellWidth(got), got)
					}
					if !strings.HasSuffix(got, "tab: switch  /help") {
						t.Fatalf("%s w=%d: no key hint: %q", st, w, got)
					}
					left := strings.TrimSuffix(strings.TrimSuffix(got, "tab: switch  /help"), "ctrl+o: reviews  ")
					if !strings.HasSuffix(left, "  ") {
						t.Fatalf("%s w=%d: parts not separated: %q", st, w, got)
					}
					for _, must := range []string{it.preset, quick, it.tokens} {
						if !strings.Contains(got, must) {
							t.Fatalf("%s w=%d: %q missing %q", st, w, got, must)
						}
					}
				}
			}
		}
	}
}

func TestFitStatusLineBelowThat(t *testing.T) {
	long := fxItems()
	long.preset = strings.Repeat("p", 30)
	got := fitStatusLine(long, 80)
	if cellWidth(got) > 80 || !strings.HasPrefix(got, long.preset) {
		t.Errorf("30-cell preset: %q", got)
	}
	got = fitStatusLine(fxItems(), 40)
	// state removed (step 6), right part removed (7), then cut (8).
	if want := "review · 48213/200000"; got != want {
		t.Errorf("40: %q want %q", got, want)
	}
	for _, w := range []int{1, 5, 10, 12, 20} {
		got = fitStatusLine(fxItems(), w)
		if cellWidth(got) > w || strings.Contains(got, "\n") {
			t.Errorf("w=%d: %q", w, got)
		}
	}
	got = fitStatusLine(fxItems(), 12)
	if got != "review · 48…" {
		t.Errorf("cut: %q", got)
	}
}

func TestFitStatusLineWideAndCombining(t *testing.T) {
	for _, name := range []string{"プリセット", "révieẃ"} {
		it := fxItems()
		it.preset = name
		got := fitStatusLine(it, 80)
		if cellWidth(got) != 80 || !strings.HasSuffix(got, "tab: switch  /help") {
			t.Errorf("%q: width %d: %q", name, cellWidth(got), got)
		}
	}
	if cellWidth("プ") != 2 || cellWidth("é") != 1 {
		t.Fatal("measure")
	}
	// A wide character straddling the cut is dropped whole.
	it := statusItems{preset: "ab" + "プリ", tokens: "1/2"}
	if got := fitStatusLine(it, 4); got != "ab…" {
		t.Errorf("straddle: %q", got)
	}
}

func TestFitStatusLineFullCounterAndNames(t *testing.T) {
	it := fxItems()
	it.preset = "abcdefghijkl"
	it.quick = "quick:abcdefghijkl"
	it.tokens = "123456789012345"
	got := fitStatusLine(it, 80)
	if cellWidth(got) != 80 || !strings.HasSuffix(got, "tab: switch  /help") || !strings.Contains(got, it.tokens) {
		t.Errorf("%q", got)
	}
}

func TestFitStatusLineNextPreset(t *testing.T) {
	it := fxItems()
	it.next = "next: solo"
	it.state, it.elapsed, it.round = "idle", "", ""
	got := fitStatusLine(it, 120)
	if !strings.HasPrefix(got, "review · next: solo · idle · ") {
		t.Errorf("fits: %q", got)
	}
	// Too wide: the next preset goes before the meter.
	it = fxItems()
	it.next = "next: solo"
	it.state, it.elapsed, it.round = "idle", "", ""
	w := cellWidth("review · idle · [████░░░░░░] 48213/200000") + 2 + cellWidth("ctrl+o: reviews  tab: switch  /help")
	got = fitStatusLine(it, w)
	if strings.Contains(got, "next:") || !strings.Contains(got, fxMeter) {
		t.Errorf("next must go before the meter: %q", got)
	}
}

func TestFitStatusLineQuickProfileCutByStep8(t *testing.T) {
	it := fxItems()
	it.quick = "quick:fastcheck123"
	// Steps 6 and 7 cannot make room, so step 8 cuts; as much of the quick
	// profile as fits stays visible.
	for w, want := range map[int]string{
		40: "review · quick:fastcheck123 · 48213/200…",
		25: "review · quick:fastcheck…",
	} {
		got := fitStatusLine(it, w)
		if got != want || cellWidth(got) > w {
			t.Errorf("w=%d: %q want %q", w, got, want)
		}
	}
}
