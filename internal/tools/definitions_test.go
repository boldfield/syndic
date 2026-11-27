package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/boldfield/syndic/internal/transport"
)

// pinnedHash is the definitions hash: SHA-256 of testdata/definitions.json.
// Any change to a name, description, schema or to the encoding changes it.
// Update it only after verifying that the change is intentional.
const pinnedHash = "145768635332f92604df7a9005b6ee6c4acf0b833ccdb6f94b91e3b3cabe0c4d"

var fixedOrder = []string{
	NameReadFile, NameListDir, NameSearchFiles,
	NameWebFetch, NameWebSearch,
	NameWriteFile, NameEditFile, NameUnstage,
	NameBash,
}

// designArguments are the argument names the design fixes for each tool.
var designArguments = map[string][]string{
	NameReadFile:    {"path", "offset", "limit"},
	NameListDir:     {"path"},
	NameSearchFiles: {"pattern", "path"},
	NameWebFetch:    {"url"},
	NameWebSearch:   {"query"},
	NameWriteFile:   {"path", "content"},
	NameEditFile:    {"path", "old", "new"},
	NameUnstage:     {"path"},
	NameBash:        {"command", "timeout_s"},
}

var designGroups = map[string]string{
	NameReadFile:    GroupRead,
	NameListDir:     GroupRead,
	NameSearchFiles: GroupRead,
	NameWebFetch:    GroupWeb,
	NameWebSearch:   GroupWeb,
	NameWriteFile:   GroupWrite,
	NameEditFile:    GroupWrite,
	NameUnstage:     GroupWrite,
	NameBash:        GroupExec,
}

func names(t *testing.T, defs []transportTool) []string {
	t.Helper()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		if d.Type != "function" {
			t.Errorf("%s: Type = %q, want %q", d.Function.Name, d.Type, "function")
		}
		out = append(out, d.Function.Name)
	}
	return out
}

func TestCanonicalJSONMatchesGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/definitions.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	got := CanonicalJSON()
	if !bytes.Equal(got, want) {
		t.Fatalf("CanonicalJSON differs from testdata/definitions.json\n got: %s\nwant: %s", got, want)
	}
	if bytes.HasSuffix(got, []byte("\n")) {
		t.Error("canonical JSON must not end with a newline")
	}
	// The encoding is the nine definitions in the fixed order, HTML escaping off.
	var decoded []struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("canonical JSON is not valid JSON: %v", err)
	}
	if len(decoded) != len(fixedOrder) {
		t.Fatalf("canonical JSON holds %d definitions, want %d", len(decoded), len(fixedOrder))
	}
	for i, d := range decoded {
		if d.Function.Name != fixedOrder[i] || d.Type != "function" {
			t.Errorf("definition %d = %s/%s, want function/%s", i, d.Type, d.Function.Name, fixedOrder[i])
		}
	}
	if bytes.Contains(got, []byte(`&`)) || bytes.Contains(got, []byte(`<`)) || bytes.Contains(got, []byte(`>`)) {
		t.Error("canonical JSON must not HTML-escape")
	}
}

func TestDefinitionsHashPinned(t *testing.T) {
	hash1 := DefinitionsHash()
	hash2 := DefinitionsHash()
	if hash1 != hash2 {
		t.Errorf("DefinitionsHash not idempotent: %q != %q", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA-256 hex)", len(hash1))
	}
	sum := sha256.Sum256(CanonicalJSON())
	if want := hex.EncodeToString(sum[:]); hash1 != want {
		t.Errorf("DefinitionsHash = %q, want %q (computed from CanonicalJSON)", hash1, want)
	}
	if hash1 != pinnedHash {
		t.Errorf("DefinitionsHash = %q, want pinned %q; update the pinned value only if the change is intentional", hash1, pinnedHash)
	}
	// Changing one byte of a definition changes the hash.
	mutated := CanonicalJSON()
	i := bytes.Index(mutated, []byte("Read a file"))
	if i < 0 {
		t.Fatal("read_file description not found in canonical JSON")
	}
	mutated[i] = 'r'
	sum = sha256.Sum256(mutated)
	if hex.EncodeToString(sum[:]) == hash1 {
		t.Error("a one-byte change to a definition did not change the hash")
	}
}

func TestAllFixedOrder(t *testing.T) {
	got := names(t, All())
	if !reflect.DeepEqual(got, fixedOrder) {
		t.Errorf("All() = %v, want %v", got, fixedOrder)
	}
}

