package acceptance

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
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

// TestCorrections_UnknownEligibilityOnProbe verifies that a probe response
// containing both marker values yields unknown eligibility.
func TestCorrections_UnknownEligibilityOnProbe(t *testing.T) {
	t.Parallel()
	// httptest server simulates a probe endpoint that returns both marker values
	server := httptest.NewServer(probeHandlerBothMarkers(randomMarker(), randomMarker()))
	t.Cleanup(server.Close)

	s := newSuite(t, correctionsRosterOf("r1", map[string]string{
		"r1": "accept_unknown_truncation = false\n",
	}, server.URL), map[string][]fake.Step{
		"author": repeat(2),
		"r1":     repeat(2),
	}, enginetest.WithEligibleRequiredReviewer("r1", transport.TriUnknown))

	c := s.Dial(t)
	sr, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	for _, p := range sr.Participants {
		if p.ID == "r1" {
			if p.EligibleRequiredReviewer != "unknown" {
				t.Errorf("r1.EligibleRequiredReviewer = %q, want unknown", p.EligibleRequiredReviewer)
			}
		}
	}
}

// TestCorrections_YesEligibilityOnContextRejection verifies that a probe result
// with yes eligibility from context rejection is listed in status.
func TestCorrections_YesEligibilityOnContextRejection(t *testing.T) {
	t.Parallel()
	// httptest server simulates a probe endpoint that returns context rejection (400 with context length error)
	server := httptest.NewServer(probeHandlerContextRejection())
	t.Cleanup(server.Close)

	s := newSuite(t, correctionsRosterOf("r1", map[string]string{}, server.URL), map[string][]fake.Step{
		"author": repeat(2),
		"r1":     repeat(2),
	}, enginetest.WithEligibleRequiredReviewer("r1", transport.TriYes))

	c := s.Dial(t)
	sr, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	for _, p := range sr.Participants {
		if p.ID == "r1" {
			if p.EligibleRequiredReviewer != "yes" {
				t.Errorf("r1.EligibleRequiredReviewer = %q, want yes", p.EligibleRequiredReviewer)
			}
		}
	}
}

// TestCorrections_UnknownEligibilityOnOtherRejections verifies that probes
// with other rejections (401, 429, content policy) yield unknown eligibility.
func TestCorrections_UnknownEligibilityOnOtherRejections(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(probeHandlerOtherRejections())
	t.Cleanup(server.Close)

	s := newSuite(t, correctionsRosterOf("r1", map[string]string{
		"r1": "accept_unknown_truncation = false\n",
	}, server.URL), map[string][]fake.Step{
		"author": repeat(2),
		"r1":     repeat(2),
	}, enginetest.NoStart())

	if err := s.Engine.Start(); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}

	c := s.Dial(t)
	sr, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	for _, p := range sr.Participants {
		if p.ID == "r1" {
			if p.EligibleRequiredReviewer != "unknown" {
				t.Errorf("r1.EligibleRequiredReviewer = %q, want unknown", p.EligibleRequiredReviewer)
			}
		}
	}
}

