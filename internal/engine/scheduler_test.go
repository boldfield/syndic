package engine_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// reviewer names a reviewer and the pool it dispatches through.
type reviewer struct{ id, pool string }

// schedRoster is RosterTOML with the given deadlines, one reviewer per entry,
// all required by the review preset, and extra appended at the end. The
// author stays in the default pool. Every other pool a reviewer names is
// declared, with the concurrency in pools or 1.
func schedRoster(attempt, turn string, reviewers []reviewer, pools map[string]int, extra string) string {
	toml := enginetest.RosterTOML
	toml = strings.Replace(toml, `attempt     = "300s"`, `attempt     = "`+attempt+`"`, 1)
	toml = strings.Replace(toml, `turn        = "900s"`, `turn        = "`+turn+`"`, 1)
	start := strings.Index(toml, "[[participants]]\nid = \"reviewer\"")
	toml = toml[:start] + toml[strings.Index(toml, "[[endpoints]]"):]
	var participants, quoted []string
	for _, r := range reviewers {
		participants = append(participants, "[[participants]]\nid = \""+r.id+"\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \""+r.pool+"\"\n")
		quoted = append(quoted, `"`+r.id+`"`)
	}
	toml = strings.Replace(toml, "[[endpoints]]", strings.Join(participants, "\n")+"\n[[endpoints]]", 1)
	toml = strings.Replace(toml, `required_reviewers = ["reviewer"]`, "required_reviewers = ["+strings.Join(quoted, ", ")+"]", 1)
	declared := map[string]bool{}
	for _, r := range reviewers {
		if r.pool == "default" || declared[r.pool] {
			continue
		}
		declared[r.pool] = true
		n := 1
		if c, ok := pools[r.pool]; ok {
			n = c
		}
		toml += fmt.Sprintf("\n[pools.%s]\nconcurrency = %d\n", r.pool, n)
	}
	return toml + extra
}

func approvers(ids ...string) map[string][]fake.Step {
	scripts := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}}
	for _, id := range ids {
		scripts[id] = []fake.Step{approveStep()}
	}
	return scripts
}

func participantState(t *testing.T, h *enginetest.Harness, id string) string {
	t.Helper()
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Participants {
		if p.ID == id {
			return p.State
		}
	}
	t.Fatalf("no participant %q", id)
	return ""
}

func waitParticipantState(t *testing.T, h *enginetest.Harness, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if participantState(t, h, id) == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("participant %s is %s, want %s", id, participantState(t, h, id), want)
}

// stateEvents returns the participant_state states emitted for a participant.
func stateEvents(h *enginetest.Harness, id string) []string {
	var states []string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvParticipantState); ok && p.Participant == id {
			states = append(states, p.State)
		}
	}
	return states
}

// eventSeq returns the seq of the n-th (from 0) attempt event of a kind for
// the participant's attempt, or 0 when there is none.
func attemptEventSeq(h *enginetest.Harness, kind, participant string, n int) int64 {
	ids := map[string]bool{}
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvAttemptBegun); ok && p.Participant == participant {
			ids[p.AttemptID] = true
		}
	}
	seen := 0
	for _, ev := range h.Events() {
		if ev.Kind != kind {
			continue
		}
		var id string
		switch p := ev.Payload.(type) {
		case *wire.EvAttemptBegun:
			id = p.AttemptID
		case *wire.EvAttemptCompleted:
			id = p.AttemptID
		}
		if ids[id] {
			if seen == n {
				return ev.Seq
			}
			seen++
		}
	}
	return 0
}

func begunDeadline(t *testing.T, h *enginetest.Harness, attemptID string) time.Time {
	t.Helper()
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvAttemptBegun); ok && p.AttemptID == attemptID {
			at, err := time.Parse(time.RFC3339Nano, p.Deadline)
			if err != nil {
				t.Fatal(err)
			}
			return at
		}
	}
	t.Fatalf("no attempt_begun for %s", attemptID)
	return time.Time{}
}

