package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/transport/openai"
	"github.com/boldfield/syndic/internal/wire"
)

const correctionsAndBudgetPara = "Corrections and Budget"

func init() {
	cover(correctionsAndBudgetPara, "a reply containing both marker values in any surrounding text yields `unknown` eligibility when probed", "TestCorrections_UnknownEligibilityOnProbe")
	cover(correctionsAndBudgetPara, "a 400 whose body names the context length yields `yes` eligibility when probed", "TestCorrections_YesEligibilityOnContextRejection")
	cover(correctionsAndBudgetPara, "a 401, a 429 and a 400 whose body names content policy each yield `unknown` eligibility when probed", "TestCorrections_UnknownEligibilityOnOtherRejections")
	cover(correctionsAndBudgetPara, "no probe outcome yields `no` eligibility", "TestCorrections_NoOutcomeYieldsNo")
	cover(correctionsAndBudgetPara, "the over-length prompt's estimated size is at least 1.5 times the assumed context", "TestCorrections_OverLengthPromptSize")
	cover(correctionsAndBudgetPara, "unknown eligibility is refused without `accept_unknown_truncation` and accepted with it", "TestCorrections_UnknownReviewerAcceptance")
	cover(correctionsAndBudgetPara, "a roster whose preset names its author as a required reviewer is refused at load", "TestCorrections_AuthorAsReviewerRefusedAtLoad")

	cover(correctionsAndBudgetPara, "a participant on a `metered` endpoint without `spending_control = \"external\"` is refused with `spending_ceiling`", "TestBudget_MeteredWithoutSpendingControlRefused")
	cover(correctionsAndBudgetPara, "a participant on a `metered` endpoint with `spending_control = \"external\"` dispatches and the manifest records the setting", "TestBudget_MeteredWithSpendingControlAccepted")
	// Tool attempts and referee decisions are not in the engine yet, so only
	// the revision round is registered.
	cover(correctionsAndBudgetPara, "a turn whose revision rounds exceed `turn_max_estimated_tokens` ends `budget:turn` at the dispatch that does not fit, and that dispatch is not sent", "TestBudget_RevisionRoundExceedsTurnBudget")
	cover(correctionsAndBudgetPara, "a metered endpoint with `spending_control = \"external\"` keeps `turn_max_estimated_tokens` as its bound and ends `budget:turn`", "TestBudget_MeteredExternalKeepsTurnBudget")
}

var probeHexMarker = regexp.MustCompile(`[0-9a-f]{32}`)

// probeOverLength answers the over-length request of a probe: it is handed
// the markers the probe put in the prompt and returns the status and body.
type probeOverLength func(markers []string) (status int, body string)

// probeCompletion is the 2xx body of a chat completion with the content.
func probeCompletion(content string) string {
	b, _ := json.Marshal(map[string]any{
		"model": "m1",
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
	})
	return string(b)
}

