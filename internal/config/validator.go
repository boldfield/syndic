package config

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

// validateSearchConfig checks [tools.web.search]: a provider name, an exact
// provider hostname and a secret reference are all required.
func validateSearchConfig(s *SearchConfig) []error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.Provider == "" {
		errs = append(errs, errors.New("tools.web.search.provider is required"))
	}
	if strings.Contains(s.Host, "*") {
		errs = append(errs, fmt.Errorf("tools.web.search.host %q must be an exact hostname", s.Host))
	} else if err := validateAllowedHostPattern(s.Host); err != nil {
		errs = append(errs, fmt.Errorf("tools.web.search.host: %w", err))
	}
	switch {
	case s.Auth == nil:
		errs = append(errs, errors.New("tools.web.search.auth (a secret reference) is required"))
	case s.Auth.Source != "env" && s.Auth.Source != "file":
		errs = append(errs, fmt.Errorf("tools.web.search.auth.source must be env or file, got %q", s.Auth.Source))
	case s.Auth.Name == "":
		errs = append(errs, errors.New("tools.web.search.auth.name is required"))
	}
	return errs
}

func validateAllowedHostPattern(pattern string) error {
	if pattern == "" {
		return errors.New("pattern cannot be empty")
	}

	// Check for paths - patterns must be hostnames only, no slashes
	if strings.Contains(pattern, "/") {
		return fmt.Errorf("pattern %q must be a hostname, not a path", pattern)
	}

	// Extract the base domain (the part without wildcards) for validation
	var baseDomain string
	wildcardCount := strings.Count(pattern, "*")

	if wildcardCount > 2 {
		return fmt.Errorf("too many wildcards in pattern %q (max 2)", pattern)
	}

	switch wildcardCount {
	case 2:
		// Double wildcard: must be "**.domain"
		if !strings.HasPrefix(pattern, "**.") {
			return fmt.Errorf("double wildcard must be at the start: %q", pattern)
		}
		baseDomain = strings.TrimPrefix(pattern, "**.")
		if strings.Contains(baseDomain, "*") {
			return fmt.Errorf("invalid wildcard placement in %q", pattern)
		}
		if !strings.Contains(baseDomain, ".") {
			return fmt.Errorf("wildcard pattern must have at least one dot: %q", pattern)
		}
	case 1:
		// Single wildcard: must be "*.domain"
		if !strings.HasPrefix(pattern, "*.") {
			return fmt.Errorf("single wildcard must be at the start: %q", pattern)
		}
		baseDomain = strings.TrimPrefix(pattern, "*.")
		if strings.Contains(baseDomain, "*") {
			return fmt.Errorf("invalid wildcard placement in %q", pattern)
		}
		if !strings.Contains(baseDomain, ".") {
			return fmt.Errorf("wildcard pattern must have at least one dot: %q", pattern)
		}
	case 0:
		// Exact match: no wildcards
		// No suffix matching allowed, so pattern must not start with a dot
		if strings.HasPrefix(pattern, ".") {
			return fmt.Errorf("suffix matching is not allowed: %q", pattern)
		}
		baseDomain = pattern
	}

	// Check for IPv6 addresses (bare or bracketed) - not allowed
	if strings.HasPrefix(baseDomain, "[") && strings.HasSuffix(baseDomain, "]") {
		if ip := net.ParseIP(baseDomain[1 : len(baseDomain)-1]); ip != nil {
			return fmt.Errorf("IPv6 literals are not allowed in allowlist: %q", pattern)
		}
	}
	if strings.Contains(baseDomain, ":") {
		// Could be IPv6 address or port separator
		// Check if it's an IPv6 literal
		if ip := net.ParseIP(baseDomain); ip != nil && ip.To4() == nil {
			return fmt.Errorf("IPv6 literals are not allowed in allowlist: %q", pattern)
		}
		// Not an IPv6 address, but contains colon - likely a port
		return fmt.Errorf("pattern %q must not include port; use just the hostname", pattern)
	}

	// Check for IPv4 addresses - not allowed when allowlist is set
	if net.ParseIP(baseDomain) != nil {
		return fmt.Errorf("IP literals are not allowed in allowlist: %q", pattern)
	}

	if err := validateHostLabels(baseDomain); err != nil {
		return fmt.Errorf("pattern %q is not a valid hostname: %w", pattern, err)
	}

	// Canonicalize domain: convert to ASCII (punycode if needed) and lowercase
	canonicalDomain, err := idna.Lookup.ToASCII(baseDomain)
	if err != nil {
		return fmt.Errorf("pattern %q contains invalid hostname: %w", pattern, err)
	}

	// Decode A-labels with the registration profile, then apply the IDNA2008
	// derived properties and contextual rules that UTS #46 alone does not.
	decoded, err := idnaRegistration.ToUnicode(canonicalDomain)
	if err != nil {
		return fmt.Errorf("pattern %q contains invalid hostname: %w", pattern, err)
	}
	for _, label := range strings.Split(decoded, ".") {
		if err := checkIDNA2008Label(label); err != nil {
			return fmt.Errorf("pattern %q is not a valid IDNA2008 hostname: %w", pattern, err)
		}
	}

	// Check if the canonicalized version matches (lowercase and punycode)
	canonicalLower := strings.ToLower(canonicalDomain)
	if canonicalDomain != canonicalLower {
		return fmt.Errorf("pattern %q must be in canonical form (lowercase): canonical is %q", pattern, canonicalLower)
	}

	// If domain was converted by IDNA (e.g., non-ASCII to punycode), reject the non-canonical form
	if canonicalDomain != baseDomain {
		// Build the canonical pattern
		var canonicalPattern string
		switch wildcardCount {
		case 2:
			canonicalPattern = "**." + canonicalDomain
		case 1:
			canonicalPattern = "*." + canonicalDomain
		default:
			canonicalPattern = canonicalDomain
		}
		return fmt.Errorf("pattern %q is not in canonical form; use %q instead", pattern, canonicalPattern)
	}

	return nil
}

