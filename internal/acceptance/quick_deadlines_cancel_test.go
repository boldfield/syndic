package acceptance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	qdcQuick     = "Quick profile"
	qdcDeadlines = "Deadlines and pools"
	qdcCancel    = "Cancellation and epochs"
)

func init() {
	cover(qdcQuick, "a roster `[quick.x]` with `preset = \"solo\"` and a `submit{quick_profile: \"x\"}` runs solo and the turn carries `quick_profile = \"x\"`", "TestQuickProfile_SoloQuickProfile")
	cover(qdcQuick, "`submit{quick_profile: \"x\", preset: \"review\"}` runs review", "TestQuickProfile_ExplicitPresetRunsReview")
	cover(qdcQuick, "an unknown name is refused with `quick_profile_unknown`", "TestQuickProfile_UnknownQuickProfile")
	cover(qdcQuick, "a roster quick profile that sets `protected_paths` fails `Roster.Validate` naming the property", "TestQuickProfile_ProtectedPathsRejected")

	cover(qdcDeadlines, "two reviewers in one pool run sequentially and the second's `queued` time counts against the turn deadline", "TestDeadlinesPools_SequentialReviewerQueueCountsTowardTurnDeadline")
	cover(qdcDeadlines, "attempt deadline on a `Hang` step yields `interrupted` and INCOMPLETE", "TestDeadlinesPools_HangStepYieldsInterrupted")
	cover(qdcDeadlines, "turn deadline fires during queue", "TestDeadlinesPools_TurnDeadlineDuringQueue")
	cover(qdcDeadlines, "repair does not extend the turn deadline", "TestDeadlinesPools_RepairDoesNotExtendTurnDeadline")
	cover(qdcDeadlines, "an attempt dispatched with less remaining turn time than `attempt` gets the smaller value", "TestDeadlinesPools_AttemptClampedToRemainingTurnTime")
	cover(qdcDeadlines, "`submit` while `provisional = true` is refused with `provisional` unless `allow_provisional: true` is set, and reattach and recovery work regardless", "TestDeadlinesPools_ProvisionalDeadlines")
	cover(qdcDeadlines, "a per-preset `[presets.x.deadlines]` override changes the attempt deadline on `attempt_begun`", "TestDeadlinesPools_PerPresetDeadlineOverride")

	cover(qdcCancel, "cancel during author → `cancelled` with no `candidate_id`", "TestCancellation_CancelDuringAuthor")
	cover(qdcCancel, "cancel during review → `cancelled` with `candidate_id` set and gathered verdicts in the transcript", "TestCancellation_CancelDuringReview")
	cover(qdcCancel, "output arriving after cancel is quarantined and the turn state does not change", "TestCancellation_LateOutputQuarantined")
	cover(qdcCancel, "with `CancelUnconfirmed`, the pool slot stays held and no automatic release occurs", "TestCancellation_CancelUnconfirmedKeepsHold")
}

// qdcFarFuture starts the fake clock so far ahead that a scripted Hang,
// which stops at the request deadline on the real wall clock, outlasts the test.
var qdcFarFuture = enginetest.ClockStart(time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC))

// qdcScripts scripts the author to answer whatever it is asked and each
// reviewer by its steps, with ordinary steps after them.
func qdcScripts(reviewers map[string][]fake.Step) map[string][]fake.Step {
	out := map[string][]fake.Step{"author": repeat(8)}
	for id, steps := range reviewers {
		out[id] = append(append([]fake.Step(nil), steps...), repeat(8)...)
	}
	return out
}

// qdcRoster is the enginetest roster with the attempt and turn deadlines
// set, the reviewer replaced by the named reviewers, which share the pool p
// (concurrency 1), and extra appended.
func qdcRoster(attempt, turn string, reviewers []string, extra string) string {
	toml := enginetest.RosterTOML
	toml = strings.Replace(toml, `attempt     = "300s"`, `attempt     = "`+attempt+`"`, 1)
	toml = strings.Replace(toml, `turn        = "900s"`, `turn        = "`+turn+`"`, 1)
	start := strings.Index(toml, "[[participants]]\nid = \"reviewer\"")
	toml = toml[:start] + toml[strings.Index(toml, "[[endpoints]]"):]
	var participants, quoted []string
	for _, id := range reviewers {
		participants = append(participants, "[[participants]]\nid = \""+id+"\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \"p\"\naccept_unknown_truncation = true\n")
		quoted = append(quoted, `"`+id+`"`)
	}
	toml = strings.Replace(toml, "[[endpoints]]", strings.Join(participants, "\n")+"\n[[endpoints]]", 1)
	toml = strings.Replace(toml, `required_reviewers = ["reviewer"]`, "required_reviewers = ["+strings.Join(quoted, ", ")+"]", 1)
	return toml + "\n[pools.p]\nconcurrency = 1\n" + extra
}

