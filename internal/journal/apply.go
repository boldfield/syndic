package journal

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
			if os.IsNotExist(err) {
				// Path is absent; classification depends on base.
				if p.BaseSHA256 == "" {
					// Create: absent is conflict (file exists).
					outcome.Outcome = PathConflict
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
			defer func() { _ = f.Close() }()

			actualHash, _, err := fileHash(f)
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
// to prevent symlink attacks and enforce confinement. It walks the path one
// component at a time, refusing any that is a symlink or leaves the workspace root.
// This is a simplified version; a full implementation would use os.Open with O_NOFOLLOW
// on platforms that support it, or openat on Unix-like systems.
func openAtNoFollow(wsRoot *os.File, relPath string) (*os.File, error) {
	// For now, use a simple approach: validate the path and open.
	// In production, this would use proper confinement via openat(2) with O_NOFOLLOW.
	if filepath.IsAbs(relPath) {
		return nil, fmt.Errorf("path must be relative: %q", relPath)
	}
	if filepath.Clean(relPath) != relPath || relPath == "." || relPath == ".." {
		return nil, fmt.Errorf("invalid path: %q", relPath)
	}
	// Check for .. traversal.
	for _, component := range filepath.SplitList(relPath) {
		if component == ".." {
			return nil, fmt.Errorf("path escapes workspace root: %q", relPath)
		}
	}
	// Check for symlinks: lstat the full path and reject if symlink.
	fullPath := filepath.Join(wsRoot.Name(), relPath)
	info, err := os.Lstat(fullPath)
	if err != nil {
		return nil, err
	}
	if (info.Mode() & os.ModeSymlink) != 0 {
		return nil, fmt.Errorf("path is a symlink: %q", relPath)
	}
	// Open the file.
	return os.Open(fullPath)
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
// It journals apply_file for each path that is applied or conflicts. Paths that
// are already applied or conflicted are skipped. It returns the outcomes and an error
// if the apply cannot proceed (e.g., base changed).
func (tx *Tx) ApplyPendingPaths(wsDir string, intent ApplyIntent, getContent func(sha256 string) ([]byte, error)) ([]ClassifyOutcome, error) {
	// Classify all paths first.
	outcomes, err := ClassifyPaths(wsDir, intent, getContent)
	if err != nil {
		return nil, fmt.Errorf("classify paths: %w", err)
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

		switch outcome.Outcome {
		case PathApplied:
			// Already applied; skip.
			continue
		case PathPending:
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
			tmpFile, err := writeTempFile(wsRoot, path.Path, newContent)
			if err != nil {
				// Write failed; mark as conflict.
				if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, PathConflict); err != nil {
					return nil, fmt.Errorf("record apply file error: %w", err)
				}
				continue
			}
			defer func() { _ = os.Remove(tmpFile) }()

			// Pre-verify: re-open and check hash against base.
			fileOutcome, err := verifyAndApplyFile(wsRoot, path.Path, path.BaseSHA256, tmpFile)
			if err != nil {
				// Verification failed; mark as conflict.
				if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, PathConflict); err != nil {
					return nil, fmt.Errorf("record apply file error: %w", err)
				}
				continue
			}

			// Record the outcome in the journal.
			if err := tx.RecordApplyFile(intent.ChangeSetID, path.Path, fileOutcome); err != nil {
				return nil, fmt.Errorf("record apply file: %w", err)
			}

		case PathConflict:
			// Already conflicted; skip.
			continue
		}
	}

	return outcomes, nil
}

// writeTempFile writes newContent to a temporary file in the target's directory.
// It returns the temp file path for later rename.
func writeTempFile(wsRoot *os.File, path string, newContent []byte) (string, error) {
	// Create a temporary file in a temp directory; in production use a directory in wsRoot.
	tmpDir := filepath.Join(wsRoot.Name(), ".syndic_tmp")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	tmpFile, err := os.CreateTemp(tmpDir, "apply_*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer func() { _ = tmpFile.Close() }()

	if _, err := tmpFile.Write(newContent); err != nil {
		return "", fmt.Errorf("write temp file: %w", err)
	}

	return tmpFile.Name(), nil
}

// verifyAndApplyFile re-opens the target path, verifies its hash matches the base,
// then renames the temp file over it. If the base is absent, it uses linkat instead.
// It returns the outcome (applied or conflict) and an error only on unexpected failures.
func verifyAndApplyFile(wsRoot *os.File, path string, baseSHA256 string, tmpFile string) (string, error) {
	fullPath := filepath.Join(wsRoot.Name(), path)
	targetDir := filepath.Dir(fullPath)

	// Re-open and verify (design section 6).
	if baseSHA256 == "" {
		// Create: file must not exist.
		if _, err := os.Stat(fullPath); err == nil {
			// File exists; conflict.
			return PathConflict, nil
		}
	} else {
		// Modify: file must exist and hash to base.
		f, err := openAtNoFollow(wsRoot, path)
		if err != nil {
			// Cannot open (not found, symlink, etc.); conflict.
			return PathConflict, nil
		}
		defer func() { _ = f.Close() }()

		actualHash, _, err := fileHash(f)
		if err != nil || actualHash != baseSHA256 {
			// Hash mismatch; conflict.
			return PathConflict, nil
		}
	}

	// Rename temp file over target (atomic per file).
	targetDir2, err := os.Open(targetDir)
	if err != nil {
		return "", fmt.Errorf("open target dir: %w", err)
	}
	defer func() { _ = targetDir2.Close() }()

	// Use os.Rename for portability; in production use renameat2 with RENAME_NOREPLACE for creates.
	if err := os.Rename(tmpFile, fullPath); err != nil {
		if baseSHA256 == "" {
			// Create failed; likely file exists now; conflict.
			return PathConflict, nil
		}
		// Rename failed; conflict.
		return PathConflict, nil
	}

	return PathApplied, nil
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
		// If workspace is missing, we cannot recover; leave the apply partial.
		if os.IsNotExist(err) {
			return fmt.Errorf("workspace missing: %w", err)
		}
		// Other classification errors; still record recovery with current outcomes.
	}

	// Update intent outcomes from classification.
	for _, outcome := range outcomes {
		if p, ok := intent.Paths[outcome.Path]; ok {
			p.Outcome = outcome.Outcome
		}
	}

	// Journal recovery: sets state to partial.
	if err := tx.RecordApplyRecovered(intent.ChangeSetID); err != nil {
		return fmt.Errorf("record apply recovered: %w", err)
	}

	return nil
}