// probeEndpoint runs the real openai adapter's probe against an httptest
// endpoint that answers every step normally except the over-length one, which
// over answers, and returns the record the probe produced.
//
// It also returns the byte size of the over-length prompt the probe sent, or
// zero when none was sent.
func probeEndpoint(t *testing.T, over probeOverLength, opts transport.ProbeOptions) (transport.CapabilityRecord, int) {
	t.Helper()
	var sent atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		var user string
		for _, m := range req.Messages {
			if m.Role != "system" {
				user = m.Content
			}
		}
		if markers := probeHexMarker.FindAllString(user, -1); len(markers) >= 2 {
			sent.Store(int64(len(user)))
			status, body := over(markers)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		promptTokens := 1
		switch {
		case user == "":
			promptTokens = 10
		case len(user) == 4096:
			promptTokens = 1034
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "m1",
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": promptTokens, "completion_tokens": 1},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	a := openai.New(config.EndpointProfile{Name: "probed", BaseURL: srv.URL}, nil)
	if opts.AssumedContextLen == 0 {
		opts.AssumedContextLen = 131072
	}
	rec, err := a.Probe(context.Background(), "probed", "m1", opts)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	return rec, int(sent.Load())
}

// wantProbeOutcome requires the probe's over-length outcome and eligibility.
func wantProbeOutcome(t *testing.T, rec transport.CapabilityRecord, overLength string, eligible transport.Tri) {
	t.Helper()
	if rec.OverLength != overLength {
		t.Errorf("OverLength = %q, want %q", rec.OverLength, overLength)
	}
	if rec.EligibleRequiredReviewer != eligible {
		t.Errorf("EligibleRequiredReviewer = %v, want %v", rec.EligibleRequiredReviewer, eligible)
	}
}

// TestCorrections_UnknownEligibilityOnProbe drives the real probe against an
// httptest endpoint: only a reply that contains both markers, in any
// surrounding text, a reply with one or none, or a probe that never ran the
// over-length step, all leave the eligibility unknown.
func TestCorrections_UnknownEligibilityOnProbe(t *testing.T) {
	t.Parallel()
	t.Run("both_markers_in_surrounding_text", func(t *testing.T) {
		rec, _ := probeEndpoint(t, func(m []string) (int, string) {
			if len(m) < 2 {
				t.Errorf("the over-length prompt carried %d markers, want 2", len(m))
				return 500, ""
			}
			return 200, probeCompletion(fmt.Sprintf("see (%s) between ... and %s!", m[0], m[len(m)-1]))
		}, transport.ProbeOptions{})
		wantProbeOutcome(t, rec, "accepted_whole", transport.TriUnknown)
	})
	t.Run("one_marker", func(t *testing.T) {
		rec, _ := probeEndpoint(t, func(m []string) (int, string) {
			return 200, probeCompletion("only " + m[0])
		}, transport.ProbeOptions{})
		wantProbeOutcome(t, rec, "markers_missing", transport.TriUnknown)
	})
	t.Run("no_markers", func(t *testing.T) {
		rec, _ := probeEndpoint(t, func([]string) (int, string) {
			return 200, probeCompletion("ok")
		}, transport.ProbeOptions{})
		wantProbeOutcome(t, rec, "markers_missing", transport.TriUnknown)
	})
	t.Run("not_run", func(t *testing.T) {
		rec, _ := probeEndpoint(t, func([]string) (int, string) {
			t.Error("the over-length request was sent although that step is disabled")
			return 500, ""
		}, transport.ProbeOptions{DisableOverLength: true})
		wantProbeOutcome(t, rec, "not_run:skipped", transport.TriUnknown)
	})
}

// TestCorrections_YesEligibilityOnContextRejection drives the real probe
// against an endpoint that refuses the over-length prompt with a 400 naming
// the context length: the only way to eligibility yes.
func TestCorrections_YesEligibilityOnContextRejection(t *testing.T) {
	t.Parallel()
	rec, _ := probeEndpoint(t, func([]string) (int, string) {
		return http.StatusBadRequest, `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`
	}, transport.ProbeOptions{})
	wantProbeOutcome(t, rec, "rejected:context", transport.TriYes)
}

// TestCorrections_UnknownEligibilityOnOtherRejections drives the real probe
// once per refusal that does not name the context length.
func TestCorrections_UnknownEligibilityOnOtherRejections(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"401", http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`},
		{"429", http.StatusTooManyRequests, `{"error":{"message":"rate limit reached"}}`},
		{"400_content_policy", http.StatusBadRequest, `{"error":{"message":"blocked by the content policy"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := probeEndpoint(t, func([]string) (int, string) { return tc.status, tc.body }, transport.ProbeOptions{})
			wantProbeOutcome(t, rec, "rejected:other", transport.TriUnknown)
		})
	}
}

// TestCorrections_UnknownReviewerAcceptance takes the eligibility of the
// required reviewer from a real probe whose reply carries both markers, so it
// is unknown, and submits through the engine: refused without
// accept_unknown_truncation and nothing dispatched, dispatched with it.
func TestCorrections_UnknownReviewerAcceptance(t *testing.T) {
	t.Parallel()
	rec, _ := probeEndpoint(t, func(m []string) (int, string) {
		return 200, probeCompletion(strings.Join(m, " "))
	}, transport.ProbeOptions{})
	wantProbeOutcome(t, rec, "accepted_whole", transport.TriUnknown)

	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{"author": budgetsScript(4), "r1": budgetsScript(4)}
	}

	t.Run("without_accept_unknown_truncation", func(t *testing.T) {
		s := newSuite(t, correctionsRosterOf("r1", map[string]string{
			"r1": "accept_unknown_truncation = false\n",
		}), scripts(), enginetest.WithEligibleRequiredReviewer("r1", rec.EligibleRequiredReviewer))
		c := s.Dial(t)
		_, err := s.budgetsTrySubmit(c, "unaccepted", "test input", "review", "")
		var we *wire.Error
		if !errors.As(err, &we) || we.Code != wire.ErrorCodePresetInvalid ||
			we.Detail == nil || *we.Detail != "unknown_required_reviewer_not_accepted:r1" {
			t.Fatalf("submit = %v, want preset_invalid with detail unknown_required_reviewer_not_accepted:r1", err)
		}
		s.budgetsWantNoTurn(t, c)
		for _, id := range []string{"author", "r1"} {
			if n := len(s.Adapters[id].Calls()); n != 0 {
				t.Errorf("%s received %d requests of a refused submit", id, n)
			}
		}
	})

	t.Run("with_accept_unknown_truncation", func(t *testing.T) {
		s := newSuite(t, correctionsRosterOf("r1", map[string]string{
			"r1": "accept_unknown_truncation = true\n",
		}), scripts(), enginetest.WithEligibleRequiredReviewer("r1", rec.EligibleRequiredReviewer))
		c := s.Dial(t)
		reply := s.budgetsSubmit(t, c, "accepted", "test input", "review", "")
		turn := s.budgetsSettled(t, c, reply.TurnID)
		wantTurn(t, turn, engine.StateApproved, "all_approved")
		if len(budgetsAttempts(turn, "r1", "review")) == 0 || len(s.budgetsCalls("r1", reply.TurnID, "review")) == 0 {
			t.Errorf("the unknown reviewer was not dispatched: %+v", turn.Attempts)
		}
	})
}

