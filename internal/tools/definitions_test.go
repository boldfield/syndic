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
const pinnedHash = "5dab091d469d2927875f90601c9f0dc2bdae633935589f701abf885614bfbd58"

var fixedOrder = []string{
	NameReadFile, NameListDir, NameSearchFiles,
	NameWebFetch, NameWebSearch,
	NameWriteFile, NameEditFile, NameUnstage,
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
	// The encoding is the eight definitions in the fixed order, HTML escaping off.
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
		{"all with provider", []string{GroupRead, GroupWeb, GroupWrite}, true, fixedOrder},
		{"all without provider", []string{GroupWrite, GroupWeb, GroupRead}, false, []string{
			NameReadFile, NameListDir, NameSearchFiles, NameWebFetch, NameWriteFile, NameEditFile, NameUnstage,
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

type transportTool = transport.Tool
