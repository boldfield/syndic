package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strings"
)

func validateAllowedHostPattern(pattern string) error {
	if pattern == "" {
		return errors.New("pattern cannot be empty")
	}

	// Check for IP literals - not allowed when allowlist is set
	if net.ParseIP(pattern) != nil {
		return errors.New("IP literals are not allowed in allowlist")
	}

	// Check if it looks like an IPv6 (has colons)
	if strings.Contains(pattern, ":") {
		return errors.New("IP literals are not allowed in allowlist")
	}

	// Valid patterns:
	// - exact host: "example.com"
	// - subdomains only: "*.example.com" (matches a.example.com but not example.com)
	// - both: "**.example.com" (matches example.com and a.example.com)

	wildcardCount := strings.Count(pattern, "*")
	if wildcardCount > 2 {
		return fmt.Errorf("too many wildcards in pattern %q (max 2)", pattern)
	}

	if wildcardCount == 1 {
		// Single wildcard: must be "*.domain"
		if !strings.HasPrefix(pattern, "*.") {
			return fmt.Errorf("single wildcard must be at the start: %q", pattern)
		}
		baseDomain := strings.TrimPrefix(pattern, "*.")
		if strings.Contains(baseDomain, "*") {
			return fmt.Errorf("invalid wildcard placement in %q", pattern)
		}
		if strings.Contains(baseDomain, ".") == false {
			return fmt.Errorf("wildcard pattern must have at least one dot: %q", pattern)
		}
	} else if wildcardCount == 2 {
		// Double wildcard: must be "**.domain"
		if !strings.HasPrefix(pattern, "**.") {
			return fmt.Errorf("double wildcard must be at the start: %q", pattern)
		}
		baseDomain := strings.TrimPrefix(pattern, "**.")
		if strings.Contains(baseDomain, "*") {
			return fmt.Errorf("invalid wildcard placement in %q", pattern)
		}
		if strings.Contains(baseDomain, ".") == false {
			return fmt.Errorf("wildcard pattern must have at least one dot: %q", pattern)
		}
	} else if wildcardCount == 0 {
		// Exact match: no wildcards
		// No suffix matching allowed, so pattern must not start with a dot
		if strings.HasPrefix(pattern, ".") {
			return fmt.Errorf("suffix matching is not allowed: %q", pattern)
		}
	}

	return nil
}

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

		// Validate spending_control (optional, "" is allowed)
		if ep.SpendingControl != "" && ep.SpendingControl != "external" {
			errs = append(errs, fmt.Errorf("endpoint %q has unknown spending_control: %q",
				ep.Name, ep.SpendingControl))
		}
		// Validate that spending_control = "external" is only on metered endpoints
		if ep.SpendingControl == "external" && ep.DeclaredBilling != "metered" {
			errs = append(errs, fmt.Errorf("endpoint %q spending_control=external can only be used with declared_billing=metered",
				ep.Name))
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

	// Validate Revision
	if r.Revision.MaxRounds != nil && (*r.Revision.MaxRounds < 0 || *r.Revision.MaxRounds > 5) {
		errs = append(errs, fmt.Errorf("revision.max_rounds must be between 0 and 5"))
	}
	if r.Revision.MaxRoundsCeiling != nil && (*r.Revision.MaxRoundsCeiling < 0 || *r.Revision.MaxRoundsCeiling > 5) {
		errs = append(errs, fmt.Errorf("revision.max_rounds_ceiling must be between 0 and 5"))
	}
	if r.Revision.MaxRounds != nil && r.Revision.MaxRoundsCeiling != nil {
		if *r.Revision.MaxRounds > *r.Revision.MaxRoundsCeiling {
			errs = append(errs, fmt.Errorf("revision.max_rounds (%d) must not exceed revision.max_rounds_ceiling (%d)", *r.Revision.MaxRounds, *r.Revision.MaxRoundsCeiling))
		}
	}

	// Validate tool sets
	validAuthorTools := map[string]bool{"read": true, "web": true, "write": true}
	validReviewerTools := map[string]bool{"read": true, "web": true}

	for name, preset := range r.Presets {
		// Validate author tools
		for _, tool := range preset.Tools {
			if !validAuthorTools[tool] {
				errs = append(errs, fmt.Errorf("preset %q: invalid author tool %q", name, tool))
			}
		}

		// Validate reviewer tools
		for _, tool := range preset.ReviewerTools {
			if !validReviewerTools[tool] {
				errs = append(errs, fmt.Errorf("preset %q: invalid reviewer tool %q", name, tool))
			}
		}

		// Solo preset cannot have write tools
		if preset.Mode == "solo" && slices.Contains(preset.Tools, "write") {
			errs = append(errs, fmt.Errorf("preset %q: solo mode cannot have write tools", name))
		}

		// Check if preset has file-derived content tools
		hasRead := slices.Contains(preset.Tools, "read") || slices.Contains(preset.ReviewerTools, "read")
		hasWrite := slices.Contains(preset.Tools, "write")
		needsEgressCheck := hasRead || hasWrite

		// If preset has read or write, check file egress destinations for all participants
		if needsEgressCheck {
			allParticipants := []string{preset.Author}
			allParticipants = append(allParticipants, preset.RequiredReviewers...)

			localEndpoints := make(map[string]bool)
			for _, ep := range r.Endpoints {
				if ep.DeclaredExecution == "local" {
					localEndpoints[ep.Name] = true
				}
			}
			egressDestinations := make(map[string]bool)
			if r.Tools != nil {
				for _, dest := range r.Tools.FileEgressDestinations {
					egressDestinations[dest] = true
				}
			}

			for _, pID := range allParticipants {
				for _, p := range r.Participants {
					if p.ID == pID {
						if p.Route == "api" && p.Endpoint != "" {
							// API participant must either be on a local endpoint or in egress destinations
							if !localEndpoints[p.Endpoint] && !egressDestinations[p.Endpoint] {
								errs = append(errs, fmt.Errorf("preset %q: participant %q on endpoint %q requires file egress, but endpoint is not local and not in tools.file_egress_destinations", name, pID, p.Endpoint))
							}
						} else if p.Route == "cli" {
							// CLI participant must have tools only if on a local endpoint or in egress destinations
							// Since CLI participants don't have an endpoint field in the traditional sense,
							// they need an explicit endpoint or must be considered local
							if p.Endpoint != "" {
								if !localEndpoints[p.Endpoint] && !egressDestinations[p.Endpoint] {
									errs = append(errs, fmt.Errorf("preset %q: CLI participant %q on endpoint %q requires file egress, but endpoint is not local and not in tools.file_egress_destinations", name, pID, p.Endpoint))
								}
							}
						}
						break
					}
				}
			}
		}

		// If preset has web tools, check that each endpoint used by a web participant has referee_model
		authorHasWeb := slices.Contains(preset.Tools, "web")
		reviewerHasWeb := slices.Contains(preset.ReviewerTools, "web")

		if authorHasWeb {
			// Author has web; check author's endpoint
			for _, p := range r.Participants {
				if p.ID == preset.Author && p.Route == "api" && p.Endpoint != "" {
					ep := endpointMap[p.Endpoint]
					if ep.RefereeModel == "" {
						errs = append(errs, fmt.Errorf("preset %q: endpoint %q used by author has no referee_model but web tools are required", name, p.Endpoint))
					}
					break
				}
			}
		}

		if reviewerHasWeb {
			// Reviewers have web; check each reviewer's endpoint
			for _, reviewerID := range preset.RequiredReviewers {
				for _, p := range r.Participants {
					if p.ID == reviewerID && p.Route == "api" && p.Endpoint != "" {
						ep := endpointMap[p.Endpoint]
						if ep.RefereeModel == "" {
							errs = append(errs, fmt.Errorf("preset %q: endpoint %q used by reviewer %q has no referee_model but web tools are required", name, p.Endpoint, reviewerID))
						}
						break
					}
				}
			}
		}

		// Check tools.allow_web_with_read constraint
		if r.Tools != nil && r.Tools.AllowWebWithRead != nil && !*r.Tools.AllowWebWithRead {
			// If allow_web_with_read is false, preset cannot have both web and (read or write) anywhere
			hasWeb := slices.Contains(preset.Tools, "web") || slices.Contains(preset.ReviewerTools, "web")
			hasRead := slices.Contains(preset.Tools, "read") || slices.Contains(preset.ReviewerTools, "read")
			hasWrite := slices.Contains(preset.Tools, "write")

			if hasWeb && (hasRead || hasWrite) {
				// Find all participants with web and determine which will receive file-derived content
				webParticipants := []string{}
				fileParticipants := []string{}

				if slices.Contains(preset.Tools, "web") {
					webParticipants = append(webParticipants, preset.Author)
				}
				if slices.Contains(preset.ReviewerTools, "web") {
					webParticipants = append(webParticipants, preset.RequiredReviewers...)
				}

				// File content recipients: author if read or write, reviewers if any tool
				if slices.Contains(preset.Tools, "read") || slices.Contains(preset.Tools, "write") {
					fileParticipants = append(fileParticipants, preset.Author)
				}
				if hasRead || (hasWrite && len(preset.ReviewerTools) > 0) {
					fileParticipants = append(fileParticipants, preset.RequiredReviewers...)
				}

				// Check if any web participant also receives file content
				for _, wp := range webParticipants {
					for _, fp := range fileParticipants {
						if wp == fp {
							errs = append(errs, fmt.Errorf("preset %q: cannot combine web and read/write tools with tools.allow_web_with_read=false (affected participant: %q)", name, wp))
							break
						}
					}
				}
			}
		}
	}

	// Validate endpoint referee configuration
	for _, ep := range r.Endpoints {
		// If referee_endpoint is set, it must reference an endpoint with local execution
		if ep.RefereeEndpoint != "" {
			found := false
			for _, otherEp := range r.Endpoints {
				if otherEp.Name == ep.RefereeEndpoint {
					if otherEp.DeclaredExecution != "local" {
						errs = append(errs, fmt.Errorf("endpoint %q: referee_endpoint %q does not have local execution", ep.Name, ep.RefereeEndpoint))
					}
					found = true
					break
				}
			}
			if !found {
				errs = append(errs, fmt.Errorf("endpoint %q: referee_endpoint references non-existent endpoint: %q", ep.Name, ep.RefereeEndpoint))
			}
		}
	}

	// Validate tools configuration
	if r.Tools != nil {
		if r.Tools.MaxFetchesPerTurn != nil && *r.Tools.MaxFetchesPerTurn < 1 {
			errs = append(errs, fmt.Errorf("tools.max_fetches_per_turn must be at least 1"))
		}
		if r.Tools.MaxCallsPerTurn != nil && *r.Tools.MaxCallsPerTurn < 1 {
			errs = append(errs, fmt.Errorf("tools.max_calls_per_turn must be at least 1"))
		}
		if r.Tools.MaxReviewerCalls != nil && *r.Tools.MaxReviewerCalls < 1 {
			errs = append(errs, fmt.Errorf("tools.max_reviewer_calls must be at least 1"))
		}
		if r.Tools.MaxResultBytes != nil && *r.Tools.MaxResultBytes < 1 {
			errs = append(errs, fmt.Errorf("tools.max_result_bytes must be at least 1"))
		}
		if r.Tools.Referee != nil && r.Tools.Referee.MaxOutputTokens != nil && *r.Tools.Referee.MaxOutputTokens < 1 {
			errs = append(errs, fmt.Errorf("tools.referee.max_output_tokens must be at least 1"))
		}

		// Validate file_egress_destinations reference valid endpoints
		for _, dest := range r.Tools.FileEgressDestinations {
			if !seenEndpointNames[dest] {
				errs = append(errs, fmt.Errorf("tools.file_egress_destinations references non-existent endpoint: %q", dest))
			}
		}

		// Validate web allowed_hosts patterns
		if r.Tools.Web != nil {
			for _, pattern := range r.Tools.Web.AllowedHosts {
				// Check for invalid patterns
				if err := validateAllowedHostPattern(pattern); err != nil {
					errs = append(errs, fmt.Errorf("tools.web.allowed_hosts: %w", err))
				}
			}
		}
	}

	// Validate Preset deadlines, budgets and revision overrides
	for name, preset := range r.Presets {
		// Validate revision overrides
		if preset.Revision != nil {
			if preset.Revision.MaxRounds != nil && (*preset.Revision.MaxRounds < 0 || *preset.Revision.MaxRounds > 5) {
				errs = append(errs, fmt.Errorf("presets[%q].revision.max_rounds must be between 0 and 5", name))
			}
			if preset.Revision.MaxRoundsCeiling != nil && (*preset.Revision.MaxRoundsCeiling < 0 || *preset.Revision.MaxRoundsCeiling > 5) {
				errs = append(errs, fmt.Errorf("presets[%q].revision.max_rounds_ceiling must be between 0 and 5", name))
			}
			if preset.Revision.MaxRounds != nil && preset.Revision.MaxRoundsCeiling != nil {
				if *preset.Revision.MaxRounds > *preset.Revision.MaxRoundsCeiling {
					errs = append(errs, fmt.Errorf("presets[%q].revision.max_rounds (%d) must not exceed presets[%q].revision.max_rounds_ceiling (%d)", name, *preset.Revision.MaxRounds, name, *preset.Revision.MaxRoundsCeiling))
				}
			}
		}

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

		// Validate turn >= attempt for preset (using effective merged values)
		effAttempt := r.Deadlines.Attempt
		effTurn := r.Deadlines.Turn
		if preset.Deadlines != nil {
			if preset.Deadlines.Attempt != nil {
				effAttempt = preset.Deadlines.Attempt
			}
			if preset.Deadlines.Turn != nil {
				effTurn = preset.Deadlines.Turn
			}
		}
		if effTurn != nil && effAttempt != nil {
			if effTurn.Duration < effAttempt.Duration {
				errs = append(errs, fmt.Errorf("presets[%q]: effective turn deadline must be at least effective attempt deadline", name))
			}
		}

		// Validate effective max_rounds <= effective max_rounds_ceiling for preset
		effMaxRounds := r.Revision.MaxRounds
		effMaxRoundsCeiling := r.Revision.MaxRoundsCeiling
		if preset.Revision != nil {
			if preset.Revision.MaxRounds != nil {
				effMaxRounds = preset.Revision.MaxRounds
			}
			if preset.Revision.MaxRoundsCeiling != nil {
				effMaxRoundsCeiling = preset.Revision.MaxRoundsCeiling
			}
		}
		if effMaxRounds != nil && effMaxRoundsCeiling != nil {
			if *effMaxRounds > *effMaxRoundsCeiling {
				errs = append(errs, fmt.Errorf("presets[%q]: effective max_rounds (%d) must not exceed effective max_rounds_ceiling (%d)", name, *effMaxRounds, *effMaxRoundsCeiling))
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

		// Check for unimplemented properties (iterate in fixed order for deterministic errors)
		unimplementedProps := []struct {
			name  string
			value any
		}{
			{"eligible_request_categories", qp.EligibleRequestCategories},
			{"protected_paths", qp.ProtectedPaths},
			{"required_review_triggers", qp.RequiredReviewTriggers},
			{"output_escalation_rules", qp.OutputEscalationRules},
		}
		for _, prop := range unimplementedProps {
			if prop.value != nil {
				errs = append(errs, fmt.Errorf("quick profile %q: %s is not yet implemented", profileName, prop.name))
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
