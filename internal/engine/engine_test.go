package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

var soloScript = map[string][]fake.Step{"author": {enginetest.Reply("the answer")}}

func soloReq(id string) wire.SubmitRequest {
	preset := "solo"
	return wire.SubmitRequest{SubmissionID: id, Input: "question", Preset: &preset}
}

func wantRefusal(t *testing.T, err error, code string) *wire.Error {
	t.Helper()
	var we *wire.Error
	if !errors.As(err, &we) {
		t.Fatalf("want *wire.Error %q, got %v", code, err)
	}
	if we.Code != code {
		t.Fatalf("want code %q, got %+v", code, we)
	}
	return we
}

func countEvents(h *enginetest.Harness, kind string) int {
	n := 0
	for _, ev := range h.Events() {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func TestStartHelloAndSecondStart(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	hello := h.Engine.Hello()
	if hello.Protocol != wire.ProtocolVersion || hello.EngineVersion != enginetest.EngineVersion {
		t.Fatalf("hello = %+v", hello)
	}

	var runOpened *enginetest.Event
	var started *enginetest.Event
	for _, ev := range h.Events() {
		if ev.Kind == wire.EventKindRunOpened {
			ev := ev
			runOpened = &ev
		}
		if ev.Kind == wire.EventKindEngineStarted {
			ev := ev
			started = &ev
		}
	}
	if runOpened == nil {
		t.Fatal("no run_opened event")
	}
	if started == nil {
		t.Fatal("no engine_started event")
	}
	if hello.RunFirstSeq != runOpened.Seq {
		t.Fatalf("RunFirstSeq %d != run_opened seq %d", hello.RunFirstSeq, runOpened.Seq)
	}
	body := started.Payload.(*wire.EvEngineStarted)
	var helloHash string
	if hello.ManifestHash != nil {
		helloHash = *hello.ManifestHash
	}
	if body.RunID != hello.RunID || body.ManifestHash != helloHash || body.RosterHash != hello.RosterHash {
		t.Fatalf("engine_started %+v does not match hello %+v", body, hello)
	}

	var runs int
	if err := h.DB.Conn().QueryRow(`SELECT COUNT(*) FROM runs WHERE kind = 'engine'`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("engine runs = %d, err %v", runs, err)
	}

	// A second start on the same journal with the same roster.
	second, err := engine.New(engine.Options{
		Store:         h.Store,
		Hub:           h.Hub,
		RosterPath:    h.RosterPath,
		EngineVersion: enginetest.EngineVersion,
		Clock:         h.Clock,
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			return h.Adapters[p.ID], enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	hello2 := second.Hello()
	if hello2.RosterHash != hello.RosterHash {
		t.Fatalf("roster hash changed: %s vs %s", hello.RosterHash, hello2.RosterHash)
	}
	if hello2.RunID == hello.RunID {
		t.Fatal("second start reused the run id")
	}
	if hello2.RunFirstSeq <= hello.RunFirstSeq {
		t.Fatalf("second RunFirstSeq %d not after first %d", hello2.RunFirstSeq, hello.RunFirstSeq)
	}
	events, err := h.Store.EventsSince(hello2.RunFirstSeq-1, 1)
	if err != nil || len(events) != 1 || events[0].Kind != wire.EventKindRunOpened {
		t.Fatalf("event at RunFirstSeq = %+v, err %v", events, err)
	}
}

func TestStartInvalidRoster(t *testing.T) {
	bad := strings.Replace(enginetest.RosterTOML, `default_preset = "review"`, `default_preset = "nope"`, 1)
	h := enginetest.New(t, bad, nil, enginetest.NoStart())
	err := h.Engine.Start()
	var re *engine.RosterError
	if !errors.As(err, &re) {
		t.Fatalf("Start err = %v, want *RosterError", err)
	}
	var runs int
	if err := h.DB.Conn().QueryRow(`SELECT COUNT(*) FROM runs WHERE kind = 'engine'`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("engine runs = %d, err %v", runs, err)
	}
	if n := countEvents(h, wire.EventKindEngineStarted); n != 0 {
		t.Fatalf("engine_started events = %d", n)
	}
}

func TestStartMissingRosterFile(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.NoStart())
	if err := os.Remove(h.RosterPath); err != nil {
		t.Fatal(err)
	}
	var re *engine.RosterError
	if err := h.Engine.Start(); !errors.As(err, &re) {
		t.Fatalf("Start err = %v, want *RosterError", err)
	}
}

func TestStatusUnprobed(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.SkipProbe("reviewer"))
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Participants) != 2 {
		t.Fatalf("participants = %d", len(st.Participants))
	}
	byID := map[string]wire.Participant{}
	for _, p := range st.Participants {
		byID[p.ID] = p
	}
	r := byID["reviewer"]
	if r.State != engine.ParticipantUnprobed || r.Detail == nil || *r.Detail != "no_record" {
		t.Fatalf("reviewer = %+v", r)
	}
	a := byID["author"]
	if a.State != engine.ParticipantIdle || a.ReportedModel != enginetest.DefaultReportedModel || a.Pool != "default" ||
		a.DeclaredBilling != "none" || len(a.EligibleRoles) != 1 || a.ProbeObservedAt == nil {
		t.Fatalf("author = %+v", a)
	}

	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvEngineStarted); ok {
			if len(p.Unprobed) != 1 || p.Unprobed[0] != "reviewer" {
				t.Fatalf("engine_started.unprobed = %v", p.Unprobed)
			}
		}
	}
}

