package config

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

func (r *Roster) Validate() error {
	var errs []error

	// Check for duplicate participant IDs
	seenParticipantIDs := make(map[string]bool)
	for _, p := range r.Participants {
		if seenParticipantIDs[p.ID] {
			errs = append(errs, fmt.Errorf("duplicate participant ID: %q", p.ID))
		}
		seenParticipantIDs[p.ID] = true
	}

	// Check for duplicate endpoint names
	seenEndpointNames := make(map[string]bool)
	endpointMap := make(map[string]EndpointProfile)
	for _, ep := range r.Endpoints {
		if seenEndpointNames[ep.Name] {
			errs = append(errs, fmt.Errorf("duplicate endpoint name: %q", ep.Name))
		}
		seenEndpointNames[ep.Name] = true
		endpointMap[ep.Name] = ep
	}

	// Validate participants
	for _, p := range r.Participants {
		// Validate route
		if p.Route != "api" && p.Route != "cli" {
			errs = append(errs, fmt.Errorf("participant %q has unknown route: %q", p.ID, p.Route))
		}

		// Check if participant references an endpoint that exists
		if p.Route == "api" && p.Endpoint != "" && !seenEndpointNames[p.Endpoint] {
			errs = append(errs, fmt.Errorf("participant %q references non-existent endpoint: %q", p.ID, p.Endpoint))
		}

		// Check api participant has endpoint
		if p.Route == "api" && p.Endpoint == "" {
			errs = append(errs, fmt.Errorf("api participant %q missing endpoint", p.ID))
		}

		// Check cli participant has binary
		if p.Route == "cli" && p.Binary == "" {
			errs = append(errs, fmt.Errorf("cli participant %q missing binary", p.ID))
		}

		// Check if model is in endpoint's allowlist
		if p.Route == "api" && p.Endpoint != "" {
			ep := endpointMap[p.Endpoint]
			if len(ep.Models) > 0 && !slices.Contains(ep.Models, p.Model) {
				errs = append(errs, fmt.Errorf("participant %q model %q not in endpoint %q allowlist",
					p.ID, p.Model, p.Endpoint))
			}
		}

		// Validate eligible roles
		for _, role := range p.EligibleRoles {
			if role != RoleAuthor && role != RoleReviewer {
				errs = append(errs, fmt.Errorf("participant %q has unknown role: %q", p.ID, role))
			}
		}
	}

	// Validate endpoint profiles
	for _, ep := range r.Endpoints {
		// Validate declared location (required, not empty)
		if ep.DeclaredLocation == "" {
			errs = append(errs, fmt.Errorf("endpoint %q has empty declared_location", ep.Name))
		} else if ep.DeclaredLocation != "local" &&
			ep.DeclaredLocation != "lan" &&
			ep.DeclaredLocation != "corporate" &&
			ep.DeclaredLocation != "cloud" {
			errs = append(errs, fmt.Errorf("endpoint %q has unknown declared_location: %q",
				ep.Name, ep.DeclaredLocation))
		}

		// Validate declared execution (required, not empty)
		if ep.DeclaredExecution == "" {
			errs = append(errs, fmt.Errorf("endpoint %q has empty declared_execution", ep.Name))
		} else if ep.DeclaredExecution != "local" &&
			ep.DeclaredExecution != "remote" &&
			ep.DeclaredExecution != "unknown" {
			errs = append(errs, fmt.Errorf("endpoint %q has unknown declared_execution: %q",
				ep.Name, ep.DeclaredExecution))
		}

		// Validate declared billing (required, not empty)
		if ep.DeclaredBilling == "" {
			errs = append(errs, fmt.Errorf("endpoint %q has empty declared_billing", ep.Name))
		} else if ep.DeclaredBilling != "none" &&
			ep.DeclaredBilling != "flat" &&
			ep.DeclaredBilling != "metered" &&
			ep.DeclaredBilling != "unknown" {
			errs = append(errs, fmt.Errorf("endpoint %q has unknown declared_billing: %q",
				ep.Name, ep.DeclaredBilling))
		}

		// Validate network dependency (required, not empty)
		if ep.NetworkDependency == "" {
			errs = append(errs, fmt.Errorf("endpoint %q has empty network_dependency", ep.Name))
		} else if ep.NetworkDependency != "none" &&
			ep.NetworkDependency != "vpn" &&
			ep.NetworkDependency != "other" {
			errs = append(errs, fmt.Errorf("endpoint %q has unknown network_dependency: %q",
				ep.Name, ep.NetworkDependency))
		}

		// Validate dialect (required, not empty)
		if ep.Dialect == "" {
			errs = append(errs, fmt.Errorf("endpoint %q has empty dialect", ep.Name))
		} else if ep.Dialect != "chat_completions_v1" {
			errs = append(errs, fmt.Errorf("endpoint %q has unknown dialect: %q",
				ep.Name, ep.Dialect))
		}

		// Validate max tokens field (optional, "" is allowed, but if set must be valid)
		if ep.MaxTokensField != "" &&
			ep.MaxTokensField != "max_tokens" &&
			ep.MaxTokensField != "max_completion_tokens" {
			errs = append(errs, fmt.Errorf("endpoint %q has unknown max_tokens_field: %q",
				ep.Name, ep.MaxTokensField))
		}

		// Validate secret refs
		if ep.Auth != nil {
			if ep.Auth.Source != "env" && ep.Auth.Source != "file" {
				errs = append(errs, fmt.Errorf("endpoint %q auth has unknown source: %q",
					ep.Name, ep.Auth.Source))
			}
		}
		for headerName, secretRef := range ep.ExtraHeaders {
			if secretRef.Source != "env" && secretRef.Source != "file" {
				errs = append(errs, fmt.Errorf("endpoint %q extra_headers[%q] has unknown source: %q",
					ep.Name, headerName, secretRef.Source))
			}
		}
	}

	// Validate presets
	seenPresetNames := make(map[string]bool)
	for name, preset := range r.Presets {
		seenPresetNames[name] = true

		// Check preset mode
		if preset.Mode != "solo" && preset.Mode != "review" {
			errs = append(errs, fmt.Errorf("preset %q has unknown mode: %q", name, preset.Mode))
		}

		// Check if author is in required reviewers
		if slices.Contains(preset.RequiredReviewers, preset.Author) {
			errs = append(errs, fmt.Errorf("preset %q: author %q is also in required reviewers",
				name, preset.Author))
		}

		// Check review preset must have required reviewers
		if preset.Mode == "review" && len(preset.RequiredReviewers) == 0 {
			errs = append(errs, fmt.Errorf("preset %q: review mode must have required reviewers", name))
		}

		// Check solo preset must not have required reviewers
		if preset.Mode == "solo" && len(preset.RequiredReviewers) > 0 {
			errs = append(errs, fmt.Errorf("preset %q: solo mode must not have required reviewers", name))
		}

		// Validate that all preset participants exist
		allPresetParticipants := []string{preset.Author}
		allPresetParticipants = append(allPresetParticipants, preset.RequiredReviewers...)

		for _, pID := range allPresetParticipants {
			if !seenParticipantIDs[pID] {
				errs = append(errs, fmt.Errorf("preset %q references non-existent participant: %q",
					name, pID))
			}
		}

		// Validate that preset participants are eligible for their roles
		if preset.Mode == "review" {
			// Author must be eligible for author role
			for _, p := range r.Participants {
				if p.ID == preset.Author {
					if !slices.Contains(p.EligibleRoles, RoleAuthor) {
						errs = append(errs, fmt.Errorf("preset %q: author %q is not eligible for author role",
							name, preset.Author))
					}
					break
				}
			}

			// All required reviewers must be eligible for reviewer role
			for _, reviewerID := range preset.RequiredReviewers {
				for _, p := range r.Participants {
					if p.ID == reviewerID {
						if !slices.Contains(p.EligibleRoles, RoleReviewer) {
							errs = append(errs, fmt.Errorf("preset %q: reviewer %q is not eligible for reviewer role",
								name, reviewerID))
						}
						break
					}
				}
			}
		}

		if preset.Mode == "solo" {
			// Author must be eligible for author role
			for _, p := range r.Participants {
				if p.ID == preset.Author {
					if !slices.Contains(p.EligibleRoles, RoleAuthor) {
						errs = append(errs, fmt.Errorf("preset %q: author %q is not eligible for author role",
							name, preset.Author))
					}
					break
				}
			}
		}
	}

	// Validate DefaultPreset
	if r.DefaultPreset == "" {
		errs = append(errs, fmt.Errorf("default_preset is empty"))
	} else if !seenPresetNames[r.DefaultPreset] {
		errs = append(errs, fmt.Errorf("default_preset references non-existent preset: %q", r.DefaultPreset))
	} else if r.Presets[r.DefaultPreset].Mode != "review" {
		errs = append(errs, fmt.Errorf("default_preset %q must have mode review", r.DefaultPreset))
	}

	// Validate AllowedDestinations (always, not just for mixed policy)
	seenAllowedDests := make(map[string]bool)
	for _, dest := range r.Policy.AllowedDestinations {
		seenAllowedDests[dest] = true
		if !seenEndpointNames[dest] {
			errs = append(errs, fmt.Errorf("allowed_destinations references non-existent endpoint: %q", dest))
		}
	}

	// Validate LocalOnly policy
	if r.Policy.LocalOnly {
		for _, p := range r.Participants {
			if p.Route == "api" && p.Endpoint != "" {
				ep := endpointMap[p.Endpoint]
				if ep.DeclaredLocation == "corporate" || ep.DeclaredLocation == "cloud" {
					errs = append(errs, fmt.Errorf("local_only policy rejects endpoint %q with declared_location %q on participant %q",
						p.Endpoint, ep.DeclaredLocation, p.ID))
				}
				if ep.DeclaredExecution != "local" {
					errs = append(errs, fmt.Errorf("local_only policy rejects endpoint %q with declared_execution %q on participant %q",
						p.Endpoint, ep.DeclaredExecution, p.ID))
				}
			}
		}
	}

	// Validate mixed policy (not LocalOnly)
	if !r.Policy.LocalOnly {
		// Check that every participant's endpoint that is corporate/cloud is in allowed destinations
		for _, p := range r.Participants {
			if p.Route == "api" && p.Endpoint != "" {
				ep := endpointMap[p.Endpoint]
				if ep.DeclaredLocation == "corporate" || ep.DeclaredLocation == "cloud" {
					if !seenAllowedDests[p.Endpoint] {
						errs = append(errs, fmt.Errorf("endpoint %q (declared_location %q) on participant %q must be in allowed_destinations",
							p.Endpoint, ep.DeclaredLocation, p.ID))
					}
				}
			}
		}
	}

	// Validate endpoint tokens_per_byte_max
	for _, ep := range r.Endpoints {
		if ep.TokensPerByteMax != nil {
			if math.IsNaN(*ep.TokensPerByteMax) {
				errs = append(errs, fmt.Errorf("endpoint %q tokens_per_byte_max must not be NaN", ep.Name))
			} else if *ep.TokensPerByteMax <= 0 {
				errs = append(errs, fmt.Errorf("endpoint %q tokens_per_byte_max must be greater than 0", ep.Name))
			}
		}
	}

	// Validate participant context_tokens and bytes_per_token
	for _, p := range r.Participants {
		if p.ContextTokens != nil && *p.ContextTokens < 1 {
			errs = append(errs, fmt.Errorf("participant %q context_tokens must be at least 1", p.ID))
		}
		if p.BytesPerToken != nil {
			if math.IsNaN(*p.BytesPerToken) {
				errs = append(errs, fmt.Errorf("participant %q bytes_per_token must not be NaN", p.ID))
			} else if *p.BytesPerToken <= 0 {
				errs = append(errs, fmt.Errorf("participant %q bytes_per_token must be greater than 0", p.ID))
			}
		}
	}

	// Validate Deadlines
	if r.Deadlines.Attempt != nil && r.Deadlines.Attempt.Duration <= 0 {
		errs = append(errs, fmt.Errorf("deadlines.attempt must be positive"))
	}
	if r.Deadlines.Turn != nil && r.Deadlines.Turn.Duration <= 0 {
		errs = append(errs, fmt.Errorf("deadlines.turn must be positive"))
	}
	if r.Deadlines.Delivery != nil && r.Deadlines.Delivery.Duration <= 0 {
		errs = append(errs, fmt.Errorf("deadlines.delivery must be positive"))
	}
	// Validate turn >= attempt
	if r.Deadlines.Turn != nil && r.Deadlines.Attempt != nil {
		if r.Deadlines.Turn.Duration < r.Deadlines.Attempt.Duration {
			errs = append(errs, fmt.Errorf("deadlines.turn must be at least deadlines.attempt"))
		}
	}

	// Validate Budgets
	if r.Budgets.CalibrationSafety != nil {
		if math.IsNaN(*r.Budgets.CalibrationSafety) {
			errs = append(errs, fmt.Errorf("budgets.calibration_safety must not be NaN"))
		} else if *r.Budgets.CalibrationSafety <= 0 || *r.Budgets.CalibrationSafety > 1.0 {
			errs = append(errs, fmt.Errorf("budgets.calibration_safety must be greater than 0 and at most 1.0"))
		}
	}
	if r.Budgets.ContextMargin != nil {
		if math.IsNaN(*r.Budgets.ContextMargin) {
			errs = append(errs, fmt.Errorf("budgets.context_margin must not be NaN"))
		} else if *r.Budgets.ContextMargin < 0 || *r.Budgets.ContextMargin >= 1 {
			errs = append(errs, fmt.Errorf("budgets.context_margin must be at least 0 and less than 1"))
		}
	}
	if r.Budgets.MessageOverheadTokens != nil && *r.Budgets.MessageOverheadTokens < 1 {
		errs = append(errs, fmt.Errorf("budgets.message_overhead_tokens must be at least 1"))
	}
	if r.Budgets.RequestOverheadTokens != nil && *r.Budgets.RequestOverheadTokens < 1 {
		errs = append(errs, fmt.Errorf("budgets.request_overhead_tokens must be at least 1"))
	}
	if r.Budgets.AuthorMaxOutputTokens != nil && *r.Budgets.AuthorMaxOutputTokens < 1 {
		errs = append(errs, fmt.Errorf("budgets.author_max_output_tokens must be at least 1"))
	}
	if r.Budgets.ReviewerMaxOutputTokens != nil && *r.Budgets.ReviewerMaxOutputTokens < 1 {
		errs = append(errs, fmt.Errorf("budgets.reviewer_max_output_tokens must be at least 1"))
	}
	if r.Budgets.DeliveryMaxTokens != nil && *r.Budgets.DeliveryMaxTokens < 1 {
		errs = append(errs, fmt.Errorf("budgets.delivery_max_tokens must be at least 1"))
	}
	if r.Budgets.TurnMaxEstimatedTokens != nil && *r.Budgets.TurnMaxEstimatedTokens < 1 {
		errs = append(errs, fmt.Errorf("budgets.turn_max_estimated_tokens must be at least 1"))
	}

	// Validate Engine
	if r.Engine.StopGrace != nil && r.Engine.StopGrace.Duration <= 0 {
		errs = append(errs, fmt.Errorf("engine.stop_grace must be positive"))
	}

	// Validate Pools
	for name, pool := range r.Pools {
		if pool.Concurrency != nil && *pool.Concurrency < 1 {
			errs = append(errs, fmt.Errorf("pools[%q].concurrency must be at least 1", name))
		}
		if pool.UncertainReleaseAfter != nil && pool.UncertainReleaseAfter.Duration < 0 {
			errs = append(errs, fmt.Errorf("pools[%q].uncertain_release_after must not be negative", name))
		}
	}

	// Validate that all participant pools reference declared pools or use default
	for _, p := range r.Participants {
		poolName := p.Pool
		if poolName == "" {
			poolName = "default"
		}
		if poolName != "default" {
			if _, ok := r.Pools[poolName]; !ok {
				errs = append(errs, fmt.Errorf("participant %q references non-existent pool: %q", p.ID, poolName))
			}
		}
	}

	// Validate Preset deadlines and budgets overrides
	for name, preset := range r.Presets {
		if preset.Deadlines != nil {
			if preset.Deadlines.Attempt != nil && preset.Deadlines.Attempt.Duration <= 0 {
				errs = append(errs, fmt.Errorf("presets[%q].deadlines.attempt must be positive", name))
			}
			if preset.Deadlines.Turn != nil && preset.Deadlines.Turn.Duration <= 0 {
				errs = append(errs, fmt.Errorf("presets[%q].deadlines.turn must be positive", name))
			}
			if preset.Deadlines.Delivery != nil && preset.Deadlines.Delivery.Duration <= 0 {
				errs = append(errs, fmt.Errorf("presets[%q].deadlines.delivery must be positive", name))
			}
		}

		if preset.Budgets != nil {
			if preset.Budgets.CalibrationSafety != nil {
				if math.IsNaN(*preset.Budgets.CalibrationSafety) {
					errs = append(errs, fmt.Errorf("presets[%q].budgets.calibration_safety must not be NaN", name))
				} else if *preset.Budgets.CalibrationSafety <= 0 || *preset.Budgets.CalibrationSafety > 1.0 {
					errs = append(errs, fmt.Errorf("presets[%q].budgets.calibration_safety must be greater than 0 and at most 1.0", name))
				}
			}
			if preset.Budgets.ContextMargin != nil {
				if math.IsNaN(*preset.Budgets.ContextMargin) {
					errs = append(errs, fmt.Errorf("presets[%q].budgets.context_margin must not be NaN", name))
				} else if *preset.Budgets.ContextMargin < 0 || *preset.Budgets.ContextMargin >= 1 {
					errs = append(errs, fmt.Errorf("presets[%q].budgets.context_margin must be at least 0 and less than 1", name))
				}
			}
			if preset.Budgets.MessageOverheadTokens != nil && *preset.Budgets.MessageOverheadTokens < 1 {
				errs = append(errs, fmt.Errorf("presets[%q].budgets.message_overhead_tokens must be at least 1", name))
			}
			if preset.Budgets.RequestOverheadTokens != nil && *preset.Budgets.RequestOverheadTokens < 1 {
				errs = append(errs, fmt.Errorf("presets[%q].budgets.request_overhead_tokens must be at least 1", name))
			}
			if preset.Budgets.AuthorMaxOutputTokens != nil && *preset.Budgets.AuthorMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("presets[%q].budgets.author_max_output_tokens must be at least 1", name))
			}
			if preset.Budgets.ReviewerMaxOutputTokens != nil && *preset.Budgets.ReviewerMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("presets[%q].budgets.reviewer_max_output_tokens must be at least 1", name))
			}
			if preset.Budgets.DeliveryMaxTokens != nil && *preset.Budgets.DeliveryMaxTokens < 1 {
				errs = append(errs, fmt.Errorf("presets[%q].budgets.delivery_max_tokens must be at least 1", name))
			}
			if preset.Budgets.TurnMaxEstimatedTokens != nil && *preset.Budgets.TurnMaxEstimatedTokens < 1 {
				errs = append(errs, fmt.Errorf("presets[%q].budgets.turn_max_estimated_tokens must be at least 1", name))
			}
		}

		// Validate turn >= attempt for preset
		if preset.Deadlines != nil && preset.Deadlines.Turn != nil && preset.Deadlines.Attempt != nil {
			if preset.Deadlines.Turn.Duration < preset.Deadlines.Attempt.Duration {
				errs = append(errs, fmt.Errorf("presets[%q].deadlines.turn must be at least presets[%q].deadlines.attempt", name, name))
			}
		}
	}

	// Validate Quick profiles
	for profileName, qp := range r.Quick {
		// Quick profile preset must be non-empty and exist
		if qp.Preset == "" {
			errs = append(errs, fmt.Errorf("quick profile %q: preset must be non-empty", profileName))
		} else if !seenPresetNames[qp.Preset] {
			errs = append(errs, fmt.Errorf("quick profile %q: preset %q does not exist", profileName, qp.Preset))
		}

		// Check for unimplemented properties
		unimplementedProps := map[string]any{
			"eligible_request_categories": qp.EligibleRequestCategories,
			"protected_paths":             qp.ProtectedPaths,
			"required_review_triggers":    qp.RequiredReviewTriggers,
			"output_escalation_rules":     qp.OutputEscalationRules,
		}
		for propName, propValue := range unimplementedProps {
			if propValue != nil {
				errs = append(errs, fmt.Errorf("quick profile %q: %s is not yet implemented", profileName, propName))
			}
		}

		// Validate budgets overrides in quick profile
		if qp.Budgets != nil {
			if qp.Budgets.CalibrationSafety != nil {
				if math.IsNaN(*qp.Budgets.CalibrationSafety) {
					errs = append(errs, fmt.Errorf("quick profile %q: budgets.calibration_safety must not be NaN", profileName))
				} else if *qp.Budgets.CalibrationSafety <= 0 || *qp.Budgets.CalibrationSafety > 1.0 {
					errs = append(errs, fmt.Errorf("quick profile %q: budgets.calibration_safety must be greater than 0 and at most 1.0", profileName))
				}
			}
			if qp.Budgets.ContextMargin != nil {
				if math.IsNaN(*qp.Budgets.ContextMargin) {
					errs = append(errs, fmt.Errorf("quick profile %q: budgets.context_margin must not be NaN", profileName))
				} else if *qp.Budgets.ContextMargin < 0 || *qp.Budgets.ContextMargin >= 1 {
					errs = append(errs, fmt.Errorf("quick profile %q: budgets.context_margin must be at least 0 and less than 1", profileName))
				}
			}
			if qp.Budgets.MessageOverheadTokens != nil && *qp.Budgets.MessageOverheadTokens < 1 {
				errs = append(errs, fmt.Errorf("quick profile %q: budgets.message_overhead_tokens must be at least 1", profileName))
			}
			if qp.Budgets.RequestOverheadTokens != nil && *qp.Budgets.RequestOverheadTokens < 1 {
				errs = append(errs, fmt.Errorf("quick profile %q: budgets.request_overhead_tokens must be at least 1", profileName))
			}
			if qp.Budgets.AuthorMaxOutputTokens != nil && *qp.Budgets.AuthorMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("quick profile %q: budgets.author_max_output_tokens must be at least 1", profileName))
			}
			if qp.Budgets.ReviewerMaxOutputTokens != nil && *qp.Budgets.ReviewerMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("quick profile %q: budgets.reviewer_max_output_tokens must be at least 1", profileName))
			}
			if qp.Budgets.DeliveryMaxTokens != nil && *qp.Budgets.DeliveryMaxTokens < 1 {
				errs = append(errs, fmt.Errorf("quick profile %q: budgets.delivery_max_tokens must be at least 1", profileName))
			}
			if qp.Budgets.TurnMaxEstimatedTokens != nil && *qp.Budgets.TurnMaxEstimatedTokens < 1 {
				errs = append(errs, fmt.Errorf("quick profile %q: budgets.turn_max_estimated_tokens must be at least 1", profileName))
			}
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
