package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/server"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const authorText = "the answer is 42"

// largestFrame is the biggest frame any server instance in this binary wrote.
var largestFrame atomic.Int64

func observeLargest(n int) {
	for {
		cur := largestFrame.Load()
		if int64(n) <= cur || largestFrame.CompareAndSwap(cur, int64(n)) {
			return
		}
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	if n := largestFrame.Load(); n > wire.MaxFrameBytes {
		fmt.Fprintf(os.Stderr, "a server wrote a frame of %d bytes, over the %d limit\n", n, wire.MaxFrameBytes)
		code = 1
	}
	os.Exit(code)
}

// serving is a Server on a real socket in a temp directory.
type serving struct {
	t        *testing.T
	srv      *server.Server
	paths    lockfile.Paths
	done     chan struct{} // closed when Serve has returned
	serveErr error
	cancel   context.CancelFunc
	frames   atomic.Int64 // largest frame since resetFrames
}

func (s *serving) resetFrames() { s.frames.Store(0) }

func (s *serving) maxFrame() int64 { return s.frames.Load() }

// serve starts a server over eng, store and h. The server is closed when the
// test ends.
func serve(t *testing.T, eng server.Engine, store *journal.Store, h *hub.Hub) *serving {
	t.Helper()
	dir, err := os.MkdirTemp("", "srv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	s := &serving{t: t, paths: lockfile.Paths{Dir: dir, Socket: filepath.Join(dir, "engine.sock"), Lock: filepath.Join(dir, "engine.lock")}}
	s.srv = server.New(server.Options{
		Paths:  s.paths,
		Engine: eng,
		Store:  store,
		Hub:    h,
		FrameObserver: func(n int) {
			observeLargest(n)
			for {
				cur := s.frames.Load()
				if int64(n) <= cur || s.frames.CompareAndSwap(cur, int64(n)) {
					return
				}
			}
		},
	})
	if err := s.srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		s.serveErr = s.srv.Serve(ctx)
		close(s.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after cancel")
		}
		_ = s.srv.Close()
	})
	return s
}

func (s *serving) dial() *client.Client {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, s.paths.Socket, "test", "0")
	if err != nil {
		s.t.Fatalf("Dial: %v", err)
	}
	s.t.Cleanup(func() { _ = c.Close() })
	return c
}

// rawConn is a client that speaks frames directly.
type rawConn struct {
	t *testing.T
	c net.Conn
	r *wire.Reader
	w *wire.Writer
}

func (s *serving) raw() *rawConn {
	s.t.Helper()
	c, err := net.Dial("unix", s.paths.Socket)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = c.Close() })
	return &rawConn{t: s.t, c: c, r: wire.NewReader(c), w: wire.NewWriter(c)}
}

func (r *rawConn) send(id, typ string, body any) {
	r.t.Helper()
	f, err := wire.NewRequest(id, typ, body)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.w.Write(f); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rawConn) read() wire.Frame {
	r.t.Helper()
	if err := r.c.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		r.t.Fatal(err)
	}
	f, err := r.r.Read()
	if err != nil {
		r.t.Fatalf("read frame: %v", err)
	}
	return f
}

func (r *rawConn) wantError(code string) wire.Error {
	r.t.Helper()
	f := r.read()
	if f.Type != "error" {
		r.t.Fatalf("frame type %q, want error %q: %s", f.Type, code, f.Body)
	}
	var e wire.Error
	if err := json.Unmarshal(f.Body, &e); err != nil {
		r.t.Fatal(err)
	}
	if e.Code != code {
		r.t.Fatalf("error code %q, want %q", e.Code, code)
	}
	return e
}

func (r *rawConn) wantClosed() {
	r.t.Helper()
	if err := r.c.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		r.t.Fatal(err)
	}
	if f, err := r.r.Read(); err == nil {
		r.t.Fatalf("connection still open, read %s %s", f.Type, f.Body)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		r.t.Fatal("connection not closed")
	}
}

func (r *rawConn) hello() {
	r.t.Helper()
	r.send("h", wire.RequestHello, wire.HelloRequest{Client: "raw", Protocol: wire.ProtocolVersion})
	if f := r.read(); f.Type != "reply" || f.ID != "h" {
		r.t.Fatalf("hello reply = %+v", f)
	}
}

func soloReq(id string) wire.SubmitRequest {
	preset := "solo"
	return wire.SubmitRequest{SubmissionID: id, Input: "question", Preset: &preset}
}

func reviewReq(id string) wire.SubmitRequest {
	return wire.SubmitRequest{SubmissionID: id, Input: "what is the answer?"}
}

func approve() fake.Step {
	return enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictApprove(req.Envelope.CandidateHash, req.Envelope.AttemptID)
	})
}

func newHarness(t *testing.T) *enginetest.Harness {
	t.Helper()
	scripts := map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText), enginetest.Reply(authorText), enginetest.Reply(authorText)},
		"reviewer": {approve(), approve(), approve()},
	}
	return enginetest.New(t, enginetest.RosterTOML, scripts)
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitTerminal(t *testing.T, h *enginetest.Harness, turnID string) *journal.TurnRow {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		row, err := h.Store.GetTurn(turnID)
		if err != nil {
			t.Fatal(err)
		}
		if row.TerminalState != "" {
			return row
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("turn %s never ended", turnID)
	return nil
}

func TestHandshakeProtocolMismatch(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)

	r := s.raw()
	r.send("1", wire.RequestHello, wire.HelloRequest{Client: "raw", Protocol: 2})
	r.wantError("protocol")
	r.wantClosed()
}

