package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/evidence"
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

// runsScripts scripts every participant of the roster to answer whatever it
// is asked, as many times as a test could need.
func runsScripts(ids ...string) map[string][]fake.Step {
	out := map[string][]fake.Step{}
	for _, id := range ids {
		out[id] = repeat(8)
	}
	return out
}

// runsDefaultScripts is runsScripts for enginetest.RosterTOML.
func runsDefaultScripts() map[string][]fake.Step {
	return runsScripts("author", "reviewer")
}

// runsKind reads the kind column of a run, which the protocol does not show.
func runsKind(t *testing.T, s *suite, runID string) string {
	t.Helper()
	var kind string
	if err := s.DB.Conn().QueryRow(`SELECT kind FROM runs WHERE id = ?`, runID).Scan(&kind); err != nil {
		t.Fatalf("kind of run %s: %v", runID, err)
	}
	return kind
}

// runsParticipant returns the participant's entry in status.
func runsParticipant(t *testing.T, st wire.StatusReply, id string) wire.Participant {
	t.Helper()
	for _, p := range st.Participants {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("status lists no participant %q: %+v", id, st.Participants)
	return wire.Participant{}
}

// runsWantUnprobed requires status to show the participant as unprobed with
// the detail.
func runsWantUnprobed(t *testing.T, st wire.StatusReply, id, detail string) {
	t.Helper()
	p := runsParticipant(t, st, id)
	if p.State != engine.ParticipantUnprobed || p.Detail == nil || *p.Detail != detail {
		t.Fatalf("participant %s is %q (detail %s), want %s:%s", id, p.State, str(p.Detail), engine.ParticipantUnprobed, detail)
	}
}

// runsRefusal requires err to be a protocol error with the code.
func runsRefusal(t *testing.T, err error, code string) *wire.Error {
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

// runsSubmitDefault submits to the default preset and returns the error.
func runsSubmitDefault(t *testing.T, s *suite, c *client.Client, submissionID string) error {
	t.Helper()
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: submissionID, Input: "what is the answer?"})
	return err
}

// runsRecord describes a probe record to seed for a participant's endpoint
// and model. The export is built from the endpoint profile as it stands in the
// roster; fingerprint, when set, changes the profile the fingerprint is
// computed from, and adapterVersion changes the version it is computed with.
type runsRecord struct {
	model          string
	reported       string
	fingerprint    func(config.EndpointProfile) config.EndpointProfile
	adapterVersion string
}

// runsExport builds the export the harness seeds, with the reported model of
// the record.
func runsExport(profile config.EndpointProfile, model, reported string, observed time.Time) evidence.Export {
	bound := enginetest.DefaultContextBound
	return evidence.Export{
		Redacted:                 true,
		RedactionRule:            "redaction/v1",
		EndpointName:             profile.Name,
		DeclaredLocation:         profile.DeclaredLocation,
		DeclaredExecution:        profile.DeclaredExecution,
		DeclaredBilling:          profile.DeclaredBilling,
		NetworkDependency:        profile.NetworkDependency,
		Model:                    model,
		AuthMode:                 "none",
		Identity:                 transport.NewUnknownIdentity(),
		ReportedModel:            reported,
		Streaming:                transport.TriUnknown,
		CancelObservable:         transport.TriUnknown,
		UsageAvailable:           transport.TriUnknown,
		ModelsEndpoint:           transport.TriUnknown,
		ModelListed:              transport.TriUnknown,
		SystemRoleHonored:        transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown,
		AdapterVersion:           enginetest.AdapterVersion,
		ContextBound:             &bound,
		ObservedAt:               observed,
	}
}

// runsSeed writes a probe run for the endpoint of the roster file and the
// record's model, newer than every run already in the journal, and returns the
// export it holds.
func runsSeed(t *testing.T, s *suite, endpoint string, rec runsRecord) evidence.Export {
	t.Helper()
	roster, err := config.Load(s.RosterPath)
	if err != nil {
		t.Fatalf("load roster: %v", err)
	}
	var profile config.EndpointProfile
	found := false
	for _, ep := range roster.Endpoints {
		if ep.Name == endpoint {
			profile, found = ep, true
		}
	}
	if !found {
		t.Fatalf("roster has no endpoint %q", endpoint)
	}
	export := runsExport(profile, rec.model, rec.reported, s.Clock.Now())
	fp := profile
	if rec.fingerprint != nil {
		fp = rec.fingerprint(profile)
	}
	version := enginetest.AdapterVersion
	if rec.adapterVersion != "" {
		version = rec.adapterVersion
	}
	pm, err := evidence.NewProbeManifest(export, fp, rec.model, version, transport.Calibration{Available: true, BytesPerToken: enginetest.DefaultCalibration})
	if err != nil {
		t.Fatalf("probe manifest: %v", err)
	}
	data, err := json.Marshal(pm)
	if err != nil {
		t.Fatalf("marshal probe manifest: %v", err)
	}
	if _, _, err := s.Store.CreateRun(data); err != nil {
		t.Fatalf("seed probe record: %v", err)
	}
	return export
}

