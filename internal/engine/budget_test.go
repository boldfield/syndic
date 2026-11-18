package engine_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

// The constants the default harness gives every participant: the seeded
// calibration of 4.0 at the default safety of 0.75, tokens_per_byte_max 1.0,
// the default overheads, margin and output bounds.
const (
	testBytesPerToken = 3.0
	testTokensPerByte = 1.0
	testInput         = "what is the answer?"
)

var testOverheads = budget.Overheads{MessageTokens: 8, RequestTokens: 16}

type recordView struct {
	Projected int  `json:"projected"`
	Reserved  int  `json:"reserved"`
	Charged   int  `json:"charged"`
	Limit     int  `json:"limit"`
	Exhausted bool `json:"exhausted"`
}

func budgetOf(t *testing.T, h *enginetest.Harness, turnID string) recordView {
	t.Helper()
	row, err := h.Store.GetTurn(turnID)
	if err != nil {
		t.Fatal(err)
	}
	var rec recordView
	if err := json.Unmarshal(row.BudgetJSON, &rec); err != nil {
		t.Fatalf("budget_json %q: %v", row.BudgetJSON, err)
	}
	return rec
}

func withBudgets(toml string, lines ...string) string {
	return toml + "\n[budgets]\n" + strings.Join(lines, "\n") + "\n"
}

func estimate(msgs []transport.Message) int {
	return budget.EstimateTokens(msgs, testBytesPerToken, testOverheads)
}

// wantBudgetError checks a submit refusal.
func wantBudgetError(t *testing.T, err error, kind string) *wire.Error {
	t.Helper()
	we := wantRefusal(t, err, wire.ErrorCodeBudget)
	if we.Budget == nil || *we.Budget != kind {
		t.Fatalf("budget error = %+v, want %s", we, kind)
	}
	return we
}

func submitInput(t *testing.T, h *enginetest.Harness, id, input string) (wire.SubmitReply, error) {
	t.Helper()
	return h.Engine.Submit(wire.SubmitRequest{SubmissionID: id, Input: input})
}

func reviewerCalls(h *enginetest.Harness, id string) []fake.CompleteCall {
	return h.Adapters[id].Calls()
}

func TestBudgetProjectionMatchesBuilders(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(approveStep()))
	reply, err := submitInput(t, h, "s1", testInput)
	if err != nil {
		t.Fatal(err)
	}

	const authorOut, reviewerOut, deliveryOut = 4096, 2048, 64
	zeros64, zeros32 := strings.Repeat("0", 64), strings.Repeat("0", 32)
	candidate := strings.Repeat("x", budget.StandInBytes(authorOut, testBytesPerToken))
	reviewerOutput := strings.Repeat("x", budget.StandInBytes(reviewerOut, testBytesPerToken))
	in := prompt.ReviewInput{Request: testInput, Candidate: candidate, CandidateHash: zeros64, AttemptID: zeros32, Delimiter: zeros32}
	review, err := prompt.Reviewer(in)
	if err != nil {
		t.Fatal(err)
	}
	repair, err := prompt.Repair(in, reviewerOutput, zeros32)
	if err != nil {
		t.Fatal(err)
	}
	delivery := prompt.Delivery(prompt.DeliveryInput{
		TerminalState:   engine.StateIncomplete,
		TerminalReason:  "block:" + zeros32,
		CandidateHash:   zeros64,
		ReviewerOutputs: []prompt.ReviewerOutput{{Participant: "reviewer", Output: reviewerOutput}},
	})
	want := estimate(prompt.Author(testInput)) + authorOut +
		estimate(review) + reviewerOut +
		estimate(repair) + reviewerOut +
		2*(estimate(delivery)+deliveryOut)

	if reply.EstimatedTokens != int64(want) {
		t.Fatalf("estimated_tokens = %d, want %d", reply.EstimatedTokens, want)
	}
	var submitted *wire.EvTurnSubmitted
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvTurnSubmitted); ok {
			submitted = p
		}
	}
	if submitted == nil || submitted.EstimatedTokens != int64(want) {
		t.Fatalf("turn_submitted = %+v, want estimated_tokens %d", submitted, want)
	}
	rec := budgetOf(t, h, reply.TurnID)
	if rec.Projected != want || rec.Limit != 250000 {
		t.Fatalf("budget record = %+v", rec)
	}
	again, err := submitInput(t, h, "s1", testInput)
	if err != nil || again.EstimatedTokens != reply.EstimatedTokens {
		t.Fatalf("resubmit = %+v, %v", again, err)
	}
}

