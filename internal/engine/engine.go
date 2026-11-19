package engine

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/manifest"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/wire"
)

// Turn modes, as written in a preset.
const (
	modeSolo   = "solo"
	modeReview = "review"
)

// Turn states carried by turn_state events and the turns table.
const (
	StateSubmitted           = "submitted"
	StateAuthorDispatched    = "author_dispatched"
	StateCandidateFrozen     = "candidate_frozen"
	StateReviewersDispatched = "reviewers_dispatched"
	StateApproved            = "approved"
	StateSoloComplete        = "solo_complete"
	StateIncomplete          = "incomplete"
	StateCancelled           = "cancelled"
	StateOverridden          = "overridden"
)

// Participant states.
const (
	ParticipantIdle     = "idle"
	ParticipantQueued   = "queued"
	ParticipantRunning  = "running"
	ParticipantUnprobed = "unprobed"
	// ParticipantCancelling lasts from the terminal Tx until Adapter.Cancel
	// returns; ParticipantUnknown while an attempt of the participant is held.
	ParticipantCancelling = "cancelling"
	ParticipantUnknown    = "unknown"
)

// Attempt phases and message senders.
const (
	phaseAuthor   = "author"
	phaseReview   = "review"
	phaseDelivery = "terminal_delivery"
	senderUser    = "user"
	senderEngine  = "engine"
)

// Terminal reasons.
const (
	reasonSoloOK       = "solo_ok"
	reasonAuthorFailed = "author_failed:"
	reasonAllApproved  = "all_approved"
	reasonCancelled    = "cancelled"

	reasonDeadlineTurn    = "deadline:turn"
	reasonDeadlineAttempt = "deadline:attempt:"
)

const defaultPool = "default"

// ErrNotImplemented is returned by requests a later task implements.
var ErrNotImplemented = errors.New("not implemented")

// RosterError is returned by Start when the roster fails to load or validate.
type RosterError struct{ Err error }

func (e *RosterError) Error() string { return "roster: " + e.Err.Error() }

func (e *RosterError) Unwrap() error { return e.Err }

// Clock abstracts time so tests can control it.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// Options configure an Engine.
type Options struct {
	Store         *journal.Store
	Hub           *hub.Hub
	RosterPath    string
	EngineVersion string
	Clock         Clock     // nil means the real clock
	Rand          io.Reader // nil means crypto/rand.Reader
	NewAdapter    func(p config.Participant, ep config.EndpointProfile) (adapter transport.Adapter, adapterVersion string, err error)
}

// Engine is the review engine.
type Engine struct {
	store      *journal.Store
	hub        *hub.Hub
	rosterPath string
	version    string
	clock      Clock
	rand       io.Reader
	retryDelay time.Duration // the package-level retryDelay when the engine was built
	noDelivery bool          // the package-level deliveryOff when the engine was built
	newAdapter func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error)

	// payloadHook lets tests replace an event payload (for example with one
	// that cannot be marshalled) to make a transaction fail after its row
	// writes. It is set before any request and never changed afterwards.
	payloadHook func(kind string, payload any) any

	// crashHook lets tests stop the engine between the terminal Tx and the
	// Adapter.Cancel calls: when it returns true no call is made and nothing
	// further is written. It is set before any request.
	crashHook func() bool

	// mu owns everything below.
	mu            sync.Mutex
	started       bool
	cancelers     int        // goroutines calling Adapter.Cancel; Stop waits for none
	cancelersOut  *sync.Cond // signalled on e.mu when cancelers drops to zero
	rosterAbs     string
	roster        *config.Roster
	adapters      map[string]transport.Adapter
	built         manifest.Built
	runID         string
	manifestHash  string
	runFirstSeq   int64
	startedAt     time.Time
	open          *openTurn
	running       map[string]bool
	inflight      map[string]int // dispatches holding a pool slot, per participant
	queued        map[string]bool
	pools         map[string]*pool
	exceeded      map[string]bool   // participants whose estimate was exceeded this run
	holds         map[string]*hold  // unreleased capacity holds, by attempt id
	orphanedHolds map[string]*hold  // holds whose group has no members in new roster
	calling       map[string]bool   // attempts whose Complete call has not returned
	cancelled     map[string]bool   // calling attempts the engine itself cancelled
	cancelling    map[string]int    // Adapter.Cancel calls pending, per participant
	pstate        map[string]string // cancelling or unknown last reported, per participant
	failed        map[string]string // failure state of the last dispatch, per participant
}