// runsProbeRunIDs lists the probe runs of the journal, oldest first.
func runsProbeRunIDs(t *testing.T, s *suite) []string {
	t.Helper()
	rows, err := s.DB.Conn().Query(`SELECT id FROM runs WHERE kind = 'probe' ORDER BY rowid`)
	if err != nil {
		t.Fatalf("list probe runs: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan probe run: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list probe runs: %v", err)
	}
	return ids
}

// runsReplay subscribes a new client from sinceSeq and reads the replay up to
// and including head. It returns the frames and the client.
func runsReplay(t *testing.T, s *suite, sinceSeq, head int64) ([]wire.Frame, *client.Client) {
	t.Helper()
	c := s.Dial(t)
	if _, err := c.Subscribe(s.ctx(), sinceSeq); err != nil {
		t.Fatalf("subscribe since %d: %v", sinceSeq, err)
	}
	var frames []wire.Frame
	for last := sinceSeq; last < head; {
		frame, err := c.NextEvent(s.ctx())
		if err != nil {
			t.Fatalf("replay since %d stopped at seq %d of %d: %v", sinceSeq, last, head, err)
		}
		frames = append(frames, frame)
		last = frame.Seq
	}
	return frames, c
}

func runsReadManifest(t *testing.T, s *suite, runID string) (*struct {
	Kind     string
	Manifest []byte
	Hash     string
}, map[string]json.RawMessage) {
	t.Helper()
	run, err := s.Store.GetRun(runID)
	if err != nil {
		t.Fatalf("GetRun %s: %v", runID, err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(run.Manifest, &members); err != nil {
		t.Fatalf("manifest of %s is not a JSON object: %v", runID, err)
	}
	return &struct {
		Kind     string
		Manifest []byte
		Hash     string
	}{runsKind(t, s, runID), run.Manifest, run.ManifestHash}, members
}

func TestRunsManifest_EngineRunKindAndHash(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, runsDefaultScripts())
	// Seed exports that differ by participant, newer than the harness's, so an
	// expected entry can only come from the export the engine selected.
	authorExport := runsSeed(t, s, "local", runsRecord{model: "llama2", reported: "author-served-model"})
	reviewerExport := runsSeed(t, s, "local", runsRecord{model: "llama3", reported: "reviewer-served-model"})
	s.Restart("", runsDefaultScripts())

	c := s.Dial(t)
	hello := c.Hello()
	run, members := runsReadManifest(t, s, hello.RunID)

	if run.Kind != "engine" {
		t.Fatalf("run %s has kind %q, want engine", hello.RunID, run.Kind)
	}
	var kind string
	if err := json.Unmarshal(members["kind"], &kind); err != nil || kind != "engine" {
		t.Fatalf("manifest kind = %s (%v), want \"engine\"", members["kind"], err)
	}
	sum := sha256.Sum256(run.Manifest)
	if want := hex.EncodeToString(sum[:]); run.Hash != want {
		t.Fatalf("stored manifest_hash = %s, want SHA-256 of the stored bytes %s", run.Hash, want)
	}
	if hello.ManifestHash == nil || *hello.ManifestHash != run.Hash {
		var got string
		if hello.ManifestHash != nil {
			got = *hello.ManifestHash
		}
		t.Fatalf("hello.manifest_hash = %s, want %s", got, run.Hash)
	}
	started := s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStarted && ev.Seq == hello.RunFirstSeq })
	if es, _ := body[wire.EvEngineStarted](started, wire.EventKindEngineStarted); es.ManifestHash != run.Hash || es.RunID != hello.RunID {
		t.Fatalf("engine_started = %+v, want run %s with manifest_hash %s", es, hello.RunID, run.Hash)
	}

	var expected map[string]struct {
		Identity      map[string]any `json:"identity"`
		ReportedModel string         `json:"reported_model"`
	}
	if err := json.Unmarshal(members["expected"], &expected); err != nil {
		t.Fatalf("manifest expected: %v", err)
	}
	if len(expected) != 2 {
		t.Fatalf("expected has %d entries, want one per participant: %s", len(expected), members["expected"])
	}
	for id, export := range map[string]evidence.Export{"author": authorExport, "reviewer": reviewerExport} {
		got, ok := expected[id]
		if !ok {
			t.Fatalf("expected has no entry for %s: %s", id, members["expected"])
		}
		var wantIdentity map[string]any
		raw, err := json.Marshal(export.Identity)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &wantIdentity); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Identity, wantIdentity) {
			t.Fatalf("expected[%s].identity = %v, want the seeded export's %v", id, got.Identity, wantIdentity)
		}
		if got.ReportedModel != export.ReportedModel {
			t.Fatalf("expected[%s].reported_model = %q, want the seeded export's %q", id, got.ReportedModel, export.ReportedModel)
		}
	}
}

func TestRunsManifest_TwoStartsEqualRosterHashDifferentRunID(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, runsDefaultScripts())
	first := s.Dial(t).Hello()
	firstRun, _ := runsReadManifest(t, s, first.RunID)

	// A start records the time it happened; the clock is the engine's.
	s.Clock.Advance(time.Second)
	s.Restart("", runsDefaultScripts())
	second := s.Dial(t).Hello()
	secondRun, _ := runsReadManifest(t, s, second.RunID)

	if first.RunID == second.RunID {
		t.Fatalf("two starts share run_id %s", first.RunID)
	}
	if first.RosterHash == "" || first.RosterHash != second.RosterHash {
		t.Fatalf("roster_hash %q then %q, want equal and not empty", first.RosterHash, second.RosterHash)
	}
	if firstRun.Hash == secondRun.Hash || first.ManifestHash == second.ManifestHash {
		t.Fatalf("manifest_hash %s then %s, want different", firstRun.Hash, secondRun.Hash)
	}
	if firstRun.Kind != "engine" || secondRun.Kind != "engine" {
		t.Fatalf("run kinds %q and %q, want engine for both", firstRun.Kind, secondRun.Kind)
	}
}

