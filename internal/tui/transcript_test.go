package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// The live tests run the App under teatest against a real engine with fake
// adapters; the rest drive transcriptLines and the App directly.

// settled waits until the engine has finished the terminal delivery of the
// turn, so that its transcript no longer grows, and returns the item count.
func settled(t *testing.T, r *rig, turn string) int {
	t.Helper()
	cl := dial(t, r.paths)
	deadline := time.Now().Add(20 * time.Second)
	last := -1
	for {
		reply, err := cl.Transcript(context.Background(), turn, "", 200)
		if err != nil {
			t.Fatal(err)
		}
		open := false
		for _, at := range mustTurn(t, r, turn).Attempts {
			open = open || at.Status == ""
		}
		if !open && len(reply.Items) == last {
			r.waitCaughtUp(t)
			return last
		}
		last = len(reply.Items)
		if time.Now().After(deadline) {
			t.Fatal("the turn never finished delivering")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// openTranscript shows the transcript tab at the given size and waits until
// the App has loaded want items and no body is still loading.
func openTranscript(t *testing.T, r *rig, want, w, h int) string {
	t.Helper()
	// Set through the program, so the App is not touched from this goroutine.
	r.until(t, "the transcript tab", func(a *App) (string, bool) { a.tab = tabTranscript; return "", true })
	send(r, tea.WindowSizeMsg{Width: w, Height: h})
	return r.until(t, "a loaded transcript", func(a *App) (string, bool) {
		v := a.View()
		return v, a.tab == tabTranscript && len(a.tItems) == want && !a.tLoading && !a.tReload && !strings.Contains(v, "(loading…)")
	})
}

func headerKinds(view string) []string {
	var kinds []string
	for _, l := range strings.Split(view, "\n") {
		switch {
		case strings.HasPrefix(l, "message"):
			kinds = append(kinds, "message")
		case strings.HasPrefix(l, "attempt "):
			kinds = append(kinds, "attempt")
		}
	}
	return kinds
}

func squash(s string) string { return strings.Join(strings.Fields(s), "") }

func TestTranscriptReviewTurnInOrderWithFullBodies(t *testing.T) {
	const before, after = "COMMENTARY-BEFORE the envelope", "COMMENTARY-AFTER the envelope"
	reviewer := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return before + "\n" + fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID) + "\n" + after
	})
	r := startAnswerRig(t, scripted(t, enginetest.Reply(answerText), reviewer))
	turn := openReviewTurn(t, r)
	turnIn(t, r, "approved")
	v := openTranscript(t, r, settled(t, r, turn), 120, 1000)

	cl := dial(t, r.paths)
	reply, err := cl.Transcript(context.Background(), turn, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	var bodies []string
	for _, it := range reply.Items {
		kind := "message"
		if strings.HasPrefix(it.Ref, "attempt:") {
			kind = "attempt"
		}
		want = append(want, kind)
		body, err := cl.Content(context.Background(), it.Ref)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(body))
	}
	// request, author's attempt, then each reviewer's request and attempt
	if got := headerKinds(v); strings.Join(got, ",") != strings.Join(want, ",") || want[0] != "message" || want[1] != "attempt" {
		t.Fatalf("items %v, engine order %v\n%s", got, want, v)
	}
	flat := squash(v)
	for i, b := range bodies {
		if !strings.Contains(flat, squash(b)) {
			t.Fatalf("body %d (%s) is not shown whole:\n%s", i, reply.Items[i].Ref, v)
		}
	}
	for _, p := range []string{"question", answerText, before, after} {
		if !strings.Contains(v, p) {
			t.Fatalf("missing %q:\n%s", p, v)
		}
	}
	if strings.Index(v, answerText) > strings.Index(v, before) {
		t.Fatalf("author output after the reviewer's:\n%s", v)
	}
	// A later event must not list anything twice.
	send(r, tickMsg{})
	if got := headerKinds(view(t, r)); len(got) != len(want) {
		t.Fatalf("after a reload %v items, want %d", got, len(want))
	}
}

