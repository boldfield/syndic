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
// The reader callback is invoked at most once, inside the per-turn lock,
// if the path has not been previously observed.
//
// Two concurrent first touches of the same path yield one record and one
// invocation of the reader (design v5 delta section 6).
func (s *Store) Observe(turnID, attemptID, path string, kind byte, reader func() ([]byte, error)) (Record, error) {
	kindStr := string(rune(kind))
	switch kindStr {
	case "f", "a", "d":
	default:
		return Record{}, fmt.Errorf("unknown observation kind %q", kindStr)
	}

	s.mu.Lock()
	tl := s.ensureTurnLock(turnID)
	s.mu.Unlock()

	tl.mu.Lock()
	defer tl.mu.Unlock()

	// Check if already observed; return the recorded observation.
	if rec, ok := tl.paths[path]; ok {
		return *rec, nil
	}

	// Read the live tree under the lock so concurrent first touches
	// serialize and only one reader is invoked.
	content, err := reader()
	if err != nil {
		return Record{}, err
	}

	// Record the observation in the journal.
	var obs journal.Observation
	err = s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		obs, txErr = tx.RecordObservation(turnID, attemptID, path, kindStr, content)
		return txErr
	})
	if err != nil {
		return Record{}, err
	}

	// Mark this path as observed only after successful journal write.
	rec := &Record{
		Kind:            kind,
		Path:            path,
		Value:           obs.ContentSHA256,
		AttemptID:       attemptID,
		SnapshotVersion: obs.SnapshotVersion,
	}
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
