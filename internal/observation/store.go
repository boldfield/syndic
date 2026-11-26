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
}

// lockKey scopes a turn lock to one journal, so every Store over the same
// journal serializes against the others.
type lockKey struct {
	j    *journal.Store
	turn string
}

// turnLocks is shared by all Stores: a second Store over the same journal
// must not bypass the per-turn serialization.
var turnLocks = struct {
	mu sync.Mutex
	m  map[lockKey]*turnLock
}{m: map[lockKey]*turnLock{}}

// turnLock is reference-counted so it exists only while a caller holds or
// waits for it.
type turnLock struct {
	mu   sync.Mutex
	refs int
}

// NewStore returns a Store recording into j.
func NewStore(j *journal.Store) *Store {
	return &Store{j: j}
}

func (s *Store) lock(turnID string) func() {
	key := lockKey{s.j, turnID}
	turnLocks.mu.Lock()
	tl := turnLocks.m[key]
	if tl == nil {
		tl = &turnLock{}
		turnLocks.m[key] = tl
	}
	tl.refs++
	turnLocks.mu.Unlock()
	tl.mu.Lock()
	return func() {
		tl.mu.Unlock()
		turnLocks.mu.Lock()
		if tl.refs--; tl.refs == 0 {
			delete(turnLocks.m, key)
		}
		turnLocks.mu.Unlock()
	}
}

// Observe returns the turn's observation of path. If the journal already has
// one it is returned without reading the live tree. Otherwise read is called
// once, under the turn's lock, and its result is recorded bound to attemptID,
// so concurrent first touches yield one record and one live read. A failed
// read or record leaves nothing recorded and the path free to retry.
//
// For directory listings, Observe reconciles prior absences and known files,
// and checks for conflicts with prior directory observations.
// For files or absences, Observe checks if the parent directory has been listed,
// and if so, whether the name is in the listing. If the name is not listed,
// an absence is recorded without calling the reader. If the name is listed,
// conflicts with prior type observations are checked.
func (s *Store) Observe(turnID, attemptID, path string, read Reader) (journal.Observation, error) {
	defer s.lock(turnID)()

	if o, err := s.lookup(turnID, path); err == nil {
		return o, nil
	} else if !errors.Is(err, journal.ErrNotObserved) {
		return journal.Observation{}, err
	}

	parent, name := parentAndName(path)

	// Before reading the live tree, check if the parent directory has been
	// listed. If it has and this name is not in the listing, synthesize an
	// absence without calling the reader.
	var listedNames map[string]bool
	var listedTypes map[string]string
	err := s.j.Tx(func(tx *journal.Tx) error {
		obs, err := tx.ObservationAt(turnID, parent)
		if errors.Is(err, journal.ErrNotObserved) {
			return nil // Parent not yet listed, proceed normally
		}
		if err != nil {
			return err
		}
		if obs.Kind != journal.ObservationListing {
			return nil // Parent not a directory, proceed normally
		}
		// Parent is listed; parse the entries.
		content, err := tx.Content(obs.ContentSHA256)
		if err != nil {
			return err
		}
		entries, err := ParseEntries(content)
		if err != nil {
			return err
		}
		listedNames = make(map[string]bool)
		listedTypes = make(map[string]string)
		for _, e := range entries {
			listedNames[e.Name] = true
			listedTypes[e.Name] = e.Type
		}
		return nil
	})
	if err != nil {
		return journal.Observation{}, err
	}

	// If parent has been listed and this name is not in it, synthesize an absence.
	if listedNames != nil && !listedNames[name] {
		// Record absence without calling the reader.
		var o journal.Observation
		err = s.j.Tx(func(tx *journal.Tx) error {
			var txErr error
			o, txErr = tx.RecordObservation(turnID, attemptID, path, journal.ObservationAbsent, nil)
			return txErr
		})
		if errors.Is(err, journal.ErrAlreadyObserved) {
			return s.lookup(turnID, path)
		}
		return o, err
	}

	// Call the reader.
	kind, content, err := read()
	if err != nil {
		return journal.Observation{}, err
	}

	// Pre-process the observation before recording it, under the turn lock.
	conflictType := ""
	switch kind {
	case journal.ObservationListing:
		// Check for conflicts with prior directory observations.
		if err := CheckFileConflict(turnID, path, "d", s.j); errors.Is(err, ErrSnapshotConflict) {
			conflictType = extractConflictType(err)
			if conflictType == "" {
				conflictType = "directory_conflict"
			}
		} else if err != nil {
			return journal.Observation{}, err
		}

		if conflictType == "" {
			// No conflict; reconcile the directory listing with prior observations.
			entries, err := ParseEntries(content)
			if err != nil {
				return journal.Observation{}, err
			}
			reconciled, err := ReconcileDirectoryObservation(turnID, path, entries, s.j)
			if err != nil {
				return journal.Observation{}, err
			}
			// Re-encode the reconciled entries.
			content, err = FrameEntries(reconciled)
			if err != nil {
				return journal.Observation{}, err
			}
		}
	case journal.ObservationFile:
		// Check for conflicts with prior directory observations.
		if err := CheckFileConflict(turnID, path, "f", s.j); errors.Is(err, ErrSnapshotConflict) {
			conflictType = extractConflictType(err)
			if conflictType == "" {
				conflictType = "type_changed"
			}
		} else if err != nil {
			return journal.Observation{}, err
		}
	case journal.ObservationAbsent:
		// Check for conflicts with prior directory observations.
		if err := CheckFileConflict(turnID, path, journal.ObservationAbsent, s.j); errors.Is(err, ErrSnapshotConflict) {
			conflictType = extractConflictType(err)
			if conflictType == "" {
				conflictType = "deleted"
			}
		} else if err != nil {
			return journal.Observation{}, err
		}
	}

	// If a conflict was detected, journal it and return the error.
	if conflictType != "" {
		_ = s.j.RecordSnapshotConflict(turnID, attemptID, path, conflictType)
		return journal.Observation{}, fmt.Errorf("%w: %s (%s)", ErrSnapshotConflict, path, conflictType)
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
