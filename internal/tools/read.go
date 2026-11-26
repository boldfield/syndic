package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
	"github.com/boldfield/syndic/internal/workspace"
)

// DefaultMaxResultBytes is tools.max_result_bytes' default (v5 delta section 6).
const DefaultMaxResultBytes = 65536

// Tool error codes journaled with an error result.
const (
	ErrCodeBadArguments     = "bad_arguments"
	ErrCodeNotFound         = "not_found"
	ErrCodeProtected        = "protected"
	ErrCodeIsDirectory      = "is_directory"
	ErrCodeNotDirectory     = "not_directory"
	ErrCodeNotRegular       = "not_regular"
	ErrCodeTooLarge         = "too_large"
	ErrCodeOutOfRange       = "out_of_range"
	ErrCodeSnapshotConflict = "snapshot_conflict"
	ErrCodeIO               = "io_error"
)

// Result is what a read tool returned to the model, exactly as journaled: the
// content of an executed call, or the error text of one that failed, with the
// error code.
type Result struct {
	Content   []byte
	ErrorCode string
}

// toolError is a failure the model is told about.
type toolError struct {
	code string
	msg  string
}

func (e *toolError) Error() string { return e.code + ": " + e.msg }

func toolErrorf(code, format string, args ...any) *toolError {
	return &toolError{code: code, msg: fmt.Sprintf(format, args...)}
}

// liveError marks a failure to read the live tree, as opposed to a failure of
// the journal.
type liveError struct{ err error }

func (e *liveError) Error() string { return e.err.Error() }
func (e *liveError) Unwrap() error { return e.err }

// ReadTools executes read_file, list_dir and search_files for one attempt of
// one turn. Every tool observes through the turn's snapshot record, sees the
// turn's staged changes as an overlay, and journals the exact bytes it returns
// against the accepted call it answers.
type ReadTools struct {
	j              *journal.Store
	obs            *observation.Store
	fs             *workspace.Confined
	turnID         string
	attemptID      string
	maxResultBytes int
}

// NewReadTools returns the read tools for attemptID of turnID over the
// confined workspace. maxResultBytes <= 0 selects DefaultMaxResultBytes.
func NewReadTools(j *journal.Store, fs *workspace.Confined, turnID, attemptID string, maxResultBytes int) *ReadTools {
	if maxResultBytes <= 0 {
		maxResultBytes = DefaultMaxResultBytes
	}
	return &ReadTools{j: j, obs: observation.NewStore(j), fs: fs, turnID: turnID, attemptID: attemptID, maxResultBytes: maxResultBytes}
}

// finish journals the outcome of the accepted call callID. A *toolError
// becomes an error result carrying its text; any other error is a failure of
// the engine itself, journals nothing and is returned.
func (t *ReadTools) finish(callID string, content []byte, err error) (Result, error) {
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

// cleanPath normalizes a model-supplied path to the slash-separated form the
// record uses, observation.Root for the workspace root.
func cleanPath(p string) (string, error) {
	if p == "" {
		return "", toolErrorf(ErrCodeBadArguments, "path must not be empty")
	}
	if strings.HasPrefix(p, "/") {
		return "", toolErrorf(ErrCodeBadArguments, "path %q must be relative to the workspace root", p)
	}
	if strings.IndexByte(p, 0) >= 0 {
		return "", toolErrorf(ErrCodeBadArguments, "path contains a NUL byte")
	}
	var comps []string
	for _, c := range strings.Split(p, "/") {
		switch c {
		case "", ".":
		case "..":
			return "", toolErrorf(ErrCodeBadArguments, "path %q leaves the workspace", p)
		default:
			comps = append(comps, c)
		}
	}
	if len(comps) == 0 {
		return observation.Root, nil
	}
	return strings.Join(comps, "/"), nil
}

func decodeArgs(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return toolErrorf(ErrCodeBadArguments, "invalid arguments: %v", err)
	}
	if dec.More() {
		return toolErrorf(ErrCodeBadArguments, "invalid arguments: trailing data")
	}
	return nil
}

