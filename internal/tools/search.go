package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/observation"
)

type searchFilesArgs struct {
	Pattern string  `json:"pattern"`
	Path    *string `json:"path"`
}

// binarySniff is how much of a file is checked for a NUL byte before the file
// is treated as binary and left out of the search.
const binarySniff = 8000

// SearchFiles executes search_files for the accepted call callID and journals
// the exact bytes returned, or the error text. Every directory in the scope
// and every file it opens is observed through the turn's record, so a repeated
// search over the same scope sees the same tree; protected paths, symbolic
// links and other non-regular entries are left out. The result is one
// "path:line:text" line per match.
func (t *ReadTools) SearchFiles(callID string, raw json.RawMessage) (Result, error) {
	content, err := t.searchFiles(raw)
	return t.finish(callID, content, err)
}

func (t *ReadTools) searchFiles(raw json.RawMessage) ([]byte, error) {
	var args searchFilesArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return nil, toolErrorf(ErrCodeBadArguments, "invalid pattern: %v", err)
	}
	scope := observation.Root
	if args.Path != nil {
		if scope, err = cleanPath(*args.Path); err != nil {
			return nil, err
		}
	}
	if t.fs.IsProtected(scope) {
		return nil, toolErrorf(ErrCodeProtected, "%q is protected", scope)
	}
	staging, err := t.loadStaging()
	if err != nil {
		return nil, err
	}
	s := &search{t: t, re: re, staging: staging}
	if err := s.walk(scope, true); err != nil {
		return nil, err
	}
	if s.out.Len() == 0 {
		return []byte("no matches"), nil
	}
	if s.out.Len() > t.maxResultBytes {
		return nil, toolErrorf(ErrCodeTooLarge, "the matches are %d bytes, over the result limit of %d bytes; narrow the pattern or the path", s.out.Len(), t.maxResultBytes)
	}
	return s.out.Bytes(), nil
}

// search is one traversal. The output stops growing once it is over the limit,
// since the call will fail with the size error anyway; the walk still runs to
// the end so that its observations do not depend on the pattern.
type search struct {
	t       *ReadTools
	re      *regexp.Regexp
	staging map[string]string
	out     bytes.Buffer
}

func (s *search) walk(path string, top bool) error {
	v, err := s.t.view(path, s.staging)
	if err != nil {
		return err
	}
	switch v.kind {
	case journal.ObservationAbsent:
		if top {
			return toolErrorf(ErrCodeNotFound, "%q not found", path)
		}
	case journal.ObservationFile:
		s.match(path, v.content)
	case journal.ObservationListing:
		for _, e := range v.entries {
			if e.Type != "d" && e.Type != "f" {
				continue
			}
			child := e.Name
			if path != observation.Root {
				child = path + "/" + e.Name
			}
			if s.t.fs.IsProtected(child) {
				continue
			}
			if err := s.walk(child, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *search) match(path string, content []byte) {
	if bytes.IndexByte(content[:min(len(content), binarySniff)], 0) >= 0 {
		return
	}
	if s.out.Len() > s.t.maxResultBytes {
		return
	}
	if len(content) == 0 {
		return // an empty file has no lines
	}
	lines := bytes.Split(content, []byte("\n"))
	if n := len(lines); len(lines[n-1]) == 0 {
		lines = lines[:n-1] // a final newline ends a line; it does not start one
	}
	for i, line := range lines {
		line = bytes.TrimSuffix(line, []byte("\r")) // CRLF is a line terminator
		if !s.re.Match(line) {
			continue
		}
		name := path
		if strings.ContainsAny(name, "\r\n:\"") {
			name = strconv.Quote(name)
		}
		fmt.Fprintf(&s.out, "%s:%d:%s\n", name, i+1, line)
	}
}
