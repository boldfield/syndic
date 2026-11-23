package tui

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// Acceptance tests for the "TUI" paragraph of section 7 of the v5 delta. Each
// asserts the exact text the user sees, or the effect a key has on the
// engine, and fails when it is absent. docs/acceptance/milestone-d.md names
// each of them against its clause, and TestV5LedgerNamesRealTests keeps that
// document honest.

// convApp is an offline App on a conversation, laid out at 80x24.
func convApp(t *testing.T, id string, turns []string, opts ...Option) *App {
	t.Helper()
	return offlineApp(t, append([]Option{WithConversation(id, turns), WithWorkDir("/ws/x")}, opts...)...)
}

// TestV5ChatFirstLineNamesTheConversation: the first line of the chat names
// the open conversation by id prefix and turn count, and a new conversation
// as new.
func TestV5ChatFirstLineNamesTheConversation(t *testing.T) {
	for _, tc := range []struct {
		turns []string
		want  string
	}{
		{[]string{"t1"}, "conversation 01234567, 1 turns · workspace /ws/x 0 earlier · 0%"},
		{[]string{"t1", "t2", "t3"}, "conversation 01234567, 3 turns · workspace /ws/x 0 earlier · 0%"},
	} {
		a := convApp(t, "0123456789abcdef", tc.turns)
		lines := strings.Split(a.View(), "\n")
		if len(lines) < 2 || lines[1] != tc.want {
			t.Errorf("%d turns: first line of the chat = %q, want %q\n%s", len(tc.turns), lines[1], tc.want, a.View())
		}
	}
	a := offlineApp(t)
	if got := strings.Split(a.View(), "\n")[1]; !strings.HasPrefix(got, "conversation: new") {
		t.Errorf("empty conversation: first line = %q, want it to start %q", got, "conversation: new")
	}
}

// TestV5StatusLineIsLastAtEverySize: the status line is the last line of the
// view on every tab at several sizes and after each resize, with the input
// directly above it.
func TestV5StatusLineIsLastAtEverySize(t *testing.T) {
	a := chatApp(t)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("t%d", i)
		addTurn(a, id, int64(i), "approved", "question "+id, "answer "+id)
		approveBy(a, id, "rev")
	}
	sizes := [][2]int{{80, 24}, {120, 40}, {200, 60}, {60, 16}, {40, 10}, {100, 30}, {80, 24}}
	for _, size := range sizes {
		w, h := size[0], size[1]
		a.Update(tea.WindowSizeMsg{Width: w, Height: h})
		for _, tb := range []tab{tabChat, tabTranscript, tabParticipants} {
			a.tab = tb
			lines := strings.Split(a.View(), "\n")
			if len(lines) != h {
				t.Fatalf("%dx%d tab %d: %d lines, want %d", w, h, tb, len(lines), h)
			}
			last := lines[h-1]
			if !strings.HasPrefix(last, "[review]") {
				t.Errorf("%dx%d tab %d: last line = %q, want the status line starting [review]", w, h, tb, last)
			}
			if !strings.HasPrefix(lines[h-2], "> ") {
				t.Errorf("%dx%d tab %d: the line above the status line = %q, want the input", w, h, tb, lines[h-2])
			}
			for i, l := range lines[:h-1] {
				if strings.HasPrefix(l, "[review]") {
					t.Errorf("%dx%d tab %d: a status line at line %d, not last", w, h, tb, i)
				}
			}
		}
	}
}

