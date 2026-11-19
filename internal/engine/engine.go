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
)

// Participant states.
const (
	ParticipantIdle       = "idle"
	ParticipantQueued     = "queued"
	ParticipantRunning    = "running"
	ParticipantUnprobed   = "unprobed"
	ParticipantCancelling = "cancelling"
	ParticipantUnknown    = "unknown"
)

// Attempt phases and message senders.
const (
	phaseAuthor  = "author"
	phaseReview  = "review"
	senderUser   = "user"
	senderEngine = "engine"
)

// Terminal reasons.
const (
	reasonSoloOK       = "solo_ok"
	reasonAuthorFailed = "author_failed:"
	reasonAllApproved  = "all_approved"

	reasonDeadlineTurn    = "deadline:turn"
	reasonDeadlineAttempt = "deadline:attempt:"
	reasonCancelled       = "cancelled"
	reasonEngineStop      = "engine_stop"
	reasonEngineRestart   = "engine_restart"
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
	newAdapter func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error)

	// payloadHook lets tests replace an event payload (for example with one
	// that cannot be marshalled) to make a transaction fail after its row
	// writes. It is set before any request and never changed afterwards.
	payloadHook func(kind string, payload any) any

	// mu owns everything below.
	mu           sync.Mutex
	started      bool
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
	exceeded     map[string]bool // participants whose estimate was exceeded this run
	cancelling   map[string]bool // participants in cancelling state
	unknown      map[string]bool // participants in unknown state (hold unreleased)
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
		newAdapter: o.NewAdapter,
		running:    make(map[string]bool),
		inflight:   make(map[string]int),
		queued:     make(map[string]bool),
		pools:      make(map[string]*pool),
		cancelling: make(map[string]bool),
		unknown:    make(map[string]bool),
	}
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
		return emit("", wire.EventKindEngineStarted, wire.EvEngineStarted{
			RunID:             runID,
			ManifestHash:      manifestHash,
			RosterHash:        built.RosterHash,
			UncertainCapacity: []string{},
			OrphanedHolds:     []wire.Hold{},
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

// Cancel ends a turn with state cancelled, idempotent.
func (e *Engine) Cancel(turnID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.started {
		return errors.New("engine: not started")
	}

	t := e.open
	if t == nil || t.id != turnID {
		// Turn is not open, check if it's already terminal
		turn, err := e.store.GetTurn(turnID)
		if err != nil {
			return &wire.Error{Code: wire.ErrorCodeNotFound}
		}
		// If already in a terminal state, check if it's already cancelled
		if turn.TerminalState == StateCancelled {
			// Already cancelled, idempotent
			return nil
		}
		if turn.TerminalState != "" {
			// Terminal in another state
			return &wire.Error{Code: wire.ErrorCodeNotOpen}
		}
		// Need to handle closed turn - this is not the open one
		return &wire.Error{Code: wire.ErrorCodeNotOpen}
	}

	// Cancel the open turn
	inflight := e.getInflightAttempts(t)
	err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
		return e.endTurnTerminalPath(tx, emit, t, StateCancelled, reasonCancelled, true, inflight)
	})
	if err != nil {
		return err
	}

	e.open = nil
	e.turnEnded(t)

	// Release lock before calling cancelInflightAttempts to avoid deadlock.
	// The defer at the top of the function will re-lock and then unlock.
	e.mu.Unlock()
	defer e.mu.Lock()

	e.cancelInflightAttempts(inflight)
	return nil
}

// Override is implemented by a later task.
func (e *Engine) Override(turnID, reason string) error { return ErrNotImplemented }

// Release releases a capacity hold.
func (e *Engine) Release(req wire.ReleaseRequest) (wire.ReleaseReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.started {
		return wire.ReleaseReply{}, errors.New("engine: not started")
	}

	if req.Reason == "" {
		return wire.ReleaseReply{}, &wire.Error{Code: wire.ErrorCodeReasonRequired}
	}

	// Find the hold(s) for this pool
	holds, err := e.store.UnreleasedHolds()
	if err != nil {
		return wire.ReleaseReply{}, err
	}

	var targetHolds []journal.Hold
	for _, h := range holds {
		if h.Pool == req.Pool {
			if req.AttemptID == nil {
				targetHolds = append(targetHolds, h)
			} else if h.AttemptID == *req.AttemptID {
				targetHolds = append(targetHolds, h)
			}
		}
	}

	// Check for errors
	if len(targetHolds) == 0 {
		if req.AttemptID != nil {
			return wire.ReleaseReply{}, &wire.Error{Code: wire.ErrorCodeNoSuchHold, Detail: strPtr(req.Pool)}
		}
		return wire.ReleaseReply{}, &wire.Error{Code: wire.ErrorCodeNoHold, Detail: strPtr(req.Pool)}
	}

	if req.AttemptID == nil && len(targetHolds) > 1 {
		// Ambiguous - multiple holds without attempt_id specified
		ambiguousHolds := make([]string, len(targetHolds))
		for i, h := range targetHolds {
			ambiguousHolds[i] = h.AttemptID
		}
		return wire.ReleaseReply{}, &wire.Error{
			Code:   wire.ErrorCodeAmbiguous,
			Detail: strPtr(req.Pool),
		}
	}

	// Release each hold
	var released []string
	for _, h := range targetHolds {
		err := e.tx(func(tx *journal.Tx, emit hub.Emit) error {
			if err := tx.ReleaseHold(h.AttemptID, "operator", req.Reason); err != nil {
				return err
			}
			return emit("", wire.EventKindCapacity, wire.EvCapacity{
				Pool:          h.Pool,
				HeldByAttempt: h.AttemptID,
				State:         "released",
				How:           strPtr("operator"),
				Reason:        strPtr(req.Reason),
			})
		})
		if err != nil {
			return wire.ReleaseReply{}, err
		}
		released = append(released, h.AttemptID)
	}

	sort.Strings(released)
	return wire.ReleaseReply{Released: released}, nil
}
