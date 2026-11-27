package engine

import (
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/config"
)

func effectiveWith(proposer, critique, integrator int) config.Effective {
	return config.Effective{Budgets: config.Budgets{
		ProposerMaxOutputTokens:   &proposer,
		CritiqueMaxOutputTokens:   &critique,
		IntegratorMaxOutputTokens: &integrator,
	}}
}

func TestDeliberateOutputBoundsClampedToProbe(t *testing.T) {
	eff := effectiveWith(4096, 2048, 6144)
	if (partBudget{}).proposerOutput(eff) != 4096 || (partBudget{}).critiqueOutput(eff) != 2048 || (partBudget{}).integratorOutput(eff) != 6144 {
		t.Fatal("unbounded participant should take the configured bounds")
	}
	bound := 1000
	pb := partBudget{outputBound: &bound}
	if got := pb.proposerOutput(eff); got != 1000 {
		t.Errorf("proposer = %d, want 1000", got)
	}
	if got := pb.critiqueOutput(eff); got != 1000 {
		t.Errorf("critique = %d, want 1000", got)
	}
	if got := pb.integratorOutput(eff); got != 1000 {
		t.Errorf("integrator = %d, want 1000", got)
	}
	big := 100000
	pb = partBudget{outputBound: &big}
	if got := pb.integratorOutput(eff); got != 6144 {
		t.Errorf("integrator with a larger probe bound = %d, want 6144", got)
	}
}

func TestDeliberateStandIns(t *testing.T) {
	eff := effectiveWith(10, 20, 30)
	pb := partBudget{bytesPerToken: 3.0}
	if got := pb.proposalStandIn(eff); len(got) != 30 || strings.Trim(got, "x") != "" {
		t.Errorf("proposal stand-in = %d bytes, want 30", len(got))
	}
	if got := pb.critiqueStandIn(eff); len(got) != 60 {
		t.Errorf("critique stand-in = %d bytes, want 60", len(got))
	}
	if got := pb.candidateStandIn(eff); len(got) != 90 {
		t.Errorf("candidate stand-in = %d bytes, want 90", len(got))
	}
	bound := 5
	pb = partBudget{bytesPerToken: 2.0, outputBound: &bound}
	if got := pb.proposalStandIn(eff); len(got) != 10 {
		t.Errorf("clamped proposal stand-in = %d bytes, want 10", len(got))
	}
}
