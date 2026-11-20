package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/budget"
	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/prompt"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const budgetsPara = "Budgets"

func init() {
	cover(budgetsPara, "the projection for a one-reviewer preset equals the sum the test computes from the prompt builders with the stand-ins, per-message and per-request overheads included", "TestBudgets_ProjectionMatchesPromptBuilders")
	cover(budgetsPara, "every `fake.CompleteCall` carries the `MaxOutputTokens` its phase requires, clamped to a seeded `output_bound` when one is smaller", "TestBudgets_MaxOutputTokensClamped")
	cover(budgetsPara, "a participant with no `context_bound` and no `context_tokens` is refused with `context_unknown` naming `context_bound`", "TestBudgets_NoContextBoundRefused")
	cover(budgetsPara, "one whose endpoint lacks `tokens_per_byte_max` is refused naming it", "TestBudgets_NoTokensPerByteMaxRefused")
	cover(budgetsPara, "one with no calibration and no roster `bytes_per_token` is refused naming `bytes_per_token`", "TestBudgets_NoBytesPerTokenRefused")
	cover(budgetsPara, "the same participant with both roster values set passes and `status` shows each source as `roster`", "TestBudgets_RosterValuesPass")
	cover(budgetsPara, "a seeded calibration of 4.0 bytes per token yields an estimate computed at 3.0 (the safety factor) and the manifest records both", "TestBudgets_CalibrationSafetyFactor")
	cover(budgetsPara, "a fake that reports `usage.prompt_tokens` above the estimate causes `budget_estimate_exceeded` and `estimate: exceeded` in `status`, the turn's charged total rises by the difference in the same transaction as the completion, and the turn still completes", "TestBudgets_EstimateExceeded")
	cover(budgetsPara, "a fake whose reported usage pushes the charged total over `turn_max_estimated_tokens` marks the turn `exhausted`, emits `budget_exhausted`, lets the other in-flight reviewer finish, and refuses the delivery reservations with `budget:turn`", "TestBudgets_BudgetExhausted")
	cover(budgetsPara, "a quarantined late result whose reported usage exceeds its reservation raises the charged total by the difference and one whose usage is below it changes nothing", "TestBudgets_QuarantinedLateResult")
	cover(budgetsPara, "a request whose admission bound exceeds the usable context is refused at dispatch even when its estimate fits, and one whose estimate exceeds the turn budget is refused even when its admission bound fits the context", "TestBudgets_AdmissionAndEstimateSeparate")
	cover(budgetsPara, "two reviewers in different pools whose combined reservations exceed `turn_max_estimated_tokens` by one token yield exactly one dispatched and one `budget:turn`, never two dispatched, under a test that makes both `BeginAttempt` transactions race", "TestBudgets_RacingReservations")
	cover(budgetsPara, "an input that projects over the author's usable bound is refused with `budget: context` naming the author", "TestBudgets_AuthorContextBoundExceeded")
	cover(budgetsPara, "a fake author reply larger than the stand-in that pushes a reviewer over its bound ends the turn `budget:context:<reviewer>` after a `budget` event, with the candidate in the transcript whole", "TestBudgets_AuthorOutputBoundExceeded")
	cover(budgetsPara, "a preset whose budgets override lowers `reviewer_max_output_tokens` changes the reviewer's `MaxOutputTokens` and the projection", "TestBudgets_PresetBudgetsOverride")
	cover(budgetsPara, "a quick profile's budgets override applies after the preset's", "TestBudgets_QuickProfileBudgetsOverride")
	cover(budgetsPara, "`usage` returned by the fake is stored on the attempt and `turn` shows estimated and reported totals", "TestBudgets_UsageStored")
	cover(budgetsPara, "a participant on an endpoint declared `metered` is refused with `budget: spending_ceiling`", "TestBudgets_MeteredEndpointRefused")
	cover(budgetsPara, "a delivery that does not fit the recipient's bound is recorded `not_sent:budget` and the other deliveries are sent", "TestBudgets_DeliveryBoundNotFit")
	cover(budgetsPara, "`bytes_per_token` is in the manifest", "TestBudgets_BytesPerTokenInManifest")
}

// budgetsInput is the request the budget tests submit. It holds multi-byte
// UTF-8, so a count of bytes differs from a count of characters.
const budgetsInput = "what is the answer? héllo ✓"

// budgetsOverheads are the roster defaults the tests leave alone.
var budgetsOverheads = budget.Overheads{MessageTokens: 8, RequestTokens: 16}

// budgetsCfg shapes the roster of a budget test. The author, the reviewer and
// the optional second reviewer each sit in a pool of their own, so two
// reviewers dispatch in parallel.
type budgetsCfg struct {
	tpbm         string // the endpoint's tokens_per_byte_max; empty leaves it unset
	billing      string // the endpoint's declared_billing; empty means none
	budgets      string // further lines of [budgets]
	authorKeys   string // further keys of the author participant
	reviewerKeys string // further keys of the reviewer participant
	second       bool   // adds reviewer2 and the presets review2 and review2r
	extra        string // further tables: presets and quick profiles
}

// budgetsRoster builds the roster TOML of the cfg. calibration_safety is
// always declared, so the manifest carries it.
func budgetsRoster(c budgetsCfg) string {
	var b strings.Builder
	b.WriteString(`default_preset = "review"

[deadlines]
attempt     = "300s"
turn        = "900s"
delivery    = "60s"
provisional = false

[budgets]
calibration_safety = 0.75
`)
	b.WriteString(c.budgets + "\n")
	b.WriteString(`
[pools.pa]
concurrency = 1
[pools.pr]
concurrency = 1
[pools.pr2]
concurrency = 1

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "pa"
` + c.authorKeys + `

[[participants]]
id = "reviewer"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "pr"
` + c.reviewerKeys + "\n")
	if c.second {
		b.WriteString(`
[[participants]]
id = "reviewer2"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "pr2"
`)
	}
	billing := c.billing
	if billing == "" {
		billing = "none"
	}
	b.WriteString(`
[[endpoints]]
name = "local"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "` + billing + `"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
`)
	if c.tpbm != "" {
		b.WriteString("tokens_per_byte_max = " + c.tpbm + "\n")
	}
	b.WriteString(`
[presets.review]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
`)
	if c.second {
		b.WriteString(`
[presets.review2]
mode = "review"
author = "author"
required_reviewers = ["reviewer", "reviewer2"]

[presets.review2r]
mode = "review"
author = "author"
required_reviewers = ["reviewer2", "reviewer"]
`)
	}
	b.WriteString(c.extra)
	b.WriteString(`
[policy]
local_only = true
allowed_destinations = []
`)
	return b.String()
}

// budgetsOK is the roster of a budget test that needs nothing special.
func budgetsOK() string { return budgetsRoster(budgetsCfg{tpbm: "1.0"}) }

// budgetsReplyText is the author's reply unless a test gives another.
const budgetsReplyText = "the answer is 42"

// budgetsStep answers whatever it is asked: an approval of the candidate in a
// review, behind commentary when there is any, and the author text otherwise.
// A non-nil usage is what the step reports.
func budgetsStep(authorText, commentary string, usage *transport.Usage) fake.Step {
	step := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase != "review" {
			return authorText
		}
		out := fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		if commentary != "" {
			out = commentary + "\n" + out
		}
		return out
	})
	step.Usage = usage
	return step
}

// budgetsPlain is a step that reports no usage.
func budgetsPlain() fake.Step { return budgetsStep(budgetsReplyText, "", nil) }

// budgetsScript plays first, then n plain steps.
func budgetsScript(n int, first ...fake.Step) []fake.Step {
	out := append([]fake.Step(nil), first...)
	for i := 0; i < n; i++ {
		out = append(out, budgetsPlain())
	}
	return out
}

// budgetsScripts scripts the author and the reviewers with plain steps.
func budgetsScripts() map[string][]fake.Step {
	return map[string][]fake.Step{
		"author":    budgetsScript(16),
		"reviewer":  budgetsScript(16),
		"reviewer2": budgetsScript(16),
	}
}

// budgetsStart starts a suite on the roster with plain scripts.
func budgetsStart(t *testing.T, roster string, opts ...enginetest.Option) (*suite, *client.Client) {
	t.Helper()
	s := newSuite(t, roster, budgetsScripts(), opts...)
	return s, s.Dial(t)
}

// budgetsTrySubmit submits the input under the preset and quick profile,
// either of which may be empty, and returns the reply or the refusal.
func (s *suite) budgetsTrySubmit(c *client.Client, id, input, preset, quick string) (wire.SubmitReply, error) {
	req := wire.SubmitRequest{SubmissionID: id, Input: input}
	if preset != "" {
		req.Preset = &preset
	}
	if quick != "" {
		req.QuickProfile = &quick
	}
	return c.Submit(s.ctx(), req)
}

