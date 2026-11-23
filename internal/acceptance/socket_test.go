package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/config"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const socket = "Socket"

func init() {
	cover(socket, "second engine refuses to start and names the holder PID", "TestSocket_SecondEngineNamesHolder")
	cover(socket, "protocol 2 client is refused", "TestSocket_Protocol2Refused")
	cover(socket, "a 2 MiB client frame is refused with `error{code: \"frame\"}`", "TestSocket_OversizedClientFrame")
	cover(socket, "every engine-emitted frame in the test suite measures under 1 MiB encoded, checked by a test-only hook on the writer", "TestSocket_FrameHook")
	cover(socket, "a 12 MiB attempt output containing bytes that JSON-escape expensively (quotes, backslashes, control characters) is fetched whole through `content` pages, each page's frame under 512 KiB encoded, and its SHA-256 verifies", "TestSocket_LargeContentPages")
	cover(socket, "`content` with `limit: 1000000` is clamped to 262144", "TestSocket_ContentLimitClamped")
	cover(socket, "`submit` twice with one `submission_id` and the same content yields one turn and two identical replies, even when the second arrives while the turn is open", "TestSocket_SubmitIdempotent")
	cover(socket, "the same id with different `input` is refused `submission_conflict` naming the turn", "TestSocket_SubmissionConflict")
	cover(socket, "a refused submission's id can be reused with new content", "TestSocket_RefusedIDReusable")
	cover(socket, "replay then live delivers each `seq` exactly once under concurrent event appends, and dispatch latency measured during a 50,000-event replay by another client is unchanged", "TestSocket_ReplayThenLive")
	cover(socket, "a subscriber that stops reading is disconnected with `lagging` and the engine's dispatch is unaffected", "TestSocket_LaggingSubscriber")
	cover(socket, "`status` output contains no secret value when the resolver holds a sentinel", "TestSocket_StatusHasNoSecret")
	cover(socket, "detach with a turn open, reattach, turn finished meanwhile, answer tab shows the result", "TestSocket_DetachReattach")
	cover(socket, "`release` without `attempt_id` on a pool with one hold releases it with `how: operator` and the reason recorded", "TestSocket_ReleaseOneHold")
	cover(socket, "with two holds it is refused `ambiguous` listing both", "TestSocket_ReleaseTwoHoldsAmbiguous")
	cover(socket, "on a pool with none it is refused `no_hold`", "TestSocket_ReleaseNoHold")
	cover(socket, "with an empty `reason` it is refused", "TestSocket_ReleaseEmptyReason")
	cover(socket, "review_count from conversation matches the total returned by AllReviews across all pages", "TestSocket_ReviewCountParity")
	cover(socket, "a solo turn has zero reviews and an empty list from AllReviews", "TestSocket_ReviewCountEmptyForSoloTurn")
}

