package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

func dialTest(t *testing.T, server *testServer) (*Client, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	c, err := Dial(ctx, server.addr, "test-client", "1.0")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, ctx
}

// Apply, Discard and Evidence send their request type and body, decode the
// reply, and surface a refusal as a *wire.Error with its paths.
func TestApplyDiscardEvidenceRoundTrip(t *testing.T) {
	seen := make(chan wire.Frame, 8)
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		seen <- frame
		var out *wire.Frame
		switch frame.Type {
		case wire.RequestApply:
			var req wire.ApplyRequest
			_ = json.Unmarshal(frame.Body, &req)
			if req.TurnID == "stale" {
				f, _ := wire.NewError(frame.ID, &wire.Error{Code: wire.ErrorCodeBaseChanged, Paths: []string{"n2.txt"}})
				out = &f
			} else {
				f, _ := wire.NewReply(frame.ID, wire.ApplyReply{State: "applied", Paths: []wire.ChangePath{{Path: "n1.txt", Outcome: "applied"}}})
				out = &f
			}
		case wire.RequestDiscard:
			f, _ := wire.NewReply(frame.ID, wire.DiscardReply{State: "discarded", Paths: []wire.ChangePath{{Path: "n1.txt", Outcome: "applied"}}})
			out = &f
		case wire.RequestEvidence:
			next := "12"
			f, _ := wire.NewReply(frame.ID, wire.EvidenceReply{
				Items:      []wire.EvidenceItem{{Seq: 12, Kind: wire.EventKindToolResult, Data: json.RawMessage(`{"sha256":"ab"}`)}},
				NextCursor: &next,
			})
			out = &f
		}
		_ = w.Write(*out)
		return true
	}))
	defer server.Close()
	c, ctx := dialTest(t, server)

	got, err := c.Apply(ctx, "t1")
	if err != nil || got.State != "applied" || len(got.Paths) != 1 || got.Paths[0].Outcome != "applied" {
		t.Fatalf("Apply = %+v, %v", got, err)
	}
	if f := <-seen; f.Type != wire.RequestApply || string(f.Body) != `{"turn_id":"t1"}` {
		t.Fatalf("apply frame = %s %s", f.Type, f.Body)
	}

	_, err = c.Apply(ctx, "stale")
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.ErrorCodeBaseChanged || len(we.Paths) != 1 || we.Paths[0] != "n2.txt" {
		t.Fatalf("Apply refusal = %v", err)
	}
	<-seen

	d, err := c.Discard(ctx, "t1")
	if err != nil || d.State != "discarded" || d.Paths[0].Outcome != "applied" {
		t.Fatalf("Discard = %+v, %v", d, err)
	}
	if f := <-seen; f.Type != wire.RequestDiscard || string(f.Body) != `{"turn_id":"t1"}` {
		t.Fatalf("discard frame = %s %s", f.Type, f.Body)
	}

	ev, err := c.Evidence(ctx, "t1", "7", 50)
	if err != nil || len(ev.Items) != 1 || ev.Items[0].Seq != 12 || ev.NextCursor == nil || *ev.NextCursor != "12" {
		t.Fatalf("Evidence = %+v, %v", ev, err)
	}
	f := <-seen
	var req wire.EvidenceRequest
	if err := json.Unmarshal(f.Body, &req); err != nil || f.Type != wire.RequestEvidence || req.TurnID != "t1" || req.Cursor == nil || *req.Cursor != "7" || req.Limit == nil || *req.Limit != 50 {
		t.Fatalf("evidence frame = %s %s", f.Type, f.Body)
	}

	// No cursor and no limit are left out of the request.
	if _, err := c.Evidence(ctx, "t1", "", 0); err != nil {
		t.Fatal(err)
	}
	if f := <-seen; string(f.Body) != `{"turn_id":"t1"}` {
		t.Fatalf("first-page evidence body = %s", f.Body)
	}
}

// An apply whose reply is lost fails on the dropped connection instead of
// hanging; asking again on a new connection returns the result.
func TestApplyReplyLostThenRetried(t *testing.T) {
	requests := make(chan int, 4)
	var n atomic.Int32
	server := startTestServer(t, helloThen(func(frame wire.Frame, w *wire.Writer) bool {
		c := int(n.Add(1))
		requests <- c
		if c == 1 {
			return false // the request is read, the reply never sent
		}
		f, _ := wire.NewReply(frame.ID, wire.ApplyReply{State: "applied"})
		_ = w.Write(f)
		return true
	}))
	defer server.Close()

	first, ctx := dialTest(t, server)
	if _, err := first.Apply(ctx, "t1"); err == nil {
		t.Fatal("Apply with a lost reply succeeded")
	}
	second, _ := dialTest(t, server)
	got, err := second.Apply(ctx, "t1")
	if err != nil || got.State != "applied" {
		t.Fatalf("retry = %+v, %v", got, err)
	}
	if got := len(requests); got != 2 {
		t.Fatalf("server saw %d apply requests, want 2", got)
	}
}