// New returns an Engine. It does not read the roster or touch the journal;
// Start does.
func New(o Options) (*Engine, error) {
	switch {
	case o.Store == nil:
		return nil, errors.New("engine: Store is required")
	case o.Hub == nil:
		return nil, errors.New("engine: Hub is required")
	case o.RosterPath == "":
		return nil, errors.New("engine: RosterPath is required")
	case o.NewAdapter == nil:
		return nil, errors.New("engine: NewAdapter is required")
	}
	e := &Engine{
		store:         o.Store,
		hub:           o.Hub,
		rosterPath:    o.RosterPath,
		version:       o.EngineVersion,
		clock:         o.Clock,
		rand:          o.Rand,
		retryDelay:    retryDelay,
		noDelivery:    deliveryOff,
		newAdapter:    o.NewAdapter,
		running:       make(map[string]bool),
		inflight:      make(map[string]int),
		queued:        make(map[string]bool),
		pools:         make(map[string]*pool),
		holds:         make(map[string]*hold),
		orphanedHolds: make(map[string]*hold),
		calling:       make(map[string]bool),
		cancelled:     make(map[string]bool),
		cancelling:    make(map[string]int),
		pstate:        make(map[string]string),
		failed:        make(map[string]string),
	}
	e.cancelersOut = sync.NewCond(&e.mu)
	if e.clock == nil {
		e.clock = realClock{}
	}
	if e.rand == nil {
		e.rand = rand.Reader
	}
	return e, nil
}

// tx runs fn in one hub transaction. Events emitted through emit commit with
// the row writes or not at all.
func (e *Engine) tx(fn func(tx *journal.Tx, emit hub.Emit) error) error {
	return e.hub.Tx(func(tx *journal.Tx, emit hub.Emit) error {
		return fn(tx, func(turnID, kind string, payload any) error {
			if e.payloadHook != nil {
				payload = e.payloadHook(kind, payload)
			}
			return emit(turnID, kind, payload)
		})
	})
}