func TestTranscriptHeaderFields(t *testing.T) {
	author := enginetest.Reply(answerText)
	author.Usage = &transport.Usage{Prompt: 4321, Completion: 77}
	r := startAnswerRig(t, scripted(t, author, approve()))
	turn := openReviewTurn(t, r)
	turnIn(t, r, "approved")
	v := openTranscript(t, r, settled(t, r, turn), 300, 1000)
	var authorHdr, reviewHdr string
	for _, l := range strings.Split(v, "\n") {
		if strings.HasPrefix(l, "attempt ") && strings.Contains(l, "· author · author ·") {
			authorHdr = l
		}
		if strings.HasPrefix(l, "attempt ") && strings.Contains(l, "· reviewer · review ·") {
			reviewHdr = l
		}
	}
	for _, p := range []string{"status complete", "scrubs 0", "est. ", "reported prompt 4321, completion 77"} {
		if !strings.Contains(authorHdr, p) {
			t.Errorf("author header lacks %q: %q", p, authorHdr)
		}
	}
	if strings.Contains(authorHdr, "transport") {
		t.Errorf("transport status shown when ok: %q", authorHdr)
	}
	for _, p := range []string{"status complete", "verdict "} {
		if !strings.Contains(reviewHdr, p) {
			t.Errorf("reviewer header lacks %q: %q", p, reviewHdr)
		}
	}
}

func TestTranscriptRepairBesideOriginal(t *testing.T) {
	original := enginetest.Reply("ORIGINAL-MARK prose, no envelope")
	repair := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return "REPAIR-MARK\n" + fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(answerText)},
		"reviewer": {original, repair},
	})
	r := startAnswerRig(t, h)
	turn := openReviewTurn(t, r)
	turnIn(t, r, "approved")
	var origID, repairID string
	for _, at := range mustTurn(t, r, turn).Attempts {
		if at.RepairOf != nil {
			origID, repairID = *at.RepairOf, at.ID
		}
	}
	if repairID == "" {
		t.Fatal("no repair attempt")
	}

	wide := openTranscript(t, r, settled(t, r, turn), 120, 1000)
	// The terminal delivery message quotes both outputs again; the pair ends before the next message.
	pair := wide[:strings.Index(wide, "message · engine")]
	lines := strings.Split(pair, "\n")
	label, mark := -1, -1
	for i, l := range lines {
		if strings.HasPrefix(l, "original") && strings.Contains(l, "│ repair") {
			label = i
		}
		if strings.Contains(l, "ORIGINAL-MARK") {
			mark = i
			if !strings.Contains(l, "│") {
				t.Errorf("original is not in the left column: %q", l)
			}
		}
		if len([]rune(l)) > 120 {
			t.Errorf("line wider than the terminal: %q", l)
		}
	}
	if label < 0 || mark < label {
		t.Fatalf("no original|repair label row above the bodies (label %d, mark %d):\n%s", label, mark, wide)
	}
	if n := strings.Count(pair, "REPAIR-MARK"); n != 1 {
		t.Fatalf("repair output appears %d times in the pair:\n%s", n, wide)
	}
	for _, id := range []string{origID, repairID} {
		if n := strings.Count(wide, "attempt "+id); n != 1 {
			t.Fatalf("attempt %s is headed %d times:\n%s", id, n, wide)
		}
	}
	if !strings.Contains(wide, "verdict ") {
		t.Fatalf("no classification in the repair pair:\n%s", wide)
	}

	narrow := openTranscript(t, r, settled(t, r, turn), 60, 1000)
	var order []string
	for _, l := range strings.Split(narrow[:strings.Index(narrow, "message · engine")], "\n") {
		switch {
		case strings.TrimSpace(l) == "original":
			order = append(order, "original")
		case strings.TrimSpace(l) == "repair":
			order = append(order, "repair")
		case strings.Contains(l, "ORIGINAL-MARK"):
			order = append(order, "ORIGINAL-MARK")
		case strings.Contains(l, "REPAIR-MARK"):
			order = append(order, "REPAIR-MARK")
		case strings.Contains(l, "│"):
			t.Errorf("a column separator at 60 cells: %q", l)
		}
	}
	if got := strings.Join(order, ","); got != "original,ORIGINAL-MARK,repair,REPAIR-MARK" {
		t.Fatalf("stacked order %s:\n%s", got, narrow)
	}
}

