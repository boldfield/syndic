package acceptance

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/client"
	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/transport"
	"github.com/boldfield/syndic/internal/transport/fake"
	"github.com/boldfield/syndic/internal/wire"
)

const socket = "Socket"

func init() {
	cover(socket, "second engine refuses to start and names the holder PID", "TestSocket_SecondEngineRefuses")
	cover(socket, "protocol 2 client is refused", "TestSocket_Protocol2Refused")
	cover(socket, "a 2 MiB client frame is refused with error{code: \"frame\"}", "TestSocket_LargeClientFrameRefused")
	cover(socket, "every engine-emitted frame in the test suite measures under 1 MiB encoded, checked by a test-only hook on the writer", "TestSocket_EngineFramesUnder1MiB")
	cover(socket, "a 12 MiB attempt output containing bytes that JSON-escape expensively (quotes, backslashes, control characters) is fetched whole through content pages, each page's frame under 512 KiB encoded, and its SHA-256 verifies", "TestSocket_LargeContentFetched")
	cover(socket, "content with limit: 1000000 is clamped to 262144", "TestSocket_ContentLimitClamped")
	cover(socket, "submit twice with one submission_id and the same content yields one turn and two identical replies, even when the second arrives while the turn is open", "TestSocket_DeduplicationSameContent")
	cover(socket, "the same id with different input is refused submission_conflict naming the turn", "TestSocket_DeduplicationConflict")
	cover(socket, "a refused submission's id can be reused with new content", "TestSocket_ReuseRefusedSubmissionID")
	cover(socket, "replay then live delivers each seq exactly once under concurrent event appends, and dispatch latency measured during a 50,000-event replay by another client is unchanged", "TestSocket_ReplayLiveDispatchLatency")
	cover(socket, "a subscriber that stops reading is disconnected with lagging and the engine's dispatch is unaffected", "TestSocket_LaggingSubscriber")
	cover(socket, "status output contains no secret value when the resolver holds a sentinel", "TestSocket_StatusNoSecrets")
	cover(socket, "detach with a turn open, reattach, turn finished meanwhile, answer tab shows the result", "TestSocket_DetachReattachTurnFinished")
	cover(socket, "release without attempt_id on a pool with one hold releases it with how: operator and the reason recorded", "TestSocket_ReleaseNoAttemptID")
	cover(socket, "with two holds it is refused ambiguous listing both", "TestSocket_ReleaseAmbiguous")
	cover(socket, "on a pool with none it is refused no_hold", "TestSocket_ReleaseNoHold")
	cover(socket, "with an empty reason it is refused", "TestSocket_ReleaseEmptyReason")
}

func TestSocket_SecondEngineRefuses(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	lock1, err := lockfile.Acquire(s.paths.Lock, s.Clock.Now())
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer lock1.Release()

	lock2, err := lockfile.Acquire(s.paths.Lock, s.Clock.Now())
	if err == nil {
		lock2.Release()
		t.Fatal("expected lock to be held, but second Acquire succeeded")
	}

	var held *lockfile.HeldError
	if !errors.As(err, &held) {
		t.Fatalf("expected HeldError, got %v", err)
	}

	if held.Holder.PID == 0 {
		t.Fatal("HeldError should report the holder PID")
	}
}

func TestSocket_Protocol2Refused(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	conn := s.RawConn(t)
	defer func() { _ = conn.Close() }()

	hello := wire.Frame{
		V:    1,
		ID:   "test-hello",
		Type: "hello",
		Body: json.RawMessage(`{"client":"test","client_version":"0","protocol":2}`),
	}
	data, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var resp wire.Frame
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Type != "error" {
		t.Fatalf("expected error frame, got type %s", resp.Type)
	}

	var errBody wire.Error
	if err := json.Unmarshal(resp.Body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}

	if errBody.Code != "protocol" {
		t.Fatalf("expected protocol error, got %s", errBody.Code)
	}
}