// Start reads and validates the roster, builds the manifest, writes the
// engine run and announces it with engine_started.
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("engine: already started")
	}

	// Recovery: mark pending attempts as unknown and end non-terminal turns
	recoveredAttempts := 0
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		// Get all pending attempts
		pendingAttempts, err := tx.RecoverPending()
		if err != nil {
			return fmt.Errorf("recover pending attempts: %w", err)
		}

		existingHolds, err := tx.UnreleasedHolds()
		if err != nil {
			return fmt.Errorf("get existing holds: %w", err)
		}
		held := make(map[string]bool, len(existingHolds))
		for _, h := range existingHolds {
			held[h.AttemptID] = true
		}

		for _, attemptID := range pendingAttempts {
			turnID, runID, err := tx.GetAttemptTurnAndRun(attemptID)
			if err != nil {
				return fmt.Errorf("get turn for attempt %s: %w", attemptID, err)
			}
			run, err := tx.GetRun(runID)
			if err != nil {
				return fmt.Errorf("get run %s: %w", runID, err)
			}

			if !held[attemptID] {
				// Get the attempt to extract participant ID
				attempt, err := tx.GetAttempt(attemptID)
				if err != nil {
					return fmt.Errorf("get attempt: %w", err)
				}

				// Extract participant info from manifest
				group, poolName, endpointName, model, err := e.extractParticipantInfoFromManifest(run.Manifest, attempt.Participant)
				if err != nil {
					return fmt.Errorf("extract participant info from manifest: %w", err)
				}

				// Open hold
				hold := journal.Hold{
					AttemptID:    attemptID,
					Pool:         poolName,
					EndpointName: endpointName,
					Model:        model,
					Group:        group,
					HeldSince:    e.clock.Now().UTC(),
				}
				if err := tx.OpenHold(hold); err != nil {
					return fmt.Errorf("open hold for attempt %s: %w", attemptID, err)
				}
			}

			// Mark attempt as unknown
			if err := tx.MarkUnknown(attemptID); err != nil {
				return fmt.Errorf("mark attempt unknown: %w", err)
			}

			// Emit attempt_unknown event
			if err := emit(turnID, wire.EventKindAttemptUnknown, wire.EvAttemptUnknown{
				AttemptID: attemptID,
			}); err != nil {
				return fmt.Errorf("emit attempt_unknown: %w", err)
			}

			recoveredAttempts++
		}

		// End non-terminal turns with engine_restart reason
		pendingTurns, err := tx.PendingTurnsWithoutTerminal()
		if err != nil {
			return fmt.Errorf("get pending turns: %w", err)
		}

		for _, turnID := range pendingTurns {
			if _, err := tx.BumpEpoch(turnID); err != nil {
				return fmt.Errorf("bump epoch for turn %s: %w", turnID, err)
			}

			if err := tx.EndTurn(turnID, StateIncomplete, "engine_restart"); err != nil {
				return fmt.Errorf("end turn %s: %w", turnID, err)
			}

			reason := "engine_restart"
			if err := emit(turnID, wire.EventKindTurnState, wire.EvTurnState{
				TurnID: turnID,
				State:  StateIncomplete,
				Reason: &reason,
			}); err != nil {
				return fmt.Errorf("emit turn_state: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("engine: recovery: %w", err)
	}

	// Now read and validate the new roster
	path, err := filepath.Abs(e.rosterPath)
	if err != nil {
		return &RosterError{Err: err}
	}
	file, err := os.ReadFile(path)
	if err != nil {
		return &RosterError{Err: err}
	}
	roster, err := config.Load(path)
	if err != nil {
		return &RosterError{Err: err}
	}
	if err := roster.Validate(); err != nil {
		return &RosterError{Err: err}
	}

	adapters := make(map[string]transport.Adapter, len(roster.Participants))
	versions := make(map[string]string, len(roster.Participants))
	for _, p := range roster.Participants {
		var ep config.EndpointProfile
		for _, candidate := range roster.Endpoints {
			if candidate.Name == p.Endpoint {
				ep = candidate
				break
			}
		}
		adapter, version, err := e.newAdapter(p, ep)
		if err != nil {
			return fmt.Errorf("engine: adapter for participant %q: %w", p.ID, err)
		}
		adapters[p.ID] = adapter
		versions[p.ID] = version
	}

	startedAt := e.clock.Now().UTC()
	built, err := manifest.Build(manifest.Input{
		Store:          e.store,
		Roster:         roster,
		RosterPath:     path,
		RosterFile:     file,
		EngineVersion:  e.version,
		StartedAt:      startedAt,
		AdapterVersion: func(p config.Participant) string { return versions[p.ID] },
	})
	if err != nil {
		return fmt.Errorf("engine: build manifest: %w", err)
	}

	before, err := e.store.MaxSeq()
	if err != nil {
		return fmt.Errorf("engine: read journal: %w", err)
	}

	unprobed := []string{}
	for _, p := range roster.Participants {
		if !built.Participants[p.ID].Usable {
			unprobed = append(unprobed, p.ID)
		}
	}

	var runID, manifestHash string
	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		var err error
		runID, manifestHash, err = tx.CreateEngineRun(built.JSON)
		if err != nil {
			return err
		}

		// Map holds across roster changes
		uncertainCapacity, orphanedHolds, err := e.mapHoldsToNewRoster(tx, roster)
		if err != nil {
			return fmt.Errorf("map holds: %w", err)
		}

		return emit("", wire.EventKindEngineStarted, wire.EvEngineStarted{
			RunID:             runID,
			ManifestHash:      manifestHash,
			RosterHash:        built.RosterHash,
			RecoveredAttempts: recoveredAttempts,
			UncertainCapacity: uncertainCapacity,
			OrphanedHolds:     orphanedHolds,
			Unprobed:          unprobed,
		})
	})
	if err != nil {
		return fmt.Errorf("engine: write run: %w", err)
	}

	firstSeq, err := e.findEngineStarted(before, runID)
	if err != nil {
		return err
	}

	// Set roster before loading holds so armHold can use it
	e.roster = roster
	e.rosterAbs = path
	e.adapters = adapters
	e.built = built
	e.runID = runID
	e.manifestHash = manifestHash
	e.runFirstSeq = firstSeq
	e.startedAt = startedAt

	// Load all unreleased holds into memory and arm their timers
	err = e.loadHoldsIntoMemory()
	if err != nil {
		return fmt.Errorf("engine: load holds: %w", err)
	}

	e.started = true
	return nil
}

