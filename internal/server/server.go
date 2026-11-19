package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/boldfield/syndic/internal/hub"
	"github.com/boldfield/syndic/internal/journal"
	"github.com/boldfield/syndic/internal/lockfile"
	"github.com/boldfield/syndic/internal/wire"
)

// Engine is the interface the server calls to handle requests.
type Engine interface {
	Hello() wire.HelloReply
	Status() (wire.StatusReply, error)
	Submit(wire.SubmitRequest) (wire.SubmitReply, error)
	Cancel(turnID string) error
	Override(turnID, reason string) error
	Release(wire.ReleaseRequest) (wire.ReleaseReply, error)
	Stopping() <-chan struct{}
}

// Options configures a Server.
type Options struct {
	Paths         lockfile.Paths
	Engine        Engine
	Store         *journal.Store
	Hub           *hub.Hub
	FrameObserver func(encodedLen int) // optional test hook
}

// Server serves the control socket protocol.
type Server struct {
	paths         lockfile.Paths
	engine        Engine
	store         *journal.Store
	hub           *hub.Hub
	frameObserver func(encodedLen int)

	mu       sync.Mutex
	listener net.Listener
	conns    map[*connection]bool
	stopping atomic.Bool
}

// New creates a new Server.
func New(o Options) *Server {
	return &Server{
		paths:         o.Paths,
		engine:        o.Engine,
		store:         o.Store,
		hub:           o.Hub,
		frameObserver: o.FrameObserver,
		conns:         make(map[*connection]bool),
	}
}

// Listen removes a stale socket, listens on the socket path, and sets mode 0600.
// It assumes the caller holds the engine lock.
func (s *Server) Listen() error {
	if err := lockfile.RemoveStaleSocket(s.paths); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", s.paths.Socket)
	if err != nil {
		return fmt.Errorf("listen on socket: %w", err)
	}

	if err := os.Chmod(s.paths.Socket, 0600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	return nil
}

// Serve accepts connections and handles requests until ctx is done or the engine is stopping.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	if s.listener == nil {
		s.mu.Unlock()
		return fmt.Errorf("server not listening")
	}
	listener := s.listener
	s.mu.Unlock()

	engineStopping := s.engine.Stopping()
	stopped := false

	for {
		select {
		case <-ctx.Done():
			s.sendStoppingToAll()
			_ = listener.Close()
			return ctx.Err()
		case <-engineStopping:
			s.sendStoppingToAll()
			_ = listener.Close()
			s.stopping.Store(true)
			return nil
		default:
		}

		tcpListener := listener.(*net.UnixListener)
		if err := tcpListener.SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			s.sendStoppingToAll()
			return fmt.Errorf("set deadline: %w", err)
		}

		conn, err := listener.Accept()
		if err != nil {
			netErr, ok := err.(net.Error)
			if ok && netErr.Timeout() {
				if !stopped {
					select {
					case <-ctx.Done():
						s.sendStoppingToAll()
						_ = listener.Close()
						return ctx.Err()
					case <-engineStopping:
						s.sendStoppingToAll()
						_ = listener.Close()
						s.stopping.Store(true)
						return nil
					default:
					}
				}
				continue
			}
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("accept connection: %w", err)
		}

		c := &connection{
			server: s,
			conn:   conn,
		}
		s.mu.Lock()
		s.conns[c] = true
		s.mu.Unlock()

		go c.handle(ctx)
	}
}

