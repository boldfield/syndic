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
	"syscall"
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
	wg       sync.WaitGroup
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

// Serve accepts connections and handles requests until ctx is done or the
// engine is stopping. Every open connection is sent error{code: "stopping"}
// when the engine stops, and is closed either way before Serve returns.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if listener == nil {
		return errors.New("server not listening")
	}

	stopped := make(chan bool, 1)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		select {
		case <-s.engine.Stopping():
			stopped <- true
		case <-ctx.Done():
			stopped <- false
		case <-quit:
			return
		}
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case engineStopped := <-stopped:
				if engineStopped {
					s.notifyStopping()
				}
				s.closeConnections()
				s.wg.Wait()
				return nil
			default:
			}
			s.closeConnections()
			s.wg.Wait()
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept connection: %w", err)
		}

		c := &connection{server: s, conn: conn}
		s.mu.Lock()
		s.conns[c] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			c.handle()
		}()
	}
}

func (s *Server) snapshotConnections() []*connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	conns := make([]*connection, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	return conns
}

// notifyStopping tells every connection the engine is stopping.
func (s *Server) notifyStopping() {
	for _, c := range s.snapshotConnections() {
		// A client that is not reading must not hold up shutdown.
		_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
		c.logSend(c.sendError("", &wire.Error{Code: wire.ErrorCodeStopping}))
	}
}

func (s *Server) closeConnections() {
	for _, c := range s.snapshotConnections() {
		_ = c.conn.Close()
	}
}

// Close closes the listener and all connections.
func (s *Server) Close() error {
	s.mu.Lock()
	listener := s.listener
	s.listener = nil
	s.mu.Unlock()

	var err error
	if listener != nil {
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			err = closeErr
		}
	}
	s.closeConnections()
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

func (c *connection) handle() {
	ctx, cancel := context.WithCancel(context.Background())
	var requests sync.WaitGroup
	defer func() {
		cancel()
		c.closeSubscription(nil)
		_ = c.conn.Close()
		requests.Wait()
		c.server.removeConnection(c)
	}()

	c.reader = wire.NewReader(c.conn)
	c.writer = wire.NewWriter(c.conn)
	if c.server.frameObserver != nil {
		c.writer.SetObserver(c.server.frameObserver)
	}

	if !c.handshake() {
		return
	}

	for {
		frame, err := c.reader.Read()
		if err != nil {
			var syntaxErr *json.SyntaxError
			var typeErr *json.UnmarshalTypeError
			switch {
			case errors.Is(err, wire.ErrFrameTooLarge):
				c.rejectOversized()
				return
			case errors.As(err, &syntaxErr), errors.As(err, &typeErr):
				c.logSend(c.sendError("", &wire.Error{Code: wire.ErrorCodeBadRequest}))
				continue
			}
			return
		}

		// subscribe and unsubscribe touch connection state, so they run in
		// read order; the event stream itself runs on its own goroutine.
		switch frame.Type {
		case wire.RequestSubscribe:
			c.handleSubscribe(ctx, &requests, frame)
		case wire.RequestUnsubscribe:
			c.handleUnsubscribe(frame)
		default:
			requests.Add(1)
			go func() {
				defer requests.Done()
				c.handleRequest(frame)
			}()
		}
	}
}

// handshake reads the first frame and answers it. It reports whether the
// connection may go on.
func (c *connection) handshake() bool {
	frame, err := c.reader.Read()
	if err != nil {
		if errors.Is(err, wire.ErrFrameTooLarge) {
			c.rejectOversized()
		}
		return false
	}
	if frame.Type != wire.RequestHello {
		c.logSend(c.sendError(frame.ID, &wire.Error{Code: wire.ErrorCodeProtocol}))
		return false
	}
	var req wire.HelloRequest
	if err := json.Unmarshal(frame.Body, &req); err != nil || req.Protocol != wire.ProtocolVersion {
		c.logSend(c.sendError(frame.ID, &wire.Error{Code: wire.ErrorCodeProtocol}))
		return false
	}
	if err := c.sendReply(frame.ID, c.server.engine.Hello()); err != nil {
		c.logSend(err)
		return false
	}
	return true
}