// findEngineStarted returns the seq of the engine_started event for runID.
// The run id in the payload identifies it exactly, whatever else was written
// to the journal meanwhile.
func (e *Engine) findEngineStarted(since int64, runID string) (int64, error) {
	for {
		events, err := e.store.EventsSince(since, 256)
		if err != nil {
			return 0, fmt.Errorf("engine: read journal: %w", err)
		}
		if len(events) == 0 {
			return 0, errors.New("engine: engine_started event not found")
		}
		for _, ev := range events {
			since = ev.Seq
			if ev.Kind != wire.EventKindEngineStarted {
				continue
			}
			var body wire.EvEngineStarted
			if err := json.Unmarshal(ev.Data, &body); err == nil && body.RunID == runID {
				return ev.Seq, nil
			}
		}
	}
}

// Hello returns the handshake reply.
func (e *Engine) Hello() wire.HelloReply {
	e.mu.Lock()
	defer e.mu.Unlock()
	return wire.HelloReply{
		EngineVersion: e.version,
		Protocol:      wire.ProtocolVersion,
		RunID:         e.runID,
		RunFirstSeq:   e.runFirstSeq,
		RosterHash:    e.built.RosterHash,
		ManifestHash:  e.manifestHash,
	}
}

// Override marks an incomplete turn that has a candidate as overridden and
// sends its terminal deliveries. Deliveries still running from the incomplete
// outcome finish on their own; their results no longer pass acceptance.
func (e *Engine) Override(turnID, reason string) error {
	if reason == "" {
		return refusal(wire.ErrorCodeReasonRequired)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started {
		return errors.New("engine: not started")
	}

	row, err := e.store.GetTurn(turnID)
	switch {
	case errors.Is(err, journal.ErrTurnNotFound):
		r := refusal(wire.ErrorCodeNotFound)
		r.TurnID = strPtr(turnID)
		return r
	case err != nil:
		return fmt.Errorf("engine: override: %w", err)
	case row.TerminalState != StateIncomplete || row.CandidateID == "":
		r := refusal(wire.ErrorCodeNotIncomplete)
		r.TurnID = strPtr(turnID)
		return r
	}

	err = e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		blocks, err := tx.OpenBlocks(turnID)
		if err != nil {
			return err
		}
		if _, err := tx.BumpEpoch(turnID); err != nil {
			return err
		}
		if err := tx.OverrideTurn(turnID, reason); err != nil {
			return err
		}
		for _, b := range blocks {
			if err := emit(turnID, wire.EventKindBlockState, wire.EvBlockState{BlockID: b.ID, State: StateOverridden}); err != nil {
				return err
			}
		}
		return emit(turnID, wire.EventKindTurnState, wire.EvTurnState{TurnID: turnID, State: StateOverridden, Reason: &reason})
	})
	if err != nil {
		return fmt.Errorf("engine: override: %w", err)
	}

	if t := e.deliveryTurn(row); t != nil {
		e.deliverTerminal(t, StateOverridden, reason)
	}
	return nil
}

