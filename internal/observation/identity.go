package observation

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
)

// Observation kinds, matching journal constants
const (
	KindFile    = "f"
	KindAbsent  = "a"
	KindListing = "d"
)

// Canonical frame format for observations and change sets.
// The snapshot record is length-framed to unambiguously handle paths with any bytes including newlines.
// Each record is: <kind> <path-byte-length>\n<path-bytes>\n<value>\n
// where value is base_sha256 for kind f, empty for kind a, or entries_sha256 for kind d.
// Entry lists are similarly framed: <type> <name-byte-length>\n<name-bytes>\n
// All records in a snapshot are sorted bytewise by path before hashing.

// SnapshotRecord represents one entry in the canonical snapshot.
type SnapshotRecord struct {
	Kind      string // "f", "a", or "d"
	Path      string // non-empty
	ValueHash string // empty for kind "a", SHA-256 hex for kind "f", entries_sha256 for kind "d"
	PathLen   int    // len([]byte(Path))
}

// EntryRecord represents one entry in a directory listing.
type EntryRecord struct {
	Type    string // "d" for directory, "f" for file, or other type
	Name    string // the entry name
	NameLen int    // len([]byte(Name))
}

// FramePath returns the canonical frame for a path and kind, ready to be part of a snapshot.
// The result is: `<kind> <path-len>\n<path><value>\n` where value is the value hash or empty.
// The frame is unambiguous even when paths contain newlines or multiple consecutive separators.
func FramePath(kind, path, valueHash string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("path must not be empty")
	}
	if kind != KindFile && kind != KindAbsent && kind != KindListing {
		return nil, fmt.Errorf("invalid kind: %q", kind)
	}
	if kind == KindFile && valueHash == "" {
		return nil, fmt.Errorf("kind f requires non-empty value hash")
	}
	if kind == KindAbsent && valueHash != "" {
		return nil, fmt.Errorf("kind a must have empty value hash")
	}
	if kind == KindListing && valueHash == "" {
		return nil, fmt.Errorf("kind d requires non-empty value hash")
	}

	pathBytes := []byte(path)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s %d\n", kind, len(pathBytes))
	buf.Write(pathBytes)
	buf.WriteByte('\n')
	buf.WriteString(valueHash)
	buf.WriteByte('\n')

	return buf.Bytes(), nil
}

// FrameEntry returns the canonical frame for one directory entry.
// The result is: `<type> <name-len>\n<name>\n`
func FrameEntry(entryType, name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("entry name must not be empty")
	}

	nameBytes := []byte(name)
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s %d\n", entryType, len(nameBytes))
	buf.Write(nameBytes)
	buf.WriteByte('\n')

	return buf.Bytes(), nil
}

// FrameEntries returns the canonical frame for a complete directory listing.
// Entries are sorted bytewise by name. The result is a concatenation of framed entries.
// When hashed with SHA-256, this hash is the value used in a snapshot record of kind d.
func FrameEntries(entries []EntryRecord) ([]byte, error) {
	if len(entries) == 0 {
		return []byte{}, nil
	}

	// Sort entries by name (bytewise)
	sortedEntries := make([]EntryRecord, len(entries))
	copy(sortedEntries, entries)
	sort.Slice(sortedEntries, func(i, j int) bool {
		return sortedEntries[i].Name < sortedEntries[j].Name
	})

	var buf bytes.Buffer
	for _, entry := range sortedEntries {
		frame, err := FrameEntry(entry.Type, entry.Name)
		if err != nil {
			return nil, err
		}
		buf.Write(frame)
	}

	return buf.Bytes(), nil
}

// FrameSnapshotRecords returns the canonical frame for all snapshot records.
// Records are sorted bytewise by path. The result is their concatenation.
// When hashed with SHA-256, this produces the snapshot_hash.
func FrameSnapshotRecords(records []SnapshotRecord) ([]byte, error) {
	if len(records) == 0 {
		return []byte{}, nil
	}

	// Sort records by path (bytewise)
	sortedRecords := make([]SnapshotRecord, len(records))
	copy(sortedRecords, records)
	sort.Slice(sortedRecords, func(i, j int) bool {
		return sortedRecords[i].Path < sortedRecords[j].Path
	})

	var buf bytes.Buffer
	for _, record := range sortedRecords {
		frame, err := FramePath(record.Kind, record.Path, record.ValueHash)
		if err != nil {
			return nil, err
		}
		buf.Write(frame)
	}

	return buf.Bytes(), nil
}