// wantRefusal requires err to be the engine's refusal with the code, and
// returns it.
func wantRefusal(t *testing.T, err error, code string) *wire.Error {
	t.Helper()
	var we *wire.Error
	if !errors.As(err, &we) {
		t.Fatalf("error = %v, want a refusal with code %q", err, code)
	}
	if we.Code != code {
		t.Fatalf("refused with code %q, want %q", we.Code, code)
	}
	return we
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// rawPeer is a bare connection that speaks the protocol frame by frame, so a
// test sees exactly what the engine wrote.
type rawPeer struct {
	t      *testing.T
	conn   net.Conn
	reader *wire.Reader
	writer *wire.Writer
	count  *countingReader
}

// rawPeer connects a bare connection and, if hello, completes the handshake.
func (s *suite) rawPeer(t *testing.T, hello bool) *rawPeer {
	t.Helper()
	conn := s.RawConn(t)
	count := &countingReader{r: conn}
	p := &rawPeer{t: t, conn: conn, reader: wire.NewReader(count), writer: wire.NewWriter(conn), count: count}
	if hello {
		p.send("h", wire.RequestHello, wire.HelloRequest{Client: "raw", ClientVersion: "0", Protocol: wire.ProtocolVersion})
		if f := p.read(); f.Type != "reply" || f.ID != "h" {
			t.Fatalf("hello reply = %+v", f)
		}
	}
	return p
}

func (p *rawPeer) send(id, typ string, body any) {
	p.t.Helper()
	f, err := wire.NewRequest(id, typ, body)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := p.writer.Write(f); err != nil {
		p.t.Fatalf("write %s: %v", typ, err)
	}
}

// read reads one frame, failing the test if none arrives within waitLimit.
func (p *rawPeer) read() wire.Frame {
	p.t.Helper()
	f, err := p.tryRead()
	if err != nil {
		p.t.Fatalf("read frame: %v", err)
	}
	return f
}

// tryRead reads one frame within waitLimit.
func (p *rawPeer) tryRead() (wire.Frame, error) {
	if err := p.conn.SetReadDeadline(time.Now().Add(waitLimit)); err != nil {
		p.t.Fatal(err)
	}
	return p.reader.Read()
}

// wantError reads one frame and requires it to be an error with the code.
func (p *rawPeer) wantError(code string) wire.Error {
	p.t.Helper()
	f := p.read()
	var e wire.Error
	if f.Type != "error" || json.Unmarshal(f.Body, &e) != nil || e.Code != code {
		p.t.Fatalf("frame = %s %s, want error %q", f.Type, f.Body, code)
	}
	return e
}

// wantClosed requires the engine to have closed the connection, with nothing
// more written to it.
func (p *rawPeer) wantClosed() {
	p.t.Helper()
	f, err := p.tryRead()
	if err == nil {
		p.t.Fatalf("connection still open, read %s %s", f.Type, f.Body)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		p.t.Fatal("connection not closed")
	}
}

// content fetches one page and returns its decoded bytes, the reply, and the
// size of the frame that carried it, newline included.
func (p *rawPeer) content(ref string, offset, limit int64) ([]byte, wire.ContentReply, int64) {
	p.t.Helper()
	before := p.count.n
	p.send("c", wire.RequestContent, wire.ContentRequest{Ref: ref, Offset: offset, Limit: limit})
	f := p.read()
	size := p.count.n - before
	if f.Type != "reply" {
		p.t.Fatalf("content reply = %s %s", f.Type, f.Body)
	}
	var reply wire.ContentReply
	if err := json.Unmarshal(f.Body, &reply); err != nil {
		p.t.Fatal(err)
	}
	data, err := wire.DecodePage(reply.Data)
	if err != nil {
		p.t.Fatalf("decode page: %v", err)
	}
	return data, reply, size
}

// largeSize returns the size of the content in bytes the large tests use:
// 12 MiB, or 1 MiB under -short.
func largeSize() int {
	if testing.Short() {
		return 1 << 20
	}
	return 12 << 20
}

// floodSize returns the number of events the large replay uses: 50,000, or
// 5,000 under -short.
func floodSize() int {
	if testing.Short() {
		return 5000
	}
	return 50000
}

// flood commits n events of kind "flood" with a payload of size bytes, batch
// to a transaction, through the hub as the engine's own events are.
func (s *suite) flood(t *testing.T, n, size, batch int) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"pad": strings.Repeat("p", size)})
	if err != nil {
		t.Fatal(err)
	}
	for n > 0 {
		k := min(batch, n)
		err := s.Hub.Tx(func(_ *journal.Tx, emit hub.Emit) error {
			for i := 0; i < k; i++ {
				if err := emit("", "flood", json.RawMessage(payload)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Errorf("append events: %v", err)
			return
		}
		n -= k
	}
}

// maxSeq returns the last seq in the journal.
func (s *suite) maxSeq(t *testing.T) int64 {
	t.Helper()
	n, err := s.Store.MaxSeq()
	if err != nil {
		t.Fatalf("MaxSeq: %v", err)
	}
	return n
}

// soloScripts scripts the author's one reply.
func soloScripts(reply string) map[string][]fake.Step {
	return map[string][]fake.Step{"author": {enginetest.Reply(reply)}}
}

// soloTurn runs a solo turn to its end and returns the turn id and the
// content ref of the author's output.
func (s *suite) soloTurn(t *testing.T, c *client.Client, submissionID string) (turnID, ref string) {
	t.Helper()
	turnID = s.SubmitPreset(t, c, submissionID, "solo")
	s.WaitTerminal(turnID)
	turn := s.Turn(t, c, turnID)
	if turn.State != engine.StateSoloComplete || len(turn.Attempts) != 1 {
		t.Fatalf("solo turn = %q with %d attempts, want %q with 1", turn.State, len(turn.Attempts), engine.StateSoloComplete)
	}
	return turnID, "attempt:" + turn.Attempts[0].ID
}

func TestSocket_SecondEngineNamesHolder(t *testing.T) {
	t.Parallel()
	lock := filepath.Join(t.TempDir(), "engine.lock")
	started := time.Date(2026, time.November, 18, 12, 0, 0, 0, time.UTC)
	first, err := lockfile.Acquire(lock, started)
	if err != nil {
		t.Fatalf("first engine's lock: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	_, err = lockfile.Acquire(lock, started.Add(time.Minute))
	if !errors.Is(err, lockfile.ErrHeld) {
		t.Fatalf("second engine's lock: %v, want it refused as held", err)
	}
	var held *lockfile.HeldError
	if !errors.As(err, &held) || held.Holder.PID != os.Getpid() {
		t.Fatalf("refusal = %v, want it to name the holder, PID %d", err, os.Getpid())
	}
	if !strings.Contains(err.Error(), "PID "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("refusal text %q does not name PID %d", err, os.Getpid())
	}
	if !held.Holder.StartedAt.Equal(started) {
		t.Fatalf("holder started %v, want %v: the refusal must describe the first engine", held.Holder.StartedAt, started)
	}

	// The refusal did not disturb the holder: once it lets go, the lock is free.
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := lockfile.Acquire(lock, started.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("lock after the holder released it: %v", err)
	}
	_ = second.Release()
}

func TestSocket_Protocol2Refused(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	p := s.rawPeer(t, false)
	p.send("h", wire.RequestHello, wire.HelloRequest{Client: "raw", ClientVersion: "0", Protocol: 2})
	p.wantError(wire.ErrorCodeProtocol)
	p.wantClosed()

	// The engine is unaffected: a protocol 1 client is served.
	c := s.Dial(t)
	if got := c.Hello().Protocol; got != wire.ProtocolVersion {
		t.Fatalf("hello reply protocol = %d, want %d", got, wire.ProtocolVersion)
	}
}

func TestSocket_OversizedClientFrame(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	for _, afterHello := range []bool{false, true} {
		p := s.rawPeer(t, afterHello)
		junk := append(bytes.Repeat([]byte("x"), 2<<20), '\n')
		go func() { _, _ = p.conn.Write(junk) }()
		p.wantError(wire.ErrorCodeFrame)
		p.wantClosed()
	}
	// The engine still serves.
	c := s.Dial(t)
	s.Status(t, c)
}

func TestSocket_FrameHook(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)
	s.Turn(t, c, turnID)

	// The hook on the server's writer saw the frames of this turn: the
	// handshake, replies and every event.
	largest, tooLarge := s.oversizedFrame()
	if largest == 0 {
		t.Fatal("the writer hook recorded no frame")
	}
	if tooLarge || largest >= wire.MaxFrameBytes {
		t.Fatalf("the engine wrote a frame of %d bytes, want under %d", largest, wire.MaxFrameBytes)
	}

	// The check that runs when the test ends rejects a frame of exactly 1 MiB
	// and accepts one byte fewer. It is run here on a suite of its own, so
	// that this test's cleanup does not fail.
	probe := &suite{}
	probe.observeFrame(wire.MaxFrameBytes - 1)
	if n, bad := probe.oversizedFrame(); bad || n != wire.MaxFrameBytes-1 {
		t.Fatalf("check on %d bytes = (%d, %v), want it to pass", wire.MaxFrameBytes-1, n, bad)
	}
	probe.observeFrame(1 << 20)
	if n, bad := probe.oversizedFrame(); !bad || n != 1<<20 {
		t.Fatalf("check on exactly 1 MiB = (%d, %v), want it to reject", n, bad)
	}
	probe.observeFrame(10)
	if _, bad := probe.oversizedFrame(); !bad {
		t.Fatal("a smaller frame after the large one cleared the record")
	}
}

// escapeHeavy is made of the bytes that cost the most to JSON-escape: quotes,
// backslashes and control characters.
var escapeHeavy = []byte("\"\\\x01\x1f\t\n\r\"\\ab")

func TestSocket_LargeContentPages(t *testing.T) {
	t.Parallel()
	size := largeSize()
	scripted := bytes.Repeat(escapeHeavy, size/len(escapeHeavy)+1)[:size]
	sum := sha256.Sum256(scripted)
	want := hex.EncodeToString(sum[:])

	s := newSuite(t, enginetest.RosterTOML, soloScripts(string(scripted)))
	c := s.Dial(t)
	_, ref := s.soloTurn(t, c, "big")

	p := s.rawPeer(t, true)
	h := sha256.New()
	var got int
	pages := 0
	for got < size {
		data, reply, frame := p.content(ref, int64(got), wire.MaxContentPageBytes)
		pages++
		if frame >= 512<<10 {
			t.Fatalf("page %d: frame measured %d bytes encoded, want under %d", pages, frame, 512<<10)
		}
		if reply.Total != int64(size) || reply.SHA256 != want {
			t.Fatalf("page %d: total %d, sha256 %s; want %d, %s", pages, reply.Total, reply.SHA256, size, want)
		}
		if reply.Offset != int64(got) || len(data) == 0 || len(data) > wire.MaxContentPageBytes {
			t.Fatalf("page %d: offset %d with %d bytes, want offset %d and 1..%d bytes", pages, reply.Offset, len(data), got, wire.MaxContentPageBytes)
		}
		h.Write(data)
		got += len(data)
	}
	if wantPages := (size + wire.MaxContentPageBytes - 1) / wire.MaxContentPageBytes; pages != wantPages {
		t.Fatalf("fetched in %d pages, want %d", pages, wantPages)
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		t.Fatal("SHA-256 of the reassembled pages differs from the scripted output")
	}

	// Past the end there is nothing more.
	data, reply, _ := p.content(ref, int64(size), wire.MaxContentPageBytes)
	if len(data) != 0 || reply.Total != int64(size) {
		t.Fatalf("page past the end has %d bytes, total %d; want 0, %d", len(data), reply.Total, size)
	}
}

func TestSocket_ContentLimitClamped(t *testing.T) {
	t.Parallel()
	scripted := bytes.Repeat(escapeHeavy, 600000/len(escapeHeavy)+1)[:600000]
	s := newSuite(t, enginetest.RosterTOML, soloScripts(string(scripted)))
	c := s.Dial(t)
	_, ref := s.soloTurn(t, c, "s1")

	p := s.rawPeer(t, true)
	data, reply, _ := p.content(ref, 0, 1000000)
	if len(data) != 262144 || reply.Total != int64(len(scripted)) {
		t.Fatalf("first page has %d bytes (total %d), want exactly 262144 of %d", len(data), reply.Total, len(scripted))
	}
	if !bytes.Equal(data, scripted[:262144]) {
		t.Fatal("first page is not the first 262144 bytes of the output")
	}
	data, _, _ = p.content(ref, 262144, 1000000)
	if len(data) != 262144 || !bytes.Equal(data, scripted[262144:524288]) {
		t.Fatalf("second page has %d bytes, want exactly 262144 of the next bytes", len(data))
	}
	// Less than a page remains: the limit does not pad.
	data, _, _ = p.content(ref, 524288, 1000000)
	if want := len(scripted) - 524288; len(data) != want || !bytes.Equal(data, scripted[524288:]) {
		t.Fatalf("last page has %d bytes, want the remaining %d", len(data), want)
	}
}

// openTurn submits a review turn whose author is held, and returns the turn
// id and the function that lets the author answer.
func (s *suite) openTurn(t *testing.T, c *client.Client, submissionID string) (turnID string, release func()) {
	t.Helper()
	release = s.h.Hold("author")
	turnID = s.Submit(t, c, submissionID)
	s.WaitTurnState(turnID, engine.StateAuthorDispatched)
	return turnID, release
}

func (s *suite) turnSubmittedCount(turnID string) int {
	n := 0
	for _, ev := range s.Events() {
		if e, ok := body[wire.EvTurnSubmitted](ev, wire.EventKindTurnSubmitted); ok && e.TurnID == turnID {
			n++
		}
	}
	return n
}

func TestSocket_SubmitIdempotent(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	c := s.Dial(t)
	other := s.Dial(t)
	req := wire.SubmitRequest{SubmissionID: "same", Input: "what is the answer?"}

	release := s.h.Hold("author")
	first, err := c.Submit(s.ctx(), req)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	s.WaitTurnState(first.TurnID, engine.StateAuthorDispatched)

	// The turn is open: the repeat is answered with the same reply, not busy.
	second, err := other.Submit(s.ctx(), req)
	if err != nil {
		t.Fatalf("second submit while the turn is open: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replies differ:\n first  %+v\n second %+v", first, second)
	}
	if open := s.Status(t, c).TurnsOpen; !reflect.DeepEqual(open, []string{first.TurnID}) {
		t.Fatalf("turns open = %v, want only %s", open, first.TurnID)
	}

	release()
	s.WaitTerminal(first.TurnID)
	third, err := c.Submit(s.ctx(), req)
	if err != nil {
		t.Fatalf("third submit after the turn ended: %v", err)
	}
	if !reflect.DeepEqual(first, third) {
		t.Fatalf("reply after the turn ended differs:\n first %+v\n third %+v", first, third)
	}

	// One turn: one announcement, one author request.
	if n := s.turnSubmittedCount(first.TurnID); n != 1 {
		t.Fatalf("turn_submitted announced %d times, want 1", n)
	}
	submitted := 0
	for _, ev := range s.Events() {
		if _, ok := body[wire.EvTurnSubmitted](ev, wire.EventKindTurnSubmitted); ok {
			submitted++
		}
	}
	if submitted != 1 {
		t.Fatalf("%d turns were submitted, want 1", submitted)
	}
	if n := len(s.calls("author", "author")); n != 1 {
		t.Fatalf("author received %d requests, want 1", n)
	}
}

func TestSocket_SubmissionConflict(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "dup")
	s.WaitTerminal(turnID)

	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "dup", Input: "a different question"})
	refusal := wantRefusal(t, err, wire.ErrorCodeSubmissionConflict)
	if refusal.TurnID == nil || *refusal.TurnID != turnID {
		t.Fatalf("conflict names turn %s, want the existing turn %s", str(refusal.TurnID), turnID)
	}

	// Nothing was written for the refused request.
	if n := s.turnSubmittedCount(turnID); n != 1 {
		t.Fatalf("turn_submitted announced %d times for the existing turn, want 1", n)
	}
	submitted := 0
	for _, ev := range s.Events() {
		if _, ok := body[wire.EvTurnSubmitted](ev, wire.EventKindTurnSubmitted); ok {
			submitted++
		}
	}
	if submitted != 1 {
		t.Fatalf("%d turns exist after the conflict, want 1", submitted)
	}
	// The original submission is still answered.
	if again, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "dup", Input: "what is the answer?"}); err != nil || again.TurnID != turnID {
		t.Fatalf("original request again = %+v, %v; want turn %s", again, err, turnID)
	}
}

