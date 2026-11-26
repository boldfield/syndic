package mcp

import (
	"reflect"
	"strings"
	"testing"
)

func TestDistinctNamesAndLookup(t *testing.T) {
	in := []Tool{{"a", "x.y"}, {"a", "x-y"}, {"b", "x_y"}}
	m, err := BuildMapping(in)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range m.Entries {
		if seen[e.Name] {
			t.Fatalf("duplicate name %s", e.Name)
		}
		seen[e.Name] = true
		got, ok := m.Lookup(e.Name)
		if !ok || got != (Tool{e.Server, e.Tool}) {
			t.Fatalf("lookup %s = %v", e.Name, got)
		}
		if !providerPattern.MatchString(e.Name) {
			t.Fatalf("bad name %s", e.Name)
		}
	}
	if m.Entries[0].Name != "mcp_0_x_y" || m.Entries[1].Name != "mcp_1_x_y" {
		t.Fatalf("names: %+v", m.Entries)
	}
	if _, ok := m.Lookup("mcp_9_x_y"); ok {
		t.Fatal("unexpected hit")
	}
}

func TestOrderIndependent(t *testing.T) {
	a, _ := BuildMapping([]Tool{{"b", "t"}, {"a", "z"}, {"a", "B"}})
	b, _ := BuildMapping([]Tool{{"a", "B"}, {"a", "z"}, {"b", "t"}})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%v != %v", a, b)
	}
	want := []Entry{{"mcp_0_B", "a", "B"}, {"mcp_1_z", "a", "z"}, {"mcp_2_t", "b", "t"}}
	if !reflect.DeepEqual(a.Entries, want) {
		t.Fatalf("got %v", a.Entries)
	}
}

func TestSuffix(t *testing.T) {
	long := strings.Repeat("a", 127) + "."
	m, err := BuildMapping([]Tool{{"s", long}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimPrefix(m.Entries[0].Name, "mcp_0_"); len(got) != 40 {
		t.Fatalf("suffix %q", got)
	}
	m, _ = BuildMapping([]Tool{{"s", strings.Repeat("a", 39) + "." + "bb"}})
	if m.Entries[0].Name != "mcp_0_"+strings.Repeat("a", 39)+"_" {
		t.Fatalf("got %s", m.Entries[0].Name)
	}
	m, _ = BuildMapping([]Tool{{"s", "Safe_name9"}})
	if m.Entries[0].Name != "mcp_0_Safe_name9" {
		t.Fatalf("got %s", m.Entries[0].Name)
	}
}

func TestRefusals(t *testing.T) {
	for _, in := range [][]Tool{
		{{"a", "t"}, {"a", "t"}},
		{{"A", "t"}},
		{{"a", "t u"}},
		{{"a", ""}},
		{{"", "t"}},
		{{"a", strings.Repeat("x", 129)}},
	} {
		if _, err := BuildMapping(in); err == nil {
			t.Errorf("accepted %v", in)
		}
	}
	if _, err := DescriptionLine(Tool{"a", "t\n"}); err == nil {
		t.Error("description accepted bad name")
	}
}

func TestDescriptionLine(t *testing.T) {
	got, err := DescriptionLine(Tool{"srv", "do.it"})
	if err != nil || got != "MCP tool do.it of server srv." {
		t.Fatalf("%q %v", got, err)
	}
}

func TestSerializeRoundTrip(t *testing.T) {
	m, _ := BuildMapping([]Tool{{"b", "x.y"}, {"a", "x-y"}, {"a", "k"}})
	data, err := m.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseMapping(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, back) {
		t.Fatalf("%v != %v", m, back)
	}
	again, _ := back.Serialize()
	if string(again) != string(data) {
		t.Fatal("not canonical")
	}
	if _, err := ParseMapping([]byte(`[{"name":"mcp_5_k","server":"a","tool":"k"}]`)); err == nil {
		t.Error("accepted wrong index")
	}
}
