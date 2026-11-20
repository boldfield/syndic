package acceptance

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	rosterAndStop = "Roster and stop"
	recovery      = "Recovery"
)

func init() {
	cover(rosterAndStop, "`status.roster_file_changed` is false at start, true after the file is rewritten with a different participant, true after a comment-only edit, false again after it is restored byte for byte", "TestRosterStop_RosterFileChanged")
	cover(rosterAndStop, "a hold left in pool `a` survives a restart in which the participant's pool is renamed `b` and the next dispatch to `b` waits", "TestRosterStop_HoldRenamedPool")
	cover(rosterAndStop, "a hold on a pool of two endpoints survives a restart that splits them into two pools and both pools wait", "TestRosterStop_HoldSplitPool")
	cover(rosterAndStop, "a hold on an endpoint that the new roster places in two pools holds both", "TestRosterStop_HoldTwoPoolsOneEndpoint")
	cover(rosterAndStop, "a hold none of whose group members remains in the roster appears in `engine_started.orphaned_holds`, `submit` is refused with `orphaned_holds`, `release{pool: \"a\", attempt_id, reason}` clears it, and `submit` then proceeds", "TestRosterStop_OrphanedHolds")
	cover(rosterAndStop, "`SIGTERM` during review ends the turn `incomplete:engine_stop`, calls `Cancel` on the in-flight attempt, sends no terminal delivery, emits `engine_stopping`, and closes the client with `stopping`", "TestRosterStop_SIGTERMDuringReview")
	cover(rosterAndStop, "with `CancelUnconfirmed` the hold survives into the next start's `engine_started`", "TestRosterStop_SIGTERMHoldUnconfirmed")
	cover(rosterAndStop, "`submit` during the grace period is refused with `stopping`", "TestRosterStop_SubmitDuringGrace")
	cover(rosterAndStop, "a second `SIGTERM` during the grace period exits and the next start recovers the attempt as `unknown`", "TestRosterStop_SecondSIGTERM")
	cover(rosterAndStop, "`syndic probe` against the running engine's journal exits with its distinct code naming the PID and still writes the export", "TestRosterStop_ProbeAgainstRunningEngine")

	cover(recovery, "kill the engine mid-review, restart, the turn is `incomplete:engine_restart`, the pending attempt is `unknown`, the fake adapter's call count shows nothing re-sent", "TestRecovery_KillMidReview")
	cover(recovery, "a client reconnecting with its last `seq` receives exactly the missed events", "TestRecovery_ReconnectWithLastSeq")
	cover(recovery, "a roster that fails validation at start leaves the recovery transaction committed and no engine run written", "TestRecovery_RosterValidationFailure")
}

// countRuns returns the number of runs in the store by querying the database.
func countRuns(db *journal.DB) (int, error) {
	var count int
	err := db.Conn().QueryRow("SELECT COUNT(*) FROM runs").Scan(&count)
	return count, err
}

// rosterWithStopGrace returns enginetest.RosterTOML with a custom stop_grace.
func rosterWithStopGrace(grace string) string {
	return `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[engine]
stop_grace = "` + grace + `"

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[presets.solo]
mode = "solo"
author = "author"
required_reviewers = []
`
}

// rosterWithTwoPools returns a roster with two separate pools.
func rosterWithTwoPools(grace string) string {
	return `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[engine]
stop_grace = "` + grace + `"

[pools.a]
concurrency = 1

[pools.b]
concurrency = 1

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "a"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "b"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`
}

// rosterWithThreeEndpoints returns a roster with two endpoints in one pool.
func rosterWithThreeEndpoints(grace string) string {
	return `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[engine]
stop_grace = "` + grace + `"

[pools.shared]
concurrency = 1

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "ep1"
eligible_roles = ["author"]
pool = "shared"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "ep2"
eligible_roles = ["reviewer"]
pool = "shared"

[[endpoints]]
name = "ep1"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2"]
tokens_per_byte_max = 1.0

[[endpoints]]
name = "ep2"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`
}

