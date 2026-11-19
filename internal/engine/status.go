package engine

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// Stop shuts the engine down. In this task it waits for nothing.
func (e *Engine) Stop(ctx context.Context) error { return nil }

// Status reports the run, the participants, pools and quick profiles.
func (e *Engine) Status() (wire.StatusReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		return wire.StatusReply{}, errors.New("engine: not started")
	}

	changed, err := manifest.RosterFileChanged(e.rosterAbs, e.built.RosterFileSHA256)
	if err != nil {
		return wire.StatusReply{}, err
	}

	r := e.roster
	started := e.startedAt.Format(time.RFC3339Nano)
	reply := wire.StatusReply{
		RunID:                e.runID,
		RunStartedAt:         started,
		RosterPath:           e.rosterAbs,
		RosterHash:           e.built.RosterHash,
		RosterFileChanged:    changed,
		DefaultPreset:        r.DefaultPreset,
		DeadlinesProvisional: r.Deadlines.Provisional != nil && *r.Deadlines.Provisional,
		Participants:         make([]wire.Participant, 0, len(r.Participants)),
		Pools:                []wire.Pool{},
		OrphanedHolds:        []wire.Hold{},
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

	names := make([]string, 0, len(poolNames))
	for name := range poolNames {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		concurrency, _ := r.PoolFor(name)
		reply.Pools = append(reply.Pools, wire.Pool{Name: name, Concurrency: concurrency, Holds: []wire.PoolHold{}})
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

func (e *Engine) participantStatus(p config.Participant) wire.Participant {
	mp := e.built.Participants[p.ID]
	ws := wire.Participant{
		ID:                       p.ID,
		Model:                    p.Model,
		Pool:                     poolName(p),
		EligibleRoles:            make([]string, 0, len(p.EligibleRoles)),
		DeclaredBilling:          e.endpoint(p.Endpoint).DeclaredBilling,
		BytesPerTokenSource:      mp.BytesPerTokenSource,
		ContextSource:            mp.ContextSource,
		EligibleRequiredReviewer: string(transport.TriUnknown),
		Estimate:                 "",
	}
	for _, role := range p.EligibleRoles {
		ws.EligibleRoles = append(ws.EligibleRoles, string(role))
	}

	// Check if participant has any unreleased holds
	hasUnreleasedHold := false
	if holds, err := e.store.UnreleasedHolds(); err == nil {
		for _, h := range holds {
			attempt, _ := e.store.GetAttempt(h.AttemptID)
			if attempt != nil && attempt.Participant == p.ID {
				hasUnreleasedHold = true
				break
			}
		}
	}

	switch {
	case !mp.Usable:
		ws.State = ParticipantUnprobed
		if mp.Detail != "" {
			detail := mp.Detail
			ws.Detail = &detail
		}
	case e.cancelling[p.ID]:
		ws.State = ParticipantCancelling
	case e.unknown[p.ID]:
		ws.State = ParticipantUnknown
	case e.inflight[p.ID] > 0:
		// Participant has in-flight attempts, keep them as running
		ws.State = ParticipantRunning
	case hasUnreleasedHold:
		// Participant has unreleased holds, so it's running (slot is held)
		ws.State = ParticipantRunning
	case e.running[p.ID]:
		ws.State = ParticipantRunning
	case e.queued[p.ID]:
		ws.State = ParticipantQueued
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
	if e.exceeded[p.ID] {
		ws.Estimate = estimateExceeded
	}
	return ws
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
