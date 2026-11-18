package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExample(t *testing.T) {
	roster, err := Load("testdata/example.toml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if roster == nil {
		t.Fatal("roster is nil")
	}
	if roster.DefaultPreset != "review" {
		t.Errorf("DefaultPreset = %q, want %q", roster.DefaultPreset, "review")
	}
	if len(roster.Participants) != 2 {
		t.Errorf("len(Participants) = %d, want 2", len(roster.Participants))
	}
	if len(roster.Endpoints) != 1 {
		t.Errorf("len(Endpoints) = %d, want 1", len(roster.Endpoints))
	}
	if len(roster.Presets) != 2 {
		t.Errorf("len(Presets) = %d, want 2", len(roster.Presets))
	}

	// Check deadlines
	if roster.Deadlines.Attempt == nil || roster.Deadlines.Attempt.Duration != 300*time.Second {
		t.Errorf("Deadlines.Attempt = %v, want 300s", roster.Deadlines.Attempt)
	}
	if roster.Deadlines.Turn == nil || roster.Deadlines.Turn.Duration != 900*time.Second {
		t.Errorf("Deadlines.Turn = %v, want 900s", roster.Deadlines.Turn)
	}
	if roster.Deadlines.Delivery == nil || roster.Deadlines.Delivery.Duration != 60*time.Second {
		t.Errorf("Deadlines.Delivery = %v, want 60s", roster.Deadlines.Delivery)
	}
	if roster.Deadlines.Provisional == nil || !*roster.Deadlines.Provisional {
		t.Errorf("Deadlines.Provisional = %v, want true", roster.Deadlines.Provisional)
	}

	// Check budgets
	if roster.Budgets.CalibrationSafety == nil || *roster.Budgets.CalibrationSafety != 0.75 {
		t.Errorf("Budgets.CalibrationSafety = %v, want 0.75", roster.Budgets.CalibrationSafety)
	}
	if roster.Budgets.MessageOverheadTokens == nil || *roster.Budgets.MessageOverheadTokens != 8 {
		t.Errorf("Budgets.MessageOverheadTokens = %v, want 8", roster.Budgets.MessageOverheadTokens)
	}
	if roster.Budgets.RequestOverheadTokens == nil || *roster.Budgets.RequestOverheadTokens != 16 {
		t.Errorf("Budgets.RequestOverheadTokens = %v, want 16", roster.Budgets.RequestOverheadTokens)
	}
	if roster.Budgets.ContextMargin == nil || *roster.Budgets.ContextMargin != 0.10 {
		t.Errorf("Budgets.ContextMargin = %v, want 0.10", roster.Budgets.ContextMargin)
	}
	if roster.Budgets.AuthorMaxOutputTokens == nil || *roster.Budgets.AuthorMaxOutputTokens != 4096 {
		t.Errorf("Budgets.AuthorMaxOutputTokens = %v, want 4096", roster.Budgets.AuthorMaxOutputTokens)
	}
	if roster.Budgets.ReviewerMaxOutputTokens == nil || *roster.Budgets.ReviewerMaxOutputTokens != 2048 {
		t.Errorf("Budgets.ReviewerMaxOutputTokens = %v, want 2048", roster.Budgets.ReviewerMaxOutputTokens)
	}
	if roster.Budgets.DeliveryMaxTokens == nil || *roster.Budgets.DeliveryMaxTokens != 64 {
		t.Errorf("Budgets.DeliveryMaxTokens = %v, want 64", roster.Budgets.DeliveryMaxTokens)
	}
	if roster.Budgets.TurnMaxEstimatedTokens == nil || *roster.Budgets.TurnMaxEstimatedTokens != 250000 {
		t.Errorf("Budgets.TurnMaxEstimatedTokens = %v, want 250000", roster.Budgets.TurnMaxEstimatedTokens)
	}

	// Check engine
	if roster.Engine.StopGrace == nil || roster.Engine.StopGrace.Duration != 5*time.Second {
		t.Errorf("Engine.StopGrace = %v, want 5s", roster.Engine.StopGrace)
	}

	// Check pools
	if len(roster.Pools) != 1 {
		t.Errorf("len(Pools) = %d, want 1", len(roster.Pools))
	}
	if _, ok := roster.Pools["default"]; !ok {
		t.Fatal("default pool not found")
	}
	if roster.Pools["default"].Concurrency == nil || *roster.Pools["default"].Concurrency != 1 {
		t.Errorf("Pools[default].Concurrency = %v, want 1", roster.Pools["default"].Concurrency)
	}
	if roster.Pools["default"].UncertainReleaseAfter == nil || roster.Pools["default"].UncertainReleaseAfter.Duration != 0 {
		t.Errorf("Pools[default].UncertainReleaseAfter = %v, want 0s", roster.Pools["default"].UncertainReleaseAfter)
	}

	// Check participants
	if roster.Participants[0].ContextTokens == nil || *roster.Participants[0].ContextTokens != 4096 {
		t.Errorf("Participants[0].ContextTokens = %v, want 4096", roster.Participants[0].ContextTokens)
	}
	if roster.Participants[0].BytesPerToken == nil || *roster.Participants[0].BytesPerToken != 4.0 {
		t.Errorf("Participants[0].BytesPerToken = %v, want 4.0", roster.Participants[0].BytesPerToken)
	}

	// Check endpoints
	if roster.Endpoints[0].TokensPerByteMax == nil || *roster.Endpoints[0].TokensPerByteMax != 1.0 {
		t.Errorf("Endpoints[0].TokensPerByteMax = %v, want 1.0", roster.Endpoints[0].TokensPerByteMax)
	}
}

