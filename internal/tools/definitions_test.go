package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

// pinnedHash is DefinitionsHash for the definitions in the specification.
// Update it only after verifying a definition change is intentional.
const pinnedHash = "5dab091d469d2927875f90601c9f0dc2bdae633935589f701abf885614bfbd58"

var fixedOrder = []string{"read_file", "list_dir", "search_files", "web_fetch", "web_search", "write_file", "edit_file", "unstage"}

// wantProp is one expected schema property, from the specification.
type wantProp struct {
	name, typ   string
	minimum     *int
	required    bool
	description string
}

// wantTools is the specification's text for every definition, written out
// independently of definitions.go so that a change to either is caught.
var wantTools = []struct {
	name, group, description string
	props                    []wantProp
}{
	{"read_file", GroupRead,
		"Read a file from the workspace. `path` is relative to the workspace root. Returns the file's content. If the file is larger than the result limit, the call returns an error stating the file's size; call again with `offset` and `limit` to read part of it. Paths outside the workspace, symbolic links and files the operator has protected are refused.",
		[]wantProp{
			{"path", "string", nil, true, "Path of the file, relative to the workspace root."},
			{"offset", "integer", intPtr(0), false, "Byte offset to start reading at. Default 0."},
			{"limit", "integer", intPtr(1), false, "Maximum number of bytes to return. Default: to the end of the file."},
		}},
	{"list_dir", GroupRead,
		"List a directory in the workspace. `path` is relative to the workspace root; use `.` for the root itself. Returns the names of the entries directly inside it and marks which are directories. It does not descend into subdirectories.",
		[]wantProp{
			{"path", "string", nil, true, "Path of the directory, relative to the workspace root."},
		}},
	{"search_files", GroupRead,
		"Search the text of files in the workspace. `pattern` is a regular expression in RE2 syntax, matched against each line. `path`, if given, limits the search to that file or to that directory and everything under it; the default is the whole workspace. Returns the matching lines with their file paths and line numbers. Files the operator has protected are skipped.",
		[]wantProp{
			{"pattern", "string", nil, true, "Regular expression in RE2 syntax, matched against each line."},
			{"path", "string", nil, false, "File or directory to search, relative to the workspace root. Default: the whole workspace."},
		}},
	{"web_fetch", GroupWeb,
		"Fetch a public web page or document with an HTTP GET request. `url` must be an absolute `http` or `https` URL. Returns the response body. The request is checked before it is sent and may be refused; a refusal is returned as an error that gives the reason. A redirect to a different host is not followed: its target is returned so that you can decide whether to fetch it. Do not put workspace content, secrets or personal data in the URL.",
		[]wantProp{
			{"url", "string", nil, true, "Absolute http or https URL to fetch."},
		}},
	{"web_search", GroupWeb,
		"Search the web. Returns a list of results, each with a title, a URL and a short extract. The query is checked before it is sent and may be refused; a refusal is returned as an error that gives the reason. Do not put workspace content, secrets or personal data in the query.",
		[]wantProp{
			{"query", "string", nil, true, "The search text."},
		}},
	{"write_file", GroupWrite,
		"Propose creating a file, or replacing the whole content of an existing file. `path` is relative to the workspace root and `content` is the complete new content. Nothing is written to the workspace now: the change is staged, shown to the reviewers as a diff alongside your answer, and applied only if the user chooses to apply it. Reading the path later in this turn returns the staged content.",
		[]wantProp{
			{"path", "string", nil, true, "Path of the file, relative to the workspace root."},
			{"content", "string", nil, true, "The complete new content of the file."},
		}},
	{"edit_file", GroupWrite,
		"Propose replacing one piece of text in a file. `old` must match exactly one place in the file's current content, which is its staged content if you have already changed it in this turn; `new` replaces it. If `old` does not match, or matches more than one place, the call returns an error and nothing changes. The change is staged in the same way as `write_file`: nothing is written to the workspace now.",
		[]wantProp{
			{"path", "string", nil, true, "Path of the file, relative to the workspace root."},
			{"old", "string", nil, true, "The exact text to replace. It must occur exactly once in the file."},
			{"new", "string", nil, true, "The text to put in its place."},
		}},
	{"unstage", GroupWrite,
		"Withdraw your staged change to a path. The path then reads as it did before you changed it, and it is left out of the proposed changes. Use it to retract a file you proposed creating or changing.",
		[]wantProp{
			{"path", "string", nil, true, "Path whose staged change to withdraw, relative to the workspace root."},
		}},
}

func toolNames(ts []transport.Tool) []string {
	out := make([]string, len(ts))
	for i, tl := range ts {
		out[i] = tl.Function.Name
	}
	return out
}

func TestGoldenCanonicalJSON(t *testing.T) {
	golden, err := os.ReadFile("testdata/definitions.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got := canonicalJSON(); !bytes.Equal(got, golden) {
		t.Errorf("canonical JSON differs from testdata/definitions.json:\ngot:  %s\nwant: %s", got, golden)
	}
	sum := sha256.Sum256(golden)
	if got, want := DefinitionsHash(), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("DefinitionsHash = %s, want SHA-256 of golden %s", got, want)
	}
}

func TestDefinitionsHashPinned(t *testing.T) {
	if got := DefinitionsHash(); got != pinnedHash {
		t.Errorf("DefinitionsHash = %s, want pinned %s; update the pinned value only if the change is intentional", got, pinnedHash)
	}
}

