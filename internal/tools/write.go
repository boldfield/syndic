package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
)

// Tool error codes of the write tools.
const (
	ErrCodeNoMatch        = "no_match"
	ErrCodeAmbiguousMatch = "ambiguous_match"
	ErrCodeNotStaged      = "not_staged"
)

// WriteTools executes write_file, edit_file and unstage for one attempt of
// one turn. None of them touches the workspace: each changes the turn's
// staging map, whose content lives in the content-addressed store, and the
// read tools show the staged content over the snapshot. The staging map
// belongs to the turn, so it persists across attempts and revision rounds.
type WriteTools struct {
	r *ReadTools
}

// NewWriteTools returns the write tools for attemptID of turnID over the
// confined workspace.
func NewWriteTools(j *journal.Store, fs *workspace.Confined, turnID, attemptID string, maxResultBytes int) *WriteTools {
	return &WriteTools{r: NewReadTools(j, fs, turnID, attemptID, maxResultBytes)}
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteFile executes write_file for the accepted call callID and journals the
// result text, or the error text.
func (t *WriteTools) WriteFile(callID string, raw json.RawMessage) (Result, error) {
	content, err := t.writeFile(raw)
	return t.r.finish(callID, content, err)
}

// target cleans a model-supplied path to a protected-checked file path.
func (t *WriteTools) target(p string) (string, error) {
	path, err := cleanPath(p)
	if err != nil {
		return "", err
	}
	if path == observation.Root {
		return "", toolErrorf(ErrCodeIsDirectory, "the workspace root is a directory")
	}
	if t.r.fs.IsProtected(path) {
		return "", toolErrorf(ErrCodeProtected, "%q is protected", path)
	}
	return path, nil
}

// observe records path in the turn's snapshot, once.
func (t *WriteTools) observe(path string) (journal.Observation, error) {
	o, err := t.r.obs.ObserveWithMode(t.r.turnID, t.r.attemptID, path, t.r.liveReader(path))
	if err != nil {
		return journal.Observation{}, classify(path, err)
	}
	return o, nil
}

func (t *WriteTools) writeFile(raw json.RawMessage) ([]byte, error) {
	var args writeFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	path, err := t.target(args.Path)
	if err != nil {
		return nil, err
	}
	staging, err := t.r.loadStaging()
	if err != nil {
		return nil, err
	}
	if err := t.checkPlacement(path, staging); err != nil {
		return nil, err
	}
	return t.stage(path, []byte(args.Content))
}

// checkPlacement refuses a write that would make the overlaid tree
// contradict itself: a file beneath a file, or a file where a directory is.
func (t *WriteTools) checkPlacement(path string, staging map[string]string) error {
	for _, a := range observation.Ancestors(path) {
		if _, ok := staging[a]; ok {
			return toolErrorf(ErrCodeNotDirectory, "%q is a file; %q cannot be created beneath it", a, path)
		}
		o, err := t.observe(a)
		if err != nil {
			return err
		}
		if o.Kind == journal.ObservationFile {
			return toolErrorf(ErrCodeNotDirectory, "%q is a file; %q cannot be created beneath it", a, path)
		}
	}
	if observation.HasStagedDescendant(staging, path) {
		return toolErrorf(ErrCodeIsDirectory, "%q is a directory in the proposed changes", path)
	}
	if _, ok := staging[path]; ok {
		return nil
	}
	o, err := t.observe(path)
	if err != nil {
		return err
	}
	if o.Kind == journal.ObservationListing {
		return toolErrorf(ErrCodeIsDirectory, "%q is a directory", path)
	}
	return nil
}

// stage records content as the new content of path, whose base is already
// observed, and returns the result text.
func (t *WriteTools) stage(path string, content []byte) ([]byte, error) {
	var e journal.StagingEntry
	if err := t.r.j.Tx(func(tx *journal.Tx) (err error) {
		e, err = tx.StageWrite(t.r.turnID, path, content)
		return err
	}); err != nil {
		return nil, err
	}
	base := "created"
	if e.BaseSHA256 != "" {
		base = "base: " + e.BaseSHA256
	}
	return []byte(fmt.Sprintf("staged: %d bytes (%s)", len(content), base)), nil
}

type editFileArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// EditFile executes edit_file for the accepted call callID and journals the
// result text, or the error text.
func (t *WriteTools) EditFile(callID string, raw json.RawMessage) (Result, error) {
	content, err := t.editFile(raw)
	return t.r.finish(callID, content, err)
}

func (t *WriteTools) editFile(raw json.RawMessage) ([]byte, error) {
	var args editFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.Old == "" {
		return nil, toolErrorf(ErrCodeBadArguments, "old must not be empty")
	}
	path, err := t.target(args.Path)
	if err != nil {
		return nil, err
	}
	staging, err := t.r.loadStaging()
	if err != nil {
		return nil, err
	}
	v, err := t.r.view(path, staging)
	if err != nil {
		return nil, err
	}
	switch v.kind {
	case journal.ObservationAbsent:
		return nil, toolErrorf(ErrCodeNotFound, "%q not found", path)
	case journal.ObservationListing:
		return nil, toolErrorf(ErrCodeIsDirectory, "%q is a directory", path)
	}
	old := []byte(args.Old)
	switch n := bytes.Count(v.content, old); {
	case n == 0:
		return nil, toolErrorf(ErrCodeNoMatch, "old text not found in %q; nothing changed", path)
	case n > 1:
		return nil, toolErrorf(ErrCodeAmbiguousMatch, "old text matches %d places in %q; nothing changed", n, path)
	}
	return t.stage(path, bytes.Replace(v.content, old, []byte(args.New), 1))
}

type unstageArgs struct {
	Path string `json:"path"`
}

// Unstage executes unstage for the accepted call callID and journals the
// result text, or the error text.
func (t *WriteTools) Unstage(callID string, raw json.RawMessage) (Result, error) {
	content, err := t.unstage(raw)
	return t.r.finish(callID, content, err)
}

func (t *WriteTools) unstage(raw json.RawMessage) ([]byte, error) {
	var args unstageArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	path, err := cleanPath(args.Path)
	if err != nil {
		return nil, err
	}
	err = t.r.j.Tx(func(tx *journal.Tx) error { return tx.Unstage(t.r.turnID, path) })
	if errors.Is(err, journal.ErrNotStaged) {
		return nil, toolErrorf(ErrCodeNotStaged, "%q has no staged change", path)
	}
	if err != nil {
		return nil, err
	}
	return []byte("unstaged: " + path), nil
}