// TestV5EveryV4BannerStringIsVerbatim: each outcome banner of v4 appears
// exactly as it was.
func TestV5EveryV4BannerStringIsVerbatim(t *testing.T) {
	t.Setenv("TERM", "dumb")
	a := chatApp(t)
	a.width, a.height = 100, 80

	addTurn(a, "t1", 1, "solo_complete", "solo question", "")
	a.state.Attempts["a1"] = AttemptInfo{ID: "a1", TurnID: "t1", Phase: "author", Status: "complete"}
	a.content.Store("attempt:a1", "solo answer", nil)
	addTurn(a, "t2", 2, "incomplete", "incomplete question", "incomplete draft")
	addTurn(a, "t3", 3, "overridden", "override question", "override draft")
	a.overrideReasons["t3"] = "good enough"
	addTurn(a, "t4", 4, "cancelled", "cancelled question", "cancelled draft")
	addTurn(a, "t5", 5, "reviewers_dispatched", "running question", "")
	a.state.Attempts["a5"] = AttemptInfo{ID: "a5", TurnID: "t5", Phase: "author", Status: "complete"}
	a.content.Store("attempt:a5", "draft text", nil)
	a.SetDraftsEnabled(true)

	view := a.View()
	for _, want := range []string{
		"UNREVIEWED (solo)",
		"INCOMPLETE, not approved",
		"HUMAN OVERRIDE: good enough",
		"CANCELLED, not approved",
		"UNREVIEWED DRAFT",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks the banner %q:\n%s", want, view)
		}
	}
}

// TestV5NoApprovedAnswerAndNotInContextMarkers: a turn that ended without an
// approved answer shows "no approved answer (<state>)" for its own state, and a
// turn /trim dropped shows "not in context", on its own line, once.
func TestV5NoApprovedAnswerAndNotInContextMarkers(t *testing.T) {
	t.Setenv("TERM", "dumb")
	a := chatApp(t)
	a.width, a.height = 100, 80
	addTurn(a, "t1", 1, "incomplete", "q1", "draft1")
	addTurn(a, "t2", 2, "cancelled", "q2", "draft2")
	addTurn(a, "t3", 3, "approved", "q3", "answer3")
	approveBy(a, "t3", "rev")
	addTurn(a, "t4", 4, "approved", "q4", "answer4")
	approveBy(a, "t4", "rev")
	a.state.ContextFrom = "t3"

	lines := strings.Split(a.View(), "\n")
	count := func(want string) (n int) {
		for _, l := range lines {
			if l == want {
				n++
			}
		}
		return n
	}
	for _, want := range []string{"no approved answer (incomplete)", "no approved answer (cancelled)"} {
		if n := count(want); n != 1 {
			t.Errorf("%d lines are exactly %q, want 1:\n%s", n, want, strings.Join(lines, "\n"))
		}
	}
	// t1 and t2 precede the trim point; t3 and t4 do not.
	if n := count(notInContext); n != 2 {
		t.Errorf("%d lines are exactly %q, want 2 (t1 and t2):\n%s", n, notInContext, strings.Join(lines, "\n"))
	}
	if i, j := lineOf(t, lines, "› q3"), lineOf(t, lines, "› q2"); i < j {
		t.Fatalf("turn order broken: q3 at %d before q2 at %d", i, j)
	}
	// The marker closes the turn it names: none follows q3's block.
	first := lineOf(t, lines, "› q3")
	for _, l := range lines[first:] {
		if l == notInContext {
			t.Errorf("a turn from the trim point on is marked %q", notInContext)
		}
	}
}

// TestV5HelpListsEveryCommandAndKey: /help lists every command the App
// dispatches, each on its own line, and every key.
func TestV5HelpListsEveryCommandAndKey(t *testing.T) {
	a := offlineApp(t)
	typeRunes(a, "/help")
	press(a, tea.KeyMsg{Type: tea.KeyEnter})
	pages := []string{a.View()}
	if !strings.Contains(pages[0], "commands:") {
		t.Fatalf("/help showed no command list:\n%s", pages[0])
	}
	for range 6 {
		press(a, tea.KeyMsg{Type: tea.KeyPgDown})
		pages = append(pages, a.View())
	}
	var all []string
	for _, p := range pages {
		all = append(all, strings.Split(p, "\n")...)
	}
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		n := 0
		for _, l := range all {
			if strings.HasPrefix(l, "/"+name+" ") {
				n++
			}
		}
		if n == 0 {
			t.Errorf("/help does not list /%s on any page", name)
		}
	}
	for _, key := range []string{"Tab, Shift+Tab", "PgUp, PgDn", "Enter", "Alt+Enter", "Ctrl+C", "Ctrl+D", "Esc"} {
		found := false
		for _, l := range all {
			found = found || strings.HasPrefix(l, key+" ")
		}
		if !found {
			t.Errorf("/help does not list the key %s", key)
		}
	}
}

