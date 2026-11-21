package tui

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// These tests drive the provisional-deadline question under teatest against
// a real engine, and read the submit requests the App sent.

var provisionalRoster = strings.Replace(enginetest.RosterTOML, "provisional = false", "provisional = true", 1)

// requests records every submit request an App sends.
type requests struct {
	mu   sync.Mutex
	reqs []wire.SubmitRequest
}

func (r *requests) observe(req wire.SubmitRequest) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
}

func (r *requests) snapshot() []wire.SubmitRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]wire.SubmitRequest(nil), r.reqs...)
}

func allows(req wire.SubmitRequest) bool { return req.AllowProvisional != nil && *req.AllowProvisional }

// provisionalRig is a connected TUI on an engine with the roster, recording its submit requests.
func provisionalRig(t *testing.T, roster string) (*rig, *requests) {
	t.Helper()
	steps := func(s fake.Step) []fake.Step { return []fake.Step{s, s, s, s, s, s} }
	h := enginetest.New(t, roster, map[string][]fake.Step{
		"author":   steps(enginetest.Reply(answerText)),
		"reviewer": steps(approve()),
	})
	paths := newPaths(t)
	r := &rig{h: h, paths: paths, seen: &seen{}}
	r.srv = startServer(t, paths, h.Engine, h)
	return r, attach(t, r)
}

// attach starts a new TUI process on the rig's engine.
func attach(t *testing.T, r *rig) *requests {
	t.Helper()
	reqs := &requests{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, r.paths.Socket, WithSubmitObserver(reqs.observe), WithWorkDir(t.TempDir()))
	r.tm = teatest.NewTestModel(t, &probe{app: r.app}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })
	r.waitConnected(t)
	return reqs
}

// submitted is the submission id of every turn the engine created.
func submitted(r *rig) []string {
	var ids []string
	for _, e := range r.h.Events() {
		if p, ok := e.Payload.(*wire.EvTurnSubmitted); ok {
			ids = append(ids, p.SubmissionID)
		}
	}
	return ids
}

// askedWith waits for the provisional question over the typed line and returns the view.
func askedWith(t *testing.T, r *rig, line string) string {
	t.Helper()
	return r.until(t, "the provisional question", func(a *App) (string, bool) {
		v := a.View()
		return v, a.question != nil && a.question.refused != nil && strings.Contains(v, provisionalQuestionText) &&
			strings.Contains(v, yesNoPrompt+line) && a.input.Value() == line
	})
}

// turnsEnded waits for the turn the submit created to end and the input to be cleared.
func turnsEnded(t *testing.T, r *rig, n int) string {
	t.Helper()
	return r.until(t, "the turn to end", func(a *App) (string, bool) {
		id := answerTurn(a.state)
		return id, len(a.state.Turns) == n && a.state.Turns[id].State == "approved" && a.input.Value() == "" && a.question == nil
	})
}

func TestProvisionalFirstSubmitAsksAndCreatesNoTurn(t *testing.T) {
	r, reqs := provisionalRig(t, provisionalRoster)
	untilView(t, r, "deadlines: provisional")

	r.typeLine("my question")
	v := askedWith(t, r, "my question")
	flat := strings.ReplaceAll(v, "\n", "")
	for _, want := range []string{"provisional deadlines are in effect", r.h.RosterPath, "deadlines: provisional"} {
		if !strings.Contains(flat, want) {
			t.Errorf("question view lacks %q:\n%s", want, v)
		}
	}
	got := reqs.snapshot()
	if len(got) != 1 || got[0].AllowProvisional != nil || got[0].Input != "my question" {
		t.Fatalf("requests = %+v", got)
	}
	if ids := submitted(r); len(ids) != 0 {
		t.Fatalf("turns created: %v", ids)
	}

	// Keys other than y, n and esc are not typed into the line.
	pressKey(r, "x")
	askedWith(t, r, "my question")
}

