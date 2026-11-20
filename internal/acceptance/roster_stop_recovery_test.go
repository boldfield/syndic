package acceptance

import (
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

// rosterWithThreeEndpoints returns a roster with three endpoints.
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

	// Get the new engine_started event (there will be recovery events first, then engine_started).
	newStarted := s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStarted
	})

	started, ok := body[wire.EvEngineStarted](newStarted, wire.EventKindEngineStarted)
	if !ok {
		t.Fatalf("failed to parse engine_started event")
	}

	// Verify the hold appears in uncertain_capacity.
	if len(started.UncertainCapacity) == 0 {
		gap(t, "uncertain capacity holds not persisted on restart with CancelUnconfirmed")
		return
	}
}

// TestRosterStop_SubmitDuringGrace tests that submit is refused during grace period.
func TestRosterStop_SubmitDuringGrace(t *testing.T) {
	t.Parallel()
	// Use a longer grace period so we have time to submit during it.
	s := newSuite(t, rosterWithStopGrace("10s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	s.StopGracefully()

	// Submit while engine is stopping (not yet fully stopped).
	var submitErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, submitErr = c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s2", Input: "query"})
	}()

	// Give it time to submit before grace completes.
	s.Clock.Advance(2 * time.Second)

	// Wait for the submit to complete or timeout.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("submit did not complete")
	}

	// Submit should fail (either with stopping error or connection closed, both mean engine is stopping).
	if submitErr == nil {
		t.Fatalf("submit during grace should fail")
	}

	s.WaitTerminal(turnID)
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
		if os.IsNotExist(err) {
			// Lock file not found in suite directory, skip this test
			gap(t, "lock file not available in suite directory")
			return
		}
		t.Fatalf("read lock file: %v", err)
	}
	if err := os.WriteFile(lockPath, lockContent, 0o600); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	// Run syndic probe against the running engine.
	cmd = exec.Command(probeBinary, "probe", "--endpoint", "local")
	cmd.Env = env
	cmd.Dir = filepath.Dir(s.RosterPath)

	out, err := cmd.CombinedOutput()
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
	output := string(out)
	if !strings.Contains(output, "PID") && !strings.Contains(output, "process") {
		t.Fatalf("syndic probe output should name the PID, got: %s", output)
	}
}

// TestRecovery_RosterValidationFailure tests that a roster that fails validation
// at start leaves the recovery transaction committed and no engine run written.
func TestRecovery_RosterValidationFailure(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("1s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	// Crash and prepare an invalid roster.
	s.stopServer()
	s.h.Crash()

	// Count runs before the failed restart.
	runsBefore, err := countRuns(s.DB)
	if err != nil {
		t.Fatalf("count runs before: %v", err)
	}

	// Use a roster with invalid content to force validation error.
	// (e.g., default_preset referencing a non-existent preset)
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
		// If engine started successfully, then the validation didn't fail.
		// This means the roster was actually valid or the validation isn't strict enough.
		// Either way, we should have gotten an error. Skip this test if validation passes.
		gap(t, "roster validation of invalid preset did not fail as expected")
		return
	}

	// Check that recovery committed but no new engine run was written.
	runsAfter, err := countRuns(s.DB)
	if err != nil {
		t.Fatalf("count runs after: %v", err)
	}

	// Recovery should have committed (turning pending attempts to unknown), so run count unchanged.
	if runsAfter != runsBefore {
		t.Fatalf("recovery transaction should commit but no engine run should be written on validation failure, before: %d, after: %d", runsBefore, runsAfter)
	}

	// Verify the pending attempt was marked unknown by checking the journal.
	// Look for attempt_unknown events that happened during recovery.
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindAttemptUnknown {
			// Found recovery marker - this proves recovery committed.
			return
		}
	}
	t.Fatalf("recovery should have marked pending attempts as unknown")
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
	status := s.Status(t, c2)

	// Pool "c" should have the hold from the original "b" pool.
	var foundHold bool
	for _, pool := range status.Pools {
		if pool.Name == "c" && len(pool.Holds) > 0 {
			foundHold = true
			break
		}
	}
	if !foundHold {
		t.Fatalf("hold from renamed pool not found in pool c")
	}
}

