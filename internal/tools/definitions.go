// Package tools holds the engine's tool definitions and, later, their execution.
package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/boldfield/syndic/internal/transport"
)

// Tool groups a role may hold.
const (
	GroupRead  = "read"
	GroupWeb   = "web"
	GroupWrite = "write"
)

// definition is one canonical tool definition and the group that offers it.
type definition struct {
	group       string
	name        string
	description string
	properties  []property
}

// property is one argument of a tool's schema.
type property struct {
	name        string
	typ         string // "string" or "integer"
	minimum     *int
	required    bool
	description string
}

func intPtr(n int) *int { return &n }

// definitions are the eight tools in their fixed order. Every string here is
// sent to a model; change none of them without changing the specification.
var definitions = []definition{
	{
		group:       GroupRead,
		name:        "read_file",
		description: "Read a file from the workspace. `path` is relative to the workspace root. Returns the file's content. If the file is larger than the result limit, the call returns an error stating the file's size; call again with `offset` and `limit` to read part of it. Paths outside the workspace, symbolic links and files the operator has protected are refused.",
		properties: []property{
			{name: "path", typ: "string", required: true, description: "Path of the file, relative to the workspace root."},
			{name: "offset", typ: "integer", minimum: intPtr(0), description: "Byte offset to start reading at. Default 0."},
			{name: "limit", typ: "integer", minimum: intPtr(1), description: "Maximum number of bytes to return. Default: to the end of the file."},
		},
	},
	{
		group:       GroupRead,
		name:        "list_dir",
		description: "List a directory in the workspace. `path` is relative to the workspace root; use `.` for the root itself. Returns the names of the entries directly inside it and marks which are directories. It does not descend into subdirectories.",
		properties: []property{
			{name: "path", typ: "string", required: true, description: "Path of the directory, relative to the workspace root."},
		},
	},
	{
		group:       GroupRead,
		name:        "search_files",
		description: "Search the text of files in the workspace. `pattern` is a regular expression in RE2 syntax, matched against each line. `path`, if given, limits the search to that file or to that directory and everything under it; the default is the whole workspace. Returns the matching lines with their file paths and line numbers. Files the operator has protected are skipped.",
		properties: []property{
			{name: "pattern", typ: "string", required: true, description: "Regular expression in RE2 syntax, matched against each line."},
			{name: "path", typ: "string", description: "File or directory to search, relative to the workspace root. Default: the whole workspace."},
		},
	},
	{
		group:       GroupWeb,
		name:        "web_fetch",
		description: "Fetch a public web page or document with an HTTP GET request. `url` must be an absolute `http` or `https` URL. Returns the response body. The request is checked before it is sent and may be refused; a refusal is returned as an error that gives the reason. A redirect to a different host is not followed: its target is returned so that you can decide whether to fetch it. Do not put workspace content, secrets or personal data in the URL.",
		properties: []property{
			{name: "url", typ: "string", required: true, description: "Absolute http or https URL to fetch."},
		},
	},
	{
		group:       GroupWeb,
		name:        "web_search",
		description: "Search the web. Returns a list of results, each with a title, a URL and a short extract. The query is checked before it is sent and may be refused; a refusal is returned as an error that gives the reason. Do not put workspace content, secrets or personal data in the query.",
		properties: []property{
			{name: "query", typ: "string", required: true, description: "The search text."},
		},
	},
	{
		group:       GroupWrite,
		name:        "write_file",
		description: "Propose creating a file, or replacing the whole content of an existing file. `path` is relative to the workspace root and `content` is the complete new content. Nothing is written to the workspace now: the change is staged, shown to the reviewers as a diff alongside your answer, and applied only if the user chooses to apply it. Reading the path later in this turn returns the staged content.",
		properties: []property{
			{name: "path", typ: "string", required: true, description: "Path of the file, relative to the workspace root."},
			{name: "content", typ: "string", required: true, description: "The complete new content of the file."},
		},
	},
	{
		group:       GroupWrite,
		name:        "edit_file",
		description: "Propose replacing one piece of text in a file. `old` must match exactly one place in the file's current content, which is its staged content if you have already changed it in this turn; `new` replaces it. If `old` does not match, or matches more than one place, the call returns an error and nothing changes. The change is staged in the same way as `write_file`: nothing is written to the workspace now.",
		properties: []property{
			{name: "path", typ: "string", required: true, description: "Path of the file, relative to the workspace root."},
			{name: "old", typ: "string", required: true, description: "The exact text to replace. It must occur exactly once in the file."},
			{name: "new", typ: "string", required: true, description: "The text to put in its place."},
		},
	},
	{
		group:       GroupWrite,
		name:        "unstage",
		description: "Withdraw your staged change to a path. The path then reads as it did before you changed it, and it is left out of the proposed changes. Use it to retract a file you proposed creating or changing.",
		properties: []property{
			{name: "path", typ: "string", required: true, description: "Path whose staged change to withdraw, relative to the workspace root."},
		},
	},
}

