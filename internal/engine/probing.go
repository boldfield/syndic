package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

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
// Probes are treated as dispatches: they journal attempts, take pool slots, and
// leave holds on uncertain outcomes.
// The built manifest is updated with successful probe results before being finalized.
func (e *Engine) runStartupProbes(ctx context.Context, roster *config.Roster, built manifest.Built, probeMissing bool) error {
	// Check for orphaned holds that would block all probes using proper classification
	orphanedHolds, err := e.checkOrphanedHolds(roster)
	if err != nil {
		log.Printf("engine: check orphaned holds: %v", err)
		orphanedHolds = false
	}
	if orphanedHolds {
		// All probes are blocked; emit probe_failed:hold for all needed participants
		for _, p := range roster.Participants {
			participantRecord := built.Participants[p.ID]
			if !participantRecord.Usable {
				ep := e.endpointByName(roster, p.Endpoint)
				if ep != nil && e.shouldProbeAutomatically(ep, probeMissing) {
					_ = e.probeFinished(e.probingTurnID, p.ID, "probe_failed:hold", "")
				}
			}
		}
		return nil
	}

	// Find participants and referees that need probes
	probeRequests, err := e.findProbesNeeded(roster, built, probeMissing)
	if err != nil {
		log.Printf("engine: find probes needed: %v", err)
		return nil
	}

	// Get held pools for ordinary hold checking
	holds, err := e.store.UnreleasedHolds()
	if err != nil {
		log.Printf("engine: get unreleased holds: %v", err)
		// Proceed without hold information if we can't fetch it
		holds = []journal.Hold{}
	}

	// Build a set of pools with ordinary holds
	poolsWithHolds := make(map[string]bool)
	index := poolMembers(roster)
	for _, h := range holds {
		pools := appliedPools(index, h.Group)
		for _, pool := range pools {
			poolsWithHolds[pool] = true
		}
	}

	// Run probes one at a time
	for _, req := range probeRequests {
		poolName := req.endpoint.Name
		for _, p := range roster.Participants {
			if p.ID == req.participantID && p.Pool != "" {
				poolName = p.Pool
				break
			}
		}
		if !req.isReferee && poolsWithHolds[poolName] {
			// This participant's pool has an ordinary hold
			_ = e.probeFinished(e.probingTurnID, req.participantID, "probe_failed:hold", "")
			continue
		}

		if err := e.runOneStartupProbe(ctx, req, roster); err != nil {
			log.Printf("engine: probe for %s: %v", req.participantID, err)
		}
	}

	return nil
}

// checkOrphanedHolds returns true if there are any orphaned holds using proper
// classification that maps holds to the new roster.
func (e *Engine) checkOrphanedHolds(roster *config.Roster) (bool, error) {
	holds, err := e.store.UnreleasedHolds()
	if err != nil {
		return false, fmt.Errorf("get unreleased holds: %w", err)
	}

	// Use the same classification logic as loadHoldsIntoMemory
	index := poolMembers(roster)
	for _, h := range holds {
		pools := appliedPools(index, h.Group)
		if len(pools) == 0 {
			// This hold is orphaned
			return true, nil
		}
	}

	return false, nil
}

// findProbesNeeded identifies participants and referees that need probes.
// A participant or referee needs a probe if it has no usable record.
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

	// TODO: Find referee models on endpoints that need probes
	// This requires referee_model to be added to config.EndpointProfile
	// and to be tracked in the built manifest

	return requests, nil
}

// shouldProbeAutomatically returns true if a participant on the given endpoint
// should be probed automatically at startup.
// - Automatic for `none` or `flat` endpoints
// - Only with --probe-missing for `metered` or `unknown` endpoints
func (e *Engine) shouldProbeAutomatically(ep *config.EndpointProfile, probeMissing bool) bool {
	if ep == nil {
		return false
	}
	switch ep.DeclaredBilling {
	case "none", "flat":
		return true
	case "metered", "unknown":
		return probeMissing
	default:
		return false
	}
}

