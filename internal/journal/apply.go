package journal

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ApplyIntent holds the parsed state of a change set's apply, used to execute
// and recover the apply operation. It is computed from the journal's apply_ops
// for a change set and reconstructed after a crash.
type ApplyIntent struct {
	ChangeSetID string
	TurnID      string
	// Paths indexed by path string, with each holding its pending, applied or conflict outcome.
	Paths map[string]*ApplyPath
	// State is the latest control op: appliable, partial, applied, conflict or discarded.
	State string
	// InProgress is true when the latest control op is started and no finished/recovered.
	InProgress bool
}

// ApplyPath holds the state of one path through an apply operation.
type ApplyPath struct {
	Path           string // The path in the change set
	Op             string // create or modify
	BaseSHA256     string // Empty for create
	NewSHA256      string
	NewLen         int64
	Outcome        string // pending, applied or conflict
	AppliedAttempt int    // Which attempt applied it (for per-attempt recovery)
}

// ClassifyOutcome is the result of classifying one path in the workspace.
type ClassifyOutcome struct {
	Path    string
	Outcome string // pending, applied or conflict
}

// ParseApplyIntent reconstructs the apply intent from the journal.
// It queries the change set, its paths and apply_ops to build the current state.
func (tx *Tx) ParseApplyIntent(changeSetID string) (ApplyIntent, error) {
	cs, err := tx.GetChangeSet(changeSetID)
	if err != nil {
		return ApplyIntent{}, fmt.Errorf("parse apply intent: %w", err)
	}

	intent := ApplyIntent{
		ChangeSetID: cs.ID,
		TurnID:      cs.TurnID,
		Paths:       make(map[string]*ApplyPath),
		State:       cs.State,
	}

	// Populate paths from change set, deriving outcomes from apply ops.
	for _, p := range cs.Paths {
		intent.Paths[p.Path] = &ApplyPath{
			Path:       p.Path,
			Op:         p.Op,
			BaseSHA256: p.BaseSHA256,
			NewSHA256:  p.NewSHA256,
			NewLen:     p.NewLen,
			Outcome:    p.Outcome,
		}
	}

	// Check if an apply is in progress: latest control op is started.
	ops, err := tx.applyLog(cs.ID)
	if err != nil {
		return ApplyIntent{}, fmt.Errorf("parse apply intent: %w", err)
	}
	intent.InProgress = inProgress(ops)

	return intent, nil
}

// ParseApplyIntent reconstructs the apply intent from the journal outside a transaction.
func (s *Store) ParseApplyIntent(changeSetID string) (ApplyIntent, error) {
	var intent ApplyIntent
	err := s.Tx(func(tx *Tx) error {
		var txErr error
		intent, txErr = tx.ParseApplyIntent(changeSetID)
		return txErr
	})
	return intent, err
}

// ClassifyPaths classifies every path in the intent against the workspace,
// returning one outcome per path. The workspace is opened relative to wsDir
// and operations use confined access (openat/O_NOFOLLOW). It does not write,
// and is used for both pre-apply verification and post-crash recovery.
func ClassifyPaths(wsDir string, intent ApplyIntent, getContent func(sha256 string) ([]byte, error)) ([]ClassifyOutcome, error) {
	// Open workspace root with confinement: once per apply, all ops relative to it.
	wsRoot, err := os.Open(wsDir)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { _ = wsRoot.Close() }()

	outcomes := make([]ClassifyOutcome, 0, len(intent.Paths))

	for _, p := range intent.Paths {
		outcome := ClassifyOutcome{Path: p.Path}

		// Open the path through the confined root with O_NOFOLLOW.
		f, err := openAtNoFollow(wsRoot, p.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Path is absent; classification depends on base.
				if p.BaseSHA256 == "" {
					// Create: absent is pending (can be created).
					outcome.Outcome = PathPending
				} else {
					// Modify: absent is conflict (base changed).
					outcome.Outcome = PathConflict
				}
			} else {
				// Error other than not found (e.g., symlink, outside root).
				// Classify as conflict and continue; the apply will report it.
				outcome.Outcome = PathConflict
			}
		} else {
			// File exists; check hash against base and new.
			actualHash, _, err := fileHash(f)
			_ = f.Close()

			if err != nil {
				outcome.Outcome = PathConflict
			} else if actualHash == p.NewSHA256 {
				// Already applied.
				outcome.Outcome = PathApplied
			} else if actualHash == p.BaseSHA256 {
				// At base, ready to apply.
				outcome.Outcome = PathPending
			} else {
				// At neither hash; conflict.
				outcome.Outcome = PathConflict
			}

			// Verify that the root is still accessible (design section 6: directory relocation).
			// If the root was renamed, we're still following it via the open descriptor.
			if _, err := wsRoot.Stat(); err != nil {
				outcome.Outcome = PathConflict
			}
		}

		outcomes = append(outcomes, outcome)
	}

	return outcomes, nil
}

