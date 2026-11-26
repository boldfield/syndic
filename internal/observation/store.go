package observation

import (
	"errors"
	"fmt"
	"sync"

	"github.com/boldfield/syndic/internal/journal"
)

// Reader takes one observation from the live tree: the kind it found
// (journal.ObservationFile, ObservationAbsent or ObservationListing) and its
// content (the file's bytes, or the canonical entry list for a listing; nil
// for an absence). It runs under the turn's lock but outside any journal
// transaction, so it may use the journal.
type Reader func() (kind string, content []byte, err error)

// Store records a turn's first observation of each path (design v5 delta,
// section 6). The journal is the source of truth; Store only serializes
// first touches per turn.
type Store struct {
	j *journal.Store

	mu    sync.Mutex
	turns map[string]*turnLock
}

// turnLock is reference-counted so it exists only while a caller holds or
// waits for it.
type turnLock struct {
	mu   sync.Mutex
	refs int
}

// NewStore returns a Store recording into j.
func NewStore(j *journal.Store) *Store {
	return &Store{j: j, turns: map[string]*turnLock{}}
}

func (s *Store) lock(turnID string) func() {
	s.mu.Lock()
	tl := s.turns[turnID]
	if tl == nil {
		tl = &turnLock{}
		s.turns[turnID] = tl
	}
	tl.refs++
	s.mu.Unlock()
	tl.mu.Lock()
	return func() {
		tl.mu.Unlock()
		s.mu.Lock()
		if tl.refs--; tl.refs == 0 {
			delete(s.turns, turnID)
		}
		s.mu.Unlock()
	}
}

// Observe returns the turn's observation of path. If the journal already has
// one it is returned without reading the live tree. Otherwise read is called
// once, under the turn's lock, and its result is recorded bound to attemptID,
// so concurrent first touches yield one record and one live read. A failed
// read or record leaves nothing recorded and the path free to retry.
func (s *Store) Observe(turnID, attemptID, path string, read Reader) (journal.Observation, error) {
	defer s.lock(turnID)()

	if o, err := s.lookup(turnID, path); err == nil {
		return o, nil
	} else if !errors.Is(err, journal.ErrNotObserved) {
		return journal.Observation{}, err
	}

	kind, content, err := read()
	if err != nil {
		return journal.Observation{}, err
	}

	var o journal.Observation
	err = s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		o, txErr = tx.RecordObservation(turnID, attemptID, path, kind, content)
		return txErr
	})
	if errors.Is(err, journal.ErrAlreadyObserved) {
		// Another Store on the same journal recorded it first.
		return s.lookup(turnID, path)
	}
	if err != nil {
		return journal.Observation{}, err
	}
	return o, nil
}

func (s *Store) lookup(turnID, path string) (o journal.Observation, err error) {
	err = s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		o, txErr = tx.ObservationAt(turnID, path)
		return txErr
	})
	if err != nil {
		return journal.Observation{}, fmt.Errorf("observe %q: %w", path, err)
	}
	return o, nil
}
