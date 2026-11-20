package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// These tests run the App against a real engine with fake adapters, driving
// it message by message, and read what the answer tab shows through View.

func blockVerdict() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b7", "medium", "line 3 divides by zero", "guard the divisor", "unsafe")
	})
}

func scripted(t *testing.T, author, reviewer fake.Step, replace ...string) *enginetest.Harness {
	t.Helper()
	roster := enginetest.RosterTOML
	for i := 0; i+1 < len(replace); i += 2 {
		roster = strings.Replace(roster, replace[i], replace[i+1], 1)
	}
	return enginetest.New(t, roster, map[string][]fake.Step{"author": {author}, "reviewer": {reviewer}})
}

func (d *driver) tab() string { return d.app.View() }

func (d *driver) submit(req wire.SubmitRequest) string {
	d.t.Helper()
	reply, err := dial(d.t, d.paths).Submit(context.Background(), req)
	if err != nil {
		d.t.Fatal(err)
	}
	return reply.TurnID
}

func (d *driver) viewHas(parts ...string) func(string) bool {
	return func(view string) bool {
		for _, p := range parts {
			if !strings.Contains(view, p) {
				return false
			}
		}
		return true
	}
}

// openReviewTurn types a prompt and waits until the reviewers have the candidate.
func (d *driver) openReviewTurn() string {
	d.t.Helper()
	d.typeLine("question")
	d.until("reviewers dispatched", func(string) bool { return d.sawStrip("[reviewers_dispatched]") })
	return d.app.state.CurrentTurn
}

func TestAnswerOpenTurnShowsNoAuthorText(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	release := h.Hold("reviewer")
	d := startRig(t, h)
	d.openReviewTurn()
	d.until("hint", d.viewHas("turn in progress"))
	if v := d.tab(); strings.Contains(v, answerText) || strings.Contains(v, "UNREVIEWED DRAFT") {
		t.Fatalf("author text shown during an open turn:\n%s", v)
	}
	release()
	d.until("approved", d.viewHas(answerText, "approved by"))
}

func TestAnswerApproved(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	d := startRig(t, h)
	d.typeLine("question")
	d.until("approved line", d.viewHas("approved by"))
	h.WaitTurnState(latest(d), engine.StateApproved)
	reply, err := dial(t, d.paths).Turn(context.Background(), latest(d))
	if err != nil || reply.CandidateHash == nil {
		t.Fatalf("turn reply %+v, %v", reply, err)
	}
	want := "approved by reviewer against " + (*reply.CandidateHash)[:12]
	d.until("candidate text and approval line", d.viewHas(answerText, want))
	if v := d.tab(); strings.Index(v, answerText) > strings.Index(v, "approved by") {
		t.Fatalf("approval line precedes the text:\n%s", v)
	}
}

// latest is the most recently submitted turn the App knows.
func latest(d *driver) string { return answerTurn(d.app.state) }

func TestAnswerSoloIsMarkedUnreviewed(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	d := startRig(t, h)
	solo := "solo"
	d.submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q", Preset: &solo})
	d.until("solo answer", d.viewHas("UNREVIEWED (solo)", answerText))
	v := d.tab()
	if strings.Index(v, "UNREVIEWED (solo)") > strings.Index(v, answerText) {
		t.Fatalf("banner does not precede the text:\n%s", v)
	}
	if strings.Contains(v, "approved") {
		t.Fatalf("solo answer mentions approval:\n%s", v)
	}
}

func TestAnswerBlockEndsIncomplete(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	d := startRig(t, h)
	d.typeLine("question")
	d.until("incomplete banner", d.viewHas("INCOMPLETE, not approved", answerText))
	v := d.tab()
	for _, want := range []string{"block:", "[medium]", "line 3 divides by zero", "guard the divisor"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "approved by") {
		t.Fatalf("a blocked turn shows an approval line:\n%s", v)
	}
}

func TestAnswerAuthorFailureShowsReasonAndNoCandidate(t *testing.T) {
	h := scripted(t, fake.Step{Kind: fake.StepKindReply, Text: "cut off", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}, approve())
	d := startRig(t, h)
	d.typeLine("question")
	d.until("reason", d.viewHas("author_failed:truncated"))
	v := d.tab()
	if strings.Contains(v, "cut off") || strings.Contains(v, "INCOMPLETE, not approved") || strings.Contains(v, "approved by") {
		t.Fatalf("a turn without a candidate shows candidate text or banners:\n%s", v)
	}
}

func TestAnswerOverriddenShowsHumanReason(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	d := startRig(t, h)
	d.typeLine("question")
	d.until("incomplete", d.viewHas("INCOMPLETE, not approved"))
	turn := latest(d)
	if err := dial(t, d.paths).Override(context.Background(), turn, "accepted operational risk"); err != nil {
		t.Fatal(err)
	}
	d.until("override", d.viewHas("HUMAN OVERRIDE: accepted operational risk", answerText))
	// The terminal turn reply must not replace the reason with the literal "override".
	h.WaitTurnState(turn, engine.StateOverridden)
	d.send(tickMsg{})
	if reply, err := dial(t, d.paths).Turn(context.Background(), turn); err != nil || reply.TerminalReason == nil {
		t.Fatalf("turn reply %+v, %v", reply, err)
	} else {
		d.send(turnMsg{gen: d.app.gen, id: turn, turn: &reply})
	}
	if v := d.tab(); !strings.Contains(v, "HUMAN OVERRIDE: accepted operational risk") || strings.Contains(v, "INCOMPLETE") {
		t.Fatalf("override reason lost: reasons=%v turn=%s answerTurn=%s turns=%d\n%s", d.app.overrideReasons, turn, latest(d), len(d.app.state.Turns), v)
	}
}