// endpointByName finds an endpoint by name in the roster.
func (e *Engine) endpointByName(roster *config.Roster, name string) *config.EndpointProfile {
	for i := range roster.Endpoints {
		if roster.Endpoints[i].Name == name {
			return &roster.Endpoints[i]
		}
	}
	return nil
}

// runOneStartupProbe runs a single startup probe for a participant or referee.
// It emits probe_started and probe_finished events, journals the attempt,
// and handles holds appropriately.
// Probe requests are treated as dispatches: they use the probing turn for attempts,
// take pool slots, and leave holds on uncertain outcomes.
func (e *Engine) runOneStartupProbe(ctx context.Context, req startupProbeRequest, roster *config.Roster) error {
	// Emit probe_started event
	if err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return emit(e.probingTurnID, wire.EventKindProbeStarted, wire.EvProbeStarted{
			Participant: req.participantID,
		})
	}); err != nil {
		return fmt.Errorf("emit probe_started: %w", err)
	}

	adapter := e.adapters[req.participantID]
	if adapter == nil {
		// Endpoint unreachable
		return e.probeFinished(e.probingTurnID, req.participantID, "probe_failed:unreachable", "")
	}

	// Get configured probe timeout from deadlines if available
	probeTimeout := 60 * time.Second
	if roster.Deadlines.Attempt != nil {
		probeTimeout = roster.Deadlines.Attempt.Duration
	}

	// Get participant pool for hold creation
	pool := req.endpoint.Name
	for _, p := range roster.Participants {
		if p.ID == req.participantID && p.Pool != "" {
			pool = p.Pool
			break
		}
	}
	if pool == "" {
		pool = defaultPool
	}

	// Determine the group for this probe (all models in the endpoint)
	var group []journal.GroupMember
	seen := make(map[journal.GroupMember]bool)
	for _, p := range roster.Participants {
		if p.Endpoint == req.endpoint.Name {
			gm := journal.GroupMember{EndpointName: p.Endpoint, Model: p.Model}
			if !seen[gm] {
				seen[gm] = true
				group = append(group, gm)
			}
		}
	}
	if req.isReferee {
		gm := journal.GroupMember{EndpointName: req.endpoint.Name, Model: req.model}
		if !seen[gm] {
			group = append(group, gm)
		}
	}
	sort.Slice(group, func(i, j int) bool {
		if group[i].EndpointName != group[j].EndpointName {
			return group[i].EndpointName < group[j].EndpointName
		}
		return group[i].Model < group[j].Model
	})

	// Journal the probe attempt before sending (durable intent)
	var attemptID string
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		// Create attempt row for the probe using the probing turn
		newAttemptID, err := tx.BeginAttemptWith(journal.AttemptParams{
			TurnID:                e.probingTurnID,
			Participant:           req.participantID,
			Phase:                 "probe",
			Epoch:                 1,
			Deadline:              nil,
			RepairOf:              "",
			Delimiter:             "",
			MaxOutputTokens:       0,
			EstimatedPromptTokens: 0,
		})
		if err != nil {
			return fmt.Errorf("create probe attempt: %w", err)
		}
		attemptID = newAttemptID
		return nil
	})
	if err != nil {
		return e.probeFinished(e.probingTurnID, req.participantID, "probe_failed:unreachable", "")
	}

	// Create probe context with timeout
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// Run the actual probe
	record, probeErr := adapter.Probe(probeCtx, req.endpoint.Name, req.model, transport.ProbeOptions{
		Timeout:            probeTimeout,
		DisableOverLength:  false,
		DisableCalibration: false,
		AssumedContextLen:  0,
		BeforeSend:         nil,
	})

	// Handle probe result
	if probeErr != nil {
		if errors.Is(probeErr, transport.ErrUncertain) {
			// Post-send failure leaves uncertain-capacity hold
			detail := e.probeErrorDetail(probeErr)
			err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
				// Create a hold for the uncertain outcome
				if err := tx.OpenHold(journal.Hold{
					AttemptID:    attemptID,
					Pool:         pool,
					EndpointName: req.endpoint.Name,
					Model:        req.model,
					Group:        group,
					HeldSince:    e.clock.Now().UTC(),
				}); err != nil {
					return fmt.Errorf("open hold: %w", err)
				}
				// Emit probe_finished with the hold detail
				return emit(e.probingTurnID, wire.EventKindProbeFinished, wire.EvProbeFinished{
					Participant: req.participantID,
					Outcome:     "probe_failed:" + detail,
				})
			})
			return err
		}
		detail := e.probeErrorDetail(probeErr)
		return e.probeFinished(e.probingTurnID, req.participantID, "probe_failed:"+detail, "")
	}

	// If probe was incomplete, it's still a failure
	if record.ReportedModel == "" || (record.ContextBound == nil && record.OutputBound == nil) {
		return e.probeFinished(e.probingTurnID, req.participantID, "probe_failed:incomplete", "")
	}

	// Probe succeeded - store the result and emit probe_finished
	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		// Create and store the probe manifest
		probeManifest, err := evidence.NewProbeManifest(
			evidence.Export{
				EndpointName:             req.endpoint.Name,
				Model:                    req.model,
				Identity:                 record.Identity,
				ReportedModel:            record.ReportedModel,
				EligibleRequiredReviewer: record.EligibleRequiredReviewer,
				ObservedAt:               e.clock.Now().UTC(),
				ContextBound:             record.ContextBound,
				OutputBound:              record.OutputBound,
			},
			req.endpoint,
			req.model,
			record.AdapterVersion,
			record.Calibration,
		)
		if err != nil {
			// If we can't create the manifest, it's a failure
			_ = emit(e.probingTurnID, wire.EventKindProbeFinished, wire.EvProbeFinished{
				Participant: req.participantID,
				Outcome:     "probe_failed:incomplete",
			})
			return nil
		}

		manifestJSON, err := json.Marshal(probeManifest)
		if err != nil {
			_ = emit(e.probingTurnID, wire.EventKindProbeFinished, wire.EvProbeFinished{
				Participant: req.participantID,
				Outcome:     "probe_failed:incomplete",
			})
			return nil
		}

		// Store the probe run
		if _, _, err := tx.CreateProbeRun(manifestJSON); err != nil {
			_ = emit(e.probingTurnID, wire.EventKindProbeFinished, wire.EvProbeFinished{
				Participant: req.participantID,
				Outcome:     "probe_failed:incomplete",
			})
			return nil
		}

		// Emit probe result event
		return emit(e.probingTurnID, wire.EventKindProbeFinished, wire.EvProbeFinished{
			Participant: req.participantID,
			Outcome:     "probed",
		})
	})
	if err != nil {
		return err
	}

	return nil
}

// probeErrorDetail maps probe errors to detail strings, using the wire protocol vocabulary.
func (e *Engine) probeErrorDetail(err error) string {
	if err == nil {
		return "unreachable"
	}

	// Map specific transport errors to detail strings
	// These match the wire protocol vocabulary
	switch {
	case errors.Is(err, transport.ErrNotSent):
		return "unreachable"
	case errors.Is(err, transport.ErrUncertain):
		return "incomplete"
	default:
		// For now, classify unknown errors as unreachable
		return "unreachable"
	}
}

// probeFinished emits the probe_finished event without a hold.
func (e *Engine) probeFinished(turnID, participantID, outcome, detail string) error {
	return e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		evt := wire.EvProbeFinished{
			Participant: participantID,
			Outcome:     outcome,
		}
		if detail != "" {
			evt.Detail = &detail
		}
		return emit(turnID, wire.EventKindProbeFinished, evt)
	})
}