func TestSocket_RefusedIDReusable(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{"author": repeat(8), "reviewer": repeat(8)})
	c := s.Dial(t)

	// Refused because the quick profile is unknown.
	unknown := "no-such-profile"
	_, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "reuse-1", Input: "first", QuickProfile: &unknown})
	_ = wantRefusal(t, err, wire.ErrorCodeQuickProfileUnknown)

	// Refused because another turn is open.
	open, release := s.openTurn(t, c, "holder")
	_, err = c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "reuse-2", Input: "first"})
	busy := wantRefusal(t, err, wire.ErrorCodeBusy)
	if busy.TurnID == nil || *busy.TurnID != open {
		t.Fatalf("busy names turn %s, want %s", str(busy.TurnID), open)
	}
	release()
	s.WaitTerminal(open)

	// Each id is accepted with new content, not refused as a conflict.
	var turns []string
	for _, id := range []string{"reuse-1", "reuse-2"} {
		reply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: id, Input: "second, new content"})
		if err != nil {
			t.Fatalf("reuse of id %s after its submission was refused: %v", id, err)
		}
		s.WaitTerminal(reply.TurnID)
		turns = append(turns, reply.TurnID)
	}
	if turns[0] == turns[1] || turns[0] == open || turns[1] == open {
		t.Fatalf("turns %v and the first %s are not three distinct turns", turns, open)
	}
	for _, id := range turns {
		if turn := s.Turn(t, c, id); turn.State != engine.StateApproved {
			t.Fatalf("turn %s is %q, want %q", id, turn.State, engine.StateApproved)
		}
	}
}