// TestCorrections_AuthorAsReviewerRefusedAtLoad verifies that a roster whose
// preset names its author as a required reviewer is refused at load.
func TestCorrections_AuthorAsReviewerRefusedAtLoad(t *testing.T) {
	t.Parallel()
	invalidRoster := `default_preset = "review"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

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
required_reviewers = ["author"]

[pools.default]
concurrency = 1
`

	h := enginetest.New(t, invalidRoster, map[string][]fake.Step{
		"author": repeat(2),
	}, enginetest.NoStart())

	err := h.Engine.Start()
	if err == nil {
		t.Fatal("engine start should fail when author is named as required reviewer")
	}
	if want := `preset "review": author "author" is also in required reviewers`; !strings.Contains(err.Error(), want) {
		t.Errorf("start error = %v, want it to contain %q", err, want)
	}
}

// TestBudget_MeteredWithoutSpendingControlRefused verifies that a participant
// on a metered endpoint without spending_control = "external" is refused.
func TestBudget_MeteredWithoutSpendingControlRefused(t *testing.T) {
	t.Parallel()
	roster := `default_preset = "review"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "metered"
eligible_roles = ["author"]
pool = "default"
accept_unknown_truncation = true

[[participants]]
id = "r1"
model = "llama3"
route = "api"
endpoint = "metered"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]
name = "metered"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "metered"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["r1"]

[pools.default]
concurrency = 1
`

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": repeat(2),
		"r1":     repeat(2),
	})

	c := s.Dial(t)
	preset := "review"
	sr, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "metered_no_control",
		Input:        "test input",
		Preset:       &preset,
	})
	if err == nil {
		t.Fatalf("submit with metered endpoint should fail, got TurnID: %v", sr.TurnID)
	}
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.ErrorCodeBudget || we.Budget == nil || *we.Budget != "spending_ceiling" {
		t.Fatalf("submit should refuse with budget:spending_ceiling, got: %v", err)
	}
}

