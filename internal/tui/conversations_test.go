package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// convRig is an App on a real engine whose author and reviewer approve at
// least n turns, with the harness and socket for the test's own requests.
type convRig struct {
	*driver
	rec   *engineRig
	h     *enginetest.Harness
	paths lockfile.Paths
}

func startConvRig(t *testing.T, n int, opts ...Option) *convRig {
	t.Helper()
	scripts := map[string][]fake.Step{}
	for i := range 3 * n { // the engine's calibration calls take some
		scripts["author"] = append(scripts["author"], enginetest.Reply(fmt.Sprintf("answer %d", i+1)))
		scripts["reviewer"] = append(scripts["reviewer"], approve())
	}
	h := enginetest.New(t, enginetest.RosterTOML, scripts)
	paths := newPaths(t)
	startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rec := &engineRig{}
	opts = append([]Option{WithWorkDir("/ws/here"), WithConversation("", nil)}, opts...)
	opts = append(opts, WithSubmitObserver(rec.record))
	d := &driver{t: t, app: NewApp(ctx, paths.Socket, opts...), msgs: make(chan tea.Msg, 1024)}
	d.app.Update(resizeMsg(80, 24))
	d.run(d.app.Init())
	d.until("connected", func(string) bool { return d.app.state.ConnState == ConnStateConnected && d.strip() != "" })
	return &convRig{driver: d, rec: rec, h: h, paths: paths}
}

// command runs a command line and waits until the view shows every part.
func (r *convRig) command(line string, parts ...string) string {
	r.t.Helper()
	r.typeLine(line)
	r.until(line+" to show "+strings.Join(parts, ", "), func(v string) bool {
		v += "\n" + strings.Join(r.app.panel, " ") // a panel line as said, before it wraps
		for _, p := range parts {
			if !strings.Contains(v, p) {
				return false
			}
		}
		return true
	})
	return r.app.View()
}

// submitElsewhere submits a turn to a new conversation in another workspace,
// from another client, and waits until it is approved.
func submitElsewhere(t *testing.T, r *convRig, workspace, input string) string {
	t.Helper()
	cl := dial(t, r.paths)
	reply, err := cl.Submit(context.Background(), wire.SubmitRequest{SubmissionID: "elsewhere-" + input, Input: input, Workspace: workspace})
	if err != nil {
		t.Fatalf("submit to %s: %v", workspace, err)
	}
	r.h.WaitTurnState(reply.TurnID, engine.StateApproved)
	// The App counts approved turns, so it must have seen this one end.
	r.until("the other turn to end", func(string) bool { return r.app.state.TurnState[reply.TurnID] == engine.StateApproved })
	return reply.ConversationID
}

