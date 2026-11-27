package secretstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestStaticGenerationChangesWithValue(t *testing.T) {
	s, _ := open(t)
	g1, err := s.Static("env:TOKEN", []byte("first-value"))
	if err != nil {
		t.Fatal(err)
	}
	g2, _ := s.Static("env:TOKEN", []byte("first-value"))
	if g1 != g2 {
		t.Fatalf("unchanged value changed generation: %q -> %q", g1, g2)
	}
	g3, _ := s.Static("env:TOKEN", []byte("second-value"))
	if g3 == g1 {
		t.Fatal("replaced value kept its generation")
	}
	other, _ := s.Static("env:OTHER", []byte("first-value"))
	if other == g1 {
		t.Fatal("generations must not derive from the value")
	}
}

func TestStaticPersistsAcrossOpen(t *testing.T) {
	s, dir := open(t)
	g1, _ := s.Static("a", []byte("v"))
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	g2, _ := s2.Static("a", []byte("v"))
	if g1 != g2 {
		t.Fatal("generation not persisted")
	}
}

func TestNoValueOrDigestLeaks(t *testing.T) {
	s, dir := open(t)
	value := []byte("super-secret-value-12345")
	g1, err := s.Static("ref", value)
	if err != nil {
		t.Fatal(err)
	}
	g2, _ := s.Static("ref", []byte("another-secret-value-67890"))
	// What the package returns, as a caller could journal it.
	returned := g1 + "\n" + g2
	// The digest is in the file; find it and check it is not in the returns.
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	d, _, _ := s.read()
	digest := d.Statics["ref"].Digest
	if digest == "" || !bytes.Contains(raw, []byte(digest)) {
		t.Fatal("test setup: digest not found")
	}
	for _, needle := range []string{string(value), "another-secret-value-67890", digest, d.Key} {
		if strings.Contains(returned, needle) {
			t.Fatalf("returned data contains %q", needle)
		}
	}
	// Errors must not carry them either.
	_, _, err = s.Get([]string{"missing"})
	if err == nil || strings.Contains(err.Error(), string(value)) || strings.Contains(err.Error(), digest) {
		t.Fatalf("bad error: %v", err)
	}
	// The raw value is never written to the file.
	if bytes.Contains(raw, value) {
		t.Fatal("value written to the store file")
	}
}

func TestRecords(t *testing.T) {
	s, _ := open(t)
	key := []string{"issuer", "resource", "client"}
	if _, _, err := s.Get(key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	g1, err := s.Put(key, []byte(`{"access":"a"}`), true)
	if err != nil {
		t.Fatal(err)
	}
	g2, _ := s.Put(key, []byte(`{"access":"b"}`), false)
	if g1 != g2 {
		t.Fatal("generation renewed without request")
	}
	rec, g, err := s.Get(key)
	if err != nil || g != g1 || string(rec) != `{"access":"b"}` {
		t.Fatalf("got %s %q %v", rec, g, err)
	}
	g3, _ := s.Put(key, []byte(`{"access":"c"}`), true)
	if g3 == g1 {
		t.Fatal("renew kept the generation")
	}
	// Composite keys do not collide.
	if _, _, err := s.Get([]string{"issuer", "resource" + "client"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("keys collided")
	}
	if err := s.Delete(key); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(key); !errors.Is(err, ErrNotFound) {
		t.Fatal("record survived delete")
	}
}

func TestFileModeAndRefusal(t *testing.T) {
	_, dir := open(t)
	p := filepath.Join(dir, FileName)
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("mode %v err %v", fi.Mode(), err)
	}
	for _, mode := range []os.FileMode{0644, 0700, 0640, 0601} {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil {
			t.Fatalf("mode %04o accepted", mode)
		}
	}
}

func TestRecordRoundTripsExactBytes(t *testing.T) {
	s, _ := open(t)
	for i, rec := range []string{"{ \"u\": \"a&b<>\" }", "not json at all\n", ""} {
		key := []string{"k", string(rune('a' + i))}
		if _, err := s.Put(key, []byte(rec), true); err != nil {
			t.Fatal(err)
		}
		got, _, err := s.Get(key)
		if err != nil || string(got) != rec {
			t.Fatalf("got %q want %q (%v)", got, rec, err)
		}
	}
}

func TestErrorsAndPutDoNotLeak(t *testing.T) {
	s, dir := open(t)
	value := "leaky-secret-value-4242"
	g, err := s.Static("ref", []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	pg, err := s.Put([]string{"a"}, []byte("rec"), true)
	if err != nil {
		t.Fatal(err)
	}
	d, _, _ := s.read()
	needles := []string{value, d.Statics["ref"].Digest, d.Key}
	check := func(what, got string) {
		for _, n := range needles {
			if strings.Contains(got, n) {
				t.Fatalf("%s contains %q", what, n)
			}
		}
	}
	check("static generation", g)
	check("put generation", pg)
	p := filepath.Join(dir, FileName)
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	_, err = Open(dir)
	if err == nil {
		t.Fatal("wide file accepted")
	}
	check("Open error (wide)", err.Error())
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{corrupt "+value), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir); err != nil {
		check("Open error (corrupt)", err.Error())
	}
	if _, err = s.Static("ref", []byte(value)); err == nil {
		t.Fatal("corrupt file accepted")
	}
	check("Static error (corrupt)", err.Error())
}
