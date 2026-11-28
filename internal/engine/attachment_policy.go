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
	// those reaching the turn through file content routes.
	participants := make(map[string]bool)
	participants[eff.Preset.Author] = true
	for _, id := range eff.Preset.RequiredReviewers {
		participants[id] = true
	}
	if eff.Preset.Mode == config.ModeDeliberate {
		for _, id := range eff.Preset.Proposers {
			participants[id] = true
		}
		for _, id := range eff.Preset.EffectiveRequiredReviewers() {
			participants[id] = true
		}
		participants[eff.Preset.Integrator] = true
	}

	for id := range participants {
		if !r.FileEgressAllowed(id) {
			return refuse(id)
		}
	}

	// Check web with file: when allow_web_with_read is false, no participant with web is allowed.
	// This applies regardless of whether the conversation was tainted or whether the preset
	// grants read/write, because attachments are always file content.
	if !r.AllowWebWithFile(eff.Preset) {
		for _, id := range eff.Preset.WebParticipants() {
			return refuse(id)
		}
	}

	return nil
}
