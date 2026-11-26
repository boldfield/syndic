package config

import "slices"

// The routes by which file-derived content reaches a participant (v5 delta
// section 6, "Egress of file-derived content"). A participant is named with
// the first route that applies to it.
const (
	// EgressRouteHistory is the answers of a file-tainted conversation's
	// earlier turns, which every participant of a later turn may see. In a
	// tainted conversation it is the route named for every participant,
	// whatever tools the preset grants.
	EgressRouteHistory = "history"
	// EgressRouteRead is a participant's own read results.
	EgressRouteRead = "read"
	// EgressRouteWrite is the author's own staged writes and their bases.
	EgressRouteWrite = "write"
	// EgressRouteDiff is the change-set diffs every reviewer is given when
	// the author can write.
	EgressRouteDiff = "diff"
	// EgressRouteEvidence is the author's read results every reviewer is
	// given as evidence.
	EgressRouteEvidence = "evidence"
	// EgressRouteVerdict is a reviewer's findings, written with file content
	// in view, that a revision gives the author.
	EgressRouteVerdict = "verdict"
)

// FileRoute is a participant of a preset and the route by which it could
// receive file-derived content.
type FileRoute struct {
	Participant string
	Route       string
}

// GrantsFileTools reports whether the preset gives any role read or write. A
// turn under such a preset is file-tainted.
func (p Preset) GrantsFileTools() bool {
	return slices.Contains(p.Tools, "read") || slices.Contains(p.Tools, "write") ||
		slices.Contains(p.ReviewerTools, "read")
}

// WebParticipants returns the participants the preset gives web, author
// first.
func (p Preset) WebParticipants() []string {
	var ids []string
	if slices.Contains(p.Tools, "web") {
		ids = append(ids, p.Author)
	}
	if slices.Contains(p.ReviewerTools, "web") {
		ids = append(ids, p.RequiredReviewers...)
	}
	return ids
}

// FileRoutes returns every participant of the preset that could receive
// file-derived content in a turn under it, author first, each with the first
// route that applies. tainted is whether the conversation the turn joins is
// already file-tainted; then every participant is reached by history. A
// preset that grants read or write reaches every participant, because diffs,
// evidence and findings go to the other roles and history goes to everyone.
func FileRoutes(p Preset, tainted bool) []FileRoute {
	authorRead := slices.Contains(p.Tools, "read")
	authorWrite := slices.Contains(p.Tools, "write")
	reviewerRead := slices.Contains(p.ReviewerTools, "read")
	if !tainted && !p.GrantsFileTools() {
		return nil
	}
	routes := make([]FileRoute, 0, 1+len(p.RequiredReviewers))
	route := EgressRouteHistory
	switch {
	case tainted:
	case authorRead:
		route = EgressRouteRead
	case authorWrite:
		route = EgressRouteWrite
	case reviewerRead:
		route = EgressRouteVerdict
	}
	routes = append(routes, FileRoute{Participant: p.Author, Route: route})
	for _, id := range p.RequiredReviewers {
		route := EgressRouteHistory
		switch {
		case tainted:
		case reviewerRead:
			route = EgressRouteRead
		case authorWrite:
			route = EgressRouteDiff
		case authorRead:
			route = EgressRouteEvidence
		}
		routes = append(routes, FileRoute{Participant: id, Route: route})
	}
	return routes
}

// FileEgressAllowed reports whether file-derived content may be sent to the
// participant: it must be an api participant on an endpoint with local
// execution or one named in tools.file_egress_destinations. A participant the
// roster does not declare is reported allowed; its absence is refused
// elsewhere.
func (r *Roster) FileEgressAllowed(participantID string) bool {
	var part *Participant
	for i := range r.Participants {
		if r.Participants[i].ID == participantID {
			part = &r.Participants[i]
			break
		}
	}
	if part == nil {
		return true
	}
	if part.Route != "api" || part.Endpoint == "" {
		return false
	}
	if r.Tools != nil && slices.Contains(r.Tools.FileEgressDestinations, part.Endpoint) {
		return true
	}
	for _, ep := range r.Endpoints {
		if ep.Name == part.Endpoint {
			return ep.DeclaredExecution == "local"
		}
	}
	return false
}

// FileEgressViolations returns the routes of FileRoutes whose participant
// FileEgressAllowed refuses. Roster load calls it untainted and a submit with
// the conversation's taint, so both hold the same rule.
func (r *Roster) FileEgressViolations(p Preset, tainted bool) []FileRoute {
	var bad []FileRoute
	for _, fr := range FileRoutes(p, tainted) {
		if !r.FileEgressAllowed(fr.Participant) {
			bad = append(bad, fr)
		}
	}
	return bad
}

// AllowWebWithFile returns the effective tools.allow_web_with_read for the
// preset: the preset's own setting, else the roster's, else true.
func (r *Roster) AllowWebWithFile(p Preset) bool {
	if p.AllowWebWithRead != nil {
		return *p.AllowWebWithRead
	}
	if r.Tools != nil && r.Tools.AllowWebWithRead != nil {
		return *r.Tools.AllowWebWithRead
	}
	return true
}

// WebWithFileViolation returns the participants given web when the preset
// turns web with file content off and a turn under it could hold file
// content, because the preset grants read or write or the conversation is
// tainted; otherwise nil. A preset whose only tool is web, in an untainted
// conversation, is unaffected.
func (r *Roster) WebWithFileViolation(p Preset, tainted bool) []string {
	if r.AllowWebWithFile(p) || (!tainted && !p.GrantsFileTools()) {
		return nil
	}
	return p.WebParticipants()
}
