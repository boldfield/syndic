package mcp

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

const baseSchema = `{"type":"object","properties":{"a":{"type":"string"}}}`

func tool(desc, extra string) string {
	d := ""
	if desc != "" {
		d = `"description":"` + desc + `",`
	}
	return `{"name":"t",` + d + extra + `"inputSchema":` + baseSchema + `}`
}

func build(t *testing.T, s string) *Definition {
	t.Helper()
	d, err := BuildDefinition([]byte(s), 0)
	if err != nil {
		t.Fatalf("BuildDefinition(%s): %v", s, err)
	}
	return d
}

func reason(t *testing.T, s string, max int) Reason {
	t.Helper()
	_, err := BuildDefinition([]byte(s), max)
	var n *NotOffered
	if !errors.As(err, &n) {
		t.Fatalf("want NotOffered for %s, got %v", s, err)
	}
	return n.Reason
}

func TestPinOrderAndWhitespace(t *testing.T) {
	a := build(t, `{"name":"t","description":"d","inputSchema":{"type":"object","properties":{}}}`)
	b := build(t, ` { "inputSchema" : { "properties":{}, "type":"object" }, "description":"d",
 "name":"t" } `)
	if a.Pin != b.Pin || string(a.Bytes) != string(b.Bytes) {
		t.Fatal("order/whitespace changed the pin")
	}
	want := `{"description":"d","inputSchema":{"properties":{},"type":"object"},"name":"t"}`
	if string(a.Bytes) != want {
		t.Fatalf("bytes = %s", a.Bytes)
	}
	if build(t, tool("d", "")).Pin == build(t, tool("e", "")).Pin {
		t.Fatal("description byte did not change the pin")
	}
}

func TestAbsentDescriptionEqualsEmpty(t *testing.T) {
	a := build(t, tool("", ""))
	b := build(t, tool("", `"description":"",`))
	if a.Pin != b.Pin {
		t.Fatal("absent and empty description differ")
	}
}

func TestAnnotationsNotPinned(t *testing.T) {
	a := build(t, tool("d", `"title":"A","annotations":{"readOnlyHint":true},"_meta":{"x":1},`))
	b := build(t, tool("d", `"title":"B","annotations":{"readOnlyHint":false},`))
	if a.Pin != b.Pin {
		t.Fatal("annotations changed the pin")
	}
	if len(a.Annotations) != 3 || string(a.Annotations["title"]) != `"A"` {
		t.Fatalf("annotations = %v", a.Annotations)
	}
	if strings.Contains(string(a.Bytes), "title") {
		t.Fatal("title in definition")
	}
}

func TestNotOffered(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want Reason
	}{
		{"dup top", `{"name":"t","name":"u","inputSchema":{}}`, 0, ReasonDuplicateMember},
		{"dup nested", `{"name":"t","inputSchema":{"a":1,"a":2}}`, 0, ReasonDuplicateMember},
		{"dup in annotations", `{"name":"t","inputSchema":{},"annotations":{"a":1,"a":2}}`, 0, ReasonDuplicateMember},
		{"no name", `{"inputSchema":{}}`, 0, ReasonMissingField},
		{"no schema", `{"name":"t"}`, 0, ReasonMissingField},
		{"schema array", `{"name":"t","inputSchema":[]}`, 0, ReasonSchemaNotObject},
		{"schema null", `{"name":"t","inputSchema":null}`, 0, ReasonSchemaNotObject},
		{"too big", tool("d", ""), 20, ReasonTooLarge},
		{"dialect", `{"name":"t","inputSchema":{"$schema":"http://json-schema.org/draft-07/schema#"}}`, 0, ReasonDialect},
		{"ref https", `{"name":"t","inputSchema":{"$ref":"https://example.com/s.json"}}`, 0, ReasonExternalRef},
		{"ref file", `{"name":"t","inputSchema":{"properties":{"a":{"$ref":"other.json#/x"}}}}`, 0, ReasonExternalRef},
		{"header deep", `{"name":"t","inputSchema":{"a":{"b":{"c":[{"d":{"x-mcp-header":"X"}}]}}}}`, 0, ReasonXMCPHeader},
		{"embedded dialect", `{"name":"t","inputSchema":{"$defs":{"x":{"$schema":"http://json-schema.org/draft-07/schema#"}}}}`, 0, ReasonDialect},
		{"dynamicRef", `{"name":"t","inputSchema":{"$dynamicRef":"https://e.com/x#m"}}`, 0, ReasonExternalRef},
		{"recursiveRef", `{"name":"t","inputSchema":{"$recursiveRef":"other.json"}}`, 0, ReasonExternalRef},
		{"ref not string", `{"name":"t","inputSchema":{"$ref":{"type":"string"}}}`, 0, ReasonExternalRef},
		{"both, header first", `{"name":"t","inputSchema":{"$ref":"x.json","x-mcp-header":"X"}}`, 0, ReasonExternalRef},
		{"header defs", `{"name":"t","inputSchema":{"$defs":{"x":{"x-mcp-header":"X"}}}}`, 0, ReasonXMCPHeader},
	}
	for _, c := range cases {
		if got := reason(t, c.in, c.max); got != c.want {
			t.Errorf("%s: reason %s, want %s", c.name, got, c.want)
		}
	}
}

func TestAccepted(t *testing.T) {
	build(t, `{"name":"t","inputSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema","$defs":{"x":{"type":"string"}},"properties":{"a":{"$ref":"#/$defs/x"}}}}`)
	build(t, `{"name":"t","inputSchema":{"type":"object"}}`)
	// A property named $ref is a name, not a reference.
	build(t, `{"name":"t","inputSchema":{"properties":{"$ref":{"type":"string"}},"default":{"$ref":"x.json"}}}`)
	// Exactly at the bound is offered.
	d := build(t, tool("d", ""))
	if _, err := BuildDefinition([]byte(tool("d", "")), len(d.Bytes)); err != nil {
		t.Fatal(err)
	}
}

func TestNoNetworkImports(t *testing.T) {
	// go list -deps covers the transitive imports of the non-test package.
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range strings.Fields(string(out)) {
		if p == "net" || strings.HasPrefix(p, "net/") || strings.Contains(p, "grpc") {
			t.Errorf("package depends on %s", p)
		}
	}
}
