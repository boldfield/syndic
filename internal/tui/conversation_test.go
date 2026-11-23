package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// startRigWith is startRig with options for the App.
func startRigWith(t *testing.T, opts ...Option) (*driver, *engineRig) {
	t.Helper()
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(answerText), enginetest.Reply(answerText), enginetest.Reply(answerText)},
		"reviewer": {approve(), approve(), approve()},
	})
	paths := newPaths(t)
	startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rec := &engineRig{}
	opts = append(opts, WithSubmitObserver(rec.record))
	d := &driver{t: t, app: NewApp(ctx, paths.Socket, opts...), msgs: make(chan tea.Msg, 1024)}
	d.app.Update(resizeMsg(80, 24))
	d.run(d.app.Init())
	d.until("connected", func(string) bool { return d.app.state.ConnState == ConnStateConnected && d.strip() != "" })
	return d, rec
}

// engineRig records the submit requests an App sent.
type engineRig struct {
	mu   sync.Mutex
	reqs []wire.SubmitRequest
}

func (r *engineRig) record(req wire.SubmitRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
}

func (r *engineRig) requests() []wire.SubmitRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wire.SubmitRequest(nil), r.reqs...)
}

// header is the first line of the chat tab, the line below the strip.
func header(d *driver) string {
	lines := strings.Split(d.app.View(), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "[") && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	return ""
}

func TestChatHeaderNamesTheOpenConversation(t *testing.T) {
	d, _ := startRigWith(t, WithConversation("0123456789abcdef", []string{"t1", "t2", "t3"}), WithWorkDir("/ws/x"))
	// Header now includes scroll position: "conversation ... 0 earlier · 0%"
	if got, want := header(d), "conversation 01234567, 3 turns · workspace /ws/x 0 earlier · 0%"; got != want {
		t.Fatalf("first line of the chat = %q, want %q\n%s", got, want, d.app.View())
	}
}

func TestChatHeaderOfAnEmptyConversation(t *testing.T) {
	d, _ := startRigWith(t)
	if got := header(d); !strings.HasPrefix(got, "conversation: new") {
		t.Fatalf("first line of the chat = %q, want the new-conversation header", got)
	}
}

// The first submit of a new conversation names no conversation; the reply's
// id is carried into every later submit, and --rounds 0 is sent with each.
func TestSubmitCarriesConversationAndRounds(t *testing.T) {
	d, rec := startRigWith(t, WithRounds(0), WithWorkDir(t.TempDir()))
	runTurn(t, d, "first")
	if n := len(rec.requests()); n != 1 {
		t.Fatalf("%d submits, want 1", n)
	}
	r1 := rec.requests()[0]
	if r1.ConversationID != nil {
		t.Errorf("first submit conversation_id = %q, want none", *r1.ConversationID)
	}
	if d.app.conversationID == "" {
		t.Fatal("the reply's conversation id was not kept")
	}
	if got := header(d); !strings.Contains(got, shortID(d.app.conversationID)) || !strings.Contains(got, "1 turns") {
		t.Errorf("header after the first turn = %q", got)
	}
	runTurn(t, d, "second")
	r2 := rec.requests()[1]
	if r2.ConversationID == nil || *r2.ConversationID != d.app.conversationID {
		t.Errorf("second submit conversation_id = %v, want %q", r2.ConversationID, d.app.conversationID)
	}
	for i, r := range []wire.SubmitRequest{r1, r2} {
		if r.MaxRounds == nil || *r.MaxRounds != 0 {
			t.Errorf("submit %d max_rounds = %v, want 0", i+1, r.MaxRounds)
		}
		if r.Workspace != d.app.workDir || r.Workspace == "" {
			t.Errorf("submit %d workspace = %q, want %q", i+1, r.Workspace, d.app.workDir)
		}
	}
}

func TestSubmitWithoutRoundsSendsNone(t *testing.T) {
	d, rec := startRigWith(t)
	runTurn(t, d, "q")
	if r := rec.requests()[0]; r.MaxRounds != nil {
		t.Errorf("max_rounds = %d, want none when --rounds is absent", *r.MaxRounds)
	}
}

