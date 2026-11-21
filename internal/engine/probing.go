package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// Start-time probing (v5 delta section 1a). It runs between the two commits
// of Start: after run_opened and before the manifest is written. Probes run
// one at a time, every request of a probe is a dispatch with its own attempt
// row, pool slot and, when its outcome is unknown, capacity hold. Nothing here
// stops the engine: a probe that fails leaves its participant unprobed.

// probeTimeout is the timeout of each probe request, as for `syndic probe`.
const probeTimeout = 60 * time.Second

// Outcomes of probe_finished, and the reasons a probe failed. The detail of an
// unprobed participant is probeFailedPrefix plus the reason.
const (
	probeOutcomeProbed = "probed"
	probeOutcomeFailed = "probe_failed"

	probeFailedPrefix = "probe_failed:"

	probeReasonUnreachable      = "unreachable"
	probeReasonUnauthorized     = "unauthorized"
	probeReasonModelUnavailable = "model_unavailable"
	probeReasonHold             = "hold"
	probeReasonIncomplete       = "incomplete"
)

// refereeIdentity is an endpoint and model that the roster uses as a referee
// (v5 delta section 6) without any participant naming it.
type refereeIdentity struct {
	Endpoint string
	Model    string
}

// refereeIdentities returns the referee models the roster names. The roster
// has no referee configuration yet, so none: the referee task fills this in
// and start-time probing already selects, probes and records whatever it
// returns exactly as it does a participant.
var refereeIdentities = func(*config.Roster) []refereeIdentity { return nil }

// probeTarget is one {endpoint, model} identity to probe. Participants lists
// the roster participants that stay unprobed without it; it is empty for a
// referee identity no participant names.
type probeTarget struct {
	participants []string
	endpoint     config.EndpointProfile
	model        string
	pool         string
	contextLen   int
	adapter      transport.Adapter
}

// label names the target in probe events: its participants, or the referee.
func (t *probeTarget) labels() []string {
	if len(t.participants) > 0 {
		return t.participants
	}
	return []string{"referee:" + t.endpoint.Name + ":" + t.model}
}

// probeResult is what start-time probing decided for the participants that
// needed a probe. failed maps each to the reason it stays unprobed; wrote
// reports that a usable record was journaled, so the manifest must be rebuilt.
type probeResult struct {
	failed map[string]string
	wrote  bool
}

// runStartupProbes probes every participant and referee identity with no
// usable record that the endpoint's billing and the recovered holds allow. It
// never returns an error: every failure is the participant's probe_failed
// detail. built is the manifest state before any probe.
func (e *Engine) runStartupProbes(ctx context.Context, roster *config.Roster, built manifest.Built, adapters map[string]transport.Adapter, versions map[string]string) probeResult {
	res := probeResult{failed: map[string]string{}}
	if e.noStartupProbes {
		return res
	}
	targets := e.probeTargets(roster, built, adapters, versions)
	if len(targets) == 0 {
		return res
	}

	blocked, orphaned, err := e.holdBlocks(roster)
	if err != nil {
		// Fail closed: with the holds unreadable no pool can be shown clear.
		log.Printf("engine: read holds before probing: %v", err)
		orphaned = true
	}

	slots := map[string]int{}
	for i := range targets {
		t := &targets[i]
		if orphaned || blocked[t.pool] {
			e.finishBlocked(t, &res)
			continue
		}
		e.probeOne(ctx, roster, t, slots, blocked, &res)
	}
	return res
}