func mustTurn(t *testing.T, r *rig, id string) wire.TurnReply {
	t.Helper()
	reply, err := dial(t, r.paths).Turn(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestTranscriptBlockUnderItsVerdict(t *testing.T) {
	r := startAnswerRig(t, scripted(t, enginetest.Reply(answerText), blockVerdict()))
	turn := openReviewTurn(t, r)
	turnIn(t, r, "incomplete")
	v := openTranscript(t, r, settled(t, r, turn), 120, 1000)
	if !strings.Contains(v, "block ") {
		t.Fatalf("no block:\n%s", v)
	}
	// The block and its evidence arrive with the turn record after the turn ends.
	v = r.until(t, "the block's evidence", func(a *App) (string, bool) {
		return a.View(), strings.Contains(a.View(), "line 3 divides by zero")
	})
	for _, p := range []string{"severity medium", "state open", "evidence: line 3 divides by zero", "remedy: guard the divisor"} {
		if !strings.Contains(v, p) {
			t.Fatalf("block lacks %q:\n%s", p, v)
		}
	}
	if strings.Index(v, "· reviewer ·") > strings.Index(v, "block ") {
		t.Fatalf("block above the verdict's attempt:\n%s", v)
	}
}

func TestTranscriptLargeOutputReachableByScrolling(t *testing.T) {
	var text strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&text, "row-%05d\n", i)
	}
	if text.Len() != 200000 {
		t.Fatalf("output is %d bytes", text.Len())
	}
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {enginetest.Reply(text.String())}, "reviewer": {approve()}},
		enginetest.WithContextBound("author", 1000000), enginetest.WithContextBound("reviewer", 1000000),
		enginetest.WithOutputBound("author", 1000000))
	r := startAnswerRig(t, h)
	submit(t, r, wire.SubmitRequest{SubmissionID: "big", Input: "question", Preset: ptr("solo")})
	turn := turnIn(t, r, "solo_complete")
	reply, err := dial(t, r.paths).Transcript(context.Background(), turn, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	openTranscript(t, r, len(reply.Items), 80, 24)
	var v string
	if v = view(t, r); strings.Contains(v, "row-19999") {
		t.Fatal("the last line is visible at the top")
	}
	send(r, tea.KeyMsg{Type: tea.KeyEnd})
	if v = view(t, r); !strings.Contains(v, "row-19999") {
		t.Fatalf("End did not reach the last line:\n%s", v)
	}
	send(r, tea.KeyMsg{Type: tea.KeyHome})
	if v = view(t, r); !strings.Contains(v, "row-00000") {
		t.Fatalf("Home did not return to the top:\n%s", v)
	}
	// Page down reaches every line in turn.
	for i := 0; i < 5; i++ {
		send(r, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if v = view(t, r); !strings.Contains(v, "row-000") || strings.Contains(v, "row-00000\n") {
		t.Fatalf("PgDown did not move:\n%s", v)
	}
}

func TestTranscriptLoadsMoreThanOnePage(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	paths := newPaths(t)
	startServer(t, paths, h.Engine, h)
	resp, err := dial(t, paths).Submit(context.Background(), wire.SubmitRequest{SubmissionID: "seed", Input: "question"})
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(resp.TurnID, "approved")
	const seeded = 250
	for i := 0; i < seeded; i++ {
		if _, _, _, err := h.Store.AppendMessage(resp.TurnID, "author", "author", nil, []byte(fmt.Sprintf("seed-%03d", i)), nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app := NewApp(ctx, paths.Socket)
	r := &rig{h: h, paths: paths, app: app, seen: &seen{}}
	r.tm = teatest.NewTestModel(t, &probe{app: app}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })
	r.waitConnected(t)

	r.until(t, "every item loaded", func(a *App) (string, bool) { return "", len(a.tItems) >= seeded+4 })
	send(r, tea.KeyMsg{Type: tea.KeyTab})
	r.until(t, "the transcript lines", func(a *App) (string, bool) {
		flat := strings.Join(a.tLines, "\n")
		for i := 0; i < seeded; i++ {
			if strings.Count(flat, fmt.Sprintf("seed-%03d", i)) != 1 {
				return "", false
			}
		}
		return "", true
	})
}

func TestTranscriptLabelsQuarantinedLateResult(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {enginetest.Reply("LATE-AUTHOR-OUTPUT")}, "reviewer": nil},
		enginetest.CancelUnconfirmed())
	release := h.Hold("author")
	r := startAnswerRig(t, h)
	r.typeLine("question")
	r.until(t, "the author's attempt", func(a *App) (string, bool) {
		for _, at := range a.state.Attempts {
			if at.Phase == "author" {
				return "", true
			}
		}
		return "", false
	})
	turn := r.until(t, "the open turn", func(a *App) (string, bool) { return a.CurrentTurn(), a.CurrentTurn() != "" })
	if err := dial(t, r.paths).Cancel(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	turnIn(t, r, "cancelled")
	if v := openTranscript(t, r, 2, 120, 1000); strings.Contains(v, "quarantined") {
		t.Fatalf("labelled before any late result:\n%s", v)
	}
	release()
	v := untilView(t, r, "LATE-AUTHOR-OUTPUT", "quarantined (late)")
	if !strings.Contains(v, "status complete") {
		t.Fatalf("late result has no status:\n%s", v)
	}
}

// transcriptApp is an App on the transcript tab with no engine behind it.
func transcriptApp(width, height int) *App {
	a := NewApp(context.Background(), "")
	a.Update(tea.WindowSizeMsg{Width: width, Height: height})
	a.state.Turns["t1"] = TurnInfo{ID: "t1", State: "approved"}
	a.state.CurrentTurn = "t1"
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	return a
}

func seedItems(a *App, from, to int) []wire.TranscriptItem {
	var items []wire.TranscriptItem
	for i := from; i < to; i++ {
		ref := fmt.Sprintf("msg:m%03d", i)
		body := fmt.Sprintf("body %03d", i)
		a.content.Store(ref, body, nil)
		items = append(items, wire.TranscriptItem{ID: fmt.Sprintf("m%03d", i), Ref: ref, ByteLen: int64(len(body))})
	}
	return items
}

func TestTranscriptViewStaysPutWhileScrolledUp(t *testing.T) {
	a := transcriptApp(80, 24)
	a.Update(transcriptMsg{turn: "t1", items: seedItems(a, 0, 40)})
	a.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	a.Update(tea.KeyMsg{Type: tea.KeyHome})
	top := a.View()
	if !strings.Contains(top, "body 000") {
		t.Fatalf("not at the top:\n%s", top)
	}
	a.Update(transcriptMsg{turn: "t1", items: seedItems(a, 0, 60)})
	if a.View() != top {
		t.Fatalf("the view moved when items arrived:\n%s", a.View())
	}
	a.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if a.View() == top {
		t.Fatal("PgDown did not scroll")
	}
}

func TestTranscriptViewFollowsWhileAtTheBottom(t *testing.T) {
	a := transcriptApp(80, 24)
	a.Update(transcriptMsg{turn: "t1", items: seedItems(a, 0, 40)})
	a.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if v := a.View(); !strings.Contains(v, "body 039") {
		t.Fatalf("End did not reach the last item:\n%s", v)
	}
	a.Update(transcriptMsg{turn: "t1", items: seedItems(a, 0, 60)})
	if v := a.View(); !strings.Contains(v, "body 059") {
		t.Fatalf("the view did not follow the new items:\n%s", v)
	}
	// Moving up leaves the bottom: later items no longer move the view.
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	held := a.View()
	a.Update(transcriptMsg{turn: "t1", items: seedItems(a, 0, 80)})
	if a.View() != held {
		t.Fatal("the view moved after scrolling up from the bottom")
	}
}

func TestTranscriptShortTranscriptFollowsOnceItGrows(t *testing.T) {
	a := transcriptApp(80, 24)
	a.Update(transcriptMsg{turn: "t1", items: seedItems(a, 0, 3)})
	a.Update(transcriptMsg{turn: "t1", items: seedItems(a, 0, 60)})
	if v := a.View(); !strings.Contains(v, "body 059") {
		t.Fatalf("a view at the bottom did not follow:\n%s", v)
	}
}

func TestTranscriptReloadReplacesItems(t *testing.T) {
	a := transcriptApp(80, 60)
	items := seedItems(a, 0, 5)
	a.Update(transcriptMsg{turn: "t1", items: items})
	a.Update(transcriptMsg{turn: "t1", items: items})
	if n := strings.Count(a.View(), "body 003"); n != 1 {
		t.Fatalf("an item is shown %d times after a reload", n)
	}
}

func TestTranscriptScrollKeys(t *testing.T) {
	a := transcriptApp(80, 24)
	a.Update(transcriptMsg{turn: "t1", items: seedItems(a, 0, 100)})
	body := a.bodyHeight()
	row := func() int { return a.tRow }
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if row() != 1 {
		t.Fatalf("down: row %d", row())
	}
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	a.Update(tea.KeyMsg{Type: tea.KeyUp})
	if row() != 0 {
		t.Fatalf("up past the top: row %d", row())
	}
	a.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if row() != body-1 {
		t.Fatalf("page down: row %d, want %d", row(), body-1)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if row() != 0 {
		t.Fatalf("page up: row %d", row())
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if row() != len(a.tLines)-body {
		t.Fatalf("end: row %d, want %d", row(), len(a.tLines)-body)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if row() != len(a.tLines)-body {
		t.Fatalf("down past the end: row %d", row())
	}
	a.Update(tea.KeyMsg{Type: tea.KeyHome})
	if row() != 0 {
		t.Fatalf("home: row %d", row())
	}
}

func TestTranscriptBodyStates(t *testing.T) {
	s := NewState()
	s.Attempts["a1"] = AttemptInfo{ID: "a1", TurnID: "t1", Participant: "p", Phase: "author", Status: "complete"}
	s.Attempts["a2"] = AttemptInfo{ID: "a2", TurnID: "t1", Participant: "p", Phase: "review"}
	in := transcriptInput{content: NewContentCache(), items: []wire.TranscriptItem{
		{ID: "a1", Ref: "attempt:a1"},
		{ID: "a2", Ref: "attempt:a2"},
		{ID: "x", Ref: "attempt:x", ByteLen: 5},
	}}
	flat := strings.Join(transcriptLines(s, "t1", in, 80), "\n")
	for _, p := range []string{"(no output)", "(waiting for output)", "(loading…)", "status running"} {
		if !strings.Contains(flat, p) {
			t.Errorf("missing %q:\n%s", p, flat)
		}
	}
	in.content.Store("attempt:x", "", fmt.Errorf("boom"))
	if flat = strings.Join(transcriptLines(s, "t1", in, 80), "\n"); !strings.Contains(flat, "could not load: boom") {
		t.Errorf("a failed fetch is not shown:\n%s", flat)
	}
}

func TestTranscriptHeaderShowsNotOKTransportAndQuarantine(t *testing.T) {
	s := NewState()
	p := int64(9)
	s.Attempts["a1"] = AttemptInfo{ID: "a1", TurnID: "t1", Participant: "p", Phase: "delivery", Status: "interrupted",
		TransportStatus: "reset", ScrubCount: 3, EstimatedTokens: 120, ReportedCompletion: &p}
	in := transcriptInput{content: NewContentCache(), quarantined: map[string]bool{"a1": true},
		items: []wire.TranscriptItem{{ID: "a1", Ref: "attempt:a1"}}}
	got := strings.Join(transcriptLines(s, "t1", in, 200), "\n")
	for _, w := range []string{"attempt a1", "p · delivery", "status interrupted", "transport reset", "scrubs 3", "est. 120 tokens", "reported completion 9", "quarantined (late)"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q:\n%s", w, got)
		}
	}
	if strings.Contains(got, "reported prompt") {
		t.Errorf("an absent figure is shown:\n%s", got)
	}
}

func TestTranscriptBlocksBelongToTheirOwnVerdict(t *testing.T) {
	s := NewState()
	s.Attempts["a1"] = AttemptInfo{ID: "a1", TurnID: "t1", Participant: "r1", Phase: "review", Status: "complete"}
	s.Attempts["a2"] = AttemptInfo{ID: "a2", TurnID: "t1", Participant: "r2", Phase: "review", Status: "complete"}
	s.Verdicts["v1"] = VerdictInfo{ID: "v1", AttemptID: "a1"}
	s.Verdicts["v2"] = VerdictInfo{ID: "v2", AttemptID: "a2"}
	s.Blocks["v2:b"] = BlockInfo{ID: "v2:b", Severity: "high", State: "withdrawn"}
	in := transcriptInput{content: NewContentCache(), blockDetail: map[string]wire.OpenBlock{"v2:b": {ID: "v2:b", Evidence: "EV", Remedy: "RM"}},
		items: []wire.TranscriptItem{{ID: "a1", Ref: "attempt:a1"}, {ID: "a2", Ref: "attempt:a2"}}}
	lines := transcriptLines(s, "t1", in, 80)
	at := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		return -1
	}
	if h1, h2, b := at("attempt a1"), at("attempt a2"), at("block v2:b"); h1 >= h2 || h2 >= b {
		t.Fatalf("block at %d, headers at %d and %d:\n%s", b, h1, h2, strings.Join(lines, "\n"))
	}
	for _, w := range []string{"severity high", "state withdrawn", "evidence: EV", "remedy: RM"} {
		if at(w) < 0 {
			t.Errorf("missing %q", w)
		}
	}
}

func TestWrapTextKeepsEveryRune(t *testing.T) {
	text := strings.Repeat("héllo wörld 世界 ", 40) + "\ttab"
	for _, w := range []int{1, 2, 7, 40} {
		lines := wrapText(text, w)
		var joined strings.Builder
		for _, l := range lines {
			if cells := len([]rune(l)); w >= 2 && cells > w {
				t.Fatalf("width %d: line %q has %d runes", w, l, cells)
			}
			joined.WriteString(l)
		}
		if want := strings.ReplaceAll(text, "\t", "    "); joined.String() != want {
			t.Fatalf("width %d: text changed", w)
		}
	}
	if got := wrapText("a\x1b[31mb", 80); got[0] != "a·[31mb" {
		t.Fatalf("control characters reach the terminal: %q", got)
	}
}

func TestTranscriptOpenedFinishedTurnStartsAtTop(t *testing.T) {
	a := transcriptApp(80, 24)
	var items []wire.TranscriptItem
	for i := 0; i < 3; i++ {
		items = append(items, wire.TranscriptItem{ID: fmt.Sprintf("m%d", i), Ref: fmt.Sprintf("msg:m%d", i), ByteLen: 400})
	}
	a.Update(transcriptMsg{turn: "t1", items: items})
	for i := 0; i < 3; i++ {
		var lines []string
		for j := 0; j < 30; j++ {
			lines = append(lines, fmt.Sprintf("item%d-line%02d", i, j))
		}
		a.Update(contentMsg{ref: fmt.Sprintf("msg:m%d", i), data: strings.Join(lines, "\n")})
	}
	if v := a.View(); !strings.Contains(v, "item0-line00") {
		t.Fatalf("bodies arriving moved the view off the top:\n%s", v)
	}
}
