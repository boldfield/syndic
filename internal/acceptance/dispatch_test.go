package acceptance

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/transport/openai"
	"github.com/boldfield/syndic/internal/wire"
)

const dispatch = "Dispatch failures"

func init() {
	cover(dispatch, "a step with `TransportStatus: unreachable` followed by a normal reply yields one journaled retry (`retry_count = 1`, two rows in `attempt_dispatches`) and a normal turn", "TestDispatch_UnreachableThenNormal")
	cover(dispatch, "two such steps yield `reviewer_failed:<id>:unreachable` without waiting for the deadline", "TestDispatch_TwoUnreachable")
	cover(dispatch, "a step with `TransportStatus: unauthorized` fails the turn at once with no retry", "TestDispatch_Unauthorized")
	cover(dispatch, "a step with `TransportStatus: reset` and status `interrupted` yields no retry and a capacity hold", "TestDispatch_ResetHoldsCapacity")
	cover(dispatch, "a step with `TransportStatus: malformed_body` yields `reviewer_failed:<id>:malformed_body` with no retry and no hold", "TestDispatch_MalformedBody")
	cover(dispatch, "a step returning an `ErrNotSent`-wrapped Go error yields an attempt with `transport_status: not_sent:<error>`, no hold, and an INCOMPLETE turn", "TestDispatch_NotSent")
	cover(dispatch, "a step returning a plain Go error yields status `interrupted`, `transport_status: adapter_error:<error>`, a capacity hold, and an INCOMPLETE turn", "TestDispatch_GoError")
	cover(dispatch, "the OpenAI adapter's contract test shows an unresolvable secret reference arrives wrapped in `ErrNotSent` and a body-read failure arrives unwrapped", "TestDispatch_OpenAIAdapterContract")
	cover(dispatch, "the next `submit` after any of these dispatches to the same participant again", "TestDispatch_NextSubmitDispatchesAgain")
}

// unreachableDelay is how long the engine waits, on its clock, before the one
// retry of an unreachable attempt. It is the engine's own default; the
// acceptance suite cannot set it.
const unreachableDelay = 500 * time.Millisecond

// failStep is a scripted reply that reports a transport failure.
func failStep(transportStatus string, status transport.CompletionStatus) fake.Step {
	return fake.Step{Kind: fake.StepKindReply, Status: status, TransportStatus: transportStatus}
}

// normal answers whatever it is asked: an approval of the candidate in a
// review, and the author's text otherwise (the author's work and the terminal
// deliveries that follow every review turn). Because it does not depend on
// the phase, which scripted step a delivery happens to consume does not
// matter to a test.
func normal() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase == "review" {
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		}
		return authorOK
	})
}

// repeat returns n normal steps.
func repeat(n int) []fake.Step {
	out := make([]fake.Step, n)
	for i := range out {
		out[i] = normal()
	}
	return out
}

// dispatchSuite starts the engine with a reviewer that plays steps first and
// is normal afterwards.
func dispatchSuite(t *testing.T, steps ...fake.Step) *suite {
	t.Helper()
	return newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(8),
		"reviewer": append(append([]fake.Step(nil), steps...), repeat(8)...),
	})
}

// reviewAttempt returns the turn's one review attempt of the reviewer.
func (s *suite) reviewAttempt(t *testing.T, turnID string) journal.AttemptRow {
	t.Helper()
	rows := s.attemptRows(t, turnID, "reviewer", "review")
	if len(rows) != 1 {
		t.Fatalf("reviewer had %d review attempts, want 1: %+v", len(rows), rows)
	}
	return rows[0]
}

