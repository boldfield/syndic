package engine

import (
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/wire"
)

// attachmentEgressRefusal checks the egress policy for attachments before any file is opened.
// According to v6 section 4, step 4 (Policy), attachments must satisfy:
//  1. Destinations: Every participant must be on an endpoint with local execution or
//     named in tools.file_egress_destinations
//  2. Web with file content: When allow_web_with_read is false and any participant has web,
//     the submit is refused
//
// Unlike the history route rules, these checks apply regardless of prior taint.
func attachmentEgressRefusal(r *config.Roster, eff config.Effective) *wire.Error {
	refuse := func(participant string) *wire.Error {
		we := refusal(wire.ErrorCodeEgress)
		we.Participant, we.Route = strPtr(participant), strPtr("attachment")
		return we
	}

	// Check destinations: every participant in the preset must be allowed for file egress.
	// This is broader than the history rules - it applies to ALL participants, not just
	// those reaching the turn through file content routes. Collect participants in a
	// consistent order: author first, then reviewers as listed, then deliberate roles.
	var participants []string
	seen := make(map[string]bool)

	// Author first
	if !seen[eff.Preset.Author] {
		participants = append(participants, eff.Preset.Author)
		seen[eff.Preset.Author] = true
	}

	// Then required reviewers
	for _, id := range eff.Preset.RequiredReviewers {
		if !seen[id] {
			participants = append(participants, id)
			seen[id] = true
		}
	}

	// Deliberate-specific roles
	if eff.Preset.Mode == config.ModeDeliberate {
		for _, id := range eff.Preset.Proposers {
			if !seen[id] {
				participants = append(participants, id)
				seen[id] = true
			}
		}
		for _, id := range eff.Preset.EffectiveRequiredReviewers() {
			if !seen[id] {
				participants = append(participants, id)
				seen[id] = true
			}
		}
		if !seen[eff.Preset.Integrator] {
			participants = append(participants, eff.Preset.Integrator)
			seen[eff.Preset.Integrator] = true
		}
	}

	for _, id := range participants {
		if !r.FileEgressAllowed(id) {
			return refuse(id)
		}
	}

	// Check web with file: when allow_web_with_read is false, no participant with web is allowed.
	// This applies regardless of whether the conversation was tainted or whether the preset
	// grants read/write, because attachments are always file content. This includes both
	// the literal "web" tool and web MCP grants (HTTP servers, or stdio with network_allow).
	if !r.AllowWebWithFile(eff.Preset) {
		for _, id := range r.WebParticipants(eff.Preset) {
			return refuse(id)
		}
	}

	return nil
}
