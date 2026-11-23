package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

var chatNow = time.Date(2026, 11, 22, 12, 0, 30, 0, time.UTC)

// chatApp is an App holding a State only, laid out at 80x24 with the clock fixed.
func chatApp(t *testing.T) *App {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := NewApp(ctx, "")
	a.now = func() time.Time { return chatNow }
	a.width, a.height = 80, 24
	a.state.ConnState = ConnStateConnected
	a.state.Status = &wire.StatusReply{DefaultPreset: "review", Revision: wire.Revision{Default: 2, Ceiling: 5}}
	return a
}

// addTurn puts a finished turn into the chat; its candidate text is loaded.
func addTurn(a *App, id string, seq int64, state, input, candidate string) {
	ti := TurnInfo{ID: id, State: state, Preset: "review", SubmitSeq: seq, SubmittedAt: chatNow.Add(-time.Duration(100-seq) * time.Minute)}
	if candidate != "" {
		hash := "abcdef1234567890" + id
		ti.CandidateHash = &hash
		a.candidateIDByTurn[id] = "cand-" + id
		a.content.Store("candidate:cand-"+id, candidate, nil)
	}
	a.state.Turns[id] = ti
	a.state.TurnState[id] = state
	if input != "" {
		a.inputs[id] = input
	}
}

// approveBy records a valid verdict from reviewer on the turn.
func approveBy(a *App, turn, reviewer string) {
	att := "att-" + turn + reviewer
	a.state.Attempts[att] = AttemptInfo{ID: att, TurnID: turn, Participant: reviewer, Phase: "review", Status: "complete"}
	a.state.Verdicts["v-"+att] = VerdictInfo{ID: "v-" + att, AttemptID: att, Class: "valid", Parsed: true}
}

func chatView(a *App) []string { return strings.Split(a.View(), "\n") }

func lineOf(t *testing.T, lines []string, sub string) int {
	t.Helper()
	for i, l := range lines {
		if strings.Contains(l, sub) {
			return i
		}
	}
	t.Fatalf("no line contains %q:\n%s", sub, strings.Join(lines, "\n"))
	return -1
}

func chatFrame(t *testing.T, seq int64, kind string, body any) wire.Frame {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return wire.Frame{Type: "event", Seq: seq, Kind: kind, At: chatNow.Add(-time.Minute).Format(time.RFC3339), Body: raw}
}

func TestChatShowsEachTurnsInputThenOutcome(t *testing.T) {
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "first question", "first answer")
	approveBy(a, "t1", "rev")
	addTurn(a, "t2", 2, "solo_complete", "second question", "")
	a.state.Attempts["a2"] = AttemptInfo{ID: "a2", TurnID: "t2", Phase: "author", Status: "complete"}
	a.content.Store("attempt:a2", "second answer", nil)
	addTurn(a, "t3", 3, "overridden", "third question", "third answer")
	a.overrideReasons["t3"] = "good enough"

	lines := chatView(a)
	order := []string{
		"you: first question", "first answer", "✓ approved by rev against abcdef123456",
		"you: second question", "· UNREVIEWED (solo)", "second answer",
		"you: third question", "! HUMAN OVERRIDE: good enough", "third answer",
	}
	prev := -1
	for _, want := range order {
		i := lineOf(t, lines, want)
		if i <= prev {
			t.Errorf("%q at line %d, want after line %d", want, i, prev)
		}
		prev = i
	}
	if !strings.Contains(lines[1], "conversation") {
		t.Errorf("first line of the chat = %q, want the conversation header", lines[1])
	}
}

func TestChatApprovedByRevisionShowsTheCount(t *testing.T) {
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q", "answer")
	approveBy(a, "t1", "rev")
	ti := a.state.Turns["t1"]
	ti.Round = 2
	a.state.Turns["t1"] = ti
	lineOf(t, chatView(a), "✓ approved by rev after 2 revisions against abcdef123456")

	// No revision: the original wording.
	addTurn(a, "t2", 2, "approved", "q2", "answer2")
	approveBy(a, "t2", "rev")
	lineOf(t, chatView(a), "✓ approved by rev against abcdef123456t2"[:len("✓ approved by rev against abcdef123456")])
}