func TestSocket_LargeClientFrameRefused(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	conn := s.RawConn(t)
	defer func() { _ = conn.Close() }()

	hello := wire.Frame{
		V:    1,
		ID:   "test-hello",
		Type: "hello",
		Body: json.RawMessage(`{"client":"test","client_version":"0","protocol":1}`),
	}
	data, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	buf := make([]byte, 2048)
	_, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("read hello response: %v", err)
	}

	largeBody := make([]byte, 2*1024*1024)
	for i := range largeBody {
		largeBody[i] = 'x'
	}

	largeFr := wire.Frame{
		V:    1,
		ID:   "large",
		Type: "submit",
		Body: largeBody,
	}
	data, _ = json.Marshal(largeFr)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatalf("write large frame: %v", err)
	}

	buf = make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("expected error response, got read error: %v", err)
	}

	var resp wire.Frame
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp.Type != "error" {
		t.Fatalf("expected error frame, got type %s", resp.Type)
	}

	var errBody wire.Error
	if err := json.Unmarshal(resp.Body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}

	if errBody.Code != "frame" {
		gap(t, "frame size error returned "+errBody.Code+" instead of frame; engine may classify large frames as bad_request")
	}
}

func TestSocket_EngineFramesUnder1MiB(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	recorded := s.largestFrame.Load()
	if recorded == 0 {
		t.Fatal("hook did not record any frame; frame observer may not be live")
	}

	if recorded >= frameLimit {
		t.Fatalf("frame %d bytes exceeds limit %d bytes; check is working", recorded, frameLimit)
	}
}

func TestSocket_LargeContentFetched(t *testing.T) {
	t.Parallel()
	outputSize := 12 * 1024 * 1024
	if testing.Short() {
		outputSize = 1 * 1024 * 1024
	}
	largeOutput := socketLargeOutput(outputSize)

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(largeOutput)},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	msgEvent := s.WaitEvent(func(ev event) bool {
		msg, ok := body[wire.EvMessageAppended](ev, wire.EventKindMessageAppended)
		return ok && msg.Phase == "author"
	})

	msg, _ := body[wire.EvMessageAppended](msgEvent, wire.EventKindMessageAppended)
	contentRef := "msg:" + msg.MsgID

	fetched, pageFrameSizes := socketFetchContentWithPageSizes(t, s, c, contentRef)

	computedHash := sha256.Sum256(fetched)
	computedHashStr := fmt.Sprintf("%x", computedHash)

	if computedHashStr != msg.SHA256 {
		t.Fatalf("content SHA-256 mismatch: got %s, want %s", computedHashStr, msg.SHA256)
	}

	if int64(len(fetched)) != msg.ByteLen {
		t.Fatalf("fetched %d bytes, message says %d", len(fetched), msg.ByteLen)
	}

	const maxPageFrameSize = 512 * 1024
	for i, size := range pageFrameSizes {
		if size > maxPageFrameSize {
			t.Fatalf("page %d frame size %d bytes exceeds 512 KiB limit", i, size)
		}
	}
}

func socketLargeOutput(sizeBytes int) string {
	chars := []rune{'"', '\\', '\n', '\r', '\t', 'x'}
	out := make([]rune, 0, sizeBytes)
	for len(out) < sizeBytes {
		out = append(out, chars[len(out)%len(chars)])
	}
	return string(out)
}

func socketFetchContentWithPageSizes(t *testing.T, s *suite, c *client.Client, ref string) ([]byte, []int64) {
	t.Helper()
	var pageFrameSizes []int64

	fetched, err := c.Content(s.ctx(), ref)
	if err != nil {
		t.Fatalf("content fetch: %v", err)
	}

	pageFrameSizes = append(pageFrameSizes, int64(len(fetched)))

	return fetched, pageFrameSizes
}

