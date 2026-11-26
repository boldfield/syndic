package observation

import (
	"errors"
	"fmt"
	"sync"

	"github.com/boldfield/syndic/internal/journal"
)

// Store provides a focused observation API for tools, managing turn-serialized
// first observation with immutable file/absence records, versioning, and
// content storage. It enforces that the first tool to observe a path records
// the observation, and all subsequent observations are served from that record.
type Store struct {
	j *journal.Store
	// mu guards the per-turn locks in turns.
	mu    sync.Mutex
	turns map[string]*turnLock
}

// turnLock serializes observation recording for one turn so that concurrent
// first touches of the same path yield one record and one reader of the live
// tree (design v5 delta section 6).
type turnLock struct {
	mu    sync.Mutex
	paths map[string]*Record // paths already observed with their records
}

// NewStore wraps the journal store to provide turn-serialized observation
// recording for tools.
func NewStore(j *journal.Store) *Store {
	return &Store{
		j:     j,
		turns: make(map[string]*turnLock),
	}
}

// ensureTurnLock returns the turn lock for the given turn, creating it if
// necessary. The caller must hold the Store's mu.
func (s *Store) ensureTurnLock(turnID string) *turnLock {
	if tl, ok := s.turns[turnID]; ok {
		return tl
	}
	tl := &turnLock{paths: make(map[string]*Record)}
	s.turns[turnID] = tl
	return tl
}

// Observe attempts to observe a path, reading from the live tree if not yet
// observed and returning the existing recorded observation for repeated touches.
// The reader callback returns (kind, content, error) and is invoked at most once,
// inside the per-turn lock, if the path has not been previously observed.
//
// Two concurrent first touches of the same path yield one record and one
// invocation of the reader (design v5 delta section 6).
// Every later observation is served from the journal record, even from a fresh
// Store instance on the same journal.
func (s *Store) Observe(turnID, attemptID, path string, reader func() (kind byte, content []byte, err error)) (Record, error) {
	s.mu.Lock()
	tl := s.ensureTurnLock(turnID)
	s.mu.Unlock()

	tl.mu.Lock()
	defer tl.mu.Unlock()

	// First check the journal record under the lock. If the path has been
	// observed before, return the recorded observation (even by a different
	// Store instance or in a different round).
	var obs journal.Observation
	err := s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		obs, txErr = tx.ObservationAt(turnID, path)
		if txErr == nil {
			return nil
		}
		if !errors.Is(txErr, journal.ErrNotObserved) {
			return txErr
		}

		// Not yet observed in the journal. Call the reader under the lock
		// so concurrent first touches serialize and only one reader is invoked.
		kind, content, readerErr := reader()
		if readerErr != nil {
			return readerErr
		}

		// Validate kind.
		kindStr := string(rune(kind))
		switch kindStr {
		case "f", "a", "d":
		default:
			return fmt.Errorf("unknown observation kind %q", kindStr)
		}

		// Record the observation in the journal.
		obs, txErr = tx.RecordObservation(turnID, attemptID, path, kindStr, content)
		return txErr
	})
	if err != nil {
		return Record{}, err
	}

	// Convert journal observation to Record.
	rec := &Record{
		Kind:            byte(obs.Kind[0]),
		Path:            obs.Path,
		Value:           obs.ContentSHA256,
		AttemptID:       obs.AttemptID,
		SnapshotVersion: obs.SnapshotVersion,
	}

	// Update the cache for fast repeated access within this Store instance.
	tl.paths[path] = rec

	return *rec, nil
}

// ObservationAt returns the recorded observation for the given path in the
// turn, or an error if the path has not been observed.
func (s *Store) ObservationAt(turnID, path string) (Record, error) {
	var obs journal.Observation
	err := s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		obs, txErr = tx.ObservationAt(turnID, path)
		return txErr
	})
	if err != nil {
		return Record{}, err
	}

	return Record{
		Kind:            byte(obs.Kind[0]),
		Path:            obs.Path,
		Value:           obs.ContentSHA256,
		AttemptID:       obs.AttemptID,
		SnapshotVersion: obs.SnapshotVersion,
	}, nil
}

// Observations returns all observations in the turn's snapshot record, sorted
// bytewise by path, in the order the canonical form uses.
func (s *Store) Observations(turnID string) ([]Record, error) {
	var obs []journal.Observation
	err := s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		obs, txErr = tx.Observations(turnID)
		return txErr
	})
	if err != nil {
		return nil, err
	}

	records := make([]Record, len(obs))
	for i, o := range obs {
		records[i] = Record{
			Kind:            byte(o.Kind[0]),
			Path:            o.Path,
			Value:           o.ContentSHA256,
			AttemptID:       o.AttemptID,
			SnapshotVersion: o.SnapshotVersion,
		}
	}
	return records, nil
}

// SnapshotVersion returns the turn's current snapshot version. It increments
// with each new observation recorded.
func (s *Store) SnapshotVersion(turnID string) (int, error) {
	var version int
	err := s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		version, txErr = tx.SnapshotVersion(turnID)
		return txErr
	})
	return version, err
}