func TestRunsManifest_ChangedRosterDifferentHash(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, runsDefaultScripts())
	first := s.Dial(t).Hello()

	changed := strings.Replace(enginetest.RosterTOML, `attempt     = "300s"`, `attempt     = "310s"`, 1)
	if changed == enginetest.RosterTOML {
		t.Fatal("the roster text did not change")
	}
	s.Restart(changed, runsDefaultScripts())
	second := s.Dial(t).Hello()

	if first.RosterHash == "" || second.RosterHash == "" || first.RosterHash == second.RosterHash {
		t.Fatalf("roster_hash %q then %q, want two different hashes", first.RosterHash, second.RosterHash)
	}
	if st := s.Status(t, s.Dial(t)); st.RosterHash != second.RosterHash {
		t.Fatalf("status roster_hash = %s, want %s", st.RosterHash, second.RosterHash)
	}
}

func TestRunsManifest_UnprobedNoRecord(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, runsDefaultScripts(), enginetest.SkipProbe("reviewer"))
	c := s.Dial(t)

	st := s.Status(t, c)
	runsWantUnprobed(t, st, "reviewer", "no_record")
	if p := runsParticipant(t, st, "author"); p.State == engine.ParticipantUnprobed {
		t.Fatalf("author is %q, want probed", p.State)
	}
	// The engine started: it announced a run and lists the participant.
	ev := s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStarted })
	if es, _ := body[wire.EvEngineStarted](ev, wire.EventKindEngineStarted); es.RunID != c.Hello().RunID || !reflect.DeepEqual(es.Unprobed, []string{"reviewer"}) {
		t.Fatalf("engine_started = %+v, want the run %s with unprobed [reviewer]", es, c.Hello().RunID)
	}

	werr := runsRefusal(t, runsSubmitDefault(t, s, c, "s1"), wire.ErrorCodeUnprobed)
	if werr.Participant == nil || *werr.Participant != "reviewer" || werr.Detail == nil || *werr.Detail != "no_record" {
		t.Fatalf("unprobed refusal = participant %s detail %s, want reviewer and no_record", str(werr.Participant), str(werr.Detail))
	}
}

func TestRunsManifest_UnprobedFingerprintMismatch(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, runsDefaultScripts())
	if p := runsParticipant(t, s.Status(t, s.Dial(t)), "reviewer"); p.State == engine.ParticipantUnprobed {
		t.Fatalf("reviewer is unprobed before any record is changed: %+v", p)
	}

	// Each stage seeds a newer record for the reviewer whose fingerprint was
	// computed from something other than what the roster now says.
	stages := []struct {
		name string
		rec  runsRecord
	}{
		{"base_url", runsRecord{fingerprint: func(p config.EndpointProfile) config.EndpointProfile {
			p.BaseURL = "http://localhost:11435/v1"
			return p
		}}},
		{"auth reference", runsRecord{fingerprint: func(p config.EndpointProfile) config.EndpointProfile {
			p.Auth = &config.SecretRef{Source: "env", Name: "SYNDIC_OTHER_KEY"}
			return p
		}}},
		{"adapter version", runsRecord{adapterVersion: enginetest.AdapterVersion + "-other"}},
	}
	for i, stage := range stages {
		rec := stage.rec
		rec.model, rec.reported = "llama3", enginetest.DefaultReportedModel
		runsSeed(t, s, "local", rec)
		s.Restart("", runsDefaultScripts())
		c := s.Dial(t)

		runsWantUnprobed(t, s.Status(t, c), "reviewer", "fingerprint_mismatch")
		werr := runsRefusal(t, runsSubmitDefault(t, s, c, fmt.Sprintf("s%d", i)), wire.ErrorCodeUnprobed)
		if werr.Participant == nil || *werr.Participant != "reviewer" || werr.Detail == nil || *werr.Detail != "fingerprint_mismatch" {
			t.Fatalf("%s: unprobed refusal = participant %s detail %s, want reviewer and fingerprint_mismatch", stage.name, str(werr.Participant), str(werr.Detail))
		}
	}
}

func TestRunsManifest_UnprobedNoFingerprint(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, runsDefaultScripts(), enginetest.SkipProbe("reviewer"))

	// A bare export is what syndic probe wrote before it wrote a fingerprint.
	roster, err := config.Load(s.RosterPath)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := json.Marshal(runsExport(roster.Endpoints[0], "llama3", enginetest.DefaultReportedModel, s.Clock.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bare, []byte("config_fingerprint")) {
		t.Fatalf("the bare export carries a fingerprint: %s", bare)
	}
	if _, _, err := s.Store.CreateRun(bare); err != nil {
		t.Fatalf("seed bare export: %v", err)
	}
	s.Restart("", runsDefaultScripts())

	c := s.Dial(t)
	runsWantUnprobed(t, s.Status(t, c), "reviewer", "no_fingerprint")
	werr := runsRefusal(t, runsSubmitDefault(t, s, c, "s1"), wire.ErrorCodeUnprobed)
	if werr.Detail == nil || *werr.Detail != "no_fingerprint" {
		t.Fatalf("unprobed refusal detail = %s, want no_fingerprint", str(werr.Detail))
	}
}