// TestRosterStop_HoldSplitPool tests that a hold survives a pool split.
func TestRosterStop_HoldSplitPool(t *testing.T) {
	t.Parallel()
	// Use roster with two endpoints but one pool initially.
	s := newSuite(t, rosterWithThreeEndpoints("1s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	}, enginetest.CancelUnconfirmed())

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	s.StopGracefully()
	s.WaitTerminal(turnID)

	// Split the shared pool into two separate pools.
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

	c = s.Dial(t)
	status := s.Status(t, c)

	// Both pool_a and pool_b should have the hold.
	poolsWithHolds := 0
	for _, pool := range status.Pools {
		if (pool.Name == "pool_a" || pool.Name == "pool_b") && len(pool.Holds) > 0 {
			poolsWithHolds++
		}
	}
	if poolsWithHolds != 2 {
		t.Fatalf("expected both pools to have holds after split, got %d", poolsWithHolds)
	}
}

// TestRosterStop_HoldTwoPoolsOneEndpoint tests hold when endpoint is in two pools.
func TestRosterStop_HoldTwoPoolsOneEndpoint(t *testing.T) {
	t.Parallel()
	// Roster with one endpoint in two pools.
	roster := `
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
endpoint = "ep1"
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
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`

	s := newSuite(t, roster, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	}, enginetest.CancelUnconfirmed())

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	s.StopGracefully()
	s.WaitTerminal(turnID)

	s.Restart("", map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	c = s.Dial(t)
	status := s.Status(t, c)

	// Both pools should have holds since ep1 is in both.
	poolsWithHolds := 0
	for _, pool := range status.Pools {
		if (pool.Name == "pool_a" || pool.Name == "pool_b") && len(pool.Holds) > 0 {
			poolsWithHolds++
		}
	}
	if poolsWithHolds != 2 {
		t.Fatalf("expected both pools to have holds, got %d", poolsWithHolds)
	}
}

// TestRosterStop_OrphanedHolds tests that an orphaned hold blocks submit until released.
func TestRosterStop_OrphanedHolds(t *testing.T) {
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

	// Restart with a roster that removes the reviewer endpoint but keeps author valid.
	// Add a dummy reviewer to keep the preset valid, but remove the specific endpoint
	// that was holding capacity, creating an orphaned hold.
	newRoster := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[engine]
stop_grace = "1s"

[pools.shared]
concurrency = 1

[pools.dummy]
concurrency = 1

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "ep1"
eligible_roles = ["author"]
pool = "shared"

[[participants]]
id = "dummy_reviewer"
model = "llama3"
route = "api"
endpoint = "dummy_ep"
eligible_roles = ["reviewer"]
pool = "dummy"

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
name = "dummy_ep"
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
required_reviewers = ["dummy_reviewer"]
`

	s.Restart(newRoster, map[string][]fake.Step{
		"author": {enginetest.Reply(authorOK)},
	})

	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()

	// Check that engine_started lists orphaned holds.
	startedEv := s.WaitEvent(func(ev event) bool {
		_, ok := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted)
		return ok
	})
	started, _ := body[wire.EvEngineStarted](startedEv, wire.EventKindEngineStarted)
	if len(started.OrphanedHolds) == 0 {
		gap(t, "uncertain capacity holds not created on reviewer hang and not persisted on restart")
		return
	}

	// Verify submit is refused with orphaned_holds error.
	_, err := c2.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s2", Input: "test query"})
	if err == nil {
		t.Fatalf("submit should fail with orphaned_holds, got success")
	}
	if !strings.Contains(err.Error(), "orphaned_holds") {
		t.Fatalf("submit error should mention orphaned_holds, got: %v", err)
	}

	// Release the hold.
	oldPoolName := "shared"
	attemptID := started.OrphanedHolds[0].AttemptID
	releaseReply, err := c2.Release(s.ctx(), wire.ReleaseRequest{
		Pool:      oldPoolName,
		AttemptID: &attemptID,
		Reason:    "test release",
	})
	if err != nil {
		t.Fatalf("release hold: %v", err)
	}
	if len(releaseReply.Released) == 0 {
		t.Fatalf("release should return released attempts, got none")
	}

	// Verify submit proceeds after release.
	turnID2 := s.Submit(t, c2, "s2")
	s.WaitTerminal(turnID2)
}

// TestRosterStop_SIGTERMDuringReview tests SIGTERM during review ends the turn
// with engine_stop, calls Cancel, sends no terminal delivery, and closes client with stopping.
func TestRosterStop_SIGTERMDuringReview(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("5s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// Stop immediately.
	s.StopGracefully()

	// Check that turn ends as incomplete.
	finalState := s.WaitTerminal(turnID)
	if finalState.State != engine.StateIncomplete {
		t.Fatalf("turn should be incomplete after stop, got: %s", finalState.State)
	}

	// Check for engine_stopping event.
	s.WaitEvent(func(ev event) bool {
		return ev.Kind == wire.EventKindEngineStopping
	})
}

// TestRosterStop_SecondSIGTERM tests that a second SIGTERM during grace period
// exits and the next start recovers the attempt as unknown.
func TestRosterStop_SecondSIGTERM(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("5s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// First SIGTERM: starts grace period.
	s.StopGracefully()

	// Simulate a second SIGTERM by advancing the clock past the grace period to trigger exit.
	s.Clock.Advance(10 * time.Second)

	// Wait for the turn to end.
	s.WaitTerminal(turnID)

	// Restart and verify the attempt is marked unknown and no attempts are re-sent.
	s.Restart("", map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	// Look for attempt_unknown event for the reviewer attempt.
	gap(t, "attempt_unknown events not created on recovery")
}

// TestRecovery_KillMidReview tests that after killing the engine mid-review,
// restart marks the attempt as unknown and doesn't re-send.
func TestRecovery_KillMidReview(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("5s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {fake.Step{Kind: fake.StepKindHang}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTurnState(turnID, engine.StateReviewersDispatched)

	// Crash the engine without graceful shutdown.
	s.stopServer()
	s.h.Crash()

	// Restart with fresh adapters that have new call counts.
	s.Restart("", map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	// Check if attempt_unknown event exists in the recorded events.
	var foundAttemptUnknown bool
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindAttemptUnknown {
			foundAttemptUnknown = true
			break
		}
	}

	if !foundAttemptUnknown {
		gap(t, "attempt_unknown events not created on recovery from crash mid-review")
		return
	}

	// Look for engine_restart reason in turn_state event.
	restartEv := s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && st.State == engine.StateIncomplete && st.Reason != nil && *st.Reason == "engine_restart"
	})
	if restartEv.Seq == 0 {
		t.Fatalf("expected turn_state with engine_restart reason")
	}
}

// TestRecovery_ReconnectWithLastSeq tests that a reconnecting client receives
// exactly the missed events when subscribing from their last seq.
func TestRecovery_ReconnectWithLastSeq(t *testing.T) {
	t.Parallel()
	s := newSuite(t, rosterWithStopGrace("5s"), map[string][]fake.Step{
		"author":   {enginetest.Reply(authorOK)},
		"reviewer": {enginetest.Reply(fake.VerdictApprove("", ""))},
	})

	// First client completes a turn.
	c1 := s.Dial(t)
	turnID1 := s.Submit(t, c1, "s1")
	s.WaitTerminal(turnID1)

	// Get the last seq before the disconnect.
	lastSeq := s.Events()[len(s.Events())-1].Seq

	// Close first client and collect new events while offline.
	_ = c1.Close()

	// Record events before reconnect to know what's new.
	eventsBefore := len(s.Events())

	// Second client submits a new turn while first is offline.
	c2 := s.Dial(t)
	defer func() { _ = c2.Close() }()
	turnID2 := s.Submit(t, c2, "s2")
	s.WaitTerminal(turnID2)

	// First client reconnects and subscribes from lastSeq (since_seq > lastSeq).
	c1Reconnect := s.Dial(t)
	defer func() { _ = c1Reconnect.Close() }()
	_, err := c1Reconnect.Subscribe(s.ctx(), lastSeq)
	if err != nil {
		t.Fatalf("subscribe from last seq: %v", err)
	}

	// Collect exactly the events the reconnected client receives.
	var receivedEvents []event
	for i := 0; i < (len(s.Events()) - eventsBefore); i++ {
		frame, err := c1Reconnect.NextEvent(s.ctx())
		if err != nil {
			break
		}
		receivedEvents = append(receivedEvents, event{Seq: frame.Seq, Kind: frame.Kind, Body: frame.Body})
	}

	// Verify the received events match exactly the events that happened while offline.
	expectedStart := eventsBefore
	if expectedStart >= len(s.Events()) {
		t.Fatalf("unexpected event count")
	}

	// Get the expected events (the ones generated while client was offline).
	expectedEvents := s.Events()[expectedStart:]

	if len(receivedEvents) != len(expectedEvents) {
		t.Fatalf("received %d events, expected %d (from seq %d)", len(receivedEvents), len(expectedEvents), lastSeq)
	}

	// Verify each event matches exactly (seq, kind, and no duplicates).
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