// TestBudget_MeteredWithSpendingControlAccepted verifies that with
// spending_control = "external" on a metered endpoint, the participant
// dispatches, the manifest records the setting, and dispatch happens.
func TestBudget_MeteredWithSpendingControlAccepted(t *testing.T) {
	t.Parallel()
	roster := `default_preset = "review"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "metered"
eligible_roles = ["author"]
pool = "default"
accept_unknown_truncation = true

[[participants]]
id = "r1"
model = "llama3"
route = "api"
endpoint = "metered"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

[[endpoints]]
name = "metered"
base_url = "http://localhost:11434/v1"
declared_location = "local"
declared_execution = "local"
declared_billing = "metered"
network_dependency = "none"
dialect = "chat_completions_v1"
models = ["llama2", "llama3"]
tokens_per_byte_max = 1.0
spending_control = "external"

[presets.review]
mode = "review"
author = "author"
required_reviewers = ["r1"]

[pools.default]
concurrency = 1
`

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": repeat(2),
		"r1":     repeat(2),
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "test input")
	s.WaitTerminal(turnID)

	sr, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	found := false
	for _, p := range sr.Participants {
		if p.ID == "r1" {
			found = true
			if p.SpendingControl != "external" {
				t.Errorf("r1.SpendingControl = %q, want external", p.SpendingControl)
			}
			break
		}
	}
	if !found {
		t.Error("r1 not found in participants")
	}

	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateApproved {
		t.Errorf("dispatch with external spending control: turn state = %q, want approved", turn.State)
	}

	// Check that the manifest records spending_control and declared_billing per endpoint
	run, err := s.Store.GetRun(sr.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(run.Manifest, &m); err != nil {
		t.Fatalf("manifest parse: %v", err)
	}

	// Navigate to roster.endpoints in the manifest
	rosterObj, ok := m["roster"].(map[string]any)
	if !ok {
		t.Fatal("manifest has no roster")
	}
	endpoints, ok := rosterObj["endpoints"].([]any)
	if !ok {
		t.Fatalf("manifest roster endpoints is not a list")
	}

	var found2 bool
	for _, ep := range endpoints {
		epMap, ok := ep.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := epMap["name"].(string); ok && name == "metered" {
			found2 = true
			if sc, ok := epMap["spending_control"].(string); !ok || sc != "external" {
				t.Errorf("manifest metered.spending_control = %q, want external", sc)
			}
			if db, ok := epMap["declared_billing"].(string); !ok || db != "metered" {
				t.Errorf("manifest metered.declared_billing = %q, want metered", db)
			}
			break
		}
	}
	if !found2 {
		t.Error("metered endpoint not found in manifest")
	}
}

// budgetsBlockingStep blocks every review and answers the author with text,
// reporting heavy usage so that what the turn has charged exceeds what the
// submit projected for it.
func budgetsBlockingStep() fake.Step {
	step := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		if req.Envelope.Phase != "review" {
			return budgetsReplyText
		}
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "evidence", "remedy", "reason")
	})
	step.Usage = &transport.Usage{Prompt: 10000, Completion: 100}
	return step
}

// budgetsRevisionRefused runs a review turn on the roster whose reviewer
// blocks the first candidate, with a limit that is exactly what the submit
// projected for the first round, so that the revision round cannot fit. It
// requires the turn to end budget:turn at the revision, with the author never
// sent the revision.
func budgetsRevisionRefused(t *testing.T, cfg budgetsCfg) {
	t.Helper()
	scripts := func() map[string][]fake.Step {
		return map[string][]fake.Step{
			"author":   {budgetsBlockingStep(), budgetsPlain(), budgetsPlain()},
			"reviewer": {budgetsBlockingStep(), budgetsPlain(), budgetsPlain()},
		}
	}
	cfg.extra += "\n[revision]\nmax_rounds = 2\n"

	probe := cfg
	probe.budgets = "turn_max_estimated_tokens = 1000000"
	ps := newSuite(t, budgetsRoster(probe), scripts())
	first := ps.budgetsSubmit(t, ps.Dial(t), "probe", budgetsInput, "", "").EstimatedTokens

	cfg.budgets = fmt.Sprintf("turn_max_estimated_tokens = %d", first)
	s := newSuite(t, budgetsRoster(cfg), scripts())
	c := s.Dial(t)
	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "")
	if reply.EstimatedTokens != first {
		t.Fatalf("the projection is %d, want %d", reply.EstimatedTokens, first)
	}
	turn := s.budgetsSettled(t, c, reply.TurnID)
	wantTurn(t, turn, engine.StateIncomplete, "budget:turn")

	// The first round ran: the reviewer was sent its review once.
	if n := len(s.budgetsCalls("reviewer", reply.TurnID, "review")); n != 1 {
		t.Fatalf("reviewer received %d review requests, want the first round's one", n)
	}
	// The revision did not fit, so the author was sent nothing after round one.
	for _, call := range s.Adapters["author"].Calls() {
		if env := call.Request.Envelope; env.TurnID == reply.TurnID && env.Phase != "author" && env.Phase != "terminal_delivery" {
			t.Fatalf("the author was sent a %q request after the first round", env.Phase)
		}
	}
	if n := len(s.budgetsCalls("author", reply.TurnID, "author")); n != 1 {
		t.Fatalf("author received %d author requests, want 1: the revision was sent", n)
	}
	over := budgetsEventsOf(s, wire.EventKindBudget, func(wire.EvBudget) bool { return true })
	if len(over) != 1 || over[0].Budget != "turn_max_estimated_tokens" || over[0].Limit != int64(first) {
		t.Fatalf("budget events = %+v, want one turn_max_estimated_tokens refusal at limit %d", over, first)
	}
}

