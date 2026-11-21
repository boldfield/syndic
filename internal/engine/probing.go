package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// startupProbeRequest describes a participant or referee to probe.
type startupProbeRequest struct {
	participantID string
	endpoint      config.EndpointProfile
	model         string
	isReferee     bool
}

// runStartupProbes orchestrates sequential probing at engine start.
// It is called after recovery and roster validation, with the journal lock held.
// It runs probes between the two phase commits of Start(): after run_opened is emitted
// and before the manifest is finalized.
func (e *Engine) runStartupProbes(ctx context.Context, roster *config.Roster, built manifest.Built, probeMissing bool) error {
	// Find participants and referees that need probes
	probeRequests, err := e.findProbesNeeded(roster, built, probeMissing)
	if err != nil {
		return fmt.Errorf("find probes needed: %w", err)
	}

	if len(probeRequests) == 0 {
		return nil
	}

	// Check for orphaned holds that would block all probes
	orphanedHolds, err := e.store.UnreleasedHolds()
	if err != nil {
		return fmt.Errorf("check holds: %w", err)
	}

	hasOrphanedHolds := false
	for _, h := range orphanedHolds {
		if h.ReleasedAt == nil {
			hasOrphanedHolds = true
			break
		}
	}

	if hasOrphanedHolds {
		// All probes are blocked by orphaned holds
		return nil
	}

	// Run probes one at a time
	for _, req := range probeRequests {
		if err := e.runOneStartupProbe(ctx, req, roster); err != nil {
			// Probe failed, but don't stop the engine
			// Just continue to the next probe
			continue
		}
	}

	return nil
}

// findProbesNeeded identifies participants and referees that need probes.
func (e *Engine) findProbesNeeded(roster *config.Roster, built manifest.Built, probeMissing bool) ([]startupProbeRequest, error) {
	var requests []startupProbeRequest

	// Map endpoints by name for quick lookup
	endpointsByName := make(map[string]config.EndpointProfile)
	for _, ep := range roster.Endpoints {
		endpointsByName[ep.Name] = ep
	}

	// Find participants with no usable record
	for _, p := range roster.Participants {
		participantRecord := built.Participants[p.ID]
		if !participantRecord.Usable {
			ep := endpointsByName[p.Endpoint]
			if e.shouldProbeAutomatically(&ep, probeMissing) {
				requests = append(requests, startupProbeRequest{
					participantID: p.ID,
					endpoint:      ep,
					model:         p.Model,
					isReferee:     false,
				})
			}
		}
	}

	return requests, nil
}

// shouldProbeAutomatically returns true if a participant on the given endpoint
// should be probed automatically at startup.
// - Automatic for `none` or `flat` endpoints
// - Only with --probe-missing for `metered` or `unknown` endpoints
func (e *Engine) shouldProbeAutomatically(ep *config.EndpointProfile, probeMissing bool) bool {
	switch ep.DeclaredBilling {
	case "none", "flat":
		return true
	case "metered", "unknown":
		return probeMissing
	default:
		return false
	}
}

// runOneStartupProbe runs a single startup probe for a participant.
// It emits probe_started and probe_finished events, and handles holds appropriately.
func (e *Engine) runOneStartupProbe(ctx context.Context, req startupProbeRequest, roster *config.Roster) error {
	// Get the participant config to determine the pool
	var participant *config.Participant
	for _, p := range roster.Participants {
		if p.ID == req.participantID {
			participant = &p
			break
		}
	}
	if participant == nil {
		return fmt.Errorf("participant %s not found in roster", req.participantID)
	}

	// Determine the pool name
	poolName := participant.Pool
	if poolName == "" {
		poolName = defaultPool
	}

	unreleased, err := e.store.UnreleasedHolds()
	if err != nil {
		return fmt.Errorf("check holds: %w", err)
	}

	// Check if there's an ordinary hold on this pool
	for _, h := range unreleased {
		if h.ReleasedAt == nil && h.Pool == poolName {
			// This pool has an outstanding hold, skip the probe
			err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
				return emit("", wire.EventKindProbeFinished, wire.EvProbeFinished{
					Participant: req.participantID,
					Outcome:     "probe_failed:hold",
				})
			})
			return err
		}
	}

	// Emit probe_started event
	if err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return emit("", wire.EventKindProbeStarted, wire.EvProbeStarted{
			Participant: req.participantID,
		})
	}); err != nil {
		return err
	}

	// Run the actual probe
	adapter := e.adapters[req.participantID]
	if adapter == nil {
		return e.probeFinished(req.participantID, "probe_failed:unreachable", "")
	}

	// Create probe context with timeout
	probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	record, err := adapter.Probe(probeCtx, req.endpoint.Name, req.model, transport.ProbeOptions{
		Timeout:            60 * time.Second,
		DisableOverLength:  false,
		DisableCalibration: false,
		AssumedContextLen:  0,
	})
	if err != nil {
		detail := e.probeErrorDetail(err)
		return e.probeFinished(req.participantID, "probe_failed:"+detail, err.Error())
	}

	// If probe was incomplete, it's still a failure
	if record.ContextBound == nil && record.OutputBound == nil {
		return e.probeFinished(req.participantID, "probe_failed:incomplete", "no model identity or token bounds")
	}

	// Emit probe_finished event with successful outcome
	return e.probeFinished(req.participantID, "probed", "")
}

// probeErrorDetail maps probe errors to detail strings.
func (e *Engine) probeErrorDetail(err error) string {
	// TODO: Implement proper error classification based on transport errors
	// For now, return a generic error detail
	return "unreachable"
}

// probeFinished emits the probe_finished event.
func (e *Engine) probeFinished(participantID, outcome, detail string) error {
	return e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		evt := wire.EvProbeFinished{
			Participant: participantID,
			Outcome:     outcome,
		}
		if detail != "" {
			evt.Detail = &detail
		}
		return emit("", wire.EventKindProbeFinished, evt)
	})
}
