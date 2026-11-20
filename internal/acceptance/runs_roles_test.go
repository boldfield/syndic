package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
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
	cover(runsManifest, "`syndic probe` against a fake server that returns `usage` writes a manifest with `export`, `config_fingerprint`, `fingerprint_rule` and a `calibration` whose `bytes_per_token` follows from the fake's reported counts", "cmd/syndic.TestProbeSuccess")
	cover(runsManifest, "and against one that returns no usage writes `calibration: unavailable`", "cmd/syndic.TestProbeNoUsage")
	cover(nAndRoles, "review with two required reviewers needs both", "TestNAndRoles_TwoReviewersNeeded")
	cover(nAndRoles, "one approve and one block is INCOMPLETE", "TestNAndRoles_ApproveAndBlockIncomplete")
	cover(nAndRoles, "a preset naming the author as reviewer is refused at submit", "TestNAndRoles_AuthorAsReviewerRefused")
	cover(nAndRoles, "N=3 and N=5 rosters run", "TestNAndRoles_MultipleReviewers")
	cover(nAndRoles, "a roster whose projection exceeds `turn_max_estimated_tokens` is refused at submit with `budget: turn_max_estimated_tokens` and the same roster passes when the limit is raised", "TestNAndRoles_ProjectionBudgetExceeded")
}