// qdcRefusal requires err to be a protocol error with the code.
func qdcRefusal(t *testing.T, err error, code string) *wire.Error {
	t.Helper()
	var werr *wire.Error
	if !errors.As(err, &werr) {
		t.Fatalf("error = %v (%T), want a protocol error %q", err, err, code)
	}
	if werr.Code != code {
		t.Fatalf("error code = %q (detail %s), want %q", werr.Code, str(werr.Detail), code)
	}
	return werr
}

// qdcBegunBy waits for the attempt_begun of the participant in the phase that
// satisfies match, and returns it.
func (s *suite) qdcBegunBy(participant, phase string, match func(wire.EvAttemptBegun) bool) wire.EvAttemptBegun {
	s.t.Helper()
	ev := s.WaitEvent(func(ev event) bool {
		b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && b.Participant == participant && b.Phase == phase && match(b)
	})
	b, _ := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
	return b
}

// qdcFirstBegun waits for the participant's first attempt in the phase.
func (s *suite) qdcFirstBegun(participant, phase string) wire.EvAttemptBegun {
	s.t.Helper()
	return s.qdcBegunBy(participant, phase, func(wire.EvAttemptBegun) bool { return true })
}

// qdcAttemptDeadline parses the deadline an attempt_begun announced.
func qdcAttemptDeadline(t *testing.T, b wire.EvAttemptBegun) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, b.Deadline)
	if err != nil {
		t.Fatalf("attempt_begun deadline %q: %v", b.Deadline, err)
	}
	return at
}

// qdcWantDeadline requires the attempt to have begun with the deadline.
func qdcWantDeadline(t *testing.T, what string, b wire.EvAttemptBegun, want time.Time) {
	t.Helper()
	if got := qdcAttemptDeadline(t, b); !got.Equal(want) {
		t.Fatalf("%s: attempt_begun deadline = %v, want %v", what, got, want)
	}
}

// qdcWaitParticipant waits for the participant_state the participant announces.
func (s *suite) qdcWaitParticipant(participant, state string) {
	s.t.Helper()
	s.WaitEvent(func(ev event) bool {
		p, ok := body[wire.EvParticipantState](ev, wire.EventKindParticipantState)
		return ok && p.Participant == participant && p.State == state
	})
}

// qdcCountBegun counts the attempt_begun events seen so far for the participant.
func (s *suite) qdcCountBegun(participant, phase string) int {
	n := 0
	for _, ev := range s.Events() {
		if b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun); ok && b.Participant == participant && b.Phase == phase {
			n++
		}
	}
	return n
}

// qdcWaitCapacity waits for the capacity event of the attempt in the state.
func (s *suite) qdcWaitCapacity(attemptID, state string) wire.EvCapacity {
	s.t.Helper()
	ev := s.WaitEvent(func(ev event) bool {
		c, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && c.HeldByAttempt == attemptID && c.State == state
	})
	c, _ := body[wire.EvCapacity](ev, wire.EventKindCapacity)
	return c
}

// qdcSubmit submits with the quick profile and preset, either of which may
// be empty.
func (s *suite) qdcSubmit(c *client.Client, id, profile, preset string) (wire.SubmitReply, error) {
	req := wire.SubmitRequest{SubmissionID: id, Input: "what is the answer?"}
	if profile != "" {
		req.QuickProfile = &profile
	}
	if preset != "" {
		req.Preset = &preset
	}
	return c.Submit(s.ctx(), req)
}

// qdcXRoster is the enginetest roster with its quick profile named x.
func qdcXRoster() string {
	return strings.ReplaceAll(enginetest.RosterTOML, "[quick.fast", "[quick.x")
}