// rejectOversized answers a frame over the limit. The rest of the oversized
// line is discarded for a moment first so the close does not reset the
// connection under the error frame.
func (c *connection) rejectOversized() {
	c.logSend(c.sendError("", &wire.Error{Code: wire.ErrorCodeFrame}))
	_ = c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, io.LimitReader(c.conn, 16<<20))
}

func (c *connection) handleRequest(frame wire.Frame) {
	switch frame.Type {
	case wire.RequestStatus:
		status, err := c.server.engine.Status()
		c.respond(frame.ID, "status", status, err)
	case wire.RequestSubmit:
		var req wire.SubmitRequest
		if !c.decode(frame, &req, "submission_id", "input") {
			return
		}
		reply, err := c.server.engine.Submit(req)
		c.respond(frame.ID, "submit", reply, err)
	case wire.RequestCancel:
		var req wire.CancelRequest
		if !c.decode(frame, &req, "turn_id") {
			return
		}
		c.respond(frame.ID, "cancel", wire.CancelReply{Accepted: true}, c.server.engine.Cancel(req.TurnID))
	case wire.RequestOverride:
		var req wire.OverrideRequest
		if !c.decode(frame, &req, "turn_id", "reason") {
			return
		}
		c.respond(frame.ID, "override", wire.OverrideReply{State: "overridden"}, c.server.engine.Override(req.TurnID, req.Reason))
	case wire.RequestRelease:
		var req wire.ReleaseRequest
		if !c.decode(frame, &req, "pool", "reason") {
			return
		}
		reply, err := c.server.engine.Release(req)
		c.respond(frame.ID, "release", reply, err)
	case wire.RequestTurn:
		var req wire.TurnRequest
		if !c.decode(frame, &req, "turn_id") {
			return
		}
		reply, err := buildTurnRecord(c.server.store, req.TurnID)
		c.respond(frame.ID, "turn", reply, err)
	case wire.RequestTranscript:
		var req wire.TranscriptRequest
		if !c.decode(frame, &req, "turn_id") {
			return
		}
		limit := maxTranscriptLimit
		if req.Limit != nil && *req.Limit > 0 && *req.Limit < limit {
			limit = *req.Limit
		}
		reply, err := buildTranscript(c.server.store, req.TurnID, req.Cursor, limit)
		c.respond(frame.ID, "transcript", reply, err)
	case wire.RequestContent:
		var req wire.ContentRequest
		if !c.decode(frame, &req, "ref", "offset", "limit") {
			return
		}
		if req.Offset < 0 || req.Limit <= 0 {
			c.logSend(c.sendError(frame.ID, &wire.Error{Code: wire.ErrorCodeBadRequest}))
			return
		}
		limit := min(req.Limit, wire.MaxContentPageBytes)
		reply, err := getContent(c.server.store, req.Ref, req.Offset, limit)
		c.respond(frame.ID, "content", reply, err)
	default:
		c.logSend(c.sendError(frame.ID, &wire.Error{Code: wire.ErrorCodeBadRequest}))
	}
}

const maxTranscriptLimit = 200

// decode unmarshals the request body into req and checks that every named
// member is present. A missing member or a body that does not decode is
// answered with bad_request and reported as false.
func (c *connection) decode(frame wire.Frame, req any, required ...string) bool {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(frame.Body, &members); err != nil {
		c.badRequest(frame.ID, errors.New("body is not an object"))
		return false
	}
	for _, name := range required {
		if v, ok := members[name]; !ok || string(v) == "null" {
			c.badRequest(frame.ID, fmt.Errorf("missing %s", name))
			return false
		}
	}
	if err := json.Unmarshal(frame.Body, req); err != nil {
		c.badRequest(frame.ID, err)
		return false
	}
	return true
}

func (c *connection) badRequest(id string, cause error) {
	detail := cause.Error()
	c.logSend(c.sendError(id, &wire.Error{Code: wire.ErrorCodeBadRequest, Detail: &detail}))
}