func TestChatRoundCountComesFromRevisionEvents(t *testing.T) {
	a := chatApp(t)
	s := a.state
	seq := int64(0)
	next := func(kind string, body any) {
		seq++
		s = apply(s, chatFrame(t, seq, kind, body))
	}
	next(wire.EventKindTurnSubmitted, wire.EvTurnSubmitted{TurnID: "t1", SubmissionID: "s1", Preset: "review", ConversationID: "c"})
	next(wire.EventKindTurnState, wire.EvTurnState{TurnID: "t1", State: "reviewers_dispatched"})
	if ti := s.Turns["t1"]; ti.Round != 0 || ti.MaxRounds != 2 {
		t.Fatalf("before a revision: round %d of %d, want 0 of 2", ti.Round, ti.MaxRounds)
	}
	next(wire.EventKindTurnState, wire.EvTurnState{TurnID: "t1", State: "revision_dispatched"})
	next(wire.EventKindTurnState, wire.EvTurnState{TurnID: "t1", State: "revision_dispatched"})
	if ti := s.Turns["t1"]; ti.Round != 2 || ti.MaxRounds != 2 {
		t.Fatalf("after two revisions: round %d of %d, want 2 of 2", ti.Round, ti.MaxRounds)
	}
	if strip := renderStrip(s, 200, chatNow); !strings.Contains(strip, "[round 2/2]") {
		t.Errorf("strip = %q, want [round 2/2]", strip)
	}
	// A limit this process set wins over the roster default.
	limit := 4
	s2 := NewState()
	s2.RoundLimit = &limit
	s2 = apply(s2, chatFrame(t, 1, wire.EventKindTurnSubmitted, wire.EvTurnSubmitted{TurnID: "t9", SubmissionID: "s9"}))
	if got := s2.Turns["t9"].MaxRounds; got != 4 {
		t.Errorf("MaxRounds = %d, want the 4 the process sent", got)
	}
}

func TestChatMissingAnswerAndTrimmedTurnsAreDistinct(t *testing.T) {
	t.Setenv("TERM", "dumb")
	a := chatApp(t)
	addTurn(a, "t1", 1, "incomplete", "old question", "old draft")
	addTurn(a, "t2", 2, "cancelled", "cancelled question", "partial")
	addTurn(a, "t3", 3, "cancelled", "early cancel", "")
	addTurn(a, "t4", 4, "approved", "newest", "newest answer")
	approveBy(a, "t4", "rev")
	a.state.ContextFrom = "t2"

	lines := chatView(a)
	// The outcome banner comes first, the line later turns see below it.
	if banner, miss := lineOf(t, lines, "INCOMPLETE, not approved"), lineOf(t, lines, "no approved answer (incomplete)"); miss != banner+1 {
		t.Errorf("incomplete: banner line %d, missing-answer line %d", banner, miss)
	}
	if banner, miss := lineOf(t, lines, "CANCELLED, not approved"), lineOf(t, lines, "no approved answer (cancelled)"); miss != banner+1 {
		t.Errorf("cancelled: banner line %d, missing-answer line %d", banner, miss)
	}
	lineOf(t, lines, "cancelled before candidate")

	// Only the turn before the trim point is not in context, and it is not the missing-answer line that says so.
	marked := 0
	for _, l := range lines {
		if strings.Contains(l, notInContext) {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("%d lines say %q, want 1:\n%s", marked, notInContext, strings.Join(lines, "\n"))
	}
	if m, in, other := lineOf(t, lines, notInContext), lineOf(t, lines, "you: old question"), lineOf(t, lines, "you: cancelled question"); m < in || m > other {
		t.Errorf("marker at line %d, want between the first turn (%d) and the second (%d)", m, in, other)
	}
}

func TestChatRunningTurnStaysIdentifiedAcrossViews(t *testing.T) {
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "earlier", "earlier answer")
	approveBy(a, "t1", "rev")
	addTurn(a, "t2", 2, "reviewers_dispatched", "running question", "")
	ti := a.state.Turns["t2"]
	ti.SubmittedAt = chatNow.Add(-30 * time.Second)
	ti.Round, ti.MaxRounds = 1, 2
	ti.Author, ti.AuthorState = "writer", "idle"
	ti.Reviewers = []string{"rev"}
	ti.ReviewerStates = map[string]string{"rev": "running"}
	a.state.Turns["t2"] = ti
	a.state.CurrentTurn = "t2"

	const progress = "turn in progress: reviewers_dispatched, round 1/2, 30s, writer idle, rev running"
	lines := chatView(a)
	ti.EstimatedTokens = 24260
	a.state.Turns["t2"] = ti
	if last := chatView(a)[23]; !strings.HasSuffix(last, keyHint) || !strings.Contains(last, "[reviewers_dispatched] [round 1/2] [30s]") || !strings.Contains(last, "author:writer idle") {
		t.Errorf("status line = %q, want the turn state, round, elapsed time, author state and the key hint", last)
	}
	lineOf(t, lines, "you: running question")
	lineOf(t, lines, progress)

	for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyTab} {
		a.Update(tea.KeyMsg{Type: key})
		v := a.View()
		if !strings.Contains(v, "[reviewers_dispatched]") || !strings.Contains(v, "round 1/2") {
			t.Errorf("status line lost the running turn on tab %d:\n%s", a.tab, v)
		}
	}
	a.Update(tea.KeyMsg{Type: tea.KeyTab})
	lineOf(t, chatView(a), progress)
}