func TestRunsManifest_SharedRecordSameEndpointModel(t *testing.T) {
	t.Parallel()
	roster := strings.Replace(enginetest.RosterTOML, `[[endpoints]]`, `[[participants]]
id = "reviewer2"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]`, 1)
	s := newSuite(t, roster, runsScripts("author", "reviewer", "reviewer2"))
	// Two records for the shared endpoint and model: only the newest is the
	// record, and both participants must select it. The author's own pair keeps
	// the harness's record.
	runsSeed(t, s, "local", runsRecord{model: "llama3", reported: "shared-older"})
	newest := runsSeed(t, s, "local", runsRecord{model: "llama3", reported: "shared-newest"})
	s.Restart("", runsScripts("author", "reviewer", "reviewer2"))

	c := s.Dial(t)
	st := s.Status(t, c)
	for _, id := range []string{"reviewer", "reviewer2"} {
		if p := runsParticipant(t, st, id); p.ReportedModel != newest.ReportedModel || p.State == engine.ParticipantUnprobed {
			t.Fatalf("participant %s is %q reporting %q, want the newest shared record's %q", id, p.State, p.ReportedModel, newest.ReportedModel)
		}
	}
	if p := runsParticipant(t, st, "author"); p.ReportedModel != enginetest.DefaultReportedModel {
		t.Fatalf("author reports %q, want its own record's %q", p.ReportedModel, enginetest.DefaultReportedModel)
	}

	_, members := runsReadManifest(t, s, c.Hello().RunID)
	var records map[string]json.RawMessage
	if err := json.Unmarshal(members["probe_records"], &records); err != nil {
		t.Fatalf("probe_records: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("probe_records has %d entries, want 3: %s", len(records), members["probe_records"])
	}
	if !bytes.Equal(records["reviewer"], records["reviewer2"]) {
		t.Fatalf("shared endpoint and model selected different records:\n%s\n%s", records["reviewer"], records["reviewer2"])
	}
	if bytes.Equal(records["reviewer"], records["author"]) {
		t.Fatalf("the author's record is the reviewers' record: %s", records["author"])
	}
	var picked evidence.ProbeManifest
	if err := json.Unmarshal(records["reviewer"], &picked); err != nil || picked.Export.ReportedModel != newest.ReportedModel {
		t.Fatalf("selected record reports %q (%v), want the newest, %q", picked.Export.ReportedModel, err, newest.ReportedModel)
	}
}

func TestRunsManifest_IneligibleRequiredReviewerPresetInvalid(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, runsDefaultScripts(), enginetest.WithEligibleRequiredReviewer("reviewer", transport.TriNo))
	c := s.Dial(t)

	if p := runsParticipant(t, s.Status(t, c), "reviewer"); p.EligibleRequiredReviewer != string(transport.TriNo) {
		t.Fatalf("status eligible_required_reviewer = %q, want %q", p.EligibleRequiredReviewer, transport.TriNo)
	}
	werr := runsRefusal(t, runsSubmitDefault(t, s, c, "s1"), wire.ErrorCodePresetInvalid)
	if werr.Detail == nil || *werr.Detail != "ineligible_required_reviewer:reviewer" {
		t.Fatalf("preset_invalid detail = %s, want ineligible_required_reviewer:reviewer", str(werr.Detail))
	}
	if len(s.calls("reviewer", "review"))+len(s.calls("author", "author")) != 0 {
		t.Fatal("a refused submission reached an adapter")
	}
}