func TestAnswerCancelledDuringAuthor(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("author")()
	d := startRig(t, h)
	d.typeLine("question")
	d.until("author running", func(string) bool { return d.sawStrip("author:author running") })
	if err := dial(t, d.paths).Cancel(context.Background(), d.app.state.CurrentTurn); err != nil {
		t.Fatal(err)
	}
	d.until("cancelled", d.viewHas("cancelled before candidate"))
	if v := d.tab(); strings.Contains(v, "CANCELLED, not approved") || strings.Contains(v, answerText) {
		t.Fatalf("cancel before a candidate shows a candidate:\n%s", v)
	}
}

func TestAnswerCancelledDuringReview(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("reviewer")()
	d := startRig(t, h)
	turn := d.openReviewTurn()
	if err := dial(t, d.paths).Cancel(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	d.until("cancelled with candidate", d.viewHas("CANCELLED, not approved", answerText))
	if v := d.tab(); strings.Contains(v, "approved by") || strings.Contains(v, "cancelled before candidate") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestAnswerDraftsOnWhileReviewersRun(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	release := h.Hold("reviewer")
	d := startRig(t, h)
	d.app.SetDraftsEnabled(true)
	d.openReviewTurn()
	d.until("draft", d.viewHas("UNREVIEWED DRAFT", answerText))
	release()
	d.until("approved", d.viewHas("approved by"))
	if v := d.tab(); strings.Contains(v, "UNREVIEWED DRAFT") {
		t.Fatalf("draft banner survives the terminal state:\n%s", v)
	}
}

func TestAnswerDraftsToggledOnMidTurn(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("reviewer")()
	d := startRig(t, h)
	d.openReviewTurn()
	if strings.Contains(d.tab(), answerText) {
		t.Fatalf("author text shown with drafts off:\n%s", d.tab())
	}
	d.app.SetDraftsEnabled(true)
	d.send(tickMsg{})
	d.until("draft after toggling", d.viewHas("UNREVIEWED DRAFT", answerText))
}

const draftsQuickRoster = "[quick.fast]\npreset = \"solo\"\ndrafts = false"

func TestAnswerQuickProfileDrafts(t *testing.T) {
	// A quick profile on the review preset with drafts = true.
	on := "[quick.fast]\npreset = \"review\"\ndrafts = true"
	h := scripted(t, enginetest.Reply(answerText), approve(), draftsQuickRoster, on)
	release := h.Hold("reviewer")
	d := startRig(t, h)
	quick := "fast"
	d.submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q", QuickProfile: &quick})
	d.until("draft from the profile", d.viewHas("UNREVIEWED DRAFT", answerText))
	release()
	d.until("terminal", d.viewHas("approved by"))
}

func TestAnswerQuickProfileDraftsOffByUser(t *testing.T) {
	on := "[quick.fast]\npreset = \"review\"\ndrafts = true"
	h := scripted(t, enginetest.Reply(answerText), approve(), draftsQuickRoster, on)
	defer h.Hold("reviewer")()
	d := startRig(t, h)
	d.app.SetDraftsEnabled(false)
	quick := "fast"
	d.submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q", QuickProfile: &quick})
	d.until("reviewers dispatched", func(string) bool { return d.sawStrip("[reviewers_dispatched]") })
	d.until("hint", d.viewHas("turn in progress"))
	if strings.Contains(d.tab(), answerText) {
		t.Fatalf("draft shown although the user turned drafts off:\n%s", d.tab())
	}
}

func TestAnswerLongCandidateScrollsToTheLastLine(t *testing.T) {
	var text strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&text, "line %d\n", i)
	}
	h := scripted(t, enginetest.Reply(strings.TrimRight(text.String(), "\n")), approve())
	d := startRig(t, h)
	d.typeLine("question")
	d.until("first line", d.viewHas("line 0\n"))

	key := func(k tea.KeyType) { d.send(tea.KeyMsg{Type: k}) }
	seen := map[string]bool{}
	record := func() {
		for _, l := range strings.Split(d.tab(), "\n") {
			seen[strings.TrimSpace(l)] = true
		}
	}
	record()
	for i := 0; i < 600 && !seen["line 4999"]; i++ {
		key(tea.KeyPgDown)
		record()
	}
	for i := 0; i < 5000; i++ {
		if !seen[fmt.Sprintf("line %d", i)] {
			t.Fatalf("line %d was never visible", i)
		}
	}
	// Past the end: the last line and the approval line stay, nothing panics,
	// and Up still moves at once.
	for i := 0; i < 50; i++ {
		key(tea.KeyDown)
	}
	end := d.tab()
	if !strings.Contains(end, "approved by") {
		t.Fatalf("end of the text lacks the approval line:\n%s", end)
	}
	key(tea.KeyUp)
	if d.tab() == end {
		t.Fatal("Up after over-scrolling did not move")
	}
	for i := 0; i < 1000; i++ {
		key(tea.KeyPgUp)
	}
	if !strings.Contains(d.tab(), "line 0\n") {
		t.Fatalf("PgUp did not return to the top:\n%s", d.tab())
	}
}

func TestContentCacheRetriesFailuresOnly(t *testing.T) {
	cc := NewContentCache()
	if !cc.Need("a") || cc.Need("a") {
		t.Fatal("a ref is fetched once while in flight")
	}
	cc.Store("a", "", context.Canceled)
	if !cc.Need("a") {
		t.Fatal("a failed fetch is not retried")
	}
	cc.Store("a", "text", nil)
	if cc.Need("a") || cc.Get("a").Data != "text" {
		t.Fatal("a successful fetch is fetched again")
	}
}