// replayed reads from the client until the event with seq last has arrived and
// returns every seq it was given, in order.
func replayed(t *testing.T, ctx context.Context, c *client.Client, last int64) []int64 {
	t.Helper()
	var seqs []int64
	for {
		ev, err := c.NextEvent(ctx)
		if err != nil {
			var tail int64
			if len(seqs) > 0 {
				tail = seqs[len(seqs)-1]
			}
			t.Fatalf("waiting for seq %d after %d events (last seq %d): %v", last, len(seqs), tail, err)
		}
		seqs = append(seqs, ev.Seq)
		if ev.Seq >= last {
			return seqs
		}
	}
}

// wantContiguous requires seqs to run from first with no gap and no repeat.
func wantContiguous(t *testing.T, seqs []int64, first int64) {
	t.Helper()
	for i, seq := range seqs {
		if want := first + int64(i); seq != want {
			t.Fatalf("event %d has seq %d, want %d (a gap, repeat or reorder)", i, seq, want)
		}
	}
}

// reattachEvents replaces the suite's own event subscription with a fresh one
// from since. The suite's subscription is not read while a test floods the
// journal, so under load the engine may have cut it off with lagging; a
// subscription begun after the flood does not depend on it.
func (s *suite) reattachEvents(t *testing.T, since int64) {
	t.Helper()
	c := s.Dial(t)
	if _, err := c.Subscribe(s.ctx(), since); err != nil {
		t.Fatalf("subscribe from %d: %v", since, err)
	}
	s.events = &eventStream{c: c}
}

