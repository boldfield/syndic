package enginetest

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
)

// Re-export ErrNotImplemented from engine for testing
var ErrNotImplemented = engine.ErrNotImplemented

// Harness is a test helper for the engine.
type Harness struct {
	t        *testing.T
	Engine   *engine.Engine
	Store    *journal.Store
	Hub      *hub.Hub
	Adapters map[string]*fake.Adapter
	Clock    *FakeClock
}

// FakeClock is a controllable clock for testing.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc registers a function to be called after a duration.
func (c *FakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	// For testing, just return a no-op stopper
	return func() bool { return true }
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
		os.Remove(f.Name())
		return nil, err
	}
	rosterPath := f.Name()

	// Create temp journal database
	journalFile, err := os.CreateTemp("", "journal-*.db")
	if err != nil {
		os.Remove(rosterPath)
		return nil, err
	}
	journalFile.Close()
	journalPath := journalFile.Name()

	// Initialize journal database
	journalDB, err := journal.Open(journalPath)
	if err != nil {
		os.Remove(rosterPath)
		os.Remove(journalPath)
		return nil, err
	}

	store := journal.NewStore(journalDB)

	// Load roster to get participant information
	roster, err := config.Load(rosterPath)
	if err != nil {
		os.Remove(rosterPath)
		os.Remove(journalPath)
		return nil, err
	}
	if err := roster.Validate(); err != nil {
		os.Remove(rosterPath)
		os.Remove(journalPath)
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
		os.Remove(rosterPath)
		os.Remove(journalPath)
		return nil, err
	}

	h := &Harness{
		t:        t,
		Engine:   eng,
		Store:    store,
		Hub:      eventHub,
		Adapters: adapters,
		Clock:    clock,
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

// Events returns all events seen so far, decoded.
func (h *Harness) Events() []journal.Event {
	events, _ := h.Store.EventsSince(0, 100000)
	return events
}

// Cleanup cleans up temporary resources.
func (h *Harness) Cleanup() error {
	// Close database connections
	if h.Store != nil {
		// Store might need cleanup if it has open resources
	}
	return nil
}