func TestHandshakeFirstFrameNotHello(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)

	r := s.raw()
	r.send("1", wire.RequestStatus, struct{}{})
	r.wantError("protocol")
	r.wantClosed()
}

func TestOversizedClientFrame(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)

	for _, afterHello := range []bool{false, true} {
		r := s.raw()
		if afterHello {
			r.hello()
		}
		junk := append(bytes.Repeat([]byte("x"), 2<<20), '\n')
		go func() { _, _ = r.c.Write(junk) }()
		r.wantError("frame")
		r.wantClosed()
	}
}

func TestSocketMode(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)

	info, err := os.Stat(s.paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %o, want 600", perm)
	}
}

func TestBadRequests(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	r := s.raw()
	r.hello()

	cases := []struct {
		name string
		typ  string
		body string
	}{
		{"unknown type", "frobnicate", `{}`},
		{"submit missing input", wire.RequestSubmit, `{"submission_id":"x"}`},
		{"cancel empty body", wire.RequestCancel, `{}`},
		{"cancel not an object", wire.RequestCancel, `[1]`},
		{"cancel wrong type", wire.RequestCancel, `{"turn_id":5}`},
		{"override missing reason", wire.RequestOverride, `{"turn_id":"t"}`},
		{"release missing pool", wire.RequestRelease, `{"reason":"r"}`},
		{"turn empty body", wire.RequestTurn, `{}`},
		{"transcript empty body", wire.RequestTranscript, `{}`},
		{"content missing limit", wire.RequestContent, `{"ref":"msg:1","offset":0}`},
		{"subscribe missing since_seq", wire.RequestSubscribe, `{}`},
		{"status not an object", wire.RequestStatus, `[1]`},
		{"unsubscribe not an object", wire.RequestUnsubscribe, `"x"`},
	}
	for i, tc := range cases {
		id := fmt.Sprintf("b%d", i)
		if err := r.w.Write(wire.Frame{ID: id, Type: tc.typ, Body: json.RawMessage(tc.body)}); err != nil {
			t.Fatal(err)
		}
		f := r.read()
		if f.ID != id || f.Type != "error" {
			t.Fatalf("%s: got %s id %q: %s", tc.name, f.Type, f.ID, f.Body)
		}
		var e wire.Error
		if err := json.Unmarshal(f.Body, &e); err != nil || e.Code != "bad_request" {
			t.Fatalf("%s: error = %s, want bad_request", tc.name, f.Body)
		}
	}

	// Explicitly empty reasons are the engine's to refuse.
	r.send("o", wire.RequestOverride, wire.OverrideRequest{TurnID: "nope", Reason: ""})
	if e := r.wantError("reason_required"); e.Code != "reason_required" {
		t.Fatal("override with empty reason did not reach the engine")
	}
	// An unknown turn is not_found, not an internal error.
	r.send("t", wire.RequestTurn, wire.TurnRequest{TurnID: "nope"})
	r.wantError("not_found")
	r.send("tr", wire.RequestTranscript, wire.TranscriptRequest{TurnID: "nope"})
	r.wantError("not_found")
	r.send("c", wire.RequestContent, wire.ContentRequest{Ref: "msg:nope", Limit: 10})
	r.wantError("not_found")
}