func TestValidateExampleRoster(t *testing.T) {
	roster, err := Load("testdata/example.toml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if err := roster.Validate(); err != nil {
		t.Errorf("Validate failed: %v", err)
	}
}

func makeBaseRoster() *Roster {
	return &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "author", Model: "gpt4", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "default"},
			{ID: "reviewer", Model: "gpt4", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleReviewer}, Pool: "default"},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Dialect: "chat_completions_v1", DeclaredLocation: "local", DeclaredExecution: "local", DeclaredBilling: "none", NetworkDependency: "none"},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "author", RequiredReviewers: []string{"reviewer"}},
		},
	}
}

func TestValidateViolations(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Roster)
		wantMsgs []string
	}{
		{
			name:     "valid roster",
			mutate:   func(r *Roster) {},
			wantMsgs: []string{},
		},
		{
			name: "duplicate participant ID",
			mutate: func(r *Roster) {
				r.Participants = append(r.Participants, Participant{
					ID: "author", Model: "gpt4", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}, Pool: "default",
				})
			},
			wantMsgs: []string{"author", "duplicate participant"},
		},
		{
			name: "duplicate endpoint name",
			mutate: func(r *Roster) {
				r.Endpoints = append(r.Endpoints, EndpointProfile{
					Name: "ep1", Dialect: "chat_completions_v1", DeclaredLocation: "local",
					DeclaredExecution: "local", DeclaredBilling: "none", NetworkDependency: "none",
				})
			},
			wantMsgs: []string{"ep1", "duplicate endpoint"},
		},
		{
			name: "participant references non-existent endpoint",
			mutate: func(r *Roster) {
				r.Participants[0].Endpoint = "nonexistent"
			},
			wantMsgs: []string{"author", "nonexistent", "references non-existent endpoint"},
		},
		{
			name: "api participant missing endpoint",
			mutate: func(r *Roster) {
				r.Participants[0].Endpoint = ""
			},
			wantMsgs: []string{"author", "missing endpoint"},
		},
		{
			name: "cli participant missing binary",
			mutate: func(r *Roster) {
				r.Participants[0].Route = "cli"
				r.Participants[0].Endpoint = ""
				r.Participants[0].Binary = ""
			},
			wantMsgs: []string{"author", "missing binary"},
		},
		{
			name: "participant model not in allowlist",
			mutate: func(r *Roster) {
				r.Participants[0].Model = "unknown"
				r.Endpoints[0].Models = []string{"gpt4-turbo", "gpt3.5"}
			},
			wantMsgs: []string{"author", "unknown", "not in endpoint"},
		},
		{
			name: "unknown participant route",
			mutate: func(r *Roster) {
				r.Participants[0].Route = "grpc"
			},
			wantMsgs: []string{"author", "grpc", "unknown route"},
		},
		{
			name: "empty participant route",
			mutate: func(r *Roster) {
				r.Participants[0].Route = ""
			},
			wantMsgs: []string{"author", "unknown route"},
		},
		{
			name: "unknown role in participant",
			mutate: func(r *Roster) {
				r.Participants[0].EligibleRoles = []Role{"unknown_role"}
			},
			wantMsgs: []string{"author", "unknown_role", "unknown role"},
		},
		{
			name: "preset author in required reviewers",
			mutate: func(r *Roster) {
				p := r.Presets["review"]
				p.RequiredReviewers = []string{"reviewer", "author"}
				r.Presets["review"] = p
			},
			wantMsgs: []string{"review", "author", "also in required reviewers"},
		},
		{
			name: "review preset with no required reviewers",
			mutate: func(r *Roster) {
				p := r.Presets["review"]
				p.RequiredReviewers = []string{}
				r.Presets["review"] = p
			},
			wantMsgs: []string{"review", "review mode must have required reviewers"},
		},
		{
			name: "solo preset with required reviewers",
			mutate: func(r *Roster) {
				r.Presets["solo"] = Preset{Mode: "solo", Author: "author", RequiredReviewers: []string{"reviewer"}}
			},
			wantMsgs: []string{"solo", "solo mode must not have required reviewers"},
		},
		{
			name: "preset references non-existent participant",
			mutate: func(r *Roster) {
				p := r.Presets["review"]
				p.RequiredReviewers = []string{"nonexistent"}
				r.Presets["review"] = p
			},
			wantMsgs: []string{"review", "nonexistent", "references non-existent participant"},
		},
		{
			name: "unknown preset mode",
			mutate: func(r *Roster) {
				p := r.Presets["review"]
				p.Mode = "unknown"
				r.Presets["review"] = p
			},
			wantMsgs: []string{"review", "unknown", "unknown mode"},
		},
		{
			name: "participant not eligible for author role",
			mutate: func(r *Roster) {
				r.Participants[0].EligibleRoles = []Role{RoleReviewer}
			},
			wantMsgs: []string{"review", "author", "not eligible for author role"},
		},
		{
			name: "participant not eligible for reviewer role",
			mutate: func(r *Roster) {
				r.Participants[1].EligibleRoles = []Role{RoleAuthor}
			},
			wantMsgs: []string{"review", "reviewer", "not eligible for reviewer role"},
		},
		{
			name: "empty default preset",
			mutate: func(r *Roster) {
				r.DefaultPreset = ""
			},
			wantMsgs: []string{"default_preset is empty"},
		},
		{
			name: "default preset non-existent",
			mutate: func(r *Roster) {
				r.DefaultPreset = "nonexistent"
			},
			wantMsgs: []string{"default_preset", "nonexistent", "references non-existent preset"},
		},
		{
			name: "default preset not review mode",
			mutate: func(r *Roster) {
				r.DefaultPreset = "solo"
				r.Presets["solo"] = Preset{Mode: "solo", Author: "author", RequiredReviewers: []string{}}
			},
			wantMsgs: []string{"default_preset", "solo", "must have mode review"},
		},
		{
			name: "empty declared location",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredLocation = ""
			},
			wantMsgs: []string{"ep1", "empty declared_location"},
		},
		{
			name: "unknown declared location",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredLocation = "mars"
			},
			wantMsgs: []string{"ep1", "mars", "unknown declared_location"},
		},
		{
			name: "empty declared execution",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredExecution = ""
			},
			wantMsgs: []string{"ep1", "empty declared_execution"},
		},
		{
			name: "unknown declared execution",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredExecution = "async"
			},
			wantMsgs: []string{"ep1", "async", "unknown declared_execution"},
		},
		{
			name: "empty declared billing",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredBilling = ""
			},
			wantMsgs: []string{"ep1", "empty declared_billing"},
		},
		{
			name: "unknown declared billing",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredBilling = "per-use"
			},
			wantMsgs: []string{"ep1", "per-use", "unknown declared_billing"},
		},
		{
			name: "empty network dependency",
			mutate: func(r *Roster) {
				r.Endpoints[0].NetworkDependency = ""
			},
			wantMsgs: []string{"ep1", "empty network_dependency"},
		},
		{
			name: "unknown network dependency",
			mutate: func(r *Roster) {
				r.Endpoints[0].NetworkDependency = "proxy"
			},
			wantMsgs: []string{"ep1", "proxy", "unknown network_dependency"},
		},
		{
			name: "empty dialect",
			mutate: func(r *Roster) {
				r.Endpoints[0].Dialect = ""
			},
			wantMsgs: []string{"ep1", "empty dialect"},
		},
		{
			name: "unknown dialect",
			mutate: func(r *Roster) {
				r.Endpoints[0].Dialect = "openai_v1"
			},
			wantMsgs: []string{"ep1", "openai_v1", "unknown dialect"},
		},
		{
			name: "unknown max_tokens_field",
			mutate: func(r *Roster) {
				r.Endpoints[0].MaxTokensField = "bogus"
			},
			wantMsgs: []string{"ep1", "bogus", "unknown max_tokens_field"},
		},
		{
			name: "empty max_tokens_field (valid)",
			mutate: func(r *Roster) {
				r.Endpoints[0].MaxTokensField = ""
			},
			wantMsgs: []string{},
		},
		{
			name: "valid max_tokens_field max_tokens",
			mutate: func(r *Roster) {
				r.Endpoints[0].MaxTokensField = "max_tokens"
			},
			wantMsgs: []string{},
		},
		{
			name: "valid max_tokens_field max_completion_tokens",
			mutate: func(r *Roster) {
				r.Endpoints[0].MaxTokensField = "max_completion_tokens"
			},
			wantMsgs: []string{},
		},
		{
			name: "unknown secret source in auth",
			mutate: func(r *Roster) {
				r.Endpoints[0].Auth = &SecretRef{Source: "vault", Name: "key"}
			},
			wantMsgs: []string{"ep1", "auth", "vault", "unknown source"},
		},
		{
			name: "unknown secret source in extra_headers",
			mutate: func(r *Roster) {
				r.Endpoints[0].ExtraHeaders = map[string]SecretRef{
					"X-API-Key": {Source: "s3", Name: "bucket/key"},
				}
			},
			wantMsgs: []string{"ep1", "X-API-Key", "s3", "unknown source"},
		},
		{
			name: "metered billing",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredBilling = "metered"
			},
			wantMsgs: []string{},
		},
		{
			name: "local_only rejects corporate location",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredLocation = "corporate"
				r.Policy.LocalOnly = true
			},
			wantMsgs: []string{"ep1", "corporate", "local_only policy rejects"},
		},
		{
			name: "local_only rejects cloud location",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredLocation = "cloud"
				r.Policy.LocalOnly = true
			},
			wantMsgs: []string{"ep1", "cloud", "local_only policy rejects"},
		},
		{
			name: "local_only rejects remote execution",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredExecution = "remote"
				r.Policy.LocalOnly = true
			},
			wantMsgs: []string{"ep1", "remote", "local_only policy rejects"},
		},
		{
			name: "local_only allows lan location",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredLocation = "lan"
				r.Policy.LocalOnly = true
			},
			wantMsgs: []string{},
		},
		{
			name: "local_only allows local execution",
			mutate: func(r *Roster) {
				r.Policy.LocalOnly = true
			},
			wantMsgs: []string{},
		},
		{
			name: "allowed_destinations non-existent endpoint",
			mutate: func(r *Roster) {
				r.Policy.AllowedDestinations = []string{"nonexistent"}
			},
			wantMsgs: []string{"nonexistent", "references non-existent endpoint"},
		},
		{
			name: "corporate endpoint not in allowed_destinations",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredLocation = "corporate"
				r.Policy.LocalOnly = false
				r.Policy.AllowedDestinations = []string{}
			},
			wantMsgs: []string{"ep1", "corporate", "must be in allowed_destinations"},
		},
		{
			name: "cloud endpoint not in allowed_destinations",
			mutate: func(r *Roster) {
				r.Endpoints[0].DeclaredLocation = "cloud"
				r.Policy.LocalOnly = false
				r.Policy.AllowedDestinations = []string{}
			},
			wantMsgs: []string{"ep1", "cloud", "must be in allowed_destinations"},
		},
		{
			name: "multiple violations",
			mutate: func(r *Roster) {
				r.DefaultPreset = "nonexistent"
				r.Participants[0].Endpoint = ""
				r.Participants = append(r.Participants, Participant{
					ID: "author", Model: "gpt4", Route: "api", EligibleRoles: []Role{RoleAuthor}, Pool: "default",
				})
			},
			wantMsgs: []string{"duplicate participant", "author", "missing endpoint", "default_preset", "nonexistent"},
		},
		{
			name: "negative attempt deadline",
			mutate: func(r *Roster) {
				negDur := Duration{-1 * time.Second}
				r.Deadlines.Attempt = &negDur
			},
			wantMsgs: []string{"deadlines.attempt", "positive"},
		},
		{
			name: "turn deadline less than attempt",
			mutate: func(r *Roster) {
				attempt := Duration{10 * time.Second}
				turn := Duration{5 * time.Second}
				r.Deadlines.Attempt = &attempt
				r.Deadlines.Turn = &turn
			},
			wantMsgs: []string{"deadlines.turn", "at least", "deadlines.attempt"},
		},
		{
			name: "calibration_safety out of range",
			mutate: func(r *Roster) {
				cs := 1.5
				r.Budgets.CalibrationSafety = &cs
			},
			wantMsgs: []string{"calibration_safety", "greater than 0", "at most 1.0"},
		},
		{
			name: "context_margin out of range",
			mutate: func(r *Roster) {
				cm := 1.5
				r.Budgets.ContextMargin = &cm
			},
			wantMsgs: []string{"context_margin", "less than 1"},
		},
		{
			name: "participant context_tokens invalid",
			mutate: func(r *Roster) {
				ct := 0
				r.Participants[0].ContextTokens = &ct
			},
			wantMsgs: []string{"author", "context_tokens", "at least 1"},
		},
		{
			name: "endpoint tokens_per_byte_max invalid",
			mutate: func(r *Roster) {
				tpb := 0.0
				r.Endpoints[0].TokensPerByteMax = &tpb
			},
			wantMsgs: []string{"ep1", "tokens_per_byte_max", "greater than 0"},
		},
		{
			name: "pool concurrency invalid",
			mutate: func(r *Roster) {
				if r.Pools == nil {
					r.Pools = make(map[string]Pool)
				}
				conc := 0
				r.Pools["test"] = Pool{Concurrency: &conc}
				r.Participants[0].Pool = "test"
			},
			wantMsgs: []string{"pools[\"test\"]", "concurrency", "at least 1"},
		},
		{
			name: "participant references non-existent pool",
			mutate: func(r *Roster) {
				r.Participants[0].Pool = "nonexistent"
			},
			wantMsgs: []string{"author", "nonexistent", "references non-existent pool"},
		},
		{
			name: "negative turn deadline",
			mutate: func(r *Roster) {
				negDur := Duration{-1 * time.Second}
				r.Deadlines.Turn = &negDur
			},
			wantMsgs: []string{"deadlines.turn", "positive"},
		},
		{
			name: "negative delivery deadline",
			mutate: func(r *Roster) {
				negDur := Duration{-1 * time.Second}
				r.Deadlines.Delivery = &negDur
			},
			wantMsgs: []string{"deadlines.delivery", "positive"},
		},
		{
			name: "negative stop_grace",
			mutate: func(r *Roster) {
				negDur := Duration{-1 * time.Second}
				r.Engine.StopGrace = &negDur
			},
			wantMsgs: []string{"engine.stop_grace", "positive"},
		},
		{
			name: "negative uncertain_release_after",
			mutate: func(r *Roster) {
				if r.Pools == nil {
					r.Pools = make(map[string]Pool)
				}
				negDur := Duration{-1 * time.Second}
				r.Pools["test"] = Pool{UncertainReleaseAfter: &negDur}
				r.Participants[0].Pool = "test"
			},
			wantMsgs: []string{"pools[\"test\"]", "uncertain_release_after", "must not be negative"},
		},
		{
			name: "participant bytes_per_token zero",
			mutate: func(r *Roster) {
				bpt := 0.0
				r.Participants[0].BytesPerToken = &bpt
			},
			wantMsgs: []string{"author", "bytes_per_token", "greater than 0"},
		},
		{
			name: "participant bytes_per_token negative",
			mutate: func(r *Roster) {
				bpt := -1.0
				r.Participants[0].BytesPerToken = &bpt
			},
			wantMsgs: []string{"author", "bytes_per_token", "greater than 0"},
		},
		{
			name: "message_overhead_tokens zero",
			mutate: func(r *Roster) {
				mot := 0
				r.Budgets.MessageOverheadTokens = &mot
			},
			wantMsgs: []string{"message_overhead_tokens", "at least 1"},
		},
		{
			name: "request_overhead_tokens zero",
			mutate: func(r *Roster) {
				rot := 0
				r.Budgets.RequestOverheadTokens = &rot
			},
			wantMsgs: []string{"request_overhead_tokens", "at least 1"},
		},
		{
			name: "author_max_output_tokens zero",
			mutate: func(r *Roster) {
				amot := 0
				r.Budgets.AuthorMaxOutputTokens = &amot
			},
			wantMsgs: []string{"author_max_output_tokens", "at least 1"},
		},
		{
			name: "reviewer_max_output_tokens zero",
			mutate: func(r *Roster) {
				rmot := 0
				r.Budgets.ReviewerMaxOutputTokens = &rmot
			},
			wantMsgs: []string{"reviewer_max_output_tokens", "at least 1"},
		},
		{
			name: "delivery_max_tokens zero",
			mutate: func(r *Roster) {
				dmt := 0
				r.Budgets.DeliveryMaxTokens = &dmt
			},
			wantMsgs: []string{"delivery_max_tokens", "at least 1"},
		},
		{
			name: "turn_max_estimated_tokens zero",
			mutate: func(r *Roster) {
				tmet := 0
				r.Budgets.TurnMaxEstimatedTokens = &tmet
			},
			wantMsgs: []string{"turn_max_estimated_tokens", "at least 1"},
		},
		{
			name: "calibration_safety zero",
			mutate: func(r *Roster) {
				cs := 0.0
				r.Budgets.CalibrationSafety = &cs
			},
			wantMsgs: []string{"calibration_safety", "greater than 0"},
		},
		{
			name: "calibration_safety negative",
			mutate: func(r *Roster) {
				cs := -0.5
				r.Budgets.CalibrationSafety = &cs
			},
			wantMsgs: []string{"calibration_safety", "greater than 0"},
		},
		{
			name: "context_margin negative",
			mutate: func(r *Roster) {
				cm := -0.1
				r.Budgets.ContextMargin = &cm
			},
			wantMsgs: []string{"context_margin", "at least 0"},
		},
		{
			name: "zero uncertain_release_after (valid)",
			mutate: func(r *Roster) {
				if r.Pools == nil {
					r.Pools = make(map[string]Pool)
				}
				zeroDur := Duration{0}
				r.Pools["test"] = Pool{UncertainReleaseAfter: &zeroDur}
				r.Participants[0].Pool = "test"
			},
			wantMsgs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roster := makeBaseRoster()
			tt.mutate(roster)

			err := roster.Validate()
			if len(tt.wantMsgs) == 0 {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("Validate() = nil, want error")
			}

			errStr := err.Error()
			for _, msg := range tt.wantMsgs {
				if !strings.Contains(errStr, msg) {
					t.Errorf("error doesn't contain %q: %v", msg, err)
				}
			}
		})
	}
}