func (s *suite) budgetsSubmit(t *testing.T, c *client.Client, id, input, preset, quick string) wire.SubmitReply {
	t.Helper()
	reply, err := s.budgetsTrySubmit(c, id, input, preset, quick)
	if err != nil {
		t.Fatalf("submit %s: %v", id, err)
	}
	return reply
}

// budgetsRefusal requires err to be a refusal with code budget and the budget
// member named, and returns it.
func budgetsRefusal(t *testing.T, err error, budgetName string) *wire.Error {
	t.Helper()
	var we *wire.Error
	if !errors.As(err, &we) {
		t.Fatalf("submit returned %v, want an error{code: budget, budget: %s}", err, budgetName)
	}
	if we.Code != wire.ErrorCodeBudget || we.Budget == nil || *we.Budget != budgetName {
		t.Fatalf("submit refused with code %q budget %s, want code budget, budget %s", we.Code, str(we.Budget), budgetName)
	}
	return we
}

// budgetsWantNoTurn requires that no turn is open and none was submitted.
func (s *suite) budgetsWantNoTurn(t *testing.T, c *client.Client) {
	t.Helper()
	if open := s.Status(t, c).TurnsOpen; len(open) != 0 {
		t.Fatalf("turns open after a refused submit: %v", open)
	}
	for _, ev := range s.Events() {
		if ev.Kind == wire.EventKindTurnSubmitted {
			t.Fatalf("a refused submit announced turn_submitted: %s", ev.Body)
		}
	}
}

func budgetsInt(p *int64) int {
	if p == nil {
		return -1
	}
	return int(*p)
}

// budgetsWantContextUnknown requires a context_unknown refusal naming the
// participant and the missing member.
func budgetsWantContextUnknown(t *testing.T, err error, participant, missing string) {
	t.Helper()
	we := budgetsRefusal(t, err, "context_unknown")
	if we.Participant == nil || *we.Participant != participant || we.Missing == nil || *we.Missing != missing {
		t.Fatalf("context_unknown names participant %s missing %s, want participant %s missing %s", str(we.Participant), str(we.Missing), participant, missing)
	}
}

// budgetsParticipant returns the participant's row of status.
func budgetsParticipant(t *testing.T, st wire.StatusReply, id string) wire.Participant {
	t.Helper()
	for _, p := range st.Participants {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("status lists no participant %q", id)
	return wire.Participant{}
}

// budgetsSettled waits, on the event stream, until the turn has ended and
// every terminal delivery it sent has completed or been refused, and returns
// the turn record. A turn with no candidate sends no deliveries.
func (s *suite) budgetsSettled(t *testing.T, c *client.Client, turnID string) wire.TurnReply {
	t.Helper()
	term := s.WaitEvent(func(ev event) bool {
		st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState)
		return ok && st.TurnID == turnID && terminal(st.State)
	})
	turn := s.Turn(t, c, turnID)
	if turn.CandidateHash != nil && turn.Mode != "solo" && turn.State != engine.StateCancelled {
		vd := s.WaitEvent(func(ev event) bool {
			v, ok := body[wire.EvVerdictsDelivered](ev, wire.EventKindVerdictsDelivered)
			return ok && v.TurnID == turnID
		})
		recipients, _ := body[wire.EvVerdictsDelivered](vd, wire.EventKindVerdictsDelivered)
		for _, r := range recipients.Recipients {
			begun := s.WaitEvent(func(ev event) bool {
				b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
				return ok && b.Phase == "terminal_delivery" && b.Participant == r && ev.Seq > term.Seq
			})
			b, _ := body[wire.EvAttemptBegun](begun, wire.EventKindAttemptBegun)
			s.WaitEvent(func(ev event) bool {
				done, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted)
				return ok && done.AttemptID == b.AttemptID
			})
		}
	}
	return s.Turn(t, c, turnID)
}

// budgetsCalls returns the requests the participant received for the turn in
// the phase; an empty phase means every phase.
func (s *suite) budgetsCalls(participant, turnID, phase string) []transport.CompleteRequest {
	var out []transport.CompleteRequest
	for _, call := range s.Adapters[participant].Calls() {
		env := call.Request.Envelope
		if env.TurnID == turnID && (phase == "" || env.Phase == phase) {
			out = append(out, call.Request)
		}
	}
	return out
}

// budgetsAttempts returns the turn's attempt records of the participant and
// phase.
func budgetsAttempts(turn wire.TurnReply, participant, phase string) []wire.AttemptRecord {
	var out []wire.AttemptRecord
	for _, a := range turn.Attempts {
		if a.Participant == participant && a.Phase == phase {
			out = append(out, a)
		}
	}
	return out
}

// budgetsOne returns the one attempt record of the participant and phase.
func budgetsOne(t *testing.T, turn wire.TurnReply, participant, phase string) wire.AttemptRecord {
	t.Helper()
	as := budgetsAttempts(turn, participant, phase)
	if len(as) != 1 {
		t.Fatalf("turn has %d %s attempts of %s, want 1: %+v", len(as), phase, participant, turn.Attempts)
	}
	return as[0]
}

// budgetsEventsOf returns the decoded bodies of the events of the kind seen so
// far that match.
func budgetsEventsOf[T any](s *suite, kind string, match func(T) bool) []T {
	var out []T
	for _, ev := range s.Events() {
		if v, ok := body[T](ev, kind); ok && match(v) {
			out = append(out, v)
		}
	}
	return out
}

// budgetsProjection is the sum the design's projection gives for a preset
// with the author and one reviewer, both estimating at bytesPerToken: the
// author's prompt and output, the reviewer's prompt and output, the
// reviewer's repair prompt and output, and a delivery for each of the two
// participants, built with the prompt builders around stand-ins. The
// stand-ins fix only a length: a candidate hash is 64 characters, an attempt
// id and a delimiter 32.
func budgetsProjection(input string, authorMax, reviewerMax, deliveryMax int, bytesPerToken float64) int {
	est := func(msgs []transport.Message) int {
		return budget.EstimateTokens(msgs, bytesPerToken, budgetsOverheads)
	}
	hash := strings.Repeat("0", 64)
	token := strings.Repeat("0", 32)
	candidate := strings.Repeat("x", budget.StandInBytes(authorMax, bytesPerToken))
	output := strings.Repeat("x", budget.StandInBytes(reviewerMax, bytesPerToken))
	review := prompt.ReviewInput{Request: input, Candidate: candidate, CandidateHash: hash, AttemptID: token, Delimiter: token}
	reviewer, err := prompt.Reviewer(review)
	if err != nil {
		panic(err)
	}
	repair, err := prompt.Repair(review, output, token)
	if err != nil {
		panic(err)
	}
	delivery := prompt.Delivery(prompt.DeliveryInput{
		TerminalState:   engine.StateIncomplete,
		TerminalReason:  "block:" + token,
		CandidateHash:   hash,
		ReviewerOutputs: []prompt.ReviewerOutput{{Participant: "reviewer", Output: output}},
	})
	return est(prompt.Author(input)) + authorMax +
		est(reviewer) + reviewerMax +
		est(repair) + reviewerMax +
		2*(est(delivery)+deliveryMax)
}

// budgetsDefaultProjection is the projection under the roster defaults and
// the harness's calibration of 4.0 bytes per token with the safety of 0.75.
func budgetsDefaultProjection(input string) int {
	return budgetsProjection(input, 4096, 2048, 64, budget.EffectiveBytesPerToken(enginetest.DefaultCalibration, 0.75))
}

func TestBudgets_ProjectionMatchesPromptBuilders(t *testing.T) {
	t.Parallel()
	s, c := budgetsStart(t, budgetsOK())
	want := budgetsDefaultProjection(budgetsInput)

	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "")
	if reply.EstimatedTokens != int64(want) {
		t.Fatalf("submit reports a projection of %d, the prompt builders give %d", reply.EstimatedTokens, want)
	}
	turn := s.budgetsSettled(t, c, reply.TurnID)
	if turn.Budget.Projected == nil || *turn.Budget.Projected != int64(want) {
		t.Fatalf("turn reports a projection of %d, the prompt builders give %d", budgetsInt(turn.Budget.Projected), want)
	}
	if turn.Budget.Limit == nil || *turn.Budget.Limit != 250000 {
		t.Fatalf("turn limit = %d, want the default 250000", budgetsInt(turn.Budget.Limit))
	}
	// The overheads are in the sum: it exceeds the same sum without them.
	bare := budget.Overheads{}
	if budget.EstimateTokens(prompt.Author(budgetsInput), 3.0, bare) >= budget.EstimateTokens(prompt.Author(budgetsInput), 3.0, budgetsOverheads) {
		t.Fatal("the overheads add nothing to an estimate")
	}
}