func TestSoloTurnEndToEnd(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	if hello := c.Hello(); hello.RunID == "" || hello.Protocol != 1 {
		t.Fatalf("hello = %+v", hello)
	}
	st, err := c.Status(ctx)
	if err != nil || len(st.Participants) == 0 || st.RunID != c.Hello().RunID {
		t.Fatalf("status = %+v, %v", st, err)
	}

	reply, err := c.Submit(ctx, soloReq("solo-1"))
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, h, reply.TurnID)

	turn, err := c.Turn(ctx, reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.State != engine.StateSoloComplete || turn.Mode != "solo" || turn.Preset != "solo" {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.TerminalReason == nil || *turn.TerminalReason == "" {
		t.Fatalf("terminal reason missing: %+v", turn)
	}
	if len(turn.Attempts) != 1 || turn.Attempts[0].Participant != "author" || turn.Attempts[0].Status == "" || turn.Attempts[0].TransportStatus == "" {
		t.Fatalf("attempts = %+v", turn.Attempts)
	}
	if len(turn.Verdicts) != 0 || len(turn.OpenBlocks) != 0 {
		t.Fatalf("verdicts %+v blocks %+v", turn.Verdicts, turn.OpenBlocks)
	}
	if turn.CandidateHash != nil {
		t.Logf("solo turn carries candidate hash %s", *turn.CandidateHash)
	}
	if turn.Budget.Projected == nil {
		t.Fatalf("budget missing: %+v", turn.Budget)
	}

	tr, err := c.Transcript(ctx, reply.TurnID, "", 0)
	if err != nil || len(tr.Items) == 0 {
		t.Fatalf("transcript = %+v, %v", tr, err)
	}
	var sawAuthor bool
	for _, it := range tr.Items {
		body, err := c.Content(ctx, it.Ref)
		if err != nil {
			t.Fatalf("content %s: %v", it.Ref, err)
		}
		if int64(len(body)) != it.ByteLen {
			t.Fatalf("content %s: %d bytes, item says %d", it.Ref, len(body), it.ByteLen)
		}
		if strings.Contains(string(body), authorText) {
			sawAuthor = true
		}
	}
	if !sawAuthor {
		t.Fatal("no transcript item holds the author's text")
	}

	// Cancel of a finished turn is the engine's answer, whatever it is, and
	// reaches the client as the same result the engine gives directly.
	want := h.Engine.Cancel(reply.TurnID)
	got := c.Cancel(ctx, reply.TurnID)
	var wantWire, gotWire *wire.Error
	if errors.As(want, &wantWire) != errors.As(got, &gotWire) || (wantWire != nil && wantWire.Code != gotWire.Code) {
		t.Fatalf("cancel via socket = %v, direct = %v", got, want)
	}
}

func TestReviewTurnEndToEnd(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	reply, err := c.Submit(ctx, reviewReq("review-1"))
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, h, reply.TurnID)

	turn, err := c.Turn(ctx, reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.State != engine.StateApproved || turn.Mode != "review" {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.TerminalReason == nil || *turn.TerminalReason != "all_approved" {
		t.Fatalf("terminal reason = %v", turn.TerminalReason)
	}
	// Terminal delivery may or may not have begun by now; it comes after
	// the author and review attempts.
	if len(turn.Attempts) < 2 || turn.Attempts[0].Phase != "author" || turn.Attempts[1].Phase != "review" {
		t.Fatalf("attempts = %+v", turn.Attempts)
	}
	if len(turn.Verdicts) != 1 || !turn.Verdicts[0].Parsed || turn.Verdicts[0].ValidationStatus != "valid" {
		t.Fatalf("verdicts = %+v", turn.Verdicts)
	}
	if turn.CandidateHash == nil || *turn.CandidateHash == "" {
		t.Fatalf("candidate hash missing: %+v", turn)
	}
	row, err := h.Store.GetTurn(reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	cand, err := h.Store.GetCandidate(row.CandidateID)
	if err != nil || cand.ContentHash != *turn.CandidateHash {
		t.Fatalf("candidate hash %v, journal says %+v (%v)", *turn.CandidateHash, cand, err)
	}
	if turn.EstimatedPromptTokens <= 0 {
		t.Fatalf("estimated prompt tokens = %d", turn.EstimatedPromptTokens)
	}

	tr, err := c.Transcript(ctx, reply.TurnID, "", 1)
	if err != nil || len(tr.Items) != 1 || tr.NextCursor == nil {
		t.Fatalf("first transcript page = %+v, %v", tr, err)
	}
	rest, err := c.Transcript(ctx, reply.TurnID, *tr.NextCursor, 0)
	if err != nil || len(rest.Items) == 0 {
		t.Fatalf("second transcript page = %+v, %v", rest, err)
	}
}

func TestOpenTurnBusyAndCancel(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	release := h.Hold("author")
	reply, err := c.Submit(ctx, reviewReq("open-1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateAuthorDispatched)

	turn, err := c.Turn(ctx, reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.State != engine.StateAuthorDispatched || turn.TerminalReason != nil {
		t.Fatalf("open turn = %+v", turn)
	}
	if len(turn.Attempts) != 1 || len(turn.Verdicts) != 0 {
		t.Fatalf("open turn attempts %+v verdicts %+v", turn.Attempts, turn.Verdicts)
	}
	if st, err := c.Status(ctx); err != nil || len(st.TurnsOpen) != 1 || st.TurnsOpen[0] != reply.TurnID {
		t.Fatalf("status turns_open = %+v, %v", st.TurnsOpen, err)
	}

	_, err = c.Submit(ctx, reviewReq("open-2"))
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.ErrorCodeBusy || we.TurnID == nil || *we.TurnID != reply.TurnID {
		t.Fatalf("second submit error = %v, want busy with turn_id %s", err, reply.TurnID)
	}

	if err := c.Cancel(ctx, reply.TurnID); err != nil {
		t.Fatal(err)
	}
	release()
	turn, err = c.Turn(ctx, reply.TurnID)
	if err != nil || turn.State != engine.StateCancelled {
		t.Fatalf("after cancel: %+v, %v", turn, err)
	}
}

func TestContentPagingAndFrameBound(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	// 12 MiB of the bytes that cost the most to JSON-escape.
	pattern := []byte("\"\\\x01\x1f\t\n\r\"\\ab")
	body := bytes.Repeat(pattern, (12<<20)/len(pattern)+1)[:12<<20]
	runID := h.Engine.Hello().RunID
	turnID, err := h.Store.CreateTurn(runID, "solo", nil)
	if err != nil {
		t.Fatal(err)
	}
	attemptID, err := h.Store.BeginAttempt(turnID, "author", "author", 0, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.Conn().Exec(`UPDATE attempts SET raw_output = ? WHERE id = ?`, string(body), attemptID); err != nil {
		t.Fatal(err)
	}
	ref := "attempt:" + attemptID

	s.resetFrames()
	got, err := c.Content(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("fetched %d bytes, want %d, and they differ", len(got), len(body))
	}
	if n := s.maxFrame(); n == 0 || n >= 512<<10 {
		t.Fatalf("largest frame during the fetch = %d bytes, want under %d", n, 512<<10)
	}

	// A limit over the page size is clamped.
	r := s.raw()
	r.hello()
	r.send("c", wire.RequestContent, wire.ContentRequest{Ref: ref, Offset: 0, Limit: 1000000})
	f := r.read()
	if f.Type != "reply" {
		t.Fatalf("content reply = %s %s", f.Type, f.Body)
	}
	var page wire.ContentReply
	if err := json.Unmarshal(f.Body, &page); err != nil {
		t.Fatal(err)
	}
	raw, err := wire.DecodePage(page.Data)
	if err != nil || len(raw) != wire.MaxContentPageBytes || page.Total != int64(len(body)) {
		t.Fatalf("page of %d bytes (%v), total %d", len(raw), err, page.Total)
	}
}

// emitFlood commits n events of the given payload size in separate
// transactions.
func emitFlood(t *testing.T, h *hub.Hub, n, size int) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"pad": strings.Repeat("p", size)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		err := h.Tx(func(tx *journal.Tx, emit hub.Emit) error {
			return emit("", "flood", json.RawMessage(payload))
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// emitBatches commits n events of the given payload size, batch to a
// transaction.
func emitBatches(t *testing.T, h *hub.Hub, n, size, batch int) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"pad": strings.Repeat("p", size)})
	if err != nil {
		t.Fatal(err)
	}
	for n > 0 {
		k := min(batch, n)
		err := h.Tx(func(tx *journal.Tx, emit hub.Emit) error {
			for i := 0; i < k; i++ {
				if err := emit("", "flood", json.RawMessage(payload)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		n -= k
	}
}

func TestReplayThenLive(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	reply, err := c.Submit(ctx, soloReq("replay-1"))
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, h, reply.TurnID)
	partway, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	partway -= 2

	// Keep writing while the subscriber replays and goes live.
	const extra = 300
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		emitFlood(t, h.Hub, extra, 64)
	}()
	live, err := c.Subscribe(ctx, partway)
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	last, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	if live <= partway {
		t.Fatalf("live_from_seq %d not after since_seq %d", live, partway)
	}

	next := partway + 1
	for next <= last {
		ev, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatalf("waiting for seq %d: %v", next, err)
		}
		if ev.Seq != next {
			t.Fatalf("got seq %d, want %d", ev.Seq, next)
		}
		next++
	}
}

func TestProbeRowsReplayFromZero(t *testing.T) {
	h := newHarness(t)
	if err := h.Store.AppendEvent("probe_completed", json.RawMessage(`{"endpoint":"local"}`)); err != nil {
		t.Fatal(err)
	}
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	if _, err := c.Subscribe(ctx, 0); err != nil {
		t.Fatal(err)
	}
	last, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for seq := int64(0); seq < last; {
		ev, err := c.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, ev.Kind)
		seq = ev.Seq
	}
	if !contains(kinds, "probe_completed") || !contains(kinds, "engine_started") {
		t.Fatalf("replay from 0 = %v", kinds)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestRestartReplaysCurrentRun(t *testing.T) {
	h := newHarness(t)
	oldFirst := h.Engine.Hello().RunFirstSeq

	eng, err := engine.New(engine.Options{
		Store:         h.Store,
		Hub:           h.Hub,
		RosterPath:    h.RosterPath,
		EngineVersion: enginetest.EngineVersion,
		Clock:         h.Clock,
		NewAdapter: func(p config.Participant, ep config.EndpointProfile) (transport.Adapter, string, error) {
			return fake.New(nil, fake.Options{DefaultReportedModel: enginetest.DefaultReportedModel}), enginetest.AdapterVersion, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })

	s := serve(t, eng, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)
	first := c.Hello().RunFirstSeq
	if first <= oldFirst {
		t.Fatalf("run_first_seq %d not after the first run's %d", first, oldFirst)
	}
	if _, err := c.Subscribe(ctx, first-1); err != nil {
		t.Fatal(err)
	}
	ev, err := c.NextEvent(ctx)
	if err != nil || ev.Kind != "engine_started" || ev.Seq != first {
		t.Fatalf("first event = %+v, %v; want engine_started at %d", ev, err, first)
	}
}

func TestSubscribeReplaceAndUnsubscribe(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	r := s.raw()
	r.hello()

	seq, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	r.send("s1", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: seq})
	r.send("s2", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: seq})
	for _, id := range []string{"s1", "s2"} {
		if f := r.read(); f.Type != "reply" || f.ID != id {
			t.Fatalf("subscribe reply = %+v, want %s", f, id)
		}
	}

	// Requests are still served on a subscribed connection, and every new
	// event arrives exactly once, from the replacement alone.
	r.send("st", wire.RequestStatus, struct{}{})
	emitFlood(t, h.Hub, 3, 8)
	var events []int64
	var gotStatus bool
	for len(events) < 3 || !gotStatus {
		f := r.read()
		switch {
		case f.Type == "event":
			events = append(events, f.Seq)
		case f.Type == "reply" && f.ID == "st":
			gotStatus = true
		default:
			t.Fatalf("unexpected frame %+v", f)
		}
	}
	for i, got := range events {
		if got != seq+1+int64(i) {
			t.Fatalf("events = %v, want %d.. in order", events, seq+1)
		}
	}

	r.send("u", wire.RequestUnsubscribe, struct{}{})
	for {
		f := r.read()
		if f.Type == "reply" && f.ID == "u" {
			break
		}
		if f.Type != "event" {
			t.Fatalf("unexpected frame %+v", f)
		}
	}
	emitFlood(t, h.Hub, 2, 8)
	r.send("st2", wire.RequestStatus, struct{}{})
	if f := r.read(); f.Type != "reply" || f.ID != "st2" {
		t.Fatalf("after unsubscribe got %+v, want only the status reply", f)
	}
}

func TestLaggingSubscriberIsDisconnected(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	ctx := ctxT(t)

	slow := s.raw()
	slow.hello()
	start, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	slow.send("s", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: start})
	if f := slow.read(); f.Type != "reply" {
		t.Fatalf("subscribe reply = %+v", f)
	}

	healthy := s.dial()
	if _, err := healthy.Subscribe(ctx, start); err != nil {
		t.Fatal(err)
	}

	// A turn is in progress while the flood lands.
	c := s.dial()
	release := h.Hold("author")
	turn, err := c.Submit(ctx, soloReq("lag-1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(turn.TurnID, engine.StateAuthorDispatched)

	// slow reads nothing, so its socket and the server's queue for it fill,
	// and the flood is more than the hub queues for it besides.
	const flood = 2000
	emitBatches(t, h.Hub, flood, 1<<10, 100)
	release()
	waitTerminal(t, h, turn.TurnID)
	last, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}

	// The healthy client got everything, in order.
	for next := start + 1; next <= last; next++ {
		ev, err := healthy.NextEvent(ctx)
		if err != nil || ev.Seq != next {
			t.Fatalf("healthy client: seq %d, %v; want %d", ev.Seq, err, next)
		}
	}

	// Reading the slow connection now yields its backlog, then lagging
	// naming the last seq it was given, then the end.
	var lastSeen int64
	for {
		f := slow.read()
		if f.Type == "event" {
			lastSeen = f.Seq
			continue
		}
		if f.Type != "error" || f.ID != "" {
			t.Fatalf("frame = %+v, want lagging error with empty id", f)
		}
		var e wire.Error
		if err := json.Unmarshal(f.Body, &e); err != nil || e.Code != "lagging" || e.LastSeq == nil {
			t.Fatalf("error = %s, want lagging with last_seq", f.Body)
		}
		if *e.LastSeq != lastSeen {
			t.Fatalf("last_seq = %d, last event delivered %d", *e.LastSeq, lastSeen)
		}
		break
	}
	slow.wantClosed()
}

func TestDetachReconnectResubscribe(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	ctx := ctxT(t)

	release := h.Hold("author")
	a := s.dial()
	start, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Subscribe(ctx, start); err != nil {
		t.Fatal(err)
	}
	reply, err := a.Submit(ctx, reviewReq("detach-1"))
	if err != nil {
		t.Fatal(err)
	}
	h.WaitTurnState(reply.TurnID, engine.StateAuthorDispatched)
	var lastSeq int64
	for {
		ev, err := a.NextEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		lastSeq = ev.Seq
		if ev.Kind == "attempt_begun" {
			break
		}
	}
	if err := a.Close(); err != nil {
		t.Logf("close: %v", err)
	}

	// Every client is gone; the turn is still open.
	time.Sleep(100 * time.Millisecond)
	if row, err := h.Store.GetTurn(reply.TurnID); err != nil || row.TerminalState != "" {
		t.Fatalf("turn after the client left: %+v, %v", row, err)
	}
	release()
	row := waitTerminal(t, h, reply.TurnID)
	if row.TerminalState != engine.StateApproved {
		t.Fatalf("turn ended %q, want approved: closing every client must not cancel it", row.TerminalState)
	}

	b := s.dial()
	if _, err := b.Subscribe(ctx, lastSeq); err != nil {
		t.Fatal(err)
	}
	max, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	var sawApproved bool
	for next := lastSeq + 1; next <= max; next++ {
		ev, err := b.NextEvent(ctx)
		if err != nil || ev.Seq != next {
			t.Fatalf("seq %d, %v; want %d", ev.Seq, err, next)
		}
		if ev.Kind == "turn_state" && bytes.Contains(ev.Body, []byte(engine.StateApproved)) {
			sawApproved = true
		}
	}
	if !sawApproved {
		t.Fatal("reconnected client never saw the approved turn_state")
	}
	turn, err := b.Turn(ctx, reply.TurnID)
	if err != nil || turn.State != engine.StateApproved {
		t.Fatalf("turn after reconnect = %+v, %v", turn, err)
	}
}

func TestStatusHasNoSecret(t *testing.T) {
	const sentinel = "SENTINEL-SECRET-VALUE-9f3a"
	t.Setenv("SYNDIC_SERVER_TEST_KEY", sentinel)
	roster := strings.Replace(enginetest.RosterTOML, `dialect = "chat_completions_v1"`,
		"dialect = \"chat_completions_v1\"\nauth = { source = \"env\", name = \"SYNDIC_SERVER_TEST_KEY\" }", 1)
	h := enginetest.New(t, roster, map[string][]fake.Step{})
	s := serve(t, h.Engine, h.Store, h.Hub)

	r := s.raw()
	r.hello()
	r.send("st", wire.RequestStatus, struct{}{})
	f := r.read()
	if f.Type != "reply" {
		t.Fatalf("status = %s %s", f.Type, f.Body)
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(sentinel)) {
		t.Fatal("status frame contains the secret")
	}
}

func TestEngineStoppingNotifiesClients(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)

	plain := s.raw()
	plain.hello()
	subscribed := s.raw()
	subscribed.hello()
	seq, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	subscribed.send("s", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: seq})
	if f := subscribed.read(); f.Type != "reply" {
		t.Fatalf("subscribe reply = %+v", f)
	}

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	for name, r := range map[string]*rawConn{"plain": plain, "subscribed": subscribed} {
		var got *wire.Error
		for got == nil {
			f := r.read()
			if f.Type == "event" {
				continue // engine_stopping is delivered before the notice
			}
			if f.Type != "error" || f.ID != "" {
				t.Fatalf("%s: frame = %+v, want stopping error", name, f)
			}
			var e wire.Error
			if err := json.Unmarshal(f.Body, &e); err != nil {
				t.Fatal(err)
			}
			got = &e
		}
		if got.Code != "stopping" {
			t.Fatalf("%s: error code %q, want stopping", name, got.Code)
		}
		r.wantClosed()
	}

	select {
	case <-s.done:
		if s.serveErr != nil {
			t.Fatalf("Serve returned %v", s.serveErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return when the engine stopped")
	}
}

func TestServeReturnsOnContextCancel(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	r := s.raw()
	r.hello()
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	r.wantClosed()
}

// blockedHarness is a harness whose reviewer blocks.
func blockedHarness(t *testing.T) *enginetest.Harness {
	t.Helper()
	block := enginetest.ReplyFunc(func(req transport.CompleteRequest) string {
		return fake.VerdictBlock(req.Envelope.CandidateHash, req.Envelope.AttemptID, "b1", "high", "evidence", "remedy", "reason")
	})
	return enginetest.New(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(authorText)},
		"reviewer": {block},
	})
}

func TestBlockedReviewTurnRecord(t *testing.T) {
	h := blockedHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	c := s.dial()
	ctx := ctxT(t)

	reply, err := c.Submit(ctx, reviewReq("blocked-1"))
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, h, reply.TurnID)

	turn, err := c.Turn(ctx, reply.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.State != engine.StateIncomplete {
		t.Fatalf("state = %q, want %q", turn.State, engine.StateIncomplete)
	}
	if len(turn.Verdicts) != 1 || turn.Verdicts[0].Parsed != true {
		t.Fatalf("verdicts = %+v", turn.Verdicts)
	}
	wantID := turn.Verdicts[0].ID + ":b1"
	if turn.TerminalReason == nil || *turn.TerminalReason != "block:"+wantID {
		t.Fatalf("terminal reason = %v, want block:%s", turn.TerminalReason, wantID)
	}
	if len(turn.OpenBlocks) != 1 {
		t.Fatalf("open blocks = %+v", turn.OpenBlocks)
	}
	if b := turn.OpenBlocks[0]; b.ID != wantID || b.Severity != "high" || b.State != "open" {
		t.Fatalf("block = %+v, want %s high open", b, wantID)
	}
	rows, err := h.Store.OpenBlocks(reply.TurnID)
	if err != nil || len(rows) != 1 || rows[0].ID != wantID {
		t.Fatalf("journal blocks = %+v, %v", rows, err)
	}
}

// TestNoEventAfterUnsubscribeReply floods events while the subscription is
// ended and replaced, and requires that nothing of the old subscription
// follows the reply that ended it.
func TestNoEventAfterUnsubscribeReply(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	r := s.raw()
	r.hello()
	start, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	r.send("s1", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: start})
	if f := r.read(); f.Type != "reply" || f.ID != "s1" {
		t.Fatalf("subscribe reply = %+v", f)
	}

	stop := make(chan struct{})
	flooded := make(chan struct{})
	go func() {
		defer close(flooded)
		payload := json.RawMessage(`{"pad":"p"}`)
		for {
			select {
			case <-stop:
				return
			default:
			}
			err := h.Hub.Tx(func(tx *journal.Tx, emit hub.Emit) error { return emit("", "flood", payload) })
			if err != nil {
				return
			}
		}
	}()
	defer func() { close(stop); <-flooded }()

	// Take some of the flood, then replace the subscription mid-stream.
	seen := start
	for seen < start+20 {
		f := r.read()
		if f.Type != "event" {
			t.Fatalf("frame = %+v", f)
		}
		seen = f.Seq
	}
	r.send("s2", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: seen})
	for {
		f := r.read()
		if f.Type == "reply" && f.ID == "s2" {
			break
		}
		if f.Type != "event" {
			t.Fatalf("frame = %+v", f)
		}
	}
	// After the replace reply only the replacement delivers: seq follows on
	// from since_seq with no repeat and no gap.
	next := seen + 1
	for i := 0; i < 30; i++ {
		f := r.read()
		if f.Type != "event" || f.Seq != next {
			t.Fatalf("after replace got %s seq %d, want event %d", f.Type, f.Seq, next)
		}
		next++
	}

	r.send("u", wire.RequestUnsubscribe, struct{}{})
	for {
		f := r.read()
		if f.Type == "reply" && f.ID == "u" {
			break
		}
		if f.Type != "event" {
			t.Fatalf("frame = %+v", f)
		}
	}
	// Nothing but the status reply may follow, however much is emitted.
	time.Sleep(50 * time.Millisecond)
	r.send("st", wire.RequestStatus, struct{}{})
	if f := r.read(); f.Type != "reply" || f.ID != "st" {
		t.Fatalf("after unsubscribe reply got %+v, want only the status reply", f)
	}
}

// TestStopDuringAccept stops the engine while connections are being made, to
// catch a connection published before it can be written to.
func TestStopDuringAccept(t *testing.T) {
	for i := 0; i < 20; i++ {
		h := newHarness(t)
		s := serve(t, h.Engine, h.Store, h.Hub)
		var wg sync.WaitGroup
		for j := 0; j < 8; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				conn, err := net.Dial("unix", s.paths.Socket)
				if err != nil {
					return
				}
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(io.Discard, conn)
				_ = conn.Close()
			}()
		}
		if err := h.Engine.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		select {
		case <-s.done:
		case <-time.After(10 * time.Second):
			t.Fatal("Serve did not return")
		}
	}
}

// TestNonReadingSubscriberIsDropped checks that a subscriber that never reads
// is disconnected by the server without its help, and that what it finds when
// it does read is its backlog, then lagging naming the last event it was
// written, then the end.
func TestNonReadingSubscriberIsDropped(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	slow := s.raw()
	slow.hello()
	start, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	slow.send("s", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: start})
	if f := slow.read(); f.Type != "reply" {
		t.Fatalf("subscribe reply = %+v", f)
	}

	const flood = 400
	emitFlood(t, h.Hub, flood, 24<<10)

	// Still without reading: the server has closed its end, so a write fails.
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := wire.NewRequest("st", wire.RequestStatus, struct{}{})
		if err != nil {
			t.Fatal(err)
		}
		if err := slow.w.Write(f); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never closed the connection of a subscriber that does not read")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var n int
	var lastEvent int64
	for {
		f := slow.read()
		if f.Type == "event" {
			n++
			lastEvent = f.Seq
			continue
		}
		var e wire.Error
		if f.Type != "error" || f.ID != "" || json.Unmarshal(f.Body, &e) != nil || e.Code != "lagging" {
			t.Fatalf("frame = %s %q %s, want lagging with an empty id", f.Type, f.ID, f.Body)
		}
		if e.LastSeq == nil || *e.LastSeq != lastEvent {
			t.Fatalf("last_seq = %v, want %d", e.LastSeq, lastEvent)
		}
		break
	}
	slow.wantClosed()
	if n >= flood {
		t.Fatalf("got all %d events, backlog was not cut off", n)
	}
}

