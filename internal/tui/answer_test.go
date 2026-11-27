package tui

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// These tests run the App under teatest against a real engine with fake
// adapters, and read what the answer tab shows through View.

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

// startAnswerRig serves the harness and runs an App on it under teatest.
func startAnswerRig(t *testing.T, h *enginetest.Harness) *rig {
	t.Helper()
	paths := newPaths(t)
	r := &rig{h: h, paths: paths, seen: &seen{}}
	r.srv = startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, paths.Socket)
	r.tm = teatest.NewTestModel(t, &probe{app: r.app}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })
	r.waitConnected(t)
	return r
}

// viewHas is a condition on the App's View.
func viewHas(parts ...string) func(*App) (string, bool) {
	return func(a *App) (string, bool) {
		v := a.View()
		for _, p := range parts {
			if !strings.Contains(v, p) {
				return v, false
			}
		}
		return v, true
	}
}

// untilView waits for the View to hold every part and returns it.
func untilView(t *testing.T, r *rig, parts ...string) string {
	t.Helper()
	return r.until(t, fmt.Sprintf("a view with %q", parts), viewHas(parts...))
}

// view is the View after every message sent so far.
func view(t *testing.T, r *rig) string {
	t.Helper()
	return r.until(t, "the view", func(a *App) (string, bool) { return a.View(), true })
}

func send(r *rig, msgs ...tea.Msg) {
	for _, m := range msgs {
		r.tm.Send(m)
	}
}

// turnIn waits for a turn in one of the states and returns its id.
func turnIn(t *testing.T, r *rig, state string) string {
	t.Helper()
	return r.until(t, "a turn in "+state, func(a *App) (string, bool) {
		id := answerTurn(a.state)
		// The submit reply clears the input; a following typeLine must not append to the question.
		return id, id != "" && a.state.Turns[id].State == state && a.input.Value() == ""
	})
}

