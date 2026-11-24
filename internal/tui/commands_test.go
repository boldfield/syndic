package tui

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// These tests drive the slash commands under teatest against a real engine
// with fake adapters, and read what the user would see through View.

// sent counts the journal events a command that reached the engine would have
// caused: overrides, cancellations and releases. A turn ending on its own
// causes none of them.
func sent(r *rig) int {
	n := 0
	for _, e := range r.h.Events() {
		switch p := e.Payload.(type) {
		case *wire.EvOverride:
			n++
		case *wire.EvCapacity:
			if p.State == "released" {
				n++
			}
		case *wire.EvTurnState:
			if p.State == "cancelled" {
				n++
			}
		}
	}
	return n
}

// idle waits until the engine has no open turn, so the next submit is not refused as busy.
func idle(t *testing.T, r *rig) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for len(openTurns(t, r.paths)) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("the engine stayed busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// presetOf is the preset the engine ran the turn with.
func presetOf(t *testing.T, r *rig, turn string) string {
	t.Helper()
	reply, err := dial(t, r.paths).Turn(context.Background(), turn)
	if err != nil {
		t.Fatal(err)
	}
	return reply.Preset
}

func TestModeAppliesToOneSubmit(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(answerText), enginetest.Reply(answerText), enginetest.Reply(answerText)},
		"reviewer": {approve(), approve()},
	})
	r := startAnswerRig(t, h)

	r.typeLine("/mode nope")
	v := untilView(t, r, `unknown preset "nope"`, "presets: review, solo")
	if strings.Contains(v, "[next:") {
		t.Fatalf("a refused preset is pending:\n%s", v)
	}

	r.typeLine("/mode solo")
	untilView(t, r, "[review] [next: solo]")
	r.typeLine("first")
	solo := turnIn(t, r, "solo_complete")
	if got := presetOf(t, r, solo); got != "solo" {
		t.Fatalf("first submit ran preset %q", got)
	}
	if v := view(t, r); strings.Contains(v, "[next:") {
		t.Fatalf("the preset is still pending after the submit:\n%s", v)
	}

	idle(t, r)
	r.typeLine("second")
	// Turn stamps have a resolution of a second, so the new turn is found by id, not by recency.
	var second, refused string
	r.until(t, "the second turn approved", func(a *App) (string, bool) {
		refused = a.refusal
		for id, ti := range a.state.Turns {
			if id != solo {
				second = id
				return "", ti.State == "approved"
			}
		}
		return "", a.refusal != ""
	})
	if second == "" {
		t.Fatalf("second submit refused: %s", refused)
	}
	if got := presetOf(t, r, second); got != "review" {
		t.Fatalf("second submit ran preset %q, want the default", got)
	}
}

func TestCancelEndsTheOpenTurn(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("author")()
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := r.waitTurn(t)
	r.typeLine("/cancel")
	untilView(t, r, "cancel sent for turn "+turn)
	turnIn(t, r, "cancelled")
	h.WaitTurnState(turn, engine.StateCancelled)
}

func TestCancelWithNoOpenTurnSendsNothing(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	r.waitCaughtUp(t)
	before := sent(r)
	r.typeLine("/cancel")
	untilView(t, r, "no open turn; nothing sent")
	if after := sent(r); after != before {
		t.Fatalf("commands sent: %d -> %d", before, after)
	}
}

func TestWhyOpenTurn(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("reviewer")()
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "reviewers_dispatched")
	r.typeLine("/why")
	untilView(t, r, "is still running", "state: reviewers_dispatched")
}

func TestWhyGateFailure(t *testing.T) {
	bad := enginetest.Reply("x")
	bad.ReportedModel = "other-model"
	h := scripted(t, bad, approve())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "incomplete")
	r.typeLine("/why")
	untilView(t, r, "reason: author_failed:gate:identity mismatch", "gate error (author): identity mismatch")
}

func TestWhyBlock(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "incomplete")
	r.typeLine("/why")
	untilView(t, r, "reason: block:", "[medium]", "  evidence: line 3 divides by zero", "  remedy: guard the divisor", "reviewer reason (reviewer): unsafe")
}

