package observation

import (
	"io/fs"
	"path/filepath"
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
	// An observation with no mode row, as one recorded before the mode record
	// existed; written directly, since every current observation has a row.
	db, err := journal.Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lf := fixture{j: journal.NewStore(db)}
	if err := lf.j.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun([]byte(`{}`))
		if err != nil {
			return err
		}
		if lf.turn, err = tx.CreateTurn(run, "review", nil); err != nil {
			return err
		}
		lf.author, err = tx.BeginAttempt(lf.turn, "author", "author", 1, nil, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	const sha = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if _, err := db.Conn().Exec(`INSERT INTO tool_content (sha256, byte_len, body) VALUES (?, 0, X'')`, sha); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`INSERT INTO observations (id, turn_id, attempt_id, snapshot_version, path, kind, content_sha256, journal_seq, recorded_at)
		VALUES ('legacy', ?, ?, 1, X'6F6C642E7368', 'f', ?, 1, 'now')`, lf.turn, lf.author, sha); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Conn().QueryRow(`SELECT COUNT(*) FROM observation_modes WHERE turn_id = ?`, lf.turn).Scan(&n); err != nil || n != 0 {
		t.Fatalf("mode rows = %d, %v; want none", n, err)
	}
	if got, err := NewStore(lf.j).OwnerExec(lf.turn, "old.sh"); err != nil || got {
		t.Errorf("OwnerExec(old.sh) = %v, %v; want false, nil", got, err)
	}
	if got, err := s.OwnerExec(f.turn, "never.sh"); err != nil || got {
		t.Errorf("OwnerExec(never.sh) = %v, %v; want false, nil", got, err)
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