func TestStatusShape(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	hello := h.Engine.Hello()
	if st.RunID != hello.RunID || st.RosterHash != hello.RosterHash || st.RosterPath != h.RosterPath ||
		st.DefaultPreset != "review" || st.DeadlinesProvisional || st.RosterFileChanged {
		t.Fatalf("status = %+v", st)
	}
	if len(st.Pools) != 1 || st.Pools[0].Name != "default" || st.Pools[0].Concurrency != 1 || len(st.Pools[0].Holds) != 0 {
		t.Fatalf("pools = %+v", st.Pools)
	}
	if len(st.OrphanedHolds) != 0 || len(st.TurnsOpen) != 0 {
		t.Fatalf("orphaned %v open %v", st.OrphanedHolds, st.TurnsOpen)
	}
	if len(st.QuickProfiles) != 1 {
		t.Fatalf("quick profiles = %+v", st.QuickProfiles)
	}
	q := st.QuickProfiles[0]
	if q.Name != "fast" || q.Preset != "solo" || q.DefinitionSHA256 == "" || q.Budgets == nil ||
		q.Budgets.AuthorMaxOutputTokens == nil || *q.Budgets.AuthorMaxOutputTokens != 2048 ||
		q.Budgets.ReviewerMaxOutputTokens == nil || *q.Budgets.ReviewerMaxOutputTokens != 2048 {
		t.Fatalf("quick profile = %+v", q)
	}

	if err := os.WriteFile(h.RosterPath, []byte(enginetest.RosterTOML+"\n# edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err = h.Engine.Status()
	if err != nil || !st.RosterFileChanged {
		t.Fatalf("roster_file_changed = %v, err %v", st.RosterFileChanged, err)
	}
}

func TestSoloHappyPath(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	if reply.Preset != "solo" || reply.QuickProfile != nil || reply.EstimatedTokens <= 0 {
		t.Fatalf("reply = %+v", reply)
	}
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)

	var seen []string
	for _, ev := range h.Events() {
		if ev.TurnID != reply.TurnID {
			continue
		}
		if p, ok := ev.Payload.(*wire.EvTurnState); ok {
			seen = append(seen, ev.Kind+":"+p.State)
			if p.State == engine.StateSoloComplete && (p.Reason == nil || *p.Reason != "solo_ok") {
				t.Fatalf("reason = %v", p.Reason)
			}
			continue
		}
		seen = append(seen, ev.Kind)
	}
	want := []string{
		wire.EventKindTurnSubmitted,
		wire.EventKindTurnState + ":" + engine.StateSubmitted,
		wire.EventKindMessageAppended,
		wire.EventKindAttemptBegun,
		wire.EventKindTurnState + ":" + engine.StateAuthorDispatched,
		wire.EventKindAttemptCompleted,
		wire.EventKindTurnState + ":" + engine.StateSoloComplete,
	}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("events\n got %v\nwant %v", seen, want)
	}

	calls := h.Adapters["author"].Calls()
	if len(calls) != 1 || calls[0].Request.MaxOutputTokens != 4096 {
		t.Fatalf("calls = %+v", calls)
	}

	row, err := h.Store.GetTurn(reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if row.TerminalState != engine.StateSoloComplete || row.TerminalReason != "solo_ok" || row.Epoch != 1 {
		t.Fatalf("turn row = %+v", row)
	}
}

func attemptRow(t *testing.T, h *enginetest.Harness, turnID string) *journal.AttemptRow {
	t.Helper()
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvAttemptBegun); ok && ev.TurnID == turnID {
			var row *journal.AttemptRow
			err := h.Store.Tx(func(tx *journal.Tx) error {
				var err error
				row, err = tx.GetAttempt(p.AttemptID)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return row
		}
	}
	t.Fatal("no attempt_begun event")
	return nil
}

func lastTurnState(t *testing.T, h *enginetest.Harness, turnID string) wire.EvTurnState {
	t.Helper()
	var last wire.EvTurnState
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvTurnState); ok && p.TurnID == turnID {
			last = *p
		}
	}
	return last
}

