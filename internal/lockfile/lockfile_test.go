package lockfile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveXDGRuntimeDir(t *testing.T) {
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "run")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}

	getenv := func(name string) string {
		switch name {
		case "XDG_RUNTIME_DIR":
			return runtimeDir
		case "XDG_STATE_HOME":
			return ""
		}
		return ""
	}

	paths, err := Resolve(getenv, "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if paths.Dir != filepath.Join(runtimeDir, "syndic") {
		t.Errorf("Dir mismatch: got %q, want %q", paths.Dir, filepath.Join(runtimeDir, "syndic"))
	}

	if _, err := os.Stat(paths.Dir); err != nil {
		t.Errorf("Dir not created: %v", err)
	}

	fi, err := os.Stat(paths.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0700 {
		t.Errorf("Dir mode: got %o, want %o", fi.Mode().Perm(), 0700)
	}
}

func TestResolveXDGStateHome(t *testing.T) {
	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	getenv := func(name string) string {
		switch name {
		case "XDG_RUNTIME_DIR":
			return ""
		case "XDG_STATE_HOME":
			return stateDir
		}
		return ""
	}

	paths, err := Resolve(getenv, "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if paths.Dir != filepath.Join(stateDir, "syndic") {
		t.Errorf("Dir mismatch: got %q, want %q", paths.Dir, filepath.Join(stateDir, "syndic"))
	}
}

func TestResolveHomeDir(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatal(err)
	}

	getenv := func(name string) string {
		return ""
	}

	paths, err := Resolve(getenv, homeDir)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if paths.Dir != filepath.Join(homeDir, ".local", "state", "syndic") {
		t.Errorf("Dir mismatch: got %q, want %q", paths.Dir, filepath.Join(homeDir, ".local", "state", "syndic"))
	}
}

func TestResolveFallback(t *testing.T) {
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "run")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}

	homeDir := filepath.Join(tmpDir, "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatal(err)
	}

	getenv := func(name string) string {
		switch name {
		case "XDG_RUNTIME_DIR":
			return runtimeDir
		case "XDG_STATE_HOME":
			return ""
		}
		return ""
	}

	paths, err := Resolve(getenv, homeDir)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if paths.Dir != filepath.Join(runtimeDir, "syndic") {
		t.Errorf("Dir mismatch: got %q, want %q", paths.Dir, filepath.Join(runtimeDir, "syndic"))
	}
}

func TestResolveNoQualifyingDir(t *testing.T) {
	getenv := func(name string) string {
		return ""
	}

	paths, err := Resolve(getenv, "/nonexistent/path")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if paths != (Paths{}) {
		t.Errorf("Expected empty Paths, got %+v", paths)
	}
}

func TestResolveEmptyEnv(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatal(err)
	}

	getenv := func(name string) string {
		return ""
	}

	paths, err := Resolve(getenv, homeDir)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if paths.Dir != filepath.Join(homeDir, ".local", "state", "syndic") {
		t.Errorf("Dir mismatch: got %q, want %q", paths.Dir, filepath.Join(homeDir, ".local", "state", "syndic"))
	}
}

func TestResolveMissingRuntimeDirSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	missingRuntimeDir := filepath.Join(tmpDir, "missing-run")
	stateDir := filepath.Join(tmpDir, "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}

	getenv := func(name string) string {
		switch name {
		case "XDG_RUNTIME_DIR":
			return missingRuntimeDir
		case "XDG_STATE_HOME":
			return stateDir
		}
		return ""
	}

	paths, err := Resolve(getenv, "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if paths.Dir != filepath.Join(stateDir, "syndic") {
		t.Errorf("Dir mismatch: got %q, want %q", paths.Dir, filepath.Join(stateDir, "syndic"))
	}

	// Ensure the missing runtime path was not created
	if _, err := os.Stat(missingRuntimeDir); err == nil {
		t.Fatal("Missing runtime dir should not have been created")
	} else if !os.IsNotExist(err) {
		t.Fatalf("Unexpected error checking missing runtime dir: %v", err)
	}
}

func TestResolveXDGStateHomeUnset(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	if err := os.MkdirAll(homeDir, 0755); err != nil {
		t.Fatal(err)
	}

	getenv := func(name string) string {
		return ""
	}

	paths, err := Resolve(getenv, homeDir)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if paths.Dir != filepath.Join(homeDir, ".local", "state", "syndic") {
		t.Errorf("Dir mismatch: got %q, want %q", paths.Dir, filepath.Join(homeDir, ".local", "state", "syndic"))
	}
}