func TestDefinitionsMatchSpecification(t *testing.T) {
	all := All()
	if got := toolNames(all); !reflect.DeepEqual(got, fixedOrder) {
		t.Fatalf("All order = %v, want %v", got, fixedOrder)
	}
	for i, w := range wantTools {
		tl := all[i]
		if tl.Type != "function" {
			t.Errorf("%s: Type = %q, want function", w.name, tl.Type)
		}
		if tl.Function.Name != w.name {
			t.Errorf("tool %d: name = %q, want %q", i, tl.Function.Name, w.name)
		}
		if tl.Function.Description != w.description {
			t.Errorf("%s: description = %q, want %q", w.name, tl.Function.Description, w.description)
		}
		params := tl.Function.Parameters
		if params["type"] != "object" {
			t.Errorf("%s: schema type = %v, want object", w.name, params["type"])
		}
		if params["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties = %v, want false", w.name, params["additionalProperties"])
		}
		props := params["properties"].(map[string]interface{})
		if len(props) != len(w.props) {
			t.Errorf("%s: %d properties, want %d", w.name, len(props), len(w.props))
		}
		var wantRequired []interface{}
		for _, wp := range w.props {
			p, ok := props[wp.name].(map[string]interface{})
			if !ok {
				t.Errorf("%s: missing property %q", w.name, wp.name)
				continue
			}
			want := map[string]interface{}{"type": wp.typ, "description": wp.description}
			if wp.minimum != nil {
				want["minimum"] = *wp.minimum
			}
			if !reflect.DeepEqual(p, want) {
				t.Errorf("%s.%s = %#v, want %#v", w.name, wp.name, p, want)
			}
			if wp.required {
				wantRequired = append(wantRequired, wp.name)
			}
		}
		required := params["required"].([]interface{})
		if !reflect.DeepEqual(required, wantRequired) {
			t.Errorf("%s: required = %v, want %v", w.name, required, wantRequired)
		}
		for _, r := range required {
			if _, ok := props[r.(string)]; !ok {
				t.Errorf("%s: required %v is not a declared property", w.name, r)
			}
		}
		texts := []string{tl.Function.Description}
		for _, wp := range w.props {
			texts = append(texts, wp.description)
		}
		for _, s := range texts {
			if strings.TrimSpace(s) != s {
				t.Errorf("%s: description %q has leading or trailing whitespace", w.name, s)
			}
		}
	}
}

func TestForGroups(t *testing.T) {
	cases := []struct {
		name     string
		groups   []string
		provider bool
		want     []string
	}{
		{"read", []string{"read"}, false, []string{"read_file", "list_dir", "search_files"}},
		{"web without provider", []string{"web"}, false, []string{"web_fetch"}},
		{"web with provider", []string{"web"}, true, []string{"web_fetch", "web_search"}},
		{"write", []string{"write"}, false, []string{"write_file", "edit_file", "unstage"}},
		{"all with provider", []string{"write", "web", "read"}, true, fixedOrder},
		{"empty", nil, true, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ForGroups(c.groups, c.provider)
			if err != nil {
				t.Fatalf("ForGroups: %v", err)
			}
			if names := toolNames(got); !reflect.DeepEqual(names, c.want) {
				t.Errorf("ForGroups(%v, %v) = %v, want %v", c.groups, c.provider, names, c.want)
			}
		})
	}
	if _, err := ForGroups([]string{"read", "shell"}, false); err == nil {
		t.Error("ForGroups with unknown group: want error")
	}
}

func TestLookup(t *testing.T) {
	for _, w := range wantTools {
		tl, group, ok := Lookup(w.name)
		if !ok {
			t.Errorf("Lookup(%q) not found", w.name)
			continue
		}
		if tl.Function.Name != w.name || group != w.group {
			t.Errorf("Lookup(%q) = %q, %q; want %q, %q", w.name, tl.Function.Name, group, w.name, w.group)
		}
	}
	if _, _, ok := Lookup("shell"); ok {
		t.Error("Lookup(shell): want not found")
	}
}

func TestReturnedDefinitionsAreCopies(t *testing.T) {
	before := DefinitionsHash()
	beforeJSON := canonicalJSON()

	all := All()
	all[0].Function.Description = "changed"
	params := all[0].Function.Parameters
	params["additionalProperties"] = true
	params["properties"].(map[string]interface{})["path"].(map[string]interface{})["description"] = "changed"
	params["required"].([]interface{})[0] = "changed"

	groups, _ := ForGroups([]string{"web"}, true)
	groups[0].Function.Parameters["properties"].(map[string]interface{})["extra"] = map[string]interface{}{}

	tl, _, _ := Lookup("unstage")
	tl.Function.Parameters["required"] = []interface{}{}

	if !bytes.Equal(canonicalJSON(), beforeJSON) {
		t.Error("mutating returned definitions changed the canonical encoding")
	}
	if got, _, _ := Lookup("read_file"); got.Function.Description != wantTools[0].description {
		t.Error("mutating a returned definition changed the next Lookup")
	}
	sum := sha256.Sum256(canonicalJSON())
	if hex.EncodeToString(sum[:]) != before {
		t.Error("mutating returned definitions changed the hash")
	}
}