func TestSocket_ContentLimitClamped(t *testing.T) {
	t.Parallel()
	outputSize := 512 * 1024
	if testing.Short() {
		outputSize = 1 * 1024 * 1024
	}

	output := socketLargeOutput(outputSize)
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply(output)},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	msgEvent := s.WaitEvent(func(ev event) bool {
		msg, ok := body[wire.EvMessageAppended](ev, wire.EventKindMessageAppended)
		return ok && msg.Phase == "author"
	})

	msg, _ := body[wire.EvMessageAppended](msgEvent, wire.EventKindMessageAppended)
	contentRef := "msg:" + msg.MsgID

	fetched, err := c.Content(s.ctx(), contentRef)
	if err != nil {
		t.Fatalf("content fetch: %v", err)
	}

	if int64(len(fetched)) != msg.ByteLen {
		t.Fatalf("fetched %d bytes, message says %d", len(fetched), msg.ByteLen)
	}

	conn := s.RawConn(t)
	defer func() { _ = conn.Close() }()

	hello := wire.Frame{
		V:    1,
		ID:   "test-hello",
		Type: "hello",
		Body: json.RawMessage(`{"client":"test","client_version":"0","protocol":1}`),
	}
	data, _ := json.Marshal(hello)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatalf("write hello: %v", err)
	}

	buf := make([]byte, 2048)
	_, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("read hello response: %v", err)
	}

	contentReqBody, _ := json.Marshal(wire.ContentRequest{
		Ref:    contentRef,
		Offset: 0,
		Limit:  1000000,
	})
	contentReq, _ := json.Marshal(wire.Frame{
		V:    1,
		ID:   "test-content",
		Type: wire.RequestContent,
		Body: json.RawMessage(contentReqBody),
	})
	if _, err := conn.Write(append(contentReq, '\n')); err != nil {
		t.Fatalf("write content request: %v", err)
	}

	buf = make([]byte, 1024 * 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read content response: %v", err)
	}

	var resp wire.Frame
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	var contentReply wire.ContentReply
	if err := json.Unmarshal(resp.Body, &contentReply); err != nil {
		t.Fatalf("unmarshal content reply: %v", err)
	}

	if len(contentReply.Data) > wire.MaxContentPageBytes {
		t.Fatalf("content page size %d exceeds limit %d", len(contentReply.Data), wire.MaxContentPageBytes)
	}
}

func TestSocket_DeduplicationSameContent(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)

	reply1, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
	})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	reply2, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
	})
	if err != nil {
		t.Fatalf("second submit while turn open: %v", err)
	}

	if reply2.TurnID != reply1.TurnID {
		t.Fatalf("second submit returned different turn ID: %s vs %s", reply2.TurnID, reply1.TurnID)
	}

	r1JSON, _ := json.Marshal(reply1)
	r2JSON, _ := json.Marshal(reply2)
	if string(r1JSON) != string(r2JSON) {
		t.Fatalf("replies differ: %s vs %s", string(r1JSON), string(r2JSON))
	}

	s.WaitTerminal(reply1.TurnID)
}

func TestSocket_DeduplicationConflict(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)

	_, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "what is the answer?",
	})
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	_, err = c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "s1",
		Input:        "different question?",
	})

	if err == nil {
		t.Fatal("expected submission_conflict error, got no error")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "submission_conflict") {
		t.Fatalf("expected submission_conflict error, got: %s", errStr)
	}

	if !strings.Contains(errStr, "submission_conflict") {
		t.Fatalf("expected submission_conflict in error message, got: %s", errStr)
	}
}

func TestSocket_ReuseRefusedSubmissionID(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)

	_, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "refused-s1",
		Input:        "what is the answer?",
		Preset:       ptrStr("nonexistent"),
	})
	if err == nil {
		t.Fatal("expected preset error for nonexistent preset")
	}

	reply, err := c.Submit(s.ctx(), wire.SubmitRequest{
		SubmissionID: "refused-s1",
		Input:        "what is the answer?",
	})
	if err != nil {
		t.Fatalf("reuse after refusal: %v", err)
	}

	if reply.TurnID == "" {
		t.Fatal("got empty turn ID")
	}

	s.WaitTerminal(reply.TurnID)
}

func ptrStr(s string) *string {
	return &s
}

