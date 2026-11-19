package engine_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

func transportStep(status string, completion transport.CompletionStatus) fake.Step {
	return fake.Step{Kind: fake.StepKindReply, Status: completion, TransportStatus: status}
}

// shortRetry makes the unreachable retry wait a millisecond of fake time. It
// must run before enginetest.New.
func shortRetry(t *testing.T) {
	t.Helper()
	t.Cleanup(engine.SetRetryDelay(time.Millisecond))
}

// advanceUntilCalls moves the fake clock a millisecond at a time until the
// participant's adapter has been called n times, which is how a test releases
// the engine's retry wait.
func advanceUntilCalls(t *testing.T, h *enginetest.Harness, participant string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(h.Adapters[participant].Calls()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%s called %d times, want %d", participant, len(h.Adapters[participant].Calls()), n)
		}
		h.Clock.Advance(time.Millisecond)
		time.Sleep(time.Millisecond)
	}
}

func announcedStates(h *enginetest.Harness, id string) []string {
	var states []string
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvParticipantState); ok && p.Participant == id {
			states = append(states, p.State)
		}
	}
	return states
}

func dispatchRows(t *testing.T, h *enginetest.Harness, attemptID string) int {
	t.Helper()
	var n int
	if err := h.DB.Conn().QueryRow(`SELECT COUNT(*) FROM attempt_dispatches WHERE attempt_id = ?`, attemptID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type failCase struct {
	name          string
	step          fake.Step
	status        string // <status> of the terminal reason
	attemptStatus string
	transportPfx  string
	hold          bool
	state         string // participant state after the failure
}

var failCases = []failCase{
	{"unauthorized", transportStep(transport.TransportStatusUnauthorized, transport.StatusError), "unauthorized", "error", "unauthorized", false, "unauthorized"},
	{"model_unavailable", transportStep(transport.TransportStatusModelUnavailable, transport.StatusError), "model_unavailable", "error", "model_unavailable", false, "model_unavailable"},
	{"reset", transportStep(transport.TransportStatusReset, transport.StatusInterrupted), "reset", "interrupted", "reset", true, "unknown"},
	{"timeout", transportStep(transport.TransportStatusTimeout, transport.StatusInterrupted), "timeout", "interrupted", "timeout", true, "unknown"},
	{"cancelled not by the engine", transportStep(transport.TransportStatusCancelled, transport.StatusInterrupted), "reset", "interrupted", "reset", true, "unknown"},
	{"malformed_body", transportStep(transport.TransportStatusMalformedBody, transport.StatusError), "malformed_body", "error", "malformed_body", false, "idle"},
	{"body_too_large", transportStep(transport.TransportStatusBodyTooLarge, transport.StatusError), "body_too_large", "error", "body_too_large", false, "idle"},
	{"http_503", transportStep("http_503", transport.StatusError), "http_503", "error", "http_503", false, "idle"},
	{"not sent", fake.Step{Kind: fake.StepKindNotSent, Err: errors.New("no secret")}, "not_sent:", "error", "not_sent:", false, "idle"},
	{"go error", fake.Step{Kind: fake.StepKindError, Err: errors.New("boom")}, "adapter_error:", "interrupted", "adapter_error:", true, "unknown"},
}

// failRole is one place a dispatch can fail: the solo author, the author of a
// review turn, or a reviewer.
type failRole struct {
	name        string
	participant string
	reasonPfx   string
	scripts     func(fail fake.Step) map[string][]fake.Step
	submit      func(t *testing.T, h *enginetest.Harness, id string) string
	okState     string
	okReason    string
}

var failRoles = []failRole{
	{
		name: "solo author", participant: "author", reasonPfx: "author_failed:",
		scripts: func(fail fake.Step) map[string][]fake.Step {
			return map[string][]fake.Step{"author": {fail, enginetest.Reply("fine")}}
		},
		submit: func(t *testing.T, h *enginetest.Harness, id string) string {
			reply, err := h.Engine.Submit(soloReq(id))
			if err != nil {
				t.Fatal(err)
			}
			return reply.TurnID
		},
		okState: engine.StateSoloComplete, okReason: "solo_ok",
	},
	{
		name: "review author", participant: "author", reasonPfx: "author_failed:",
		scripts: func(fail fake.Step) map[string][]fake.Step {
			return map[string][]fake.Step{
				"author":   {fail, enginetest.Reply(authorText)},
				"reviewer": {approveStep()},
			}
		},
		submit:  submitReview,
		okState: engine.StateApproved, okReason: "all_approved",
	},
	{
		name: "reviewer", participant: "reviewer", reasonPfx: "reviewer_failed:reviewer:",
		scripts: func(fail fake.Step) map[string][]fake.Step {
			return map[string][]fake.Step{
				"author":   {enginetest.Reply(authorText), enginetest.Reply(authorText)},
				"reviewer": {fail, approveStep()},
			}
		},
		submit:  submitReview,
		okState: engine.StateApproved, okReason: "all_approved",
	},
}

func TestDispatchFailures(t *testing.T) {
	t.Cleanup(engine.DisableDelivery())
	for _, role := range failRoles {
		for _, tc := range failCases {
			t.Run(role.name+"/"+tc.name, func(t *testing.T) {
				h := enginetest.New(t, enginetest.RosterTOML, role.scripts(tc.step))
				turnID := role.submit(t, h, "s1")
				row := waitEnded(t, h, turnID)
				if row.TerminalState != engine.StateIncomplete || !strings.HasPrefix(row.TerminalReason, role.reasonPfx+tc.status) {
					t.Fatalf("turn ended %q %q, want incomplete %q...", row.TerminalState, row.TerminalReason, role.reasonPfx+tc.status)
				}
				if got := len(h.Adapters[role.participant].Calls()); got != 1 {
					t.Fatalf("adapter called %d times, want 1", got)
				}

				ids := attemptsOf(h, turnID, role.participant)
				if len(ids) != 1 {
					t.Fatalf("attempts = %v", ids)
				}
				a := getAttempt(t, h, ids[0])
				if a.Status == nil || *a.Status != tc.attemptStatus {
					t.Fatalf("attempt status = %v, want %s", a.Status, tc.attemptStatus)
				}
				if a.TransportStatus == nil || !strings.HasPrefix(*a.TransportStatus, tc.transportPfx) {
					t.Fatalf("attempt transport status = %v, want prefix %s", a.TransportStatus, tc.transportPfx)
				}
				if a.RetryCount != nil && *a.RetryCount != 0 {
					t.Fatalf("retry count = %d", *a.RetryCount)
				}
				if _, found := getHold(t, h, ids[0]); found != tc.hold {
					t.Fatalf("hold found = %v, want %v", found, tc.hold)
				}
				if tc.hold {
					if holds := poolHolds(t, h, "default"); len(holds) != 1 || holds[0].AttemptID != ids[0] {
						t.Fatalf("pool holds = %+v", holds)
					}
					if got := countEvents(h, wire.EventKindCapacity); got != 1 {
						t.Fatalf("%d capacity events, want 1", got)
					}
					if n := len(h.Adapters[role.participant].Calls()); n != 1 {
						t.Fatalf("calls = %d", n)
					}
				} else if holds := poolHolds(t, h, "default"); len(holds) != 0 {
					t.Fatalf("pool holds = %+v", holds)
				}
				if got := participantState(t, h, role.participant); got != tc.state {
					t.Fatalf("participant state = %q, want %q", got, tc.state)
				}

				if tc.hold {
					// running goes straight to unknown, with no idle between.
					var tail []string
					for _, st := range announcedStates(h, role.participant) {
						if st != "queued" {
							tail = append(tail, st)
						}
					}
					if n := len(tail); n < 2 || tail[n-1] != "unknown" || tail[n-2] != "running" {
						t.Fatalf("announced states = %v, want running then unknown", tail)
					}
				}

				// A held slot is the operator's to give back; after that the
				// next submit dispatches to the same participant and succeeds.
				if tc.hold {
					if _, err := h.Engine.Release(wire.ReleaseRequest{Pool: "default", Reason: "test"}); err != nil {
						t.Fatal(err)
					}
				}
				turn2 := role.submit(t, h, "s2")
				wantEnd(t, h, turn2, role.okState, role.okReason)
				if got := participantState(t, h, role.participant); got != "idle" {
					t.Fatalf("participant state after success = %q, want idle", got)
				}
			})
		}
	}
}

func TestDispatchStateAnnounced(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {
		transportStep(transport.TransportStatusUnauthorized, transport.StatusError),
		enginetest.Reply("fine"),
	}})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "author_failed:unauthorized")
	if got := participantState(t, h, "author"); got != "unauthorized" {
		t.Fatalf("state = %q", got)
	}
	reply, err = h.Engine.Submit(soloReq("s2"))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateSoloComplete, "solo_ok")
	if got := participantState(t, h, "author"); got != "idle" {
		t.Fatalf("state = %q", got)
	}
	want := []string{"running", "unauthorized", "running", "idle"}
	got := announcedStates(h, "author")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("announced %v, want %v", got, want)
	}
}

