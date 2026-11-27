package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func refereeRead(t *testing.T, r *RefereeReader, name string, args any) Result {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Read(name, raw)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// The reader serves the record under the staged overlay, for all three tools.
func TestRefereeReaderServesRecordAndOverlay(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "base", "b.txt": "bee"}, 0)
	f.read(map[string]any{"path": "b.txt"})
	f.stage("a.txt", "edited")
	f.stage("n.txt", "created")
	f.list(".")
	r, err := NewRefereeReader(f.j, f.tools.fs, f.turn, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res := refereeRead(t, r, NameReadFile, map[string]any{"path": "a.txt"}); string(res.Content) != "edited" {
		t.Errorf("staged edit read %q", res.Content)
	}
	if res := refereeRead(t, r, NameReadFile, map[string]any{"path": "n.txt"}); string(res.Content) != "created" {
		t.Errorf("staged creation read %q", res.Content)
	}
	if res := refereeRead(t, r, NameListDir, map[string]any{"path": "."}); !strings.Contains(string(res.Content), "n.txt") || !strings.Contains(string(res.Content), "b.txt") {
		t.Errorf("merged listing %q", res.Content)
	}
	if res := refereeRead(t, r, NameSearchFiles, map[string]any{"pattern": "edit"}); !strings.Contains(string(res.Content), "a.txt") {
		t.Errorf("search %q", res.Content)
	}
	if res := refereeRead(t, r, NameReadFile, map[string]any{"path": ".env"}); res.ErrorCode != ErrCodeProtected {
		t.Errorf("protected read %+v", res)
	}
}

// Observations and staging recorded after the reader is built are not seen.
func TestRefereeReaderViewIsFrozen(t *testing.T) {
	f := newReadFixture(t, map[string]string{"a.txt": "base", "late.txt": "late"}, 0)
	f.read(map[string]any{"path": "a.txt"})
	f.stage("a.txt", "before")
	r, err := NewRefereeReader(f.j, f.tools.fs, f.turn, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.read(map[string]any{"path": "late.txt"})
	f.stage("a.txt", "after")
	f.stage("later.txt", "x")
	if res := refereeRead(t, r, NameReadFile, map[string]any{"path": "late.txt"}); res.ErrorCode != ErrCodeNotFound {
		t.Errorf("a path observed after the decision began read %+v", res)
	}
	if res := refereeRead(t, r, NameReadFile, map[string]any{"path": "later.txt"}); res.ErrorCode != ErrCodeNotFound {
		t.Errorf("a path staged after the decision began read %+v", res)
	}
	if res := refereeRead(t, r, NameReadFile, map[string]any{"path": "a.txt"}); string(res.Content) != "before" {
		t.Errorf("overlay read %q, want the content staged when the decision began", res.Content)
	}
}