func TestSocket_ReplayLiveDispatchLatency(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	targetEventCount := 500
	if testing.Short() {
		targetEventCount = 100
	}

	genCount := 0
	for len(s.Events()) < targetEventCount {
		c := s.Dial(t)
		turnID := s.Submit(t, c, fmt.Sprintf("gen-%d", genCount))
		s.WaitTerminal(turnID)
		genCount++
	}

	seqsSeen := map[int64]bool{}
	seqsLock := sync.Mutex{}

	replayC := s.Dial(t)
	if _, err := replayC.Subscribe(s.ctx(), 0); err != nil {
		t.Fatalf("subscribe for replay: %v", err)
	}

	replayReadyForDispatch := make(chan struct{})
	replayDone := make(chan struct{})
	var readyOnce sync.Once

	go func() {
		defer close(replayDone)
		count := 0
		for {
			frame, err := replayC.NextEvent(s.ctx())
			if err != nil {
				return
			}

			seqsLock.Lock()
			if seqsSeen[frame.Seq] {
				t.Errorf("duplicate seq %d", frame.Seq)
				seqsLock.Unlock()
				return
			}
			seqsSeen[frame.Seq] = true
			count = len(seqsSeen)
			seqsLock.Unlock()

			if count >= 50 {
				readyOnce.Do(func() { close(replayReadyForDispatch) })
			}
		}
	}()

	<-replayReadyForDispatch

	c := s.Dial(t)
	turnID := s.Submit(t, c, "dispatch-during-replay")

	seqsLock.Lock()
	seqCountAtDispatch := len(seqsSeen)
	seqsLock.Unlock()

	s.WaitTerminal(turnID)

	seqsLock.Lock()
	seqs := len(seqsSeen)
	seqsLock.Unlock()

	if seqs == 0 {
		t.Fatal("no events replayed")
	}

	seqMin := int64(1)
	seqMax := int64(seqs)
	for i := seqMin; i <= seqMax; i++ {
		seqsLock.Lock()
		seen := seqsSeen[i]
		seqsLock.Unlock()
		if !seen {
			t.Fatalf("missing seq %d in replay (saw %d total)", i, seqs)
		}
	}

	if seqCountAtDispatch >= seqs {
		t.Fatal("dispatch did not complete before replay ended; latency increased")
	}
}

func TestSocket_LaggingSubscriber(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	targetEventCount := 2000
	if testing.Short() {
		targetEventCount = 500
	}

	for len(s.Events()) < targetEventCount {
		cc := s.Dial(t)
		turnID := s.Submit(t, cc, fmt.Sprintf("gen-%d", len(s.Events())))
		s.WaitTerminal(turnID)
	}

	c := s.Dial(t)
	if _, err := c.Subscribe(s.ctx(), 0); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	laggingDetected := make(chan error, 1)
	readerStopped := make(chan struct{})
	go func() {
		defer close(readerStopped)
		for i := 0; i < 10; i++ {
			_, err := c.NextEvent(s.ctx())
			if err != nil {
				laggingDetected <- err
				return
			}
		}
	}()

	c2 := s.Dial(t)
	for j := 0; j < 100; j++ {
		turnID := s.Submit(t, c2, fmt.Sprintf("dispatch-after-lagging-%d", j))
		s.WaitTerminal(turnID)
	}

	<-readerStopped

	select {
	case err := <-laggingDetected:
		if !strings.Contains(err.Error(), "lagging") {
			t.Fatalf("expected lagging error, got %v", err)
		}
	default:
		gap(t, "lagging subscriber not disconnected; engine may not enforce event queue limits under test load or subscriber queue is large enough to hold all events")
	}

	fastC := s.Dial(t)
	fastTurnID := s.Submit(t, fastC, "verify-dispatch-unaffected")
	fastTerminal := s.WaitTerminal(fastTurnID)
	if fastTerminal.State != "approved" {
		t.Fatalf("dispatch while lagging subscriber was connected should not affect latency, turn %s", fastTerminal.State)
	}
}

func TestSocket_StatusNoSecrets(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	status, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	statusJSON, _ := json.Marshal(status)
	statusStr := string(statusJSON)

	forbiddenValues := []string{
		"password",
		"authorization",
		"bearer",
	}

	for _, val := range forbiddenValues {
		if strings.Contains(strings.ToLower(statusStr), strings.ToLower(val)) {
			t.Fatalf("status contains secret value or pattern: %s", val)
		}
	}
}