func TestChatStatusLineIsLastAt80x24(t *testing.T) {
	a := chatApp(t)
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("t%02d", i)
		addTurn(a, id, int64(i), "approved", "question "+id, "answer "+id)
		approveBy(a, id, "rev")
	}
	a.state.Status.DeadlinesProvisional = true
	a.state.Status.RosterFileChanged = true
	a.state.ConnState = ConnStateReconnecting
	profile := "fast"
	a.state.UnapprovedProfile = &profile

	for _, tab := range []tab{tabChat, tabTranscript, tabParticipants} {
		a.tab = tab
		lines := chatView(a)
		if len(lines) != 24 {
			t.Fatalf("tab %d: %d lines, want 24", tab, len(lines))
		}
		last := lines[23]
		if !strings.HasPrefix(last, "[review]") || !strings.HasSuffix(last, keyHint) {
			t.Errorf("tab %d: last line = %q, want the status line ending %q", tab, last, keyHint)
		}
		if !strings.HasPrefix(lines[22], "> ") {
			t.Errorf("tab %d: line above the status line = %q, want the input", tab, lines[22])
		}
		if w := len([]rune(last)); w > 80 {
			t.Errorf("tab %d: status line is %d cells", tab, w)
		}
		if !strings.Contains(lines[0], "[") || !strings.Contains(lines[0], "chat") {
			t.Errorf("tab %d: tab bar = %q", tab, lines[0])
		}
	}
}

func TestChatScrollsBackThroughOlderTurns(t *testing.T) {
	a := chatApp(t)
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("t%02d", i)
		addTurn(a, id, int64(i), "approved", "question "+id, "answer "+id)
		approveBy(a, id, "rev")
	}
	v := a.View()
	if !strings.Contains(v, "you: question t12") || strings.Contains(v, "you: question t01") {
		t.Fatalf("a new chat should follow its newest turn:\n%s", v)
	}
	for i := 0; i < 20; i++ {
		a.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	}
	v = a.View()
	if !strings.Contains(v, "you: question t01") || strings.Contains(v, "you: question t12") {
		t.Fatalf("PgUp did not reach the oldest turn:\n%s", v)
	}
	for i := 0; i < 20; i++ {
		a.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if v = a.View(); !strings.Contains(v, "you: question t12") {
		t.Fatalf("PgDn did not return to the newest turn:\n%s", v)
	}
}