func joinStates(s []string) string { return strings.Join(s, ",") }

func TestPoolSerializesSecondReviewer(t *testing.T) {
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "p"}, {"r2", "p"}}, nil, ""), approvers("r1", "r2"))
	release := h.Hold("r1")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 2)
	waitParticipantState(t, h, "r2", engine.ParticipantQueued)
	if n := len(attemptsOf(h, turnID, "r2")); n != 0 {
		t.Fatalf("queued reviewer has %d attempts", n)
	}
	if n := len(h.Adapters["r2"].Calls()); n != 0 {
		t.Fatalf("queued reviewer was called %d times", n)
	}
	release()
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	begun := attemptEventSeq(h, wire.EventKindAttemptBegun, "r2", 0)
	completed := attemptEventSeq(h, wire.EventKindAttemptCompleted, "r1", 0)
	if begun == 0 || completed == 0 || begun < completed {
		t.Fatalf("r2 begun at %d, r1 completed at %d", begun, completed)
	}
	if got := joinStates(stateEvents(h, "r2")); got != "queued,running,idle" {
		t.Fatalf("r2 states = %s", got)
	}
	waitParticipantState(t, h, "r2", engine.ParticipantIdle)
}

func TestPoolsRunReviewersInParallel(t *testing.T) {
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "a"}, {"r2", "b"}}, nil, ""), approvers("r1", "r2"))
	release1, release2 := h.Hold("r1"), h.Hold("r2")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 3)
	if n := countEvents(h, wire.EventKindAttemptCompleted); n != 1 {
		t.Fatalf("%d attempts completed while both reviewers are held, want 1 (the author)", n)
	}
	release1()
	release2()
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
}

func TestPoolConcurrencyTwoQueuesThird(t *testing.T) {
	roster := schedRoster("300s", "900s", []reviewer{{"r1", "p"}, {"r2", "p"}, {"r3", "p"}}, map[string]int{"p": 2}, "")
	h := enginetest.New(t, roster, approvers("r1", "r2", "r3"))
	release1, release2 := h.Hold("r1"), h.Hold("r2")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 3)
	waitParticipantState(t, h, "r3", engine.ParticipantQueued)
	for _, id := range []string{"r1", "r2"} {
		if s := participantState(t, h, id); s != engine.ParticipantRunning {
			t.Fatalf("%s is %s, want running", id, s)
		}
	}
	if n := len(h.Adapters["r3"].Calls()); n != 0 {
		t.Fatalf("queued reviewer was called %d times", n)
	}
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Pools {
		if p.Name == "p" && p.Concurrency != 2 {
			t.Fatalf("pool p concurrency = %d, want 2", p.Concurrency)
		}
	}
	release1()
	release2()
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	if n := len(h.Adapters["r3"].Calls()); n != 1 {
		t.Fatalf("r3 called %d times, want 1", n)
	}
}

func TestTurnDeadlineCountsQueueTime(t *testing.T) {
	h := enginetest.New(t, schedRoster("100s", "100s", []reviewer{{"r1", "p"}, {"r2", "p"}}, nil, ""), approvers("r1", "r2"))
	release := h.Hold("r1")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 2)
	waitParticipantState(t, h, "r2", engine.ParticipantQueued)

	h.Clock.Advance(101 * time.Second)
	wantEnd(t, h, turnID, engine.StateIncomplete, "deadline:turn")
	waitParticipantState(t, h, "r2", engine.ParticipantIdle)

	release()
	waitParticipantState(t, h, "r1", engine.ParticipantIdle)
	if n := len(h.Adapters["r2"].Calls()); n != 0 {
		t.Fatalf("dropped reviewer was called %d times", n)
	}
	if n := len(attemptsOf(h, turnID, "r2")); n != 0 {
		t.Fatalf("dropped reviewer has %d attempts", n)
	}
	if got := joinStates(stateEvents(h, "r2")); got != "queued,idle" {
		t.Fatalf("r2 states = %s", got)
	}
	if row, err := h.Store.GetTurn(turnID); err != nil || row.TerminalReason != "deadline:turn" {
		t.Fatalf("turn = %+v, err %v", row, err)
	}
}

