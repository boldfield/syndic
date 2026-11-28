package engine

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// Status reports the run, the participants, pools and quick profiles.
func (e *Engine) Status() (wire.StatusReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started && e.runState != "probing" {
		return wire.StatusReply{}, errors.New("engine: not started")
	}

	changed, err := manifest.RosterFileChanged(e.rosterAbs, e.built.RosterFileSHA256)
	if err != nil {
		return wire.StatusReply{}, err
	}

	r := e.roster
	started := e.startedAt.Format(time.RFC3339Nano)

	// Build orphaned holds list
	orphanedHolds := make([]wire.Hold, 0, len(e.orphanedHolds))
	for _, h := range e.orphanedHolds {
		orphanedHolds = append(orphanedHolds, wire.Hold{
			Pool:      h.pool,
			AttemptID: h.attemptID,
			Group:     h.group,
		})
	}
	sort.Slice(orphanedHolds, func(i, j int) bool {
		return orphanedHolds[i].AttemptID < orphanedHolds[j].AttemptID
	})

	reply := wire.StatusReply{
		RunID:                e.runID,
		RunStartedAt:         started,
		RosterPath:           e.rosterAbs,
		RosterHash:           e.built.RosterHash,
		RosterFileChanged:    changed,
		DefaultPreset:        r.DefaultPreset,
		Presets:              presetNames(r),
		DeadlinesProvisional: r.Deadlines.Provisional != nil && *r.Deadlines.Provisional,
		Participants:         make([]wire.Participant, 0, len(r.Participants)),
		Pools:                []wire.Pool{},
		OrphanedHolds:        orphanedHolds,
		QuickProfiles:        []wire.QuickProfile{},
		TurnsOpen:            []string{},
		EngineStartedAt:      started,
	}

	poolNames := map[string]bool{}
	for name := range r.Pools {
		poolNames[name] = true
	}
	for _, p := range r.Participants {
		reply.Participants = append(reply.Participants, e.participantStatus(p))
		poolNames[poolName(p)] = true
	}

	reply.ProbeSubjects = e.probeSubjects()

	names := make([]string, 0, len(poolNames))
	for name := range poolNames {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		concurrency, _ := r.PoolFor(name)
		holds := []wire.PoolHold{}
		for _, h := range e.sortedHolds(name) {
			holds = append(holds, wire.PoolHold{
				AttemptID: h.attemptID,
				HeldSince: h.since.Format(time.RFC3339Nano),
				Group:     h.group,
			})
		}
		reply.Pools = append(reply.Pools, wire.Pool{Name: name, Concurrency: concurrency, Holds: holds})
	}

	quick := make([]string, 0, len(r.Quick))
	for name := range r.Quick {
		quick = append(quick, name)
	}
	sort.Strings(quick)
	for _, name := range quick {
		eff, err := r.Resolve("", name)
		if err != nil {
			return wire.StatusReply{}, err
		}
		sum, err := r.QuickDefinitionSHA256(name)
		if err != nil {
			return wire.StatusReply{}, err
		}
		reply.QuickProfiles = append(reply.QuickProfiles, wire.QuickProfile{
			Name:             name,
			Preset:           eff.PresetName,
			Drafts:           eff.Drafts,
			Budgets:          wireBudgets(eff.Budgets),
			DefinitionSHA256: sum,
		})
	}

	// The round limits in force are those of a submit that names no preset.
	eff, err := r.Resolve(r.DefaultPreset, "")
	if err != nil {
		return wire.StatusReply{}, err
	}
	reply.Revision.Default, reply.Revision.Ceiling = revisionLimits(eff)

	if e.open != nil {
		reply.TurnsOpen = append(reply.TurnsOpen, e.open.id)
	}
	return reply, nil
}

func poolName(p config.Participant) string {
	if p.Pool == "" {
		return defaultPool
	}
	return p.Pool
}

// retryCommand is the exact `syndic probe` invocation that would record the
// participant's endpoint and model in the journal this daemon reads.
func (e *Engine) retryCommand(p config.Participant) string {
	return e.retryCommandFor(p.Endpoint, p.Model, p.ContextTokens)
}

func (e *Engine) retryCommandFor(endpoint, model string, contextTokens *int) string {
	cmd := "syndic probe --endpoint " + shQuote(endpoint) + " --model " + shQuote(model) + " --roster " + shQuote(e.rosterAbs)
	if e.journalPath != "" {
		cmd += " --journal " + shQuote(e.journalPath)
	}
	if e.secretsDir != "" {
		cmd += " --secrets-dir " + shQuote(e.secretsDir)
	}
	if contextTokens != nil {
		cmd += " --context-tokens " + fmt.Sprint(*contextTokens)
	}
	return cmd
}

// shQuote escapes a string for safe shell use.
func shQuote(s string) string {
	// If the string contains no special characters, return as-is
	if len(s) > 0 && !needsQuoting(s) {
		return s
	}
	// Otherwise, use single quotes and escape any single quotes in the string
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func needsQuoting(s string) bool {
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' && c != '/' {
			return true
		}
	}
	return false
}