// deliveryTurn rebuilds, from the journal, the turn state terminal delivery
// reads for a turn that already left e.open. It returns nil when the turn
// cannot be rebuilt, in which case nothing is sent. e.mu is held.
func (e *Engine) deliveryTurn(row *journal.TurnRow) *openTurn {
	eff, err := e.roster.Resolve(row.Preset, row.QuickProfile)
	if err != nil {
		log.Printf("engine: turn %s: no delivery, preset unresolved: %v", row.ID, err)
		return nil
	}
	cand, err := e.store.GetCandidate(row.CandidateID)
	if err != nil {
		log.Printf("engine: turn %s: no delivery, candidate unreadable: %v", row.ID, err)
		return nil
	}
	attempts, err := e.store.ListAttempts(row.ID)
	if err != nil {
		log.Printf("engine: turn %s: no delivery, attempts unreadable: %v", row.ID, err)
		return nil
	}
	t := &openTurn{
		id:            row.ID,
		mode:          row.Mode,
		author:        eff.Preset.Author,
		eff:           eff,
		candidateHash: cand.ContentHash,
		outputs:       make(map[string]string),
		stopAttempt:   make(map[string]func() bool),
		pending:       make(map[string]time.Time),
	}
	for _, a := range attempts {
		if a.Phase == phaseReview && a.State != attemptPending && !a.Quarantined && a.RawOutput != nil {
			t.outputs[a.ID] = *a.RawOutput
		}
	}
	return t
}

// mapHoldsToNewRoster maps unreleased holds from previous runs to the new roster.
// Returns uncertain capacity pool names and orphaned holds.
func (e *Engine) mapHoldsToNewRoster(tx *journal.Tx, newRoster *config.Roster) ([]string, []wire.Hold, error) {
	// Get all unreleased holds
	holds, err := tx.UnreleasedHolds()
	if err != nil {
		return nil, nil, fmt.Errorf("get unreleased holds: %w", err)
	}

	// Build a map of pools to participants for quick lookup
	poolParticipants := make(map[string]map[string]bool) // pool -> {endpoint:model} set
	for _, p := range newRoster.Participants {
		pool := poolName(p)
		key := p.Endpoint + ":" + p.Model
		if poolParticipants[pool] == nil {
			poolParticipants[pool] = make(map[string]bool)
		}
		poolParticipants[pool][key] = true
	}

	uncertainCapacitySet := make(map[string]bool)
	var orphanedHolds []wire.Hold

	for _, h := range holds {
		// Check if this hold applies to any pool in the new roster
		appliesToAnyPool := false

		for pool := range poolParticipants {
			// Check if any participant in this pool is in the hold's group
			for _, member := range h.Group {
				key := member.EndpointName + ":" + member.Model
				if poolParticipants[pool][key] {
					uncertainCapacitySet[pool] = true
					appliesToAnyPool = true
					break
				}
			}
		}

		if !appliesToAnyPool {
			// This hold is orphaned
			orphanedHolds = append(orphanedHolds, wire.Hold{
				Pool:      h.Pool,
				AttemptID: h.AttemptID,
				Group: func() []wire.GroupMember {
					var g []wire.GroupMember
					for _, m := range h.Group {
						g = append(g, wire.GroupMember{
							EndpointName: m.EndpointName,
							Model:        m.Model,
						})
					}
					return g
				}(),
			})
		}
	}

	var uncertainCapacity []string
	for pool := range uncertainCapacitySet {
		uncertainCapacity = append(uncertainCapacity, pool)
	}
	sort.Strings(uncertainCapacity)

	return uncertainCapacity, orphanedHolds, nil
}

