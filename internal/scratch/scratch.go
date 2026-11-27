// Package scratch builds one participant's scratch directory for a command
// (design v7 delta, section 5): the turn's record under the staging overlay,
// with the design's rules for entry types, modes and times and its bounds on
// size, and the three values the command's view records.
package scratch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/boldfield/syndic/internal/command"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
)

// Default bounds (tools.exec.max_workspace_bytes and max_workspace_files) and
// exclusion (tools.exec.exclude).
const (
	DefaultMaxBytes = 1 << 30
	DefaultMaxFiles = 100000
)

// DefaultExclude is tools.exec.exclude's default.
var DefaultExclude = []string{".git/"}

// ErrBound is matched, through errors.Is, by the error Build returns when the
// workspace passes max_workspace_bytes or max_workspace_files. The call is
// refused with a tool error and nothing runs.
var ErrBound = errors.New("workspace bound exceeded")

// BoundError says which bound was passed.
type BoundError struct {
	Bound string // "max_workspace_bytes" or "max_workspace_files"
	Limit int64
}

func (e *BoundError) Error() string {
	return fmt.Sprintf("workspace is over tools.exec.%s (%d)", e.Bound, e.Limit)
}

// Unwrap lets errors.Is(err, ErrBound) match.
func (e *BoundError) Unwrap() error { return ErrBound }

// Record is the turn's snapshot record and the live tree it is taken from.
// First observations are made under AttemptID.
type Record struct {
	Journal   *journal.Store
	Store     *observation.Store
	FS        *workspace.Confined
	TurnID    string
	AttemptID string
}

// Overlay is the staging map to lay over the record: the turn's own for an
// author, the frozen candidate's change set for a reviewer. An entry with an
// empty BaseSHA256 is a creation, any other a modification.
type Overlay []journal.StagingEntry

// Config is the part of tools.exec the build reads.
type Config struct {
	MaxBytes   int64     // max_workspace_bytes; <= 0 selects DefaultMaxBytes
	MaxFiles   int64     // max_workspace_files; <= 0 selects DefaultMaxFiles
	Exclude    []string  // tools.exec.exclude; nil selects DefaultExclude
	SubmitTime time.Time // modification time of every entry
}

type builder struct {
	dir     string
	rec     Record
	cfg     Config
	staged  map[string]journal.StagingEntry
	staging map[string]string
	files   int64
	bytes   int64
	modes   []modeEntry
	dirs    []string
}

type modeEntry struct {
	path string
	exec bool
}

// Build replaces dir with the scratch directory for rec under overlay and
// returns the view taken when it completes. Whatever was in dir is removed
// first. On any failure, including a bound, dir is removed; observations
// already made stay in the record.
func Build(dir string, rec Record, overlay Overlay, cfg Config) (command.View, error) {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = DefaultMaxFiles
	}
	if cfg.Exclude == nil {
		cfg.Exclude = DefaultExclude
	}
	if err := os.RemoveAll(dir); err != nil {
		return command.View{}, fmt.Errorf("clear scratch directory: %w", err)
	}
	b := &builder{dir: dir, rec: rec, cfg: cfg, staged: map[string]journal.StagingEntry{}, staging: map[string]string{}}
	for _, e := range overlay {
		b.staged[e.Path] = e
		b.staging[e.Path] = e.NewSHA256
	}
	view, err := b.build(overlay)
	if err != nil {
		_ = os.RemoveAll(dir)
		return command.View{}, err
	}
	return view, nil
}

