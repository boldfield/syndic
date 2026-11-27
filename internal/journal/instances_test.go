package journal_test

import (
	"errors"
	"testing"

	"github.com/boldfield/syndic/internal/journal"
)

func TestMCPInstanceLifecycle(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		if _, err := tx.RecordMCPInstanceStarted(journal.MCPInstanceInput{Server: "docs", Kind: "bogus", Owner: "o"}); err == nil {
			t.Error("unknown kind accepted")
		}
		if _, err := tx.RecordMCPInstanceStarted(journal.MCPInstanceInput{Server: "docs", Kind: journal.MCPInstanceTurn}); err == nil {
			t.Error("turn instance without owner accepted")
		}
		a, err := tx.RecordMCPInstanceStarted(journal.MCPInstanceInput{Server: "docs", Kind: journal.MCPInstanceTurn, Owner: "t1/a", StateDir: "/s", TempDir: "/t"})
		if err != nil {
			return err
		}
		b, err := tx.RecordMCPInstanceStarted(journal.MCPInstanceInput{Server: "web", Kind: journal.MCPInstanceRun})
		if err != nil {
			return err
		}
		if got, _ := tx.UnresolvedMCPInstances(""); len(got) != 2 {
			t.Errorf("unresolved = %d", len(got))
		}
		if got, _ := tx.UnresolvedMCPInstances("docs"); len(got) != 1 || got[0].ID != a.ID || got[0].StateDir != "/s" || got[0].ContainmentUnverified() {
			t.Errorf("docs unresolved = %+v", got)
		}
		if err := tx.RecordMCPContainmentFailure(a.ID, "pid 7 survived"); err != nil {
			return err
		}
		if got, _ := tx.UnresolvedMCPInstances("docs"); len(got) != 1 || !got[0].ContainmentUnverified() {
			t.Errorf("after failure = %+v", got)
		}
		if err := tx.RecordMCPContainmentFailure("nope", "x"); err == nil {
			t.Error("failure for unknown instance accepted")
		}
		if err := tx.RecordMCPInstanceEnded(a.ID); err != nil {
			return err
		}
		if err := tx.RecordMCPInstanceEnded(a.ID); !errors.Is(err, journal.ErrMCPInstanceEnded) {
			t.Errorf("second end err = %v", err)
		}
		if err := tx.RecordMCPContainmentFailure(a.ID, "late"); !errors.Is(err, journal.ErrMCPInstanceEnded) {
			t.Errorf("failure after end err = %v", err)
		}
		if got, _ := tx.UnresolvedMCPInstances(""); len(got) != 1 || got[0].ID != b.ID {
			t.Errorf("after end = %+v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMCPMarksAndClear(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		if err := tx.RecordMCPFileHolding("https://a.example/mcp"); err != nil {
			return err
		}
		if err := tx.RecordMCPFileHolding("https://a.example/mcp"); err != nil {
			return err
		}
		if err := tx.RecordMCPFileHolding("https://b.example/mcp"); err != nil {
			return err
		}
		if err := tx.RecordMCPStateFileHolding("a", "a/p1/ws1"); err != nil {
			return err
		}
		if err := tx.RecordMCPStateFileHolding("b", "b/p1/ws1"); err != nil {
			return err
		}
		m, err := tx.MCPMarksInForce()
		if err != nil {
			return err
		}
		if len(m.Destinations) != 2 || len(m.Directories) != 2 {
			t.Fatalf("marks = %+v", m)
		}
		if err := tx.RecordMCPProvenanceCleared("a", "https://a.example/mcp"); !errors.Is(err, journal.ErrMCPClearNotStarted) {
			t.Errorf("cleared before started err = %v", err)
		}
		if err := tx.RecordMCPProvenanceClearStarted("a", "https://a.example/mcp"); err != nil {
			return err
		}
		if m, _ := tx.MCPMarksInForce(); len(m.Destinations) != 2 || len(m.Directories) != 2 {
			t.Errorf("marks after start = %+v", m)
		}
		// A crash, then a second run, then completion.
		if err := tx.RecordMCPProvenanceClearStarted("a", "https://a.example/mcp"); err != nil {
			return err
		}
		if err := tx.RecordMCPProvenanceCleared("a", "https://a.example/mcp"); err != nil {
			return err
		}
		if err := tx.RecordMCPProvenanceCleared("a", "https://a.example/mcp"); !errors.Is(err, journal.ErrMCPClearNotStarted) {
			t.Errorf("second cleared err = %v", err)
		}
		m, _ = tx.MCPMarksInForce()
		if len(m.Destinations) != 1 || m.Destinations[0] != "https://b.example/mcp" ||
			len(m.Directories) != 1 || m.Directories[0].Server != "b" {
			t.Errorf("marks after clear = %+v", m)
		}
		// A mark made after the clear stands.
		if err := tx.RecordMCPFileHolding("https://a.example/mcp"); err != nil {
			return err
		}
		if m, _ := tx.MCPMarksInForce(); len(m.Destinations) != 2 {
			t.Errorf("marks after re-mark = %+v", m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMCPConnections(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()
	store := journal.NewStore(db)
	err := store.Tx(func(tx *journal.Tx) error {
		inst, err := tx.RecordMCPInstanceStarted(journal.MCPInstanceInput{Server: "docs", Kind: journal.MCPInstanceRun})
		if err != nil {
			return err
		}
		if _, err := tx.RecordMCPConnection(inst.ID, "", "", 443, true, 0); err == nil {
			t.Error("connection without host accepted")
		}
		if _, err := tx.RecordMCPConnection(inst.ID, "", "x.example", 443, false, 0); err == nil {
			t.Error("refused connection without status accepted")
		}
		if _, err := tx.RecordMCPConnection("nope", "", "x.example", 443, true, 0); err == nil {
			t.Error("connection for unknown instance accepted")
		}
		c1, err := tx.RecordMCPConnection(inst.ID, "", "x.example", 443, true, 0)
		if err != nil {
			return err
		}
		c2, err := tx.RecordMCPConnection(inst.ID, "", "y.example", 80, false, 403)
		if err != nil {
			return err
		}
		if _, err := tx.CloseMCPConnection(c2.ID, 1, 1); err == nil {
			t.Error("close of refused connection accepted")
		}
		if _, err := tx.CloseMCPConnection(c1.ID, 10, 20); err != nil {
			return err
		}
		if _, err := tx.CloseMCPConnection(c1.ID, 10, 20); err == nil {
			t.Error("second close accepted")
		}
		got, err := tx.MCPConnections(inst.ID)
		if err != nil {
			return err
		}
		if len(got) != 2 || got[0].Close == nil || got[0].Close.BytesDown != 20 || got[1].Close != nil || got[1].RefusalStatus != 403 {
			t.Errorf("connections = %+v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