// probeTargets selects what needs a probe, in roster order: participants
// with no usable record on an endpoint that is probed automatically or by
// --probe-missing, then referee identities without a usable record. A usable
// record is never probed, and an identity shared by several participants is
// probed once.
func (e *Engine) probeTargets(roster *config.Roster, built manifest.Built, adapters map[string]transport.Adapter, versions map[string]string) []probeTarget {
	var targets []probeTarget
	index := map[string]int{}
	for _, p := range roster.Participants {
		if built.Participants[p.ID].Usable {
			continue
		}
		ep := endpointOf(roster, p.Endpoint)
		if ep == nil || !probeAllowed(*ep, e.probeMissing) {
			continue
		}
		key := p.Endpoint + "\x00" + p.Model
		if i, ok := index[key]; ok {
			targets[i].participants = append(targets[i].participants, p.ID)
			continue
		}
		index[key] = len(targets)
		ctxLen := 0
		if p.ContextTokens != nil {
			ctxLen = *p.ContextTokens
		}
		targets = append(targets, probeTarget{
			participants: []string{p.ID},
			endpoint:     *ep,
			model:        p.Model,
			pool:         poolName(p),
			contextLen:   ctxLen,
			adapter:      adapters[p.ID],
		})
	}

	for _, r := range refereeIdentities(roster) {
		ep := endpointOf(roster, r.Endpoint)
		if ep == nil || !probeAllowed(*ep, e.probeMissing) {
			continue
		}
		key := r.Endpoint + "\x00" + r.Model
		if _, ok := index[key]; ok {
			continue // a participant on the same identity is probed already
		}
		// A participant of the roster on this identity with a usable record
		// settles it, whatever its pool.
		covered := false
		for _, p := range roster.Participants {
			if p.Endpoint == r.Endpoint && p.Model == r.Model && built.Participants[p.ID].Usable {
				covered = true
			}
		}
		if covered {
			continue
		}
		pool, version, adapter := defaultPool, "", transport.Adapter(nil)
		for _, p := range roster.Participants {
			if p.Endpoint == r.Endpoint {
				pool, version, adapter = poolName(p), versions[p.ID], adapters[p.ID]
				break
			}
		}
		if adapter == nil {
			a, v, err := e.newAdapter(config.Participant{ID: "referee:" + r.Endpoint + ":" + r.Model, Endpoint: r.Endpoint, Model: r.Model, Route: "api"}, *ep)
			if err != nil {
				log.Printf("engine: adapter for referee %s/%s: %v", r.Endpoint, r.Model, err)
				continue
			}
			adapter, version = a, v
		}
		usable, err := e.recordUsable(*ep, r.Model, version)
		if err != nil {
			log.Printf("engine: referee record %s/%s: %v", r.Endpoint, r.Model, err)
		}
		if usable {
			continue
		}
		index[key] = len(targets)
		targets = append(targets, probeTarget{endpoint: *ep, model: r.Model, pool: pool, adapter: adapter})
	}
	return targets
}

// probeAllowed reports whether start-time probing may probe a participant on
// the endpoint: automatically on `none` or `flat`, on `metered` or `unknown`
// only under --probe-missing. An endpoint with a spending_control of
// "external" is still `metered` here; the setting does not lift the flag.
// A metered endpoint without that spending control is never probed, flag or
// not, as `syndic probe` refuses it.
func probeAllowed(ep config.EndpointProfile, probeMissing bool) bool {
	switch ep.DeclaredBilling {
	case "none", "flat":
		return true
	case "metered":
		return probeMissing && ep.SpendingControl == "external"
	case "unknown":
		return probeMissing
	}
	return false
}

func endpointOf(roster *config.Roster, name string) *config.EndpointProfile {
	for i := range roster.Endpoints {
		if roster.Endpoints[i].Name == name {
			return &roster.Endpoints[i]
		}
	}
	return nil
}

// recordUsable reports whether the journal holds a record for the identity
// whose fingerprint matches the endpoint's current profile, as manifest.Build
// decides for a participant.
func (e *Engine) recordUsable(ep config.EndpointProfile, model, adapterVersion string) (bool, error) {
	rec, found, err := e.store.LatestProbeRecord(ep.Name, model)
	if err != nil || !found {
		return false, err
	}
	pm, hasFingerprint, err := evidence.ParseProbeManifest(rec.Manifest)
	if err != nil || !hasFingerprint {
		return false, err
	}
	fp, err := evidence.ConfigFingerprint(ep, model, adapterVersion)
	if err != nil {
		return false, err
	}
	return pm.ConfigFingerprint == fp, nil
}

// holdBlocks reads the recovered holds. blocked is every current pool holding
// an ordinary hold; orphaned is true when a hold has no current pool
// containing a member of its group, which blocks every probe.
func (e *Engine) holdBlocks(roster *config.Roster) (blocked map[string]bool, orphaned bool, err error) {
	holds, err := e.store.UnreleasedHolds()
	if err != nil {
		return nil, false, err
	}
	blocked = map[string]bool{}
	index := poolMembers(roster)
	for _, h := range holds {
		pools := appliedPools(index, h.Group)
		if len(pools) == 0 {
			orphaned = true
		}
		for _, pool := range pools {
			blocked[pool] = true
		}
	}
	return blocked, orphaned, nil
}

// finishBlocked reports a target a hold keeps from probing: probe_started is
// not sent, probe_finished carries probe_failed with detail hold.
func (e *Engine) finishBlocked(t *probeTarget, res *probeResult) {
	for _, label := range t.labels() {
		e.emitProbe(wire.EventKindProbeFinished, wire.EvProbeFinished{
			Participant: label,
			Outcome:     probeOutcomeFailed,
			Detail:      strPtr(probeReasonHold),
		})
	}
	for _, id := range t.participants {
		res.failed[id] = probeReasonHold
	}
}

// emitProbe emits one probe event in its own Tx. A failed write is logged:
// it must not stop the engine.
func (e *Engine) emitProbe(kind string, payload any) {
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error { return emit("", kind, payload) })
	if err != nil {
		log.Printf("engine: emit %s: %v", kind, err)
	}
}