func TestSoloFailures(t *testing.T) {
	cases := []struct {
		name   string
		step   fake.Step
		reason string
		gate   bool
	}{
		{"refused", fake.Step{Kind: fake.StepKindReply, Text: "no", Status: transport.StatusRefused, TransportStatus: transport.TransportStatusOK}, "author_failed:refused", true},
		{"truncated", fake.Step{Kind: fake.StepKindReply, Text: "cut", Status: transport.StatusTruncated, TransportStatus: transport.TransportStatusOK}, "author_failed:truncated", true},
		{"identity mismatch", fake.Step{Kind: fake.StepKindReply, Text: "x", Status: transport.StatusComplete, TransportStatus: transport.TransportStatusOK, ReportedModel: "other-model"}, "author_failed:gate:identity mismatch", true},
		{"transport status", fake.Step{Kind: fake.StepKindReply, Status: transport.StatusError, TransportStatus: transport.TransportStatusUnauthorized}, "author_failed:unauthorized", true},
		{"go error", fake.Step{Kind: fake.StepKindError, Err: errors.New("boom")}, "author_failed:adapter_error:boom", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {tc.step}})
			reply, err := h.Engine.Submit(soloReq("s1"))
			if err != nil {
				t.Fatal(err)
			}
			h.WaitTurnState(reply.TurnID, engine.StateIncomplete)
			last := lastTurnState(t, h, reply.TurnID)
			if last.State != engine.StateIncomplete || last.Reason == nil || *last.Reason != tc.reason {
				t.Fatalf("last turn_state = %+v", last)
			}
			row := attemptRow(t, h, reply.TurnID)
			if tc.gate && (row.GateError == nil || *row.GateError == "") {
				t.Fatalf("attempt row has no gate error: %+v", row)
			}
			if row.State != "completed" {
				t.Fatalf("attempt state = %q", row.State)
			}
			turn, err := h.Store.GetTurn(reply.TurnID)
			if err != nil || turn.Epoch != 1 || turn.TerminalReason != tc.reason {
				t.Fatalf("turn = %+v, err %v", turn, err)
			}
			// The engine is free again.
			if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
				t.Fatalf("submit after failure: %v", err)
			}
		})
	}
}