// An opened conversation's id goes out on the first submit.
func TestSubmitToOpenedConversation(t *testing.T) {
	d, rec := startRigWith(t, WithConversation("not-in-journal", []string{"t1", "t2"}))
	d.typeLine("q")
	d.until("the request", func(string) bool { return len(rec.requests()) == 1 })
	if r := rec.requests()[0]; r.ConversationID == nil || *r.ConversationID != "not-in-journal" {
		t.Errorf("conversation_id = %v, want the opened one", r.ConversationID)
	}
}

// runTurn submits input and waits until one more turn is approved.
func runTurn(t *testing.T, d *driver, input string) {
	t.Helper()
	approved := func() (n int) {
		for _, st := range d.app.state.TurnState {
			if st == engine.StateApproved {
				n++
			}
		}
		return n
	}
	before := approved()
	d.typeLine(input)
	d.until("the turn to be approved", func(string) bool { return approved() > before })
}

// Two workspaces hold one conversation each on the same engine. An app started
// on the first shows that conversation's earlier turn, under its header, and
// nothing of the second.
func TestStartupShowsTheSelectedConversationOnly(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("ALPHA-ANSWER"), enginetest.Reply("BRAVO-ANSWER")},
		"reviewer": {approve(), approve()},
	})
	paths := newPaths(t)
	startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	start := func(opts ...Option) *driver {
		d := &driver{t: t, app: NewApp(ctx, paths.Socket, opts...), msgs: make(chan tea.Msg, 1024)}
		d.app.Update(resizeMsg(80, 24))
		d.run(d.app.Init())
		d.until("connected", func(string) bool { return d.app.state.ConnState == ConnStateConnected && d.strip() != "" })
		return d
	}
	a := start(WithWorkDir("/ws/a"), WithConversation("", nil))
	runTurn(t, a, "alpha question")
	b := start(WithWorkDir("/ws/b"), WithConversation("", nil))
	runTurn(t, b, "bravo question")
	if a.app.conversationID == "" || a.app.conversationID == b.app.conversationID {
		t.Fatalf("conversations a=%q b=%q, want two different ones", a.app.conversationID, b.app.conversationID)
	}
	var turns []string
	for id := range a.app.state.Scope.Turns {
		turns = append(turns, id)
	}

	re := start(WithWorkDir("/ws/a"), WithConversation(a.app.conversationID, turns))
	re.until("the earlier answer and input", func(v string) bool {
		return strings.Contains(v, "ALPHA-ANSWER") && strings.Contains(v, "› alpha question")
	})
	v := re.app.View()
	if strings.Contains(v, "BRAVO-ANSWER") {
		t.Errorf("the other conversation's answer is shown:\n%s", v)
	}
	// Header now includes scroll position: "conversation ... 0 earlier · 0%"
	if got, want := header(re), "conversation "+shortID(a.app.conversationID)+", 1 turns · workspace /ws/a 0 earlier · 0%"; got != want {
		t.Errorf("header = %q, want %q", got, want)
	}

	// An empty conversation started beside them shows neither answer.
	fresh := start(WithWorkDir("/ws/c"), WithConversation("", nil))
	if v := fresh.app.View(); strings.Contains(v, "ALPHA-ANSWER") || strings.Contains(v, "BRAVO-ANSWER") {
		t.Errorf("an empty conversation shows other turns:\n%s", v)
	}
}