func (e *Engine) participantStatus(p config.Participant) wire.Participant {
	mp := e.built.Participants[p.ID]
	ep := e.endpoint(p.Endpoint)
	ws := wire.Participant{
		ID:                       p.ID,
		Model:                    p.Model,
		Endpoint:                 p.Endpoint,
		Pool:                     poolName(p),
		EligibleRoles:            make([]string, 0, len(p.EligibleRoles)),
		DeclaredBilling:          ep.DeclaredBilling,
		SpendingControl:          ep.SpendingControl,
		BytesPerTokenSource:      mp.BytesPerTokenSource,
		ContextSource:            mp.ContextSource,
		EligibleRequiredReviewer: string(transport.TriUnknown),
		Estimate:                 "",
	}
	for _, role := range p.EligibleRoles {
		ws.EligibleRoles = append(ws.EligibleRoles, string(role))
	}
	switch {
	case !mp.Usable:
		ws.State = ParticipantUnprobed
		if mp.Detail != "" {
			detail := mp.Detail
			ws.Detail = &detail
		}
	case e.cancelling[p.ID] > 0:
		ws.State = ParticipantCancelling
	case e.queued[p.ID]:
		// Waiting for a slot is more current than an earlier hold.
		ws.State = ParticipantQueued
	case e.specialState(p.ID) != "":
		ws.State = ParticipantUnknown
	case e.running[p.ID]:
		ws.State = ParticipantRunning
	case e.failed[p.ID] != "":
		ws.State = e.failed[p.ID]
	default:
		ws.State = ParticipantIdle
	}
	if mp.Usable {
		ws.ReportedModel = mp.Expected.ReportedModel
		ws.Identity = mp.Expected.Identity.Kind
		if mp.EligibleRequiredReviewer != "" {
			ws.EligibleRequiredReviewer = string(mp.EligibleRequiredReviewer)
		}
	}
	if mp.ProbeObservedAt != nil {
		observed := mp.ProbeObservedAt.UTC().Format(time.RFC3339Nano)
		ws.ProbeObservedAt = &observed
	}
	if mp.ContextBound != nil {
		v := int64(*mp.ContextBound)
		ws.ContextBound = &v
	}
	if mp.OutputBound != nil {
		v := int64(*mp.OutputBound)
		ws.OutputBound = &v
	}
	ws.BytesPerToken = mp.BytesPerToken
	ws.FailingStep = mp.FailingStep
	ws.ServerStatus = mp.ServerStatus
	ws.ProbeAtStart = e.probed[p.ID]

	// Populate accept_unknown_truncation from config
	if p.AcceptUnknownTruncation != nil {
		ws.AcceptUnknownTruncation = *p.AcceptUnknownTruncation
	}

	// Check if participant is in the default preset (author or required reviewer)
	if preset, ok := e.roster.Presets[e.roster.DefaultPreset]; ok {
		if p.ID == preset.Author {
			ws.InDefaultPreset = true
		} else {
			for _, rr := range preset.RequiredReviewers {
				if p.ID == rr {
					ws.InDefaultPreset = true
					break
				}
			}
		}
	}

	// Build retry command for unprobed participants
	if !mp.Usable {
		cmd := e.retryCommand(p)
		ws.RetryCommand = &cmd
	}

	if e.exceeded[p.ID] {
		ws.Estimate = estimateExceeded
	}
	if e.participantHoldsWeb(p.ID) {
		if reason := e.webOffReason(p.Endpoint); reason != "" {
			ws.WebOff = &reason
		}
		if reason := e.searchOffReason(); reason != "" {
			ws.SearchOff = &reason
		}
	}
	ws.MCPOff = e.mcpOffFor(p.ID)
	return ws
}

// probeSubjects reports the referee identities the roster names. They carry
// probe state only: a referee is no participant, so it has no eligible roles.
func (e *Engine) probeSubjects() []wire.ProbeSubject {
	var out []wire.ProbeSubject
	for _, r := range refereeIdentities(e.roster) {
		ep := endpointOf(e.roster, r.Endpoint)
		if ep == nil {
			continue
		}
		label := "referee:" + r.Endpoint + ":" + r.Model
		subj := wire.ProbeSubject{
			Kind:         "referee",
			Identity:     label,
			Endpoint:     r.Endpoint,
			Model:        r.Model,
			State:        ParticipantUnprobed,
			ProbeAtStart: e.probed[label],
		}
		usable := false
		if _, version, err := e.newAdapter(config.Participant{ID: label, Endpoint: r.Endpoint, Model: r.Model, Route: "api"}, *ep); err == nil {
			usable, _ = e.recordUsable(*ep, r.Model, version)
		}
		if usable {
			subj.State = "probed"
		} else {
			if f, ok := e.probeFailed[label]; ok {
				detail := probeFailedPrefix + f.reason
				subj.Detail = &detail
				subj.FailingStep = f.failingStep
				subj.ServerStatus = f.serverStat
			}
			cmd := e.retryCommandFor(r.Endpoint, r.Model, nil)
			subj.RetryCommand = &cmd
		}
		out = append(out, subj)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}

func (e *Engine) endpoint(name string) config.EndpointProfile {
	for _, ep := range e.roster.Endpoints {
		if ep.Name == name {
			return ep
		}
	}
	return config.EndpointProfile{}
}

func wireBudgets(b config.Budgets) *wire.Budgets {
	i64 := func(v *int) *int64 {
		if v == nil {
			return nil
		}
		n := int64(*v)
		return &n
	}
	return &wire.Budgets{
		CalibrationSafety:       b.CalibrationSafety,
		MessageOverheadTokens:   i64(b.MessageOverheadTokens),
		RequestOverheadTokens:   i64(b.RequestOverheadTokens),
		ContextMargin:           b.ContextMargin,
		AuthorMaxOutputTokens:   i64(b.AuthorMaxOutputTokens),
		ReviewerMaxOutputTokens: i64(b.ReviewerMaxOutputTokens),
		DeliveryMaxTokens:       i64(b.DeliveryMaxTokens),
		TurnMaxEstimatedTokens:  i64(b.TurnMaxEstimatedTokens),
	}
}

// presetNames is the roster's preset names, sorted, so a client can check a
// preset before submitting it.
func presetNames(r *config.Roster) []string {
	names := make([]string, 0, len(r.Presets))
	for name := range r.Presets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
