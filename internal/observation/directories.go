package observation

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/boldfield/syndic/internal/journal"
)

// ErrSnapshotConflict is returned when a later observation contradicts an earlier one.
// The conflict is recorded in the journal.
var ErrSnapshotConflict = errors.New("snapshot_conflict")

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

// parentAndName splits a journal path into parent directory and name, using "/" separator.
// A top-level file (name with no "/") has parent "".
func parentAndName(p string) (parent, name string) {
	if idx := strings.LastIndexByte(p, '/'); idx >= 0 {
		return p[:idx], p[idx+1:]
	}
	return "", p
}

// ReconcileDirectoryObservation reconciles a directory observation with the
// turn's existing observations. It:
// 1. Filters out entries that were previously observed as absent in that directory.
// 2. Adds entries for files previously observed in this directory but missing from the live listing.
// 3. Preserves known types: a previously-recorded type overrides a conflicting live type.
func ReconcileDirectoryObservation(turnID string, dirPath string, entries []Entry, j *journal.Store) ([]Entry, error) {
	var absences map[string]bool
	var priorTypes map[string]string

	err := j.Tx(func(tx *journal.Tx) error {
		obs, err := tx.Observations(turnID)
		if err != nil {
			return err
		}
		absences = make(map[string]bool)
		priorTypes = make(map[string]string)

		for _, o := range obs {
			parent, name := parentAndName(o.Path)
			if parent != dirPath {
				continue
			}

			switch o.Kind {
			case journal.ObservationAbsent:
				absences[name] = true
			case journal.ObservationFile:
				priorTypes[name] = "f"
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile directory observation: %w", err)
	}

	// Build result: live entries except those absent, plus prior files missing from live listing.
	result := make(map[string]Entry)

	// Add live entries that are not marked absent.
	for _, e := range entries {
		if !absences[e.Name] {
			// Use prior type if available, otherwise use live type.
			if priorType, found := priorTypes[e.Name]; found && priorType != e.Type {
				// Type recorded previously: preserve it.
				result[e.Name] = Entry{Type: priorType, Name: e.Name}
			} else {
				result[e.Name] = e
			}
		}
	}

	// Add previously-observed entries that are not in the live listing.
	for name, priorType := range priorTypes {
		if _, inLive := result[name]; !inLive {
			// Entry was previously observed but missing from live listing.
			// Add it to the result with its prior type.
			result[name] = Entry{Type: priorType, Name: name}
		}
	}

	// Convert map back to sorted slice.
	var out []Entry
	for _, e := range result {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})

	return out, nil
}

// CheckFileConflict checks whether a file or absence observation conflicts with
// prior directory observations. It detects when:
// - A file listed in a parent directory has been deleted or changed type
// - A file is not in a recorded parent listing but exists in the live tree
// - A file is in a recorded parent listing but is absent in the live tree
// liveFileType should be "f", "d", "", or journal.ObservationAbsent for an absence.
func CheckFileConflict(turnID string, path string, liveFileType string, j *journal.Store) error {
	var conflict bool
	var conflictType string

	err := j.Tx(func(tx *journal.Tx) error {
		obs, err := tx.Observations(turnID)
		if err != nil {
			return err
		}

		parent, name := parentAndName(path)

		for _, o := range obs {
			if o.Kind != journal.ObservationListing {
				continue
			}
			if o.Path != parent {
				continue
			}

			// Found a listing of the parent directory.
			content, err := tx.Content(o.ContentSHA256)
			if err != nil {
				return err
			}

			entries, err := ParseEntries(content)
			if err != nil {
				return err
			}

			// Check if our file name is in the listing.
			fileInListing := false
			var listedType string
			for _, e := range entries {
				if e.Name == name {
					fileInListing = true
					listedType = e.Type
					break
				}
			}

			switch liveFileType {
			case journal.ObservationAbsent, "":
				// Live observation is an absence (empty string or ObservationAbsent).
				if fileInListing {
					// File is in listing but missing from live tree: conflict.
					conflict = true
					conflictType = "deleted"
					return nil
				}
			default:
				// Live observation is a file or directory with a type ("f" or "d").
				if !fileInListing {
					// File exists in live tree but was not in cached listing: conflict.
					conflict = true
					conflictType = "unlisted"
					return nil
				}
				if listedType != liveFileType {
					// File type changed.
					conflict = true
					conflictType = "type-changed"
					return nil
				}
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return err
	}

	if conflict {
		return fmt.Errorf("%w: %s (%s)", ErrSnapshotConflict, path, conflictType)
	}
	return nil
}
