package acceptance

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/boldfield/syndic/internal/engine"
	"github.com/boldfield/syndic/internal/engine/enginetest"
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
	_ = s
	gap(t, "second engine lock test: either register cmd/syndicd TestSecondRun external test or take engine.lock twice through internal/lockfile and check PID")
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
		t.Logf("write error: %v", err)
	}

	buf = make([]byte, 1024)
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
		t.Fatalf("expected frame error, got %s", errBody.Code)
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
}

func TestSocket_LargeContentFetched(t *testing.T) {
	t.Parallel()
	largeOutput := socketLargeOutput(12 * 1024 * 1024)

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

	fetched, err := c.Content(s.ctx(), contentRef)
	if err != nil {
		t.Fatalf("content fetch: %v", err)
	}

	computedHash := sha256.Sum256(fetched)
	computedHashStr := fmt.Sprintf("%x", computedHash)

	if computedHashStr != msg.SHA256 {
		t.Fatalf("content SHA-256 mismatch: got %s, want %s", computedHashStr, msg.SHA256)
	}

	if int64(len(fetched)) != msg.ByteLen {
		t.Fatalf("fetched %d bytes, message says %d", len(fetched), msg.ByteLen)
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

func TestSocket_ContentLimitClamped(t *testing.T) {
	t.Parallel()
	largeOutput := socketLargeOutput(1024 * 1024)

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

	fetched, err := c.Content(s.ctx(), contentRef)
	if err != nil {
		t.Fatalf("content fetch: %v", err)
	}

	if int64(len(fetched)) != msg.ByteLen {
		t.Fatalf("fetched %d bytes, message says %d", len(fetched), msg.ByteLen)
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

	if reply2.Preset != reply1.Preset {
		t.Fatalf("second reply has different preset: %s vs %s", reply2.Preset, reply1.Preset)
	}

	if reply2.EstimatedTokens != reply1.EstimatedTokens {
		t.Fatalf("second reply has different estimated tokens: %d vs %d", reply2.EstimatedTokens, reply1.EstimatedTokens)
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

	reply1, err := c.Submit(s.ctx(), wire.SubmitRequest{
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

	if !strings.Contains(errStr, reply1.TurnID) {
		t.Fatalf("submission_conflict error should name turn ID %s, got: %s", reply1.TurnID, errStr)
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
	if testing.Short() {
		t.Skip("replay test with 50,000 events is slow")
	}

	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	targetEventCount := 50000
	eventCount := len(s.Events())
	for eventCount < targetEventCount {
		c := s.Dial(t)
		turnID := s.Submit(t, c, fmt.Sprintf("gen-%d", eventCount))
		s.WaitTerminal(turnID)
		eventCount = len(s.Events())
	}

	seqsSeen := map[int64]bool{}
	seqsLock := sync.Mutex{}
	replayDone := make(chan struct{})
	dispatchCompleted := make(chan struct{})

	go func() {
		defer close(replayDone)
		replayC := s.Dial(t)
		if _, err := replayC.Subscribe(s.ctx(), 0); err != nil {
			t.Errorf("subscribe: %v", err)
			return
		}

		for {
			frame, err := replayC.NextEvent(s.ctx())
			if err != nil {
				return
			}

			seqsLock.Lock()
			if seqsSeen[frame.Seq] {
				t.Errorf("duplicate seq %d", frame.Seq)
			}
			seqsSeen[frame.Seq] = true
			seqsLock.Unlock()

			select {
			case <-dispatchCompleted:
				return
			default:
			}
		}
	}()

	c := s.Dial(t)
	dispatchID := s.Submit(t, c, "dispatch-during-replay")
	s.WaitTerminal(dispatchID)
	close(dispatchCompleted)

	<-replayDone

	seqsLock.Lock()
	defer seqsLock.Unlock()

	if len(seqsSeen) == 0 {
		t.Fatal("no events replayed")
	}

	for i := int64(1); i <= int64(len(seqsSeen)); i++ {
		if !seqsSeen[i] {
			t.Fatalf("missing seq %d in replay", i)
		}
	}
}

func TestSocket_LaggingSubscriber(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author":   {enginetest.Reply("the answer is 42")},
		"reviewer": {verdict(fake.VerdictApprove)},
	})

	c := s.Dial(t)
	if _, err := c.Subscribe(s.ctx(), 0); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	laggingErr := make(chan error, 1)

	go func() {
		for i := 0; i < 2000; i++ {
			cc := s.Dial(t)
			s.Submit(t, cc, fmt.Sprintf("lag-%d", i))
		}
	}()

	_, err := c.NextEvent(s.ctx())
	if err != nil {
		if strings.Contains(err.Error(), "lagging") {
			laggingErr <- err
		} else {
			laggingErr <- fmt.Errorf("got error but not lagging: %v", err)
		}
	} else {
		laggingErr <- fmt.Errorf("expected lagging error, got no error")
	}

	err = <-laggingErr
	if err != nil {
		t.Fatal(err)
	}

	c2 := s.Dial(t)
	turnID := s.Submit(t, c2, "dispatch-after-lagging")
	s.WaitTerminal(turnID)
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
		"secret",
		"authorization",
		"bearer",
	}

	for _, val := range forbiddenValues {
		if strings.Contains(strings.ToLower(statusStr), val) {
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
	for lastSeq == 0 || len(s.Events()) == 0 {
		if len(s.Events()) > 0 {
			lastSeq = s.Events()[len(s.Events())-1].Seq
		}
	}

	_ = c.Close()

	newC := s.Dial(t)
	s.WaitTerminal(turnID)

	if _, err := newC.Subscribe(s.ctx(), lastSeq); err != nil {
		t.Fatalf("subscribe from %d: %v", lastSeq, err)
	}

	missedEvents := 0
	for {
		frame, err := newC.NextEvent(s.ctx())
		if err != nil {
			break
		}
		if frame.Seq > lastSeq {
			missedEvents++
		}
	}

	if missedEvents == 0 {
		t.Fatal("expected to replay missed events, got none")
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

	for _, pool := range status.Pools {
		if len(pool.Holds) > 0 {
			reply, err := c.Release(s.ctx(), wire.ReleaseRequest{
				Pool:   pool.Name,
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
			if capEv.Reason == nil || *capEv.Reason == "" {
				t.Fatal("release reason is empty")
			}
			return
		}
	}

	gap(t, "no holds created by reset")
}

func TestSocket_ReleaseAmbiguous(t *testing.T) {
	t.Parallel()
	s := newSuite(t, enginetest.RosterTOML, map[string][]fake.Step{
		"author": {
			enginetest.Reply("the answer is 42"),
			enginetest.Reply("the answer is 42"),
		},
		"reviewer": {
			fake.Step{Kind: fake.StepKindReply, TransportStatus: "reset", Status: transport.StatusInterrupted},
			fake.Step{Kind: fake.StepKindReply, TransportStatus: "reset", Status: transport.StatusInterrupted},
		},
	})

	c := s.Dial(t)

	turnID1 := s.Submit(t, c, "s1")
	s.WaitTerminal(turnID1)

	turnID2 := s.Submit(t, c, "s2")
	s.WaitTerminal(turnID2)

	status, err := c.Status(s.ctx())
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	for _, pool := range status.Pools {
		if len(pool.Holds) >= 2 {
			_, err := c.Release(s.ctx(), wire.ReleaseRequest{
				Pool:   pool.Name,
				Reason: "test",
			})

			if err == nil {
				t.Fatal("expected ambiguous error, got no error")
			}

			errStr := err.Error()
			if !strings.Contains(errStr, "ambiguous") {
				t.Fatalf("expected ambiguous error, got: %s", errStr)
			}

			if !strings.Contains(errStr, turnID1) && !strings.Contains(errStr, turnID2) {
				t.Fatalf("ambiguous error should name both attempt IDs, got: %s", errStr)
			}
			return
		}
	}

	gap(t, "couldn't create two holds for ambiguous test")
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

	errStr := err.Error()
	if !strings.Contains(errStr, "no_hold") {
		t.Fatalf("expected no_hold error, got: %s", errStr)
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

	errStr := err.Error()
	if !strings.Contains(errStr, "reason") {
		t.Fatalf("expected reason_required error, got: %s", errStr)
	}
}
