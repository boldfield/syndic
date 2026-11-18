package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boldfield/syndic/internal/wire"
)

var ErrClosed = errors.New("client: connection closed")

type Client struct {
	mu       sync.Mutex
	conn     net.Conn
	reader   *wire.Reader
	writer   *wire.Writer
	closed   bool
	closeErr error

	hello *wire.HelloReply

	// Request/reply tracking
	pendingMu sync.Mutex
	pending   map[string]*pendingRequest

	// Event stream
	eventMu     sync.Mutex
	subscribed  bool
	lastSeq     int64
	eventQueue  chan wire.Frame
	eventCtx    context.Context
	eventCancel context.CancelFunc
	eventBuffer []wire.Frame

	// Frame ID generation
	idCounter uint64
}

type pendingRequest struct {
	done chan struct{}
	err  error
	body []byte
}

func Dial(ctx context.Context, socketPath, clientName, clientVersion string) (*Client, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	c := &Client{
		conn:       conn,
		reader:     wire.NewReader(conn),
		writer:     wire.NewWriter(conn),
		pending:    make(map[string]*pendingRequest),
		eventQueue: make(chan wire.Frame, 1024),
	}

	// Send hello
	req, err := wire.NewRequest("", wire.RequestHello, wire.HelloRequest{
		Client:        clientName,
		ClientVersion: clientVersion,
		Protocol:      wire.ProtocolVersion,
	})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("hello request: %w", err)
	}

	if err := c.writer.Write(req); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write hello: %w", err)
	}

	// Set a deadline from ctx for reading the hello reply
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(deadline)
		defer conn.SetReadDeadline(time.Time{})
	}

	// Read hello reply or error
	frame, err := c.reader.Read()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read hello reply: %w", err)
	}

	if frame.Type == "error" {
		_ = conn.Close()
		var errBody wire.Error
		if err := json.Unmarshal(frame.Body, &errBody); err != nil {
			return nil, fmt.Errorf("unmarshal error: %w", err)
		}
		return nil, &errBody
	}

	if frame.Type != "reply" {
		_ = conn.Close()
		return nil, fmt.Errorf("expected reply, got %s", frame.Type)
	}

	var hello wire.HelloReply
	if err := json.Unmarshal(frame.Body, &hello); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("unmarshal hello reply: %w", err)
	}

	c.hello = &hello

	// Start reader goroutine
	go c.readLoop()

	return c, nil
}

func (c *Client) Hello() wire.HelloReply {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hello != nil {
		return *c.hello
	}
	return wire.HelloReply{}
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return c.closeErr
	}

	c.closed = true
	if c.eventCancel != nil {
		c.eventCancel()
	}

	err := c.conn.Close()
	c.closeErr = err

	// Wake up all pending requests
	c.pendingMu.Lock()
	for _, req := range c.pending {
		req.err = ErrClosed
		close(req.done)
	}
	c.pending = make(map[string]*pendingRequest)
	c.pendingMu.Unlock()

	if err != nil {
		return err
	}
	return nil
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
	actual := fmt.Sprintf("%x", hash)
	if actual != expectedSHA256 {
		return nil, fmt.Errorf("content: sha256 mismatch")
	}

	return result, nil
}

func (c *Client) Subscribe(ctx context.Context, sinceSeq int64) (int64, error) {
	c.eventMu.Lock()
	if c.subscribed {
		c.eventMu.Unlock()
		return 0, errors.New("already subscribed")
	}
	c.eventMu.Unlock()

	eventCtx, eventCancel := context.WithCancel(context.Background())

	var reply wire.SubscribeReply
	body, err := c.doRequest(ctx, wire.RequestSubscribe, wire.SubscribeRequest{SinceSeq: sinceSeq})
	if err != nil {
		eventCancel()
		return 0, err
	}

	if err := json.Unmarshal(body, &reply); err != nil {
		eventCancel()
		return 0, fmt.Errorf("unmarshal subscribe reply: %w", err)
	}

	c.eventMu.Lock()
	c.eventCtx = eventCtx
	c.eventCancel = eventCancel
	c.lastSeq = sinceSeq
	c.subscribed = true
	bufferedEvents := c.eventBuffer
	c.eventBuffer = nil
	c.eventMu.Unlock()

	// Flush buffered events to the queue
	for _, evt := range bufferedEvents {
		select {
		case c.eventQueue <- evt:
		case <-eventCtx.Done():
			return reply.LiveFromSeq, nil
		}
	}

	return reply.LiveFromSeq, nil
}

