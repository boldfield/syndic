package enginetest

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/evidence"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// Re-export ErrNotImplemented from engine for testing
var ErrNotImplemented = engine.ErrNotImplemented

// Harness is a test helper for the engine.
type Harness struct {
	t           *testing.T
	Engine      *engine.Engine
	Store       *journal.Store
	Hub         *hub.Hub
	Adapters    map[string]*fake.Adapter
	Clock       *FakeClock
	rosterPath  string
	journalPath string
	db          *journal.DB
}

// FakeClock is a controllable clock for testing.
type FakeClock struct {
	mu       sync.Mutex
	now      time.Time
	timers   []fakeTimer
	timersMu sync.Mutex
}

type fakeTimer struct {
	fireAt  time.Time
	fn      func()
	stopped bool
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc registers a function to be called after a duration.
func (c *FakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.timersMu.Lock()
	now := c.Now()
	timer := fakeTimer{fireAt: now.Add(d), fn: f}
	idx := len(c.timers)
	c.timers = append(c.timers, timer)
	c.timersMu.Unlock()

	return func() bool {
		c.timersMu.Lock()
		defer c.timersMu.Unlock()
		if idx >= len(c.timers) || c.timers[idx].stopped {
			return false
		}
		c.timers[idx].stopped = true
		return true
	}
}

// New creates a new test harness.
func New(t *testing.T, rosterTOML string, scripts map[string][]*fake.Step) (*Harness, error) {
	// Write roster to temp file
	f, err := os.CreateTemp("", "roster-*.toml")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if _, err := f.WriteString(rosterTOML); err != nil {
		_ = os.Remove(f.Name())
		return nil, err
	}
	rosterPath := f.Name()
	t.Cleanup(func() { _ = os.Remove(rosterPath) })

	// Create temp journal database
	journalFile, err := os.CreateTemp("", "journal-*.db")
	if err != nil {
		return nil, err
	}
	_ = journalFile.Close()
	journalPath := journalFile.Name()
	t.Cleanup(func() { _ = os.Remove(journalPath) })

	// Initialize journal database
	journalDB, err := journal.Open(journalPath)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = journalDB.Close() })

	store := journal.NewStore(journalDB)

	// Load roster to get participant information
	roster, err := config.Load(rosterPath)
	if err != nil {
		return nil, err
	}
	if err := roster.Validate(); err != nil {
		return nil, err
	}

	// Create fake adapters for each participant
	adapters := make(map[string]*fake.Adapter)
	for _, p := range roster.Participants {
		// Get script for this participant or use empty list
		scriptPtrs := scripts[p.ID]
		steps := make([]fake.Step, 0)
		if scriptPtrs != nil {
			for _, step := range scriptPtrs {
				if step != nil {
					steps = append(steps, *step)
				}
			}
		}

		// Create fake adapter with options
		adapter := fake.New(steps, fake.Options{
			DefaultIdentity:      transport.NewUnknownIdentity(),
			DefaultReportedModel: "test-model",
		})
		adapters[p.ID] = adapter
	}

	// Seed probe records for each participant
	for _, p := range roster.Participants {
		endpoint := findEndpoint(roster, p.Endpoint)
		if endpoint == nil {
			continue
		}

		// Create a minimal export
		exp := evidence.Export{
			Redacted:                 true,
			RedactionRule:            "evidence/v1",
			EndpointName:             endpoint.Name,
			DeclaredLocation:         endpoint.DeclaredLocation,
			DeclaredExecution:        endpoint.DeclaredExecution,
			DeclaredBilling:          endpoint.DeclaredBilling,
			NetworkDependency:        endpoint.NetworkDependency,
			Model:                    p.Model,
			AuthMode:                 "api_key",
			Identity:                 transport.NewUnknownIdentity(),
			ReportedModel:            "test-model",
			ContextBound:             ptrInt(32768),
			OutputBound:              ptrInt(4096),
			EligibleRequiredReviewer: transport.TriYes,
			FinishReasons: map[string]transport.CompletionStatus{
				"stop": transport.StatusComplete,
			},
			AdapterVersion: "test-adapter-1.0",
			ObservedAt:     time.Now(),
		}

		// Create probe manifest
		probeManifest, err := evidence.NewProbeManifest(exp, *endpoint, p.Model, "test-adapter-1.0", transport.Calibration{
			Available:            true,
			BytesPerToken:        2.5,
			ObservedPromptTokens: 100,
			BaselinePromptTokens: 10,
		})
		if err != nil {
			return nil, err
		}

		// Marshal manifest to JSON
		manifestJSON, err := json.Marshal(probeManifest)
		if err != nil {
			return nil, err
		}

		// Create probe run in journal
		_, _, err = store.CreateRun(json.RawMessage(manifestJSON))
		if err != nil {
			return nil, err
		}
	}

	// Create clock
	clock := &FakeClock{now: time.Now()}

	// Create hub
	eventHub := hub.New(store, 1024)

	// Create engine
	eng, err := engine.New(engine.Options{
		Store:         store,
		Hub:           eventHub,
		RosterPath:    rosterPath,
		EngineVersion: "1.0.0",
		Clock:         clock,
		Rand:          bytes.NewReader([]byte("test-rand-bytes-16")),
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			adapter := adapters[p.ID]
			return adapter, "test-adapter-1.0", nil
		},
	})
	if err != nil {
		return nil, err
	}

	h := &Harness{
		t:           t,
		Engine:      eng,
		Store:       store,
		Hub:         eventHub,
		Adapters:    adapters,
		Clock:       clock,
		rosterPath:  rosterPath,
		journalPath: journalPath,
		db:          journalDB,
	}

	// Start engine
	if err := h.Engine.Start(); err != nil {
		h.Cleanup()
		return nil, err
	}

	return h, nil
}

// WaitTurnState waits for a turn to reach the given state.
func (h *Harness) WaitTurnState(turnID, state string) error {
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return errors.New("timeout waiting for turn state")
		case <-ticker.C:
			turn, err := h.Store.GetTurn(turnID)
			if err == nil && turn != nil && turn.TerminalState == state {
				return nil
			}
		}
	}
}

// Events returns all events seen so far.
func (h *Harness) Events() []journal.Event {
	events, _ := h.Store.EventsSince(0, 100000)
	return events
}

// Cleanup cleans up temporary resources.
func (h *Harness) Cleanup() error {
	return nil
}

func findEndpoint(roster *config.Roster, name string) *config.EndpointProfile {
	for i := range roster.Endpoints {
		if roster.Endpoints[i].Name == name {
			return &roster.Endpoints[i]
		}
	}
	return nil
}

func ptrInt(v int) *int {
	return &v
}
