// Package tools holds the canonical definitions of the engine's tools: the
// eight function tools of the v5 delta's section 6, with their descriptions
// and JSON schemas. Every place that counts, validates against or sends a tool
// definition reads it from here, so every model receives the same bytes.
//
// This file defines nothing but the definitions. It executes no tool,
// validates no call and sends no request.
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

// The tool groups a preset may enable.
const (
	GroupRead  = "read"
	GroupWeb   = "web"
	GroupWrite = "write"
)

// The tool names, in the fixed order of the definitions.
const (
	NameReadFile    = "read_file"
	NameListDir     = "list_dir"
	NameSearchFiles = "search_files"
	NameWebFetch    = "web_fetch"
	NameWebSearch   = "web_search"
	NameWriteFile   = "write_file"
	NameEditFile    = "edit_file"
	NameUnstage     = "unstage"
)

// definition pairs a tool with the group that enables it.
type definition struct {
	group string
	tool  transport.Tool
}

// property builds one JSON Schema property of the given type with a
// description. extra entries (such as "minimum") are added to it.
func property(typ, description string, extra ...map[string]interface{}) map[string]interface{} {
	p := map[string]interface{}{
		"type":        typ,
		"description": description,
	}
	for _, e := range extra {
		for k, v := range e {
			p[k] = v
		}
	}
	return p
}

// schema builds a tool's parameter schema: an object with the given
// properties, the given required names and no additional properties.
func schema(properties map[string]interface{}, required ...string) map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func function(group, name, description string, parameters map[string]interface{}) definition {
	return definition{
		group: group,
		tool: transport.Tool{
			Type: "function",
			Function: transport.ToolFunction{
				Name:        name,
				Description: description,
				Parameters:  parameters,
			},
		},
	}
}

// canonical is the eight definitions in the fixed order. It is never handed
// out: every accessor returns a deep copy.
var canonical = []definition{
	function(GroupRead, NameReadFile,
		"Read a file from the workspace. `path` is relative to the workspace root. Returns the file's content. If the file is larger than the result limit, the call returns an error stating the file's size; call again with `offset` and `limit` to read part of it. Paths outside the workspace, symbolic links and files the operator has protected are refused.",
		schema(map[string]interface{}{
			"path":   property("string", "Path of the file, relative to the workspace root."),
			"offset": property("integer", "Byte offset to start reading at. Default 0.", map[string]interface{}{"minimum": 0}),
			"limit":  property("integer", "Maximum number of bytes to return. Default: to the end of the file.", map[string]interface{}{"minimum": 1}),
		}, "path"),
	),
	function(GroupRead, NameListDir,
		"List a directory in the workspace. `path` is relative to the workspace root; use `.` for the root itself. Returns the names of the entries directly inside it and marks which are directories. It does not descend into subdirectories.",
		schema(map[string]interface{}{
			"path": property("string", "Path of the directory, relative to the workspace root."),
		}, "path"),
	),
	function(GroupRead, NameSearchFiles,
		"Search the text of files in the workspace. `pattern` is a regular expression in RE2 syntax, matched against each line. `path`, if given, limits the search to that file or to that directory and everything under it; the default is the whole workspace. Returns the matching lines with their file paths and line numbers. Files the operator has protected are skipped.",
		schema(map[string]interface{}{
			"pattern": property("string", "Regular expression in RE2 syntax, matched against each line."),
			"path":    property("string", "File or directory to search, relative to the workspace root. Default: the whole workspace."),
		}, "pattern"),
	),
	function(GroupWeb, NameWebFetch,
		"Fetch a public web page or document with an HTTP GET request. `url` must be an absolute `http` or `https` URL. Returns the response body. The request is checked before it is sent and may be refused; a refusal is returned as an error that gives the reason. A redirect to a different host is not followed: its target is returned so that you can decide whether to fetch it. Do not put workspace content, secrets or personal data in the URL.",
		schema(map[string]interface{}{
			"url": property("string", "Absolute http or https URL to fetch."),
		}, "url"),
	),
	function(GroupWeb, NameWebSearch,
		"Search the web. Returns a list of results, each with a title, a URL and a short extract. The query is checked before it is sent and may be refused; a refusal is returned as an error that gives the reason. Do not put workspace content, secrets or personal data in the query.",
		schema(map[string]interface{}{
			"query": property("string", "The search text."),
		}, "query"),
	),
	function(GroupWrite, NameWriteFile,
		"Propose creating a file, or replacing the whole content of an existing file. `path` is relative to the workspace root and `content` is the complete new content. Nothing is written to the workspace now: the change is staged, shown to the reviewers as a diff alongside your answer, and applied only if the user chooses to apply it. Reading the path later in this turn returns the staged content.",
		schema(map[string]interface{}{
			"path":    property("string", "Path of the file, relative to the workspace root."),
			"content": property("string", "The complete new content of the file."),
		}, "path", "content"),
	),
	function(GroupWrite, NameEditFile,
		"Propose replacing one piece of text in a file. `old` must match exactly one place in the file's current content, which is its staged content if you have already changed it in this turn; `new` replaces it. If `old` does not match, or matches more than one place, the call returns an error and nothing changes. The change is staged in the same way as `write_file`: nothing is written to the workspace now.",
		schema(map[string]interface{}{
			"path": property("string", "Path of the file, relative to the workspace root."),
			"old":  property("string", "The exact text to replace. It must occur exactly once in the file."),
			"new":  property("string", "The text to put in its place."),
		}, "path", "old", "new"),
	),
	function(GroupWrite, NameUnstage,
		"Withdraw your staged change to a path. The path then reads as it did before you changed it, and it is left out of the proposed changes. Use it to retract a file you proposed creating or changing.",
		schema(map[string]interface{}{
			"path": property("string", "Path whose staged change to withdraw, relative to the workspace root."),
		}, "path"),
	),
}

