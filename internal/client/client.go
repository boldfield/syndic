package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

var ErrClosed = errors.New("client: connection closed")

type Client struct {
	conn   net.Conn
	reader *wire.Reader
	hello  wire.HelloReply

	// writeMu serialises frame writes. It is never held while waiting for
	// anything but the write itself, so Close can always unblock it.
	writeMu sync.Mutex
	writer  *wire.Writer

	// mu guards closed, closeErr and pending. A pending request is completed
	// by whoever removes it from the map, so each is completed exactly once.
	mu       sync.Mutex
	closed   bool
	closeErr error
	pending  map[string]chan result

	// eventMu guards the event stream state. readLoop is the only producer of
	// events; it appends in arrival order, so NextEvent order is wire order.
	eventMu    sync.Mutex
	accepting  bool
	events     []wire.Frame
	lastSeq    int64
	eventWake  chan struct{}
	eventsDone error

	idCounter uint64
}

type result struct {
	body []byte
	err  error
}

func Dial(ctx context.Context, socketPath, clientName, clientVersion string) (*Client, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	c := &Client{
		conn:      conn,
		reader:    wire.NewReader(conn),
		writer:    wire.NewWriter(conn),
		pending:   make(map[string]chan result),
		eventWake: make(chan struct{}, 1),
	}

	req, err := wire.NewRequest("", wire.RequestHello, wire.HelloRequest{
		Client:        clientName,
		ClientVersion: clientVersion,
		Protocol:      wire.ProtocolVersion,
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("hello request: %w", err)
	}

	// Cancelling ctx (or hitting its deadline) interrupts the handshake I/O.
	// The watcher is stopped, and waited for, before Dial returns so it can
	// never touch the live connection afterwards.
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(interrupted)
		_ = conn.SetDeadline(time.Now())
	})
	fail := func(what string, err error) (*Client, error) {
		if !stop() {
			<-interrupted
		}
		_ = conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%s: %w", what, ctxErr)
		}
		return nil, fmt.Errorf("%s: %w", what, err)
	}

	if err := c.writer.Write(req); err != nil {
		return fail("write hello", err)
	}
	frame, err := c.reader.Read()
	if err != nil {
		return fail("read hello reply", err)
	}
	if !stop() {
		<-interrupted
		_ = conn.Close()
		return nil, fmt.Errorf("hello: %w", ctx.Err())
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("clear deadline: %w", err)
	}

	switch frame.Type {
	case "error":
		_ = conn.Close()
		var errBody wire.Error
		if err := json.Unmarshal(frame.Body, &errBody); err != nil {
			return nil, fmt.Errorf("unmarshal error: %w", err)
		}
		return nil, &errBody
	case "reply":
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("expected reply, got %s", frame.Type)
	}

	if err := json.Unmarshal(frame.Body, &c.hello); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("unmarshal hello reply: %w", err)
	}

	go c.readLoop()

	return c, nil
}

func (c *Client) Hello() wire.HelloReply {
	return c.hello
}

func (c *Client) Close() error {
	c.shutdown(ErrClosed)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

// shutdown marks the client closed (once), closes the connection, fails every
// pending request with err and wakes event readers.
func (c *Client) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = c.conn.Close()
	pending := c.pending
	c.pending = make(map[string]chan result)
	c.mu.Unlock()

	for _, ch := range pending {
		ch <- result{err: err}
	}

	c.eventMu.Lock()
	c.eventsDone = err
	c.eventMu.Unlock()
	c.wakeEvents()
}

func (c *Client) wakeEvents() {
	select {
	case c.eventWake <- struct{}{}:
	default:
	}
}

func (c *Client) Status(ctx context.Context) (wire.StatusReply, error) {
	var reply wire.StatusReply
	body, err := c.doRequest(ctx, wire.RequestStatus, wire.StatusRequest{})
	if err != nil {
		return reply, err
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return reply, fmt.Errorf("unmarshal status reply: %w", err)
	}
	return reply, nil
}

func (c *Client) Submit(ctx context.Context, req wire.SubmitRequest) (wire.SubmitReply, error) {
	var reply wire.SubmitReply
	body, err := c.doRequest(ctx, wire.RequestSubmit, req)
	if err != nil {
		return reply, err
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return reply, fmt.Errorf("unmarshal submit reply: %w", err)
	}
	return reply, nil
}

