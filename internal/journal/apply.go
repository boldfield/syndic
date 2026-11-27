package journal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Apply errors. Both are returned wrapped; match them with errors.Is.
var (
	// ErrWorkspaceMissing is returned when the workspace root cannot be
	// opened. Nothing is written or journaled.
	ErrWorkspaceMissing = errors.New("workspace missing")
	// ErrBaseChanged is returned by a first apply when a path is not at its
	// base. The change set stays appliable and nothing is written. The error
	// is a *BaseChangedError naming the paths.
	ErrBaseChanged = errors.New("base changed")
)

// BaseChangedError names the paths that were not pending on a first apply.
type BaseChangedError struct {
	Paths []ChangeSetPath
}

func (e *BaseChangedError) Error() string {
	names := make([]string, len(e.Paths))
	for i, p := range e.Paths {
		names[i] = p.Path
	}
	return fmt.Sprintf("%s: %s", ErrBaseChanged, strings.Join(names, ", "))
}

func (e *BaseChangedError) Unwrap() error { return ErrBaseChanged }

// ApplyResult is the change set's state and per-path outcomes after an apply
// or a recovery. Conflicting paths are the entries whose Outcome is conflict.
type ApplyResult struct {
	State string
	Paths []ChangeSetPath
}

// applyHook, when set by a test, is called at named points of an apply: with
// "started" once apply_started is durable, with "write" before each pending
// path's effect and with "renamed" after it, before it is journaled. A
// returned error aborts the apply there, as a crash would.
var applyHook func(stage, path string) error

func hook(stage, path string) error {
	if applyHook == nil {
		return nil
	}
	return applyHook(stage, path)
}

// Live states of a path in the workspace.
type liveKind int

const (
	liveAbsent  liveKind = iota
	liveBlocked          // a symbolic link, a non-directory parent or a non-regular file
	liveFile
)

type live struct {
	kind liveKind
	sha  string
}

func openRoot(workspace string) (int, error) {
	fd, err := unix.Open(workspace, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open workspace %q: %w: %w", workspace, ErrWorkspaceMissing, err)
	}
	return fd, nil
}

// splitPath validates a change-set path as a clean relative path and returns
// its components.
func splitPath(path string) ([]string, error) {
	if path == "" || strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("path %q is not relative", path)
	}
	parts := strings.Split(path, "/")
	for _, c := range parts {
		if c == "" || c == "." || c == ".." || strings.ContainsRune(c, 0) {
			return nil, fmt.Errorf("path %q leaves the workspace or is not clean", path)
		}
	}
	return parts, nil
}

// walkDir opens the directory holding the path's last component by walking
// one component at a time from root with O_NOFOLLOW. With create, missing
// directories are made. The caller closes the returned descriptor. absent is true
// when a component is absent (and not created) or is not a plain directory.
func walkDir(root int, dirs []string, create bool) (fd int, absent bool, err error) {
	cur, err := unix.Dup(root)
	if err != nil {
		return -1, false, err
	}
	unix.CloseOnExec(cur)
	for _, c := range dirs {
		next, err := unix.Openat(cur, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if merr := unix.Mkdirat(cur, c, 0o777); merr != nil && !errors.Is(merr, unix.EEXIST) {
				_ = unix.Close(cur)
				return -1, false, merr
			}
			next, err = unix.Openat(cur, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		_ = unix.Close(cur)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				return -1, true, nil
			}
			return -1, false, err
		}
		cur = next
	}
	return cur, false, nil
}

// inspect opens name in dirfd with O_NOFOLLOW and reports what it refers to,
// hashing a regular file through the descriptor it opened. It never blocks on
// a special file.
func inspect(dirfd int, name string) (live, error) {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return live{kind: liveAbsent}, nil
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENXIO), errors.Is(err, unix.EMLINK):
		return live{kind: liveBlocked}, nil
	case err != nil:
		return live{}, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return live{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return live{kind: liveBlocked}, nil
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return live{}, err
	}
	return live{kind: liveFile, sha: computeSHA256(body)}, nil
}

// inspectPath is inspect through the confined walk from root.
func inspectPath(root int, path string) (live, error) {
	parts, err := splitPath(path)
	if err != nil {
		return live{kind: liveBlocked}, nil
	}
	dir, absent, err := walkDir(root, parts[:len(parts)-1], false)
	switch {
	case absent:
		return live{kind: liveAbsent}, nil
	case isBlocked(err):
		return live{kind: liveBlocked}, nil
	case err != nil:
		return live{}, err
	}
	defer func() { _ = unix.Close(dir) }()
	return inspect(dir, parts[len(parts)-1])
}

func isBlocked(err error) bool {
	return errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EMLINK)
}