// TestBudget_RevisionRoundExceedsTurnBudget runs a turn that is admitted within
// turn_max_estimated_tokens, whose first round charges more than was projected
// and whose reviewer blocks: the revision round does not fit what is left, the
// turn ends budget:turn and the revision is not sent.
func TestBudget_RevisionRoundExceedsTurnBudget(t *testing.T) {
	t.Parallel()
	budgetsRevisionRefused(t, budgetsCfg{tpbm: "1.0"})
}

// TestBudget_MeteredExternalKeepsTurnBudget runs the same turn on a metered
// endpoint with spending_control = "external", which is admitted as flat: the
// turn budget is its only bound and still ends the turn budget:turn.
func TestBudget_MeteredExternalKeepsTurnBudget(t *testing.T) {
	t.Parallel()
	budgetsRevisionRefused(t, budgetsCfg{tpbm: "1.0\nspending_control = \"external\"", billing: "metered"})
}

// TestCorrections_NoOutcomeYieldsNo drives the real probe through every kind
// of over-length outcome, errored included, and requires that none yields no.
func TestCorrections_NoOutcomeYieldsNo(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		over probeOverLength
		opts transport.ProbeOptions
		want string
	}{
		{"accepted_whole", func(m []string) (int, string) { return 200, probeCompletion(strings.Join(m, " ")) }, transport.ProbeOptions{}, "accepted_whole"},
		{"markers_missing", func([]string) (int, string) { return 200, probeCompletion("ok") }, transport.ProbeOptions{}, "markers_missing"},
		{"rejected_context", func([]string) (int, string) {
			return http.StatusBadRequest, `{"error":{"code":"context_length_exceeded","message":"too long"}}`
		}, transport.ProbeOptions{}, "rejected:context"},
		{"rejected_other", func([]string) (int, string) { return http.StatusUnauthorized, `{}` }, transport.ProbeOptions{}, "rejected:other"},
		{"errored", func([]string) (int, string) { return 200, "this is not json" }, transport.ProbeOptions{}, "errored"},
		{"not_run", func([]string) (int, string) { return 500, "" }, transport.ProbeOptions{DisableOverLength: true}, "not_run:skipped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := probeEndpoint(t, tc.over, tc.opts)
			if rec.OverLength != tc.want {
				t.Fatalf("OverLength = %q, want %q", rec.OverLength, tc.want)
			}
			if rec.EligibleRequiredReviewer == transport.TriNo {
				t.Errorf("outcome %q yields eligibility no", rec.OverLength)
			}
			if tc.want != "rejected:context" && rec.EligibleRequiredReviewer != transport.TriUnknown {
				t.Errorf("outcome %q yields %v, want unknown", rec.OverLength, rec.EligibleRequiredReviewer)
			}
		})
	}
}

// TestCorrections_OverLengthPromptSize requires the over-length prompt the
// probe really sends to be estimated at least 1.5 times the assumed context.
func TestCorrections_OverLengthPromptSize(t *testing.T) {
	t.Parallel()
	for _, assumed := range []int{4096, 131072} {
		rec, size := probeEndpoint(t, func([]string) (int, string) {
			return http.StatusBadRequest, `{"error":{"message":"maximum context length exceeded"}}`
		}, transport.ProbeOptions{AssumedContextLen: assumed})
		if size == 0 {
			t.Fatalf("assumed %d: no over-length prompt was sent", assumed)
		}
		if rec.Calibration.BytesPerToken <= 0 {
			t.Fatalf("assumed %d: calibration %+v has no bytes per token", assumed, rec.Calibration)
		}
		estimated := float64(size) / rec.Calibration.BytesPerToken
		if want := 1.5 * float64(assumed); estimated < want {
			t.Errorf("assumed %d: the prompt of %d bytes is about %.0f tokens, want at least %.0f", assumed, size, estimated, want)
		}
	}
}

func correctionsRosterOf(reviewerID string, reviewerKeys map[string]string) string {
	var b strings.Builder
	b.WriteString(`default_preset = "review"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "`)
	b.WriteString(reviewerID)
	b.WriteString(`"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"
`)
	if keys, ok := reviewerKeys[reviewerID]; ok {
		b.WriteString(keys)
	}
	b.WriteString(`
[[endpoints]]
name = "local"
base_url = "`)
	b.WriteString("http://localhost:11434/v1")
	b.WriteString(`"
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
required_reviewers = ["`)
	b.WriteString(reviewerID)
	b.WriteString(`"]

[pools.default]
concurrency = 1
`)
	return b.String()
}