// openTurnRig is a rig whose author is held, so a submitted turn stays open.
func openTurnRig(t *testing.T) (*rig, func()) {
	t.Helper()
	r := newRig(t)
	release := r.h.Hold("author")
	r.waitConnected(t)
	return r, release
}

// TestV5CtrlDAndCtrlCDetachWithoutCancellingARunningTurn: with a turn running
// and the input empty, Ctrl+D and Ctrl+C each end the TUI, send no cancel, and
// the turn runs on to approval.
func TestV5CtrlDAndCtrlCDetachWithoutCancellingARunningTurn(t *testing.T) {
	for _, key := range []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"ctrl+d", tea.KeyMsg{Type: tea.KeyCtrlD}},
		{"ctrl+c", tea.KeyMsg{Type: tea.KeyCtrlC}},
	} {
		t.Run(key.name, func(t *testing.T) {
			r, release := openTurnRig(t)
			r.typeLine("a long question")
			turn := r.waitTurn(t)
			if got := openTurns(t, r.paths); len(got) != 1 || got[0] != turn {
				t.Fatalf("open turns before the key = %v, want [%s]", got, turn)
			}

			r.tm.Send(key.msg)
			app := r.tm.FinalModel(t, teatest.WithFinalTimeout(10*time.Second)).(*probe).app
			if app.CurrentTurn() != turn {
				t.Errorf("CurrentTurn after the detach = %q, want %q", app.CurrentTurn(), turn)
			}
			if got := openTurns(t, r.paths); len(got) != 1 || got[0] != turn {
				t.Errorf("open turns after the detach = %v, want [%s]: the turn must continue", got, turn)
			}
			for id, ad := range r.h.Adapters {
				if calls := ad.CancelCalls(); len(calls) != 0 {
					t.Errorf("%s: cancel sent after %s: %v", id, key.name, calls)
				}
			}

			release()
			r.h.WaitTurnState(turn, "approved")
			for _, s := range r.h.TurnStates(turn) {
				if s == "cancelled" {
					t.Errorf("turn states %v include cancelled", r.h.TurnStates(turn))
				}
			}
			if got := openTurns(t, r.paths); len(got) != 0 {
				t.Errorf("open turns after the turn finished = %v", got)
			}
		})
	}
}

// TestV5CtrlCWithTextClearsAndSendsNothing: Ctrl+C is not a cancel even while a
// turn is running: with text typed it only clears the input.
func TestV5CtrlCWithTextClearsAndSendsNothing(t *testing.T) {
	r, release := openTurnRig(t)
	r.typeLine("a long question")
	turn := r.waitTurn(t)
	r.tm.Type("half a thought")
	r.until(t, "the text to be typed", func(a *App) (string, bool) { return "", a.input.Value() == "half a thought" })
	r.tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	r.until(t, "the input to be cleared", func(a *App) (string, bool) { return "", a.input.Value() == "" })
	for id, ad := range r.h.Adapters {
		if calls := ad.CancelCalls(); len(calls) != 0 {
			t.Errorf("%s: cancel sent by ctrl+c: %v", id, calls)
		}
	}
	release()
	r.h.WaitTurnState(turn, "approved")
}