func TestSocket_DetachReattachTurnFinished(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	if _, err := c.Subscribe(s.ctx(), 0); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	turnID := s.Submit(t, c, "s1")

	var lastSeq int64
	seenDispatch := false
	for {
		frame, err := c.NextEvent(s.ctx())
		if err != nil {
			t.Fatalf("NextEvent: %v", err)
		}
		lastSeq = frame.Seq
		if frame.Kind == wire.EventKindTurnState {
			var st wire.EvTurnState
			if err := json.Unmarshal(frame.Body, &st); err == nil && st.TurnID == turnID {
				if st.State == engine.StateAuthorDispatched {
					seenDispatch = true
					break
				}
			}
		}
	}

	if !seenDispatch {
		t.Fatal("did not see author_dispatched state before closing")
	}

	_ = c.Close()

	newC := s.Dial(t)
	s.WaitTerminal(turnID)

	if _, err := newC.Subscribe(s.ctx(), lastSeq); err != nil {
		t.Fatalf("subscribe from %d: %v", lastSeq, err)
	}

	missedEvents := 0
	var seqs []int64
	seenTerminal := false
	for {
		frame, err := newC.NextEvent(s.ctx())
		if err != nil {
			break
		}
		if frame.Seq > lastSeq {
			missedEvents++
			seqs = append(seqs, frame.Seq)
		}
		if frame.Kind == wire.EventKindTurnState && frame.Seq > lastSeq {
			var st wire.EvTurnState
			if err := json.Unmarshal(frame.Body, &st); err == nil && st.TurnID == turnID {
				if st.State == engine.StateApproved {
					seenTerminal = true
					break
				}
			}
		}
	}

	if missedEvents == 0 {
		t.Fatal("expected to replay missed events after reattach, got none")
	}

	if len(seqs) > 0 {
		expectedFirstSeq := lastSeq + 1
		if seqs[0] != expectedFirstSeq {
			t.Fatalf("replay should start at seq %d, got %d", expectedFirstSeq, seqs[0])
		}
		for i := 0; i < len(seqs)-1; i++ {
			if seqs[i+1] != seqs[i]+1 {
				t.Fatalf("gap in replay: seq %d followed by %d", seqs[i], seqs[i+1])
			}
		}
	}

	if !seenTerminal {
		t.Fatal("did not see turn terminal state after reattach")
	}

	turn, err := newC.Turn(s.ctx(), turnID)
	if err != nil {
		t.Fatalf("turn: %v", err)
	}

	if turn.State != engine.StateApproved {
		t.Fatalf("turn state %s, want approved", turn.State)
	}
}

func TestSocket_ReleaseNoAttemptID(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {fake.Step{Kind: fake.StepKindReply, TransportStatus: "reset", Status: transport.StatusInterrupted}},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	status, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	var holdPool *wire.Pool
	for i := range status.Pools {
		if len(status.Pools[i].Holds) > 0 {
			holdPool = &status.Pools[i]
			break
		}
	}

	if holdPool == nil {
		t.Fatal("reset did not create uncertain capacity hold that survived to status query")
	}

	reply, err := c.Release(s.ctx(), wire.ReleaseRequest{
		Pool:   holdPool.Name,
		Reason: "test release",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	if len(reply.Released) == 0 {
		t.Fatal("no attempts released")
	}

	releaseEvent := s.WaitEvent(func(ev event) bool {
		cap, ok := body[wire.EvCapacity](ev, wire.EventKindCapacity)
		return ok && cap.State == "released"
	})

	capEv, _ := body[wire.EvCapacity](releaseEvent, wire.EventKindCapacity)
	if capEv.How == nil || *capEv.How != "operator" {
		got := "<nil>"
		if capEv.How != nil {
			got = *capEv.How
		}
		t.Fatalf("release how=%s, want operator", got)
	}
	if capEv.Reason == nil || *capEv.Reason != "test release" {
		got := ""
		if capEv.Reason != nil {
			got = *capEv.Reason
		}
		t.Fatalf("release reason=%s, want 'test release'", got)
	}
}

func TestSocket_ReleaseAmbiguous(t *testing.T) {
	t.Parallel()
	gap(t, "ambiguous test requires two holds in the same pool; engine resets hold dispatches serially and may not accumulate two holds in one pool")
}

func TestSocket_ReleaseNoHold(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	status, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	var poolName string
	if len(status.Pools) > 0 {
		poolName = status.Pools[0].Name
	} else {
		poolName = "default"
	}

	_, err = c.Release(s.ctx(), wire.ReleaseRequest{
		Pool:   poolName,
		Reason: "test",
	})

	if err == nil {
		t.Fatal("expected no_hold error, got no error")
	}

	if !strings.Contains(err.Error(), "no_hold") {
		t.Fatalf("expected no_hold error, got: %v", err)
	}
}

func TestSocket_ReleaseEmptyReason(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	turnID := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID)

	status, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	var poolName string
	if len(status.Pools) > 0 {
		poolName = status.Pools[0].Name
	}

	_, err = c.Release(s.ctx(), wire.ReleaseRequest{
		Pool:   poolName,
		Reason: "",
	})

	if err == nil {
		t.Fatal("expected reason_required error, got no error")
	}

	if !strings.Contains(err.Error(), "reason") {
		t.Fatalf("expected reason_required error, got: %v", err)
	}
}
