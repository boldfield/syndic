package engine

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	StateSoloComplete        = "solo_complete"
	StateIncomplete          = "incomplete"
	StateApproved            = "approved"
)

// Participant states.
const (
	ParticipantIdle     = "idle"
	ParticipantRunning  = "running"
	ParticipantUnprobed = "unprobed"
)

// Attempt phases and message senders.
const (
	phaseAuthor = "author"
	senderUser  = "user"
)

// Terminal reasons.
const (
	reasonSoloOK       = "solo_ok"
	reasonAuthorFailed = "author_failed:"
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

// Cancel is implemented by a later task.
func (e *Engine) Cancel(turnID string) error { return ErrNotImplemented }

// Override is implemented by a later task.
func (e *Engine) Override(turnID, reason string) error { return ErrNotImplemented }

// Release is implemented by a later task.
func (e *Engine) Release(req wire.ReleaseRequest) (wire.ReleaseReply, error) {
	return wire.ReleaseReply{}, ErrNotImplemented
}
