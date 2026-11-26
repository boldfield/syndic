package config

import (
	"slices"
	"strings"
	"testing"
)

func deliberateRoster(mutate func(*Preset)) *Roster {
	r := makeBaseRoster()
	both := []Role{RoleAuthor, RoleReviewer}
	for _, id := range []string{"a", "b", "c", "d"} {
		r.Participants = append(r.Participants, Participant{ID: id, Model: "gpt4", Route: "api", Endpoint: "ep1", EligibleRoles: both, Pool: "default"})
	}
	p := Preset{Mode: ModeDeliberate, Proposers: []string{"a", "b", "c"}, Integrator: "a"}
	if mutate != nil {
		mutate(&p)
	}
	r.Presets["deliberate"] = p
	return r
}

func TestDeliberatePresetValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Preset)
		want   string // empty: accepted
	}{
		{"valid", nil, ""},
		{"non-proposer reviewer accepted", func(p *Preset) { p.RequiredReviewers = []string{"b", "d"} }, ""},
		{"one proposer", func(p *Preset) { p.Proposers = []string{"a"} }, "at least two proposers"},
		{"duplicate proposer", func(p *Preset) { p.Proposers = []string{"a", "b", "b"} }, "listed twice"},
		{"unknown proposer", func(p *Preset) { p.Proposers = []string{"a", "zz"} }, "not a participant"},
		{"no integrator", func(p *Preset) { p.Integrator = "" }, "needs an integrator"},
		{"integrator not a proposer", func(p *Preset) { p.Integrator = "d" }, "not one of the proposers"},
		{"integrator among reviewers", func(p *Preset) { p.RequiredReviewers = []string{"a", "b"} }, "must not be a required reviewer"},
		{"empty reviewers", func(p *Preset) { p.RequiredReviewers = []string{} }, "must not be empty"},
		{"unknown reviewer", func(p *Preset) { p.RequiredReviewers = []string{"zz"} }, "required reviewer \"zz\" is not a participant"},
		{"ineligible required reviewer", func(p *Preset) { p.RequiredReviewers = []string{"author"} }, "reviewer \"author\" is not eligible for reviewer role"},
		{"author refused", func(p *Preset) { p.Author = "a" }, "must not set author"},
		{"ineligible proposer", func(p *Preset) { p.Proposers = []string{"a", "author"} }, "proposer \"author\" is not eligible for reviewer role"},
		{"ineligible integrator", func(p *Preset) { p.Proposers = []string{"reviewer", "b"}; p.Integrator = "reviewer" }, "integrator \"reviewer\" is not eligible for author role"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := deliberateRoster(tt.mutate).Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), `preset "deliberate"`) {
				t.Fatalf("Validate() = %v, want %q naming the preset", err, tt.want)
			}
		})
	}
}

func TestProposersRefusedOnOtherModes(t *testing.T) {
	r := makeBaseRoster()
	p := r.Presets["review"]
	p.Proposers = []string{"author", "reviewer"}
	r.Presets["review"] = p
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "valid only with mode deliberate") {
		t.Fatalf("Validate() = %v", err)
	}
	p.Proposers, p.Integrator = []string{}, ""
	r.Presets["review"] = p
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "valid only with mode deliberate") {
		t.Fatalf("explicit empty proposers: Validate() = %v", err)
	}
	p.Proposers, p.Integrator = nil, "author"
	r.Presets["review"] = p
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "valid only with mode deliberate") {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestDefaultPresetMustStillBeReview(t *testing.T) {
	r := deliberateRoster(nil)
	r.DefaultPreset = "deliberate"
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "must have mode review") {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestDeliberateAccessors(t *testing.T) {
	p := Preset{Mode: ModeDeliberate, Proposers: []string{"c", "a", "b"}, Integrator: "a"}
	if got := p.EffectiveRequiredReviewers(); !slices.Equal(got, []string{"c", "b"}) {
		t.Fatalf("default reviewers = %v", got)
	}
	if got := p.ProposersWithoutVote(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("without vote = %v", got)
	}
	if got := p.DeliberateProposers(); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("proposers = %v", got)
	}
	if p.DeliberateIntegrator() != "a" {
		t.Fatalf("integrator = %q", p.DeliberateIntegrator())
	}

	p.RequiredReviewers = []string{"b", "d"}
	if got := p.EffectiveRequiredReviewers(); !slices.Equal(got, []string{"b", "d"}) {
		t.Fatalf("explicit reviewers = %v", got)
	}
	// "d" is no proposer and "a" votes nowhere; "c" is a proposer with no vote.
	if got := p.ProposersWithoutVote(); !slices.Equal(got, []string{"c", "a"}) {
		t.Fatalf("without vote = %v", got)
	}
}