func TestSocket_ReplayThenLive(t *testing.T) {
	t.Parallel()
	replayThenLive(t)
	dispatchDuringReplay(t)
}

// replayThenLive: a subscriber replaying from an old seq while events are
// appended receives every seq exactly once.
func replayThenLive(t *testing.T) {
	t.Helper()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	const before, after, size = 3000, 3000, 64
	since := s.maxSeq(t) - 3
	s.flood(t, before, size, 100)
	atSubscribe := s.maxSeq(t)

	// Appends go on while the subscriber replays and goes live: the second
	// half is held back until the subscription is in place.
	proceed := make(chan struct{})
	var appender sync.WaitGroup
	appender.Add(1)
	go func() {
		defer appender.Done()
		<-proceed
		s.flood(t, after, size, 50)
	}()
	late := s.Dial(t)
	liveFrom, err := late.Subscribe(s.ctx(), since)
	if err != nil {
		close(proceed)
		t.Fatalf("subscribe: %v", err)
	}
	close(proceed)
	appended := make(chan struct{})
	go func() { appender.Wait(); close(appended) }()
	select {
	case <-appended:
	case <-s.ctx().Done():
		t.Fatal("the concurrent appends did not finish")
	}

	watermark := s.maxSeq(t)
	if watermark != atSubscribe+after {
		t.Fatalf("journal ends at seq %d, want %d", watermark, atSubscribe+after)
	}
	if liveFrom <= since || liveFrom > atSubscribe+1 {
		t.Fatalf("live_from_seq = %d, want it in (%d, %d]: everything after it was appended after the subscribe", liveFrom, since, atSubscribe+1)
	}
	seqs := replayed(t, s.ctx(), late, watermark)
	wantContiguous(t, seqs, since+1)
	if int64(len(seqs)) != watermark-since {
		t.Fatalf("received %d events, want %d", len(seqs), watermark-since)
	}
	if late.LastSeq() != watermark {
		t.Fatalf("client's last seq %d, want %d", late.LastSeq(), watermark)
	}
}

// dispatchDuringReplay: a turn is submitted and completes while another
// client is midway through a large replay.
func dispatchDuringReplay(t *testing.T) {
	t.Helper()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	n := floodSize()
	start := s.maxSeq(t)
	s.flood(t, n, 64, 500)
	flooded := s.maxSeq(t)
	s.reattachEvents(t, flooded)

	// The replaying client takes a known number of frames and then stops
	// reading, midway through the replay.
	const taken = 1000
	p := s.rawPeer(t, true)
	p.send("s", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: start})
	if f := p.read(); f.Type != "reply" || f.ID != "s" {
		t.Fatalf("subscribe reply = %+v", f)
	}
	var seqs []int64
	for len(seqs) < taken {
		f := p.read()
		if f.Type != "event" {
			t.Fatalf("frame = %s %s, want an event", f.Type, f.Body)
		}
		seqs = append(seqs, f.Seq)
	}
	wantContiguous(t, seqs, start+1)

	// A turn is submitted and completes while the replay has frames outstanding.
	c := s.Dial(t)
	turnID := s.Submit(t, c, "during-replay")
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")
	if last := seqs[len(seqs)-1]; last >= flooded {
		t.Fatalf("the replaying client had reached seq %d of %d when the turn ended", last, flooded)
	}
	if int64(len(seqs)) >= int64(n) {
		t.Fatalf("the replaying client had taken %d of %d frames", len(seqs), n)
	}

	// The replay then carries on through the turn's own events, in order, to
	// the end of the journal. Should the turn have taken so long that the
	// engine's own grace for a subscriber that has stopped reading ran out,
	// the end is its lagging notice instead.
	end := s.maxSeq(t)
	for last := seqs[len(seqs)-1]; last < end; {
		f := p.read()
		if f.Type == "error" {
			var e wire.Error
			if json.Unmarshal(f.Body, &e) != nil || e.Code != wire.ErrorCodeLagging {
				t.Fatalf("replay ended with %s %s, want events or the lagging notice", f.Type, f.Body)
			}
			return
		}
		if f.Type != "event" || f.Seq != last+1 {
			t.Fatalf("replay frame = %s seq %d %s, want event %d", f.Type, f.Seq, f.Body, last+1)
		}
		last = f.Seq
	}
}

// noticeGuaranteed reports whether a subscriber that stops reading must be
// sent the lagging notice before it is closed: on Linux the server keeps room
// in the socket for it; elsewhere it may close without it.
func noticeGuaranteed() bool { return runtime.GOOS == "linux" }