func (c *Client) Cancel(ctx context.Context, turnID string) error {
	_, err := c.doRequest(ctx, wire.RequestCancel, wire.CancelRequest{TurnID: turnID})
	return err
}

func (c *Client) Override(ctx context.Context, turnID, reason string) error {
	_, err := c.doRequest(ctx, wire.RequestOverride, wire.OverrideRequest{TurnID: turnID, Reason: reason})
	return err
}

func (c *Client) Release(ctx context.Context, req wire.ReleaseRequest) (wire.ReleaseReply, error) {
	var reply wire.ReleaseReply
	body, err := c.doRequest(ctx, wire.RequestRelease, req)
	if err != nil {
		return reply, err
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return reply, fmt.Errorf("unmarshal release reply: %w", err)
	}
	return reply, nil
}

func (c *Client) Turn(ctx context.Context, turnID string) (wire.TurnReply, error) {
	var reply wire.TurnReply
	body, err := c.doRequest(ctx, wire.RequestTurn, wire.TurnRequest{TurnID: turnID})
	if err != nil {
		return reply, err
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return reply, fmt.Errorf("unmarshal turn reply: %w", err)
	}
	return reply, nil
}

func (c *Client) Transcript(ctx context.Context, turnID, cursor string, limit int) (wire.TranscriptReply, error) {
	var reply wire.TranscriptReply
	req := wire.TranscriptRequest{TurnID: turnID}
	if cursor != "" {
		req.Cursor = &cursor
	}
	if limit > 0 {
		req.Limit = &limit
	}
	body, err := c.doRequest(ctx, wire.RequestTranscript, req)
	if err != nil {
		return reply, err
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return reply, fmt.Errorf("unmarshal transcript reply: %w", err)
	}
	return reply, nil
}

func (c *Client) Content(ctx context.Context, ref string) ([]byte, error) {
	var result []byte
	var totalSize int64
	var expectedSHA256 string
	offset := int64(0)

	for {
		var reply wire.ContentReply
		body, err := c.doRequest(ctx, wire.RequestContent, wire.ContentRequest{
			Ref:    ref,
			Offset: offset,
			Limit:  wire.MaxContentPageBytes,
		})
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &reply); err != nil {
			return nil, fmt.Errorf("unmarshal content reply: %w", err)
		}

		if totalSize == 0 {
			totalSize = reply.Total
			expectedSHA256 = reply.SHA256
		} else {
			if reply.Total != totalSize || reply.SHA256 != expectedSHA256 {
				return nil, fmt.Errorf("content: total or sha256 changed between pages")
			}
		}

		if reply.Total == 0 {
			break
		}

		decoded, err := wire.DecodePage(reply.Data)
		if err != nil {
			return nil, fmt.Errorf("decode content page: %w", err)
		}

		if len(decoded) == 0 && offset < totalSize {
			return nil, fmt.Errorf("content: received zero-length page before reaching total")
		}

		result = append(result, decoded...)
		offset += int64(len(decoded))

		if offset >= totalSize {
			break
		}
	}

	if len(result) != int(totalSize) {
		return nil, fmt.Errorf("content: expected %d bytes, got %d", totalSize, len(result))
	}

	hash := sha256.Sum256(result)
	if hex.EncodeToString(hash[:]) != expectedSHA256 {
		return nil, fmt.Errorf("content: sha256 mismatch")
	}

	return result, nil
}

func (c *Client) Subscribe(ctx context.Context, sinceSeq int64) (int64, error) {
	c.eventMu.Lock()
	if c.accepting {
		c.eventMu.Unlock()
		return 0, errors.New("already subscribed")
	}
	// Events may follow the reply immediately; start collecting before the
	// request goes out so none are dropped.
	c.accepting = true
	c.events = nil
	c.lastSeq = sinceSeq
	c.eventMu.Unlock()

	var reply wire.SubscribeReply
	body, err := c.doRequest(ctx, wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: sinceSeq})
	if err == nil {
		if err = json.Unmarshal(body, &reply); err != nil {
			err = fmt.Errorf("unmarshal subscribe reply: %w", err)
		}
	}
	if err != nil {
		c.stopEvents()
		return 0, err
	}
	return reply.LiveFromSeq, nil
}