func TestForGroups(t *testing.T) {
	cases := []struct {
		name     string
		groups   []string
		provider bool
		want     []string
	}{
		{"empty", nil, true, nil},
		{"empty slice", []string{}, true, nil},
		{"read", []string{GroupRead}, true, []string{NameReadFile, NameListDir, NameSearchFiles}},
		{"web without provider", []string{GroupWeb}, false, []string{NameWebFetch}},
		{"web with provider", []string{GroupWeb}, true, []string{NameWebFetch, NameWebSearch}},
		{"write", []string{GroupWrite}, true, []string{NameWriteFile, NameEditFile, NameUnstage}},
		{"exec", []string{GroupExec}, true, []string{NameBash}},
		{"all with provider", []string{GroupExec, GroupRead, GroupWeb, GroupWrite}, true, fixedOrder},
		{"all without provider", []string{GroupWrite, GroupExec, GroupWeb, GroupRead}, false, []string{
			NameReadFile, NameListDir, NameSearchFiles, NameWebFetch, NameWriteFile, NameEditFile, NameUnstage, NameBash,
		}},
		{"read and exec", []string{GroupExec, GroupRead}, true, []string{
			NameReadFile, NameListDir, NameSearchFiles, NameBash,
		}},
		{"order and repetition ignored", []string{GroupWrite, GroupRead, GroupRead}, true, []string{
			NameReadFile, NameListDir, NameSearchFiles, NameWriteFile, NameEditFile, NameUnstage,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defs, err := ForGroups(tc.groups, tc.provider)
			if err != nil {
				t.Fatalf("ForGroups(%v, %v) error: %v", tc.groups, tc.provider, err)
			}
			got := names(t, defs)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ForGroups(%v, %v) = %v, want %v", tc.groups, tc.provider, got, tc.want)
			}
		})
	}
	for _, bad := range [][]string{{"shell"}, {GroupRead, "mcp"}, {""}} {
		if defs, err := ForGroups(bad, true); err == nil {
			t.Errorf("ForGroups(%v) = %v, want error for unknown group", bad, names(t, defs))
		}
	}
}

func TestLookup(t *testing.T) {
	for _, name := range fixedOrder {
		tool, group, ok := Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q) not found", name)
			continue
		}
		if tool.Function.Name != name || tool.Type != "function" {
			t.Errorf("Lookup(%q) = %s/%s", name, tool.Type, tool.Function.Name)
		}
		if group != designGroups[name] {
			t.Errorf("Lookup(%q) group = %q, want %q", name, group, designGroups[name])
		}
	}
	for _, name := range []string{"shell", "delete", "", "READ_FILE"} {
		if tool, group, ok := Lookup(name); ok {
			t.Errorf("Lookup(%q) = %v (%s), want not found", name, tool.Function.Name, group)
		}
	}
}

func TestReturnedDefinitionsAreCopies(t *testing.T) {
	before := CanonicalJSON()
	hashBefore := DefinitionsHash()

	all := All()
	all[0].Function.Name = "mutated"
	all[0].Function.Description = "mutated"
	all[0].Function.Parameters["type"] = "mutated"
	all[0].Function.Parameters["properties"].(map[string]interface{})["path"].(map[string]interface{})["description"] = "mutated"
	all[0].Function.Parameters["required"].([]string)[0] = "mutated"
	delete(all[0].Function.Parameters, "additionalProperties")

	byGroup, err := ForGroups([]string{GroupWeb}, true)
	if err != nil {
		t.Fatal(err)
	}
	byGroup[0].Function.Parameters["properties"].(map[string]interface{})["url"] = "mutated"

	one, _, _ := Lookup(NameUnstage)
	one.Function.Parameters["required"] = []string{}

	again := All()
	if again[0].Function.Name != NameReadFile || again[0].Function.Description == "mutated" {
		t.Error("mutating a returned definition changed the next All()")
	}
	if again[0].Function.Parameters["type"] != "object" || again[0].Function.Parameters["additionalProperties"] != false {
		t.Error("mutating a returned schema changed the next All()")
	}
	if again[0].Function.Parameters["properties"].(map[string]interface{})["path"].(map[string]interface{})["description"] == "mutated" {
		t.Error("mutating a nested property changed the next All()")
	}
	if again[0].Function.Parameters["required"].([]string)[0] != "path" {
		t.Error("mutating the required list changed the next All()")
	}
	web, _ := ForGroups([]string{GroupWeb}, true)
	if _, isMap := web[0].Function.Parameters["properties"].(map[string]interface{})["url"].(map[string]interface{}); !isMap {
		t.Error("mutating a ForGroups schema changed the next ForGroups()")
	}
	unstage, _, _ := Lookup(NameUnstage)
	if req := unstage.Function.Parameters["required"].([]string); len(req) != 1 {
		t.Error("mutating a Lookup schema changed the next Lookup()")
	}
	if !bytes.Equal(CanonicalJSON(), before) || DefinitionsHash() != hashBefore {
		t.Error("mutating returned definitions changed the canonical JSON or hash")
	}
	// The canonical bytes are copies too.
	b := CanonicalJSON()
	b[0] = 'x'
	if !bytes.Equal(CanonicalJSON(), before) {
		t.Error("mutating the returned canonical JSON changed the next call")
	}
}