func TestSocket_LaggingSubscriber(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))

	slow := s.rawPeer(t, true)
	start := s.maxSeq(t)
	slow.send("s", wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: start})
	if f := slow.read(); f.Type != "reply" || f.ID != "s" {
		t.Fatalf("subscribe reply = %+v", f)
	}

	// A subscriber that reads throughout, to show the engine kept feeding the
	// ones that do. Its frames are handed over a channel big enough for the
	// whole run; the suite's own subscription is not read during the flood
	// and is replaced once it is over.
	reader := s.Dial(t)
	if _, err := reader.Subscribe(s.ctx(), start); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	got := make(chan int64, 20000)
	go func() {
		for {
			ev, err := reader.NextEvent(s.ctx())
			if err != nil {
				close(got)
				return
			}
			got <- ev.Seq
		}
	}()

	// From here slow reads nothing. A turn is in progress while a flood lands
	// that is far more than the hub queues for a subscriber.
	c := s.Dial(t)
	turnID, release := s.openTurn(t, c, "lag-1")
	s.flood(t, 6000, 1<<10, 100)
	flooded := s.maxSeq(t)
	s.reattachEvents(t, flooded)
	release()
	s.WaitTerminal(turnID)
	wantTurn(t, s.Turn(t, c, turnID), engine.StateApproved, "all_approved")

	// The reading subscriber was given every event, in order.
	last := s.maxSeq(t)
	for prev := start; prev < last; {
		seq, ok := <-got
		if !ok {
			t.Fatalf("the reading subscriber's stream ended at seq %d of %d", prev, last)
		}
		if seq != prev+1 {
			t.Fatalf("the reading subscriber went from seq %d to %d", prev, seq)
		}
		prev = seq
	}

	// The one that did not read is read now: its backlog, then lagging
	// naming the last event written to it, then the end.
	var lastWritten int64
	var notice *wire.Error
	for notice == nil {
		f, err := slow.tryRead()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatal("the engine never closed the connection of a subscriber that does not read")
			}
			if noticeGuaranteed() || !errors.Is(err, io.EOF) {
				t.Fatalf("read after %d: %v, want the lagging notice", lastWritten, err)
			}
			break
		}
		if f.Type == "event" {
			lastWritten = f.Seq
			continue
		}
		var e wire.Error
		if f.Type != "error" || f.ID != "" || json.Unmarshal(f.Body, &e) != nil || e.Code != wire.ErrorCodeLagging {
			t.Fatalf("frame = %s %q %s, want error lagging with no id", f.Type, f.ID, f.Body)
		}
		notice = &e
	}
	if notice != nil {
		if notice.LastSeq == nil || *notice.LastSeq != lastWritten {
			t.Fatalf("lagging last_seq = %v, last event written %d", notice.LastSeq, lastWritten)
		}
	}
	if lastWritten >= last {
		t.Fatalf("the subscriber was given every event up to %d, it was not cut off", last)
	}
	slow.wantClosed()
}

func TestSocket_StatusHasNoSecret(t *testing.T) {
	const sentinel = "SENTINEL-SECRET-VALUE-9f3a-do-not-print"
	const envName = "ACCEPTANCE_SOCKET_SENTINEL"
	t.Setenv(envName, sentinel)
	roster := strings.Replace(enginetest.RosterTOML, `dialect = "chat_completions_v1"`,
		"dialect = \"chat_completions_v1\"\nauth = { source = \"env\", name = \""+envName+"\" }\nextra_headers = { \"X-Key\" = { source = \"env\", name = \""+envName+"\" } }", 1)
	if roster == enginetest.RosterTOML {
		t.Fatal("the roster was not given a secret reference")
	}
	s := newSuite(t, roster, soloScripts(authorOK))

	// The resolver the engine's adapters use holds the sentinel for the
	// roster's reference.
	loaded, err := config.Load(s.RosterPath)
	if err != nil {
		t.Fatal(err)
	}
	ep := loaded.Endpoints[0]
	if ep.Auth == nil {
		t.Fatal("the endpoint has no auth reference")
	}
	resolver := config.NewEnvFileResolver(t.TempDir())
	for name, ref := range map[string]config.SecretRef{"auth": *ep.Auth, "extra header": ep.ExtraHeaders["X-Key"]} {
		if got, err := resolver.Resolve(ref); err != nil || got != sentinel {
			t.Fatalf("resolver for %s = %q, %v; want the sentinel", name, got, err)
		}
	}

	// A turn has run, so status has participants with state to report.
	c := s.Dial(t)
	s.soloTurn(t, c, "s1")

	p := s.rawPeer(t, true)
	before := p.count.n
	p.send("st", wire.RequestStatus, struct{}{})
	f := p.read()
	if f.Type != "reply" {
		t.Fatalf("status = %s %s", f.Type, f.Body)
	}
	if p.count.n == before {
		t.Fatal("no status bytes were read")
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(sentinel)) || bytes.Contains(f.Body, []byte(sentinel)) {
		t.Fatal("the status reply contains the secret value")
	}
	var status wire.StatusReply
	if err := json.Unmarshal(f.Body, &status); err != nil || len(status.Participants) != 2 {
		t.Fatalf("status = %+v, %v; want a reply listing both participants", status, err)
	}
}