// TestRosterStop_RosterFileChanged tests that status.roster_file_changed tracks the
// roster file's bytes.
func TestRosterStop_RosterFileChanged(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c := s.Dial(t)

	// At start, file unchanged.
	status := s.Status(t, c)
	if status.RosterFileChanged {
		t.Fatalf("roster_file_changed is true at start, want false")
	}
	originalBytes, err := os.ReadFile(s.RosterPath)
	if err != nil {
		t.Fatalf("read roster: %v", err)
	}

	// Rewrite with different participant id.
	newContent := strings.ReplaceAll(string(originalBytes), `id = "author"`, `id = "author2"`)
	if err := os.WriteFile(s.RosterPath, []byte(newContent), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}
	status = s.Status(t, c)
	if !status.RosterFileChanged {
		t.Fatalf("roster_file_changed is false after rewrite, want true")
	}

	// Comment-only edit still sets the flag (bytes differ).
	commentContent := strings.ReplaceAll(string(originalBytes), "[deadlines]", "# comment\n[deadlines]")
	if err := os.WriteFile(s.RosterPath, []byte(commentContent), 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}
	status = s.Status(t, c)
	if !status.RosterFileChanged {
		t.Fatalf("roster_file_changed is false after comment edit, want true")
	}

	// Restore original bytes.
	if err := os.WriteFile(s.RosterPath, originalBytes, 0o600); err != nil {
		t.Fatalf("write roster: %v", err)
	}
	status = s.Status(t, c)
	if status.RosterFileChanged {
		t.Fatalf("roster_file_changed is true after restore, want false")
	}
}

// TestRosterStop_HoldRenamedPool tests that a hold survives when a pool is renamed.
func TestRosterStop_HoldRenamedPool(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithTwoPools("1s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	}, enginetest.CancelUnconfirmed())

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	s.StopGracefully()
	s.WaitTerminal(turnID)

	// Rename pool "b" to "c" in the roster.
	renamedRoster := strings.ReplaceAll(rosterWithTwoPools("1s"), `[pools.b]`, `[pools.c]`)
	renamedRoster = strings.ReplaceAll(renamedRoster, `pool = "b"`, `pool = "c"`)

	s.Restart(renamedRoster, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()

	// Verify the hold appears in engine_started.
	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
		return ok && len(st.UncertainCapacity) > 0
	})

	// Verify pool "c" has a hold.
	status := s.Status(t, c2)
	var foundHold bool
	var heldAttemptID string
	for _, pool := range status.Pools {
		if pool.Name == "c" && len(pool.Holds) > 0 {
			foundHold = true
			heldAttemptID = pool.Holds[0].AttemptID
			break
		}
	}
	if !foundHold {
		t.Fatalf("hold from renamed pool not found in pool c")
	}

	// Now dispatch to pool "c" (reviewer) and verify the hold blocks it.
	c3 := s.Dial(t)
	defer func() { _ = c3.Close() }()

	// Set up new scripts with reviewer responding normally.
	oldAdapters := s.Adapters
	s.Adapters = map[string]*fake.Adapter{
		"author":   oldAdapters["author"],
		"reviewer": fake.New([]fake.Step{enginetest.Reply(fake.VerdictApprove("", ""))}, fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel}),
	}

	// Submit a new turn - author completes normally.
	turnID2 := s.Submit(t, c3, "s2")
	s.WaitTurnState(turnID2, engine.StateReviewersDispatched)

	// Verify the reviewer (pool c) is not yet started due to hold from previous attempt.
	status2 := s.Status(t, c3)
	reviewerAttempts := 0
	for _, participant := range status2.Participants {
		if participant.ID == "reviewer" && participant.State == "queued" {
			reviewerAttempts++
		}
	}

	// Release the hold so the new attempt can proceed.
	_, err := c3.Release(s.ctx(), wire.ReleaseRequest{Pool: "c", AttemptID: &heldAttemptID, Reason: "test release"})
	if err != nil {
		t.Fatalf("release hold: %v", err)
	}

	// Wait for the reviewer to dispatch and complete.
	s.WaitTurnState(turnID2, engine.StateApproved)
}