func TestHelpListsCommandsAndKeys(t *testing.T) {
	a := offlineApp(t)
	a.input.SetValue("/help")
	a.update(tea.KeyMsg{Type: tea.KeyEnter})
	v := a.View()
	if !strings.Contains(v, "commands:") || !strings.Contains(v, "more lines (PgDn)") {
		t.Fatalf("/help view has no command list or no more-lines line:\n%s", v)
	}
	// Every command and key line is shown on some page.
	seen := v
	for range 5 {
		a.update(tea.KeyMsg{Type: tea.KeyPgDown})
		seen += "\n" + a.View()
	}
	for _, want := range append(append([]string{"keys:"}, commandHelp...), keyHelp...) {
		if !strings.Contains(seen, want) {
			t.Errorf("/help never shows %q", want)
		}
	}
	for _, name := range []string{"/new", "/conversations", "/switch", "/trim", "/rounds", "/budget", "/help"} {
		if !strings.Contains(seen, name+" ") {
			t.Errorf("/help does not list %s", name)
		}
	}
	for _, k := range []string{"Tab, Shift+Tab", "PgUp, PgDn", "Alt+Enter", "Ctrl+C", "Ctrl+D"} {
		if !strings.Contains(seen, k) {
			t.Errorf("/help does not list the key %s", k)
		}
	}
	if !strings.Contains(a.View(), "Detaching never cancels a turn") {
		t.Errorf("the last page of /help does not end the list:\n%s", a.View())
	}
	// PgUp goes back to the top; esc closes the panel.
	for range 5 {
		a.update(tea.KeyMsg{Type: tea.KeyPgUp})
	}
	if !strings.Contains(a.View(), "commands:") {
		t.Errorf("pgup did not return to the top:\n%s", a.View())
	}
	a.update(tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Contains(a.View(), "commands:") {
		t.Errorf("esc left the panel open")
	}
}

func TestRoundsShowsValueProvenanceAndCeiling(t *testing.T) {
	status := &wire.StatusReply{DefaultPreset: "review", Revision: wire.Revision{Default: 2, Ceiling: 3}}
	run := func(a *App, line string) string {
		a.input.SetValue(line)
		a.update(tea.KeyMsg{Type: tea.KeyEnter})
		if len(a.panel) > 0 {
			return strings.Join(a.panel, " ") // a line wider than the screen
		}
		return a.refusal
	}

	a := offlineApp(t)
	if got, want := run(a, "/rounds"), "rounds: the roster default, not known until connected; ceiling 5 (the engine's limit; the roster's is not known until connected)"; got != want {
		t.Errorf("offline /rounds = %q, want %q", got, want)
	}

	a.state.Status = status
	if got, want := run(a, "/rounds"), "rounds: 2 (the roster default for preset review); ceiling 3 (preset review)"; got != want {
		t.Errorf("/rounds = %q, want %q", got, want)
	}
	if a.state.maxRounds() != 2 {
		t.Errorf("round limit in force = %d, want the default 2", a.state.maxRounds())
	}

	// Above the preset's ceiling, below zero or not a number: refused, unchanged.
	for _, bad := range []string{"4", "-1", "two"} {
		if got := run(a, "/rounds "+bad); !strings.Contains(got, "not a whole number from 0 to the ceiling 3") || !strings.HasSuffix(got, "unchanged") {
			t.Errorf("/rounds %s = %q, want a refusal naming the ceiling", bad, got)
		}
		if a.roundsIsSet || a.state.RoundLimit != nil {
			t.Fatalf("/rounds %s changed the limit", bad)
		}
	}

	if got, want := run(a, "/rounds 3"), "rounds set to 3 for this process; ceiling 3 (preset review)"; got != want {
		t.Errorf("/rounds 3 = %q, want %q", got, want)
	}
	if a.state.RoundLimit == nil || *a.state.RoundLimit != 3 || a.state.maxRounds() != 3 {
		t.Errorf("round limit after /rounds 3 = %v", a.state.RoundLimit)
	}
	if got, want := run(a, "/rounds"), "rounds: 3 (set by /rounds); ceiling 3 (preset review)"; got != want {
		t.Errorf("/rounds after /rounds 3 = %q, want %q", got, want)
	}
	if got := run(a, "/rounds 1 2"); got != "usage: /rounds [n]" {
		t.Errorf("/rounds 1 2 = %q", got)
	}

	b := offlineApp(t, WithRounds(1))
	b.state.Status = status
	if got, want := run(b, "/rounds"), "rounds: 1 (set by --rounds); ceiling 3 (preset review)"; got != want {
		t.Errorf("/rounds with --rounds 1 = %q, want %q", got, want)
	}
}

// /rounds n goes out as max_rounds with every later submit.
func TestRoundsIsSentWithLaterSubmits(t *testing.T) {
	r := startConvRig(t, 2)
	runTurn(t, r.driver, "before")
	r.command("/rounds 0", "rounds set to 0")
	runTurn(t, r.driver, "after")
	reqs := r.rec.requests()
	if reqs[0].MaxRounds != nil {
		t.Errorf("first submit max_rounds = %d, want none", *reqs[0].MaxRounds)
	}
	if reqs[1].MaxRounds == nil || *reqs[1].MaxRounds != 0 {
		t.Errorf("submit after /rounds 0 max_rounds = %v, want 0", reqs[1].MaxRounds)
	}
}

func TestBudgetShowsReservedChargedAndLimit(t *testing.T) {
	a := offlineApp(t)
	a.input.SetValue("/budget")
	a.update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.refusal != "no turn yet; no budget to show" {
		t.Errorf("/budget with no turn = %q", a.refusal)
	}

	r := startConvRig(t, 1)
	runTurn(t, r.driver, "q")
	id := answerTurn(r.app.state)
	// Charges go on after the turn ends (terminal delivery), so the line is
	// compared with the record read just after it, until the two agree.
	cl := dial(t, r.paths)
	for deadline := time.Now().Add(20 * time.Second); ; {
		r.command("/budget", "budget of turn "+shortID(id)+": reserved ")
		shown := r.app.refusal
		tr, err := cl.Turn(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if tr.Budget.Limit == nil || tr.Budget.Charged == nil {
			t.Fatalf("turn record budget %+v has no limit or charge", tr.Budget)
		}
		want := fmt.Sprintf("budget of turn %s: reserved %s, charged %d, limit %d tokens", shortID(id), figure(tr.Budget.Reserved), *tr.Budget.Charged, *tr.Budget.Limit)
		if shown == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/budget = %q, want %q", shown, want)
		}
		r.send(tea.KeyMsg{Type: tea.KeyEsc})
		time.Sleep(50 * time.Millisecond)
	}
}

// A figure the record does not carry is shown, not left out.
func TestBudgetLineShowsMissingFigures(t *testing.T) {
	ten := int64(10)
	if got, want := budgetLine("0123456789", wire.BudgetRecord{Limit: &ten}), "budget of turn 01234567: reserved -, charged -, limit 10 tokens"; got != want {
		t.Errorf("budgetLine = %q, want %q", got, want)
	}
}

// /conversations lists this directory's conversations, latest first, with
// the open one marked; /switch opens another by prefix.
func TestConversationsAndSwitch(t *testing.T) {
	r := startConvRig(t, 4)
	runTurn(t, r.driver, "first question")
	first := r.app.conversationID
	other := submitElsewhere(t, r, "/ws/elsewhere", "foreign question")

	r.command("/new", "new conversation; the next submit creates it")
	if got := header(r.driver); !strings.HasPrefix(got, "conversation: new") {
		t.Errorf("header after /new = %q", got)
	}
	if v := r.app.View(); strings.Contains(v, "› first question") {
		t.Errorf("the chat after /new shows the old conversation:\n%s", v)
	}
	runTurn(t, r.driver, "second question")
	second := r.app.conversationID
	if second == first || second == "" {
		t.Fatalf("conversation after /new = %q, first %q", second, first)
	}

	v := r.command("/conversations", "conversations in /ws/here", "first question", "second question", "foreign question")
	lines := strings.Split(v, "\n")
	at := func(s string) int {
		return slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, s) })
	}
	fi, si := at(shortID(first)), at(shortID(second))
	if fi < 0 || si < 0 || si > fi {
		t.Fatalf("want both conversations, the latest first:\n%s", v)
	}
	if !strings.Contains(lines[si], "› * "+shortID(second)) || !strings.Contains(lines[fi], "  "+shortID(first)) || strings.Contains(lines[fi], "*") {
		t.Errorf("open conversation not marked or selected:\n%s\n%s", lines[si], lines[fi])
	}
	for _, l := range []string{lines[si], lines[fi]} {
		if !strings.Contains(l, "1 turns") || !strings.Contains(l, time.Now().Format("2006-01-02")) {
			t.Errorf("line %q lacks the turn count or the last activity", l)
		}
	}
	// Another directory's conversation is listed with its workspace, and
	// the selection skips it.
	if oi := at(shortID(other)); oi < 0 || !strings.Contains(lines[oi], "[/ws/elsewhere]") || strings.Contains(lines[oi], "›") {
		t.Errorf("another directory's conversation is not listed as unselectable:\n%s", v)
	}
	r.send(tea.KeyMsg{Type: tea.KeyDown})
	r.send(tea.KeyMsg{Type: tea.KeyDown})
	r.send(tea.KeyMsg{Type: tea.KeyDown})
	if got := r.app.conv.visible()[r.app.conv.sel].ID; got != first {
		t.Errorf("selection after Down = %q, want the first conversation %q", got, first)
	}
	r.send(tea.KeyMsg{Type: tea.KeyEsc})
	if r.app.conv != nil {
		t.Error("Esc left the list open")
	}

	// Another directory's conversation is refused, and nothing changes.
	r.command("/switch "+other[:8], "belongs to /ws/elsewhere", "not switched")
	if r.app.conversationID != second {
		t.Errorf("conversation after a refused switch = %q, want %q", r.app.conversationID, second)
	}
	r.command("/switch zzzzzzzz", "not_found", "not switched")

	// The first conversation opens by prefix, with its turn and only that.
	r.command("/switch "+first[:6], "switched to conversation "+shortID(first)+", 1 turns", "› first question")
	if got, want := header(r.driver), "conversation "+shortID(first)+", 1 turns · workspace /ws/here"; got != want {
		t.Errorf("header after /switch = %q, want %q", got, want)
	}
	if v := r.app.View(); strings.Contains(v, "› second question") {
		t.Errorf("the chat after /switch shows the other conversation:\n%s", v)
	}
	runTurn(t, r.driver, "third question")
	reqs := r.rec.requests()
	if last := reqs[len(reqs)-1]; last.ConversationID == nil || *last.ConversationID != first {
		t.Errorf("submit after /switch conversation_id = %v, want %q", last.ConversationID, first)
	}
}