// classify returns a path's outcome from the live workspace: at its base,
// pending; at its new content, applied; at neither, conflict. An absent base
// is pending while the name is absent.
func classify(root int, p ChangeSetPath) (string, error) {
	l, err := inspectPath(root, p.Path)
	if err != nil {
		return "", err
	}
	switch {
	case l.kind == liveBlocked:
		return PathConflict, nil
	case l.kind == liveAbsent && p.BaseSHA256 == "":
		return PathPending, nil
	case l.kind == liveAbsent:
		return PathConflict, nil
	case l.sha == p.NewSHA256:
		return PathApplied, nil
	case p.BaseSHA256 != "" && l.sha == p.BaseSHA256:
		return PathPending, nil
	}
	return PathConflict, nil
}

// writeFile performs one pending path's effect through root and returns
// applied, or conflict when the target fails its re-check or a create finds
// its name taken. The content goes to a temporary name in the target's
// directory; a modify re-opens the target by name immediately before renaming
// over it, a create links the temporary file to the name and so fails if the
// name exists. There is no replacing fallback.
func writeFile(root int, p ChangeSetPath, content []byte) (string, error) {
	parts, err := splitPath(p.Path)
	if err != nil {
		return PathConflict, nil
	}
	name := parts[len(parts)-1]
	dir, absent, err := walkDir(root, parts[:len(parts)-1], p.Op == "create")
	switch {
	case absent || isBlocked(err):
		return PathConflict, nil
	case err != nil:
		return "", err
	}
	defer func() { _ = unix.Close(dir) }()

	// A create takes 0o666 through openat, so the process umask applies; a
	// modify starts private and copies the original's permission bits.
	createMode := uint32(0o666)
	chmod := false
	var mode uint32
	if p.Op != "create" {
		createMode = 0o600
		l, err := inspect(dir, name)
		if err != nil {
			return "", err
		}
		if l.kind != liveFile || l.sha != p.BaseSHA256 {
			return PathConflict, nil
		}
		fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err == nil {
			var st unix.Stat_t
			if unix.Fstat(fd, &st) == nil {
				mode, chmod = uint32(st.Mode&0o7777), true
			}
			_ = unix.Close(fd)
		}
	}

	tmp, err := tempName()
	if err != nil {
		return "", err
	}
	tfd, err := unix.Openat(dir, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, createMode)
	if err != nil {
		return "", fmt.Errorf("create temporary file for %q: %w", p.Path, err)
	}
	defer func() { _ = unix.Unlinkat(dir, tmp, 0) }() // no-op once renamed away
	tf := os.NewFile(uintptr(tfd), tmp)
	_, werr := tf.Write(content)
	if werr == nil && chmod {
		werr = tf.Chmod(os.FileMode(mode & 0o7777))
	}
	if werr == nil {
		werr = tf.Sync()
	}
	if cerr := tf.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("write temporary file for %q: %w", p.Path, werr)
	}

	// Hook point after temp write, before final re-check (for atomic-save tests).
	if err := hook("temp", p.Path); err != nil {
		return "", err
	}

	if p.Op == "create" {
		if err := unix.Linkat(dir, tmp, dir, name, 0); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return PathConflict, nil
			}
			return "", fmt.Errorf("link %q: %w", p.Path, err)
		}
	} else {
		// Re-open by name through the verified directory immediately before
		// replacing, never through a descriptor kept from classification.
		l, err := inspect(dir, name)
		if err != nil {
			return "", err
		}
		if l.kind != liveFile || l.sha != p.BaseSHA256 {
			return PathConflict, nil
		}
		if err := unix.Renameat(dir, tmp, dir, name); err != nil {
			return "", fmt.Errorf("rename %q: %w", p.Path, err)
		}
	}
	_ = unix.Fsync(dir)
	return PathApplied, nil
}

func tempName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("temporary name: %w", err)
	}
	return ".syndic-" + hex.EncodeToString(b[:]) + ".tmp", nil
}

// result reads the change set's state and outcomes for the caller.
func (s *Store) applyResult(changeSetID string) (ApplyResult, error) {
	cs, err := s.GetChangeSet(changeSetID)
	if err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{State: cs.State, Paths: cs.Paths}, nil
}

