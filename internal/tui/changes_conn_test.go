package tui

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/wire"
	tea "github.com/charmbracelet/bubbletea"
)

// evidenceEngine is a fake engine on a unix socket: it answers hello, serves
// the turn's evidence in two pages and answers every other request with an
// empty reply. It records the evidence requests it was sent.
type evidenceEngine struct {
	mu    sync.Mutex
	reqs  []wire.EvidenceRequest
	pages [][]wire.EvidenceItem
}

func (e *evidenceEngine) requests() []wire.EvidenceRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]wire.EvidenceRequest(nil), e.reqs...)
}

func (e *evidenceEngine) serve(t *testing.T, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r, w := wire.NewReader(conn), wire.NewWriter(conn)
	for {
		f, err := r.Read()
		if err != nil {
			return
		}
		var body any = struct{}{}
		switch f.Type {
		case wire.RequestHello:
			body = wire.HelloReply{Protocol: wire.ProtocolVersion, State: "running"}
		case wire.RequestEvidence:
			var req wire.EvidenceRequest
			if err := json.Unmarshal(f.Body, &req); err != nil {
				t.Error(err)
				return
			}
			e.mu.Lock()
			e.reqs = append(e.reqs, req)
			e.mu.Unlock()
			reply := wire.EvidenceReply{Items: e.pages[0]}
			if req.Cursor == nil {
				next := "page-2"
				reply.NextCursor = &next
			} else {
				reply.Items = e.pages[1]
			}
			body = reply
		}
		out, err := wire.NewReply(f.ID, body)
		if err != nil {
			t.Error(err)
			return
		}
		if err := w.Write(out); err != nil {
			return
		}
	}
}

// dialEvidenceEngine starts the fake engine and dials it.
func dialEvidenceEngine(t *testing.T, e *evidenceEngine) *client.Client {
	t.Helper()
	// A short directory: unix socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "e29")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go e.serve(t, conn)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl, err := client.Dial(ctx, filepath.Join(dir, "s"), "test", "0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// awaitEvidence runs cmd, batches included, until it yields an evidenceMsg.
func awaitEvidence(t *testing.T, cmd tea.Cmd) evidenceMsg {
	t.Helper()
	msgs := make(chan tea.Msg, 64)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			m := c()
			if b, ok := m.(tea.BatchMsg); ok {
				for _, sub := range b {
					run(sub)
				}
				return
			}
			msgs <- m
		}()
	}
	run(cmd)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-msgs:
			if em, ok := m.(evidenceMsg); ok {
				return em
			}
		case <-deadline:
			t.Fatal("the turn reply sent no evidence request")
		}
	}
}

// A reattached TUI learns the current turn from a turn reply; that reply reads
// the stored evidence, page by page, and rebuilds the progress from it.
func TestTurnReplyForTheCurrentTurnPagesItsEvidence(t *testing.T) {
	asciiProfile(t)
	var items []wire.EvidenceItem
	for _, f := range toolFrames(t) {
		items = append(items, wire.EvidenceItem{Seq: f.Seq, Kind: f.Kind, Data: f.Body})
	}
	e := &evidenceEngine{pages: [][]wire.EvidenceItem{items[:4], items[4:]}}
	a := toolApp(t, "reviewing")
	a.cl = dialEvidenceEngine(t, e)

	_, cmd := a.Update(turnMsg{gen: a.gen, id: "t1", turn: &wire.TurnReply{State: "reviewing", Mode: "review", Preset: "review"}})
	em := awaitEvidence(t, cmd)
	if em.err != nil || em.turn != "t1" || len(em.items) != len(items) {
		t.Fatalf("evidence = turn %q, %d items, err %v; want t1, %d items", em.turn, len(em.items), em.err, len(items))
	}
	reqs := e.requests()
	if len(reqs) != 2 || reqs[0].TurnID != "t1" || reqs[0].Cursor != nil || reqs[1].Cursor == nil || *reqs[1].Cursor != "page-2" {
		t.Errorf("evidence requests = %+v, want t1 from the start then from page-2", reqs)
	}

	a.Update(em)
	loadArgs(a)
	v := a.View()
	for _, want := range []string{
		"author read_file main.go ok",
		"reviewer 1 web_fetch https://example.test/doc error · referee: deny",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("rebuilt view lacks %q:\n%s", want, v)
		}
	}
}