// view is a path as every tool sees it: the recorded observation under the
// turn's staged overlay.
type view struct {
	kind    string // journal.ObservationFile, ObservationListing or ObservationAbsent
	content []byte
	entries []observation.Entry
}

// liveReader reads path from the live tree for the observation store.
func (t *ReadTools) liveReader(path string) observation.Reader {
	return func() (string, []byte, error) {
		if t.fs.IsProtected(path) {
			return "", nil, toolErrorf(ErrCodeProtected, "%q is protected", path)
		}
		ex, err := t.fs.Examine(path)
		switch {
		case err == nil:
		case errors.Is(err, workspace.ErrSymlinkOnPath):
			// Checked before ENOTDIR, which it wraps: refused, not absent.
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
		if !ex.IsDir {
			return journal.ObservationFile, ex.Content, nil
		}
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
}

// classify turns a failure to observe path into the tool error the model sees.
func classify(path string, err error) error {
	var le *liveError
	switch {
	case errors.Is(err, observation.ErrSnapshotConflict):
		return &toolError{code: ErrCodeSnapshotConflict, msg: err.Error()}
	case errors.Is(err, observation.ErrSymlinkOnPath):
		return toolErrorf(ErrCodeNotRegular, "%q has a symbolic link on its path; symbolic links are refused", path)
	case errors.Is(err, workspace.ErrNotRegular), errors.Is(err, observation.ErrOtherEntry):
		return toolErrorf(ErrCodeNotRegular, "%q is neither a regular file nor a directory", path)
	case errors.As(err, &le):
		return toolErrorf(ErrCodeIO, "cannot read %q: %v", path, le.err)
	}
	return err
}

// view observes path (once per turn, through the record) and applies the
// staged overlay.
func (t *ReadTools) view(path string, staging map[string]string) (view, error) {
	if sha, ok := staging[path]; ok {
		body, err := t.content(sha)
		return view{kind: journal.ObservationFile, content: body}, err
	}
	o, err := t.obs.Observe(t.turnID, t.attemptID, path, t.liveReader(path))
	if err != nil {
		return view{}, classify(path, err)
	}
	extra := observation.StagedChildren(staging, path)
	switch o.Kind {
	case journal.ObservationFile:
		body, err := t.content(o.ContentSHA256)
		return view{kind: o.Kind, content: body}, err
	case journal.ObservationAbsent:
		if len(extra) == 0 {
			return view{kind: o.Kind}, nil
		}
	case journal.ObservationListing:
	}
	var entries []observation.Entry
	if o.Kind == journal.ObservationListing {
		body, err := t.content(o.ContentSHA256)
		if err != nil {
			return view{}, err
		}
		if entries, err = observation.ParseEntries(body); err != nil {
			return view{}, fmt.Errorf("recorded listing %q: %w", path, err)
		}
	}
	byName := map[string]string{}
	for _, e := range entries {
		byName[e.Name] = e.Type
	}
	for n, typ := range extra {
		byName[n] = typ
	}
	merged := make([]observation.Entry, 0, len(byName))
	for n, typ := range byName {
		merged = append(merged, observation.Entry{Type: typ, Name: n})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Name < merged[j].Name })
	return view{kind: journal.ObservationListing, entries: merged}, nil
}

// loadStaging returns the turn's staged paths with their content hashes.
func (t *ReadTools) loadStaging() (map[string]string, error) {
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

func (t *ReadTools) content(sha string) (body []byte, err error) {
	err = t.j.Tx(func(tx *journal.Tx) (err error) {
		body, err = tx.Content(sha)
		return err
	})
	return body, err
}

type readFileArgs struct {
	Path   string `json:"path"`
	Offset *int64 `json:"offset"`
	Limit  *int64 `json:"limit"`
}

// ReadFile executes read_file for the accepted call callID and journals the
// exact bytes returned, or the error text.
func (t *ReadTools) ReadFile(callID string, raw json.RawMessage) (Result, error) {
	content, err := t.readFile(raw)
	return t.finish(callID, content, err)
}

func (t *ReadTools) readFile(raw json.RawMessage) ([]byte, error) {
	var args readFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	path, err := cleanPath(args.Path)
	if err != nil {
		return nil, err
	}
	if args.Offset != nil && *args.Offset < 0 {
		return nil, toolErrorf(ErrCodeBadArguments, "offset must be >= 0")
	}
	if args.Limit != nil && *args.Limit < 1 {
		return nil, toolErrorf(ErrCodeBadArguments, "limit must be >= 1")
	}
	if t.fs.IsProtected(path) {
		return nil, toolErrorf(ErrCodeProtected, "%q is protected", path)
	}
	staging, err := t.loadStaging()
	if err != nil {
		return nil, err
	}
	v, err := t.view(path, staging)
	if err != nil {
		return nil, err
	}
	switch v.kind {
	case journal.ObservationAbsent:
		return nil, toolErrorf(ErrCodeNotFound, "%q not found", path)
	case journal.ObservationListing:
		return nil, toolErrorf(ErrCodeIsDirectory, "%q is a directory; use list_dir", path)
	}

	size := int64(len(v.content))
	body := v.content
	ranged := args.Offset != nil || args.Limit != nil
	if ranged {
		var off int64
		if args.Offset != nil {
			off = *args.Offset
		}
		if off > size {
			return nil, toolErrorf(ErrCodeOutOfRange, "offset %d is beyond the end of %q, which is %d bytes", off, path, size)
		}
		n := size - off
		if args.Limit != nil && *args.Limit < n {
			n = *args.Limit
		}
		body = v.content[off : off+n]
	}
	if len(body) > t.maxResultBytes {
		if ranged {
			return nil, toolErrorf(ErrCodeTooLarge, "the requested range of %q is %d bytes, over the result limit of %d bytes; ask for a smaller limit", path, len(body), t.maxResultBytes)
		}
		return nil, toolErrorf(ErrCodeTooLarge, "%q is %d bytes, over the result limit of %d bytes; call again with offset and limit to read part of it", path, size, t.maxResultBytes)
	}
	return body, nil
}

type listDirArgs struct {
	Path string `json:"path"`
}

// ListDir executes list_dir for the accepted call callID and journals the
// exact bytes returned, or the error text. The result is one name per line,
// directories with a trailing "/" and symbolic links (which every tool
// refuses) with a trailing "@", in byte order.
func (t *ReadTools) ListDir(callID string, raw json.RawMessage) (Result, error) {
	content, err := t.listDir(raw)
	return t.finish(callID, content, err)
}

func (t *ReadTools) listDir(raw json.RawMessage) ([]byte, error) {
	var args listDirArgs
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
	v, err := t.view(path, staging)
	if err != nil {
		return nil, err
	}
	switch v.kind {
	case journal.ObservationAbsent:
		return nil, toolErrorf(ErrCodeNotFound, "%q not found", path)
	case journal.ObservationFile:
		return nil, toolErrorf(ErrCodeNotDirectory, "%q is a file; use read_file", path)
	}
	var out strings.Builder
	for _, e := range v.entries {
		name := e.Name
		if strings.ContainsAny(name, "\r\n\"") || strings.HasSuffix(name, "@") {
			name = strconv.Quote(name)
		}
		out.WriteString(name)
		switch e.Type {
		case "d":
			out.WriteByte('/')
		case "l":
			out.WriteByte('@')
		}
		out.WriteByte('\n')
	}
	if out.Len() > t.maxResultBytes {
		return nil, toolErrorf(ErrCodeTooLarge, "the listing of %q is %d bytes, over the result limit of %d bytes", path, out.Len(), t.maxResultBytes)
	}
	return []byte(out.String()), nil
}