func TestBudgets_MaxOutputTokensClamped(t *testing.T) {
	t.Parallel()
	// The author's bound is below its configured 4096 and above the
	// delivery's 64; the reviewer's is below all of 2048, repair and 64.
	reviewerProse := enginetest.ReplyFunc(func(req transport.CompleteRequest) string { return fake.VerdictProseApprove() })
	repairApprove := budgetsStep(budgetsReplyText, "", nil)
	s := newSuite(t, budgetsOK(), map[string][]fake.Step{
		"author":   budgetsScript(4),
		"reviewer": append([]fake.Step{reviewerProse, repairApprove}, budgetsScript(4)...),
	}, enginetest.WithOutputBound("author", 1000), enginetest.WithOutputBound("reviewer", 40))
	c := s.Dial(t)
	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "")
	turn := s.budgetsSettled(t, c, reply.TurnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	type call struct{ participant, phase string }
	want := map[call][]int{
		{"author", "author"}:              {1000},
		{"author", "terminal_delivery"}:   {64},
		{"reviewer", "review"}:            {40, 40}, // the review and its repair
		{"reviewer", "terminal_delivery"}: {40},
	}
	got := 0
	for id, a := range s.Adapters {
		for _, cc := range a.Calls() {
			got++
			key := call{id, cc.Request.Envelope.Phase}
			if len(want[key]) == 0 {
				t.Fatalf("unexpected call %+v", key)
			}
			if cc.Request.MaxOutputTokens != want[key][0] {
				t.Fatalf("%s %s call carries MaxOutputTokens %d, want %d", id, key.phase, cc.Request.MaxOutputTokens, want[key][0])
			}
			want[key] = want[key][1:]
		}
	}
	for key, rest := range want {
		if len(rest) != 0 {
			t.Fatalf("%s never received %d %s call(s)", key.participant, len(rest), key.phase)
		}
	}
	if got != 5 {
		t.Fatalf("adapters received %d calls, want 5", got)
	}
	repairs := 0
	for _, a := range turn.Attempts {
		if a.RepairOf != nil {
			repairs++
		}
	}
	if repairs != 1 {
		t.Fatalf("turn has %d repair attempts, want 1", repairs)
	}
	if one := budgetsOne(t, turn, "author", "author"); one.MaxOutputTokens != 1000 {
		t.Fatalf("author attempt records MaxOutputTokens %d, want 1000", one.MaxOutputTokens)
	}
}

func TestBudgets_NoContextBoundRefused(t *testing.T) {
	t.Parallel()
	s, c := budgetsStart(t, budgetsOK(), enginetest.WithContextBound("reviewer", 0))
	_, err := s.budgetsTrySubmit(c, "s1", budgetsInput, "", "")
	budgetsWantContextUnknown(t, err, "reviewer", "context_bound")
	s.budgetsWantNoTurn(t, c)
	if p := budgetsParticipant(t, s.Status(t, c), "reviewer"); p.ContextBound != nil {
		t.Fatalf("reviewer shows a context bound %d, want none", *p.ContextBound)
	}
}

func TestBudgets_NoTokensPerByteMaxRefused(t *testing.T) {
	t.Parallel()
	s, c := budgetsStart(t, budgetsRoster(budgetsCfg{}))
	_, err := s.budgetsTrySubmit(c, "s1", budgetsInput, "", "")
	budgetsWantContextUnknown(t, err, "author", "tokens_per_byte_max")
	s.budgetsWantNoTurn(t, c)
}

func TestBudgets_NoBytesPerTokenRefused(t *testing.T) {
	t.Parallel()
	s, c := budgetsStart(t, budgetsOK(), enginetest.WithCalibration("reviewer", 0))
	_, err := s.budgetsTrySubmit(c, "s1", budgetsInput, "", "")
	budgetsWantContextUnknown(t, err, "reviewer", "bytes_per_token")
	s.budgetsWantNoTurn(t, c)
	if p := budgetsParticipant(t, s.Status(t, c), "reviewer"); p.BytesPerToken != nil {
		t.Fatalf("reviewer shows bytes_per_token %v, want none", *p.BytesPerToken)
	}
}

func TestBudgets_RosterValuesPass(t *testing.T) {
	t.Parallel()
	declared := "context_tokens = 32768\nbytes_per_token = 2.5"
	roster := budgetsRoster(budgetsCfg{tpbm: "1.0", authorKeys: declared, reviewerKeys: declared})
	// The probe records of both participants carry neither value.
	s, c := budgetsStart(t, roster,
		enginetest.WithContextBound("author", 0), enginetest.WithContextBound("reviewer", 0),
		enginetest.WithCalibration("author", 0), enginetest.WithCalibration("reviewer", 0))

	for _, id := range []string{"author", "reviewer"} {
		p := budgetsParticipant(t, s.Status(t, c), id)
		if p.ContextSource != "roster" || p.BytesPerTokenSource != "roster" {
			t.Fatalf("%s sources: context %q, bytes_per_token %q, want roster for both", id, p.ContextSource, p.BytesPerTokenSource)
		}
		if p.ContextBound == nil || *p.ContextBound != 32768 || p.BytesPerToken == nil || *p.BytesPerToken != 2.5 {
			t.Fatalf("%s shows context %v bytes_per_token %v, want 32768 and 2.5 as declared", id, p.ContextBound, p.BytesPerToken)
		}
	}

	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "")
	turn := s.budgetsSettled(t, c, reply.TurnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	// The roster's value is used as declared, with no safety factor.
	call := s.budgetsCalls("author", reply.TurnID, "author")[0]
	if got, want := budgetsOne(t, turn, "author", "author").EstimatedPromptTokens, budget.EstimateTokens(call.Messages, 2.5, budgetsOverheads); got != int64(want) {
		t.Fatalf("author's estimate is %d, want %d computed at the declared 2.5", got, want)
	}
}

