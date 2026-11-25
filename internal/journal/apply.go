package journal

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
// If relPath is ".", returns wsRoot itself (duplicated).
func openAtNoFollow(wsRoot *os.File, relPath string) (*os.File, error) {
	if filepath.IsAbs(relPath) {
		return nil, fmt.Errorf("path must be relative: %q", relPath)
	}
	if relPath == ".." {
		return nil, fmt.Errorf("invalid path: %q", relPath)
	}
	if relPath == "." {
		// Return a duplicate of wsRoot fd for the current directory
		fd := int(wsRoot.Fd())
		newFd, err := unix.Dup(fd)
		if err != nil {
			return nil, fmt.Errorf("dup wsRoot fd: %w", err)
		}
		return os.NewFile(uintptr(newFd), "."), nil
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
// On first apply, every path must be pending (not already applied or in conflict).
// On retry, done paths are skipped, pending paths are written, conflicts reported.
// It journals apply_started at the beginning (or uses existing), apply_file for each path, and
// apply_finished at the end with the final state.
func (tx *Tx) ApplyPendingPaths(wsDir string, intent ApplyIntent, getContent func(sha256 string) ([]byte, error)) error {
	// Classify all paths first.
	outcomes, err := ClassifyPaths(wsDir, intent, getContent)
	if err != nil {
		return fmt.Errorf("classify paths: %w", err)
	}

	// Check if this is a first apply by looking at the apply ops in the database.
	cs, err := tx.GetChangeSet(intent.ChangeSetID)
	if err != nil {
		return fmt.Errorf("get change set: %w", err)
	}
	ops, err := tx.applyLog(cs.ID)
	if err != nil {
		return fmt.Errorf("get apply log: %w", err)
	}
	isFirstApply := len(ops) == 0 || (len(ops) == 1 && ops[0].OpType == "started")

	// First-apply check: on first apply, every path must be pending.
	if isFirstApply {
		for _, outcome := range outcomes {
			if outcome.Outcome != PathPending {
				return fmt.Errorf("cannot apply: path %q is in conflict (base may have changed)", outcome.Path)
			}
		}
	}

	// Open workspace root for the apply.
	wsRoot, err := os.Open(wsDir)
	if err != nil {
		return fmt.Errorf("open workspace root for apply: %w", err)
	}
	defer func() { _ = wsRoot.Close() }()

	// Apply each path, handling pending, applied and conflict appropriately.
	for _, outcome := range outcomes {
		path := intent.Paths[outcome.Path]

		if outcome.Outcome == PathApplied {
			// Already applied; on retry, skip writing and journal if not recorded.
			if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, PathApplied); err != nil {
				return fmt.Errorf("record apply file (already applied): %w", err)
			}
			continue
		}

		if outcome.Outcome == PathConflict {
			// On retry, report conflict and skip writing.
			if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, PathConflict); err != nil {
				return fmt.Errorf("record apply file conflict: %w", err)
			}
			continue
		}

		// PathPending: Ready to apply; write the file.
		newContent, err := getContent(path.NewSHA256)
		if err != nil {
			// Content not found; mark as conflict.
			if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, PathConflict); err != nil {
				return fmt.Errorf("record apply file error: %w", err)
			}
			continue
		}

		// Write to temporary file in target directory.
		fileOutcome := writeAndApplyFile(wsRoot, path.Path, path.BaseSHA256, path.Op, newContent)

		// Record the outcome in the journal.
		if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, fileOutcome); err != nil {
			return fmt.Errorf("record apply file: %w", err)
		}
	}

	// Finish the apply: computes final state from path outcomes.
	_, err = tx.FinishApply(intent.ChangeSetID)
	if err != nil {
		return fmt.Errorf("finish apply: %w", err)
	}

	return nil
}

// writeAndApplyFile writes newContent to the target path using confined access.
// For creates (baseSHA256 == ""), ensures the file doesn't exist then links it.
// For modifies, re-verifies the hash matches the base immediately before rename.
// Returns the outcome (applied or conflict). Never overwrites on failure.
func writeAndApplyFile(wsRoot *os.File, relPath string, baseSHA256 string, op string, newContent []byte) string {
	// Get the directory containing the target, relative to wsRoot.
	dir, name := filepath.Split(relPath)
	if dir == "" {
		dir = "."
	}

	// Open the target's parent directory through confined access from wsRoot.
	targetDir, err := openAtNoFollow(wsRoot, filepath.Clean(dir))
	if err != nil {
		return PathConflict
	}
	defer func() { _ = targetDir.Close() }()

	targetDirFd := int(targetDir.Fd())

	// Generate a unique temp file name in the target directory.
	// Use the same directory to ensure same-filesystem rename.
	randomBytes := make([]byte, 8)
	if _, err := rand.Read(randomBytes); err != nil {
		return PathConflict
	}
	tmpBaseName := fmt.Sprintf(".syndic_tmp_%s", hex.EncodeToString(randomBytes))
	tmpFd, err := unix.Openat(targetDirFd, tmpBaseName, unix.O_CREAT|unix.O_WRONLY|unix.O_EXCL, 0o600)
	if err != nil {
		return PathConflict
	}
	defer func() { _ = unix.Close(tmpFd) }()
	defer func() { _ = unix.Unlinkat(targetDirFd, tmpBaseName, 0) }()

	// Write content to temp file
	offset := 0
	for offset < len(newContent) {
		n, err := unix.Write(tmpFd, newContent[offset:])
		if err != nil {
			return PathConflict
		}
		if n <= 0 {
			return PathConflict
		}
		offset += n
	}

	// Sync before close to ensure durability
	if err := unix.Fsync(tmpFd); err != nil {
		return PathConflict
	}

	// Handle create vs modify
	if baseSHA256 == "" && op == "create" {
		// Create: link the temp file to the target name without replacement.
		// This fails if the target exists, which is what we want.
		err := unix.Linkat(targetDirFd, tmpBaseName, targetDirFd, name, 0)
		if err != nil {
			return PathConflict
		}
		// Successfully linked; now remove the temp file.
		_ = unix.Unlinkat(targetDirFd, tmpBaseName, 0)
		return PathApplied

	} else if op == "modify" {
		// Modify: file must exist and hash to base.
		// Re-open by name immediately before rename to catch atomic-save replacements.
		targetFd, err := unix.Openat(targetDirFd, name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
		if err != nil {
			return PathConflict
		}
		defer func() { _ = unix.Close(targetFd) }()

		// Verify the file still hashes to the base.
		actualHash, _, err := fileHashFd(targetFd)
		if err != nil || actualHash != baseSHA256 {
			return PathConflict
		}

		// Hash matches; safe to rename over it.
		// Close the target before renaming to avoid ETXTBSY on some systems.
		_ = unix.Close(targetFd)

		err = unix.Renameat(targetDirFd, tmpBaseName, targetDirFd, name)
		if err != nil {
			return PathConflict
		}
		return PathApplied
	}

	return PathConflict
}

// fileHashFd computes SHA-256 of a file opened via descriptor.
func fileHashFd(fd int) (string, int64, error) {
	h := sha256.New()
	var totalLen int64
	buf := make([]byte, 32*1024)
	for {
		n, err := unix.Read(fd, buf)
		if err != nil {
			return "", 0, fmt.Errorf("read file: %w", err)
		}
		if n == 0 {
			break
		}
		if _, err := h.Write(buf[:n]); err != nil {
			return "", 0, fmt.Errorf("hash write: %w", err)
		}
		totalLen += int64(n)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), totalLen, nil
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