// stalledSubscriber subscribes a raw client that then reads nothing while n
// large events are emitted, too few for it to lag.
func stalledSubscriber(t *testing.T, s *serving, h *enginetest.Harness, n int) *rawConn {
	t.Helper()
	r := s.raw()
	r.hello()
	start, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	r.send("s", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: start})
	if f := r.read(); f.Type != "reply" {
		t.Fatalf("subscribe reply = %+v", f)
	}
	emitFlood(t, h.Hub, n, 24<<10)
	return r
}

// waitServe requires Serve to return within limit.
func waitServe(t *testing.T, s *serving, limit time.Duration) {
	t.Helper()
	select {
	case <-s.done:
		if s.serveErr != nil {
			t.Fatalf("Serve returned %v", s.serveErr)
		}
	case <-time.After(limit):
		t.Fatalf("Serve did not return within %v of the engine stopping", limit)
	}
}

// TestStopWithStalledSubscriber stops the engine while a subscriber that is
// not reading has a backlog: Serve returns promptly, and the client finds
// whole events and then the stopping notice as the last frame.
func TestStopWithStalledSubscriber(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	r := stalledSubscriber(t, s, h, 60)
	time.Sleep(200 * time.Millisecond)

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitServe(t, s, 5*time.Second)

	for {
		f := r.read()
		if f.Type == "event" {
			continue
		}
		var e wire.Error
		if f.Type != "error" || f.ID != "" || json.Unmarshal(f.Body, &e) != nil || e.Code != "stopping" {
			t.Fatalf("frame = %s %q %s, want stopping", f.Type, f.ID, f.Body)
		}
		break
	}
	r.wantClosed()
}