func TestBudgetMaxOutputTokensPerPhase(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, reviewScripts(approveStep()),
		enginetest.WithOutputBound("reviewer", 100))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
	if calls := h.Adapters["author"].Calls(); len(calls) != 1 || calls[0].Request.MaxOutputTokens != 4096 {
		t.Fatalf("author calls = %+v", calls)
	}
	calls := reviewerCalls(h, "reviewer")
	if len(calls) != 1 || calls[0].Request.MaxOutputTokens != 100 {
		t.Fatalf("reviewer calls = %+v, want output clamped to the bound 100", calls)
	}
	row := getAttempt(t, h, calls[0].Request.Envelope.AttemptID)
	if row.MaxOutputTokens == nil || *row.MaxOutputTokens != 100 || row.EstimatedPromptTokens == nil ||
		*row.EstimatedPromptTokens != estimate(calls[0].Request.Messages) {
		t.Fatalf("attempt row = %+v", row)
	}
	for _, ev := range h.Events() {
		if p, ok := ev.Payload.(*wire.EvAttemptBegun); ok && p.Participant == "reviewer" {
			if p.MaxOutputTokens != 100 || p.EstimatedPromptTokens != int64(estimate(calls[0].Request.Messages)) {
				t.Fatalf("attempt_begun = %+v", p)
			}
		}
	}
}

func TestBudgetContextUnknown(t *testing.T) {
	missing := func(t *testing.T, we *wire.Error, participant, what string) {
		t.Helper()
		if we.Participant == nil || *we.Participant != participant || we.Missing == nil || *we.Missing != what {
			t.Fatalf("budget error = %+v, want %s missing for %s", we, what, participant)
		}
	}
	t.Run("context bound", func(t *testing.T) {
		h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.WithContextBound("author", 0))
		_, err := submitReview2(h, "s1")
		missing(t, wantBudgetError(t, err, "context_unknown"), "author", "context_bound")
	})
	t.Run("tokens per byte max", func(t *testing.T) {
		toml := strings.Replace(enginetest.RosterTOML, "tokens_per_byte_max = 1.0\n", "", 1)
		h := enginetest.New(t, toml, nil)
		_, err := submitReview2(h, "s1")
		missing(t, wantBudgetError(t, err, "context_unknown"), "author", "tokens_per_byte_max")
	})
	t.Run("bytes per token", func(t *testing.T) {
		h := enginetest.New(t, enginetest.RosterTOML, nil, enginetest.WithCalibration("reviewer", 0))
		_, err := submitReview2(h, "s1")
		missing(t, wantBudgetError(t, err, "context_unknown"), "reviewer", "bytes_per_token")
	})
	t.Run("roster declarations pass", func(t *testing.T) {
		toml := strings.Replace(enginetest.RosterTOML, `eligible_roles = ["author"]`,
			"eligible_roles = [\"author\"]\ncontext_tokens = 100000\nbytes_per_token = 2.5", 1)
		h := enginetest.New(t, toml, soloScript,
			enginetest.WithContextBound("author", 0), enginetest.WithCalibration("author", 0))
		reply, err := h.Engine.Submit(soloReq("s1"))
		if err != nil {
			t.Fatal(err)
		}
		h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)
		st, err := h.Engine.Status()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range st.Participants {
			switch p.ID {
			case "author":
				if p.ContextSource != "roster" || p.BytesPerTokenSource != "roster" || p.ContextBound == nil || *p.ContextBound != 100000 {
					t.Fatalf("author status = %+v", p)
				}
			case "reviewer":
				if p.ContextSource != "probe" || p.BytesPerTokenSource != "probe" {
					t.Fatalf("reviewer status = %+v", p)
				}
			}
		}
	})
}

