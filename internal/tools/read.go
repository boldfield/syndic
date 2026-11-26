package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
	"golang.org/x/sys/unix"
)

// ReadFileArgs are the arguments to the read_file tool.
type ReadFileArgs struct {
	Path   string `json:"path"`
	Offset *int64 `json:"offset,omitempty"`
	Limit  *int64 `json:"limit,omitempty"`
}

// ListDirArgs are the arguments to the list_dir tool.
type ListDirArgs struct {
	Path string `json:"path"`
}

// ReadResult is the result of a successful read_file call.
type ReadResult struct {
	Content string `json:"content"`
}

// ListResult is the result of a successful list_dir call.
type ListResult struct {
	Entries []DirEntry `json:"entries"`
}

// DirEntry is one entry in a directory listing.
type DirEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SizeErrorResult is the error result when a file is too large to read in one call.
type SizeErrorResult struct {
	Error string `json:"error"`
	Size  int64  `json:"size"`
}

// ReadFile executes the read_file tool. It reads a file from the workspace,
// observing it through the snapshot store, and returns its content. If offset
// and limit are provided, it reads only that range. If the file is larger than
// the max result size and no range is given, it returns a size error.
func ReadFile(
	ctx ReadFileContext,
	args ReadFileArgs,
	staging map[string]string, // path -> new_sha256 for staged changes
	maxResultBytes int64,
) (json.RawMessage, error) {
	// Normalize and validate the path.
	path := filepath.Clean(args.Path)
	if filepath.IsAbs(path) || strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("invalid path: %q", args.Path)
	}

	// Check offset and limit.
	offset := int64(0)
	if args.Offset != nil {
		offset = *args.Offset
		if offset < 0 {
			return nil, errors.New("offset must be non-negative")
		}
	}
	limit := int64(maxResultBytes)
	if args.Limit != nil {
		if *args.Limit < 1 {
			return nil, errors.New("limit must be at least 1")
		}
		limit = *args.Limit
	}

	// Check if the file is staged.
	if newSHA, ok := staging[path]; ok {
		return readStaged(ctx, path, newSHA, offset, limit, maxResultBytes)
	}

	// Observe the file through the snapshot.
	reader := func() (string, []byte, error) {
		return readLiveFile(ctx.Confined, path)
	}
	obs, err := ctx.ObserveStore.Observe(ctx.TurnID, ctx.AttemptID, path, reader)
	if err != nil {
		return nil, fmt.Errorf("observe %q: %w", path, err)
	}

	if obs.Kind == journal.ObservationAbsent {
		return nil, fmt.Errorf("file not found")
	}
	if obs.Kind != journal.ObservationFile {
		return nil, fmt.Errorf("path is not a file")
	}

	// Get the file content from the content store.
	content, err := ctx.ContentStore.Content(obs.ContentSHA256)
	if err != nil {
		return nil, fmt.Errorf("get file content: %w", err)
	}

	// Check if the file is too large and no range is requested.
	if int64(len(content)) > maxResultBytes && offset == 0 && limit == maxResultBytes {
		result := SizeErrorResult{
			Error: "file too large",
			Size:  int64(len(content)),
		}
		data, _ := json.Marshal(result)
		return data, nil
	}

	// Handle ranged reads.
	if offset > 0 || limit < int64(len(content)) {
		return rangedRead(content, offset, limit)
	}

	return json.Marshal(ReadResult{Content: string(content)})
}

// ListDirFunc executes the list_dir tool. It lists the entries in a directory,
// observing the directory through the snapshot store.
func ListDirFunc(
	ctx ReadFileContext,
	args ListDirArgs,
	staging map[string]string, // path -> new_sha256 for staged changes
) (json.RawMessage, error) {
	// Normalize and validate the path.
	path := filepath.Clean(args.Path)
	if filepath.IsAbs(path) || strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("invalid path: %q", args.Path)
	}
	if path == "." || path == "" {
		path = observation.Root
	}

	// Observe the directory through the snapshot.
	reader := func() (string, []byte, error) {
		return readLiveDir(ctx.Confined, path)
	}
	obs, err := ctx.ObserveStore.Observe(ctx.TurnID, ctx.AttemptID, path, reader)
	if err != nil {
		return nil, fmt.Errorf("observe %q: %w", path, err)
	}

	if obs.Kind == journal.ObservationAbsent {
		return nil, fmt.Errorf("directory not found")
	}
	if obs.Kind != journal.ObservationListing {
		return nil, fmt.Errorf("path is not a directory")
	}

	// Parse the directory entries from the content store.
	listingData, err := ctx.ContentStore.Content(obs.ContentSHA256)
	if err != nil {
		return nil, fmt.Errorf("get directory listing: %w", err)
	}

	entries, err := observation.ParseEntries(listingData)
	if err != nil {
		return nil, fmt.Errorf("parse directory entries: %w", err)
	}

	// Convert to the result format.
	result := ListResult{Entries: make([]DirEntry, 0, len(entries))}
	for _, e := range entries {
		result.Entries = append(result.Entries, DirEntry{Name: e.Name, Type: e.Type})
	}

	// Apply staged changes.
	result.Entries = applyStagedChanges(result.Entries, path, staging)

	return json.Marshal(result)
}

