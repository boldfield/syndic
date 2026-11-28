package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func turnReq(server, who, ws string) StateRequest {
	return StateRequest{Server: server, Kind: TurnInstance, State: StatePersistent, Participant: who, WorkspaceHash: ws}
}

func TestStateFreshIsEmptyAndRemoved(t *testing.T) {
	d := &StateDirs{Root: t.TempDir()}
	a, err := d.Acquire(StateRequest{Server: "s", Kind: TurnInstance})
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.Acquire(StateRequest{Server: "s", Kind: TurnInstance, State: StateFresh})
	if err != nil {
		t.Fatal(err)
	}
	if a.Path == b.Path {
		t.Fatal("fresh directories shared")
	}
	if err := os.WriteFile(filepath.Join(a.Path, "x"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatalf("fresh directory kept: %v", err)
	}
	if ents, _ := os.ReadDir(b.Path); len(ents) != 0 {
		t.Fatal("fresh directory not empty")
	}
	_ = b.Release()
}

func TestStatePersistentKeptAndReused(t *testing.T) {
	d := &StateDirs{Root: t.TempDir()}
	a, err := d.Acquire(turnReq("s", "author", "w1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Path, "x"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	b, err := d.Acquire(turnReq("s", "author", "w1"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Release() }()
	if b.Path != a.Path {
		t.Fatal("same key, different directory")
	}
	if _, err := os.Stat(filepath.Join(b.Path, "x")); err != nil {
		t.Fatalf("state not kept: %v", err)
	}
}

func TestStateKeysAreDistinct(t *testing.T) {
	reqs := []StateRequest{
		turnReq("s", "author", "w1"),
		turnReq("s", "reviewer", "w1"),
		turnReq("s", "author", "w2"),
		turnReq("t", "author", "w1"),
		{Server: "s", Kind: RunInstance, State: StatePersistent},
		{Server: "t", Kind: RunInstance, State: StatePersistent},
		turnReq("ab", "c", "w1"),
		turnReq("a", "bc", "w1"),
	}
	seen := map[string]int{}
	d := &StateDirs{Root: t.TempDir()}
	for i, r := range reqs {
		k, err := StateKey(r)
		if err != nil {
			t.Fatal(err)
		}
		if j, ok := seen[k]; ok {
			t.Fatalf("requests %d and %d share a key", j, i)
		}
		seen[k] = i
		s, err := d.Acquire(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Release() }()
	}
	// An apply instance shares its staging author's turn key.
	ta, _ := StateKey(turnReq("s", "author", "w1"))
	aa, _ := StateKey(StateRequest{Server: "s", Kind: ApplyInstance, State: StatePersistent, Participant: "author", WorkspaceHash: "w1"})
	if ta != aa {
		t.Fatal("apply key differs from the author's turn key")
	}
	// The run instance's key ignores participant and workspace.
	a, _ := StateKey(StateRequest{Server: "s", Kind: RunInstance, Participant: "x", WorkspaceHash: "y"})
	b, _ := StateKey(StateRequest{Server: "s", Kind: RunInstance})
	if a != b {
		t.Fatal("run key depends on more than the server")
	}
}

func TestStateLockExclusive(t *testing.T) {
	root := t.TempDir()
	d := &StateDirs{Root: root}
	a, err := d.Acquire(turnReq("s", "p", "w"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Acquire(turnReq("s", "p", "w")); !errors.Is(err, ErrStateLocked) {
		t.Fatalf("same manager: %v", err)
	}
	// A second manager on the same root has its own map: the flock holds.
	if _, err := (&StateDirs{Root: root}).Acquire(turnReq("s", "p", "w")); !errors.Is(err, ErrStateLocked) {
		t.Fatalf("other manager: %v", err)
	}
	// Another key is not blocked.
	o, err := d.Acquire(turnReq("s", "q", "w"))
	if err != nil {
		t.Fatal(err)
	}
	_ = o.Release()
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	c, err := (&StateDirs{Root: root}).Acquire(turnReq("s", "p", "w"))
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	_ = c.Release()
}

func TestStateListingNeverPersistent(t *testing.T) {
	d := &StateDirs{Root: t.TempDir()}
	r := StateRequest{Server: "s", Kind: ListingInstance, State: StatePersistent, Participant: "p", WorkspaceHash: "w"}
	s, err := d.Acquire(r)
	if err != nil {
		t.Fatal(err)
	}
	if s.Key != "" {
		t.Fatal("listing instance got a persistent key")
	}
	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatalf("listing directory not removed: %v", err)
	}
	if _, err := d.Acquire(StateRequest{Server: "s", Kind: TurnInstance, State: StatePersistent}); !errors.Is(err, ErrStateKey) {
		t.Fatalf("missing participant accepted: %v", err)
	}
}

func TestStateOtherKeyLeavesDirectoryAlone(t *testing.T) {
	d := &StateDirs{Root: t.TempDir()}
	a, err := d.Acquire(turnReq("s", "p", "w1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Path, "x"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = a.Release()
	b, err := d.Acquire(turnReq("s", "p", "w2"))
	if err != nil {
		t.Fatal(err)
	}
	if b.Path == a.Path {
		t.Fatal("other key reused the directory")
	}
	if _, err := os.Stat(filepath.Join(b.Path, "x")); err == nil {
		t.Fatal("other key sees the first key's state")
	}
	_ = b.Release()
	got, err := os.ReadFile(filepath.Join(a.Path, "x"))
	if err != nil || string(got) != "keep" {
		t.Fatalf("first key's directory not kept: %q %v", got, err)
	}
}
