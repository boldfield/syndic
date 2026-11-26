package tools

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
)

// SearchFilesArgs are the arguments to the search_files tool.
type SearchFilesArgs struct {
	Pattern string  `json:"pattern"`
	Path    *string `json:"path,omitempty"`
}

// SearchMatch is one matching line from search_files.
type SearchMatch struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// SearchResult is the result of a successful search_files call.
type SearchResult struct {
	Matches []SearchMatch `json:"matches"`
}

// SearchFilesFunc executes the search_files tool. It searches for lines matching
// a regex pattern in files within the workspace. If path is given, it limits
// the search to that file or directory. The search observes every file and
// directory it traverses through the snapshot store.
func SearchFilesFunc(
	ctx ReadFileContext,
	args SearchFilesArgs,
	staging map[string]string, // path -> new_sha256 for staged changes
	isProtected func(string) bool, // function to check if a path is protected
) (json.RawMessage, error) {
	// Compile the regex pattern.
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	// Determine the search scope.
	scope := observation.Root
	if args.Path != nil {
		scope = filepath.Clean(*args.Path)
		if filepath.IsAbs(scope) || strings.Contains(scope, "..") || strings.HasPrefix(scope, "/") {
			return nil, fmt.Errorf("invalid search path: %q", *args.Path)
		}
	}

	// Check if scope is a protected path.
	if isProtected(scope) {
		return nil, fmt.Errorf("protected path: %q", scope)
	}

	// Perform the search.
	matches, err := searchScope(ctx, scope, re, staging, isProtected)
	if err != nil {
		return nil, err
	}

	result := SearchResult{Matches: matches}
	return json.Marshal(result)
}

// searchScope recursively searches for matches in a scope (file or directory).
func searchScope(
	ctx ReadFileContext,
	scope string,
	pattern *regexp.Regexp,
	staging map[string]string,
	isProtected func(string) bool,
) ([]SearchMatch, error) {
	// Normalize scope path.
	if scope == observation.Root || scope == "." {
		scope = observation.Root
	}

	// Check if scope is protected.
	if isProtected(scope) {
		return nil, nil
	}

	// First, observe the scope to determine if it's a file or directory.
	// We use readLiveFile first to check, then readLiveDir if it's not a file.
	var obs journal.Observation
	var err error

	// Try to observe as a file first.
	fileReader := func() (string, []byte, error) {
		return readLiveFile(ctx.Confined, scope)
	}
	obs, err = ctx.ObserveStore.Observe(ctx.TurnID, ctx.AttemptID, scope, fileReader)
	if err != nil {
		return nil, fmt.Errorf("observe %q: %w", scope, err)
	}

	var matches []SearchMatch

	// If it's a file, search it.
	if obs.Kind == journal.ObservationFile {
		scopePath := scope
		if scopePath == observation.Root {
			scopePath = "."
		}
		matches, err := searchFile(ctx, scopePath, pattern, staging)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return matches, nil
	}

	// If it's absent, nothing to search.
	if obs.Kind == journal.ObservationAbsent {
		return nil, nil
	}

	// Now try to observe as a directory. We need a fresh observe call for directory.
	// Since we already observed the scope as a file/absent, we won't re-observe it here.
	// Instead, we directly try to list it.
	dirReader := func() (string, []byte, error) {
		return readLiveDir(ctx.Confined, scope)
	}
	obs, err = ctx.ObserveStore.Observe(ctx.TurnID, ctx.AttemptID, scope, dirReader)
	if err != nil {
		return nil, fmt.Errorf("observe %q: %w", scope, err)
	}

	// If it's not a directory, nothing to search.
	if obs.Kind != journal.ObservationListing {
		return nil, nil
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

	// Apply staged changes to include newly staged files.
	entries = applyStagedDirEntries(entries, scope, staging)

	// Search each entry in the directory.
	for _, entry := range entries {
		entryPath := entry.Name
		if scope != observation.Root {
			entryPath = scope + "/" + entry.Name
		}

		// Check if entry is protected.
		if isProtected(entryPath) {
			continue
		}

		// Recursively search subdirectories.
		if entry.Type == "d" {
			subMatches, err := searchScope(ctx, entryPath, pattern, staging, isProtected)
			if err != nil {
				return nil, err
			}
			matches = append(matches, subMatches...)
		} else {
			// Search files.
			fileMatches, err := searchFile(ctx, entryPath, pattern, staging)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			matches = append(matches, fileMatches...)
		}
	}

	return matches, nil
}

// searchFile searches for matches in a single file.
func searchFile(
	ctx ReadFileContext,
	path string,
	pattern *regexp.Regexp,
	staging map[string]string,
) ([]SearchMatch, error) {
	// Observe the file through the snapshot.
	reader := func() (string, []byte, error) {
		return readLiveFile(ctx.Confined, path)
	}
	obs, err := ctx.ObserveStore.Observe(ctx.TurnID, ctx.AttemptID, path, reader)
	if err != nil {
		return nil, fmt.Errorf("observe %q: %w", path, err)
	}

	// If the file is absent and not staged, skip it.
	var content []byte
	if newSHA, ok := staging[path]; ok {
		// Get staged content.
		var err error
		content, err = ctx.ContentStore.Content(newSHA)
		if err != nil {
			return nil, fmt.Errorf("get staged content %q: %w", path, err)
		}
	} else {
		// Get live content.
		if obs.Kind != journal.ObservationFile {
			return nil, nil
		}
		var err error
		content, err = ctx.ContentStore.Content(obs.ContentSHA256)
		if err != nil {
			return nil, fmt.Errorf("get file content %q: %w", path, err)
		}
	}

	// Search for matches line by line.
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	var matches []SearchMatch
	lineNum := 1

	for scanner.Scan() {
		line := scanner.Text()
		if pattern.MatchString(line) {
			matches = append(matches, SearchMatch{
				File: path,
				Line: lineNum,
				Text: line,
			})
		}
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan file %q: %w", path, err)
	}

	return matches, nil
}

// applyStagedDirEntries applies staged file creations to a directory listing.
func applyStagedDirEntries(entries []observation.Entry, dirPath string, staging map[string]string) []observation.Entry {
	if dirPath == observation.Root {
		dirPath = ""
	}

	// Build a map of current entries.
	entryMap := make(map[string]observation.Entry, len(entries))
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

		// Add or update the entry as a file.
		entryMap[rel] = observation.Entry{Type: "f", Name: rel}
	}

	// Convert back to a sorted slice.
	names := make([]string, 0, len(entryMap))
	for name := range entryMap {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]observation.Entry, 0, len(names))
	for _, name := range names {
		result = append(result, entryMap[name])
	}

	return result
}