// dispatchRows counts the attempt's rows in attempt_dispatches.
func (s *suite) dispatchRows(t *testing.T, attemptID string) int {
	t.Helper()
	var n int
	if err := s.DB.Conn().QueryRow(`SELECT COUNT(*) FROM attempt_dispatches WHERE attempt_id = ?`, attemptID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func retries(a journal.AttemptRow) int {
	if a.RetryCount == nil {
		return 0
	}
	return *a.RetryCount
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// holds returns the capacity holds in the pool, through status.
func (s *suite) holds(t *testing.T, c *client.Client, pool string) []wire.PoolHold {
	t.Helper()
	for _, p := range s.Status(t, c).Pools {
		if p.Name == pool {
			return p.Holds
		}
	}
	t.Fatalf("status lists no pool %q", pool)
	return nil
}

// wantNoHold requires that the attempt opened no capacity hold, in the
// journal and in status, and that no capacity event was announced.
func (s *suite) wantNoHold(t *testing.T, c *client.Client, attemptID string) {
	t.Helper()
	if _, found, err := s.Store.GetHold(attemptID); err != nil || found {
		t.Fatalf("hold for %s: found=%v err=%v, want none", attemptID, found, err)
	}
	if h := s.holds(t, c, "default"); len(h) != 0 {
		t.Fatalf("pool default holds %+v, want none", h)
	}
	for _, ev := range s.Events() {
		if cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity); ok {
			t.Fatalf("capacity event %+v, want none", cap)
		}
	}
}

// wantHold requires the attempt to hold the pool, in the journal and status,
// and a capacity event announcing it.
func (s *suite) wantHold(t *testing.T, c *client.Client, attemptID string) {
	t.Helper()
	if _, found, err := s.Store.GetHold(attemptID); err != nil || !found {
		t.Fatalf("hold for %s: found=%v err=%v, want one", attemptID, found, err)
	}
	holds := s.holds(t, c, "default")
	if len(holds) != 1 || holds[0].AttemptID != attemptID {
		t.Fatalf("pool default holds %+v, want one held by %s", holds, attemptID)
	}
	s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.HeldByAttempt == attemptID && cap.State == "uncertain"
	})
}

// wantReviewCalls requires the reviewer to have received n review requests.
func (s *suite) wantReviewCalls(t *testing.T, n int) []transport.CompleteRequest {
	t.Helper()
	calls := s.calls("reviewer", "review")
	if len(calls) != n {
		t.Fatalf("reviewer received %d review requests, want %d", len(calls), n)
	}
	return calls
}

// retryAfterDelay waits for the engine to wait out the unreachable retry
// delay on the fake clock, then moves the clock past it.
func (s *suite) retryAfterDelay(t *testing.T) {
	t.Helper()
	if err := s.Clock.WaitTimer(s.ctx(), unreachableDelay); err != nil {
		t.Fatalf("the engine never waited %v to retry: %v", unreachableDelay, err)
	}
	s.Clock.Advance(unreachableDelay)
}

func TestDispatch_UnreachableThenNormal(t *testing.T) {
	t.Parallel()
	s := dispatchSuite(t, failStep(transport.TransportStatusUnreachable, transport.StatusError))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.retryAfterDelay(t)
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	a := s.reviewAttempt(t, turnID)
	if retries(a) != 1 {
		t.Fatalf("retry_count = %d, want 1", retries(a))
	}
	if n := s.dispatchRows(t, a.ID); n != 2 {
		t.Fatalf("%d attempt_dispatches rows, want 2", n)
	}
	calls := s.wantReviewCalls(t, 2)
	if calls[0].Envelope != calls[1].Envelope {
		t.Fatalf("the retry changed the request: %+v vs %+v", calls[0].Envelope, calls[1].Envelope)
	}
	if a.Status == nil || *a.Status != string(transport.StatusComplete) || str(a.TransportStatus) != transport.TransportStatusOK {
		t.Fatalf("attempt ended %s/%s, want the retry's normal result", str(a.Status), str(a.TransportStatus))
	}
	s.wantNoHold(t, c, a.ID)
}

func TestDispatch_TwoUnreachable(t *testing.T) {
	t.Parallel()
	unreachable := failStep(transport.TransportStatusUnreachable, transport.StatusError)
	s := dispatchSuite(t, unreachable, unreachable)
	c := s.Dial(t)
	start := s.Clock.Now()
	turnID := s.Submit(t, c, "s1")
	s.retryAfterDelay(t)
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:unreachable")
	a := s.reviewAttempt(t, turnID)
	if retries(a) != 1 {
		t.Fatalf("retry_count = %d, want exactly one retry", retries(a))
	}
	if n := s.dispatchRows(t, a.ID); n != 2 {
		t.Fatalf("%d attempt_dispatches rows, want 2", n)
	}
	s.wantReviewCalls(t, 2)
	// The turn ended on the second failure, not on a deadline: the clock
	// moved only by the retry delay, far short of the attempt deadline.
	if elapsed := s.Clock.Now().Sub(start); elapsed != unreachableDelay {
		t.Fatalf("the clock moved %v, want only the %v retry delay", elapsed, unreachableDelay)
	}
	for _, ev := range s.Events() {
		if d, ok := body[wire.EvDeadline](ev, wire.EventKindDeadline); ok {
			t.Fatalf("deadline event %+v: the turn should have failed before any deadline", d)
		}
	}
	s.wantNoHold(t, c, a.ID)
	s.WaitEvent(func(ev event) bool {
		p, ok := body[wire.EvParticipantState](ev, wire.EventKindParticipantState)
		return ok && p.Participant == "reviewer" && p.State == "unreachable"
	})
}

