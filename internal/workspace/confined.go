package workspace

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

// Confined provides descriptor-relative file operations within a workspace root with confinement checks.
// It opens the workspace root once and all subsequent operations are relative to that descriptor.
type Confined struct {
	root     string
	rootfd   int
	patterns []*regexp.Regexp
}

// OpenConfined opens the workspace root and returns a Confined instance for safe path operations.
// The patterns list contains glob patterns for protected paths that cannot be read or written.
func OpenConfined(root string, patterns []string) (*Confined, error) {
	if root == "" {
		return nil, errors.New("workspace root path cannot be empty")
	}

	rootfd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}

	// Compile protected path patterns
	var regexps []*regexp.Regexp
	for _, pattern := range patterns {
		re, err := compileProtectionPattern(pattern)
		if err != nil {
			_ = unix.Close(rootfd)
			return nil, fmt.Errorf("compile protected path pattern %q: %w", pattern, err)
		}
		regexps = append(regexps, re)
	}

	return &Confined{
		root:     root,
		rootfd:   rootfd,
		patterns: regexps,
	}, nil
}

// Close closes the workspace root file descriptor.
func (c *Confined) Close() error {
	if c.rootfd >= 0 {
		return unix.Close(c.rootfd)
	}
	return nil
}

// OpenFile opens a file relative to the workspace root with confinement checks.
// It walks the path component by component using openat(2) with O_NOFOLLOW.
// Returns an error if the path contains symlinks, absolute components, or ".." traversals.
func (c *Confined) OpenFile(path string, flags int, perm fs.FileMode) (*os.File, error) {
	if err := c.validatePath(path); err != nil {
		return nil, err
	}

	if err := c.checkProtectedPaths(path); err != nil {
		return nil, err
	}

	// Normalize and split the path
	path = strings.TrimPrefix(path, "/")
	if path == "" || path == "." {
		return nil, errors.New("cannot open empty or root path")
	}

	components := strings.Split(path, "/")

	// Filter out empty and "." components
	var filteredComponents []string
	for _, component := range components {
		if component != "" && component != "." {
			filteredComponents = append(filteredComponents, component)
		}
	}

	if len(filteredComponents) == 0 {
		return nil, errors.New("path contains only empty or dot components")
	}

	components = filteredComponents

	// Walk the path component by component using openat with O_NOFOLLOW
	currentfd := c.rootfd
	openedfd := -1

	for i, component := range components {
		if component == ".." {
			if openedfd >= 0 && openedfd != c.rootfd {
				_ = unix.Close(openedfd)
			}
			return nil, fmt.Errorf("path traversal with ..: %q", path)
		}

		isLast := i == len(components)-1
		openFlags := unix.O_NOFOLLOW | unix.O_CLOEXEC

		if isLast {
			// Last component: apply the requested flags
			openFlags |= flags
			// Use O_NONBLOCK on read opens to prevent FIFOs from blocking
			if (flags & unix.O_WRONLY) == 0 {
				openFlags |= unix.O_NONBLOCK
			}
		} else {
			// Intermediate components: open as directory for further traversal
			openFlags |= unix.O_RDONLY | unix.O_DIRECTORY
		}

		fd, err := unix.Openat(currentfd, component, openFlags, uint32(perm))
		if err != nil {
			if openedfd >= 0 && openedfd != c.rootfd {
				_ = unix.Close(openedfd)
			}
			return nil, fmt.Errorf("open component %q: %w", component, err)
		}

		// Close the previous fd if it's not the root
		if openedfd >= 0 && openedfd != c.rootfd {
			_ = unix.Close(openedfd)
		}

		openedfd = fd
		currentfd = fd
	}

	if openedfd < 0 {
		return nil, errors.New("failed to open file")
	}

	// Validate that the opened file is a regular file (not directory, FIFO, device, etc.)
	var stat unix.Stat_t
	if err := unix.Fstat(openedfd, &stat); err != nil {
		_ = unix.Close(openedfd)
		return nil, fmt.Errorf("fstat opened file: %w", err)
	}

	// Check that it's a regular file
	if (stat.Mode & unix.S_IFMT) != unix.S_IFREG {
		_ = unix.Close(openedfd)
		return nil, fmt.Errorf("opened path is not a regular file: mode=0%o", stat.Mode)
	}

	return os.NewFile(uintptr(openedfd), path), nil
}

// ReadFile reads the entire contents of a file, checking confinement and protected paths.
func (c *Confined) ReadFile(path string) ([]byte, error) {
	f, err := c.OpenFile(path, unix.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	return io.ReadAll(f)
}

// WriteFile writes data to a file, checking confinement and protected paths.
// If the file exists, it will be truncated.
func (c *Confined) WriteFile(path string, data []byte, perm fs.FileMode) error {
	f, err := c.OpenFile(path, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	_, err = f.Write(data)
	return err
}

// Stat returns file information, checking confinement and protected paths.
func (c *Confined) Stat(path string) (fs.FileInfo, error) {
	f, err := c.OpenFile(path, unix.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	return f.Stat()
}

// validatePath checks that a path is relative and doesn't contain absolute components.
func (c *Confined) validatePath(path string) error {
	if filepath.IsAbs(path) {
		return fmt.Errorf("absolute path not allowed: %q", path)
	}

	// Check for .. at the beginning or after separators
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		if component == ".." {
			return fmt.Errorf("path traversal with ..: %q", path)
		}
	}

	return nil
}

// checkProtectedPaths validates that the path doesn't match any protected patterns.
func (c *Confined) checkProtectedPaths(path string) error {
	// Normalize path for comparison
	normalized := filepath.Clean(path)
	normalized = strings.TrimPrefix(normalized, "./")

	for _, pattern := range c.patterns {
		if pattern.MatchString(normalized) {
			return fmt.Errorf("path matches protected pattern: %q", path)
		}
	}

	return nil
}

// compileProtectionPattern converts glob patterns to regex patterns.
// Supports patterns like ".git/", ".env*", "secret/**", etc.
// ** matches zero or more directory components, * matches any chars except /,
// and ? matches a single char except /.
func compileProtectionPattern(pattern string) (*regexp.Regexp, error) {
	// Escape special regex characters except * and ?
	escaped := regexp.QuoteMeta(pattern)

	// Convert glob wildcards to regex in specific order:
	// First, handle **/ (zero or more path components with trailing slash)
	escaped = strings.ReplaceAll(escaped, `\*\*/`, `(?:.*/)?`)
	// Then, handle /** (matches anything including path separators)
	escaped = strings.ReplaceAll(escaped, `/\*\*`, `/.*`)
	// Then, remaining ** (matches anything including path separators)
	escaped = strings.ReplaceAll(escaped, `\*\*`, `.*`)
	// * matches any characters except /
	escaped = strings.ReplaceAll(escaped, `\*`, `[^/]*`)
	// ? matches a single character except /
	escaped = strings.ReplaceAll(escaped, `\?`, `[^/]`)

	// Anchor pattern appropriately
	if !strings.HasPrefix(pattern, "/") {
		// Pattern should match from the start or any directory
		escaped = `(^|/)` + escaped
	}

	// Add end anchor and handle directory matches
	if strings.HasSuffix(pattern, "/") {
		// Directory pattern like ".git/" - matches ".git", ".git/", ".git/anything"
		// Remove trailing / (already processed above) and match the directory and its contents
		escaped = strings.TrimSuffix(escaped, "/")
		escaped = escaped + `($|/.*)`
	} else {
		// Match the file/dir exactly or with trailing slash or as a directory
		escaped = escaped + `($|/.*)`
	}

	return regexp.Compile(escaped)
}