func TestRunsManifest_RestartRunFirstSeqReplay(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, runsDefaultScripts())
	// The first run, and rows of other kinds before the second run.
	oldHello := s.Dial(t).Hello()
	for _, id := range runsProbeRunIDs(t, s) {
		payload, _ := json.Marshal(map[string]string{"run_id": id})
		if err := s.Store.AppendEvent("probe_completed", payload); err != nil {
			t.Fatalf("append probe_completed: %v", err)
		}
	}
	s.Restart("", runsDefaultScripts())

	c := s.Dial(t)
	hello := c.Hello()
	if hello.RunFirstSeq <= oldHello.RunFirstSeq || hello.RunID == oldHello.RunID {
		t.Fatalf("restart gave run %s at seq %d after run %s at seq %d", hello.RunID, hello.RunFirstSeq, oldHello.RunID, oldHello.RunFirstSeq)
	}
	// The hello names the seq of the new engine_started event.
	started := s.WaitEvent(func(ev event) bool { return ev.Kind == wire.EventKindEngineStarted && ev.Seq == hello.RunFirstSeq })
	if es, _ := body[wire.EvEngineStarted](started, wire.EventKindEngineStarted); es.RunID != hello.RunID {
		t.Fatalf("engine_started at run_first_seq is of run %s, want %s", es.RunID, hello.RunID)
	}
	rows, err := s.Store.EventsSince(0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	startedBefore := 0
	for _, row := range rows {
		if row.Kind == wire.EventKindEngineStarted && row.Seq > oldHello.RunFirstSeq-1 && row.Seq < hello.RunFirstSeq {
			startedBefore++
		}
		if row.Kind == wire.EventKindEngineStarted && row.Seq > hello.RunFirstSeq {
			t.Fatalf("a second engine_started at seq %d follows the run's own at %d", row.Seq, hello.RunFirstSeq)
		}
	}
	if startedBefore != 1 {
		t.Fatalf("the journal holds %d engine_started rows before the new run's, want the first run's one", startedBefore)
	}

	// The new run does some work, so its events are more than its start.
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	head, err := s.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}

	frames, _ := runsReplay(t, s, hello.RunFirstSeq-1, head)
	want, err := s.Store.EventsSince(hello.RunFirstSeq-1, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var wantRows []string
	for _, row := range want {
		if row.Seq <= head {
			wantRows = append(wantRows, fmt.Sprintf("%d:%s", row.Seq, row.Kind))
		}
	}
	var got []string
	engineStarts := 0
	for i, frame := range frames {
		got = append(got, fmt.Sprintf("%d:%s", frame.Seq, frame.Kind))
		if frame.Seq < hello.RunFirstSeq {
			t.Fatalf("replay carried seq %d, before run_first_seq %d", frame.Seq, hello.RunFirstSeq)
		}
		if i > 0 && frame.Seq != frames[i-1].Seq+1 {
			t.Fatalf("replay jumped from seq %d to %d", frames[i-1].Seq, frame.Seq)
		}
		if frame.Kind == wire.EventKindEngineStarted {
			engineStarts++
		}
		if frame.Kind == "probe_completed" {
			t.Fatalf("replay of the new run carried a probe_completed row at seq %d", frame.Seq)
		}
	}
	if len(frames) == 0 || frames[0].Seq != hello.RunFirstSeq || frames[0].Kind != wire.EventKindEngineStarted {
		t.Fatalf("replay begins with %v, want engine_started at %d", got, hello.RunFirstSeq)
	}
	if engineStarts != 1 {
		t.Fatalf("replay carried %d engine_started frames, want 1", engineStarts)
	}
	if len(frames) < 4 {
		t.Fatalf("replay of a run with a turn is only %v", got)
	}
	if !reflect.DeepEqual(got, wantRows) {
		t.Fatalf("replay = %v, want exactly the journal rows after run_first_seq-1: %v", got, wantRows)
	}
	var turnStates int
	for _, frame := range frames {
		if st, ok := body[wire.EvTurnState](event{Kind: frame.Kind, Body: frame.Body}, wire.EventKindTurnState); ok && st.TurnID == turnID {
			turnStates++
		}
	}
	if turnStates == 0 {
		t.Fatalf("replay of the new run carried no state of its turn %s: %v", turnID, got)
	}
}

func TestRunsManifest_SubscribeSinceZeroIgnoresProbeCompleted(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, runsDefaultScripts())

	// The seeded probe rows, each announced the way syndic probe announces it.
	probes := runsProbeRunIDs(t, s)
	if len(probes) != 2 {
		t.Fatalf("the harness seeded %d probe runs, want 2", len(probes))
	}
	for _, id := range probes {
		payload, _ := json.Marshal(map[string]string{"run_id": id})
		if err := s.Store.AppendEvent("probe_completed", payload); err != nil {
			t.Fatalf("append probe_completed: %v", err)
		}
	}
	s.Restart("", runsDefaultScripts())
	hello := s.Dial(t).Hello()
	head, err := s.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}

	frames, c := runsReplay(t, s, 0, head)
	var probeRuns []string
	var current []wire.Frame
	for _, frame := range frames {
		if frame.Kind == "probe_completed" {
			if frame.Seq >= hello.RunFirstSeq {
				t.Fatalf("probe_completed at seq %d, not before run_first_seq %d", frame.Seq, hello.RunFirstSeq)
			}
			var p struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(frame.Body, &p); err != nil {
				t.Fatalf("probe_completed body %s: %v", frame.Body, err)
			}
			probeRuns = append(probeRuns, p.RunID)
		}
		if frame.Seq >= hello.RunFirstSeq {
			current = append(current, frame)
		}
	}
	if !reflect.DeepEqual(probeRuns, probes) {
		t.Fatalf("replay from 0 carried probe_completed for %v, want the seeded %v", probeRuns, probes)
	}
	// The client delivered them without error and kept reading: it ended at
	// the head and its position moved past them.
	if c.LastSeq() != head {
		t.Fatalf("client position = %d after the replay, want the head %d", c.LastSeq(), head)
	}

	// What belongs to the current run is what begins at run_first_seq, and no
	// probe row is in it.
	if len(current) == 0 || current[0].Seq != hello.RunFirstSeq || current[0].Kind != wire.EventKindEngineStarted {
		t.Fatalf("current run frames = %d, want a run beginning with engine_started at %d", len(current), hello.RunFirstSeq)
	}
	if es, _ := body[wire.EvEngineStarted](event{Kind: current[0].Kind, Body: current[0].Body}, wire.EventKindEngineStarted); es.RunID != hello.RunID {
		t.Fatalf("current run begins with run %s, want %s", es.RunID, hello.RunID)
	}
	for _, frame := range current {
		if frame.Kind == "probe_completed" {
			t.Fatalf("probe_completed at seq %d counted as part of the current run", frame.Seq)
		}
	}
	for _, frame := range frames {
		if frame.Seq < hello.RunFirstSeq && frame.Kind == wire.EventKindEngineStarted {
			if es, _ := body[wire.EvEngineStarted](event{Kind: frame.Kind, Body: frame.Body}, wire.EventKindEngineStarted); es.RunID == hello.RunID {
				t.Fatalf("an engine_started of the current run at seq %d, before run_first_seq", frame.Seq)
			}
		}
	}

	// A client that wants the current run alone is not shown the probe rows.
	own, _ := runsReplay(t, s, hello.RunFirstSeq-1, head)
	if len(own) != len(current) {
		t.Fatalf("replay from run_first_seq-1 carried %d frames, the current run in the full replay has %d", len(own), len(current))
	}
	if later := s.Dial(t).Hello().RunID; later != hello.RunID {
		t.Fatalf("a later client's hello names run %s, want %s", later, hello.RunID)
	}
}