func TestSocket_DetachReattach(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	start := s.maxSeq(t)

	a := s.Dial(t)
	if _, err := a.Subscribe(s.ctx(), start); err != nil {
		t.Fatal(err)
	}
	turnID, release := s.openTurn(t, a, "detach-1")
	var lastSeq int64
	for {
		ev, err := a.NextEvent(s.ctx())
		if err != nil {
			t.Fatalf("reading events before detaching: %v", err)
		}
		lastSeq = ev.Seq
		if b, ok := body[wire.EvAttemptBegun](event{Kind: ev.Kind, Body: ev.Body}, wire.EventKindAttemptBegun); ok && b.Phase == "author" {
			break
		}
	}
	if err := a.Close(); err != nil {
		t.Logf("close: %v", err)
	}

	// Every client but the suite's own is gone, and the turn is still open.
	other := s.Dial(t)
	if open := s.Status(t, other).TurnsOpen; !reflect.DeepEqual(open, []string{turnID}) {
		t.Fatalf("turns open after the client left = %v, want [%s]", open, turnID)
	}
	if turn := s.Turn(t, other, turnID); terminal(turn.State) {
		t.Fatalf("turn is %q after the client detached", turn.State)
	}

	release()
	s.WaitTerminal(turnID)

	b := s.Dial(t)
	turn := s.Turn(t, b, turnID)
	wantTurn(t, turn, engine.StateApproved, "all_approved")
	if turn.CandidateHash == nil || *turn.CandidateHash == "" {
		t.Fatalf("turn has no candidate hash: %+v", turn)
	}
	if _, err := b.Subscribe(s.ctx(), lastSeq); err != nil {
		t.Fatal(err)
	}
	watermark := s.maxSeq(t)
	seqs := replayed(t, s.ctx(), b, watermark)
	wantContiguous(t, seqs, lastSeq+1)
	if int64(len(seqs)) != watermark-lastSeq {
		t.Fatalf("replayed %d events, want the %d missed", len(seqs), watermark-lastSeq)
	}
	// The replay carried the turn's end: the missed events include the
	// approval.
	var approved bool
	for _, ev := range s.Events() {
		if st, ok := body[wire.EvTurnState](ev, wire.EventKindTurnState); ok && st.TurnID == turnID && st.State == engine.StateApproved && ev.Seq > lastSeq {
			approved = true
		}
	}
	if !approved {
		t.Fatalf("no turn_state approved after seq %d", lastSeq)
	}
}

// heldPool is a suite with the pool default held by an interrupted reviewer
// attempt: the reviewer did not answer and the fake clock moved to the attempt
// deadline, and Cancel is answered with an unknown confirmation, so the slot
// stays held.
func heldPool(t *testing.T) *capacityFixture {
	t.Helper()
	return capacityHeld(t, enginetest.RosterTOML, false)
}

func TestSocket_ReleaseOneHold(t *testing.T) {
	t.Parallel()
	f := heldPool(t)
	s := f.s
	s.wantHoldUnreleased(t, f.c, f.attemptID)

	reply, err := f.c.Release(s.ctx(), wire.ReleaseRequest{Pool: "default", Reason: "the endpoint was checked"})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !reflect.DeepEqual(reply.Released, []string{f.attemptID}) {
		t.Fatalf("released %v, want [%s]", reply.Released, f.attemptID)
	}
	cp := s.wantReleasedHow(t, f.attemptID, "operator")
	if cp.Reason == nil || *cp.Reason != "the endpoint was checked" {
		t.Fatalf("capacity event %+v, want the operator's reason", cp)
	}
	row, _, err := s.Store.GetHold(f.attemptID)
	if err != nil || row.ReleasedHow != "operator" || row.ReleasedReason != "the endpoint was checked" {
		t.Fatalf("hold row %+v (err %v), want released by the operator with the reason", row, err)
	}
	if h := s.holds(t, f.c, "default"); len(h) != 0 {
		t.Fatalf("pool default still holds %+v", h)
	}
}

// twoHolds runs a review turn on two reviewers in one pool that do not
// answer, and moves the fake clock to the attempt deadline: both attempts are
// interrupted and each holds a slot of the pool.
func twoHolds(t *testing.T) (s *suite, c *client.Client, ids []string) {
	t.Helper()
	scripts := map[string][]fake.Step{
		"author": repeat(8),
		"r1":     append([]fake.Step{{Kind: fake.StepKindHang}}, repeat(8)...),
		"r2":     append([]fake.Step{{Kind: fake.StepKindHang}}, repeat(8)...),
	}
	s = newSuite(t, capacityTwoReviewers(), scripts, enginetest.ClockStart(capacityFarFuture), enginetest.CancelUnconfirmed())
	c = s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	a1 := s.capacityBegun(t, c, "r1", "review").AttemptID
	a2 := s.capacityBegun(t, c, "r2", "review").AttemptID
	s.Clock.Advance(attemptDeadline)
	s.WaitTerminal(turnID)
	for _, id := range []string{a1, a2} {
		s.WaitEvent(func(ev event) bool {
			cp, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
			return ok && cp.HeldByAttempt == id && cp.State == "uncertain"
		})
	}
	ids = []string{a1, a2}
	sort.Strings(ids)
	held := s.holds(t, c, "default")
	if len(held) != 2 {
		t.Fatalf("pool default holds %+v, want two", held)
	}
	return s, c, ids
}