// ---- Quick profile ----

func TestQuickProfile_SoloQuickProfile(t *testing.T) {
	t.Parallel()
	s := newSuite(t, qdcXRoster(), qdcScripts(map[string][]fake.Step{"reviewer": nil}))
	c := s.Dial(t)
	reply, err := s.qdcSubmit(c, "s1", "x", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if reply.Preset != "solo" || reply.QuickProfile == nil || *reply.QuickProfile != "x" {
		t.Fatalf("submit reply = preset %q quick_profile %s, want solo and x", reply.Preset, str(reply.QuickProfile))
	}
	s.WaitTerminal(reply.TurnID)
	turn := s.Turn(t, c, reply.TurnID)
	wantTurn(t, turn, engine.StateSoloComplete, "solo_ok")
	if turn.Mode != "solo" || turn.Preset != "solo" {
		t.Fatalf("turn ran mode %q preset %q, want solo", turn.Mode, turn.Preset)
	}
	if turn.QuickProfile == nil || *turn.QuickProfile != "x" {
		t.Fatalf("turn quick_profile = %s, want x", str(turn.QuickProfile))
	}
	if n := len(s.Adapters["reviewer"].Calls()); n != 0 {
		t.Fatalf("the reviewer received %d requests in a solo turn, want 0", n)
	}
	if len(turn.Verdicts) != 0 {
		t.Fatalf("a solo turn holds %d verdicts, want none", len(turn.Verdicts))
	}
}

func TestQuickProfile_ExplicitPresetRunsReview(t *testing.T) {
	t.Parallel()
	s := newSuite(t, qdcXRoster(), qdcScripts(map[string][]fake.Step{"reviewer": {verdict(fake.VerdictApprove)}}))
	c := s.Dial(t)
	reply, err := s.qdcSubmit(c, "s1", "x", "review")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if reply.Preset != "review" {
		t.Fatalf("submit reply preset = %q, want review: an explicit preset wins over the profile's", reply.Preset)
	}
	s.WaitTerminal(reply.TurnID)
	turn := s.Turn(t, c, reply.TurnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if turn.Mode != "review" || turn.Preset != "review" {
		t.Fatalf("turn ran mode %q preset %q, want review", turn.Mode, turn.Preset)
	}
	if turn.QuickProfile == nil || *turn.QuickProfile != "x" {
		t.Fatalf("turn quick_profile = %s, want it recorded as x", str(turn.QuickProfile))
	}
	if n := len(s.calls("reviewer", "review")); n != 1 {
		t.Fatalf("the reviewer received %d review requests, want 1", n)
	}
	if len(turn.Verdicts) != 1 {
		t.Fatalf("turn holds %d verdicts, want 1", len(turn.Verdicts))
	}
}

func TestQuickProfile_UnknownQuickProfile(t *testing.T) {
	t.Parallel()
	s := newSuite(t, qdcXRoster(), qdcScripts(map[string][]fake.Step{"reviewer": nil}))
	c := s.Dial(t)
	_, err := s.qdcSubmit(c, "s1", "nope", "")
	werr := qdcRefusal(t, err, wire.ErrorCodeQuickProfileUnknown)
	if werr.QuickProfile == nil || *werr.QuickProfile != "nope" {
		t.Fatalf("refusal quick_profile = %s, want the name sent, nope", str(werr.QuickProfile))
	}
	if st := s.Status(t, c); len(st.TurnsOpen) != 0 {
		t.Fatalf("turns_open = %v after a refused submit, want none", st.TurnsOpen)
	}
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindTurnSubmitted {
			t.Fatal("a refused submit journaled turn_submitted")
		}
	}
	if n := len(s.Adapters["author"].Calls()); n != 0 {
		t.Fatalf("the author received %d requests for a refused submit, want 0", n)
	}
	// The name x is still accepted: the refusal is of the name, not of quick profiles.
	if _, err := s.qdcSubmit(c, "s2", "x", ""); err != nil {
		t.Fatalf("submit with the profile the roster defines: %v", err)
	}
}

func TestQuickProfile_ProtectedPathsRejected(t *testing.T) {
	t.Parallel()
	toml := strings.Replace(qdcXRoster(), "[quick.x]\npreset = \"solo\"", "[quick.x]\npreset = \"solo\"\nprotected_paths = [\"secrets/\"]", 1)
	if !strings.Contains(toml, "protected_paths") {
		t.Fatal("the roster was not given a protected_paths property")
	}
	path := filepath.Join(t.TempDir(), "roster.toml")
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	roster, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = roster.Validate()
	if err == nil {
		t.Fatal("Validate accepted a quick profile that sets protected_paths")
	}
	if msg := err.Error(); !strings.Contains(msg, "protected_paths") || !strings.Contains(msg, `"x"`) {
		t.Fatalf("Validate error = %q, want it to name the property protected_paths and the profile x", msg)
	}

	// The same roster without the property is valid, so the property is what failed.
	path = filepath.Join(t.TempDir(), "roster.toml")
	if err := os.WriteFile(path, []byte(qdcXRoster()), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate of the roster without protected_paths: %v", err)
	}
}

// ---- Deadlines and pools ----

func TestDeadlinesPools_SequentialReviewerQueueCountsTowardTurnDeadline(t *testing.T) {
	t.Parallel()
	roster := qdcRoster("800s", "900s", []string{"r1", "r2"}, "")
	s := newSuite(t, roster, qdcScripts(map[string][]fake.Step{"r1": {verdict(fake.VerdictApprove)}, "r2": {verdict(fake.VerdictApprove)}}))
	release := s.h.Hold("r1")
	c := s.Dial(t)
	start := s.Clock.Now()
	turnID := s.Submit(t, c, "s1")

	r1 := s.qdcFirstBegun("r1", "review")
	s.qdcWaitParticipant("r2", "queued")
	if n := s.qdcCountBegun("r2", "review"); n != 0 {
		t.Fatalf("r2 began %d attempts while r1 holds the pool, want none", n)
	}
	if n := len(s.Adapters["r2"].Calls()); n != 0 {
		t.Fatalf("r2 was called %d times while queued, want 0", n)
	}
	qdcWantDeadline(t, "r1", r1, start.Add(800*time.Second))

	// r2 waits 600s of the 900s turn, queued.
	s.Clock.Advance(600 * time.Second)
	release()
	r2 := s.qdcFirstBegun("r2", "review")
	qdcWantDeadline(t, "r2 after 600s queued", r2, start.Add(900*time.Second))
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	// Sequential: r2 began only after r1's attempt had completed.
	var r1Done, r2Begun int64
	for _, ev := range s.Events() {
		if done, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted); ok && done.AttemptID == r1.AttemptID {
			r1Done = ev.Seq
		}
		if b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun); ok && b.AttemptID == r2.AttemptID {
			r2Begun = ev.Seq
		}
	}
	if r1Done == 0 || r2Begun == 0 || r2Begun < r1Done {
		t.Fatalf("r1 completed at seq %d, r2 began at seq %d: want r2 after r1", r1Done, r2Begun)
	}
}