// ApplyChangeSet applies a change set to workspace. The root is opened once
// and every operation is relative to that descriptor. An applied change set
// returns its state and writes nothing. A first apply (state appliable)
// requires every path pending, else it returns a *BaseChangedError and
// writes nothing. On a partial or conflict change set done paths are skipped
// and pending paths written. apply_started is committed before any write and
// each apply_file in its own transaction right after its effect, so a crash
// leaves a journal RecoverApply can classify. The final state is applied when
// every path is done, else conflict; conflicting paths are those of the
// result whose Outcome is conflict.
func (s *Store) ApplyChangeSet(workspace, changeSetID string, override bool) (ApplyResult, error) {
	cs, err := s.GetChangeSet(changeSetID)
	if err != nil {
		return ApplyResult{}, err
	}
	switch cs.State {
	case ChangeSetApplied:
		// Stable whatever the workspace has become since.
		return ApplyResult{State: cs.State, Paths: cs.Paths}, nil
	case ChangeSetAppliable, ChangeSetPartial, ChangeSetConflict:
	default:
		return ApplyResult{}, fmt.Errorf("apply of %s change set %q: %w", cs.State, cs.ID, ErrChangeSetState)
	}
	root, err := openRoot(workspace)
	if err != nil {
		return ApplyResult{}, err
	}
	defer func() { _ = unix.Close(root) }()

	outcomes := make(map[string]string, len(cs.Paths))
	for _, p := range cs.Paths {
		if outcomes[p.Path], err = classify(root, p); err != nil {
			return ApplyResult{}, fmt.Errorf("classify %q: %w", p.Path, err)
		}
	}
	if cs.State == ChangeSetAppliable {
		var bad []ChangeSetPath
		for _, p := range cs.Paths {
			if outcomes[p.Path] != PathPending {
				p.Outcome = outcomes[p.Path]
				bad = append(bad, p)
			}
		}
		if len(bad) > 0 {
			return ApplyResult{}, &BaseChangedError{Paths: bad}
		}
	}

	if err := s.Tx(func(tx *Tx) error { return tx.BeginApply(cs.ID, override) }); err != nil {
		return ApplyResult{}, err
	}
	if err := hook("started", ""); err != nil {
		return ApplyResult{}, err
	}
	for _, p := range cs.Paths {
		outcome := outcomes[p.Path]
		if outcome == PathPending {
			if err := hook("write", p.Path); err != nil {
				return ApplyResult{}, err
			}
			var content []byte
			if err := s.Tx(func(tx *Tx) (cerr error) {
				content, cerr = tx.Content(p.NewSHA256)
				return cerr
			}); err != nil {
				return ApplyResult{}, err
			}
			if outcome, err = writeFile(root, p, content); err != nil {
				return ApplyResult{}, err
			}
			if err := hook("renamed", p.Path); err != nil {
				return ApplyResult{}, err
			}
		}
		if outcome == p.Outcome {
			continue
		}
		if err := s.Tx(func(tx *Tx) error { return tx.RecordApplyFile(cs.ID, p.Path, outcome) }); err != nil {
			return ApplyResult{}, err
		}
	}
	if err := s.Tx(func(tx *Tx) error { _, ferr := tx.FinishApply(cs.ID); return ferr }); err != nil {
		return ApplyResult{}, err
	}
	return s.applyResult(cs.ID)
}

// RecoverApply classifies every path of an apply that began and never
// finished, records each path's outcome and the recovered state, and returns
// them. It writes nothing to the workspace. A change set with no unfinished
// apply is returned as it is.
func (s *Store) RecoverApply(workspace, changeSetID string) (ApplyResult, error) {
	root, err := openRoot(workspace)
	if err != nil {
		return ApplyResult{}, err
	}
	defer func() { _ = unix.Close(root) }()

	cs, err := s.GetChangeSet(changeSetID)
	if err != nil {
		return ApplyResult{}, err
	}
	ops, err := s.ApplyOps(changeSetID)
	if err != nil {
		return ApplyResult{}, err
	}
	if !inProgress(ops) {
		return ApplyResult{State: cs.State, Paths: cs.Paths}, nil
	}
	outcomes := make(map[string]string, len(cs.Paths))
	for _, p := range cs.Paths {
		if outcomes[p.Path], err = classify(root, p); err != nil {
			return ApplyResult{}, fmt.Errorf("classify %q: %w", p.Path, err)
		}
	}
	err = s.Tx(func(tx *Tx) error {
		for _, p := range cs.Paths {
			if o := outcomes[p.Path]; o != p.Outcome {
				if err := tx.RecordApplyFile(cs.ID, p.Path, o); err != nil {
					return err
				}
			}
		}
		return tx.RecordApplyRecovered(cs.ID)
	})
	if err != nil {
		return ApplyResult{}, err
	}
	return s.applyResult(cs.ID)
}

// UnfinishedApply names a change set whose latest apply began and neither
// finished nor was recovered, with its conversation's workspace, empty when
// the conversation has none.
type UnfinishedApply struct {
	ChangeSetID string
	TurnID      string
	Workspace   string
}

// UnfinishedApplies lists, in one query, the change sets with an unfinished
// apply: those whose latest control op is started.
func (s *Store) UnfinishedApplies() ([]UnfinishedApply, error) {
	var out []UnfinishedApply
	err := s.Tx(func(tx *Tx) error {
		rows, err := tx.tx.Query(`SELECT cs.id, cs.turn_id, COALESCE(c.workspace, '')
			FROM change_sets cs
			JOIN turns t ON t.id = cs.turn_id
			LEFT JOIN conversations c ON c.id = t.conversation_id
			WHERE (SELECT op_type FROM apply_ops WHERE change_set_id = cs.id AND op_type != 'file' ORDER BY id DESC LIMIT 1) = 'started'
			ORDER BY cs.id`)
		if err != nil {
			return fmt.Errorf("query unfinished applies: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var u UnfinishedApply
			if err := rows.Scan(&u.ChangeSetID, &u.TurnID, &u.Workspace); err != nil {
				return fmt.Errorf("scan unfinished apply: %w", err)
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}