// probeDispatch is the dispatch bookkeeping of one probe: the attempt row of
// the request in flight and the slot it holds.
type probeDispatch struct {
	e       *Engine
	roster  *config.Roster
	t       *probeTarget
	runID   string
	pool    string
	group   []journal.GroupMember
	limit   int
	slots   map[string]int
	current string // attempt id of the request in flight, "" when none
}

// beforeSend is the probe's BeforeSend hook. It settles the previous request
// (its response came back, or the next could not have been sent), then, in one
// Tx, writes the next request's attempt row and takes a slot in the pool. A
// failure sends nothing: the adapter reports it as ErrNotSent.
func (d *probeDispatch) beforeSend(req *transport.ProbeRequest) error {
	var next string
	err := d.e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		if d.current != "" {
			if err := tx.CompleteProbeAttempt(d.current, string(transport.StatusComplete), transport.TransportStatusOK); err != nil {
				return err
			}
		}
		if d.current == "" && d.slots[d.pool] >= d.limit {
			return fmt.Errorf("pool %q has no free slot", d.pool)
		}
		id, err := tx.BeginProbeAttempt(journal.ProbeAttemptParams{
			RunID:       d.runID,
			Participant: firstOr(d.t.participants, ""),
			Endpoint:    d.t.endpoint.Name,
			Model:       d.t.model,
			Pool:        d.pool,
			Group:       d.group,
			Step:        req.Step,
		})
		next = id
		return err
	})
	if err != nil {
		// The Tx rolled back, so the previous attempt is still pending.
		return err
	}
	if d.current == "" {
		d.slots[d.pool]++
	}
	d.current = next
	return nil
}

// settle ends the probe's last request after the adapter returned err. A
// request that provably reached no server (ErrNotSent), or got a response,
// completes its attempt and frees the slot; every other failure after a
// possible send opens an uncertain-capacity hold on the pool, which keeps the
// slot taken, so a restart never resends. Classified failures keep their
// transport status for reporting. It reports whether a hold was opened.
func (d *probeDispatch) settle(err error) (held bool) {
	if d.current == "" {
		return false
	}
	id := d.current
	d.current = ""
	status, transportStatus, hold := string(transport.StatusComplete), transport.TransportStatusOK, false
	switch {
	case err == nil, errors.Is(err, transport.ErrIncompleteProbe):
	case errors.Is(err, transport.ErrNotSent):
		status, transportStatus = string(transport.StatusError), statusNotSentPrefix+err.Error()
	case errors.Is(err, transport.ErrUnreachable):
		status, transportStatus, hold = string(transport.StatusError), transport.TransportStatusUnreachable, true
	case errors.Is(err, transport.ErrUnauthorized):
		status, transportStatus, hold = string(transport.StatusError), transport.TransportStatusUnauthorized, true
	case errors.Is(err, transport.ErrModelUnavailable):
		status, transportStatus, hold = string(transport.StatusError), transport.TransportStatusModelUnavailable, true
	default:
		// ErrUncertain and anything unclassified: the request may have been
		// processed, so its slot stays held.
		status, transportStatus, hold = string(transport.StatusInterrupted), statusAdapterErrPrefix+err.Error(), true
	}
	txErr := d.e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		if err := tx.CompleteProbeAttempt(id, status, transportStatus); err != nil {
			return err
		}
		if !hold {
			return nil
		}
		if err := tx.OpenHold(journal.Hold{
			AttemptID:    id,
			Pool:         d.pool,
			EndpointName: d.t.endpoint.Name,
			Model:        d.t.model,
			Group:        d.group,
			HeldSince:    d.e.clock.Now().UTC(),
		}); err != nil {
			return err
		}
		return emit("", wire.EventKindCapacity, wire.EvCapacity{Pool: d.pool, HeldByAttempt: id, State: capacityUncertain})
	})
	if txErr != nil {
		// The outcome could not be journaled. The attempt stays pending, so the
		// next start recovers it as unknown and holds the pool.
		log.Printf("engine: settle probe attempt %s: %v", id, txErr)
		return true
	}
	if !hold {
		d.slots[d.pool]--
	}
	return hold
}