func TestIdentityMismatchGateError(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {
		{Kind: fake.StepKindReply, Text: "x", Status: transport.StatusComplete, TransportStatus: transport.TransportStatusOK, ReportedModel: "other-model"},
	}})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateIncomplete)
	row := attemptRow(t, h, reply.TurnID)
	if row.GateError == nil || !strings.Contains(*row.GateError, transport.ErrIdentityMismatch.Error()) {
		t.Fatalf("gate error = %v", row.GateError)
	}
}

func TestSoloUsageStored(t *testing.T) {
	step := enginetest.Reply("ok")
	step.Usage = &transport.Usage{Prompt: 11, Completion: 7}
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {step}})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)
	row := attemptRow(t, h, reply.TurnID)
	if row.UsagePrompt == nil || *row.UsagePrompt != 11 || row.UsageCompletion == nil || *row.UsageCompletion != 7 {
		t.Fatalf("usage = %v %v", row.UsagePrompt, row.UsageCompletion)
	}
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvAttemptCompleted); ok {
			if p.Usage == nil || *p.Usage.PromptTokens != 11 || *p.Usage.CompletionTokens != 7 {
				t.Fatalf("event usage = %+v", p.Usage)
			}
		}
	}
}

func TestOutputBoundClamp(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript, enginetest.WithOutputBound("author", 1000))
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)
	if got := h.Adapters["author"].Calls()[0].Request.MaxOutputTokens; got != 1000 {
		t.Fatalf("MaxOutputTokens = %d, want 1000", got)
	}
	if row := attemptRow(t, h, reply.TurnID); row.MaxOutputTokens == nil || *row.MaxOutputTokens != 1000 {
		t.Fatalf("attempt max output = %v", row.MaxOutputTokens)
	}
}

func TestQuickProfileResolution(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	quick := "fast"
	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q", QuickProfile: &quick})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Preset != "solo" || reply.QuickProfile == nil || *reply.QuickProfile != "fast" {
		t.Fatalf("reply = %+v", reply)
	}
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)
	if got := h.Adapters["author"].Calls()[0].Request.MaxOutputTokens; got != 2048 {
		t.Fatalf("MaxOutputTokens = %d, want the quick profile's 2048", got)
	}
}

func TestAttemptDeadline(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)
	want := h.Clock.Now().Add(300 * time.Second)
	if got := h.Adapters["author"].Calls()[0].Request.Deadline; !got.Equal(want) {
		t.Fatalf("deadline = %v, want %v", got, want)
	}
	row := attemptRow(t, h, reply.TurnID)
	if row.Deadline == nil || !row.Deadline.Equal(want) {
		t.Fatalf("attempt row deadline = %v, want %v", row.Deadline, want)
	}
}

func TestSubmitIdempotent(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	release := h.Hold("author")
	first, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(first.TurnID, engine.StateAuthorDispatched)

	// Same id and content while the turn is open.
	again, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.TurnID != first.TurnID || again.Preset != first.Preset {
		t.Fatalf("retry reply %+v != %+v", again, first)
	}

	// Same id, different input.
	changed := soloReq("s1")
	changed.Input = "different"
	we := wantRefusal(t, func() error { _, err := h.Engine.Submit(changed); return err }(), wire.ErrorCodeSubmissionConflict)
	if we.TurnID == nil || *we.TurnID != first.TurnID {
		t.Fatalf("conflict turn id = %v", we.TurnID)
	}

	// A different id is busy, naming the open turn.
	_, err = h.Engine.Submit(soloReq("s2"))
	we = wantRefusal(t, err, wire.ErrorCodeBusy)
	if we.TurnID == nil || *we.TurnID != first.TurnID {
		t.Fatalf("busy turn id = %v", we.TurnID)
	}

	release()
	h.WaitTurnState(first.TurnID, engine.StateSoloComplete)

	// Still the same turn once it is over, and only one turn exists.
	done, err := h.Engine.Submit(soloReq("s1"))
	if err != nil || done.TurnID != first.TurnID {
		t.Fatalf("retry after completion: %+v, %v", done, err)
	}
	if n := countEvents(h, wire.EventKindTurnSubmitted); n != 1 {
		t.Fatalf("turn_submitted events = %d", n)
	}
	var turns int
	if err := h.DB.Conn().QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turns); err != nil || turns != 1 {
		t.Fatalf("turns = %d, err %v", turns, err)
	}
}

