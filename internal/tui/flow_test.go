package tui

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	"github.com/mattn/go-runewidth"
)

func frame(seq int64, at time.Time, kind string, body any) wire.Frame {
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return wire.Frame{V: 1, Type: "event", Seq: seq, Kind: kind, At: at.Format(time.RFC3339), Body: b}
}

// TestApplyFullReviewTurn folds the events of a review turn, with a repair
// attempt and a block, and checks the state after every one.
func TestApplyFullReviewTurn(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return t0.Add(time.Duration(sec) * time.Second) }
	dl := at(300).Format(time.RFC3339)
	hash := strings.Repeat("c", 64)
	repairOf := "a2"
	reason := "all_required_reviewers_approved"

	s := NewState()
	s = MergeStatus(s, &wire.StatusReply{
		DefaultPreset: "review",
		Participants: []wire.Participant{
			{ID: "author", State: "idle", Pool: "default"},
			{ID: "r1", State: "idle", Pool: "default"},
		},
		Pools: []wire.Pool{{Name: "default", Concurrency: 1}},
	})

	steps := []struct {
		name  string
		frame wire.Frame
		check func(t *testing.T, s State)
	}{
		{"turn submitted", frame(1, at(0), wire.EventKindTurnSubmitted, wire.EvTurnSubmitted{TurnID: "t1", SubmissionID: "s1", Preset: "review", EstimatedTokens: 1000}),
			func(t *testing.T, s State) {
				ti := s.Turns["t1"]
				if s.CurrentTurn != "t1" || ti.State != "submitted" || ti.Preset != "review" || ti.EstimatedTokens != 1000 || !ti.SubmittedAt.Equal(at(0)) {
					t.Fatalf("turn = %+v current=%q", ti, s.CurrentTurn)
				}
			}},
		{"author running", frame(2, at(1), wire.EventKindParticipantState, wire.EvParticipantState{Participant: "author", State: "running"}),
			func(t *testing.T, s State) {
				if s.Participants["author"].State != "running" {
					t.Fatalf("participant = %+v", s.Participants["author"])
				}
			}},
		{"author attempt begun", frame(3, at(1), wire.EventKindAttemptBegun, wire.EvAttemptBegun{AttemptID: "a1", Participant: "author", Phase: "author", Epoch: 1, Deadline: dl, MaxOutputTokens: 4096, EstimatedPromptTokens: 500}),
			func(t *testing.T, s State) {
				a := s.Attempts["a1"]
				if a.Participant != "author" || a.Phase != "author" || a.Epoch != 1 || !a.Deadline.Equal(at(300)) || a.TurnID != "t1" {
					t.Fatalf("attempt = %+v", a)
				}
				if ti := s.Turns["t1"]; ti.Author != "author" || ti.AuthorState != "running" {
					t.Fatalf("turn author = %q %q", ti.Author, ti.AuthorState)
				}
			}},
		{"author attempt completed", frame(4, at(5), wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{AttemptID: "a1", Status: "complete", TransportStatus: "ok", ScrubCount: 2, Usage: &wire.ReportedUsage{PromptTokens: ptr(int64(480)), CompletionTokens: ptr(int64(60))}}),
			func(t *testing.T, s State) {
				a := s.Attempts["a1"]
				if a.Status != "complete" || a.TransportStatus != "ok" || a.ScrubCount != 2 || *a.ReportedPrompt != 480 || *a.ReportedCompletion != 60 {
					t.Fatalf("attempt = %+v", a)
				}
			}},
		{"author idle", frame(5, at(5), wire.EventKindParticipantState, wire.EvParticipantState{Participant: "author", State: "idle"}),
			func(t *testing.T, s State) {
				if got := s.Turns["t1"].AuthorState; got != "idle" {
					t.Fatalf("author state = %q", got)
				}
			}},
		{"message appended", frame(6, at(5), wire.EventKindMessageAppended, wire.EvMessageAppended{MsgID: "m1", TurnID: "t1", Phase: "author", Sender: "author", SHA256: hash, ByteLen: 321}),
			func(t *testing.T, s State) {
				if m := s.Messages["m1"]; m.Phase != "author" || m.Sender != "author" || m.SHA256 != hash || m.ByteLen != 321 {
					t.Fatalf("message = %+v", m)
				}
			}},
		{"candidate frozen", frame(7, at(6), wire.EventKindCandidateFrozen, wire.EvCandidateFrozen{TurnID: "t1", CandidateHash: hash}),
			func(t *testing.T, s State) {
				if h := s.Turns["t1"].CandidateHash; h == nil || *h != hash {
					t.Fatalf("candidate hash = %v", h)
				}
			}},
		{"reviewer queued", frame(8, at(6), wire.EventKindParticipantState, wire.EvParticipantState{Participant: "r1", State: "queued"}),
			func(t *testing.T, s State) {
				if s.Participants["r1"].State != "queued" {
					t.Fatalf("r1 = %+v", s.Participants["r1"])
				}
			}},
		{"reviewer attempt begun", frame(9, at(7), wire.EventKindAttemptBegun, wire.EvAttemptBegun{AttemptID: "a2", Participant: "r1", Phase: "review", Epoch: 1, Deadline: dl}),
			func(t *testing.T, s State) {
				ti := s.Turns["t1"]
				if !reflect.DeepEqual(ti.Reviewers, []string{"r1"}) || ti.ReviewerStates["r1"] != "queued" {
					t.Fatalf("reviewers = %v %v", ti.Reviewers, ti.ReviewerStates)
				}
			}},
		{"reviewer running", frame(10, at(8), wire.EventKindParticipantState, wire.EvParticipantState{Participant: "r1", State: "running"}),
			func(t *testing.T, s State) {
				if got := s.Turns["t1"].ReviewerStates["r1"]; got != "running" {
					t.Fatalf("reviewer state = %q", got)
				}
			}},
		{"reviewer attempt completed", frame(11, at(20), wire.EventKindAttemptCompleted, wire.EvAttemptCompleted{AttemptID: "a2", Status: "complete", TransportStatus: "ok"}),
			func(t *testing.T, s State) {
				if s.Attempts["a2"].Status != "complete" {
					t.Fatalf("attempt = %+v", s.Attempts["a2"])
				}
			}},
		{"malformed verdict", frame(12, at(20), wire.EventKindVerdictRecorded, wire.EvVerdictRecorded{VerdictID: "v1", AttemptID: "a2", Parsed: false, ValidationStatus: "malformed"}),
			func(t *testing.T, s State) {
				v := s.Verdicts["v1"]
				a := s.Attempts["a2"]
				if v.Parsed || v.Class != "malformed" || a.VerdictParsed || a.VerdictClass != "malformed" {
					t.Fatalf("verdict = %+v attempt = %+v", v, a)
				}
			}},
		{"repair attempt begun", frame(13, at(21), wire.EventKindAttemptBegun, wire.EvAttemptBegun{AttemptID: "a3", Participant: "r1", Phase: "review", Epoch: 1, Deadline: dl, RepairOf: &repairOf}),
			func(t *testing.T, s State) {
				if r := s.Attempts["a3"].RepairOf; r == nil || *r != "a2" {
					t.Fatalf("repair_of = %v", r)
				}
				if got := s.Turns["t1"].Reviewers; !reflect.DeepEqual(got, []string{"r1"}) {
					t.Fatalf("a repair must not add a reviewer: %v", got)
				}
			}},
		{"repair verdict", frame(14, at(30), wire.EventKindVerdictRecorded, wire.EvVerdictRecorded{VerdictID: "v2", AttemptID: "a3", Parsed: true, ValidationStatus: "approve"}),
			func(t *testing.T, s State) {
				if a := s.Attempts["a3"]; !a.VerdictParsed || a.VerdictClass != "approve" {
					t.Fatalf("attempt = %+v", a)
				}
			}},
		{"block opened", frame(15, at(31), wire.EventKindBlockOpened, wire.EvBlockOpened{BlockID: "b1", Severity: "major"}),
			func(t *testing.T, s State) {
				if b := s.Blocks["b1"]; b.Severity != "major" || b.State != "open" {
					t.Fatalf("block = %+v", b)
				}
			}},
		{"block resolved", frame(16, at(32), wire.EventKindBlockState, wire.EvBlockState{BlockID: "b1", State: "resolved"}),
			func(t *testing.T, s State) {
				if b := s.Blocks["b1"]; b.State != "resolved" {
					t.Fatalf("block = %+v", b)
				}
			}},
		{"budget", frame(17, at(32), wire.EventKindBudget, wire.EvBudget{TurnID: "t1", Budget: "admitted", Limit: 250000}),
			func(t *testing.T, s State) {
				if s.LastBudgetNotice == nil || s.LastBudgetNotice.Kind != "admitted" || s.Turns["t1"].LimitTokens != 250000 {
					t.Fatalf("budget = %+v limit=%d", s.LastBudgetNotice, s.Turns["t1"].LimitTokens)
				}
			}},
		{"estimate exceeded", frame(18, at(33), wire.EventKindBudgetEstimateExceeded, wire.EvBudgetEstimateExceeded{TurnID: "t1", AttemptID: "a1", Estimated: 500, Reported: 900}),
			func(t *testing.T, s State) {
				if e := s.LastBudgetExceeded; e == nil || e.Estimated != 500 || e.Reported != 900 {
					t.Fatalf("exceeded = %+v", e)
				}
			}},
		{"budget exhausted", frame(19, at(34), wire.EventKindBudgetExhausted, wire.EvBudgetExhausted{TurnID: "t1", Charged: 260000, Limit: 250000}),
			func(t *testing.T, s State) {
				if e := s.LastBudgetExhausted; e == nil || e.Charged != 260000 || e.Limit != 250000 {
					t.Fatalf("exhausted = %+v", e)
				}
			}},
		{"turn approved", frame(20, at(40), wire.EventKindTurnState, wire.EvTurnState{TurnID: "t1", State: "approved", Reason: &reason}),
			func(t *testing.T, s State) {
				ti := s.Turns["t1"]
				if s.CurrentTurn != "" || ti.State != "approved" || ti.TerminalReason == nil || *ti.TerminalReason != reason || s.LastSeq != 20 {
					t.Fatalf("turn = %+v current=%q seq=%d", ti, s.CurrentTurn, s.LastSeq)
				}
			}},
	}
	for _, st := range steps {
		before := s
		beforeTurn := before.Turns["t1"]
		beforeStates := copyMap(beforeTurn.ReviewerStates)
		s = apply(s, st.frame)
		if again := apply(before, st.frame); !reflect.DeepEqual(again, s) {
			t.Fatalf("%s: apply is not deterministic", st.name)
		}
		if len(before.Turns["t1"].ReviewerStates) != len(beforeStates) || before.Turns["t1"].State != beforeTurn.State {
			t.Fatalf("%s: apply mutated its input", st.name)
		}
		st.check(t, s)
		if t.Failed() {
			t.Fatalf("after step %q", st.name)
		}
	}
}