// probeOne runs one probe and records what came of it.
func (e *Engine) probeOne(ctx context.Context, roster *config.Roster, t *probeTarget, slots map[string]int, blocked map[string]bool, res *probeResult) {
	for _, label := range t.labels() {
		e.emitProbe(wire.EventKindProbeStarted, wire.EvProbeStarted{Participant: label})
	}

	fail := func(reason string) {
		for _, label := range t.labels() {
			e.emitProbe(wire.EventKindProbeFinished, wire.EvProbeFinished{
				Participant: label,
				Outcome:     probeOutcomeFailed,
				Detail:      strPtr(reason),
			})
		}
		for _, id := range t.participants {
			res.failed[id] = reason
		}
	}

	if t.adapter == nil {
		fail(probeReasonUnreachable)
		return
	}
	limit, _ := roster.PoolFor(t.pool)
	if limit < 1 {
		limit = 1
	}
	d := &probeDispatch{
		e: e, roster: roster, t: t, runID: e.runID, pool: t.pool,
		group: probeGroup(roster, t), limit: limit, slots: slots,
	}

	opts := transport.ProbeOptions{
		Timeout:           e.probeTimeout,
		AssumedContextLen: t.contextLen,
		BeforeSend:        d.beforeSend,
	}
	record, err := t.adapter.Probe(ctx, t.endpoint.Name, t.model, opts)
	held := d.settle(err)
	if held {
		blocked[t.pool] = true
	}

	incomplete := errors.Is(err, transport.ErrIncompleteProbe)
	if err != nil && !incomplete {
		fail(probeFailureReason(err, held))
		return
	}

	// A probe that captured no reported model or no working max-tokens field
	// is a failed probe: raw record and export for diagnosis, no usable record.
	exp, expErr := e.writeProbeEvidence(record, t.endpoint)
	if incomplete || record.ReportedModel == "" || record.MaxTokensField == "" {
		fail(probeReasonIncomplete)
		return
	}
	if expErr != nil {
		log.Printf("engine: probe evidence for %s/%s: %v", t.endpoint.Name, t.model, expErr)
		fail(probeReasonIncomplete)
		return
	}

	pm, err := evidence.NewProbeManifest(exp, t.endpoint, t.model, record.AdapterVersion, record.Calibration)
	var data []byte
	if err == nil {
		data, err = json.Marshal(pm)
	}
	if err == nil {
		err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
			if _, _, err := tx.CreateProbeRun(data); err != nil {
				return err
			}
			for _, label := range t.labels() {
				if err := emit("", wire.EventKindProbeFinished, wire.EvProbeFinished{
					Participant: label,
					Outcome:     probeOutcomeProbed,
				}); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err != nil {
		// Nothing was committed, so the identity stays unprobed.
		log.Printf("engine: record probe %s/%s: %v", t.endpoint.Name, t.model, err)
		fail(probeReasonIncomplete)
		return
	}
	res.wrote = true
}

// writeProbeEvidence redacts the record into its export and, when the engine
// has directories for them, writes the raw record and the export as
// `syndic probe` does. A write failure is returned but the export is still
// usable.
func (e *Engine) writeProbeEvidence(record transport.CapabilityRecord, ep config.EndpointProfile) (evidence.Export, error) {
	exp, err := evidence.Redact(record, ep)
	if err != nil {
		return evidence.Export{}, err
	}
	var errs []string
	if e.probeRawDir != "" {
		if _, err := evidence.WriteRaw(record, e.probeRawDir); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if e.probeExportDir != "" {
		if _, err := evidence.WriteExport(exp, e.probeExportDir); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		log.Printf("engine: write probe evidence: %s", strings.Join(errs, "; "))
	}
	return exp, nil
}

// probeFailureReason is the reason a probe error leaves in probe_failed:<reason>.
func probeFailureReason(err error, held bool) string {
	switch {
	case errors.Is(err, transport.ErrUnauthorized):
		return probeReasonUnauthorized
	case errors.Is(err, transport.ErrModelUnavailable):
		return probeReasonModelUnavailable
	case errors.Is(err, transport.ErrIncompleteProbe):
		return probeReasonIncomplete
	case errors.Is(err, transport.ErrUnreachable):
		return probeReasonUnreachable
	case held:
		return probeReasonHold
	}
	return probeReasonUnreachable
}

// probeGroup is the {endpoint, model} members of the target's pool in the
// roster, plus the probed identity, sorted: the group of a hold on the pool.
func probeGroup(roster *config.Roster, t *probeTarget) []journal.GroupMember {
	seen := map[journal.GroupMember]bool{}
	var group []journal.GroupMember
	add := func(m journal.GroupMember) {
		if !seen[m] {
			seen[m] = true
			group = append(group, m)
		}
	}
	for _, p := range roster.Participants {
		if poolName(p) == t.pool {
			add(journal.GroupMember{EndpointName: p.Endpoint, Model: p.Model})
		}
	}
	add(journal.GroupMember{EndpointName: t.endpoint.Name, Model: t.model})
	sort.Slice(group, func(i, j int) bool {
		if group[i].EndpointName != group[j].EndpointName {
			return group[i].EndpointName < group[j].EndpointName
		}
		return group[i].Model < group[j].Model
	})
	return group
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

// probeTimeoutOf is the per-request probe timeout an Options value asks for.
func probeTimeoutOf(d time.Duration) time.Duration {
	if d <= 0 {
		return probeTimeout
	}
	return d
}