// TestStopWithUnreadReplies stops the engine while a client has asked for
// large content pages and is not reading them, so a reply write is stuck.
func TestStopWithUnreadReplies(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)

	body := bytes.Repeat([]byte(`"`), 4<<20)
	turnID, err := h.Store.CreateTurn(h.Engine.Hello().RunID, "solo", nil)
	if err != nil {
		t.Fatal(err)
	}
	attemptID, err := h.Store.BeginAttempt(turnID, "author", "author", 0, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.Conn().Exec(`UPDATE attempts SET raw_output = ? WHERE id = ?`, string(body), attemptID); err != nil {
		t.Fatal(err)
	}
	r := s.raw()
	r.hello()
	for i := 0; i < 8; i++ {
		r.send(fmt.Sprintf("c%d", i), wire.RequestContent, wire.ContentRequest{Ref: "attempt:" + attemptID, Offset: int64(i) * wire.MaxContentPageBytes, Limit: wire.MaxContentPageBytes})
	}
	time.Sleep(300 * time.Millisecond)

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitServe(t, s, 5*time.Second)
}

// stallStatus is an engine whose Status waits to be released.
type stallStatus struct {
	server.Engine
	entered chan struct{}
	release chan struct{}
}

func (e *stallStatus) Status() (wire.StatusReply, error) {
	close(e.entered)
	<-e.release
	return e.Engine.Status()
}

