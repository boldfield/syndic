package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/build"
	"strings"
	"testing"
)

func mustDefine(t *testing.T, tool string) *Definition {
	t.Helper()
	d, err := BuildDefinition([]byte(tool), 0)
	if err != nil {
		t.Fatalf("BuildDefinition(%s): %v", tool, err)
	}
	return d
}

func reasonOf(t *testing.T, tool string, maxBytes int) Reason {
	t.Helper()
	d, err := BuildDefinition([]byte(tool), maxBytes)
	if err == nil {
		t.Fatalf("BuildDefinition(%s) offered the tool: %s", tool, d.Bytes)
	}
	var no *NotOfferedError
	if !errors.As(err, &no) {
		t.Fatalf("BuildDefinition(%s): error %v is not a *NotOfferedError", tool, err)
	}
	return no.Reason
}

func TestDefinitionBytesAndPin(t *testing.T) {
	d := mustDefine(t, `{
		"title": "Search",
		"name": "search",
		"inputSchema": {"type": "object", "properties": {"q": {"type": "string"}}},
		"description": "Find things.",
		"annotations": {"readOnlyHint": true},
		"outputSchema": {"type": "object"},
		"icons": [{"src": "https://example.com/i.png"}],
		"_meta": {"k": 1}
	}`)
	want := `{"description":"Find things.","inputSchema":{"properties":{"q":{"type":"string"}},"type":"object"},"name":"search"}`
	if string(d.Bytes) != want {
		t.Fatalf("Bytes = %s, want %s", d.Bytes, want)
	}
	sum := sha256.Sum256([]byte(want))
	if d.Pin != hex.EncodeToString(sum[:]) {
		t.Fatalf("Pin = %s, want the SHA-256 of the bytes", d.Pin)
	}
	if d.Name != "search" {
		t.Fatalf("Name = %q", d.Name)
	}
	if string(d.Annotations) != `{"readOnlyHint":true}` {
		t.Fatalf("Annotations = %s", d.Annotations)
	}
}

func TestPinIgnoresOrderAndWhitespace(t *testing.T) {
	a := mustDefine(t, `{"name":"t","description":"d","inputSchema":{"type":"object","required":["a"]}}`)
	b := mustDefine(t, "{ \"inputSchema\" : {\n\t\"required\": [ \"a\" ], \"type\": \"object\" },\n \"description\": \"d\", \"name\": \"t\" }")
	if a.Pin != b.Pin {
		t.Fatalf("pins differ: %s vs %s", a.Pin, b.Pin)
	}
	c := mustDefine(t, `{"name":"t","description":"e","inputSchema":{"type":"object","required":["a"]}}`)
	if a.Pin == c.Pin {
		t.Fatal("one description byte changed but the pin did not")
	}
}

func TestAbsentDescriptionIsEmpty(t *testing.T) {
	a := mustDefine(t, `{"name":"t","inputSchema":{}}`)
	b := mustDefine(t, `{"name":"t","description":"","inputSchema":{}}`)
	if a.Pin != b.Pin {
		t.Fatalf("absent and empty descriptions give different pins")
	}
	if string(a.Bytes) != `{"description":"","inputSchema":{},"name":"t"}` {
		t.Fatalf("Bytes = %s", a.Bytes)
	}
}

func TestTitleAndAnnotationsLeavePinUnchanged(t *testing.T) {
	a := mustDefine(t, `{"name":"t","title":"One","annotations":{"readOnlyHint":true},"inputSchema":{}}`)
	b := mustDefine(t, `{"name":"t","title":"Two","annotations":{"readOnlyHint":false,"destructiveHint":true},"inputSchema":{}}`)
	if a.Pin != b.Pin {
		t.Fatal("a title or annotation change changed the pin")
	}
	if string(a.Annotations) == string(b.Annotations) {
		t.Fatal("annotations were not returned separately")
	}
	c := mustDefine(t, `{"name":"t","inputSchema":{}}`)
	if c.Annotations != nil {
		t.Fatalf("Annotations = %s, want nil", c.Annotations)
	}
}