func submitReview2(h *enginetest.Harness, id string) (wire.SubmitReply, error) {
	return h.Engine.Submit(wire.SubmitRequest{SubmissionID: id, Input: testInput})
}

func TestBudgetCalibrationUsesSafetyFactor(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, soloScript)
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Participants {
		if p.BytesPerToken == nil || *p.BytesPerToken != 3.0 {
			t.Fatalf("%s bytes_per_token = %v, want 4.0 x 0.75", p.ID, p.BytesPerToken)
		}
	}
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)
	calls := h.Adapters["author"].Calls()
	row := getAttempt(t, h, calls[0].Request.Envelope.AttemptID)
	want := budget.EstimateTokens(calls[0].Request.Messages, 3.0, testOverheads)
	if row.EstimatedPromptTokens == nil || *row.EstimatedPromptTokens != want {
		t.Fatalf("estimated prompt tokens = %v, want %d", row.EstimatedPromptTokens, want)
	}
}

func TestBudgetMeteredEndpointRefused(t *testing.T) {
	toml := strings.Replace(enginetest.RosterTOML, `declared_billing = "none"`, `declared_billing = "metered"`, 1)
	h := enginetest.New(t, toml, nil)
	_, err := submitReview2(h, "s1")
	we := wantBudgetError(t, err, "spending_ceiling")
	if we.Participant == nil || *we.Participant != "author" {
		t.Fatalf("error = %+v", we)
	}
}

func TestBudgetTurnLimitRefusesAndPasses(t *testing.T) {
	h := enginetest.New(t, withBudgets(enginetest.RosterTOML, "turn_max_estimated_tokens = 1000"), nil)
	_, err := submitReview2(h, "s1")
	we := wantBudgetError(t, err, "turn_max_estimated_tokens")
	if we.Limit == nil || *we.Limit != 1000 || we.Estimated == nil || *we.Estimated <= 1000 {
		t.Fatalf("error = %+v", we)
	}
	if rows := countEvents(h, wire.EventKindTurnSubmitted); rows != 0 {
		t.Fatalf("a refused submission wrote %d turn_submitted events", rows)
	}

	h = enginetest.New(t, withBudgets(enginetest.RosterTOML, "turn_max_estimated_tokens = 100000"), reviewScripts(approveStep()))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateApproved, "all_approved")
}

func TestBudgetLargerRosterProjectsLarger(t *testing.T) {
	scripts := func(n int) map[string][]fake.Step {
		m := map[string][]fake.Step{"author": {enginetest.Reply(authorText)}}
		for i := 1; i <= n; i++ {
			m["r"+string(rune('0'+i))] = []fake.Step{approveStep()}
		}
		return m
	}
	three := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts(2))
	five := enginetest.New(t, reviewRoster("author", "r1", "r2", "r3", "r4"), scripts(4))
	a, err := submitReview2(three, "s1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := submitReview2(five, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if b.EstimatedTokens <= a.EstimatedTokens {
		t.Fatalf("five participants project %d, three project %d", b.EstimatedTokens, a.EstimatedTokens)
	}
}

func TestBudgetAuthorInputOverContext(t *testing.T) {
	h := enginetest.New(t, enginetest.RosterTOML, nil)
	_, err := submitInput(t, h, "s1", strings.Repeat("a", 120000))
	we := wantBudgetError(t, err, "context")
	if we.Participant == nil || *we.Participant != "author" || we.Limit == nil || *we.Limit != 115200 ||
		we.Admission == nil || *we.Admission <= *we.Limit {
		t.Fatalf("error = %+v", we)
	}
}