func (b *builder) build(overlay Overlay) (command.View, error) {
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return command.View{}, fmt.Errorf("create scratch directory: %w", err)
	}
	if err := os.Chmod(b.dir, 0o755); err != nil {
		return command.View{}, err
	}
	b.dirs = append(b.dirs, b.dir)
	if err := b.walk(observation.Root); err != nil {
		return command.View{}, err
	}
	// Directories last, innermost first, so that creating nothing further
	// disturbs a time already set.
	for i := len(b.dirs) - 1; i >= 0; i-- {
		if err := os.Chtimes(b.dirs[i], b.cfg.SubmitTime, b.cfg.SubmitTime); err != nil {
			return command.View{}, err
		}
	}
	overlaySum, err := b.overlayHash(overlay)
	if err != nil {
		return command.View{}, err
	}
	var version int
	if err := b.rec.Journal.Tx(func(tx *journal.Tx) (err error) {
		version, err = tx.SnapshotVersion(b.rec.TurnID)
		return err
	}); err != nil {
		return command.View{}, err
	}
	return command.View{SnapshotVersion: version, OverlaySHA256: overlaySum, ModesSHA256: modesHash(b.modes)}, nil
}

func (b *builder) excluded(p string) bool {
	for _, pat := range b.cfg.Exclude {
		dirOnly := strings.HasSuffix(pat, "/")
		pat = strings.Trim(pat, "/")
		if pat == "" {
			continue
		}
		if p == pat || strings.HasPrefix(p, pat+"/") {
			return true
		}
		if !dirOnly {
			if ok, _ := path.Match(pat, p); ok {
				return true
			}
		}
	}
	return false
}

func join(dir, name string) string {
	if dir == observation.Root {
		return name
	}
	return dir + "/" + name
}

func (b *builder) content(sha string) (body []byte, err error) {
	err = b.rec.Journal.Tx(func(tx *journal.Tx) (err error) {
		body, err = tx.Content(sha)
		return err
	})
	return body, err
}

// walk materialises the directory at rel, whose scratch counterpart exists.
func (b *builder) walk(rel string) error {
	o, err := b.rec.Store.ObserveWithMode(b.rec.TurnID, b.rec.AttemptID, rel, b.reader(rel))
	if err != nil {
		if rel != observation.Root && skippable(err) {
			return nil
		}
		return err
	}
	types := map[string]string{}
	if o.Kind == journal.ObservationListing {
		body, err := b.content(o.ContentSHA256)
		if err != nil {
			return err
		}
		entries, err := observation.ParseEntries(body)
		if err != nil {
			return fmt.Errorf("recorded listing %q: %w", rel, err)
		}
		for _, e := range entries {
			types[e.Name] = e.Type
		}
	}
	for n, t := range observation.StagedChildren(b.staging, rel) {
		types[n] = t
	}
	names := make([]string, 0, len(types))
	for n := range types {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		child := join(rel, name)
		if b.rec.FS.IsProtected(child) || b.excluded(child) {
			continue
		}
		target := filepath.Join(b.dir, filepath.FromSlash(child))
		switch types[name] {
		case "d":
			if err := os.Mkdir(target, 0o755); err != nil {
				return err
			}
			if err := os.Chmod(target, 0o755); err != nil {
				return err
			}
			b.dirs = append(b.dirs, target)
			if err := b.walk(child); err != nil {
				return err
			}
		case "f":
			if err := b.file(child, target); err != nil {
				return err
			}
		}
		// Symbolic links, FIFOs, sockets and devices are left out.
	}
	return nil
}

func skippable(err error) bool {
	return errors.Is(err, observation.ErrOtherEntry) || errors.Is(err, observation.ErrSymlinkOnPath)
}