func TestNotOffered(t *testing.T) {
	cases := []struct {
		name string
		tool string
		want Reason
	}{
		{"duplicate at top", `{"name":"t","name":"u","inputSchema":{}}`, ReasonDuplicateMember},
		{"duplicate in schema", `{"name":"t","inputSchema":{"properties":{"a":{},"a":{}}}}`, ReasonDuplicateMember},
		{"duplicate in annotations", `{"name":"t","inputSchema":{},"annotations":{"x":1,"x":2}}`, ReasonDuplicateMember},
		{"not JSON", `{"name":`, ReasonMalformed},
		{"not an object", `["t"]`, ReasonMalformed},
		{"name missing", `{"inputSchema":{}}`, ReasonMissingField},
		{"name null", `{"name":null,"inputSchema":{}}`, ReasonMissingField},
		{"name number", `{"name":1,"inputSchema":{}}`, ReasonMalformed},
		{"description null", `{"name":"t","description":null,"inputSchema":{}}`, ReasonMalformed},
		{"description number", `{"name":"t","description":2,"inputSchema":{}}`, ReasonMalformed},
		{"schema missing", `{"name":"t"}`, ReasonMissingField},
		{"schema null", `{"name":"t","inputSchema":null}`, ReasonMissingField},
		{"schema array", `{"name":"t","inputSchema":[]}`, ReasonSchemaNotObject},
		{"schema boolean", `{"name":"t","inputSchema":true}`, ReasonSchemaNotObject},
		{"draft-07", `{"name":"t","inputSchema":{"$schema":"http://json-schema.org/draft-07/schema#"}}`, ReasonDialect},
		{"dialect not a string", `{"name":"t","inputSchema":{"$schema":1}}`, ReasonDialect},
		{"embedded draft-07", `{"name":"t","inputSchema":{"$defs":{"x":{"$id":"x","$schema":"http://json-schema.org/draft-07/schema#"}}}}`, ReasonDialect},
		{"dialect in property", `{"name":"t","inputSchema":{"properties":{"a":{"items":{"$schema":"https://json-schema.org/draft/2019-09/schema"}}}}}`, ReasonDialect},
		{"https ref", `{"name":"t","inputSchema":{"$ref":"https://example.com/s.json"}}`, ReasonExternalRef},
		{"relative ref", `{"name":"t","inputSchema":{"properties":{"a":{"$ref":"other.json#/x"}}}}`, ReasonExternalRef},
		{"ref not a string", `{"name":"t","inputSchema":{"$ref":{"type":"string"}}}`, ReasonExternalRef},
		{"dynamic ref", `{"name":"t","inputSchema":{"$dynamicRef":"https://e.com/x#m"}}`, ReasonExternalRef},
		{"recursive ref", `{"name":"t","inputSchema":{"anyOf":[{"$recursiveRef":"s.json"}]}}`, ReasonExternalRef},
		{"ref in legacy items", `{"name":"t","inputSchema":{"items":[{},{"$ref":"x.json"}]}}`, ReasonExternalRef},
		{"ref in dependencies", `{"name":"t","inputSchema":{"dependencies":{"a":{"$ref":"x.json"}}}}`, ReasonExternalRef},
		{"header at root", `{"name":"t","inputSchema":{"x-mcp-header":"X-Id"}}`, ReasonMCPHeader},
		{"header five deep", `{"name":"t","inputSchema":{"properties":{"a":{"properties":{"b":{"properties":{"c":{"items":{"x-mcp-header":"X"}}}}}}}}}`, ReasonMCPHeader},
		{"header in $defs", `{"name":"t","inputSchema":{"$defs":{"d":{"x-mcp-header":"X"}}}}`, ReasonMCPHeader},
		{"header in data", `{"name":"t","inputSchema":{"x-vendor":{"x-mcp-header":"X"}}}`, ReasonMCPHeader},
		{"header in dependentRequired", `{"name":"t","inputSchema":{"dependentRequired":{"x-mcp-header":["a"]}}}`, ReasonMCPHeader},
		{"header as property name", `{"name":"t","inputSchema":{"properties":{"x-mcp-header":{}}}}`, ReasonMCPHeader},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reasonOf(t, c.tool, 0); got != c.want {
				t.Fatalf("reason = %s, want %s", got, c.want)
			}
		})
	}
}