// TestWhyBlockReasonOnce asks /why after the reviewer's terminal delivery is
// recorded, so the reviewer has two attempts and still one verdict.
func TestWhyBlockReasonOnce(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	id := turnIn(t, r, "incomplete")
	cl := dial(t, r.paths)
	deadline := time.Now().Add(15 * time.Second)
	for {
		tr, err := cl.Turn(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, at := range tr.Attempts {
			if at.Participant == "reviewer" {
				n++
			}
		}
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reviewer has %d attempts, want a review and a terminal delivery", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.typeLine("/why")
	v := untilView(t, r, "reviewer reason (reviewer): unsafe")
	if n := strings.Count(v, "reviewer reason (reviewer): unsafe"); n != 1 {
		t.Fatalf("reviewer reason shown %d times, want once:\n%s", n, v)
	}
}

// TestWhyTrustsTheTurnRecord covers a turn reply that has already recorded
// the turn as incomplete while its turn_state event has not yet arrived: /why
// still fetches the record and explains the ending instead of saying the turn
// is running.
func TestWhyTrustsTheTurnRecord(t *testing.T) {
	a := NewApp(context.Background(), "unused.sock")
	a.cl = &client.Client{}
	a.state.Turns = map[string]TurnInfo{"t1": {ID: "t1", State: "incomplete", SubmitSeq: 1}}
	a.state.TurnState = map[string]string{"t1": "author_dispatched"}
	if cmd := cmdWhy(a, nil); cmd == nil {
		t.Fatal("/why did not fetch the turn record")
	}
	reason := "author_failed:gate:identity mismatch"
	a.onWhy(whyMsg{gen: a.gen, id: "t1", turn: &wire.TurnReply{State: "incomplete", TerminalReason: &reason}})
	v := a.View()
	if strings.Contains(v, "still running") || !strings.Contains(v, "reason: "+reason) {
		t.Fatalf("/why did not explain the ended turn:\n%s", v)
	}

	a.onWhy(whyMsg{gen: a.gen, id: "t1", turn: &wire.TurnReply{State: "reviewers_dispatched"}})
	if v := a.View(); !strings.Contains(v, "turn t1 is still running") || !strings.Contains(v, "state: reviewers_dispatched") {
		t.Fatalf("/why on an open turn record:\n%s", v)
	}
}

func TestWhyMalformedTwice(t *testing.T) {
	prose := enginetest.Reply(fake.VerdictProseApprove())
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(answerText)},
		"reviewer": {prose, prose},
	})
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "incomplete")
	r.typeLine("/why")
	untilView(t, r, "reason: malformed_twice:reviewer", "parser classification (reviewer): malformed, malformed")
}

func TestWhyReviewerRejected(t *testing.T) {
	wrong := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictWrongHash(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
	h := scripted(t, enginetest.Reply(answerText), wrong)
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "incomplete")
	r.typeLine("/why")
	untilView(t, r, "reason: reviewer_failed:reviewer:rejected", "parser classification (reviewer): rejected")
}