func TestDispatch_Unauthorized(t *testing.T) {
	t.Parallel()
	s := dispatchSuite(t, failStep(transport.TransportStatusUnauthorized, transport.StatusError))
	c := s.Dial(t)
	start := s.Clock.Now()
	turnID := s.Submit(t, c, "s1")
	// The clock is never advanced: a failure that needed a retry delay
	// could not end the turn.
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:unauthorized")
	a := s.reviewAttempt(t, turnID)
	if retries(a) != 0 {
		t.Fatalf("retry_count = %d, want none", retries(a))
	}
	if n := s.dispatchRows(t, a.ID); n != 1 {
		t.Fatalf("%d attempt_dispatches rows, want 1", n)
	}
	s.wantReviewCalls(t, 1)
	if !s.Clock.Now().Equal(start) {
		t.Fatalf("the clock moved %v", s.Clock.Now().Sub(start))
	}
	if str(a.TransportStatus) != transport.TransportStatusUnauthorized {
		t.Fatalf("transport_status = %s", str(a.TransportStatus))
	}
	s.wantNoHold(t, c, a.ID)
}

func TestDispatch_ResetHoldsCapacity(t *testing.T) {
	t.Parallel()
	s := dispatchSuite(t, failStep(transport.TransportStatusReset, transport.StatusInterrupted))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:reset")
	a := s.reviewAttempt(t, turnID)
	if str(a.Status) != string(transport.StatusInterrupted) || str(a.TransportStatus) != transport.TransportStatusReset {
		t.Fatalf("attempt ended %s/%s, want interrupted/reset", str(a.Status), str(a.TransportStatus))
	}
	if retries(a) != 0 {
		t.Fatalf("retry_count = %d, want none", retries(a))
	}
	if n := s.dispatchRows(t, a.ID); n != 1 {
		t.Fatalf("%d attempt_dispatches rows, want 1", n)
	}
	s.wantReviewCalls(t, 1)
	s.wantHold(t, c, a.ID)
}

func TestDispatch_MalformedBody(t *testing.T) {
	t.Parallel()
	s := dispatchSuite(t, failStep(transport.TransportStatusMalformedBody, transport.StatusError))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:malformed_body")
	a := s.reviewAttempt(t, turnID)
	if str(a.Status) != string(transport.StatusError) || str(a.TransportStatus) != transport.TransportStatusMalformedBody {
		t.Fatalf("attempt ended %s/%s, want error/malformed_body", str(a.Status), str(a.TransportStatus))
	}
	if retries(a) != 0 {
		t.Fatalf("retry_count = %d, want none", retries(a))
	}
	if n := s.dispatchRows(t, a.ID); n != 1 {
		t.Fatalf("%d attempt_dispatches rows, want 1", n)
	}
	s.wantReviewCalls(t, 1)
	s.wantNoHold(t, c, a.ID)
}

func TestDispatch_NotSent(t *testing.T) {
	t.Parallel()
	cause := errors.New("no secret")
	s := dispatchSuite(t, fake.Step{Kind: fake.StepKindNotSent, Err: cause})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	// The adapter's error is the sentinel wrapping the cause; the attempt
	// records its text after not_sent:.
	wantStatus := "not_sent:" + fmt.Errorf("%w: %w", transport.ErrNotSent, cause).Error()
	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:"+wantStatus)
	a := s.reviewAttempt(t, turnID)
	if str(a.TransportStatus) != wantStatus {
		t.Fatalf("transport_status = %q, want %q", str(a.TransportStatus), wantStatus)
	}
	if n := s.dispatchRows(t, a.ID); n != 1 {
		t.Fatalf("%d attempt_dispatches rows, want 1", n)
	}
	s.wantReviewCalls(t, 1)
	s.wantNoHold(t, c, a.ID)
}

func TestDispatch_GoError(t *testing.T) {
	t.Parallel()
	s := dispatchSuite(t, fake.Step{Kind: fake.StepKindError, Err: errors.New("boom")})
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateIncomplete, "reviewer_failed:reviewer:adapter_error:boom")
	a := s.reviewAttempt(t, turnID)
	if str(a.Status) != string(transport.StatusInterrupted) || str(a.TransportStatus) != "adapter_error:boom" {
		t.Fatalf("attempt ended %s/%s, want interrupted/adapter_error:boom", str(a.Status), str(a.TransportStatus))
	}
	if retries(a) != 0 {
		t.Fatalf("retry_count = %d, want none", retries(a))
	}
	s.wantReviewCalls(t, 1)
	s.wantHold(t, c, a.ID)
}

// secretFailure is a resolver that cannot produce any secret.
type secretFailure struct{}