// checkIDNA2008Label applies the RFC 5892 derived-property algorithm (§3)
// and the CONTEXTO rules (Appendix A) to one decoded U-label. ContextJ
// (U+200C/U+200D) and the Bidi rule are enforced by idnaRegistration.
func checkIDNA2008Label(label string) error {
	runes := []rune(label)
	for i, r := range runes {
		switch idna2008Property(r) {
		case idnaPValid:
		case idnaContextJ:
			// Validated by the CheckJoiners option of idnaRegistration.
		case idnaContextO:
			if !contextORuleHolds(runes, i) {
				return fmt.Errorf("code point %U fails its IDNA2008 contextual rule", r)
			}
		default:
			return fmt.Errorf("code point %U is not allowed in hostnames", r)
		}
	}
	return nil
}

type idnaProperty int

const (
	idnaDisallowed idnaProperty = iota
	idnaPValid
	idnaContextJ
	idnaContextO
)

// idnaExceptions is the RFC 5892 §2.6 Exceptions table.
var idnaExceptions = map[rune]idnaProperty{
	0x00DF: idnaPValid, 0x03C2: idnaPValid, 0x06FD: idnaPValid,
	0x06FE: idnaPValid, 0x0F0B: idnaPValid, 0x3007: idnaPValid,
	0x00B7: idnaContextO, 0x0375: idnaContextO, 0x05F3: idnaContextO,
	0x05F4: idnaContextO, 0x30FB: idnaContextO,
	0x0640: idnaDisallowed, 0x07FA: idnaDisallowed, 0x302E: idnaDisallowed,
	0x302F: idnaDisallowed, 0x3031: idnaDisallowed, 0x3032: idnaDisallowed,
	0x3033: idnaDisallowed, 0x3034: idnaDisallowed, 0x3035: idnaDisallowed,
	0x303B: idnaDisallowed,
}