func TestStatusShowsRunningAndOpenTurn(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	release := h.Hold("author")
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateAuthorDispatched)
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.TurnsOpen) != 1 || st.TurnsOpen[0] != reply.TurnID {
		t.Fatalf("turns_open = %v", st.TurnsOpen)
	}
	for _, p := range st.Participants {
		want := engine.ParticipantIdle
		if p.ID == "author" {
			want = engine.ParticipantRunning
		}
		if p.State != want {
			t.Fatalf("participant %s state = %s, want %s", p.ID, p.State, want)
		}
	}
	release()
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)
	st, err = h.Engine.Status()
	if err != nil || len(st.TurnsOpen) != 0 || st.Participants[0].State != engine.ParticipantIdle {
		t.Fatalf("status after = %+v, err %v", st, err)
	}
}

func TestRefusalsWriteNothingAndFreeTheID(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	before := len(h.Events())

	if _, err := h.Engine.Submit(wire.SubmitRequest{Input: "q"}); err == nil {
		t.Fatal("empty submission_id accepted")
	}
	unknown := "nope"
	_, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q", QuickProfile: &unknown})
	we := wantRefusal(t, err, wire.ErrorCodeQuickProfileUnknown)
	if we.QuickProfile == nil || *we.QuickProfile != "nope" {
		t.Fatalf("quick_profile = %v", we.QuickProfile)
	}
	_, err = h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q", Preset: &unknown})
	we = wantRefusal(t, err, wire.ErrorCodePresetInvalid)
	if we.Detail == nil {
		t.Fatal("preset_invalid has no detail")
	}

	if _, found, err := h.Store.FindSubmission("s1"); err != nil || found {
		t.Fatalf("refused submission recorded: found=%v err=%v", found, err)
	}
	if got := len(h.Events()); got != before {
		t.Fatalf("refusals emitted %d events", got-before)
	}

	// The same id is free for a valid submission.
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatalf("reuse of refused id: %v", err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)
}

func TestRefusalOrder(t *testing.T) {
	// quick_profile_unknown comes before preset_invalid.
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	bad := "nope"
	_, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q", QuickProfile: &bad, Preset: &bad})
	_ = wantRefusal(t, err, wire.ErrorCodeQuickProfileUnknown)

	// busy comes before everything after it.
	h2 := enginetest.New(t, enginetest.RosterTOML, soloScript)
	release := h2.Hold("author")
	reply, err := h2.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	h2.WaitTurnState(reply.TurnID, engine.StateAuthorDispatched)
	_, err = h2.Engine.Submit(wire.SubmitRequest{SubmissionID: "s2", Input: "q", QuickProfile: &bad})
	_ = wantRefusal(t, err, wire.ErrorCodeBusy)
	release()
}

func TestIneligibleRequiredReviewer(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.WithEligibleRequiredReviewer("reviewer", transport.TriNo))
	_, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q"})
	we := wantRefusal(t, err, wire.ErrorCodePresetInvalid)
	if we.Detail == nil || *we.Detail != "ineligible_required_reviewer:reviewer" {
		t.Fatalf("detail = %v", we.Detail)
	}
	// Solo does not involve the reviewer.
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("solo refused: %v", err)
	}
}