// budgetsManifest returns the stored manifest of the engine's current run.
func (s *suite) budgetsManifest(t *testing.T, c *client.Client) map[string]any {
	t.Helper()
	run, err := s.Store.GetRun(s.Status(t, c).RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(run.Manifest, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

// budgetsPath walks the keys of nested JSON objects.
func budgetsPath(t *testing.T, m any, keys ...string) any {
	t.Helper()
	cur := m
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("manifest at %v: not an object before %q", keys, k)
		}
		if cur, ok = obj[k]; !ok {
			t.Fatalf("manifest has no %v", keys)
		}
	}
	return cur
}

// budgetsRosterParticipant returns the manifest's roster entry of the participant.
func budgetsRosterParticipant(t *testing.T, m map[string]any, id string) map[string]any {
	t.Helper()
	list, _ := budgetsPath(t, m, "roster", "participants").([]any)
	for _, e := range list {
		if p, ok := e.(map[string]any); ok && p["id"] == id {
			return p
		}
	}
	t.Fatalf("manifest roster lists no participant %q", id)
	return nil
}

func TestBudgets_CalibrationSafetyFactor(t *testing.T) {
	t.Parallel()
	s, c := budgetsStart(t, budgetsOK()) // the harness seeds a calibration of 4.0
	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "")
	turn := s.budgetsSettled(t, c, reply.TurnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	for _, id := range []string{"author", "reviewer"} {
		p := budgetsParticipant(t, s.Status(t, c), id)
		if p.BytesPerTokenSource != "probe" || p.BytesPerToken == nil || *p.BytesPerToken != 3.0 {
			t.Fatalf("%s: bytes_per_token %v from %q, want 3.0 from probe", id, p.BytesPerToken, p.BytesPerTokenSource)
		}
	}
	// The estimate of a real request is computed at 3.0 and not at 4.0.
	for _, id := range []string{"author", "reviewer"} {
		phase := map[string]string{"author": "author", "reviewer": "review"}[id]
		call := s.budgetsCalls(id, reply.TurnID, phase)[0]
		at3 := budget.EstimateTokens(call.Messages, 3.0, budgetsOverheads)
		at4 := budget.EstimateTokens(call.Messages, 4.0, budgetsOverheads)
		if at3 == at4 {
			t.Fatalf("%s: estimates at 3.0 and 4.0 are equal (%d); the request cannot tell them apart", id, at3)
		}
		if got := budgetsOne(t, turn, id, phase).EstimatedPromptTokens; got != int64(at3) {
			t.Fatalf("%s's estimate is %d, want %d (at 3.0; at 4.0 it would be %d)", id, got, at3, at4)
		}
	}

	// The manifest records the calibration of 4.0 and the safety of 0.75 that
	// turn it into 3.0.
	m := s.budgetsManifest(t, c)
	for _, id := range []string{"author", "reviewer"} {
		cal := budgetsPath(t, m, "probe_records", id, "calibration", "bytes_per_token")
		if cal != 4.0 {
			t.Fatalf("manifest records a calibration of %v for %s, want 4.0", cal, id)
		}
	}
	safety := budgetsPath(t, m, "roster", "budgets", "calibration_safety")
	if safety != 0.75 || budget.EffectiveBytesPerToken(4.0, safety.(float64)) != 3.0 {
		t.Fatalf("manifest records calibration_safety %v, want 0.75 so that 4.0 becomes 3.0", safety)
	}
}

// budgetsCharged is what the turn's total must be once every attempt is
// settled: the reported usage of an attempt that has one and its reservation
// otherwise.
func budgetsCharged(turn wire.TurnReply) int64 {
	var total int64
	for _, a := range turn.Attempts {
		if a.Usage != nil {
			if a.Usage.PromptTokens != nil {
				total += *a.Usage.PromptTokens
			}
			if a.Usage.CompletionTokens != nil {
				total += *a.Usage.CompletionTokens
			}
			continue
		}
		total += a.EstimatedPromptTokens + a.MaxOutputTokens
	}
	return total
}

func TestBudgets_EstimateExceeded(t *testing.T) {
	t.Parallel()
	usage := &transport.Usage{Prompt: 50000, Completion: 100}
	s := newSuite(t, budgetsOK(), map[string][]fake.Step{
		"author":   budgetsScript(4),
		"reviewer": budgetsScript(4, budgetsStep(budgetsReplyText, "", usage)),
	})
	c := s.Dial(t)
	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "")
	turn := s.budgetsSettled(t, c, reply.TurnID)
	// The turn still completes.
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	review := budgetsOne(t, turn, "reviewer", "review")
	if review.EstimatedPromptTokens >= int64(usage.Prompt) {
		t.Fatalf("premise: the reviewer's estimate %d does not lie below the reported %d", review.EstimatedPromptTokens, usage.Prompt)
	}
	exceeded := budgetsEventsOf(s, wire.EventKindBudgetEstimateExceeded, func(wire.EvBudgetEstimateExceeded) bool { return true })
	if len(exceeded) != 1 || exceeded[0].TurnID != reply.TurnID || exceeded[0].AttemptID != review.ID ||
		exceeded[0].Estimated != review.EstimatedPromptTokens || exceeded[0].Reported != int64(usage.Prompt) {
		t.Fatalf("budget_estimate_exceeded events = %+v, want one for attempt %s estimated %d reported %d", exceeded, review.ID, review.EstimatedPromptTokens, usage.Prompt)
	}

	st := s.Status(t, c)
	if got := budgetsParticipant(t, st, "reviewer").Estimate; got != "exceeded" {
		t.Fatalf("reviewer estimate in status = %q, want exceeded", got)
	}
	if got := budgetsParticipant(t, st, "author").Estimate; got != "" {
		t.Fatalf("author estimate in status = %q, want none", got)
	}

	// The total is the usage the servers reported for the reviewer and the
	// reservations of the rest: it rose, by reported minus reservation.
	if turn.Budget.Charged == nil || *turn.Budget.Charged != budgetsCharged(turn) {
		t.Fatalf("charged = %d, want %d from the attempts", budgetsInt(turn.Budget.Charged), budgetsCharged(turn))
	}
	reservation := review.EstimatedPromptTokens + review.MaxOutputTokens
	rose := int64(usage.Prompt+usage.Completion) - reservation
	if rose <= 0 {
		t.Fatalf("premise: reported usage %d does not exceed the reservation %d", usage.Prompt+usage.Completion, reservation)
	}
	var reservations int64
	for _, a := range turn.Attempts {
		reservations += a.EstimatedPromptTokens + a.MaxOutputTokens
	}
	if got := *turn.Budget.Charged - reservations; got != rose {
		t.Fatalf("charged rose by %d over the reservations, want %d (reported %d less reservation %d)", got, rose, usage.Prompt+usage.Completion, reservation)
	}

	// The charge is in the transaction of the completion: the event of the
	// excess is followed at once by the attempt's completion.
	var completedSeq, exceededSeq int64
	for _, ev := range s.Events() {
		if done, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted); ok && done.AttemptID == review.ID {
			completedSeq = ev.Seq
		}
		if _, ok := body[wire.EvBudgetEstimateExceeded](ev, wire.EventKindBudgetEstimateExceeded); ok {
			exceededSeq = ev.Seq
		}
	}
	if completedSeq == 0 || completedSeq-exceededSeq != 1 {
		t.Fatalf("budget_estimate_exceeded at seq %d, attempt_completed at %d; want them adjacent, one transaction", exceededSeq, completedSeq)
	}
}

func TestBudgets_BudgetExhausted(t *testing.T) {
	t.Parallel()
	// reviewer reports more than the whole limit; reviewer2 is still in
	// flight when it does.
	usage := &transport.Usage{Prompt: 260000, Completion: 10}
	s := newSuite(t, budgetsRoster(budgetsCfg{tpbm: "1.0", second: true}), map[string][]fake.Step{
		"author":    budgetsScript(4),
		"reviewer":  budgetsScript(4, budgetsStep(budgetsReplyText, "", usage)),
		"reviewer2": budgetsScript(4),
	})
	c := s.Dial(t)
	releaseFirst := s.h.Hold("reviewer")
	releaseSecond := s.h.Hold("reviewer2")
	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "review2", "")
	turnID := reply.TurnID

	var second wire.EvAttemptBegun
	for _, id := range []string{"reviewer", "reviewer2"} {
		ev := s.WaitEvent(func(ev event) bool {
			b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
			return ok && b.Phase == "review" && b.Participant == id
		})
		if id == "reviewer2" {
			second, _ = body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		}
	}
	releaseFirst()
	ex := s.WaitEvent(func(ev event) bool {
		e, ok := body[wire.EvBudgetExhausted](ev, wire.EventKindBudgetExhausted)
		return ok && e.TurnID == turnID
	})
	exhausted, _ := body[wire.EvBudgetExhausted](ex, wire.EventKindBudgetExhausted)
	if exhausted.Limit != 250000 || exhausted.Charged <= exhausted.Limit {
		t.Fatalf("budget_exhausted = %+v, want a charge over the limit of 250000", exhausted)
	}
	// The other reviewer is in flight still and nothing has ended the turn.
	for _, ev := range s.Events() {
		if done, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted); ok && done.AttemptID == second.AttemptID {
			t.Fatal("reviewer2's attempt completed before it was released")
		}
		if st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState); ok && st.TurnID == turnID && terminal(st.State) {
			t.Fatalf("the turn ended %q while reviewer2 was in flight", st.State)
		}
	}
	if b := s.Turn(t, c, turnID).Budget; !b.Exhausted {
		t.Fatalf("turn budget = %+v, want exhausted", b)
	}

	releaseSecond()
	turn := s.budgetsSettled(t, c, turnID)
	// The in-flight attempt ran to completion and the turn reached a terminal
	// state through it.
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if done := budgetsOne(t, turn, "reviewer2", "review"); done.Status != string(transport.StatusComplete) || done.TransportStatus != transport.TransportStatusOK {
		t.Fatalf("reviewer2's attempt = %+v, want it complete", done)
	}
	if !turn.Budget.Exhausted {
		t.Fatalf("turn budget = %+v, want exhausted at the end", turn.Budget)
	}
	// Every delivery reservation was refused: three recipients, none sent.
	deliveries := 0
	for _, a := range turn.Attempts {
		if a.Phase != "terminal_delivery" {
			continue
		}
		deliveries++
		if a.TransportStatus != "not_sent:budget" {
			t.Fatalf("delivery to %s has transport status %q, want not_sent:budget", a.Participant, a.TransportStatus)
		}
	}
	if deliveries != 3 {
		t.Fatalf("%d delivery attempts, want one for each of the three participants", deliveries)
	}
	for id := range s.Adapters {
		if n := len(s.budgetsCalls(id, turnID, "terminal_delivery")); n != 0 {
			t.Fatalf("%s received %d deliveries, want none", id, n)
		}
	}
}