func TestApplyKeepsReplayedSubmitTime(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s := apply(NewState(), frame(1, t0, wire.EventKindTurnSubmitted, wire.EvTurnSubmitted{TurnID: "t1", Preset: "review"}))
	if got := s.Turns["t1"].SubmittedAt; !got.Equal(t0) {
		t.Fatalf("SubmittedAt = %v, want the frame's time %v", got, t0)
	}
}

func TestMergeTurnKeepsLiveRolesAndEstimate(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s.Turns["t1"] = TurnInfo{ID: "t1", State: "author_dispatched", Preset: "review", SubmittedAt: t0, EstimatedTokens: 1000, Author: "author", AuthorState: "running", Reviewers: []string{"r1"}, ReviewerStates: map[string]string{"r1": "queued"}}
	s = MergeTurn(s, "t1", &wire.TurnReply{
		State: "author_dispatched", Preset: "review",
		Deadlines: wire.TurnDeadlines{Turn: 900000},
		Budget:    wire.BudgetRecord{Limit: ptr(int64(250000))},
	}, t0)
	ti := s.Turns["t1"]
	if ti.Author != "author" || ti.AuthorState != "running" || ti.EstimatedTokens != 1000 || len(ti.Reviewers) != 1 {
		t.Fatalf("a reply with no attempts clobbered live state: %+v", ti)
	}
	if ti.LimitTokens != 250000 || ti.DeadlineDur != 15*time.Minute || !ti.TurnDeadline.Equal(t0.Add(15*time.Minute)) {
		t.Fatalf("limit/deadline = %d %v %v", ti.LimitTokens, ti.DeadlineDur, ti.TurnDeadline)
	}
}