// /new and /switch while a turn runs change only what the chat shows and
// where the next submit goes: the turn keeps running and is never cancelled,
// a submit meanwhile is refused as busy naming the turn's conversation, and
// detaching keys leave it alone too.
func TestNewAndSwitchLeaveARunningTurnAlone(t *testing.T) {
	r := startConvRig(t, 2)
	release := r.h.Hold("author")
	r.typeLine("long question")
	r.until("the turn to open", func(string) bool { return r.app.state.CurrentTurn != "" && r.app.conversationID != "" })
	turn, conv := r.app.state.CurrentTurn, r.app.conversationID

	r.command("/new", "new conversation; the next submit creates it; turn "+shortID(turn)+" keeps running in")
	if line := strings.Join(r.app.panel, " "); !strings.HasSuffix(line, "keeps running in conversation "+shortID(conv)) {
		t.Errorf("/new says %q, want it to name the running turn's conversation", line)
	}
	if v := r.app.View(); strings.Contains(v, "› long question") {
		t.Errorf("the chat after /new shows the running turn's conversation:\n%s", v)
	}
	if r.app.state.CurrentTurn != turn || strings.Contains(r.strip(), "idle") {
		t.Errorf("status line after /new = %q, want the running turn", r.strip())
	}

	r.typeLine("meanwhile")
	r.until("the busy refusal", func(string) bool {
		v := strings.Join(r.app.panel, " ")
		return strings.Contains(v, "busy") && strings.Contains(v, "the open turn "+shortID(turn)+" belongs to conversation "+shortID(conv))
	})
	if got := r.app.input.Value(); got != "meanwhile" {
		t.Errorf("input after a busy refusal = %q, want it kept", got)
	}
	r.send(tea.KeyMsg{Type: tea.KeyCtrlC}) // clears the input; nothing is cancelled
	if r.app.input.Value() != "" {
		t.Errorf("ctrl+c did not clear the input")
	}

	r.command("/switch "+conv[:8], "switched to conversation "+shortID(conv), "› long question")
	r.command("/new", "new conversation")
	if line := strings.Join(r.app.panel, " "); !strings.HasSuffix(line, "keeps running in conversation "+shortID(conv)) {
		t.Errorf("second /new says %q", line)
	}

	if st, _ := dial(t, r.paths).Turn(context.Background(), turn); isTerminalState(st.State) {
		t.Fatalf("the turn ended while switching: %s", st.State)
	}
	release()
	r.h.WaitTurnState(turn, engine.StateApproved)
	if states := r.h.TurnStates(turn); slices.Contains(states, "cancelled") || slices.Contains(states, "cancel_requested") {
		t.Errorf("the turn was cancelled: %v", states)
	}
	r.until("the turn to end", func(string) bool { return r.app.state.CurrentTurn == "" })

	// The next submit starts the new conversation, not the old one.
	runTurn(t, r.driver, "fresh question")
	reqs := r.rec.requests()
	if last := reqs[len(reqs)-1]; last.ConversationID != nil {
		t.Errorf("submit after /new conversation_id = %q, want none", *last.ConversationID)
	}
	if r.app.conversationID == conv {
		t.Errorf("the submit after /new went to the old conversation")
	}
}