// ReadFileContext provides the dependencies for read and list operations.
type ReadFileContext struct {
	TurnID       string
	AttemptID    string
	Confined     *workspace.Confined
	ObserveStore *observation.Store
	ContentStore interface {
		Content(sha256 string) ([]byte, error)
	}
}

// readLiveFile reads a file from the live workspace.
func readLiveFile(conf *workspace.Confined, path string) (string, []byte, error) {
	content, err := conf.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return journal.ObservationAbsent, nil, nil
		}
		return "", nil, err
	}
	return journal.ObservationFile, content, nil
}

// readLiveDir reads a directory from the live workspace and returns the
// canonical entry list.
func readLiveDir(conf *workspace.Confined, dirPath string) (string, []byte, error) {
	// For the workspace root, convert "." to observation.Root
	if dirPath == observation.Root {
		dirPath = "."
	}

	// Open the directory descriptor for safe listing.
	f, err := conf.ListDir(dirPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return journal.ObservationAbsent, nil, nil
		}
		return "", nil, err
	}
	defer func() { _ = f.Close() }()

	// Get the FileInfo to check it's a directory.
	info, err := f.Stat()
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return journal.ObservationFile, nil, nil
	}

	// List the directory entries.
	entries, err := f.Readdirnames(-1)
	if err != nil {
		return "", nil, err
	}

	// Sort for consistent ordering (Readdirnames may not return sorted).
	sort.Strings(entries)

	// Get type for each entry using fstatat relative to the opened directory.
	var dirEntries []observation.Entry
	for _, name := range entries {
		// Use fstatat to determine the type without crossing symlinks.
		var stat unix.Stat_t
		if err := unix.Fstatat(int(f.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			// Skip entries that can't be stat'd (they may have been deleted).
			continue
		}

		typ := "f"
		if (stat.Mode & unix.S_IFMT) == unix.S_IFDIR {
			typ = "d"
		}
		dirEntries = append(dirEntries, observation.Entry{Type: typ, Name: name})
	}

	// Frame the entries.
	listing, err := observation.FrameEntries(dirEntries)
	if err != nil {
		return "", nil, err
	}

	return journal.ObservationListing, listing, nil
}

// readStaged reads a file that has been staged for writing.
func readStaged(ctx ReadFileContext, path string, newSHA string, offset int64, limit int64, maxResultBytes int64) (json.RawMessage, error) {
	// Get the staged content from the content store.
	content, err := ctx.ContentStore.Content(newSHA)
	if err != nil {
		return nil, fmt.Errorf("get staged content: %w", err)
	}

	// Check if the file is too large and no range is requested.
	if int64(len(content)) > maxResultBytes && offset == 0 && limit == maxResultBytes {
		result := SizeErrorResult{
			Error: "file too large",
			Size:  int64(len(content)),
		}
		data, _ := json.Marshal(result)
		return data, nil
	}

	// Handle ranged reads.
	if offset > 0 || limit < int64(len(content)) {
		return rangedRead(content, offset, limit)
	}

	return json.Marshal(ReadResult{Content: string(content)})
}

// rangedRead returns a portion of the file content.
func rangedRead(content []byte, offset int64, limit int64) (json.RawMessage, error) {
	if offset > int64(len(content)) {
		return json.Marshal(ReadResult{Content: ""})
	}

	end := offset + limit
	if end > int64(len(content)) {
		end = int64(len(content))
	}

	return json.Marshal(ReadResult{Content: string(content[offset:end])})
}

// applyStagedChanges applies staged changes to a directory listing.
func applyStagedChanges(entries []DirEntry, dirPath string, staging map[string]string) []DirEntry {
	if dirPath == observation.Root {
		dirPath = ""
	}

	// Build a map of current entries.
	entryMap := make(map[string]DirEntry, len(entries))
	for _, e := range entries {
		entryMap[e.Name] = e
	}

	// Apply staged changes.
	for stagedPath := range staging {
		// Check if the staged path is in this directory.
		var prefix string
		if dirPath == "" {
			prefix = ""
		} else {
			prefix = dirPath + "/"
		}

		if !strings.HasPrefix(stagedPath, prefix) {
			continue
		}

		// Get the relative path within this directory.
		rel := stagedPath[len(prefix):]
		if strings.Contains(rel, "/") {
			// This is a file in a subdirectory, not in this directory.
			continue
		}

		// Add or update the entry.
		entryMap[rel] = DirEntry{Name: rel, Type: "f"}
	}

	// Convert back to a sorted slice.
	names := make([]string, 0, len(entryMap))
	for name := range entryMap {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]DirEntry, 0, len(names))
	for _, name := range names {
		result = append(result, entryMap[name])
	}

	return result
}
