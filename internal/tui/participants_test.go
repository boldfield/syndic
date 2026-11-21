package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/mattn/go-runewidth"
)

func TestModelFamily(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Llama3.1:70b", "llama"},
		{"llama3.2", "llama"},
		{"gpt-4o", "gpt"},
		{"gpt-4.1", "gpt"},
		{"qwen2.5", "qwen"},
		{"mistral", "mistral"},
		{"", ""},
		{"3b", ""},
	} {
		if got := modelFamily(c.in); got != c.want {
			t.Errorf("modelFamily(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSameFamily(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"Llama3.1:70b", "llama3.2", true},
		{"gpt-4o", "gpt-4.1", true},
		{"qwen2.5", "llama3", false},
		{"", "", false},
		{"", "llama3", false},
		{"llama3", "", false},
		{"3b", "7b", false},
	} {
		if got := sameFamily(c.a, c.b); got != c.want {
			t.Errorf("sameFamily(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{45 * time.Second, "45s"},
		{200 * time.Second, "3m20s"},
		{125 * time.Minute, "2h5m"},
		{76 * time.Hour, "3d4h"},
	} {
		if got := formatAge(c.d); got != c.want {
			t.Errorf("formatAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func i64(v int64) *int64 { return &v }

// wideState is a roster whose names and values are long enough to overflow
// 80 columns, with a current turn so the same-family marker shows.
func wideState() State {
	long := strings.Repeat("x", 40)
	observed := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	s := NewState()
	for _, id := range []string{"author-" + long, "reviewer-" + long, "世界-" + long} {
		s.Participants[id] = ParticipantInfo{
			ID: id, State: "unprobed", Detail: "fingerprint_mismatch", Pool: "pool-" + long,
			ReportedModel: "llama3.1:70b-" + long, Identity: "unknown", EligibleRoles: []string{"author", "reviewer"},
			ProbeObservedAt: &observed, ContextBound: i64(128000), OutputBound: i64(4096),
			DeclaredBilling: "flat", Estimate: "exceeded", EligibleRequiredReviewer: "unknown",
		}
	}
	s.Turns["t1"] = TurnInfo{ID: "t1", Author: "author-" + long, Reviewers: []string{"reviewer-" + long}}
	s.CurrentTurn = "t1"
	s.Pools["pool-"+long] = wire.Pool{Name: "pool-" + long, Concurrency: 1, Holds: []wire.PoolHold{{AttemptID: "a-" + long, HeldSince: "2026-11-18T12:00:00Z"}}}
	s.OrphanedHolds = []wire.Hold{{Pool: "old-" + long, AttemptID: "a-" + long}}
	return s
}

func TestRenderParticipantsFitsEveryWidth(t *testing.T) {
	s := wideState()
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	for _, width := range []int{1, 20, 40, 80, 120} {
		for _, id := range append(participantIDs(s), "") {
			for _, l := range strings.Split(renderParticipants(s, width, 40, id, now), "\n") {
				if w := runewidth.StringWidth(l); w > width {
					t.Fatalf("width %d, selected %q: line of %d cells: %q", width, id, w, l)
				}
			}
		}
	}
}

func TestRenderParticipantsDropsColumnsAt80(t *testing.T) {
	s := NewState()
	s.Participants["author"] = ParticipantInfo{ID: "author", State: "idle", Pool: "default", ReportedModel: "llama3.2:8b", EligibleRoles: []string{"author"}, ContextBound: i64(128000)}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	wide := renderParticipants(s, 200, 40, "", now)
	narrow := renderParticipants(s, 40, 40, "", now)
	if !strings.Contains(wide, "ctx") || strings.Contains(narrow, "ctx") {
		t.Fatalf("columns not dropped by width:\n%s\n---\n%s", wide, narrow)
	}
	for _, want := range []string{"id", "state", "idle"} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("narrow table lost %q:\n%s", want, narrow)
		}
	}
}

func TestRenderParticipantsControlCharacters(t *testing.T) {
	s := NewState()
	s.Participants["a"] = ParticipantInfo{ID: "a", State: "idle", ReportedModel: "evil\x1b[2Jmodel"}
	out := renderParticipants(s, 80, 20, "a", time.Now())
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("escape reached the output: %q", out)
	}
}

func TestRenderParticipantsAuthorControlCharacters(t *testing.T) {
	s := NewState()
	s.Participants["a\x1b[Hx"] = ParticipantInfo{ID: "a\x1b[Hx", State: "busy", ReportedModel: "model\x1b[2J"}
	s.Participants["r"] = ParticipantInfo{ID: "r", State: "busy", ReportedModel: "model"}
	s.Turns["t1"] = TurnInfo{ID: "t1", Author: "a\x1b[Hx", Reviewers: []string{"r"}}
	s.CurrentTurn = "t1"
	out := renderParticipants(s, 80, 20, "r", time.Now())
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("escape reached the output: %q", out)
	}
	if !strings.Contains(out, "author: a·[Hx (model·[2J)") {
		t.Fatalf("author line not sanitized as expected: %q", out)
	}
}

// partRoster has an author, two reviewers on different endpoints and pools, and
// three participants whose records are missing, bare or for another base_url.
func partRoster() string {
	var b strings.Builder
	b.WriteString("default_preset = \"review\"\n\n[deadlines]\nattempt = \"300s\"\nturn = \"900s\"\ndelivery = \"60s\"\nprovisional = false\n\n")
	part := func(id, endpoint, model, role, pool, extra string) {
		fmt.Fprintf(&b, "[[participants]]\nid = %q\nmodel = %q\nroute = \"api\"\nendpoint = %q\neligible_roles = [%q]\npool = %q\n%s\n", id, model, endpoint, role, pool, extra)
	}
	part("author", "local", "llama2", "author", "default", "")
	part("r1", "local", "llama3", "reviewer", "default", "context_tokens = 100000\nbytes_per_token = 3.5")
	part("r2", "paid", "qwen", "reviewer", "p2", "")
	part("nofp", "local", "nofp", "reviewer", "spare", "")
	part("mism", "local", "mism", "reviewer", "spare", "")
	part("norec", "local", "norec", "reviewer", "spare", "")
	endpoint := func(name, port, billing, models string) {
		fmt.Fprintf(&b, "[[endpoints]]\nname = %q\nbase_url = \"http://localhost:%s/v1\"\ndeclared_location = \"local\"\ndeclared_execution = \"local\"\ndeclared_billing = %q\nnetwork_dependency = \"none\"\ndialect = \"chat_completions_v1\"\nmodels = [%s]\ntokens_per_byte_max = 1.0\n\n", name, port, billing, models)
	}
	endpoint("local", "11434", "none", `"llama2", "llama3", "nofp", "mism", "norec"`)
	endpoint("paid", "11435", "flat", `"qwen"`)
	b.WriteString("[presets.review]\nmode = \"review\"\nauthor = \"author\"\nrequired_reviewers = [\"r1\", \"r2\"]\n\n[policy]\nlocal_only = true\nallowed_destinations = []\n\n[pools.default]\nconcurrency = 1\n\n[pools.p2]\nconcurrency = 1\n\n[pools.spare]\nconcurrency = 1\n")
	return b.String()
}

type seedKind int

const (
	seedFull seedKind = iota
	seedNoBounds
	seedBare
	seedMismatch
)

// seedRecord writes a probe run for the endpoint and model, newer than any in the journal.
func seedRecord(t *testing.T, h *enginetest.Harness, endpoint, model, reported string, kind seedKind, outputBound *int) {
	t.Helper()
	roster, err := config.Load(h.RosterPath)
	if err != nil {
		t.Fatal(err)
	}
	var profile config.EndpointProfile
	for _, ep := range roster.Endpoints {
		if ep.Name == endpoint {
			profile = ep
		}
	}
	bound := enginetest.DefaultContextBound
	calibration := transport.Calibration{Available: true, BytesPerToken: enginetest.DefaultCalibration}
	if kind == seedNoBounds {
		bound, calibration = 0, transport.Calibration{}
	}
	export := evidence.Export{
		Redacted: true, RedactionRule: "redaction/v1", EndpointName: profile.Name,
		DeclaredLocation: profile.DeclaredLocation, DeclaredExecution: profile.DeclaredExecution,
		DeclaredBilling: profile.DeclaredBilling, NetworkDependency: profile.NetworkDependency,
		Model: model, AuthMode: "none", Identity: transport.NewUnknownIdentity(), ReportedModel: reported,
		Streaming: transport.TriUnknown, CancelObservable: transport.TriUnknown, UsageAvailable: transport.TriUnknown,
		ModelsEndpoint: transport.TriUnknown, ModelListed: transport.TriUnknown, SystemRoleHonored: transport.TriUnknown,
		EligibleRequiredReviewer: transport.TriUnknown, AdapterVersion: enginetest.AdapterVersion,
		OutputBound: outputBound, ObservedAt: h.Clock.Now(),
	}
	if bound > 0 {
		export.ContextBound = &bound
	}
	var data []byte
	if kind == seedBare {
		data, err = json.Marshal(export)
	} else {
		fp := profile
		if kind == seedMismatch {
			fp.BaseURL = "http://localhost:1/v1"
		}
		var pm evidence.ProbeManifest
		pm, err = evidence.NewProbeManifest(export, fp, model, enginetest.AdapterVersion, calibration)
		if err == nil {
			data, err = json.Marshal(pm)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Store.CreateRun(data); err != nil {
		t.Fatal(err)
	}
}

func reporting(s fake.Step, model string) fake.Step {
	s.ReportedModel = model
	return s
}

// startPartsRig runs an App on the participants tab over h, reading the
// harness's clock for ages.
func startPartsRig(t *testing.T, h *enginetest.Harness) *rig {
	t.Helper()
	paths := newPaths(t)
	r := &rig{h: h, paths: paths, seen: &seen{}}
	r.srv = startServer(t, paths, h.Engine, h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.app = NewApp(ctx, paths.Socket)
	r.app.now = h.Clock.Now
	r.tm = teatest.NewTestModel(t, &probe{app: r.app}, teatest.WithInitialTermSize(80, 24))
	t.Cleanup(func() { _ = r.tm.Quit() })
	r.waitConnected(t)
	send(r, tea.KeyMsg{Type: tea.KeyTab}, tea.KeyMsg{Type: tea.KeyTab})
	return r
}

// roster is the harness over partRoster with the probe records described there.
func rosterHarness(t *testing.T) *enginetest.Harness {
	t.Helper()
	bound := 4096
	h := enginetest.New(t, partRoster(), map[string][]fake.Step{
		"author": {reporting(enginetest.Reply(answerText), "llama3.2:8b")},
		"r1":     {{Kind: fake.StepKindHang}},
		"r2":     {{Kind: fake.StepKindHang}},
	}, enginetest.ClockStart(farFutureTime()), enginetest.NoStart(), enginetest.SkipProbe("author"), enginetest.SkipProbe("r1"), enginetest.SkipProbe("r2"),
		enginetest.SkipProbe("nofp"), enginetest.SkipProbe("mism"), enginetest.SkipProbe("norec"))
	seedRecord(t, h, "local", "llama2", "llama3.2:8b", seedFull, &bound)
	seedRecord(t, h, "local", "llama3", "llama3.1:70b", seedNoBounds, nil)
	seedRecord(t, h, "paid", "qwen", "qwen2.5:32b", seedFull, nil)
	seedRecord(t, h, "local", "nofp", "m", seedBare, nil)
	seedRecord(t, h, "local", "mism", "m", seedMismatch, nil)
	if err := h.Engine.Start(); err != nil {
		t.Fatal(err)
	}
	return h
}

// untilPartsView waits for the View to hold every part and returns it; on a
// timeout it fails with the last View the program drew.
func untilPartsView(t *testing.T, r *rig, parts ...string) string {
	t.Helper()
	var mu sync.Mutex
	last := ""
	done := make(chan string, 1)
	w := &waiter{done: done, cond: func(a *App) (string, bool) {
		v, ok := viewHas(parts...)(a)
		mu.Lock()
		last = v
		mu.Unlock()
		return v, ok
	}}
	r.tm.Send(waitMsg{w})
	select {
	case v := <-done:
		return v
	case <-time.After(15 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		var events strings.Builder
		for _, e := range r.h.Events() {
			j, _ := json.Marshal(e.Payload)
			fmt.Fprintf(&events, "%d %s %s\n", e.Seq, e.Kind, j)
		}
		t.Fatalf("timed out waiting for a view with %q; last view:\n%s\nevents:\n%s", parts, last, events.String())
		return ""
	}
}

func downs(n int) []tea.Msg {
	out := make([]tea.Msg, n)
	for i := range out {
		out[i] = tea.KeyMsg{Type: tea.KeyDown}
	}
	return out
}

// rowOf is the table row of the participant in the view.
func rowOf(view, id string) string {
	for _, l := range strings.Split(view, "\n") {
		if strings.HasPrefix(l, "  "+id+" ") || strings.HasPrefix(l, "> "+id+" ") {
			return l
		}
	}
	return ""
}

func wantParts(t *testing.T, what, got string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(got, p) {
			t.Errorf("%s lacks %q:\n%s", what, p, got)
		}
	}
}

func TestParticipantsRowsAndUnprobed(t *testing.T) {
	r := startPartsRig(t, rosterHarness(t))
	v := untilPartsView(t, r, "unprobed:no_record", "unprobed:fingerprint_mismatch", "unprobed:no_fingerprint")
	for id, parts := range map[string][]string{
		"author": {"idle", "default", "llama3.2:8b"},
		"r1":     {"idle", "default", "llama3.1:70b"},
		"r2":     {"idle", "p2", "qwen2.5:32b"},
		"nofp":   {"unprobed:no_fingerprint", "spare"},
		"mism":   {"unprobed:fingerprint_mismatch", "spare"},
		"norec":  {"unprobed:no_record", "spare"},
	} {
		row := rowOf(v, id)
		if row == "" {
			t.Fatalf("no row for %s:\n%s", id, v)
		}
		wantParts(t, "row of "+id, row, parts...)
	}
	// Selection starts on the first row; ids are sorted: author mism nofp norec r1 r2.
	for i, c := range []struct{ id, detail string }{{"mism", "fingerprint_mismatch"}, {"nofp", "no_fingerprint"}, {"norec", "no_record"}} {
		send(r, downs(1)...)
		v := untilPartsView(t, r, "detail: "+c.id)
		wantParts(t, fmt.Sprintf("detail %d", i), v, "state: unprobed ("+c.detail+")", "hint: "+unprobedHint)
	}
}

func TestParticipantsSameFamilyDuringTurn(t *testing.T) {
	r := startPartsRig(t, rosterHarness(t))
	r.typeLine("question")
	// Both reviewers hang with their attempt begun, so both are reviewers of the turn.
	r.until(t, "both reviewers dispatched", func(a *App) (string, bool) {
		ti := a.state.Turns[a.state.CurrentTurn]
		return fmt.Sprintf("%+v %s", ti, a.View()), len(ti.Reviewers) == 2
	})
	v := untilPartsView(t, r, "author: author (llama3.2:8b)", "same-family")
	if !strings.Contains(rowOf(v, "r1"), "same-family") {
		t.Errorf("r1 (llama3.1:70b) is not marked against author llama3.2:8b:\n%s", v)
	}
	if strings.Contains(rowOf(v, "r2"), "same-family") || strings.Contains(rowOf(v, "author"), "same-family") {
		t.Errorf("r2 (qwen2.5:32b) or the author is marked:\n%s", v)
	}
	send(r, downs(4)...)
	v = untilPartsView(t, r, "detail: r1")
	wantParts(t, "r1 detail", v, "same-family")
	send(r, downs(1)...)
	v = untilPartsView(t, r, "detail: r2")
	if strings.Contains(v, "same-family:") {
		t.Errorf("r2 detail carries the marker:\n%s", v)
	}
}

func TestParticipantsDetailFields(t *testing.T) {
	r := startPartsRig(t, rosterHarness(t))
	untilPartsView(t, r, "unprobed:no_record")
	// The author: probed, bounds from the probe record, output bound from the record.
	v := untilPartsView(t, r, "detail: author")
	wantParts(t, "author detail", v,
		"state: idle", "pool: default", "model: llama3.2:8b", "identity: unknown (name-matched)",
		"roles: author", "billing: none", "probe observed_at: 2099-01-01 00:00:00Z (", " ago)",
		"context bound: 128000 (probe)", "output bound: 4096 (probe)", "bytes per token: 3.00 (probe)")
	if strings.Contains(v, "truncation: unknown") {
		t.Errorf("the author is not a reviewer, but the detail says truncation unknown:\n%s", v)
	}
	// r1 is configured through the roster.
	send(r, downs(4)...)
	v = untilPartsView(t, r, "detail: r1")
	wantParts(t, "r1 detail", v, "context bound: 100000 (roster)", "bytes per token: 3.50 (roster)",
		"roles: reviewer", "billing: none", "truncation: unknown", "output bound: -")
	// r2 is on the flat-billed endpoint.
	send(r, downs(1)...)
	v = untilPartsView(t, r, "detail: r2")
	wantParts(t, "r2 detail", v, "billing: flat", "model: qwen2.5:32b")
	for _, l := range strings.Split(v, "\n") {
		if w := runewidth.StringWidth(l); w > 80 {
			t.Errorf("line of %d cells at 80 columns: %q", w, l)
		}
	}
}

func TestParticipantsEstimateExceeded(t *testing.T) {
	over := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
	over.Usage = &transport.Usage{Prompt: 20000, Completion: 10}
	h := scripted(t, enginetest.Reply(answerText), over)
	r := startPartsRig(t, h)
	untilPartsView(t, r, "reviewer")
	r.typeLine("question")
	turnIn(t, r, "approved")
	send(r, downs(1)...)
	v := untilPartsView(t, r, "detail: reviewer", "estimate: exceeded")
	if strings.Contains(rowOf(v, "author"), "exceeded") {
		t.Errorf("the author is marked:\n%s", v)
	}
}

// farFutureTime is where tests that hang an adapter start the fake clock: the
// attempt's context deadline is on the real clock, and must not have passed.
func farFutureTime() time.Time { return time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC) }

func holdHarness(t *testing.T, roster string) *enginetest.Harness {
	t.Helper()
	return enginetest.New(t, roster, map[string][]fake.Step{
		"author":   {enginetest.Reply(answerText)},
		"reviewer": {{Kind: fake.StepKindHang}},
	}, enginetest.ClockStart(farFutureTime()), enginetest.CancelUnconfirmed())
}

// waitTimer waits for the engine to be waiting on the clock for d, so advancing it fires that timer.
func waitTimer(t *testing.T, h *enginetest.Harness, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.Clock.WaitTimer(ctx, d); err != nil {
		t.Fatalf("no timer of %v: %v", d, err)
	}
}

func TestParticipantsHoldAgeAdvances(t *testing.T) {
	h := holdHarness(t, enginetest.RosterTOML)
	r := startPartsRig(t, h)
	r.typeLine("question")
	turnIn(t, r, "reviewers_dispatched")
	waitTimer(t, h, 300*time.Second)
	h.Clock.Advance(300 * time.Second)
	v := untilPartsView(t, r, "pools:", "default: concurrency 1, 1 held", "hold ")
	age := regexp.MustCompile(`hold \S+, age (\S+)`).FindStringSubmatch(v)
	if age == nil {
		t.Fatalf("no hold with an age:\n%s", v)
	}
	h.Clock.Advance(90 * time.Second)
	send(r, resizeMsg(80, 24))
	v2 := untilPartsView(t, r, ", age ")
	age2 := regexp.MustCompile(`hold \S+, age (\S+)`).FindStringSubmatch(v2)
	if age2 == nil || age2[1] == age[1] {
		t.Fatalf("the hold's age stayed %q: now %q\n%s", age[1], age2, v2)
	}
	wantParts(t, "view after the advance", v2, "1m30s")
}

const soloRoster = `
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
id = "spare"
model = "llama2"
route = "api"
endpoint = "local2"
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

[[endpoints]]
name = "local2"
base_url = "http://localhost:11435/v1"
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
required_reviewers = ["spare"]

[policy]
local_only = true
allowed_destinations = []
`

func TestParticipantsOrphanedHold(t *testing.T) {
	// The reviewer has a pool to itself, so the hold's group is its endpoint and model alone.
	at := strings.Index(enginetest.RosterTOML, `id = "reviewer"`)
	roster := enginetest.RosterTOML[:at] + strings.Replace(enginetest.RosterTOML[at:], `pool = "default"`, `pool = "rpool"`, 1) + "\n[pools.rpool]\nconcurrency = 1\n"
	h := holdHarness(t, roster)
	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "question"})
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateReviewersDispatched)
	waitTimer(t, h, 300*time.Second)
	h.Clock.Advance(300 * time.Second)
	h.WaitTurnState(reply.TurnID, engine.StateIncomplete)

	// Restart on a roster that no longer has the reviewer's endpoint and model.
	h.Crash()
	if err := writeFile(h.RosterPath, soloRoster); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(engine.Options{
		Store: h.Store, Hub: h.Hub, RosterPath: h.RosterPath, EngineVersion: enginetest.EngineVersion, Clock: h.Clock,
		NewAdapter: func(p config.Participant, _ config.EndpointProfile) (transport.Adapter, string, error) {
			return fake.New(nil, fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel}), enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	h.Engine = eng

	r := startPartsRig(t, h)
	v := untilPartsView(t, r, "orphaned holds:")
	re := regexp.MustCompile(`/release rpool (\S+)`)
	m := re.FindStringSubmatch(v)
	if m == nil {
		t.Fatalf("no release hint:\n%s", v)
	}
	wantParts(t, "orphaned hold", v, "pool rpool, attempt "+m[1], "local/llama3")
	if strings.Contains(v, "rpool: concurrency") {
		t.Errorf("the old pool is listed as a live pool:\n%s", v)
	}

	// On a short terminal the fitted view cuts the orphaned hold; PgDn reaches it.
	send(r, resizeMsg(80, 14))
	untilPartsView(t, r, "more lines (PgDn)")
	send(r, tea.KeyMsg{Type: tea.KeyPgDown}, tea.KeyMsg{Type: tea.KeyPgDown})
	v = untilPartsView(t, r, "/release rpool "+m[1])
	if n := len(strings.Split(v, "\n")); n > 14 {
		t.Errorf("view is %d lines at 14 rows", n)
	}
	send(r, tea.KeyMsg{Type: tea.KeyPgUp}, tea.KeyMsg{Type: tea.KeyPgUp}, tea.KeyMsg{Type: tea.KeyPgUp})
	untilPartsView(t, r, "more lines (PgDn)")

	// Paged to the bottom, a refresh that shortens the content keeps the body
	// filled from the last full page, not from the stale offset.
	send(r, tea.KeyMsg{Type: tea.KeyPgDown}, tea.KeyMsg{Type: tea.KeyPgDown}, tea.KeyMsg{Type: tea.KeyPgDown})
	untilPartsView(t, r, "/release rpool "+m[1])
	if _, err := h.Engine.Release(wire.ReleaseRequest{Pool: "rpool", AttemptID: &m[1], Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	r.until(t, "a filled view after the release", func(a *App) (string, bool) {
		v := a.View()
		full := strings.Split(a.participantsFull(a.width), "\n")
		page := strings.Join(full[max(len(full)-a.bodyHeight(), 0):], "\n")
		return v, !strings.Contains(v, "orphaned holds:") && strings.Contains(v, page)
	})
}

func writeFile(path, text string) error { return os.WriteFile(path, []byte(text), 0o600) }

func TestMarkedSameFamilyOnlyForReviewers(t *testing.T) {
	s := NewState()
	for id, model := range map[string]string{"a": "llama3.2:8b", "rev": "llama3.1:70b", "spare": "llama3:8b", "other": "qwen2.5"} {
		s.Participants[id] = ParticipantInfo{ID: id, ReportedModel: model}
	}
	s.Turns["t1"] = TurnInfo{ID: "t1", Author: "a", Reviewers: []string{"rev", "other"}}
	s.CurrentTurn = "t1"
	for id, want := range map[string]bool{"a": false, "rev": true, "spare": false, "other": false} {
		if got := markedSameFamily(s, s.Participants[id]); got != want {
			t.Errorf("markedSameFamily(%s) = %v, want %v", id, got, want)
		}
	}
}

func TestRenderParticipantsFitsHeight(t *testing.T) {
	s := NewState()
	for i := range 30 {
		id := fmt.Sprintf("p%02d", i)
		s.Participants[id] = ParticipantInfo{ID: id, State: "idle", Pool: "pool", ReportedModel: "m", EligibleRoles: []string{"reviewer"}}
	}
	s.Pools["pool"] = wire.Pool{Name: "pool", Concurrency: 1, Holds: []wire.PoolHold{{AttemptID: "att1", HeldSince: "2026-11-18T12:00:00Z"}}}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	for _, sel := range []string{"p00", "p17", "p29"} {
		out := renderParticipants(s, 80, 21, sel, now)
		if n := len(strings.Split(out, "\n")); n > 21 {
			t.Fatalf("selected %s: %d lines for a height of 21", sel, n)
		}
		wantParts(t, "view of "+sel, out, "> "+sel, "detail: "+sel, "hold att1")
	}
}

func TestParticipantsFitsBodyAt80x24(t *testing.T) {
	r := startPartsRig(t, rosterHarness(t))
	untilPartsView(t, r, "unprobed:no_record")
	send(r, downs(5)...)
	v := untilPartsView(t, r, "detail: r2")
	wantParts(t, "r2 view", v, "> r2", "pools:", "default: concurrency 1", "p2: concurrency 1", "spare: concurrency 1")
	if n := len(strings.Split(v, "\n")); n > 24 {
		t.Errorf("view is %d lines at 24 rows", n)
	}
}

func TestParticipantDetailShowsExternalSpendingControl(t *testing.T) {
	s := NewState()
	s.Participants["metered-no-ctrl"] = ParticipantInfo{
		ID:              "metered-no-ctrl",
		State:           "idle",
		DeclaredBilling: "metered",
		SpendingControl: "",
	}
	s.Participants["metered-external"] = ParticipantInfo{
		ID:              "metered-external",
		State:           "idle",
		DeclaredBilling: "metered",
		SpendingControl: "external",
	}
	s.Participants["flat"] = ParticipantInfo{
		ID:              "flat",
		State:           "idle",
		DeclaredBilling: "flat",
		SpendingControl: "external",
	}

	now := time.Now()

	detail1 := participantDetail(s, s.Participants["metered-no-ctrl"], now)
	line1 := strings.Join(detail1, "\n")
	if !strings.Contains(line1, "billing: metered") {
		t.Fatalf("metered-no-ctrl detail missing 'billing: metered':\n%s", line1)
	}
	if strings.Contains(line1, "external spending control") {
		t.Fatalf("metered-no-ctrl detail should not contain 'external spending control':\n%s", line1)
	}

	detail2 := participantDetail(s, s.Participants["metered-external"], now)
	line2 := strings.Join(detail2, "\n")
	if !strings.Contains(line2, "metered, external spending control") {
		t.Fatalf("metered-external detail missing 'metered, external spending control':\n%s", line2)
	}

	detail3 := participantDetail(s, s.Participants["flat"], now)
	line3 := strings.Join(detail3, "\n")
	if !strings.Contains(line3, "billing: flat") {
		t.Fatalf("flat detail missing 'billing: flat':\n%s", line3)
	}
	if strings.Contains(line3, "external spending control") {
		t.Fatalf("flat detail should not show spending control as 'metered, external spending control':\n%s", line3)
	}
}