// TestNothingFollowsStopping holds a request in the engine across the stop:
// its reply, finished after the notice, is never written.
func TestNothingFollowsStopping(t *testing.T) {
	h := newHarness(t)
	eng := &stallStatus{Engine: h.Engine, entered: make(chan struct{}), release: make(chan struct{})}
	s := serve(t, eng, h.Store, h.Hub)
	r := s.raw()
	r.hello()
	r.send("st", wire.RequestStatus, struct{}{})
	<-eng.entered

	if err := h.Engine.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e := r.wantError("stopping"); e.Code != "stopping" {
		t.Fatal("no stopping notice")
	}
	close(eng.release)
	r.wantClosed()
	waitServe(t, s, 5*time.Second)
}

// TestLongReplayIsPacedByTheReader replays thousands of events from the start
// of the journal, to a client reading at full speed and to one reading with a
// small delay. Replay waits for the reader: both get every seq once, in
// order, and neither is lagging.
func TestLongReplayIsPacedByTheReader(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	const flood = 5000
	emitBatches(t, h.Hub, flood, 1<<10, 500)
	ctx := ctxT(t)
	last, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}

	fast := s.dial()
	if _, err := fast.Subscribe(ctx, 0); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	for next := int64(1); next <= last; next++ {
		ev, err := fast.NextEvent(ctx)
		if err != nil {
			t.Fatalf("after %d of %d events: %v", next-1, last, err)
		}
		if ev.Seq != next {
			t.Fatalf("got seq %d, want %d", ev.Seq, next)
		}
	}

	slow := s.raw()
	slow.hello()
	slow.send("s", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: 0})
	if f := slow.read(); f.Type != "reply" || f.ID != "s" {
		t.Fatalf("subscribe reply = %s %q %s", f.Type, f.ID, f.Body)
	}
	for next := int64(1); next <= last; next++ {
		if next%10 == 0 {
			time.Sleep(time.Millisecond)
		}
		f := slow.read()
		if f.Type != "event" || f.Seq != next {
			t.Fatalf("got %s seq %d %s, want event %d", f.Type, f.Seq, f.Body, next)
		}
	}
}