func TestDeadlinesPools_HangStepYieldsInterrupted(t *testing.T) {
	t.Parallel()
	roster := qdcRoster("60s", "900s", []string{"reviewer"}, "")
	s := newSuite(t, roster, qdcScripts(map[string][]fake.Step{"reviewer": {{Kind: fake.StepKindHang}}}), qdcFarFuture)
	c := s.Dial(t)
	start := s.Clock.Now()
	turnID := s.Submit(t, c, "s1")
	b := s.qdcFirstBegun("reviewer", "review")
	qdcWantDeadline(t, "reviewer", b, start.Add(60*time.Second))

	s.Clock.Advance(59 * time.Second)
	if open := s.Turn(t, c, turnID); terminal(open.State) {
		t.Fatalf("turn ended %q before the attempt deadline", open.State)
	}
	s.Clock.Advance(2 * time.Second)
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateIncomplete, "deadline:attempt:"+b.AttemptID)

	done := s.WaitEvent(func(ev event) bool {
		a, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted)
		return ok && a.AttemptID == b.AttemptID
	})
	completed, _ := body[wire.EvAttemptCompleted](done, wire.EventKindAttemptCompleted)
	if completed.Status != "interrupted" {
		t.Fatalf("attempt %s completed with status %q, want interrupted", b.AttemptID, completed.Status)
	}
	rows := s.attemptRows(t, turnID, "reviewer", "review")
	if len(rows) != 1 || str(rows[0].Status) != "interrupted" {
		t.Fatalf("reviewer attempt rows = %+v, want one with status interrupted", rows)
	}
	if n := len(s.verdictRows(t, turnID)); n != 0 {
		t.Fatalf("%d verdicts recorded from an interrupted attempt, want none", n)
	}
}

