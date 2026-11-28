package execproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/journal"
)

func newInstanceProxy(t *testing.T, j Journal, owner func() error) (*InstanceProxy, *env) {
	t.Helper()
	e := newEnv(t)
	ip, err := NewInstance(InstanceConfig{
		Patterns: []string{"example.com", "*.example.com"}, Journal: j, Resolver: e.res, OwnerLive: owner,
	})
	if err != nil {
		t.Fatal(err)
	}
	ip.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", e.stub.Addr().String())
	}
	t.Cleanup(ip.BeginTermination)
	return ip, e
}

func TestInstanceProxyRequiresOwnerLive(t *testing.T) {
	var live atomic.Bool
	live.Store(true)
	ip, _ := newInstanceProxy(t, &fakeJournal{}, func() error {
		if !live.Load() {
			return errors.New("turn closed")
		}
		return nil
	})
	st, c, _ := connectReq(t, ip.Addr(), "api.example.com:443", auth(ip.Credential()))
	if st != 200 {
		t.Fatalf("status %d", st)
	}
	_ = c.Close()
	live.Store(false)
	if st, _, _ := connectReq(t, ip.Addr(), "api.example.com:443", auth(ip.Credential())); st != 503 {
		t.Fatalf("owner gone: status %d", st)
	}
}

func TestInstanceProxyBeginTerminationClosesEverything(t *testing.T) {
	ip, _ := newInstanceProxy(t, &fakeJournal{}, func() error { return nil })
	addr, cred := ip.Addr(), ip.Credential()
	st, c, br := connectReq(t, addr, "api.example.com:443", auth(cred))
	if st != 200 {
		t.Fatal(st)
	}
	ip.BeginTermination()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("tunnel still open")
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("listener still open")
	}
	if err := ip.cfg.Check(); !errors.Is(err, ErrInstanceNotLive) {
		t.Fatalf("check = %v", err)
	}
	ip.BeginTermination()
}

func TestInstanceProxyNeedsOwnerCheck(t *testing.T) {
	if _, err := NewInstance(InstanceConfig{Patterns: []string{"example.com"}, Journal: &fakeJournal{}}); err == nil {
		t.Fatal("accepted no owner check")
	}
}

func TestMCPStoreJournalRecordsInstanceAndCall(t *testing.T) {
	db, err := journal.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := journal.NewStore(db)
	var inst journal.MCPInstance
	if err := store.Tx(func(tx *journal.Tx) error {
		var err error
		inst, err = tx.RecordMCPInstanceStarted(journal.MCPInstanceInput{Server: "docs", Kind: journal.MCPInstanceRun})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var callID string
	if err := store.Tx(func(tx *journal.Tx) error {
		run, _, err := tx.CreateRun(json.RawMessage(`{}`))
		if err != nil {
			return err
		}
		turn, err := tx.CreateTurn(run, "review", nil)
		if err != nil {
			return err
		}
		attempt, err := tx.BeginAttempt(turn, "author", "author", 1, nil, "")
		if err != nil {
			return err
		}
		c, err := tx.RecordMCPCall(journal.MCPCallInput{
			AttemptID: attempt, Server: "docs", Tool: "search", Args: []byte(`{"q":"x"}`),
			RefereeAllowed: true, RefereeReason: "reads docs", ServerIdentity: "id1", PinSHA256: "pin1", Revision: "2025-06-18",
		})
		callID = c.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var call atomic.Value
	call.Store("")
	j := MCPStoreJournal{Store: store, InstanceID: inst.ID, Call: func() string { return call.Load().(string) }}
	ip, _ := newInstanceProxy(t, j, func() error { return nil })
	// No call in flight: a background connection is journaled with none.
	st, c, _ := connectReq(t, ip.Addr(), "api.example.com:443", auth(ip.Credential()))
	if st != 200 {
		t.Fatal(st)
	}
	_ = c.Close()
	if st, _, _ := connectReq(t, ip.Addr(), "evil.test:443", auth(ip.Credential())); st != 403 {
		t.Fatalf("unlisted: %d", st)
	}
	// A call in flight: the connection it opens carries the call's id.
	call.Store(callID)
	st, c, _ = connectReq(t, ip.Addr(), "api.example.com:443", auth(ip.Credential()))
	if st != 200 {
		t.Fatal(st)
	}
	_ = c.Close()
	var conns []journal.MCPConnection
	waitFor(t, func() bool {
		_ = store.Tx(func(tx *journal.Tx) error {
			var err error
			conns, err = tx.MCPConnections(inst.ID)
			return err
		})
		return len(conns) == 3 && conns[0].Close != nil && conns[2].Close != nil
	})
	if c0 := conns[0]; c0.Host != "api.example.com" || c0.Port != 443 || !c0.Allowed || c0.CallID != "" {
		t.Fatalf("first = %+v", c0)
	}
	if c1 := conns[1]; c1.Host != "evil.test" || c1.Allowed || c1.RefusalStatus != 403 {
		t.Fatalf("second = %+v", c1)
	}
	if c2 := conns[2]; c2.CallID != callID || !c2.Allowed {
		t.Fatalf("third = %+v, want call %q", c2, callID)
	}
}