// budgetsLate cancels a turn with its reviewer in flight, then lets the
// reviewer answer with the usage. It returns the turn's charged total before
// and after the late result is stored, and the attempt's reservation.
func budgetsLate(t *testing.T, usage *transport.Usage) (before, after, reservation int64) {
	t.Helper()
	s := newSuite(t, budgetsOK(), map[string][]fake.Step{
		"author":   budgetsScript(4),
		"reviewer": budgetsScript(4, budgetsStep(budgetsReplyText, "", usage)),
	})
	c := s.Dial(t)
	release := s.h.Hold("reviewer")
	turnID := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "").TurnID
	begun := s.WaitEvent(func(ev event) bool {
		b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun)
		return ok && b.Participant == "reviewer" && b.Phase == "review"
	})
	attempt, _ := body[wire.EvAttemptBegun](begun, wire.EventKindAttemptBegun)
	if err := c.Cancel(s.ctx(), turnID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	s.WaitTurnState(turnID, engine.StateCancelled)
	for _, ev := range s.Events() {
		if done, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted); ok && done.AttemptID == attempt.AttemptID {
			t.Fatal("premise: the reviewer's attempt completed before its result was released")
		}
	}
	b := s.Turn(t, c, turnID).Budget
	if b.Charged == nil || b.Exhausted {
		t.Fatalf("turn budget before the late result = %+v", b)
	}
	before = *b.Charged

	release()
	// The late result is stored and charged in the transaction that
	// announces the attempt's completion.
	s.WaitEvent(func(ev event) bool {
		done, ok := body[wire.EvAttemptCompleted](ev, wire.EventKindAttemptCompleted)
		return ok && done.AttemptID == attempt.AttemptID
	})
	turn := s.Turn(t, c, turnID)
	wantTurn(t, turn, engine.StateCancelled, "cancelled")
	if turn.Budget.Charged == nil || turn.Budget.Reserved == nil || *turn.Budget.Reserved != 0 {
		t.Fatalf("turn budget after the late result = %+v, want nothing reserved", turn.Budget)
	}
	one := budgetsOne(t, turn, "reviewer", "review")
	return before, *turn.Budget.Charged, one.EstimatedPromptTokens + one.MaxOutputTokens
}

func TestBudgets_QuarantinedLateResult(t *testing.T) {
	t.Parallel()
	// Above the reservation: the overuse is charged.
	above := &transport.Usage{Prompt: 9000, Completion: 1000}
	before, after, reservation := budgetsLate(t, above)
	if int64(above.Prompt+above.Completion) <= reservation {
		t.Fatalf("premise: reported %d does not exceed the reservation %d", above.Prompt+above.Completion, reservation)
	}
	if want := before + int64(above.Prompt+above.Completion) - reservation; after != want {
		t.Fatalf("charged after a late result above its reservation = %d, want %d (%d + reported %d - reservation %d)", after, want, before, above.Prompt+above.Completion, reservation)
	}

	// Below it: the reservation stands and nothing is refunded.
	below := &transport.Usage{Prompt: 100, Completion: 20}
	before, after, reservation = budgetsLate(t, below)
	if int64(below.Prompt+below.Completion) >= reservation {
		t.Fatalf("premise: reported %d is not below the reservation %d", below.Prompt+below.Completion, reservation)
	}
	if after != before {
		t.Fatalf("charged after a late result below its reservation = %d, want it unchanged at %d", after, before)
	}
}

// budgetsReviewerNeed is the usable context a reviewer needs for the
// projection to pass when the candidate is a stand-in of authorMax tokens at 3
// bytes per token: the greater of the admission bounds, with the output, of
// the reviewer's review and repair prompts as the projection builds them.
func budgetsReviewerNeed(input string, authorMax, reviewerMax int) int {
	const bytesPerToken = 3.0
	hash := strings.Repeat("0", 64)
	token := strings.Repeat("0", 32)
	review := prompt.ReviewInput{
		Request:       input,
		Candidate:     strings.Repeat("x", budget.StandInBytes(authorMax, bytesPerToken)),
		CandidateHash: hash,
		AttemptID:     token,
		Delimiter:     token,
	}
	msgs, err := prompt.Reviewer(review)
	if err != nil {
		panic(err)
	}
	repair, err := prompt.Repair(review, strings.Repeat("x", budget.StandInBytes(reviewerMax, bytesPerToken)), token)
	if err != nil {
		panic(err)
	}
	need := 0
	for _, m := range [][]transport.Message{msgs, repair} {
		if n := budget.AdmissionTokens(m, 1.0, budgetsOverheads) + reviewerMax; n > need {
			need = n
		}
	}
	return need
}

// budgetsRealReview is the reviewer's prompt for the candidate the author
// really returns.
func budgetsRealReview(input, candidate string) []transport.Message {
	msgs, err := prompt.Reviewer(prompt.ReviewInput{
		Request:       input,
		Candidate:     candidate,
		CandidateHash: strings.Repeat("0", 64),
		AttemptID:     strings.Repeat("0", 32),
		Delimiter:     strings.Repeat("0", 32),
	})
	if err != nil {
		panic(err)
	}
	return msgs
}

// budgetsOverflow submits a turn whose author's reply is larger than the
// stand-in the projection used, against a reviewer whose context holds
// exactly what the projection needed. It returns the suite, a client, the
// turn id, the reply and the reviewer's usable bound.
func budgetsOverflow(t *testing.T) (s *suite, c *client.Client, turnID, reply string, usable int) {
	t.Helper()
	const authorMax, reviewerMax = 100, 100
	usable = budgetsReviewerNeed(budgetsInput, authorMax, reviewerMax)
	reply = strings.Repeat("y", 4000)
	roster := budgetsRoster(budgetsCfg{
		tpbm: "1.0",
		budgets: fmt.Sprintf("author_max_output_tokens = %d\nreviewer_max_output_tokens = %d\ncontext_margin = 0.0",
			authorMax, reviewerMax),
	})
	s = newSuite(t, roster, map[string][]fake.Step{
		"author":   budgetsScript(4, budgetsStep(reply, "", nil)),
		"reviewer": budgetsScript(4),
	}, enginetest.WithContextBound("reviewer", usable))
	c = s.Dial(t)
	// The submit passes: the projection's stand-in fits the reviewer exactly.
	turnID = s.budgetsSubmit(t, c, "s1", budgetsInput, "", "").TurnID
	return s, c, turnID, reply, usable
}

func TestBudgets_AdmissionAndEstimateSeparate(t *testing.T) {
	t.Parallel()

	// Admission refuses while the estimate fits. The reviewer's real prompt
	// has an estimate (at 3 bytes per token) that fits its context, and an
	// admission bound (at 1 token per byte) that does not.
	s, c, turnID, reply, usable := budgetsOverflow(t)
	msgs := budgetsRealReview(budgetsInput, reply)
	est := budget.EstimateTokens(msgs, 3.0, budgetsOverheads) + 100
	adm := budget.AdmissionTokens(msgs, 1.0, budgetsOverheads) + 100
	if est > usable || usable >= adm {
		t.Fatalf("premise: estimate %d, usable %d, admission %d; want estimate <= usable < admission", est, usable, adm)
	}
	turn := s.budgetsSettled(t, c, turnID)
	wantTurn(t, turn, engine.StateIncomplete, "budget:context:reviewer")
	if n := len(s.budgetsCalls("reviewer", turnID, "review")); n != 0 {
		t.Fatalf("the reviewer was sent %d requests the admission bound refuses", n)
	}
	if len(budgetsAttempts(turn, "reviewer", "review")) != 0 {
		t.Fatalf("a refused dispatch left an attempt: %+v", turn.Attempts)
	}
	refused := budgetsEventsOf(s, wire.EventKindBudget, func(wire.EvBudget) bool { return true })
	if len(refused) != 1 || refused[0].Budget != "context" || refused[0].Participant == nil || *refused[0].Participant != "reviewer" ||
		refused[0].Admission == nil || *refused[0].Admission != int64(adm) || refused[0].Limit != int64(usable) {
		t.Fatalf("budget events = %+v, want one context refusal of reviewer with admission %d over limit %d", refused, adm, usable)
	}

	// The turn budget refuses while admission fits. A reply of 90000 bytes
	// has an admission bound well inside the default context of 128000 and an
	// estimate that takes the turn past a limit set to the projection.
	limit := budgetsDefaultProjection(budgetsInput)
	big := strings.Repeat("z", 90000)
	roster := budgetsRoster(budgetsCfg{tpbm: "1.0", budgets: fmt.Sprintf("turn_max_estimated_tokens = %d", limit)})
	s2 := newSuite(t, roster, map[string][]fake.Step{
		"author":   budgetsScript(4, budgetsStep(big, "", nil)),
		"reviewer": budgetsScript(4),
	})
	c2 := s2.Dial(t)
	reply2 := s2.budgetsSubmit(t, c2, "s1", budgetsInput, "", "")
	if reply2.EstimatedTokens != int64(limit) {
		t.Fatalf("the projection is %d, want it exactly at the limit %d", reply2.EstimatedTokens, limit)
	}
	bigMsgs := budgetsRealReview(budgetsInput, big)
	if got, usable := budget.AdmissionTokens(bigMsgs, 1.0, budgetsOverheads)+2048, budget.UsableBound(enginetest.DefaultContextBound, 0.1); got > usable {
		t.Fatalf("premise: admission %d does not fit the usable context %d", got, usable)
	}
	turn2 := s2.budgetsSettled(t, c2, reply2.TurnID)
	wantTurn(t, turn2, engine.StateIncomplete, "budget:turn")
	if len(budgetsAttempts(turn2, "reviewer", "review")) != 0 || len(s2.budgetsCalls("reviewer", reply2.TurnID, "review")) != 0 {
		t.Fatalf("the reviewer was dispatched past the turn budget: %+v", turn2.Attempts)
	}
	author := budgetsOne(t, turn2, "author", "author")
	wantNext := author.EstimatedPromptTokens + author.MaxOutputTokens +
		int64(budget.EstimateTokens(bigMsgs, 3.0, budgetsOverheads)+2048)
	over := budgetsEventsOf(s2, wire.EventKindBudget, func(wire.EvBudget) bool { return true })
	if len(over) != 1 || over[0].Budget != "turn_max_estimated_tokens" || over[0].Limit != int64(limit) ||
		over[0].Estimated == nil || *over[0].Estimated != wantNext || wantNext <= int64(limit) {
		t.Fatalf("budget events = %+v, want one turn refusal estimating %d over the limit %d", over, wantNext, limit)
	}
}

