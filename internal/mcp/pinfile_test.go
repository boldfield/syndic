package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDef(t *testing.T, name, desc string) *Definition {
	t.Helper()
	d, err := BuildDefinition([]byte(`{"name":"`+name+`","description":"`+desc+`","inputSchema":{"type":"object"}}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustSet(t *testing.T, s *Snapshot, server string, d *Definition) *Snapshot {
	t.Helper()
	n, err := s.Set(server, d)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPinFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), PinFileName)
	empty, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := mustSet(t, empty, "files", testDef(t, "read", "reads"))
	s = mustSet(t, s, "files", testDef(t, "list", "lists <all>"))
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat %v mode %v", err, fi)
	}
	loaded, hash, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if hash != s.Hash() {
		t.Fatalf("hash %s want %s", hash, s.Hash())
	}
	def, pin, ok := loaded.Lookup("files", "list")
	want := testDef(t, "list", "lists <all>")
	if !ok || string(def) != string(want.Bytes) || pin != want.Pin {
		t.Fatalf("lookup %q %q %v", def, pin, ok)
	}
	if string(loaded.Bytes()) != string(s.Bytes()) {
		t.Fatal("bytes differ after reload")
	}
	if _, _, ok := loaded.Lookup("files", "nope"); ok {
		t.Fatal("unexpected tool")
	}
}

func TestPinFileSnapshotIsFixed(t *testing.T) {
	path := filepath.Join(t.TempDir(), PinFileName)
	old := mustSet(t, &Snapshot{pins: nil}, "srv", testDef(t, "t", "old"))
	if err := old.Save(path); err != nil {
		t.Fatal(err)
	}
	snap, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	newer := mustSet(t, snap, "srv", testDef(t, "t", "new"))
	if err := newer.Save(path); err != nil {
		t.Fatal(err)
	}
	_, pin, _ := snap.Lookup("srv", "t")
	if pin != testDef(t, "t", "old").Pin {
		t.Fatal("snapshot changed after file rewrite")
	}
	if _, p, _ := newer.Lookup("srv", "t"); p == pin {
		t.Fatal("Set did not replace")
	}
}

func TestPinFileRefusals(t *testing.T) {
	d := testDef(t, "t", "d")
	good := string(mustSet(t, &Snapshot{}, "srv", d).Bytes())
	other := testDef(t, "t", "other")
	cases := map[string]string{
		"tampered pin":        strings.Replace(good, d.Pin, other.Pin, 1),
		"tampered definition": strings.Replace(good, `\"d\"`, `\"x\"`, 1),
		"duplicate member":    strings.Replace(good, `"servers": {`, `"servers": {"srv": {}, `, 1),
		"duplicate tool":      strings.Replace(good, `"t": {`, `"t": {}, "t": {`, 1),
		"not offered": `{"servers":{"srv":{"t":{"definition":"{}","sha256":"` +
			sha256Hex([]byte("{}")) + `"}}}}`,
	}
	for name, text := range cases {
		if text == good {
			t.Fatalf("%s: case did not alter the file", name)
		}
		path := filepath.Join(t.TempDir(), PinFileName)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPinFileOrderIndependent(t *testing.T) {
	a, b, c := testDef(t, "a", "1"), testDef(t, "b", "2"), testDef(t, "c", "3")
	s1 := mustSet(t, mustSet(t, mustSet(t, &Snapshot{}, "x", a), "y", b), "x", c)
	s2 := mustSet(t, mustSet(t, mustSet(t, &Snapshot{}, "x", c), "y", b), "x", a)
	if string(s1.Bytes()) != string(s2.Bytes()) {
		t.Fatalf("differ:\n%s\n%s", s1.Bytes(), s2.Bytes())
	}
	dir := t.TempDir()
	if err := s1.Save(filepath.Join(dir, "1")); err != nil {
		t.Fatal(err)
	}
	if err := s2.Save(filepath.Join(dir, "2")); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(filepath.Join(dir, "1"))
	b2, _ := os.ReadFile(filepath.Join(dir, "2"))
	if string(b1) != string(b2) || !strings.HasSuffix(string(b1), "}\n") {
		t.Fatal("saved bytes differ")
	}
}

func TestPinFileMissingIsEmpty(t *testing.T) {
	s, hash, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if hash != sha256Hex(nil) || len(s.Servers()) != 0 {
		t.Fatalf("hash %s servers %v", hash, s.Servers())
	}
	// An empty snapshot's file form loads back as empty.
	path := filepath.Join(t.TempDir(), PinFileName)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if again, _, err := Load(path); err != nil || len(again.Servers()) != 0 {
		t.Fatalf("%v", err)
	}
}

func TestDiff(t *testing.T) {
	d := Diff(testDef(t, "t", "old").Bytes, testDef(t, "t", "new").Bytes)
	if !strings.Contains(d, `- `+"  \"description\": \"old\"") || !strings.Contains(d, `+ `+"  \"description\": \"new\"") {
		t.Fatalf("diff:\n%s", d)
	}
	if strings.Contains(d, "- ") && strings.Contains(Diff(testDef(t, "t", "x").Bytes, testDef(t, "t", "x").Bytes), "- ") {
		t.Fatal("identical definitions differ")
	}
}