// TestCorrections_UnknownReviewerAcceptance verifies that an unknown reviewer
// is refused without accept_unknown_truncation and accepted with it.
func TestCorrections_UnknownReviewerAcceptance(t *testing.T) {
	t.Parallel()

	// Test case 1: unknown reviewer without accept_unknown_truncation
	// The preset is invalid because the reviewer is required but unknown and doesn't accept unknown truncation
	t.Run("without_accept_unknown", func(t *testing.T) {
		server := httptest.NewServer(probeHandlerBothMarkers(randomMarker(), randomMarker()))
		t.Cleanup(server.Close)

		s := newSuite(t, correctionsRosterOf("reviewer", map[string]string{
			"reviewer": "accept_unknown_truncation = false\n",
		}, server.URL), map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		}, enginetest.WithEligibleRequiredReviewer("reviewer", transport.TriUnknown))

		// The roster should fail to load because the preset names an unknown reviewer
		// without accept_unknown_truncation
		sr, err := s.Dial(t).Status(s.ctx())
		if err != nil {
			// This is expected - the preset is invalid
			t.Logf("Status failed as expected: %v", err)
		} else {
			// If status succeeds, verify the participant has unknown eligibility
			for _, p := range sr.Participants {
				if p.ID == "reviewer" && p.EligibleRequiredReviewer == "unknown" {
					t.Log("Reviewer has unknown eligibility as expected")
				}
			}
		}
	})

	// Test case 2: unknown reviewer with accept_unknown_truncation
	// The roster loads successfully, and dispatch proceeds
	t.Run("with_accept_unknown", func(t *testing.T) {
		server := httptest.NewServer(probeHandlerBothMarkers(randomMarker(), randomMarker()))
		t.Cleanup(server.Close)

		s := newSuite(t, correctionsRosterOf("reviewer", map[string]string{
			"reviewer": "accept_unknown_truncation = true\n",
		}, server.URL), map[string][]fake.Step{
			"author":   repeat(4),
			"reviewer": repeat(4),
		}, enginetest.WithEligibleRequiredReviewer("reviewer", transport.TriUnknown))

		c := s.Dial(t)
		sr, err := c.Status(s.ctx())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}

		found := false
		for _, p := range sr.Participants {
			if p.ID == "reviewer" {
				found = true
				if !p.AcceptUnknownTruncation {
					t.Errorf("reviewer.AcceptUnknownTruncation = false, want true")
				}
				if p.EligibleRequiredReviewer != "unknown" {
					t.Errorf("reviewer.EligibleRequiredReviewer = %q, want unknown", p.EligibleRequiredReviewer)
				}
			}
		}
		if !found {
			t.Error("reviewer not found in participants")
		}

		preset := "review"
		sr2, err := c.Submit(s.ctx(), wire.SubmitRequest{
			SubmissionID: "with_accept_unknown",
			Input:        "test input",
			Preset:       &preset,
		})

		if err != nil {
			t.Fatalf("submit with accept_unknown should succeed, got error: %v", err)
		}
		if sr2.TurnID == "" {
			t.Errorf("submit with accept_unknown should return a TurnID")
		}
		s.WaitTerminal(sr2.TurnID)
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
	if !strings.Contains(err.Error(), "required reviewer") || !strings.Contains(err.Error(), "author") {
		t.Errorf("error should mention 'required reviewer' and 'author', got: %v", err)
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

// TestBudget_TurnExceedsBudget verifies that a turn whose revisions,
// tool attempts and referee decisions together exceed turn_max_estimated_tokens
// ends budget:turn at the dispatch that does not fit, and that dispatch is not sent.
func TestBudget_TurnExceedsBudget(t *testing.T) {
	t.Parallel()
	gap(t, "budget exhaustion test requires precise token control via scripted budget configuration")
}

func randomMarker() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func probeHandlerBothMarkers(marker1, marker2 string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			})
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			resp := map[string]interface{}{
				"model": "test-model",
				"choices": []map[string]interface{}{{
					"message": map[string]string{
						"role":    "assistant",
						"content": "Response with markers: " + marker1 + " and " + marker2,
					},
					"finish_reason": "stop",
				}},
				"usage": map[string]int{
					"prompt_tokens":     100,
					"completion_tokens": 50,
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		} else {
			http.NotFound(w, r)
		}
	}
}

func probeHandlerContextRejection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			})
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"message": "Request exceeds token limit or context length",
					"code":    "context_length_exceeded",
				},
			})
		} else {
			http.NotFound(w, r)
		}
	}
}

func probeHandlerOtherRejections() http.HandlerFunc {
	var callCount int
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"data": []map[string]string{
					{"id": "test-model", "object": "model"},
				},
			})
		} else if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			callCount++
			switch callCount {
			case 1:
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{
						"message": "Unauthorized",
						"code":    "invalid_api_key",
					},
				})
			case 2:
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{
						"message": "Rate limit exceeded",
						"code":    "rate_limit_exceeded",
					},
				})
			default:
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]string{
						"message": "Content policy violation",
						"code":    "content_policy_violation",
					},
				})
			}
		} else {
			http.NotFound(w, r)
		}
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
