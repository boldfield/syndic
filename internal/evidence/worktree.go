package evidence

import (
	"os"
	"path/filepath"
	"strings"
)

// InsideGitWorktree reports whether path, or the nearest existing ancestor of
// it, lies inside a git worktree, so a raw record written there could be
// committed. A .git it cannot inspect counts as a worktree.
func InsideGitWorktree(path string) bool {
	// Find the nearest existing ancestor and evaluate symlinks
	current := path
	for {
		// Check if this path exists
		if _, err := os.Stat(current); err == nil {
			// Found an existing path, convert to absolute path before symlink evaluation
			absPath, err := filepath.Abs(current)
			if err != nil {
				absPath = current
			}
			// Evaluate symlinks
			resolved, err := filepath.EvalSymlinks(absPath)
			if err != nil {
				// If we can't resolve, continue with absolute path
				resolved = absPath
			}
			// Walk up from the resolved path
			checkPath := resolved
			for {
				gitPath := filepath.Join(checkPath, ".git")
				if isRealGitRepository(gitPath) {
					return true
				}
				parent := filepath.Dir(checkPath)
				if parent == checkPath {
					break
				}
				checkPath = parent
			}
			return false
		}

		// Try parent directory
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root
			break
		}
		current = parent
	}
	return false
}

func isRealGitRepository(gitPath string) bool {
	// Try to stat the path
	fi, err := os.Stat(gitPath)
	if err != nil {
		if os.IsNotExist(err) {
			// .git doesn't exist, not a repository
			return false
		}
		// Can't inspect .git, fail closed - treat as worktree
		return true
	}

	if fi.IsDir() {
		// It's a directory. Check if it contains a HEAD regular file.
		headPath := filepath.Join(gitPath, "HEAD")
		fi, err := os.Stat(headPath)
		if err != nil {
			if os.IsNotExist(err) {
				// HEAD doesn't exist
				return false
			}
			// Can't inspect HEAD, fail closed
			return true
		}
		// HEAD exists and is a regular file, it's a repository
		return fi.Mode().IsRegular()
	}
	// It's a file (or symlink, etc). Check if it's a regular file containing "gitdir:" after leading whitespace.
	if !fi.Mode().IsRegular() {
		// Not a regular file (could be FIFO, socket, etc), ignore it
		return false
	}
	content, err := os.ReadFile(gitPath)
	if err != nil {
		// Can't read the file, fail closed
		return true
	}
	contentStr := strings.TrimSpace(string(content))
	return strings.HasPrefix(contentStr, "gitdir:")
}
