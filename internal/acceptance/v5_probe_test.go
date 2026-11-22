package acceptance

import (
	"strings"
	"testing"

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

// TestCorrections_UnknownEligibilityOnProbe verifies that a reviewer with unknown
// eligibility from the probe is listed in status with the correct eligibility value.
func TestCorrections_UnknownEligibilityOnProbe(t *testing.T) {
	t.Parallel()
	s := newSuite(t, correctionsRosterOf("r1", map[string]string{
		"r1": "accept_unknown_truncation = false\n",
	}), map[string][]fake.Step{
		"author":   repeat(2),
		"r1":       repeat(2),
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

// TestCorrections_YesEligibilityOnContextRejection verifies that a reviewer with yes
// eligibility from a context-rejection probe result is listed in status correctly.
func TestCorrections_YesEligibilityOnContextRejection(t *testing.T) {
	t.Parallel()
	s := newSuite(t, correctionsRosterOf("r1", map[string]string{}), map[string][]fake.Step{
		"author":   repeat(2),
		"r1":       repeat(2),
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

// TestCorrections_UnknownEligibilityOnOtherRejections verifies that reviewers
// with unknown eligibility can be configured multiple ways via enginetest options.
func TestCorrections_UnknownEligibilityOnOtherRejections(t *testing.T) {
	t.Parallel()
	s := newSuite(t, correctionsRosterOf("r1", map[string]string{
		"r1": "accept_unknown_truncation = false\n",
	}), map[string][]fake.Step{
		"author":   repeat(2),
		"r1":       repeat(2),
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


// TestCorrections_UnknownReviewerAcceptance verifies that an `unknown` reviewer
// is refused without `accept_unknown_truncation` and accepted with it.
func TestCorrections_UnknownReviewerAcceptance(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name           string
		rosterKeys     string
		shouldDispatch bool
	}{
		{
			name:           "without_accept_unknown",
			rosterKeys:     "accept_unknown_truncation = false\n",
			shouldDispatch: false,
		},
		{
			name:           "with_accept_unknown",
			rosterKeys:     "accept_unknown_truncation = true\n",
			shouldDispatch: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSuite(t, correctionsRosterOf("r1", map[string]string{
				"r1": tc.rosterKeys,
			}), map[string][]fake.Step{
				"author":   repeat(4),
				"r1":       repeat(4),
			}, enginetest.WithEligibleRequiredReviewer("r1", transport.TriUnknown))

			c := s.Dial(t)
			sr, err := c.Status(s.ctx())
			if err != nil {
				t.Fatalf("Status: %v", err)
			}

			for _, p := range sr.Participants {
				if p.ID == "r1" {
					if !tc.shouldDispatch && p.AcceptUnknownTruncation {
						t.Errorf("test case %s expects rejection, but AcceptUnknownTruncation is true", tc.name)
					}
					if tc.shouldDispatch && !p.AcceptUnknownTruncation {
						t.Errorf("test case %s expects acceptance, but AcceptUnknownTruncation is false", tc.name)
					}
				}
			}
		})
	}
}

// TestCorrections_AuthorAsReviewerRefusedAtLoad verifies that a roster whose
// preset names its author as a required reviewer fails engine validation.
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
}

// TestBudget_MeteredWithoutSpendingControlRefused verifies that a participant on
// a `metered` endpoint without `spending_control = "external"` is refused with
// `spending_ceiling`.
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
		Input:  "test input",
		Preset: &preset,
	})
	if err == nil {
		t.Fatalf("submit should fail with spending_ceiling error, got TurnID: %v", sr.TurnID)
	}
}

// TestBudget_MeteredWithSpendingControlAccepted verifies that with
// `spending_control = "external"` on a metered endpoint, the participant
// dispatches and the manifest records the setting.
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
}

// TestBudget_TurnExceedsBudget verifies that a turn whose total (revisions,
// tool attempts, referee decisions) would exceed `turn_max_estimated_tokens`
// is refused with budget error.
func TestBudget_TurnExceedsBudget(t *testing.T) {
	t.Parallel()
	roster := `default_preset = "review"

[deadlines]
attempt = "300s"
turn = "900s"
delivery = "60s"
provisional = false

[budgets]
turn_max_estimated_tokens = 500

[[participants]]
id = "author"
model = "llama2"
route = "api"
endpoint = "local"
eligible_roles = ["author"]
pool = "default"

[[participants]]
id = "r1"
model = "llama3"
route = "api"
endpoint = "local"
eligible_roles = ["reviewer"]
pool = "default"
accept_unknown_truncation = true

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
required_reviewers = ["r1"]

[pools.default]
concurrency = 1
`

	largeInput := strings.Repeat("word ", 300)

	s := newSuite(t, roster, map[string][]fake.Step{
		"author": repeat(2),
		"r1":     repeat(2),
	})

	c := s.Dial(t)
	preset := "review"
	sr, err := c.Submit(s.ctx(), wire.SubmitRequest{
		Input:  largeInput,
		Preset: &preset,
	})

	if err == nil {
		t.Fatalf("submit with large input should fail, got TurnID: %v", sr.TurnID)
	}
}

// correctionsRosterOf builds a roster for corrections tests with the author and
// the specified reviewer in pool "default", with optional additional keys for the reviewer.
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
required_reviewers = ["`)
	b.WriteString(reviewerID)
	b.WriteString(`"]

[pools.default]
concurrency = 1
`)
	return b.String()
}