func TestAcquireAndReadHolder(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")
	now := time.Now().UTC()

	lock, err := Acquire(lockPath, now)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			t.Errorf("Release failed: %v", err)
		}
	}()

	holder, err := ReadHolder(lockPath)
	if err != nil {
		t.Fatalf("ReadHolder failed: %v", err)
	}

	if holder.PID != os.Getpid() {
		t.Errorf("PID mismatch: got %d, want %d", holder.PID, os.Getpid())
	}

	if holder.StartedAt != now {
		t.Errorf("StartedAt mismatch: got %v, want %v", holder.StartedAt, now)
	}
}

func TestAcquireSecondFails(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")
	now := time.Now().UTC()

	lock1, err := Acquire(lockPath, now)
	if err != nil {
		t.Fatalf("First Acquire failed: %v", err)
	}
	defer func() {
		if err := lock1.Release(); err != nil {
			t.Errorf("Release failed: %v", err)
		}
	}()

	_, err = Acquire(lockPath, now)
	if err == nil {
		t.Fatal("Second Acquire should fail")
	}

	if !errors.Is(err, ErrHeld) {
		t.Errorf("Expected ErrHeld, got %v", err)
	}

	var heldErr *HeldError
	if !errors.As(err, &heldErr) {
		t.Fatalf("Expected *HeldError, got %T", err)
	}

	if heldErr.Holder.PID != os.Getpid() {
		t.Errorf("Holder PID mismatch: got %d, want %d", heldErr.Holder.PID, os.Getpid())
	}
}

func TestReleaseAndAcquireAgain(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")
	now := time.Now().UTC()

	lock1, err := Acquire(lockPath, now)
	if err != nil {
		t.Fatalf("First Acquire failed: %v", err)
	}

	if err := lock1.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	lock2, err := Acquire(lockPath, now)
	if err != nil {
		t.Fatalf("Second Acquire after Release failed: %v", err)
	}
	if err := lock2.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
}

func TestStaleLockCanBeAcquired(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")
	now := time.Now().UTC()

	lock1, err := Acquire(lockPath, now)
	if err != nil {
		t.Fatalf("First Acquire failed: %v", err)
	}

	// Close the descriptor without unlocking to simulate a stale lock.
	if err := lock1.file.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	lock2, err := Acquire(lockPath, now)
	if err != nil {
		t.Fatalf("Acquire stale lock failed: %v", err)
	}
	if err := lock2.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
}

func TestRemoveStaleSocket(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "engine.sock")

	f, err := os.Create(sockPath)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	paths := Paths{
		Dir:    tmpDir,
		Socket: sockPath,
	}

	if err := RemoveStaleSocket(paths); err != nil {
		t.Fatalf("RemoveStaleSocket failed: %v", err)
	}

	if _, err := os.Stat(sockPath); err == nil {
		t.Fatal("Socket file still exists")
	} else if !os.IsNotExist(err) {
		t.Fatalf("Unexpected error: %v", err)
	}
}

func TestRemoveStaleSocketNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "engine.sock")

	paths := Paths{
		Dir:    tmpDir,
		Socket: sockPath,
	}

	if err := RemoveStaleSocket(paths); err != nil {
		t.Fatalf("RemoveStaleSocket with non-existent socket failed: %v", err)
	}
}

func TestLockFileMode(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")
	now := time.Now().UTC()

	lock, err := Acquire(lockPath, now)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			t.Errorf("Release failed: %v", err)
		}
	}()

	fi, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	if fi.Mode().Perm() != 0600 {
		t.Errorf("Lock file mode: got %o, want %o", fi.Mode().Perm(), 0600)
	}
}

func TestResolveCreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	runtimeDir := filepath.Join(tmpDir, "run")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}

	getenv := func(name string) string {
		if name == "XDG_RUNTIME_DIR" {
			return runtimeDir
		}
		return ""
	}

	paths, err := Resolve(getenv, "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if _, err := os.Stat(paths.Dir); err != nil {
		t.Errorf("Dir not created: %v", err)
	}

	if paths.Socket != filepath.Join(paths.Dir, "engine.sock") {
		t.Errorf("Socket path mismatch: got %q, want %q", paths.Socket, filepath.Join(paths.Dir, "engine.sock"))
	}

	if paths.Lock != filepath.Join(paths.Dir, "engine.lock") {
		t.Errorf("Lock path mismatch: got %q, want %q", paths.Lock, filepath.Join(paths.Dir, "engine.lock"))
	}
}

func TestReadHolderFormat(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "test.lock")

	holder := Holder{
		PID:       12345,
		StartedAt: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(holder)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(lockPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	readHolder, err := ReadHolder(lockPath)
	if err != nil {
		t.Fatalf("ReadHolder failed: %v", err)
	}

	if readHolder != holder {
		t.Errorf("Holder mismatch: got %+v, want %+v", readHolder, holder)
	}
}