func submit(t *testing.T, r *rig, req wire.SubmitRequest) {
	t.Helper()
	if _, err := dial(t, r.paths).Submit(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

// openReviewTurn types a prompt and waits until the reviewers have the candidate.
func openReviewTurn(t *testing.T, r *rig) string {
	t.Helper()
	r.typeLine("question")
	return turnIn(t, r, "reviewers_dispatched")
}

func TestAnswerOpenTurnShowsNoAuthorText(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	release := h.Hold("reviewer")
	r := startAnswerRig(t, h)
	openReviewTurn(t, r)
	if v := untilView(t, r, "turn in progress"); strings.Contains(v, answerText) || strings.Contains(v, "UNREVIEWED DRAFT") {
		t.Fatalf("author text shown during an open turn:\n%s", v)
	}
	release()
	untilView(t, r, answerText, "approved by")
}

func TestAnswerApproved(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := turnIn(t, r, "approved")
	h.WaitTurnState(turn, engine.StateApproved)
	reply, err := dial(t, r.paths).Turn(context.Background(), turn)
	if err != nil || reply.CandidateHash == nil {
		t.Fatalf("turn reply %+v, %v", reply, err)
	}
	want := "approved by reviewer against " + (*reply.CandidateHash)[:12]
	v := untilView(t, r, answerText, want)
	if strings.Index(v, answerText) > strings.Index(v, "approved by") {
		t.Fatalf("approval line precedes the text:\n%s", v)
	}
}

func TestAnswerSoloIsMarkedUnreviewed(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	r := startAnswerRig(t, h)
	solo := "solo"
	submit(t, r, wire.SubmitRequest{SubmissionID: "s1", Input: "q", Preset: &solo})
	v := untilView(t, r, "UNREVIEWED (solo)", answerText)
	if strings.Index(v, "UNREVIEWED (solo)") > strings.Index(v, answerText) {
		t.Fatalf("banner does not precede the text:\n%s", v)
	}
	if strings.Contains(v, "approved") {
		t.Fatalf("solo answer mentions approval:\n%s", v)
	}
}

func TestAnswerBlockEndsIncomplete(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	v := untilView(t, r, "INCOMPLETE, not approved", answerText, "line 3 divides by zero", "guard the divisor")
	for _, want := range []string{"block:", "[medium]"} {
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
	r := startAnswerRig(t, h)
	r.typeLine("question")
	v := untilView(t, r, "author_failed:truncated")
	if strings.Contains(v, "cut off") || strings.Contains(v, "INCOMPLETE, not approved") || strings.Contains(v, "approved by") {
		t.Fatalf("a turn without a candidate shows candidate text or banners:\n%s", v)
	}
}

func TestAnswerOverriddenShowsHumanReason(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), blockVerdict())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := turnIn(t, r, "incomplete")
	if err := dial(t, r.paths).Override(context.Background(), turn, "accepted operational risk"); err != nil {
		t.Fatal(err)
	}
	untilView(t, r, "HUMAN OVERRIDE: accepted operational risk", answerText)
	// The terminal turn reply must not replace the reason with the literal "override".
	r.until(t, "the terminal turn record", func(a *App) (string, bool) {
		ti := a.state.Turns[turn]
		return "", ti.State == "overridden" && ti.TerminalReason != nil && *ti.TerminalReason == "override"
	})
	if v := view(t, r); !strings.Contains(v, "HUMAN OVERRIDE: accepted operational risk") || strings.Contains(v, "INCOMPLETE") {
		t.Fatalf("override reason lost:\n%s", v)
	}
}

func TestAnswerCancelledDuringAuthor(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("author")()
	r := startAnswerRig(t, h)
	r.typeLine("question")
	turn := r.waitTurn(t)
	r.until(t, "the author running", func(a *App) (string, bool) {
		for _, ai := range a.state.Attempts {
			if ai.TurnID == turn && ai.Phase == "author" && ai.Status == "in_progress" {
				return "", true
			}
		}
		return "", false
	})
	if err := dial(t, r.paths).Cancel(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	v := untilView(t, r, "cancelled before candidate")
	if strings.Contains(v, "CANCELLED, not approved") || strings.Contains(v, answerText) {
		t.Fatalf("cancel before a candidate shows a candidate:\n%s", v)
	}
}

func TestAnswerCancelledDuringReview(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("reviewer")()
	r := startAnswerRig(t, h)
	turn := openReviewTurn(t, r)
	if err := dial(t, r.paths).Cancel(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	v := untilView(t, r, "CANCELLED, not approved", answerText)
	if strings.Contains(v, "approved by") || strings.Contains(v, "cancelled before candidate") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestAnswerDraftsOnWhileReviewersRun(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	release := h.Hold("reviewer")
	r := startAnswerRig(t, h)
	r.until(t, "drafts on", func(a *App) (string, bool) { a.SetDraftsEnabled(true); return "", true })
	openReviewTurn(t, r)
	untilView(t, r, "UNREVIEWED DRAFT", answerText)
	release()
	if v := untilView(t, r, "approved by"); strings.Contains(v, "UNREVIEWED DRAFT") {
		t.Fatalf("draft banner survives the terminal state:\n%s", v)
	}
}

func TestAnswerDraftsToggledOnMidTurn(t *testing.T) {
	h := scripted(t, enginetest.Reply(answerText), approve())
	defer h.Hold("reviewer")()
	r := startAnswerRig(t, h)
	openReviewTurn(t, r)
	if v := untilView(t, r, "turn in progress"); strings.Contains(v, answerText) {
		t.Fatalf("author text shown with drafts off:\n%s", v)
	}
	// Set through the program, so the App is not touched from this goroutine.
	r.until(t, "drafts on", func(a *App) (string, bool) { a.SetDraftsEnabled(true); return "", true })
	send(r, tickMsg{})
	untilView(t, r, "UNREVIEWED DRAFT", answerText)
}

const draftsQuickRoster = "[quick.fast]\npreset = \"solo\"\ndrafts = false"

func TestAnswerQuickProfileDrafts(t *testing.T) {
	// A quick profile on the review preset with drafts = true.
	on := "[quick.fast]\npreset = \"review\"\ndrafts = true"
	h := scripted(t, enginetest.Reply(answerText), approve(), draftsQuickRoster, on)
	release := h.Hold("reviewer")
	r := startAnswerRig(t, h)
	quick := "fast"
	submit(t, r, wire.SubmitRequest{SubmissionID: "s1", Input: "q", QuickProfile: &quick})
	untilView(t, r, "UNREVIEWED DRAFT", answerText)
	release()
	untilView(t, r, "approved by")
}

func TestAnswerQuickProfileDraftsOffByUser(t *testing.T) {
	on := "[quick.fast]\npreset = \"review\"\ndrafts = true"
	h := scripted(t, enginetest.Reply(answerText), approve(), draftsQuickRoster, on)
	defer h.Hold("reviewer")()
	r := startAnswerRig(t, h)
	r.until(t, "drafts off", func(a *App) (string, bool) { a.SetDraftsEnabled(false); return "", true })
	quick := "fast"
	submit(t, r, wire.SubmitRequest{SubmissionID: "s1", Input: "q", QuickProfile: &quick})
	turnIn(t, r, "reviewers_dispatched")
	if v := untilView(t, r, "turn in progress"); strings.Contains(v, answerText) {
		t.Fatalf("draft shown although the user turned drafts off:\n%s", v)
	}
}

func TestAnswerLongCandidateScrollsToTheLastLine(t *testing.T) {
	var text strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&text, "line %d\n", i)
	}
	h := scripted(t, enginetest.Reply(strings.TrimRight(text.String(), "\n")), approve())
	r := startAnswerRig(t, h)
	r.typeLine("question")
	// The chat starts at the bottom, so the approval line is shown (addendum 1, section 3).
	untilView(t, r, "approved by")

	// A waiter on the program's goroutine records every line any View showed.
	seenLines := map[string]bool{}
	w := &waiter{done: make(chan string, 1), cond: func(a *App) (string, bool) {
		for _, l := range strings.Split(a.View(), "\n") {
			seenLines[strings.TrimSpace(l)] = true
		}
		for i := 0; i < 5000; i++ {
			if !seenLines[fmt.Sprintf("line %d", i)] {
				return "", false
			}
		}
		return "", true
	}}
	r.tm.Send(waitMsg{w})
	for i := 0; i < 700; i++ {
		send(r, tea.KeyMsg{Type: tea.KeyPgUp})
	}
	select {
	case <-w.done:
	case <-time.After(30 * time.Second):
		t.Fatal("not every line became visible")
	}

	for i := 0; i < 700; i++ {
		send(r, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	// Past the end: the approval line stays, nothing panics, and Up moves at once.
	for i := 0; i < 50; i++ {
		send(r, tea.KeyMsg{Type: tea.KeyDown})
	}
	end := untilView(t, r, "approved by")
	send(r, tea.KeyMsg{Type: tea.KeyUp})
	if view(t, r) == end {
		t.Fatal("Up after over-scrolling did not move")
	}
	for i := 0; i < 1000; i++ {
		send(r, tea.KeyMsg{Type: tea.KeyPgUp})
	}
	if v := view(t, r); !strings.Contains(v, "line 0\n") {
		t.Fatalf("PgUp did not return to the top:\n%s", v)
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

// Two turns submitted within the same second are ordered by journal sequence,
// not by their random ids.
func TestAnswerTurnOrdersBySubmitSeq(t *testing.T) {
	at := time.Unix(1700000000, 0)
	s := State{Turns: map[string]TurnInfo{
		"zzz": {ID: "zzz", SubmittedAt: at, SubmitSeq: 5},
		"aaa": {ID: "aaa", SubmittedAt: at, SubmitSeq: 9},
	}}
	if got := answerTurn(s); got != "aaa" {
		t.Fatalf("answerTurn = %q, want the later submission aaa", got)
	}
}

// TestTranscriptContentLineRendering verifies that hostile content in the transcript
// is properly escaped and prefixed with two spaces on every terminal line.
func TestTranscriptContentLineRendering(t *testing.T) {
	// Create a content cache with various hostile content
	cc := NewContentCache()

	// Add content with control characters, bidi, and other hostile bytes
	hostileContent := "normal text\x1b[31mred\x1b[0m\x0dCR\u202eRTL\nline2"
	cc.Store("attempt:a1", hostileContent, nil)

	// Create a minimal state with the item
	s := State{
		Attempts: map[string]AttemptInfo{
			"a1": {ID: "a1", TurnID: "t1", Phase: "author", Status: "complete"},
		},
	}

	// Create transcript input
	in := transcriptInput{
		items: []wire.TranscriptItem{
			{Ref: "attempt:a1", ByteLen: int64(len(hostileContent))},
		},
		content: cc,
	}

	// Render the transcript
	width := 80
	lines := transcriptLines(s, "t1", in, width)
	output := strings.Join(lines, "\n")

	// Verify no raw control bytes in output
	dangerousBytes := []byte{0x1b, 0x07, 0x0d}
	for _, b := range dangerousBytes {
		if bytes.Contains([]byte(output), []byte{b}) {
			t.Errorf("transcript output contains raw control byte 0x%02x", b)
		}
	}

	// Verify content lines are prefixed (body lines should have two-space prefix)
	// Find the body content lines (they should start with "  ")
	bodyLines := []string{}
	for _, line := range lines {
		// Skip header and empty lines - body content should be prefixed
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && line != "  " {
			bodyLines = append(bodyLines, line)
		}
	}

	if len(bodyLines) == 0 {
		t.Fatal("no content lines found in transcript output")
	}

	// Verify the content is there but escaped
	if !strings.Contains(output, "\\x1b") {
		t.Error("escape sequence not found in output")
	}
	if !strings.Contains(output, "\\x0d") {
		t.Error("CR escape not found in output")
	}
	if !strings.Contains(output, "\\u202e") {
		t.Error("RTL override escape not found in output")
	}
}
