package sandbox

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLockProbeSeesHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !lockFree(path) || lockHeld(path) {
		t.Fatal("unlocked file reported held")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if lockFree(path) || !lockHeld(path) {
		t.Fatal("held file reported free")
	}
}
