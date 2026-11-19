package engine

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	reasonOverride     = "override"

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
	mu           sync.Mutex
	started      bool
	cancelers    int        // goroutines calling Adapter.Cancel; Stop waits for none
	cancelersOut *sync.Cond // signalled on e.mu when cancelers drops to zero
	rosterAbs    string
	roster       *config.Roster
	adapters     map[string]transport.Adapter
	built        manifest.Built
	runID        string
	manifestHash string
	runFirstSeq  int64
	startedAt    time.Time
	open         *openTurn
	running      map[string]bool
	inflight     map[string]int // dispatches holding a pool slot, per participant
	queued       map[string]bool
	pools        map[string]*pool
	exceeded     map[string]bool   // participants whose estimate was exceeded this run
	holds        map[string]*hold  // unreleased capacity holds, by attempt id
	calling      map[string]bool   // attempts whose Complete call has not returned
	cancelled    map[string]bool   // calling attempts the engine itself cancelled
	cancelling   map[string]int    // Adapter.Cancel calls pending, per participant
	pstate       map[string]string // cancelling or unknown last reported, per participant
	failed       map[string]string // failure state of the last dispatch, per participant
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
		store:      o.Store,
		hub:        o.Hub,
		rosterPath: o.RosterPath,
		version:    o.EngineVersion,
		clock:      o.Clock,
		rand:       o.Rand,
		retryDelay: retryDelay,
		noDelivery: deliveryOff,
		newAdapter: o.NewAdapter,
		running:    make(map[string]bool),
		inflight:   make(map[string]int),
		queued:     make(map[string]bool),
		pools:      make(map[string]*pool),
		holds:      make(map[string]*hold),
		calling:    make(map[string]bool),
		cancelled:  make(map[string]bool),
		cancelling: make(map[string]int),
		pstate:     make(map[string]string),
		failed:     make(map[string]string),
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

		for _, attemptID := range pendingAttempts {
			// Get the turn and run for this attempt
			turnID, runID, err := tx.GetAttemptTurnAndRun(attemptID)
			if err != nil {
				return fmt.Errorf("get turn for attempt %s: %w", attemptID, err)
			}

			// Get the run manifest to extract participant info
			run, err := tx.GetRun(runID)
			if err != nil {
				return fmt.Errorf("get run %s: %w", runID, err)
			}

			// Check if attempt already has a hold
			existingHolds, err := tx.UnreleasedHolds()
			if err != nil {
				return fmt.Errorf("get existing holds: %w", err)
			}

			hasHold := false
			for _, h := range existingHolds {
				if h.AttemptID == attemptID {
					hasHold = true
					break
				}
			}

			if !hasHold {
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

	e.roster = roster
	e.rosterAbs = path
	e.adapters = adapters
	e.built = built
	e.runID = runID
	e.manifestHash = manifestHash
	e.runFirstSeq = firstSeq
	e.startedAt = startedAt
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

// Override marks a turn as overridden with the given reason.
func (e *Engine) Override(turnID, reason string) error {
	if reason == "" {
		return fmt.Errorf("engine: reason_required")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.started {
		return errors.New("engine: not started")
	}

	// Perform the override in a transaction
	return e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		// Check that turn exists and is incomplete with a candidate
		turnRow, err := tx.GetTurn(turnID)
		if err != nil {
			return fmt.Errorf("not_found")
		}

		if turnRow.TerminalState != StateIncomplete {
			return fmt.Errorf("not_incomplete")
		}

		if turnRow.CandidateID == "" {
			return fmt.Errorf("not_incomplete")
		}

		// Bump epoch
		if _, err := tx.BumpEpoch(turnID); err != nil {
			return fmt.Errorf("bump epoch: %w", err)
		}

		// Override the turn and emit the override event
		if err := tx.OverrideTurn(turnID, reason); err != nil {
			return fmt.Errorf("override turn: %w", err)
		}

		// Get the open blocks and emit block_state events for each
		blocks, err := tx.OpenBlocks(turnID)
		if err != nil {
			return fmt.Errorf("get open blocks: %w", err)
		}

		for _, block := range blocks {
			if err := emit(turnID, wire.EventKindBlockState, wire.EvBlockState{
				BlockID: block.ID,
				State:   "overridden",
			}); err != nil {
				return fmt.Errorf("emit block_state: %w", err)
			}
		}

		// Emit turn_state event
		if err := emit(turnID, wire.EventKindTurnState, wire.EvTurnState{
			TurnID: turnID,
			State:  StateOverridden,
			Reason: &reason,
		}); err != nil {
			return fmt.Errorf("emit turn_state: %w", err)
		}

		return nil
	})
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
		key := p.Endpoint + ":" + p.Model
		if poolParticipants[p.Pool] == nil {
			poolParticipants[p.Pool] = make(map[string]bool)
		}
		poolParticipants[p.Pool][key] = true
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

// extractParticipantInfoFromManifest extracts participant info from the manifest JSON.
// Returns group, pool, endpoint_name, model, error.
func (e *Engine) extractParticipantInfoFromManifest(manifest json.RawMessage, participantID string) ([]journal.GroupMember, string, string, string, error) {
	var manifestData map[string]interface{}
	if err := json.Unmarshal(manifest, &manifestData); err != nil {
		return nil, "", "", "", fmt.Errorf("unmarshal manifest: %w", err)
	}

	roster, ok := manifestData["roster"].(map[string]interface{})
	if !ok {
		return nil, "", "", "", fmt.Errorf("roster not found in manifest")
	}

	participants, ok := roster["participants"].([]interface{})
	if !ok {
		return nil, "", "", "", fmt.Errorf("participants not found in roster")
	}

	// Find the participant by ID and extract endpoint_name and model
	var targetParticipant map[string]interface{}
	var poolName, endpointName, model string

	for _, p := range participants {
		pMap, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if pMap["id"] == participantID {
			targetParticipant = pMap
			if ep, ok := pMap["endpoint"].(string); ok {
				endpointName = ep
			}
			if m, ok := pMap["model"].(string); ok {
				model = m
			}
			if pool, ok := pMap["pool"].(string); ok {
				poolName = pool
			}
			break
		}
	}

	if targetParticipant == nil {
		return nil, "", "", "", fmt.Errorf("participant %s not found in manifest", participantID)
	}

	// Now build the group by finding all participants in the same pool
	var group []journal.GroupMember
	groupSet := make(map[string]bool) // For deduplication

	for _, p := range participants {
		pMap, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if pool, ok := pMap["pool"].(string); ok && pool == poolName {
			if ep, ok := pMap["endpoint"].(string); ok {
				if m, ok := pMap["model"].(string); ok {
					key := ep + ":" + m
					if !groupSet[key] {
						group = append(group, journal.GroupMember{
							EndpointName: ep,
							Model:        m,
						})
						groupSet[key] = true
					}
				}
			}
		}
	}

	// Sort the group by endpoint name then model (as required by OpenHold)
	sort.Slice(group, func(i, j int) bool {
		if group[i].EndpointName != group[j].EndpointName {
			return group[i].EndpointName < group[j].EndpointName
		}
		return group[i].Model < group[j].Model
	})

	return group, poolName, endpointName, model, nil
}
