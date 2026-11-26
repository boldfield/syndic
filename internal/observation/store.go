package observation

import (
	"errors"
	"fmt"
	"io/fs"
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

// KindSymlink and KindSpecial are the kinds a Reader returns for a live path
// that is a symbolic link or another special file (FIFO, socket, device).
// They are compared with the recorded entry type ("l" and "o") but never
// recorded: Observe returns ErrOtherEntry instead.
const (
	KindSymlink = "symlink"
	KindSpecial = "special"
)

// ErrOtherEntry is returned by Observe when the live path is a symbolic link
// or special file consistent with the record.
var ErrOtherEntry = errors.New("path is a symbolic link or special file")

// ErrSymlinkOnPath is returned by Observe, recording nothing, when a component
// of the path before the last is a symbolic link, by the record or (returned
// by the Reader) live. The path is refused: it is neither present nor absent.
var ErrSymlinkOnPath = errors.New("path has a symbolic link on it")

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
// The record is one namespace: a path an earlier listing or absence rules out
// is recorded absent without a live read, a live listing is reconciled with
// what is already recorded beneath it, and a live observation that
// contradicts a listed name returns *SnapshotConflictError (matching
// ErrSnapshotConflict) after journaling the conflict, recording nothing.
// Paths are slash-separated, with Root for the workspace root.
func (s *Store) Observe(turnID, attemptID, path string, read Reader) (journal.Observation, error) {
	return s.ObserveWithMode(turnID, attemptID, path, func() (string, []byte, bool, error) {
		kind, content, err := read()
		return kind, content, false, err
	})
}

// ModeReader is a Reader that also reports, for a file, whether it was
// executable for its owner (see OwnerExec) at the moment its content was read.
// The bit is ignored for any other kind.
type ModeReader func() (kind string, content []byte, ownerExec bool, err error)

// OwnerExec reports the owner-execute bit of mode. It is the only bit of a
// file's mode ever recorded: setuid, setgid, sticky and every other bit are
// dropped (design v7 delta, section 5, "Modes").
func OwnerExec(mode fs.FileMode) bool { return mode&0o100 != 0 }

// ObserveWithMode is Observe for a reader that reports the owner-execute bit.
// For a file the bit is recorded in the mode record in the same transaction
// as the content, and the live tree is never asked for it again.
func (s *Store) ObserveWithMode(turnID, attemptID, path string, read ModeReader) (journal.Observation, error) {
	defer s.lock(turnID)()

	if o, err := s.lookup(turnID, path); err == nil {
		return o, nil
	} else if !errors.Is(err, journal.ErrNotObserved) {
		return journal.Observation{}, err
	}

	var ns namespace
	if err := s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		ns, txErr = loadNamespace(tx, turnID, path)
		return txErr
	}); err != nil {
		return journal.Observation{}, err
	}
	if ns.viaSymlink {
		return journal.Observation{}, ErrSymlinkOnPath
	}
	if ns.absent {
		// The record already settles the name as absent; the live tree is not
		// consulted.
		return s.record(turnID, attemptID, path, journal.ObservationAbsent, nil, false)
	}

	kind, content, ownerExec, err := read()
	if err != nil {
		return journal.Observation{}, err
	}
	if ct := ns.conflict(kind); ct != "" {
		c := &SnapshotConflictError{Path: path, Type: ct}
		if err := s.journalConflict(turnID, attemptID, c); err != nil {
			return journal.Observation{}, fmt.Errorf("journal snapshot conflict at %q (%s): %w", c.Path, c.Type, err)
		}
		return journal.Observation{}, c
	}
	if kind == KindSymlink || kind == KindSpecial {
		// A symbolic link or special file matches its recorded type but
		// has no content to record.
		return journal.Observation{}, ErrOtherEntry
	}
	if kind == journal.ObservationListing {
		if content, err = ns.reconcileListing(content); err != nil {
			return journal.Observation{}, err
		}
	}
	return s.record(turnID, attemptID, path, kind, content, ownerExec)
}

// OwnerExec returns the owner-execute bit recorded with turnID's observation
// of path. A path with no mode record, including one observed before modes
// were recorded, reads as not executable.
func (s *Store) OwnerExec(turnID, path string) (exec bool, err error) {
	err = s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		exec, txErr = tx.ObservationOwnerExec(turnID, path)
		return txErr
	})
	return exec, err
}

func (s *Store) record(turnID, attemptID, path, kind string, content []byte, ownerExec bool) (journal.Observation, error) {
	var o journal.Observation
	err := s.j.Tx(func(tx *journal.Tx) error {
		var txErr error
		if o, txErr = tx.RecordObservation(turnID, attemptID, path, kind, content); txErr != nil {
			return txErr
		}
		if kind == journal.ObservationFile {
			return tx.RecordObservationMode(turnID, path, ownerExec)
		}
		return nil
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