func TestDispatchHeldSlotBlocksNextDispatch(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {
		transportStep(transport.TransportStatusReset, transport.StatusInterrupted),
		enginetest.Reply("fine"),
	}})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "author_failed:reset")
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatal(err)
	}
	waitParticipantState(t, h, "author", "queued")
	if n := len(h.Adapters["author"].Calls()); n != 1 {
		t.Fatalf("calls = %d, want the held slot to keep the second dispatch back", n)
	}
	if _, err := h.Engine.Release(wire.ReleaseRequest{Pool: "default", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	waitCount(t, h, wire.EventKindTurnState, 6)
}

func TestUnreachableRetriedOnce(t *testing.T) {
	t.Cleanup(engine.DisableDelivery())
	for _, role := range failRoles {
		t.Run(role.name, func(t *testing.T) {
			shortRetry(t)
			h := enginetest.New(t, enginetest.RosterTOML, role.scripts(transportStep(transport.TransportStatusUnreachable, transport.StatusError)))
			turnID := role.submit(t, h, "s1")
			advanceUntilCalls(t, h, role.participant, 2)
			wantEnd(t, h, turnID, role.okState, role.okReason)

			calls := h.Adapters[role.participant].Calls()
			if len(calls) != 2 {
				t.Fatalf("calls = %d", len(calls))
			}
			if calls[0].Request.Envelope != calls[1].Request.Envelope || !calls[0].Request.Deadline.Equal(calls[1].Request.Deadline) {
				t.Fatalf("retry changed the request: %+v vs %+v", calls[0].Request, calls[1].Request)
			}
			ids := attemptsOf(h, turnID, role.participant)
			if len(ids) != 1 {
				t.Fatalf("attempts = %v, want one row", ids)
			}
			a := getAttempt(t, h, ids[0])
			if a.RetryCount == nil || *a.RetryCount != 1 {
				t.Fatalf("retry count = %v", a.RetryCount)
			}
			if n := dispatchRows(t, h, ids[0]); n != 2 {
				t.Fatalf("%d attempt_dispatches rows, want 2", n)
			}
			if got := participantState(t, h, role.participant); got != "idle" {
				t.Fatalf("state = %q", got)
			}
		})
	}
}

func TestUnreachableTwice(t *testing.T) {
	t.Cleanup(engine.DisableDelivery())
	for _, role := range failRoles {
		t.Run(role.name, func(t *testing.T) {
			shortRetry(t)
			unreachable := transportStep(transport.TransportStatusUnreachable, transport.StatusError)
			scripts := role.scripts(unreachable)
			scripts[role.participant] = append([]fake.Step{unreachable}, scripts[role.participant]...)
			h := enginetest.New(t, enginetest.RosterTOML, scripts)
			start := h.Clock.Now()
			turnID := role.submit(t, h, "s1")
			advanceUntilCalls(t, h, role.participant, 2)
			wantEnd(t, h, turnID, engine.StateIncomplete, role.reasonPfx+"unreachable")

			if n := len(h.Adapters[role.participant].Calls()); n != 2 {
				t.Fatalf("calls = %d, want 2", n)
			}
			if elapsed := h.Clock.Now().Sub(start); elapsed > time.Minute {
				t.Fatalf("clock moved %v; the turn should fail without reaching a deadline", elapsed)
			}
			ids := attemptsOf(h, turnID, role.participant)
			a := getAttempt(t, h, ids[0])
			if a.Status == nil || *a.Status != "error" || a.RetryCount == nil || *a.RetryCount != 1 {
				t.Fatalf("attempt = status %v retry %v", a.Status, a.RetryCount)
			}
			if _, found := getHold(t, h, ids[0]); found {
				t.Fatal("unreachable opened a hold")
			}
			if got := participantState(t, h, role.participant); got != "unreachable" {
				t.Fatalf("state = %q", got)
			}
			// Nothing is cached: the next submit dispatches again.
			turn2 := role.submit(t, h, "s2")
			wantEnd(t, h, turn2, role.okState, role.okReason)
			if got := participantState(t, h, role.participant); got != "idle" {
				t.Fatalf("state = %q", got)
			}
		})
	}
}

func TestUnreachableThenResetHoldsCapacity(t *testing.T) {
	shortRetry(t)
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {
		transportStep(transport.TransportStatusUnreachable, transport.StatusError),
		transportStep(transport.TransportStatusReset, transport.StatusInterrupted),
	}})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	advanceUntilCalls(t, h, "author", 2)
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "author_failed:reset")
	ids := attemptsOf(h, reply.TurnID, "author")
	if _, found := getHold(t, h, ids[0]); !found {
		t.Fatal("no hold after a reset on the retry")
	}
}