func TestAttemptDeadlineOnHang(t *testing.T) {
	start := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindHang}}}
	h := enginetest.New(t, schedRoster("60s", "900s", []reviewer{{"r1", "p"}}, nil, ""), scripts, enginetest.ClockStart(start))
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	attemptID := attemptsOf(h, reply.TurnID, "author")[0]

	h.Clock.Advance(59 * time.Second)
	if row, err := h.Store.GetTurn(reply.TurnID); err != nil || row.TerminalState != "" {
		t.Fatalf("turn ended early: %+v, err %v", row, err)
	}
	h.Clock.Advance(2 * time.Second)
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "deadline:attempt:"+attemptID)

	// The engine gave up, but the author's slot is held until Complete
	// returns.
	if s := participantState(t, h, "author"); s != engine.ParticipantRunning {
		t.Fatalf("author is %s while its request is in flight", s)
	}
	if _, err := h.Adapters["author"].Cancel(context.Background(), attemptID); err != nil {
		t.Fatal(err)
	}
	waitParticipantState(t, h, "author", engine.ParticipantIdle)
	row := getAttempt(t, h, attemptID)
	if !row.Quarantined {
		t.Fatalf("late result not quarantined: %+v", row)
	}
}

func TestAttemptDeadlineClampedToTurn(t *testing.T) {
	h := enginetest.New(t, schedRoster("80s", "100s", []reviewer{{"r1", "p"}}, nil, ""), approvers("r1"))
	start := h.Clock.Now()
	release := h.Hold("author")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	// The author's attempt has the full 80s; 70s in, only 30s of turn remain
	// for the reviewer.
	h.Clock.Advance(70 * time.Second)
	release()
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	author := attemptsOf(h, turnID, "author")[0]
	if got, want := begunDeadline(t, h, author), start.Add(80*time.Second); !got.Equal(want) {
		t.Fatalf("author deadline = %v, want %v", got, want)
	}
	want := start.Add(100 * time.Second)
	id := attemptsOf(h, turnID, "r1")[0]
	if got := begunDeadline(t, h, id); !got.Equal(want) {
		t.Fatalf("attempt_begun deadline = %v, want %v", got, want)
	}
	calls := h.Adapters["r1"].Calls()
	if len(calls) != 1 || !calls[0].Request.Deadline.Equal(want) {
		t.Fatalf("request deadline = %+v, want %v", calls, want)
	}
	if row := getAttempt(t, h, id); row.Deadline == nil || !row.Deadline.Equal(want) {
		t.Fatalf("attempt row deadline = %v, want %v", row.Deadline, want)
	}
}

func TestPresetAttemptDeadlineOverride(t *testing.T) {
	extra := "\n[presets.solo.deadlines]\nattempt = \"45s\"\n"
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "p"}}, nil, extra), map[string][]fake.Step{"author": {enginetest.Reply("ok")}})
	start := h.Clock.Now()
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	id := attemptsOf(h, reply.TurnID, "author")[0]
	if got, want := begunDeadline(t, h, id), start.Add(45*time.Second); !got.Equal(want) {
		t.Fatalf("attempt_begun deadline = %v, want %v", got, want)
	}
}