func TestProvisionalYesResendsOnceAndIsRemembered(t *testing.T) {
	r, reqs := provisionalRig(t, provisionalRoster)
	r.typeLine("my question")
	askedWith(t, r, "my question")
	pressKey(r, "y")
	turnsEnded(t, r, 1)

	got := reqs.snapshot()
	if len(got) != 2 || got[0].AllowProvisional != nil || !allows(got[1]) {
		t.Fatalf("requests = %+v", got)
	}
	if got[1].SubmissionID == got[0].SubmissionID || got[1].Input != "my question" {
		t.Fatalf("resend %+v reuses the refused id or changes the input of %+v", got[1], got[0])
	}
	if ids := submitted(r); len(ids) != 1 || ids[0] != got[1].SubmissionID {
		t.Fatalf("turns created by %v, want one by %s", ids, got[1].SubmissionID)
	}
	untilView(t, r, "deadlines: provisional")

	// A later submit in the same process is sent with the flag and never asks.
	r.typeWithoutQuestion(t, "second question")
	turnsEnded(t, r, 2)
	got = reqs.snapshot()
	if len(got) != 3 || !allows(got[2]) {
		t.Fatalf("requests = %+v", got)
	}
	if ids := submitted(r); len(ids) != 2 || ids[1] != got[2].SubmissionID {
		t.Fatalf("turns created by %v", ids)
	}
	untilView(t, r, "deadlines: provisional")
}

// typeWithoutQuestion submits a line and fails if a question opens before the turn ends.
func (r *rig) typeWithoutQuestion(t *testing.T, line string) {
	t.Helper()
	r.typeLine(line)
	asked := false
	r.until(t, "the submit to be accepted", func(a *App) (string, bool) {
		asked = asked || a.question != nil
		return "", asked || a.input.Value() == ""
	})
	if asked {
		t.Fatal("a question opened for a submit after yes")
	}
}

func TestProvisionalNoAndEscKeepTheInputAndAskAgain(t *testing.T) {
	r, reqs := provisionalRig(t, provisionalRoster)
	r.typeLine("keep me")
	askedWith(t, r, "keep me")

	for i, key := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("n")}, {Type: tea.KeyEsc}} {
		send(r, key)
		v := r.until(t, "the question to close", func(a *App) (string, bool) {
			return a.View(), a.question == nil && a.input.Value() == "keep me"
		})
		if !strings.Contains(v, "not accepted; nothing sent") || !strings.Contains(v, defaultPrompt+"keep me") || strings.Contains(v, provisionalQuestionText) {
			t.Fatalf("after %s:\n%s", key, v)
		}
		if got := reqs.snapshot(); len(got) != i+1 {
			t.Fatalf("after %s requests = %+v", key, got)
		}
		untilView(t, r, "deadlines: provisional")

		// The next submit of the kept line is refused and asks again.
		send(r, tea.KeyMsg{Type: tea.KeyEnter})
		askedWith(t, r, "keep me")
	}
	for _, req := range reqs.snapshot() {
		if req.AllowProvisional != nil || req.Input != "keep me" {
			t.Fatalf("request %+v", req)
		}
	}
	if ids := submitted(r); len(ids) != 0 {
		t.Fatalf("turns created: %v", ids)
	}
}

func TestProvisionalFalseNeverAsks(t *testing.T) {
	r, reqs := provisionalRig(t, enginetest.RosterTOML)
	r.typeWithoutQuestion(t, "first")
	turnsEnded(t, r, 1)
	r.typeWithoutQuestion(t, "second")
	turnsEnded(t, r, 2)

	got := reqs.snapshot()
	if len(got) != 2 {
		t.Fatalf("requests = %+v", got)
	}
	for _, req := range got {
		if req.AllowProvisional != nil {
			t.Fatalf("request carried allow_provisional: %+v", req)
		}
	}
	if v := view(t, r); strings.Contains(v, "deadlines: provisional") {
		t.Fatalf("banner without provisional deadlines:\n%s", v)
	}
}

func TestProvisionalNewProcessAsksAgain(t *testing.T) {
	r, _ := provisionalRig(t, provisionalRoster)
	r.typeLine("first")
	askedWith(t, r, "first")
	pressKey(r, "y")
	turnsEnded(t, r, 1)
	r.final(t)

	reqs := attach(t, r)
	r.typeLine("second")
	askedWith(t, r, "second")
	if got := reqs.snapshot(); len(got) != 1 || got[0].AllowProvisional != nil {
		t.Fatalf("new process requests = %+v", got)
	}
	if ids := submitted(r); len(ids) != 1 {
		t.Fatalf("turns created: %v", ids)
	}
}