func TestUnknownRequiredReviewerNotAccepted(t *testing.T) {
	rosterWithoutAccept := strings.Replace(enginetest.RosterTOML, `accept_unknown_truncation = true`, `accept_unknown_truncation = false`, 1)
	h := enginetest.New(t, rosterWithoutAccept, nil, enginetest.WithEligibleRequiredReviewer("reviewer", transport.TriUnknown))
	_, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q"})
	we := wantRefusal(t, err, wire.ErrorCodePresetInvalid)
	if we.Detail == nil || *we.Detail != "unknown_required_reviewer_not_accepted:reviewer" {
		t.Fatalf("detail = %v", we.Detail)
	}
}

func TestUnknownRequiredReviewerAccepted(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer")},
		"reviewer": {enginetest.Reply("approved")},
	}, enginetest.WithEligibleRequiredReviewer("reviewer", transport.TriUnknown))
	reply, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q"})
	if err != nil {
		t.Fatalf("Submit refused: %v", err)
	}
	if reply.TurnID == "" {
		t.Fatal("no turn id returned")
	}
}

func TestUnprobedRefusal(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.SkipProbe("reviewer"))
	_, err := h.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q"})
	we := wantRefusal(t, err, wire.ErrorCodeUnprobed)
	if we.Participant == nil || *we.Participant != "reviewer" || we.Detail == nil || *we.Detail != "no_record" {
		t.Fatalf("refusal = %+v", we)
	}

	// The author comes first.
	h2 := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.SkipProbe("reviewer"), enginetest.SkipProbe("author"))
	_, err = h2.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: "q"})
	we = wantRefusal(t, err, wire.ErrorCodeUnprobed)
	if we.Participant == nil || *we.Participant != "author" {
		t.Fatalf("participant = %v", we.Participant)
	}
}

func TestProvisional(t *testing.T) {
	roster := strings.Replace(enginetest.RosterTOML, "provisional = false", "provisional = true", 1)
	h := enginetest.New(t, roster, soloScript)
	st, err := h.Engine.Status()
	if err != nil || !st.DeadlinesProvisional {
		t.Fatalf("deadlines_provisional = %v, err %v", st.DeadlinesProvisional, err)
	}
	_, err = h.Engine.Submit(soloReq("s1"))
	_ = wantRefusal(t, err, wire.ErrorCodeProvisional)

	req := soloReq("s1")
	allow := true
	req.AllowProvisional = &allow
	reply, err := h.Engine.Submit(req)
	if err != nil {
		t.Fatalf("with allow_provisional: %v", err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)
}

type unmarshalable struct{ C chan int }

func TestSubmitTxIsAtomic(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	h.Engine.SetPayloadHook(func(kind string, payload any) any {
		if p, ok := payload.(wire.EvTurnState); ok && p.State == engine.StateSubmitted {
			return unmarshalable{C: make(chan int)}
		}
		return payload
	})
	before := len(h.Events())
	if _, err := h.Engine.Submit(soloReq("s1")); err == nil {
		t.Fatal("submit succeeded despite failing transaction")
	}
	var turns int
	if err := h.DB.Conn().QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turns); err != nil || turns != 0 {
		t.Fatalf("turn row survived the failed transaction: %d, err %v", turns, err)
	}
	if _, found, _ := h.Store.FindSubmission("s1"); found {
		t.Fatal("submission recorded")
	}
	st, err := h.Engine.Status()
	if err != nil || len(st.TurnsOpen) != 0 {
		t.Fatalf("status = %+v, err %v", st, err)
	}
	// Events that did commit must not include the turn_submitted from the
	// failed transaction.
	h.Engine.SetPayloadHook(nil)
	if n := countEvents(h, wire.EventKindTurnSubmitted); n != 0 || len(h.Events()) != before {
		t.Fatalf("events leaked: %d turn_submitted, %d new", n, len(h.Events())-before)
	}
}