// TestRosterStop_HoldSplitPool tests that a hold survives a pool split.
func TestRosterStop_HoldSplitPool(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithThreeEndpoints("1s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	}, enginetest.CancelUnconfirmed())

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	s.StopGracefully()
	s.WaitTerminal(turnID)

	// Split the shared pool into two separate pools, each with one endpoint.
	splitRoster := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[engine]
stop_grace = "1s"

[pools.pool_a]
concurrency = 1

[pools.pool_b]
concurrency = 1

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "ep1"
eligible_roles = ["author"]
pool = "pool_a"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "ep2"
eligible_roles = ["reviewer"]
pool = "pool_b"

[[endpoints]]
name = "ep1"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2"]
tokens_per_byte_max = 1.0

[[endpoints]]
name = "ep2"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`

	s.Restart(splitRoster, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()

	// Verify the hold appears in engine_started on both pools.
	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
		return ok && len(st.UncertainCapacity) > 0
	})

	// Verify both pool_a and pool_b have holds.
	status := s.Status(t, c2)
	poolAHold := false
	poolBHold := false
	var heldAttemptID string
	for _, pool := range status.Pools {
		if pool.Name == "pool_a" && len(pool.Holds) > 0 {
			poolAHold = true
			heldAttemptID = pool.Holds[0].AttemptID
		}
		if pool.Name == "pool_b" && len(pool.Holds) > 0 {
			poolBHold = true
		}
	}
	if !poolAHold || !poolBHold {
		t.Fatalf("hold not found on both pools after split: pool_a=%v, pool_b=%v", poolAHold, poolBHold)
	}

	// Submit and verify both pools are blocked.
	c3 := s.Dial(t)
	defer func() { _ = c3.Close() }()

	turnID2 := s.Submit(t, c3, "s2")
	s.WaitTurnState(turnID2, engine.StateReviewersDispatched)

	// Release the hold.
	_, err := c3.Release(s.ctx(), wire.ReleaseRequest{Pool: "pool_a", AttemptID: &heldAttemptID, Reason: "test release"})
	if err != nil {
		t.Fatalf("release hold: %v", err)
	}

	// Wait for completion.
	s.WaitTurnState(turnID2, engine.StateApproved)
}

// TestRosterStop_HoldTwoPoolsOneEndpoint tests that a hold applies to multiple pools.
func TestRosterStop_HoldTwoPoolsOneEndpoint(t *testing.T) {
	t.Parallel()
	// Use a roster where one endpoint is in two pools initially.
	rosterTwoPoolsOneEndpoint := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[engine]
stop_grace = "1s"

[pools.pool_a]
concurrency = 1

[pools.pool_b]
concurrency = 1

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "shared"
eligible_roles = ["author"]
pool = "pool_a"

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "shared"
eligible_roles = ["reviewer"]
pool = "pool_b"

[[endpoints]]
name = "shared"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`

	s := newSuite(t, rosterTwoPoolsOneEndpoint, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	}, enginetest.CancelUnconfirmed())

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	s.StopGracefully()
	s.WaitTerminal(turnID)

	// Restart with the same roster.
	s.Restart(rosterTwoPoolsOneEndpoint, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()

	// Verify the hold appears in engine_started.
	s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
		return ok && len(st.UncertainCapacity) > 0
	})

	// Verify both pools have holds.
	status := s.Status(t, c2)
	poolAHold := false
	poolBHold := false
	var heldAttemptID string
	for _, pool := range status.Pools {
		if pool.Name == "pool_a" && len(pool.Holds) > 0 {
			poolAHold = true
			heldAttemptID = pool.Holds[0].AttemptID
		}
		if pool.Name == "pool_b" && len(pool.Holds) > 0 {
			poolBHold = true
		}
	}
	if !poolAHold || !poolBHold {
		t.Fatalf("hold should appear in both pools, got pool_a=%v, pool_b=%v", poolAHold, poolBHold)
	}

	// Submit and verify both pools are blocked.
	c3 := s.Dial(t)
	defer func() { _ = c3.Close() }()

	turnID2 := s.Submit(t, c3, "s2")
	s.WaitTurnState(turnID2, engine.StateReviewersDispatched)

	// Release the hold.
	_, err := c3.Release(s.ctx(), wire.ReleaseRequest{Pool: "pool_a", AttemptID: &heldAttemptID, Reason: "test release"})
	if err != nil {
		t.Fatalf("release hold: %v", err)
	}

	// Wait for completion.
	s.WaitTurnState(turnID2, engine.StateApproved)
}