func TestSecretRefString(t *testing.T) {
	tests := []struct {
		ref      SecretRef
		expected string
	}{
		{SecretRef{Source: "env", Name: "API_KEY"}, "env:API_KEY"},
		{SecretRef{Source: "file", Name: "secret.txt"}, "file:secret.txt"},
	}

	for _, tt := range tests {
		got := tt.ref.String()
		if got != tt.expected {
			t.Errorf("String() = %q, want %q", got, tt.expected)
		}
	}
}

func TestResolveEnv(t *testing.T) {
	t.Setenv("TEST_SECRET", "test-value")

	value, err := resolveEnv("TEST_SECRET")
	if err != nil {
		t.Fatalf("resolveEnv failed: %v", err)
	}
	if value != "test-value" {
		t.Errorf("value = %q, want %q", value, "test-value")
	}
}

func TestResolveEnvNotSet(t *testing.T) {
	_, err := resolveEnv("NONEXISTENT_VAR_12345")
	if err == nil {
		t.Fatal("expected error for unset env var")
	}
	if !strings.Contains(err.Error(), "not set") {
		t.Errorf("error doesn't mention not set: %v", err)
	}
}

func TestResolveFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(filePath, []byte("secret-value\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	value, err := resolveFile(dir, "secret.txt")
	if err != nil {
		t.Fatalf("resolveFile failed: %v", err)
	}
	if value != "secret-value" {
		t.Errorf("value = %q, want %q", value, "secret-value")
	}
}

func TestResolveFilePathSeparator(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveFile(dir, "subdir/secret.txt")
	if err == nil {
		t.Fatal("expected error for path separator in name")
	}
	if !strings.Contains(err.Error(), "path separator") {
		t.Errorf("error doesn't mention path separator: %v", err)
	}
}

func TestResolveFileNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveFile(dir, "nonexistent.txt")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestEnvFileResolver(t *testing.T) {
	dir := t.TempDir()
	resolver := NewEnvFileResolver(dir)

	// Test env resolver
	t.Setenv("TEST_SECRET", "env-value")

	value, err := resolver.Resolve(SecretRef{Source: "env", Name: "TEST_SECRET"})
	if err != nil {
		t.Fatalf("Resolve env failed: %v", err)
	}
	if value != "env-value" {
		t.Errorf("env value = %q, want %q", value, "env-value")
	}

	// Test file resolver
	filePath := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(filePath, []byte("file-value\n"), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	value, err = resolver.Resolve(SecretRef{Source: "file", Name: "secret.txt"})
	if err != nil {
		t.Fatalf("Resolve file failed: %v", err)
	}
	if value != "file-value" {
		t.Errorf("file value = %q, want %q", value, "file-value")
	}
}

func TestResolveWithSentinelString(t *testing.T) {
	// This test verifies that fmt.Sprintf output never contains resolved secret values
	t.Setenv("SENTINEL_SECRET", "SENTINEL_VALUE_12345")

	dir := t.TempDir()
	resolver := NewEnvFileResolver(dir)

	secretRef := SecretRef{Source: "env", Name: "SENTINEL_SECRET"}
	value, err := resolver.Resolve(secretRef)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// The resolved value should contain the sentinel
	if value != "SENTINEL_VALUE_12345" {
		t.Fatalf("resolved value = %q, want %q", value, "SENTINEL_VALUE_12345")
	}

	// But the string representation of SecretRef should not
	str := fmt.Sprintf("%v", secretRef)
	if strings.Contains(str, "SENTINEL_VALUE_12345") {
		t.Errorf("SecretRef.String() contains resolved secret: %s", str)
	}

	// And the roster string should not
	roster := &Roster{
		DefaultPreset: "review",
		Participants: []Participant{
			{ID: "p1", Model: "m1", Route: "api", Endpoint: "ep1", EligibleRoles: []Role{RoleAuthor}},
		},
		Endpoints: []EndpointProfile{
			{Name: "ep1", Auth: &secretRef},
		},
		Presets: map[string]Preset{
			"review": {Mode: "review", Author: "p1", RequiredReviewers: []string{}},
		},
	}
	str = fmt.Sprintf("%v", roster)
	if strings.Contains(str, "SENTINEL_VALUE_12345") {
		t.Errorf("Roster.String() contains resolved secret: %s", str)
	}
}

func TestApplyDefaults(t *testing.T) {
	r := &Roster{}
	r.ApplyDefaults()

	if r.Deadlines.Attempt == nil || r.Deadlines.Attempt.Duration != 300*time.Second {
		t.Errorf("Deadlines.Attempt = %v, want 300s", r.Deadlines.Attempt)
	}
	if r.Deadlines.Turn == nil || r.Deadlines.Turn.Duration != 900*time.Second {
		t.Errorf("Deadlines.Turn = %v, want 900s", r.Deadlines.Turn)
	}
	if r.Deadlines.Delivery == nil || r.Deadlines.Delivery.Duration != 60*time.Second {
		t.Errorf("Deadlines.Delivery = %v, want 60s", r.Deadlines.Delivery)
	}
	if r.Deadlines.Provisional == nil || !*r.Deadlines.Provisional {
		t.Errorf("Deadlines.Provisional = %v, want true", r.Deadlines.Provisional)
	}

	if r.Budgets.CalibrationSafety == nil || *r.Budgets.CalibrationSafety != 0.75 {
		t.Errorf("Budgets.CalibrationSafety = %v, want 0.75", r.Budgets.CalibrationSafety)
	}
	if r.Budgets.MessageOverheadTokens == nil || *r.Budgets.MessageOverheadTokens != 8 {
		t.Errorf("Budgets.MessageOverheadTokens = %v, want 8", r.Budgets.MessageOverheadTokens)
	}
	if r.Budgets.RequestOverheadTokens == nil || *r.Budgets.RequestOverheadTokens != 16 {
		t.Errorf("Budgets.RequestOverheadTokens = %v, want 16", r.Budgets.RequestOverheadTokens)
	}
	if r.Budgets.ContextMargin == nil || *r.Budgets.ContextMargin != 0.10 {
		t.Errorf("Budgets.ContextMargin = %v, want 0.10", r.Budgets.ContextMargin)
	}
	if r.Budgets.AuthorMaxOutputTokens == nil || *r.Budgets.AuthorMaxOutputTokens != 4096 {
		t.Errorf("Budgets.AuthorMaxOutputTokens = %v, want 4096", r.Budgets.AuthorMaxOutputTokens)
	}
	if r.Budgets.ReviewerMaxOutputTokens == nil || *r.Budgets.ReviewerMaxOutputTokens != 2048 {
		t.Errorf("Budgets.ReviewerMaxOutputTokens = %v, want 2048", r.Budgets.ReviewerMaxOutputTokens)
	}
	if r.Budgets.DeliveryMaxTokens == nil || *r.Budgets.DeliveryMaxTokens != 64 {
		t.Errorf("Budgets.DeliveryMaxTokens = %v, want 64", r.Budgets.DeliveryMaxTokens)
	}
	if r.Budgets.TurnMaxEstimatedTokens == nil || *r.Budgets.TurnMaxEstimatedTokens != 250000 {
		t.Errorf("Budgets.TurnMaxEstimatedTokens = %v, want 250000", r.Budgets.TurnMaxEstimatedTokens)
	}

	if r.Engine.StopGrace == nil || r.Engine.StopGrace.Duration != 5*time.Second {
		t.Errorf("Engine.StopGrace = %v, want 5s", r.Engine.StopGrace)
	}
}

func TestApplyDefaultsPreservesExisting(t *testing.T) {
	r, err := Load("testdata/apply-defaults-preserve.toml")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Should have attempt = 10s set, everything else should be defaults
	if r.Deadlines.Attempt == nil || r.Deadlines.Attempt.Duration != 10*time.Second {
		t.Errorf("Deadlines.Attempt = %v, want 10s", r.Deadlines.Attempt)
	}
	if r.Deadlines.Turn == nil || r.Deadlines.Turn.Duration != 900*time.Second {
		t.Errorf("Deadlines.Turn = %v, want 900s", r.Deadlines.Turn)
	}
	if r.Deadlines.Delivery == nil || r.Deadlines.Delivery.Duration != 60*time.Second {
		t.Errorf("Deadlines.Delivery = %v, want 60s", r.Deadlines.Delivery)
	}
	if r.Deadlines.Provisional == nil || !*r.Deadlines.Provisional {
		t.Errorf("Deadlines.Provisional = %v, want true", r.Deadlines.Provisional)
	}

	if r.Budgets.CalibrationSafety == nil || *r.Budgets.CalibrationSafety != 0.75 {
		t.Errorf("Budgets.CalibrationSafety = %v, want 0.75", r.Budgets.CalibrationSafety)
	}
	if r.Budgets.MessageOverheadTokens == nil || *r.Budgets.MessageOverheadTokens != 8 {
		t.Errorf("Budgets.MessageOverheadTokens = %v, want 8", r.Budgets.MessageOverheadTokens)
	}
	if r.Budgets.RequestOverheadTokens == nil || *r.Budgets.RequestOverheadTokens != 16 {
		t.Errorf("Budgets.RequestOverheadTokens = %v, want 16", r.Budgets.RequestOverheadTokens)
	}
	if r.Budgets.ContextMargin == nil || *r.Budgets.ContextMargin != 0.10 {
		t.Errorf("Budgets.ContextMargin = %v, want 0.10", r.Budgets.ContextMargin)
	}
	if r.Budgets.AuthorMaxOutputTokens == nil || *r.Budgets.AuthorMaxOutputTokens != 4096 {
		t.Errorf("Budgets.AuthorMaxOutputTokens = %v, want 4096", r.Budgets.AuthorMaxOutputTokens)
	}
	if r.Budgets.ReviewerMaxOutputTokens == nil || *r.Budgets.ReviewerMaxOutputTokens != 2048 {
		t.Errorf("Budgets.ReviewerMaxOutputTokens = %v, want 2048", r.Budgets.ReviewerMaxOutputTokens)
	}
	if r.Budgets.DeliveryMaxTokens == nil || *r.Budgets.DeliveryMaxTokens != 64 {
		t.Errorf("Budgets.DeliveryMaxTokens = %v, want 64", r.Budgets.DeliveryMaxTokens)
	}
	if r.Budgets.TurnMaxEstimatedTokens == nil || *r.Budgets.TurnMaxEstimatedTokens != 250000 {
		t.Errorf("Budgets.TurnMaxEstimatedTokens = %v, want 250000", r.Budgets.TurnMaxEstimatedTokens)
	}

	if r.Engine.StopGrace == nil || r.Engine.StopGrace.Duration != 5*time.Second {
		t.Errorf("Engine.StopGrace = %v, want 5s", r.Engine.StopGrace)
	}
}

func TestPoolFor(t *testing.T) {
	r := &Roster{
		Pools: map[string]Pool{
			"spark": {
				Concurrency:           ptr(5),
				UncertainReleaseAfter: &Duration{10 * time.Second},
			},
		},
	}

	// Test declared pool
	conc, release := r.PoolFor("spark")
	if conc != 5 {
		t.Errorf("PoolFor(spark) concurrency = %d, want 5", conc)
	}
	if release != 10*time.Second {
		t.Errorf("PoolFor(spark) release = %v, want 10s", release)
	}

	// Test default pool
	conc, release = r.PoolFor("default")
	if conc != 1 {
		t.Errorf("PoolFor(default) concurrency = %d, want 1", conc)
	}
	if release != 0 {
		t.Errorf("PoolFor(default) release = %v, want 0", release)
	}

	// Test empty string
	conc, release = r.PoolFor("")
	if conc != 1 {
		t.Errorf("PoolFor(\"\") concurrency = %d, want 1", conc)
	}
	if release != 0 {
		t.Errorf("PoolFor(\"\") release = %v, want 0", release)
	}

	// Test undeclared pool
	conc, release = r.PoolFor("unknown")
	if conc != 1 {
		t.Errorf("PoolFor(unknown) concurrency = %d, want 1", conc)
	}
	if release != 0 {
		t.Errorf("PoolFor(unknown) release = %v, want 0", release)
	}
}

func TestPoolForWithPartialFields(t *testing.T) {
	r := &Roster{
		Pools: map[string]Pool{
			"test": {
				Concurrency: ptr(3),
			},
		},
	}

	conc, release := r.PoolFor("test")
	if conc != 3 {
		t.Errorf("PoolFor(test) concurrency = %d, want 3", conc)
	}
	if release != 0 {
		t.Errorf("PoolFor(test) release = %v, want 0", release)
	}
}

func TestMalformedDuration(t *testing.T) {
	_, err := Load("testdata/malformed-duration.toml")
	if err == nil {
		t.Fatal("expected error for malformed duration")
	}
	if !strings.Contains(err.Error(), "deadlines.attempt") {
		t.Errorf("error doesn't mention deadlines.attempt: %v", err)
	}
}

func TestUnknownBudgetKey(t *testing.T) {
	_, err := Load("testdata/unknown-budget-key.toml")
	if err == nil {
		t.Fatal("expected error for unknown budget key")
	}
	if !strings.Contains(err.Error(), "unknown keys") {
		t.Errorf("error doesn't mention unknown keys: %v", err)
	}
}

func ptr[T any](v T) *T {
	return &v
}

func BenchmarkValidate(b *testing.B) {
	roster, _ := Load("testdata/example.toml")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = roster.Validate()
	}
}

func TestStressTest(t *testing.T) {
	start := time.Now()

	participants := make([]Participant, 50)
	endpoints := make([]EndpointProfile, 50)

	for i := 0; i < 50; i++ {
		participants[i] = Participant{
			ID:            fmt.Sprintf("p%d", i),
			Model:         "m1",
			Route:         "api",
			Endpoint:      fmt.Sprintf("ep%d", i),
			EligibleRoles: []Role{RoleAuthor, RoleReviewer},
			Pool:          "default",
		}
		endpoints[i] = EndpointProfile{
			Name:              fmt.Sprintf("ep%d", i),
			Dialect:           "chat_completions_v1",
			DeclaredLocation:  "local",
			DeclaredExecution: "local",
			DeclaredBilling:   "none",
			NetworkDependency: "none",
		}
	}

	presets := make(map[string]Preset)
	presets["review"] = Preset{Mode: "review", Author: "p0", RequiredReviewers: []string{"p1"}}

	roster := &Roster{
		DefaultPreset: "review",
		Participants:  participants,
		Endpoints:     endpoints,
		Presets:       presets,
		Policy:        RoutingPolicy{LocalOnly: true},
	}

	err := roster.Validate()
	if err != nil {
		t.Errorf("Validate failed on stress test: %v", err)
	}

	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Logf("Validation took %v (may be slow)", elapsed)
	}
}
