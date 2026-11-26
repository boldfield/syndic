package observation

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/boldfield/syndic/internal/journal"
)

// ParseEntries decodes a canonical directory listing from bytes.
// Format: each entry is "<type> <name-len>\n<name>\n".
func ParseEntries(data []byte) ([]Entry, error) {
	var entries []Entry
	pos := 0

	for pos < len(data) {
		// Find the space separating type from name length
		spacePos := pos
		for spacePos < len(data) && data[spacePos] != ' ' {
			spacePos++
		}
		if spacePos == len(data) {
			return nil, fmt.Errorf("malformed entry list: no space found")
		}

		typeStr := string(data[pos:spacePos])

		// Parse the name length
		pos = spacePos + 1
		newlinePos := pos
		for newlinePos < len(data) && data[newlinePos] != '\n' {
			newlinePos++
		}
		if newlinePos == len(data) {
			return nil, fmt.Errorf("malformed entry list: no newline after length")
		}

		var nameLen int
		if _, err := fmt.Sscanf(string(data[pos:newlinePos]), "%d", &nameLen); err != nil {
			return nil, fmt.Errorf("malformed entry list: invalid length: %w", err)
		}

		// Read the name
		pos = newlinePos + 1
		nameStart := pos
		nameEnd := pos + nameLen
		if nameEnd > len(data) {
			return nil, fmt.Errorf("malformed entry list: name exceeds data bounds")
		}

		name := string(data[nameStart:nameEnd])

		// Expect a newline after the name
		pos = nameEnd
		if pos >= len(data) || data[pos] != '\n' {
			return nil, fmt.Errorf("malformed entry list: no newline after name")
		}
		pos++

		entries = append(entries, Entry{Type: typeStr, Name: name})
	}

	// Sort entries by name for consistency.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

// ReconcileDirectoryObservation reconciles a directory observation with the
// turn's existing observations. It filters out entries that were previously
// observed as absent in that directory.
func ReconcileDirectoryObservation(turnID string, dirPath string, entries []Entry, j *journal.Store) ([]Entry, error) {
	var absent map[string]bool
	err := j.Tx(func(tx *journal.Tx) error {
		obs, err := tx.Observations(turnID)
		if err != nil {
			return err
		}
		absent = make(map[string]bool)
		for _, o := range obs {
			if o.Kind != journal.ObservationAbsent {
				continue
			}
			parent := filepath.Dir(o.Path)
			if parent == dirPath {
				absent[filepath.Base(o.Path)] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile directory observation: %w", err)
	}

	// Filter out entries that were previously observed as absent.
	filtered := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if !absent[e.Name] {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// CheckFileConflict checks whether a file observation conflicts with prior
// directory observations. It detects when a file listed in a parent directory
// has been deleted or changed type, returning a snapshot_conflict error.
func CheckFileConflict(turnID string, path string, liveFileType string, j *journal.Store) error {
	var conflict bool
	err := j.Tx(func(tx *journal.Tx) error {
		obs, err := tx.Observations(turnID)
		if err != nil {
			return err
		}

		// Look for a directory listing that contains this path.
		parent := filepath.Dir(path)
		name := filepath.Base(path)

		for _, o := range obs {
			if o.Kind != journal.ObservationListing {
				continue
			}
			if o.Path != parent {
				continue
			}

			// Found a listing of the parent directory.
			// Decode the entries to check if this path is listed.
			content, err := tx.Content(o.ContentSHA256)
			if err != nil {
				return err
			}

			entries, err := ParseEntries(content)
			if err != nil {
				return err
			}

			// Check if our file name is in the listing.
			for _, e := range entries {
				if e.Name == name {
					// The file is listed. Check if the type changed.
					if e.Type != liveFileType {
						conflict = true
						return nil
					}
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	if conflict {
		return fmt.Errorf("snapshot_conflict: %s", path)
	}
	return nil
}