func TestUnreachableNotRetriedWithoutRoom(t *testing.T) {
	// A delay longer than the 900s turn deadline leaves no room.
	t.Cleanup(engine.SetRetryDelay(20 * time.Minute))
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {
		transportStep(transport.TransportStatusUnreachable, transport.StatusError),
		enginetest.Reply("never asked"),
	}})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateIncomplete, "author_failed:unreachable")
	if n := len(h.Adapters["author"].Calls()); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
	a := attemptRow(t, h, reply.TurnID)
	if a.RetryCount != nil && *a.RetryCount != 0 {
		t.Fatalf("retry count = %d", *a.RetryCount)
	}
	if n := dispatchRows(t, h, a.ID); n != 1 {
		t.Fatalf("%d dispatch rows", n)
	}
	if got := participantState(t, h, "author"); got != "unreachable" {
		t.Fatalf("state = %q", got)
	}
}

func TestUnreachableNotRetriedAfterCancel(t *testing.T) {
	shortRetry(t)
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {
		transportStep(transport.TransportStatusUnreachable, transport.StatusError),
		enginetest.Reply("never asked"),
	}})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(h.Adapters["author"].Calls()) < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := h.Engine.Cancel(reply.TurnID); err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(time.Second)
	time.Sleep(50 * time.Millisecond)
	h.Clock.Advance(time.Second)
	if n := len(h.Adapters["author"].Calls()); n != 1 {
		t.Fatalf("calls = %d, want the cancelled turn not retried", n)
	}
	a := attemptRow(t, h, reply.TurnID)
	if n := dispatchRows(t, h, a.ID); n != 1 {
		t.Fatalf("%d dispatch rows, want 1", n)
	}
	if a.RetryCount != nil && *a.RetryCount != 0 {
		t.Fatalf("retry count = %d", *a.RetryCount)
	}
}