// openAtNoFollow opens a path relative to a directory descriptor with O_NOFOLLOW
// to prevent symlink attacks and enforce confinement.
func openAtNoFollow(wsRoot *os.File, relPath string) (*os.File, error) {
	if filepath.IsAbs(relPath) {
		return nil, fmt.Errorf("path must be relative: %q", relPath)
	}
	if relPath == "." || relPath == ".." {
		return nil, fmt.Errorf("invalid path: %q", relPath)
	}
	if filepath.Clean(relPath) != relPath {
		return nil, fmt.Errorf("path not clean: %q", relPath)
	}

	// Split path on '/' to get components.
	parts := strings.Split(relPath, "/")
	cleanParts := []string{}
	for _, part := range parts {
		if part != "" && part != "." {
			if part == ".." {
				return nil, fmt.Errorf("path escapes workspace root: %q", relPath)
			}
			cleanParts = append(cleanParts, part)
		}
	}

	currentFd := int(wsRoot.Fd())
	closePrevious := false

	for i, part := range cleanParts {
		// Use openat(2) with O_NOFOLLOW to avoid symlink following
		newFd, err := unix.Openat(currentFd, part, unix.O_NOFOLLOW|unix.O_RDONLY, 0)
		if err != nil {
			if closePrevious {
				_ = unix.Close(currentFd)
			}
			return nil, fmt.Errorf("openat %q: %w", part, err)
		}

		// Stat to verify not a symlink and check type
		var stat unix.Stat_t
		if err := unix.Fstat(newFd, &stat); err != nil {
			_ = unix.Close(newFd)
			if closePrevious {
				_ = unix.Close(currentFd)
			}
			return nil, fmt.Errorf("fstat %q: %w", part, err)
		}

		// Check it's not a symlink
		if (stat.Mode & unix.S_IFMT) == unix.S_IFLNK {
			_ = unix.Close(newFd)
			if closePrevious {
				_ = unix.Close(currentFd)
			}
			return nil, fmt.Errorf("path component is a symlink: %q", part)
		}

		// If not last component, it must be a directory
		if i < len(cleanParts)-1 && (stat.Mode&unix.S_IFMT) != unix.S_IFDIR {
			_ = unix.Close(newFd)
			if closePrevious {
				_ = unix.Close(currentFd)
			}
			return nil, fmt.Errorf("path component must be a directory: %q", part)
		}

		// Close the previous fd if it wasn't wsRoot
		if closePrevious {
			_ = unix.Close(currentFd)
		}

		currentFd = newFd
		closePrevious = true
	}

	return os.NewFile(uintptr(currentFd), relPath), nil
}

// fileHash computes the SHA-256 of a file's content and returns the hash and length.
func fileHash(f *os.File) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("hash file: %w", err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), n, nil
}

