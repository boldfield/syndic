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

	// afterComponent, when non-nil, runs after component i of a walk has been
	// opened and before the next openat. Tests use it to swap path components
	// at a deterministic point inside the walk.
	afterComponent func(i int, name string)
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
	fd, path, err := c.openWalk(path, flags, perm, false)
	if err != nil {
		return nil, err
	}

	// Validate that the opened file is a regular file (not directory, FIFO, device, etc.)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("fstat opened file: %w", err)
	}

	// Check that it's a regular file
	if (stat.Mode & unix.S_IFMT) != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("opened path is not a regular file: mode=0%o", stat.Mode)
	}

	return os.NewFile(uintptr(fd), path), nil
}

// openWalk checks path and opens its last component relative to the root,
// walking one component at a time with O_NOFOLLOW. The last component is
// opened with flags and whatever file type it has; callers check the type. It
// returns the descriptor and the normalized path. With allowRoot, a path that
// names the root itself opens the root directory.
func (c *Confined) openWalk(path string, flags int, perm fs.FileMode, allowRoot bool) (int, string, error) {
	if err := c.validatePath(path); err != nil {
		return -1, "", err
	}

	if err := c.checkProtectedPaths(path); err != nil {
		return -1, "", err
	}

	// Normalize and split the path
	path = strings.TrimPrefix(path, "/")
	if !allowRoot && (path == "" || path == ".") {
		return -1, "", errors.New("cannot open empty or root path")
	}

	// Filter out empty and "." components
	var components []string
	for _, component := range strings.Split(path, "/") {
		if component != "" && component != "." {
			components = append(components, component)
		}
	}

	if len(components) == 0 {
		if !allowRoot {
			return -1, "", errors.New("path contains only empty or dot components")
		}
		fd, err := unix.Openat(c.rootfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return -1, "", fmt.Errorf("open workspace root: %w", err)
		}
		return fd, ".", nil
	}

	// Walk the path component by component using openat with O_NOFOLLOW
	currentfd := c.rootfd
	openedfd := -1

	for i, component := range components {
		if component == ".." {
			if openedfd >= 0 {
				_ = unix.Close(openedfd)
			}
			return -1, "", fmt.Errorf("path traversal with ..: %q", path)
		}

		isLast := i == len(components)-1
		openFlags := unix.O_NOFOLLOW | unix.O_CLOEXEC

		if isLast {
			// Last component: apply the requested flags
			openFlags |= flags
			// Use O_NONBLOCK on all final opens to prevent FIFOs from blocking
			openFlags |= unix.O_NONBLOCK
		} else {
			// Intermediate components: open as directory for further traversal
			openFlags |= unix.O_RDONLY | unix.O_DIRECTORY
		}

		fd, err := unix.Openat(currentfd, component, openFlags, uint32(perm))
		if err != nil {
			err = c.walkError(currentfd, component, isLast, err)
			if openedfd >= 0 {
				_ = unix.Close(openedfd)
			}
			return -1, "", err
		}

		// Close the previous fd if it's not the root
		if openedfd >= 0 {
			_ = unix.Close(openedfd)
		}

		openedfd = fd
		currentfd = fd

		if c.afterComponent != nil {
			c.afterComponent(i, component)
		}
	}

	return openedfd, strings.Join(components, "/"), nil
}

// walkError describes the failure to open component relative to dirfd.
func (c *Confined) walkError(dirfd int, component string, isLast bool, err error) error {
	if isLast && errors.Is(err, unix.ELOOP) {
		// O_NOFOLLOW refused the last component: it is a symbolic link.
		return fmt.Errorf("open component %q: %w: %w", component, ErrSymlink, err)
	}
	if !isLast && (errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP)) {
		// O_DIRECTORY|O_NOFOLLOW refuses a symbolic link with ENOTDIR, as it
		// does a regular file; only the link is a refusal.
		var st unix.Stat_t
		if unix.Fstatat(dirfd, component, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return fmt.Errorf("open component %q: %w: %w", component, ErrSymlinkOnPath, err)
		}
	}
	return fmt.Errorf("open component %q: %w", component, err)
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
	if strings.HasPrefix(pattern, "/") {
		// Root-anchored pattern: strip leading / and anchor at start
		escaped = strings.TrimPrefix(escaped, `/`)
		escaped = `^` + escaped
	} else {
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

// DirEntry is one name in a directory. Type is "f" for a regular file, "d" for
// a directory, "l" for a symbolic link and "o" for anything else.
type DirEntry struct {
	Name string
	Type string
}

// Examination is what a path holds right now: a directory's entries, or a
// regular file's content.
type Examination struct {
	IsDir   bool
	Entries []DirEntry
	Content []byte
	// OwnerExec is a regular file's owner-execute bit, from the same fstat
	// as Content. Setuid, setgid and sticky are not reported.
	OwnerExec bool
}

// ErrNotRegular is returned by Examine for a path that is neither a regular
// file nor a directory.
var ErrNotRegular = errors.New("not a regular file or directory")

// ErrSymlink is returned (wrapping ELOOP) when the last component of a path is
// a symbolic link, which the confined walk never follows.
var ErrSymlink = errors.New("path is a symbolic link")

// ErrSymlinkOnPath is returned (wrapping the open error) when a component
// before the last is a symbolic link: the walk refuses to follow it, and the
// path is neither present nor absent.
var ErrSymlinkOnPath = errors.New("path has a symbolic link on it")

// IsProtected reports whether path matches a protected pattern.
func (c *Confined) IsProtected(path string) bool {
	return c.checkProtectedPaths(path) != nil
}

// Examine opens path through the same confined walk as OpenFile and returns
// its content (regular file) or its entries (directory; "." is the root).
// Entries that match a protected pattern are left out. A missing path matches
// fs.ErrNotExist, and so does a path below something that is not a directory.
func (c *Confined) Examine(path string) (Examination, error) {
	fd, norm, err := c.openWalk(path, unix.O_RDONLY, 0, true)
	if err != nil {
		return Examination{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return Examination{}, fmt.Errorf("fstat %q: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), norm)
	defer func() { _ = f.Close() }()

	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		content, err := io.ReadAll(f)
		if err != nil {
			return Examination{}, fmt.Errorf("read %q: %w", path, err)
		}
		return Examination{Content: content, OwnerExec: stat.Mode&unix.S_IXUSR != 0}, nil
	case unix.S_IFDIR:
		names, err := f.Readdirnames(-1)
		if err != nil {
			return Examination{}, fmt.Errorf("list %q: %w", path, err)
		}
		ex := Examination{IsDir: true}
		for _, name := range names {
			child := name
			if norm != "." {
				child = norm + "/" + name
			}
			if c.IsProtected(child) {
				continue
			}
			var st unix.Stat_t
			if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue // removed while listing
				}
				return Examination{}, fmt.Errorf("stat entry %q: %w", child, err)
			}
			typ := "o"
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFREG:
				typ = "f"
			case unix.S_IFDIR:
				typ = "d"
			case unix.S_IFLNK:
				typ = "l"
			}
			ex.Entries = append(ex.Entries, DirEntry{Name: name, Type: typ})
		}
		return ex, nil
	}
	return Examination{}, fmt.Errorf("%q: %w", path, ErrNotRegular)
}