// A turn submitted with context_from_turn leaves the turns before it out of
// context. An app that opens the conversation afterwards learns the trim point
// from the turn records and marks only those turns.
func TestReopenedTrimmedConversationMarksTurnsNotInContext(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(answerText), enginetest.Reply(answerText), enginetest.Reply(answerText), enginetest.Reply(answerText), enginetest.Reply(answerText), enginetest.Reply(answerText)},
		"reviewer": {approve(), approve(), approve(), approve(), approve(), approve()},
	})
	paths := newPaths(t)
	startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	start := func(opts ...Option) *driver {
		d := &driver{t: t, app: NewApp(ctx, paths.Socket, opts...), msgs: make(chan tea.Msg, 1024)}
		d.app.Update(resizeMsg(80, 24))
		d.run(d.app.Init())
		d.until("connected", func(string) bool { return d.app.state.ConnState == ConnStateConnected && d.strip() != "" })
		return d
	}
	a := start(WithWorkDir("/ws/a"), WithConversation("", nil))
	runTurn(t, a, "first question")
	runTurn(t, a, "second question")
	conv := a.app.conversationID
	order := append([]string(nil), a.app.state.Scope.Order...)
	if len(order) != 2 {
		t.Fatalf("scope order %v, want two turns", order)
	}

	// Reopened untrimmed, every turn is in context.
	plain := start(WithWorkDir("/ws/a"), WithConversation(conv, order))
	plain.until("both turns approved", func(v string) bool {
		return strings.Count(v, "approved by") == 2 && strings.Contains(v, "› second question")
	})
	if v := plain.app.View(); strings.Contains(v, notInContext) {
		t.Fatalf("an untrimmed conversation shows %q:\n%s", notInContext, v)
	}

	// The first turn has no history; the second's starts at the first.
	cl := dial(t, paths)
	for i, want := range []string{"", order[0]} {
		turn, err := cl.Turn(ctx, order[i])
		if err != nil {
			t.Fatal(err)
		}
		if got := turn.ContextFromTurn; (want == "") != (got == nil) || (got != nil && *got != want) {
			t.Errorf("turn %d context_from_turn = %v, want %q", i+1, got, want)
		}
	}

	// Another client submits the third turn from the second onward.
	reply, err := cl.Submit(ctx, wire.SubmitRequest{SubmissionID: "trimmed-third", Input: "third question", Workspace: "/ws/a", ConversationID: &conv, ContextFromTurn: &order[1]})
	if err != nil {
		t.Fatalf("trimmed submit: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		turn, err := cl.Turn(ctx, reply.TurnID)
		if err != nil {
			t.Fatal(err)
		}
		if turn.State == engine.StateApproved {
			if turn.ContextFromTurn == nil || *turn.ContextFromTurn != order[1] {
				t.Fatalf("turn record context_from_turn = %v, want %q", turn.ContextFromTurn, order[1])
			}
			break
		}
		if turn.State == "incomplete" || turn.State == "cancelled" || time.Now().After(deadline) {
			t.Fatalf("trimmed turn never approved; state %q, reason %v", turn.State, turn.TerminalReason)
		}
		time.Sleep(20 * time.Millisecond)
	}

	re := start(WithWorkDir("/ws/a"), WithConversation(conv, append(order, reply.TurnID)))
	re.until("the trimmed turn marked", func(v string) bool {
		return strings.Count(v, "approved by") == 3 && strings.Contains(v, notInContext) &&
			strings.Contains(v, "› first question") && strings.Contains(v, "› second question") && strings.Contains(v, "› third question")
	})
	lines := strings.Split(re.app.View(), "\n")
	first, second, marker := -1, -1, -1
	marked := 0
	for i, l := range lines {
		switch {
		case strings.Contains(l, "› first question"):
			first = i
		case strings.Contains(l, "› second question"):
			second = i
		case strings.Contains(l, notInContext):
			marker = i
			marked++
		}
	}
	if marked != 1 || first < 0 || second < 0 || marker < first || marker > second {
		t.Errorf("marker line %d (%d marked), want one between the first turn (%d) and the second (%d):\n%s", marker, marked, first, second, strings.Join(lines, "\n"))
	}
}

// A trim point the app holds goes out as context_from_turn with the next submit.
func TestSubmitCarriesTheTrimPoint(t *testing.T) {
	d, rec := startRigWith(t, WithWorkDir(t.TempDir()), WithConversation("", nil))
	runTurn(t, d, "first")
	if r := rec.requests()[0]; r.ContextFromTurn != nil {
		t.Errorf("untrimmed submit context_from_turn = %q, want none", *r.ContextFromTurn)
	}
	first := d.app.state.Scope.Order[0]
	d.app.state.ContextFrom = first
	runTurn(t, d, "second")
	if r := rec.requests()[1]; r.ContextFromTurn == nil || *r.ContextFromTurn != first {
		t.Errorf("trimmed submit context_from_turn = %v, want %q", r.ContextFromTurn, first)
	}
}