// TestV5ApprovedAfterRevisionShowsIdsCountAndHash: through a real engine, a
// block then a revised candidate and an approval renders
// "approved by <ids> after 1 revision against <hash>" on the chat tab.
func TestV5ApprovedAfterRevisionShowsIdsCountAndHash(t *testing.T) {
	roster := strings.Replace(enginetest.RosterTOML, "max_rounds = 0", "max_rounds = 2\nmax_rounds_ceiling = 5", 1)
	block := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "wrong", "fix it", "wrong")
	})
	approveV2 := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictV2WithBlockStates(req.Envelope.CandidateHash, req.Envelope.AttemptID, 1, []string{"block-state: b1 withdrawn"})
	})
	h := enginetest.New(t, roster, map[string][]fake.Step{
		"author": {
			enginetest.Reply("the answer is 41"), enginetest.Reply("the answer is 42, and here is why"),
			enginetest.Reply("ok"), enginetest.Reply("ok"), enginetest.Reply("ok"),
		},
		"reviewer": {block, approveV2},
	})
	r := &rig{h: h, paths: newPaths(t), seen: &seen{}}
	r.srv = startServer(t, r.paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, r.paths.Socket, WithEventObserver(r.seen.observe))
	r.tm = teatest.NewTestModel(t, &probe{app: r.app}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })

	r.waitConnected(t)
	r.typeLine("what is the answer?")
	turn := r.waitTurn(t)
	view := r.until(t, "the revised turn approved on the chat tab", func(a *App) (string, bool) {
		ti := a.state.Turns[turn]
		if ti.State != "approved" || ti.CandidateHash == nil {
			return "", false
		}
		want := "approved by reviewer after 1 revision against " + (*ti.CandidateHash)[:12]
		v := a.View()
		return v, strings.Contains(v, want) && strings.Contains(v, "the answer is 42, and here is why")
	})
	if !strings.Contains(view, "the answer is 42, and here is why") {
		t.Errorf("the chat does not show the revised answer:\n%s", view)
	}
	if strings.Contains(view, "the answer is 41") {
		t.Errorf("the chat shows the superseded candidate:\n%s", view)
	}
}

// TestV5LedgerNamesRealTests keeps docs/acceptance/milestone-d.md honest: every
// Test function a ledger row names exists in the Go package it names, none of
// them calls Skip, and every row names at least one.
func TestV5LedgerNamesRealTests(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "docs", "acceptance", "milestone-d.md"))
	if err != nil {
		t.Fatal(err)
	}
	cache := map[string]map[string]*ast.FuncDecl{}
	funcs := func(dir string) map[string]*ast.FuncDecl {
		if m, ok := cache[dir]; ok {
			return m
		}
		m := map[string]*ast.FuncDecl{}
		cache[dir] = m
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(dir), "*_test.go"))
		if err != nil || len(matches) == 0 {
			t.Errorf("no test files in %s", dir)
			return m
		}
		for _, name := range matches {
			f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
					m[fn.Name.Name] = fn
				}
			}
		}
		return m
	}
	ref := regexp.MustCompile("`((?:cmd|internal)/[a-z/]+)\\.(Test[A-Za-z0-9_]+)`")
	rows := 0
	for n, line := range bytes.Split(raw, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("| ")) || bytes.HasPrefix(line, []byte("| Clause")) || bytes.HasPrefix(line, []byte("| Behavior")) || bytes.HasPrefix(line, []byte("| ---")) {
			continue
		}
		rows++
		found := ref.FindAllSubmatch(line, -1)
		if len(found) == 0 {
			t.Errorf("milestone-d.md:%d: row names no test: %.80s", n+1, line)
		}
		for _, m := range found {
			dir, name := string(m[1]), string(m[2])
			fn, ok := funcs(dir)[name]
			if !ok {
				t.Errorf("milestone-d.md:%d: %s has no test %s", n+1, dir, name)
				continue
			}
			ast.Inspect(fn, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Skip" || sel.Sel.Name == "Skipf" || sel.Sel.Name == "SkipNow") {
						t.Errorf("milestone-d.md:%d: %s.%s calls %s", n+1, dir, name, sel.Sel.Name)
					}
				}
				return true
			})
		}
	}
	if rows < 90 {
		t.Errorf("the ledger has %d rows; every clause of section 7's D paragraphs needs one", rows)
	}
}