// All returns copies of all eight definitions in the fixed order, whatever a
// preset enables.
func All() []transport.Tool {
	out := make([]transport.Tool, 0, len(canonical))
	for _, d := range canonical {
		out = append(out, cloneTool(d.tool))
	}
	return out
}

// ForGroups returns copies of the definitions a role holding the given groups
// receives, in the fixed order. The groups are a subset of GroupRead, GroupWeb
// and GroupWrite; their order and any repetition are ignored. web_search is
// included only when a search provider is configured. An unknown group is an
// error, and an empty set returns no definitions.
func ForGroups(groups []string, searchProvider bool) ([]transport.Tool, error) {
	enabled := make(map[string]bool, len(groups))
	for _, g := range groups {
		switch g {
		case GroupRead, GroupWeb, GroupWrite:
			enabled[g] = true
		default:
			return nil, fmt.Errorf("unknown tool group %q", g)
		}
	}
	var out []transport.Tool
	for _, d := range canonical {
		if !enabled[d.group] {
			continue
		}
		if d.tool.Function.Name == NameWebSearch && !searchProvider {
			continue
		}
		out = append(out, cloneTool(d.tool))
	}
	return out, nil
}

// Lookup returns a copy of the definition with the given name and the group
// that enables it. ok is false when no tool has that name.
func Lookup(name string) (tool transport.Tool, group string, ok bool) {
	for _, d := range canonical {
		if d.tool.Function.Name == name {
			return cloneTool(d.tool), d.group, true
		}
	}
	return transport.Tool{}, "", false
}

// CanonicalJSON returns the canonical JSON encoding of all eight definitions
// in the fixed order: the exact bytes DefinitionsHash is computed over, and
// the content of testdata/definitions.json.
//
// The encoding is: a JSON array of the definitions, each rendered as the
// object {"type": <Type>, "function": {"name": <Name>, "description":
// <Description>, "parameters": <Parameters>}}; encoded by encoding/json with
// object keys sorted bytewise at every level (its ordering for maps), no
// insignificant whitespace, HTML escaping off (SetEscapeHTML(false), so
// "<", ">" and "&" are written as themselves) and no trailing newline.
func CanonicalJSON() []byte {
	initCanonical()
	return bytes.Clone(canonicalBytes)
}

// DefinitionsHash returns the definitions hash: the lowercase hex SHA-256 of
// CanonicalJSON. It covers all eight definitions whatever a preset enables, so
// a change to any definition changes it.
func DefinitionsHash() string {
	initCanonical()
	return canonicalHash
}

var (
	canonicalOnce  sync.Once
	canonicalBytes []byte
	canonicalHash  string
)

func initCanonical() {
	canonicalOnce.Do(func() {
		b, err := encodeCanonical(canonical)
		if err != nil {
			// The definitions are fixed values of encodable types; a failure
			// here is a programming error, not a runtime condition.
			panic(fmt.Sprintf("tools: encode canonical definitions: %v", err))
		}
		sum := sha256.Sum256(b)
		canonicalBytes = b
		canonicalHash = hex.EncodeToString(sum[:])
	})
}

// encodeCanonical renders defs in the encoding documented on CanonicalJSON.
func encodeCanonical(defs []definition) ([]byte, error) {
	arr := make([]map[string]interface{}, 0, len(defs))
	for _, d := range defs {
		arr = append(arr, map[string]interface{}{
			"type": d.tool.Type,
			"function": map[string]interface{}{
				"name":        d.tool.Function.Name,
				"description": d.tool.Function.Description,
				"parameters":  d.tool.Function.Parameters,
			},
		})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(arr); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// cloneTool deep-copies a definition, including its schema, so a caller
// cannot change the canonical values.
func cloneTool(t transport.Tool) transport.Tool {
	t.Function.Parameters = cloneMap(t.Function.Parameters)
	return t
}

func cloneMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return cloneMap(val)
	case []interface{}:
		out := make([]interface{}, len(val))
		for i, item := range val {
			out[i] = cloneValue(item)
		}
		return out
	case []string:
		out := make([]string, len(val))
		copy(out, val)
		return out
	default:
		return v
	}
}