func TestSizeBound(t *testing.T) {
	tool := `{"name":"t","description":"` + strings.Repeat("a", 100) + `","inputSchema":{}}`
	d := mustDefine(t, tool)
	if _, err := BuildDefinition([]byte(tool), len(d.Bytes)); err != nil {
		t.Fatalf("a definition at the bound was refused: %v", err)
	}
	if got := reasonOf(t, tool, len(d.Bytes)-1); got != ReasonTooLarge {
		t.Fatalf("reason = %s, want %s", got, ReasonTooLarge)
	}
	// The bound is on the definition, not the reported object.
	padded := `{"name":"t","inputSchema":{},"title":"` + strings.Repeat("b", DefaultMaxDefinitionBytes) + `"}`
	mustDefine(t, padded)
	big := `{"name":"t","description":"` + strings.Repeat("c", DefaultMaxDefinitionBytes) + `","inputSchema":{}}`
	if got := reasonOf(t, big, 0); got != ReasonTooLarge {
		t.Fatalf("default bound: reason = %s, want %s", got, ReasonTooLarge)
	}
}

func TestAccepted(t *testing.T) {
	for _, tool := range []string{
		`{"name":"t","inputSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema"}}`,
		`{"name":"t","inputSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema#"}}`,
		`{"name":"t","inputSchema":{"$defs":{"x":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/x"}}}}`,
		`{"name":"t","inputSchema":{"$dynamicRef":"#meta","$ref":"#"}}`,
		// Names that are instance or definition names, not keywords.
		`{"name":"t","inputSchema":{"properties":{"$ref":{"type":"string"},"$schema":{}}}}`,
		`{"name":"t","inputSchema":{"$defs":{"$ref":{}},"patternProperties":{"$ref":{}}}}`,
		`{"name":"t","inputSchema":{"dependentRequired":{"$ref":["other"]}}}`,
		`{"name":"t","inputSchema":{"required":["$ref"],"dependencies":{"a":["$ref"]}}}`,
		// Data in annotation and extension keywords.
		`{"name":"t","inputSchema":{"x-vendor":{"$ref":"annotation data, not a schema reference"}}}`,
		`{"name":"t","inputSchema":{"x-vendor":{"$schema":"http://json-schema.org/draft-07/schema#"}}}`,
		`{"name":"t","inputSchema":{"default":{"$ref":"x.json"},"examples":[{"$ref":"y.json"}],"const":{"$schema":1},"enum":[{"$ref":"z"}]}}`,
		// x-mcp-header as a value, not a member name.
		`{"name":"t","inputSchema":{"description":"x-mcp-header","enum":["x-mcp-header"]}}`,
	} {
		if _, err := BuildDefinition([]byte(tool), 0); err != nil {
			t.Errorf("BuildDefinition(%s): %v", tool, err)
		}
	}
}

func TestReasonIsDeterministic(t *testing.T) {
	tool := `{"name":"t","inputSchema":{"$defs":{"a":{"$ref":"x.json"},"b":{"$schema":"http://json-schema.org/draft-04/schema#"},"c":{"x-mcp-header":"X"}},"properties":{"p":{"$ref":"y.json"},"q":{"$dynamicRef":"z.json"}}}}`
	var first string
	for i := 0; i < 50; i++ {
		_, err := BuildDefinition([]byte(tool), 0)
		if err == nil {
			t.Fatal("tool was offered")
		}
		if i == 0 {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("run %d: %v, first run: %s", i, err, first)
		}
	}
}

// TestNoNetworkImports asserts that the package, with everything it imports
// transitively, contains no network package, so no schema reference can be
// fetched.
func TestNoNetworkImports(t *testing.T) {
	seen := map[string]bool{}
	var visit func(path, dir string)
	visit = func(path, dir string) {
		if path == "C" || path == "unsafe" || seen[path] {
			return
		}
		seen[path] = true
		pkg, err := build.Default.Import(path, dir, 0)
		if err != nil {
			t.Fatalf("import %s: %v", path, err)
		}
		for _, imp := range pkg.Imports {
			visit(imp, pkg.Dir)
		}
	}
	visit("github.com/boldfield/syndic/internal/mcp", ".")
	if !seen["github.com/boldfield/syndic/internal/mcp/jcs"] {
		t.Fatal("dependency walk did not reach internal/mcp/jcs")
	}
	for path := range seen {
		if path == "net" || strings.HasPrefix(path, "net/") || strings.HasPrefix(path, "vendor/golang.org/x/net/") ||
			strings.HasPrefix(path, "golang.org/x/net/") || path == "crypto/tls" || path == "os/exec" {
			t.Errorf("internal/mcp depends on %s", path)
		}
	}
}
