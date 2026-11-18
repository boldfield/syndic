// Package lockfile provides exclusive locking for the engine socket and lock file.
package lockfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Paths holds the runtime paths for the engine socket and lock file.
type Paths struct {
	Dir    string // .../syndic
	Socket string // Dir/engine.sock
	Lock   string // Dir/engine.lock
}

// Resolve returns the first of $XDG_RUNTIME_DIR/syndic, $XDG_STATE_HOME/syndic,
// <homeDir>/.local/state/syndic whose parent directory exists and is writable.
// It creates the syndic directory with mode 0700 if absent.
// An unset or empty variable is skipped.
// If none qualifies it returns an error listing what it tried.
func Resolve(getenv func(string) string, homeDir string) (Paths, error) {
	var paths []string
	var tried []string

	xdgRuntimeDir := getenv("XDG_RUNTIME_DIR")
	if xdgRuntimeDir != "" {
		paths = append(paths, filepath.Join(xdgRuntimeDir, "syndic"))
		tried = append(tried, fmt.Sprintf("$XDG_RUNTIME_DIR/syndic (%s)", xdgRuntimeDir))
	}

	xdgStateHome := getenv("XDG_STATE_HOME")
	if xdgStateHome != "" {
		paths = append(paths, filepath.Join(xdgStateHome, "syndic"))
		tried = append(tried, fmt.Sprintf("$XDG_STATE_HOME/syndic (%s)", xdgStateHome))
	}

	if homeDir != "" {
		paths = append(paths, filepath.Join(homeDir, ".local", "state", "syndic"))
		tried = append(tried, fmt.Sprintf("<homeDir>/.local/state/syndic (%s)", homeDir))
	}

	for i, p := range paths {
		parent := filepath.Dir(p)

		fi, err := os.Stat(parent)
		if err != nil {
			// For XDG paths, skip if parent doesn't exist. For homeDir fallback (last candidate),
			// create the parent directories.
			if i < len(paths)-1 {
				continue
			}
			if err := os.MkdirAll(parent, 0755); err != nil {
				continue
			}
		} else if !fi.IsDir() {
			continue
		}

		if err := isWritable(parent); err != nil {
			continue
		}

		if err := os.MkdirAll(p, 0700); err != nil {
			continue
		}

		if err := os.Chmod(p, 0700); err != nil {
			continue
		}

		return Paths{
			Dir:    p,
			Socket: filepath.Join(p, "engine.sock"),
			Lock:   filepath.Join(p, "engine.lock"),
		}, nil
	}

	return Paths{}, fmt.Errorf("no writable directory found: tried %v", tried)
}

// isWritable returns nil if the directory is writable, an error otherwise.
func isWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".check-")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// Holder contains the PID and start time of the lock holder.
type Holder struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// Lock represents an exclusive lock on the lock file.
type Lock struct {
	file *os.File
}

// ErrHeld is returned when the lock is already held.
var ErrHeld = errors.New("engine lock is held")

// HeldError is returned when the lock is held with information about the holder.
type HeldError struct {
	Holder Holder
}

// Error implements the error interface.
func (e *HeldError) Error() string {
	return fmt.Sprintf("engine lock is held by PID %d since %s", e.Holder.PID, e.Holder.StartedAt.Format(time.RFC3339))
}

// Is returns true if target is ErrHeld.
func (e *HeldError) Is(target error) bool {
	return target == ErrHeld
}

// Acquire opens the lock file (creating it with mode 0600), takes an exclusive
// non-blocking flock, and writes the current PID and now in JSON format.
// If the lock is held by another open file description it returns a *HeldError
// without blocking and without modifying the file.
func Acquire(path string, now time.Time) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		_ = f.Close()

		if err == syscall.EWOULDBLOCK {
			holder, readErr := ReadHolder(path)
			if readErr == nil {
				return nil, &HeldError{Holder: holder}
			}
			// ReadHolder failure is acceptable; return a zero Holder since the lock is held.
			return nil, &HeldError{Holder: Holder{}}
		}

		return nil, fmt.Errorf("lock file: %w", err)
	}

	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("truncate lock file: %w", err)
	}

	holder := Holder{
		PID:       os.Getpid(),
		StartedAt: now,
	}

	data, err := json.Marshal(holder)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("marshal holder: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, fmt.Errorf("write lock file: %w", err)
	}

	return &Lock{file: f}, nil
}

// Release unlocks and closes the lock file.
// The file is left in place.
func (l *Lock) Release() error {
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()

	if err != nil {
		return fmt.Errorf("unlock: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close: %w", closeErr)
	}

	return nil
}

// ReadHolder reads the lock holder information from the lock file.
func ReadHolder(path string) (Holder, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Holder{}, fmt.Errorf("read lock file: %w", err)
	}

	var holder Holder
	if err := json.Unmarshal(data, &holder); err != nil {
		return Holder{}, fmt.Errorf("unmarshal holder: %w", err)
	}

	return holder, nil
}

// RemoveStaleSocket removes the socket file if it exists.
// The caller must hold the engine lock; otherwise it may remove a live engine's socket.
func RemoveStaleSocket(p Paths) error {
	err := os.Remove(p.Socket)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove socket: %w", err)
	}
	return nil
}