func TestProvisionalResendsTheRefusedSubmission(t *testing.T) {
	r, _ := provisionalRig(t, provisionalRoster)
	_ = r.final(t)
	// The driver feeds replies only when asked, so both lines are submitted
	// before either refusal is back; the line stays in the input until a
	// submit is accepted.
	reqs := &requests{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &driver{t: t, app: NewApp(ctx, r.paths.Socket, WithSubmitObserver(reqs.observe), WithWorkDir(t.TempDir())), msgs: make(chan tea.Msg, 1024)}
	d.send(resizeMsg(80, 24))
	d.run(d.app.Init())
	d.until("the TUI to connect", func(string) bool { return d.app.state.ConnState == ConnStateConnected })

	d.typeLine("first")
	d.typeLine(" and more")
	d.until("both refusals", func(string) bool { return d.app.question != nil && len(d.app.questions) == 1 })
	d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	d.until("both resends", func(string) bool {
		return len(reqs.snapshot()) == 4 && d.app.question == nil && len(d.app.questions) == 0
	})

	got := reqs.snapshot()
	inputs := map[string]int{}
	for i, req := range got {
		if (i < 2) == (req.AllowProvisional != nil) || (i >= 2 && !allows(req)) {
			t.Fatalf("requests = %+v", got)
		}
		inputs[req.Input]++
	}
	if inputs["first"] != 2 || inputs["first and more"] != 2 || got[2].SubmissionID == got[0].SubmissionID ||
		got[2].SubmissionID == got[1].SubmissionID || got[3].SubmissionID == got[0].SubmissionID || got[3].SubmissionID == got[1].SubmissionID {
		t.Fatalf("requests = %+v", got)
	}
}

// questionApp is an App with no engine whose project selects an unapproved quick profile.
func questionApp(t *testing.T) *App {
	t.Helper()
	_, project := quickEnv(t)
	writeQuickFile(t, project, `profile = "fast"`)
	a := NewApp(context.Background(), "unused.sock", WithWorkDir(project))
	a.state.Status = &wire.StatusReply{RosterPath: "/r/roster.toml", DeadlinesProvisional: true}
	a.input.SetValue("my question")
	return a
}

func (a *App) refuseProvisional(input string) {
	a.Update(submitMsg{sub: submission{id: "s1", input: input}, err: &wire.Error{Code: wire.ErrorCodeProvisional}})
}

func (a *App) offerApproval() {
	a.state.Status.QuickProfiles = []wire.QuickProfile{{Name: "fast", Preset: "solo", DefinitionSHA256: "d1"}}
	a.updateQuickProfileState()
}

func pressAppKey(a *App, k string) { a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}) }

func wantQuestion(t *testing.T, a *App, text string) {
	t.Helper()
	v := a.View()
	if !strings.Contains(v, text) || !strings.Contains(v, yesNoPrompt+"my question") {
		t.Fatalf("view lacks %q over the kept input:\n%s", text, v)
	}
}

func wantNoQuestion(t *testing.T, a *App) {
	t.Helper()
	v := a.View()
	if a.question != nil || len(a.questions) != 0 || strings.Contains(v, "(y/n)") || !strings.Contains(v, defaultPrompt+"my question") {
		t.Fatalf("questions left, or the input lost:\n%s", v)
	}
}

func TestProvisionalWaitsBehindAnOpenApproval(t *testing.T) {
	a := questionApp(t)
	a.offerApproval()
	wantQuestion(t, a, approvalPromptText)
	a.refuseProvisional("my question")
	wantQuestion(t, a, approvalPromptText)

	pressAppKey(a, "y")
	wantQuestion(t, a, provisionalQuestionText)
	if !strings.Contains(a.View(), "quick profile fast approved") {
		t.Errorf("the approval's outcome is not shown:\n%s", a.View())
	}
	pressAppKey(a, "n")
	wantNoQuestion(t, a)
}

func TestApprovalWaitsBehindAnOpenProvisionalQuestion(t *testing.T) {
	for _, answer := range []string{"y", "n"} {
		a := questionApp(t)
		a.refuseProvisional("my question")
		wantQuestion(t, a, provisionalQuestionText)
		a.offerApproval()
		a.offerApproval() // a later status refresh does not queue it twice
		wantQuestion(t, a, provisionalQuestionText)

		pressAppKey(a, "n")
		wantQuestion(t, a, approvalPromptText)
		pressAppKey(a, answer)
		wantNoQuestion(t, a)
	}
}