func TestDeadlinesPools_TurnDeadlineDuringQueue(t *testing.T) {
	t.Parallel()
	// The attempt deadline equals the turn deadline: r1, which holds the pool,
	// cannot outlast the turn, so the turn deadline is the one that fires.
	roster := qdcRoster("100s", "100s", []string{"r1", "r2"}, "")
	s := newSuite(t, roster, qdcScripts(map[string][]fake.Step{"r1": {verdict(fake.VerdictApprove)}, "r2": {verdict(fake.VerdictApprove)}}))
	release := s.h.Hold("r1")
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.qdcFirstBegun("r1", "review")
	s.qdcWaitParticipant("r2", "queued")

	s.Clock.Advance(101 * time.Second)
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateIncomplete, "deadline:turn")
	if n := s.qdcCountBegun("r2", "review"); n != 0 {
		t.Fatalf("r2 began %d attempts although the turn ended while it was queued", n)
	}
	// The queue is cleared: r2 goes idle and is never called, even once r1 returns.
	s.qdcWaitParticipant("r2", "idle")
	release()
	s.qdcWaitParticipant("r1", "idle")
	if n := len(s.Adapters["r2"].Calls()); n != 0 {
		t.Fatalf("r2 was called %d times, want 0", n)
	}
	if n := len(s.attemptRows(t, turnID, "r2", "review")); n != 0 {
		t.Fatalf("r2 has %d attempt rows, want none", n)
	}
}

func TestDeadlinesPools_RepairDoesNotExtendTurnDeadline(t *testing.T) {
	t.Parallel()
	// A fresh attempt is 60s; the turn is 100s. A repair that begins at 50s
	// would end at 110s, ten seconds after the turn.
	roster := qdcRoster("60s", "100s", []string{"reviewer"}, "")
	scripts := qdcScripts(map[string][]fake.Step{"reviewer": {enginetest.Reply(fake.VerdictProseApprove()), {Kind: fake.StepKindHang}}})
	s := newSuite(t, roster, scripts, qdcFarFuture)
	release := s.h.Hold("reviewer")
	c := s.Dial(t)
	start := s.Clock.Now()
	turnID := s.Submit(t, c, "s1")
	first := s.qdcFirstBegun("reviewer", "review")
	qdcWantDeadline(t, "first attempt", first, start.Add(60*time.Second))

	s.Clock.Advance(50 * time.Second)
	release()
	repair := s.qdcBegunBy("reviewer", "review", func(b wire.EvAttemptBegun) bool { return b.RepairOf != nil })
	if *repair.RepairOf != first.AttemptID {
		t.Fatalf("repair is of %s, want %s", *repair.RepairOf, first.AttemptID)
	}
	qdcWantDeadline(t, "repair", repair, start.Add(100*time.Second))

	s.Clock.Advance(49 * time.Second)
	if open := s.Turn(t, c, turnID); terminal(open.State) {
		t.Fatalf("turn ended %q at 99s, before its deadline", open.State)
	}
	s.Clock.Advance(1 * time.Second)
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete || turn.TerminalReason == nil || *turn.TerminalReason != "deadline:turn" {
		t.Fatalf("turn is %q (%s) at its original deadline, want incomplete on the deadline", turn.State, str(turn.TerminalReason))
	}
	rows := s.attemptRows(t, turnID, "reviewer", "review")
	if len(rows) != 2 {
		t.Fatalf("reviewer attempts = %d, want the original and the repair", len(rows))
	}
}