func (s *Server) sendStoppingToAll() {
	s.mu.Lock()
	conns := make([]*connection, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	for _, c := range conns {
		_ = c.sendError("", &wire.Error{Code: "stopping"})
		_ = c.conn.Close()
	}
}

// Close closes the listener and all connections.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var err error
	if s.listener != nil {
		if closeErr := s.listener.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		s.listener = nil
	}

	for c := range s.conns {
		if closeErr := c.conn.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	s.conns = make(map[*connection]bool)

	return err
}

func (s *Server) removeConnection(c *connection) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

// connection handles one client connection.
type connection struct {
	server  *Server
	conn    net.Conn
	reader  *wire.Reader
	writer  *wire.Writer
	writeMu sync.Mutex
	subMu   sync.Mutex
	sub     *hub.Subscription
}

func (c *connection) handle(ctx context.Context) {
	defer func() {
		c.subMu.Lock()
		if c.sub != nil {
			c.sub.Close()
		}
		c.subMu.Unlock()
		_ = c.conn.Close()
		c.server.removeConnection(c)
	}()

	c.reader = wire.NewReader(c.conn)
	c.writer = wire.NewWriter(c.conn)
	if c.server.frameObserver != nil {
		c.writer.SetObserver(c.server.frameObserver)
	}

	// First frame must be hello
	frame, err := c.reader.Read()
	if err != nil {
		if err == wire.ErrFrameTooLarge {
			_ = c.sendError(frame.ID, &wire.Error{Code: "frame"})
		} else if err != io.EOF {
			fmt.Fprintf(os.Stderr, "read hello: %v\n", err)
		}
		return
	}

	if frame.Type != "hello" {
		if err := c.sendError(frame.ID, &wire.Error{Code: "protocol"}); err != nil {
			fmt.Fprintf(os.Stderr, "send protocol error: %v\n", err)
		}
		return
	}

	var helloReq wire.HelloRequest
	if err := json.Unmarshal(frame.Body, &helloReq); err != nil {
		if err := c.sendError(frame.ID, &wire.Error{Code: "protocol"}); err != nil {
			fmt.Fprintf(os.Stderr, "send protocol error: %v\n", err)
		}
		return
	}

	if helloReq.Protocol != wire.ProtocolVersion {
		if err := c.sendError(frame.ID, &wire.Error{Code: "protocol"}); err != nil {
			fmt.Fprintf(os.Stderr, "send protocol error: %v\n", err)
		}
		return
	}

	helloReply := c.server.engine.Hello()
	if err := c.sendReply(frame.ID, helloReply); err != nil {
		fmt.Fprintf(os.Stderr, "send hello reply: %v\n", err)
		return
	}

	// Handle subsequent requests
	for {
		frame, err := c.reader.Read()
		if err != nil {
			if err == wire.ErrFrameTooLarge {
				_ = c.sendError(frame.ID, &wire.Error{Code: "frame"})
				return
			} else if err != io.EOF {
				fmt.Fprintf(os.Stderr, "read frame: %v\n", err)
			}
			return
		}

		// Handle subscribe requests specially to manage subscription state
		switch frame.Type {
		case wire.RequestSubscribe:
			c.handleSubscribe(ctx, frame)
		case wire.RequestUnsubscribe:
			c.handleUnsubscribe(frame)
		default:
			// Run other requests concurrently
			go c.handleRequest(frame)
		}
	}
}

func (c *connection) handleRequest(frame wire.Frame) {
	switch frame.Type {
	case wire.RequestStatus:
		c.handleStatus(frame)
	case wire.RequestSubmit:
		c.handleSubmit(frame)
	case wire.RequestCancel:
		c.handleCancel(frame)
	case wire.RequestOverride:
		c.handleOverride(frame)
	case wire.RequestRelease:
		c.handleRelease(frame)
	case wire.RequestTurn:
		c.handleTurn(frame)
	case wire.RequestTranscript:
		c.handleTranscript(frame)
	case wire.RequestContent:
		c.handleContent(frame)
	default:
		if err := c.sendError(frame.ID, &wire.Error{Code: wire.ErrorCodeBadRequest}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
	}
}

func (c *connection) handleStatus(frame wire.Frame) {
	status, err := c.server.engine.Status()
	if err != nil {
		if wireErr, ok := err.(*wire.Error); ok {
			if err := c.sendError(frame.ID, wireErr); err != nil {
				fmt.Fprintf(os.Stderr, "send status error: %v\n", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "status: %v\n", err)
			if err := c.sendError(frame.ID, &wire.Error{Code: "internal", Detail: ptrStr(err.Error())}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		}
		return
	}

	if err := c.sendReply(frame.ID, status); err != nil {
		fmt.Fprintf(os.Stderr, "send status reply: %v\n", err)
	}
}

func (c *connection) handleSubmit(frame wire.Frame) {
	var req wire.SubmitRequest
	if err := json.Unmarshal(frame.Body, &req); err != nil {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	reply, err := c.server.engine.Submit(req)
	if err != nil {
		if wireErr, ok := err.(*wire.Error); ok {
			if err := c.sendError(frame.ID, wireErr); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "submit: %v\n", err)
			if err := c.sendError(frame.ID, &wire.Error{Code: "internal", Detail: ptrStr(err.Error())}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		}
		return
	}

	if err := c.sendReply(frame.ID, reply); err != nil {
		fmt.Fprintf(os.Stderr, "send reply: %v\n", err)
	}
}

func (c *connection) handleCancel(frame wire.Frame) {
	var req wire.CancelRequest
	if err := json.Unmarshal(frame.Body, &req); err != nil {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	if req.TurnID == "" {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	err := c.server.engine.Cancel(req.TurnID)
	if err != nil {
		if wireErr, ok := err.(*wire.Error); ok {
			if err := c.sendError(frame.ID, wireErr); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "cancel: %v\n", err)
			if err := c.sendError(frame.ID, &wire.Error{Code: "internal", Detail: ptrStr(err.Error())}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		}
		return
	}

	if err := c.sendReply(frame.ID, wire.CancelReply{Accepted: true}); err != nil {
		fmt.Fprintf(os.Stderr, "send reply: %v\n", err)
	}
}

func (c *connection) handleOverride(frame wire.Frame) {
	var req wire.OverrideRequest
	if err := json.Unmarshal(frame.Body, &req); err != nil {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	if req.TurnID == "" || req.Reason == "" {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	err := c.server.engine.Override(req.TurnID, req.Reason)
	if err != nil {
		if wireErr, ok := err.(*wire.Error); ok {
			if err := c.sendError(frame.ID, wireErr); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "override: %v\n", err)
			if err := c.sendError(frame.ID, &wire.Error{Code: "internal", Detail: ptrStr(err.Error())}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		}
		return
	}

	if err := c.sendReply(frame.ID, wire.OverrideReply{State: "overridden"}); err != nil {
		fmt.Fprintf(os.Stderr, "send reply: %v\n", err)
	}
}

func (c *connection) handleRelease(frame wire.Frame) {
	var req wire.ReleaseRequest
	if err := json.Unmarshal(frame.Body, &req); err != nil {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	if req.Pool == "" || req.Reason == "" {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	reply, err := c.server.engine.Release(req)
	if err != nil {
		if wireErr, ok := err.(*wire.Error); ok {
			if err := c.sendError(frame.ID, wireErr); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "release: %v\n", err)
			if err := c.sendError(frame.ID, &wire.Error{Code: "internal", Detail: ptrStr(err.Error())}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		}
		return
	}

	if err := c.sendReply(frame.ID, reply); err != nil {
		fmt.Fprintf(os.Stderr, "send reply: %v\n", err)
	}
}

func (c *connection) handleTurn(frame wire.Frame) {
	var req wire.TurnRequest
	if err := json.Unmarshal(frame.Body, &req); err != nil {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	if req.TurnID == "" {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	turnReply, err := buildTurnRecord(c.server.store, req.TurnID)
	if err != nil {
		if errors.Is(err, journal.ErrTurnNotFound) {
			if err := c.sendError(frame.ID, &wire.Error{Code: "not_found"}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "build turn: %v\n", err)
			if err := c.sendError(frame.ID, &wire.Error{Code: "internal", Detail: ptrStr(err.Error())}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		}
		return
	}

	if err := c.sendReply(frame.ID, turnReply); err != nil {
		fmt.Fprintf(os.Stderr, "send reply: %v\n", err)
	}
}

func (c *connection) handleTranscript(frame wire.Frame) {
	var req wire.TranscriptRequest
	if err := json.Unmarshal(frame.Body, &req); err != nil {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	if req.TurnID == "" {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	limit := 200
	if req.Limit != nil && *req.Limit > 0 && *req.Limit < 200 {
		limit = *req.Limit
	}

	transcript, err := buildTranscript(c.server.store, req.TurnID, req.Cursor, limit)
	if err != nil {
		if errors.Is(err, journal.ErrTurnNotFound) {
			if err := c.sendError(frame.ID, &wire.Error{Code: "not_found"}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "build transcript: %v\n", err)
			if err := c.sendError(frame.ID, &wire.Error{Code: "internal", Detail: ptrStr(err.Error())}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		}
		return
	}

	if err := c.sendReply(frame.ID, transcript); err != nil {
		fmt.Fprintf(os.Stderr, "send reply: %v\n", err)
	}
}

func (c *connection) handleContent(frame wire.Frame) {
	var req wire.ContentRequest
	if err := json.Unmarshal(frame.Body, &req); err != nil {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	if req.Ref == "" {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	limit := req.Limit
	if limit > wire.MaxContentPageBytes {
		limit = wire.MaxContentPageBytes
	}

	content, err := getContent(c.server.store, req.Ref, req.Offset, limit)
	if err != nil {
		if errors.Is(err, journal.ErrNoSuchContent) {
			if err := c.sendError(frame.ID, &wire.Error{Code: "not_found"}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		} else if errors.Is(err, journal.ErrOutOfRange) {
			if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "get content: %v\n", err)
			if err := c.sendError(frame.ID, &wire.Error{Code: "internal", Detail: ptrStr(err.Error())}); err != nil {
				fmt.Fprintf(os.Stderr, "send error: %v\n", err)
			}
		}
		return
	}

	if err := c.sendReply(frame.ID, content); err != nil {
		fmt.Fprintf(os.Stderr, "send reply: %v\n", err)
	}
}

func (c *connection) handleSubscribe(ctx context.Context, frame wire.Frame) {
	var req wire.SubscribeRequest
	if err := json.Unmarshal(frame.Body, &req); err != nil {
		if err := c.sendError(frame.ID, &wire.Error{Code: "bad_request"}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	c.subMu.Lock()
	if c.sub != nil {
		c.sub.Close()
	}
	c.subMu.Unlock()

	sub, liveFromSeq, err := c.server.hub.Subscribe(req.SinceSeq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "subscribe: %v\n", err)
		if err := c.sendError(frame.ID, &wire.Error{Code: "internal", Detail: ptrStr(err.Error())}); err != nil {
			fmt.Fprintf(os.Stderr, "send error: %v\n", err)
		}
		return
	}

	c.subMu.Lock()
	c.sub = sub
	c.subMu.Unlock()

	if err := c.sendReply(frame.ID, wire.SubscribeReply{OK: true, LiveFromSeq: liveFromSeq}); err != nil {
		fmt.Fprintf(os.Stderr, "send subscribe reply: %v\n", err)
		c.subMu.Lock()
		if c.sub != nil {
			c.sub.Close()
			c.sub = nil
		}
		c.subMu.Unlock()
		return
	}

	go c.forwardSubscription(ctx)
}

func (c *connection) forwardSubscription(ctx context.Context) {
	c.subMu.Lock()
	sub := c.sub
	c.subMu.Unlock()
	if sub == nil {
		return
	}

	for {
		event, err := sub.Next(ctx)
		if err != nil {
			if laggingErr, ok := err.(*hub.LaggingError); ok {
				_ = c.sendError("", &wire.Error{Code: "lagging", LastSeq: &laggingErr.LastSeq})
				c.subMu.Lock()
				if c.sub == sub {
					c.sub = nil
				}
				c.subMu.Unlock()
				sub.Close()
				_ = c.conn.Close()
				return
			}
			if err == hub.ErrClosed || err == context.Canceled || err == ctx.Err() {
				c.subMu.Lock()
				if c.sub == sub {
					c.sub = nil
				}
				c.subMu.Unlock()
				sub.Close()
				return
			}
			fmt.Fprintf(os.Stderr, "get event: %v\n", err)
			c.subMu.Lock()
			if c.sub == sub {
				c.sub = nil
			}
			c.subMu.Unlock()
			sub.Close()
			return
		}

		eventFrame := wire.NewEvent(event.Seq, event.Kind, event.At, event.Data)
		if err := c.sendFrame(eventFrame); err != nil {
			fmt.Fprintf(os.Stderr, "send event: %v\n", err)
			c.subMu.Lock()
			if c.sub == sub {
				c.sub = nil
			}
			c.subMu.Unlock()
			sub.Close()
			return
		}
	}
}

func (c *connection) handleUnsubscribe(frame wire.Frame) {
	c.subMu.Lock()
	if c.sub != nil {
		c.sub.Close()
		c.sub = nil
	}
	c.subMu.Unlock()

	if err := c.sendReply(frame.ID, wire.UnsubscribeReply{OK: true}); err != nil {
		fmt.Fprintf(os.Stderr, "send reply: %v\n", err)
	}
}

func (c *connection) sendReply(id string, body any) error {
	frame, err := wire.NewReply(id, body)
	if err != nil {
		return err
	}
	if err := c.sendFrame(frame); err != nil {
		if err == wire.ErrFrameTooLarge {
			// If reply is too large, send frame error instead
			return c.sendError(id, &wire.Error{Code: "frame"})
		}
		return err
	}
	return nil
}

func (c *connection) sendError(id string, e *wire.Error) error {
	frame, err := wire.NewError(id, e)
	if err != nil {
		return err
	}
	return c.sendFrame(frame)
}

func (c *connection) sendFrame(frame wire.Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writer.Write(frame)
}

func ptrStr(s string) *string {
	return &s
}