func TestChatRefsCoverEveryTurn(t *testing.T) {
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "q1", "a1")
	addTurn(a, "t2", 2, "incomplete", "q2", "a2")
	refs := chatRefs(a.state, a.chatIn())
	want := map[string]bool{"candidate:cand-t1": true, "candidate:cand-t2": true}
	if len(refs) != len(want) {
		t.Fatalf("refs = %v, want %v", refs, want)
	}
	for _, r := range refs {
		if !want[r] {
			t.Errorf("unexpected ref %q", r)
		}
	}
}

func TestChatOnlyShowsTurnsOfTheScope(t *testing.T) {
	a := chatApp(t)
	a.state.Scope = &TurnScope{ConversationID: "c", Turns: map[string]bool{}, Submissions: map[string]bool{}}
	a.state.Scope.add("t2")
	a.state.Scope.add("t1")
	addTurn(a, "t1", 1, "approved", "mine one", "a1")
	addTurn(a, "t2", 2, "approved", "mine two", "a2")
	addTurn(a, "t3", 3, "approved", "someone else's", "a3")
	approveBy(a, "t1", "rev")
	chat := renderChat(a.state, a.chatIn())
	if strings.Contains(chat, "someone else's") {
		t.Errorf("a turn outside the conversation is shown:\n%s", chat)
	}
	// The scope's order, not submission order, orders the chat.
	if strings.Index(chat, "mine two") > strings.Index(chat, "mine one") {
		t.Errorf("scope order not used:\n%s", chat)
	}
}

func TestChatHeaderNamesTheWorkspace(t *testing.T) {
	a := chatApp(t)
	a.workDir = "/ws/alpha"
	lineOf(t, chatView(a), "conversation: new (created at the first submit) · workspace /ws/alpha")
	a.conversationID, a.convTurns = "0123456789abcdef", 3
	lineOf(t, chatView(a), "conversation 01234567, 3 turns · workspace /ws/alpha")
}

func TestChatRecordLoadedTurnShowsRevisionsAndInput(t *testing.T) {
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "", "the answer")
	approveBy(a, "t1", "rev")
	two := 2
	ref := "msg:m1"
	a.state = MergeTurn(a.state, "t1", &wire.TurnReply{State: "approved", Mode: "review", Preset: "review", Round: 2, MaxRounds: &two, InputRef: &ref, CandidateHash: a.state.Turns["t1"].CandidateHash}, chatNow)
	if got := a.state.Turns["t1"]; got.Round != 2 || got.MaxRounds != 2 || got.InputRef != ref {
		t.Fatalf("turn after merge = %+v, want round 2 of 2 and the input ref", got)
	}
	lineOf(t, chatView(a), "you: loading…")
	refs := chatRefs(a.state, a.chatIn())
	found := false
	for _, r := range refs {
		found = found || r == ref
	}
	if !found {
		t.Errorf("refs = %v, want the input ref among them", refs)
	}
	a.content.Store(ref, "what was typed", nil)
	lines := chatView(a)
	lineOf(t, lines, "you: what was typed")
	lineOf(t, lines, "approved by rev after 2 revisions against abcdef123456")
}

func TestMergeTurnSoloHasNoRoundLimit(t *testing.T) {
	s := NewState()
	s.Turns["t1"] = TurnInfo{ID: "t1", MaxRounds: 2}
	three := 3
	s = MergeTurn(s, "t1", &wire.TurnReply{State: "author_dispatched", Mode: "solo", MaxRounds: &three}, chatNow)
	if got := s.Turns["t1"].MaxRounds; got != 0 {
		t.Errorf("solo MaxRounds = %d, want 0", got)
	}
	s = MergeTurn(s, "t1", &wire.TurnReply{State: "author_dispatched", Mode: "review", MaxRounds: &three}, chatNow)
	if got := s.Turns["t1"].MaxRounds; got != 3 {
		t.Errorf("review MaxRounds = %d, want the turn's own 3", got)
	}
}