func TestBudgetReviewerContextRefusedAtDispatch(t *testing.T) {
	huge := strings.Repeat("a", 30000)
	scripts := map[string][]fake.Step{
		"author":   {enginetest.Reply(huge)},
		"reviewer": {approveStep()},
	}
	h := enginetest.New(t, enginetest.RosterTOML, scripts, enginetest.WithContextBound("reviewer", 30000))
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "budget:context:reviewer")

	if n := len(reviewerCalls(h, "reviewer")); n != 0 {
		t.Fatalf("reviewer adapter called %d times", n)
	}
	if n := len(attemptsOf(h, turnID, "reviewer")); n != 0 {
		t.Fatalf("%d attempts begun for the refused reviewer", n)
	}
	var ev *wire.EvBudget
	order := []string{}
	for _, e := range h.Events() {
		if p, ok := e.Payload.(*wire.EvBudget); ok {
			ev = p
		}
		if e.TurnID == turnID && (e.Kind == wire.EventKindBudget || e.Kind == wire.EventKindTurnState) {
			order = append(order, e.Kind)
		}
	}
	if ev == nil || ev.Budget != "context" || ev.Participant == nil || *ev.Participant != "reviewer" ||
		ev.Admission == nil || *ev.Admission <= ev.Limit || ev.Limit != 27000 {
		t.Fatalf("budget event = %+v", ev)
	}
	if got := strings.Join(order, ","); !strings.HasSuffix(got, "budget,turn_state") {
		t.Fatalf("event order = %s", got)
	}
	row, err := h.Store.GetTurn(turnID)
	if err != nil || row.CandidateID == "" {
		t.Fatalf("turn = %+v, %v", row, err)
	}
	cand, err := h.Store.GetCandidate(row.CandidateID)
	if err != nil || cand.ByteLen != int64(len(huge)) {
		t.Fatalf("candidate = %+v, err %v; want all %d bytes stored", cand, err, len(huge))
	}
}

// projectionOf runs a throwaway turn to learn what the roster projects.
func projectionOf(t *testing.T, toml string, scripts map[string][]fake.Step) int {
	t.Helper()
	h := enginetest.New(t, toml, scripts)
	reply, err := submitReview2(h, "p")
	if err != nil {
		t.Fatal(err)
	}
	return int(reply.EstimatedTokens)
}

func TestBudgetTurnLimitRefusedAtDispatch(t *testing.T) {
	big := strings.Repeat("a", 60000)
	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{"author": {enginetest.Reply(big)}, "reviewer": {approveStep()}}
	}
	limit := projectionOf(t, enginetest.RosterTOML, scripts())
	// The reviewer's real prompt carries 60000 bytes where the projection
	// assumed 12288, so its estimate overruns the limit although its
	// admission bound (about 61000 of 115200) fits the context.
	h := enginetest.New(t, withBudgets(enginetest.RosterTOML, "turn_max_estimated_tokens = "+strconv.Itoa(limit)), scripts())
	turnID := submitReview(t, h, "s1")
	wantEnd(t, h, turnID, engine.StateIncomplete, "budget:turn")
	var ev *wire.EvBudget
	for _, e := range h.Events() {
		if p, ok := e.Payload.(*wire.EvBudget); ok {
			ev = p
		}
	}
	if ev == nil || ev.Budget != "turn_max_estimated_tokens" || ev.Estimated == nil || *ev.Estimated <= int64(limit) || ev.Limit != int64(limit) {
		t.Fatalf("budget event = %+v", ev)
	}
	if n := len(reviewerCalls(h, "reviewer")); n != 0 {
		t.Fatalf("reviewer adapter called %d times", n)
	}
}