func TestMergeTurnDoesNotUndoTerminalState(t *testing.T) {
	s := NewState()
	s.Turns["t1"] = TurnInfo{ID: "t1", State: "approved"}
	s = MergeTurn(s, "t1", &wire.TurnReply{State: "reviewers_dispatched"}, time.Time{})
	if s.Turns["t1"].State != "approved" {
		t.Fatalf("state = %q", s.Turns["t1"].State)
	}
}

func TestMergeStatusKeepsPoolConcurrencyAndOpenTurnRoles(t *testing.T) {
	s := NewState()
	s.CurrentTurn = "t1"
	s.Turns["t1"] = TurnInfo{ID: "t1", Author: "author", Reviewers: []string{"r1"}, ReviewerStates: map[string]string{"r1": ""}}
	s = MergeStatus(s, &wire.StatusReply{
		Participants:  []wire.Participant{{ID: "author", State: "running"}, {ID: "r1", State: "queued"}},
		Pools:         []wire.Pool{{Name: "default", Concurrency: 2, Holds: []wire.PoolHold{{}}}},
		OrphanedHolds: []wire.Hold{{}},
	})
	ti := s.Turns["t1"]
	if ti.AuthorState != "running" || ti.ReviewerStates["r1"] != "queued" {
		t.Fatalf("roles = %+v", ti)
	}
	if p := s.Pools["default"]; p.Concurrency != 2 || len(p.Holds) != 1 || len(s.OrphanedHolds) != 1 {
		t.Fatalf("pools = %+v orphaned = %v", s.Pools, s.OrphanedHolds)
	}
}