func (c *Client) Unsubscribe(ctx context.Context) error {
	c.eventMu.Lock()
	if !c.subscribed {
		c.eventMu.Unlock()
		return errors.New("not subscribed")
	}
	c.eventMu.Unlock()

	_, err := c.doRequest(ctx, wire.RequestUnsubscribe, wire.UnsubscribeRequest{})
	if err != nil {
		return err
	}

	c.eventMu.Lock()
	c.subscribed = false
	c.eventMu.Unlock()

	if c.eventCancel != nil {
		c.eventCancel()
	}

	return nil
}

func (c *Client) NextEvent(ctx context.Context) (wire.Frame, error) {
	c.eventMu.Lock()
	eventCtx := c.eventCtx
	c.eventMu.Unlock()

	if eventCtx == nil {
		return wire.Frame{}, errors.New("not subscribed")
	}

	select {
	case frame, ok := <-c.eventQueue:
		if !ok {
			return wire.Frame{}, ErrClosed
		}
		if frame.Type == "error" {
			var errBody wire.Error
			if err := json.Unmarshal(frame.Body, &errBody); err != nil {
				return wire.Frame{}, fmt.Errorf("unmarshal error frame: %w", err)
			}
			c.eventMu.Lock()
			c.lastSeq = frame.Seq
			c.eventMu.Unlock()
			return wire.Frame{}, &errBody
		}

		c.eventMu.Lock()
		c.lastSeq = frame.Seq
		c.eventMu.Unlock()
		return frame, nil

	case <-ctx.Done():
		return wire.Frame{}, ctx.Err()
	case <-eventCtx.Done():
		return wire.Frame{}, ErrClosed
	}
}

func (c *Client) LastSeq() int64 {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	return c.lastSeq
}

// Internal methods

func (c *Client) nextID() string {
	id := atomic.AddUint64(&c.idCounter, 1)
	return fmt.Sprintf("%d", id)
}

func (c *Client) doRequest(ctx context.Context, typ string, body any) ([]byte, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.mu.Unlock()

	id := c.nextID()
	req, err := wire.NewRequest(id, typ, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Register pending request
	pending := &pendingRequest{
		done: make(chan struct{}),
	}

	c.pendingMu.Lock()
	c.pending[id] = pending
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	// Send request
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	err = c.writer.Write(req)
	c.mu.Unlock()

	if err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Wait for reply
	select {
	case <-pending.done:
		if pending.err != nil {
			return nil, pending.err
		}
		return pending.body, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) readLoop() {
	for {
		frame, err := c.reader.Read()
		if err != nil {
			if err == io.EOF {
				err = ErrClosed
			}
			c.mu.Lock()
			c.closed = true
			c.mu.Unlock()

			// Notify all pending requests
			c.pendingMu.Lock()
			for _, req := range c.pending {
				req.err = err
				close(req.done)
			}
			c.pending = make(map[string]*pendingRequest)
			c.pendingMu.Unlock()

			// Close event queue
			close(c.eventQueue)
			return
		}

		if frame.Type == "event" {
			c.eventMu.Lock()
			subscribed := c.subscribed
			if !subscribed {
				c.eventBuffer = append(c.eventBuffer, frame)
				c.eventMu.Unlock()
			} else {
				eventCtx := c.eventCtx
				c.eventMu.Unlock()
				if eventCtx != nil {
					select {
					case c.eventQueue <- frame:
					case <-eventCtx.Done():
						return
					}
				}
			}
		} else if frame.Type == "reply" || frame.Type == "error" {
			if frame.Type == "error" && frame.ID == "" {
				// Error frame with no ID: deliver to NextEvent and all pending requests
				var errBody wire.Error
				_ = json.Unmarshal(frame.Body, &errBody)

				// Deliver to all pending requests
				c.pendingMu.Lock()
				for _, req := range c.pending {
					req.err = &errBody
					close(req.done)
				}
				c.pending = make(map[string]*pendingRequest)
				c.pendingMu.Unlock()

				// Deliver to NextEvent as well
				c.eventMu.Lock()
				subscribed := c.subscribed
				eventCtx := c.eventCtx
				c.eventMu.Unlock()
				if subscribed && eventCtx != nil {
					select {
					case c.eventQueue <- frame:
					case <-eventCtx.Done():
						return
					}
				}
			} else {
				// Match to pending request by ID
				c.pendingMu.Lock()
				pending, ok := c.pending[frame.ID]
				if ok {
					delete(c.pending, frame.ID)
				}
				c.pendingMu.Unlock()

				if ok {
					if frame.Type == "error" {
						var errBody wire.Error
						if err := json.Unmarshal(frame.Body, &errBody); err != nil {
							pending.err = err
						} else {
							pending.err = &errBody
						}
					} else {
						pending.body = frame.Body
					}
					close(pending.done)
				}
			}
		}
	}
}