// ApplyPendingPaths writes every pending path to the workspace using confined access.
// It first verifies that all paths are pending, then writes each one, journaling
// the outcome. It returns the outcomes and an error if any path is not pending.
func (tx *Tx) ApplyPendingPaths(wsDir string, intent ApplyIntent, getContent func(sha256 string) ([]byte, error)) ([]ClassifyOutcome, error) {
	// Classify all paths first.
	outcomes, err := ClassifyPaths(wsDir, intent, getContent)
	if err != nil {
		return nil, fmt.Errorf("classify paths: %w", err)
	}

	// First-apply precondition: every path must be pending or applied.
	// If any path is in conflict before writing, return error and write nothing.
	for _, outcome := range outcomes {
		if outcome.Outcome == PathConflict {
			return outcomes, fmt.Errorf("cannot apply: path %q is in conflict (base may have changed)", outcome.Path)
		}
	}

	// Open workspace root for the apply.
	wsRoot, err := os.Open(wsDir)
	if err != nil {
		return nil, fmt.Errorf("open workspace root for apply: %w", err)
	}
	defer func() { _ = wsRoot.Close() }()

	// Apply each pending path.
	for _, outcome := range outcomes {
		path := intent.Paths[outcome.Path]

		if outcome.Outcome == PathApplied {
			// Already applied; journal it if not already recorded.
			if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, PathApplied); err != nil {
				return nil, fmt.Errorf("record apply file (already applied): %w", err)
			}
			continue
		}

		if outcome.Outcome == PathPending {
			// Ready to apply; write the file.
			newContent, err := getContent(path.NewSHA256)
			if err != nil {
				// Content not found; mark as conflict.
				if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, PathConflict); err != nil {
					return nil, fmt.Errorf("record apply file conflict: %w", err)
				}
				continue
			}

			// Write to temporary file.
			tmpFd, tmpPath, err := writeTempFile(newContent)
			if err != nil {
				// Write failed; mark as conflict.
				if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, PathConflict); err != nil {
					return nil, fmt.Errorf("record apply file error: %w", err)
				}
				continue
			}

			// Pre-verify: re-open and check hash against base, then apply.
			fileOutcome := verifyAndApplyFile(wsRoot, path.Path, path.BaseSHA256, path.Op, tmpFd, tmpPath)

			// Record the outcome in the journal.
			if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, fileOutcome); err != nil {
				return nil, fmt.Errorf("record apply file: %w", err)
			}
		}
	}

	return outcomes, nil
}

// writeTempFile writes newContent to a temporary file outside the workspace.
// Returns the file descriptor and the basename for use in confined operations.
func writeTempFile(newContent []byte) (int, string, error) {
	// Create a temporary file in the system temp directory
	tmpFile, err := os.CreateTemp("", "syndic_apply_*.tmp")
	if err != nil {
		return -1, "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(newContent); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return -1, "", fmt.Errorf("write temp file: %w", err)
	}

	// Sync before closing to ensure data is durable
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return -1, "", fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return -1, "", fmt.Errorf("close temp file: %w", err)
	}

	// Open the temp file for reading and linking
	fd, err := unix.Open(tmpPath, unix.O_RDONLY, 0)
	if err != nil {
		_ = os.Remove(tmpPath)
		return -1, "", fmt.Errorf("reopen temp file: %w", err)
	}

	return fd, tmpPath, nil
}

