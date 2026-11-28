package config

import (
	"fmt"
	"slices"
	"strings"
)

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
func (p Preset) WebParticipants() []string { return p.toolParticipants("web") }

// ExecParticipants returns the participants the preset gives exec, author
// first.
func (p Preset) ExecParticipants() []string { return p.toolParticipants("exec") }

func (p Preset) toolParticipants(group string) []string {
	return p.participantsWhere(func(set []string) bool { return slices.Contains(set, group) })
}

// participantsWhere returns the participants whose tool set satisfies has,
// author first. In a deliberate preset tools is the integrator's set and
// reviewer_tools everyone else's.
func (p Preset) participantsWhere(has func(set []string) bool) []string {
	if p.Mode == ModeDeliberate {
		v := p.deliberateEgressView()
		ids := v.participantsWhere(has)
		// The integrator also proposes and critiques, so it holds
		// reviewer_tools in those phases.
		if has(p.ReviewerTools) && !slices.Contains(ids, p.Integrator) {
			ids = append(ids, p.Integrator)
		}
		return ids
	}
	var ids []string
	if has(p.Tools) {
		ids = append(ids, p.Author)
	}
	if has(p.ReviewerTools) {
		ids = append(ids, p.RequiredReviewers...)
	}
	return ids
}

// MCPGrantPrefix starts a tool-set entry that grants MCP tools: mcp:<server>
// for every allowed tool of a server, mcp:<server>/<tool> for one.
const MCPGrantPrefix = "mcp:"

// IsMCPGrant reports whether a tool-set entry is an MCP grant.
func IsMCPGrant(entry string) bool { return strings.HasPrefix(entry, MCPGrantPrefix) }

func hasMCPGrant(set []string) bool { return slices.ContainsFunc(set, IsMCPGrant) }

// MCPToolRef is one allowed MCP tool a grant names.
type MCPToolRef struct {
	Server, Tool, Class string
}

// MCPGrantTools resolves one tool-set entry to the tools it grants, sorted by
// tool name. A server or tool the roster does not declare is an error.
func (r *Roster) MCPGrantTools(entry string) ([]MCPToolRef, error) {
	spec := strings.TrimPrefix(entry, MCPGrantPrefix)
	server, tool, one := strings.Cut(spec, "/")
	var srv MCPServer
	var ok bool
	if r.MCP != nil {
		srv, ok = r.MCP.Servers[server]
	}
	if !ok {
		return nil, fmt.Errorf("MCP grant %q names server %q, which is not in the roster", entry, server)
	}
	if one {
		class, ok := srv.Tools[tool]
		if !ok {
			return nil, fmt.Errorf("MCP grant %q names tool %q, which server %q does not allow", entry, tool, server)
		}
		return []MCPToolRef{{server, tool, class}}, nil
	}
	refs := make([]MCPToolRef, 0, len(srv.Tools))
	for t, class := range srv.Tools {
		refs = append(refs, MCPToolRef{server, t, class})
	}
	slices.SortFunc(refs, func(a, b MCPToolRef) int { return strings.Compare(a.Tool, b.Tool) })
	return refs, nil
}

// mcpToolsOf returns the tools every MCP grant in set resolves to; an
// unresolvable grant contributes none (it is refused on its own).
func (r *Roster) mcpToolsOf(set []string) []MCPToolRef {
	var out []MCPToolRef
	for _, e := range set {
		if IsMCPGrant(e) {
			refs, _ := r.MCPGrantTools(e)
			out = append(out, refs...)
		}
	}
	return out
}

// mcpToolIsWeb reports whether a tool counts as web for
// tools.allow_web_with_read: any tool of a remote server, and any tool of a
// local server with a non-empty network_allow.
func (r *Roster) mcpToolIsWeb(t MCPToolRef) bool {
	srv := r.MCP.Servers[t.Server]
	return srv.Transport == MCPTransportHTTP ||
		(srv.Transport == MCPTransportStdio && len(srv.NetworkAllow) > 0)
}

func (r *Roster) grantsMCPWeb(set []string) bool {
	return slices.ContainsFunc(r.mcpToolsOf(set), r.mcpToolIsWeb)
}

// WebParticipants returns the participants given web, counting both the literal
// "web" tool and any web MCP grants (HTTP servers, or stdio servers with
// network_allow), author first.
func (r *Roster) WebParticipants(p Preset) []string {
	return p.participantsWhere(func(set []string) bool {
		return slices.Contains(set, "web") || r.grantsMCPWeb(set)
	})
}