// TestLaggingKeepsQueuedReplies has a subscriber that stops reading send a
// request: its reply was queued before the subscriber lagged, so it still
// goes out ahead of the lagging notice.
func TestLaggingKeepsQueuedReplies(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	slow := s.raw()
	slow.hello()
	start, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	slow.send("s", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: start})
	if f := slow.read(); f.Type != "reply" {
		t.Fatalf("subscribe reply = %+v", f)
	}
	// Enough to fill the socket, so the reply queues behind events.
	emitBatches(t, h.Hub, 40, 24<<10, 40)
	slow.send("st", wire.RequestStatus, struct{}{})

	waitPeerClosed(t, slow)
	var lastEvent int64
	var replied bool
	for {
		f := slow.read()
		if f.Type == "event" {
			lastEvent = f.Seq
			continue
		}
		if f.Type == "reply" {
			replied = replied || f.ID == "st"
			continue
		}
		var e wire.Error
		if f.Type != "error" || f.ID != "" || json.Unmarshal(f.Body, &e) != nil || e.Code != "lagging" {
			t.Fatalf("frame = %s %q %s, want lagging with an empty id", f.Type, f.ID, f.Body)
		}
		if e.LastSeq == nil || *e.LastSeq != lastEvent {
			t.Fatalf("last_seq = %v, want %d", e.LastSeq, lastEvent)
		}
		break
	}
	if !replied {
		t.Fatal("the status reply queued before lagging was dropped")
	}
	slow.wantClosed()
}

