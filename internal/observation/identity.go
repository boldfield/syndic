// Package observation pins the canonical byte forms behind a turn's snapshot
// and change set (design v5 delta, section 6). Names and paths may contain any
// byte, including a newline, so every variable-length name is length-framed.
package observation

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Observation kinds recorded in a snapshot.
const (
	KindFile   = 'f' // file content; value is base_sha256
	KindAbsent = 'a' // path absence; value is empty
	KindDir    = 'd' // directory listing; value is entries_sha256
)

// Change operations.
const (
	OpCreate = "create"
	OpModify = "modify"
)

// Absent is the base_sha256 of a created path.
const Absent = "absent"

var sha256HexRegex = regexp.MustCompile(`^[a-f0-9]{64}$`)

// IsSHA256Hex reports whether s is 64 lowercase hex characters.
func IsSHA256Hex(s string) bool { return sha256HexRegex.MatchString(s) }

// Record is one snapshot observation.
type Record struct {
	Kind            byte
	Path            string
	Value           string
	AttemptID       string // which attempt first observed this path
	SnapshotVersion int    // version of the snapshot when recorded
}

// Entry is one name in a recorded directory listing.
type Entry struct {
	Type string // single token without whitespace, e.g. "f", "d", "l"
	Name string
}

// Change is one staged path in a change set.
type Change struct {
	Path    string
	Op      string
	BaseSHA string // 64 hex, or Absent for a create
	NewSHA  string
	NewLen  int64
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}

// frame writes "<head> <len>\n<name>\n".
func frame(buf *bytes.Buffer, head, name string) {
	buf.WriteString(head)
	buf.WriteByte(' ')
	buf.WriteString(strconv.Itoa(len(name)))
	buf.WriteByte('\n')
	buf.WriteString(name)
	buf.WriteByte('\n')
}

func validateRecord(r Record) error {
	switch r.Kind {
	case KindFile, KindDir:
		if !IsSHA256Hex(r.Value) {
			return fmt.Errorf("record %q: value must be 64 lowercase hex characters, got %q", r.Path, r.Value)
		}
	case KindAbsent:
		if r.Value != "" {
			return fmt.Errorf("record %q: absence must have an empty value", r.Path)
		}
	default:
		return fmt.Errorf("record %q: unknown kind %q", r.Path, string(r.Kind))
	}
	return nil
}

// FrameSnapshotRecords returns the canonical bytes of a snapshot: records
// sorted bytewise by path, each "<kind> <path-len>\n<path>\n<value>\n".
func FrameSnapshotRecords(records []Record) ([]byte, error) {
	sorted := append([]Record(nil), records...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var buf bytes.Buffer
	for i, r := range sorted {
		if err := validateRecord(r); err != nil {
			return nil, err
		}
		if i > 0 && sorted[i-1].Path == r.Path {
			return nil, fmt.Errorf("duplicate snapshot path %q", r.Path)
		}
		frame(&buf, string(r.Kind), r.Path)
		buf.WriteString(r.Value)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// SnapshotHash is the SHA-256 over the framed snapshot records.
func SnapshotHash(records []Record) (string, error) {
	b, err := FrameSnapshotRecords(records)
	if err != nil {
		return "", err
	}
	return sum(b), nil
}

// FrameEntries returns the canonical bytes of a directory entry list: names
// sorted bytewise, each "<type> <name-len>\n<name>\n".
func FrameEntries(entries []Entry) ([]byte, error) {
	sorted := append([]Entry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var buf bytes.Buffer
	for i, e := range sorted {
		if e.Type == "" || strings.ContainsAny(e.Type, " \t\r\n") {
			return nil, fmt.Errorf("entry %q: type must be a non-empty token without whitespace, got %q", e.Name, e.Type)
		}
		if i > 0 && sorted[i-1].Name == e.Name {
			return nil, fmt.Errorf("duplicate directory entry %q", e.Name)
		}
		frame(&buf, e.Type, e.Name)
	}
	return buf.Bytes(), nil
}

// EntriesHash is entries_sha256, the SHA-256 over the framed entry list.
func EntriesHash(entries []Entry) (string, error) {
	b, err := FrameEntries(entries)
	if err != nil {
		return "", err
	}
	return sum(b), nil
}

func validateChange(c Change) error {
	if c.Path == "" {
		return fmt.Errorf("change path must be non-empty")
	}
	switch c.Op {
	case OpCreate:
		if c.BaseSHA != Absent {
			return fmt.Errorf("change %q: create must have base %q, got %q", c.Path, Absent, c.BaseSHA)
		}
	case OpModify:
		if !IsSHA256Hex(c.BaseSHA) {
			return fmt.Errorf("change %q: modify base must be 64 lowercase hex characters, got %q", c.Path, c.BaseSHA)
		}
	default:
		return fmt.Errorf("change %q: op must be create or modify, got %q", c.Path, c.Op)
	}
	if !IsSHA256Hex(c.NewSHA) {
		return fmt.Errorf("change %q: new_sha256 must be 64 lowercase hex characters, got %q", c.Path, c.NewSHA)
	}
	if c.NewLen < 0 {
		return fmt.Errorf("change %q: new_len must be non-negative", c.Path)
	}
	return nil
}

// FrameChangeSetLines returns the sorted canonical change-set lines, without
// the snapshot line: "<path-len> <path> <op> <base_sha256> <new_sha256> <new_len>\n".
// The path is length-framed, so a path holding spaces or newlines cannot forge
// another line. The unchanged-revision comparison uses these bytes alone.
func FrameChangeSetLines(changes []Change) ([]byte, error) {
	sorted := append([]Change(nil), changes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var buf bytes.Buffer
	for i, c := range sorted {
		if err := validateChange(c); err != nil {
			return nil, err
		}
		if i > 0 && sorted[i-1].Path == c.Path {
			return nil, fmt.Errorf("duplicate change path %q", c.Path)
		}
		fmt.Fprintf(&buf, "%d %s %s %s %s %d\n", len(c.Path), c.Path, c.Op, c.BaseSHA, c.NewSHA, c.NewLen)
	}
	return buf.Bytes(), nil
}

// FrameChangeSet returns the lines followed by "snapshot:<snapshot_hash>\n".
func FrameChangeSet(changes []Change, snapshotHash string) ([]byte, error) {
	if !IsSHA256Hex(snapshotHash) {
		return nil, fmt.Errorf("snapshot hash must be 64 lowercase hex characters, got %q", snapshotHash)
	}
	lines, err := FrameChangeSetLines(changes)
	if err != nil {
		return nil, err
	}
	return append(lines, "snapshot:"+snapshotHash+"\n"...), nil
}

// ChangesHash is changes_hash, bound into the candidate identity.
func ChangesHash(changes []Change, snapshotHash string) (string, error) {
	b, err := FrameChangeSet(changes, snapshotHash)
	if err != nil {
		return "", err
	}
	return sum(b), nil
}