// idna2008Property derives a code point's IDNA2008 property in the order
// given by RFC 5892 §3. Unstable code points are excluded beforehand by the
// UTS #46 registration mapping, which rejects anything NFKC_Casefold changes;
// unassigned code points have no general category and fall through to
// DISALLOWED.
func idna2008Property(r rune) idnaProperty {
	if p, ok := idnaExceptions[r]; ok {
		return p
	}
	if (r >= 0x0660 && r <= 0x0669) || (r >= 0x06F0 && r <= 0x06F9) {
		return idnaContextO
	}
	if r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
		return idnaPValid
	}
	if r == 0x200C || r == 0x200D {
		return idnaContextJ
	}
	if unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || unicode.Is(unicode.Variation_Selector, r) ||
		unicode.Is(unicode.Noncharacter_Code_Point, r) || unicode.Is(unicode.White_Space, r) {
		return idnaDisallowed
	}
	if (r >= 0x20D0 && r <= 0x20FF) || (r >= 0x1D100 && r <= 0x1D24F) {
		return idnaDisallowed // IgnorableBlocks
	}
	if (r >= 0x1100 && r <= 0x11FF) || (r >= 0xA960 && r <= 0xA97F) || (r >= 0xD7B0 && r <= 0xD7FF) {
		return idnaDisallowed // OldHangulJamo
	}
	if unicode.In(r, unicode.Ll, unicode.Lu, unicode.Lo, unicode.Nd, unicode.Lm, unicode.Mn, unicode.Mc) {
		return idnaPValid
	}
	return idnaDisallowed
}

// contextORuleHolds evaluates the RFC 5892 Appendix A rule for the CONTEXTO
// code point at runes[i].
func contextORuleHolds(runes []rune, i int) bool {
	r := runes[i]
	switch {
	case r == 0x00B7: // MIDDLE DOT: between two 'l'
		return i > 0 && i+1 < len(runes) && runes[i-1] == 'l' && runes[i+1] == 'l'
	case r == 0x0375: // GREEK LOWER NUMERAL SIGN: followed by Greek
		return i+1 < len(runes) && unicode.Is(unicode.Greek, runes[i+1])
	case r == 0x05F3 || r == 0x05F4: // HEBREW GERESH/GERSHAYIM: preceded by Hebrew
		return i > 0 && unicode.Is(unicode.Hebrew, runes[i-1])
	case r == 0x30FB: // KATAKANA MIDDLE DOT: label has Hiragana, Katakana or Han
		for _, o := range runes {
			if o != 0x30FB && unicode.In(o, unicode.Hiragana, unicode.Katakana, unicode.Han) {
				return true
			}
		}
		return false
	case r >= 0x0660 && r <= 0x0669: // ARABIC-INDIC DIGITS: no extended digits
		return !slices.ContainsFunc(runes, func(o rune) bool { return o >= 0x06F0 && o <= 0x06F9 })
	case r >= 0x06F0 && r <= 0x06F9: // EXTENDED ARABIC-INDIC DIGITS: no Arabic-Indic digits
		return !slices.ContainsFunc(runes, func(o rune) bool { return o >= 0x0660 && o <= 0x0669 })
	}
	return false
}

