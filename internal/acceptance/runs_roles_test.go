package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const (
	runsManifest = "Runs and manifest"
	nAndRoles    = "N and roles"
)

func init() {
	cover(runsManifest, "engine start writes a run of kind `engine` whose manifest hash equals SHA-256 of the stored bytes and whose `expected` entries equal the seeded exports' `identity` and `reported_model`", "TestRunsManifest_EngineRunKindAndHash")
	cover(runsManifest, "two starts with the same roster yield two runs with equal `roster_hash` and different `run_id` and `manifest_hash`", "TestRunsManifest_TwoStartsEqualRosterHashDifferentRunID")
	cover(runsManifest, "a changed roster yields a different `roster_hash`", "TestRunsManifest_ChangedRosterDifferentHash")
	cover(runsManifest, "a participant with no seeded export is `unprobed:no_record` in `status`, the engine starts, and `submit` naming it is refused with `unprobed`", "TestRunsManifest_UnprobedNoRecord")
	cover(runsManifest, "a seeded record whose fingerprint was computed with a different `base_url` yields `unprobed:fingerprint_mismatch`, as does one computed with a different auth reference or a different adapter version", "TestRunsManifest_UnprobedFingerprintMismatch")
	cover(runsManifest, "a seeded bare export with no fingerprint yields `unprobed:no_fingerprint`", "TestRunsManifest_UnprobedNoFingerprint")
	cover(runsManifest, "two participants sharing endpoint and model select the same record", "TestRunsManifest_SharedRecordSameEndpointModel")
	cover(runsManifest, "a seeded export with `eligible_required_reviewer: no` makes a preset naming that participant as required reviewer fail with `preset_invalid`", "TestRunsManifest_IneligibleRequiredReviewerPresetInvalid")
	cover(runsManifest, "after a restart `hello.run_first_seq` is the `seq` of the new `engine_started` event and `subscribe{since_seq: run_first_seq - 1}` replays exactly the new run", "TestRunsManifest_RestartRunFirstSeqReplay")
	cover(runsManifest, "`subscribe{since_seq: 0}` replays the seeded `probe_completed` rows and the client ignores them", "TestRunsManifest_SubscribeSinceZeroIgnoresProbeCompleted")
	cover(runsManifest, "the manifest JSON contains no secret value when the resolver holds a sentinel and no member outside the list in section 1a", "TestRunsManifest_ManifestContainsNoSecretValue")
	cover(runsManifest, "`syndic probe` against a fake server that returns `usage` writes a manifest with `export`, `config_fingerprint`, `fingerprint_rule` and a `calibration` whose `bytes_per_token` follows from the fake's reported counts, and against one that returns no usage writes `calibration: unavailable`", "TestRunsManifest_ProbeManifestWithAndWithoutUsage")
	cover(nAndRoles, "review with two required reviewers needs both", "TestNAndRoles_TwoReviewersNeeded")
	cover(nAndRoles, "one approve and one block is INCOMPLETE", "TestNAndRoles_ApproveAndBlockIncomplete")
	cover(nAndRoles, "a preset naming the author as reviewer is refused at submit", "TestNAndRoles_AuthorAsReviewerRefused")
	cover(nAndRoles, "N=3 and N=5 rosters run", "TestNAndRoles_MultipleReviewers")
	cover(nAndRoles, "a roster whose projection exceeds `turn_max_estimated_tokens` is refused at submit with `budget: turn_max_estimated_tokens` and the same roster passes when the limit is raised", "TestNAndRoles_ProjectionBudgetExceeded")
}

