package mcp

// This file owns the state directories of local servers (v9 delta, section 6,
// "State directories"). With state = "fresh" every instance gets an empty
// directory that is removed when it ends. With state = "persistent" the
// directory is kept between instances under a key, and an instance holds an
// exclusive lock on it for its life: one that cannot take the lock is not
// started. A directory under another key is kept and never reused under this
// one. Marks on directories are another task's.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// State settings of a server (mcp.<server>.state).
const (
	StateFresh      = "fresh"
	StatePersistent = "persistent"
)

var (
	// ErrStateLocked is wrapped by the error of an instance that could not
	// take the exclusive lock on its persistent directory; it is not started.
	ErrStateLocked = errors.New("mcp: state directory is locked")
	// ErrStateKey is wrapped by the error of a request that has no valid key.
	ErrStateKey = errors.New("mcp: invalid state directory request")
)

// StateRequest says which state directory an instance needs.
type StateRequest struct {
	Server string
	// Kind is TurnInstance, ApplyInstance, ListingInstance or RunInstance.
	Kind string
	// State is StateFresh (or empty) or StatePersistent.
	State string
	// Participant is the turn instance's participant, or the author whose
	// turn staged the effects for an apply instance. Unused for the run
	// instance.
	Participant string
	// WorkspaceHash is the hash of the conversation's workspace root. Unused
	// for the run instance.
	WorkspaceHash string
}

// StateKey is the key a persistent directory is kept under: for a turn
// instance the server, the participant and the workspace hash; for an apply
// instance the server, the staging author and the workspace hash; for the run
// instance the server alone. The kind is not part of the key: an apply
// instance shares the directory of its author's turn instance (the lock keeps
// them apart in time). Different keys never collide: each field is
// length-prefixed, and the run key has fewer fields. A listing instance has no key.
func StateKey(req StateRequest) (string, error) {
	if req.Server == "" {
		return "", fmt.Errorf("%w: no server", ErrStateKey)
	}
	var fields []string
	switch req.Kind {
	case TurnInstance, ApplyInstance:
		if req.Participant == "" || req.WorkspaceHash == "" {
			return "", fmt.Errorf("%w: a %s instance needs a participant and a workspace hash", ErrStateKey, req.Kind)
		}
		fields = []string{req.Server, req.Participant, req.WorkspaceHash}
	case RunInstance:
		fields = []string{req.Server}
	case ListingInstance:
		return "", fmt.Errorf("%w: a listing instance never uses a persistent directory", ErrStateKey)
	default:
		return "", fmt.Errorf("%w: unknown instance kind %q", ErrStateKey, req.Kind)
	}
	h := sha256.New()
	for _, f := range fields {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// StateDirs hands out state directories under Root. It is safe for
// concurrent use.
type StateDirs struct {
	// Root holds the fresh directories and the persistent ones; it is
	// created if absent.
	Root string

	mu   sync.Mutex
	held map[string]bool // keys locked by this process
}

// StateDir is the state directory of one instance. Release it when the
// instance has ended.
type StateDir struct {
	// Path is the directory the instance uses as its state directory.
	Path string
	// Key is the persistent key; empty for a fresh directory.
	Key string

	dirs *StateDirs
	lock *os.File
	once sync.Once
	err  error
}

// Acquire returns the state directory for req. A fresh directory is new and
// empty. A persistent one is created if absent and locked exclusively; if
// the lock is held, by this process or another, the error wraps
// ErrStateLocked and nothing is created for the instance. A listing instance
// always gets a fresh directory, whatever State says.
func (d *StateDirs) Acquire(req StateRequest) (*StateDir, error) {
	if req.Kind == ListingInstance {
		return d.fresh()
	}
	switch req.State {
	case "", StateFresh:
		return d.fresh()
	case StatePersistent:
		return d.persistent(req)
	default:
		return nil, fmt.Errorf("%w: unknown state %q", ErrStateKey, req.State)
	}
}

func (d *StateDirs) fresh() (*StateDir, error) {
	parent := filepath.Join(d.Root, "fresh")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("mcp: create state root: %w", err)
	}
	p, err := os.MkdirTemp(parent, "state-")
	if err != nil {
		return nil, fmt.Errorf("mcp: create fresh state directory: %w", err)
	}
	return &StateDir{Path: p, dirs: d}, nil
}

func (d *StateDirs) persistent(req StateRequest) (*StateDir, error) {
	key, err := StateKey(req)
	if err != nil {
		return nil, err
	}
	// flock excludes other open file descriptions, so this process's own
	// instances are excluded by the map as well as by the lock.
	d.mu.Lock()
	if d.held[key] {
		d.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrStateLocked, req.Server)
	}
	if d.held == nil {
		d.held = map[string]bool{}
	}
	d.held[key] = true
	d.mu.Unlock()
	release := func() {
		d.mu.Lock()
		delete(d.held, key)
		d.mu.Unlock()
	}

	parent := filepath.Join(d.Root, "persistent")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		release()
		return nil, fmt.Errorf("mcp: create state root: %w", err)
	}
	// The lock file sits beside the directory, so the server's directory
	// holds only what the server put there.
	lock, err := os.OpenFile(filepath.Join(parent, key+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		release()
		return nil, fmt.Errorf("mcp: open state lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		release()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrStateLocked, req.Server)
		}
		return nil, fmt.Errorf("mcp: lock state directory: %w", err)
	}
	p := filepath.Join(parent, key)
	if err := os.MkdirAll(p, 0o700); err != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		release()
		return nil, fmt.Errorf("mcp: create persistent state directory: %w", err)
	}
	return &StateDir{Path: p, Key: key, dirs: d, lock: lock}, nil
}

// Release is called once the instance has ended. A fresh directory is
// removed; a persistent one is kept and unlocked. Further calls return the
// first outcome. A fresh directory of an instance whose containment was not
// confirmed must not be released: it stays in place.
func (s *StateDir) Release() error {
	s.once.Do(func() {
		if s.lock == nil {
			if err := os.RemoveAll(s.Path); err != nil {
				s.err = fmt.Errorf("mcp: remove fresh state directory: %w", err)
			}
			return
		}
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		s.err = s.lock.Close()
		s.dirs.mu.Lock()
		delete(s.dirs.held, s.Key)
		s.dirs.mu.Unlock()
	})
	return s.err
}
