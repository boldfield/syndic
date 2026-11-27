package mcp

// This file lists a server's tools (v9 delta, section 3, "Listing"). It
// follows the listing's cursors to the end within the design's bounds and runs
// each tool through the definition rules. The page fetch is a function, so the
// listing is the same under either revision and over either transport; this
// package carries no network code. Only tools/list is ever asked for: a tool
// is never called to list it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/boldfield/syndic/internal/mcp/jcs"
)

// Bounds on a listing when the roster does not set them.
const (
	// MaxListPages is the most pages a listing may span.
	MaxListPages = 32
	// DefaultMaxTools is mcp.max_tools when the roster does not set it.
	DefaultMaxTools = 256
)

// ErrListing is wrapped by every error that leaves the whole server unoffered
// for the turn because its listing is unusable.
var ErrListing = errors.New("mcp: listing failed")

// Fetch returns the result of one tools/list request. cursor is empty for the
// first page. A failure to fetch is returned as it is.
type Fetch func(cursor string) (json.RawMessage, error)

// ListOptions bounds a listing. A zero field takes the design's value.
type ListOptions struct {
	// MaxPages is the page bound; zero means MaxListPages.
	MaxPages int
	// MaxTools is mcp.max_tools.
	MaxTools int
	// MaxDefinitionBytes is mcp.max_definition_bytes.
	MaxDefinitionBytes int
}

// ListedTool is one listed tool: its definition and pin, or why it cannot be
// offered.
type ListedTool struct {
	// Name is the name the server reported, empty when it reported none that
	// could be read.
	Name string
	// Definition is set when the tool can be offered.
	Definition *Definition
	// NotOffered is set when it cannot; Definition is then nil.
	NotOffered *NotOfferedError

	// annotations is kept whether or not the tool can be offered.
	annotations json.RawMessage
}

// Annotations is the canonical JSON of the tool's annotations, or nil. It is
// kept for a tool that cannot be offered too, when the tool could be read.
func (t ListedTool) Annotations() json.RawMessage {
	return t.annotations
}

// Listing is a server's complete listing, tools in the order reported.
type Listing struct {
	Tools []ListedTool
	// Pages is the number of pages read.
	Pages int
}

func listFailure(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrListing, fmt.Sprintf(format, args...))
}

// ListTools follows nextCursor until the server reports none. A listing that
// passes the page or tool bound, that names one tool twice, or whose page is
// not well formed fails as a whole, with an error wrapping ErrListing; a
// failure of fetch itself is returned wrapped, unchanged underneath.
func ListTools(fetch Fetch, opts ListOptions) (*Listing, error) {
	if opts.MaxPages <= 0 {
		opts.MaxPages = MaxListPages
	}
	if opts.MaxTools <= 0 {
		opts.MaxTools = DefaultMaxTools
	}
	l := &Listing{}
	seen := map[string]bool{}
	cursor := ""
	for {
		if l.Pages == opts.MaxPages {
			return nil, listFailure("more than %d pages", opts.MaxPages)
		}
		raw, err := fetch(cursor)
		if err != nil {
			return nil, fmt.Errorf("mcp: tools/list: %w", err)
		}
		l.Pages++
		var page struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor json.RawMessage   `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil || page.Tools == nil {
			return nil, listFailure("page %d is not a result with a tools array", l.Pages)
		}
		for _, rt := range page.Tools {
			if len(l.Tools) == opts.MaxTools {
				return nil, listFailure("more than %d tools", opts.MaxTools)
			}
			t, named := listTool(rt, opts.MaxDefinitionBytes)
			if named {
				if seen[t.Name] {
					return nil, listFailure("tool %q is named twice", t.Name)
				}
				seen[t.Name] = true
			}
			l.Tools = append(l.Tools, t)
		}
		next, err := nextCursor(page.NextCursor)
		if err != nil {
			return nil, listFailure("page %d: %v", l.Pages, err)
		}
		if next == "" {
			return l, nil
		}
		cursor = next
	}
}

func nextCursor(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var s string
	if raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", errors.New("nextCursor is not a string")
	}
	return s, nil
}

// listTool derives one tool. A tool that is not a JSON object with a string
// name has no name here, and named is false; it fails in BuildDefinition. An
// empty string is a name.
func listTool(raw json.RawMessage, maxBytes int) (t ListedTool, named bool) {
	var n struct {
		Name *string `json:"name"`
	}
	_ = json.Unmarshal(raw, &n)
	if n.Name != nil {
		t.Name = *n.Name
		named = true
	}
	if canon, err := jcs.Canonicalize(raw); err == nil {
		var members map[string]json.RawMessage
		if json.Unmarshal(canon, &members) == nil {
			t.annotations = members["annotations"]
		}
	}
	def, err := BuildDefinition(raw, maxBytes)
	if err != nil {
		var ne *NotOfferedError
		if !errors.As(err, &ne) {
			ne = &NotOfferedError{Reason: ReasonMalformed, Detail: err.Error()}
		}
		t.NotOffered = ne
		return t, named
	}
	t.Definition = def
	t.Name = def.Name
	return t, true
}