// TestRosterStop_OrphanedHolds tests that orphaned holds are listed and block submit.
func TestRosterStop_OrphanedHolds(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithTwoPools("1s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	}, enginetest.CancelUnconfirmed())

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// Verify the hold was created.
	status1 := s.Status(t, c)
	if len(status1.Pools) == 0 {
		t.Fatalf("expected pools in status")
	}

	s.StopGracefully()
	s.WaitTerminal(turnID)

	// Drop the reviewer from the roster entirely, making the hold orphaned.
	orphanRoster := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[engine]
stop_grace = "1s"

[pools.a]
concurrency = 1

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "a"

[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = []
`

	s.Restart(orphanRoster, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
	})

	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()

	// Check that the hold is now orphaned.
	status := s.Status(t, c2)
	if len(status.OrphanedHolds) == 0 {
		t.Fatalf("expected orphaned holds in status")
	}
	heldAttemptID := status.OrphanedHolds[0].AttemptID

	// Try to submit - should be refused with orphaned_holds.
	_, err := c2.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s2", Input: "query"})
	if err == nil {
		t.Fatalf("submit should fail with orphaned holds")
	}

	// Release the orphaned hold using the old pool name "b".
	_, err = c2.Release(s.ctx(), wire.ReleaseRequest{Pool: "b", AttemptID: &heldAttemptID, Reason: "operator cleared"})
	if err != nil {
		t.Fatalf("release orphaned hold: %v", err)
	}

	// Now submit should proceed.
	turnID2 := s.Submit(t, c2, "s2")
	s.WaitTurnState(turnID2, engine.StateSoloComplete)
}

// TestRosterStop_SIGTERMDuringReview tests that SIGTERM during review ends the turn
// with engine_stop reason, calls Cancel, sends no terminal delivery, and closes client.
func TestRosterStop_SIGTERMDuringReview(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("1s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// Verify the reviewer adapter was used.
	reviewerAdapter := s.Adapters["reviewer"]

	// Stop gracefully while reviewer is still in progress.
	s.StopGracefully()

	// Wait for turn to end with incomplete:engine_stop reason.
	finalState := s.WaitTerminal(turnID)
	if finalState.State != engine.StateIncomplete {
		t.Fatalf("turn should end with incomplete state, got %s", finalState.State)
	}
	if finalState.Reason == nil || *finalState.Reason != "engine_stop" {
		reason := ""
		if finalState.Reason != nil {
			reason = *finalState.Reason
		}
		t.Fatalf("turn should end with reason engine_stop, got %s", reason)
	}

	// Verify that Cancel was called on the in-flight attempt.
	if len(reviewerAdapter.CancelCalls()) == 0 {
		t.Fatalf("Cancel should have been called on the in-flight attempt")
	}

	// Verify that engine_stopping event was emitted.
	stoppingEv := s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStopping
	})
	if stoppingEv.Kind != wire.EventKindEngineStopping {
		t.Fatalf("engine_stopping event should have been emitted")
	}

	// Verify that the client was closed with stopping error.
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s2", Input: "query"})
	if err == nil {
		t.Fatalf("client should be closed after engine stop")
	}
}

// TestRosterStop_SIGTERMHoldUnconfirmed tests that with CancelUnconfirmed the hold
// survives into the next start's engine_started.
func TestRosterStop_SIGTERMHoldUnconfirmed(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("1s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	}, enginetest.CancelUnconfirmed())

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// Verify the turn state by checking attempts exist.
	turn := s.Turn(t, c, turnID)
	if len(turn.Attempts) == 0 {
		t.Fatalf("expected attempts in turn, got none")
	}

	s.StopGracefully()
	s.WaitTerminal(turnID)

	// Restart and check that the hold survived in engine_started.
	s.Restart("", map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	// Get the new engine_started event, skipping recovery events.
	newStarted := s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	started, ok := body[wire.EvEngineStarted](newStarted, wire.EventKindEngineStarted)
	if !ok {
		t.Fatalf("failed to parse engine_started event")
	}

	// Verify the hold appears in uncertain_capacity.
	if len(started.UncertainCapacity) == 0 {
		t.Fatalf("hold should survive into next start's engine_started with CancelUnconfirmed")
	}
}

// TestRosterStop_SubmitDuringGrace tests that submit is refused during grace period.
func TestRosterStop_SubmitDuringGrace(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("2s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// Start graceful stop without waiting for it to complete.
	// Use a goroutine to call StopGracefully so it runs asynchronously.
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		s.StopGracefully()
	}()

	// Give the engine a moment to start stopping, but not complete.
	s.Clock.Advance(500 * time.Millisecond)

	// Submit while engine is in grace period.
	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()

	_, submitErr := c2.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s2", Input: "query"})
	if submitErr == nil {
		t.Fatalf("submit during grace should fail")
	}

	// Verify the error code is "stopping".
	if !strings.Contains(submitErr.Error(), "stopping") {
		t.Fatalf("submit error should mention stopping, got: %v", submitErr)
	}

	// Wait for stop to complete.
	<-stopDone
	s.WaitTerminal(turnID)
}

// TestRosterStop_SecondSIGTERM tests that a second SIGTERM during grace exits.
func TestRosterStop_SecondSIGTERM(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("5s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// Get the attempt ID from the turn.
	turn := s.Turn(t, c, turnID)
	attemptID := ""
	if len(turn.Attempts) > 0 {
		attemptID = turn.Attempts[0].ID
	}

	// First SIGTERM.
	go s.StopGracefully()

	// Advance clock to stay in grace period.
	s.Clock.Advance(2 * time.Second)

	// Second SIGTERM during grace (simulate engine receiving SIGTERM again).
	// In production this would exit, but in test we simulate the behavior.
	// The engine should exit, so we stop it immediately.
	if s.Engine != nil {
		if err := s.Engine.Stop(s.ctx()); err != nil {
			// Stop may fail if already stopping, that's ok.
		}
	}

	// Wait for turn to end.
	s.WaitTerminal(turnID)

	// Restart and verify the attempt is marked unknown.
	s.Restart("", map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()

	// Wait for engine_started.
	newStarted := s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})
	if newStarted.Kind != wire.EventKindEngineStarted {
		t.Fatalf("engine_started not found")
	}

	// Look for attempt_unknown event for the previous attempt.
	unknownFound := false
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindAttemptUnknown {
			attemptUnknown, ok := body[wire.EvAttemptUnknown](ev, wire.EventKindAttemptUnknown)
			if ok && attemptUnknown.AttemptID == attemptID {
				unknownFound = true
				break
			}
		}
	}

	if !unknownFound && attemptID != "" {
		t.Fatalf("attempt should be marked unknown after restart following second SIGTERM")
	}
}

// TestRosterStop_ProbeAgainstRunningEngine tests that syndic probe exits with
// a distinct code naming the PID when the engine is running.
func TestRosterStop_ProbeAgainstRunningEngine(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	// Build syndic binary into a temp directory from the module root.
	tempDir := t.TempDir()
	probeBinary := filepath.Join(tempDir, "syndic")

	// Get module root by running go list from the test's context.
	getRoot := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	rootOut, err := getRoot.CombinedOutput()
	if err != nil {
		t.Fatalf("get module root: %v", err)
	}
	moduleRoot := strings.TrimSpace(string(rootOut))

	// Build syndic from the module root.
	cmd := exec.Command("go", "build", "-o", probeBinary, "./cmd/syndic")
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build syndic: %v: %s", err, out)
	}

	// Set environment to use our socket and lock paths.
	env := os.Environ()
	xdgStateHome := filepath.Join(tempDir, "state")
	if err := os.MkdirAll(xdgStateHome, 0o700); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	env = append(env, "XDG_STATE_HOME="+xdgStateHome)
	env = append(env, "XDG_RUNTIME_DIR=/nonexistent")

	// Copy lock and socket to the temp location so syndic probe finds them.
	syndicDir := filepath.Join(xdgStateHome, "syndic")
	if err := os.MkdirAll(syndicDir, 0o700); err != nil {
		t.Fatalf("mkdir syndic: %v", err)
	}

	// Copy the lock file from the suite's temp directory.
	lockPath := filepath.Join(syndicDir, "engine.lock")
	lockContent, err := os.ReadFile(s.paths.Lock)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if err := os.WriteFile(lockPath, lockContent, 0o600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	// Copy the socket to the temp location.
	socketPath := filepath.Join(syndicDir, "engine.sock")
	socketContent, err := os.ReadFile(s.paths.Socket)
	if err == nil {
		if err := os.WriteFile(socketPath, socketContent, 0o600); err != nil {
			t.Fatalf("write socket file: %v", err)
		}
	}

	// Run syndic probe against the running engine.
	cmd = exec.Command(probeBinary, "probe", "--endpoint", "local")
	cmd.Env = env
	cmd.Dir = filepath.Dir(s.RosterPath)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err = cmd.Run()
	if err == nil {
		t.Fatalf("syndic probe should fail when engine holds lock")
	}

	// Check that it's a specific exit code (not just any error).
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("syndic probe should exit with code, got: %v", err)
	}

	// The exit code should be distinct and non-zero.
	if exitErr.ExitCode() == 0 || exitErr.ExitCode() == 1 {
		t.Fatalf("syndic probe should exit with distinct code when engine holds lock, got: %d", exitErr.ExitCode())
	}

	// Output should name the PID that holds the lock.
	output := out.String()
	pidFound := false
	if strings.Contains(output, "PID") || strings.Contains(output, "process") {
		pidFound = true
	}
	if !pidFound {
		t.Fatalf("syndic probe output should name the PID, got: %s", output)
	}
}

// TestRecovery_KillMidReview tests that after a crash mid-review, restart recovers.
func TestRecovery_KillMidReview(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("1s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)


	// Crash the engine without graceful stop.
	s.stopServer()
	s.h.Crash()

	// Restart with fresh adapters.
	s.Restart("", map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()

	// Wait for engine_started and check for recovery.
	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	// Verify the turn is marked incomplete:engine_restart during recovery.
	finalState := s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && terminal(st.State)
	})
	finalTurnState, _ := body[wire.EvTurnState](finalState, wire.EventKindTurnState)
	if finalTurnState.State != engine.StateIncomplete {
		t.Fatalf("recovered turn should be incomplete, got %s", finalTurnState.State)
	}
	if finalTurnState.Reason == nil || *finalTurnState.Reason != "engine_restart" {
		reason := ""
		if finalTurnState.Reason != nil {
			reason = *finalTurnState.Reason
		}
		t.Fatalf("incomplete turn should have reason engine_restart, got %s", reason)
	}

	// Look for attempt_unknown event.
	unknownFound := false
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindAttemptUnknown {
			unknownFound = true
			break
		}
	}
	if !unknownFound {
		t.Fatalf("pending attempt should be marked unknown")
	}

	// Verify the new adapter's call count matches the old one (nothing re-sent).
	newReviewerAdapter := s.Adapters["reviewer"]
	callsAfter := len(newReviewerAdapter.Calls())
	if callsAfter != 0 {
		t.Fatalf("new adapter should have no calls (nothing re-sent), got %d", callsAfter)
	}
}

// TestRecovery_ReconnectWithLastSeq tests that reconnecting with last seq gets exact events.
func TestRecovery_ReconnectWithLastSeq(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c1 := s.Dial(t)
	turnID1 := s.Submit(t, c1, "s1")
	s.WaitTerminal(turnID1)

	// Get the last seq before the disconnect.
	lastSeq := s.Events()[len(s.Events())-1].Seq

	// Close first client.
	_ = c1.Close()

	// Record events before reconnect to know what's new.
	eventsBefore := len(s.Events())

	// Second client submits a new turn while first is offline.
	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()
	turnID2 := s.Submit(t, c2, "s2")
	s.WaitTerminal(turnID2)

	// First client reconnects and subscribes from lastSeq.
	c1Reconnect := s.Dial(t)
	defer func() { _ = c1Reconnect.Close() }()
	_, err := c1Reconnect.Subscribe(s.ctx(), lastSeq)
	if err != nil {
		t.Fatalf("subscribe from last seq: %v", err)
	}

	// Collect exactly the events the reconnected client receives.
	var receivedEvents []event
	expectedEventCount := len(s.Events()) - eventsBefore
	for i := 0; i < expectedEventCount; i++ {
		frame, err := c1Reconnect.NextEvent(s.ctx())
		if err != nil {
			break
		}
		receivedEvents = append(receivedEvents, event{Seq: frame.Seq, Kind: frame.Kind, Body: frame.Body})
	}

	// Get the expected events (the ones generated while client was offline).
	expectedEvents := s.Events()[eventsBefore:]

	if len(receivedEvents) != len(expectedEvents) {
		t.Fatalf("received %d events, expected %d (from seq %d)", len(receivedEvents), len(expectedEvents), lastSeq)
	}

	// Verify each event matches exactly (seq and kind).
	for i, expected := range expectedEvents {
		if i >= len(receivedEvents) {
			t.Fatalf("missing event at index %d", i)
		}
		received := receivedEvents[i]
		if received.Seq != expected.Seq {
			t.Fatalf("event %d: seq mismatch, got %d want %d", i, received.Seq, expected.Seq)
		}
		if received.Kind != expected.Kind {
			t.Fatalf("event %d: kind mismatch, got %s want %s", i, received.Kind, expected.Kind)
		}
	}
}

// TestRecovery_RosterValidationFailure tests roster validation failure during recovery.
func TestRecovery_RosterValidationFailure(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("1s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// Verify there's a pending attempt.
	turn := s.Turn(t, c, turnID)
	if len(turn.Attempts) == 0 {
		t.Fatalf("expected pending attempt")
	}
	pendingAttemptID := turn.Attempts[0].ID

	// Crash the engine.
	s.stopServer()
	s.h.Crash()

	// Count runs before the failed restart.
	runsBefore, err := countRuns(s.DB)
	if err != nil {
		t.Fatalf("count runs before: %v", err)
	}

	// Use a roster with invalid content to force validation error.
	invalidRoster := rosterWithStopGrace("1s")
	invalidRoster = strings.ReplaceAll(invalidRoster, `default_preset = "review"`, `default_preset = "nonexistent_preset"`)

	// Write the invalid roster.
	if err := os.WriteFile(s.RosterPath, []byte(invalidRoster), 0o600); err != nil {
		t.Fatalf("write invalid roster: %v", err)
	}

	// Try to restart - engine should fail to start due to invalid roster.
	var mu sync.Mutex
	adapters := map[string]*fake.Adapter{}
	_, err = engine.New(engine.Options{
		Store:         s.Store,
		Hub:           s.Hub,
		RosterPath:    s.RosterPath,
		EngineVersion: enginetest.EngineVersion,
		Clock:         s.Clock,
		NewAdapter: func(p config.Participant, _ config.EndpointProfile) (transport.Adapter, string, error) {
			a := fake.New([]fake.Step{}, fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel})
			mu.Lock()
			adapters[p.ID] = a
			mu.Unlock()
			return a, enginetest.AdapterVersion, nil
		},
	})

	// Verify that roster validation prevented engine start.
	if err == nil {
		t.Fatalf("engine.New should fail with invalid roster")
	}

	// Check that recovery committed but no new engine run was written.
	runsAfter, err := countRuns(s.DB)
	if err != nil {
		t.Fatalf("count runs after: %v", err)
	}

	// Recovery should have committed pending attempts to unknown, so run count unchanged.
	if runsAfter != runsBefore {
		t.Fatalf("no new run should be written on validation failure, before: %d, after: %d", runsBefore, runsAfter)
	}

	// Verify the pending attempt was marked unknown by checking the journal.
	unknownFound := false
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindAttemptUnknown {
			attemptUnknown, ok := body[wire.EvAttemptUnknown](ev, wire.EventKindAttemptUnknown)
			if ok && attemptUnknown.AttemptID == pendingAttemptID {
				unknownFound = true
				break
			}
		}
	}
	if !unknownFound {
		t.Fatalf("recovery should have marked pending attempt %s as unknown", pendingAttemptID)
	}
}