func TestBudgetEstimateExceeded(t *testing.T) {
	step := enginetest.Reply("the answer")
	step.Usage = &transport.Usage{Prompt: 5000, Completion: 10}
	h := enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{"author": {step}})
	reply, err := h.Engine.Submit(soloReq("s1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateSoloComplete)

	calls := h.Adapters["author"].Calls()
	row := getAttempt(t, h, calls[0].Request.Envelope.AttemptID)
	est := estimate(calls[0].Request.Messages)
	if row.EstimatedPromptTokens == nil || *row.EstimatedPromptTokens != est || row.UsagePrompt == nil || *row.UsagePrompt != 5000 {
		t.Fatalf("attempt row = %+v", row)
	}
	var ev *wire.EvBudgetEstimateExceeded
	for _, e := range h.Events() {
		if p, ok := e.Payload.(*wire.EvBudgetEstimateExceeded); ok {
			ev = p
		}
	}
	if ev == nil || ev.Estimated != int64(est) || ev.Reported != 5000 || ev.AttemptID != row.ID {
		t.Fatalf("budget_estimate_exceeded = %+v", ev)
	}
	st, err := h.Engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range st.Participants {
		want := ""
		if p.ID == "author" {
			want = "exceeded"
		}
		if p.Estimate != want {
			t.Fatalf("%s estimate = %q, want %q", p.ID, p.Estimate, want)
		}
	}
	// The reservation was est+4096; the total now holds what was reported.
	if rec := budgetOf(t, h, reply.TurnID); rec.Charged != 5010 || rec.Reserved != 0 || rec.Exhausted {
		t.Fatalf("budget record = %+v", rec)
	}
}

func TestBudgetExhaustionRefusesLaterReservation(t *testing.T) {
	heavy := approveStep()
	heavy.Usage = &transport.Usage{Prompt: 10, Completion: 300000}
	scripts := map[string][]fake.Step{
		"author": {enginetest.Reply(authorText)},
		"r1":     {heavy},
		"r2":     {enginetest.Reply("no envelope"), approveStep()},
	}
	h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
	release := h.Hold("r2")
	turnID := submitReview(t, h, "s1")
	waitCount(t, h, wire.EventKindBudgetExhausted, 1)
	if rec := budgetOf(t, h, turnID); !rec.Exhausted || rec.Charged <= rec.Limit {
		t.Fatalf("budget record = %+v", rec)
	}
	// r2 was dispatched before the exhaustion and still finishes; its
	// malformed answer needs a repair, a later reservation, which is refused.
	release()
	wantEnd(t, h, turnID, engine.StateIncomplete, "budget:turn")
	if n := len(attemptsOf(h, turnID, "r2")); n != 1 {
		t.Fatalf("r2 attempts = %d, want only the one begun before exhaustion", n)
	}
	if first := getAttempt(t, h, attemptsOf(h, turnID, "r2")[0]); first.State == "pending" {
		t.Fatalf("r2 attempt = %+v", first)
	}
	exhausted := 0
	for _, e := range h.Events() {
		if p, ok := e.Payload.(*wire.EvBudgetExhausted); ok {
			exhausted++
			if p.Limit != 250000 || p.Charged <= p.Limit {
				t.Fatalf("budget_exhausted = %+v", p)
			}
		}
	}
	if exhausted != 1 {
		t.Fatalf("budget_exhausted events = %d", exhausted)
	}
}

func TestBudgetQuarantinedUsageIsChargedNeverRefunded(t *testing.T) {
	run := func(t *testing.T, usage transport.Usage) (before, after recordView, reservation int) {
		t.Helper()
		late := approveStep()
		late.Usage = &usage
		scripts := map[string][]fake.Step{
			"author": {enginetest.Reply(authorText)},
			"r1":     {late},
			"r2":     {blockStep("b1")},
		}
		h := enginetest.New(t, reviewRoster("author", "r1", "r2"), scripts)
		release := h.Hold("r1")
		turnID := submitReview(t, h, "s1")
		waitEnded(t, h, turnID)
		before = budgetOf(t, h, turnID)
		release()
		waitCount(t, h, wire.EventKindAttemptCompleted, 3)
		id := attemptsOf(h, turnID, "r1")[0]
		row := getAttempt(t, h, id)
		if !row.Quarantined || row.UsagePrompt == nil {
			t.Fatalf("late attempt = %+v", row)
		}
		reservation = *row.EstimatedPromptTokens + *row.MaxOutputTokens
		return before, budgetOf(t, h, turnID), reservation
	}
	t.Run("above", func(t *testing.T) {
		before, after, reservation := run(t, transport.Usage{Prompt: 10, Completion: 100000})
		if want := before.Charged + 100010 - reservation; after.Charged != want {
			t.Fatalf("charged = %d, want %d", after.Charged, want)
		}
		if after.Reserved != before.Reserved-reservation {
			t.Fatalf("reserved = %d, was %d", after.Reserved, before.Reserved)
		}
	})
	t.Run("below", func(t *testing.T) {
		before, after, _ := run(t, transport.Usage{Prompt: 1, Completion: 1})
		if after.Charged != before.Charged {
			t.Fatalf("charged went from %d to %d", before.Charged, after.Charged)
		}
	})
}

func TestBudgetRacingDispatchesExactlyOne(t *testing.T) {
	big := strings.Repeat("a", 60000)
	toml := reviewRoster("author", "r1", "r2")
	// Different pools, so both dispatches are ready at the same moment.
	toml = strings.Replace(toml, "id = \"r2\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \"reviewers\"",
		"id = \"r2\"\nmodel = \"llama3\"\nroute = \"api\"\nendpoint = \"local\"\neligible_roles = [\"reviewer\"]\npool = \"other\"", 1)
	toml += "\n[pools.other]\nconcurrency = 10\n"
	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{
			"author": {enginetest.Reply(big)},
			"r1":     {approveStep()},
			"r2":     {approveStep()},
		}
	}

	// A full run under a generous limit shows what the three reservations
	// add up to.
	full := enginetest.New(t, toml, scripts())
	turnID := submitReview(t, full, "s1")
	wantEnd(t, full, turnID, engine.StateApproved, "all_approved")
	total := budgetOf(t, full, turnID).Charged
	projected := budgetOf(t, full, turnID).Projected
	if total-1 < projected {
		t.Fatalf("test needs an actual total (%d) above the projection (%d)", total, projected)
	}

	for i := 0; i < 5; i++ {
		h := enginetest.New(t, withBudgets(toml, "turn_max_estimated_tokens = "+strconv.Itoa(total-1)), scripts())
		turnID := submitReview(t, h, "s1")
		wantEnd(t, h, turnID, engine.StateIncomplete, "budget:turn")
		begun := 0
		for _, e := range h.Events() {
			if p, ok := e.Payload.(*wire.EvAttemptBegun); ok && p.Phase == "review" {
				begun++
			}
		}
		if begun != 1 {
			t.Fatalf("run %d: %d reviewers dispatched, want exactly 1", i, begun)
		}
		if n := countEvents(h, wire.EventKindBudget); n != 1 {
			t.Fatalf("run %d: %d budget events", i, n)
		}
	}
}