// budgetsRacingTurn submits one turn of the racing scenario and waits for it
// to end.
func budgetsRacingTurn(t *testing.T, s *suite, c *client.Client, id, preset string) (string, wire.TurnReply) {
	t.Helper()
	reply := s.budgetsSubmit(t, c, id, budgetsInput, preset, "")
	s.WaitTerminal(reply.TurnID)
	return reply.TurnID, s.Turn(t, c, reply.TurnID)
}

// budgetsGate holds a turn's terminal deliveries inside the fake's Complete
// call until the test lets them go. entered receives once for each delivery
// held.
type budgetsGate struct {
	entered chan string
	release chan struct{}
	once    sync.Once
}

func (g *budgetsGate) open() { g.once.Do(func() { close(g.release) }) }

// budgetsGates are the delivery gates of a suite, one per turn, created by
// whichever side asks first.
type budgetsGates struct {
	mu sync.Mutex
	m  map[string]*budgetsGate
}

func (gs *budgetsGates) of(turnID string) *budgetsGate {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	g := gs.m[turnID]
	if g == nil {
		g = &budgetsGate{entered: make(chan string, 8), release: make(chan struct{})}
		gs.m[turnID] = g
	}
	return g
}

func (gs *budgetsGates) openAll() {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	for _, g := range gs.m {
		g.open()
	}
}

// budgetsGatedStep approves a review and answers a terminal delivery only
// once the delivery's turn gate is open.
func budgetsGatedStep(gs *budgetsGates) fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		switch req.Envelope.Phase {
		case "review":
			return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
		case "terminal_delivery":
			g := gs.of(req.Envelope.TurnID)
			g.entered <- req.Envelope.Participant
			<-g.release
		}
		return budgetsReplyText
	})
}

func TestBudgets_RacingReservations(t *testing.T) {
	t.Parallel()
	// Measure the reservations of a roster with room: the attempts of a
	// two-reviewer turn, and its projection.
	probe, pc := budgetsStart(t, budgetsRoster(budgetsCfg{tpbm: "1.0", second: true}))
	measuredID, _ := budgetsRacingTurn(t, probe, pc, "m1", "review2")
	measured := probe.budgetsSettled(t, pc, measuredID)
	wantTurn(t, measured, engine.StateApproved, "all_approved")
	res := func(participant string) int64 {
		a := budgetsOne(t, measured, participant, "review")
		return a.EstimatedPromptTokens + a.MaxOutputTokens
	}
	limit := *measured.Budget.Projected
	// The limit is the projection, so every submit passes. In a racing turn
	// the author reports the usage that makes the two reviewers'
	// reservations, on top of it, exceed the limit by exactly one token: the
	// total at the second reservation is limit + 1.
	authorCharge := limit + 1 - res("reviewer") - res("reviewer2")
	if authorCharge <= 0 || authorCharge > limit {
		t.Fatalf("premise: the author's charge %d does not lie within the limit %d", authorCharge, limit)
	}

	// How the race is forced. Each round runs two turns. The first, a plain
	// turn, is approved and sends its terminal deliveries; the fake holds the
	// two reviewers' deliveries inside Complete, so each keeps its
	// reviewer's pool (pr and pr2, concurrency 1) occupied. The second, the
	// racing turn, is submitted then: its author runs in its own pool, and
	// its two reviewers find their pools full and queue. Only then does the
	// test open the gate, letting both held deliveries return at once. Each
	// returns on its own goroutine, frees its pool and, from that goroutine,
	// starts the reviewer queued there, whose BeginAttempt transaction makes
	// the reservation. So the two reservations are made by two goroutines
	// released together, and either may go first; nothing in the test orders
	// them. The engine's locks decide the order and the second must see the
	// first's reservation. Run under -race, 24 rounds give the detector both
	// goroutines touching the turn's budget each time.
	const rounds = 24
	gates := &budgetsGates{m: map[string]*budgetsGate{}}
	t.Cleanup(gates.openAll)
	roster := budgetsRoster(budgetsCfg{tpbm: "1.0", second: true, budgets: fmt.Sprintf("turn_max_estimated_tokens = %d", limit)})
	// The author's calls are serial in its pool: the plain turn's work and
	// delivery, then the racing turn's work, which reports the charge, and
	// its delivery, which fits the budget and is sent.
	var author, reviewers []fake.Step
	for i := 0; i < rounds; i++ {
		author = append(author, budgetsPlain(), budgetsPlain(),
			budgetsStep(budgetsReplyText, "", &transport.Usage{Prompt: int(authorCharge)}), budgetsPlain())
	}
	for i := 0; i < 4*rounds; i++ {
		reviewers = append(reviewers, budgetsGatedStep(gates))
	}
	s := newSuite(t, roster, map[string][]fake.Step{
		"author":    author,
		"reviewer":  reviewers,
		"reviewer2": append([]fake.Step(nil), reviewers...),
	})
	c := s.Dial(t)

	won := map[string]int{}
	for i := 0; i < rounds; i++ {
		// The plain turn: approved, its reviewers' deliveries held.
		plain := s.budgetsSubmit(t, c, fmt.Sprintf("p%d", i), budgetsInput, "review2", "")
		if st := s.WaitTerminal(plain.TurnID); st.State != engine.StateApproved {
			t.Fatalf("round %d: the plain turn ended %+v, want approved", i, st)
		}
		held := gates.of(plain.TurnID)
		got := map[string]bool{<-held.entered: true}
		got[<-held.entered] = true
		if len(got) != 2 || !got["reviewer"] || !got["reviewer2"] {
			t.Fatalf("round %d: deliveries held for %v, want reviewer and reviewer2", i, got)
		}

		// The racing turn: both reviewers queue behind the held deliveries.
		turnID := s.budgetsSubmit(t, c, fmt.Sprintf("r%d", i), budgetsInput, "review2", "").TurnID
		gates.of(turnID).open()
		submitted := s.WaitEvent(func(ev event) bool {
			b, ok := body[wire.EvTurnSubmitted](ev, wire.EventKindTurnSubmitted)
			return ok && b.TurnID == turnID
		})
		for _, id := range []string{"reviewer", "reviewer2"} {
			s.WaitEvent(func(ev event) bool {
				p, ok := body[wire.EvParticipantState](ev, wire.EventKindParticipantState)
				return ok && ev.Seq > submitted.Seq && p.Participant == id && p.State == engine.ParticipantQueued
			})
		}
		for _, ev := range s.Events() {
			if b, ok := body[wire.EvAttemptBegun](ev, wire.EventKindAttemptBegun); ok && ev.Seq > submitted.Seq && b.Phase == "review" {
				t.Fatalf("round %d: premise: %s's review began before the pools were freed", i, b.Participant)
			}
		}
		held.open()

		s.WaitTerminal(turnID)
		turn := s.Turn(t, c, turnID)
		wantTurn(t, turn, engine.StateIncomplete, "budget:turn")
		// A dispatch is an attempt begun; the request itself may or may not
		// have left before the turn ended, so the adapters' calls are not
		// counted, but the loser was sent nothing.
		var winners []string
		for _, id := range []string{"reviewer", "reviewer2"} {
			if n := len(budgetsAttempts(turn, id, "review")); n > 0 {
				winners = append(winners, id)
			}
		}
		if len(winners) != 1 || len(budgetsAttempts(turn, winners[0], "review")) != 1 {
			t.Fatalf("round %d: review attempts begun by %v, want exactly one: %+v", i, winners, turn.Attempts)
		}
		won[winners[0]]++
		for _, id := range []string{"reviewer", "reviewer2"} {
			if n := len(s.budgetsCalls(id, turnID, "review")); n > 1 || (id != winners[0] && n != 0) {
				t.Fatalf("round %d: %s was sent %d review requests", i, id, n)
			}
		}
		refused := budgetsEventsOf(s, wire.EventKindBudget, func(b wire.EvBudget) bool { return b.TurnID == turnID })
		if len(refused) != 1 || refused[0].Budget != "turn_max_estimated_tokens" || refused[0].Limit != limit ||
			refused[0].Estimated == nil || *refused[0].Estimated != limit+1 {
			t.Fatalf("round %d: budget events %+v, want one turn refusal at %d over the limit %d", i, refused, limit+1, limit)
		}
		if turn.Budget.Charged == nil || *turn.Budget.Charged > limit || turn.Budget.Exhausted {
			t.Fatalf("round %d: budget %+v, want a charge within the limit %d", i, turn.Budget, limit)
		}
		// Let the racing turn's deliveries settle before the next round.
		s.budgetsSettled(t, c, turnID)
	}
	t.Logf("first reservation won by: %v", won)
}