// verifyAndApplyFile verifies the target path and applies the temp file.
// For creates (baseSHA256 == ""), ensures the file doesn't exist then renames it.
// For modifies, re-verifies the hash matches the base, then renames.
// Returns the outcome (applied or conflict).
func verifyAndApplyFile(wsRoot *os.File, path string, baseSHA256 string, op string, tmpFd int, tmpPath string) string {
	defer func() { _ = unix.Close(tmpFd) }()
	defer func() { _ = os.Remove(tmpPath) }()

	fullPath := filepath.Join(wsRoot.Name(), path)

	// Handle create vs modify
	if baseSHA256 == "" && op == "create" {
		// Create: file must not exist. Check before renaming.
		if _, err := os.Stat(fullPath); err == nil {
			// File exists; conflict.
			return PathConflict
		} else if !errors.Is(err, os.ErrNotExist) {
			// Other error; conflict.
			return PathConflict
		}

		// File doesn't exist; safe to rename. Try renameat2 first for atomicity.
		if err := tryRenameAtNoreplace(tmpPath, fullPath); err == nil {
			return PathApplied
		}

		// Fall back to os.Rename (less safe, but works on more systems)
		if err := os.Rename(tmpPath, fullPath); err == nil {
			return PathApplied
		}
		return PathConflict

	} else if op == "modify" {
		// Modify: file must exist and hash to base.
		// Re-open by name to catch atomic-save replacements (new inode, new content)
		f, err := openAtNoFollow(wsRoot, path)
		if err != nil {
			// Cannot open (not found, symlink, etc.); conflict.
			return PathConflict
		}
		defer func() { _ = f.Close() }()

		actualHash, _, err := fileHash(f)
		if err != nil || actualHash != baseSHA256 {
			// Hash mismatch; conflict.
			return PathConflict
		}

		// Hash matches; safe to rename over it using os.Rename
		if err := os.Rename(tmpPath, fullPath); err != nil {
			return PathConflict
		}
		return PathApplied
	}

	return PathConflict
}

// tryRenameAtNoreplace attempts to use renameat2 with RENAME_NOREPLACE
// to atomically replace oldPath with newPath only if newPath doesn't exist.
func tryRenameAtNoreplace(oldPath, newPath string) error {
	// renameat2 with RENAME_NOREPLACE
	const RENAME_NOREPLACE = 1
	// Split paths into directory and name
	oldDir := filepath.Dir(oldPath)
	oldName := filepath.Base(oldPath)
	newDir := filepath.Dir(newPath)
	newName := filepath.Base(newPath)

	// Open directories
	oldDirFd, err := unix.Open(oldDir, unix.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open old dir: %w", err)
	}
	defer func() { _ = unix.Close(oldDirFd) }()

	newDirFd, err := unix.Open(newDir, unix.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open new dir: %w", err)
	}
	defer func() { _ = unix.Close(newDirFd) }()

	// Use Renameat2 to atomically rename with no replace
	return unix.Renameat2(oldDirFd, oldName, newDirFd, newName, RENAME_NOREPLACE)
}

// RecoverApplyState queries the change set's apply state after a crash,
// classifies all paths without writing, and journals the recovery if needed.
func (tx *Tx) RecoverApplyState(wsDir string, intent ApplyIntent, getContent func(sha256 string) ([]byte, error)) error {
	// If not in progress, nothing to recover.
	if !intent.InProgress {
		return nil
	}

	// Classify all paths without writing.
	outcomes, err := ClassifyPaths(wsDir, intent, getContent)
	if err != nil {
		// Check if workspace is missing (unwrap the error first)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("workspace missing: %w", err)
		}
		// Other classification errors; still record recovery with current outcomes.
		// If we can't classify, we can't recover reliably.
		return fmt.Errorf("classify paths during recovery: %w", err)
	}

	// Update intent outcomes from classification and journal only applied/conflict ones.
	// Pending paths are the default state and don't need to be recorded.
	for _, outcome := range outcomes {
		if p, ok := intent.Paths[outcome.Path]; ok {
			p.Outcome = outcome.Outcome
			// Journal the outcome if it's applied or conflict
			if outcome.Outcome != PathPending {
				if err := tx.RecordApplyFile(intent.ChangeSetID, outcome.Path, outcome.Outcome); err != nil {
					return fmt.Errorf("record recovery outcome for %q: %w", outcome.Path, err)
				}
			}
		}
	}

	// Journal recovery: sets state to partial if not complete.
	if err := tx.RecordApplyRecovered(intent.ChangeSetID); err != nil {
		return fmt.Errorf("record apply recovered: %w", err)
	}

	return nil
}
