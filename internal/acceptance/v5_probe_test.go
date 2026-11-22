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
	cover(correctionsAndBudgetPara, "unknown eligibility is refused without `accept_unknown_truncation` and accepted with it", "TestCorrections_UnknownReviewerAcceptance")
	cover(correctionsAndBudgetPara, "a roster whose preset names its author as a required reviewer is refused at load", "TestCorrections_AuthorAsReviewerRefusedAtLoad")

	cover(correctionsAndBudgetPara, "a participant on a `metered` endpoint without `spending_control = \"external\"` is refused with `spending_ceiling`", "TestBudget_MeteredWithoutSpendingControlRefused")
	cover(correctionsAndBudgetPara, "a participant on a `metered` endpoint with `spending_control = \"external\"` dispatches and the manifest records the setting", "TestBudget_MeteredWithSpendingControlAccepted")
	cover(correctionsAndBudgetPara, "a turn whose revisions, tool attempts and referee decisions exceed `turn_max_estimated_tokens` ends `budget:turn`", "TestBudget_TurnExceedsBudget")
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
func probeEndpoint(t *testing.T, over probeOverLength, opts transport.ProbeOptions) transport.CapabilityRecord {
	t.Helper()
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
		if len(user) > 100000 {
			status, body := over(probeHexMarker.FindAllString(user, -1))
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
	return rec
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
		rec := probeEndpoint(t, func(m []string) (int, string) {
			if len(m) < 2 {
				t.Errorf("the over-length prompt carried %d markers, want 2", len(m))
				return 500, ""
			}
			return 200, probeCompletion(fmt.Sprintf("see (%s) between ... and %s!", m[0], m[len(m)-1]))
		}, transport.ProbeOptions{})
		wantProbeOutcome(t, rec, "accepted_whole", transport.TriUnknown)
	})
	t.Run("one_marker", func(t *testing.T) {
		rec := probeEndpoint(t, func(m []string) (int, string) {
			return 200, probeCompletion("only " + m[0])
		}, transport.ProbeOptions{})
		wantProbeOutcome(t, rec, "markers_missing", transport.TriUnknown)
	})
	t.Run("no_markers", func(t *testing.T) {
		rec := probeEndpoint(t, func([]string) (int, string) {
			return 200, probeCompletion("ok")
		}, transport.ProbeOptions{})
		wantProbeOutcome(t, rec, "markers_missing", transport.TriUnknown)
	})
	t.Run("not_run", func(t *testing.T) {
		rec := probeEndpoint(t, func([]string) (int, string) {
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
	rec := probeEndpoint(t, func([]string) (int, string) {
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
			rec := probeEndpoint(t, func([]string) (int, string) { return tc.status, tc.body }, transport.ProbeOptions{})
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
	rec := probeEndpoint(t, func(m []string) (int, string) {
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

// TestBudget_TurnExceedsBudget runs a turn that is admitted within
// turn_max_estimated_tokens: the author's reply is larger than the stand-in the
// projection used, so the review dispatch that follows no longer fits. The turn
// ends budget:turn and the reviewer is never sent that request.
func TestBudget_TurnExceedsBudget(t *testing.T) {
	t.Parallel()
	limit := budgetsDefaultProjection(budgetsInput)
	big := strings.Repeat("z", 90000)
	roster := budgetsRoster(budgetsCfg{tpbm: "1.0", budgets: fmt.Sprintf("turn_max_estimated_tokens = %d", limit)})
	s := newSuite(t, roster, map[string][]fake.Step{
		"author":   budgetsScript(4, budgetsStep(big, "", nil)),
		"reviewer": budgetsScript(4),
	})
	c := s.Dial(t)
	reply := s.budgetsSubmit(t, c, "s1", budgetsInput, "", "")
	if reply.EstimatedTokens != int64(limit) {
		t.Fatalf("the projection is %d, want it exactly at the limit %d", reply.EstimatedTokens, limit)
	}
	turn := s.budgetsSettled(t, c, reply.TurnID)
	wantTurn(t, turn, engine.StateIncomplete, "budget:turn")
	if len(budgetsAttempts(turn, "reviewer", "review")) != 0 || len(s.budgetsCalls("reviewer", reply.TurnID, "")) != 0 {
		t.Fatalf("the reviewer was dispatched past the turn budget: %+v", turn.Attempts)
	}
	over := budgetsEventsOf(s, wire.EventKindBudget, func(wire.EvBudget) bool { return true })
	if len(over) != 1 || over[0].Budget != "turn_max_estimated_tokens" || over[0].Limit != int64(limit) {
		t.Fatalf("budget events = %+v, want one turn_max_estimated_tokens refusal at limit %d", over, limit)
	}
}

func correctionsRosterOf(reviewerID string, reviewerKeys map[string]string, baseURL ...string) string {
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
	if len(baseURL) > 0 {
		b.WriteString(baseURL[0])
	} else {
		b.WriteString("http://localhost:11434/v1")
	}
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