// /trim shows what it drops and what stays before it sets anything; the
// history of the next submit is exactly what it said stays.
func TestTrimPreviewMatchesIncludedHistory(t *testing.T) {
	r := startConvRig(t, 4)
	for _, q := range []string{"alpha question", "bravo question", "charlie question"} {
		runTurn(t, r.driver, q)
	}
	order := append([]string(nil), r.app.state.Scope.Order...)
	if len(order) != 3 {
		t.Fatalf("scope order %v, want three turns", order)
	}

	r.command("/trim zzzz", `trim: no turn "zzzz" in this conversation`)

	// Declined, nothing changes.
	r.command("/trim "+order[1][:8], "Trim? (y/n)")
	r.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if r.app.state.ContextFrom != "" || r.app.refusal != "trim cancelled; the history is unchanged" {
		t.Fatalf("declined trim: context from %q, refusal %q", r.app.state.ContextFrom, r.app.refusal)
	}

	v := r.command("/trim "+order[1][:8], "Trim? (y/n)")
	lines := strings.Split(v, "\n")
	section := func(title, next string) []string {
		start := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, title) })
		end := slices.IndexFunc(lines, func(l string) bool { return next != "" && strings.HasPrefix(l, next) })
		if start < 0 || (next != "" && end < start) {
			t.Fatalf("no %q section in the preview:\n%s", title, v)
		}
		if next == "" {
			end = slices.IndexFunc(lines[start+1:], func(l string) bool { return !strings.HasPrefix(l, "  ") }) + start + 1
		}
		return lines[start+1 : end]
	}
	dropped, kept := section("dropped (not in context):", "kept in the history:"), section("kept in the history:", "")
	if want := []string{"  " + shortID(order[0]) + "  you: alpha question"}; !slices.Equal(dropped, want) {
		t.Errorf("dropped = %q, want %q", dropped, want)
	}
	wantKept := []string{"  " + shortID(order[1]) + "  you: bravo question", "  " + shortID(order[2]) + "  you: charlie question"}
	if !slices.Equal(kept, wantKept) {
		t.Errorf("kept = %q, want %q", kept, wantKept)
	}
	if !strings.Contains(v, "later history drops 1 turns, keeps 2. Trim? (y/n)") {
		t.Errorf("preview summary missing:\n%s", v)
	}

	r.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if r.app.state.ContextFrom != order[1] {
		t.Fatalf("context from = %q, want %q", r.app.state.ContextFrom, order[1])
	}
	// The dropped turn is marked in the chat, once.
	full, _ := chatLines(r.app.state, r.app.chatIn())
	if n := strings.Count(strings.Join(full, "\n"), notInContext); n != 1 {
		t.Errorf("%d turns marked %q, want 1", n, notInContext)
	}

	before := len(authorCalls(&rig{h: r.h}))
	runTurn(t, r.driver, "delta question")
	reqs := r.rec.requests()
	if last := reqs[len(reqs)-1]; last.ContextFromTurn == nil || *last.ContextFromTurn != order[1] {
		t.Errorf("context_from_turn = %v, want %q", last.ContextFromTurn, order[1])
	}
	calls := authorCalls(&rig{h: r.h})[before:]
	if len(calls) != 1 {
		t.Fatalf("%d author calls for the trimmed turn, want 1", len(calls))
	}
	var sent []string
	for _, m := range calls[0].Request.Messages {
		if m.Role == "user" {
			sent = append(sent, m.Content)
		}
	}
	history := strings.Join(sent, "\n")
	for _, kept := range []string{"bravo question", "charlie question", "delta question"} {
		if !strings.Contains(history, kept) {
			t.Errorf("the author's history lacks the kept %q", kept)
		}
	}
	if strings.Contains(history, "alpha question") {
		t.Errorf("the author's history includes the dropped turn")
	}
}