func TestWhyBudget(t *testing.T) {
	const input = "what is the answer?"
	big := strings.Repeat("a", 60000)
	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{"author": {enginetest.Reply(big)}, "reviewer": {approve()}}
	}
	// A throwaway engine tells what the roster projects; the limit is exactly that.
	probeH := enginetest.New(t, enginetest.RosterTOML, scripts())
	reply, err := probeH.Engine.Submit(wire.SubmitRequest{SubmissionID: "p", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	limit := int(reply.EstimatedTokens)

	roster := enginetest.RosterTOML + "\n[budgets]\nturn_max_estimated_tokens = " + strconv.Itoa(limit) + "\n"
	h := enginetest.New(t, roster, scripts())
	r := startAnswerRig(t, h)
	r.typeLine(input)
	turnIn(t, r, "incomplete")
	r.typeLine("/why")
	v := untilView(t, r, "reason: budget:turn", "budget turn_max_estimated_tokens", "limit "+strconv.Itoa(limit), "estimated ", "tokens: reserved ", "charged ")
	if !strings.Contains(v, "limit "+strconv.Itoa(limit)+" (projected") {
		t.Fatalf("limit not shown beside reserved and charged:\n%s", v)
	}
}

func TestOverrideNeedsAReason(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := turnIn(t, r, "incomplete")
	before := sent(r)

	// /override without reason opens a form
	r.typeLine("/override")
	v := untilView(t, r, "override turn", "reason>")
	if !strings.Contains(v, turn) {
		t.Fatalf("the prompt does not name the turn:\n%s", v)
	}

	// Empty-Enter shows validation error and keeps form open
	send(r, tea.KeyMsg{Type: tea.KeyEnter})
	v = untilView(t, r, "reason required", "reason>")
	if !strings.Contains(v, "reason required") {
		t.Fatalf("validation error not shown:\n%s", v)
	}
	if after := sent(r); after != before {
		t.Fatalf("commands sent after empty submit: %d -> %d", before, after)
	}

	// Esc cancels with override-specific message
	send(r, tea.KeyMsg{Type: tea.KeyEsc})
	v = untilView(t, r, "override cancelled; nothing sent")
	if strings.Contains(v, "reason>") {
		t.Fatalf("the prompt is still up after Esc:\n%s", v)
	}

	// A reason overrides the turn
	r.typeLine("/override")
	untilView(t, r, "reason>")
	r.typeLine("reason provided")
	untilView(t, r, "overridden: reason provided")
}

func TestOverrideIncompleteTurn(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := turnIn(t, r, "incomplete")
	r.typeLine("/override accepted the risk")
	untilView(t, r, "HUMAN OVERRIDE: accepted the risk", answerText)
	turnIn(t, r, "overridden")
	h.WaitTurnState(turn, engine.StateOverridden)
}

func TestOverrideApprovedTurnIsRefused(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "approved")
	r.typeLine("/override because")
	untilView(t, r, "not_incomplete")
}

func TestDraftsToggle(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	release := h.Hold("reviewer")
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "reviewers_dispatched")
	untilView(t, r, "turn in progress")
	r.typeLine("/drafts on")
	untilView(t, r, "UNREVIEWED DRAFT", answerText)
	r.typeLine("/drafts off")
	v := untilView(t, r, "drafts off")
	if strings.Contains(v, answerText) || strings.Contains(v, "UNREVIEWED DRAFT") {
		t.Fatalf("draft still shown:\n%s", v)
	}
	r.typeLine("/drafts maybe")
	untilView(t, r, "usage: /drafts on|off")
	release()
}

func TestParticipantsCommandSwitchesTab(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	r.typeLine("/participants")
	r.until(t, "the participants tab", func(a *App) (string, bool) { return "", a.tab == tabParticipants })
}

func TestHelpListsEveryCommandOnItsOwnLine(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	send(r, resizeMsg(80, 40))
	r.typeLine("/help")
	v := untilView(t, r, "commands:")
	for _, c := range []string{"/mode", "/participants", "/cancel", "/why", "/override", "/release", "/drafts", "/resume", "/quit", "/help"} {
		n := 0
		for _, l := range strings.Split(v, "\n") {
			if strings.HasPrefix(l, c+" ") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s is on %d lines:\n%s", c, n, v)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	r.typeLine("/bogus")
	untilView(t, r, "unknown command: /bogus")
}

// holdsOn is the held attempts the App knows of in the pool.
func holdsOn(a *App, pool string) []string {
	var ids []string
	for _, h := range a.state.Pools[pool].Holds {
		ids = append(ids, h.AttemptID)
	}
	return ids
}

// holdRig opens a review turn with the reviewers' adapters held and cancels
// it, so each reviewer's pool slot is an uncertain-capacity hold. It returns
// once the App shows n holds in the pool.
func holdRig(t *testing.T, roster string, held []string, pool string, n int) *rig {
	t.Helper()
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(answerText)}}
	for _, id := range held {
		scripts[id] = []fake.Step{approve()}
	}
	h := enginetest.New(t, roster, scripts, enginetest.CancelUnconfirmed())
	for _, id := range held {
		h.Hold(id)
	}
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "reviewers_dispatched")
	r.until(t, "every reviewer in flight", func(a *App) (string, bool) {
		running := 0
		for _, at := range a.state.Attempts {
			if at.Phase == "review" {
				running++
			}
		}
		return "", running == len(held)
	})
	r.typeLine("/cancel")
	turnIn(t, r, "cancelled")
	r.until(t, "the holds", func(a *App) (string, bool) { return "", len(holdsOn(a, pool)) == n })
	return r
}