func TestDeadlinesPools_AttemptClampedToRemainingTurnTime(t *testing.T) {
	t.Parallel()
	roster := qdcRoster("800s", "900s", []string{"reviewer"}, "")
	s := newSuite(t, roster, qdcScripts(map[string][]fake.Step{"reviewer": {verdict(fake.VerdictApprove)}}))
	release := s.h.Hold("author")
	c := s.Dial(t)
	start := s.Clock.Now()
	turnID := s.Submit(t, c, "s1")
	author := s.qdcFirstBegun("author", "author")
	qdcWantDeadline(t, "author", author, start.Add(800*time.Second))

	// 700s in, 200s of the turn remain: less than the 800s an attempt gets.
	s.Clock.Advance(700 * time.Second)
	release()
	reviewer := s.qdcFirstBegun("reviewer", "review")
	qdcWantDeadline(t, "reviewer with 200s of turn left", reviewer, start.Add(900*time.Second))
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
	calls := s.wantReviewCalls(t, 1)
	if !calls[0].Deadline.Equal(start.Add(900 * time.Second)) {
		t.Fatalf("reviewer request deadline = %v, want the turn deadline %v", calls[0].Deadline, start.Add(900*time.Second))
	}
}

func TestDeadlinesPools_ProvisionalDeadlines(t *testing.T) {
	t.Parallel()
	roster := strings.Replace(qdcRoster("300s", "900s", []string{"reviewer"}, ""), "provisional = false", "provisional = true", 1)
	if !strings.Contains(roster, "provisional = true") {
		t.Fatal("the roster was not made provisional")
	}
	s := newSuite(t, roster, qdcScripts(map[string][]fake.Step{"reviewer": {verdict(fake.VerdictApprove)}}))
	c := s.Dial(t)

	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?"})
	_ = qdcRefusal(t, err, wire.ErrorCodeProvisional)
	no := false
	_, err = c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?", AllowProvisional: &no})
	_ = qdcRefusal(t, err, wire.ErrorCodeProvisional)
	if st := s.Status(t, c); len(st.TurnsOpen) != 0 {
		t.Fatalf("turns_open = %v after a refused submit, want none", st.TurnsOpen)
	}
	if n := len(s.Adapters["author"].Calls()); n != 0 {
		t.Fatalf("the author received %d requests for a refused submit, want 0", n)
	}

	// Reattach works: a second client connects, and replays the journal.
	c2 := s.Dial(t)
	if st := s.Status(t, c2); len(st.TurnsOpen) != 0 {
		t.Fatalf("a reattached client sees turns_open = %v", st.TurnsOpen)
	}

	yes := true
	release := s.h.Hold("author")
	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s2", Input: "what is the answer?", AllowProvisional: &yes})
	if err != nil {
		t.Fatalf("submit with allow_provisional: %v", err)
	}
	s.qdcFirstBegun("author", "author")
	if open := s.Status(t, c2).TurnsOpen; len(open) != 1 || open[0] != reply.TurnID {
		t.Fatalf("reattached client sees turns_open = %v, want [%s]", open, reply.TurnID)
	}

	// Recovery works: the engine restarts with the turn open and ends it, with
	// the roster still provisional.
	s.Restart(roster, qdcScripts(map[string][]fake.Step{"reviewer": nil}))
	release()
	c3 := s.Dial(t)
	s.WaitTerminal(reply.TurnID)
	wantTurn(t, s.Turn(t, c3, reply.TurnID), engine.StateIncomplete, "engine_restart")
	_, err = c3.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s3", Input: "what is the answer?"})
	_ = qdcRefusal(t, err, wire.ErrorCodeProvisional)
}

func TestDeadlinesPools_PerPresetDeadlineOverride(t *testing.T) {
	t.Parallel()
	roster := qdcRoster("300s", "900s", []string{"reviewer"}, "\n[presets.review.deadlines]\nattempt = \"100s\"\n")
	s := newSuite(t, roster, qdcScripts(map[string][]fake.Step{"reviewer": {verdict(fake.VerdictApprove)}}))
	release := s.h.Hold("reviewer")
	c := s.Dial(t)
	start := s.Clock.Now()
	turnID := s.Submit(t, c, "s1")
	author := s.qdcFirstBegun("author", "author")
	qdcWantDeadline(t, "author under the preset override", author, start.Add(100*time.Second))
	reviewer := s.qdcFirstBegun("reviewer", "review")
	qdcWantDeadline(t, "reviewer under the preset override", reviewer, start.Add(100*time.Second))

	// The override is enforced: 100s ends the attempt, where the roster's 300s would not.
	s.Clock.Advance(99 * time.Second)
	if open := s.Turn(t, c, turnID); terminal(open.State) {
		t.Fatalf("turn ended %q at 99s", open.State)
	}
	s.Clock.Advance(2 * time.Second)
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateIncomplete, "deadline:attempt:"+reviewer.AttemptID)
	release()
}