func openTurnState(reviewers ...string) State {
	s := NewState()
	s.ConnState = ConnStateConnected
	s.CurrentTurn = "t1"
	states := map[string]string{}
	for _, r := range reviewers {
		states[r] = "queued"
	}
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	dl := t0.Add(15 * time.Minute)
	s.Turns["t1"] = TurnInfo{
		ID: "t1", State: "author_dispatched", Preset: "review", SubmittedAt: t0,
		TurnDeadline: &dl, DeadlineDur: 15 * time.Minute, EstimatedTokens: 1234, LimitTokens: 250000,
		Author: "author", AuthorState: "running", Reviewers: reviewers, ReviewerStates: states,
	}
	return s
}

func TestRenderStripFull(t *testing.T) {
	s := openTurnState("r1", "r2")
	now := s.Turns["t1"].SubmittedAt.Add(75 * time.Second)
	want := "[review] [author_dispatched] [1m15s / 15m0s] [░░░░░░░░ est. 1234 / 250000]"
	if got := renderStrip(s, 200, now, false); got != want {
		t.Fatalf("strip =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderStripQuickAndIdle(t *testing.T) {
	s := openTurnState()
	ti := s.Turns["t1"]
	ti.QuickProfile = ptr("fast")
	s.Turns["t1"] = ti
	if got := renderStrip(s, 200, ti.SubmittedAt, false); !strings.HasPrefix(got, "[review] [quick:fast] [author_dispatched] ") {
		t.Fatalf("strip = %q", got)
	}

	idle := NewState()
	idle.ConnState = ConnStateConnected
	idle.Status = &wire.StatusReply{DefaultPreset: "solo"}
	if got := renderStrip(idle, 80, time.Time{}); got != "[solo] [idle]" {
		t.Fatalf("idle strip = %q", got)
	}
}

func TestRenderStripEstimateIsLabelled(t *testing.T) {
	s := openTurnState()
	ti := s.Turns["t1"]
	ti.LimitTokens = 0
	s.Turns["t1"] = ti
	if got := renderStrip(s, 200, ti.SubmittedAt, false); !strings.Contains(got, "[est. 1234]") {
		t.Fatalf("strip = %q", got)
	}
}

func TestRenderStripLeavesParticipantsToTheProgressLine(t *testing.T) {
	s := openTurnState("reviewer-1", "reviewer-2", "reviewer-3", "reviewer-4", "reviewer-5")
	got := renderStrip(s, 200, s.Turns["t1"].SubmittedAt, false)
	if strings.Contains(got, "reviewer-") || strings.Contains(got, "author:") {
		t.Fatalf("strip carries participant states: %q", got)
	}
	for _, w := range []int{1, 5, 20, 40, 79} {
		if g := renderStrip(s, w, s.Turns["t1"].SubmittedAt); runewidth.StringWidth(g, false) > w || strings.Contains(g, "\n") {
			t.Fatalf("width %d: strip is %d cells: %q", w, runewidth.StringWidth(g), g)
		}
	}
}

func TestRenderBanners(t *testing.T) {
	s := NewState()
	s.ConnState = ConnStateConnected
	if b := renderBanners(s); len(b) != 0 {
		t.Fatalf("banners with nothing to say: %v", b)
	}
	s.Status = &wire.StatusReply{DeadlinesProvisional: true, RosterFileChanged: true}
	s.ConnState = ConnStateReconnecting
	s.UnapprovedProfile = ptr("fast")
	want := []string{
		"deadlines: provisional",
		"roster file changed since start; restart the engine to apply",
		"engine: reconnecting",
		"quick: unapproved fast",
	}
	if got := renderBanners(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("banners = %q, want %q", got, want)
	}
	// The reconnect notice is a banner only, not also the strip.
	if strings.Contains(renderStrip(s, 200, time.Time{}, false), "reconnecting") {
		t.Fatal("reconnecting shown in the strip as well")
	}
}

// engineWith starts an engine and a server over the harness's journal.
func startRig(t *testing.T, h *enginetest.Harness) *driver {
	t.Helper()
	paths := newPaths(t)
	startServer(t, paths, h.Engine, h)
	d := newDriver(t, paths.Socket)
	d.paths = paths
	d.until("connected", func(string) bool { return d.app.state.ConnState == ConnStateConnected && d.strip() != "" })
	return d
}

func TestLiveStripDuringReviewTurn(t *testing.T) {
	h := newHarness(t)
	releaseAuthor := h.Hold("author")
	releaseReviewer := h.Hold("reviewer")
	d := startRig(t, h)
	d.send(resizeMsg(200, 24))

	if !strings.HasPrefix(d.strip(), "[review] [idle]") {
		t.Fatalf("idle strip = %q", d.strip())
	}
	d.typeLine("question")
	d.until("author running with limit and deadline", func(string) bool {
		return d.sawStrip("[review] [author_dispatched] [", "/ 15m0s] [", "est. ", " / 250000]")
	})
	spinning := func(v, id string) bool {
		for _, f := range spinnerFrames {
			if strings.Contains(v, ", "+f+" "+id) {
				return true
			}
		}
		return false
	}
	d.until("the progress line to show the author working", func(v string) bool {
		return strings.Contains(v, " turn in progress: author_dispatched") && spinning(v, "author")
	})
	releaseAuthor()
	d.until("the author done and the reviewer working", func(v string) bool {
		return strings.Contains(v, "turn in progress: reviewers_dispatched") && strings.Contains(v, ", ✓ author, ") && spinning(v, "reviewer")
	})
	releaseReviewer()
	turn := d.app.state.CurrentTurn
	h.WaitTurnState(turn, engine.StateApproved)
	d.until("terminal reason from the turn reply", func(string) bool {
		return d.app.state.CurrentTurn == "" && d.app.state.Turns[turn].TerminalReason != nil
	})

	for _, want := range []string{"[submitted]", "[author_dispatched]", "[candidate_frozen]", "[reviewers_dispatched]"} {
		if !d.sawStrip(want) {
			t.Errorf("no strip showed %q; strips:\n%s", want, strings.Join(d.seen, "\n"))
		}
	}
	reply, err := dial(t, d.paths).Turn(context.Background(), turn)
	if err != nil {
		t.Fatal(err)
	}
	if reply.TerminalReason == nil {
		t.Fatal("the turn reply has no terminal reason")
	}
	if got := d.app.state.Turns[turn]; got.State != reply.State || *got.TerminalReason != *reply.TerminalReason {
		t.Fatalf("state holds %q/%q, the turn reply says %q/%q", got.State, *got.TerminalReason, reply.State, *reply.TerminalReason)
	}
	if !strings.HasPrefix(d.strip(), "[review] [idle]") {
		t.Fatalf("strip after the turn = %q", d.strip())
	}
}

func TestTickAdvancesElapsed(t *testing.T) {
	h := newHarness(t)
	defer h.Hold("author")()
	d := startRig(t, h)
	d.typeLine("question")
	d.until("author running", func(v string) bool { return strings.Contains(v, "turn in progress: author_dispatched") })
	// Nothing else happens while the author is held; only the tick can move
	// the elapsed time.
	d.until("elapsed to reach 2s", func(string) bool { return d.sawStrip("[2s / ") })
}

func TestProvisionalBanner(t *testing.T) {
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(answerText)}, "reviewer": {approve()}}
	h := enginetest.New(t, strings.Replace(enginetest.RosterTOML, "provisional = false", "provisional = true", 1), scripts)
	d := startRig(t, h)
	if !strings.Contains(d.app.View(), "deadlines: provisional") {
		t.Fatalf("view has no provisional banner:\n%s", d.app.View())
	}
	// The healthy roster shows none.
	h2 := newHarness(t)
	if d2 := startRig(t, h2); strings.Contains(d2.app.View(), "deadlines: provisional") {
		t.Fatal("provisional banner shown for non-provisional deadlines")
	}
}