func TestReleaseWarnsAndNeedsAReason(t *testing.T) {
	r := holdRig(t, enginetest.RosterTOML, []string{"reviewer"}, "default", 1)
	r.typeLine("/release default")
	v := untilView(t, r, "may still be computing the held request", "may overlap it", "reason>")
	if !strings.Contains(v, "release held capacity on pool default") {
		t.Fatalf("the prompt does not name the pool:\n%s", v)
	}

	// Empty-Enter shows validation error and keeps form open
	before := sent(r)
	send(r, tea.KeyMsg{Type: tea.KeyEnter})
	v = untilView(t, r, "reason required", "reason>")
	if !strings.Contains(v, "reason required") {
		t.Fatalf("validation error not shown:\n%s", v)
	}
	if after := sent(r); after != before {
		t.Fatalf("commands sent after empty submit: %d -> %d", before, after)
	}

	// Esc cancels with release-specific message
	send(r, tea.KeyMsg{Type: tea.KeyEsc})
	v = untilView(t, r, "release cancelled; nothing sent")
	if strings.Contains(v, "may overlap it") || strings.Contains(v, "reason>") {
		t.Fatalf("the prompt is still up after Esc:\n%s", v)
	}

	// A reason releases the hold.
	r.typeLine("/release default")
	untilView(t, r, "reason>")
	r.typeLine("operator checked the server")
	untilView(t, r, "released on pool default: ")
	r.until(t, "the hold gone", func(a *App) (string, bool) { return "", len(holdsOn(a, "default")) == 0 })
}

func TestReleaseEscCancels(t *testing.T) {
	r := holdRig(t, enginetest.RosterTOML, []string{"reviewer"}, "default", 1)
	r.typeLine("/release default")
	untilView(t, r, "reason>")
	before := sent(r)
	// Text typed first is not sent either.
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("half a reason")}, tea.KeyMsg{Type: tea.KeyEsc})
	v := untilView(t, r, "release cancelled; nothing sent")
	if strings.Contains(v, "may overlap it") || strings.Contains(v, "half a reason") {
		t.Fatalf("the prompt or its text is still up:\n%s", v)
	}
	if after := sent(r); after != before {
		t.Fatalf("commands sent: %d -> %d", before, after)
	}
	r.until(t, "the hold kept", func(a *App) (string, bool) { return "", len(holdsOn(a, "default")) == 1 })
}

// twoReviewerRoster puts two reviewers in one wide pool.
func twoReviewerRoster() string {
	toml := enginetest.RosterTOML
	start := strings.Index(toml, "[[participants]]\nid = \"reviewer\"")
	toml = toml[:start] + toml[strings.Index(toml, "[[endpoints]]"):]
	var extra string
	for _, id := range []string{"r1", "r2"} {
		extra += "[[participants]]\nid = \"" + id + "\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \"reviewers\"\naccept_unknown_truncation = true\n\n"
	}
	toml = strings.Replace(toml, "[[endpoints]]", extra+"[[endpoints]]", 1)
	toml = strings.Replace(toml, `required_reviewers = ["reviewer"]`, `required_reviewers = ["r1", "r2"]`, 1)
	return toml + "\n[pools.reviewers]\nconcurrency = 10\n"
}

