package scratch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
)

var submit = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type fx struct {
	t   *testing.T
	ws  string
	dir string
	rec Record
	cfg Config
}

func newFx(t *testing.T) *fx {
	t.Helper()
	db, err := journal.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	j := journal.NewStore(db)
	var turn, attempt string
	if err := j.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun([]byte(`{}`))
		if err != nil {
			return err
		}
		if turn, err = tx.CreateTurn(run, "review", nil); err != nil {
			return err
		}
		attempt, err = tx.BeginAttempt(turn, "author", "author", 1, nil, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	c, err := workspace.OpenConfined(ws, []string{"secret.txt"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	base := t.TempDir()
	return &fx{t: t, ws: ws, dir: filepath.Join(base, "scratch"),
		rec: Record{Journal: j, Store: observation.NewStore(j), FS: c, TurnID: turn, AttemptID: attempt},
		cfg: Config{SubmitTime: submit}}
}

func (f *fx) put(rel, body string, mode os.FileMode) {
	f.t.Helper()
	p := filepath.Join(f.ws, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) overlay() Overlay {
	f.t.Helper()
	es, err := f.rec.Journal.Staging(f.rec.TurnID)
	if err != nil {
		f.t.Fatal(err)
	}
	return Overlay(es)
}

func (f *fx) build() (v struct{ V, O, M string }, err error) {
	view, err := Build(f.dir, f.rec, f.overlay(), f.cfg)
	return struct{ V, O, M string }{fmt.Sprint(view.SnapshotVersion), view.OverlaySHA256, view.ModesSHA256}, err
}

func (f *fx) stage(rel, body string) {
	f.t.Helper()
	if _, err := f.rec.Store.ObserveWithMode(f.rec.TurnID, f.rec.AttemptID, rel, func() (string, []byte, bool, error) {
		return journal.ObservationAbsent, nil, false, nil
	}); err != nil && !errors.Is(err, observation.ErrSnapshotConflict) {
		f.t.Fatal(err)
	}
	if err := f.rec.Journal.Tx(func(tx *journal.Tx) error {
		_, err := tx.StageWrite(f.rec.TurnID, rel, []byte(body))
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) read(rel string) string {
	b, err := os.ReadFile(filepath.Join(f.dir, rel))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *fx) absent(rel string) {
	f.t.Helper()
	if _, err := os.Lstat(filepath.Join(f.dir, rel)); !os.IsNotExist(err) {
		f.t.Fatalf("%s present (err %v)", rel, err)
	}
}

func (f *fx) mode(rel string) os.FileMode {
	fi, err := os.Lstat(filepath.Join(f.dir, rel))
	if err != nil {
		f.t.Fatal(err)
	}
	return fi.Mode()
}

func TestStagedEditAndCreationThenUnstage(t *testing.T) {
	f := newFx(t)
	f.put("a.txt", "base", 0o644)
	f.put("sub/b.txt", "bbb", 0o644)
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	f.stage("a.txt", "edited")
	f.stage("sub/new/c.txt", "created")
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if f.read("a.txt") != "edited" || f.read("sub/new/c.txt") != "created" || f.read("sub/b.txt") != "bbb" {
		t.Fatal("overlay not applied")
	}
	for _, p := range []string{"a.txt", "sub/new/c.txt"} {
		if err := f.rec.Journal.Tx(func(tx *journal.Tx) error { return tx.Unstage(f.rec.TurnID, p) }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if f.read("a.txt") != "base" {
		t.Fatal("base not shown after unstage")
	}
	f.absent("sub/new/c.txt")
}

func TestSpecialEntriesProtectedAndExcludedAreAbsent(t *testing.T) {
	f := newFx(t)
	f.put("ok.txt", "ok", 0o644)
	f.put("secret.txt", "s", 0o644)
	f.put(".git/config", "g", 0o644)
	if err := os.Symlink("ok.txt", filepath.Join(f.ws, "link")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(f.ws, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.stage("secret.txt", "overlay")
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	f.read("ok.txt")
	for _, p := range []string{"link", "fifo", "secret.txt", ".git"} {
		f.absent(p)
	}
}

func TestModesAndTimes(t *testing.T) {
	f := newFx(t)
	f.put("run.sh", "x", 0o755)
	f.put("suid", "x", 0o4755)
	f.put("plain", "x", 0o666)
	f.put("d/in", "x", 0o644)
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if m := f.mode("run.sh"); m != 0o755 {
		t.Fatalf("run.sh %v", m)
	}
	if m := f.mode("suid"); m != 0o755 {
		t.Fatalf("suid %v", m)
	}
	if m := f.mode("plain"); m != 0o644 {
		t.Fatalf("plain %v", m)
	}
	if m := f.mode("d"); m.Perm() != 0o755 || !m.IsDir() {
		t.Fatalf("d %v", m)
	}
	f.stage("run.sh", "edit")
	f.stage("made.sh", "new")
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if f.mode("run.sh") != 0o755 || f.mode("made.sh") != 0o644 {
		t.Fatalf("staged modes %v %v", f.mode("run.sh"), f.mode("made.sh"))
	}
	for _, p := range []string{"run.sh", "d", "d/in", "made.sh", "."} {
		fi, err := os.Lstat(filepath.Join(f.dir, p))
		if err != nil || !fi.ModTime().Equal(submit) {
			t.Fatalf("%s mtime %v err %v", p, fi.ModTime(), err)
		}
	}
	// Made executable live after first observation: not executable later.
	if err := os.Chmod(filepath.Join(f.ws, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if f.mode("plain") != 0o644 {
		t.Fatal("live mode leaked")
	}
}

func TestRecordedContentServedAfterLiveChangeAndRebuildClears(t *testing.T) {
	f := newFx(t)
	f.put("a", "one", 0o644)
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	f.put("a", "two", 0o644)
	if err := os.WriteFile(filepath.Join(f.dir, "leftover"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if f.read("a") != "one" {
		t.Fatal("live content seen")
	}
	f.absent("leftover")
}

func TestBoundsRefuseRemoveAndKeepObservations(t *testing.T) {
	for name, cfg := range map[string]Config{
		"bytes": {SubmitTime: submit, MaxBytes: 5},
		"files": {SubmitTime: submit, MaxFiles: 2},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.cfg = cfg
			f.put("a", "1234", 0o644)
			f.put("b", "1234", 0o644)
			f.put("c", "1234", 0o644)
			_, err := f.build()
			var be *BoundError
			if !errors.Is(err, ErrBound) || !errors.As(err, &be) {
				t.Fatalf("err = %v", err)
			}
			if _, err := os.Lstat(f.dir); !os.IsNotExist(err) {
				t.Fatalf("partial directory remains: %v", err)
			}
			obs, err := f.rec.Journal.Observations(f.rec.TurnID)
			if err != nil || len(obs) == 0 {
				t.Fatalf("observations %v %v", obs, err)
			}
		})
	}
}

func TestViewValues(t *testing.T) {
	f := newFx(t)
	f.put("x.sh", "x", 0o755)
	f.put("p", "p", 0o644)
	empty := sha256.Sum256(nil)
	v, err := f.build()
	if err != nil {
		t.Fatal(err)
	}
	if v.O != hex.EncodeToString(empty[:]) {
		t.Fatalf("empty overlay hash %s", v.O)
	}
	f.stage("n", "new")
	v, err = f.build()
	if err != nil {
		t.Fatal(err)
	}
	// Byte framing: sorted by path, bit, space, length, LF, path, LF.
	want := sha256.Sum256([]byte("- 1\nn\n- 1\np\nx 4\nx.sh\n"))
	var ver int
	if err := f.rec.Journal.Tx(func(tx *journal.Tx) (err error) {
		ver, err = tx.SnapshotVersion(f.rec.TurnID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if v.V != fmt.Sprint(ver) {
		t.Fatalf("version %s want %d", v.V, ver)
	}
	if v.M != hex.EncodeToString(want[:]) {
		t.Fatalf("modes hash %s want %x", v.M, want)
	}
	newSum := sha256.Sum256([]byte("new"))
	line := fmt.Sprintf("1 n create absent %s 3\n", hex.EncodeToString(newSum[:]))
	ov := sha256.Sum256([]byte(line))
	if v.O != hex.EncodeToString(ov[:]) {
		t.Fatalf("overlay hash %s", v.O)
	}
}

func TestModesHashSorting(t *testing.T) {
	got := modesHash([]modeEntry{{"b", false}, {"a/z", true}, {"a", false}})
	var in string
	for _, n := range []string{"- 1\na\n", "x 3\na/z\n", "- 1\nb\n"} {
		in += n
	}
	want := sha256.Sum256([]byte(in))
	if got != hex.EncodeToString(want[:]) {
		t.Fatal("framing mismatch")
	}
}