// SnapshotHash computes SHA-256 over the canonical frame of snapshot records.
func SnapshotHash(records []SnapshotRecord) (string, error) {
	frame, err := FrameSnapshotRecords(records)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(frame)
	return fmt.Sprintf("%x", hash), nil
}

// ChangeSetPathRecord represents one line of the change set for hashing.
// The fields path, op, base_sha256, new_sha256, new_len are taken from the change set,
// and are hashed in sorted order by path before the snapshot line is appended.
type ChangeSetPathRecord struct {
	Path    string
	Op      string // "create" or "modify"
	BaseSHA string // SHA-256 hex or empty for created
	NewSHA  string // SHA-256 hex
	NewLen  int64
}

// FrameChangeSetLines returns the canonical frame for change-set lines (without snapshot line).
// Lines are sorted bytewise by path.
// Each line is: `<path> <op> <base_sha256> <new_sha256> <new_len>\n`
func FrameChangeSetLines(paths []ChangeSetPathRecord) ([]byte, error) {
	if len(paths) == 0 {
		return []byte{}, nil
	}

	// Sort by path (bytewise)
	sortedPaths := make([]ChangeSetPathRecord, len(paths))
	copy(sortedPaths, paths)
	sort.Slice(sortedPaths, func(i, j int) bool {
		return sortedPaths[i].Path < sortedPaths[j].Path
	})

	var buf bytes.Buffer
	for _, p := range sortedPaths {
		if p.BaseSHA == "" {
			p.BaseSHA = "absent"
		}
		fmt.Fprintf(&buf, "%s %s %s %s %d\n", p.Path, p.Op, p.BaseSHA, p.NewSHA, p.NewLen)
	}

	return buf.Bytes(), nil
}

// ChangesHash computes SHA-256 over change-set lines followed by the snapshot line.
// The snapshot line is: `snapshot:<snapshot_hash>\n`
func ChangesHash(paths []ChangeSetPathRecord, snapshotHash string) (string, error) {
	if snapshotHash == "" {
		return "", fmt.Errorf("snapshot hash must not be empty")
	}

	lines, err := FrameChangeSetLines(paths)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	buf.Write(lines)
	fmt.Fprintf(&buf, "snapshot:%s\n", snapshotHash)

	hash := sha256.Sum256(buf.Bytes())
	return fmt.Sprintf("%x", hash), nil
}

// PathHasBinaryContent checks if a path contains binary bytes that could affect framing.
// This is primarily for testing that the length-framing correctly handles arbitrary bytes.
// Newlines (\n) are allowed since they're handled by length-framing; carriage returns (\r)
// are considered binary because they can cause parsing issues.
func PathHasBinaryContent(path string) bool {
	pathBytes := []byte(path)
	for _, b := range pathBytes {
		if b == 0 || (b < 32 && b != '\t' && b != '\n') || b == '\r' {
			return true
		}
	}
	return false
}

// IsValidSHA256Hex checks if a string is a valid 64-character lowercase hex SHA-256 hash.
func IsValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// ValidateRecord checks that a snapshot record is valid for framing and hashing.
func ValidateRecord(r SnapshotRecord) error {
	if r.Path == "" {
		return fmt.Errorf("path must not be empty")
	}
	if r.Kind != KindFile && r.Kind != KindAbsent && r.Kind != KindListing {
		return fmt.Errorf("invalid kind: %q", r.Kind)
	}
	if r.Kind == KindFile && r.ValueHash == "" {
		return fmt.Errorf("kind f requires non-empty value hash")
	}
	if r.Kind == KindFile && !IsValidSHA256Hex(r.ValueHash) {
		return fmt.Errorf("kind f requires valid SHA-256 hex, got %q", r.ValueHash)
	}
	if r.Kind == KindAbsent && r.ValueHash != "" {
		return fmt.Errorf("kind a must have empty value hash")
	}
	if r.Kind == KindListing && r.ValueHash == "" {
		return fmt.Errorf("kind d requires non-empty value hash")
	}
	if r.Kind == KindListing && !IsValidSHA256Hex(r.ValueHash) {
		return fmt.Errorf("kind d requires valid SHA-256 hex, got %q", r.ValueHash)
	}
	return nil
}
