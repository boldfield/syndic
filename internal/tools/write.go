package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
)

// WriteTools executes write_file, edit_file and unstage for one attempt of one
// turn. Every tool stages content and journals the exact bytes it changed,
// against the accepted call it answers.
type WriteTools struct {
	j         *journal.Store
	obs       *observation.Store
	fs        *workspace.Confined
	turnID    string
	attemptID string
}

// NewWriteTools returns the write tools for attemptID of turnID over the
// confined workspace.
func NewWriteTools(j *journal.Store, fs *workspace.Confined, turnID, attemptID string) *WriteTools {
	return &WriteTools{j: j, obs: observation.NewStore(j), fs: fs, turnID: turnID, attemptID: attemptID}
}

// finish journals the outcome of the accepted call callID. A *toolError
// becomes an error result carrying its text; any other error is a failure of
// the engine itself, journals nothing and is returned.
func (t *WriteTools) finish(callID string, content []byte, err error) (Result, error) {
	res := Result{Content: content}
	status := journal.ToolCallExecuted
	var te *toolError
	switch {
	case err == nil:
	case errors.As(err, &te):
		res = Result{Content: []byte(te.msg), ErrorCode: te.code}
		status = journal.ToolCallError
	default:
		return Result{}, err
	}
	if res.Content == nil {
		res.Content = []byte{}
	}
	if jerr := t.j.Tx(func(tx *journal.Tx) error {
		_, e := tx.RecordToolResult(callID, status, res.Content, res.ErrorCode)
		return e
	}); jerr != nil {
		return Result{}, fmt.Errorf("journal result of call %s: %w", callID, jerr)
	}
	return res, nil
}

// liveReader reads path from the live tree for the observation store, checking
// if it is protected first.
func (t *WriteTools) liveReader(path string) observation.Reader {
	return func() (string, []byte, error) {
		if t.fs.IsProtected(path) {
			return "", nil, toolErrorf(ErrCodeProtected, "%q is protected", path)
		}
		ex, err := t.fs.Examine(path)
		switch {
		case err == nil:
		case errors.Is(err, workspace.ErrSymlinkOnPath):
			return "", nil, observation.ErrSymlinkOnPath
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR):
			return journal.ObservationAbsent, nil, nil
		case errors.Is(err, workspace.ErrSymlink):
			return observation.KindSymlink, nil, nil
		case errors.Is(err, workspace.ErrNotRegular):
			return observation.KindSpecial, nil, nil
		default:
			return "", nil, &liveError{err}
		}
		if ex.IsDir {
			entries := make([]observation.Entry, len(ex.Entries))
			for i, e := range ex.Entries {
				entries[i] = observation.Entry{Type: e.Type, Name: e.Name}
			}
			framed, err := observation.FrameEntries(entries)
			if err != nil {
				return "", nil, &liveError{err}
			}
			return journal.ObservationListing, framed, nil
		}
		return journal.ObservationFile, ex.Content, nil
	}
}

// classify turns a failure to observe path into the tool error the model sees.
func classifyForWrite(path string, err error) error {
	var le *liveError
	switch {
	case errors.Is(err, observation.ErrSymlinkOnPath):
		return toolErrorf(ErrCodeNotRegular, "%q has a symbolic link on its path; symbolic links are refused", path)
	case errors.Is(err, workspace.ErrNotRegular), errors.Is(err, observation.ErrOtherEntry):
		return toolErrorf(ErrCodeNotRegular, "%q is neither a regular file nor a directory", path)
	case errors.As(err, &le):
		return toolErrorf(ErrCodeIO, "cannot access %q: %v", path, le.err)
	}
	return err
}

// observePath observes path once per turn through the observation store and
// returns its recorded status.
func (t *WriteTools) observePath(path string) (journal.Observation, error) {
	obs, err := t.obs.Observe(t.turnID, t.attemptID, path, t.liveReader(path))
	if err != nil {
		return journal.Observation{}, classifyForWrite(path, err)
	}
	return obs, nil
}

// loadStaging returns the turn's staged paths with their content hashes.
func (t *WriteTools) loadStaging() (map[string]string, error) {
	var entries []journal.StagingEntry
	if err := t.j.Tx(func(tx *journal.Tx) (err error) {
		entries, err = tx.Staging(t.turnID)
		return err
	}); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.Path] = e.NewSHA256
	}
	return m, nil
}

// content retrieves content by its SHA256 hash from the store.
func (t *WriteTools) content(sha string) (body []byte, err error) {
	err = t.j.Tx(func(tx *journal.Tx) (err error) {
		body, err = tx.Content(sha)
		return err
	})
	return body, err
}

// stageContent stages content at path and returns what was staged.
func (t *WriteTools) stageContent(path string, content []byte) (journal.StagingEntry, error) {
	var entry journal.StagingEntry
	err := t.j.Tx(func(tx *journal.Tx) (err error) {
		entry, err = tx.StageWrite(t.turnID, path, content)
		return err
	})
	return entry, err
}

// unstageContent removes path from staging.
func (t *WriteTools) unstageContent(path string) error {
	return t.j.Tx(func(tx *journal.Tx) error {
		return tx.Unstage(t.turnID, path)
	})
}

// hasFileAncestor checks if any proper ancestor of path is a file in the
// overlaid view (recorded observation or staged path).
func (t *WriteTools) hasFileAncestor(path string, staging map[string]string) bool {
	parts := strings.Split(path, "/")
	for i := 1; i < len(parts); i++ {
		ancestor := strings.Join(parts[:i], "/")
		if _, staged := staging[ancestor]; staged {
			return true
		}
		obs, err := t.observePath(ancestor)
		if err == nil && obs.Kind == journal.ObservationFile {
			return true
		}
	}
	return false
}