// idnaRegistration validates A-labels against IDNA2008 registration rules.
var idnaRegistration = idna.New(idna.ValidateForRegistration(), idna.StrictDomainName(true))

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
		if preset.Mode != "solo" && preset.Mode != "review" && preset.Mode != ModeDeliberate {
			errs = append(errs, fmt.Errorf("preset %q has unknown mode: %q", name, preset.Mode))
		}

		if preset.Mode == ModeDeliberate {
			errs = append(errs, r.validateDeliberatePreset(name, preset, seenParticipantIDs)...)
			continue
		}
		if preset.Proposers != nil || preset.Integrator != "" {
			errs = append(errs, fmt.Errorf("preset %q: proposers and integrator are valid only with mode deliberate", name))
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
		if preset.Mode == "review" || preset.Mode == "solo" {
			errs = append(errs, r.checkRoleEligible(name, "author", preset.Author, RoleAuthor)...)
		}
		if preset.Mode == "review" {
			for _, reviewerID := range preset.RequiredReviewers {
				errs = append(errs, r.checkRoleEligible(name, "reviewer", reviewerID, RoleReviewer)...)
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

	// The routing policy, one rule per participant. A deliberate preset is
	// held to it for every pair of participants between which it sends text.
	// Nothing is omitted to make a preset fit.
	refused := make(map[string][]string)
	for _, p := range r.Participants {
		reasons := r.routingRefusals(p, endpointMap, seenAllowedDests)
		if len(reasons) == 0 {
			continue
		}
		refused[p.ID] = reasons
		for _, why := range reasons {
			errs = append(errs, fmt.Errorf("%s on participant %q", why, p.ID))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(r.Presets)) {
		preset := r.Presets[name]
		if preset.Mode != ModeDeliberate {
			continue
		}
		var order []string
		pairs := make(map[string][]string)
		for _, tr := range preset.DeliberateTextRoutes() {
			if len(refused[tr.To]) == 0 {
				continue
			}
			if _, ok := pairs[tr.To]; !ok {
				order = append(order, tr.To)
			}
			pairs[tr.To] = append(pairs[tr.To], fmt.Sprintf("%s from %q to %q", tr.Content, tr.From, tr.To))
		}
		for _, to := range order {
			errs = append(errs, fmt.Errorf("preset %q: the routing policy refuses %s: %s",
				name, strings.Join(pairs[to], "; "), strings.Join(refused[to], "; ")))
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
	if r.Budgets.ProposerMaxOutputTokens != nil && *r.Budgets.ProposerMaxOutputTokens < 1 {
		errs = append(errs, fmt.Errorf("budgets.proposer_max_output_tokens must be at least 1"))
	}
	if r.Budgets.CritiqueMaxOutputTokens != nil && *r.Budgets.CritiqueMaxOutputTokens < 1 {
		errs = append(errs, fmt.Errorf("budgets.critique_max_output_tokens must be at least 1"))
	}
	if r.Budgets.IntegratorMaxOutputTokens != nil && *r.Budgets.IntegratorMaxOutputTokens < 1 {
		errs = append(errs, fmt.Errorf("budgets.integrator_max_output_tokens must be at least 1"))
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

		// Every participant file-derived content could reach must be on an
		// endpoint it may go to; the error names the participant and the route.
		for _, v := range r.FileEgressViolations(preset, false) {
			var part *Participant
			for i := range r.Participants {
				if r.Participants[i].ID == v.Participant {
					part = &r.Participants[i]
					break
				}
			}
			switch {
			case part.Route != "api":
				errs = append(errs, fmt.Errorf("preset %q: CLI participant %q requires file egress by route %q; use an api participant on an endpoint with local execution or in tools.file_egress_destinations", name, v.Participant, v.Route))
			case part.Endpoint == "":
				errs = append(errs, fmt.Errorf("preset %q: api participant %q missing endpoint but requires file egress by route %q", name, v.Participant, v.Route))
			default:
				errs = append(errs, fmt.Errorf("preset %q: participant %q on endpoint %q requires file egress by route %q, but endpoint is not local and not in tools.file_egress_destinations", name, v.Participant, part.Endpoint, v.Route))
			}
		}

		// If preset has web tools, check that each endpoint used by a web participant has referee_model
		authorHasWeb := slices.Contains(preset.Tools, "web")
		reviewerHasWeb := slices.Contains(preset.ReviewerTools, "web")

		if authorHasWeb {
			// Author has web; must have an api endpoint with referee_model
			for _, p := range r.Participants {
				if p.ID == preset.Author {
					switch p.Route {
					case "cli":
						errs = append(errs, fmt.Errorf("preset %q: CLI author %q cannot have web tools", name, preset.Author))
					case "api":
						if p.Endpoint == "" {
							errs = append(errs, fmt.Errorf("preset %q: api author %q missing endpoint but has web tools", name, preset.Author))
						} else {
							ep := endpointMap[p.Endpoint]
							if ep.RefereeModel == "" {
								errs = append(errs, fmt.Errorf("preset %q: endpoint %q used by author has no referee_model but web tools are required", name, p.Endpoint))
							}
						}
					}
					break
				}
			}
		}

		if reviewerHasWeb {
			// Reviewers have web; each must have an api endpoint with referee_model
			for _, reviewerID := range preset.RequiredReviewers {
				for _, p := range r.Participants {
					if p.ID == reviewerID {
						switch p.Route {
						case "cli":
							errs = append(errs, fmt.Errorf("preset %q: CLI reviewer %q cannot have web tools", name, reviewerID))
						case "api":
							if p.Endpoint == "" {
								errs = append(errs, fmt.Errorf("preset %q: api reviewer %q missing endpoint but has web tools", name, reviewerID))
							} else {
								ep := endpointMap[p.Endpoint]
								if ep.RefereeModel == "" {
									errs = append(errs, fmt.Errorf("preset %q: endpoint %q used by reviewer %q has no referee_model but web tools are required", name, p.Endpoint, reviewerID))
								}
							}
						}
						break
					}
				}
			}
		}

		// With tools.allow_web_with_read off (per preset or roster-wide), web
		// and file content cannot meet in one preset.
		if web := r.WebWithFileViolation(preset, false); len(web) > 0 {
			var exposed []string
			for _, fr := range FileRoutes(preset, false) {
				exposed = append(exposed, fr.Participant)
			}
			errs = append(errs, fmt.Errorf("preset %q: cannot combine web (participants: %v) with read/write (file content reaches participants: %v) when tools.allow_web_with_read=false",
				name, web, exposed))
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
		if r.Tools.Referee != nil && r.Tools.Referee.ExecMaxOutputTokens != nil && *r.Tools.Referee.ExecMaxOutputTokens < 1 {
			errs = append(errs, fmt.Errorf("tools.referee.exec_max_output_tokens must be at least 1"))
		}
		if r.Tools.Referee != nil && r.Tools.Referee.MaxReads != nil && *r.Tools.Referee.MaxReads < 1 {
			errs = append(errs, fmt.Errorf("tools.referee.max_reads must be at least 1"))
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
			errs = append(errs, validateSearchConfig(r.Tools.Web.Search)...)
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
			if preset.Budgets.ProposerMaxOutputTokens != nil && *preset.Budgets.ProposerMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("presets[%q].budgets.proposer_max_output_tokens must be at least 1", name))
			}
			if preset.Budgets.CritiqueMaxOutputTokens != nil && *preset.Budgets.CritiqueMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("presets[%q].budgets.critique_max_output_tokens must be at least 1", name))
			}
			if preset.Budgets.IntegratorMaxOutputTokens != nil && *preset.Budgets.IntegratorMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("presets[%q].budgets.integrator_max_output_tokens must be at least 1", name))
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
			if qp.Budgets.ProposerMaxOutputTokens != nil && *qp.Budgets.ProposerMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("quick profile %q: budgets.proposer_max_output_tokens must be at least 1", profileName))
			}
			if qp.Budgets.CritiqueMaxOutputTokens != nil && *qp.Budgets.CritiqueMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("quick profile %q: budgets.critique_max_output_tokens must be at least 1", profileName))
			}
			if qp.Budgets.IntegratorMaxOutputTokens != nil && *qp.Budgets.IntegratorMaxOutputTokens < 1 {
				errs = append(errs, fmt.Errorf("quick profile %q: budgets.integrator_max_output_tokens must be at least 1", profileName))
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

// validateHostLabels checks that name is a dotted sequence of LDH labels
// (letters, digits, hyphens; 1-63 bytes; no leading/trailing hyphen).
// Non-ASCII labels are allowed here and handled by IDNA canonicalization.
func validateHostLabels(name string) error {
	if strings.HasSuffix(name, ".") {
		return errors.New("trailing dot is not canonical")
	}
	if len(name) > 253 {
		return errors.New("hostname too long")
	}
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if l == "" {
			return errors.New("empty label")
		}
		if len(l) > 63 {
			return fmt.Errorf("label %q too long", l)
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return fmt.Errorf("label %q must not start or end with a hyphen", l)
		}
		for _, r := range l {
			if r >= 0x80 {
				continue
			}
			ok := r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !ok {
				return fmt.Errorf("label %q contains invalid character %q", l, r)
			}
		}
	}
	last := labels[len(labels)-1]
	if strings.Trim(last, "0123456789") == "" {
		return fmt.Errorf("top-level label %q must not be all numeric", last)
	}
	return nil
}