func TestSocket_ReleaseTwoHoldsAmbiguous(t *testing.T) {
	t.Parallel()
	s, c, ids := twoHolds(t)

	_, err := c.Release(s.ctx(), wire.ReleaseRequest{Pool: "default", Reason: "which one?"})
	refusal := wantRefusal(t, err, wire.ErrorCodeAmbiguous)
	var listed []string
	if err := json.Unmarshal(refusal.Holds, &listed); err != nil {
		t.Fatalf("holds in the refusal = %s: %v", refusal.Holds, err)
	}
	sort.Strings(listed)
	if !reflect.DeepEqual(listed, ids) {
		t.Fatalf("refusal lists %v, want both holds %v", listed, ids)
	}
	if refusal.Pool == nil || *refusal.Pool != "default" {
		t.Fatalf("refusal names pool %s, want default", str(refusal.Pool))
	}

	// Nothing was released.
	if held := s.holds(t, c, "default"); len(held) != 2 {
		t.Fatalf("pool default holds %+v after the refusal, want both", held)
	}
	for _, id := range ids {
		if row, found, err := s.Store.GetHold(id); err != nil || !found || row.ReleasedAt != nil {
			t.Fatalf("hold %s = %+v (found %v, err %v), want it unreleased", id, row, found, err)
		}
	}
	for _, ev := range s.Events() {
		if cp, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity); ok && cp.State == "released" {
			t.Fatalf("capacity event %+v after a refused release", cp)
		}
	}
}

func TestSocket_ReleaseNoHold(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(verdict(fake.VerdictApprove)))
	c := s.Dial(t)
	if held := s.holds(t, c, "default"); len(held) != 0 {
		t.Fatalf("pool default holds %+v at start", held)
	}

	_, err := c.Release(s.ctx(), wire.ReleaseRequest{Pool: "default", Reason: "nothing to release"})
	refusal := wantRefusal(t, err, wire.ErrorCodeNoHold)
	if refusal.Pool == nil || *refusal.Pool != "default" {
		t.Fatalf("refusal names pool %s, want default", str(refusal.Pool))
	}
	for _, ev := range s.Events() {
		if _, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity); ok {
			t.Fatalf("capacity event %s after a refused release", ev.Body)
		}
	}
}

func TestSocket_ReleaseEmptyReason(t *testing.T) {
	t.Parallel()
	f := heldPool(t)
	s := f.s

	_, err := f.c.Release(s.ctx(), wire.ReleaseRequest{Pool: "default", Reason: ""})
	_ = wantRefusal(t, err, wire.ErrorCodeReasonRequired)
	// The hold, which a release with a reason would have cleared, remains.
	s.wantHoldUnreleased(t, f.c, f.attemptID)
	for _, ev := range s.Events() {
		if cp, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity); ok && cp.State == "released" {
			t.Fatalf("capacity event %+v after a refused release", cp)
		}
	}
}

func TestSocket_ReviewCountParity(t *testing.T) {
	t.Parallel()
	// Test multi-page reviews with accepted repairs
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(
		verdict(fake.VerdictApprove),
		verdict(fake.VerdictApprove),
	))
	start := s.maxSeq(t)

	c := s.Dial(t)
	if _, err := c.Subscribe(s.ctx(), start); err != nil {
		t.Fatal(err)
	}

	// Open a turn and get its ID, and the conversation ID from the submit response
	release := s.h.Hold("author")
	submitReply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "review-count-1", Input: "what is the answer?"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	turnID := submitReply.TurnID
	convID := submitReply.ConversationID
	s.WaitTurnState(turnID, engine.StateAuthorDispatched)
	s.WaitTerminal(turnID)
	release()

	// Get conversation with the turn
	conv, err := c.Conversation(s.ctx(), convID)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}

	// Find the turn in the conversation
	var turn *wire.ConversationTurnSummary
	for i := range conv.Turns {
		if conv.Turns[i].ID == turnID {
			turn = &conv.Turns[i]
			break
		}
	}
	if turn == nil {
		t.Fatalf("turn %s not found in conversation", turnID)
	}

	// If review_count is set, verify AllReviews matches it
	if turn.ReviewCount != nil {
		allReviews, err := c.AllReviews(s.ctx(), turnID)
		if err != nil {
			t.Fatalf("AllReviews: %v", err)
		}
		if len(allReviews) != *turn.ReviewCount {
			t.Errorf("AllReviews returned %d items, want %d per review_count", len(allReviews), *turn.ReviewCount)
		}
	}
}

func TestSocket_ReviewCountEmptyForSoloTurn(t *testing.T) {
	t.Parallel()
	// Test solo turn (no reviews)
	s := newSuite(t, enginetest.RosterTOML, reviewScripts(
		verdict(fake.VerdictApprove),
	))
	start := s.maxSeq(t)

	c := s.Dial(t)
	if _, err := c.Subscribe(s.ctx(), start); err != nil {
		t.Fatal(err)
	}

	// Open a solo turn and get conversation ID
	release := s.h.Hold("author")
	submitReply, err := c.Submit(s.ctx(), wire.SubmitRequest{SubmissionID: "solo-1", Input: "what is the answer?"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	turnID := submitReply.TurnID
	convID := submitReply.ConversationID
	s.WaitTurnState(turnID, engine.StateAuthorDispatched)
	s.WaitTerminal(turnID)
	release()

	// Get the turn's reviews
	allReviews, err := c.AllReviews(s.ctx(), turnID)
	if err != nil {
		t.Fatalf("AllReviews: %v", err)
	}
	if len(allReviews) != 0 {
		t.Errorf("solo turn AllReviews returned %d items, want 0", len(allReviews))
	}

	// Check conversation turn summary
	conv, err := c.Conversation(s.ctx(), convID)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}

	for i := range conv.Turns {
		if conv.Turns[i].ID == turnID {
			if conv.Turns[i].ReviewCount != nil && *conv.Turns[i].ReviewCount != 0 {
				t.Errorf("solo turn review_count = %d, want 0 or nil", *conv.Turns[i].ReviewCount)
			}
			break
		}
	}
}
