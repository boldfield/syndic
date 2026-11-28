package engine_test

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/tools"
	"github.com/boldfield/syndic/internal/transport"
)

func deliberateRoster(extra string) string {
	toml := strings.ReplaceAll(enginetest.RosterTOML, `eligible_roles = ["author"]`, `eligible_roles = ["author", "reviewer"]`)
	toml = strings.ReplaceAll(toml, `eligible_roles = ["reviewer"]`, `eligible_roles = ["author", "reviewer"]`)
	return toml + `
[presets.deliberate]
mode = "deliberate"
proposers = ["author", "reviewer"]
integrator = "author"
` + extra
}

func TestDeliberateProjectionListsEveryPhase(t *testing.T) {
	h := enginetest.New(t, deliberateRoster(""), nil)
	reply, err := h.Engine.ProjectBudget("deliberate", testInput)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	sum := int64(0)
	for _, p := range reply.Phases {
		got = append(got, p.Phase+":"+p.Participant)
		sum += p.EstimatedTokens + p.MaxOutputTokens
	}
	want := []string{
		"proposal:author", "proposal:reviewer",
		"critique:author", "critique:reviewer",
		"integration:author", "integration_repair:author",
		"review:reviewer", "review_repair:reviewer",
		"terminal_delivery:author", "terminal_delivery:reviewer",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	if sum != reply.EstimatedTokens {
		t.Fatalf("phases sum %d != projected %d", sum, reply.EstimatedTokens)
	}
}

func TestDeliberateIntegrationOverContextRefused(t *testing.T) {
	// The integration prompt carries both proposals and critiques, so it is
	// the first prompt to pass a bound the proposal and critique prompts fit.
	h := enginetest.New(t, deliberateRoster(withBudgets("", "integrator_max_output_tokens = 4096", "proposer_max_output_tokens = 15000", "critique_max_output_tokens = 15000")),
		nil)
	_, err := h.Engine.ProjectBudget("deliberate", testInput)
	we := wantBudgetError(t, err, "context")
	if we.Participant == nil || *we.Participant != "author" || we.Phase == nil || *we.Phase != "integration" {
		t.Fatalf("error = %+v phase %v, want participant author, phase integration", we, we.Phase)
	}
}

// The definitions a phase offers are part of its rendered requests: the
// preset's reviewer_tools in proposal, critique and review, its tools in
// integration, and none in a repair or a delivery.
func TestDeliberateProjectionCarriesPhaseTools(t *testing.T) {
	bare := enginetest.New(t, deliberateRoster(""), nil)
	plain, err := bare.Engine.ProjectBudget("deliberate", testInput)
	if err != nil {
		t.Fatal(err)
	}
	toml := withPresetTools(t, deliberateRoster(""), "deliberate", []string{tools.GroupRead, tools.GroupWrite}, []string{tools.GroupRead})
	h := enginetest.New(t, toml, nil, enginetest.WithToolCalls("author", transport.TriYes), enginetest.WithToolCalls("reviewer", transport.TriYes))
	withTools, err := h.Engine.ProjectBudget("deliberate", testInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(withTools.Phases) != len(plain.Phases) {
		t.Fatalf("phases %d, want %d", len(withTools.Phases), len(plain.Phases))
	}
	reviewer := int64(definitionsEstimate(definitions(t, tools.GroupRead)))
	author := int64(definitionsEstimate(definitions(t, tools.GroupRead, tools.GroupWrite)))
	want := map[string]int64{"proposal": reviewer, "critique": reviewer, "review": reviewer, "integration": author}
	for i, p := range withTools.Phases {
		if got := p.EstimatedTokens - plain.Phases[i].EstimatedTokens; got != want[p.Phase] {
			t.Errorf("%s %s: definitions add %d tokens, want %d", p.Phase, p.Participant, got, want[p.Phase])
		}
	}
}