// respond sends body as the reply to id, or the error err maps to.
func (c *connection) respond(id, op string, body any, err error) {
	if err != nil {
		var wireErr *wire.Error
		switch {
		case errors.As(err, &wireErr):
			c.logSend(c.sendError(id, wireErr))
		case errors.Is(err, journal.ErrTurnNotFound), errors.Is(err, journal.ErrNoSuchContent):
			c.logSend(c.sendError(id, &wire.Error{Code: "not_found"}))
		case errors.Is(err, journal.ErrOutOfRange):
			c.logSend(c.sendError(id, &wire.Error{Code: wire.ErrorCodeBadRequest}))
		default:
			fmt.Fprintf(os.Stderr, "%s: %v\n", op, err)
			c.logSend(c.sendError(id, &wire.Error{Code: wire.ErrorCodeInternal, Detail: ptrStr(err.Error())}))
		}
		return
	}
	c.logSend(c.sendReply(id, body))
}

func (c *connection) handleSubscribe(ctx context.Context, requests *sync.WaitGroup, frame wire.Frame) {
	var req wire.SubscribeRequest
	if !c.decode(frame, &req, "since_seq") {
		return
	}

	// A second subscribe replaces the first.
	c.closeSubscription(nil)

	sub, liveFromSeq, err := c.server.hub.Subscribe(req.SinceSeq)
	if err != nil {
		c.respond(frame.ID, "subscribe", nil, err)
		return
	}

	c.subMu.Lock()
	c.sub = sub
	c.subMu.Unlock()

	if err := c.sendReply(frame.ID, wire.SubscribeReply{OK: true, LiveFromSeq: liveFromSeq}); err != nil {
		c.logSend(err)
		c.closeSubscription(sub)
		return
	}

	requests.Add(1)
	go func() {
		defer requests.Done()
		c.forwardSubscription(ctx, sub)
	}()
}

// closeSubscription closes sub, or the current subscription when sub is nil,
// and forgets it only if it is still the current one, so a goroutine for a
// replaced subscription cannot tear down its replacement.
func (c *connection) closeSubscription(sub *hub.Subscription) {
	c.subMu.Lock()
	if sub == nil {
		sub = c.sub
	}
	if sub == c.sub {
		c.sub = nil
	}
	c.subMu.Unlock()
	if sub != nil {
		sub.Close()
	}
}

func (c *connection) forwardSubscription(ctx context.Context, sub *hub.Subscription) {
	defer c.closeSubscription(sub)
	for {
		event, err := sub.Next(ctx)
		if err != nil {
			var lagging *hub.LaggingError
			if errors.As(err, &lagging) {
				c.logSend(c.sendError("", &wire.Error{Code: wire.ErrorCodeLagging, LastSeq: &lagging.LastSeq}))
				_ = c.conn.Close()
			} else if !errors.Is(err, hub.ErrClosed) && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "subscription: %v\n", err)
			}
			return
		}
		if err := c.sendFrame(wire.NewEvent(event.Seq, event.Kind, event.At, event.Data)); err != nil {
			c.logSend(err)
			return
		}
	}
}

func (c *connection) handleUnsubscribe(frame wire.Frame) {
	c.closeSubscription(nil)
	c.logSend(c.sendReply(frame.ID, wire.UnsubscribeReply{OK: true}))
}

func (c *connection) sendReply(id string, body any) error {
	frame, err := wire.NewReply(id, body)
	if err != nil {
		return err
	}
	if err := c.sendFrame(frame); err != nil {
		if errors.Is(err, wire.ErrFrameTooLarge) {
			return c.sendError(id, &wire.Error{Code: wire.ErrorCodeFrame})
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

// logSend reports a failed write. A closed connection is the client leaving
// and is not worth a line.
func (c *connection) logSend(err error) {
	if err == nil || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
		return
	}
	fmt.Fprintf(os.Stderr, "server: send: %v\n", err)
}

func ptrStr(s string) *string {
	return &s
}