func TestDispatchTxIsAtomic(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	h.Engine.SetPayloadHook(func(kind string, payload any) any {
		if kind == wire.EventKindTurnState {
			if p, ok := payload.(wire.EvTurnState); ok && p.State == engine.StateAuthorDispatched {
				return unmarshalable{C: make(chan int)}
			}
		}
		return payload
	})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	// The dispatch transaction failed after writing the message, the attempt
	// and two events; none of them may remain, and the turn is ended.
	h.WaitTurnState(reply.TurnID, engine.StateIncomplete)
	for _, table := range []string{"messages", "attempts"} {
		var n int
		if err := h.DB.Conn().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows = %d, err %v", table, n, err)
		}
	}
	for _, kind := range []string{wire.EventKindMessageAppended, wire.EventKindAttemptBegun, wire.EventKindParticipantState} {
		if n := countEvents(h, kind); n != 0 {
			t.Fatalf("%d %s events leaked", n, kind)
		}
	}
	if calls := h.Adapters["author"].Calls(); len(calls) != 0 {
		t.Fatalf("adapter called %d times", len(calls))
	}
	if _, err := h.Engine.Submit(soloReq("s2")); err != nil {
		t.Fatalf("engine stayed busy: %v", err)
	}
}

func TestNewRequiresOptions(t *testing.T) {
	if _, err := engine.New(engine.Options{}); err == nil {
		t.Fatal("New accepted empty options")
	}
	dir := t.TempDir()
	db, err := journal.Open(filepath.Join(dir, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	store := journal.NewStore(db)
	if _, err := engine.New(engine.Options{Store: store, Hub: hub.New(store, 0), RosterPath: "r.toml"}); err == nil {
		t.Fatal("New accepted a missing NewAdapter")
	}
}

// A crash between run_opened and the manifest leaves a probing run with no
// manifest. The next start closes it and opens a new run; it never reuses the
// stale run or sends a request on its behalf.
func TestStartAbandonsUnfinishedProbingRun(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	first := h.Engine.Hello()

	var staleID string
	err := h.Store.Tx(func(tx *journal.Tx) error {
		var err error
		if staleID, err = tx.AllocateEngineRun(); err != nil {
			return err
		}
		_, err = tx.Emit("", wire.EventKindRunOpened, []byte(`{"run_id":"`+staleID+`","roster_hash":"x"}`))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	staleSeq, found, err := h.Store.RunOpenedSeq(staleID)
	if err != nil || !found {
		t.Fatalf("stale run_opened: found %v, err %v", found, err)
	}
	if run, err := h.Store.GetRun(staleID); err != nil || run.State != "probing" || run.ManifestHash != "" || run.Manifest != nil {
		t.Fatalf("stale run = %+v, err %v", run, err)
	}

	second, err := engine.New(engine.Options{
		Store:         h.Store,
		Hub:           h.Hub,
		RosterPath:    h.RosterPath,
		EngineVersion: enginetest.EngineVersion,
		Clock:         h.Clock,
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			return h.Adapters[p.ID], enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Start(); err != nil {
		t.Fatalf("restart over a probing run: %v", err)
	}
	hello := second.Hello()
	if hello.RunID == staleID || hello.RunID == first.RunID {
		t.Fatalf("restart reused run %s", hello.RunID)
	}
	if hello.State != "started" || hello.ManifestHash == nil {
		t.Fatalf("hello = %+v, want started with a manifest hash", hello)
	}
	if hello.RunFirstSeq <= staleSeq {
		t.Fatalf("run_first_seq %d not after the stale run_opened %d", hello.RunFirstSeq, staleSeq)
	}
	events, err := h.Store.EventsSince(hello.RunFirstSeq-1, 1)
	if err != nil || len(events) != 1 || events[0].Kind != wire.EventKindRunOpened {
		t.Fatalf("event at run_first_seq = %+v, err %v", events, err)
	}
	if run, err := h.Store.GetRun(staleID); err != nil || run.State != "abandoned" || run.Manifest != nil {
		t.Fatalf("stale run after restart = %+v, err %v", run, err)
	}
	var attempts int
	if err := h.DB.Conn().QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("attempts = %d, err %v; restart must not replay requests", attempts, err)
	}
}