// runsAllowedTop is the list of manifest members in section 1a.
var runsAllowedTop = []string{"kind", "engine_version", "started_at", "roster", "roster_path", "roster_file_sha256", "probe_records", "expected"}

// runsAllowedRoster is the list of roster members in section 1a, with the
// two members section 2b adds to the roster file: the [engine] table and
// default_preset.
var runsAllowedRoster = []string{"participants", "endpoints", "presets", "policy", "pools", "deadlines", "budgets", "quick", "engine", "default_preset"}

// runsUnexpected returns the members of an object that are not in allowed.
func runsUnexpected(t *testing.T, raw json.RawMessage, allowed []string) []string {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatalf("not a JSON object: %v: %s", err, raw)
	}
	ok := map[string]bool{}
	for _, name := range allowed {
		ok[name] = true
	}
	var extra []string
	for name := range members {
		if !ok[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return extra
}

func TestRunsManifest_ManifestContainsNoSecretValue(t *testing.T) {
	t.Parallel()
	const (
		sentinelKey    = "SENTINEL-api-key-7f3a91c2"
		sentinelHeader = "SENTINEL-org-header-5be04d18"
	)
	secrets := t.TempDir()
	for name, value := range map[string]string{"api-key": sentinelKey + "\n", "org-key": sentinelHeader + "\n"} {
		if err := os.WriteFile(filepath.Join(secrets, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	roster := strings.Replace(enginetest.RosterTOML, `dialect = "chat_completions_v1"`, `dialect = "chat_completions_v1"
auth = { source = "file", name = "api-key" }
extra_headers = { X-Org = { source = "file", name = "org-key" } }`, 1)

	// The engine is built here, not by the harness, so that its adapter
	// factory can resolve the endpoint's secrets as syndicd does.
	h := enginetest.New(t, roster, runsDefaultScripts(), enginetest.NoStart())
	resolver := config.NewEnvFileResolver(secrets)
	var resolved []string
	eng, err := engine.New(engine.Options{
		Store:         h.Store,
		Hub:           h.Hub,
		RosterPath:    h.RosterPath,
		EngineVersion: enginetest.EngineVersion,
		Clock:         h.Clock,
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			key, err := resolver.Resolve(*ep.Auth)
			if err != nil {
				return nil, "", err
			}
			header, err := resolver.Resolve(ep.ExtraHeaders["X-Org"])
			if err != nil {
				return nil, "", err
			}
			resolved = append(resolved, key, header)
			return fake.New(nil, fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel}), enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	t.Cleanup(func() {
		if err := eng.Stop(t.Context()); err != nil {
			t.Errorf("engine.Stop: %v", err)
		}
	})
	if len(resolved) == 0 {
		t.Fatal("the engine never resolved the endpoint's secrets")
	}
	for i, got := range resolved {
		if want := []string{sentinelKey, sentinelHeader}[i%2]; got != want {
			t.Fatalf("resolver returned %q, want the sentinel %q", got, want)
		}
	}

	run, err := h.Store.GetRun(eng.Hello().RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	for _, secret := range []string{sentinelKey, sentinelHeader, "SENTINEL"} {
		if bytes.Contains(run.Manifest, []byte(secret)) {
			t.Fatalf("the manifest contains %q: %s", secret, run.Manifest)
		}
	}
	events, err := h.Store.EventsSince(0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if bytes.Contains(ev.Data, []byte("SENTINEL")) {
			t.Fatalf("event %d (%s) contains a secret: %s", ev.Seq, ev.Kind, ev.Data)
		}
	}

	// The references are there, by name.
	var m struct {
		Roster struct {
			Endpoints []map[string]any `json:"endpoints"`
		} `json:"roster"`
	}
	if err := json.Unmarshal(run.Manifest, &m); err != nil || len(m.Roster.Endpoints) != 1 {
		t.Fatalf("manifest endpoints: %v: %s", err, run.Manifest)
	}
	ep := m.Roster.Endpoints[0]
	if ep["auth"] != "file:api-key" || !reflect.DeepEqual(ep["extra_headers"], map[string]any{"X-Org": "file:org-key"}) {
		t.Fatalf("manifest endpoint carries auth %v and extra_headers %v, want the references file:api-key and file:org-key", ep["auth"], ep["extra_headers"])
	}

	// Members: exactly the list in section 1a at the top, in the roster, and in
	// each selected record and expected entry.
	if extra := runsUnexpected(t, run.Manifest, runsAllowedTop); len(extra) != 0 {
		t.Fatalf("the manifest has members outside section 1a: %v", extra)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(run.Manifest, &members); err != nil {
		t.Fatal(err)
	}
	for _, name := range runsAllowedTop {
		if _, ok := members[name]; !ok {
			t.Fatalf("the manifest has no member %q", name)
		}
	}
	if extra := runsUnexpected(t, members["roster"], runsAllowedRoster); len(extra) != 0 {
		t.Fatalf("the manifest roster has members outside section 1a: %v", extra)
	}
	for member, allowed := range map[string][]string{
		"probe_records": {"export", "config_fingerprint", "fingerprint_rule", "calibration"},
		"expected":      {"identity", "reported_model"},
	} {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(members[member], &entries); err != nil || len(entries) != 2 {
			t.Fatalf("%s: %d entries (%v), want one per participant", member, len(entries), err)
		}
		for id, entry := range entries {
			if extra := runsUnexpected(t, entry, allowed); len(extra) != 0 {
				t.Fatalf("%s[%s] has members outside section 1a: %v", member, id, extra)
			}
		}
	}
}

// nRolesTOML is a roster with an author and n reviewers, each on its own
// model, whose default preset names every reviewer as required. A positive
// turnMax sets turn_max_estimated_tokens. selfPreset adds a preset that names
// the author among its required reviewers.
func nRolesTOML(n, turnMax int, selfPreset bool) string {
	models := []string{`"m-author"`}
	var b strings.Builder
	b.WriteString("default_preset = \"review\"\n\n[deadlines]\nattempt = \"300s\"\nturn = \"900s\"\ndelivery = \"60s\"\nprovisional = false\n\n")
	if turnMax > 0 {
		fmt.Fprintf(&b, "[budgets]\nturn_max_estimated_tokens = %d\n\n", turnMax)
	}
	roles := `["author"]`
	if selfPreset {
		roles = `["author", "reviewer"]`
	}
	fmt.Fprintf(&b, "[[participants]]\nid = \"author\"\nmodel = \"m-author\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = %s\npool = \"default\"\n\n", roles)
	var ids []string
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("r%d", i)
		ids = append(ids, fmt.Sprintf("%q", id))
		models = append(models, fmt.Sprintf("%q", "m-"+id))
		fmt.Fprintf(&b, "[[participants]]\nid = %q\nmodel = %q\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \"default\"\naccept_unknown_truncation = true\n\n", id, "m-"+id)
	}
	fmt.Fprintf(&b, `[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "none"
network_dependency = "none"
dialect = "chat_completions_v1"
models = [%s]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = [%s]

[presets.solo]
mode = "solo"
author = "author"
required_reviewers = []
`, strings.Join(models, ", "), strings.Join(ids, ", "))
	if selfPreset {
		b.WriteString("\n[presets.selfreview]\nmode = \"review\"\nauthor = \"author\"\nrequired_reviewers = [\"author\", \"r1\"]\n")
	}
	b.WriteString("\n[policy]\nlocal_only = true\nallowed_destinations = []\n")
	return b.String()
}

// nRolesIDs lists the author and the n reviewers of nRolesTOML.
func nRolesIDs(n int) []string {
	ids := []string{"author"}
	for i := 1; i <= n; i++ {
		ids = append(ids, fmt.Sprintf("r%d", i))
	}
	return ids
}

func TestNAndRoles_TwoReviewersNeeded(t *testing.T) {
	t.Parallel()
	s := newSuite(t, nRolesTOML(2, 0, false), runsScripts(nRolesIDs(2)...))
	// The second reviewer is slow: the first has approved by the time the turn
	// is observed, and the turn must still be open.
	release := s.h.Hold("r2")
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitEvent(func(ev event) bool {
		v, ok := body[wire.EvVerdictRecorded](ev, wire.EventKindVerdictRecorded)
		return ok && v.Parsed && v.ValidationStatus != ""
	})

	open := s.Turn(t, c, turnID)
	if terminal(open.State) || open.TerminalReason != nil {
		t.Fatalf("turn ended %q (%s) on one reviewer's approval, want it open until both have answered", open.State, str(open.TerminalReason))
	}
	if len(open.Verdicts) != 1 {
		t.Fatalf("turn holds %d verdicts while one reviewer is pending, want 1", len(open.Verdicts))
	}
	if open := s.Status(t, c).TurnsOpen; !reflect.DeepEqual(open, []string{turnID}) {
		t.Fatalf("turns_open = %v, want [%s]", open, turnID)
	}

	release()
	s.WaitTerminal(turnID)
	done := s.Turn(t, c, turnID)
	wantTurn(t, done, engine.StateApproved, "all_approved")
	if len(done.Verdicts) != 2 {
		t.Fatalf("approved turn holds %d verdicts, want one per required reviewer", len(done.Verdicts))
	}
	for _, v := range done.Verdicts {
		if !v.Parsed {
			t.Fatalf("verdict %+v is not parsed", v)
		}
	}
	for _, id := range []string{"r1", "r2"} {
		if n := len(s.calls(id, "review")); n != 1 {
			t.Fatalf("%s received %d review requests, want 1", id, n)
		}
	}
}

func TestNAndRoles_ApproveAndBlockIncomplete(t *testing.T) {
	t.Parallel()
	scripts := runsScripts("author")
	scripts["r1"] = []fake.Step{verdict(fake.VerdictApprove), normal()}
	scripts["r2"] = []fake.Step{verdict(func(hash, attempt string) string {
		return fake.VerdictBlock(hash, attempt, "b1", "high", "the evidence", "the remedy", "the reason")
	}), normal()}
	s := newSuite(t, nRolesTOML(2, 0, false), scripts)
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	turn := s.Turn(t, c, turnID)
	if len(turn.OpenBlocks) != 1 || turn.OpenBlocks[0].State != "open" {
		t.Fatalf("open blocks = %+v, want the one block, open", turn.OpenBlocks)
	}
	wantTurn(t, turn, engine.StateIncomplete, "block:"+turn.OpenBlocks[0].ID)
	if len(turn.Verdicts) != 2 {
		t.Fatalf("turn holds %d verdicts, want the approve and the block", len(turn.Verdicts))
	}
	// Both reviewers were asked: the approval did not end the turn early and the
	// block did not stop it from being recorded.
	for _, id := range []string{"r1", "r2"} {
		if n := len(s.calls(id, "review")); n != 1 {
			t.Fatalf("%s received %d review requests, want 1", id, n)
		}
	}
}

func TestNAndRoles_AuthorAsReviewerRefused(t *testing.T) {
	t.Parallel()
	gap(t, "config.Validate rejects a preset whose required_reviewers contains its author when the roster is loaded (config/validator.go: \"author is also in required reviewers\"), so the engine refuses to start on such a roster and no submit can ever name the preset; the design has the engine start and refuse the submit")
	s := newSuite(t, nRolesTOML(2, 0, false), runsScripts(nRolesIDs(2)...))
	s.Restart(nRolesTOML(2, 0, true), runsScripts(nRolesIDs(2)...))
	c := s.Dial(t)

	preset := "selfreview"
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "s1", Input: "what is the answer?", Preset: &preset})
	werr := runsRefusal(t, err, wire.ErrorCodePresetInvalid)
	if werr.Detail == nil || !strings.Contains(*werr.Detail, "author") {
		t.Fatalf("preset_invalid detail = %s, want one naming the author", str(werr.Detail))
	}
	if len(s.calls("author", "author")) != 0 {
		t.Fatal("a refused submission reached the author")
	}
}

func TestNAndRoles_MultipleReviewers(t *testing.T) {
	t.Parallel()
	for _, n := range []int{3, 5} {
		s := newSuite(t, nRolesTOML(n, 0, false), runsScripts(nRolesIDs(n)...))
		c := s.Dial(t)

		st := s.Status(t, c)
		if len(st.Participants) != n+1 {
			t.Fatalf("N=%d: status lists %d participants, want %d", n, len(st.Participants), n+1)
		}
		turnID := s.Submit(t, c, fmt.Sprintf("s%d", n))
		s.WaitTerminal(turnID)
		turn := s.Turn(t, c, turnID)
		wantTurn(t, turn, engine.StateApproved, "all_approved")
		if len(turn.Verdicts) != n {
			t.Fatalf("N=%d: turn holds %d verdicts, want %d", n, len(turn.Verdicts), n)
		}
		for _, id := range nRolesIDs(n)[1:] {
			if got := len(s.calls(id, "review")); got != 1 {
				t.Fatalf("N=%d: %s received %d review requests, want 1", n, id, got)
			}
		}
	}
}

func TestNAndRoles_ProjectionBudgetExceeded(t *testing.T) {
	t.Parallel()
	const tight, raised = 100, 1000000
	s := newSuite(t, nRolesTOML(2, tight, false), runsScripts(nRolesIDs(2)...))
	c := s.Dial(t)

	werr := runsRefusal(t, runsSubmitDefault(t, s, c, "s1"), wire.ErrorCodeBudget)
	if werr.Budget == nil || *werr.Budget != "turn_max_estimated_tokens" {
		t.Fatalf("budget refusal names budget %s, want turn_max_estimated_tokens", str(werr.Budget))
	}
	if werr.Limit == nil || *werr.Limit != tight || werr.Estimated == nil || *werr.Estimated <= tight {
		t.Fatalf("budget refusal estimated %v against limit %v, want an estimate over %d", werr.Estimated, werr.Limit, tight)
	}
	if n := len(s.Status(t, c).TurnsOpen); n != 0 {
		t.Fatalf("a refused submission left %d turns open", n)
	}
	if len(s.calls("author", "author")) != 0 {
		t.Fatal("a refused submission reached the author")
	}

	// The same roster with the limit raised admits the same submission.
	s.Restart(nRolesTOML(2, raised, false), runsScripts(nRolesIDs(2)...))
	c = s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
}