func (secretFailure) Resolve(ref config.SecretRef) (string, error) {
	return "", fmt.Errorf("no value for %s", ref.Name)
}

func TestDispatch_OpenAIAdapterContract(t *testing.T) {
	t.Parallel()
	req := transport.CompleteRequest{
		Envelope:        transport.AttemptEnvelope{Participant: "p", TurnID: "t", Phase: "review", AttemptID: "a", CandidateHash: "h"},
		Model:           "m",
		Messages:        []transport.Message{{Role: "user", Content: "hello"}},
		MaxOutputTokens: 100,
		Deadline:        time.Now().Add(10 * time.Second),
	}
	profile := func(url string, auth *config.SecretRef) config.EndpointProfile {
		return config.EndpointProfile{Name: "e", BaseURL: url, Auth: auth, ExtraHeaders: map[string]config.SecretRef{}, Dialect: "chat_completions_v1", MaxTokensField: "max_tokens"}
	}

	t.Run("an unresolvable secret reference is wrapped in ErrNotSent", func(t *testing.T) {
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
		t.Cleanup(srv.Close)
		a := openai.New(profile(srv.URL, &config.SecretRef{Source: "env", Name: "missing"}), secretFailure{})
		_, err := a.Complete(context.Background(), req)
		if !errors.Is(err, transport.ErrNotSent) {
			t.Fatalf("err = %v, want it to wrap ErrNotSent", err)
		}
		if n := hits.Load(); n != 0 {
			t.Fatalf("%d requests reached the server, want none", n)
		}
	})

	t.Run("a body-read failure arrives unwrapped", func(t *testing.T) {
		var hits atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write([]byte(`{"incomplete": "resp`))
		}))
		t.Cleanup(srv.Close)
		a := openai.New(profile(srv.URL, nil), secretFailure{})
		res, err := a.Complete(context.Background(), req)
		if errors.Is(err, transport.ErrNotSent) {
			t.Fatalf("err = %v: a failure after the request was sent was wrapped in ErrNotSent", err)
		}
		if n := hits.Load(); n != 1 {
			t.Fatalf("%d requests reached the server, want 1", n)
		}
		if err == nil && res.Status == transport.StatusComplete {
			t.Fatalf("a body that ended early was taken as complete: %+v", res)
		}
	})
}

func TestDispatch_NextSubmitDispatchesAgain(t *testing.T) {
	unreachable := failStep(transport.TransportStatusUnreachable, transport.StatusError)
	cases := []struct {
		name  string
		steps []fake.Step
		// retried: the first dispatch waits out the retry delay.
		retried bool
		// held: the first dispatch leaves a capacity hold to release.
		held bool
		// calls is how many review requests the first submit made.
		calls int
	}{
		{"unreachable then normal", []fake.Step{unreachable}, true, false, 2},
		{"two unreachable", []fake.Step{unreachable, unreachable}, true, false, 2},
		{"unauthorized", []fake.Step{failStep(transport.TransportStatusUnauthorized, transport.StatusError)}, false, false, 1},
		{"reset", []fake.Step{failStep(transport.TransportStatusReset, transport.StatusInterrupted)}, false, true, 1},
		{"malformed body", []fake.Step{failStep(transport.TransportStatusMalformedBody, transport.StatusError)}, false, false, 1},
		{"not sent", []fake.Step{{Kind: fake.StepKindNotSent, Err: errors.New("no secret")}}, false, false, 1},
		{"go error", []fake.Step{{Kind: fake.StepKindError, Err: errors.New("boom")}}, false, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := dispatchSuite(t, tc.steps...)
			c := s.Dial(t)
			first := s.Submit(t, c, "s1")
			if tc.retried {
				s.retryAfterDelay(t)
			}
			s.WaitTerminal(first)
			s.wantReviewCalls(t, tc.calls)

			if tc.held {
				holds := s.holds(t, c, "default")
				if len(holds) != 1 {
					t.Fatalf("holds = %+v, want one to release", holds)
				}
				if _, err := c.Release(s.ctx(), wire.ReleaseRequest{Pool: "default", Reason: "operator checked the endpoint"}); err != nil {
					t.Fatalf("release: %v", err)
				}
			}

			second := s.Submit(t, c, "s2")
			s.WaitTerminal(second)
			wantTurn(t, s.Turn(t, c, second), engine.StateApproved, "all_approved")
			calls := s.wantReviewCalls(t, tc.calls+1)
			if got := calls[tc.calls].Envelope; got.TurnID != second || got.Participant != "reviewer" {
				t.Fatalf("the next review request was %+v, want the reviewer asked again in turn %s", got, second)
			}
		})
	}
}