// The newest turn record's trim point decides what is in context, and a trim
// point the app holds for its next submit overrides it.
func TestChatTrimPointComesFromTheNewestTurnRecord(t *testing.T) {
	t.Setenv("TERM", "dumb")
	a := chatApp(t)
	addTurn(a, "t1", 1, "approved", "one", "answer one")
	addTurn(a, "t2", 2, "approved", "two", "answer two")
	addTurn(a, "t3", 3, "approved", "three", "answer three")
	for _, id := range []string{"t1", "t2", "t3"} {
		approveBy(a, id, "rev")
	}
	merge := func(id, from string) {
		a.state = MergeTurn(a.state, id, &wire.TurnReply{State: "approved", Mode: "review", Preset: "review", ContextFromTurn: &from, CandidateHash: a.state.Turns[id].CandidateHash}, chatNow)
	}
	marked := func() (n int) {
		for _, l := range chatView(a) {
			if strings.Contains(l, notInContext) {
				n++
			}
		}
		return n
	}

	merge("t3", "t2")
	if n := marked(); n != 1 {
		t.Errorf("trimmed from t2: %d turns marked, want 1", n)
	}
	if lineOf(t, chatView(a), notInContext) > lineOf(t, chatView(a), "you: two") {
		t.Error("the marker is not on the first turn")
	}

	// An older turn's trim does not outlive a newer untrimmed turn.
	merge("t2", "t1")
	merge("t3", "t1")
	if n := marked(); n != 0 {
		t.Errorf("untrimmed newest turn: %d turns marked, want 0", n)
	}

	a.state.ContextFrom = "t3"
	if n := marked(); n != 2 {
		t.Errorf("held trim from t3: %d turns marked, want 2", n)
	}
}

// A turn record fetched at startup can land between replayed events. The
// record and the events each count every revision from the turn's start, so
// whatever the order the round must not be counted twice.
func TestChatRoundSurvivesRecordBetweenReplayedEvents(t *testing.T) {
	two := 2
	submitted := chatFrame(t, 1, wire.EventKindTurnSubmitted, wire.EvTurnSubmitted{TurnID: "t1", SubmissionID: "s1", Preset: "review", ConversationID: "c"})
	revision := chatFrame(t, 2, wire.EventKindTurnState, wire.EvTurnState{TurnID: "t1", State: "revision_dispatched"})
	open := &wire.TurnReply{State: "revision_dispatched", Mode: "review", Preset: "review", Round: 1, MaxRounds: &two}
	final := &wire.TurnReply{State: "approved", Mode: "review", Preset: "review", Round: 1, MaxRounds: &two}

	s := apply(chatApp(t).state, submitted)
	s = MergeTurn(s, "t1", open, chatNow)
	s = apply(s, revision)
	if got := s.Turns["t1"].Round; got != 1 {
		t.Fatalf("round after record/replay race = %d, want 1", got)
	}
	// The same frame arriving again after a reconnect is not a new revision.
	s = apply(s, revision)
	if got := s.Turns["t1"].Round; got != 1 {
		t.Fatalf("round after a resent revision frame = %d, want 1", got)
	}
	s = MergeTurn(s, "t1", final, chatNow)
	if got := s.Turns["t1"].Round; got != 1 {
		t.Fatalf("round after the terminal record = %d, want 1", got)
	}
	if strip := renderStrip(s, 200, chatNow); strings.Contains(strip, "[round 2/") {
		t.Errorf("strip = %q, want no doubled round", strip)
	}

	// Events first, then the record: still one revision.
	s = apply(chatApp(t).state, submitted)
	s = apply(s, revision)
	s = MergeTurn(s, "t1", final, chatNow)
	if got := s.Turns["t1"].Round; got != 1 {
		t.Errorf("round with events before the record = %d, want 1", got)
	}
	// A further revision after the record has been merged is still counted.
	s = apply(s, chatFrame(t, 3, wire.EventKindTurnState, wire.EvTurnState{TurnID: "t1", State: "revision_dispatched"}))
	if got := s.Turns["t1"].Round; got != 2 {
		t.Errorf("round after a second revision = %d, want 2", got)
	}
}
