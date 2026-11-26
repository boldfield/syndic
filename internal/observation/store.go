package observation

import (
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
	paths map[string]bool // paths already observed
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
	tl := &turnLock{paths: make(map[string]bool)}
	s.turns[turnID] = tl
	return tl
}

// FirstObservation records an observation of path as kind with the given
// content (or empty for absence), or returns an error. If the path is already
// observed in this turn, FirstObservation returns an error. This method
// serializes concurrent first touches so that two threads recording the same
// path simultaneously yield one record and one reader of the live tree.
//
// The returned Record carries the recorded kind and content hash (for file or
// directory) or is empty (for absence).
func (s *Store) FirstObservation(turnID, attemptID, path string, kind byte, content []byte) (Record, error) {
	s.mu.Lock()
	tl := s.ensureTurnLock(turnID)
	s.mu.Unlock()

	tl.mu.Lock()
	defer tl.mu.Unlock()

	if tl.paths[path] {
		return Record{}, fmt.Errorf("path %q already observed in turn %q", path, turnID)
	}

	// Mark this path as observed so concurrent attempts fail.
	tl.paths[path] = true

	// Record the observation in the journal.
	kindStr := string(rune(kind))
	switch kindStr {
	case "f", "a", "d":
	default:
		return Record{}, fmt.Errorf("unknown observation kind %q", kindStr)
	}

	var obs journal.Observation
	err := s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		obs, txErr = tx.RecordObservation(turnID, attemptID, path, kindStr, content)
		return txErr
	})
	if err != nil {
		return Record{}, err
	}

	// Return the record in the observation.Record format.
	return Record{
		Kind:  kind,
		Path:  path,
		Value: obs.ContentSHA256,
	}, nil
}

// RecordObservation records an observation using the journal directly. This is
// a lower-level method that does NOT enforce the concurrent-first-touch
// serialization; it is for use by code that has its own coordination.
//
// Deprecated: use FirstObservation for most tool use cases.
func (s *Store) RecordObservation(turnID, attemptID, path string, kind byte, content []byte) (Record, error) {
	kindStr := string(rune(kind))
	switch kindStr {
	case "f", "a", "d":
	default:
		return Record{}, fmt.Errorf("unknown observation kind %q", kindStr)
	}

	var obs journal.Observation
	err := s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		obs, txErr = tx.RecordObservation(turnID, attemptID, path, kindStr, content)
		return txErr
	})
	if err != nil {
		return Record{}, err
	}

	return Record{
		Kind:  kind,
		Path:  path,
		Value: obs.ContentSHA256,
	}, nil
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
		Kind:  byte(obs.Kind[0]),
		Path:  obs.Path,
		Value: obs.ContentSHA256,
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
			Kind:  byte(o.Kind[0]),
			Path:  o.Path,
			Value: o.ContentSHA256,
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

// ClearTurnLock releases the per-turn lock. This is intended for testing or
// cleanup after a turn is complete. After this call, FirstObservation will
// allow re-recording paths that were previously observed (which will fail in
// the journal layer, but the store lock will not prevent it).
func (s *Store) ClearTurnLock(turnID string) {
	s.mu.Lock()
	delete(s.turns, turnID)
	s.mu.Unlock()
}