func TestSchemasAreWellFormed(t *testing.T) {
	for _, tool := range All() {
		name := tool.Function.Name
		params := tool.Function.Parameters
		if params["type"] != "object" {
			t.Errorf("%s: schema type = %v, want object", name, params["type"])
		}
		if ap, ok := params["additionalProperties"].(bool); !ok || ap {
			t.Errorf("%s: additionalProperties = %v, want false", name, params["additionalProperties"])
		}
		props, ok := params["properties"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s: properties missing", name)
		}
		required, ok := params["required"].([]string)
		if !ok {
			t.Fatalf("%s: required missing", name)
		}
		if len(required) == 0 {
			t.Errorf("%s: no required property", name)
		}
		for _, r := range required {
			if _, declared := props[r]; !declared {
				t.Errorf("%s: required %q is not a declared property", name, r)
			}
		}
		wantArgs := designArguments[name]
		if len(props) != len(wantArgs) {
			t.Errorf("%s: %d properties, want %d", name, len(props), len(wantArgs))
		}
		for _, a := range wantArgs {
			p, declared := props[a].(map[string]interface{})
			if !declared {
				t.Errorf("%s: design argument %q is not a declared property", name, a)
				continue
			}
			if typ, _ := p["type"].(string); typ != "string" && typ != "integer" {
				t.Errorf("%s.%s: type = %v", name, a, p["type"])
			}
			if d, _ := p["description"].(string); d == "" {
				t.Errorf("%s.%s: no description", name, a)
			}
		}
		if d := tool.Function.Description; d == "" || d[0] == '\n' || d[len(d)-1] == '\n' {
			t.Errorf("%s: description empty or has a leading or trailing newline", name)
		}
	}
	// The integer properties carry their minimums.
	rf, _, _ := Lookup(NameReadFile)
	props := rf.Function.Parameters["properties"].(map[string]interface{})
	if props["offset"].(map[string]interface{})["minimum"] != 0 {
		t.Errorf("read_file.offset minimum = %v, want 0", props["offset"].(map[string]interface{})["minimum"])
	}
	if props["limit"].(map[string]interface{})["minimum"] != 1 {
		t.Errorf("read_file.limit minimum = %v, want 1", props["limit"].(map[string]interface{})["minimum"])
	}
}

// The bash definition's text, as fixed by the task specification. A model
// receives these bytes, so they are compared exactly.
const (
	bashDescription        = "Run a shell command and return its output. The command runs with `bash -c` in a private copy of the workspace that includes the changes you have staged in this turn. Standard input is empty and there is no terminal, so the command must not wait for input. Standard output and standard error are returned together, in the order written. Anything the command writes to files is discarded when it ends and does not change the workspace or your staged changes: to propose a change, use `write_file` or `edit_file`. Each command starts from a fresh copy, so nothing carries over from one command to the next; do related steps in one command. The command is checked before it runs and may be refused; a refusal is returned as an error that gives the reason. A command that exits with a non-zero status is returned as a result with its exit code, not as an error. Output beyond the size limit is cut off and the result says so. There is no network access unless the operator has allowed specific hosts."
	bashCommandDescription = "The command line to run with bash -c."
	bashTimeoutDescription = "Seconds to allow before the command is stopped. Default 120. A value above the maximum is reduced to the maximum."
)

func TestBashDefinition(t *testing.T) {
	tool, group, ok := Lookup(NameBash)
	if !ok {
		t.Fatal("Lookup(bash) not found")
	}
	if NameBash != "bash" || GroupExec != "exec" || group != GroupExec {
		t.Errorf("bash name/group = %q/%q, want bash/exec", NameBash, group)
	}
	if tool.Function.Description != bashDescription {
		t.Errorf("bash description = %q, want %q", tool.Function.Description, bashDescription)
	}
	params := tool.Function.Parameters
	props := params["properties"].(map[string]interface{})
	command := props["command"].(map[string]interface{})
	if command["type"] != "string" || command["description"] != bashCommandDescription || len(command) != 2 {
		t.Errorf("bash.command = %v", command)
	}
	timeout := props["timeout_s"].(map[string]interface{})
	if timeout["type"] != "integer" || timeout["description"] != bashTimeoutDescription || timeout["minimum"] != 1 || len(timeout) != 3 {
		t.Errorf("bash.timeout_s = %v", timeout)
	}
	if req := params["required"].([]string); !reflect.DeepEqual(req, []string{"command"}) {
		t.Errorf("bash required = %v, want [command]", req)
	}
}

type transportTool = transport.Tool