// ---- Cancellation and epochs ----

func TestCancellation_CancelDuringAuthor(t *testing.T) {
	t.Parallel()
	roster := qdcRoster("300s", "900s", []string{"reviewer"}, "")
	scripts := qdcScripts(map[string][]fake.Step{"reviewer": {verdict(fake.VerdictApprove)}})
	scripts["author"] = []fake.Step{{Kind: fake.StepKindHang}}
	s := newSuite(t, roster, scripts, qdcFarFuture)
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.qdcFirstBegun("author", "author")

	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateCancelled, "cancelled")
	if turn.CandidateHash != nil {
		t.Fatalf("a turn cancelled during the author has candidate %s, want none", *turn.CandidateHash)
	}
	row, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.CandidateID != "" {
		t.Fatalf("journaled candidate_id = %q, want none", row.CandidateID)
	}
	if row.PhaseAtCancel == "" {
		t.Fatal("phase_at_cancel is empty")
	}
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindCandidateFrozen {
			t.Fatal("a candidate was frozen in a turn cancelled during the author")
		}
	}
	if n := len(s.Adapters["reviewer"].Calls()); n != 0 {
		t.Fatalf("the reviewer received %d requests in a turn cancelled before a candidate, want 0", n)
	}
}

func TestCancellation_CancelDuringReview(t *testing.T) {
	t.Parallel()
	roster := qdcRoster("300s", "900s", []string{"r1", "r2"}, "")
	scripts := qdcScripts(map[string][]fake.Step{"r1": {verdict(fake.VerdictApprove)}, "r2": {{Kind: fake.StepKindHang}}})
	s := newSuite(t, roster, scripts, qdcFarFuture)
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	// r1 and r2 share a pool: r2 begins once r1 has answered, so r1's verdict
	// is gathered while r2 is in flight.
	s.qdcFirstBegun("r2", "review")
	s.WaitEvent(func(ev event) bool {
		v, ok := body[wire.EvVerdictRecorded](ev, wire.EventKindVerdictRecorded)
		return ok && v.Parsed
	})

	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateCancelled, "cancelled")
	if turn.CandidateHash == nil || *turn.CandidateHash == "" {
		t.Fatal("a turn cancelled during review has no candidate")
	}
	row, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.CandidateID == "" || row.PhaseAtCancel == "" {
		t.Fatalf("journaled turn = candidate_id %q phase_at_cancel %q, want both set", row.CandidateID, row.PhaseAtCancel)
	}
	if len(turn.Verdicts) != 1 || !turn.Verdicts[0].Parsed {
		t.Fatalf("turn verdicts = %+v, want r1's one parsed verdict", turn.Verdicts)
	}

	// The gathered verdict is in the transcript: r1's attempt is listed and its
	// content is the approval of the frozen candidate.
	r1 := s.attemptRows(t, turnID, "r1", "review")
	if len(r1) != 1 || turn.Verdicts[0].AttemptID != r1[0].ID {
		t.Fatalf("verdict %+v is not of r1's attempt %+v", turn.Verdicts[0], r1)
	}
	items, err := c.Transcript(s.ctx(), turnID, "", 100)
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	found := false
	for _, item := range items.Items {
		if item.ID != r1[0].ID && !strings.HasSuffix(item.Ref, r1[0].ID) {
			continue
		}
		data, err := c.Content(s.ctx(), item.Ref)
		if err != nil {
			t.Fatalf("content %s: %v", item.Ref, err)
		}
		if strings.Contains(string(data), *turn.CandidateHash) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the transcript %+v has no item carrying r1's verdict on candidate %s", items.Items, *turn.CandidateHash)
	}
}