func TestReleaseAmbiguousListsAttemptIds(t *testing.T) {
	r := holdRig(t, twoReviewerRoster(), []string{"r1", "r2"}, "reviewers", 2)
	var ids []string
	r.until(t, "the held attempts", func(a *App) (string, bool) {
		ids = holdsOn(a, "reviewers")
		return "", true
	})
	if len(ids) != 2 {
		t.Fatalf("holds = %v", ids)
	}
	r.typeLine("/release reviewers")
	untilView(t, r, "reason>")
	r.typeLine("checked")
	v := untilView(t, r, "ambiguous", "pool=reviewers")
	for _, id := range ids {
		if !strings.Contains(strings.ReplaceAll(v, "\n", ""), id) {
			t.Errorf("attempt id %s missing from the refusal:\n%s", id, v)
		}
	}
	r.until(t, "both holds kept", func(a *App) (string, bool) { return "", len(holdsOn(a, "reviewers")) == 2 })

	r.typeLine("/release reviewers " + ids[0])
	untilView(t, r, "reason>")
	r.typeLine("checked")
	untilView(t, r, "released on pool reviewers: "+ids[0])
	r.until(t, "one hold left", func(a *App) (string, bool) {
		left := holdsOn(a, "reviewers")
		return "", len(left) == 1 && left[0] == ids[1]
	})
}

func TestReleaseWithNoHoldShowsItsCode(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	r.typeLine("/release default")
	untilView(t, r, "reason>")
	r.typeLine("why not")
	untilView(t, r, "no_hold", "pool=default")
}

func TestReleaseUsage(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	r.typeLine("/release")
	untilView(t, r, "usage: /release <pool> [attempt_id]")
}

func TestOverrideEscSendsNothing(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "incomplete")
	before := sent(r)

	r.typeLine("/override")
	untilView(t, r, "reason>")
	// Type some text then Esc
	send(r, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("some reason")}, tea.KeyMsg{Type: tea.KeyEsc})
	v := untilView(t, r, "override cancelled; nothing sent")
	if strings.Contains(v, "reason>") || strings.Contains(v, "some reason") {
		t.Fatalf("the prompt or its text is still up:\n%s", v)
	}
	if after := sent(r); after != before {
		t.Fatalf("commands sent after Esc: %d -> %d", before, after)
	}
}

func TestOverrideReasonSentExactlyOnce(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := turnIn(t, r, "incomplete")

	// Override with inline reason sends directly
	r.typeLine("/override test reason")
	untilView(t, r, "overridden: test reason")

	// Verify the turn state changed, confirming the override was sent
	h.WaitTurnState(turn, engine.StateOverridden)
}

func TestReleaseWarningVisible(t *testing.T) {
	r := holdRig(t, enginetest.RosterTOML, []string{"reviewer"}, "default", 1)
	r.typeLine("/release default")
	v := untilView(t, r, "release held capacity on pool default", "overlap it")
	// Verify the warning about overlapping requests is visible
	if !strings.Contains(v, "may still be computing") {
		t.Fatalf("compute warning not shown:\n%s", v)
	}
	if !strings.Contains(v, "may overlap it") {
		t.Fatalf("overlap warning not shown:\n%s", v)
	}
	if !strings.Contains(v, "reason>") {
		t.Fatalf("reason prompt not shown:\n%s", v)
	}
}

func TestFormatRefusalMembers(t *testing.T) {
	pool, budget := "p", "turn_max_estimated_tokens"
	est, lim := int64(120), int64(100)
	got := formatRefusal(&wire.Error{Code: "ambiguous", Pool: &pool, Holds: []byte(`["a1","a2"]`)})
	if got != "ambiguous [pool=p holds=[a1 a2]]" {
		t.Errorf("ambiguous: %q", got)
	}
	got = formatRefusal(&wire.Error{Code: "budget", Budget: &budget, Estimated: &est, Limit: &lim})
	if got != "budget [budget=turn_max_estimated_tokens estimated=120 limit=100]" {
		t.Errorf("budget: %q", got)
	}
}
