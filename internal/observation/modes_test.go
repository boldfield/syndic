package observation

import (
	"io/fs"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func modeReader(exec *bool, body string, reads *int) ModeReader {
	return func() (string, []byte, bool, error) {
		*reads++
		return journal.ObservationFile, []byte(body), *exec, nil
	}
}

func TestOwnerExecKeepsOnlyTheOwnerBit(t *testing.T) {
	for mode, want := range map[fs.FileMode]bool{
		0o755:                         true,
		0o644:                         false,
		0o100:                         true,
		0o077:                         false,
		fs.ModeSetuid | 0o644:         false,
		fs.ModeSetgid | fs.ModeSticky: false,
		fs.ModeSetuid | 0o755:         true,
	} {
		if got := OwnerExec(mode); got != want {
			t.Errorf("OwnerExec(%v) = %v, want %v", mode, got, want)
		}
	}
}

func TestModeRecordedOnceWithContent(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	exec, reads := false, 0

	if _, err := s.ObserveWithMode(f.turn, f.author, "run.sh", modeReader(&exec, "v1", &reads)); err != nil {
		t.Fatal(err)
	}
	// The file becomes executable in the live tree after its first
	// observation: a later observation, from any Store, serves the record.
	exec = true
	if _, err := NewStore(f.j).ObserveWithMode(f.turn, f.author, "run.sh", modeReader(&exec, "v2", &reads)); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("live reads = %d, want 1", reads)
	}
	got, err := NewStore(f.j).OwnerExec(f.turn, "run.sh")
	if err != nil || got {
		t.Fatalf("OwnerExec = %v, %v; want false, nil", got, err)
	}

	exec = true
	if _, err := s.ObserveWithMode(f.turn, f.author, "tool.sh", modeReader(&exec, "x", &reads)); err != nil {
		t.Fatal(err)
	}
	exec = false
	if got, err := s.OwnerExec(f.turn, "tool.sh"); err != nil || !got {
		t.Fatalf("OwnerExec = %v, %v; want true, nil", got, err)
	}
}

func TestUnrecordedPathsAreNotExecutable(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	var n int
	// Observe, which carries no mode, and a path never observed.
	if _, err := s.Observe(f.turn, f.author, "old.sh", func() (string, []byte, error) {
		n++
		return journal.ObservationFile, []byte("x"), nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"old.sh", "never.sh"} {
		if got, err := s.OwnerExec(f.turn, p); err != nil || got {
			t.Errorf("OwnerExec(%q) = %v, %v; want false, nil", p, got, err)
		}
	}
}

func TestModeIgnoredForNonFiles(t *testing.T) {
	f := newFixture(t)
	s := NewStore(f.j)
	if _, err := s.ObserveWithMode(f.turn, f.author, "gone", func() (string, []byte, bool, error) {
		return journal.ObservationAbsent, nil, true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.OwnerExec(f.turn, "gone"); err != nil || got {
		t.Fatalf("OwnerExec = %v, %v; want false, nil", got, err)
	}
}