func TestCancellation_LateOutputQuarantined(t *testing.T) {
	t.Parallel()
	s := newSuite(t, qdcRoster("300s", "900s", []string{"reviewer"}, ""), qdcScripts(map[string][]fake.Step{"reviewer": nil}), enginetest.CancelUnconfirmed())
	release := s.h.Hold("author")
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	author := s.qdcFirstBegun("author", "author")
	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.WaitTerminal(turnID)
	s.qdcWaitCapacity(author.AttemptID, "uncertain")
	before, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if before.TerminalState != engine.StateCancelled {
		t.Fatalf("turn is %q before the late output, want cancelled", before.TerminalState)
	}

	// The author's output arrives after the cancel. The engine's release of the
	// hold on a late result is the signal that it has been processed.
	release()
	late := s.qdcWaitCapacity(author.AttemptID, "released")
	if late.How == nil || *late.How != "late_result" {
		t.Fatalf("capacity released how = %s, want late_result", str(late.How))
	}
	row, err := s.Store.GetAttempt(author.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !row.Quarantined {
		t.Fatalf("the author's late output was not quarantined: %+v", row)
	}
	after, err := s.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TerminalState != before.TerminalState || after.TerminalReason != before.TerminalReason || after.Epoch != before.Epoch || after.CandidateID != "" {
		t.Fatalf("turn changed from %+v to %+v after the late output", before, after)
	}
	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateCancelled, "cancelled")
	if turn.CandidateHash != nil || len(turn.Verdicts) != 0 {
		t.Fatalf("late output produced candidate %s and %d verdicts, want neither", str(turn.CandidateHash), len(turn.Verdicts))
	}
	if n := len(s.Adapters["reviewer"].Calls()); n != 0 {
		t.Fatalf("the reviewer received %d requests from quarantined output, want 0", n)
	}
	for _, ev := range s.Events() {
		if st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState); ok && st.TurnID == turnID && st.State != engine.StateCancelled && terminal(st.State) {
			t.Fatalf("turn announced terminal state %q after cancel", st.State)
		}
	}
}

func TestCancellation_CancelUnconfirmedKeepsHold(t *testing.T) {
	t.Parallel()
	scripts := qdcScripts(map[string][]fake.Step{"reviewer": nil})
	scripts["author"] = []fake.Step{{Kind: fake.StepKindHang}}
	s := newSuite(t, qdcRoster("100000h", "100000h", []string{"reviewer"}, ""), scripts, qdcFarFuture, enginetest.CancelUnconfirmed())
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	author := s.qdcFirstBegun("author", "author")
	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.WaitTerminal(turnID)
	s.qdcWaitCapacity(author.AttemptID, "uncertain")
	s.wantHold(t, c, author.AttemptID)

	// A later submit waits behind the held slot, however far the clock goes.
	next := s.Submit(t, c, "s2")
	s.qdcWaitParticipant("author", "queued")
	s.Clock.Advance(10 * time.Minute)
	if open := s.Status(t, c).TurnsOpen; len(open) != 1 || open[0] != next {
		t.Fatalf("turns_open = %v, want the queued turn %s", open, next)
	}
	if n := s.qdcCountBegun("author", "author"); n != 1 {
		t.Fatalf("the author began %d attempts, want the one cancelled: the queued turn must not dispatch", n)
	}
	holds := s.holds(t, c, "default")
	if len(holds) != 1 || holds[0].AttemptID != author.AttemptID {
		t.Fatalf("pool default holds %+v, want the one held by %s", holds, author.AttemptID)
	}
	if _, found, err := s.Store.GetHold(author.AttemptID); err != nil || !found {
		t.Fatalf("journaled hold for %s: found=%v err=%v", author.AttemptID, found, err)
	}
	for _, ev := range s.Events() {
		if cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity); ok && cap.State == "released" {
			t.Fatalf("capacity %+v was released with no operator release", cap)
		}
	}

	// Only the operator's release frees the slot.
	if _, err := c.Release(s.ctx(), wire.ReleaseRequest{Pool: "default", Reason: "the server was checked"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	released := s.qdcWaitCapacity(author.AttemptID, "released")
	if released.How == nil || *released.How != "operator" {
		t.Fatalf("capacity released how = %s, want operator", str(released.How))
	}
	s.qdcBegunBy("author", "author", func(b wire.EvAttemptBegun) bool { return b.AttemptID != author.AttemptID })
}