// checkRoleEligible refuses a preset member whose participant lacks the
// eligible role (v4 section 1a). Unknown ids are reported elsewhere.
func (r *Roster) checkRoleEligible(preset, label, id string, role Role) []error {
	for _, p := range r.Participants {
		if p.ID == id {
			if !slices.Contains(p.EligibleRoles, role) {
				return []error{fmt.Errorf("preset %q: %s %q is not eligible for %s role", preset, label, id, role)}
			}
			break
		}
	}
	return nil
}

// validateDeliberatePreset applies the deliberate-mode rules of v8 section 1.
func (r *Roster) validateDeliberatePreset(name string, preset Preset, known map[string]bool) []error {
	var errs []error
	if preset.Author != "" {
		errs = append(errs, fmt.Errorf("preset %q: deliberate mode must not set author", name))
	}
	if len(preset.Proposers) < 2 {
		errs = append(errs, fmt.Errorf("preset %q: deliberate mode needs at least two proposers", name))
	}
	seen := make(map[string]bool)
	for _, id := range preset.Proposers {
		if seen[id] {
			errs = append(errs, fmt.Errorf("preset %q: proposer %q is listed twice", name, id))
		}
		seen[id] = true
		if !known[id] {
			errs = append(errs, fmt.Errorf("preset %q: proposer %q is not a participant", name, id))
			continue
		}
		errs = append(errs, r.checkRoleEligible(name, "proposer", id, RoleReviewer)...)
	}
	switch {
	case preset.Integrator == "":
		errs = append(errs, fmt.Errorf("preset %q: deliberate mode needs an integrator", name))
	case !slices.Contains(preset.Proposers, preset.Integrator):
		errs = append(errs, fmt.Errorf("preset %q: integrator %q is not one of the proposers", name, preset.Integrator))
	default:
		errs = append(errs, r.checkRoleEligible(name, "integrator", preset.Integrator, RoleAuthor)...)
	}
	if preset.RequiredReviewers != nil {
		if len(preset.RequiredReviewers) == 0 {
			errs = append(errs, fmt.Errorf("preset %q: required_reviewers must not be empty when given", name))
		}
		for _, id := range preset.RequiredReviewers {
			if id == preset.Integrator {
				errs = append(errs, fmt.Errorf("preset %q: integrator %q must not be a required reviewer", name, id))
			} else if !known[id] {
				errs = append(errs, fmt.Errorf("preset %q: required reviewer %q is not a participant", name, id))
			} else {
				errs = append(errs, r.checkRoleEligible(name, "reviewer", id, RoleReviewer)...)
			}
		}
	}
	return errs
}

// routingRefusals returns every reason the routing policy refuses to send
// text to the participant; none means it may receive it.
func (r *Roster) routingRefusals(p Participant, endpoints map[string]EndpointProfile, allowed map[string]bool) []string {
	if p.Route != "api" || p.Endpoint == "" {
		return nil
	}
	ep := endpoints[p.Endpoint]
	offsite := ep.DeclaredLocation == "corporate" || ep.DeclaredLocation == "cloud"
	var reasons []string
	if r.Policy.LocalOnly {
		if offsite {
			reasons = append(reasons, fmt.Sprintf("local_only policy rejects endpoint %q with declared_location %q", p.Endpoint, ep.DeclaredLocation))
		}
		if ep.DeclaredExecution != "local" {
			reasons = append(reasons, fmt.Sprintf("local_only policy rejects endpoint %q with declared_execution %q", p.Endpoint, ep.DeclaredExecution))
		}
	} else if offsite && !allowed[p.Endpoint] {
		reasons = append(reasons, fmt.Sprintf("endpoint %q (declared_location %q) must be in allowed_destinations", p.Endpoint, ep.DeclaredLocation))
	}
	return reasons
}