// loadHoldsIntoMemory loads all unreleased holds from the journal into e.holds
// and e.orphanedHolds, and arms their timers. e.roster must already be set. e.mu is held.
func (e *Engine) loadHoldsIntoMemory() error {
	holds, err := e.store.UnreleasedHolds()
	if err != nil {
		return fmt.Errorf("get unreleased holds: %w", err)
	}

	// Build a map of pools to participants for quick lookup
	poolParticipants := make(map[string]map[string]bool) // pool -> {endpoint:model} set
	for _, p := range e.roster.Participants {
		pool := poolName(p)
		key := p.Endpoint + ":" + p.Model
		if poolParticipants[pool] == nil {
			poolParticipants[pool] = make(map[string]bool)
		}
		poolParticipants[pool][key] = true
	}

	for _, journalHold := range holds {
		participant := ""
		if a, err := e.store.GetAttempt(journalHold.AttemptID); err == nil {
			participant = a.Participant
		}
		// Convert journal hold to wire hold with group members
		var group []wire.GroupMember
		for _, m := range journalHold.Group {
			group = append(group, wire.GroupMember{
				EndpointName: m.EndpointName,
				Model:        m.Model,
			})
		}

		// Find all pools this hold applies to in the new roster
		var appliedPools []string
		for pool := range poolParticipants {
			// Check if any participant in this pool is in the hold's group
			for _, member := range journalHold.Group {
				key := member.EndpointName + ":" + member.Model
				if poolParticipants[pool][key] {
					appliedPools = append(appliedPools, pool)
					break
				}
			}
		}

		if len(appliedPools) > 0 {
			// Sort for deterministic order
			sort.Strings(appliedPools)
			h := &hold{
				attemptID:    journalHold.AttemptID,
				pool:         journalHold.Pool, // original pool for display
				appliedPools: appliedPools,
				participant:  participant,
				since:        journalHold.HeldSince,
				group:        group,
			}
			e.holds[journalHold.AttemptID] = h
			e.armHold(h)
		} else {
			// This hold is orphaned - store it in orphanedHolds
			h := &hold{
				attemptID:    journalHold.AttemptID,
				pool:         journalHold.Pool,
				appliedPools: []string{},
				participant:  participant,
				since:        journalHold.HeldSince,
				group:        group,
			}
			e.orphanedHolds[journalHold.AttemptID] = h
		}
	}

	return nil
}

// manifestRoster is the part of a run manifest recovery reads.
type manifestRoster struct {
	Roster struct {
		Participants []struct {
			ID       string `json:"id"`
			Endpoint string `json:"endpoint"`
			Model    string `json:"model"`
			Pool     string `json:"pool"`
		} `json:"participants"`
	} `json:"roster"`
}

// extractParticipantInfoFromManifest reads a participant's pool, endpoint and
// model from a run manifest, and the group of its pool: every {endpoint,
// model} in that pool, sorted. An empty pool is the default pool.
func (e *Engine) extractParticipantInfoFromManifest(raw json.RawMessage, participantID string) ([]journal.GroupMember, string, string, string, error) {
	var m manifestRoster
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", "", "", fmt.Errorf("unmarshal manifest: %w", err)
	}
	pool := func(p string) string {
		if p == "" {
			return defaultPool
		}
		return p
	}
	found := false
	var poolID, endpoint, model string
	for _, p := range m.Roster.Participants {
		if p.ID == participantID {
			found, poolID, endpoint, model = true, pool(p.Pool), p.Endpoint, p.Model
			break
		}
	}
	if !found {
		return nil, "", "", "", fmt.Errorf("participant %s not found in manifest", participantID)
	}
	seen := make(map[journal.GroupMember]bool)
	var group []journal.GroupMember
	for _, p := range m.Roster.Participants {
		gm := journal.GroupMember{EndpointName: p.Endpoint, Model: p.Model}
		if pool(p.Pool) == poolID && !seen[gm] {
			seen[gm] = true
			group = append(group, gm)
		}
	}
	sort.Slice(group, func(i, j int) bool {
		if group[i].EndpointName != group[j].EndpointName {
			return group[i].EndpointName < group[j].EndpointName
		}
		return group[i].Model < group[j].Model
	})
	return group, poolID, endpoint, model, nil
}