func (b *builder) file(rel, target string) error {
	var body []byte
	var exec bool
	if e, ok := b.staged[rel]; ok {
		var err error
		if body, err = b.content(e.NewSHA256); err != nil {
			return err
		}
		if e.BaseSHA256 != "" {
			if exec, err = b.rec.Store.OwnerExec(b.rec.TurnID, rel); err != nil {
				return err
			}
		}
	} else {
		o, err := b.rec.Store.ObserveWithMode(b.rec.TurnID, b.rec.AttemptID, rel, b.reader(rel))
		if err != nil {
			if skippable(err) {
				return nil
			}
			return err
		}
		if o.Kind != journal.ObservationFile {
			return nil
		}
		if body, err = b.content(o.ContentSHA256); err != nil {
			return err
		}
		if exec, err = b.rec.Store.OwnerExec(b.rec.TurnID, rel); err != nil {
			return err
		}
	}
	if b.files+1 > b.cfg.MaxFiles {
		return &BoundError{Bound: "max_workspace_files", Limit: b.cfg.MaxFiles}
	}
	if b.bytes+int64(len(body)) > b.cfg.MaxBytes {
		return &BoundError{Bound: "max_workspace_bytes", Limit: b.cfg.MaxBytes}
	}
	b.files++
	b.bytes += int64(len(body))
	mode := fs.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	if err := os.WriteFile(target, body, mode); err != nil {
		return err
	}
	if err := os.Chmod(target, mode); err != nil {
		return err
	}
	if err := os.Chtimes(target, b.cfg.SubmitTime, b.cfg.SubmitTime); err != nil {
		return err
	}
	b.modes = append(b.modes, modeEntry{path: rel, exec: exec})
	return nil
}

func (b *builder) overlayHash(overlay Overlay) (string, error) {
	changes := make([]observation.Change, 0, len(overlay))
	for _, e := range overlay {
		body, err := b.content(e.NewSHA256)
		if err != nil {
			return "", err
		}
		c := observation.Change{Path: e.Path, Op: observation.OpModify, BaseSHA: e.BaseSHA256, NewSHA: e.NewSHA256, NewLen: int64(len(body))}
		if e.BaseSHA256 == "" {
			c.Op, c.BaseSHA = observation.OpCreate, observation.Absent
		}
		changes = append(changes, c)
	}
	lines, err := observation.FrameChangeSetLines(changes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(lines)
	return hex.EncodeToString(sum[:]), nil
}

// modesHash is SHA-256 over one entry per regular file, sorted bytewise by
// path: the bit as 'x' or '-', a space, the path's length in decimal, a line
// feed, the path, a line feed.
func modesHash(entries []modeEntry) string {
	sorted := append([]modeEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].path < sorted[j].path })
	var buf bytes.Buffer
	for _, e := range sorted {
		bit := byte('-')
		if e.exec {
			bit = 'x'
		}
		buf.WriteByte(bit)
		buf.WriteByte(' ')
		buf.WriteString(strconv.Itoa(len(e.path)))
		buf.WriteByte('\n')
		buf.WriteString(e.path)
		buf.WriteByte('\n')
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// reader reads rel from the live tree for the observation store.
func (b *builder) reader(rel string) observation.ModeReader {
	return func() (string, []byte, bool, error) {
		if b.rec.FS.IsProtected(rel) {
			return "", nil, false, fmt.Errorf("%q is protected", rel)
		}
		ex, err := b.rec.FS.Examine(rel)
		switch {
		case err == nil:
		case errors.Is(err, workspace.ErrSymlinkOnPath):
			return "", nil, false, observation.ErrSymlinkOnPath
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR):
			return journal.ObservationAbsent, nil, false, nil
		case errors.Is(err, workspace.ErrSymlink):
			return observation.KindSymlink, nil, false, nil
		case errors.Is(err, workspace.ErrNotRegular):
			return observation.KindSpecial, nil, false, nil
		default:
			return "", nil, false, err
		}
		if !ex.IsDir {
			return journal.ObservationFile, ex.Content, ex.OwnerExec, nil
		}
		entries := make([]observation.Entry, len(ex.Entries))
		for i, e := range ex.Entries {
			entries[i] = observation.Entry{Type: e.Type, Name: e.Name}
		}
		framed, err := observation.FrameEntries(entries)
		if err != nil {
			return "", nil, false, err
		}
		return journal.ObservationListing, framed, false, nil
	}
}