// hasStagedDescendants checks if path has staged descendants that would make
// it a directory in the overlay.
func (t *WriteTools) hasStagedDescendants(path string, staging map[string]string) bool {
	prefix := path + "/"
	for staged := range staging {
		if strings.HasPrefix(staged, prefix) {
			return true
		}
	}
	return false
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// WriteFile stages the complete content of a file, creating it or replacing it
// entirely. It journals the new content by hash.
func (t *WriteTools) WriteFile(callID string, raw json.RawMessage) (Result, error) {
	content, err := t.writeFile(raw)
	return t.finish(callID, content, err)
}

func (t *WriteTools) writeFile(raw json.RawMessage) ([]byte, error) {
	var args writeFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	path, err := cleanPath(args.Path)
	if err != nil {
		return nil, err
	}
	if t.fs.IsProtected(path) {
		return nil, toolErrorf(ErrCodeProtected, "%q is protected", path)
	}
	staging, err := t.loadStaging()
	if err != nil {
		return nil, err
	}
	if t.hasFileAncestor(path, staging) {
		return nil, toolErrorf(ErrCodeNotDirectory, "a proper ancestor of %q is a file", path)
	}
	if t.hasStagedDescendants(path, staging) {
		return nil, toolErrorf(ErrCodeIsDirectory, "%q has staged descendants", path)
	}
	obs, err := t.observePath(path)
	if err != nil {
		return nil, err
	}
	if obs.Kind == journal.ObservationListing {
		return nil, toolErrorf(ErrCodeIsDirectory, "%q is a directory; cannot replace it with write_file", path)
	}
	body := []byte(args.Content)
	entry, err := t.stageContent(path, body)
	if err != nil {
		return nil, fmt.Errorf("stage content: %w", err)
	}
	var msg string
	if entry.BaseSHA256 != "" {
		msg = fmt.Sprintf("staged: %d bytes (base: %s)", len(body), entry.BaseSHA256)
	} else {
		msg = fmt.Sprintf("staged: %d bytes (created)", len(body))
	}
	return []byte(msg), nil
}

type editFileArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// EditFile stages a change replacing one piece of text in a file. It returns
// an error if the old text does not match exactly once.
func (t *WriteTools) EditFile(callID string, raw json.RawMessage) (Result, error) {
	content, err := t.editFile(raw)
	return t.finish(callID, content, err)
}

func (t *WriteTools) editFile(raw json.RawMessage) ([]byte, error) {
	var args editFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	path, err := cleanPath(args.Path)
	if err != nil {
		return nil, err
	}
	if t.fs.IsProtected(path) {
		return nil, toolErrorf(ErrCodeProtected, "%q is protected", path)
	}
	if args.Old == "" {
		return nil, toolErrorf(ErrCodeBadArguments, "old must not be empty")
	}
	staging, err := t.loadStaging()
	if err != nil {
		return nil, err
	}
	var currentBody []byte
	if stagedHash, staged := staging[path]; staged {
		body, err := t.content(stagedHash)
		if err != nil {
			return nil, fmt.Errorf("get staged content: %w", err)
		}
		currentBody = body
	} else {
		obs, err := t.observePath(path)
		if err != nil {
			return nil, err
		}
		if obs.Kind != journal.ObservationFile {
			if obs.Kind == journal.ObservationListing {
				return nil, toolErrorf(ErrCodeIsDirectory, "%q is a directory; cannot edit with edit_file", path)
			}
			return nil, toolErrorf(ErrCodeNotFound, "%q not found", path)
		}
		body, err := t.content(obs.ContentSHA256)
		if err != nil {
			return nil, fmt.Errorf("get current content: %w", err)
		}
		currentBody = body
	}
	oldStr := args.Old
	newStr := args.New
	count := bytes.Count(currentBody, []byte(oldStr))
	if count == 0 {
		return nil, toolErrorf(ErrCodeBadArguments, "%q does not appear in %q", oldStr, path)
	}
	if count > 1 {
		return nil, toolErrorf(ErrCodeBadArguments, "%q appears %d times in %q, must be exactly once", oldStr, count, path)
	}
	newBody := bytes.Replace(currentBody, []byte(oldStr), []byte(newStr), 1)
	entry, err := t.stageContent(path, newBody)
	if err != nil {
		return nil, fmt.Errorf("stage content: %w", err)
	}
	var msg string
	if entry.BaseSHA256 != "" {
		msg = fmt.Sprintf("replaced %d bytes with %d bytes (base: %s)", len(oldStr), len(newStr), entry.BaseSHA256)
	} else {
		msg = fmt.Sprintf("replaced %d bytes with %d bytes (created)", len(oldStr), len(newStr))
	}
	return []byte(msg), nil
}

type unstageArgs struct {
	Path string `json:"path"`
}

// Unstage removes a path from the staging map, reverting any changes made to it
// in this turn.
func (t *WriteTools) Unstage(callID string, raw json.RawMessage) (Result, error) {
	content, err := t.unstage(raw)
	return t.finish(callID, content, err)
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
	if err := t.unstageContent(path); err != nil {
		if errors.Is(err, journal.ErrNotStaged) {
			return nil, toolErrorf(ErrCodeBadArguments, "%q is not staged", path)
		}
		return nil, fmt.Errorf("unstage: %w", err)
	}
	return []byte("unstaged"), nil
}