func TestBudgets_AuthorContextBoundExceeded(t *testing.T) {
	t.Parallel()
	const authorMax = 4096
	s, c := budgetsStart(t, budgetsOK(), enginetest.WithContextBound("author", 6000))
	usable := budget.UsableBound(6000, 0.1)
	input := strings.Repeat("q", 8000)
	_, err := s.budgetsTrySubmit(c, "s1", input, "", "")
	we := budgetsRefusal(t, err, "context")
	wantAdmission := budget.AdmissionTokens(prompt.Author(input), 1.0, budgetsOverheads) + authorMax
	if we.Participant == nil || *we.Participant != "author" ||
		we.Admission == nil || *we.Admission != int64(wantAdmission) || we.Limit == nil || *we.Limit != int64(usable) {
		t.Fatalf("refusal names participant %s admission %d limit %d, want author, %d over %d",
			str(we.Participant), budgetsInt(we.Admission), budgetsInt(we.Limit), wantAdmission, usable)
	}
	s.budgetsWantNoTurn(t, c)

	// A small input fits the same author: the size of the input refused it.
	small := budget.AdmissionTokens(prompt.Author(budgetsInput), 1.0, budgetsOverheads) + authorMax
	if small > usable {
		t.Fatalf("premise: the small input needs %d, over the usable %d", small, usable)
	}
	reply := s.budgetsSubmit(t, c, "s2", budgetsInput, "", "")
	wantTurn(t, s.budgetsSettled(t, c, reply.TurnID), engine.StateApproved, "all_approved")
}

func TestBudgets_AuthorOutputBoundExceeded(t *testing.T) {
	t.Parallel()
	s, c, turnID, reply, usable := budgetsOverflow(t)
	turn := s.budgetsSettled(t, c, turnID)
	wantTurn(t, turn, engine.StateIncomplete, "budget:context:reviewer")

	refused := budgetsEventsOf(s, wire.EventKindBudget, func(wire.EvBudget) bool { return true })
	if len(refused) != 1 || refused[0].TurnID != turnID || refused[0].Budget != "context" ||
		refused[0].Participant == nil || *refused[0].Participant != "reviewer" || refused[0].Limit != int64(usable) {
		t.Fatalf("budget events = %+v, want one context refusal of reviewer over %d", refused, usable)
	}
	// The budget event came before the turn ended.
	var budgetSeq, endSeq int64
	for _, ev := range s.Events() {
		if _, ok := body[wire.EvBudget](ev, wire.EventKindBudget); ok {
			budgetSeq = ev.Seq
		}
		if st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState); ok && st.TurnID == turnID && terminal(st.State) {
			endSeq = ev.Seq
		}
	}
	if budgetSeq == 0 || budgetSeq >= endSeq {
		t.Fatalf("budget event at seq %d, turn ended at %d; want the event first", budgetSeq, endSeq)
	}

	// The candidate is whole in the transcript: the author's reply, byte for
	// byte, is one of the transcript's items.
	tr, err := c.Transcript(s.ctx(), turnID, "", 100)
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	found := false
	for _, item := range tr.Items {
		data, err := c.Content(s.ctx(), item.Ref)
		if err != nil {
			t.Fatalf("content %s: %v", item.Ref, err)
		}
		if string(data) == reply {
			found = true
		}
	}
	if !found {
		t.Fatalf("no transcript item of %d items holds the author's reply of %d bytes whole", len(tr.Items), len(reply))
	}
	if turn.CandidateHash == nil {
		t.Fatal("the turn has no candidate")
	}
}

// budgetsRunTurn submits under the preset and quick profile and returns the
// reply and the settled turn.
func budgetsRunTurn(t *testing.T, s *suite, c *client.Client, id, preset, quick string) (wire.SubmitReply, wire.TurnReply) {
	t.Helper()
	reply := s.budgetsSubmit(t, c, id, budgetsInput, preset, quick)
	return reply, s.budgetsSettled(t, c, reply.TurnID)
}

// budgetsWantOutputs requires the turn's author and reviewer calls to carry
// the maxima.
func (s *suite) budgetsWantOutputs(t *testing.T, turnID string, author, reviewer int) {
	t.Helper()
	if calls := s.budgetsCalls("author", turnID, "author"); len(calls) != 1 || calls[0].MaxOutputTokens != author {
		t.Fatalf("author calls = %+v, want one with MaxOutputTokens %d", calls, author)
	}
	if calls := s.budgetsCalls("reviewer", turnID, "review"); len(calls) != 1 || calls[0].MaxOutputTokens != reviewer {
		t.Fatalf("reviewer calls = %+v, want one with MaxOutputTokens %d", calls, reviewer)
	}
}

func TestBudgets_PresetBudgetsOverride(t *testing.T) {
	t.Parallel()
	roster := budgetsRoster(budgetsCfg{tpbm: "1.0", extra: `
[presets.small]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
[presets.small.budgets]
reviewer_max_output_tokens = 512
`})
	s, c := budgetsStart(t, roster)
	bpt := budget.EffectiveBytesPerToken(enginetest.DefaultCalibration, 0.75)

	base, baseTurn := budgetsRunTurn(t, s, c, "s1", "review", "")
	wantTurn(t, baseTurn, engine.StateApproved, "all_approved")
	s.budgetsWantOutputs(t, base.TurnID, 4096, 2048)
	if want := budgetsProjection(budgetsInput, 4096, 2048, 64, bpt); base.EstimatedTokens != int64(want) {
		t.Fatalf("projection under the roster budgets is %d, want %d", base.EstimatedTokens, want)
	}

	small, smallTurn := budgetsRunTurn(t, s, c, "s2", "small", "")
	wantTurn(t, smallTurn, engine.StateApproved, "all_approved")
	s.budgetsWantOutputs(t, small.TurnID, 4096, 512)
	want := budgetsProjection(budgetsInput, 4096, 512, 64, bpt)
	if small.EstimatedTokens != int64(want) || *smallTurn.Budget.Projected != int64(want) {
		t.Fatalf("projection under the preset's override is %d (turn %d), want %d", small.EstimatedTokens, budgetsInt(smallTurn.Budget.Projected), want)
	}
	if small.EstimatedTokens >= base.EstimatedTokens {
		t.Fatalf("the lowered maximum did not lower the projection: %d against %d", small.EstimatedTokens, base.EstimatedTokens)
	}
}

func TestBudgets_QuickProfileBudgetsOverride(t *testing.T) {
	t.Parallel()
	// The preset lowers the author's maximum and the reviewer's; the quick
	// profile lowers the reviewer's further and leaves the author's alone.
	roster := budgetsRoster(budgetsCfg{tpbm: "1.0", extra: `
[presets.small]
mode = "review"
author = "author"
required_reviewers = ["reviewer"]
[presets.small.budgets]
author_max_output_tokens = 1000
reviewer_max_output_tokens = 512

[quick.q]
preset = "small"
[quick.q.budgets]
reviewer_max_output_tokens = 256
`})
	s, c := budgetsStart(t, roster)
	bpt := budget.EffectiveBytesPerToken(enginetest.DefaultCalibration, 0.75)

	// Without the profile the preset's values govern.
	plain, plainTurn := budgetsRunTurn(t, s, c, "s1", "small", "")
	wantTurn(t, plainTurn, engine.StateApproved, "all_approved")
	s.budgetsWantOutputs(t, plain.TurnID, 1000, 512)
	if want := budgetsProjection(budgetsInput, 1000, 512, 64, bpt); plain.EstimatedTokens != int64(want) {
		t.Fatalf("projection under the preset is %d, want %d", plain.EstimatedTokens, want)
	}

	// With it, its value replaces the preset's where both set one.
	quick, quickTurn := budgetsRunTurn(t, s, c, "s2", "", "q")
	wantTurn(t, quickTurn, engine.StateApproved, "all_approved")
	if quickTurn.QuickProfile == nil || *quickTurn.QuickProfile != "q" || quickTurn.Preset != "small" {
		t.Fatalf("turn carries preset %q quick profile %v, want small and q", quickTurn.Preset, quickTurn.QuickProfile)
	}
	s.budgetsWantOutputs(t, quick.TurnID, 1000, 256)
	want := budgetsProjection(budgetsInput, 1000, 256, 64, bpt)
	if quick.EstimatedTokens != int64(want) || *quickTurn.Budget.Projected != int64(want) {
		t.Fatalf("projection under preset and profile is %d (turn %d), want %d", quick.EstimatedTokens, budgetsInt(quickTurn.Budget.Projected), want)
	}
	if one := budgetsOne(t, quickTurn, "reviewer", "review"); one.MaxOutputTokens != 256 {
		t.Fatalf("reviewer attempt records MaxOutputTokens %d, want 256", one.MaxOutputTokens)
	}
}