func (c *Client) stopEvents() {
	c.eventMu.Lock()
	c.accepting = false
	c.events = nil
	c.eventMu.Unlock()
	c.wakeEvents()
}

func (c *Client) Unsubscribe(ctx context.Context) error {
	c.eventMu.Lock()
	subscribed := c.accepting
	c.eventMu.Unlock()
	if !subscribed {
		return errors.New("not subscribed")
	}

	if _, err := c.doRequest(ctx, wire.RequestUnsubscribe, wire.UnsubscribeRequest{}); err != nil {
		return err
	}
	c.stopEvents()
	return nil
}

func (c *Client) NextEvent(ctx context.Context) (wire.Frame, error) {
	for {
		c.eventMu.Lock()
		if len(c.events) > 0 {
			frame := c.events[0]
			c.events[0] = wire.Frame{}
			c.events = c.events[1:]
			more := len(c.events) > 0
			if frame.Type != "error" {
				c.lastSeq = frame.Seq
			}
			c.eventMu.Unlock()
			if more {
				c.wakeEvents()
			}
			if frame.Type == "error" {
				var errBody wire.Error
				if err := json.Unmarshal(frame.Body, &errBody); err != nil {
					return wire.Frame{}, fmt.Errorf("unmarshal error frame: %w", err)
				}
				return wire.Frame{}, &errBody
			}
			return frame, nil
		}
		done := c.eventsDone
		accepting := c.accepting
		c.eventMu.Unlock()

		if done != nil {
			return wire.Frame{}, done
		}
		if !accepting {
			return wire.Frame{}, errors.New("not subscribed")
		}

		select {
		case <-c.eventWake:
		case <-ctx.Done():
			return wire.Frame{}, ctx.Err()
		}
	}
}

func (c *Client) LastSeq() int64 {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	return c.lastSeq
}

func (c *Client) nextID() string {
	return fmt.Sprintf("%d", atomic.AddUint64(&c.idCounter, 1))
}

func (c *Client) doRequest(ctx context.Context, typ string, body any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	id := c.nextID()
	req, err := wire.NewRequest(id, typ, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	ch := make(chan result, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()

	c.writeMu.Lock()
	err = c.writer.Write(req)
	c.writeMu.Unlock()
	if err != nil {
		wrapped := fmt.Errorf("%w: write request: %v", ErrClosed, err)
		c.shutdown(wrapped)
		// shutdown completed our entry (or readLoop did); take its result.
		return nil, (<-ch).err
	}

	select {
	case res := <-ch:
		return res.body, res.err
	case <-ctx.Done():
		c.mu.Lock()
		_, still := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if !still {
			// Completed concurrently; the buffered result is ours to drop.
			<-ch
		}
		return nil, ctx.Err()
	}
}

// complete hands res to the request registered under id, if any.
func (c *Client) complete(id string, res result) {
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		ch <- res
	}
}

func (c *Client) readLoop() {
	for {
		frame, err := c.reader.Read()
		if err != nil {
			c.shutdown(fmt.Errorf("%w: %v", ErrClosed, err))
			return
		}

		switch frame.Type {
		case "event":
			c.pushEvent(frame)
		case "reply":
			c.complete(frame.ID, result{body: frame.Body})
		case "error":
			var errBody wire.Error
			uerr := json.Unmarshal(frame.Body, &errBody)
			if frame.ID != "" {
				if uerr != nil {
					c.complete(frame.ID, result{err: uerr})
				} else {
					c.complete(frame.ID, result{err: &errBody})
				}
				continue
			}
			// Connection-level notice: every waiting request and NextEvent.
			c.mu.Lock()
			pending := c.pending
			c.pending = make(map[string]chan result)
			c.mu.Unlock()
			for _, ch := range pending {
				ch <- result{err: &errBody}
			}
			c.pushEvent(frame)
		}
	}
}

func (c *Client) pushEvent(frame wire.Frame) {
	c.eventMu.Lock()
	if c.accepting {
		c.events = append(c.events, frame)
	}
	c.eventMu.Unlock()
	c.wakeEvents()
}