func TestBudgetOverridesChangeOutputAndProjection(t *testing.T) {
	toml := enginetest.RosterTOML +
		"\n[presets.review.budgets]\nreviewer_max_output_tokens = 512\n" +
		"\n[quick.q2]\npreset = \"review\"\n[quick.q2.budgets]\nreviewer_max_output_tokens = 256\n"

	base := projectionOf(t, enginetest.RosterTOML, nil)

	h := enginetest.New(t, toml, reviewScripts(approveStep()))
	reply, err := submitReview2(h, "s1")
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, h, reply.TurnID, engine.StateApproved, "all_approved")
	if calls := reviewerCalls(h, "reviewer"); len(calls) != 1 || calls[0].Request.MaxOutputTokens != 512 {
		t.Fatalf("reviewer calls = %+v, want the preset's 512", calls)
	}
	if reply.EstimatedTokens >= int64(base) {
		t.Fatalf("projection %d did not shrink from %d", reply.EstimatedTokens, base)
	}

	q := enginetest.New(t, toml, reviewScripts(approveStep()))
	quick := "q2"
	qr, err := q.Engine.Submit(wire.SubmitRequest{SubmissionID: "s1", Input: testInput, QuickProfile: &quick})
	if err != nil {
		t.Fatal(err)
	}
	wantEnd(t, q, qr.TurnID, engine.StateApproved, "all_approved")
	if calls := reviewerCalls(q, "reviewer"); len(calls) != 1 || calls[0].Request.MaxOutputTokens != 256 {
		t.Fatalf("reviewer calls = %+v, want the quick profile's 256", calls)
	}
	if qr.EstimatedTokens >= reply.EstimatedTokens {
		t.Fatalf("quick projection %d did not shrink from %d", qr.EstimatedTokens, reply.EstimatedTokens)
	}
}