func TestResolveTurn(t *testing.T) {
	ids := []string{"abc111", "abc222", "def333"}
	for _, c := range []struct{ prefix, want, err string }{
		{"abc111", "abc111", ""},
		{"def", "def333", ""},
		{"abc", "", `turn prefix "abc" is ambiguous: 2 turns match`},
		{"x", "", `no turn "x" in this conversation`},
	} {
		got, err := resolveTurn(ids, c.prefix)
		if got != c.want || (err == nil) != (c.err == "") || (err != nil && err.Error() != c.err) {
			t.Errorf("resolveTurn(%q) = %q, %v; want %q, %q", c.prefix, got, err, c.want, c.err)
		}
	}
}

func TestTrimWithoutTurns(t *testing.T) {
	a := offlineApp(t, WithConversation("", nil))
	a.input.SetValue("/trim abc")
	a.update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.refusal != "this conversation has no turns; nothing to trim" {
		t.Errorf("/trim with no turns = %q", a.refusal)
	}
}

// The conversation list cuts a long preview to the width by cells, never
// splitting a character.
func TestConversationLinesFitTheWidth(t *testing.T) {
	a := chatApp(t)
	a.workDir = "/w"
	ws := "/w"
	a.conv = &convChooser{loaded: true, items: []wire.ConversationSummary{{ID: "0123456789abcdef", Workspace: &ws, LastSubmittedAt: "", TurnCount: 3, Running: true, Preview: strings.Repeat("日本語", 30)}}}
	a.resetSel()
	lines := a.conversationLines(10, 40)
	l := lines[1]
	if !strings.HasPrefix(l, "› ") || !strings.Contains(l, "01234567  -  3 turns, running  日本") || !strings.HasSuffix(l, "…") {
		t.Errorf("line = %q", l)
	}
	if w := ansi.StringWidth(l); w > 40 {
		t.Errorf("line %q is %d cells", l, w)
	}
	if !strings.Contains(l, "日") || strings.ContainsRune(l, '�') {
		t.Errorf("line %q split a character", l)
	}
}

// A deliberate turn's budget lists each phase of its projection.
func TestBudgetLineListsPhases(t *testing.T) {
	b := wire.BudgetRecord{Phases: []wire.PhaseEstimate{{Phase: "integration", Participant: "author", EstimatedTokens: 120, MaxOutputTokens: 30}}}
	got := budgetLine("0123456789", b)
	if want := "\n  integration author: ~120 tokens in, up to 30 out"; !strings.HasSuffix(got, want) {
		t.Errorf("budgetLine = %q, want suffix %q", got, want)
	}
}
