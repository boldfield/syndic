package observation

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/boldfield/syndic/internal/journal"
)

// Root is the path a listing of the workspace root is recorded under. Every
// other path is slash-separated and relative to it, with no leading "./".
const Root = "."

// ErrSnapshotConflict is matched, through errors.Is, by the error Observe
// returns when a live observation contradicts the record (design v5 delta,
// section 6). The tool layer answers it as the tool error snapshot_conflict.
var ErrSnapshotConflict = errors.New("snapshot_conflict")

// Conflict types carried by SnapshotConflictError.
const (
	ConflictDeleted     = "deleted"      // listed (or has recorded descendants) but gone live
	ConflictTypeChanged = "type_changed" // listed or recorded as one type, live is another
)

// SnapshotConflictError names the path whose live state contradicts the
// record. Nothing is recorded for it; the conflict is journaled as a
// snapshot_conflict event.
type SnapshotConflictError struct {
	Path string
	Type string
}

func (e *SnapshotConflictError) Error() string {
	return fmt.Sprintf("snapshot_conflict: %q (%s)", e.Path, e.Type)
}

// Unwrap lets errors.Is(err, ErrSnapshotConflict) match.
func (e *SnapshotConflictError) Unwrap() error { return ErrSnapshotConflict }

// ParseEntries is the strict inverse of FrameEntries: "<type> <len>\n<name>\n"
// repeated, with a canonical non-negative decimal length.
func ParseEntries(data []byte) ([]Entry, error) {
	var out []Entry
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			return nil, errors.New("entry list: unterminated header")
		}
		typ, lenStr, ok := strings.Cut(string(data[:nl]), " ")
		if !ok || typ == "" || strings.ContainsAny(typ, " \t\r\n") {
			return nil, fmt.Errorf("entry list: bad header %q", data[:nl])
		}
		n, err := strconv.Atoi(lenStr)
		if err != nil || n < 0 || strconv.Itoa(n) != lenStr {
			return nil, fmt.Errorf("entry list: bad name length %q", lenStr)
		}
		data = data[nl+1:]
		if len(data) < n+1 || data[n] != '\n' {
			return nil, errors.New("entry list: name not newline-terminated at its length")
		}
		out = append(out, Entry{Type: typ, Name: string(data[:n])})
		data = data[n+1:]
	}
	return out, nil
}

// namespace is what the record already says about one path: whether an
// ancestor settles it as absent, the type its parent's listing gives it, and
// the observations recorded beneath it.
type namespace struct {
	path        string
	absent      bool   // an ancestor listing or observation puts path outside the tree
	viaSymlink  bool   // an ancestor listing records a component of path as a symbolic link
	parentType  string // type in the parent's recorded listing; "" if unlisted
	descendants []journal.Observation
}

func splitPath(p string) []string {
	if p == Root {
		return nil
	}
	return strings.Split(p, "/")
}

func descendantPrefix(p string) string {
	if p == Root {
		return ""
	}
	return p + "/"
}

// loadNamespace reads the record for path's ancestors and descendants.
func loadNamespace(tx *journal.Tx, turnID, path string) (namespace, error) {
	ns := namespace{path: path}
	obs, err := tx.Observations(turnID)
	if err != nil {
		return ns, err
	}
	byPath := make(map[string]journal.Observation, len(obs))
	prefix := descendantPrefix(path)
	for _, o := range obs {
		byPath[o.Path] = o
		if o.Path != path && o.Path != Root && strings.HasPrefix(o.Path, prefix) {
			ns.descendants = append(ns.descendants, o)
		}
	}
	comps := splitPath(path)
	ancestor := Root
	for i, c := range comps {
		if o, ok := byPath[ancestor]; ok {
			switch o.Kind {
			case journal.ObservationAbsent, journal.ObservationFile:
				ns.absent = true
				return ns, nil
			case journal.ObservationListing:
				body, err := tx.Content(o.ContentSHA256)
				if err != nil {
					return ns, err
				}
				entries, err := ParseEntries(body)
				if err != nil {
					return ns, fmt.Errorf("recorded listing %q: %w", ancestor, err)
				}
				typ := ""
				for _, e := range entries {
					if e.Name == c {
						typ = e.Type
						break
					}
				}
				last := i == len(comps)-1
				if !last && typ == "l" {
					ns.viaSymlink = true
					return ns, nil
				}
				if typ == "" || (!last && typ != "d") {
					ns.absent = true
					return ns, nil
				}
				if last {
					ns.parentType = typ
				}
			}
		}
		if ancestor == Root {
			ancestor = c
		} else {
			ancestor += "/" + c
		}
	}
	return ns, nil
}

// hasPresentDescendant reports whether a file or directory is recorded
// beneath the path, which then must be a directory.
func (ns namespace) hasPresentDescendant() bool {
	for _, d := range ns.descendants {
		if d.Kind != journal.ObservationAbsent {
			return true
		}
	}
	return false
}

// conflict returns the conflict type if a live observation of kind contradicts
// the record, or "".
func (ns namespace) conflict(kind string) string {
	if ns.hasPresentDescendant() {
		switch kind {
		case journal.ObservationAbsent:
			return ConflictDeleted
		case journal.ObservationFile, KindSymlink, KindSpecial:
			return ConflictTypeChanged
		}
	}
	var want string
	switch ns.parentType {
	case "":
		return ""
	case "f":
		want = journal.ObservationFile
	case "d":
		want = journal.ObservationListing
	case "l":
		want = KindSymlink
	case "o":
		want = KindSpecial
	}
	switch {
	case kind == journal.ObservationAbsent:
		return ConflictDeleted
	case want != "" && kind != want:
		return ConflictTypeChanged
	}
	return ""
}

// reconcileListing applies the record to a live listing: a recorded absence
// is removed, a recorded file or directory is kept whatever the live tree
// says, and a name with recorded content beneath it is a directory.
func (ns namespace) reconcileListing(live []byte) ([]byte, error) {
	entries, err := ParseEntries(live)
	if err != nil {
		return nil, fmt.Errorf("live listing of %q: %w", ns.path, err)
	}
	byName := make(map[string]string, len(entries))
	for _, e := range entries {
		byName[e.Name] = e.Type
	}
	prefix := descendantPrefix(ns.path)
	for _, d := range ns.descendants {
		if name, _, deeper := strings.Cut(d.Path[len(prefix):], "/"); deeper && d.Kind != journal.ObservationAbsent {
			byName[name] = "d"
		}
	}
	for _, d := range ns.descendants {
		name := d.Path[len(prefix):]
		if strings.Contains(name, "/") {
			continue
		}
		switch d.Kind {
		case journal.ObservationAbsent:
			delete(byName, name)
		case journal.ObservationFile:
			byName[name] = "f"
		case journal.ObservationListing:
			byName[name] = "d"
		}
	}
	out := make([]Entry, 0, len(byName))
	for n, t := range byName {
		out = append(out, Entry{Type: t, Name: n})
	}
	return FrameEntries(out)
}

// journalConflict records the conflict as a snapshot_conflict event bound to
// the turn and attempt.
func (s *Store) journalConflict(turnID, attemptID string, c *SnapshotConflictError) error {
	payload := map[string]any{"attempt_id": attemptID, "conflict": c.Type}
	if utf8.ValidString(c.Path) {
		payload["path"] = c.Path
	} else {
		payload["path_b64"] = base64.StdEncoding.EncodeToString([]byte(c.Path))
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.j.Tx(func(tx *journal.Tx) error {
		return tx.EmitAttemptEvent(turnID, attemptID, "snapshot_conflict", data)
	})
}