// tool builds a fresh transport.Tool for d. Every call returns new maps and
// slices, so a caller that mutates the result cannot change the canonical value.
func (d definition) tool() transport.Tool {
	props := make(map[string]interface{}, len(d.properties))
	required := []interface{}{}
	for _, p := range d.properties {
		schema := map[string]interface{}{
			"type":        p.typ,
			"description": p.description,
		}
		if p.minimum != nil {
			schema["minimum"] = *p.minimum
		}
		props[p.name] = schema
		if p.required {
			required = append(required, p.name)
		}
	}
	return transport.Tool{
		Type: "function",
		Function: transport.ToolFunction{
			Name:        d.name,
			Description: d.description,
			Parameters: map[string]interface{}{
				"type":                 "object",
				"properties":           props,
				"required":             required,
				"additionalProperties": false,
			},
		},
	}
}

// All returns all eight definitions in the fixed order, whatever a preset enables.
func All() []transport.Tool {
	out := make([]transport.Tool, len(definitions))
	for i, d := range definitions {
		out[i] = d.tool()
	}
	return out
}

// ForGroups returns the definitions offered to a role holding groups, in the
// fixed order: read_file, list_dir and search_files for read; web_fetch for
// web, and web_search too only when searchProvider is true; write_file,
// edit_file and unstage for write. An unknown group is an error; no groups
// gives no definitions.
func ForGroups(groups []string, searchProvider bool) ([]transport.Tool, error) {
	held := make(map[string]bool, len(groups))
	for _, g := range groups {
		switch g {
		case GroupRead, GroupWeb, GroupWrite:
			held[g] = true
		default:
			return nil, fmt.Errorf("unknown tool group %q", g)
		}
	}
	var out []transport.Tool
	for _, d := range definitions {
		if !held[d.group] {
			continue
		}
		if d.name == "web_search" && !searchProvider {
			continue
		}
		out = append(out, d.tool())
	}
	return out, nil
}

// Lookup returns the definition named name and its group, and false when no
// tool has that name.
func Lookup(name string) (transport.Tool, string, bool) {
	for _, d := range definitions {
		if d.name == name {
			return d.tool(), d.group, true
		}
	}
	return transport.Tool{}, "", false
}

// canonicalJSON encodes all eight definitions, in the fixed order, as a JSON
// array of objects in the shape sent on the wire:
//
//	{"function":{"description":…,"name":…,"parameters":{…}},"type":"function"}
//
// It is encoding/json output of maps (so object keys are sorted at every
// level), with no insignificant whitespace, no trailing newline, and HTML
// escaping off. testdata/definitions.json holds these exact bytes.
func canonicalJSON() []byte {
	all := All()
	wire := make([]interface{}, len(all))
	for i, t := range all {
		wire[i] = map[string]interface{}{
			"type": t.Type,
			"function": map[string]interface{}{
				"name":        t.Function.Name,
				"description": t.Function.Description,
				"parameters":  t.Function.Parameters,
			},
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(wire); err != nil {
		panic(fmt.Sprintf("encode tool definitions: %v", err))
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

var (
	hashOnce  sync.Once
	hashCache string
)

// DefinitionsHash is the lowercase hex SHA-256 of canonicalJSON.
func DefinitionsHash() string {
	hashOnce.Do(func() {
		h := sha256.Sum256(canonicalJSON())
		hashCache = hex.EncodeToString(h[:])
	})
	return hashCache
}