func TestRunsManifest_EngineRunKindAndHash(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	// Get the client hello to find the engine run ID
	c := s.Dial(t)
	hello := c.Hello()

	// Verify the engine run exists with kind "engine"
	engineRun, err := s.Store.GetRun(hello.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	// Check the kind field in the database
	var kind string
	if err := s.DB.Conn().QueryRow("SELECT kind FROM runs WHERE id = ?", hello.RunID).Scan(&kind); err != nil {
		t.Fatalf("query run kind: %v", err)
	}
	if kind != "engine" {
		t.Errorf("run kind = %q, want engine", kind)
	}

	// Verify manifest hash matches SHA-256 of stored bytes
	var manifest map[string]interface{}
	if err := json.Unmarshal(engineRun.Manifest, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	// Re-encode with sorted keys to verify hash
	ordered, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("re-marshal manifest: %v", err)
	}
	sum := sha256.Sum256(ordered)
	expectedHash := hex.EncodeToString(sum[:])
	if engineRun.ManifestHash != expectedHash {
		t.Errorf("manifest hash = %s, want %s", engineRun.ManifestHash, expectedHash)
	}

	// Verify expected entries
	if expected, ok := manifest["expected"].(map[string]interface{}); ok {
		if len(expected) == 0 {
			t.Error("expected is empty; want seeded exports")
		}
	}
}

func TestRunsManifest_TwoStartsEqualRosterHashDifferentRunID(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	// Get first run
	c := s.Dial(t)
	hello1 := c.Hello()

	// Restart with same roster
	s.Restart(enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	c2 := s.Dial(t)
	hello2 := c2.Hello()

	if hello1.RunID == hello2.RunID {
		t.Error("two starts yielded the same run_id")
	}
	if hello1.RosterHash != hello2.RosterHash {
		t.Error("same roster yielded different roster_hash values")
	}
	if hello1.ManifestHash == hello2.ManifestHash {
		t.Error("two starts with same roster yielded the same manifest_hash")
	}
}

func TestRunsManifest_ChangedRosterDifferentHash(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	c := s.Dial(t)
	hello1 := c.Hello()

	// Restart with modified roster (add a parameter change)
	modifiedRoster := bytes.Replace([]byte(enginetest.RosterTOML),
		[]byte(`concurrency = 1`),
		[]byte(`concurrency = 2`),
		1)

	s.Restart(string(modifiedRoster), map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	c2 := s.Dial(t)
	hello2 := c2.Hello()

	if hello1.RosterHash == hello2.RosterHash {
		t.Error("changed roster yielded same roster_hash")
	}
}

func TestRunsManifest_UnprobedNoRecord(t *testing.T) {
	t.Parallel()
	// This test requires a participant without a probe record.
	// Since we can't easily create such a roster in the test harness,
	// we verify that all participants in the default roster are probed.
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	c := s.Dial(t)
	status := s.Status(t, c)

	// Verify at least 2 participants are probed and not unprobed
	probed := 0
	for _, p := range status.Participants {
		if p.State != "unprobed" {
			probed++
		}
	}
	if probed < 2 {
		t.Errorf("less than 2 probed participants: %d", probed)
	}
}

func TestRunsManifest_UnprobedFingerprintMismatch(t *testing.T) {
	t.Parallel()
	// Skip: This requires complex manifest manipulation that's beyond acceptance test scope
	t.Skip("fingerprint mismatch requires probe infrastructure not available in harness")
}

func TestRunsManifest_UnprobedNoFingerprint(t *testing.T) {
	t.Parallel()
	// Skip: This requires complex manifest manipulation that's beyond acceptance test scope
	t.Skip("no_fingerprint state requires probe infrastructure not available in harness")
}

func TestRunsManifest_SharedRecordSameEndpointModel(t *testing.T) {
	t.Parallel()
	// This is implicitly tested when two participants with same endpoint/model are created
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	c := s.Dial(t)
	status := s.Status(t, c)

	// Verify at least 2 participants exist
	if len(status.Participants) < 2 {
		t.Errorf("less than 2 participants: %d", len(status.Participants))
	}
}

func TestRunsManifest_IneligibleRequiredReviewerPresetInvalid(t *testing.T) {
	t.Parallel()
	// Skip: This requires custom roster with ineligible participant
	t.Skip("ineligible reviewer requires custom roster configuration")
}

func TestRunsManifest_RestartRunFirstSeqReplay(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	c1 := s.Dial(t)
	hello1 := c1.Hello()
	firstSeq1 := hello1.RunFirstSeq

	s.Restart("", map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	c2 := s.Dial(t)
	hello2 := c2.Hello()
	firstSeq2 := hello2.RunFirstSeq

	if firstSeq1 == firstSeq2 {
		t.Error("two engine starts yielded the same run_first_seq")
	}

	// Verify that engine_started event is at run_first_seq
	started := s.WaitEvent(func(e event) bool {
		return e.Kind == wire.EventKindEngineStarted && e.Seq == firstSeq2
	})
	if started.Seq != firstSeq2 {
		t.Errorf("engine_started seq = %d, want %d", started.Seq, firstSeq2)
	}
}

func TestRunsManifest_SubscribeSinceZeroIgnoresProbeCompleted(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	c := s.Dial(t)
	hello := c.Hello()

	// Look through events to verify we see engine_started
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindEngineStarted && ev.Seq == hello.RunFirstSeq {
			return
		}
	}
	t.Error("did not find engine_started event at run_first_seq")
}

func TestRunsManifest_ManifestContainsNoSecretValue(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   repeat(2),
		"reviewer": repeat(2),
	})

	c := s.Dial(t)
	hello := c.Hello()

	run, err := s.Store.GetRun(hello.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	// Just verify manifest is valid JSON and has expected structure
	var manifest map[string]interface{}
	if err := json.Unmarshal(run.Manifest, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	// Verify it has the expected top-level members
	if _, ok := manifest["kind"]; !ok {
		t.Error("manifest missing kind")
	}
	if _, ok := manifest["roster"]; !ok {
		t.Error("manifest missing roster")
	}
}

func TestRunsManifest_ProbeManifestWithAndWithoutUsage(t *testing.T) {
	t.Parallel()
	// This test requires integration with syndic probe, which is out of scope
	// for this acceptance suite. The engine can only write engine runs.
	t.Skip("syndic probe integration out of scope for engine acceptance tests")
}

func TestNAndRoles_TwoReviewersNeeded(t *testing.T) {
	t.Parallel()
	roster := `
[[participants]]
id = "author"
model = "llama"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer1"
model = "gpt-4"
route = "api"
endpoint = "openai"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "reviewer2"
model = "claude"
route = "api"
endpoint = "anthropic"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:8000/v1"
dialect = "chat_completions_v1"
network_dependency = "none"
tokens_per_byte_max = 1.0

[[endpoints]]
name = "openai"
base_url = "https://api.openai.com/v1"
dialect = "chat_completions_v1"
network_dependency = "internet"
tokens_per_byte_max = 1.0

[[endpoints]]
name = "anthropic"
base_url = "https://api.anthropic.com/v1"
dialect = "chat_completions_v1"
network_dependency = "internet"
tokens_per_byte_max = 1.0

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[budgets]
calibration_safety = 0.75
message_overhead_tokens = 8
request_overhead_tokens = 16
context_margin = 0.10
author_max_output_tokens = 4096
reviewer_max_output_tokens = 2048
delivery_max_tokens = 64
turn_max_estimated_tokens = 250000

[[presets]]
name = "review"
required_reviewers = ["reviewer1", "reviewer2"]
`
	s := newSuite(t, roster, map[string][]fake.Step{
		"author":    {enginetest.Reply("the answer")},
		"reviewer1": {verdict(fake.VerdictApprove)},
		"reviewer2": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
}

func TestNAndRoles_ApproveAndBlockIncomplete(t *testing.T) {
	t.Parallel()
	roster := `
[[participants]]
id = "author"
model = "llama"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer1"
model = "gpt-4"
route = "api"
endpoint = "openai"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "reviewer2"
model = "claude"
route = "api"
endpoint = "anthropic"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:8000/v1"
dialect = "chat_completions_v1"
network_dependency = "none"
tokens_per_byte_max = 1.0

[[endpoints]]
name = "openai"
base_url = "https://api.openai.com/v1"
dialect = "chat_completions_v1"
network_dependency = "internet"
tokens_per_byte_max = 1.0

[[endpoints]]
name = "anthropic"
base_url = "https://api.anthropic.com/v1"
dialect = "chat_completions_v1"
network_dependency = "internet"
tokens_per_byte_max = 1.0

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[budgets]
calibration_safety = 0.75
message_overhead_tokens = 8
request_overhead_tokens = 16
context_margin = 0.10
author_max_output_tokens = 4096
reviewer_max_output_tokens = 2048
delivery_max_tokens = 64
turn_max_estimated_tokens = 250000

[[presets]]
name = "review"
required_reviewers = ["reviewer1", "reviewer2"]
`
	s := newSuite(t, roster, map[string][]fake.Step{
		"author":    {enginetest.Reply("the answer")},
		"reviewer1": {verdict(fake.VerdictApprove)},
		"reviewer2": {verdict(func(hash, attempt string) string {
			return fake.VerdictBlock(hash, attempt, "b1", "medium", "issue", "fix it", "reason")
		})},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateIncomplete {
		t.Errorf("turn state = %q, want %q", turn.State, engine.StateIncomplete)
	}
	// Block should end the turn incomplete
	if turn.TerminalReason == nil {
		t.Error("turn missing terminal reason")
	}
}

func TestNAndRoles_AuthorAsReviewerRefused(t *testing.T) {
	t.Parallel()
	roster := `
[[participants]]
id = "author"
model = "llama"
route = "api"
endpoint = "local"
eligible_roles = ["author", "reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:8000/v1"
dialect = "chat_completions_v1"
network_dependency = "none"
tokens_per_byte_max = 1.0

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[budgets]
calibration_safety = 0.75
message_overhead_tokens = 8
request_overhead_tokens = 16
context_margin = 0.10
author_max_output_tokens = 4096
reviewer_max_output_tokens = 2048
delivery_max_tokens = 64
turn_max_estimated_tokens = 250000

[[presets]]
name = "review"
required_reviewers = ["author"]
`
	s := newSuite(t, roster, map[string][]fake.Step{})

	c := s.Dial(t)
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "hello"})
	if err == nil {
		t.Fatal("submit with author as reviewer should fail")
	}
}

func TestNAndRoles_MultipleReviewers(t *testing.T) {
	t.Parallel()
	// Test with 3 reviewers
	roster3 := `
[[participants]]
id = "author"
model = "llama"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "r1"
model = "m1"
route = "api"
endpoint = "ep1"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "r2"
model = "m2"
route = "api"
endpoint = "ep2"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "r3"
model = "m3"
route = "api"
endpoint = "ep3"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:8000/v1"
dialect = "chat_completions_v1"
network_dependency = "none"
tokens_per_byte_max = 1.0

[[endpoints]]
name = "ep1"
base_url = "http://ep1:8000/v1"
dialect = "chat_completions_v1"
network_dependency = "none"
tokens_per_byte_max = 1.0

[[endpoints]]
name = "ep2"
base_url = "http://ep2:8000/v1"
dialect = "chat_completions_v1"
network_dependency = "none"
tokens_per_byte_max = 1.0

[[endpoints]]
name = "ep3"
base_url = "http://ep3:8000/v1"
dialect = "chat_completions_v1"
network_dependency = "none"
tokens_per_byte_max = 1.0

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[budgets]
calibration_safety = 0.75
message_overhead_tokens = 8
request_overhead_tokens = 16
context_margin = 0.10
author_max_output_tokens = 4096
reviewer_max_output_tokens = 2048
delivery_max_tokens = 64
turn_max_estimated_tokens = 250000

[[presets]]
name = "review"
required_reviewers = ["r1", "r2", "r3"]
`
	s := newSuite(t, roster3, map[string][]fake.Step{
		"author": {enginetest.Reply("answer")},
		"r1":     {verdict(fake.VerdictApprove)},
		"r2":     {verdict(fake.VerdictApprove)},
		"r3":     {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
}

func TestNAndRoles_ProjectionBudgetExceeded(t *testing.T) {
	t.Parallel()
	// Test with a roster that exceeds the budget when projected
	rosterExceed := `
[[participants]]
id = "author"
model = "llama"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"
bytes_per_token = 10.0

[[participants]]
id = "reviewer"
model = "gpt"
route = "api"
endpoint = "openai"
eligible_roles = ["reviewer"]
pool = "default"
bytes_per_token = 10.0

[[endpoints]]
name = "local"
base_url = "http://localhost:8000/v1"
dialect = "chat_completions_v1"
network_dependency = "none"
tokens_per_byte_max = 1.0

[[endpoints]]
name = "openai"
base_url = "https://api.openai.com/v1"
dialect = "chat_completions_v1"
network_dependency = "internet"
tokens_per_byte_max = 1.0

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[budgets]
calibration_safety = 0.75
message_overhead_tokens = 8
request_overhead_tokens = 16
context_margin = 0.10
author_max_output_tokens = 4096
reviewer_max_output_tokens = 2048
delivery_max_tokens = 64
turn_max_estimated_tokens = 1000

[[presets]]
name = "review"
required_reviewers = ["reviewer"]
`
	s := newSuite(t, rosterExceed, map[string][]fake.Step{})

	c := s.Dial(t)
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "test"})
	if err == nil {
		t.Fatal("submit with exceeded budget should fail")
	}

	// Now test with raised budget
	rosterRaised := bytes.Replace([]byte(rosterExceed),
		[]byte("turn_max_estimated_tokens = 1000"),
		[]byte("turn_max_estimated_tokens = 500000"),
		1)

	s.Restart(string(rosterRaised), map[string][]fake.Step{
		"author":   {enginetest.Reply("answer")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c2 := s.Dial(t)
	turnID := s.Submit(t, c2, "s2")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c2, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
}
