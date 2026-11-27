package tools

import (
	"encoding/json"
	"fmt"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/workspace"
)

// RefereeReader serves a command referee's reads (v7 delta section 6,
// "Reads"). It answers read_file, list_dir and search_files from the turn's
// record and its staged overlay as they stand, under the same confinement,
// protected-path and size rules as a participant's read. It never reads the
// live tree: a path the record does not hold reads "not found", nothing is
// observed, and no snapshot_version changes. It journals nothing; the engine
// journals each read under its decision.
type RefereeReader struct {
	t *ReadTools
}

// NewRefereeReader returns a reader for turnID. maxResultBytes <= 0 selects
// DefaultMaxResultBytes.
func NewRefereeReader(j *journal.Store, fs *workspace.Confined, turnID string, maxResultBytes int) *RefereeReader {
	t := NewReadTools(j, fs, turnID, "", maxResultBytes)
	t.recordOnly = true
	return &RefereeReader{t: t}
}

// Read executes the named read tool. A failure the referee is told about is a
// Result with an ErrorCode; any other error is a failure of the engine.
func (r *RefereeReader) Read(name string, raw json.RawMessage) (Result, error) {
	var content []byte
	var err error
	switch name {
	case NameReadFile:
		content, err = r.t.readFile(raw)
	case NameListDir:
		content, err = r.t.listDir(raw)
	case NameSearchFiles:
		content, err = r.t.searchFiles(raw)
	default:
		return Result{}, fmt.Errorf("tools: %q is not a read tool", name)
	}
	res, _, err := outcome(content, err)
	return res, err
}