// validatePresetMCP applies the roster-load rules for the MCP grants of one
// preset (v9 section 2, "Presets").
func (r *Roster) validatePresetMCP(name string, p Preset) []error {
	var errs []error
	for _, set := range [][]string{p.Tools, p.ReviewerTools} {
		for _, e := range set {
			if IsMCPGrant(e) {
				if _, err := r.MCPGrantTools(e); err != nil {
					errs = append(errs, fmt.Errorf("preset %q: %w", name, err))
				}
			}
		}
	}
	for _, t := range r.mcpToolsOf(p.ReviewerTools) {
		if t.Class == MCPClassEffect {
			errs = append(errs, fmt.Errorf("preset %q: reviewer_tools may hold only read tools; MCP tool %s/%s is effect", name, t.Server, t.Tool))
		}
	}
	if p.Mode == "solo" {
		for _, t := range r.mcpToolsOf(p.Tools) {
			if t.Class == MCPClassEffect {
				errs = append(errs, fmt.Errorf("preset %q: effect MCP tool %s/%s requires a review or deliberate preset, not solo", name, t.Server, t.Tool))
			}
		}
	}
	for _, id := range p.participantsWhere(hasMCPGrant) {
		i := slices.IndexFunc(r.Participants, func(q Participant) bool { return q.ID == id })
		if i < 0 {
			continue
		}
		part := r.Participants[i]
		switch {
		case part.Route == "cli":
			errs = append(errs, fmt.Errorf("preset %q: CLI participant %q cannot have MCP tools", name, id))
		case part.Route == "api" && part.Endpoint == "":
			errs = append(errs, fmt.Errorf("preset %q: api participant %q missing endpoint but has MCP tools", name, id))
		case part.Route == "api":
			hasReferee := false
			for _, ep := range r.Endpoints {
				if ep.Name == part.Endpoint {
					hasReferee = ep.RefereeModel != ""
					break
				}
			}
			if !hasReferee {
				errs = append(errs, fmt.Errorf("preset %q: endpoint %q used by participant %q has no referee_model but MCP tools are granted", name, part.Endpoint, id))
			}
		}
	}
	return errs
}

// validateMCPScratchNetwork refuses a local server with both
// workspace = "scratch" and a non-empty network_allow when
// tools.allow_web_with_read is false, whatever the presets grant. The setting
// is false when the roster or any preset turns it off.
func (r *Roster) validateMCPScratchNetwork() []error {
	if r.MCP == nil {
		return nil
	}
	off := r.Tools != nil && r.Tools.AllowWebWithRead != nil && !*r.Tools.AllowWebWithRead
	for _, p := range r.Presets {
		if p.AllowWebWithRead != nil && !*p.AllowWebWithRead {
			off = true
		}
	}
	if !off {
		return nil
	}
	var errs []error
	for name, s := range r.MCP.Servers {
		if s.Transport == MCPTransportStdio && s.Workspace == MCPWorkspaceScratch && len(s.NetworkAllow) > 0 {
			errs = append(errs, fmt.Errorf("mcp.%s: workspace = %q with a non-empty network_allow is refused when tools.allow_web_with_read = false", name, MCPWorkspaceScratch))
		}
	}
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return errs
}

// FileRoutes returns every participant of the preset that could receive
// file-derived content in a turn under it, author first, each with the first
// route that applies. tainted is whether the conversation the turn joins is
// already file-tainted; then every participant is reached by history. A
// preset that grants read or write reaches every participant, because diffs,
// evidence and findings go to the other roles and history goes to everyone.
func FileRoutes(p Preset, tainted bool) []FileRoute {
	if p.Mode == ModeDeliberate {
		p = p.deliberateEgressView()
	}
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
	return r.WebParticipants(p)
}

// deliberateEgressView maps a deliberate preset onto the author-and-reviewers
// shape the egress rules are written for (v8 section 3): the integrator is
// the author, and every other participant that takes part, proposer or
// required reviewer, is a reviewer. D1's tool sets carry over unchanged.
func (p Preset) deliberateEgressView() Preset {
	v := p
	v.Mode = "review"
	v.Author = p.Integrator
	v.RequiredReviewers = nil
	for _, id := range p.Proposers {
		if id != p.Integrator && !slices.Contains(v.RequiredReviewers, id) {
			v.RequiredReviewers = append(v.RequiredReviewers, id)
		}
	}
	for _, id := range p.EffectiveRequiredReviewers() {
		if id != p.Integrator && !slices.Contains(v.RequiredReviewers, id) {
			v.RequiredReviewers = append(v.RequiredReviewers, id)
		}
	}
	return v
}

// ExecSettings returns the roster's resolved [tools.exec], with defaults for
// anything absent.
func (r *Roster) ExecSettings() ExecSettings {
	if r.Tools == nil {
		return (*ExecConfig)(nil).Settings()
	}
	return r.Tools.Exec.Settings()
}

// EffectiveNetworkAllow returns the exec network list for the preset: the
// preset's own network_allow when it sets one, which replaces the roster's,
// else tools.exec.network_allow.
func (r *Roster) EffectiveNetworkAllow(p Preset) []string {
	if p.NetworkAllow != nil {
		return slices.Clone(p.NetworkAllow)
	}
	if r.Tools != nil && r.Tools.Exec != nil {
		return slices.Clone(r.Tools.Exec.NetworkAllow)
	}
	return nil
}