func TestBudgets_UsageStored(t *testing.T) {
	t.Parallel()
	authorUsage := &transport.Usage{Prompt: 77, Completion: 8}
	reviewUsage := &transport.Usage{Prompt: 1234, Completion: 56}
	s := newSuite(t, budgetsOK(), map[string][]fake.Step{
		"author":   budgetsScript(4, budgetsStep(budgetsReplyText, "", authorUsage)),
		"reviewer": budgetsScript(4, budgetsStep(budgetsReplyText, "", reviewUsage)),
	})
	c := s.Dial(t)
	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "")
	turn := s.budgetsSettled(t, c, reply.TurnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	for _, w := range []struct {
		participant, phase string
		usage              *transport.Usage
	}{{"author", "author", authorUsage}, {"reviewer", "review", reviewUsage}} {
		a := budgetsOne(t, turn, w.participant, w.phase)
		if a.Usage == nil || a.Usage.PromptTokens == nil || *a.Usage.PromptTokens != int64(w.usage.Prompt) ||
			a.Usage.CompletionTokens == nil || *a.Usage.CompletionTokens != int64(w.usage.Completion) {
			t.Fatalf("%s attempt shows usage %+v, want prompt %d completion %d", w.participant, a.Usage, w.usage.Prompt, w.usage.Completion)
		}
	}
	// The journal holds the same, and the deliveries, which reported none,
	// hold none.
	rows, err := s.Store.ListAttempts(reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	stored := 0
	for _, r := range rows {
		if r.UsagePrompt != nil || r.UsageCompletion != nil {
			stored++
			if r.Phase == "terminal_delivery" {
				t.Fatalf("delivery attempt %s stored usage it never reported", r.ID)
			}
		}
	}
	if stored != 2 {
		t.Fatalf("%d attempt rows hold usage, want the author's and the reviewer's", stored)
	}

	// The turn shows the totals: reported across attempts, and the estimates
	// of every attempt's prompt.
	if turn.ReportedUsage == nil || turn.ReportedUsage.PromptTokens == nil || *turn.ReportedUsage.PromptTokens != 77+1234 ||
		turn.ReportedUsage.CompletionTokens == nil || *turn.ReportedUsage.CompletionTokens != 8+56 {
		t.Fatalf("turn reported usage = %+v, want prompt %d completion %d", turn.ReportedUsage, 77+1234, 8+56)
	}
	var estimated int64
	for _, a := range turn.Attempts {
		estimated += a.EstimatedPromptTokens
	}
	if estimated == 0 || turn.EstimatedPromptTokens != estimated {
		t.Fatalf("turn estimated total = %d, want the %d the attempts sum to", turn.EstimatedPromptTokens, estimated)
	}
}

func TestBudgets_MeteredEndpointRefused(t *testing.T) {
	t.Parallel()
	s, c := budgetsStart(t, budgetsRoster(budgetsCfg{tpbm: "1.0", billing: "metered"}))
	_, err := s.budgetsTrySubmit(c, "s1", budgetsInput, "", "")
	we := budgetsRefusal(t, err, "spending_ceiling")
	if we.Participant == nil || *we.Participant != "author" {
		t.Fatalf("spending_ceiling names participant %s, want author", str(we.Participant))
	}
	s.budgetsWantNoTurn(t, c)

	// Flat, none and unknown billing are not affected.
	for _, billing := range []string{"flat", "unknown"} {
		s2, c2 := budgetsStart(t, budgetsRoster(budgetsCfg{tpbm: "1.0", billing: billing}))
		reply := s2.budgetsSubmit(t, c2, "s1", budgetsInput, "", "")
		wantTurn(t, s2.budgetsSettled(t, c2, reply.TurnID), engine.StateApproved, "all_approved")
	}
}

func TestBudgets_DeliveryBoundNotFit(t *testing.T) {
	t.Parallel()
	// The author's context holds its own prompt and what the projection
	// stands in for a reviewer's output, 128 tokens of 3 bytes. The reviewer's
	// real output is 8000 bytes, and the delivery of it to the author no
	// longer fits.
	commentary := strings.Repeat("c", 8000)
	roster := budgetsRoster(budgetsCfg{
		tpbm:    "1.0",
		budgets: "author_max_output_tokens = 256\nreviewer_max_output_tokens = 128",
	})
	s := newSuite(t, roster, map[string][]fake.Step{
		"author":   budgetsScript(4),
		"reviewer": budgetsScript(4, budgetsStep(budgetsReplyText, commentary, nil)),
	}, enginetest.WithContextBound("author", 6000))
	c := s.Dial(t)
	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "")
	turn := s.budgetsSettled(t, c, reply.TurnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")

	usable := budget.UsableBound(6000, 0.1)
	delivery := prompt.Delivery(prompt.DeliveryInput{
		TerminalState: engine.StateApproved, TerminalReason: "all_approved", CandidateHash: strings.Repeat("0", 64),
		ReviewerOutputs: []prompt.ReviewerOutput{{Participant: "reviewer", Output: commentary}},
	})
	if adm := budget.AdmissionTokens(delivery, 1.0, budgetsOverheads) + 64; adm <= usable {
		t.Fatalf("premise: the delivery's admission %d fits the author's usable bound %d", adm, usable)
	}

	toAuthor := budgetsOne(t, turn, "author", "terminal_delivery")
	if toAuthor.TransportStatus != "not_sent:budget" {
		t.Fatalf("delivery to the author has transport status %q, want not_sent:budget", toAuthor.TransportStatus)
	}
	if n := len(s.budgetsCalls("author", reply.TurnID, "terminal_delivery")); n != 0 {
		t.Fatalf("the author was sent %d deliveries that do not fit, want none", n)
	}
	// The other delivery was sent, and carries the commentary.
	toReviewer := budgetsOne(t, turn, "reviewer", "terminal_delivery")
	if toReviewer.TransportStatus != transport.TransportStatusOK || toReviewer.Status != string(transport.StatusComplete) {
		t.Fatalf("delivery to the reviewer = %+v, want it sent and complete", toReviewer)
	}
	sent := s.budgetsCalls("reviewer", reply.TurnID, "terminal_delivery")
	if len(sent) != 1 || !strings.Contains(sent[0].Messages[len(sent[0].Messages)-1].Content, commentary) {
		t.Fatalf("reviewer's deliveries = %d, want one carrying the reviewer's output", len(sent))
	}
}

func TestBudgets_BytesPerTokenInManifest(t *testing.T) {
	t.Parallel()
	// The author's calibration comes from its probe record; the reviewer's
	// is declared in the roster, its probe record having none.
	roster := budgetsRoster(budgetsCfg{tpbm: "1.0", reviewerKeys: "bytes_per_token = 2.5"})
	s, c := budgetsStart(t, roster, enginetest.WithCalibration("reviewer", 0))
	m := s.budgetsManifest(t, c)

	if got := budgetsPath(t, m, "probe_records", "author", "calibration", "bytes_per_token"); got != 4.0 {
		t.Fatalf("manifest records the author's calibrated bytes_per_token as %v, want 4.0", got)
	}
	if got := budgetsRosterParticipant(t, m, "reviewer")["bytes_per_token"]; got != 2.5 {
		t.Fatalf("manifest records the reviewer's declared bytes_per_token as %v, want 2.5", got)
	}
	if got := budgetsRosterParticipant(t, m, "author")["bytes_per_token"]; got != nil {
		t.Fatalf("manifest records a declared bytes_per_token %v for the author, which declared none", got)
	}

	st := s.Status(t, c)
	if p := budgetsParticipant(t, st, "author"); p.BytesPerTokenSource != "probe" || p.BytesPerToken == nil || *p.BytesPerToken != 3.0 {
		t.Fatalf("author: bytes_per_token %v from %q, want 3.0 from probe", p.BytesPerToken, p.BytesPerTokenSource)
	}
	if p := budgetsParticipant(t, st, "reviewer"); p.BytesPerTokenSource != "roster" || p.BytesPerToken == nil || *p.BytesPerToken != 2.5 {
		t.Fatalf("reviewer: bytes_per_token %v from %q, want 2.5 from roster", p.BytesPerToken, p.BytesPerTokenSource)
	}
}