// waitPeerClosed waits, without reading, for the server to close r: a write
// then fails. It writes single spaces, which never end a frame, so the server
// has nothing to answer.
func waitPeerClosed(t *testing.T, r *rawConn) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := r.c.Write([]byte(" ")); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("server never closed the connection of a subscriber that does not read")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestNonReadingSubscriberLargeEvents has a subscriber stop reading while
// events larger than the socket buffer arrive, so one is only partly written
// when the subscriber lags. The partial event is finished before the notice:
// reading again yields whole events, then lagging with last_seq, then the
// end. Another client is unaffected meanwhile.
func TestNonReadingSubscriberLargeEvents(t *testing.T) {
	h := newHarness(t)
	s := serve(t, h.Engine, h.Store, h.Hub)
	ctx := ctxT(t)
	slow := s.raw()
	slow.hello()
	start, err := h.Store.MaxSeq()
	if err != nil {
		t.Fatal(err)
	}
	slow.send("s", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: start})
	if f := slow.read(); f.Type != "reply" {
		t.Fatalf("subscribe reply = %+v", f)
	}
	const flood = 40
	emitBatches(t, h.Hub, flood, 400<<10, 10)

	// Long enough for the stalled event to make the subscriber lag.
	time.Sleep(3 * time.Second)
	other := s.dial()
	if _, err := other.Status(ctx); err != nil {
		t.Fatalf("another client's status: %v", err)
	}

	var n int
	var lastEvent int64
	for {
		f := slow.read()
		if f.Type == "event" {
			n++
			lastEvent = f.Seq
			continue
		}
		var e wire.Error
		if f.Type != "error" || f.ID != "" || json.Unmarshal(f.Body, &e) != nil || e.Code != "lagging" {
			t.Fatalf("frame = %s %q, want lagging with an empty id", f.Type, f.ID)
		}
		if e.LastSeq == nil || *e.LastSeq != lastEvent {
			t.Fatalf("last_seq = %v, want %d", e.LastSeq, lastEvent)
		}
		break
	}
	slow.wantClosed()
	if n >= flood {
		t.Fatalf("got all %d events, the subscriber never lagged", n)
	}
}