func TestRepairDeadlineIsFreshButCappedByTurn(t *testing.T) {
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {enginetest.Reply(fake.VerdictProseApprove()), approveStep()},
	}
	h := enginetest.New(t, schedRoster("60s", "100s", []reviewer{{"r1", "p"}}, nil, ""), scripts)
	start := h.Clock.Now()
	release := h.Hold("r1")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 2)
	h.Clock.Advance(50 * time.Second)
	release()
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")

	attempts := attemptsOf(h, turnID, "r1")
	if len(attempts) != 2 {
		t.Fatalf("%d reviewer attempts, want 2", len(attempts))
	}
	if got, want := begunDeadline(t, h, attempts[0]), start.Add(60*time.Second); !got.Equal(want) {
		t.Fatalf("first deadline = %v, want %v", got, want)
	}
	// 50s in, a fresh 60s would end at 110s; the turn ends at 100s.
	if got, want := begunDeadline(t, h, attempts[1]), start.Add(100*time.Second); !got.Equal(want) {
		t.Fatalf("repair deadline = %v, want %v", got, want)
	}
}

func TestDeadlineEventsOncePerSecond(t *testing.T) {
	h := enginetest.New(t, schedRoster("300s", "900s", []reviewer{{"r1", "p"}}, nil, ""), approvers("r1"))
	release := h.Hold("r1")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 2)
	reviewerAttempt := attemptsOf(h, turnID, "r1")[0]

	h.Clock.Advance(10500 * time.Millisecond)
	var turnRemaining []int64
	attemptEvents := 0
	for _, ev := range h.Events() {
		p, ok := ev.Payload.(*wire.EvDeadline)
		if !ok || p.TurnID != turnID {
			continue
		}
		switch p.Kind {
		case "turn":
			turnRemaining = append(turnRemaining, p.RemainingMs)
		case "attempt":
			if p.AttemptID == nil {
				t.Fatalf("attempt deadline event without attempt id: %+v", p)
			}
			if *p.AttemptID == reviewerAttempt {
				attemptEvents++
			}
		}
	}
	if len(turnRemaining) != 10 || turnRemaining[0] != 899000 || turnRemaining[9] != 890000 {
		t.Fatalf("turn remaining_ms = %v, want ten events from 899000 to 890000", turnRemaining)
	}
	if attemptEvents != 10 {
		t.Fatalf("%d attempt deadline events for the running reviewer, want 10", attemptEvents)
	}
	release()
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
}

func TestNoTimerFiresAfterTurnEnds(t *testing.T) {
	h := enginetest.New(t, schedRoster("60s", "100s", []reviewer{{"r1", "p"}}, nil, ""), approvers("r1"))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	waitParticipantState(t, h, "r1", engine.ParticipantIdle)
	before := h.Events()
	row := waitEnded(t, h, turnID)

	h.Clock.Advance(10 * time.Hour)
	after := h.Events()
	if len(after) != len(before) {
		t.Fatalf("%d events after the turn ended, want none", len(after)-len(before))
	}
	again, err := h.Store.GetTurn(turnID)
	if err != nil || again.TerminalState != row.TerminalState || again.TerminalReason != row.TerminalReason {
		t.Fatalf("turn changed from %+v to %+v, err %v", row, again, err)
	}
}

func TestAttemptDeadlineOnHangReviewAuthorIdle(t *testing.T) {
	start := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	scripts := map[string][]fake.Step{"author": {{Kind: fake.StepKindHang}}}
	h := enginetest.New(t, schedRoster("60s", "900s", []reviewer{{"r1", "p"}}, nil, ""), scripts, enginetest.ClockStart(start))
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindAttemptBegun, 1)
	attemptID := attemptsOf(h, turnID, "author")[0]

	h.Clock.Advance(61 * time.Second)
	wantEnd(t, h, turnID, engine.StateIncomplete, "deadline:attempt:"+attemptID)
	if _, err := h.Adapters["author"].Cancel(context.Background(), attemptID); err != nil {
		t.Fatal(err)
	}
	waitParticipantState(t, h, "author", engine.ParticipantIdle)

	var states []string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvParticipantState); ok && p.Participant == "author" {
			states = append(states, p.State)
		}
	}
	want := []string{engine.ParticipantRunning, engine.ParticipantIdle}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("author participant_state events = %v, want %v", states, want)
	}
}