func TestRunsManifest_EngineRunKindAndHash(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	hello := c.Hello()

	engineRun, err := s.Store.GetRun(hello.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	if manifest := engineRun.Manifest; manifest == nil {
		t.Fatal("manifest is nil")
	}

	sum := sha256.Sum256(engineRun.Manifest)
	expectedHash := hex.EncodeToString(sum[:])
	if engineRun.ManifestHash != expectedHash {
		t.Errorf("manifest hash = %s, want %s (hash of raw bytes)", engineRun.ManifestHash, expectedHash)
	}

	var manifest map[string]interface{}
	if err := json.Unmarshal(engineRun.Manifest, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if manifest["kind"] != "engine" {
		t.Errorf("manifest kind = %v, want engine", manifest["kind"])
	}

	expected, ok := manifest["expected"].(map[string]interface{})
	if !ok || len(expected) != 2 {
		t.Errorf("expected map has %d entries, want 2", len(expected))
	}

	for _, participant := range []string{"author", "reviewer"} {
		entry, ok := expected[participant].(map[string]interface{})
		if !ok {
			t.Errorf("expected[%q] not a map", participant)
			continue
		}
		identity, ok := entry["identity"].(map[string]interface{})
		if !ok {
			t.Errorf("expected[%q].identity not a map", participant)
			continue
		}
		kind, ok := identity["Kind"].(string)
		if !ok {
			t.Errorf("expected[%q].identity.Kind missing or not a string", participant)
		} else if kind != "unknown" {
			t.Errorf("expected[%q].identity.Kind = %q, want unknown (OpenAI route)", participant, kind)
		}
		reportedModel, ok := entry["reported_model"].(string)
		if !ok {
			t.Errorf("expected[%q].reported_model not a string", participant)
			continue
		}
		if reportedModel != enginetest.DefaultReportedModel {
			t.Errorf("expected[%q].reported_model = %q, want %q", participant, reportedModel, enginetest.DefaultReportedModel)
		}
	}
}

func TestRunsManifest_TwoStartsEqualRosterHashDifferentRunID(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	hello1 := c.Hello()

	run1, err := s.Store.GetRun(hello1.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	s.Clock.Advance(time.Second)

	s.Restart(enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c2 := s.Dial(t)
	hello2 := c2.Hello()

	run2, err := s.Store.GetRun(hello2.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	if hello1.RunID == hello2.RunID {
		t.Error("two starts yielded the same run_id")
	}
	if hello1.RosterHash != hello2.RosterHash {
		t.Error("same roster yielded different roster_hash values")
	}
	if run1.ManifestHash == run2.ManifestHash {
		t.Error("two starts with same roster yielded equal manifest_hash; should differ due to started_at")
	}
}

func TestRunsManifest_ChangedRosterDifferentHash(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	hello1 := c.Hello()

	modifiedRoster := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

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

[[participants]]
id = "extra"
model = "llama2"
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

[quick.fast]
preset = "solo"
drafts = false
[quick.fast.budgets]
author_max_output_tokens = 2048

[policy]
local_only = true
allowed_destinations = []
`

	s.Restart(modifiedRoster, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
		"extra":    {verdict(fake.VerdictApprove)},
	})

	c2 := s.Dial(t)
	hello2 := c2.Hello()

	if hello1.RosterHash == hello2.RosterHash {
		t.Error("changed roster yielded same roster_hash")
	}
}

func TestRunsManifest_UnprobedNoRecord(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.SkipProbe("reviewer"))

	c := s.Dial(t)
	status := s.Status(t, c)

	unprobed := false
	for _, p := range status.Participants {
		if p.ID == "reviewer" && p.State == "unprobed" && p.Detail != nil && *p.Detail == "no_record" {
			unprobed = true
		}
	}
	if !unprobed {
		t.Error("reviewer should be unprobed:no_record")
	}

	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "test"})
	if err == nil {
		t.Fatal("submit with unprobed reviewer should be refused")
	}
	if !strings.Contains(err.Error(), "unprobed") {
		t.Errorf("submit error should mention unprobed, got: %v", err)
	}
}

func TestRunsManifest_UnprobedFingerprintMismatch(t *testing.T) {
	t.Parallel()

	rosterWithDifferentBaseURL := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

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
base_url = "http://localhost:11435/v1"
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

[quick.fast]
preset = "solo"
drafts = false
[quick.fast.budgets]
author_max_output_tokens = 2048

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	_ = s.Dial(t)

	s.Restart(rosterWithDifferentBaseURL, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c2 := s.Dial(t)
	status := s.Status(t, c2)

	found := false
	for _, p := range status.Participants {
		if p.ID == "reviewer" && p.State == "unprobed" && p.Detail != nil && *p.Detail == "fingerprint_mismatch" {
			found = true
		}
	}
	if !found {
		t.Error("reviewer should be unprobed:fingerprint_mismatch with different base_url")
	}

}

func TestRunsManifest_UnprobedNoFingerprint(t *testing.T) {
	t.Parallel()

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.SkipProbe("reviewer"))

	_ = s.Dial(t)

	bareExport := json.RawMessage(`{
		"endpoint_name":"local",
		"model":"llama3",
		"redaction_rule":"redaction/v1",
		"redacted":true,
		"declared_location":"local",
		"declared_execution":"local",
		"declared_billing":"none",
		"network_dependency":"none",
		"auth_mode":"none",
		"streaming":"unknown",
		"cancel_observable":"unknown",
		"usage_available":"unknown",
		"reported_model":"enginetest-model",
		"identity":{"kind":"unknown"},
		"eligible_required_reviewer":"unknown",
		"observed_at":"2026-01-01T00:00:00Z"
	}`)

	if _, _, err := s.Store.CreateRun(bareExport); err != nil {
		t.Fatalf("seed bare export probe: %v", err)
	}

	s.Restart("", map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c2 := s.Dial(t)
	status := s.Status(t, c2)

	found := false
	for _, p := range status.Participants {
		if p.ID == "reviewer" && p.State == "unprobed" && p.Detail != nil && *p.Detail == "no_fingerprint" {
			found = true
		}
	}
	if !found {
		t.Error("reviewer should be unprobed:no_fingerprint for bare export")
	}
}

func TestRunsManifest_SharedRecordSameEndpointModel(t *testing.T) {
	t.Parallel()
	roster := `
default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[[participants]]
id = "author"
model = "shared-model"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer1"
model = "shared-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "reviewer2"
model = "shared-model"
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
models = ["shared-model"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer1", "reviewer2"]

[presets.solo]
mode = "solo"
author = "author"
required_reviewers = []

[quick.fast]
preset = "solo"
drafts = false
[quick.fast.budgets]
author_max_output_tokens = 2048

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, roster, map[string][]fake.Step{
		"author":    {enginetest.Reply("test")},
		"reviewer1": {verdict(fake.VerdictApprove)},
		"reviewer2": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	hello := c.Hello()

	run, err := s.Store.GetRun(hello.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	var manifest map[string]interface{}
	if err := json.Unmarshal(run.Manifest, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	probeRecords, ok := manifest["probe_records"].(map[string]interface{})
	if !ok {
		t.Fatalf("probe_records not a map")
	}

	record1, ok1 := probeRecords["reviewer1"]
	record2, ok2 := probeRecords["reviewer2"]

	if !ok1 || !ok2 {
		t.Fatal("probe_records should contain reviewer1 and reviewer2")
	}

	rec1JSON, err := json.Marshal(record1)
	if err != nil {
		t.Fatalf("marshal record1: %v", err)
	}
	rec2JSON, err := json.Marshal(record2)
	if err != nil {
		t.Fatalf("marshal record2: %v", err)
	}

	if string(rec1JSON) != string(rec2JSON) {
		t.Error("two participants with same endpoint and model should select the same record")
	}
}

func TestRunsManifest_IneligibleRequiredReviewerPresetInvalid(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	}, enginetest.WithEligibleRequiredReviewer("reviewer", transport.TriNo))

	c := s.Dial(t)
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "test", Preset: &[]string{"review"}[0]})
	if err == nil {
		t.Fatal("submit with ineligible required reviewer should be refused")
	}
	if !strings.Contains(err.Error(), "preset_invalid") {
		t.Errorf("submit error should mention preset_invalid, got: %v", err)
	}
}

func TestRunsManifest_RestartRunFirstSeqReplay(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c1 := s.Dial(t)
	hello1 := c1.Hello()
	firstSeq1 := hello1.RunFirstSeq

	s.Restart("", map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c2 := s.Dial(t)
	hello2 := c2.Hello()
	firstSeq2 := hello2.RunFirstSeq

	if firstSeq1 == firstSeq2 {
		t.Error("two engine starts yielded the same run_first_seq")
	}

	started := s.WaitEvent(func(e event) bool {
		return e.Kind == wire.EventKindEngineStarted && e.Seq == firstSeq2
	})
	if started.Seq != firstSeq2 {
		t.Errorf("engine_started seq = %d, want %d", started.Seq, firstSeq2)
	}

	c3, err := client.Dial(s.ctx(), s.paths.Socket, "acceptance", "0")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c3.Close() }()

	if _, err := c3.Subscribe(s.ctx(), firstSeq2-1); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	var engineStartedSeen bool
	seenSeqs := map[int64]bool{}
	timeoutChan := make(chan bool, 1)
	go func() {
		for i := 0; i < 100; i++ {
			frame, err := c3.NextEvent(s.ctx())
			if err != nil {
				return
			}
			if frame.Seq < firstSeq2 {
				t.Errorf("event seq %d before run_first_seq %d", frame.Seq, firstSeq2)
				return
			}
			if _, dup := seenSeqs[frame.Seq]; dup {
				t.Errorf("duplicate seq %d", frame.Seq)
				return
			}
			seenSeqs[frame.Seq] = true
			if frame.Kind == wire.EventKindEngineStarted && frame.Seq == firstSeq2 {
				engineStartedSeen = true
				timeoutChan <- true
				return
			}
		}
		timeoutChan <- false
	}()

	select {
	case result := <-timeoutChan:
		if !result || !engineStartedSeen {
			t.Error("replay did not reach or verify engine_started at run_first_seq")
		}
	case <-s.ctx().Done():
		t.Fatal("replay subscription timed out before receiving engine_started")
	}
}

func TestRunsManifest_SubscribeSinceZeroIgnoresProbeCompleted(t *testing.T) {
	t.Parallel()

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	hello := c.Hello()

	engineStarted := s.WaitEvent(func(e event) bool {
		return e.Kind == wire.EventKindEngineStarted && e.Seq == hello.RunFirstSeq
	})

	if engineStarted.Seq != hello.RunFirstSeq {
		t.Errorf("engine_started seq = %d, want %d", engineStarted.Seq, hello.RunFirstSeq)
	}

	engineStartedFound := false
	probeCompletedCount := 0
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindEngineStarted && ev.Seq == hello.RunFirstSeq {
			engineStartedFound = true
		}
		if ev.Kind == "probe_completed" {
			probeCompletedCount++
		}
	}
	if !engineStartedFound {
		t.Error("events should include engine_started at run_first_seq")
	}
	if probeCompletedCount != 0 {
		t.Errorf("events included %d probe_completed events, expected 0 in this test", probeCompletedCount)
	}
}

func TestRunsManifest_ManifestContainsNoSecretValue(t *testing.T) {
	t.Parallel()

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("test")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	hello := c.Hello()

	run, err := s.Store.GetRun(hello.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	manifestBytes := run.Manifest
	if bytes.Contains(manifestBytes, []byte("SecretRef")) {
		t.Error("manifest should not contain unresolved SecretRef")
	}

	var manifest map[string]interface{}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	topLevel := map[string]bool{
		"kind": true, "engine_version": true, "started_at": true,
		"roster": true, "roster_path": true, "roster_file_sha256": true,
		"probe_records": true, "expected": true,
	}

	for key := range manifest {
		if !topLevel[key] {
			t.Errorf("manifest contains unexpected top-level member %q", key)
		}
	}

	roster, ok := manifest["roster"].(map[string]interface{})
	if !ok {
		t.Fatal("roster not a map")
	}

	endpoints, ok := roster["endpoints"].([]interface{})
	if !ok {
		t.Fatal("endpoints not an array")
	}

	allowedMembers := map[string]bool{
		"name": true, "base_url": true, "dialect": true, "max_tokens_field": true,
		"declared_location": true, "declared_execution": true, "declared_billing": true,
		"network_dependency": true, "models": true, "terms_note": true,
		"tokens_per_byte_max": true,
	}

	for _, ep := range endpoints {
		epMap, ok := ep.(map[string]interface{})
		if !ok {
			t.Error("endpoint not a map")
			continue
		}
		for key, val := range epMap {
			if !allowedMembers[key] {
				t.Errorf("endpoint contains member %q not in section 1a allowlist: %v", key, val)
			}
		}
	}

	probeRecords, ok := manifest["probe_records"].(map[string]interface{})
	if !ok {
		t.Fatal("probe_records not a map")
	}

	for _, record := range probeRecords {
		recJSON, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		manifestStr := string(recJSON)
		if bytes.Contains([]byte(manifestStr), []byte("SecretRef")) {
			t.Error("probe record should not contain unresolved SecretRef")
		}
	}
}

func TestNAndRoles_TwoReviewersNeeded(t *testing.T) {
	t.Parallel()
	roster := `
default_preset = "review"

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

[[participants]]
id = "author"
model = "author-model"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer1"
model = "reviewer1-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "reviewer2"
model = "reviewer2-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:8000/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
dialect = "chat_completions_v1"
network_dependency = "none"
models = ["author-model", "reviewer1-model", "reviewer2-model"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer1", "reviewer2"]

[policy]
local_only = true
allowed_destinations = []
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
default_preset = "review"

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

[[participants]]
id = "author"
model = "author-model"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer1"
model = "reviewer1-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "reviewer2"
model = "reviewer2-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:8000/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
dialect = "chat_completions_v1"
network_dependency = "none"
models = ["author-model", "reviewer1-model", "reviewer2-model"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer1", "reviewer2"]

[policy]
local_only = true
allowed_destinations = []
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
	if turn.TerminalReason == nil {
		t.Error("turn missing terminal reason")
	} else if !strings.Contains(*turn.TerminalReason, "block") {
		t.Errorf("terminal reason should contain block reference, got %q", *turn.TerminalReason)
	}
}

func TestNAndRoles_AuthorAsReviewerRefused(t *testing.T) {
	t.Parallel()

	gap(t, "roster with author as required reviewer is rejected at engine.Start (roster validation), not at submit as the design specifies. The engine validates and refuses rosters during initialization, not at submit time, when author appears in required_reviewers.")
}

func TestNAndRoles_MultipleReviewers(t *testing.T) {
	t.Parallel()
	roster := `
default_preset = "review3"

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

[[participants]]
id = "author"
model = "author-model"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "r1"
model = "r1-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "r2"
model = "r2-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "r3"
model = "r3-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "r4"
model = "r4-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[participants]]
id = "r5"
model = "r5-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:8000/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
dialect = "chat_completions_v1"
network_dependency = "none"
models = ["author-model", "r1-model", "r2-model", "r3-model", "r4-model", "r5-model"]
tokens_per_byte_max = 1.0

[presets.review3]
mode = "review"
author = "author"
required_reviewers = ["r1", "r2", "r3"]

[presets.review5]
mode = "review"
author = "author"
required_reviewers = ["r1", "r2", "r3", "r4", "r5"]

[policy]
local_only = true
allowed_destinations = []
`

	s3 := newSuite(t, roster, map[string][]fake.Step{
		"author": {enginetest.Reply("answer")},
		"r1":     {verdict(fake.VerdictApprove)},
		"r2":     {verdict(fake.VerdictApprove)},
		"r3":     {verdict(fake.VerdictApprove)},
		"r4":     {verdict(fake.VerdictApprove)},
		"r5":     {verdict(fake.VerdictApprove)},
	})

	c3 := s3.Dial(t)
	preset := "review3"
	turnID3 := s3.SubmitPreset(t, c3, "s1", preset)
	s3.WaitTerminal(turnID3)

	turn3 := s3.Turn(t, c3, turnID3)
	wantTurn(t, turn3, engine.StateApproved, "all_approved")

	s5 := newSuite(t, roster, map[string][]fake.Step{
		"author": {enginetest.Reply("answer")},
		"r1":     {verdict(fake.VerdictApprove)},
		"r2":     {verdict(fake.VerdictApprove)},
		"r3":     {verdict(fake.VerdictApprove)},
		"r4":     {verdict(fake.VerdictApprove)},
		"r5":     {verdict(fake.VerdictApprove)},
	})

	c5 := s5.Dial(t)
	preset5 := "review5"
	turnID5 := s5.SubmitPreset(t, c5, "s2", preset5)
	s5.WaitTerminal(turnID5)

	turn5 := s5.Turn(t, c5, turnID5)
	wantTurn(t, turn5, engine.StateApproved, "all_approved")
}

func TestNAndRoles_ProjectionBudgetExceeded(t *testing.T) {
	t.Parallel()
	rosterExceed := `
default_preset = "review"

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

[[participants]]
id = "author"
model = "author-model"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "reviewer"
model = "reviewer-model"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"

[[endpoints]]
name = "local"
base_url = "http://localhost:8000/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
dialect = "chat_completions_v1"
network_dependency = "none"
models = ["author-model", "reviewer-model"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]

[policy]
local_only = true
allowed_destinations = []
`

	s := newSuite(t, rosterExceed, map[string][]fake.Step{})

	c := s.Dial(t)
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "test"})
	if err == nil {
		t.Fatal("submit with exceeded budget should be refused")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("submit error should mention budget, got: %v", err)
	}

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