func TestRosterChangedBanner(t *testing.T) {
	const banner = "roster file changed since start; restart the engine to apply"
	h := newHarness(t)
	d := startRig(t, h)
	if strings.Contains(d.app.View(), banner) {
		t.Fatal("banner shown before the roster was edited")
	}
	orig, err := os.ReadFile(h.RosterPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.RosterPath, append(append([]byte(nil), orig...), []byte("\n# edited\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	// A turn's participant events make the TUI refresh status.
	d.typeLine("first")
	d.until("roster banner", func(v string) bool { return strings.Contains(v, banner) })
	d.until("first turn over", func(string) bool { return d.app.state.CurrentTurn == "" && len(d.app.state.Turns) == 1 })

	if err := os.WriteFile(h.RosterPath, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	d.typeLine("second")
	d.until("banner cleared", func(v string) bool { return !strings.Contains(v, banner) && len(d.app.state.Turns) == 2 })
}

func TestReconnectBannerShowsThenClears(t *testing.T) {
	h := newHarness(t)
	paths := newPaths(t)
	srv := startServer(t, paths, h.Engine, h)
	d := newDriver(t, paths.Socket)
	d.until("connected", func(string) bool { return d.app.state.ConnState == ConnStateConnected })
	if strings.Contains(d.app.View(), "engine: reconnecting") {
		t.Fatal("reconnecting banner while connected")
	}

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-srv.done
	d.until("reconnecting banner", func(v string) bool { return strings.Contains(v, "engine: reconnecting") })
	if n := strings.Count(d.app.View(), "engine: reconnecting"); n != 1 {
		t.Fatalf("reconnecting shown %d times:\n%s", n, d.app.View())
	}

	eng, err := engine.New(engine.Options{
		Store: h.Store, Hub: h.Hub, RosterPath: h.RosterPath,
		EngineVersion: enginetest.EngineVersion, Clock: h.Clock,
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			return fake.New(nil, fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel}), enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	startServer(t, paths, eng, h)
	d.until("banner cleared", func(v string) bool {
		return d.app.state.ConnState == ConnStateConnected && !strings.Contains(v, "engine: reconnecting")
	})
}

// A reply fetched while the turn was open can land after newer events; it must
// not roll back what the events already established.
func TestMergeTurnStaleOpenReplyKeepsNewerEvents(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	s := NewState()
	s.CurrentTurn = "t1"
	cand := "hash1"
	s.Turns["t1"] = TurnInfo{ID: "t1", State: "candidate_frozen", Preset: "review", SubmittedAt: t0, CandidateHash: &cand, EstimatedTokens: 9000}
	s.Attempts["a1"] = AttemptInfo{ID: "a1", TurnID: "t1", Status: "complete", ScrubCount: 2, VerdictClass: "valid", VerdictParsed: true}
	s.Blocks["b1"] = BlockInfo{ID: "b1", Severity: "major", State: "resolved"}
	s.Verdicts["v1"] = VerdictInfo{ID: "v1", AttemptID: "a1", Parsed: true, Class: "valid"}

	s = MergeTurn(s, "t1", &wire.TurnReply{
		State:                 "author_dispatched",
		Preset:                "review",
		EstimatedPromptTokens: 500,
		Attempts:              []wire.AttemptRecord{{ID: "a1", Status: "in_progress", Phase: "author"}, {ID: "a2", Status: "in_progress", Phase: "review", Participant: "r1"}},
		Verdicts:              []wire.VerdictRecord{{ID: "v1", AttemptID: "a1", Parsed: false, ValidationStatus: "invalid"}},
		OpenBlocks:            []wire.OpenBlock{{ID: "b1", Severity: "major", State: "open"}},
		Deadlines:             wire.TurnDeadlines{Turn: 900000},
		Budget:                wire.BudgetRecord{Limit: ptr(int64(5000))},
	}, t0)

	ti := s.Turns["t1"]
	if ti.State != "candidate_frozen" || ti.CandidateHash == nil || *ti.CandidateHash != "hash1" {
		t.Errorf("turn rolled back: %+v", ti)
	}
	if a := s.Attempts["a1"]; a.Status != "complete" || a.ScrubCount != 2 || a.VerdictClass != "valid" || !a.VerdictParsed {
		t.Errorf("attempt a1 rolled back: %+v", a)
	}
	if ti.EstimatedTokens != 9000 {
		t.Errorf("estimate rolled back: %d", ti.EstimatedTokens)
	}
	if _, ok := s.Attempts["a2"]; !ok {
		t.Errorf("unknown attempt a2 was not added")
	}
	if s.Blocks["b1"].State != "resolved" {
		t.Errorf("block rolled back: %+v", s.Blocks["b1"])
	}
	if s.Verdicts["v1"].Class != "valid" {
		t.Errorf("verdict rolled back: %+v", s.Verdicts["v1"])
	}
	if ti.LimitTokens != 5000 || ti.DeadlineDur != 15*time.Minute {
		t.Errorf("limit/deadline not supplied: %d %v", ti.LimitTokens, ti.DeadlineDur)
	}
	if got := ti.Remaining(t0.Add(5 * time.Minute)); got != 10*time.Minute {
		t.Errorf("Remaining = %v", got)
	}
}

// TestLiveProgressMarksPoolQueuedReviewer: with two reviewers sharing a pool of
// one slot, the engine queues the second before it has an attempt, and the
// progress line marks it queued beside the first one working.
func TestLiveProgressMarksPoolQueuedReviewer(t *testing.T) {
	roster := strings.Replace(enginetest.RosterTOML, `required_reviewers = ["reviewer"]`, `required_reviewers = ["reviewer", "second"]`, 1)
	at := strings.Index(roster, "[[endpoints]]")
	roster = roster[:at] + "[[participants]]\nid = \"second\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \"default\"\naccept_unknown_truncation = true\n\n" + roster[at:] + "\n[pools.default]\nconcurrency = 1\n"
	h := enginetest.New(t, roster, map[string][]fake.Step{
		"author":   {enginetest.Reply(answerText)},
		"reviewer": {approve()},
		"second":   {approve()},
	})
	release := h.Hold("reviewer")
	d := startRig(t, h)
	d.send(resizeMsg(200, 24))
	d.typeLine("question")
	d.until("the first reviewer working and the second queued", func(v string) bool {
		for _, f := range spinnerFrames {
			if strings.Contains(v, ", ✓ author, "+f+" reviewer, ○ second, ") {
				return true
			}
		}
		return false
	})
	release()
	h.WaitTurnState(d.app.state.CurrentTurn, engine.StateApproved)
}
